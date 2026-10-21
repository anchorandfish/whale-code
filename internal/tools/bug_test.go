package tools

// 反例测试（Bug Reproduction Tests）— tools 模块
//
// 本文件是对标审查机制的核心组件。每个测试对应 docs/BENCHMARK_FACTS.md 中的一个事实条目。
//
// 测试特点：
// - 测试名以 TestBug_ 开头
// - 在当前（含 bug 的）代码上运行会 **失败**，证明 bug 存在
// - 修复 bug 后测试 **通过**，证明 bug 已修
// - 如果实现实际合规，测试通过（区分真假 bug，避免误报）
//
// 使用方式：
//   go test ./internal/tools/ -run TestBug_ -v
//
// 预期结果：当前代码下失败的 TestBug_ 测试证明 bug 存在
// 修复后：所有 TestBug_ 测试应通过

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// TestBug_BashSubstringDetection 对应 FACT-TOOLS-001
// Bash 命令必须用 AST 解析而非子串检测
// 当前实现：bash.go convertLinuxToPowerShell 用 strings.HasPrefix/Contains 检测命令
// 混淆命令（shell 引号拼接）无法被子串匹配识别，AST 解析可正确处理
func TestBug_BashSubstringDetection(t *testing.T) {
	// FACT-TOOLS-001
	// 这些混淆的 rm 命令应被识别为 rm（AST 会解析 shell 引号）
	// 但子串/前缀匹配（strings.HasPrefix(trimmed, "rm ")）无法识别
	obfuscatedCmds := []string{
		"r''m -rf /tmp",
		`r""m -rf /tmp`,
		"r\\m -rf /tmp",
	}

	for _, cmd := range obfuscatedCmds {
		result := convertLinuxToPowerShell(cmd)
		// AST 解析会识别 r''m 等价于 rm，转换为 Remove-Item
		// 子串匹配遗漏，返回原命令
		if !strings.Contains(result, "Remove-Item") {
			t.Errorf("BUG [FACT-TOOLS-001]: 混淆 rm 命令未被识别（子串匹配遗漏）\n"+
				"  命令: %s\n"+
				"  期望: 检测为 rm 并转换为 Remove-Item（AST 解析 shell 引号）\n"+
				"  实际: 未检测，返回原命令 %q（子串匹配遗漏）\n"+
				"  原因: bash.go convertLinuxToPowerShell 用 strings.HasPrefix(trimmed, \"rm \")\n"+
				"  正确做法: 用 tree-sitter AST 解析命令，提取命令名/参数/引号\n"+
				"  证据: 外部参考实现 全套 AST 验证器",
				cmd, result)
		}
	}
}

// TestBug_NoCommandValidators 对应 FACT-TOOLS-002
// 危险命令必须有专用验证器，检查 --force、-rf、--no-preserve-root 等危险 flag
// 当前实现：bash.go convertRmCommand 仅做格式转换，无任何安全验证
func TestBug_NoCommandValidators(t *testing.T) {
	// FACT-TOOLS-002
	// 这些危险 rm 命令应被专用验证器拦截或警告
	dangerousCmds := []string{
		"rm -rf --no-preserve-root /",
		"rm -rf /",
		"rm -f /etc/passwd",
	}

	for _, cmd := range dangerousCmds {
		result := convertRmCommand(cmd)
		// 专用验证器应拦截 --no-preserve-root、-rf / 等危险组合
		// convertRmCommand 仅做 PowerShell 格式转换，无安全检查
		if strings.Contains(result, "Remove-Item") {
			t.Errorf("BUG [FACT-TOOLS-002]: 危险 rm 命令被直接转换，无专用验证器拦截\n"+
				"  命令: %s\n"+
				"  期望: 专用验证器拦截或警告（--no-preserve-root, -rf /）\n"+
				"  实际: 直接转换为 %q（无安全检查）\n"+
				"  原因: bash.go 无专用命令验证器，仅靠黑名单字符串匹配\n"+
				"  正确做法: rm/curl/wget/git 等危险命令各一个验证器，检查危险 flag\n"+
				"  证据: 外部参考实现 每个危险命令一个验证器文件",
				cmd, result)
		}
	}
}

// TestBug_KillProcessTreeUnixMissing 对应 FACT-TOOLS-003
// 进程树终止必须跨平台：Unix 用进程组 kill，Windows 用 Job Object/taskkill /T
// 当前实现：bash.go:749-754 仅 Windows 有 taskkill /T，Unix 无进程组 kill
func TestBug_KillProcessTreeUnixMissing(t *testing.T) {
	// FACT-TOOLS-003
	t.Skip("需要启动子进程并验证进程树终止 - 手动验证步骤：\n" +
		"  1. 在 Unix 上执行 bash 命令 'sleep 100 & sleep 200'（后台子进程）\n" +
		"  2. 设置 timeout=2s 让命令超时\n" +
		"  3. 超时后用 ps 检查 sleep 子进程是否存活\n" +
		"  4. 预期（BUG）：Unix 下子进程孤儿存活，因为 bash.go:749-754 仅 Windows 有 taskkill /T\n" +
		"  5. 正确做法：Unix 用 setpgid + kill -PGID 终止整个进程组\n" +
		"  证据：同类工具 跨平台进程管理，Unix 用进程组 kill\n" +
		"  代码位置：bash.go:749-754 if runtime.GOOS == \"windows\" && err != nil { taskkill /F /T }")
}

// TestBug_FilePathTraversalFalsePositive 对应 FACT-TOOLS-010
// 路径遍历检测必须用路径组件，而非子串
// 当前实现：file.go:374 strings.Contains(path, "..") 误报合法路径
func TestBug_FilePathTraversalFalsePositive(t *testing.T) {
	// FACT-TOOLS-010
	// 合法文件名包含 ".." 但不是路径遍历
	legalPaths := []string{
		"my..file.txt",
		"version-1..2.tar.gz",
		"src/foo..bar.go",
		"docs/section..subsection.md",
	}

	for _, path := range legalPaths {
		err := validateFilePath(path)
		if err != nil {
			t.Errorf("BUG [FACT-TOOLS-010]: 合法路径被误报为路径遍历\n"+
				"  路径: %s\n"+
				"  期望: 不报错（合法路径，.. 不是独立路径组件）\n"+
				"  实际: %v\n"+
				"  原因: file.go:374 strings.Contains(path, \"..\") 子串匹配\n"+
				"  正确做法: 用路径组件检查，.. 必须是独立路径组件才报警\n"+
				"  正则: /(?:^|[\\\\/])\\.\\.(?:[\\\\/]|$)/\n"+
				"  证据: 外部参考实现（留档见审查文档）",
				path, err)
		}
	}
}

// TestBug_NonAtomicWrite 对应 FACT-TOOLS-011
// 文件写入必须原子化（临时文件 + rename）
// 当前实现：file.go writeFileHandler 使用 util.AtomicWriteFile（临时文件 + rename）
// 此测试验证写入确实是原子的 — 如果通过，说明实现合规（无 bug）
func TestBug_NonAtomicWrite(t *testing.T) {
	// FACT-TOOLS-011
	// 此测试验证 util.AtomicWriteFile 确实使用临时文件 + rename 模式
	// 如果测试通过，说明 FACT-TOOLS-011 合规（事实表中标注的"直接写入"状态已过时）
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")

	// 写入初始内容
	if err := os.WriteFile(target, []byte("original"), 0644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	// 原子写入新内容
	if err := util.AtomicWriteFile(target, []byte("new content"), 0644); err != nil {
		t.Fatalf("AtomicWriteFile failed: %v", err)
	}

	// 验证内容已写入
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "new content" {
		t.Errorf("content mismatch: got %q, want %q", string(data), "new content")
	}

	// 验证没有临时文件残留
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("临时文件残留: %s（说明 rename 未完成或非原子）", e.Name())
		}
	}

	// 此测试通过，证明 FACT-TOOLS-011 合规
	// util.AtomicWriteFile 使用临时文件 + os.Rename（原子替换）
	// 事实表中标注的"直接写入，Windows 下非原子"状态已过时
	t.Log("FACT-TOOLS-011: 合规 — util.AtomicWriteFile 使用临时文件 + rename（原子写入）")
}

// TestBug_NoMtimeCheck 对应 FACT-TOOLS-012
// 必须检测文件外部修改（mtime 检查），防止覆盖外部修改导致数据丢失。
// T6 设计：客户端读取时获得 mtime（ReturnMtime 尾行），写入时携带 ExpectedMtime，
// handler 比对当前 mtime 与 ExpectedMtime，不一致则拒绝写入（opt-in 乐观锁）。
func TestBug_NoMtimeCheck(t *testing.T) {
	// FACT-TOOLS-012
	dir := t.TempDir()
	reg := NewRegistry()
	e := NewExecutor(reg, dir)

	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("original"), 0644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	// 记录原始 mtime（模拟客户端读取文件时获得的 mtime）
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	originalMtime := info.ModTime()

	// 模拟外部修改（另一个进程/编辑器修改了文件，mtime 随之变化）
	time.Sleep(15 * time.Millisecond)
	if err := os.WriteFile(filePath, []byte("external modification"), 0644); err != nil {
		t.Fatalf("external modify failed: %v", err)
	}

	// 通过 handler 写入新内容，携带 ExpectedMtime — 应检测到 mtime 变化并拒绝
	handler := e.writeFileHandler()
	inputJSON, _ := json.Marshal(WriteFileInput{
		Path:          filePath,
		Content:       "handler write",
		ExpectedMtime: &originalMtime,
	})
	_, err = handler(context.Background(), inputJSON)
	if err == nil {
		t.Errorf("BUG [FACT-TOOLS-012]: writeFileHandler 未检测到 mtime 变化，覆盖了外部修改\n" +
			"  场景: 文件被外部修改后，handler 携带旧 ExpectedMtime 写入\n" +
			"  期望: 检测到 mtime 不一致并返回错误（防止数据丢失）\n" +
			"  实际: 未检测 mtime，直接覆盖外部修改\n" +
			"  原因: file.go writeFileHandler 无 mtime 守卫\n" +
			"  正确做法: 写入前比对 ExpectedMtime 与当前 mtime，不一致则拒绝\n" +
			"  证据: 外部参考实现 mtime 守卫")
	}

	// 验证文件未被覆盖（外部修改保留）
	data, _ := os.ReadFile(filePath)
	if string(data) == "handler write" {
		t.Errorf("BUG [FACT-TOOLS-012]: writeFileHandler 覆盖了外部修改，mtime 守卫未生效\n"+
			"  期望: 文件内容仍为外部修改 %q\n"+
			"  实际: 被覆盖为 %q\n"+
			"  原因: mtime 守卫未拒绝写入", "external modification", string(data))
	}
}

// TestBug_UNCPathAllowed 对应 FACT-TOOLS-013
// Windows 下 UNC 路径（\\server\share）必须拦截，防止访问网络资源
// 当前实现：file.go validateFilePath 无 UNC 检测
func TestBug_UNCPathAllowed(t *testing.T) {
	// FACT-TOOLS-013
	// UNC 路径可访问网络共享资源，必须拦截
	uncPaths := []string{
		`\\server\share\file.txt`,
		`\\localhost\c$\secret.txt`,
		`\\malicious\share\payload.exe`,
	}

	for _, path := range uncPaths {
		err := validateFilePath(path)
		if err == nil {
			t.Errorf("BUG [FACT-TOOLS-013]: UNC 路径未被拦截\n"+
				"  路径: %s\n"+
				"  期望: 拦截 UNC 路径（防止访问网络资源）\n"+
				"  实际: 通过验证（无 UNC 检测）\n"+
				"  原因: file.go validateFilePath 无 UNC 路径检测\n"+
				"  正确做法: 检测路径以 \\\\ 开头（UNC 前缀）并拒绝\n"+
				"  证据: 外部参考实现 UNC 检测",
				path)
		}
	}
}

// TestBug_UnregisteredToolPassesSchema 对应 FACT-TOOLS-020
// 工具注册必须 fail-closed：未注册工具应被拒绝，而非跳过验证放行
// 当前实现：pipeline.go SchemaValidateStage 对未注册工具返回 Proceed=true（fail-open）
func TestBug_UnregisteredToolPassesSchema(t *testing.T) {
	// FACT-TOOLS-020
	reg := NewRegistry()
	stage := &SchemaValidateStage{registry: reg}

	// "fake_tool" 未注册
	call := Call{Name: "fake_tool", Input: `{"param":"value"}`}
	result := stage.Execute(context.Background(), call, &PipelineState{})

	if result.Proceed {
		t.Errorf("BUG [FACT-TOOLS-020]: 未注册工具通过 Schema 验证（fail-open）\n" +
			"  工具: fake_tool（未注册）\n" +
			"  期望: Proceed=false（未注册工具应 fail-closed 拒绝）\n" +
			"  实际: Proceed=true（跳过验证放行）\n" +
			"  原因: pipeline.go:188-195 SchemaValidateStage 未注册工具返回 Proceed=true\n" +
			"  正确做法: buildTool 失败/未注册时整个工具不可用（fail-closed）\n" +
			"  证据: 外部参考实现 TOOL_DEFAULTS fail-closed 模式")
	}
}

// TestBug_PermissionStageFailOpen 对应 FACT-TOOLS-020
// 工具权限阶段 nil 策略时必须 fail-closed（拒绝），而非 fail-open（放行）
// 当前实现：pipeline.go:486-489 PermissionStage nil policy 时返回 Proceed=true
func TestBug_PermissionStageFailOpen(t *testing.T) {
	// FACT-TOOLS-020
	stage := &PermissionStage{policy: nil}

	call := Call{Name: "write_file", Input: `{"path":"/tmp/test.txt","content":"test"}`}
	result := stage.Execute(context.Background(), call, &PipelineState{})

	if result.Proceed {
		t.Errorf("BUG [FACT-TOOLS-020]: nil 权限策略 fail-open（放行所有工具）\n" +
			"  场景: PermissionStage policy == nil\n" +
			"  期望: Proceed=false（无策略时 fail-closed 拒绝）\n" +
			"  实际: Proceed=true（无策略时 fail-open 放行）\n" +
			"  原因: pipeline.go:486-489 s.policy == nil 时返回 Proceed=true\n" +
			"  正确做法: 无策略时默认拒绝（fail-closed），而非放行\n" +
			"  证据: 外部参考实现 TOOL_DEFAULTS fail-closed 模式")
	}
}

// TestBug_SubAgentPermissionBypass 对应 FACT-TOOLS-021
// 子代理权限必须冒泡：子代理工具调用应继承父会话权限，通过 Pipeline 检查
// C-P0-3 修复：executeToolCall 已改用 ExecuteWithPipeline + Pipeline=nil fail-closed
// 反例测试已迁移至 agent_pipeline_test.go::TestCP0_3_PipelineEnforced
func TestBug_SubAgentPermissionBypass(t *testing.T) {
	// FACT-TOOLS-021
	// C-P0-3 已修复：executeToolCall 现在走 ExecuteWithPipeline，不再绕过权限审批。
	// 完整反例测试见 agent_pipeline_test.go::TestCP0_3_PipelineEnforced
	// （设置 LevelReadOnly 策略，验证子代理调用 bash 被 Pipeline 权限阶段拒绝）
	t.Skip("C-P0-3 已修复，反例测试迁移至 agent_pipeline_test.go::TestCP0_3_PipelineEnforced")
}

// TestBug_PatternSubstringAffectsTools 对应 FACT-MATCH-001
// 命令匹配必须按 token 而非子串（permission/rules.go matchPattern 也影响 tools）
// 当前实现：permission/rules.go:296-302 matchPattern 默认用 strings.Contains
func TestBug_PatternSubstringAffectsTools(t *testing.T) {
	// FACT-MATCH-001
	t.Skip("matchPattern 在 permission 包，需跨包测试 - 手动验证步骤：\n" +
		"  1. 配置 deny 规则 'rm:*'（拒绝 rm 命令）\n" +
		"  2. 通过 bash 工具执行命令 'arm --version'\n" +
		"  3. 预期（BUG）：'arm' 被 'rm' 规则误匹配（strings.Contains(\"arm\", \"rm\") = true）\n" +
		"  4. 'arm' 应被放行但被误拒\n" +
		"  5. 原因：permission/rules.go:296-302 matchPattern 默认 case 用 strings.Contains(s, core)\n" +
		"  6. 正确做法：按 token 精确匹配或前缀匹配，非子串包含\n" +
		"  7. 验证命令：go test ./internal/permission/ -run TestBug_PatternSubstringFalsePositive -v\n" +
		"  证据：通用原则，同类工具 按 token 匹配")
}
