package tui

import (
	"fmt"
	"time"

	"github.com/anchorandfish/whale-code/internal/session"
)

// makeSessionIdentity 产生新的会话身份：UUID 主键 + 人类可读别名。
//
// 为什么必须是共享函数而不是各写一份：TUI 里存在两条会话命名路径
// （Model.autoNameSession 与 runnerCore.autoNameSession），历史上它们是两份
// 独立实现，规则漂移导致过真实缺陷 —— TUI 那份漏了文件名净化，用户输入
// "请执行 rm /tmp/x" 就让会话文件写到不存在的子目录，保存报 ENOENT
// （见 session_name_slash_test.go）。
//
// 身份构造因此收敛为**一处**规则：
// - 主键 id = UUID（进程唯一 + 时间有序）—— 多开时两个实例永不撞同一个文件；
// - 别名 alias = session.DeriveAlias(首条输入) —— 只用于展示与按名查找，
// 不参与文件名，因此截断/净化规则变化不会影响已有会话的可寻址性。
//
// 返回 ("", "") 表示首条输入派不出别名（空输入），调用方应保持未命名状态，
// 等下一次有内容时再命名。
func makeSessionIdentity(firstUserInput string) (id, alias string) {
	alias = session.DeriveAlias(firstUserInput)
	if alias == "" {
		return "", ""
	}
	return session.GenerateID(), alias
}

// sessionHandles 为一组会话摘要分配**组内互不相同**的展示句柄。
//
// 规则本身**不在本包**：唯一实现是 session.Handles（起改为取主键的随机尾段，
// 并在组内撞车时按需加长）。本函数只把摘要列表摊成 id 列表，避免 TUI 侧再长出一份
// 「取几位、取哪段」的本地实现 —— 那正是 的成因。
//
// ★ 必须**整组一起算**：句柄长度取决于「同组里还有谁」。逐行各取固定 8 位就无法保证
// 两行不重复 —— 随机尾段之间有生日碰撞概率，撞了就又是「两行一样的 [xxxx]」。
func sessionHandles(summaries []session.SessionSummary) map[string]string {
	ids := make([]string, 0, len(summaries))
	for _, s := range summaries {
		ids = append(ids, s.ID)
	}
	return session.Handles(ids)
}

// formatSessionLine 渲染列表中的一行会话摘要。
//
// 单一实现：/session list 与 /resume（无参）共用，避免两处各写一份格式后
// 逐渐漂移（历史上两处都在打 s.ID，别名引入后就会双双退化成 hex）。
// handle 由 sessionHandles 按整组算好后传入 —— 行渲染不再自己决定短码长度。
func formatSessionLine(index int, s session.SessionSummary, handle string) string {
	name := s.Name
	if name == "" {
		name = s.ID
	}
	timeStr := time.UnixMilli(s.UpdatedAt).Format("2006-01-02 15:04")
	return fmt.Sprintf(" %d. %s  [%s]  (Turns:%d %s)\n", index, name, handle, s.TurnCount, timeStr)
}
