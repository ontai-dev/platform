package v1alpha1

// TalosCluster types are now owned by platform (seam.ontai.dev/v1alpha1).
// Platform reconcilers reference these aliases; all field types and constants resolve
// to the platform/api/seam/v1alpha1 definitions. MIGRATION-3.1.

import (
	seamv1alpha1 "github.com/ontai-dev/platform/api/seam/v1alpha1"
	"github.com/ontai-dev/seam/pkg/conditions"
)

// Type aliases -- struct definitions live in platform/api/seam/v1alpha1.
// These preserve the platformv1alpha1 package interface for all reconcilers without source edits.
// +kubebuilder:object:generate=false
type TalosCluster = seamv1alpha1.TalosCluster

// +kubebuilder:object:generate=false
type TalosClusterList = seamv1alpha1.TalosClusterList

// +kubebuilder:object:generate=false
type TalosClusterSpec = seamv1alpha1.TalosClusterSpec

// +kubebuilder:object:generate=false
type TalosClusterStatus = seamv1alpha1.TalosClusterStatus

// +kubebuilder:object:generate=false
type TalosClusterMode = seamv1alpha1.TalosClusterMode

// +kubebuilder:object:generate=false
type TalosClusterRole = seamv1alpha1.TalosClusterRole

// +kubebuilder:object:generate=false
type TalosClusterOrigin = seamv1alpha1.TalosClusterOrigin

// +kubebuilder:object:generate=false
type InfrastructureProvider = seamv1alpha1.InfrastructureProvider

// +kubebuilder:object:generate=false
type LocalObjectRef = seamv1alpha1.LocalObjectRef

// +kubebuilder:object:generate=false
type DeletionStage = seamv1alpha1.DeletionStage

// +kubebuilder:object:generate=false
type NodeRole = seamv1alpha1.NodeRole

// +kubebuilder:object:generate=false
type NodeAddress = seamv1alpha1.NodeAddress

// DeletionStage constants -- re-exported from platform/api/seam/v1alpha1. RECON-I1.
const (
	DeletionStageNone          = seamv1alpha1.DeletionStageNone
	DeletionStagePackExecution = seamv1alpha1.DeletionStagePackExecution
	DeletionStagePackInstalled = seamv1alpha1.DeletionStagePackInstalled
	DeletionStagePackDelivery  = seamv1alpha1.DeletionStagePackDelivery
	DeletionStageRunnerConfig  = seamv1alpha1.DeletionStageRunnerConfig
	DeletionStageComplete      = seamv1alpha1.DeletionStageComplete
)

// Mode constants.
const (
	TalosClusterModeBootstrap = seamv1alpha1.TalosClusterModeBootstrap
	TalosClusterModeImport    = seamv1alpha1.TalosClusterModeImport
)

// Role constants.
const (
	TalosClusterRoleManagement = seamv1alpha1.TalosClusterRoleManagement
	TalosClusterRoleTenant     = seamv1alpha1.TalosClusterRoleTenant
)

// Origin constants.
const (
	TalosClusterOriginBootstrapped = seamv1alpha1.TalosClusterOriginBootstrapped
	TalosClusterOriginImported     = seamv1alpha1.TalosClusterOriginImported
)

// InfrastructureProvider constants.
const (
	InfrastructureProviderNative = seamv1alpha1.InfrastructureProviderNative
	InfrastructureProviderScreen = seamv1alpha1.InfrastructureProviderScreen
)

// NodeRole constants -- re-exported from platform/api/seam/v1alpha1. RECON-A9.
const (
	NodeRoleControlPlane = seamv1alpha1.NodeRoleControlPlane
	NodeRoleWorker       = seamv1alpha1.NodeRoleWorker
)

// Condition type constants for TalosCluster -- re-exported from seam-core/pkg/conditions.
// Platform reconcilers reference these via the platformv1alpha1 alias; new code should
// import github.com/ontai-dev/seam/pkg/conditions directly.
const (
	ConditionTypeReady                        = conditions.ConditionTypeReady
	ConditionTypeBootstrapping                = conditions.ConditionTypeBootstrapping
	ConditionTypeBootstrapped                 = conditions.ConditionTypeBootstrapped
	ConditionTypeImporting                    = conditions.ConditionTypeImporting
	ConditionTypeDegraded                     = conditions.ConditionTypeDegraded
	ConditionTypeCiliumPending                = conditions.ConditionTypeCiliumPending
	ConditionTypeControlPlaneUnreachable      = conditions.ConditionTypeControlPlaneUnreachable
	ConditionTypePartialWorkerAvailability    = conditions.ConditionTypePartialWorkerAvailability
	ConditionTypeConductorReady               = conditions.ConditionTypeConductorReady
	ConditionTypeScreenProviderNotImplemented = conditions.ConditionTypeScreenProviderNotImplemented
	ConditionTypePhaseFailed                  = conditions.ConditionTypePhaseFailed
	ConditionTypeKubeconfigUnavailable        = conditions.ConditionTypeKubeconfigUnavailable
	ConditionTypeVersionUpgradePending        = conditions.ConditionTypeVersionUpgradePending
	ConditionTypeVersionRegressionBlocked     = conditions.ConditionTypeVersionRegressionBlocked
	ConditionTypeHardeningApplied             = conditions.ConditionTypeHardeningApplied
)

// Reason constants for TalosCluster -- re-exported from seam-core/pkg/conditions.
const (
	ReasonBootstrapJobSubmitted      = conditions.ReasonBootstrapJobSubmitted
	ReasonBootstrapJobComplete       = conditions.ReasonBootstrapJobComplete
	ReasonBootstrapJobFailed         = conditions.ReasonBootstrapJobFailed
	ReasonCiliumPackPending          = conditions.ReasonCiliumPackPending
	ReasonCiliumPackReady            = conditions.ReasonCiliumPackReady
	ReasonClusterReady               = conditions.ReasonClusterReady
	ReasonImportComplete             = conditions.ReasonImportComplete
	ReasonDegraded                   = conditions.ReasonDegraded
	ReasonControlPlaneNodeUnreachable = conditions.ReasonControlPlaneNodeUnreachable
	ReasonWorkerNodeUnreachable      = conditions.ReasonWorkerNodeUnreachable
	ReasonConductorBootstrapComplete = conditions.ReasonConductorBootstrapComplete
	ReasonConductorBootstrapPending  = conditions.ReasonConductorBootstrapPending
	ReasonScreenNotImplemented       = conditions.ReasonScreenNotImplemented
	ReasonTalosVersionRequired       = conditions.ReasonTalosVersionRequired
	ReasonTalosConfigSecretAbsent    = conditions.ReasonTalosConfigSecretAbsent
	ReasonVersionUpgradeRequested    = conditions.ReasonVersionUpgradeRequested
	ReasonVersionUpgradeSubmitted    = conditions.ReasonVersionUpgradeSubmitted
	ReasonVersionUpgradeComplete     = conditions.ReasonVersionUpgradeComplete
	ReasonVersionRegressionAttempted = conditions.ReasonVersionRegressionAttempted
	ReasonHardeningApplied           = conditions.ReasonHardeningApplied
	ReasonHardeningPending           = conditions.ReasonHardeningPending
	ReasonHardeningProfileNotValid   = conditions.ReasonHardeningProfileNotValid
)
