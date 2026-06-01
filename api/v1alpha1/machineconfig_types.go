package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MachineConfigRole classifies a Talos node in MachineConfig CRs.
// Extends seam NodeRole with "init" for the first control-plane node.
// +kubebuilder:validation:Enum=init;controlplane;worker
type MachineConfigRole string

const (
	// MachineConfigRoleInit is the first control-plane node that initialises etcd.
	MachineConfigRoleInit MachineConfigRole = "init"

	// MachineConfigRoleControlPlane is a non-init control-plane node.
	MachineConfigRoleControlPlane MachineConfigRole = "controlplane"

	// MachineConfigRoleWorker is a worker node.
	MachineConfigRoleWorker MachineConfigRole = "worker"
)

// MachineConfigSpec declares the node configuration for a single Talos node.
// machine and cluster root sections mirror the Talos v1alpha1 config structure.
// These fields use unstructured JSON to remain Talos-version-agnostic.
type MachineConfigSpec struct {
	// Role is the Talos node role: init, controlplane, or worker.
	// +kubebuilder:validation:Enum=init;controlplane;worker
	Role MachineConfigRole `json:"role"`

	// Order controls upgrade sequencing. Nodes with lower Order values are
	// upgraded first. init node = 0, control-plane nodes = 1..N, workers = N+1..M.
	Order int32 `json:"order"`

	// ClusterRef references the TalosCluster this node belongs to.
	ClusterRef corev1.LocalObjectReference `json:"clusterRef"`

	// NodeIP is the primary IPv4 address of the node (Talos API port 50000).
	NodeIP string `json:"nodeIP"`

	// NodeHostname is the node hostname as declared in the cluster-input spec.
	NodeHostname string `json:"nodeHostname"`

	// Machine holds the Talos machine-level configuration as unstructured JSON.
	// Corresponds to the top-level "machine:" section of a Talos v1alpha1 config.
	// Version-agnostic: stored as raw JSON so it survives Talos API version bumps.
	// +optional
	Machine *apiextensionsv1.JSON `json:"machine,omitempty"`

	// Cluster holds the Talos cluster-level configuration as unstructured JSON.
	// Corresponds to the top-level "cluster:" section of a Talos v1alpha1 config.
	// +optional
	Cluster *apiextensionsv1.JSON `json:"cluster,omitempty"`
}

// MachineConfigStatus is the observed state of a MachineConfig.
type MachineConfigStatus struct {
	// ObservedGeneration is the spec generation most recently reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// SyncedVersion records the Talos version string at the time of the last
	// confirmed successful machineconfig apply (e.g., "v1.9.3").
	// +optional
	SyncedVersion string `json:"syncedVersion,omitempty"`

	// LastSyncedAt is the UTC timestamp of the last confirmed successful apply.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`

	// Conditions is the list of status conditions for this MachineConfig.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// MachineConfig is the first-class CRD for a Talos node configuration.
// Replaces machineconfig Kubernetes Secrets. Admin-authored (via compiler bootstrap
// or compiler addnode). Platform reads this CR via MachineConfigSync to drive
// Conductor machineconfig-sync Jobs. platform.ontai.dev/v1alpha1.
//
// Named convention: seam-mc-{cluster}-{bare-hostname}.
// Namespace: seam-tenant-{cluster}.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=mc
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=".spec.role"
// +kubebuilder:printcolumn:name="Order",type=integer,JSONPath=".spec.order"
// +kubebuilder:printcolumn:name="NodeIP",type=string,JSONPath=".spec.nodeIP"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type MachineConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MachineConfigSpec   `json:"spec,omitempty"`
	Status MachineConfigStatus `json:"status,omitempty"`
}

// MachineConfigList contains a list of MachineConfig.
//
// +kubebuilder:object:root=true
type MachineConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MachineConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MachineConfig{}, &MachineConfigList{})
}
