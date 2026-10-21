package bridge

import (
	"fmt"

	"github.com/anchorandfish/whale-code/internal/security"
)

// CommandGuard 是 IDE 命令校验的桥接器，委托 H2 security.CheckCommand（P1-5，02 §2.3.1 契约）。
// H7 §6.3 确认"H2 为 bridge 预留 hook，默认不激活，N10 深度对标时统一启用"。
// 适配 H2 实际签名：security.CheckCommand(cmd string) Decision，Decision.Action 为 Allow/Deny/Ask。
type CommandGuard struct {
	enabled bool // 是否启用（默认 false，bridge opt-in 时才激活）
}

// NewCommandGuard 创建命令校验桥接器。
func NewCommandGuard(enabled bool) *CommandGuard {
	return &CommandGuard{enabled: enabled}
}

// CheckCommand 校验 IDE 下发的命令是否允许执行（委托 H2）。
// enabled=false 时直接放行（向后兼容，bridge 默认禁用时无校验）。
// enabled=true 时委托 security.CheckCommand(cmd) 返回 Decision（v2 R7：对齐 02 契约签名）。
// Decision.Action != Allow 时返回错误（带 N6 安全类 ErrorID，由 H2 Decision.Err 携带）。
// Ask 动作按 fail-closed 处理（IDE 命令需明确允许，不接受"需确认"）。
func (g *CommandGuard) CheckCommand(cmd string) error {
	if !g.enabled {
		return nil // 未启用，放行
	}
	// 委托 H2 security.CheckCommand（02 §2.3.1 契约）
	decision := security.CheckCommand(cmd)
	if decision.Action != security.Allow {
		// 拦截：Deny 返回 H2 已包装的 ErrSecurityBlocked/ErrSecuritySandbox；
		// Ask 按 fail-closed 返回错误（IDE 命令不接受"需确认"）
		if decision.Err != nil {
			return decision.Err
		}
		return fmt.Errorf("command blocked by H2: %s", decision.Reason)
	}
	return nil
}
