package controller

import (
	"testing"
)

// TestMachineConfigLabelConstants verifies that label constant values are stable.
// These values are referenced by the conductor machineconfig-sync capability.
// Changing them is a breaking change requiring a matching conductor update.
func TestMachineConfigLabelConstants(t *testing.T) {
	if LabelMachineConfigCompression != "platform.ontai.dev/compression" {
		t.Errorf("LabelMachineConfigCompression value changed: %q", LabelMachineConfigCompression)
	}
	if MachineConfigCompressionGzip != "gzip" {
		t.Errorf("MachineConfigCompressionGzip value changed: %q", MachineConfigCompressionGzip)
	}
	if AnnotationMCGeneration != "platform.ontai.dev/mc-generation" {
		t.Errorf("AnnotationMCGeneration value changed: %q", AnnotationMCGeneration)
	}
}
