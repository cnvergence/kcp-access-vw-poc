// Package accessprovider defines the AccessProvider interface — the
// writer-side seam between the access graph and the various sources
// of authorization data that may populate it.
//
// Providers are populators: they observe an authorization source
// (kcp-native CRBs/RBs, an external webhook authorizer, an OpenFGA
// store, etc.) and translate what they see into Grant/Revoke/Forget
// calls on the supplied graph. The SCAR handler does not interact
// with providers directly; it reads from the same graph the providers
// populate.
//
// The MVP ships a single provider — the kcp-native one watching
// ClusterRoleBindings and RoleBindings via cross-shard informers.
// Additional implementations (external webhook, FGA) follow the same
// contract: Start populates the graph until ctx is cancelled, and
// calls graph.SetReady() once the initial sync is complete.
package accessprovider

import (
	"context"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
)

// AccessProvider populates an access graph from some authorization source.
//
// Implementations must be safe for concurrent use; callers expect to
// be able to start a provider and concurrently issue queries against
// the graph it populates.
type AccessProvider interface {
	// Start populates g and keeps it in sync until ctx is cancelled.
	//
	// Implementations are responsible for calling g.SetReady() once
	// the initial sync has completed; until then, the graph reports
	// itself as not ready and SCAR consumers gate on that.
	//
	// Start blocks until ctx is cancelled or an unrecoverable error
	// occurs. Returning a non-nil error indicates the provider gave
	// up; the caller is expected to log it and either restart the
	// provider or exit.
	Start(ctx context.Context, g *graph.Graph) error
}
