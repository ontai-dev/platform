package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam-core/pkg/lineage"
)

// Condition type and reason constants for TalosMachineConfigBackup.
const (
	// ConditionTypeMachineConfigBackupReady indicates the backup completed successfully.
	ConditionTypeMachineConfigBackupReady = "Ready"

	// ConditionTypeMachineConfigBackupRunning indicates the Conductor Job is running.
	ConditionTypeMachineConfigBackupRunning = "Running"

	// ConditionTypeMachineConfigBackupDegraded indicates the backup failed.
	ConditionTypeMachineConfigBackupDegraded = "Degraded"

	// ReasonMachineConfigBackupJobSubmitted is set when the Conductor executor Job is submitted.
	ReasonMachineConfigBackupJobSubmitted = "JobSubmitted"

	// ReasonMachineConfigBackupJobComplete is set when the Job completed successfully.
	ReasonMachineConfigBackupJobComplete = "JobComplete"

	// ReasonMachineConfigBackupJobFailed is set when the Job failed. INV-018 applies.
	ReasonMachineConfigBackupJobFailed = "JobFailed"

	// ReasonMachineConfigBackupS3Absent indicates no S3 backup destination is configured.
	ReasonMachineConfigBackupS3Absent = "S3DestinationAbsent"

	// ConditionTypeMachineConfigBackupS3Absent is the condition type for absent S3 config.
	ConditionTypeMachineConfigBackupS3Absent = "S3DestinationAbsent"
)

// TalosMachineConfigBackupSpec defines the desired state of TalosMachineConfigBackup.
type TalosMachineConfigBackupSpec struct {
	// ClusterRef references the TalosCluster whose node machine configs are to be backed up.
	ClusterRef LocalObjectRef `json:"clusterRef"`

	// S3BackupSecretRef references a Secret containing S3 backup credentials for this
	// operation. Takes precedence over the cluster-wide seam-etcd-backup-config Secret
	// in seam-system. platform-schema.md §10.
	// +optional
	S3BackupSecretRef *corev1.SecretReference `json:"s3BackupSecretRef,omitempty"`

	// S3Destination is the S3 location to write node machine configs to.
	// The bucket is required. The key prefix is auto-generated as:
	// {cluster}/machineconfigs/{TIMESTAMP}/{hostname}.yaml
	S3Destination S3Ref `json:"s3Destination"`

	// Lineage is the sealed causal chain record for this root declaration.
	// Authored once at object creation time and immutable thereafter.
	// seam-core-schema.md §5, CLAUDE.md §14 Decision 1.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// TalosMachineConfigBackupStatus defines the observed state of TalosMachineConfigBackup.
type TalosMachineConfigBackupStatus struct {
	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// JobName is the name of the most recently submitted Conductor executor Job.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// OperationResult is the message from the Conductor OperationResult ConfigMap.
	// +optional
	OperationResult string `json:"operationResult,omitempty"`

	// Conditions is the list of status conditions for this TalosMachineConfigBackup.
	// Condition types: Ready, Running, Degraded, S3DestinationAbsent, LineageSynced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// TalosMachineConfigBackup triggers a machine config backup for all nodes of a target
// cluster. The Conductor executor reads each node's running config via GetMachineConfig
// and uploads it to S3 at {cluster}/machineconfigs/{TIMESTAMP}/{hostname}.yaml.
// Named Conductor capability: machineconfig-backup. platform-schema.md §11.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=mcb
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type TalosMachineConfigBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TalosMachineConfigBackupSpec   `json:"spec,omitempty"`
	Status TalosMachineConfigBackupStatus `json:"status,omitempty"`
}

// TalosMachineConfigBackupList is the list type for TalosMachineConfigBackup.
//
// +kubebuilder:object:root=true
type TalosMachineConfigBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []TalosMachineConfigBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TalosMachineConfigBackup{}, &TalosMachineConfigBackupList{})
}
