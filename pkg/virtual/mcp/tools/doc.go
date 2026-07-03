// Package tools implements MCP tool handlers for the kcp Access Virtual Workspace.
//
// Each tool is scoped to the caller's authorized workspaces via WorkspaceScope.
// kcp-specific tools use explicit GVR (group, version, resource) inputs.
// Generic tools (list_resources, get_resource, create_resource, etc.) also accept
// apiVersion+kind and infer the resource name via simple pluralization.
//
// Tool naming: kcp-specific objects use <verb>_kcp_<noun> (e.g., list_kcp_workspaces),
// generic Kubernetes objects use <verb>_<noun> (e.g., list_resources, get_resource).
package tools
