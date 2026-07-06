package mcp

import (
	"fmt"
	"net/http"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual/mcp/tools"
)

// ClientFactory owns the shared HTTP transport and produces K8s clients
// that differ only in bearer token and target endpoint. Constructed once
// at server start to amortize TLS handshake cost across all requests.
type ClientFactory struct {
	base *rest.Config
	rt   http.RoundTripper
}

// NewClientFactory creates a ClientFactory from the access-vw's kubeconfig.
// The resulting factory reuses the TLS configuration and connection pool
// across all clients it produces.
func NewClientFactory(baseConfig *rest.Config) (*ClientFactory, error) {
	if baseConfig == nil {
		return nil, fmt.Errorf("base config cannot be nil")
	}

	base := rest.CopyConfig(baseConfig)

	rt, err := rest.TransportFor(base)
	if err != nil {
		return nil, fmt.Errorf("creating transport: %w", err)
	}

	return &ClientFactory{
		base: base,
		rt:   rt,
	}, nil
}

// NewClientFactoryFromHost creates a ClientFactory from a bare host URL.
// Useful for tests where no real cluster connection is needed.
func NewClientFactoryFromHost(host string) (*ClientFactory, error) {
	return NewClientFactory(&rest.Config{
		Host: host,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
	})
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

	cfg := &rest.Config{
		Host:      endpoint,
		Transport: &tokenRoundTripper{base: f.rt, token: token},
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

// tokenRoundTripper wraps an http.RoundTripper to inject a bearer token.
type tokenRoundTripper struct {
	base  http.RoundTripper
	token string
}

func (t *tokenRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid mutating the original
	req2 := req.Clone(req.Context())
	req2.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req2)
}

// WorkspaceScope holds per-request authorization context. Built fresh for
// each MCP request from the caller's identity and the access graph state.
type WorkspaceScope struct {
	User        string
	Groups      []string
	Token       string
	ClusterList []graph.AccessEndpointSlice
	factory     *ClientFactory
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

// ClientFor returns K8s clients for the given workspace. Returns an error
// if the workspace is not in scope or client creation fails.
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

	return s.factory.Clients(endpoint, s.Token)
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
