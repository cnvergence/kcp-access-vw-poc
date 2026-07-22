package mcp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/virtual-workspace-framework/framework"
	frameworkhandler "github.com/kcp-dev/virtual-workspace-framework/pkg/handler"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual"
)

// VirtualWorkspaceName is the name the MCP VW is registered under.
const VirtualWorkspaceName = "mcp"

// RootPath is the URL prefix the front-proxy routes to this VW. MCP
// clients connect to it directly (streamable HTTP).
const RootPath = "/services/" + VirtualWorkspaceName

// NewVirtualWorkspace builds the MCP VW: a raw HTTP handler registered
// with the virtual-workspace root apiserver. MCP is not a Kubernetes
// API, so it does not go through the fixed-group-version apiserver
// machinery — but it still sits behind the root apiserver's filter
// chain, which authenticates the caller and enforces the VW authorizer
// before the handler runs.
func NewVirtualWorkspace(g *graph.Graph, factory *ClientFactory) rootapiserver.NamedVirtualWorkspace {
	vw := &frameworkhandler.VirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, ctx context.Context) (bool, string, context.Context) {
			if urlPath != RootPath && !strings.HasPrefix(urlPath, RootPath+"/") {
				return false, "", ctx
			}
			return true, RootPath, ctx
		}),
		Authorizer: virtual.AuthenticatedOnlyAuthorizer(),
		ReadyChecker: framework.ReadyFunc(func() error {
			if !g.Ready() {
				return errors.New("access graph has not completed its initial sync")
			}
			return nil
		}),
		HandlerFactory: func(_ genericapiserver.CompletedConfig) (http.Handler, error) {
			return NewHandler(g, factory), nil
		},
	}

	return rootapiserver.NamedVirtualWorkspace{
		Name:             VirtualWorkspaceName,
		VirtualWorkspace: vw,
	}
}
