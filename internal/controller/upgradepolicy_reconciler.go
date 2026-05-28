package controller

// UpgradePolicyReconciler reconciles UpgradePolicy CRs. Submits a Conductor executor
// Job for talos-upgrade, kube-upgrade, or stack-upgrade.
//
// Named Conductor capabilities: talos-upgrade, kube-upgrade, stack-upgrade.
// platform-schema.md §5 UpgradePolicy. platform-design.md §2.1.

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
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

const (
	capabilityTalosUpgrade = "talos-upgrade"
	capabilityKubeUpgrade  = "kube-upgrade"
	capabilityStackUpgrade = "stack-upgrade"
)

// UpgradePolicyReconciler reconciles UpgradePolicy objects.
type UpgradePolicyReconciler struct {
	Client    client.Client
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Recorder  clientevents.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=upgradepolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=upgradepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=upgradepolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructuretalosclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructuretalosclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructuretalosclusteroperationresults,verbs=get;list;watch

func (r *UpgradePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	up := &platformv1alpha1.UpgradePolicy{}
	if err := r.Client.Get(ctx, req.NamespacedName, up); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get UpgradePolicy %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(up.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, up, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch UpgradePolicy status",
					"name", up.Name, "namespace", up.Namespace)
			}
		}
	}()

	up.Status.ObservedGeneration = up.Generation

	// Initialize LineageSynced on first observation — one-time write.
	if platformv1alpha1.FindCondition(up.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&up.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			up.Generation,
		)
	}

	// If already complete, do nothing.
	readyCond := platformv1alpha1.FindCondition(up.Status.Conditions, platformv1alpha1.ConditionTypeUpgradePolicyReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		return ctrl.Result{}, nil
	}

	return r.reconcileDirectUpgrade(ctx, up)
}

// reconcileDirectUpgrade gates on capability then submits a single batch/v1
// Conductor executor Job. conductor-schema.md §5 §17.
func (r *UpgradePolicyReconciler) reconcileDirectUpgrade(ctx context.Context, up *platformv1alpha1.UpgradePolicy) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	capability, err := upgradeCapability(up.Spec.UpgradeType)
	if err != nil {
		platformv1alpha1.SetCondition(
			&up.Status.Conditions,
			platformv1alpha1.ConditionTypeUpgradePolicyDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonUpgradeJobFailed,
			fmt.Sprintf("unknown upgradeType %q: %v", up.Spec.UpgradeType, err),
			up.Generation,
		)
		return ctrl.Result{}, nil
	}

	// Gate: read the cluster RunnerConfig from ont-system and verify capability.
	clusterRC, err := getClusterRunnerConfig(ctx, r.Client, up.Spec.ClusterRef.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("UpgradePolicyReconciler: get cluster RunnerConfig: %w", err)
	}
	if clusterRC == nil {
		platformv1alpha1.SetCondition(
			&up.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonRunnerConfigNotFound,
			"Cluster RunnerConfig not yet present in ont-system. Waiting for Conductor agent.",
			up.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	if !hasCapability(clusterRC, capability) {
		platformv1alpha1.SetCondition(
			&up.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonCapabilityNotPublished,
			fmt.Sprintf("Capability %q not yet published by Conductor agent.", capability),
			up.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	platformv1alpha1.SetCondition(
		&up.Status.Conditions,
		platformv1alpha1.ConditionTypeCapabilityUnavailable,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonCapabilityNotPublished,
		"",
		up.Generation,
	)

	jobName := retryJobName(up.Name, capability, up.Status.RetryCount)

	existingJob, err := getOperationalJob(ctx, r.Client, up.Namespace, jobName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("UpgradePolicyReconciler: check job: %w", err)
	}

	if existingJob == nil {
		leaderNode, lErr := resolveOperatorLeaderNode(ctx, r.Client, r.APIReader)
		if lErr != nil {
			return ctrl.Result{}, fmt.Errorf("UpgradePolicyReconciler: resolve leader node: %w", lErr)
		}
		nodeExclusions := buildNodeExclusions(nil, leaderNode)

		job := jobSpecWithExclusions(jobName, up.Namespace, up.Spec.ClusterRef.Name, capability, nodeExclusions, clusterRC.Spec.RunnerImage)
		// RECON-J2, RECON-J7: mount target cluster kubeconfig for drain and node-ready checks.
		addKubeconfigMount(job, up.Spec.ClusterRef.Name)

		// For management cluster upgrades: pass LEADER_NODE so Conductor upgrades
		// the leader last and performs lease handover before its node reboots.
		// Conductor's talos-upgrade capability uses this to sequence the rolling upgrade.
		// Lease release is a stub until the full conductor-side implementation lands.
		if leaderNode != "" {
			isManagement, mErr := r.isManagementCluster(ctx, up)
			if mErr != nil {
				return ctrl.Result{}, fmt.Errorf("UpgradePolicyReconciler: check cluster role: %w", mErr)
			}
			if isManagement {
				addLeaderNodeEnv(job, leaderNode)
				logger.Info("management cluster upgrade: passing LEADER_NODE for sequenced reboot",
					"cluster", up.Spec.ClusterRef.Name, "leaderNode", leaderNode)
			}
		}

		if err := controllerutil.SetControllerReference(up, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("UpgradePolicyReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("UpgradePolicyReconciler: create job: %w", err)
		}
		up.Status.JobName = jobName
		platformv1alpha1.SetCondition(
			&up.Status.Conditions,
			platformv1alpha1.ConditionTypeUpgradePolicyReady,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonUpgradeJobSubmitted,
			fmt.Sprintf("Conductor executor Job %s submitted for %s.", jobName, capability),
			up.Generation,
		)
		r.Recorder.Eventf(up, nil, "Normal", "JobSubmitted", "JobSubmitted",
			"Submitted Conductor executor Job %s for %s", jobName, capability)
		logger.Info("submitted upgrade Conductor executor Job",
			"name", up.Name, "jobName", jobName, "upgradeType", up.Spec.UpgradeType)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job exists — check OperationResult ConfigMap.
	complete, failed, result := readOperationRecord(ctx, r.Client, up.Spec.ClusterRef.Name, jobName)
	if failed {
		up.Status.RetryCount++
		up.Status.OperationResult = result
		if up.Status.RetryCount >= effectiveMaxRetry(up.Spec.MaxRetry) {
			msg := fmt.Sprintf("Conductor executor Job %s failed after %d attempts: %s. Human intervention required.", jobName, up.Status.RetryCount, result)
			platformv1alpha1.SetCondition(
				&up.Status.Conditions,
				platformv1alpha1.ConditionTypeUpgradePolicyDegraded,
				metav1.ConditionTrue,
				platformv1alpha1.ReasonUpgradePermanentFailure,
				msg,
				up.Generation,
			)
			r.Recorder.Eventf(up, nil, "Warning", "PermanentFailure", "PermanentFailure", "%s", msg)
			clusterNS := up.Spec.ClusterRef.Namespace
			if clusterNS == "" {
				clusterNS = up.Namespace
			}
			_ = setTalosClusterHumanInterventionRequired(ctx, r.Client, up.Spec.ClusterRef.Name, clusterNS,
				fmt.Sprintf("UpgradePolicy %s/%s permanently failed after %d attempts.", up.Namespace, up.Name, up.Status.RetryCount),
				up.Generation)
			return ctrl.Result{}, nil
		}
		platformv1alpha1.SetCondition(
			&up.Status.Conditions,
			platformv1alpha1.ConditionTypeUpgradePolicyDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonUpgradeJobFailed,
			fmt.Sprintf("Conductor executor Job %s failed (attempt %d/%d): %s. Retrying.", jobName, up.Status.RetryCount, effectiveMaxRetry(up.Spec.MaxRetry), result),
			up.Generation,
		)
		r.Recorder.Eventf(up, nil, "Warning", "JobFailed", "JobFailed",
			"Conductor executor Job %s failed (attempt %d/%d): %s", jobName, up.Status.RetryCount, effectiveMaxRetry(up.Spec.MaxRetry), result)
		return ctrl.Result{RequeueAfter: retryJobRetryInterval}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	up.Status.RetryCount = 0
	up.Status.OperationResult = result
	platformv1alpha1.SetCondition(
		&up.Status.Conditions,
		platformv1alpha1.ConditionTypeUpgradePolicyReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonUpgradeJobComplete,
		fmt.Sprintf("Conductor executor Job %s completed successfully.", jobName),
		up.Generation,
	)
	r.Recorder.Eventf(up, nil, "Normal", "JobComplete", "JobComplete",
		"Conductor executor Job %s completed successfully", jobName)
	logger.Info("UpgradePolicy complete", "name", up.Name, "upgradeType", up.Spec.UpgradeType)

	// After a successful talos or stack upgrade, record the new Talos version in
	// InfrastructureTalosCluster.status.observedTalosVersion. This prevents spec
	// regressions and allows the reconciler to detect version changes.
	if up.Spec.TargetTalosVersion != "" &&
		(up.Spec.UpgradeType == platformv1alpha1.UpgradeTypeTalos ||
			up.Spec.UpgradeType == platformv1alpha1.UpgradeTypeStack) {
		if pErr := r.patchObservedTalosVersion(ctx, up, up.Spec.TargetTalosVersion); pErr != nil {
			logger.Error(pErr, "failed to patch InfrastructureTalosCluster observedTalosVersion",
				"cluster", up.Spec.ClusterRef.Name, "version", up.Spec.TargetTalosVersion)
		}
		// Advance the TCOR to a new revision epoch for the new talosVersion.
		// Archives N-1 operations to GraphQuery DB stub and clears the operations map.
		if bumpErr := bumpTCORRevision(ctx, r.Client, up.Spec.ClusterRef.Name, up.Spec.TargetTalosVersion); bumpErr != nil {
			logger.Error(bumpErr, "failed to bump TCOR revision",
				"cluster", up.Spec.ClusterRef.Name, "version", up.Spec.TargetTalosVersion)
		}
	}
	return ctrl.Result{}, nil
}

// upgradeCapability maps an UpgradeType to a single Conductor capability name.
// stack-upgrade is a named compound capability in conductor — the executor handles
// talos→kube sequencing internally. conductor-schema.md §6.
func upgradeCapability(ut platformv1alpha1.UpgradeType) (string, error) {
	switch ut {
	case platformv1alpha1.UpgradeTypeTalos:
		return capabilityTalosUpgrade, nil
	case platformv1alpha1.UpgradeTypeKubernetes:
		return capabilityKubeUpgrade, nil
	case platformv1alpha1.UpgradeTypeStack:
		return capabilityStackUpgrade, nil
	default:
		return "", fmt.Errorf("unknown UpgradeType %q", ut)
	}
}

// patchObservedTalosVersion patches InfrastructureTalosCluster.status.observedTalosVersion
// to the given version after a successful talos or stack upgrade. The TalosCluster
// reconciler uses this to prevent spec.talosVersion from regressing below the current
// running version. seam-core-schema.md §TBD.
func (r *UpgradePolicyReconciler) patchObservedTalosVersion(ctx context.Context, up *platformv1alpha1.UpgradePolicy, version string) error {
	clusterNS := up.Spec.ClusterRef.Namespace
	if clusterNS == "" {
		clusterNS = up.Namespace
	}
	tc := &platformv1alpha1.TalosCluster{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      up.Spec.ClusterRef.Name,
		Namespace: clusterNS,
	}, tc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("patchObservedTalosVersion: get TalosCluster: %w", err)
	}
	patch := client.MergeFrom(tc.DeepCopy())
	tc.Status.ObservedTalosVersion = version
	return r.Client.Status().Patch(ctx, tc, patch)
}

// isManagementCluster returns true when the cluster referenced by the UpgradePolicy
// has role=management. Used to decide whether to pass LEADER_NODE to the upgrade Job.
func (r *UpgradePolicyReconciler) isManagementCluster(ctx context.Context, up *platformv1alpha1.UpgradePolicy) (bool, error) {
	clusterNS := up.Spec.ClusterRef.Namespace
	if clusterNS == "" {
		clusterNS = up.Namespace
	}
	tc := &platformv1alpha1.TalosCluster{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      up.Spec.ClusterRef.Name,
		Namespace: clusterNS,
	}, tc); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("isManagementCluster: get TalosCluster: %w", err)
	}
	return tc.Spec.Role == platformv1alpha1.TalosClusterRoleManagement, nil
}

// addLeaderNodeEnv appends LEADER_NODE to the first container's env of a Job.
// Conductor's talos-upgrade capability uses this to sequence the rolling upgrade
// so the leader node reboots last, allowing the operator a graceful lease handover
// before its node becomes unavailable. Conductor-side sequencing is a stub until
// the full talos-upgrade capability implements the LEADER_NODE path.
func addLeaderNodeEnv(job *batchv1.Job, leaderNode string) {
	if len(job.Spec.Template.Spec.Containers) == 0 {
		return
	}
	job.Spec.Template.Spec.Containers[0].Env = append(
		job.Spec.Template.Spec.Containers[0].Env,
		corev1.EnvVar{Name: "LEADER_NODE", Value: leaderNode},
	)
}

// SetupWithManager registers UpgradePolicyReconciler with the manager.
func (r *UpgradePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.UpgradePolicy{}).
		Complete(r)
}
