// Package tools — N16 APILimits 测试。
// 校验 DefaultAPILimits 值与同类 CLI apiLimits.ts 对齐。
package tools

import (
	"testing"
)

// TestDefaultAPILimits_Values 校验默认 API 限制值对齐业内既有实现。
func TestDefaultAPILimits_Values(t *testing.T) {
	if DefaultAPILimits.MaxMediaPerRequest != 100 {
		t.Errorf("MaxMediaPerRequest 应为 100，got %d", DefaultAPILimits.MaxMediaPerRequest)
	}
	if DefaultAPILimits.MaxImageBase64Size != 5<<20 {
		t.Errorf("MaxImageBase64Size 应为 5MB，got %d", DefaultAPILimits.MaxImageBase64Size)
	}
	if DefaultAPILimits.MaxImageDimension != 2000 {
		t.Errorf("MaxImageDimension 应为 2000，got %d", DefaultAPILimits.MaxImageDimension)
	}
	if DefaultAPILimits.MaxPDFPages != 100 {
		t.Errorf("MaxPDFPages 应为 100，got %d", DefaultAPILimits.MaxPDFPages)
	}
	if DefaultAPILimits.MaxToolResultsPerMsg != 20 {
		t.Errorf("MaxToolResultsPerMsg 应为 20，got %d", DefaultAPILimits.MaxToolResultsPerMsg)
	}
}
