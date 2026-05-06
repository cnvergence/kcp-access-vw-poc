// Command server runs the Access Virtual Workspace as a single
// HTTP service.
//
// Two run modes:
//
//   - With -kubeconfig=PATH: connect to the indicated kcp shard,
//     start the kcp-native RBAC provider (informers on CRBs and
//     RBs), and serve SCAR queries against the graph the provider
//     populates.
//   - Without -kubeconfig: seed the graph with demo data and serve
//     against that. Useful for tinkering with curl, demos, or
//     exercising the SCAR handler before a real kcp is wired in.
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

	"k8s.io/client-go/tools/clientcmd"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
	"github.com/cnvergence/kcp-access-vw/pkg/rbacprovider"
	"github.com/cnvergence/kcp-access-vw/pkg/scar"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	kubeconfig := flag.String("kubeconfig", "", "Path to kubeconfig; when set, the kcp-native RBAC provider runs against this cluster instead of using demo data")
	endpointBase := flag.String("endpoint-base", "https://kcp.example.com/clusters/", "FrontProxy URL prefix for cluster endpoints")
	flag.Parse()

	g := graph.New()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *kubeconfig != "" {
		config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			log.Fatalf("load kubeconfig: %v", err)
		}
		provider := rbacprovider.New(*endpointBase)
		provider.RestConfig = config
		go func() {
			if err := provider.Start(ctx, g); err != nil {
				log.Fatalf("provider: %v", err)
			}
		}()
		log.Printf("rbacprovider running against kubeconfig=%s", *kubeconfig)
	} else {
		populateDemoData(g)
		g.SetReady()
		log.Print("no kubeconfig; serving demo data")
	}

	mux := http.NewServeMux()
	mux.Handle(scar.Path, scar.Handler(g))
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
