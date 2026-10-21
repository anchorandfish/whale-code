package ai

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestExtractJSON 测试从各种格式中提取 JSON
func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		wantKey string // expected提取的 JSON 中包含的 key（简单验证）
		wantVal string // expected的值
	}{
		{
			name:    "纯 JSON 对象",
			input:   `{"key":"value"}`,
			wantErr: false,
			wantKey: "key",
			wantVal: "value",
		},
		{
			name:    "Markdown 代码块中的 JSON",
			input:   "```json\n{\"key\":\"value\"}\n```",
			wantErr: false,
			wantKey: "key",
			wantVal: "value",
		},
		{
			name:    "JSON 周围有文本",
			input:   "Here is the result:\n{\"key\":\"value\"}\nDone.",
			wantErr: false,
			wantKey: "key",
			wantVal: "value",
		},
		{
			name:    "JSON 数组",
			input:   `[1,2,3]`,
			wantErr: false,
		},
		{
			name:    "嵌套 JSON",
			input:   `{"outer":{"inner":"deep"}}`,
			wantErr: false,
			wantKey: "outer",
		},
		{
			name:    "无效文本应报错",
			input:   "this is not json at all",
			wantErr: true,
		},
		{
			name:    "空字符串应报错",
			input:   "",
			wantErr: true,
		},
		{
			name:    "带空白的 JSON",
			input:   "  \n  {\"key\":\"value\"}  \n  ",
			wantErr: false,
			wantKey: "key",
			wantVal: "value",
		},
		{
			name:    "Markdown 代码块无语言标记",
			input:   "```\n{\"key\":\"value\"}\n```",
			wantErr: false,
			wantKey: "key",
			wantVal: "value",
		},
		{
			name:    "多个代码块取第一个",
			input:   "```json\n{\"first\":1}\n```\nSome text\n```json\n{\"second\":2}\n```",
			wantErr: false,
			wantKey: "first",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := extractJSON(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("extractJSON(%q) expected返回错误，但未返回", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("extractJSON(%q) 返回意外错误: %v", tt.input, err)
				return
			}
			// 验证提取结果是有效 JSON
			var parsed interface{}
			if err := json.Unmarshal([]byte(result), &parsed); err != nil {
				t.Errorf("extractJSON(%q) 结果不是有效 JSON: %q, 错误: %v", tt.input, result, err)
			}
			// 如果指定了 wantKey，验证 key 存在
			if tt.wantKey != "" {
				if m, ok := parsed.(map[string]interface{}); ok {
					if _, ok := m[tt.wantKey]; !ok {
						t.Errorf("extractJSON(%q) 结果缺少 key %q", tt.input, tt.wantKey)
					}
					if tt.wantVal != "" {
						if val, _ := m[tt.wantKey].(string); val != tt.wantVal {
							t.Errorf("extractJSON(%q) 结果[%q] = %q, want %q", tt.input, tt.wantKey, val, tt.wantVal)
						}
					}
				}
			}
		})
	}
}

// TestValidateAgainstSchema 测试 Schema 验证
func TestValidateAgainstSchema(t *testing.T) {
	tests := []struct {
		name        string
		data        map[string]interface{}
		schema      map[string]interface{}
		wantErrs    int // expected的错误数量
		wantErrSubj string
	}{
		{
			name: "有效数据匹配 schema",
			data: map[string]interface{}{
				"name": "Alice",
				"age":  float64(30),
			},
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{"type": "string"},
					"age":  map[string]interface{}{"type": "number"},
				},
				"required": []interface{}{"name", "age"},
			},
			wantErrs: 0,
		},
		{
			name: "missing required field",
			data: map[string]interface{}{
				"name": "Alice",
			},
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{"type": "string"},
					"age":  map[string]interface{}{"type": "number"},
				},
				"required": []interface{}{"name", "age"},
			},
			wantErrs:    1,
			wantErrSubj: "age",
		},
		{
			name: "类型不匹配 string vs number",
			data: map[string]interface{}{
				"age": "thirty",
			},
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"age": map[string]interface{}{"type": "number"},
				},
				"required": []interface{}{"age"},
			},
			wantErrs:    1,
			wantErrSubj: "age",
		},
		{
			name: "可选字段缺失应通过",
			data: map[string]interface{}{
				"name": "Alice",
			},
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{"type": "string"},
					"bio":  map[string]interface{}{"type": "string"},
				},
				"required": []interface{}{"name"},
			},
			wantErrs: 0,
		},
		{
			name: "嵌套对象验证",
			data: map[string]interface{}{
				"address": map[string]interface{}{
					"city": "Beijing",
				},
			},
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"address": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"city": map[string]interface{}{"type": "string"},
						},
						"required": []interface{}{"city"},
					},
				},
				"required": []interface{}{"address"},
			},
			wantErrs: 0,
		},
		{
			name: "数组类型验证",
			data: map[string]interface{}{
				"tags": []interface{}{"go", "ai"},
			},
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"tags": map[string]interface{}{"type": "array"},
				},
				"required": []interface{}{"tags"},
			},
			wantErrs: 0,
		},
		{
			name: "布尔类型验证",
			data: map[string]interface{}{
				"active": true,
			},
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"active": map[string]interface{}{"type": "boolean"},
				},
				"required": []interface{}{"active"},
			},
			wantErrs: 0,
		},
		{
			name: "整数类型验证",
			data: map[string]interface{}{
				"count": float64(42),
			},
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"count": map[string]interface{}{"type": "integer"},
				},
				"required": []interface{}{"count"},
			},
			wantErrs: 0,
		},
		{
			name: "嵌套对象missing required field",
			data: map[string]interface{}{
				"address": map[string]interface{}{},
			},
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"address": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"city": map[string]interface{}{"type": "string"},
						},
						"required": []interface{}{"city"},
					},
				},
				"required": []interface{}{"address"},
			},
			wantErrs:    1,
			wantErrSubj: "city",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateAgainstSchema(tt.data, tt.schema)
			if len(errs) != tt.wantErrs {
				t.Errorf("validateAgainstSchema 返回 %d 个错误, want %d, 错误: %v", len(errs), tt.wantErrs, errs)
			}
			if tt.wantErrSubj != "" && len(errs) > 0 {
				found := false
				for _, e := range errs {
					if contains(e, tt.wantErrSubj) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("validateAgainstSchema 错误中未包含 %q, 错误: %v", tt.wantErrSubj, errs)
				}
			}
		})
	}
}

// contains 检查字符串 s 是否包含 substr
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestMinifySchema 测试 Schema 精简
func TestMinifySchema(t *testing.T) {
	tests := []struct {
		name   string
		input  map[string]interface{}
		expect map[string]interface{}
	}{
		{
			name: "移除 description/examples/default/title",
			input: map[string]interface{}{
				"type":        "object",
				"description": "A person",
				"title":       "Person",
				"default":     map[string]interface{}{"name": "unknown"},
				"examples":    []interface{}{map[string]interface{}{"name": "Alice"}},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Person's name",
					},
				},
				"required": []interface{}{"name"},
			},
			expect: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type": "string",
					},
				},
				"required": []interface{}{"name"},
			},
		},
		{
			name: "递归精简嵌套 properties",
			input: map[string]interface{}{
				"type":        "object",
				"description": "Root",
				"properties": map[string]interface{}{
					"address": map[string]interface{}{
						"type":        "object",
						"description": "Address info",
						"properties": map[string]interface{}{
							"city": map[string]interface{}{
								"type":        "string",
								"description": "City name",
							},
						},
					},
				},
			},
			expect: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"address": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"city": map[string]interface{}{
								"type": "string",
							},
						},
					},
				},
			},
		},
		{
			name: "递归精简 items",
			input: map[string]interface{}{
				"type":        "array",
				"description": "List of items",
				"items": map[string]interface{}{
					"type":        "object",
					"description": "An item",
					"properties": map[string]interface{}{
						"id": map[string]interface{}{
							"type":        "integer",
							"description": "Item ID",
						},
					},
				},
			},
			expect: map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id": map[string]interface{}{
							"type": "integer",
						},
					},
				},
			},
		},
		{
			name:   "空 schema",
			input:  map[string]interface{}{},
			expect: map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MinifySchema(tt.input)
			resultJSON, _ := json.Marshal(result)
			expectJSON, _ := json.Marshal(tt.expect)
			if string(resultJSON) != string(expectJSON) {
				t.Errorf("MinifySchema 结果:\n%s\nexpected:\n%s", resultJSON, expectJSON)
			}
		})
	}
}

// TestBuildStructuredOutputPrompt 测试提示词生成
func TestBuildStructuredOutputPrompt(t *testing.T) {
	t.Run("包含 schema JSON", func(t *testing.T) {
		schema := &ResponseSchema{
			Name:        "test_schema",
			Description: "A test schema",
			Schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{"type": "string"},
				},
				"required": []interface{}{"name"},
			},
			Strict: true,
		}
		prompt := buildStructuredOutputPrompt(schema)
		if prompt == "" {
			t.Error("buildStructuredOutputPrompt 返回空字符串")
		}
		// 验证包含 schema 中的关键字段
		if !containsSubstr(prompt, "name") {
			t.Errorf("提示词中should contain 'name'，actual: %s", prompt)
		}
		if !containsSubstr(prompt, "string") {
			t.Errorf("提示词中should contain 'string'，actual: %s", prompt)
		}
		if !containsSubstr(prompt, "object") {
			t.Errorf("提示词中should contain 'object'，actual: %s", prompt)
		}
	})

	t.Run("包含关键指令", func(t *testing.T) {
		schema := &ResponseSchema{
			Name:   "test",
			Schema: map[string]interface{}{"type": "object"},
		}
		prompt := buildStructuredOutputPrompt(schema)
		// should contain JSON 相关指令
		if !containsSubstr(prompt, "JSON") {
			t.Errorf("提示词中should contain 'JSON' 关键字，actual: %s", prompt)
		}
		if !containsSubstr(prompt, "schema") {
			t.Errorf("提示词中should contain 'schema' 关键字，actual: %s", prompt)
		}
	})

	t.Run("空 description 处理", func(t *testing.T) {
		schema := &ResponseSchema{
			Name:        "test",
			Description: "",
			Schema:      map[string]interface{}{"type": "object"},
		}
		prompt := buildStructuredOutputPrompt(schema)
		if prompt == "" {
			t.Error("空 description 时应仍能生成提示词")
		}
	})
}

// TestBuildRetryMessage test retry消息生成
func TestBuildRetryMessage(t *testing.T) {
	t.Run("有验证错误", func(t *testing.T) {
		validationErrors := []string{"字段 'name' 缺失", "字段 'age' 类型错误"}
		msg := buildRetryMessage(validationErrors, nil)
		if msg == "" {
			t.Error("buildRetryMessage should return non-empty字符串")
		}
		if !containsSubstr(msg, "name") {
			t.Errorf("重试消息中should contain 'name'，actual: %s", msg)
		}
	})

	t.Run("有解析错误", func(t *testing.T) {
		parseErr := fmt.Errorf("invalid JSON at position 5")
		msg := buildRetryMessage(nil, parseErr)
		if msg == "" {
			t.Error("buildRetryMessage should return non-empty字符串")
		}
		if !containsSubstr(msg, "JSON") {
			t.Errorf("重试消息中should contain 'JSON'，actual: %s", msg)
		}
	})

	t.Run("同时有验证和解析错误", func(t *testing.T) {
		validationErrors := []string{"字段 'name' 缺失"}
		parseErr := fmt.Errorf("parse error")
		msg := buildRetryMessage(validationErrors, parseErr)
		if msg == "" {
			t.Error("buildRetryMessage should return non-empty字符串")
		}
	})
}

// TestValidateFieldType 测试字段类型验证
func TestValidateFieldType(t *testing.T) {
	tests := []struct {
		name         string
		fieldName    string
		value        interface{}
		expectedType string
		wantErr      bool
	}{
		{"string 类型正确", "name", "Alice", "string", false},
		{"string 类型错误", "name", 123, "string", true},
		{"number 类型正确", "age", float64(30), "number", false},
		{"number 类型错误", "age", "thirty", "number", true},
		{"integer 类型正确", "count", float64(42), "integer", false},
		{"integer 类型错误-小数", "count", float64(3.14), "integer", true},
		{"integer 类型错误-字符串", "count", "42", "integer", true},
		{"boolean 类型正确", "active", true, "boolean", false},
		{"boolean 类型错误", "active", "true", "boolean", true},
		{"array 类型正确", "tags", []interface{}{"go"}, "array", false},
		{"array 类型错误", "tags", "go", "array", true},
		{"object 类型正确", "data", map[string]interface{}{"key": "val"}, "object", false},
		{"object 类型错误", "data", "not object", "object", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFieldType(tt.fieldName, tt.value, tt.expectedType)
			if tt.wantErr && err == nil {
				t.Errorf("validateFieldType(%q, %v, %q) expected返回错误，但未返回", tt.fieldName, tt.value, tt.expectedType)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validateFieldType(%q, %v, %q) 返回意外错误: %v", tt.fieldName, tt.value, tt.expectedType, err)
			}
		})
	}
}

// TestResponseSchema 测试 ResponseSchema 结构体序列化
func TestResponseSchema(t *testing.T) {
	schema := ResponseSchema{
		Name:        "person",
		Description: "A person object",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string"},
			},
		},
		Strict: true,
	}

	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("序列化 ResponseSchema 失败: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}

	if name, _ := decoded["name"].(string); name != "person" {
		t.Errorf("name = %q, want 'person'", name)
	}
	if _, ok := decoded["description"]; !ok {
		t.Error("缺少 description 字段")
	}
	if _, ok := decoded["schema"]; !ok {
		t.Error("缺少 schema 字段")
	}
	if strict, _ := decoded["strict"].(bool); !strict {
		t.Error("strict 应为 true")
	}

	// 测试 omitempty: Description 为空时不序列化
	schema2 := ResponseSchema{
		Name:   "test",
		Schema: map[string]interface{}{"type": "object"},
	}
	data2, _ := json.Marshal(schema2)
	var decoded2 map[string]interface{}
	json.Unmarshal(data2, &decoded2)
	if _, ok := decoded2["description"]; ok {
		t.Error("Description 为空时不应序列化")
	}
}

// TestStructuredOutputResult 测试 StructuredOutputResult 结构体
func TestStructuredOutputResult(t *testing.T) {
	result := StructuredOutputResult{
		Data: map[string]interface{}{
			"name": "Alice",
			"age":  float64(30),
		},
		Raw:              `{"name":"Alice","age":30}`,
		Validated:        true,
		ValidationErrors: nil,
		ProviderUsed:     MethodOpenAINative,
	}

	if !result.Validated {
		t.Error("Validated 应为 true")
	}
	if result.ProviderUsed != MethodOpenAINative {
		t.Errorf("ProviderUsed = %q, want %q", result.ProviderUsed, MethodOpenAINative)
	}
	if result.Data["name"] != "Alice" {
		t.Errorf("Data['name'] = %v, want 'Alice'", result.Data["name"])
	}
}

// TestStructuredOutputMethod 测试 StructuredOutputMethod 常量
func TestStructuredOutputMethod(t *testing.T) {
	if MethodOpenAINative != "openai_native" {
		t.Errorf("MethodOpenAINative = %q, want 'openai_native'", MethodOpenAINative)
	}
	if MethodAnthropicToolUse != "anthropic_tool_use" {
		t.Errorf("MethodAnthropicToolUse = %q, want 'anthropic_tool_use'", MethodAnthropicToolUse)
	}
	if MethodFallbackPrompt != "fallback_prompt" {
		t.Errorf("MethodFallbackPrompt = %q, want 'fallback_prompt'", MethodFallbackPrompt)
	}
}
