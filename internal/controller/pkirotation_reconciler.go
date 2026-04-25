package controller

// PKIRotationReconciler reconciles PKIRotation CRs.
//
// Pattern: read the cluster RunnerConfig from ont-system, gate on capability
// availability, then submit a single batch/v1 Conductor executor Job.
// conductor-schema.md §5 §17.
//
// Named Conductor capability: pki-rotate. platform-schema.md §5.
// INV-018: gate failures are permanent — backoffLimit=0, no retries.

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

const capabilityPKIRotate = "pki-rotate"

// PKIRotationReconciler reconciles PKIRotation objects.
type PKIRotationReconciler struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Recorder clientevents.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=pkirotations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=pkirotations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=pkirotations/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=runner.ontai.dev,resources=runnerconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

func (r *PKIRotationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	pkir := &platformv1alpha1.PKIRotation{}
	if err := r.Client.Get(ctx, req.NamespacedName, pkir); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get PKIRotation %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(pkir.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, pkir, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch PKIRotation status",
					"name", pkir.Name, "namespace", pkir.Namespace)
			}
		}
	}()

	pkir.Status.ObservedGeneration = pkir.Generation

	// Initialize LineageSynced on first observation — one-time write.
	if platformv1alpha1.FindCondition(pkir.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&pkir.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			pkir.Generation,
		)
	}

	// If already complete, do nothing.
	readyCond := platformv1alpha1.FindCondition(pkir.Status.Conditions, platformv1alpha1.ConditionTypePKIRotationReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		return ctrl.Result{}, nil
	}

	// Gate: read the cluster RunnerConfig from ont-system and verify capability.
	clusterRC, err := getClusterRunnerConfig(ctx, r.Client, pkir.Spec.ClusterRef.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("PKIRotationReconciler: get cluster RunnerConfig: %w", err)
	}
	if clusterRC == nil {
		platformv1alpha1.SetCondition(
			&pkir.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonRunnerConfigNotFound,
			"Cluster RunnerConfig not yet present in ont-system. Waiting for Conductor agent.",
			pkir.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	if !hasCapability(clusterRC, capabilityPKIRotate) {
		platformv1alpha1.SetCondition(
			&pkir.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonCapabilityNotPublished,
			fmt.Sprintf("Capability %q not yet published by Conductor agent.", capabilityPKIRotate),
			pkir.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	platformv1alpha1.SetCondition(
		&pkir.Status.Conditions,
		platformv1alpha1.ConditionTypeCapabilityUnavailable,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonCapabilityNotPublished,
		"",
		pkir.Generation,
	)

	jobName := operationalJobName(pkir.Name, capabilityPKIRotate)

	existingJob, err := getOperationalJob(ctx, r.Client, pkir.Namespace, jobName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("PKIRotationReconciler: check job: %w", err)
	}

	if existingJob == nil {
		leaderNode, lErr := resolveOperatorLeaderNode(ctx, r.Client)
		if lErr != nil {
			return ctrl.Result{}, fmt.Errorf("PKIRotationReconciler: resolve leader node: %w", lErr)
		}
		nodeExclusions := buildNodeExclusions(nil, leaderNode)

		job := jobSpecWithExclusions(jobName, pkir.Namespace, pkir.Spec.ClusterRef.Name, capabilityPKIRotate, nodeExclusions, clusterRC.Spec.RunnerImage)
		if err := controllerutil.SetControllerReference(pkir, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("PKIRotationReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("PKIRotationReconciler: create job: %w", err)
		}
		pkir.Status.JobName = jobName
		platformv1alpha1.SetCondition(
			&pkir.Status.Conditions,
			platformv1alpha1.ConditionTypePKIRotationReady,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonPKIJobSubmitted,
			fmt.Sprintf("Conductor executor Job %s submitted for pki-rotate.", jobName),
			pkir.Generation,
		)
		r.Recorder.Eventf(pkir, nil, "Normal", "JobSubmitted", "JobSubmitted",
			"Submitted Conductor executor Job %s for pki-rotate", jobName)
		logger.Info("submitted PKIRotation Conductor executor Job",
			"name", pkir.Name, "jobName", jobName)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job exists — check OperationResult ConfigMap.
	complete, failed, result := readOperationalResult(ctx, r.Client, pkir.Namespace, jobName)
	if failed {
		pkir.Status.OperationResult = result
		platformv1alpha1.SetCondition(
			&pkir.Status.Conditions,
			platformv1alpha1.ConditionTypePKIRotationDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonPKIJobFailed,
			fmt.Sprintf("Conductor executor Job %s failed: %s", jobName, result),
			pkir.Generation,
		)
		r.Recorder.Eventf(pkir, nil, "Warning", "JobFailed", "JobFailed",
			"Conductor executor Job %s failed: %s", jobName, result)
		return ctrl.Result{}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	pkir.Status.OperationResult = result
	platformv1alpha1.SetCondition(
		&pkir.Status.Conditions,
		platformv1alpha1.ConditionTypePKIRotationReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonPKIJobComplete,
		fmt.Sprintf("Conductor executor Job %s completed successfully.", jobName),
		pkir.Generation,
	)
	r.Recorder.Eventf(pkir, nil, "Normal", "JobComplete", "JobComplete",
		"Conductor executor Job %s completed successfully", jobName)
	logger.Info("PKIRotation complete", "name", pkir.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager registers PKIRotationReconciler with the manager.
func (r *PKIRotationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.PKIRotation{}).
		Complete(r)
}
