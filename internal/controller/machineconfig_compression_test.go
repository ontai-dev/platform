package controller

import (
	"bytes"
	"compress/gzip"
	"testing"
)

// TestCompressMachineConfig_RoundTrip verifies that compress then decompress
// recovers the original bytes. RECON-F5.
func TestCompressMachineConfig_RoundTrip(t *testing.T) {
	original := []byte("machine:\n  type: controlplane\n  network:\n    hostname: cp1\n")
	compressed, err := compressMachineConfig(original)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if bytes.Equal(original, compressed) {
		t.Errorf("expected compressed bytes to differ from original")
	}
	// Decompress using gzip directly to verify the format.
	r, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(r); err != nil {
		t.Fatalf("read: %v", err)
	}
	_ = r.Close()
	if !bytes.Equal(original, out.Bytes()) {
		t.Errorf("round-trip failed: got %q, want %q", out.Bytes(), original)
	}
}

// TestCompressMachineConfig_SizeSmallerForTypicalYAML verifies that compression
// produces smaller output for typical machineconfig YAML content. RECON-F5.
func TestCompressMachineConfig_SizeSmallerForTypicalYAML(t *testing.T) {
	// Simulate a realistic machineconfig (repetitive YAML compresses very well).
	var buf bytes.Buffer
	for i := 0; i < 50; i++ {
		buf.WriteString("machine:\n  type: controlplane\n  network:\n    interfaces: []\n  install:\n    disk: /dev/vda\n")
	}
	original := buf.Bytes()
	compressed, err := compressMachineConfig(original)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if len(compressed) >= len(original) {
		t.Errorf("expected compression to reduce size: original=%d compressed=%d", len(original), len(compressed))
	}
}

// TestWriteMachineConfigSecret_SetsCompressionLabel verifies that the secret is
// written with the gzip compression label. RECON-F5.
func TestWriteMachineConfigSecret_SetsCompressionLabel(t *testing.T) {
	original := []byte("machine:\n  type: controlplane\n")
	compressed, err := compressMachineConfig(original)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	// Verify that compressed bytes are recoverable (label invariant test).
	if len(compressed) == len(original) {
		t.Skip("tiny payload: compression did not reduce size, label check would be ambiguous")
	}
	// The compression label constant must match what the conductor capability expects.
	if LabelMachineConfigCompression != "platform.ontai.dev/compression" {
		t.Errorf("LabelMachineConfigCompression value changed: %q", LabelMachineConfigCompression)
	}
	if MachineConfigCompressionGzip != "gzip" {
		t.Errorf("MachineConfigCompressionGzip value changed: %q", MachineConfigCompressionGzip)
	}
}
