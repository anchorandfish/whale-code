package tui

import (
	"encoding/json"
	"strings"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// approvalTargetSummary describes the concrete operation instead of exposing
// only the internal tool name to the user.
//
// ★ ：修前是满屏 "Operation / 操作: …" 行内并列（用户 2026-09-18
// 已否决的形态：中文更宽，TUI 里被硬换行切成碎片）。
// ★ （2026-10-12 同日修订）：本摘要改**英文单行**。确认弹窗是模态决策面，
// 30 行小终端里弹窗高度是硬预算 —— B1 的「每行都加中文辅行」把框体撑到 25 行，
// 实测 lastInputAreaTop=0（输入区被顶出屏幕，既有夹具红，见 回执 §A/B）。
// 中文辅行因此只保留在**决策要素**上（标题 / 选项 / Risk 内容）；本摘要的标签属
// 低价值辅行候选，按用户原话「把**一些**英文翻为中文」取英文单行（合规）。
// 命令/路径/工具名是语言中性的数据，随主行出现。
func approvalTargetSummary(toolName, input string) string {
	// ★ P-002（2026-09-22 反缝补扫描）：命令载体的判据唯一来源 =
	// `permission.IsCommandBearingTool`（= {bash, monitor}）。修复前是手抄的小写名对
	// `case "bash", "powershell":`（注意大小写还与别处不一致 —— 手抄名单的典型症状）
	// ⇒ Monitor 的确认框退化成 default 分支的原始 JSON 输入预览：用户在**唯一能看到
	// "到底要执行什么"的地方**看不到命令，只看到一串 JSON。
	if permission.IsCommandBearingTool(toolName) {
		command := strings.TrimSpace(permission.ExtractCommand(input))
		if command == "" {
			command = strings.TrimSpace(input)
		}
		command = truncateRunes(command, 2000)
		return "Operation: Run command\n" +
			"Command: " + command + "\n" +
			"Tool: " + toolName
	}
	switch strings.ToLower(toolName) {
	case "write_file":
		return approvalFileSummary("Write file", toolName, input)
	case "edit_file", "notebookedit":
		return approvalFileSummary("Modify file", toolName, input)
	default:
		inputPreview := truncateRunes(strings.TrimSpace(input), 200)
		return "Operation: Call tool\n" +
			"Tool: " + toolName + "\n" +
			"Input: " + inputPreview
	}
}

// approvalFileSummary 文件类工具的确认摘要（：英文单行，高度预算理由
// 见 approvalTargetSummary 的 B' 注释 —— 中文辅行集中在标题/选项/Risk）。
func approvalFileSummary(operation, toolName, input string) string {
	path := strings.TrimSpace(permission.ExtractFilePath(input))
	if path == "" {
		path = "(path unavailable)"
	}
	path = truncateRunes(path, 240)

	var fields struct {
		Content string `json:"content"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	_ = json.Unmarshal([]byte(input), &fields)

	details := ""
	if toolName == "write_file" && fields.Content != "" {
		content := approvalInlinePreview(fields.Content)
		details = "\nContent: " + content +
			"\nExecution: file write only; no shell command"
	} else if toolName != "write_file" && (fields.OldText != "" || fields.NewText != "") {
		oldText := approvalInlinePreview(fields.OldText)
		newText := approvalInlinePreview(fields.NewText)
		details = "\nOld text: " + oldText +
			"\nNew text: " + newText +
			"\nExecution: file edit only; no shell command"
	}
	return "Operation: " + operation + "\n" +
		"File: " + path + "\n" +
		"Tool: " + toolName + details
}

func approvalInlinePreview(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return truncateRunes(value, 1000)
}
