// Package scar implements the SelfClusterAccessReview HTTP endpoint.
//
// The handler authenticates the caller via the virtual workspace's
// auth.Resolver, calls Graph.ClustersFor on the shared access graph,
// and returns the result as a SCAR-shaped JSON response. It is
// intentionally a thin wrapper around the graph: no authorization
// decisions are made here (SCAR is a self-review — the caller asks
// about themselves), no caching, no batching. The graph is the seam;
// the handler is the projection of that graph into HTTP.
//
// Authentication is delegated to the auth.Resolver configured on the
// VirtualWorkspace. In production that's typically a ChainResolver
// trying bearer-token TokenReview, then client-cert verification,
// then (optionally) FrontProxy header trust. The handler doesn't know
// or care which mechanism succeeds.
//
// The wire types come from pkg/apis/access/v1alpha1, which holds the
// real, registered Kubernetes API types (TypeMeta, ObjectMeta,
// Status). Encoding is plain json.Marshal; switching to the Kubernetes
// codecs is mechanical now that types are scheme-registered.
package scar

import (
	"encoding/json"
	"log"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	accessv1alpha1 "github.com/cnvergence/kcp-access-vw/pkg/apis/access/v1alpha1"
	"github.com/cnvergence/kcp-access-vw/pkg/graph"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual/auth"
)

// Path is the canonical URL path the handler is registered at when
// served behind kcp's FrontProxy.
const Path = "/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews"

// APIVersion is the apiVersion string returned in SCAR responses.
const APIVersion = accessv1alpha1.GroupName + "/v1alpha1"

// Kind is the kind string returned in SCAR responses.
const Kind = "SelfClusterAccessReview"

// SelfClusterAccessReview re-exports the API type so callers can
// import it from this package. The canonical definition lives in
// pkg/apis/access/v1alpha1.
type SelfClusterAccessReview = accessv1alpha1.SelfClusterAccessReview

// SelfClusterAccessReviewStatus re-exports the status type.
type SelfClusterAccessReviewStatus = accessv1alpha1.SelfClusterAccessReviewStatus

// AccessEndpointSlice re-exports the wire type.
type AccessEndpointSlice = accessv1alpha1.AccessEndpointSlice

// Register mounts the SCAR handler on the supplied mux using the
// canonical Path.
func Register(mux *http.ServeMux, g *graph.Graph, resolver auth.Resolver) {
	mux.Handle(Path, Handler(g, resolver))
}

// Handler returns an http.Handler that serves SCAR queries against
// the given graph, authenticating callers via the supplied resolver.
//
// Behavior:
//
//   - POST only. Other methods return 405.
//   - The resolver must successfully resolve identity; otherwise 401.
//   - If the graph reports !Ready, the handler returns 503.
//   - On success, returns 200 with a SelfClusterAccessReview JSON body.
func Handler(g *graph.Graph, resolver auth.Resolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if !g.Ready() {
			http.Error(w, "access graph is not ready; try again shortly", http.StatusServiceUnavailable)
			return
		}

		id, err := resolver.Resolve(r.Context(), r)
		if err != nil {
			log.Printf("auth: %v", err)
			http.Error(w, "authentication failed", http.StatusUnauthorized)
			return
		}

		clusters := g.ClustersFor(id.Username, id.Groups)

		resp := SelfClusterAccessReview{
			TypeMeta: metav1.TypeMeta{
				Kind:       Kind,
				APIVersion: APIVersion,
			},
			Status: SelfClusterAccessReviewStatus{
				Clusters: toAccessEndpointSlices(clusters),
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// toAccessEndpointSlices converts the graph's internal slice type
// to the wire type. Both have identical fields; the boundary keeps
// the graph free of api/ imports.
func toAccessEndpointSlices(in []graph.AccessEndpointSlice) []AccessEndpointSlice {
	if in == nil {
		return nil
	}
	out := make([]AccessEndpointSlice, len(in))
	for i, s := range in {
		out[i] = AccessEndpointSlice{
			ClusterName: s.ClusterName,
			Endpoint:    s.Endpoint,
		}
	}
	return out
}
