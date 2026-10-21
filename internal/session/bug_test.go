package session

// 反例测试（Bug Reproduction Tests）
//
// 本文件是对标审查机制的核心组件。每个测试对应 docs/BENCHMARK_FACTS.md 中的一个事实条目。
//
// 测试特点：
// - 测试名以 TestBug_ 开头
// - 在当前（含 bug 的）代码上运行会 **失败**，证明 bug 存在
// - 修复 bug 后测试 **通过**，证明 bug 已修
//
// 使用方式：
//   go test ./internal/session/ -run TestBug_ -v
//
// 预期结果：当前代码下所有 TestBug_ 测试应失败（证明 bug 存在）
// 修复后：所有 TestBug_ 测试应通过（证明 bug 已修）

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestBug_AppendBatchNotAppendOnly 对应 FACT-SESS-001
// JSONL 必须 append-only，但 AppendBatch 读取整个文件 + 临时文件 + rename 全量重写
// FACT-SESS-001
func TestBug_AppendBatchNotAppendOnly(t *testing.T) {
	// FACT-SESS-001
	// Windows 上 os.SameFile 对 rename 检测不可靠（MoveFileEx 可能保留文件标识符）
	// 改用检测 .tmp 临时文件的方式验证全量重写行为
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "test-append-only"

	// 先用 Append 写入一条消息（append-only，使用 O_APPEND）
	if err := store.Append(sessionID, UserText("first message")); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	path := store.sessionPath(sessionID)
	tmpPath := path + ".tmp"

	// 预先创建一个 .tmp 文件，模拟 AppendBatch 的临时文件
	// 如果 AppendBatch 是 append-only（O_APPEND），它不会创建 .tmp 文件
	// 如果 AppendBatch 是全量重写，它会创建 .tmp 文件再 rename
	// 我们通过检查 AppendBatch 后 .tmp 文件是否被清理来推断它是否使用了临时文件
	// 但更直接的方法：检查 AppendBatch 是否读取了整个文件

	// 验证方法：创建一个大文件（1MB），AppendBatch 一条小消息
	// 如果是 append-only，文件大小 = 原大小 + 新消息大小
	// 如果是全量重写，文件大小 = 原大小 + 新消息大小（内容相同，无法区分大小）
	// 所以改用检测 .tmp 文件残留的方式

	// 方法：让 AppendBatch 在 rename 前失败，检查是否有 .tmp 文件
	// 通过将目标路径设为只读目录来阻止 rename（Unix）
	// Windows 上无法可靠阻止 rename，所以用代码检查方式

	// 最终方法：检查 AppendBatch 源码行为 —— 通过验证 .tmp 文件在 AppendBatch 后不存在
	// 但如果 AppendBatch 是 append-only，.tmp 文件也不会存在
	// 所以这个方法不能区分

	// 最可靠的方法：检查 AppendBatch 是否使用了 os.ReadFile
	// 通过创建一个文件，然后检查 AppendBatch 是否读取了它
	// 如果 AppendBatch 读取了整个文件，我们可以通过检查内存使用来验证
	// 但这在测试中不实际

	// 实际验证方法：检查 AppendBatch 后文件是否被替换
	// 在 Unix 上用 inode，在 Windows 上用文件创建时间
	infoBefore, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat before failed: %v", err)
	}

	// 用 AppendBatch 追加一条消息
	if err := store.AppendBatch(sessionID, []Message{UserText("second message")}); err != nil {
		t.Fatalf("AppendBatch failed: %v", err)
	}

	infoAfter, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after failed: %v", err)
	}

	// 检测全量重写的方法：
	// 1. Unix: os.SameFile 比较 inode（rename 后 inode 变化）
	// 2. Windows: 检查 .tmp 文件是否曾存在（无法事后检查）
	// 由于 Windows 上 os.SameFile 不可靠，我们改用行为验证：
	// AppendBatch 的注释明确说 "atomic: all-or-nothing via temp file + rename"
	// 这是代码层面的证据，测试层面用 os.SameFile 在 Unix 上验证

	if runtime.GOOS != "windows" {
		// Unix: os.SameFile 比较 inode
		if !os.SameFile(infoBefore, infoAfter) {
			t.Errorf("BUG [FACT-SESS-001]: AppendBatch rewrites entire file instead of append-only\n" +
				"  期望: append-only (文件标识符不变, SameFile=true)\n" +
				"  实际: 全量重写 (文件标识符变化, SameFile=false)\n" +
				"  原因: store.go:111-171 AppendBatch 读取整个文件 + 写入临时文件 + os.Rename\n" +
				"  正确做法: fs.appendFileSync(path, line, { mode: 0o600 })\n" +
				"  证据: 外部参考实现\n" +
				"  影响: 并发或崩溃时丢失历史")
		}
	} else {
		// Windows: os.SameFile 不可靠，检查 .tmp 文件残留
		// 如果 AppendBatch 是 append-only，不会创建 .tmp 文件
		// 如果 AppendBatch 是全量重写，会创建 .tmp 文件（执行后已删除）
		// 我们通过检查 AppendBatch 执行期间是否有 .tmp 文件来验证
		// 但这需要并发检查，所以用 t.Skip 说明
		t.Skip("Windows 上 os.SameFile 对 rename 检测不可靠（MoveFileEx 可能保留文件标识符）。" +
			"手动验证步骤：\n" +
			"1. 检查 store.go:111-171 源码，AppendBatch 使用 os.ReadFile + os.WriteFile(tmpPath) + os.Rename\n" +
			"2. 这明确是全量重写而非 append-only\n" +
			"3. 在 Linux/macOS 上运行此测试可自动检测\n" +
			"证据: 外部参考实现（留档见审查文档）")
	}

	// 确保没有 .tmp 文件残留
	if _, err := os.Stat(tmpPath); err == nil {
		t.Errorf("BUG [FACT-SESS-001]: .tmp file left behind after AppendBatch\n" +
			"  .tmp 文件存在说明 AppendBatch 使用了临时文件 + rename（全量重写）\n" +
			"  正确做法: append-only 不需要临时文件")
	}
}

// TestBug_FilePermissionNot0600 对应 FACT-SESS-002
// 会话文件权限必须 0o600，当前实现使用 0644
// FACT-SESS-002
func TestBug_FilePermissionNot0600(t *testing.T) {
	// FACT-SESS-002
	// Windows 不使用 Unix 权限模型，权限检查不可靠
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不使用 Unix 权限模型，权限检查在 Windows 上不可靠。手动验证步骤：\n" +
			"1. 在 Linux/macOS 上运行此测试\n" +
			"2. 检查 session 文件权限: ls -l <session-dir>/*.jsonl\n" +
			"3. 期望: -rw------- (0600)\n" +
			"4. 实际: -rw-r--r-- (0644)")
	}

	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "test-perms"

	// Append 创建文件
	if err := store.Append(sessionID, UserText("test")); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	path := store.sessionPath(sessionID)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	// 期望权限 0o600（owner read/write only）
	// 当前实现使用 0644（其他用户可读）
	gotPerm := info.Mode().Perm()
	const wantPerm os.FileMode = 0o600
	if gotPerm != wantPerm {
		t.Errorf("BUG [FACT-SESS-002]: session file permission not 0o600\n"+
			"  期望: %o (0o600, owner read/write only)\n"+
			"  实际: %o (其他用户可读)\n"+
			"  原因: store.go:91 os.OpenFile(path, ..., 0644)\n"+
			"        store.go:159 os.WriteFile(tmpPath, buf.Bytes(), 0644)\n"+
			"  正确做法: mode: 0o600\n"+
			"  证据: 外部参考实现\n"+
			"  影响: transcript 含敏感对话，其他用户可读",
			wantPerm, gotPerm)
	}
}

// TestBug_LockBestEffortNotMutex 对应 FACT-SESS-003
// best-effort 模式下锁失败仍写入，锁非真正互斥
// FACT-SESS-003
//
// ★ L1 修复（flock 迁移，2026-09-22）：本缺陷已被根治。原实现用 O_EXCL + 30s 陈旧窗口，
// best-effort 下锁失败仍继续写入；迁移到内核 flock 后，锁被真实持有（另一进程 flock）
// 时 best-effort 也会拒绝写入。测试用真实子进程走 AcquireLock 持锁（写元数据），
// 父进程 best-effort 写入必须被拒。
func TestBug_LockBestEffortNotMutex(t *testing.T) {
	// FACT-SESS-003
	dir := t.TempDir()
	sessionID := "test-lock-mutex"

	holder := startProbeChild(t, dir, sessionID, "hold")
	defer func() {
		_ = holder.Process.Kill()
		_, _ = holder.Process.Wait()
	}()
	waitForReadyFile(t, dir)

	// 期望：锁被持有时，Append 应该失败（跨进程互斥）。
	if err := runProbeWriteChild(t, dir, sessionID); err == nil {
		t.Errorf("BUG [FACT-SESS-003]: best-effort lock mode allows write despite lock held\n" +
			"  flock 迁移后仍应拒绝：锁被真实持有时 best-effort 也不得写入")
	}
}

// TestBug_LockStaleTimeTooLong 对应 FACT-SESS-003
// stale 检测时间为 30 秒，事实表要求 10 秒（stale: 10000）
// FACT-SESS-003
//
// ★ L1 修复（flock 迁移，2026-09-22）：本缺陷被**根治**。原实现用 O_EXCL + 30s 陈旧
// 窗口，持锁进程崩溃后要等窗口过期才能回收；迁移到内核 flock 后，进程退出内核自动
// 释放锁，不存在 stale 窗口。故本用例改为断言「死进程残留的锁文件不阻塞 AcquireLock」
// —— flock 路径下，残留的 JSON 锁文件只是诊断信息，不再构成锁的持有。
func TestBug_LockStaleTimeTooLong(t *testing.T) {
	// FACT-SESS-003
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "test-stale"

	// 一个 15 秒前、由已死进程留下的锁文件（flock 路径下这是纯诊断残留，不构成持有）。
	lockPath := filepath.Join(dir, sessionID+".lock")
	lockData, _ := json.Marshal(SessionLock{
		PID:        99999, // 不存在的进程
		AcquiredAt: time.Now().Add(-15 * time.Second),
	})
	os.WriteFile(lockPath, lockData, 0644)

	// flock 迁移后：残留的死进程锁文件不再阻塞 AcquireLock（内核锁才是权威）。
	lock, err := store.AcquireLock(sessionID)
	if err != nil {
		t.Errorf("BUG [FACT-SESS-003]: 死进程残留锁文件仍阻塞 AcquireLock（%v）；"+
			"flock 迁移后锁由内核管理，残留 JSON 只是诊断信息，不应阻塞", err)
	} else {
		lock.Release()
	}
}

// TestBug_NoTombstoneForLargeFile 对应 FACT-SESS-004
// 大文件（>50MB）时应跳过重写，当前实现无条件重写
// FACT-SESS-004
func TestBug_NoTombstoneForLargeFile(t *testing.T) {
	// FACT-SESS-004
	t.Skip("需要创建 >50MB 文件验证 OOM 风险。手动验证步骤：\n" +
		"1. 创建一个 >50MB 的 session 文件（写入大量消息）\n" +
		"2. 调用 AppendBatch 追加一条消息\n" +
		"3. 观察 AppendBatch 是否读取整个文件到内存（store.go:134 os.ReadFile）\n" +
		"4. 期望：>50MB 时跳过重写，仅追加 tombstone 标记\n" +
		"5. 实际：无条件 os.ReadFile 读取整个文件，可能导致 OOM\n" +
		"证据: 外部参考实现（留档见审查文档）")
}

// TestBug_ExportMarkdownNoLock 对应 FACT-CONC-001
// ExportMarkdown 遍历 s.messages 但不持锁，并发修改时数据竞争
// FACT-CONC-001
func TestBug_ExportMarkdownNoLock(t *testing.T) {
	// FACT-CONC-001
	// 此测试需要 go test -race 才能检测数据竞争
	// 运行方式: go test -race ./internal/session/ -run TestBug_ExportMarkdownNoLock
	s := NewSession()
	for i := 0; i < 10; i++ {
		s.AddMessage(UserText("initial"))
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// goroutine 1: 并发修改 messages
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			s.AddMessage(UserText("concurrent write"))
		}
	}()

	// goroutine 2: 并发读取 messages（ExportMarkdown 不持锁）
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			s.ExportMarkdown()
		}
	}()

	wg.Wait()
	// 在 go test -race 下，如果 ExportMarkdown 不持锁，会检测到数据竞争并失败
	// 普通模式下此测试可能通过，需要 -race 标志
	t.Log("此测试需要 go test -race 才能检测数据竞争")
	t.Log("运行: go test -race ./internal/session/ -run TestBug_ExportMarkdownNoLock")
}

// TestBug_MessagesRawExposesInternal 对应 FACT-CONC-001
// MessagesRaw 返回内部 slice 不持锁，且暴露内部状态
// FACT-CONC-001
func TestBug_MessagesRawExposesInternal(t *testing.T) {
	// FACT-CONC-001
	s := NewSession()
	s.AddMessage(UserText("first"))
	s.AddMessage(UserText("second"))

	raw := s.MessagesRaw()
	if len(raw) != 2 {
		t.Fatalf("Expected 2 messages, got %d", len(raw))
	}

	// 修改返回的 slice 会影响内部状态（证明暴露了内部引用）
	raw[0] = UserText("tampered")

	// 验证内部状态是否被修改
	internal := s.MessagesRaw()
	if internal[0].TextContent() == "tampered" {
		t.Errorf("BUG [FACT-CONC-001]: MessagesRaw exposes internal slice without copy\n" +
			"  期望: 返回副本，修改不影响内部状态\n" +
			"  实际: 修改返回的 slice 影响内部状态\n" +
			"  原因: session.go:265-267 MessagesRaw 直接返回 s.messages\n" +
			"  正确做法: 返回副本或要求调用者持锁\n" +
			"  影响: 外部修改导致数据不一致")
	}
}

// TestBug_TagIndexNotReloaded 对应 FACT-CONC-003
// loadTagIndex 懒加载后不检查磁盘变化
// FACT-CONC-003
func TestBug_TagIndexNotReloaded(t *testing.T) {
	// FACT-CONC-003
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// 先写入 tag index 文件，使 loadTagIndex 缓存结果
	// 注意：loadTagIndex 在文件不存在时返回空 map 但不缓存（s.tagIndex 保持 nil）
	// 只有文件存在且解析成功时才缓存（s.tagIndex = index）
	tagPath := store.tagIndexPath()
	entries1 := []tagIndexEntry{
		{SessionID: "session-1", Tags: []string{"old-tag"}},
	}
	data1, _ := json.Marshal(entries1)
	os.WriteFile(tagPath, data1, 0644)

	// 第一次加载 tag index（缓存结果，s.tagIndex 被设置为非 nil）
	index := store.loadTagIndex()
	if len(index) != 1 || len(index["old-tag"]) != 1 {
		t.Fatalf("Expected 1 tag 'old-tag', got %v", index)
	}

	// 修改磁盘文件（模拟外部进程修改）
	entries2 := []tagIndexEntry{
		{SessionID: "session-2", Tags: []string{"new-tag"}},
	}
	data2, _ := json.Marshal(entries2)
	os.WriteFile(tagPath, data2, 0644)

	// 强制让文件 mtime 变化（WSL 挂载 NTFS 时 mtime 精度只有秒级，需显式设置未来时间）
	// 这样 loadTagIndex 的 mtime 检测 info.ModTime().After(s.tagIndexTime) 才能正确触发重载
	futureTime := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(tagPath, futureTime, futureTime); err != nil {
		t.Fatalf("os.Chtimes failed: %v", err)
	}

	// 再次加载 tag index
	// 期望：应该重新从磁盘加载，反映外部修改
	// 实际：loadTagIndex 检查 s.tagIndex != nil，直接返回缓存
	index2 := store.loadTagIndex()
	if _, hasNew := index2["new-tag"]; !hasNew {
		t.Errorf("BUG [FACT-CONC-003]: loadTagIndex does not reload after disk change\n" +
			"  期望: index 包含 'new-tag' (磁盘已更新)\n" +
			"  实际: index 不包含 'new-tag' (返回缓存)\n" +
			"  原因: store.go:627-648 loadTagIndex 检查 s.tagIndex != nil 直接返回\n" +
			"  正确做法: 从磁盘加载状态后，所有派生索引必须重建\n" +
			"  影响: tag 索引过期导致 SearchByTag 结果不一致")
	}
}

// TestBug_SaveNoBackup 对应 FACT-SESS-005
// Save 全量重写前无备份
// FACT-SESS-005
func TestBug_SaveNoBackup(t *testing.T) {
	// FACT-SESS-005
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "test-backup"

	// 写入初始内容
	store.AppendBatch(sessionID, []Message{UserText("original")})

	// Save 覆盖写入
	session := NewSession()
	session.AddMessage(UserText("overwritten"))
	err = store.Save(sessionID, session)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 检查是否有备份文件
	// 期望：Save 前应创建备份
	// 实际：无备份
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	hasBackup := false
	for _, entry := range entries {
		name := entry.Name()
		if strings.Contains(name, ".bak") || strings.Contains(name, ".backup") {
			hasBackup = true
			break
		}
	}

	if !hasBackup {
		t.Errorf("BUG [FACT-SESS-005]: Save does not create backup before overwrite\n" +
			"  期望: Save 前创建备份文件\n" +
			"  实际: 无备份文件\n" +
			"  原因: store.go:573-619 Save 直接覆盖，无备份\n" +
			"  正确做法: 创建时间戳备份并保留最近 5 个\n" +
			"  证据: 外部参考实现\n" +
			"  影响: 崩溃导致数据丢失，无法回滚")
	}
}
