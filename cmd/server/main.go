// Command server runs the Access Virtual Workspace: a virtual-workspace
// root apiserver (kcp virtual-workspace-framework) serving the SCAR API
// at /services/access and MCP at /services/mcp, designed to sit behind
// kcp's front-proxy.
//
// Identity handling:
//
//   - Behind the front-proxy (production): the proxy authenticates the
//     user and forwards identity via X-Remote-* headers over mTLS. Trust
//     is established with --requestheader-client-ca-file (and optionally
//     --requestheader-allowed-names).
//   - Direct access (development): bearer tokens are resolved via
//     TokenReview against kcp using --authentication-kubeconfig
//     (defaults to --kubeconfig).
//
// The RBAC provider runs in one of two modes, as before:
//
//   - Multi-shard (--kubeconfig and --apiexport-endpointslice both set):
//     production mode via multicluster-runtime; only workspaces bound to
//     the access VW's system APIExport are indexed.
//   - Single-shard (--kubeconfig only): development mode with standard
//     informers against one cluster.
package main

import (
	goflag "flag"
	"os"

	"github.com/spf13/pflag"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/klog/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cnvergence/kcp-access-vw/pkg/server"
)

func main() {
	// Wire controller-runtime's logger to klog so multicluster-runtime
	// and controller-runtime log through the standard pipeline.
	ctrllog.SetLogger(klog.NewKlogr())

	opts := server.NewOptions()

	fs := pflag.CommandLine
	klog.InitFlags(goflag.CommandLine)
	// controller-runtime's client/config package registers a
	// "kubeconfig" flag on the standard flag set in init(); adopt it
	// instead of redefining.
	fs.AddGoFlagSet(goflag.CommandLine)
	opts.AddFlags(fs)
	pflag.Parse()

	// If --kubeconfig came from controller-runtime's flag rather than
	// our own registration, copy the parsed value into the options.
	if opts.Kubeconfig == "" {
		if f := fs.Lookup("kubeconfig"); f != nil {
			opts.Kubeconfig = f.Value.String()
		}
	}

	ctx := genericapiserver.SetupSignalContext()

	if err := server.Run(ctx, opts); err != nil {
		klog.ErrorS(err, "access-vw failed")
		os.Exit(1)
	}
}
