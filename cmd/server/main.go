// Command server runs the Access Virtual Workspace as a single
// HTTP service.
//
// Two run modes, picked from the flags:
//
//   - Multi-shard (-kubeconfig and -apiexport-endpointslice both set):
//     production mode. The kcp-native RBAC provider runs a
//     multicluster-runtime manager backed by the kcp apiexport
//     provider, watching ClusterRoleBindings and RoleBindings across
//     every workspace bound to the access VW's system APIExport.
//   - Single-shard (-kubeconfig only): development mode. Standard
//     client-go informers against one cluster.
//
// In both modes, the SCAR HTTP handler is the same — it reads from
// the graph the provider populates.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
	"github.com/cnvergence/kcp-access-vw/pkg/rbacprovider"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual/auth"
	"github.com/cnvergence/kcp-access-vw/pkg/virtual/scar"
)

func main() {
	addr := flag.String("addr", ":9099", "HTTP listen address")

	// controller-runtime's client/config package registers a "kubeconfig"
	// flag in init(). Reuse it if present to avoid a redefined-flag panic.
	if f := flag.CommandLine.Lookup("kubeconfig"); f == nil {
		flag.String("kubeconfig", "", "Path to kubeconfig (required)")
	}
	kubeconfig := flag.CommandLine.Lookup("kubeconfig")

	endpointBase := flag.String("endpoint-base", "https://kcp.example.com/clusters/", "FrontProxy URL prefix for cluster endpoints")
	endpointSlice := flag.String("apiexport-endpointslice", "", "Name of the APIExportEndpointSlice for the access VW's system APIExport. When set together with -kubeconfig, the provider runs in multi-shard mode via multicluster-runtime; only workspaces with an APIBinding to that APIExport are indexed.")
	trustHeaders := flag.Bool("trust-headers", false, "Trust X-Remote-User/X-Remote-Group headers without additional authentication. Only safe when the server sits behind kcp's FrontProxy.")
	flag.Parse()

	// Wire controller-runtime's logger to klog so multicluster-runtime
	// and controller-runtime log through the standard pipeline.
	ctrllog.SetLogger(klog.NewKlogr())

	g := graph.New()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Build the auth resolver chain. Order: bearer token → client cert → headers (if enabled).
	var resolvers []auth.Resolver

	kubeconfigPath := kubeconfig.Value.String()
	if kubeconfigPath == "" {
		log.Fatal("-kubeconfig is required")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
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
	log.Printf("rbacprovider running %s against kubeconfig=%s", mode, kubeconfigPath)

	if *trustHeaders {
		resolvers = append(resolvers, auth.HeaderResolver{})
		log.Print("WARNING: trusting X-Remote-User/X-Remote-Group headers — only safe behind FrontProxy")
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
	mux.HandleFunc("/debug/graph", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		snap := g.Snapshot()
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(snap)
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
