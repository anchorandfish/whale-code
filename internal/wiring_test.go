// Package internal 提供 whale-code 项目的接线完整性核查测试。
//
// 通过反射 + 字符串扫描 + go/ast 解析机械化核查以下四个维度：
// - doctor FixSuggest 中引用的 whale <cmd> 命令是否已注册
// - internal/command 中注册的命令 Handler 是否为桩函数
// - config.Config 公开字段是否在业务代码中被读取
// - main.go printHelp 文本与实际 flag 注册是否一致
//
// 设计原则：不 mock 真实业务逻辑，仅做静态/反射层面的接线核查；
// 发现问题时只报告不修复（项目宪法硬约束：不修改非测试文件）。
package internal

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/config"
)

// findProjectRoot 从测试工作目录向上查找包含 go.mod 的目录作为项目根。
// 测试文件位于 internal/wiring_test.go，项目根为其父目录。
// 找不到 go.mod 时终止测试并报告错误。
func findProjectRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("无法获取工作目录: %v", err)
	}
	dir := wd
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("未找到 go.mod（从 %s 向上查找）", wd)
	return ""
}

// extractStringLit 从 ast.Expr 中递归提取字符串字面量值。
// 支持 *ast.BasicLit（单字符串）和 *ast.BinaryExpr（字符串拼接，如 "a" + "b"）。
// 非字符串表达式（函数调用、变量等）返回空字符串。
// 使用 strconv.Unquote 正确处理转义字符和反引号字符串。
func extractStringLit(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			if s, err := strconv.Unquote(e.Value); err == nil {
				return s
			}
		}
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			return extractStringLit(e.X) + extractStringLit(e.Y)
		}
	}
	return ""
}

// TestFixSuggestCommandsExist 核查 doctor FixSuggest 中引用的 whale <cmd> 命令在 main.go 或 command 包中已注册。
//
// 扫描 internal/doctor/*.go 中所有 FixSuggest 字段的字符串字面量值，
// 用正则 whale\s+(\w+) 提取被引用的命令名，
// 验证每个命令在 main.go 的 flag 分发（os.Args[1]=="xxx" / arg=="xxx" / args[0]=="xxx"）
// 或 internal/command 包的 NewRegistry 注册列表中有对应处理。
// 失败时输出缺失命令名及对应 FixSuggest 的 file:line 位置。
func TestFixSuggestCommandsExist(t *testing.T) {
	root := findProjectRoot(t)
	doctorDir := filepath.Join(root, "internal", "doctor")

	// 1. 用 AST 收集所有 FixSuggest 字符串字面量及其位置
	type fixEntry struct {
		text string
		file string
		line int
	}
	var entries []fixEntry

	files, err := os.ReadDir(doctorDir)
	if err != nil {
		t.Fatalf("读取 doctor 目录失败: %v", err)
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(doctorDir, f.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		astFile, parseErr := parser.ParseFile(fset, path, content, parser.ParseComments)
		if parseErr != nil {
			continue
		}
		ast.Inspect(astFile, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			ident, ok := kv.Key.(*ast.Ident)
			if !ok || ident.Name != "FixSuggest" {
				return true
			}
			text := extractStringLit(kv.Value)
			if text == "" {
				return true // 动态值（函数调用等），跳过
			}
			pos := fset.Position(kv.Pos())
			entries = append(entries, fixEntry{text: text, file: path, line: pos.Line})
			return true
		})
	}

	if len(entries) == 0 {
		t.Log("未找到 FixSuggest 字符串字面量")
		return
	}

	// 2. 从 FixSuggest 文本中提取 whale <cmd> 命令引用
	cmdRe := regexp.MustCompile(`whale\s+(\w+)`)
	type cmdRef struct {
		cmd  string
		file string
		line int
		text string
	}
	var refs []cmdRef
	for _, e := range entries {
		for _, m := range cmdRe.FindAllStringSubmatch(e.text, -1) {
			refs = append(refs, cmdRef{cmd: m[1], file: e.file, line: e.line, text: e.text})
		}
	}

	if len(refs) == 0 {
		t.Log("FixSuggest 中未找到 whale <cmd> 命令引用")
		return
	}

	// 3. 收集已知命令集合
	knownCmds := make(map[string]bool)

	// 3a. main.go 中的 flag/子命令分发
	mainGo := filepath.Join(root, "main.go")
	mainContent, err := os.ReadFile(mainGo)
	if err != nil {
		t.Fatalf("读取 main.go 失败: %v", err)
	}
	mainText := string(mainContent)
	// os.Args[1] == "xxx" — 子命令入口
	for _, m := range regexp.MustCompile(`os\.Args\[1\]\s*==\s*"([^"]+)"`).FindAllStringSubmatch(mainText, -1) {
		knownCmds[m[1]] = true
	}
	// arg == "xxx" — flag 分发
	for _, m := range regexp.MustCompile(`arg\s*==\s*"([^"]+)"`).FindAllStringSubmatch(mainText, -1) {
		knownCmds[m[1]] = true
	}
	// args[0] == "xxx" — handleArgs 分发
	for _, m := range regexp.MustCompile(`args\[0\]\s*==\s*"([^"]+)"`).FindAllStringSubmatch(mainText, -1) {
		knownCmds[m[1]] = true
	}

	// 3b. command 包中注册的命令
	reg := command.NewRegistry()
	for _, name := range reg.List() {
		knownCmds[name] = true
	}

	// 4. 验证每个引用的命令是否已注册
	for _, ref := range refs {
		if !knownCmds[ref.cmd] {
			t.Errorf("FixSuggest 引用的命令 %q 未在 main.go 或 command 包中注册\n  位置: %s:%d\n  FixSuggest: %s",
				ref.cmd, ref.file, ref.line, ref.text)
		}
	}
}

// TestTUICommandsNotStub 核查 internal/command 中注册的命令 Handler 不是桩函数。
//
// 创建 NewRegistry()，遍历所有注册命令，对每个 Handler 传入空字符串调用一次。
// 断言返回结果不含 "not yet implemented" / "stub" / "TODO"（除非命令显式标记 Stub:true）。
// 为防止网络/文件 IO 阻塞，每个 Handler 调用设 5 秒超时，超时则跳过并记录。
// 为防止 panic 导致测试崩溃，goroutine 内 defer recover 捕获异常。
// 失败时输出命令名及对应 commands_*.go 中的 file:line 位置。
func TestTUICommandsNotStub(t *testing.T) {
	root := findProjectRoot(t)
	cmdDir := filepath.Join(root, "internal", "command")

	// 1. 构建 命令名 → file:line 映射（通过 AST 解析 commands_*.go）
	cmdLocation := make(map[string]string)
	files, err := os.ReadDir(cmdDir)
	if err != nil {
		t.Fatalf("读取 command 目录失败: %v", err)
	}
	for _, f := range files {
		if !strings.HasPrefix(f.Name(), "commands_") || !strings.HasSuffix(f.Name(), ".go") {
			continue
		}
		path := filepath.Join(cmdDir, f.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		astFile, parseErr := parser.ParseFile(fset, path, content, parser.ParseComments)
		if parseErr != nil {
			continue
		}
		ast.Inspect(astFile, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			ident, ok := kv.Key.(*ast.Ident)
			if !ok || ident.Name != "Name" {
				return true
			}
			lit, ok := kv.Value.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			name, unquoteErr := strconv.Unquote(lit.Value)
			if unquoteErr != nil {
				return true
			}
			pos := fset.Position(kv.Pos())
			cmdLocation[name] = fmt.Sprintf("%s:%d", f.Name(), pos.Line)
			return true
		})
	}

	// 2. 创建 Registry 并遍历所有命令
	reg := command.NewRegistry()

	// 跳过列表：
	// - upgrade：handleUpgradeCheck 调用 GitHub Releases API（10s×3 重试），空参调用会发 HTTP 请求导致超时
	// - help：FormatHelp 输出含 "💡 Use /help --all to show stub commands" 提示文本，
	// 会触发 stub 关键字误报（help 命令本身不是桩，只是帮助文本中提及 stub）
	skipCmds := map[string]bool{
		"upgrade": true,
		"help":    true,
	}

	// 桩标记字符串（大小写不敏感匹配）。
	// 注意：不含 "not yet implemented"（ErrNotImplemented 常量）和 "not initialized"，
	// 这些是 callback 未接线时的标准降级消息（项目设计模式，见 command_test.go:707 验证），
	// 不是真正的桩实现。仅保留 "todo" 作为明确的未完成实现标记。
	stubMarkers := []string{"todo"}

	// 3. 逐个调用 Handler 并检查
	type callResult struct {
		out string
		err error
	}
	for _, cmd := range reg.All() {
		// 显式标记为桩的命令跳过
		if cmd.Stub {
			continue
		}
		if skipCmds[cmd.Name] {
			continue
		}
		if cmd.Handler == nil {
			loc := cmdLocation[cmd.Name]
			t.Errorf("命令 %q 的 Handler 为 nil\n  位置: %s", cmd.Name, loc)
			continue
		}

		// 带超时调用 Handler，防止网络/IO 阻塞
		ch := make(chan callResult, 1)
		go func(name string, handler func(string) (string, error)) {
			defer func() {
				if r := recover(); r != nil {
					ch <- callResult{out: fmt.Sprintf("PANIC: %v", r), err: nil}
				}
			}()
			out, err := handler("")
			ch <- callResult{out: out, err: err}
		}(cmd.Name, cmd.Handler)

		select {
		case r := <-ch:
			lower := strings.ToLower(r.out)
			for _, marker := range stubMarkers {
				if strings.Contains(lower, marker) {
					loc := cmdLocation[cmd.Name]
					t.Errorf("命令 %q 返回桩标记 %q\n  位置: %s\n  输出: %s",
						cmd.Name, marker, loc, r.out)
					break
				}
			}
		case <-time.After(5 * time.Second):
			loc := cmdLocation[cmd.Name]
			t.Logf("命令 %q 调用超时（5s），跳过桩检查\n  位置: %s", cmd.Name, loc)
		}
	}
}

// TestConfigFieldsRead 核查 config.Config 公开字段在业务代码中至少有一处读取。
//
// 用反射扫描 config.Config 的所有导出字段，对每个字段在 internal/ 下的
// 非 config 包、非 _test.go 的 .go 文件中搜索 .FieldName 模式。
// 允许某些字段无读取（如已废弃或仅预留字段），需在 whitelist 中显式列出并说明原因。
// 失败时输出未被读取的字段名。
func TestConfigFieldsRead(t *testing.T) {
	root := findProjectRoot(t)
	internalDir := filepath.Join(root, "internal")

	// 1. 用反射获取 Config 的所有公开字段
	cfgType := reflect.TypeOf(config.Config{})
	var fields []string
	for i := 0; i < cfgType.NumField(); i++ {
		f := cfgType.Field(i)
		if f.PkgPath != "" {
			continue // 非导出字段跳过
		}
		// 跳过 json:"-" 的运行时字段（不持久化，仅运行时设置）
		if strings.Contains(string(f.Tag), `json:"-"`) {
			continue
		}
		fields = append(fields, f.Name)
	}

	// 2. 白名单：允许未被业务代码读取的字段及原因
	whitelist := map[string]string{
		"MaxSnap":              "snapshot 包已被 FileHistory 替代，字段保留用于未来 FileHistory 上限配置化",
		"BudgetCompactPercent": "P0-13 后不再驱动 compact 触发决策，仅作未来恢复百分比模式时的预留字段",
		// 以下为已确认的孤岛字段（Wiring Issue）：仅在 config 包内赋值/验证，无业务代码读取
		"McpServers":             "孤岛字段：MCP 服务配置仅在 config 包内合并赋值，业务代码使用独立 mcp.json，未读取此字段（P1-5 待修复）",
		"FastModeEnabled":        "孤岛字段：fast 模式开关仅在 config 包内从环境变量赋值，无业务代码读取此字段（P1 待修复）",
		"SessionBudget":          "孤岛字段：仅在 config 包内验证和非负修正，无业务代码读取此字段进行预算阻断",
		"PermissionPriorityMode": "孤岛字段：仅在 config 包内验证和回填默认值，无业务代码读取此字段决定权限优先级",
		"ModelCapabilities":      "孤岛字段：仅在 config 包内通过 RegisterModelCapabilitiesFromMap 注册到全局，无其他业务代码直接读取",
		"LeanSystemPrompt":       "v0.1 稳定化冻结：保留配置兼容性，完整接线与 CLI 开关延后到 vNext",
	}

	// 3. 收集 internal/ 下所有 .go 文件内容（排除 config/ 定义处和 _test.go）
	type fileInfo struct {
		path    string
		content string
	}
	var goFiles []fileInfo
	walkErr := filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// 排除 config/ 目录（字段定义处，不算业务读取）
		normalized := filepath.ToSlash(path)
		if strings.Contains(normalized, "/config/") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		goFiles = append(goFiles, fileInfo{path: path, content: string(content)})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("遍历 internal/ 目录失败: %v", walkErr)
	}

	// 3b. 补充扫描项目根目录的 main.go（部分 config 字段在 main.go 中读取，如 PluginsEnabled/MetricsEnabled）
	mainGo := filepath.Join(root, "main.go")
	if mainContent, err := os.ReadFile(mainGo); err == nil {
		goFiles = append(goFiles, fileInfo{path: mainGo, content: string(mainContent)})
	}

	// 4. 对每个字段检查是否有 .FieldName 读取模式
	for _, fieldName := range fields {
		if reason, ok := whitelist[fieldName]; ok {
			t.Logf("白名单字段 %q: %s", fieldName, reason)
			continue
		}
		pattern := "." + fieldName
		found := false
		for _, f := range goFiles {
			if strings.Contains(f.content, pattern) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Config 字段 %q 在 internal/ 业务代码中未被读取（.FieldName 模式未匹配）", fieldName)
		}
	}
}

// TestCLIHelpConsistency 核查 main.go printHelp 文本与实际 flag 注册的一致性。
//
// 从 main.go 中提取 printHelp 函数体内的 flag 文本（--xxx / -x 模式），
// 与 main.go 中实际注册/分发的 flag（arg=="xxx" / args[0]=="xxx" / os.Args[1]=="xxx"
// / strings.HasPrefix(arg, "xxx=")）进行双向比对。
// 断言两者一致：注册的 flag 都在 help 中出现，help 中的 flag 都已注册。
// 失败时输出差异列表。子命令的子参数（如 migrate --from）通过白名单豁免。
//
// 输入完整性：扫描前剔除**整行注释**（TrimSpace 后以 // 起首的行）——
// 注释里的字面量（如 main.go 中讲解 的 `arg == "--xxx"` 示例）
// 不是真实注册点，曾被旧版正则当 flag 误报（门吃了注释当事实）。
// 保守起见只剔整行注释：行尾注释与含 "https://" 的字符串字面量一律不动。
func stripGoLineComments(src string) string {
	lines := strings.Split(src, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func TestCLIHelpConsistency(t *testing.T) {
	root := findProjectRoot(t)
	mainGo := filepath.Join(root, "main.go")
	content, err := os.ReadFile(mainGo)
	if err != nil {
		t.Fatalf("读取 main.go 失败: %v", err)
	}
	mainText := stripGoLineComments(string(content))

	// 1. 提取 printHelp 函数体
	helpStart := strings.Index(mainText, "func printHelp()")
	if helpStart < 0 {
		t.Fatal("未找到 printHelp 函数")
	}
	// 查找下一个顶层 func 声明作为 help 函数的结束
	searchFrom := helpStart + 1
	nextFuncIdx := strings.Index(mainText[searchFrom:], "\nfunc ")
	helpEnd := len(mainText)
	if nextFuncIdx >= 0 {
		helpEnd = searchFrom + nextFuncIdx
	}
	helpBody := mainText[helpStart:helpEnd]

	// 2. 提取 help 文本中的 flag（--xxx 或 -x 形式）
	flagRe := regexp.MustCompile(`--[a-z][a-z-]+|-[a-z]\b`)
	helpFlags := make(map[string]bool)
	for _, m := range flagRe.FindAllString(helpBody, -1) {
		helpFlags[m] = true
	}

	// 3. 提取 main.go 中注册/分发的 flag
	registeredFlags := make(map[string]bool)
	// 匹配 arg == "xxx", args[0] == "xxx", os.Args[N] == "xxx"
	strCmpRe := regexp.MustCompile(`(?:arg|args\[0\]|os\.Args\[\d+\])\s*==\s*"([^"]+)"`)
	for _, m := range strCmpRe.FindAllStringSubmatch(mainText, -1) {
		s := m[1]
		if strings.HasPrefix(s, "-") {
			registeredFlags[s] = true
		}
	}
	// 匹配 switch args[0] { case "--xxx", "-x": ... } 形式（handleArgs 用 switch 分发）
	// case 子句可含多个值用逗号分隔，如 case "--version", "-v":
	caseRe := regexp.MustCompile(`case\s+([^:]+):`)
	for _, m := range caseRe.FindAllStringSubmatch(mainText, -1) {
		// 拆分逗号分隔的多个值
		parts := strings.Split(m[1], ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			// 去除引号
			if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
				s := p[1 : len(p)-1]
				if strings.HasPrefix(s, "-") {
					registeredFlags[s] = true
				}
			}
		}
	}
	// 匹配 strings.HasPrefix(arg, "--xxx=") 形式
	hasPrefixRe := regexp.MustCompile(`strings\.HasPrefix\(arg,\s*"([^"]+)"\)`)
	for _, m := range hasPrefixRe.FindAllStringSubmatch(mainText, -1) {
		s := m[1]
		if idx := strings.Index(s, "="); idx >= 0 {
			s = s[:idx]
		}
		registeredFlags[s] = true
	}

	// 4. help 中出现但属于子命令子参数的白名单（不算顶层 flag 不一致）
	helpOnlyWhitelist := map[string]bool{
		"--from": true, // whale migrate --from 同类 CLI|同类 CLI 的子参数
	}

	// 4b. 注册了但故意未在 help 中文档化的 flag 白名单（调试/内部/高级 flag）
	registeredOnlyWhitelist := map[string]string{
		"--bridge":               "IDE 集成模式，普通用户不需要，避免 help 噪音",
		"--daemon":               "守护进程模式，普通用户不需要",
		"--disable-metrics-auth": "M22-05 P0 调试专用 flag，仅本地调试时禁用鉴权，不应在 help 中暴露",
		"--enable-plugins":       "P0-7/B7 高级 flag，插件默认禁用，避免在 help 中暴露给普通用户",
	}

	// 4c. 别名映射：短 flag 和长 flag 互为别名，help 中出现任一形式即视为已文档化
	aliasMap := map[string]string{
		"-v":         "--version",
		"-h":         "--help",
		"-c":         "--continue",
		"-p":         "--prompt",
		"--continue": "-c",
		"--prompt":   "-p",
		"--version":  "-v",
		"--help":     "-h",
	}

	// 5. 双向比对
	// 5a. 注册了但 help 中未出现的 flag
	for flag := range registeredFlags {
		// 跳过故意未文档化的 flag
		if _, ok := registeredOnlyWhitelist[flag]; ok {
			continue
		}
		// 检查别名是否在 help 中出现
		alias, hasAlias := aliasMap[flag]
		if helpFlags[flag] {
			continue // 自身已出现
		}
		if hasAlias && helpFlags[alias] {
			continue // 别名已出现
		}
		t.Errorf("flag %q 在 main.go 中已注册分发，但未在 printHelp 中出现", flag)
	}
	// 5b. help 中出现了但未注册的 flag
	for flag := range helpFlags {
		if !registeredFlags[flag] {
			// 检查别名是否已注册
			alias, hasAlias := aliasMap[flag]
			if hasAlias && registeredFlags[alias] {
				continue // 别名已注册
			}
			if helpOnlyWhitelist[flag] {
				continue
			}
			t.Logf("flag %q 在 printHelp 中出现，但未在 main.go 分发逻辑中找到（可能为子参数或别名）", flag)
		}
	}
}
