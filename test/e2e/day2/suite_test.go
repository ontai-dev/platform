// Package day2_e2e_test contains end-to-end tests for the platform day-2
// operational CRD reconcilers.
//
// Management cluster specs (ccs-mgmt) run immediately when MGMT_KUBECONFIG is set.
// Tenant cluster specs (ccs-dev) require TENANT_CLUSTER_NAME (default: ccs-dev).
//
// Required environment variables:
//   - MGMT_KUBECONFIG       -- path to management cluster kubeconfig (all specs skip if absent)
//   - TENANT_CLUSTER_NAME   -- import-mode tenant cluster name (default: ccs-dev)
//   - TENANT_WORKER_NODE    -- worker node name on the tenant cluster (optional)
//   - REGISTRY_ADDR         -- OCI registry address for test pack push (default: 10.20.0.1:5000)
//
// Run with: make e2e
// Skip condition: MGMT_KUBECONFIG absent -> all specs skip.
package day2_e2e_test

import (
	"context"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"
	e2ehelpers "github.com/ontai-dev/seam-core/pkg/e2e"
)

var (
	mgmtClient   client.Client
	mgmtCtx      context.Context
	registry     *e2ehelpers.RegistryClient
	registryAddr string
)

func TestDay2E2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "platform day-2 E2E Suite")
}

var _ = BeforeSuite(func() {
	mgmtCtx = context.Background()

	kubeconfig := os.Getenv("MGMT_KUBECONFIG")
	if kubeconfig == "" {
		Skip("MGMT_KUBECONFIG not set — tests require a live cluster")
	}

	registryAddr = os.Getenv("REGISTRY_ADDR")
	if registryAddr == "" {
		registryAddr = "10.20.0.1:5000"
	}
	registry = e2ehelpers.NewRegistryClient(registryAddr)

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred(), "build REST config from MGMT_KUBECONFIG")

	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(platformv1alpha1.AddToScheme(scheme)).To(Succeed())
	Expect(seamcorev1alpha1.AddToScheme(scheme)).To(Succeed())

	mgmtClient, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred(), "build management cluster client")
})
