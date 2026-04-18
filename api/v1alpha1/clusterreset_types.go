package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam-core/pkg/lineage"
)

// Condition type and reason constants for ClusterReset.
const (
	// ConditionTypeResetReady indicates the cluster reset completed successfully.
	ConditionTypeResetReady = "Ready"

	// ConditionTypeResetDegraded indicates the reset operation failed.
	ConditionTypeResetDegraded = "Degraded"

	// ConditionTypeResetPendingApproval indicates the reset is waiting for the
	// human approval annotation. CP-INV-006, INV-007.
	ConditionTypeResetPendingApproval = "PendingApproval"

	// ReasonApprovalRequired is set when ontai.dev/reset-approved=true annotation
	// is absent. The reconciler halts and waits for human approval. CP-INV-006.
	ReasonApprovalRequired = "ApprovalRequired"

	// ReasonCAPIClusterDeleting is set when the CAPI Cluster deletion is in
	// progress (capi.enabled=true path). The reconciler waits for all Machine
	// objects to reach Deleted phase before submitting the reset Job.
	ReasonCAPIClusterDeleting = "CAPIClusterDeleting"

	// ReasonCAPIClusterDrained is set when all CAPI Machine objects have reached
	// Deleted phase and the reset Job is about to be submitted.
	ReasonCAPIClusterDrained = "CAPIClusterDrained"

	// ReasonResetJobSubmitted is set when the Conductor executor Job has been submitted.
	ReasonResetJobSubmitted = "JobSubmitted"

	// ReasonResetJobComplete is set when the Conductor executor Job completed successfully.
	ReasonResetJobComplete = "JobComplete"

	// ReasonResetJobFailed is set when the Conductor executor Job failed. INV-018 applies.
	ReasonResetJobFailed = "JobFailed"

	// ReasonResetComplete is set when the full reset sequence has completed and the
	// tenant namespace has been deleted.
	ReasonResetComplete = "ResetComplete"
)

// ResetApprovalAnnotation is the annotation that must be present with value "true"
// before the ClusterReset reconciler proceeds. INV-007, CP-INV-006.
const ResetApprovalAnnotation = "ontai.dev/reset-approved"

// ClusterResetSpec defines the desired state of ClusterReset.
type ClusterResetSpec struct {
	// ClusterRef references the TalosCluster to reset. The cluster and its tenant
	// namespace will be factory reset and decommissioned.
	ClusterRef LocalObjectRef `json:"clusterRef"`

	// DrainGracePeriodSeconds is the number of seconds to wait for node drain to
	// complete before forcing the reset. Defaults to 300 if not set.
	// +optional
	DrainGracePeriodSeconds int32 `json:"drainGracePeriodSeconds,omitempty"`

	// WipeDisks controls whether the Talos reset API is called with wipeDisks=true,
	// which destroys all data on the node's disks. Defaults to false.
	// +optional
	WipeDisks bool `json:"wipeDisks,omitempty"`

	// Lineage is the sealed causal chain record for this root declaration.
	// Authored once at object creation time and immutable thereafter.
	// seam-core-schema.md §5, CLAUDE.md §14 Decision 1.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// ClusterResetStatus defines the observed state of ClusterReset.
type ClusterResetStatus struct {
	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// JobName is the name of the Conductor executor Job submitted for the reset.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// OperationResult is the message from the Conductor OperationResult ConfigMap.
	// +optional
	OperationResult string `json:"operationResult,omitempty"`

	// Conditions is the list of status conditions for this ClusterReset.
	// Condition types: PendingApproval, Ready, Degraded, LineageSynced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ClusterReset performs a destructive factory reset of a Talos cluster.
//
// HUMAN GATE REQUIRED: the ontai.dev/reset-approved=true annotation must be
// present on this object before any reconciliation proceeds. The reconciler
// holds at PendingApproval and emits an event if the annotation is absent.
// INV-007, CP-INV-006.
//
// For CAPI-managed clusters (capi.enabled=true): the CAPI Cluster object is
// deleted first, then all Machine objects are drained through the Seam
// Infrastructure Provider, then the cluster-reset Conductor Job is submitted.
//
// For management cluster (capi.enabled=false): the cluster-reset Conductor Job
// is submitted directly.
//
// Named Conductor capability: cluster-reset. platform-schema.md §5.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=crst
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="PendingApproval",type=string,JSONPath=".status.conditions[?(@.type==\"PendingApproval\")].status"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type ClusterReset struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterResetSpec   `json:"spec,omitempty"`
	Status ClusterResetStatus `json:"status,omitempty"`
}

// ClusterResetList is the list type for ClusterReset.
//
// +kubebuilder:object:root=true
type ClusterResetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ClusterReset `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterReset{}, &ClusterResetList{})
}
