package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// 本文件钉住「面向模型的 schema 投影」语义（P0-2）。
// 对照实现：同类 CLI 的 tool schema 压缩（业内既有做法；按 门口径不引竞品源码路径）
// MAX_COMPACT_TOOL_SCHEMA_BYTES = 4_000 / MAX_COMPACT_TOOL_SCHEMA_DEPTH = 3
// 渐进降损的 pass 顺序 = 去描述 → 丢 $defs → 折叠深层对象 → 剪组合关键字
//
// ★ 最关键的一条不变量：**投影绝不修改 schema 本体**。
// 本体（spec.InputSchema）同时被 SchemaValidateStage 用于校验模型参数
// （internal/tools/pipeline.go:610）。若投影就地改了本体，模型按残缺 schema
// 传参就会被 server / 校验层拒掉 —— 这是"省 token"变成"功能坏掉"的经典路径。

func marshalLen(t *testing.T, v interface{}) int {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return len(b)
}

// TestCompactSchemaForModel_SmallSchemaUnchanged：未超预算的 schema 必须原样返回。
// 这是"零风险"的形式化表述 —— 58 个内建 schema 全部走这条分支。
func TestCompactSchemaForModel_SmallSchemaUnchanged(t *testing.T) {
	in := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "文件路径", // 未超预算 ⇒ 描述必须保留
			},
		},
		"required": []string{"path"},
	}
	out := CompactSchemaForModel(in)
	if marshalLen(t, out) != marshalLen(t, in) {
		t.Fatalf("未超预算的 schema 被改动了：in=%d out=%d", marshalLen(t, in), marshalLen(t, out))
	}
	props, _ := out["properties"].(map[string]interface{})
	path, _ := props["path"].(map[string]interface{})
	if path == nil || path["description"] != "文件路径" {
		t.Fatalf("未超预算时描述被误删：%#v", out)
	}
}

// TestCompactSchemaForModel_HugeDescriptionsStripped：超预算时首轮 pass = 删描述。
// 对照口径：4500 字节 description 的巨型 schema，仅删描述即应落入预算（业内同款判例）。
func TestCompactSchemaForModel_HugeDescriptionsStripped(t *testing.T) {
	huge := strings.Repeat("x", 4500)
	in := map[string]interface{}{
		"type":        "object",
		"description": huge,
		"properties": map[string]interface{}{
			"metadata": map[string]interface{}{
				"$ref": "#/$defs/metadata",
			},
		},
		"$defs": map[string]interface{}{
			"metadata": map[string]interface{}{
				"type":        "string",
				"description": "Metadata value",
			},
		},
	}
	if marshalLen(t, in) <= MaxModelSchemaBytes {
		t.Fatalf("测试前提失效：构造的 schema 未超预算（%d）", marshalLen(t, in))
	}
	out := CompactSchemaForModel(in)

	if marshalLen(t, out) > MaxModelSchemaBytes {
		t.Fatalf("压缩后仍超预算：%d > %d", marshalLen(t, out), MaxModelSchemaBytes)
	}
	if _, ok := out["description"]; ok {
		t.Error("顶层 description 未被剥离")
	}
	// 结构面必须保留：properties/$defs/$ref 都还在（只掉描述，不掉骨架）
	props, _ := out["properties"].(map[string]interface{})
	meta, _ := props["metadata"].(map[string]interface{})
	if meta == nil || meta["$ref"] != "#/$defs/metadata" {
		t.Errorf("properties.metadata.$ref 被误伤：%#v", out["properties"])
	}
	defs, _ := out["$defs"].(map[string]interface{})
	def, _ := defs["metadata"].(map[string]interface{})
	if def == nil || def["type"] != "string" {
		t.Errorf("$defs.metadata.type 被误伤：%#v", out["$defs"])
	}
	if _, ok := def["description"]; ok {
		t.Error("$defs 内 description 未被剥离")
	}
}

// TestCompactSchemaForModel_DeepObjectsCollapsed：字节不够时，深层（depth>=3）复杂对象折叠为 {}。
// 构造要点：**字节全压在 depth>=3 的对象内部、且不带 description**，这样 pass1/pass2
// 都是空转，只有 pass3（深度折叠）能把它拉回预算 —— 从而精确地判别 pass3 是否生效。
func TestCompactSchemaForModel_DeepObjectsCollapsed(t *testing.T) {
	// depth-3 对象里挂两个超长 key（key 不会被任何 pass 剥离，只有折叠整层才能消掉）。
	deepProps := map[string]interface{}{
		strings.Repeat("k", 3000): map[string]interface{}{"type": "string"},
		strings.Repeat("j", 3000): map[string]interface{}{"type": "number"},
	}
	c := map[string]interface{}{"type": "object", "properties": deepProps}                      // depth 3
	b := map[string]interface{}{"type": "object", "properties": map[string]interface{}{"c": c}} // depth 2
	a := map[string]interface{}{"type": "object", "properties": map[string]interface{}{"b": b}} // depth 1
	root := map[string]interface{}{"type": "object", "properties": map[string]interface{}{"a": a}}

	if marshalLen(t, root) <= MaxModelSchemaBytes {
		t.Fatalf("测试前提失效：构造的 schema 未超预算（%d）", marshalLen(t, root))
	}
	out := CompactSchemaForModel(root)
	if marshalLen(t, out) > MaxModelSchemaBytes {
		t.Fatalf("压缩后仍超预算：%d", marshalLen(t, out))
	}
	// 精确断言：depth-3 的对象 c 被折叠为空，而 depth-1/2 的 a/b 骨架保留。
	p, _ := out["properties"].(map[string]interface{})
	aOut, _ := p["a"].(map[string]interface{})
	if aOut == nil {
		t.Fatalf("depth-1 的 a 被误删：%#v", out)
	}
	bOut, _ := aOut["properties"].(map[string]interface{})
	bVal, _ := bOut["b"].(map[string]interface{})
	if bVal == nil {
		t.Fatalf("depth-2 的 b 被误删：%#v", aOut)
	}
	cOut, _ := bVal["properties"].(map[string]interface{})
	cVal, _ := cOut["c"].(map[string]interface{})
	if cVal == nil {
		t.Fatalf("depth-3 的 c 被整个删掉（应折叠为空对象）：%#v", bVal)
	}
	if len(cVal) != 0 {
		t.Errorf("depth-3 的复杂对象未被折叠：%#v", cVal)
	}
}

// TestCompactSchemaForModel_DoesNotMutateInput：★ 核心不变量 —— 本体零改动。
// 本体被 SchemaValidateStage（pipeline.go:610）复用；被就地改坏 ⇒ 模型按残缺
// schema 传参会被拒。此测必须能判红（见 overlay mutant 自证）。
func TestCompactSchemaForModel_DoesNotMutateInput(t *testing.T) {
	in := map[string]interface{}{
		"type":        "object",
		"description": strings.Repeat("x", 6000),
		"properties": map[string]interface{}{
			"p": map[string]interface{}{
				"type":        "string",
				"description": strings.Repeat("y", 2000),
			},
		},
		"required": []string{"p"},
	}
	before := marshalLen(t, in)
	_ = CompactSchemaForModel(in)
	after := marshalLen(t, in)
	if before != after {
		t.Fatalf("本体被就地修改了：before=%d after=%d", before, after)
	}
	if _, ok := in["description"]; !ok {
		t.Error("本体的顶层 description 被就地删除 —— 本体污染")
	}
	props, _ := in["properties"].(map[string]interface{})
	p, _ := props["p"].(map[string]interface{})
	if p == nil || p["description"] == nil {
		t.Error("本体嵌套 description 被就地删除 —— 本体污染")
	}
}

// TestCompactSchemaForModel_NilSafe：nil 输入不得 panic。
func TestCompactSchemaForModel_NilSafe(t *testing.T) {
	if out := CompactSchemaForModel(nil); out != nil {
		t.Errorf("nil 输入应返回 nil，得到 %#v", out)
	}
}

// TestCompactSchemaForModel_BuiltinSchemasAreNoOp：安全包络的形式化。
// 全部内建 schema 都不超预算 ⇒ 投影对它们是恒等映射（零行为变化）。
// 若将来有人加了个巨大的内建 schema，这条会红 —— 提醒他要么收描述，要么显式声明确实要被压。
func TestCompactSchemaForModel_BuiltinSchemasAreNoOp(t *testing.T) {
	r := NewRegistry()
	for _, spec := range r.All() {
		if len(spec.InputSchema) == 0 {
			continue
		}
		before := marshalLen(t, spec.InputSchema)
		out := CompactSchemaForModel(spec.InputSchema)
		if marshalLen(t, out) != before {
			t.Errorf("内建 %s 的 schema 被压缩了（%d → %d）—— 内建应当全部在预算内",
				spec.Name, before, marshalLen(t, out))
		}
	}
}
