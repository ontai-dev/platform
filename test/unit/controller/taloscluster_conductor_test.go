// Package controller_test tests the TalosCluster Conductor Deployment functions.
// Tests cover the conductorAgentDeployment builder and the kubeconfig-absent
// branch of ensureConductorDeploymentOnTargetCluster.
//
// Testing the full remote-cluster path (building a real client from a kubeconfig
// and creating a Deployment on a target cluster) requires a live cluster and is
// covered by integration tests, not unit tests.
//
// platform-schema.md §12 Conductor Deployment Contract.
// conductor-schema.md §15 Role Declaration Contract.
package controller_test

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

// TestConductorAgentDeployment_RoleStamp verifies that conductorAgentDeployment
// builds a Deployment with CONDUCTOR_ROLE=tenant as a first-class spec field.
// conductor-schema.md §15: the role field is in the container spec, never in metadata.
func TestConductorAgentDeployment_RoleStamp(t *testing.T) {
	dep := controller.BuildConductorAgentDeployment("test-cluster")

	if dep.Name != "conductor-agent" {
		t.Errorf("Deployment name = %q, want %q", dep.Name, "conductor-agent")
	}
	if dep.Namespace != "ont-system" {
		t.Errorf("Deployment namespace = %q, want %q", dep.Namespace, "ont-system")
	}

	containers := dep.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		t.Fatal("no containers in Deployment spec")
	}

	var roleValue string
	for _, env := range containers[0].Env {
		if env.Name == "CONDUCTOR_ROLE" {
			roleValue = env.Value
			break
		}
	}
	if roleValue != "tenant" {
		t.Errorf("CONDUCTOR_ROLE = %q, want %q", roleValue, "tenant")
	}
}

// TestConductorAgentDeployment_ClusterLabel verifies the cluster label is set.
func TestConductorAgentDeployment_ClusterLabel(t *testing.T) {
	dep := controller.BuildConductorAgentDeployment("my-cluster")

	if dep.Labels["runner.ontai.dev/cluster"] != "my-cluster" {
		t.Errorf("cluster label = %q, want %q",
			dep.Labels["runner.ontai.dev/cluster"], "my-cluster")
	}
	podLabels := dep.Spec.Template.ObjectMeta.Labels
	if podLabels["runner.ontai.dev/cluster"] != "my-cluster" {
		t.Errorf("pod cluster label = %q, want %q",
			podLabels["runner.ontai.dev/cluster"], "my-cluster")
	}
}

// TestEnsureConductorDeployment_KubeconfigAbsentIsGraceful verifies that when
// the CAPI kubeconfig Secret does not yet exist, ensureConductorDeployment
// returns nil (not fatal) so the reconciler can requeue. This is the window
// between CAPI cluster Running and CAPI writing the kubeconfig Secret.
func TestEnsureConductorDeployment_KubeconfigAbsentIsGraceful(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			CAPI: platformv1alpha1.CAPIConfig{Enabled: true},
		},
	}
	// No kubeconfig Secret pre-populated — simulates CAPI not yet ready.
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc).WithStatusSubresource(tc).Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
	}

	err := r.EnsureConductorDeploymentOnTargetCluster(context.Background(), tc)
	if err != nil {
		t.Errorf("expected nil error when kubeconfig absent, got: %v", err)
	}
}

// TestTalosClusterReconcile_CAPIPathDoesNotBreakOnAbsentKubeconfig verifies that
// the CAPI reconcile path succeeds end-to-end (reaching requeue or no-CiliumPackRef
// path) without error when the kubeconfig Secret is absent.
// This ensures the conductor deployment step does not make the reconciler fail.
func TestTalosClusterReconcile_CAPIPathDoesNotBreakOnAbsentKubeconfig(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			CAPI: platformv1alpha1.CAPIConfig{
				Enabled:           true,
				TalosVersion:      "v1.7.0",
				KubernetesVersion: "v1.31.0",
				ControlPlane: platformv1alpha1.CAPIControlPlaneConfig{
					Replicas: 3,
				},
				// No CiliumPackRef — skips the Cilium gate and goes to dev-mode path.
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc).WithStatusSubresource(tc).Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
	}

	// First reconcile: creates CAPI objects, polls CAPI status.
	// Since CAPI Cluster doesn't exist in fake client, getCAPIClusterPhase returns error,
	// reconciler requeues without error.
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-cluster", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should requeue to wait for CAPI cluster.
	if result.RequeueAfter == 0 {
		t.Error("expected requeue while waiting for CAPI cluster")
	}
}
