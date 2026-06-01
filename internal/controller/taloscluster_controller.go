package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientevents "k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

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
	Recorder clientevents.EventRecorder

	// RemoteConductorBootstrapDoneFn, if non-nil, replaces the real remote bootstrap
	// window setup in EnsureRemoteConductorBootstrap. Used exclusively in unit tests
	// to inject a controlled response without a live target cluster kubeconfig.
	RemoteConductorBootstrapDoneFn func(ctx context.Context, clusterName string) (bool, error)

	// KubeconfigGeneratorFn, if non-nil, replaces the real talos goclient call in
	// ensureKubeconfigSecret. The function receives the cluster name and endpoint and
	// returns raw kubeconfig bytes. Used exclusively in unit tests to avoid requiring
	// a live talos endpoint. CP-INV-001 extension: authorized by Governor 2026-04-10.
	KubeconfigGeneratorFn func(ctx context.Context, clusterName, endpoint string) ([]byte, error)

}

// Reconcile is the main reconciliation loop for TalosCluster.
//
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=upgradepolicies,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructuretalosclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructuretalosclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructurerunnerconfigs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructuretalosclusteroperationresults,verbs=get;list;watch
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

	// Step A2 — Handle deletion. If DeletionTimestamp is set, run cleanup and return.
	// The finalizer was added on first reconcile when ontai.dev/owns-runnerconfig=true.
	// Bug 3: RunnerConfig in ont-system is deleted before the TalosCluster is released.
	if !tc.DeletionTimestamp.IsZero() {
		return r.handleTalosClusterDeletion(ctx, tc)
	}

	// Step B — Set up deferred status patch.
	// RetryOnConflict handles the 409 Conflict that arises under a 2-replica
	// deployment when both replicas reconcile the same TalosCluster concurrently.
	// On each attempt, the latest object is fetched to get a fresh resourceVersion,
	// the computed status is applied, and the patch is sent. PLATFORM-BL-STATUS-PATCH-CONFLICT.
	defer func() {
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			latest := &platformv1alpha1.TalosCluster{}
			if err := r.Client.Get(ctx, client.ObjectKeyFromObject(tc), latest); err != nil {
				if apierrors.IsNotFound(err) {
					return nil
				}
				return err
			}
			freshBase := client.MergeFrom(latest.DeepCopy())
			latest.Status = tc.Status
			return r.Client.Status().Patch(ctx, latest, freshBase)
		})
		if retryErr != nil {
			if !apierrors.IsNotFound(retryErr) {
				logger.Error(retryErr, "failed to patch TalosCluster status",
					"name", tc.Name, "namespace", tc.Namespace)
			}
		}
	}()

	// Step C0 — Ensure metadata-only finalizers before any status is written to tc.
	// Finalizer adds call r.Client.Update(ctx, tc). The controller-runtime fake client
	// with WithStatusSubresource strips tc.Status in-place on Update, so all finalizer
	// Updates must precede any tc.Status assignments.

	// RunnerConfig cleanup finalizer (annotation-gated). Bug 3.
	if err := r.ensureRunnerConfigCleanupFinalizer(ctx, tc); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile: ensure runnerconfig-cleanup finalizer: %w", err)
	}
	// Tenant namespace cleanup finalizer (CAPI clusters only). Cross-namespace
	// ownerReferences are not supported by Kubernetes GC. PLATFORM-BL-TENANT-GC.
	if err := r.ensureTenantNamespaceCleanupFinalizer(ctx, tc); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile: ensure tenant-namespace-cleanup finalizer: %w", err)
	}
	// Wrapper-runner CRB cleanup finalizer (role=tenant only). Cluster-scoped CRB
	// cannot be removed by namespace deletion. PLATFORM-BL-WRAPPER-RUNNER-RBAC-LIFECYCLE.
	if err := r.ensureWrapperRunnerCRBCleanupFinalizer(ctx, tc); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile: ensure wrapper-runner-crb finalizer: %w", err)
	}
	// Decision H cascade finalizer (role=tenant only). Enforces wrapper-first,
	// guardian-second teardown order before RunnerConfig, namespace, and CRB cleanup.
	// Decision H, T-24.
	if err := r.ensureDecisionHCascadeFinalizer(ctx, tc); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile: ensure decision-h-cascade finalizer: %w", err)
	}

	// Step C — Advance ObservedGeneration.
	tc.Status.ObservedGeneration = tc.Generation

	// Step C1 is now handled in Step C0 above.

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

	// Step C3 — Version regression guard. If spec.talosVersion would downgrade the
	// cluster below status.observedTalosVersion, block and set condition. The cluster
	// stays at its current running version until spec is corrected.
	if checkVersionRegression(tc) {
		return ctrl.Result{}, nil
	}

	// Step C4 — spec.versionUpgrade gate. If set on a Ready cluster, auto-create an
	// UpgradePolicy CR and track completion. Only applicable to cluster-wide Talos
	// upgrades, not to individual node ops, etcd maintenance, or other day-2 ops.
	readyCond := platformv1alpha1.FindCondition(tc.Status.Conditions, platformv1alpha1.ConditionTypeReady)
	if tc.Spec.VersionUpgrade && readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		if done, result, err := r.reconcileVersionUpgrade(ctx, tc); done {
			return result, err
		}
	}

	// Step D — Check for reserved infrastructure provider paths before routing.
	// Screen is a future operator (INV-021). Surface the reservation condition and
	// halt without reconciling the screen path.
	if tc.Spec.InfrastructureProvider == platformv1alpha1.InfrastructureProviderScreen {
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

	// Capture whether the cluster was already in Ready state before routing.
	// This distinguishes stable-Ready clusters from clusters that just transitioned
	// to Ready in this reconcile pass. PKI expiry monitoring is only triggered for
	// stable-Ready clusters so that first-pass Ready transitions retain the original
	// clean ctrl.Result{} return. platform-schema.md §13.
	prevReadyCond := platformv1alpha1.FindCondition(tc.Status.Conditions, platformv1alpha1.ConditionTypeReady)
	wasAlreadyReady := prevReadyCond != nil && prevReadyCond.Status == metav1.ConditionTrue

	// Step E — Reconcile via direct bootstrap path.
	routeResult, routeErr := r.reconcileDirectBootstrap(ctx, tc)
	if routeErr != nil {
		return routeResult, routeErr
	}

	// Step G -- Bootstrap hardening. When hardeningProfileRef is set and the cluster is
	// currently Ready, ensure the bootstrap NodeMaintenance exists in seam-tenant-{cluster}
	// and set HardeningApplied once it reaches Ready=True.
	// Idempotent: the label check prevents duplicate NodeMaintenance creation.
	if tc.Spec.HardeningProfileRef != nil {
		currentReady := platformv1alpha1.FindCondition(tc.Status.Conditions, platformv1alpha1.ConditionTypeReady)
		if currentReady != nil && currentReady.Status == metav1.ConditionTrue {
			hardenResult, hardenErr := r.ensureBootstrapHardening(ctx, tc)
			if hardenErr != nil {
				return ctrl.Result{}, fmt.Errorf("reconcile: ensureBootstrapHardening: %w", hardenErr)
			}
			if hardenResult.RequeueAfter > 0 {
				return hardenResult, nil
			}
		}
	}

	// Step F -- PKI expiry check and annotation-triggered rotation.
	// Only executed when the cluster was already in Ready state before this
	// reconcile pass (stable-Ready). Non-fatal: failures are logged and result
	// in a requeue rather than an error return. platform-schema.md §13.
	if wasAlreadyReady {
		// Annotation-based node roster refresh. RECON-C9.
		if tc.Annotations != nil && tc.Annotations[AnnotationRefreshNodeRoster] == "true" {
			if err := r.reconcileNodeRosterRefresh(ctx, tc); err != nil {
				logger.Error(err, "node roster refresh failed -- non-fatal, will retry")
				return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
			}
		}

		// Annotation-based on-demand rotation.
		if tc.Annotations != nil && tc.Annotations["platform.ontai.dev/rotate-pki"] == "true" {
			if err := ensureAnnotationRotationPKI(ctx, r.Client, r.Scheme, tc); err != nil {
				return ctrl.Result{}, fmt.Errorf("ensureAnnotationRotationPKI: %w", err)
			}
			// Clear annotation via patch.
			patch := client.MergeFrom(tc.DeepCopy())
			delete(tc.Annotations, "platform.ontai.dev/rotate-pki")
			if pErr := r.Client.Patch(ctx, tc, patch); pErr != nil {
				return ctrl.Result{}, fmt.Errorf("clear rotate-pki annotation: %w", pErr)
			}
		}

		// Expiry detection and auto-rotation.
		rotationNeeded, pkiErr := syncPKIExpiry(ctx, r.Client, tc)
		if pkiErr != nil {
			logger.Error(pkiErr, "PKI expiry detection failed -- non-fatal, will retry")
			// Requeue for retry in 1h; do not return an error that would reset backoff.
			return ctrl.Result{RequeueAfter: 1 * time.Hour}, nil
		}
		if rotationNeeded {
			if err := ensureAutoRotationPKI(ctx, r.Client, r.Scheme, tc); err != nil {
				return ctrl.Result{}, fmt.Errorf("ensureAutoRotationPKI: %w", err)
			}
		}

		// Schedule daily expiry monitoring so the reconciler checks PKI expiry
		// even when no other spec or status changes trigger a reconcile.
		if routeResult.RequeueAfter == 0 {
			return ctrl.Result{RequeueAfter: 24 * time.Hour}, nil
		}
	}

	return routeResult, nil
}

// reconcileDirectBootstrap handles the management cluster bootstrap path
// (spec.capi.enabled=false). The reconciler implements a three-part contract
// that prevents duplicate bootstrap Jobs when the TalosCluster CR is applied
// to a cluster that already exists. platform-schema.md §3, platform-design.md §5.
//
// Part 1 — Idempotency guard: if a RunnerConfig already exists in the management
// namespace before this reconcile creates one, the cluster was already running when
// the CR was applied. Skip Job submission and transition directly to Ready.
//
// Part 2 — RunnerConfig creation: ensure a RunnerConfig exists in the management
// namespace before any Job is submitted. Created on first observation when absent.
//
// Part 3 — Management cluster ready transition: after the idempotency guard fires,
// set origin=bootstrapped and Ready=True to acknowledge the existing cluster.
func (r *TalosClusterReconciler) reconcileDirectBootstrap(ctx context.Context, tc *platformv1alpha1.TalosCluster) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling TalosCluster via direct bootstrap path",
		"name", tc.Name, "namespace", tc.Namespace)

	// Part 1 — check whether a RunnerConfig pre-exists before this reconcile
	// creates one. A pre-existing RunnerConfig signals that bootstrap already
	// occurred (Platform created it in a prior session, or the compiler created it
	// during the bootstrap sequence). The result is captured before any creation so
	// that the distinction between "pre-existing" and "just created by us" is stable
	// within this reconcile pass. platform-schema.md §3.
	preExistingRC, err := r.getBootstrapRunnerConfig(ctx, tc.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcileDirectBootstrap: check RunnerConfig: %w", err)
	}

	// Part 2 — ensure RunnerConfig exists. On first observation create it now so
	// that a RunnerConfig is always present before any Job is submitted or any
	// status transition fires. Idempotent — no-op if it already exists.
	if preExistingRC == nil {
		if err := r.ensureBootstrapRunnerConfig(ctx, tc); err != nil {
			if errors.Is(err, errTalosVersionRequired) {
				// PhaseFailed condition already written to tc by ensureBootstrapRunnerConfig.
				// Do not requeue — operator waits for the spec to be corrected.
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, fmt.Errorf("reconcileDirectBootstrap: ensure RunnerConfig: %w", err)
		}
	}

	// Import mode — the cluster already exists and is being brought under Seam
	// governance. Never submit a bootstrap Job. RunnerConfig is ensured above so
	// Conductor can attach to the cluster.
	//
	// Namespace-first ordering: for role=tenant clusters, seam-tenant-{cluster} does
	// not pre-exist (compiler no longer emits it; CP-INV-004). Create it before
	// reading the talosconfig Secret so the admin can apply secrets to the namespace
	// after the TalosCluster CR is admitted. platform-schema.md §9.
	//
	// Admin apply order for import clusters: TalosCluster CR first; once the
	// seam-tenant-{cluster} namespace appears, apply the talosconfig Secret there.
	// Platform requeues with KubeconfigUnavailable until the talosconfig is present.
	// platform-schema.md §5 TalosClusterModeImport.
	if tc.Spec.Mode == platformv1alpha1.TalosClusterModeImport {
		tc.Status.Origin = platformv1alpha1.TalosClusterOriginImported

		// Read talosconfig Secret from seam-tenant-{cluster} and generate the kubeconfig.
		// The bootstrap bundle includes a seam-tenant-namespace.yaml so the admin can
		// apply it (and the Secrets) before the TalosCluster CR in a single apply run.
		// For mode=import the namespace is always pre-existing when this reconcile fires.
		// platform-schema.md §9.
		if result, err := r.ensureKubeconfigSecret(ctx, tc); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcileDirectBootstrap: import kubeconfig: %w", err)
		} else if result.RequeueAfter > 0 {
			// talosconfig Secret absent — condition already set, status will be patched by caller.
			return result, nil
		}

		// Ensure per-node MachineConfigSync CRs exist for all admin-provided MachineConfig CRs.
		// Non-fatal: admin or compiler must create MachineConfig CRs before sync can run.
		if mcErr := r.ensureMachineConfigCRsExist(ctx, tc); mcErr != nil {
			logger.Info("ensureMachineConfigCRsExist: partial or full failure (non-fatal, import proceeds)",
				"name", tc.Name, "error", mcErr.Error())
		}

		// Detect MachineConfig CR generation changes and trigger watch-triggered sync CRs.
		// Non-fatal: watch may not be delivering a change on this reconcile pass.
		if mcErr := r.reconcileMachineConfigSync(ctx, tc); mcErr != nil {
			logger.Info("reconcileMachineConfigSync: error detecting MC generation changes (non-fatal)",
				"name", tc.Name, "error", mcErr.Error())
		}

		// Delete import MachineConfigSync CRs whose sync CRs are READY=True.
		// Non-fatal: import CRs are cosmetic after initial sync completes.
		if mcErr := r.pruneImportMachineConfigSyncCRs(ctx, tc); mcErr != nil {
			logger.Info("pruneImportMachineConfigSyncCRs: error pruning import CRs (non-fatal)",
				"name", tc.Name, "error", mcErr.Error())
		}

		// Role=tenant on the direct path: register the cluster for RBAC and pack delivery.
		// CP-INV-004 amendment (Governor 2026-05-31): namespace is created by admin/compiler.
		if tc.Spec.Role == platformv1alpha1.TalosClusterRoleTenant {
			// WS1: register the tenant cluster in guardian RBAC policy/profiles and
			// create the Kueue LocalQueue so pack deployments can be admitted.
			if err := r.ensureTenantOnboarding(ctx, tc); err != nil {
				return ctrl.Result{}, fmt.Errorf("reconcileDirectBootstrap: tenant onboarding: %w", err)
			}

			// Management-side onboarding complete. Mark Bootstrapped and drive the
			// conductor state machine. Ready=True is gated on ConductorReady=True.
			// platform-schema.md §12, guardian-schema.md §20.
			platformv1alpha1.SetCondition(
				&tc.Status.Conditions,
				platformv1alpha1.ConditionTypeBootstrapped,
				metav1.ConditionTrue,
				platformv1alpha1.ReasonImportComplete,
				"Tenant cluster import: RunnerConfig created, kubeconfig generated, tenant namespace provisioned.",
				tc.Generation,
			)
			return r.ensureConductorReadyAndTransition(ctx, tc)
		}

		// Management cluster import: Conductor (role=management) is already deployed
		// by compiler enable. No remote conductor deployment needed. Transition directly.
		if err := r.ensureManagementOnboarding(ctx, tc); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcileDirectBootstrap: management onboarding (import): %w", err)
		}
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeBootstrapped,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonImportComplete,
			"Management cluster import: RunnerConfig created, kubeconfig generated, cluster adopted under Seam governance without bootstrap Job.",
			tc.Generation,
		)
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeReady,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonClusterReady,
			"Management cluster imported and Ready.",
			tc.Generation,
		)
		logger.Info("management cluster import complete -- transitioning to Ready", "name", tc.Name)
		return ctrl.Result{}, nil
	}

	// Check for an existing bootstrap Job for this TalosCluster.
	jobName := bootstrapJobName(tc.Name)
	existingJob, err := r.getBootstrapJob(ctx, tc.Namespace, jobName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcileDirectBootstrap: check bootstrap job: %w", err)
	}

	if existingJob == nil {
		// Part 1 + Part 3 — idempotency guard: RunnerConfig pre-existed and no Job
		// was ever submitted. The cluster was already running when this CR was applied.
		// Acknowledge it as operational without submitting a bootstrap Job.
		if preExistingRC != nil {
			tc.Status.Origin = platformv1alpha1.TalosClusterOriginBootstrapped
			platformv1alpha1.SetCondition(
				&tc.Status.Conditions,
				platformv1alpha1.ConditionTypeBootstrapped,
				metav1.ConditionTrue,
				platformv1alpha1.ReasonBootstrapJobComplete,
				"Management cluster was already running when TalosCluster CR was applied. No bootstrap Job required.",
				tc.Generation,
			)
			platformv1alpha1.SetCondition(
				&tc.Status.Conditions,
				platformv1alpha1.ConditionTypeReady,
				metav1.ConditionTrue,
				platformv1alpha1.ReasonClusterReady,
				"Management cluster acknowledged as operational.",
				tc.Generation,
			)
			// WS2: register management cluster in the platform RBAC policy.
			if err := r.ensureManagementOnboarding(ctx, tc); err != nil {
				return ctrl.Result{}, fmt.Errorf("reconcileDirectBootstrap: management onboarding (pre-existing): %w", err)
			}
			logger.Info("management cluster pre-existing — transitioning to Ready without bootstrap Job",
				"name", tc.Name)
			return ctrl.Result{}, nil
		}

		// No pre-existing RunnerConfig and no Job — fresh bootstrap. Submit Job now.
		if err := r.submitBootstrapJob(ctx, tc, jobName); err != nil {
			platformv1alpha1.SetCondition(
				&tc.Status.Conditions,
				platformv1alpha1.ConditionTypeBootstrapped,
				metav1.ConditionFalse,
				platformv1alpha1.ReasonBootstrapJobFailed,
				fmt.Sprintf("Failed to submit bootstrap Job: %v", err),
				tc.Generation,
			)
			return ctrl.Result{}, fmt.Errorf("reconcileDirectBootstrap: submit bootstrap job: %w", err)
		}
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeBootstrapped,
			metav1.ConditionFalse,
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
	complete, failed, result := r.readOperationResult(ctx, tc.Name, jobName)
	if failed {
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeBootstrapped,
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
		platformv1alpha1.ConditionTypeBootstrapped,
		metav1.ConditionTrue,
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
	// WS2: register management cluster in the platform RBAC policy.
	if err := r.ensureManagementOnboarding(ctx, tc); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcileDirectBootstrap: management onboarding (bootstrap complete): %w", err)
	}
	logger.Info("management cluster bootstrap complete, cluster Ready",
		"name", tc.Name)
	return ctrl.Result{}, nil
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

	done, err := r.EnsureRemoteConductorBootstrap(ctx, tc)
	if err != nil {
		logger.Error(err, "failed to complete conductor bootstrap window on target cluster",
			"cluster", tc.Name)
		return ctrl.Result{RequeueAfter: capiPollInterval},
			fmt.Errorf("ensureConductorReadyAndTransition: %w", err)
	}

	if !done {
		// Bootstrap window items not yet complete — kubeconfig pending or items in progress.
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeConductorReady,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonConductorBootstrapPending,
			"Conductor bootstrap window setup in progress (namespace, RBAC, InfrastructureTalosCluster copy). Requeuing.",
			tc.Generation,
		)
		logger.Info("conductor bootstrap window pending — requeueing", "cluster", tc.Name)
		return ctrl.Result{RequeueAfter: capiPollInterval}, nil
	}

	// Bootstrap window complete — set ConductorReady=True, then transition cluster to Ready.
	platformv1alpha1.SetCondition(
		&tc.Status.Conditions,
		platformv1alpha1.ConditionTypeConductorReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonConductorBootstrapComplete,
		"Conductor bootstrap window complete: ont-system namespace, RBAC, and InfrastructureTalosCluster copy established on the target cluster.",
		tc.Generation,
	)
	r.transitionToReady(tc)
	logger.Info("TalosCluster Ready — conductor bootstrap window complete",
		"name", tc.Name)
	return ctrl.Result{}, nil
}

// transitionToReady sets the Ready and CiliumPending conditions for the final Ready state.
// Origin is set by each calling path before invoking this function.
func (r *TalosClusterReconciler) transitionToReady(tc *platformv1alpha1.TalosCluster) {
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
		"Cluster Ready: Cilium up, all nodes Ready.",
		tc.Generation,
	)
}

// SetupWithManager registers TalosClusterReconciler with the controller-runtime
// manager. platform-design.md §2.1.
//
// RECON-A6: Watches machineconfig Secrets (labeled platform.ontai.dev/mc-class) and
// maps them to TalosCluster reconcile requests via machineConfigSecretToTalosCluster.
// This ensures that admin edits to machineconfig Secrets trigger reconcileMachineConfigSync
// without requiring a TalosCluster spec change.
func (r *TalosClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.TalosCluster{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.machineConfigSecretToTalosCluster),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				_, hasMCClass := obj.GetLabels()[LabelMachineConfigClass]
				return hasMCClass
			})),
		).
		Complete(r)
}

// machineConfigSecretToTalosCluster maps a machineconfig Secret event to the
// TalosCluster reconcile request for that cluster. The Secret must carry
// LabelMachineConfigCluster to identify its owning cluster. RECON-A6.
func (r *TalosClusterReconciler) machineConfigSecretToTalosCluster(
	_ context.Context, obj client.Object,
) []reconcile.Request {
	clusterName := obj.GetLabels()[LabelMachineConfigCluster]
	if clusterName == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: clusterName, Namespace: "seam-system"},
	}}
}
