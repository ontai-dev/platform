// Package v1alpha1 contains API types for the seam.ontai.dev/v1alpha1 API group
// as owned by platform. TalosCluster is the primary type declared here.
//
// +groupName=seam.ontai.dev
// +kubebuilder:object:generate=true
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group + version for all types in this package.
	// API group: seam.ontai.dev. INV-008 -- this value is ground truth.
	GroupVersion = schema.GroupVersion{Group: "seam.ontai.dev", Version: "v1alpha1"}

	// SchemeBuilder registers Go types with the Kubernetes runtime scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds all types in this package to the provided scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
