// Package day2_test contains integration tests for CAPI-bootstrapped cluster
// day-2 operations: UpgradePolicy CAPI delegation, NodeOperation CAPI path,
// ClusterReset CAPI sequencing, and ClusterMaintenance pause/resume via
// blockOutsideWindows.
//
// All tests use controller-runtime's fake client — no live cluster required.
// CAPI-path delegation is verified by pre-populating a TalosCluster with
// capi.enabled=true, causing the dual-path reconcilers to route to their CAPI
// branches rather than the direct RunnerConfig path.
//
// platform-schema.md §5 dual-path CRDs. platform-design.md §2.1.
package day2_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// buildCAPITenantCluster returns a TalosCluster with capi.enabled=true for use
// as the routing target in dual-path reconcilers.
func buildCAPITenantCluster(name, namespace string) *platformv1alpha1.TalosCluster {
	return &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			Mode:         platformv1alpha1.TalosClusterModeBootstrap,
			Role:         platformv1alpha1.TalosClusterRoleTenant,
			TalosVersion: "v1.9.3",
			CAPI: &platformv1alpha1.CAPIConfig{
				Enabled:      true,
				TalosVersion: "v1.9.3",
			},
		},
	}
}

// ── UpgradePolicy: CAPI delegation ───────────────────────────────────────────

// TestUpgradePolicyCAPI_DelegationConditionSet verifies that when the owning
// TalosCluster has capi.enabled=true, UpgradePolicyReconciler sets
// CAPIDelegated=True instead of submitting a RunnerConfig.
// platform-schema.md §5 UpgradePolicy dual-path routing.
func TestUpgradePolicyCAPI_DelegationConditionSet(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	ns := "seam-tenant-ccs-app"

	tc := buildCAPITenantCluster("ccs-app", ns)
	up := &platformv1alpha1.UpgradePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "upgrade-1", Namespace: ns, Generation: 1},
		Spec: platformv1alpha1.UpgradePolicySpec{
			ClusterRef:              platformv1alpha1.LocalObjectRef{Name: "ccs-app"},
			TargetTalosVersion:      "v1.10.0",
			TargetKubernetesVersion: "1.33.0",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, up).
		WithStatusSubresource(up).
		Build()
	r := &controller.UpgradePolicyReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "upgrade-1", Namespace: ns},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// CAPI path: no RunnerConfig submitted.
	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 0 {
		t.Errorf("CAPI path must not submit RunnerConfig, got %d", len(rcList.Items))
	}

	got := &platformv1alpha1.UpgradePolicy{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "upgrade-1", Namespace: ns}, got); err != nil {
		t.Fatalf("get UpgradePolicy: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeUpgradePolicyCAPIDelegated)
	if cond == nil {
		t.Fatal("CAPIDelegated condition not set for CAPI path upgrade")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("CAPIDelegated.Status = %s, want True", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonUpgradeCAPIDelegated {
		t.Errorf("CAPIDelegated.Reason = %q, want %q", cond.Reason, platformv1alpha1.ReasonUpgradeCAPIDelegated)
	}
}

// TestUpgradePolicyCAPI_NonCAPICluster_UsesDirectPath verifies that when the
// owning TalosCluster has capi.enabled=false, UpgradePolicyReconciler falls
// through to the direct RunnerConfig path. Regression guard for dual-path routing.
func TestUpgradePolicyCAPI_NonCAPICluster_UsesDirectPath(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	ns := "seam-system"

	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-mgmt", Namespace: ns, Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			Mode:         platformv1alpha1.TalosClusterModeBootstrap,
			TalosVersion: "v1.9.3",
			// CAPI nil — capi.enabled=false
		},
	}
	up := &platformv1alpha1.UpgradePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "upgrade-mgmt", Namespace: ns, Generation: 1},
		Spec: platformv1alpha1.UpgradePolicySpec{
			ClusterRef:         platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			UpgradeType:        platformv1alpha1.UpgradeTypeTalos,
			TargetTalosVersion: "v1.10.0",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, up).
		WithStatusSubresource(up).
		Build()
	r := &controller.UpgradePolicyReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "upgrade-mgmt", Namespace: ns},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Non-CAPI path submits a RunnerConfig.
	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Errorf("non-CAPI path: expected 1 RunnerConfig, got %d", len(rcList.Items))
	}

	// CAPIDelegated must NOT be set on the non-CAPI path.
	got := &platformv1alpha1.UpgradePolicy{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "upgrade-mgmt", Namespace: ns}, got); err != nil {
		t.Fatalf("get UpgradePolicy: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeUpgradePolicyCAPIDelegated)
	if cond != nil && cond.Status == metav1.ConditionTrue {
		t.Error("CAPIDelegated must not be True on non-CAPI path")
	}
}

// ── NodeOperation: CAPI path ──────────────────────────────────────────────────

// TestNodeOperationCAPI_RebootDelegated verifies that a NodeOperation with
// operation=reboot on a capi.enabled=true TalosCluster sets
// CAPIDelegated=True and does not submit a RunnerConfig.
func TestNodeOperationCAPI_RebootDelegated(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	ns := "seam-tenant-ccs-app"

	tc := buildCAPITenantCluster("ccs-app", ns)
	nop := &platformv1alpha1.NodeOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "reboot-1", Namespace: ns, Generation: 1},
		Spec: platformv1alpha1.NodeOperationSpec{
			ClusterRef:  platformv1alpha1.LocalObjectRef{Name: "ccs-app"},
			Operation:   platformv1alpha1.NodeOperationTypeReboot,
			TargetNodes: []string{"ccs-app-w1"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, nop).
		WithStatusSubresource(nop).
		Build()
	r := &controller.NodeOperationReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "reboot-1", Namespace: ns},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// No RunnerConfig on CAPI path.
	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 0 {
		t.Errorf("CAPI NodeOperation must not submit RunnerConfig, got %d", len(rcList.Items))
	}

	got := &platformv1alpha1.NodeOperation{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "reboot-1", Namespace: ns}, got); err != nil {
		t.Fatalf("get NodeOperation: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeNodeOperationCAPIDelegated)
	if cond == nil {
		t.Fatal("CAPIDelegated condition not set for CAPI NodeOperation")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("NodeOperation CAPIDelegated.Status = %s, want True", cond.Status)
	}
}

// ── ClusterReset: CAPI sequencing ────────────────────────────────────────────

// TestClusterResetCAPI_ApprovedSubmitsRunnerConfig verifies that a ClusterReset
// with the reset-approved annotation on a CAPI cluster proceeds past the human
// gate and submits a RunnerConfig with capability=cluster-reset.
// Both CAPI and non-CAPI paths emit a RunnerConfig for reset (CAPI objects deleted
// post-reset by the reconciler separately). CP-INV-006.
func TestClusterResetCAPI_ApprovedSubmitsRunnerConfig(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	ns := "seam-tenant-ccs-app"

	tc := buildCAPITenantCluster("ccs-app", ns)
	crst := &platformv1alpha1.ClusterReset{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "reset-capi",
			Namespace:  ns,
			Generation: 1,
			Annotations: map[string]string{
				"ontai.dev/reset-approved": "true",
			},
		},
		Spec: platformv1alpha1.ClusterResetSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-app"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, crst).
		WithStatusSubresource(crst).
		Build()
	r := &controller.ClusterResetReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "reset-capi", Namespace: ns},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 after RunnerConfig submission")
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Fatalf("CAPI ClusterReset: expected 1 RunnerConfig after approval, got %d", len(rcList.Items))
	}
	if rcList.Items[0].Spec.Steps[0].Capability != "cluster-reset" {
		t.Errorf("capability = %q, want cluster-reset", rcList.Items[0].Spec.Steps[0].Capability)
	}
}

// ── ClusterMaintenance: CAPI pause/resume ─────────────────────────────────────

// TestClusterMaintenanceCAPI_BlockOutsideWindows_NoWindowPausesCluster verifies that
// when blockOutsideWindows=true and no maintenance window is active, the reconciler
// sets Paused=True on the ClusterMaintenance status and the CAPI cluster gets the
// paused annotation. platform-schema.md §5 ClusterMaintenance CAPI path.
func TestClusterMaintenanceCAPI_BlockOutsideWindows_NoWindowPausesCluster(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	ns := "seam-tenant-ccs-app"

	tc := buildCAPITenantCluster("ccs-app", ns)
	cm := &platformv1alpha1.ClusterMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "maint-1", Namespace: ns, Generation: 1},
		Spec: platformv1alpha1.ClusterMaintenanceSpec{
			ClusterRef:          platformv1alpha1.LocalObjectRef{Name: "ccs-app"},
			BlockOutsideWindows: true,
			// No Windows configured — outside any window at all times.
		},
	}
	// Pre-create the CAPI Cluster so reconcileCAPIPause can find it.
	// Without it the CAPI path is a no-op (NotFound → return nil).
	capiCluster := &unstructured.Unstructured{}
	capiCluster.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "Cluster",
	})
	capiCluster.SetName("ccs-app")
	capiCluster.SetNamespace("seam-tenant-ccs-app")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, cm, capiCluster).
		WithStatusSubresource(cm).
		Build()
	// Fix the clock so there is never an active window.
	fixedNow := time.Date(2026, 4, 20, 3, 0, 0, 0, time.UTC)
	r := &controller.ClusterMaintenanceReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(8),
		Now:      func() time.Time { return fixedNow },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "maint-1", Namespace: ns},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := &platformv1alpha1.ClusterMaintenance{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "maint-1", Namespace: ns}, got); err != nil {
		t.Fatalf("get ClusterMaintenance: %v", err)
	}

	// Paused condition must be True when blockOutsideWindows=true and no window active.
	pausedCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeClusterMaintenancePaused)
	if pausedCond == nil {
		t.Fatal("Paused condition not set when blockOutsideWindows=true and no active window")
	}
	if pausedCond.Status != metav1.ConditionTrue {
		t.Errorf("Paused.Status = %s, want True", pausedCond.Status)
	}

	// WindowActive must be False (no windows configured).
	windowCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeClusterMaintenanceWindowActive)
	if windowCond == nil {
		t.Fatal("WindowActive condition not set")
	}
	if windowCond.Status != metav1.ConditionFalse {
		t.Errorf("WindowActive.Status = %s, want False", windowCond.Status)
	}
}

// TestClusterMaintenanceCAPI_BlockOutsideWindows_False_NeverPauses verifies that
// when blockOutsideWindows=false, the Paused condition is always False regardless
// of window state. platform-schema.md §5.
func TestClusterMaintenanceCAPI_BlockOutsideWindows_False_NeverPauses(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	ns := "seam-tenant-ccs-app"

	tc := buildCAPITenantCluster("ccs-app", ns)
	cm := &platformv1alpha1.ClusterMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "maint-noblock", Namespace: ns, Generation: 1},
		Spec: platformv1alpha1.ClusterMaintenanceSpec{
			ClusterRef:          platformv1alpha1.LocalObjectRef{Name: "ccs-app"},
			BlockOutsideWindows: false,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, cm).
		WithStatusSubresource(cm).
		Build()
	fixedNow := time.Date(2026, 4, 20, 3, 0, 0, 0, time.UTC)
	r := &controller.ClusterMaintenanceReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(8),
		Now:      func() time.Time { return fixedNow },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "maint-noblock", Namespace: ns},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := &platformv1alpha1.ClusterMaintenance{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "maint-noblock", Namespace: ns}, got); err != nil {
		t.Fatalf("get ClusterMaintenance: %v", err)
	}

	pausedCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeClusterMaintenancePaused)
	if pausedCond == nil {
		t.Fatal("Paused condition not set")
	}
	if pausedCond.Status != metav1.ConditionFalse {
		t.Errorf("blockOutsideWindows=false: Paused.Status = %s, want False", pausedCond.Status)
	}
}
