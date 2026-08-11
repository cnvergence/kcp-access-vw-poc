/*
Copyright 2026 The kcp Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package rbacprovider implements the kcp-native RBAC AccessProvider.
//
// The provider observes RBAC bindings and their referenced roles
// across kcp shards and projects declared resource-read bindings onto
// the shared access graph. The graph sums those edges, and the SCAR
// HTTP handler reads from it.
//
// This file holds the pure translation logic — given a binding plus
// its cluster context, what graph mutations does it produce — with
// full reference counting so overlapping bindings (multiple bindings
// granting the same Subject access to the same cluster) don't lose
// access on partial deletion. The informer and controller wiring that
// drives the translator from real Kubernetes and kcp events lives in
// informers.go and multicluster.go.
//
// Scope:
//
//   - Bindings grant workspace discovery only when their referenced
//     Role or ClusterRole has at least one resource rule containing a
//     read verb (get, list, watch, or *). The standard global bootstrap
//     ClusterRoles are recognized by name because kcp does not materialize
//     them in consumer workspaces. Non-resource discovery roles and
//     write-only roles do not expose the workspace.
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

const (
	clusterRoleKind = "ClusterRole"
	roleKind        = "Role"
)

// kcp serves Kubernetes' standard ClusterRoles from the trusted global
// bootstrap policy. They are bindable in every logical cluster, but they are
// not materialized in consumer workspaces and therefore do not appear through
// APIExport permission claims. Every role in this set grants resource reads.
var readableBootstrapClusterRoles = map[string]struct{}{
	"admin":         {},
	"cluster-admin": {},
	"edit":          {},
	"view":          {},
}

type bindingKey struct {
	cluster   graph.LogicalCluster
	namespace string
	name      string
}

type bindingState struct {
	subjects       []graph.Subject
	activeSubjects []graph.Subject
	endpoint       string
	role           roleKey
}

type roleKey struct {
	cluster   graph.LogicalCluster
	kind      string
	namespace string
	name      string
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
	// useKCPBootstrapRoles enables the trusted KCP LocalAdminCluster role
	// fallback. It is enabled only for a known KCP provider mode.
	useKCPBootstrapRoles bool

	mu sync.Mutex
	// refs[subject][cluster] is the set of binding keys that justify
	// the (subject, cluster) edge. The edge exists in the graph iff
	// this set is non-empty.
	refs map[graph.Subject]map[graph.LogicalCluster]map[bindingKey]struct{}
	// bindings tracks the last-observed state of each known binding,
	// so the next Apply can compute a diff against it.
	bindings map[bindingKey]bindingState
	// readableRoles contains only observed roles whose effective PolicyRules
	// grant at least one resource read. Unknown missing roles fail closed.
	readableRoles map[roleKey]struct{}
}

// TranslatorOption scopes behavior that depends on the backing authorization
// implementation rather than on the RBAC objects themselves.
type TranslatorOption func(*Translator)

// WithKCPBootstrapRoles recognizes KCP's trusted global bootstrap roles,
// which are bindable in every workspace but absent from permission claims.
func WithKCPBootstrapRoles() TranslatorOption {
	return func(t *Translator) { t.useKCPBootstrapRoles = true }
}

// NewTranslator returns a Translator that will emit Grant/Revoke calls on g.
func NewTranslator(g *graph.Graph, options ...TranslatorOption) *Translator {
	t := &Translator{
		g:             g,
		refs:          make(map[graph.Subject]map[graph.LogicalCluster]map[bindingKey]struct{}),
		bindings:      make(map[bindingKey]bindingState),
		readableRoles: make(map[roleKey]struct{}),
	}
	for _, option := range options {
		option(t)
	}
	return t
}

// ApplyClusterRole records the effective readability of a ClusterRole
// and immediately re-evaluates every binding that references it.
func (t *Translator) ApplyClusterRole(role *rbacv1.ClusterRole, cluster graph.LogicalCluster) {
	t.applyRole(roleKey{cluster: cluster, kind: clusterRoleKind, name: role.Name}, role.Rules)
}

// RemoveClusterRole removes a ClusterRole and revokes edges justified
// only by bindings that referenced it.
func (t *Translator) RemoveClusterRole(name string, cluster graph.LogicalCluster) {
	t.removeRole(roleKey{cluster: cluster, kind: clusterRoleKind, name: name})
}

// ApplyRole is the namespaced analogue of ApplyClusterRole.
func (t *Translator) ApplyRole(role *rbacv1.Role, cluster graph.LogicalCluster) {
	t.applyRole(roleKey{cluster: cluster, kind: roleKind, namespace: role.Namespace, name: role.Name}, role.Rules)
}

// RemoveRole is the namespaced analogue of RemoveClusterRole.
func (t *Translator) RemoveRole(namespace, name string, cluster graph.LogicalCluster) {
	t.removeRole(roleKey{cluster: cluster, kind: roleKind, namespace: namespace, name: name})
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
	t.apply(key, translateSubjects(crb.Subjects, ""), endpoint, roleForClusterRoleBinding(crb, cluster))
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
	t.apply(key, translateSubjects(rb.Subjects, rb.Namespace), endpoint, roleForRoleBinding(rb, cluster))
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
	for key := range t.readableRoles {
		if key.cluster == cluster {
			delete(t.readableRoles, key)
		}
	}
	t.g.Forget(cluster)
}

func (t *Translator) apply(key bindingKey, subjects []graph.Subject, endpoint string, role roleKey) {
	t.mu.Lock()
	defer t.mu.Unlock()

	oldState := t.bindings[key]
	state := bindingState{subjects: subjects, endpoint: endpoint, role: role}
	if t.roleReadableLocked(role) {
		state.activeSubjects = subjects
	}
	t.bindings[key] = state
	t.updateRefsLocked(key, oldState.activeSubjects, state.activeSubjects, endpoint)

	// Refresh the endpoint even when subjects and permissions are
	// unchanged. Graph.Grant is idempotent and updates cluster metadata.
	for _, s := range state.activeSubjects {
		t.g.Grant(s, key.cluster, endpoint)
	}
}

func (t *Translator) updateRefsLocked(key bindingKey, oldSubjects, newSubjects []graph.Subject, endpoint string) {
	oldSet := subjectSet(oldSubjects)
	newSet := subjectSet(newSubjects)

	// Subjects in old but not new: lose a reference for this key.
	for s := range oldSet {
		if _, in := newSet[s]; !in {
			t.decrementRef(s, key.cluster, key)
		}
	}

	// Subjects in new but not already counted under this key: gain one.
	for s := range newSet {
		if _, in := oldSet[s]; in {
			continue
		}
		t.incrementRef(s, key.cluster, endpoint, key)
	}
}

func (t *Translator) applyRole(key roleKey, rules []rbacv1.PolicyRule) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if grantsResourceRead(rules) {
		t.readableRoles[key] = struct{}{}
	} else {
		delete(t.readableRoles, key)
	}
	t.reconcileRoleBindingsLocked(key)
}

func (t *Translator) removeRole(key roleKey) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.readableRoles, key)
	t.reconcileRoleBindingsLocked(key)
}

func (t *Translator) reconcileRoleBindingsLocked(role roleKey) {
	readable := t.roleReadableLocked(role)
	for key, state := range t.bindings {
		if state.role != role {
			continue
		}
		oldSubjects := state.activeSubjects
		state.activeSubjects = nil
		if readable {
			state.activeSubjects = state.subjects
		}
		t.bindings[key] = state
		t.updateRefsLocked(key, oldSubjects, state.activeSubjects, state.endpoint)
		for _, s := range state.activeSubjects {
			t.g.Grant(s, key.cluster, state.endpoint)
		}
	}
}

func (t *Translator) roleReadableLocked(role roleKey) bool {
	if role == (roleKey{}) {
		return false
	}
	if _, readable := t.readableRoles[role]; readable {
		return true
	}
	if !t.useKCPBootstrapRoles || role.kind != clusterRoleKind || role.namespace != "" {
		return false
	}
	_, readable := readableBootstrapClusterRoles[role.name]
	return readable
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
	for _, s := range state.activeSubjects {
		t.decrementRef(s, key.cluster, key)
	}
}

func roleForClusterRoleBinding(crb *rbacv1.ClusterRoleBinding, cluster graph.LogicalCluster) roleKey {
	if crb.RoleRef.APIGroup != rbacv1.GroupName || crb.RoleRef.Kind != clusterRoleKind {
		return roleKey{}
	}
	return roleKey{cluster: cluster, kind: clusterRoleKind, name: crb.RoleRef.Name}
}

func roleForRoleBinding(rb *rbacv1.RoleBinding, cluster graph.LogicalCluster) roleKey {
	if rb.RoleRef.APIGroup != rbacv1.GroupName {
		return roleKey{}
	}
	switch rb.RoleRef.Kind {
	case clusterRoleKind:
		return roleKey{cluster: cluster, kind: clusterRoleKind, name: rb.RoleRef.Name}
	case roleKind:
		return roleKey{cluster: cluster, kind: roleKind, namespace: rb.Namespace, name: rb.RoleRef.Name}
	default:
		return roleKey{}
	}
}

func grantsResourceRead(rules []rbacv1.PolicyRule) bool {
	for _, rule := range rules {
		if len(rule.Resources) == 0 {
			continue
		}
		for _, verb := range rule.Verbs {
			switch verb {
			case "get", "list", "watch", "*":
				return true
			}
		}
	}
	return false
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

func translateSubjects(in []rbacv1.Subject, defaultNamespace string) []graph.Subject {
	seen := make(map[graph.Subject]struct{})
	out := make([]graph.Subject, 0, len(in))
	for _, rs := range in {
		s, ok := translateSubject(rs, defaultNamespace)
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

func translateSubject(rs rbacv1.Subject, defaultNamespace string) (graph.Subject, bool) {
	switch rs.Kind {
	case rbacv1.UserKind:
		return graph.User(rs.Name), true
	case rbacv1.GroupKind:
		return graph.Group(rs.Name), true
	case rbacv1.ServiceAccountKind:
		namespace := rs.Namespace
		if namespace == "" {
			namespace = defaultNamespace
		}
		if namespace == "" {
			return graph.Subject{}, false
		}
		return graph.User("system:serviceaccount:" + namespace + ":" + rs.Name), true
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
