package controller

// EtcdMaintenanceReconciler reconciles EtcdMaintenance CRs. It emits a
// RunnerConfig CR with a single-step execution intent for the requested etcd
// lifecycle operation regardless of the owning TalosCluster's capi.enabled
// value — CAPI has no etcd concept. Conductor's execute mode runs the step.
//
// Named Conductor capabilities: etcd-backup, etcd-restore, etcd-defrag.
// platform-schema.md §5 EtcdMaintenance. platform-design.md §6.
// conductor-schema.md §17 RunnerConfig Execution Model.
//
// CP-INV-010: No Kueue. RunnerConfig submitted directly.
// INV-018: gate failures are permanent — HaltOnFailure=true on every step.

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

// etcd Conductor capability names per conductor-schema.md.
const (
	capabilityEtcdBackup  = "etcd-backup"
	capabilityEtcdRestore = "etcd-restore"
	capabilityEtcdDefrag  = "etcd-defrag"
)

// EtcdMaintenanceReconciler reconciles EtcdMaintenance objects.
type EtcdMaintenanceReconciler struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Recorder clientevents.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=etcdmaintenances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=etcdmaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=etcdmaintenances/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=runner.ontai.dev,resources=runnerconfigs,verbs=get;list;watch;create;update;patch;delete

func (r *EtcdMaintenanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	em := &platformv1alpha1.EtcdMaintenance{}
	if err := r.Client.Get(ctx, req.NamespacedName, em); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get EtcdMaintenance %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(em.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, em, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch EtcdMaintenance status",
					"name", em.Name, "namespace", em.Namespace)
			}
		}
	}()

	em.Status.ObservedGeneration = em.Generation

	// Initialize LineageSynced on first observation — one-time write.
	// seam-core-schema.md §7 Declaration 5.
	if platformv1alpha1.FindCondition(em.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&em.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			em.Generation,
		)
	}

	// If already complete, do nothing — this is a one-shot CR.
	readyCond := platformv1alpha1.FindCondition(em.Status.Conditions, platformv1alpha1.ConditionTypeEtcdMaintenanceReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		return ctrl.Result{}, nil
	}

	// Determine the Conductor capability for this operation.
	capability, err := etcdCapability(em.Spec.Operation)
	if err != nil {
		platformv1alpha1.SetCondition(
			&em.Status.Conditions,
			platformv1alpha1.ConditionTypeEtcdMaintenanceDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonEtcdJobFailed,
			fmt.Sprintf("unknown operation %q: %v", em.Spec.Operation, err),
			em.Generation,
		)
		return ctrl.Result{}, nil
	}

	rcName := operationalRunnerConfigName(em.Name)

	// Check for an existing RunnerConfig.
	existingRC, err := getOperationalRunnerConfig(ctx, r.Client, em.Namespace, rcName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("EtcdMaintenanceReconciler: check RunnerConfig: %w", err)
	}

	if existingRC == nil {
		// For backup operations: resolve S3 destination before submitting.
		// Store resolved secret ref in step Parameters.
		// platform-schema.md §10 S3 resolution hierarchy.
		var stepParams map[string]string
		if em.Spec.Operation == platformv1alpha1.EtcdMaintenanceOperationBackup {
			secretName, secretNS, found, sErr := resolveEtcdBackupS3Secret(ctx, r.Client, em)
			if sErr != nil {
				return ctrl.Result{}, fmt.Errorf("EtcdMaintenanceReconciler: resolve S3 secret: %w", sErr)
			}
			if !found {
				platformv1alpha1.SetCondition(
					&em.Status.Conditions,
					platformv1alpha1.EtcdBackupDestinationAbsent,
					metav1.ConditionTrue,
					platformv1alpha1.ReasonEtcdBackupDestinationAbsent,
					"No S3 backup destination configured: spec.etcdBackupS3SecretRef is absent and seam-etcd-backup-config Secret is not found in seam-system. Set either to proceed. platform-schema.md §10.",
					em.Generation,
				)
				r.Recorder.Eventf(em, nil, "Warning", "S3DestinationAbsent",
					"EtcdMaintenance %s/%s: no S3 backup destination configured", em.Namespace, em.Name)
				return ctrl.Result{}, nil
			}
			stepParams = map[string]string{
				"s3SecretName":      secretName,
				"s3SecretNamespace": secretNS,
			}
		}

		// Resolve operator leader node and build node exclusions.
		// conductor-schema.md §13: SelfOperation=true — exclude targets + leader.
		leaderNode, lErr := resolveOperatorLeaderNode(ctx, r.Client)
		if lErr != nil {
			return ctrl.Result{}, fmt.Errorf("EtcdMaintenanceReconciler: resolve leader node: %w", lErr)
		}
		exclusionNodes := buildNodeExclusions(em.Spec.TargetNodes, leaderNode)

		steps := []OperationalStep{
			{
				Name:          capability,
				Capability:    capability,
				Parameters:    stepParams,
				HaltOnFailure: true,
			},
		}

		rc := buildOperationalRunnerConfig(rcName, em.Namespace, em.Spec.ClusterRef.Name,
			exclusionNodes, leaderNode, steps)
		if err := controllerutil.SetControllerReference(em, rc, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("EtcdMaintenanceReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, rc); err != nil {
			return ctrl.Result{}, fmt.Errorf("EtcdMaintenanceReconciler: create RunnerConfig: %w", err)
		}
		em.Status.JobName = rcName
		platformv1alpha1.SetCondition(
			&em.Status.Conditions,
			platformv1alpha1.ConditionTypeEtcdMaintenanceRunning,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonEtcdJobSubmitted,
			fmt.Sprintf("RunnerConfig %s submitted for %s.", rcName, capability),
			em.Generation,
		)
		r.Recorder.Eventf(em, nil, "Normal", "RunnerConfigSubmitted", "",
			"Submitted RunnerConfig %s for %s", rcName, capability)
		logger.Info("submitted RunnerConfig",
			"name", em.Name, "rcName", rcName, "capability", capability)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// RunnerConfig exists — check terminal condition.
	complete, failed, failedStep := readRunnerConfigTerminalCondition(existingRC)
	if failed {
		em.Status.OperationResult = fmt.Sprintf("RunnerConfig failed at step %q.", failedStep)
		platformv1alpha1.SetCondition(
			&em.Status.Conditions,
			platformv1alpha1.ConditionTypeEtcdMaintenanceDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonEtcdJobFailed,
			fmt.Sprintf("RunnerConfig %s failed at step %q.", rcName, failedStep),
			em.Generation,
		)
		platformv1alpha1.SetCondition(
			&em.Status.Conditions,
			platformv1alpha1.ConditionTypeEtcdMaintenanceRunning,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonEtcdJobFailed,
			"RunnerConfig failed.",
			em.Generation,
		)
		r.Recorder.Eventf(em, nil, "Warning", "RunnerConfigFailed", "",
			"RunnerConfig %s failed at step %q", rcName, failedStep)
		return ctrl.Result{}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// RunnerConfig complete.
	em.Status.OperationResult = "RunnerConfig completed successfully."
	platformv1alpha1.SetCondition(
		&em.Status.Conditions,
		platformv1alpha1.ConditionTypeEtcdMaintenanceRunning,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonEtcdJobComplete,
		"RunnerConfig completed.",
		em.Generation,
	)
	platformv1alpha1.SetCondition(
		&em.Status.Conditions,
		platformv1alpha1.ConditionTypeEtcdMaintenanceReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonEtcdJobComplete,
		fmt.Sprintf("RunnerConfig %s completed successfully.", rcName),
		em.Generation,
	)
	r.Recorder.Eventf(em, nil, "Normal", "RunnerConfigComplete", "",
		"RunnerConfig %s completed successfully", rcName)
	logger.Info("EtcdMaintenance complete",
		"name", em.Name, "capability", capability)
	return ctrl.Result{}, nil
}

// etcdCapability maps an EtcdMaintenanceOperation to the Conductor capability name.
func etcdCapability(op platformv1alpha1.EtcdMaintenanceOperation) (string, error) {
	switch op {
	case platformv1alpha1.EtcdMaintenanceOperationBackup:
		return capabilityEtcdBackup, nil
	case platformv1alpha1.EtcdMaintenanceOperationRestore:
		return capabilityEtcdRestore, nil
	case platformv1alpha1.EtcdMaintenanceOperationDefrag:
		return capabilityEtcdDefrag, nil
	default:
		return "", fmt.Errorf("unknown EtcdMaintenanceOperation %q", op)
	}
}

// resolveEtcdBackupS3Secret resolves the S3 Secret for an etcd backup operation.
// Resolution order (platform-schema.md §10):
//  1. spec.etcdBackupS3SecretRef — per-operation Secret override.
//  2. seam-etcd-backup-config in seam-system — cluster-wide default.
//
// Returns (secretName, secretNamespace, found, error).
// found=false with error=nil means neither source is configured — the caller
// should set EtcdBackupDestinationAbsent and skip RunnerConfig submission.
func resolveEtcdBackupS3Secret(ctx context.Context, c client.Client, em *platformv1alpha1.EtcdMaintenance) (string, string, bool, error) {
	if em.Spec.EtcdBackupS3SecretRef != nil {
		ns := em.Spec.EtcdBackupS3SecretRef.Namespace
		if ns == "" {
			ns = "seam-system"
		}
		secret := &corev1.Secret{}
		if err := c.Get(ctx, types.NamespacedName{
			Name:      em.Spec.EtcdBackupS3SecretRef.Name,
			Namespace: ns,
		}, secret); err != nil {
			if apierrors.IsNotFound(err) {
				return "", "", false, nil
			}
			return "", "", false, fmt.Errorf("get S3 secret %s/%s: %w", ns, em.Spec.EtcdBackupS3SecretRef.Name, err)
		}
		return em.Spec.EtcdBackupS3SecretRef.Name, ns, true, nil
	}
	// Cluster-wide default.
	const defaultName = "seam-etcd-backup-config"
	const defaultNS = "seam-system"
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: defaultName, Namespace: defaultNS}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("get default S3 secret %s/%s: %w", defaultNS, defaultName, err)
	}
	return defaultName, defaultNS, true, nil
}

// SetupWithManager registers EtcdMaintenanceReconciler with the manager.
func (r *EtcdMaintenanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.EtcdMaintenance{}).
		Complete(r)
}
