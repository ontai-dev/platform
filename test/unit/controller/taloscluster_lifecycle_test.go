// Package controller_test — TalosCluster lifecycle unit tests.
//
// Workstream 1: TalosCluster lifecycle coverage.
//
// These tests exercise the TalosClusterReconciler paths not covered by
// taloscluster_conductor_test.go:
//
//  1. Management cluster bootstrap (capi.enabled=false): bootstrap Job submitted,
//     Bootstrapping condition set with BootstrapJobSubmitted reason.
//  2. Management cluster bootstrap completion: OperationResult ConfigMap with
//     status=success transitions the cluster to Ready=True.
//  3. LineageSynced initialization: first reconcile of any TalosCluster sets
//     LineageSynced=False/LineageControllerAbsent exactly once.
//
// All tests use the fake controller-runtime client. No live cluster required.
// platform-schema.md §5. seam-core-schema.md §7 Declaration 5.
package controller_test

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// buildManagementTalosCluster returns a TalosCluster configured for the
// management cluster direct bootstrap path (capi.enabled=false).
func buildManagementTalosCluster(name, namespace string) *platformv1alpha1.TalosCluster {
	return &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			Mode: platformv1alpha1.TalosClusterModeBootstrap,
			CAPI: platformv1alpha1.CAPIConfig{Enabled: false},
		},
	}
}

// TestTalosClusterReconcile_ManagementBootstrapJobSubmitted verifies that when a
// management cluster TalosCluster (capi.enabled=false) is first reconciled, the
// reconciler submits a bootstrap Conductor Job and sets Bootstrapping=True with
// reason BootstrapJobSubmitted.
// platform-schema.md §5, platform-design.md §5.
func TestTalosClusterReconcile_ManagementBootstrapJobSubmitted(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildManagementTalosCluster("ccs-mgmt", "seam-system")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Reconciler must requeue to poll for Job completion.
	if result.RequeueAfter == 0 {
		t.Error("expected non-zero RequeueAfter after bootstrap Job submission")
	}

	// Bootstrap Job must exist in the fake client.
	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 1 {
		t.Errorf("expected 1 bootstrap Job, got %d", len(jobList.Items))
	}
	if len(jobList.Items) > 0 {
		job := jobList.Items[0]
		if job.Name != "ccs-mgmt-bootstrap" {
			t.Errorf("bootstrap Job name = %q, want ccs-mgmt-bootstrap", job.Name)
		}
		if job.Namespace != "seam-system" {
			t.Errorf("bootstrap Job namespace = %q, want seam-system", job.Namespace)
		}
		// Verify cluster label.
		if job.Labels["platform.ontai.dev/cluster"] != "ccs-mgmt" {
			t.Errorf("Job cluster label = %q, want ccs-mgmt", job.Labels["platform.ontai.dev/cluster"])
		}
	}

	// Bootstrapping condition must be True with BootstrapJobSubmitted reason.
	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-mgmt", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeBootstrapping)
	if cond == nil {
		t.Fatal("Bootstrapping condition not set after bootstrap Job submission")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("Bootstrapping = %s, want True", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonBootstrapJobSubmitted {
		t.Errorf("Bootstrapping reason = %q, want %q", cond.Reason, platformv1alpha1.ReasonBootstrapJobSubmitted)
	}
}

// TestTalosClusterReconcile_ManagementBootstrapComplete verifies that when the
// OperationResult ConfigMap reports status=success, the reconciler transitions the
// TalosCluster to Ready=True and clears the Bootstrapping condition.
// platform-design.md §5.
func TestTalosClusterReconcile_ManagementBootstrapComplete(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildManagementTalosCluster("ccs-mgmt", "seam-system")

	// Pre-create the bootstrap Job (simulates it having been submitted in a prior reconcile).
	existingJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ccs-mgmt-bootstrap",
			Namespace: "seam-system",
		},
	}

	// Pre-create the OperationResult ConfigMap with status=success.
	resultCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ccs-mgmt-bootstrap-result",
			Namespace: "seam-system",
		},
		Data: map[string]string{
			"status":  "success",
			"message": "cluster bootstrapped",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, existingJob, resultCM).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Bootstrap complete — no requeue needed.
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue after bootstrap completion, got RequeueAfter=%v", result.RequeueAfter)
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-mgmt", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}

	// Ready must be True.
	readyCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeReady)
	if readyCond == nil {
		t.Fatal("Ready condition not set after bootstrap completion")
	}
	if readyCond.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s, want True", readyCond.Status)
	}
	if readyCond.Reason != platformv1alpha1.ReasonClusterReady {
		t.Errorf("Ready reason = %q, want %q", readyCond.Reason, platformv1alpha1.ReasonClusterReady)
	}

	// Origin must be bootstrapped.
	if got.Status.Origin != platformv1alpha1.TalosClusterOriginBootstrapped {
		t.Errorf("Origin = %q, want bootstrapped", got.Status.Origin)
	}
}

// TestTalosClusterReconcile_RunnerConfigCreatedOnFirstObservation verifies that when a
// management cluster TalosCluster (capi.enabled=false) has no RunnerConfig in the fake
// client, the first reconcile creates one with the correct clusterRef.
// platform-schema.md §3 Part 2: RunnerConfig creation on first observation.
func TestTalosClusterReconcile_RunnerConfigCreatedOnFirstObservation(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildManagementTalosCluster("ccs-mgmt", "seam-system")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// RunnerConfig must exist after first reconcile.
	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Errorf("expected 1 RunnerConfig after first observation, got %d", len(rcList.Items))
	}
	if len(rcList.Items) > 0 {
		rc := rcList.Items[0]
		if rc.Spec.ClusterRef != "ccs-mgmt" {
			t.Errorf("RunnerConfig clusterRef = %q, want ccs-mgmt", rc.Spec.ClusterRef)
		}
		if rc.Namespace != "seam-system" {
			t.Errorf("RunnerConfig namespace = %q, want seam-system", rc.Namespace)
		}
	}
}

// TestTalosClusterReconcile_RunnerConfigAlreadyExistsSkipsJob verifies that when a
// RunnerConfig pre-exists in ont-system for this cluster (no bootstrap Job present),
// the reconciler skips Job submission and transitions the TalosCluster directly to
// Ready. This is the idempotency guard for clusters already running when the CR
// is applied. platform-schema.md §3 Parts 1 and 3.
func TestTalosClusterReconcile_RunnerConfigAlreadyExistsSkipsJob(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildManagementTalosCluster("ccs-mgmt", "seam-system")

	// Pre-create the RunnerConfig, simulating a prior bootstrap sequence or
	// a prior Platform session that left it behind.
	existingRC := &controller.OperationalRunnerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ccs-mgmt-bootstrap-rc",
			Namespace: "seam-system",
			Labels: map[string]string{
				"platform.ontai.dev/cluster": "ccs-mgmt",
			},
		},
		Spec: controller.OperationalRunnerConfigSpec{
			ClusterRef:  "ccs-mgmt",
			RunnerImage: "registry.ontai.dev/ontai-dev/conductor:latest",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, existingRC).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No requeue — management cluster acknowledged as operational.
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue when RunnerConfig pre-exists and no Job, got RequeueAfter=%v",
			result.RequeueAfter)
	}

	// No bootstrap Job must have been submitted.
	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 0 {
		t.Errorf("expected 0 Jobs when RunnerConfig pre-exists, got %d", len(jobList.Items))
	}

	// TalosCluster must be Ready with origin=bootstrapped.
	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-mgmt", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	readyCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeReady)
	if readyCond == nil {
		t.Fatal("Ready condition not set when RunnerConfig pre-exists and no Job")
	}
	if readyCond.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s, want True", readyCond.Status)
	}
	if readyCond.Reason != platformv1alpha1.ReasonClusterReady {
		t.Errorf("Ready reason = %q, want %q", readyCond.Reason, platformv1alpha1.ReasonClusterReady)
	}
	if got.Status.Origin != platformv1alpha1.TalosClusterOriginBootstrapped {
		t.Errorf("Origin = %q, want bootstrapped", got.Status.Origin)
	}
}

// TestTalosClusterReconcile_ManagementClusterReadyAfterRunnerConfigPresent verifies
// Part 3: after a first-observation reconcile creates the RunnerConfig, a second
// reconcile that finds the RunnerConfig present but no Job transitions the cluster
// to Ready. This validates the two-pass idempotency sequence for a pre-existing
// management cluster. platform-schema.md §3.
func TestTalosClusterReconcile_ManagementClusterReadyAfterRunnerConfigPresent(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildManagementTalosCluster("ccs-mgmt", "seam-system")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
	}
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"},
	}

	// First reconcile: no RunnerConfig pre-exists → creates RunnerConfig and
	// submits bootstrap Job (fresh bootstrap path).
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile error: %v", err)
	}

	// Remove the submitted Job to simulate the scenario where the cluster was already
	// running (the Job was never submitted in reality — we simulate the state after
	// the RunnerConfig exists but no Job is present).
	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	for i := range jobList.Items {
		if err := c.Delete(context.Background(), &jobList.Items[i]); err != nil {
			t.Fatalf("delete bootstrap Job: %v", err)
		}
	}

	// Second reconcile: RunnerConfig now pre-exists, no Job → must transition to Ready.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile error: %v", err)
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatalf("get TalosCluster after second reconcile: %v", err)
	}
	readyCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeReady)
	if readyCond == nil {
		t.Fatal("Ready condition not set on second reconcile with RunnerConfig present and no Job")
	}
	if readyCond.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s, want True", readyCond.Status)
	}
	if got.Status.Origin != platformv1alpha1.TalosClusterOriginBootstrapped {
		t.Errorf("Origin = %q, want bootstrapped", got.Status.Origin)
	}
}

// TestTalosClusterReconcile_LineageSyncedInitialized verifies that the first reconcile
// of a TalosCluster initializes the LineageSynced condition to False with reason
// LineageControllerAbsent. This is a one-time write — InfrastructureLineageController
// takes ownership when deployed. seam-core-schema.md §7 Declaration 5.
func TestTalosClusterReconcile_LineageSyncedInitialized(t *testing.T) {
	scheme := buildDay2Scheme(t)
	// Use a simple management cluster that will reconcile quickly.
	tc := buildManagementTalosCluster("ccs-mgmt-lineage", "seam-system")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt-lineage", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-mgmt-lineage", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}

	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced)
	if cond == nil {
		t.Fatal("LineageSynced condition not initialized on first reconcile")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("LineageSynced = %s, want False (stub phase — controller absent)", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonLineageControllerAbsent {
		t.Errorf("LineageSynced reason = %q, want %q",
			cond.Reason, platformv1alpha1.ReasonLineageControllerAbsent)
	}
}

// TestTalosClusterReconcile_LineageSyncedNotUpdatedOnSecondReconcile verifies the
// one-time write invariant: if LineageSynced is already set, the reconciler does
// not overwrite it on subsequent reconciles.
// seam-core-schema.md §7 Declaration 5: "The reconciler writes it once; it does
// not poll or re-evaluate it."
func TestTalosClusterReconcile_LineageSyncedNotUpdatedOnSecondReconcile(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildManagementTalosCluster("ccs-mgmt-lineage2", "seam-system")

	// Pre-populate the bootstrap Job so the second reconcile does not recreate it.
	existingJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ccs-mgmt-lineage2-bootstrap",
			Namespace: "seam-system",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, existingJob).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
	}
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt-lineage2", Namespace: "seam-system"},
	}

	// First reconcile: sets LineageSynced=False.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile error: %v", err)
	}

	// Manually simulate LineageController ownership: set LineageSynced=True.
	updated := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("get after first reconcile: %v", err)
	}
	platformv1alpha1.SetCondition(
		&updated.Status.Conditions,
		platformv1alpha1.ConditionTypeLineageSynced,
		metav1.ConditionTrue,
		"LineageControllerOwned",
		"Simulated LineageController ownership for test.",
		updated.Generation,
	)
	if err := c.Status().Update(context.Background(), updated); err != nil {
		t.Fatalf("simulate LineageController ownership: %v", err)
	}

	// Second reconcile: reconciler must NOT overwrite LineageSynced.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile error: %v", err)
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatalf("get after second reconcile: %v", err)
	}

	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced)
	if cond == nil {
		t.Fatal("LineageSynced condition missing after second reconcile")
	}
	// LineageSynced must remain True — reconciler must not overwrite it.
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("LineageSynced = %s after second reconcile, want True (reconciler must not overwrite controller-owned condition)",
			cond.Status)
	}
}
