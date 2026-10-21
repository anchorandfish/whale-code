package tools

// 工具执行MCP域 · Fix 验证测试（G1/I8 + ④/I1/I8）。
//
// 生产入口：Executor.DiscoverAndRegisterMCPTools —— 正是
// tui/lifecycle.go:517 / tui/plain.go:78 / tui/bridge.go:291
// 在启动时调用的那条路径。测试从该入口进入，断言三件事：
//
//	(A) I8：沙箱隔离在生产路径上真正生效（Prepare 被调用 + Restrict 收到沙箱 env，
// 且宿主密钥未泄漏）—— 修复前 getMCPClientForWorkDir 传 nil → Prepare 从不被调用。
//	(B) ④：注册的工具名 = 全限定名 mcp__<server>__<tool>，可被 deny 契约匹配
// —— 修复前是单下划线 mcp_<server>_<tool>（deny 规则静默命中不了）。
//
// 用真实的 stdio MCP server 子进程（临时编译）保证走的是与生产相同的传输/启动链路，
// 而不是手工拼出来的假路径（production-path-tdd R2）。

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/mcp"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// auditMCPProbe 记录沙箱管理器在生产路径上被调用的情况。
type auditMCPProbe struct {
	prepareCalls int32
	restricted   int32
	restrictEnv  []string
}

func (p *auditMCPProbe) Prepare(_ context.Context, _ sandbox.SandboxLevel, workDir string) (sandbox.SandboxContext, func(), error) {
	atomic.AddInt32(&p.prepareCalls, 1)
	// 故意返回一个「不含宿主密钥」的最小 env，以便断言 Restrict 看到的是沙箱 env，
	// 而不是 os.Environ()（密钥泄漏与否由此可判定）。
	return sandbox.NewSandboxContextForTesting(workDir, []string{"PATH=/usr/bin:/bin", "WHALE_MCP_AUDIT_SANDBOX_ENV=1"}, &auditMCPOSSandbox{probe: p}), func() {}, nil
}

func (p *auditMCPProbe) ValidateCommand(_ context.Context, _ string, _ *sandbox.SandboxConfig) (*sandbox.ValidationResult, error) {
	return &sandbox.ValidationResult{Blocked: false}, nil
}

func (p *auditMCPProbe) ValidatePath(_ context.Context, _ string, _ *sandbox.SandboxConfig) error {
	return nil
}

func (p *auditMCPProbe) Level() sandbox.SandboxLevel { return sandbox.SandboxLevelRelaxed }

// auditMCPOSSandbox 捕获 Restrict 收到的 *exec.Cmd（含其 Env）。
type auditMCPOSSandbox struct{ probe *auditMCPProbe }

func (o *auditMCPOSSandbox) Enter() (interface{}, func(), error) { return nil, func() {}, nil }
func (o *auditMCPOSSandbox) Exit() error                         { return nil }
func (o *auditMCPOSSandbox) PostStartHook() func(*exec.Cmd) error {
	return nil
}
func (o *auditMCPOSSandbox) Platform() string { return "audit-fake" }
func (o *auditMCPOSSandbox) Restrict(cmd *exec.Cmd) error {
	atomic.StoreInt32(&o.probe.restricted, 1)
	o.probe.restrictEnv = append([]string(nil), cmd.Env...)
	return nil
}

// auditMockMCPSource 是一个最小 stdio MCP server：initialize + tools/list(echo) + tools/call。
// 使用 map[string]json.RawMessage 解析以避开结构体 tag 反引号（外层用 Go raw string）。
const auditMockMCPSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func main() {
	//  探针：若传入 W062_DUMP_ENV，把本进程的完整 env 落盘，
	// 供父测试断言"去沙箱后仍剥密钥、但保留其余"。无该变量时零副作用。
	if dump := os.Getenv("W062_DUMP_ENV"); dump != "" {
		_ = os.WriteFile(dump, []byte(strings.Join(os.Environ(), "\n")), 0o644)
	}
	r := bufio.NewReader(os.Stdin)
	w := os.Stdout
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		// newline-delimited：整行即一帧 JSON（MCP spec 2024-11-05 §Transports/stdio）
		buf := []byte(line)
		var req map[string]json.RawMessage
		if err := json.Unmarshal(buf, &req); err != nil {
			continue
		}
		idRaw, ok := req["id"]
		if !ok || string(idRaw) == "null" {
			continue
		}
		var method string
		json.Unmarshal(req["method"], &method)
		var result interface{}
		switch method {
		case "initialize":
			result = map[string]interface{}{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
				"serverInfo":      map[string]string{"name": "audit-mock", "version": "0.0.1"},
			}
		case "tools/list":
			result = map[string]interface{}{
				"tools": []map[string]interface{}{
					{"name": "echo", "description": "echo", "inputSchema": map[string]interface{}{"type": "object"}},
				},
			}
		case "tools/call":
			result = map[string]interface{}{"content": []map[string]interface{}{{"type": "text", "text": "ok"}}}
		default:
			result = map[string]interface{}{}
		}
		rj, _ := json.Marshal(result)
		resp := "{\"jsonrpc\":\"2.0\",\"id\":" + string(idRaw) + ",\"result\":" + string(rj) + "}"
		fmt.Fprintf(w, "%s\n", resp)
	}
}
`

// buildAuditMockMCP 编译最小 MCP server，返回可执行文件路径。
func buildAuditMockMCP(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "audit_mock_mcp.go")
	if err := os.WriteFile(src, []byte(auditMockMCPSource), 0o644); err != nil {
		t.Fatalf("write mock server source: %v", err)
	}
	exe := filepath.Join(dir, "audit_mock_mcp")
	cmd := exec.Command("go", "build", "-o", exe, src)
	// 独立文件、无外部依赖：关闭 module/vendor 干扰，保证在测试进程环境里也能编译。
	cmd.Env = append(os.Environ(), "GO111MODULE=off", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build mock MCP server: %v\n%s", err, string(out))
	}
	return exe
}

// writeAuditMCPConfig 在 workDir/.whale/mcp.json 写入一个 server（生产 loadMCPConfig 的真实读取点）。
//
// sandboxLevel 显式写入 ServerConfig.SandboxLevel（起默认 Disabled；
// 想验沙箱路径的用例必须显式传 "relaxed"）。extraEnv 用于给 server 传探针参数
// （经 ServerConfig.Env，追加在过滤后的 env 之后）。
func writeAuditMCPConfig(t *testing.T, workDir, serverName, command, sandboxLevel string, extraEnv map[string]string) {
	t.Helper()
	dir := filepath.Join(workDir, ".whale")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .whale: %v", err)
	}
	cfg := mcp.Config{Servers: map[string]mcp.ServerConfig{
		serverName: {Command: command, SandboxLevel: sandboxLevel, Env: extraEnv},
	}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal mcp config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), data, 0o644); err != nil {
		t.Fatalf("write mcp.json: %v", err)
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestAudit_MCP_SandboxEngagedAndQualifiedName 是 G1/I8 与 ④/I1/I8 的生产路径验证。
//
// ★ 后本用例**显式**要求沙箱（`sandbox_level:"relaxed"`）：
// 默认已改为去沙箱 ⇒ "沙箱在生产路径上生效"不再是默认行为，只在显式 opt-in 时成立。
// 保留原意（验逃生舱：显式要沙箱时真被 Prepare+Restrict），另见
// TestAudit_MCP_DefaultUnsandboxed_KeepsSecretFilter 验新的默认行为。
func TestAudit_MCP_SandboxEngagedAndQualifiedName(t *testing.T) {
	// 宿主里放一个"密钥"哨兵，用于证明沙箱 env 已替换宿主 env（未泄漏）。
	t.Setenv("WHALE_MCP_AUDIT_SECRET", "must-not-leak")

	exe := buildAuditMockMCP(t)
	workDir := t.TempDir()
	const serverName = "auditprobe"
	writeAuditMCPConfig(t, workDir, serverName, exe, "relaxed", nil)

	probe := &auditMCPProbe{}
	e := &Executor{
		handlers: make(map[string]Handler),
		registry: NewRegistry(),
		workDir:  workDir,
	}
	e.SetSandboxMgr(probe)

	// 全局 client 缓存按 server 名索引：确保测试隔离 + 结束后清理（含杀子进程）。
	CloseAllMCPClients()
	defer CloseAllMCPClients()

	if err := e.DiscoverAndRegisterMCPTools(workDir); err != nil {
		t.Fatalf("DiscoverAndRegisterMCPTools: %v", err)
	}

	// ---- 断言 A（I8）：沙箱在生产路径上生效 ----
	if got := atomic.LoadInt32(&probe.prepareCalls); got == 0 {
		t.Fatalf("I8 GAP: 生产路径未调用 sandbox.Prepare（MCP server 在无沙箱下启动）")
	}
	if atomic.LoadInt32(&probe.restricted) == 0 {
		t.Fatalf("I8 GAP: 生产路径未对 MCP server 子进程调用 OSSandbox.Restrict（无 OS 级隔离）")
	}
	if len(probe.restrictEnv) == 0 {
		t.Fatalf("I8 GAP: Restrict 未观察到 cmd.Env")
	}
	for _, kv := range probe.restrictEnv {
		if strings.HasPrefix(kv, "WHALE_MCP_AUDIT_SECRET=") {
			t.Fatalf("I8 GAP: MCP server 子进程泄漏了宿主密钥（env 含 %q）——说明未走沙箱 env", kv)
		}
	}
	if !containsStr(probe.restrictEnv, "WHALE_MCP_AUDIT_SANDBOX_ENV=1") {
		t.Fatalf("I8: 子进程 env 未来自沙箱 Prepare 返回值；got=%v", probe.restrictEnv)
	}

	// ---- 断言 B（④）：注册名 = 全限定名，与 deny 契约一致 ----
	qualified := mcp.BuildMcpToolName(serverName, "echo")
	registered := e.registry.ListMCPServers()
	if !containsStr(registered, qualified) {
		t.Fatalf("④ GAP: 未按全限定名注册；want %q, got %v", qualified, registered)
	}
	legacy := "mcp_" + serverName + "_echo"
	if containsStr(registered, legacy) {
		t.Fatalf("④ GAP: 仍以单下划线旧名注册 %q（deny 契约 mcp__server 命中不了）", legacy)
	}

	// ---- 断言 C（I1/I8 deny 契约）：按文档写的 deny 规则能命中外部 MCP 工具 ----
	pol := permission.NewPolicy(permission.LevelWorkspaceWrite)
	if err := pol.AddRule(permission.PermissionRule{
		ToolName: "mcp__" + serverName, // 文档形式：按 server 前缀 deny
		Behavior: permission.RuleDeny,
		Source:   permission.SourceUserSettings,
	}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if allowed, _ := pol.Check(qualified, ""); allowed {
		t.Fatalf("I1/I8 GAP: deny 规则 %q 未命中注册名 %q（外部工具无法被 deny）", "mcp__"+serverName, qualified)
	}
	// 反例（为什么要"对齐"而不是"两种格式都兼容"）：旧单下划线名**不被该规则**命中。
	//
	// ⚠️ 这里不能再用 pol.Check(legacy) 表达：legacy 不是已登记工具，L1起
	// 未登记工具 fail-closed 到最高档，Check 会**因另一个原因**返回拒绝 —— 断言会退化成
	// 恒真、失去判别力（本次即因此发现原写法与 L1 冲突）。故下沉到规则匹配层，
	// 钉住真正关心的性质：deny 规则 `mcp__<server>` 的匹配单位是**双下划线分段的全限定名**。
	rs := permission.NewRuleSet()
	rs.Add(permission.PermissionRule{
		ToolName: "mcp__" + serverName,
		Behavior: permission.RuleDeny,
		Source:   permission.SourceUserSettings,
	})
	if got := rs.GetMatchingRules(qualified, ""); len(got) == 0 {
		t.Fatalf("I1/I8 GAP: 规则层未把 %q 与全限定名 %q 视为同一 server 段", "mcp__"+serverName, qualified)
	}
	if got := rs.GetMatchingRules(legacy, ""); len(got) != 0 {
		t.Fatalf("前置假设不成立：旧命名 %q 竟被规则 %q 命中，说明契约匹配逻辑与预期不同", legacy, "mcp__"+serverName)
	}
}

// waitReadFile 轮询读文件，直到出现或超时（子进程在 main 开头落盘，通常即时）。
func waitReadFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return string(b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("10s 内未等到 MCP server 落盘的 env 转储：%s", path)
	return ""
}

// TestAudit_MCP_DefaultUnsandboxed_KeepsSecretFilter 是 的生产路径验证（新默认行为）。
//
// 从生产入口 DiscoverAndRegisterMCPTools 进入，server 配置**不写 sandbox_level**（用默认）：
//
//	(A) 去沙箱：不 Prepare、不 Restrict（联网型 server 才能开箱可用）；
//	(B) 但仍剥密钥：子进程 env 不含宿主密钥哨兵；
//	(C) 但不缩到最小集：非白名单的普通变量必须保留（证明没用沙箱窄白名单）。
//
// 判别性：把 parseSandboxLevel("") 改回 Relaxed（或 Disabled 分支回退 os.Environ()）都会打红。
func TestAudit_MCP_DefaultUnsandboxed_KeepsSecretFilter(t *testing.T) {
	t.Setenv("WHALE_MCP_AUDIT_SECRET", "must-not-leak") // 命中 *_SECRET ⇒ 应被剥
	t.Setenv("WHALE_MCP_AUDIT_PUBLIC", "keep-me")       // 非密钥、非白名单 ⇒ 应保留

	exe := buildAuditMockMCP(t)
	workDir := t.TempDir()
	const serverName = "w062probe"
	dumpPath := filepath.Join(workDir, "child_env.txt")
	// 默认：SandboxLevel 留空 ⇒ 应走 Disabled（去沙箱）。
	writeAuditMCPConfig(t, workDir, serverName, exe, "", map[string]string{"W062_DUMP_ENV": dumpPath})

	probe := &auditMCPProbe{}
	e := &Executor{
		handlers: make(map[string]Handler),
		registry: NewRegistry(),
		workDir:  workDir,
	}
	e.SetSandboxMgr(probe)

	CloseAllMCPClients()
	defer CloseAllMCPClients()

	if err := e.DiscoverAndRegisterMCPTools(workDir); err != nil {
		t.Fatalf("DiscoverAndRegisterMCPTools: %v", err)
	}

	// (A) 去沙箱
	if got := atomic.LoadInt32(&probe.prepareCalls); got != 0 {
		t.Fatalf(" GAP: 默认配置下仍调用 sandbox.Prepare %d 次（应 0，去沙箱）", got)
	}
	if got := atomic.LoadInt32(&probe.restricted); got != 0 {
		t.Fatalf(" GAP: 默认配置下仍调用 OSSandbox.Restrict %d 次（应 0，去沙箱）", got)
	}

	dump := waitReadFile(t, dumpPath)
	// (B) 密钥仍被剥
	if strings.Contains(dump, "WHALE_MCP_AUDIT_SECRET=") {
		t.Fatalf(" GAP: 去沙箱后宿主密钥泄漏进 MCP server env（含 WHALE_MCP_AUDIT_SECRET）")
	}
	// (C) 普通变量保留（证明是"只剥不缩"，不是沙箱最小集）
	if !strings.Contains(dump, "WHALE_MCP_AUDIT_PUBLIC=keep-me") {
		t.Fatalf(" GAP: 非白名单普通变量 WHALE_MCP_AUDIT_PUBLIC 被误剥 —— env 被缩成沙箱最小集，联网/构建会失败\n子进程 env 转储：\n%s", dump)
	}
}
