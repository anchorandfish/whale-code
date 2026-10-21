package hook

import (
	"encoding/json"
	"fmt"
)

// parseHookResponse 解析钩子响应 JSON
// 支持 {"decision":"allow"/"deny", "message":"...", "updatedInput":{...}}
func (m *Manager) parseHookResponse(response string, eventType HookEventType) HookResult {
	var control struct {
		Decision     string          `json:"decision"`
		Message      string          `json:"message"`
		UpdatedInput json.RawMessage `json:"updatedInput"`
	}
	if err := json.Unmarshal([]byte(response), &control); err != nil {
		return HookResult{Action: ActionAllow, Output: response}
	}

	result := HookResult{Output: response}
	switch control.Decision {
	case "deny":
		result.Action = ActionBlock
		result.Reason = control.Message
		if eventType.IsLifecycleEvent() {
			result.Action = ActionAllow
			result.Messages = append(result.Messages, fmt.Sprintf("[hook warn] deny is ignored for lifecycle event %s: %s", eventType, control.Message))
		}
	case "allow":
		result.Action = ActionAllow
	default:
		result.Action = ActionAllow
	}

	if len(control.UpdatedInput) > 0 {
		updated := string(control.UpdatedInput)
		result.UpdatedInput = &updated
	}
	if control.Message != "" {
		result.Messages = append(result.Messages, control.Message)
	}

	return result
}

// parseHookJSONControl 解析钩子命令 stdout 的 JSON 控制输出
// 对标同类 CLI try_parse_pre_hook_json_control
func parseHookJSONControl(stdout []byte) (*HookJSONControl, bool) {
	var control HookJSONControl
	if err := json.Unmarshal(stdout, &control); err != nil {
		return nil, false
	}
	if control.Decision == "" && len(control.UpdatedInput) == 0 && control.Message == "" {
		return nil, false
	}
	return &control, true
}
