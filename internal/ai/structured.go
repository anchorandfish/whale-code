// Package ai - Structured Output functionality
package ai

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
)

// ResponseSchema is the JSON Schema constraint for structured output
type ResponseSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Schema      map[string]interface{} `json:"schema"`
	Strict      bool                   `json:"strict,omitempty"`
}

// StructuredOutputResult is the resolved structured output result
type StructuredOutputResult struct {
	Data             map[string]interface{}
	Raw              string
	Validated        bool
	ValidationErrors []string
	ProviderUsed     StructuredOutputMethod
}

// StructuredOutputMethod is the structured output implementation method
type StructuredOutputMethod string

const (
	MethodOpenAINative     StructuredOutputMethod = "openai_native"
	MethodAnthropicToolUse StructuredOutputMethod = "anthropic_tool_use"
	MethodFallbackPrompt   StructuredOutputMethod = "fallback_prompt"
)

// SchemaValidationError Schema verificationerror
type SchemaValidationError struct {
	Path    string
	Message string
}

// extractJSON extracts JSON from a text string
// tries four strategies in order: pure JSON, Markdown code block, curly brace matching, square bracket matching
func extractJSON(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("text is empty, cannot extract JSON")
	}

	// strategy 1: pure JSON - try parsing directly
	if json.Valid([]byte(text)) {
		return text, nil
	}

	// strategy 2: Markdown code block extraction
	if extracted, ok := extractFromCodeBlock(text); ok {
		return extracted, nil
	}

	// strategy 3: curly brace matching extraction
	if extracted, ok := extractBalanced(text, '{', '}'); ok {
		return extracted, nil
	}

	// strategy 4: square bracket matching extraction
	if extracted, ok := extractBalanced(text, '[', ']'); ok {
		return extracted, nil
	}

	return "", fmt.Errorf("cannot extract JSON from text")
}

// codeBlockRegex pre-compiled regex for extracting JSON from Markdown code blocks
var codeBlockRegex = regexp.MustCompile("(?s)```(?:json)?\\s*\\n(.*?)\\n```")

// extractFromCodeBlock extracts content from Markdown code block
func extractFromCodeBlock(text string) (string, bool) {
	matches := codeBlockRegex.FindStringSubmatch(text)
	if len(matches) >= 2 {
		candidate := strings.TrimSpace(matches[1])
		if json.Valid([]byte(candidate)) {
			return candidate, true
		}
	}
	return "", false
}

// extractBalanced extracts balanced bracket content from text
func extractBalanced(text string, open, close rune) (string, bool) {
	startIdx := -1
	depth := 0
	for i, r := range text {
		if r == open {
			if depth == 0 {
				startIdx = i
			}
			depth++
		} else if r == close {
			depth--
			if depth == 0 && startIdx >= 0 {
				candidate := text[startIdx : i+1]
				if json.Valid([]byte(candidate)) {
					return candidate, true
				}
				// continue finding next balanced segment
				startIdx = -1
			}
		}
	}
	return "", false
}

// validateAgainstSchema validates data against JSON Schema
// checks if required fields exist and field types match
func validateAgainstSchema(data map[string]interface{}, schema map[string]interface{}) []string {
	var errs []string

	required, _ := schema["required"].([]interface{})
	for _, r := range required {
		fieldName, _ := r.(string)
		if _, ok := data[fieldName]; !ok {
			errs = append(errs, fmt.Sprintf("missing required field: %s", fieldName))
		}
	}

	properties, _ := schema["properties"].(map[string]interface{})
	for fieldName, fieldSchema := range properties {
		value, exists := data[fieldName]
		if !exists {
			continue
		}
		propMap, _ := fieldSchema.(map[string]interface{})
		expectedType, _ := propMap["type"].(string)
		if expectedType == "" {
			continue
		}

		if expectedType == "object" {
			if nested, ok := value.(map[string]interface{}); ok {
				nestedErrs := validateAgainstSchema(nested, propMap)
				errs = append(errs, nestedErrs...)
			} else {
				errs = append(errs, fmt.Sprintf("field '%s' should be object type", fieldName))
			}
			continue
		}

		if expectedType == "array" {
			arr, ok := value.([]interface{})
			if !ok {
				errs = append(errs, fmt.Sprintf("field '%s' should be array type", fieldName))
				continue
			}
			itemsSchema, _ := propMap["items"].(map[string]interface{})
			if itemsSchema != nil {
				for i, item := range arr {
					itemErrs := validateArrayItem(item, itemsSchema, fmt.Sprintf("%s[%d]", fieldName, i))
					errs = append(errs, itemErrs...)
				}
			}
			continue
		}

		if err := validateFieldType(fieldName, value, expectedType); err != nil {
			errs = append(errs, err.Error())
		}
	}

	return errs
}

// validateArrayItem validates a single array item against its items schema
func validateArrayItem(item interface{}, itemsSchema map[string]interface{}, path string) []string {
	var errs []string
	itemType, _ := itemsSchema["type"].(string)
	if itemType == "" {
		return errs
	}

	switch itemType {
	case "object":
		if nested, ok := item.(map[string]interface{}); ok {
			nestedErrs := validateAgainstSchema(nested, itemsSchema)
			errs = append(errs, nestedErrs...)
		} else {
			errs = append(errs, fmt.Sprintf("%s should be object type", path))
		}
	case "array":
		arr, ok := item.([]interface{})
		if !ok {
			errs = append(errs, fmt.Sprintf("%s should be array type", path))
			return errs
		}
		nestedItems, _ := itemsSchema["items"].(map[string]interface{})
		if nestedItems != nil {
			for i, subItem := range arr {
				subErrs := validateArrayItem(subItem, nestedItems, fmt.Sprintf("%s[%d]", path, i))
				errs = append(errs, subErrs...)
			}
		}
	default:
		if err := validateFieldType(path, item, itemType); err != nil {
			errs = append(errs, err.Error())
		}
	}

	return errs
}

// validateFieldType validates if field value matches expected JSON Schema type
func validateFieldType(fieldName string, value interface{}, expectedType string) error {
	switch expectedType {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("field '%s' should be string type, got %T", fieldName, value)
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("field '%s' should be number type, got %T", fieldName, value)
		}
	case "integer":
		f, ok := value.(float64)
		if !ok {
			return fmt.Errorf("field '%s' should be integer type, got %T", fieldName, value)
		}
		if math.Trunc(f) != f {
			return fmt.Errorf("field '%s' should be integer type, but value is float %v", fieldName, f)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("field '%s' should be boolean type, got %T", fieldName, value)
		}
	case "array":
		if _, ok := value.([]interface{}); !ok {
			return fmt.Errorf("field '%s' should be array type, got %T", fieldName, value)
		}
	case "object":
		if _, ok := value.(map[string]interface{}); !ok {
			return fmt.Errorf("field '%s' should be object type, got %T", fieldName, value)
		}
	default:
		// unknown type, skip verification
	}
	return nil
}

// MinifySchema minifies JSON Schema, removing description/examples/default/title and other redundant fields
// recursively processes properties and items
func MinifySchema(schema map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// top-level keys to preserve
	keepKeys := map[string]bool{
		"type":       true,
		"properties": true,
		"required":   true,
		"items":      true,
		"enum":       true,
		"anyOf":      true,
		"oneOf":      true,
		"allOf":      true,
	}

	for k, v := range schema {
		if !keepKeys[k] {
			continue
		}

		switch k {
		case "properties":
			if props, ok := v.(map[string]interface{}); ok {
				minProps := make(map[string]interface{})
				for pk, pv := range props {
					if propMap, ok := pv.(map[string]interface{}); ok {
						minProps[pk] = MinifySchema(propMap)
					} else {
						minProps[pk] = pv
					}
				}
				result[k] = minProps
			} else {
				result[k] = v
			}

		case "items":
			if itemMap, ok := v.(map[string]interface{}); ok {
				result[k] = MinifySchema(itemMap)
			} else {
				result[k] = v
			}

		case "anyOf", "oneOf", "allOf":
			if arr, ok := v.([]interface{}); ok {
				minArr := make([]interface{}, len(arr))
				for i, item := range arr {
					if itemMap, ok := item.(map[string]interface{}); ok {
						minArr[i] = MinifySchema(itemMap)
					} else {
						minArr[i] = item
					}
				}
				result[k] = minArr
			} else {
				result[k] = v
			}

		default:
			result[k] = v
		}
	}

	return result
}

// buildStructuredOutputPrompt generates the structured output prompt
// includes schema JSON and key instructions, guiding model to output JSON conforming to schema
func buildStructuredOutputPrompt(schema *ResponseSchema) string {
	schemaJSON, _ := json.MarshalIndent(schema.Schema, "", "  ")

	var sb strings.Builder
	sb.WriteString("Please strictly output result according to the JSON Schema below. Output only JSON, no other content。\n\n")
	if schema.Description != "" {
		sb.WriteString(fmt.Sprintf("Output description: %s\n\n", schema.Description))
	}
	sb.WriteString("JSON Schema:\n```json\n")
	sb.WriteString(string(schemaJSON))
	sb.WriteString("\n```\n\n")
	sb.WriteString("Important instructions:\n")
	sb.WriteString("1. Output must be valid JSON conforming to the schema above\n")
	sb.WriteString("2. Do not add any text or Markdown markers before or after the JSON\n")
	sb.WriteString("3. Ensure all required fields exist with correct types\n")
	sb.WriteString("4. If the schema defines required fields, all must be included\n")

	return sb.String()
}

// buildRetryMessage generates the prompt message for structured output retry
// includes validation error and/or resolution error info
func buildRetryMessage(validationErrors []string, parseError error) string {
	var sb strings.Builder
	sb.WriteString("Previous structured output did not meet requirements, please output again。\n\n")

	if parseError != nil {
		sb.WriteString(fmt.Sprintf("JSON resolveerror: %s\n\n", parseError.Error()))
	}

	if len(validationErrors) > 0 {
		sb.WriteString("verificationerror:\n")
		for _, e := range validationErrors {
			sb.WriteString(fmt.Sprintf("- %s\n", e))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Please strictly output valid JSON per schema, ensuring all required fields exist with correct types。\n")

	return sb.String()
}
