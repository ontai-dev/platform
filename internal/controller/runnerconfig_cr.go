package controller

// OperationalRunnerConfig is the platform-internal Go representation of a
// RunnerConfig CR submitted by platform day-2 reconcilers.
//
// GVK: runner.ontai.dev/v1alpha1 RunnerConfig — matches the CRD owned by the
// conductor repository. Platform creates these objects to express multi-step
// operation intent. Conductor's execute mode is the sole step-sequencing
// authority; the owning reconciler watches Status.Phase for the terminal
// condition. conductor-schema.md §17.
//
// This type is intentionally a local platform representation rather than an
// import of conductor/pkg/runnerlib. It carries only the fields platform cares
// about (spec.steps, spec.maintenanceTargetNodes, spec.operatorLeaderNode, and
// the two terminal status fields). conductor-schema.md §13.

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// runnerConfigGVK and runnerConfigListGVK are the canonical GVKs for the
// RunnerConfig CRD owned by runner.ontai.dev. These explicit GVKs are used
// with AddKnownTypeWithName so the scheme maps *OperationalRunnerConfig to
// Kind="RunnerConfig" — not "OperationalRunnerConfig" (the Go struct name that
// AddKnownTypes would derive). Without this, production REST calls fail because
// the API server's REST mapper finds no resource for Kind=OperationalRunnerConfig.
var (
	runnerConfigGVK = schema.GroupVersionKind{
		Group:   "runner.ontai.dev",
		Version: "v1alpha1",
		Kind:    "RunnerConfig",
	}
	runnerConfigListGVK = schema.GroupVersionKind{
		Group:   "runner.ontai.dev",
		Version: "v1alpha1",
		Kind:    "RunnerConfigList",
	}
)

// OperationalRunnerConfigGVK is the GVK for RunnerConfig CRs created by
// platform. Matches the conductor-owned CRD. conductor-schema.md §17.
var OperationalRunnerConfigGVK = schema.GroupVersionKind{
	Group:   "runner.ontai.dev",
	Version: "v1alpha1",
	Kind:    "RunnerConfig",
}

var operationalRunnerConfigGV = schema.GroupVersion{
	Group:   "runner.ontai.dev",
	Version: "v1alpha1",
}

// AddOperationalRunnerConfigToScheme registers OperationalRunnerConfig and
// OperationalRunnerConfigList with the given scheme under the canonical
// runner.ontai.dev/v1alpha1 GVKs (Kind=RunnerConfig / Kind=RunnerConfigList).
//
// AddKnownTypeWithName is used instead of AddKnownTypes because AddKnownTypes
// derives Kind from the Go struct name ("OperationalRunnerConfig"), which does
// not match the CRD Kind ("RunnerConfig"). Using the wrong Kind causes the
// production REST mapper to return "no matches for kind OperationalRunnerConfig
// in version runner.ontai.dev/v1alpha1" when the operator tries to Get or
// Create RunnerConfig objects. Call this in both the manager init (production)
// and test scheme builders (unit tests).
func AddOperationalRunnerConfigToScheme(s *runtime.Scheme) error {
	s.AddKnownTypeWithName(runnerConfigGVK, &OperationalRunnerConfig{})
	s.AddKnownTypeWithName(runnerConfigListGVK, &OperationalRunnerConfigList{})
	metav1.AddToGroupVersion(s, operationalRunnerConfigGV)
	return nil
}

// OperationalRunnerConfig is the typed Go representation of a RunnerConfig CR.
//
// +kubebuilder:object:root=false
type OperationalRunnerConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              OperationalRunnerConfigSpec   `json:"spec,omitempty"`
	Status            OperationalRunnerConfigStatus `json:"status,omitempty"`
}

// OperationalRunnerConfigSpec declares the multi-step execution intent.
type OperationalRunnerConfigSpec struct {
	// ClusterRef is the name of the target cluster this operation governs.
	ClusterRef string `json:"clusterRef"`

	// RunnerImage is the Conductor executor image used for each step Job.
	RunnerImage string `json:"runnerImage"`

	// MaintenanceTargetNodes lists the nodes targeted by this operation.
	// The Conductor executor excludes these from step Job scheduling.
	// conductor-schema.md §13.
	// +optional
	MaintenanceTargetNodes []string `json:"maintenanceTargetNodes,omitempty"`

	// OperatorLeaderNode is the node running the platform operator leader pod.
	// The Conductor executor excludes this node from step Job scheduling.
	// conductor-schema.md §13.
	// +optional
	OperatorLeaderNode string `json:"operatorLeaderNode,omitempty"`

	// Steps is the ordered, multi-step execution sequence.
	// conductor-schema.md §17.
	Steps []OperationalStep `json:"steps"`
}

// OperationalStep declares one step in the execution sequence.
type OperationalStep struct {
	// Name is the unique step identifier within this RunnerConfig.
	Name string `json:"name"`

	// Capability is the named Conductor executor capability to invoke.
	Capability string `json:"capability"`

	// Parameters carries capability-specific key/value pairs passed to the
	// executor. conductor-schema.md §17.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`

	// DependsOn is the name of a prior step that must have reached
	// Succeeded before this step is dispatched.
	// +optional
	DependsOn string `json:"dependsOn,omitempty"`

	// HaltOnFailure instructs the sequencer to stop the entire sequence if
	// this step reaches the Failed phase.
	// +optional
	HaltOnFailure bool `json:"haltOnFailure,omitempty"`
}

// CapabilityEntry is a single entry in the Conductor capability manifest.
// Platform reads status.capabilities to gate Job submission. Only the Name
// field is consumed by platform; other fields are present for round-trip fidelity
// with the conductor-schema.md §5 CRD definition.
type CapabilityEntry struct {
	Name string `json:"name"`
}

// OperationalRunnerConfigStatus is written by the Conductor executor.
// The platform operator watches Phase for terminal conditions and reads
// Capabilities to gate day-2 Job submission. conductor-schema.md §17.
type OperationalRunnerConfigStatus struct {
	// Capabilities is the self-declared capability list published by the Conductor
	// agent on startup. Operators read this before submitting any day-2 Job.
	// conductor-schema.md §5, CR-INV-005.
	// +optional
	Capabilities []CapabilityEntry `json:"capabilities,omitempty"`

	// Phase is the terminal execution phase. Empty means in-progress.
	// "Completed" — all steps succeeded.
	// "Failed"    — at least one step failed (see FailedStep).
	// +optional
	Phase string `json:"phase,omitempty"`

	// FailedStep is the name of the first step that reached the Failed phase.
	// +optional
	FailedStep string `json:"failedStep,omitempty"`
}

// OperationalRunnerConfigList is the list type for OperationalRunnerConfig.
type OperationalRunnerConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OperationalRunnerConfig `json:"items"`
}

// DeepCopyObject implements runtime.Object for OperationalRunnerConfig.
func (rc *OperationalRunnerConfig) DeepCopyObject() runtime.Object {
	if rc == nil {
		return nil
	}
	out := *rc
	out.Status.Capabilities = nil
	if len(rc.Status.Capabilities) > 0 {
		out.Status.Capabilities = make([]CapabilityEntry, len(rc.Status.Capabilities))
		copy(out.Status.Capabilities, rc.Status.Capabilities)
	}
	out.Spec.MaintenanceTargetNodes = nil
	if len(rc.Spec.MaintenanceTargetNodes) > 0 {
		out.Spec.MaintenanceTargetNodes = make([]string, len(rc.Spec.MaintenanceTargetNodes))
		copy(out.Spec.MaintenanceTargetNodes, rc.Spec.MaintenanceTargetNodes)
	}
	out.Spec.Steps = nil
	if len(rc.Spec.Steps) > 0 {
		out.Spec.Steps = make([]OperationalStep, len(rc.Spec.Steps))
		for i, s := range rc.Spec.Steps {
			step := s
			if len(s.Parameters) > 0 {
				step.Parameters = make(map[string]string, len(s.Parameters))
				for k, v := range s.Parameters {
					step.Parameters[k] = v
				}
			}
			out.Spec.Steps[i] = step
		}
	}
	return &out
}

// DeepCopyObject implements runtime.Object for OperationalRunnerConfigList.
func (rcl *OperationalRunnerConfigList) DeepCopyObject() runtime.Object {
	if rcl == nil {
		return nil
	}
	out := *rcl
	out.Items = nil
	if len(rcl.Items) > 0 {
		out.Items = make([]OperationalRunnerConfig, len(rcl.Items))
		for i, item := range rcl.Items {
			out.Items[i] = *item.DeepCopyObject().(*OperationalRunnerConfig)
		}
	}
	return &out
}
