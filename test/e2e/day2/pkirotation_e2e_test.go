package day2_e2e_test

// pkirotation_e2e_test.go -- Live PKI rotation end-to-end tests.
//
// Covers the full PKI rotation flow for an import-mode tenant cluster (ccs-dev):
//
//   TENANT-PKI-ROTATE       -- PKIRotation CR reaches Ready=True; kubeconfig Secrets
//                              refreshed in seam-tenant-{cluster}
//   TENANT-PKI-CLUSTER-REACH -- After rotation, proves ccs-dev is reachable by
//                              pushing a minimal single-manifest test ClusterPack and
//                              waiting for InfrastructurePackExecution to reach
//                              Succeeded=True using the refreshed kubeconfig
//
// The reachability test pushes two OCI tar.gz layers (empty RBAC + single ConfigMap
// workload) to the lab registry, creates an InfrastructureClusterPack CR, and lets
// the normal wrapper/signing/conductor-execute pipeline run. Succeeded=True on the
// PackExecution proves the conductor-execute Job successfully connected to ccs-dev
// using the kubeconfig written by pkiRotateHandler.
//
// Required environment variables:
//   MGMT_KUBECONFIG      -- path to management cluster kubeconfig (all tests skip if absent)
//   TENANT_CLUSTER_NAME  -- import-mode cluster name (default: ccs-dev)
//   REGISTRY_ADDR        -- OCI registry address (default: 10.20.0.1:5000)
//
// platform-schema.md §13 PKIRotation, §5 NodeMaintenance.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"
)

// pkiRotationTimeout is the time budget for a PKI rotation Job to complete.
// Rotation involves a staged machineconfig apply + Talos reboot coordination.
const pkiRotationTimeout = 10 * time.Minute

// packDeployTimeout is the time budget for a pack-deploy Job to reach Succeeded,
// including waiting for the signing loop and Kueue scheduling.
const packDeployTimeout = 10 * time.Minute

// ── TENANT-PKI-ROTATE: full rotation lifecycle on import-mode cluster ─────────

var _ = Describe("TENANT-PKI-ROTATE: PKIRotation on import-mode cluster", func() {
	It("PKIRotation CR reaches Ready=True and kubeconfig Secrets are refreshed for TENANT_CLUSTER_NAME", func() {
		cluster := tenantClusterName()
		tenantNS := "seam-tenant-" + cluster
		tenantRef := platformv1alpha1.LocalObjectRef{Name: cluster, Namespace: "seam-system"}

		crName := fmt.Sprintf("pki-rotate-%d", time.Now().UnixNano())
		pr := &platformv1alpha1.PKIRotation{
			ObjectMeta: metav1.ObjectMeta{
				Name:      crName,
				Namespace: tenantNS,
			},
			Spec: platformv1alpha1.PKIRotationSpec{
				ClusterRef: tenantRef,
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, pr)).To(Succeed())
		DeferCleanup(func() {
			_ = mgmtClient.Delete(mgmtCtx, pr, client.GracePeriodSeconds(0))
		})

		// Wait for the reconciler to submit the executor Job.
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.PKIRotation{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: crName, Namespace: tenantNS,
			}, got)).To(Succeed())
			g.Expect(got.Status.JobName).NotTo(BeEmpty(),
				"JobName not set -- pki-rotate Job not yet submitted")
		}, jobSubmitTimeout, pollInterval).Should(Succeed())

		// Wait for the executor Job to complete -- Ready=True.
		Eventually(func(g Gomega) {
			got := &platformv1alpha1.PKIRotation{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: crName, Namespace: tenantNS,
			}, got)).To(Succeed())
			cond := platformv1alpha1.FindCondition(got.Status.Conditions,
				platformv1alpha1.ConditionTypePKIRotationReady)
			g.Expect(cond).NotTo(BeNil(), "Ready condition not set on PKIRotation")
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue),
				"PKIRotation Ready must be True after pki-rotate completes")
		}, pkiRotationTimeout, pollInterval).Should(Succeed())

		// Verify that pkiRotateHandler wrote the refreshed seam-mc-{cluster}-kubeconfig
		// Secret. This is the canonical kubeconfig name; target-cluster-kubeconfig no
		// longer exists. platform-schema.md §13.
		Eventually(func(g Gomega) {
			secret := &corev1.Secret{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name:      "seam-mc-" + cluster + "-kubeconfig",
				Namespace: tenantNS,
			}, secret)).To(Succeed(), "seam-mc-%s-kubeconfig Secret not found in %s", cluster, tenantNS)
			g.Expect(secret.Data).NotTo(BeEmpty(),
				"seam-mc-%s-kubeconfig Secret must have non-empty data", cluster)
		}, 30*time.Second, pollInterval).Should(Succeed())
	})
})

// ── TENANT-PKI-CLUSTER-REACH: post-rotation ClusterPack probe ────────────────

var _ = Describe("TENANT-PKI-CLUSTER-REACH: single-manifest ClusterPack proves cluster reachable after PKI rotation", func() {
	It("minimal ClusterPack deploy to TENANT_CLUSTER_NAME reaches PackExecution Succeeded=True", func() {
		cluster := tenantClusterName()
		tenantNS := "seam-tenant-" + cluster

		ts := time.Now().UnixNano()
		packName := fmt.Sprintf("pki-probe-%d", ts)
		repo := fmt.Sprintf("ontai-dev/pki-probe-%d", ts)

		// Build and push the RBAC layer (minimal ServiceAccount -- empty RBAC is allowed
		// but a bare SA avoids guardian intake returning an unprovisioned RBACProfile).
		rbacYAML := fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata:
  name: pki-probe-%d
  namespace: default
`, ts)
		rbacBlob := buildTarGzManifest("rbac.yaml", rbacYAML)
		rbacDigest, err := registry.PushArtifact(mgmtCtx, repo, "rbac-v1", rbacBlob)
		Expect(err).NotTo(HaveOccurred(), "push RBAC layer to registry")

		// Build and push the workload layer (single ConfigMap).
		workloadYAML := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: pki-probe-%d
  namespace: default
data:
  probe: "pki-rotation-reachability-test"
  cluster: %q
`, ts, cluster)
		workloadBlob := buildTarGzManifest("workload.yaml", workloadYAML)
		workloadDigest, err := registry.PushArtifact(mgmtCtx, repo, "workload-v1", workloadBlob)
		Expect(err).NotTo(HaveOccurred(), "push workload layer to registry")

		// Create the ClusterPack CR. Wrapper watches ClusterPacks and creates a
		// PackExecution for each entry in spec.targetClusters.
		registryURL := registryAddr + "/" + repo
		cp := &seamcorev1alpha1.InfrastructureClusterPack{
			ObjectMeta: metav1.ObjectMeta{
				Name:      packName,
				Namespace: tenantNS,
			},
			Spec: seamcorev1alpha1.InfrastructureClusterPackSpec{
				Version: "v1.0.0-pki-probe",
				RegistryRef: seamcorev1alpha1.InfrastructurePackRegistryRef{
					URL:    registryURL,
					Digest: rbacDigest,
				},
				BasePackName:   "pki-probe",
				RBACDigest:     rbacDigest,
				WorkloadDigest: workloadDigest,
				TargetClusters: []string{cluster},
			},
		}
		Expect(mgmtClient.Create(mgmtCtx, cp)).To(Succeed())
		DeferCleanup(func() {
			// Delete ClusterPack -- wrapper GC handles PackExecution and PackInstance.
			latest := &seamcorev1alpha1.InfrastructureClusterPack{}
			if err := mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: packName, Namespace: tenantNS,
			}, latest); err == nil {
				_ = mgmtClient.Delete(mgmtCtx, latest)
			}
		})

		// Wait for the management conductor signing loop to sign the ClusterPack.
		// The pack-deploy flow requires status.signed=true before conductor-execute
		// runs. The signing loop runs on the management cluster conductor leader.
		Eventually(func(g Gomega) {
			got := &seamcorev1alpha1.InfrastructureClusterPack{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: packName, Namespace: tenantNS,
			}, got)).To(Succeed())
			g.Expect(got.Status.Signed).To(BeTrue(),
				"ClusterPack must be signed by the management conductor signing loop")
		}, 3*time.Minute, pollInterval).Should(Succeed())

		// Wait for wrapper to create the PackExecution.
		// PackExecution name convention: {clusterPackName}-{clusterName}.
		peName := packName + "-" + cluster
		Eventually(func(g Gomega) {
			pe := &seamcorev1alpha1.InfrastructurePackExecution{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: peName, Namespace: tenantNS,
			}, pe)).To(Succeed(), "PackExecution %s not yet created by wrapper", peName)
		}, jobSubmitTimeout, pollInterval).Should(Succeed())

		// Wait for PackExecution to reach Succeeded=True.
		// This proves conductor-execute successfully connected to ccs-dev using the
		// kubeconfig refreshed by pkiRotateHandler and applied the test ConfigMap.
		Eventually(func(g Gomega) {
			pe := &seamcorev1alpha1.InfrastructurePackExecution{}
			g.Expect(mgmtClient.Get(mgmtCtx, types.NamespacedName{
				Name: peName, Namespace: tenantNS,
			}, pe)).To(Succeed())

			var succeededCond *metav1.Condition
			for i := range pe.Status.Conditions {
				if pe.Status.Conditions[i].Type == "Succeeded" {
					succeededCond = &pe.Status.Conditions[i]
					break
				}
			}
			g.Expect(succeededCond).NotTo(BeNil(),
				"Succeeded condition not yet set on PackExecution %s", peName)
			g.Expect(succeededCond.Status).To(Equal(metav1.ConditionTrue),
				"PackExecution Succeeded must be True -- ccs-dev is reachable with refreshed kubeconfig")
		}, packDeployTimeout, pollInterval).Should(Succeed())
	})
})

// buildTarGzManifest creates a tar.gz archive containing a single YAML file.
// Used to construct minimal OCI layer blobs for test ClusterPacks.
func buildTarGzManifest(filename, content string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	hdr := &tar.Header{
		Name:    filename,
		Mode:    0o644,
		Size:    int64(len(content)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		panic(fmt.Sprintf("buildTarGzManifest: write header: %v", err))
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		panic(fmt.Sprintf("buildTarGzManifest: write content: %v", err))
	}
	if err := tw.Close(); err != nil {
		panic(fmt.Sprintf("buildTarGzManifest: close tar: %v", err))
	}
	if err := gz.Close(); err != nil {
		panic(fmt.Sprintf("buildTarGzManifest: close gzip: %v", err))
	}
	return buf.Bytes()
}
