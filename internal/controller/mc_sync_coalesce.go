package controller

import (
	"sync"
	"time"
)

// mcSyncCoalesceWindow is the minimum time between MachineConfigSync CR submissions
// for the same (cluster, class) pair. Rapid Secret content changes within this window
// are coalesced: only the latest hash triggers a submission. RECON-F2.
const mcSyncCoalesceWindow = 30 * time.Second

// mcSyncDebounceKey identifies a (cluster, nodeClass) pair.
type mcSyncDebounceKey struct {
	cluster   string
	nodeClass string
}

// mcSyncDebounceEntry records the last time a MachineConfigSync CR was submitted
// for a given (cluster, class) pair and the hash that was used.
type mcSyncDebounceEntry struct {
	lastSubmitted time.Time
	lastHash      string
}

// MCSyncCoalescer debounces MachineConfigSync CR creation to prevent content-change
// storms from flooding the Job queue with redundant sync operations. RECON-F2.
//
// Usage: call ShouldSubmit before creating a MachineConfigSync CR. If it returns
// false, the same or a newer submission is already queued within the coalesce window.
// Call MarkSubmitted after successfully creating the CR.
type MCSyncCoalescer struct {
	mu      sync.Mutex
	entries map[mcSyncDebounceKey]*mcSyncDebounceEntry
}

// NewMCSyncCoalescer allocates a zero-state coalescer.
func NewMCSyncCoalescer() *MCSyncCoalescer {
	return &MCSyncCoalescer{
		entries: make(map[mcSyncDebounceKey]*mcSyncDebounceEntry),
	}
}

// ShouldSubmit returns true if a new MachineConfigSync CR should be created for
// (cluster, nodeClass) with the given content hash.
//
// Returns false when:
//   - A submission for the SAME hash was recorded within the coalesce window.
//
// Returns true when:
//   - No prior submission exists.
//   - The last submission was outside the coalesce window (regardless of hash).
//   - The hash has changed since the last submission (content updated again).
//
// The hash-changed case always returns true so that the most recent content is
// always applied, even within the coalesce window.
func (c *MCSyncCoalescer) ShouldSubmit(cluster, nodeClass, hash string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := mcSyncDebounceKey{cluster: cluster, nodeClass: nodeClass}
	entry, ok := c.entries[key]
	if !ok {
		return true
	}
	if time.Since(entry.lastSubmitted) > mcSyncCoalesceWindow {
		return true
	}
	// Within the window: allow if hash changed; suppress if same hash.
	return entry.lastHash != hash
}

// MarkSubmitted records that a MachineConfigSync CR was submitted for (cluster, nodeClass)
// with the given hash. Call immediately after successfully creating the CR.
func (c *MCSyncCoalescer) MarkSubmitted(cluster, nodeClass, hash string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := mcSyncDebounceKey{cluster: cluster, nodeClass: nodeClass}
	c.entries[key] = &mcSyncDebounceEntry{
		lastSubmitted: time.Now(),
		lastHash:      hash,
	}
}
