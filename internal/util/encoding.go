// Package util — 编码识别与解码（canonical 实现，供 tools 等包薄包装引用）。
//
// 本文件提供：
//   - Encoding 类型与常量（10 种编码）
//   - BinaryExts 二进制扩展名表（canonical 来源）
//   - DetectEncoding 字节流编码检测（不读盘，纯字节分析）
//   - DecodeToUTF8 按检测到的编码解码为 UTF-8 字符串
package util

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

// Encoding 表示文本字节流的编码识别结果。
type Encoding string

const (
	EncodingUTF8     Encoding = "utf-8"
	EncodingUTF8BOM  Encoding = "utf-8-bom"
	EncodingUTF16LE  Encoding = "utf-16le"
	EncodingUTF16BE  Encoding = "utf-16be"
	EncodingGBK      Encoding = "gbk"
	EncodingBig5     Encoding = "big5"
	EncodingShiftJIS Encoding = "shift-jis"
	EncodingLatin1   Encoding = "latin-1"
	EncodingBinary   Encoding = "binary"
	EncodingUnknown  Encoding = "unknown"
)

// BinaryExts 是二进制文件扩展名集合（canonical 来源，供 tools 包引用）。
// 大小写不敏感：使用前应 strings.ToLower。运行期只读。
var BinaryExts = map[string]bool{
	".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".bmp": true, ".ico": true, ".webp": true, ".svg": true,
	".zip": true, ".tar": true, ".gz": true, ".rar": true,
	".7z": true, ".bz2": true, ".xz": true,
	".mp3": true, ".mp4": true, ".wav": true, ".avi": true,
	".mov": true, ".mkv": true, ".flac": true,
	".pdf": true, ".doc": true, ".docx": true,
	".pyc": true, ".o": true, ".a": true, ".class": true,
	".wasm": true, ".node": true,
}

// maxScanBytes 限制编码检测扫描的字节数（8KB），
// 超过部分不参与检测但 DecodeToUTF8 仍会完整解码。
const maxScanBytes = 8192

// minCJKDataLen CJK 启发式检测的最小数据长度。
// 短于 4 字节的数据不参与 CJK 检测，直接 fallback 到 Latin1。
const minCJKDataLen = 4

// minCJKPairs CJK 启发式检测要求的最少有效双字节对数。
// 低于 2 对（4 字节）视为噪声，不判定为 CJK 编码。
const minCJKPairs = 2

// fullCoverageDataLen 短数据阈值：数据长度 ≤ 此值时要求 100% CJK 字节对覆盖，
// 避免随机高位字节误判为 CJK 编码。
const fullCoverageDataLen = 8

// DetectEncoding 检测字节流的文本编码（不读盘，纯字节分析）。
//
// 检测顺序：
//  1. len(data) == 0 → EncodingUnknown
//  2. UTF-16 BOM (FF FE / FE FF) → EncodingUTF16LE / EncodingUTF16BE
//     （BOM 优先于 NUL 检测，因 UTF-16 ASCII 字符含 0x00 字节）
//  3. UTF-8 BOM (EF BB BF) → EncodingUTF8BOM
//  4. 前 8KB 含 NUL 字节 → EncodingBinary
//  5. utf8.Valid(data) → EncodingUTF8
//  6. detectCJKHeuristic(data) → EncodingGBK / EncodingBig5 / EncodingShiftJIS
//  7. fallback → EncodingLatin1
func DetectEncoding(data []byte) Encoding {
	if len(data) == 0 {
		return EncodingUnknown
	}

	// UTF-16 LE BOM: FF FE（必须在 NUL 检测之前，因 UTF-16 含 0x00 字节）
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		return EncodingUTF16LE
	}
	// UTF-16 BE BOM: FE FF
	if len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF {
		return EncodingUTF16BE
	}
	// UTF-8 BOM: EF BB BF
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return EncodingUTF8BOM
	}

	// 扫描前 8KB 检测 NUL 字节（无 BOM 的 NUL 判为二进制）
	scanLen := len(data)
	if scanLen > maxScanBytes {
		scanLen = maxScanBytes
	}
	for i := 0; i < scanLen; i++ {
		if data[i] == 0 {
			return EncodingBinary
		}
	}

	// 纯 UTF-8（无 BOM）
	if utf8.Valid(data) {
		return EncodingUTF8
	}

	// CJK 启发式检测
	if enc := detectCJKHeuristic(data); enc != EncodingUnknown {
		return enc
	}

	// fallback Latin1（不丢数据，逐字节解码）
	return EncodingLatin1
}

// detectCJKHeuristic 无 BOM 的 CJK 编码启发式检测。
//
// 扫描前 8KB，统计各编码的有效双字节对数，按特异性从高到低判定：
//  1. Shift-JIS：首字节 0x81-0x9F / 0xE0-0xEF（首字节范围最窄，优先检查）
//  2. Big5：首字节 0xA1-0xF9，且次字节主要分布在 0x40-0x7E（ASCII 区）
//  3. GBK：首字节 0x81-0xFE（范围最宽，兜底判定）
//
// 阈值策略：
//   - 短数据（≤ 8 字节）：要求 100% 字节对覆盖（所有字节均构成 CJK 对）
//   - 长数据（> 8 字节）：要求 ≥30% 字节对覆盖
//   - 最少 2 对（4 字节）有效 CJK 序列
func detectCJKHeuristic(data []byte) Encoding {
	if len(data) < minCJKDataLen {
		return EncodingUnknown
	}

	scanLen := len(data)
	if scanLen > maxScanBytes {
		scanLen = maxScanBytes
	}

	var sjisPairs, big5Pairs, gbkPairs int
	var big5LowSecond, gbkHighSecond int // 次字节分布统计（用于 Big5/GBK 区分）

	i := 0
	for i < scanLen-1 {
		b1 := data[i]
		b2 := data[i+1]
		matched := false

		// Shift-JIS：首字节 0x81-0x9F / 0xE0-0xEF，次字节 0x40-0x7E / 0x80-0xFC
		if (b1 >= 0x81 && b1 <= 0x9F) || (b1 >= 0xE0 && b1 <= 0xEF) {
			if (b2 >= 0x40 && b2 <= 0x7E) || (b2 >= 0x80 && b2 <= 0xFC) {
				sjisPairs++
				matched = true
			}
		}

		// Big5：首字节 0xA1-0xF9，次字节 0x40-0x7E / 0xA1-0xFE
		if b1 >= 0xA1 && b1 <= 0xF9 {
			if (b2 >= 0x40 && b2 <= 0x7E) || (b2 >= 0xA1 && b2 <= 0xFE) {
				big5Pairs++
				if b2 >= 0x40 && b2 <= 0x7E {
					big5LowSecond++
				}
				matched = true
			}
		}

		// GBK：首字节 0x81-0xFE，次字节 0x40-0x7E / 0x80-0xFE
		if b1 >= 0x81 && b1 <= 0xFE {
			if (b2 >= 0x40 && b2 <= 0x7E) || (b2 >= 0x80 && b2 <= 0xFE) {
				gbkPairs++
				if b2 >= 0x80 && b2 <= 0xFE {
					gbkHighSecond++
				}
				matched = true
			}
		}

		if matched {
			i += 2
		} else {
			i++
		}
	}

	// 阈值计算：短数据要求 100% 覆盖，长数据要求 30%
	var threshold int
	if scanLen <= fullCoverageDataLen {
		threshold = scanLen // 100% 覆盖
	} else {
		threshold = scanLen * 30 / 100
	}

	// Shift-JIS 优先（首字节范围最特异）
	if sjisPairs >= minCJKPairs && sjisPairs*2 >= threshold {
		return EncodingShiftJIS
	}

	// Big5：要求 low second byte 占优（Big5 常用 ASCII 区次字节）
	if big5Pairs >= minCJKPairs && big5Pairs*2 >= threshold && big5LowSecond > gbkHighSecond {
		return EncodingBig5
	}

	// GBK 兜底
	if gbkPairs >= minCJKPairs && gbkPairs*2 >= threshold {
		return EncodingGBK
	}

	return EncodingUnknown
}

// DecodeToUTF8 将字节流按检测到的编码解码为 UTF-8 字符串。
//
// 行为：
//   - EncodingUTF8 / EncodingUTF8BOM：剥离 BOM 后原样返回
//   - EncodingUTF16LE / EncodingUTF16BE：用 unicode/utf16.Decode 转码
//   - EncodingGBK / EncodingBig5 / EncodingShiftJIS：用 golang.org/x/text 编码包转码
//   - EncodingLatin1：用 charmap.ISO8859_1 解码
//   - EncodingBinary：返回占位符 "[binary content, N bytes]"
//   - EncodingUnknown：返回空串 + error
//   - 转码失败时返回已解码部分 + error（不丢数据）
func DecodeToUTF8(data []byte) (string, error) {
	enc := DetectEncoding(data)
	switch enc {
	case EncodingUTF8:
		return string(data), nil
	case EncodingUTF8BOM:
		return string(data[3:]), nil
	case EncodingUTF16LE:
		return decodeUTF16(data[2:], true), nil
	case EncodingUTF16BE:
		return decodeUTF16(data[2:], false), nil
	case EncodingGBK:
		return decodeWithEncoding(data, simplifiedchinese.GBK.NewDecoder())
	case EncodingBig5:
		return decodeWithEncoding(data, traditionalchinese.Big5.NewDecoder())
	case EncodingShiftJIS:
		return decodeWithEncoding(data, japanese.ShiftJIS.NewDecoder())
	case EncodingLatin1:
		return decodeWithEncoding(data, charmap.ISO8859_1.NewDecoder())
	case EncodingBinary:
		return fmt.Sprintf("[binary content, %d bytes]", len(data)), nil
	case EncodingUnknown:
		return "", errors.New("util: unknown encoding")
	}
	return "", errors.New("util: unsupported encoding")
}

// decodeWithEncoding 使用指定编码解码器流式解码字节流。
// 转码失败时返回已解码部分 + error（不丢数据）。
func decodeWithEncoding(data []byte, decoder *encoding.Decoder) (string, error) {
	reader := transform.NewReader(bytes.NewReader(data), decoder)
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return string(decoded), err
	}
	return string(decoded), nil
}

// decodeUTF16 将 UTF-16 字节流（已剥 BOM）解码为 UTF-8 字符串。
// littleEndian=true 表示 LE 序，false 表示 BE 序。
func decodeUTF16(data []byte, littleEndian bool) string {
	if len(data)%2 != 0 {
		data = data[:len(data)-1] // 奇数字节丢弃末尾
	}
	u16 := make([]uint16, len(data)/2)
	for i := 0; i < len(data); i += 2 {
		if littleEndian {
			u16[i/2] = uint16(data[i]) | uint16(data[i+1])<<8
		} else {
			u16[i/2] = uint16(data[i])<<8 | uint16(data[i+1])
		}
	}
	return string(utf16.Decode(u16))
}
