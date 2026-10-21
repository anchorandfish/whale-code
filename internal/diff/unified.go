package diff

// unified.go — unified diff 的**解析**与**应用**（G5，2026-09-14）。
//
// 背景：本包此前只有「渲染」侧（Render / ComputeLineDiffs / groupHunks），
// **没有任何 Apply / ApplyPatch 能力** —— whale-code 能把改动渲染成 unified diff
// 给用户看，却无法把一段 unified diff 写回文件。本文件补上后半条链路。
//
// 设计取舍：
//   - **复用同一 hunk 模型**：`Hunk`（render.go 原 unexported `hunk` 提升而来）与
//     `LineDiff` 同时服务渲染与解析，不再另造一套同形结构（两套必然漂移）。
//   - **解析与应用分层**：`ParseUnifiedDiff` 只做语法 → 模型；`ApplyFilePatch` 只做
//     模型 → 内容。两者都不碰磁盘 —— 落盘、沙箱、工作区边界分别是 tools 包与管线的职责。
//   - **整文件原子**：任一 hunk 冲突即整文件失败，不返回"部分应用"的内容。
//     （这是 ApplyFilePatch 的契约；跨文件的回滚策略由调用方决定。）
//   - **fuzz 有上界且不裁实质行**：fuzz 只能裁掉 hunk 首尾的**上下文行**；
//     一旦需要裁掉 +/- 行，说明匹配条件已放宽到不可信 —— 直接判该级别不可用。

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// devNull 是 unified diff 表示「文件不存在」的路径。
const devNull = "/dev/null"

// ErrPatchConflict 是补丁冲突的哨兵错误。所有 ConflictError 都 Unwrap 到它，
// 调用方用 errors.Is(err, diff.ErrPatchConflict) 判定「冲突」而无需解析文本。
var ErrPatchConflict = errors.New("patch conflict")

// hunkHeaderRe 匹配 unified diff 的 hunk 头。
// 兼容 git 的 `@@ -1,3 +1,4 @@ func foo()` 尾部节标题；
// 不匹配 combined diff 的 `@@@ ... @@@`（本实现明确不支持，见 ParseUnifiedDiff）。
var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// FilePatch 是一个文件在 unified diff 中的完整补丁。
type FilePatch struct {
	OldPath string // 去前缀后的旧路径（新建文件为空）
	NewPath string // 去前缀后的新路径（删除文件为空）
	Hunks   []Hunk

	IsNew     bool // 旧侧是 /dev/null
	IsDeleted bool // 新侧是 /dev/null

	// OldNoNewlineAtEOF / NewNoNewlineAtEOF 记录补丁末尾的
	// `\ No newline at end of file` 标记落在哪一侧。
	// 语义由**最后一个 hunk** 决定（marker 只在触及 EOF 的 hunk 里出现）。
	OldNoNewlineAtEOF bool
	NewNoNewlineAtEOF bool

	// RawOldHeader / RawNewHeader 保留 `--- ` / `+++ ` 原文，供回报与排错。
	RawOldHeader string
	RawNewHeader string
}

// TargetPath 返回补丁应当应用到的文件路径。
// 删除补丁的目标是被删文件，其余情况目标是新路径。
func (fp FilePatch) TargetPath() string {
	if fp.NewPath == "" {
		return fp.OldPath
	}
	return fp.NewPath
}

// ApplyOptions 控制 hunk 定位的宽松度。
type ApplyOptions struct {
	// MaxFuzz 允许裁掉的上下文行数（首尾各算）。0 = 严格匹配。
	// 语义对齐 GNU patch 的 --fuzz。
	MaxFuzz int
	// MaxLineDrift 允许 hunk 实际位置偏离 header 声明行号的最大行数。
	// 0 会被归一到 max(1, ...) —— 只按声明位置找一次在真实补丁里太脆。
	MaxLineDrift int
}

// DefaultApplyOptions 返回默认的宽松度：允许 2 行 fuzz、±200 行漂移。
// 上界刻意收窄：fuzz 越大越容易「匹配到错误的位置」，宁可报冲突也不改错文件。
func DefaultApplyOptions() ApplyOptions {
	return ApplyOptions{MaxFuzz: 2, MaxLineDrift: 200}
}

// ApplyStats 是一次应用的统计。
type ApplyStats struct {
	HunksApplied int
	HunksTotal   int
	MaxFuzz      int // 本次实际用到的最大 fuzz（0 = 全程精确匹配）
}

// ConflictError 描述一次 hunk 应用失败。它 Unwrap 到 ErrPatchConflict。
type ConflictError struct {
	Path      string
	HunkIndex int // 1-based
	Header    string
	Reason    string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("patch conflict: %s hunk #%d (%s): %s", e.Path, e.HunkIndex, e.Header, e.Reason)
}

// Unwrap 让 errors.Is(err, ErrPatchConflict) 成立。
func (e *ConflictError) Unwrap() error { return ErrPatchConflict }

// ParseError 描述补丁语法错误（与「冲突」区分：语法错 = 补丁本身不合法）。
type ParseError struct {
	Line   int // 1-based，0 表示不适用
	Reason string
}

func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("patch parse error at line %d: %s", e.Line, e.Reason)
	}
	return "patch parse error: " + e.Reason
}

// ---------------------------------------------------------------- 解析

// ParseUnifiedDiff 把 unified diff 文本解析为按文件分组的补丁列表。
//
// 支持：
//   - git 风格 `a/` `b/` 前缀与无前缀两种（`/dev/null` 表示新建/删除）；
//   - `diff --git` / `index` / `old mode` 等元信息行（透传忽略）；
//   - hunk 内容按 **header 声明的行数**消费 —— 不靠"下一条像什么"猜边界，
//     因此 hunk 之间的空行、文件之间的分隔行都不会被误吞；
//   - `\ No newline at end of file` 标记。
//
// 不支持（显式报错，不静默降级）：combined diff（`@@@`）、binary patch（`GIT binary patch`）。
func ParseUnifiedDiff(text string) ([]FilePatch, error) {
	if strings.TrimSpace(text) == "" {
		return nil, &ParseError{Reason: "补丁为空"}
	}
	lines := strings.Split(text, "\n")

	var (
		out []FilePatch
		cur *FilePatch
	)
	flush := func() {
		if cur == nil {
			return
		}
		if len(cur.Hunks) > 0 || cur.OldPath != "" || cur.NewPath != "" {
			cur.IsNew = cur.OldPath == ""
			cur.IsDeleted = cur.NewPath == ""
			out = append(out, *cur)
		}
		cur = nil
	}
	ensure := func() *FilePatch {
		if cur == nil {
			cur = &FilePatch{}
		}
		return cur
	}

	for i := 0; i < len(lines); i++ {
		structural := strings.TrimSuffix(lines[i], "\r")

		switch {
		case strings.HasPrefix(structural, "diff --git "):
			// 新文件边界。若上一个文件还没有 hunks（只有 --git 行），不产出一个空补丁。
			flush()
			cur = &FilePatch{}
			cur.OldPath, cur.NewPath = gitHeaderPaths(structural)
			continue

		case strings.HasPrefix(structural, "--- "):
			// 上一个文件已有 hunks ⇒ 这是新文件边界（无 `diff --git` 的裸补丁）。
			if cur != nil && len(cur.Hunks) > 0 {
				flush()
			}
			fp := ensure()
			fp.RawOldHeader = structural
			fp.OldPath = parseDiffPath(strings.TrimPrefix(structural, "--- "))
			continue

		case strings.HasPrefix(structural, "+++ "):
			fp := ensure()
			fp.RawNewHeader = structural
			fp.NewPath = parseDiffPath(strings.TrimPrefix(structural, "+++ "))
			continue

		case strings.HasPrefix(structural, "GIT binary patch"):
			return nil, &ParseError{Line: i + 1, Reason: "不支持 binary patch（GIT binary patch）"}

		case strings.HasPrefix(structural, "@@@"):
			return nil, &ParseError{Line: i + 1, Reason: "不支持 combined diff（@@@）"}
		}

		m := hunkHeaderRe.FindStringSubmatch(structural)
		if m == nil {
			// 其余元信息行（index / old mode / new file mode / rename from…）与空行：忽略。
			continue
		}

		hk, err := parseHunkHeader(m, structural)
		if err != nil {
			return nil, &ParseError{Line: i + 1, Reason: err.Error()}
		}

		next, oldNoNL, newNoNL, err := consumeHunk(lines, i+1, &hk)
		if err != nil {
			return nil, &ParseError{Line: i + 1, Reason: err.Error()}
		}
		fp := ensure()
		fp.Hunks = append(fp.Hunks, hk)
		// 由最后一个 hunk 决定 EOF 换行事实（marker 只在触及 EOF 的 hunk 出现）。
		fp.OldNoNewlineAtEOF = oldNoNL
		fp.NewNoNewlineAtEOF = newNoNL
		i = next - 1
	}

	flush()
	if len(out) == 0 {
		return nil, &ParseError{Reason: "补丁里没有任何文件（缺少 ---/+++ 头或 @@ hunk 头）"}
	}
	for _, fp := range out {
		if len(fp.Hunks) == 0 {
			return nil, &ParseError{Reason: fmt.Sprintf("文件 %q 没有任何 hunk", fp.TargetPath())}
		}
	}
	return out, nil
}

// parseHunkHeader 把正则子匹配转成 Hunk（仅头信息，不含行内容）。
func parseHunkHeader(m []string, raw string) (Hunk, error) {
	atoi := func(s string) (int, error) {
		if s == "" {
			return 0, nil
		}
		return strconv.Atoi(s)
	}
	oldStart, err := atoi(m[1])
	if err != nil {
		return Hunk{}, fmt.Errorf("hunk 头行号非法: %s", raw)
	}
	oldCount, err := atoi(m[2])
	if err != nil {
		return Hunk{}, fmt.Errorf("hunk 头行数非法: %s", raw)
	}
	newStart, err := atoi(m[3])
	if err != nil {
		return Hunk{}, fmt.Errorf("hunk 头行号非法: %s", raw)
	}
	newCount, err := atoi(m[4])
	if err != nil {
		return Hunk{}, fmt.Errorf("hunk 头行数非法: %s", raw)
	}
	// `@@ -1 +1 @@`：省略的行数默认 1（POSIX 规定）。
	if m[2] == "" {
		oldCount = 1
	}
	if m[4] == "" {
		newCount = 1
	}
	return Hunk{OldStart: oldStart, OldCount: oldCount, NewStart: newStart, NewCount: newCount}, nil
}

// consumeHunk 从 start 起按 header 声明的行数消费 hunk 内容。
// 返回下一个未消费行的下标（可与 len(lines) 相等）、两侧的 no-newline 标记。
func consumeHunk(lines []string, start int, hk *Hunk) (next int, oldNoNL, newNoNL bool, err error) {
	oldLeft, newLeft := hk.OldCount, hk.NewCount
	i := start
	lastPrefix := byte(0)

	for i < len(lines) {
		raw := lines[i]
		if oldLeft == 0 && newLeft == 0 {
			// 计数已满。仅允许再吞一个 EOF 换行标记 —— 它不计入两侧行数，
			// 因此可能在"最后一个内容行刚好吃满计数"之后才出现（git 的常见形态）。
			// 其余内容一律交还上层（hunk 之间的空行、下一个文件的 diff 头等）。
			if isNoNewlineMarker(raw) {
				oldNoNL, newNoNL = markNoNewline(lastPrefix, oldNoNL, newNoNL)
				i++
			}
			break
		}
		if raw == "" {
			return i, false, false, fmt.Errorf(
				"hunk (@@ -%d,%d +%d,%d @@) 内出现空行（行首空格可能已被剥离），剩余 old=%d new=%d",
				hk.OldStart, hk.OldCount, hk.NewStart, hk.NewCount, oldLeft, newLeft)
		}

		switch raw[0] {
		case ' ':
			if oldLeft == 0 || newLeft == 0 {
				return i, false, false, fmt.Errorf("上下文行超出 hunk 声明的行数")
			}
			hk.Lines = append(hk.Lines, LineDiff{Type: diffmatchpatch.DiffEqual, Text: raw[1:]})
			oldLeft--
			newLeft--
			lastPrefix = ' '
		case '-':
			if oldLeft == 0 {
				return i, false, false, fmt.Errorf("删除行超出 hunk 声明的旧文件行数")
			}
			hk.Lines = append(hk.Lines, LineDiff{Type: diffmatchpatch.DiffDelete, Text: raw[1:]})
			oldLeft--
			lastPrefix = '-'
		case '+':
			if newLeft == 0 {
				return i, false, false, fmt.Errorf("新增行超出 hunk 声明的新文件行数")
			}
			hk.Lines = append(hk.Lines, LineDiff{Type: diffmatchpatch.DiffInsert, Text: raw[1:]})
			newLeft--
			lastPrefix = '+'
		case '\\':
			if !isNoNewlineMarker(raw) {
				return i, false, false, fmt.Errorf("无法识别的 hunk 内标记行: %q", raw)
			}
			oldNoNL, newNoNL = markNoNewline(lastPrefix, oldNoNL, newNoNL)
			i++
			continue
		default:
			return i, false, false, fmt.Errorf("hunk 内出现非法前缀行: %q", raw)
		}
		i++
	}

	if oldLeft != 0 || newLeft != 0 {
		return i, false, false, fmt.Errorf(
			"hunk 被截断：声明 old=%d new=%d，实际读到 old=%d new=%d",
			hk.OldCount, hk.NewCount, hk.OldCount-oldLeft, hk.NewCount-newLeft)
	}
	return i, oldNoNL, newNoNL, nil
}

// isNoNewlineMarker 判断该行是否为 `\ No newline at end of file` 标记
// （容忍 CRLF 补丁残留在行尾的 \r）。
func isNoNewlineMarker(raw string) bool {
	return strings.HasPrefix(strings.TrimSuffix(raw, "\r"), `\ No newline at end of file`)
}

// markNoNewline 依据标记前面那个内容行的前缀，决定标记归属哪一侧：
//   - `+` 行后 → 只有新文件末尾无换行；
//   - `-` 行后 → 只有旧文件末尾无换行（即新文件补上了换行）；
//   - 上下文行后 → 两侧末尾都无换行。
func markNoNewline(lastPrefix byte, oldNoNL, newNoNL bool) (bool, bool) {
	switch lastPrefix {
	case '+':
		newNoNL = true
	case '-':
		oldNoNL = true
	default:
		oldNoNL, newNoNL = true, true
	}
	return oldNoNL, newNoNL
}

// parseDiffPath 解析 `--- ` / `+++ ` 后的路径字段。
// 去掉 git 的时间戳尾巴、引号包裹、以及 `a/` `b/` 单层前缀（等价 git apply -p1，
// 但只在该层恰好是 `a`/`b` 时剥离，避免误伤真实以 a/ 开头的路径）。
func parseDiffPath(field string) string {
	if idx := strings.IndexByte(field, '\t'); idx >= 0 {
		field = field[:idx]
	}
	field = strings.TrimSpace(field)
	if field == "" || field == devNull {
		return ""
	}
	if len(field) >= 2 && field[0] == '"' && field[len(field)-1] == '"' {
		if unquoted, err := strconv.Unquote(field); err == nil {
			field = unquoted
		}
	}
	field = strings.TrimPrefix(field, "./")
	if head, rest, ok := strings.Cut(field, "/"); ok && (head == "a" || head == "b") && rest != "" {
		field = rest
	}
	return field
}

// gitHeaderPaths 从 `diff --git a/x b/y` 提取两侧路径（仅作兜底，
// 后续 `---`/`+++` 头命中时会覆盖）。带空格的路径 git 会用引号包裹，此处按引号切分。
func gitHeaderPaths(line string) (oldPath, newPath string) {
	rest := strings.TrimPrefix(line, "diff --git ")
	left, right, ok := splitGitHeaderFields(rest)
	if !ok {
		return "", ""
	}
	return parseDiffPath(left), parseDiffPath(right)
}

// splitGitHeaderFields 切分 `a/x b/y`（路径含空格时被引号包裹）。
func splitGitHeaderFields(s string) (string, string, bool) {
	if strings.HasPrefix(s, `"`) {
		// 找到闭合引号（忽略转义）
		for i := 1; i < len(s); i++ {
			if s[i] == '"' && s[i-1] != '\\' {
				return s[:i+1], strings.TrimSpace(s[i+1:]), true
			}
		}
		return "", "", false
	}
	idx := strings.IndexByte(s, ' ')
	if idx < 0 {
		return "", "", false
	}
	return s[:idx], strings.TrimSpace(s[idx+1:]), true
}

// ScanPatchTargets 从 unified diff 文本中**宽松地**提取全部候选目标路径。
//
// 用途单一且明确：供管线在**执行前**做零副作用的越界预检（tools.CustomValidateStage）。
// 因此它只扫头部行、不做完整解析，且**宁多报不漏报**（`---` / `+++` 两侧都收）：
// 多报一个路径最多让一次本来就该拒的调用被拒；漏报则会让越界写穿到 handler。
// 完整解析与应用由 ParseUnifiedDiff / ApplyFilePatch 负责。
func ScanPatchTargets(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	// 去重的顺序保持首次出现顺序（便于稳定报错文案与测试）。
	push := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		switch {
		case strings.HasPrefix(line, "--- "):
			push(parseDiffPath(strings.TrimPrefix(line, "--- ")))
		case strings.HasPrefix(line, "+++ "):
			push(parseDiffPath(strings.TrimPrefix(line, "+++ ")))
		case strings.HasPrefix(line, "diff --git "):
			old, new := gitHeaderPaths(line)
			push(old)
			push(new)
		}
	}
	return out
}

// ---------------------------------------------------------------- 应用

// ApplyFilePatch 把 fp 的 hunks 应用到 oldContent 上，返回新内容与统计。
//
// 契约：
//   - **整文件原子**：任一 hunk 定位失败即返回 ConflictError，且返回值不包含
//     "部分应用"的内容（调用方不会误写半个文件）。
//   - **逐字节**：输出只由 `oldContent` 的行集合 + 补丁的 +/- 行决定；
//     EOF 换行的有无由最后一个 hunk 的 `\ No newline` 标记决定（无标记则保留原状）。
//   - 不做任何磁盘/沙箱/权限判断 —— 那是调用方的职责。
func ApplyFilePatch(oldContent string, fp FilePatch, opts ApplyOptions) (string, ApplyStats, error) {
	if opts.MaxFuzz < 0 {
		opts.MaxFuzz = 0
	}
	if opts.MaxLineDrift <= 0 {
		opts.MaxLineDrift = 200
	}

	hadTrailingNL := strings.HasSuffix(oldContent, "\n")
	lines := splitLines(oldContent)
	stats := ApplyStats{HunksTotal: len(fp.Hunks)}

	delta := 0
	for hi, hk := range fp.Hunks {
		want := hk.OldStart - 1 + delta
		if hk.OldCount == 0 {
			// 纯插入 hunk（`@@ -0,0 +1,N @@` 或 `@@ -5,0 +6,1 @@`）：
			// OldStart 是"插入点前一行"，故插入位置就是 OldStart。
			want = hk.OldStart + delta
		}
		pos, fuzz, ok := locateHunk(lines, hk, want, opts)
		if !ok {
			return "", stats, &ConflictError{
				Path:      fp.TargetPath(),
				HunkIndex: hi + 1,
				Header:    HunkHeaderString(hk),
				Reason: fmt.Sprintf("上下文未在 ±%d 行内匹配（fuzz 上限 %d）",
					opts.MaxLineDrift, opts.MaxFuzz),
			}
		}
		oldSeg, newSeg, segOK := fuzzSegments(hk.Lines, fuzz)
		if !segOK {
			// locateHunk 只在 segOK 时返回成功，这里属防御性分支。
			return "", stats, &ConflictError{
				Path:      fp.TargetPath(),
				HunkIndex: hi + 1,
				Header:    HunkHeaderString(hk),
				Reason:    "内部错误：定位成功但无法取回 hunk 片段",
			}
		}
		if pos < 0 || pos+len(oldSeg) > len(lines) {
			return "", stats, &ConflictError{
				Path:      fp.TargetPath(),
				HunkIndex: hi + 1,
				Header:    HunkHeaderString(hk),
				Reason:    "定位结果越出文件范围",
			}
		}
		merged := make([]string, 0, len(lines)-len(oldSeg)+len(newSeg))
		merged = append(merged, lines[:pos]...)
		merged = append(merged, newSeg...)
		merged = append(merged, lines[pos+len(oldSeg):]...)
		lines = merged
		delta += len(newSeg) - len(oldSeg)
		if fuzz > stats.MaxFuzz {
			stats.MaxFuzz = fuzz
		}
		stats.HunksApplied++
	}

	if len(lines) == 0 {
		return "", stats, nil
	}

	trailing := hadTrailingNL || fp.IsNew
	switch {
	case fp.NewNoNewlineAtEOF:
		trailing = false
	case fp.OldNoNewlineAtEOF:
		trailing = true
	}

	out := strings.Join(lines, "\n")
	if trailing {
		out += "\n"
	}
	return out, stats, nil
}

// locateHunk 在 lines 中为 hunk 定位，返回起始下标与实际用到的 fuzz 级别。
// 搜索顺序：fuzz 0→MaxFuzz，每个 fuzz 内按距离 want 由近及远 ——
// 「最小放宽 + 最近位置」优先，避免用大 fuzz 匹配到远处错误位置。
func locateHunk(lines []string, hk Hunk, want int, opts ApplyOptions) (int, int, bool) {
	for f := 0; f <= opts.MaxFuzz; f++ {
		oldSeg, _, ok := fuzzSegments(hk.Lines, f)
		if !ok {
			return 0, 0, false
		}
		if pos, found := searchSegment(lines, oldSeg, want, opts.MaxLineDrift); found {
			return pos, f, true
		}
	}
	return 0, 0, false
}

// searchSegment 在 [want-drift, want+drift] 内按「离 want 由近及远」寻找 seg。
// seg 为空（纯插入 hunk）时返回夹紧后的 want。
func searchSegment(lines, seg []string, want, drift int) (int, bool) {
	maxPos := len(lines) - len(seg)
	if maxPos < 0 {
		return 0, false
	}
	if want < 0 {
		want = 0
	}
	if want > maxPos {
		want = maxPos
	}
	if len(seg) == 0 {
		return want, true
	}
	lo := want - drift
	if lo < 0 {
		lo = 0
	}
	hi := want + drift
	if hi > maxPos {
		hi = maxPos
	}
	for d := 0; d <= drift; d++ {
		for _, p := range [2]int{want - d, want + d} {
			if p < lo || p > hi {
				continue
			}
			if matchAt(lines, seg, p) {
				return p, true
			}
		}
	}
	return 0, false
}

// matchAt 判断 seg 是否逐字节等于 lines[pos:pos+len(seg)]。
func matchAt(lines, seg []string, pos int) bool {
	if pos < 0 || pos+len(seg) > len(lines) {
		return false
	}
	for i, s := range seg {
		if lines[pos+i] != s {
			return false
		}
	}
	return true
}

// fuzzSegments 按 fuzz 级别裁掉 hunk 首尾的上下文行，返回（旧侧行, 新侧行）。
//
// 只允许裁掉**上下文行**：`+`/`-` 行是 hunk 的实质内容，需要裁掉它们才能匹配
// 说明已达「不可信匹配」，此时返回 ok=false 让调用方停止放宽。
func fuzzSegments(hkLines []LineDiff, fuzz int) (oldSeg, newSeg []string, ok bool) {
	if fuzz < 0 || fuzz*2 > len(hkLines) {
		return nil, nil, false
	}
	for i := 0; i < fuzz; i++ {
		if hkLines[i].Type != diffmatchpatch.DiffEqual ||
			hkLines[len(hkLines)-1-i].Type != diffmatchpatch.DiffEqual {
			return nil, nil, false
		}
	}
	for _, ld := range hkLines[fuzz : len(hkLines)-fuzz] {
		switch ld.Type {
		case diffmatchpatch.DiffEqual:
			oldSeg = append(oldSeg, ld.Text)
			newSeg = append(newSeg, ld.Text)
		case diffmatchpatch.DiffDelete:
			oldSeg = append(oldSeg, ld.Text)
		case diffmatchpatch.DiffInsert:
			newSeg = append(newSeg, ld.Text)
		}
	}
	return oldSeg, newSeg, true
}

// HunkHeaderString 还原 hunk 头的文本形式（用于冲突报文与回报）。
func HunkHeaderString(hk Hunk) string {
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@", hk.OldStart, hk.OldCount, hk.NewStart, hk.NewCount)
}
