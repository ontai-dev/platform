package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam-core/pkg/lineage"
)

// EtcdMaintenanceOperation declares the etcd lifecycle operation to perform.
//
// +kubebuilder:validation:Enum=backup;restore;defrag
type EtcdMaintenanceOperation string

const (
	// EtcdMaintenanceOperationBackup performs an etcd snapshot backup to S3.
	EtcdMaintenanceOperationBackup EtcdMaintenanceOperation = "backup"

	// EtcdMaintenanceOperationRestore restores an etcd cluster from an S3 snapshot.
	EtcdMaintenanceOperationRestore EtcdMaintenanceOperation = "restore"

	// EtcdMaintenanceOperationDefrag runs etcd defragmentation on all members.
	EtcdMaintenanceOperationDefrag EtcdMaintenanceOperation = "defrag"
)

// Condition type and reason constants for EtcdMaintenance.
const (
	// ConditionTypeEtcdMaintenanceReady indicates the operation completed successfully.
	ConditionTypeEtcdMaintenanceReady = "Ready"

	// ConditionTypeEtcdMaintenanceRunning indicates the Conductor Job is running.
	ConditionTypeEtcdMaintenanceRunning = "Running"

	// ConditionTypeEtcdMaintenanceDegraded indicates the operation failed.
	ConditionTypeEtcdMaintenanceDegraded = "Degraded"

	// ReasonEtcdJobSubmitted is set when the Conductor executor Job has been submitted.
	ReasonEtcdJobSubmitted = "JobSubmitted"

	// ReasonEtcdJobComplete is set when the Conductor executor Job completed successfully.
	ReasonEtcdJobComplete = "JobComplete"

	// ReasonEtcdJobFailed is set when the Conductor executor Job failed. INV-018 applies.
	ReasonEtcdJobFailed = "JobFailed"

	// ReasonEtcdPendingApproval is not used for EtcdMaintenance (no approval gate).
	// ReasonEtcdOperationPending is set before the first Job submission.
	ReasonEtcdOperationPending = "Pending"

	// EtcdBackupDestinationAbsent indicates no S3 backup destination is configured.
	// Set when operation=backup and neither spec.etcdBackupS3SecretRef nor the
	// cluster-wide seam-etcd-backup-config Secret in seam-system is present.
	// platform-schema.md §10.
	EtcdBackupDestinationAbsent = "EtcdBackupDestinationAbsent"

	// EtcdBackupLocalFallback indicates PVC-based local backup is in use as a
	// degraded-mode fallback when S3 is unavailable. platform-schema.md §10.
	EtcdBackupLocalFallback = "EtcdBackupLocalFallback"

	// ReasonEtcdBackupDestinationAbsent is the reason when no S3 config is found.
	ReasonEtcdBackupDestinationAbsent = "S3DestinationAbsent"
)

// S3Ref references an S3 location for backup or restore.
type S3Ref struct {
	// Bucket is the S3 bucket name.
	Bucket string `json:"bucket"`

	// Key is the S3 object key path.
	Key string `json:"key"`

	// CredentialsSecretRef references the Secret containing S3 credentials.
	// The Secret must be in ont-system.
	CredentialsSecretRef SecretRef `json:"credentialsSecretRef"`
}

// EtcdMaintenanceSpec defines the desired state of EtcdMaintenance.
type EtcdMaintenanceSpec struct {
	// ClusterRef references the TalosCluster this operation targets.
	// The TalosCluster must exist in ont-system (management cluster) or in the
	// tenant namespace (target cluster).
	ClusterRef LocalObjectRef `json:"clusterRef"`

	// Operation declares the etcd lifecycle operation to perform.
	// Named Conductor capabilities: etcd-backup, etcd-restore, etcd-defrag.
	// platform-schema.md §5 EtcdMaintenance.
	// +kubebuilder:validation:Enum=backup;restore;defrag
	Operation EtcdMaintenanceOperation `json:"operation"`

	// EtcdBackupS3SecretRef references a Secret containing S3 backup destination
	// configuration for this operation. Takes precedence over the cluster-wide
	// seam-etcd-backup-config Secret in seam-system. Required when operation=backup
	// and no cluster default is configured. platform-schema.md §10.
	// +optional
	EtcdBackupS3SecretRef *corev1.SecretReference `json:"etcdBackupS3SecretRef,omitempty"`

	// S3Destination is the S3 location to write the etcd snapshot to.
	// Required when operation=backup.
	// +optional
	S3Destination *S3Ref `json:"s3Destination,omitempty"`

	// S3SnapshotPath is the S3 location of the snapshot to restore from.
	// Required when operation=restore.
	// +optional
	S3SnapshotPath *S3Ref `json:"s3SnapshotPath,omitempty"`

	// TargetNodes is the list of node names to target for restore operations.
	// When empty for restore, all etcd members are targeted.
	// +optional
	TargetNodes []string `json:"targetNodes,omitempty"`

	// PVCFallbackEnabled instructs the reconciler to set EtcdBackupLocalFallback
	// and proceed with RunnerConfig submission (no S3 params) when operation=backup
	// and no S3 destination is configured. The executor writes the snapshot to a
	// PVC mounted in the Job pod. platform-schema.md §10.
	// +optional
	PVCFallbackEnabled bool `json:"pvcFallbackEnabled,omitempty"`

	// Schedule is a cron expression for recurring backup operations.
	// When set with operation=backup, a recurring Job is submitted on schedule.
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// Lineage is the sealed causal chain record for this root declaration.
	// Authored once at object creation time and immutable thereafter.
	// seam-core-schema.md §5, CLAUDE.md §14 Decision 1.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// EtcdMaintenanceStatus defines the observed state of EtcdMaintenance.
type EtcdMaintenanceStatus struct {
	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// JobName is the name of the most recently submitted Conductor executor Job.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// OperationResult is the message from the Conductor OperationResult ConfigMap.
	// +optional
	OperationResult string `json:"operationResult,omitempty"`

	// Conditions is the list of status conditions for this EtcdMaintenance.
	// Condition types: Ready, Running, Degraded, LineageSynced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// EtcdMaintenance covers all etcd lifecycle operations for both management and
// target clusters. CAPI has no etcd concept — this always submits a direct
// Conductor executor Job regardless of the owning TalosCluster's capi.enabled.
// Named Conductor capabilities: etcd-backup, etcd-restore, etcd-defrag.
// platform-schema.md §5.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=em
// +kubebuilder:printcolumn:name="Operation",type=string,JSONPath=".spec.operation"
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type EtcdMaintenance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EtcdMaintenanceSpec   `json:"spec,omitempty"`
	Status EtcdMaintenanceStatus `json:"status,omitempty"`
}

// EtcdMaintenanceList is the list type for EtcdMaintenance.
//
// +kubebuilder:object:root=true
type EtcdMaintenanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []EtcdMaintenance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EtcdMaintenance{}, &EtcdMaintenanceList{})
}
