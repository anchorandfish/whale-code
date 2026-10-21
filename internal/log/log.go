// Package log — structured logging with sensitive data redaction
package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/anchorandfish/whale-code/internal/secret"
)

var (
	level   = slog.LevelInfo
	levelMu sync.Mutex

	// fileSink 持有文件 sink 的可关闭 writer（*lumberjack.Logger 实现 io.Closer）。
	// 为 nil 时表示未启用文件落盘，日志仅输出到 stderr。
	fileSink   io.Closer
	fileSinkMu sync.Mutex

	// debugToggle M22-04 P0 引入：SIGUSR1 触发的 Debug/Info 切换状态（0=Info，1=Debug）。
	// 使用 atomic.Int32 避免在 SIGUSR1 hook 中加锁，与 levelMu 协作但自身无锁。
	// ToggleDebug 在两个状态之间 ping-pong。
	debugToggle atomic.Int32

	// debugMode D4 修复：--debug 标志触发的 stderr 双写开关。
	// 默认 false（生产模式）：slog 只写文件，stderr 重定向到日志文件（magic_docs 等标准库 log 不泄漏到 TUI）。
	// true（调试模式）：slog 双写 stderr + 文件，便于开发期实时查看日志。
	// 使用 atomic.Bool 避免在 SetDebugMode 中加锁，与 currentWriter 协作但自身无锁。
	debugMode atomic.Bool
)

// SetDebugMode 设置调试模式开关（D4 修复，2026-07-31）。
// true：currentWriter 返回 MultiWriter(stderr, lumberjack) 双写，便于开发期实时查看日志；
// false：currentWriter 只返回 lumberjack，slog 不写 stderr，避免日志泄漏到 TUI 下方。
// 必须在 InitFileLogger 之前调用，否则首次 slog handler 构建仍按 false（只写文件）。
// 线程安全：atomic 操作无锁，与 levelMu/fileSinkMu 协作但自身无锁。
func SetDebugMode(enabled bool) {
	debugMode.Store(enabled)
}

// init 初始化默认 logger。
//
// P0-14 修复（v4 模块审查）：通过 NewChainedHandler 包装基础 TextHandler，
// 启用 RedactHandler 脱敏层（redact=true），SamplerConfig 留空（Rate=0 表示不采样）。
// 原实现直接用 NewTextHandler，绕过了已实现的脱敏 handler，导致敏感数据明文泄漏。
func init() {
	baseHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})
	chainedHandler := NewChainedHandler(baseHandler, SamplerConfig{}, true)
	slog.SetDefault(slog.New(chainedHandler))
}

// SetLevel sets the global log level
//
// L-1 修复（全模块审查）：消除双锁死锁风险。
// 原实现锁顺序：SetLevel(levelMu → currentWriter → fileSinkMu)，
// InitFileLogger 锁顺序：InitFileLogger(fileSinkMu → levelMu)，
// 两者相反存在 AB-BA 死锁风险。
// 修复：SetLevel 不再在持有 levelMu 时调用 currentWriter（会获取 fileSinkMu）。
// 改为先在 levelMu 内更新 level + 快照 fileSink 引用，释放 levelMu 后再获取 fileSinkMu
// 并重建 slog handler。InitFileLogger 的锁顺序保持 fileSinkMu → levelMu 不变（仅一个方向）。
//
// D4 修复：writer 改为调用无锁的 writerForSink，根据 debugMode 决定双写或只写文件。
func SetLevel(l slog.Level) {
	// L-1 修复：先在 levelMu 内更新 level，快照 fileSink 引用后释放 levelMu
	levelMu.Lock()
	level = l
	var sink io.Closer
	sink = fileSink // 快照引用（原子读，不加 fileSinkMu）
	levelMu.Unlock()
	// 释放 levelMu 后重建 slog handler（writerForSink 无锁，不会死锁）
	writer := writerForSink(sink)
	// P0-14 修复（v4 模块审查）：通过 NewChainedHandler 包装基础 TextHandler，
	// 启用 RedactHandler 脱敏层（redact=true），SamplerConfig 留空（Rate=0 表示不采样）。
	// 原实现直接用 NewTextHandler，绕过了已实现的脱敏 handler，导致敏感数据明文泄漏。
	baseHandler := slog.NewTextHandler(writer, &slog.HandlerOptions{
		Level: l,
	})
	chainedHandler := NewChainedHandler(baseHandler, SamplerConfig{}, true)
	slog.SetDefault(slog.New(chainedHandler))
}

// ToggleDebug 在 Debug 与 Info 之间切换全局日志级别（M22-04 P0）。
//
// SIGUSR1 信号触发本函数。内部用 atomic.Int32 维护切换状态：
// - 0（Info）→ 切换到 Debug，状态置 1
// - 1（Debug）→ 切换到 Info，状态置 0
//
// 线程安全：atomic 操作无锁，SetLevel 内部 levelMu 保证 level 字段写入有序。
// 调用此函数后立即对后续 log.Info/Debug 生效。
func ToggleDebug() {
	if debugToggle.Load() == 0 {
		debugToggle.Store(1)
		SetLevel(slog.LevelDebug)
	} else {
		debugToggle.Store(0)
		SetLevel(slog.LevelInfo)
	}
}

// CurrentLevel 返回当前日志级别（M22-04 P0 引入，供测试与运维查询）。
// 仅用于读侧观测，写入仍需通过 SetLevel/ToggleDebug。
func CurrentLevel() slog.Level {
	levelMu.Lock()
	defer levelMu.Unlock()
	return level
}

// SetJSONFormat switches to JSON structured logging format for better
// machine-readability and log aggregation system integration.
//
// L-1 修复：与 SetLevel 同样的锁顺序修复——不在持有 levelMu 时调用 currentWriter。
//
// D4 修复：writer 改为调用无锁的 writerForSink，根据 debugMode 决定双写或只写文件。
func SetJSONFormat() {
	// L-1 修复：先在 levelMu 内快照 level + fileSink，释放后再重建 handler
	levelMu.Lock()
	l := level
	var sink io.Closer
	sink = fileSink
	levelMu.Unlock()
	// 释放 levelMu 后重建 slog handler（writerForSink 无锁，不会死锁）
	writer := writerForSink(sink)
	// P0-14 修复（v4 模块审查）：通过 NewChainedHandler 包装基础 JSONHandler，
	// 启用 RedactHandler 脱敏层（redact=true），SamplerConfig 留空（Rate=0 表示不采样）。
	// 原实现直接用 NewJSONHandler，绕过了已实现的脱敏 handler，导致敏感数据明文泄漏。
	baseHandler := slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level: l,
	})
	chainedHandler := NewChainedHandler(baseHandler, SamplerConfig{}, true)
	slog.SetDefault(slog.New(chainedHandler))
}

// writerForSink 根据 debugMode 和 sink 返回合适的 writer（D4 修复，2026-07-31）。
//
// 无锁辅助函数，调用方需自行保证 sink 引用的线程安全（已在 fileSinkMu 或 levelMu 快照后调用）。
// 避免 SetLevel/SetJSONFormat/InitFileLogger 在持有 levelMu 或 fileSinkMu 时调用 currentWriter 导致死锁。
//
// 行为矩阵：
// - sink 为 nil（未启用文件日志）：返回 os.Stderr（保持原行为，启动早期未 InitFileLogger 时）
// - sink 非 nil 且 debugMode=true：返回 MultiWriter(stderr, lumberjack) 双写（调试模式）
// - sink 非 nil 且 debugMode=false：返回 lumberjack 只写文件（生产模式，避免日志泄漏到 TUI）
func writerForSink(sink io.Closer) io.Writer {
	if sink == nil {
		return os.Stderr
	}
	if lj, ok := sink.(*lumberjack.Logger); ok {
		if debugMode.Load() {
			return io.MultiWriter(os.Stderr, lj)
		}
		return lj
	}
	return os.Stderr
}

// currentWriter 返回当前应使用的 writer（加锁版本，供未持有 fileSinkMu 的调用方使用）。
// 文件 sink 启用且 debugMode=true 时返回 MultiWriter(stderr, lumberjack) 双写；
// 文件 sink 启用且 debugMode=false 时返回 lumberjack 只写文件；
// 文件 sink 未启用时返回 os.Stderr。
// SetLevel/SetJSONFormat/InitFileLogger 不调用本函数（已持有 levelMu 或 fileSinkMu，会死锁），
// 改为调用无锁的 writerForSink。
func currentWriter() io.Writer {
	fileSinkMu.Lock()
	defer fileSinkMu.Unlock()
	return writerForSink(fileSink)
}

// InitFileLogger 初始化文件日志 sink，在 stderr 之外同时写入指定文件。
// path: 日志文件路径（目录自动创建）；maxSizeMB: 单文件最大 MB；maxBackups: 保留旧文件数；maxAgeDays: 旧文件保留天数。
// 调用后所有 log.Info/Warn/Error 会同时输出到 stderr 和文件，并切到 JSONHandler（机器可读，便于日志聚合）。
// 重复调用会先关闭旧 sink 再建新 sink，避免句柄泄漏。
func InitFileLogger(path string, maxSizeMB, maxBackups, maxAgeDays int) error {
	fileSinkMu.Lock()
	defer fileSinkMu.Unlock()

	// 关闭旧 sink
	if fileSink != nil {
		_ = fileSink.Close()
		fileSink = nil
	}

	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("log: 创建日志目录失败: %w", err)
	}

	lj := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    maxSizeMB,
		MaxBackups: maxBackups,
		MaxAge:     maxAgeDays,
		Compress:   true, // 旧日志 gzip 压缩
		LocalTime:  true,
	}
	fileSink = lj

	// D4 修复：根据 debugMode 决定双写或只写文件。
	// 非 debug 模式只写文件（避免日志泄漏到 TUI 下方），debug 模式双写 stderr + 文件。
	// 直接调用 writerForSink(lj) 而非 currentWriter()，因当前已持有 fileSinkMu，currentWriter 会死锁。
	mw := writerForSink(lj)
	levelMu.Lock()
	defer levelMu.Unlock()
	// P0-14 修复（v4 模块审查）：通过 NewChainedHandler 包装基础 JSONHandler，
	// 启用 RedactHandler 脱敏层（redact=true），SamplerConfig 留空（Rate=0 表示不采样）。
	// 原实现直接用 NewJSONHandler，绕过了已实现的脱敏 handler，导致敏感数据明文泄漏。
	baseHandler := slog.NewJSONHandler(mw, &slog.HandlerOptions{
		Level: level,
	})
	chainedHandler := NewChainedHandler(baseHandler, SamplerConfig{}, true)
	slog.SetDefault(slog.New(chainedHandler))
	return nil
}

// CloseFileLogger 关闭文件 sink，进程退出时调用。
// 可多次安全调用（nil 检查），未初始化时为空操作。
func CloseFileLogger() {
	fileSinkMu.Lock()
	defer fileSinkMu.Unlock()
	if fileSink != nil {
		_ = fileSink.Close()
		fileSink = nil
	}
}

// RedactSensitive 导出脱敏函数，供 ai 包 defaultRedactor 调用。
// H3 收口：委托 secret.RedactSensitive（守 02 §2.3.1 契约，脱敏唯一入口）。
// 保留此函数签名以兼容现有调用点（ai/errors.go defaultRedactor / N21 导出等）。
func RedactSensitive(msg string) string {
	return secret.RedactSensitive(msg)
}

// redactSensitive 是未导出别名，委托 secret.RedactSensitive。
// 保留以兼容 log 包内部调用（Info/Warn/Error/redactArgs）。
func redactSensitive(msg string) string {
	return secret.RedactSensitive(msg)
}

// Debug 输出 DEBUG 级别日志（性能计时点专用）。
//
// 仅当通过 SetLevel(slog.LevelDebug) 或 ToggleDebug 切到 Debug 级别时才会输出，
// 默认 Info 级别下被 slog handler 过滤，不影响生产吞吐。
// 与 Info/Warn/Error 一致地走 RedactHandler 脱敏层，避免敏感数据明文泄漏。
func Debug(msg string, args ...any) {
	redacted := redactSensitive(msg)
	redactedArgs := redactArgs(args...)
	slog.Debug(redacted, redactedArgs...)
}

func Info(msg string, args ...any) {
	redacted := redactSensitive(msg)
	redactedArgs := redactArgs(args...)
	slog.Info(redacted, redactedArgs...)
}

func Warn(msg string, args ...any) {
	redacted := redactSensitive(msg)
	redactedArgs := redactArgs(args...)
	slog.Warn(redacted, redactedArgs...)
}

func Error(msg string, args ...any) {
	redacted := redactSensitive(msg)
	redactedArgs := redactArgs(args...)
	slog.Error(redacted, redactedArgs...)
}

// redactArgs redacts sensitive values in log arguments.
//
// 5-A 增强：识别 slog.Attr，对其 value 递归脱敏，
// 覆盖 log.Info("msg", "token", "ghp_xxx") 与 log.Info("msg", slog.String("token","ghp_xxx")) 场景。
//
// F1 收口：参数级不再自写一份类型分支，而是**委托** redactValue
// —— 属性级与参数级共用同一份 Kind 分派逻辑。原实现只处理 `string` 与"slog.Attr 的 string 值"，
// 于是 `error`（G1）与 `slog.Group`（G2）在这一层同样漏过；两处各写一份就必然出现
// "一个补了、一个没补"的分裂。
//
// ★ 参数级**只做值级脱敏，不做键级掩码**（职责边界，不是遗漏）：
//
//	args 是扁平 kv 序列（`("token", "ghp_…")`），参数级**拿不到可靠的键**（奇数位才是键，
//	且调用方可以传 Attr 混排）；键级掩码因此统一由属性级 `RedactHandler` 负责。
//	两层职责不重叠，这也保证既有契约不变（`TestRedactArgs_SlogAttr` 的 `[REDACTED]`
//	与 `TestS14Char_LogPath_RedactsStringValueAndSensitiveKeys` 的 `token: "***"` 同时成立）。
//
// 裸值（非 Attr 参数）经 `slog.AnyValue(v)` 取得 Kind 后再按同一规则脱敏；
// **Kind 不承载文本（Int/Float/Bool/Time/Duration…）时原样保留** —— 一是它们
// 不可能承载密钥，二是 AnyValue 往返会改变 Go 类型（int→int64）而污染日志字段。
func redactArgs(args ...any) []any {
	redacted := make([]any, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case string:
			redacted[i] = redactSensitive(v)
		case slog.Attr:
			redacted[i] = slog.Attr{Key: v.Key, Value: redactValue(v.Value, 0)}
		default:
			val := slog.AnyValue(arg).Resolve()
			switch val.Kind() {
			case slog.KindString, slog.KindGroup, slog.KindAny:
				redacted[i] = redactValue(val, 0).Any()
			default:
				redacted[i] = arg
			}
		}
	}
	return redacted
}
