package day2_e2e_test

// AC-DAY2-NODEOP: NodeOperation end-to-end contract.
//
// Promotion condition: requires live cluster with MGMT_KUBECONFIG set,
// TENANT-CLUSTER-E2E closed, and AC-1 passing.
//
// platform-schema.md §5 NodeOperation.

import (
	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("AC-DAY2-NODEOP: NodeOperation lifecycle", func() {
	It("scale-up on non-CAPI cluster submits node-scale-up RunnerConfig and reaches Ready=True", func() {
		Skip("AC-DAY2-NODEOP: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("reboot on non-CAPI cluster submits node-reboot RunnerConfig", func() {
		Skip("AC-DAY2-NODEOP: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("decommission on non-CAPI cluster submits node-decommission RunnerConfig", func() {
		Skip("AC-DAY2-NODEOP: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("scale-up on CAPI cluster updates MachineDeployment replicas and sets CAPIDelegated=True", func() {
		Skip("AC-DAY2-NODEOP: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("transitions to Degraded=True when RunnerConfig fails", func() {
		Skip("AC-DAY2-NODEOP: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})
})
