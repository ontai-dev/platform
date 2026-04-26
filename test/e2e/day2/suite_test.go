// Package day2_e2e_test contains end-to-end test stubs for the platform day-2
// operational CRD reconcilers.
//
// Promotion condition for stub specs: requires TENANT-CLUSTER-E2E and AC-1 closed.
// Management cluster specs run immediately when MGMT_KUBECONFIG is set.
//
// Required environment variables:
//   - MGMT_KUBECONFIG  — path to management cluster kubeconfig
//   - TENANT_KUBECONFIG — path to tenant cluster kubeconfig
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
)

var (
	mgmtClient client.Client
	mgmtCtx    context.Context
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

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred(), "build REST config from MGMT_KUBECONFIG")

	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(platformv1alpha1.AddToScheme(scheme)).To(Succeed())
	Expect(seamcorev1alpha1.AddToScheme(scheme)).To(Succeed())

	mgmtClient, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred(), "build management cluster client")
})
