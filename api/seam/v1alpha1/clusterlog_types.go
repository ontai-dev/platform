package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResultStatus is the terminal status of a TalosCluster day-2 operation.
// +kubebuilder:validation:Enum=Succeeded;Failed
type ResultStatus string

const (
	ResultSucceeded ResultStatus = "Succeeded"
	ResultFailed    ResultStatus = "Failed"
)

// OperationFailureReason is a structured failure description for
// a day-2 operation that reached a terminal Failed state.
type OperationFailureReason struct {
	// Category classifies the failure domain.
	// +kubebuilder:validation:Enum=ValidationFailure;CapabilityUnavailable;ExecutionFailure;ExternalDependencyFailure;InvariantViolation
	Category string `json:"category"`

	// Reason is a human-readable description of the failure.
	Reason string `json:"reason"`
}

// OperationRecord is a single day-2 operation record within one
// talosVersion revision. Multiple records accumulate in the parent ClusterLog as
// operations are performed against the cluster.
type OperationRecord struct {
	// Capability is the conductor capability that produced this record.
	Capability string `json:"capability"`

	// JobRef is the Kubernetes Job name that produced this record.
	// The platform reconciler uses this to correlate the record with the Job it submitted.
	JobRef string `json:"jobRef"`

	// Status is the terminal status of the capability execution.
	// +kubebuilder:validation:Enum=Succeeded;Failed
	Status ResultStatus `json:"status"`

	// Message provides a human-readable summary of the outcome.
	// +optional
	Message string `json:"message,omitempty"`

	// StartedAt is the time the capability execution began.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is the time the capability execution finished.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// FailureReason is populated when Status is Failed. Nil on success.
	// +optional
	FailureReason *OperationFailureReason `json:"failureReason,omitempty"`
}

// ClusterLogSpec is the accumulated day-2 operation history for one cluster,
// scoped to the current talosVersion revision.
//
// One CR per cluster. Created by the platform operator when the cluster tenant
// namespace is provisioned. Named by the cluster name. Lives in seam-tenant-{clusterRef}.
//
// When the cluster talosVersion is upgraded, the current revision is archived to
// the GraphQuery DB and a new revision begins: Revision increments, TalosVersion
// is updated, and Operations is cleared.
//
// conductor-schema.md §8.
type ClusterLogSpec struct {
	// ClusterRef is the name of the TalosCluster this log accumulates.
	ClusterRef string `json:"clusterRef"`

	// TalosVersion is the cluster talosVersion for the current active revision.
	// Matches TalosCluster.spec.talosVersion at the time this revision began.
	TalosVersion string `json:"talosVersion"`

	// Revision is the monotonic revision counter. Starts at 1. Increments on each
	// talosVersion upgrade. Each revision holds the operations performed during that
	// version epoch. Archived revisions are stored in the GraphQuery DB.
	// +kubebuilder:default=1
	Revision int64 `json:"revision"`

	// Operations is the map of day-2 operation records for the current revision,
	// keyed by Kubernetes Job name. Map keying enables O(1) lookup by the platform
	// reconciler and clean serialization when archiving the revision to the GraphQuery DB.
	// +optional
	Operations map[string]OperationRecord `json:"operations,omitempty"`

	// OperationCount is the count of records in Operations for the current revision.
	// Maintained by the writer alongside Operations so kubectl can display it
	// as an integer column. Updated atomically with every Operations write.
	// json tag intentionally omits omitempty so the writer always serializes 0.
	// +optional
	OperationCount int64 `json:"operationCount"`
}

// ClusterLogStatus is the observed state.
// Currently empty; reserved for future conditions.
type ClusterLogStatus struct {
	// ObservedGeneration is the last generation observed by any consumer.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=clog
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterRef`
// +kubebuilder:printcolumn:name="TalosVersion",type=string,JSONPath=`.spec.talosVersion`
// +kubebuilder:printcolumn:name="Revision",type=integer,JSONPath=`.spec.revision`
// +kubebuilder:printcolumn:name="Ops",type=integer,JSONPath=`.spec.operationCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClusterLog accumulates the day-2 operation history for one cluster. One CR per
// cluster, created when the platform operator provisions the cluster tenant namespace.
// Operations are appended by the Conductor execute-mode Job. On talosVersion upgrade,
// the current revision is archived to the GraphQuery DB and a new revision epoch begins.
//
// Named by the cluster name. Lives in seam-tenant-{clusterRef}.
// conductor-schema.md §8.
type ClusterLog struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterLogSpec   `json:"spec,omitempty"`
	Status ClusterLogStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterLogList contains a list of ClusterLog.
type ClusterLogList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterLog `json:"items"`
}

func init() {
	SchemeBuilder.Register(
		&ClusterLog{},
		&ClusterLogList{},
	)
}
