package controller

import (
	"testing"
)

// TestImportSecretsNamespace verifies the naming convention for tenant namespaces.
func TestImportSecretsNamespace(t *testing.T) {
	cases := []struct {
		cluster string
		want    string
	}{
		{"ccs-dev", "seam-tenant-ccs-dev"},
		{"ccs-mgmt", "seam-tenant-ccs-mgmt"},
		{"my-cluster", "seam-tenant-my-cluster"},
	}
	for _, c := range cases {
		got := importSecretsNamespace(c.cluster)
		if got != c.want {
			t.Errorf("importSecretsNamespace(%q) = %q, want %q", c.cluster, got, c.want)
		}
	}
}

// TestTalosconfigSecretName verifies the talosconfig Secret naming convention.
func TestTalosconfigSecretName(t *testing.T) {
	cases := []struct {
		cluster string
		want    string
	}{
		{"ccs-dev", "seam-mc-ccs-dev-talosconfig"},
		{"ccs-mgmt", "seam-mc-ccs-mgmt-talosconfig"},
	}
	for _, c := range cases {
		got := talosconfigSecretName(c.cluster)
		if got != c.want {
			t.Errorf("talosconfigSecretName(%q) = %q, want %q", c.cluster, got, c.want)
		}
	}
}

// TestKubeconfigSecretName verifies the kubeconfig Secret naming convention.
func TestKubeconfigSecretName(t *testing.T) {
	cases := []struct {
		cluster string
		want    string
	}{
		{"ccs-dev", "seam-mc-ccs-dev-kubeconfig"},
		{"ccs-mgmt", "seam-mc-ccs-mgmt-kubeconfig"},
	}
	for _, c := range cases {
		got := kubeconfigSecretName(c.cluster)
		if got != c.want {
			t.Errorf("kubeconfigSecretName(%q) = %q, want %q", c.cluster, got, c.want)
		}
	}
}
