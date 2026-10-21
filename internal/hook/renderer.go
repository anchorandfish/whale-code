package hook

import "strings"

// renderTemplate 替换模板变量
func (m *Manager) renderTemplate(tmpl string, payload HookPayload) string {
	result := tmpl
	result = strings.ReplaceAll(result, "{{tool_name}}", payload.ToolName)
	result = strings.ReplaceAll(result, "{{file_path}}", payload.FilePath)
	result = strings.ReplaceAll(result, "{{command}}", payload.Command)
	result = strings.ReplaceAll(result, "{{tool_output}}", payload.ToolOutput)
	result = strings.ReplaceAll(result, "{{event}}", payload.HookEventName)
	return result
}
