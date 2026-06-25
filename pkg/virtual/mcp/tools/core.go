package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// kcp core.kcp.io and topology.kcp.io GVRs
var (
	logicalClusterGVR = schema.GroupVersionResource{
		Group:    "core.kcp.io",
		Version:  "v1alpha1",
		Resource: "logicalclusters",
	}

	shardGVR = schema.GroupVersionResource{
		Group:    "core.kcp.io",
		Version:  "v1alpha1",
		Resource: "shards",
	}

	partitionGVR = schema.GroupVersionResource{
		Group:    "topology.kcp.io",
		Version:  "v1alpha1",
		Resource: "partitions",
	}

	partitionSetGVR = schema.GroupVersionResource{
		Group:    "topology.kcp.io",
		Version:  "v1alpha1",
		Resource: "partitionsets",
	}
)

// LogicalClusterInfo represents a LogicalCluster in list output.
type LogicalClusterInfo struct {
	Name  string `json:"name"`
	Phase string `json:"phase,omitempty"`
	URL   string `json:"url,omitempty"`
}

// ListLogicalClustersInput is the input for list_logicalclusters.
type ListLogicalClustersInput struct {
	Workspace string `json:"workspace"`
}

// ListLogicalClustersOutput is the output for list_logicalclusters.
type ListLogicalClustersOutput struct {
	LogicalClusters []LogicalClusterInfo `json:"logicalClusters"`
	Count           int                  `json:"count"`
}

// ShardInfo represents a Shard in list output.
type ShardInfo struct {
	Name        string `json:"name"`
	BaseURL     string `json:"baseURL,omitempty"`
	ExternalURL string `json:"externalURL,omitempty"`
}

// ListShardsInput is the input for list_shards.
type ListShardsInput struct {
	Workspace string `json:"workspace"`
}

// ListShardsOutput is the output for list_shards.
type ListShardsOutput struct {
	Shards []ShardInfo `json:"shards"`
	Count  int         `json:"count"`
}

// PartitionInfo represents a Partition in list output.
type PartitionInfo struct {
	Name     string            `json:"name"`
	Selector map[string]string `json:"selector,omitempty"`
}

// ListPartitionsInput is the input for list_partitions.
type ListPartitionsInput struct {
	Workspace string `json:"workspace"`
}

// ListPartitionsOutput is the output for list_partitions.
type ListPartitionsOutput struct {
	Partitions []PartitionInfo `json:"partitions"`
	Count      int             `json:"count"`
}

// PartitionSetInfo represents a PartitionSet in list output.
type PartitionSetInfo struct {
	Name       string `json:"name"`
	Dimensions int    `json:"dimensions,omitempty"`
}

// ListPartitionSetsInput is the input for list_partitionsets.
type ListPartitionSetsInput struct {
	Workspace string `json:"workspace"`
}

// ListPartitionSetsOutput is the output for list_partitionsets.
type ListPartitionSetsOutput struct {
	PartitionSets []PartitionSetInfo `json:"partitionSets"`
	Count         int                `json:"count"`
}

func registerCoreTools(server *mcp.Server, scope Scope) {
	// list_logicalclusters
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_logicalclusters",
		Description: `List LogicalClusters visible from a kcp workspace.
LogicalClusters represent the internal cluster identity within kcp.
Each workspace has an associated LogicalCluster.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListLogicalClustersInput) (*mcp.CallToolResult, ListLogicalClustersOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListLogicalClustersOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListLogicalClustersOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(logicalClusterGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListLogicalClustersOutput{}, fmt.Errorf("listing LogicalClusters: %w", err)
		}

		items := extractListItems(list)
		clusters := make([]LogicalClusterInfo, 0, len(items))
		for _, item := range items {
			info := LogicalClusterInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			if status, ok := item["status"].(map[string]any); ok {
				if phase, ok := status["phase"].(string); ok {
					info.Phase = phase
				}
				if url, ok := status["URL"].(string); ok {
					info.URL = url
				}
			}
			clusters = append(clusters, info)
		}

		return nil, ListLogicalClustersOutput{
			LogicalClusters: clusters,
			Count:           len(clusters),
		}, nil
	})

	// list_shards
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_shards",
		Description: `List Shards visible from a kcp workspace.
Shards are the physical kcp server instances that host workspaces.
This is typically only visible from the root workspace.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListShardsInput) (*mcp.CallToolResult, ListShardsOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListShardsOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListShardsOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(shardGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListShardsOutput{}, fmt.Errorf("listing Shards: %w", err)
		}

		items := extractListItems(list)
		shards := make([]ShardInfo, 0, len(items))
		for _, item := range items {
			info := ShardInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			if spec, ok := item["spec"].(map[string]any); ok {
				if url, ok := spec["baseURL"].(string); ok {
					info.BaseURL = url
				}
				if url, ok := spec["externalURL"].(string); ok {
					info.ExternalURL = url
				}
			}
			shards = append(shards, info)
		}

		return nil, ListShardsOutput{
			Shards: shards,
			Count:  len(shards),
		}, nil
	})

	// list_partitions
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_partitions",
		Description: `List Partitions visible from a kcp workspace.
Partitions define how workspaces are distributed across shards for scalability.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListPartitionsInput) (*mcp.CallToolResult, ListPartitionsOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListPartitionsOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListPartitionsOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(partitionGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListPartitionsOutput{}, fmt.Errorf("listing Partitions: %w", err)
		}

		items := extractListItems(list)
		partitions := make([]PartitionInfo, 0, len(items))
		for _, item := range items {
			info := PartitionInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			if spec, ok := item["spec"].(map[string]any); ok {
				if sel, ok := spec["selector"].(map[string]any); ok {
					info.Selector = make(map[string]string)
					for k, v := range sel {
						if vs, ok := v.(string); ok {
							info.Selector[k] = vs
						}
					}
				}
			}
			partitions = append(partitions, info)
		}

		return nil, ListPartitionsOutput{
			Partitions: partitions,
			Count:      len(partitions),
		}, nil
	})

	// list_partitionsets
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_partitionsets",
		Description: `List PartitionSets visible from a kcp workspace.
PartitionSets group partitions together for management.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListPartitionSetsInput) (*mcp.CallToolResult, ListPartitionSetsOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListPartitionSetsOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListPartitionSetsOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(partitionSetGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListPartitionSetsOutput{}, fmt.Errorf("listing PartitionSets: %w", err)
		}

		items := extractListItems(list)
		sets := make([]PartitionSetInfo, 0, len(items))
		for _, item := range items {
			info := PartitionSetInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			if spec, ok := item["spec"].(map[string]any); ok {
				if dims, ok := spec["dimensions"].([]any); ok {
					info.Dimensions = len(dims)
				}
			}
			sets = append(sets, info)
		}

		return nil, ListPartitionSetsOutput{
			PartitionSets: sets,
			Count:         len(sets),
		}, nil
	})
}
