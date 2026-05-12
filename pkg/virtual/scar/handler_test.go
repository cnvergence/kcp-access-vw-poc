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

func TestHandler_errors(t *testing.T) {
	tests := []struct {
		name     string
		ready    bool
		method   string
		headers  map[string][]string
		wantCode int
	}{
		{
			name:     "GET not allowed",
			ready:    true,
			method:   http.MethodGet,
			headers:  map[string][]string{"X-Remote-User": {"alice"}},
			wantCode: http.StatusMethodNotAllowed,
		},
		{
			name:     "PUT not allowed",
			ready:    true,
			method:   http.MethodPut,
			headers:  map[string][]string{"X-Remote-User": {"alice"}},
			wantCode: http.StatusMethodNotAllowed,
		},
		{
			name:     "DELETE not allowed",
			ready:    true,
			method:   http.MethodDelete,
			headers:  map[string][]string{"X-Remote-User": {"alice"}},
			wantCode: http.StatusMethodNotAllowed,
		},
		{
			name:     "PATCH not allowed",
			ready:    true,
			method:   http.MethodPatch,
			headers:  map[string][]string{"X-Remote-User": {"alice"}},
			wantCode: http.StatusMethodNotAllowed,
		},
		{
			name:     "missing user returns 401",
			ready:    true,
			method:   http.MethodPost,
			headers:  nil,
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "not ready returns 503",
			ready:    false,
			method:   http.MethodPost,
			headers:  map[string][]string{"X-Remote-User": {"alice"}},
			wantCode: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := graph.New()
			if tt.ready {
				g.SetReady()
			}
			h := scar.Handler(g, testResolver)
			rr := doSCAR(h, tt.method, tt.headers)
			if rr.Code != tt.wantCode {
				t.Errorf("got %d, want %d", rr.Code, tt.wantCode)
			}
		})
	}
}

func TestHandler_success(t *testing.T) {
	tests := []struct {
		name         string
		grants       []grant
		headers      map[string][]string
		wantClusters int
	}{
		{
			name:         "direct user access",
			grants:       []grant{{graph.SubjectKindUser, "alice", "ws-alice"}},
			headers:      map[string][]string{"X-Remote-User": {"alice"}},
			wantClusters: 1,
		},
		{
			name: "group access",
			grants: []grant{
				{graph.SubjectKindGroup, "eng", "ws-eng-1"},
				{graph.SubjectKindGroup, "eng", "ws-eng-2"},
			},
			headers:      map[string][]string{"X-Remote-User": {"alice"}, "X-Remote-Group": {"eng"}},
			wantClusters: 2,
		},
		{
			name: "multiple group headers",
			grants: []grant{
				{graph.SubjectKindGroup, "eng", "ws-eng"},
				{graph.SubjectKindGroup, "platform", "ws-platform"},
			},
			headers:      map[string][]string{"X-Remote-User": {"alice"}, "X-Remote-Group": {"eng", "platform"}},
			wantClusters: 2,
		},
		{
			name:         "unknown user returns empty clusters",
			grants:       []grant{{graph.SubjectKindUser, "alice", "ws-alice"}},
			headers:      map[string][]string{"X-Remote-User": {"nobody"}},
			wantClusters: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newReadyGraph(t, tt.grants)
			h := scar.Handler(g, testResolver)
			rr := doSCAR(h, http.MethodPost, tt.headers)

			if rr.Code != http.StatusOK {
				t.Fatalf("got %d, want 200; body: %s", rr.Code, rr.Body.String())
			}

			var resp scar.SelfClusterAccessReview
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode body: %v", err)
			}

			if resp.Kind != scar.Kind || resp.APIVersion != scar.APIVersion {
				t.Errorf("Kind/APIVersion = %q/%q, want %q/%q",
					resp.Kind, resp.APIVersion, scar.Kind, scar.APIVersion)
			}

			if got := len(resp.Status.Clusters); got != tt.wantClusters {
				t.Errorf("len(Clusters) = %d, want %d", got, tt.wantClusters)
			}
		})
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
