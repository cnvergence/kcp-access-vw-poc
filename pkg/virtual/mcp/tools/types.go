package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Scope provides workspace access context for tool handlers.
// It abstracts the WorkspaceScope to avoid circular imports.
type Scope interface {
	// Names returns the list of workspace IDs the caller has access to.
	Names() []string

	// HasAccess returns true if the workspace is in the caller's scope.
	HasAccess(workspace string) bool

	// ClientFor returns K8s clients for the given workspace.
	ClientFor(workspace string) (kubernetes.Interface, dynamic.Interface, error)

	// Clusters returns the raw cluster access info (endpoint + cluster name).
	Clusters() []ClusterInfo
}

// ClusterInfo holds workspace endpoint information.
type ClusterInfo struct {
	ClusterName string
	Endpoint    string
}

// ScopeError is returned when a tool targets a workspace outside the caller's scope.
// It provides both the requested workspace and available workspaces to help
// the model understand the access boundary.
type ScopeError struct {
	Requested string   `json:"requested"`
	Available []string `json:"available"`
}

func (e *ScopeError) Error() string {
	return "workspace not in scope: " + e.Requested
}

// NewScopeError creates a ScopeError for the given workspace and scope.
func NewScopeError(workspace string, scope Scope) *ScopeError {
	return &ScopeError{
		Requested: workspace,
		Available: scope.Names(),
	}
}

// ToolRegistrar is the interface for registering tools with an MCP server.
type ToolRegistrar interface {
	// The MCP SDK uses mcp.AddTool, so we pass the server directly
}

// RegisterAll registers all MCP tools with the given server.
func RegisterAll(server *mcp.Server, scope Scope) {
	// Core workspace tools
	registerListWorkspaces(server, scope)
	registerListResources(server, scope)
	registerGetResource(server, scope)

	// Write operations (create, update, patch, delete, scale)
	registerWriteOperations(server, scope)

	// API discovery tools (for exploring available K8s/kcp APIs)
	registerDiscoveryTools(server, scope)

	// Kubernetes core tools
	registerEventTools(server, scope)

	// kcp apis.kcp.io tools
	registerAPIExportTools(server, scope)
	registerAPIResourceSchemaTools(server, scope)
	registerAPIBindingTools(server, scope)

	// kcp tenancy.kcp.io tools
	registerTenancyTools(server, scope)
	registerWorkspaceStatusTools(server, scope)

	// kcp core.kcp.io and topology.kcp.io tools
	registerCoreTools(server, scope)

	// kcp workload/scheduling tools (replication)
	registerReplicationTools(server, scope)
}

// contextKey is the type for context keys in this package.
type contextKey string

// ScopeKey is the context key for storing the WorkspaceScope.
const ScopeKey contextKey = "mcp-workspace-scope"

// ScopeFromContext retrieves the Scope from context.
func ScopeFromContext(ctx context.Context) Scope {
	if v := ctx.Value(ScopeKey); v != nil {
		if s, ok := v.(Scope); ok {
			return s
		}
	}
	return nil
}
