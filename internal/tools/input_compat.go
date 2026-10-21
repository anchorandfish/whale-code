package tools

import (
	"encoding/json"
	"strings"
)

// normalizeToolCallInput repairs known provider/model serialization quirks
// before the input reaches schema validation or a tool handler.
func normalizeToolCallInput(call Call) Call {
	if NormalizeToolName(call.Name) != "AskUser" {
		return call
	}

	if normalized, ok := normalizeAskUserInput(call.Input); ok {
		call.Input = normalized
	}
	return call
}

// normalizeAskUserInput accepts a JSON-encoded options array. Some OpenAI-
// compatible models also append fields such as default_idx inside that string.
func normalizeAskUserInput(input string) (string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &fields); err != nil {
		return input, false
	}

	rawOptions, exists := fields["options"]
	if !exists {
		return input, false
	}

	var encodedOptions string
	if err := json.Unmarshal(rawOptions, &encodedOptions); err != nil {
		return input, false
	}

	encodedOptions = strings.TrimSpace(encodedOptions)
	if !strings.HasPrefix(encodedOptions, "[") {
		return input, false
	}

	decoder := json.NewDecoder(strings.NewReader(encodedOptions))
	var options []AskUserOption
	if err := decoder.Decode(&options); err != nil {
		return input, false
	}

	normalizedOptions, err := json.Marshal(options)
	if err != nil {
		return input, false
	}
	fields["options"] = normalizedOptions

	mergeAskUserStringSuffix(fields, encodedOptions[decoder.InputOffset():])
	normalized, err := json.Marshal(fields)
	if err != nil {
		return input, false
	}
	return string(normalized), true
}

func mergeAskUserStringSuffix(fields map[string]json.RawMessage, suffix string) {
	suffix = strings.TrimSpace(suffix)
	suffix = strings.TrimSpace(strings.TrimPrefix(suffix, ","))
	if suffix == "" {
		return
	}

	var extra map[string]json.RawMessage
	if err := json.Unmarshal([]byte("{"+suffix+"}"), &extra); err != nil {
		return
	}

	for _, key := range []string{"default_idx", "category", "allow_free_text", "context"} {
		if _, exists := fields[key]; exists {
			continue
		}
		if value, exists := extra[key]; exists {
			fields[key] = value
		}
	}
}
