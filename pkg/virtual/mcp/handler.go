// Package mcp implements the MCP Virtual Workspace.
//
// The handler serves the Model Context Protocol (MCP) over streamable
// HTTP. Authentication has already happened by the time a request gets
// here — the virtual-workspace root apiserver's filter chain resolves
// the caller (front-proxy requestheader certs, bearer-token TokenReview,
// or client certs) and stores the identity in the request context. The
// handler builds a per-request WorkspaceScope from the shared access
// graph and exposes tools scoped to the caller's authorized workspaces.
//
// Like the SCAR storage, this is intentionally thin: no authorization
// decisions (the graph populator made those observable, and kcp
// re-authorizes every per-workspace call), no caching, no batching.
// The graph is the seam; the handler projects it into MCP.
package mcp

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
)

// NewHandler returns the streamable-HTTP MCP handler. The handler is
// stateless: each request rebuilds the WorkspaceScope from the identity
// in the request context and the current graph state.
func NewHandler(g *graph.Graph, factory *ClientFactory) http.Handler {
	if factory == nil {
		panic("MCP handler requires a ClientFactory")
	}

	return mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			return serverForRequest(r, g, factory)
		},
		&mcp.StreamableHTTPOptions{
			Stateless: true, // Per SEP-1442/2322: stateless from day one
		},
	)
}

// serverForRequest builds an MCP server scoped to the authenticated
// caller. Returns an error server if the identity is missing or the
// graph is not ready, so MCP clients see a useful message rather than
// a connection failure.
func serverForRequest(r *http.Request, g *graph.Graph, factory *ClientFactory) *mcp.Server {
	if !g.Ready() {
		return errorServer("access graph is not ready; try again shortly")
	}

	u, ok := genericapirequest.UserFrom(r.Context())
	if !ok {
		// The apiserver filter chain authenticates before delegation;
		// a missing user here means a wiring bug, not a client error.
		klog.Error("mcp: no user in request context; authentication filter did not run?")
		return errorServer("no authenticated user in request context")
	}

	clusters := g.ClustersFor(u.GetName(), u.GetGroups())
	klog.V(4).InfoS("mcp: building scoped server", "user", u.GetName(), "groups", u.GetGroups(), "workspaces", len(clusters))

	scope := &WorkspaceScope{
		User:        u,
		ClusterList: clusters,
		factory:     factory,
	}

	return NewServer(scope)
}

// errorServer returns an MCP server that exposes a single tool describing
// the initialization error. This lets the MCP client see a useful error
// message rather than a connection failure.
func errorServer(msg string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "kcp",
		Version: "v1alpha1",
	}, nil)

	type errorInput struct{}
	type errorOutput struct {
		Error string `json:"error"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "error",
		Description: "Returns server initialization error",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ errorInput) (*mcp.CallToolResult, errorOutput, error) {
		return &mcp.CallToolResult{
			IsError: true,
		}, errorOutput{Error: fmt.Sprintf("MCP server unavailable: %s", msg)}, nil
	})

	return server
}
