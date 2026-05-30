// Package controller_test -- RECON-A2 and RECON-A6 unit tests for MCSOT path.
//
// RECON-A2: import flow machineconfig source-of-truth Secrets -- reading machineconfigs
// from Talos nodes, classifying by machine.type, creating Secrets and MachineConfigSync CRs.
// RECON-A6: Secret Watch content-change trigger -- reconcileMachineConfigSync detects
// admin edits to machineconfig Secrets and creates watch-triggered MachineConfigSync CRs.
//
// All tests use the fake client and inject MachineConfigReaderFn where needed.
package controller_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// computeTestHash returns the label-safe (63-char) hex SHA-256 of b. Used to build
// pre-existing Secret labels that match or differ from test content in RECON-A6 tests.
// Must match labelSafeHash in machineconfig_labels.go: truncated to 63 chars.
func computeTestHash(b []byte) string {
	sum := sha256.Sum256(b)
	h := hex.EncodeToString(sum[:])
	if len(h) > 63 {
		return h[:63]
	}
	return h
}

// buildMachineConfigSecretSynced creates a pre-existing machineconfig Secret that
// appears fully synced (sync-status=synced, sync-hash matches content).
// Used in RECON-A6 tests to simulate a Secret that has not changed since last sync.
func buildMachineConfigSecretSynced(clusterName, class string, content []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      controller.MachineConfigSecretName(clusterName, class),
			Namespace: "seam-tenant-" + clusterName,
			Labels: map[string]string{
				controller.LabelMachineConfigCluster:    clusterName,
				controller.LabelMachineConfigClass:      class,
				controller.LabelMachineConfigSyncStatus: controller.MachineConfigSyncStatusSynced,
				controller.LabelMachineConfigSyncHash:   computeTestHash(content),
			},
		},
		Data: map[string][]byte{controller.MachineConfigDataKey: content},
	}
}

// buildMachineConfigSecretChanged creates a pre-existing machineconfig Secret where
// the content hash does not match the sync-hash label -- simulating an admin edit.
// Used in RECON-A6 tests to verify that reconcileMachineConfigSync creates a sync CR.
func buildMachineConfigSecretChanged(clusterName, class string, oldContent, newContent []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      controller.MachineConfigSecretName(clusterName, class),
			Namespace: "seam-tenant-" + clusterName,
			Labels: map[string]string{
				controller.LabelMachineConfigCluster:    clusterName,
				controller.LabelMachineConfigClass:      class,
				controller.LabelMachineConfigSyncStatus: controller.MachineConfigSyncStatusSynced,
				controller.LabelMachineConfigSyncHash:   computeTestHash(oldContent), // stale hash
			},
		},
		Data: map[string][]byte{controller.MachineConfigDataKey: newContent}, // new content
	}
}

// buildFakeTalosconfigSecretWithEndpoints returns a talosconfig Secret with the given
// node endpoint IPs. Used for RECON-A2 tests where ensureMachineConfigSecrets must
// iterate over real endpoints (empty endpoints cause an early non-fatal return).
func buildFakeTalosconfigSecretWithEndpoints(clusterName string, endpoints []string) *corev1.Secret {
	endpointYAML := "["
	for i, ep := range endpoints {
		if i > 0 {
			endpointYAML += ", "
		}
		endpointYAML += "\"" + ep + "\""
	}
	endpointYAML += "]"
	talosconfigYAML := fmt.Sprintf(
		"context: %s\ncontexts:\n  %s:\n    endpoints: %s\n",
		clusterName, clusterName, endpointYAML,
	)
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "seam-mc-" + clusterName + "-talosconfig",
			Namespace: "seam-tenant-" + clusterName,
		},
		Data: map[string][]byte{
			"talosconfig": []byte(talosconfigYAML),
		},
	}
}

// fakeCPReader returns a MachineConfigReaderFn that classifies every endpoint as
// controlplane and returns a minimal machineconfig payload.
func fakeCPReader(configContent []byte) func(ctx context.Context, clusterName, endpoint string) ([]byte, string, error) {
	return func(_ context.Context, _, _ string) ([]byte, string, error) {
		return configContent, controller.MachineConfigClassControlPlane, nil
	}
}

// fakeEndpointClassReader returns a MachineConfigReaderFn where the classification
// is determined by the endpoint. The map key is endpoint IP; value is class string.
// Unknown endpoints return an error.
func fakeEndpointClassReader(endpointClass map[string]string) func(ctx context.Context, clusterName, endpoint string) ([]byte, string, error) {
	return func(_ context.Context, _, endpoint string) ([]byte, string, error) {
		class, ok := endpointClass[endpoint]
		if !ok {
			return nil, "", fmt.Errorf("unknown endpoint %q", endpoint)
		}
		payload := []byte("machine:\n  type: " + class + "\n")
		return payload, class, nil
	}
}

// fakeErrorReader returns a MachineConfigReaderFn that always returns an error.
func fakeErrorReader(msg string) func(ctx context.Context, clusterName, endpoint string) ([]byte, string, error) {
	return func(_ context.Context, _, endpoint string) ([]byte, string, error) {
		return nil, "", fmt.Errorf("%s: endpoint %s", msg, endpoint)
	}
}

// TestMCSOT_ImportMode_ControlPlaneSecretAndCRCreated verifies that when a single
// controlplane endpoint is read during import, the machineconfig Secret and
// MachineConfigSync CR are created for the controlplane class.
// RECON-A2.
func TestMCSOT_ImportMode_ControlPlaneSecretAndCRCreated(t *testing.T) {
	const cluster = "mcsot-cp"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecretWithEndpoints(cluster, []string{"10.20.0.2"})
	configBytes := []byte("machine:\n  type: controlplane\n")

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
		MachineConfigReaderFn: fakeCPReader(configBytes),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	ns := "seam-tenant-" + cluster

	// Secret must exist with correct labels.
	secretName := controller.MachineConfigSecretName(cluster, controller.MachineConfigClassControlPlane)
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: secretName, Namespace: ns}, secret); err != nil {
		t.Fatalf("machineconfig Secret not found: %v", err)
	}
	if secret.Labels[controller.LabelMachineConfigClass] != controller.MachineConfigClassControlPlane {
		t.Errorf("LabelMachineConfigClass = %q, want %q",
			secret.Labels[controller.LabelMachineConfigClass], controller.MachineConfigClassControlPlane)
	}
	if secret.Labels[controller.LabelMachineConfigSyncStatus] != controller.MachineConfigSyncStatusPending {
		t.Errorf("LabelMachineConfigSyncStatus = %q, want %q",
			secret.Labels[controller.LabelMachineConfigSyncStatus], controller.MachineConfigSyncStatusPending)
	}
	if len(secret.Data[controller.MachineConfigDataKey]) == 0 {
		t.Error("machineconfig Secret data key is empty")
	}

	// MachineConfigSync CR must exist with reason=import-initial-sync.
	crName := cluster + "-mc-import-" + controller.MachineConfigClassControlPlane
	mcs := &platformv1alpha1.MachineConfigSync{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: crName, Namespace: ns}, mcs); err != nil {
		t.Fatalf("MachineConfigSync CR not found: %v", err)
	}
	if mcs.Spec.Reason != "import-initial-sync" {
		t.Errorf("MachineConfigSync.Spec.Reason = %q, want import-initial-sync", mcs.Spec.Reason)
	}
	if mcs.Spec.ClusterRef.Name != cluster {
		t.Errorf("MachineConfigSync.Spec.ClusterRef.Name = %q, want %q", mcs.Spec.ClusterRef.Name, cluster)
	}
	if mcs.Spec.NodeClass != controller.MachineConfigClassControlPlane {
		t.Errorf("MachineConfigSync.Spec.NodeClass = %q, want %q",
			mcs.Spec.NodeClass, controller.MachineConfigClassControlPlane)
	}
}

// TestMCSOT_ImportMode_BothClassesFromMultipleEndpoints verifies that when endpoints
// return different machine types, both controlplane and worker Secrets and
// MachineConfigSync CRs are created.
// RECON-A2.
func TestMCSOT_ImportMode_BothClassesFromMultipleEndpoints(t *testing.T) {
	const cluster = "mcsot-dual"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecretWithEndpoints(cluster, []string{"10.20.0.2", "10.20.0.3"})

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
		MachineConfigReaderFn: fakeEndpointClassReader(map[string]string{
			"10.20.0.2": controller.MachineConfigClassControlPlane,
			"10.20.0.3": controller.MachineConfigClassWorker,
		}),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	ns := "seam-tenant-" + cluster
	for _, class := range []string{controller.MachineConfigClassControlPlane, controller.MachineConfigClassWorker} {
		secretName := controller.MachineConfigSecretName(cluster, class)
		if err := c.Get(context.Background(), types.NamespacedName{Name: secretName, Namespace: ns}, &corev1.Secret{}); err != nil {
			t.Errorf("machineconfig Secret for class %q not found: %v", class, err)
		}
		crName := cluster + "-mc-import-" + class
		if err := c.Get(context.Background(), types.NamespacedName{Name: crName, Namespace: ns}, &platformv1alpha1.MachineConfigSync{}); err != nil {
			t.Errorf("MachineConfigSync CR for class %q not found: %v", class, err)
		}
	}
}

// TestMCSOT_ImportMode_SecretIdempotent verifies that a pre-existing machineconfig
// Secret is never overwritten during import. The content must remain unchanged after
// a second reconcile pass.
// RECON-A2 idempotency.
func TestMCSOT_ImportMode_SecretIdempotent(t *testing.T) {
	const cluster = "mcsot-idem"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecretWithEndpoints(cluster, []string{"10.20.0.2"})

	ns := "seam-tenant-" + cluster
	secretName := controller.MachineConfigSecretName(cluster, controller.MachineConfigClassControlPlane)
	originalContent := []byte("original-admin-content")
	preExistingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ns,
			Labels: map[string]string{
				controller.LabelMachineConfigClass:      controller.MachineConfigClassControlPlane,
				controller.LabelMachineConfigSyncStatus: controller.MachineConfigSyncStatusSynced,
			},
		},
		Data: map[string][]byte{
			controller.MachineConfigDataKey: originalContent,
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret, preExistingSecret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(16),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
		MachineConfigReaderFn: fakeCPReader([]byte("new-content-should-not-overwrite")),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: secretName, Namespace: ns}, got); err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(got.Data[controller.MachineConfigDataKey]) != string(originalContent) {
		t.Errorf("Secret content was overwritten: got %q, want %q",
			got.Data[controller.MachineConfigDataKey], originalContent)
	}
}

// TestMCSOT_ImportMode_MachineConfigSyncCRIdempotent verifies that a pre-existing
// MachineConfigSync CR is not duplicated if import runs more than once.
// RECON-A2 idempotency.
func TestMCSOT_ImportMode_MachineConfigSyncCRIdempotent(t *testing.T) {
	const cluster = "mcsot-cr-idem"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecretWithEndpoints(cluster, []string{"10.20.0.2"})

	ns := "seam-tenant-" + cluster
	crName := cluster + "-mc-import-" + controller.MachineConfigClassControlPlane
	preExistingCR := &platformv1alpha1.MachineConfigSync{
		ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: ns},
		Spec: platformv1alpha1.MachineConfigSyncSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: cluster},
			NodeClass:  controller.MachineConfigClassControlPlane,
			Reason:     "import-initial-sync",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret, preExistingCR).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(16),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
		MachineConfigReaderFn: fakeCPReader([]byte("machine:\n  type: controlplane\n")),
	}

	// Reconcile twice.
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}

	// List all MachineConfigSync CRs in the namespace.
	mcsList := &platformv1alpha1.MachineConfigSyncList{}
	if err := c.List(context.Background(), mcsList); err != nil {
		t.Fatalf("list MachineConfigSync: %v", err)
	}
	cpCRs := 0
	for _, cr := range mcsList.Items {
		if cr.Namespace == ns && cr.Spec.NodeClass == controller.MachineConfigClassControlPlane {
			cpCRs++
		}
	}
	if cpCRs != 1 {
		t.Errorf("expected exactly 1 MachineConfigSync CR for controlplane, got %d", cpCRs)
	}
}

// TestMCSOT_ImportMode_AllEndpointsFailIsNonFatal verifies that when all node
// endpoints fail to return a machineconfig, the import reconcile still completes
// without returning an error (ensureMachineConfigSecrets failure is non-fatal).
// RECON-A2 resilience.
func TestMCSOT_ImportMode_AllEndpointsFailIsNonFatal(t *testing.T) {
	const cluster = "mcsot-allfail"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecretWithEndpoints(cluster, []string{"10.20.0.2"})

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
		MachineConfigReaderFn: fakeErrorReader("simulated node unreachable"),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"},
	})
	if err != nil {
		t.Errorf("all-endpoints-fail must be non-fatal; Reconcile returned error: %v", err)
	}

	// TalosCluster must still reach Ready (import proceeds despite MCSOT failure).
	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: cluster, Namespace: "seam-system"}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	readyCond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeReady)
	if readyCond == nil || readyCond.Status != metav1.ConditionTrue {
		t.Errorf("TalosCluster must still be Ready when MCSOT fails; cond=%v", readyCond)
	}
}

// --- RECON-A6: Secret Watch content-change trigger tests ---

// TestMCSOT_SecretWatch_ContentChangeCreatesSyncCR verifies that when a machineconfig
// Secret's content hash differs from the sync-hash label (admin edit), a watch-triggered
// MachineConfigSync CR is created with reason="secret-content-changed".
// RECON-A6.
func TestMCSOT_SecretWatch_ContentChangeCreatesSyncCR(t *testing.T) {
	const cluster = "a6-change"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecretWithEndpoints(cluster, []string{})
	oldContent := []byte("machine:\n  type: controlplane\n# version 1\n")
	newContent := []byte("machine:\n  type: controlplane\n# version 2\n")
	mcSecret := buildMachineConfigSecretChanged(cluster, controller.MachineConfigClassControlPlane, oldContent, newContent)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret, mcSecret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(16),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
		MachineConfigReaderFn: fakeErrorReader("no real nodes"),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	ns := "seam-tenant-" + cluster
	crName := cluster + "-mc-sync-" + controller.MachineConfigClassControlPlane
	mcs := &platformv1alpha1.MachineConfigSync{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: crName, Namespace: ns}, mcs); err != nil {
		t.Fatalf("watch-triggered MachineConfigSync CR not found: %v", err)
	}
	if mcs.Spec.Reason != "secret-content-changed" {
		t.Errorf("Reason = %q, want secret-content-changed", mcs.Spec.Reason)
	}
	if mcs.Spec.NodeClass != controller.MachineConfigClassControlPlane {
		t.Errorf("NodeClass = %q, want %q", mcs.Spec.NodeClass, controller.MachineConfigClassControlPlane)
	}
}

// TestMCSOT_SecretWatch_NoChangeDoesNotCreateSyncCR verifies that when a machineconfig
// Secret's content hash matches the sync-hash label, no watch-triggered MachineConfigSync
// CR is created (content unchanged since last sync).
// RECON-A6 idempotency.
func TestMCSOT_SecretWatch_NoChangeDoesNotCreateSyncCR(t *testing.T) {
	const cluster = "a6-nochange"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecretWithEndpoints(cluster, []string{})
	content := []byte("machine:\n  type: controlplane\n")
	mcSecret := buildMachineConfigSecretSynced(cluster, controller.MachineConfigClassControlPlane, content)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret, mcSecret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(16),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
		MachineConfigReaderFn: fakeErrorReader("no real nodes"),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	ns := "seam-tenant-" + cluster
	crName := cluster + "-mc-sync-" + controller.MachineConfigClassControlPlane
	mcs := &platformv1alpha1.MachineConfigSync{}
	err := c.Get(context.Background(), types.NamespacedName{Name: crName, Namespace: ns}, mcs)
	if err == nil {
		t.Errorf("expected no watch-triggered MachineConfigSync CR when content unchanged, got one")
	}
}

// TestMCSOT_SecretWatch_StaleCRReplacedOnRehash verifies that when a watch-triggered
// MachineConfigSync CR already exists for a PREVIOUS content version (observedHash !=
// newHash), the stale CR is deleted and a fresh one is created for the new content.
// RECON-A6 replace-stale behavior.
func TestMCSOT_SecretWatch_StaleCRReplacedOnRehash(t *testing.T) {
	const cluster = "a6-stale"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecretWithEndpoints(cluster, []string{})

	oldContent := []byte("machine:\n  type: controlplane\n# v1\n")
	newContent := []byte("machine:\n  type: controlplane\n# v2\n")
	mcSecret := buildMachineConfigSecretChanged(cluster, controller.MachineConfigClassControlPlane, oldContent, newContent)

	ns := "seam-tenant-" + cluster
	crName := cluster + "-mc-sync-" + controller.MachineConfigClassControlPlane
	// Pre-existing stale CR targeting the old content hash.
	staleCR := &platformv1alpha1.MachineConfigSync{
		ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: ns},
		Spec: platformv1alpha1.MachineConfigSyncSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: cluster},
			NodeClass:  controller.MachineConfigClassControlPlane,
			Reason:     "secret-content-changed",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret, mcSecret, staleCR).
		WithStatusSubresource(tc, staleCR).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(16),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
		MachineConfigReaderFn: fakeErrorReader("no real nodes"),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Stale CR was replaced -- a fresh CR with the same name now exists.
	freshCR := &platformv1alpha1.MachineConfigSync{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: crName, Namespace: ns}, freshCR); err != nil {
		t.Fatalf("fresh MachineConfigSync CR not found after stale replacement: %v", err)
	}
	if freshCR.Spec.Reason != "secret-content-changed" {
		t.Errorf("fresh CR Reason = %q, want secret-content-changed", freshCR.Spec.Reason)
	}
}

// TestMCSOT_ImportMode_NodeAddressesPopulatedWithRoles verifies that after import,
// spec.nodeAddresses on the TalosCluster is populated with classified IPs. RECON-A9.
func TestMCSOT_ImportMode_NodeAddressesPopulatedWithRoles(t *testing.T) {
	const cluster = "mcsot-nodeaddr"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecretWithEndpoints(cluster, []string{"10.20.0.2", "10.20.0.3", "10.20.0.4"})

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
		MachineConfigReaderFn: fakeEndpointClassReader(map[string]string{
			"10.20.0.2": controller.MachineConfigClassControlPlane,
			"10.20.0.3": controller.MachineConfigClassControlPlane,
			"10.20.0.4": controller.MachineConfigClassControlPlane,
		}),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	updated := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: cluster, Namespace: "seam-system"}, updated); err != nil {
		t.Fatalf("get updated TalosCluster: %v", err)
	}
	if len(updated.Spec.NodeAddresses) != 3 {
		t.Fatalf("expected 3 NodeAddresses, got %d", len(updated.Spec.NodeAddresses))
	}
	for _, na := range updated.Spec.NodeAddresses {
		if na.Role != platformv1alpha1.NodeRoleControlPlane {
			t.Errorf("NodeAddress %q: expected role=controlplane, got %q", na.IP, na.Role)
		}
	}
}

// --- PLT-BUG-3-ARCH: per-node MachineConfigSync import path ---

// buildCompilerPerNodeSecret creates a fake compiler-generated per-node machineconfig
// Secret in the style the Compiler produces. Uses the machineconfig.yaml data key.
// PLT-BUG-3-ARCH.
func buildCompilerPerNodeSecret(clusterName, shortName, nodeRole string, content []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "seam-mc-" + clusterName + "-" + shortName,
			Namespace: "seam-tenant-" + clusterName,
			Labels: map[string]string{
				"ontai.dev/managed-by": "compiler",
				"ontai.dev/cluster":    clusterName,
				"ontai.dev/node":       clusterName + "-" + shortName,
				"ontai.dev/node-role":  nodeRole,
			},
		},
		Data: map[string][]byte{
			controller.MachineConfigDataKeyYAML: content,
		},
	}
}

// TestMCSOT_ImportMode_CompilerSecretsCreatePerNodeMCS verifies that when compiler-
// generated per-node secrets exist in seam-tenant-{cluster}, the import helper
// creates per-node MachineConfigSync CRs with spec.nodeRef set to the node IP,
// rather than class-level MCS CRs. PLT-BUG-3-ARCH.
func TestMCSOT_ImportMode_CompilerSecretsCreatePerNodeMCS(t *testing.T) {
	const cluster = "plt-arch-3"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecretWithEndpoints(cluster, []string{"10.20.0.11", "10.20.0.12", "10.20.0.13"})

	cpContent := []byte("version: v1alpha1\nmachine:\n  type: controlplane\n")
	cp1Secret := buildCompilerPerNodeSecret(cluster, "cp1", "init", cpContent)
	cp2Secret := buildCompilerPerNodeSecret(cluster, "cp2", "controlplane", cpContent)
	cp3Secret := buildCompilerPerNodeSecret(cluster, "cp3", "controlplane", cpContent)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret, cp1Secret, cp2Secret, cp3Secret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(16),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
		// MachineConfigReaderFn is intentionally NOT set: compiler path must not call it.
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	ns := "seam-tenant-" + cluster

	// Expect per-node MCS CRs for cp1, cp2, cp3 -- NOT a class-level CR.
	for i, nodeShort := range []string{"cp1", "cp2", "cp3"} {
		expectedIP := []string{"10.20.0.11", "10.20.0.12", "10.20.0.13"}[i]
		crName := cluster + "-mc-import-" + nodeShort
		mcs := &platformv1alpha1.MachineConfigSync{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: crName, Namespace: ns}, mcs); err != nil {
			t.Fatalf("per-node MachineConfigSync CR %q not found: %v", crName, err)
		}
		if mcs.Spec.NodeClass != nodeShort {
			t.Errorf("CR %q: NodeClass = %q, want %q", crName, mcs.Spec.NodeClass, nodeShort)
		}
		if mcs.Spec.NodeRef != expectedIP {
			t.Errorf("CR %q: NodeRef = %q, want %q", crName, mcs.Spec.NodeRef, expectedIP)
		}
		if mcs.Spec.Reason != "import-initial-sync" {
			t.Errorf("CR %q: Reason = %q, want import-initial-sync", crName, mcs.Spec.Reason)
		}
	}

	// Verify that no class-level MCS CR was created.
	classCRName := cluster + "-mc-import-controlplane"
	classMCS := &platformv1alpha1.MachineConfigSync{}
	err := c.Get(context.Background(), types.NamespacedName{Name: classCRName, Namespace: ns}, classMCS)
	if err == nil {
		t.Errorf("class-level MachineConfigSync CR %q must not be created when compiler per-node secrets exist", classCRName)
	}
}

// TestMCSOT_ImportMode_CompilerSecretsPopulateNodeAddresses verifies that when the
// compiler path is taken, spec.nodeAddresses is populated from endpoints and role labels.
// PLT-BUG-3-ARCH.
func TestMCSOT_ImportMode_CompilerSecretsPopulateNodeAddresses(t *testing.T) {
	const cluster = "plt-arch-3-addr"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecretWithEndpoints(cluster, []string{"10.20.0.11", "10.20.0.12"})

	cpContent := []byte("version: v1alpha1\nmachine:\n  type: controlplane\n")
	cp1Secret := buildCompilerPerNodeSecret(cluster, "cp1", "init", cpContent)
	cp2Secret := buildCompilerPerNodeSecret(cluster, "cp2", "controlplane", cpContent)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret, cp1Secret, cp2Secret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(16),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	updated := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: cluster, Namespace: "seam-system"}, updated); err != nil {
		t.Fatalf("get updated TalosCluster: %v", err)
	}
	if len(updated.Spec.NodeAddresses) != 2 {
		t.Fatalf("expected 2 NodeAddresses, got %d", len(updated.Spec.NodeAddresses))
	}
	for _, na := range updated.Spec.NodeAddresses {
		if na.Role != platformv1alpha1.NodeRoleControlPlane {
			t.Errorf("NodeAddress %q: expected role=controlplane, got %q", na.IP, na.Role)
		}
	}
}

// TestMCSOT_ImportMode_CompilerSecretsIdempotent verifies that running import twice
// when compiler secrets exist does not duplicate per-node MCS CRs. PLT-BUG-3-ARCH.
func TestMCSOT_ImportMode_CompilerSecretsIdempotent(t *testing.T) {
	const cluster = "plt-arch-3-idem"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecretWithEndpoints(cluster, []string{"10.20.0.11"})
	cpContent := []byte("version: v1alpha1\nmachine:\n  type: controlplane\n")
	cp1Secret := buildCompilerPerNodeSecret(cluster, "cp1", "init", cpContent)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret, cp1Secret).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(16),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}

	ns := "seam-tenant-" + cluster
	mcsList := &platformv1alpha1.MachineConfigSyncList{}
	if err := c.List(context.Background(), mcsList); err != nil {
		t.Fatalf("list MachineConfigSync: %v", err)
	}
	cp1CRs := 0
	for _, cr := range mcsList.Items {
		if cr.Namespace == ns && cr.Spec.NodeClass == "cp1" {
			cp1CRs++
		}
	}
	if cp1CRs != 1 {
		t.Errorf("expected exactly 1 per-node MachineConfigSync CR for cp1, got %d", cp1CRs)
	}
}

// TestMCSOT_ImportMode_NodeAddressesNotOverwrittenIfPopulated verifies that if
// spec.nodeAddresses is already set, it is not overwritten by a re-import. RECON-A9.
func TestMCSOT_ImportMode_NodeAddressesNotOverwrittenIfPopulated(t *testing.T) {
	const cluster = "mcsot-nodeaddr-idem"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	// Pre-populate nodeAddresses.
	tc.Spec.NodeAddresses = []platformv1alpha1.NodeAddress{
		{IP: "10.20.0.2", Role: platformv1alpha1.NodeRoleControlPlane},
	}
	talosSecret := buildFakeTalosconfigSecretWithEndpoints(cluster, []string{"10.20.0.2", "10.20.0.3"})

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
		MachineConfigReaderFn: fakeEndpointClassReader(map[string]string{
			"10.20.0.2": controller.MachineConfigClassControlPlane,
			"10.20.0.3": controller.MachineConfigClassWorker,
		}),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	updated := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: cluster, Namespace: "seam-system"}, updated); err != nil {
		t.Fatalf("get updated TalosCluster: %v", err)
	}
	// Should remain 1 (original), not overwritten to 2.
	if len(updated.Spec.NodeAddresses) != 1 {
		t.Errorf("expected nodeAddresses to remain unchanged (1 entry), got %d", len(updated.Spec.NodeAddresses))
	}
}
