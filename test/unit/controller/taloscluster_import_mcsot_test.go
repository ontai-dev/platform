// Package controller_test -- MachineConfig CR-based import path and sync detection tests.
//
// Tests cover:
//   - ensureMachineConfigCRsExist: creates per-node MachineConfigSync CRs for each
//     admin/compiler-provided MachineConfig CR in the cluster's tenant namespace.
//   - reconcileMachineConfigSync: detects MachineConfig CR generation changes and
//     creates or replaces watch-triggered MachineConfigSync CRs.
//
// All tests use the fake client. No live Kubernetes API or Talos API is required.
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

// buildMachineConfigCR creates a MachineConfig CR for the given cluster and node.
func buildMachineConfigCR(clusterName, hostname, nodeIP string, order int32, generation int64) *platformv1alpha1.MachineConfig {
	return &platformv1alpha1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "seam-mc-" + clusterName + "-" + hostname,
			Namespace:  "seam-tenant-" + clusterName,
			Generation: generation,
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

// buildWatchSyncCR creates a pre-existing watch-triggered MachineConfigSync CR with
// the given MC generation annotation.
func buildWatchSyncCR(clusterName, hostname, ns string, mcGeneration int64) *platformv1alpha1.MachineConfigSync {
	return &platformv1alpha1.MachineConfigSync{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName + "-mc-sync-" + hostname,
			Namespace: ns,
			Annotations: map[string]string{
				controller.AnnotationMCGeneration: fmt.Sprintf("%d", mcGeneration),
			},
		},
		Spec: platformv1alpha1.MachineConfigSyncSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: clusterName},
			NodeClass:  hostname,
			Reason:     "mc-generation-changed",
		},
	}
}

// --- ensureMachineConfigCRsExist tests ---

// TestMCSOT_MachineConfigCRsExist_CreatesMCSCRPerNode verifies that when MachineConfig CRs
// exist in the tenant namespace, ensureMachineConfigCRsExist creates one per-node
// MachineConfigSync CR per node with the correct NodeRef and Reason.
func TestMCSOT_MachineConfigCRsExist_CreatesMCSCRPerNode(t *testing.T) {
	const cluster = "mcsot-creates"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecret(cluster)
	mc1 := buildMachineConfigCR(cluster, "cp1", "10.20.0.11", 0, 1)
	mc2 := buildMachineConfigCR(cluster, "cp2", "10.20.0.12", 1, 1)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret, mc1, mc2).
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

	ns := "seam-tenant-" + cluster
	for _, tc2 := range []struct {
		hostname string
		nodeIP   string
	}{
		{"cp1", "10.20.0.11"},
		{"cp2", "10.20.0.12"},
	} {
		crName := cluster + "-mc-import-" + tc2.hostname
		mcs := &platformv1alpha1.MachineConfigSync{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: crName, Namespace: ns}, mcs); err != nil {
			t.Errorf("MachineConfigSync CR %q not found: %v", crName, err)
			continue
		}
		if mcs.Spec.NodeRef != tc2.nodeIP {
			t.Errorf("MachineConfigSync %q: NodeRef = %q, want %q", crName, mcs.Spec.NodeRef, tc2.nodeIP)
		}
		if mcs.Spec.Reason != "import-initial-sync" {
			t.Errorf("MachineConfigSync %q: Reason = %q, want import-initial-sync", crName, mcs.Spec.Reason)
		}
	}
}

// TestMCSOT_MachineConfigCRsExist_NoOpWhenNoCRs verifies that when no MachineConfig CRs
// exist, the reconcile completes without error and no MachineConfigSync CRs are created.
func TestMCSOT_MachineConfigCRsExist_NoOpWhenNoCRs(t *testing.T) {
	const cluster = "mcsot-no-crs"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecret(cluster)

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
		NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("Reconcile must not return error when no MachineConfig CRs exist: %v", err)
	}

	mcsList := &platformv1alpha1.MachineConfigSyncList{}
	if err := c.List(context.Background(), mcsList); err != nil {
		t.Fatalf("list MachineConfigSync: %v", err)
	}
	if len(mcsList.Items) != 0 {
		t.Errorf("expected no MachineConfigSync CRs when no MachineConfig CRs exist, got %d", len(mcsList.Items))
	}
}

// TestMCSOT_MachineConfigCRsExist_Idempotent verifies that reconciling twice with the
// same MachineConfig CRs does not duplicate MachineConfigSync CRs.
func TestMCSOT_MachineConfigCRsExist_Idempotent(t *testing.T) {
	const cluster = "mcsot-idem-cr"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecret(cluster)
	mc1 := buildMachineConfigCR(cluster, "cp1", "10.20.0.11", 0, 1)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret, mc1).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:                c,
		Scheme:                scheme,
		Recorder:              clientevents.NewFakeRecorder(16),
		KubeconfigGeneratorFn: fakeKubeconfigGenerator,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cluster, Namespace: "seam-system"}}
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("Reconcile %d: %v", i+1, err)
		}
	}

	ns := "seam-tenant-" + cluster
	// Only count the import CR (ensureMachineConfigCRsExist creates {cluster}-mc-import-{hostname}).
	// reconcileMachineConfigSync separately creates {cluster}-mc-sync-{hostname}.
	importCRName := cluster + "-mc-import-cp1"
	mcsList := &platformv1alpha1.MachineConfigSyncList{}
	if err := c.List(context.Background(), mcsList); err != nil {
		t.Fatalf("list MachineConfigSync: %v", err)
	}
	importCRCount := 0
	for _, cr := range mcsList.Items {
		if cr.Namespace == ns && cr.Name == importCRName {
			importCRCount++
		}
	}
	if importCRCount != 1 {
		t.Errorf("expected exactly 1 import MachineConfigSync CR %q, got %d", importCRName, importCRCount)
	}
}

// --- reconcileMachineConfigSync tests ---

// TestMCSOT_MCGeneration_NoCRYetCreatesOne verifies that when a MachineConfig CR exists
// but no watch-triggered MachineConfigSync CR does, reconcileMachineConfigSync creates one
// with the correct generation annotation and reason.
func TestMCSOT_MCGeneration_NoCRYetCreatesOne(t *testing.T) {
	const cluster = "mcsot-gen-new"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecret(cluster)
	mc := buildMachineConfigCR(cluster, "cp1", "10.20.0.11", 0, 1)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret, mc).
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

	ns := "seam-tenant-" + cluster
	crName := cluster + "-mc-sync-cp1"
	mcs := &platformv1alpha1.MachineConfigSync{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: crName, Namespace: ns}, mcs); err != nil {
		t.Fatalf("watch-triggered MachineConfigSync CR not found: %v", err)
	}
	if mcs.Annotations[controller.AnnotationMCGeneration] != "1" {
		t.Errorf("AnnotationMCGeneration = %q, want \"1\"", mcs.Annotations[controller.AnnotationMCGeneration])
	}
	if mcs.Spec.Reason != "mc-generation-changed" {
		t.Errorf("Reason = %q, want mc-generation-changed", mcs.Spec.Reason)
	}
	if mcs.Spec.NodeRef != "10.20.0.11" {
		t.Errorf("NodeRef = %q, want 10.20.0.11", mcs.Spec.NodeRef)
	}
}

// TestMCSOT_MCGeneration_SameGenerationNoOp verifies that when a watch-triggered
// MachineConfigSync CR already exists with a matching generation annotation,
// reconcileMachineConfigSync does not create a duplicate.
func TestMCSOT_MCGeneration_SameGenerationNoOp(t *testing.T) {
	const cluster = "mcsot-gen-same"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecret(cluster)
	mc := buildMachineConfigCR(cluster, "cp1", "10.20.0.11", 0, 1)

	ns := "seam-tenant-" + cluster
	existingSync := buildWatchSyncCR(cluster, "cp1", ns, 1)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret, mc, existingSync).
		WithStatusSubresource(tc, existingSync).
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

	// Count watch-triggered sync CRs for cp1 -- should still be exactly 1.
	mcsList := &platformv1alpha1.MachineConfigSyncList{}
	if err := c.List(context.Background(), mcsList); err != nil {
		t.Fatalf("list MachineConfigSync: %v", err)
	}
	cp1SyncCRs := 0
	for _, cr := range mcsList.Items {
		if cr.Namespace == ns && cr.Name == cluster+"-mc-sync-cp1" {
			cp1SyncCRs++
		}
	}
	if cp1SyncCRs != 1 {
		t.Errorf("expected exactly 1 watch-triggered sync CR when generation unchanged, got %d", cp1SyncCRs)
	}
}

// TestMCSOT_MCGeneration_StaleReplacedOnGenerationChange verifies that when a
// watch-triggered MachineConfigSync CR exists with an outdated generation annotation,
// it is deleted and replaced with a fresh one carrying the current generation.
func TestMCSOT_MCGeneration_StaleReplacedOnGenerationChange(t *testing.T) {
	const cluster = "mcsot-gen-stale"
	scheme := buildDay2Scheme(t)
	tc := buildImportTalosCluster(cluster, "seam-system")
	talosSecret := buildFakeTalosconfigSecret(cluster)
	// MC is at generation 2.
	mc := buildMachineConfigCR(cluster, "cp1", "10.20.0.11", 0, 2)

	ns := "seam-tenant-" + cluster
	// Pre-existing sync CR targets generation 1 (stale).
	staleCR := buildWatchSyncCR(cluster, "cp1", ns, 1)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, talosSecret, mc, staleCR).
		WithStatusSubresource(tc, staleCR).
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

	crName := cluster + "-mc-sync-cp1"
	freshCR := &platformv1alpha1.MachineConfigSync{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: crName, Namespace: ns}, freshCR); err != nil {
		t.Fatalf("fresh MachineConfigSync CR not found: %v", err)
	}
	if freshCR.Annotations[controller.AnnotationMCGeneration] != "2" {
		t.Errorf("fresh CR AnnotationMCGeneration = %q, want \"2\"",
			freshCR.Annotations[controller.AnnotationMCGeneration])
	}
}
