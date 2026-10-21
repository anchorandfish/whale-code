package tui

// 「文案 ↔ 命令存在性」一致性机测。
//
// 背景面：提示文案里的斜杠命令标识符是「第二份实现」，与命令分发各写各的，漂移后
// 不报错、只会静默地对用户说谎 —— 用户照文案执行必然 Unknown。本轮实测抓到的真违规：
// ① internal/skill/trust.go（两处）：让用户 run `/skill trust <name>` / `/skill trust all`
// —— 但 /skill 从未注册（只有 /skills）；
// ② internal/tui/blocked_handoff.go:59、commands_session.go:350：指引 `/new`
// —— 注册表无 "new"、本地表无 "new"、plain 分发无 case "new"；
// ③ internal/tui/commands_misc.go:535：帮助文案仍列 `/effort low|med|high`
// —— /effort 已整体移除（effort_removed_test.go 为证）。
//
// 门的设计（production-path-tdd R4-8b「命中即红」型读盘门）：
// - 运行期用 go/parser 扫生产源码（非 _test）字符串字面量，提取 `/xxx [yyy]` 引用；
// - 命令存在性 SSOT = **注册表 ∪ TUI 本地表**（本包可直接调 isTUICommandHandledLocally
// —— 这是把门放在 tui 包而不是 command 包的原因）：
// 解析成功 = 注册表精确名 / 注册表「head *」前缀族（/daemon <start|…> 占位形态）/
// 本地表命中（含全名），任一成立即可；
// - 扫描退化自检：引用总数 < 阈值 ⇒ 扫描器坏了，本门结果不可信（防静默零扫描假绿）。
//
// 判别性自证（R4-8b 正向注入反例，通道 B′ 私有探针）：
// 注入含 "/zz-fake-probe-cmd" 的私有探针文件 ⇒ 违规数恰 +1；rm 后恢复。本轮已实测。
//
// 豁免维护纪律：denylist 每项必须注明触发字符串；不许为消红而豁免真命令名。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/command"
)

// 文件系统/协议常见词豁免表（触发字符串注明在行尾注释）。
var refGateFsRootDenylist = map[string]bool{
	"tmp": true, "usr": true, "var": true, "etc": true, "dev": true,
	"bin": true, "sbin": true, "opt": true, "home": true, "users": true,
	"private": true, "library": true, "system": true, "applications": true,
	"proc": true, "volumes": true, "null": true, "path": true,
	"boot": true, "sys": true, "root": true, // 安全检测里的系统路径字面量（bash.go/path_extractors.go）
	"lib": true, "lib64": true, // bash.go:643 动态库路径
	"lib32": true, "libx32": true, "run": true, // os_sandbox_linux_landlock.go:157-158 landlock 路径
	"environ": true,                // security_checks.go /proc/*/environ 模式
	"log":     true,                // compressor_profile.go `.*/log` 正则
	"metrics": true,                // plain.go daemon status 的 http://host/metrics 端点
	"script":  true, "style": true, // web.go HTML 清洗 "</script>"（已被 '<' 前置排除，双保险）
	"v1": true, "messages": true, // provider.go API 端点路径片段（"/v1" + "/messages"）
	"health": true, "events": true, // daemon/metrics/mcp 的 HTTP 端点（/health、/events）
	"format": true, // process_windows.go Windows 命令行 /format:value
}

// 引用形态：边界字符 + "/" + head（小写开头，可含连字符）。
// 前置边界排除 [A-Za-z0-9:._~/<–-]：拦 URL scheme、双斜杠、模块路径、HTML 闭合标签（</script>）。
var refGateHeadRe = regexp.MustCompile(
	`(?:^|[^A-Za-z0-9:._~/<-])/([a-z][a-z0-9]*(?:-[a-z0-9]+)*)`)

var refGateSubRe = regexp.MustCompile(`^ ([a-z][a-z0-9-]*)`)

type refGateViolation struct {
	file string
	line int
	ref  string
	lit  string
}

// refGateResolve：SSOT = 注册表 ∪ TUI 本地表。
func refGateResolve(head, sub string) bool {
	candidates := []string{head}
	if sub != "" {
		candidates = append([]string{head + " " + sub}, candidates...)
	}
	for _, c := range candidates {
		if _, ok := registryForRefGate.Get(c); ok {
			return true
		}
		if isTUICommandHandledLocally(c) {
			return true
		}
	}
	// 占位形态兜底："/daemon <start|stop|…>"、"/worktree [list|add…]" ——
	// head 本身未注册，但注册表存在 "head xxx" 家族 ⇒ 视为存在。
	for _, name := range registryForRefGate.List() {
		if strings.HasPrefix(name, head+" ") {
			return true
		}
	}
	return false
}

var registryForRefGate = command.NewRegistry()

func refGateSkipDir(name string) bool {
	switch name {
	case ".git", "vendor", "third_party", "node_modules", "trae_record",
		"_smoke", "p2", "fakemodel", "testing", "docs", "handoff", "skills",
		"scripts", "vscode-extension", "testdata":
		return true
	}
	return strings.HasPrefix(name, "ab_")
}

func TestSlashCommandReferencesInMessagesResolve(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}

	var violations []refGateViolation
	refCount := 0
	fileCount := 0

	scanFile := func(path string) {
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Logf("[ref-gate] skip unparseable %s: %v", path, perr)
			return
		}
		fileCount++
		ast.Inspect(f, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			lit := bl.Value
			for _, loc := range refGateHeadRe.FindAllStringSubmatchIndex(lit, -1) {
				head := lit[loc[2]:loc[3]]
				// 单字母 head 排除：仓库不存在单字母命令，"/c" "/f" "/s" "/q"
				// 全是 Windows 命令行 flag（del /f /s /q）或路径片段。
				if len(head) < 2 {
					continue
				}
				if refGateFsRootDenylist[head] {
					continue
				}
				after := lit[loc[3]:]
				if len(after) > 0 {
					c := after[0]
					if c == '/' || c == '.' || c == '_' ||
						(c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
						continue
					}
				}
				sub := ""
				if m := refGateSubRe.FindStringSubmatch(after); m != nil {
					sub = m[1]
				}
				refCount++
				if refGateResolve(head, sub) {
					continue
				}
				violations = append(violations, refGateViolation{
					file: path,
					line: fset.Position(bl.Pos()).Line,
					ref:  strings.TrimSpace("/" + head + " " + sub),
					lit:  refGateTruncate(lit, 140),
				})
			}
			return true
		})
	}

	// 只扫用户可见面：internal/ + cmd/ + 根 main.go（文案全部产自这些包）
	for _, dir := range []string{filepath.Join(root, "internal"), filepath.Join(root, "cmd")} {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, werr error) error {
			if werr != nil {
				return nil
			}
			if d.IsDir() {
				if p != dir && refGateSkipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			scanFile(p)
			return nil
		})
	}
	scanFile(filepath.Join(root, "main.go"))

	// 扫描退化自检：真实仓命令引用远多于阈值；扫描器坏了必须停（不许静默假绿）。
	if refCount < 50 {
		t.Fatalf("扫描退化自检失败：仅提取到 %d 条命令引用（fileCount=%d）——本门结果不可信",
			refCount, fileCount)
	}
	t.Logf("[ref-gate] scanned %d files, %d command references", fileCount, refCount)

	if len(violations) > 0 {
		var b strings.Builder
		b.WriteString("文案引用了不存在的命令（「文案↔命令存在性」一致性被破坏）：\n")
		for _, v := range violations {
			b.WriteString("  " + v.file + ":" + strconv.Itoa(v.line) +
				"  ref=" + v.ref + "  lit=" + v.lit + "\n")
		}
		b.WriteString("修复方向：改文案指向真实入口，或注册缺失的命令。二者择一，不许改本门豁免消红。")
		t.Fatal(b.String())
	}
}

func refGateTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
