package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam/pkg/lineage"
)

// Condition type and reason constants for MachineConfigSync.
const (
	// ConditionTypeMachineConfigSyncReady indicates the sync Job completed successfully.
	ConditionTypeMachineConfigSyncReady = "Ready"

	// ConditionTypeMachineConfigSyncDegraded indicates the sync Job failed.
	ConditionTypeMachineConfigSyncDegraded = "Degraded"

	// ConditionTypeMachineConfigSyncRunning indicates a Conductor executor Job is in flight.
	ConditionTypeMachineConfigSyncRunning = "Running"

	// ConditionTypeMachineConfigSyncLineageSynced indicates the LineageRecord descendant
	// entry for this sync has been written.
	ConditionTypeMachineConfigSyncLineageSynced = "LineageSynced"

	// ReasonMachineConfigSyncJobSubmitted is set when the Conductor executor Job is submitted.
	ReasonMachineConfigSyncJobSubmitted = "JobSubmitted"

	// ReasonMachineConfigSyncJobComplete is set when the Job completed successfully.
	ReasonMachineConfigSyncJobComplete = "JobComplete"

	// ReasonMachineConfigSyncJobFailed is set when the Job failed. INV-018 applies.
	ReasonMachineConfigSyncJobFailed = "JobFailed"

	// ReasonMachineConfigSyncHashMatch is set when the machineconfig hash matches the
	// last confirmed sync hash and forceApply=false. The sync is a no-op.
	ReasonMachineConfigSyncHashMatch = "HashMatch"

	// ReasonMachineConfigSyncPending is set before the first reconcile action.
	ReasonMachineConfigSyncPending = "Pending"

	// ReasonMachineConfigSyncPermanentFailure is set when the Job has failed
	// maxRetry times. No further Jobs will be submitted. Human intervention required.
	ReasonMachineConfigSyncPermanentFailure = "PermanentFailure"
)

// MachineConfigSyncSpec defines the desired state of MachineConfigSync.
// platform-schema.md §15.
type MachineConfigSyncSpec struct {
	// ClusterRef references the TalosCluster this sync targets.
	ClusterRef LocalObjectRef `json:"clusterRef"`

	// NodeClass identifies which class of machineconfig to sync.
	// Values: "controlplane", "worker", "node-{node-name}", or a compiler-generated
	// per-node short name such as "cp1", "cp2", "cp3".
	// +kubebuilder:validation:MinLength=1
	NodeClass string `json:"nodeClass"`

	// NodeRef is the IP address of the specific node that this sync targets.
	// When set, the Conductor executor applies the machineconfig to only this node
	// rather than all nodes enumerated in the cluster talosconfig. Used for
	// per-node import targeting (PLT-BUG-3-ARCH): one MachineConfigSync CR per
	// node, each pointing at a compiler-generated per-node secret via NodeClass.
	// When empty, the conductor applies to all nodes in the cluster talosconfig
	// (default class-based behavior).
	// +optional
	NodeRef string `json:"nodeRef,omitempty"`

	// MaxRetry is the maximum number of times the reconciler will re-submit the
	// Conductor executor Job after a failure before declaring permanent failure
	// and setting HumanInterventionRequired on the owning TalosCluster.
	// Defaults to 3 when unset or zero.
	// +optional
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	MaxRetry int `json:"maxRetry,omitempty"`

	// ForceApply skips the hash-equality check and reapplies the machineconfig
	// even if the node-side hash already matches. Use for repair scenarios.
	// +optional
	ForceApply bool `json:"forceApply,omitempty"`

	// Reason is a human-readable trigger description for the audit trail.
	// Examples: "import-initial-sync", "secret-content-changed", "day2-upgrade-complete".
	// +optional
	Reason string `json:"reason,omitempty"`

	// Lineage is the sealed causal chain record for this root declaration.
	// Authored once at object creation time and immutable thereafter.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// MachineConfigSyncStatus defines the observed state of MachineConfigSync.
type MachineConfigSyncStatus struct {
	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// JobName is the name of the Conductor executor Job submitted for this sync.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// RetryCount is the number of Job submission attempts that have failed so far.
	// Reset to zero on successful Job completion.
	// +optional
	RetryCount int `json:"retryCount,omitempty"`

	// ObservedHash is the SHA-256 hash of the machineconfig bytes that were applied.
	// Copied from the machineconfig Secret's sync-hash label after Job completion.
	// +optional
	ObservedHash string `json:"observedHash,omitempty"`

	// OperationResult is the result message from the Conductor OperationResult ConfigMap.
	// +optional
	OperationResult string `json:"operationResult,omitempty"`

	// Conditions is the list of status conditions for this MachineConfigSync.
	// Condition types: Ready, Degraded, LineageSynced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// MachineConfigSync is a day-2 operation CR that drives a Conductor exec Job to apply
// a Talos machineconfig from the canonical source-of-truth Secret to target nodes.
//
// Created by:
//   - TalosClusterReconciler on Secret content hash change (RECON-A6)
//   - import flow after reading node configs (RECON-A2: reason=import-initial-sync)
//   - day2 op completion hooks (RECON-A7: reason=day2-{capability}-complete)
//
// Named Conductor capability: machineconfig-sync. platform-schema.md §15.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=mcs
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=".spec.nodeClass"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type MachineConfigSync struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MachineConfigSyncSpec   `json:"spec,omitempty"`
	Status MachineConfigSyncStatus `json:"status,omitempty"`
}

// MachineConfigSyncList is the list type for MachineConfigSync.
//
// +kubebuilder:object:root=true
type MachineConfigSyncList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MachineConfigSync `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MachineConfigSync{}, &MachineConfigSyncList{})
}
