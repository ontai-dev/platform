package controller

// MachineConfigSyncReconciler reconciles MachineConfigSync CRs.
//
// Pattern: read the cluster RunnerConfig from ont-system, gate on machineconfig-sync
// capability, submit a Conductor executor Job, poll OperationResult for completion,
// then update the source-of-truth Secret sync labels. platform-schema.md §15.
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
	"time"

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
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;update;patch

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

	// Read the source-of-truth machineconfig Secret from seam-tenant-{clusterRef}.
	secretName := MachineConfigSecretName(clusterRef, nodeClass)
	secretNS := tenantNS(clusterRef)
	mcSecret := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: secretName, Namespace: secretNS}, mcSecret); err != nil {
		if apierrors.IsNotFound(err) {
			platformv1alpha1.SetCondition(
				&mcs.Status.Conditions,
				platformv1alpha1.ConditionTypeMachineConfigSyncDegraded,
				metav1.ConditionTrue,
				platformv1alpha1.ReasonMachineConfigSyncJobFailed,
				fmt.Sprintf("MachineConfig Secret %s/%s not found. Create the secret with key %q before triggering sync.", secretNS, secretName, MachineConfigDataKey),
				mcs.Generation,
			)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get MachineConfig Secret %s/%s: %w", secretNS, secretName, err)
	}

	mcBytes := mcSecret.Data[MachineConfigDataKey]
	if len(mcBytes) == 0 {
		platformv1alpha1.SetCondition(
			&mcs.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigSyncDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMachineConfigSyncJobFailed,
			fmt.Sprintf("MachineConfig Secret %s/%s has no data key %q.", secretNS, secretName, MachineConfigDataKey),
			mcs.Generation,
		)
		return ctrl.Result{}, nil
	}

	// Compute SHA-256 of machineconfig content.
	sum := sha256.Sum256(mcBytes)
	contentHash := fmt.Sprintf("%x", sum)

	// Hash-equality check: skip Job if hash matches and forceApply=false.
	if !mcs.Spec.ForceApply {
		lastHash := mcSecret.Labels[LabelMachineConfigSyncHash]
		lastStatus := mcSecret.Labels[LabelMachineConfigSyncStatus]
		if lastHash == contentHash && lastStatus == MachineConfigSyncStatusSynced {
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

	jobName := operationalJobName(mcs.Name, capabilityMachineConfigSync)

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
		appendMCSyncEnvVars(job, nodeClass, mcs.Spec.ForceApply)

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
		mcs.Status.OperationResult = result
		platformv1alpha1.SetCondition(
			&mcs.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigSyncDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMachineConfigSyncJobFailed,
			fmt.Sprintf("Conductor executor Job %s failed: %s", jobName, result),
			mcs.Generation,
		)
		platformv1alpha1.SetCondition(
			&mcs.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigSyncRunning,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonMachineConfigSyncJobFailed,
			"Job failed.",
			mcs.Generation,
		)
		r.Recorder.Eventf(mcs, nil, "Warning", "JobFailed", "JobFailed",
			"Conductor executor Job %s failed: %s", jobName, result)
		return ctrl.Result{}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job complete -- update Secret sync labels and MachineConfigSync status.
	mcs.Status.OperationResult = result
	mcs.Status.ObservedHash = contentHash
	if err := r.updateSecretSyncLabels(ctx, mcSecret, contentHash); err != nil {
		return ctrl.Result{}, fmt.Errorf("MachineConfigSyncReconciler: update Secret sync labels: %w", err)
	}
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

// appendMCSyncEnvVars injects MC_NODE_CLASS and MC_FORCE_APPLY env vars into
// the executor Job's first container. Called after jobSpecWithExclusions.
func appendMCSyncEnvVars(job *batchv1.Job, nodeClass string, forceApply bool) {
	job.Spec.Template.Spec.Containers[0].Env = append(
		job.Spec.Template.Spec.Containers[0].Env,
		corev1.EnvVar{Name: envMCNodeClass, Value: nodeClass},
		corev1.EnvVar{Name: envMCForceApply, Value: strconv.FormatBool(forceApply)},
	)
}

// updateSecretSyncLabels patches the machineconfig Secret with confirmed sync labels.
// Called by the reconciler after a successful MachineConfigSync Job completion.
func (r *MachineConfigSyncReconciler) updateSecretSyncLabels(ctx context.Context, secret *corev1.Secret, contentHash string) error {
	patch := client.MergeFrom(secret.DeepCopy())
	if secret.Labels == nil {
		secret.Labels = make(map[string]string)
	}
	secret.Labels[LabelMachineConfigSyncStatus] = MachineConfigSyncStatusSynced
	secret.Labels[LabelMachineConfigSyncHash] = contentHash
	secret.Labels[LabelMachineConfigSyncedAt] = time.Now().UTC().Format(time.RFC3339)
	return r.Client.Patch(ctx, secret, patch)
}

// SetupWithManager registers MachineConfigSyncReconciler with the manager.
func (r *MachineConfigSyncReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.MachineConfigSync{}).
		Complete(r)
}
