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
	clientevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1alpha1 "github.com/ontai-dev/platform/api/infrastructure/v1alpha1"
	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
	seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"
)

// fakeRecorder returns a buffered fake event recorder for use in tests.
func fakeRecorder() clientevents.EventRecorder {
	return clientevents.NewFakeRecorder(32)
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
	if err := infrav1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add infrav1alpha1 scheme: %v", err)
	}
	if err := seamcorev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add seamcorev1alpha1 scheme: %v", err)
	}
	return s
}

// clusterRC builds a cluster RunnerConfig in ont-system with the given capabilities.
// Day-2 reconcilers gate on this before submitting any Job. conductor-schema.md §5 CR-INV-005.
func clusterRC(clusterName string, capabilities ...string) *controller.OperationalRunnerConfig {
	caps := make([]controller.CapabilityEntry, len(capabilities))
	for i, c := range capabilities {
		caps[i] = controller.CapabilityEntry{Name: c, Version: "1.0.0"}
	}
	rc := &controller.OperationalRunnerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: "ont-system"},
	}
	rc.Spec.RunnerImage = "10.20.0.1:5000/ontai-dev/conductor:v1.9.3-dev"
	rc.Status.Capabilities = caps
	return rc
}

// successResultTCOR builds an InfrastructureTalosClusterOperationResult CR indicating success.
// CR name equals jobName (no suffix). The conductor executor creates it with OPERATION_RESULT_CR=jobName.
func successResultTCOR(jobName, namespace string) *seamcorev1alpha1.InfrastructureTalosClusterOperationResult {
	return &seamcorev1alpha1.InfrastructureTalosClusterOperationResult{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: namespace},
		Spec: seamcorev1alpha1.InfrastructureTalosClusterOperationResultSpec{
			Capability: "test-capability",
			ClusterRef: "test-cluster",
			JobRef:     jobName,
			Status:     seamcorev1alpha1.TalosClusterResultSucceeded,
			Message:    "operation completed",
		},
	}
}

// failedResultTCOR builds an InfrastructureTalosClusterOperationResult CR indicating failure.
func failedResultTCOR(jobName, namespace, message string) *seamcorev1alpha1.InfrastructureTalosClusterOperationResult {
	return &seamcorev1alpha1.InfrastructureTalosClusterOperationResult{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: namespace},
		Spec: seamcorev1alpha1.InfrastructureTalosClusterOperationResultSpec{
			Capability: "test-capability",
			ClusterRef: "test-cluster",
			JobRef:     jobName,
			Status:     seamcorev1alpha1.TalosClusterResultFailed,
			Message:    message,
		},
	}
}

// preExistingJob builds a pre-existing batch/v1 Job for use in completion tests.
func preExistingJob(jobName, namespace string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: namespace},
	}
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

// TestEtcdMaintenanceReconcile_SubmitsJob verifies that a Conductor executor Job
// is submitted on the first reconcile for a backup operation when the cluster
// RunnerConfig publishes the etcd-backup capability and the default S3 Secret exists.
func TestEtcdMaintenanceReconcile_SubmitsJob(t *testing.T) {
	scheme := buildDay2Scheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-1", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
	}
	defaultS3Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "seam-etcd-backup-config", Namespace: "seam-system"},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, defaultS3Secret, clusterRC("test-cluster", "etcd-backup")).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-1", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after Job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 Job, got %d", len(jobList.Items))
	}
	if len(jobList.Items) > 0 {
		job := jobList.Items[0]
		wantName := "backup-1-etcd-backup"
		if job.Name != wantName {
			t.Errorf("Job name = %q, want %q", job.Name, wantName)
		}
		if job.Labels["platform.ontai.dev/capability"] != "etcd-backup" {
			t.Errorf("Job capability label = %q, want etcd-backup", job.Labels["platform.ontai.dev/capability"])
		}
	}
}

// TestEtcdMaintenanceReconcile_JobComplete verifies that when the OperationResult
// ConfigMap reports success, EtcdMaintenance transitions to Ready=True.
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
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			em,
			clusterRC("test-cluster", "etcd-backup"),
			preExistingJob(jobName, "seam-tenant-test"),
			successResultTCOR(jobName, "seam-tenant-test"),
		).
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
			Name:       "reset-dev",
			Namespace:  "seam-tenant-dev",
			Generation: 1,
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
	if result.RequeueAfter != 0 {
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

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 0 {
		t.Errorf("no Job should be submitted without approval, got %d", len(jobList.Items))
	}
}

// TestClusterResetReconcile_ApprovedDirectPath verifies that with the approval
// annotation set and capi.enabled=false (no TalosCluster found), the reconciler
// submits a cluster-reset Conductor executor Job directly.
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
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(crst, clusterRC("ccs-mgmt", "cluster-reset")).
		WithStatusSubresource(crst).
		Build()
	r := &controller.ClusterResetReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "reset-mgmt", Namespace: "ont-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after Job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 cluster-reset Job, got %d", len(jobList.Items))
	}
	if len(jobList.Items) > 0 {
		job := jobList.Items[0]
		if job.Labels["platform.ontai.dev/capability"] != "cluster-reset" {
			t.Errorf("Job capability label = %q, want cluster-reset", job.Labels["platform.ontai.dev/capability"])
		}
	}
}

// TestClusterResetReconcile_JobComplete verifies that when the OperationResult
// ConfigMap reports success, ClusterReset transitions to Ready=True/ResetComplete.
func TestClusterResetReconcile_JobComplete(t *testing.T) {
	scheme := buildDay2Scheme(t)
	jobName := "reset-done-cluster-reset"
	crst := &platformv1alpha1.ClusterReset{
		ObjectMeta: metav1.ObjectMeta{
			Name: "reset-done", Namespace: "ont-system", Generation: 1,
			Annotations: map[string]string{platformv1alpha1.ResetApprovalAnnotation: "true"},
		},
		Spec: platformv1alpha1.ClusterResetSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
		},
		Status: platformv1alpha1.ClusterResetStatus{
			JobName: jobName,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			crst,
			clusterRC("ccs-mgmt", "cluster-reset"),
			preExistingJob(jobName, "ont-system"),
			successResultTCOR(jobName, "ont-system"),
		).
		WithStatusSubresource(crst).
		Build()
	r := &controller.ClusterResetReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "reset-done", Namespace: "ont-system"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.ClusterReset{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "reset-done", Namespace: "ont-system",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeResetReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Ready=True after Job completion")
	}
}

// TestClusterResetReconcile_JobFailed verifies that when the OperationResult
// ConfigMap reports failure, ClusterReset transitions to Degraded=True.
func TestClusterResetReconcile_JobFailed(t *testing.T) {
	scheme := buildDay2Scheme(t)
	jobName := "reset-fail-cluster-reset"
	crst := &platformv1alpha1.ClusterReset{
		ObjectMeta: metav1.ObjectMeta{
			Name: "reset-fail", Namespace: "ont-system", Generation: 1,
			Annotations: map[string]string{platformv1alpha1.ResetApprovalAnnotation: "true"},
		},
		Spec: platformv1alpha1.ClusterResetSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
		},
		Status: platformv1alpha1.ClusterResetStatus{
			JobName: jobName,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			crst,
			clusterRC("ccs-mgmt", "cluster-reset"),
			preExistingJob(jobName, "ont-system"),
			failedResultTCOR(jobName, "ont-system", "reset failed"),
		).
		WithStatusSubresource(crst).
		Build()
	r := &controller.ClusterResetReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "reset-fail", Namespace: "ont-system"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.ClusterReset{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "reset-fail", Namespace: "ont-system",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeResetDegraded)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Degraded=True after Job failure")
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
	if result.RequeueAfter != 0 {
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

// TestHardeningProfileReconcile_ValidConditionSet verifies that a profile with
// valid machineConfigPatches and sysctlParams gets Valid=True.
func TestHardeningProfileReconcile_ValidConditionSet(t *testing.T) {
	scheme := buildDay2Scheme(t)
	hp := &platformv1alpha1.HardeningProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "cis-l2", Namespace: "seam-tenant-dev", Generation: 1},
		Spec: platformv1alpha1.HardeningProfileSpec{
			Description:          "CIS Level 2",
			MachineConfigPatches: []string{`{"op":"replace","path":"/machine/sysctls/net.ipv4.conf.all.accept_redirects","value":"0"}`},
			SysctlParams:         map[string]string{"net.ipv4.ip_forward": "1"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hp).WithStatusSubresource(hp).Build()
	r := &controller.HardeningProfileReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cis-l2", Namespace: "seam-tenant-dev"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.HardeningProfile{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "cis-l2", Namespace: "seam-tenant-dev",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeHardeningProfileValid)
	if cond == nil {
		t.Fatal("Valid condition not set")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("Valid status = %s, want True for valid spec", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonHardeningProfileValid {
		t.Errorf("Valid reason = %s, want %s", cond.Reason, platformv1alpha1.ReasonHardeningProfileValid)
	}
}

// TestHardeningProfileReconcile_InvalidEmptyPatch verifies that an empty patch
// string in machineConfigPatches causes Valid=False/ProfileInvalid.
func TestHardeningProfileReconcile_InvalidEmptyPatch(t *testing.T) {
	scheme := buildDay2Scheme(t)
	hp := &platformv1alpha1.HardeningProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-1", Namespace: "seam-tenant-dev", Generation: 1},
		Spec: platformv1alpha1.HardeningProfileSpec{
			MachineConfigPatches: []string{""}, // empty patch — invalid
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hp).WithStatusSubresource(hp).Build()
	r := &controller.HardeningProfileReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "invalid-1", Namespace: "seam-tenant-dev"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.HardeningProfile{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "invalid-1", Namespace: "seam-tenant-dev",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeHardeningProfileValid)
	if cond == nil {
		t.Fatal("Valid condition not set")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Valid status = %s, want False for empty patch", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonHardeningProfileInvalid {
		t.Errorf("Valid reason = %s, want %s", cond.Reason, platformv1alpha1.ReasonHardeningProfileInvalid)
	}
}

// TestHardeningProfileReconcile_EmptySpecIsValid verifies that a profile with
// no patches or sysctlParams is valid (zero-field config is allowed).
func TestHardeningProfileReconcile_EmptySpecIsValid(t *testing.T) {
	scheme := buildDay2Scheme(t)
	hp := &platformv1alpha1.HardeningProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "empty-profile", Namespace: "seam-tenant-dev", Generation: 1},
		Spec: platformv1alpha1.HardeningProfileSpec{
			Description: "empty but valid",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hp).WithStatusSubresource(hp).Build()
	r := &controller.HardeningProfileReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "empty-profile", Namespace: "seam-tenant-dev"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.HardeningProfile{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "empty-profile", Namespace: "seam-tenant-dev",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeHardeningProfileValid)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Valid=True for empty spec (zero-field profile is valid)")
	}
}

// --- PKIRotation tests ---

// TestPKIRotationReconcile_SubmitsJob verifies a pki-rotate Conductor executor Job
// is submitted when the cluster RunnerConfig publishes the pki-rotate capability.
func TestPKIRotationReconcile_SubmitsJob(t *testing.T) {
	scheme := buildDay2Scheme(t)
	pkir := &platformv1alpha1.PKIRotation{
		ObjectMeta: metav1.ObjectMeta{Name: "pkir-1", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.PKIRotationSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pkir, clusterRC("test-cluster", "pki-rotate")).
		WithStatusSubresource(pkir).
		Build()
	r := &controller.PKIRotationReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "pkir-1", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after Job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 Job, got %d", len(jobList.Items))
	}
	if len(jobList.Items) > 0 {
		job := jobList.Items[0]
		wantName := "pkir-1-pki-rotate"
		if job.Name != wantName {
			t.Errorf("Job name = %q, want %q", job.Name, wantName)
		}
		if job.Labels["platform.ontai.dev/capability"] != "pki-rotate" {
			t.Errorf("Job capability label = %q, want pki-rotate", job.Labels["platform.ontai.dev/capability"])
		}
	}
}

// TestPKIRotationReconcile_InProgress verifies that when a Job exists but no
// OperationResult ConfigMap is present, the reconciler requeues.
func TestPKIRotationReconcile_InProgress(t *testing.T) {
	scheme := buildDay2Scheme(t)
	jobName := "pkir-2-pki-rotate"
	pkir := &platformv1alpha1.PKIRotation{
		ObjectMeta: metav1.ObjectMeta{Name: "pkir-2", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.PKIRotationSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
		},
		Status: platformv1alpha1.PKIRotationStatus{
			JobName: jobName,
			Conditions: []metav1.Condition{
				{
					Type:               platformv1alpha1.ConditionTypePKIRotationReady,
					Status:             metav1.ConditionFalse,
					Reason:             platformv1alpha1.ReasonPKIJobSubmitted,
					Message:            "submitted",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			pkir,
			clusterRC("test-cluster", "pki-rotate"),
			preExistingJob(jobName, "seam-tenant-test"),
			// No result CM — job still in progress.
		).
		WithStatusSubresource(pkir).
		Build()
	r := &controller.PKIRotationReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "pkir-2", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue while PKI rotation is in progress")
	}

	got := &platformv1alpha1.PKIRotation{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "pkir-2", Namespace: "seam-tenant-test",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypePKIRotationReady)
	if cond == nil {
		t.Fatal("Ready condition not set")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Ready status = %s, want False while in progress", cond.Status)
	}
}

// TestPKIRotationReconcile_Complete verifies that when the OperationResult
// ConfigMap reports success, PKIRotation transitions to Ready=True/JobComplete.
func TestPKIRotationReconcile_Complete(t *testing.T) {
	scheme := buildDay2Scheme(t)
	jobName := "pkir-3-pki-rotate"
	pkir := &platformv1alpha1.PKIRotation{
		ObjectMeta: metav1.ObjectMeta{Name: "pkir-3", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.PKIRotationSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
		},
		Status: platformv1alpha1.PKIRotationStatus{
			JobName: jobName,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			pkir,
			clusterRC("test-cluster", "pki-rotate"),
			preExistingJob(jobName, "seam-tenant-test"),
			successResultTCOR(jobName, "seam-tenant-test"),
		).
		WithStatusSubresource(pkir).
		Build()
	r := &controller.PKIRotationReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "pkir-3", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue after completion, got %v", result.RequeueAfter)
	}

	got := &platformv1alpha1.PKIRotation{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "pkir-3", Namespace: "seam-tenant-test",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypePKIRotationReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Ready=True after Job completion")
	}
	if cond.Reason != platformv1alpha1.ReasonPKIJobComplete {
		t.Errorf("Ready reason = %s, want %s", cond.Reason, platformv1alpha1.ReasonPKIJobComplete)
	}
}

// TestPKIRotationReconcile_Failed verifies that when the OperationResult
// ConfigMap reports failure, PKIRotation transitions to Degraded=True/JobFailed.
func TestPKIRotationReconcile_Failed(t *testing.T) {
	scheme := buildDay2Scheme(t)
	jobName := "pkir-4-pki-rotate"
	pkir := &platformv1alpha1.PKIRotation{
		ObjectMeta: metav1.ObjectMeta{Name: "pkir-4", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.PKIRotationSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
		},
		Status: platformv1alpha1.PKIRotationStatus{
			JobName: jobName,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			pkir,
			clusterRC("test-cluster", "pki-rotate"),
			preExistingJob(jobName, "seam-tenant-test"),
			failedResultTCOR(jobName, "seam-tenant-test", "pki rotation error"),
		).
		WithStatusSubresource(pkir).
		Build()
	r := &controller.PKIRotationReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "pkir-4", Namespace: "seam-tenant-test"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.PKIRotation{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "pkir-4", Namespace: "seam-tenant-test",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypePKIRotationDegraded)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Degraded=True after Job failure")
	}
	if cond.Reason != platformv1alpha1.ReasonPKIJobFailed {
		t.Errorf("Degraded reason = %s, want %s", cond.Reason, platformv1alpha1.ReasonPKIJobFailed)
	}
}

// --- NodeMaintenance tests ---

// TestNodeMaintenanceReconcile_SubmitsSingleJob verifies that a NodeMaintenance
// with operation=patch submits a single Conductor executor Job for node-patch.
func TestNodeMaintenanceReconcile_SubmitsSingleJob(t *testing.T) {
	scheme := buildDay2Scheme(t)
	nm := &platformv1alpha1.NodeMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "patch-1", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.NodeMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.NodeMaintenanceOperationPatch,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nm, clusterRC("test-cluster", "node-patch")).
		WithStatusSubresource(nm).
		Build()
	r := &controller.NodeMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "patch-1", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after Job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("expected 1 Job, got %d", len(jobList.Items))
	}
	job := jobList.Items[0]
	wantName := "patch-1-node-patch"
	if job.Name != wantName {
		t.Errorf("Job name = %q, want %q", job.Name, wantName)
	}
	if job.Labels["platform.ontai.dev/capability"] != "node-patch" {
		t.Errorf("Job capability label = %q, want node-patch", job.Labels["platform.ontai.dev/capability"])
	}
}

// TestNodeMaintenanceReconcile_HardeningApplyStep verifies that operation=hardening-apply
// submits a Job with the hardening-apply capability.
func TestNodeMaintenanceReconcile_HardeningApplyStep(t *testing.T) {
	scheme := buildDay2Scheme(t)
	nm := &platformv1alpha1.NodeMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "harden-1", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.NodeMaintenanceSpec{
			ClusterRef:  platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:   platformv1alpha1.NodeMaintenanceOperationHardeningApply,
			TargetNodes: []string{"worker-1"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nm, clusterRC("test-cluster", "hardening-apply")).
		WithStatusSubresource(nm).
		Build()
	r := &controller.NodeMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "harden-1", Namespace: "seam-tenant-test"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("expected 1 Job, got %d", len(jobList.Items))
	}
	job := jobList.Items[0]
	if job.Labels["platform.ontai.dev/capability"] != "hardening-apply" {
		t.Errorf("Job capability label = %q, want hardening-apply", job.Labels["platform.ontai.dev/capability"])
	}
	if job.Name != "harden-1-hardening-apply" {
		t.Errorf("Job name = %q, want harden-1-hardening-apply", job.Name)
	}
}

// TestNodeMaintenanceReconcile_CredentialRotateStep verifies that
// operation=credential-rotate submits a Job with the credential-rotate capability.
func TestNodeMaintenanceReconcile_CredentialRotateStep(t *testing.T) {
	scheme := buildDay2Scheme(t)
	nm := &platformv1alpha1.NodeMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "cred-rotate-1", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.NodeMaintenanceSpec{
			ClusterRef:  platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:   platformv1alpha1.NodeMaintenanceOperationCredentialRotate,
			TargetNodes: []string{"control-1"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nm, clusterRC("test-cluster", "credential-rotate")).
		WithStatusSubresource(nm).
		Build()
	r := &controller.NodeMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cred-rotate-1", Namespace: "seam-tenant-test"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("expected 1 Job, got %d", len(jobList.Items))
	}
	if jobList.Items[0].Labels["platform.ontai.dev/capability"] != "credential-rotate" {
		t.Errorf("Job capability label = %q, want credential-rotate",
			jobList.Items[0].Labels["platform.ontai.dev/capability"])
	}
}

// TestNodeMaintenanceReconcile_IdempotentAfterReady verifies that a second
// reconcile on a NodeMaintenance with Ready=True is a no-op.
func TestNodeMaintenanceReconcile_IdempotentAfterReady(t *testing.T) {
	scheme := buildDay2Scheme(t)
	nm := &platformv1alpha1.NodeMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "nm-done", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.NodeMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.NodeMaintenanceOperationPatch,
		},
		Status: platformv1alpha1.NodeMaintenanceStatus{
			Conditions: []metav1.Condition{
				{
					Type:               platformv1alpha1.ConditionTypeNodeMaintenanceReady,
					Status:             metav1.ConditionTrue,
					Reason:             platformv1alpha1.ReasonNodeJobComplete,
					Message:            "complete",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nm).WithStatusSubresource(nm).Build()
	r := &controller.NodeMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nm-done", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error on idempotent reconcile: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue on Ready=True NodeMaintenance, got %v", result.RequeueAfter)
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 0 {
		t.Errorf("expected no Job on idempotent reconcile, got %d", len(jobList.Items))
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

// TestClusterMaintenanceReconcile_BlockOutsideWindowsNoWindow verifies that when
// blockOutsideWindows=true and no maintenance window is active, the reconciler
// sets Paused=True/ConductorJobGateBlocked on the non-CAPI path.
func TestClusterMaintenanceReconcile_BlockOutsideWindowsNoWindow(t *testing.T) {
	scheme := buildDay2Scheme(t)
	cm := &platformv1alpha1.ClusterMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "maint-gated", Namespace: "seam-tenant-dev", Generation: 1},
		Spec: platformv1alpha1.ClusterMaintenanceSpec{
			ClusterRef:          platformv1alpha1.LocalObjectRef{Name: "ccs-dev"},
			BlockOutsideWindows: true,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).WithStatusSubresource(cm).Build()
	r := &controller.ClusterMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "maint-gated", Namespace: "seam-tenant-dev"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("ClusterMaintenance should requeue for periodic window re-evaluation")
	}

	got := &platformv1alpha1.ClusterMaintenance{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "maint-gated", Namespace: "seam-tenant-dev",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeClusterMaintenancePaused)
	if cond == nil {
		t.Fatal("Paused condition not set")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("Paused status = %s, want True when outside window and blockOutsideWindows=true", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonConductorJobGateBlocked {
		t.Errorf("Paused reason = %s, want %s", cond.Reason, platformv1alpha1.ReasonConductorJobGateBlocked)
	}
}

// --- UpgradePolicy tests ---

// TestUpgradePolicyReconcile_DirectPath verifies that for a non-CAPI cluster,
// a talos-upgrade Conductor executor Job is submitted directly.
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
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(up, clusterRC("ccs-mgmt", "talos-upgrade")).
		WithStatusSubresource(up).
		Build()
	r := &controller.UpgradePolicyReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "upgrade-1", Namespace: "ont-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after Job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 talos-upgrade Job, got %d", len(jobList.Items))
	}
	if len(jobList.Items) > 0 {
		if jobList.Items[0].Labels["platform.ontai.dev/capability"] != "talos-upgrade" {
			t.Errorf("Job capability label = %q, want talos-upgrade",
				jobList.Items[0].Labels["platform.ontai.dev/capability"])
		}
	}
}

// TestUpgradePolicyReconcile_StackUpgradeSingleJob verifies that a stack upgrade
// type submits a single Conductor executor Job with the stack-upgrade capability.
// conductor-schema.md §5: stack-upgrade is a compound capability handled internally.
func TestUpgradePolicyReconcile_StackUpgradeSingleJob(t *testing.T) {
	scheme := buildDay2Scheme(t)
	up := &platformv1alpha1.UpgradePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "stack-1", Namespace: "ont-system", Generation: 1},
		Spec: platformv1alpha1.UpgradePolicySpec{
			ClusterRef:              platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			UpgradeType:             platformv1alpha1.UpgradeTypeStack,
			TargetTalosVersion:      "v1.9.0",
			TargetKubernetesVersion: "v1.31.0",
			RollingStrategy:         platformv1alpha1.RollingStrategySequential,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(up, clusterRC("ccs-mgmt", "stack-upgrade")).
		WithStatusSubresource(up).
		Build()
	r := &controller.UpgradePolicyReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "stack-1", Namespace: "ont-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after Job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("expected 1 stack-upgrade Job, got %d", len(jobList.Items))
	}
	job := jobList.Items[0]
	if job.Labels["platform.ontai.dev/capability"] != "stack-upgrade" {
		t.Errorf("Job capability label = %q, want stack-upgrade", job.Labels["platform.ontai.dev/capability"])
	}
	if job.Name != "stack-1-stack-upgrade" {
		t.Errorf("Job name = %q, want stack-1-stack-upgrade", job.Name)
	}
}

// TestUpgradePolicyReconcile_KubeUpgradeJob verifies that a kube-upgrade type
// UpgradePolicy on a non-CAPI cluster submits a single kube-upgrade Job.
func TestUpgradePolicyReconcile_KubeUpgradeJob(t *testing.T) {
	scheme := buildDay2Scheme(t)
	up := &platformv1alpha1.UpgradePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-up-1", Namespace: "ont-system", Generation: 1},
		Spec: platformv1alpha1.UpgradePolicySpec{
			ClusterRef:              platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			UpgradeType:             platformv1alpha1.UpgradeTypeKubernetes,
			TargetKubernetesVersion: "v1.31.0",
			RollingStrategy:         platformv1alpha1.RollingStrategySequential,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(up, clusterRC("ccs-mgmt", "kube-upgrade")).
		WithStatusSubresource(up).
		Build()
	r := &controller.UpgradePolicyReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "kube-up-1", Namespace: "ont-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after Job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 Job, got %d", len(jobList.Items))
	}
	if len(jobList.Items) > 0 && jobList.Items[0].Labels["platform.ontai.dev/capability"] != "kube-upgrade" {
		t.Errorf("Job capability label = %q, want kube-upgrade",
			jobList.Items[0].Labels["platform.ontai.dev/capability"])
	}
}

// TestUpgradePolicyReconcile_CAPIPath verifies that when the owning TalosCluster
// has capi.enabled=true, the reconciler sets CAPIDelegated=True instead of
// submitting a Job.
func TestUpgradePolicyReconcile_CAPIPath(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-target", Namespace: "ont-system"},
		Spec: platformv1alpha1.TalosClusterSpec{
			CAPI: &platformv1alpha1.CAPIConfig{Enabled: true},
		},
	}
	up := &platformv1alpha1.UpgradePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "capi-up-1", Namespace: "ont-system", Generation: 1},
		Spec: platformv1alpha1.UpgradePolicySpec{
			ClusterRef:      platformv1alpha1.LocalObjectRef{Name: "ccs-target"},
			UpgradeType:     platformv1alpha1.UpgradeTypeTalos,
			RollingStrategy: platformv1alpha1.RollingStrategySequential,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc, up).WithStatusSubresource(up).Build()
	r := &controller.UpgradePolicyReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "capi-up-1", Namespace: "ont-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("CAPI path should not requeue, got %v", result.RequeueAfter)
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 0 {
		t.Errorf("expected 0 Jobs on CAPI path, got %d", len(jobList.Items))
	}

	got := &platformv1alpha1.UpgradePolicy{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "capi-up-1", Namespace: "ont-system",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeUpgradePolicyCAPIDelegated)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected CAPIDelegated=True on CAPI upgrade path")
	}
}

// TestUpgradePolicyReconcile_Failed verifies that when the OperationResult
// ConfigMap reports failure, UpgradePolicy transitions to Degraded=True.
func TestUpgradePolicyReconcile_Failed(t *testing.T) {
	scheme := buildDay2Scheme(t)
	jobName := "upgrade-fail-1-talos-upgrade"
	up := &platformv1alpha1.UpgradePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "upgrade-fail-1", Namespace: "ont-system", Generation: 1},
		Spec: platformv1alpha1.UpgradePolicySpec{
			ClusterRef:         platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			UpgradeType:        platformv1alpha1.UpgradeTypeTalos,
			TargetTalosVersion: "v1.9.0",
			RollingStrategy:    platformv1alpha1.RollingStrategySequential,
		},
		Status: platformv1alpha1.UpgradePolicyStatus{
			JobName: jobName,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			up,
			clusterRC("ccs-mgmt", "talos-upgrade"),
			preExistingJob(jobName, "ont-system"),
			failedResultTCOR(jobName, "ont-system", "upgrade failed"),
		).
		WithStatusSubresource(up).
		Build()
	r := &controller.UpgradePolicyReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "upgrade-fail-1", Namespace: "ont-system"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.UpgradePolicy{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "upgrade-fail-1", Namespace: "ont-system"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeUpgradePolicyDegraded)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Degraded=True after Job failure")
	}
	if cond.Reason != platformv1alpha1.ReasonUpgradeJobFailed {
		t.Errorf("Degraded reason = %s, want %s", cond.Reason, platformv1alpha1.ReasonUpgradeJobFailed)
	}
}

// --- NodeOperation tests ---

// TestNodeOperationReconcile_DirectScaleUp verifies that for a non-CAPI cluster,
// a node-scale-up Conductor executor Job is submitted.
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
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nop, clusterRC("ccs-mgmt", "node-scale-up")).
		WithStatusSubresource(nop).
		Build()
	r := &controller.NodeOperationReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "scale-1", Namespace: "ont-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after Job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 node-scale-up Job, got %d", len(jobList.Items))
	}
	if len(jobList.Items) > 0 {
		if jobList.Items[0].Labels["platform.ontai.dev/capability"] != "node-scale-up" {
			t.Errorf("Job capability label = %q, want node-scale-up",
				jobList.Items[0].Labels["platform.ontai.dev/capability"])
		}
	}
}

// TestNodeOperationReconcile_RebootJob verifies that operation=reboot submits
// a single node-reboot Conductor executor Job.
func TestNodeOperationReconcile_RebootJob(t *testing.T) {
	scheme := buildDay2Scheme(t)
	nop := &platformv1alpha1.NodeOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "reboot-1", Namespace: "ont-system", Generation: 1},
		Spec: platformv1alpha1.NodeOperationSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			Operation:  platformv1alpha1.NodeOperationTypeReboot,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nop, clusterRC("ccs-mgmt", "node-reboot")).
		WithStatusSubresource(nop).
		Build()
	r := &controller.NodeOperationReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "reboot-1", Namespace: "ont-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after Job submission for reboot")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 node-reboot Job, got %d", len(jobList.Items))
	}
	if len(jobList.Items) > 0 && jobList.Items[0].Labels["platform.ontai.dev/capability"] != "node-reboot" {
		t.Errorf("Job capability label = %q, want node-reboot",
			jobList.Items[0].Labels["platform.ontai.dev/capability"])
	}
}

// TestNodeOperationReconcile_Failed verifies that when the OperationResult
// ConfigMap reports failure, NodeOperation transitions to Degraded=True.
func TestNodeOperationReconcile_Failed(t *testing.T) {
	scheme := buildDay2Scheme(t)
	jobName := "nop-fail-1-node-scale-up"
	nop := &platformv1alpha1.NodeOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "nop-fail-1", Namespace: "ont-system", Generation: 1},
		Spec: platformv1alpha1.NodeOperationSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			Operation:  platformv1alpha1.NodeOperationTypeScaleUp,
		},
		Status: platformv1alpha1.NodeOperationStatus{
			JobName: jobName,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			nop,
			clusterRC("ccs-mgmt", "node-scale-up"),
			preExistingJob(jobName, "ont-system"),
			failedResultTCOR(jobName, "ont-system", "scale-up failed"),
		).
		WithStatusSubresource(nop).
		Build()
	r := &controller.NodeOperationReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nop-fail-1", Namespace: "ont-system"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.NodeOperation{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "nop-fail-1", Namespace: "ont-system"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeNodeOperationDegraded)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Degraded=True after Job failure")
	}
}

// --- EtcdMaintenance S3, node exclusion, and idempotency tests ---

// TestEtcdMaintenanceReconcile_S3AbsentBlocksJob verifies that when no S3 backup
// destination is configured and PVC fallback is disabled, the reconciler sets
// EtcdBackupDestinationAbsent and skips Job creation. platform-schema.md §10.
func TestEtcdMaintenanceReconcile_S3AbsentBlocksJob(t *testing.T) {
	scheme := buildDay2Scheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-2", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, clusterRC("test-cluster", "etcd-backup")).
		WithStatusSubresource(em).
		Build()
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

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 0 {
		t.Errorf("expected no Job when S3 absent, got %d", len(jobList.Items))
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

// TestEtcdMaintenanceReconcile_S3SecretRefSubmitsJob verifies that when
// spec.etcdBackupS3SecretRef references an existing Secret, a Job is submitted.
// platform-schema.md §10.
func TestEtcdMaintenanceReconcile_S3SecretRefSubmitsJob(t *testing.T) {
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
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, s3Secret, clusterRC("test-cluster", "etcd-backup")).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-3", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after Job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 Job when explicit S3 SecretRef present, got %d", len(jobList.Items))
	}
}

// TestEtcdMaintenanceReconcile_DefragNoS3Check verifies that defrag operations
// do not require S3 config and proceed to Job submission regardless.
func TestEtcdMaintenanceReconcile_DefragNoS3Check(t *testing.T) {
	scheme := buildDay2Scheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "defrag-1", Namespace: "seam-tenant-test", Generation: 1},
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
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "defrag-1", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after Job submission for defrag")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 etcd-defrag Job, got %d", len(jobList.Items))
	}
	if len(jobList.Items) > 0 && jobList.Items[0].Labels["platform.ontai.dev/capability"] != "etcd-defrag" {
		t.Errorf("Job capability label = %q, want etcd-defrag",
			jobList.Items[0].Labels["platform.ontai.dev/capability"])
	}
}

// TestEtcdMaintenanceReconcile_NodeExclusionsApplied verifies that when the
// platform-leader Lease exists and its holder pod is found, the submitted Job
// has NodeAffinity NotIn constraints for both the target node and leader node.
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
		WithObjects(em, lease, leaderPod, clusterRC("test-cluster", "etcd-defrag")).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "defrag-3", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("expected 1 Job, got %d", len(jobList.Items))
	}

	job := jobList.Items[0]
	if job.Spec.Template.Spec.Affinity == nil {
		t.Fatal("expected NodeAffinity on Job with node exclusions")
	}
	terms := job.Spec.Template.Spec.Affinity.NodeAffinity.
		RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 || len(terms[0].MatchExpressions) != 1 {
		t.Fatal("unexpected NodeSelectorTerms shape")
	}
	excluded := terms[0].MatchExpressions[0].Values
	wantExclusions := map[string]bool{leaderNode: true, targetNode: true}
	for _, v := range excluded {
		delete(wantExclusions, v)
	}
	if len(wantExclusions) > 0 {
		t.Errorf("NodeAffinity exclusions missing values: %v (got %v)", wantExclusions, excluded)
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
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, clusterRC("test-cluster", "etcd-defrag")).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "defrag-2", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error when Lease absent: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after Job submission even without Lease")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 Job when Lease absent, got %d", len(jobList.Items))
	}
	if len(jobList.Items) > 0 {
		job := jobList.Items[0]
		if job.Spec.Template.Spec.Affinity != nil {
			t.Error("expected no NodeAffinity when no targets and no Lease")
		}
	}
}

// --- MaintenanceBundle tests (unchanged — already uses Job-based pattern) ---

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
	rc := clusterRC("test-cluster", "drain")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mb, rc).WithStatusSubresource(mb, rc).Build()
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

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("expected 1 Job, got %d", len(jobList.Items))
	}

	job := jobList.Items[0]
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
	existingJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-etcd-backup-etcd-backup",
			Namespace: "seam-system",
		},
	}
	resultTCOR := successResultTCOR("test-etcd-backup-etcd-backup", "seam-system")
	resultTCOR.Spec.Message = "backup completed"
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(mb, existingJob, resultTCOR, clusterRC("test-cluster", "etcd-backup")).WithStatusSubresource(mb).Build()
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
	resultTCOR := failedResultTCOR("test-etcd-backup-etcd-backup", "seam-system", "S3 unreachable")
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(mb, existingJob, resultTCOR, clusterRC("test-cluster", "etcd-backup")).WithStatusSubresource(mb).Build()
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

// TestEtcdMaintenanceReconcile_RestoreJob verifies that operation=restore submits
// a Job with the etcd-restore capability without requiring an S3 Secret lookup.
func TestEtcdMaintenanceReconcile_RestoreJob(t *testing.T) {
	scheme := buildDay2Scheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "restore-1", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationRestore,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, clusterRC("test-cluster", "etcd-restore")).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "restore-1", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after Job submission for restore")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 etcd-restore Job, got %d", len(jobList.Items))
	}
	if len(jobList.Items) > 0 && jobList.Items[0].Labels["platform.ontai.dev/capability"] != "etcd-restore" {
		t.Errorf("Job capability label = %q, want etcd-restore",
			jobList.Items[0].Labels["platform.ontai.dev/capability"])
	}
}

// TestEtcdMaintenanceReconcile_PVCFallbackEnabled verifies that when
// spec.pvcFallbackEnabled=true and no S3 destination is configured, the reconciler
// sets EtcdBackupLocalFallback and proceeds to create a Job.
// platform-schema.md §10.
func TestEtcdMaintenanceReconcile_PVCFallbackEnabled(t *testing.T) {
	scheme := buildDay2Scheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-pvc", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef:         platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:          platformv1alpha1.EtcdMaintenanceOperationBackup,
			PVCFallbackEnabled: true,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, clusterRC("test-cluster", "etcd-backup")).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-pvc", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after Job submission with PVC fallback")
	}

	got := &platformv1alpha1.EtcdMaintenance{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "backup-pvc", Namespace: "seam-tenant-test",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.EtcdBackupLocalFallback)
	if cond == nil {
		t.Fatal("EtcdBackupLocalFallback condition not set")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("EtcdBackupLocalFallback status = %s, want True", cond.Status)
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 Job with PVC fallback, got %d", len(jobList.Items))
	}
}

// TestEtcdMaintenanceReconcile_IdempotentAfterReady verifies that a second
// reconcile on an EtcdMaintenance with Ready=True is a no-op: no Job is
// created and no requeue is requested.
func TestEtcdMaintenanceReconcile_IdempotentAfterReady(t *testing.T) {
	scheme := buildDay2Scheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "done-1", Namespace: "seam-tenant-test", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationDefrag,
		},
		Status: platformv1alpha1.EtcdMaintenanceStatus{
			Conditions: []metav1.Condition{
				{
					Type:               platformv1alpha1.ConditionTypeEtcdMaintenanceReady,
					Status:             metav1.ConditionTrue,
					Reason:             platformv1alpha1.ReasonEtcdJobComplete,
					Message:            "complete",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(em).WithStatusSubresource(em).Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "done-1", Namespace: "seam-tenant-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error on idempotent reconcile: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue on Ready=True EtcdMaintenance, got RequeueAfter=%v", result.RequeueAfter)
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 0 {
		t.Errorf("expected no Job on idempotent reconcile, got %d", len(jobList.Items))
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

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobList.Items) != 0 {
		t.Errorf("expected no Jobs created on idempotent reconcile, got %d", len(jobList.Items))
	}
}

// TestJobSpec_ConductorEnvInterface verifies that Jobs created by day-2 reconcilers
// use the correct conductor binary interface:
//   - Args: ["execute"] only (no --capability / --cluster flags)
//   - CAPABILITY, CLUSTER_REF, OPERATION_RESULT_CM env vars are set
//   - POD_NAMESPACE sourced from downward API
//   - TALOSCONFIG_PATH set and volume mounted
//   - RunnerImage from RunnerConfig.Spec.RunnerImage
//
// This test guards against regressions to the conductor execute-mode ENV contract.
// conductor-schema.md §17, config.EnvCapability/EnvClusterRef/EnvOperationResultCR.
func TestJobSpec_ConductorEnvInterface(t *testing.T) {
	scheme := buildDay2Scheme(t)
	nop := &platformv1alpha1.NodeOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "reboot-1", Namespace: "ont-system", Generation: 1},
		Spec: platformv1alpha1.NodeOperationSpec{
			ClusterRef:  platformv1alpha1.LocalObjectRef{Name: "test-cluster"},
			Operation:   "reboot",
			TargetNodes: []string{"worker-1"},
		},
	}
	rc := clusterRC("test-cluster", "node-reboot")
	rc.Spec.RunnerImage = "10.20.0.1:5000/ontai-dev/conductor:v1.9.3-dev"

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nop, rc).WithStatusSubresource(nop, rc).Build()
	r := &controller.NodeOperationReconciler{Client: c, Scheme: scheme, Recorder: fakeRecorder()}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "reboot-1", Namespace: "ont-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("expected 1 Job, got %d", len(jobList.Items))
	}
	job := jobList.Items[0]
	containers := job.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		t.Fatal("no containers in job spec")
	}
	c0 := containers[0]

	// Args: exactly ["execute"]
	if len(c0.Args) != 1 || c0.Args[0] != "execute" {
		t.Errorf("container Args = %v, want [execute]", c0.Args)
	}

	// Image from RunnerConfig.Spec.RunnerImage
	if c0.Image != "10.20.0.1:5000/ontai-dev/conductor:v1.9.3-dev" {
		t.Errorf("container Image = %q, want RunnerConfig.Spec.RunnerImage", c0.Image)
	}

	envMap := make(map[string]string)
	for _, e := range c0.Env {
		if e.Value != "" {
			envMap[e.Name] = e.Value
		}
	}
	if envMap["CAPABILITY"] != "node-reboot" {
		t.Errorf("CAPABILITY = %q, want node-reboot", envMap["CAPABILITY"])
	}
	if envMap["CLUSTER_REF"] != "test-cluster" {
		t.Errorf("CLUSTER_REF = %q, want test-cluster", envMap["CLUSTER_REF"])
	}
	jobName := job.Name
	// OPERATION_RESULT_CR = jobName (no suffix); conductor creates TCOR CR with this name.
	if envMap["OPERATION_RESULT_CR"] != jobName {
		t.Errorf("OPERATION_RESULT_CR = %q, want %q", envMap["OPERATION_RESULT_CR"], jobName)
	}
	if envMap["TALOSCONFIG_PATH"] == "" {
		t.Error("TALOSCONFIG_PATH not set")
	}

	// POD_NAMESPACE from downward API
	var podNSFieldPath string
	for _, e := range c0.Env {
		if e.Name == "POD_NAMESPACE" && e.ValueFrom != nil && e.ValueFrom.FieldRef != nil {
			podNSFieldPath = e.ValueFrom.FieldRef.FieldPath
		}
	}
	if podNSFieldPath != "metadata.namespace" {
		t.Errorf("POD_NAMESPACE fieldPath = %q, want metadata.namespace", podNSFieldPath)
	}

	// talosconfig volume mounted
	var foundVol bool
	for _, vol := range job.Spec.Template.Spec.Volumes {
		if vol.Name == "talosconfig" && vol.Secret != nil {
			if vol.Secret.SecretName == "test-cluster-talosconfig" {
				foundVol = true
			}
		}
	}
	if !foundVol {
		t.Error("talosconfig volume not found or wrong Secret name")
	}
}
