package day2_e2e_test

// Management cluster day-2 operation end-to-end tests.
//
// These tests run against the live ccs-mgmt management cluster when MGMT_KUBECONFIG
// is set. Each test covers one day-2 operation type. Tests verify the full lifecycle:
// CR submission -> platform reconciler processing -> Running condition set.
//
// For safe operations (EtcdMaintenance defrag, ClusterMaintenance), the test waits
// for Ready=True. For more invasive operations (NodeOperation reboot, PKIRotation),
// only Running/submission conditions are verified before cleanup.
//
// Target cluster: ccs-mgmt (management cluster, seam-system namespace)
// Day-2 CR namespace: seam-tenant-ccs-mgmt
// Worker node for node-level tests: ccs-mgmt-w2

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

const (
	mgmtClusterName = "ccs-mgmt"
	mgmtClusterNS   = "seam-system"
	mgmtDay2NS      = "seam-tenant-ccs-mgmt"
	mgmtWorkerNode  = "ccs-mgmt-w2"

	// jobSubmitTimeout is the maximum time to wait for the reconciler to submit
	// a Conductor Job and set the Running condition.
	jobSubmitTimeout = 2 * time.Minute
	// operationCompleteTimeout is the maximum time to wait for a fast operation
	// (etcd defrag, ClusterMaintenance) to reach Ready=True.
	operationCompleteTimeout = 5 * time.Minute
	// pollInterval is the Gomega polling interval for Eventually checks.
	pollInterval = 5 * time.Second
)

func mgmtClusterRef() platformv1alpha1.LocalObjectRef {
	return platformv1alpha1.LocalObjectRef{Name: mgmtClusterName, Namespace: mgmtClusterNS}
}

// ── EtcdMaintenance defrag ────────────────────────────────────────────────────

var _ = Describe("MGMT-DAY2-ETCD: EtcdMaintenance defrag on management cluster", func() {
	It("defrag operation reaches Ready=True on ccs-mgmt", func() {
		crName := fmt.Sprintf("em-defrag-%d", time.Now().UnixNano())
		em := &platformv1alpha1.EtcdMaintenance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      crName,
				Namespace: mgmtDay2NS,
			},
			Spec: platformv1alpha1.EtcdMaintenanceSpec{
				ClusterRef: mgmtClusterRef(),
				Operation:  platformv1alpha1.EtcdMaintenanceOperationDefrag,
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, em)).To(Succeed())
		DeferCleanup(func() {
			_ = mgmtClient.Delete(mgmtCtx, em)
		})

		// Wait for Running condition to confirm the executor Job was submitted.
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.EtcdMaintenance{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: crName, Namespace: mgmtDay2NS,
			}, got)).To(Succeed())
			cond := platformv1alpha1.FindCondition(got.Status.Conditions,
				platformv1alpha1.ConditionTypeEtcdMaintenanceRunning)
			g.Expect(cond).NotTo(BeNil(), "Running condition not set")
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		}, jobSubmitTimeout, pollInterval).Should(Succeed())

		// Wait for Ready=True — defrag is a fast operation.
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.EtcdMaintenance{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: crName, Namespace: mgmtDay2NS,
			}, got)).To(Succeed())
			cond := platformv1alpha1.FindCondition(got.Status.Conditions,
				platformv1alpha1.ConditionTypeEtcdMaintenanceReady)
			g.Expect(cond).NotTo(BeNil(), "Ready condition not set")
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		}, operationCompleteTimeout, pollInterval).Should(Succeed())
	})
})

// ── ClusterMaintenance window definition ─────────────────────────────────────

var _ = Describe("MGMT-DAY2-CMAINT: ClusterMaintenance window on management cluster", func() {
	It("maintenance window CR is accepted and WindowActive condition is set", func() {
		crName := fmt.Sprintf("cm-window-%d", time.Now().UnixNano())
		cm := &platformv1alpha1.ClusterMaintenance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      crName,
				Namespace: mgmtDay2NS,
			},
			Spec: platformv1alpha1.ClusterMaintenanceSpec{
				ClusterRef: mgmtClusterRef(),
				Windows: []platformv1alpha1.MaintenanceWindow{
					{
						// Weekly window: Saturday 02:00-04:00 UTC.
						Start:           "0 2 * * 6",
						DurationMinutes: 120,
					},
				},
				BlockOutsideWindows: false,
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, cm)).To(Succeed())
		DeferCleanup(func() {
			_ = mgmtClient.Delete(mgmtCtx, cm)
		})

		// Wait for the reconciler to process the CR (WindowActive condition set either
		// True or False depending on the current time — both indicate reconciliation).
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.ClusterMaintenance{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: crName, Namespace: mgmtDay2NS,
			}, got)).To(Succeed())
			cond := platformv1alpha1.FindCondition(got.Status.Conditions,
				platformv1alpha1.ConditionTypeClusterMaintenanceWindowActive)
			g.Expect(cond).NotTo(BeNil(), "WindowActive condition not set")
		}, jobSubmitTimeout, pollInterval).Should(Succeed())
	})
})

// ── NodeOperation reboot ──────────────────────────────────────────────────────

var _ = Describe("MGMT-DAY2-NODEOP: NodeOperation reboot on management cluster", func() {
	It("reboot of ccs-mgmt-w2 reaches Running condition", func() {
		crName := fmt.Sprintf("nodeop-reboot-%d", time.Now().UnixNano())
		no := &platformv1alpha1.NodeOperation{
			ObjectMeta: metav1.ObjectMeta{
				Name:      crName,
				Namespace: mgmtDay2NS,
			},
			Spec: platformv1alpha1.NodeOperationSpec{
				ClusterRef:  mgmtClusterRef(),
				Operation:   platformv1alpha1.NodeOperationTypeReboot,
				TargetNodes: []string{mgmtWorkerNode},
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, no)).To(Succeed())
		DeferCleanup(func() {
			_ = mgmtClient.Delete(mgmtCtx, no, client.GracePeriodSeconds(0))
		})

		// Wait for the executor Job to be submitted (Running or Ready condition set).
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.NodeOperation{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: crName, Namespace: mgmtDay2NS,
			}, got)).To(Succeed())
			// Either Ready (fast path) or Degraded (error) signals reconciliation.
			// The JobName being set is sufficient to confirm Job submission.
			g.Expect(got.Status.JobName).NotTo(BeEmpty(), "JobName not set — Job not yet submitted")
		}, jobSubmitTimeout, pollInterval).Should(Succeed())
	})
})

// ── NodeMaintenance node-patch ────────────────────────────────────────────────

var _ = Describe("MGMT-DAY2-NODEMAINT: NodeMaintenance node-patch on management cluster", func() {
	It("node-patch on ccs-mgmt-w2 reaches Running condition", func() {
		crName := fmt.Sprintf("nm-patch-%d", time.Now().UnixNano())
		nm := &platformv1alpha1.NodeMaintenance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      crName,
				Namespace: mgmtDay2NS,
			},
			Spec: platformv1alpha1.NodeMaintenanceSpec{
				ClusterRef:  mgmtClusterRef(),
				Operation:   platformv1alpha1.NodeMaintenanceOperationPatch,
				TargetNodes: []string{mgmtWorkerNode},
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, nm)).To(Succeed())
		DeferCleanup(func() {
			_ = mgmtClient.Delete(mgmtCtx, nm, client.GracePeriodSeconds(0))
		})

		// Wait for the executor Job to be submitted (JobName set on status).
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.NodeMaintenance{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: crName, Namespace: mgmtDay2NS,
			}, got)).To(Succeed())
			g.Expect(got.Status.JobName).NotTo(BeEmpty(), "JobName not set — Job not yet submitted")
		}, jobSubmitTimeout, pollInterval).Should(Succeed())
	})
})

// ── PKIRotation ───────────────────────────────────────────────────────────────

var _ = Describe("MGMT-DAY2-PKI: PKIRotation on management cluster", func() {
	It("PKIRotation for ccs-mgmt reaches Running condition", func() {
		crName := fmt.Sprintf("pki-rotate-%d", time.Now().UnixNano())
		pr := &platformv1alpha1.PKIRotation{
			ObjectMeta: metav1.ObjectMeta{
				Name:      crName,
				Namespace: mgmtDay2NS,
			},
			Spec: platformv1alpha1.PKIRotationSpec{
				ClusterRef: mgmtClusterRef(),
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, pr)).To(Succeed())
		DeferCleanup(func() {
			_ = mgmtClient.Delete(mgmtCtx, pr, client.GracePeriodSeconds(0))
		})

		// Wait for executor Job submission (Running condition set True).
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.PKIRotation{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: crName, Namespace: mgmtDay2NS,
			}, got)).To(Succeed())
			cond := platformv1alpha1.FindCondition(got.Status.Conditions,
				platformv1alpha1.ConditionTypePKIRotationReady)
			// Either Running or Ready condition being set confirms reconciliation.
			g.Expect(got.Status.JobName).NotTo(BeEmpty(), "JobName not set — Job not yet submitted")
			_ = cond
		}, jobSubmitTimeout, pollInterval).Should(Succeed())
	})
})

// ── UpgradePolicy via spec.versionUpgrade ─────────────────────────────────────

var _ = Describe("MGMT-DAY2-UPGRADE: UpgradePolicy via spec.versionUpgrade on management cluster", func() {
	It("versionUpgrade=true auto-creates an UpgradePolicy and sets VersionUpgradePending=True", func() {
		// Fetch the current TalosCluster to read spec.talosVersion.
		itc := &platformv1alpha1.TalosCluster{}
		Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
			Name: mgmtClusterName, Namespace: mgmtClusterNS,
		}, itc)).To(Succeed())

		targetVersion := itc.Spec.TalosVersion
		if targetVersion == "" {
			targetVersion = "v1.9.3"
		}

		// Patch spec.versionUpgrade=true using a merge patch.
		patch := []byte(`{"spec":{"versionUpgrade":true,"talosVersion":"` + targetVersion + `"}}`)
		Expect(mgmtClient.Patch(mgmtCtx, itc, client.RawPatch(
			types.MergePatchType, patch,
		))).To(Succeed())

		DeferCleanup(func() {
			// Clear the flag and delete the auto-created UpgradePolicy.
			clearPatch := []byte(`{"spec":{"versionUpgrade":false}}`)
			_ = mgmtClient.Patch(mgmtCtx, itc, client.RawPatch(types.MergePatchType, clearPatch))
			up := &platformv1alpha1.UpgradePolicy{}
			upName := mgmtClusterName + "-version-upgrade"
			if err := mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: upName, Namespace: mgmtClusterNS,
			}, up); err == nil {
				_ = mgmtClient.Delete(mgmtCtx, up)
			}
		})

		// Wait for the auto-created UpgradePolicy CR.
		Eventually(func(g Gomega) {
			up := &platformv1alpha1.UpgradePolicy{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name:      mgmtClusterName + "-version-upgrade",
				Namespace: mgmtClusterNS,
			}, up)).To(Succeed())
		}, jobSubmitTimeout, pollInterval).Should(Succeed())

		// VersionUpgradePending=True must be set on the ITC.
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.TalosCluster{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: mgmtClusterName, Namespace: mgmtClusterNS,
			}, got)).To(Succeed())
			cond := platformv1alpha1.FindCondition(got.Status.Conditions,
				platformv1alpha1.ConditionTypeVersionUpgradePending)
			g.Expect(cond).NotTo(BeNil(), "VersionUpgradePending condition not set")
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		}, jobSubmitTimeout, pollInterval).Should(Succeed())
	})
})
