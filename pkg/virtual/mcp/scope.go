package mcp

import (
	"fmt"
	"net/http"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
)

// ClientFactory owns the shared HTTP transport and produces K8s clients
// that differ only in bearer token and target endpoint. Constructed once
// at server start to amortize TLS handshake cost across all requests.
type ClientFactory struct {
	base *rest.Config      // CAData, TLS settings from access-vw kubeconfig
	rt   http.RoundTripper // shared transport, reused across all clients
}

// NewClientFactory creates a ClientFactory from the access-vw's kubeconfig.
// The resulting factory reuses the TLS configuration and connection pool
// across all clients it produces.
func NewClientFactory(baseConfig *rest.Config) (*ClientFactory, error) {
	if baseConfig == nil {
		return nil, fmt.Errorf("base config cannot be nil")
	}

	// Clone the config to avoid mutating the original
	base := rest.CopyConfig(baseConfig)

	// Create a shared transport from the base config
	rt, err := rest.TransportFor(base)
	if err != nil {
		return nil, fmt.Errorf("creating transport: %w", err)
	}

	return &ClientFactory{
		base: base,
		rt:   rt,
	}, nil
}

// Clients returns typed and dynamic clients for the given workspace endpoint,
// using the caller's bearer token for authentication.
func (f *ClientFactory) Clients(endpoint, token string) (kubernetes.Interface, dynamic.Interface, error) {
	if endpoint == "" {
		return nil, nil, fmt.Errorf("endpoint cannot be empty")
	}
	if token == "" {
		return nil, nil, fmt.Errorf("token cannot be empty")
	}

	// Build a config for this specific endpoint + token
	cfg := &rest.Config{
		Host:        endpoint,
		BearerToken: token,
		// Reuse TLS config and transport from the base
		TLSClientConfig: f.base.TLSClientConfig,
		Transport:       f.rt,
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

// WorkspaceScope holds per-request authorization context. Built fresh for
// each MCP request from the caller's identity and the access graph state.
type WorkspaceScope struct {
	User     string
	Groups   []string
	Token    string                      // caller's bearer token
	Clusters []graph.AccessEndpointSlice // already filtered by graph.ClustersFor
	factory  *ClientFactory              // shared, not owned
}

// Names returns the list of workspace IDs the caller has access to.
func (s *WorkspaceScope) Names() []string {
	names := make([]string, len(s.Clusters))
	for i, c := range s.Clusters {
		names[i] = c.ClusterName
	}
	return names
}

// HasAccess returns true if the workspace is in the caller's scope.
func (s *WorkspaceScope) HasAccess(workspace string) bool {
	for _, c := range s.Clusters {
		if c.ClusterName == workspace {
			return true
		}
	}
	return false
}

// ClientFor returns K8s clients for the given workspace. Returns an error
// if the workspace is not in scope or client creation fails.
func (s *WorkspaceScope) ClientFor(workspace string) (kubernetes.Interface, dynamic.Interface, error) {
	// Find the endpoint for this workspace
	var endpoint string
	for _, c := range s.Clusters {
		if c.ClusterName == workspace {
			endpoint = c.Endpoint
			break
		}
	}

	if endpoint == "" {
		return nil, nil, fmt.Errorf("workspace %q not in scope (available: %v)", workspace, s.Names())
	}

	return s.factory.Clients(endpoint, s.Token)
}
