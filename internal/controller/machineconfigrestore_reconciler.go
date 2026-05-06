package controller

// MachineConfigRestoreReconciler reconciles TalosMachineConfigRestore CRs.
//
// Pattern mirrors MachineConfigBackupReconciler: read cluster RunnerConfig,
// gate on capability, project S3 credentials, submit a Conductor executor Job,
// watch OperationResult ConfigMap for completion. conductor-schema.md §5 §17.
//
// Named Conductor capability: machineconfig-restore.
// platform-schema.md §11 TalosMachineConfigRestore.
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

const capabilityMachineConfigRestore = "machineconfig-restore"

// MachineConfigRestoreReconciler reconciles TalosMachineConfigRestore objects.
type MachineConfigRestoreReconciler struct {
	Client    client.Client
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Recorder  clientevents.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosmachineconfigrestores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosmachineconfigrestores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosmachineconfigrestores/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructurerunnerconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructuretalosclusteroperationresults,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch

func (r *MachineConfigRestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	mcr := &platformv1alpha1.TalosMachineConfigRestore{}
	if err := r.Client.Get(ctx, req.NamespacedName, mcr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get TalosMachineConfigRestore %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(mcr.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, mcr, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch TalosMachineConfigRestore status",
					"name", mcr.Name, "namespace", mcr.Namespace)
			}
		}
	}()

	mcr.Status.ObservedGeneration = mcr.Generation

	// Initialize LineageSynced on first observation.
	if platformv1alpha1.FindCondition(mcr.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&mcr.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			mcr.Generation,
		)
	}

	// Already complete -- one-shot CR.
	readyCond := platformv1alpha1.FindCondition(mcr.Status.Conditions, platformv1alpha1.ConditionTypeMachineConfigRestoreReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		mcr.Status.Phase = "Succeeded"
		return ctrl.Result{}, nil
	}

	// Gate: backupTimestamp must be non-empty.
	if mcr.Spec.BackupTimestamp == "" {
		platformv1alpha1.SetCondition(
			&mcr.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigRestoreS3Absent,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMachineConfigRestoreS3Absent,
			"spec.backupTimestamp is required. platform-schema.md §11.",
			mcr.Generation,
		)
		mcr.Status.Phase = "Failed"
		return ctrl.Result{}, nil
	}

	// Gate: s3SourceBucket must be non-empty.
	if mcr.Spec.S3SourceBucket == "" {
		platformv1alpha1.SetCondition(
			&mcr.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigRestoreS3Absent,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMachineConfigRestoreS3Absent,
			"spec.s3SourceBucket is required. platform-schema.md §11.",
			mcr.Generation,
		)
		mcr.Status.Phase = "Failed"
		return ctrl.Result{}, nil
	}

	// Gate: read the cluster RunnerConfig from ont-system and verify capability.
	clusterRC, err := getClusterRunnerConfig(ctx, r.Client, mcr.Spec.ClusterRef.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("MachineConfigRestoreReconciler: get cluster RunnerConfig: %w", err)
	}
	if clusterRC == nil {
		platformv1alpha1.SetCondition(
			&mcr.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonRunnerConfigNotFound,
			"Cluster RunnerConfig not yet present in ont-system. Waiting for Conductor agent.",
			mcr.Generation,
		)
		mcr.Status.Phase = "Pending"
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	if !hasCapability(clusterRC, capabilityMachineConfigRestore) {
		platformv1alpha1.SetCondition(
			&mcr.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonCapabilityNotPublished,
			fmt.Sprintf("Capability %q not yet published by Conductor agent.", capabilityMachineConfigRestore),
			mcr.Generation,
		)
		mcr.Status.Phase = "Pending"
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	platformv1alpha1.SetCondition(
		&mcr.Status.Conditions,
		platformv1alpha1.ConditionTypeCapabilityUnavailable,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonCapabilityNotPublished,
		"",
		mcr.Generation,
	)

	jobName := operationalJobName(mcr.Name, capabilityMachineConfigRestore)

	existingJob, err := getOperationalJob(ctx, r.Client, mcr.Namespace, jobName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("MachineConfigRestoreReconciler: check job: %w", err)
	}

	if existingJob == nil {
		s3Name, s3NS, found, sErr := resolveS3BackupSecretRef(ctx, r.Client, mcr.Spec.S3BackupSecretRef)
		if sErr != nil {
			return ctrl.Result{}, fmt.Errorf("MachineConfigRestoreReconciler: resolve S3 secret: %w", sErr)
		}
		if !found {
			platformv1alpha1.SetCondition(
				&mcr.Status.Conditions,
				platformv1alpha1.ConditionTypeMachineConfigRestoreS3Absent,
				metav1.ConditionTrue,
				platformv1alpha1.ReasonMachineConfigRestoreS3Absent,
				"No S3 credentials configured: spec.s3BackupSecretRef is absent and seam-etcd-backup-config Secret not found in seam-system. platform-schema.md §10.",
				mcr.Generation,
			)
			r.Recorder.Eventf(mcr, nil, "Warning", "S3CredentialsAbsent",
				"TalosMachineConfigRestore %s/%s: no S3 credentials configured", mcr.Namespace, mcr.Name)
			mcr.Status.Phase = "Failed"
			return ctrl.Result{}, nil
		}

		s3EnvSecretName := mcr.Name + s3EnvSecretSuffix
		if err := ensureS3EnvSecretFor(ctx, r.Client, r.Scheme, s3Name, s3NS, mcr, s3EnvSecretName, mcr.Namespace); err != nil {
			return ctrl.Result{}, fmt.Errorf("MachineConfigRestoreReconciler: project S3 env secret: %w", err)
		}

		job := jobSpec(jobName, mcr.Namespace, mcr.Spec.ClusterRef.Name, capabilityMachineConfigRestore, clusterRC.Spec.RunnerImage)
		appendS3EnvFrom(job, s3EnvSecretName)
		if err := controllerutil.SetControllerReference(mcr, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("MachineConfigRestoreReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("MachineConfigRestoreReconciler: create job: %w", err)
		}
		mcr.Status.JobName = jobName
		mcr.Status.Phase = "Running"
		platformv1alpha1.SetCondition(
			&mcr.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigRestoreRunning,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMachineConfigRestoreJobSubmitted,
			fmt.Sprintf("Conductor executor Job %s submitted for %s.", jobName, capabilityMachineConfigRestore),
			mcr.Generation,
		)
		r.Recorder.Eventf(mcr, nil, "Normal", "JobSubmitted", "JobSubmitted",
			"Submitted Conductor executor Job %s for machineconfig-restore", jobName)
		logger.Info("submitted Conductor executor Job",
			"name", mcr.Name, "jobName", jobName, "capability", capabilityMachineConfigRestore)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	complete, failed, result := readOperationRecord(ctx, r.Client, mcr.Spec.ClusterRef.Name, jobName)
	if failed {
		mcr.Status.OperationResult = result
		mcr.Status.Phase = "Failed"
		platformv1alpha1.SetCondition(
			&mcr.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigRestoreDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMachineConfigRestoreJobFailed,
			fmt.Sprintf("Conductor executor Job %s failed: %s", jobName, result),
			mcr.Generation,
		)
		platformv1alpha1.SetCondition(
			&mcr.Status.Conditions,
			platformv1alpha1.ConditionTypeMachineConfigRestoreRunning,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonMachineConfigRestoreJobFailed,
			"Job failed.",
			mcr.Generation,
		)
		r.Recorder.Eventf(mcr, nil, "Warning", "JobFailed", "JobFailed",
			"Conductor executor Job %s failed: %s", jobName, result)
		return ctrl.Result{}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	mcr.Status.OperationResult = result
	mcr.Status.Phase = "Succeeded"
	platformv1alpha1.SetCondition(
		&mcr.Status.Conditions,
		platformv1alpha1.ConditionTypeMachineConfigRestoreRunning,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonMachineConfigRestoreJobComplete,
		"Job completed.",
		mcr.Generation,
	)
	platformv1alpha1.SetCondition(
		&mcr.Status.Conditions,
		platformv1alpha1.ConditionTypeMachineConfigRestoreReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonMachineConfigRestoreJobComplete,
		fmt.Sprintf("Conductor executor Job %s completed successfully.", jobName),
		mcr.Generation,
	)
	r.Recorder.Eventf(mcr, nil, "Normal", "JobComplete", "JobComplete",
		"Conductor executor Job %s completed successfully", jobName)
	logger.Info("TalosMachineConfigRestore complete",
		"name", mcr.Name, "capability", capabilityMachineConfigRestore)
	return ctrl.Result{}, nil
}

// SetupWithManager registers MachineConfigRestoreReconciler with the manager.
func (r *MachineConfigRestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.TalosMachineConfigRestore{}).
		Complete(r)
}
