// Package virtual provides the shared skeleton for the Access
// Virtual Workspace — the external service that mounts handlers
// under /services/access-virtual-workspace/ and answers
// authenticated queries against the in-memory access graph.
//
// VirtualWorkspace holds the shared state (graph, auth resolver,
// kcp config) and exposes handler registration so each sub-handler
// (SCAR today, potentially workspace-listing or metrics later) can
// be plugged in independently.
//
// # Architecture
//
// The VW is NOT backed by multicluster-runtime for request serving.
// The HTTP path is plain net/http: authenticate caller → read graph
// → return JSON. Only the controller side (pkg/rbacprovider) uses
// MCR to drive the graph from RBAC events across shards.
//
// Authentication follows the kedge pattern:
//   - Bearer tokens are resolved via kcp's TokenReview API.
//   - Client certificates are verified against a CA bundle.
//   - FrontProxy X-Remote-* headers are accepted as a dev-only fallback.
//
// All three are plugged into a ChainResolver; the SCAR handler
// calls Resolve once per request.
package virtual

import (
	"net/http"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual/auth"
)

// VirtualWorkspace holds shared state for the Access Virtual
// Workspace's HTTP handlers.
type VirtualWorkspace struct {
	// Graph is the in-memory access graph populated by providers.
	Graph *graph.Graph

	// Auth resolves caller identity from incoming requests.
	Auth auth.Resolver
}

// New returns a VirtualWorkspace with the supplied graph and resolver.
func New(g *graph.Graph, resolver auth.Resolver) *VirtualWorkspace {
	return &VirtualWorkspace{
		Graph: g,
		Auth:  resolver,
	}
}

// RegisterHandlers wires all VW sub-handlers onto the supplied mux.
// Today this is just SCAR; future handlers (workspace listing, etc.)
// are added here.
//
// Callers import and call the sub-packages' registration functions
// directly (e.g. scar.Register(mux, vw)) rather than going through
// this method, because each sub-package may have its own
// configuration. This method exists as a convenience for the common
// "register everything with defaults" path.
func (vw *VirtualWorkspace) RegisterHandlers(mux *http.ServeMux) {
	// Sub-packages register themselves; see pkg/virtual/scar.Register.
	// This method is a placeholder for the "register all with defaults"
	// convenience pattern that will make more sense once there are ≥2
	// sub-handlers.
}
