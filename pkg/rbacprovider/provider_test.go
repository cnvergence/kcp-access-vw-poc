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

package rbacprovider

import (
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/rest"

	"github.com/cnvergence/kcp-access-vw/pkg/graph"
)

func TestProviderScopesKCPBootstrapRoles(t *testing.T) {
	tests := []struct {
		name     string
		provider *Provider
		want     bool
	}{
		{
			name: "multi-shard KCP enables fallback automatically",
			provider: &Provider{
				RestConfig:             &rest.Config{},
				APIExportEndpointSlice: "access.kcp.io",
			},
			want: true,
		},
		{
			name: "single-shard KCP enables explicit fallback",
			provider: &Provider{
				RestConfig:        &rest.Config{},
				KCPBootstrapRoles: true,
			},
			want: true,
		},
		{
			name: "plain Kubernetes fails closed",
			provider: &Provider{
				RestConfig: &rest.Config{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := graph.New()
			translator := tt.provider.newTranslator(g)
			translator.ApplyClusterRoleBinding(&rbacv1.ClusterRoleBinding{
				RoleRef: rbacv1.RoleRef{
					APIGroup: rbacv1.GroupName,
					Kind:     "ClusterRole",
					Name:     "view",
				},
				Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "alice"}},
			}, graph.LogicalCluster("cluster"), "https://kcp.example/clusters/cluster")

			got := len(g.ClustersFor("alice", nil)) == 1
			if got != tt.want {
				t.Fatalf("bootstrap role visible = %v, want %v", got, tt.want)
			}
		})
	}
}
