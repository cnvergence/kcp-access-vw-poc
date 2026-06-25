// Package tools implements MCP tool handlers for the kcp Access Virtual Workspace.
//
// Each tool is scoped to the caller's authorized workspaces via WorkspaceScope.
// Tools use explicit GVR (group, version, resource) inputs rather than inferring
// from kind names, avoiding the plural-form ambiguity common in Kubernetes.
//
// Tool naming follows the pattern: <verb>_<noun> (e.g., list_workspaces, get_resource).
//
// Available tools:
//   - list_workspaces: List accessible workspaces
//   - list_resources: List resources of a given type in a workspace
//   - get_resource: Get a specific resource by name
//
// kcp-specific API tools:
//   - list_apiexports: List APIExports in a workspace
//   - list_apiresourceschemas: List APIResourceSchemas
//   - list_workspaces_status: List workspace status (initializing/terminating)
package tools
