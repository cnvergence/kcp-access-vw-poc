package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupName is the API group for the Access Virtual Workspace.
const GroupName = "access.kcp.io"

// SchemeGroupVersion is the group + version this package registers.
var SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: "v1alpha1"}

// Resource takes an unqualified resource name and returns a Group-qualified
// GroupResource for this package.
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

// Kind takes an unqualified kind name and returns a Group-qualified
// GroupKind for this package.
func Kind(kind string) schema.GroupKind {
	return SchemeGroupVersion.WithKind(kind).GroupKind()
}

var (
	// SchemeBuilder collects the type-registration functions for this
	// package. AddToScheme calls them in order.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	// AddToScheme registers the types in this package with a scheme.
	// Callers (the SCAR handler, future client-go integrations) use
	// this to make the codec-machinery aware of the types.
	AddToScheme = SchemeBuilder.AddToScheme
)

// addKnownTypes is the actual registration function. Kept private
// because callers should always go through SchemeBuilder/AddToScheme.
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&SelfClusterAccessReview{},
		&SelfClusterAccessReviewList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
