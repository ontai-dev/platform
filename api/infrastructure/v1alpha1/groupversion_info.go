// Package v1alpha1 contains API Schema definitions for the Seam Infrastructure
// Provider CRDs under the infrastructure.cluster.x-k8s.io API group.
//
// SeamInfrastructureCluster and SeamInfrastructureMachine implement the CAPI
// InfrastructureCluster and InfrastructureMachine contracts for Talos nodes.
// platform-schema.md §4.

// +groupName=infrastructure.cluster.x-k8s.io
// +kubebuilder:object:generate=true
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the CAPI infrastructure provider API group and version.
	GroupVersion = schema.GroupVersion{Group: "infrastructure.cluster.x-k8s.io", Version: "v1alpha1"}

	// SchemeBuilder is the scheme builder for Seam Infrastructure Provider types.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds all Seam Infrastructure Provider types to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
