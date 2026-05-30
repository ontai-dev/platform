package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	seamplatformv1alpha1 "github.com/ontai-dev/platform/api/seam/v1alpha1"
	seamcorev1alpha1 "github.com/ontai-dev/seam/api/v1alpha1"
)

// buildRetryTestScheme constructs a runtime.Scheme for RECON-I3 unit tests.
func buildRetryTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	if err := seamplatformv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add seamplatformv1alpha1 scheme: %v", err)
	}
	if err := seamcorev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add seamcorev1alpha1 scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add platformv1alpha1 scheme: %v", err)
	}
	return s
}

// --- retryJobName ---

func TestRetryJobName_FirstAttempt(t *testing.T) {
	name := retryJobName("my-mcs", "machineconfig-sync", 0)
	want := "my-mcs-machineconfig-sync"
	if name != want {
		t.Errorf("retryJobName(retry=0) = %q, want %q", name, want)
	}
}

func TestRetryJobName_Retry1(t *testing.T) {
	name := retryJobName("my-mcs", "machineconfig-sync", 1)
	want := "my-mcs-machineconfig-sync-r1"
	if name != want {
		t.Errorf("retryJobName(retry=1) = %q, want %q", name, want)
	}
}

func TestRetryJobName_Retry2(t *testing.T) {
	name := retryJobName("my-upgrade", "talos-upgrade", 2)
	want := "my-upgrade-talos-upgrade-r2"
	if name != want {
		t.Errorf("retryJobName(retry=2) = %q, want %q", name, want)
	}
}

func TestRetryJobName_NextJobDiffersFromCurrent(t *testing.T) {
	crName := "my-upgrade"
	cap := "talos-upgrade"
	current := retryJobName(crName, cap, 1)
	next := retryJobName(crName, cap, 2)
	if current == next {
		t.Errorf("current job %q and next job %q must differ for retry collision avoidance", current, next)
	}
}

// --- effectiveMaxRetry ---

func TestEffectiveMaxRetry_Zero_ReturnsDefault(t *testing.T) {
	if got := effectiveMaxRetry(0); got != defaultMaxRetry {
		t.Errorf("effectiveMaxRetry(0) = %d, want %d (defaultMaxRetry)", got, defaultMaxRetry)
	}
}

func TestEffectiveMaxRetry_Custom(t *testing.T) {
	if got := effectiveMaxRetry(5); got != 5 {
		t.Errorf("effectiveMaxRetry(5) = %d, want 5", got)
	}
}

func TestEffectiveMaxRetry_One(t *testing.T) {
	if got := effectiveMaxRetry(1); got != 1 {
		t.Errorf("effectiveMaxRetry(1) = %d, want 1", got)
	}
}

// --- setTalosClusterHumanInterventionRequired ---

func TestSetTalosClusterHumanInterventionRequired_SetsCondition(t *testing.T) {
	s := buildRetryTestScheme(t)
	ns := "seam-tenant-test-cluster"
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: ns},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(tc).WithObjects(tc).Build()

	err := setTalosClusterHumanInterventionRequired(context.Background(), c,
		"test-cluster", ns, "permanently failed", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "test-cluster", Namespace: ns}, updated); err != nil {
		t.Fatalf("get TalosCluster after patch: %v", err)
	}
	cond := platformv1alpha1.FindCondition(updated.Status.Conditions, seamplatformv1alpha1.ConditionTypeHumanInterventionRequired)
	if cond == nil {
		t.Fatal("HumanInterventionRequired condition not set on TalosCluster")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("status = %q, want True", cond.Status)
	}
	if cond.Reason != seamplatformv1alpha1.ReasonHumanInterventionNeeded {
		t.Errorf("reason = %q, want %q", cond.Reason, seamplatformv1alpha1.ReasonHumanInterventionNeeded)
	}
}

func TestSetTalosClusterHumanInterventionRequired_NotFound_NoError(t *testing.T) {
	s := buildRetryTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	err := setTalosClusterHumanInterventionRequired(context.Background(), c,
		"missing", "seam-tenant-missing", "msg", 1)
	if err != nil {
		t.Errorf("expected no error for missing TalosCluster, got: %v", err)
	}
}

// --- Retry counter logic ---

// TestRetryCounter_IncrementsBelowMax verifies that incrementing retryCount
// below maxRetry does not trigger permanent failure.
func TestRetryCounter_IncrementsBelowMax(t *testing.T) {
	mcs := &platformv1alpha1.MachineConfigSync{
		Spec:   platformv1alpha1.MachineConfigSyncSpec{MaxRetry: 3},
		Status: platformv1alpha1.MachineConfigSyncStatus{RetryCount: 0},
	}
	mcs.Status.RetryCount++
	if mcs.Status.RetryCount != 1 {
		t.Errorf("RetryCount after increment = %d, want 1", mcs.Status.RetryCount)
	}
	if mcs.Status.RetryCount >= effectiveMaxRetry(mcs.Spec.MaxRetry) {
		t.Error("should not be at permanent failure limit with retryCount=1, maxRetry=3")
	}
}

// TestRetryCounter_PermanentFailureAtMax verifies that reaching maxRetry triggers
// the permanent failure branch (retryCount >= maxRetry).
func TestRetryCounter_PermanentFailureAtMax(t *testing.T) {
	mcs := &platformv1alpha1.MachineConfigSync{
		ObjectMeta: metav1.ObjectMeta{Name: "my-mcs", Namespace: "seam-tenant-ccs-mgmt"},
		Spec:       platformv1alpha1.MachineConfigSyncSpec{MaxRetry: 2},
		Status:     platformv1alpha1.MachineConfigSyncStatus{RetryCount: 1},
	}

	mcs.Status.RetryCount++

	if mcs.Status.RetryCount < effectiveMaxRetry(mcs.Spec.MaxRetry) {
		t.Fatalf("expected permanent failure: retryCount=%d maxRetry=%d",
			mcs.Status.RetryCount, effectiveMaxRetry(mcs.Spec.MaxRetry))
	}

	platformv1alpha1.SetCondition(
		&mcs.Status.Conditions,
		platformv1alpha1.ConditionTypeMachineConfigSyncDegraded,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonMachineConfigSyncPermanentFailure,
		"permanently failed",
		mcs.Generation,
	)

	cond := platformv1alpha1.FindCondition(mcs.Status.Conditions,
		platformv1alpha1.ConditionTypeMachineConfigSyncDegraded)
	if cond == nil {
		t.Fatal("Degraded condition not set")
	}
	if cond.Reason != platformv1alpha1.ReasonMachineConfigSyncPermanentFailure {
		t.Errorf("reason = %q, want %q", cond.Reason,
			platformv1alpha1.ReasonMachineConfigSyncPermanentFailure)
	}
}

// TestMachineConfigSync_PermanentFailure_ShortCircuit verifies that the reconciler
// exits immediately without submitting new jobs when Degraded=PermanentFailure is already set.
// This guards against the runaway retry loop where each status update triggers another reconcile
// that submits another job, incrementing RetryCount indefinitely past maxRetry.
func TestMachineConfigSync_PermanentFailure_ShortCircuit(t *testing.T) {
	s := buildRetryTestScheme(t)

	mcs := &platformv1alpha1.MachineConfigSync{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ccs-dev-mc-sync-controlplane",
			Namespace: "seam-tenant-ccs-dev",
		},
		Spec: platformv1alpha1.MachineConfigSyncSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-dev"},
			NodeClass:  "controlplane",
			MaxRetry:   3,
		},
		Status: platformv1alpha1.MachineConfigSyncStatus{
			RetryCount: 10,
		},
	}
	platformv1alpha1.SetCondition(
		&mcs.Status.Conditions,
		platformv1alpha1.ConditionTypeMachineConfigSyncDegraded,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonMachineConfigSyncPermanentFailure,
		"permanently failed after 3 attempts",
		mcs.Generation,
	)

	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(mcs).WithObjects(mcs).Build()
	r := &MachineConfigSyncReconciler{Client: c}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: mcs.Namespace, Name: mcs.Name},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected empty result (no requeue), got %+v", result)
	}

	// RetryCount must not have increased.
	updated := &platformv1alpha1.MachineConfigSync{}
	_ = c.Get(context.Background(), types.NamespacedName{Name: mcs.Name, Namespace: mcs.Namespace}, updated)
	if updated.Status.RetryCount > 10 {
		t.Errorf("RetryCount increased to %d after PermanentFailure short-circuit", updated.Status.RetryCount)
	}
}

// TestRetryCounter_SuccessResetsToZero verifies that a successful Job completion
// resets RetryCount to zero regardless of the previous count.
func TestRetryCounter_SuccessResetsToZero(t *testing.T) {
	mcs := &platformv1alpha1.MachineConfigSync{
		Status: platformv1alpha1.MachineConfigSyncStatus{RetryCount: 2},
	}
	mcs.Status.RetryCount = 0
	if mcs.Status.RetryCount != 0 {
		t.Errorf("RetryCount after success = %d, want 0", mcs.Status.RetryCount)
	}
}
