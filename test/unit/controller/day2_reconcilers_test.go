// Package controller_test contains unit tests for the day-2 operational CRD
// reconcilers. These are pure unit tests — no envtest, no live Kubernetes API.
// All tests use controller-runtime's fake client.
package controller_test

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// fakeRecorder returns a buffered fake event recorder for use in tests.
func fakeRecorder() record.EventRecorder {
	return record.NewFakeRecorder(32)
}

// buildDay2Scheme returns a runtime.Scheme with platform, batch, and
// OperationalRunnerConfig types registered.
func buildDay2Scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add platformv1alpha1 scheme: %v", err)
	}
	if err := controller.AddOperationalRunnerConfigToScheme(s); err != nil {
		t.Fatalf("add OperationalRunnerConfig scheme: %v", err)
	}
	return s
}

// listRunnerConfigs returns all OperationalRunnerConfig objects in the fake client.
func listRunnerConfigs(t *testing.T, c interface {
	List(context.Context, *controller.OperationalRunnerConfigList, ...interface{}) error
}, scheme *runtime.Scheme) []controller.OperationalRunnerConfig {
	t.Helper()
	return nil // unused helper — inline List calls used instead for clarity
}

// --- EtcdMaintenance tests ---

// TestEtcdMaintenanceReconcile_LineageSyncedInitialized verifies that the first
// reconcile initializes LineageSynced=False/LineageControllerAbsent.
func TestEtcdMaintenanceReconcile_LineageSyncedInitialized(t *testing.T) {
	scheme := buildDay2Scheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-1", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(em).WithStatusSubresource(em).Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-1", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.EtcdMaintenance{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "backup-1", Namespace: "seam-tenant-test",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}

	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced)
	if cond == nil {
		t.Fatal("LineageSynced condition not set")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("LineageSynced status = %s, want False", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonLineageControllerAbsent {
		t.Errorf("LineageSynced reason = %s, want %s", cond.Reason, platformv1alpha1.ReasonLineageControllerAbsent)
	}
}

// TestEtcdMaintenanceReconcile_SubmitsRunnerConfig verifies that a RunnerConfig
// is submitted on the first reconcile for a backup operation when the default
// S3 Secret exists.
func TestEtcdMaintenanceReconcile_SubmitsRunnerConfig(t *testing.T) {
	scheme := buildDay2Scheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-1", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
	}
	// Pre-populate the cluster-wide default S3 Secret so the reconciler proceeds.
	defaultS3Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "seam-etcd-backup-config", Namespace: "seam-system"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(em, defaultS3Secret).WithStatusSubresource(em).Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-1", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after RunnerConfig submission")
	}

	// Verify a RunnerConfig was created (RC name = CR name).
	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Errorf("expected 1 RunnerConfig, got %d", len(rcList.Items))
	}
	if len(rcList.Items) > 0 {
		rc := rcList.Items[0]
		if rc.Name != "backup-1" {
			t.Errorf("RunnerConfig name = %q, want %q", rc.Name, "backup-1")
		}
		if len(rc.Spec.Steps) != 1 {
			t.Errorf("expected 1 step, got %d", len(rc.Spec.Steps))
		}
		if len(rc.Spec.Steps) > 0 && rc.Spec.Steps[0].Capability != "etcd-backup" {
			t.Errorf("step[0].Capability = %q, want %q", rc.Spec.Steps[0].Capability, "etcd-backup")
		}
		// S3 params should be set for backup.
		if len(rc.Spec.Steps) > 0 {
			if rc.Spec.Steps[0].Parameters["s3SecretName"] == "" {
				t.Error("step[0].Parameters[s3SecretName] should be set for backup")
			}
		}
	}
}

// TestEtcdMaintenanceReconcile_RunnerConfigComplete verifies that when a
// RunnerConfig's terminal Phase is "Completed", EtcdMaintenance transitions
// to Ready=True.
func TestEtcdMaintenanceReconcile_RunnerConfigComplete(t *testing.T) {
	scheme := buildDay2Scheme(t)
	rcName := "backup-1" // operationalRunnerConfigName("backup-1") = CR name
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-1", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
		Status: platformv1alpha1.EtcdMaintenanceStatus{
			JobName: rcName,
			Conditions: []metav1.Condition{
				{
					Type:   platformv1alpha1.ConditionTypeEtcdMaintenanceRunning,
					Status: metav1.ConditionTrue,
					Reason: platformv1alpha1.ReasonEtcdJobSubmitted,
				},
			},
		},
	}
	// Pre-existing RunnerConfig with terminal Completed state.
	rc := &controller.OperationalRunnerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: rcName, Namespace: "seam-tenant-test"},
		Status: controller.OperationalRunnerConfigStatus{
			Phase: "Completed",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, rc).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-1", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("unexpected requeue after completion: %v", result.RequeueAfter)
	}

	got := &platformv1alpha1.EtcdMaintenance{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "backup-1", Namespace: "seam-tenant-test",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}

	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeEtcdMaintenanceReady)
	if cond == nil {
		t.Fatal("Ready condition not set")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("Ready status = %s, want True", cond.Status)
	}
}

// --- ClusterReset tests ---

// TestClusterResetReconcile_ApprovalGate verifies that without the approval
// annotation, the reconciler sets PendingApproval and does not submit a
// RunnerConfig.
func TestClusterResetReconcile_ApprovalGate(t *testing.T) {
	scheme := buildDay2Scheme(t)
	crst := &platformv1alpha1.ClusterReset{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "reset-dev",
			Namespace:  "seam-tenant-dev",
			Generation: 1,
			// Note: no ontai.dev/reset-approved annotation.
		},
		Spec: platformv1alpha1.ClusterResetSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-dev"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crst).WithStatusSubresource(crst).Build()
	r := &controller.ClusterResetReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "reset-dev", Namespace: "seam-tenant-dev"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No requeue — controller waits for annotation write trigger.
	if result.RequeueAfter != 0 || result.Requeue {
		t.Errorf("should not requeue while waiting for approval, got %+v", result)
	}

	got := &platformv1alpha1.ClusterReset{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "reset-dev", Namespace: "seam-tenant-dev",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}

	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeResetPendingApproval)
	if cond == nil {
		t.Fatal("PendingApproval condition not set")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("PendingApproval status = %s, want True", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonApprovalRequired {
		t.Errorf("PendingApproval reason = %s, want %s", cond.Reason, platformv1alpha1.ReasonApprovalRequired)
	}

	// Verify no RunnerConfig was submitted.
	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 0 {
		t.Errorf("no RunnerConfig should be submitted without approval, got %d", len(rcList.Items))
	}
}

// TestClusterResetReconcile_ApprovedDirectPath verifies that with the approval
// annotation set and capi.enabled=false (no TalosCluster found), the reconciler
// submits the cluster-reset RunnerConfig directly.
func TestClusterResetReconcile_ApprovedDirectPath(t *testing.T) {
	scheme := buildDay2Scheme(t)
	crst := &platformv1alpha1.ClusterReset{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "reset-mgmt",
			Namespace:  "ont-system",
			Generation: 1,
			Annotations: map[string]string{
				platformv1alpha1.ResetApprovalAnnotation: "true",
			},
		},
		Spec: platformv1alpha1.ClusterResetSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crst).WithStatusSubresource(crst).Build()
	r := &controller.ClusterResetReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "reset-mgmt", Namespace: "ont-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after RunnerConfig submission")
	}

	// Verify the RunnerConfig was submitted.
	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Errorf("expected 1 cluster-reset RunnerConfig, got %d", len(rcList.Items))
	}
	if len(rcList.Items) > 0 {
		rc := rcList.Items[0]
		if len(rc.Spec.Steps) != 1 {
			t.Errorf("expected 1 step, got %d", len(rc.Spec.Steps))
		}
		if len(rc.Spec.Steps) > 0 && rc.Spec.Steps[0].Capability != "cluster-reset" {
			t.Errorf("step[0].Capability = %q, want cluster-reset", rc.Spec.Steps[0].Capability)
		}
	}
}

// --- HardeningProfile tests ---

// TestHardeningProfileReconcile_LineageSynced verifies that HardeningProfile
// initializes LineageSynced on first reconcile and does not error.
func TestHardeningProfileReconcile_LineageSynced(t *testing.T) {
	scheme := buildDay2Scheme(t)
	hp := &platformv1alpha1.HardeningProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "cis-l2", Namespace: "seam-tenant-dev", Generation: 1},
		Spec: platformv1alpha1.HardeningProfileSpec{
			Description: "CIS Level 2 hardening profile",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hp).WithStatusSubresource(hp).Build()
	r := &controller.HardeningProfileReconciler{Client: c, Scheme: scheme}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cis-l2", Namespace: "seam-tenant-dev"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// HardeningProfile is a config CR — no requeue.
	if result.RequeueAfter != 0 || result.Requeue {
		t.Errorf("HardeningProfile should not requeue, got %+v", result)
	}

	got := &platformv1alpha1.HardeningProfile{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "cis-l2", Namespace: "seam-tenant-dev",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}

	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced)
	if cond == nil {
		t.Fatal("LineageSynced condition not set")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("LineageSynced status = %s, want False", cond.Status)
	}
}

// --- PKIRotation tests ---

// TestPKIRotationReconcile_SubmitsRunnerConfig verifies a pki-rotate RunnerConfig
// is submitted with the correct step structure.
func TestPKIRotationReconcile_SubmitsRunnerConfig(t *testing.T) {
	scheme := buildDay2Scheme(t)
	pkir := &platformv1alpha1.PKIRotation{
		ObjectMeta: metav1.ObjectMeta{Name: "pkir-1", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.PKIRotationSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pkir).WithStatusSubresource(pkir).Build()
	r := &controller.PKIRotationReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "pkir-1", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after RunnerConfig submission")
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Errorf("expected 1 RunnerConfig, got %d", len(rcList.Items))
	}
	if len(rcList.Items) > 0 {
		rc := rcList.Items[0]
		// RC name = CR name.
		if rc.Name != "pkir-1" {
			t.Errorf("RunnerConfig name = %q, want %q", rc.Name, "pkir-1")
		}
		if len(rc.Spec.Steps) != 1 {
			t.Errorf("expected 1 step, got %d", len(rc.Spec.Steps))
		}
		if len(rc.Spec.Steps) > 0 {
			step := rc.Spec.Steps[0]
			if step.Capability != "pki-rotate" {
				t.Errorf("step.Capability = %q, want pki-rotate", step.Capability)
			}
			if !step.HaltOnFailure {
				t.Error("step.HaltOnFailure should be true")
			}
		}
	}
}

// --- NodeMaintenance tests ---

// TestNodeMaintenanceReconcile_SubmitsFourStepRunnerConfig verifies that a
// NodeMaintenance with operation=patch produces a RunnerConfig with the
// 4-step cordon→drain→operate→uncordon sequence.
func TestNodeMaintenanceReconcile_SubmitsFourStepRunnerConfig(t *testing.T) {
	scheme := buildDay2Scheme(t)
	nm := &platformv1alpha1.NodeMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "patch-1", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.NodeMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.NodeMaintenanceOperationPatch,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nm).WithStatusSubresource(nm).Build()
	r := &controller.NodeMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "patch-1", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after RunnerConfig submission")
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Fatalf("expected 1 RunnerConfig, got %d", len(rcList.Items))
	}

	rc := rcList.Items[0]
	if len(rc.Spec.Steps) != 4 {
		t.Fatalf("expected 4 steps (cordon/drain/operate/uncordon), got %d", len(rc.Spec.Steps))
	}

	// Verify step names and capabilities.
	wantSteps := []struct {
		name       string
		capability string
		dependsOn  string
		halt       bool
	}{
		{"cordon", "node-cordon", "", true},
		{"drain", "node-drain", "cordon", true},
		{"operate", "node-patch", "drain", false},
		{"uncordon", "node-uncordon", "operate", false},
	}
	for i, want := range wantSteps {
		s := rc.Spec.Steps[i]
		if s.Name != want.name {
			t.Errorf("step[%d].Name = %q, want %q", i, s.Name, want.name)
		}
		if s.Capability != want.capability {
			t.Errorf("step[%d].Capability = %q, want %q", i, s.Capability, want.capability)
		}
		if s.DependsOn != want.dependsOn {
			t.Errorf("step[%d].DependsOn = %q, want %q", i, s.DependsOn, want.dependsOn)
		}
		if s.HaltOnFailure != want.halt {
			t.Errorf("step[%d].HaltOnFailure = %v, want %v", i, s.HaltOnFailure, want.halt)
		}
	}
}

// --- ClusterMaintenance tests ---

// TestClusterMaintenanceReconcile_NoBlockOutsideWindows verifies that when
// blockOutsideWindows=false, the cluster is never paused.
func TestClusterMaintenanceReconcile_NoBlockOutsideWindows(t *testing.T) {
	scheme := buildDay2Scheme(t)
	cm := &platformv1alpha1.ClusterMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "maint-1", Namespace: "seam-tenant-dev", Generation: 1},
		Spec: platformv1alpha1.ClusterMaintenanceSpec{
			ClusterRef:          platformv1alpha1.LocalObjectRef{Name: "ccs-dev"},
			BlockOutsideWindows: false,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).WithStatusSubresource(cm).Build()
	r := &controller.ClusterMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "maint-1", Namespace: "seam-tenant-dev"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should requeue periodically to evaluate windows.
	if result.RequeueAfter == 0 {
		t.Error("ClusterMaintenance should requeue for periodic window evaluation")
	}

	got := &platformv1alpha1.ClusterMaintenance{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "maint-1", Namespace: "seam-tenant-dev",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}

	pausedCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeClusterMaintenancePaused)
	if pausedCond == nil {
		t.Fatal("Paused condition not set")
	}
	if pausedCond.Status != metav1.ConditionFalse {
		t.Errorf("Paused status = %s, want False (no blocking when blockOutsideWindows=false)", pausedCond.Status)
	}

	lineageCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced)
	if lineageCond == nil {
		t.Fatal("LineageSynced condition not set")
	}
}

// --- UpgradePolicy tests ---

// TestUpgradePolicyReconcile_DirectPath verifies that for a non-CAPI cluster
// (TalosCluster not found), a talos-upgrade RunnerConfig is submitted directly.
func TestUpgradePolicyReconcile_DirectPath(t *testing.T) {
	scheme := buildDay2Scheme(t)
	up := &platformv1alpha1.UpgradePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "upgrade-1", Namespace: "ont-system", Generation: 1},
		Spec: platformv1alpha1.UpgradePolicySpec{
			ClusterRef:         platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			UpgradeType:        platformv1alpha1.UpgradeTypeTalos,
			TargetTalosVersion: "v1.9.0",
			RollingStrategy:    platformv1alpha1.RollingStrategySequential,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(up).WithStatusSubresource(up).Build()
	r := &controller.UpgradePolicyReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "upgrade-1", Namespace: "ont-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after RunnerConfig submission")
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Errorf("expected 1 talos-upgrade RunnerConfig, got %d", len(rcList.Items))
	}
	if len(rcList.Items) > 0 {
		rc := rcList.Items[0]
		if len(rc.Spec.Steps) != 1 {
			t.Errorf("talos upgrade: expected 1 step, got %d", len(rc.Spec.Steps))
		}
		if len(rc.Spec.Steps) > 0 && rc.Spec.Steps[0].Capability != "talos-upgrade" {
			t.Errorf("step[0].Capability = %q, want talos-upgrade", rc.Spec.Steps[0].Capability)
		}
	}
}

// TestUpgradePolicyReconcile_StackUpgradeTwoSteps verifies that a stack upgrade
// produces a 2-step RunnerConfig with talos-upgrade→kube-upgrade dependency.
func TestUpgradePolicyReconcile_StackUpgradeTwoSteps(t *testing.T) {
	scheme := buildDay2Scheme(t)
	up := &platformv1alpha1.UpgradePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "stack-1", Namespace: "ont-system", Generation: 1},
		Spec: platformv1alpha1.UpgradePolicySpec{
			ClusterRef:               platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			UpgradeType:              platformv1alpha1.UpgradeTypeStack,
			TargetTalosVersion:       "v1.9.0",
			TargetKubernetesVersion:  "v1.31.0",
			RollingStrategy:          platformv1alpha1.RollingStrategySequential,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(up).WithStatusSubresource(up).Build()
	r := &controller.UpgradePolicyReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "stack-1", Namespace: "ont-system"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Fatalf("expected 1 RunnerConfig, got %d", len(rcList.Items))
	}

	rc := rcList.Items[0]
	if len(rc.Spec.Steps) != 2 {
		t.Fatalf("stack upgrade: expected 2 steps, got %d", len(rc.Spec.Steps))
	}
	if rc.Spec.Steps[0].Capability != "talos-upgrade" {
		t.Errorf("step[0].Capability = %q, want talos-upgrade", rc.Spec.Steps[0].Capability)
	}
	if rc.Spec.Steps[1].Capability != "kube-upgrade" {
		t.Errorf("step[1].Capability = %q, want kube-upgrade", rc.Spec.Steps[1].Capability)
	}
	if rc.Spec.Steps[1].DependsOn != "talos-upgrade" {
		t.Errorf("step[1].DependsOn = %q, want talos-upgrade", rc.Spec.Steps[1].DependsOn)
	}
}

// --- NodeOperation tests ---

// TestNodeOperationReconcile_DirectScaleUp verifies that for a non-CAPI cluster,
// a node-scale-up RunnerConfig is submitted.
func TestNodeOperationReconcile_DirectScaleUp(t *testing.T) {
	scheme := buildDay2Scheme(t)
	nop := &platformv1alpha1.NodeOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "scale-1", Namespace: "ont-system", Generation: 1},
		Spec: platformv1alpha1.NodeOperationSpec{
			ClusterRef:   platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			Operation:    platformv1alpha1.NodeOperationTypeScaleUp,
			ReplicaCount: 4,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nop).WithStatusSubresource(nop).Build()
	r := &controller.NodeOperationReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "scale-1", Namespace: "ont-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after RunnerConfig submission")
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Errorf("expected 1 node-scale-up RunnerConfig, got %d", len(rcList.Items))
	}
	if len(rcList.Items) > 0 {
		rc := rcList.Items[0]
		if len(rc.Spec.Steps) != 1 {
			t.Errorf("expected 1 step, got %d", len(rc.Spec.Steps))
		}
		if len(rc.Spec.Steps) > 0 && rc.Spec.Steps[0].Capability != "node-scale-up" {
			t.Errorf("step[0].Capability = %q, want node-scale-up", rc.Spec.Steps[0].Capability)
		}
	}
}

// --- Self-operation node exclusion tests ---

// TestEtcdMaintenanceReconcile_S3AbsentBlocksRunnerConfig verifies that when no
// S3 backup destination is configured, the reconciler sets EtcdBackupDestinationAbsent
// and skips RunnerConfig creation. platform-schema.md §10.
func TestEtcdMaintenanceReconcile_S3AbsentBlocksRunnerConfig(t *testing.T) {
	scheme := buildDay2Scheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-2", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(em).WithStatusSubresource(em).Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-2", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("should not requeue without S3 config, got RequeueAfter=%v", result.RequeueAfter)
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 0 {
		t.Errorf("expected no RunnerConfig when S3 absent, got %d", len(rcList.Items))
	}

	got := &platformv1alpha1.EtcdMaintenance{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "backup-2", Namespace: "seam-tenant-test",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.EtcdBackupDestinationAbsent)
	if cond == nil {
		t.Fatal("EtcdBackupDestinationAbsent condition not set")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("EtcdBackupDestinationAbsent status = %s, want True", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonEtcdBackupDestinationAbsent {
		t.Errorf("EtcdBackupDestinationAbsent reason = %s, want %s",
			cond.Reason, platformv1alpha1.ReasonEtcdBackupDestinationAbsent)
	}
}

// TestEtcdMaintenanceReconcile_S3SecretRefPresent verifies that when
// spec.etcdBackupS3SecretRef references an existing Secret, the RunnerConfig
// is submitted with S3 params in the step Parameters.
// platform-schema.md §10.
func TestEtcdMaintenanceReconcile_S3SecretRefPresent(t *testing.T) {
	scheme := buildDay2Scheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-3", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
			EtcdBackupS3SecretRef: &corev1.SecretReference{
				Name:      "my-s3-config",
				Namespace: "seam-system",
			},
		},
	}
	s3Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-s3-config", Namespace: "seam-system"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(em, s3Secret).WithStatusSubresource(em).Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-3", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after RunnerConfig submission")
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Errorf("expected 1 RunnerConfig when explicit S3 SecretRef present, got %d", len(rcList.Items))
	}
	if len(rcList.Items) > 0 {
		rc := rcList.Items[0]
		if len(rc.Spec.Steps) > 0 {
			params := rc.Spec.Steps[0].Parameters
			if params["s3SecretName"] != "my-s3-config" {
				t.Errorf("step.Parameters[s3SecretName] = %q, want my-s3-config", params["s3SecretName"])
			}
			if params["s3SecretNamespace"] != "seam-system" {
				t.Errorf("step.Parameters[s3SecretNamespace] = %q, want seam-system", params["s3SecretNamespace"])
			}
		}
	}
}

// TestEtcdMaintenanceReconcile_DefragNoS3Check verifies that defrag operations
// do not require S3 config and proceed to RunnerConfig submission regardless.
func TestEtcdMaintenanceReconcile_DefragNoS3Check(t *testing.T) {
	scheme := buildDay2Scheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "defrag-1", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationDefrag,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(em).WithStatusSubresource(em).Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "defrag-1", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after RunnerConfig submission for defrag")
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Errorf("expected 1 etcd-defrag RunnerConfig, got %d", len(rcList.Items))
	}
	if len(rcList.Items) > 0 {
		rc := rcList.Items[0]
		if len(rc.Spec.Steps) > 0 && rc.Spec.Steps[0].Capability != "etcd-defrag" {
			t.Errorf("step[0].Capability = %q, want etcd-defrag", rc.Spec.Steps[0].Capability)
		}
	}
}

// TestEtcdMaintenanceReconcile_NodeExclusionsApplied verifies that when the
// platform-leader Lease exists and its holder pod is found, the submitted
// RunnerConfig has MaintenanceTargetNodes and OperatorLeaderNode set.
// conductor-schema.md §13.
func TestEtcdMaintenanceReconcile_NodeExclusionsApplied(t *testing.T) {
	scheme := buildDay2Scheme(t)

	holderPodName := "platform-operator-pod"
	holderIdentity := holderPodName + "_abc-uid-123"
	leaderNode := "control-plane-1"
	targetNode := "worker-1"

	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "defrag-3", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef:  platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:   platformv1alpha1.EtcdMaintenanceOperationDefrag,
			TargetNodes: []string{targetNode},
		},
	}
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-leader", Namespace: "seam-system"},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holderIdentity},
	}
	leaderPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: holderPodName, Namespace: "seam-system"},
		Spec:       corev1.PodSpec{NodeName: leaderNode},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, lease, leaderPod).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "defrag-3", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Fatalf("expected 1 RunnerConfig, got %d", len(rcList.Items))
	}

	rc := rcList.Items[0]
	// Verify OperatorLeaderNode is set.
	if rc.Spec.OperatorLeaderNode != leaderNode {
		t.Errorf("OperatorLeaderNode = %q, want %q", rc.Spec.OperatorLeaderNode, leaderNode)
	}
	// Verify MaintenanceTargetNodes includes the target node and leader node.
	wantExclusions := map[string]bool{leaderNode: true, targetNode: true}
	for _, v := range rc.Spec.MaintenanceTargetNodes {
		delete(wantExclusions, v)
	}
	if len(wantExclusions) > 0 {
		t.Errorf("MaintenanceTargetNodes missing values: %v (got %v)",
			wantExclusions, rc.Spec.MaintenanceTargetNodes)
	}
}

// TestEtcdMaintenanceReconcile_NoLeaseGraceful verifies that when the
// platform-leader Lease is absent, the reconciler proceeds without node
// exclusions rather than returning an error. conductor-schema.md §13.
func TestEtcdMaintenanceReconcile_NoLeaseGraceful(t *testing.T) {
	scheme := buildDay2Scheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "defrag-2", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationDefrag,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(em).WithStatusSubresource(em).Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "defrag-2", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error when Lease absent: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after RunnerConfig submission even without Lease")
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Errorf("expected 1 RunnerConfig when Lease absent, got %d", len(rcList.Items))
	}
	if len(rcList.Items) > 0 {
		rc := rcList.Items[0]
		if rc.Spec.OperatorLeaderNode != "" {
			t.Errorf("expected empty OperatorLeaderNode when no Lease, got %q", rc.Spec.OperatorLeaderNode)
		}
		if len(rc.Spec.MaintenanceTargetNodes) != 0 {
			t.Errorf("expected empty MaintenanceTargetNodes when no targets and no Lease, got %v",
				rc.Spec.MaintenanceTargetNodes)
		}
	}
}

// --- MaintenanceBundle tests (unchanged — still submits Jobs directly) ---

// TestMaintenanceBundleReconcile_LineageSyncedInitialized verifies that the first
// reconcile initializes LineageSynced=False/LineageControllerAbsent.
func TestMaintenanceBundleReconcile_LineageSyncedInitialized(t *testing.T) {
	scheme := buildDay2Scheme(t)
	mb := &platformv1alpha1.MaintenanceBundle{
		ObjectMeta: metav1.ObjectMeta{Name: "test-etcd-backup", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.MaintenanceBundleSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.MaintenanceBundleOperationEtcdBackup,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mb).WithStatusSubresource(mb).Build()
	r := &controller.MaintenanceBundleReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-etcd-backup", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.MaintenanceBundle{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "test-etcd-backup", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}

	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced)
	if cond == nil {
		t.Fatal("LineageSynced condition not set")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("LineageSynced status = %s, want False", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonLineageControllerAbsent {
		t.Errorf("LineageSynced reason = %s, want %s", cond.Reason, platformv1alpha1.ReasonLineageControllerAbsent)
	}
}

// TestMaintenanceBundleReconcile_SubmitsJobWithPreEncodedContext verifies that
// the reconciler creates a Job using the pre-encoded scheduling context from the
// bundle spec (no live cluster queries).
func TestMaintenanceBundleReconcile_SubmitsJobWithPreEncodedContext(t *testing.T) {
	scheme := buildDay2Scheme(t)
	mb := &platformv1alpha1.MaintenanceBundle{
		ObjectMeta: metav1.ObjectMeta{Name: "test-drain", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.MaintenanceBundleSpec{
			ClusterRef:             platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:              platformv1alpha1.MaintenanceBundleOperationDrain,
			MaintenanceTargetNodes: []string{"worker-1", "worker-2"},
			OperatorLeaderNode:     "control-plane-1",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mb).WithStatusSubresource(mb).Build()
	r := &controller.MaintenanceBundleReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-drain", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after job submission")
	}

	// Verify Job was created.
	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("expected 1 Job, got %d", len(jobList.Items))
	}

	job := jobList.Items[0]
	// Verify node exclusions applied: worker-1, worker-2, control-plane-1.
	if job.Spec.Template.Spec.Affinity == nil {
		t.Fatal("expected Affinity on Job with node exclusions")
	}
	terms := job.Spec.Template.Spec.Affinity.NodeAffinity.
		RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 || len(terms[0].MatchExpressions) != 1 {
		t.Fatal("unexpected NodeSelectorTerms shape")
	}
	excluded := terms[0].MatchExpressions[0].Values
	if len(excluded) != 3 {
		t.Errorf("expected 3 excluded nodes, got %d: %v", len(excluded), excluded)
	}

	// Verify Pending condition set with JobSubmitted reason.
	got := &platformv1alpha1.MaintenanceBundle{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "test-drain", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeMaintenanceBundlePending)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Pending=True after job submission")
	}
	if cond.Reason != platformv1alpha1.ReasonMaintenanceBundleJobSubmitted {
		t.Errorf("Pending reason = %s, want JobSubmitted", cond.Reason)
	}
}

// TestMaintenanceBundleReconcile_JobSucceeds verifies that the reconciler transitions
// to Ready=True when the OperationResult ConfigMap reports success.
func TestMaintenanceBundleReconcile_JobSucceeds(t *testing.T) {
	scheme := buildDay2Scheme(t)
	mb := &platformv1alpha1.MaintenanceBundle{
		ObjectMeta: metav1.ObjectMeta{Name: "test-etcd-backup", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.MaintenanceBundleSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.MaintenanceBundleOperationEtcdBackup,
		},
	}
	// Pre-existing Job and success result ConfigMap.
	existingJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-etcd-backup-etcd-backup",
			Namespace: "seam-system",
		},
	}
	resultCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-etcd-backup-etcd-backup-result",
			Namespace: "seam-system",
		},
		Data: map[string]string{"status": "success", "message": "backup completed"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(mb, existingJob, resultCM).WithStatusSubresource(mb).Build()
	r := &controller.MaintenanceBundleReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-etcd-backup", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.MaintenanceBundle{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "test-etcd-backup", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}

	readyCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeMaintenanceBundleReady)
	if readyCond == nil || readyCond.Status != metav1.ConditionTrue {
		t.Error("expected Ready=True after job success")
	}
	if readyCond.Reason != platformv1alpha1.ReasonMaintenanceBundleJobComplete {
		t.Errorf("Ready reason = %s, want JobComplete", readyCond.Reason)
	}
	if got.Status.OperationResult != "backup completed" {
		t.Errorf("OperationResult = %q, want %q", got.Status.OperationResult, "backup completed")
	}
}

// TestMaintenanceBundleReconcile_JobFails verifies that the reconciler transitions
// to Degraded=True when the OperationResult ConfigMap reports failure.
func TestMaintenanceBundleReconcile_JobFails(t *testing.T) {
	scheme := buildDay2Scheme(t)
	mb := &platformv1alpha1.MaintenanceBundle{
		ObjectMeta: metav1.ObjectMeta{Name: "test-etcd-backup", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.MaintenanceBundleSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.MaintenanceBundleOperationEtcdBackup,
		},
	}
	existingJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-etcd-backup-etcd-backup",
			Namespace: "seam-system",
		},
	}
	resultCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-etcd-backup-etcd-backup-result",
			Namespace: "seam-system",
		},
		Data: map[string]string{"status": "failed", "message": "S3 unreachable"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(mb, existingJob, resultCM).WithStatusSubresource(mb).Build()
	r := &controller.MaintenanceBundleReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-etcd-backup", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.MaintenanceBundle{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "test-etcd-backup", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}

	degradedCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeMaintenanceBundleDegraded)
	if degradedCond == nil || degradedCond.Status != metav1.ConditionTrue {
		t.Error("expected Degraded=True after job failure")
	}
	if degradedCond.Reason != platformv1alpha1.ReasonMaintenanceBundleJobFailed {
		t.Errorf("Degraded reason = %s, want JobFailed", degradedCond.Reason)
	}
	if got.Status.OperationResult != "S3 unreachable" {
		t.Errorf("OperationResult = %q, want %q", got.Status.OperationResult, "S3 unreachable")
	}
}

// TestMaintenanceBundleReconcile_IdempotentAfterSuccess verifies that a second
// reconcile on a Ready=True bundle is a no-op.
func TestMaintenanceBundleReconcile_IdempotentAfterSuccess(t *testing.T) {
	scheme := buildDay2Scheme(t)
	mb := &platformv1alpha1.MaintenanceBundle{
		ObjectMeta: metav1.ObjectMeta{Name: "test-done", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.MaintenanceBundleSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.MaintenanceBundleOperationUpgrade,
		},
		Status: platformv1alpha1.MaintenanceBundleStatus{
			Conditions: []metav1.Condition{
				{
					Type:               platformv1alpha1.ConditionTypeMaintenanceBundleReady,
					Status:             metav1.ConditionTrue,
					Reason:             platformv1alpha1.ReasonMaintenanceBundleJobComplete,
					Message:            "complete",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mb).WithStatusSubresource(mb).Build()
	r := &controller.MaintenanceBundleReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-done", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue on completed bundle, got RequeueAfter=%v", result.RequeueAfter)
	}

	// No Job should have been created.
	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobList.Items) != 0 {
		t.Errorf("expected no Jobs created on idempotent reconcile, got %d", len(jobList.Items))
	}
}
