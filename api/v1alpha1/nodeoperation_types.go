package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam/pkg/lineage"
)

// NodeOperationType declares the node lifecycle operation to perform.
//
// +kubebuilder:validation:Enum=scale-up;decommission;reboot;rollback
type NodeOperationType string

const (
	// NodeOperationTypeScaleUp adds nodes to a worker pool.
	NodeOperationTypeScaleUp NodeOperationType = "scale-up"

	// NodeOperationTypeDecommission removes specific nodes from the cluster.
	NodeOperationTypeDecommission NodeOperationType = "decommission"

	// NodeOperationTypeReboot reboots specific nodes.
	NodeOperationTypeReboot NodeOperationType = "reboot"

	// NodeOperationTypeRollback rolls target nodes back to the previous Talos OS image.
	// Used after a failed upgrade to restore the prior version. RECON-H4.
	NodeOperationTypeRollback NodeOperationType = "rollback"
)

// Condition type and reason constants for NodeOperation.
const (
	// ConditionTypeNodeOperationReady indicates the operation completed successfully.
	ConditionTypeNodeOperationReady = "Ready"

	// ConditionTypeNodeOperationDegraded indicates the operation failed.
	ConditionTypeNodeOperationDegraded = "Degraded"

	// ReasonNodeOpJobSubmitted is set when the Conductor executor Job has been submitted.
	ReasonNodeOpJobSubmitted = "JobSubmitted"

	// ReasonNodeOpJobComplete is set when the Conductor executor Job completed successfully.
	ReasonNodeOpJobComplete = "JobComplete"

	// ReasonNodeOpJobFailed is set when the Conductor executor Job failed. INV-018 applies.
	ReasonNodeOpJobFailed = "JobFailed"

	// ReasonNodeOpPending is set before the first action.
	ReasonNodeOpPending = "Pending"

	// ReasonNodeOpPermanentFailure is set when the Job has failed maxRetry times.
	// No further Jobs will be submitted. Human intervention required.
	ReasonNodeOpPermanentFailure = "PermanentFailure"
)

// NodeOperationSpec defines the desired state of NodeOperation.
type NodeOperationSpec struct {
	// ClusterRef references the TalosCluster this operation targets.
	ClusterRef LocalObjectRef `json:"clusterRef"`

	// Operation declares the node lifecycle operation to perform.
	// +kubebuilder:validation:Enum=scale-up;decommission;reboot;rollback
	Operation NodeOperationType `json:"operation"`

	// TargetNodes is the list of node names to target for decommission or reboot.
	// Required when operation=decommission or operation=reboot.
	// +optional
	TargetNodes []string `json:"targetNodes,omitempty"`

	// TargetNodeIP is the IP address of the new node in Talos maintenance mode.
	// Required when operation=scale-up.
	// +optional
	TargetNodeIP string `json:"targetNodeIP,omitempty"`

	// NodeRole declares the role of the node for scale-up operations.
	// Valid values are "controlplane" and "worker". Defaults to "worker" when unset.
	// +optional
	// +kubebuilder:validation:Enum=controlplane;worker
	NodeRole string `json:"nodeRole,omitempty"`

	// MaxRetry is the maximum number of times the reconciler will re-submit the
	// Conductor executor Job after a failure before declaring permanent failure
	// and setting HumanInterventionRequired on the owning TalosCluster.
	// Defaults to 3 when unset or zero.
	// +optional
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	MaxRetry int `json:"maxRetry,omitempty"`

	// ReplicaCount is the desired number of worker replicas after scale-up.
	// Required when operation=scale-up.
	// +optional
	ReplicaCount int32 `json:"replicaCount,omitempty"`

	// PerformWipe enables a secure disk wipe after decommission reset.
	// Only valid when operation=decommission. Caller must satisfy INV-007 approval
	// gate before setting this field. RECON-H4.
	// +optional
	PerformWipe bool `json:"performWipe,omitempty"`

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

	// RetryCount is the number of Job submission attempts that have failed so far.
	// Reset to zero on successful Job completion.
	// +optional
	RetryCount int `json:"retryCount,omitempty"`

	// JobName is the name of the Conductor executor Job submitted for this operation.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// OperationResult is the message from the Conductor OperationResult ConfigMap.
	// +optional
	OperationResult string `json:"operationResult,omitempty"`

	// Conditions is the list of status conditions for this NodeOperation.
	// Condition types: Ready, Degraded, LineageSynced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// NodeOperation governs node lifecycle operations: scale-up, decommission, reboot.
// Submits a node-scale-up, node-decommission, or node-reboot Conductor executor Job.
//
// Named Conductor capabilities: node-scale-up, node-decommission, node-reboot.
// platform-schema.md §5.
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
