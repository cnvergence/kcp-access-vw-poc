package rbacprovider

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/cnvergence/kcp-access-vw/pkg/accessprovider"
	"github.com/cnvergence/kcp-access-vw/pkg/graph"
)

// Compile-time check that Provider satisfies the AccessProvider
// contract. If the interface ever changes, this line breaks first
// and we hear about it before any caller does.
var _ accessprovider.AccessProvider = (*Provider)(nil)

// Provider is the kcp-native RBAC AccessProvider.
//
// On Start, it stands up cross-shard informers on
// ClusterRoleBinding and RoleBinding (RBAC verb-level filtering
// against (Cluster)Role rules is a follow-up; the MVP treats any
// binding as "view"), translates events into graph mutations via a
// Translator, and marks the graph Ready once the initial sync
// completes.
//
// Provider supports two modes:
//
//   - Live mode: RestConfig is non-nil. Start builds a
//     kubernetes.Interface, runs informers, and feeds the translator
//     with real events.
//   - Stub mode: RestConfig is nil. Start sets up the translator,
//     marks the graph Ready (with an empty data set), and blocks on
//     ctx. Useful for the demo binary and for tests that drive the
//     translator manually via the Translator() accessor.
type Provider struct {
	// EndpointBaseURL is the FrontProxy URL prefix used to construct
	// each cluster's endpoint. Per-cluster URL is base + cluster name.
	EndpointBaseURL string

	// RestConfig is the Kubernetes REST config the provider talks to.
	// When nil, Provider runs in stub mode (see type doc).
	RestConfig *rest.Config

	translator *Translator
}

// New returns a configured Provider in stub mode.
//
// Set RestConfig before calling Start to switch to live mode.
func New(endpointBaseURL string) *Provider {
	return &Provider{EndpointBaseURL: endpointBaseURL}
}

// Translator returns the underlying Translator, building one against
// the supplied graph if Start has not run yet.
//
// Exposed so tests (and any caller wiring events manually before
// real informers exist) can drive translation directly.
func (p *Provider) Translator() *Translator {
	return p.translator
}

// Start implements accessprovider.AccessProvider.
//
// In live mode it builds a kubernetes.Interface from RestConfig,
// stands up CRB and RB informers, waits for the initial cache sync,
// marks the graph Ready, and blocks until ctx is cancelled.
//
// In stub mode (RestConfig is nil) it just builds the translator,
// marks the graph Ready, and blocks until ctx is cancelled.
//
// Returning a non-nil error means the provider has given up; the
// caller is expected to log it and exit (or restart the provider).
func (p *Provider) Start(ctx context.Context, g *graph.Graph) error {
	p.translator = NewTranslator(g)

	if p.RestConfig == nil {
		g.SetReady()
		<-ctx.Done()
		return nil
	}

	client, err := kubernetes.NewForConfig(p.RestConfig)
	if err != nil {
		return fmt.Errorf("build kubernetes client: %w", err)
	}

	return p.runInformers(ctx, client, g)
}

// endpointFor derives a workspace's FrontProxy URL from its logical
// cluster name. Used by the informer event handlers when calling
// Apply* on the translator.
func (p *Provider) endpointFor(c graph.LogicalCluster) string {
	return p.EndpointBaseURL + string(c)
}
