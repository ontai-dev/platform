package e2e_test

// Scenario: TalosCluster bootstrap direct path
//
// Pre-conditions required for this test to run:
//   - ccs-mgmt fully provisioned (MGMT_KUBECONFIG set)
//   - Platform operator running in seam-system on ccs-mgmt
//   - guardian webhook in Enforce mode with platform RBACProfile provisioned
//   - 5 VMs for ccs-test booted in Talos maintenance mode, reachable on port 50000
//   - SeamInfrastructureMachine CRs pre-applied for all 5 nodes
//   - Cilium ClusterPack compiled and available in the registry
//   - seam-tenant-ccs-test namespace exists (Platform creates on first reconcile)
//
// What this test verifies (platform-schema.md §2, session/30 WS2):
//   - TalosCluster CR creation triggers CAPI object creation
//     (SeamInfrastructureCluster, Cluster, TalosControlPlane, MachineDeployment)
//   - SeamInfrastructureProvider delivers machineconfigs to nodes on port 50000
//   - CAPI Cluster reaches Running status
//   - Conductor Deployment is created in ont-system on ccs-test
//   - TalosCluster ConductorReady condition becomes True (session/36 WS1)

import (
	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("TalosCluster bootstrap direct path", func() {
	It("TalosCluster CR creation spawns CAPI objects in seam-tenant-ccs-test", func() {
		Skip("requires bootstrapped tenant cluster (mode=bootstrap, not mode=import) and 5 VMs in Talos maintenance mode — PLATFORM-BL-BOOTSTRAP-E2E")
	})

	It("SeamInfrastructureProvider delivers machineconfig to control plane nodes on port 50000", func() {
		Skip("requires bootstrapped tenant cluster (mode=bootstrap, not mode=import) and 5 VMs in Talos maintenance mode — PLATFORM-BL-BOOTSTRAP-E2E")
	})

	It("CAPI Cluster ccs-test reaches Running phase", func() {
		Skip("requires bootstrapped tenant cluster (mode=bootstrap, not mode=import) and 5 VMs in Talos maintenance mode — PLATFORM-BL-BOOTSTRAP-E2E")
	})

	It("Conductor Deployment is created in ont-system on ccs-test cluster", func() {
		Skip("requires bootstrapped tenant cluster (mode=bootstrap, not mode=import) and 5 VMs in Talos maintenance mode — PLATFORM-BL-BOOTSTRAP-E2E")
	})

	It("TalosCluster ConductorReady condition transitions to True after Conductor is Available", func() {
		Skip("requires bootstrapped tenant cluster (mode=bootstrap, not mode=import) and 5 VMs in Talos maintenance mode — PLATFORM-BL-BOOTSTRAP-E2E")
	})
})
