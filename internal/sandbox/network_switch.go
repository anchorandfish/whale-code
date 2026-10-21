// Package sandbox — 门 3 粗粒度网络开关（v1）。
//
// 默认断网姿态不变；用户显式设置 env WHALE_SANDBOX_ALLOW_NETWORK=1/true/yes/on
// 时放行沙箱内网络（pip install / go mod download 类操作）：
//   - Linux：NewManager 归一化过滤 "net" namespace（不创建 CLONE_NEWNET）
//   - macOS：Seatbelt profile 显式 (allow network*) 替代 (deny network*)
//     （注意：(deny default) 姿态下仅省略 deny 规则网络仍被拒，必须显式 allow）
//   - 应用层（Layer 2/2.5）：NewSandbox 同步消费 env——networkIsolated=false
//   - 默认 deny 列表移除 curl/wget（口径一致性，见 sandbox.go NewSandbox）
//   - Windows：无操作（Job Object 本就不隔离网络，GAP-WIN-P3；
//     不宣称 network=on——宣称即说谎，默认姿态也从未断网）
//
// 单一事实源：env 解析收敛在 networkAllowedByEnv；OS 级开关经 NewManager
// 归一化，应用层经 NewSandbox，cloneflags / profile / status banner /
// 应用层校验全部读同一 env。
package sandbox

import (
	"fmt"
	"os"
	"strings"
)

// networkAllowedByEnv 解析 WHALE_SANDBOX_ALLOW_NETWORK。
//
// 值域：1/true/yes/on（大小写不敏感）→ true；其余（含未设置/空/垃圾值）→ false。
// fail-closed：拼写错误不静默放行网络；非空未识别值打 canary 到真实终端
// stderr（fd 2 直写，绕过 main.go 的 os.Stderr 日志重定向——canary 走变量
// 会被吞进日志文件，静默防护教训：无出口的配置忽略不可见）。
func networkAllowedByEnv() bool {
	raw := strings.TrimSpace(os.Getenv("WHALE_SANDBOX_ALLOW_NETWORK"))
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	case "":
		return false
	default:
		fmt.Fprintf(stderrDirect(),
			"[sandbox] WHALE_SANDBOX_ALLOW_NETWORK=%q not recognized (expected 1/true/yes/on); keeping network isolated\n", raw)
		return false
	}
}

// filterNetNamespace 从 namespaces 中移除 "net"（门 3：AllowNetwork=true 时调用）。
//
// 返回新切片（不修改调用方底层数组）；不含 "net" 时原样返回。
func filterNetNamespace(namespaces []string) []string {
	hasNet := false
	for _, ns := range namespaces {
		if ns == "net" {
			hasNet = true
			break
		}
	}
	if !hasNet {
		return namespaces
	}
	filtered := make([]string, 0, len(namespaces))
	for _, ns := range namespaces {
		if ns != "net" {
			filtered = append(filtered, ns)
		}
	}
	return filtered
}
