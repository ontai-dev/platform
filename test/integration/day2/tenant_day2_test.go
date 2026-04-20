// Package day2_test contains integration tests for tenant cluster day-2
// operational CRD reconcilers. Tests run against both import-mode and
// CAPI-mode tenant TaloscCluster configurations.
//
// All tests use controller-runtime's fake client — no live cluster required.
// The import-mode and CAPI-mode tenant tests verify that EtcdMaintenance
// is namespace-agnostic (operates in seam-tenant-{cluster-name}) and that
// the S3 resolution hierarchy applies equally for management and tenant clusters.
//
// platform-schema.md §5. CP-INV-003, CP-INV-010. conductor-schema.md §17.
package day2_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// ── Tenant import-mode EtcdMaintenance ───────────────────────────────────────

// TestTenantEtcdMaintenance_ImportMode_BackupSubmitted verifies that an EtcdMaintenance
// in a tenant namespace (seam-tenant-ccs-dev) proceeds through the full S3 resolve and
// RunnerConfig submit path, identical to the management cluster path.
// capi.enabled=false tenant clusters use direct Conductor Jobs. platform-schema.md §5.
func TestTenantEtcdMaintenance_ImportMode_BackupSubmitted(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	ns := "seam-tenant-ccs-dev"
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-backup-1", Namespace: ns, Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-dev"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, defaultS3Secret()).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tenant-backup-1", Namespace: ns},
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
		t.Fatalf("expected 1 RunnerConfig in tenant namespace, got %d", len(rcList.Items))
	}
	rc := rcList.Items[0]
	if rc.Namespace != ns {
		t.Errorf("RunnerConfig.Namespace = %q, want %q", rc.Namespace, ns)
	}
	if rc.Spec.Steps[0].Capability != "etcd-backup" {
		t.Errorf("capability = %q, want etcd-backup", rc.Spec.Steps[0].Capability)
	}
	// S3 falls back to cluster-wide default (no per-op secret set).
	if rc.Spec.Steps[0].Parameters["s3SecretName"] != "seam-etcd-backup-config" {
		t.Errorf("s3SecretName = %q, want seam-etcd-backup-config", rc.Spec.Steps[0].Parameters["s3SecretName"])
	}

	// EtcdMaintenanceRunning condition must be set.
	got := &platformv1alpha1.EtcdMaintenance{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "tenant-backup-1", Namespace: ns}, got); err != nil {
		t.Fatalf("get EtcdMaintenance: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeEtcdMaintenanceRunning)
	if cond == nil {
		t.Fatal("EtcdMaintenanceRunning condition not set after RunnerConfig submission")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("EtcdMaintenanceRunning.Status = %s, want True", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonEtcdJobSubmitted {
		t.Errorf("EtcdMaintenanceRunning.Reason = %q, want %q", cond.Reason, platformv1alpha1.ReasonEtcdJobSubmitted)
	}
}

// TestTenantEtcdMaintenance_CAPIMode_BackupSubmitted verifies that EtcdMaintenance
// on a CAPI-mode tenant cluster (capi.enabled=true) uses the same direct RunnerConfig
// path. CAPI has no etcd concept — all etcd operations bypass CAPI entirely.
// platform-schema.md §5: EtcdMaintenance is always a direct Conductor Job.
func TestTenantEtcdMaintenance_CAPIMode_BackupSubmitted(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	ns := "seam-tenant-ccs-app"
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "capi-tenant-backup-1", Namespace: ns, Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-app"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em, defaultS3Secret()).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "capi-tenant-backup-1", Namespace: ns},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Fatalf("CAPI tenant: expected 1 RunnerConfig (direct Job, not CAPI path), got %d", len(rcList.Items))
	}
	if rcList.Items[0].Namespace != ns {
		t.Errorf("RunnerConfig.Namespace = %q, want %q", rcList.Items[0].Namespace, ns)
	}
}

// ── Tenant EtcdMaintenance: S3 absent on tenant ───────────────────────────────

// TestTenantEtcdMaintenance_S3AbsentInTenantNamespace verifies that when no S3 Secret
// exists for a tenant backup, EtcdBackupDestinationAbsent is set regardless of which
// namespace the EtcdMaintenance lives in. The cluster-wide default is always in
// seam-system; per-op overrides can be in any namespace.
func TestTenantEtcdMaintenance_S3AbsentInTenantNamespace(t *testing.T) {
	scheme := buildDay2IntegrationScheme(t)
	ns := "seam-tenant-ccs-dev"
	em := &platformv1alpha1.EtcdMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-backup-absent", Namespace: ns, Generation: 1},
		Spec: platformv1alpha1.EtcdMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-dev"},
			Operation:  platformv1alpha1.EtcdMaintenanceOperationBackup,
			// No EtcdBackupS3SecretRef and no cluster-wide default in seam-system.
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(em).
		WithStatusSubresource(em).
		Build()
	r := &controller.EtcdMaintenanceReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tenant-backup-absent", Namespace: ns},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 0 {
		t.Errorf("expected 0 RunnerConfigs when S3 absent, got %d", len(rcList.Items))
	}

	got := &platformv1alpha1.EtcdMaintenance{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "tenant-backup-absent", Namespace: ns}, got); err != nil {
		t.Fatalf("get EtcdMaintenance: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.EtcdBackupDestinationAbsent)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("EtcdBackupDestinationAbsent condition not set True in tenant namespace when S3 absent")
	}
}
