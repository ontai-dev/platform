// Package controller_test -- Unit tests for pki_cert_helpers.go.
//
// Tests verify certificate expiry parsing from PEM data, kubeconfig YAML,
// and talosconfig YAML. All tests are self-contained: no live cluster, no
// filesystem access, no external dependencies. platform-schema.md §13.
package controller_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ontai-dev/platform/internal/controller"
)

// generateSelfSignedCert generates an ECDSA P-256 self-signed certificate
// with the given NotBefore and NotAfter times. Returns PEM-encoded cert bytes.
func generateSelfSignedCert(t *testing.T, notBefore, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-cert"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestParsePEMCertExpiry_SingleCert verifies that a single certificate PEM block
// returns the correct NotAfter date.
func TestParsePEMCertExpiry_SingleCert(t *testing.T) {
	expected := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	certPEM := generateSelfSignedCert(t, time.Now().Add(-time.Hour), expected)

	got, err := controller.ParsePEMCertExpiry(certPEM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil expiry")
	}
	if !got.Equal(expected) {
		t.Errorf("expected %v; got %v", expected, *got)
	}
}

// TestParsePEMCertExpiry_MultipleCerts verifies that the earliest NotAfter is
// returned when the PEM data contains multiple certificates.
func TestParsePEMCertExpiry_MultipleCerts(t *testing.T) {
	earlier := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)

	pem1 := generateSelfSignedCert(t, time.Now().Add(-time.Hour), later)
	pem2 := generateSelfSignedCert(t, time.Now().Add(-time.Hour), earlier)
	combined := append(pem1, pem2...)

	got, err := controller.ParsePEMCertExpiry(combined)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil expiry")
	}
	if !got.Equal(earlier) {
		t.Errorf("expected earliest %v; got %v", earlier, *got)
	}
}

// TestParsePEMCertExpiry_Empty verifies that empty input returns nil without error.
func TestParsePEMCertExpiry_Empty(t *testing.T) {
	got, err := controller.ParsePEMCertExpiry([]byte{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty input; got %v", got)
	}
}

// TestParsePEMCertExpiry_NonCertPEM verifies that non-CERTIFICATE PEM blocks
// are skipped and nil is returned when no CERTIFICATE block is present.
func TestParsePEMCertExpiry_NonCertPEM(t *testing.T) {
	pemData := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("irrelevant")})
	got, err := controller.ParsePEMCertExpiry(pemData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for non-cert PEM; got %v", got)
	}
}

// buildKubeconfig builds a minimal kubeconfig YAML string with an embedded
// client certificate using the provided PEM cert bytes.
func buildKubeconfig(t *testing.T, certPEM []byte) []byte {
	t.Helper()
	certB64 := base64.StdEncoding.EncodeToString(certPEM)
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: test-ctx
clusters:
- name: test-cluster
  cluster:
    server: https://10.0.0.1:6443
contexts:
- name: test-ctx
  context:
    cluster: test-cluster
    user: admin
users:
- name: admin
  user:
    client-certificate-data: %s
    client-key-data: dGVzdA==
`, certB64)
	return []byte(kubeconfig)
}

// TestParseKubeconfigCertExpiry_Valid verifies that a kubeconfig with embedded
// client certificate data returns the correct expiry.
func TestParseKubeconfigCertExpiry_Valid(t *testing.T) {
	expected := time.Date(2027, 3, 15, 0, 0, 0, 0, time.UTC)
	certPEM := generateSelfSignedCert(t, time.Now().Add(-time.Hour), expected)
	kubeconfigYAML := buildKubeconfig(t, certPEM)

	got, err := controller.ParseKubeconfigCertExpiry(kubeconfigYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil expiry")
	}
	if !got.Equal(expected) {
		t.Errorf("expected %v; got %v", expected, *got)
	}
}

// TestParseKubeconfigCertExpiry_NoCertData verifies that a kubeconfig without
// embedded client certificate data returns nil without error.
func TestParseKubeconfigCertExpiry_NoCertData(t *testing.T) {
	kubeconfig := `apiVersion: v1
kind: Config
current-context: test-ctx
clusters:
- name: test-cluster
  cluster:
    server: https://10.0.0.1:6443
contexts:
- name: test-ctx
  context:
    cluster: test-cluster
    user: admin
users:
- name: admin
  user:
    token: sometoken
`
	got, err := controller.ParseKubeconfigCertExpiry([]byte(kubeconfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when no cert data; got %v", got)
	}
}

// buildTalosconfig builds a minimal talosconfig YAML string with a base64-encoded
// PEM certificate in the active context's crt field.
func buildTalosconfig(t *testing.T, certPEM []byte) []byte {
	t.Helper()
	crtB64 := base64.StdEncoding.EncodeToString(certPEM)
	tc := fmt.Sprintf(`context: admin@test-cluster
contexts:
  admin@test-cluster:
    endpoints:
      - 10.0.0.1
    nodes:
      - 10.0.0.1
    ca: dGVzdA==
    crt: %s
    key: dGVzdA==
`, crtB64)
	return []byte(tc)
}

// TestParseTalosConfigCertExpiry_Valid verifies that a talosconfig with a crt
// field returns the correct certificate expiry.
func TestParseTalosConfigCertExpiry_Valid(t *testing.T) {
	expected := time.Date(2027, 9, 30, 0, 0, 0, 0, time.UTC)
	certPEM := generateSelfSignedCert(t, time.Now().Add(-time.Hour), expected)
	talosconfigYAML := buildTalosconfig(t, certPEM)

	got, err := controller.ParseTalosConfigCertExpiry(talosconfigYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil expiry")
	}
	if !got.Equal(expected) {
		t.Errorf("expected %v; got %v", expected, *got)
	}
}

// TestParseTalosConfigCertExpiry_MissingCrt verifies that a talosconfig without
// a crt field returns nil without error.
func TestParseTalosConfigCertExpiry_MissingCrt(t *testing.T) {
	tc := `context: admin@test-cluster
contexts:
  admin@test-cluster:
    endpoints:
      - 10.0.0.1
    ca: dGVzdA==
    key: dGVzdA==
`
	got, err := controller.ParseTalosConfigCertExpiry([]byte(tc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when crt absent; got %v", got)
	}
}

// TestParseTalosConfigCertExpiry_NoActiveContext verifies that a talosconfig
// without an active context set returns nil without error.
func TestParseTalosConfigCertExpiry_NoActiveContext(t *testing.T) {
	tc := `contexts:
  admin@test-cluster:
    endpoints:
      - 10.0.0.1
`
	got, err := controller.ParseTalosConfigCertExpiry([]byte(tc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when context absent; got %v", got)
	}
}
