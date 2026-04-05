package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam-core/pkg/lineage"
)

// NodeOperationType declares the node lifecycle operation to perform.
//
// +kubebuilder:validation:Enum=scale-up;decommission;reboot
type NodeOperationType string

const (
	// NodeOperationTypeScaleUp adds nodes to a worker pool.
	NodeOperationTypeScaleUp NodeOperationType = "scale-up"

	// NodeOperationTypeDecommission removes specific nodes from the cluster.
	NodeOperationTypeDecommission NodeOperationType = "decommission"

	// NodeOperationTypeReboot reboots specific nodes.
	NodeOperationTypeReboot NodeOperationType = "reboot"
)

// Condition type and reason constants for NodeOperation.
const (
	// ConditionTypeNodeOperationReady indicates the operation completed successfully.
	ConditionTypeNodeOperationReady = "Ready"

	// ConditionTypeNodeOperationDegraded indicates the operation failed.
	ConditionTypeNodeOperationDegraded = "Degraded"

	// ConditionTypeNodeOperationCAPIDelegated indicates the operation has been
	// delegated to CAPI native machinery (capi.enabled=true path).
	ConditionTypeNodeOperationCAPIDelegated = "CAPIDelegated"

	// ReasonNodeOpJobSubmitted is set when the Conductor executor Job has been submitted.
	ReasonNodeOpJobSubmitted = "JobSubmitted"

	// ReasonNodeOpJobComplete is set when the Conductor executor Job completed successfully.
	ReasonNodeOpJobComplete = "JobComplete"

	// ReasonNodeOpJobFailed is set when the Conductor executor Job failed. INV-018 applies.
	ReasonNodeOpJobFailed = "JobFailed"

	// ReasonNodeOpCAPIDelegated is set when the operation is delegated to CAPI
	// for capi.enabled=true clusters.
	ReasonNodeOpCAPIDelegated = "CAPIDelegated"

	// ReasonNodeOpPending is set before the first action.
	ReasonNodeOpPending = "Pending"
)

// NodeOperationSpec defines the desired state of NodeOperation.
type NodeOperationSpec struct {
	// ClusterRef references the TalosCluster this operation targets.
	ClusterRef LocalObjectRef `json:"clusterRef"`

	// Operation declares the node lifecycle operation to perform.
	// +kubebuilder:validation:Enum=scale-up;decommission;reboot
	Operation NodeOperationType `json:"operation"`

	// TargetNodes is the list of node names to target for decommission or reboot.
	// Required when operation=decommission or operation=reboot.
	// +optional
	TargetNodes []string `json:"targetNodes,omitempty"`

	// ReplicaCount is the desired number of worker replicas after scale-up.
	// Required when operation=scale-up.
	// +optional
	ReplicaCount int32 `json:"replicaCount,omitempty"`

	// Lineage is the sealed causal chain record for this root declaration.
	// Authored once at object creation time and immutable thereafter.
	// seam-core-schema.md §5, CLAUDE.md §14 Decision 1.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// NodeOperationStatus defines the observed state of NodeOperation.
type NodeOperationStatus struct {
	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// JobName is the name of the Conductor executor Job submitted for this operation.
	// Only set for the capi.enabled=false (non-CAPI) path.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// OperationResult is the message from the Conductor OperationResult ConfigMap.
	// +optional
	OperationResult string `json:"operationResult,omitempty"`

	// Conditions is the list of status conditions for this NodeOperation.
	// Condition types: Ready, Degraded, CAPIDelegated, LineageSynced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// NodeOperation governs node lifecycle operations: scale-up, decommission, reboot.
//
// Dual-path CRD governed by spec.capi.enabled on the owning TalosCluster:
//   - For CAPI-managed clusters (capi.enabled=true): modifies MachineDeployment
//     replicas for scale-up, deletes specific Machine objects for decommission,
//     or sets the Machine reboot annotation — all handled natively by CAPI.
//   - For management cluster (capi.enabled=false): submits node-scale-up,
//     node-decommission, or node-reboot Conductor executor Job.
//
// Named Conductor capabilities (non-CAPI path): node-scale-up, node-decommission,
// node-reboot. platform-schema.md §5.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nop
// +kubebuilder:printcolumn:name="Operation",type=string,JSONPath=".spec.operation"
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type NodeOperation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeOperationSpec   `json:"spec,omitempty"`
	Status NodeOperationStatus `json:"status,omitempty"`
}

// NodeOperationList is the list type for NodeOperation.
//
// +kubebuilder:object:root=true
type NodeOperationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []NodeOperation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeOperation{}, &NodeOperationList{})
}
