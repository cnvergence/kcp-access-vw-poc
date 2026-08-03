/*
Copyright 2026 The kcp Authors.

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

// Package bootstrap installs the kcp-side objects the access virtual
// workspace needs, and verifies that they came up.
//
// The apply logic is deliberately local rather than importing
// github.com/kcp-dev/kcp/config/helpers: that would pull kcp core and its
// Kubernetes fork into this module for about a hundred lines of
// create-or-update. platform-mesh/provider-quickstart makes the same trade.
package bootstrap

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	configapiexport "github.com/cnvergence/kcp-access-vw/config/apiexport"
)

// Names of the objects the access VW installs. The APIExport, its
// endpoint slice and the API group all share one name so operators have a
// single string to reason about.
const (
	APIExportName = "access.kcp.io"
)

var apiExportEndpointSliceGVR = schema.GroupVersionResource{
	Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
}

// Result reports what a successful bootstrap produced, so callers can
// print the values the server needs.
type Result struct {
	// WorkspacePath is the absolute path of the workspace the objects
	// were installed into.
	WorkspacePath string

	// APIExportEndpointSlice is the name to pass to the server's
	// --apiexport-endpointslice flag.
	APIExportEndpointSlice string

	// VirtualWorkspaceURLs are the per-shard URLs the slice resolved to.
	// Empty means the provider will discover nothing.
	VirtualWorkspaceURLs []string
}

// Options configures Bootstrap.
type Options struct {
	// WorkspacePath is the absolute path the objects were installed
	// into, used only for reporting.
	WorkspacePath string

	// Timeout bounds the apply-and-verify loop.
	Timeout time.Duration
}

// Bootstrap applies the embedded manifests to the workspace cfg points at,
// then waits for the APIExport and its endpoint slice to become usable.
func Bootstrap(ctx context.Context, cfg *rest.Config, opts Options) (*Result, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 3 * time.Minute
	}

	logger := klog.FromContext(ctx)
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	dynamicClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build dynamic client: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build discovery client: %w", err)
	}
	cache := memory.NewMemCacheClient(discoveryClient)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cache)

	logger.Info("applying access virtual workspace assets", "workspace", opts.WorkspacePath)
	if err := applyFS(ctx, dynamicClient, mapper, cache, configapiexport.FS); err != nil {
		return nil, fmt.Errorf("apply assets: %w", err)
	}

	urls, err := waitForEndpointSlice(ctx, dynamicClient)
	if err != nil {
		return nil, err
	}

	return &Result{
		WorkspacePath:          opts.WorkspacePath,
		APIExportEndpointSlice: APIExportName,
		VirtualWorkspaceURLs:   urls,
	}, nil
}

func applyFS(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, cache discovery.CachedDiscoveryInterface, fs embed.FS) error {
	logger := klog.FromContext(ctx)

	var lastErr error
	attempt := 0
	err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		attempt++
		if err := applyResources(ctx, client, mapper, fs); err != nil {
			lastErr = err
			logger.V(2).Info("assets not applied yet, retrying", "attempt", attempt, "err", err)
			// New CRDs may have appeared since the mapper last looked.
			cache.Invalidate()
			return false, nil
		}
		logger.Info("assets applied", "attempts", attempt)
		return true, nil
	})
	if err != nil && lastErr != nil {
		return fmt.Errorf("%w (last error: %v)", err, lastErr)
	}
	return err
}

func applyResources(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, fs embed.FS) error {
	entries, err := fs.ReadDir(".")
	if err != nil {
		return fmt.Errorf("read embedded assets: %w", err)
	}

	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		if err := applyFile(ctx, client, mapper, fs, e.Name()); err != nil {
			errs = append(errs, err)
		}
	}
	return utilerrors.NewAggregate(errs)
}

func applyFile(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, fs embed.FS, filename string) error {
	raw, err := fs.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read %s: %w", filename, err)
	}

	reader := kubeyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))
	var errs []error
	for i := 1; ; i++ {
		doc, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read document %d of %s: %w", i, filename, err)
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		if err := applyResource(ctx, client, mapper, doc); err != nil {
			errs = append(errs, fmt.Errorf("%s document %d: %w", filename, i, err))
		}
	}
	return utilerrors.NewAggregate(errs)
}

func applyResource(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, raw []byte) error {
	logger := klog.FromContext(ctx)

	u := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(raw, &u.Object); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	gvk := u.GroupVersionKind()
	if gvk.Kind == "" {
		return errors.New("manifest has no kind")
	}

	m, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("REST mapping for %s: %w", gvk, err)
	}

	resource := client.Resource(m.Resource).Namespace(u.GetNamespace())

	existing, err := resource.Get(ctx, u.GetName(), metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := resource.Create(ctx, u, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create %s %s: %w", gvk.Kind, u.GetName(), err)
		}
		logger.Info("created", "kind", gvk.Kind, "name", u.GetName())
		return nil
	case err != nil:
		return fmt.Errorf("get %s %s: %w", gvk.Kind, u.GetName(), err)
	}

	u.SetResourceVersion(existing.GetResourceVersion())
	if _, err := resource.Update(ctx, u, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update %s %s: %w", gvk.Kind, u.GetName(), err)
	}
	logger.V(2).Info("updated", "kind", gvk.Kind, "name", u.GetName())
	return nil
}

func waitForEndpointSlice(ctx context.Context, client dynamic.Interface) ([]string, error) {
	logger := klog.FromContext(ctx)

	var urls []string
	err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		slice, err := client.Resource(apiExportEndpointSliceGVR).Get(ctx, APIExportName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}

		endpoints, found, err := unstructured.NestedSlice(slice.Object, "status", "endpoints")
		if err != nil || !found || len(endpoints) == 0 {
			logger.V(2).Info("waiting for APIExportEndpointSlice endpoints", "name", APIExportName)
			return false, nil
		}

		urls = nil
		for _, e := range endpoints {
			entry, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if u, ok := entry["url"].(string); ok && u != "" {
				urls = append(urls, u)
			}
		}
		return len(urls) > 0, nil
	})
	if err != nil {
		return nil, fmt.Errorf("APIExportEndpointSlice %q has no virtual workspace endpoints: %w "+
			"(is the APIExport valid, and is a shard serving it?)", APIExportName, err)
	}

	return urls, nil
}

// VerifyAPIBinding reports whether the given workspace has an APIBinding to
// this APIExport with all of its permission claims accepted.
func VerifyAPIBinding(ctx context.Context, cfg *rest.Config, bindingName string) error {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("build dynamic client: %w", err)
	}

	binding, err := client.Resource(schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings",
	}).Get(ctx, bindingName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get APIBinding %q: %w", bindingName, err)
	}

	declared, _, _ := unstructured.NestedSlice(binding.Object, "spec", "permissionClaims")
	applied, _, _ := unstructured.NestedSlice(binding.Object, "status", "appliedPermissionClaims")

	if len(applied) < len(declared) {
		return fmt.Errorf("APIBinding %q has %d permission claims declared but only %d applied; "+
			"the virtual workspace cannot see RBAC objects until they are accepted",
			bindingName, len(declared), len(applied))
	}
	return nil
}
