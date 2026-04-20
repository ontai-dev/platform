package day2_e2e_test

// AC-DAY2-PKI: PKIRotation end-to-end contract.
//
// Promotion condition: requires live cluster with MGMT_KUBECONFIG set,
// TENANT-CLUSTER-E2E closed, and AC-1 passing.
//
// platform-schema.md §5 PKIRotation.

import (
	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("AC-DAY2-PKI: PKIRotation lifecycle", func() {
	It("submits pki-rotate RunnerConfig and transitions to Ready=False/JobSubmitted", func() {
		Skip("AC-DAY2-PKI: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("transitions to Ready=True/JobComplete after RunnerConfig completes", func() {
		Skip("AC-DAY2-PKI: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("transitions to Degraded=True/JobFailed when RunnerConfig fails", func() {
		Skip("AC-DAY2-PKI: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})

	It("second reconcile on Ready=True PKIRotation is a no-op", func() {
		Skip("AC-DAY2-PKI: requires live cluster with MGMT_KUBECONFIG set and TENANT-CLUSTER-E2E closed")
	})
})
