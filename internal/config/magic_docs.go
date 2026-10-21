package config

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// magicDocsV1WarnOnce ensures deprecation warning is logged only once per process
var magicDocsV1WarnOnce sync.Once

// MagicDocs auto-detects and tracks Marked Documents in the project
//
// Deprecated: 使用 magic_docs_v2 的 DiscoverInstructionFiles 代替；V1 保留作为
// fallback 与简单项目场景，详见 H4 方案文档。
type MagicDocs struct {
	docs     map[string]string
	mu       sync.RWMutex
	watchDir string
}

// NewMagicDocs create Magic Docs instance
//
// Deprecated: 使用 magic_docs_v2 的 DiscoverInstructionFiles 代替；V1 保留作为
// fallback 与简单项目场景，详见 H4 方案文档。
func NewMagicDocs(watchDir string) *MagicDocs {
	md := &MagicDocs{
		docs:     make(map[string]string),
		watchDir: watchDir,
	}
	md.scan()
	return md
}

// scan scans for Marked Documents in directory
func (m *MagicDocs) scan() {
	if m.watchDir == "" {
		return
	}

	patterns := []struct {
		filename string
		key      string
	}{
		{"WHALE.md", "project"},
		{"CLAUDE.md", "project"},
		{".cursorrules", "rules"},
		{".github/copilot-instructions.md", "copilot"},
	}

	for _, p := range patterns {
		path := filepath.Join(m.watchDir, p.filename)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content != "" {
			m.docs[p.key] = content
		}
	}

	// scan .md files under docs/ directory
	docsDir := filepath.Join(m.watchDir, "docs")
	entries, err := os.ReadDir(docsDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(docsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content != "" {
			key := "docs/" + entry.Name()
			m.docs[key] = content
		}
	}
}

// Get fetches document content
func (m *MagicDocs) Get(key string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	content, ok := m.docs[key]
	return content, ok
}

// All fetches all documents
func (m *MagicDocs) All() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]string, len(m.docs))
	for k, v := range m.docs {
		result[k] = v
	}
	return result
}

// ProjectContext fetches project context (for system prompt)
//
// Deprecated: 使用 magic_docs_v2 的 DiscoverInstructionFiles 代替；V1 保留作为
// fallback 与简单项目场景，详见 H4 方案文档。
func (m *MagicDocs) ProjectContext() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var parts []string
	if content, ok := m.docs["project"]; ok {
		parts = append(parts, "=== 项目说明 ===\n"+content)
	}
	if content, ok := m.docs["rules"]; ok {
		parts = append(parts, "=== 规则 ===\n"+content)
	}

	// V1 fallback 路径告警：进程生命周期内仅告警一次，避免日志噪声
	if len(parts) > 0 && os.Getenv("WHALE_SUPPRESS_DEPRECATED_WARN") == "" {
		magicDocsV1WarnOnce.Do(func() {
			log.Printf("[WARN] magic_docs V1 (MagicDocs.ProjectContext) fallback 路径被触发，V2 不可用或无数据；V1 已 deprecated，详见 H4 方案文档")
		})
	}

	return strings.Join(parts, "\n\n")
}

// Refresh rescans documents
func (m *MagicDocs) Refresh() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docs = make(map[string]string)
	m.scan()
}

// List lists all detected documents
func (m *MagicDocs) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.docs))
	for k := range m.docs {
		keys = append(keys, k)
	}
	return keys
}

// markerCheckLines defines max lines to check for marker detection
const markerCheckLines = 5

// HasMagicMarker detects if file content contains Magic Doc marker
// Only checks first 5 lines, supports HTML comment marker <!-- MAGIC DOC --> and
// Markdown heading marker # MAGIC DOC: title formats
func HasMagicMarker(content string) bool {
	lines := strings.Split(content, "\n")
	checkEnd := markerCheckLines
	if len(lines) < checkEnd {
		checkEnd = len(lines)
	}

	for i := 0; i < checkEnd; i++ {
		line := strings.TrimSpace(lines[i])
		// HTML comment marker: <!-- MAGIC DOC -->
		if strings.Contains(line, "<!-- MAGIC DOC -->") {
			return true
		}
		// Markdown heading marker: # MAGIC DOC: title
		if strings.HasPrefix(line, "# MAGIC DOC:") {
			return true
		}
	}

	return false
}
