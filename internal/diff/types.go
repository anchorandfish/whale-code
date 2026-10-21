package diff

import (
	"hash/fnv"
)

// FileChange 文件改动数据，N9 主设计，供 Render 消费
// 对标同类 CLI toolUseResult.structuredPatch 的数据载体
type FileChange struct {
	Path       string // 文件路径（相对工作目录）
	OldContent string // 旧内容（空表示新建文件）
	NewContent string // 新内容（空表示删除文件）
	IsNew      bool   // 是否新建文件
	IsDeleted  bool   // 是否删除文件
}

// Hash 返回 FileChange 的内容哈希，用于缓存键
// 使用 \x00 分隔符防止字段边界碰撞
func (c FileChange) Hash() uint64 {
	h := fnv.New64()
	h.Write([]byte(c.Path))
	h.Write([]byte{0})
	h.Write([]byte(c.OldContent))
	h.Write([]byte{0})
	h.Write([]byte(c.NewContent))
	return h.Sum64()
}

// Stats diff 统计，N9 自有类型（inline 工具结果场景）
// 注意：rewind 场景消费 H1/N4 的 filehistory.DiffStats，不与此类型冲突
type Stats struct {
	FilesChanged int
	Insertions   int
	Deletions    int
}

// TurnDiff 一轮对话的文件改动汇总，对标同类 CLI TurnDiff
type TurnDiff struct {
	TurnIndex     int
	PromptPreview string
	Files         []FileChange
	Stats         Stats
}

// NewTurnDiff 组装一轮的文件变更汇总，并把 Stats 一次算好。
//
// 本类型与 RenderTurn 早在本包落地，但**生产零调用点**
// （全仓 grep 仅命中本包与文档）—— 轮末"这轮改了什么"从来没有生产者。
// 本构造器就是那个生产者：变更集由 filehistory 从**真实提交的 track 记录**算出
// （`FileHistory.TurnChanges`），这里只负责统计口径统一。
//
// 为什么统计必须收在这里而不是各 UI 自己数：轮末卡片（一行 `+N -M`）与 `/diff`
// （逐文件正文 + 合计）是**同一个事实的两个视图**，各数一遍必然漂移（同源）。
// 口径：新增/删除文件按整文件行数计入 Insertions/Deletions（ComputeLineDiffs 对
// 空基线/空终态天然给全加/全删），FilesChanged = 变更文件数。
func NewTurnDiff(turnIndex int, promptPreview string, files []FileChange) TurnDiff {
	stats := Stats{FilesChanged: len(files)}
	for _, f := range files {
		added, removed := CountChanges(f)
		stats.Insertions += added
		stats.Deletions += removed
	}
	return TurnDiff{
		TurnIndex:     turnIndex,
		PromptPreview: promptPreview,
		Files:         files,
		Stats:         stats,
	}
}

// RenderConfig 渲染配置
type RenderConfig struct {
	MaxLines      int     // 最大渲染行数（默认 400，对标同类 CLI）
	MaxFileSize   int64   // 最大文件字节数（默认 1MB，对标同类 CLI）
	WordThreshold float64 // 词级高亮阈值（默认 0.4，对标同类 CLI）
	ContextLines  int     // 上下文行数（默认 3，对标 snapshot.go:422）
	ColorBlind    bool    // 色盲主题
	Plain         bool    // 纯文本输出：不产生任何 ANSI 转义序列（见 PlainConfig）
}

// DefaultConfig 返回默认渲染配置
func DefaultConfig() RenderConfig {
	return RenderConfig{
		MaxLines:      400,
		MaxFileSize:   1 * 1024 * 1024,
		WordThreshold: 0.4,
		ContextLines:  3,
		ColorBlind:    false,
		Plain:         false,
	}
}

// PlainConfig 返回「纯文本」渲染配置：渲染结果不含任何 ANSI 转义序列。
//
// 什么时候该用它：**数据层**产出时用，即工具结果 Output。
// 工具结果有三个消费者，全是纯文本消费者：
// 1. 剪贴板（/copy all、/copy N）—— ANSI 在粘贴出去的地方是乱码；
// 2. 模型上下文（tool_result content 原样发给 LLM）—— 纯噪音，还白吃 token；
// 3. 会话存档 / 导出 —— 存转义序列没有意义。
//
// 什么时候不该用：**渲染层**要给人看的时候（终端展示、/diff 命令）走 DefaultConfig()。
//
// 一句话判据：颜色是渲染层的关注点，不该出现在数据层。
// 由此还顺带消掉一个次生问题 —— 工具结果按字节截断（file.go 的 maxDiffLen）会切碎
// 转义序列、切坏 UTF-8；纯文本里没有转义序列可切。
func PlainConfig() RenderConfig {
	cfg := DefaultConfig()
	cfg.Plain = true
	return cfg
}
