package controller

// MaintenanceBundleReconciler reconciles MaintenanceBundle CRs. A MaintenanceBundle
// is a pre-compiled scheduling artifact produced by `compiler maintenance`. The
// reconciler creates a RunnerConfig from the pre-resolved scheduling context and
// submits the appropriate Conductor executor Job.
//
// F-P5: This is a stub reconciler. Full implementation is deferred.
// conductor-schema.md §9, platform-schema.md §10.

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

// MaintenanceBundleReconciler reconciles MaintenanceBundle objects.
type MaintenanceBundleReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=maintenancebundles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=maintenancebundles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=maintenancebundles/finalizers,verbs=update

func (r *MaintenanceBundleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	mb := &platformv1alpha1.MaintenanceBundle{}
	if err := r.Client.Get(ctx, req.NamespacedName, mb); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get MaintenanceBundle %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(mb.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, mb, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch MaintenanceBundle status",
					"name", mb.Name, "namespace", mb.Namespace)
			}
		}
	}()

	mb.Status.ObservedGeneration = mb.Generation

	// Initialize LineageSynced on first observation — one-time write.
	// seam-core-schema.md §7 Declaration 5.
	if platformv1alpha1.FindCondition(mb.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&mb.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			mb.Generation,
		)
	}

	// Stub: full RunnerConfig creation and Job submission deferred (F-P5).
	platformv1alpha1.SetCondition(
		&mb.Status.Conditions,
		platformv1alpha1.ConditionTypeMaintenanceBundlePending,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonMaintenanceBundleReconcilerNotImplemented,
		"MaintenanceBundle reconciler is not yet implemented. Full implementation deferred to F-P5.",
		mb.Generation,
	)

	logger.Info("MaintenanceBundle reconciler stub — F-P5 deferred",
		"name", mb.Name, "namespace", mb.Namespace,
		"operation", mb.Spec.Operation)
	return ctrl.Result{}, nil
}

// SetupWithManager registers MaintenanceBundleReconciler with the manager.
func (r *MaintenanceBundleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.MaintenanceBundle{}).
		Complete(r)
}
