package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition type and reason constants for TalosMachineConfigBackupSchedule.
const (
	// ConditionTypeMCBScheduleActive indicates the schedule is active and will create backups.
	ConditionTypeMCBScheduleActive = "Active"

	// ReasonMCBScheduleNextRunPending is set while waiting for the next scheduled run.
	ReasonMCBScheduleNextRunPending = "NextRunPending"

	// ReasonMCBScheduleRunning is set while a backup CR is being created.
	ReasonMCBScheduleRunning = "Running"

	// ReasonMCBScheduleParseError is set when the schedule duration cannot be parsed.
	ReasonMCBScheduleParseError = "ParseError"
)

// TalosMachineConfigBackupScheduleSpec defines the desired state of TalosMachineConfigBackupSchedule.
type TalosMachineConfigBackupScheduleSpec struct {
	// ClusterRef references the TalosCluster to back up on schedule.
	ClusterRef LocalObjectRef `json:"clusterRef"`

	// Schedule is the backup interval as a Go duration string (e.g., "24h", "6h", "1h").
	// The reconciler creates a new TalosMachineConfigBackup CR each time the interval elapses.
	Schedule string `json:"schedule"`

	// S3Destination is the S3 location to write node machine configs to.
	// The bucket is required.
	S3Destination S3Ref `json:"s3Destination"`

	// S3BackupSecretRef references a Secret containing S3 backup credentials.
	// Falls back to seam-etcd-backup-config in seam-system when absent.
	// platform-schema.md §10.
	// +optional
	S3BackupSecretRef *corev1.SecretReference `json:"s3BackupSecretRef,omitempty"`
}

// TalosMachineConfigBackupScheduleStatus defines the observed state of TalosMachineConfigBackupSchedule.
type TalosMachineConfigBackupScheduleStatus struct {
	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// NextRunAt is the time the next backup CR will be created.
	// +optional
	NextRunAt *metav1.Time `json:"nextRunAt,omitempty"`

	// LastRunAt is the time the most recent backup CR was created.
	// +optional
	LastRunAt *metav1.Time `json:"lastRunAt,omitempty"`

	// LastBackupName is the name of the most recently created TalosMachineConfigBackup CR.
	// +optional
	LastBackupName string `json:"lastBackupName,omitempty"`

	// Conditions is the list of status conditions.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// TalosMachineConfigBackupSchedule creates TalosMachineConfigBackup CRs on a repeating
// interval. The schedule field accepts Go duration strings (e.g. "24h").
// platform-schema.md §11.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=mcbs
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=".spec.schedule"
// +kubebuilder:printcolumn:name="NextRun",type=date,JSONPath=".status.nextRunAt"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type TalosMachineConfigBackupSchedule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TalosMachineConfigBackupScheduleSpec   `json:"spec,omitempty"`
	Status TalosMachineConfigBackupScheduleStatus `json:"status,omitempty"`
}

// TalosMachineConfigBackupScheduleList is the list type.
//
// +kubebuilder:object:root=true
type TalosMachineConfigBackupScheduleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []TalosMachineConfigBackupSchedule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TalosMachineConfigBackupSchedule{}, &TalosMachineConfigBackupScheduleList{})
}
