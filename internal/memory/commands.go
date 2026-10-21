// commands.go — H11/N27 Phase B B6 + Phase C C1 命令注册（/memory confirm/probe add/invalidate/reflect/compact）。
// ；落地：3 子命令通过 RegisterCommands 包装 r.OnMemory 回调，扩展 commandCompletions["memory"]（在 command_list.go 修改）
// Q4-ProbeFn H2 校验；落地：/memory probe add 的 shell 命令复用 RegisterShellProbe（内部走 H2 校验或 basicDangerCheck 降级）
// 委托；落地：H11/N27 用户自定义 ProbeFn 若是 shell 命令，注册时必须委托 H2 校验（RegisterShellProbe 已实现）
// /memory invalidate；落地：handleInvalidate 调 InvalidateMemory（B5 已实现）
// /memory reflect；落地：HandleReflect 调 ReflectMemories（C1 实现）
// SNAPSHOT: commands.go={5子命令: /memory confirm(unverified→verified), /memory probe add(H2校验), /memory invalidate(调InvalidateMemory), /memory reflect(调ReflectMemories, C1), /memory compact(调CompactContext, C2); RegisterCommands=包装r.OnMemory+追加H11N27子命令分发+未处理回退N12}

package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/command"
)

// RegisterCommands 注册 H11/N27 Phase B 的 3 个子命令到 command.Registry。
// 对齐 02 §2.5.1 Q5：通过包装 r.OnMemory 回调追加 H11/N27 子命令分发。
//
// 设计（包装模式，不破坏 N12 现有逻辑）：
// - 保存旧 r.OnMemory（N12 app.go 接线时注入的 list/edit/show/add/delete 逻辑）
// - 注入新 r.OnMemory：先尝试 H11/N27 子命令（confirm/probe/invalidate），未处理回退到旧 OnMemory
// - app.go 接线顺序：N12 注入 OnMemory → memory.RegisterCommands(r) 包装追加
//
// 子命令：
// - /memory confirm <name>：unverified→verified（用户确认 ReflectMemories 生成的记忆）
// - /memory probe add <key> <command>：注册用户自定义 ProbeFn（shell 命令走 H2 校验， Q4）
// - /memory invalidate <name> <reason>：标记记忆失效（B5 实现）
//
// 注：commandCompletions["memory"].SubCommands 的扩展在 command_list.go 修改（B6 直接改源码）。
func RegisterCommands(r *command.Registry) error {
	if r == nil {
		return errors.New("RegisterCommands: registry is nil")
	}

	// 包装现有 OnMemory 回调，追加 H11/N27 子命令分发
	prevOnMemory := r.OnMemory
	r.OnMemory = func(args string) (string, error) {
		handled, result, err := DispatchMemorySubcommand(args)
		if handled {
			return result, err
		}
		// 未匹配 H11/N27 子命令，回退到 N12 现有逻辑
		if prevOnMemory != nil {
			return prevOnMemory(args)
		}
		return "Memory: feature not connected", nil
	}
	return nil
}

// DispatchMemorySubcommand 分发 H11/N27 Phase B 的 3 个子命令。
// 返回 (handled, result, err)：
// - handled=true：已处理（confirm/probe/invalidate），返回 result/err
// - handled=false：未匹配 H11/N27 子命令，调用方应回退到 N12 逻辑
//
// 解析规则：
// - "confirm <name>" → handleConfirm
// - "probe add <key> <command>" → handleProbeAdd
// - "probe" 单独 → 返回 Usage
// - "invalidate <name> <reason>" → handleInvalidate
// - 其他（list/edit/show/add/delete 等） → handled=false，回退 N12
func DispatchMemorySubcommand(args string) (handled bool, result string, err error) {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return false, "", nil
	}

	switch parts[0] {
	case "confirm":
		// /memory confirm <name>
		rest := strings.TrimSpace(strings.TrimPrefix(args, "confirm"))
		result, err := HandleConfirm(rest)
		return true, result, err
	case "probe":
		// /memory probe add <key> <command>
		rest := strings.TrimSpace(strings.TrimPrefix(args, "probe"))
		result, err := HandleProbe(rest)
		return true, result, err
	case "invalidate":
		// /memory invalidate <name> <reason>
		rest := strings.TrimSpace(strings.TrimPrefix(args, "invalidate"))
		result, err := HandleInvalidate(rest)
		return true, result, err
	case "reflect":
		// /memory reflect <scope>（C1 实现）
		rest := strings.TrimSpace(strings.TrimPrefix(args, "reflect"))
		result, err := HandleReflect(rest)
		return true, result, err
	case "compact":
		// /memory compact --task <task-id>（C2 实现）
		rest := strings.TrimSpace(strings.TrimPrefix(args, "compact"))
		result, err := HandleCompact(rest)
		return true, result, err
	}
	// 未匹配 H11/N27 子命令，回退到 N12
	return false, "", nil
}

// HandleConfirm 处理 /memory confirm <name> 子命令。
// 将 unverified 记忆标记为 verified（用户确认 ReflectMemories 生成的记忆）。
// 调用 transitionVerificationOnFile 复用 Phase A TransitionVerification 公共 API。
//
// 返回：
// - 成功："Memory <name> confirmed: verification=verified"
// - 失败：error（ErrEntryNotFound / ErrInvalidTransition）
func HandleConfirm(args string) (string, error) {
	name := strings.TrimSpace(args)
	if name == "" {
		return "Usage: /memory confirm <name>", nil
	}

	if globalMergeLayer == nil {
		return "", errors.New("HandleConfirm: global MergeLayer not initialized, call SetGlobalMergeLayer first")
	}

	// 调用公共桥接函数（unverified→verified）
	if err := transitionVerificationOnFile(name, VerificationVerified, "user confirm via /memory confirm"); err != nil {
		return "", fmt.Errorf("HandleConfirm: %w", err)
	}

	return fmt.Sprintf("Memory %q confirmed: verification=%s", name, VerificationVerified), nil
}

// HandleProbe 处理 /memory probe 子命令（解析 add 子命令）。
// 当前仅支持 "add <key> <command>"，其他返回 Usage。
func HandleProbe(args string) (string, error) {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return "Usage: /memory probe add <key> <command>", nil
	}

	if parts[0] != "add" {
		return "Usage: /memory probe add <key> <command>", nil
	}

	// 剥离 "add" 后剩余部分作为 HandleProbeAdd 的 args
	rest := strings.TrimSpace(strings.TrimPrefix(args, "add"))
	return HandleProbeAdd(rest)
}

// HandleProbeAdd 处理 /memory probe add <key> <command> 子命令。
// shell 命令走 RegisterShellProbe（内部 H2 校验或 basicDangerCheck 降级， Q4）。
// 校验失败拒绝注册返回 "ProbeFn rejected by H2: <reason>"。
//
// 返回：
// - 成功："ProbeFn registered: <key> → <command>"
// - H2 拦截：error（含 ErrProbeH2Blocked）
// - 参数错误：error
func HandleProbeAdd(args string) (string, error) {
	// 解析 "<key> <command>"：key 是第一个 token，command 是剩余部分（含空格）
	rest := strings.TrimSpace(args)
	if rest == "" {
		return "Usage: /memory probe add <key> <command>", nil
	}

	// 用 SplitN 分割：key=parts[0], command=parts[1]（保留 command 中的空格）
	parts := strings.SplitN(rest, " ", 2)
	key := parts[0]
	if key == "" {
		return "Usage: /memory probe add <key> <command>", nil
	}

	var shellCmd string
	if len(parts) > 1 {
		shellCmd = strings.TrimSpace(parts[1])
	}
	if shellCmd == "" {
		return "Usage: /memory probe add <key> <command>", nil
	}

	// 注册 shell ProbeFn（内部走 H2 校验， Q4）
	if err := RegisterShellProbe(key, shellCmd); err != nil {
		return "", fmt.Errorf("HandleProbeAdd: %w", err)
	}

	return fmt.Sprintf("ProbeFn registered: %s → %s", key, shellCmd), nil
}

// HandleInvalidate 处理 /memory invalidate <name> <reason> 子命令（B5 实现，B6 注册）。
// 调用 InvalidateMemory 标记记忆失效（verification→contradicted + valid_until=now）。
//
// 返回：
// - 成功："Memory <name> invalidated: <reason>"
// - 失败：error（ErrEntryNotFound / ErrInvalidTransition）
func HandleInvalidate(args string) (string, error) {
	rest := strings.TrimSpace(args)
	if rest == "" {
		return "Usage: /memory invalidate <name> <reason>", nil
	}

	// 解析 "<name> <reason>"：name=parts[0], reason=parts[1]（保留 reason 中的空格）
	parts := strings.SplitN(rest, " ", 2)
	name := parts[0]
	if name == "" {
		return "Usage: /memory invalidate <name> <reason>", nil
	}

	var reason string
	if len(parts) > 1 {
		reason = strings.TrimSpace(parts[1])
	}
	// reason 可为空（用户仅指定 name）

	if err := InvalidateMemory(name, reason); err != nil {
		return "", fmt.Errorf("HandleInvalidate: %w", err)
	}

	return fmt.Sprintf("Memory %q invalidated: %s", name, reason), nil
}

// HandleReflect 处理 /memory reflect <scope> 子命令（C1 实现）。
// 触发反思：收集近期 episodic（7 天内 + access_count >= 2），调 AI 模型合成为 1-3 条 procedural 记忆。
//
// 参数解析：
// - args 为空 → scope 默认 "project"
// - args = "user" 或 "project" → 指定 scope
// - args = 其他 → 返回 Usage
//
// 返回：
// - 成功："Reflected N procedural memories: [name1, name2, ...]"
// - AI 失败：error（含原始 error）
// - 无可反思源："Reflected 0 procedural memories (no episodic sources match filter)"
//
// /memory reflect；落地：调用 ReflectMemories（C1 实现）
func HandleReflect(args string) (string, error) {
	scope := strings.TrimSpace(args)
	if scope == "" {
		scope = "project"
	}
	if scope != "user" && scope != "project" {
		return "Usage: /memory reflect [user|project] (default: project)", nil
	}

	// ReflectMemories 内部检查 aiCaller 是否注入
	newNames, err := ReflectMemories(context.Background(), nil, scope, ReflectOptions{
		Model:         "", // Phase 1 传主模型（app.go 接线时通过 SetAICaller 注入实际模型）
		MaxProcedural: ReflectDefaultMaxProcedural,
	})
	if err != nil {
		return "", fmt.Errorf("HandleReflect: %w", err)
	}

	if len(newNames) == 0 {
		return "Reflected 0 procedural memories (no episodic sources match filter or AI returned empty)", nil
	}

	return fmt.Sprintf("Reflected %d procedural memories: %v", len(newNames), newNames), nil
}

// HandleCompact 处理 /memory compact --task <task-id> 子命令（C2 实现）。
// 压缩指定任务的 trace+quality 为 ephemeral 记忆。
//
// 参数解析：
// - "--task <task-id>"：必填，指定任务 ID
// - "--trace <path>"：可选，覆盖默认 trace.jsonl 路径
// - "--quality <path>"：可选，覆盖默认 quality.jsonl 路径
// - "--schema <version>"：可选，覆盖默认 schema 版本（默认 "v1"）
//
// 默认路径推导：
// - trace: ${workDir}/.whale/orchestrator/<task-id>/trace.jsonl
// - quality: ${workDir}/.whale/orchestrator/quality.jsonl
//
// 返回：
// - 成功："Compacted task <task-id> to memory: [name1, ...]"
// - schema drift：error（含 "schema drift" warn 信息）
// - AI 失败：error（含原始 error）
//
// /memory compact；落地：调用 CompactContext（C2 实现）
func HandleCompact(args string) (string, error) {
	return HandleCompactWithWorkDir(args, "")
}

// HandleCompactWithWorkDir 带工作目录的 HandleCompact 实现（app.go 接线时传入实际 workDir）。
// workDir 为空时，--task/--trace/--quality 参数必须显式指定路径。
func HandleCompactWithWorkDir(args, workDir string) (string, error) {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return "Usage: /memory compact --task <task-id> [--trace <path>] [--quality <path>] [--schema <version>]", nil
	}

	// 解析 --task / --trace / --quality / --schema 参数
	var taskID, tracePath, qualityPath, schemaVersion string
	for i := 0; i < len(parts); i++ {
		switch parts[i] {
		case "--task":
			if i+1 < len(parts) {
				taskID = parts[i+1]
				i++
			}
		case "--trace":
			if i+1 < len(parts) {
				tracePath = parts[i+1]
				i++
			}
		case "--quality":
			if i+1 < len(parts) {
				qualityPath = parts[i+1]
				i++
			}
		case "--schema":
			if i+1 < len(parts) {
				schemaVersion = parts[i+1]
				i++
			}
		}
	}

	if taskID == "" {
		return "Usage: /memory compact --task <task-id> [--trace <path>] [--quality <path>] [--schema <version>]", nil
	}

	// 默认 schema 版本
	if schemaVersion == "" {
		schemaVersion = DefaultCompactSchemaVersion
	}

	// 默认路径推导（仅当 workDir 非空且未显式指定路径时）
	if workDir != "" {
		if tracePath == "" {
			tracePath = workDir + "/.whale/orchestrator/" + taskID + "/trace.jsonl"
		}
		if qualityPath == "" {
			qualityPath = workDir + "/.whale/orchestrator/quality.jsonl"
		}
	}

	source := CompactSource{
		TaskID:        taskID,
		TracePath:     tracePath,
		QualityPath:   qualityPath,
		SchemaVersion: schemaVersion,
	}

	newNames, err := CompactContext(context.Background(), source)
	if err != nil {
		return "", fmt.Errorf("HandleCompact: %w", err)
	}

	if len(newNames) == 0 {
		return fmt.Sprintf("Compacted task %q but no memory written (AI returned empty)", taskID), nil
	}

	return fmt.Sprintf("Compacted task %q to memory: %v", taskID, newNames), nil
}
