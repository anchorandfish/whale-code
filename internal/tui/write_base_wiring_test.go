package tui

// 装配级判据
//
// 背景： 的写基准守卫（tools/read_base.go）在 tools 包有实现级单测
// （write_base_test.go）。但手测卡「P-a 并发覆盖」要求**用户切终端改文件、
// 再回会话写**才能触发 —— 这条依赖两个模型自觉（① 第一步真的调 read_file 建基准；
// ② 第三步不自己先读一遍刷新基准），且用户无法从 TUI 界面看出「基准到底登记没登记」，
// 因此**不可判别**（2026-10-14 用户实测：会话里没发生 read_file ⇒ 未登记 ⇒ 放行，
// 被误读成"守卫没接上"）。
//
// 本文件把这条面从「依赖模型自觉的手测」降级为「走生产装配的机测」：
// - 构造：`NewModel`（生产构造 `newTUIRegressionModel`）；executor 用同款生产构造器
// `tools.NewExecutor`（workDir = 临时目录，避免污染仓库）。
// - 入口：`m.toolService.ExecuteTool`（= TUI 工具执行的生产入口，
// `tool_exec.go:29` 的 `capturedExecutor` 同源）→ 真实 read_file / write_file handler。
// - 机制断言：读与写必须落在**同一个 executor 实例**上（`lifecycle.go:360` 装配的
// `signatureExecutor`，进程启动建一次、指针跨回合共享）—— 这正是手测里最容易失真的
// 那一环（重启 whale / 新会话 ⇒ 内存登记表清空 ⇒ 假绿）。
//
// 判别性：① 把 read_file 那步删掉 ⇒ 本判据退化为「未登记放行」（下方反例锁定该语义）；
// ② 把守卫（file.go:283 的 checkWriteBase 分支）删掉 ⇒ 主用例变红。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// newW072Model 构造一个"工具执行走生产装配"的测试模型：
// 隔离 HOME（filehistory 落临时目录），executor 的 workDir 指向临时目录。
func newW072Model(t *testing.T) (*Model, string) {
	t.Helper()
	// ★ 刻意用 os.MkdirTemp 而非 t.TempDir()：executor 的 filehistory 会起长驻后台
	// goroutine（GC/遗忘扫描）持有该目录，t.TempDir() 的自动 RemoveAll 会因"目录非空"
	// 竞态报错（同 turn_diff_display_test.go 的注释，2026-10-08 实测过一次假失败）。
	home, err := os.MkdirTemp("", "w072home")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)

	m := newTUIRegressionModel(t)
	workDir := t.TempDir()
	// 与 lifecycle.go:360 装配的是同一个生产字段、同一个类型（仅换 workDir 做隔离）。
	m.ToolModel.executor = tools.NewExecutor(tools.NewRegistry(), workDir)
	m.reassignServices()
	return m, workDir
}

// execTool 经生产工具入口执行一次工具调用。
func execTool(t *testing.T, m *Model, name string, input any) *tools.Result {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.toolService.ExecuteTool(context.Background(), name, json.RawMessage(raw))
	if err != nil {
		t.Fatalf("%s 返回 error（不期望）: %v", name, err)
	}
	if res == nil {
		t.Fatalf("%s 返回 nil result", name)
	}
	return res
}

// 主用例：本会话 read_file 读过 → 外部进程改写 → write_file 盲覆写 ⇒ 必须拒绝。
func TestW072_TUIProductionPath_BlindOverwriteRejected(t *testing.T) {
	m, workDir := newW072Model(t)
	p := filepath.Join(workDir, "note.txt")
	if err := os.WriteFile(p, []byte("AAA\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// ① 经生产工具入口读（服务端登记「读基准」= 盘上原始字节 hash）
	if r := execTool(t, m, "read_file", map[string]string{"path": p}); r.IsError {
		t.Fatalf("前置失败：read_file 应成功，实得 IsError=true: %s", r.Output)
	}

	// ② 外部进程改写（vim / 另开终端 printf，不经 whale）
	if err := os.WriteFile(p, []byte("BBB-external\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// ③ 盲覆写 ⇒ 必须被拒
	r := execTool(t, m, "write_file", map[string]string{"path": p, "content": "CCC"})
	if !r.IsError {
		t.Fatalf("假绿： 守卫未接上 —— 「本会话读过、此后被外部改写」的整篇覆写被放行。\n"+
			"  result: %+v", r)
	}
	if !strings.Contains(r.Output, "has changed on disk since it was last read") {
		t.Fatalf("拒绝文案不含逐字判据串。\n  实得输出: %s", r.Output)
	}
	if r.FailureKind != tools.FailureKindPatchConflict {
		t.Errorf("拒绝必须走结构化通道 FailureKind=patch_conflict，实得 %q", r.FailureKind)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "BBB-external\n" {
		t.Fatalf("外部修改被覆盖：盘上应为 BBB-external，实得 %q", string(data))
	}

	// ④ 自愈：重读后再写 ⇒ 放行（判据要求"重读后自愈成功"）
	execTool(t, m, "read_file", map[string]string{"path": p})
	if r := execTool(t, m, "write_file", map[string]string{"path": p, "content": "CCC\n"}); r.IsError {
		t.Fatalf("重读后重写应自愈放行，实得 IsError=true: %s", r.Output)
	}
	if data, _ := os.ReadFile(p); string(data) != "CCC\n" {
		t.Fatalf("自愈后盘上应为 CCC，实得 %q", string(data))
	}
}

// 反例（防过度拦截）：本会话从未读过的文件，写必须放行 —— 与修复前行为一致。
func TestW072_TUIProductionPath_UnreadFileAllowed(t *testing.T) {
	m, workDir := newW072Model(t)
	p := filepath.Join(workDir, "other.txt")
	if err := os.WriteFile(p, []byte("untouched\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// 从未经 read_file 读 ⇒ 未登记 ⇒ 放行
	if r := execTool(t, m, "write_file", map[string]string{"path": p, "content": "XXX"}); r.IsError {
		t.Fatalf("过度拦截：本会话从未读过的文件，write_file 被拒。\n  输出: %s", r.Output)
	}
	if data, _ := os.ReadFile(p); string(data) != "XXX" {
		t.Fatalf("盘上应为 XXX，实得 %q", string(data))
	}
}
