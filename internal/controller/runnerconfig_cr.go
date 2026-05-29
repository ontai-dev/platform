package controller

// RunnerConfig types are owned by seam-core (infrastructure.ontai.dev/v1alpha1).
// ClusterLog (OperationResult) is owned by platform (seam.ontai.dev/v1alpha1).
// Platform reconcilers reference these aliases through the controller package.
// T-2B-8, MIGRATION-3.2.

import (
	seamplatformv1alpha1 "github.com/ontai-dev/platform/api/seam/v1alpha1"
	seamcorev1alpha1 "github.com/ontai-dev/seam/api/v1alpha1"
)

// Type aliases -- struct definitions live in the owning packages. These preserve
// the controller package interface for all day-2 reconcilers without source edits.
type (
	OperationalRunnerConfig     = seamcorev1alpha1.RunnerConfig
	OperationalRunnerConfigList = seamcorev1alpha1.RunnerConfigList
	OperationalRunnerConfigSpec = seamcorev1alpha1.RunnerConfigSpec

	// OperationalStep is an alias for RunnerConfigStep.
	OperationalStep = seamcorev1alpha1.RunnerConfigStep

	// OperationalRunnerConfigStatus is an alias for RunnerConfigStatus.
	OperationalRunnerConfigStatus = seamcorev1alpha1.RunnerConfigStatus

	// TalosClusterOperationResult is the day-2 operation result CR (ClusterLog) written
	// by the Conductor execute-mode Job. One CR per cluster, in seam-tenant-{clusterRef}.
	TalosClusterOperationResult     = seamplatformv1alpha1.ClusterLog
	TalosClusterOperationResultList = seamplatformv1alpha1.ClusterLogList
)
