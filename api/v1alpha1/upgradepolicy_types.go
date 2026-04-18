package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam-core/pkg/lineage"
)

// UpgradeType declares the type of upgrade to perform.
//
// +kubebuilder:validation:Enum=talos;kubernetes;stack
type UpgradeType string

const (
	// UpgradeTypeTalos upgrades the Talos OS version on all nodes.
	UpgradeTypeTalos UpgradeType = "talos"

	// UpgradeTypeKubernetes upgrades the Kubernetes version.
	UpgradeTypeKubernetes UpgradeType = "kubernetes"

	// UpgradeTypeStack upgrades both Talos and Kubernetes together.
	UpgradeTypeStack UpgradeType = "stack"
)

// RollingStrategy declares the rolling upgrade strategy.
//
// +kubebuilder:validation:Enum=sequential;parallel
type RollingStrategy string

const (
	// RollingStrategySequential upgrades nodes one at a time in order.
	RollingStrategySequential RollingStrategy = "sequential"

	// RollingStrategyParallel upgrades nodes concurrently (control plane first,
	// then workers in parallel within each pool).
	RollingStrategyParallel RollingStrategy = "parallel"
)

// Condition type and reason constants for UpgradePolicy.
const (
	// ConditionTypeUpgradePolicyReady indicates the upgrade completed successfully.
	ConditionTypeUpgradePolicyReady = "Ready"

	// ConditionTypeUpgradePolicyDegraded indicates the upgrade failed.
	ConditionTypeUpgradePolicyDegraded = "Degraded"

	// ConditionTypeUpgradePolicyCAPIDelegated indicates the upgrade has been
	// delegated to CAPI native machinery (capi.enabled=true path).
	ConditionTypeUpgradePolicyCAPIDelegated = "CAPIDelegated"

	// ReasonUpgradeJobSubmitted is set when the Conductor executor Job has been submitted.
	ReasonUpgradeJobSubmitted = "JobSubmitted"

	// ReasonUpgradeJobComplete is set when the Conductor executor Job completed successfully.
	ReasonUpgradeJobComplete = "JobComplete"

	// ReasonUpgradeJobFailed is set when the Conductor executor Job failed. INV-018 applies.
	ReasonUpgradeJobFailed = "JobFailed"

	// ReasonUpgradeCAPIDelegated is set when the upgrade is delegated to CAPI
	// native machinery for capi.enabled=true clusters.
	ReasonUpgradeCAPIDelegated = "CAPIDelegated"

	// ReasonUpgradeOperationPending is set before the first action.
	ReasonUpgradeOperationPending = "Pending"
)

// UpgradePolicySpec defines the desired state of UpgradePolicy.
type UpgradePolicySpec struct {
	// ClusterRef references the TalosCluster this upgrade targets.
	ClusterRef LocalObjectRef `json:"clusterRef"`

	// UpgradeType declares the type of upgrade to perform.
	// +kubebuilder:validation:Enum=talos;kubernetes;stack
	UpgradeType UpgradeType `json:"upgradeType"`

	// TargetTalosVersion is the target Talos version for talos and stack upgrades.
	// Required when upgradeType=talos or upgradeType=stack.
	// +optional
	TargetTalosVersion string `json:"targetTalosVersion,omitempty"`

	// TargetKubernetesVersion is the target Kubernetes version for kubernetes and
	// stack upgrades. Required when upgradeType=kubernetes or upgradeType=stack.
	// +optional
	TargetKubernetesVersion string `json:"targetKubernetesVersion,omitempty"`

	// RollingStrategy controls the order in which nodes are upgraded.
	// Defaults to sequential.
	// +optional
	// +kubebuilder:default=sequential
	RollingStrategy RollingStrategy `json:"rollingStrategy,omitempty"`

	// HealthGateConditions is a list of Kubernetes condition types that must be
	// True on each node before the upgrade proceeds to the next node. Used to
	// gate inter-node upgrade sequencing on cluster health.
	// +optional
	HealthGateConditions []string `json:"healthGateConditions,omitempty"`

	// Lineage is the sealed causal chain record for this root declaration.
	// Authored once at object creation time and immutable thereafter.
	// seam-core-schema.md §5, CLAUDE.md §14 Decision 1.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// UpgradePolicyStatus defines the observed state of UpgradePolicy.
type UpgradePolicyStatus struct {
	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// JobName is the name of the Conductor executor Job submitted for this upgrade.
	// Only set for the capi.enabled=false (non-CAPI) path.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// OperationResult is the message from the Conductor OperationResult ConfigMap.
	// +optional
	OperationResult string `json:"operationResult,omitempty"`

	// Conditions is the list of status conditions for this UpgradePolicy.
	// Condition types: Ready, Degraded, CAPIDelegated, LineageSynced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// UpgradePolicy governs Talos OS, Kubernetes, or combined stack upgrades.
//
// Dual-path CRD governed by spec.capi.enabled on the owning TalosCluster:
//   - For CAPI-managed clusters (capi.enabled=true): updates TalosControlPlane
//     version and MachineDeployment rolling upgrade settings natively through
//     CAPI machinery. No Conductor Job is submitted.
//   - For management cluster (capi.enabled=false): submits talos-upgrade,
//     kube-upgrade, or stack-upgrade Conductor executor Job.
//
// Named Conductor capabilities (non-CAPI path): talos-upgrade, kube-upgrade,
// stack-upgrade. platform-schema.md §5.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=upgp
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=".spec.upgradeType"
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type UpgradePolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   UpgradePolicySpec   `json:"spec,omitempty"`
	Status UpgradePolicyStatus `json:"status,omitempty"`
}

// UpgradePolicyList is the list type for UpgradePolicy.
//
// +kubebuilder:object:root=true
type UpgradePolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []UpgradePolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&UpgradePolicy{}, &UpgradePolicyList{})
}
