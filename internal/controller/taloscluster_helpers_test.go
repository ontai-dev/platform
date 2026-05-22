package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clientevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	seamplatformv1alpha1 "github.com/ontai-dev/platform/api/seam/v1alpha1"
)

// buildHelperTestScheme constructs a runtime.Scheme with all types required for
// taloscluster_helpers unit tests.
func buildHelperTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	// seamplatformv1alpha1 registers TalosCluster under seam.ontai.dev/v1alpha1.
	if err := seamplatformv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add seamplatformv1alpha1 scheme: %v", err)
	}
	// PackExecution and PackInstalled are owned by wrapper (seam.ontai.dev/v1alpha1).
	// Register as unstructured so the fake client can store/retrieve them.
	s.AddKnownTypeWithName(packExecutionTenantGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(
		packExecutionTenantGVK.GroupVersion().WithKind(packExecutionTenantGVK.Kind+"List"),
		&unstructured.UnstructuredList{},
	)
	s.AddKnownTypeWithName(packInstanceTenantGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(
		packInstanceTenantGVK.GroupVersion().WithKind(packInstanceTenantGVK.Kind+"List"),
		&unstructured.UnstructuredList{},
	)
	// guardian.ontai.dev types (RBACPolicy, RBACProfile) are not in seam-core;
	// register as unstructured so the fake client can list/patch them.
	s.AddKnownTypeWithName(rbacPolicyGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(
		rbacPolicyGVK.GroupVersion().WithKind(rbacPolicyGVK.Kind+"List"),
		&unstructured.UnstructuredList{},
	)
	s.AddKnownTypeWithName(rbacProfileGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(
		rbacProfileGVK.GroupVersion().WithKind(rbacProfileGVK.Kind+"List"),
		&unstructured.UnstructuredList{},
	)
	return s
}

// fakePackExecution builds a minimal PackExecution unstructured object.
func fakePackExecution(name, ns string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(packExecutionTenantGVK)
	obj.SetName(name)
	obj.SetNamespace(ns)
	obj.SetResourceVersion("1")
	return obj
}

// fakePackInstance builds a minimal PackInstalled unstructured object.
func fakePackInstance(name, ns string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(packInstanceTenantGVK)
	obj.SetName(name)
	obj.SetNamespace(ns)
	obj.SetResourceVersion("1")
	return obj
}

// fakeRBACPolicy builds a minimal guardian RBACPolicy unstructured object with
// the given allowedClusters list.
func fakeRBACPolicy(name, ns string, allowedClusters []string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(rbacPolicyGVK)
	obj.SetName(name)
	obj.SetNamespace(ns)
	obj.SetResourceVersion("1")
	clusters := make([]interface{}, len(allowedClusters))
	for i, c := range allowedClusters {
		clusters[i] = c
	}
	_ = unstructured.SetNestedSlice(obj.Object, clusters, "spec", "allowedClusters")
	return obj
}

// fakeTenantTalosCluster creates a role=tenant TalosCluster with the given finalizers.
// The fake client requires at least one finalizer when DeletionTimestamp is set;
// use fakeTenantTalosClusterPendingDelete if DeletionTimestamp is needed.
func fakeTenantTalosCluster(name string, finalizers []string) *platformv1alpha1.TalosCluster {
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       "seam-system",
			Finalizers:      finalizers,
			ResourceVersion: "1",
		},
		Spec: platformv1alpha1.TalosClusterSpec{
			Role: platformv1alpha1.TalosClusterRoleTenant,
			Mode: platformv1alpha1.TalosClusterModeImport,
		},
	}
	return tc
}

// setDeletionTimestamp patches the DeletionTimestamp onto tc in the fake client.
// The fake client refuses to create an object with DeletionTimestamp set; instead
// we simulate deletion by calling Delete and then re-fetching.
func setDeletionTimestamp(t *testing.T, c client.Client, tc *platformv1alpha1.TalosCluster) *platformv1alpha1.TalosCluster {
	t.Helper()
	if err := c.Delete(context.Background(), tc); err != nil {
		t.Fatalf("setDeletionTimestamp: Delete: %v", err)
	}
	latest := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: tc.Name, Namespace: tc.Namespace}, latest); err != nil {
		t.Fatalf("setDeletionTimestamp: Get after Delete: %v", err)
	}
	return latest
}

// TestHandleTalosClusterDeletion_DecisionHCascade_DeletesPackExecutions verifies
// that Step 0 of handleTalosClusterDeletion deletes PackExecutions and PackInstances
// in the tenant namespace and removes the finalizerDecisionHCascade. T-24.
func TestHandleTalosClusterDeletion_DecisionHCascade_DeletesPackExecutions(t *testing.T) {
	scheme := buildHelperTestScheme(t)
	clusterName := "ccs-dev"
	tenantNS := "seam-tenant-" + clusterName

	pe := fakePackExecution("nginx-pack-exec", tenantNS)
	pi := fakePackInstance("nginx-pack-inst", tenantNS)
	tc := fakeTenantTalosCluster(clusterName, []string{finalizerDecisionHCascade})

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, pe, pi).
		WithStatusSubresource(&platformv1alpha1.TalosCluster{}).
		Build()

	r := &TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: clientevents.NewFakeRecorder(8),
	}

	// Simulate deletion: set DeletionTimestamp by calling Delete (fake client sets it
	// when finalizers are present). Fetch the updated object.
	tc = setDeletionTimestamp(t, c, tc)

	result, err := r.handleTalosClusterDeletion(context.Background(), tc)
	if err != nil {
		t.Fatalf("handleTalosClusterDeletion: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("unexpected requeue: %+v", result)
	}

	// PackExecution must be deleted.
	peGet := &unstructured.Unstructured{}
	peGet.SetGroupVersionKind(packExecutionTenantGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: "nginx-pack-exec", Namespace: tenantNS}, peGet); err == nil {
		t.Error("expected PackExecution to be deleted but it still exists")
	}

	// PackInstalled must be deleted.
	piGet := &unstructured.Unstructured{}
	piGet.SetGroupVersionKind(packInstanceTenantGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: "nginx-pack-inst", Namespace: tenantNS}, piGet); err == nil {
		t.Error("expected PackInstalled to be deleted but it still exists")
	}

	// finalizerDecisionHCascade must be removed. The fake client GC's the object once
	// all finalizers are removed while DeletionTimestamp is set, so "not found" is
	// also an acceptable outcome.
	latest := &platformv1alpha1.TalosCluster{}
	err = c.Get(context.Background(), types.NamespacedName{Name: clusterName, Namespace: "seam-system"}, latest)
	if err == nil {
		// Object still exists -- finalizer must be absent.
		if controllerutil.ContainsFinalizer(latest, finalizerDecisionHCascade) {
			t.Error("expected finalizerDecisionHCascade to be removed after cascade")
		}
	}
	// If err != nil (NotFound), the object was GC'd by the fake client -- which
	// means all finalizers were removed successfully.
}

// TestHandleTalosClusterDeletion_DecisionHCascade_RemovesFromAllowedClusters verifies
// that Step 0d removes the cluster name from seam-platform-rbac-policy.spec.allowedClusters.
// T-24.
func TestHandleTalosClusterDeletion_DecisionHCascade_RemovesFromAllowedClusters(t *testing.T) {
	scheme := buildHelperTestScheme(t)
	clusterName := "ccs-dev"

	rbacPolicy := fakeRBACPolicy("seam-platform-rbac-policy", rbacPolicyNamespace, []string{"ccs-dev", "other-cluster"})
	tc := fakeTenantTalosCluster(clusterName, []string{finalizerDecisionHCascade})

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, rbacPolicy).
		WithStatusSubresource(&platformv1alpha1.TalosCluster{}).
		Build()

	r := &TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: clientevents.NewFakeRecorder(8),
	}

	tc = setDeletionTimestamp(t, c, tc)

	_, err := r.handleTalosClusterDeletion(context.Background(), tc)
	if err != nil {
		t.Fatalf("handleTalosClusterDeletion: %v", err)
	}

	// allowedClusters must no longer contain "ccs-dev".
	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(rbacPolicyGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: "seam-platform-rbac-policy", Namespace: rbacPolicyNamespace}, updated); err != nil {
		t.Fatalf("get RBACPolicy: %v", err)
	}
	raw, _, _ := unstructured.NestedStringSlice(updated.Object, "spec", "allowedClusters")
	for _, v := range raw {
		if v == clusterName {
			t.Errorf("expected %q to be removed from allowedClusters, but it is still present: %v", clusterName, raw)
		}
	}
	// "other-cluster" must still be present.
	found := false
	for _, v := range raw {
		if v == "other-cluster" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected other-cluster to remain in allowedClusters: %v", raw)
	}
}

// TestHandleTalosClusterDeletion_DecisionHCascade_NotTenant verifies that
// finalizerDecisionHCascade is not added and not processed for non-tenant clusters.
// T-24.
func TestHandleTalosClusterDeletion_DecisionHCascade_NotTenant(t *testing.T) {
	scheme := buildHelperTestScheme(t)

	// Use a dummy finalizer so we can set DeletionTimestamp via Delete().
	const dummyFinalizer = "test.ontai.dev/dummy"
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "mgmt",
			Namespace:       "seam-system",
			Finalizers:      []string{dummyFinalizer},
			ResourceVersion: "1",
		},
		Spec: platformv1alpha1.TalosClusterSpec{
			Role: platformv1alpha1.TalosClusterRoleManagement,
			Mode: platformv1alpha1.TalosClusterModeBootstrap,
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc).
		WithStatusSubresource(&platformv1alpha1.TalosCluster{}).
		Build()

	r := &TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: clientevents.NewFakeRecorder(8),
	}

	// Call ensureDecisionHCascadeFinalizer — should be no-op for management role.
	if err := r.ensureDecisionHCascadeFinalizer(context.Background(), tc); err != nil {
		t.Fatalf("ensureDecisionHCascadeFinalizer: %v", err)
	}
	if controllerutil.ContainsFinalizer(tc, finalizerDecisionHCascade) {
		t.Error("management cluster should not get finalizerDecisionHCascade")
	}

	// Set DeletionTimestamp via Delete (fake client requires at least one finalizer).
	tc = setDeletionTimestamp(t, c, tc)

	// handleTalosClusterDeletion on a management cluster with no Decision H finalizer.
	// The dummyFinalizer is not a known finalizer; both steps should be no-ops.
	result, err := r.handleTalosClusterDeletion(context.Background(), tc)
	if err != nil {
		t.Fatalf("handleTalosClusterDeletion on management cluster: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("unexpected requeue for management cluster deletion: %+v", result)
	}
}

// TestRemoveFromUnstructuredStringSlice_Basic verifies add and remove round-trip.
// T-24.
func TestRemoveFromUnstructuredStringSlice_Basic(t *testing.T) {
	scheme := buildHelperTestScheme(t)
	rbacPolicy := fakeRBACPolicy("seam-platform-rbac-policy", rbacPolicyNamespace, []string{"a", "b", "c"})

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(rbacPolicy).
		Build()

	r := &TalosClusterReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(0)}

	if err := r.removeFromUnstructuredStringSlice(
		context.Background(), rbacPolicyGVK, rbacPolicyNamespace, "seam-platform-rbac-policy",
		[]string{"spec", "allowedClusters"}, "b",
	); err != nil {
		t.Fatalf("removeFromUnstructuredStringSlice: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(rbacPolicyGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: "seam-platform-rbac-policy", Namespace: rbacPolicyNamespace}, got); err != nil {
		t.Fatalf("get RBACPolicy: %v", err)
	}
	raw, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "allowedClusters")
	want := []string{"a", "c"}
	if len(raw) != len(want) {
		t.Fatalf("allowedClusters = %v, want %v", raw, want)
	}
	for i := range want {
		if raw[i] != want[i] {
			t.Errorf("allowedClusters[%d] = %q, want %q", i, raw[i], want[i])
		}
	}
}

// TestRemoveFromUnstructuredStringSlice_NotFound verifies that removing from a
// non-existent resource returns nil (non-fatal). T-24.
func TestRemoveFromUnstructuredStringSlice_NotFound(t *testing.T) {
	scheme := buildHelperTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &TalosClusterReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(0)}

	err := r.removeFromUnstructuredStringSlice(
		context.Background(), rbacPolicyGVK, rbacPolicyNamespace, "nonexistent",
		[]string{"spec", "allowedClusters"}, "ccs-dev",
	)
	if err != nil {
		t.Errorf("expected nil for NotFound resource, got: %v", err)
	}
}

// Ensure fake.Client interface is satisfied (compile-time check).
var _ client.Client = fake.NewClientBuilder().Build()
