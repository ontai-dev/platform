package controller

// EtcdMaintenanceReconciler reconciles EtcdMaintenance CRs. It submits a direct
// Conductor executor Job for the requested etcd lifecycle operation regardless of
// the owning TalosCluster's capi.enabled value — CAPI has no etcd concept.
// Named Conductor capabilities: etcd-backup, etcd-restore, etcd-defrag.
// platform-schema.md §5 EtcdMaintenance. platform-design.md §6.
//
// CP-INV-010: No Kueue. Jobs are submitted directly.
// INV-018: backoffLimit=0. Gate failures are permanent.

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
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
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=etcdmaintenances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=etcdmaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=etcdmaintenances/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

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

	jobName := operationalJobName(em.Name, capability)

	// Check for an existing Job.
	existingJob, err := getOperationalJob(ctx, r.Client, em.Namespace, jobName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("EtcdMaintenanceReconciler: check job: %w", err)
	}

	if existingJob == nil {
		// No Job yet — submit one.
		job := jobSpec(jobName, em.Namespace, em.Spec.ClusterRef.Name, capability)
		if err := controllerutil.SetControllerReference(em, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("EtcdMaintenanceReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("EtcdMaintenanceReconciler: create job: %w", err)
		}
		em.Status.JobName = jobName
		platformv1alpha1.SetCondition(
			&em.Status.Conditions,
			platformv1alpha1.ConditionTypeEtcdMaintenanceRunning,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonEtcdJobSubmitted,
			fmt.Sprintf("Conductor executor Job %s submitted for %s.", jobName, capability),
			em.Generation,
		)
		r.Recorder.Eventf(em, "Normal", "JobSubmitted",
			"Submitted Conductor executor Job %s for %s", jobName, capability)
		logger.Info("submitted Conductor executor Job",
			"name", em.Name, "jobName", jobName, "capability", capability)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job exists — check for OperationResult.
	complete, failed, result := readOperationalResult(ctx, r.Client, em.Namespace, jobName)
	if failed {
		em.Status.OperationResult = result
		platformv1alpha1.SetCondition(
			&em.Status.Conditions,
			platformv1alpha1.ConditionTypeEtcdMaintenanceDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonEtcdJobFailed,
			fmt.Sprintf("Conductor executor Job %s failed: %s", jobName, result),
			em.Generation,
		)
		platformv1alpha1.SetCondition(
			&em.Status.Conditions,
			platformv1alpha1.ConditionTypeEtcdMaintenanceRunning,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonEtcdJobFailed,
			"Job failed.",
			em.Generation,
		)
		r.Recorder.Eventf(em, "Warning", "JobFailed",
			"Conductor executor Job %s failed: %s", jobName, result)
		return ctrl.Result{}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job complete.
	em.Status.OperationResult = result
	platformv1alpha1.SetCondition(
		&em.Status.Conditions,
		platformv1alpha1.ConditionTypeEtcdMaintenanceRunning,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonEtcdJobComplete,
		"Job completed.",
		em.Generation,
	)
	platformv1alpha1.SetCondition(
		&em.Status.Conditions,
		platformv1alpha1.ConditionTypeEtcdMaintenanceReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonEtcdJobComplete,
		fmt.Sprintf("Conductor executor Job %s completed successfully.", jobName),
		em.Generation,
	)
	r.Recorder.Eventf(em, "Normal", "JobComplete",
		"Conductor executor Job %s completed successfully", jobName)
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

// SetupWithManager registers EtcdMaintenanceReconciler with the manager.
func (r *EtcdMaintenanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.EtcdMaintenance{}).
		Complete(r)
}
