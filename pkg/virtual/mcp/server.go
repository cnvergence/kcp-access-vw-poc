package mcp

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cnvergence/kcp-access-vw/pkg/virtual/mcp/tools"
)

// NewServer creates an MCP server with tools bound to the given scope.
func NewServer(scope *WorkspaceScope) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "kcp-access-mcp",
		Version: "v1alpha1",
	}, nil)

	tools.RegisterAll(server, scope)

	return server
}
