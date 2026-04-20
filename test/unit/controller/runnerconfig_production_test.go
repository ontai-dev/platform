// Package controller_test — operational Job production unit tests.
//
// These tests cover paths not addressed in day2_reconcilers_test.go:
//
//  1. S3 resolution tier 2: spec.etcdBackupS3SecretRef absent, platform-wide
//     seam-etcd-backup-config Secret present → Job submitted. platform-schema.md §10.
//  2. EtcdMaintenance idempotency: second reconcile with a pending Job (no
//     OperationResult ConfigMap yet) must not create a duplicate Job.
//  3. EtcdMaintenance cluster label is propagated correctly to the submitted Job.
//
// All tests use the fake controller-runtime client. No live cluster required.
package controller_test

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
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
// reconciler submits a Conductor executor Job. platform-schema.md §10.
func TestEtcdMaintenanceReconcile_S3PlatformDefaultFallback(t *testing.T) {
	scheme := buildDay2Scheme(t)

	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-tier2", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
	}
	platformDefault := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "seam-etcd-backup-config", Namespace: "seam-system"},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, platformDefault, clusterRC("test-cluster", "etcd-backup")).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(16)}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-tier2", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected non-zero RequeueAfter after Job submission using platform default S3")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("expected 1 Job when platform default S3 present, got %d", len(jobList.Items))
	}
	if jobList.Items[0].Labels["platform.ontai.dev/capability"] != "etcd-backup" {
		t.Errorf("Job capability label = %q, want etcd-backup",
			jobList.Items[0].Labels["platform.ontai.dev/capability"])
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
// pending EtcdMaintenance (Job submitted but no OperationResult ConfigMap yet)
// does NOT create a duplicate Job.
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
		WithObjects(em, clusterRC("test-cluster", "etcd-defrag")).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(16)}
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "defrag-idem", Namespace: "seam-tenant-test"},
	}

	// First reconcile: creates the Job.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile error: %v", err)
	}

	jobListAfterFirst := &batchv1.JobList{}
	if err := c.List(context.Background(), jobListAfterFirst); err != nil {
		t.Fatalf("list after first reconcile: %v", err)
	}
	if len(jobListAfterFirst.Items) != 1 {
		t.Fatalf("expected 1 Job after first reconcile, got %d", len(jobListAfterFirst.Items))
	}
	firstUID := jobListAfterFirst.Items[0].UID

	// Second reconcile: Job already exists, no result CM → idempotent.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile error: %v", err)
	}

	jobListAfterSecond := &batchv1.JobList{}
	if err := c.List(context.Background(), jobListAfterSecond); err != nil {
		t.Fatalf("list after second reconcile: %v", err)
	}
	if len(jobListAfterSecond.Items) != 1 {
		t.Errorf("expected 1 Job after second reconcile (idempotent), got %d — duplicate detected",
			len(jobListAfterSecond.Items))
	}
	if len(jobListAfterSecond.Items) > 0 && jobListAfterSecond.Items[0].UID != firstUID {
		t.Errorf("Job UID changed between reconciles: first=%v second=%v",
			firstUID, jobListAfterSecond.Items[0].UID)
	}
}

// TestEtcdMaintenanceReconcile_JobClusterLabelPropagated verifies that the
// submitted Job carries the correct cluster label matching spec.clusterRef.name.
func TestEtcdMaintenanceReconcile_JobClusterLabelPropagated(t *testing.T) {
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
		WithObjects(em, clusterRC("ccs-dev", "etcd-defrag")).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(16)}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "defrag-clusterref", Namespace: "seam-tenant-test"},
	}); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("expected 1 Job, got %d", len(jobList.Items))
	}
	job := jobList.Items[0]
	if job.Labels["platform.ontai.dev/cluster"] != "ccs-dev" {
		t.Errorf("Job cluster label = %q, want ccs-dev", job.Labels["platform.ontai.dev/cluster"])
	}
}
