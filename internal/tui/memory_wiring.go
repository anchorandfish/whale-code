// memory_wiring.go — H11N27 6 接口 tui 层接线。
// 363 SetGlobalMergeLayer；落地：tui.NewModel 内注入全局 MergeLayer
// 39 SetAICaller；落地：aiCallerAdapter 包装 SimpleClient.Stream 为 func(ctx, model, prompt) (string, error)
// 71 SetH2Checker；落地：h2CheckerAdapter 包装 security.CheckCommand 为 func(cmd) (bool, string)
// 84 SetN17ProbeCapability；落地：跳过注入（nil），走 builtin_probes 自实现降级
// 34 RegisterCommands；落地：包装 r.OnMemory 追加 H11N27 5 子命令分发
// REF-internal-forgetting.go:122 StartForgettingScanner；落地：启动 2 个 goroutine（user + project），ctx 联动取消
// 23 evalAgentAdapter 模式；落地：aiCallerAdapter 复用此模式包装 SimpleClient
// REF-外部-GenerativeAgents@https://arxiv.org/abs/2304.03442+2026-07-16访问-reflection；借鉴逻辑：episodic→procedural 合成（aiCaller 注入支持）
// SNAPSHOT: wireH11N27Memory={6 接口注入 + 2 wrapper}; aiCallerAdapter={MaxTokens=2048, 包装 Stream 收集 TextDeltaEvent}; h2CheckerAdapter={包装 Decision.Action==Allow → bool, Decision.Reason → string}; ForgettingScanner={2 goroutine user+project, ctx 联动取消}

package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/memory"
	"github.com/anchorandfish/whale-code/internal/memorybridge"
	"github.com/anchorandfish/whale-code/internal/security"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/anchorandfish/whale-code/internal/whalehome"
)

// aiCallerMaxTokens 反思 AI 单次调用最大输出 token 数。
// 反思 prompt 期望生成 1-3 条 procedural 记忆，每条 200-500 token，留余量取 2048。
// 参考 goal_wiring.go evalMaxOutputTokens=1280，反思场景略高。
const aiCallerMaxTokens = 2048

// aiCallerAdapter 包装 ai.SimpleClient.Stream 为 memory.SetAICaller 所需签名。
// 签名：func(ctx, model, prompt) (string, error)
// 参考 goal_wiring.go:23 evalAgentAdapter 模式（包装 SimpleClient.Stream 收集 TextDeltaEvent）。
type aiCallerAdapter struct {
	client *ai.SimpleClient
}

// Call 实现 SetAICaller 所需签名，调用 SimpleClient.Stream 收集完整响应后返回。
// model 为空时 fallback 到 client.Model()；prompt 作为 user message 发送。
func (a *aiCallerAdapter) Call(ctx context.Context, model, prompt string) (string, error) {
	if a.client == nil {
		return "", fmt.Errorf("aiCallerAdapter: client not configured")
	}
	if model == "" {
		model = a.client.Model()
	}
	req := ai.Request{
		Model:     model,
		MaxTokens: aiCallerMaxTokens,
		Messages: []ai.MessageEntry{
			{Role: "user", Content: prompt},
		},
	}
	stream, err := a.client.Stream(ctx, req)
	if err != nil {
		return "", fmt.Errorf("aiCallerAdapter stream: %w", err)
	}
	var sb strings.Builder
	for event := range stream {
		switch e := event.(type) {
		case ai.TextDeltaEvent:
			sb.WriteString(e.Content)
		case ai.ErrorEvent:
			return sb.String(), fmt.Errorf("aiCallerAdapter stream error: %w", e.Err)
		}
	}
	return sb.String(), nil
}

// h2CheckerAdapter 包装 security.CheckCommand 为 memory.SetH2Checker 所需签名。
// security.CheckCommand 返回 Decision{Action, Reason, Err, ...}；
// SetH2Checker 期望 func(cmd string) (allowed bool, reason string)。
// allowed = (Action == security.Allow)；reason = Decision.Reason。
// 注：security 未初始化时 CheckCommand fail-closed 返回 Deny，本适配器返回 (false, "Checker 未初始化")。
func h2CheckerAdapter(cmd string) (bool, string) {
	decision := security.CheckCommand(cmd)
	return decision.Action == security.Allow, decision.Reason
}

// wireH11N27Memory 在 tui.NewModel 内调用，完成 H11N27 6 接口注入。
// 时序约束：必须在 N12 OnMemory 接线完成后调用（RegisterCommands 包装模式要求 prevOnMemory 非 nil）。
// 降级策略：任一接口注入失败 log warn 不阻塞，下游走对应降级路径。
//
// 参数：
// - cfg：配置（含 Evolution 等，本函数未使用，预留扩展）
// - client：AI SimpleClient（用于 aiCallerAdapter）
// - cmdRegistry：命令注册表（用于 RegisterCommands）
// - homeDir：用户主目录（~/.whale/memory/_user/）
// - workDir：工作目录（~/.whale/memory/{hash}/）
// - ctx：顶层 ctx，用于 ForgettingScanner goroutine 联动取消
//
// 返回：等待函数；调用方取消 ctx 后必须调用，确保扫描协程退出后再释放目录和其他资源。
func wireH11N27Memory(cfg *config.Config, client *ai.SimpleClient, cmdRegistry *command.Registry, homeDir, workDir string, ctx context.Context) func() {
	_ = cfg // 预留扩展，当前未使用
	baseDir := whalehome.Dir()
	var scannerWaitGroup sync.WaitGroup

	// 1. 创建 userStore + projectStore + MergeLayer
	userStore, err := memory.NewMemoryStoreForScope(baseDir, "user", false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[wiring] H11N27 userStore init failed: %v\n", err)
		userStore = nil
	}
	projectStore, err := memory.NewMemoryStoreForProject(baseDir, workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[wiring] H11N27 projectStore init failed: %v\n", err)
		projectStore = nil
	}
	// TD-11 生产接线：将 projectStore 注入为默认文件存储实例，使 verification/capability
	// 的 loadEntryByName/loadEntryByKey/saveEntry 从内存 map stub 切换到文件持久化。
	// projectStore 为项目作用域，与三分类系统的 verification/capability 主作用域一致。
	// 未注入时（projectStore==nil）保留 nil，verification.go 内部回退到内存 map（测试场景兼容）。
	memory.SetDefaultMemoryStore(projectStore)
	if userStore != nil && projectStore != nil {
		mergeLayer := memory.NewMergeLayer(userStore, projectStore)
		memory.SetGlobalMergeLayer(mergeLayer)
	} else {
		fmt.Fprintf(os.Stderr, "[wiring] H11N27 SetGlobalMergeLayer skipped (store nil)\n")
	}

	// 2. 注入 aiCaller（aiCallerAdapter 包装 SimpleClient.Stream）
	if client != nil {
		adapter := &aiCallerAdapter{client: client}
		memory.SetAICaller(adapter.Call)
	} else {
		fmt.Fprintf(os.Stderr, "[wiring] H11N27 SetAICaller skipped (client nil)\n")
	}

	// 3. 注入 H2 校验器（h2CheckerAdapter 包装 security.CheckCommand）
	// 注：T0 §3 冲突 5 声明错误，security.CheckCommand 返回 Decision 非 (bool, string)，必须用 wrapper
	memory.SetH2Checker(h2CheckerAdapter)

	// 4. N17 ProbeCapability 跳过注入（doctor 包无 ProbeCapability 函数，T0 §3 冲突 6 声明错误）
	// builtin_probes.go:79 走自实现 shell 探测降级
	memory.SetN17ProbeCapability(nil)
	fmt.Fprintf(os.Stderr, "[wiring] H11N27 N17 ProbeCapability not injected, builtin_probes will use shell fallback\n")

	// 5. 注册 H11N27 5 子命令（confirm/probe/invalidate/reflect/compact）
	// 时序：必须在 N12 OnMemory 接线后调用（RegisterCommands 包装 prevOnMemory）
	if cmdRegistry != nil {
		if err := memory.RegisterCommands(cmdRegistry); err != nil {
			fmt.Fprintf(os.Stderr, "[wiring] H11N27 RegisterCommands failed: %v\n", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "[wiring] H11N27 RegisterCommands skipped (cmdRegistry nil)\n")
	}

	// 6. 启动 ForgettingScanner goroutine（user + project，ctx 联动取消）
	// StartForgettingScanner 会阻塞到 ctx 取消，因此用 WaitGroup 追踪退出，避免取消后仍写目录。
	startScanner := func(scopeDir string) {
		scannerWaitGroup.Add(1)
		go func() {
			defer scannerWaitGroup.Done()
			memory.StartForgettingScanner(ctx, scopeDir)
		}()
	}
	if userStore != nil {
		startScanner(userStore.Dir())
	}
	if projectStore != nil {
		startScanner(projectStore.Dir())
	}

	return scannerWaitGroup.Wait
}

// wireExecutorMemoryCallbacks 把 memory.GetCapability / memory.ReflectMemories
// 适配为闭包注入到 Executor.CapabilityLookup / ReflectTrigger 函数字段（桶 X3 病灶1 接线层补充）。
//
// 背景：桶 X3 在 Executor struct 上新增了 CapabilityLookup / ReflectTrigger 两个函数字段
// 用于解耦 tools→memory 的循环依赖（memory 包已依赖 command→github→tools），但接线层
// 未在 tui/main.go 注入实际实现，导致字段恒为 nil，AgentContext fallback 后仍跳过
// capability 注入与反思触发，memory 端到端仍处半连通状态。
//
// 修复策略（函数注入模式）：
// - CapabilityLookup 闭包包装 memory.GetCapability：
// 返回 (cv.Value 转 string, cv.Trust, err)；ErrCapabilityNotFound 透传给
// AgentContext.loadAgentCapability 由其按 "not found" 关键字静默降级。
// - ReflectTrigger 闭包包装 memory.ReflectMemories：
// 传入默认 ReflectOptions{}（由 memory 内部回填 MaxProcedural 等默认值），
// 丢弃返回的 procedural 名称列表（调用方仅关心 error）。
//
// 降级策略：executor==nil 时直接返回（不 panic），保持与现有测试兼容；
// memory 包内 globalMergeLayer/aiCaller 未注入时由 memory 内部返回 error，
// AgentContext.triggerReflectAsync / loadAgentCapability 静默降级（仅记 warn）。
//
// 时序约束：应在 wireH11N27Memory 之后调用（依赖 SetGlobalMergeLayer + SetAICaller 已完成），
// 否则 ReflectMemories 会因 aiCaller==nil 返回 error。
//
// 参数：
// - executor: 工具执行器实例（其 CapabilityLookup/ReflectTrigger/MemorySearchFunc 字段将被覆盖）
// - homeDir/workDir: 记忆目录基座（~/.whale + 工作目录 hash），供 P4 检索 Source 构建
func wireExecutorMemoryCallbacks(executor *tools.Executor, homeDir, workDir string) {
	if executor == nil {
		fmt.Fprintf(os.Stderr, "[wiring] wireExecutorMemoryCallbacks skipped (executor nil)\n")
		return
	}

	// 注入 CapabilityLookup 闭包：包装 memory.GetCapability
	// 返回值约定：value=capability 内容字符串；trust=信任标记（"capability" / "capability:unverified"）；
	// err 透传（包含 "not found" 关键字时由调用方静默降级）。
	executor.CapabilityLookup = func(key string) (string, string, error) {
		cv, err := memory.GetCapability(key)
		if err != nil {
			return "", "", err
		}
		// CapabilityValue.Value 为 interface{}，转为字符串供 AgentContext 拼接 system prompt
		valueStr := fmt.Sprintf("%v", cv.Value)
		return valueStr, cv.Trust, nil
	}

	// 注入 ReflectTrigger 闭包：包装 memory.ReflectMemories
	// 传入空 ReflectOptions（memory 内部回填 MaxProcedural=3 等默认值）；
	// 丢弃返回的 procedural 名称列表（调用方 triggerReflectAsync 仅关心 error）。
	executor.ReflectTrigger = func(ctx context.Context, sourceNames []string, scope string) error {
		_, err := memory.ReflectMemories(ctx, sourceNames, scope, memory.ReflectOptions{})
		return err
	}

	// 注入 MemorySearchFunc 闭包（P4，2026-10-08）：包装 memorybridge.Source.Search ——
	// 模型主动检索记忆（对标 同类 CLI memories.search）。Source 内部持有**显式 merge 层**，
	// 与注入路径（List/Rank）同一数据源，不读包级全局层（P3-1b 回执 §4 / ：
	// 「同一读路径两个数据源」会静默读不同 workDir）。
	// NewSource 失败 ⇒ MemorySearchFunc 保持 nil ⇒ memory_search handler 走
	// "not wired" 软失败（与其余接口的降级策略一致，不阻塞主流程）。
	if src, err := memorybridge.NewSource(homeDir, workDir); err != nil {
		fmt.Fprintf(os.Stderr, "[wiring] P4 MemorySearchFunc skipped (memorybridge init: %v)\n", err)
	} else {
		executor.MemorySearchFunc = func(ctx context.Context, query string, topK int) ([]tools.MemorySearchHit, error) {
			entries, err := src.Search(ctx, query, topK)
			if err != nil {
				return nil, err
			}
			hits := make([]tools.MemorySearchHit, 0, len(entries))
			for _, entry := range entries {
				if entry == nil {
					continue
				}
				hit := tools.MemorySearchHit{
					Name:        entry.Name,
					Description: entry.Description,
					Type:        string(entry.Type),
					Path:        entry.FilePath,
					Updated:     entry.UpdatedAt.Format("2006-01-02"),
				}
				// description 为空时用正文首个非空行兜底（模型至少能看到一段可判读文本）。
				if hit.Description == "" && entry.Content != "" {
					hit.Excerpt = firstNonEmptyLine(entry.Content, 160)
				}
				hits = append(hits, hit)
			}
			return hits, nil
		}
	}
}

// firstNonEmptyLine 取 content 第一个非空行，超长截断（P4 摘录兜底）。
// 跳过空行 / frontmatter 分隔（---）与纯标题行（# 开头）——标题语义已在 Name/Description 里。
func firstNonEmptyLine(content string, maxRunes int) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "---") {
			continue
		}
		r := []rune(line)
		if len(r) > maxRunes {
			return string(r[:maxRunes]) + "…"
		}
		return line
	}
	return ""
}
