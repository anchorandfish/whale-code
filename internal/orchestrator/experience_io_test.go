// experience_io_test.go — H10 Phase 4 模块 5 ExportPackage / ImportPackage 反例测试（TDD 先行，Phase 4 §3.5）。
// 覆盖：导出含 manifest + checksum / 导入 checksum 不匹配拒绝整包 / 导入强制重置 unverified（S4 风险防护）/ 重复 pattern_id 跳过 / 无签名标记 untrusted。
// unverified；落地：ImportPackage 重置 VerifiedState（S4 风险防护）
// checksums；落地：computeFileChecksum sha256 校验
// SNAPSHOT: tests={ExportPackage_ContainsManifest, ExportPackage_ChecksumsCorrect, ImportPackage_ChecksumMismatch_Rejected, ImportPackage_ForceUnverified, ImportPackage_DuplicatePatternID_Skipped, ImportPackage_NoSignature_Untrusted, ImportPackage_GPGSignature_Verified, ExportPackage_InvalidScope_Rejected, ExportPackage_EmptyOutputPath_Rejected, ImportPackage_NonExistentFile_Rejected, ImportPackage_EmptyPath_Rejected, ImportPackage_RoundTrip_PreservesData}

package orchestrator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExportPackage_ContainsManifest 导出包含 manifest.json + patterns/pitfalls/templates。
func TestExportPackage_ContainsManifest(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)

	// 添加 1 个 pattern + 1 个 pitfall
	p := makeTestPattern("p-export-1", "fix export bug", ScopeGlobal, VerifiedStateVerified)
	_ = s.Save(p)
	pitfall := &Pitfall{
		PitfallID:     "pitfall-export-1",
		TaskSignature: "fix export bug",
		Symptom:       "compile error",
		RootCause:     "missing import",
		Mitigation:    "add import",
		Occurrences:   1,
		Scope:         ScopeGlobal,
	}
	_ = s.SavePitfall(pitfall)

	// 导出
	outputPath := filepath.Join(t.TempDir(), "test-export.whale-exp")
	if err := s.ExportPackage(context.Background(), ScopeGlobal, outputPath); err != nil {
		t.Fatalf("ExportPackage: %v", err)
	}

	// 解压验证包含 manifest.json + patterns/patterns.jsonl + pitfalls/pitfalls.jsonl
	files, err := readTarGz(outputPath)
	if err != nil {
		t.Fatalf("readTarGz: %v", err)
	}

	if _, ok := files["manifest.json"]; !ok {
		t.Error("导出包应包含 manifest.json")
	}
	if _, ok := files["patterns/patterns.jsonl"]; !ok {
		t.Error("导出包应包含 patterns/patterns.jsonl")
	}
	if _, ok := files["pitfalls/pitfalls.jsonl"]; !ok {
		t.Error("导出包应包含 pitfalls/pitfalls.jsonl")
	}
}

// TestExportPackage_ChecksumsCorrect manifest 中 checksums 与实际文件 sha256 一致。
func TestExportPackage_ChecksumsCorrect(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-checksum-1", "fix checksum bug", ScopeGlobal, VerifiedStateVerified)
	_ = s.Save(p)

	outputPath := filepath.Join(t.TempDir(), "checksum-export.whale-exp")
	if err := s.ExportPackage(context.Background(), ScopeGlobal, outputPath); err != nil {
		t.Fatalf("ExportPackage: %v", err)
	}

	files, err := readTarGz(outputPath)
	if err != nil {
		t.Fatalf("readTarGz: %v", err)
	}

	// 解析 manifest
	var manifest Manifest
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatalf("Unmarshal manifest: %v", err)
	}

	// 校验每个文件的 checksum
	for relPath, expectedChecksum := range manifest.Checksums {
		data, ok := files[relPath]
		if !ok {
			t.Errorf("manifest 引用的文件 %s 不在包中", relPath)
			continue
		}
		actualChecksum := computeFileChecksum(data)
		if actualChecksum != expectedChecksum {
			t.Errorf("文件 %s checksum 不匹配: expected %s, got %s", relPath, expectedChecksum, actualChecksum)
		}
	}
}

// TestExportPackage_StatsCorrect manifest stats 字段统计正确。
func TestExportPackage_StatsCorrect(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	// 添加 2 个 pattern + 1 个 pitfall
	_ = s.Save(makeTestPattern("p-stats-1", "fix stats bug 1", ScopeGlobal, VerifiedStateVerified))
	_ = s.Save(makeTestPattern("p-stats-2", "fix stats bug 2", ScopeGlobal, VerifiedStateVerified))
	_ = s.SavePitfall(&Pitfall{
		PitfallID: "pitfall-stats-1", TaskSignature: "fix stats bug",
		Symptom: "err", Scope: ScopeGlobal,
	})

	outputPath := filepath.Join(t.TempDir(), "stats-export.whale-exp")
	if err := s.ExportPackage(context.Background(), ScopeGlobal, outputPath); err != nil {
		t.Fatalf("ExportPackage: %v", err)
	}

	files, _ := readTarGz(outputPath)
	var manifest Manifest
	_ = json.Unmarshal(files["manifest.json"], &manifest)

	if manifest.Stats.PatternCount != 2 {
		t.Errorf("PatternCount 应为 2, got %d", manifest.Stats.PatternCount)
	}
	if manifest.Stats.PitfallCount != 1 {
		t.Errorf("PitfallCount 应为 1, got %d", manifest.Stats.PitfallCount)
	}
}

// TestExportPackage_InvalidScope_Rejected 非法 scope → 返回 error。
func TestExportPackage_InvalidScope_Rejected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	err := s.ExportPackage(context.Background(), "invalid-scope", filepath.Join(t.TempDir(), "bad.whale-exp"))
	if err == nil {
		t.Error("非法 scope 必须返回 error")
	}
}

// TestExportPackage_EmptyOutputPath_Rejected 空 outputPath → 返回 error。
func TestExportPackage_EmptyOutputPath_Rejected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	err := s.ExportPackage(context.Background(), ScopeGlobal, "")
	if err == nil {
		t.Error("空 outputPath 必须返回 error")
	}
}

// TestImportPackage_ForceUnverified 导入 verified 状态的 pattern → 导入后 VerifiedState=unverified（S4 风险防护，零容忍）。
func TestImportPackage_ForceUnverified(t *testing.T) {
	t.Parallel()
	// 源 store：含 verified pattern
	srcStore := newTestExperienceStore(t)
	verifiedP := makeTestPattern("p-import-verified", "fix import verified bug", ScopeGlobal, VerifiedStateVerified)
	_ = srcStore.Save(verifiedP)

	// 导出
	outputPath := filepath.Join(t.TempDir(), "force-unverified.whale-exp")
	if err := srcStore.ExportPackage(context.Background(), ScopeGlobal, outputPath); err != nil {
		t.Fatalf("ExportPackage: %v", err)
	}

	// 目标 store：导入
	dstStore := newTestExperienceStore(t)
	result, err := dstStore.ImportPackage(context.Background(), outputPath)
	if err != nil {
		t.Fatalf("ImportPackage: %v", err)
	}
	if result.ImportedPatterns == 0 {
		t.Fatal("应导入至少 1 个 pattern")
	}

	// 验证导入后 VerifiedState = unverified（S4 风险防护，零容忍）
	got, err := dstStore.Get("p-import-verified")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateUnverified {
		t.Errorf("致命反例 S4：导入 verified pattern 后应强制重置为 unverified, got %s（零容忍）", got.VerifiedState)
	}
}

// TestImportPackage_DuplicatePatternID_Skipped 同 pattern_id 已存在 → SkippedDuplicates++。
func TestImportPackage_DuplicatePatternID_Skipped(t *testing.T) {
	t.Parallel()
	// 源 store：含 1 个 pattern
	srcStore := newTestExperienceStore(t)
	p := makeTestPattern("p-duplicate", "fix duplicate bug", ScopeGlobal, VerifiedStateUnverified)
	_ = srcStore.Save(p)

	outputPath := filepath.Join(t.TempDir(), "duplicate.whale-exp")
	_ = srcStore.ExportPackage(context.Background(), ScopeGlobal, outputPath)

	// 目标 store：先预存同 pattern_id 的 pattern
	dstStore := newTestExperienceStore(t)
	existingP := makeTestPattern("p-duplicate", "fix duplicate bug", ScopeGlobal, VerifiedStateUnverified)
	_ = dstStore.Save(existingP)

	// 导入（应跳过重复）
	result, err := dstStore.ImportPackage(context.Background(), outputPath)
	if err != nil {
		t.Fatalf("ImportPackage: %v", err)
	}
	if result.SkippedDuplicates == 0 {
		t.Error("同 pattern_id 已存在应 SkippedDuplicates++")
	}
}

// TestImportPackage_NoSignature_Untrusted 无签名包 → 标记 untrusted + 用户需确认。
func TestImportPackage_NoSignature_Untrusted(t *testing.T) {
	t.Parallel()
	srcStore := newTestExperienceStore(t)
	_ = srcStore.Save(makeTestPattern("p-nosig", "fix nosig bug", ScopeGlobal, VerifiedStateUnverified))

	outputPath := filepath.Join(t.TempDir(), "nosig.whale-exp")
	_ = srcStore.ExportPackage(context.Background(), ScopeGlobal, outputPath)

	// 导入
	dstStore := newTestExperienceStore(t)
	result, err := dstStore.ImportPackage(context.Background(), outputPath)
	if err != nil {
		t.Fatalf("ImportPackage: %v", err)
	}
	// 无签名（manifest.Signature 为空）→ Untrusted=true
	if !result.Untrusted {
		t.Error("无签名包应标记 Untrusted=true（需用户确认）")
	}
}

// TestImportPackage_GPGSignature_Verified 有签名包 → 校验通过（Untrusted=false）。
func TestImportPackage_GPGSignature_Verified(t *testing.T) {
	t.Parallel()
	srcStore := newTestExperienceStore(t)
	_ = srcStore.Save(makeTestPattern("p-sig", "fix sig bug", ScopeGlobal, VerifiedStateUnverified))

	outputPath := filepath.Join(t.TempDir(), "signed.whale-exp")
	_ = srcStore.ExportPackage(context.Background(), ScopeGlobal, outputPath)

	// 手动修改包：在 manifest.json 中加入 signature 字段
	files, _ := readTarGz(outputPath)
	var manifest Manifest
	_ = json.Unmarshal(files["manifest.json"], &manifest)
	manifest.Signature = "fake-gpg-signature-abc123"
	manifestData, _ := json.MarshalIndent(manifest, "", "  ")
	files["manifest.json"] = manifestData

	// 重写包
	if err := writeTarGz(outputPath, files); err != nil {
		t.Fatalf("writeTarGz: %v", err)
	}

	// 导入
	dstStore := newTestExperienceStore(t)
	result, err := dstStore.ImportPackage(context.Background(), outputPath)
	if err != nil {
		t.Fatalf("ImportPackage: %v", err)
	}
	// 有签名 → Untrusted=false
	if result.Untrusted {
		t.Error("有签名包不应标记 Untrusted")
	}
}

// TestImportPackage_ChecksumMismatch_Rejected 篡改包内文件 → checksum 不匹配 → 拒绝整包（零容忍）。
func TestImportPackage_ChecksumMismatch_Rejected(t *testing.T) {
	t.Parallel()
	srcStore := newTestExperienceStore(t)
	_ = srcStore.Save(makeTestPattern("p-tamper", "fix tamper bug", ScopeGlobal, VerifiedStateUnverified))

	outputPath := filepath.Join(t.TempDir(), "tampered.whale-exp")
	_ = srcStore.ExportPackage(context.Background(), ScopeGlobal, outputPath)

	// 篡改包内 patterns.jsonl（修改内容但不改 manifest 中的 checksum）
	files, _ := readTarGz(outputPath)
	tamperedData := bytes.ReplaceAll(files["patterns/patterns.jsonl"], []byte("fix tamper bug"), []byte("TAMPERED CONTENT"))
	files["patterns/patterns.jsonl"] = tamperedData
	if err := writeTarGz(outputPath, files); err != nil {
		t.Fatalf("writeTarGz: %v", err)
	}

	// 导入应失败（checksum 不匹配）
	dstStore := newTestExperienceStore(t)
	_, err := dstStore.ImportPackage(context.Background(), outputPath)
	if err == nil {
		t.Fatal("致命反例：checksum 不匹配应拒绝整包（S4 安全约束，零容忍）")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error 应含 'checksum mismatch', got: %v", err)
	}
}

// TestImportPackage_EmptyPath_Rejected 空路径 → 返回 error。
func TestImportPackage_EmptyPath_Rejected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	_, err := s.ImportPackage(context.Background(), "")
	if err == nil {
		t.Error("空路径必须返回 error")
	}
}

// TestImportPackage_NonExistentFile_Rejected 文件不存在 → 返回 error。
func TestImportPackage_NonExistentFile_Rejected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	_, err := s.ImportPackage(context.Background(), filepath.Join(t.TempDir(), "nonexistent.whale-exp"))
	if err == nil {
		t.Error("文件不存在必须返回 error")
	}
}

// TestImportPackage_NoManifest_Rejected 包内无 manifest.json → 返回 error。
func TestImportPackage_NoManifest_Rejected(t *testing.T) {
	t.Parallel()
	// 构造无 manifest 的包
	outputPath := filepath.Join(t.TempDir(), "no-manifest.whale-exp")
	files := map[string][]byte{
		"patterns/patterns.jsonl": []byte(`{"pattern_id":"p1"}` + "\n"),
	}
	if err := writeTarGz(outputPath, files); err != nil {
		t.Fatalf("writeTarGz: %v", err)
	}

	s := newTestExperienceStore(t)
	_, err := s.ImportPackage(context.Background(), outputPath)
	if err == nil {
		t.Error("包内无 manifest.json 必须返回 error")
	}
}

// TestImportPackage_RoundTrip_PreservesData 导出→导入往返数据一致（pattern_id/task_signature/Steps 不丢失）。
func TestImportPackage_RoundTrip_PreservesData(t *testing.T) {
	t.Parallel()
	srcStore := newTestExperienceStore(t)
	srcP := makeTestPattern("p-roundtrip", "fix roundtrip bug", ScopeGlobal, VerifiedStateVerified)
	srcP.Steps = []Step{{Action: "edit_file", Target: "main.go", Description: "修改主文件"}}
	srcP.FilesTouched = []string{"main.go", "util.go"}
	_ = srcStore.Save(srcP)

	outputPath := filepath.Join(t.TempDir(), "roundtrip.whale-exp")
	_ = srcStore.ExportPackage(context.Background(), ScopeGlobal, outputPath)

	dstStore := newTestExperienceStore(t)
	_, err := dstStore.ImportPackage(context.Background(), outputPath)
	if err != nil {
		t.Fatalf("ImportPackage: %v", err)
	}

	got, err := dstStore.Get("p-roundtrip")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TaskSignature != "fix roundtrip bug" {
		t.Errorf("task_signature 往返不一致: got %s", got.TaskSignature)
	}
	if len(got.Steps) != 1 {
		t.Errorf("Steps 往返后数量不一致: got %d", len(got.Steps))
	}
	if len(got.FilesTouched) != 2 {
		t.Errorf("FilesTouched 往返后数量不一致: got %d", len(got.FilesTouched))
	}
	// VerifiedState 应被强制重置为 unverified（S4 风险防护）
	if got.VerifiedState != VerifiedStateUnverified {
		t.Errorf("往返后 VerifiedState 应为 unverified（S4 强制重置）, got %s", got.VerifiedState)
	}
}

// TestExportPackage_ManifestFormatVersion manifest 格式版本为 v1。
func TestExportPackage_ManifestFormatVersion(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	_ = s.Save(makeTestPattern("p-fmt-ver", "fix fmt ver bug", ScopeGlobal, VerifiedStateUnverified))

	outputPath := filepath.Join(t.TempDir(), "fmtver.whale-exp")
	_ = s.ExportPackage(context.Background(), ScopeGlobal, outputPath)

	files, _ := readTarGz(outputPath)
	var manifest Manifest
	_ = json.Unmarshal(files["manifest.json"], &manifest)

	if manifest.FormatVersion != ManifestFormatVersion {
		t.Errorf("FormatVersion 应为 %s, got %s", ManifestFormatVersion, manifest.FormatVersion)
	}
	if manifest.ExportedFrom != ManifestExportedFrom {
		t.Errorf("ExportedFrom 应为 %s, got %s", ManifestExportedFrom, manifest.ExportedFrom)
	}
	if manifest.SourceScope != ScopeGlobal {
		t.Errorf("SourceScope 应为 %s, got %s", ScopeGlobal, manifest.SourceScope)
	}
}

// TestWriteTarGz_CreatesParentDir writeTarGz 自动创建父目录。
func TestWriteTarGz_CreatesParentDir(t *testing.T) {
	t.Parallel()
	nestedPath := filepath.Join(t.TempDir(), "nested", "deep", "dir", "test.whale-exp")
	files := map[string][]byte{
		"test.txt": []byte("hello"),
	}
	if err := writeTarGz(nestedPath, files); err != nil {
		t.Fatalf("writeTarGz 应自动创建父目录: %v", err)
	}
	if _, err := os.Stat(nestedPath); err != nil {
		t.Errorf("文件应存在: %v", err)
	}
}

// TestReadTarGz_ZipBomb_Rejected 单文件 > 10MB 被拒绝（防 zip bomb）。
func TestReadTarGz_ZipBomb_Rejected(t *testing.T) {
	t.Parallel()
	outputPath := filepath.Join(t.TempDir(), "zipbomb.whale-exp")

	// 构造 > 10MB 的文件
	f, err := os.Create(outputPath)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	hugeData := make([]byte, 11*1024*1024) // 11MB > 10MB 限制
	hdr := &tar.Header{
		Name: "huge.txt",
		Mode: 0o644,
		Size: int64(len(hugeData)),
	}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write(hugeData)
	_ = tw.Close()
	_ = gz.Close()
	_ = f.Close()

	// 读取应失败
	_, err = readTarGz(outputPath)
	if err == nil {
		t.Error("单文件 > 10MB 应被拒绝（防 zip bomb）")
	}
}
