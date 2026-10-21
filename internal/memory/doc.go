// Package memory 提供 H11/N27 memory 三分类核心字段层（Phase A）。
//
// 独立包，不放入 internal/config/（H11/N27 是独立方向）。
// N12 的 internal/config/memory_v2.go + memory_store.go 保留，
// H11/N27 通过扩展 N12 struct + 新包封装接口实现。
//
// 三层架构：
// - Phase A：核心字段层（MemoryEntry/CapabilityValue/verification 三态/ProbeFn/Ebbinghaus）
// - Phase B：双层 scope + 跨 scope 合并层 + SearchMemory BM25 + LinkMemories + 命令注册
// - Phase C：反思（ReflectMemories episodic→procedural）+ 压缩（CompactContext）
//
// 关键设计：
// - MemoryEntry 14 frontmatter 字段（N12 5 + v0.3 3 + v0.4 6）+ 3 内存字段（不持久化）
// - verification 三态：verified/unverified/contradicted（abstention 决策）
// - ProbeFn：注册表 + H2 委托 + builtin（N17 单向复用）+ 连续 3 次失败转 contradicted（PIT-001 防护）
// - Ebbinghaus 衰减：归一化 min(1.0, ...) + 后台 24h 扫描 + 启动补扫描
//
// 禁止 CGO/go plugin（PIT-004 Windows 不可用）。
package memory
