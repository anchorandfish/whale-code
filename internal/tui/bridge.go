package tui

import (
	"bufio"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	stdlog "log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/agentguard"
	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/bridge"
	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/mention"
	"github.com/anchorandfish/whale-code/internal/secret"
	"github.com/anchorandfish/whale-code/internal/session"
	sig "github.com/anchorandfish/whale-code/internal/signal"
	"github.com/anchorandfish/whale-code/internal/skill"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/anchorandfish/whale-code/internal/whalehome"
)

const maxBridgeImageSize = 10 * 1024 * 1024 // 10MB

// maxBridgeScannerBuffer is the maximum scanner buffer size for stdio messages
// It must be larger than maxBridgeImageSize to accommodate base64-encoded image data
const maxBridgeScannerBuffer = 12 * 1024 * 1024 // 12MB

// bridgeStreamCompletedSentinel 标记 dispatchBridgeCommand 内已触发 AI 流式响应
// （streamAIRequest 已发送 text + done 通知），handleBridgeCommand 检测到此哨兵值时
// 跳过重复的 done 通知，仅发送 JSON-RPC result（B7-P1-2 security-review 命令使用）。
const bridgeStreamCompletedSentinel = "__BRIDGE_STREAM_COMPLETED__"

// BridgeRunner communicates with IDE extension via JSON-RPC over stdio
// reuses PlainRunner core logic, converts I/O to JSON-RPC protocol
type BridgeRunner struct {
	*runnerCore

	// Bridge-specific fields
	encoder         *json.Encoder        // JSON-RPC output encoder (stdout)
	encoderMu       sync.Mutex           // output mutex
	cancelMu        sync.Mutex           // cancelFunc mutex
	cancelFunc      context.CancelFunc   // used to abort current generation
	continueSession bool                 // whether to continue previous session
	authProvider    *bridge.AuthProvider // JWT authentication provider
	commandGuard    *bridge.CommandGuard // IDE 命令校验桥接器（P1-5，委托 H2）
	// shutdown 跨模式优雅退出协调器（修复点6）：注册 cancelFunc 与 cleanup，
	// 收到 SIGINT/SIGTERM 时分层退出，stdin EOF 时主动 Shutdown 执行清理。
	shutdown *sig.GracefulShutdown
	// guard P0 agent 防护（agent 防护体系 2026-08-31）：轮次上限/无进展熔断/
	// 审查子循环上限/进度检测。handleChat 时 BeginRequest 归零；
	// streamAIRequest 顶部统一闸门判定。nil 时防线降级放行（canary 日志）。
	guard *agentguard.Guard
}

// JSON-RPC message structures

// jsonRPCRequest represents a JSON-RPC 2.0 request
type jsonRPCRequest struct {
	JSONRPC string                 `json:"jsonrpc"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params,omitempty"`
	ID      *int                   `json:"id,omitempty"`
}

// jsonRPCNotification represents a JSON-RPC 2.0 notification
type jsonRPCNotification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// jsonRPCResponse represents a JSON-RPC 2.0 response
type jsonRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
	ID      int           `json:"id"`
}

// jsonRPCError represents a JSON-RPC 2.0 error
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// streamParams streaming notification parameters
type streamParams struct {
	Type         string                 `json:"type"`
	Content      string                 `json:"content,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Input        map[string]interface{} `json:"input,omitempty"`
	Output       string                 `json:"output,omitempty"`
	ExitCode     int                    `json:"exitCode,omitempty"`
	InputTokens  int                    `json:"inputTokens,omitempty"`
	OutputTokens int                    `json:"outputTokens,omitempty"`
}

// NewBridgeRunner 创建 Bridge 模式 runner 并完成安全组件接线。
// P0-13 修复：fail-closed — 当 BridgeEnabled=true 且 JwtSecret=="" 时返回 error 拒绝启动，
// 避免攻击者伪造任意 JWT（原实现跳过验签为 fail-open）。
func NewBridgeRunner(cfg *config.Config, client *ai.SimpleClient, reg *ai.ModelRegistry, continueSession bool) (*BridgeRunner, error) {
	workDir, _ := os.Getwd()
	homeDir, _ := os.UserHomeDir()
	sessionDir := filepath.Join(whalehome.Dir(), "sessions")

	core := NewRunnerCore(cfg, client, reg, workDir, homeDir, sessionDir, "English")

	runner := &BridgeRunner{
		runnerCore:      core,
		encoder:         json.NewEncoder(os.Stdout),
		continueSession: continueSession,
		guard:           newAgentGuard(cfg),
		authProvider: bridge.NewAuthProvider(bridge.AuthProviderConfig{
			SchedulerConfig: bridge.RefreshSchedulerConfig{
				RefreshBufferMs:     300000,
				MaxRefreshFailures:  3,
				RefreshRetryDelayMs: 60000,
				FallbackIntervalMs:  1800000,
			},
		}),
	}

	// N10 B1 修复：接入 TokenManager（验签）/ ReconnectManager（401/403 重连）/ CommandGuard（命令校验）
	// 消费 BridgeConfig 6 项配置，使 P0-1/P0-2/P0-3/P1-4/P1-5 安全修复在生产路径生效。
	// P0-13 修复：fail-closed — bridge_enabled=true 时必须配置 jwt_secret，空 secret 直接返回 error 拒绝启动。
	bc := cfg.Bridge
	if cfg.BridgeEnabled && bc.JwtSecret == "" {
		// P0-13: 空 secret 时 fail-closed 拒绝启动（原实现跳过验签为 fail-open，攻击者可伪造任意 JWT）
		return nil, fmt.Errorf("bridge_enabled=true but jwt_secret is empty: fail-closed 拒绝跳过验签 (P0-13)")
	}
	if cfg.BridgeEnabled {
		// D4：密钥读取经 secret.SafeEnv（白名单过滤，避免泄露到子进程）
		// 此处 jwt_secret 已由 config 从 env/settings.json 读入，SafeEnv 用于后续子进程 env 过滤
		tokenMgr := bridge.NewTokenManagerWithVerify(nil, bridge.TokenManagerOptions{
			Secret:           bc.JwtSecret,
			ExpectedIssuer:   bc.ExpectedIssuer,
			ExpectedAudience: bc.ExpectedAudience,
			ClockSkewSeconds: bc.ClockSkewSeconds,
		})
		runner.authProvider.SetTokenManager(tokenMgr)

		// P1-4：401/403 reconnect（指数退避 + 抖动，MaxAttempts 从配置读取）
		// B2 修复：refreshToken 回调由 SetReconnectManager 自动注入（委托 scheduler 刷新逻辑）
		reconnectConfig := bridge.DefaultReconnectConfig
		if bc.ReconnectMaxAttempts > 0 {
			reconnectConfig.MaxAttempts = bc.ReconnectMaxAttempts
		}
		reconnectMgr := bridge.NewReconnectManager(reconnectConfig, nil)
		runner.authProvider.SetReconnectManager(reconnectMgr) // 内部自动调 SetRefreshToken 注入回调
	}

	// P1-5：IDE 命令校验委托 H2（CommandGuard，enabled 由配置控制）
	runner.commandGuard = bridge.NewCommandGuard(bc.CommandGuardEnabled)

	// Register common command callbacks (undo, redo, skills, plugins, hooks, etc.)
	registerCommonCommands(core,
		func() string { return runner.handleUndo() },
		func() string { return runner.handleRedo() },
		nil, nil, nil, nil, // plugin callbacks not needed in bridge mode
		nil, nil, // github, daemon not needed in bridge mode
	)
	// 阶段 4：补接 bridge 路径的 OnCheckpoint 和 OnRewind 回调（当前缺失）
	runner.CmdRegistry.OnCheckpoint = func(args string) string {
		return runner.handleCheckpoint(args)
	}
	runner.CmdRegistry.OnRewind = func(args string) string {
		return runner.handleRewind(args)
	}
	runner.CmdRegistry.OnMcp = func(args string) string {
		return command.NewMcpCommandHandler(
			runner.Executor,
			runner.WorkDir,
		)(args)
	}
	runner.CmdRegistry.OnEnv = func(args string) string {
		return command.NewEnvCommandHandler(
			command.NewConfigEnvPersister(
				func() map[string]string { return runner.Config.WhaleEnv },
				func() error {
					return config.SaveToFile(runner.Config, filepath.Join(whalehome.Dir(), "settings.json"))
				},
			),
			runner.CmdRegistry.EnvStore, // CMD-P1-004: 进程内 EnvStore，废除 os.Setenv
		)(args)
	}
	runner.CmdRegistry.OnOutputStyle = func(args string) string {
		return command.NewOutputStyleCommandHandler(
			&bridgeOutputStyleSetter{
				config: runner.Config,
				saver: func() error {
					return config.SaveToFile(runner.Config, filepath.Join(whalehome.Dir(), "settings.json"))
				},
			},
		)(args)
	}
	runner.CmdRegistry.OnTerminalSetup = func(args string) string {
		return command.NewTerminalSetupCommandHandler()(args)
	}
	runner.CmdRegistry.OnMigrate = func(args string) string {
		return command.NewMigrateCommandHandler(homeDir)(args)
	}
	runner.CmdRegistry.OnSkillExecute = func(skillName, args string) string {
		result, err := runner.Executor.SkillMgr.Execute(skillName, args)
		if err != nil {
			return ""
		}
		return result
	}
	// OnSkillExecuteWithMeta 带 meta 的 skill 执行回调（供需要 model/allowedTools 的调用方使用）
	runner.CmdRegistry.OnSkillExecuteWithMeta = func(skillName, args string) (skill.SkillResult, error) {
		return runner.Executor.SkillMgr.ExecuteWithMeta(skillName, args)
	}

	// P0-16 修复（v4 模块审查）：接线 /import 命令回调到真实的导入逻辑。
	// 原实现 Bridge 未接线 OnImport，走 handleImport 桩（command.go:751-760），
	// 返回 "Session imported" 但不真正导入，导致假成功。
	// 修复后调用 runnerCore.handleImportSessionShared，与 TUI 模式行为一致。
	runner.CmdRegistry.OnImport = func(args string) (string, error) {
		return runner.handleImportSessionShared(args), nil
	}

	// B7-P1-1 修复：接线 /summary 命令回调到 handleSummaryShared。
	// 原实现 Bridge 未接线 OnSummary，走 Registry 桩返回 "Generating summary..."（假成功）。
	// 修复后调用 runnerCore.handleSummaryShared 生成基于统计的真实摘要，与 TUI 模式行为一致。
	runner.CmdRegistry.OnSummary = func() string {
		return runner.handleSummaryShared()
	}

	// B7-P1-6 修复：接线 /compact-strategy 命令回调到 handleCompactStrategyShared。
	// 原实现 Bridge 未接线 OnCompactStrategy，走 Registry 桩返回
	// "switched to: <args> (Note: strategy only takes effect in memory)"（假成功，不持久化）。
	// 修复后调用 runnerCore.handleCompactStrategyShared 实际更新 Config 并持久化到 settings.json。
	runner.CmdRegistry.OnCompactStrategy = func(strategy string) string {
		return runner.handleCompactStrategyShared(strategy)
	}

	// set authentication callback
	// D4：token 日志/通知经 secret.RedactSensitive 脱敏（H3 委托，02 §2.3.1 契约）
	runner.authProvider.SetOnTokenUpdate(func(newToken string) {
		runner.sendNotification("tokenRefreshed", map[string]interface{}{
			"token": secret.RedactSensitive(bridge.MaskToken(newToken)),
		})
	})
	runner.authProvider.SetOnStateChange(func(state string) {
		runner.sendNotification("tokenStatus", map[string]interface{}{
			"state": state,
		})
	})
	// T1-P1-A1：注册异步重连完成回调，发 reconnectStatus JSON-RPC 通知给 IDE
	// success=true 时 err 为 nil；success=false 时 err 经脱敏后传给 IDE（不传 token 原文，吸纳审查优化 4）
	// IDE 收到通知后可自动重发上一个失败的 chat（IDE 侧实现，本 DEC 不涉及）
	runner.authProvider.SetOnReconnectComplete(func(success bool, err error) {
		notif := map[string]interface{}{
			"success": success,
		}
		if err != nil {
			notif["error"] = secret.RedactSensitive(err.Error())
		}
		runner.sendNotification("reconnectStatus", notif)
	})

	// initialize tool execution pipeline
	runner.Executor.InitPipelineWithHooks(runner.Policy, workDir)

	// P0-7/B7: NewBridgeRunner 重复调用 InitPipelineWithHooks 会重置内部 handlers，
	// 故在此处重新启用插件（NewRunnerCore 中的 EnableAllPlugins 已被覆盖）。
	// 仅当 --enable-plugins 显式启用时执行。
	if runner.Config.PluginsEnabled {
		// M20-04：注册前清空累计器，注册后 drain 失败项并输出到 stderr/日志。
		tools.ResetToolRegistrationFailures()
		runner.Executor.EnableAllPlugins()
		if failures := tools.DrainToolRegistrationFailures(); len(failures) > 0 {
			fmt.Fprintf(os.Stderr, "⚠️ %d plugin tool(s) failed to register during startup:\n", len(failures))
			fmt.Fprint(os.Stderr, tools.FormatToolRegistrationFailures(failures))
			stdlog.Printf("[plugin] %d tool(s) failed to register during bridge startup", len(failures))
		}
	}

	// discover and register MCP tools
	// S6/G8 ③ 阶段一：失败不再静默 —— error 落 ~/.whale/logs/whale.log（同 lifecycle.go）。
	// bridge 是第二个生产调用点；只改 lifecycle.go 会让本模式下的 MCP 失败继续完全静默。
	go func() {
		if err := runner.Executor.DiscoverAndRegisterMCPTools(workDir); err != nil {
			stdlog.Printf("[mcp] tool discovery failed: %v", err)
		}
	}()

	// 修复点6：创建 GracefulShutdown 协调器，注册 cancelFunc 与 cleanup。
	// - RegisterCancel：信号触发时调 cancelFunc 中断当前 AI 流（与 handleAbort 等价）
	// - RegisterCleanup：信号/EOF 触发时执行 cleanupOnExit（保存会话 + Close AIClient + Persist FileCache）
	// 注意 cancelFunc 可能在 handleChat/handleAbort 中被 nil 化，注册的是 *cancelFunc 的
	// 间接调用包装，运行时通过 cancelMu 保护读取。
	runner.shutdown = sig.NewGracefulShutdown()
	runner.shutdown.RegisterCancel(func() {
		runner.cancelMu.Lock()
		cf := runner.cancelFunc
		runner.cancelMu.Unlock()
		if cf != nil {
			cf()
		}
	})
	runner.shutdown.RegisterCleanup(func() error {
		runner.cleanupOnExit()
		return nil
	})

	// §4.1 + §4.5 修复：非 TUI 模式补齐 security.Init + sandbox.SetEnabled + wireH11N27Memory。
	// 等价于 TUI 模式 lifecycle.go 中的 4 步生产初始化，必须在 registerCommonCommands 之后调用。
	// wiringCancel 保存到 runnerCore，Close 时自动取消 ForgettingScanner goroutine。
	runner.wiringCancel = initProductionWiring(cfg, client, runner.runnerCore, homeDir, workDir)

	return runner, nil
}

// Run starts the Bridge mode main loop
// reads JSON-RPC requests from stdin, processes and sends notifications/responses via stdout
//
// 信号处理（修复点6）：Run() 开头安装 SIGINT/SIGTERM handler，分层退出语义
// 由 GracefulShutdown.HandleSignal 实现（首次优雅中断流+清理，2s 内二次强制 os.Exit(130)）。
// scanner 循环结束（stdin EOF）后主动调 Shutdown(false) 执行 cleanupOnExit，
// 保证 cancelFunc 被清空、AIClient 被关闭、FileCache 被持久化、会话被保存。
func (r *BridgeRunner) Run() error {
	// 修复点6：安装信号 handler，分层退出
	sig.InstallSignalHandler(r.shutdown)

	// send ready notification
	r.sendNotification("ready", map[string]interface{}{
		"modelName": r.ModelName,
		"workDir":   r.WorkDir,
	})

	// send initial auth status
	if status := r.authProvider.GetStatus(); status.IsValid {
		r.sendNotification("tokenStatus", map[string]interface{}{
			"isValid":      status.IsValid,
			"expiresIn":    status.ExpiresIn,
			"state":        status.State,
			"refreshState": status.RefreshState,
			"source":       status.Source,
		})
	}

	// if continuing previous session, auto-resume
	if r.continueSession {
		r.resumeLastSession()
	}

	scanner := bufio.NewScanner(os.Stdin)
	// increase buffer to support long messages; must exceed max image size to handle base64 data
	scanner.Buffer(make([]byte, 0, 1024*1024), maxBridgeScannerBuffer)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			r.sendError(nil, -32700, "Parse error")
			continue
		}

		r.handleRequest(req)
	}

	// 修复点6：stdin EOF 退出时主动 Shutdown，执行 cancelFunc 清空 + cleanupOnExit
	// （保存会话 + Close AIClient + Persist FileCache）。once 保证重复调用幂等。
	log.Info("Bridge stdin EOF, shutting down gracefully")
	r.shutdown.Shutdown(false)

	return nil
}

// parseBridgeImages parses image blocks from JSON-RPC params
// Uses a helper function to avoid deeply nested type assertions in handleRequest
func parseBridgeImages(params map[string]interface{}) []session.ImageBlock {
	imagesRaw, ok := params["images"]
	if !ok {
		return nil
	}

	imagesArr, ok := imagesRaw.([]interface{})
	if !ok {
		return nil
	}

	var images []session.ImageBlock
	for _, imgRaw := range imagesArr {
		imgMap, ok := imgRaw.(map[string]interface{})
		if !ok {
			continue
		}
		mediaType, _ := imgMap["mediaType"].(string)
		base64Data, _ := imgMap["base64Data"].(string)
		if len(base64Data) > maxBridgeImageSize {
			continue
		}
		if mediaType != "" && base64Data != "" {
			images = append(images, session.ImageBlock{
				MediaType:  mediaType,
				Base64Data: base64Data,
			})
		}
	}
	return images
}

// handleRequest processes a single JSON-RPC request
func (r *BridgeRunner) handleRequest(req jsonRPCRequest) {
	switch req.Method {
	case "chat":
		message, _ := req.Params["message"].(string)
		if message == "" {
			r.sendError(req.ID, -32602, "Missing 'message' parameter")
			return
		}
		// parse images parameter (from extension clipboard image paste)
		bridgeImages := parseBridgeImages(req.Params)
		r.handleChat(message, req.ID, bridgeImages)
	case "newSession":
		r.handleNewSession(req.ID)
	case "continueSession":
		r.resumeLastSession()
		r.sendResult(req.ID, "ok")
	case "abort":
		r.handleAbort()
		r.sendResult(req.ID, "ok")
	case "setToken":
		token, _ := req.Params["token"].(string)
		source, _ := req.Params["source"].(string)
		if source == "" {
			source = "ide"
		}
		r.handleSetToken(token, source, req.ID)
	case "tokenStatus":
		r.handleTokenStatus(req.ID)
	case "refreshToken":
		r.handleRefreshToken(req.ID)
	default:
		r.sendError(req.ID, -32601, fmt.Sprintf("Method not found: %s", req.Method))
	}
}

// handleChat processes chat request
func (r *BridgeRunner) handleChat(message string, id *int, bridgeImages []session.ImageBlock) {
	// 斜杠命令检测：命中时走命令调度，不进入 AI 流程
	// 走唯一谓词 command.IsCommandInput（不再裸 HasPrefix）。
	if command.IsCommandInput(message) {
		r.handleBridgeCommand(message, id)
		return
	}

	r.cancelMu.Lock()
	if r.cancelFunc != nil {
		r.cancelFunc()
		r.cancelFunc = nil
	}
	r.cancelMu.Unlock()

	// parse mention
	mentions := mention.DetectMentions(message)
	resolved := mention.ResolveMentions(mentions, r.WorkDir)

	// check images in mention parse results
	hasMentionImage := false
	for _, rm := range resolved {
		if rm.IsImage {
			hasMentionImage = true
			break
		}
	}

	// check clipboard images from extension
	hasBridgeImage := len(bridgeImages) > 0

	if hasMentionImage || hasBridgeImage {
		textPart := message
		// remove original reference text of mention images
		for _, rm := range resolved {
			if rm.IsImage {
				textPart = strings.Replace(textPart, rm.Raw, "", 1)
			}
		}
		textPart = strings.TrimSpace(textPart)

		var images []session.ImageBlock
		// add images parsed from mention
		for _, rm := range resolved {
			if rm.IsImage {
				images = append(images, session.ImageBlock{
					MediaType:  rm.MediaType,
					Base64Data: rm.Base64,
				})
			}
		}
		// add clipboard images from extension
		images = append(images, bridgeImages...)

		r.Session.SetMessages(append(r.Session.GetMessages(), session.UserWithImages(textPart, images)))
	} else {
		expandedInput := mention.ExpandMentions(message, r.WorkDir)
		r.Session.SetMessages(append(r.Session.GetMessages(), session.UserText(expandedInput)))
	}
	// 双写期：设置当前消息 ID，供 FileHistory.Track/Snapshot 关联使用
	if r.Executor != nil {
		msgs := r.Session.GetMessages()
		if len(msgs) > 0 {
			r.Executor.SetCurrentMessageId(msgs[len(msgs)-1].UUID)
		}
	}

	// 取反侧同走唯一谓词（与上方判定侧同源）。
	if r.FirstUserInput == "" && !command.IsCommandInput(message) {
		r.FirstUserInput = message
	}

	if r.SessionName == "" {
		r.autoNameSession()
	}

	r.guardBeginRequest() // P0 防护：新用户请求归零
	r.setState(stateStreaming)
	r.streamAIRequest(id)
}

// handleBridgeCommand 在 Bridge 模式下调度斜杠命令
// 解析命令名与参数，通过 Registry 查找并执行 Handler，结果以 JSON-RPC stream 通知发送至 IDE
// N10 P1-5：命令执行前经 CommandGuard 校验（委托 H2 security.CheckCommand）
func (r *BridgeRunner) handleBridgeCommand(message string, id *int) {
	name, args := command.Parse(message)

	// P1-5：IDE 命令校验（委托 H2 security.CheckCommand）
	// commandGuard.enabled=false 时直接放行（向后兼容）
	if r.commandGuard != nil {
		if err := r.commandGuard.CheckCommand(message); err != nil {
			r.sendStream("text", fmt.Sprintf("Command blocked: %v", err), "", nil, "", 0, 0, 0)
			r.sendStream("done", "", "", nil, "", 0, 0, 0)
			r.sendResult(id, "ok")
			return
		}
	}

	cmd, ok := r.CmdRegistry.Get(name)
	if !ok {
		// 命令未注册时返回拼写建议
		suggestions := command.Suggest(name, r.CmdRegistry)
		var output string
		if len(suggestions) > 0 {
			output = fmt.Sprintf("Unknown: /%s\nDid you mean: /%s?", name, suggestions[0])
		} else {
			output = fmt.Sprintf("Unknown: /%s\nType /help for all commands", name)
		}
		r.sendStream("text", output, "", nil, "", 0, 0, 0)
		r.sendStream("done", "", "", nil, "", 0, 0, 0)
		r.sendResult(id, "ok")
		return
	}

	output, err := cmd.Handler(args)
	if err != nil {
		r.sendStream("text", fmt.Sprintf("Command error: %v", err), "", nil, "", 0, 0, 0)
		r.sendStream("done", "", "", nil, "", 0, 0, 0)
		r.sendResult(id, "ok")
		return
	}

	// 处理特定命令的副作用
	output = r.dispatchBridgeCommand(name, args, output)

	// B7-P1-2: security-review 等命令在 dispatchBridgeCommand 内已触发 AI 流
	// （streamAIRequest 已发送 text + done 通知），此时 output 为哨兵值，
	// 仅需发送 JSON-RPC result，跳过重复的 done 通知。
	if output == bridgeStreamCompletedSentinel {
		r.sendResult(id, "ok")
		return
	}

	// 将命令输出发送至 IDE
	if output != "" {
		r.sendStream("text", output, "", nil, "", 0, 0, 0)
	}
	r.sendStream("done", "", "", nil, "", 0, 0, 0)
	r.sendResult(id, "ok")
}

// dispatchBridgeCommand 处理 Bridge 模式下特定命令的副作用
// 根据命令名执行会话清空、模型切换、进程退出、成本统计、会话/工作树管理等操作，
// 返回最终需发送给 IDE 的输出文本。该函数负责将命令 Handler 返回的魔法字符串
// （如 __COST__、__SESSION_LIST__）替换为真实可读的输出，避免魔法字符串泄露到 IDE。
func (r *BridgeRunner) dispatchBridgeCommand(name, args, output string) string {
	switch name {
	case "exit", "quit":
		// 退出命令：保存会话后终止进程，返回空字符串避免 __EXIT__ 魔法字符串泄露到 IDE
		_ = r.saveSession()
		go func() {
			time.Sleep(100 * time.Millisecond)
			os.Exit(0)
		}()
		return ""
	case "clear":
		// 清空会话上下文与 token 计数
		r.replaceSession(session.NewSession())
		return output
	case "model":
		// 模型切换：更新当前模型名
		if args != "" {
			r.ModelName = args
			r.requestSnapshot = contextRequestSnapshot{}
			if r.Caching != nil {
				r.Caching.UpdateModel(args)
			}
		}
		return output
	case "models":
		// B7-P1-4 修复：返回真实可用模型列表，替换 Registry 桩返回的 "Available models list"（假成功）。
		return ai.FormatModelList(r.ModelName, r.AvailableModels, r.ModelRegistry)
	case "compact":
		// B7-P1-3 修复：调用 runnerCore.handleCompact 执行真实上下文压缩，
		// 替换 Registry 桩返回的 "Compressing..."（假成功，不压缩）。
		return r.handleCompact(args)
	case "archive":
		// 对抗式审查 1.3 P1 修复：bridge 的 compact/checkAutoCompact 会产生归档，
		// /archive 必须同样可用（否则占位文案"仅在交互会话中可用"误导——bridge 即交互）。
		return r.handleArchive(args)
	case "security-review":
		// B7-P1-2 修复：构建 AI 审查提示词并触发 AI 流式响应，执行真实安全审查。
		// 原实现走 Registry 桩返回静态 "Security review started" 文本（假成功，不触发 AI）。
		return r.handleSecurityReview(args)
	case "session", "session list", "session save", "session switch", "session merge":
		// 会话管理：复用 r.Session 实现 list/save/switch 子命令，替换 __SESSION_LIST__ 等魔法字符串
		return r.handleBridgeSession(bridgeSubArgs(name, "session", args))
	case "sessions":
		// P0-5 修复：/sessions 等同于 /session list，路由到 handleBridgeSession 避免魔法字符串泄露
		return r.handleBridgeSession("list")
	case "resume":
		// P0-5 修复：/resume 在 Bridge 模式下降级提示（会话恢复需 IDE 侧配合），避免 __RESUME__ 魔法字符串泄露
		if strings.TrimSpace(args) != "" {
			return fmt.Sprintf("Session resume is not supported in bridge mode.\nTo resume session %q, use Whale Code TUI directly: whale-code", args)
		}
		return "Session resume is not supported in bridge mode.\nUse /sessions to list saved sessions, or use Whale Code TUI directly to resume."
	case "session-split":
		// P0-5 修复：/session-split 在 Bridge 模式下降级提示，避免 __SESSION_SPLIT__ 魔法字符串泄露
		return "Session split is not supported in bridge mode.\nUse Whale Code TUI directly: whale-code"
	case "worktree", "worktree list", "worktree add", "worktree remove", "worktree prune":
		// 工作树管理：调用 git worktree 命令实现 add/remove/list/prune 子命令
		return r.handleBridgeWorktree(bridgeSubArgs(name, "worktree", args))
	default:
		return output
	}
}

// handleSecurityReview 处理 /security-review 命令（B7-P1-2 修复）。
// 对齐 TUI Model.handleSecurityReview 与 PlainRunner.handleSecurityReview 的逻辑：
// 通过 runnerCore.buildSecurityReviewPrompt 构建 AI 审查提示词（优先加载 security-review
// skill 模板，回退内置模板），注入会话作为用户消息并触发 AI 流式响应，执行真实安全审查。
// 原实现走 Registry 桩返回静态 "Security review started" 文本（假成功，不触发 AI）。
// 返回 bridgeStreamCompletedSentinel 哨兵值通知 handleBridgeCommand 跳过重复 done 通知，
// 因为 streamAIRequest 已通过 bridgeStreamHandler 发送 text + done JSON-RPC 通知。
func (r *BridgeRunner) handleSecurityReview(args string) string {
	reviewPrompt := r.buildSecurityReviewPrompt(args)
	if reviewPrompt == "" {
		return "Security review prompt generation failed"
	}

	// 注入审查提示词作为用户消息（Bridge 模式不打印到 stdout，仅通过 JSON-RPC 通信）
	r.Session.SetMessages(append(r.Session.GetMessages(), session.UserText(reviewPrompt)))
	if r.FirstUserInput == "" {
		r.FirstUserInput = reviewPrompt
	}
	if r.SessionName == "" {
		r.autoNameSession()
	}

	// 触发 AI 流式响应：streamAIRequest 同步阻塞直至流结束，
	// 期间 bridgeStreamHandler 通过 sendStream 发送 text/thinking/tool_use/done 通知至 IDE。
	r.guardBeginRequest() // P0 防护：审查请求视为新用户请求，状态归零（与 plain.go:493 对齐）
	r.setState(stateStreaming)
	r.streamAIRequest(nil)
	return bridgeStreamCompletedSentinel
}

// bridgeSubArgs 将复合命令名（如 "session list"）归一化为子命令参数。
// 对于裸命令（name==base），直接返回原始 args；对于复合命令，提取子命令并与 args 拼接。
// 例如：bridgeSubArgs("session list", "session", "") 返回 "list"；
//
//	bridgeSubArgs("session", "session", "save") 返回 "save"。
func bridgeSubArgs(name, base, args string) string {
	if name == base {
		return args
	}
	sub := strings.TrimSpace(strings.TrimPrefix(name, base))
	if args == "" {
		return sub
	}
	return sub + " " + args
}

// formatBridgeCost 已移除：provider 不返回费用详情，本地费用估算不可靠。
// Bridge 模式的 token 用量统计走 /usage（UsageTracker.FormatUsageReport）。

// handleBridgeSession 处理 Bridge 模式下的 /session 子命令。
// 复用 r.Session 与 r.saveSession() 实现 list/save/switch 子命令，
// 替换 __SESSION_LIST__/__SESSIONS__ 等魔法字符串，向 IDE 返回真实会话信息。
func (r *BridgeRunner) handleBridgeSession(args string) string {
	sub := strings.TrimSpace(strings.ToLower(args))
	parts := strings.Fields(sub)
	subCmd := ""
	if len(parts) > 0 {
		subCmd = parts[0]
	}
	switch subCmd {
	case "", "list":
		// list：返回当前会话信息
		name := r.SessionName
		if name == "" {
			name = "(unnamed)"
		}
		return fmt.Sprintf("Current session:\n Name: %s\n Messages: %d\n Input tokens: %d\n Output tokens: %d",
			name, len(r.Session.GetMessages()), r.InputTokens, r.OutputTokens)
	case "save":
		// save：保存当前会话
		if err := r.saveSession(); err != nil {
			return fmt.Sprintf("Session save failed: %v", err)
		}
		return "Session saved"
	case "switch":
		// switch：切换会话（Bridge 模式下仅返回提示，实际切换由 IDE 侧驱动）
		target := strings.TrimSpace(strings.TrimPrefix(sub, "switch"))
		return "Session switched to: " + target
	case "merge":
		// merge：合并会话（Bridge 模式下降级提示）
		return "Session merge not supported in bridge mode"
	default:
		return "Usage: /session [list|save|switch <id>]"
	}
}

// handleBridgeWorktree 处理 Bridge 模式下的 /worktree 子命令。
// 通过 exec.Command 调用 git worktree 命令实现 add/remove/list/prune 子命令，
// 参考 runnerCore.handleWorktreeAdd 等已有实现，向 IDE 返回真实 git 输出。
func (r *BridgeRunner) handleBridgeWorktree(args string) string {
	sub := strings.TrimSpace(args)
	parts := strings.Fields(sub)
	subCmd := ""
	if len(parts) > 0 {
		subCmd = parts[0]
	}
	switch subCmd {
	case "", "list":
		// list：列出所有 worktree
		cmd := exec.Command("git", "worktree", "list")
		cmd.Dir = r.WorkDir
		out, err := cmd.Output()
		if err != nil {
			return fmt.Sprintf("Git error: %v", err)
		}
		return fmt.Sprintf("Worktrees:\n%s", string(out))
	case "add":
		// add：创建 worktree，用法 /worktree add <path> [branch]
		if len(parts) < 2 {
			return "Usage: /worktree add <path> [branch]"
		}
		path := parts[1]
		branch := ""
		if len(parts) > 2 {
			branch = parts[2]
		}
		cmdArgs := []string{"worktree", "add", path}
		if branch != "" {
			cmdArgs = append(cmdArgs, "-b", branch)
		}
		cmdArgs = append(cmdArgs, "HEAD")
		cmd := exec.Command("git", cmdArgs...)
		cmd.Dir = r.WorkDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Sprintf("create worktree failed: %v\n%s", err, string(out))
		}
		return fmt.Sprintf("Worktree created:\n%s", string(out))
	case "remove":
		// remove：移除 worktree
		if len(parts) < 2 {
			return "Usage: /worktree remove <path>"
		}
		cmd := exec.Command("git", "worktree", "remove", parts[1])
		cmd.Dir = r.WorkDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Sprintf("remove worktree failed: %v\n%s", err, string(out))
		}
		return fmt.Sprintf("Worktree removed: %s", parts[1])
	case "prune":
		// prune：清理失效的 worktree 引用
		cmd := exec.Command("git", "worktree", "prune")
		cmd.Dir = r.WorkDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Sprintf("prune worktrees failed: %v\n%s", err, string(out))
		}
		result := "stale worktree references pruned"
		if len(strings.TrimSpace(string(out))) > 0 {
			result += fmt.Sprintf("\n%s", string(out))
		}
		return result
	default:
		return "Usage: /worktree [list|add <path> [branch]|remove <path>|prune]"
	}
}

// streamAIRequest sends AI request and streams output
func (r *BridgeRunner) streamAIRequest(reqID *int) {
	if r.guardBeforeModelCall() { // P0 防护：轮次/审查到限则停机，不发起模型调用
		return
	}
	req := r.buildRequest()

	// record cache state before API call
	if r.Caching != nil {
		r.Caching.RecordStateBeforeRequest(strings.Join(layerBlocksToStrings(r.buildSystemPrompt()), "\n"), r.buildToolDefs(), r.ModelName)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	r.cancelMu.Lock()
	r.cancelFunc = cancel
	r.cancelMu.Unlock()
	defer cancel()

	events, err := ai.StreamWithRetry(ctx, r.AIClient, req, r.RetryConfig, r.Fallback, r.RecoveryCoordinator)
	if err != nil {
		r.handleStreamError(err)
		return
	}

	r.handleStreamEvents(ctx, events, reqID)
}

// handleStreamEvents processes AI streaming events using shared processStreamEvents,
// sends to IDE via JSON-RPC notifications
func (r *BridgeRunner) handleStreamEvents(ctx context.Context, events <-chan ai.Event, reqID *int) {
	handler := &bridgeStreamHandler{runner: r}
	responseText, _, pendingTools, streamErr := r.runnerCore.processStreamEvents(ctx, events, handler)

	// Handle stream error: notify IDE and set state to idle
	if streamErr != nil {
		r.setState(stateIdle)
		return
	}

	// save assistant message
	blocks := make([]session.ContentBlock, 0)
	if responseText != "" {
		blocks = append(blocks, session.ContentBlock{Type: "text", Text: responseText})
	}
	blocks = append(blocks, pendingTools...)

	usage := &session.TokenUsage{
		InputTokens:  r.LastInputTokens,
		OutputTokens: r.LastOutputTokens,
	}
	assistantMsg := session.AssistantWithTools(blocks, r.ModelName, usage)
	r.Session.SetMessages(append(r.Session.GetMessages(), assistantMsg))

	if len(pendingTools) > 0 {
		r.setState(stateToolExec)
		r.executeAndContinue(pendingTools, reqID)
	} else {
		r.setState(stateIdle)
		r.Session.IncrementTurn()
		_ = r.saveSession()
		// 修复：每轮对话结束自动创建 FileHistory 快照
		r.autoSnapshotOnTurnEnd()
		// N26：触发 turn 结束钩子（goal.Manager.OnTurnEnd，）
		r.runTurnEndHooks(context.Background())
		r.checkAutoCompact()

		// send completion notification
		r.sendStream("done", "", "", nil, "", 0, 0, 0)

		// V2 auto preference extraction
		if r.MemExtractor != nil && r.Config.AutoMemoryEnabled {
			go func() {
				_ = r.MemExtractor.ExtractAndSave(context.Background(), r.AIClient, r.Session.GetMessages())
			}()
		}
	}
}

// bridgeStreamHandler implements OutputHandler for BridgeRunner stream events
type bridgeStreamHandler struct {
	runner *BridgeRunner
}

// OnText sends text content to IDE via JSON-RPC
func (h *bridgeStreamHandler) OnText(text string) {
	h.runner.sendStream("text", text, "", nil, "", 0, 0, 0)
}

// OnThinking sends thinking content to IDE via JSON-RPC
func (h *bridgeStreamHandler) OnThinking(text string) {
	h.runner.sendStream("thinking", text, "", nil, "", 0, 0, 0)
}

// OnToolUse sends tool use notification to IDE via JSON-RPC
func (h *bridgeStreamHandler) OnToolUse(name string, input json.RawMessage) {
	h.runner.sendStream("tool_use", "", name, nil, "", 0, 0, 0)
}

// OnToolResult sends tool result notification to IDE via JSON-RPC
func (h *bridgeStreamHandler) OnToolResult(result string) {
	h.runner.sendStream("tool_result", "", "", nil, result, 0, 0, 0)
}

// OnError sends error notification to IDE via JSON-RPC
func (h *bridgeStreamHandler) OnError(err error) {
	h.runner.sendStream("error", fmt.Sprintf("%v", err), "", nil, "", 0, 0, 0)
}

// executeAndContinue executes tool and sends result back to AI
func (r *BridgeRunner) executeAndContinue(pendingTools []session.ContentBlock, reqID *int) {
	// resultCallback sends tool results via JSON-RPC to IDE；同时收集 guard 记账所需
	// 的 tool 结果（P0 防护：每轮工具落定后判定熔断/进度收敛）。
	guardResults := make([]tools.Result, 0, len(pendingTools))
	resultCallback := func(res tools.Result) {
		guardResults = append(guardResults, res)
		r.sendStream("tool_result", "", res.Name, nil, res.Output, 0, 0, 0)
	}

	r.runnerCore.executeToolsAndContinue(pendingTools, resultCallback, nil)
	if r.guardAfterToolRound(pendingTools, guardResults) { // P0 防护：到限则停机
		return
	}
	r.streamAIRequest(reqID)
}

// handleStreamError processes streaming errors.
// 2026-07-31：context.Canceled 在 streaming/toolExec 状态下为用户主动取消（stop 键/Ctrl+C），
// 静默处理（仅发送 done 通知，不发送 error），与 app.go/plain.go 行为一致。
// 其他 State 下的 context.Canceled 仍按真实错误处理（可能是网络超时等）。
func (r *BridgeRunner) handleStreamError(err error) {
	// 保存原 State 用于判断是否为用户主动取消（2026-07-31 修正与 app.go/plain.go 一致）。
	prevState := r.getState()
	r.setState(stateIdle)

	// 用户主动取消（Ctrl+C / stop），静默处理：仅发送 done 通知，不发送 error。
	if stderrors.Is(err, context.Canceled) && (prevState == stateStreaming || prevState == stateToolExec) {
		r.sendStream("done", "", "", nil, "", 0, 0, 0)
		return
	}

	errMsg := err.Error()

	if ai.IsContextOverflowError(errMsg) {
		// first try collapse recovery
		if r.CollapseMgr != nil {
			if recovered, ok := r.CollapseMgr.RecoverFromOverflow(r.Session.GetMessages()); ok {
				r.Session.SetMessages(recovered)
				r.setState(stateStreaming)
				r.streamAIRequest(nil)
				return
			}
		}
		// collapse recovery failed, execute compaction
		r.sendStream("error", "Context overflow, auto-compacting...", "", nil, "", 0, 0, 0)
		beforeRevision := sessionRevision(r.Session)
		compactResult := r.handleCompactWithTrigger("", ctxpkg.CompactionTriggerReactive, ctxpkg.CompactionPolicyForce)
		if compactCommittedSince(r.Session, beforeRevision) {
			r.setState(stateStreaming)
			r.streamAIRequest(nil)
		} else {
			r.sendStream("error", "Context overflow recovery stopped: "+compactResult+". Use /rewind or /clear to free space.", "", nil, "", 0, 0, 0)
		}
		return
	}

	if ai.IsOverloadedError(errMsg) {
		if r.Fallback.Record529() {
			r.ModelName = r.Fallback.ActiveModel()
			r.requestSnapshot = contextRequestSnapshot{}
			r.AIClient.SetModel(r.ModelName)
			if r.Caching != nil {
				r.Caching.UpdateModel(r.ModelName)
			}
		}
		// 切换模型后重试当前请求
		r.setState(stateStreaming)
		r.streamAIRequest(nil)
		return
	}

	r.sendStream("error", r.friendlyError(err), "", nil, "", 0, 0, 0)
	r.sendStream("done", "", "", nil, "", 0, 0, 0)
}

// handleNewSession starts a new session
func (r *BridgeRunner) handleNewSession(id *int) {
	_ = r.saveSession()
	r.replaceSession(session.NewSession())
	r.SessionName = ""
	r.FirstUserInput = ""
	r.SavedMsgCount = 0
	r.sendResult(id, "ok")
}

// handleAbort aborts current generation
func (r *BridgeRunner) handleAbort() {
	r.cancelMu.Lock()
	if r.cancelFunc != nil {
		r.cancelFunc()
		r.cancelFunc = nil
	}
	r.cancelMu.Unlock()
	r.setState(stateIdle)
	r.sendStream("done", "", "", nil, "", 0, 0, 0)
}

// cleanupOnExit 退出时清理资源（修复点6）。
//
// 实现内容（参考 PlainRunner.cleanupOnExit）：
// 1. 中断当前 AI 流：调 cancelFunc（若有），避免 Close 时流仍在写
// 2. 保存会话：空会话（无 FirstUserInput）直接 Delete，非空会话 saveSession
// 3. 关闭 AI 客户端：释放底层 HTTP 连接（与 runnerCore.Close 中的 Close 幂等）
// 4. 持久化文件缓存：Persist FileCache 元数据
//
// 调用时机：GracefulShutdown.RegisterCleanup 注册，由 Shutdown(false) 触发；
// 也由 main.go 的 defer runner.Close() 间接触发 runnerCore.Close（不调本方法）。
// 因此本方法必须自包含完整清理，不依赖 Close。
func (r *BridgeRunner) cleanupOnExit() {
	// 1. 中断当前 AI 流
	r.cancelMu.Lock()
	cf := r.cancelFunc
	r.cancelFunc = nil
	r.cancelMu.Unlock()
	if cf != nil {
		cf()
	}

	// 2. 保存或删除会话
	if r.SessionName != "" && r.FirstUserInput == "" {
		if r.Store != nil {
			r.Store.Delete(r.SessionName)
		}
		r.SessionName = ""
	} else if r.SessionName != "" {
		_ = r.saveSession()
	}

	// 3. 关闭 AI 客户端（与 runnerCore.Close 幂等，重复调用安全）
	if r.AIClient != nil {
		_ = r.AIClient.Close()
	}

	// 4. 持久化文件缓存
	if r.Executor != nil && r.Executor.FileCache != nil {
		_ = r.Executor.FileCache.Persist()
	}
}

// handleSetToken processes token setting request
// T1-P1-A1：SetToken 异步触发重连时返回 ErrBridgeReconnecting(-32006)，IDE 可重试
func (r *BridgeRunner) handleSetToken(token string, source string, id *int) {
	status, err := r.authProvider.SetToken(token, source)
	if err != nil {
		// T1-P1-A1：识别 ErrBridgeReconnecting 错误码，原样返回给 IDE（不改为 ErrInvalidToken）
		code := bridge.ErrInvalidToken
		var bridgeErr *bridge.BridgeError
		if stderrors.As(err, &bridgeErr) {
			code = bridgeErr.Code
		}
		r.sendError(id, code, err.Error())
		return
	}
	// send tokenStatus notification
	r.sendNotification("tokenStatus", map[string]interface{}{
		"isValid":      status.IsValid,
		"expiresIn":    status.ExpiresIn,
		"state":        status.State,
		"refreshState": status.RefreshState,
		"source":       status.Source,
	})
	r.sendResult(id, map[string]interface{}{
		"isValid": status.IsValid,
		"source":  status.Source,
	})
}

// handleTokenStatus handles token status query
func (r *BridgeRunner) handleTokenStatus(id *int) {
	status := r.authProvider.GetStatus()
	r.sendResult(id, map[string]interface{}{
		"isValid":      status.IsValid,
		"expiresIn":    status.ExpiresIn,
		"state":        status.State,
		"refreshState": status.RefreshState,
		"source":       status.Source,
	})
}

// handleRefreshToken handles manual token refresh request
func (r *BridgeRunner) handleRefreshToken(id *int) {
	status, err := r.authProvider.RefreshToken()
	if err != nil {
		r.sendError(id, bridge.ErrRefreshFailed, err.Error())
		return
	}
	r.sendResult(id, map[string]interface{}{
		"isValid": status.IsValid,
		"source":  status.Source,
	})
}

// resumeLastSession resumes the previous session
func (r *BridgeRunner) resumeLastSession() {
	if r.runnerCore.Store == nil {
		return
	}
	summaries, err := r.Store.List()
	if err != nil || len(summaries) == 0 {
		return
	}
	// Sort by UpdatedAt descending to ensure [0] is the latest session
	sort.Slice(summaries, func(i, j int) bool {
		return time.UnixMilli(summaries[i].UpdatedAt).After(time.UnixMilli(summaries[j].UpdatedAt))
	})
	latest := summaries[0]
	loaded, err := r.Store.Load(latest.ID)
	if err != nil {
		return
	}
	r.replaceSession(loaded)
	r.SessionName = latest.ID
	r.SavedMsgCount = len(loaded.GetMessages())
	r.sendNotification("sessionInfo", map[string]interface{}{
		"sessionId": latest.ID,
		"modelName": r.ModelName,
		"workDir":   r.WorkDir,
		"turnCount": loaded.Metadata.TurnCount,
	})
}

// ---- JSON-RPC Communication Methods ----

// sendResult sends JSON-RPC success response
func (r *BridgeRunner) sendResult(id *int, result interface{}) {
	if id == nil {
		return
	}
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		Result:  result,
		ID:      *id,
	}
	r.writeJSON(resp)
}

// sendError sends JSON-RPC error response
func (r *BridgeRunner) sendError(id *int, code int, message string) {
	if id == nil {
		return
	}
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		Error:   &jsonRPCError{Code: code, Message: message},
		ID:      *id,
	}
	r.writeJSON(resp)
}

// sendNotification sends JSON-RPC notification
func (r *BridgeRunner) sendNotification(method string, params interface{}) {
	notif := jsonRPCNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	r.writeJSON(notif)
}

// sendStream sends streaming notification
func (r *BridgeRunner) sendStream(streamType, content, name string, input map[string]interface{}, output string, exitCode, inTokens, outTokens int) {
	params := streamParams{
		Type:         streamType,
		Content:      content,
		Name:         name,
		Input:        input,
		Output:       output,
		ExitCode:     exitCode,
		InputTokens:  inTokens,
		OutputTokens: outTokens,
	}
	r.sendNotification("stream", params)
}

// writeJSON writes JSON to stdout in a thread-safe manner
func (r *BridgeRunner) writeJSON(v interface{}) {
	r.encoderMu.Lock()
	defer r.encoderMu.Unlock()
	r.encoder.Encode(v)
}

// ---- Reuse PlainRunner Helper Methods ----

// the following methods share the exact same logic as PlainRunner, directly reused

// buildRequest builds AI request

// buildSystemPrompt builds system prompt

// buildMessages builds message list

// buildToolDefs builds tool definitions

// handleCompact handles compaction command

// checkAutoCompact checks if auto-compaction is needed
func (r *BridgeRunner) checkAutoCompact() {
	estimated := r.estimateCurrentInputContextTokens()
	// H13-G1: BodyAfterPrefix scope — 获取 cachedInputTokens
	cached := ctxpkg.GetCachedInputTokens(r.TokenSource, r.ModelName, estimated, func() {
		if r.Caching != nil {
			r.Caching.RecordCacheAnomaly()
		}
	})
	state := r.Budget.CheckState(estimated, cached)
	// 97% 兜底：只做硬停 + 最小交接模板，不再尝试压缩（窗口已满时压缩请求本身可能失败）
	if state == ctxpkg.StateBlocked {
		fmt.Fprintf(os.Stderr, "%s\n", blockedHandoffMessage(r.Budget.UsagePercent(estimated), r.FirstUserInput, r.Session.GetMessages()))
		return
	}
	if !autoCompactDue(r.Config, r.Budget, estimated, cached) {
		return
	}

	// if collapse manager suppresses auto-compaction, skip
	if r.CollapseMgr != nil && !r.CollapseMgr.ShouldAutoCompact() {
		return
	}

	// 对抗式审查 3.11 P1 修复：auto-compact 结果不得丢弃——归档失败会中止
	// Rewrite 并经此返回错误文案，静默丢弃 = 压缩未发生零感知。
	if out := r.handleCompactWithTrigger("--ai", ctxpkg.CompactionTriggerAuto, ctxpkg.CompactionPolicyBudgeted); out != "" {
		fmt.Printf("[bridge] auto-compact: %s\n", out)
	}
}

// autoNameSession auto-names the session

// saveSession incrementally saves session

// estimateCurrentTokens estimates current token count

// friendlyError converts errors to user-friendly messages

// handleUndo delegates to shared runnerCore implementation
func (r *BridgeRunner) handleUndo() string {
	return r.runnerCore.handleUndo()
}

// handleRedo delegates to shared runnerCore implementation
func (r *BridgeRunner) handleRedo() string {
	return r.runnerCore.handleRedo()
}

// handleRewind delegates to shared runnerCore implementation（阶段 4：补接 bridge 路径）
func (r *BridgeRunner) handleRewind(args string) string {
	return r.runnerCore.handleRewind(args)
}

// handleCheckpoint delegates to shared runnerCore implementation（阶段 4：补接 bridge 路径）
func (r *BridgeRunner) handleCheckpoint(args string) string {
	return r.runnerCore.handleCheckpoint(args)
}

// bridgeOutputStyleSetter implements command.OutputStyleSetter interface
type bridgeOutputStyleSetter struct {
	config *config.Config
	saver  func() error
}

func (s *bridgeOutputStyleSetter) SetOutputStyle(style string) error {
	s.config.OutputStyle = config.OutputStyle(style)
	if s.saver != nil {
		return s.saver()
	}
	return nil
}

func (s *bridgeOutputStyleSetter) GetOutputStyle() string {
	if s.config.OutputStyle == "" {
		return string(config.OutputDefault)
	}
	return string(s.config.OutputStyle)
}
