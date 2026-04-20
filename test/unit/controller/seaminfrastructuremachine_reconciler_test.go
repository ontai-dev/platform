package controller_test

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	corev1 "k8s.io/api/core/v1"
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
	sim := buildSIM("cp1", "seam-tenant-ccs-dev")

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
		NamespacedName: types.NamespacedName{Name: "cp1", Namespace: "seam-tenant-ccs-dev"},
	})
	// Will requeue (no owning CAPI Machine), but must not error.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &infrav1alpha1.SeamInfrastructureMachine{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "cp1", Namespace: "seam-tenant-ccs-dev",
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
	sim := buildSIM("cp1", "seam-tenant-ccs-dev")

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
		NamespacedName: types.NamespacedName{Name: "cp1", Namespace: "seam-tenant-ccs-dev"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue when no owning CAPI Machine")
	}

	got := &infrav1alpha1.SeamInfrastructureMachine{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "cp1", Namespace: "seam-tenant-ccs-dev",
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
	sim := buildSIM("cp1", "seam-tenant-ccs-dev")
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
		NamespacedName: types.NamespacedName{Name: "cp1", Namespace: "seam-tenant-ccs-dev"},
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
	sim := buildSIM("cp1", "seam-tenant-ccs-dev")
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
		NamespacedName: types.NamespacedName{Name: "cp1", Namespace: "seam-tenant-ccs-dev"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
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
		{"seam-tenant-ccs-dev", "ccs-dev"},
		{"seam-tenant-my-cluster", "my-cluster"},
		{"seam-tenant-", ""},                // edge case: empty cluster name
		{"ont-system", "ont-system"},        // non-tenant namespace passes through unchanged
	}
	for _, tc := range cases {
		got := controller.ExtractClusterName(tc.namespace)
		if got != tc.want {
			t.Errorf("ExtractClusterName(%q) = %q, want %q", tc.namespace, got, tc.want)
		}
	}
}

// buildSIMWithCAPIBootstrap creates the fake-client objects needed to reach the
// ApplyConfiguration step: a SIM with an ownerRef to a CAPI Machine, an
// unstructured CAPI Machine with bootstrap dataSecretName, and a corev1.Secret
// containing the machineconfig bytes.
func buildSIMWithCAPIBootstrap(t *testing.T, ns string) (
	sim *infrav1alpha1.SeamInfrastructureMachine,
	capiMachine *unstructured.Unstructured,
	bootstrapSecret *corev1.Secret,
) {
	t.Helper()
	simName := "cp1"
	machineName := "ccs-dev-cp1"
	secretName := "cp1-bootstrap"

	sim = buildSIM(simName, ns)
	sim.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: "cluster.x-k8s.io/v1beta1",
			Kind:       "Machine",
			Name:       machineName,
			UID:        "mach-uid",
		},
	}

	capiMachine = &unstructured.Unstructured{}
	capiMachine.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "Machine",
	})
	capiMachine.SetName(machineName)
	capiMachine.SetNamespace(ns)
	_ = unstructured.SetNestedField(capiMachine.Object, secretName, "spec", "bootstrap", "dataSecretName")

	bootstrapSecret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns},
		Data:       map[string][]byte{"value": []byte("machineconfig-yaml-data")},
	}
	return sim, capiMachine, bootstrapSecret
}

// TestPort50000Backoff verifies the exponential backoff formula.
// Base 10s, cap 5min. Formula: 10s * 2^(attempts-1).
func TestPort50000Backoff(t *testing.T) {
	cases := []struct {
		attempts int32
		want     time.Duration
	}{
		{1, 10 * time.Second},
		{2, 20 * time.Second},
		{3, 40 * time.Second},
		{4, 80 * time.Second},
		{10, 5 * time.Minute}, // capped
		{20, 5 * time.Minute}, // still capped
	}
	for _, c := range cases {
		got := controller.Port50000Backoff(c.attempts)
		if got != c.want {
			t.Errorf("Port50000Backoff(%d) = %s, want %s", c.attempts, got, c.want)
		}
	}
}

// TestSIMReconcile_ApplyFailureBackoff_FirstAttempt verifies that the first
// ApplyConfiguration failure: increments ApplyAttempts to 1, returns a 10s
// requeue, returns nil error (no controller-runtime double-backoff), and sets
// PortReachable=False.
func TestSIMReconcile_ApplyFailureBackoff_FirstAttempt(t *testing.T) {
	scheme := buildSIMScheme(t)
	ns := "seam-tenant-ccs-dev"
	sim, capiMachine, secret := buildSIMWithCAPIBootstrap(t, ns)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sim, secret).
		WithStatusSubresource(sim).
		Build()
	// Store unstructured CAPI Machine without scheme registration.
	if err := c.Create(context.Background(), capiMachine); err != nil {
		t.Fatalf("create CAPI Machine: %v", err)
	}

	applier := &mockApplier{applyErr: errors.New("connection refused")}
	r := &controller.SeamInfrastructureMachineReconciler{
		Client:   c,
		Scheme:   scheme,
		Applier:  applier,
		Recorder: fakeRecorder(),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sim.Name, Namespace: ns},
	})
	if err != nil {
		t.Fatalf("expected nil error on ApplyConfiguration failure, got: %v", err)
	}
	if result.RequeueAfter != 10*time.Second {
		t.Errorf("RequeueAfter = %s, want 10s (first attempt backoff)", result.RequeueAfter)
	}

	got := &infrav1alpha1.SeamInfrastructureMachine{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: sim.Name, Namespace: ns}, got); err != nil {
		t.Fatalf("get SIM after reconcile: %v", err)
	}
	if got.Status.ApplyAttempts != 1 {
		t.Errorf("ApplyAttempts = %d, want 1", got.Status.ApplyAttempts)
	}
	cond := infrav1alpha1.FindCondition(got.Status.Conditions, infrav1alpha1.ConditionTypePortReachable)
	if cond == nil {
		t.Fatal("PortReachable condition not set after failure")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("PortReachable = %s, want False", cond.Status)
	}
	if cond.Reason != infrav1alpha1.ReasonPortUnreachable {
		t.Errorf("PortReachable reason = %s, want %s", cond.Reason, infrav1alpha1.ReasonPortUnreachable)
	}
}

// TestSIMReconcile_ApplyFailureBackoff_SecondAttempt verifies that the second
// consecutive failure doubles the backoff to 20s and increments ApplyAttempts to 2.
func TestSIMReconcile_ApplyFailureBackoff_SecondAttempt(t *testing.T) {
	scheme := buildSIMScheme(t)
	ns := "seam-tenant-ccs-dev"
	sim, capiMachine, secret := buildSIMWithCAPIBootstrap(t, ns)
	// Pre-set ApplyAttempts=1 to simulate a previous failure.
	sim.Status.ApplyAttempts = 1

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sim, secret).
		WithStatusSubresource(sim).
		Build()
	if err := c.Create(context.Background(), capiMachine); err != nil {
		t.Fatalf("create CAPI Machine: %v", err)
	}

	applier := &mockApplier{applyErr: errors.New("connection refused")}
	r := &controller.SeamInfrastructureMachineReconciler{
		Client:   c,
		Scheme:   scheme,
		Applier:  applier,
		Recorder: fakeRecorder(),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sim.Name, Namespace: ns},
	})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if result.RequeueAfter != 20*time.Second {
		t.Errorf("RequeueAfter = %s, want 20s (second attempt backoff)", result.RequeueAfter)
	}

	got := &infrav1alpha1.SeamInfrastructureMachine{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: sim.Name, Namespace: ns}, got); err != nil {
		t.Fatalf("get SIM: %v", err)
	}
	if got.Status.ApplyAttempts != 2 {
		t.Errorf("ApplyAttempts = %d, want 2", got.Status.ApplyAttempts)
	}
}

// TestSIMReconcile_BootstrapDataSecretNameAbsent verifies that when the owning
// CAPI Machine exists but bootstrap.dataSecretName is not yet set (CABPT has not
// rendered the machineconfig), the reconciler sets BootstrapDataNotReady condition,
// does NOT call ApplyConfiguration, and requeues.
func TestSIMReconcile_BootstrapDataSecretNameAbsent(t *testing.T) {
	scheme := buildSIMScheme(t)
	ns := "seam-tenant-ccs-dev"
	machineName := "ccs-dev-cp1"

	sim := buildSIM("cp1", ns)
	sim.OwnerReferences = []metav1.OwnerReference{
		{APIVersion: "cluster.x-k8s.io/v1beta1", Kind: "Machine", Name: machineName, UID: "m1"},
	}

	// CAPI Machine exists but bootstrap.dataSecretName is absent.
	capiMachine := &unstructured.Unstructured{}
	capiMachine.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "cluster.x-k8s.io", Version: "v1beta1", Kind: "Machine",
	})
	capiMachine.SetName(machineName)
	capiMachine.SetNamespace(ns)

	applier := &mockApplier{}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sim).
		WithStatusSubresource(sim).
		Build()
	if err := c.Create(context.Background(), capiMachine); err != nil {
		t.Fatalf("create CAPI Machine: %v", err)
	}

	r := &controller.SeamInfrastructureMachineReconciler{
		Client:  c,
		Scheme:  scheme,
		Applier: applier,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cp1", Namespace: ns},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue when bootstrap dataSecretName absent")
	}
	if applier.applyCalled {
		t.Error("ApplyConfiguration must not be called before bootstrap dataSecretName is set")
	}

	got := &infrav1alpha1.SeamInfrastructureMachine{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp1", Namespace: ns}, got); err != nil {
		t.Fatalf("get SIM: %v", err)
	}
	cond := infrav1alpha1.FindCondition(got.Status.Conditions, infrav1alpha1.ConditionTypeMachineReady)
	if cond == nil {
		t.Fatal("MachineReady condition not set")
	}
	if cond.Reason != infrav1alpha1.ReasonBootstrapDataNotReady {
		t.Errorf("reason = %q, want BootstrapDataNotReady", cond.Reason)
	}
}

// TestSIMReconcile_ApplyCalledWithCorrectAddressPortConfig verifies that when
// bootstrap data is present, ApplyConfiguration receives the address and port from
// spec and the machineconfig bytes from the bootstrap Secret's "value" key.
func TestSIMReconcile_ApplyCalledWithCorrectAddressPortConfig(t *testing.T) {
	scheme := buildSIMScheme(t)
	ns := "seam-tenant-ccs-dev"

	sim, capiMachine, secret := buildSIMWithCAPIBootstrap(t, ns)
	// outOfMaintenance=false so reconcile returns after apply, before ready.
	applier := &mockApplier{outOfMaintenance: false}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sim, secret).
		WithStatusSubresource(sim).
		Build()
	if err := c.Create(context.Background(), capiMachine); err != nil {
		t.Fatalf("create CAPI Machine: %v", err)
	}

	var capturedAddress string
	var capturedPort int32
	var capturedConfig []byte
	capturingApplier := &captureApplier{
		inner: applier,
		onApply: func(addr string, port int32, cfg []byte) {
			capturedAddress = addr
			capturedPort = port
			capturedConfig = cfg
		},
	}

	r := &controller.SeamInfrastructureMachineReconciler{
		Client:   c,
		Scheme:   scheme,
		Applier:  capturingApplier,
		Recorder: fakeRecorder(),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sim.Name, Namespace: ns},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedAddress != "10.20.0.11" {
		t.Errorf("ApplyConfiguration address = %q, want 10.20.0.11", capturedAddress)
	}
	if capturedPort != 50000 {
		t.Errorf("ApplyConfiguration port = %d, want 50000", capturedPort)
	}
	if string(capturedConfig) != "machineconfig-yaml-data" {
		t.Errorf("ApplyConfiguration config = %q, want machineconfig-yaml-data", capturedConfig)
	}
}

// TestSIMReconcile_MachineConfigApplied_SkipsApply verifies that when
// status.machineConfigApplied=true (config already delivered), a second reconcile
// does NOT call ApplyConfiguration and proceeds directly to the maintenance poll step.
func TestSIMReconcile_MachineConfigApplied_SkipsApply(t *testing.T) {
	scheme := buildSIMScheme(t)
	ns := "seam-tenant-ccs-dev"

	sim, capiMachine, secret := buildSIMWithCAPIBootstrap(t, ns)
	// Pre-mark config as applied — skip steps 2-4 per platform-design.md §3.1.
	sim.Status.MachineConfigApplied = true

	applier := &mockApplier{outOfMaintenance: false}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sim, secret).
		WithStatusSubresource(sim).
		Build()
	if err := c.Create(context.Background(), capiMachine); err != nil {
		t.Fatalf("create CAPI Machine: %v", err)
	}

	r := &controller.SeamInfrastructureMachineReconciler{
		Client:  c,
		Scheme:  scheme,
		Applier: applier,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sim.Name, Namespace: ns},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applier.applyCalled {
		t.Error("ApplyConfiguration must not be called when machineConfigApplied=true")
	}
	// Still in maintenance → requeue expected.
	if result.RequeueAfter == 0 {
		t.Error("expected requeue while node is still in maintenance mode")
	}
}

// TestSIMReconcile_ReadyAfterOutOfMaintenance verifies that when the node exits
// maintenance mode, the reconciler sets status.ready=true, writes the providerID,
// and sets MachineReady=True. platform-design.md §3.1 Steps 5 and 6.
func TestSIMReconcile_ReadyAfterOutOfMaintenance(t *testing.T) {
	scheme := buildSIMScheme(t)
	ns := "seam-tenant-ccs-dev"

	sim, capiMachine, secret := buildSIMWithCAPIBootstrap(t, ns)
	sim.Status.MachineConfigApplied = true
	// Node has exited maintenance mode.
	applier := &mockApplier{outOfMaintenance: true}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sim, secret).
		WithStatusSubresource(sim).
		Build()
	if err := c.Create(context.Background(), capiMachine); err != nil {
		t.Fatalf("create CAPI Machine: %v", err)
	}

	r := &controller.SeamInfrastructureMachineReconciler{
		Client:  c,
		Scheme:  scheme,
		Applier: applier,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sim.Name, Namespace: ns},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No requeue after ready.
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue after ready, got %v", result.RequeueAfter)
	}

	got := &infrav1alpha1.SeamInfrastructureMachine{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: sim.Name, Namespace: ns}, got); err != nil {
		t.Fatalf("get SIM: %v", err)
	}
	if !got.Status.Ready {
		t.Error("status.ready must be true after node exits maintenance mode")
	}
	wantProviderID := "talos://ccs-dev/10.20.0.11"
	if got.Status.ProviderID != wantProviderID {
		t.Errorf("providerID = %q, want %q", got.Status.ProviderID, wantProviderID)
	}
	cond := infrav1alpha1.FindCondition(got.Status.Conditions, infrav1alpha1.ConditionTypeMachineReady)
	if cond == nil {
		t.Fatal("MachineReady condition not set")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("MachineReady = %s, want True", cond.Status)
	}
	if cond.Reason != infrav1alpha1.ReasonMachineReady {
		t.Errorf("reason = %q, want MachineReady", cond.Reason)
	}
}

// TestSIMReconcile_IsOutOfMaintenanceError verifies that an error from
// IsOutOfMaintenance propagates as a reconcile error (returned to controller-runtime
// for retrying with backoff), not as a status condition.
func TestSIMReconcile_IsOutOfMaintenanceError(t *testing.T) {
	scheme := buildSIMScheme(t)
	ns := "seam-tenant-ccs-dev"

	sim, capiMachine, secret := buildSIMWithCAPIBootstrap(t, ns)
	sim.Status.MachineConfigApplied = true
	applier := &mockApplier{outOfMaintenanceErr: errors.New("talos api unavailable")}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sim, secret).
		WithStatusSubresource(sim).
		Build()
	if err := c.Create(context.Background(), capiMachine); err != nil {
		t.Fatalf("create CAPI Machine: %v", err)
	}

	r := &controller.SeamInfrastructureMachineReconciler{
		Client:  c,
		Scheme:  scheme,
		Applier: applier,
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sim.Name, Namespace: ns},
	})
	if err == nil {
		t.Error("expected non-nil error when IsOutOfMaintenance returns error")
	}
}

// captureApplier wraps a MachineConfigApplier and captures the arguments passed to
// ApplyConfiguration for assertion in tests.
type captureApplier struct {
	inner   controller.MachineConfigApplier
	onApply func(address string, port int32, configData []byte)
}

var _ controller.MachineConfigApplier = (*captureApplier)(nil)

func (c *captureApplier) ApplyConfiguration(ctx context.Context, address string, port int32, configData []byte) error {
	if c.onApply != nil {
		c.onApply(address, port, configData)
	}
	return c.inner.ApplyConfiguration(ctx, address, port, configData)
}

func (c *captureApplier) IsOutOfMaintenance(ctx context.Context, address string) (bool, error) {
	return c.inner.IsOutOfMaintenance(ctx, address)
}
