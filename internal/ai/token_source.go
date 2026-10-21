// Package ai — TokenSource 统一 token 数据来源
//
// T1-B实施：三层 token 来源架构
//
//	L1 远端 usage 优先：Provider API 响应 usage 字段（100% 精确）
//	L2 通用估算预检：启发式估算 + CJK 感知 + EMA offset 校准（误差 ≤5% 校准后）
//	L3 可选 tiktoken-go：critical model（gpt-4/gpt-4o 等）BPE 分词（误差 <5%）
//
// 跨模块共享：M04 维护 TokenSource 接口，M05/H10 通过依赖注入调用。
//
// 官方对标：
// - tiktoken-go @https://github.com/pkoukk/tiktoken-go
// - Anthropic Usage @https://docs.anthropic.com/en/api/messages
// - OpenAI Usage @https://platform.openai.com/docs/api-reference/chat
// - Gemini Usage @https://ai.google.dev/api/rest/v1beta/models/generateContent
// - DeepSeek Usage @https://api-docs.deepseek.com/api-reference-1
// - DashScope Usage @https://help.aliyun.com/zh/dashscope/
package ai

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/log"
)

// TokenSource 统一 token 数据来源接口，三模块（M04/M05/H10）共享。
//
// §3 冲突 2 草案扩展：
// - PreCheckEstimate: 请求前估算（M04 预检 + H10 派发前预估）
// - PostCallActual: 请求后真实 usage（M05 校准 / H10 cost 计费）
// - ContextCount: 上下文管理用计数（M05 调用，复用估算逻辑 + offset 校准）
// - RecordRemoteUsage: 远端 usage 回流（L1 → L2 校准反馈）
type TokenSource interface {
	// PreCheckEstimate 请求前估算（用于 M04 预检 + H10 派发前预估）。
	// L3 优先：若 model 在 critical 白名单且 tiktoken 可用 → tiktoken-go BPE。
	// L2 降级：启发式估算 + CJK 感知 + EMA offset 校准。
	PreCheckEstimate(messages []MessageEntry, model string) (inputTokens int)

	// PostCallActual 请求后真实 usage（来自远端 API 响应）。
	// L1 优先：远端 usage 已记录到 UsageTracker 时直接读最近一次记录。
	// L2 降级：远端 usage 缺失或 model 从未调用过时返回零值 + ok=false（吸纳优化 3）。
	PostCallActual(model string) (usage UnifiedUsage, ok bool)

	// ContextCount 上下文管理用计数（M05 调用）。
	// 复用 PreCheckEstimate 估算逻辑，但应用 EMA offset 校准。
	ContextCount(messages []MessageEntry, model string) (totalTokens int)

	// RecordRemoteUsage 远端 usage 回流（L1 → L2 校准反馈）。
	// 在 stream.go parseSSEEvent 解析到 UsageEvent 时同步调用（在 events chan 发送前）。
	// estimated 参数从 Request.EstimatedInputTokens 读取（吸纳优化 2）。
	RecordRemoteUsage(model string, actual UnifiedUsage, estimated int)

	// ApplyCalibration 将 EMA 校准偏移应用到任意估算值。
	// 用于 UI 上下文估算（system+tools+messages 完整口径）：估算偏差会随
	// RecordRemoteUsage 回流持续收敛，使状态栏/预警/自动压缩判定基于
	// 校准后的值。无采样数据或校准异常时返回原值。
	ApplyCalibration(model string, estimated int) int

	// EstimateRequestInput 完整口径原始估算（system+tools+messages，不应用校准）。
	// B-P4 口径修复：RecordRemoteUsage 的 estimated 必须与 actual 同口径
	// （完整请求输入）。此前只估 messages（≈10 token），actual 含 system+tools
	// （≈6200）→ ratio≈622 超脏数据上界 → 校准永不生效。
	// 供 SimpleClient.Stream / streamViaAdapter 填充 EstimatedInputTokens。
	EstimateRequestInput(req Request) int

	// CalibrationInfo 读取指定 model 的校准状态（可观测性）。
	// ok=false 表示无采样数据。返回结构体拷贝，并发安全。
	CalibrationInfo(model string) (CalibrationOffset, bool)
}

// UnifiedUsage 统一 usage 字段（5 Provider 字段映射后的归一化结构）。
//
// REF-外部-Anthropic/OpenAI/Gemini/DeepSeek/DashScope 5 Provider usage 字段统一：
// - Anthropic: input_tokens / output_tokens / cache_creation_input_tokens / cache_read_input_tokens
// - OpenAI: prompt_tokens / completion_tokens / prompt_tokens_details.cached_tokens（无 cache_creation）
// - Gemini: usageMetadata.promptTokenCount / candidatesTokenCount / cachedContentTokenCount（无 cache_creation）
// - DeepSeek: prompt_tokens / completion_tokens / prompt_cache_hit_tokens（无 cache_creation）
// - DashScope: input_tokens / output_tokens（无 cache_creation / cache_read）
type UnifiedUsage struct {
	InputTokens   int `json:"input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	CacheCreation int `json:"cache_creation"` // Anthropic 独有；OpenAI/Gemini/DeepSeek/DashScope 无（=0）
	CacheRead     int `json:"cache_read"`     // OpenAI: cached_tokens；DeepSeek: prompt_cache_hit_tokens；Gemini: cachedContentTokenCount
}

// CalibrationOffset 校准偏移（缓存远端 usage 与估算值的 EMA 混合校准参数）。
//
// B-P3 混合校准模型：calibrated = estimated × ratio + offset
// - Ratio（乘法系数）：吸纳比例误差——中文/代码/JSON 的 token 密度与启发式
// 假设的偏差随内容量线性放大，纯加性 offset 无法覆盖（大注入场景失准）。
// 1.0 表示无比例误差；>1 估算偏低，<1 估算偏高。
// - Offset（加法常数）：吸纳残差——消息格式 overhead、role 包装等固定误差。
// ratio 先吃掉比例部分，剩余差额进 offset，两者 EMA 平滑（α=0.3）。
//
// 拟合策略（每轮样本）：
// 1. newRatio = actual / estimated（瞬时比例）
// 2. ratio = EMA(ratio, newRatio)
// 3. residual = actual - estimated×ratio（比例吸纳后的残差）
// 4. offset = EMA(offset, residual)
//
// 纯比例误差时 residual→0（场景：actual=1.5×estimated）；纯恒差时 ratio→1、
// offset→恒差（场景：actual=estimated+585，用户实测）。
type CalibrationOffset struct {
	ModelName   string    `json:"model_name"`
	Ratio       float64   `json:"ratio"`        // EMA(actual/estimated)，乘法系数（1.0=无比例误差）
	Offset      float64   `json:"offset"`       // EMA 残差（actual - estimated×ratio），固定误差部分
	SampleCount int       `json:"sample_count"` // 累计采样次数
	UpdatedAt   time.Time `json:"updated_at"`   // 最后更新时间
}

// criticalModels critical model 白名单（仅这些模型走 tiktoken-go 路径）。
//
// 实施期补充（优化 5）：gpt-4.1/gpt-4.5/gpt-5 用 o200k_base 编码。
// model 名查询时统一转小写（isCriticalModel 函数处理）。
//
// 官方编码参考：
// - cl100k_base: gpt-4 / gpt-4-turbo / gpt-3.5-turbo
// - o200k_base: gpt-4o / gpt-4o-mini / gpt-4.1 / gpt-4.5 / gpt-5
var criticalModels = map[string]string{
	"gpt-4":         "cl100k_base",
	"gpt-4-turbo":   "cl100k_base",
	"gpt-4o":        "o200k_base",
	"gpt-4o-mini":   "o200k_base",
	"gpt-3.5-turbo": "cl100k_base",
	"gpt-4.1":       "o200k_base",
	"gpt-4.5":       "o200k_base",
	"gpt-5":         "o200k_base",
}

// tokenSourceImpl TokenSource 接口实现。
//
// /ai/token.go:33 TokenEstimator 扩展：
// - 复用现有 TokenEstimator 的 CJK 感知估算
// - 复用现有 UsageTracker 的远端 usage 读取
// - 新增 offsets map 按 model 缓存 EMA 校准偏移
// - tiktokenLoader lazy 加载 tiktoken-go 编码器（吸纳优化 1：5 秒超时降级 L2）
type tokenSourceImpl struct {
	estimator       *TokenEstimator                 // 复用现有 CJK 感知估算
	usageTracker    *UsageTracker                   // 复用现有 UsageTracker 读远端 usage
	offsets         map[string]*CalibrationOffset   // 按 model 缓存校准偏移
	offsetsMu       sync.RWMutex                    // 并发安全（H10/M05 多 goroutine 调用）
	tiktokenEnabled bool                            // 配置开关（lazy import）
	tiktokenLoader  func() (TiktokenEncoder, error) // tiktoken-go lazy 加载器
	offsetTTL       time.Duration                   // 校准偏移缓存时长（默认 30 分钟）
	emaAlpha        float64                         // EMA 平滑系数（默认 0.3）
}

// TiktokenEncoder tiktoken-go 编码器抽象（便于 mock 测试）。
//
// 吸纳优化 6：补全 allowedSpecial []string 参数（对齐 tiktoken-go 实际 API）。
// 默认传 []string{} 即 disallow 所有 special tokens。
type TiktokenEncoder interface {
	// Encode 编码文本为 token id 列表，len 即 token 数。
	Encode(text string, allowedSpecial []string) []int
}

// NewTokenSource 构造 TokenSource 实例。
//
// 参数：
// - estimator: 现有 TokenEstimator 实例（复用 CJK 感知估算）
// - usageTracker: 现有 UsageTracker 实例（复用远端 usage 读取）
// - tiktokenEnabled: 是否启用 tiktoken-go（false 时 L3 路径降级 L2）
// - offsetTTL: offset 缓存时长（建议 30 分钟）
//
// 返回 TokenSource 接口实例。
//
// 注：tiktokenEnabled=true 时需额外调用 SetTiktokenLoader 注入加载器，
// 或使用 NewTokenSourceWithTiktoken 一步到位（推荐）。
func NewTokenSource(estimator *TokenEstimator, usageTracker *UsageTracker, tiktokenEnabled bool, offsetTTL time.Duration) TokenSource {
	return &tokenSourceImpl{
		estimator:       estimator,
		usageTracker:    usageTracker,
		offsets:         make(map[string]*CalibrationOffset),
		tiktokenEnabled: tiktokenEnabled,
		offsetTTL:       offsetTTL,
		emaAlpha:        0.3, // 吸纳优化 1：EMA α=0.3
	}
}

// NewTokenSourceWithTiktoken 构造启用 L3 tiktoken-go 路径的 TokenSource 实例。
//
// 与 NewTokenSource 区别：内部已注入 NewTiktokenLoader()（5 秒超时降级 L2），
// 调用方无需再手动 SetTiktokenLoader。
//
// 用于 main.go/runner_core.go 接线：cfg.Token.TiktokenEnabled=true 时使用此构造函数。
//
// 参数：
// - estimator: 现有 TokenEstimator 实例
// - usageTracker: 现有 UsageTracker 实例
// - offsetTTL: offset 缓存时长（建议 30 分钟）
func NewTokenSourceWithTiktoken(estimator *TokenEstimator, usageTracker *UsageTracker, offsetTTL time.Duration) TokenSource {
	ts := &tokenSourceImpl{
		estimator:       estimator,
		usageTracker:    usageTracker,
		offsets:         make(map[string]*CalibrationOffset),
		tiktokenEnabled: true,
		offsetTTL:       offsetTTL,
		emaAlpha:        0.3,
	}
	ts.tiktokenLoader = NewTiktokenLoader()
	return ts
}

// SetTiktokenLoader 注入 tiktoken-go lazy 加载器（依赖注入）。
//
// 吸纳优化 1：lazy import + 5 秒超时降级 L2。
// loader 为 nil 时 L3 路径降级 L2（不阻塞主流程）。
func (ts *tokenSourceImpl) SetTiktokenLoader(loader func() (TiktokenEncoder, error)) {
	ts.offsetsMu.Lock()
	defer ts.offsetsMu.Unlock()
	ts.tiktokenLoader = loader
}

// PreCheckEstimate 请求前估算（L3 优先 / L2 降级）。
//
// 路径选择：
// - L3：tiktokenEnabled=true + model 在 criticalModels 白名单 + loader 加载成功 → tiktoken BPE
// - L2：启发式估算 + CJK 感知 + EMA offset 校准
//
// 任一 L3 前置条件失败均降级 L2，不阻塞主流程。
func (ts *tokenSourceImpl) PreCheckEstimate(messages []MessageEntry, model string) int {
	// L3 路径：critical model + tiktoken 可用
	if ts.tiktokenEnabled && isCriticalModel(model) {
		if encoder, err := ts.loadTiktoken(model); err == nil && encoder != nil {
			return ts.estimateWithTiktoken(messages, encoder)
		}
		// tiktoken 加载失败降级 L2
	}
	// L2 路径：启发式 + CJK 感知 + offset 校准
	estimated := ts.estimator.EstimateMessagesTokens(messages)
	return ts.applyOffset(model, estimated)
}

// PostCallActual 请求后真实 usage（L1 优先 / L2 降级）。
//
// L1 路径：从 UsageTracker.GetRecentModelUsage 读最近一次该 model 的 usage。
// L2 降级：model 从未调用过或远端 usage 缺失（吸纳优化 3），返回零值 + ok=false。
func (ts *tokenSourceImpl) PostCallActual(model string) (UnifiedUsage, bool) {
	if ts.usageTracker == nil {
		return UnifiedUsage{}, false
	}
	return ts.usageTracker.GetRecentModelUsage(model)
}

// ContextCount 上下文管理用计数（M05 调用，应用 offset 校准）。
//
// 复用 TokenEstimator.EstimateMessagesTokens 估算逻辑 + applyOffset EMA 校准。
// 与 PreCheckEstimate L2 路径一致；M05 调用此方法而非自行实现估算（避免逻辑重复）。
func (ts *tokenSourceImpl) ContextCount(messages []MessageEntry, model string) int {
	estimated := ts.estimator.EstimateMessagesTokens(messages)
	return ts.applyOffset(model, estimated)
}

// RecordRemoteUsage 远端 usage 回流（EMA 混合校准反馈）。
//
// B-P3 混合模式（ratio + offset）：见 CalibrationOffset 注释的拟合策略。
// estimated <= 0 时跳过（防除零 + 无意义校准）。
// B-P4 脏样本防护：瞬时 ratio 超出 [0.5, 2]（正常估算器与真实 tokenizer 的
// 比例偏差不会超过 2 倍）时丢弃该样本——此前脏样本（口径错配产生的 ratio≈622）
// 的残差 estimated×(newRatio−ratio) 会产生大负数灌进 offset EMA，把状态栏
// 校准值一路拉低（实测 3 轮后 5649→731 持续递减）。
// offset 缓存过期判断：超过 offsetTTL 时重置 SampleCount=1。
func (ts *tokenSourceImpl) RecordRemoteUsage(model string, actual UnifiedUsage, estimated int) {
	if estimated <= 0 {
		return // 防除零
	}
	newRatio := float64(actual.InputTokens) / float64(estimated)
	// 脏样本防护：口径错配/异常 usage 不进入 EMA。
	// 拒绝时打 canary 日志（静默死亡教训：口径错配曾导致校准永不生效而不可见）。
	if newRatio < 0.5 || newRatio > 2 {
		log.Warn("token calibration sample rejected (dirty ratio)",
			"model", model,
			"ratio", fmt.Sprintf("%.2f", newRatio),
			"estimated", estimated,
			"actual", actual.InputTokens,
			"hint", "estimated/actual 口径错配或异常 usage，检查 EstimateRequestInput 覆盖范围",
		)
		return
	}
	ts.recordSample(model, newRatio, estimated)
}

// recordSample 写入一轮混合校准样本（ratio EMA + 残差 offset EMA）。
// 由 RecordRemoteUsage 调用（已做脏样本防护）；拆分以便测试直接注入 ratio 样本。
func (ts *tokenSourceImpl) recordSample(model string, newRatio float64, estimated int) {
	ts.offsetsMu.Lock()
	defer ts.offsetsMu.Unlock()

	// 过期判断：超过 TTL 重置（不沿用旧 ratio/offset）
	if existing, ok := ts.offsets[model]; ok {
		if time.Since(existing.UpdatedAt) > ts.offsetTTL {
			ts.offsets[model] = ts.newSample(model, newRatio, estimated, existing.Offset)
			return
		}
		// EMA 更新：ratio 先收敛，残差 offset 随 ratio 收敛逐步沉淀固定误差
		existing.Ratio = ts.emaAlpha*newRatio + (1-ts.emaAlpha)*existing.Ratio
		// 残差 = 本轮 actual - ratio 吸纳后的值 = estimated × (瞬时ratio - EMA ratio)
		// 纯比例误差时瞬时 ratio 稳定 → 残差→0；纯恒差时 ratio→1 → 残差→恒差本身。
		residual := float64(estimated) * (newRatio - existing.Ratio)
		existing.Offset = ts.emaAlpha*residual + (1-ts.emaAlpha)*existing.Offset
		existing.SampleCount++
		existing.UpdatedAt = time.Now()
	} else {
		ts.offsets[model] = ts.newSample(model, newRatio, estimated, 0)
	}
}

// newSample 构造首轮样本：ratio = 瞬时比例，offset = 初始残差（可为既有 offset 的延续，供 TTL 重置继承）。
func (ts *tokenSourceImpl) newSample(model string, newRatio float64, estimated int, initialOffset float64) *CalibrationOffset {
	// 首轮：ratio 直接取瞬时值；残差 offset 继承初值（TTL 重置场景保留部分记忆，可选 0）
	return &CalibrationOffset{
		ModelName:   model,
		Ratio:       newRatio,
		Offset:      initialOffset,
		SampleCount: 1,
		UpdatedAt:   time.Now(),
	}
}

// ApplyCalibration 将 EMA offset 校准应用到任意估算值（完整口径含 system+tools+messages）。
//
// 与 ContextCount/PreCheckEstimate 内部校准同源（applyOffset）；独立公开以便
// TUI 层对已组装好的完整估算值做校准，而无需重新按 MessageEntry 逐条估算。
// 并发安全：持 RLock 读取。
func (ts *tokenSourceImpl) ApplyCalibration(model string, estimated int) int {
	return ts.applyOffset(model, estimated)
}

// EstimateRequestInput 完整口径原始估算：system + tools + messages（不应用校准）。
//
// B-P4 口径修复：RecordRemoteUsage 的 estimated 必须与 actual 同口径——
// actual 是 provider 对完整请求输入的计数（system+tools+messages），此前
// Stream 只估 messages（首轮≈10 token），ratio = 6220/10 ≈ 622 直接命中
// 脏数据上界 → 校准永不生效（实测恒差 585 不收敛）。
// 不应用校准（防递归：EstimatedInputTokens → ratio → EstimatedInputTokens 污染样本）。
// system 估算：SystemPromptLayers 非空时按 layers，否则 strings.Join(SystemPrompt)。
// tools 估算：逐个 ToolDefinition JSON 序列化后估文本。
func (ts *tokenSourceImpl) EstimateRequestInput(req Request) int {
	total := 0

	// system：layers 优先，降级 join
	if len(req.SystemPromptLayers) > 0 {
		for _, layer := range req.SystemPromptLayers {
			total += ts.estimator.EstimateTextTokens(layer.Content)
		}
	} else if len(req.SystemPrompt) > 0 {
		total += ts.estimator.EstimateTextTokens(strings.Join(req.SystemPrompt, "\n"))
	}

	// tools：逐个定义 JSON 估算
	for _, tool := range req.Tools {
		if b, err := json.Marshal(tool); err == nil {
			total += ts.estimator.EstimateTextTokens(string(b))
		}
	}

	// messages：原始估算（无校准）
	total += ts.estimator.EstimateMessagesTokens(req.Messages)
	return total
}

// CalibrationInfo 读取指定 model 的校准状态拷贝（可观测性，/context 展示用）。
func (ts *tokenSourceImpl) CalibrationInfo(model string) (CalibrationOffset, bool) {
	ts.offsetsMu.RLock()
	defer ts.offsetsMu.RUnlock()
	existing, ok := ts.offsets[model]
	if !ok {
		return CalibrationOffset{}, false
	}
	return *existing, true
}

// applyOffset 应用混合校准（ratio × estimated + offset）到估算值。
//
// B-P3 混合模式：
// - 无 offset 或 offset 过期：返回原估算值
// - ratio 异常防护：非正数或超界（<0.1 或 >10，正常估算器不会偏这么远）
// 时视为脏数据，只应用 offset；offset 超界（|offset| > estimated×5）同理跳过
// - 校准后值 < 0：返回原估算值（防异常）
// - 正常情况：返回 estimated × ratio + offset（int 截断）
//
// 并发安全：持 RLock 读取。
func (ts *tokenSourceImpl) applyOffset(model string, estimated int) int {
	ts.offsetsMu.RLock()
	defer ts.offsetsMu.RUnlock()

	if existing, ok := ts.offsets[model]; ok {
		if time.Since(existing.UpdatedAt) <= ts.offsetTTL {
			ratio := 1.0
			if existing.Ratio >= 0.1 && existing.Ratio <= 10 {
				ratio = existing.Ratio
			}
			calibrated := float64(estimated)*ratio + existing.Offset
			// offset 异常防护（B-P4 收紧）：残差不应超过估算值本身（固定误差
			// 量级远小于上下文规模）。超界视为脏数据，只信 ratio。
			if existing.Offset < -float64(estimated) || existing.Offset > float64(estimated) {
				calibrated = float64(estimated) * ratio
			}
			if calibrated < 0 {
				return estimated // 校准异常时返回原估算值
			}
			return int(calibrated)
		}
	}
	return estimated // 无 offset 或过期则返回原估算值
}

// isCriticalModel 判断是否为 critical model（白名单查询，统一转小写）。
//
// 吸纳优化 5：model 名大小写不敏感（GPT-4 vs gpt-4 均命中）。
func isCriticalModel(model string) bool {
	_, ok := criticalModels[strings.ToLower(model)]
	return ok
}

// loadTiktoken lazy 加载 tiktoken-go 编码器。
//
// 吸纳优化 1：5 秒超时降级 L2。
// loader 未配置（nil）时返回 error，调用方降级 L2。
func (ts *tokenSourceImpl) loadTiktoken(model string) (TiktokenEncoder, error) {
	ts.offsetsMu.RLock()
	loader := ts.tiktokenLoader
	ts.offsetsMu.RUnlock()

	if loader == nil {
		return nil, fmt.Errorf("tiktoken loader not configured")
	}
	return loader()
}

// estimateWithTiktoken 使用 tiktoken 编码器估算 messages token 数。
//
// 估算规则：
// - 每条消息 4 token overhead（role + formatting）
// - text content 用 tiktoken BPE 编码取 len
// - 非 string content（[]interface{} 等）降级为 TokenEstimator 估算
//
// 吸纳优化 6：Encode 调用传 allowedSpecial=[]string{}（disallow 所有 special tokens）。
func (ts *tokenSourceImpl) estimateWithTiktoken(messages []MessageEntry, encoder TiktokenEncoder) int {
	total := 0
	for _, msg := range messages {
		// 每条消息 4 token overhead（role + formatting）
		total += 4
		switch content := msg.Content.(type) {
		case string:
			total += len(encoder.Encode(content, []string{}))
		case []interface{}:
			// 复杂 content 降级为 TokenEstimator 估算（避免 tiktoken 对 map 编码误差）
			total += ts.estimator.EstimateMessageTokens(msg) - 4 // 减去 overhead 避免重复
		default:
			// 其他类型不增加 token（仅 overhead）
		}
	}
	return total
}
