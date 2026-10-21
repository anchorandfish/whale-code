// memory_source.go — 记忆读路径的数据来源抽象（P2 接线，2026-10-08）。
//
// ## 这一层为什么必须存在
//
// 读路径（MemorySelector / MemoryIndex）原先直接依赖 *MemoryStore —— 单目录、
// 按 cwd hash 隔离。它拿不到 internal/memory 的能力：
// - 双 scope 合并语义（user 偏好 + project 事实，project 覆盖 user）
// - 有效性判定（contradicted / 已过期不得注入）
//
// 结果是「机制在 internal/memory，路径走 config」两边断开（TD-12 的表现之一）。
//
// ## 为什么不能直接把字段类型换成 *memory.MemoryStore
//
// 会成环：
//
//	internal/memory → import internal/command（commands.go 注册子命令）
//	internal/command → import internal/config（fast_handler / command / migrate_handler）
//	⇒ config → memory 会形成 config → memory → command → config（Go 编译拒绝）
//
// ## 因此做依赖倒置
//
// config 只声明「读路径需要什么」（读 + 写不了 + 索引目录），
// 由中间层 internal/memorybridge（可同时 import config 与 memory）适配 memory 的实现。
//
// ## 兼容性
//
// *config.MemoryStore 的方法集天然满足本接口（Dir/List/Read 签名一致）
// ⇒ 现有全部调用方与测试**零改动**；只有 tui 装配层改为注入 bridge 适配器。
package config

import "context"

// MemorySource 是记忆读路径所需的最小数据来源契约。
//
// 实现方：
// - *MemoryStore（本包内，单目录 project scope；保留为降级路径与测试替身）
// - *memorybridge.Source（跨包适配器，接入 memory 的双 scope 合并 + 有效性过滤）
//
// 约定：
// - List 返回的条目**必须已按 UpdatedAt 降序**。读路径的本地兜底依赖这个顺序
// （：首轮不等 AI，取「最近更新的前 N 条」）。实现方负责排序，调用方不补排。
// - List 返回的条目**必须已做有效性过滤**（不返回 contradicted / 已过期条目）。
// 判定的唯一权威是 memory 包的 LiveAt；config 不复制该条件。
type MemorySource interface {
	// Dir 返回读路径索引文件（MEMORY.md）所在目录的绝对路径。
	Dir() string
	// List 返回当前可见的全部记忆条目（已排序、已过滤）。
	List() ([]MemoryEntryV2, error)
	// Read 按 name 读取单条记忆正文；name 可带或不带 .md 后缀。
	Read(name string) (*MemoryEntryV2, error)
}

// MemoryRanker 是读路径的**可选**能力：按查询对记忆做本地相关度排序（P3-wire，2026-10-08）。
//
// ## 为什么单列一个接口，而不是并进 MemorySource
//
// - 只有新适配器（memorybridge，背后是 internal/memory 的打分器）能实现它；
// *MemoryStore（旧单目录 store）**没有**打分器。
// - 若并进 MemorySource，旧 store 就必须**伪造**一个排序（恒返回空、或返回全部，
// 二者都不是"排序"）—— 那是"同一语义两处实现"的起点（本仓 G5–G7 / 的形态）。
//
// 所以：**能力可选，缺失即降级**。调用方（MemorySelector）在 store 未实现本接口时
// 退回"按 UpdatedAt 取前 N 条"，即接线前的行为。
//
// ## 为什么需要它（接线要解决的问题）
//
// manifest 条数 > maxSelections 时，首轮注入以前取的是"**最近更新的 N 条**"——
// 与用户这一句问什么**完全无关**。用户会看到"它明明记过这件事，却想不起来"。
type MemoryRanker interface {
	// Rank 返回与 query 相关的记忆 name（相关度降序，最多 topK 条）。
	//
	// 约定：
	// - 返回**空切片**表示"没有词法信号"（不是错误）⇒ 调用方退回默认顺序；
	// - 允许返回的名字不在 List() 里（调用方按名过滤，不视为错误）；
	// - 不返回错误以外的失败信号（实现方不要用 error 表达"没搜到"）。
	Rank(ctx context.Context, query string, topK int) ([]string, error)
}
