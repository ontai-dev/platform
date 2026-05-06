package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

const hardeningBootstrapLabel = "ontai.dev/hardening-trigger"
const hardeningBootstrapLabelValue = "bootstrap"
const hardeningRequeueInterval = 30 * time.Second

// ensureBootstrapHardening creates a NodeMaintenance with operation=hardening-apply and
// label ontai.dev/hardening-trigger=bootstrap in seam-tenant-{cluster} when
// spec.hardeningProfileRef is set and the cluster is Ready (ONT-native path only).
// Sets HardeningApplied on TalosCluster:
//   - False/HardeningProfileNotValid: HardeningProfile Valid=True not yet set
//   - False/HardeningPending: NodeMaintenance created, not yet Ready
//   - True/HardeningApplied: NodeMaintenance reached Ready=True
//
// Returns a non-zero RequeueAfter when NodeMaintenance is pending.
func (r *TalosClusterReconciler) ensureBootstrapHardening(
	ctx context.Context,
	tc *platformv1alpha1.TalosCluster,
) (ctrl.Result, error) {
	if tc.Spec.HardeningProfileRef == nil {
		return ctrl.Result{}, nil
	}

	hpNS := tc.Spec.HardeningProfileRef.Namespace
	if hpNS == "" {
		hpNS = tc.Namespace
	}

	// Verify the HardeningProfile exists and has Valid=True before creating NodeMaintenance.
	hp := &platformv1alpha1.HardeningProfile{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      tc.Spec.HardeningProfileRef.Name,
		Namespace: hpNS,
	}, hp); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensureBootstrapHardening: get HardeningProfile: %w", err)
	}
	validCond := platformv1alpha1.FindCondition(hp.Status.Conditions, platformv1alpha1.ConditionTypeHardeningProfileValid)
	if validCond == nil || validCond.Status != metav1.ConditionTrue {
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeHardeningApplied,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonHardeningProfileNotValid,
			fmt.Sprintf("HardeningProfile %s/%s does not have Valid=True.", hpNS, hp.Name),
			tc.Generation,
		)
		return ctrl.Result{RequeueAfter: hardeningRequeueInterval}, nil
	}

	tenantNS := "seam-tenant-" + tc.Name

	// Idempotency guard: check for an existing bootstrap NodeMaintenance.
	nmList := &platformv1alpha1.NodeMaintenanceList{}
	if err := r.Client.List(ctx, nmList,
		client.InNamespace(tenantNS),
		client.MatchingLabels{hardeningBootstrapLabel: hardeningBootstrapLabelValue},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensureBootstrapHardening: list NodeMaintenance: %w", err)
	}

	if len(nmList.Items) > 0 {
		nm := nmList.Items[0]
		readyCond := platformv1alpha1.FindCondition(nm.Status.Conditions, platformv1alpha1.ConditionTypeNodeMaintenanceReady)
		if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
			platformv1alpha1.SetCondition(
				&tc.Status.Conditions,
				platformv1alpha1.ConditionTypeHardeningApplied,
				metav1.ConditionTrue,
				platformv1alpha1.ReasonHardeningApplied,
				fmt.Sprintf("Bootstrap NodeMaintenance %s reached Ready=True.", nm.Name),
				tc.Generation,
			)
			return ctrl.Result{}, nil
		}
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeHardeningApplied,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonHardeningPending,
			fmt.Sprintf("Bootstrap NodeMaintenance %s pending.", nm.Name),
			tc.Generation,
		)
		return ctrl.Result{RequeueAfter: hardeningRequeueInterval}, nil
	}

	// Create the bootstrap NodeMaintenance.
	nm := &platformv1alpha1.NodeMaintenance{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: tc.Name + "-bootstrap-hardening-",
			Namespace:    tenantNS,
			Labels: map[string]string{
				hardeningBootstrapLabel: hardeningBootstrapLabelValue,
			},
		},
		Spec: platformv1alpha1.NodeMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{
				Name:      tc.Name,
				Namespace: tc.Namespace,
			},
			Operation: platformv1alpha1.NodeMaintenanceOperationHardeningApply,
			HardeningProfileRef: &platformv1alpha1.LocalObjectRef{
				Name:      tc.Spec.HardeningProfileRef.Name,
				Namespace: hpNS,
			},
		},
	}
	if err := r.Client.Create(ctx, nm); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, fmt.Errorf("ensureBootstrapHardening: create NodeMaintenance: %w", err)
	}

	platformv1alpha1.SetCondition(
		&tc.Status.Conditions,
		platformv1alpha1.ConditionTypeHardeningApplied,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonHardeningPending,
		"Bootstrap NodeMaintenance created, hardening-apply pending.",
		tc.Generation,
	)
	return ctrl.Result{RequeueAfter: hardeningRequeueInterval}, nil
}
