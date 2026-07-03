// Package mcp implements the MCP Virtual Workspace HTTP endpoint.
//
// The handler serves the Model Context Protocol (MCP) over streamable HTTP,
// authenticating callers via the virtual workspace's auth.Resolver, building
// a per-request WorkspaceScope from the shared access graph, and exposing
// tools scoped to the caller's authorized workspaces.
//
// Like the SCAR handler, this is intentionally thin: no authorization
// decisions (those happened in the graph populator), no caching, no batching.
// The graph is the seam; the handler projects it into MCP.
package mcp

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual/auth"
)

// Path is the canonical URL path the handler is registered at when
// served behind kcp's FrontProxy.
const Path = "/services/access-virtual-workspace/mcp"

// Options configures the MCP handler.
type Options struct {
	// EndpointBase is the FrontProxy URL prefix for workspace endpoints.
	EndpointBase string

	// ClientFactory produces K8s clients. Shared across all requests to
	// reuse TLS connections.
	ClientFactory *ClientFactory
}

// Register mounts the MCP handler on the supplied mux using the
// canonical Path. The handler is stateless: each request re-authenticates
// and rebuilds the WorkspaceScope.
func Register(mux *http.ServeMux, g *graph.Graph, resolver auth.Resolver, opts *Options) {
	if opts == nil {
		opts = &Options{}
	}
	if opts.ClientFactory == nil {
		panic("MCP handler requires ClientFactory")
	}

	handler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			return serverForRequest(r, g, resolver, opts)
		},
		&mcp.StreamableHTTPOptions{
			Stateless: true, // Per SEP-1442/2322: stateless from day one
		},
	)

	mux.Handle(Path, handler)
}

// serverForRequest builds an MCP server scoped to the authenticated caller.
// Returns an error server if authentication or graph readiness fails.
func serverForRequest(r *http.Request, g *graph.Graph, resolver auth.Resolver, opts *Options) *mcp.Server {
	if !g.Ready() {
		return errorServer("access graph is not ready; try again shortly")
	}

	id, err := resolver.Resolve(r.Context(), r)
	if err != nil {
		log.Printf("mcp: auth failed: %v", err)
		return errorServer("authentication failed")
	}

	token := auth.BearerTokenFromRequest(r)
	if token == "" {
		log.Printf("mcp: no bearer token for user %s", id.Username)
		return errorServer("missing bearer token")
	}

	log.Printf("mcp: authenticated user=%s groups=%v token_len=%d",
		id.Username, id.Groups, len(token))

	clusters := g.ClustersFor(id.Username, id.Groups)
	log.Printf("mcp: user=%s has access to %d workspaces", id.Username, len(clusters))

	scope := &WorkspaceScope{
		User:        id.Username,
		Groups:      id.Groups,
		Token:       token,
		ClusterList: clusters,
		factory:     opts.ClientFactory,
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
		return nil, errorOutput{}, fmt.Errorf("MCP server unavailable: %s", msg)
	})

	return server
}
