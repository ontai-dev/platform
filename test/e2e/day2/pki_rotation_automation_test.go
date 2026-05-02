package day2_e2e_test

// pki_rotation_automation_test.go -- E2E stubs for PKI rotation automation.
//
// Covers annotation-triggered on-demand rotation and synthetic expiry injection.
// Both specs skip when MGMT_KUBECONFIG is absent.
//
// Promotion condition: requires management cluster access and BACKLOG-PKI-001 closed.
// platform-schema.md §13.

import (
	"context"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"
)

// pkirotationAutomationClusterName is the name of the test InfrastructureTalosCluster
// used for PKI rotation automation E2E tests. Configurable via env var.
func pkirotationAutomationClusterName() string {
	if v := os.Getenv("TENANT_CLUSTER_NAME"); v != "" {
		return v
	}
	return "ccs-test"
}

var _ = Describe("PKIRotation automation", func() {
	Describe("annotation-triggered rotation", func() {
		It("creates a PKIRotation CR when rotate-pki annotation is set", func() {
			Skip("requires PKI rotation automation controller implementation and DAY2-OPS-TENANT closed")
			if os.Getenv("MGMT_KUBECONFIG") == "" {
				Skip("requires management cluster access and DAY2-OPS-TENANT closed")
			}

			clusterName := pkirotationAutomationClusterName()
			tenantNS := "seam-tenant-" + clusterName

			// Annotate the InfrastructureTalosCluster with the rotate-pki trigger.
			itc := &seamcorev1alpha1.InfrastructureTalosCluster{}
			Expect(mgmtClient.Get(mgmtCtx, client.ObjectKey{
				Name:      clusterName,
				Namespace: "seam-system",
			}, itc)).To(Succeed(), "get InfrastructureTalosCluster")

			itcPatch := client.MergeFrom(itc.DeepCopy())
			if itc.Annotations == nil {
				itc.Annotations = make(map[string]string)
			}
			itc.Annotations["platform.ontai.dev/rotate-pki"] = "true"
			Expect(mgmtClient.Patch(mgmtCtx, itc, itcPatch)).To(Succeed(),
				"patch rotate-pki annotation")

			// Wait for a PKIRotation CR with pki-trigger=manual label to appear.
			Eventually(func() bool {
				pkiList := &platformv1alpha1.PKIRotationList{}
				if err := mgmtClient.List(mgmtCtx, pkiList,
					client.InNamespace(tenantNS),
					client.MatchingLabels{"platform.ontai.dev/pki-trigger": "manual"},
				); err != nil {
					return false
				}
				return len(pkiList.Items) > 0
			}, 30*time.Second, time.Second).Should(BeTrue(),
				"PKIRotation CR with pki-trigger=manual must appear within 30s")

			// Verify the annotation was cleared.
			Eventually(func() bool {
				updated := &seamcorev1alpha1.InfrastructureTalosCluster{}
				if err := mgmtClient.Get(mgmtCtx, client.ObjectKey{
					Name:      clusterName,
					Namespace: "seam-system",
				}, updated); err != nil {
					return false
				}
				return updated.Annotations["platform.ontai.dev/rotate-pki"] != "true"
			}, 15*time.Second, time.Second).Should(BeTrue(),
				"rotate-pki annotation must be cleared after PKIRotation CR is created")

			// Cleanup: delete the manual PKIRotation CR(s).
			DeferCleanup(func(ctx context.Context) {
				pkiList := &platformv1alpha1.PKIRotationList{}
				if err := mgmtClient.List(ctx, pkiList,
					client.InNamespace(tenantNS),
					client.MatchingLabels{"platform.ontai.dev/pki-trigger": "manual"},
				); err != nil {
					return
				}
				for i := range pkiList.Items {
					_ = mgmtClient.Delete(ctx, &pkiList.Items[i])
				}
			})
		})
	})

	Describe("synthetic expiry injection", func() {
		It("auto-creates PKIRotation CR when pkiExpiryDate is within threshold", func() {
			Skip("requires PKI rotation automation controller implementation and DAY2-OPS-TENANT closed")
			if os.Getenv("MGMT_KUBECONFIG") == "" {
				Skip("requires management cluster access and DAY2-OPS-TENANT closed")
			}

			clusterName := pkirotationAutomationClusterName()
			tenantNS := "seam-tenant-" + clusterName

			// Inject a near-term pkiExpiryDate via status patch (5 days from now,
			// well within the 30-day default threshold).
			syntheticExpiry := metav1.NewTime(time.Now().Add(5 * 24 * time.Hour))

			itc := &seamcorev1alpha1.InfrastructureTalosCluster{}
			Expect(mgmtClient.Get(mgmtCtx, client.ObjectKey{
				Name:      clusterName,
				Namespace: "seam-system",
			}, itc)).To(Succeed(), "get InfrastructureTalosCluster")

			itcStatusPatch := client.MergeFrom(itc.DeepCopy())
			itc.Status.PkiExpiryDate = &syntheticExpiry
			Expect(mgmtClient.Status().Patch(mgmtCtx, itc, itcStatusPatch)).To(Succeed(),
				"patch pkiExpiryDate status")

			// Wait for an auto-triggered PKIRotation CR to appear.
			Eventually(func() bool {
				pkiList := &platformv1alpha1.PKIRotationList{}
				if err := mgmtClient.List(mgmtCtx, pkiList,
					client.InNamespace(tenantNS),
					client.MatchingLabels{"platform.ontai.dev/pki-trigger": "auto"},
				); err != nil {
					return false
				}
				return len(pkiList.Items) > 0
			}, 30*time.Second, time.Second).Should(BeTrue(),
				"auto PKIRotation CR must appear within 30s of synthetic expiry injection")

			// Cleanup: delete the auto PKIRotation CR(s) and clear pkiExpiryDate.
			DeferCleanup(func(ctx context.Context) {
				pkiList := &platformv1alpha1.PKIRotationList{}
				if err := mgmtClient.List(ctx, pkiList,
					client.InNamespace(tenantNS),
					client.MatchingLabels{"platform.ontai.dev/pki-trigger": "auto"},
				); err == nil {
					for i := range pkiList.Items {
						_ = mgmtClient.Delete(ctx, &pkiList.Items[i])
					}
				}

				// Clear the synthetic pkiExpiryDate.
				latest := &seamcorev1alpha1.InfrastructureTalosCluster{}
				if err := mgmtClient.Get(ctx, client.ObjectKey{
					Name:      clusterName,
					Namespace: "seam-system",
				}, latest); err == nil {
					clearPatch := client.MergeFrom(latest.DeepCopy())
					latest.Status.PkiExpiryDate = nil
					_ = mgmtClient.Status().Patch(ctx, latest, clearPatch)
				}
			})
		})

		It("does not create duplicate PKIRotation CRs when one is already in-progress", func() {
			if os.Getenv("MGMT_KUBECONFIG") == "" {
				Skip("requires management cluster access and BACKLOG-PKI-001 closed")
			}
			// This test verifies the idempotency guard in ensureAutoRotationPKI.
			// Create a manual PKIRotation CR without Ready condition, inject expiry,
			// reconcile twice, and verify no second CR is created.
			Skip("requires management cluster access and BACKLOG-PKI-001 closed")
		})
	})
})
