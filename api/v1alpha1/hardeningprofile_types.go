package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam-core/pkg/lineage"
)

// Condition type and reason constants for HardeningProfile.
const (
	// ConditionTypeHardeningProfileValid indicates the HardeningProfile spec is
	// structurally valid and ready to be referenced by NodeMaintenance.
	ConditionTypeHardeningProfileValid = "Valid"

	// ReasonHardeningProfileValid is set when all spec fields pass validation.
	ReasonHardeningProfileValid = "ProfileValid"

	// ReasonHardeningProfileInvalid is set when a spec field fails validation.
	ReasonHardeningProfileInvalid = "ProfileInvalid"
)

// HardeningProfileSpec defines the desired state of HardeningProfile.
type HardeningProfileSpec struct {
	// MachineConfigPatches is the list of Talos machine config patches to apply
	// when this profile is used. Patches are expressed as JSON Patch operations
	// applied to the rendered machineconfig. At compile time, Compiler merges
	// these patches into TalosConfigTemplate. At runtime, NodeMaintenance
	// operation=hardening-apply submits a Conductor Job to apply them.
	// +optional
	MachineConfigPatches []string `json:"machineConfigPatches,omitempty"`

	// SysctlParams is a map of sysctl key/value pairs to apply to target nodes.
	// Merged into the machineconfig sysctl section.
	// +optional
	SysctlParams map[string]string `json:"sysctlParams,omitempty"`

	// Description is a human-readable description of this hardening profile.
	// +optional
	Description string `json:"description,omitempty"`

	// Lineage is the sealed causal chain record for this root declaration.
	// Authored once at object creation time and immutable thereafter.
	// seam-core-schema.md §5, CLAUDE.md §14 Decision 1.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// HardeningProfileStatus defines the observed state of HardeningProfile.
type HardeningProfileStatus struct {
	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions is the list of status conditions for this HardeningProfile.
	// Condition types: Valid, LineageSynced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// HardeningProfile is a reusable hardening ruleset referenced by NodeMaintenance
// at runtime (operation=hardening-apply) and by TalosControlPlane and
// TalosWorkerConfig at compile time. It is a configuration CR only — it does
// not directly trigger a Conductor Job. Jobs are submitted by NodeMaintenance
// when it references this profile. platform-schema.md §5.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=hp
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type HardeningProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HardeningProfileSpec   `json:"spec,omitempty"`
	Status HardeningProfileStatus `json:"status,omitempty"`
}

// HardeningProfileList is the list type for HardeningProfile.
//
// +kubebuilder:object:root=true
type HardeningProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []HardeningProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HardeningProfile{}, &HardeningProfileList{})
}
