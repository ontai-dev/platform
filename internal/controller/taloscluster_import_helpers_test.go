package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	seamplatformv1alpha1 "github.com/ontai-dev/platform/api/seam/v1alpha1"
)

// TestImportSecretsNamespace verifies the naming convention for tenant namespaces.
func TestImportSecretsNamespace(t *testing.T) {
	cases := []struct {
		cluster string
		want    string
	}{
		{"ccs-dev", "seam-tenant-ccs-dev"},
		{"ccs-mgmt", "seam-tenant-ccs-mgmt"},
		{"my-cluster", "seam-tenant-my-cluster"},
	}
	for _, c := range cases {
		got := importSecretsNamespace(c.cluster)
		if got != c.want {
			t.Errorf("importSecretsNamespace(%q) = %q, want %q", c.cluster, got, c.want)
		}
	}
}

// TestTalosconfigSecretName verifies the talosconfig Secret naming convention.
func TestTalosconfigSecretName(t *testing.T) {
	cases := []struct {
		cluster string
		want    string
	}{
		{"ccs-dev", "seam-mc-ccs-dev-talosconfig"},
		{"ccs-mgmt", "seam-mc-ccs-mgmt-talosconfig"},
	}
	for _, c := range cases {
		got := talosconfigSecretName(c.cluster)
		if got != c.want {
			t.Errorf("talosconfigSecretName(%q) = %q, want %q", c.cluster, got, c.want)
		}
	}
}

// TestKubeconfigSecretName verifies the kubeconfig Secret naming convention.
func TestKubeconfigSecretName(t *testing.T) {
	cases := []struct {
		cluster string
		want    string
	}{
		{"ccs-dev", "seam-mc-ccs-dev-kubeconfig"},
		{"ccs-mgmt", "seam-mc-ccs-mgmt-kubeconfig"},
	}
	for _, c := range cases {
		got := kubeconfigSecretName(c.cluster)
		if got != c.want {
			t.Errorf("kubeconfigSecretName(%q) = %q, want %q", c.cluster, got, c.want)
		}
	}
}

// TestPruneImportMachineConfigSyncCRs_DeletesWhenSyncReady verifies that import CRs
// are deleted once their corresponding sync CR is READY=True.
func TestPruneImportMachineConfigSyncCRs_DeletesWhenSyncReady(t *testing.T) {
	ns := "seam-tenant-ccs-mgmt"
	tc := &seamplatformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-mgmt", Namespace: "seam-system"},
	}
	importCR := &platformv1alpha1.MachineConfigSync{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-mgmt-mc-import-cp1", Namespace: ns},
		Spec: platformv1alpha1.MachineConfigSyncSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			NodeClass:  "cp1",
		},
	}
	syncCR := &platformv1alpha1.MachineConfigSync{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-mgmt-mc-sync-cp1", Namespace: ns},
		Spec: platformv1alpha1.MachineConfigSyncSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			NodeClass:  "cp1",
		},
		Status: platformv1alpha1.MachineConfigSyncStatus{
			Conditions: []metav1.Condition{
				{Type: "Ready", Status: "True", Reason: "Synced"},
			},
		},
	}

	scheme := buildHelperTestScheme(t)
	c := buildRosterTestScheme(t).WithScheme(scheme).
		WithObjects(tc, importCR).
		WithStatusSubresource(syncCR).
		WithObjects(syncCR).
		Build()

	r := buildRosterReconciler(t, c)
	if err := r.pruneImportMachineConfigSyncCRs(context.Background(), tc); err != nil {
		t.Fatalf("pruneImportMachineConfigSyncCRs: %v", err)
	}

	// Import CR should be gone.
	mcs := &platformv1alpha1.MachineConfigSync{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "ccs-mgmt-mc-import-cp1", Namespace: ns}, mcs); err == nil {
		t.Error("import CR still exists after pruning; expected deletion")
	}
}

// TestPruneImportMachineConfigSyncCRs_KeepsWhenSyncNotReady verifies that import CRs
// are kept when the sync CR exists but is not yet READY=True.
func TestPruneImportMachineConfigSyncCRs_KeepsWhenSyncNotReady(t *testing.T) {
	ns := "seam-tenant-ccs-mgmt"
	tc := &seamplatformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-mgmt", Namespace: "seam-system"},
	}
	importCR := &platformv1alpha1.MachineConfigSync{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-mgmt-mc-import-cp1", Namespace: ns},
		Spec: platformv1alpha1.MachineConfigSyncSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			NodeClass:  "cp1",
		},
	}
	syncCR := &platformv1alpha1.MachineConfigSync{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-mgmt-mc-sync-cp1", Namespace: ns},
		Spec: platformv1alpha1.MachineConfigSyncSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt"},
			NodeClass:  "cp1",
		},
		Status: platformv1alpha1.MachineConfigSyncStatus{
			Conditions: []metav1.Condition{
				{Type: "Ready", Status: "False", Reason: "Pending"},
			},
		},
	}

	scheme := buildHelperTestScheme(t)
	c := buildRosterTestScheme(t).WithScheme(scheme).
		WithObjects(tc, importCR).
		WithStatusSubresource(syncCR).
		WithObjects(syncCR).
		Build()

	r := buildRosterReconciler(t, c)
	if err := r.pruneImportMachineConfigSyncCRs(context.Background(), tc); err != nil {
		t.Fatalf("pruneImportMachineConfigSyncCRs: %v", err)
	}

	// Import CR should still exist.
	mcs := &platformv1alpha1.MachineConfigSync{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "ccs-mgmt-mc-import-cp1", Namespace: ns}, mcs); err != nil {
		t.Errorf("import CR was deleted but sync CR is not READY: %v", err)
	}
}
