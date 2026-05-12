// Package rbacprovider implements the kcp-native RBAC AccessProvider.
//
// The provider observes ClusterRoleBindings and RoleBindings across
// kcp shards and projects them onto the shared access graph: each
// binding contributes (Subject, LogicalCluster) edges, the graph
// sums them, and the SCAR HTTP handler reads from it.
//
// This file holds the pure translation logic — given a binding plus
// its cluster context, what graph mutations does it produce — with
// full reference counting so overlapping bindings (multiple bindings
// granting the same Subject access to the same cluster) don't lose
// access on partial deletion. The informer wiring that drives the
// translator from real kcp events lives in provider.go and is filled
// in once kcp's multicluster-runtime is integrated.
//
// MVP scope:
//
//   - Bindings grant "view" implicitly: any binding to a Subject in
//     a workspace means the Subject can see the workspace. Resolving
//     verbs through the role's PolicyRules to enforce strict "view
//     or above" is a follow-up; the translator's API doesn't need to
//     change to add it (we'll filter at the Apply boundary).
//   - Subject kinds: User and Group map directly; ServiceAccount is
//     translated to the canonical "system:serviceaccount:<ns>:<name>"
//     User string. Other Kinds are skipped.
//   - Warrants and Scopes are explicitly out of scope.
package rbacprovider

import (
	"sync"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
)

type bindingKey struct {
	cluster   graph.LogicalCluster
	namespace string
	name      string
}

type bindingState struct {
	subjects []graph.Subject
	endpoint string
}

// Translator turns RBAC binding events into graph mutations.
//
// It is the pure-logic core of the RBAC AccessProvider: it consumes
// rbacv1 binding events plus a (cluster, endpoint) context and emits
// the right Grant/Revoke/Forget calls on the graph, with reference
// counting so overlapping bindings cooperate cleanly.
//
// Translator is safe for concurrent use.
type Translator struct {
	g *graph.Graph

	mu sync.Mutex
	// refs[subject][cluster] is the set of binding keys that justify
	// the (subject, cluster) edge. The edge exists in the graph iff
	// this set is non-empty.
	refs map[graph.Subject]map[graph.LogicalCluster]map[bindingKey]struct{}
	// bindings tracks the last-observed state of each known binding,
	// so the next Apply can compute a diff against it.
	bindings map[bindingKey]bindingState
}

// NewTranslator returns a Translator that will emit Grant/Revoke
// calls on g.
func NewTranslator(g *graph.Graph) *Translator {
	return &Translator{
		g:        g,
		refs:     make(map[graph.Subject]map[graph.LogicalCluster]map[bindingKey]struct{}),
		bindings: make(map[bindingKey]bindingState),
	}
}

// ApplyClusterRoleBinding records the effect of a ClusterRoleBinding
// observed in the given logical cluster, addressable at endpoint.
//
// Apply handles both creation and update: on first Apply for a key,
// every translatable subject gains a reference (and is Granted on the
// graph if this is its first reference); on subsequent Applys, the
// diff between the previous and new subject sets is applied.
//
// Subjects whose Kind the translator doesn't know how to translate
// (anything other than User, Group, ServiceAccount) are silently
// skipped.
func (t *Translator) ApplyClusterRoleBinding(crb *rbacv1.ClusterRoleBinding, cluster graph.LogicalCluster, endpoint string) {
	key := bindingKey{cluster: cluster, name: crb.Name}
	t.apply(key, translateSubjects(crb.Subjects), endpoint)
}

// ApplyRoleBinding is the namespaced analogue of
// ApplyClusterRoleBinding. The (cluster, namespace, name) triple is
// what uniquely identifies a RoleBinding in this codebase.
//
// For SCAR purposes, RoleBindings and ClusterRoleBindings grant the
// same kind of "this Subject can see this workspace" access — the
// distinction is whether the binding is workspace- or
// namespace-scoped, which doesn't matter at the SCAR level.
func (t *Translator) ApplyRoleBinding(rb *rbacv1.RoleBinding, cluster graph.LogicalCluster, endpoint string) {
	key := bindingKey{cluster: cluster, namespace: rb.Namespace, name: rb.Name}
	t.apply(key, translateSubjects(rb.Subjects), endpoint)
}

// RemoveClusterRoleBinding undoes a previously-applied CRB:
// every (subject, cluster) edge it contributed loses one reference,
// and any edge whose ref count reaches zero is Revoked on the graph.
//
// Removing an unknown binding is a no-op.
func (t *Translator) RemoveClusterRoleBinding(name string, cluster graph.LogicalCluster) {
	t.remove(bindingKey{cluster: cluster, name: name})
}

// RemoveRoleBinding is the namespaced analogue of
// RemoveClusterRoleBinding.
func (t *Translator) RemoveRoleBinding(namespace, name string, cluster graph.LogicalCluster) {
	t.remove(bindingKey{cluster: cluster, namespace: namespace, name: name})
}

// ForgetCluster removes every binding observed in the given cluster
// and clears the cluster's endpoint from the graph. Used when a
// workspace itself is deleted.
func (t *Translator) ForgetCluster(cluster graph.LogicalCluster) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for key := range t.bindings {
		if key.cluster == cluster {
			t.removeLocked(key)
		}
	}
	t.g.Forget(cluster)
}

func (t *Translator) apply(key bindingKey, subjects []graph.Subject, endpoint string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	oldState, hasOld := t.bindings[key]
	t.bindings[key] = bindingState{subjects: subjects, endpoint: endpoint}

	oldSet := subjectSet(oldState.subjects)
	newSet := subjectSet(subjects)

	// Subjects in old but not new: lose a reference for this key.
	if hasOld {
		for s := range oldSet {
			if _, in := newSet[s]; !in {
				t.decrementRef(s, key.cluster, key)
			}
		}
	}

	// Subjects in new but not already counted under this key: gain one.
	for s := range newSet {
		if hasOld {
			if _, in := oldSet[s]; in {
				continue
			}
		}
		t.incrementRef(s, key.cluster, endpoint, key)
	}
}

func (t *Translator) remove(key bindingKey) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.removeLocked(key)
}

func (t *Translator) removeLocked(key bindingKey) {
	state, ok := t.bindings[key]
	if !ok {
		return
	}
	delete(t.bindings, key)
	for _, s := range state.subjects {
		t.decrementRef(s, key.cluster, key)
	}
}

func (t *Translator) incrementRef(s graph.Subject, c graph.LogicalCluster, endpoint string, key bindingKey) {
	if t.refs[s] == nil {
		t.refs[s] = make(map[graph.LogicalCluster]map[bindingKey]struct{})
	}
	if t.refs[s][c] == nil {
		t.refs[s][c] = make(map[bindingKey]struct{})
	}
	first := len(t.refs[s][c]) == 0
	t.refs[s][c][key] = struct{}{}
	if first {
		t.g.Grant(s, c, endpoint)
	}
}

func (t *Translator) decrementRef(s graph.Subject, c graph.LogicalCluster, key bindingKey) {
	if t.refs[s] == nil || t.refs[s][c] == nil {
		return
	}
	delete(t.refs[s][c], key)
	if len(t.refs[s][c]) == 0 {
		delete(t.refs[s], c)
		if len(t.refs[s]) == 0 {
			delete(t.refs, s)
		}
		t.g.Revoke(s, c)
	}
}

func translateSubjects(in []rbacv1.Subject) []graph.Subject {
	seen := make(map[graph.Subject]struct{})
	out := make([]graph.Subject, 0, len(in))
	for _, rs := range in {
		s, ok := translateSubject(rs)
		if !ok {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func translateSubject(rs rbacv1.Subject) (graph.Subject, bool) {
	switch rs.Kind {
	case rbacv1.UserKind:
		return graph.User(rs.Name), true
	case rbacv1.GroupKind:
		return graph.Group(rs.Name), true
	case rbacv1.ServiceAccountKind:
		return graph.User("system:serviceaccount:" + rs.Namespace + ":" + rs.Name), true
	default:
		return graph.Subject{}, false
	}
}

func subjectSet(ss []graph.Subject) map[graph.Subject]struct{} {
	out := make(map[graph.Subject]struct{}, len(ss))
	for _, s := range ss {
		out[s] = struct{}{}
	}
	return out
}
