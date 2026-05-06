package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SelfClusterAccessReview is the response a caller gets when asking
// the Access Virtual Workspace "which workspaces can I see?".
//
// It is a Kubernetes-shaped self-review resource, modelled on
// SelfSubjectAccessReview: callers POST a request, the server fills
// in Status from the authenticated identity, and the populated
// resource comes back in the response. There is no Spec because the
// caller does not parameterise the question — the only input is the
// caller's own identity, taken from the request's auth headers.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type SelfClusterAccessReview struct {
	metav1.TypeMeta `json:",inline"`
	// Standard object metadata. Empty on requests; the server may
	// populate CreationTimestamp on responses but otherwise leaves
	// this empty for self-review resources.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Status carries the result of the review.
	// +optional
	Status SelfClusterAccessReviewStatus `json:"status,omitempty"`
}

// SelfClusterAccessReviewStatus carries the list of clusters the
// authenticated subject can access.
type SelfClusterAccessReviewStatus struct {
	// Clusters is the list of logical clusters the subject has at
	// least 'view' access to, paired with each cluster's FrontProxy
	// endpoint URL. Sorted by ClusterName for stable output.
	// +optional
	Clusters []AccessEndpointSlice `json:"clusters,omitempty"`
}

// AccessEndpointSlice maps a logical cluster the caller can access
// to the API URL it can be reached at.
type AccessEndpointSlice struct {
	// ClusterName is the logical cluster identifier (kcp's
	// "logicalcluster" name).
	ClusterName string `json:"clusterName"`
	// Endpoint is the FrontProxy URL the caller can address this
	// cluster at.
	Endpoint string `json:"endpoint"`
}

// SelfClusterAccessReviewList is the list form of
// SelfClusterAccessReview. Self-review resources are normally not
// listed; the type exists so the resource can be registered with a
// scheme without special-casing.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type SelfClusterAccessReviewList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []SelfClusterAccessReview `json:"items"`
}
