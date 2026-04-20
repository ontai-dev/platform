// Package controller_test -- AC-1 management cluster import acceptance contract tests.
//
// AC-1: After a TalosCluster CR is applied in seam-system with mode=import and
// capi.enabled=false, Platform must:
//   - Set status.origin=imported
//   - Create exactly one RunnerConfig in ont-system
//   - Set Ready=True
//   - Set LineageSynced=False/LineageControllerAbsent
//   - Not submit any Job or WorkloadSpec
//   - Complete within a single reconcile pass
//
// These tests constitute the acceptance contract gate for AC-1.
// They supplement the broader coverage in taloscluster_lifecycle_test.go.
//
// platform-schema.md §5 TalosClusterModeImport.
// seam-core-schema.md §7 Declaration 5.
package controller_test

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// TestAC1_ImportMode_SetsOriginImportedAndReady verifies that a management cluster
// import (mode=import, capi.enabled=false) sets status.origin=imported and
// Ready=True within a single reconcile pass with no requeue.
// AC-1 gate: origin and readiness contract.
func TestAC1_ImportMode_SetsOriginImportedAndReady(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster("ac1-mgmt", "seam-system")
	talosSecret := buildFakeTalosconfigSecret("ac1-mgmt")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(16),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ac1-mgmt", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("AC-1: unexpected reconcile error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("AC-1: import must complete in one pass, got RequeueAfter=%v", result.RequeueAfter)
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "ac1-mgmt", Namespace: "seam-system"}, got); err != nil {
		t.Fatalf("AC-1: get TalosCluster: %v", err)
	}
	if got.Status.Origin != platformv1alpha1.TalosClusterOriginImported {
		t.Errorf("AC-1: status.origin = %q, want imported", got.Status.Origin)
	}
	readyCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeReady)
	if readyCond == nil || readyCond.Status != metav1.ConditionTrue {
		t.Errorf("AC-1: Ready condition not True after import; cond=%v", readyCond)
	}
}

// TestAC1_ImportMode_CreatesExactlyOneRunnerConfigInOntSystem verifies that the
// import path creates exactly one RunnerConfig in ont-system. Duplicate RunnerConfigs
// would cause Conductor to run conflicting capability sets.
// AC-1 gate: RunnerConfig count and placement contract.
func TestAC1_ImportMode_CreatesExactlyOneRunnerConfigInOntSystem(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster("ac1-rc-mgmt", "seam-system")
	talosSecret := buildFakeTalosconfigSecret("ac1-rc-mgmt")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(16),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ac1-rc-mgmt", Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("AC-1: unexpected reconcile error: %v", err)
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("AC-1: list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Fatalf("AC-1: want exactly 1 RunnerConfig, got %d", len(rcList.Items))
	}
	rc := rcList.Items[0]
	if rc.Namespace != "ont-system" {
		t.Errorf("AC-1: RunnerConfig namespace = %q, want ont-system", rc.Namespace)
	}
	if rc.Name != "ac1-rc-mgmt" {
		t.Errorf("AC-1: RunnerConfig name = %q, want ac1-rc-mgmt", rc.Name)
	}
}

// TestAC1_ImportMode_DoesNotSubmitJob verifies that the import path never submits
// a bootstrap Job or WorkloadSpec. Import is a direct status transition.
// AC-1 gate: no-Job contract (INV-002).
func TestAC1_ImportMode_DoesNotSubmitJob(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster("ac1-nojob-mgmt", "seam-system")
	talosSecret := buildFakeTalosconfigSecret("ac1-nojob-mgmt")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(16),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ac1-nojob-mgmt", Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("AC-1: unexpected reconcile error: %v", err)
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("AC-1: list Jobs: %v", err)
	}
	if len(jobList.Items) != 0 {
		t.Errorf("AC-1: import mode must not submit any Job, got %d", len(jobList.Items))
	}
}

// TestAC1_ImportMode_SecondReconcileIsIdempotent verifies that a second reconcile
// on an already-imported TalosCluster does not create a duplicate RunnerConfig.
// The fake client returns AlreadyExists for the duplicate Create; the reconciler
// must handle this without error and must not increment the RunnerConfig count.
// AC-1 gate: idempotency contract.
func TestAC1_ImportMode_SecondReconcileIsIdempotent(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster("ac1-idem-mgmt", "seam-system")
	talosSecret := buildFakeTalosconfigSecret("ac1-idem-mgmt")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(16),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "ac1-idem-mgmt", Namespace: "seam-system"}}

	// First reconcile.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("AC-1 idempotency: first reconcile error: %v", err)
	}

	// Second reconcile -- simulates controller restart or watch re-trigger.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("AC-1 idempotency: second reconcile error: %v", err)
	}

	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("AC-1 idempotency: list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Errorf("AC-1 idempotency: want exactly 1 RunnerConfig after second reconcile, got %d", len(rcList.Items))
	}

	// Ready=True must still hold after second reconcile.
	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "ac1-idem-mgmt", Namespace: "seam-system"}, got); err != nil {
		t.Fatalf("AC-1 idempotency: get TalosCluster: %v", err)
	}
	readyCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeReady)
	if readyCond == nil || readyCond.Status != metav1.ConditionTrue {
		t.Errorf("AC-1 idempotency: Ready not True after second reconcile; cond=%v", readyCond)
	}
}

// TestAC1_ImportMode_LineageSyncedInitializedFalse verifies that the first reconcile
// of an import-mode TalosCluster sets LineageSynced=False with reason
// LineageControllerAbsent. This is the stub-phase contract: the LineageController
// is not yet deployed; the reconciler records the absence.
// seam-core-schema.md §7 Declaration 5. Decision 6 (root CLAUDE.md).
func TestAC1_ImportMode_LineageSyncedInitializedFalse(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster("ac1-lineage-mgmt", "seam-system")
	talosSecret := buildFakeTalosconfigSecret("ac1-lineage-mgmt")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(16),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ac1-lineage-mgmt", Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("AC-1 lineage: unexpected reconcile error: %v", err)
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "ac1-lineage-mgmt", Namespace: "seam-system"}, got); err != nil {
		t.Fatalf("AC-1 lineage: get TalosCluster: %v", err)
	}

	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced)
	if cond == nil {
		t.Fatal("AC-1 lineage: LineageSynced condition not set on first import reconcile")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("AC-1 lineage: LineageSynced = %s, want False (LineageController not yet deployed)", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonLineageControllerAbsent {
		t.Errorf("AC-1 lineage: reason = %q, want %q", cond.Reason, platformv1alpha1.ReasonLineageControllerAbsent)
	}
}
