// Package graph provides an in-memory RBAC permission map for kcp.
//
// The graph is the shared seam between providers and the SCAR HTTP
// handler: providers (the kcp-native RBAC reconciler, an external/FGA
// integration, etc.) populate the graph with Grant/Revoke calls, and
// the handler reads from it via ClustersFor. Providers depend on this
// package; the SCAR handler depends on this package; nothing in this
// package depends on either of them.
//
// The package deliberately has no kcp imports so it stays cleanly
// extractable for possible promotion into kcp upstream and reusable
// by other consumers (admin tooling, FrontProxy optimisations, future
// Warrants/Scopes evaluators if those land).
package graph

import "sync"

// SubjectKind enumerates the kinds of subjects the graph tracks.
//
// Only User and Group are modelled in the MVP. Service-account
// callers are represented as Users whose Name carries the standard
// kcp/Kubernetes service-account string (e.g. system:serviceaccount:...).
type SubjectKind string

const (
	SubjectKindUser  SubjectKind = "User"
	SubjectKindGroup SubjectKind = "Group"
)

// Subject identifies a principal that may have access to logical clusters.
type Subject struct {
	Kind SubjectKind
	Name string
}

// User returns a Subject of kind User.
func User(name string) Subject {
	return Subject{Kind: SubjectKindUser, Name: name}
}

// Group returns a Subject of kind Group.
func Group(name string) Subject {
	return Subject{Kind: SubjectKindGroup, Name: name}
}

// LogicalCluster identifies a kcp logical cluster (workspace).
type LogicalCluster string

// AccessEndpointSlice is a single (cluster name, FrontProxy endpoint)
// pair returned to a caller. It matches the shape consumed by the
// SCAR API; the handler returns these directly inside its response
// payload.
type AccessEndpointSlice struct {
	// ClusterName is the LogicalCluster identifier.
	ClusterName string `json:"clusterName"`
	// Endpoint is the FrontProxy URL for this cluster.
	Endpoint string `json:"endpoint"`
}

// Graph is an in-memory RBAC permission map.
//
// The zero value is not usable; obtain one via New.
//
// Graph is safe for concurrent use. Multiple providers may populate
// the same graph concurrently; the SCAR handler may read it
// concurrently with provider writes.
type Graph struct {
	mu sync.RWMutex
	// access maps a subject to the set of clusters it can reach
	// directly. Group reachability for a user (a user in group G
	// inherits G's clusters) is computed at query time in
	// ClustersFor, not stored here.
	access map[Subject]map[LogicalCluster]struct{}
	// endpoints records the FrontProxy URL for each known cluster.
	// Populated as a side-effect of Grant; never used to imply access
	// on its own.
	endpoints map[LogicalCluster]string

	readyMu sync.RWMutex
	ready   bool
}

// New returns a new empty Graph.
func New() *Graph {
	return &Graph{
		access:    make(map[Subject]map[LogicalCluster]struct{}),
		endpoints: make(map[LogicalCluster]string),
	}
}

// Grant records that subject has access to cluster, reachable at
// the given endpoint URL.
//
// The endpoint is recorded per-cluster, not per-(subject, cluster)
// pair: subsequent Grants for the same cluster overwrite the stored
// endpoint, which is the expected behaviour when an authoritative
// provider observes a renamed or moved cluster.
//
// Grant is idempotent: granting the same access more than once with
// the same arguments is a no-op.
func (g *Graph) Grant(subject Subject, cluster LogicalCluster, endpoint string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.access[subject] == nil {
		g.access[subject] = make(map[LogicalCluster]struct{})
	}
	g.access[subject][cluster] = struct{}{}
	g.endpoints[cluster] = endpoint
}

// Revoke removes subject's access to cluster.
//
// The cluster's endpoint entry is left in place; an orphaned endpoint
// has no effect because ClustersFor never returns a cluster the
// caller has no access edge to. Providers that need to forget a
// cluster entirely can call Forget.
//
// Revoke is idempotent.
func (g *Graph) Revoke(subject Subject, cluster LogicalCluster) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if clusters, ok := g.access[subject]; ok {
		delete(clusters, cluster)
		if len(clusters) == 0 {
			delete(g.access, subject)
		}
	}
}

// Forget removes a cluster entirely: every subject's access to it,
// and the cluster's recorded endpoint. Providers should call this
// when a cluster is deleted from the underlying source so stale
// endpoints don't accumulate.
//
// Forget is idempotent.
func (g *Graph) Forget(cluster LogicalCluster) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for subject, clusters := range g.access {
		if _, ok := clusters[cluster]; ok {
			delete(clusters, cluster)
			if len(clusters) == 0 {
				delete(g.access, subject)
			}
		}
	}
	delete(g.endpoints, cluster)
}

// SetReady marks the graph as having completed its initial sync.
//
// Once SetReady is called, Ready returns true. SetReady is idempotent.
func (g *Graph) SetReady() {
	g.readyMu.Lock()
	defer g.readyMu.Unlock()
	g.ready = true
}

// Ready reports whether the graph has completed its initial sync and
// is ready to serve accurate queries.
//
// Consumers that issue queries before Ready returns true may receive
// incomplete results. The SCAR handler should gate on Ready and
// surface a clear "not ready" error to callers, rather than serving
// a silently incomplete answer.
func (g *Graph) Ready() bool {
	g.readyMu.RLock()
	defer g.readyMu.RUnlock()
	return g.ready
}
