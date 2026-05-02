package day2_e2e_test

// hardeningprofile_e2e_test.go -- Live HardeningProfile end-to-end tests.
//
// Covers the full flow from HardeningProfile CR creation through NodeMaintenance
// Job submission to Ready=True for both bootstrap and import cluster modes:
//
//   MGMT-HP-PROFILE  -- HardeningProfile Valid=True on ccs-mgmt (mode=bootstrap)
//   MGMT-HP-CLUSTER  -- Full-cluster hardening on ccs-mgmt reaches Ready=True
//   MGMT-HP-NODE     -- Single-node hardening on ccs-mgmt worker reaches Ready=True
//   TENANT-HP-PROFILE -- HardeningProfile Valid=True on TENANT_CLUSTER_NAME (mode=import)
//   TENANT-HP-CLUSTER -- Full-cluster hardening on TENANT_CLUSTER_NAME reaches Ready=True
//   TENANT-HP-NODE   -- Single-node hardening on TENANT_WORKER_NODE reaches Ready=True
//                       (skips when TENANT_WORKER_NODE is not set)
//
// The machine config overlay used in all tests is a safe, idempotent sysctl
// setting (net.ipv4.ip_forward=1) that is already set on all Kubernetes nodes
// and does not require a reboot or restart any service.
//
// Required environment variables:
//   MGMT_KUBECONFIG      -- path to management cluster kubeconfig (all tests skip if absent)
//   TENANT_CLUSTER_NAME  -- name of the import-mode tenant cluster (default: ccs-dev)
//   TENANT_WORKER_NODE   -- name of a specific worker node in the tenant cluster (optional)
//
// platform-schema.md §5 HardeningProfile, NodeMaintenance.

import (
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

// safeHardeningPatch is an idempotent Talos machineconfig overlay used in all
// live hardening tests. It sets net.ipv4.ip_forward=1 which is already enforced
// on all Kubernetes nodes (required for pod networking). Applying it again in
// no-reboot mode causes no disruption and no restart of any service.
const safeHardeningPatch = `machine:
  sysctls:
    net.ipv4.ip_forward: "1"
    net.ipv4.conf.all.rp_filter: "1"
`

// tenantClusterName returns the import-mode tenant cluster name from the
// environment, defaulting to ccs-dev.
func tenantClusterName() string {
	if v := os.Getenv("TENANT_CLUSTER_NAME"); v != "" {
		return v
	}
	return "ccs-dev"
}

// tenantWorkerNode returns the worker node name for the tenant cluster, or empty
// string when TENANT_WORKER_NODE is not set (which causes single-node tests to skip).
func tenantWorkerNode() string {
	return os.Getenv("TENANT_WORKER_NODE")
}

// ── Bootstrap cluster (ccs-mgmt, mode=bootstrap) ────────────────────────────

var _ = Describe("MGMT-HP-PROFILE: HardeningProfile validation on bootstrap cluster", func() {
	It("HardeningProfile reaches Valid=True in seam-tenant-ccs-mgmt", func() {
		crName := fmt.Sprintf("hp-base-%d", time.Now().UnixNano())
		hp := &platformv1alpha1.HardeningProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:      crName,
				Namespace: mgmtDay2NS,
			},
			Spec: platformv1alpha1.HardeningProfileSpec{
				Description:         "Seam baseline hardening profile -- bootstrap cluster test",
				MachineConfigPatches: []string{safeHardeningPatch},
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, hp)).To(Succeed())
		DeferCleanup(func() {
			_ = mgmtClient.Delete(mgmtCtx, hp)
		})

		Eventually(func(g Gomega) {
			got := &platformv1alpha1.HardeningProfile{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: crName, Namespace: mgmtDay2NS,
			}, got)).To(Succeed())
			cond := platformv1alpha1.FindCondition(got.Status.Conditions,
				platformv1alpha1.ConditionTypeHardeningProfileValid)
			g.Expect(cond).NotTo(BeNil(), "Valid condition not set on HardeningProfile")
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue),
				"HardeningProfile Valid condition must be True")
		}, jobSubmitTimeout, pollInterval).Should(Succeed())
	})
})

var _ = Describe("MGMT-HP-CLUSTER: Full-cluster hardening on bootstrap cluster", func() {
	It("NodeMaintenance hardening-apply with no targetNodes reaches Ready=True on ccs-mgmt", func() {
		hpName := fmt.Sprintf("hp-full-%d", time.Now().UnixNano())
		nmName := fmt.Sprintf("nm-hp-full-%d", time.Now().UnixNano())

		hp := &platformv1alpha1.HardeningProfile{
			ObjectMeta: metav1.ObjectMeta{Name: hpName, Namespace: mgmtDay2NS},
			Spec: platformv1alpha1.HardeningProfileSpec{
				Description:         "Full-cluster hardening test (bootstrap)",
				MachineConfigPatches: []string{safeHardeningPatch},
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, hp)).To(Succeed())
		DeferCleanup(func() { _ = mgmtClient.Delete(mgmtCtx, hp) })

		nm := &platformv1alpha1.NodeMaintenance{
			ObjectMeta: metav1.ObjectMeta{Name: nmName, Namespace: mgmtDay2NS},
			Spec: platformv1alpha1.NodeMaintenanceSpec{
				ClusterRef: mgmtClusterRef(),
				Operation:  platformv1alpha1.NodeMaintenanceOperationHardeningApply,
				HardeningProfileRef: &platformv1alpha1.LocalObjectRef{
					Name:      hpName,
					Namespace: mgmtDay2NS,
				},
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, nm)).To(Succeed())
		DeferCleanup(func() {
			_ = mgmtClient.Delete(mgmtCtx, nm, client.GracePeriodSeconds(0))
		})

		// Wait for the executor Job to be submitted.
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.NodeMaintenance{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: nmName, Namespace: mgmtDay2NS,
			}, got)).To(Succeed())
			g.Expect(got.Status.JobName).NotTo(BeEmpty(),
				"JobName not set -- hardening-apply Job not yet submitted")
		}, jobSubmitTimeout, pollInterval).Should(Succeed())

		// Wait for Ready=True -- hardening-apply in no-reboot mode completes quickly.
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.NodeMaintenance{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: nmName, Namespace: mgmtDay2NS,
			}, got)).To(Succeed())
			cond := platformv1alpha1.FindCondition(got.Status.Conditions,
				platformv1alpha1.ConditionTypeNodeMaintenanceReady)
			g.Expect(cond).NotTo(BeNil(), "Ready condition not set")
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue),
				"NodeMaintenance Ready must be True after hardening-apply")
		}, operationCompleteTimeout, pollInterval).Should(Succeed())
	})
})

var _ = Describe("MGMT-HP-NODE: Single-node hardening on bootstrap cluster", func() {
	It("NodeMaintenance hardening-apply targeting ccs-mgmt-w2 reaches Ready=True", func() {
		hpName := fmt.Sprintf("hp-node-%d", time.Now().UnixNano())
		nmName := fmt.Sprintf("nm-hp-node-%d", time.Now().UnixNano())

		hp := &platformv1alpha1.HardeningProfile{
			ObjectMeta: metav1.ObjectMeta{Name: hpName, Namespace: mgmtDay2NS},
			Spec: platformv1alpha1.HardeningProfileSpec{
				Description:         "Single-node hardening test (bootstrap)",
				MachineConfigPatches: []string{safeHardeningPatch},
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, hp)).To(Succeed())
		DeferCleanup(func() { _ = mgmtClient.Delete(mgmtCtx, hp) })

		nm := &platformv1alpha1.NodeMaintenance{
			ObjectMeta: metav1.ObjectMeta{Name: nmName, Namespace: mgmtDay2NS},
			Spec: platformv1alpha1.NodeMaintenanceSpec{
				ClusterRef:  mgmtClusterRef(),
				Operation:   platformv1alpha1.NodeMaintenanceOperationHardeningApply,
				TargetNodes: []string{mgmtWorkerNode},
				HardeningProfileRef: &platformv1alpha1.LocalObjectRef{
					Name:      hpName,
					Namespace: mgmtDay2NS,
				},
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, nm)).To(Succeed())
		DeferCleanup(func() {
			_ = mgmtClient.Delete(mgmtCtx, nm, client.GracePeriodSeconds(0))
		})

		// Wait for Job submission.
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.NodeMaintenance{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: nmName, Namespace: mgmtDay2NS,
			}, got)).To(Succeed())
			g.Expect(got.Status.JobName).NotTo(BeEmpty(),
				"JobName not set -- single-node hardening Job not yet submitted")
		}, jobSubmitTimeout, pollInterval).Should(Succeed())

		// Wait for Ready=True.
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.NodeMaintenance{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: nmName, Namespace: mgmtDay2NS,
			}, got)).To(Succeed())
			cond := platformv1alpha1.FindCondition(got.Status.Conditions,
				platformv1alpha1.ConditionTypeNodeMaintenanceReady)
			g.Expect(cond).NotTo(BeNil(), "Ready condition not set")
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		}, operationCompleteTimeout, pollInterval).Should(Succeed())
	})
})

// ── Import-mode tenant cluster (ccs-dev, mode=import) ────────────────────────

var _ = Describe("TENANT-HP-PROFILE: HardeningProfile validation on import-mode cluster", func() {
	It("HardeningProfile reaches Valid=True in seam-tenant-{TENANT_CLUSTER_NAME}", func() {
		cluster := tenantClusterName()
		tenantNS := "seam-tenant-" + cluster

		crName := fmt.Sprintf("hp-base-import-%d", time.Now().UnixNano())
		hp := &platformv1alpha1.HardeningProfile{
			ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: tenantNS},
			Spec: platformv1alpha1.HardeningProfileSpec{
				Description:         "Seam baseline hardening profile -- import cluster test",
				MachineConfigPatches: []string{safeHardeningPatch},
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, hp)).To(Succeed())
		DeferCleanup(func() { _ = mgmtClient.Delete(mgmtCtx, hp) })

		Eventually(func(g Gomega) {
			got := &platformv1alpha1.HardeningProfile{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: crName, Namespace: tenantNS,
			}, got)).To(Succeed())
			cond := platformv1alpha1.FindCondition(got.Status.Conditions,
				platformv1alpha1.ConditionTypeHardeningProfileValid)
			g.Expect(cond).NotTo(BeNil(), "Valid condition not set")
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		}, jobSubmitTimeout, pollInterval).Should(Succeed())
	})
})

var _ = Describe("TENANT-HP-CLUSTER: Full-cluster hardening on import-mode cluster", func() {
	It("NodeMaintenance hardening-apply with no targetNodes reaches Ready=True on TENANT_CLUSTER_NAME", func() {
		cluster := tenantClusterName()
		tenantNS := "seam-tenant-" + cluster
		tenantRef := platformv1alpha1.LocalObjectRef{Name: cluster, Namespace: "seam-system"}

		hpName := fmt.Sprintf("hp-import-full-%d", time.Now().UnixNano())
		nmName := fmt.Sprintf("nm-import-full-%d", time.Now().UnixNano())

		hp := &platformv1alpha1.HardeningProfile{
			ObjectMeta: metav1.ObjectMeta{Name: hpName, Namespace: tenantNS},
			Spec: platformv1alpha1.HardeningProfileSpec{
				Description:         "Full-cluster hardening test (import)",
				MachineConfigPatches: []string{safeHardeningPatch},
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, hp)).To(Succeed())
		DeferCleanup(func() { _ = mgmtClient.Delete(mgmtCtx, hp) })

		nm := &platformv1alpha1.NodeMaintenance{
			ObjectMeta: metav1.ObjectMeta{Name: nmName, Namespace: tenantNS},
			Spec: platformv1alpha1.NodeMaintenanceSpec{
				ClusterRef: tenantRef,
				Operation:  platformv1alpha1.NodeMaintenanceOperationHardeningApply,
				HardeningProfileRef: &platformv1alpha1.LocalObjectRef{
					Name:      hpName,
					Namespace: tenantNS,
				},
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, nm)).To(Succeed())
		DeferCleanup(func() {
			_ = mgmtClient.Delete(mgmtCtx, nm, client.GracePeriodSeconds(0))
		})

		// Wait for Job submission.
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.NodeMaintenance{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: nmName, Namespace: tenantNS,
			}, got)).To(Succeed())
			g.Expect(got.Status.JobName).NotTo(BeEmpty(),
				"JobName not set -- full-cluster hardening Job not yet submitted on import cluster")
		}, jobSubmitTimeout, pollInterval).Should(Succeed())

		// Wait for Ready=True.
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.NodeMaintenance{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: nmName, Namespace: tenantNS,
			}, got)).To(Succeed())
			cond := platformv1alpha1.FindCondition(got.Status.Conditions,
				platformv1alpha1.ConditionTypeNodeMaintenanceReady)
			g.Expect(cond).NotTo(BeNil(), "Ready condition not set")
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue),
				"NodeMaintenance Ready must be True after hardening-apply on import cluster")
		}, operationCompleteTimeout, pollInterval).Should(Succeed())
	})
})

var _ = Describe("TENANT-HP-NODE: Single-node hardening on import-mode cluster", func() {
	It("NodeMaintenance hardening-apply targeting TENANT_WORKER_NODE reaches Ready=True", func() {
		node := tenantWorkerNode()
		if node == "" {
			Skip("TENANT-HP-NODE: requires TENANT_WORKER_NODE set to a worker node name on the tenant cluster")
		}

		cluster := tenantClusterName()
		tenantNS := "seam-tenant-" + cluster
		tenantRef := platformv1alpha1.LocalObjectRef{Name: cluster, Namespace: "seam-system"}

		hpName := fmt.Sprintf("hp-import-node-%d", time.Now().UnixNano())
		nmName := fmt.Sprintf("nm-import-node-%d", time.Now().UnixNano())

		hp := &platformv1alpha1.HardeningProfile{
			ObjectMeta: metav1.ObjectMeta{Name: hpName, Namespace: tenantNS},
			Spec: platformv1alpha1.HardeningProfileSpec{
				Description:         "Single-node hardening test (import)",
				MachineConfigPatches: []string{safeHardeningPatch},
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, hp)).To(Succeed())
		DeferCleanup(func() { _ = mgmtClient.Delete(mgmtCtx, hp) })

		nm := &platformv1alpha1.NodeMaintenance{
			ObjectMeta: metav1.ObjectMeta{Name: nmName, Namespace: tenantNS},
			Spec: platformv1alpha1.NodeMaintenanceSpec{
				ClusterRef:  tenantRef,
				Operation:   platformv1alpha1.NodeMaintenanceOperationHardeningApply,
				TargetNodes: []string{node},
				HardeningProfileRef: &platformv1alpha1.LocalObjectRef{
					Name:      hpName,
					Namespace: tenantNS,
				},
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, nm)).To(Succeed())
		DeferCleanup(func() {
			_ = mgmtClient.Delete(mgmtCtx, nm, client.GracePeriodSeconds(0))
		})

		// Wait for Job submission.
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.NodeMaintenance{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: nmName, Namespace: tenantNS,
			}, got)).To(Succeed())
			g.Expect(got.Status.JobName).NotTo(BeEmpty(),
				fmt.Sprintf("JobName not set -- single-node hardening Job not yet submitted (node=%s)", node))
		}, jobSubmitTimeout, pollInterval).Should(Succeed())

		// Wait for Ready=True.
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.NodeMaintenance{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: nmName, Namespace: tenantNS,
			}, got)).To(Succeed())
			cond := platformv1alpha1.FindCondition(got.Status.Conditions,
				platformv1alpha1.ConditionTypeNodeMaintenanceReady)
			g.Expect(cond).NotTo(BeNil(), "Ready condition not set")
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		}, operationCompleteTimeout, pollInterval).Should(Succeed())
	})
})
