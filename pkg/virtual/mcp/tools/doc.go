// Package tools implements MCP tool handlers for the kcp Access Virtual Workspace.
//
// Each tool is scoped to the caller's authorized workspaces via WorkspaceScope.
// Tools use explicit GVR (group, version, resource) inputs rather than inferring
// from kind names, avoiding the plural-form ambiguity common in Kubernetes.
//
// Tool naming: kcp-specific objects use <verb>_kcp_<noun> (e.g., list_kcp_workspaces),
// generic Kubernetes objects use <verb>_<noun> (e.g., list_resources, get_resource).
//
// Available tools:
//   - list_kcp_workspaces: List accessible workspaces
//   - list_resources: List resources of a given type in a workspace
//   - get_resource: Get a specific resource by name
//
// kcp-specific API tools:
//   - list_kcp_apiexports: List APIExports in a workspace
//   - list_kcp_apiresourceschemas: List APIResourceSchemas
//   - list_kcp_initializingworkspaces / list_kcp_terminatingworkspaces: List workspace status
package tools
