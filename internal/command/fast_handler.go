// Package command — 命令注册与处理。
// fast_handler.go — /fast 命令的完整实现。
// 契约：02 §2.5 命令注册；02 §2.3.2 错误走 N6；02 §2.3.1 校验委托 H2。
// 对标同类 CLI fast/index.ts + fastMode.ts。
// M9 修订：轻量冷却机制（参考同类实现 简化，固定 60 秒，不实现指数退避）。
package command

import (
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/util"
)

// handleFastCommand 处理 /fast 命令。
// 契约：02 §2.5 命令注册；02 §2.3.2 错误走 N6（ai.WithID + ErrSecurityBlocked/ErrConfigInvalid）；
// 02 §2.3.1 校验委托 H2（H2 未合入时 OnSecurityCheck 为 nil 跳过）。
// 行为：
//   - 无参或 status：显示当前 fast mode 状态（on/off/cooldown）
//   - on：开启 fast mode，切换到快速模型
//   - off：关闭 fast mode，切换回默认模型
//   - M6：切换时 session 上下文保留，仅影响后续请求
func (r *Registry) handleFastCommand(args string) (string, error) {
	// 1. 安全校验（委托 H2，02 §2.3.1）
	if r.OnSecurityCheck != nil {
		if decision := r.OnSecurityCheck("fast " + args); decision == "block" {
			return "", ai.WithID(fmt.Errorf("command blocked by security policy"), ai.ErrSecurityBlocked)
		}
	}

	args = strings.TrimSpace(strings.ToLower(args))

	// 2. 无参数或 status：显示当前状态
	if args == "" || args == "status" {
		state := config.FastModeOff
		fastModel := ""
		defaultModel := ""
		if r.OnGetFastModeState != nil {
			state, fastModel, defaultModel = r.OnGetFastModeState()
		}
		switch state {
		case config.FastModeOn:
			return util.BiSame("Fast mode: on\nCurrent model: %s\nDefault model: %s", "快速模式：已开启\n当前模型：%s\n默认模型：%s", fastModel, defaultModel), nil
		case config.FastModeCooldown:
			return util.BiSame("Fast mode: cooling down\nReason: rate limited or overloaded; recovers automatically\nDefault model: %s", "快速模式：冷却中\n原因：限流/过载，稍后自动恢复\n默认模型：%s", defaultModel), nil
		default:
			return util.BiSame("Fast mode: off\nFast model: %s\nDefault model: %s", "快速模式：已关闭\n快速模型：%s\n默认模型：%s", fastModel, defaultModel), nil
		}
	}

	// 3. on / off 切换
	switch args {
	case "on":
		if r.OnSetFastMode != nil {
			if err := r.OnSetFastMode(true); err != nil {
				return "", ai.WithID(fmt.Errorf("failed to enable fast mode: %w", err), ai.ErrConfigInvalid)
			}
		}
		// M6：切换模型时 session 上下文保留，不重置
		return util.Bi("Fast mode enabled, switched to the fast model (session context preserved)", "快速模式已开启，已切换到快速模型（session 上下文保留）"), nil
	case "off":
		if r.OnSetFastMode != nil {
			if err := r.OnSetFastMode(false); err != nil {
				return "", ai.WithID(fmt.Errorf("failed to disable fast mode: %w", err), ai.ErrConfigInvalid)
			}
		}
		return util.Bi("Fast mode disabled, switched back to the default model", "快速模式已关闭，已切换回默认模型"), nil
	default:
		return "", ai.WithID(fmt.Errorf("invalid fast mode argument: %s (valid: on, off, status)", args), ai.ErrConfigInvalid)
	}
}
