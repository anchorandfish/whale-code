package image

import (
	"strings"
	"testing"
)

// TestEstimateTokens_Basic 测试基础 token 估算
func TestEstimateTokens_Basic(t *testing.T) {
	// 空字符串返回 0
	tokens := EstimateTokens("")
	if tokens != 0 {
		t.Errorf("expected 0 tokens for empty string, got %d", tokens)
	}

	// 1000 字符 base64 返回 ceil(1000*0.125)=125
	base64Data := strings.Repeat("A", 1000)
	tokens = EstimateTokens(base64Data)
	if tokens != 125 {
		t.Errorf("expected 125 tokens for 1000 chars, got %d", tokens)
	}
}

// TestEstimateTokens_Ceil 测试向上取整
func TestEstimateTokens_Ceil(t *testing.T) {
	// 9 字符 → ceil(9*0.125)=ceil(1.125)=2
	base64Data := strings.Repeat("A", 9)
	tokens := EstimateTokens(base64Data)
	if tokens != 2 {
		t.Errorf("expected 2 tokens for 9 chars, got %d", tokens)
	}

	// 8 字符 → ceil(8*0.125)=ceil(1)=1
	base64Data = strings.Repeat("A", 8)
	tokens = EstimateTokens(base64Data)
	if tokens != 1 {
		t.Errorf("expected 1 token for 8 chars, got %d", tokens)
	}

	// 1 字符 → ceil(1*0.125)=ceil(0.125)=1
	base64Data = "A"
	tokens = EstimateTokens(base64Data)
	if tokens != 1 {
		t.Errorf("expected 1 token for 1 char, got %d", tokens)
	}
}

// TestBytesForTokens 测试 token 转字节
func TestBytesForTokens(t *testing.T) {
	// 100 tokens → 600 bytes
	bytes := BytesForTokens(100)
	if bytes != 600 {
		t.Errorf("expected 600 bytes for 100 tokens, got %d", bytes)
	}

	// 0 tokens → 0
	bytes = BytesForTokens(0)
	if bytes != 0 {
		t.Errorf("expected 0 bytes for 0 tokens, got %d", bytes)
	}

	// 负数 tokens → 0
	bytes = BytesForTokens(-10)
	if bytes != 0 {
		t.Errorf("expected 0 bytes for negative tokens, got %d", bytes)
	}
}

// TestCompressWithTokenLimit_Positive 测试正 token 限制下的压缩
func TestCompressWithTokenLimit_Positive(t *testing.T) {
	pngData := makeTestPNG(50, 50)
	compressed, w, h, err := CompressWithTokenLimit(pngData, "image/png", 10000)
	if err != nil {
		t.Fatalf("CompressWithTokenLimit failed: %v", err)
	}
	if len(compressed) == 0 {
		t.Error("expected non-empty compressed data")
	}
	if w <= 0 || h <= 0 {
		t.Errorf("expected positive dimensions, got %dx%d", w, h)
	}
}

// TestCompressWithTokenLimit_ZeroTokens 测试 maxTokens=0 应返回错误
func TestCompressWithTokenLimit_ZeroTokens(t *testing.T) {
	pngData := makeTestPNG(10, 10)
	_, _, _, err := CompressWithTokenLimit(pngData, "image/png", 0)
	if err == nil {
		t.Error("expected error for maxTokens=0")
	}
}
