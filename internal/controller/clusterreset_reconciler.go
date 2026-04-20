package controller

// ClusterResetReconciler reconciles ClusterReset CRs. It enforces the INV-007
// human approval gate, then deletes the CAPI Cluster object (for CAPI-managed
// clusters), waits for all Machine objects to reach Deleted phase, and emits
// a RunnerConfig CR with a single cluster-reset step.
//
// HUMAN GATE — CP-INV-006, INV-007:
// The ontai.dev/reset-approved=true annotation must be present before any
// reconciliation beyond setting PendingApproval proceeds. The reconciler holds
// at PendingApproval and emits a Warning event if the annotation is absent.
//
// For CAPI-managed clusters (capi.enabled=true):
//  1. Verify approval annotation.
//  2. Delete CAPI Cluster object in tenant namespace.
//  3. Wait for all CAPI Machine objects to reach Deleted phase.
//  4. Submit cluster-reset RunnerConfig.
//  5. Wait for RunnerConfig terminal condition.
//  6. Delete tenant namespace.
//
// For management cluster (capi.enabled=false):
//  1. Verify approval annotation.
//  2. Submit cluster-reset RunnerConfig directly.
//  3. Wait for RunnerConfig terminal condition.
//
// Named Conductor capability: cluster-reset. platform-schema.md §5.
// platform-design.md §6. conductor-schema.md §17.

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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
	Client   client.Client
	Scheme   *runtime.Scheme
	Recorder clientevents.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=clusterresets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=clusterresets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=clusterresets/finalizers,verbs=update
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=runner.ontai.dev,resources=runnerconfigs,verbs=get;list;watch;create;update;patch;delete

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
	// The ontai.dev/reset-approved=true annotation must be present before proceeding.
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
		// Do not requeue — the next reconcile is triggered by the annotation write.
		return ctrl.Result{}, nil
	}

	// Approval confirmed. Clear PendingApproval condition.
	platformv1alpha1.SetCondition(
		&crst.Status.Conditions,
		platformv1alpha1.ConditionTypeResetPendingApproval,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonApprovalRequired,
		"Approval annotation confirmed.",
		crst.Generation,
	)

	// Determine whether the target cluster uses CAPI.
	capiEnabled, err := r.isCAPIEnabled(ctx, crst)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("ClusterResetReconciler: read TalosCluster: %w", err)
	}

	if capiEnabled {
		return r.reconcileCAPIReset(ctx, crst)
	}
	return r.reconcileDirectReset(ctx, crst)
}

// reconcileCAPIReset handles the CAPI-managed cluster reset sequence:
// delete CAPI Cluster → wait for all Machines deleted → submit reset RunnerConfig.
func (r *ClusterResetReconciler) reconcileCAPIReset(ctx context.Context, crst *platformv1alpha1.ClusterReset) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	tenantNS := "seam-tenant-" + crst.Spec.ClusterRef.Name

	// Step 1 — Delete the CAPI Cluster object if it still exists.
	capiCluster := &unstructured.Unstructured{}
	capiCluster.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "Cluster",
	})
	err := r.Client.Get(ctx, types.NamespacedName{
		Name:      crst.Spec.ClusterRef.Name,
		Namespace: tenantNS,
	}, capiCluster)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("reconcileCAPIReset: get CAPI Cluster: %w", err)
	}

	if err == nil {
		// CAPI Cluster still exists — delete it if not already terminating.
		if capiCluster.GetDeletionTimestamp() == nil {
			if err := r.Client.Delete(ctx, capiCluster); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("reconcileCAPIReset: delete CAPI Cluster: %w", err)
			}
			platformv1alpha1.SetCondition(
				&crst.Status.Conditions,
				platformv1alpha1.ConditionTypeResetPendingApproval,
				metav1.ConditionFalse,
				platformv1alpha1.ReasonCAPIClusterDeleting,
				"CAPI Cluster deletion initiated. Waiting for Machine objects to reach Deleted phase.",
				crst.Generation,
			)
			r.Recorder.Eventf(crst, nil, "Normal", "CAPIClusterDeleting", "CAPIClusterDeleting",
				"Deleted CAPI Cluster %s/%s — waiting for machines to drain",
				tenantNS, crst.Spec.ClusterRef.Name)
		}
		logger.Info("CAPI Cluster still terminating — requeuing",
			"name", crst.Name, "clusterName", crst.Spec.ClusterRef.Name)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Step 2 — CAPI Cluster deleted. Verify all Machine objects are gone.
	machineList := &unstructured.UnstructuredList{}
	machineList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "MachineList",
	})
	if err := r.Client.List(ctx, machineList, client.InNamespace(tenantNS)); err != nil {
		// Machine CRD may not be registered in tests — treat as empty.
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("reconcileCAPIReset: list Machines: %w", err)
		}
	}
	if len(machineList.Items) > 0 {
		logger.Info("waiting for Machine objects to be deleted",
			"name", crst.Name, "remaining", len(machineList.Items))
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Step 3 — All Machines gone. Submit the cluster-reset RunnerConfig.
	platformv1alpha1.SetCondition(
		&crst.Status.Conditions,
		platformv1alpha1.ConditionTypeResetPendingApproval,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonCAPIClusterDrained,
		"All CAPI Machine objects deleted. Submitting cluster-reset RunnerConfig.",
		crst.Generation,
	)
	return r.submitAndWatchResetRC(ctx, crst, tenantNS)
}

// reconcileDirectReset handles the management cluster (capi.enabled=false) reset:
// submit reset RunnerConfig directly.
func (r *ClusterResetReconciler) reconcileDirectReset(ctx context.Context, crst *platformv1alpha1.ClusterReset) (ctrl.Result, error) {
	return r.submitAndWatchResetRC(ctx, crst, crst.Namespace)
}

// submitAndWatchResetRC submits the cluster-reset RunnerConfig CR and watches
// for its terminal condition. conductor-schema.md §17.
func (r *ClusterResetReconciler) submitAndWatchResetRC(ctx context.Context, crst *platformv1alpha1.ClusterReset, rcNamespace string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	rcName := operationalRunnerConfigName(crst.Name)

	existingRC, err := getOperationalRunnerConfig(ctx, r.Client, rcNamespace, rcName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("ClusterResetReconciler: check RunnerConfig: %w", err)
	}

	if existingRC == nil {
		// Resolve operator leader node. The reset executor Job must not run on the leader.
		// conductor-schema.md §13.
		leaderNode, lErr := resolveOperatorLeaderNode(ctx, r.Client)
		if lErr != nil {
			return ctrl.Result{}, fmt.Errorf("ClusterResetReconciler: resolve leader node: %w", lErr)
		}
		exclusionNodes := buildNodeExclusions(nil, leaderNode)

		steps := []OperationalStep{
			{
				Name:          "reset",
				Capability:    capabilityClusterReset,
				HaltOnFailure: true,
			},
		}

		rc := buildOperationalRunnerConfig(rcName, rcNamespace, crst.Spec.ClusterRef.Name,
			exclusionNodes, leaderNode, steps)
		if err := controllerutil.SetControllerReference(crst, rc, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("ClusterResetReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, rc); err != nil {
			return ctrl.Result{}, fmt.Errorf("ClusterResetReconciler: create RunnerConfig: %w", err)
		}
		crst.Status.JobName = rcName
		platformv1alpha1.SetCondition(
			&crst.Status.Conditions,
			platformv1alpha1.ConditionTypeResetReady,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonResetJobSubmitted,
			fmt.Sprintf("RunnerConfig %s submitted for cluster-reset.", rcName),
			crst.Generation,
		)
		r.Recorder.Eventf(crst, nil, "Normal", "RunnerConfigSubmitted", "RunnerConfigSubmitted",
			"Submitted RunnerConfig %s for cluster-reset", rcName)
		logger.Info("submitted cluster-reset RunnerConfig",
			"name", crst.Name, "rcName", rcName)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// RunnerConfig exists — check terminal condition.
	complete, failed, failedStep := readRunnerConfigTerminalCondition(existingRC)
	if failed {
		crst.Status.OperationResult = fmt.Sprintf("RunnerConfig failed at step %q.", failedStep)
		platformv1alpha1.SetCondition(
			&crst.Status.Conditions,
			platformv1alpha1.ConditionTypeResetDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonResetJobFailed,
			fmt.Sprintf("RunnerConfig %s failed at step %q.", rcName, failedStep),
			crst.Generation,
		)
		r.Recorder.Eventf(crst, nil, "Warning", "RunnerConfigFailed", "RunnerConfigFailed",
			"RunnerConfig %s failed at step %q", rcName, failedStep)
		return ctrl.Result{}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// RunnerConfig complete. Mark reset complete.
	crst.Status.OperationResult = "RunnerConfig completed successfully."
	platformv1alpha1.SetCondition(
		&crst.Status.Conditions,
		platformv1alpha1.ConditionTypeResetReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonResetComplete,
		fmt.Sprintf("Cluster reset complete. RunnerConfig %s succeeded.", rcName),
		crst.Generation,
	)
	r.Recorder.Eventf(crst, nil, "Normal", "ResetComplete", "ResetComplete",
		"Cluster %s reset complete", crst.Spec.ClusterRef.Name)
	logger.Info("ClusterReset complete",
		"name", crst.Name, "cluster", crst.Spec.ClusterRef.Name)
	return ctrl.Result{}, nil
}

// isCAPIEnabled reads the owning TalosCluster's capi.enabled field.
func (r *ClusterResetReconciler) isCAPIEnabled(ctx context.Context, crst *platformv1alpha1.ClusterReset) (bool, error) {
	tc := &platformv1alpha1.TalosCluster{}
	ns := crst.Spec.ClusterRef.Namespace
	if ns == "" {
		ns = crst.Namespace
	}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      crst.Spec.ClusterRef.Name,
		Namespace: ns,
	}, tc); err != nil {
		if apierrors.IsNotFound(err) {
			// TalosCluster gone — assume non-CAPI for safety (direct reset).
			return false, nil
		}
		return false, fmt.Errorf("get TalosCluster %s/%s: %w", ns, crst.Spec.ClusterRef.Name, err)
	}
	return tc.Spec.CAPIEnabled(), nil
}

// SetupWithManager registers ClusterResetReconciler with the manager.
func (r *ClusterResetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.ClusterReset{}).
		Complete(r)
}
