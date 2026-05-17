package controller

// pki_cert_helpers.go -- Certificate expiry detection and PKI rotation triggering.
//
// The reconciler reads the kubeconfig and talosconfig Secrets for an imported
// cluster, parses the embedded X.509 certificates, and writes the earliest expiry
// into TalosCluster.status.pkiExpiryDate. When the expiry is within
// spec.pkiRotationThresholdDays of the current time, a PKIRotation CR is created
// automatically. platform-schema.md §13.

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

// defaultPKIThreshold is the default number of days before cert expiry to
// auto-trigger PKI rotation when spec.pkiRotationThresholdDays is zero.
const defaultPKIThreshold = 30

// ParsePEMCertExpiry iterates PEM blocks in pemData, parses each CERTIFICATE
// block, and returns the earliest NotAfter time found. Returns nil if no
// CERTIFICATE blocks are present in pemData. Exported for unit tests.
// platform-schema.md §13.
func ParsePEMCertExpiry(pemData []byte) (*time.Time, error) {
	var earliest *time.Time
	rest := pemData
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsePEMCertExpiry: parse certificate: %w", err)
		}
		notAfter := cert.NotAfter
		if earliest == nil || notAfter.Before(*earliest) {
			t := notAfter
			earliest = &t
		}
	}
	return earliest, nil
}

// ParseKubeconfigCertExpiry parses a kubeconfig YAML and returns the earliest
// certificate expiry found in the client authentication sections. Returns nil
// when no embedded client certificate data is present. Only ClientCertificateData
// (embedded) is inspected; ClientCertificate (file path) entries are skipped
// as they are not used in our Secret-based setup. Exported for unit tests.
// platform-schema.md §13.
func ParseKubeconfigCertExpiry(kubeconfigYAML []byte) (*time.Time, error) {
	clientConfig, err := clientcmd.NewClientConfigFromBytes(kubeconfigYAML)
	if err != nil {
		return nil, fmt.Errorf("parseKubeconfigCertExpiry: load kubeconfig: %w", err)
	}
	rawCfg, err := clientConfig.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("parseKubeconfigCertExpiry: get raw config: %w", err)
	}

	var earliest *time.Time
	for _, authInfo := range rawCfg.AuthInfos {
		if len(authInfo.ClientCertificateData) == 0 {
			continue
		}
		t, err := ParsePEMCertExpiry(authInfo.ClientCertificateData)
		if err != nil {
			return nil, fmt.Errorf("ParseKubeconfigCertExpiry: parse cert data: %w", err)
		}
		if t == nil {
			continue
		}
		if earliest == nil || t.Before(*earliest) {
			earliest = t
		}
	}
	return earliest, nil
}

// ParseTalosConfigCertExpiry parses a talosconfig YAML and returns the earliest
// certificate expiry found in the active context's client certificate. Returns nil
// when the crt field is absent or contains no parseable certificate. Exported for
// unit tests. platform-schema.md §13.
func ParseTalosConfigCertExpiry(talosConfigYAML []byte) (*time.Time, error) {
	var raw map[string]interface{}
	if err := yaml.Unmarshal(talosConfigYAML, &raw); err != nil {
		return nil, fmt.Errorf("parseTalosConfigCertExpiry: unmarshal YAML: %w", err)
	}

	activeContext, _ := raw["context"].(string)
	if activeContext == "" {
		return nil, nil
	}

	contextsRaw, ok := raw["contexts"].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	ctxRaw, ok := contextsRaw[activeContext].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	crtB64, ok := ctxRaw["crt"].(string)
	if !ok || crtB64 == "" {
		return nil, nil
	}

	certPEM, err := base64.StdEncoding.DecodeString(crtB64)
	if err != nil {
		return nil, fmt.Errorf("parseTalosConfigCertExpiry: base64 decode crt: %w", err)
	}
	return ParsePEMCertExpiry(certPEM)
}

// detectClusterPKIExpiry reads the kubeconfig and talosconfig Secrets for
// clusterName and returns the earliest certificate expiry across both. Returns
// (nil, nil) when neither Secret exists yet. Tolerates NotFound gracefully.
// platform-schema.md §13.
func detectClusterPKIExpiry(ctx context.Context, c client.Client, clusterName string) (*time.Time, error) {
	secretsNS := importSecretsNamespace(clusterName)

	var earliest *time.Time

	// Check kubeconfig Secret.
	kubeconfigExpiry, err := readSecretAndParseExpiry(ctx, c,
		types.NamespacedName{Name: kubeconfigSecretName(clusterName), Namespace: secretsNS},
		kubeconfigSecretKey,
		ParseKubeconfigCertExpiry,
	)
	if err != nil {
		return nil, fmt.Errorf("detectClusterPKIExpiry: kubeconfig: %w", err)
	}
	if kubeconfigExpiry != nil {
		earliest = kubeconfigExpiry
	}

	// Check talosconfig Secret.
	talosconfigExpiry, err := readSecretAndParseExpiry(ctx, c,
		types.NamespacedName{Name: talosconfigSecretName(clusterName), Namespace: secretsNS},
		talosconfigSecretKey,
		ParseTalosConfigCertExpiry,
	)
	if err != nil {
		return nil, fmt.Errorf("detectClusterPKIExpiry: talosconfig: %w", err)
	}
	if talosconfigExpiry != nil && (earliest == nil || talosconfigExpiry.Before(*earliest)) {
		earliest = talosconfigExpiry
	}

	return earliest, nil
}

// readSecretAndParseExpiry reads a Secret by namespacedName, extracts dataField,
// and calls parserFn to obtain the cert expiry. Returns (nil, nil) when the
// Secret does not exist.
func readSecretAndParseExpiry(
	ctx context.Context,
	c client.Client,
	key types.NamespacedName,
	dataField string,
	parserFn func([]byte) (*time.Time, error),
) (*time.Time, error) {
	s := &corev1.Secret{}
	if err := c.Get(ctx, key, s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get Secret %s/%s: %w", key.Namespace, key.Name, err)
	}
	data, ok := s.Data[dataField]
	if !ok || len(data) == 0 {
		return nil, nil
	}
	return parserFn(data)
}

// syncPKIExpiry calls detectClusterPKIExpiry, writes the result to
// tc.Status.PkiExpiryDate (modifying in place), and returns rotationNeeded=true
// when the expiry is within the configured threshold. platform-schema.md §13.
func syncPKIExpiry(ctx context.Context, c client.Client, tc *platformv1alpha1.TalosCluster) (bool, error) {
	expiry, err := detectClusterPKIExpiry(ctx, c, tc.Name)
	if err != nil {
		return false, err
	}

	if expiry != nil {
		t := metav1.NewTime(*expiry)
		tc.Status.PkiExpiryDate = &t
	}

	if expiry == nil {
		return false, nil
	}

	threshold := int32(defaultPKIThreshold)
	if tc.Spec.PkiRotationThresholdDays > 0 {
		threshold = tc.Spec.PkiRotationThresholdDays
	}
	deadline := time.Now().Add(time.Duration(threshold) * 24 * time.Hour)
	rotationNeeded := expiry.Before(deadline)
	return rotationNeeded, nil
}

// ensureAutoRotationPKI creates a PKIRotation CR when auto-rotation is triggered
// by an approaching cert expiry. It is idempotent: if a PKIRotation CR for this
// cluster already exists and is not yet complete, no duplicate is created.
// platform-schema.md §13.
func ensureAutoRotationPKI(ctx context.Context, c client.Client, _ *runtime.Scheme, tc *platformv1alpha1.TalosCluster) error {
	ns := importSecretsNamespace(tc.Name)

	existing := &platformv1alpha1.PKIRotationList{}
	if err := c.List(ctx, existing,
		client.InNamespace(ns),
		client.MatchingLabels{"platform.ontai.dev/cluster": tc.Name},
	); err != nil {
		return fmt.Errorf("ensureAutoRotationPKI: list PKIRotation: %w", err)
	}

	for _, item := range existing.Items {
		readyCond := platformv1alpha1.FindCondition(item.Status.Conditions, platformv1alpha1.ConditionTypePKIRotationReady)
		degradedCond := platformv1alpha1.FindCondition(item.Status.Conditions, platformv1alpha1.ConditionTypePKIRotationDegraded)
		inProgress := (readyCond == nil || readyCond.Status != metav1.ConditionTrue) &&
			(degradedCond == nil || degradedCond.Status != metav1.ConditionTrue)
		if inProgress {
			return nil
		}
	}

	ts := time.Now().UTC().Format("20060102t150405")
	name := fmt.Sprintf("%s-pki-auto-%s", tc.Name, ts)

	pkir := &platformv1alpha1.PKIRotation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"platform.ontai.dev/cluster":     tc.Name,
				"platform.ontai.dev/pki-trigger": "auto",
			},
		},
		Spec: platformv1alpha1.PKIRotationSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: tc.Name},
		},
	}
	if err := c.Create(ctx, pkir); err != nil {
		return fmt.Errorf("ensureAutoRotationPKI: create PKIRotation %s/%s: %w", ns, name, err)
	}
	return nil
}

// ensureAnnotationRotationPKI creates a PKIRotation CR for an on-demand
// annotation-triggered rotation. The annotation platform.ontai.dev/rotate-pki=true
// has already been detected by the caller; this function creates the CR.
// The caller removes the annotation after this returns. platform-schema.md §13.
func ensureAnnotationRotationPKI(ctx context.Context, c client.Client, _ *runtime.Scheme, tc *platformv1alpha1.TalosCluster) error {
	ns := importSecretsNamespace(tc.Name)

	ts := time.Now().UTC().Format("20060102t150405")
	name := fmt.Sprintf("%s-pki-manual-%s", tc.Name, ts)

	pkir := &platformv1alpha1.PKIRotation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"platform.ontai.dev/cluster":     tc.Name,
				"platform.ontai.dev/pki-trigger": "manual",
			},
		},
		Spec: platformv1alpha1.PKIRotationSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: tc.Name},
		},
	}
	if err := c.Create(ctx, pkir); err != nil {
		return fmt.Errorf("ensureAnnotationRotationPKI: create PKIRotation %s/%s: %w", ns, name, err)
	}
	return nil
}
