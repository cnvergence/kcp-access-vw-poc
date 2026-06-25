package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// kcp APIs GVRs
var (
	apiExportGVR = schema.GroupVersionResource{
		Group:    "apis.kcp.io",
		Version:  "v1alpha1",
		Resource: "apiexports",
	}
)

// APIExportInfo represents an APIExport in list output.
type APIExportInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Resources   []string `json:"resources,omitempty"`
}

// ListAPIExportsInput is the input for list_apiexports.
type ListAPIExportsInput struct {
	Workspace string `json:"workspace"`
}

// ListAPIExportsOutput is the output for list_apiexports.
type ListAPIExportsOutput struct {
	APIExports []APIExportInfo `json:"apiExports"`
	Count      int             `json:"count"`
}

// GetAPIExportInput is the input for get_apiexport.
type GetAPIExportInput struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
}

// GetAPIExportOutput is the output for get_apiexport.
type GetAPIExportOutput struct {
	Object map[string]any `json:"object"`
}

func registerAPIExportTools(server *mcp.Server, scope Scope) {
	// list_apiexports
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_apiexports",
		Description: `List APIExports in a kcp workspace.
APIExports define APIs that can be consumed by other workspaces via APIBindings.
They are the foundation of kcp's multi-tenant API sharing model.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_workspaces)",
				},
			},
			"required": []string{"workspace"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListAPIExportsInput) (*mcp.CallToolResult, ListAPIExportsOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListAPIExportsOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListAPIExportsOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(apiExportGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListAPIExportsOutput{}, fmt.Errorf("listing APIExports: %w", err)
		}

		items := extractListItems(list)
		exports := make([]APIExportInfo, 0, len(items))
		for _, item := range items {
			info := APIExportInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			if spec, ok := item["spec"].(map[string]any); ok {
				if desc, ok := spec["description"].(string); ok {
					info.Description = desc
				}
			}
			exports = append(exports, info)
		}

		return nil, ListAPIExportsOutput{
			APIExports: exports,
			Count:      len(exports),
		}, nil
	})

	// get_apiexport
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_apiexport",
		Description: `Get a specific APIExport by name from a workspace.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "APIExport name",
				},
			},
			"required": []string{"workspace", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetAPIExportInput) (*mcp.CallToolResult, GetAPIExportOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, GetAPIExportOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, GetAPIExportOutput{}, fmt.Errorf("getting client: %w", err)
		}

		obj, err := dynClient.Resource(apiExportGVR).Get(ctx, input.Name, metav1.GetOptions{})
		if err != nil {
			return nil, GetAPIExportOutput{}, fmt.Errorf("getting APIExport: %w", err)
		}

		return nil, GetAPIExportOutput{Object: extractObject(obj)}, nil
	})
}
