package v1alpha1

// TalosCluster types are owned by seam-core (infrastructure.ontai.dev/v1alpha1).
// Platform reconcilers reference these aliases; all field types and constants resolve
// to the seam-core definitions. T-2B-8.

import (
	seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"
	"github.com/ontai-dev/seam-core/pkg/conditions"
)

// Type aliases -- struct definitions moved to seam-core. These preserve the
// platformv1alpha1 package interface for all reconcilers without source edits.
type (
	TalosCluster           = seamcorev1alpha1.InfrastructureTalosCluster
	TalosClusterList       = seamcorev1alpha1.InfrastructureTalosClusterList
	TalosClusterSpec       = seamcorev1alpha1.InfrastructureTalosClusterSpec
	TalosClusterStatus     = seamcorev1alpha1.InfrastructureTalosClusterStatus
	TalosClusterMode       = seamcorev1alpha1.InfrastructureTalosClusterMode
	TalosClusterRole       = seamcorev1alpha1.InfrastructureTalosClusterRole
	TalosClusterOrigin     = seamcorev1alpha1.InfrastructureTalosClusterOrigin
	InfrastructureProvider = seamcorev1alpha1.InfrastructureProvider
	CAPIConfig             = seamcorev1alpha1.InfrastructureCAPIConfig
	CAPIControlPlaneConfig = seamcorev1alpha1.InfrastructureCAPIControlPlaneConfig
	CAPIWorkerPool         = seamcorev1alpha1.InfrastructureCAPIWorkerPool
	CAPICiliumPackRef      = seamcorev1alpha1.InfrastructureCAPICiliumPackRef
	LocalObjectRef         = seamcorev1alpha1.InfrastructureLocalObjectRef
)

// Mode constants.
const (
	TalosClusterModeBootstrap = seamcorev1alpha1.InfrastructureTalosClusterModeBootstrap
	TalosClusterModeImport    = seamcorev1alpha1.InfrastructureTalosClusterModeImport
)

// Role constants.
const (
	TalosClusterRoleManagement = seamcorev1alpha1.InfrastructureTalosClusterRoleManagement
	TalosClusterRoleTenant     = seamcorev1alpha1.InfrastructureTalosClusterRoleTenant
)

// Origin constants.
const (
	TalosClusterOriginBootstrapped = seamcorev1alpha1.InfrastructureTalosClusterOriginBootstrapped
	TalosClusterOriginImported     = seamcorev1alpha1.InfrastructureTalosClusterOriginImported
)

// InfrastructureProvider constants.
const (
	InfrastructureProviderNative = seamcorev1alpha1.InfrastructureProviderNative
	InfrastructureProviderCAPI   = seamcorev1alpha1.InfrastructureProviderCAPI
	InfrastructureProviderScreen = seamcorev1alpha1.InfrastructureProviderScreen
)

// Condition type constants for TalosCluster -- re-exported from seam-core/pkg/conditions.
// Platform reconcilers reference these via the platformv1alpha1 alias; new code should
// import github.com/ontai-dev/seam-core/pkg/conditions directly.
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
)

// Reason constants for TalosCluster -- re-exported from seam-core/pkg/conditions.
const (
	ReasonBootstrapJobSubmitted          = conditions.ReasonBootstrapJobSubmitted
	ReasonBootstrapJobComplete           = conditions.ReasonBootstrapJobComplete
	ReasonBootstrapJobFailed             = conditions.ReasonBootstrapJobFailed
	ReasonCAPIObjectsCreated             = conditions.ReasonCAPIObjectsCreated
	ReasonCAPIClusterRunning             = conditions.ReasonCAPIClusterRunning
	ReasonCiliumPackPending              = conditions.ReasonCiliumPackPending
	ReasonCiliumPackReady                = conditions.ReasonCiliumPackReady
	ReasonClusterReady                   = conditions.ReasonClusterReady
	ReasonImportComplete                 = conditions.ReasonImportComplete
	ReasonDegraded                       = conditions.ReasonDegraded
	ReasonControlPlaneNodeUnreachable    = conditions.ReasonControlPlaneNodeUnreachable
	ReasonWorkerNodeUnreachable          = conditions.ReasonWorkerNodeUnreachable
	ReasonConductorDeploymentAvailable   = conditions.ReasonConductorDeploymentAvailable
	ReasonConductorDeploymentUnavailable = conditions.ReasonConductorDeploymentUnavailable
	ReasonScreenNotImplemented           = conditions.ReasonScreenNotImplemented
	ReasonTalosVersionRequired           = conditions.ReasonTalosVersionRequired
	ReasonTalosConfigSecretAbsent        = conditions.ReasonTalosConfigSecretAbsent
	ReasonVersionUpgradeRequested        = conditions.ReasonVersionUpgradeRequested
	ReasonVersionUpgradeSubmitted        = conditions.ReasonVersionUpgradeSubmitted
	ReasonVersionUpgradeComplete         = conditions.ReasonVersionUpgradeComplete
	ReasonVersionRegressionAttempted     = conditions.ReasonVersionRegressionAttempted
)
