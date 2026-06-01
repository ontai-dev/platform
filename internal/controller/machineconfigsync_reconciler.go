package controller

// MachineConfigSyncReconciler reconciles MachineConfigSync CRs.
//
// Pattern: read the source-of-truth MachineConfig CR from seam-tenant-{cluster},
// compute content hash, gate on machineconfig-sync capability in the cluster RunnerConfig,
// submit a Conductor executor Job, poll OperationResult for completion,
// then record ObservedHash on the MachineConfigSync status. platform-schema.md §15.
//
// Named Conductor capability: machineconfig-sync. RECON-A5.
//
// CP-INV-003: RunnerConfig is generated at runtime, never hand-coded.
// CP-INV-010: Kueue is NOT used. Jobs submitted directly.
// INV-018: gate failures are permanent -- backoffLimit=0, no retries.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"

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

// capabilityMachineConfigSync is the Conductor capability name for machineconfig apply.
// Must match CapabilityMachineConfigSync in conductor-sdk/runnerlib/constants.go.
const capabilityMachineConfigSync = "machineconfig-sync"

const (
	// envMCNodeClass is the env var key injected into the machineconfig-sync executor Job.
	envMCNodeClass = "MC_NODE_CLASS"

	// envMCForceApply controls whether the hash-equality check is skipped.
	envMCForceApply = "MC_FORCE_APPLY"

	// envMCNodeIP is the env var key injected when MachineConfigSync.spec.nodeRef is set.
	// When present in the executor Job, the conductor applies the machineconfig to only
	// this specific node IP rather than all nodes in the cluster talosconfig.
	// PLT-BUG-3-ARCH.
	envMCNodeIP = "MC_NODE_IP"
)

// MachineConfigSyncReconciler reconciles MachineConfigSync objects.
type MachineConfigSyncReconciler struct {
	Client    client.Client
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Recorder  clientevents.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=machineconfigsyncs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=machineconfigsyncs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=machineconfigsyncs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructurerunnerconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructuretalosclusteroperationresults,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=machineconfigs,verbs=get;list;watch

func (r *MachineConfigSyncReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	mcs := &platformv1alpha1.MachineConfigSync{}
	if err := r.Client.Get(ctx, req.NamespacedName, mcs); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get MachineConfigSync %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(mcs.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, mcs, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch MachineConfigSync status",
					"name", mcs.Name, "namespace", mcs.Namespace)
			}
		}
	}()

	mcs.Status.ObservedGeneration = mcs.Generation

	// Initialize LineageSynced on first observation.
	if platformv1alpha1.FindCondition(mcs.Status.Conditions, platformv1alpha1.ConditionTypeMachineConfigSyncLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&mcs.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigSyncLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			mcs.Generation,
		)
	}

	// If permanently failed, do not re-submit jobs. Human intervention resolves this by
	// deleting the CR; the TalosCluster reconciler will create a new one on next reconcile.
	degradedCond := platformv1alpha1.FindCondition(mcs.Status.Conditions, platformv1alpha1.ConditionTypeMachineConfigSyncDegraded)
	if degradedCond != nil && degradedCond.Status == metav1.ConditionTrue &&
		degradedCond.Reason == platformv1alpha1.ReasonMachineConfigSyncPermanentFailure {
		return ctrl.Result{}, nil
	}

	// If already complete, self-delete after the day-2 TTL.
	readyCond := platformv1alpha1.FindCondition(mcs.Status.Conditions, platformv1alpha1.ConditionTypeMachineConfigSyncReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		if expired, after := day2TTLExpired(readyCond.LastTransitionTime.Time); expired {
			_ = r.Client.Delete(ctx, mcs)
			return ctrl.Result{}, nil
		} else {
			return ctrl.Result{RequeueAfter: after}, nil
		}
	}

	clusterRef := mcs.Spec.ClusterRef.Name
	nodeClass := mcs.Spec.NodeClass

	// Read the source-of-truth MachineConfig CR from seam-tenant-{clusterRef}.
	mcCRName := MachineConfigCRName(clusterRef, nodeClass)
	mcNS := tenantNS(clusterRef)
	mc := &platformv1alpha1.MachineConfig{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: mcCRName, Namespace: mcNS}, mc); err != nil {
		if apierrors.IsNotFound(err) {
			platformv1alpha1.SetCondition(
				&mcs.Status.Conditions,
				platformv1alpha1.ConditionTypeMachineConfigSyncDegraded,
				metav1.ConditionTrue,
				platformv1alpha1.ReasonMachineConfigSyncJobFailed,
				fmt.Sprintf("MachineConfig CR %s/%s not found. Admin must apply MachineConfig CRs before triggering sync.", mcNS, mcCRName),
				mcs.Generation,
			)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get MachineConfig CR %s/%s: %w", mcNS, mcCRName, err)
	}

	// Compute SHA-256 content hash over the raw JSON of machine and cluster sections.
	var machineRaw, clusterRaw []byte
	if mc.Spec.Machine != nil {
		machineRaw = mc.Spec.Machine.Raw
	}
	if mc.Spec.Cluster != nil {
		clusterRaw = mc.Spec.Cluster.Raw
	}
	sum := sha256.Sum256(append(machineRaw, clusterRaw...))
	contentHash := fmt.Sprintf("%x", sum)

	// Hash-equality check: skip Job if content matches last confirmed sync and forceApply=false.
	if !mcs.Spec.ForceApply && mcs.Status.ObservedHash == contentHash {
		platformv1alpha1.SetCondition(
			&mcs.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigSyncReady,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMachineConfigSyncHashMatch,
			"MachineConfig content hash matches last confirmed sync. No apply needed.",
			mcs.Generation,
		)
		mcs.Status.ObservedHash = contentHash
		logger.Info("MachineConfigSync skipped: hash match",
			"name", mcs.Name, "hash", contentHash)
		return ctrl.Result{}, nil
	}

	// Gate: read cluster RunnerConfig and verify machineconfig-sync capability.
	clusterRC, err := getClusterRunnerConfig(ctx, r.Client, clusterRef)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("MachineConfigSyncReconciler: get cluster RunnerConfig: %w", err)
	}
	if clusterRC == nil {
		platformv1alpha1.SetCondition(
			&mcs.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonRunnerConfigNotFound,
			"Cluster RunnerConfig not yet present in ont-system. Waiting for Conductor agent.",
			mcs.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	if !hasCapability(clusterRC, capabilityMachineConfigSync) {
		platformv1alpha1.SetCondition(
			&mcs.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonCapabilityNotPublished,
			fmt.Sprintf("Capability %q not yet published by Conductor agent.", capabilityMachineConfigSync),
			mcs.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	platformv1alpha1.SetCondition(
		&mcs.Status.Conditions,
		platformv1alpha1.ConditionTypeCapabilityUnavailable,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonCapabilityNotPublished,
		"",
		mcs.Generation,
	)

	jobName := retryJobName(mcs.Name, capabilityMachineConfigSync, mcs.Status.RetryCount)

	existingJob, err := getOperationalJob(ctx, r.Client, mcs.Namespace, jobName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("MachineConfigSyncReconciler: check job: %w", err)
	}

	if existingJob == nil {
		leaderNode, lErr := resolveOperatorLeaderNode(ctx, r.Client, r.APIReader)
		if lErr != nil {
			return ctrl.Result{}, fmt.Errorf("MachineConfigSyncReconciler: resolve leader node: %w", lErr)
		}
		nodeExclusions := buildNodeExclusions(nil, leaderNode)

		job := jobSpecWithExclusions(jobName, mcs.Namespace, clusterRef, capabilityMachineConfigSync, nodeExclusions, clusterRC.Spec.RunnerImage)
		appendMCSyncEnvVars(job, nodeClass, mcs.Spec.NodeRef, mcs.Spec.ForceApply)

		if err := controllerutil.SetControllerReference(mcs, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("MachineConfigSyncReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("MachineConfigSyncReconciler: create job: %w", err)
		}
		mcs.Status.JobName = jobName
		platformv1alpha1.SetCondition(
			&mcs.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigSyncRunning,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMachineConfigSyncJobSubmitted,
			fmt.Sprintf("Conductor executor Job %s submitted.", jobName),
			mcs.Generation,
		)
		r.Recorder.Eventf(mcs, nil, "Normal", "JobSubmitted", "JobSubmitted",
			"Submitted Conductor executor Job %s for machineconfig-sync nodeClass=%s", jobName, nodeClass)
		logger.Info("submitted Conductor executor Job",
			"name", mcs.Name, "jobName", jobName, "nodeClass", nodeClass)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job exists -- poll OperationResult.
	complete, failed, result := readOperationRecord(ctx, r.Client, clusterRef, jobName)
	if failed {
		mcs.Status.RetryCount++
		mcs.Status.OperationResult = result
		platformv1alpha1.SetCondition(
			&mcs.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigSyncRunning,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonMachineConfigSyncJobFailed,
			"Job failed.",
			mcs.Generation,
		)
		if mcs.Status.RetryCount >= effectiveMaxRetry(mcs.Spec.MaxRetry) {
			msg := fmt.Sprintf("Conductor executor Job %s failed after %d attempts: %s. Human intervention required.", jobName, mcs.Status.RetryCount, result)
			platformv1alpha1.SetCondition(
				&mcs.Status.Conditions,
				platformv1alpha1.ConditionTypeMachineConfigSyncDegraded,
				metav1.ConditionTrue,
				platformv1alpha1.ReasonMachineConfigSyncPermanentFailure,
				msg,
				mcs.Generation,
			)
			r.Recorder.Eventf(mcs, nil, "Warning", "PermanentFailure", "PermanentFailure", "%s", msg)
			clusterNS := mcs.Spec.ClusterRef.Namespace
			if clusterNS == "" {
				clusterNS = mcs.Namespace
			}
			_ = setTalosClusterHumanInterventionRequired(ctx, r.Client, clusterRef, clusterNS,
				fmt.Sprintf("MachineConfigSync %s/%s permanently failed after %d attempts.", mcs.Namespace, mcs.Name, mcs.Status.RetryCount),
				mcs.Generation)
			return ctrl.Result{}, nil
		}
		platformv1alpha1.SetCondition(
			&mcs.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigSyncDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMachineConfigSyncJobFailed,
			fmt.Sprintf("Conductor executor Job %s failed (attempt %d/%d): %s. Retrying.", jobName, mcs.Status.RetryCount, effectiveMaxRetry(mcs.Spec.MaxRetry), result),
			mcs.Generation,
		)
		r.Recorder.Eventf(mcs, nil, "Warning", "JobFailed", "JobFailed",
			"Conductor executor Job %s failed (attempt %d/%d): %s", jobName, mcs.Status.RetryCount, effectiveMaxRetry(mcs.Spec.MaxRetry), result)
		return ctrl.Result{RequeueAfter: retryJobRetryInterval}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job complete -- update MachineConfigSync status.
	mcs.Status.RetryCount = 0
	mcs.Status.OperationResult = result
	mcs.Status.ObservedHash = contentHash
	platformv1alpha1.SetCondition(
		&mcs.Status.Conditions,
		platformv1alpha1.ConditionTypeMachineConfigSyncRunning,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonMachineConfigSyncJobComplete,
		"Job completed.",
		mcs.Generation,
	)
	platformv1alpha1.SetCondition(
		&mcs.Status.Conditions,
		platformv1alpha1.ConditionTypeMachineConfigSyncReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonMachineConfigSyncJobComplete,
		fmt.Sprintf("Conductor executor Job %s completed successfully. Hash: %s.", jobName, contentHash),
		mcs.Generation,
	)
	r.Recorder.Eventf(mcs, nil, "Normal", "JobComplete", "JobComplete",
		"Conductor executor Job %s completed successfully", jobName)
	logger.Info("MachineConfigSync complete",
		"name", mcs.Name, "nodeClass", nodeClass, "hash", contentHash)
	return ctrl.Result{}, nil
}

// appendMCSyncEnvVars injects MC_NODE_CLASS, MC_FORCE_APPLY, and optionally MC_NODE_IP
// env vars into the executor Job's first container. nodeRef is the value for MC_NODE_IP
// from spec.nodeRef; when empty no MC_NODE_IP var is injected and the conductor applies
// to all nodes in the cluster talosconfig (default). PLT-BUG-3-ARCH.
func appendMCSyncEnvVars(job *batchv1.Job, nodeClass, nodeRef string, forceApply bool) {
	envVars := []corev1.EnvVar{
		{Name: envMCNodeClass, Value: nodeClass},
		{Name: envMCForceApply, Value: strconv.FormatBool(forceApply)},
	}
	if nodeRef != "" {
		envVars = append(envVars, corev1.EnvVar{Name: envMCNodeIP, Value: nodeRef})
	}
	job.Spec.Template.Spec.Containers[0].Env = append(
		job.Spec.Template.Spec.Containers[0].Env,
		envVars...,
	)
}

// SetupWithManager registers MachineConfigSyncReconciler with the manager.
func (r *MachineConfigSyncReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.MachineConfigSync{}).
		Complete(r)
}
