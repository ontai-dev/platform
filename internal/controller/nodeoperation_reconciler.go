package controller

// NodeOperationReconciler reconciles NodeOperation CRs. Submits a Conductor executor
// Job for node-scale-up, node-decommission, or node-reboot.
//
// Named Conductor capabilities: node-scale-up, node-decommission, node-reboot.
// platform-schema.md §5 NodeOperation. platform-design.md §2.1.

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

const (
	capabilityNodeScaleUp      = "node-scale-up"
	capabilityNodeDecommission = "node-decommission"
	capabilityNodeReboot       = "node-reboot"
	capabilityNodeRollback     = "node-rollback"
)

// NodeOperationReconciler reconciles NodeOperation objects.
type NodeOperationReconciler struct {
	Client    client.Client
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Recorder  clientevents.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=nodeoperations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=nodeoperations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=nodeoperations/finalizers,verbs=update
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinedeployments,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch;delete;patch;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

func (r *NodeOperationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	nop := &platformv1alpha1.NodeOperation{}
	if err := r.Client.Get(ctx, req.NamespacedName, nop); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get NodeOperation %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(nop.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, nop, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch NodeOperation status",
					"name", nop.Name, "namespace", nop.Namespace)
			}
		}
	}()

	nop.Status.ObservedGeneration = nop.Generation

	// Initialize LineageSynced on first observation — one-time write.
	if platformv1alpha1.FindCondition(nop.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&nop.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			nop.Generation,
		)
	}

	// If already complete, self-delete after the day-2 TTL; requeue until then.
	readyCond := platformv1alpha1.FindCondition(nop.Status.Conditions, platformv1alpha1.ConditionTypeNodeOperationReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		if expired, after := day2TTLExpired(readyCond.LastTransitionTime.Time); expired {
			_ = r.Client.Delete(ctx, nop)
			return ctrl.Result{}, nil
		} else {
			return ctrl.Result{RequeueAfter: after}, nil
		}
	}

	return r.reconcileDirectNodeOp(ctx, nop)
}

// reconcileDirectNodeOp gates on capability then submits a single batch/v1
// Conductor executor Job. conductor-schema.md §5 §17.
func (r *NodeOperationReconciler) reconcileDirectNodeOp(ctx context.Context, nop *platformv1alpha1.NodeOperation) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	capability, err := nodeOpCapability(nop.Spec.Operation)
	if err != nil {
		platformv1alpha1.SetCondition(
			&nop.Status.Conditions,
			platformv1alpha1.ConditionTypeNodeOperationDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonNodeOpJobFailed,
			fmt.Sprintf("unknown operation %q: %v", nop.Spec.Operation, err),
			nop.Generation,
		)
		return ctrl.Result{}, nil
	}

	// Gate: read the cluster RunnerConfig from ont-system and verify capability.
	clusterRC, err := getClusterRunnerConfig(ctx, r.Client, nop.Spec.ClusterRef.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("NodeOperationReconciler: get cluster RunnerConfig: %w", err)
	}
	if clusterRC == nil {
		platformv1alpha1.SetCondition(
			&nop.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonRunnerConfigNotFound,
			"Cluster RunnerConfig not yet present in ont-system. Waiting for Conductor agent.",
			nop.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	if !hasCapability(clusterRC, capability) {
		platformv1alpha1.SetCondition(
			&nop.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonCapabilityNotPublished,
			fmt.Sprintf("Capability %q not yet published by Conductor agent.", capability),
			nop.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	platformv1alpha1.SetCondition(
		&nop.Status.Conditions,
		platformv1alpha1.ConditionTypeCapabilityUnavailable,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonCapabilityNotPublished,
		"",
		nop.Generation,
	)

	jobName := retryJobName(nop.Name, capability, nop.Status.RetryCount)

	existingJob, err := getOperationalJob(ctx, r.Client, nop.Namespace, jobName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("NodeOperationReconciler: check job: %w", err)
	}

	if existingJob == nil {
		leaderNode, lErr := resolveOperatorLeaderNode(ctx, r.Client, r.APIReader)
		if lErr != nil {
			return ctrl.Result{}, fmt.Errorf("NodeOperationReconciler: resolve leader node: %w", lErr)
		}
		nodeExclusions := buildNodeExclusions(nop.Spec.TargetNodes, leaderNode)

		job := jobSpecWithExclusions(jobName, nop.Namespace, nop.Spec.ClusterRef.Name, capability, nodeExclusions, clusterRC.Spec.RunnerImage)
		// Scale-up needs the tenant cluster kubeconfig to poll Kubernetes node Ready. RECON-C8.
		if capability == capabilityNodeScaleUp {
			addKubeconfigMount(job, nop.Spec.ClusterRef.Name)
		}
		if err := controllerutil.SetControllerReference(nop, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("NodeOperationReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("NodeOperationReconciler: create job: %w", err)
		}
		nop.Status.JobName = jobName
		platformv1alpha1.SetCondition(
			&nop.Status.Conditions,
			platformv1alpha1.ConditionTypeNodeOperationReady,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonNodeOpJobSubmitted,
			fmt.Sprintf("Conductor executor Job %s submitted for %s.", jobName, capability),
			nop.Generation,
		)
		r.Recorder.Eventf(nop, nil, "Normal", "JobSubmitted", "JobSubmitted",
			"Submitted Conductor executor Job %s for %s", jobName, capability)
		logger.Info("submitted NodeOperation Conductor executor Job",
			"name", nop.Name, "jobName", jobName, "capability", capability)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job exists — check OperationResult ConfigMap.
	complete, failed, result := readOperationRecord(ctx, r.Client, nop.Spec.ClusterRef.Name, jobName)
	if failed {
		nop.Status.RetryCount++
		nop.Status.OperationResult = result
		if nop.Status.RetryCount >= effectiveMaxRetry(nop.Spec.MaxRetry) {
			msg := fmt.Sprintf("Conductor executor Job %s failed after %d attempts: %s. Human intervention required.", jobName, nop.Status.RetryCount, result)
			platformv1alpha1.SetCondition(
				&nop.Status.Conditions,
				platformv1alpha1.ConditionTypeNodeOperationDegraded,
				metav1.ConditionTrue,
				platformv1alpha1.ReasonNodeOpPermanentFailure,
				msg,
				nop.Generation,
			)
			r.Recorder.Eventf(nop, nil, "Warning", "PermanentFailure", "PermanentFailure", "%s", msg)
			clusterNS := nop.Spec.ClusterRef.Namespace
			if clusterNS == "" {
				clusterNS = nop.Namespace
			}
			_ = setTalosClusterHumanInterventionRequired(ctx, r.Client, nop.Spec.ClusterRef.Name, clusterNS,
				fmt.Sprintf("NodeOperation %s/%s permanently failed after %d attempts.", nop.Namespace, nop.Name, nop.Status.RetryCount),
				nop.Generation)
			return ctrl.Result{}, nil
		}
		platformv1alpha1.SetCondition(
			&nop.Status.Conditions,
			platformv1alpha1.ConditionTypeNodeOperationDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonNodeOpJobFailed,
			fmt.Sprintf("Conductor executor Job %s failed (attempt %d/%d): %s. Retrying.", jobName, nop.Status.RetryCount, effectiveMaxRetry(nop.Spec.MaxRetry), result),
			nop.Generation,
		)
		r.Recorder.Eventf(nop, nil, "Warning", "JobFailed", "JobFailed",
			"Conductor executor Job %s failed (attempt %d/%d): %s", jobName, nop.Status.RetryCount, effectiveMaxRetry(nop.Spec.MaxRetry), result)
		return ctrl.Result{RequeueAfter: retryJobRetryInterval}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	nop.Status.RetryCount = 0
	nop.Status.OperationResult = result
	platformv1alpha1.SetCondition(
		&nop.Status.Conditions,
		platformv1alpha1.ConditionTypeNodeOperationReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonNodeOpJobComplete,
		fmt.Sprintf("Conductor executor Job %s completed successfully.", jobName),
		nop.Generation,
	)
	r.Recorder.Eventf(nop, nil, "Normal", "JobComplete", "JobComplete",
		"Conductor executor Job %s completed successfully", jobName)
	logger.Info("NodeOperation complete", "name", nop.Name, "capability", capability)
	return ctrl.Result{}, nil
}

// nodeOpCapability maps a NodeOperationType to the Conductor capability name.
func nodeOpCapability(op platformv1alpha1.NodeOperationType) (string, error) {
	switch op {
	case platformv1alpha1.NodeOperationTypeScaleUp:
		return capabilityNodeScaleUp, nil
	case platformv1alpha1.NodeOperationTypeDecommission:
		return capabilityNodeDecommission, nil
	case platformv1alpha1.NodeOperationTypeReboot:
		return capabilityNodeReboot, nil
	case platformv1alpha1.NodeOperationTypeRollback:
		return capabilityNodeRollback, nil
	default:
		return "", fmt.Errorf("unknown NodeOperationType %q", op)
	}
}

// SetupWithManager registers NodeOperationReconciler with the manager.
func (r *NodeOperationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.NodeOperation{}).
		Complete(r)
}
