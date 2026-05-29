package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam/pkg/lineage"
)

// Condition type and reason constants for ClusterMaintenance.
const (
	// ConditionTypeClusterMaintenancePaused indicates the cluster is outside an active
	// maintenance window and Conductor Job admission is blocked.
	ConditionTypeClusterMaintenancePaused = "Paused"

	// ConditionTypeClusterMaintenanceWindowActive indicates a maintenance window
	// is currently active.
	ConditionTypeClusterMaintenanceWindowActive = "WindowActive"

	// ReasonMaintenanceWindowOpen is set when an active maintenance window allows operations.
	ReasonMaintenanceWindowOpen = "MaintenanceWindowOpen"

	// ReasonMaintenanceWindowClosed is set when no maintenance window is active and
	// blockOutsideWindows=true is configured.
	ReasonMaintenanceWindowClosed = "MaintenanceWindowClosed"

	// ReasonConductorJobGateBlocked is set when the conductor Job admission
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
	// When true and no active window exists: blocks Conductor Job admission for this cluster.
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
// Records gate state in status; Conductor Job admission is blocked outside
// active windows when blockOutsideWindows=true.
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
