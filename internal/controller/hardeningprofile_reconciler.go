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

	// Validate spec fields and set Valid condition.
	// machineConfigPatches must be non-empty JSON strings.
	// sysctlParams keys must be non-empty.
	invalidReason := validateHardeningProfileSpec(hp.Spec)
	if invalidReason != "" {
		platformv1alpha1.SetCondition(
			&hp.Status.Conditions,
			platformv1alpha1.ConditionTypeHardeningProfileValid,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonHardeningProfileInvalid,
			invalidReason,
			hp.Generation,
		)
	} else {
		platformv1alpha1.SetCondition(
			&hp.Status.Conditions,
			platformv1alpha1.ConditionTypeHardeningProfileValid,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonHardeningProfileValid,
			"HardeningProfile spec is valid.",
			hp.Generation,
		)
	}

	logger.V(1).Info("HardeningProfile reconciled",
		"name", hp.Name, "namespace", hp.Namespace,
		"patchCount", len(hp.Spec.MachineConfigPatches))
	return ctrl.Result{}, nil
}

// validateHardeningProfileSpec returns a non-empty reason string if the spec is
// invalid, or an empty string if it passes all checks.
func validateHardeningProfileSpec(spec platformv1alpha1.HardeningProfileSpec) string {
	for i, patch := range spec.MachineConfigPatches {
		if len(patch) == 0 {
			return fmt.Sprintf("machineConfigPatches[%d] is empty", i)
		}
	}
	for k := range spec.SysctlParams {
		if len(k) == 0 {
			return "sysctlParams contains an empty key"
		}
	}
	return ""
}

// SetupWithManager registers HardeningProfileReconciler with the manager.
func (r *HardeningProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.HardeningProfile{}).
		Complete(r)
}
