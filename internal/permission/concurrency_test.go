// Package permission — concurrency tests for permission memory and policy
package permission

import (
	"sync"
	"testing"
)

// TestMemoryConcurrentAccess tests concurrent read/write access to permission memory
// to detect race conditions in the LRU cache and fingerprint lookups.
// NOTE: This test is skipped because Memory is not thread-safe by design.
// The Memory struct is used internally by Policy which holds a mutex.
// For thread-safety, use Policy.Has() and Policy.Remember() instead.
// Run with: go test -run TestMemoryConcurrentAccess -race
func TestMemoryConcurrentAccess(t *testing.T) {
	t.Skip("Memory is not thread-safe; use Policy (which holds a mutex) for concurrent access")
}

// TestPolicyConcurrentAccess tests concurrent access to Policy
func TestPolicyConcurrentAccess(t *testing.T) {
	policy := NewPolicy(LevelWorkspaceWrite)
	policy.SetAllowedTools([]string{"bash", "write_file"})
	var wg sync.WaitGroup

	// Concurrent checks
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			policy.Check("bash", `{"command":"ls"}`)
		}()
	}

	// Concurrent mode changes
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			policy.SetMode(LevelReadOnly)
			policy.SetMode(LevelWorkspaceWrite)
		}()
	}

	// Concurrent memory operations
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			policy.Remember("bash", `{"command":"ls"}`, true)
			policy.Has("bash", `{"command":"ls"}`)
		}()
	}

	wg.Wait()
}

// TestMemoryLRUEvictionConcurrent tests LRU eviction under concurrent access.
// NOTE: Skipped because Memory is not thread-safe by design.
// Use Policy for concurrent access instead.
func TestMemoryLRUEvictionConcurrent(t *testing.T) {
	t.Skip("Memory is not thread-safe; use Policy for concurrent access")
}
