package bridge

import (
	"fmt"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// BridgeError 是 bridge 模块的结构化错误类型（P0-5，对标 N6 §8.1）。
// 支持 errors.As 提取，并实现 ErrorID() 方法以兼容 N6 GetErrorID 接口提取（v2 R2）。
// 错误码保留 JSON-RPC 风格（-32001~-32005）向后兼容，ID 字段对接 N6 ErrorID 命名空间。
type BridgeError struct {
	Code    int        // 原 JSON-RPC 风格错误码（-32001~-32005，保留向后兼容）
	ID      ai.ErrorID // N6 ErrorID（ErrBridgeJWT/ErrBridgeReconnect）
	Message string     // 错误消息（已脱敏，委托 H3 RedactSensitive）
}

// Error 实现 error 接口，返回包含错误码与 ErrorID 的格式化消息。
func (e *BridgeError) Error() string {
	return fmt.Sprintf("bridge error %d [%s]: %s", e.Code, e.ID, e.Message)
}

// ErrorID 返回 N6 ErrorID（v2 R2 新增，兼容 N6 GetErrorID 接口提取）。
// 使拥有 *BridgeError 引用的调用方可直接获取 ErrorID；
// formatError 额外用 ai.WithID 双层包装，确保 ai.GetErrorID 也能经由 *ai.Error 提取。
func (e *BridgeError) ErrorID() ai.ErrorID {
	return e.ID
}

// NewBridgeError 创建 BridgeError，自动映射错误码到 N6 ErrorID。
// code 经 MapErrorCodeToErrorID 映射到 ErrBridgeJWT 或 ErrBridgeReconnect（02 §2.3.2 契约）。
func NewBridgeError(code int, message string) *BridgeError {
	return &BridgeError{
		Code:    code,
		ID:      MapErrorCodeToErrorID(code),
		Message: message,
	}
}
