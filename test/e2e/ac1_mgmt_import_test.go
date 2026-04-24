package e2e_test

// Step 2: TalosCluster mode=import validation.
// AC-1: Management cluster import acceptance contract.
//
// Pre-conditions:
//   - MGMT_KUBECONFIG set (suite-level gate)
//   - ccs-mgmt TalosCluster CR applied in seam-system with mode=import, role=management
//   - platform operator running in seam-system
//
// Reusable: validateTalosClusterImport accepts any ClusterClient and cluster name.
// Run against management cluster first; reuse for tenant cluster validation once
// TENANT-CLUSTER-E2E closes.
//
// Covers management cluster validation gate Step 2 (GAP_TO_FILL.md).
// platform-schema.md §5 TalosClusterModeImport.

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	e2ehelpers "github.com/ontai-dev/seam-core/pkg/e2e"
)

var (
	talosClusterGVR = schema.GroupVersionResource{
		Group:    "platform.ontai.dev",
		Version:  "v1alpha1",
		Resource: "talosclusters",
	}
	runnerConfigGVR = schema.GroupVersionResource{
		Group:    "runner.ontai.dev",
		Version:  "v1alpha1",
		Resource: "runnerconfigs",
	}
)

var _ = Describe("Step 2: TalosCluster mode=import validation (AC-1)", func() {
	const (
		clusterNS    = "seam-system"
		runnerNS     = "ont-system"
		pollInterval = 2 * time.Second
		pollTimeout  = 60 * time.Second
	)

	It("TalosCluster ccs-mgmt reaches Ready=True within 60s after import", func() {
		validateTalosClusterReady(context.Background(), mgmtClient, mgmtClusterName, pollTimeout, pollInterval)
	})

	It("exactly one RunnerConfig exists in ont-system for ccs-mgmt", func() {
		validateSingleRunnerConfig(context.Background(), mgmtClient, mgmtClusterName)
	})

	It("no duplicate RunnerConfig exists after repeated reconciliation", func() {
		validateRunnerConfigIdempotency(context.Background(), mgmtClient, mgmtClusterName, pollTimeout, pollInterval)
	})

	It("status.origin is imported", func() {
		validateTalosClusterOrigin(context.Background(), mgmtClient, mgmtClusterName)
	})

	It("TalosCluster with mode=import and role absent is rejected at admission", func() {
		validateModeImportRoleAbsentRejected(context.Background(), mgmtClient)
	})
})

// validateTalosClusterReady polls until TalosCluster clusterName in seam-system
// reaches Ready=True on the given cluster client.
func validateTalosClusterReady(
	ctx context.Context,
	cl *e2ehelpers.ClusterClient,
	clusterName string,
	timeout, interval time.Duration,
) {
	By("waiting for TalosCluster " + clusterName + " Ready=True on cluster " + cl.Name)
	Eventually(func() bool {
		obj, err := cl.Dynamic.Resource(talosClusterGVR).Namespace("seam-system").
			Get(ctx, clusterName, metav1.GetOptions{})
		if err != nil {
			return false
		}
		status, _ := obj.Object["status"].(map[string]interface{})
		conditions, _ := status["conditions"].([]interface{})
		for _, c := range conditions {
			cm, _ := c.(map[string]interface{})
			if cm["type"] == "Ready" && cm["status"] == "True" {
				return true
			}
		}
		return false
	}, timeout, interval).Should(BeTrue(),
		"TalosCluster %s did not reach Ready=True on %s within %s", clusterName, cl.Name, timeout)
}

// validateSingleRunnerConfig verifies exactly one RunnerConfig exists in ont-system
// for the given cluster.
func validateSingleRunnerConfig(
	ctx context.Context,
	cl *e2ehelpers.ClusterClient,
	clusterName string,
) {
	By("checking exactly one RunnerConfig in ont-system for " + clusterName)
	list, err := cl.Dynamic.Resource(runnerConfigGVR).Namespace("ont-system").
		List(ctx, metav1.ListOptions{
			LabelSelector: "platform.ontai.dev/cluster=" + clusterName,
		})
	Expect(err).NotTo(HaveOccurred(), "list RunnerConfigs in ont-system")
	Expect(list.Items).To(HaveLen(1),
		"expected exactly 1 RunnerConfig for %s in ont-system on %s, got %d",
		clusterName, cl.Name, len(list.Items))
}

// validateRunnerConfigIdempotency waits one reconciliation cycle and confirms no
// duplicate RunnerConfigs appear.
func validateRunnerConfigIdempotency(
	ctx context.Context,
	cl *e2ehelpers.ClusterClient,
	clusterName string,
	timeout, interval time.Duration,
) {
	By("confirming RunnerConfig count is stable after one reconciliation cycle")
	Consistently(func() int {
		list, err := cl.Dynamic.Resource(runnerConfigGVR).Namespace("ont-system").
			List(ctx, metav1.ListOptions{
				LabelSelector: "platform.ontai.dev/cluster=" + clusterName,
			})
		if err != nil {
			return -1
		}
		return len(list.Items)
	}, timeout, interval).Should(Equal(1),
		"idempotency violation: RunnerConfig count changed on %s for cluster %s", cl.Name, clusterName)
}

// validateTalosClusterOrigin verifies status.origin=imported on the TalosCluster CR.
func validateTalosClusterOrigin(
	ctx context.Context,
	cl *e2ehelpers.ClusterClient,
	clusterName string,
) {
	By("verifying status.origin=imported on TalosCluster " + clusterName)
	obj, err := cl.Dynamic.Resource(talosClusterGVR).Namespace("seam-system").
		Get(ctx, clusterName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())

	status, ok := obj.Object["status"].(map[string]interface{})
	Expect(ok).To(BeTrue(), "TalosCluster status block absent on %s", cl.Name)
	Expect(status["origin"]).To(Equal("imported"),
		"status.origin must be imported for mode=import TalosCluster on %s", cl.Name)
}

// validateModeImportRoleAbsentRejected applies a TalosCluster CR with mode=import
// and no role field and confirms the admission webhook rejects it.
// This verifies T-04a (CEL validation) and T-01 (compiler validation) together.
func validateModeImportRoleAbsentRejected(ctx context.Context, cl *e2ehelpers.ClusterClient) {
	By("applying TalosCluster with mode=import and absent role -- expecting webhook rejection")
	applier := e2ehelpers.NewCRApplier(cl)
	badManifest := []byte(`
apiVersion: platform.ontai.dev/v1alpha1
kind: TalosCluster
metadata:
  name: e2e-bad-import-no-role
  namespace: seam-system
spec:
  mode: import
  capi:
    enabled: false
`)
	_, err := applier.Apply(ctx, talosClusterGVR, badManifest)
	Expect(err).To(HaveOccurred(),
		"webhook should have rejected TalosCluster mode=import with absent role on %s", cl.Name)
	Expect(err.Error()).To(ContainSubstring("role"),
		"rejection message should mention the missing role field")
}
