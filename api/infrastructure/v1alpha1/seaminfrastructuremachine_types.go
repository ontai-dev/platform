package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam-core/pkg/lineage"
)

// NodeRole defines the role of a node in a Talos cluster.
//
// +kubebuilder:validation:Enum=controlplane;worker
type NodeRole string

const (
	// NodeRoleControlPlane designates control plane nodes.
	NodeRoleControlPlane NodeRole = "controlplane"

	// NodeRoleWorker designates worker nodes.
	NodeRoleWorker NodeRole = "worker"
)

// SecretRef is a reference to a Kubernetes Secret by name and namespace.
type SecretRef struct {
	// Name is the Secret name.
	Name string `json:"name"`

	// Namespace is the Secret namespace. Defaults to the object's own namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ProviderIDPrefix is the prefix for Talos provider IDs written to CAPI Machine
// spec.providerID. Format: talos://{cluster-name}/{node-ip}.
const ProviderIDPrefix = "talos://"

// Condition type and reason constants for SeamInfrastructureMachine.
const (
	// ConditionTypeMachineReady indicates the machine has been provisioned
	// and the node has exited maintenance mode.
	ConditionTypeMachineReady = "MachineReady"

	// ReasonBootstrapDataNotReady is set when the CAPI Machine has not yet
	// received its bootstrap data secret from CABPT.
	ReasonBootstrapDataNotReady = "BootstrapDataNotReady"

	// ReasonMachineConfigApplied is set after the machineconfig has been
	// successfully applied via the Talos maintenance API.
	ReasonMachineConfigApplied = "MachineConfigApplied"

	// ReasonMachineConfigFailed is set when applying the machineconfig fails.
	ReasonMachineConfigFailed = "MachineConfigFailed"

	// ReasonMachineOutOfMaintenance is set when the node has exited maintenance
	// mode and is reachable via its normal Talos API endpoint.
	ReasonMachineOutOfMaintenance = "MachineOutOfMaintenance"

	// ReasonMachineReady is set when the machine is fully provisioned and ready.
	ReasonMachineReady = "MachineReady"

	// ReasonCAPIMachineNotBound is set when no owning CAPI Machine has bound to
	// this SeamInfrastructureMachine yet.
	ReasonCAPIMachineNotBound = "CAPIMachineNotBound"

	// ConditionTypePortReachable indicates whether the Talos maintenance API port
	// (default 50000) on this node is reachable for machineconfig delivery.
	ConditionTypePortReachable = "PortReachable"

	// ReasonPortUnreachable is set when ApplyConfiguration fails because the node
	// cannot be reached on the maintenance API port.
	ReasonPortUnreachable = "PortUnreachable"
)

// SeamInfrastructureMachineSpec defines the desired state of SeamInfrastructureMachine.
type SeamInfrastructureMachineSpec struct {
	// Address is the pre-provisioned node IP address reachable on the Talos
	// maintenance API port (default 50000).
	Address string `json:"address"`

	// Port is the Talos maintenance API port on this node.
	// Defaults to 50000 if not specified.
	// +optional
	Port int32 `json:"port,omitempty"`

	// TalosConfigSecretRef references the talosconfig Secret in ont-system used
	// by the provider to authenticate calls to the node's normal Talos API
	// (post-maintenance). platform-schema.md §4.
	TalosConfigSecretRef SecretRef `json:"talosConfigSecretRef"`

	// NodeRole declares whether this machine is a control plane or worker node.
	// Must match the MachineDeployment role. Used by SeamInfrastructureCluster
	// to determine when all control plane machines are ready.
	NodeRole NodeRole `json:"nodeRole"`

	// Lineage is the sealed causal chain record for this root declaration.
	// Authored once at object creation time and immutable thereafter.
	// seam-core-schema.md §5, CLAUDE.md §14 Decision 1.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// SeamInfrastructureMachineStatus defines the observed state of SeamInfrastructureMachine.
type SeamInfrastructureMachineStatus struct {
	// Ready is true after the machineconfig has been applied and the node has
	// exited maintenance mode. Satisfies the CAPI InfrastructureMachine contract.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// MachineConfigApplied is true after ApplyConfiguration has been successfully
	// called against the Talos maintenance API on this node.
	// +optional
	MachineConfigApplied bool `json:"machineConfigApplied,omitempty"`

	// ApplyAttempts counts how many times ApplyConfiguration has been attempted
	// and failed consecutively. Resets to zero on success. Used by
	// TalosClusterReconciler to raise ControlPlaneUnreachable after 3 attempts.
	// +optional
	ApplyAttempts int32 `json:"applyAttempts,omitempty"`

	// ProviderID is the Talos provider ID written back to the owning CAPI Machine.
	// Format: talos://{cluster-name}/{node-ip}. Set after machineConfigApplied.
	// +optional
	ProviderID string `json:"providerID,omitempty"`

	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions is the list of status conditions for this SeamInfrastructureMachine.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// SeamInfrastructureMachine wraps a pre-provisioned Talos node IP address and
// its connection parameters. It implements the CAPI InfrastructureMachine contract.
// One instance per node in the cluster, in the tenant-{cluster-name} namespace.
// Declared by human or GitOps before bootstrapping. platform-schema.md §4.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=sim
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=".spec.nodeRole"
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=".spec.address"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type SeamInfrastructureMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SeamInfrastructureMachineSpec   `json:"spec,omitempty"`
	Status SeamInfrastructureMachineStatus `json:"status,omitempty"`
}

// SeamInfrastructureMachineList is the list type for SeamInfrastructureMachine.
//
// +kubebuilder:object:root=true
type SeamInfrastructureMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []SeamInfrastructureMachine `json:"items"`
}

// SeamInfrastructureMachineTemplateSpec defines the spec of SeamInfrastructureMachineTemplate.
type SeamInfrastructureMachineTemplateSpec struct {
	// Template is the machine template resource.
	Template SeamInfrastructureMachineTemplateResource `json:"template"`
}

// SeamInfrastructureMachineTemplateResource wraps SeamInfrastructureMachineSpec for
// use as a CAPI machine template. Follows the standard CAPI InfrastructureMachineTemplate
// pattern. platform-schema.md §2.2.
type SeamInfrastructureMachineTemplateResource struct {
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the SeamInfrastructureMachine spec to use as a template.
	Spec SeamInfrastructureMachineSpec `json:"spec"`
}

// SeamInfrastructureMachineTemplate is the template CRD for SeamInfrastructureMachine
// objects. CAPI's MachineDeployment controller uses this template to define the shape
// of worker node machines. platform-schema.md §2.2.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=simt
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type SeamInfrastructureMachineTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec SeamInfrastructureMachineTemplateSpec `json:"spec,omitempty"`
}

// SeamInfrastructureMachineTemplateList is the list type for SeamInfrastructureMachineTemplate.
//
// +kubebuilder:object:root=true
type SeamInfrastructureMachineTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []SeamInfrastructureMachineTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(
		&SeamInfrastructureMachine{}, &SeamInfrastructureMachineList{},
		&SeamInfrastructureMachineTemplate{}, &SeamInfrastructureMachineTemplateList{},
	)
}
