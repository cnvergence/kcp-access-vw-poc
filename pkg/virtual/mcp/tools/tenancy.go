package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// kcp tenancy.kcp.io GVRs
var (
	workspaceTypeGVR = schema.GroupVersionResource{
		Group:    "tenancy.kcp.io",
		Version:  "v1alpha1",
		Resource: "workspacetypes",
	}
)

// WorkspaceTypeInfo represents a WorkspaceType in list output.
type WorkspaceTypeInfo struct {
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	Initializers   []string `json:"initializers,omitempty"`
	DefaultAPIPath string   `json:"defaultAPIPath,omitempty"`
}

// ListWorkspaceTypesInput is the input for list_workspacetypes.
type ListWorkspaceTypesInput struct {
	Workspace string `json:"workspace"`
}

// ListWorkspaceTypesOutput is the output for list_workspacetypes.
type ListWorkspaceTypesOutput struct {
	WorkspaceTypes []WorkspaceTypeInfo `json:"workspaceTypes"`
	Count          int                 `json:"count"`
}

// GetWorkspaceTypeInput is the input for get_workspacetype.
type GetWorkspaceTypeInput struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
}

// GetWorkspaceTypeOutput is the output for get_workspacetype.
type GetWorkspaceTypeOutput struct {
	Object map[string]any `json:"object"`
}

func registerTenancyTools(server *mcp.Server, scope Scope) {
	// list_workspacetypes
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_kcp_workspacetypes",
		Description: `List WorkspaceTypes in a kcp workspace.
WorkspaceTypes define templates for creating new workspaces, including:
- Which initializers run when a workspace is created
- Default API bindings
- Allowed child workspace types`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
			},
			"required": []string{"workspace"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListWorkspaceTypesInput) (*mcp.CallToolResult, ListWorkspaceTypesOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListWorkspaceTypesOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListWorkspaceTypesOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(workspaceTypeGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListWorkspaceTypesOutput{}, fmt.Errorf("listing WorkspaceTypes: %w", err)
		}

		items := extractListItems(list)
		types := make([]WorkspaceTypeInfo, 0, len(items))
		for _, item := range items {
			info := WorkspaceTypeInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			if spec, ok := item["spec"].(map[string]any); ok {
				if desc, ok := spec["description"].(string); ok {
					info.Description = desc
				}
				if inits, ok := spec["initializers"].([]any); ok {
					info.Initializers = make([]string, 0, len(inits))
					for _, init := range inits {
						if s, ok := init.(string); ok {
							info.Initializers = append(info.Initializers, s)
						}
					}
				}
			}
			types = append(types, info)
		}

		return nil, ListWorkspaceTypesOutput{
			WorkspaceTypes: types,
			Count:          len(types),
		}, nil
	})

	// get_workspacetype
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_kcp_workspacetype",
		Description: `Get a specific WorkspaceType by name from a workspace.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "WorkspaceType name",
				},
			},
			"required": []string{"workspace", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetWorkspaceTypeInput) (*mcp.CallToolResult, GetWorkspaceTypeOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, GetWorkspaceTypeOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, GetWorkspaceTypeOutput{}, fmt.Errorf("getting client: %w", err)
		}

		obj, err := dynClient.Resource(workspaceTypeGVR).Get(ctx, input.Name, metav1.GetOptions{})
		if err != nil {
			return nil, GetWorkspaceTypeOutput{}, fmt.Errorf("getting WorkspaceType: %w", err)
		}

		return nil, GetWorkspaceTypeOutput{Object: extractObject(obj)}, nil
	})
}
