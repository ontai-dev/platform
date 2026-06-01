package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

// buildRosterTestScheme builds a fake client builder with the helper test scheme.
func buildRosterTestScheme(t *testing.T) *fake.ClientBuilder {
	t.Helper()
	scheme := buildHelperTestScheme(t)
	return fake.NewClientBuilder().WithScheme(scheme)
}

// buildRosterReconciler builds a TalosClusterReconciler for roster tests.
func buildRosterReconciler(t *testing.T, c client.Client) *TalosClusterReconciler {
	t.Helper()
	return &TalosClusterReconciler{
		Client:   c,
		Scheme:   buildHelperTestScheme(t),
		Recorder: clientevents.NewFakeRecorder(8),
	}
}

// buildRosterMachineConfigCR creates a MachineConfig CR for roster tests.
func buildRosterMachineConfigCR(clusterName, hostname, nodeIP string, order int32) *platformv1alpha1.MachineConfig {
	return &platformv1alpha1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "seam-mc-" + clusterName + "-" + hostname,
			Namespace:       importSecretsNamespace(clusterName),
			Generation:      1,
			ResourceVersion: "1",
		},
		Spec: platformv1alpha1.MachineConfigSpec{
			Role:         platformv1alpha1.MachineConfigRoleControlPlane,
			Order:        order,
			NodeIP:       nodeIP,
			NodeHostname: hostname,
			ClusterRef:   corev1.LocalObjectReference{Name: clusterName},
		},
	}
}

// TestReconcileNodeRosterRefresh_NoAnnotation verifies that the function is a no-op
// when the refresh annotation is absent. RECON-C9.
func TestReconcileNodeRosterRefresh_NoAnnotation(t *testing.T) {
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "ccs-dev",
			Namespace:       "seam-system",
			ResourceVersion: "1",
		},
	}
	c := buildRosterTestScheme(t).WithObjects(tc).
		WithStatusSubresource(&platformv1alpha1.TalosCluster{}).Build()
	r := buildRosterReconciler(t, c)

	if err := r.reconcileNodeRosterRefresh(context.Background(), tc); err != nil {
		t.Errorf("expected no error for missing annotation, got %v", err)
	}
}

// TestReconcileNodeRosterRefresh_AnnotationFalse verifies that a false annotation
// value does not trigger a refresh. RECON-C9.
func TestReconcileNodeRosterRefresh_AnnotationFalse(t *testing.T) {
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ccs-dev",
			Namespace: "seam-system",
			Annotations: map[string]string{
				AnnotationRefreshNodeRoster: "false",
			},
			ResourceVersion: "1",
		},
	}
	c := buildRosterTestScheme(t).WithObjects(tc).
		WithStatusSubresource(&platformv1alpha1.TalosCluster{}).Build()
	r := buildRosterReconciler(t, c)

	if err := r.reconcileNodeRosterRefresh(context.Background(), tc); err != nil {
		t.Errorf("expected no error for false annotation, got %v", err)
	}
}

// TestReconcileNodeRosterRefresh_CreatesMCSCRForMachineConfigCR verifies that
// when the refresh annotation is set and MachineConfig CRs exist, the roster
// refresh creates per-node MachineConfigSync CRs and clears the annotation. RECON-C9.
func TestReconcileNodeRosterRefresh_CreatesMCSCRForMachineConfigCR(t *testing.T) {
	const cluster = "ccs-dev"
	mc1 := buildRosterMachineConfigCR(cluster, "cp1", "10.20.0.11", 0)
	mc2 := buildRosterMachineConfigCR(cluster, "cp2", "10.20.0.12", 1)

	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster,
			Namespace: "seam-system",
			Annotations: map[string]string{
				AnnotationRefreshNodeRoster: "true",
			},
			ResourceVersion: "1",
		},
	}

	c := buildRosterTestScheme(t).
		WithObjects(tc, mc1, mc2).
		WithStatusSubresource(&platformv1alpha1.TalosCluster{}).Build()
	r := buildRosterReconciler(t, c)

	if err := r.reconcileNodeRosterRefresh(context.Background(), tc); err != nil {
		t.Fatalf("reconcileNodeRosterRefresh: %v", err)
	}

	ns := importSecretsNamespace(cluster)
	for _, hostname := range []string{"cp1", "cp2"} {
		crName := cluster + "-mc-import-" + hostname
		mcs := &platformv1alpha1.MachineConfigSync{}
		if err := c.Get(context.Background(), client.ObjectKey{Name: crName, Namespace: ns}, mcs); err != nil {
			t.Errorf("MachineConfigSync CR %q not found: %v", crName, err)
		}
	}

	// Annotation must be cleared.
	updated := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(tc), updated); err != nil {
		t.Fatalf("get updated TalosCluster: %v", err)
	}
	if updated.Annotations != nil && updated.Annotations[AnnotationRefreshNodeRoster] == "true" {
		t.Errorf("expected annotation cleared after refresh, still present")
	}
}

// TestReconcileNodeRosterRefresh_ClearsAnnotationNoMachineConfigCRs verifies the
// annotation is cleared even when no MachineConfig CRs exist. RECON-C9.
func TestReconcileNodeRosterRefresh_ClearsAnnotationNoMachineConfigCRs(t *testing.T) {
	const cluster = "ccs-dev"
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster,
			Namespace: "seam-system",
			Annotations: map[string]string{
				AnnotationRefreshNodeRoster: "true",
			},
			ResourceVersion: "1",
		},
	}

	c := buildRosterTestScheme(t).
		WithObjects(tc).
		WithStatusSubresource(&platformv1alpha1.TalosCluster{}).Build()
	r := buildRosterReconciler(t, c)

	if err := r.reconcileNodeRosterRefresh(context.Background(), tc); err != nil {
		t.Fatalf("reconcileNodeRosterRefresh: %v", err)
	}

	updated := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(tc), updated); err != nil {
		t.Fatalf("get updated TalosCluster: %v", err)
	}
	if updated.Annotations != nil && updated.Annotations[AnnotationRefreshNodeRoster] == "true" {
		t.Errorf("expected annotation cleared after refresh, still present")
	}
}

// TestMachineConfigSyncStatusDecommissioned_Value verifies the constant. RECON-C9.
func TestMachineConfigSyncStatusDecommissioned_Value(t *testing.T) {
	if MachineConfigSyncStatusDecommissioned != "decommissioned" {
		t.Errorf("expected %q, got %q", "decommissioned", MachineConfigSyncStatusDecommissioned)
	}
}
