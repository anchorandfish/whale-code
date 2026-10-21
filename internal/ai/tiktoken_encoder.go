// Package ai — tiktoken-go 可选集成（L3 critical model 路径）
//
// T1-B实施：tiktoken-go lazy import + 5 秒超时降级 L2。
//
// 设计要点：
// - tiktoken-go 是纯 Go 实现（无 CGO 依赖），符合 CGO_ENABLED=0 约束
// - 首次使用时下载 cl100k_base.bin / o200k_base.bin 编码文件（~1.8MB），走 HTTPS
// - 编码文件下载失败时（离线场景 / 网络受限）降级 L2 启发式估算
// - lazy import：sync.Once 保证只加载一次，5 秒超时降级（吸纳优化 1）
//
// 官方源：
// - tiktoken-go @https://github.com/pkoukk/tiktoken-go
// - 编码映射表 @https://github.com/pkoukk/tiktoken-go#available-encodings
package ai

import (
	"context"
	"fmt"
	"sync"
	"time"

	tiktoken "github.com/pkoukk/tiktoken-go"
)

// tiktokenLoadTimeout tiktoken-go 编码文件下载超时阈值（吸纳优化 1：5 秒超时降级 L2）。
//
// 编码文件 ~1.8MB，正常网络下 1-3 秒下载完成；超过 5 秒视为网络受限，
// 降级 L2 启发式估算（不阻塞主流程）。
const tiktokenLoadTimeout = 5 * time.Second

// tiktokenGoEncoderWrapper 包装 tiktoken-go 的 *tiktoken.Tiktoken 实现 TiktokenEncoder 接口。
//
// 适配层：tiktoken-go 实际 API 是 Encode(text, allowedSpecial, disallowedSpecial []string) []int，
// 本 wrapper 在内部固定 disallowedSpecial=nil（disallow 所有 special tokens），
// 对外暴露 Encode(text, allowedSpecial []string) []int（吸纳优化 6 对齐签名）。
type tiktokenGoEncoderWrapper struct {
	encoder *tiktoken.Tiktoken
}

// Encode 编码文本为 token id 列表，len 即 token 数。
//
// 实现细节：调用 tiktoken-go 的 Encode(text, allowedSpecial, nil)，
// disallowedSpecial=nil 表示不显式 disallow 任何 special token（与 nil allowedSpecial 配对）。
func (w *tiktokenGoEncoderWrapper) Encode(text string, allowedSpecial []string) []int {
	if w.encoder == nil {
		return nil
	}
	return w.encoder.Encode(text, allowedSpecial, nil)
}

// NewTiktokenLoader 构造 tiktoken-go lazy 加载器（吸纳优化 1：5 秒超时降级 L2）。
//
// 行为：
// - sync.Once 保证同进程只加载一次（避免重复下载编码文件）
// - 5 秒超时：context.WithTimeout 控制 tiktoken.EncodingForModel 调用时长
// - 加载失败（超时 / 网络错误 / 编码不支持）返回 error，调用方降级 L2
//
// 返回的 loader 函数线程安全，可被多 goroutine 并发调用。
//
// 用法：
//
//	loader := NewTiktokenLoader()
//	ts := NewTokenSource(estimator, tracker, true, 30*time.Minute)
//	ts.(*tokenSourceImpl).SetTiktokenLoader(loader)
func NewTiktokenLoader() func() (TiktokenEncoder, error) {
	var (
		encoder TiktokenEncoder
		err     error
		once    sync.Once
	)
	return func() (TiktokenEncoder, error) {
		once.Do(func() {
			// 5 秒超时（吸纳优化 1）
			ctx, cancel := context.WithTimeout(context.Background(), tiktokenLoadTimeout)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				// 实际调用 tiktoken-go API（默认 cl100k_base 编码，覆盖 gpt-4/gpt-3.5-turbo）
				// o200k_base（gpt-4o/gpt-4.1/gpt-4.5）也由 tiktoken-go 内部支持
				tke, loadErr := tiktoken.EncodingForModel("gpt-4")
				if loadErr != nil {
					err = fmt.Errorf("tiktoken load failed: %w", loadErr)
					return
				}
				encoder = &tiktokenGoEncoderWrapper{encoder: tke}
			}()
			select {
			case <-done:
				// 加载完成（成功或失败）
			case <-ctx.Done():
				err = fmt.Errorf("tiktoken load timeout (5s)")
			}
		})
		return encoder, err
	}
}

// NewTiktokenLoaderForModel 构造指定 model 的 tiktoken-go lazy 加载器。
//
// 与 NewTiktokenLoader 区别：显式传入 model 名（如 "gpt-4o" 用 o200k_base 编码）。
// 用于测试或需要特定编码的场景。
//
// model 参数透传给 tiktoken.EncodingForModel，由 tiktoken-go 内部映射到对应编码。
func NewTiktokenLoaderForModel(model string) func() (TiktokenEncoder, error) {
	var (
		encoder TiktokenEncoder
		err     error
		once    sync.Once
	)
	return func() (TiktokenEncoder, error) {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), tiktokenLoadTimeout)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				tke, loadErr := tiktoken.EncodingForModel(model)
				if loadErr != nil {
					err = fmt.Errorf("tiktoken load failed for model %s: %w", model, loadErr)
					return
				}
				encoder = &tiktokenGoEncoderWrapper{encoder: tke}
			}()
			select {
			case <-done:
			case <-ctx.Done():
				err = fmt.Errorf("tiktoken load timeout (5s) for model %s", model)
			}
		})
		return encoder, err
	}
}
