// Package config — memory_migrate.go V1→V2 迁移（N12 P0-1，）
//
// MigrateV1ToV2 将 V1 memory.json（JSONL 格式）的 key-value 迁移为 V2 .md 文件。
// 迁移幂等：目标 .md 已存在则跳过。迁移后 memory.json 重命名为 memory.json.migrated。
package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// MigrateV1ToV2 将 V1 memory.json（JSONL 格式）的 key-value 迁移为 V2 .md 文件。
// V1 格式：JSONL，每行一个 {"key":"...","value":"..."}。
// 迁移规则：
// - Name = sanitizeMemoryFilename(key)（小写/连字符/无特殊字符），文件名 = Name + ".md"
// - frontmatter: type=reference, description="migrated from V1", createdAt/updatedAt=迁移时间
// - Content = value（原 V1 value 直接作为 Markdown body）
//
// 幂等：目标 .md 已存在则跳过（不覆盖用户手动编辑的记忆）。
// 迁移完成后将 memory.json 重命名为 memory.json.migrated（不删除，留作备份）。
// 返回迁移条目数。
//
// 拆分为 migrateV1Entries（读取+写入，defer Close）+ MigrateV1ToV2（编排+rename），
// 修复 Windows 下 os.Rename 文件句柄占用问题（原实现 defer f.Close() 延迟到函数返回，
// 导致 os.Rename 在文件仍打开时执行，Windows 拒绝重命名被打开的文件）。
func MigrateV1ToV2(v1Path string, store *MemoryStore) (migrated int, err error) {
	// 阶段1：读取+写入（migrateV1Entries 内 defer Close 确保文件关闭后返回）
	migrated, err = migrateV1Entries(v1Path, store)
	if err != nil {
		return migrated, err
	}

	// 阶段2：重命名 memory.json → memory.json.migrated（留作备份）
	// 此时文件已 Close（migrateV1Entries 返回时 defer 触发），可安全 Rename
	if renameErr := os.Rename(v1Path, v1Path+".migrated"); renameErr != nil {
		log.Printf("[memory] 重命名 memory.json 失败（迁移已完成但备份未生成）: %v", renameErr)
		return migrated, ai.WithID(fmt.Errorf("重命名 memory.json 失败: %w", renameErr), ai.ErrMemoryMigrate)
	}

	return migrated, nil
}

// migrateV1Entries 读取 V1 memory.json 并写入 V2 .md 文件（私有函数）。
// 函数返回时 defer f.Close() 自动关闭文件，确保调用方 os.Rename 时文件已释放。
// 保留 index 局部变量逻辑：三种路径（跳过已存在/写入失败/写入成功）均 index++，仅成功时 migrated++。
func migrateV1Entries(v1Path string, store *MemoryStore) (migrated int, err error) {
	f, err := os.Open(v1Path)
	if err != nil {
		return 0, ai.WithID(fmt.Errorf("打开 V1 memory.json 失败: %w", err), ai.ErrMemoryMigrate)
	}
	defer f.Close() // 函数返回时关闭，确保后续 os.Rename 可重命名

	scanner := bufio.NewScanner(f)
	// V1 单条可能较大，扩大 buffer 至 10MB（对标 memory.go:57-58 load()）
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	index := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			log.Printf("[memory] V1 迁移跳过无法解析的行: %v", err)
			continue
		}
		if entry.Key == "" {
			continue
		}

		// 生成文件名：sanitize key，空则用 untitled-{index}
		name := sanitizeMemoryFilename(entry.Key)
		if name == "" || name == "untitled" {
			name = fmt.Sprintf("untitled-%d", index)
		}

		// 幂等检查：目标文件已存在则跳过
		targetPath := store.FilePath(name)
		if _, statErr := os.Stat(targetPath); statErr == nil {
			log.Printf("[memory] V1 迁移跳过已存在: %q → %q.md", entry.Key, name)
			index++
			continue
		}

		// 构建 V2 entry 并写入
		v2Entry := MemoryEntryV2{
			Name:        name,
			Type:        MemoryTypeReference,
			Description: "migrated from V1",
			Content:     entry.Value,
		}
		if writeErr := store.Write(v2Entry); writeErr != nil {
			log.Printf("[memory] V1 迁移写入失败 %q: %v", entry.Key, writeErr)
			index++
			continue
		}
		log.Printf("[memory] migrated: %q → %q.md", entry.Key, name)
		migrated++
		index++
	}

	if scanErr := scanner.Err(); scanErr != nil {
		return migrated, ai.WithID(fmt.Errorf("扫描 V1 memory.json 失败: %w", scanErr), ai.ErrMemoryMigrate)
	}

	return migrated, nil
}
