// Package secret — 密钥泄露/缺失错误包装（回调模式避免循环导入）
package secret

import (
	"fmt"
	"sync"
)

// ErrorWrapper 是错误包装回调类型，在启动阶段由 tui/app.go 注入 ai.WithID。
// 入参 err 已脱敏的 error，errorID 为 N6 定义的 ErrorID 字符串（如 "secret_leak"）。
type ErrorWrapper func(err error, errorID string) error

var (
	errorWrapper   ErrorWrapper
	errorWrapperMu sync.RWMutex
)

// SetErrorWrapper 注入错误包装函数（启动阶段由 tui/app.go 调用）。
// 绑定 ai.WithID 实现 N6 ErrorID 结构化（守 02 §2.3.2）。
func SetErrorWrapper(fn ErrorWrapper) {
	errorWrapperMu.Lock()
	defer errorWrapperMu.Unlock()
	errorWrapper = fn
}

// getErrorWrapper 线程安全读取错误包装函数。
func getErrorWrapper() ErrorWrapper {
	errorWrapperMu.RLock()
	defer errorWrapperMu.RUnlock()
	return errorWrapper
}

// WrapLeak 包装密钥泄露错误（对标 N6 ErrSecretLeak）。
// 调用前先对 err.Error() 调 RedactSensitive 脱敏，再通过 ErrorWrapper 绑定 ErrorID。
// 若 ErrorWrapper 未注入（开发/测试场景），降级返回带 [secret_leak] 前缀的普通 error。
func WrapLeak(err error) error {
	if err == nil {
		return nil
	}
	redacted := RedactSensitive(err.Error())
	wrapped := fmt.Errorf("%s", redacted)
	if w := getErrorWrapper(); w != nil {
		return w(wrapped, "secret_leak")
	}
	return fmt.Errorf("[secret_leak] %s", redacted)
}

// WrapMissing 包装密钥缺失错误（对标 N6 ErrSecretMissing）。
// 调用前先对 err.Error() 调 RedactSensitive 脱敏，再通过 ErrorWrapper 绑定 ErrorID。
// 若 ErrorWrapper 未注入（开发/测试场景），降级返回带 [secret_missing] 前缀的普通 error。
func WrapMissing(err error) error {
	if err == nil {
		return nil
	}
	redacted := RedactSensitive(err.Error())
	wrapped := fmt.Errorf("%s", redacted)
	if w := getErrorWrapper(); w != nil {
		return w(wrapped, "secret_missing")
	}
	return fmt.Errorf("[secret_missing] %s", redacted)
}
