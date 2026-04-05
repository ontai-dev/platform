// Package controller_test contains unit tests for the day-2 operational CRD
// reconcilers. These are pure unit tests — no envtest, no live Kubernetes API.
// All tests use controller-runtime's fake client.
package controller_test

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
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

// buildDay2Scheme returns a runtime.Scheme with platform and batch types registered.
func buildDay2Scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add platformv1alpha1 scheme: %v", err)
	}
	return s
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

// TestEtcdMaintenanceReconcile_SubmitsJob verifies that a Job is submitted on
// the first reconcile for a backup operation.
func TestEtcdMaintenanceReconcile_SubmitsJob(t *testing.T) {
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

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-1", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after job submission")
	}

	// Verify a Job was created.
	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 Job, got %d", len(jobList.Items))
	}
}

// TestEtcdMaintenanceReconcile_JobComplete verifies that when an OperationResult
// ConfigMap reports success, the EtcdMaintenance transitions to Ready.
func TestEtcdMaintenanceReconcile_JobComplete(t *testing.T) {
	scheme := buildDay2Scheme(t)
	jobName := "backup-1-etcd-backup"
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-1", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
		Status: platformv1alpha1.EtcdMaintenanceStatus{
			JobName: jobName,
			Conditions: []metav1.Condition{
				{
					Type:   platformv1alpha1.ConditionTypeEtcdMaintenanceRunning,
					Status: metav1.ConditionTrue,
					Reason: platformv1alpha1.ReasonEtcdJobSubmitted,
				},
			},
		},
	}
	// Pre-existing Job.
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "seam-tenant-test"},
	}
	// OperationResult ConfigMap indicating success.
	resultCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName + "-result",
			Namespace: "seam-tenant-test",
		},
		Data: map[string]string{
			"status":  "success",
			"message": "etcd backup complete",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, job, resultCM).
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
// annotation, the reconciler sets PendingApproval and does not submit a Job.
func TestClusterResetReconcile_ApprovalGate(t *testing.T) {
	scheme := buildDay2Scheme(t)
	crst := &platformv1alpha1.ClusterReset{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "reset-dev",
			Namespace: "seam-tenant-dev",
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

	// Verify no Job was submitted.
	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobList.Items) != 0 {
		t.Errorf("no jobs should be submitted without approval, got %d", len(jobList.Items))
	}
}

// TestClusterResetReconcile_ApprovedDirectPath verifies that with the approval
// annotation set and capi.enabled=false (no TalosCluster found), the reconciler
// submits the cluster-reset Job directly.
func TestClusterResetReconcile_ApprovedDirectPath(t *testing.T) {
	scheme := buildDay2Scheme(t)
	crst := &platformv1alpha1.ClusterReset{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "reset-mgmt",
			Namespace: "ont-system",
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
		t.Error("expected requeue after job submission")
	}

	// Verify the Job was submitted.
	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 cluster-reset Job, got %d", len(jobList.Items))
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

// TestPKIRotationReconcile_SubmitsJob verifies a pki-rotate Job is submitted.
func TestPKIRotationReconcile_SubmitsJob(t *testing.T) {
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
		t.Error("expected requeue after job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 Job, got %d", len(jobList.Items))
	}
	if jobList.Items[0].Name != "pkir-1-pki-rotate" {
		t.Errorf("job name = %s, want pkir-1-pki-rotate", jobList.Items[0].Name)
	}
}

// --- NodeMaintenance tests ---

// TestNodeMaintenanceReconcile_SubmitsNodePatchJob verifies a node-patch Job
// is submitted for a NodeMaintenance with operation=patch.
func TestNodeMaintenanceReconcile_SubmitsNodePatchJob(t *testing.T) {
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
		t.Error("expected requeue after job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 Job, got %d", len(jobList.Items))
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
// (TalosCluster not found), a talos-upgrade Job is submitted directly.
func TestUpgradePolicyReconcile_DirectPath(t *testing.T) {
	scheme := buildDay2Scheme(t)
	up := &platformv1alpha1.UpgradePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "upgrade-1", Namespace: "ont-system", Generation: 1},
		Spec: platformv1alpha1.UpgradePolicySpec{
			ClusterRef:          platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			UpgradeType:         platformv1alpha1.UpgradeTypeTalos,
			TargetTalosVersion:  "v1.9.0",
			RollingStrategy:     platformv1alpha1.RollingStrategySequential,
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
		t.Error("expected requeue after job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 talos-upgrade Job, got %d", len(jobList.Items))
	}
}

// --- NodeOperation tests ---

// TestNodeOperationReconcile_DirectScaleUp verifies that for a non-CAPI cluster,
// a node-scale-up Job is submitted.
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
		t.Error("expected requeue after job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 node-scale-up Job, got %d", len(jobList.Items))
	}
}
