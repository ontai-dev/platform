package v1alpha1

// MaintenanceBundle is a pre-compiled scheduling artifact produced by
// `compiler maintenance`. It pre-encodes the scheduling context required for a
// maintenance operation so that neither Platform nor Conductor need to perform
// cluster queries at execution time.
//
// conductor-schema.md §9, platform-schema.md §10.
// F-P5: type definition delivered; reconciler is a stub.

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam-core/pkg/lineage"
)

// MaintenanceBundleOperation declares the maintenance operation type.
// Determines which Conductor capability the resulting RunnerConfig targets.
//
// +kubebuilder:validation:Enum=drain;upgrade;etcd-backup;machineconfig-rotation
type MaintenanceBundleOperation string

const (
	// MaintenanceBundleOperationDrain drains specified target nodes.
	MaintenanceBundleOperationDrain MaintenanceBundleOperation = "drain"

	// MaintenanceBundleOperationUpgrade upgrades Talos or Kubernetes on target nodes.
	MaintenanceBundleOperationUpgrade MaintenanceBundleOperation = "upgrade"

	// MaintenanceBundleOperationEtcdBackup takes an etcd snapshot backup to S3.
	MaintenanceBundleOperationEtcdBackup MaintenanceBundleOperation = "etcd-backup"

	// MaintenanceBundleOperationMachineConfigRotation applies machineconfig rotation.
	MaintenanceBundleOperationMachineConfigRotation MaintenanceBundleOperation = "machineconfig-rotation"
)

// Condition type and reason constants for MaintenanceBundle.
const (
	// ConditionTypeMaintenanceBundleReady indicates the bundle has been processed
	// and a RunnerConfig has been created for execution.
	ConditionTypeMaintenanceBundleReady = "Ready"

	// ConditionTypeMaintenanceBundlePending indicates the bundle is awaiting processing.
	ConditionTypeMaintenanceBundlePending = "Pending"

	// ConditionTypeMaintenanceBundleDegraded indicates bundle processing failed.
	ConditionTypeMaintenanceBundleDegraded = "Degraded"

	// ReasonMaintenanceBundlePending is set when the reconciler first observes the CR.
	ReasonMaintenanceBundlePending = "Pending"

	// ReasonMaintenanceBundleReconcilerNotImplemented marks the stub reconciler state.
	ReasonMaintenanceBundleReconcilerNotImplemented = "ReconcilerNotImplemented"

	// ReasonMaintenanceBundleJobSubmitted is set when the Conductor executor Job is submitted.
	ReasonMaintenanceBundleJobSubmitted = "JobSubmitted"

	// ReasonMaintenanceBundleJobComplete is set when the executor Job completes successfully.
	ReasonMaintenanceBundleJobComplete = "JobComplete"

	// ReasonMaintenanceBundleJobFailed is set when the executor Job reports failure.
	ReasonMaintenanceBundleJobFailed = "JobFailed"

	// ReasonMaintenanceBundleCapabilityUnknown is set when the operation type cannot be
	// mapped to a named Conductor capability. This is a programming error.
	ReasonMaintenanceBundleCapabilityUnknown = "CapabilityUnknown"
)

// MaintenanceBundleSpec defines the desired state of MaintenanceBundle.
// All fields are pre-resolved by `compiler maintenance` at compile time.
// conductor-schema.md §9.
type MaintenanceBundleSpec struct {
	// ClusterRef references the TalosCluster this bundle targets.
	ClusterRef LocalObjectRef `json:"clusterRef"`

	// Operation declares the maintenance operation type.
	// Determines which Conductor capability the resulting RunnerConfig targets.
	// conductor-schema.md §9.
	// +kubebuilder:validation:Enum=drain;upgrade;etcd-backup;machineconfig-rotation
	Operation MaintenanceBundleOperation `json:"operation"`

	// MaintenanceTargetNodes is the pre-resolved list of nodes targeted by the
	// operation. Validated against the live cluster by `compiler maintenance`.
	// Directly populates RunnerConfig.MaintenanceTargetNodes at execution time.
	// +optional
	MaintenanceTargetNodes []string `json:"maintenanceTargetNodes,omitempty"`

	// OperatorLeaderNode is the node hosting the leader pod of the Platform operator
	// at compile time, resolved from the platform-leader Lease in seam-system.
	// Directly populates RunnerConfig.OperatorLeaderNode at execution time.
	// conductor-schema.md §13.
	// +optional
	OperatorLeaderNode string `json:"operatorLeaderNode,omitempty"`

	// S3ConfigSecretRef references the S3 configuration Secret, pre-resolved and
	// validated to exist by `compiler maintenance`. Follows the platform-schema.md §10
	// S3 resolution hierarchy. A MaintenanceBundle is never committed without a valid
	// S3 reference when the operation requires it (etcd-backup).
	// +optional
	S3ConfigSecretRef *corev1.SecretReference `json:"s3ConfigSecretRef,omitempty"`

	// Lineage is the sealed causal chain record for this root declaration.
	// Authored once at object creation time and immutable thereafter.
	// seam-core-schema.md §5, CLAUDE.md §14 Decision 1.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// MaintenanceBundleStatus defines the observed state of MaintenanceBundle.
type MaintenanceBundleStatus struct {
	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// JobName is the name of the Conductor executor Job submitted for this bundle.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// OperationResult carries the result message written by the Conductor executor Job.
	// +optional
	OperationResult string `json:"operationResult,omitempty"`

	// Conditions is the list of status conditions for this MaintenanceBundle.
	// Condition types: Ready, Pending, Degraded, LineageSynced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// MaintenanceBundle is a pre-compiled scheduling artifact produced by
// `compiler maintenance`. Carries pre-resolved scheduling context so neither
// Platform nor Conductor need to perform cluster queries at execution time.
// conductor-schema.md §9. API group: platform.ontai.dev.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=mb
// +kubebuilder:printcolumn:name="Operation",type=string,JSONPath=".spec.operation"
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type MaintenanceBundle struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MaintenanceBundleSpec   `json:"spec,omitempty"`
	Status MaintenanceBundleStatus `json:"status,omitempty"`
}

// MaintenanceBundleList is the list type for MaintenanceBundle.
//
// +kubebuilder:object:root=true
type MaintenanceBundleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MaintenanceBundle `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MaintenanceBundle{}, &MaintenanceBundleList{})
}
