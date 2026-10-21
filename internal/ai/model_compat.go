// Package ai - Model compatibility patches
// Centralizes workarounds for non-standard behavior from specific model providers.
package ai

import (
	"encoding/json"
	"strings"
)

// ensureValidArguments ensures tool call arguments is a valid JSON object string
// Some models (e.g. MiniMax-M2.5) return non-standard argument values:
//
//	nil → "null", empty string → "", non-JSON string → plain text
//
// These are not valid JSON objects, so we normalize to "{}" as a safe default.
func ensureValidArguments(inputRaw interface{}) string {
	if inputRaw == nil {
		return "{}"
	}
	switch v := inputRaw.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" || trimmed == "null" {
			return "{}"
		} else if strings.HasPrefix(trimmed, "{") {
			return v
		}
		return "{}"
	default:
		inputJSON, err := json.Marshal(inputRaw)
		if err != nil {
			return "{}"
		}
		arguments := string(inputJSON)
		if arguments == "null" {
			return "{}"
		}
		return arguments
	}
}
