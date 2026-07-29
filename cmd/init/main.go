/*
Copyright 2026 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Command access-vw-init installs the kcp-side objects the access virtual
// workspace needs — the APIResourceSchema, the APIExport, and the
// APIExportEndpointSlice the RBAC provider follows — and verifies they came
// up.
//
//	access-vw-init --kubeconfig=admin.kubeconfig --create-workspaces \
//	  --workspace=root:access:magic
package main

import (
	"context"
	goflag "flag"
	"os"
	"time"

	"github.com/spf13/pflag"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/cnvergence/kcp-access-vw/pkg/bootstrap"
)

func main() {
	var (
		kubeconfig       string
		workspacePath    string
		workspaceType    string
		createWorkspaces bool
		verifyBinding    string
		timeout          time.Duration
	)

	klog.InitFlags(goflag.CommandLine)
	pflag.CommandLine.AddGoFlagSet(goflag.CommandLine)

	pflag.StringVar(&kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"),
		"Path to the kubeconfig for the target kcp. Defaults to $KUBECONFIG.")
	pflag.StringVar(&workspacePath, "workspace", bootstrap.DefaultWorkspacePath,
		"Absolute path of the workspace to install into, e.g. root:access:magic. "+
			"Only meaningful with --create-workspaces; without it the kubeconfig's "+
			"current context decides the target workspace.")
	pflag.StringVar(&workspaceType, "workspace-type", bootstrap.DefaultWorkspaceType,
		"WorkspaceType for workspaces created by --create-workspaces.")
	pflag.BoolVar(&createWorkspaces, "create-workspaces", false,
		"Create any missing workspace along --workspace before installing. Requires "+
			"rights to create workspaces from root down; omit it when you only hold "+
			"cluster-admin in the target workspace.")
	pflag.StringVar(&verifyBinding, "verify-apibinding", "",
		"After installing, check that this APIBinding in the target workspace has all "+
			"of its permission claims accepted. Useful when the workspace being "+
			"bootstrapped is also a consumer.")
	pflag.DurationVar(&timeout, "timeout", 3*time.Minute,
		"Overall time budget for installing and verifying.")
	pflag.Parse()

	ctx := context.Background()
	logger := klog.FromContext(ctx)

	if kubeconfig == "" {
		logger.Error(nil, "--kubeconfig is required, or set KUBECONFIG")
		os.Exit(1)
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		logger.Error(err, "loading kubeconfig", "path", kubeconfig)
		os.Exit(1)
	}

	target := cfg
	reportedPath := "(kubeconfig current context)"

	if createWorkspaces {
		logger.Info("creating workspace path", "path", workspacePath, "type", workspaceType)
		target, err = bootstrap.CreateWorkspacePath(ctx, cfg, workspacePath, workspaceType)
		if err != nil {
			logger.Error(err, "creating workspace path", "path", workspacePath)
			os.Exit(1)
		}
		reportedPath = workspacePath
	}

	result, err := bootstrap.Bootstrap(ctx, target, bootstrap.Options{
		WorkspacePath: reportedPath,
		Timeout:       timeout,
	})
	if err != nil {
		logger.Error(err, "bootstrap failed")
		os.Exit(1)
	}

	if verifyBinding != "" {
		if err := bootstrap.VerifyAPIBinding(ctx, target, verifyBinding); err != nil {
			logger.Error(err, "APIBinding verification failed", "name", verifyBinding)
			os.Exit(1)
		}
		logger.Info("APIBinding permission claims accepted", "name", verifyBinding)
	}

	report(logger, target, result)
}

func report(logger klog.Logger, cfg *rest.Config, result *bootstrap.Result) {
	logger.Info("bootstrap complete",
		"workspace", result.WorkspacePath,
		"apiExportEndpointSlice", result.APIExportEndpointSlice,
		"virtualWorkspaceURLs", result.VirtualWorkspaceURLs,
	)
	logger.Info("start the server with",
		"--apiexport-endpointslice", result.APIExportEndpointSlice,
		"--kubeconfig", "(a kubeconfig for "+cfg.Host+")",
	)
	logger.Info("consumer workspaces opt in by creating an APIBinding to this APIExport; " +
		"see config/examples/apibinding-consumer.yaml")
}
