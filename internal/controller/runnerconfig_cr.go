package controller

// RunnerConfig types are owned by seam-core (infrastructure.ontai.dev/v1alpha1).
// Platform reconcilers reference these aliases through the controller package.
// Replaces the previous AddKnownTypeWithName workaround for runner.ontai.dev/v1alpha1.
// T-2B-8.

import seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"

// Type aliases -- struct definitions are in seam-core. These preserve the
// controller package interface for all day-2 reconcilers without source edits.
type (
	OperationalRunnerConfig     = seamcorev1alpha1.InfrastructureRunnerConfig
	OperationalRunnerConfigList = seamcorev1alpha1.InfrastructureRunnerConfigList
	OperationalRunnerConfigSpec = seamcorev1alpha1.InfrastructureRunnerConfigSpec

	// OperationalStep is an alias for RunnerConfigStep.
	OperationalStep = seamcorev1alpha1.RunnerConfigStep

	// CapabilityEntry is an alias for RunnerCapabilityEntry.
	CapabilityEntry = seamcorev1alpha1.RunnerCapabilityEntry

	// OperationalRunnerConfigStatus is an alias for InfrastructureRunnerConfigStatus.
	OperationalRunnerConfigStatus = seamcorev1alpha1.InfrastructureRunnerConfigStatus
)
