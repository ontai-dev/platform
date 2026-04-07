// Package controller_test — Screen infrastructure provider reservation test.
//
// Workstream 3: Screen reserved path documentation test.
//
// Screen is a future operator for KubeVirt workload lifecycle on VM-class
// clusters. INV-021: no implementation work proceeds until a formal Architecture
// Decision Record is approved by the Platform Governor.
//
// This test documents the reservation contract:
//   - A TalosCluster with spec.infrastructureProvider=screen must have the
//     ScreenProviderNotImplemented condition set to True.
//   - The reconciler must halt without attempting any further reconciliation.
//   - The spec.infrastructureProvider field must be preserved unchanged.
//
// This test WILL FAIL when Screen is implemented. That failure is the intended
// signal to the Platform Governor that the reservation contract has changed.
//
// platform-schema.md INV-021. CLAUDE.md §3 Screen entry.
package controller_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// TestTalosClusterReconcile_ScreenProviderNotImplemented verifies the Screen
// infrastructure provider reservation contract.
//
// A TalosCluster with spec.infrastructureProvider=screen must:
//  1. Have ScreenProviderNotImplemented condition set to True.
//  2. Not transition to Ready, Bootstrapping, or any operational state.
//  3. Preserve the spec.infrastructureProvider field without modification.
//
// This test documents the reserved path. It passes today because Screen is not
// implemented. When Screen is implemented, this test will fail — which is the
// signal to the Platform Governor that Screen reconciliation is live and the
// reservation contract should be revisited. INV-021.
func TestTalosClusterReconcile_ScreenProviderNotImplemented(t *testing.T) {
	scheme := buildDay2Scheme(t)

	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "vm-cluster",
			Namespace:  "seam-system",
			Generation: 1,
		},
		Spec: platformv1alpha1.TalosClusterSpec{
			Mode:                   platformv1alpha1.TalosClusterModeBootstrap,
			InfrastructureProvider: "screen",
			CAPI: platformv1alpha1.CAPIConfig{
				Enabled: true,
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc).
		WithStatusSubresource(tc).
		Build()

	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "vm-cluster", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error when screen provider set: %v", err)
	}
	// Reconciler must halt — no requeue.
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue for screen provider (halted), got RequeueAfter=%v — Screen may have been implemented", result.RequeueAfter)
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "vm-cluster", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}

	// Assertion 1: ScreenProviderNotImplemented must be True.
	screenCond := platformv1alpha1.FindCondition(got.Status.Conditions,
		platformv1alpha1.ConditionTypeScreenProviderNotImplemented)
	if screenCond == nil {
		t.Fatal("ScreenProviderNotImplemented condition not set — reservation contract not enforced")
	}
	if screenCond.Status != metav1.ConditionTrue {
		t.Errorf("ScreenProviderNotImplemented = %s, want True — Screen path should be reserved, not active",
			screenCond.Status)
	}
	if screenCond.Reason != platformv1alpha1.ReasonScreenNotImplemented {
		t.Errorf("ScreenProviderNotImplemented reason = %q, want %q",
			screenCond.Reason, platformv1alpha1.ReasonScreenNotImplemented)
	}

	// Assertion 2: Cluster must not be in any operational state.
	// Ready=True would indicate the screen path ran a successful reconciliation.
	readyCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		t.Error("TalosCluster must not be Ready when screen provider is reserved — Screen may be implemented now")
	}
	// Bootstrapping=True would indicate CAPI or bootstrap path was entered.
	bootstrappingCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeBootstrapping)
	if bootstrappingCond != nil && bootstrappingCond.Status == metav1.ConditionTrue {
		t.Error("TalosCluster must not be Bootstrapping when screen provider is reserved — reconciler should have halted")
	}

	// Assertion 3: spec.infrastructureProvider must be preserved.
	if got.Spec.InfrastructureProvider != "screen" {
		t.Errorf("spec.infrastructureProvider = %q after reconcile, want screen — field must be preserved",
			got.Spec.InfrastructureProvider)
	}
}
