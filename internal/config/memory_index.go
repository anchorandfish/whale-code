// Package config — memory_index.go MEMORY.md 入口索引（N12 P1-2，）
//
// MemoryIndex 维护 MEMORY.md 入口文件（对标同类 CLI memdir.ts ENTRYPOINT_NAME）。
// MEMORY.md always loaded 进 system prompt，提供所有记忆的概览索引。
// 约束：MAX_ENTRYPOINT_LINES=200, MAX_ENTRYPOINT_BYTES=25000。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/util"
)

const (
	// entrypointName 索引文件名（对标同类 CLI memdir.ts ENTRYPOINT_NAME）。
	entrypointName = "MEMORY.md"
	// MaxEntrypointLines 索引文件最大行数（对标同类 CLI MAX_ENTRYPOINT_LINES）。
	MaxEntrypointLines = 200
	// MaxEntrypointBytes 索引文件最大字节数（对标同类 CLI MAX_ENTRYPOINT_BYTES）。
	MaxEntrypointBytes = 25000
)

// MemoryIndex 维护 MEMORY.md 入口文件，提供所有记忆的概览索引。
// always loaded 进 system prompt，AI 据此决定是否需通过 /memory show 查看详情。
type MemoryIndex struct {
	store MemorySource
}

// NewMemoryIndex 创建记忆索引实例。
// store 为读路径数据源（P2 起为接口）：Dir 决定 MEMORY.md 位置，List 用于重建索引。
func NewMemoryIndex(store MemorySource) *MemoryIndex {
	return &MemoryIndex{store: store}
}

// entrypointPath 返回 MEMORY.md 完整路径（{store.Dir()}/MEMORY.md）。
func (idx *MemoryIndex) entrypointPath() string {
	return filepath.Join(idx.store.Dir(), entrypointName)
}

// LoadEntrypoint 加载 MEMORY.md 内容（always loaded，注入 system prompt）。
// 若文件不存在则调 RebuildEntrypoint 重建。超过 MaxEntrypointBytes 时按行截断。
func (idx *MemoryIndex) LoadEntrypoint() (string, error) {
	path := idx.entrypointPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 不存在则重建
			if rbErr := idx.RebuildEntrypoint(); rbErr != nil {
				return "", ai.WithID(fmt.Errorf("重建 MEMORY.md 失败: %w", rbErr), ai.ErrMemoryIndex)
			}
			data, err = os.ReadFile(path)
			if err != nil {
				return "", ai.WithID(fmt.Errorf("读取 MEMORY.md 失败: %w", err), ai.ErrMemoryIndex)
			}
		} else {
			return "", ai.WithID(fmt.Errorf("读取 MEMORY.md 失败: %w", err), ai.ErrMemoryIndex)
		}
	}

	content := string(data)
	// 超过 MaxEntrypointBytes 时按行截断（对标同类 CLI truncateEntrypointContent）
	if len(content) > MaxEntrypointBytes {
		content = truncateEntrypointBytes(content, MaxEntrypointBytes)
	}
	return content, nil
}

// RebuildEntrypoint 扫描所有 .md 记忆文件，重建 MEMORY.md 索引。
// 格式：按 type 分组（user/feedback/project/reference），每行 "- [{type}] {name}: {content首行}"。
// 超过 MaxEntrypointLines 时截断并追加 "\n... (truncated, use /memory list to see all)"。
func (idx *MemoryIndex) RebuildEntrypoint() error {
	list, err := idx.store.List()
	if err != nil {
		return ai.WithID(fmt.Errorf("列出记忆失败: %w", err), ai.ErrMemoryIndex)
	}

	// 按 type 分组
	groups := map[MemoryType][]MemoryEntryV2{}
	for _, e := range list {
		groups[e.Type] = append(groups[e.Type], e)
	}

	// 按 type 顺序输出：user → feedback → project → reference
	typeOrder := []MemoryType{MemoryTypeUser, MemoryTypeFeedback, MemoryTypeProject, MemoryTypeReference}

	var sb strings.Builder
	sb.WriteString("# Memory Index\n\n")
	sb.WriteString("以下为当前项目所有记忆的概览索引。使用 `/memory show <name>` 查看完整内容。\n\n")

	lineCount := 4 // 已写入 4 行（标题 + 空行 + 说明 + 空行）
	truncated := false

	for _, t := range typeOrder {
		entries := groups[t]
		if len(entries) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("## %s\n\n", memoryTypeLabel(t)))
		lineCount += 2
		for _, e := range entries {
			// 预留 2 行余量：1 行组结束空行 + 1 行 truncated 消息
			if lineCount >= MaxEntrypointLines-2 {
				truncated = true
				break
			}
			// 取 content 首行作为预览
			preview := firstLine(e.Content)
			if preview == "" {
				preview = e.Description
			}
			if len(preview) > 80 {
				preview = preview[:80] + "..."
			}
			sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n", t, e.Name, preview))
			lineCount++
		}
		sb.WriteString("\n")
		lineCount++
		if truncated {
			break
		}
	}

	if truncated {
		// 不再追加开头 \n（前面已有组结束空行），避免多出空行导致超过 MaxEntrypointLines
		sb.WriteString("... (truncated, use /memory list to see all)\n")
	}

	return util.AtomicWriteFile(idx.entrypointPath(), []byte(sb.String()), 0600)
}

// memoryTypeLabel 返回记忆类型的中文标签。
func memoryTypeLabel(t MemoryType) string {
	switch t {
	case MemoryTypeUser:
		return "用户偏好 (user)"
	case MemoryTypeFeedback:
		return "用户反馈 (feedback)"
	case MemoryTypeProject:
		return "项目相关 (project)"
	case MemoryTypeReference:
		return "参考资料 (reference)"
	default:
		return string(t)
	}
}

// firstLine 返回字符串的首行（去除空白）。
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return s
}

// truncateEntrypointBytes 按字节截断索引内容，在新行边界结束（对标同类 CLI truncateEntrypointContent）。
// 避免截断到多字节字符中间导致乱码。
func truncateEntrypointBytes(content string, maxBytes int) string {
	if len(content) <= maxBytes {
		return content
	}
	// 在 maxBytes 范围内找最后一个换行符，避免截断到行中间
	cut := maxBytes
	for cut > 0 && content[cut] != '\n' {
		cut--
	}
	if cut == 0 {
		// 找不到换行，硬截断
		cut = maxBytes
	}
	return content[:cut] + "\n... (truncated due to size limit)\n"
}
