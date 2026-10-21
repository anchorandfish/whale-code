// Package context — tests for Base64Data SHA256 hashing (P0-12).
package context

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

// TestComputeBase64Hash_Deterministic verifies that the same Base64Data
// always produces the same hash.
func TestComputeBase64Hash_Deterministic(t *testing.T) {
	b1 := session.ContentBlock{Type: "image", Base64Data: "iVBORw0KGgo="}
	b2 := session.ContentBlock{Type: "image", Base64Data: "iVBORw0KGgo="}

	b1.ComputeBase64Hash()
	b2.ComputeBase64Hash()

	if b1.Base64Hash == "" {
		t.Fatal("Base64Hash should be non-empty after ComputeBase64Hash")
	}
	if b1.Base64Hash != b2.Base64Hash {
		t.Errorf("hashes differ: %q vs %q (same input should produce same hash)", b1.Base64Hash, b2.Base64Hash)
	}
}

// TestComputeBase64Hash_DifferentInputs verifies that different Base64Data
// produces different hashes.
func TestComputeBase64Hash_DifferentInputs(t *testing.T) {
	b1 := session.ContentBlock{Type: "image", Base64Data: "iVBORw0KGgo="}
	b2 := session.ContentBlock{Type: "image", Base64Data: "R0lGODlhAQAB"}

	b1.ComputeBase64Hash()
	b2.ComputeBase64Hash()

	if b1.Base64Hash == b2.Base64Hash {
		t.Errorf("different inputs should produce different hashes: both = %q", b1.Base64Hash)
	}
}

// TestComputeBase64Hash_EmptyData verifies that empty Base64Data clears
// the hash.
func TestComputeBase64Hash_EmptyData(t *testing.T) {
	b := session.ContentBlock{Type: "image", Base64Data: "", Base64Hash: "preexisting"}
	b.ComputeBase64Hash()
	if b.Base64Hash != "" {
		t.Errorf("empty Base64Data should clear hash, got %q", b.Base64Hash)
	}
}

// TestMessagesEqual_HashBasedComparison verifies that messagesEqual uses
// the hash for O(1) comparison when both blocks have hashes.
func TestMessagesEqual_HashBasedComparison(t *testing.T) {
	// Two blocks with the SAME Base64Data but different actual stored
	// Base64Data values (simulating a scenario where the full data was
	// stripped but the hash was preserved). The hash comparison should
	// declare them equal.
	b1 := session.ContentBlock{Type: "image", MediaType: "image/png", Base64Data: "iVBORw0KGgo="}
	b1.ComputeBase64Hash()
	// Simulate stripping the full data (e.g., after persistence to disk).
	b1.Base64Data = ""

	b2 := session.ContentBlock{Type: "image", MediaType: "image/png", Base64Data: "iVBORw0KGgo="}
	b2.ComputeBase64Hash()
	b2.Base64Data = ""

	a := session.Message{Role: session.RoleUser, Blocks: []session.ContentBlock{b1}}
	b := session.Message{Role: session.RoleUser, Blocks: []session.ContentBlock{b2}}

	if !messagesEqual(a, b) {
		t.Error("messages with same hash should be equal even if Base64Data stripped")
	}
}

// TestMessagesEqual_HashMismatch verifies that different hashes cause
// messagesEqual to return false.
func TestMessagesEqual_HashMismatch(t *testing.T) {
	b1 := session.ContentBlock{Type: "image", MediaType: "image/png", Base64Data: "aaa"}
	b1.ComputeBase64Hash()
	b1.Base64Data = "" // stripped

	b2 := session.ContentBlock{Type: "image", MediaType: "image/png", Base64Data: "bbb"}
	b2.ComputeBase64Hash()
	b2.Base64Data = "" // stripped

	a := session.Message{Role: session.RoleUser, Blocks: []session.ContentBlock{b1}}
	b := session.Message{Role: session.RoleUser, Blocks: []session.ContentBlock{b2}}

	if messagesEqual(a, b) {
		t.Error("messages with different hashes should not be equal")
	}
}

// TestMessagesEqual_HashFallbackToFullString verifies that when one block
// has a hash and the other doesn't, messagesEqual falls back to full
// Base64Data string comparison.
func TestMessagesEqual_HashFallbackToFullString(t *testing.T) {
	// b1 has hash, b2 doesn't — should fall back to full string comparison.
	b1 := session.ContentBlock{Type: "image", MediaType: "image/png", Base64Data: "same"}
	b1.ComputeBase64Hash()

	b2 := session.ContentBlock{Type: "image", MediaType: "image/png", Base64Data: "same"}
	// b2.ComputeBase64Hash() NOT called — no hash.

	a := session.Message{Role: session.RoleUser, Blocks: []session.ContentBlock{b1}}
	b := session.Message{Role: session.RoleUser, Blocks: []session.ContentBlock{b2}}

	if !messagesEqual(a, b) {
		t.Error("fallback to full string comparison should find them equal")
	}

	// Now with different data — fallback should detect inequality.
	b3 := session.ContentBlock{Type: "image", MediaType: "image/png", Base64Data: "different"}
	c := session.Message{Role: session.RoleUser, Blocks: []session.ContentBlock{b3}}
	if messagesEqual(a, c) {
		t.Error("fallback should detect different Base64Data")
	}
}

// TestMessagesEqual_LargeBase64Performance is a smoke test verifying that
// hash-based comparison handles large base64 strings without issues.
func TestMessagesEqual_LargeBase64Performance(t *testing.T) {
	// Create a large base64 string (~1MB).
	largeData := strings.Repeat("A", 1_000_000)

	b1 := session.ContentBlock{Type: "image", MediaType: "image/png", Base64Data: largeData}
	b1.ComputeBase64Hash()

	b2 := session.ContentBlock{Type: "image", MediaType: "image/png", Base64Data: largeData}
	b2.ComputeBase64Hash()

	a := session.Message{Role: session.RoleUser, Blocks: []session.ContentBlock{b1}}
	b := session.Message{Role: session.RoleUser, Blocks: []session.ContentBlock{b2}}

	// Hash comparison should be instant regardless of data size.
	if !messagesEqual(a, b) {
		t.Error("large base64 with same hash should be equal")
	}
}
