package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	apiResourceSchemaGVR = schema.GroupVersionResource{
		Group:    "apis.kcp.io",
		Version:  "v1alpha1",
		Resource: "apiresourceschemas",
	}
)

// APIResourceSchemaInfo represents an APIResourceSchema in list output.
type APIResourceSchemaInfo struct {
	Name    string `json:"name"`
	Group   string `json:"group,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Version string `json:"version,omitempty"`
	Scope   string `json:"scope,omitempty"` // Cluster or Namespaced
}

// ListAPIResourceSchemasInput is the input for list_apiresourceschemas.
type ListAPIResourceSchemasInput struct {
	Workspace string `json:"workspace"`
}

// ListAPIResourceSchemasOutput is the output for list_apiresourceschemas.
type ListAPIResourceSchemasOutput struct {
	Schemas []APIResourceSchemaInfo `json:"schemas"`
	Count   int                     `json:"count"`
}

// GetAPIResourceSchemaInput is the input for get_apiresourceschema.
type GetAPIResourceSchemaInput struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
}

// GetAPIResourceSchemaOutput is the output for get_apiresourceschema.
type GetAPIResourceSchemaOutput struct {
	Object map[string]any `json:"object"`
}

func registerAPIResourceSchemaTools(server *mcp.Server, scope Scope) {
	// list_apiresourceschemas
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_apiresourceschemas",
		Description: `List APIResourceSchemas in a kcp workspace.
APIResourceSchemas define the schema (CRD-like) for custom resources that can be exported via APIExports.
They contain the OpenAPI schema, validation rules, and other metadata for custom resources.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListAPIResourceSchemasInput) (*mcp.CallToolResult, ListAPIResourceSchemasOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListAPIResourceSchemasOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListAPIResourceSchemasOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(apiResourceSchemaGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListAPIResourceSchemasOutput{}, fmt.Errorf("listing APIResourceSchemas: %w", err)
		}

		items := extractListItems(list)
		schemas := make([]APIResourceSchemaInfo, 0, len(items))
		for _, item := range items {
			info := APIResourceSchemaInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			if spec, ok := item["spec"].(map[string]any); ok {
				if group, ok := spec["group"].(string); ok {
					info.Group = group
				}
				if names, ok := spec["names"].(map[string]any); ok {
					if kind, ok := names["kind"].(string); ok {
						info.Kind = kind
					}
				}
				if versions, ok := spec["versions"].([]any); ok && len(versions) > 0 {
					if v, ok := versions[0].(map[string]any); ok {
						if name, ok := v["name"].(string); ok {
							info.Version = name
						}
					}
				}
				if scope, ok := spec["scope"].(string); ok {
					info.Scope = scope
				}
			}
			schemas = append(schemas, info)
		}

		return nil, ListAPIResourceSchemasOutput{
			Schemas: schemas,
			Count:   len(schemas),
		}, nil
	})

	// get_apiresourceschema
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_apiresourceschema",
		Description: `Get a specific APIResourceSchema by name from a workspace.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "APIResourceSchema name",
				},
			},
			"required": []string{"workspace", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetAPIResourceSchemaInput) (*mcp.CallToolResult, GetAPIResourceSchemaOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, GetAPIResourceSchemaOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, GetAPIResourceSchemaOutput{}, fmt.Errorf("getting client: %w", err)
		}

		obj, err := dynClient.Resource(apiResourceSchemaGVR).Get(ctx, input.Name, metav1.GetOptions{})
		if err != nil {
			return nil, GetAPIResourceSchemaOutput{}, fmt.Errorf("getting APIResourceSchema: %w", err)
		}

		return nil, GetAPIResourceSchemaOutput{Object: extractObject(obj)}, nil
	})
}
