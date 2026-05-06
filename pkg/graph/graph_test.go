package graph_test

import (
	"reflect"
	"testing"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
)

// endpoint constructs a synthetic FrontProxy URL for a workspace name,
// purely for test readability.
func endpoint(name string) string {
	return "https://kcp.example.com/clusters/" + name
}

// slice is a small helper to construct an expected AccessEndpointSlice
// from a workspace name, using the same synthetic endpoint format
// the tests Grant with.
func slice(name string) graph.AccessEndpointSlice {
	return graph.AccessEndpointSlice{ClusterName: name, Endpoint: endpoint(name)}
}

func TestGrant_individualUser(t *testing.T) {
	g := graph.New()
	g.Grant(graph.User("alice"), "ws-1", endpoint("ws-1"))

	got := g.ClustersFor("alice", nil)
	want := []graph.AccessEndpointSlice{slice("ws-1")}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("ClustersFor(alice) = %v, want %v", got, want)
	}
}

func TestGrant_groupReachesEveryMember(t *testing.T) {
	g := graph.New()
	g.Grant(graph.Group("eng"), "ws-1", endpoint("ws-1"))
	g.Grant(graph.Group("eng"), "ws-2", endpoint("ws-2"))

	for _, user := range []string{"alice", "bob"} {
		got := g.ClustersFor(user, []string{"eng"})
		want := []graph.AccessEndpointSlice{slice("ws-1"), slice("ws-2")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ClustersFor(%s, [eng]) = %v, want %v", user, got, want)
		}
	}
}

func TestClustersFor_unionOfUserAndGroups(t *testing.T) {
	g := graph.New()
	g.Grant(graph.User("alice"), "ws-direct", endpoint("ws-direct"))
	g.Grant(graph.Group("eng"), "ws-eng", endpoint("ws-eng"))
	g.Grant(graph.Group("platform"), "ws-platform", endpoint("ws-platform"))

	got := g.ClustersFor("alice", []string{"eng", "platform"})
	want := []graph.AccessEndpointSlice{
		slice("ws-direct"),
		slice("ws-eng"),
		slice("ws-platform"),
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("union = %v, want %v", got, want)
	}
}

func TestClustersFor_disjointSubjects(t *testing.T) {
	g := graph.New()
	g.Grant(graph.User("alice"), "ws-alice", endpoint("ws-alice"))
	g.Grant(graph.User("bob"), "ws-bob", endpoint("ws-bob"))

	if got := g.ClustersFor("alice", nil); !reflect.DeepEqual(got, []graph.AccessEndpointSlice{slice("ws-alice")}) {
		t.Errorf("alice = %v, want [ws-alice]", got)
	}
	if got := g.ClustersFor("bob", nil); !reflect.DeepEqual(got, []graph.AccessEndpointSlice{slice("ws-bob")}) {
		t.Errorf("bob = %v, want [ws-bob]", got)
	}
}

func TestClustersFor_unknownUserReturnsEmpty(t *testing.T) {
	g := graph.New()
	g.Grant(graph.User("alice"), "ws-1", endpoint("ws-1"))

	got := g.ClustersFor("nobody", nil)
	if len(got) != 0 {
		t.Errorf("unknown user got %v, want empty", got)
	}
}

func TestClustersFor_stableOrdering(t *testing.T) {
	g := graph.New()
	// Grant in non-alphabetical order; expect sorted output by ClusterName.
	g.Grant(graph.User("alice"), "ws-z", endpoint("ws-z"))
	g.Grant(graph.User("alice"), "ws-a", endpoint("ws-a"))
	g.Grant(graph.User("alice"), "ws-m", endpoint("ws-m"))

	got := g.ClustersFor("alice", nil)
	want := []graph.AccessEndpointSlice{slice("ws-a"), slice("ws-m"), slice("ws-z")}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("unordered: got %v, want %v", got, want)
	}
}

func TestClustersFor_deduplicates(t *testing.T) {
	g := graph.New()
	// Same cluster reachable both directly and via group.
	g.Grant(graph.User("alice"), "ws-shared", endpoint("ws-shared"))
	g.Grant(graph.Group("eng"), "ws-shared", endpoint("ws-shared"))

	got := g.ClustersFor("alice", []string{"eng"})
	want := []graph.AccessEndpointSlice{slice("ws-shared")}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("duplicate handling: got %v, want %v", got, want)
	}
}

func TestGrant_idempotent(t *testing.T) {
	g := graph.New()
	g.Grant(graph.User("alice"), "ws-1", endpoint("ws-1"))
	g.Grant(graph.User("alice"), "ws-1", endpoint("ws-1"))

	got := g.ClustersFor("alice", nil)
	want := []graph.AccessEndpointSlice{slice("ws-1")}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("idempotent grant = %v, want %v", got, want)
	}
}

func TestGrant_endpointUpdate(t *testing.T) {
	g := graph.New()
	g.Grant(graph.User("alice"), "ws-1", "https://old.example.com/clusters/ws-1")
	// Provider observes a moved cluster; new Grant should overwrite endpoint.
	g.Grant(graph.User("alice"), "ws-1", "https://new.example.com/clusters/ws-1")

	got := g.ClustersFor("alice", nil)
	want := []graph.AccessEndpointSlice{
		{ClusterName: "ws-1", Endpoint: "https://new.example.com/clusters/ws-1"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("endpoint update = %v, want %v", got, want)
	}
}

func TestRevoke_removesAccess(t *testing.T) {
	g := graph.New()
	g.Grant(graph.User("alice"), "ws-1", endpoint("ws-1"))
	g.Grant(graph.User("alice"), "ws-2", endpoint("ws-2"))

	g.Revoke(graph.User("alice"), "ws-1")

	got := g.ClustersFor("alice", nil)
	want := []graph.AccessEndpointSlice{slice("ws-2")}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("after revoke = %v, want %v", got, want)
	}
}

func TestRevoke_idempotent(t *testing.T) {
	g := graph.New()
	g.Revoke(graph.User("alice"), "ws-1") // never granted

	got := g.ClustersFor("alice", nil)
	if len(got) != 0 {
		t.Errorf("revoke of never-granted = %v, want empty", got)
	}
}

func TestRevoke_groupAccess(t *testing.T) {
	g := graph.New()
	g.Grant(graph.Group("eng"), "ws-1", endpoint("ws-1"))
	g.Revoke(graph.Group("eng"), "ws-1")

	got := g.ClustersFor("alice", []string{"eng"})
	if len(got) != 0 {
		t.Errorf("after group revoke = %v, want empty", got)
	}
}

func TestForget_clusterDisappears(t *testing.T) {
	g := graph.New()
	g.Grant(graph.User("alice"), "ws-1", endpoint("ws-1"))
	g.Grant(graph.Group("eng"), "ws-1", endpoint("ws-1"))
	g.Grant(graph.User("alice"), "ws-2", endpoint("ws-2"))

	g.Forget("ws-1")

	// alice should still have ws-2 directly, but no ws-1.
	if got := g.ClustersFor("alice", []string{"eng"}); !reflect.DeepEqual(got, []graph.AccessEndpointSlice{slice("ws-2")}) {
		t.Errorf("after Forget(ws-1) alice should only see ws-2, got %v", got)
	}
}

func TestReady(t *testing.T) {
	g := graph.New()
	if g.Ready() {
		t.Error("new graph reported ready")
	}
	g.SetReady()
	if !g.Ready() {
		t.Error("graph not ready after SetReady")
	}
	// idempotent
	g.SetReady()
	if !g.Ready() {
		t.Error("Ready flipped on second SetReady")
	}
}
