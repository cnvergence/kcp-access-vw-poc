// Package v1alpha1 holds the API types for the Access Virtual
// Workspace (group access.kcp.io).
//
// The MVP exposes a single resource — SelfClusterAccessReview — that
// callers POST against to get back the list of logical clusters they
// can see, along with each cluster's FrontProxy endpoint URL.
//
// The types use the standard Kubernetes machinery (metav1.TypeMeta,
// metav1.ObjectMeta, registration to a runtime.Scheme via SchemeBuilder)
// so that a) the SCAR handler can decode/encode with the Kubernetes
// codecs and b) downstream tooling (kubectl, generated clients) sees
// the resource as a normal Kubernetes object once the proper
// APIServer plumbing lands.
//
// +kubebuilder:object:generate=true
// +groupName=access.kcp.io
package v1alpha1
