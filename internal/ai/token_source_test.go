// Package ai — TokenSource 单元测试（T1-B）
//
// 测试覆盖：
// - PreCheckEstimate L2 启发式路径（非 critical model）
// - PreCheckEstimate L3 tiktoken 路径（critical model + mock encoder）
// - PreCheckEstimate L3 降级 L2（tiktokenEnabled=false）
// - PreCheckEstimate L3 降级 L2（loader 返回 error）
// - PostCallActual L1 远端 usage 读取
// - PostCallActual L2 降级（model 从未记录）
// - RecordRemoteUsage 首次采样
// - RecordRemoteUsage EMA 更新公式
// - RecordRemoteUsage offset TTL 过期重置
// - applyOffset 无/有/过期/负数校准
// - ContextCount 应用 offset 校准
// - isCriticalModel 大小写不敏感
// - 并发安全（10 goroutine -race）
package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockTiktokenEncoder 测试用 mock tiktoken 编码器。
//
// 按固定比例 text→tokens（每 5 字符 1 token）模拟 BPE 分词，
// 避免测试依赖网络下载 tiktoken-go 编码文件。
type mockTiktokenEncoder struct {
	ratio int // 每 ratio 字符 1 token
}

// Encode 模拟 tiktoken 编码：返回长度为 len(text)/ratio 的切片。
func (m *mockTiktokenEncoder) Encode(text string, allowedSpecial []string) []int {
	if m.ratio <= 0 {
		m.ratio = 5
	}
	count := len(text) / m.ratio
	if len(text)%m.ratio > 0 {
		count++
	}
	return make([]int, count)
}

// TestPreCheckEstimate_L2_Heuristic 测试非 critical model 走 L2 启发式 + CJK 感知。
//
// 验证：claude-sonnet-4 走 L2 路径，估算结果与 TokenEstimator.EstimateMessagesTokens 一致（无 offset 时）。
func TestPreCheckEstimate_L2_Heuristic(t *testing.T) {
	estimator := NewTokenEstimator()
	tracker := NewUsageTracker("")
	ts := NewTokenSource(estimator, tracker, false, 30*time.Minute)

	messages := []MessageEntry{
		{Role: "user", Content: "Hello, world!"},
	}
	estimated := ts.PreCheckEstimate(messages, "claude-sonnet-4-20250514")
	direct := estimator.EstimateMessagesTokens(messages)
	if estimated != direct {
		t.Errorf("L2 估算不一致: PreCheckEstimate=%d, EstimateMessagesTokens=%d", estimated, direct)
	}
}

// TestPreCheckEstimate_L3_Tiktoken 测试 critical model + tiktokenEnabled=true 走 L3。
//
// 验证：gpt-4 + tiktokenEnabled=true + mock loader 返回成功 → 走 L3 路径，
// 估算结果与 mock tiktoken encoder 一致。
func TestPreCheckEstimate_L3_Tiktoken(t *testing.T) {
	estimator := NewTokenEstimator()
	tracker := NewUsageTracker("")
	ts := NewTokenSource(estimator, tracker, true, 30*time.Minute)

	// 注入 mock loader（返回 mockTiktokenEncoder）
	ts.(*tokenSourceImpl).SetTiktokenLoader(func() (TiktokenEncoder, error) {
		return &mockTiktokenEncoder{ratio: 5}, nil
	})

	messages := []MessageEntry{
		{Role: "user", Content: "Hello, world! This is a test."},
	}
	estimated := ts.PreCheckEstimate(messages, "gpt-4")

	// 期望：4 overhead + len("Hello, world! This is a test.")=28 → 28/5=6（向上取整）
	expected := 4 + 6
	if estimated != expected {
		t.Errorf("L3 tiktoken 估算错误: got=%d, expected=%d", estimated, expected)
	}
}

// TestPreCheckEstimate_L3_Disabled 测试 critical model + tiktokenEnabled=false 降级 L2。
//
// 验证：gpt-4 + tiktokenEnabled=false → 走 L2 路径（不走 tiktoken）。
func TestPreCheckEstimate_L3_Disabled(t *testing.T) {
	estimator := NewTokenEstimator()
	tracker := NewUsageTracker("")
	ts := NewTokenSource(estimator, tracker, false, 30*time.Minute)

	messages := []MessageEntry{
		{Role: "user", Content: "Hello, world!"},
	}
	estimated := ts.PreCheckEstimate(messages, "gpt-4")
	direct := estimator.EstimateMessagesTokens(messages)
	if estimated != direct {
		t.Errorf("tiktokenEnabled=false 应降级 L2: got=%d, expected=%d (L2)", estimated, direct)
	}
}

// TestPreCheckEstimate_L3_LazyLoadFail 测试 tiktoken lazy import 失败降级 L2。
//
// 验证：gpt-4 + tiktokenEnabled=true + loader 返回 error → 降级 L2 路径。
func TestPreCheckEstimate_L3_LazyLoadFail(t *testing.T) {
	estimator := NewTokenEstimator()
	tracker := NewUsageTracker("")
	ts := NewTokenSource(estimator, tracker, true, 30*time.Minute)

	// 注入失败的 loader
	ts.(*tokenSourceImpl).SetTiktokenLoader(func() (TiktokenEncoder, error) {
		return nil, errTiktokenLoadFail
	})

	messages := []MessageEntry{
		{Role: "user", Content: "Hello, world!"},
	}
	estimated := ts.PreCheckEstimate(messages, "gpt-4")
	direct := estimator.EstimateMessagesTokens(messages)
	if estimated != direct {
		t.Errorf("loader 失败应降级 L2: got=%d, expected=%d (L2)", estimated, direct)
	}
}

// TestPostCallActual_L1_Remote 测试远端 usage 已记录返回真实值 + ok=true。
//
// 验证：RecordUsage 后 PostCallActual 返回最近一次 usage。
func TestPostCallActual_L1_Remote(t *testing.T) {
	tracker := NewUsageTracker("")
	tracker.RecordUsage("claude-sonnet-4-20250514", 1000, 500, 200, 100)

	ts := NewTokenSource(NewTokenEstimator(), tracker, false, 30*time.Minute)
	usage, ok := ts.PostCallActual("claude-sonnet-4-20250514")
	if !ok {
		t.Fatal("PostCallActual 应返回 ok=true（已记录）")
	}
	if usage.InputTokens != 1000 {
		t.Errorf("InputTokens: got=%d, expected=1000", usage.InputTokens)
	}
	if usage.OutputTokens != 500 {
		t.Errorf("OutputTokens: got=%d, expected=500", usage.OutputTokens)
	}
	if usage.CacheCreation != 200 {
		t.Errorf("CacheCreation: got=%d, expected=200", usage.CacheCreation)
	}
	if usage.CacheRead != 100 {
		t.Errorf("CacheRead: got=%d, expected=100", usage.CacheRead)
	}
}

// TestPostCallActual_L2_Fallback 测试远端 usage 缺失返回零值 + ok=false（吸纳优化 3）。
//
// 验证：model 从未调用过时 PostCallActual 返回零值 + ok=false。
func TestPostCallActual_L2_Fallback(t *testing.T) {
	tracker := NewUsageTracker("")
	ts := NewTokenSource(NewTokenEstimator(), tracker, false, 30*time.Minute)

	usage, ok := ts.PostCallActual("never-called-model")
	if ok {
		t.Fatal("PostCallActual 应返回 ok=false（从未记录）")
	}
	if usage.InputTokens != 0 || usage.OutputTokens != 0 {
		t.Errorf("PostCallActual 未记录时应返回零值: got=%+v", usage)
	}
}

// TestStream_PopulatesEstimatedInputTokens E2E：校准链路断点回归测试（B-P2）。
//
// 修复前：Request.EstimatedInputTokens 全仓库无赋值点（只读），恒为 0，
// RecordRemoteUsage 因 estimated<=0 直接 return，EMA 校准从未启动——
// 实测状态栏与 provider usage 恒差 ~585 且不收敛。
// 修复后：SimpleClient.Stream 统一填充估算值，UsageEvent 回流建立 offset。
func TestStream_PopulatesEstimatedInputTokens(t *testing.T) {
	// 完整口径请求：system + messages（B-P4）。actual 按 estimated×1.1 构造，
	// 使样本 ratio ≈1.1 落入 [0.5,2] 健康区间（脏样本防护不拦截）。
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)

	req := Request{
		SystemPrompt: []string{"You are a coding assistant. Follow instructions carefully and produce concise answers."},
		Messages:     []MessageEntry{{Role: "user", Content: "Hello, please respond briefly."}},
	}
	estimated := ts.EstimateRequestInput(req)
	if estimated <= 0 {
		t.Fatalf("估算值异常: %d", estimated)
	}
	actualInput := int(float64(estimated) * 1.1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\n"+
			"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":%d}}}\n\n"+
			"event: message_stop\n"+
			"data: {\"type\":\"message_stop\"}\n\n", actualInput)
	}))
	defer server.Close()

	client := NewSimpleClient("claude-sonnet-4", "test-key", server.URL, ProviderAnthropic)
	defer client.Close()
	client.SetTokenSource(ts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := client.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	for range events { // 消费完事件流，确保 UsageEvent 已被处理
	}

	// 校准链路通的直接证据：CalibrationOffset 已写入且样本健康。
	// 若 EstimatedInputTokens 未填充（恒 0），RecordRemoteUsage 直接跳过。
	// 若口径错配（只估 messages），ratio 会超 [0.5,2] 被脏样本防护丢弃。
	impl := ts.(*tokenSourceImpl)
	impl.offsetsMu.RLock()
	off, ok := impl.offsets["claude-sonnet-4"]
	impl.offsetsMu.RUnlock()
	if !ok {
		t.Fatal("CalibrationOffset 未写入（EstimatedInputTokens 未填充或口径错配，校准链路断裂）")
	}
	if off.SampleCount != 1 {
		t.Errorf("SampleCount = %d, want 1", off.SampleCount)
	}
	if off.Ratio < 1.05 || off.Ratio > 1.15 {
		t.Errorf("Ratio = %f, want ≈1.1（完整口径估算 vs actual 同口径）", off.Ratio)
	}
}

// TestApplyCalibration 校准应用于任意估算值（含 system+tools+messages 的完整口径）。
//
// 验证：
// - 有有效 offset 时 ApplyCalibration(model, estimated) = estimated + offset
// - 无采样模型返回原值（nil 防御）
// - 校准后为负时返回原值（异常防御）
func TestApplyCalibration(t *testing.T) {
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)

	// 无采样：返回原值
	if got := ts.ApplyCalibration("gpt-4", 1000); got != 1000 {
		t.Errorf("无采样时 ApplyCalibration = %d, want 1000", got)
	}

	// 采样：actual=1500, estimated=1000 → ratio=1.5（B-P3 混合模式）
	// ApplyCalibration(2000) = 2000×1.5 + 0 = 3000（加性模式时代为 2500）
	ts.RecordRemoteUsage("gpt-4", UnifiedUsage{InputTokens: 1500}, 1000)
	if got := ts.ApplyCalibration("gpt-4", 2000); got != 3000 {
		t.Errorf("ApplyCalibration = %d, want 3000 (2000×1.5)", got)
	}

	// 负值防御：actual 远小于 estimated（比例场景下不触发负值，此用例验证恒差负偏移）
	ts2 := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)
	ts2.RecordRemoteUsage("neg-model", UnifiedUsage{InputTokens: 5}, 100) // ratio=0.05 → 视为脏数据
	if got := ts2.ApplyCalibration("neg-model", 100); got != 100 {        // ratio 超下界 → 只用 offset
		t.Errorf("ratio 脏数据时应回退原值: ApplyCalibration = %d, want 100", got)
	}

	// 其他模型不受影响
	if got := ts.ApplyCalibration("claude-3", 1000); got != 1000 {
		t.Errorf("未采样模型 ApplyCalibration = %d, want 1000", got)
	}
}

// TestRecordRemoteUsage_FirstSample 测试首次采样（B-P3 混合模式）。
//
// 验证：首次 RecordRemoteUsage 后 Ratio = actual/estimated，残差 Offset=0，SampleCount=1。
func TestRecordRemoteUsage_FirstSample(t *testing.T) {
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)

	ts.RecordRemoteUsage("gpt-4", UnifiedUsage{InputTokens: 200}, 150)

	impl := ts.(*tokenSourceImpl)
	impl.offsetsMu.RLock()
	defer impl.offsetsMu.RUnlock()
	offset, ok := impl.offsets["gpt-4"]
	if !ok {
		t.Fatal("offset 未写入")
	}
	if offset.Ratio != 200.0/150.0 { // 200/150 ≈ 1.333
		t.Errorf("首次 Ratio: got=%f, expected=%f", offset.Ratio, 200.0/150.0)
	}
	if offset.Offset != 0 { // 首轮 ratio 全额吸纳，残差为 0
		t.Errorf("首次残差 Offset: got=%f, expected=0", offset.Offset)
	}
	if offset.SampleCount != 1 {
		t.Errorf("SampleCount: got=%d, expected=1", offset.SampleCount)
	}
}

// TestRecordRemoteUsage_EMA_Update 测试 EMA 公式（B-P3 混合模式）。
//
// 验证：二次采样后 Ratio = 0.3*newRatio + 0.7*oldRatio，SampleCount=2。
func TestRecordRemoteUsage_EMA_Update(t *testing.T) {
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)

	// 首次：actual=200, estimated=150 → ratio=200/150
	ts.RecordRemoteUsage("gpt-4", UnifiedUsage{InputTokens: 200}, 150)
	// 二次：actual=180, estimated=150 → newRatio=180/150
	// EMA: ratio = 0.3*(180/150) + 0.7*(200/150)
	ts.RecordRemoteUsage("gpt-4", UnifiedUsage{InputTokens: 180}, 150)

	impl := ts.(*tokenSourceImpl)
	impl.offsetsMu.RLock()
	defer impl.offsetsMu.RUnlock()
	offset, _ := impl.offsets["gpt-4"]
	expected := 0.3*(180.0/150.0) + 0.7*(200.0/150.0)
	if offset.Ratio != expected {
		t.Errorf("EMA Ratio: got=%f, expected=%f", offset.Ratio, expected)
	}
	if offset.SampleCount != 2 {
		t.Errorf("SampleCount: got=%d, expected=2", offset.SampleCount)
	}
}

// TestRecordRemoteUsage_OffsetTTL_Expiry 测试 offset 30 分钟过期后重置。
//
// 验证：offset 超过 TTL 后 RecordRemoteUsage 重置 SampleCount=1（不沿用旧 ratio/offset）。
func TestRecordRemoteUsage_OffsetTTL_Expiry(t *testing.T) {
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)

	// 首次采样：ratio=200/150
	ts.RecordRemoteUsage("gpt-4", UnifiedUsage{InputTokens: 200}, 150)

	impl := ts.(*tokenSourceImpl)
	// 手动将 UpdatedAt 设为 31 分钟前（模拟过期）
	impl.offsetsMu.Lock()
	impl.offsets["gpt-4"].UpdatedAt = time.Now().Add(-31 * time.Minute)
	impl.offsetsMu.Unlock()

	// 二次采样（应触发过期重置）：ratio 重置为 180/150，offset 继承旧值（TTL 重置保留固定误差记忆）
	ts.RecordRemoteUsage("gpt-4", UnifiedUsage{InputTokens: 180}, 150)

	impl.offsetsMu.RLock()
	defer impl.offsetsMu.RUnlock()
	offset, _ := impl.offsets["gpt-4"]
	if offset.SampleCount != 1 {
		t.Errorf("过期后 SampleCount 应重置为 1: got=%d", offset.SampleCount)
	}
	if offset.Ratio != 180.0/150.0 { // 1.2（重置为新值，非 EMA）
		t.Errorf("过期后 Ratio 应为 %f: got=%f", 180.0/150.0, offset.Ratio)
	}
}

// TestApplyOffset_NoOffset 测试无 offset 时返回原估算值。
func TestApplyOffset_NoOffset(t *testing.T) {
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)
	impl := ts.(*tokenSourceImpl)

	result := impl.applyOffset("no-offset-model", 100)
	if result != 100 {
		t.Errorf("无 offset 时应返回原值: got=%d, expected=100", result)
	}
}

// TestApplyOffset_WithOffset 测试有 offset 时返回估算值 + offset。
func TestApplyOffset_WithOffset(t *testing.T) {
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)
	impl := ts.(*tokenSourceImpl)

	// 手动写入 offset
	impl.offsetsMu.Lock()
	impl.offsets["gpt-4"] = &CalibrationOffset{
		Offset:      10.5,
		UpdatedAt:   time.Now(),
		SampleCount: 1,
	}
	impl.offsetsMu.Unlock()

	result := impl.applyOffset("gpt-4", 100)
	if result != 110 { // 100 + 10.5 = 110.5 → int(110.5) = 110
		t.Errorf("有 offset 时应返回 110: got=%d", result)
	}
}

// TestApplyOffset_ExpiredOffset 测试 offset 过期时返回原估算值。
func TestApplyOffset_ExpiredOffset(t *testing.T) {
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)
	impl := ts.(*tokenSourceImpl)

	// 手动写入已过期的 offset
	impl.offsetsMu.Lock()
	impl.offsets["gpt-4"] = &CalibrationOffset{
		Offset:      10,
		UpdatedAt:   time.Now().Add(-31 * time.Minute), // 31 分钟前，已过期
		SampleCount: 1,
	}
	impl.offsetsMu.Unlock()

	result := impl.applyOffset("gpt-4", 100)
	if result != 100 {
		t.Errorf("过期 offset 应返回原值: got=%d, expected=100", result)
	}
}

// TestApplyOffset_NegativeCalibration 测试校准后负数时返回原估算值（防异常）。
func TestApplyOffset_NegativeCalibration(t *testing.T) {
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)
	impl := ts.(*tokenSourceImpl)

	// offset = -150，estimated = 100 → calibrated = -50 < 0
	impl.offsetsMu.Lock()
	impl.offsets["gpt-4"] = &CalibrationOffset{
		Offset:      -150,
		UpdatedAt:   time.Now(),
		SampleCount: 1,
	}
	impl.offsetsMu.Unlock()

	result := impl.applyOffset("gpt-4", 100)
	if result != 100 {
		t.Errorf("负数校准应返回原值: got=%d, expected=100", result)
	}
}

// TestContextCount_OffsetApplied 测试 ContextCount 应用 offset 校准。
//
// 验证：ContextCount 与 PreCheckEstimate L2 路径一致（均应用 offset）。
func TestContextCount_OffsetApplied(t *testing.T) {
	estimator := NewTokenEstimator()
	ts := NewTokenSource(estimator, NewUsageTracker(""), false, 30*time.Minute)
	impl := ts.(*tokenSourceImpl)

	// 写入混合校准参数（B-P3：Ratio 必填，零值 0 会触发脏数据防护回退 ratio=1）
	impl.offsetsMu.Lock()
	impl.offsets["claude-sonnet-4"] = &CalibrationOffset{
		Ratio:       1.0,
		Offset:      20,
		UpdatedAt:   time.Now(),
		SampleCount: 1,
	}
	impl.offsetsMu.Unlock()

	messages := []MessageEntry{
		{Role: "user", Content: strings.Repeat("Hello world, this is a test message with enough length. ", 5)},
	}
	direct := estimator.EstimateMessagesTokens(messages)
	if direct <= 20 {
		t.Fatalf("测试前置失败：消息估算(%d)应大于 offset(20)，否则触发 offset 超界防护", direct)
	}
	count := ts.ContextCount(messages, "claude-sonnet-4")
	if count != direct+20 {
		t.Errorf("ContextCount 应应用 offset: got=%d, expected=%d", count, direct+20)
	}
}

// TestIsCriticalModel_CaseInsensitive 测试 model 名大小写不敏感（优化 5）。
//
// 验证：GPT-4 / gpt-4 / Gpt-4 均命中 criticalModels 白名单。
func TestIsCriticalModel_CaseInsensitive(t *testing.T) {
	cases := []struct {
		model    string
		expected bool
	}{
		{"gpt-4", true},
		{"GPT-4", true},
		{"Gpt-4", true},
		{"gpt-4o", true},
		{"GPT-4O", true},
		{"gpt-4.1", true},
		{"gpt-4.5", true},
		{"gpt-5", true},
		{"claude-sonnet-4", false},
		{"", false},
	}
	for _, c := range cases {
		result := isCriticalModel(c.model)
		if result != c.expected {
			t.Errorf("isCriticalModel(%q): got=%v, expected=%v", c.model, result, c.expected)
		}
	}
}

// TestTokenSource_ConcurrentSafe 测试 10 goroutine 并发调用无 race。
//
// 验证：多 goroutine 并发 PreCheckEstimate + RecordRemoteUsage + applyOffset 无数据竞争。
// 必须用 -race 运行：go test -race -run TestTokenSource_ConcurrentSafe
func TestTokenSource_ConcurrentSafe(t *testing.T) {
	estimator := NewTokenEstimator()
	tracker := NewUsageTracker("")
	tracker.RecordUsage("gpt-4", 1000, 500, 0, 0)
	ts := NewTokenSource(estimator, tracker, false, 30*time.Minute)

	messages := []MessageEntry{
		{Role: "user", Content: "Hello, world!"},
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// 并发读（PreCheckEstimate + PostCallActual + ContextCount）
			_ = ts.PreCheckEstimate(messages, "gpt-4")
			_, _ = ts.PostCallActual("gpt-4")
			_ = ts.ContextCount(messages, "gpt-4")
			// 并发写（RecordRemoteUsage）
			ts.RecordRemoteUsage("gpt-4", UnifiedUsage{
				InputTokens:  1000 + idx,
				OutputTokens: 500,
			}, 900+idx)
		}(i)
	}
	wg.Wait()
	// 若无 race detector 报告则通过
}

// TestApplyCalibration_HybridRatioOffset B-P3 混合校准：比例+偏移模式。
//
// 校准模型：calibrated = estimated × ratio + offset
// - ratio（乘法系数）：吸纳比例误差（中文/代码 token 密度偏差，随内容量缩放）
// - offset（加法常数）：吸纳固定误差（消息格式 overhead、role 包装）
//
// 验证：
// - 纯固定误差场景：ratio 收敛到 1，offset 吃掉恒差（与旧加性模式行为一致）
// - 纯比例误差场景：offset 收敛到 0，ratio 吃掉系数偏差（旧加性模式的盲区）
// - 混合误差场景：两者共同分担
// - 大注入场景：估算值放大 10 倍时，比例校准跟着放大（加性模式会失准）
func TestApplyCalibration_HybridRatioOffset(t *testing.T) {
	// 场景 1：纯比例误差（加性 offset 的结构性盲区）
	// actual = 1.5 × estimated：estimated=1000 → actual=1500；estimated=10000 → actual=15000
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)
	for i := 0; i < 10; i++ { // 喂恒定比例样本让 EMA 收敛
		ts.RecordRemoteUsage("prop-model", UnifiedUsage{InputTokens: 1500}, 1000)
	}
	// 收敛后：estimated=10000 → 校准值应接近 15000（±5%）
	got := ts.ApplyCalibration("prop-model", 10000)
	if got < 14250 || got > 15750 {
		t.Errorf("纯比例误差场景：ApplyCalibration(10000) = %d, want ≈15000（ratio 校准未生效，仍为加性偏移）", got)
	}

	// 场景 2：大注入——比例校准随内容量缩放，加性模式在此失准
	// estimated=100（小输入）→ actual=150；用该样本校准后再估算 100000（大注入）
	ts2 := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)
	for i := 0; i < 5; i++ {
		ts2.RecordRemoteUsage("inject-model", UnifiedUsage{InputTokens: 150}, 100)
	}
	// 大注入估算 100000：比例模式 → ~150000；加性模式 → ~100050（严重低估）
	got2 := ts2.ApplyCalibration("inject-model", 100000)
	if got2 < 130000 {
		t.Errorf("大注入场景：ApplyCalibration(100000) = %d, want ≥130000（比例校准未随内容量缩放）", got2)
	}

	// 场景 3：恒差（用户实测 585 场景）仍被正确吸纳
	// actual = estimated + 585：多轮采样后 offset 主导
	ts3 := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)
	for i := 0; i < 10; i++ {
		ts3.RecordRemoteUsage("const-model", UnifiedUsage{InputTokens: 1585}, 1000)
	}
	got3 := ts3.ApplyCalibration("const-model", 1000)
	if got3 < 1500 || got3 > 1670 {
		t.Errorf("恒差场景：ApplyCalibration(1000) = %d, want ≈1585（固定误差未被吸纳）", got3)
	}
}

// TestRecordRemoteUsage_HybridFields B-P3 混合校准：RecordRemoteUsage 后
// CalibrationOffset 的 ratio/offset 字段均被写入（EMA 平滑）。
func TestRecordRemoteUsage_HybridFields(t *testing.T) {
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)

	// 首次：actual=1500, estimated=1000 → ratio=1.5, residual offset=0
	ts.RecordRemoteUsage("hybrid-model", UnifiedUsage{InputTokens: 1500}, 1000)

	impl := ts.(*tokenSourceImpl)
	impl.offsetsMu.RLock()
	defer impl.offsetsMu.RUnlock()
	off, ok := impl.offsets["hybrid-model"]
	if !ok {
		t.Fatal("offset 未写入")
	}
	if off.Ratio != 1.5 {
		t.Errorf("首次 Ratio: got=%f, want=1.5（actual/estimated）", off.Ratio)
	}
	// ratio 已吸纳全部误差 → 残差 offset 应为 0
	if off.Offset != 0 {
		t.Errorf("首次残差 Offset: got=%f, want=0（ratio 全额吸纳后残差为 0）", off.Offset)
	}
	if off.SampleCount != 1 {
		t.Errorf("SampleCount: got=%d, want=1", off.SampleCount)
	}
}

// TestRecordRemoteUsage_DirtySampleGuard B-P4 脏样本防护。
//
// 修复前：口径错配样本（estimated 只估 messages≈10，actual 含 system+tools≈6220）
// 产生 ratio≈622 进入 EMA；其残差 estimated×(newRatio−ratio) 产生大负数灌进
// offset EMA → applyOffset 把状态栏校准值一路拉低（实测 5649→731 持续递减）。
// 修复后：瞬时 ratio 超出 [0.5, 2] 的样本直接丢弃，不污染 EMA。
func TestRecordRemoteUsage_DirtySampleGuard(t *testing.T) {
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)

	// 健康样本：ratio = 6220/5650 ≈ 1.10（完整口径 vs 完整口径）
	ts.RecordRemoteUsage("guard-model", UnifiedUsage{InputTokens: 6220}, 5650)

	// 脏样本：ratio = 6220/10 = 622（口径错配），应被丢弃
	ts.RecordRemoteUsage("guard-model", UnifiedUsage{InputTokens: 6220}, 10)

	impl := ts.(*tokenSourceImpl)
	impl.offsetsMu.RLock()
	off, ok := impl.offsets["guard-model"]
	impl.offsetsMu.RUnlock()
	if !ok {
		t.Fatal("健康样本应写入")
	}
	if off.SampleCount != 1 {
		t.Errorf("脏样本应被丢弃: SampleCount = %d, want 1", off.SampleCount)
	}
	if off.Ratio < 1.05 || off.Ratio > 1.15 {
		t.Errorf("ratio 应保持健康值 ≈1.10: got %f", off.Ratio)
	}
	// 脏 offset 不应把校准值拉到远低于估算
	if got := ts.ApplyCalibration("guard-model", 5650); got < 5000 {
		t.Errorf("ApplyCalibration(5650) = %d, 不可被脏样本拉低（修复前会递减到 731）", got)
	}
}

// TestEstimateRequestInput_FullScope B-P4 完整口径估算。
//
// 验证：EstimateRequestInput = system + tools + messages（与 actual 同口径），
// 而非只估 messages（B-P2 旧修复的口径错配根因）。
func TestEstimateRequestInput_FullScope(t *testing.T) {
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)
	impl := ts.(*tokenSourceImpl)

	messagesOnly := []MessageEntry{{Role: "user", Content: "请回复ok"}}
	req := Request{
		SystemPrompt: []string{"You are a coding assistant with many detailed rules."},
		Tools: []ToolDefinition{
			{Name: "read_file", Description: "Read file content from disk", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}}},
		},
		Messages: messagesOnly,
	}

	full := ts.EstimateRequestInput(req)
	msgs := impl.estimator.EstimateMessagesTokens(messagesOnly)
	if full <= msgs {
		t.Errorf("完整口径估算(%d)应大于纯 messages(%d)：system+tools 未计入", full, msgs)
	}
	// 脏样本防护下 ratio 可用性：actual/estimated 应在 [0.5,2] 内（粗略同量级）
	if ratio := float64(full) / float64(msgs); ratio > 100 {
		t.Errorf("完整口径与 messages 口径比值 %.1f 仍过大：口径错配未修复", ratio)
	}
}

// TestRecordRemoteUsage_ZeroEstimated 测试 estimated<=0 时跳过（防除零 + 无意义校准）。
func TestRecordRemoteUsage_ZeroEstimated(t *testing.T) {
	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)

	// estimated=0 应跳过
	ts.RecordRemoteUsage("gpt-4", UnifiedUsage{InputTokens: 100}, 0)

	impl := ts.(*tokenSourceImpl)
	impl.offsetsMu.RLock()
	defer impl.offsetsMu.RUnlock()
	if _, ok := impl.offsets["gpt-4"]; ok {
		t.Error("estimated=0 时不应写入 offset")
	}
}

// errTiktokenLoadFail 测试用错误常量（模拟 tiktoken 加载失败）。
var errTiktokenLoadFail = errTiktokenLoadFailErr{}

type errTiktokenLoadFailErr struct{}

func (e errTiktokenLoadFailErr) Error() string { return "tiktoken load failed (mock)" }
