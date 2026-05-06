// Command server runs the Access Virtual Workspace as a single
// HTTP service.
//
// Three run modes, picked from the flags:
//
//   - Multi-shard (-kubeconfig and -apiexport-endpointslice both set):
//     production mode. The kcp-native RBAC provider runs a
//     multicluster-runtime manager backed by the kcp apiexport
//     provider, watching ClusterRoleBindings and RoleBindings across
//     every workspace bound to the access VW's system APIExport.
//   - Single-shard (-kubeconfig only): development mode. Standard
//     client-go informers against one cluster.
//   - Demo (no -kubeconfig): seed the graph with a small static
//     dataset and serve. Useful for curl tinkering before kcp is
//     wired in.
//
// In all modes, the SCAR HTTP handler is the same — it reads from
// the graph the provider populates.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
	"github.com/cnvergence/kcp-access-vw/pkg/rbacprovider"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual/auth"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual/scar"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	kubeconfig := flag.String("kubeconfig", "", "Path to kubeconfig; when set, the kcp-native RBAC provider runs against this cluster instead of using demo data")
	endpointBase := flag.String("endpoint-base", "https://kcp.example.com/clusters/", "FrontProxy URL prefix for cluster endpoints")
	endpointSlice := flag.String("apiexport-endpointslice", "", "Name of the APIExportEndpointSlice for the access VW's system APIExport. When set together with -kubeconfig, the provider runs in multi-shard mode via multicluster-runtime; only workspaces with an APIBinding to that APIExport are indexed.")
	trustHeaders := flag.Bool("trust-headers", false, "Trust X-Remote-User/X-Remote-Group headers without additional authentication. Only safe when the server sits behind kcp's FrontProxy.")
	flag.Parse()

	g := graph.New()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Build the auth resolver chain. Order: bearer token → client cert → headers (if enabled).
	var resolvers []auth.Resolver

	if *kubeconfig != "" {
		config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			log.Fatalf("load kubeconfig: %v", err)
		}

		// TokenReview resolver — uses the kubeconfig's credentials to
		// validate bearer tokens from incoming requests against kcp.
		client, err := kubernetes.NewForConfig(config)
		if err != nil {
			log.Fatalf("create kubernetes client for token review: %v", err)
		}
		resolvers = append(resolvers, &auth.TokenReviewResolver{Client: client})

		provider := rbacprovider.New(*endpointBase)
		provider.RestConfig = config
		provider.APIExportEndpointSlice = *endpointSlice
		go func() {
			if err := provider.Start(ctx, g); err != nil {
				log.Fatalf("provider: %v", err)
			}
		}()
		mode := "single-shard"
		if *endpointSlice != "" {
			mode = "multi-shard (apiexport=" + *endpointSlice + ")"
		}
		log.Printf("rbacprovider running %s against kubeconfig=%s", mode, *kubeconfig)
	} else {
		populateDemoData(g)
		g.SetReady()
		log.Print("no kubeconfig; serving demo data")
	}

	if *trustHeaders {
		resolvers = append(resolvers, auth.HeaderResolver{})
		log.Print("WARNING: trusting X-Remote-User/X-Remote-Group headers — only safe behind FrontProxy")
	}

	// If no kubeconfig and no trust-headers, fall back to headers anyway
	// for demo mode backward compatibility.
	if len(resolvers) == 0 {
		resolvers = append(resolvers, auth.HeaderResolver{})
		log.Print("demo mode: using header-based auth (no kubeconfig)")
	}

	resolver := &auth.ChainResolver{Resolvers: resolvers}

	mux := http.NewServeMux()
	scar.Register(mux, g, resolver)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if g.Ready() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready"))
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("access-vw listening on %s", *addr)
		log.Printf("SCAR endpoint: %s", scar.Path)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// populateDemoData seeds the graph with a small, predictable set of
// access edges so the SCAR endpoint returns interesting results
// when -kubeconfig is not provided.
//
// Demo identities:
//
//	alice                  — direct access to ws-alice
//	in group "eng"         — ws-eng-1, ws-eng-2
//	in group "platform"    — ws-platform
//
// Try, e.g.:
//
//	curl -s -X POST \
//	     -H 'X-Remote-User: alice' \
//	     -H 'X-Remote-Group: eng' \
//	     -H 'X-Remote-Group: platform' \
//	     localhost:8080/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews | jq
func populateDemoData(g *graph.Graph) {
	const base = "https://kcp.example.com/clusters/"

	g.Grant(graph.User("alice"), "ws-alice", base+"ws-alice")

	g.Grant(graph.Group("eng"), "ws-eng-1", base+"ws-eng-1")
	g.Grant(graph.Group("eng"), "ws-eng-2", base+"ws-eng-2")

	g.Grant(graph.Group("platform"), "ws-platform", base+"ws-platform")
}
