// Package controller_test -- RECON-A2 unit tests for ensureMachineConfigSecrets.
//
// These tests verify the machineconfig source-of-truth (MCSOT) import path: reading
// machineconfigs from Talos nodes, classifying them by machine.type, creating Secret
// and MachineConfigSync CRs. All tests inject MachineConfigReaderFn to bypass the
// real talos goclient.
//
// RECON-A2: Import flow -- create source-of-truth Secrets after kubeconfig.
package controller_test

import (
	"context"
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
