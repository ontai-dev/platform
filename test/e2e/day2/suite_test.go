// Package day2_e2e_test contains end-to-end test stubs for the platform day-2
// operational CRD reconcilers.
//
// All specs skip until TENANT-CLUSTER-E2E and AC-1 are closed. Promotion happens
// in the cluster verification session, not here.
//
// Required environment variables:
//   - MGMT_KUBECONFIG  — path to management cluster kubeconfig
//   - TENANT_KUBECONFIG — path to tenant cluster kubeconfig
//
// Run with: make e2e
// Skip condition: MGMT_KUBECONFIG absent → all specs skip.
package day2_e2e_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDay2E2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "platform day-2 E2E Suite")
}

var _ = BeforeSuite(func() {
	if os.Getenv("MGMT_KUBECONFIG") == "" {
		Skip("MGMT_KUBECONFIG not set — tests require a live cluster")
	}
})
