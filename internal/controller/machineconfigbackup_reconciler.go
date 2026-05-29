package controller

// MachineConfigBackupReconciler reconciles TalosMachineConfigBackup CRs.
//
// Pattern: read the cluster RunnerConfig from ont-system, gate on capability
// availability, project S3 credentials into the job namespace, then submit a
// single batch/v1 Conductor executor Job. Watches the OperationResult ConfigMap
// for completion. conductor-schema.md §5 §17.
//
// Named Conductor capability: machineconfig-backup.
// platform-schema.md §11 TalosMachineConfigBackup.
//
// CP-INV-003: RunnerConfig is generated at runtime, never hand-coded.
// INV-018: gate failures are permanent -- backoffLimit=0, no retries.

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

// machineconfig Conductor capability name per conductor-schema.md.
const capabilityMachineConfigBackup = "machineconfig-backup"

// MachineConfigBackupReconciler reconciles TalosMachineConfigBackup objects.
type MachineConfigBackupReconciler struct {
	Client    client.Client
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Recorder  clientevents.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosmachineconfigbackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosmachineconfigbackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosmachineconfigbackups/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructurerunnerconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructuretalosclusteroperationresults,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch

func (r *MachineConfigBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	mcb := &platformv1alpha1.TalosMachineConfigBackup{}
	if err := r.Client.Get(ctx, req.NamespacedName, mcb); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get TalosMachineConfigBackup %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(mcb.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, mcb, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch TalosMachineConfigBackup status",
					"name", mcb.Name, "namespace", mcb.Namespace)
			}
		}
	}()

	mcb.Status.ObservedGeneration = mcb.Generation

	// Initialize LineageSynced on first observation -- one-time write.
	if platformv1alpha1.FindCondition(mcb.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&mcb.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			mcb.Generation,
		)
	}

	// If already complete, self-delete after the day-2 TTL; requeue until then.
	readyCond := platformv1alpha1.FindCondition(mcb.Status.Conditions, platformv1alpha1.ConditionTypeMachineConfigBackupReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		if expired, after := day2TTLExpired(readyCond.LastTransitionTime.Time); expired {
			_ = r.Client.Delete(ctx, mcb)
			return ctrl.Result{}, nil
		} else {
			return ctrl.Result{RequeueAfter: after}, nil
		}
	}

	// Gate: S3 bucket must be non-empty.
	if mcb.Spec.S3Destination.Bucket == "" {
		platformv1alpha1.SetCondition(
			&mcb.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigBackupS3Absent,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMachineConfigBackupS3Absent,
			"spec.s3Destination.bucket is required. platform-schema.md §11.",
			mcb.Generation,
		)
		return ctrl.Result{}, nil
	}

	// Gate: read the cluster RunnerConfig from ont-system and verify capability.
	clusterRC, err := getClusterRunnerConfig(ctx, r.Client, mcb.Spec.ClusterRef.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("MachineConfigBackupReconciler: get cluster RunnerConfig: %w", err)
	}
	if clusterRC == nil {
		platformv1alpha1.SetCondition(
			&mcb.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonRunnerConfigNotFound,
			"Cluster RunnerConfig not yet present in ont-system. Waiting for Conductor agent.",
			mcb.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	if !hasCapability(clusterRC, capabilityMachineConfigBackup) {
		platformv1alpha1.SetCondition(
			&mcb.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonCapabilityNotPublished,
			fmt.Sprintf("Capability %q not yet published by Conductor agent.", capabilityMachineConfigBackup),
			mcb.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	platformv1alpha1.SetCondition(
		&mcb.Status.Conditions,
		platformv1alpha1.ConditionTypeCapabilityUnavailable,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonCapabilityNotPublished,
		"",
		mcb.Generation,
	)

	jobName := operationalJobName(mcb.Name, capabilityMachineConfigBackup)

	// Check for an existing Job.
	existingJob, err := getOperationalJob(ctx, r.Client, mcb.Namespace, jobName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("MachineConfigBackupReconciler: check job: %w", err)
	}

	if existingJob == nil {
		// Resolve S3 credentials and project them into the job namespace.
		s3Name, s3NS, found, sErr := resolveS3BackupSecretRef(ctx, r.Client, mcb.Spec.S3BackupSecretRef)
		if sErr != nil {
			return ctrl.Result{}, fmt.Errorf("MachineConfigBackupReconciler: resolve S3 secret: %w", sErr)
		}
		if !found {
			platformv1alpha1.SetCondition(
				&mcb.Status.Conditions,
				platformv1alpha1.ConditionTypeMachineConfigBackupS3Absent,
				metav1.ConditionTrue,
				platformv1alpha1.ReasonMachineConfigBackupS3Absent,
				"No S3 credentials configured: spec.s3BackupSecretRef is absent and seam-etcd-backup-config Secret not found in seam-system. platform-schema.md §10.",
				mcb.Generation,
			)
			r.Recorder.Eventf(mcb, nil, "Warning", "S3CredentialsAbsent",
				"TalosMachineConfigBackup %s/%s: no S3 credentials configured", mcb.Namespace, mcb.Name)
			return ctrl.Result{}, nil
		}

		s3EnvSecretName := mcb.Name + s3EnvSecretSuffix
		if err := ensureS3EnvSecretFor(ctx, r.Client, r.Scheme, s3Name, s3NS, mcb, s3EnvSecretName, mcb.Namespace); err != nil {
			return ctrl.Result{}, fmt.Errorf("MachineConfigBackupReconciler: project S3 env secret: %w", err)
		}

		job := jobSpec(jobName, mcb.Namespace, mcb.Spec.ClusterRef.Name, capabilityMachineConfigBackup, clusterRC.Spec.RunnerImage)
		appendS3EnvFrom(job, s3EnvSecretName)
		if err := controllerutil.SetControllerReference(mcb, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("MachineConfigBackupReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("MachineConfigBackupReconciler: create job: %w", err)
		}
		mcb.Status.JobName = jobName
		platformv1alpha1.SetCondition(
			&mcb.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigBackupRunning,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMachineConfigBackupJobSubmitted,
			fmt.Sprintf("Conductor executor Job %s submitted for %s.", jobName, capabilityMachineConfigBackup),
			mcb.Generation,
		)
		r.Recorder.Eventf(mcb, nil, "Normal", "JobSubmitted", "JobSubmitted",
			"Submitted Conductor executor Job %s for machineconfig-backup", jobName)
		logger.Info("submitted Conductor executor Job",
			"name", mcb.Name, "jobName", jobName, "capability", capabilityMachineConfigBackup)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job exists -- check OperationResult ConfigMap.
	complete, failed, result := readOperationRecord(ctx, r.Client, mcb.Spec.ClusterRef.Name, jobName)
	if failed {
		mcb.Status.OperationResult = result
		platformv1alpha1.SetCondition(
			&mcb.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigBackupDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMachineConfigBackupJobFailed,
			fmt.Sprintf("Conductor executor Job %s failed: %s", jobName, result),
			mcb.Generation,
		)
		platformv1alpha1.SetCondition(
			&mcb.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigBackupRunning,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonMachineConfigBackupJobFailed,
			"Job failed.",
			mcb.Generation,
		)
		r.Recorder.Eventf(mcb, nil, "Warning", "JobFailed", "JobFailed",
			"Conductor executor Job %s failed: %s", jobName, result)
		return ctrl.Result{}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job complete.
	mcb.Status.OperationResult = result
	platformv1alpha1.SetCondition(
		&mcb.Status.Conditions,
		platformv1alpha1.ConditionTypeMachineConfigBackupRunning,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonMachineConfigBackupJobComplete,
		"Job completed.",
		mcb.Generation,
	)
	platformv1alpha1.SetCondition(
		&mcb.Status.Conditions,
		platformv1alpha1.ConditionTypeMachineConfigBackupReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonMachineConfigBackupJobComplete,
		fmt.Sprintf("Conductor executor Job %s completed successfully.", jobName),
		mcb.Generation,
	)
	r.Recorder.Eventf(mcb, nil, "Normal", "JobComplete", "JobComplete",
		"Conductor executor Job %s completed successfully", jobName)
	logger.Info("TalosMachineConfigBackup complete",
		"name", mcb.Name, "capability", capabilityMachineConfigBackup)
	return ctrl.Result{}, nil
}

// SetupWithManager registers MachineConfigBackupReconciler with the manager.
func (r *MachineConfigBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.TalosMachineConfigBackup{}).
		Complete(r)
}
