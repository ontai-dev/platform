// Package controller_test — RunnerConfig production unit tests.
//
// Workstream 2: RunnerConfig production gaps.
//
// These tests cover paths not addressed in day2_reconcilers_test.go:
//
//  1. S3 resolution tier 2: spec.etcdBackupS3SecretRef absent, platform-wide
//     seam-etcd-backup-config Secret present → RunnerConfig submitted with default
//     S3 params. platform-schema.md §10.
//  2. EtcdMaintenance idempotency: second reconcile with a pending RunnerConfig (no
//     terminal status) must not create a duplicate RunnerConfig.
//     conductor-schema.md §17.
//  3. EtcdMaintenance RunnerConfig clusterRef is propagated correctly to the
//     RunnerConfig spec.clusterRef field.
//
// All tests use the fake controller-runtime client. No live cluster required.
package controller_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// TestEtcdMaintenanceReconcile_S3PlatformDefaultFallback verifies that when
// spec.etcdBackupS3SecretRef is not set (tier 1 absent) but the platform-wide
// default Secret "seam-etcd-backup-config" exists in seam-system (tier 2), the
// reconciler submits the RunnerConfig with the platform-wide default S3 params.
// platform-schema.md §10: three-tier S3 resolution hierarchy.
func TestEtcdMaintenanceReconcile_S3PlatformDefaultFallback(t *testing.T) {
	scheme := buildDay2Scheme(t)

	// EtcdMaintenance with no explicit S3 SecretRef — relies on platform default.
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-tier2", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
			// EtcdBackupS3SecretRef intentionally absent — tier 1 not configured.
		},
	}

	// Platform-wide default Secret in seam-system — tier 2.
	platformDefault := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "seam-etcd-backup-config",
			Namespace: "seam-system",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, platformDefault).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(16)}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-tier2", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// RunnerConfig submitted — reconciler must requeue.
	if result.RequeueAfter == 0 {
		t.Error("expected non-zero RequeueAfter after RunnerConfig submission using platform default S3")
	}

	// Exactly one RunnerConfig must be present.
	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Fatalf("expected 1 RunnerConfig when platform default S3 present, got %d", len(rcList.Items))
	}

	rc := rcList.Items[0]
	if len(rc.Spec.Steps) == 0 {
		t.Fatal("RunnerConfig has no steps")
	}
	step := rc.Spec.Steps[0]
	// Step capability must be etcd-backup.
	if step.Capability != "etcd-backup" {
		t.Errorf("step.Capability = %q, want etcd-backup", step.Capability)
	}
	// S3 params must reference the platform-wide default Secret.
	if step.Parameters["s3SecretName"] != "seam-etcd-backup-config" {
		t.Errorf("step.Parameters[s3SecretName] = %q, want seam-etcd-backup-config",
			step.Parameters["s3SecretName"])
	}
	if step.Parameters["s3SecretNamespace"] != "seam-system" {
		t.Errorf("step.Parameters[s3SecretNamespace] = %q, want seam-system",
			step.Parameters["s3SecretNamespace"])
	}

	// EtcdBackupDestinationAbsent must NOT be set — the platform default was found.
	got := &platformv1alpha1.EtcdMaintenance{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "backup-tier2", Namespace: "seam-tenant-test",
	}, got); err != nil {
		t.Fatalf("get EtcdMaintenance: %v", err)
	}
	absentCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.EtcdBackupDestinationAbsent)
	if absentCond != nil && absentCond.Status == metav1.ConditionTrue {
		t.Error("EtcdBackupDestinationAbsent must not be True when platform default S3 Secret exists")
	}
}

// TestEtcdMaintenanceReconcile_Idempotent verifies that a second reconcile on a
// pending EtcdMaintenance (RunnerConfig submitted but not yet terminal) does NOT
// create a duplicate RunnerConfig. conductor-schema.md §17.
func TestEtcdMaintenanceReconcile_Idempotent(t *testing.T) {
	scheme := buildDay2Scheme(t)

	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "defrag-idem", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationDefrag,
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(16)}
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "defrag-idem", Namespace: "seam-tenant-test"},
	}

	// First reconcile: creates the RunnerConfig.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile error: %v", err)
	}

	rcListAfterFirst := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcListAfterFirst); err != nil {
		t.Fatalf("list after first reconcile: %v", err)
	}
	if len(rcListAfterFirst.Items) != 1 {
		t.Fatalf("expected 1 RunnerConfig after first reconcile, got %d", len(rcListAfterFirst.Items))
	}
	firstUID := rcListAfterFirst.Items[0].UID

	// Second reconcile: RunnerConfig already exists, no terminal status → idempotent.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile error: %v", err)
	}

	rcListAfterSecond := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcListAfterSecond); err != nil {
		t.Fatalf("list after second reconcile: %v", err)
	}
	if len(rcListAfterSecond.Items) != 1 {
		t.Errorf("expected 1 RunnerConfig after second reconcile (idempotent), got %d — duplicate detected",
			len(rcListAfterSecond.Items))
	}
	// Same object — UID must be unchanged.
	if len(rcListAfterSecond.Items) > 0 && rcListAfterSecond.Items[0].UID != firstUID {
		t.Errorf("RunnerConfig UID changed between reconciles: first=%v second=%v — duplicate may have been created and first deleted",
			firstUID, rcListAfterSecond.Items[0].UID)
	}
}

// TestEtcdMaintenanceReconcile_RunnerConfigClusterRefPropagated verifies that the
// ClusterRef in the submitted RunnerConfig matches the EtcdMaintenance
// spec.clusterRef.name. conductor-schema.md §17.
func TestEtcdMaintenanceReconcile_RunnerConfigClusterRefPropagated(t *testing.T) {
	scheme := buildDay2Scheme(t)

	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "defrag-clusterref", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-dev"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationDefrag,
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(16)}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "defrag-clusterref", Namespace: "seam-tenant-test"},
	}); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Fatalf("expected 1 RunnerConfig, got %d", len(rcList.Items))
	}
	rc := rcList.Items[0]
	if rc.Spec.ClusterRef != "ccs-dev" {
		t.Errorf("RunnerConfig.Spec.ClusterRef = %q, want ccs-dev", rc.Spec.ClusterRef)
	}
	// The cluster label on the RunnerConfig must also match.
	if rc.Labels["platform.ontai.dev/cluster"] != "ccs-dev" {
		t.Errorf("RunnerConfig cluster label = %q, want ccs-dev",
			rc.Labels["platform.ontai.dev/cluster"])
	}
}
