package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam/pkg/lineage"
)

// Condition type and reason constants for TalosMachineConfigRestore.
const (
	// ConditionTypeMachineConfigRestoreReady indicates the restore completed successfully.
	ConditionTypeMachineConfigRestoreReady = "Ready"

	// ConditionTypeMachineConfigRestoreRunning indicates the Conductor Job is running.
	ConditionTypeMachineConfigRestoreRunning = "Running"

	// ConditionTypeMachineConfigRestoreDegraded indicates the restore failed.
	ConditionTypeMachineConfigRestoreDegraded = "Degraded"

	// ReasonMachineConfigRestoreJobSubmitted is set when the Conductor executor Job is submitted.
	ReasonMachineConfigRestoreJobSubmitted = "JobSubmitted"

	// ReasonMachineConfigRestoreJobComplete is set when the Job completed successfully.
	ReasonMachineConfigRestoreJobComplete = "JobComplete"

	// ReasonMachineConfigRestoreJobFailed is set when the Job failed. INV-018 applies.
	ReasonMachineConfigRestoreJobFailed = "JobFailed"

	// ReasonMachineConfigRestoreS3Absent indicates no S3 source is configured.
	ReasonMachineConfigRestoreS3Absent = "S3SourceAbsent"

	// ConditionTypeMachineConfigRestoreS3Absent is the condition type for absent S3 config.
	ConditionTypeMachineConfigRestoreS3Absent = "S3SourceAbsent"
)

// TalosMachineConfigRestoreSpec defines the desired state of TalosMachineConfigRestore.
type TalosMachineConfigRestoreSpec struct {
	// ClusterRef references the TalosCluster whose nodes will have their machine
	// config restored.
	ClusterRef LocalObjectRef `json:"clusterRef"`

	// BackupTimestamp identifies which backup to restore from. Must match the
	// timestamp component of the S3 path written by a prior machineconfig-backup
	// operation: {cluster}/machineconfigs/{backupTimestamp}/{hostname}.yaml.
	// Format: 20060102T150405Z (UTC).
	BackupTimestamp string `json:"backupTimestamp"`

	// TargetNodes is the optional list of node hostnames to restore. When empty
	// all nodes in the cluster are restored. When set only the listed hostnames
	// are restored.
	// +optional
	TargetNodes []string `json:"targetNodes,omitempty"`

	// S3SourceBucket is the S3 bucket containing the backup objects. Must match
	// the bucket used during the original machineconfig-backup operation.
	S3SourceBucket string `json:"s3SourceBucket"`

	// S3BackupSecretRef references a Secret containing S3 credentials.
	// Falls back to seam-etcd-backup-config in seam-system when absent.
	// platform-schema.md §10.
	// +optional
	S3BackupSecretRef *corev1.SecretReference `json:"s3BackupSecretRef,omitempty"`

	// Lineage is the sealed causal chain record for this root declaration.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// TalosMachineConfigRestoreStatus defines the observed state of TalosMachineConfigRestore.
type TalosMachineConfigRestoreStatus struct {
	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is the current phase of the restore operation.
	// One of: Pending, Running, Succeeded, Failed, PartiallyFailed.
	// +optional
	Phase string `json:"phase,omitempty"`

	// JobName is the name of the most recently submitted Conductor executor Job.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// OperationResult is the message from the Conductor OperationResult ConfigMap.
	// +optional
	OperationResult string `json:"operationResult,omitempty"`

	// RestoredNodes is the list of node hostnames successfully restored.
	// +optional
	RestoredNodes []string `json:"restoredNodes,omitempty"`

	// Conditions is the list of status conditions for this TalosMachineConfigRestore.
	// Condition types: Ready, Running, Degraded, S3SourceAbsent, LineageSynced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// TalosMachineConfigRestore triggers a machine config restore for target nodes of a
// cluster. The Conductor executor downloads each node's config from S3 at
// {cluster}/machineconfigs/{backupTimestamp}/{hostname}.yaml and applies it via
// ApplyConfiguration. Named Conductor capability: machineconfig-restore.
// platform-schema.md §11.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=mcr
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Timestamp",type=string,JSONPath=".spec.backupTimestamp"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type TalosMachineConfigRestore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TalosMachineConfigRestoreSpec   `json:"spec,omitempty"`
	Status TalosMachineConfigRestoreStatus `json:"status,omitempty"`
}

// TalosMachineConfigRestoreList is the list type for TalosMachineConfigRestore.
//
// +kubebuilder:object:root=true
type TalosMachineConfigRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []TalosMachineConfigRestore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TalosMachineConfigRestore{}, &TalosMachineConfigRestoreList{})
}
