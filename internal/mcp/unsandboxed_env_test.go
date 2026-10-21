package mcp

// · MCP 默认去沙箱（对齐 同类 CLI）—— 红/绿判别器。
//
// 背景：whale 的 MCP stdio server 过去**默认套 OS 沙箱**（`parseSandboxLevel("")`
// → `SandboxLevelRelaxed` ⇒ `sbMgr.Prepare` + `OSSandbox.Restrict`）。后果是
// 联网型 server（检索 / GitHub / 远程库）开箱即死，且在 TUI 面板点"允许"也放行不了
// 。
//
// 拍板口径（对齐 同类 CLI）：**去沙箱 ＋ 留审批 ＋ 留密钥过滤**。本文件只钉前两点的
// 环境面（审批层在 internal/permission，本改动一行未动）：
//
//	① 默认级别 ⇒ Disabled（不再套 OS 沙箱）；
//	② 去沙箱 ≠ 泄漏密钥 —— 子进程 env 仍剥 `*_API_KEY/*_TOKEN/*_SECRET/AWS_*/…`；
//	③ 去沙箱 ≠ 缩到最小集 —— 非白名单变量（代理、语言运行时路径…）必须**保留**，
// 否则"去沙箱了还是跑不起来"。这正是不能复用 DefaultEnvConfig 窄白名单的原因。
//
// 从**生产传输入口** NewStdioTransport 进入（与 Executor.getMCPClientForWorkDir 同一条），
// 直接读 tr.cmd.Env（与 mcp_char_test.go 同一取证手法）。

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// w062ProbeManager 只记录 Prepare 是否被调用；无需真的隔离。
type w062ProbeManager struct{ prepareCalls int32 }

func (p *w062ProbeManager) Prepare(_ context.Context, _ sandbox.SandboxLevel, workDir string) (sandbox.SandboxContext, func(), error) {
	atomic.AddInt32(&p.prepareCalls, 1)
	// 故意返回"最小沙箱 env"（与 DefaultEnvConfig 白名单同形）：若默认走了这条路，
	// 非白名单变量会消失 ⇒ 判别器据此变红。
	return sandbox.NewSandboxContextForTesting(workDir, []string{"PATH=/usr/bin:/bin", "W062_FROM_SANDBOX_ENV=1"}, nil), func() {}, nil
}

func (p *w062ProbeManager) ValidateCommand(_ context.Context, _ string, _ *sandbox.SandboxConfig) (*sandbox.ValidationResult, error) {
	return &sandbox.ValidationResult{Blocked: false}, nil
}
func (p *w062ProbeManager) ValidatePath(_ context.Context, _ string, _ *sandbox.SandboxConfig) error {
	return nil
}
func (p *w062ProbeManager) Level() sandbox.SandboxLevel { return sandbox.SandboxLevelRelaxed }

func envHas(list []string, prefix string) bool {
	for _, kv := range list {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

// TestMCP_DefaultUnsandboxed_StripsSecretsKeepsRest 钉住 的默认行为。
//
// before（现状）：parseSandboxLevel("") == Relaxed ⇒ Prepare 被调用、env = 最小沙箱 env。
// after（期望）：parseSandboxLevel("") == Disabled ⇒ Prepare 不被调用；env = 宿主 env 剥密钥。
func TestMCP_DefaultUnsandboxed_StripsSecretsKeepsRest(t *testing.T) {
	// 宿主里放两个哨兵：一个密钥类（应被剥），一个普通类（应保留）。
	t.Setenv("W062_HOST_API_KEY", "must-not-leak")
	t.Setenv("W062_HOST_PUBLIC", "keep-me")

	// ① 默认级别必须是 Disabled（去沙箱）。
	level := parseSandboxLevel("")
	if level != sandbox.SandboxLevelDisabled {
		t.Fatalf(" GAP: 默认级别应为 Disabled（去沙箱），实际 %v —— 联网型 server 仍会被沙箱断网", level)
	}

	probe := &w062ProbeManager{}
	tr, err := NewStdioTransport(context.Background(), "true", nil, nil, probe, level)
	if err != nil {
		t.Skipf("无法在本机启动 'true' 做 env 探针，跳过: %v", err)
	}
	defer func() { _ = tr.Close() }()

	// ② 不得调用 Prepare（没有任何 OS 沙箱准备）。
	if got := atomic.LoadInt32(&probe.prepareCalls); got != 0 {
		t.Fatalf(" GAP: 去沙箱路径仍调用了 sandbox.Prepare %d 次（应 0）", got)
	}
	// ③ 密钥必须仍被剥。
	if envHas(tr.cmd.Env, "W062_HOST_API_KEY=") {
		t.Fatalf(" GAP: 去沙箱后宿主密钥泄漏进子进程 env（含 W062_HOST_API_KEY）")
	}
	// ④ 非白名单的普通变量必须保留（证明没有缩到沙箱最小集）。
	if !envHas(tr.cmd.Env, "W062_HOST_PUBLIC=keep-me") {
		t.Fatalf(" GAP: 普通变量 W062_HOST_PUBLIC 被误剥 —— env 被缩成沙箱最小集（联网/构建会失败）")
	}
	// ⑤ 不得出现沙箱路径的痕迹（防止"误走 Prepare 但恰好看不出"）。
	if envHas(tr.cmd.Env, "W062_FROM_SANDBOX_ENV=") {
		t.Fatalf(" GAP: 子进程 env 来自沙箱 Prepare 返回值（含 W062_FROM_SANDBOX_ENV）")
	}
}

// TestMCP_ExplicitRelaxed_StillSandboxes 是逃生舱：显式写 sandbox_level 仍应回到沙箱内。
//
// 钉住"翻转默认值 ≠ 拆掉能力"：显式 relaxed ⇒ Prepare 被调用。
func TestMCP_ExplicitRelaxed_StillSandboxes(t *testing.T) {
	level := parseSandboxLevel("relaxed")
	if level != sandbox.SandboxLevelRelaxed {
		t.Fatalf("显式 relaxed 应解析为 SandboxLevelRelaxed，实际 %v", level)
	}
	probe := &w062ProbeManager{}
	tr, err := NewStdioTransport(context.Background(), "true", nil, nil, probe, level)
	if err != nil {
		t.Skipf("无法在本机启动 'true' 做 env 探针，跳过: %v", err)
	}
	defer func() { _ = tr.Close() }()

	if got := atomic.LoadInt32(&probe.prepareCalls); got == 0 {
		t.Fatalf("显式 relaxed 应仍走沙箱路径（Prepare 被调用），实际 0 次")
	}
	if !envHas(tr.cmd.Env, "W062_FROM_SANDBOX_ENV=1") {
		t.Fatalf("显式 relaxed 时子进程 env 应来自 Prepare 返回值，实际未观察到")
	}
}
