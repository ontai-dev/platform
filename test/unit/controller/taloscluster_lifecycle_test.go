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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// buildManagementTalosCluster returns a TalosCluster configured for the
// management cluster direct bootstrap path (capi.enabled=false).
// TalosVersion is set to a realistic value so ensureBootstrapRunnerConfig can
// derive the conductor image per INV-012.
func buildManagementTalosCluster(name, namespace string) *platformv1alpha1.TalosCluster {
	return &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			Mode:         platformv1alpha1.TalosClusterModeBootstrap,
			TalosVersion: "v1.9.3",
			// CAPI nil -- disabled path (C-34: nil suppresses capi block in YAML)
		},
	}
}

// buildImportTalosCluster returns a TalosCluster configured for the management
// cluster import path (capi.enabled=false, mode=import).
// TalosVersion is set so ensureBootstrapRunnerConfig can derive the conductor image.
func buildImportTalosCluster(name, namespace string) *platformv1alpha1.TalosCluster {
	return &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			Mode:         platformv1alpha1.TalosClusterModeImport,
			TalosVersion: "v1.9.3",
			// CAPI nil -- disabled path (C-34: nil suppresses capi block in YAML)
		},
	}
}

// buildFakeTalosconfigSecret returns a Secret mimicking the talosconfig Secret
// that the import path reads from seam-tenant-{cluster}. Content is a minimal
// valid YAML placeholder -- the talos goclient is bypassed via KubeconfigGeneratorFn.
// Governor ruling 2026-04-21: talosconfig Secret lives in seam-tenant-{clusterName}.
func buildFakeTalosconfigSecret(clusterName string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "seam-mc-" + clusterName + "-talosconfig",
			Namespace: "seam-tenant-" + clusterName,
		},
		Data: map[string][]byte{
			"talosconfig": []byte("context: " + clusterName + "\ncontexts:\n  " + clusterName + ":\n    endpoints: []\n"),
		},
	}
}

// fakeKubeconfigGenerator is a KubeconfigGeneratorFn that returns a minimal
// placeholder kubeconfig. Used to bypass the real talos goclient in unit tests.
func fakeKubeconfigGenerator(_ context.Context, _, _ string) ([]byte, error) {
	return []byte("apiVersion: v1\nkind: Config\nclusters: []\nusers: []\ncontexts: []\n"), nil
}

// TestTalosClusterReconcile_ImportModeCreatesRunnerConfigAndTransitionsToReady
// verifies that spec.mode=import creates a RunnerConfig in ont-system, generates
// the kubeconfig Secret, sets origin=imported and Ready=True, and never submits a
// bootstrap Job. KubeconfigGeneratorFn is injected to bypass the talos goclient.
// platform-schema.md §5 TalosClusterModeImport.
func TestTalosClusterReconcile_ImportModeCreatesRunnerConfigAndTransitionsToReady(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster("ccs-mgmt", "seam-system")
	talosconfigSecret := buildFakeTalosconfigSecret("ccs-mgmt")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosconfigSecret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(32),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Import mode must not requeue — cluster is immediately Ready.
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue for import mode, got RequeueAfter=%v", result.RequeueAfter)
	}

	// No bootstrap Job must have been submitted.
	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobList.Items) != 0 {
		t.Errorf("import mode must not submit a bootstrap Job, got %d Jobs", len(jobList.Items))
	}

	// RunnerConfig must exist in ont-system with the cluster name and correct image.
	rcList := &controller.OperationalRunnerConfigList{}
	if err := c.List(context.Background(), rcList); err != nil {
		t.Fatalf("list RunnerConfigs: %v", err)
	}
	if len(rcList.Items) != 1 {
		t.Errorf("expected 1 RunnerConfig after import, got %d", len(rcList.Items))
	}
	if len(rcList.Items) > 0 {
		rc := rcList.Items[0]
		if rc.Name != "ccs-mgmt" {
			t.Errorf("RunnerConfig name = %q, want ccs-mgmt", rc.Name)
		}
		if rc.Namespace != "ont-system" {
			t.Errorf("RunnerConfig namespace = %q, want ont-system", rc.Namespace)
		}
		// Image: conductor-execute (executor image) with :dev tag in lab.
		// conductor-schema.md §3, INV-012, Decision 12.
		wantImage := "10.20.0.1:5000/ontai-dev/conductor-execute:dev"
		if rc.Spec.RunnerImage != wantImage {
			t.Errorf("RunnerConfig RunnerImage = %q, want %q", rc.Spec.RunnerImage, wantImage)
		}
	}

	// TalosCluster must be Ready with origin=imported.
	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-mgmt", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	if got.Status.Origin != platformv1alpha1.TalosClusterOriginImported {
		t.Errorf("Origin = %q, want imported", got.Status.Origin)
	}
	readyCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeReady)
	if readyCond == nil {
		t.Fatal("Ready condition not set after import")
	}
	if readyCond.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s, want True", readyCond.Status)
	}
	if readyCond.Reason != platformv1alpha1.ReasonClusterReady {
		t.Errorf("Ready reason = %q, want %q", readyCond.Reason, platformv1alpha1.ReasonClusterReady)
	}
	bootstrapCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeBootstrapped)
	if bootstrapCond == nil {
		t.Fatal("Bootstrapped condition not set after import")
	}
	if bootstrapCond.Status != metav1.ConditionTrue {
		t.Errorf("Bootstrapped = %s, want True", bootstrapCond.Status)
	}
	if bootstrapCond.Reason != platformv1alpha1.ReasonImportComplete {
		t.Errorf("Bootstrapped reason = %q, want %q", bootstrapCond.Reason, platformv1alpha1.ReasonImportComplete)
	}

	// Kubeconfig Secret must exist in seam-tenant-{cluster}.
	// Governor ruling 2026-04-21: kubeconfig Secret lives in seam-tenant-{clusterName}.
	kubeconfigSecret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "seam-mc-ccs-mgmt-kubeconfig",
		Namespace: "seam-tenant-ccs-mgmt",
	}, kubeconfigSecret); err != nil {
		t.Fatalf("kubeconfig Secret not created: %v", err)
	}
	if _, ok := kubeconfigSecret.Data["value"]; !ok {
		t.Error("kubeconfig Secret missing 'value' key")
	}
}

// TestTalosClusterReconcile_ImportMode_TalosConfigAbsent verifies that when
// spec.mode=import and the talosconfig Secret is absent, the reconciler sets
// KubeconfigUnavailable=True with reason TalosConfigSecretAbsent and requeues.
// platform-schema.md §5 TalosClusterModeImport.
func TestTalosClusterReconcile_ImportMode_TalosConfigAbsent(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster("ccs-mgmt", "seam-system")

	// No talosconfig Secret pre-created.
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: clientevents.NewFakeRecorder(32),
		// No KubeconfigGeneratorFn — should not reach the generator because talosconfig is absent.
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue when talosconfig Secret is absent, got RequeueAfter=0")
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-mgmt", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeKubeconfigUnavailable)
	if cond == nil {
		t.Fatal("KubeconfigUnavailable condition not set")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("KubeconfigUnavailable = %s, want True", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonTalosConfigSecretAbsent {
		t.Errorf("KubeconfigUnavailable reason = %q, want %q", cond.Reason, platformv1alpha1.ReasonTalosConfigSecretAbsent)
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
		Recorder: clientevents.NewFakeRecorder(32),
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

	// Bootstrapped condition must be False with BootstrapJobSubmitted reason (in progress).
	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-mgmt", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeBootstrapped)
	if cond == nil {
		t.Fatal("Bootstrapped condition not set after bootstrap Job submission")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Bootstrapped = %s, want False (job in progress)", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonBootstrapJobSubmitted {
		t.Errorf("Bootstrapped reason = %q, want %q", cond.Reason, platformv1alpha1.ReasonBootstrapJobSubmitted)
	}
}

// TestTalosClusterReconcile_ManagementBootstrapComplete verifies that when the
// InfrastructureTalosClusterOperationResult CR reports status=Succeeded, the reconciler
// transitions the TalosCluster to Ready=True and clears the Bootstrapping condition.
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

	// Pre-create the TCOR CR with status=Succeeded. CR name equals the Job name.
	resultTCOR := successResultTCOR("ccs-mgmt-bootstrap", "seam-system")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, existingJob, resultTCOR).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: clientevents.NewFakeRecorder(32),
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
		Recorder: clientevents.NewFakeRecorder(32),
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
		if rc.Name != "ccs-mgmt" {
			t.Errorf("RunnerConfig name = %q, want ccs-mgmt", rc.Name)
		}
		if rc.Namespace != "ont-system" {
			t.Errorf("RunnerConfig namespace = %q, want ont-system", rc.Namespace)
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

	// Pre-create the RunnerConfig in ont-system, simulating a prior bootstrap
	// sequence or a prior Platform session that left it behind.
	// Name equals TalosCluster.Name — Conductor resolves it by cluster-ref flag value.
	existingRC := &controller.OperationalRunnerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ccs-mgmt",
			Namespace: "ont-system",
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
		Recorder: clientevents.NewFakeRecorder(32),
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
		Recorder: clientevents.NewFakeRecorder(32),
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
		Recorder: clientevents.NewFakeRecorder(32),
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
		Recorder: clientevents.NewFakeRecorder(32),
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

// TestTalosClusterReconcile_StatusPatchRetryOnConflict verifies that the status
// patch deferred closure writes computed status correctly. The fake client does
// not simulate 409 Conflict, but this test confirms the re-fetch path in
// RetryOnConflict does not regress: after reconcile, the fresh Get inside the
// retry closure sees the latest object and the computed status is applied.
// PLATFORM-BL-STATUS-PATCH-CONFLICT.
func TestTalosClusterReconcile_StatusPatchRetryOnConflict(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildManagementTalosCluster("ccs-mgmt", "seam-system")

	// Pre-create a bootstrap Job so the reconciler doesn't try to submit one
	// (which would require RunnerConfig).
	existingJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ccs-mgmt-bootstrap",
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
		Recorder: clientevents.NewFakeRecorder(32),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-mgmt", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	// The deferred status patch must have applied at least ObservedGeneration.
	if got.Status.ObservedGeneration != tc.Generation {
		t.Errorf("ObservedGeneration = %d after reconcile; want %d (deferred status patch must fire)",
			got.Status.ObservedGeneration, tc.Generation)
	}
}

// TestTalosClusterReconcile_TenantImport_CreatesLocalQueue verifies PLATFORM-BL-3:
// when a Role=Tenant TalosCluster is imported (spec.mode=import), the reconciler
// creates a Kueue LocalQueue named pack-deploy-queue in seam-tenant-{cluster-name}
// pointing to ClusterQueue seam-pack-deploy.
func TestTalosClusterReconcile_TenantImport_CreatesLocalQueue(t *testing.T) {
	scheme := buildDay2Scheme(t)

	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ccs-dev",
			Namespace: "seam-system",
			Generation: 1,
		},
		Spec: platformv1alpha1.TalosClusterSpec{
			Mode:         platformv1alpha1.TalosClusterModeImport,
			TalosVersion: "v1.9.3",
			Role:         platformv1alpha1.TalosClusterRoleTenant,
			// CAPI nil -- disabled path (C-34: nil suppresses capi block in YAML)
		},
	}
	talosconfigSecret := buildFakeTalosconfigSecret("ccs-dev")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosconfigSecret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(32),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	// Verify the LocalQueue was created in seam-tenant-ccs-dev.
	lq := &unstructured.Unstructured{}
	lq.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kueue.x-k8s.io",
		Version: "v1beta1",
		Kind:    "LocalQueue",
	})
	err = c.Get(context.Background(), types.NamespacedName{
		Name:      "pack-deploy-queue",
		Namespace: "seam-tenant-ccs-dev",
	}, lq)
	if err != nil {
		t.Fatalf("LocalQueue pack-deploy-queue not found in seam-tenant-ccs-dev: %v", err)
	}

	// Verify the clusterQueue field points to the right ClusterQueue.
	spec, ok := lq.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("LocalQueue spec is not a map")
	}
	clusterQueue, _ := spec["clusterQueue"].(string)
	if clusterQueue != "seam-pack-deploy" {
		t.Errorf("LocalQueue spec.clusterQueue = %q; want %q", clusterQueue, "seam-pack-deploy")
	}
}
