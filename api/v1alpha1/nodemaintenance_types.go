package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ontai-dev/seam-core/pkg/lineage"
)

// NodeMaintenanceOperation declares the node-level operation to perform.
//
// +kubebuilder:validation:Enum=patch;hardening-apply;credential-rotate
type NodeMaintenanceOperation string

const (
	// NodeMaintenanceOperationPatch applies a Talos machine config patch to target nodes.
	NodeMaintenanceOperationPatch NodeMaintenanceOperation = "patch"

	// NodeMaintenanceOperationHardeningApply applies a HardeningProfile to target nodes.
	NodeMaintenanceOperationHardeningApply NodeMaintenanceOperation = "hardening-apply"

	// NodeMaintenanceOperationCredentialRotate rotates credentials on target nodes.
	NodeMaintenanceOperationCredentialRotate NodeMaintenanceOperation = "credential-rotate"
)

// Condition type and reason constants for NodeMaintenance.
const (
	// ConditionTypeNodeMaintenanceReady indicates the operation completed successfully.
	ConditionTypeNodeMaintenanceReady = "Ready"

	// ConditionTypeNodeMaintenanceDegraded indicates the operation failed.
	ConditionTypeNodeMaintenanceDegraded = "Degraded"

	// ReasonNodeJobSubmitted is set when the Conductor executor Job has been submitted.
	ReasonNodeJobSubmitted = "JobSubmitted"

	// ReasonNodeJobComplete is set when the Conductor executor Job completed successfully.
	ReasonNodeJobComplete = "JobComplete"

	// ReasonNodeJobFailed is set when the Conductor executor Job failed. INV-018 applies.
	ReasonNodeJobFailed = "JobFailed"

	// ReasonNodeOperationPending is set before the first Job submission.
	ReasonNodeOperationPending = "Pending"
)

// NodeMaintenanceSpec defines the desired state of NodeMaintenance.
type NodeMaintenanceSpec struct {
	// ClusterRef references the TalosCluster this operation targets.
	ClusterRef LocalObjectRef `json:"clusterRef"`

	// Operation declares the node-level operation to perform.
	// Named Conductor capabilities: node-patch, hardening-apply, credential-rotate.
	// platform-schema.md §5 NodeMaintenance.
	// +kubebuilder:validation:Enum=patch;hardening-apply;credential-rotate
	Operation NodeMaintenanceOperation `json:"operation"`

	// TargetNodes is the list of node names or IPs to target.
	// When empty, the operation targets all nodes in the cluster.
	// +optional
	TargetNodes []string `json:"targetNodes,omitempty"`

	// PatchSecretRef references the Secret containing the machine config patch YAML.
	// Required when operation=patch.
	// +optional
	PatchSecretRef *SecretRef `json:"patchSecretRef,omitempty"`

	// HardeningProfileRef references the HardeningProfile CR to apply.
	// Required when operation=hardening-apply.
	// +optional
	HardeningProfileRef *LocalObjectRef `json:"hardeningProfileRef,omitempty"`

	// RotateServiceAccountKeys controls whether service account signing keys are
	// rotated. Applies when operation=credential-rotate.
	// +optional
	RotateServiceAccountKeys bool `json:"rotateServiceAccountKeys,omitempty"`

	// RotateOIDCCredentials controls whether OIDC credentials are rotated.
	// Applies when operation=credential-rotate.
	// +optional
	RotateOIDCCredentials bool `json:"rotateOIDCCredentials,omitempty"`

	// Lineage is the sealed causal chain record for this root declaration.
	// Authored once at object creation time and immutable thereafter.
	// seam-core-schema.md §5, CLAUDE.md §14 Decision 1.
	// +optional
	Lineage *lineage.SealedCausalChain `json:"lineage,omitempty"`
}

// NodeMaintenanceStatus defines the observed state of NodeMaintenance.
type NodeMaintenanceStatus struct {
	// ObservedGeneration is the generation of the spec last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// JobName is the name of the most recently submitted Conductor executor Job.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// OperationResult is the message from the Conductor OperationResult ConfigMap.
	// +optional
	OperationResult string `json:"operationResult,omitempty"`

	// Conditions is the list of status conditions for this NodeMaintenance.
	// Condition types: Ready, Degraded, LineageSynced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// NodeMaintenance covers targeted node-level operations CAPI has no equivalent
// for. Applies to both management and target clusters via a direct Conductor
// executor Job regardless of the owning TalosCluster's capi.enabled value.
// Named Conductor capabilities: node-patch, hardening-apply, credential-rotate.
// platform-schema.md §5.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nm
// +kubebuilder:printcolumn:name="Operation",type=string,JSONPath=".spec.operation"
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type NodeMaintenance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeMaintenanceSpec   `json:"spec,omitempty"`
	Status NodeMaintenanceStatus `json:"status,omitempty"`
}

// NodeMaintenanceList is the list type for NodeMaintenance.
//
// +kubebuilder:object:root=true
type NodeMaintenanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []NodeMaintenance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeMaintenance{}, &NodeMaintenanceList{})
}
