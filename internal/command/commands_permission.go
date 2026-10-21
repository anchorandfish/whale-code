package command

import "github.com/anchorandfish/whale-code/internal/util"

// permissionCommands 返回 Permission 类别的内置命令。
//
// ★ 描述必须写明**轴归属**（G12/G13）。本仓有三个安全写入面，各写不同的轴：
//
//	/permissions <mode> → 审批轴 only（"问不问"） —— **不含沙箱**
//	/sandbox on|off → 能力轴 only（OS 隔离） —— **不含审批档**
//	/security-preset <id> → 两轴一起（预设）
//
// `permissions` 这个名字极易被当成"总的权限开关"（实测用户反馈），
// 而它完全不碰 OS 隔离 ⇒ 描述里必须显式否定沙箱，否则用户会以为设了它就安全了。
//
// ⚠️ 本表受 `TestCommandDescriptions_AllChinese` / `_StartsWithVerb` 约束：
// 必须含中文、以中文动词开头、≤50 字符。**英文版**是另一条出口
// （`command_metadata.go` 的 englishCommandDescriptions，供 slash 补全弹窗），
// 两张表都要带轴归属 —— 只改一处 = 半修。
func (r *Registry) permissionCommands() []Command {
	return []Command{
		{Name: "permissions", Description: "切换审批档，不含沙箱（沙箱请用 /sandbox）", Usage: "/permissions [mode|status]", Category: "Permission",
			Handler: func(args string) (string, error) {
				return util.Bi("⚠️ /permissions is only available in interactive TUI mode. Use this command in the whale interactive UI.", "⚠️ /permissions 仅在交互式 TUI 模式下可用。请在 whale 交互界面中使用此命令。"), nil
			}},
		// ★ 2026-10-14：Usage 补 status —— 此前写的是 "/sandbox on|off"，而 handleSandbox
		// 的 default 分支一直支持状态查询。/help 的 Usage 行是子命令形态唯一的说明书位
		// （command.go FormatHelp），漏写 = 用户少知一个功能（用户直报「/sandbox status
		// 显示里只有 /sandbox」）。
		{Name: "sandbox", Description: "切换沙箱隔离，不含审批档（审批请用 /permissions）", Usage: "/sandbox [on|off|status]", Category: "Permission",
			Handler: func(args string) (string, error) {
				return util.Bi("⚠️ /sandbox is only available in interactive TUI mode. Use this command in the whale interactive UI.", "⚠️ /sandbox 仅在交互式 TUI 模式下可用。请在 whale 交互界面中使用此命令。"), nil
			}},
		// 家族子命令：注册全名（与 /monitors stop、/checkpoint save 同构）。
		// ① `/` 弹窗可提示（typeahead 对注册名做前缀匹配，typeahead.go:146）；
		// ② command.Parse 靠 knownPrefixes 把 `/sandbox status` 切成 (name="sandbox status")。
		// status 是 /sandbox 家族的**既有能力**（handleSandbox default 分支），此前三个
		// 可见出口（弹窗/Usage/回执）一个都没提它 ⇒ 按 /monitors 判例「唯一出口不可发现
		// = 该能力不存在」，登记即补承诺。plain 侧经 dispatchCommand 转发同一实现
		// （plain.go 调度表， 两侧都验）。
		{Name: "sandbox status", Description: "查询沙箱门状态（只读，不改审批档）", Usage: "/sandbox status", Category: "Permission",
			Handler: func(args string) (string, error) {
				return util.Bi("⚠️ /sandbox is only available in interactive TUI mode. Use this command in the whale interactive UI.", "⚠️ /sandbox 仅在交互式 TUI 模式下可用。请在 whale 交互界面中使用此命令。"), nil
			}},
		{Name: "security-preset", Description: "配置安全套餐，同时改审批与沙箱两轴", Usage: "/security-preset [safe|standard|unrestricted]", Category: "Permission",
			Handler: func(args string) (string, error) {
				return util.Bi("⚠️ /security-preset is only available in interactive TUI mode. Use this command in the whale interactive UI.", "⚠️ /security-preset 仅在交互式 TUI 模式下可用。请在 whale 交互界面中使用此命令。"), nil
			}},
	}
}
