package day2_e2e_test

// AC-DAY2-RESET: ClusterReset end-to-end contract.
//
// Promotion condition: requires live cluster with MGMT_KUBECONFIG set,
// TENANT-CLUSTER-E2E closed, and AC-1 passing.
//
// CP-INV-006: ClusterReset requires ontai.dev/reset-approved=true annotation.
// platform-schema.md §5 ClusterReset.

import (
	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("AC-DAY2-RESET: ClusterReset lifecycle", func() {
	It("without approval annotation, sets PendingApproval and skips RunnerConfig", func() {
		Skip("AC-DAY2-RESET: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("with approval annotation on non-CAPI cluster, submits cluster-reset RunnerConfig", func() {
		Skip("AC-DAY2-RESET: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("with approval on CAPI cluster, deletes CAPI Cluster before submitting RunnerConfig", func() {
		Skip("AC-DAY2-RESET: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("transitions to Ready=True/ResetComplete after RunnerConfig completes", func() {
		Skip("AC-DAY2-RESET: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("transitions to Degraded=True/JobFailed when RunnerConfig fails", func() {
		Skip("AC-DAY2-RESET: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})
})
