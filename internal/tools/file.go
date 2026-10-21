package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/diff"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/secret"
	"github.com/anchorandfish/whale-code/internal/util"
)

// errSandboxDeniedSoft 是本包**所有**沙箱/越界拒绝的「软」哨兵（S11 G1-B 定型，G1-A 收口）。
//
// 与 bash 的 errSandboxDenied（executeDirect 映射为 IsError=true + FailureKind）不同，
// 本哨兵刻意保留模型的非-error 契约（IsError=false），同时由 executeDirect 打上
// FailureKind=FailureKindSandboxDenied，供编排/TUI 识别为「终局拒绝」——这正是
// TUI tool_exec.go 阻断 write_file→bash→edit_file 噪声循环所依赖的标记。
//
// 用法（两种等价形态，都落到 executeDirect 的同一个软分支）：
// - 直接返回：(msg, errSandboxDeniedSoft)
// —— file.go（write_file/edit_file）· monitor.go · search.go（glob/grep）
// · executor.go（插件工具直 exec 路径）；
// - 经 Unwrap 承载：实现 `Unwrap() error { return errSandboxDeniedSoft }`
// —— apply_diff.go 的 sandboxDeniedError（需同时携带结构化文案时）。
//
// ⚠️ 判据必须是**结构化**的（错误链上的哨兵），不得退回解析本地化文本（R1 契约 C4）。
// R4 的拒绝检测收口（roundDeniedKind）依赖这里打上的 FailureKind。
// （「拒绝上报统一」选项 A：把 IsError 也统一为 true —— 另立轮次，本轮不改 IsError 语义。）
var errSandboxDeniedSoft = errors.New("sandbox denied (soft)")

// sanitizeInjectionPatternsFn 中和 prompt injection 模式（D7-1，REF-OWASP-LLM01）。
// 默认 nil（fail-open，返回原内容）；由 prompt 包通过 SetSanitizeInjectionFn 注入实际实现，
// 避免 tools→prompt 循环导入（prompt/builder.go 已 import tools，反向 import 会成环）。
var sanitizeInjectionPatternsFn func(string) string

// SetSanitizeInjectionFn 注入 prompt injection 中和函数（由 prompt 包 init 调用）。
// 传入 nil 可重置为 fail-open no-op（测试清理用）。
func SetSanitizeInjectionFn(fn func(string) string) { sanitizeInjectionPatternsFn = fn }

// ReadFileInput is the read_file tool input
type ReadFileInput struct {
	Path   string `json:"path" desc:"File path"`
	Offset int    `json:"offset,omitempty" desc:"Starting line number (1-based)"`
	Limit  int    `json:"limit,omitempty" desc:"Number of lines to read"`
	// ReturnMtime 为 true 时，读取结果追加 [mtime:<unixnano>] 尾行，
	// 供后续 WriteFile/EditFile 的 ExpectedMtime 守卫使用（T6 mtime 守卫闭环）。
	ReturnMtime bool `json:"return_mtime,omitempty" desc:"If true, append mtime to result for write guard"`
}

// WriteFileInput is the write_file tool input
type WriteFileInput struct {
	Path    string `json:"path" desc:"File path"`
	Content string `json:"content" desc:"File content"`
	// ExpectedMtime 非空时，写入前比对文件当前 mtime；不匹配则拒绝写入，
	// 防止覆盖外部修改（T6 mtime 守卫，对标同类 CLI）。nil 跳过守卫。
	ExpectedMtime *time.Time `json:"expected_mtime,omitempty" desc:"Expected mtime for write guard (skip if empty)"`
}

// EditFileInput is the edit_file tool input
type EditFileInput struct {
	Path    string `json:"path" desc:"File path"`
	OldText string `json:"old_text" desc:"Text to replace"`
	NewText string `json:"new_text" desc:"Replacement text"`
	// ExpectedMtime 非空时，编辑前比对文件当前 mtime；不匹配则拒绝（T6 mtime 守卫）。
	ExpectedMtime *time.Time `json:"expected_mtime,omitempty" desc:"Expected mtime for edit guard (skip if empty)"`
}

// readFileHandler reads file contents
func (e *Executor) readFileHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input ReadFileInput
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if input.Path == "" {
			return "", fmt.Errorf("file path cannot be empty")
		}

		// T8: 路径遍历 + UNC 拦截（validateFilePath 内含 UNC 检测）
		if err := validateFilePath(input.Path); err != nil {
			return "", err
		}

		// C14: 文件操作工具调 sandbox.ValidatePath（Manager 优先，回退 legacy）
		if err := e.validatePathSandbox(ctx, input.Path); err != nil {
			return "", err
		}

		// T6: 按需采集 mtime，供 ReturnMtime 尾行使用（守卫闭环）
		var fileMtime time.Time
		if input.ReturnMtime {
			if st, stErr := os.Stat(input.Path); stErr == nil {
				fileMtime = st.ModTime()
			}
		}

		// sanitizeAndReturn 对读取内容应用 prompt injection 中和后追加 mtime 尾行（D7-1）。
		// 统一收口 readFileHandler 的 4 个返回点，避免重复 sanitize 调用。
		// fail-open：sanitizeInjectionPatternsFn 为 nil（未注入）或 panic 时回退原始内容，
		// 不阻断读取（REF-OWASP-LLM01）。
		sanitizeAndReturn := func(content string) (string, error) {
			// 登记「读基准」—— 以**盘上原始字节**为准
			//（此处 content 可能带行号/净化，不能当基准）。多一次磁盘读是
			// 刻意选择的代价：换 「hash 永远等于真盘内容」的正确性，不去猜
			// 各返回路径的 content 变换。登记失败（文件此刻已消失）静默跳过。
			if abs, absErr := filepath.Abs(input.Path); absErr == nil {
				if data, readErr := os.ReadFile(input.Path); readErr == nil {
					e.recordReadBase(abs, data)
				}
			}
			sanitized := content
			if sanitizeInjectionPatternsFn != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("[readFileHandler] sanitize panic (fail-open): %v", r)
							sanitized = content
						}
					}()
					sanitized = sanitizeInjectionPatternsFn(content)
				}()
			}
			return appendMtimeTrailer(sanitized, fileMtime, input.ReturnMtime), nil
		}

		// Record file read context
		if e.SearchCtx != nil {
			e.SearchCtx.RecordRead(input.Path)
		}

		// cache-aware read: only for full file reads (no offset/limit)
		if e.FileCache != nil && input.Offset == 0 && input.Limit == 0 {
			result := e.FileCache.Read(input.Path)

			if result.TokensSaved > 0 {
				e.FileCache.Metrics().RecordFileCacheHit(
					result.FilePath,
					result.Status,
					result.FullTokens,
					result.TokensSaved,
				)
			}

			if result.Status == CacheUnchanged || result.Status == CacheDiff {
				return sanitizeAndReturn(result.Content)
			}
			// CacheFull: fall through to existing logic (adds line numbers)
			// CacheError: fall through to existing logic (error handling)
		}

		const maxReadFileSize = 10 * 1024 * 1024
		info, err := os.Stat(input.Path)
		if err != nil {
			return "", fmt.Errorf("failed to stat file: %w", err)
		}
		if info.Size() > maxReadFileSize {
			return "", fmt.Errorf("file too large (%d bytes, max %d bytes)", info.Size(), maxReadFileSize)
		}
		// T6: 若未在前面采集到 mtime（ReturnMtime=true 但缓存未命中路径），此处补采
		if input.ReturnMtime && fileMtime.IsZero() {
			fileMtime = info.ModTime()
		}

		f, err := os.Open(input.Path)
		if err != nil {
			return "", fmt.Errorf("failed to read file: %w", err)
		}
		defer f.Close()

		var lines []string
		scanner := bufio.NewScanner(f)
		maxLines := 10000
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
			if len(lines) >= maxLines {
				break
			}
		}
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("failed to read file: %w", err)
		}

		if input.Offset > 0 || input.Limit > 0 {
			start := input.Offset - 1
			if start < 0 {
				start = 0
			}
			if start > len(lines) {
				start = len(lines)
			}

			end := len(lines)
			if input.Limit > 0 {
				end = start + input.Limit
				if end > len(lines) {
					end = len(lines)
				}
			}

			selected := lines[start:end]
			var sb strings.Builder
			for i, line := range selected {
				sb.WriteString(fmt.Sprintf("%6d→%s\n", start+i+1, line))
			}
			return sanitizeAndReturn(sb.String())
		}

		if len(lines) > 5000 {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("File has %d lines, showing first 5000 lines:\n", len(lines)))
			for i, line := range lines[:5000] {
				sb.WriteString(fmt.Sprintf("%6d→%s\n", i+1, line))
			}
			return sanitizeAndReturn(sb.String())
		}

		var sb strings.Builder
		for i, line := range lines {
			sb.WriteString(fmt.Sprintf("%6d→%s\n", i+1, line))
		}
		return sanitizeAndReturn(sb.String())
	}
}

// appendMtimeTrailer 在读取结果尾部追加 mtime 标记行（T6 守卫闭环）。
// 仅当 returnMtime=true 时追加；格式：\n[mtime:<unixnano>]。
func appendMtimeTrailer(content string, mtime time.Time, returnMtime bool) string {
	if !returnMtime || mtime.IsZero() {
		return content
	}
	return content + fmt.Sprintf("\n[mtime:%d]", mtime.UnixNano())
}

// detectSecretLeakForWrite 检测写入内容是否含密钥模式，命中时返回命中的规则标签列表。
// D7-2 重构：逻辑收口到 secret.DetectLeakWithLabels 共用函数（消除与 github 包重复实现）。
// fail-open：内部 panic 时返回 ("", false) 不阻断主流程（REF-OWASP-LLM02 + REF-）。
// 供 writeFileHandler / editFileHandler 在写磁盘前调用。
func detectSecretLeakForWrite(content string) (labels string, hit bool) {
	return secret.DetectLeakWithLabels(content)
}

// writeFileHandler writes a file
func (e *Executor) writeFileHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input WriteFileInput
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.Path == "" {
			return "", fmt.Errorf("path is required")
		}

		// T8: 路径遍历 + UNC 拦截
		if err := validateFilePath(input.Path); err != nil {
			return "", err
		}

		// C14: 文件操作工具调 sandbox.ValidatePath（Manager 优先，回退 legacy）
		// 注：writable-root 收敛（F2）已移除——生产管线 CustomValidateStage
		//（pipeline.go:487-501）在写类工具越出 workDir 时前置拒绝，F2 在管线内不可达
		// （死代码）。此处保留 C14 沙箱路径验证作为 handler 底线（无管线/未接线兜底）。
		if err := e.validatePathSandbox(ctx, input.Path); err != nil {
			// S11 G1-B：保留 (msg, !nil-soft) —— executeDirect 映射为
			// IsError=false + FailureKind=SandboxDenied（模型文本不变 + 结构化标记）。
			return fmt.Sprintf("Sandbox denied: %s", err.Error()), errSandboxDeniedSoft
		}

		// 写基准守卫 —— 服务端自动强制，不依赖模型传参。
		// 本会话经 read_file 读过该文件、且盘上现值已不等于读时内容 ⇒ 别人改过了，
		// 拒绝整篇覆写（结构化 patch_conflict：Terminal=false，模型重读后重写即自愈）。
		// 未登记（没读过/新建）⇒ 放行，与修复前行为一致（见 read_base.go 语义约定）。
		// 位置在 T6（opt-in）之前：本守卫是权威面，T6 保留兼容模型的显式传参。
		writeAbsPath, absErr := filepath.Abs(input.Path)
		if absErr == nil && e.checkWriteBase(writeAbsPath) == writeBaseStale {
			return "", &ToolExecError{
				Tool: "write_file",
				Kind: FailureKindPatchConflict,
				Msg: fmt.Sprintf("write_file: file %q has changed on disk since it was last read in this session (modified externally). "+
					"Re-read the file with read_file, then write again.", input.Path),
			}
		}

		// T6: mtime 守卫 — 写入前比对 ExpectedMtime，防止覆盖外部修改
		if input.ExpectedMtime != nil {
			if info, stErr := os.Stat(input.Path); stErr == nil {
				if !info.ModTime().Equal(*input.ExpectedMtime) {
					return "", fmt.Errorf("file modified externally: expected mtime %v, got %v",
						input.ExpectedMtime, info.ModTime())
				}
			}
			// 文件不存在（新建）时跳过守卫，允许写入
		}

		var beforeContent *string
		var diffPreview string
		var filePerm os.FileMode = 0644
		if info, statErr := os.Stat(input.Path); statErr == nil {
			filePerm = info.Mode().Perm()
		}
		{
			oldData, oldErr := os.ReadFile(input.Path)
			if oldErr == nil {
				oldStr := string(oldData)
				beforeContent = &oldStr
				change := diff.FileChange{Path: input.Path, OldContent: oldStr, NewContent: input.Content}
				rendered := diff.Render(change, diff.PlainConfig())
				added, removed := diff.CountChanges(change)
				diffPreview = fmt.Sprintf("\nDiff: +%d/-%d lines\n%s", added, removed, rendered)
			} else {
				change := diff.FileChange{Path: input.Path, OldContent: "", NewContent: input.Content, IsNew: true}
				rendered := diff.Render(change, diff.PlainConfig())
				added, _ := diff.CountChanges(change)
				diffPreview = fmt.Sprintf("\nNew file: +%d lines\n%s", added, rendered)
			}
		}

		// 双写期：在写文件前调 FileHistory.Track 记录变更前状态（与 pushFileChange 中的 TrackAfter 配对）
		e.trackBefore(ctx, "write_file", input.Path)

		// 写入磁盘前检测密钥泄露（D7-2，REF-OWASP-LLM02 + REF-）
		if leaked, hit := detectSecretLeakForWrite(input.Content); hit {
			// ★ ：双语 SSOT 形态（util.BiSame）—— 修前中文为主；
			// 英文主行保留 "SECRET_LEAK_BLOCKED"（file_test.go:668 钉该子串）。
			return "", fmt.Errorf("%s", util.BiSame(
				"⚠️ [SECRET_LEAK_BLOCKED] Secret pattern detected; write blocked:\n%s\nCheck whether the AI output contains sensitive data (API key/Token/Private Key)",
				"⚠️ [SECRET_LEAK_BLOCKED] 检测到密钥模式，已阻断写入：\n%s\n请检查 AI 输出是否含敏感数据（API key/Token/Private Key）",
				leaked))
		}

		if err := util.AtomicWriteFile(input.Path, []byte(input.Content), filePerm); err != nil {
			return "", fmt.Errorf("write file: %w", err)
		}

		// 成功写入后基准随写更新（否则连续 write / read→write→write 会被误拒）
		if absErr == nil {
			e.recordWriteBase(writeAbsPath, []byte(input.Content))
		}

		// invalidate file cache after write
		if e.FileCache != nil {
			e.FileCache.Invalidate(input.Path)
		}

		// B4-3A 新增：写文件后通知 LSP 内容变更（失败不阻塞写文件主流程）
		e.notifyLSPDidChange(ctx, input.Path, input.Content)

		e.pushFileChange("write_file", input.Path, beforeContent, &input.Content, ToolCallIDFromCtx(ctx))

		// W-046a：行数走唯一语义源 util.CountLines（编辑器语义）——
		// 旧 strings.Count(content,"\n")+1 在末尾带换行时比同轮 read_file 多报 1 行。
		lineCount := util.CountLines(input.Content)
		result := fmt.Sprintf("File written: %s (%d lines)", input.Path, lineCount)
		if diffPreview != "" {
			maxDiffLen := 2000
			if len(diffPreview) > maxDiffLen {
				diffPreview = diffPreview[:maxDiffLen] + "\n... (diff truncated)"
			}
			result += diffPreview
		}
		return result, nil
	}
}

// trackBefore 在工具执行前调用 FileHistory.Track 记录文件变更前状态（双写）。
// toolCallId 从 ctx 获取（由 Execute 入口注入 call.ID），msgId 从 Executor.currentMsgId 获取。
// FileHistory 为 nil（降级）或失败时静默跳过（非致命，不影响旧 UndoStack 路径）。
func (e *Executor) trackBefore(ctx context.Context, toolName, path string) {
	if e.FileHistory == nil {
		return
	}
	toolCallId := ToolCallIDFromCtx(ctx)
	msgId := e.CurrentMessageId()
	if err := e.FileHistory.Track(msgId, toolCallId, toolName, []string{path}); err != nil {
		log.Printf("[executor] filehistory track-before failed (non-fatal): %v", err)
	}
}

// pushFileChange pushes a file change into the undo stack and records to FileHistory (双写)
// toolCallId 来自 tools.Call.ID，由调用方通过 ToolCallIDFromCtx(ctx) 获取并传入，
// pushFileChange 记录文件变更到 FileHistory（阶段 5：旧 UndoStack 双写已删除）。
// toolCallId 用于 FileHistory.TrackAfter 关联当前工具调用。
func (e *Executor) pushFileChange(toolName, filePath string, before, after *string, toolCallId string) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		absPath = filePath
	}

	// FileHistory 路径：TrackAfter 记录文件变更后状态，与 trackBefore 调用的 Track 配对。
	// 失败非致命（日志吞掉），不影响主流程。
	if e.FileHistory != nil {
		msgId := e.CurrentMessageId()
		if err := e.FileHistory.TrackAfter(msgId, toolCallId, []string{absPath}); err != nil {
			log.Printf("[executor] filehistory track-after failed (non-fatal): %v", err)
		}
	}
}

// editFileHandler edits a file (Search/Replace mode)
func (e *Executor) editFileHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input EditFileInput
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if input.Path == "" {
			return "", fmt.Errorf("file path cannot be empty")
		}
		if input.OldText == "" {
			return "", fmt.Errorf("search text cannot be empty")
		}
		if input.OldText == input.NewText {
			return "", fmt.Errorf("search text and replacement text are identical")
		}

		// T8: 路径遍历 + UNC 拦截
		if err := validateFilePath(input.Path); err != nil {
			return "", err
		}

		// C14: 文件操作工具调 sandbox.ValidatePath（Manager 优先，回退 legacy）
		// 注：writable-root 收敛（F2）已移除——生产管线 CustomValidateStage
		//（pipeline.go:487-501）在写类工具越出 workDir 时前置拒绝，F2 在管线内不可达
		// （死代码）。此处保留 C14 沙箱路径验证作为 handler 底线（无管线/未接线兜底）。
		if err := e.validatePathSandbox(ctx, input.Path); err != nil {
			// S11 G1-B：同 write_file，软拒绝 + 结构化标记。
			return fmt.Sprintf("Sandbox denied: %s", err.Error()), errSandboxDeniedSoft
		}

		// T6: mtime 守卫 — 编辑前比对 ExpectedMtime
		if input.ExpectedMtime != nil {
			if info, stErr := os.Stat(input.Path); stErr == nil {
				if !info.ModTime().Equal(*input.ExpectedMtime) {
					return "", fmt.Errorf("file modified externally: expected mtime %v, got %v",
						input.ExpectedMtime, info.ModTime())
				}
			}
		}

		data, err := os.ReadFile(input.Path)
		if err != nil {
			return "", fmt.Errorf("failed to read file: %w", err)
		}

		content := string(data)
		beforeContent := &content

		normalizedContent := content
		normalizedOldText := input.OldText
		normalizedNewText := input.NewText

		// Detect the dominant line ending style in the original file
		// by counting LF-only vs CRLF line endings
		crlfCount := strings.Count(content, "\r\n")
		lfOnlyCount := strings.Count(content, "\n") - crlfCount
		hasCRLF := crlfCount > lfOnlyCount
		oldTextHasCRLF := strings.Contains(input.OldText, "\r\n")
		newTextHasCRLF := strings.Contains(input.NewText, "\r\n")

		// Normalize all to LF for consistent matching if any CRLF is present
		if hasCRLF || oldTextHasCRLF || newTextHasCRLF {
			normalizedContent = strings.ReplaceAll(content, "\r\n", "\n")
			normalizedOldText = strings.ReplaceAll(input.OldText, "\r\n", "\n")
			normalizedNewText = strings.ReplaceAll(input.NewText, "\r\n", "\n")
		}

		count := strings.Count(normalizedContent, normalizedOldText)
		if count == 0 {
			return "", fmt.Errorf("no matching text found")
		}
		if count > 1 {
			return "", fmt.Errorf("%s", util.Bi(
				fmt.Sprintf("found %d matches; provide a more specific search text", count),
				fmt.Sprintf("找到 %d 处匹配，请提供更具体的搜索文本", count),
			))
		}

		// G4（S11）：在原始 content 上做单次替换，未触及部分逐字节保留；
		// 仅被替换文本按文件主导行尾适配。旧实现把整个文件 \n→\r\n 重写，
		// 会把混合行尾文件中原本 LF 的行也改成 CRLF。
		eol := "\n"
		if hasCRLF {
			eol = "\r\n"
		}
		finalContent := replacePreservingEOL(content, normalizedContent, normalizedOldText, normalizedNewText, eol)

		var diffPreview string
		var editFilePerm os.FileMode = 0644
		if info, statErr := os.Stat(input.Path); statErr == nil {
			editFilePerm = info.Mode().Perm()
		}
		// 同 write_file：变更预览是数据层产出，走 PlainConfig（无 ANSI）。
		change := diff.FileChange{Path: input.Path, OldContent: content, NewContent: finalContent}
		rendered := diff.Render(change, diff.PlainConfig())
		added, removed := diff.CountChanges(change)
		diffPreview = fmt.Sprintf("\nDiff: +%d/-%d lines\n%s", added, removed, rendered)

		// 双写期：在写文件前调 FileHistory.Track 记录变更前状态（与 pushFileChange 中的 TrackAfter 配对）
		e.trackBefore(ctx, "edit_file", input.Path)

		// 写入磁盘前检测密钥泄露（D7-2，REF-OWASP-LLM02 + REF-）
		if leaked, hit := detectSecretLeakForWrite(finalContent); hit {
			// ★ ：双语 SSOT 形态（util.BiSame）—— 修前中文为主；
			// 英文主行保留 "SECRET_LEAK_BLOCKED"（file_test.go:668 钉该子串）。
			return "", fmt.Errorf("%s", util.BiSame(
				"⚠️ [SECRET_LEAK_BLOCKED] Secret pattern detected; write blocked:\n%s\nCheck whether the AI output contains sensitive data (API key/Token/Private Key)",
				"⚠️ [SECRET_LEAK_BLOCKED] 检测到密钥模式，已阻断写入：\n%s\n请检查 AI 输出是否含敏感数据（API key/Token/Private Key）",
				leaked))
		}

		// G2（S11）：复用与 write_file 相同的原子写（temp+fsync+rename），
		// 统一原子性纪律，并避免固定 "*.whale-tmp" 命名的并发冲突/残留。
		if err := util.AtomicWriteFile(input.Path, []byte(finalContent), editFilePerm); err != nil {
			return "", fmt.Errorf("write file: %w", err)
		}

		// edit 成功后读基准随写更新（edit 本身以盘上现值为基准、天然安全，
		// 但若不更新基准，紧随其后的 write_file 会拿旧基准误判 stale ⇒ 误伤 edit→write 连招）
		if editAbs, editAbsErr := filepath.Abs(input.Path); editAbsErr == nil {
			e.recordWriteBase(editAbs, []byte(finalContent))
		}

		// invalidate file cache after edit
		if e.FileCache != nil {
			e.FileCache.Invalidate(input.Path)
		}

		// B4-3A 新增：编辑文件后通知 LSP 内容变更（失败不阻塞写文件主流程）
		e.notifyLSPDidChange(ctx, input.Path, finalContent)

		e.pushFileChange("edit_file", input.Path, beforeContent, &finalContent, ToolCallIDFromCtx(ctx))

		result := fmt.Sprintf("File edited: %s (1 replacement)", input.Path)
		if diffPreview != "" {
			maxDiffLen := 2000
			if len(diffPreview) > maxDiffLen {
				diffPreview = diffPreview[:maxDiffLen] + "\n... (diff truncated)"
			}
			result += diffPreview
		}
		return result, nil
	}
}

// validateFilePath 校验文件路径安全性（T8）：UNC 拦截 + 路径遍历组件级检测 + 根目录拦截。
//
// 路径遍历采用"组件级"检测（对标同类 CLI）：仅当 `..` 作为独立路径组件时才告警，
// 避免误伤 `file..txt` 这类合法文件名（旧实现用 strings.Contains 会误报）。
func validateFilePath(path string) error {
	// T8: UNC 路径拦截（Windows 网络资源）
	if isUNCPath(path) {
		return fmt.Errorf("UNC path not allowed: %s", path)
	}

	// T8: 组件级路径遍历检测（按 / 与 \ 拆分，任一组件为 ".." 即拒绝）
	if hasTraversalComponent(path) {
		return fmt.Errorf("file path contains invalid traversal pattern: %s", path)
	}

	cleaned := filepath.Clean(path)
	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		return fmt.Errorf("file path is invalid: %s: %w", path, err)
	}
	// Reject paths that resolve to root or system directories
	if absPath == "/" || absPath == "\\" || absPath == "C:\\" || absPath == "C:" {
		return fmt.Errorf("file path resolves to root directory: %s", path)
	}
	return nil
}

// isUNCPath 检测 Windows UNC 路径（T8）。
// 命中 `\\` 或 `//` 前缀即视为 UNC，拒绝访问网络资源。
func isUNCPath(path string) bool {
	return strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, `//`)
}

// hasTraversalComponent 组件级路径遍历检测（T8）。
// 按 / 与 \ 拆分路径，若任一组件恰为 ".." 则返回 true。
// 注意：不用 strings.Contains(path, "..")，避免误伤 file..txt 等合法名。
func hasTraversalComponent(path string) bool {
	// 同时按正反斜杠拆分
	s := strings.NewReplacer("/", "\\", "\\", "\\").Replace(path)
	for _, comp := range strings.Split(s, "\\") {
		if comp == ".." {
			return true
		}
	}
	return false
}

// replacePreservingEOL 在原始 content 上做单次字符串替换，未触及部分逐字节保留。
//
// - normalizedContent 是 content 经 "\r\n" → "\n" 归一化后的结果（归一化仅删除 \r）。
// - oldText/newText 均为 LF 形态（已归一化）；调用方须确保 oldText 在 normalizedContent
// 中唯一出现。
// - eol 为被替换文本应使用的行尾（文件主导行尾）；未触及部分不受影响。
//
// 背景（S11 G4）：旧实现把整个文件 \n→\r\n 重写，导致混合行尾文件中原本 LF 的行
// 被改成 CRLF。此处改为在原文上定位命中区间并仅替换该区间，保留其余字节原样。
func replacePreservingEOL(content, normalizedContent, oldText, newText, eol string) string {
	idx := strings.Index(normalizedContent, oldText)
	if idx < 0 {
		// 调用方已确认唯一命中，此处仅防御性兜底（不应到达）。
		return normalizedContent
	}

	// 建立 normalized 索引 → content 索引 映射（归一化每删除一个 \r，索引左移一位）。
	// 末尾追加哨兵 len(content)，使 end 可取到 len(normalizedContent)。
	origOf := make([]int, 0, len(normalizedContent)+1)
	for oi := 0; oi < len(content); {
		if content[oi] == '\r' && oi+1 < len(content) && content[oi+1] == '\n' {
			origOf = append(origOf, oi)
			oi += 2 // 跳过 \r\n，对应 normalized 中的单个 \n
		} else {
			origOf = append(origOf, oi)
			oi++
		}
	}
	origOf = append(origOf, len(content))

	start := origOf[idx]
	end := origOf[idx+len(oldText)]

	adapted := newText
	if eol != "\n" {
		adapted = strings.ReplaceAll(newText, "\n", eol)
	}
	return content[:start] + adapted + content[end:]
}

// validatePathSandbox 是 C14 的统一入口：文件操作工具（Read/Write/Edit/Glob/Grep）
// 调用 sandbox.ValidatePath 做应用层路径验证。
//
// 优先使用 Manager（C1 权威接口，ValidatePath 返回 error）；
// Manager 不可用时回退到 legacy Sandbox.ValidatePath（返回 bool+reason）；
// 两者都不可用时放行（无沙箱配置）。
//
// 返回 nil 表示路径在允许范围内。
func (e *Executor) validatePathSandbox(ctx context.Context, path string) error {
	// 路径 1: Manager 模式（C1/C14 权威）
	if e.SandboxMgr != nil {
		if e.SandboxMgr.Level() == sandbox.SandboxLevelDisabled {
			return nil
		}
		return e.SandboxMgr.ValidatePath(ctx, path, nil)
	}
	// 路径 2: Legacy Sandbox 模式（向后兼容）
	if e.Sandbox != nil && e.Sandbox.Enabled() {
		if allowed, reason := e.Sandbox.ValidatePath(path); !allowed {
			return fmt.Errorf("%s", reason)
		}
	}
	// 路径 3: 无沙箱配置，放行
	return nil
}
