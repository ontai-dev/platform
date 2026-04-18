package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam-core/pkg/lineage"
)

// Condition type and reason constants for ClusterMaintenance.
const (
	// ConditionTypeClusterMaintenancePaused indicates the cluster is currently paused
	// (CAPI path: cluster.x-k8s.io/paused=true annotation set).
	ConditionTypeClusterMaintenancePaused = "Paused"

	// ConditionTypeClusterMaintenanceWindowActive indicates a maintenance window
	// is currently active.
	ConditionTypeClusterMaintenanceWindowActive = "WindowActive"

	// ReasonMaintenanceWindowOpen is set when an active maintenance window allows operations.
	ReasonMaintenanceWindowOpen = "MaintenanceWindowOpen"

	// ReasonMaintenanceWindowClosed is set when no maintenance window is active and
	// blockOutsideWindows=true is configured.
	ReasonMaintenanceWindowClosed = "MaintenanceWindowClosed"

	// ReasonCAPIPaused is set when the CAPI Cluster object has been paused by
	// setting cluster.x-k8s.io/paused=true.
	ReasonCAPIPaused = "CAPIPaused"

	// ReasonCAPIResumed is set when the CAPI Cluster pause annotation has been removed.
	ReasonCAPIResumed = "CAPIResumed"

	// ReasonConductorJobGateBlocked is set when the non-CAPI conductor Job admission
	// gate is blocking operations for this cluster.
	ReasonConductorJobGateBlocked = "ConductorJobGateBlocked"
)

// MaintenanceWindow defines a time window during which maintenance operations
// are permitted.
type MaintenanceWindow struct {
	// Name is an optional label for this window.
	// +optional
	Name string `json:"name,omitempty"`

	// Start is the window start time in cron format (e.g., "0 2 * * 6" for
	// 02:00 every Saturday UTC).
	Start string `json:"start"`

	// DurationMinutes is the length of the maintenance window in minutes.
	DurationMinutes int32 `json:"durationMinutes"`

	// Timezone is the IANA timezone for interpreting the cron schedule.
	// Defaults to UTC.
	// +optional
	Timezone string `json:"timezone,omitempty"`
}

// ClusterMaintenanceSpec defines the desired state of ClusterMaintenance.
type ClusterMaintenanceSpec struct {
	// ClusterRef references the TalosCluster this maintenance gate controls.
	ClusterRef LocalObjectRef `json:"clusterRef"`

	// Windows is the list of maintenance windows during which operations are permitted.
	// When empty, operations are always permitted (unless blockOutsideWindows=true
	// with no windows, in which case operations are always blocked).
	// +optional
	Windows []MaintenanceWindow `json:"windows,omitempty"`

	// BlockOutsideWindows controls whether operations are blocked when no active
	// window exists. When false (default), operations are permitted at any time.
	// When true and no active window exists:
	//   - CAPI path: sets cluster.x-k8s.io/paused=true on the CAPI Cluster, halting
	//     all CAPI reconciliation until the window opens.
	//   - Non-CAPI path: blocks Conductor Job admission for this cluster.
	// +optional
	BlockOutsideWindows bool `json:"blockOutsideWindows,omitempty"`

	// Lineage is the sealed causal chain record for this root declaration.
	// Authored once at object creation time and immutable thereafter.
	// seam-core-schema.md §5, CLAUDE.md §14 Decision 1.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// ClusterMaintenanceStatus defines the observed state of ClusterMaintenance.
type ClusterMaintenanceStatus struct {
	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ActiveWindowName is the name of the currently active maintenance window,
	// or empty if no window is currently active.
	// +optional
	ActiveWindowName string `json:"activeWindowName,omitempty"`

	// Conditions is the list of status conditions for this ClusterMaintenance.
	// Condition types: Paused, WindowActive, LineageSynced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ClusterMaintenance is a maintenance window gate for a Talos cluster.
//
// Dual-path CRD governed by spec.capi.enabled on the owning TalosCluster:
//   - For CAPI-managed clusters (capi.enabled=true): sets
//     cluster.x-k8s.io/paused=true on the CAPI Cluster when no active window
//     exists and blockOutsideWindows=true. Pause halts all CAPI reconciliation
//     until the window opens and the annotation is lifted.
//   - For management cluster (capi.enabled=false): blocks Conductor Job
//     admission for the cluster during restricted periods.
//
// platform-schema.md §5.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cmaint
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Paused",type=string,JSONPath=".status.conditions[?(@.type==\"Paused\")].status"
// +kubebuilder:printcolumn:name="WindowActive",type=string,JSONPath=".status.conditions[?(@.type==\"WindowActive\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type ClusterMaintenance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterMaintenanceSpec   `json:"spec,omitempty"`
	Status ClusterMaintenanceStatus `json:"status,omitempty"`
}

// ClusterMaintenanceList is the list type for ClusterMaintenance.
//
// +kubebuilder:object:root=true
type ClusterMaintenanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ClusterMaintenance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterMaintenance{}, &ClusterMaintenanceList{})
}
