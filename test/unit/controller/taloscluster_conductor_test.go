// Package controller_test tests the TalosCluster conductor bootstrap window
// functions. Tests cover the ConductorReady condition lifecycle driven by
// RemoteConductorBootstrapDoneFn for tenant and management import clusters.
//
// Testing the full remote-cluster path requires a live cluster and is covered by
// integration tests, not unit tests.
//
// platform-schema.md §12 Conductor Bootstrap Window Contract. INV-020.
package controller_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// buildTenantImportTalosCluster returns a TalosCluster configured for the tenant
// import path (mode=import, role=tenant).
func buildTenantImportTalosCluster(name, namespace string) *platformv1alpha1.TalosCluster {
	return &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			Mode:         platformv1alpha1.TalosClusterModeImport,
			TalosVersion: "v1.9.3",
			Role:         platformv1alpha1.TalosClusterRoleTenant,
		},
	}
}

// TestTenantImport_ConductorPending_RequeuesWhenNotAvailable verifies that a role=tenant
// import reconcile sets ConductorReady=False and requeues (RequeueAfter>0) when
// RemoteConductorBootstrapDoneFn reports the Conductor Deployment is not yet Available.
// Platform must not set Ready=True until ConductorReady=True. guardian-schema.md §20.
func TestTenantImport_ConductorPending_RequeuesWhenNotAvailable(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildTenantImportTalosCluster("ccs-dev", "seam-system")
	talosSecret := buildFakeTalosconfigSecret("ccs-dev")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(32),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
		RemoteConductorBootstrapDoneFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil // Deployment created but not yet Available.
		},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter>0 while Conductor not yet Available")
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}

	readyCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		t.Error("Ready must not be True while Conductor is pending")
	}
	crCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeConductorReady)
	if crCond == nil || crCond.Status != metav1.ConditionFalse {
		t.Errorf("expected ConductorReady=False while Deployment not yet Available, got %v", crCond)
	}

	// Bootstrapped=True must be set (management-side onboarding complete).
	bootCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeBootstrapped)
	if bootCond == nil || bootCond.Status != metav1.ConditionTrue {
		t.Errorf("expected Bootstrapped=True after management-side onboarding, got %v", bootCond)
	}
}

// TestTenantImport_ConductorReady_TransitionsToReady verifies that once
// RemoteConductorBootstrapDoneFn returns true, the reconciler sets ConductorReady=True
// and Ready=True on the TalosCluster. This is the final state for a tenant import.
// guardian-schema.md §20.
func TestTenantImport_ConductorReady_TransitionsToReady(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildTenantImportTalosCluster("ccs-dev-ready", "seam-system")
	talosSecret := buildFakeTalosconfigSecret("ccs-dev-ready")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(32),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
		RemoteConductorBootstrapDoneFn: func(_ context.Context, _ string) (bool, error) {
			return true, nil // Conductor Deployment is Available.
		},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev-ready", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue when Conductor is Available, got RequeueAfter=%v", result.RequeueAfter)
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "ccs-dev-ready", Namespace: "seam-system"}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}

	crCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeConductorReady)
	if crCond == nil || crCond.Status != metav1.ConditionTrue {
		t.Errorf("expected ConductorReady=True when Deployment Available, got %v", crCond)
	}
	readyCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeReady)
	if readyCond == nil || readyCond.Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True when Conductor Available, got %v", readyCond)
	}
	if got.Status.Origin != platformv1alpha1.TalosClusterOriginImported {
		t.Errorf("expected status.origin=imported, got %q", got.Status.Origin)
	}
}

// TestTenantImport_ManagementImportStillImmediatelyReady verifies that a role=management
// import (the management cluster itself being imported) does NOT wait for a remote
// Conductor -- it transitions to Ready in a single pass because Conductor role=management
// is already deployed by compiler enable. guardian-schema.md §20.
func TestTenantImport_ManagementImportStillImmediatelyReady(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster("ccs-mgmt", "seam-system")
	talosSecret := buildFakeTalosconfigSecret("ccs-mgmt")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(32),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
		// No RemoteConductorBootstrapDoneFn -- must not be called for management import.
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("management import: unexpected reconcile error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("management import must complete in one pass, got RequeueAfter=%v", result.RequeueAfter)
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	readyCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeReady)
	if readyCond == nil || readyCond.Status != metav1.ConditionTrue {
		t.Errorf("management import: Ready not True after single reconcile, got %v", readyCond)
	}
}
