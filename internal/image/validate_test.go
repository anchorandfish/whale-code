package image

import (
	"strings"
	"testing"
)

// TestValidateSize_Valid 测试正常大小应通过
func TestValidateSize_Valid(t *testing.T) {
	data := strings.Repeat("A", 1000)
	err := ValidateSize(data)
	if err != nil {
		t.Errorf("expected no error for valid size, got %v", err)
	}
}

// TestValidateSize_TooLarge 测试超大 base64 应返回 ImageSizeError
func TestValidateSize_TooLarge(t *testing.T) {
	data := strings.Repeat("A", APIImageMaxBase64Size+1)
	err := ValidateSize(data)
	if err == nil {
		t.Error("expected error for too large size")
	}
	if _, ok := err.(*ImageSizeError); !ok {
		t.Errorf("expected *ImageSizeError, got %T", err)
	}
}

// TestValidateRawSize_Valid 测试原始大小正常应通过
func TestValidateRawSize_Valid(t *testing.T) {
	err := ValidateRawSize(1024)
	if err != nil {
		t.Errorf("expected no error for valid raw size, got %v", err)
	}
}

// TestValidateRawSize_TooLarge 测试原始大小超限应返回 ImageSizeError
func TestValidateRawSize_TooLarge(t *testing.T) {
	err := ValidateRawSize(ImageTargetRawSize + 1)
	if err == nil {
		t.Error("expected error for too large raw size")
	}
	if _, ok := err.(*ImageSizeError); !ok {
		t.Errorf("expected *ImageSizeError, got %T", err)
	}
}

// TestValidateDimensions_Valid 测试正常尺寸应通过
func TestValidateDimensions_Valid(t *testing.T) {
	err := ValidateDimensions(100, 100)
	if err != nil {
		t.Errorf("expected no error for valid dimensions, got %v", err)
	}
}

// TestValidateDimensions_TooLarge 测试超大尺寸应返回错误
func TestValidateDimensions_TooLarge(t *testing.T) {
	// 宽度超限
	err := ValidateDimensions(ImageMaxWidth+1, 100)
	if err == nil {
		t.Error("expected error for too large width")
	}

	// 高度超限
	err = ValidateDimensions(100, ImageMaxHeight+1)
	if err == nil {
		t.Error("expected error for too large height")
	}

	// 都超限
	err = ValidateDimensions(ImageMaxWidth+1, ImageMaxHeight+1)
	if err == nil {
		t.Error("expected error for too large both dimensions")
	}
}

// TestValidateMediaCount_Valid 测试正常媒体数应通过
func TestValidateMediaCount_Valid(t *testing.T) {
	err := ValidateMediaCount(5)
	if err != nil {
		t.Errorf("expected no error for valid media count, got %v", err)
	}
}

// TestValidateMediaCount_TooMany 测试媒体数超限应返回错误
func TestValidateMediaCount_TooMany(t *testing.T) {
	err := ValidateMediaCount(APIMaxMediaPerRequest + 1)
	if err == nil {
		t.Error("expected error for too many media")
	}
}

// TestValidateForAPI_ValidMessages 测试有效消息应通过
func TestValidateForAPI_ValidMessages(t *testing.T) {
	pngData := makeTestPNG(10, 10)
	base64Str := base64Encode(pngData)

	messages := []map[string]interface{}{
		{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{
					"type": "image",
					"source": map[string]interface{}{
						"data": base64Str,
					},
				},
			},
		},
	}

	err := ValidateForAPI(messages)
	if err != nil {
		t.Errorf("expected no error for valid messages, got %v", err)
	}
}

// TestValidateForAPI_TooLarge 测试超大图片应返回错误
func TestValidateForAPI_TooLarge(t *testing.T) {
	largeData := strings.Repeat("A", APIImageMaxBase64Size+1)

	messages := []map[string]interface{}{
		{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{
					"type": "image",
					"source": map[string]interface{}{
						"data": largeData,
					},
				},
			},
		},
	}

	err := ValidateForAPI(messages)
	if err == nil {
		t.Error("expected error for too large image in messages")
	}
	if sizeErr, ok := err.(*ImageSizeError); ok {
		if sizeErr.Index != 0 {
			t.Errorf("expected Index 0, got %d", sizeErr.Index)
		}
	} else {
		t.Errorf("expected *ImageSizeError, got %T", err)
	}
}

// TestImageSizeError_Error 验证 Error() 方法包含 Index 信息
func TestImageSizeError_Error(t *testing.T) {
	// Index >= 0 的情况
	err := &ImageSizeError{
		Size:    1000,
		MaxSize: 500,
		Index:   2,
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "#2") {
		t.Errorf("expected error string to contain '#2', got %s", errStr)
	}

	// Index < 0 的情况
	err2 := &ImageSizeError{
		Size:    1000,
		MaxSize: 500,
		Index:   -1,
	}
	errStr2 := err2.Error()
	if strings.Contains(errStr2, "#") {
		t.Errorf("expected error string not to contain '#', got %s", errStr2)
	}
}
