package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
)

// CreateResourceInput is the input for create_resource.
type CreateResourceInput struct {
	Workspace string `json:"workspace"`
	Resource  string `json:"resource"` // YAML or JSON representation
}

// CreateResourceOutput is the output for create_resource.
type CreateResourceOutput struct {
	Created map[string]any `json:"created"`
	Message string         `json:"message"`
}

// UpdateResourceInput is the input for update_resource.
type UpdateResourceInput struct {
	Workspace string `json:"workspace"`
	Resource  string `json:"resource"` // YAML or JSON representation (full resource)
}

// UpdateResourceOutput is the output for update_resource.
type UpdateResourceOutput struct {
	Updated map[string]any `json:"updated"`
	Message string         `json:"message"`
}

// PatchResourceInput is the input for patch_resource.
type PatchResourceInput struct {
	Workspace string `json:"workspace"`

	// Resource identification (apiVersion+kind or GVR)
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Group      string `json:"group,omitempty"`
	Version    string `json:"version,omitempty"`
	Resource   string `json:"resource,omitempty"`

	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`

	// Patch content (JSON merge patch)
	Patch string `json:"patch"`
}

// PatchResourceOutput is the output for patch_resource.
type PatchResourceOutput struct {
	Patched map[string]any `json:"patched"`
	Message string         `json:"message"`
}

// DeleteResourceInput is the input for delete_resource.
type DeleteResourceInput struct {
	Workspace string `json:"workspace"`

	// Resource identification
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Group      string `json:"group,omitempty"`
	Version    string `json:"version,omitempty"`
	Resource   string `json:"resource,omitempty"`

	Name               string `json:"name"`
	Namespace          string `json:"namespace,omitempty"`
	GracePeriodSeconds *int64 `json:"gracePeriodSeconds,omitempty"`
}

// DeleteResourceOutput is the output for delete_resource.
type DeleteResourceOutput struct {
	Message string `json:"message"`
}

// ScaleResourceInput is the input for scale_resource.
type ScaleResourceInput struct {
	Workspace string `json:"workspace"`

	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Group      string `json:"group,omitempty"`
	Version    string `json:"version,omitempty"`
	Resource   string `json:"resource,omitempty"`

	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Replicas  *int32 `json:"replicas,omitempty"` // If nil, just returns current scale
}

// ScaleResourceOutput is the output for scale_resource.
type ScaleResourceOutput struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace,omitempty"`
	CurrentReplicas int32  `json:"currentReplicas"`
	DesiredReplicas int32  `json:"desiredReplicas"`
	Message         string `json:"message"`
}

func registerWriteOperations(server *mcp.Server, scope Scope) {
	registerCreateResource(server, scope)
	registerUpdateResource(server, scope)
	registerPatchResource(server, scope)
	registerDeleteResource(server, scope)
	registerScaleResource(server, scope)
}

func registerCreateResource(server *mcp.Server, scope Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "create_resource",
		Description: `Create a Kubernetes resource in a workspace.

Provide the resource as YAML or JSON with apiVersion, kind, metadata, and spec.

Example YAML:
  apiVersion: v1
  kind: ConfigMap
  metadata:
    name: my-config
    namespace: default
  data:
    key: value

The resource will be created in the specified workspace.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
				"resource": map[string]any{
					"type":        "string",
					"description": "YAML or JSON representation of the Kubernetes resource",
				},
			},
			"required": []string{"workspace", "resource"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input CreateResourceInput) (*mcp.CallToolResult, CreateResourceOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, CreateResourceOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, CreateResourceOutput{}, fmt.Errorf("getting client: %w", err)
		}

		// Parse the resource YAML/JSON
		obj, gvr, err := parseResource(input.Resource)
		if err != nil {
			return nil, CreateResourceOutput{}, fmt.Errorf("parsing resource: %w", err)
		}

		// Create the resource
		var created *unstructured.Unstructured
		ns := obj.GetNamespace()
		if ns != "" {
			created, err = dynClient.Resource(gvr).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
		} else {
			created, err = dynClient.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{})
		}
		if err != nil {
			return nil, CreateResourceOutput{}, fmt.Errorf("creating resource: %w", err)
		}

		return nil, CreateResourceOutput{
			Created: created.UnstructuredContent(),
			Message: fmt.Sprintf("Created %s/%s", created.GetKind(), created.GetName()),
		}, nil
	})
}

func registerUpdateResource(server *mcp.Server, scope Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "update_resource",
		Description: `Update (replace) a Kubernetes resource in a workspace.

Provide the full resource as YAML or JSON. The resource must include:
- apiVersion, kind, metadata.name
- metadata.resourceVersion (get current version with get_resource first)

This performs a full replacement of the resource spec.
For partial updates, use patch_resource instead.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
				"resource": map[string]any{
					"type":        "string",
					"description": "Full YAML or JSON representation of the resource (must include resourceVersion)",
				},
			},
			"required": []string{"workspace", "resource"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input UpdateResourceInput) (*mcp.CallToolResult, UpdateResourceOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, UpdateResourceOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, UpdateResourceOutput{}, fmt.Errorf("getting client: %w", err)
		}

		obj, gvr, err := parseResource(input.Resource)
		if err != nil {
			return nil, UpdateResourceOutput{}, fmt.Errorf("parsing resource: %w", err)
		}

		var updated *unstructured.Unstructured
		ns := obj.GetNamespace()
		if ns != "" {
			updated, err = dynClient.Resource(gvr).Namespace(ns).Update(ctx, obj, metav1.UpdateOptions{})
		} else {
			updated, err = dynClient.Resource(gvr).Update(ctx, obj, metav1.UpdateOptions{})
		}
		if err != nil {
			return nil, UpdateResourceOutput{}, fmt.Errorf("updating resource: %w", err)
		}

		return nil, UpdateResourceOutput{
			Updated: updated.UnstructuredContent(),
			Message: fmt.Sprintf("Updated %s/%s", updated.GetKind(), updated.GetName()),
		}, nil
	})
}

func registerPatchResource(server *mcp.Server, scope Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "patch_resource",
		Description: `Patch a Kubernetes resource with a JSON merge patch.

Use this for partial updates without needing the full resource.
The patch is a JSON object that will be merged with the existing resource.

Example patch to add a label:
  {"metadata": {"labels": {"env": "prod"}}}

Example patch to update a ConfigMap data field:
  {"data": {"newKey": "newValue"}}`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
				"apiVersion": map[string]any{
					"type":        "string",
					"description": "API version (e.g., 'v1', 'apps/v1')",
				},
				"kind": map[string]any{
					"type":        "string",
					"description": "Resource kind (e.g., 'ConfigMap', 'Deployment')",
				},
				"group": map[string]any{
					"type":        "string",
					"description": "API group (alternative to apiVersion+kind)",
				},
				"version": map[string]any{
					"type":        "string",
					"description": "API version (alternative to apiVersion)",
				},
				"resource": map[string]any{
					"type":        "string",
					"description": "Resource name plural (alternative to kind)",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Resource name",
				},
				"namespace": map[string]any{
					"type":        "string",
					"description": "Namespace (optional for cluster-scoped resources)",
				},
				"patch": map[string]any{
					"type":        "string",
					"description": "JSON merge patch content",
				},
			},
			"required": []string{"workspace", "name", "patch"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input PatchResourceInput) (*mcp.CallToolResult, PatchResourceOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, PatchResourceOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, PatchResourceOutput{}, fmt.Errorf("getting client: %w", err)
		}

		gvr, err := parseGVR(input.APIVersion, input.Kind, input.Group, input.Version, input.Resource)
		if err != nil {
			return nil, PatchResourceOutput{}, err
		}

		// Validate patch is valid JSON
		var patchData map[string]any
		if err := json.Unmarshal([]byte(input.Patch), &patchData); err != nil {
			return nil, PatchResourceOutput{}, fmt.Errorf("invalid patch JSON: %w", err)
		}

		var patched *unstructured.Unstructured
		if input.Namespace != "" {
			patched, err = dynClient.Resource(gvr).Namespace(input.Namespace).Patch(
				ctx, input.Name, types.MergePatchType, []byte(input.Patch), metav1.PatchOptions{})
		} else {
			patched, err = dynClient.Resource(gvr).Patch(
				ctx, input.Name, types.MergePatchType, []byte(input.Patch), metav1.PatchOptions{})
		}
		if err != nil {
			return nil, PatchResourceOutput{}, fmt.Errorf("patching resource: %w", err)
		}

		return nil, PatchResourceOutput{
			Patched: patched.UnstructuredContent(),
			Message: fmt.Sprintf("Patched %s/%s", patched.GetKind(), patched.GetName()),
		}, nil
	})
}

func registerDeleteResource(server *mcp.Server, scope Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "delete_resource",
		Description: `Delete a Kubernetes resource from a workspace.

Specify the resource by apiVersion+kind or group+version+resource, plus name.
Optionally set gracePeriodSeconds for controlled termination (0 = immediate).`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
				"apiVersion": map[string]any{
					"type":        "string",
					"description": "API version (e.g., 'v1', 'apps/v1')",
				},
				"kind": map[string]any{
					"type":        "string",
					"description": "Resource kind (e.g., 'Pod', 'ConfigMap')",
				},
				"group": map[string]any{
					"type":        "string",
					"description": "API group (alternative to apiVersion+kind)",
				},
				"version": map[string]any{
					"type":        "string",
					"description": "API version (alternative to apiVersion)",
				},
				"resource": map[string]any{
					"type":        "string",
					"description": "Resource name plural (alternative to kind)",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Resource name to delete",
				},
				"namespace": map[string]any{
					"type":        "string",
					"description": "Namespace (optional for cluster-scoped resources)",
				},
				"gracePeriodSeconds": map[string]any{
					"type":        "integer",
					"description": "Seconds before deletion (0 = immediate, omit for default)",
				},
			},
			"required": []string{"workspace", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input DeleteResourceInput) (*mcp.CallToolResult, DeleteResourceOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, DeleteResourceOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, DeleteResourceOutput{}, fmt.Errorf("getting client: %w", err)
		}

		gvr, err := parseGVR(input.APIVersion, input.Kind, input.Group, input.Version, input.Resource)
		if err != nil {
			return nil, DeleteResourceOutput{}, err
		}

		deleteOpts := metav1.DeleteOptions{}
		if input.GracePeriodSeconds != nil {
			deleteOpts.GracePeriodSeconds = input.GracePeriodSeconds
		}

		if input.Namespace != "" {
			err = dynClient.Resource(gvr).Namespace(input.Namespace).Delete(ctx, input.Name, deleteOpts)
		} else {
			err = dynClient.Resource(gvr).Delete(ctx, input.Name, deleteOpts)
		}
		if err != nil {
			return nil, DeleteResourceOutput{}, fmt.Errorf("deleting resource: %w", err)
		}

		return nil, DeleteResourceOutput{
			Message: fmt.Sprintf("Deleted %s %s", resourceLabel(input.Kind, gvr.Resource), input.Name),
		}, nil
	})
}

func registerScaleResource(server *mcp.Server, scope Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "scale_resource",
		Description: `Get or set the scale (replicas) of a Kubernetes resource.

Works with Deployments, StatefulSets, ReplicaSets, and other scalable resources.
If replicas is omitted, returns the current scale without changing it.
If replicas is provided, scales the resource to that number.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
				"apiVersion": map[string]any{
					"type":        "string",
					"description": "API version (e.g., 'apps/v1')",
				},
				"kind": map[string]any{
					"type":        "string",
					"description": "Resource kind (e.g., 'Deployment', 'StatefulSet')",
				},
				"group": map[string]any{
					"type":        "string",
					"description": "API group (alternative to apiVersion+kind)",
				},
				"version": map[string]any{
					"type":        "string",
					"description": "API version (alternative to apiVersion)",
				},
				"resource": map[string]any{
					"type":        "string",
					"description": "Resource name plural (alternative to kind)",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Resource name",
				},
				"namespace": map[string]any{
					"type":        "string",
					"description": "Namespace",
				},
				"replicas": map[string]any{
					"type":        "integer",
					"description": "Desired replica count (omit to just get current scale)",
				},
			},
			"required": []string{"workspace", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ScaleResourceInput) (*mcp.CallToolResult, ScaleResourceOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ScaleResourceOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ScaleResourceOutput{}, fmt.Errorf("getting client: %w", err)
		}

		gvr, err := parseGVR(input.APIVersion, input.Kind, input.Group, input.Version, input.Resource)
		if err != nil {
			return nil, ScaleResourceOutput{}, err
		}

		// Get current scale
		var scaleObj *unstructured.Unstructured
		scaleGVR := schema.GroupVersionResource{
			Group:    gvr.Group,
			Version:  gvr.Version,
			Resource: gvr.Resource,
		}

		if input.Namespace != "" {
			scaleObj, err = dynClient.Resource(scaleGVR).Namespace(input.Namespace).Get(ctx, input.Name, metav1.GetOptions{}, "scale")
		} else {
			scaleObj, err = dynClient.Resource(scaleGVR).Get(ctx, input.Name, metav1.GetOptions{}, "scale")
		}
		if err != nil {
			return nil, ScaleResourceOutput{}, fmt.Errorf("getting scale: %w", err)
		}

		currentReplicas := int32(0)
		if spec, ok := scaleObj.Object["spec"].(map[string]any); ok {
			if r, ok := spec["replicas"].(int64); ok {
				currentReplicas = int32(r)
			}
		}

		desiredReplicas := currentReplicas
		message := fmt.Sprintf("Current scale: %d replicas", currentReplicas)

		// If replicas specified, update the scale
		if input.Replicas != nil {
			desiredReplicas = *input.Replicas

			// Update the scale subresource
			if err := unstructured.SetNestedField(scaleObj.Object, int64(desiredReplicas), "spec", "replicas"); err != nil {
				return nil, ScaleResourceOutput{}, fmt.Errorf("setting replicas: %w", err)
			}

			if input.Namespace != "" {
				scaleObj, err = dynClient.Resource(scaleGVR).Namespace(input.Namespace).Update(ctx, scaleObj, metav1.UpdateOptions{}, "scale")
			} else {
				scaleObj, err = dynClient.Resource(scaleGVR).Update(ctx, scaleObj, metav1.UpdateOptions{}, "scale")
			}
			if err != nil {
				return nil, ScaleResourceOutput{}, fmt.Errorf("updating scale: %w", err)
			}

			message = fmt.Sprintf("Scaled from %d to %d replicas", currentReplicas, desiredReplicas)
		}

		return nil, ScaleResourceOutput{
			Name:            input.Name,
			Namespace:       input.Namespace,
			CurrentReplicas: currentReplicas,
			DesiredReplicas: desiredReplicas,
			Message:         message,
		}, nil
	})
}

// parseResource parses YAML or JSON into an unstructured object and extracts its GVR.
func parseResource(data string) (*unstructured.Unstructured, schema.GroupVersionResource, error) {
	// Try to parse as JSON first, then YAML
	var rawObj map[string]any

	data = strings.TrimSpace(data)
	if strings.HasPrefix(data, "{") {
		if err := json.Unmarshal([]byte(data), &rawObj); err != nil {
			return nil, schema.GroupVersionResource{}, fmt.Errorf("invalid JSON: %w", err)
		}
	} else {
		// Parse as YAML
		jsonData, err := yaml.YAMLToJSON([]byte(data))
		if err != nil {
			return nil, schema.GroupVersionResource{}, fmt.Errorf("invalid YAML: %w", err)
		}
		if err := json.Unmarshal(jsonData, &rawObj); err != nil {
			return nil, schema.GroupVersionResource{}, fmt.Errorf("converting YAML to object: %w", err)
		}
	}

	obj := &unstructured.Unstructured{Object: rawObj}

	// Extract GVK
	apiVersion := obj.GetAPIVersion()
	kind := obj.GetKind()

	if apiVersion == "" || kind == "" {
		return nil, schema.GroupVersionResource{}, fmt.Errorf("resource must have apiVersion and kind")
	}

	gvr, err := parseGVR(apiVersion, kind, "", "", "")
	if err != nil {
		return nil, schema.GroupVersionResource{}, err
	}

	return obj, gvr, nil
}
