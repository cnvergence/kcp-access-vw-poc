package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer creates an MCP server with tools bound to the given scope.
func NewServer(scope *WorkspaceScope) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "kcp-access-mcp",
		Version: "v1alpha1",
	}, nil)

	// Register tools - for now, just an empty server
	// Tools will be added in Phase 3
	registerTools(server, scope)

	return server
}

// registerTools adds all MCP tools to the server, scoped to the caller's
// accessible workspaces.
func registerTools(server *mcp.Server, scope *WorkspaceScope) {
	// Phase 3 will add:
	// - list_workspaces
	// - get_resource
	// - list_resources

	// For now, register a simple ping tool to verify the handler works
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_workspaces",
		Description: "List kcp workspaces the authenticated user has access to",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, ListWorkspacesOutput, error) {
		workspaces := make([]WorkspaceInfo, len(scope.Clusters))
		for i, c := range scope.Clusters {
			workspaces[i] = WorkspaceInfo{
				ID:       c.ClusterName,
				Endpoint: c.Endpoint,
			}
		}
		return nil, ListWorkspacesOutput{Workspaces: workspaces}, nil
	})
}

// WorkspaceInfo represents a single workspace in the list_workspaces output.
type WorkspaceInfo struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
}

// ListWorkspacesOutput is the output schema for list_workspaces.
type ListWorkspacesOutput struct {
	Workspaces []WorkspaceInfo `json:"workspaces"`
}
