// Package secret — redact_optimized.go 单元测试（M15-01）。
package secret

import (
	"strings"
	"sync"
	"testing"
)

// TestIsSecretLeakFast_Equivalence 验证 IsSecretLeakFast 与 IsSecretLeak 行为等价。
func TestIsSecretLeakFast_Equivalence(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"clean_text", "hello world, this is a normal log line"},
		{"openai_key", "api_key=sk-abc123def456ghi789jkl012mno345pqr678"},
		{"anthropic_key", "ANTHROPIC_API_KEY=sk-ant-api03-XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"},
		{"github_token", "GH_TOKEN=ghp_0123456789012345678901234567890123456789"},
		{"aws_key", "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE"},
		{"no_match_mixed", "user logged in with avatar https://example.com/avatar.png"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fast := IsSecretLeakFast(tc.text)
			slow := IsSecretLeak(tc.text)
			if fast != slow {
				t.Errorf("IsSecretLeakFast(%q) = %v, IsSecretLeak = %v (should be equal)", tc.text, fast, slow)
			}
		})
	}
}

// TestRedactSensitiveFast_Equivalence 验证 RedactSensitiveFast 与 RedactSensitive 行为等价。
func TestRedactSensitiveFast_Equivalence(t *testing.T) {
	cases := []string{
		"hello world, this is a normal log line",
		"api_key=sk-abc123def456ghi789jkl012mno345pqr678",
		"Bearer abc.def.ghi",
		"postgres://user:password@host:5432/db",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA...\n-----END RSA PRIVATE KEY-----",
		"",
	}
	for i, text := range cases {
		fast := RedactSensitiveFast(text)
		slow := RedactSensitive(text)
		if fast != slow {
			t.Errorf("case %d: RedactSensitiveFast != RedactSensitive\nfast=%q\nslow=%q", i, fast, slow)
		}
	}
}

// TestScanSecretsFast_Equivalence 验证 ScanSecretsFast 与 ScanSecrets 行为等价。
func TestScanSecretsFast_Equivalence(t *testing.T) {
	cases := []string{
		"hello world, this is a normal log line",
		"api_key=sk-abc123def456ghi789jkl012mno345pqr678",
		"Bearer abc.def.ghi",
		"",
	}
	for i, text := range cases {
		fast := ScanSecretsFast(text)
		slow := ScanSecrets(text)
		if len(fast) != len(slow) {
			t.Errorf("case %d: ScanSecretsFast len=%d, ScanSecrets len=%d (should be equal)", i, len(fast), len(slow))
			continue
		}
		for j := range fast {
			if fast[j].RuleID != slow[j].RuleID || fast[j].Label != slow[j].Label {
				t.Errorf("case %d entry %d: fast=%+v slow=%+v (should be equal)", i, j, fast[j], slow[j])
			}
		}
	}
}

// TestRedactSensitiveCached_HitAfterMiss 验证缓存命中后返回缓存结果。
func TestRedactSensitiveCached_HitAfterMiss(t *testing.T) {
	ClearRedactCache()
	input := "api_key=sk-cachedtest1234567890123456789abc"
	// 第一次调用：缓存 miss，走慢路径
	out1 := RedactSensitiveCached(input)
	// 第二次调用：缓存 hit，应返回相同结果
	out2 := RedactSensitiveCached(input)
	if out1 != out2 {
		t.Errorf("Second call should return cached result, got %q vs %q", out1, out2)
	}
	// 验证确实做了脱敏（不是原样返回）
	if out1 == input {
		t.Errorf("Output should be redacted, got same as input: %q", out1)
	}
	if !strings.Contains(out1, "[REDACTED]") {
		t.Errorf("Output should contain [REDACTED], got %q", out1)
	}
}

// TestRedactSensitiveCached_NoCacheForCleanText 验证干净文本不进缓存。
func TestRedactSensitiveCached_NoCacheForCleanText(t *testing.T) {
	ClearRedactCache()
	clean := "this is a clean log line without any secrets"
	out := RedactSensitiveCached(clean)
	if out != clean {
		t.Errorf("Clean text should pass through unchanged, got %q", out)
	}
	// 验证缓存为空（未写入）
	redactCacheMu.RLock()
	cacheLen := len(redactCache)
	redactCacheMu.RUnlock()
	if cacheLen != 0 {
		t.Errorf("Cache should be empty for clean text, got %d entries", cacheLen)
	}
}

// TestRedactSensitiveCached_LRUEviction 验证 LRU 淘汰逻辑。
func TestRedactSensitiveCached_LRUEviction(t *testing.T) {
	ClearRedactCache()
	// 填满缓存（256 条）
	for i := 0; i < redactCacheMax; i++ {
		input := "sk-testkey" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "0123456789abcdefghijklmnop"
		_ = RedactSensitiveCached(input)
	}
	redactCacheMu.RLock()
	cacheLen := len(redactCache)
	redactCacheMu.RUnlock()
	if cacheLen != redactCacheMax {
		t.Errorf("Cache should be full at %d, got %d", redactCacheMax, cacheLen)
	}
	// 再加 1 条，触发 LRU 淘汰（应保持 256 条而非 257）
	extra := "sk-extra" + strings.Repeat("X", 40) + "0123456789abcdef"
	_ = RedactSensitiveCached(extra)
	redactCacheMu.RLock()
	cacheLen = len(redactCache)
	redactCacheMu.RUnlock()
	if cacheLen != redactCacheMax {
		t.Errorf("Cache should still be at %d after eviction, got %d", redactCacheMax, cacheLen)
	}
	// 验证新条目在缓存中
	redactCacheMu.RLock()
	_, hit := redactCache[extra]
	redactCacheMu.RUnlock()
	if !hit {
		t.Error("New entry should be in cache after eviction")
	}
}

// TestRedactSensitiveCached_Concurrent 验证并发安全（-race 检测）。
func TestRedactSensitiveCached_Concurrent(t *testing.T) {
	ClearRedactCache()
	inputs := []string{
		"api_key=sk-test1" + strings.Repeat("x", 30),
		"Bearer abc.def.ghijklmnopqrstuv",
		"postgres://u:p@host:5432/db",
		"clean text without secrets",
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				input := inputs[(idx+j)%len(inputs)]
				_ = RedactSensitiveCached(input)
			}
		}(i)
	}
	wg.Wait()
}
