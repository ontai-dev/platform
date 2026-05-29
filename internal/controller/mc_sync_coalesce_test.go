package controller

import (
	"testing"
	"time"
)

func TestMCSyncCoalescer_FirstSubmission_True(t *testing.T) {
	c := NewMCSyncCoalescer()
	if !c.ShouldSubmit("ccs-mgmt", "controlplane", "abc123") {
		t.Error("expected true for first submission (no prior entry)")
	}
}

func TestMCSyncCoalescer_SameHashWithinWindow_False(t *testing.T) {
	c := NewMCSyncCoalescer()
	c.MarkSubmitted("ccs-mgmt", "controlplane", "abc123")
	// Same hash within the coalesce window: suppress.
	if c.ShouldSubmit("ccs-mgmt", "controlplane", "abc123") {
		t.Error("expected false: same hash within coalesce window should be suppressed")
	}
}

func TestMCSyncCoalescer_DifferentHashWithinWindow_True(t *testing.T) {
	c := NewMCSyncCoalescer()
	c.MarkSubmitted("ccs-mgmt", "controlplane", "abc123")
	// Hash changed: must allow even within the window (latest content wins).
	if !c.ShouldSubmit("ccs-mgmt", "controlplane", "def456") {
		t.Error("expected true: hash changed within window should be allowed (latest content wins)")
	}
}

func TestMCSyncCoalescer_SameHashAfterWindow_True(t *testing.T) {
	c := NewMCSyncCoalescer()
	c.MarkSubmitted("ccs-mgmt", "controlplane", "abc123")
	// Simulate the coalesce window having elapsed by backdating the entry.
	key := mcSyncDebounceKey{cluster: "ccs-mgmt", nodeClass: "controlplane"}
	c.mu.Lock()
	c.entries[key].lastSubmitted = time.Now().Add(-(mcSyncCoalesceWindow + time.Second))
	c.mu.Unlock()
	// Same hash but window expired: allow.
	if !c.ShouldSubmit("ccs-mgmt", "controlplane", "abc123") {
		t.Error("expected true: same hash after coalesce window should be allowed")
	}
}

func TestMCSyncCoalescer_DifferentClusters_Independent(t *testing.T) {
	c := NewMCSyncCoalescer()
	c.MarkSubmitted("ccs-mgmt", "controlplane", "abc123")
	// Different cluster: not suppressed.
	if !c.ShouldSubmit("ccs-dev", "controlplane", "abc123") {
		t.Error("expected true: different cluster entries are independent")
	}
}

func TestMCSyncCoalescer_DifferentClasses_Independent(t *testing.T) {
	c := NewMCSyncCoalescer()
	c.MarkSubmitted("ccs-mgmt", "controlplane", "abc123")
	// Same cluster, different class: not suppressed.
	if !c.ShouldSubmit("ccs-mgmt", "worker", "abc123") {
		t.Error("expected true: different nodeClass entries are independent")
	}
}

func TestMCSyncCoalescer_MarkUpdatesEntry(t *testing.T) {
	c := NewMCSyncCoalescer()
	c.MarkSubmitted("ccs-mgmt", "controlplane", "hash1")
	// Mark with new hash.
	c.MarkSubmitted("ccs-mgmt", "controlplane", "hash2")
	// hash2 within window: suppress.
	if c.ShouldSubmit("ccs-mgmt", "controlplane", "hash2") {
		t.Error("expected false: hash2 was just marked, should be suppressed")
	}
	// hash1 within window but differs from current last hash: allow.
	if !c.ShouldSubmit("ccs-mgmt", "controlplane", "hash1") {
		t.Error("expected true: hash1 differs from last submitted hash2, content changed")
	}
}
