package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/anchorandfish/whale-code/internal/safebuf"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/security"
)

// applySandbox 为 HookCommand 应用沙箱隔离（SNAPSHOT）。
//
// 子审查优化 1 吸纳：从 executeCommandHook 抽取沙箱逻辑，避免函数臃肿。
// O-2 修复：返回 SandboxContext，供调用方在 cmd.Start() 后调用
// PostStartHook（Windows Job Object AssignProcess）和 ApplyResourceLimits，
// 避免 hook 子进程绕过资源限制。
//
// 级别优先级：hook.SandboxLevel > m.defaultSandboxLevel > disabled
// - hook.SandboxLevel != disabled：用 hook 级别
// - hook.SandboxLevel == disabled && m.defaultSandboxLevel != disabled：用默认级别
// - 两者均 disabled：跳过沙箱（返回 nil cleanup）
//
// 降级策略（S-4 修复， 全模块审查）：
// - sandboxMgr=nil：跳过沙箱
// - Prepare 失败：
// - SandboxLevelStrict/SandboxLevelIsolated：fail-closed 返回错误（拒绝执行）
// - SandboxLevelRelaxed：fail-open 降级执行（不修改 cmd）+ 记录日志
// - Restrict 失败：同上分级策略
// - Env 覆盖：沙箱环境变量覆盖 buildHookEnv（C15 隔离）
//
// 返回：
// - cleanup: 沙箱清理函数（defer 调用，nil 表示未启用沙箱）
// - sctx: 沙箱上下文（含 PostStartHook 和 ResourceLimits，零值表示未启用沙箱）
// - err: 沙箱应用错误（fail-closed 路径返回错误供调用方拒绝执行；fail-open 路径返回 nil）
//
// 官方源： @2026-07-20（Manager.Prepare + OSSandbox.Restrict）
func (m *Manager) applySandbox(ctx context.Context, cmd *exec.Cmd, h *HookCommand) (cleanup func(), sctx sandbox.SandboxContext, err error) {
	// 级别解析：hook.SandboxLevel > defaultSandboxLevel > disabled
	level := h.SandboxLevel
	if level == sandbox.SandboxLevelDisabled {
		level = m.defaultSandboxLevel
	}
	// 两者均 disabled 或 sandboxMgr=nil：跳过沙箱
	if level == sandbox.SandboxLevelDisabled || m.sandboxMgr == nil {
		return nil, sandbox.SandboxContext{}, nil
	}

	// S-4 修复：高风险级别（Strict/Isolated）采用 fail-closed 策略，
	// Prepare/Restrict 失败时拒绝执行；低风险级别（Relaxed）保留 fail-open 降级执行。
	failClosed := level >= sandbox.SandboxLevelStrict

	// Prepare 沙箱上下文
	sctx, prepCleanup, prepareErr := m.sandboxMgr.Prepare(ctx, level, m.workDir)
	if prepareErr != nil {
		if failClosed {
			// 高风险级别 fail-closed：返回错误，调用方应拒绝执行
			log.Printf("[hook sandbox] Prepare failed (fail-closed, level=%s): %v", level, prepareErr)
			return nil, sandbox.SandboxContext{}, prepareErr
		}
		// 低风险级别 fail-open：降级执行（不修改 cmd）+ 记录日志
		log.Printf("[hook sandbox] Prepare failed (degraded, fail-open, level=%s): %v", level, prepareErr)
		return nil, sandbox.SandboxContext{}, nil
	}

	// Env 覆盖：沙箱环境变量覆盖 buildHookEnv（C15 隔离）。
	// 顺序修复（对抗式审查阻断项）：必须在 Restrict 之前覆盖。
	// darwin/linux 的 re-exec helper（__whale-rlimit / __whale-landlock）在
	// Restrict 时向 cmd.Env 追加 spec env；若 Restrict 后再整体覆盖 Env，
	// spec 会被剥离 → helper exit 126，所有 hook 命令失败（已实证复现）。
	// 对齐 bash.go 的顺序（Env/Dir 先设，Restrict 后调）。
	if len(sctx.Env) > 0 {
		cmd.Env = sctx.Env
	}

	// WorkDir 覆盖：沙箱工作目录（可能与 m.workDir 不同）
	if sctx.WorkDir != "" {
		cmd.Dir = sctx.WorkDir
	}

	// Restrict 应用到 cmd（设置 SysProcAttr / re-exec helper 包装）
	if osSandbox := sctx.OSSandbox(); osSandbox != nil {
		if restrictErr := osSandbox.Restrict(cmd); restrictErr != nil {
			prepCleanup()
			if failClosed {
				// 高风险级别 fail-closed：Restrict 失败返回错误
				log.Printf("[hook sandbox] Restrict failed (fail-closed, level=%s): %v", level, restrictErr)
				return nil, sandbox.SandboxContext{}, restrictErr
			}
			// 低风险级别 fail-open：降级执行（不修改 cmd）
			log.Printf("[hook sandbox] Restrict failed (degraded, fail-open, level=%s): %v", level, restrictErr)
			return nil, sandbox.SandboxContext{}, nil
		}
	}

	return prepCleanup, sctx, nil
}

// executeCommandHook 执行命令类型钩子
// 通过子进程执行命令，支持 stdin payload、环境变量、JSON 控制输出解析
func (m *Manager) executeCommandHook(h *HookCommand, payload HookPayload, eventType HookEventType) HookResult {
	if h == nil {
		return HookResult{Action: ActionWarn, Messages: []string{"[hook warn] command hook is nil"}}
	}
	if h.Command == "" {
		return HookResult{Action: ActionAllow}
	}

	command := h.Command

	// H2：委托 security.CheckCommand 进行命令安全校验（02 §2.3.1 契约）。
	// hook 为后台执行场景，Ask（需确认）时 fail-closed 拒绝（无交互能力）。
	decision := security.CheckCommand(command)
	if decision.Action == security.Deny {
		return HookResult{
			Action:   ActionBlock,
			Reason:   decision.Reason,
			Messages: []string{fmt.Sprintf("[hook block] %v", decision.Err)},
		}
	}
	if decision.Action == security.Ask {
		log.Printf("[hook] 命令需确认但无交互能力，拒绝: %s", decision.Reason)
		return HookResult{
			Action:   ActionBlock,
			Reason:   fmt.Sprintf("hook 命令需用户确认: %s", decision.Reason),
			Messages: []string{fmt.Sprintf("[hook block] 命令需用户确认: %s", decision.Reason)},
		}
	}

	timeout := time.Duration(h.Timeout) * time.Millisecond
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = m.workDir
	cmd.Env = buildHookEnv(payload, eventType)

	payloadJSON, _ := json.Marshal(payload)
	cmd.Stdin = bytes.NewReader(payloadJSON)

	var stdout, stderr safebuf.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// SNAPSHOT：应用沙箱隔离（可选，nil=禁用，向后兼容）
	// S-4+H-1 修复（全模块审查）：
	// - applySandbox 对 Strict/Isolated 级别 fail-closed 返回错误
	// - 此处捕获错误并拒绝执行（Block），避免高风险命令在沙箱降级时绕过隔离
	// - 对 Relaxed 级别 fail-open 路径返回 nil cleanup + nil error，继续执行
	// O-2 修复：同时获取 SandboxContext，用于 Start 后调用 PostStartHook/ApplyResourceLimits
	sandboxCleanup, sbCtx, sbErr := m.applySandbox(ctx, cmd, h)
	if sbErr != nil {
		// fail-closed：高风险级别沙箱应用失败，拒绝执行 hook 命令
		return HookResult{
			Action:   ActionBlock,
			Reason:   fmt.Sprintf("sandbox apply failed (fail-closed): %v", sbErr),
			Messages: []string{fmt.Sprintf("[hook block] sandbox failure, refusing execution: %v", sbErr)},
		}
	}
	sandboxed := sandboxCleanup != nil
	if sandboxCleanup != nil {
		defer sandboxCleanup()
	}

	// O-2 修复：sandboxed 路径用 Start + PostStartHook + ApplyResourceLimits + Wait，
	// 确保 hook 子进程被加入 Job Object（Windows）并应用资源限制，避免绕过沙箱。
	// 非 sandboxed 路径保持 cmd.Run() 行为（向后兼容）。
	var err error
	if sandboxed {
		// sandboxed：用进程组启动，确保可终止整个进程树（同 bash.go/executor.go 模式）
		if startErr := sandbox.StartCommandWithProcessGroupContext(ctx, cmd); startErr != nil {
			err = fmt.Errorf("hook command start failed: %w", startErr)
		} else {
			// PostStartHook（Windows Job Object AssignProcess），fail-closed：
			// 失败时 kill 进程组并返回错误，避免进程在 Job 外运行无资源限制
			if sbCtx.PostStartHook != nil {
				if hookErr := sbCtx.PostStartHook(cmd); hookErr != nil {
					_ = sandbox.KillProcessGroup(cmd)
					_ = cmd.Wait() // 释放僵尸进程
					err = fmt.Errorf("sandbox: post-start hook failed: %w", hookErr)
				}
			}
			// ApplyResourceLimits（best-effort，不阻断执行）
			// Windows 资源限制通过 Job Object 在 Restrict 时设置，无需此处调用
			if err == nil && sbCtx.ResourceLimits != (sandbox.ResourceLimits{}) {
				_ = sandbox.ApplyResourceLimits(cmd, sbCtx.ResourceLimits, nil)
			}
			// 等待命令完成；ctx 到期/取消时**先杀整个进程组**再收尾。
			// 修复前是「先 cmd.Wait()、后 if ctx.Err()!=nil { KillProcessGroup }」：
			// 管道孙进程持有写端会拖住 Wait ⇒ 其后的杀组永不可达。
			// 现与 tools/bash.go、tools/executor.go 共用 sandbox 的单一实现。
			// （cmd 由上面的 StartCommandWithProcessGroup 启动 ⇒ 独立进程组已武装。）
			if err == nil {
				_, err = sandbox.WaitProcessGroupWithContext(ctx, cmd)
			}
		}
	} else {
		// 非 sandboxed：保持 cmd.Run() 行为（向后兼容）
		err = cmd.Run()
	}
	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += "[stderr]\n" + stderr.String()
	}

	result := HookResult{
		Action: h.Action,
		Reason: h.Reason,
		Output: output,
		Error:  err,
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result.Action = ActionWarn
			result.Messages = append(result.Messages, fmt.Sprintf("[hook warn] command timed out after %s", timeout))
			return result
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode := exitErr.ExitCode()
			switch {
			case exitCode == 2:
				result.Action = ActionBlock
				if output != "" {
					result.Reason = output
				}
				if eventType.IsLifecycleEvent() {
					result.Action = ActionAllow
					result.Messages = append(result.Messages, fmt.Sprintf("[hook warn] deny is ignored for lifecycle event %s", eventType))
				}
				return result
			default:
				result.Action = ActionWarn
				result.Messages = append(result.Messages, fmt.Sprintf("[hook warn] command exited with code %d: %s", exitCode, output))
				return result
			}
		}
	}

	if eventType == EventPreToolUse && stdout.Len() > 0 {
		control, parsed := parseHookJSONControl(stdout.Bytes())
		if parsed {
			if control.Decision == "deny" {
				result.Action = ActionBlock
				if control.Message != "" {
					result.Reason = control.Message
				}
				return result
			}
			if len(control.UpdatedInput) > 0 {
				updatedStr := string(control.UpdatedInput)
				result.UpdatedInput = &updatedStr
			}
			if control.Message != "" {
				result.Messages = append(result.Messages, control.Message)
			}
			return result
		}
	}

	if (eventType == EventPostToolUse || eventType == EventPostBash) && result.Error == nil && stdout.Len() > 0 {
		modified := stdout.String()
		result.Modified = &modified
	}

	return result
}

// executePromptHook 执行 prompt 类型钩子
// 调用 AI 客户端评估 prompt，解析 JSON 响应并返回决策
func (m *Manager) executePromptHook(h *HookPrompt, payload HookPayload, eventType HookEventType) HookResult {
	if h == nil {
		return HookResult{Action: ActionWarn, Messages: []string{"[hook warn] prompt hook is nil"}}
	}
	if m.aiClient == nil {
		return HookResult{Action: ActionWarn, Messages: []string{"[hook warn] AI client not configured for prompt hook"}}
	}
	if h.Prompt == "" {
		return HookResult{Action: ActionWarn, Messages: []string{"[hook warn] prompt hook missing 'prompt' field"}}
	}

	prompt := m.renderTemplate(h.Prompt, payload)

	timeout := time.Duration(h.Timeout) * time.Millisecond
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	model := h.Model
	if model == "" {
		model = "qwen3.6-27b"
	}

	response, err := m.aiClient.EvaluatePrompt(ctx, prompt, model)
	if err != nil {
		return HookResult{Action: ActionWarn, Error: err, Messages: []string{fmt.Sprintf("[hook warn] prompt hook AI error: %v", err)}}
	}

	return m.parseHookResponse(response, eventType)
}

// executeAgentHook 执行 agent 类型钩子
// 启动子代理评估，使用 AI 客户端（只读模式）
func (m *Manager) executeAgentHook(h *HookAgent, payload HookPayload, eventType HookEventType) HookResult {
	if h == nil {
		return HookResult{Action: ActionWarn, Messages: []string{"[hook warn] agent hook is nil"}}
	}
	if m.aiClient == nil {
		return HookResult{Action: ActionWarn, Messages: []string{"[hook warn] AI client not configured for agent hook"}}
	}
	if h.Prompt == "" {
		return HookResult{Action: ActionWarn, Messages: []string{"[hook warn] agent hook missing 'prompt' field"}}
	}

	prompt := m.renderTemplate(h.Prompt, payload)
	timeout := time.Duration(h.Timeout) * time.Millisecond
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	model := h.Model
	if model == "" {
		model = "qwen3.6-35b-a3b"
	}

	response, err := m.aiClient.EvaluatePrompt(ctx, prompt, model)
	if err != nil {
		return HookResult{Action: ActionWarn, Error: err, Messages: []string{fmt.Sprintf("[hook warn] agent hook AI error: %v", err)}}
	}

	return m.parseHookResponse(response, eventType)
}

// executeHTTPHook 执行 http 类型钩子
// 发送 HTTP POST 请求，解析 JSON 响应并返回决策
func (m *Manager) executeHTTPHook(h *HookHTTP, payload HookPayload, eventType HookEventType) HookResult {
	if h == nil {
		return HookResult{Action: ActionWarn, Messages: []string{"[hook warn] http hook is nil"}}
	}
	if h.URL == "" {
		return HookResult{Action: ActionWarn, Messages: []string{"[hook warn] http hook missing 'url' field"}}
	}

	timeout := time.Duration(h.Timeout) * time.Millisecond
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	client := m.httpClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(context.Background(), "POST", h.URL, bytes.NewReader(body))
	if err != nil {
		return HookResult{Action: ActionWarn, Error: err, Messages: []string{fmt.Sprintf("[hook warn] http hook request error: %v", err)}}
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range h.Headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return HookResult{Action: ActionWarn, Error: err, Messages: []string{fmt.Sprintf("[hook warn] http hook connection error: %v", err)}}
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	output := string(respBody)

	if resp.StatusCode != 200 {
		return HookResult{
			Action:   ActionWarn,
			Output:   output,
			Messages: []string{fmt.Sprintf("[hook warn] http hook returned status %d", resp.StatusCode)},
		}
	}

	return m.parseHookResponse(output, eventType)
}

// executeCallbackHook 执行 callback 类型钩子
// 直接调用 Go 函数，带 panic 恢复
func (m *Manager) executeCallbackHook(h *HookCallback, payload HookPayload, eventType HookEventType) HookResult {
	if h == nil || h.Callback == nil {
		return HookResult{Action: ActionWarn, Messages: []string{"[hook warn] callback hook missing callback function"}}
	}

	var result HookResult
	func() {
		defer func() {
			if r := recover(); r != nil {
				result = HookResult{
					Action:   ActionWarn,
					Messages: []string{fmt.Sprintf("[hook warn] callback hook panicked: %v", r)},
				}
			}
		}()
		result = h.Callback(payload)
	}()

	if result.Action == ActionBlock && eventType.IsLifecycleEvent() {
		result.Action = ActionAllow
		result.Messages = append(result.Messages, fmt.Sprintf("[hook warn] deny is ignored for lifecycle event %s", eventType))
	}

	return result
}
