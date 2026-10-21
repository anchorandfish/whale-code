// Package util — 安全文件读取与编码解码。
//
// ReadFileSafe 封装"读取文件 + 编码检测 + 转 UTF-8"全流程，
// 处理大文件跳过与二进制文件跳过，返回结构化结果供调用方决策。
package util

import (
	"fmt"
	"os"
	"path/filepath"
)

// ReadFileResult 是 ReadFileSafe 的返回结构。
type ReadFileResult struct {
	Content  string   // 解码为 UTF-8 的文本内容（二进制文件为占位符）
	Encoding Encoding // 实际检测到的编码
	Bytes    int64    // 文件原始字节数
	Skipped  bool     // 是否因大小/二进制被跳过（返回占位符）
}

// ReadFileSafe 安全读取文件并解码为 UTF-8。
//
// 流程：
//  1. filepath.Abs(path)
//  2. os.Stat：不存在返回 err；size > maxBytes（maxBytes>0 时）→ Skipped=true + 占位符
//  3. os.ReadFile 读取全文
//  4. DetectEncoding → 若 EncodingBinary → Skipped=true + 占位符
//  5. DecodeToUTF8 解码为 UTF-8
//
// maxBytes <= 0 表示不限制。
func ReadFileSafe(path string, maxBytes int64) (*ReadFileResult, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, err
	}
	size := info.Size()

	// 大小限制
	if maxBytes > 0 && size > maxBytes {
		return &ReadFileResult{
			Content:  fmt.Sprintf("[file too large: %d bytes, max %d]", size, maxBytes),
			Encoding: EncodingUnknown,
			Bytes:    size,
			Skipped:  true,
		}, nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}

	enc := DetectEncoding(data)
	if enc == EncodingBinary {
		return &ReadFileResult{
			Content:  fmt.Sprintf("[binary file, %d bytes, encoding: binary]", size),
			Encoding: EncodingBinary,
			Bytes:    size,
			Skipped:  true,
		}, nil
	}

	content, decErr := DecodeToUTF8(data)
	if decErr != nil {
		// 解码失败：返回原始内容（不丢数据）
		content = string(data)
	}

	return &ReadFileResult{
		Content:  content,
		Encoding: enc,
		Bytes:    size,
		Skipped:  false,
	}, nil
}
