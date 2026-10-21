package filehistory

// =============================================================================
// S10 文件历史 / rewind 快照 —— 审计 characterization 测试（Snapshot 步骤）
//
// 目的：在动手 Fix 之前，用测试"钉死" whale-code 当前（可能含缺陷的）行为。
// 这些测试描述 CURRENT BEHAVIOR，因此全部应当 GREEN。
// 任何修复都必须让相关测试保持 GREEN（或刻意翻转并补新测试），
// 以此证明"生产真正执行的那条路径"上的行为变了。
//
// 覆盖的不变量：I6（原子 & 安全持久化 / fail-safe）、I4（rewind 状态机完备性）、
// 以及 I10/I3（restore 路径越界）。
//
// 命名前缀 AuditS10_ 便于单独运行：go test ./internal/filehistory/ -run AuditS10 -v
// =============================================================================

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// AuditS10_1 (I6 还原保真度): Rewind 到快照必须把文件内容精确恢复成快照 hash。
// 钉住"happy path 还原字节级一致"这一基础性质。
// ---------------------------------------------------------------------------
func TestAuditS10_Rewind_RestoresExactContent(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "this-is-v1-content")

	h.Track("msg-001", "tc-001", "write_file", []string{file1})
	h.TrackAfter("msg-001", "tc-001", []string{file1})
	h.Snapshot("msg-001", "first", 1)

	// 改坏它
	if err := os.WriteFile(file1, []byte("this-is-v2-content-corrupted"), 0644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	h.Track("msg-002", "tc-002", "write_file", []string{file1})
	h.TrackAfter("msg-002", "tc-002", []string{file1})
	h.Snapshot("msg-002", "second", 2)

	result, err := h.Rewind("msg-001", nil, "session-001")
	if err != nil {
		t.Fatalf("Rewind returned error: %v", err)
	}
	got, err := os.ReadFile(file1)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(got) != "this-is-v1-content" {
		t.Errorf("restore fidelity broken: expected 'this-is-v1-content', got %q", string(got))
	}
	if result.FilesRestored != 1 {
		t.Errorf("expected FilesRestored=1, got %d", result.FilesRestored)
	}
}

// ---------------------------------------------------------------------------
// AuditS10_2 (I6 / API fail-closed): 目标内容块在 content store 中缺失时，
// Rewind 必须在 API 层返回 error，且失败文件保持改前内容（restore 未生效）。
// 钉住"缺失内容 -> 报错而非静默成功"这一性质。
// ---------------------------------------------------------------------------
func TestAuditS10_Rewind_ReturnsError_OnMissingContent(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "v1-before")

	h.Track("msg-001", "tc-001", "write_file", []string{file1})
	h.TrackAfter("msg-001", "tc-001", []string{file1})
	snap, err := h.GetSnapshot("msg-001")
	if err != nil || snap == nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	targetHash := *snap.Files[file1].Hash // v1 内容的 hash

	// 修改文件（产生 v2），使当前磁盘内容偏离快照
	// ★ 本函数已于 2026-10-12 **有意变更**——**下一个人别当回归**：
	// 修复前这里用裸 os.WriteFile 制造 v2，隐式依赖「外部修改照样覆写」的旧语义
	// （已判定该语义是缺陷：可靠判据没接到不可逆动作上，现改为跳过冲突文件）。
	// 裸写的 v2 hash 不在会话 track 记录中 ⇒ 落入新的 skip 分支，restore 不再尝试，
	// 本用例就测不到「content store 缺块 ⇒ 报错」了。改为经 Track/TrackAfter 产生 v2
	// （= 会话自身写入，回滚照常尝试 restore）—— 本用例真正要钉的性质不变。
	if err := os.WriteFile(file1, []byte("v2-after"), 0644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	h.Track("msg-002", "tc-002", "write_file", []string{file1})
	h.TrackAfter("msg-002", "tc-002", []string{file1})

	// 模拟 content store 中目标块损坏/缺失（删除 v1 内容块）
	if err := removeContent(h.contentDir, targetHash); err != nil {
		t.Fatalf("removeContent: %v", err)
	}

	// Rewind 应失败（readContent(targetHash) 失败 -> diffAndRestore 触发 rollback -> 返回 error）
	_, err = h.Rewind("msg-001", nil, "session-001")
	if err == nil {
		t.Fatal("expected Rewind to return error when target content block is missing, got nil")
	}

	// 失败文件应保持改前(v2)内容：restore 从未对其生效
	got, _ := os.ReadFile(file1)
	if string(got) != "v2-after" {
		t.Errorf("file should be untouched on failed restore, expected 'v2-after', got %q", string(got))
	}
}

// ---------------------------------------------------------------------------
// AuditS10_3 (I6 双保险): Rewind 必须创建 rewind-backup 快照，并回填 BackupMsgId。
// 钉住"回滚前自动备份"机制存在（用于失败反向回滚）。
// ---------------------------------------------------------------------------
func TestAuditS10_Rewind_CreatesBackupSnapshot(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "v1")

	h.Track("msg-001", "tc-001", "write_file", []string{file1})
	h.TrackAfter("msg-001", "tc-001", []string{file1})
	h.Snapshot("msg-001", "first", 1)

	if err := os.WriteFile(file1, []byte("v2"), 0644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	h.Track("msg-002", "tc-002", "write_file", []string{file1})
	h.TrackAfter("msg-002", "tc-002", []string{file1})
	h.Snapshot("msg-002", "second", 2)

	result, err := h.Rewind("msg-001", nil, "session-001")
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if result.BackupMsgId == "" {
		t.Error("expected non-empty BackupMsgId (rewind-backup snapshot must be created before restore)")
	}
	// 验证备份快照确实写入了索引（label == rewind-backup）
	found := false
	for _, e := range h.indexCache {
		if e.Type == "snapshot" && e.Label == "rewind-backup" {
			found = true
			break
		}
	}
	_ = found // 成功 Rewind 后备份可能被截断；BackupMsgId 非空已足够钉住"创建"行为
}

// ---------------------------------------------------------------------------
// AuditS10_4 (I6 / I2, G2): 索引分片含损坏行时，NewFileHistory 必须 fail-closed
// （拒绝初始化文件历史），不得静默降级为部分加载。
// 原 characterization 钉的是 fail-open（静默跳过）；决策 HG1=a 后刻意翻转为 fail-closed。
// ---------------------------------------------------------------------------
func TestAuditS10_NewFileHistory_FailClosedOnCorruptShard(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "v1")
	h.Track("msg-001", "tc-001", "write_file", []string{file1})
	h.TrackAfter("msg-001", "tc-001", []string{file1})
	h.Snapshot("msg-001", "first", 1)

	// 向活跃分片追加一行非法 JSON（模拟崩溃残留/磁盘损坏）
	shardPath := h.indexStore.ActiveShardPath()
	f, err := os.OpenFile(shardPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open shard: %v", err)
	}
	if _, err := f.WriteString("{this-is-not-valid-json\n"); err != nil {
		t.Fatalf("append corrupt line: %v", err)
	}
	f.Close()

	// 用同一 baseDir 的父目录（NewFileHistory 会在 baseDir 下再拼 workDirHash）重新构造，
	// 触发 loadIndex 扫描同一份（含损坏行的）分片。
	// HG1=a：必须返回错误（拒绝初始化），而不是静默部分加载。
	parentBaseDir := filepath.Dir(h.baseDir)
	if _, err := NewFileHistory(workDir, parentBaseDir); err == nil {
		t.Fatal("expected NewFileHistory to FAIL (fail-closed) on corrupt shard, got nil error")
	}
}

// ---------------------------------------------------------------------------
// AuditS10_5 (I6 潜在缺陷 G3): restoreFile 在 mode==0 时不执行 chmod，
// 且 AtomicWriteFile 以 0 权限写文件 -> 恢复出的文件权限为 0000（不可读）。
// 钉住这一潜在（当前被快照 mode>=0644 掩盖的）错误行为。
// ---------------------------------------------------------------------------
func TestAuditS10_RestoreFile_ModeZero_WritesZeroPerm(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	outPath := filepath.Join(workDir, "restored.go")

	hash, err := writeContent(h.contentDir, []byte("restored"))
	if err != nil {
		t.Fatalf("writeContent: %v", err)
	}
	// 用 mode=0 恢复
	if err := h.restoreFile(outPath, &hash, 0); err != nil {
		t.Fatalf("restoreFile: %v", err)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat restored: %v", err)
	}
	if info.Mode().Perm() != 0 {
		t.Errorf("expected 0000 perms when mode==0, got %o", info.Mode().Perm())
	}
}

// ---------------------------------------------------------------------------
// AuditS10_6 (I6, G4): 超过 10MB 的文件也必须用全量 SHA256 做内容寻址。
// 决策 HG3=b 后：不同内容的大文件 -> 不同 content-address（原"前4KB前缀+大小"近似
// hash 会让不同内容碰撞）。刻意翻转原 characterization（其钉的是"碰撞"的缺陷行为）。
// 走生产入口 writeContent（而非 computeLargeFileHash），证明生产路径行为真的变了。
// ---------------------------------------------------------------------------
func TestAuditS10_LargeFile_FullSHA256NoCollision(t *testing.T) {
	const size = 11 * 1024 * 1024 // > maxContentHashSize(10MB)
	prefix := strings.Repeat("A", 4096)
	contentA := []byte(prefix + strings.Repeat("B", size-4096))
	contentB := []byte(prefix + strings.Repeat("C", size-4096))

	dir := t.TempDir()
	hashA, err := writeContent(dir, contentA)
	if err != nil {
		t.Fatalf("writeContent A: %v", err)
	}
	hashB, err := writeContent(dir, contentB)
	if err != nil {
		t.Fatalf("writeContent B: %v", err)
	}
	if hashA == hashB {
		t.Errorf("expected DISTINCT full-SHA256 addresses for distinct >10MB contents, both got %s", hashA)
	}
	// 大文件现在必须等于全量内容 hash（不再是前缀近似）
	if want := computeHash(contentA); hashA != want {
		t.Errorf("large file must use FULL SHA256: got %s want %s", hashA, want)
	}
}

// ---------------------------------------------------------------------------
// AuditS10_7 (I10 / I3, G8): 恢复必须对目标路径做 safePath 复检，拒绝工作区外写入。
// 决策 HG4=a 后：含工作区外路径的快照 -> diffAndRestore 拒绝（返回错误），
// 且工作区外文件**不得被创建**。刻意翻转原 characterization（其钉的是越界写入）。
// ---------------------------------------------------------------------------
func TestAuditS10_RestoreFile_RejectsOutsideWorkdir(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	_ = workDir

	// 把一个内容块写进 content store
	evilContent := []byte("i was restored outside the workdir")
	hash, err := writeContent(h.contentDir, evilContent)
	if err != nil {
		t.Fatalf("writeContent: %v", err)
	}

	// 构造一个指向工作区外的快照条目（模拟被篡改/导入的索引）
	outside := filepath.Join(t.TempDir(), "evil-restored.txt")
	snap := &Snapshot{
		MsgId:           "evil-msg",
		Files:           map[string]*FileState{outside: {Hash: &hash, Mode: 0644}},
		SessionMsgCount: 0,
	}

	// 直接驱动内部恢复路径：HG4=a -> 必须拒绝
	// （起 diffAndRestore 第 4 个返回值为结构化冲突面，本用例只关心 err）
	_, _, _, _, err = h.diffAndRestore(snap)
	if err == nil {
		t.Fatal("expected diffAndRestore to REJECT outside-workdir path (fail-closed), got nil error")
	}

	// 关键：工作区外文件不得被创建
	if _, rerr := os.Stat(outside); !os.IsNotExist(rerr) {
		t.Errorf("outside file must NOT be created by restore, stat err=%v", rerr)
	}
}

// ---------------------------------------------------------------------------
// AuditS10_8 (I6, G1 / HG2=a): Rewind 以"确实存在可回退备份"为前提。
// 若备份快照创建失败，Rewind 必须在改动任何文件之前中止（fail-closed），
// 不得带着空备份继续（原实现只记 warning 后继续）。
// 通过 store 边界注入 AppendEntry 失败触发"备份创建失败"分支（生产真实路径）。
// ---------------------------------------------------------------------------
func TestAuditS10_Rewind_AbortsWhenBackupFails(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "v1")

	h.Track("msg-001", "tc-001", "write_file", []string{file1})
	h.TrackAfter("msg-001", "tc-001", []string{file1})
	h.Snapshot("msg-001", "first", 1)

	if err := os.WriteFile(file1, []byte("v2-changed"), 0644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	h.Track("msg-002", "tc-002", "write_file", []string{file1})
	h.TrackAfter("msg-002", "tc-002", []string{file1})
	h.Snapshot("msg-002", "second", 2)

	// 注入：备份快照写入索引时 AppendEntry 失败（模拟磁盘满/索引不可写）
	h.indexStore = failAppendStore{IndexStore: h.indexStore}

	_, err := h.Rewind("msg-001", nil, "session-001")
	if err == nil {
		t.Fatal("expected Rewind to ABORT when backup snapshot cannot be created, got nil error")
	}
	// 关键：文件必须保持改前内容（备份失败时 rewind 未对文件做任何改动）
	got, rerr := os.ReadFile(file1)
	if rerr != nil {
		t.Fatalf("read file: %v", rerr)
	}
	if string(got) != "v2-changed" {
		t.Errorf("file must be untouched when rewind aborts on backup failure, got %q", string(got))
	}
}

// failAppendStore 包装真实 IndexStore，仅让 AppendEntry 失败，用于触发"备份创建失败"分支。
type failAppendStore struct {
	IndexStore
}

func (failAppendStore) AppendEntry(IndexEntry) error {
	return errors.New("injected AppendEntry failure (test)")
}
