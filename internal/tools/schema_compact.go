// Package tools — 面向模型的 tool schema 投影（P0-2，2026-10-14 立项；2026-10-28 重写为独立实现）。
//
// 问题：外部来源的工具（MCP server / 第三方插件）的 input_schema 是**无上限**的 ——
// 它们由服务端任意生成，可能带巨量 description、$defs 定义表和深达十几层的嵌套。
// whale 此前把 spec.InputSchema **原样**塞进每次请求的 tools 数组（buildToolDefs），
// 于是这些字节每一轮都要重付一次，且**挤占上下文窗口**（不是缓存命中能救的：它污染预算）。
//
// 设计：一趟遍历 + 档位递增，而不是"每个降损动作各写一份递归"。
//
//	· 只有一个遍历器 shrinkTree，四个降损档位共用它；档位只回答两件事：
//	  对本节点做什么、要不要继续下钻（返回 false = 本节点已被抹平）。
//	· 档位顺序沿用业内通行做法：先掉信息量最小的（描述 → 定义表 → 深层骨架 → 组合分支）。
//	· 每升一档前重新量一次字节数，一旦入预算立刻停手（best-effort，非硬上限）。
//
// 第一性原理取舍：业内同类实现按"来源"分流（外部压、内建不压）。whale 侧实测 58 个内建
// schema **全部** ≤ 1442 字节（远低于 4000 预算）⇒ 无需追踪来源，**统一施加**即可：
// 入预算的分支直接返回原对象 ⇒ 对内建是**可证的恒等映射**（零行为变化），
// 只有真正超预算的外部 schema 才会被降损。少一个分流维度，就少一处"两边各写各的"。
package tools

import "encoding/json"

// 面向模型的 schema 预算。MaxModelSchemaBytes 用「紧凑序列化字节数」当作 token 预算的
// 本地代理（业内同思路）。⚠️ 字节 ≠ token：中文/转义密集的 schema 会显著低估 token 数，
// 要换成真 tokenizer 计数需同时改这里与 schemaByteLen 的口径（当前未做）。
const (
	MaxModelSchemaBytes = 4000
	MaxModelSchemaDepth = 3
)

// shrinkStage 是降损档位。数值即"破坏力排序"，shrinkLadder 按它递增。
type shrinkStage int

const (
	stagePlainProse   shrinkStage = iota // 抹掉给人看的自然语言描述
	stageFlattenRefs                     // 本地 $ref 内联成空对象，并清掉根定义表
	stageCapNesting                      // 超过深度上限的结构化对象整体抹平
	stageDropVariants                    // 带 anyOf/oneOf/allOf 的对象整体抹平
)

// shrinkLadder 是档位序列（顺序有语义：破坏力从小到大）。
var shrinkLadder = []shrinkStage{
	stagePlainProse,
	stageFlattenRefs,
	stageCapNesting,
	stageDropVariants,
}

// schema 里"子 schema 可能挂在哪"的三组键。
//   - nestedSchemaKeys：值的语义就是子 schema；
//   - variantKeys：组合分支，等价于"多选一"，剪掉即退化为"随便传"；
//   - tableKeys：定义表，其 value 也是子 schema（只在 stagePlainProse 下钻）。
var (
	nestedSchemaKeys = []string{"items", "anyOf", "oneOf", "allOf"}
	variantKeys      = []string{"anyOf", "oneOf", "allOf"}
	tableKeys        = []string{"$defs", "definitions"}
	internalRefHeads = []string{"#/$defs/", "#/definitions/"}
)

// CompactSchemaForModel 返回工具 input_schema 的「发给模型」投影。
//
// 三条性质（都由 schema_compact_test.go 钉住）：
//  1. **恒等**：序列化后未超预算 ⇒ 原对象直接返回（不拷贝、不改类型）⇒ 对内建零影响。
//  2. **纯函数**：超预算时才深拷贝再降损 —— 本体（spec.InputSchema）**永不被就地修改**。
//     本体仍被 SchemaValidateStage（pipeline.go:610）用于校验模型参数，投影只作用于模型侧。
//  3. **best-effort**：按破坏力从小到大逐档降损，一旦入预算即停；不保证一定 ≤ 预算。
//
// 调用点：buildToolDefs（internal/tui/runner_core.go / stream.go）—— 即"模型真正看到的那份"。
func CompactSchemaForModel(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	// 未超预算 ⇒ 原样返回。这一步让本函数对全部内建 schema 成为恒等映射。
	if schemaByteLen(schema) <= MaxModelSchemaBytes {
		return schema
	}
	// 超预算 ⇒ 深拷贝后再降损。拷贝失败就**放弃降损**而不是原地改：
	// 宁可多发几千字节，也不能破"本体零改动"这条不变量。宁可不省也不能坏。
	work, ok := cloneForShrinking(schema)
	if !ok {
		return schema
	}
	for _, stage := range shrinkLadder {
		if schemaByteLen(work) <= MaxModelSchemaBytes {
			break
		}
		shrinkTree(work, stage, 0)
		if stage == stageFlattenRefs {
			// 定义表的删除只作用在根对象上：内层定义表可能仍被未内联的引用指到，
			// 交给后续深度档按"整层抹平"处理，而不是这一档逐层清空。
			for _, key := range tableKeys {
				delete(work, key)
			}
		}
	}
	return work
}

// schemaByteLen 返回紧凑序列化后的字节数；序列化不了就返回 0（解释为"看起来没超"，即不折腾它）。
func schemaByteLen(v interface{}) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// cloneForShrinking 经 JSON 往返做深拷贝，保证后续就地改写碰不到调用方的 map。
// 注意：会把手写的 []string 等归一为 []interface{}；仅发生在超预算（外部）schema 上。
func cloneForShrinking(schema map[string]interface{}) (map[string]interface{}, bool) {
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, false
	}
	var out map[string]interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, false
	}
	return out, true
}

// shrinkTree 是唯一的遍历器：先按档位处理当前节点，再按档位口径决定是否下钻。
//
// level 只对 stageCapNesting 有意义，且**只在 map 层递增**：数组只是容器，不构成一层嵌套
// （与"深度"的语义一致 —— 深度说的是 schema 嵌套，不是 JSON 层级）。
func shrinkTree(node interface{}, stage shrinkStage, level int) {
	switch typed := node.(type) {
	case []interface{}:
		for _, item := range typed {
			shrinkTree(item, stage, level)
		}
	case map[string]interface{}:
		if !shrinkNode(typed, stage, level) {
			return // 本节点已被抹平，不再下钻
		}
		for _, child := range childSchemas(typed, stage == stagePlainProse) {
			shrinkTree(child, stage, level+1)
		}
	}
}

// shrinkNode 对单个节点施加当前档位，返回"是否继续下钻子节点"。
func shrinkNode(m map[string]interface{}, stage shrinkStage, level int) bool {
	switch stage {
	case stagePlainProse:
		delete(m, "description")
		return true
	case stageFlattenRefs:
		if ref, isString := m["$ref"].(string); isString && pointsInsideDocument(ref) {
			wipe(m)
			return false
		}
		return true
	case stageCapNesting:
		if level >= MaxModelSchemaDepth && isStructured(m) {
			wipe(m)
			return false
		}
		return true
	case stageDropVariants:
		if declaresVariants(m) {
			wipe(m)
			return false
		}
		return true
	}
	return true
}

// childSchemas 列出一个 schema 对象的直接子 schema。
// includeTables 只在抹描述那一档为真：定义表里的 schema 也要抹描述，
// 但其余档位不碰定义表（抹平定义表会连带废掉仍被引用的类型信息）。
func childSchemas(m map[string]interface{}, includeTables bool) []interface{} {
	children := make([]interface{}, 0, len(m)+2)
	if properties, ok := m["properties"].(map[string]interface{}); ok {
		for _, value := range properties {
			children = append(children, value)
		}
	}
	for _, key := range nestedSchemaKeys {
		if value, ok := m[key]; ok {
			children = append(children, value)
		}
	}
	// additionalProperties 为 bool 时是"允许/禁止额外属性"的开关，不是子 schema。
	if extra, ok := m["additionalProperties"]; ok {
		if _, isSwitch := extra.(bool); !isSwitch {
			children = append(children, extra)
		}
	}
	if includeTables {
		for _, key := range tableKeys {
			if table, ok := m[key].(map[string]interface{}); ok {
				for _, value := range table {
					children = append(children, value)
				}
			}
		}
	}
	return children
}

// isStructured 判断这个对象是否"还带结构"（有子 schema 或引用）。
// 只用来决定深度档上要不要抹平：一个 {} 或 {"type":"string"} 不值得再花钱。
func isStructured(m map[string]interface{}) bool {
	for _, key := range nestedSchemaKeys {
		if _, ok := m[key]; ok {
			return true
		}
	}
	for _, key := range []string{"properties", "additionalProperties", "$ref"} {
		if _, ok := m[key]; ok {
			return true
		}
	}
	return false
}

// declaresVariants 判断是否带组合分支（anyOf/oneOf/allOf）。
func declaresVariants(m map[string]interface{}) bool {
	for _, key := range variantKeys {
		if _, ok := m[key]; ok {
			return true
		}
	}
	return false
}

// pointsInsideDocument 判断 $ref 是否指向本文档内的定义表（#/$defs/x 或 #/definitions/x）。
// 外部地址（http://…、其他文件）不在此列：那些引用内联不了，只能留着。
func pointsInsideDocument(ref string) bool {
	for _, head := range internalRefHeads {
		if len(ref) > len(head) && ref[:len(head)] == head {
			return true
		}
	}
	return false
}

// wipe 清空一个对象的所有键（保留对象本身 —— 调用方看到的是 {}，不是"字段消失"）。
func wipe(m map[string]interface{}) {
	for key := range m {
		delete(m, key)
	}
}
