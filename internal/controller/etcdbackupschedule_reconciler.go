package controller

// EtcdBackupScheduleReconciler reconciles TalosEtcdBackupSchedule CRs.
//
// Creates an EtcdMaintenance CR with operation=backup each time the interval
// specified in spec.schedule elapses. The schedule field is a Go duration string.
// Returns RequeueAfter = remaining time until the next run.
// platform-schema.md §10.

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

// EtcdBackupScheduleReconciler reconciles TalosEtcdBackupSchedule objects.
type EtcdBackupScheduleReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosetcdbackupschedules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosetcdbackupschedules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=etcdmaintenances,verbs=get;list;watch;create;update;patch;delete

func (r *EtcdBackupScheduleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	sched := &platformv1alpha1.TalosEtcdBackupSchedule{}
	if err := r.Client.Get(ctx, req.NamespacedName, sched); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get TalosEtcdBackupSchedule %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(sched.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, sched, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch TalosEtcdBackupSchedule status",
					"name", sched.Name, "namespace", sched.Namespace)
			}
		}
	}()

	sched.Status.ObservedGeneration = sched.Generation

	interval, err := time.ParseDuration(sched.Spec.Schedule)
	if err != nil || interval <= 0 {
		platformv1alpha1.SetCondition(
			&sched.Status.Conditions,
			platformv1alpha1.ConditionTypeEtcdBackupScheduleActive,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonEtcdBackupScheduleParseError,
			fmt.Sprintf("spec.schedule %q is not a valid Go duration: %v", sched.Spec.Schedule, err),
			sched.Generation,
		)
		return ctrl.Result{}, nil
	}

	now := time.Now().UTC()

	due := sched.Status.LastRunAt == nil || now.After(sched.Status.LastRunAt.Time.Add(interval))
	if !due {
		next := sched.Status.LastRunAt.Time.Add(interval)
		remaining := next.Sub(now)
		platformv1alpha1.SetCondition(
			&sched.Status.Conditions,
			platformv1alpha1.ConditionTypeEtcdBackupScheduleActive,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonEtcdBackupScheduleNextRunPending,
			fmt.Sprintf("Next etcd backup at %s.", next.Format(time.RFC3339)),
			sched.Generation,
		)
		return ctrl.Result{RequeueAfter: remaining}, nil
	}

	ts := now.Format("20060102t150405z")
	emName := fmt.Sprintf("%s-etcdsched-%s", sched.Spec.ClusterRef.Name, ts)

	// S3Ref for EtcdMaintenance: bucket from schedule spec, key derived from cluster and timestamp.
	s3KeyPrefix := fmt.Sprintf("%s/etcd/%s/snapshot.db", sched.Spec.ClusterRef.Name, ts)
	s3Ref := platformv1alpha1.S3Ref{
		Bucket: sched.Spec.S3Destination.Bucket,
		Key:    s3KeyPrefix,
	}

	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      emName,
			Namespace: sched.Namespace,
		},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef:            sched.Spec.ClusterRef,
			Operation:             platformv1alpha1.EtcdMaintenanceOperationBackup,
			S3Destination:         &s3Ref,
			EtcdBackupS3SecretRef: sched.Spec.EtcdBackupS3SecretRef,
		},
	}

	if createErr := r.Client.Create(ctx, em); createErr != nil {
		if !apierrors.IsAlreadyExists(createErr) {
			return ctrl.Result{}, fmt.Errorf("EtcdBackupScheduleReconciler: create EtcdMaintenance CR: %w", createErr)
		}
	} else {
		logger.Info("created scheduled EtcdMaintenance backup",
			"schedule", sched.Name, "etcdmaintenance", emName)
	}

	nowMeta := metav1.NewTime(now)
	sched.Status.LastRunAt = &nowMeta
	nextRun := metav1.NewTime(now.Add(interval))
	sched.Status.NextRunAt = &nextRun
	sched.Status.LastBackupName = emName

	platformv1alpha1.SetCondition(
		&sched.Status.Conditions,
		platformv1alpha1.ConditionTypeEtcdBackupScheduleActive,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonEtcdBackupScheduleNextRunPending,
		fmt.Sprintf("EtcdMaintenance %s created. Next backup at %s.", emName, nextRun.Format(time.RFC3339)),
		sched.Generation,
	)

	return ctrl.Result{RequeueAfter: interval}, nil
}

// SetupWithManager registers EtcdBackupScheduleReconciler with the manager.
func (r *EtcdBackupScheduleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.TalosEtcdBackupSchedule{}).
		Complete(r)
}
