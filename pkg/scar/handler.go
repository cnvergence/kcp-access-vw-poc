// Package scar implements the SelfClusterAccessReview HTTP endpoint.
//
// The handler reads identity from FrontProxy-injected headers
// (X-Remote-User, X-Remote-Group), calls Graph.ClustersFor on the
// shared access graph, and returns the result as a SCAR-shaped JSON
// response. It is intentionally a thin wrapper around the graph: no
// auth decisions are made here, no caching, no batching. The graph is
// the seam; the handler is the projection of that graph into HTTP.
//
// The MVP type definitions in this package match the field shape from
// kcp-dev/kcp#3839 but do not yet embed Kubernetes metav1 types; they
// will move to a dedicated api/ package once we depend on
// k8s.io/apimachinery for the full TypeMeta/ObjectMeta plumbing.
package scar

import (
	"encoding/json"
	"net/http"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
)

// Path is the canonical URL path the handler is registered at when
// served behind kcp's FrontProxy.
const Path = "/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews"

// APIVersion is the apiVersion string returned in SCAR responses.
const APIVersion = "access.kcp.io/v1alpha1"

// Kind is the kind string returned in SCAR responses.
const Kind = "SelfClusterAccessReview"

// SelfClusterAccessReview is the response payload returned to a
// caller.
//
// MVP shape: a hand-rolled struct with JSON tags matching the SCAR
// spec. When this package gains a dependency on k8s.io/apimachinery,
// metav1.TypeMeta and metav1.ObjectMeta will replace the manual
// Kind/APIVersion fields and we'll move the types to a dedicated
// api/ package.
type SelfClusterAccessReview struct {
	Kind       string                        `json:"kind"`
	APIVersion string                        `json:"apiVersion"`
	Status     SelfClusterAccessReviewStatus `json:"status"`
}

// SelfClusterAccessReviewStatus carries the list of clusters the
// authenticated subject can access.
type SelfClusterAccessReviewStatus struct {
	// Clusters is the list of clusters the subject has at least
	// view access to. Populated from Graph.ClustersFor.
	Clusters []graph.AccessEndpointSlice `json:"clusters"`
}

// Handler returns an http.Handler that serves SCAR queries against
// the given graph.
//
// Behavior:
//
//   - POST only. Other methods return 405. (Matches the convention
//     for K8s self-review APIs.)
//   - X-Remote-User must be present; otherwise 401. The header is
//     populated by kcp's FrontProxy from the authenticated identity.
//   - If the graph reports !Ready, the handler returns 503 with a
//     clear "not ready" message rather than serving a silently
//     incomplete answer.
//   - On success, returns 200 with a SelfClusterAccessReview JSON body.
func Handler(g *graph.Graph) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if !g.Ready() {
			http.Error(w, "access graph is not ready; try again shortly", http.StatusServiceUnavailable)
			return
		}

		user := r.Header.Get("X-Remote-User")
		if user == "" {
			http.Error(w, "missing X-Remote-User header", http.StatusUnauthorized)
			return
		}
		groups := r.Header.Values("X-Remote-Group")

		clusters := g.ClustersFor(user, groups)

		resp := SelfClusterAccessReview{
			Kind:       Kind,
			APIVersion: APIVersion,
			Status: SelfClusterAccessReviewStatus{
				Clusters: clusters,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// If the encoder fails after WriteHeader, there's nothing
		// useful we can return to the caller. The error is intentionally
		// swallowed; structured logging will be wired in alongside the
		// real provider integration.
		_ = json.NewEncoder(w).Encode(resp)
	})
}
