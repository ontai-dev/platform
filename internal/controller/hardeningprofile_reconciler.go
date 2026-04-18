package controller

// HardeningProfileReconciler reconciles HardeningProfile CRs. HardeningProfile
// is a configuration CR only — it does not trigger a Conductor Job directly.
// It initializes lineage tracking and advances ObservedGeneration. NodeMaintenance
// with operation=hardening-apply references this profile when submitting Jobs.
// platform-schema.md §5 HardeningProfile.

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

// HardeningProfileReconciler reconciles HardeningProfile objects.
type HardeningProfileReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=hardeningprofiles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=hardeningprofiles/status,verbs=get;update;patch

func (r *HardeningProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	hp := &platformv1alpha1.HardeningProfile{}
	if err := r.Client.Get(ctx, req.NamespacedName, hp); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get HardeningProfile %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(hp.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, hp, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch HardeningProfile status",
					"name", hp.Name, "namespace", hp.Namespace)
			}
		}
	}()

	hp.Status.ObservedGeneration = hp.Generation

	// Initialize LineageSynced on first observation — one-time write.
	// HardeningProfile is a root declaration: it carries a lineage field.
	// seam-core-schema.md §7 Declaration 5.
	if platformv1alpha1.FindCondition(hp.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&hp.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			hp.Generation,
		)
	}

	// HardeningProfile is a pure configuration CR — no Job submission, no
	// further reconciliation required. Validation and lineage initialization
	// above are the complete reconciliation path.
	logger.V(1).Info("HardeningProfile reconciled",
		"name", hp.Name, "namespace", hp.Namespace,
		"patchCount", len(hp.Spec.MachineConfigPatches))
	return ctrl.Result{}, nil
}

// SetupWithManager registers HardeningProfileReconciler with the manager.
func (r *HardeningProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.HardeningProfile{}).
		Complete(r)
}
