package controller

// MachineConfigBackupScheduleReconciler reconciles TalosMachineConfigBackupSchedule CRs.
//
// Creates a TalosMachineConfigBackup CR each time the interval specified in
// spec.schedule elapses. The schedule field is a Go duration string (e.g. "24h").
// Returns RequeueAfter = remaining time until the next run.
// platform-schema.md §11.

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

// MachineConfigBackupScheduleReconciler reconciles TalosMachineConfigBackupSchedule objects.
type MachineConfigBackupScheduleReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosmachineconfigbackupschedules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosmachineconfigbackupschedules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosmachineconfigbackups,verbs=get;list;watch;create;update;patch;delete

func (r *MachineConfigBackupScheduleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	sched := &platformv1alpha1.TalosMachineConfigBackupSchedule{}
	if err := r.Client.Get(ctx, req.NamespacedName, sched); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get TalosMachineConfigBackupSchedule %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(sched.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, sched, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch TalosMachineConfigBackupSchedule status",
					"name", sched.Name, "namespace", sched.Namespace)
			}
		}
	}()

	sched.Status.ObservedGeneration = sched.Generation

	interval, err := time.ParseDuration(sched.Spec.Schedule)
	if err != nil || interval <= 0 {
		platformv1alpha1.SetCondition(
			&sched.Status.Conditions,
			platformv1alpha1.ConditionTypeMCBScheduleActive,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonMCBScheduleParseError,
			fmt.Sprintf("spec.schedule %q is not a valid Go duration: %v", sched.Spec.Schedule, err),
			sched.Generation,
		)
		return ctrl.Result{}, nil
	}

	now := time.Now().UTC()

	// Determine if a run is due.
	due := sched.Status.LastRunAt == nil || now.After(sched.Status.LastRunAt.Time.Add(interval))
	if !due {
		next := sched.Status.LastRunAt.Time.Add(interval)
		remaining := next.Sub(now)
		platformv1alpha1.SetCondition(
			&sched.Status.Conditions,
			platformv1alpha1.ConditionTypeMCBScheduleActive,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonMCBScheduleNextRunPending,
			fmt.Sprintf("Next backup at %s.", next.Format(time.RFC3339)),
			sched.Generation,
		)
		return ctrl.Result{RequeueAfter: remaining}, nil
	}

	// Create a TalosMachineConfigBackup CR.
	ts := now.Format("20060102t150405z")
	backupName := fmt.Sprintf("%s-sched-%s", sched.Spec.ClusterRef.Name, ts)
	backup := &platformv1alpha1.TalosMachineConfigBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backupName,
			Namespace: sched.Namespace,
		},
		Spec: platformv1alpha1.TalosMachineConfigBackupSpec{
			ClusterRef:        sched.Spec.ClusterRef,
			S3Destination:     sched.Spec.S3Destination,
			S3BackupSecretRef: sched.Spec.S3BackupSecretRef,
		},
	}

	if createErr := r.Client.Create(ctx, backup); createErr != nil {
		if !apierrors.IsAlreadyExists(createErr) {
			return ctrl.Result{}, fmt.Errorf("MachineConfigBackupScheduleReconciler: create backup CR: %w", createErr)
		}
	} else {
		logger.Info("created scheduled TalosMachineConfigBackup",
			"schedule", sched.Name, "backup", backupName)
	}

	nowMeta := metav1.NewTime(now)
	sched.Status.LastRunAt = &nowMeta
	nextRun := metav1.NewTime(now.Add(interval))
	sched.Status.NextRunAt = &nextRun
	sched.Status.LastBackupName = backupName

	platformv1alpha1.SetCondition(
		&sched.Status.Conditions,
		platformv1alpha1.ConditionTypeMCBScheduleActive,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonMCBScheduleNextRunPending,
		fmt.Sprintf("Backup %s created. Next backup at %s.", backupName, nextRun.Format(time.RFC3339)),
		sched.Generation,
	)

	return ctrl.Result{RequeueAfter: interval}, nil
}

// SetupWithManager registers MachineConfigBackupScheduleReconciler with the manager.
func (r *MachineConfigBackupScheduleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.TalosMachineConfigBackupSchedule{}).
		Complete(r)
}
