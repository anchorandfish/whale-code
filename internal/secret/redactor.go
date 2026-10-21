// Package secret — C15 统一脱敏接口 Redactor（主导，M22-02 在 P2-G7 复用）。
//
// 设计目标（T0-2 §2 C15 仲裁）：
// - 收口 internal/secret + internal/feedback + internal/log 三处重复脱敏逻辑
// - 提供 Redact(input string) string / RedactStruct(s any) any 双入口
// - M22-02 结构体字段脱敏通过 RedactStruct 复用，不重复实现
// - 与 H2 已有的 security.Redactor 接口保持兼容（RedactSensitive 方法）
//
// 实施说明（SNAPSHOT）：
// - 本文件定义 Redactor 接口 + 默认 defaultRedactor 实现
// - defaultRedactor 委托 RedactSensitive（redact.go）+ RedactStruct（本文件）
// - 启动阶段通过 SetRedactor 注入 security.Checker（与 H2/H3 既有路径一致）
package secret

import (
	"reflect"
	"strings"
)

// Redactor 是 C15 仲裁的统一脱敏接口（SNAPSHOT）。
//
// 方法语义：
// - Redact(s string) string：对字符串做密钥脱敏，命中规则替换为 [REDACTED]
// - RedactStruct(s any) any：对结构体/string 切片/map 递归脱敏，返回新值（不修改原值）
//
// 实现约定：
// - 默认实现 defaultRedactor 委托 RedactSensitive（已编译的正则集）
// - 并发安全：实现需支持多 goroutine 并发调用
// - 性能：单次 Redact 调用应 O(n)（n=输入长度），不依赖 Pattern 数量
type Redactor interface {
	// Redact 对字符串做密钥脱敏。
	Redact(s string) string

	// RedactStruct 对结构体递归脱敏，返回新结构体（深拷贝 string 字段）。
	// 支持 string 字段、[]string、map[string]string 三种常见形态。
	RedactStruct(s any) any
}

// defaultRedactor 是 Redactor 的默认实现（委托 RedactSensitive + reflect 递归）。
type defaultRedactor struct{}

// NewRedactor 创建默认 Redactor 实例。
func NewRedactor() Redactor {
	return defaultRedactor{}
}

// Redact 实现 Redactor.Redact（委托 RedactSensitive，保持与 H2/H3 既有路径兼容）。
func (defaultRedactor) Redact(s string) string {
	return RedactSensitive(s)
}

// RedactStruct 实现 Redactor.RedactStruct。
//
// 递归处理：
// - string → Redact(string)
// - *T → 解引用后递归
// - []T → 逐元素递归
// - map[K]V → 逐 value 递归（key 不脱敏，假设 key 是字段名）
// - struct → 逐字段递归（含非导出字段，需 unsafe；本实现仅处理导出字段）
// - 其他类型（int/bool/...）→ 原值返回
//
// 性能：reflect 有开销，建议仅用于低频场景（日志/导出），不用于热路径。
// 并发安全：无共享状态，纯函数式。
func (defaultRedactor) RedactStruct(s any) any {
	return redactValue(reflect.ValueOf(s))
}

// redactValue 递归脱敏 reflect.Value。
//
// 返回新值（不修改原值）。原值不可寻址时返回副本。
func redactValue(v reflect.Value) any {
	switch v.Kind() {
	case reflect.String:
		return RedactSensitive(v.String())
	case reflect.Ptr:
		if v.IsNil() {
			return nil
		}
		// 解引用后递归，返回指针指向的新值（不返回指针，避免修改原值）
		return redactValue(v.Elem())
	case reflect.Slice:
		if v.IsNil() {
			return nil
		}
		out := make([]any, v.Len())
		for i := 0; i < v.Len(); i++ {
			out[i] = redactValue(v.Index(i))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		out := make(map[string]any, v.Len())
		for _, key := range v.MapKeys() {
			// key 转字符串（假设 key 是 string 或可 String()）
			keyStr := key.String()
			out[keyStr] = redactValue(v.MapIndex(key))
		}
		return out
	case reflect.Struct:
		out := make(map[string]any, v.NumField())
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := t.Field(i)
			// 跳过非导出字段（PkgPath 非空表示非导出）
			if field.PkgPath != "" {
				continue
			}
			out[field.Name] = redactValue(v.Field(i))
		}
		return out
	default:
		// int/bool/float 等原值返回
		if v.CanInterface() {
			return v.Interface()
		}
		return nil
	}
}

// === 全局 Redactor 单例（与 security.Redactor 接口桥接）===

// SecretRedactor 实现 security.Redactor 接口（H2 改进方案 L130-L133）。
//
// 通过结构化类型满足 security.Redactor 的 RedactSensitive(s string) string 方法，
// 无需 import security 包（Go 接口隐式实现），避免循环导入。
//
// 启动阶段调用：
//
//	security.SetRedactor(secret.SecretRedactor{}) // 在 tui/lifecycle.go 中
//
// SNAPSHOT：本结构体保留为 legacy 适配层，新增 C15 Redactor 接口由
// defaultRedactor 实现（本文件上方）。两者并存，调用方按需选择。
type SecretRedactor struct{}

// RedactSensitive 委托 secret.RedactSensitive 进行密钥脱敏。
// 满足 security.Redactor 接口要求。
func (SecretRedactor) RedactSensitive(s string) string {
	return RedactSensitive(s)
}

// Redact 委托 RedactSensitive，满足 secret.Redactor 接口（C15 扩展）。
// 允许 SecretRedactor 同时实现 security.Redactor + secret.Redactor 两个接口。
func (SecretRedactor) Redact(s string) string {
	return RedactSensitive(s)
}

// RedactStruct 满足 secret.Redactor 接口（C15 扩展）。
// 委托 defaultRedactor.RedactStruct 实现。
func (s SecretRedactor) RedactStruct(input any) any {
	return defaultRedactor{}.RedactStruct(input)
}

// globalRedactor 是全局 Redactor 实例（启动阶段注入）。
var globalRedactor Redactor = defaultRedactor{}

// SetGlobalRedactor 注入全局 Redactor 实例。
// 应在启动阶段调用一次（main.go / tui/app.go）。
func SetGlobalRedactor(r Redactor) {
	globalRedactor = r
}

// GetGlobalRedactor 返回全局 Redactor 实例（默认 defaultRedactor）。
func GetGlobalRedactor() Redactor {
	return globalRedactor
}

// Redact 是包级便捷函数：调用全局 Redactor.Redact。
func Redact(s string) string {
	return globalRedactor.Redact(s)
}

// RedactStructGlobal 是包级便捷函数：调用全局 Redactor.RedactStruct。
func RedactStructGlobal(s any) any {
	return globalRedactor.RedactStruct(s)
}

// RedactMap 便捷函数：对 map[string]string 整体脱敏。
//
// 用于 M22-02 结构体字段脱敏场景：将结构体转为 map 后整体脱敏。
func RedactMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = RedactSensitive(v)
	}
	return out
}

// RedactSlice 便捷函数：对 []string 切片整体脱敏。
func RedactSlice(s []string) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = RedactSensitive(v)
	}
	return out
}

// RedactField 便捷函数：脱敏单个字段（用于 reflect.Value.String() 后处理）。
//
// 比直接 RedactSensitive 多一层判断：空字符串直接返回，避免无谓正则匹配。
func RedactField(s string) string {
	if s == "" {
		return s
	}
	if !strings.ContainsAny(s, "=: \n\r\t") && !strings.Contains(s, "sk-") &&
		!strings.Contains(s, "ghp_") && !strings.Contains(s, "gho_") &&
		!strings.Contains(s, "ghu_") && !strings.Contains(s, "ghs_") &&
		!strings.Contains(s, "ghr_") && !strings.Contains(s, "AKIA") &&
		!strings.Contains(s, "ASIA") && !strings.Contains(s, "xox") &&
		!strings.Contains(s, "eyJ") && !strings.Contains(s, "AIza") &&
		!strings.Contains(s, "Bearer ") && !strings.Contains(s, "bearer ") &&
		!strings.Contains(s, "-----BEGIN ") {
		// 快速路径：不含任何密钥特征前缀，直接返回
		return s
	}
	return RedactSensitive(s)
}
