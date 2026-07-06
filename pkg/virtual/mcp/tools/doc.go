// Package tools implements MCP tool handlers for the kcp Access Virtual Workspace.
//
// Each tool is scoped to the caller's authorized workspaces via WorkspaceScope.
// kcp-specific tools (list_kcp_workspaces, etc.) hardcode their GVRs and only
// require a workspace parameter. Generic tools (list_resources, get_resource,
// create_resource, etc.) accept either explicit GVR or apiVersion+kind inputs
// and infer the resource name via simple pluralization.
//
// Tool naming: kcp-specific objects use <verb>_kcp_<noun> (e.g., list_kcp_workspaces),
// generic Kubernetes objects use <verb>_<noun> (e.g., list_resources, get_resource).
package tools
