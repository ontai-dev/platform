package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam-core/pkg/lineage"
)

// Condition type constants for SeamInfrastructureCluster.
const (
	// ConditionTypeInfrastructureReady indicates the infrastructure cluster is ready:
	// all control plane SeamInfrastructureMachine objects have status.ready=true.
	ConditionTypeInfrastructureReady = "InfrastructureReady"

	// ReasonAllControlPlaneMachinesReady is set when all CP machines are ready.
	ReasonAllControlPlaneMachinesReady = "AllControlPlaneMachinesReady"

	// ReasonControlPlaneMachinesNotReady is set when at least one CP machine is not ready.
	ReasonControlPlaneMachinesNotReady = "ControlPlaneMachinesNotReady"

	// ReasonControlPlaneMachinesPending is set when no CP machines exist yet.
	ReasonControlPlaneMachinesPending = "ControlPlaneMachinesPending"
)

// APIEndpoint defines a Kubernetes API server endpoint.
type APIEndpoint struct {
	// Host is the VIP or first control plane IP.
	Host string `json:"host"`

	// Port is the Kubernetes API server port. Defaults to 6443.
	// +optional
	Port int32 `json:"port,omitempty"`
}

// SeamInfrastructureClusterSpec defines the desired state of SeamInfrastructureCluster.
type SeamInfrastructureClusterSpec struct {
	// ControlPlaneEndpoint is the API server endpoint for this cluster.
	// Written into the CAPI Cluster object and into all generated machineconfigs
	// via CABPT. platform-schema.md §4.
	// +optional
	ControlPlaneEndpoint APIEndpoint `json:"controlPlaneEndpoint,omitempty"`

	// Lineage is the sealed causal chain record for this root declaration.
	// Authored once at object creation time and immutable thereafter.
	// seam-core-schema.md §5, CLAUDE.md §14 Decision 1.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// SeamInfrastructureClusterStatus defines the observed state of SeamInfrastructureCluster.
type SeamInfrastructureClusterStatus struct {
	// Ready is true when all control plane SeamInfrastructureMachine objects
	// have status.ready=true. Satisfies the CAPI InfrastructureCluster contract.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions is the list of status conditions for this SeamInfrastructureCluster.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// SeamInfrastructureCluster is the cluster-level CAPI infrastructure reference for
// a Seam-managed Talos cluster. It implements the CAPI InfrastructureCluster contract.
// One instance per target cluster, in the tenant-{cluster-name} namespace.
// Owned by the CAPI Cluster object. platform-schema.md §4.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=sic
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=".spec.controlPlaneEndpoint.host"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type SeamInfrastructureCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SeamInfrastructureClusterSpec   `json:"spec,omitempty"`
	Status SeamInfrastructureClusterStatus `json:"status,omitempty"`
}

// SeamInfrastructureClusterList is the list type for SeamInfrastructureCluster.
//
// +kubebuilder:object:root=true
type SeamInfrastructureClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []SeamInfrastructureCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SeamInfrastructureCluster{}, &SeamInfrastructureClusterList{})
}
