package day2_e2e_test

// AC-DAY2-UPGRADE: UpgradePolicy end-to-end contract.
//
// Promotion condition: requires live cluster with MGMT_KUBECONFIG set,
// TENANT-CLUSTER-E2E closed, and AC-1 passing.
//
// platform-schema.md §5 UpgradePolicy.

import (
	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("AC-DAY2-UPGRADE: UpgradePolicy lifecycle", func() {
	It("talos-upgrade on non-CAPI cluster submits single-step RunnerConfig and reaches Ready=True", func() {
		Skip("AC-DAY2-UPGRADE: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("kube-upgrade on non-CAPI cluster submits kube-upgrade RunnerConfig", func() {
		Skip("AC-DAY2-UPGRADE: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("stack-upgrade on non-CAPI cluster submits 2-step talos-upgrade+kube-upgrade RunnerConfig", func() {
		Skip("AC-DAY2-UPGRADE: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("upgrade on CAPI cluster sets CAPIDelegated=True and patches CAPI objects", func() {
		Skip("AC-DAY2-UPGRADE: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("transitions to Degraded=True when RunnerConfig fails", func() {
		Skip("AC-DAY2-UPGRADE: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})
})
