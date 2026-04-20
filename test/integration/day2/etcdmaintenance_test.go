package day2_integration_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// TestEtcdMaintenance_Integration_BackupWithS3 verifies that the reconciler
// submits a RunnerConfig with S3 params when the default S3 Secret exists.
// Uses a real API server — validates SSA status patch semantics.
func TestEtcdMaintenance_Integration_BackupWithS3(t *testing.T) {
	ns := "seam-tenant-test"
	crName := fmt.Sprintf("em-backup-%d", time.Now().UnixNano())
	rcName := crName // operationalRunnerConfigName returns CR name

	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: ns, Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
	}
	defaultS3Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "seam-etcd-backup-config", Namespace: "seam-system"},
	}
	ctx := context.Background()
	if err := testClient.Create(ctx, defaultS3Secret); err != nil {
		_ = err // may already exist from prior run
	}
	if err := testClient.Create(ctx, em); err != nil {
		t.Fatalf("create EtcdMaintenance: %v", err)
	}
	t.Cleanup(func() {
		_ = testClient.Delete(ctx, em)
		_ = testClient.Delete(ctx, defaultS3Secret)
		_ = testClient.Delete(ctx, &controller.OperationalRunnerConfig{
			ObjectMeta: metav1.ObjectMeta{Name: rcName, Namespace: ns},
		})
	})

	r := &controller.EtcdMaintenanceReconciler{
		Client:   testClient,
		Scheme:   testScheme,
		Recorder: clientevents.NewFakeRecorder(16),
	}

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: crName, Namespace: ns},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after RunnerConfig submission")
	}

	// Verify RunnerConfig created with correct capability and S3 params.
	rc := &controller.OperationalRunnerConfig{}
	if err := testClient.Get(ctx, types.NamespacedName{Name: rcName, Namespace: ns}, rc); err != nil {
		t.Fatalf("get RunnerConfig: %v", err)
	}
	if len(rc.Spec.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(rc.Spec.Steps))
	}
	if len(rc.Spec.Steps) > 0 {
		if rc.Spec.Steps[0].Capability != "etcd-backup" {
			t.Errorf("step[0].Capability = %q, want etcd-backup", rc.Spec.Steps[0].Capability)
		}
		if rc.Spec.Steps[0].Parameters["s3SecretName"] == "" {
			t.Error("step[0].Parameters[s3SecretName] should be set")
		}
	}

	// Verify status was patched via SSA: LineageSynced and Running conditions set.
	got := &platformv1alpha1.EtcdMaintenance{}
	if err := testClient.Get(ctx, types.NamespacedName{Name: crName, Namespace: ns}, got); err != nil {
		t.Fatalf("get EtcdMaintenance: %v", err)
	}
	lineage := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced)
	if lineage == nil {
		t.Error("LineageSynced condition absent after reconcile")
	}
	running := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeEtcdMaintenanceRunning)
	if running == nil || running.Status != metav1.ConditionTrue {
		t.Error("Running=True condition absent after RunnerConfig submission")
	}
}

// TestEtcdMaintenance_Integration_S3AbsentSetsCondition verifies that when no S3
// Secret is present, EtcdBackupDestinationAbsent condition is set via real SSA
// status patch and no RunnerConfig is created.
func TestEtcdMaintenance_Integration_S3AbsentSetsCondition(t *testing.T) {
	ns := "seam-tenant-test"
	crName := fmt.Sprintf("em-s3absent-%d", time.Now().UnixNano())

	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: ns, Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
	}
	ctx := context.Background()
	if err := testClient.Create(ctx, em); err != nil {
		t.Fatalf("create EtcdMaintenance: %v", err)
	}
	t.Cleanup(func() {
		_ = testClient.Delete(ctx, em)
	})

	r := &controller.EtcdMaintenanceReconciler{
		Client:   testClient,
		Scheme:   testScheme,
		Recorder: clientevents.NewFakeRecorder(16),
	}

	if _, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: crName, Namespace: ns},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := &platformv1alpha1.EtcdMaintenance{}
	if err := testClient.Get(ctx, types.NamespacedName{Name: crName, Namespace: ns}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.EtcdBackupDestinationAbsent)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("EtcdBackupDestinationAbsent condition not set when S3 absent")
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := testClient.List(ctx, rcList, client.InNamespace(ns)); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 0 {
		t.Errorf("expected 0 RunnerConfigs when S3 absent, got %d", len(rcList.Items))
	}
}
