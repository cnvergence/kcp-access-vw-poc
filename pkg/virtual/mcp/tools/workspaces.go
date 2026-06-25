package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// WorkspaceInfo represents a single workspace in the list_workspaces output.
type WorkspaceInfo struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
}

// ListWorkspacesInput is the input schema for list_workspaces (empty).
type ListWorkspacesInput struct{}

// ListWorkspacesOutput is the output schema for list_workspaces.
type ListWorkspacesOutput struct {
	Workspaces []WorkspaceInfo `json:"workspaces"`
}

func registerListWorkspaces(server *mcp.Server, scope Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_workspaces",
		Description: "List kcp workspaces the authenticated user has access to. Returns workspace IDs and their API endpoints.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListWorkspacesInput) (*mcp.CallToolResult, ListWorkspacesOutput, error) {
		clusters := scope.Clusters()
		workspaces := make([]WorkspaceInfo, len(clusters))
		for i, c := range clusters {
			workspaces[i] = WorkspaceInfo{
				ID:       c.ClusterName,
				Endpoint: c.Endpoint,
			}
		}
		return nil, ListWorkspacesOutput{Workspaces: workspaces}, nil
	})
}
