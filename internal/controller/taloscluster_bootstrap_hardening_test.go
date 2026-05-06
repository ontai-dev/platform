package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"
)

// buildHardeningTestScheme registers all types needed for ensureBootstrapHardening tests.
func buildHardeningTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add clientgo: %v", err)
	}
	if err := seamcorev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add seamcore: %v", err)
	}
	if err := platformv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add platform: %v", err)
	}
	return s
}

// makeValidHardeningProfile returns a HardeningProfile with Valid=True.
func makeValidHardeningProfile(name, ns string) *platformv1alpha1.HardeningProfile {
	hp := &platformv1alpha1.HardeningProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       ns,
			ResourceVersion: "1",
		},
		Spec: platformv1alpha1.HardeningProfileSpec{
			SysctlParams: map[string]string{"kernel.dmesg_restrict": "1"},
		},
		Status: platformv1alpha1.HardeningProfileStatus{
			Conditions: []metav1.Condition{
				{
					Type:               platformv1alpha1.ConditionTypeHardeningProfileValid,
					Status:             metav1.ConditionTrue,
					Reason:             "ProfileValid",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
	return hp
}

// makeTalosClusterWithHardeningRef returns a TalosCluster in seam-system with
// spec.hardeningProfileRef pointing to the given name/namespace.
func makeTalosClusterWithHardeningRef(clusterName, hpName, hpNS string) *platformv1alpha1.TalosCluster {
	return &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:            clusterName,
			Namespace:       "seam-system",
			ResourceVersion: "1",
		},
		Spec: platformv1alpha1.TalosClusterSpec{
			Mode: platformv1alpha1.TalosClusterModeImport,
			HardeningProfileRef: &platformv1alpha1.LocalObjectRef{
				Name:      hpName,
				Namespace: hpNS,
			},
		},
	}
}

// TestEnsureBootstrapHardening_NilRef returns without action when HardeningProfileRef is nil.
func TestEnsureBootstrapHardening_NilRef(t *testing.T) {
	s := buildHardeningTestScheme(t)
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ccs-dev", Namespace: "seam-system", ResourceVersion: "1"},
		Spec:       platformv1alpha1.TalosClusterSpec{Mode: platformv1alpha1.TalosClusterModeImport},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tc).Build()
	r := &TalosClusterReconciler{Client: c}

	result, err := r.ensureBootstrapHardening(context.Background(), tc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got %v", result.RequeueAfter)
	}

	// No HardeningApplied condition should be set.
	cond := platformv1alpha1.FindCondition(tc.Status.Conditions, platformv1alpha1.ConditionTypeHardeningApplied)
	if cond != nil {
		t.Errorf("expected no HardeningApplied condition, found: %+v", cond)
	}

	// No NodeMaintenance should exist.
	nmList := &platformv1alpha1.NodeMaintenanceList{}
	if err := c.List(context.Background(), nmList); err != nil {
		t.Fatalf("list NodeMaintenance: %v", err)
	}
	if len(nmList.Items) != 0 {
		t.Errorf("expected 0 NodeMaintenance, got %d", len(nmList.Items))
	}
}

// TestEnsureBootstrapHardening_CreatesNodeMaintenance verifies that a NodeMaintenance is
// created with the correct label and operation when no bootstrap NodeMaintenance exists.
func TestEnsureBootstrapHardening_CreatesNodeMaintenance(t *testing.T) {
	s := buildHardeningTestScheme(t)
	hp := makeValidHardeningProfile("baseline-hardening", "seam-system")
	tc := makeTalosClusterWithHardeningRef("ccs-dev", "baseline-hardening", "seam-system")

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tc, hp).Build()
	r := &TalosClusterReconciler{Client: c}

	result, err := r.ensureBootstrapHardening(context.Background(), tc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected non-zero RequeueAfter while NodeMaintenance is pending")
	}

	// HardeningApplied=False with reason HardeningPending.
	cond := platformv1alpha1.FindCondition(tc.Status.Conditions, platformv1alpha1.ConditionTypeHardeningApplied)
	if cond == nil {
		t.Fatal("expected HardeningApplied condition")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("expected HardeningApplied=False, got %s", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonHardeningPending {
		t.Errorf("expected reason %q, got %q", platformv1alpha1.ReasonHardeningPending, cond.Reason)
	}

	// One NodeMaintenance with the bootstrap label.
	nmList := &platformv1alpha1.NodeMaintenanceList{}
	if err := c.List(context.Background(), nmList); err != nil {
		t.Fatalf("list NodeMaintenance: %v", err)
	}
	if len(nmList.Items) != 1 {
		t.Fatalf("expected 1 NodeMaintenance, got %d", len(nmList.Items))
	}
	nm := nmList.Items[0]
	if nm.Labels[hardeningBootstrapLabel] != hardeningBootstrapLabelValue {
		t.Errorf("expected label %s=%s", hardeningBootstrapLabel, hardeningBootstrapLabelValue)
	}
	if nm.Spec.Operation != platformv1alpha1.NodeMaintenanceOperationHardeningApply {
		t.Errorf("expected operation hardening-apply, got %q", nm.Spec.Operation)
	}
	if nm.Spec.HardeningProfileRef == nil || nm.Spec.HardeningProfileRef.Name != "baseline-hardening" {
		t.Errorf("unexpected HardeningProfileRef: %v", nm.Spec.HardeningProfileRef)
	}
}

// TestEnsureBootstrapHardening_NoDuplicate verifies that no second NodeMaintenance is
// created when a bootstrap NodeMaintenance already exists (pending).
func TestEnsureBootstrapHardening_NoDuplicate(t *testing.T) {
	s := buildHardeningTestScheme(t)
	hp := makeValidHardeningProfile("baseline-hardening", "seam-system")
	tc := makeTalosClusterWithHardeningRef("ccs-dev", "baseline-hardening", "seam-system")

	// Pre-create a pending bootstrap NodeMaintenance.
	existingNM := &platformv1alpha1.NodeMaintenance{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "ccs-dev-bootstrap-hardening-abc",
			Namespace:       "seam-tenant-ccs-dev",
			ResourceVersion: "1",
			Labels:          map[string]string{hardeningBootstrapLabel: hardeningBootstrapLabelValue},
		},
		Spec: platformv1alpha1.NodeMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-dev", Namespace: "seam-system"},
			Operation:  platformv1alpha1.NodeMaintenanceOperationHardeningApply,
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tc, hp, existingNM).Build()
	r := &TalosClusterReconciler{Client: c}

	result, err := r.ensureBootstrapHardening(context.Background(), tc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected non-zero RequeueAfter while NodeMaintenance is pending")
	}

	// Still only one NodeMaintenance.
	nmList := &platformv1alpha1.NodeMaintenanceList{}
	if err := c.List(context.Background(), nmList); err != nil {
		t.Fatalf("list NodeMaintenance: %v", err)
	}
	if len(nmList.Items) != 1 {
		t.Errorf("expected 1 NodeMaintenance (no duplicate), got %d", len(nmList.Items))
	}
}

// TestEnsureBootstrapHardening_SetsAppliedWhenReady verifies that HardeningApplied=True
// is set when the existing bootstrap NodeMaintenance has Ready=True.
func TestEnsureBootstrapHardening_SetsAppliedWhenReady(t *testing.T) {
	s := buildHardeningTestScheme(t)
	hp := makeValidHardeningProfile("baseline-hardening", "seam-system")
	tc := makeTalosClusterWithHardeningRef("ccs-dev", "baseline-hardening", "seam-system")

	// Pre-create a Ready bootstrap NodeMaintenance.
	readyNM := &platformv1alpha1.NodeMaintenance{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "ccs-dev-bootstrap-hardening-abc",
			Namespace:       "seam-tenant-ccs-dev",
			ResourceVersion: "1",
			Labels:          map[string]string{hardeningBootstrapLabel: hardeningBootstrapLabelValue},
		},
		Spec: platformv1alpha1.NodeMaintenanceSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: "ccs-dev", Namespace: "seam-system"},
			Operation:  platformv1alpha1.NodeMaintenanceOperationHardeningApply,
		},
		Status: platformv1alpha1.NodeMaintenanceStatus{
			Conditions: []metav1.Condition{
				{
					Type:               platformv1alpha1.ConditionTypeNodeMaintenanceReady,
					Status:             metav1.ConditionTrue,
					Reason:             "JobComplete",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(readyNM).WithObjects(tc, hp, readyNM).Build()
	r := &TalosClusterReconciler{Client: c}

	result, err := r.ensureBootstrapHardening(context.Background(), tc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue when hardening complete, got %v", result.RequeueAfter)
	}

	cond := platformv1alpha1.FindCondition(tc.Status.Conditions, platformv1alpha1.ConditionTypeHardeningApplied)
	if cond == nil {
		t.Fatal("expected HardeningApplied condition")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("expected HardeningApplied=True, got %s", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonHardeningApplied {
		t.Errorf("expected reason %q, got %q", platformv1alpha1.ReasonHardeningApplied, cond.Reason)
	}
}
