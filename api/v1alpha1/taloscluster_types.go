package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam-core/pkg/lineage"
)

// TalosClusterMode declares whether this TalosCluster represents a bootstrap
// operation or an import of an existing cluster.
//
// +kubebuilder:validation:Enum=bootstrap;import
type TalosClusterMode string

const (
	// TalosClusterModeBootstrap provisions a new Talos cluster from scratch.
	// For management clusters (capi.enabled=false): submits a bootstrap Conductor Job.
	// For target clusters (capi.enabled=true): creates CAPI objects to drive CABPT and
	// the Seam Infrastructure Provider.
	TalosClusterModeBootstrap TalosClusterMode = "bootstrap"

	// TalosClusterModeImport records an existing Talos cluster managed outside
	// of Seam bootstrap. The cluster is adopted but not re-bootstrapped.
	TalosClusterModeImport TalosClusterMode = "import"
)

// InfrastructureProvider declares the infrastructure provider backing this
// TalosCluster. The value constrains the reconciliation path the operator takes.
//
// +kubebuilder:validation:Enum=native;capi;screen
type InfrastructureProvider string

const (
	// InfrastructureProviderNative is the default provider. The operator manages
	// cluster lifecycle directly: management cluster via a bootstrap Conductor Job
	// (capi.enabled=false), target clusters via the CAPI path (capi.enabled=true).
	// This is the standard path for all currently supported clusters.
	InfrastructureProviderNative InfrastructureProvider = "native"

	// InfrastructureProviderCAPI is an explicit alias for the CAPI-backed target
	// cluster path. Functionally equivalent to InfrastructureProviderNative when
	// spec.capi.enabled=true. Reserved for future explicit-provider semantics.
	InfrastructureProviderCAPI InfrastructureProvider = "capi"

	// InfrastructureProviderScreen is reserved for the future Screen operator
	// (KubeVirt workload lifecycle on VM-class clusters). INV-021: no implementation
	// work proceeds until a formal Architecture Decision Record is approved by the
	// Platform Governor. When observed, the reconciler sets
	// ScreenProviderNotImplemented=True and halts without reconciling.
	InfrastructureProviderScreen InfrastructureProvider = "screen"
)

// TalosClusterRole declares the role of this cluster in the Seam topology.
// Management clusters host the Seam control plane. Tenant clusters are governed
// by the management cluster. This field is required on the direct bootstrap path
// (capi.enabled=false) to determine namespace creation and kubeconfig routing.
// platform-schema.md §5. WS5.
//
// +kubebuilder:validation:Enum=management;tenant
type TalosClusterRole string

const (
	// TalosClusterRoleManagement declares this cluster as the Seam management cluster.
	// On the direct bootstrap path (capi.enabled=false): no seam-tenant namespace is
	// created; kubeconfig is written to seam-system only. There is exactly one
	// management cluster per Seam installation.
	TalosClusterRoleManagement TalosClusterRole = "management"

	// TalosClusterRoleTenant declares this cluster as a Seam target (tenant) cluster.
	// On the direct bootstrap path (capi.enabled=false, mode=import): a seam-tenant
	// namespace is created and the kubeconfig is copied there as target-cluster-kubeconfig
	// so that operators can locate it alongside other tenant-scoped resources.
	TalosClusterRoleTenant TalosClusterRole = "tenant"
)

// TalosClusterOrigin records how the cluster came to exist.
//
// +kubebuilder:validation:Enum=bootstrapped;imported
type TalosClusterOrigin string

const (
	// TalosClusterOriginBootstrapped indicates the cluster was created via the
	// bootstrap path (TalosClusterModeBootstrap).
	TalosClusterOriginBootstrapped TalosClusterOrigin = "bootstrapped"

	// TalosClusterOriginImported indicates the cluster was adopted via the
	// import path (TalosClusterModeImport).
	TalosClusterOriginImported TalosClusterOrigin = "imported"
)

// Condition type constants for TalosCluster.
const (
	// ConditionTypeReady indicates the cluster is fully operational.
	// For CAPI clusters: CAPI Cluster running AND Cilium PackInstance ready AND
	// all nodes Ready.
	// For management clusters: bootstrap Job complete, cluster API accessible.
	ConditionTypeReady = "Ready"

	// ConditionTypeBootstrapping indicates a bootstrap operation is in progress.
	// Set when a bootstrap Conductor Job has been submitted and not yet completed.
	ConditionTypeBootstrapping = "Bootstrapping"

	// ConditionTypeImporting indicates an import operation is in progress.
	ConditionTypeImporting = "Importing"

	// ConditionTypeDegraded indicates the cluster is in a degraded state that
	// requires operator attention.
	ConditionTypeDegraded = "Degraded"

	// ConditionTypeCiliumPending indicates the CAPI cluster has reached Running
	// state but the Cilium ClusterPack has not yet reached PackInstance.Ready.
	// Nodes are NotReady during this window. This is the expected state between
	// CAPI Running and Cilium Ready. platform-schema.md §5, CP-INV-013.
	ConditionTypeCiliumPending = "CiliumPending"

	// ConditionTypeControlPlaneUnreachable is set when one or more control plane
	// SeamInfrastructureMachine nodes cannot be reached on port 50000 after the
	// retry threshold. Reconciliation halts until the condition clears.
	ConditionTypeControlPlaneUnreachable = "ControlPlaneUnreachable"

	// ConditionTypePartialWorkerAvailability is set when one or more worker
	// SeamInfrastructureMachine nodes are unreachable on port 50000 after the
	// retry threshold. Reconciliation proceeds with available workers. The
	// condition clears automatically on the next successful reconcile.
	ConditionTypePartialWorkerAvailability = "PartialWorkerAvailability"

	// ConditionTypeConductorReady is set after the Conductor agent Deployment has
	// been created on the target cluster. True when the Deployment's Available
	// condition is True. False when the Deployment exists but is not yet Available.
	// The cluster does not transition to Ready until ConductorReady=True.
	// platform-schema.md §12. Gap 27.
	ConditionTypeConductorReady = "ConductorReady"

	// ConditionTypeScreenProviderNotImplemented is set when
	// spec.infrastructure.provider=screen is observed. Screen is a future operator
	// (INV-021). The reconciler preserves the field and stops without attempting
	// further reconciliation on the screen path. Clears when Screen is implemented.
	ConditionTypeScreenProviderNotImplemented = "ScreenProviderNotImplemented"

	// ConditionTypePhaseFailed is set when a required field is missing or invalid
	// and reconciliation cannot proceed. The reason encodes the specific cause.
	ConditionTypePhaseFailed = "PhaseFailed"

	// ConditionTypeKubeconfigUnavailable is set on the import path when a kubeconfig
	// Secret cannot be generated because a prerequisite resource is absent.
	// The reason encodes the specific cause. Clears when the kubeconfig Secret is
	// successfully written to seam-system.
	ConditionTypeKubeconfigUnavailable = "KubeconfigUnavailable"
)

// Condition reason constants for TalosCluster.
const (
	// ReasonBootstrapJobSubmitted is set when a bootstrap Conductor Job has been
	// submitted for a management cluster (capi.enabled=false).
	ReasonBootstrapJobSubmitted = "BootstrapJobSubmitted"

	// ReasonBootstrapJobComplete is set when the bootstrap Conductor Job has
	// completed successfully and the cluster API is accessible.
	ReasonBootstrapJobComplete = "BootstrapJobComplete"

	// ReasonBootstrapJobFailed is set when the bootstrap Conductor Job failed.
	ReasonBootstrapJobFailed = "BootstrapJobFailed"

	// ReasonCAPIObjectsCreated is set when all required CAPI objects (Cluster,
	// TalosControlPlane, MachineDeployments, SeamInfrastructureCluster) have
	// been created for a target cluster (capi.enabled=true).
	ReasonCAPIObjectsCreated = "CAPIObjectsCreated"

	// ReasonCAPIClusterRunning is set when the CAPI Cluster status.phase
	// transitions to Running. Triggers the Cilium ClusterPack deployment.
	ReasonCAPIClusterRunning = "CAPIClusterRunning"

	// ReasonCiliumPackPending is set when the CAPI cluster is Running but the
	// Cilium ClusterPack PackInstance has not yet reached Ready status.
	ReasonCiliumPackPending = "CiliumPackPending"

	// ReasonCiliumPackReady is set when the Cilium PackInstance has reached
	// Ready status and all nodes have transitioned to Ready.
	ReasonCiliumPackReady = "CiliumPackReady"

	// ReasonClusterReady is set when the cluster has reached the fully
	// operational Ready state.
	ReasonClusterReady = "ClusterReady"

	// ReasonImportComplete is set when an import operation has completed.
	ReasonImportComplete = "ImportComplete"

	// ReasonDegraded is set when a non-recoverable degraded condition is detected.
	ReasonDegraded = "Degraded"

	// ReasonControlPlaneNodeUnreachable is set on ControlPlaneUnreachable when one
	// or more control plane nodes cannot be reached on port 50000.
	ReasonControlPlaneNodeUnreachable = "ControlPlaneNodeUnreachable"

	// ReasonWorkerNodeUnreachable is set on PartialWorkerAvailability when one or
	// more worker nodes cannot be reached on port 50000.
	ReasonWorkerNodeUnreachable = "WorkerNodeUnreachable"

	// ReasonConductorDeploymentAvailable is set on ConductorReady when the Conductor
	// Deployment on the target cluster has reached Available=True.
	ReasonConductorDeploymentAvailable = "ConductorDeploymentAvailable"

	// ReasonConductorDeploymentUnavailable is set on ConductorReady when the Conductor
	// Deployment has been created but has not yet reached Available=True.
	// The reconciler requeues until Available=True.
	ReasonConductorDeploymentUnavailable = "ConductorDeploymentUnavailable"

	// ReasonScreenNotImplemented is set on ScreenProviderNotImplemented when
	// spec.infrastructure.provider=screen is observed. INV-021.
	ReasonScreenNotImplemented = "ScreenNotImplemented"

	// ReasonTalosVersionRequired is set on PhaseFailed when spec.talosVersion is
	// empty and a RunnerConfig cannot be created without a version-tagged conductor image.
	// conductor-schema.md §3, INV-012.
	ReasonTalosVersionRequired = "TalosVersionRequired"

	// ReasonTalosConfigSecretAbsent is set on KubeconfigUnavailable when the
	// seam-mc-{cluster-name}-talosconfig Secret is not found in seam-system.
	// The reconciler requeues after setting this condition. Clears when the Secret appears.
	ReasonTalosConfigSecretAbsent = "TalosConfigSecretAbsent"
)

// CAPICiliumPackRef is a reference to the cluster-specific Cilium ClusterPack
// that must be deployed immediately after the CAPI cluster reaches Running state.
// The pack is pre-compiled on the workstation and is cluster-endpoint-specific.
// platform-schema.md §2.3.
type CAPICiliumPackRef struct {
	// Name is the ClusterPack CR name for the Cilium pack.
	Name string `json:"name"`

	// Version is the ClusterPack version string.
	Version string `json:"version"`
}

// CAPIWorkerPool declares a worker node pool for a CAPI-managed target cluster.
// Each pool maps to a MachineDeployment + SeamInfrastructureMachineTemplate.
type CAPIWorkerPool struct {
	// Name is the pool identifier. Used as the MachineDeployment name suffix.
	Name string `json:"name"`

	// Replicas is the desired number of worker nodes in this pool.
	Replicas int32 `json:"replicas"`

	// SeamInfrastructureMachineNames lists the SeamInfrastructureMachine CR names
	// pre-provisioned for this pool. One per node. The Seam Infrastructure Provider
	// delivers machineconfigs to these nodes when CABPT renders their configs.
	// +optional
	SeamInfrastructureMachineNames []string `json:"seamInfrastructureMachineNames,omitempty"`
}

// CAPIControlPlaneConfig declares the control plane configuration for a CAPI
// target cluster.
type CAPIControlPlaneConfig struct {
	// Replicas is the desired number of control plane nodes.
	Replicas int32 `json:"replicas"`
}

// CAPIConfig holds the CAPI-specific configuration fields for a TalosCluster.
// These fields are only consulted when capi.enabled=true (target clusters).
// platform-schema.md §5.
type CAPIConfig struct {
	// Enabled determines whether this TalosCluster uses the CAPI path.
	// True for all target clusters. False for the management cluster.
	// When true, the TalosClusterReconciler creates and owns CAPI objects.
	// When false, it follows the direct bootstrap path via Conductor Job.
	Enabled bool `json:"enabled"`

	// TalosVersion is the Talos version to use for TalosConfigTemplate and
	// CABPT machineconfig generation. Required when Enabled=true.
	// +optional
	TalosVersion string `json:"talosVersion,omitempty"`

	// KubernetesVersion is the Kubernetes version for TalosControlPlane.
	// Required when Enabled=true.
	// +optional
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`

	// ControlPlane holds control plane configuration. Required when Enabled=true.
	// +optional
	ControlPlane CAPIControlPlaneConfig `json:"controlPlane,omitempty"`

	// Workers is the list of worker node pools. Each pool maps to a
	// MachineDeployment + SeamInfrastructureMachineTemplate.
	// +optional
	Workers []CAPIWorkerPool `json:"workers,omitempty"`

	// CiliumPackRef references the cluster-specific Cilium ClusterPack.
	// Applied as the first pack after the CAPI cluster reaches Running state.
	// Required when Enabled=true. platform-schema.md §2.3.
	// +optional
	CiliumPackRef *CAPICiliumPackRef `json:"ciliumPackRef,omitempty"`
}

// TalosClusterSpec defines the desired state of a TalosCluster.
type TalosClusterSpec struct {
	// Mode declares whether this TalosCluster bootstraps a new cluster or
	// imports an existing one. platform-schema.md §5.
	// +kubebuilder:validation:Enum=bootstrap;import
	Mode TalosClusterMode `json:"mode"`

	// TalosVersion is the Talos version running on this cluster (e.g. "v1.9.3").
	// Required for management clusters (capi.enabled=false): the conductor image is
	// derived from this field as {registry}/conductor:{talosVersion}-dev.
	// conductor-schema.md §3, INV-012.
	// +optional
	TalosVersion string `json:"talosVersion,omitempty"`

	// ClusterEndpoint is the cluster VIP or primary API endpoint IP used to
	// reach the control plane. Required on the import path when generating the
	// kubeconfig Secret from the talosconfig Secret. Optional for bootstrap mode
	// (endpoint is derived from the bootstrap Job output). platform-schema.md §5.
	// +optional
	ClusterEndpoint string `json:"clusterEndpoint,omitempty"`

	// NodeAddresses is the list of node IPs belonging to this cluster.
	// Used by DSNSReconciler to populate A records in the seam DNS zone.
	// Optional. platform-schema.md §5.
	// +optional
	NodeAddresses []string `json:"nodeAddresses,omitempty"`

	// CAPI holds the CAPI-specific configuration for target cluster lifecycle.
	// When CAPI.Enabled=false (management cluster), this field is ignored except
	// for the Enabled flag itself.
	// +optional
	CAPI CAPIConfig `json:"capi,omitempty"`

	// InfrastructureProvider declares the infrastructure provider backing this cluster.
	// Defaults to native when absent. The only reserved future value is screen (INV-021):
	// when set to screen the reconciler surfaces ScreenProviderNotImplemented and halts.
	// +kubebuilder:validation:Enum=native;capi;screen
	// +kubebuilder:default=native
	// +optional
	InfrastructureProvider InfrastructureProvider `json:"infrastructureProvider,omitempty"`

	// Role declares whether this TalosCluster is the Seam management cluster or a
	// tenant (target) cluster. Required on the direct bootstrap path (capi.enabled=false)
	// to determine namespace creation and kubeconfig routing. When absent, the
	// reconciler defaults to management behaviour (no seam-tenant namespace).
	// platform-schema.md §5. WS5.
	// +kubebuilder:validation:Enum=management;tenant
	// +optional
	Role TalosClusterRole `json:"role,omitempty"`

	// Lineage is the sealed causal chain record for this root declaration.
	// Authored once at object creation time and immutable thereafter.
	// The admission webhook rejects any update that modifies this field after creation.
	// seam-core-schema.md §5, CLAUDE.md §14 Decision 1.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// LocalObjectRef is a reference to a Kubernetes object by name and namespace.
type LocalObjectRef struct {
	// Name is the object name.
	Name string `json:"name"`

	// Namespace is the object namespace. May be empty for cluster-scoped objects.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// TalosClusterStatus defines the observed state of a TalosCluster.
type TalosClusterStatus struct {
	// ObservedGeneration is the generation of the TalosCluster spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Origin records how the cluster came to exist.
	// +optional
	Origin TalosClusterOrigin `json:"origin,omitempty"`

	// CAPIClusterRef is a reference to the owned CAPI Cluster object in the tenant
	// namespace. Only set for CAPI-managed clusters (capi.enabled=true).
	// +optional
	CAPIClusterRef *LocalObjectRef `json:"capiClusterRef,omitempty"`

	// Conditions is the list of status conditions for this TalosCluster.
	// Condition types: Ready, Bootstrapping, Importing, Degraded,
	// CiliumPending, LineageSynced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// TalosCluster is the Seam root CR for every cluster. For target clusters it
// owns all CAPI objects as children. For the management cluster it is the
// bootstrap record and operational anchor. platform-schema.md §5.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=tc
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=".spec.mode"
// +kubebuilder:printcolumn:name="CAPI",type=boolean,JSONPath=".spec.capi.enabled"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type TalosCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TalosClusterSpec   `json:"spec,omitempty"`
	Status TalosClusterStatus `json:"status,omitempty"`
}

// TalosClusterList is the list type for TalosCluster.
//
// +kubebuilder:object:root=true
type TalosClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []TalosCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TalosCluster{}, &TalosClusterList{})
}
