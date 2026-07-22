package mcp

import "k8s.io/client-go/rest"

// NewClientFactoryFromHost creates a ClientFactory from a bare host URL
// with TLS verification disabled. Test-only: no real cluster connection
// is made, so the insecure transport never carries production traffic.
func NewClientFactoryFromHost(host string) (*ClientFactory, error) {
	return NewClientFactory(&rest.Config{
		Host: host,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
	})
}
