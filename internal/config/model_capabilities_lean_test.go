// Package config — B5：`ModelCapabilities` 不许有"零读取"能力位。
//
// `ModelCapabilities` 的字段集（= JSON key 集）是**对外契约**：用户照着它写
// settings.json 的 `model_capabilities`。曾经的 `supports_thinking` /
// `supports_cache` 两个键在**全仓零读取**（wiring_test.go 早把整块登记为
// "孤岛字段"）—— 写进去不生效，却看起来像能配，属 「同一事实多源」。
// B5 删除它们，并用本门钉住"不许无声复活"。
package config

import (
	"bytes"
	"log"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestModelCapabilities_NoDeadCapabilityBits 是 B5 的结构面门。
//
// ★ 判别性：把 `SupportsThinking bool ` + "`json:\"supports_thinking\"`" + ` 加回去
// ⇒ 字段集不等 ⇒ 本判据红（并打印出"新增字段前先确认它有生产读取点"的提示）。
func TestModelCapabilities_NoDeadCapabilityBits(t *testing.T) {
	typ := reflect.TypeOf(ModelCapabilities{})

	var keys []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("json")
		key := strings.Split(tag, ",")[0]
		if key == "" || key == "-" {
			t.Errorf("字段 %s 缺少 json tag（对外契约必须有稳定的 key）", f.Name)
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	want := []string{"supports_effort", "supports_max_effort", "supports_native_search"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf(`ModelCapabilities 的 JSON key 集变了：
  got  = %v
  want = %v
★ 新增字段前先确认它有**生产读取点** —— 零读取的能力位只会制造"配了会生效"的错觉
  （B5 删掉的 supports_thinking / supports_cache 就是这样）`, keys, want)
	}
}

// TestModelCapabilities_DeprecatedKeysIgnoredWithLog 钉住两件**不同层面**的事实：
//
// 1. **结构化事实**（配置解析）：老的 `supports_thinking` / `supports_cache`
// 写进 settings.json 后**不改动任何东西** —— 不报错、不落任何位。
// 2. **产品面事实**（用户可见提示）：不能**静默**忽略，必须打一条 log 指路。
// 用户写了却以为生效，正是 那类"同一事实多源"误解。
//
// 两层分开断言是有意的：结构化事实**不靠**散文关键词来判定（那会犯铁律）；
// 这里的 log 断言是**产品面产物**本身（告警就是交付物），属合法断言面。
//
// ★ 判别性：删掉 config.go 里的两条 `log.Printf(... is IGNORED ...)`
// ⇒ 第 2 层红；把废弃键重新解析回某个字段 ⇒ 第 1 层红。两条路都动不了本判据才算真绿。
func TestModelCapabilities_DeprecatedKeysIgnoredWithLog(t *testing.T) {
	raw := map[string]interface{}{
		"model_capabilities": map[string]interface{}{
			"my-model": map[string]interface{}{
				"supports_effort":        true,
				"supports_native_search": true,
				// 两个已废弃键：**存在但必须无效果**，且必须留下告警
				"supports_thinking": true,
				"supports_cache":    true,
			},
		},
	}

	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()

	c := Config{}
	mergeInto(&c, raw) // 不 panic 即证"废弃键不致命"

	// —— 层 1：结构化事实 ——
	caps, ok := c.ModelCapabilities["my-model"]
	if !ok {
		t.Fatalf("model_capabilities[my-model] 未被解析出来（mergeInto 丢条目）")
	}
	if !caps.SupportsEffort {
		t.Errorf("supports_effort 未生效：got=false（既有键被误伤）")
	}
	if !caps.SupportsNativeSearch {
		t.Errorf("supports_native_search 未生效：got=false（既有键被误伤）")
	}

	// —— 层 2：产品面告警 ——
	out := buf.String()
	for _, want := range []string{
		"supports_thinking is IGNORED",
		"supports_cache is IGNORED",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("废弃键缺告警：log 应含 %q，实际 log=\n%s", want, out)
		}
	}
}
