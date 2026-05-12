package rbacprovider

import (
	"context"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/multicluster-provider/apiexport"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
)

// runMulticluster wires the translator into a multicluster-runtime
// manager backed by kcp-dev/multicluster-provider/apiexport.
//
// Architecture:
//
//   - One mcmanager.Manager owns the fleet.
//   - apiexport.Provider, watching the configured APIExportEndpointSlice,
//     supplies the fleet: every logical cluster with an APIBinding to
//     the access VW's APIExport joins as a "cluster" in the
//     multicluster-runtime sense, addressable by its kcp logical
//     cluster name. This is the "opt-in indexing" behaviour the
//     design calls for — workspaces only show up in the graph if
//     they bind to the access VW's APIExport.
//   - Two mcbuilder controllers, one per RBAC kind, fan reconciles
//     into the shared Translator. Reconcile inputs carry ClusterName
//     directly, so we don't need to re-derive logical-cluster context
//     from annotations the way the single-shard wiring does.
//
// runMulticluster blocks on mgr.Start until ctx is cancelled. The
// graph is marked Ready once the manager's caches have synced; the
// SCAR handler returns 503 until then.
//
// kcp-side configuration: the access VW's system APIExport must
// export ClusterRoleBinding and RoleBinding from
// rbac.authorization.k8s.io/v1, otherwise the apiexport virtual
// workspace won't surface them. That is not something this code
// controls; it's a deployment concern.
func (p *Provider) runMulticluster(ctx context.Context, cfg *rest.Config, g *graph.Graph) error {
	if p.APIExportEndpointSlice == "" {
		return fmt.Errorf("APIExportEndpointSlice is required for multi-shard mode")
	}

	logger := log.FromContext(ctx).WithName("rbacprovider")

	sch := runtime.NewScheme()
	utilruntime.Must(scheme.AddToScheme(sch))
	utilruntime.Must(corev1alpha1.AddToScheme(sch))
	utilruntime.Must(tenancyv1alpha1.AddToScheme(sch))
	utilruntime.Must(apisv1alpha1.AddToScheme(sch))

	provider, err := apiexport.New(cfg, p.APIExportEndpointSlice, apiexport.Options{
		Scheme: sch,
		Log:    &logger,
	})
	if err != nil {
		return fmt.Errorf("construct apiexport provider: %w", err)
	}

	mgr, err := mcmanager.New(cfg, provider, manager.Options{
		Scheme: sch,
		Metrics: metricsserver.Options{BindAddress: "0"}, // disable; access-vw has its own HTTP server
	})
	if err != nil {
		return fmt.Errorf("construct multicluster manager: %w", err)
	}

	if err := registerRBACControllers(mgr, p.translator, p.endpointFor); err != nil {
		return fmt.Errorf("register controllers: %w", err)
	}

	// Mark the graph Ready once the manager has started its
	// runnables. Multicluster-runtime engages clusters
	// asynchronously, so there's no single "fleet synced" event we
	// can hook here; flipping Ready when the manager starts means
	// SCAR responses become non-503 once the manager is alive, with
	// the implicit caveat that very-early queries may see a
	// partially-populated graph as clusters get engaged. The
	// translator's idempotent Apply semantics make that safe — the
	// graph will converge — but consumers that need strict
	// completeness should poll Ready a couple of times.
	if err := mgr.GetLocalManager().Add(manager.RunnableFunc(func(ctx context.Context) error {
		g.SetReady()
		logger.Info("access graph marked ready (multicluster manager started)")
		<-ctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("register readiness runnable: %w", err)
	}

	return mgr.Start(ctx)
}

func registerRBACControllers(
	mgr mcmanager.Manager,
	t *Translator,
	endpointFor func(graph.LogicalCluster) string,
) error {
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("access-vw-clusterrolebinding").
		For(&rbacv1.ClusterRoleBinding{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			return reconcileCRB(ctx, mgr, t, endpointFor, req)
		})); err != nil {
		return fmt.Errorf("build CRB controller: %w", err)
	}

	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("access-vw-rolebinding").
		For(&rbacv1.RoleBinding{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			return reconcileRB(ctx, mgr, t, endpointFor, req)
		})); err != nil {
		return fmt.Errorf("build RB controller: %w", err)
	}

	return nil
}

func reconcileCRB(
	ctx context.Context,
	mgr mcmanager.Manager,
	t *Translator,
	endpointFor func(graph.LogicalCluster) string,
	req mcreconcile.Request,
) (ctrl.Result, error) {
	cluster := graph.LogicalCluster(req.ClusterName)

	cl, err := mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("get cluster %q: %w", req.ClusterName, err)
	}

	var crb rbacv1.ClusterRoleBinding
	if err := cl.GetClient().Get(ctx, req.NamespacedName, &crb); err != nil {
		if apierrors.IsNotFound(err) {
			t.RemoveClusterRoleBinding(req.Name, cluster)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("get ClusterRoleBinding: %w", err)
	}
	t.ApplyClusterRoleBinding(&crb, cluster, endpointFor(cluster))
	return reconcile.Result{}, nil
}

func reconcileRB(
	ctx context.Context,
	mgr mcmanager.Manager,
	t *Translator,
	endpointFor func(graph.LogicalCluster) string,
	req mcreconcile.Request,
) (ctrl.Result, error) {
	cluster := graph.LogicalCluster(req.ClusterName)

	cl, err := mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("get cluster %q: %w", req.ClusterName, err)
	}

	var rb rbacv1.RoleBinding
	if err := cl.GetClient().Get(ctx, req.NamespacedName, &rb); err != nil {
		if apierrors.IsNotFound(err) {
			t.RemoveRoleBinding(req.Namespace, req.Name, cluster)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("get RoleBinding: %w", err)
	}
	t.ApplyRoleBinding(&rb, cluster, endpointFor(cluster))
	return reconcile.Result{}, nil
}
