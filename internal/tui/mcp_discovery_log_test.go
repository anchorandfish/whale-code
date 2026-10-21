package tui

// S6/G8 Patch ③ 阶段一 —— discovery 失败落日志的判别性验证。
//
// 一结构 + 一行为（缺一不可）：
//   · TestGuard_MCPDiscoveryNoBareGoStatement —— AST 解析**三个生产文件**，断言不再存在
//     `go <expr>.DiscoverAndRegisterMCPTools(...)` 这种丢弃 error 的裸 go 语句。
//     判别性：把 lifecycle.go / bridge.go / plain.go **任意一处**改回旧写法 ⇒ 必红。
//   · TestMCPDiscoveryFailureIsLogged —— 走**生产入口 NewModel**，注入一个必然连不上的
//     MCP server，断言日志里出现 `[mcp] tool discovery failed`。
//     判别性：把 ③ 改回 `go …`（丢 error）⇒ 日志行消失 ⇒ 必红。
//
// ⚠️ 观测面说明（production-path-tdd R4-8）：两条判据都在**运行期读磁盘 / 捕获运行时输出**，
// 因此 `-overlay` 对它们**物理无效**，判别性必须用「写回 + 无条件还原 + md5」证明（见回执）。
//
// ⚠️ 诚实边界：本阶段只证明"**事后查得到**"（日志里有），**不证明"用户看得见"** ——
// fd 2 直写的启动提示会被 TUI alt-screen 覆盖，且 discovery 是异步的（等 initialize 超时）
// ⇒ 必然晚于切换点。"会话期间可见"须走 TUI 消息通道，与 S7 §7.2 合并决策。

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/safebuf"
)

// TestGuard_MCPDiscoveryNoBareGoStatement ③ 的结构判据。
func TestGuard_MCPDiscoveryNoBareGoStatement(t *testing.T) {
	files := []string{"lifecycle.go", "bridge.go", "plain.go"}

	parsed := 0
	var offenders []string

	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读取生产文件 %s 失败: %v", f, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", f, err)
		}
		parsed++
		ast.Inspect(file, func(n ast.Node) bool {
			gs, ok := n.(*ast.GoStmt)
			if !ok {
				return true
			}
			sel, ok := gs.Call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == "DiscoverAndRegisterMCPTools" {
				offenders = append(offenders, fset.Position(gs.Pos()).String())
			}
			return true
		})
	}

	// 先钉"目标文件确实被解析到" —— 否则改名/移动会让本守卫静默失真（恒绿）。
	if parsed != len(files) {
		t.Fatalf("仅解析到 %d/%d 个生产文件，守卫失真", parsed, len(files))
	}
	if len(offenders) > 0 {
		t.Fatalf("仍有丢弃 error 的裸 go 语句 DiscoverAndRegisterMCPTools（③ 被回退或漏改）: %v", offenders)
	}
}

// TestMCPDiscoveryFailureIsLogged ③ 的行为判据（走生产入口）。
func TestMCPDiscoveryFailureIsLogged(t *testing.T) {
	t.Setenv("HOME", newIsolatedTestHome(t)) // 隔离：loadMCPConfig 也会读 $HOME/.whale/mcp.json

	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, ".whale"), 0o755); err != nil {
		t.Fatalf("mkdir .whale: %v", err)
	}
	// 必然连不上的 server：command 指向不存在的可执行文件 ⇒ spawn 失败 ⇒ discovery 返回 error。
	cfg := map[string]any{"servers": map[string]any{
		"broken": map[string]any{
			"command":       filepath.Join(workDir, "definitely-not-a-real-mcp-server"),
			"sandbox_level": "disabled",
		},
	}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".whale", "mcp.json"), data, 0o644); err != nil {
		t.Fatalf("write mcp.json: %v", err)
	}
	t.Chdir(workDir) // NewModel 的 workDir = os.Getwd()

	// lifecycle.go 用的是标准库 log（非 debug 模式下 main.go 已把它重定向到 whale.log）。
	// 该缓冲被 discovery goroutine 写、测试 goroutine 轮询读 ⇒ 必须并发安全
	// （旧写法 bytes.Buffer 在 -race 下报 "buf.String vs log.Printf"）。
	var buf safebuf.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	m := NewModel(newTestConfig(), newTestClient("test-model"), newTestRegistry())
	if m.executor == nil {
		t.Fatal("NewModel 未装配 executor（本用例前提不成立，需复核脚手架）")
	}

	const want = "[mcp] tool discovery failed"
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("MCP discovery 失败后 30s 内未出现 %q（③ 被回退？）\n捕获到的日志:\n%s", want, buf.String())
}
