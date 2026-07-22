// Package virtual provides shared building blocks for the two virtual
// workspaces served by this binary — the Access VW (SCAR) and the MCP
// VW — both built on the kcp virtual-workspace-framework and served
// behind kcp's front-proxy.
//
// # Architecture
//
// The binary runs a virtual-workspace root apiserver (k8s.io/apiserver
// based, via github.com/kcp-dev/virtual-workspace-framework). The root
// handler chain authenticates every request (front-proxy requestheader
// client certificates, bearer-token TokenReview against kcp, or client
// certs — standard delegated authentication), resolves the URL path to
// one of the registered virtual workspaces, strips the VW prefix, and
// delegates:
//
//   - /services/access → fixed-group-version apiserver serving
//     apis/access.kcp.io/v1alpha1/selfclusteraccessreviews
//   - /services/mcp    → raw HTTP handler serving MCP over
//     streamable HTTP
//
// Authorization is per-VW: both workspaces allow any authenticated,
// non-anonymous user, because SCAR is a self-review and the MCP tools
// scope themselves to the caller's authorized workspaces via the
// access graph (and kcp re-authorizes every per-workspace call).
//
// Only the controller side (pkg/rbacprovider) uses multicluster-runtime,
// to drive the graph from RBAC events across shards. The serving side is
// pure apiserver machinery.
package virtual

import (
	"context"

	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/virtual-workspace-framework/framework"
)

// AuthenticatedOnlyAuthorizer allows any authenticated, non-anonymous
// user and denies everyone else. Both VWs use it: SCAR is a
// self-review (the caller asks about themselves), and MCP tool calls
// are scoped by the access graph and re-authorized by kcp on every
// per-workspace request.
func AuthenticatedOnlyAuthorizer() authorizer.Authorizer {
	return authorizer.AuthorizerFunc(func(_ context.Context, attrs authorizer.Attributes) (authorizer.Decision, string, error) {
		u := attrs.GetUser()
		if u == nil || u.GetName() == "" || u.GetName() == user.Anonymous {
			return authorizer.DecisionDeny, "authentication required", nil
		}
		return authorizer.DecisionAllow, "available to any authenticated user", nil
	})
}

// CoreVirtualWorkspace is framework.VirtualWorkspace minus the
// admission interfaces — the part that concrete VW implementations
// like fixedgvs actually provide.
type CoreVirtualWorkspace interface {
	authorizer.Authorizer
	framework.RootPathResolver
	framework.ReadyChecker
	Register(name string, rootAPIServerConfig genericapiserver.CompletedConfig, delegateAPIServer genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error)
}

// WithoutAdmission adds no-op Admit/Validate to a CoreVirtualWorkspace
// so it satisfies framework.VirtualWorkspace. Neither VW here uses
// admission: SCAR persists nothing, and the MCP VW is a raw handler.
//
// TODO: if SCAR ever grows persisted (writable) state, replace this
// with real admission — at minimum validation of the incoming object.
type WithoutAdmission struct {
	CoreVirtualWorkspace
}

var _ framework.VirtualWorkspace = &WithoutAdmission{}

// Admit is a no-op.
func (w *WithoutAdmission) Admit(_ context.Context, _ admission.Attributes, _ admission.ObjectInterfaces) error {
	return nil
}

// Validate is a no-op.
func (w *WithoutAdmission) Validate(_ context.Context, _ admission.Attributes, _ admission.ObjectInterfaces) error {
	return nil
}
