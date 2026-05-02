package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"
)

// buildDriftSignalTestScheme returns a scheme for DriftSignalReconciler unit tests.
func buildDriftSignalTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	if err := seamcorev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add seamcorev1alpha1 scheme: %v", err)
	}
	return s
}

// fakeDriftSignal builds a minimal DriftSignal with the given state and kind.
func fakeDriftSignal(name, ns, state, kind string) *seamcorev1alpha1.DriftSignal {
	return &seamcorev1alpha1.DriftSignal{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       ns,
			ResourceVersion: "1",
		},
		Spec: seamcorev1alpha1.DriftSignalSpec{
			State:         seamcorev1alpha1.DriftSignalState(state),
			CorrelationID: "test-correlation-id",
			ObservedAt:    metav1.Now(),
			AffectedCRRef: seamcorev1alpha1.DriftAffectedCRRef{
				Group: "infrastructure.ontai.dev",
				Kind:  kind,
				Name:  "ccs-dev",
			},
			DriftReason: "RunnerConfig not found in ont-system -- cluster-state drift",
		},
	}
}

// fakeTalosClusterForDrift builds a minimal TalosCluster for DriftSignal tests.
func fakeTalosClusterForDrift(name string) *platformv1alpha1.TalosCluster {
	return &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       rbacProfileNamespace, // seam-system
			ResourceVersion: "1",
		},
		Spec: platformv1alpha1.TalosClusterSpec{
			Role:         platformv1alpha1.TalosClusterRoleTenant,
			Mode:         platformv1alpha1.TalosClusterModeImport,
			TalosVersion: "v1.7.0",
		},
	}
}

// fakeTCOR builds a minimal InfrastructureTalosClusterOperationResult for DriftSignal tests.
func fakeTCOR(clusterName, talosVersion string) *seamcorev1alpha1.InfrastructureTalosClusterOperationResult {
	return &seamcorev1alpha1.InfrastructureTalosClusterOperationResult{
		ObjectMeta: metav1.ObjectMeta{
			Name:            clusterName,
			Namespace:       tenantNS(clusterName),
			ResourceVersion: "1",
		},
		Spec: seamcorev1alpha1.InfrastructureTalosClusterOperationResultSpec{
			ClusterRef:   clusterName,
			TalosVersion: talosVersion,
			Revision:     1,
		},
	}
}

// fakeDriftSignalWithVersion builds a DriftSignal for InfrastructureTalosCluster version drift.
func fakeDriftSignalWithVersion(name, ns, specVersion, observedVersion string) *seamcorev1alpha1.DriftSignal {
	return &seamcorev1alpha1.DriftSignal{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       ns,
			ResourceVersion: "1",
		},
		Spec: seamcorev1alpha1.DriftSignalSpec{
			State:         seamcorev1alpha1.DriftSignalStatePending,
			CorrelationID: "test-version-correlation-id",
			ObservedAt:    metav1.Now(),
			AffectedCRRef: seamcorev1alpha1.DriftAffectedCRRef{
				Group: "infrastructure.ontai.dev",
				Kind:  "InfrastructureTalosCluster",
				Name:  "ccs-dev",
			},
			DriftReason: "talos version drift: spec=" + specVersion + " observed=" + observedVersion,
		},
	}
}

// TestDriftSignalReconciler_RunnerConfigKind_RequeuesTalosCluster verifies that a
// pending DriftSignal with kind=InfrastructureRunnerConfig annotates the TalosCluster
// and advances the signal to queued. T-23.
func TestDriftSignalReconciler_RunnerConfigKind_RequeuesTalosCluster(t *testing.T) {
	scheme := buildDriftSignalTestScheme(t)
	clusterName := "ccs-dev"
	tenantNS := "seam-tenant-" + clusterName

	ds := fakeDriftSignal("drift-runnerconfig-ccs-dev", tenantNS, "pending", "InfrastructureRunnerConfig")
	tc := fakeTalosClusterForDrift(clusterName)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ds, tc).
		WithStatusSubresource(&seamcorev1alpha1.DriftSignal{}).
		Build()

	r := &DriftSignalReconciler{Client: c}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: ds.Name, Namespace: tenantNS},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Errorf("unexpected requeue result: %+v", result)
	}

	// TalosCluster must have the drift-requeue annotation.
	gotTC := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: clusterName, Namespace: rbacProfileNamespace}, gotTC); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	if gotTC.Annotations == nil || gotTC.Annotations["ontai.dev/runnerconfig-drift-requeue"] == "" {
		t.Error("expected ontai.dev/runnerconfig-drift-requeue annotation on TalosCluster")
	}

	// DriftSignal state must be advanced to queued.
	gotDS := &seamcorev1alpha1.DriftSignal{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: ds.Name, Namespace: tenantNS}, gotDS); err != nil {
		t.Fatalf("get DriftSignal: %v", err)
	}
	if gotDS.Spec.State != seamcorev1alpha1.DriftSignalStateQueued {
		t.Errorf("DriftSignal.Spec.State = %q, want %q",
			gotDS.Spec.State, seamcorev1alpha1.DriftSignalStateQueued)
	}
}

// TestDriftSignalReconciler_NonPending_NoOp verifies that a DriftSignal already
// in state=queued is not acted upon. T-23.
func TestDriftSignalReconciler_NonPending_NoOp(t *testing.T) {
	scheme := buildDriftSignalTestScheme(t)
	clusterName := "ccs-dev"
	tenantNS := "seam-tenant-" + clusterName

	ds := fakeDriftSignal("drift-runnerconfig-ccs-dev", tenantNS, "queued", "InfrastructureRunnerConfig")
	tc := fakeTalosClusterForDrift(clusterName)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ds, tc).
		WithStatusSubresource(&seamcorev1alpha1.DriftSignal{}).
		Build()

	r := &DriftSignalReconciler{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: ds.Name, Namespace: tenantNS},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// TalosCluster must NOT have been annotated.
	gotTC := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: clusterName, Namespace: rbacProfileNamespace}, gotTC); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	if gotTC.Annotations != nil && gotTC.Annotations["ontai.dev/runnerconfig-drift-requeue"] != "" {
		t.Error("TalosCluster should not be annotated for a non-pending DriftSignal")
	}
}

// TestDriftSignalReconciler_UnknownKind_NoOp verifies that a pending DriftSignal with
// a non-RunnerConfig kind is ignored. T-23.
func TestDriftSignalReconciler_UnknownKind_NoOp(t *testing.T) {
	scheme := buildDriftSignalTestScheme(t)
	clusterName := "ccs-dev"
	tenantNS := "seam-tenant-" + clusterName

	ds := fakeDriftSignal("drift-other", tenantNS, "pending", "SomeOtherKind")
	tc := fakeTalosClusterForDrift(clusterName)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ds, tc).
		WithStatusSubresource(&seamcorev1alpha1.DriftSignal{}).
		Build()

	r := &DriftSignalReconciler{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: ds.Name, Namespace: tenantNS},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// TalosCluster annotation must not be set.
	gotTC := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: clusterName, Namespace: rbacProfileNamespace}, gotTC); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	if gotTC.Annotations != nil && gotTC.Annotations["ontai.dev/runnerconfig-drift-requeue"] != "" {
		t.Error("TalosCluster should not be annotated for a non-RunnerConfig DriftSignal kind")
	}
}

// TestDriftSignalReconciler_NotFound_NoOp verifies that a deleted DriftSignal
// returns without error. T-23.
func TestDriftSignalReconciler_NotFound_NoOp(t *testing.T) {
	scheme := buildDriftSignalTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &DriftSignalReconciler{Client: c}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "missing", Namespace: "seam-tenant-ccs-dev"},
	})
	if err != nil {
		t.Errorf("expected nil error for NotFound DriftSignal, got: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Errorf("unexpected requeue: %+v", result)
	}
}

// TestDriftSignalReconciler_TalosVersionDrift_PatchesObservedVersionAndBumpsTCOR verifies
// that a pending InfrastructureTalosCluster DriftSignal causes:
//   - TalosCluster.status.observedTalosVersion to be patched to the observed version
//   - An out-of-band record written to the TCOR
//   - The TCOR revision bumped to the observed version
//   - The DriftSignal advanced to queued
func TestDriftSignalReconciler_TalosVersionDrift_PatchesObservedVersionAndBumpsTCOR(t *testing.T) {
	scheme := buildDriftSignalTestScheme(t)
	if err := seamcorev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add seamcore scheme: %v", err)
	}

	clusterName := "ccs-dev"
	tenantNSName := tenantNS(clusterName)
	specVersion := "v1.7.0"
	observedVersion := "v1.7.4"
	signalName := "drift-version-" + clusterName

	ds := fakeDriftSignalWithVersion(signalName, tenantNSName, specVersion, observedVersion)
	tc := fakeTalosClusterForDrift(clusterName)
	tcor := fakeTCOR(clusterName, specVersion)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ds, tc, tcor).
		WithStatusSubresource(&seamcorev1alpha1.DriftSignal{}, &platformv1alpha1.TalosCluster{}).
		Build()

	r := &DriftSignalReconciler{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: signalName, Namespace: tenantNSName},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// observedTalosVersion must be updated on TalosCluster status.
	gotTC := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: clusterName, Namespace: rbacProfileNamespace}, gotTC); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	if gotTC.Status.ObservedTalosVersion != observedVersion {
		t.Errorf("TalosCluster.Status.ObservedTalosVersion = %q, want %q",
			gotTC.Status.ObservedTalosVersion, observedVersion)
	}

	// TCOR must have been bumped to the observed version.
	gotTCOR := &seamcorev1alpha1.InfrastructureTalosClusterOperationResult{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: clusterName, Namespace: tenantNSName}, gotTCOR); err != nil {
		t.Fatalf("get TCOR: %v", err)
	}
	if gotTCOR.Spec.TalosVersion != observedVersion {
		t.Errorf("TCOR.Spec.TalosVersion = %q, want %q (should be bumped to observed version)",
			gotTCOR.Spec.TalosVersion, observedVersion)
	}
	if gotTCOR.Spec.Revision != 2 {
		t.Errorf("TCOR.Spec.Revision = %d, want 2 (bump increments revision)", gotTCOR.Spec.Revision)
	}

	// DriftSignal must be advanced to queued.
	gotDS := &seamcorev1alpha1.DriftSignal{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: signalName, Namespace: tenantNSName}, gotDS); err != nil {
		t.Fatalf("get DriftSignal: %v", err)
	}
	if gotDS.Spec.State != seamcorev1alpha1.DriftSignalStateQueued {
		t.Errorf("DriftSignal.Spec.State = %q, want queued", gotDS.Spec.State)
	}
}

// TestDriftSignalReconciler_TalosVersionDrift_NoParsableVersion_AdvancesToQueued verifies
// that a version drift signal without a parseable observed version is still advanced to queued
// (does not retry indefinitely).
func TestDriftSignalReconciler_TalosVersionDrift_NoParsableVersion_AdvancesToQueued(t *testing.T) {
	scheme := buildDriftSignalTestScheme(t)
	clusterName := "ccs-dev"
	tenantNSName := tenantNS(clusterName)
	signalName := "drift-version-" + clusterName

	ds := &seamcorev1alpha1.DriftSignal{
		ObjectMeta: metav1.ObjectMeta{
			Name: signalName, Namespace: tenantNSName, ResourceVersion: "1",
		},
		Spec: seamcorev1alpha1.DriftSignalSpec{
			State:         seamcorev1alpha1.DriftSignalStatePending,
			CorrelationID: "test-no-version",
			ObservedAt:    metav1.Now(),
			AffectedCRRef: seamcorev1alpha1.DriftAffectedCRRef{
				Group: "infrastructure.ontai.dev",
				Kind:  "InfrastructureTalosCluster",
				Name:  clusterName,
			},
			DriftReason: "talos version drift: no version info",
		},
	}
	tc := fakeTalosClusterForDrift(clusterName)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ds, tc).
		WithStatusSubresource(&seamcorev1alpha1.DriftSignal{}).
		Build()

	r := &DriftSignalReconciler{Client: c}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: signalName, Namespace: tenantNSName},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Signal must still be advanced to queued to avoid retry storms.
	gotDS := &seamcorev1alpha1.DriftSignal{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: signalName, Namespace: tenantNSName}, gotDS); err != nil {
		t.Fatalf("get DriftSignal: %v", err)
	}
	if gotDS.Spec.State != seamcorev1alpha1.DriftSignalStateQueued {
		t.Errorf("DriftSignal.Spec.State = %q, want queued", gotDS.Spec.State)
	}
}
