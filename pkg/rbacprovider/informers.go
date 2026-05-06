package rbacprovider

import (
	"context"
	"fmt"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
)

// defaultResyncPeriod is the informer relist interval. Resync is
// belt-and-braces against missed delete events; the translator's
// reference counting handles duplicate Adds idempotently.
const defaultResyncPeriod = 10 * time.Minute

// fallbackCluster is used as the LogicalCluster name when an object
// has no kcp.io/cluster annotation, e.g. when the provider is run
// against plain Kubernetes for development. With it set to a
// nonempty constant, single-cluster setups still produce useful
// graph state.
const fallbackCluster graph.LogicalCluster = "root"

// runInformers stands up CRB and RB informers against the supplied
// Kubernetes client, wires their events into the translator, waits
// for the initial cache sync, marks the graph Ready, and blocks
// until ctx is cancelled.
//
// Single-shard wiring: this function sees every binding visible to
// the supplied kubernetes.Interface and uses kcp's logicalcluster
// helper to extract per-binding cluster context from the
// kcp.io/cluster annotation. For multi-shard deployments, the
// natural follow-up is to swap this for an MCR-backed equivalent
// that fans events from N shards into the same translator; the
// translator API doesn't have to change.
func (p *Provider) runInformers(ctx context.Context, client kubernetes.Interface, g *graph.Graph) error {
	factory := informers.NewSharedInformerFactory(client, defaultResyncPeriod)

	crbInformer := factory.Rbac().V1().ClusterRoleBindings().Informer()
	rbInformer := factory.Rbac().V1().RoleBindings().Informer()

	if _, err := crbInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { p.onCRB(obj, false) },
		UpdateFunc: func(_, obj any) { p.onCRB(obj, false) },
		DeleteFunc: func(obj any) { p.onCRB(obj, true) },
	}); err != nil {
		return fmt.Errorf("register CRB handler: %w", err)
	}

	if _, err := rbInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { p.onRB(obj, false) },
		UpdateFunc: func(_, obj any) { p.onRB(obj, false) },
		DeleteFunc: func(obj any) { p.onRB(obj, true) },
	}); err != nil {
		return fmt.Errorf("register RB handler: %w", err)
	}

	factory.Start(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(),
		crbInformer.HasSynced,
		rbInformer.HasSynced,
	) {
		return fmt.Errorf("informer cache sync failed (context cancelled or watch error)")
	}

	g.SetReady()

	<-ctx.Done()
	return nil
}

// onCRB dispatches a ClusterRoleBinding informer event into the
// translator. On Delete events the runtime may deliver a
// DeletedFinalStateUnknown tombstone instead of the original object;
// we unwrap if so. Any object the cache hands us that doesn't match
// the expected types is ignored — informer event handlers must not
// panic on stray data.
func (p *Provider) onCRB(obj any, deleted bool) {
	crb, ok := obj.(*rbacv1.ClusterRoleBinding)
	if !ok {
		tomb, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		crb, ok = tomb.Obj.(*rbacv1.ClusterRoleBinding)
		if !ok {
			return
		}
		deleted = true
	}
	cluster := clusterOf(crb)
	if deleted {
		p.translator.RemoveClusterRoleBinding(crb.Name, cluster)
		return
	}
	p.translator.ApplyClusterRoleBinding(crb, cluster, p.endpointFor(cluster))
}

// onRB dispatches a RoleBinding informer event into the translator.
// Same tombstone unwrapping behaviour as onCRB.
func (p *Provider) onRB(obj any, deleted bool) {
	rb, ok := obj.(*rbacv1.RoleBinding)
	if !ok {
		tomb, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		rb, ok = tomb.Obj.(*rbacv1.RoleBinding)
		if !ok {
			return
		}
		deleted = true
	}
	cluster := clusterOf(rb)
	if deleted {
		p.translator.RemoveRoleBinding(rb.Namespace, rb.Name, cluster)
		return
	}
	p.translator.ApplyRoleBinding(rb, cluster, p.endpointFor(cluster))
}

// clusterOf extracts the kcp logical-cluster name from an object's
// kcp.io/cluster annotation, falling back to a constant name for
// non-kcp clusters (so plain-Kubernetes development setups still
// produce a useful graph).
func clusterOf(obj metav1.Object) graph.LogicalCluster {
	name := logicalcluster.From(obj)
	if name == "" {
		return fallbackCluster
	}
	return graph.LogicalCluster(name.String())
}
