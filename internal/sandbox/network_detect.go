// Package sandbox — M13-03 网络访问检测。
//
// 设计目标：
// - 静态扫描命令字符串，检测潜在的网络访问行为（curl/wget/nc/ssh/scp/rsync 等）
// - 与 NetworkMode 配置（none/proxy/host）协作：none 模式下命中即阻止
// - 不依赖运行时拦截（运行时拦截由 OS 级沙箱的 namespace 隔离完成）
// - 跨平台：纯字符串/正则匹配，无平台依赖
// - CGO_ENABLED=0：纯 Go，仅标准库
//
// 检测维度：
// - 网络工具调用（curl/wget/nc/ncat/socat/ssh/scp/rsync/telnet/ftp/ftpget）
// - 远程 URL（http://, https://, ftp://, scp://, rsync://）
// - 端口绑定（nc -l, socat TCP-LISTEN）
// - 反向 shell 模式（bash -i >& /dev/tcp/...）
//
// 官方源对标：
// - Linux nc(1): https://man7.org/linux/man-pages/man1/nc.1.html @2026-07-21
// - curl(1): https://curl.se/docs/manpage.html @2026-07-21
// - OWASP Command Injection: https://owasp.org/www-community/attacks/Command_Injection @2026-07-21
package sandbox

import (
	"fmt"
	"regexp"
	"strings"
)

// NetworkAccessType 表示网络访问类型（M13-03 SNAPSHOT）。
type NetworkAccessType int

const (
	// NetworkAccessNone 无网络访问。
	NetworkAccessNone NetworkAccessType = iota
	// NetworkAccessOutbound 出站连接（curl/wget/ssh 等）。
	NetworkAccessOutbound
	// NetworkAccessInbound 入站监听（nc -l / socat TCP-LISTEN）。
	NetworkAccessInbound
	// NetworkAccessReverseShell 反向 shell（高危）。
	NetworkAccessReverseShell
)

// String 返回 NetworkAccessType 的字符串表示。
func (n NetworkAccessType) String() string {
	switch n {
	case NetworkAccessNone:
		return "none"
	case NetworkAccessOutbound:
		return "outbound"
	case NetworkAccessInbound:
		return "inbound"
	case NetworkAccessReverseShell:
		return "reverse-shell"
	default:
		return fmt.Sprintf("unknown(%d)", int(n))
	}
}

// NetworkDetectionResult 是网络访问检测结果（M13-03 SNAPSHOT）。
type NetworkDetectionResult struct {
	// AccessType 网络访问类型。
	AccessType NetworkAccessType
	// Tool 命中的网络工具名（如 "curl" / "nc"），未命中为空。
	Tool string
	// Pattern 命中的模式描述（如 "URL: https://example.com"）。
	Pattern string
	// Severity 严重度（0=Low 1=Medium 2=High 3=Critical）。
	Severity int
}

// networkToolRe 匹配常见网络工具调用（M13-03 SNAPSHOT）。
//
// 匹配策略：单词边界 + 命令开头或管道/分号后，避免误报（如 curlinger）。
var networkToolRe = regexp.MustCompile(`(?:^|[|;&\s])(curl|wget|nc|ncat|netcat|socat|ssh|scp|sftp|rsync|telnet|ftp|ftpget|tftp|lynx|w3m)\b`)

// urlRe 匹配 URL（http/https/ftp/scp/rsync）。
var urlRe = regexp.MustCompile(`\b(?:https?|ftp|scp|rsync)://[^\s"'<>()]+`)

// devTcpRe 匹配 /dev/tcp/ 和 /dev/udp/（bash 反向 shell 常用）。
var devTcpRe = regexp.MustCompile(`/dev/(?:tcp|udp)/`)

// reverseShellRe 匹配反向 shell 高危 pattern。
var reverseShellRe = regexp.MustCompile(`(?:>&\s*/dev/(?:tcp|udp)|bash\s+-i\s+>&|sh\s+-i\s+>&|/bin/sh\s+-i)`)

// bindListenRe 匹配 nc/ncat/netcat 的 -l 监听模式（含 -lp / -lv / -lk 等组合短选项）。
var bindListenRe = regexp.MustCompile(`(?:^|[|;&\s])(?:nc|ncat|netcat)\s+(?:[^|;&\s]*\s+)*-l\w*\b`)

// socatListenRe 匹配 socat 的 TCP-LISTEN / UNIX-LISTEN 等监听模式。
var socatListenRe = regexp.MustCompile(`(?:^|[|;&\s])socat\b[^\n]*?\bTCP-LISTEN:`)

// socatListenRe2 匹配 socat 的 SOCKET-LISTEN 等变体。
var socatListenRe2 = regexp.MustCompile(`(?:^|[|;&\s])socat\b[^\n]*?\b(?:UNIX-LISTEN|SOCKET-LISTEN|ABSTRACT-LISTEN):`)

// DetectNetworkAccess 静态扫描命令字符串，检测网络访问行为（M13-03）。
//
// 参数：
// - cmd: 待检测的命令字符串
//
// 返回：
// - result: 检测结果（未命中时 AccessType=NetworkAccessNone）
// - blocked: 是否应阻止（true=NetworkMode=none 时必须阻止）
//
// 阻断策略（与 manager.go 协作）：
// - ReverseShell: Severity=Critical，始终阻止（任何 NetworkMode）
// - Inbound: Severity=High，NetworkMode=none/proxy 时阻止
// - Outbound: Severity=Medium，NetworkMode=none 时阻止
//
// 注：本函数仅做静态字符串扫描，不做运行时拦截。
// 运行时网络隔离由 OS 级沙箱（namespace net 隔离）完成。
func DetectNetworkAccess(cmd string) (result NetworkDetectionResult, blocked bool) {
	// 1. 反向 shell（最高优先级，始终阻止）
	if reverseShellRe.MatchString(cmd) {
		// 提取 /dev/tcp/IP/port 用于报告
		pattern := "reverse-shell pattern"
		if m := devTcpRe.FindString(cmd); m != "" {
			pattern = fmt.Sprintf("reverse shell via %s", m)
		}
		return NetworkDetectionResult{
			AccessType: NetworkAccessReverseShell,
			Tool:       "bash/sh",
			Pattern:    pattern,
			Severity:   SeverityCritical,
		}, true
	}

	// 2. 入站监听（nc -l / socat TCP-LISTEN）
	if m := bindListenRe.FindString(cmd); m != "" {
		tool := strings.TrimSpace(m)
		fields := strings.Fields(tool)
		if len(fields) > 0 {
			tool = fields[0]
		}
		return NetworkDetectionResult{
			AccessType: NetworkAccessInbound,
			Tool:       tool,
			Pattern:    fmt.Sprintf("listen mode: %s", strings.TrimSpace(m)),
			Severity:   SeverityHigh,
		}, true
	}
	// socat TCP-LISTEN:port 形式
	if socatListenRe.MatchString(cmd) || socatListenRe2.MatchString(cmd) {
		return NetworkDetectionResult{
			AccessType: NetworkAccessInbound,
			Tool:       "socat",
			Pattern:    "socat listen mode",
			Severity:   SeverityHigh,
		}, true
	}

	// 3. 出站连接（网络工具调用）
	if m := networkToolRe.FindString(cmd); m != "" {
		tool := strings.TrimSpace(m)
		fields := strings.Fields(tool)
		if len(fields) > 0 {
			tool = fields[0]
		}
		return NetworkDetectionResult{
			AccessType: NetworkAccessOutbound,
			Tool:       tool,
			Pattern:    fmt.Sprintf("network tool: %s", tool),
			Severity:   SeverityMedium,
		}, false
	}

	// 4. URL 出现（无网络工具但有 URL，可能是 wget/curl 的间接调用）
	if m := urlRe.FindString(cmd); m != "" {
		return NetworkDetectionResult{
			AccessType: NetworkAccessOutbound,
			Tool:       "url",
			Pattern:    fmt.Sprintf("URL: %s", m),
			Severity:   SeverityLow,
		}, false
	}

	// 5. /dev/tcp/ 出现但未命中 reverseShellRe（可能是 echo > /dev/tcp/... 这种正向连接）
	if devTcpRe.MatchString(cmd) {
		return NetworkDetectionResult{
			AccessType: NetworkAccessOutbound,
			Tool:       "bash-dev-tcp",
			Pattern:    "bash /dev/tcp/ connection",
			Severity:   SeverityHigh,
		}, true
	}

	return NetworkDetectionResult{
		AccessType: NetworkAccessNone,
	}, false
}

// ShouldBlockByNetworkMode 根据 NetworkMode 判断检测结果是否应阻止（M13-03）。
//
// 参数：
// - result: DetectNetworkAccess 的返回值
// - networkMode: SandboxConfig.NetworkMode（"none" / "proxy" / "host" / "" 视为 host）
//
// 阻断规则：
// - "none": ReverseShell/Inbound/Outbound 都阻止
// - "proxy": ReverseShell/Inbound 阻止，Outbound 不阻止（由代理过滤）
// - "host"/"": 仅 ReverseShell 阻止（开发环境友好）
//
// 注：result.Severity=Critical 时无视 networkMode 始终阻止。
func ShouldBlockByNetworkMode(result NetworkDetectionResult, networkMode string) bool {
	mode := strings.ToLower(strings.TrimSpace(networkMode))
	if mode == "" {
		mode = "host"
	}

	// Critical 始终阻止
	if result.Severity >= SeverityCritical {
		return true
	}

	switch result.AccessType {
	case NetworkAccessReverseShell:
		return true // 始终
	case NetworkAccessInbound:
		// none/proxy 阻止，host 放行
		return mode == "none" || mode == "proxy"
	case NetworkAccessOutbound:
		// 仅 none 阻止
		return mode == "none"
	default:
		return false
	}
}
