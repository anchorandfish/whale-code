// Package session — 归档式压缩（archive-on-compact）。
//
// 设计契约（2026-09-07 方案）：
// - 主会话文件仍是"模型所见"的唯一事实来源（不变量不变）；
// - Rewrite/Compact 物理删除消息前，被删消息先落入 sidecar 归档
// <sessionID>.archive.jsonl，写入顺序 = 归档先于主文件重写；
// - 归档失败时整体中止（主文件原样保留），杜绝"归档没写、主文件先删"
// 的永久丢失窗口；
// - 归档按"压缩记录"组织：每条记录以 type=compact_archive 头行开始，
// 后跟被删消息的原始 JSONL 行（保留 UUID/ParentUUID 链，供跨记录重建）；
// - 归档按 UUID 幂等去重：崩溃在"归档后、重写前"窗口重试不产生重复。
//
// 与同类 CLI（append-only transcript）的差异是有意的：whale 的
// "文件 = 模型上下文"承重结构不动，归档只补上"压缩那一刻的安全网"。
//
// 契约边界（对抗式审查 4.1c，显式声明）：归档覆盖"被整条删除的消息"；
// micro-compact/Collapse 对存活消息的原地截断/合并（UUID 不变）不归档
// 原始版本——"主文件 ∪ 归档 = 完整历史"不变量在内容维度不成立，
// 仅在消息维度成立。如需内容级审计，需另立 snapshot 机制。
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// archiveFileSuffix 归档文件后缀（<sessionID>.archive.jsonl）。
// 注意：List()/SearchByTag() 必须排除该后缀，否则归档会被误列为会话。
const archiveFileSuffix = ".archive.jsonl"

// compactArchiveRecordType 归档压缩记录头行的 type 值。
const compactArchiveRecordType = "compact_archive"

// compactArchiveHeader 归档中每条压缩记录的头行。
type compactArchiveHeader struct {
	Type            string `json:"type"`
	Timestamp       int64  `json:"ts"`
	CompactionCount int    `json:"compaction_count"`
	Dropped         int    `json:"dropped"`
}

// ArchiveStats 归档文件的统计信息（供 /compact log 展示）。
type ArchiveStats struct {
	Path     string
	Bytes    int64
	Records  int // 压缩记录数（头行数）
	Messages int // 已归档消息数
}

// isArchiveFileName 判断目录条目是否为归档文件（非会话文件）。
func isArchiveFileName(name string) bool {
	return strings.HasSuffix(name, archiveFileSuffix)
}

// archivePath 返回会话归档文件路径（与主文件同目录的 sidecar）。
//
// 与主文件一致做净化：sessionID 可能含 '/'（会话名取自用户输入），
// 不净化会让 sidecar 写进不存在的子目录。
func (s *Store) archivePath(sessionID string) string {
	return filepath.Join(s.dir, SanitizeSessionID(sessionID)+".archive.jsonl")
}

// ArchivePath 返回会话归档文件路径（公开版：供 backup-then-rename 类
// 原子替换流程（如 rewind 的 ReplaceMessages）在清理备份前保护归档
// sidecar——对抗式审查 1.1 P1 修复配套）。
func (s *Store) ArchivePath(sessionID string) string {
	return s.archivePath(sessionID)
}

// SetArchiveEnabled 开关归档式压缩（settings.json compact_archive，默认 true）。
// 关闭后 Rewrite/Compact 保持旧行为：直接物理删除，不产生归档文件。
func (s *Store) SetArchiveEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archiveEnabled = enabled
}

// ArchiveEnabled 返回归档开关状态。
// 注：Store.mu 为 sync.Mutex（非 RWMutex），短暂持锁读 bool 开销可忽略。
func (s *Store) ArchiveEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.archiveEnabled
}

// decodeArchiveLine 解析归档文件的一行：返回消息或压缩记录头行之一。
// 头行 type=compact_archive；消息行复用主文件的解码规则（无 type 字段）。
func decodeArchiveLine(data []byte) (*Message, *compactArchiveHeader, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, nil, err
	}
	if envelope.Type == compactArchiveRecordType {
		var header compactArchiveHeader
		if err := json.Unmarshal(data, &header); err != nil {
			return nil, nil, err
		}
		return nil, &header, nil
	}
	if envelope.Type != "" {
		return nil, nil, fmt.Errorf("unknown archive record type %q", envelope.Type)
	}
	msg, _, err := decodeSessionRecord(data)
	if err != nil {
		return nil, nil, err
	}
	return msg, nil, nil
}

// archiveDroppedLocked 把主文件中不在 survivors 内的消息追加进归档。
// 调用方必须持有 s.mu（Rewrite/Compact 路径）。
//
// 幂等性：扫描既有归档的 UUID 集合，已在归档中的消息不重复写入；
// 若本批无新增（fresh 为空）则不追加任何行（含头行）——崩溃重试不留痕。
//
// 失败语义：任何 IO 错误直接返回 error，调用方必须中止后续的主文件重写。
func (s *Store) archiveDroppedLocked(sessionID string, survivors []Message, compactionCount int) error {
	mainPath := s.sessionPath(sessionID)
	data, err := os.ReadFile(mainPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 主文件不存在（新会话首次压缩）——无被删内容
		}
		return fmt.Errorf("archive: read transcript: %w", err)
	}

	// 解析主文件中的全部消息
	var oldMsgs []Message
	badLines := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		msg, _, err := decodeSessionRecord(line)
		if err != nil {
			// 解析失败的坏行计入 canary（元数据行 err==nil 不计）——
			// 坏行内容会随后续主文件重写物理消失，零观测 = 静默数据丢失
			//（对抗式审查 3.2/3.3 P1 修复）。
			badLines++
			continue
		}
		if msg == nil {
			continue // 元数据行（首行）：非消息，不参与归档
		}
		oldMsgs = append(oldMsgs, *msg)
	}
	if badLines > 0 {
		log.Printf("[session] archive %s: %d malformed line(s) in transcript will be dropped by rewrite (not archived)", sessionID, badLines)
	}
	if len(oldMsgs) == 0 {
		return nil
	}

	// 存活集合：UUID 匹配；遗留无 UUID 消息按序列化内容匹配（向后兼容）
	survivorIDs := make(map[string]bool, len(survivors))
	survivorBlobs := make(map[string]bool, len(survivors))
	for _, m := range survivors {
		if m.UUID != "" {
			survivorIDs[m.UUID] = true
		}
		if blob, err := json.Marshal(m); err == nil {
			survivorBlobs[string(blob)] = true
		}
	}

	// 被删消息 = 主文件中既不在 UUID 集合、也不在内容集合里的消息
	var dropped []Message
	for _, m := range oldMsgs {
		if m.UUID != "" {
			if !survivorIDs[m.UUID] {
				dropped = append(dropped, m)
			}
			continue
		}
		blob, err := json.Marshal(m)
		if err == nil && !survivorBlobs[string(blob)] {
			dropped = append(dropped, m)
		}
	}
	if len(dropped) == 0 {
		return nil
	}

	// 既有归档 UUID 集合（幂等去重；坏行/截断尾行跳过并可观测）
	archivedIDs := make(map[string]bool)
	if archData, err := os.ReadFile(s.archivePath(sessionID)); err == nil {
		for _, line := range bytes.Split(archData, []byte{'\n'}) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			msg, _, err := decodeArchiveLine(line)
			if err != nil {
				log.Printf("[session] archive: skipping malformed archive line in %s: %v", sessionID, err)
				continue
			}
			if msg != nil && msg.UUID != "" {
				archivedIDs[msg.UUID] = true
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("archive: read existing archive: %w", err)
	}

	var fresh []Message
	for _, m := range dropped {
		if m.UUID != "" && archivedIDs[m.UUID] {
			continue // 崩溃重试：已归档过
		}
		fresh = append(fresh, m)
	}
	if len(fresh) == 0 {
		return nil
	}

	// 追加写入：头行 + 消息行。O_APPEND 保证与既有归档的原子衔接；
	// 半行崩溃只可能发生在归档尾部（主文件尚未动，无数据丢失）。
	f, err := os.OpenFile(s.archivePath(sessionID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("archive: open archive file: %w", err)
	}
	defer f.Close()

	writer := bufio.NewWriter(f)
	header := compactArchiveHeader{
		Type:            compactArchiveRecordType,
		Timestamp:       time.Now().Unix(),
		CompactionCount: compactionCount,
		Dropped:         len(fresh),
	}
	headerData, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("archive: serialize header: %w", err)
	}
	if _, err := writer.Write(append(headerData, '\n')); err != nil {
		return fmt.Errorf("archive: write header: %w", err)
	}
	for _, m := range fresh {
		msgData, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("archive: serialize message: %w", err)
		}
		if _, err := writer.Write(append(msgData, '\n')); err != nil {
			return fmt.Errorf("archive: write message: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("archive: flush: %w", err)
	}
	// fsync（对抗式审查 3.6 P1 修复）：主文件重写走 AtomicWriteFile（含
	// fsync），归档若只有 Flush，掉电时会出现"主文件替换已持久、归档追加
	// 丢失"——被删消息永久丢失且契约无检测。Sync 保证归档先于主文件落盘。
	if err := f.Sync(); err != nil {
		return fmt.Errorf("archive: fsync: %w", err)
	}
	return nil
}

// ArchiveStats 返回会话归档的统计信息。归档不存在时返回零值（不报错）。
func (s *Store) ArchiveStats(sessionID string) (ArchiveStats, error) {
	stats := ArchiveStats{Path: s.archivePath(sessionID)}
	data, err := os.ReadFile(stats.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return stats, nil
		}
		return stats, fmt.Errorf("archive: read stats: %w", err)
	}
	stats.Bytes = int64(len(data))
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		msg, header, err := decodeArchiveLine(line)
		if err != nil {
			continue // 统计容错：坏行不致命
		}
		if header != nil {
			stats.Records++
			continue
		}
		if msg != nil {
			stats.Messages++
		}
	}
	return stats, nil
}

// ArchiveRecord 一条压缩归档记录：头行 + 本次压缩被删的原始消息。
// 消息保序且保留 UUID/ParentUUID 链，供 /archive 查看与导出使用。
type ArchiveRecord struct {
	Header   compactArchiveHeader
	Messages []Message
}

// ReadArchive 读取会话的全部归档记录（只读；按头行切分）。
// 归档不存在时返回空切片（未压缩过的会话），不报错；
// 坏行/截断尾行跳过（半行崩溃只可能发生在尾部，前面完整记录仍可读）。
func (s *Store) ReadArchive(sessionID string) ([]ArchiveRecord, error) {
	data, err := os.ReadFile(s.archivePath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("archive: read records: %w", err)
	}
	var records []ArchiveRecord
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		msg, header, err := decodeArchiveLine(line)
		if err != nil {
			continue // 坏行容错：与 ArchiveStats 同策略
		}
		if header != nil {
			records = append(records, ArchiveRecord{Header: *header})
			continue
		}
		if msg == nil {
			continue
		}
		if len(records) == 0 {
			continue // 头行丢失的孤儿消息行：无法归属记录，跳过
		}
		last := &records[len(records)-1]
		last.Messages = append(last.Messages, *msg)
	}
	return records, nil
}

// PurgeArchive 删除会话归档文件（/compact purge）。
// 返回是否真的删除了文件及释放的字节数；归档不存在时幂等返回 false。
func (s *Store) PurgeArchive(sessionID string) (removed bool, freed int64, err error) {
	path := s.archivePath(sessionID)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("archive: stat: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return false, 0, fmt.Errorf("archive: remove: %w", err)
	}
	log.Printf("[session] archive purged for session %s (%d bytes)", sessionID, info.Size())
	return true, info.Size(), nil
}

// copyArchiveIfExists 把源会话的归档复制到新会话 ID（fork 继承压缩谱系）。
// 源归档不存在时是 no-op；复制失败返回错误（调用方决定降级策略）。
func copyArchiveIfExists(srcPath, dstPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("archive: read source archive: %w", err)
	}
	if err := os.WriteFile(dstPath, data, 0o600); err != nil {
		return fmt.Errorf("archive: write forked archive: %w", err)
	}
	return nil
}
