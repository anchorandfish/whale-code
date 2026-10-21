package hook

import "fmt"

// filterHooksBySource 按来源过滤钩子列表
// keep=true 保留匹配 source 的钩子；keep=false 移除匹配 source 的钩子
func filterHooksBySource(hooks []Hook, source HookSource, keep bool) []Hook {
	var result []Hook
	for _, h := range hooks {
		if (h.Base().Source == source) == keep {
			result = append(result, h)
		}
	}
	return result
}

// shouldReturnResult 判断钩子结果是否应短路返回
// 纯阻止/允许规则（HookCommand 且 Command 为空）不短路，允许后续钩子继续匹配
//
// ⚠️ D1：本函数只回答"这个**参与了决策**的 hook 结果是否要短路"，不回答"要不要跳过它"。
// 被跳过的 hook（Skipped=true，未信任 / 显式禁用）返回的是 ActionAllow + 完整 Command，
// 若交给本函数就会短路整条链 ⇒ 静默吃掉后面已信任 hook 的 block。
// 调用方必须先判 `result.Skipped` 并 `continue`（见 lifecycle_hook.go 的 4 处循环）。
func shouldReturnResult(h Hook) bool {
	if cmd, ok := h.(*HookCommand); ok {
		return cmd.Command != ""
	}
	return true // 非 command 类型总是短路
}

// ExecuteHookWithPayload 执行钩子（带 stdin payload 和环境变量）
// 对标同类 CLI hook 执行机制：
// - 通过 stdin 传递 JSON payload
// - 通过环境变量传递 HOOK_* 变量
// - 解析 stdout JSON 控制输出（仅 PreToolUse）
// - 生命周期事件忽略 deny
//
// SNAPSHOT：入口/出口插桩（startSpan/endSpan），obs=nil 时零开销。
// defer endSpan 自动覆盖所有 return 路径，无需逐个 return 处插桩。
// 注意：result 必须在函数作用域声明（非分支内 :=），否则 defer 无法捕获。
func (m *Manager) ExecuteHookWithPayload(h Hook, payload HookPayload, eventType HookEventType) HookResult {
	// 启动 span（obs=nil 时返回零值，零开销）
	span := m.startSpan(h, payload, eventType)
	// result 必须在 defer 之前声明，确保 defer 捕获同一变量
	var result HookResult
	// defer endSpan：捕获 result 指针，所有 return 路径自动记录 span
	defer m.endSpan(&span, &result)
	// D3：同一个兜底点把结果里的结构化事实送出口（环形缓冲 + 结构化日志）。
	// 放在 defer 里而不是各 return 处 —— 本函数有 4 个 return 分支，逐个插桩必然漏。
	defer func() { m.recordOutcome(outcomeFromResult(h, payload, eventType, result)) }()

	b := h.Base()

	// D2（S7-P1-①）：配置显式 `enabled:false` ⇒ 该 hook **不运行**。
	// 修复前该字段是死配置（照跑，而 `/hooks` 列表已显示 (disabled)）—— 用户以为关掉的 enforce 还在。
	// 跳过语义 = Allow + Skipped 标记：它不是一次决策，调用方必须 continue（不得短路整条链，见 D1）。
	if !b.IsEnabled() {
		result = HookResult{
			Action:     ActionAllow,
			Reason:     "hook disabled (enabled: false)",
			Skipped:    true,
			SkipReason: SkipDisabled,
			Messages: []string{
				fmt.Sprintf("[hook skip] %s hook %q is disabled (enabled: false) — not run", b.Type, b.ID),
			},
		}
		return result
	}

	// PR-A 信任门（Q1）：未信任的 hook 静默跳过、根本不运行。
	// 这是执行路径的唯一兜底点 —— 8 个 Run*Hooks 全部汇聚到本函数，
	// 4 个配置入口（LoadConfig/LoadConfigFromBytes/SetConfig/AddHook）也只能通过它产生副作用。
	// 跳过语义 = Allow（不是 Block）：hook 不被信任只意味着"它无权参与决策"，
	// 不应把工具调用一并阻断（否则 hook 配置变动会导致所有工具不可用）。
	if m.trustEnabled {
		if st := m.TrustStatus(h); st != TrustTrusted {
			result = HookResult{
				Action:     ActionAllow,
				Reason:     fmt.Sprintf("hook not trusted (%s), skipped", st),
				Skipped:    true,
				SkipReason: SkipUntrusted,
				Messages: []string{
					fmt.Sprintf("[hook skip] %s hook %q not trusted (%s) — skipped, run `/hooks review` to inspect", b.Type, b.ID, st),
				},
			}
			return result
		}
	}

	// 纯阻止动作（无命令/无类型特定配置）
	if b.Action == ActionBlock && b.Type != HookTypeCallback {
		result = HookResult{Action: ActionBlock, Reason: b.Reason}
		if eventType.IsLifecycleEvent() {
			result.Action = ActionAllow
			result.Messages = append(result.Messages, fmt.Sprintf("[hook warn] deny is ignored for lifecycle event %s: %s", eventType, b.Reason))
		}
		return result
	}

	hookType := b.Type
	if hookType == "" {
		hookType = HookTypeCommand // 向后兼容
	}

	switch hookType {
	case HookTypeCommand:
		cmd, _ := h.(*HookCommand)
		result = m.executeCommandHook(cmd, payload, eventType)
	case HookTypePrompt:
		p, _ := h.(*HookPrompt)
		result = m.executePromptHook(p, payload, eventType)
	case HookTypeAgent:
		a, _ := h.(*HookAgent)
		result = m.executeAgentHook(a, payload, eventType)
	case HookTypeHTTP:
		hh, _ := h.(*HookHTTP)
		result = m.executeHTTPHook(hh, payload, eventType)
	case HookTypeCallback:
		cb, _ := h.(*HookCallback)
		result = m.executeCallbackHook(cb, payload, eventType)
	default:
		result = HookResult{Action: ActionWarn, Messages: []string{fmt.Sprintf("[hook warn] unknown hook type: %s", hookType)}}
	}

	// Q4 改写特权（PR-A2 · ② 拍板：默认关，需单独授权）。
	//
	// 「可运行」≠「可改写」：hook 获准运行只意味着它有权参与决策（allow/block/ask/warn）；
	// 而 updatedInput / Modified 会改变 agent 的真实动作与喂回模型的输出，属强能力，
	// 必须显式 `/hooks allow-rewrite` 授权。这里在**执行之后统一剥离**，
	// 与信任门同一个兜底点 —— 覆盖 8 个 Run* 路径，不论改写指令来自 stdout JSON 还是 callback。
	if m.trustEnabled && !m.RewriteGranted(h) {
		if result.UpdatedInput != nil || result.Modified != nil {
			result.UpdatedInput = nil
			result.Modified = nil
			ref, label := b.ID, fmt.Sprintf("%q", b.ID)
			if b.ID == "" {
				ref, label = "all", "(no id, use `all`)"
			}
			result.Messages = append(result.Messages, fmt.Sprintf(
				"[hook] %s hook %s rewrite not granted — updatedInput/Modified ignored; run `/hooks allow-rewrite %s` to grant",
				b.Type, label, ref))
		}
	}
	return result
}

// ExecuteHook 执行钩子命令（向后兼容接口）
func (m *Manager) ExecuteHook(h Hook, filePath string) HookResult {
	payload := HookPayload{
		FilePath: filePath,
		Cwd:      m.workDir,
	}
	return m.ExecuteHookWithPayload(h, payload, EventPreToolUse)
}
