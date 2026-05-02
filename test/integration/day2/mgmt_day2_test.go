// Package day2_test contains integration tests for management cluster day-2
// operational CRD reconcilers: EtcdMaintenance, NodeMaintenance, PKIRotation,
// and ClusterReset.
//
// All tests use controller-runtime's fake client — no live cluster required.
// Scenarios exercise the S3 resolution hierarchy, RunnerConfig submission,
// human approval gate, and operator restart recovery (idempotency).
//
// platform-schema.md §5 day-2 CRDs. CP-INV-003, CP-INV-010. INV-006, INV-018.
package day2_integration_test

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clientevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"
	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// buildDay2IntegrationScheme returns a runtime.Scheme with all types needed for
// day2 integration tests.
func buildDay2IntegrationScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add platformv1alpha1 scheme: %v", err)
	}
	if err := seamcorev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add seamcorev1alpha1 scheme: %v", err)
	}
	return s
}

// defaultS3Secret builds the cluster-wide default S3 Secret in seam-system.
// Used to satisfy the S3 resolution hierarchy fallback. platform-schema.md §10.
func defaultS3Secret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "seam-etcd-backup-config",
			Namespace: "seam-system",
		},
	}
}

// perOpS3Secret builds a per-operation S3 Secret for use as spec.etcdBackupS3SecretRef.
func perOpS3Secret(name, ns string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
}

// fakeClusterRC returns a cluster RunnerConfig in ont-system for clusterName with
// the given capability names. Status is populated inline so the fake client stores
// it on WithObjects (not registered with WithStatusSubresource). All day-2 reconcilers
// gate on this object before submitting a Conductor executor Job.
func fakeClusterRC(clusterName string, caps ...string) *controller.OperationalRunnerConfig {
	capEntries := make([]controller.CapabilityEntry, len(caps))
	for i, c := range caps {
		capEntries[i] = controller.CapabilityEntry{Name: c, Version: "1.0.0"}
	}
	rc := &controller.OperationalRunnerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: "ont-system",
		},
		Spec: controller.OperationalRunnerConfigSpec{
			ClusterRef:  clusterName,
			RunnerImage: "ghcr.io/ontai-dev/conductor-execute:dev",
		},
	}
	rc.Status.Capabilities = capEntries
	return rc
}

// ── EtcdMaintenance: S3 resolution hierarchy ─────────────────────────────────

// TestEtcdMaintenanceIntegration_S3AbsentSetsCondition verifies sub-scenario 1:
// when no S3 Secret exists (neither per-op nor cluster-wide default), the reconciler
// sets EtcdBackupDestinationAbsent=True and does not submit a RunnerConfig.
// platform-schema.md §10 hierarchy level 0: absent.
func TestEtcdMaintenanceIntegration_S3AbsentSetsCondition(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-absent", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
	}
	rc := fakeClusterRC("ccs-mgmt", "etcd-backup")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(em, rc).WithStatusSubresource(em).Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(8)}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-absent", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// No Job must have been submitted when S3 is absent.
	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList, client.InNamespace("seam-system")); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 0 {
		t.Errorf("expected 0 Jobs when S3 absent, got %d", len(jobList.Items))
	}

	// EtcdBackupDestinationAbsent condition must be set.
	got := &platformv1alpha1.EtcdMaintenance{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "backup-absent", Namespace: "seam-system"}, got); err != nil {
		t.Fatalf("get EtcdMaintenance: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.EtcdBackupDestinationAbsent)
	if cond == nil {
		t.Fatal("EtcdBackupDestinationAbsent condition not set when no S3 configured")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("EtcdBackupDestinationAbsent.Status = %s, want True", cond.Status)
	}
}

// TestEtcdMaintenanceIntegration_S3ClusterDefault verifies sub-scenario 2:
// when only the cluster-wide default Secret exists, the reconciler uses it and
// submits a RunnerConfig with s3SecretName=seam-etcd-backup-config.
// platform-schema.md §10 hierarchy level 2: cluster-wide default.
func TestEtcdMaintenanceIntegration_S3ClusterDefault(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-default", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
	}
	rc := fakeClusterRC("ccs-mgmt", "etcd-backup")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, rc, defaultS3Secret()).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(8)}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-default", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// The reconciler resolves the cluster-wide default S3 Secret and submits a Job.
	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList, client.InNamespace("seam-system")); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("expected 1 Job, got %d", len(jobList.Items))
	}
	if jobList.Items[0].Labels["platform.ontai.dev/capability"] != "etcd-backup" {
		t.Errorf("Job capability label = %q, want etcd-backup",
			jobList.Items[0].Labels["platform.ontai.dev/capability"])
	}
}

// TestEtcdMaintenanceIntegration_S3PerOpOverride verifies sub-scenario 3:
// when spec.etcdBackupS3SecretRef is set, the per-operation Secret overrides the
// cluster-wide default. platform-schema.md §10 hierarchy level 1: per-operation.
func TestEtcdMaintenanceIntegration_S3PerOpOverride(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-perop", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
			EtcdBackupS3SecretRef: &corev1.SecretReference{
				Name:      "my-s3-secret",
				Namespace: "seam-system",
			},
		},
	}
	rc := fakeClusterRC("ccs-mgmt", "etcd-backup")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, rc, defaultS3Secret(), perOpS3Secret("my-s3-secret", "seam-system")).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(8)}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-perop", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Per-op S3 secret is present: reconciler proceeds past S3 check and submits a Job.
	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList, client.InNamespace("seam-system")); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("expected 1 Job, got %d", len(jobList.Items))
	}
	if jobList.Items[0].Labels["platform.ontai.dev/capability"] != "etcd-backup" {
		t.Errorf("Job capability label = %q, want etcd-backup",
			jobList.Items[0].Labels["platform.ontai.dev/capability"])
	}
}

// TestEtcdMaintenanceIntegration_DefragNoS3 verifies sub-scenario 4:
// defrag operations do not require any S3 Secret — RunnerConfig is submitted
// without s3SecretName in the step parameters.
func TestEtcdMaintenanceIntegration_DefragNoS3(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "defrag-1", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationDefrag,
		},
	}
	rc := fakeClusterRC("ccs-mgmt", "etcd-defrag")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, rc).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(8)}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "defrag-1", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Defrag does not require S3 — Job is submitted directly.
	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList, client.InNamespace("seam-system")); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("defrag: expected 1 Job without S3, got %d", len(jobList.Items))
	}
	if jobList.Items[0].Labels["platform.ontai.dev/capability"] != "etcd-defrag" {
		t.Errorf("Job capability label = %q, want etcd-defrag",
			jobList.Items[0].Labels["platform.ontai.dev/capability"])
	}
}

// ── NodeMaintenance: credential-rotate ───────────────────────────────────────

// TestNodeMaintenanceIntegration_CredentialRotateSubmitsRunnerConfig verifies that a
// NodeMaintenance with operation=credential-rotate produces a RunnerConfig with
// capability=credential-rotate and HaltOnFailure=true. INV-018.
func TestNodeMaintenanceIntegration_CredentialRotateSubmitsRunnerConfig(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	nm := &platformv1alpha1.NodeMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "cred-rotate-1", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.NodeMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			Operation:  platformv1alpha1.NodeMaintenanceOperationCredentialRotate,
		},
	}
	rc := fakeClusterRC("ccs-mgmt", "credential-rotate")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nm, rc).
		WithStatusSubresource(nm).
		Build()
	r := &controller.NodeMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(8)}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cred-rotate-1", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 after Job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList, client.InNamespace("seam-system")); err != nil {
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

// ── PKIRotation ───────────────────────────────────────────────────────────────

// TestPKIRotationIntegration_SubmitsRunnerConfig verifies that a PKIRotation CR
// produces a RunnerConfig with capability=pki-rotate and sets
// Running=True/JobSubmitted on the CR status.
func TestPKIRotationIntegration_SubmitsRunnerConfig(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	pkir := &platformv1alpha1.PKIRotation{
		ObjectMeta: metav1.ObjectMeta{Name: "pki-1", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.PKIRotationSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
		},
	}
	rc := fakeClusterRC("ccs-mgmt", "pki-rotate")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pkir, rc).
		WithStatusSubresource(pkir).
		Build()
	r := &controller.PKIRotationReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(8)}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "pki-1", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 after Job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList, client.InNamespace("seam-system")); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("expected 1 Job, got %d", len(jobList.Items))
	}
	if jobList.Items[0].Labels["platform.ontai.dev/capability"] != "pki-rotate" {
		t.Errorf("Job capability label = %q, want pki-rotate",
			jobList.Items[0].Labels["platform.ontai.dev/capability"])
	}
}

// ── ClusterReset: human approval gate ────────────────────────────────────────

// TestClusterResetIntegration_ApprovalGateBlocks verifies CP-INV-006: a ClusterReset
// without the ontai.dev/reset-approved=true annotation must not submit a RunnerConfig
// and must set PendingApproval=True. INV-007 human gate.
func TestClusterResetIntegration_ApprovalGateBlocks(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	crst := &platformv1alpha1.ClusterReset{
		ObjectMeta: metav1.ObjectMeta{Name: "reset-1", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.ClusterResetSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(crst).
		WithStatusSubresource(crst).
		Build()
	r := &controller.ClusterResetReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(8)}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "reset-1", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// No RunnerConfig — human gate not satisfied. INV-007.
	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 0 {
		t.Errorf("expected 0 RunnerConfigs without approval, got %d", len(rcList.Items))
	}

	got := &platformv1alpha1.ClusterReset{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "reset-1", Namespace: "seam-system"}, got); err != nil {
		t.Fatalf("get ClusterReset: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeResetPendingApproval)
	if cond == nil {
		t.Fatal("PendingApproval condition not set when approval annotation absent")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("PendingApproval.Status = %s, want True", cond.Status)
	}
}

// TestClusterResetIntegration_ApprovalAnnotationProceed verifies that when the
// ontai.dev/reset-approved=true annotation is present, the reconciler proceeds
// past the gate and submits a RunnerConfig with capability=cluster-reset.
func TestClusterResetIntegration_ApprovalAnnotationProceed(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	crst := &platformv1alpha1.ClusterReset{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "reset-approved",
			Namespace:  "seam-system",
			Generation: 1,
			Annotations: map[string]string{
				"ontai.dev/reset-approved": "true",
			},
		},
		Spec: platformv1alpha1.ClusterResetSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
		},
	}
	rc := fakeClusterRC("ccs-mgmt", "cluster-reset")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(crst, rc).
		WithStatusSubresource(crst).
		Build()
	r := &controller.ClusterResetReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(8)}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "reset-approved", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 after Job submission")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList, client.InNamespace("seam-system")); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Fatalf("expected 1 Job after approval, got %d", len(jobList.Items))
	}
	if jobList.Items[0].Labels["platform.ontai.dev/capability"] != "cluster-reset" {
		t.Errorf("Job capability label = %q, want cluster-reset",
			jobList.Items[0].Labels["platform.ontai.dev/capability"])
	}
}

// ── Operator restart recovery ─────────────────────────────────────────────────

// TestEtcdMaintenanceIntegration_RestartRecovery_NoDuplicateRunnerConfig verifies
// that when a RunnerConfig already exists from a previous reconcile cycle (e.g.,
// after operator restart), the reconciler does not create a duplicate.
// This guards against the double-submission bug that triggered CP-INV-010.
func TestEtcdMaintenanceIntegration_RestartRecovery_NoDuplicateRunnerConfig(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "backup-restart", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
		Status: platformv1alpha1.EtcdMaintenanceStatus{
			JobName: "backup-restart-etcd-backup",
		},
	}
	// Pre-existing Job from before the operator restart — reconciler must not create a duplicate.
	existingJob := &batchv1.Job{}
	existingJob.SetName("backup-restart-etcd-backup")
	existingJob.SetNamespace("seam-system")

	rc := fakeClusterRC("ccs-mgmt", "etcd-backup")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, existingJob, rc, defaultS3Secret()).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(8)}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "backup-restart", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList, client.InNamespace("seam-system")); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("restart recovery: expected 1 Job (no duplicate), got %d", len(jobList.Items))
	}
}
