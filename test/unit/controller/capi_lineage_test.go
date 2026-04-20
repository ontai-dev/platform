// Package controller_test -- CAPI derived lineage label unit tests.
//
// Tests that SetDescendantLabels is called on all four CAPI objects created by
// reconcileCAPIPath. The DescendantReconciler in seam-core reads these labels to
// append DescendantEntry records to the TalosCluster InfrastructureLineageIndex.
// PLATFORM-BL-CAPI-DERIVED-LINEAGE.
package controller_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
	"github.com/ontai-dev/seam-core/pkg/lineage"
)

// capiTCForLineage returns a minimal TalosCluster with CAPI enabled.
func capiTCForLineage(name string) *platformv1alpha1.TalosCluster {
	return &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			CAPI: &platformv1alpha1.CAPIConfig{
				Enabled:           true,
				TalosVersion:      "v1.7.0",
				KubernetesVersion: "v1.31.0",
				ControlPlane:      &platformv1alpha1.CAPIControlPlaneConfig{Replicas: 3},
			},
		},
	}
}

// assertLineageLabels fails the test if the given unstructured object does not
// carry the expected descendant lineage labels for the named TalosCluster.
func assertLineageLabels(t *testing.T, obj *unstructured.Unstructured, clusterName string) {
	t.Helper()
	labels := obj.GetLabels()
	wantILI := lineage.IndexName("TalosCluster", clusterName)
	if got := labels["infrastructure.ontai.dev/root-ili"]; got != wantILI {
		t.Errorf("root-ili label: got %q want %q", got, wantILI)
	}
	if got := labels["infrastructure.ontai.dev/root-ili-namespace"]; got != "seam-system" {
		t.Errorf("root-ili-namespace label: got %q want %q", got, "seam-system")
	}
	if got := labels["infrastructure.ontai.dev/seam-operator"]; got != "platform" {
		t.Errorf("seam-operator label: got %q want %q", got, "platform")
	}
	if got := labels["infrastructure.ontai.dev/creation-rationale"]; got != string(lineage.ClusterProvision) {
		t.Errorf("creation-rationale label: got %q want %q", got, lineage.ClusterProvision)
	}
}

// TestCAPILineage_SeamInfrastructureCluster verifies that the SeamInfrastructureCluster
// created by ensureSeamInfrastructureCluster carries the four descendant lineage labels.
func TestCAPILineage_SeamInfrastructureCluster(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := capiTCForLineage("ccs-dev")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: fakeRecorder(),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "infrastructure.cluster.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "SeamInfrastructureCluster",
	})
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-dev", Namespace: "seam-tenant-ccs-dev",
	}, obj); err != nil {
		t.Fatalf("get SeamInfrastructureCluster: %v", err)
	}
	assertLineageLabels(t, obj, "ccs-dev")
}

// TestCAPILineage_CAPICluster verifies that the CAPI Cluster carries the four
// descendant lineage labels pointing to the TalosCluster ILI.
func TestCAPILineage_CAPICluster(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := capiTCForLineage("ccs-dev")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: fakeRecorder(),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "Cluster",
	})
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-dev", Namespace: "seam-tenant-ccs-dev",
	}, obj); err != nil {
		t.Fatalf("get CAPI Cluster: %v", err)
	}
	assertLineageLabels(t, obj, "ccs-dev")
}

// TestCAPILineage_TalosControlPlane verifies that the TalosControlPlane carries
// the four descendant lineage labels pointing to the TalosCluster ILI.
func TestCAPILineage_TalosControlPlane(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := capiTCForLineage("ccs-dev")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: fakeRecorder(),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "controlplane.cluster.x-k8s.io",
		Version: "v1alpha3",
		Kind:    "TalosControlPlane",
	})
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-dev-control-plane", Namespace: "seam-tenant-ccs-dev",
	}, obj); err != nil {
		t.Fatalf("get TalosControlPlane: %v", err)
	}
	assertLineageLabels(t, obj, "ccs-dev")
}

// TestCAPILineage_MachineDeployment verifies that a MachineDeployment created for a
// worker pool carries the four descendant lineage labels pointing to the TalosCluster ILI.
func TestCAPILineage_MachineDeployment(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-dev", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			CAPI: &platformv1alpha1.CAPIConfig{
				Enabled:           true,
				TalosVersion:      "v1.7.0",
				KubernetesVersion: "v1.31.0",
				ControlPlane:      &platformv1alpha1.CAPIControlPlaneConfig{Replicas: 1},
				Workers: []platformv1alpha1.CAPIWorkerPool{
					{Name: "workers", Replicas: 2},
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: fakeRecorder(),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "MachineDeployment",
	})
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-dev-workers", Namespace: "seam-tenant-ccs-dev",
	}, obj); err != nil {
		t.Fatalf("get MachineDeployment: %v", err)
	}
	assertLineageLabels(t, obj, "ccs-dev")
}
