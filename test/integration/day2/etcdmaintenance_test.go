package day2_integration_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// buildClusterRC creates an OperationalRunnerConfig in ont-system with the given
// capabilities set on its status. The status subresource is written separately via
// Status().Update so the envtest API server accepts it.
func buildClusterRC(ctx context.Context, t *testing.T, clusterName string, capabilities ...string) *controller.OperationalRunnerConfig {
	t.Helper()
	rc := &controller.OperationalRunnerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: "ont-system",
		},
		Spec: controller.OperationalRunnerConfigSpec{
			ClusterRef: clusterName,
		},
	}
	if err := testClient.Create(ctx, rc); err != nil {
		t.Fatalf("create cluster RunnerConfig: %v", err)
	}
	caps := make([]controller.CapabilityEntry, len(capabilities))
	for i, c := range capabilities {
		caps[i] = controller.CapabilityEntry{Name: c, Version: "1.0.0"}
	}
	rc.Status.Capabilities = caps
	if err := testClient.Status().Update(ctx, rc); err != nil {
		t.Fatalf("update cluster RunnerConfig status: %v", err)
	}
	return rc
}

// TestEtcdMaintenance_Integration_BackupWithS3 verifies that the reconciler
// submits a Conductor executor Job when capability and S3 config are present.
// Uses a real API server — validates SSA status patch semantics.
func TestEtcdMaintenance_Integration_BackupWithS3(t *testing.T) {
	ns := "seam-tenant-test"
	crName := fmt.Sprintf("em-backup-%d", time.Now().UnixNano())
	ctx := context.Background()

	// Cluster RunnerConfig with etcd-backup capability. CR-INV-005.
	rc := buildClusterRC(ctx, t, "test-cluster", "etcd-backup")
	t.Cleanup(func() { _ = testClient.Delete(ctx, rc) })

	defaultS3Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "seam-etcd-backup-config", Namespace: "seam-system"},
	}
	if err := testClient.Create(ctx, defaultS3Secret); err != nil {
		_ = err // may already exist from prior run
	}

	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: ns, Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
	}
	if err := testClient.Create(ctx, em); err != nil {
		t.Fatalf("create EtcdMaintenance: %v", err)
	}
	t.Cleanup(func() {
		_ = testClient.Delete(ctx, em)
		_ = testClient.Delete(ctx, defaultS3Secret)
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
		t.Error("expected requeue after Job submission")
	}

	// Verify Job created with correct capability label.
	jobName := crName + "-etcd-backup"
	job := &batchv1.Job{}
	if err := testClient.Get(ctx, types.NamespacedName{Name: jobName, Namespace: ns}, job); err != nil {
		t.Fatalf("get Job %s: %v", jobName, err)
	}
	if job.Labels["platform.ontai.dev/capability"] != "etcd-backup" {
		t.Errorf("Job capability label = %q, want etcd-backup",
			job.Labels["platform.ontai.dev/capability"])
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
		t.Error("Running=True condition absent after Job submission")
	}
}

// TestEtcdMaintenance_Integration_S3AbsentSetsCondition verifies that when no S3
// Secret is present (and capability gate passes), EtcdBackupDestinationAbsent
// condition is set via real SSA status patch and no Job is created.
func TestEtcdMaintenance_Integration_S3AbsentSetsCondition(t *testing.T) {
	ns := "seam-tenant-test"
	crName := fmt.Sprintf("em-s3absent-%d", time.Now().UnixNano())
	ctx := context.Background()

	// Cluster RunnerConfig with etcd-backup capability so the S3 check is reached.
	rc := buildClusterRC(ctx, t, "test-cluster-s3absent", "etcd-backup")
	t.Cleanup(func() { _ = testClient.Delete(ctx, rc) })

	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: ns, Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster-s3absent"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
	}
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

	// No Job should be created when S3 is absent.
	jobList := &batchv1.JobList{}
	if err := testClient.List(ctx, jobList, client.InNamespace(ns)); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	for _, j := range jobList.Items {
		if j.Labels["platform.ontai.dev/cluster"] == "test-cluster-s3absent" {
			t.Errorf("unexpected Job %q created when S3 absent", j.Name)
		}
	}
}
