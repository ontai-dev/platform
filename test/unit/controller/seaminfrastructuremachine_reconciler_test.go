package controller_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1alpha1 "github.com/ontai-dev/platform/api/infrastructure/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// mockApplier is a test double for controller.MachineConfigApplier.
// It satisfies the interface with configurable return values.
type mockApplier struct {
	applyErr            error
	applyCalled         bool
	outOfMaintenance    bool
	outOfMaintenanceErr error
}

// Compile-time verification that mockApplier satisfies MachineConfigApplier.
var _ controller.MachineConfigApplier = (*mockApplier)(nil)

func (m *mockApplier) ApplyConfiguration(_ context.Context, address string, port int32, configData []byte) error {
	m.applyCalled = true
	return m.applyErr
}

func (m *mockApplier) IsOutOfMaintenance(_ context.Context, address string) (bool, error) {
	return m.outOfMaintenance, m.outOfMaintenanceErr
}

// buildSIMScheme returns a scheme with infra and core types registered.
func buildSIMScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	if err := infrav1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add infrav1alpha1 scheme: %v", err)
	}
	return s
}

// buildSIM builds a SeamInfrastructureMachine for testing.
func buildSIM(name, namespace string) *infrav1alpha1.SeamInfrastructureMachine {
	return &infrav1alpha1.SeamInfrastructureMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: infrav1alpha1.SeamInfrastructureMachineSpec{
			Address:  "10.20.0.11",
			Port:     50000,
			NodeRole: infrav1alpha1.NodeRoleControlPlane,
			TalosConfigSecretRef: infrav1alpha1.SecretRef{
				Name:      "ccs-dev-talosconfig",
				Namespace: "ont-system",
			},
		},
	}
}

// TestSIMReconcile_LineageSyncedInitialized verifies the one-time LineageSynced init.
func TestSIMReconcile_LineageSyncedInitialized(t *testing.T) {
	scheme := buildSIMScheme(t)
	sim := buildSIM("cp1", "tenant-ccs-dev")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sim).
		WithStatusSubresource(sim).
		Build()

	r := &controller.SeamInfrastructureMachineReconciler{
		Client:  c,
		Scheme:  scheme,
		Applier: &mockApplier{},
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cp1", Namespace: "tenant-ccs-dev"},
	})
	// Will requeue (no owning CAPI Machine), but must not error.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &infrav1alpha1.SeamInfrastructureMachine{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "cp1", Namespace: "tenant-ccs-dev",
	}, got); err != nil {
		t.Fatalf("get after reconcile: %v", err)
	}

	cond := infrav1alpha1.FindCondition(got.Status.Conditions, infrav1alpha1.ConditionTypeLineageSynced)
	if cond == nil {
		t.Fatal("LineageSynced condition not initialized")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("LineageSynced = %s, want False", cond.Status)
	}
	if cond.Reason != infrav1alpha1.ReasonLineageControllerAbsent {
		t.Errorf("reason = %s, want LineageControllerAbsent", cond.Reason)
	}
}

// TestSIMReconcile_NoCAPIMachine verifies CAPIMachineNotBound condition when no
// owning Machine ownerReference is set.
func TestSIMReconcile_NoCAPIMachine(t *testing.T) {
	scheme := buildSIMScheme(t)
	sim := buildSIM("cp1", "tenant-ccs-dev")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sim).
		WithStatusSubresource(sim).
		Build()

	r := &controller.SeamInfrastructureMachineReconciler{
		Client:  c,
		Scheme:  scheme,
		Applier: &mockApplier{},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cp1", Namespace: "tenant-ccs-dev"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue when no owning CAPI Machine")
	}

	got := &infrav1alpha1.SeamInfrastructureMachine{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "cp1", Namespace: "tenant-ccs-dev",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := infrav1alpha1.FindCondition(got.Status.Conditions, infrav1alpha1.ConditionTypeMachineReady)
	if cond == nil {
		t.Fatal("MachineReady condition not set")
	}
	if cond.Reason != infrav1alpha1.ReasonCAPIMachineNotBound {
		t.Errorf("reason = %s, want CAPIMachineNotBound", cond.Reason)
	}
	if got.Status.Ready {
		t.Error("SIM should not be ready with no owning Machine")
	}
}

// TestSIMReconcile_MachineOwnerRefButNotFound verifies requeue when Machine
// ownerReference is set but the Machine object doesn't exist yet.
func TestSIMReconcile_MachineOwnerRefButNotFound(t *testing.T) {
	scheme := buildSIMScheme(t)
	sim := buildSIM("cp1", "tenant-ccs-dev")
	sim.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: "cluster.x-k8s.io/v1beta1",
			Kind:       "Machine",
			Name:       "ccs-dev-cp1",
			UID:        "abc-123",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sim).
		WithStatusSubresource(sim).
		Build()

	r := &controller.SeamInfrastructureMachineReconciler{
		Client:  c,
		Scheme:  scheme,
		Applier: &mockApplier{},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cp1", Namespace: "tenant-ccs-dev"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Machine not in fake client → NotFound → requeue.
	if result.RequeueAfter == 0 {
		t.Error("expected requeue when Machine object not found")
	}
}

// TestSIMReconcile_AlreadyReadyIsIdempotent verifies a ready SIM returns immediately.
func TestSIMReconcile_AlreadyReadyIsIdempotent(t *testing.T) {
	scheme := buildSIMScheme(t)
	sim := buildSIM("cp1", "tenant-ccs-dev")
	sim.Status.Ready = true
	sim.Status.MachineConfigApplied = true
	sim.Status.ProviderID = "talos://ccs-dev/10.20.0.11"

	applier := &mockApplier{}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sim).
		WithStatusSubresource(sim).
		Build()

	r := &controller.SeamInfrastructureMachineReconciler{
		Client:  c,
		Scheme:  scheme,
		Applier: applier,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cp1", Namespace: "tenant-ccs-dev"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 || result.Requeue {
		t.Errorf("ready SIM should not requeue, got %+v", result)
	}
	if applier.applyCalled {
		t.Error("ApplyConfiguration must not be called for an already-ready SIM")
	}
}

// TestExtractClusterName verifies the namespace-to-cluster-name extraction helper.
func TestExtractClusterName(t *testing.T) {
	cases := []struct {
		namespace string
		want      string
	}{
		{"tenant-ccs-dev", "ccs-dev"},
		{"tenant-my-cluster", "my-cluster"},
		{"tenant-", ""},                // edge case: empty cluster name
		{"ont-system", "ont-system"},   // non-tenant namespace passes through unchanged
	}
	for _, tc := range cases {
		got := controller.ExtractClusterName(tc.namespace)
		if got != tc.want {
			t.Errorf("ExtractClusterName(%q) = %q, want %q", tc.namespace, got, tc.want)
		}
	}
}
