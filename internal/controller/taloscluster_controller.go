package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrav1alpha1 "github.com/ontai-dev/platform/api/infrastructure/v1alpha1"
	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

// machineApplyAttemptsHaltThreshold is the number of consecutive ApplyConfiguration
// failures on port 50000 before TalosClusterReconciler raises ControlPlaneUnreachable
// (control plane nodes) or PartialWorkerAvailability (worker nodes).
const machineApplyAttemptsHaltThreshold int32 = 3

// TalosClusterReconciler watches TalosCluster CRs and drives cluster lifecycle.
//
// For management clusters (spec.capi.enabled=false): reads bootstrap secrets from
// ont-system and submits a bootstrap Conductor Job directly. Watches for Job
// completion and OperationResult, then sets TalosCluster status to Ready.
// platform-design.md §5.
//
// For target clusters (spec.capi.enabled=true): creates and owns all CAPI objects
// (SeamInfrastructureCluster, CAPI Cluster, TalosControlPlane, MachineDeployments,
// TalosConfigTemplates, SeamInfrastructureMachineTemplates) in the tenant namespace.
// Watches CAPI Cluster status and transitions TalosCluster status accordingly.
// Triggers the Cilium ClusterPack deployment when CAPI cluster reaches Running state.
// After Cilium is ready, ensures the Conductor Deployment is Available on the target
// cluster before marking the TalosCluster Ready (ConductorReady condition, Gap 27).
// platform-design.md §2.1, §4, §12.
//
// CP-INV-007: leader election is required — no reconciliation proceeds before
// the manager acquires the leader lock.
// CP-INV-008: all CAPI objects are owned by TalosCluster via ownerReference.
// CP-INV-009: every TalosConfigTemplate includes CNI=none and Cilium BPF params.
type TalosClusterReconciler struct {
	// Client is the controller-runtime client for Kubernetes API access.
	Client client.Client

	// Scheme is the runtime scheme used for object type registration.
	Scheme *runtime.Scheme

	// Recorder is the Kubernetes event recorder for emitting Warning and Normal events.
	Recorder record.EventRecorder

	// RemoteConductorAvailableFn, if non-nil, replaces the real remote Conductor
	// Deployment availability check in EnsureConductorDeploymentOnTargetCluster.
	// Used exclusively in unit tests to inject a controlled availability response
	// without a live target cluster kubeconfig.
	RemoteConductorAvailableFn func(ctx context.Context, clusterName string) (bool, error)
}

// Reconcile is the main reconciliation loop for TalosCluster.
//
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=seaminfrastructuremachines,verbs=get;list;watch
func (r *TalosClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Step A — Fetch the TalosCluster CR.
	tc := &platformv1alpha1.TalosCluster{}
	if err := r.Client.Get(ctx, req.NamespacedName, tc); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted — deletion triggers an event, not a Job. INV-006.
			logger.Info("TalosCluster not found — likely deleted, ignoring",
				"namespacedName", req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get TalosCluster %s: %w", req.NamespacedName, err)
	}

	// Step B — Set up deferred status patch.
	patchBase := client.MergeFrom(tc.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, tc, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch TalosCluster status",
					"name", tc.Name, "namespace", tc.Namespace)
			}
		}
	}()

	// Step C — Advance ObservedGeneration.
	tc.Status.ObservedGeneration = tc.Generation

	// Step C2 — Initialize LineageSynced on first observation (one-time write).
	// InfrastructureLineageController takes ownership when deployed.
	// seam-core-schema.md §7 Declaration 5.
	if platformv1alpha1.FindCondition(tc.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			tc.Generation,
		)
	}

	// Step D — Check for reserved infrastructure provider paths before routing.
	// Screen is a future operator (INV-021). Surface the reservation condition and
	// halt without reconciling the screen path.
	if tc.Spec.InfrastructureProvider == "screen" {
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeScreenProviderNotImplemented,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonScreenNotImplemented,
			"spec.infrastructureProvider=screen is reserved for the future Screen operator (INV-021). Screen is not yet implemented. No reconciliation will proceed on this path.",
			tc.Generation,
		)
		logger.Info("TalosCluster uses screen provider — reserved path, halting reconciliation",
			"name", tc.Name, "namespace", tc.Namespace)
		return ctrl.Result{}, nil
	}

	// Step E — Route to the appropriate reconciliation path.
	if !tc.Spec.CAPI.Enabled {
		return r.reconcileDirectBootstrap(ctx, tc)
	}
	return r.reconcileCAPIPath(ctx, tc)
}

// reconcileDirectBootstrap handles the management cluster bootstrap path
// (spec.capi.enabled=false). Submits a bootstrap Conductor Job if one does not
// yet exist. Watches for Job completion and OperationResult.
// platform-design.md §5.
func (r *TalosClusterReconciler) reconcileDirectBootstrap(ctx context.Context, tc *platformv1alpha1.TalosCluster) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling TalosCluster via direct bootstrap path",
		"name", tc.Name, "namespace", tc.Namespace)

	// Check for an existing bootstrap Job for this TalosCluster.
	jobName := bootstrapJobName(tc.Name)
	existingJob, err := r.getBootstrapJob(ctx, tc.Namespace, jobName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcileDirectBootstrap: check bootstrap job: %w", err)
	}

	if existingJob == nil {
		// No bootstrap Job yet — submit one.
		if err := r.submitBootstrapJob(ctx, tc, jobName); err != nil {
			platformv1alpha1.SetCondition(
				&tc.Status.Conditions,
				platformv1alpha1.ConditionTypeBootstrapping,
				metav1.ConditionFalse,
				platformv1alpha1.ReasonBootstrapJobFailed,
				fmt.Sprintf("Failed to submit bootstrap Job: %v", err),
				tc.Generation,
			)
			return ctrl.Result{}, fmt.Errorf("reconcileDirectBootstrap: submit bootstrap job: %w", err)
		}
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeBootstrapping,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonBootstrapJobSubmitted,
			fmt.Sprintf("Bootstrap Conductor Job %s submitted.", jobName),
			tc.Generation,
		)
		logger.Info("submitted bootstrap Conductor Job",
			"name", tc.Name, "jobName", jobName)
		// Requeue to watch for Job completion.
		return ctrl.Result{RequeueAfter: bootstrapPollInterval}, nil
	}

	// Bootstrap Job exists — check for OperationResult.
	complete, failed, result := r.readOperationResult(ctx, tc.Namespace, jobName)
	if failed {
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeBootstrapping,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonBootstrapJobFailed,
			fmt.Sprintf("Bootstrap Job %s failed: %s", jobName, result),
			tc.Generation,
		)
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonBootstrapJobFailed,
			fmt.Sprintf("Bootstrap Job %s failed: %s", jobName, result),
			tc.Generation,
		)
		return ctrl.Result{}, nil
	}
	if !complete {
		// Still running — requeue to poll.
		return ctrl.Result{RequeueAfter: bootstrapPollInterval}, nil
	}

	// Bootstrap complete — transition to Ready.
	tc.Status.Origin = platformv1alpha1.TalosClusterOriginBootstrapped
	platformv1alpha1.SetCondition(
		&tc.Status.Conditions,
		platformv1alpha1.ConditionTypeBootstrapping,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonBootstrapJobComplete,
		"Bootstrap Conductor Job completed successfully.",
		tc.Generation,
	)
	platformv1alpha1.SetCondition(
		&tc.Status.Conditions,
		platformv1alpha1.ConditionTypeReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonClusterReady,
		"Management cluster bootstrapped and Ready.",
		tc.Generation,
	)
	logger.Info("management cluster bootstrap complete, cluster Ready",
		"name", tc.Name)
	return ctrl.Result{}, nil
}

// reconcileCAPIPath handles the target cluster CAPI lifecycle path
// (spec.capi.enabled=true). Creates and owns all CAPI objects. Watches CAPI
// Cluster status and triggers Cilium deployment when cluster reaches Running.
// platform-design.md §2.1, §4.
func (r *TalosClusterReconciler) reconcileCAPIPath(ctx context.Context, tc *platformv1alpha1.TalosCluster) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling TalosCluster via CAPI path",
		"name", tc.Name, "namespace", tc.Namespace)

	// Step 1 — Ensure the tenant namespace exists.
	// Platform is the sole namespace creation authority. CP-INV-004.
	if err := r.ensureTenantNamespace(ctx, tc); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcileCAPIPath: ensure tenant namespace: %w", err)
	}

	// Step 2 — Ensure SeamInfrastructureCluster exists.
	// Owned by TalosCluster via ownerReference. CP-INV-008.
	if err := r.ensureSeamInfrastructureCluster(ctx, tc); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcileCAPIPath: ensure SeamInfrastructureCluster: %w", err)
	}

	// Step 3 — Ensure CAPI Cluster object exists.
	if err := r.ensureCAPICluster(ctx, tc); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcileCAPIPath: ensure CAPI Cluster: %w", err)
	}

	// Step 4 — Ensure TalosConfigTemplate exists (with CNI=none + Cilium BPF params).
	// CP-INV-009: every TalosConfigTemplate includes cluster.network.cni.name: none
	// and the Cilium-required BPF kernel parameters.
	if err := r.ensureTalosConfigTemplate(ctx, tc); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcileCAPIPath: ensure TalosConfigTemplate: %w", err)
	}

	// Step 5 — Ensure TalosControlPlane exists.
	if err := r.ensureTalosControlPlane(ctx, tc); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcileCAPIPath: ensure TalosControlPlane: %w", err)
	}

	// Step 6 — Ensure MachineDeployments and SeamInfrastructureMachineTemplates exist.
	for _, pool := range tc.Spec.CAPI.Workers {
		if err := r.ensureWorkerPool(ctx, tc, pool); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcileCAPIPath: ensure worker pool %q: %w",
				pool.Name, err)
		}
	}

	// Record CAPI objects created.
	platformv1alpha1.SetCondition(
		&tc.Status.Conditions,
		platformv1alpha1.ConditionTypeBootstrapping,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonCAPIObjectsCreated,
		"CAPI objects created. Waiting for CAPI Cluster to reach Running state.",
		tc.Generation,
	)

	// Step 6.5 — Check for port-50000 unreachability on SeamInfrastructureMachine nodes.
	// Control plane failures after machineApplyAttemptsHaltThreshold halt this reconcile.
	// Worker failures are noted as PartialWorkerAvailability but do not block.
	halt, err := r.checkMachineReachability(ctx, tc)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcileCAPIPath: check machine reachability: %w", err)
	}
	if halt {
		return ctrl.Result{RequeueAfter: capiPollInterval}, nil
	}

	// Step 7 — Read CAPI Cluster status.phase.
	capiPhase, err := r.getCAPIClusterPhase(ctx, tc)
	if err != nil {
		// CAPI Cluster not yet visible — requeue.
		return ctrl.Result{RequeueAfter: capiPollInterval}, nil
	}

	if capiPhase != "Running" {
		// CAPI cluster not yet Running — poll.
		logger.Info("CAPI Cluster not yet Running",
			"name", tc.Name, "capiPhase", capiPhase)
		return ctrl.Result{RequeueAfter: capiPollInterval}, nil
	}

	// Step 8 — CAPI cluster Running. Set CiliumPending condition.
	// CP-INV-013: CiliumPending is not a degraded state.
	platformv1alpha1.SetCondition(
		&tc.Status.Conditions,
		platformv1alpha1.ConditionTypeCiliumPending,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonCiliumPackPending,
		"CAPI Cluster Running. Waiting for Cilium ClusterPack PackInstance to reach Ready.",
		tc.Generation,
	)
	platformv1alpha1.SetCondition(
		&tc.Status.Conditions,
		platformv1alpha1.ConditionTypeBootstrapping,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonCAPIClusterRunning,
		"CAPI Cluster reached Running state.",
		tc.Generation,
	)

	// Record the CAPI cluster reference.
	tc.Status.CAPIClusterRef = &platformv1alpha1.LocalObjectRef{
		Name:      tc.Name,
		Namespace: tc.Namespace,
	}

	// Step 9 — Check Cilium PackInstance Ready status.
	if tc.Spec.CAPI.CiliumPackRef == nil {
		// No Cilium pack configured — skip Cilium gate (development mode).
		logger.Info("no CiliumPackRef configured — skipping Cilium gate (development mode)",
			"name", tc.Name)
		// Ensure Conductor Deployment exists and is Available. Gap 27.
		return r.ensureConductorReadyAndTransition(ctx, tc)
	}

	ciliumReady, err := r.isCiliumPackInstanceReady(ctx, tc)
	if err != nil {
		return ctrl.Result{RequeueAfter: capiPollInterval}, nil
	}
	if !ciliumReady {
		return ctrl.Result{RequeueAfter: capiPollInterval}, nil
	}

	// Step 10 — Cilium Ready. Ensure Conductor Deployment Available, then mark Ready.
	// The ConductorReady condition is the final gate before Ready=True. Gap 27.
	// platform-schema.md §12 Conductor Deployment Contract.
	return r.ensureConductorReadyAndTransition(ctx, tc)
}

// ensureConductorReadyAndTransition ensures the Conductor Deployment exists on the
// target cluster and has reached Available=True. If Available, sets ConductorReady=True
// and calls transitionToReady. If not yet Available, sets ConductorReady=False and
// requeues. This is the final gate before the TalosCluster transitions to Ready.
// platform-schema.md §12, Gap 27.
func (r *TalosClusterReconciler) ensureConductorReadyAndTransition(
	ctx context.Context,
	tc *platformv1alpha1.TalosCluster,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	available, err := r.EnsureConductorDeploymentOnTargetCluster(ctx, tc)
	if err != nil {
		logger.Error(err, "failed to ensure Conductor Deployment on target cluster",
			"cluster", tc.Name)
		return ctrl.Result{RequeueAfter: capiPollInterval},
			fmt.Errorf("ensureConductorReadyAndTransition: %w", err)
	}

	if !available {
		// Conductor Deployment not yet Available — set ConductorReady=False and requeue.
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeConductorReady,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonConductorDeploymentUnavailable,
			"Conductor Deployment has been created on the target cluster but has not yet reached Available=True. Requeuing.",
			tc.Generation,
		)
		logger.Info("Conductor Deployment not yet Available — requeueing",
			"cluster", tc.Name)
		return ctrl.Result{RequeueAfter: capiPollInterval}, nil
	}

	// Conductor is Available — set ConductorReady=True, then transition to cluster Ready.
	platformv1alpha1.SetCondition(
		&tc.Status.Conditions,
		platformv1alpha1.ConditionTypeConductorReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonConductorDeploymentAvailable,
		"Conductor Deployment is Available on the target cluster.",
		tc.Generation,
	)
	r.transitionToReady(tc)
	logger.Info("TalosCluster Ready — CAPI Running, Cilium Ready, Conductor Available",
		"name", tc.Name)
	return ctrl.Result{}, nil
}

// transitionToReady sets the TalosCluster to the fully Ready state.
func (r *TalosClusterReconciler) transitionToReady(tc *platformv1alpha1.TalosCluster) {
	tc.Status.Origin = platformv1alpha1.TalosClusterOriginBootstrapped
	platformv1alpha1.SetCondition(
		&tc.Status.Conditions,
		platformv1alpha1.ConditionTypeCiliumPending,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonCiliumPackReady,
		"Cilium ClusterPack PackInstance reached Ready.",
		tc.Generation,
	)
	platformv1alpha1.SetCondition(
		&tc.Status.Conditions,
		platformv1alpha1.ConditionTypeReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonClusterReady,
		"Cluster Ready: CAPI Running, Cilium up, all nodes Ready.",
		tc.Generation,
	)
}

// checkMachineReachability lists SeamInfrastructureMachine objects in the tenant
// namespace and checks for port-50000 ApplyConfiguration failures. After
// machineApplyAttemptsHaltThreshold failures:
//   - Control plane nodes → sets ControlPlaneUnreachable=true, returns halt=true.
//   - Worker nodes → sets PartialWorkerAvailability=true, returns halt=false.
//
// When no machines are stuck, both conditions are cleared. Returns (true, nil) to
// halt reconciliation when a control plane node is unreachable past the threshold.
func (r *TalosClusterReconciler) checkMachineReachability(ctx context.Context, tc *platformv1alpha1.TalosCluster) (halt bool, err error) {
	logger := log.FromContext(ctx)
	tenantNS := "seam-tenant-" + tc.Name

	simList := &infrav1alpha1.SeamInfrastructureMachineList{}
	if listErr := r.Client.List(ctx, simList, client.InNamespace(tenantNS)); listErr != nil {
		if apierrors.IsNotFound(listErr) {
			return false, nil
		}
		return false, fmt.Errorf("list SeamInfrastructureMachines in %s: %w", tenantNS, listErr)
	}

	if len(simList.Items) == 0 {
		return false, nil
	}

	var cpUnreachable, workerUnreachable bool
	for _, sim := range simList.Items {
		if sim.Status.MachineConfigApplied || sim.Status.ApplyAttempts < machineApplyAttemptsHaltThreshold {
			continue
		}
		if sim.Spec.NodeRole == infrav1alpha1.NodeRoleControlPlane {
			cpUnreachable = true
		} else {
			workerUnreachable = true
		}
	}

	if cpUnreachable {
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeControlPlaneUnreachable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonControlPlaneNodeUnreachable,
			fmt.Sprintf("Control plane node(s) unreachable on port 50000 after %d attempts. Halting reconciliation.", machineApplyAttemptsHaltThreshold),
			tc.Generation,
		)
		r.Recorder.Eventf(tc, "Warning", "ControlPlaneUnreachable",
			"Control plane node(s) unreachable on port 50000 after %d attempts", machineApplyAttemptsHaltThreshold)
		logger.Info("halting TalosCluster reconcile — control plane port-50000 unreachable",
			"name", tc.Name, "threshold", machineApplyAttemptsHaltThreshold)
		return true, nil
	}

	// Clear ControlPlaneUnreachable if previously set and now resolved.
	platformv1alpha1.SetCondition(
		&tc.Status.Conditions,
		platformv1alpha1.ConditionTypeControlPlaneUnreachable,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonControlPlaneNodeUnreachable,
		"All control plane nodes reachable on port 50000.",
		tc.Generation,
	)

	if workerUnreachable {
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypePartialWorkerAvailability,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonWorkerNodeUnreachable,
			fmt.Sprintf("Worker node(s) unreachable on port 50000 after %d attempts. Proceeding with available workers.", machineApplyAttemptsHaltThreshold),
			tc.Generation,
		)
		r.Recorder.Eventf(tc, "Warning", "PartialWorkerAvailability",
			"Worker node(s) unreachable on port 50000 after %d attempts — proceeding with available workers",
			machineApplyAttemptsHaltThreshold)
	} else {
		// Clear PartialWorkerAvailability — clears on next reconcile once resolved.
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypePartialWorkerAvailability,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonWorkerNodeUnreachable,
			"All worker nodes reachable on port 50000.",
			tc.Generation,
		)
	}

	return false, nil
}

// SetupWithManager registers TalosClusterReconciler with the controller-runtime
// manager. platform-design.md §2.1.
func (r *TalosClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.TalosCluster{}).
		Complete(r)
}
