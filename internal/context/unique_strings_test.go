// Package context — tests for uniqueStrings (P0-13).
package context

import (
	"reflect"
	"testing"
)

// TestUniqueStrings_DoesNotMutateInput verifies that uniqueStrings returns
// a new slice and does NOT modify the input slice's backing array.
//
// P0-13: the old implementation used result := s[:0], which reused the
// input's backing array and could overwrite the caller's data. The fix
// uses make([]string, 0, len(s)) to guarantee the input is untouched.
func TestUniqueStrings_DoesNotMutateInput(t *testing.T) {
	input := []string{"a", "b", "a", "c", "b"}
	inputCopy := make([]string, len(input))
	copy(inputCopy, input)

	result := uniqueStrings(input)

	// The input slice must be unchanged after the call.
	if !reflect.DeepEqual(input, inputCopy) {
		t.Errorf("uniqueStrings mutated input:\n  got      = %v\n  expected = %v", input, inputCopy)
	}

	// The result should contain only unique values in first-occurrence order.
	expected := []string{"a", "b", "c"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("uniqueStrings result = %v, want %v", result, expected)
	}
}

// TestUniqueStrings_EmptyInput verifies behavior on an empty slice.
func TestUniqueStrings_EmptyInput(t *testing.T) {
	result := uniqueStrings([]string{})
	if len(result) != 0 {
		t.Errorf("expected empty result, got %v", result)
	}
}

// TestUniqueStrings_AllUnique verifies that all-unique inputs are
// preserved in order.
func TestUniqueStrings_AllUnique(t *testing.T) {
	input := []string{"x", "y", "z"}
	result := uniqueStrings(input)
	if !reflect.DeepEqual(result, input) {
		t.Errorf("expected %v, got %v", input, result)
	}
}

// TestUniqueStrings_AllDuplicates verifies that all-duplicate inputs
// collapse to a single element.
func TestUniqueStrings_AllDuplicates(t *testing.T) {
	input := []string{"dup", "dup", "dup"}
	result := uniqueStrings(input)
	expected := []string{"dup"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

// TestUniqueStrings_PreservesFirstOccurrenceOrder verifies that the
// first occurrence of each element determines its position in the result.
func TestUniqueStrings_PreservesFirstOccurrenceOrder(t *testing.T) {
	input := []string{"c", "a", "b", "a", "c", "d", "b"}
	result := uniqueStrings(input)
	expected := []string{"c", "a", "b", "d"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

// TestUniqueStrings_ReturnsNewSlice verifies that the returned slice
// does not share a backing array with the input (capacity may differ).
func TestUniqueStrings_ReturnsNewSlice(t *testing.T) {
	input := []string{"a", "b", "c"}
	result := uniqueStrings(input)
	// Modifying the result should not affect the input.
	if len(result) > 0 {
		result[0] = "MUTATED"
		if input[0] == "MUTATED" {
			t.Error("result shares backing array with input — modifying result affected input")
		}
	}
}
