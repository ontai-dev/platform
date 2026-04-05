package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam-core/pkg/lineage"
)

// Condition type and reason constants for PKIRotation.
const (
	// ConditionTypePKIRotationReady indicates the PKI rotation completed successfully.
	ConditionTypePKIRotationReady = "Ready"

	// ConditionTypePKIRotationDegraded indicates the PKI rotation failed.
	ConditionTypePKIRotationDegraded = "Degraded"

	// ReasonPKIJobSubmitted is set when the Conductor executor Job has been submitted.
	ReasonPKIJobSubmitted = "JobSubmitted"

	// ReasonPKIJobComplete is set when the Conductor executor Job completed successfully.
	ReasonPKIJobComplete = "JobComplete"

	// ReasonPKIJobFailed is set when the Conductor executor Job failed. INV-018 applies.
	ReasonPKIJobFailed = "JobFailed"

	// ReasonPKIOperationPending is set before the first Job submission.
	ReasonPKIOperationPending = "Pending"
)

// PKIRotationSpec defines the desired state of PKIRotation.
type PKIRotationSpec struct {
	// ClusterRef references the TalosCluster whose PKI is to be rotated.
	// Applies to both management and target clusters.
	ClusterRef LocalObjectRef `json:"clusterRef"`

	// Lineage is the sealed causal chain record for this root declaration.
	// Authored once at object creation time and immutable thereafter.
	// seam-core-schema.md §5, CLAUDE.md §14 Decision 1.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// PKIRotationStatus defines the observed state of PKIRotation.
type PKIRotationStatus struct {
	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// JobName is the name of the Conductor executor Job submitted for this rotation.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// OperationResult is the message from the Conductor OperationResult ConfigMap.
	// +optional
	OperationResult string `json:"operationResult,omitempty"`

	// Conditions is the list of status conditions for this PKIRotation.
	// Condition types: Ready, Degraded, LineageSynced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PKIRotation triggers a cluster PKI rotation for either the management cluster
// or a target cluster. Always submits a direct Conductor executor Job via the
// pki-rotate named capability regardless of the owning TalosCluster's
// capi.enabled value. CAPI has no PKI rotation equivalent.
// Named Conductor capability: pki-rotate. platform-schema.md §5.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=pkir
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type PKIRotation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PKIRotationSpec   `json:"spec,omitempty"`
	Status PKIRotationStatus `json:"status,omitempty"`
}

// PKIRotationList is the list type for PKIRotation.
//
// +kubebuilder:object:root=true
type PKIRotationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []PKIRotation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PKIRotation{}, &PKIRotationList{})
}
