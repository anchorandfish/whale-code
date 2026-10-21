// inverted_index.go — H10 Phase 4 模块 2 倒排索引 + 中英混排分词（Phase 4 §3.2.2）。
// REF-外部-同类CLI@2026-03-31快照-memory types.ts 相关性选择器选 top-5；借鉴逻辑：粗筛 + 精排两阶段；落地：InvertedIndex 粗筛 + LLMReranker 精排
// 数据结构；落地：byTag/byToken/byType 三 map + RWMutex
// 零外部依赖；落地：Unicode 分类 + 驼峰拆分 + 去停用词
// REF-演进-v0.1 总纲 §7.1 v0.1 无索引机制；落地：v1 补全倒排索引 + _index.json
// SNAPSHOT: pkg=internal/orchestrator; type=InvertedIndex; maps={byTag,byToken,byType}(map[string]map[string]struct{}); scoring=token*1+tag*2+task_type*3; topN_default=20; tokenize=Unicode分类+驼峰拆分+去停用词(零外部依赖); persistence=_index.json; rebuild=从jsonl全量重建

package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// stopWords 内置停用词表（零外部依赖，Windows 单二进制友好）。
var stopWords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "of": {}, "and": {}, "or": {}, "to": {}, "in": {}, "on": {}, "for": {}, "is": {}, "are": {},
	"的": {}, "了": {}, "等": {}, "和": {}, "与": {}, "或": {}, "在": {}, "是": {},
}

// searchTopNDefault 默认粗筛 top-N 上限（对齐 同类 CLI top-5 选择器）。
const searchTopNDefault = 20

// InvertedIndex 关键词倒排索引，支持 O(log n) 标签/分词查询（Phase 4 §3.2.2）。
// 三 map 设计：byTag tag→pattern_id 集合 / byToken token→pattern_id 集合 / byType task_type→pattern_id 集合。
// 并发保护：sync.RWMutex，Add 加写锁，Search 加读锁。
type InvertedIndex struct {
	mu          sync.RWMutex
	byTag       map[string]map[string]struct{} // tag -> set(pattern_id)
	byToken     map[string]map[string]struct{} // token -> set(pattern_id)
	byType      map[string]map[string]struct{} // task_type -> set(pattern_id)
	bySignature map[string]string              // pattern_id -> task_signature（B-P1 修复：保留原始签名供持久化）
}

// NewInvertedIndex 创建空倒排索引（Phase 4 §3.2.2）。
func NewInvertedIndex() *InvertedIndex {
	return &InvertedIndex{
		byTag:       make(map[string]map[string]struct{}),
		byToken:     make(map[string]map[string]struct{}),
		byType:      make(map[string]map[string]struct{}),
		bySignature: make(map[string]string),
	}
}

// Add 将 Pattern 加入倒排索引（同步更新 byTag/byToken/byType，Phase 4 §3.2.2）。
func (idx *InvertedIndex) Add(p *Pattern) {
	if p == nil || p.PatternID == "" {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// byToken：从 TaskSignature 分词
	for _, tok := range tokenize(p.TaskSignature) {
		if idx.byToken[tok] == nil {
			idx.byToken[tok] = make(map[string]struct{})
		}
		idx.byToken[tok][p.PatternID] = struct{}{}
	}

	// byType：从 Pattern 提取 task_type
	if tt := extractTaskType(p); tt != "" {
		if idx.byType[tt] == nil {
			idx.byType[tt] = make(map[string]struct{})
		}
		idx.byType[tt][p.PatternID] = struct{}{}
	}

	// byTag：从 Pattern 提取 tags
	for _, tag := range extractTags(p) {
		if idx.byTag[tag] == nil {
			idx.byTag[tag] = make(map[string]struct{})
		}
		idx.byTag[tag][p.PatternID] = struct{}{}
	}

	// bySignature：保留原始 task_signature（B-P1 修复：持久化时不再丢失）
	idx.bySignature[p.PatternID] = p.TaskSignature
}

// Search 粗筛：返回 top-N 候选 pattern_id（默认 N=20，Phase 4 §3.2.2）。
// 评分：token 命中数 * 1 + tag 命中数 * 2 + task_type 命中数 * 3，按总分降序取 top-N。
func (idx *InvertedIndex) Search(signature string, taskType string, tags []string, topN int) []string {
	if topN <= 0 {
		topN = searchTopNDefault
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	scores := make(map[string]int)

	// token 命中（×1）
	for _, tok := range tokenize(signature) {
		if ids, ok := idx.byToken[tok]; ok {
			for id := range ids {
				scores[id] += 1
			}
		}
	}

	// task_type 命中（×3）
	if taskType != "" {
		if ids, ok := idx.byType[taskType]; ok {
			for id := range ids {
				scores[id] += 3
			}
		}
	}

	// tag 命中（×2）
	for _, tag := range tags {
		if ids, ok := idx.byTag[tag]; ok {
			for id := range ids {
				scores[id] += 2
			}
		}
	}

	// 按总分降序排序，取 top-N
	type scored struct {
		id    string
		score int
	}
	result := make([]scored, 0, len(scores))
	for id, sc := range scores {
		result = append(result, scored{id: id, score: sc})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].score != result[j].score {
			return result[i].score > result[j].score
		}
		return result[i].id < result[j].id // 同分按 id 字典序稳定排序
	})

	if len(result) > topN {
		result = result[:topN]
	}

	ids := make([]string, 0, len(result))
	for _, r := range result {
		ids = append(ids, r.id)
	}
	return ids
}

// LoadFromIndexFile 从 _index.json 加载（启动时调用，Phase 4 §3.2.2）。
// 文件不存在视为空索引，不报错。
func (idx *InvertedIndex) LoadFromIndexFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inverted_index: read %s: %w", path, err)
	}
	var idxFile patternIndex
	if err := json.Unmarshal(data, &idxFile); err != nil {
		return fmt.Errorf("inverted_index: unmarshal %s: %w", path, err)
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	// 清空再加载
	idx.byTag = make(map[string]map[string]struct{})
	idx.byToken = make(map[string]map[string]struct{})
	idx.byType = make(map[string]map[string]struct{})
	idx.bySignature = make(map[string]string)

	for patternID, entry := range idxFile.Entries {
		// 从 entry.Tags 还原 byTag
		for _, tag := range entry.Tags {
			if idx.byTag[tag] == nil {
				idx.byTag[tag] = make(map[string]struct{})
			}
			idx.byTag[tag][patternID] = struct{}{}
		}
		// 从 entry.TaskSignature 还原 byToken
		for _, tok := range tokenize(entry.TaskSignature) {
			if idx.byToken[tok] == nil {
				idx.byToken[tok] = make(map[string]struct{})
			}
			idx.byToken[tok][patternID] = struct{}{}
		}
		// 从 entry.TaskType 还原 byType
		if entry.TaskType != "" {
			if idx.byType[entry.TaskType] == nil {
				idx.byType[entry.TaskType] = make(map[string]struct{})
			}
			idx.byType[entry.TaskType][patternID] = struct{}{}
		}
		// B-P1 修复：还原 bySignature（保留原始 task_signature 供下次持久化）
		idx.bySignature[patternID] = entry.TaskSignature
	}
	return nil
}

// SaveToIndexFile 持久化到 _index.json（写入时同步更新，Phase 4 §3.2.2）。
// 注意：本方法保留供独立索引工具使用；ExperienceStore.Save 已在写 jsonl 时同步更新 _index.json。
func (idx *InvertedIndex) SaveToIndexFile(path string) error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	// 重建 patternIndex 结构：聚合每个 pattern_id 的 tags/task_signature/task_type
	type entryAgg struct {
		tags          []string
		taskSignature string
		taskType      string
	}
	aggs := make(map[string]*entryAgg)

	// 从 byTag 反推
	for tag, ids := range idx.byTag {
		for id := range ids {
			if aggs[id] == nil {
				aggs[id] = &entryAgg{}
			}
			aggs[id].tags = append(aggs[id].tags, tag)
		}
	}
	// 从 byType 反推 task_type（取第一个匹配）
	for tt, ids := range idx.byType {
		for id := range ids {
			if aggs[id] == nil {
				aggs[id] = &entryAgg{}
			}
			if aggs[id].taskType == "" {
				aggs[id].taskType = tt
			}
		}
	}
	// B-P1 修复：从 bySignature 读取原始 task_signature（旧实现留空导致持久化丢失签名）
	for id, sig := range idx.bySignature {
		if aggs[id] == nil {
			aggs[id] = &entryAgg{}
		}
		aggs[id].taskSignature = sig
	}

	idxFile := patternIndex{Entries: make(map[string]patternIndexEntry, len(aggs))}
	for id, agg := range aggs {
		idxFile.Entries[id] = patternIndexEntry{
			Tags:          agg.tags,
			TaskSignature: agg.taskSignature,
			TaskType:      agg.taskType,
		}
	}

	data, err := json.MarshalIndent(idxFile, "", "  ")
	if err != nil {
		return fmt.Errorf("inverted_index: marshal: %w", err)
	}
	// 原子写入
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("inverted_index: write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("inverted_index: rename %s → %s: %w", tmp, path, err)
	}
	return nil
}

// Rebuild 从 jsonl 全量重建索引（崩溃恢复 / 手动 rebuild 命令，Phase 4 §3.2.2）。
// 扫所有 jsonl 文件 → 逐行 json.Unmarshal → Add(p) → 写新 _index.json + checksum → 原子替换。
func (idx *InvertedIndex) Rebuild(jsonlDir string) error {
	// 扫描 jsonlDir 下所有 .jsonl 文件
	entries, err := os.ReadDir(jsonlDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inverted_index: read dir %s: %w", jsonlDir, err)
	}

	// 收集所有 Pattern
	var patterns []*Pattern
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(jsonlDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range splitJSONLines(data) {
			var p Pattern
			if err := json.Unmarshal(line, &p); err != nil {
				continue
			}
			patterns = append(patterns, &p)
		}
	}

	// 清空索引并重新填充
	idx.mu.Lock()
	idx.byTag = make(map[string]map[string]struct{})
	idx.byToken = make(map[string]map[string]struct{})
	idx.byType = make(map[string]map[string]struct{})
	idx.bySignature = make(map[string]string)
	idx.mu.Unlock()

	for _, p := range patterns {
		idx.Add(p)
	}

	// 写 _index.json + checksum
	idxPath := filepath.Join(jsonlDir, indexFileName)
	if err := idx.SaveToIndexFile(idxPath); err != nil {
		return fmt.Errorf("inverted_index: save index: %w", err)
	}

	// 计算 checksum 并写入 _index.checksum 文件
	checksumPath := filepath.Join(jsonlDir, "_index.checksum")
	if err := writeChecksumFile(checksumPath, idxPath); err != nil {
		return fmt.Errorf("inverted_index: write checksum: %w", err)
	}

	return nil
}

// writeChecksumFile 计算 path 文件的 sha256 checksum，写入 checksumPath。
func writeChecksumFile(checksumPath, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read for checksum: %w", err)
	}
	checksum := computeSHA256Hex(data)
	// 格式：sha256:<hex> <filename>
	content := fmt.Sprintf("sha256:%s  %s\n", checksum, filepath.Base(path))
	tmp := checksumPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write checksum tmp: %w", err)
	}
	if err := os.Rename(tmp, checksumPath); err != nil {
		return fmt.Errorf("rename checksum: %w", err)
	}
	return nil
}

// computeSHA256Hex 计算数据的 sha256 hex（用于索引 checksum，Phase 4 §3.2.2）。
func computeSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// tokenize 中英混排分词（不依赖外部分词库，用 Unicode 分类 + 驼峰拆分，Phase 4 §3.2.2）。
// 1. Unicode 分类：CJK 字符按字切，ASCII 按非字母数字边界切
// 2. 驼峰拆分：AddSlashCommand → [add, slash, command]
// 3. 转小写 + 去停用词（the/a/an/of/的/了/等）
// 4. 返回 token 列表
func tokenize(signature string) []string {
	if signature == "" {
		return nil
	}

	var tokens []string
	var current []rune

	flushCurrent := func() {
		if len(current) == 0 {
			return
		}
		// 驼峰拆分：扫描 current，遇小写→大写转换点切分
		subs := splitCamelCase(string(current))
		for _, s := range subs {
			tok := strings.ToLower(s)
			if tok == "" {
				continue
			}
			if _, isStop := stopWords[tok]; isStop {
				continue
			}
			tokens = append(tokens, tok)
		}
		current = current[:0]
	}

	for _, r := range signature {
		if unicode.Is(unicode.Han, r) {
			// CJK 字符：先 flush 当前 ASCII 段，再单独切一个字
			flushCurrent()
			tok := strings.ToLower(string(r))
			if _, isStop := stopWords[tok]; isStop {
				continue
			}
			tokens = append(tokens, tok)
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current = append(current, r)
		} else {
			// 非字母数字：分隔符，flush 当前
			flushCurrent()
		}
	}
	flushCurrent()

	return tokens
}

// splitCamelCase 驼峰拆分：AddSlashCommand → [Add, Slash, Command]。
// 同时处理 ABCToken 这种连续大写后接小写的情况（保留 ABC + Token）。
func splitCamelCase(s string) []string {
	if s == "" {
		return nil
	}
	runes := []rune(s)
	var result []string
	start := 0
	for i := 1; i < len(runes); i++ {
		// 小写 → 大写：切分点
		if unicode.IsLower(runes[i-1]) && unicode.IsUpper(runes[i]) {
			result = append(result, string(runes[start:i]))
			start = i
			continue
		}
		// 大写 → 大写 → 小写：在第二个大写前切分（处理 ABCToken → ABC + Token）
		if i+1 < len(runes) && unicode.IsUpper(runes[i-1]) && unicode.IsUpper(runes[i]) && unicode.IsLower(runes[i+1]) {
			result = append(result, string(runes[start:i]))
			start = i
		}
	}
	if start < len(runes) {
		result = append(result, string(runes[start:]))
	}
	return result
}

// Size 返回索引中 pattern_id 总数（仅用于诊断）。
func (idx *InvertedIndex) Size() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, ids := range idx.byToken {
		for id := range ids {
			seen[id] = struct{}{}
		}
	}
	for _, ids := range idx.byTag {
		for id := range ids {
			seen[id] = struct{}{}
		}
	}
	for _, ids := range idx.byType {
		for id := range ids {
			seen[id] = struct{}{}
		}
	}
	return len(seen)
}
