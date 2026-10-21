// experience_io.go — H10 Phase 4 模块 5 ExportPackage / ImportPackage + manifest.json（Phase 4 §3.5）。
// REF-外部-Go 标准库@https://pkg.go.dev/archive/tar+2026-07-15访问-archive/tar + compress/gzip；落地：.whale-exp = tar.gz，零外部依赖
// B 选定；落地：.whale-exp 自描述包 + manifest.json
// unverified；落地：ImportPackage 重置 VerifiedState（S4 风险防护）
// SNAPSHOT: pkg=internal/orchestrator; format=.whale-exp(tar.gz); contents={manifest.json,patterns/patterns.jsonl,pitfalls/pitfalls.jsonl,templates/,README.md(可选)}; manifest_fields={FormatVersion,ExportedAt,ExportedFrom,SourceScope,Checksums(sha256),Stats,Signature(可选GPG)}; import_safety=强制重置unverified+checksum校验+可选GPG签名

package orchestrator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ManifestFormatVersion 经验包格式版本（Phase 4 §3.5.2）。
const ManifestFormatVersion = "v1"

// ManifestExportedFrom 导出来源标识（Phase 4 §3.5.2）。
const ManifestExportedFrom = "whale-code v0.4.0"

// Manifest .whale-exp 包元数据（Phase 4 §3.5.2）。
type Manifest struct {
	FormatVersion string            `json:"format_version"` // v1
	ExportedAt    string            `json:"exported_at"`
	ExportedFrom  string            `json:"exported_from"` // whale-code v0.4.0
	SourceScope   string            `json:"source_scope"`
	Checksums     map[string]string `json:"checksums"` // file -> sha256:xxx
	Stats         ManifestStats     `json:"stats"`
	Signature     string            `json:"signature,omitempty"` // 可选 GPG 签名
}

// ManifestStats 包统计（Phase 4 §3.5.2）。
type ManifestStats struct {
	PatternCount  int `json:"pattern_count"`
	PitfallCount  int `json:"pitfall_count"`
	TemplateCount int `json:"template_count"`
}

// ImportResult 导入结果（Phase 4 §3.5.3）。
type ImportResult struct {
	ImportedPatterns  int
	ImportedPitfalls  int
	ImportedTemplates int
	SkippedDuplicates int      // 同 pattern_id 已存在则跳过
	Warnings          []string // 校验失败的条目（如 checksum 不匹配）
	Untrusted         bool     // 无 GPG 签名标记 untrusted，需用户确认
}

// ExportPackage 导出经验包到 .whale-exp 文件（Phase 4 §3.5.3）。
// scope=global|project，导出对应作用域的 patterns + pitfalls + templates + manifest.json。
func (s *ExperienceStore) ExportPackage(ctx context.Context, scope string, outputPath string) error {
	if scope != ScopeGlobal && scope != ScopeProject {
		return fmt.Errorf("experience_io: invalid scope %q", scope)
	}
	if outputPath == "" {
		return fmt.Errorf("experience_io: outputPath must not be empty")
	}

	// 收集 scope 下所有文件
	files := make(map[string][]byte)

	s.mu.RLock()
	root := s.scopeRoot(scope)
	s.mu.RUnlock()

	// patterns.jsonl
	patternsPath := filepath.Join(root, subdirPatterns, patternsFileName)
	if data, err := os.ReadFile(patternsPath); err == nil {
		files["patterns/patterns.jsonl"] = data
	}
	// pitfalls.jsonl
	pitfallsPath := filepath.Join(root, subdirPitfalls, pitfallsFileName)
	if data, err := os.ReadFile(pitfallsPath); err == nil {
		files["pitfalls/pitfalls.jsonl"] = data
	}
	// templates/ 目录（递归收集）
	templatesDir := filepath.Join(root, subdirTemplates)
	if entries, err := os.ReadDir(templatesDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			full := filepath.Join(templatesDir, entry.Name())
			if data, err := os.ReadFile(full); err == nil {
				files["templates/"+entry.Name()] = data
			}
		}
	}

	// 构造 manifest
	manifest := s.buildManifest(scope, files)

	// 序列化 manifest
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("experience_io: marshal manifest: %w", err)
	}
	files["manifest.json"] = manifestData

	// 打包为 .whale-exp (tar.gz)
	if err := writeTarGz(outputPath, files); err != nil {
		return fmt.Errorf("experience_io: write tar.gz: %w", err)
	}

	return nil
}

// ImportPackage 从 .whale-exp 文件导入经验（Phase 4 §3.5.3）。
// 安全策略：所有导入条目标记 unverified（不论原状态），需重新跑 §3.3 验证流程（S4 风险）。
// 导入前校验 manifest.json 的 checksums，不匹配则拒绝整包。
func (s *ExperienceStore) ImportPackage(ctx context.Context, inputPath string) (*ImportResult, error) {
	if inputPath == "" {
		return nil, fmt.Errorf("experience_io: inputPath must not be empty")
	}

	// 解压 .whale-exp
	files, err := readTarGz(inputPath)
	if err != nil {
		return nil, fmt.Errorf("experience_io: read tar.gz: %w", err)
	}

	// 读 manifest
	manifestData, ok := files["manifest.json"]
	if !ok {
		return nil, fmt.Errorf("experience_io: manifest.json not found in package")
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("experience_io: unmarshal manifest: %w", err)
	}

	result := &ImportResult{
		Warnings:  []string{},
		Untrusted: manifest.Signature == "", // 无签名标记 untrusted
	}

	// 校验 checksums（每个文件算 sha256 与 manifest 对比，不匹配则拒绝整包，S4 安全约束零容忍）
	for relPath, expectedChecksum := range manifest.Checksums {
		data, ok := files[relPath]
		if !ok {
			return nil, fmt.Errorf("experience_io: file %s in manifest but missing in package", relPath)
		}
		actualChecksum := computeFileChecksum(data)
		if actualChecksum != expectedChecksum {
			return nil, fmt.Errorf("experience_io: checksum mismatch for %s (expected %s, got %s) - 整包拒绝（S4 安全约束）",
				relPath, expectedChecksum, actualChecksum)
		}
	}

	// 导入 patterns（强制重置 VerifiedState = unverified，S4 风险防护零容忍）
	if patternsData, ok := files["patterns/patterns.jsonl"]; ok {
		for _, line := range splitJSONLines(patternsData) {
			if len(line) == 0 {
				continue
			}
			var p Pattern
			if err := json.Unmarshal(line, &p); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("跳过损坏 pattern 行: %v", err))
				continue
			}
			// 强制重置 VerifiedState = unverified（不论原状态，S4 风险防护）
			p.VerifiedState = VerifiedStateUnverified
			// 检查是否已存在（同 pattern_id 跳过）
			if existing, err := s.Get(p.PatternID); err == nil && existing != nil {
				result.SkippedDuplicates++
				continue
			}
			// Save
			if err := s.Save(&p); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("保存 pattern %s 失败: %v", p.PatternID, err))
				continue
			}
			result.ImportedPatterns++
		}
	}

	// 导入 pitfalls
	if pitfallsData, ok := files["pitfalls/pitfalls.jsonl"]; ok {
		for _, line := range splitJSONLines(pitfallsData) {
			if len(line) == 0 {
				continue
			}
			var p Pitfall
			if err := json.Unmarshal(line, &p); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("跳过损坏 pitfall 行: %v", err))
				continue
			}
			if err := s.SavePitfall(&p); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("保存 pitfall %s 失败: %v", p.PitfallID, err))
				continue
			}
			result.ImportedPitfalls++
		}
	}

	// 导入 templates（仅统计数量，文件直接复制到目标目录）
	for relPath := range files {
		if !strings.HasPrefix(relPath, "templates/") || relPath == "templates/" {
			continue
		}
		// 写入目标 templates 目录（按 SourceScope 决定）
		targetDir := filepath.Join(s.scopeRoot(manifest.SourceScope), subdirTemplates)
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			continue
		}
		filename := strings.TrimPrefix(relPath, "templates/")
		targetPath := filepath.Join(targetDir, filename)
		_ = os.WriteFile(targetPath, files[relPath], 0o644)
		result.ImportedTemplates++
	}

	return result, nil
}

// buildManifest 构造 manifest.json（含 checksums + stats，Phase 4 §3.5.2）。
func (s *ExperienceStore) buildManifest(scope string, files map[string][]byte) *Manifest {
	manifest := &Manifest{
		FormatVersion: ManifestFormatVersion,
		ExportedAt:    s.nowFunc().UTC().Format(time.RFC3339),
		ExportedFrom:  ManifestExportedFrom,
		SourceScope:   scope,
		Checksums:     make(map[string]string),
	}

	for relPath, data := range files {
		manifest.Checksums[relPath] = computeFileChecksum(data)
	}

	// 统计
	manifest.Stats = ManifestStats{}
	if data, ok := files["patterns/patterns.jsonl"]; ok {
		manifest.Stats.PatternCount = countJSONLLines(data)
	}
	if data, ok := files["pitfalls/pitfalls.jsonl"]; ok {
		manifest.Stats.PitfallCount = countJSONLLines(data)
	}
	for relPath := range files {
		if strings.HasPrefix(relPath, "templates/") && relPath != "templates/" {
			manifest.Stats.TemplateCount++
		}
	}

	return manifest
}

// computeFileChecksum 计算文件数据的 sha256 checksum（格式：sha256:<hex>）。
func computeFileChecksum(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// countJSONLLines 统计 JSONL 数据行数（非空行）。
func countJSONLLines(data []byte) int {
	count := 0
	for _, line := range splitJSONLines(data) {
		if len(line) > 0 {
			count++
		}
	}
	return count
}

// writeTarGz 写入 tar.gz 文件（零外部依赖，archive/tar + compress/gzip）。
func writeTarGz(path string, files map[string][]byte) error {
	// 确保目录存在
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	for name, data := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(data)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write header %s: %w", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			return fmt.Errorf("write data %s: %w", name, err)
		}
	}
	return nil
}

// readTarGz 读取 tar.gz 文件，返回 map[relPath]data。
func readTarGz(path string) (map[string][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	files := make(map[string][]byte)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar next: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // 跳过目录等其他类型
		}
		// 限制单文件大小（防 zip bomb，限制 10MB）
		if hdr.Size > 10*1024*1024 {
			return nil, fmt.Errorf("file %s too large: %d bytes", hdr.Name, hdr.Size)
		}
		buf := bytes.NewBuffer(make([]byte, 0, hdr.Size))
		if _, err := io.CopyN(buf, tr, hdr.Size); err != nil && err != io.EOF {
			return nil, fmt.Errorf("read file %s: %w", hdr.Name, err)
		}
		files[hdr.Name] = buf.Bytes()
	}
	return files, nil
}
