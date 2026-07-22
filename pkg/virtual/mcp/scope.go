package mcp

import (
	"fmt"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual/mcp/tools"
)

// ClientFactory produces per-workspace Kubernetes clients that act on
// behalf of the authenticated caller via user impersonation.
//
// Behind kcp's front-proxy the caller's original bearer token never
// reaches the VW — the front-proxy consumes it and forwards identity
// as X-Remote-* headers. So per-workspace calls use the VW's own
// service identity (from its kubeconfig) with Impersonate-User /
// Impersonate-Group headers carrying the caller. kcp authorizes each
// request as the impersonated user, and audit logs record both
// identities.
//
// The VW's identity therefore needs RBAC permission to impersonate
// users, groups, and userextras in the target kcp.
type ClientFactory struct {
	base *rest.Config
}

// NewClientFactory creates a ClientFactory from the access-vw's own
// kubeconfig. Transport-level settings (TLS, timeouts) are inherited
// from that config; client-go's transport cache reuses connections
// across clients that share them.
func NewClientFactory(baseConfig *rest.Config) (*ClientFactory, error) {
	if baseConfig == nil {
		return nil, fmt.Errorf("base config cannot be nil")
	}
	return &ClientFactory{base: rest.CopyConfig(baseConfig)}, nil
}

// Clients returns typed and dynamic clients for the given workspace
// endpoint, impersonating the supplied user.
func (f *ClientFactory) Clients(endpoint string, u user.Info) (kubernetes.Interface, dynamic.Interface, error) {
	if endpoint == "" {
		return nil, nil, fmt.Errorf("endpoint cannot be empty")
	}
	if u == nil || u.GetName() == "" {
		return nil, nil, fmt.Errorf("user cannot be empty")
	}

	cfg := rest.CopyConfig(f.base)
	cfg.Host = endpoint
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: u.GetName(),
		Groups:   u.GetGroups(),
		Extra:    u.GetExtra(),
	}

	typedClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("creating typed client: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("creating dynamic client: %w", err)
	}

	return typedClient, dynClient, nil
}

// WorkspaceScope holds per-request authorization context. Built fresh
// for each MCP request from the caller's identity (resolved by the
// apiserver authentication filter) and the access graph state.
type WorkspaceScope struct {
	// User is the authenticated caller. Per-workspace clients
	// impersonate this identity.
	User user.Info

	// ClusterList is the set of workspaces the caller may access,
	// as answered by the access graph.
	ClusterList []graph.AccessEndpointSlice

	factory *ClientFactory
}

// Names returns the list of workspace IDs the caller has access to.
func (s *WorkspaceScope) Names() []string {
	names := make([]string, len(s.ClusterList))
	for i, c := range s.ClusterList {
		names[i] = c.ClusterName
	}
	return names
}

// HasAccess returns true if the workspace is in the caller's scope.
func (s *WorkspaceScope) HasAccess(workspace string) bool {
	for _, c := range s.ClusterList {
		if c.ClusterName == workspace {
			return true
		}
	}
	return false
}

// ClientFor returns K8s clients for the given workspace, impersonating
// the scope's user. Returns an error if the workspace is not in scope
// or client creation fails.
func (s *WorkspaceScope) ClientFor(workspace string) (kubernetes.Interface, dynamic.Interface, error) {
	var endpoint string
	for _, c := range s.ClusterList {
		if c.ClusterName == workspace {
			endpoint = c.Endpoint
			break
		}
	}

	if endpoint == "" {
		return nil, nil, fmt.Errorf("workspace %q not in scope (available: %v)", workspace, s.Names())
	}

	return s.factory.Clients(endpoint, s.User)
}

// Clusters returns cluster info for the tools package.
// This satisfies the tools.Scope interface.
func (s *WorkspaceScope) Clusters() []tools.ClusterInfo {
	result := make([]tools.ClusterInfo, len(s.ClusterList))
	for i, c := range s.ClusterList {
		result[i] = tools.ClusterInfo{
			ClusterName: c.ClusterName,
			Endpoint:    c.Endpoint,
		}
	}
	return result
}
