// Package tools — 工具能力面判定（S3 · 裁定 C=②「工具面 = 能力面」）。
//
// 本文件只回答一个问题：**某个工具此刻到底有没有能力可用**。
// 它存在的唯一理由，是让「承诺」与「能力」由**同一个谓词**派生 ——
// 三条暴露面各自实现一套判断必然漂移（本仓 G5–G7「工具面唯一真相源」、「同一事实跨入口必须同源」
// 都是同族事故； 是这一族在档位目录上的表现）。
//
// 三条面共用本谓词：
//
//	① 注册点 tools.go registerBuiltinTools() —— 不建 spec ⇒ 自动不在 AllDirect()/List() 里
//	② 档位目录 toollevel.go toolLevelsSnapshot() —— 快照"只增不减" ⇒ 必须**显式**过滤（§11.1 落点①）
//	③ 执行门 executor.go registerBuiltinHandlers() —— 不注册 handler ⇒ 落到"未知工具"出口
//
// 别名入口（`search` → `WebSearch`，tools.go NormalizeToolName）无需单独接线：
// 它在**注册之后**解析到同一个名字，自然落到同一个出口 —— 这正是 B1 要求的"两条路一种错"。
package tools

import (
	"os"
	"strings"
	"sync"
)

// SearchChannel 是检索通道。
//
// v0.1 的实际取值域只有 LocalKey | None：裁定 A（S6 延后）与裁定 B（P0 探针延后）
// ⇒ L1 的能力位与注入都不接线（§11.3）。Native 是**刻意保留、当前不可达**的枚举值，
// 保留它是为了 v0.2 接 L1 时不必改签名（进而不会改到判据）。
type SearchChannel string

const (
	// ChannelNative provider 原生检索（L1）。★ v0.1 不可达（S6 延后）。
	ChannelNative SearchChannel = "native"
	// ChannelLocalKey 本地凭据通道（L2）：SEARCH_API_KEY 等能解析出可用契约。
	ChannelLocalKey SearchChannel = "local"
	// ChannelNone 无可用通道（L3 兜底）。
	ChannelNone SearchChannel = "none"
)

var (
	// availabilityMu 保护 availabilityOverride。
	availabilityMu sync.RWMutex

	// availabilityOverride 显式可用性覆盖表。**不在表里 = 可用。**
	//
	// 默认"全部可用"是刻意的：它让本谓词对既有行为**零影响**（§3.2），
	// 于是"无通道时不注册"这件事必须由一个**显式**的启动期装配写进来，
	// 而不是靠"默认就不可用"隐式发生 —— 后者会让所有不配置环境变量的单测
	// 悄悄换一个世界（判据随之失去判别力）。
	availabilityOverride = map[string]bool{}
)

// SetToolAvailability 显式设置某工具的可用性。
// 生产调用点唯一：main.go 的启动期装配（loadEnvFiles 咽喉点）。
func SetToolAvailability(name string, available bool) {
	availabilityMu.Lock()
	defer availabilityMu.Unlock()
	availabilityOverride[name] = available
}

// ResetToolAvailability 清除全部显式覆盖，回到"默认全部可用"。
// 只给测试用：每条判据都在干净的默认态上起跑，用例之间不互相污染。
func ResetToolAvailability() {
	availabilityMu.Lock()
	defer availabilityMu.Unlock()
	availabilityOverride = map[string]bool{}
}

// toolAvailable 是「工具面 = 能力面」的**唯一谓词**。
//
// 三面（注册点 / 档位目录 / 执行门）都必须调它；任何一处自行判断都是漂移的起点。
func toolAvailable(name string) bool {
	availabilityMu.RLock()
	defer availabilityMu.RUnlock()
	if available, ok := availabilityOverride[name]; ok {
		return available
	}
	return true
}

// ToolAvailable 是唯一谓词的**只读出口**，供 tools 包外的能力面消费者使用
// （S3 · 裁定 C=② 的 GitHub 面：internal/github 的注册点与执行门必须读**同一个**谓词，
// 见 internal/github/availability.go）。
//
// 它是同一份判定的**薄转发**，不是第二套判断：语义只有一条，本体留在包内。
// ⚠️ 跟进项（刻意保留双名，勿视为第二种语义）：待 `executor.go` 无并行写者时，
// 把包内 3 处直调（toollevel.go:92 / executor.go:1069 / tools.go:1376）一并改名为
// ToolAvailable 并删除本转发 —— 现在不改是因为那 3 个文件此刻各有别的会话在写。
func ToolAvailable(name string) bool { return toolAvailable(name) }

// WebSearchChannel 是检索通道的判定入口（唯一），由启动期装配调用。
func WebSearchChannel() SearchChannel {
	return webSearchChannelFrom(os.Getenv)
}

// webSearchChannelFrom 是 WebSearchChannel 的可注入形态：判据用固定 getenv 驱动，
// 不依赖进程环境 ⇒ 同一条判据在任何人机器上结论一致（否则本包判据会随开发机的
// SEARCH_API_KEY 而飘，属"环境相关假绿"）。
//
// ★ 它**不是**另写一套能力判断，而是复用 webSearchHandler 真正会走的那个闸门：
// 契约能否解析出来 + 该契约要不要凭据而凭据在不在。
// 于是「工具面说有能力」⇔「handler 真跑得起来」成为同一事实的两次读取，而不是两份实现
// —— 若另写一套，就是 「同一事实跨入口不同源」的翻版。
func webSearchChannelFrom(getenv func(string) string) SearchChannel {
	// L1（ChannelNative）在 v0.1 恒不可用：能力位与注入都不接线（§11.3），故此处不判它。

	contract, err := resolveSearchContract(getenv)
	if err != nil {
		// "配置写错"（SEARCH_API_FORMAT 非法/缺 URL）与"根本没配"在这里同归 None：
		// 两者都跑不起来，工具面都不该承诺（与 webSearchHandler 的 fail-closed 同结局）。
		return ChannelNone
	}
	if contract.requiresAPIKey() && strings.TrimSpace(getenv(searchAPIKeyEnv)) == "" {
		return ChannelNone
	}
	return ChannelLocalKey
}
