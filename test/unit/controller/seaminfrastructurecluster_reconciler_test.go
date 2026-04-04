// Package controller_test contains unit tests for the Seam Infrastructure Provider
// reconcilers. These are pure unit tests — no envtest, no live Kubernetes API.
// All tests use controller-runtime's fake client.
package controller_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1alpha1 "github.com/ontai-dev/platform/api/infrastructure/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// buildSICScheme returns a runtime.Scheme with infrastructure types registered.
func buildSICScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	if err := infrav1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add infrav1alpha1 scheme: %v", err)
	}
	return s
}

// newSIC builds a minimal SeamInfrastructureCluster for testing.
func newSIC(name, namespace, host string, port int32) *infrav1alpha1.SeamInfrastructureCluster {
	return &infrav1alpha1.SeamInfrastructureCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: infrav1alpha1.SeamInfrastructureClusterSpec{
			ControlPlaneEndpoint: infrav1alpha1.APIEndpoint{
				Host: host,
				Port: port,
			},
		},
	}
}

// newSIM builds a SeamInfrastructureMachine for testing.
func newSIM(name, namespace string, role infrav1alpha1.NodeRole, ready bool) *infrav1alpha1.SeamInfrastructureMachine {
	return &infrav1alpha1.SeamInfrastructureMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: infrav1alpha1.SeamInfrastructureMachineSpec{
			Address:  "10.20.0.10",
			NodeRole: role,
			TalosConfigSecretRef: infrav1alpha1.SecretRef{
				Name:      "test-talosconfig",
				Namespace: "ont-system",
			},
		},
		Status: infrav1alpha1.SeamInfrastructureMachineStatus{
			Ready: ready,
		},
	}
}

// TestSICReconcile_LineageSyncedInitialized verifies that the first reconcile
// initializes LineageSynced=False/LineageControllerAbsent.
func TestSICReconcile_LineageSyncedInitialized(t *testing.T) {
	scheme := buildSICScheme(t)
	sic := newSIC("test-cluster", "tenant-test-cluster", "10.20.0.10", 6443)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sic).
		WithStatusSubresource(sic).
		Build()

	r := &controller.SeamInfrastructureClusterReconciler{
		Client: c,
		Scheme: scheme,
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-cluster", Namespace: "tenant-test-cluster"},
	})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	got := &infrav1alpha1.SeamInfrastructureCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "test-cluster",
		Namespace: "tenant-test-cluster",
	}, got); err != nil {
		t.Fatalf("get after reconcile: %v", err)
	}

	cond := infrav1alpha1.FindCondition(got.Status.Conditions, infrav1alpha1.ConditionTypeLineageSynced)
	if cond == nil {
		t.Fatal("LineageSynced condition not set")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("LineageSynced status = %s, want False", cond.Status)
	}
	if cond.Reason != infrav1alpha1.ReasonLineageControllerAbsent {
		t.Errorf("LineageSynced reason = %s, want %s", cond.Reason, infrav1alpha1.ReasonLineageControllerAbsent)
	}
}

// TestSICReconcile_NoControlPlaneMachines verifies that with no CP machines the
// reconciler sets ControlPlaneMachinesPending and requeues.
func TestSICReconcile_NoControlPlaneMachines(t *testing.T) {
	scheme := buildSICScheme(t)
	sic := newSIC("ccs-dev", "tenant-ccs-dev", "10.20.0.10", 6443)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sic).
		WithStatusSubresource(sic).
		Build()

	r := &controller.SeamInfrastructureClusterReconciler{
		Client: c,
		Scheme: scheme,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "tenant-ccs-dev"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue, got zero RequeueAfter")
	}

	got := &infrav1alpha1.SeamInfrastructureCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-dev", Namespace: "tenant-ccs-dev",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := infrav1alpha1.FindCondition(got.Status.Conditions, infrav1alpha1.ConditionTypeInfrastructureReady)
	if cond == nil {
		t.Fatal("InfrastructureReady condition not set")
	}
	if cond.Reason != infrav1alpha1.ReasonControlPlaneMachinesPending {
		t.Errorf("reason = %s, want %s", cond.Reason, infrav1alpha1.ReasonControlPlaneMachinesPending)
	}
	if got.Status.Ready {
		t.Error("cluster should not be ready yet")
	}
}

// TestSICReconcile_SomeNotReady verifies that with some CP machines not ready
// the reconciler sets ControlPlaneMachinesNotReady and requeues.
func TestSICReconcile_SomeNotReady(t *testing.T) {
	scheme := buildSICScheme(t)
	sic := newSIC("ccs-dev", "tenant-ccs-dev", "10.20.0.10", 6443)
	cp1 := newSIM("cp1", "tenant-ccs-dev", infrav1alpha1.NodeRoleControlPlane, true)
	cp2 := newSIM("cp2", "tenant-ccs-dev", infrav1alpha1.NodeRoleControlPlane, false) // not ready

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sic, cp1, cp2).
		WithStatusSubresource(sic, cp1, cp2).
		Build()

	r := &controller.SeamInfrastructureClusterReconciler{
		Client: c,
		Scheme: scheme,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "tenant-ccs-dev"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue")
	}

	got := &infrav1alpha1.SeamInfrastructureCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-dev", Namespace: "tenant-ccs-dev",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Ready {
		t.Error("cluster should not be ready when some CP machines are not ready")
	}
	cond := infrav1alpha1.FindCondition(got.Status.Conditions, infrav1alpha1.ConditionTypeInfrastructureReady)
	if cond == nil {
		t.Fatal("InfrastructureReady condition not set")
	}
	if cond.Reason != infrav1alpha1.ReasonControlPlaneMachinesNotReady {
		t.Errorf("reason = %s, want %s", cond.Reason, infrav1alpha1.ReasonControlPlaneMachinesNotReady)
	}
}

// TestSICReconcile_AllReady verifies that when all CP machines are ready, the
// cluster is marked ready and InfrastructureReady=True is set.
func TestSICReconcile_AllReady(t *testing.T) {
	scheme := buildSICScheme(t)
	sic := newSIC("ccs-dev", "tenant-ccs-dev", "10.20.0.10", 6443)
	cp1 := newSIM("cp1", "tenant-ccs-dev", infrav1alpha1.NodeRoleControlPlane, true)
	cp2 := newSIM("cp2", "tenant-ccs-dev", infrav1alpha1.NodeRoleControlPlane, true)
	cp3 := newSIM("cp3", "tenant-ccs-dev", infrav1alpha1.NodeRoleControlPlane, true)
	// Worker machines should not affect CP readiness check.
	worker1 := newSIM("w1", "tenant-ccs-dev", infrav1alpha1.NodeRoleWorker, false)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sic, cp1, cp2, cp3, worker1).
		WithStatusSubresource(sic, cp1, cp2, cp3, worker1).
		Build()

	r := &controller.SeamInfrastructureClusterReconciler{
		Client: c,
		Scheme: scheme,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "tenant-ccs-dev"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No requeue needed when all ready.
	if result.RequeueAfter != 0 {
		t.Errorf("unexpected requeue: %v", result.RequeueAfter)
	}

	got := &infrav1alpha1.SeamInfrastructureCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-dev", Namespace: "tenant-ccs-dev",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Status.Ready {
		t.Error("cluster should be ready when all CP machines are ready")
	}
	cond := infrav1alpha1.FindCondition(got.Status.Conditions, infrav1alpha1.ConditionTypeInfrastructureReady)
	if cond == nil {
		t.Fatal("InfrastructureReady condition not set")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("InfrastructureReady status = %s, want True", cond.Status)
	}
	if cond.Reason != infrav1alpha1.ReasonAllControlPlaneMachinesReady {
		t.Errorf("reason = %s, want %s", cond.Reason, infrav1alpha1.ReasonAllControlPlaneMachinesReady)
	}
}

// TestSICReconcile_AlreadyReadyIsIdempotent verifies that a cluster already marked
// ready returns without requeue.
func TestSICReconcile_AlreadyReadyIsIdempotent(t *testing.T) {
	scheme := buildSICScheme(t)
	sic := newSIC("ccs-dev", "tenant-ccs-dev", "10.20.0.10", 6443)
	sic.Status.Ready = true

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sic).
		WithStatusSubresource(sic).
		Build()

	r := &controller.SeamInfrastructureClusterReconciler{
		Client: c,
		Scheme: scheme,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "tenant-ccs-dev"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 || result.Requeue {
		t.Errorf("already-ready cluster should not requeue, got %+v", result)
	}
}
