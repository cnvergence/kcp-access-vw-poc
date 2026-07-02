package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// kcp apis.kcp.io GVRs
var (
	apiBindingGVR = schema.GroupVersionResource{
		Group:    "apis.kcp.io",
		Version:  "v1alpha1",
		Resource: "apibindings",
	}

	apiConversionGVR = schema.GroupVersionResource{
		Group:    "apis.kcp.io",
		Version:  "v1alpha1",
		Resource: "apiconversions",
	}

	apiExportEndpointSliceGVR = schema.GroupVersionResource{
		Group:    "apis.kcp.io",
		Version:  "v1alpha1",
		Resource: "apiexportendpointslices",
	}
)

// APIBindingInfo represents an APIBinding in list output.
type APIBindingInfo struct {
	Name       string `json:"name"`
	ExportName string `json:"exportName,omitempty"`
	ExportPath string `json:"exportPath,omitempty"`
	Phase      string `json:"phase,omitempty"`
}

// ListAPIBindingsInput is the input for list_apibindings.
type ListAPIBindingsInput struct {
	Workspace string `json:"workspace"`
}

// ListAPIBindingsOutput is the output for list_apibindings.
type ListAPIBindingsOutput struct {
	APIBindings []APIBindingInfo `json:"apiBindings"`
	Count       int              `json:"count"`
}

// GetAPIBindingInput is the input for get_apibinding.
type GetAPIBindingInput struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
}

// GetAPIBindingOutput is the output for get_apibinding.
type GetAPIBindingOutput struct {
	Object map[string]any `json:"object"`
}

// APIConversionInfo represents an APIConversion in list output.
type APIConversionInfo struct {
	Name string `json:"name"`
}

// ListAPIConversionsInput is the input for list_apiconversions.
type ListAPIConversionsInput struct {
	Workspace string `json:"workspace"`
}

// ListAPIConversionsOutput is the output for list_apiconversions.
type ListAPIConversionsOutput struct {
	APIConversions []APIConversionInfo `json:"apiConversions"`
	Count          int                 `json:"count"`
}

// APIExportEndpointSliceInfo represents an APIExportEndpointSlice in list output.
type APIExportEndpointSliceInfo struct {
	Name     string `json:"name"`
	Export   string `json:"export,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
}

// ListAPIExportEndpointSlicesInput is the input for list_apiexportendpointslices.
type ListAPIExportEndpointSlicesInput struct {
	Workspace string `json:"workspace"`
}

// ListAPIExportEndpointSlicesOutput is the output for list_apiexportendpointslices.
type ListAPIExportEndpointSlicesOutput struct {
	Slices []APIExportEndpointSliceInfo `json:"slices"`
	Count  int                          `json:"count"`
}

func registerAPIBindingTools(server *mcp.Server, scope Scope) {
	// list_apibindings
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_kcp_apibindings",
		Description: `List APIBindings in a kcp workspace.
APIBindings connect a workspace to APIs exported by another workspace via APIExport.
They are how consumers access shared APIs in kcp's multi-tenant model.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListAPIBindingsInput) (*mcp.CallToolResult, ListAPIBindingsOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListAPIBindingsOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListAPIBindingsOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(apiBindingGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListAPIBindingsOutput{}, fmt.Errorf("listing APIBindings: %w", err)
		}

		items := extractListItems(list)
		bindings := make([]APIBindingInfo, 0, len(items))
		for _, item := range items {
			info := APIBindingInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			if spec, ok := item["spec"].(map[string]any); ok {
				if ref, ok := spec["reference"].(map[string]any); ok {
					if export, ok := ref["export"].(map[string]any); ok {
						if name, ok := export["name"].(string); ok {
							info.ExportName = name
						}
						if path, ok := export["path"].(string); ok {
							info.ExportPath = path
						}
					}
				}
			}
			if status, ok := item["status"].(map[string]any); ok {
				if phase, ok := status["phase"].(string); ok {
					info.Phase = phase
				}
			}
			bindings = append(bindings, info)
		}

		return nil, ListAPIBindingsOutput{
			APIBindings: bindings,
			Count:       len(bindings),
		}, nil
	})

	// get_apibinding
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_kcp_apibinding",
		Description: `Get a specific APIBinding by name from a workspace.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "APIBinding name",
				},
			},
			"required": []string{"workspace", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetAPIBindingInput) (*mcp.CallToolResult, GetAPIBindingOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, GetAPIBindingOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, GetAPIBindingOutput{}, fmt.Errorf("getting client: %w", err)
		}

		obj, err := dynClient.Resource(apiBindingGVR).Get(ctx, input.Name, metav1.GetOptions{})
		if err != nil {
			return nil, GetAPIBindingOutput{}, fmt.Errorf("getting APIBinding: %w", err)
		}

		return nil, GetAPIBindingOutput{Object: extractObject(obj)}, nil
	})

	// list_apiconversions
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_kcp_apiconversions",
		Description: `List APIConversions in a kcp workspace.
APIConversions define how to convert between different versions of an API.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListAPIConversionsInput) (*mcp.CallToolResult, ListAPIConversionsOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListAPIConversionsOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListAPIConversionsOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(apiConversionGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListAPIConversionsOutput{}, fmt.Errorf("listing APIConversions: %w", err)
		}

		items := extractListItems(list)
		conversions := make([]APIConversionInfo, 0, len(items))
		for _, item := range items {
			info := APIConversionInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			conversions = append(conversions, info)
		}

		return nil, ListAPIConversionsOutput{
			APIConversions: conversions,
			Count:          len(conversions),
		}, nil
	})

	// list_apiexportendpointslices
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_kcp_apiexportendpointslices",
		Description: `List APIExportEndpointSlices in a kcp workspace.
APIExportEndpointSlices contain the endpoints where an APIExport's virtual workspace can be accessed.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListAPIExportEndpointSlicesInput) (*mcp.CallToolResult, ListAPIExportEndpointSlicesOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListAPIExportEndpointSlicesOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListAPIExportEndpointSlicesOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(apiExportEndpointSliceGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListAPIExportEndpointSlicesOutput{}, fmt.Errorf("listing APIExportEndpointSlices: %w", err)
		}

		items := extractListItems(list)
		slices := make([]APIExportEndpointSliceInfo, 0, len(items))
		for _, item := range items {
			info := APIExportEndpointSliceInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			if spec, ok := item["spec"].(map[string]any); ok {
				if export, ok := spec["export"].(map[string]any); ok {
					if name, ok := export["name"].(string); ok {
						info.Export = name
					}
				}
			}
			if status, ok := item["status"].(map[string]any); ok {
				if endpoints, ok := status["endpoints"].([]any); ok && len(endpoints) > 0 {
					if ep, ok := endpoints[0].(map[string]any); ok {
						if url, ok := ep["url"].(string); ok {
							info.Endpoint = url
						}
					}
				}
			}
			slices = append(slices, info)
		}

		return nil, ListAPIExportEndpointSlicesOutput{
			Slices: slices,
			Count:  len(slices),
		}, nil
	})
}
