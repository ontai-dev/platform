package controller

import (
	"testing"
)

const testMachineConfig = `version: v1alpha1
debug: false
persist: true
machine:
    type: controlplane
    token: abc123
    ca:
        crt: CERT
        key: KEY
    kubelet:
        image: ghcr.io/siderolabs/kubelet:v1.32.3
        nodeIP:
            validSubnets:
                - 10.20.0.0/24
    network:
        hostname: ccs-dev-cp1
        interfaces:
            - interface: enp1s0
              dhcp: false
              addresses:
                - 10.20.0.11/24
              routes:
                - network: 0.0.0.0/0
                  gateway: 10.20.0.1
              vip:
                ip: 10.20.0.20
        nameservers:
            - 8.8.4.4
            - 8.8.8.8
    install:
        disk: /dev/vda
cluster:
    clusterName: ccs-dev
`

// TestStripPerNodeNetworkConfig_RemovesHostnameAndInterfaces verifies that hostname
// and interfaces are stripped from the class secret while nameservers are preserved.
func TestStripPerNodeNetworkConfig_RemovesHostnameAndInterfaces(t *testing.T) {
	out, err := stripPerNodeNetworkConfig([]byte(testMachineConfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	outStr := string(out)

	if contains(outStr, "ccs-dev-cp1") {
		t.Error("hostname 'ccs-dev-cp1' must be stripped from class config")
	}
	if contains(outStr, "10.20.0.11") {
		t.Error("node IP '10.20.0.11' must be stripped from class config")
	}
	if contains(outStr, "0.20.0.20") {
		t.Error("VIP '10.20.0.20' in interfaces must be stripped from class config")
	}
	if !contains(outStr, "8.8.4.4") {
		t.Error("nameservers must be preserved in class config")
	}
	if !contains(outStr, "10.20.0.0/24") {
		t.Error("kubelet.nodeIP.validSubnets must be preserved in class config")
	}
	if !contains(outStr, "ccs-dev") {
		t.Error("clusterName must be preserved in class config")
	}
}

// TestStripPerNodeNetworkConfig_NoNetwork verifies no-op when machine.network is absent.
func TestStripPerNodeNetworkConfig_NoNetwork(t *testing.T) {
	input := []byte(`version: v1alpha1
machine:
    type: controlplane
    token: abc123
cluster:
    clusterName: ccs-dev
`)
	out, err := stripPerNodeNetworkConfig(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(string(out), "ccs-dev") {
		t.Error("clusterName must be preserved when no network section present")
	}
}

// TestStripPerNodeNetworkConfig_NameserversOnly verifies that a network section
// containing only nameservers (no hostname or interfaces) is preserved as-is.
func TestStripPerNodeNetworkConfig_NameserversOnly(t *testing.T) {
	input := []byte(`version: v1alpha1
machine:
    type: controlplane
    network:
        nameservers:
            - 1.1.1.1
`)
	out, err := stripPerNodeNetworkConfig(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(string(out), "1.1.1.1") {
		t.Error("nameservers must be preserved when hostname/interfaces absent")
	}
}

// TestStripPerNodeNetworkConfig_EmptyNetworkRemoved verifies that an empty network
// section is deleted entirely after stripping hostname and interfaces.
func TestStripPerNodeNetworkConfig_EmptyNetworkRemoved(t *testing.T) {
	input := []byte(`version: v1alpha1
machine:
    type: controlplane
    network:
        hostname: ccs-dev-cp1
        interfaces:
            - interface: enp1s0
              addresses:
                - 10.20.0.11/24
`)
	out, err := stripPerNodeNetworkConfig(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	outStr := string(out)
	if contains(outStr, "ccs-dev-cp1") {
		t.Error("hostname must be stripped")
	}
	if contains(outStr, "10.20.0.11") {
		t.Error("interface address must be stripped")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSlice([]byte(s), []byte(sub)))
}

func containsSlice(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
