// Package controller_test — TalosCluster spec.versionUpgrade and anti-regression tests.
//
// These tests exercise:
//  1. spec.versionUpgrade=true on a Ready cluster auto-creates an UpgradePolicy CR.
//  2. spec.versionUpgrade=true without talosVersion sets PhaseFailed.
//  3. Version regression guard: spec.talosVersion < status.observedTalosVersion
//     sets VersionRegressionBlocked=True and returns without upgrading.
//  4. After UpgradePolicy reaches Ready=True, spec.versionUpgrade is cleared and
//     VersionUpgradePending is set to False.
//  5. TCOR stub: readOperationalResult calls stubDumpTCORToGraphQueryDB on terminal
//     status. Verified via TCOR deletion lifecycle: ownerReference ensures TCOR is
//     GC'd when the owning UpgradePolicy is deleted.
package controller_test

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// buildReadyManagementCluster returns a TalosCluster already in Ready=True state
// with a given observedTalosVersion, simulating a cluster that has been previously
// bootstrapped and potentially upgraded.
func buildReadyManagementCluster(name, namespace, talosVersion, observedVersion string) *platformv1alpha1.TalosCluster {
	tc := &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec: platformv1alpha1.TalosClusterSpec{
			Mode:         platformv1alpha1.TalosClusterModeImport,
			TalosVersion: talosVersion,
			Role:         platformv1alpha1.TalosClusterRoleManagement,
		},
		Status: platformv1alpha1.TalosClusterStatus{
			ObservedTalosVersion: observedVersion,
			Conditions: []metav1.Condition{
				{
					Type:               platformv1alpha1.ConditionTypeReady,
					Status:             metav1.ConditionTrue,
					Reason:             platformv1alpha1.ReasonClusterReady,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               platformv1alpha1.ConditionTypeBootstrapped,
					Status:             metav1.ConditionTrue,
					Reason:             platformv1alpha1.ReasonImportComplete,
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
	return tc
}

// TestTalosCluster_VersionUpgrade_CreatesUpgradePolicy verifies that when a Ready
// TalosCluster has spec.versionUpgrade=true and spec.talosVersion set, the reconciler
// creates an UpgradePolicy CR with the target version and sets VersionUpgradePending=True.
func TestTalosCluster_VersionUpgrade_CreatesUpgradePolicy(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildReadyManagementCluster("ccs-mgmt", "seam-system", "v1.9.4", "v1.9.3")
	tc.Spec.VersionUpgrade = true

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

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should requeue to poll UpgradePolicy.
	if result.RequeueAfter == 0 {
		t.Error("expected non-zero RequeueAfter while waiting for UpgradePolicy")
	}

	// UpgradePolicy must exist.
	up := &platformv1alpha1.UpgradePolicy{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "ccs-mgmt-version-upgrade",
		Namespace: "seam-system",
	}, up); err != nil {
		t.Fatalf("UpgradePolicy not created: %v", err)
	}
	if up.Spec.UpgradeType != platformv1alpha1.UpgradeTypeTalos {
		t.Errorf("UpgradePolicy.Spec.UpgradeType = %q, want talos", up.Spec.UpgradeType)
	}
	if up.Spec.TargetTalosVersion != "v1.9.4" {
		t.Errorf("UpgradePolicy.Spec.TargetTalosVersion = %q, want v1.9.4", up.Spec.TargetTalosVersion)
	}
	if up.Spec.ClusterRef.Name != "ccs-mgmt" {
		t.Errorf("UpgradePolicy.Spec.ClusterRef.Name = %q, want ccs-mgmt", up.Spec.ClusterRef.Name)
	}

	// VersionUpgradePending must be True.
	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-mgmt", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeVersionUpgradePending)
	if cond == nil {
		t.Fatal("VersionUpgradePending condition not set")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("VersionUpgradePending = %s, want True", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonVersionUpgradeSubmitted {
		t.Errorf("VersionUpgradePending reason = %q, want %q",
			cond.Reason, platformv1alpha1.ReasonVersionUpgradeSubmitted)
	}
}

// TestTalosCluster_VersionUpgrade_NoVersion_SetsPhaseFailed verifies that when
// spec.versionUpgrade=true but spec.talosVersion is empty, the reconciler sets
// PhaseFailed=True and does not create an UpgradePolicy.
func TestTalosCluster_VersionUpgrade_NoVersion_SetsPhaseFailed(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildReadyManagementCluster("ccs-mgmt", "seam-system", "", "v1.9.3")
	tc.Spec.VersionUpgrade = true

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
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No UpgradePolicy must have been created.
	upList := &platformv1alpha1.UpgradePolicyList{}
	if err := c.List(context.Background(), upList); err != nil {
		t.Fatalf("list UpgradePolicies: %v", err)
	}
	if len(upList.Items) != 0 {
		t.Errorf("expected no UpgradePolicy when talosVersion empty, got %d", len(upList.Items))
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-mgmt", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypePhaseFailed)
	if cond == nil {
		t.Fatal("PhaseFailed condition not set when talosVersion empty")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("PhaseFailed = %s, want True", cond.Status)
	}
}

// TestTalosCluster_VersionRegressionBlocked verifies that when spec.talosVersion
// is lower than status.observedTalosVersion, the reconciler sets
// VersionRegressionBlocked=True and does not create an UpgradePolicy or downgrade.
func TestTalosCluster_VersionRegressionBlocked(t *testing.T) {
	scheme := buildDay2Scheme(t)
	// spec=v1.9.2, observed=v1.9.3 → regression attempt.
	tc := buildReadyManagementCluster("ccs-mgmt", "seam-system", "v1.9.2", "v1.9.3")

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

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return without requeue — blocked state, waiting for spec correction.
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue when regression blocked, got RequeueAfter=%v", result.RequeueAfter)
	}

	// No UpgradePolicy must exist.
	upList := &platformv1alpha1.UpgradePolicyList{}
	if err := c.List(context.Background(), upList); err != nil {
		t.Fatalf("list UpgradePolicies: %v", err)
	}
	if len(upList.Items) != 0 {
		t.Errorf("expected no UpgradePolicy when regression blocked, got %d", len(upList.Items))
	}

	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-mgmt", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeVersionRegressionBlocked)
	if cond == nil {
		t.Fatal("VersionRegressionBlocked condition not set")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("VersionRegressionBlocked = %s, want True", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonVersionRegressionAttempted {
		t.Errorf("VersionRegressionBlocked reason = %q, want %q",
			cond.Reason, platformv1alpha1.ReasonVersionRegressionAttempted)
	}
}

// TestTalosCluster_VersionUpgrade_CompletesCondition verifies that when the
// auto-created UpgradePolicy reaches Ready=True, the reconciler sets
// VersionUpgradePending=False. spec.versionUpgrade is user-controlled and is
// NOT auto-cleared by the reconciler.
func TestTalosCluster_VersionUpgrade_CompletesCondition(t *testing.T) {
	scheme := buildDay2Scheme(t)
	tc := buildReadyManagementCluster("ccs-mgmt", "seam-system", "v1.9.4", "v1.9.3")
	tc.Spec.VersionUpgrade = true

	// Pre-create the UpgradePolicy in Ready=True state (simulates prior reconcile
	// creating it and the upgrade completing).
	existingUP := &platformv1alpha1.UpgradePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ccs-mgmt-version-upgrade",
			Namespace: "seam-system",
		},
		Spec: platformv1alpha1.UpgradePolicySpec{
			ClusterRef:         platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt", Namespace: "seam-system"},
			UpgradeType:        platformv1alpha1.UpgradeTypeTalos,
			TargetTalosVersion: "v1.9.4",
		},
		Status: platformv1alpha1.UpgradePolicyStatus{
			Conditions: []metav1.Condition{
				{
					Type:               platformv1alpha1.ConditionTypeUpgradePolicyReady,
					Status:             metav1.ConditionTrue,
					Reason:             platformv1alpha1.ReasonUpgradeJobComplete,
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, existingUP).
		WithStatusSubresource(tc, existingUP).
		Build()
	r := &controller.TalosClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: clientevents.NewFakeRecorder(32),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ccs-mgmt", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Upgrade complete — no requeue needed.
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue after upgrade completion, got RequeueAfter=%v", result.RequeueAfter)
	}

	// spec.versionUpgrade is user-controlled; the reconciler does not clear it.
	// VersionUpgradePending=False is the authoritative completion signal.
	got := &platformv1alpha1.TalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-mgmt", Namespace: "seam-system",
	}, got); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}

	// VersionUpgradePending must be False.
	cond := platformv1alpha1.FindCondition(got.Status.Conditions, platformv1alpha1.ConditionTypeVersionUpgradePending)
	if cond == nil {
		t.Fatal("VersionUpgradePending condition not set after completion")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("VersionUpgradePending = %s, want False", cond.Status)
	}
	if cond.Reason != platformv1alpha1.ReasonVersionUpgradeComplete {
		t.Errorf("VersionUpgradePending reason = %q, want %q",
			cond.Reason, platformv1alpha1.ReasonVersionUpgradeComplete)
	}
}

// TestUpgradePolicy_PatchesObservedTalosVersion verifies that when an UpgradePolicy
// for a talos upgrade completes successfully, the reconciler patches
// InfrastructureTalosCluster.status.observedTalosVersion to the target version.
func TestUpgradePolicy_PatchesObservedTalosVersion(t *testing.T) {
	scheme := buildDay2Scheme(t)

	// buildReadyManagementCluster returns a *platformv1alpha1.TalosCluster, which is a type
	// alias for *seamcorev1alpha1.InfrastructureTalosCluster. patchObservedTalosVersion
	// patches status on this same object. observedVersion="v1.9.3" is the pre-upgrade value.
	tc := buildReadyManagementCluster("ccs-mgmt", "seam-system", "v1.9.4", "v1.9.3")

	up := &platformv1alpha1.UpgradePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "upgrade-test", Namespace: "seam-system", Generation: 1},
		Spec: platformv1alpha1.UpgradePolicySpec{
			ClusterRef:         platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt", Namespace: "seam-system"},
			UpgradeType:        platformv1alpha1.UpgradeTypeTalos,
			TargetTalosVersion: "v1.9.4",
			RollingStrategy:    platformv1alpha1.RollingStrategySequential,
		},
	}

	// Pre-create the Job and per-cluster TCOR with Succeeded status so the reconciler
	// sees completion. TCOR is named by clusterRef, lives in seam-tenant-{clusterRef}.
	jobName := "upgrade-test-talos-upgrade"
	existingJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "seam-system"},
	}
	resultTCOR := successResultTCOR("ccs-mgmt", jobName)

	// Pre-create a cluster RunnerConfig so the capability gate passes.
	rc := clusterRC("ccs-mgmt", "talos-upgrade")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, up, existingJob, resultTCOR, rc).
		WithStatusSubresource(tc, up).
		Build()
	r := &controller.UpgradePolicyReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: clientevents.NewFakeRecorder(32),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "upgrade-test", Namespace: "seam-system"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// InfrastructureTalosCluster.status.observedTalosVersion must be updated.
	gotTC := &seamcorev1alpha1.InfrastructureTalosCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-mgmt", Namespace: "seam-system",
	}, gotTC); err != nil {
		t.Fatalf("get InfrastructureTalosCluster: %v", err)
	}
	if gotTC.Status.ObservedTalosVersion != "v1.9.4" {
		t.Errorf("ObservedTalosVersion = %q, want v1.9.4", gotTC.Status.ObservedTalosVersion)
	}

	// UpgradePolicy must be Ready=True.
	gotUP := &platformv1alpha1.UpgradePolicy{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "upgrade-test", Namespace: "seam-system",
	}, gotUP); err != nil {
		t.Fatalf("get UpgradePolicy: %v", err)
	}
	readyCond := platformv1alpha1.FindCondition(gotUP.Status.Conditions, platformv1alpha1.ConditionTypeUpgradePolicyReady)
	if readyCond == nil {
		t.Fatal("UpgradePolicy Ready condition not set after completion")
	}
	if readyCond.Status != metav1.ConditionTrue {
		t.Errorf("UpgradePolicy Ready = %s, want True", readyCond.Status)
	}
}

// TestTCOR_RevisionBumpedAfterUpgrade verifies that when an UpgradePolicy for a talos
// upgrade completes, bumpTCORRevision advances the per-cluster TCOR to a new revision
// epoch: Revision increments, TalosVersion updates, and Operations are cleared.
// The TCOR is never deleted — it is a long-lived accumulator for infrastructure memory.
// CONDUCTOR-BL-GRAPHQUERY-ARCHIVE: archive stub phase.
func TestTCOR_RevisionBumpedAfterUpgrade(t *testing.T) {
	scheme := buildDay2Scheme(t)

	up := &platformv1alpha1.UpgradePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "upgrade-rev-test",
			Namespace:  "seam-system",
			Generation: 1,
		},
		Spec: platformv1alpha1.UpgradePolicySpec{
			ClusterRef:         platformv1alpha1.LocalObjectRef{Name: "ccs-mgmt", Namespace: "seam-system"},
			UpgradeType:        platformv1alpha1.UpgradeTypeTalos,
			TargetTalosVersion: "v1.9.4",
		},
	}

	// Pre-create the per-cluster TCOR at seam-tenant-ccs-mgmt/ccs-mgmt with TalosVersion=v1.9.3
	// and a prior operation record. After upgrade completes, revision must advance to 2,
	// TalosVersion must update to v1.9.4, and Operations must be cleared.
	jobName := "upgrade-rev-test-talos-upgrade"
	existingTCOR := successResultTCOR("ccs-mgmt", jobName)

	existingJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "seam-system"},
	}
	tc := buildReadyManagementCluster("ccs-mgmt", "seam-system", "v1.9.4", "v1.9.3")
	rc := clusterRC("ccs-mgmt", "talos-upgrade")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tc, up, existingJob, existingTCOR, rc).
		WithStatusSubresource(tc, up).
		Build()
	r := &controller.UpgradePolicyReconciler{Client: c, Scheme: scheme, Recorder: clientevents.NewFakeRecorder(32)}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "upgrade-rev-test", Namespace: "seam-system"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// TCOR must still exist at seam-tenant-ccs-mgmt/ccs-mgmt — never deleted.
	tcor := &seamcorev1alpha1.InfrastructureTalosClusterOperationResult{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: "ccs-mgmt", Namespace: "seam-tenant-ccs-mgmt",
	}, tcor); err != nil {
		t.Fatalf("TCOR not found after upgrade: %v", err)
	}

	// Revision must advance from 1 to 2.
	if tcor.Spec.Revision != 2 {
		t.Errorf("Revision = %d, want 2", tcor.Spec.Revision)
	}
	// TalosVersion must reflect the upgrade target.
	if tcor.Spec.TalosVersion != "v1.9.4" {
		t.Errorf("TalosVersion = %q, want v1.9.4", tcor.Spec.TalosVersion)
	}
	// Operations cleared — new epoch begins empty.
	if len(tcor.Spec.Operations) != 0 {
		t.Errorf("Operations len = %d after revision bump, want 0", len(tcor.Spec.Operations))
	}
}
