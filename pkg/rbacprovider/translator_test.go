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

package rbacprovider_test

import (
	"reflect"
	"sort"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
	"github.com/cnvergence/kcp-access-vw/pkg/rbacprovider"
)

const testCluster graph.LogicalCluster = "ws-test"

func testEndpoint(name string) string {
	return "https://kcp.example.com/clusters/" + string(name)
}

func crb(name string, subjects ...rbacv1.Subject) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Subjects:   subjects,
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "view"},
	}
}

func rb(namespace, name string, subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Subjects:   subjects,
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "view"},
	}
}

func readableRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}}}
}

func newTranslator(g *graph.Graph, clusters ...graph.LogicalCluster) *rbacprovider.Translator {
	tr := rbacprovider.NewTranslator(g)
	if len(clusters) == 0 {
		clusters = []graph.LogicalCluster{testCluster}
	}
	for _, cluster := range clusters {
		tr.ApplyClusterRole(&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "view"}, Rules: readableRules()}, cluster)
	}
	return tr
}

func userSubject(name string) rbacv1.Subject {
	return rbacv1.Subject{Kind: rbacv1.UserKind, Name: name}
}

func groupSubject(name string) rbacv1.Subject {
	return rbacv1.Subject{Kind: rbacv1.GroupKind, Name: name}
}

func saSubject(namespace, name string) rbacv1.Subject {
	return rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: namespace, Name: name}
}

func clusterNames(slices []graph.AccessEndpointSlice) []string {
	out := make([]string, 0, len(slices))
	for _, s := range slices {
		out = append(out, s.ClusterName)
	}
	sort.Strings(out)
	return out
}

func TestApplyClusterRoleBinding(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*rbacprovider.Translator)
		queryUser  string
		queryGroup []string
		want       []string
	}{
		{
			name: "single user",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("alice-binding", userSubject("alice")), testCluster, testEndpoint("ws-test"))
			},
			queryUser: "alice",
			want:      []string{string(testCluster)},
		},
		{
			name: "single group",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("eng-binding", groupSubject("eng")), testCluster, testEndpoint("ws-test"))
			},
			queryUser:  "alice",
			queryGroup: []string{"eng"},
			want:       []string{string(testCluster)},
		},
		{
			name: "service account",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("sa-binding", saSubject("kube-system", "default")), testCluster, testEndpoint("ws-test"))
			},
			queryUser: "system:serviceaccount:kube-system:default",
			want:      []string{string(testCluster)},
		},
		{
			name: "cluster role binding service account without namespace is ignored",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("invalid-sa", saSubject("", "default")), testCluster, testEndpoint("ws-test"))
			},
			queryUser: "system:serviceaccount::default",
			want:      nil,
		},
		{
			name: "unknown subject kind ignored",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("weird-binding", rbacv1.Subject{Kind: "FooBar", Name: "alice"}), testCluster, testEndpoint("ws-test"))
			},
			queryUser: "alice",
			want:      nil,
		},
		{
			name: "multiple subjects at once",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("multi", userSubject("alice"), userSubject("bob"), groupSubject("eng")), testCluster, testEndpoint("ws-test"))
			},
			queryUser: "alice",
			want:      []string{string(testCluster)},
		},
		{
			name: "unchanged subjects is no-op",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
				tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
			},
			queryUser: "alice",
			want:      []string{string(testCluster)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := graph.New()
			tr := newTranslator(g)
			tt.setup(tr)

			got := clusterNames(g.ClustersFor(tt.queryUser, tt.queryGroup))
			if tt.want == nil {
				if len(got) != 0 {
					t.Errorf("got %v, want empty", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyClusterRoleBinding_dedupesSameSubject(t *testing.T) {
	g := graph.New()
	tr := newTranslator(g)

	tr.ApplyClusterRoleBinding(crb("alice-twice", userSubject("alice"), userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.RemoveClusterRoleBinding("alice-twice", testCluster)

	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Errorf("after remove, alice should have no access; got %v", got)
	}
}

func TestApplyClusterRoleBinding_updatesSubjects(t *testing.T) {
	g := graph.New()
	tr := newTranslator(g)

	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice"), userSubject("bob")), testCluster, testEndpoint("ws-test"))
	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice"), userSubject("carol")), testCluster, testEndpoint("ws-test"))

	if got := clusterNames(g.ClustersFor("alice", nil)); !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("alice retained: got %v", got)
	}
	if got := g.ClustersFor("bob", nil); len(got) != 0 {
		t.Errorf("bob should have lost access; got %v", got)
	}
	if got := clusterNames(g.ClustersFor("carol", nil)); !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("carol gained: got %v", got)
	}
}

func TestApplyRoleBinding(t *testing.T) {
	g := graph.New()
	tr := newTranslator(g)

	tr.ApplyRoleBinding(rb("ns-1", "alice-binding", userSubject("alice")), testCluster, testEndpoint("ws-test"))

	got := clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("got %v, want [%s]", got, testCluster)
	}
}

func TestRoleBindingDefaultsServiceAccountNamespace(t *testing.T) {
	g := graph.New()
	tr := newTranslator(g)

	tr.ApplyRoleBinding(rb("team-a", "default-sa", saSubject("", "default")), testCluster, testEndpoint("ws-test"))

	got := clusterNames(g.ClustersFor("system:serviceaccount:team-a:default", nil))
	if !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Fatalf("defaulted service account namespace: got %v", got)
	}
}

func TestBindingRequiresReadableReferencedRole(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*rbacprovider.Translator) *rbacv1.ClusterRoleBinding
		want  bool
	}{
		{
			name: "missing role fails closed",
			setup: func(_ *rbacprovider.Translator) *rbacv1.ClusterRoleBinding {
				binding := crb("missing", userSubject("alice"))
				binding.RoleRef.Name = "missing"
				return binding
			},
		},
		{
			name: "empty role fails closed",
			setup: func(tr *rbacprovider.Translator) *rbacv1.ClusterRoleBinding {
				tr.ApplyClusterRole(&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "empty"}}, testCluster)
				binding := crb("empty", userSubject("alice"))
				binding.RoleRef.Name = "empty"
				return binding
			},
		},
		{
			name: "write-only role is not workspace visibility",
			setup: func(tr *rbacprovider.Translator) *rbacv1.ClusterRoleBinding {
				tr.ApplyClusterRole(&rbacv1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{Name: "writer"},
					Rules:      []rbacv1.PolicyRule{{Resources: []string{"pods"}, Verbs: []string{"create", "update", "patch", "delete"}}},
				}, testCluster)
				binding := crb("writer", userSubject("alice"))
				binding.RoleRef.Name = "writer"
				return binding
			},
		},
		{
			name: "non-resource discovery role is ignored",
			setup: func(tr *rbacprovider.Translator) *rbacv1.ClusterRoleBinding {
				tr.ApplyClusterRole(&rbacv1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{Name: "discovery"},
					Rules:      []rbacv1.PolicyRule{{NonResourceURLs: []string{"/api", "/apis"}, Verbs: []string{"get"}}},
				}, testCluster)
				binding := crb("discovery", groupSubject("system:authenticated"))
				binding.RoleRef.Name = "discovery"
				return binding
			},
		},
		{
			name: "resource wildcard is readable",
			setup: func(tr *rbacprovider.Translator) *rbacv1.ClusterRoleBinding {
				tr.ApplyClusterRole(&rbacv1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{Name: "admin"},
					Rules:      []rbacv1.PolicyRule{{Resources: []string{"*"}, Verbs: []string{"*"}}},
				}, testCluster)
				binding := crb("admin", userSubject("alice"))
				binding.RoleRef.Name = "admin"
				return binding
			},
			want: true,
		},
		{
			name: "invalid role ref fails closed",
			setup: func(_ *rbacprovider.Translator) *rbacv1.ClusterRoleBinding {
				binding := crb("invalid", userSubject("alice"))
				binding.RoleRef.APIGroup = "foreign.example"
				return binding
			},
		},
		{
			name: "cluster role binding cannot reference namespaced role",
			setup: func(_ *rbacprovider.Translator) *rbacv1.ClusterRoleBinding {
				binding := crb("invalid-kind", userSubject("alice"))
				binding.RoleRef.Kind = "Role"
				return binding
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := graph.New()
			tr := rbacprovider.NewTranslator(g)
			tr.ApplyClusterRoleBinding(tt.setup(tr), testCluster, testEndpoint("ws-test"))
			got := len(g.ClustersFor("alice", []string{"system:authenticated"})) > 0
			if got != tt.want {
				t.Fatalf("workspace visible = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKCPBootstrapClusterRoles(t *testing.T) {
	for _, roleName := range []string{"admin", "cluster-admin", "edit", "view"} {
		t.Run(roleName, func(t *testing.T) {
			g := graph.New()
			tr := rbacprovider.NewTranslator(g, rbacprovider.WithKCPBootstrapRoles())
			binding := crb("bootstrap-"+roleName, userSubject("alice"))
			binding.RoleRef.Name = roleName
			tr.ApplyClusterRoleBinding(binding, testCluster, testEndpoint("ws-test"))

			if got := clusterNames(g.ClustersFor("alice", nil)); !reflect.DeepEqual(got, []string{string(testCluster)}) {
				t.Fatalf("unmaterialized global bootstrap role should be readable: %v", got)
			}

			// KCP unions same-name local and LocalAdminCluster roles. An unreadable
			// local contribution therefore cannot remove the global read grant.
			tr.ApplyClusterRole(&rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{Name: roleName},
				Rules:      []rbacv1.PolicyRule{{Resources: []string{"pods"}, Verbs: []string{"create"}}},
			}, testCluster)
			if got := clusterNames(g.ClustersFor("alice", nil)); !reflect.DeepEqual(got, []string{string(testCluster)}) {
				t.Fatalf("local role must union with bootstrap role: %v", got)
			}

			tr.RemoveClusterRole(roleName, testCluster)
			if got := clusterNames(g.ClustersFor("alice", nil)); !reflect.DeepEqual(got, []string{string(testCluster)}) {
				t.Fatalf("removing local contribution changed bootstrap fallback: %v", got)
			}
		})
	}
}

func TestKCPBootstrapClusterRolesAreExplicitlyScoped(t *testing.T) {
	tests := []struct {
		name    string
		options []rbacprovider.TranslatorOption
		binding func() *rbacv1.RoleBinding
		want    bool
	}{
		{
			name: "fallback disabled outside KCP APIExport mode",
			binding: func() *rbacv1.RoleBinding {
				return rb("team-a", "view", userSubject("alice"))
			},
		},
		{
			name:    "RoleBinding to global ClusterRole is visible",
			options: []rbacprovider.TranslatorOption{rbacprovider.WithKCPBootstrapRoles()},
			binding: func() *rbacv1.RoleBinding {
				return rb("team-a", "view", userSubject("alice"))
			},
			want: true,
		},
		{
			name:    "namespaced Role with bootstrap name is not special",
			options: []rbacprovider.TranslatorOption{rbacprovider.WithKCPBootstrapRoles()},
			binding: func() *rbacv1.RoleBinding {
				binding := rb("team-a", "view", userSubject("alice"))
				binding.RoleRef.Kind = "Role"
				return binding
			},
		},
		{
			name:    "unknown missing ClusterRole fails closed",
			options: []rbacprovider.TranslatorOption{rbacprovider.WithKCPBootstrapRoles()},
			binding: func() *rbacv1.RoleBinding {
				binding := rb("team-a", "unknown", userSubject("alice"))
				binding.RoleRef.Name = "unknown"
				return binding
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := graph.New()
			tr := rbacprovider.NewTranslator(g, tt.options...)
			tr.ApplyRoleBinding(tt.binding(), testCluster, testEndpoint("ws-test"))
			got := len(g.ClustersFor("alice", nil)) > 0
			if got != tt.want {
				t.Fatalf("workspace visible = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKCPBootstrapBindingSurvivesForgetAndReAdd(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g, rbacprovider.WithKCPBootstrapRoles())
	binding := crb("bootstrap-admin", userSubject("alice"))
	binding.RoleRef.Name = "cluster-admin"
	tr.ApplyClusterRoleBinding(binding, testCluster, testEndpoint("ws-test"))
	tr.ForgetCluster(testCluster)
	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Fatalf("forgotten cluster remained visible: %v", got)
	}

	tr.ApplyClusterRoleBinding(binding, testCluster, testEndpoint("ws-test"))
	if got := clusterNames(g.ClustersFor("alice", nil)); !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Fatalf("bootstrap binding did not recover after re-add: %v", got)
	}
}

func TestRoleLifecycleReconcilesExistingBindings(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)
	binding := crb("late-role", userSubject("alice"))
	binding.RoleRef.Name = "late-role"
	tr.ApplyClusterRoleBinding(binding, testCluster, testEndpoint("ws-test"))

	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Fatalf("binding must fail closed before its role arrives: %v", got)
	}

	role := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "late-role"}, Rules: readableRules()}
	tr.ApplyClusterRole(role, testCluster)
	if got := clusterNames(g.ClustersFor("alice", nil)); !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Fatalf("readable role should activate existing binding: %v", got)
	}

	role.Rules = []rbacv1.PolicyRule{{Resources: []string{"pods"}, Verbs: []string{"create"}}}
	tr.ApplyClusterRole(role, testCluster)
	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Fatalf("write-only role update should revoke workspace: %v", got)
	}

	role.Rules = readableRules()
	tr.ApplyClusterRole(role, testCluster)
	tr.RemoveClusterRole(role.Name, testCluster)
	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Fatalf("role deletion should revoke workspace: %v", got)
	}
}

func TestRoleBindingResolvesNamespacedRole(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "reader"}, Rules: readableRules()}
	tr.ApplyRole(role, testCluster)
	binding := rb("team-a", "reader", userSubject("alice"))
	binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "reader"}
	tr.ApplyRoleBinding(binding, testCluster, testEndpoint("ws-test"))

	if got := clusterNames(g.ClustersFor("alice", nil)); !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Fatalf("namespaced Role should activate its RoleBinding: %v", got)
	}

	tr.RemoveRole("team-a", "reader", testCluster)
	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Fatalf("Role deletion should revoke workspace: %v", got)
	}
}

func TestRoleBindingDoesNotCrossNamespaceForRole(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)
	tr.ApplyRole(&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: "team-b", Name: "reader"}, Rules: readableRules()}, testCluster)
	binding := rb("team-a", "reader", userSubject("alice"))
	binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "reader"}
	tr.ApplyRoleBinding(binding, testCluster, testEndpoint("ws-test"))

	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Fatalf("RoleBinding resolved Role across namespace: %v", got)
	}
}

func TestUnreadableBindingDoesNotRevokeOverlappingReadableBinding(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)
	tr.ApplyClusterRole(&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "reader"}, Rules: readableRules()}, testCluster)
	tr.ApplyClusterRole(&rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "writer"},
		Rules:      []rbacv1.PolicyRule{{Resources: []string{"pods"}, Verbs: []string{"create"}}},
	}, testCluster)
	reader := crb("reader", userSubject("alice"))
	reader.RoleRef.Name = "reader"
	writer := crb("writer", userSubject("alice"))
	writer.RoleRef.Name = "writer"
	tr.ApplyClusterRoleBinding(reader, testCluster, testEndpoint("ws-test"))
	tr.ApplyClusterRoleBinding(writer, testCluster, testEndpoint("ws-test"))
	tr.RemoveClusterRoleBinding("writer", testCluster)

	if got := clusterNames(g.ClustersFor("alice", nil)); !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Fatalf("readable binding should continue to justify access: %v", got)
	}
}

func TestRemoveClusterRoleBinding(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*rbacprovider.Translator)
		user  string
	}{
		{
			name: "revokes access",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
				tr.RemoveClusterRoleBinding("b1", testCluster)
			},
			user: "alice",
		},
		{
			name: "unknown binding is no-op",
			setup: func(tr *rbacprovider.Translator) {
				tr.RemoveClusterRoleBinding("never-existed", testCluster)
				tr.RemoveRoleBinding("ns", "never-existed", testCluster)
			},
			user: "alice",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := graph.New()
			tr := newTranslator(g)
			tt.setup(tr)

			if got := g.ClustersFor(tt.user, nil); len(got) != 0 {
				t.Errorf("got %v, want empty", got)
			}
		})
	}
}

func TestOverlappingBindings(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*rbacprovider.Translator)
		user      string
		groups    []string
		wantAfter []string
	}{
		{
			name: "partial remove preserves access via other binding",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
				tr.ApplyClusterRoleBinding(crb("b2", userSubject("alice")), testCluster, testEndpoint("ws-test"))
				tr.RemoveClusterRoleBinding("b1", testCluster)
			},
			user:      "alice",
			wantAfter: []string{string(testCluster)},
		},
		{
			name: "direct and group — remove direct keeps group access",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("direct", userSubject("alice")), testCluster, testEndpoint("ws-test"))
				tr.ApplyClusterRoleBinding(crb("eng", groupSubject("eng")), testCluster, testEndpoint("ws-test"))
				tr.RemoveClusterRoleBinding("direct", testCluster)
			},
			user:      "alice",
			groups:    []string{"eng"},
			wantAfter: []string{string(testCluster)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := graph.New()
			tr := newTranslator(g)
			tt.setup(tr)

			got := clusterNames(g.ClustersFor(tt.user, tt.groups))
			if !reflect.DeepEqual(got, tt.wantAfter) {
				t.Errorf("got %v, want %v", got, tt.wantAfter)
			}
		})
	}
}

func TestApplyClusterRoleBinding_endpointUpdate(t *testing.T) {
	g := graph.New()
	tr := newTranslator(g)

	const before = "https://old.example.com/clusters/ws-test"
	const after = "https://new.example.com/clusters/ws-test"

	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, before)
	got := g.ClustersFor("alice", nil)
	if got[0].Endpoint != before {
		t.Fatalf("first apply: got endpoint %q, want %q", got[0].Endpoint, before)
	}

	// Subjects unchanged but endpoint moved. The cluster metadata must
	// still refresh so SCAR never returns a stale routing target.
	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, after)
	got = g.ClustersFor("alice", nil)
	if got[0].Endpoint != after {
		t.Fatalf("updated apply: got endpoint %q, want %q", got[0].Endpoint, after)
	}
}

func TestForgetCluster(t *testing.T) {
	g := graph.New()
	const otherCluster graph.LogicalCluster = "ws-other"
	tr := newTranslator(g, testCluster, otherCluster)

	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.ApplyClusterRoleBinding(crb("b2", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.ApplyClusterRoleBinding(crb("b3", userSubject("alice")), otherCluster, testEndpoint("ws-other"))

	tr.ForgetCluster(testCluster)

	got := clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(otherCluster)}) {
		t.Errorf("after Forget(%s): got %v, want [%s]", testCluster, got, otherCluster)
	}

	// Re-applying should bring access back cleanly.
	tr.ApplyClusterRole(&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "view"}, Rules: readableRules()}, testCluster)
	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	got = clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(otherCluster), string(testCluster)}) {
		t.Errorf("after re-apply: got %v, want both clusters", got)
	}
}

func TestRoleBinding_distinctFromCRB(t *testing.T) {
	g := graph.New()
	tr := newTranslator(g)

	tr.ApplyClusterRoleBinding(crb("shared-name", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.ApplyRoleBinding(rb("ns-a", "shared-name", userSubject("alice")), testCluster, testEndpoint("ws-test"))

	tr.RemoveClusterRoleBinding("shared-name", testCluster)

	got := clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("after CRB remove: got %v, want [%s]", got, testCluster)
	}

	tr.RemoveRoleBinding("ns-a", "shared-name", testCluster)
	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Errorf("after RB remove: got %v, want empty", got)
	}
}
