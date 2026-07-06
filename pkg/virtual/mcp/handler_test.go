package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual/auth"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual/mcp"
)

// stubResolver always returns a fixed identity or error.
type stubResolver struct {
	id  *auth.Identity
	err error
}

func (s *stubResolver) Resolve(_ context.Context, _ *http.Request) (*auth.Identity, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.id, nil
}

// parseSSEResponse extracts the first JSON-RPC response from an SSE body.
func parseSSEResponse(t *testing.T, body string) map[string]any {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var resp map[string]any
			if err := json.Unmarshal([]byte(data), &resp); err != nil {
				t.Fatalf("unmarshal SSE data: %v\nraw: %s", err, data)
			}
			return resp
		}
	}
	t.Fatalf("no SSE data line found in body:\n%s", body)
	return nil
}

func newMCPRequest(t *testing.T, method string) *http.Request {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q}`, method)
	req := httptest.NewRequest(http.MethodPost, mcp.Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer test-token")
	return req
}

func TestHandler_GraphNotReady(t *testing.T) {
	g := graph.New() // not ready

	mux := http.NewServeMux()
	cf := mustClientFactory(t)
	mcp.Register(mux, g, &stubResolver{
		id: &auth.Identity{Username: "alice"},
	}, &mcp.Options{
		EndpointBase:  "https://kcp.example/clusters/",
		ClientFactory: cf,
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newMCPRequest(t, "tools/list"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseSSEResponse(t, rec.Body.String())
	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 error tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "error" {
		t.Errorf("expected error tool, got %q", tool["name"])
	}
}

func TestHandler_AuthFailure(t *testing.T) {
	g := graph.New()
	g.SetReady()

	mux := http.NewServeMux()
	cf := mustClientFactory(t)
	mcp.Register(mux, g, &stubResolver{
		err: fmt.Errorf("no credentials"),
	}, &mcp.Options{
		EndpointBase:  "https://kcp.example/clusters/",
		ClientFactory: cf,
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newMCPRequest(t, "tools/list"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseSSEResponse(t, rec.Body.String())
	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 error tool, got %d", len(tools))
	}
}

func TestHandler_AuthenticatedUser(t *testing.T) {
	g := graph.New()
	g.Grant(graph.User("alice"), graph.LogicalCluster("ws1"), "https://kcp.example/clusters/ws1")
	g.SetReady()

	mux := http.NewServeMux()
	cf := mustClientFactory(t)
	mcp.Register(mux, g, &stubResolver{
		id: &auth.Identity{Username: "alice", Groups: []string{"system:authenticated"}},
	}, &mcp.Options{
		EndpointBase:  "https://kcp.example/clusters/",
		ClientFactory: cf,
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newMCPRequest(t, "tools/list"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseSSEResponse(t, rec.Body.String())
	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)
	// Should have more than 1 tool (all registered tools, not just error)
	if len(tools) <= 1 {
		t.Errorf("expected multiple tools for authenticated user, got %d", len(tools))
	}
}

func mustClientFactory(t *testing.T) *mcp.ClientFactory {
	t.Helper()
	cf, err := mcp.NewClientFactoryFromHost("https://localhost:6443")
	if err != nil {
		t.Fatalf("create client factory: %v", err)
	}
	return cf
}
