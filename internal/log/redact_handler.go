// Package log — M22-02 结构体字段脱敏 Handler（SNAPSHOT）
//
// 本文件实现自定义 slog.Handler，对结构体字段进行递归脱敏。
// 对标 slog 自定义 Handler（https://pkg.go.dev/log/slog#Handler）。
//
// 设计要点（候选 A 选定）：
// - 接口签名：RedactHandler 包装 slog.Handler，递归脱敏
// - 结构体字段：redactConfig 含 SensitiveKeys/Redactor/MaxDepth
// - 分层权责：本文件只做"Handler 脱敏"，密钥正则由 internal/secret
// - 降级策略：脱敏失败保留原值（避免日志丢失）
// - TTL规则：无 TTL（每次 Handle 调用即时脱敏）
package log

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/secret"
)

// redactMaxDepth 是结构体递归脱敏最大深度（防循环引用，5 层足够）。
const redactMaxDepth = 5

// redactSensitiveKeys 是需脱敏的字段名集合（小写匹配）。
var redactSensitiveKeys = map[string]bool{
	"password":      true,
	"passwd":        true,
	"token":         true,
	"secret":        true,
	"api_key":       true,
	"apikey":        true,
	"access_token":  true,
	"refresh_token": true,
	"private_key":   true,
	"client_secret": true,
	"authorization": true,
	"auth":          true,
	"credential":    true,
	"credentials":   true,
}

// RedactHandler 是脱敏 slog.Handler 包装器（SNAPSHOT-1）。
//
// 包装底层 handler，在 Handle 调用时对 slog.Record 的属性递归脱敏：
// - string 值调用 secret.RedactSensitive（复用现有正则）
// - 结构体字段名命中 redactSensitiveKeys 时整体替换为 "***"
// - 嵌套结构体递归处理（最大深度 redactMaxDepth）
type RedactHandler struct {
	inner slog.Handler
}

// NewRedactHandler 创建脱敏 handler 包装器（SNAPSHOT-2）。
//
// inner 是底层 handler（如 slog.NewJSONHandler / slog.NewTextHandler）。
func NewRedactHandler(inner slog.Handler) *RedactHandler {
	return &RedactHandler{inner: inner}
}

// Enabled 委托给底层 handler（SNAPSHOT-3）。
func (h *RedactHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle 脱敏后委托给底层 handler（SNAPSHOT-4）。
//
// 行为：
// - 遍历 Record 的属性
// - 对每个属性值递归脱敏
// - 构造新 Record 委托给 inner handler
func (h *RedactHandler) Handle(ctx context.Context, r slog.Record) error {
	newRecord := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(attr slog.Attr) bool {
		newRecord.AddAttrs(redactAttr(attr, 0))
		return true
	})
	return h.inner.Handle(ctx, newRecord)
}

// WithAttrs 委托给底层 handler（SNAPSHOT-5）。
func (h *RedactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		redacted[i] = redactAttr(attr, 0)
	}
	return &RedactHandler{inner: h.inner.WithAttrs(redacted)}
}

// WithGroup 委托给底层 handler（SNAPSHOT-6）。
func (h *RedactHandler) WithGroup(name string) slog.Handler {
	return &RedactHandler{inner: h.inner.WithGroup(name)}
}

// redactAttr 递归脱敏 slog.Attr（SNAPSHOT-7；F1 补分支 2026-09-15）。
//
// 行为：
// - 字段名命中 sensitiveKeys → 整体替换为 "***"
// - 其余值交给 redactValue 按 Kind 分派（F1：补 error / Group / 命名 string / fmt.Stringer）
//
// 递归深度上限 redactMaxDepth（防循环引用 / 病态嵌套）。
func redactAttr(attr slog.Attr, depth int) slog.Attr {
	if depth > redactMaxDepth {
		return attr // 超过最大深度，保留原值
	}
	if redactSensitiveKeys[strings.ToLower(attr.Key)] {
		return slog.Attr{Key: attr.Key, Value: slog.StringValue("***")}
	}
	return slog.Attr{Key: attr.Key, Value: redactValue(attr.Value, depth)}
}

// redactValue 按 slog.Value 的 **Kind** 脱敏并返回同 Kind 的 Value。
//
// 这是 `redactAttr`（属性级）与 `redactArgs`（参数级）的**单一实现** ——
// 两层若各写一份分支，就会出现"一个补了 error/Group、另一个没补"的分裂，
// 那正是 G1/G2 的成因。
//
// ★ 为什么必须按 Kind 分派（F1 的结构性要点）：**Kind 决定"这个值最终被渲染成什么形状"**，
// 脱敏必须按同一形状做，否则会有两类错误：
//
//	① 误伤与本层无关的值 —— 例如 `slog.Time("t", now)` 的 Kind()==KindTime，
//
// 而 `time.Time` 恰好实现了 `fmt.Stringer`；若先按 Go 类型匹配，会把它当成
// Stringer 脱敏成一行字符串，改掉所有时间字段的日志形状。
//
//	② 把结构化 payload 压成一行字符串 —— struct 走 json.Marshal（实测：slog 的
//
// JSON/Text 编码器**不用** struct 的 String() 方法），按 Stringer 处理既丢信息、
// 又与真实输出不符。
//
// 实测（Go 1.25 log/slog，见本包 F1 探针测试）：
//
//	slog.Error(msg,"error",err) → {"error":"<Error() 文本>"}
//	slog.Any(k,slog.GroupValue(a,b)) → {"k":{"a":…,"b":…}} （AnyValue(Value) 保留 Kind=Group）
//	slog.Any(k,[]slog.Attr{a,b}) → {"k":{"a":…,"b":…}} （AnyValue 归一化为 Group）
//	slog.Any(k,struct{X string}) → {"k":{"X":…}} （走 json.Marshal，不走 String()）
//	slog.Any(k,time.Now()) → {"k":"<RFC3339>"} （AnyValue(time.Time) → KindTime）
func redactValue(val slog.Value, depth int) slog.Value {
	// Resolve 处理 slog.LogValuer：原实现就是先 Resolve 再取值，此处保持同语义
	// （否则 KindLogValuer 会落到 default 分支而**整条不脱敏**）。
	val = val.Resolve()
	switch val.Kind() {
	case slog.KindString:
		return slog.StringValue(secret.RedactSensitive(val.String()))
	case slog.KindGroup:
		group := val.Group()
		redacted := make([]slog.Attr, 0, len(group))
		for _, sub := range group {
			redacted = append(redacted, redactAttr(sub, depth+1))
		}
		return slog.GroupValue(redacted...)
	case slog.KindAny:
		return slog.AnyValue(redactAny(val.Any(), depth))
	default:
		// KindTime / KindDuration / KindInt / KindFloat / KindUint / KindBool：
		// 非文本，不可能承载密钥；保持原值（也避免把"恰好实现 fmt.Stringer"的
		// 结构化值误伤成字符串）。
		return val
	}
}

// redactAny 对一个**即将被 slog 编码的任意值**做脱敏，返回可安全编码的值（F1）。
//
// 判定顺序刻意与"slog 实际会怎么渲染它"一致（实测见 redactValue 注释）：
//
// 1. `error` → 编码器渲染 Error() 文本 ⇒ 按文本脱敏（替换为脱敏后的字符串，形状一致）
// 2. string / 命名 string 类型 → 按文本脱敏（`type APIKey string` 匹配不到 `case string`）
// 3. struct / map → 递归字段（JSON 对象形状）
// 4. []slog.Attr → 逐成员递归（JSON 对象形状；AnyValue 已归一化为 Group，此处为兜底）
// 5. fmt.Stringer → 仅剩余**非结构化**类型才按 String() 脱敏
// 6. 其余 → 原值
func redactAny(v any, depth int) any {
	if v == nil {
		return nil
	}
	// 1) error：**必须排在 struct/map 之前** —— 否则 `*errors.errorString` 这类
	// "指针 → 非 struct/map" 的值会原样落盘（G1 的成因）。
	if err, ok := v.(error); ok {
		return secret.RedactSensitive(err.Error())
	}
	// 2) 文本类（含命名 string 类型）。
	if s, ok := v.(string); ok {
		return secret.RedactSensitive(s)
	}
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return v
	}
	if rv.Kind() == reflect.String {
		return secret.RedactSensitive(rv.String())
	}
	// 3) 结构化：递归字段（保留对象形状）。
	if rv.Kind() == reflect.Struct || rv.Kind() == reflect.Map {
		return redactReflectValue(rv, depth).Interface()
	}
	// 4) []slog.Attr 兜底（正常路径已被 AnyValue 归一化为 KindGroup）。
	if attrs, ok := v.([]slog.Attr); ok {
		redacted := make([]slog.Attr, 0, len(attrs))
		for _, sub := range attrs {
			redacted = append(redacted, redactAttr(sub, depth+1))
		}
		return redacted
	}
	// 5) fmt.Stringer：只对**非结构化**类型生效（见 redactValue 的"为什么按 Kind 分派"）。
	if str, ok := v.(fmt.Stringer); ok {
		return secret.RedactSensitive(str.String())
	}
	return v
}

// redactReflectValue 递归脱敏 reflect.Value（SNAPSHOT-8）。
//
// 处理 Struct 和 Map，字段名命中 sensitiveKeys 时替换为 "***"。
//
// F1 伴随修正（2026-09-15，两处同函数内缺陷，缺一则 F1 的新分支会引入新故障）：
//
//	(a) 未导出字段：原实现对 `field.Type.Kind()==String` 的字段**无 CanSet 检查**就
//
// `SetString`，而 `reflect.New(t).Elem().Field(i)` 的未导出字段 CanSet()==false
// ⇒ **打一行日志即 panic**。F1 把 error/命名类型/Group 等更多值路由进本函数，
// 不修就等于扩大"打日志崩溃"的面。
// 跳过未导出字段**不是**放松脱敏：`encoding/json` 不序列化未导出字段 ⇒ 它们
// 根本不会出现在日志输出里，既无泄露面、也无信息损失。
//
//	(b) 非字符串字段被清零：原判断写成 `rv.Field(i).CanSet()`（**源** value 不可寻址
//
// ⇒ 恒 false），于是整型/布尔等字段被静默丢弃成零值 —— "脱敏"变成了"篡改数据"。
// 修正为对**目标**字段判断 CanSet，忠实复制非敏感字段。
func redactReflectValue(rv reflect.Value, depth int) reflect.Value {
	if depth > redactMaxDepth || !rv.IsValid() {
		return rv
	}
	switch rv.Kind() {
	case reflect.Struct:
		// 构造新结构体（复制后修改字段）
		result := reflect.New(rv.Type()).Elem()
		for i := 0; i < rv.NumField(); i++ {
			field := rv.Type().Field(i)
			dst := result.Field(i)
			if !dst.CanSet() {
				// 未导出字段：不进 JSON 输出 ⇒ 无泄露面；跳过同时消除 SetString panic。
				continue
			}
			fieldName := strings.ToLower(field.Name)
			if redactSensitiveKeys[fieldName] {
				// 敏感字段替换为 "***"
				if field.Type.Kind() == reflect.String {
					dst.SetString("***")
				} else {
					dst.Set(reflect.ValueOf("***"))
				}
				continue
			}
			// 非敏感字段：递归处理，或按原值忠实复制。
			switch {
			case field.Type.Kind() == reflect.Struct || field.Type.Kind() == reflect.Map:
				dst.Set(redactReflectValue(rv.Field(i), depth+1))
			case field.Type.Kind() == reflect.String:
				dst.SetString(secret.RedactSensitive(rv.Field(i).String()))
			default:
				dst.Set(rv.Field(i))
			}
		}
		return result
	case reflect.Map:
		// 处理 map[string]interface{}
		if rv.Type().Key().Kind() == reflect.String {
			newMap := reflect.MakeMap(rv.Type())
			for _, key := range rv.MapKeys() {
				keyStr := strings.ToLower(key.String())
				val := rv.MapIndex(key)
				if redactSensitiveKeys[keyStr] {
					// 敏感 key 替换值为 "***"
					newMap.SetMapIndex(key, reflect.ValueOf("***"))
				} else {
					// 递归处理值
					if val.Kind() == reflect.Struct || val.Kind() == reflect.Map {
						newMap.SetMapIndex(key, redactReflectValue(val, depth+1))
					} else if val.Kind() == reflect.String {
						newMap.SetMapIndex(key, reflect.ValueOf(secret.RedactSensitive(val.String())))
					} else {
						newMap.SetMapIndex(key, val)
					}
				}
			}
			return newMap
		}
		return rv
	default:
		return rv
	}
}

// FormatRedactedStruct 格式化结构体并脱敏（导出供非 slog 场景使用， SNAPSHOT-9）。
//
// 用于 fmt.Sprintf("%+v", struct) 场景的脱敏。
// 返回脱敏后的字符串表示。
func FormatRedactedStruct(v interface{}) string {
	if v == nil {
		return "<nil>"
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return "<nil>"
		}
		rv = rv.Elem()
	}
	redacted := redactReflectValue(rv, 0)
	return fmt.Sprintf("%+v", redacted.Interface())
}

// IsSensitiveKey 判断字段名是否敏感（导出供外部调用， SNAPSHOT-10）。
func IsSensitiveKey(key string) bool {
	return redactSensitiveKeys[strings.ToLower(key)]
}

// _ 确保 time 包被使用（避免 unused import，若未来扩展用 time.Now()）。
var _ = time.Now
