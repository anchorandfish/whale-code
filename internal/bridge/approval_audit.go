// approval_audit.go — 工具审批决策的持久化留痕（delta F，2026-09-14）。
//
// 归属：delta F。与 S9 delta E「授权记忆」是**两回事**，必须解耦：
// - delta E 管 `Policy.memory`（[A] 允许本会话的免问表），**严格会话级、从不落盘**；
// - delta F 管 `AuditLogger`（每次决策写一行**只写不读**的审计条目），**只写不读**。
//
// ★ 本文件**不**从审计文件回读任何东西到 Policy —— 那会让"留痕"退化成"持久授权"。
//
//	守卫测试 TestDeltaF_T1_AuditIsNotReadBackAsAuthorization 钉死这一点；
//	其判别力由探针 TestDeltaF_Probe_ReadbackTurnsT1Red（tui 包）证明：
//	注入「回读成授权」这一反例后，T1 的判据必须翻红。
//
// 通道选型（S13-HG Q1 族 C / Q2 = 甲）：**泛化** internal/bridge 既有审计实现，
// 而不是另起一套平行实现 —— S13-G1 的措辞是「全仓**唯一**专用审计实现……
// 生产零调用」，再开一个平行实现只会让 G1 更难收口。审批字段走 AuditEntry 的
// **新增列**（见 audit.go），不复用 TokenID/TokenMask 语义（避免硬塞）。
package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/anchorandfish/whale-code/internal/secret"
)

// approvalSchemaVersion 是审批审计条目的 schema 版本。
// 对齐业内既有实现 rollout-trace `raw_event.rs` 的版本化条目设计。
const approvalSchemaVersion = 1

// maxToolInputBytes 是脱敏后入参预览的字节上限。
const maxToolInputBytes = 2048

// redactedMarker 是 secret.RedactSensitive 输出的占位标记。
const redactedMarker = "[REDACTED]"

// redactedBlobPlaceholder 是 fail-closed 降级后写入的整串占位。
const redactedBlobPlaceholder = "[REDACTED-BLOB]"

// minSecretTailRun 是判定"残留尾字符"所需的**最短**密钥字符连段。
// 取 4：G3 的典型残留是密钥第 37 位起的尾部（可能只有几位），4 是下限；
// 取更小会更容易误伤普通文本，取更大则漏掉短残留。
const minSecretTailRun = 4

// ApprovalOrigin 标识审批决策来自哪条交互路径。
type ApprovalOrigin string

const (
	// OriginTUISubagent 子 agent（Agent 工具的确认通道 agentConfirmBridge）。
	OriginTUISubagent ApprovalOrigin = "tui-subagent"
	// OriginTUISandboxEscalate on-failure 沙箱升级询问（，2026-10-14）。
	OriginTUISandboxEscalate ApprovalOrigin = "tui-sandbox-escalate"
	// OriginTUIMain 主会话内联确认（exec / file / pipeline）。
	OriginTUIMain ApprovalOrigin = "tui-main"
	// OriginTUIElicitation MCP elicitation。
	OriginTUIElicitation ApprovalOrigin = "tui-elicitation"
	// OriginRPC 远端 SSH 会话的本地审批渲染。
	OriginRPC ApprovalOrigin = "rpc"
)

// ApprovalRecord 是一次工具审批决策的留痕输入。
//
// Decision / Source 取 internal/tools 的 **string** 值（`ConfirmDecision` /
// `ConfirmSource`），**不取** tui 的 `ApprovalDecision`（iota int）：后者同一个
// Esc 在 exec 与 elicitation 两条路径上语义漂移（一个判 Deny、一个判 Cancel），
// 不能作为审计真相源。
type ApprovalRecord struct {
	Origin    ApprovalOrigin
	Tool      string
	ToolInput string
	Decision  string
	Source    string
	SessionID string
	RequestID string
	// McpServer 仅 elicitation 填：是**哪个 MCP server** 在向用户要信息
	// （Tool 对 elicitation 恒为 "mcp_elicitation"，没有 server 名就还原不出"谁在问"）。
	McpServer string
}

// ApprovalAudit 是审批留痕通道。
//
// ★ 刻意**没有** Query / List / Load ——「只写不读」不靠调用方自觉，而是
// 接口层面就不给出读的口子。唯一方法 `Record` 只写盘。
//
// ⚠️ 注意与 `AuditLogger.Query` 的区别：后者是 token 审计的既有接口（生产零调用），
// 本通道**不暴露**它，以免 S9 的"只写不读"不变量被误当成"整个 bridge 包不许读"。
type ApprovalAudit interface {
	// Record 写入一条审批留痕。
	//
	// 返回的 error 仅供**可见化**（Q5：不阻断，但绝不静默失败）——调用方
	// **不得**因该 error 改变已做出的审批结果。
	Record(rec ApprovalRecord) error

	// RecordRequest 写入一条「确认请求已送达 TUI」的 request 侧留痕
	// （AuditOpToolApprovalRequest，2026-10-07）。
	//
	// 与 Record（decision 侧）经 ToolInputHash 关联：两条时间戳之差 = pending 时长。
	// 失败语义同 Record：不阻断，但必须可见。
	RecordRequest(rec ApprovalRecord) error
}

// approvalAudit 是 ApprovalAudit 的文件实现。
type approvalAudit struct {
	logger AuditLogger
	seq    atomic.Int64

	// fallback 是主 sink 写失败时的**降级出口**（C6：审计失败不阻断审批，但绝不静默）。
	//
	// nil ⇒ 用 os.Stderr，且**在写入时读取**（不是构造时快照）：这样测试可以临时
	// 重定向 os.Stderr 来断言"降级确实发生了"，也避免把启动瞬间的 stderr 固化下来。
	fallback io.Writer
}

// NewApprovalAudit 创建审批留痕通道。auditPath 为空 ⇒ 返回 noopAudit（不落盘）。
func NewApprovalAudit(auditPath string) ApprovalAudit {
	if auditPath == "" {
		return noopAudit{}
	}
	return &approvalAudit{logger: NewFileAuditLogger(auditPath)}
}

// fallbackWriter 返回降级出口（C6）。
func (a *approvalAudit) fallbackWriter() io.Writer {
	if a.fallback != nil {
		return a.fallback
	}
	return os.Stderr
}

// Record 写入一条审批留痕（O_APPEND + 跨进程 advisory lock，见 audit.go Log）。
//
// 失败语义（C6）：**不阻断**（决策已经做出，审计写失败不得改变已生效的审批结果），
// 但必须**可见** —— ① 返回 error 供调用方告警；② 本条降级写到 fallback sink（stderr）。
// 静默失败比"没有留痕"更危险：用户会以为有留痕。
func (a *approvalAudit) Record(rec ApprovalRecord) error {
	if a == nil || a.logger == nil {
		return nil
	}
	preview, blob := redactToolInput(rec.ToolInput)
	entry := &AuditEntry{
		Operation:      AuditOpToolApproval,
		Result:         approvalResultOf(rec.Decision),
		Detail:         approvalDetail(rec, blob),
		SchemaVersion:  approvalSchemaVersion,
		Seq:            a.seq.Add(1),
		Actor:          "user",
		Origin:         string(rec.Origin),
		Tool:           rec.Tool,
		ToolInput:      preview,
		ToolInputHash:  hashToolInput(rec.ToolInput),
		Decision:       rec.Decision,
		DecisionSource: rec.Source,
		SessionID:      rec.SessionID,
		RequestID:      rec.RequestID,
		McpServer:      rec.McpServer,
	}
	if err := a.logger.Log(entry); err != nil {
		a.degrade(entry, err)
		return fmt.Errorf("approval audit: %w", err)
	}
	return nil
}

// approvalAuditDegradedMarker 是降级行的前缀（grep 用；也是 C6 的判别锚点）。
const approvalAuditDegradedMarker = "[approval-audit-degraded]"

// RecordRequest 写入一条 request 侧留痕（AuditOpToolApprovalRequest）。
//
// 与 Record 的分工：Record 记「用户何时拍板」，本方法记「弹窗何时被置入待决」。
// 两者经 ToolInputHash 关联；只有 decision 侧时刻时，pending 时长无法归因给
// 「用户晚点」还是「弹窗晚弹」（2026-10-07 真机两次空档的教训）。
// 脱敏、追加写、跨进程锁、失败降级全部复用 Record 的同一套机制（C6/C8 不放松）。
func (a *approvalAudit) RecordRequest(rec ApprovalRecord) error {
	if a == nil || a.logger == nil {
		return nil
	}
	preview, _ := redactToolInput(rec.ToolInput)
	entry := &AuditEntry{
		Operation:      AuditOpToolApprovalRequest,
		Result:         AuditResultSuccess,
		Detail:         fmt.Sprintf("tool approval request surfaced origin=%s", rec.Origin),
		SchemaVersion:  approvalSchemaVersion,
		Seq:            a.seq.Add(1),
		Actor:          "system",
		Origin:         string(rec.Origin),
		Tool:           rec.Tool,
		ToolInput:      preview,
		ToolInputHash:  hashToolInput(rec.ToolInput),
		Decision:       "requested",
		DecisionSource: "surfaced",
		SessionID:      rec.SessionID,
		RequestID:      rec.RequestID,
		McpServer:      rec.McpServer,
	}
	if err := a.logger.Log(entry); err != nil {
		a.degrade(entry, err)
		return fmt.Errorf("approval request audit: %w", err)
	}
	return nil
}

// degrade 把"写失败的那一条"打到备用 sink（stderr），使这次审批**仍然留下可见痕迹**。
//
// 安全前提：`entry.ToolInput` 已经过 `redactToolInput` 的 fail-closed 脱敏
// （见该函数），因此这里把条目序列化到 stderr 不会引入新的泄露面。
//
// 降级自身**永不** panic、**永不**返回错误：它已经是最后一道出口，
// 再失败就只能放弃（此时调用方仍会拿到 Record 的 error 并告警）。
func (a *approvalAudit) degrade(entry *AuditEntry, cause error) {
	w := a.fallbackWriter()
	if w == nil {
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		// 理论上不可达（条目字段均为可序列化的 string/int）—— 但仍不 panic。
		data = []byte(fmt.Sprintf(`{"operation":%q,"decision":%q}`, entry.Operation, entry.Decision))
	}
	fmt.Fprintf(w, "%s %s (primary sink failed: %v)\n", approvalAuditDegradedMarker, data, cause)
}

// noopAudit 是未配置审计路径时的空实现：不落盘，也不报错。
type noopAudit struct{}

// Record 什么都不做。
func (noopAudit) Record(ApprovalRecord) error { return nil }

// RecordRequest 什么都不做（与 noopAudit.Record 同语义：未配置审计路径即不落盘）。
func (noopAudit) RecordRequest(ApprovalRecord) error { return nil }

// approvalResultOf 把决策类别映射为既有的 AuditResult（粗粒度 allow/deny）。
//
// 细粒度真相在 `Decision` 字段里 —— 这里只回答"这次调用有没有被放行"。
// 未知取值 fail-closed 记为 denied（不得把"未知决策"记成放行）。
func approvalResultOf(decision string) AuditResult {
	switch decision {
	case "approved", "approved_session":
		return AuditResultSuccess
	default:
		return AuditResultDenied
	}
}

// approvalDetail 生成一行人类可读摘要。
// 结构化事实（tool/origin/decision/…）各有独立字段，**不**编进这里再解析
// （R1 契约 C4：不把结构化事实塞进字符串）。
func approvalDetail(rec ApprovalRecord, blob bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tool approval decision=%s source=%s origin=%s", rec.Decision, rec.Source, rec.Origin)
	if blob {
		b.WriteString(" input=redacted-blob")
	}
	return b.String()
}

// redactToolInput 对工具入参做 **fail-closed** 脱敏（Q7 = C8）。
//
// 三步，顺序不可换：
// 1. `secret.RedactSensitive` —— 常规脱敏（H2 契约的**唯一入口**）
// 2. **两道复检**：
// a) `secret.IsSecretLeak` —— 输出里是否还留着**完整**的已知密钥形状
// b) `residualSecretTail` —— 输出里是否留着**半截**密钥（S14-G3 形状）
// ★ 只有 (a) 是不够的 —— 实测：`ghp_`+36位+`ZZZZ` 经 RedactSensitive 后得到
// `[REDACTED]ZZZZ`，前缀已被吃掉 ⇒ 任何已知 pattern 都**不再匹配**，
// IsSecretLeak 返回 false，尾字符照样落盘。必须补 (b)。
// 3. 任一命中 ⇒ 整串降级为 `[REDACTED-BLOB]`（fail-closed：宁可丢预览，
// 也不在盘上留半个密钥）。双双未命中 ⇒ 才允许截断成预览。
//
// ★「截断」与「fail-closed」的关系（防止被读成互相矛盾）：
//
//	二者是**先后**关系，不是替代关系。fail-closed 决定"还能不能留预览"；
//	截断只在**判定安全之后**限制预览的**长度**。命中侧**不截断** —— 对一个
//	已判定含残留密钥的串做截断，等于把残片重新放回盘上。
//
// ⚠️ 审计 JSONL 走 os.WriteFile，**不经过 slog 的 Redact handler**
//
//	（那是 `NewChainedHandler` 装饰器，只覆盖 slog 输出）⇒ 这里必须**显式**脱敏，
//	指望 handler 兜底 = 零覆盖。
func redactToolInput(raw string) (preview string, blob bool) {
	if raw == "" {
		return "", false
	}
	redacted := secret.RedactSensitive(raw)
	if secret.IsSecretLeak(redacted) || residualSecretTail(redacted) {
		return redactedBlobPlaceholder, true
	}
	return truncateUTF8Bytes(redacted, maxToolInputBytes), false
}

// residualSecretTail 检测"脱敏残留密钥尾字符"（S14-G3 的形状）：
// 占位标记 `[REDACTED]` **紧邻**一段 ≥ minSecretTailRun 个密钥字符。
//
// 成因（密钥_STATUS.md §一 G3）：internal/secret 用**定长**量词
// （`ghp_{36}` / `AIza{35}`）匹配，只吃掉密钥前 N 位；第 N+1 位起的尾字符原样
// 留在输出里（`ghp_AAA…ZZZZ` → `[REDACTED]ZZZZ`）。
//
// ⚠️ 这是 delta F 的**补偿控制**，不是根治 —— 根治在 internal/secret 把
//
//	`{36}`/`{35}` 改成 `{36,}`/`{35,}`（属 S14 域）。之所以仍要在审计侧兜底：
//	审计是**最后一道落盘关口**，一旦这里放行，半个密钥就永久留在盘上。
func residualSecretTail(s string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], redactedMarker)
		if j < 0 {
			return false
		}
		i += j + len(redactedMarker)
		run := 0
		for i+run < len(s) && isSecretTailByte(s[i+run]) {
			run++
		}
		if run >= minSecretTailRun {
			return true
		}
	}
}

// isSecretTailByte 报告 b 是否属于密钥字符集（base64url 字母表）。
func isSecretTailByte(b byte) bool {
	switch {
	case b >= '0' && b <= '9', b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z':
		return true
	case b == '-' || b == '_':
		return true
	}
	return false
}

// truncateUTF8Bytes 在**字节**上限内截断，且不切断 UTF-8 序列。
func truncateUTF8Bytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…[truncated]"
}

// hashToolInput 返回原始入参的 sha256（供关联，不可从摘要反推原文）。
// 空入参返回空串（避免在审计里留一个无意义的常量摘要）。
func hashToolInput(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}
