package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ListResourcesInput is the input for list_resources.
// Supports two modes:
// 1. apiVersion + kind (user-friendly, like kubectl)
// 2. group + version + resource (explicit GVR)
type ListResourcesInput struct {
	Workspace string `json:"workspace"`

	// Mode 1: apiVersion + kind (e.g., "apps/v1" + "Deployment")
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`

	// Mode 2: explicit GVR (e.g., group="apps", version="v1", resource="deployments")
	Group    string `json:"group,omitempty"`
	Version  string `json:"version,omitempty"`
	Resource string `json:"resource,omitempty"`

	// Filtering
	Namespace     string `json:"namespace,omitempty"`
	LabelSelector string `json:"labelSelector,omitempty"`
	FieldSelector string `json:"fieldSelector,omitempty"`
}

// ListResourcesOutput is the output for list_resources.
type ListResourcesOutput struct {
	Items []map[string]any `json:"items"`
	Count int              `json:"count"`
}

// parseGVR parses the input to determine the GVR to use.
// Supports apiVersion+kind (user-friendly) or explicit group+version+resource.
func parseGVR(apiVersion, kind, group, version, resource string) (schema.GroupVersionResource, error) {
	// Mode 1: apiVersion + kind
	if apiVersion != "" && kind != "" {
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			return schema.GroupVersionResource{}, fmt.Errorf("invalid apiVersion %q: %w", apiVersion, err)
		}
		// Convert kind to resource (lowercase + pluralize)
		// This is a simplification - in real K8s you'd use RESTMapper
		res := strings.ToLower(kind)
		if !strings.HasSuffix(res, "s") {
			res += "s"
		}
		// Handle common irregular plurals
		switch kind {
		case "Ingress":
			res = "ingresses"
		case "NetworkPolicy":
			res = "networkpolicies"
		case "PodSecurityPolicy":
			res = "podsecuritypolicies"
		case "StorageClass":
			res = "storageclasses"
		case "IngressClass":
			res = "ingressclasses"
		case "Endpoints":
			res = "endpoints"
		}
		return schema.GroupVersionResource{
			Group:    gv.Group,
			Version:  gv.Version,
			Resource: res,
		}, nil
	}

	// Mode 2: explicit GVR
	if version != "" && resource != "" {
		return schema.GroupVersionResource{
			Group:    group,
			Version:  version,
			Resource: resource,
		}, nil
	}

	return schema.GroupVersionResource{}, fmt.Errorf("provide either apiVersion+kind or group+version+resource")
}

// resourceLabel returns a human-readable label for a resource type.
// Uses kind if provided, falls back to the GVR resource name.
func resourceLabel(kind, resource string) string {
	if kind != "" {
		return kind
	}
	return resource
}

func registerListResources(server *mcp.Server, scope Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_resources",
		Description: `List Kubernetes resources in a workspace.

Supports two input modes:
1. apiVersion + kind (user-friendly, like kubectl):
   - apiVersion: "v1", "apps/v1", "networking.k8s.io/v1"
   - kind: "Pod", "Deployment", "Service", "ConfigMap"

2. group + version + resource (explicit GVR):
   - group: "" (core), "apps", "networking.k8s.io"
   - version: "v1", "v1beta1"
   - resource: "pods", "deployments", "services"

Common apiVersion + kind combinations:
- v1 Pod, v1 Service, v1 ConfigMap, v1 Secret, v1 Namespace
- apps/v1 Deployment, apps/v1 StatefulSet, apps/v1 DaemonSet
- networking.k8s.io/v1 Ingress, networking.k8s.io/v1 NetworkPolicy
- apis.kcp.io/v1alpha1 APIBinding, apis.kcp.io/v1alpha1 APIExport

Use labelSelector for filtering by labels (e.g., "app=nginx,env=prod").
Use fieldSelector for filtering by fields (e.g., "status.phase=Running").`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
				"apiVersion": map[string]any{
					"type":        "string",
					"description": "API version (e.g., 'v1', 'apps/v1'). Use with kind.",
				},
				"kind": map[string]any{
					"type":        "string",
					"description": "Resource kind (e.g., 'Pod', 'Deployment'). Use with apiVersion.",
				},
				"group": map[string]any{
					"type":        "string",
					"description": "API group (empty for core). Use with version+resource.",
				},
				"version": map[string]any{
					"type":        "string",
					"description": "API version. Use with group+resource.",
				},
				"resource": map[string]any{
					"type":        "string",
					"description": "Resource name plural (e.g., 'pods'). Use with group+version.",
				},
				"namespace": map[string]any{
					"type":        "string",
					"description": "Namespace (optional, omit for cluster-scoped or all namespaces)",
				},
				"labelSelector": map[string]any{
					"type":        "string",
					"description": "Label selector (e.g., 'app=nginx,env=prod')",
				},
				"fieldSelector": map[string]any{
					"type":        "string",
					"description": "Field selector (e.g., 'status.phase=Running')",
				},
			},
			"required":             []string{"workspace"},
			"additionalProperties": false,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListResourcesInput) (*mcp.CallToolResult, ListResourcesOutput, error) {
		// Check scope
		if !scope.HasAccess(input.Workspace) {
			return nil, ListResourcesOutput{}, NewScopeError(input.Workspace, scope)
		}

		// Get client
		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListResourcesOutput{}, fmt.Errorf("getting client: %w", err)
		}

		// Parse GVR
		gvr, err := parseGVR(input.APIVersion, input.Kind, input.Group, input.Version, input.Resource)
		if err != nil {
			return nil, ListResourcesOutput{}, err
		}

		// Build list options
		listOpts := metav1.ListOptions{}
		if input.LabelSelector != "" {
			listOpts.LabelSelector = input.LabelSelector
		}
		if input.FieldSelector != "" {
			listOpts.FieldSelector = input.FieldSelector
		}

		// List resources using dynamic client
		var list any
		if input.Namespace != "" {
			list, err = dynClient.Resource(gvr).Namespace(input.Namespace).List(ctx, listOpts)
		} else {
			list, err = dynClient.Resource(gvr).List(ctx, listOpts)
		}
		if err != nil {
			return nil, ListResourcesOutput{}, fmt.Errorf("listing resources: %w", err)
		}

		// Extract items from the unstructured list
		items := extractListItems(list)

		return nil, ListResourcesOutput{
			Items: items,
			Count: len(items),
		}, nil
	})
}

// GetResourceInput is the input for get_resource.
type GetResourceInput struct {
	Workspace string `json:"workspace"`

	// Mode 1: apiVersion + kind
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`

	// Mode 2: explicit GVR
	Group    string `json:"group,omitempty"`
	Version  string `json:"version,omitempty"`
	Resource string `json:"resource,omitempty"`

	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// GetResourceOutput is the output for get_resource.
type GetResourceOutput struct {
	Object map[string]any `json:"object"`
}

func registerGetResource(server *mcp.Server, scope Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_resource",
		Description: `Get a specific Kubernetes resource by name from a workspace.

Supports two input modes:
1. apiVersion + kind (e.g., "apps/v1" + "Deployment")
2. group + version + resource (explicit GVR)

See list_resources for common apiVersion+kind combinations.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
				"apiVersion": map[string]any{
					"type":        "string",
					"description": "API version (e.g., 'v1', 'apps/v1'). Use with kind.",
				},
				"kind": map[string]any{
					"type":        "string",
					"description": "Resource kind (e.g., 'Pod', 'Deployment'). Use with apiVersion.",
				},
				"group": map[string]any{
					"type":        "string",
					"description": "API group. Use with version+resource.",
				},
				"version": map[string]any{
					"type":        "string",
					"description": "API version. Use with group+resource.",
				},
				"resource": map[string]any{
					"type":        "string",
					"description": "Resource name plural. Use with group+version.",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Resource name",
				},
				"namespace": map[string]any{
					"type":        "string",
					"description": "Namespace (optional)",
				},
			},
			"required": []string{"workspace", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetResourceInput) (*mcp.CallToolResult, GetResourceOutput, error) {
		// Check scope
		if !scope.HasAccess(input.Workspace) {
			return nil, GetResourceOutput{}, NewScopeError(input.Workspace, scope)
		}

		// Get client
		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, GetResourceOutput{}, fmt.Errorf("getting client: %w", err)
		}

		// Parse GVR
		gvr, err := parseGVR(input.APIVersion, input.Kind, input.Group, input.Version, input.Resource)
		if err != nil {
			return nil, GetResourceOutput{}, err
		}

		// Get resource using dynamic client
		var obj any
		if input.Namespace != "" {
			obj, err = dynClient.Resource(gvr).Namespace(input.Namespace).Get(ctx, input.Name, metav1.GetOptions{})
		} else {
			obj, err = dynClient.Resource(gvr).Get(ctx, input.Name, metav1.GetOptions{})
		}
		if err != nil {
			return nil, GetResourceOutput{}, fmt.Errorf("getting resource: %w", err)
		}

		return nil, GetResourceOutput{Object: extractObject(obj)}, nil
	})
}

// extractListItems extracts items from an unstructured list.
// The dynamic client returns *unstructured.UnstructuredList.
func extractListItems(list any) []map[string]any {
	// Use type assertion to get UnstructuredContent
	if ul, ok := list.(interface{ UnstructuredContent() map[string]any }); ok {
		content := ul.UnstructuredContent()
		if items, ok := content["items"].([]any); ok {
			result := make([]map[string]any, 0, len(items))
			for _, item := range items {
				if m, ok := item.(map[string]any); ok {
					result = append(result, m)
				}
			}
			return result
		}
	}
	return nil
}

// extractObject extracts the object map from an unstructured resource.
func extractObject(obj any) map[string]any {
	if u, ok := obj.(interface{ UnstructuredContent() map[string]any }); ok {
		return u.UnstructuredContent()
	}
	return nil
}
