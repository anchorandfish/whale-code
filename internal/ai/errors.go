package ai

import (
	stderrors "errors"
	"fmt"
	"sync"

	"github.com/anchorandfish/whale-code/internal/log"
)

// ErrorID 是结构化错误的唯一标识符，与 02 §2.3.2 预定义清单严格对齐。
type ErrorID string

const (
	// ErrAIApiOverload AI API 过载错误（HTTP 529）
	ErrAIApiOverload ErrorID = "ai_api_overload"
	// ErrAIApiServer AI API 服务端错误（HTTP 500/502/503/504）
	ErrAIApiServer ErrorID = "ai_api_server"
	// ErrAIApiRateLimit AI API 限流错误（HTTP 429）
	ErrAIApiRateLimit ErrorID = "ai_api_ratelimit"
	// ErrAIRetryable AI API 可重试错误（通用）
	ErrAIRetryable ErrorID = "ai_retryable"
	// ErrAIFatal AI API 致命错误（不可恢复）
	ErrAIFatal ErrorID = "ai_fatal"
	// ErrConfigInvalid 配置错误（无效参数/配置设置失败）
	// N14 引入：改进方案 §7.3 要求复用此 ErrorID，N6 遗漏，现补齐。
	ErrConfigInvalid ErrorID = "config_invalid"
	// ErrSecurityBlocked 安全拦截错误
	ErrSecurityBlocked ErrorID = "security_blocked"
	// ErrSecuritySandbox 安全沙箱错误
	ErrSecuritySandbox ErrorID = "security_sandbox"
	// ErrSecretLeak 密钥泄漏错误
	ErrSecretLeak ErrorID = "secret_leak"
	// ErrSecretMissing 密钥缺失错误
	ErrSecretMissing ErrorID = "secret_missing"
	// ErrAgentTimeout 代理超时错误
	ErrAgentTimeout ErrorID = "agent_timeout"
	// ErrAgentRecursionLimit 代理递归深度超限错误
	ErrAgentRecursionLimit ErrorID = "agent_recursion_limit"
	// ErrBridgeJWT bridge JWT 认证错误
	ErrBridgeJWT ErrorID = "bridge_jwt"
	// ErrBridgeReconnect bridge 重连错误
	ErrBridgeReconnect ErrorID = "bridge_reconnect"
	// ErrMemoryStore 记忆存储层错误（读写/删除/锁/文件上限）
	ErrMemoryStore ErrorID = "memory_store"
	// ErrMemoryEditor 记忆编辑器错误（$VISUAL/$EDITOR 启动失败）
	ErrMemoryEditor ErrorID = "memory_editor"
	// ErrMemoryIndex MEMORY.md 索引错误（加载/重建/截断）
	ErrMemoryIndex ErrorID = "memory_index"
	// ErrMemoryMigrate V1→V2 迁移错误（读取/扫描/重命名）
	ErrMemoryMigrate ErrorID = "memory_migrate"
	// ErrMemoryParse 记忆 frontmatter 解析错误（YAML/格式校验）
	ErrMemoryParse ErrorID = "memory_parse"
	// ErrMemoryAI 记忆 AI 调用错误（SelectRelevant/ExtractAndSave）
	ErrMemoryAI ErrorID = "memory_ai"
	// ErrKeybindingsInvalid keybindings 配置校验/解析错误（无效 mode/key/action/冲突/保留键覆盖）
	// D7 修复：N7 keybindings 模块接入 N6 ErrorID 命名空间
	ErrKeybindingsInvalid ErrorID = "keybindings_invalid"
	// ErrKeybindingsLoad keybindings 配置文件加载错误（路径校验/读取/JSON 解析）
	// D7 修复：N7 keybindings 模块接入 N6 ErrorID 命名空间
	ErrKeybindingsLoad ErrorID = "keybindings_load"
	// ErrRemoteConnect SSH 远程连接错误（Dial/握手/known_hosts 校验失败）
	// 修复：N23 远程模块接入 02 §2.3.2 ErrorID 命名空间
	ErrRemoteConnect ErrorID = "remote_connect"
	// ErrRemoteDeploy 远程 binary 部署错误（传输/SHA-256 校验/原子重命名失败）
	// 修复：N23 远程模块接入 02 §2.3.2 ErrorID 命名空间
	ErrRemoteDeploy ErrorID = "remote_deploy"
	// ErrRemoteExecute 远程工具执行错误（RPC 调用/远程沙箱失败）
	// 修复：N23 远程模块接入 02 §2.3.2 ErrorID 命名空间
	ErrRemoteExecute ErrorID = "remote_execute"
	// ErrRemoteHeartbeat 远程心跳错误（心跳超时/重连耗尽重试次数）
	// 修复：N23 远程模块接入 02 §2.3.2 ErrorID 命名空间
	ErrRemoteHeartbeat ErrorID = "remote_heartbeat"
	// ErrRemoteAuthProxy 远程认证代理错误（IPC 监听/API key 转发失败）
	// 修复：N23 远程模块接入 02 §2.3.2 ErrorID 命名空间
	ErrRemoteAuthProxy ErrorID = "remote_authproxy"
	// ErrRemoteThread 远程会话持久化错误（Thread 存储/加载/归档失败）
	// 修复：N23 远程模块接入 02 §2.3.2 ErrorID 命名空间
	ErrRemoteThread ErrorID = "remote_thread"
	// ErrGoalBlocked goal 状态阻塞错误（评估 Agent 判定目标偏离/无法达成，需用户干预）
	// N26 引入：goal 状态机进入 blocked 状态时返回，对标同类 CLI GoalStatus::Blocked。
	ErrGoalBlocked ErrorID = "goal_blocked"
	// ErrGoalUsageLimited goal 用量受限错误（评估 Agent 用尽预算或触发频控，需用户调整）
	// N26 引入：goal 状态机进入 usage_limited 状态时返回。
	ErrGoalUsageLimited ErrorID = "goal_usage_limited"
	// ErrGoalEvalFailed goal 评估失败错误（评估 Agent 调用失败/超时/解析失败，可降级为 Unverified）
	// N26 引入：评估 Agent 调用链失败时返回，触发 降级路径，不阻塞主流程。
	ErrGoalEvalFailed ErrorID = "goal_eval_failed"
)

// Category 是错误分类，用于决定错误处理策略。
type Category int

const (
	// CategoryRetryable 可重试错误，适合指数退避后重试
	CategoryRetryable Category = iota
	// CategoryFatal 致命错误，不可恢复，应立即终止
	CategoryFatal
	// CategoryUserActionable 用户可操作错误，需要用户干预
	CategoryUserActionable
	// CategorySystem 系统级错误，由系统自动处理
	CategorySystem
)

// Error 是结构化错误类型，实现 error 接口，包含错误 ID、分类、消息和原因。
type Error struct {
	ID       ErrorID
	Category Category
	Message  string
	Cause    error
}

// Error 实现 error 接口，返回格式化的错误消息。
func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.ID, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.ID, e.Message)
}

// Unwrap 实现 errors.Unwrap 接口，返回原始错误。
func (e *Error) Unwrap() error {
	return e.Cause
}

// IsRetryable 判断错误是否可重试。
func (e *Error) IsRetryable() bool {
	return e.Category == CategoryRetryable
}

// WithID 包装错误并附加 ErrorID，实现 02 §2.3.1 契约签名。
// 返回的 error 可通过 errors.As 提取 *Error 类型。
// Message 字段经过脱敏处理，确保敏感信息被替换为 [REDACTED]。
func WithID(err error, id ErrorID) error {
	if err == nil {
		return nil
	}
	var existing *Error
	if stderrors.As(err, &existing) {
		if existing.ID != "" {
			return err
		}
		existing.ID = id
		existing.Category = categoryFromID(id)
		return existing
	}
	return &Error{
		ID:       id,
		Category: categoryFromID(id),
		Message:  RedactMessage(err.Error()),
		Cause:    err,
	}
}

// GetErrorID 从错误链中提取 ErrorID。
// 支持 *Error 和 *APIError 两种携带 ErrorID 的错误类型。
// 若错误链中不包含这两种类型，返回空字符串。
func GetErrorID(err error) ErrorID {
	var e *Error
	if stderrors.As(err, &e) {
		return e.ID
	}
	var apiErr *APIError
	if stderrors.As(err, &apiErr) {
		return apiErr.ErrorID
	}
	return ""
}

// errorCategorySpecs 是 ErrorID → Category 的**唯一显式归属表**（，2026-09-21）。
//
// 为什么是「显式 map」而非 switch+default：
//
//	switch 带 default(=CategoryFatal) ⇒ 新增 ErrorID 漏登记时**零编译/测试信号**，
//	静默继承 CategoryFatal ⇒ IsRetryable() 恒 false ⇒ 瞬时故障永久不重试。
//	显式 map 让「漏登记」变成可判别：跑期守卫穷举 AllErrorIDs()、AST 门扫常量声明，
//	两侧都忘登记 ⇒ 至少一条门必红（见 error_category_registry_test.go / error_category_ast_gate_test.go）。
//
// ★ 语义与替换前 switch **逐字等价**（31 项，Retryable 4 / Fatal 5 / UserActionable 19 / System 3）——
//
//	本表只做「收口」，不改任何一条归属。
var errorCategorySpecs = map[ErrorID]Category{
	// Retryable（可重试，指数退避后重试）
	ErrAIApiServer:     CategoryRetryable,
	ErrAIRetryable:     CategoryRetryable,
	ErrBridgeJWT:       CategoryRetryable,
	ErrBridgeReconnect: CategoryRetryable,

	// Fatal（致命，不可恢复）
	// ⚠️ 策略反转：ErrAIApiOverload(529) / ErrAIApiRateLimit(429) 归为 Fatal（不可重试）
	// 对齐业内既有实现 ServerOverloaded.is_retryable = false。
	ErrAIApiOverload:   CategoryFatal,
	ErrAIApiRateLimit:  CategoryFatal,
	ErrAIFatal:         CategoryFatal,
	ErrSecurityBlocked: CategoryFatal,
	ErrSecuritySandbox: CategoryFatal,

	// UserActionable（需用户干预）
	ErrSecretLeak:         CategoryUserActionable,
	ErrSecretMissing:      CategoryUserActionable,
	ErrMemoryStore:        CategoryUserActionable,
	ErrMemoryEditor:       CategoryUserActionable,
	ErrMemoryIndex:        CategoryUserActionable,
	ErrMemoryMigrate:      CategoryUserActionable,
	ErrMemoryParse:        CategoryUserActionable,
	ErrMemoryAI:           CategoryUserActionable,
	ErrConfigInvalid:      CategoryUserActionable,
	ErrKeybindingsInvalid: CategoryUserActionable,
	ErrKeybindingsLoad:    CategoryUserActionable,
	ErrRemoteConnect:      CategoryUserActionable,
	ErrRemoteDeploy:       CategoryUserActionable,
	ErrRemoteExecute:      CategoryUserActionable,
	ErrRemoteHeartbeat:    CategoryUserActionable,
	ErrRemoteAuthProxy:    CategoryUserActionable,
	ErrRemoteThread:       CategoryUserActionable,
	ErrGoalBlocked:        CategoryUserActionable,
	ErrGoalUsageLimited:   CategoryUserActionable,

	// System（系统自动处理）
	ErrAgentTimeout:        CategorySystem,
	ErrAgentRecursionLimit: CategorySystem,
	ErrGoalEvalFailed:      CategorySystem,
}

// AllErrorIDs 返回全部 ErrorID 常量的显式枚举（供跑期守卫穷举）。
//
// ★ 本函数是「第三个清单」，与常量声明（errors.go 头部 const 块）和 errorCategorySpecs
// 的关系由两道守卫钉住：registry 测试双向比对本函数与 errorCategorySpecs，AST 门比对
// 常量声明与 errorCategorySpecs —— 任何一处漏登记都会被至少一道门抓红。
func AllErrorIDs() []ErrorID {
	return []ErrorID{
		ErrAIApiOverload, ErrAIApiServer, ErrAIApiRateLimit, ErrAIRetryable, ErrAIFatal,
		ErrConfigInvalid, ErrSecurityBlocked, ErrSecuritySandbox, ErrSecretLeak, ErrSecretMissing,
		ErrAgentTimeout, ErrAgentRecursionLimit, ErrBridgeJWT, ErrBridgeReconnect,
		ErrMemoryStore, ErrMemoryEditor, ErrMemoryIndex, ErrMemoryMigrate, ErrMemoryParse, ErrMemoryAI,
		ErrKeybindingsInvalid, ErrKeybindingsLoad,
		ErrRemoteConnect, ErrRemoteDeploy, ErrRemoteExecute, ErrRemoteHeartbeat, ErrRemoteAuthProxy, ErrRemoteThread,
		ErrGoalBlocked, ErrGoalUsageLimited, ErrGoalEvalFailed,
	}
}

// categoryFromID 根据 ErrorID 推断错误分类（查显式归属表 errorCategorySpecs）。
// ⚠️ 策略反转：ErrAIApiOverload(529) / ErrAIApiRateLimit(429) 归为 Fatal（不可重试）
// 对齐业内既有实现 ServerOverloaded.is_retryable = false。
// D7 修复：ErrSecurityBlocked/ErrSecuritySandbox 归为 Fatal（对齐开发 prompt §2.3）。
// D8 修复：兜底返回 CategoryFatal（保守分类，对齐开发 prompt §2.3）。
//
// ★ ：由 switch+default 改为查显式表 + 兜底。兜底 CategoryFatal 仍保留 ——
// 因为 categoryFromID 可能被传入**任意外来** ErrorID（如 GetErrorID 从 APIError 提取的未知值），
// 兜底是必要的 fail-closed；「新增常量漏登记」由 errorCategorySpecs 的两道守卫抓，不是靠删掉兜底。
func categoryFromID(id ErrorID) Category {
	if c, ok := errorCategorySpecs[id]; ok {
		return c
	}
	return CategoryFatal
}

// Redactor 接口定义脱敏函数签名，委托 H3 实现密钥脱敏。
type Redactor interface {
	Redact(msg string) string
}

// defaultRedactor 使用 log.RedactSensitive 实现默认脱敏。
type defaultRedactor struct{}

func (r *defaultRedactor) Redact(msg string) string {
	return log.RedactSensitive(msg)
}

var (
	redactor   Redactor = &defaultRedactor{}
	redactorMu sync.RWMutex
)

// SetRedactor 设置自定义脱敏器，供 H3 模块注入自定义实现。
func SetRedactor(r Redactor) {
	redactorMu.Lock()
	defer redactorMu.Unlock()
	if r != nil {
		redactor = r
	}
}

// RedactMessage 对消息进行脱敏处理。
func RedactMessage(msg string) string {
	redactorMu.RLock()
	defer redactorMu.RUnlock()
	return redactor.Redact(msg)
}
