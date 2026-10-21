package filehistory

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// SessionRestorer 会话恢复接口（解耦对 session.Store 的直接依赖）
// 由 session.Store 实现此接口，或通过适配器包装
type SessionRestorer interface {
	// LoadMessages 加载指定会话的消息列表
	LoadMessages(sessionId string) ([][]byte, error)
	// ReplaceMessages 用消息列表替换当前会话内容
	ReplaceMessages(sessionId string, messages [][]byte) error
	// SaveSession 保存会话（空会话创建）
	SaveSession(sessionId string) error
}

// diffAndRestore 计算快照与当前状态的差异并恢复文件
// 返回恢复的文件数、删除的文件数、警告列表、冲突列表
// 安全措施：
// - 恢复过程中任一文件操作失败时，回滚已修改的文件到操作前状态（01-文档决策 4）
// - ：盘上现值 hash 不在本会话 track 记录中（⇒ 被外部写入者
// 改过：另一会话 / 用户编辑器 / 其它工具）的文件，**跳过恢复/删除**并把冲突
// 面结构化上报 —— 修复前这里只写 warning 就照样覆写，可靠判据没接到不可逆
// 动作上（用户已拍板：仅跳过冲突文件、其余照常恢复；盖掉外部工作不可逆，
// 回滚不完整可补救）。注意：**本会话自己的后续写入**（hash 在 knownHashes 中）
// 不算冲突，照常回滚。
func (h *FileHistory) diffAndRestore(snap *Snapshot) (restored, deleted int, warnings []string, conflicts []RewindConflict, err error) {
	warnings = []string{}
	conflicts = []RewindConflict{}

	// fileBackup 记录单个文件被修改前的原始状态，用于失败时回滚
	type fileBackup struct {
		path     string  // 文件绝对路径
		origHash *string // 操作前的内容 hash（nil = 文件原本不存在）
		origMode uint32  // 操作前的权限位
	}

	// modifiedFiles 记录所有已被本函数修改/删除的文件及其原始状态
	var modifiedFiles []fileBackup

	// rollback 回滚已修改的文件到原始状态（best-effort，失败仅记录日志）
	// 返回包含回滚统计的合并错误
	rollback := func(failPath string, failErr error) error {
		rolledBack := 0
		// 逆序回滚（后修改的先恢复，更接近原始状态）
		for i := len(modifiedFiles) - 1; i >= 0; i-- {
			fb := modifiedFiles[i]
			if fb.origHash != nil {
				// 原本存在 → 用原始 hash 恢复内容
				if rErr := h.restoreFile(fb.path, fb.origHash, fb.origMode); rErr != nil {
					log.Printf("[filehistory] rollback: failed to restore %s: %v", fb.path, rErr)
				} else {
					rolledBack++
				}
			} else {
				// 原本不存在 → 删除（restore 创建的新文件）
				if rErr := h.deleteFile(fb.path); rErr != nil {
					log.Printf("[filehistory] rollback: failed to delete %s: %v", fb.path, rErr)
				} else {
					rolledBack++
				}
			}
		}
		return fmt.Errorf("restore %s: %w (rolled back %d/%d modified files)", failPath, failErr, rolledBack, len(modifiedFiles))
	}

	// 收集所有已知的 hash（用于检测外部修改）
	knownHashes := h.collectKnownHashes()

	// 处理快照中的文件：恢复到快照记录的状态
	for path, targetState := range snap.Files {
		currentState, gErr := h.getCurrentFileState(path)
		if gErr != nil {
			warnings = append(warnings, fmt.Sprintf("cannot read current state of %s: %v", path, gErr))
			continue
		}

		// 目标状态：文件不存在（hash 为 nil）
		if targetState.Hash == nil {
			// 当前文件存在 → 删除
			if currentState.Hash != nil {
				// 现值 hash 不在本会话 track 记录中 ⇒ 文件是外部创建/改写的，
				// 删除同样不可逆 ⇒ 跳过并上报冲突
				if !knownHashes[*currentState.Hash] {
					warnings = append(warnings, fmt.Sprintf("file %s was modified externally; skipped delete on rewind", path))
					conflicts = append(conflicts, RewindConflict{
						Path:        path,
						Reason:      RewindConflictReasonExternalModified,
						CurrentHash: currentState.Hash,
						TargetHash:  nil,
					})
					continue
				}
				// 记录原始状态用于回滚
				modifiedFiles = append(modifiedFiles, fileBackup{
					path: path, origHash: currentState.Hash, origMode: currentState.Mode,
				})
				if dErr := h.deleteFile(path); dErr != nil {
					return restored, deleted, warnings, conflicts, rollback(path, dErr)
				}
				deleted++
			}
			continue
		}

		// 目标状态：文件存在
		// 当前文件不存在 → 从内容存储恢复
		if currentState.Hash == nil {
			// 记录原始状态（原本不存在）用于回滚
			modifiedFiles = append(modifiedFiles, fileBackup{
				path: path, origHash: nil, origMode: 0,
			})
			if rErr := h.restoreFile(path, targetState.Hash, targetState.Mode); rErr != nil {
				return restored, deleted, warnings, conflicts, rollback(path, rErr)
			}
			restored++
			continue
		}

		// 两边都存在，比较 hash
		if *currentState.Hash != *targetState.Hash {
			// 现值 hash 不在任何 track 记录中 ⇒ 外部修改 ⇒ **跳过恢复**（不覆写），
			// 结构化上报。本会话自己的写入（hash 已知）不算冲突，照常回滚。
			if !knownHashes[*currentState.Hash] {
				warnings = append(warnings, fmt.Sprintf("file %s was modified externally (current hash not in history); skipped restore on rewind", path))
				conflicts = append(conflicts, RewindConflict{
					Path:        path,
					Reason:      RewindConflictReasonExternalModified,
					CurrentHash: currentState.Hash,
					TargetHash:  targetState.Hash,
				})
				continue
			}

			// 记录原始状态用于回滚
			modifiedFiles = append(modifiedFiles, fileBackup{
				path: path, origHash: currentState.Hash, origMode: currentState.Mode,
			})
			// 恢复到目标状态
			if rErr := h.restoreFile(path, targetState.Hash, targetState.Mode); rErr != nil {
				return restored, deleted, warnings, conflicts, rollback(path, rErr)
			}
			restored++
		}
		// hash 相同 → 无需操作
	}

	// 处理快照中不存在但当前存在的文件（在快照之后创建的文件）
	snapPaths := make(map[string]bool)
	for path := range snap.Files {
		snapPaths[path] = true
	}
	for path := range h.trackedPaths {
		if snapPaths[path] {
			continue
		}
		// 文件在 trackedPaths 中但不在快照中 → 删除
		if _, statErr := os.Stat(path); statErr == nil {
			// 获取当前状态用于回滚（getCurrentFileState 会将内容写入 content store，回滚时可恢复）
			currentState, gErr := h.getCurrentFileState(path)
			if gErr != nil {
				warnings = append(warnings, fmt.Sprintf("cannot get current state of %s for rollback: %v", path, gErr))
				continue
			}
			// 本会话创建后又被外部改写的文件（现值 hash 不在 track 记录中），
			// 删除同样不可逆 ⇒ 跳过并上报冲突（与上方删除分支同一判据）
			if currentState != nil && currentState.Hash != nil && !knownHashes[*currentState.Hash] {
				warnings = append(warnings, fmt.Sprintf("file %s was modified externally; skipped delete on rewind", path))
				conflicts = append(conflicts, RewindConflict{
					Path:        path,
					Reason:      RewindConflictReasonExternalModified,
					CurrentHash: currentState.Hash,
					TargetHash:  nil,
				})
				continue
			}
			origHash := (*string)(nil)
			var origMode uint32
			if currentState != nil {
				origHash = currentState.Hash
				origMode = currentState.Mode
			}
			modifiedFiles = append(modifiedFiles, fileBackup{
				path: path, origHash: origHash, origMode: origMode,
			})
			if dErr := h.deleteFile(path); dErr != nil {
				return restored, deleted, warnings, conflicts, rollback(path, dErr)
			}
			deleted++
		}
	}

	return restored, deleted, warnings, conflicts, nil
}

// restoreFile 从内容存储恢复单个文件到指定 hash 状态
// 包含权限恢复（os.Chmod）
func (h *FileHistory) restoreFile(path string, hash *string, mode uint32) error {
	if hash == nil {
		return nil
	}

	// S10 / HG4（决策 HG4=a）：恢复（写盘）前对目标路径做 safePath 复检，拒绝工作区外写入。
	// Track/TrackAfter 时已校验过路径，但索引是本地/可共享/可导入的——被篡改或过期的
	// 条目可能指向工作区外；恢复是对外写副作用，必须在落盘前再校验一次（fail-closed）。
	safe, err := h.safePath(path)
	if err != nil {
		return fmt.Errorf("refuse to restore outside workdir: %w", err)
	}
	path = safe

	// 从内容存储读取内容
	content, err := readContent(h.contentDir, *hash)
	if err != nil {
		return fmt.Errorf("read content %s: %w", *hash, err)
	}

	// 确保父目录存在
	parentDir := filepath.Dir(path)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	// 原子写入文件
	if err := util.AtomicWriteFile(path, content, os.FileMode(mode)); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	// 恢复文件权限（AtomicWriteFile 已设置，但确保 mode 正确）
	if mode != 0 {
		if err := os.Chmod(path, os.FileMode(mode)); err != nil {
			// 权限恢复失败不阻塞文件恢复，记录日志便于诊断（06-文档 §5.4）
			log.Printf("[filehistory] restoreFile: chmod %s to %o failed: %v", path, mode, err)
		}
	}

	return nil
}

// deleteFile 安全删除文件（检查是否在快照范围内）
// 仅删除文件，不删除目录；文件不存在视为成功
func (h *FileHistory) deleteFile(path string) error {
	// S10 / HG4（决策 HG4=a）：删除也是"索引路径 -> 磁盘副作用"，落盘前复检 safePath，
	// 防止被篡改/过期索引把工作区外的文件删掉。
	safe, err := h.safePath(path)
	if err != nil {
		return fmt.Errorf("refuse to delete outside workdir: %w", err)
	}
	path = safe

	// 检查文件是否存在
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 文件已不存在，视为成功
		}
		return fmt.Errorf("stat file: %w", err)
	}

	// 安全检查：只删除普通文件，不删除目录
	if info.IsDir() {
		return fmt.Errorf("refuse to delete directory: %s", path)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove file: %w", err)
	}

	return nil
}

// restoreSession 恢复会话状态（委托给 SessionRestorer）
// 根据快照的 SessionMsgCount 截断会话消息到目标消息数
// 当目标 msgId 无 snapshot 记录时返回错误（无法确定截断点）
func (h *FileHistory) restoreSession(msgId string, store SessionRestorer, sessionId string) error {
	if store == nil {
		return nil
	}

	// 加载当前会话消息
	messages, err := store.LoadMessages(sessionId)
	if err != nil {
		return fmt.Errorf("load messages: %w", err)
	}

	// 查找目标快照的 SessionMsgCount
	targetMsgCount := 0
	foundSnapshot := false
	for _, entry := range h.indexCache {
		if entry.Type == "snapshot" && entry.MsgId == msgId {
			targetMsgCount = entry.SessionMsgCount
			foundSnapshot = true
			break
		}
	}

	// 无 snapshot 记录时无法确定截断点，返回错误（调用方记入 Warnings）
	if !foundSnapshot {
		return fmt.Errorf("no snapshot record for msgId=%s, cannot determine session truncation point", msgId)
	}

	// 截断消息列表到目标数量
	if targetMsgCount > 0 && targetMsgCount < len(messages) {
		messages = messages[:targetMsgCount]
	}

	// 替换会话内容
	if err := store.ReplaceMessages(sessionId, messages); err != nil {
		return fmt.Errorf("replace messages: %w", err)
	}

	return nil
}

// createBackupSnapshot 在 Rewind 前创建备份快照
// 记录当前所有 tracked 文件的状态，生成唯一备份 msgId
func (h *FileHistory) createBackupSnapshot() (string, error) {
	// 生成唯一备份 msgId
	backupMsgId := fmt.Sprintf("rewind-backup-%d", time.Now().UnixNano())

	var changes []FileChange
	for path := range h.trackedPaths {
		state, err := h.getCurrentFileState(path)
		if err != nil {
			continue // 跳过无法读取的文件
		}

		fc := FileChange{
			Path: path,
			Mode: state.Mode,
		}
		if state.Hash != nil {
			fc.Hash = state.Hash
		}
		changes = append(changes, fc)
	}

	entry := IndexEntry{
		Type:      "snapshot",
		MsgId:     backupMsgId,
		Label:     "rewind-backup",
		Timestamp: time.Now(),
		Files:     changes,
	}

	if err := h.appendIndexEntry(entry); err != nil {
		return "", fmt.Errorf("append backup snapshot: %w", err)
	}

	return backupMsgId, nil
}

// collectKnownHashes 收集索引中所有出现过的 hash（用于检测外部修改）
// 跳过 rewind-backup 快照，因为它记录的是 Rewind 前的当前状态（可能包含外部修改）
func (h *FileHistory) collectKnownHashes() map[string]bool {
	hashes := make(map[string]bool)
	for _, entry := range h.indexCache {
		// 跳过 rewind-backup 快照，避免把外部修改后的 hash 误判为已知 hash
		if entry.Type == "snapshot" && entry.Label == "rewind-backup" {
			continue
		}
		for _, fc := range entry.Files {
			if fc.BeforeHash != nil {
				hashes[*fc.BeforeHash] = true
			}
			if fc.AfterHash != nil {
				hashes[*fc.AfterHash] = true
			}
			if fc.Hash != nil {
				hashes[*fc.Hash] = true
			}
		}
	}
	return hashes
}
