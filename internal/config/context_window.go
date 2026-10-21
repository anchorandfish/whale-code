package config

import "github.com/anchorandfish/whale-code/internal/ai"

// DefaultContextWindow 是「用户**什么都没配**」时生效的窗口。
//
// ★ C4为何**保持 131072**、而不是自动放宽到模型表里的更大窗口：
// 窗口变大 ⇒ 压缩更晚 ⇒ 长会话更容易飘进更高计价档（本机 1M 档约等于 2× 账单）。
// 「自动放宽」等于替用户改账单，所以默认值只做**保守兜底**；
// 想放宽请显式设 `context_window` 或 `WHALE_CONTEXT_WINDOW`
// （拍板依据：2026-10-05「默认保持 131072 + 兜底 200000」）。
const DefaultContextWindow = 131072

// ResolveContextWindow 是「显式值 + 模型名 ⇒ 运行期窗口」的**唯一规则实现**（C4，2026-10-05）。
//
// 规则只有一条：`explicit > 0` 用显式值；否则按模型名查表（`ai.ContextWindowForModel`，
// 未知模型由它兜底 200000 —— 兜底值单源，此处不复制）。
//
// 两个承载体都收敛到这里，避免同一规则出现第二份实现：
//
//	config.Config.EffectiveContextWindow() —— 用户配置这一侧（启动装配）
//	config.ResolveContextWindow(cfg.ContextWindow, cfg.Name) —— 单个模型配置那一侧
//
// （/model set、/fast 这类**换模型**的路径，
// 窗口应跟着"目标模型"走，而不是旧配置）
func ResolveContextWindow(explicit int, modelName string) int {
	if explicit > 0 {
		return explicit
	}
	return ai.ContextWindowForModel(modelName)
}

// EffectiveContextWindow 是**用户配置侧**的窗口解析点（C4，2026-10-05）。
//
// 语义（与 `Config.ContextWindow` 字段的声明保持一致，见 config.go 的 validate/Validate 注释）：
//
//	ContextWindow > 0 → 用户显式值，原样采用（settings.json 的 context_window / WHALE_CONTEXT_WINDOW）
//	ContextWindow == 0 → 「由模型决定」⇒ 按 Config.Model 查模型表
//	c == nil → DefaultContextWindow
//
// ★ 为什么必须有这个函数（而不是让各构造点自己读 `cfg.ContextWindow`）：
// 此前 7 处预算构造点直接吃 `cfg.ContextWindow`，于是字段注释里那句
// 「0 = 由模型决定」是**空头声明** —— 传 0 进去并不会「由模型决定」，而是让
// `Budget.ContextWindow <= 0` 的每个分支全线降级（`internal/context/context.go` 多处）。
// 解析器 `ai.ContextWindowForModel` 早就存在，只是**全仓零生产调用点**
// —— 与 同族：声明了能力却没接线，注释即谎报。
func (c *Config) EffectiveContextWindow() int {
	if c == nil {
		return DefaultContextWindow
	}
	return ResolveContextWindow(c.ContextWindow, c.Model)
}
