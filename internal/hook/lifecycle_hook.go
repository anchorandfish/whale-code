package hook

// RunPreHooks 运行所有 pre-hooks
func (m *Manager) RunPreHooks(toolName, input string) HookResult {
	filePath := extractPathFromInput(input)
	payload := HookPayload{
		HookEventName: string(EventPreToolUse),
		ToolName:      toolName,
		ToolInputJSON: input,
		Cwd:           m.workDir,
	}

	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PreToolUse))
	copy(hooks, m.config.PreToolUse)
	m.mu.RUnlock()

	// skipped 记住最近一次"跳过"结果：整条链都没做决策时把它带出去（D1/D3）。
	var skipped *HookResult

	for i := range hooks {
		h := hooks[i]
		b := h.Base()
		if !matchGlob(b.Matcher, toolName) {
			continue
		}
		if b.Pattern != "" && filePath != "" && !matchGlob(b.Pattern, filePath) {
			continue
		}
		if b.Pattern != "" && filePath == "" {
			continue
		}

		payload.FilePath = filePath
		result := m.ExecuteHookWithPayload(h, payload, EventPreToolUse)
		// D1：被跳过的 hook（未信任 / 显式禁用）不是一次决策 —— 必须下一条，不得短路整条链。
		// 但"整条链都没有决策"这件事本身要如实带出去（Skipped），否则调用方看到的
		// 是一个来历不明的 Allow（结构化事实被 last-write 掉）。
		if result.Skipped {
			skipped = &result
			continue
		}
		if result.Action == ActionBlock {
			return result
		}
		if shouldReturnResult(h) {
			return result
		}
	}

	if skipped != nil {
		return *skipped
	}
	return HookResult{Action: ActionAllow}
}

// RunPostHooks 运行所有 post-hooks
func (m *Manager) RunPostHooks(toolName, input string, output string) HookResult {
	filePath := extractPathFromInput(input)
	payload := HookPayload{
		HookEventName:     string(EventPostToolUse),
		ToolName:          toolName,
		ToolInputJSON:     input,
		ToolOutput:        output,
		ToolResultIsError: false,
		Cwd:               m.workDir,
	}

	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PostToolUse))
	copy(hooks, m.config.PostToolUse)
	m.mu.RUnlock()

	var skipped *HookResult

	for i := range hooks {
		h := hooks[i]
		b := h.Base()
		if !matchGlob(b.Matcher, toolName) {
			continue
		}
		if b.Pattern != "" && filePath != "" && !matchGlob(b.Pattern, filePath) {
			continue
		}
		if b.Pattern != "" && filePath == "" {
			continue
		}

		payload.FilePath = filePath
		result := m.ExecuteHookWithPayload(h, payload, EventPostToolUse)
		// D1：跳过 ≠ 决策，继续下一条。
		if result.Skipped {
			skipped = &result
			continue
		}
		if result.Action == ActionBlock {
			return result
		}
		if shouldReturnResult(h) {
			return result
		}
	}

	if skipped != nil {
		return *skipped
	}
	return HookResult{Action: ActionAllow}
}

// RunStopHooks 运行 stop hooks
func (m *Manager) RunStopHooks() HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.Stop))
	copy(hooks, m.config.Stop)
	m.mu.RUnlock()
	return m.runLifecycleHooks(hooks, EventStop)
}

// RunPostCompactHooks 运行 post-compact hooks
func (m *Manager) RunPostCompactHooks() HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PostCompact))
	copy(hooks, m.config.PostCompact)
	m.mu.RUnlock()
	return m.runLifecycleHooks(hooks, EventPostCompact)
}

// RunSessionStartHooks 运行 session start hooks
func (m *Manager) RunSessionStartHooks() HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.SessionStart))
	copy(hooks, m.config.SessionStart)
	m.mu.RUnlock()
	return m.runLifecycleHooks(hooks, EventSessionStart)
}

// RunSessionStopHooks 运行 session stop hooks
func (m *Manager) RunSessionStopHooks() HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.SessionStop))
	copy(hooks, m.config.SessionStop)
	m.mu.RUnlock()
	return m.runLifecycleHooks(hooks, EventSessionStop)
}

// RunPreEditHooks 运行 pre-edit hooks
func (m *Manager) RunPreEditHooks(toolName, filePath string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PreEdit))
	copy(hooks, m.config.PreEdit)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPreEdit, toolName, filePath, HookPayload{
		FilePath: filePath,
	})
}

// RunPostEditHooks 运行 post-edit hooks
func (m *Manager) RunPostEditHooks(toolName, filePath, oldContent, newContent string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PostEdit))
	copy(hooks, m.config.PostEdit)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPostEdit, toolName, filePath, HookPayload{
		FilePath:   filePath,
		OldContent: oldContent,
		NewContent: newContent,
	})
}

// RunPreBashHooks 运行 pre-Bash hooks
func (m *Manager) RunPreBashHooks(command string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PreBash))
	copy(hooks, m.config.PreBash)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPreBash, "bash", "", HookPayload{
		Command: command,
	})
}

// RunPostBashHooks 运行 post-Bash hooks
func (m *Manager) RunPostBashHooks(command, output string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PostBash))
	copy(hooks, m.config.PostBash)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPostBash, "bash", "", HookPayload{
		Command:    command,
		ToolOutput: output,
	})
}

// RunPreReadHooks 运行 pre-read hooks
func (m *Manager) RunPreReadHooks(filePath string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PreRead))
	copy(hooks, m.config.PreRead)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPreRead, "read_file", filePath, HookPayload{
		FilePath: filePath,
	})
}

// RunPostReadHooks 运行 post-read hooks
func (m *Manager) RunPostReadHooks(filePath, content string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PostRead))
	copy(hooks, m.config.PostRead)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPostRead, "read_file", filePath, HookPayload{
		FilePath:   filePath,
		ToolOutput: content,
	})
}

// RunPreWriteHooks 运行 pre-write hooks
func (m *Manager) RunPreWriteHooks(filePath string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PreWrite))
	copy(hooks, m.config.PreWrite)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPreWrite, "write_file", filePath, HookPayload{
		FilePath: filePath,
	})
}

// RunPostWriteHooks 运行 post-write hooks
func (m *Manager) RunPostWriteHooks(filePath string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PostWrite))
	copy(hooks, m.config.PostWrite)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPostWrite, "write_file", filePath, HookPayload{
		FilePath: filePath,
	})
}

// RunPreSearchHooks 运行 pre-search hooks
func (m *Manager) RunPreSearchHooks(query string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PreSearch))
	copy(hooks, m.config.PreSearch)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPreSearch, "search", "", HookPayload{
		Command: query,
	})
}

// RunPostSearchHooks 运行 post-search hooks
func (m *Manager) RunPostSearchHooks(query, results string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PostSearch))
	copy(hooks, m.config.PostSearch)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPostSearch, "search", "", HookPayload{
		Command:    query,
		ToolOutput: results,
	})
}

// RunPreGitHooks 运行 pre-Git hooks
func (m *Manager) RunPreGitHooks(operation string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PreGit))
	copy(hooks, m.config.PreGit)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPreGit, "git", "", HookPayload{
		Command: operation,
	})
}

// RunPostGitHooks 运行 post-Git hooks
func (m *Manager) RunPostGitHooks(operation, output string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PostGit))
	copy(hooks, m.config.PostGit)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPostGit, "git", "", HookPayload{
		Command:    operation,
		ToolOutput: output,
	})
}

// RunPreWebHooks 运行 pre-Web hooks
func (m *Manager) RunPreWebHooks(url string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PreWeb))
	copy(hooks, m.config.PreWeb)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPreWeb, "web", "", HookPayload{
		Command: url,
	})
}

// RunPostWebHooks 运行 post-Web hooks
func (m *Manager) RunPostWebHooks(url, content string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PostWeb))
	copy(hooks, m.config.PostWeb)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPostWeb, "web", "", HookPayload{
		Command:    url,
		ToolOutput: content,
	})
}

// RunPreAgentHooks 运行 pre-agent hooks
func (m *Manager) RunPreAgentHooks(agentType string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PreAgent))
	copy(hooks, m.config.PreAgent)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPreAgent, "agent", "", HookPayload{
		Command: agentType,
	})
}

// RunPostAgentHooks 运行 post-agent hooks
func (m *Manager) RunPostAgentHooks(agentType, output string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.PostAgent))
	copy(hooks, m.config.PostAgent)
	m.mu.RUnlock()
	return m.runToolHooks(hooks, EventPostAgent, "agent", "", HookPayload{
		Command:    agentType,
		ToolOutput: output,
	})
}

// runLifecycleHooks 运行生命周期 hooks（deny 被忽略）
func (m *Manager) runLifecycleHooks(hooks []Hook, eventType HookEventType) HookResult {
	var skipped *HookResult
	for i := range hooks {
		h := hooks[i]
		payload := HookPayload{
			HookEventName: string(eventType),
			Cwd:           m.workDir,
		}
		result := m.ExecuteHookWithPayload(h, payload, eventType)
		// D1：跳过 ≠ 决策，继续下一条。
		if result.Skipped {
			skipped = &result
			continue
		}
		if shouldReturnResult(h) || h.Base().Action == ActionBlock {
			return result
		}
	}
	if skipped != nil {
		return *skipped
	}
	return HookResult{Action: ActionAllow}
}

// runToolHooks 运行工具 hooks（deny 生效）
func (m *Manager) runToolHooks(hooks []Hook, eventType HookEventType, toolName, filePath string, basePayload HookPayload) HookResult {
	var skipped *HookResult
	for i := range hooks {
		h := hooks[i]
		b := h.Base()
		if !matchGlob(b.Matcher, toolName) {
			continue
		}
		if b.Pattern != "" && filePath != "" && !matchGlob(b.Pattern, filePath) {
			continue
		}

		basePayload.HookEventName = string(eventType)
		basePayload.ToolName = toolName
		basePayload.FilePath = filePath
		basePayload.Cwd = m.workDir

		result := m.ExecuteHookWithPayload(h, basePayload, eventType)
		// D1：跳过 ≠ 决策，继续下一条。
		if result.Skipped {
			skipped = &result
			continue
		}
		if result.Action == ActionBlock {
			return result
		}
		if shouldReturnResult(h) {
			return result
		}
	}
	if skipped != nil {
		return *skipped
	}
	return HookResult{Action: ActionAllow}
}
