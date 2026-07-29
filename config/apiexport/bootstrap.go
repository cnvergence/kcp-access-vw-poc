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

// Package apiexport holds the kcp-side assets the access virtual workspace
// needs in the workspace that hosts its APIExport: the APIResourceSchema for
// SelfClusterAccessReview, the APIExport itself, and the
// APIExportEndpointSlice the RBAC provider follows.
//
// They are embedded rather than applied with kubectl so the init binary can
// bootstrap a deployment on its own, following the same pattern as kcp's own
// config packages (config/root, config/shard, ...).
package apiexport

import "embed"

//go:embed *.yaml
var FS embed.FS
