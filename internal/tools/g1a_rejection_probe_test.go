package tools

// =============================================================================
// S11 G1-A —— 「沙箱拒绝上报统一」取证 / 表征探针
// （**只读现状**：本文件不含任何生产改动，可独立运行）
//
// 目的：在选 A/B/C 之前，先用**事实**回答两个问题 ——
// ① 6 条沙箱/越界拒绝路径，当前各自产出 (IsError, FailureKind) 是什么？
// ② 其中哪几条在**生产入口**（InitPipelineWithHooks → Execute）上真的可达？
//
// 为什么必须实测、不能只读代码：读代码只能得到"handler 返回 (msg, nil)"，
// 但**管线会不会覆盖/前置拦掉它**，只有跑一遍才知道。实测已确认两条关键前提：
// - `ExecuteStage.Execute`（pipeline.go:1085）内部调 `executeDirect`，
// ⇒ 软哨兵 `errSandboxDeniedSoft` 的映射在**有管线**时同样生效
// （选项 B 因此可以只改 handler、不碰管线）。
// - `CustomValidateStage` 的工作区边界只对 `isWriteTool` 生效（pipeline.go:545
// = write_file/edit_file/NotebookEdit/LspRename/git_*/export_session），
// ⇒ Glob / Grep / Monitor **不受管线边界保护**，handler 层是它们**唯一**的沙箱门，
// 即清单 3/4/5 在**生产主路径**上真实可达（不是"仅有回退入口"）。
//
// ---------------------------------------------------------------------------
// 实测结论表（2026-09-14，darwin/amd64，本机生产沙箱）
//
//	# 路径 入口 IsError FailureKind
//	1 write_file file.go:255 executeDirect(Pipeline==nil) false sandbox_denied
//	2 edit_file file.go:389 executeDirect(Pipeline==nil) false sandbox_denied
//	3 monitor monitor.go:41 生产管线 false "" ← 漏
//	4 glob search.go:27 生产管线 false "" ← 漏
//	5 grep search.go:68 生产管线 false "" ← 漏
//	6 plugin bash executor.go:1234 生产管线 false "" ← 漏
//	7 bash 硬拒 bash.go:804 生产管线 true sandbox_denied （对照）
//
// ⇒ 「漏 FailureKind」的正是 3/4/5/6；1/2 已由 S11 G1-B 落地（软哨兵）。
// ⇒ G1-A 选 B 落盘后：3/4/5/6 的 FailureKind 均由 "" 变为 sandbox_denied（IsError 保持 false）。
//
// ---------------------------------------------------------------------------
// G1-A 落盘结论：**选 B**（只补 FailureKind、保留 IsError=false）—— 与 S11 G1-B 语义一致。
// 清单 3/4/5/6 的 handler 现已返回软哨兵 errSandboxDeniedSoft，由 executeDirect 统一映射为
// IsError=false + FailureKind=SandboxDenied。上表「漏」列已全部补齐（1/2 早已由 G1-B 落地）。
//
// 双模设计（判别性闭环 —— 无 git 仓库下「回退即红」的等价物）
//
//	默认 → 断言 **G1-A(B) 落盘后** 的目标形态 ⇒ 现在**绿**。
//	WHALE_G1A_PRE=1 → 断言落盘**之前**的现状 ⇒ 现在**红**（3/4/5/6）。
// 证明这批用例确实有牙，不是「怎么改都绿」的空断言。
//
// 实测判别性证据（同一批用例，两个方向都翻过红）：
//	落盘前 12:44：默认档 绿 · WHALE_G1A_STRICT=1 → **4 红**（3/4/5/6）
//	落盘后 12:4x：默认档 绿 · WHALE_G1A_PRE=1 → **4 红**（3/4/5/6）
//	（旧 env 名 WHALE_G1A_STRICT 已并入默认档，不再需要。）
//
// 运行：
//	cd whale-code
//	go test -mod=vendor -count=1 ./internal/tools/ -run TestG1A_Probe -v
//	WHALE_G1A_PRE=1 go test -mod=vendor -count=1 ./internal/tools/ -run TestG1A_Probe -v
//
// ⚠️ 若 G1-A 最终选 **A**（IsError 也统一为 true），请把下表 target 列的
// tgtIsError 一并改为 true —— 那才是 A 的判据。
// =============================================================================

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/monitor"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// g1aAllow 构造 workspace-write 策略并显式放行给定工具。
//
// 为什么必须显式放行：走完整生产管线时 PermissionStage 会拦未放行的工具并返回
// `requires user confirmation`（NeedConfirm，无确认回调即视为失败），
// 那样就测不到本探针想测的**沙箱/边界**分支了。这是探针前提，不是产品缺陷。
func g1aAllow(t *testing.T, names ...string) *permission.Policy {
	t.Helper()
	p := permission.NewPolicy(permission.LevelWorkspaceWrite)
	for _, n := range names {
		rule, err := permission.ParseRuleString(n, permission.RuleAllow, permission.SourceSession)
		if err != nil {
			t.Fatalf("ParseRuleString(%s): %v", n, err)
		}
		if err := p.AddRule(*rule); err != nil {
			t.Fatalf("AddRule(%s): %v", n, err)
		}
	}
	return p
}

// g1aRow 是一次调用的取证记录。
type g1aRow struct {
	Path    string // 清单编号 + 生产坐标
	Entry   string // 进入方式
	IsError bool
	Kind    FailureKind
	Output  string
}

// g1aWant 同一条路径的「现状」与「G1-A 目标」两组期望。
type g1aWant struct {
	curIsError bool
	curKind    FailureKind
	tgtIsError bool
	tgtKind    FailureKind
}

// g1aVerify 打印取证行，并按当前档位（现状 / 目标）断言。
func g1aVerify(t *testing.T, r g1aRow, w g1aWant) {
	t.Helper()
	// 默认断言「G1-A(B) 落盘后」的目标形态；WHALE_G1A_PRE=1 断言落盘前的现状（判别性反证）。
	pre := os.Getenv("WHALE_G1A_PRE") == "1"
	wantIsError, wantKind, mode := w.tgtIsError, w.tgtKind, "G1-A(B)落盘后"
	if pre {
		wantIsError, wantKind, mode = w.curIsError, w.curKind, "G1-A(B)落盘前(反证)"
	}

	t.Logf("[G1-A/%s] %-34s entry=%-28s IsError=%-5v FailureKind=%-16q out=%.52q",
		mode, r.Path, r.Entry, r.IsError, string(r.Kind), r.Output)

	if r.IsError != wantIsError {
		t.Errorf("%s：%s 期望 IsError=%v，实际 %v；output=%q",
			r.Path, mode, wantIsError, r.IsError, r.Output)
	}
	if r.Kind != wantKind {
		t.Errorf("%s：%s 期望 FailureKind=%q，实际 %q；output=%q",
			r.Path, mode, wantKind, r.Kind, r.Output)
	}
}

// g1aCollect 发起一次生产调用并收集取证行。
func g1aCollect(e *Executor, path, entry, name, input string) g1aRow {
	res := e.Execute(context.Background(), Call{ID: "g1a", Name: name, Input: input})
	return g1aRow{Path: path, Entry: entry, IsError: res.IsError, Kind: res.FailureKind, Output: res.Output}
}

// g1aSoft 是 1/2/3/4/5/6 当前共同的**形态**：软拒（IsError=false）。
// 差别只在 FailureKind 有没有被打上 —— 这正是 G1-A 的分歧点。
var g1aSoftCur = g1aWant{curIsError: false, tgtIsError: false}

// -----------------------------------------------------------------------------
// TestG1A_Probe_AllRejectionPaths —— 清单 6 条 + bash 硬拒对照
// -----------------------------------------------------------------------------
func TestG1A_Probe_AllRejectionPaths(t *testing.T) {
	t.Run("01_write_file_handler_fallback", func(t *testing.T) {
		// 清单 1（file.go:255）。⚠️ 文档化的**回退入口**：写类工具的越界在主路径被
		// CustomValidateStage 先拦（见 06），handler 层 serve 的是 `Pipeline==nil`
		// 与「沙箱比管线更严」的纵深防御场景（s11_manual_probe_test.go P2 同款）。
		ws := t.TempDir()
		e := NewExecutor(NewRegistry(), ws)
		e.SetSandboxMgr(sandbox.NewProductionSandboxManager(ws))
		// 故意不 InitPipelineWithHooks

		// ⚠️ 必须用真实沙箱**确实会拒**的目标：`t.TempDir()` 的父级仍在
		// ProductionSandboxManager 的允许范围内（实测：写到 ws/.. 会被放行），
		// 只有系统目录才稳定触发 handler 层拒绝（s11_manual_probe_test.go P1b 已证）。
		outside := "/etc/whale_g1a_probe_write.txt"
		r := g1aCollect(e, "01 write_file(file.go:255)", "executeDirect(Pipeline==nil)",
			"write_file", fmt.Sprintf(`{"path":%q,"content":"x"}`, outside))
		if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
			t.Fatalf("越界文件竟被创建：%s —— 本用例的拒绝前提失效", outside)
		}
		g1aVerify(t, r, g1aWant{
			curIsError: false, curKind: FailureKindSandboxDenied,
			tgtIsError: false, tgtKind: FailureKindSandboxDenied, // 已落地（G1-B），两档一致
		})
	})

	t.Run("02_edit_file_handler_fallback", func(t *testing.T) {
		ws := t.TempDir()
		e := NewExecutor(NewRegistry(), ws)
		e.SetSandboxMgr(sandbox.NewProductionSandboxManager(ws))

		outside := "/etc/whale_g1a_probe_edit.txt"
		r := g1aCollect(e, "02 edit_file(file.go:389)", "executeDirect(Pipeline==nil)",
			"edit_file", fmt.Sprintf(`{"path":%q,"old_text":"a","new_text":"b"}`, outside))
		if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
			t.Fatalf("越界文件竟被创建：%s —— 本用例的拒绝前提失效", outside)
		}
		g1aVerify(t, r, g1aWant{
			curIsError: false, curKind: FailureKindSandboxDenied,
			tgtIsError: false, tgtKind: FailureKindSandboxDenied,
		})
	})

	t.Run("03_monitor", func(t *testing.T) {
		// 清单 3（monitor.go:41）。Monitor 不是 write 工具 ⇒ 管线边界不覆盖它。
		ws := t.TempDir()
		e := NewExecutor(NewRegistry(), ws)
		// 真实 Manager + 白名单只允许 go ⇒ curl 必被拒（不真的起进程）。
		e.SetSandboxMgr(sandbox.NewManager(
			&sandbox.SandboxConfig{WorkDir: ws, AllowCommands: []string{"go"}},
			sandbox.SandboxLevelRelaxed))
		e.MonitorMgr = monitor.NewManager()
		e.InitPipelineWithHooks(g1aAllow(t, "Monitor"), ws)
		if e.Pipeline == nil {
			t.Fatal("Pipeline 未初始化：本用例前提失效")
		}

		before := len(e.MonitorMgr.List())
		r := g1aCollect(e, "03 monitor(monitor.go:41)", "InitPipelineWithHooks+Execute",
			"Monitor", `{"command":"curl http://evil.com","description":"g1a probe"}`)
		if after := len(e.MonitorMgr.List()); after != before {
			t.Errorf("拒绝路径竟创建了监控进程：before=%d after=%d —— 拒绝前提失效", before, after)
		}
		w := g1aSoftCur
		w.curKind, w.tgtKind = "", FailureKindSandboxDenied
		g1aVerify(t, r, w)
	})

	t.Run("04_glob_search", func(t *testing.T) {
		// 清单 4（search.go:27）。搜索目录越出工作区。
		ws := t.TempDir()
		e := NewExecutor(NewRegistry(), ws)
		e.SetSandboxMgr(sandbox.NewProductionSandboxManager(ws))
		e.InitPipelineWithHooks(g1aAllow(t, "glob_search"), ws)

		r := g1aCollect(e, "04 glob(search.go:27)", "InitPipelineWithHooks+Execute",
			"glob_search", `{"pattern":"*.txt","path":"/etc"}`)
		w := g1aSoftCur
		w.curKind, w.tgtKind = "", FailureKindSandboxDenied
		g1aVerify(t, r, w)
	})

	t.Run("05_grep_search", func(t *testing.T) {
		// 清单 5（search.go:68）。
		ws := t.TempDir()
		e := NewExecutor(NewRegistry(), ws)
		e.SetSandboxMgr(sandbox.NewProductionSandboxManager(ws))
		e.InitPipelineWithHooks(g1aAllow(t, "grep_search"), ws)

		r := g1aCollect(e, "05 grep(search.go:68)", "InitPipelineWithHooks+Execute",
			"grep_search", `{"pattern":"root","path":"/etc"}`)
		w := g1aSoftCur
		w.curKind, w.tgtKind = "", FailureKindSandboxDenied
		g1aVerify(t, r, w)
	})

	t.Run("06_plugin_tool_bash", func(t *testing.T) {
		// 清单 6（executor.go:1234，插件工具直 exec 路径）。
		ws := t.TempDir()
		g1aWritePlugin(t, ws, "rm -rf /")

		e := NewExecutor(NewRegistry(), ws)
		if e.PluginMgr == nil {
			t.Fatal("PluginMgr 未初始化：<ws>/.whale/plugins 未被发现，前提失效")
		}
		e.EnableAllPlugins()
		e.SetSandboxMgr(sandbox.NewManager(
			&sandbox.SandboxConfig{WorkDir: ws, AllowCommands: []string{"go"}},
			sandbox.SandboxLevelRelaxed))
		e.InitPipelineWithHooks(g1aAllow(t, "custom_tool"), ws)

		r := g1aCollect(e, "06 plugin(executor.go:1234)", "InitPipelineWithHooks+Execute",
			"custom_tool", `{}`)
		// 前置断言：handler 必须真的被注册并被调用（防"测了个不存在的工具"假绿）。
		if r.Output == "unknown tool: custom_tool" {
			t.Fatal("前置失败：custom_tool handler 未注册，本用例没走到插件沙箱分支")
		}
		w := g1aSoftCur
		w.curKind, w.tgtKind = "", FailureKindSandboxDenied
		g1aVerify(t, r, w)
	})

	t.Run("07_bash_hard_deny_control", func(t *testing.T) {
		// 对照组（**非清单项、不在 G1-A 变更对象内**）：bash 硬拒绝已经是
		// IsError=true + kind。放在同一张表里是为了让 A/B/C 的差别可读。
		ws := t.TempDir()
		e := NewExecutor(NewRegistry(), ws)
		e.SetSandboxMgr(sandbox.NewManager(
			&sandbox.SandboxConfig{WorkDir: ws, AllowCommands: []string{"go"}},
			sandbox.SandboxLevelRelaxed))
		e.InitPipelineWithHooks(g1aAllow(t, "bash"), ws)

		r := g1aCollect(e, "07 bash(bash.go:804) 对照", "InitPipelineWithHooks+Execute",
			"bash", `{"command":"curl http://evil.com"}`)
		g1aVerify(t, r, g1aWant{
			curIsError: true, curKind: FailureKindSandboxDenied,
			tgtIsError: true, tgtKind: FailureKindSandboxDenied,
		})
	})
}

// -----------------------------------------------------------------------------
// TestG1A_Probe_GrepGlobReachability
//
// 单独钉一件事：Glob 的越界拒绝**在生产管线下可达**，且**确实由 handler 的沙箱门**
// 拦下（不是被管线提前拦掉、也不是被别的原因拒掉）。这条不成立 ⇒ B 方案收益要重估。
// -----------------------------------------------------------------------------
func TestG1A_Probe_GrepGlobReachability(t *testing.T) {
	ws := t.TempDir()
	e := NewExecutor(NewRegistry(), ws)
	e.SetSandboxMgr(sandbox.NewProductionSandboxManager(ws))
	e.InitPipelineWithHooks(g1aAllow(t, "glob_search", "grep_search"), ws)

	row := g1aCollect(e, "reachability/glob", "InitPipelineWithHooks+Execute",
		"glob_search", `{"pattern":"*.conf","path":"/etc"}`)
	t.Logf("[G1-A] glob 越界 Output=%.120q IsError=%v kind=%q", row.Output, row.IsError, string(row.Kind))

	if row.Output == "" {
		t.Fatal("前置失败：glob 越界没有产生任何输出，无法判定拦截来源")
	}
	if row.IsError {
		t.Fatalf("前置失败：glob 越界走了 IsError=true（%q）—— 说明它不是 handler 沙箱门，本探针结论无效", row.Output)
	}
	// 判别锚点：输出必须**自证是沙箱拒绝**（而非空结果/其它错误）。
	if !strings.Contains(row.Output, "Sandbox denied") {
		t.Fatalf("前置失败：glob 越界输出不是沙箱拒绝文案（%q）—— 结论无效", row.Output)
	}
}

// g1aWritePlugin 在 <ws>/.whale/plugins/custom-plugin/plugin.json 写一个单工具插件
// （与 internal/plugin/plugin_integration_test.go 的 createCustomPluginDir 同型）。
func g1aWritePlugin(t *testing.T, ws, command string) {
	t.Helper()
	dir := filepath.Join(ws, ".whale", "plugins", "custom-plugin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建插件目录: %v", err)
	}
	manifest := map[string]interface{}{
		"id":      "custom-plugin",
		"name":    "Custom Plugin",
		"version": "1.0.0",
		"tools": []map[string]interface{}{{
			"name":         "custom_tool",
			"description":  "G1-A probe tool",
			"input_schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			"command":      command,
		}},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0o644); err != nil {
		t.Fatalf("写 plugin.json: %v", err)
	}
}
