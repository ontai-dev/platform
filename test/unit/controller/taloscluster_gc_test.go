// Package controller_test -- TalosCluster tenant namespace GC unit tests.
//
// Tests for PLATFORM-BL-TENANT-GC: the finalizer-based seam-tenant-{name} namespace
// deletion on TalosCluster deletion. Cross-namespace ownerReferences are not supported
// by the Kubernetes GC controller, so a finalizer is required for CAPI-enabled clusters.
package controller_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

const finalizerTenantNS = "platform.ontai.dev/tenant-namespace-cleanup"

// TestTenantGC_FinalizerAddedOnCAPIEnabled verifies that a CAPI-enabled TalosCluster
// receives the tenant-namespace-cleanup finalizer on the first reconcile.
func TestTenantGC_FinalizerAddedOnCAPIEnabled(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-dev", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			CAPI: &platformv1alpha1.CAPIConfig{
				Enabled:           true,
				TalosVersion:      "v1.7.0",
				KubernetesVersion: "v1.31.0",
				ControlPlane:      &platformv1alpha1.CAPIControlPlaneConfig{Replicas: 1},
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

	updated := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"}, updated); err != nil {
		t.Fatalf("get TalosCluster after reconcile: %v", err)
	}
	if !controllerutil.ContainsFinalizer(updated, finalizerTenantNS) {
		t.Errorf("expected finalizer %q on CAPI-enabled TalosCluster, got finalizers: %v",
			finalizerTenantNS, updated.Finalizers)
	}
}

// TestTenantGC_FinalizerNotAddedOnDirectPath verifies that the tenant-namespace-cleanup
// finalizer is NOT added to a TalosCluster with capi.enabled=false (direct bootstrap path).
func TestTenantGC_FinalizerNotAddedOnDirectPath(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-mgmt", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			Mode:         platformv1alpha1.TalosClusterModeImport,
			TalosVersion: "v1.7.0",
			Role:         platformv1alpha1.TalosClusterRoleManagement,
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
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	updated := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"}, updated); err != nil {
		t.Fatalf("get TalosCluster after reconcile: %v", err)
	}
	if controllerutil.ContainsFinalizer(updated, finalizerTenantNS) {
		t.Errorf("did not expect finalizer %q on direct-path TalosCluster", finalizerTenantNS)
	}
}

// TestTenantGC_NamespaceDeletedOnDeletion verifies that the seam-tenant-{name} namespace
// is deleted when a CAPI-enabled TalosCluster with the tenant-namespace-cleanup finalizer
// has its DeletionTimestamp set. PLATFORM-BL-TENANT-GC.
func TestTenantGC_NamespaceDeletedOnDeletion(t *testing.T) {
	scheme := buildDay2Scheme(t)

	now := metav1.Now()
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "ccs-dev",
			Namespace:         "seam-system",
			Generation:        1,
			DeletionTimestamp: &now,
			Finalizers:        []string{finalizerTenantNS},
		},
		Spec: platformv1alpha1.TalosClusterSpec{
			CAPI: &platformv1alpha1.CAPIConfig{
				Enabled:           true,
				TalosVersion:      "v1.7.0",
				KubernetesVersion: "v1.31.0",
			},
		},
	}
	tenantNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "seam-tenant-ccs-dev"},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, tenantNS).
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
		t.Fatalf("reconcile on deletion: %v", err)
	}

	ns := &corev1.Namespace{}
	err := c.Get(context.Background(), types.NamespacedName{Name: "seam-tenant-ccs-dev"}, ns)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected seam-tenant-ccs-dev to be deleted, but Get returned: %v", err)
	}
}

// TestTenantGC_IdempotentWhenNamespaceAlreadyGone verifies that the deletion handler
// is idempotent when the tenant namespace is already absent. The finalizer must still
// be removed so the TalosCluster can be garbage-collected. PLATFORM-BL-TENANT-GC.
func TestTenantGC_IdempotentWhenNamespaceAlreadyGone(t *testing.T) {
	scheme := buildDay2Scheme(t)

	now := metav1.Now()
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "ccs-dev",
			Namespace:         "seam-system",
			Generation:        1,
			DeletionTimestamp: &now,
			Finalizers:        []string{finalizerTenantNS},
		},
		Spec: platformv1alpha1.TalosClusterSpec{
			CAPI: &platformv1alpha1.CAPIConfig{
				Enabled:           true,
				TalosVersion:      "v1.7.0",
				KubernetesVersion: "v1.31.0",
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

	// When the fake client removes the last finalizer from an object with a
	// DeletionTimestamp, it garbage-collects the object. NotFound here means
	// the finalizer was removed successfully and the object was released.
	updated := &platformv1alpha1.TalosCluster{}
	err := c.Get(context.Background(), types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"}, updated)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return // object released — finalizer was removed
		}
		t.Fatalf("get TalosCluster after deletion reconcile: %v", err)
	}
	if controllerutil.ContainsFinalizer(updated, finalizerTenantNS) {
		t.Errorf("expected finalizer %q removed after namespace-already-gone deletion, got finalizers: %v",
			finalizerTenantNS, updated.Finalizers)
	}
}
