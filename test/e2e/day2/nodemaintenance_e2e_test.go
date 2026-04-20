package day2_e2e_test

// AC-DAY2-NODE: NodeMaintenance end-to-end contract.
//
// Promotion condition: requires live cluster with MGMT_KUBECONFIG set,
// TENANT-CLUSTER-E2E closed, and AC-1 passing.
//
// platform-schema.md §5 NodeMaintenance.

import (
	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("AC-DAY2-NODE: NodeMaintenance lifecycle", func() {
	It("patch operation submits 4-step cordon/drain/patch/uncordon RunnerConfig and reaches Ready=True", func() {
		Skip("AC-DAY2-NODE: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("hardening-apply operation uses hardening-apply as operate step", func() {
		Skip("AC-DAY2-NODE: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("credential-rotate operation uses credential-rotate as operate step", func() {
		Skip("AC-DAY2-NODE: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("cordon step failure halts the sequence (HaltOnFailure=true enforced)", func() {
		Skip("AC-DAY2-NODE: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("second reconcile on Ready=True NodeMaintenance is a no-op", func() {
		Skip("AC-DAY2-NODE: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})
})
