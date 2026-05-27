package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam/pkg/lineage"
)

// TalosCluster health and intervention condition type constants.
// Written by ClusterNodeHealthLoop in conductor agent mode.
const (
	// ConditionTypeNodeHealthSummary is True when all nodes are Ready.
	// False when any node is Degraded or Unreachable.
	// Written by conductor ClusterNodeHealthLoop. RECON-B1.
	ConditionTypeNodeHealthSummary = "NodeHealthSummary"

	// ConditionTypeHumanInterventionRequired is True when the cluster has entered a state
	// that conductor cannot resolve autonomously regardless of AutonomyLevel.
	// Examples: control plane quorum loss, multiple nodes simultaneously degraded.
	// Written by conductor ClusterNodeHealthLoop. RECON-B3 Tier 3.
	ConditionTypeHumanInterventionRequired = "HumanInterventionRequired"

	// ConditionTypeCapacitySaturation is True when any node exceeds the CPU or memory
	// utilisation threshold for the configured consecutive check window.
	// Written by conductor ClusterNodeHealthLoop. RECON-C6.
	ConditionTypeCapacitySaturation = "CapacitySaturation"

	// ConditionTypeDiskPressure is True when any node's ephemeral or STATE partition
	// exceeds the critical disk usage threshold. Written by conductor ClusterNodeHealthLoop. RECON-C7.
	ConditionTypeDiskPressure = "DiskPressure"
)

// Reason constants for health-related TalosCluster conditions.
const (
	ReasonAllNodesReady            = "AllNodesReady"
	ReasonNodesDegraded            = "NodesDegraded"
	ReasonNodesUnreachable         = "NodesUnreachable"
	ReasonControlPlaneQuorumAtRisk = "ControlPlaneQuorumAtRisk"
	ReasonHumanInterventionNeeded  = "HumanInterventionNeeded"
	ReasonPKIExpiryApproaching     = "PKIExpiryApproaching"
)

// NodeHealthAnnotation is the TalosCluster annotation key for the per-node JSON health summary.
// Written by ClusterNodeHealthLoop. Format: {"nodes":[{"name":"...","ip":"...","state":"..."}]}.
const NodeHealthAnnotation = "platform.ontai.dev/node-health-summary"

// TalosClusterMode declares whether the cluster is bootstrapped or imported.
// +kubebuilder:validation:Enum=bootstrap;import
type TalosClusterMode string

const (
	TalosClusterModeBootstrap TalosClusterMode = "bootstrap"
	TalosClusterModeImport    TalosClusterMode = "import"
)

// TalosClusterRole declares the role of the cluster in the Seam topology.
// Mandatory on mode=import.
// +kubebuilder:validation:Enum=management;tenant
type TalosClusterRole string

const (
	TalosClusterRoleManagement TalosClusterRole = "management"
	TalosClusterRoleTenant     TalosClusterRole = "tenant"
)

// TalosClusterOrigin records how the cluster came to exist.
// +kubebuilder:validation:Enum=bootstrapped;imported
type TalosClusterOrigin string

const (
	TalosClusterOriginBootstrapped TalosClusterOrigin = "bootstrapped"
	TalosClusterOriginImported     TalosClusterOrigin = "imported"
)

// InfrastructureProvider declares the infrastructure provider backing a TalosCluster.
// +kubebuilder:validation:Enum=native;capi;screen
type InfrastructureProvider string

const (
	// InfrastructureProviderNative is the default provider.
	InfrastructureProviderNative InfrastructureProvider = "native"

	// InfrastructureProviderCAPI is an explicit alias for the CAPI-backed path.
	InfrastructureProviderCAPI InfrastructureProvider = "capi"

	// InfrastructureProviderScreen is reserved for the future Screen operator (INV-021).
	InfrastructureProviderScreen InfrastructureProvider = "screen"
)

// LocalObjectRef is a reference to a Kubernetes object by name and namespace.
type LocalObjectRef struct {
	// Name is the object name.
	Name string `json:"name"`

	// Namespace is the object namespace. May be empty for cluster-scoped objects.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// CAPICiliumPackRef is a reference to the cluster-specific Cilium PackDelivery.
// platform-schema.md §2.3.
type CAPICiliumPackRef struct {
	// Name is the PackDelivery CR name for the Cilium pack.
	Name string `json:"name"`

	// Version is the PackDelivery version string.
	Version string `json:"version"`
}

// CAPIWorkerPool declares a worker node pool for a CAPI-managed target cluster.
type CAPIWorkerPool struct {
	// Name is the pool identifier. Used as the MachineDeployment name suffix.
	Name string `json:"name"`

	// Replicas is the desired number of worker nodes in this pool.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// SeamInfrastructureMachineNames lists the SeamInfrastructureMachine CR names
	// pre-provisioned for this pool. One per node.
	// +optional
	SeamInfrastructureMachineNames []string `json:"seamInfrastructureMachineNames,omitempty"`
}

// CAPIControlPlaneConfig declares the control plane configuration for a CAPI target cluster.
type CAPIControlPlaneConfig struct {
	// Replicas is the desired number of control plane nodes.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`
}

// CAPIConfig holds CAPI integration settings for a target cluster.
// Only consulted when capi.enabled=true. platform-schema.md §5.
type CAPIConfig struct {
	// Enabled determines whether this TalosCluster uses the CAPI path.
	Enabled bool `json:"enabled"`

	// TalosVersion is the Talos version to use for TalosConfigTemplate generation.
	// +optional
	TalosVersion string `json:"talosVersion,omitempty"`

	// KubernetesVersion is the Kubernetes version for TalosControlPlane.
	// +optional
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`

	// ControlPlane holds control plane configuration. Required when Enabled=true.
	// +optional
	ControlPlane *CAPIControlPlaneConfig `json:"controlPlane,omitempty"`

	// Workers is the list of worker node pools.
	// +optional
	Workers []CAPIWorkerPool `json:"workers,omitempty"`

	// CiliumPackRef references the cluster-specific Cilium PackDelivery.
	// +optional
	CiliumPackRef *CAPICiliumPackRef `json:"ciliumPackRef,omitempty"`
}

// TalosClusterSpec is the declared desired state of a TalosCluster.
// platform-schema.md §4.
// +kubebuilder:validation:XValidation:rule="self.mode != 'import' || (has(self.role) && self.role != '')",message="role is required when mode is import"
type TalosClusterSpec struct {
	// Mode declares whether this cluster is bootstrapped from scratch or imported.
	// +kubebuilder:validation:Enum=bootstrap;import
	Mode TalosClusterMode `json:"mode"`

	// Role declares the cluster role in the Seam topology. Mandatory on mode=import.
	// +kubebuilder:validation:Enum=management;tenant
	// +optional
	Role TalosClusterRole `json:"role,omitempty"`

	// TalosVersion is the Talos OS version for this cluster. INV-012.
	// +optional
	TalosVersion string `json:"talosVersion,omitempty"`

	// KubernetesVersion is the Kubernetes version for this cluster. When
	// spec.versionUpgrade=true, setting this field drives an UpgradeTypeKubernetes
	// UpgradePolicy. Setting both talosVersion and kubernetesVersion drives an
	// UpgradeTypeStack policy (sequential Talos then Kubernetes upgrade).
	// +optional
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`

	// VersionUpgrade, when set to true, triggers a cluster-level rolling upgrade.
	// Upgrade type is derived from which version fields are set:
	//   - talosVersion only: UpgradeTypeTalos
	//   - kubernetesVersion only: UpgradeTypeKubernetes
	//   - both: UpgradeTypeStack (sequential Talos then k8s)
	// +optional
	VersionUpgrade bool `json:"versionUpgrade,omitempty"`

	// ClusterEndpoint is the cluster VIP or primary API endpoint IP.
	// +optional
	ClusterEndpoint string `json:"clusterEndpoint,omitempty"`

	// NodeAddresses is the list of node IPs for DSNSReconciler A-record population.
	// +optional
	NodeAddresses []string `json:"nodeAddresses,omitempty"`

	// CAPI holds CAPI integration settings. When absent, direct bootstrap is used.
	// +optional
	CAPI *CAPIConfig `json:"capi,omitempty"`

	// InfrastructureProvider declares the infrastructure provider backing this cluster.
	// +kubebuilder:validation:Enum=native;capi;screen
	// +kubebuilder:default=native
	// +optional
	InfrastructureProvider InfrastructureProvider `json:"infrastructureProvider,omitempty"`

	// KubeconfigSecretRef is the name of the Secret containing the kubeconfig.
	// Required on mode=import. Not used when CAPI manages the lifecycle.
	// +optional
	KubeconfigSecretRef string `json:"kubeconfigSecretRef,omitempty"`

	// TalosconfigSecretRef is the name of the Secret containing the talosconfig.
	// +optional
	TalosconfigSecretRef string `json:"talosconfigSecretRef,omitempty"`

	// Lineage is the sealed causal chain record. Immutable after creation.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`

	// PkiRotationThresholdDays is the days before cert expiry at which a PKIRotation
	// CR is auto-created. Default 30. platform-schema.md §13.
	// +optional
	// +kubebuilder:default=30
	// +kubebuilder:validation:Minimum=1
	PkiRotationThresholdDays int32 `json:"pkiRotationThresholdDays,omitempty"`

	// HardeningProfileRef references a HardeningProfile CR to apply at bootstrap.
	// platform-schema.md §11.
	// +optional
	HardeningProfileRef *LocalObjectRef `json:"hardeningProfileRef,omitempty"`
}

// DeletionStage is the current step in the TalosCluster deletion cascade.
// Written to status before each step so that a reconciler restart can resume
// from the correct step rather than re-attempting already-completed deletes.
// RECON-I1.
//
// +kubebuilder:validation:Enum="";pack-execution;pack-installed;pack-delivery;runner-config;complete
type DeletionStage string

const (
	// DeletionStageNone is the zero value (no deletion in progress).
	DeletionStageNone DeletionStage = ""
	// DeletionStagePackExecution indicates the cascade is deleting PackExecutions.
	DeletionStagePackExecution DeletionStage = "pack-execution"
	// DeletionStagePackInstalled indicates the cascade is deleting PackInstalled CRs.
	DeletionStagePackInstalled DeletionStage = "pack-installed"
	// DeletionStagePackDelivery indicates the cascade is deleting PackDelivery CRs.
	DeletionStagePackDelivery DeletionStage = "pack-delivery"
	// DeletionStageRunnerConfig indicates the cascade is deleting the RunnerConfig.
	DeletionStageRunnerConfig DeletionStage = "runner-config"
	// DeletionStageComplete indicates all cascade steps completed and the finalizer
	// is being removed. After this stage the TalosCluster CR is released.
	DeletionStageComplete DeletionStage = "complete"
)

// TalosClusterStatus is the observed state of a TalosCluster.
type TalosClusterStatus struct {
	// ObservedGeneration is the generation most recently reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Origin records how this cluster came under Seam governance.
	// +optional
	Origin TalosClusterOrigin `json:"origin,omitempty"`

	// ObservedTalosVersion is the Talos version last confirmed running.
	// +optional
	ObservedTalosVersion string `json:"observedTalosVersion,omitempty"`

	// CAPIClusterRef is a reference to the owned CAPI Cluster object.
	// Only set for CAPI-managed clusters (capi.enabled=true).
	// +optional
	CAPIClusterRef *LocalObjectRef `json:"capiClusterRef,omitempty"`

	// Conditions is the list of status conditions for this TalosCluster.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// PkiExpiryDate is the earliest certificate expiry across the talosconfig and
	// kubeconfig Secrets. Set by the TalosCluster reconciler. platform-schema.md §13.
	// +optional
	PkiExpiryDate *metav1.Time `json:"pkiExpiryDate,omitempty"`

	// DeletionStage is the current step in the deletion cascade. Written before
	// each step so the reconciler can resume from the correct step after a restart.
	// Empty when no deletion is in progress. RECON-I1.
	// +optional
	DeletionStage DeletionStage `json:"deletionStage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=tc
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=".spec.mode"
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=".spec.role"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// TalosCluster is the platform CRD for a Talos cluster under Seam governance.
// platform-schema.md §4. Decision H. seam.ontai.dev/v1alpha1.
type TalosCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TalosClusterSpec   `json:"spec,omitempty"`
	Status TalosClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TalosClusterList contains a list of TalosCluster.
type TalosClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TalosCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TalosCluster{}, &TalosClusterList{})
}
