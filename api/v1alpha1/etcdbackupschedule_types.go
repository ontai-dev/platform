package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition type and reason constants for TalosEtcdBackupSchedule.
const (
	// ConditionTypeEtcdBackupScheduleActive indicates the schedule is active.
	ConditionTypeEtcdBackupScheduleActive = "Active"

	// ReasonEtcdBackupScheduleNextRunPending is set while waiting for the next run.
	ReasonEtcdBackupScheduleNextRunPending = "NextRunPending"

	// ReasonEtcdBackupScheduleRunning is set while an EtcdMaintenance CR is being created.
	ReasonEtcdBackupScheduleRunning = "Running"

	// ReasonEtcdBackupScheduleParseError is set when the schedule duration cannot be parsed.
	ReasonEtcdBackupScheduleParseError = "ParseError"
)

// TalosEtcdBackupScheduleSpec defines the desired state of TalosEtcdBackupSchedule.
type TalosEtcdBackupScheduleSpec struct {
	// ClusterRef references the TalosCluster to back up on schedule.
	ClusterRef LocalObjectRef `json:"clusterRef"`

	// Schedule is the backup interval as a Go duration string (e.g., "24h", "6h").
	// The reconciler creates a new EtcdMaintenance CR with operation=backup each time
	// the interval elapses.
	Schedule string `json:"schedule"`

	// S3Destination is the S3 location to write etcd snapshots to.
	S3Destination S3Ref `json:"s3Destination"`

	// EtcdBackupS3SecretRef references a Secret containing S3 backup credentials.
	// Falls back to seam-etcd-backup-config in seam-system when absent.
	// platform-schema.md §10.
	// +optional
	EtcdBackupS3SecretRef *corev1.SecretReference `json:"etcdBackupS3SecretRef,omitempty"`
}

// TalosEtcdBackupScheduleStatus defines the observed state of TalosEtcdBackupSchedule.
type TalosEtcdBackupScheduleStatus struct {
	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// NextRunAt is the time the next EtcdMaintenance CR will be created.
	// +optional
	NextRunAt *metav1.Time `json:"nextRunAt,omitempty"`

	// LastRunAt is the time the most recent EtcdMaintenance CR was created.
	// +optional
	LastRunAt *metav1.Time `json:"lastRunAt,omitempty"`

	// LastBackupName is the name of the most recently created EtcdMaintenance CR.
	// +optional
	LastBackupName string `json:"lastBackupName,omitempty"`

	// Conditions is the list of status conditions.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// TalosEtcdBackupSchedule creates EtcdMaintenance CRs with operation=backup on a
// repeating interval. The schedule field accepts Go duration strings (e.g. "24h").
// platform-schema.md §10.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=etcdbs
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=".spec.schedule"
// +kubebuilder:printcolumn:name="NextRun",type=date,JSONPath=".status.nextRunAt"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type TalosEtcdBackupSchedule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TalosEtcdBackupScheduleSpec   `json:"spec,omitempty"`
	Status TalosEtcdBackupScheduleStatus `json:"status,omitempty"`
}

// TalosEtcdBackupScheduleList is the list type.
//
// +kubebuilder:object:root=true
type TalosEtcdBackupScheduleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []TalosEtcdBackupSchedule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TalosEtcdBackupSchedule{}, &TalosEtcdBackupScheduleList{})
}
