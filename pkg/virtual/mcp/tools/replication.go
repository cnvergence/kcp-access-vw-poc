package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// Replication resources from workload.kcp.io
	syncTargetGVR = schema.GroupVersionResource{
		Group:    "workload.kcp.io",
		Version:  "v1alpha1",
		Resource: "synctargets",
	}

	placementGVR = schema.GroupVersionResource{
		Group:    "scheduling.kcp.io",
		Version:  "v1alpha1",
		Resource: "placements",
	}

	locationGVR = schema.GroupVersionResource{
		Group:    "scheduling.kcp.io",
		Version:  "v1alpha1",
		Resource: "locations",
	}
)

// SyncTargetInfo represents a SyncTarget in list output.
type SyncTargetInfo struct {
	Name          string   `json:"name"`
	State         string   `json:"state,omitempty"`
	VirtualWS     string   `json:"virtualWorkspace,omitempty"`
	SyncedObjects []string `json:"syncedObjects,omitempty"`
}

// PlacementInfo represents a Placement in list output.
type PlacementInfo struct {
	Name          string `json:"name"`
	LocationName  string `json:"locationName,omitempty"`
	Phase         string `json:"phase,omitempty"`
	SelectedCount int    `json:"selectedCount,omitempty"`
}

// LocationInfo represents a Location in list output.
type LocationInfo struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// ListSyncTargetsInput is the input for list_synctargets.
type ListSyncTargetsInput struct {
	Workspace string `json:"workspace"`
}

// ListSyncTargetsOutput is the output for list_synctargets.
type ListSyncTargetsOutput struct {
	SyncTargets []SyncTargetInfo `json:"syncTargets"`
	Count       int              `json:"count"`
}

// ListPlacementsInput is the input for list_placements.
type ListPlacementsInput struct {
	Workspace string `json:"workspace"`
}

// ListPlacementsOutput is the output for list_placements.
type ListPlacementsOutput struct {
	Placements []PlacementInfo `json:"placements"`
	Count      int             `json:"count"`
}

// ListLocationsInput is the input for list_locations.
type ListLocationsInput struct {
	Workspace string `json:"workspace"`
}

// ListLocationsOutput is the output for list_locations.
type ListLocationsOutput struct {
	Locations []LocationInfo `json:"locations"`
	Count     int            `json:"count"`
}

func registerReplicationTools(server *mcp.Server, scope Scope) {
	// list_synctargets
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_kcp_synctargets",
		Description: `List SyncTargets in a kcp workspace.
SyncTargets represent physical clusters that have been registered with kcp for workload syncing.
They define where workloads can be placed and what resources are available.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListSyncTargetsInput) (*mcp.CallToolResult, ListSyncTargetsOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListSyncTargetsOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListSyncTargetsOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(syncTargetGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListSyncTargetsOutput{}, fmt.Errorf("listing SyncTargets: %w", err)
		}

		items := extractListItems(list)
		syncTargets := make([]SyncTargetInfo, 0, len(items))
		for _, item := range items {
			info := SyncTargetInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			if status, ok := item["status"].(map[string]any); ok {
				if vws, ok := status["virtualWorkspaces"].([]any); ok && len(vws) > 0 {
					if vw, ok := vws[0].(map[string]any); ok {
						if url, ok := vw["url"].(string); ok {
							info.VirtualWS = url
						}
					}
				}
			}
			syncTargets = append(syncTargets, info)
		}

		return nil, ListSyncTargetsOutput{
			SyncTargets: syncTargets,
			Count:       len(syncTargets),
		}, nil
	})

	// list_placements
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_kcp_placements",
		Description: `List Placements in a kcp workspace.
Placements define how workloads are scheduled to SyncTargets based on location selectors.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListPlacementsInput) (*mcp.CallToolResult, ListPlacementsOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListPlacementsOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListPlacementsOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(placementGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListPlacementsOutput{}, fmt.Errorf("listing Placements: %w", err)
		}

		items := extractListItems(list)
		placements := make([]PlacementInfo, 0, len(items))
		for _, item := range items {
			info := PlacementInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			if spec, ok := item["spec"].(map[string]any); ok {
				if locSel, ok := spec["locationSelectors"].([]any); ok && len(locSel) > 0 {
					// Extract first location name if available
					if sel, ok := locSel[0].(map[string]any); ok {
						if name, ok := sel["name"].(string); ok {
							info.LocationName = name
						}
					}
				}
			}
			if status, ok := item["status"].(map[string]any); ok {
				if phase, ok := status["phase"].(string); ok {
					info.Phase = phase
				}
				if selected, ok := status["selectedBy"].([]any); ok {
					info.SelectedCount = len(selected)
				}
			}
			placements = append(placements, info)
		}

		return nil, ListPlacementsOutput{
			Placements: placements,
			Count:      len(placements),
		}, nil
	})

	// list_locations
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_kcp_locations",
		Description: `List Locations in a kcp workspace.
Locations are logical groupings of SyncTargets, used for placement decisions.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListLocationsInput) (*mcp.CallToolResult, ListLocationsOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListLocationsOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListLocationsOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(locationGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListLocationsOutput{}, fmt.Errorf("listing Locations: %w", err)
		}

		items := extractListItems(list)
		locations := make([]LocationInfo, 0, len(items))
		for _, item := range items {
			info := LocationInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
				if labels, ok := meta["labels"].(map[string]any); ok {
					info.Labels = make(map[string]string)
					for k, v := range labels {
						if vs, ok := v.(string); ok {
							info.Labels[k] = vs
						}
					}
				}
			}
			if spec, ok := item["spec"].(map[string]any); ok {
				if desc, ok := spec["description"].(string); ok {
					info.Description = desc
				}
			}
			locations = append(locations, info)
		}

		return nil, ListLocationsOutput{
			Locations: locations,
			Count:     len(locations),
		}, nil
	})
}
