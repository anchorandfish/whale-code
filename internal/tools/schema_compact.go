// Package tools — 面向模型的 tool schema 投影（P0-2，2026-10-14）。
//
// 问题：外部来源的工具（MCP server / 第三方插件）的 input_schema 是**无上限**的 ——
// 它们由服务端任意生成，可能带巨量 description、$defs 定义表和深达十几层的嵌套。
// whale 此前把 spec.InputSchema **原样**塞进每次请求的 tools 数组（buildToolDefs），
// 于是这些字节每一轮都要重付一次，且**挤占上下文窗口**（不是缓存命中能救的：它污染预算）。
//
// 对照实现（"看看别人"）：同类 CLI 的 tool schema 压缩（业内既有做法；
// 按 门口径，注释不引竞品源码路径）。
//
//	· 带压缩入口：MCP / 动态工具的**外部** schema 走压缩；
//	· 可信旁路：少数自研内建工具不压缩；
//	· 预算常量：MAX_COMPACT_TOOL_SCHEMA_BYTES = 4_000 / MAX_COMPACT_TOOL_SCHEMA_DEPTH = 3
//	· 算法：渐进降损，pass 顺序 = 去描述 → 丢 $defs → 折叠深层对象 → 剪组合关键字；
//
// 一旦落入预算立即停止（best-effort，非硬上限）
//
// 第一性原理取舍：同类 CLI 按"来源"分流（外部压、内建不压）。whale 侧实测 58 个内建 schema
// **全部** ≤ 1442 字节（远低于 4000 预算）⇒ 无需追踪来源，**统一施加**即可：
// 入预算的分支直接返回原对象 ⇒ 对内建是**可证的恒等映射**（零行为变化），
// 只有真正超预算的外部 schema 才会被降损。少一个分流维度，就少一处"两边各写各的"。
package tools

import "encoding/json"

// 面向模型的 schema 预算（对标同类 CLI 的既有做法）。
// MaxModelSchemaBytes 用「紧凑序列化字节数」当作 token 预算的本地代理（业内同思路）。
const (
	MaxModelSchemaBytes = 4000
	MaxModelSchemaDepth = 3
)

// schemaChildKeys / compositionSchemaKeys / definitionTableKeys 与同类 CLI 实现
// 的语义一一对应（组合关键字 / 定义表键 / 定义引用前缀）。
var (
	schemaChildKeys       = []string{"items", "anyOf", "oneOf", "allOf"}
	compositionSchemaKeys = []string{"anyOf", "oneOf", "allOf"}
	definitionTableKeys   = []string{"$defs", "definitions"}
	definitionRefPrefixes = []string{"#/$defs/", "#/definitions/"}
)

// CompactSchemaForModel 返回工具 input_schema 的「发给模型」投影。
//
// 三条性质（都由 schema_compact_test.go 钉住）：
// 1. **恒等**：序列化后未超预算 ⇒ 原对象直接返回（不拷贝、不改类型）⇒ 对内建零影响。
// 2. **纯函数**：超预算时才深拷贝再降损 —— 本体（spec.InputSchema）**永不被就地修改**。
// 本体仍被 SchemaValidateStage（pipeline.go:610）用于校验模型参数，投影只作用于模型侧。
// 3. **best-effort**：按业内既有的 pass 顺序逐级降损，一旦入预算即停；不保证一定 ≤ 预算。
//
// 调用点：buildToolDefs（internal/tui/runner_core.go / stream.go）—— 即"模型真正看到的那份"。
func CompactSchemaForModel(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	// 未超预算 ⇒ 原样返回。这一步让本函数对全部内建 schema 成为恒等映射。
	if compactSchemaFitsBudget(schema) {
		return schema
	}
	// 超预算 ⇒ 深拷贝后再降损，保证不动本体。
	root := deepCopyJSON(schema)
	for _, pass := range largeSchemaCompactionPasses {
		if compactSchemaFitsBudget(root) {
			break
		}
		pass(root)
	}
	return root
}

// largeSchemaCompactionPasses 是渐进降损的 pass 序列（顺序有语义：先掉信息量最小的）。
// pass 顺序对齐同类 CLI 的既有实现（先掉信息量最小的）。
var largeSchemaCompactionPasses = []func(interface{}){
	stripSchemaDescriptions,
	dropSchemaDefinitions,
	collapseDeepSchemaObjectsFromRoot,
	pruneSchemaCompositions,
}

// deepCopyJSON 经 JSON 往返深拷贝，确保后续 in-place pass 不会触碰调用方的 map。
// 注意：会把手写的 []string 等归一为 []interface{}；仅发生在超预算（外部）schema 上。
func deepCopyJSON(schema map[string]interface{}) map[string]interface{} {
	b, err := json.Marshal(schema)
	if err != nil {
		return schema // 无法序列化（理论上不会）⇒ 保守原样返回，宁可不省也不能坏
	}
	var out map[string]interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		return schema
	}
	return out
}

// compactSchemaFitsBudget 用紧凑序列化长度判定是否落入预算。
func compactSchemaFitsBudget(v interface{}) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return true // 序列化不了就不折腾它
	}
	return len(b) <= MaxModelSchemaBytes
}

// —— pass 1：剥离 description（信息量最小，先掉）——————————————
func stripSchemaDescriptions(v interface{}) {
	switch t := v.(type) {
	case []interface{}:
		for _, e := range t {
			stripSchemaDescriptions(e)
		}
	case map[string]interface{}:
		delete(t, "description")
		forEachSchemaChild(t, definitionTraversalInclude, stripSchemaDescriptions)
	}
}

// —— pass 2：先把本地 $ref 改写成空 schema，再删掉 $defs/definitions 表 ——
func dropSchemaDefinitions(v interface{}) {
	rewriteDefinitionRefsToEmptySchemas(v)
	if m, ok := v.(map[string]interface{}); ok {
		for _, k := range definitionTableKeys {
			delete(m, k)
		}
	}
}

func rewriteDefinitionRefsToEmptySchemas(v interface{}) {
	switch t := v.(type) {
	case []interface{}:
		for _, e := range t {
			rewriteDefinitionRefsToEmptySchemas(e)
		}
	case map[string]interface{}:
		if ref, ok := t["$ref"].(string); ok && isLocalDefinitionRef(ref) {
			for k := range t {
				delete(t, k)
			}
			return
		}
		forEachSchemaChild(t, definitionTraversalSkip, rewriteDefinitionRefsToEmptySchemas)
	}
}

// —— pass 3：深度 ≥ MaxModelSchemaDepth 的复杂对象折叠为 {} ——
func collapseDeepSchemaObjectsFromRoot(v interface{}) {
	collapseDeepSchemaObjects(v, 0)
}

func collapseDeepSchemaObjects(v interface{}, depth int) {
	switch t := v.(type) {
	case []interface{}:
		for _, e := range t {
			collapseDeepSchemaObjects(e, depth)
		}
	case map[string]interface{}:
		if depth >= MaxModelSchemaDepth && isComplexSchemaObject(t) {
			for k := range t {
				delete(t, k)
			}
			return
		}
		forEachSchemaChild(t, definitionTraversalSkip, func(child interface{}) {
			collapseDeepSchemaObjects(child, depth+1)
		})
	}
}

// —— pass 4：剪掉组合关键字（anyOf/oneOf/allOf）所在的对象 ——
func pruneSchemaCompositions(v interface{}) {
	switch t := v.(type) {
	case []interface{}:
		for _, e := range t {
			pruneSchemaCompositions(e)
		}
	case map[string]interface{}:
		if hasCompositionKeyword(t) {
			for k := range t {
				delete(t, k)
			}
			return
		}
		forEachSchemaChild(t, definitionTraversalSkip, pruneSchemaCompositions)
	}
}

// —— 共用遍历与判定 ——————————————————————————————————————

type definitionTraversal int

const (
	definitionTraversalInclude definitionTraversal = iota
	definitionTraversalSkip
)

// forEachSchemaChild 遍历一个 schema 对象的直接子节点（properties 各值 + 子 schema 键 +
// additionalProperties + 可选的定义表），语义与同类 CLI 的子 schema 遍历对齐。
func forEachSchemaChild(m map[string]interface{}, def definitionTraversal, visit func(interface{})) {
	if props, ok := m["properties"].(map[string]interface{}); ok {
		for _, child := range props {
			visit(child)
		}
	}
	for _, key := range schemaChildKeys {
		if child, ok := m[key]; ok {
			visit(child)
		}
	}
	if ap, ok := m["additionalProperties"]; ok {
		if _, isBool := ap.(bool); !isBool {
			visit(ap)
		}
	}
	if def == definitionTraversalInclude {
		for _, key := range definitionTableKeys {
			if table, ok := m[key].(map[string]interface{}); ok {
				for _, child := range table {
					visit(child)
				}
			}
		}
	}
}

func isComplexSchemaObject(m map[string]interface{}) bool {
	for _, key := range schemaChildKeys {
		if _, ok := m[key]; ok {
			return true
		}
	}
	if _, ok := m["properties"]; ok {
		return true
	}
	if _, ok := m["additionalProperties"]; ok {
		return true
	}
	if _, ok := m["$ref"]; ok {
		return true
	}
	return false
}

func hasCompositionKeyword(m map[string]interface{}) bool {
	for _, key := range compositionSchemaKeys {
		if _, ok := m[key]; ok {
			return true
		}
	}
	return false
}

func isLocalDefinitionRef(ref string) bool {
	for _, p := range definitionRefPrefixes {
		if len(ref) > len(p) && ref[:len(p)] == p {
			return true
		}
	}
	return false
}
