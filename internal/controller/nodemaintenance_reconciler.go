package controller

// NodeMaintenanceReconciler reconciles NodeMaintenance CRs. It submits a direct
// Conductor executor Job for the requested node-level operation regardless of the
// owning TalosCluster's capi.enabled value — CAPI has no node-patch equivalent.
// Named Conductor capabilities: node-patch, hardening-apply, credential-rotate.
// platform-schema.md §5 NodeMaintenance. platform-design.md §6.
//
// CP-INV-001: No talos goclient here. Node operations use Conductor executor Jobs.
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

// node maintenance Conductor capability names per conductor-schema.md.
const (
	capabilityNodePatch         = "node-patch"
	capabilityHardeningApply    = "hardening-apply"
	capabilityCredentialRotate  = "credential-rotate"
)

// NodeMaintenanceReconciler reconciles NodeMaintenance objects.
type NodeMaintenanceReconciler struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=nodemaintenances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=nodemaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=nodemaintenances/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

func (r *NodeMaintenanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	nm := &platformv1alpha1.NodeMaintenance{}
	if err := r.Client.Get(ctx, req.NamespacedName, nm); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get NodeMaintenance %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(nm.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, nm, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch NodeMaintenance status",
					"name", nm.Name, "namespace", nm.Namespace)
			}
		}
	}()

	nm.Status.ObservedGeneration = nm.Generation

	// Initialize LineageSynced on first observation — one-time write.
	if platformv1alpha1.FindCondition(nm.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&nm.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			nm.Generation,
		)
	}

	// If already complete, do nothing.
	readyCond := platformv1alpha1.FindCondition(nm.Status.Conditions, platformv1alpha1.ConditionTypeNodeMaintenanceReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		return ctrl.Result{}, nil
	}

	capability, err := nodeMaintenanceCapability(nm.Spec.Operation)
	if err != nil {
		platformv1alpha1.SetCondition(
			&nm.Status.Conditions,
			platformv1alpha1.ConditionTypeNodeMaintenanceDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonNodeJobFailed,
			fmt.Sprintf("unknown operation %q: %v", nm.Spec.Operation, err),
			nm.Generation,
		)
		return ctrl.Result{}, nil
	}

	jobName := operationalJobName(nm.Name, capability)

	existingJob, err := getOperationalJob(ctx, r.Client, nm.Namespace, jobName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("NodeMaintenanceReconciler: check job: %w", err)
	}

	if existingJob == nil {
		job := jobSpec(jobName, nm.Namespace, nm.Spec.ClusterRef.Name, capability)
		if err := controllerutil.SetControllerReference(nm, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("NodeMaintenanceReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("NodeMaintenanceReconciler: create job: %w", err)
		}
		nm.Status.JobName = jobName
		platformv1alpha1.SetCondition(
			&nm.Status.Conditions,
			platformv1alpha1.ConditionTypeNodeMaintenanceReady,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonNodeJobSubmitted,
			fmt.Sprintf("Conductor executor Job %s submitted for %s.", jobName, capability),
			nm.Generation,
		)
		r.Recorder.Eventf(nm, "Normal", "JobSubmitted",
			"Submitted Conductor executor Job %s for %s", jobName, capability)
		logger.Info("submitted Conductor executor Job",
			"name", nm.Name, "jobName", jobName, "capability", capability)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	complete, failed, result := readOperationalResult(ctx, r.Client, nm.Namespace, jobName)
	if failed {
		nm.Status.OperationResult = result
		platformv1alpha1.SetCondition(
			&nm.Status.Conditions,
			platformv1alpha1.ConditionTypeNodeMaintenanceDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonNodeJobFailed,
			fmt.Sprintf("Conductor executor Job %s failed: %s", jobName, result),
			nm.Generation,
		)
		r.Recorder.Eventf(nm, "Warning", "JobFailed",
			"Conductor executor Job %s failed: %s", jobName, result)
		return ctrl.Result{}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	nm.Status.OperationResult = result
	platformv1alpha1.SetCondition(
		&nm.Status.Conditions,
		platformv1alpha1.ConditionTypeNodeMaintenanceReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonNodeJobComplete,
		fmt.Sprintf("Conductor executor Job %s completed successfully.", jobName),
		nm.Generation,
	)
	r.Recorder.Eventf(nm, "Normal", "JobComplete",
		"Conductor executor Job %s completed successfully", jobName)
	logger.Info("NodeMaintenance complete", "name", nm.Name, "capability", capability)
	return ctrl.Result{}, nil
}

// nodeMaintenanceCapability maps a NodeMaintenanceOperation to the Conductor capability.
func nodeMaintenanceCapability(op platformv1alpha1.NodeMaintenanceOperation) (string, error) {
	switch op {
	case platformv1alpha1.NodeMaintenanceOperationPatch:
		return capabilityNodePatch, nil
	case platformv1alpha1.NodeMaintenanceOperationHardeningApply:
		return capabilityHardeningApply, nil
	case platformv1alpha1.NodeMaintenanceOperationCredentialRotate:
		return capabilityCredentialRotate, nil
	default:
		return "", fmt.Errorf("unknown NodeMaintenanceOperation %q", op)
	}
}

// SetupWithManager registers NodeMaintenanceReconciler with the manager.
func (r *NodeMaintenanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.NodeMaintenance{}).
		Complete(r)
}
