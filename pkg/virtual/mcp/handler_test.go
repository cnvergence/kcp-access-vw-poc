package mcp_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual/mcp"
)

// withUser wraps a handler and injects the given user into the request
// context, standing in for the root apiserver's authentication filter.
// A nil user simulates the filter not having run.
func withUser(h http.Handler, u user.Info) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u != nil {
			r = r.WithContext(genericapirequest.WithUser(r.Context(), u))
		}
		h.ServeHTTP(w, r)
	})
}

// parseSSEResponse extracts the first JSON-RPC response from an SSE body.
func parseSSEResponse(t *testing.T, body string) map[string]any {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1 MB to handle large tools/list responses
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
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}
	t.Fatalf("no SSE data line found in body:\n%s", body)
	return nil
}

func newMCPRequest(t *testing.T, method string) *http.Request {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q}`, method)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	return req
}

// listTools serves a tools/list request against the handler and returns
// the tools array.
func listTools(t *testing.T, h http.Handler) []any {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newMCPRequest(t, "tools/list"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := parseSSEResponse(t, rec.Body.String())
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in response: %v", resp)
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("no tools in result: %v", result)
	}
	return tools
}

func TestHandler_GraphNotReady(t *testing.T) {
	g := graph.New() // not ready

	h := withUser(mcp.NewHandler(g, mustClientFactory(t)), &user.DefaultInfo{Name: "alice"})

	tools := listTools(t, h)
	if len(tools) != 1 {
		t.Fatalf("expected 1 error tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "error" {
		t.Errorf("expected error tool, got %q", tool["name"])
	}
}

func TestHandler_MissingUser(t *testing.T) {
	g := graph.New()
	g.SetReady()

	// No user in the request context — simulates the authentication
	// filter not having run.
	h := withUser(mcp.NewHandler(g, mustClientFactory(t)), nil)

	tools := listTools(t, h)
	if len(tools) != 1 {
		t.Fatalf("expected 1 error tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "error" {
		t.Errorf("expected error tool, got %q", tool["name"])
	}
}

func TestHandler_AuthenticatedUser(t *testing.T) {
	g := graph.New()
	g.Grant(graph.User("alice"), graph.LogicalCluster("ws1"), "https://kcp.example/clusters/ws1")
	g.SetReady()

	h := withUser(mcp.NewHandler(g, mustClientFactory(t)), &user.DefaultInfo{
		Name:   "alice",
		Groups: []string{"system:authenticated"},
	})

	tools := listTools(t, h)
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
