// Package controller_test -- CAPI provisioning path unit tests.
//
// These tests cover the reconcileCAPIPath steps not otherwise exercised:
//
//  1. SeamInfrastructureCluster created in seam-tenant-{name} namespace.
//  2. CAPI Cluster created with spec.infrastructureRef pointing to SeamInfrastructureCluster.
//  3. TalosControlPlane created with correct replica count and Kubernetes version.
//  4. CiliumPending condition set when CAPI Cluster reaches Running and CiliumPackRef is set.
//  5. MachineDeployment created for each worker pool in spec.capi.workers.
//  6. TalosConfigTemplate includes cluster.network.cni.name=none (CP-INV-009).
//  7. TalosConfigTemplate includes Cilium BPF sysctl params (CP-INV-009).
//  8. CiliumPending cleared when Cilium PackInstance reaches Ready.
//
// All tests use the fake controller-runtime client. No live cluster required.
// platform-schema.md §2, §4. taloscluster_helpers.go ensureXxx functions.
package controller_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// TestTalosClusterReconcile_CAPI_CreatesSeamInfrastructureCluster verifies that
// reconcileCAPIPath creates a SeamInfrastructureCluster in the tenant namespace
// seam-tenant-{tc.Name} on the first reconcile. CP-INV-008.
func TestTalosClusterReconcile_CAPI_CreatesSeamInfrastructureCluster(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-dev", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			CAPI: &platformv1alpha1.CAPIConfig{
				Enabled:           true,
				TalosVersion:      "v1.7.0",
				KubernetesVersion: "v1.31.0",
				ControlPlane:      &platformv1alpha1.CAPIControlPlaneConfig{Replicas: 3},
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
		Recorder: clientevents.NewFakeRecorder(32),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sic := &unstructured.Unstructured{}
	sic.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "infrastructure.cluster.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "SeamInfrastructureCluster",
	})
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "ccs-dev",
		Namespace: "seam-tenant-ccs-dev",
	}, sic); err != nil {
		t.Fatalf("SeamInfrastructureCluster not created in seam-tenant-ccs-dev: %v", err)
	}

	// Verify the owner reference points to TalosCluster. CP-INV-008.
	owners := sic.GetOwnerReferences()
	if len(owners) == 0 {
		t.Fatal("SeamInfrastructureCluster has no ownerReferences")
	}
	if owners[0].Kind != "TalosCluster" {
		t.Errorf("ownerReference kind = %q, want TalosCluster", owners[0].Kind)
	}
}

// TestTalosClusterReconcile_CAPI_CreatesCAPIClusterWithInfraRef verifies that
// reconcileCAPIPath creates a CAPI Cluster with spec.infrastructureRef.kind set
// to SeamInfrastructureCluster. platform-schema.md §4.
func TestTalosClusterReconcile_CAPI_CreatesCAPIClusterWithInfraRef(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-dev", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			CAPI: &platformv1alpha1.CAPIConfig{
				Enabled:           true,
				TalosVersion:      "v1.7.0",
				KubernetesVersion: "v1.31.0",
				ControlPlane:      &platformv1alpha1.CAPIControlPlaneConfig{Replicas: 3},
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
		Recorder: clientevents.NewFakeRecorder(32),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	capiCluster := &unstructured.Unstructured{}
	capiCluster.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "Cluster",
	})
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "ccs-dev",
		Namespace: "seam-tenant-ccs-dev",
	}, capiCluster); err != nil {
		t.Fatalf("CAPI Cluster not created in seam-tenant-ccs-dev: %v", err)
	}

	infraKind, _, _ := unstructured.NestedString(capiCluster.Object, "spec", "infrastructureRef", "kind")
	if infraKind != "SeamInfrastructureCluster" {
		t.Errorf("spec.infrastructureRef.kind = %q, want SeamInfrastructureCluster", infraKind)
	}

	infraName, _, _ := unstructured.NestedString(capiCluster.Object, "spec", "infrastructureRef", "name")
	if infraName != "ccs-dev" {
		t.Errorf("spec.infrastructureRef.name = %q, want ccs-dev", infraName)
	}

	// ControlPlaneRef must point to TalosControlPlane.
	cpKind, _, _ := unstructured.NestedString(capiCluster.Object, "spec", "controlPlaneRef", "kind")
	if cpKind != "TalosControlPlane" {
		t.Errorf("spec.controlPlaneRef.kind = %q, want TalosControlPlane", cpKind)
	}
}

// TestTalosClusterReconcile_CAPI_CreatesTalosControlPlaneWithReplicasAndVersion
// verifies that reconcileCAPIPath creates a TalosControlPlane with the replica
// count and Kubernetes version from spec. platform-schema.md §2.1.
func TestTalosClusterReconcile_CAPI_CreatesTalosControlPlaneWithReplicasAndVersion(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-dev", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			CAPI: &platformv1alpha1.CAPIConfig{
				Enabled:           true,
				TalosVersion:      "v1.7.0",
				KubernetesVersion: "v1.31.0",
				ControlPlane:      &platformv1alpha1.CAPIControlPlaneConfig{Replicas: 3},
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
		Recorder: clientevents.NewFakeRecorder(32),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tcp := &unstructured.Unstructured{}
	tcp.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "controlplane.cluster.x-k8s.io",
		Version: "v1alpha3",
		Kind:    "TalosControlPlane",
	})
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "ccs-dev-control-plane",
		Namespace: "seam-tenant-ccs-dev",
	}, tcp); err != nil {
		t.Fatalf("TalosControlPlane not created: %v", err)
	}

	replicas, _, _ := unstructured.NestedInt64(tcp.Object, "spec", "replicas")
	if replicas != 3 {
		t.Errorf("spec.replicas = %d, want 3", replicas)
	}

	version, _, _ := unstructured.NestedString(tcp.Object, "spec", "version")
	if version != "v1.31.0" {
		t.Errorf("spec.version = %q, want v1.31.0", version)
	}
}

// TestTalosClusterReconcile_CAPI_CiliumPendingWhenClusterRunning verifies that
// when the CAPI Cluster has reached Running state and CiliumPackRef is configured,
// the reconciler sets CiliumPending=True. CP-INV-013: CiliumPending is not degraded.
func TestTalosClusterReconcile_CAPI_CiliumPendingWhenClusterRunning(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-dev", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			CAPI: &platformv1alpha1.CAPIConfig{
				Enabled:           true,
				TalosVersion:      "v1.7.0",
				KubernetesVersion: "v1.31.0",
				ControlPlane:      &platformv1alpha1.CAPIControlPlaneConfig{Replicas: 3},
				CiliumPackRef:     &platformv1alpha1.CAPICiliumPackRef{Name: "cilium-pack", Version: "1.15.0"},
			},
		},
	}
	// Pre-create a CAPI Cluster in Running state so the reconciler advances past step 7.
	capiCluster := buildFakeCAPIClusterRunning("ccs-dev", "seam-tenant-ccs-dev")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, capiCluster).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: clientevents.NewFakeRecorder(32),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-dev", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}

	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeCiliumPending)
	if cond == nil {
		t.Fatal("CiliumPending condition not set after CAPI Cluster reached Running")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("CiliumPending = %s, want True", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonCiliumPackPending {
		t.Errorf("CiliumPending reason = %q, want %s", cond.Reason, platformv1alpha1.ReasonCiliumPackPending)
	}
}

// TestTalosClusterReconcile_CAPI_TalosConfigTemplateHasCNINone verifies that
// ensureTalosConfigTemplate creates a TalosConfigTemplate whose configPatches
// include a replace patch for /cluster/network/cni/name with value "none".
// CP-INV-009: CNI=none is mandatory; Cilium replaces it at runtime.
func TestTalosClusterReconcile_CAPI_TalosConfigTemplateHasCNINone(t *testing.T) {
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
		Recorder: clientevents.NewFakeRecorder(32),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tct := &unstructured.Unstructured{}
	tct.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "bootstrap.cluster.x-k8s.io",
		Version: "v1alpha3",
		Kind:    "TalosConfigTemplate",
	})
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "ccs-dev-config-template",
		Namespace: "seam-tenant-ccs-dev",
	}, tct); err != nil {
		t.Fatalf("TalosConfigTemplate not created: %v", err)
	}

	patches, _, _ := unstructured.NestedSlice(tct.Object, "spec", "template", "spec", "configPatches")
	foundCNI := false
	for _, p := range patches {
		patch, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if patch["path"] == "/cluster/network/cni/name" && patch["value"] == "none" {
			foundCNI = true
		}
	}
	if !foundCNI {
		t.Error("TalosConfigTemplate configPatches missing /cluster/network/cni/name=none (CP-INV-009)")
	}
}

// TestTalosClusterReconcile_CAPI_TalosConfigTemplateHasBPFSysctls verifies that
// ensureTalosConfigTemplate sets the two Cilium-required BPF kernel parameters
// in the machine sysctl patch. CP-INV-009.
func TestTalosClusterReconcile_CAPI_TalosConfigTemplateHasBPFSysctls(t *testing.T) {
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
		Recorder: clientevents.NewFakeRecorder(32),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tct := &unstructured.Unstructured{}
	tct.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "bootstrap.cluster.x-k8s.io",
		Version: "v1alpha3",
		Kind:    "TalosConfigTemplate",
	})
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "ccs-dev-config-template",
		Namespace: "seam-tenant-ccs-dev",
	}, tct); err != nil {
		t.Fatalf("TalosConfigTemplate not created: %v", err)
	}

	patches, _, _ := unstructured.NestedSlice(tct.Object, "spec", "template", "spec", "configPatches")
	var sysctls map[string]interface{}
	for _, p := range patches {
		patch, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if patch["path"] == "/machine/sysctls" {
			sysctls, _ = patch["value"].(map[string]interface{})
			break
		}
	}
	if sysctls == nil {
		t.Fatal("TalosConfigTemplate configPatches missing /machine/sysctls patch (CP-INV-009)")
	}
	if sysctls["net.core.bpf_jit_harden"] != "0" {
		t.Errorf("net.core.bpf_jit_harden = %v, want \"0\"", sysctls["net.core.bpf_jit_harden"])
	}
	if sysctls["kernel.unprivileged_bpf_disabled"] != "0" {
		t.Errorf("kernel.unprivileged_bpf_disabled = %v, want \"0\"", sysctls["kernel.unprivileged_bpf_disabled"])
	}
}

// TestTalosClusterReconcile_CAPI_CiliumPendingClearedWhenPackInstanceReady verifies
// that when the CAPI Cluster is Running and the Cilium PackInstance reaches Ready,
// the reconciler clears CiliumPending and sets Ready=True. CP-INV-013.
func TestTalosClusterReconcile_CAPI_CiliumPendingClearedWhenPackInstanceReady(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-dev", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			CAPI: &platformv1alpha1.CAPIConfig{
				Enabled:           true,
				TalosVersion:      "v1.7.0",
				KubernetesVersion: "v1.31.0",
				ControlPlane:      &platformv1alpha1.CAPIControlPlaneConfig{Replicas: 1},
				CiliumPackRef:     &platformv1alpha1.CAPICiliumPackRef{Name: "cilium-pack", Version: "1.15.0"},
			},
		},
	}

	capiCluster := buildFakeCAPIClusterRunning("ccs-dev", "seam-tenant-ccs-dev")

	// Build a PackInstance in Ready state with the Cilium pack label.
	packInstance := &unstructured.Unstructured{}
	packInstance.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "infra.ontai.dev",
		Version: "v1alpha1",
		Kind:    "PackInstance",
	})
	packInstance.SetName("cilium-pack-instance")
	packInstance.SetNamespace("seam-tenant-ccs-dev")
	packInstance.SetLabels(map[string]string{
		"infra.ontai.dev/pack-name": "cilium-pack",
	})
	_ = unstructured.SetNestedField(packInstance.Object, true, "status", "ready")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, capiCluster, packInstance).
		WithStatusSubresource(tc).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: clientevents.NewFakeRecorder(32),
		RemoteConductorAvailableFn: func(_ context.Context, _ string) (bool, error) {
			return true, nil
		},
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-dev", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}

	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeCiliumPending)
	if cond == nil {
		t.Fatal("CiliumPending condition absent after transition; expected CiliumPending=False")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("CiliumPending = %s, want False", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonCiliumPackReady {
		t.Errorf("CiliumPending reason = %q, want %s", cond.Reason, platformv1alpha1.ReasonCiliumPackReady)
	}

	ready := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Error("TalosCluster Ready condition should be True after Cilium and Conductor both ready")
	}
}

// TestTalosClusterReconcile_CAPI_CreatesMachineDeploymentPerWorkerPool verifies
// that reconcileCAPIPath creates a MachineDeployment for each entry in
// spec.capi.workers. platform-schema.md §2.2.
func TestTalosClusterReconcile_CAPI_CreatesMachineDeploymentPerWorkerPool(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-dev", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			CAPI: &platformv1alpha1.CAPIConfig{
				Enabled:           true,
				TalosVersion:      "v1.7.0",
				KubernetesVersion: "v1.31.0",
				ControlPlane:      &platformv1alpha1.CAPIControlPlaneConfig{Replicas: 3},
				Workers: []platformv1alpha1.CAPIWorkerPool{
					{Name: "workers", Replicas: 2},
					{Name: "gpu", Replicas: 1},
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
		Recorder: clientevents.NewFakeRecorder(32),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-dev", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, poolName := range []string{"workers", "gpu"} {
		md := &unstructured.Unstructured{}
		md.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "cluster.x-k8s.io",
			Version: "v1beta1",
			Kind:    "MachineDeployment",
		})
		mdName := "ccs-dev-" + poolName
		if err := c.Get(context.Background(), types.NamespacedName{
			Name:      mdName,
			Namespace: "seam-tenant-ccs-dev",
		}, md); err != nil {
			t.Errorf("MachineDeployment %q not created in seam-tenant-ccs-dev: %v", mdName, err)
		}
	}
}
