package e2e_test

// AC-1: Management cluster import acceptance contract.
//
// Scenario: The ccs-mgmt TalosCluster CR is applied in seam-system with
// mode=import and capi.enabled=false. Platform must:
//   - Set status.origin=imported
//   - Create exactly one RunnerConfig in ont-system
//   - Set Ready=True
//   - Complete within 60 seconds
//
// Promotion condition: requires live cluster with MGMT_KUBECONFIG set.
// All specs skip when MGMT_KUBECONFIG is absent (enforced in BeforeSuite).
//
// platform-schema.md §5 TalosClusterModeImport.

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var _ = Describe("AC-1: management cluster import", func() {
	const (
		clusterNS    = "seam-system"
		runnerNS     = "ont-system"
		pollInterval = 2 * time.Second
		pollTimeout  = 60 * time.Second
	)

	It("TalosCluster ccs-mgmt reaches Ready=True within 60s after import", func() {
		Skip("requires live cluster with MGMT_KUBECONFIG set and AC-1 manually triggered")

		ctx := context.Background()
		tcGVR := schema.GroupVersionResource{
			Group:    "platform.ontai.dev",
			Version:  "v1alpha1",
			Resource: "talosclusters",
		}

		start := time.Now()
		Eventually(func() bool {
			obj, err := mgmtClient.Dynamic.Resource(tcGVR).Namespace(clusterNS).
				Get(ctx, mgmtClusterName, metav1.GetOptions{})
			if err != nil {
				return false
			}
			conditions, ok := obj.Object["status"].(map[string]interface{})["conditions"].([]interface{})
			if !ok {
				return false
			}
			for _, c := range conditions {
				cm, ok := c.(map[string]interface{})
				if !ok {
					continue
				}
				if cm["type"] == "Ready" && cm["status"] == "True" {
					return true
				}
			}
			return false
		}, pollTimeout, pollInterval).Should(BeTrue(),
			"TalosCluster %s did not reach Ready=True within %s", mgmtClusterName, pollTimeout)

		Expect(time.Since(start)).To(BeNumerically("<", pollTimeout),
			"import took longer than 60s")
	})

	It("exactly one RunnerConfig exists in ont-system for ccs-mgmt", func() {
		Skip("requires live cluster with MGMT_KUBECONFIG set and AC-1 manually triggered")

		ctx := context.Background()
		rcGVR := schema.GroupVersionResource{
			Group:    "runner.ontai.dev",
			Version:  "v1alpha1",
			Resource: "runnerconfigs",
		}

		list, err := mgmtClient.Dynamic.Resource(rcGVR).Namespace(runnerNS).
			List(ctx, metav1.ListOptions{
				LabelSelector: "platform.ontai.dev/cluster=" + mgmtClusterName,
			})
		Expect(err).NotTo(HaveOccurred(), "list RunnerConfigs in ont-system")
		Expect(list.Items).To(HaveLen(1),
			"expected exactly 1 RunnerConfig for %s in ont-system, got %d",
			mgmtClusterName, len(list.Items))
	})

	It("no duplicate RunnerConfig exists after repeated reconciliation", func() {
		Skip("requires live cluster with MGMT_KUBECONFIG set and AC-1 manually triggered")

		ctx := context.Background()
		rcGVR := schema.GroupVersionResource{
			Group:    "runner.ontai.dev",
			Version:  "v1alpha1",
			Resource: "runnerconfigs",
		}

		// Wait briefly for any duplicate to appear if idempotency is broken.
		time.Sleep(5 * time.Second)

		list, err := mgmtClient.Dynamic.Resource(rcGVR).Namespace(runnerNS).
			List(ctx, metav1.ListOptions{
				LabelSelector: "platform.ontai.dev/cluster=" + mgmtClusterName,
			})
		Expect(err).NotTo(HaveOccurred())
		Expect(list.Items).To(HaveLen(1),
			"idempotency violation: more than one RunnerConfig for %s in ont-system", mgmtClusterName)
	})

	It("status.origin is imported", func() {
		Skip("requires live cluster with MGMT_KUBECONFIG set and AC-1 manually triggered")

		ctx := context.Background()
		tcGVR := schema.GroupVersionResource{
			Group:    "platform.ontai.dev",
			Version:  "v1alpha1",
			Resource: "talosclusters",
		}

		obj, err := mgmtClient.Dynamic.Resource(tcGVR).Namespace(clusterNS).
			Get(ctx, mgmtClusterName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())

		status, ok := obj.Object["status"].(map[string]interface{})
		Expect(ok).To(BeTrue(), "status block absent")
		Expect(status["origin"]).To(Equal("imported"),
			"status.origin must be imported for mode=import cluster")
	})

	It("import completes within 60 seconds", func() {
		Skip("requires live cluster with MGMT_KUBECONFIG set and AC-1 manually triggered")

		// This spec is a timing assertion bundled with the Ready check above.
		// Kept as a separate spec for CI skip-reason tracking.
		// Promote together with the Ready=True spec (same MGMT_KUBECONFIG condition).
	})
})
