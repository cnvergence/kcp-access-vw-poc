package tools

import (
	"context"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// APIResourceInfo represents an API resource available in a workspace.
type APIResourceInfo struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Group        string   `json:"group"`
	Version      string   `json:"version"`
	Namespaced   bool     `json:"namespaced"`
	Verbs        []string `json:"verbs"`
	ShortNames   []string `json:"shortNames,omitempty"`
	SingularName string   `json:"singularName,omitempty"`
}

// ListAPIResourcesInput is the input for list_api_resources.
type ListAPIResourcesInput struct {
	Workspace string `json:"workspace"`
	Group     string `json:"group,omitempty"` // optional filter by group
}

// ListAPIResourcesOutput is the output for list_api_resources.
type ListAPIResourcesOutput struct {
	Resources []APIResourceInfo `json:"resources"`
	Count     int               `json:"count"`
}

func registerDiscoveryTools(server *mcp.Server, scope Scope) {
	// list_api_resources - discover available APIs
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_api_resources",
		Description: `Discover available API resources in a kcp workspace.
Similar to 'kubectl api-resources', this lists all resource types available in the workspace.
Use this to find the correct group, version, and resource name for list_resources/get_resource.

Common kcp API groups:
- apis.kcp.io: APIExports, APIBindings, APIResourceSchemas
- tenancy.kcp.io: Workspaces, WorkspaceTypes
- core.kcp.io: LogicalClusters, Shards
- topology.kcp.io: Partitions, PartitionSets

Common Kubernetes API groups:
- "" (core): pods, services, configmaps, secrets, namespaces
- apps: deployments, statefulsets, daemonsets, replicasets
- batch: jobs, cronjobs
- rbac.authorization.k8s.io: roles, rolebindings, clusterroles, clusterrolebindings`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_workspaces)",
				},
				"group": map[string]any{
					"type":        "string",
					"description": "Optional: filter by API group (e.g., 'apis.kcp.io', 'apps', '')",
				},
			},
			"required": []string{"workspace"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListAPIResourcesInput) (*mcp.CallToolResult, ListAPIResourcesOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListAPIResourcesOutput{}, NewScopeError(input.Workspace, scope)
		}

		typedClient, _, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListAPIResourcesOutput{}, fmt.Errorf("getting client: %w", err)
		}

		// Get server groups and resources
		_, resourceLists, err := typedClient.Discovery().ServerGroupsAndResources()
		if err != nil {
			// Discovery can return partial results with errors for some groups
			// We'll proceed with what we got
			if resourceLists == nil {
				return nil, ListAPIResourcesOutput{}, fmt.Errorf("discovering API resources: %w", err)
			}
		}

		var resources []APIResourceInfo
		for _, resList := range resourceLists {
			// Parse group/version from GroupVersion string
			gv, err := parseGroupVersion(resList.GroupVersion)
			if err != nil {
				continue
			}

			// Filter by group if specified
			if input.Group != "" && gv.Group != input.Group {
				continue
			}

			for _, res := range resList.APIResources {
				// Skip subresources (they contain '/')
				if containsSlash(res.Name) {
					continue
				}

				info := APIResourceInfo{
					Name:         res.Name,
					Kind:         res.Kind,
					Group:        gv.Group,
					Version:      gv.Version,
					Namespaced:   res.Namespaced,
					Verbs:        res.Verbs,
					ShortNames:   res.ShortNames,
					SingularName: res.SingularName,
				}
				resources = append(resources, info)
			}
		}

		// Sort by group, then name for consistent output
		sort.Slice(resources, func(i, j int) bool {
			if resources[i].Group != resources[j].Group {
				return resources[i].Group < resources[j].Group
			}
			return resources[i].Name < resources[j].Name
		})

		return nil, ListAPIResourcesOutput{
			Resources: resources,
			Count:     len(resources),
		}, nil
	})

	// list_api_groups - list available API groups
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_api_groups",
		Description: `List available API groups in a kcp workspace.
Use this to discover what API groups are available before drilling down with list_api_resources.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input struct {
		Workspace string `json:"workspace"`
	}) (*mcp.CallToolResult, struct {
		Groups []string `json:"groups"`
		Count  int      `json:"count"`
	}, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, struct {
				Groups []string `json:"groups"`
				Count  int      `json:"count"`
			}{}, NewScopeError(input.Workspace, scope)
		}

		typedClient, _, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, struct {
				Groups []string `json:"groups"`
				Count  int      `json:"count"`
			}{}, fmt.Errorf("getting client: %w", err)
		}

		serverGroups, err := typedClient.Discovery().ServerGroups()
		if err != nil {
			return nil, struct {
				Groups []string `json:"groups"`
				Count  int      `json:"count"`
			}{}, fmt.Errorf("discovering API groups: %w", err)
		}

		groups := make([]string, 0, len(serverGroups.Groups))
		for _, g := range serverGroups.Groups {
			groups = append(groups, g.Name)
		}
		sort.Strings(groups)

		return nil, struct {
			Groups []string `json:"groups"`
			Count  int      `json:"count"`
		}{
			Groups: groups,
			Count:  len(groups),
		}, nil
	})

	// list_namespaces - convenience tool for listing namespaces
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_namespaces",
		Description: `List namespaces in a kcp workspace.
Convenience wrapper around list_resources for the common case of listing namespaces.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input struct {
		Workspace string `json:"workspace"`
	}) (*mcp.CallToolResult, struct {
		Namespaces []string `json:"namespaces"`
		Count      int      `json:"count"`
	}, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, struct {
				Namespaces []string `json:"namespaces"`
				Count      int      `json:"count"`
			}{}, NewScopeError(input.Workspace, scope)
		}

		typedClient, _, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, struct {
				Namespaces []string `json:"namespaces"`
				Count      int      `json:"count"`
			}{}, fmt.Errorf("getting client: %w", err)
		}

		nsList, err := typedClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, struct {
				Namespaces []string `json:"namespaces"`
				Count      int      `json:"count"`
			}{}, fmt.Errorf("listing namespaces: %w", err)
		}

		namespaces := make([]string, 0, len(nsList.Items))
		for _, ns := range nsList.Items {
			namespaces = append(namespaces, ns.Name)
		}

		return nil, struct {
			Namespaces []string `json:"namespaces"`
			Count      int      `json:"count"`
		}{
			Namespaces: namespaces,
			Count:      len(namespaces),
		}, nil
	})
}

// groupVersion holds parsed group and version.
type groupVersion struct {
	Group   string
	Version string
}

// parseGroupVersion parses a "group/version" string.
func parseGroupVersion(gv string) (groupVersion, error) {
	// Core API group is just "v1"
	if gv == "v1" {
		return groupVersion{Group: "", Version: "v1"}, nil
	}

	// Find the last slash
	for i := len(gv) - 1; i >= 0; i-- {
		if gv[i] == '/' {
			return groupVersion{
				Group:   gv[:i],
				Version: gv[i+1:],
			}, nil
		}
	}

	// No slash, treat as version only (shouldn't happen except for v1)
	return groupVersion{Group: "", Version: gv}, nil
}

// containsSlash checks if a string contains a slash.
func containsSlash(s string) bool {
	for _, c := range s {
		if c == '/' {
			return true
		}
	}
	return false
}
