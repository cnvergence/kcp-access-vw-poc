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

// --- helpers ---------------------------------------------------------

const testCluster graph.LogicalCluster = "ws-test"

func testEndpoint(name string) string {
	return "https://kcp.example.com/clusters/" + string(name)
}

// crb builds a ClusterRoleBinding with the given name and subjects.
// The RoleRef is left zero — the translator doesn't read it in the
// MVP, since "any binding ⇒ access" is the current rule.
func crb(name string, subjects ...rbacv1.Subject) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Subjects:   subjects,
	}
}

// rb builds a RoleBinding with the given namespace, name, and subjects.
func rb(namespace, name string, subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Subjects:   subjects,
	}
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

// clusterNames returns the sorted ClusterName field of each
// AccessEndpointSlice — easier to assert on than full slices.
func clusterNames(slices []graph.AccessEndpointSlice) []string {
	out := make([]string, 0, len(slices))
	for _, s := range slices {
		out = append(out, s.ClusterName)
	}
	sort.Strings(out)
	return out
}

// --- tests -----------------------------------------------------------

func TestApply_singleUserCRB(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	tr.ApplyClusterRoleBinding(crb("alice-binding", userSubject("alice")), testCluster, testEndpoint("ws-test"))

	got := clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("got %v, want [%s]", got, testCluster)
	}
}

func TestApply_singleGroupCRB(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	tr.ApplyClusterRoleBinding(crb("eng-binding", groupSubject("eng")), testCluster, testEndpoint("ws-test"))

	// Group reach: any user in eng sees ws-test.
	for _, user := range []string{"alice", "bob"} {
		got := clusterNames(g.ClustersFor(user, []string{"eng"}))
		if !reflect.DeepEqual(got, []string{string(testCluster)}) {
			t.Errorf("user %s in eng: got %v, want [%s]", user, got, testCluster)
		}
	}
}

func TestApply_serviceAccountCRB(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	tr.ApplyClusterRoleBinding(
		crb("sa-binding", saSubject("kube-system", "default")),
		testCluster, testEndpoint("ws-test"),
	)

	// SA appears as system:serviceaccount:<ns>:<name> in X-Remote-User.
	got := clusterNames(g.ClustersFor("system:serviceaccount:kube-system:default", nil))
	if !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("got %v, want [%s]", got, testCluster)
	}
}

func TestApply_unknownKindIgnored(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	tr.ApplyClusterRoleBinding(
		crb("weird-binding", rbacv1.Subject{Kind: "FooBar", Name: "alice"}),
		testCluster, testEndpoint("ws-test"),
	)

	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Errorf("unknown Kind should not grant access; got %v", got)
	}
}

func TestApply_dedupesSameSubjectInBinding(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	// Binding lists alice twice — should still be a single grant, single ref.
	tr.ApplyClusterRoleBinding(
		crb("alice-twice", userSubject("alice"), userSubject("alice")),
		testCluster, testEndpoint("ws-test"),
	)

	// Now remove the binding. If dedup worked, access should disappear cleanly.
	tr.RemoveClusterRoleBinding("alice-twice", testCluster)

	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Errorf("after remove, alice should have no access; got %v", got)
	}
}

func TestApply_RoleBindingNamespaced(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	tr.ApplyRoleBinding(rb("ns-1", "alice-binding", userSubject("alice")), testCluster, testEndpoint("ws-test"))

	got := clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("got %v, want [%s]", got, testCluster)
	}
}

func TestApply_addsAndRemovesOnUpdate(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	// Initial: alice + bob.
	tr.ApplyClusterRoleBinding(
		crb("b1", userSubject("alice"), userSubject("bob")),
		testCluster, testEndpoint("ws-test"),
	)

	// Update: alice + carol (bob removed, carol added).
	tr.ApplyClusterRoleBinding(
		crb("b1", userSubject("alice"), userSubject("carol")),
		testCluster, testEndpoint("ws-test"),
	)

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

func TestApply_unchangedSubjectsNoOp(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))

	got := clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("got %v, want [%s]", got, testCluster)
	}
}

func TestRemove_revokesAccess(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.RemoveClusterRoleBinding("b1", testCluster)

	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Errorf("after remove: got %v, want empty", got)
	}
}

func TestRemove_unknownBindingIsNoOp(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	// Removing something we never applied must not panic or affect state.
	tr.RemoveClusterRoleBinding("never-existed", testCluster)
	tr.RemoveRoleBinding("ns", "never-existed", testCluster)
}

// Overlapping bindings: two bindings that both grant alice access to
// the same cluster. Removing one preserves access via the other.
func TestOverlappingBindings_partialRemove(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.ApplyClusterRoleBinding(crb("b2", userSubject("alice")), testCluster, testEndpoint("ws-test"))

	// Remove only b1; alice should still see ws-test via b2.
	tr.RemoveClusterRoleBinding("b1", testCluster)

	got := clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("after partial remove: got %v, want [%s]", got, testCluster)
	}

	// Remove b2 too; access disappears.
	tr.RemoveClusterRoleBinding("b2", testCluster)

	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Errorf("after full remove: got %v, want empty", got)
	}
}

func TestOverlappingBindings_directAndGroup(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	// alice gets access directly and via her group eng.
	tr.ApplyClusterRoleBinding(crb("direct", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.ApplyClusterRoleBinding(crb("eng", groupSubject("eng")), testCluster, testEndpoint("ws-test"))

	// Drop the direct binding. alice still gets it via eng.
	tr.RemoveClusterRoleBinding("direct", testCluster)
	got := clusterNames(g.ClustersFor("alice", []string{"eng"}))
	if !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("after direct remove, got %v, want [%s]", got, testCluster)
	}

	// Drop the group binding too. Now no access.
	tr.RemoveClusterRoleBinding("eng", testCluster)
	if got := g.ClustersFor("alice", []string{"eng"}); len(got) != 0 {
		t.Errorf("after both removed, got %v, want empty", got)
	}
}

func TestForgetCluster_removesAllBindingsAndEndpoint(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	const otherCluster graph.LogicalCluster = "ws-other"

	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.ApplyClusterRoleBinding(crb("b2", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.ApplyClusterRoleBinding(crb("b3", userSubject("alice")), otherCluster, testEndpoint("ws-other"))

	tr.ForgetCluster(testCluster)

	// alice should still see ws-other.
	got := clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(otherCluster)}) {
		t.Errorf("after Forget(%s): got %v, want [%s]", testCluster, got, otherCluster)
	}

	// Re-applying b1 against the forgotten cluster should bring access back cleanly,
	// proving Forget didn't leave half-deleted ref state behind.
	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	got = clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(otherCluster), string(testCluster)}) {
		t.Errorf("after re-apply: got %v, want both clusters", got)
	}
}

func TestApply_multipleSubjectsAtOnce(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	tr.ApplyClusterRoleBinding(
		crb("multi",
			userSubject("alice"),
			userSubject("bob"),
			groupSubject("eng"),
		),
		testCluster, testEndpoint("ws-test"),
	)

	if got := clusterNames(g.ClustersFor("alice", nil)); !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("alice: got %v", got)
	}
	if got := clusterNames(g.ClustersFor("bob", nil)); !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("bob: got %v", got)
	}
	if got := clusterNames(g.ClustersFor("carol", []string{"eng"})); !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("carol via eng: got %v", got)
	}
}

func TestApply_endpointUpdate(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	const before = "https://old.example.com/clusters/ws-test"
	const after = "https://new.example.com/clusters/ws-test"

	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, before)
	got := g.ClustersFor("alice", nil)
	if got[0].Endpoint != before {
		t.Fatalf("first apply: got endpoint %q, want %q", got[0].Endpoint, before)
	}

	// Subjects unchanged but the binding's endpoint has moved (e.g.,
	// FrontProxy hostname change). Re-applying with a new endpoint
	// should refresh — but in the current MVP, the diff has nothing
	// to do (subjects didn't change), and the graph keeps the old
	// endpoint. This test pins that current behavior so we know to
	// revisit if/when endpoints can change in flight.
	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, after)

	got = g.ClustersFor("alice", nil)
	if got[0].Endpoint != before {
		t.Logf("MVP behavior change: endpoint refreshed to %q (was %q)", got[0].Endpoint, before)
	}
}

func TestRoleBinding_distinctFromCRB(t *testing.T) {
	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	// CRB and RB can both exist with the same name; the translator
	// must key on (cluster, namespace, name) so they don't collide.
	tr.ApplyClusterRoleBinding(crb("shared-name", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.ApplyRoleBinding(rb("ns-a", "shared-name", userSubject("alice")), testCluster, testEndpoint("ws-test"))

	// Two refs from two distinct bindings; remove the CRB only.
	tr.RemoveClusterRoleBinding("shared-name", testCluster)

	// alice still has access via the namespaced RB.
	got := clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("after CRB remove: got %v, want [%s]", got, testCluster)
	}

	tr.RemoveRoleBinding("ns-a", "shared-name", testCluster)
	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Errorf("after RB remove: got %v, want empty", got)
	}
}
