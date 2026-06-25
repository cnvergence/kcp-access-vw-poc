package tools

import (
	"context"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EventInfo represents a Kubernetes event in list output.
type EventInfo struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace,omitempty"`
	Type           string `json:"type"`           // Normal or Warning
	Reason         string `json:"reason"`         // e.g., Created, Failed, Scheduled
	Message        string `json:"message"`        // Human-readable description
	InvolvedObject string `json:"involvedObject"` // e.g., Pod/my-pod
	Count          int32  `json:"count,omitempty"`
	LastTimestamp  string `json:"lastTimestamp,omitempty"`
	Source         string `json:"source,omitempty"`
}

// ListEventsInput is the input for list_events.
type ListEventsInput struct {
	Workspace     string `json:"workspace"`
	Namespace     string `json:"namespace,omitempty"`
	FieldSelector string `json:"fieldSelector,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

// ListEventsOutput is the output for list_events.
type ListEventsOutput struct {
	Events []EventInfo `json:"events"`
	Count  int         `json:"count"`
}

func registerEventTools(server *mcp.Server, scope Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_events",
		Description: `List Kubernetes events in a workspace.
Events provide information about what's happening - resource scheduling, errors, warnings, etc.
Very useful for debugging and understanding cluster state.
Events are sorted by timestamp (most recent first).

Use fieldSelector for server-side filtering. Supported fields:
- type=Warning or type=Normal
- involvedObject.kind=Pod
- involvedObject.name=my-pod
- involvedObject.namespace=default
- reason=Created

Example: "type=Warning,involvedObject.kind=Pod"`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_workspaces)",
				},
				"namespace": map[string]any{
					"type":        "string",
					"description": "Namespace (optional, omit for all namespaces)",
				},
				"fieldSelector": map[string]any{
					"type":        "string",
					"description": "Kubernetes field selector for server-side filtering (e.g., 'type=Warning', 'involvedObject.name=my-pod')",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum events to return (default 50)",
				},
			},
			"required": []string{"workspace"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListEventsInput) (*mcp.CallToolResult, ListEventsOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListEventsOutput{}, NewScopeError(input.Workspace, scope)
		}

		typedClient, _, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListEventsOutput{}, fmt.Errorf("getting client: %w", err)
		}

		listOpts := metav1.ListOptions{}
		if input.FieldSelector != "" {
			listOpts.FieldSelector = input.FieldSelector
		}

		var eventList *corev1.EventList
		if input.Namespace != "" {
			eventList, err = typedClient.CoreV1().Events(input.Namespace).List(ctx, listOpts)
		} else {
			eventList, err = typedClient.CoreV1().Events("").List(ctx, listOpts)
		}
		if err != nil {
			return nil, ListEventsOutput{}, fmt.Errorf("listing events: %w", err)
		}

		events := make([]EventInfo, 0, len(eventList.Items))
		for _, ev := range eventList.Items {
			// Determine best timestamp (same logic as kubernetes-mcp-server)
			var timestamp string
			if !ev.EventTime.IsZero() {
				timestamp = ev.EventTime.Format("2006-01-02T15:04:05Z")
			} else if ev.Series != nil && !ev.Series.LastObservedTime.IsZero() {
				timestamp = ev.Series.LastObservedTime.Format("2006-01-02T15:04:05Z")
			} else if ev.Count > 1 && !ev.LastTimestamp.IsZero() {
				timestamp = ev.LastTimestamp.Format("2006-01-02T15:04:05Z")
			} else if !ev.FirstTimestamp.IsZero() {
				timestamp = ev.FirstTimestamp.Format("2006-01-02T15:04:05Z")
			}

			info := EventInfo{
				Name:           ev.Name,
				Namespace:      ev.Namespace,
				Type:           ev.Type,
				Reason:         ev.Reason,
				Message:        ev.Message,
				InvolvedObject: fmt.Sprintf("%s/%s", ev.InvolvedObject.Kind, ev.InvolvedObject.Name),
				Count:          ev.Count,
				LastTimestamp:  timestamp,
				Source:         ev.Source.Component,
			}
			events = append(events, info)
		}

		// Sort by timestamp (most recent first)
		sort.Slice(events, func(i, j int) bool {
			return events[i].LastTimestamp > events[j].LastTimestamp
		})

		limit := input.Limit
		if limit <= 0 {
			limit = 50
		}
		if len(events) > limit {
			events = events[:limit]
		}

		return nil, ListEventsOutput{
			Events: events,
			Count:  len(events),
		}, nil
	})
}
