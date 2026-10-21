// Package tools — N16 tool schema 强类型化。
// 本文件定义 ToolSchema 强类型结构与 BuildSchemaFromStruct 反射生成器，
// 解决对标分析 G-03（schema 与 Input 结构体字段不一致 / 手写 map 易错）。
// 递归深度上限 3 层，超过降级为 type:"object" 不展开 properties（v2 补充，回应审查报告必改2）。
package tools

import (
	"encoding/json"
	"reflect"
	"strings"
)

// ToolSchema 强类型的工具输入 schema，替代 map[string]interface{}。
// 与 ai.ToolDefinition.InputSchema 通过 ToMap() 桥接，保持向后兼容。
type ToolSchema struct {
	Type       string                     `json:"type"`
	Properties map[string]*SchemaProperty `json:"properties,omitempty"`
	Required   []string                   `json:"required,omitempty"`
}

// SchemaProperty 单个参数的 schema 定义，支持嵌套与数组。
type SchemaProperty struct {
	Type        string                     `json:"type"`
	Description string                     `json:"description,omitempty"`
	Enum        []interface{}              `json:"enum,omitempty"`
	Default     interface{}                `json:"default,omitempty"`
	Minimum     *float64                   `json:"minimum,omitempty"`
	Maximum     *float64                   `json:"maximum,omitempty"`
	Items       *SchemaProperty            `json:"items,omitempty"`
	Properties  map[string]*SchemaProperty `json:"properties,omitempty"`
	Required    []string                   `json:"required,omitempty"`
}

// ToMap 转换为 map[string]interface{}，向后兼容 ai.ToolDefinition.InputSchema。
// nil 接收者返回最小 object schema，避免 nil panic。
func (s *ToolSchema) ToMap() map[string]interface{} {
	if s == nil {
		return map[string]interface{}{"type": "object"}
	}
	b, _ := json.Marshal(s)
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	return m
}

// BuildSchemaFromStruct 从 Go 结构体反射生成 ToolSchema。
// 确保 schema 与 Input 结构体字段一致（解决 G-03）。
// 递归深度上限 3 层（v2 补充，回应审查报告必改2），超过降级为 type:"object" 不展开 properties。
// json:"-" 字段被排除（如 ExpectedMtime 不暴露给 AI）。
func BuildSchemaFromStruct(v interface{}) *ToolSchema {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	schema := &ToolSchema{
		Type:       "object",
		Properties: make(map[string]*SchemaProperty),
	}
	visited := make(map[reflect.Type]bool) // 循环引用防护
	required := []string{}
	buildProperties(t, schema.Properties, &required, 1, visited)
	schema.Required = required
	return schema
}

// buildProperties 递归生成字段属性（depth 从 1 起，>3 终止）。
// visited 用于循环引用防护，defer delete 实现回溯，允许兄弟节点复用同类型。
func buildProperties(t reflect.Type, props map[string]*SchemaProperty, required *[]string, depth int, visited map[reflect.Type]bool) {
	if depth > 3 || visited[t] {
		return // 深度超限或循环引用，终止递归
	}
	visited[t] = true
	defer delete(visited, t) // 回溯：允许兄弟节点复用同类型

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		jsonTag := f.Tag.Get("json")
		if jsonTag == "-" {
			continue // 跳过隐藏字段
		}
		name := strings.Split(jsonTag, ",")[0]
		if name == "" {
			name = f.Name
		}

		ft := f.Type
		if ft.Kind() == reflect.Ptr {
			ft = ft.Elem() // 解引用指针
		}

		prop := &SchemaProperty{
			Type:        goTypeToJSONType(ft),
			Description: f.Tag.Get("desc"),
		}

		// 嵌套 struct 递归生成 Properties（depth < 3 时）
		if ft.Kind() == reflect.Struct && depth < 3 {
			prop.Properties = make(map[string]*SchemaProperty)
			nestedRequired := []string{}
			buildProperties(ft, prop.Properties, &nestedRequired, depth+1, visited)
			prop.Required = nestedRequired // 嵌套 required
		}

		props[name] = prop
		if !strings.Contains(jsonTag, "omitempty") {
			*required = append(*required, name)
		}
	}
}

// goTypeToJSONType 将 Go 类型映射到 JSON Schema type。
// 未识别类型默认 string，保证总能产出有效 schema。
func goTypeToJSONType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Map, reflect.Struct:
		return "object"
	default:
		return "string"
	}
}
