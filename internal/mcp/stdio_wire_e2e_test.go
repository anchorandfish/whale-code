package mcp_test

// S6/G8 Patch ② —— 真 server 端到端（**走生产入口**）+ 两个方向各一个负向反例。
//
// 为什么必须有它：internal/mcp 的既有 MCP 测试全部用 whale 自己的帧实现去喂 whale
// 自己的帧实现（mock 与实现**同错**）⇒ 对「线协议是否符合 MCP spec」零判别力 ——
// 缺陷态下 `go test ./internal/mcp/` 实测全绿（G-REBUILD_STATUS.md §3.1）。
// 本文件引入一个**独立按 spec 写的对端**（testdata/wire_server.py），并从生产入口
// tools.Executor.DiscoverAndRegisterMCPTools 进入 —— 与
// tui/lifecycle.go:601 / tui/plain.go:78 / tui/bridge.go:291 是**同一条装配路径**。
//
// 一正两反（缺一不可，production-path-tdd R4：反例必须能被打红）：
//   · 正：合规 newline-delimited server ⇒ 注册成功 + 工具真实可调用；
//   · 反 A（--legacy-content-length）：整机 CL 帧 server ⇒ 必须接入失败
//     ⇒ 若有人把 whale 的**写侧**改回 CL 帧，本用例变红（server 能读懂 ⇒ 接入成功）；
//   · 反 B（--legacy-write）：读侧仍收 newline、**写侧**发 CL 帧（body 后无 '\n'）
//     ⇒ 必须接入失败 ⇒ 若有人给读侧加「双格式兼容」，本用例变红（§3.1 决策 A 被推翻）。
//
// 探针前提（**不是产品缺陷**）：夹具在 mcp.json 里用 sandbox_level:"disabled"。
// 本用例判别的是**帧格式**，与 OS 沙箱正交；macOS 下 sandbox-exec 起不来会让
// 「server 没被 spawn」与「帧格式错」混淆归因（Round3 定式，见 G-REBUILD_STATUS.md §3.1）。

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/mcp"
	"github.com/anchorandfish/whale-code/internal/testutil"
	"github.com/anchorandfish/whale-code/internal/tools"
)

const wireServerName = "probe"

func wireServerPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "wire_server.py"))
	if err != nil {
		t.Fatalf("解析夹具路径失败: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("夹具缺失 %s: %v", p, err)
	}
	return p
}

// requirePython3 显式 skip（不静默通过）：夹具是 Python3 标准库脚本，零第三方依赖。
func requirePython3(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用 ⇒ 端到端帧格式判别**未执行**（显式 skip，不静默通过）")
	}
	return p
}

func writeWireMCPConfig(t *testing.T, workDir, serverName string, argv []string) {
	t.Helper()
	dir := filepath.Join(workDir, ".whale")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .whale: %v", err)
	}
	cfg := mcp.Config{Servers: map[string]mcp.ServerConfig{
		serverName: {
			Command: argv[0],
			Args:    argv[1:],
			// 见文件头「探针前提」：与 OS 沙箱正交，隔离被测变量。
			SandboxLevel: "disabled",
		},
	}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal mcp config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), data, 0o644); err != nil {
		t.Fatalf("写入 mcp.json: %v", err)
	}
}

// newWireExecutor 装配生产入口。返回 (executor, registry)。
// HOME 覆盖：loadMCPConfig 也会读 $HOME/.whale ⇒ 隔离本机真实 MCP 配置。
func newWireExecutor(t *testing.T, workDir string) (*tools.Executor, *tools.Registry) {
	t.Helper()
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))
	tools.CloseAllMCPClients()
	t.Cleanup(tools.CloseAllMCPClients)

	reg := tools.NewRegistry()
	return tools.NewExecutor(reg, workDir), reg
}

func containsTool(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestE2E_DiscoverMCPTools_NewlineDelimited 正向：合规 server 必须被接入且工具可调用。
func TestE2E_DiscoverMCPTools_NewlineDelimited(t *testing.T) {
	py := requirePython3(t)
	workDir := t.TempDir()
	writeWireMCPConfig(t, workDir, wireServerName, []string{py, wireServerPath(t)})

	e, reg := newWireExecutor(t, workDir)

	if err := e.DiscoverAndRegisterMCPTools(workDir); err != nil {
		t.Fatalf("生产入口 DiscoverAndRegisterMCPTools 失败 —— 合规 newline-delimited server 未被接受: %v", err)
	}

	qualified := mcp.BuildMcpToolName(wireServerName, "echo")
	if got := reg.ListMCPServers(); !containsTool(got, qualified) {
		t.Fatalf("未注册工具 %q；ListMCPServers=%v", qualified, got)
	}
	if server, tool, ok := mcp.ParseMcpToolName(qualified); !ok || server != wireServerName || tool != "echo" {
		t.Fatalf("工具名不符合 deny 契约（mcp__<server>__<tool>）: %q", qualified)
	}

	// 真实调用一次：Executor.Execute → handler → client.CallTool → 同一条 stdio 通道。
	res := e.Execute(context.Background(), tools.Call{
		ID:    "s6-e2e-1",
		Name:  qualified,
		Input: `{"text":"hello-s6"}`,
	})
	if res.IsError {
		t.Fatalf("已注册的 MCP 工具调用失败: output=%q failureKind=%q", res.Output, res.FailureKind)
	}
	if !strings.Contains(res.Output, "hello-s6") {
		t.Fatalf("调用返回文本不符（期望含 echo 回来的 hello-s6）: %q", res.Output)
	}
}

// TestE2E_LegacyReadWriteServer_MustFail 负向反例 A：整机 CL 帧 server 必须接入失败。
//
// 判别性：把 whale 的**写侧**（writeMessage）改回 CL 帧 ⇒ 本 server 能读懂请求并应答
// ⇒ 工具被注册 ⇒ 本用例变红。
func TestE2E_LegacyReadWriteServer_MustFail(t *testing.T) {
	py := requirePython3(t)
	workDir := t.TempDir()
	writeWireMCPConfig(t, workDir, wireServerName,
		[]string{py, wireServerPath(t), "--legacy-content-length"})

	e, reg := newWireExecutor(t, workDir)

	err := e.DiscoverAndRegisterMCPTools(workDir)
	got := reg.ListMCPServers()
	qualified := mcp.BuildMcpToolName(wireServerName, "echo")

	if containsTool(got, qualified) {
		t.Fatalf("不合规的 Content-Length server 竟被成功接入（err=%v, tools=%v）"+
			"⇒ 写侧很可能未被改成 newline-delimited", err, got)
	}
	t.Logf("负向反例 A 成立：CL 帧 server 未被接入（err=%v, tools=%v）", err, got)
}

// TestE2E_LegacyWriteOnlyServer_MustFail 负向反例 B：只判别**读侧**是否做了双格式兼容。
//
// server 读 newline（能听懂 whale 的请求）、写 CL 头帧且 body 后不带 '\n'（LSP 真实形态，旧 whale 的格式）。
//
//	· newline-only 读侧：读不到分隔符 ⇒ initialize 超时 ⇒ 接入失败（期望）；
//	· 「双格式兼容读」读侧：解析 CL 头 + ReadFull ⇒ 接入成功 ⇒ 本用例变红。
func TestE2E_LegacyWriteOnlyServer_MustFail(t *testing.T) {
	py := requirePython3(t)
	workDir := t.TempDir()
	writeWireMCPConfig(t, workDir, wireServerName,
		[]string{py, wireServerPath(t), "--legacy-write"})

	e, reg := newWireExecutor(t, workDir)

	err := e.DiscoverAndRegisterMCPTools(workDir)
	got := reg.ListMCPServers()
	qualified := mcp.BuildMcpToolName(wireServerName, "echo")

	if containsTool(got, qualified) {
		t.Fatalf("读侧接受了 CL 头帧（server 只写 CL、whale 却读懂了）⇒ err=%v tools=%v"+
			"⇒ 读侧做了「双格式兼容」，违反 §3.1 决策 A（单格式）", err, got)
	}
	t.Logf("负向反例 B 成立：只写 CL 帧的 server 未被接入（err=%v, tools=%v）", err, got)
}
