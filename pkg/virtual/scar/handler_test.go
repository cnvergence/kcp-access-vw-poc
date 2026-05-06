package scar_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual/auth"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual/scar"
)

// newReadyGraph returns a graph populated with the given grants and
// marked as ready.
func newReadyGraph(t *testing.T, grants []grant) *graph.Graph {
	t.Helper()
	g := graph.New()
	for _, gr := range grants {
		var subj graph.Subject
		switch gr.kind {
		case graph.SubjectKindUser:
			subj = graph.User(gr.name)
		case graph.SubjectKindGroup:
			subj = graph.Group(gr.name)
		default:
			t.Fatalf("unknown subject kind %q", gr.kind)
		}
		g.Grant(subj, graph.LogicalCluster(gr.cluster), endpoint(gr.cluster))
	}
	g.SetReady()
	return g
}

type grant struct {
	kind    graph.SubjectKind
	name    string
	cluster string
}

func endpoint(name string) string {
	return "https://kcp.example.com/clusters/" + name
}

// Tests use HeaderResolver — it doesn't need a real kcp. The auth
// layer is independently tested in pkg/virtual/auth.
var testResolver auth.Resolver = auth.HeaderResolver{}

func doSCAR(h http.Handler, method string, headers map[string][]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, scar.Path, strings.NewReader(""))
	for k, vs := range headers {
		req.Header[k] = vs
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestHandler_methodNotAllowed(t *testing.T) {
	g := graph.New()
	g.SetReady()
	h := scar.Handler(g, testResolver)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rr := doSCAR(h, method, map[string][]string{"X-Remote-User": {"alice"}})
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("method %s: got %d, want 405", method, rr.Code)
		}
	}
}

func TestHandler_missingUserReturns401(t *testing.T) {
	g := graph.New()
	g.SetReady()
	h := scar.Handler(g, testResolver)

	rr := doSCAR(h, http.MethodPost, nil)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rr.Code)
	}
}

func TestHandler_notReadyReturns503(t *testing.T) {
	g := graph.New() // never SetReady
	h := scar.Handler(g, testResolver)

	rr := doSCAR(h, http.MethodPost, map[string][]string{"X-Remote-User": {"alice"}})
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", rr.Code)
	}
}

func TestHandler_directUserAccess(t *testing.T) {
	g := newReadyGraph(t, []grant{
		{graph.SubjectKindUser, "alice", "ws-alice"},
	})
	h := scar.Handler(g, testResolver)

	rr := doSCAR(h, http.MethodPost, map[string][]string{"X-Remote-User": {"alice"}})

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp scar.SelfClusterAccessReview
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v; raw: %s", err, rr.Body.String())
	}

	if resp.Kind != scar.Kind || resp.APIVersion != scar.APIVersion {
		t.Errorf("Kind/APIVersion = %q/%q, want %q/%q",
			resp.Kind, resp.APIVersion, scar.Kind, scar.APIVersion)
	}

	if got, want := len(resp.Status.Clusters), 1; got != want {
		t.Fatalf("len(Clusters) = %d, want %d", got, want)
	}
	if resp.Status.Clusters[0].ClusterName != "ws-alice" {
		t.Errorf("ClusterName = %q, want ws-alice", resp.Status.Clusters[0].ClusterName)
	}
	if resp.Status.Clusters[0].Endpoint != endpoint("ws-alice") {
		t.Errorf("Endpoint = %q, want %q",
			resp.Status.Clusters[0].Endpoint, endpoint("ws-alice"))
	}
}

func TestHandler_groupAccess(t *testing.T) {
	g := newReadyGraph(t, []grant{
		{graph.SubjectKindGroup, "eng", "ws-eng-1"},
		{graph.SubjectKindGroup, "eng", "ws-eng-2"},
	})
	h := scar.Handler(g, testResolver)

	rr := doSCAR(h, http.MethodPost, map[string][]string{
		"X-Remote-User":  {"alice"},
		"X-Remote-Group": {"eng"},
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rr.Code)
	}

	var resp scar.SelfClusterAccessReview
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if got, want := len(resp.Status.Clusters), 2; got != want {
		t.Errorf("len(Clusters) = %d, want %d; body: %s", got, want, rr.Body.String())
	}
}

func TestHandler_multipleGroupHeaders(t *testing.T) {
	g := newReadyGraph(t, []grant{
		{graph.SubjectKindGroup, "eng", "ws-eng"},
		{graph.SubjectKindGroup, "platform", "ws-platform"},
	})
	h := scar.Handler(g, testResolver)

	rr := doSCAR(h, http.MethodPost, map[string][]string{
		"X-Remote-User":  {"alice"},
		"X-Remote-Group": {"eng", "platform"},
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rr.Code)
	}

	var resp scar.SelfClusterAccessReview
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if got, want := len(resp.Status.Clusters), 2; got != want {
		t.Errorf("len(Clusters) = %d, want %d", got, want)
	}
}

func TestHandler_unknownUserReturnsEmpty(t *testing.T) {
	g := newReadyGraph(t, []grant{
		{graph.SubjectKindUser, "alice", "ws-alice"},
	})
	h := scar.Handler(g, testResolver)

	rr := doSCAR(h, http.MethodPost, map[string][]string{"X-Remote-User": {"nobody"}})

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rr.Code)
	}

	var resp scar.SelfClusterAccessReview
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if len(resp.Status.Clusters) != 0 {
		t.Errorf("expected empty Clusters, got %v", resp.Status.Clusters)
	}
}

func TestHandler_contentTypeIsJSON(t *testing.T) {
	g := newReadyGraph(t, nil)
	h := scar.Handler(g, testResolver)

	rr := doSCAR(h, http.MethodPost, map[string][]string{"X-Remote-User": {"alice"}})

	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}
