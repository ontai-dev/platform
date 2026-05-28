package controller

// ClusterResetReconciler reconciles ClusterReset CRs. It enforces the INV-007
// human approval gate, then submits a cluster-reset Conductor executor Job.
//
// HUMAN GATE — CP-INV-006, INV-007:
// The ontai.dev/reset-approved=true annotation must be present before any
// reconciliation beyond setting PendingApproval proceeds.
//
//  1. Verify approval annotation.
//  2. Gate on cluster RunnerConfig capability availability.
//  3. Submit cluster-reset Conductor executor Job.
//  4. Wait for OperationResult ConfigMap.
//
// Named Conductor capability: cluster-reset. platform-schema.md §5.
// conductor-schema.md §5 §17.

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

const capabilityClusterReset = "cluster-reset"

// ClusterResetReconciler reconciles ClusterReset objects.
type ClusterResetReconciler struct {
	Client    client.Client
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Recorder  clientevents.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=clusterresets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=clusterresets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=clusterresets/finalizers,verbs=update
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=runner.ontai.dev,resources=runnerconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

func (r *ClusterResetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	crst := &platformv1alpha1.ClusterReset{}
	if err := r.Client.Get(ctx, req.NamespacedName, crst); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ClusterReset %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(crst.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, crst, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch ClusterReset status",
					"name", crst.Name, "namespace", crst.Namespace)
			}
		}
	}()

	crst.Status.ObservedGeneration = crst.Generation

	// Initialize LineageSynced on first observation — one-time write.
	if platformv1alpha1.FindCondition(crst.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&crst.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			crst.Generation,
		)
	}

	// If already complete, do nothing.
	readyCond := platformv1alpha1.FindCondition(crst.Status.Conditions, platformv1alpha1.ConditionTypeResetReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		return ctrl.Result{}, nil
	}

	// HUMAN GATE — CP-INV-006, INV-007.
	if crst.Annotations[platformv1alpha1.ResetApprovalAnnotation] != "true" {
		platformv1alpha1.SetCondition(
			&crst.Status.Conditions,
			platformv1alpha1.ConditionTypeResetPendingApproval,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonApprovalRequired,
			fmt.Sprintf("Waiting for human approval. Set annotation %s=true to proceed.", platformv1alpha1.ResetApprovalAnnotation),
			crst.Generation,
		)
		r.Recorder.Eventf(crst, nil, "Warning", "ApprovalRequired", "ApprovalRequired",
			"ClusterReset %s/%s is waiting for annotation %s=true",
			crst.Namespace, crst.Name, platformv1alpha1.ResetApprovalAnnotation)
		logger.Info("ClusterReset waiting for human approval",
			"name", crst.Name, "namespace", crst.Namespace,
			"annotation", platformv1alpha1.ResetApprovalAnnotation)
		return ctrl.Result{}, nil
	}

	platformv1alpha1.SetCondition(
		&crst.Status.Conditions,
		platformv1alpha1.ConditionTypeResetPendingApproval,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonApprovalRequired,
		"Approval annotation confirmed.",
		crst.Generation,
	)

	return r.submitAndWatchResetJob(ctx, crst, crst.Namespace)
}

// submitAndWatchResetJob gates on capability, submits the cluster-reset Job,
// and watches for its OperationResult ConfigMap. conductor-schema.md §5 §17.
func (r *ClusterResetReconciler) submitAndWatchResetJob(ctx context.Context, crst *platformv1alpha1.ClusterReset, jobNamespace string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Gate: read the cluster RunnerConfig from ont-system and verify capability.
	clusterRC, err := getClusterRunnerConfig(ctx, r.Client, crst.Spec.ClusterRef.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("ClusterResetReconciler: get cluster RunnerConfig: %w", err)
	}
	if clusterRC == nil {
		platformv1alpha1.SetCondition(
			&crst.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonRunnerConfigNotFound,
			"Cluster RunnerConfig not yet present in ont-system. Waiting for Conductor agent.",
			crst.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	if !hasCapability(clusterRC, capabilityClusterReset) {
		platformv1alpha1.SetCondition(
			&crst.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonCapabilityNotPublished,
			fmt.Sprintf("Capability %q not yet published by Conductor agent.", capabilityClusterReset),
			crst.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	platformv1alpha1.SetCondition(
		&crst.Status.Conditions,
		platformv1alpha1.ConditionTypeCapabilityUnavailable,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonCapabilityNotPublished,
		"",
		crst.Generation,
	)

	jobName := operationalJobName(crst.Name, capabilityClusterReset)

	existingJob, err := getOperationalJob(ctx, r.Client, jobNamespace, jobName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("ClusterResetReconciler: check job: %w", err)
	}

	if existingJob == nil {
		leaderNode, lErr := resolveOperatorLeaderNode(ctx, r.Client, r.APIReader)
		if lErr != nil {
			return ctrl.Result{}, fmt.Errorf("ClusterResetReconciler: resolve leader node: %w", lErr)
		}
		nodeExclusions := buildNodeExclusions(nil, leaderNode)

		job := jobSpecWithExclusions(jobName, jobNamespace, crst.Spec.ClusterRef.Name, capabilityClusterReset, nodeExclusions, clusterRC.Spec.RunnerImage)
		if err := controllerutil.SetControllerReference(crst, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("ClusterResetReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("ClusterResetReconciler: create job: %w", err)
		}
		crst.Status.JobName = jobName
		platformv1alpha1.SetCondition(
			&crst.Status.Conditions,
			platformv1alpha1.ConditionTypeResetReady,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonResetJobSubmitted,
			fmt.Sprintf("Conductor executor Job %s submitted for cluster-reset.", jobName),
			crst.Generation,
		)
		r.Recorder.Eventf(crst, nil, "Normal", "JobSubmitted", "JobSubmitted",
			"Submitted Conductor executor Job %s for cluster-reset", jobName)
		logger.Info("submitted cluster-reset Conductor executor Job",
			"name", crst.Name, "jobName", jobName)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job exists — check OperationResult ConfigMap.
	complete, failed, result := readOperationRecord(ctx, r.Client, crst.Spec.ClusterRef.Name, jobName)
	if failed {
		crst.Status.OperationResult = result
		platformv1alpha1.SetCondition(
			&crst.Status.Conditions,
			platformv1alpha1.ConditionTypeResetDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonResetJobFailed,
			fmt.Sprintf("Conductor executor Job %s failed: %s", jobName, result),
			crst.Generation,
		)
		r.Recorder.Eventf(crst, nil, "Warning", "JobFailed", "JobFailed",
			"Conductor executor Job %s failed: %s", jobName, result)
		return ctrl.Result{}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	crst.Status.OperationResult = result
	platformv1alpha1.SetCondition(
		&crst.Status.Conditions,
		platformv1alpha1.ConditionTypeResetReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonResetComplete,
		fmt.Sprintf("Cluster reset complete. Conductor executor Job %s succeeded.", jobName),
		crst.Generation,
	)
	r.Recorder.Eventf(crst, nil, "Normal", "ResetComplete", "ResetComplete",
		"Cluster %s reset complete", crst.Spec.ClusterRef.Name)
	logger.Info("ClusterReset complete",
		"name", crst.Name, "cluster", crst.Spec.ClusterRef.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager registers ClusterResetReconciler with the manager.
func (r *ClusterResetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.ClusterReset{}).
		Complete(r)
}
