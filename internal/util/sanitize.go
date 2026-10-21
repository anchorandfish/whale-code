package util

import (
	"strings"
)

// SanitizeExternalText 剥离来自进程外部（MCP server / 网络 / 用户输入）文本中的
// 终端控制字符，防止终端注入（terminal injection）。
//
// 攻击面：恶意或已被攻陷的 MCP server 可在 elicitation message / server name 中
// 注入控制序列 —— \r（回车覆盖行首伪造可信名）、\x1b[2J（清屏）、\x1b[8m（隐藏）、
// \x1b]0;（改终端标题）。这些字符串会被原样渲染进 TUI，直接被终端解释执行。
//
// 本函数在**信任域边界**处调用（外部数据进入本进程的位置），剥离 C0 控制字符
// （\x00-\x1f）与 DEL（\x7f），但保留 \n（换行，message 合法需要）与 \t（制表，
// 无害）。其余控制字符一律删除（而非替换为空格），避免 \r 被替换后仍残留可读
// 混淆。（方案 A，双端净化）。
func SanitizeExternalText(s string) string {
	return strings.Map(func(r rune) rune {
		// 保留换行与制表；其余 C0（0x00-0x1f）与 DEL（0x7f）删除。
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
