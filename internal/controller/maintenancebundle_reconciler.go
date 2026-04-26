package controller

// MaintenanceBundleReconciler reconciles MaintenanceBundle CRs. A MaintenanceBundle
// is a pre-compiled scheduling artifact produced by `compiler maintenance`. The
// reconciler reads the pre-encoded scheduling context (maintenanceTargetNodes,
// operatorLeaderNode, s3ConfigSecretRef, operation type) and submits the
// appropriate Conductor executor Job directly — no cluster queries required.
//
// conductor-schema.md §9, platform-schema.md §10.
// CP-INV-010: No Kueue. Jobs are submitted directly.
// INV-018: backoffLimit=0. Gate failures are permanent.

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

// MaintenanceBundle Conductor capability names per conductor-schema.md §9.
const (
	capabilityNodeDrain              = "node-drain"
	capabilityMaintenanceUpgrade     = "talos-upgrade"
	capabilityMaintenanceEtcdBackup  = "etcd-backup"
	capabilityMachineConfigRotation  = "machineconfig-rotation"
)

// MaintenanceBundleReconciler reconciles MaintenanceBundle objects.
type MaintenanceBundleReconciler struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Recorder clientevents.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=maintenancebundles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=maintenancebundles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=maintenancebundles/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

func (r *MaintenanceBundleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	mb := &platformv1alpha1.MaintenanceBundle{}
	if err := r.Client.Get(ctx, req.NamespacedName, mb); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get MaintenanceBundle %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(mb.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, mb, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch MaintenanceBundle status",
					"name", mb.Name, "namespace", mb.Namespace)
			}
		}
	}()

	mb.Status.ObservedGeneration = mb.Generation

	// Initialize LineageSynced on first observation — one-time write.
	// seam-core-schema.md §7 Declaration 5.
	if platformv1alpha1.FindCondition(mb.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&mb.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			mb.Generation,
		)
	}

	// If already complete (Ready or Degraded), do nothing — one-shot CR.
	readyCond := platformv1alpha1.FindCondition(mb.Status.Conditions, platformv1alpha1.ConditionTypeMaintenanceBundleReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		return ctrl.Result{}, nil
	}
	degradedCond := platformv1alpha1.FindCondition(mb.Status.Conditions, platformv1alpha1.ConditionTypeMaintenanceBundleDegraded)
	if degradedCond != nil && degradedCond.Status == metav1.ConditionTrue {
		return ctrl.Result{}, nil
	}

	// Map the bundle operation to a Conductor capability name.
	capability, err := maintenanceBundleCapability(mb.Spec.Operation)
	if err != nil {
		platformv1alpha1.SetCondition(
			&mb.Status.Conditions,
			platformv1alpha1.ConditionTypeMaintenanceBundleDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMaintenanceBundleCapabilityUnknown,
			fmt.Sprintf("unknown operation %q: %v", mb.Spec.Operation, err),
			mb.Generation,
		)
		return ctrl.Result{}, nil
	}

	// Read the cluster RunnerConfig to obtain the executor image. CP-INV-003.
	clusterRC, err := getClusterRunnerConfig(ctx, r.Client, mb.Spec.ClusterRef.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("MaintenanceBundleReconciler: get cluster RunnerConfig: %w", err)
	}
	if clusterRC == nil {
		platformv1alpha1.SetCondition(
			&mb.Status.Conditions,
			platformv1alpha1.ConditionTypeMaintenanceBundlePending,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMaintenanceBundleJobSubmitted,
			"Cluster RunnerConfig not yet present in ont-system. Waiting for Conductor agent.",
			mb.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}

	jobName := operationalJobName(mb.Name, capability)

	// Check for an existing Job.
	existingJob, err := getOperationalJob(ctx, r.Client, mb.Namespace, jobName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("MaintenanceBundleReconciler: check job: %w", err)
	}

	if existingJob == nil {
		// Build node exclusions from pre-encoded scheduling context.
		// compiler maintenance pre-resolved maintenanceTargetNodes and operatorLeaderNode —
		// the reconciler consumes them directly without any cluster queries.
		nodeExclusions := buildNodeExclusions(mb.Spec.MaintenanceTargetNodes, mb.Spec.OperatorLeaderNode)

		job := jobSpecWithExclusions(jobName, mb.Namespace, mb.Spec.ClusterRef.Name, capability, nodeExclusions, clusterRC.Spec.RunnerImage)
		if err := controllerutil.SetControllerReference(mb, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("MaintenanceBundleReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("MaintenanceBundleReconciler: create job: %w", err)
		}
		mb.Status.JobName = jobName
		platformv1alpha1.SetCondition(
			&mb.Status.Conditions,
			platformv1alpha1.ConditionTypeMaintenanceBundlePending,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMaintenanceBundleJobSubmitted,
			fmt.Sprintf("Conductor executor Job %s submitted for %s.", jobName, capability),
			mb.Generation,
		)
		r.Recorder.Eventf(mb, nil, "Normal", "JobSubmitted", "JobSubmitted",
			"Submitted Conductor executor Job %s for %s", jobName, capability)
		logger.Info("submitted Conductor executor Job",
			"name", mb.Name, "jobName", jobName, "capability", capability)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job exists — check for OperationResult.
	complete, failed, result := readOperationRecord(ctx, r.Client, mb.Spec.ClusterRef.Name, jobName)
	if failed {
		mb.Status.OperationResult = result
		platformv1alpha1.SetCondition(
			&mb.Status.Conditions,
			platformv1alpha1.ConditionTypeMaintenanceBundleDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMaintenanceBundleJobFailed,
			fmt.Sprintf("Conductor executor Job %s failed: %s", jobName, result),
			mb.Generation,
		)
		platformv1alpha1.SetCondition(
			&mb.Status.Conditions,
			platformv1alpha1.ConditionTypeMaintenanceBundlePending,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonMaintenanceBundleJobFailed,
			"Job failed.",
			mb.Generation,
		)
		r.Recorder.Eventf(mb, nil, "Warning", "JobFailed", "JobFailed",
			"Conductor executor Job %s failed: %s", jobName, result)
		return ctrl.Result{}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job complete.
	mb.Status.OperationResult = result
	platformv1alpha1.SetCondition(
		&mb.Status.Conditions,
		platformv1alpha1.ConditionTypeMaintenanceBundlePending,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonMaintenanceBundleJobComplete,
		"Job completed.",
		mb.Generation,
	)
	platformv1alpha1.SetCondition(
		&mb.Status.Conditions,
		platformv1alpha1.ConditionTypeMaintenanceBundleReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonMaintenanceBundleJobComplete,
		fmt.Sprintf("Conductor executor Job %s completed successfully.", jobName),
		mb.Generation,
	)
	r.Recorder.Eventf(mb, nil, "Normal", "JobComplete", "JobComplete",
		"Conductor executor Job %s completed successfully", jobName)
	logger.Info("MaintenanceBundle complete",
		"name", mb.Name, "capability", capability)
	return ctrl.Result{}, nil
}

// maintenanceBundleCapability maps a MaintenanceBundleOperation to the Conductor
// capability name. conductor-schema.md §9.
func maintenanceBundleCapability(op platformv1alpha1.MaintenanceBundleOperation) (string, error) {
	switch op {
	case platformv1alpha1.MaintenanceBundleOperationDrain:
		return capabilityNodeDrain, nil
	case platformv1alpha1.MaintenanceBundleOperationUpgrade:
		return capabilityMaintenanceUpgrade, nil
	case platformv1alpha1.MaintenanceBundleOperationEtcdBackup:
		return capabilityMaintenanceEtcdBackup, nil
	case platformv1alpha1.MaintenanceBundleOperationMachineConfigRotation:
		return capabilityMachineConfigRotation, nil
	default:
		return "", fmt.Errorf("unknown MaintenanceBundleOperation %q", op)
	}
}

// SetupWithManager registers MaintenanceBundleReconciler with the manager.
func (r *MaintenanceBundleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.MaintenanceBundle{}).
		Complete(r)
}
