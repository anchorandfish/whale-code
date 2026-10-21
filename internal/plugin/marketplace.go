// Package plugin — M20-01 插件市场接口（SNAPSHOT）
//
// 本文件实现插件市场能力：git URL 拉取 + manifest 解析 + 索引查询。
// 对标 VSCode Marketplace（https://code.visualstudio.com/api/reference/extension-manifest），
// 简化版：以 git 仓库作为分发单元，plugin.json 作为 manifest，不引入中央注册中心。
//
// 设计要点（候选 A 选定）：
// - 接口签名：Marketplace 接口抽象，支持多后端（git/registry）
// - 结构体字段：MarketEntry 含 ID/Name/Version/Repository/Description/Homepage
// - 分层权责：本文件只做"发现+拉取"，签名校验由 signature.go，升级由 upgrade.go
// - 降级策略：git 不可用/网络错误返回 error，调用方决定降级（仅本地插件）
// - TTL规则：索引缓存 5 分钟（pluginIndexTTL），过期后强制重新拉取
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// pluginIndexTTL 是市场索引缓存有效期（5 分钟，对标 VSCode Marketplace 缓存策略）。
const pluginIndexTTL = 5 * time.Minute

// MarketEntry 是插件市场索引条目（对标 VSCode extension package.json 字段子集）。
type MarketEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Author      string `json:"author,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	Repository  string `json:"repository"`
	License     string `json:"license,omitempty"`
}

// MarketIndex 是插件市场索引（从市场仓库根目录 index.json 拉取）。
type MarketIndex struct {
	Version int           `json:"version"`
	Updated int64         `json:"updated"`
	Entries []MarketEntry `json:"entries"`
}

// Marketplace 是插件市场接口（SNAPSHOT-1）。
//
// 接口契约：
// - FetchIndex：拉取市场索引（带 TTL 缓存）
// - FetchEntry：按 ID 查询单个条目
// - Download：下载插件源码（返回 git URL 或本地路径，由 Manager.Install 处理）
//
// 实现方：
// - gitMarketplace：git 仓库作为市场（本文件实现）
// - 未来可扩展 registryMarketplace（中央注册中心）
type Marketplace interface {
	// FetchIndex 拉取市场索引。
	// ctx 用于超时控制（建议 30s）。
	// 返回的 MarketIndex 在 TTL 内缓存，避免频繁网络请求。
	FetchIndex(ctx context.Context) (*MarketIndex, error)

	// FetchEntry 按 ID 查询条目。
	// 未找到返回 (nil, nil)（非 error）。
	FetchEntry(ctx context.Context, id string) (*MarketEntry, error)

	// Download 返回插件可安装源（git URL 或本地路径）。
	// 调用方应将返回值传给 Manager.Install。
	Download(ctx context.Context, id string) (string, error)
}

// gitMarketplace 是基于 git 仓库的插件市场实现（候选 A）。
//
// 市场仓库结构：
// - 根目录 index.json：MarketIndex 序列化
// - 各插件子目录：含 plugin.json + 源码
//
// 缓存策略：
// - index 在 pluginIndexTTL 内缓存（内存）
// - 超时后下次 FetchIndex 强制重新拉取
type gitMarketplace struct {
	mu         sync.Mutex
	indexURL   string       // 索引文件 URL（https://.../index.json）
	index      *MarketIndex // 缓存的索引
	fetchedAt  time.Time    // 上次拉取时间
	httpClient *http.Client // HTTP 客户端（可注入，默认 30s 超时)
}

// NewGitMarketplace 创建基于 git 仓库的插件市场实例。
//
// 参数：
// - indexURL: 市场索引文件 URL（如 https://raw.githubusercontent.com/whale-code/plugins/main/index.json）
//
// 返回的实例线程安全（内部 mutex 保护索引缓存）。
//
// TODO(v1.1): 插件市场子系统尚未在生产接线。需新增 /plugin marketplace list/search/install
// 命令并在 NewExecutor 中注入 NewGitMarketplace（indexURL 从 config 读取）。
// 桶 X5 死代码清理：当前该工厂零生产调用，标记为 v1.1 路线图项。
func NewGitMarketplace(indexURL string) Marketplace {
	return &gitMarketplace{
		indexURL:   indexURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// FetchIndex 拉取市场索引（带 TTL 缓存， SNAPSHOT-2）。
//
// 行为：
// - 缓存有效（fetchedAt + TTL > now）→ 返回缓存
// - 缓存失效或不存在 → HTTP GET 拉取并更新缓存
// - 网络错误返回 error（调用方降级为"仅本地插件"）
func (g *gitMarketplace) FetchIndex(ctx context.Context) (*MarketIndex, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 缓存有效直接返回
	if g.index != nil && time.Since(g.fetchedAt) < pluginIndexTTL {
		return g.index, nil
	}

	// HTTP GET 拉取索引
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.indexURL, nil)
	if err != nil {
		return nil, fmt.Errorf("marketplace: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("marketplace: fetch index: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("marketplace: fetch index: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB 上限防 OOM
	if err != nil {
		return nil, fmt.Errorf("marketplace: read index body: %w", err)
	}

	var idx MarketIndex
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("marketplace: parse index JSON: %w", err)
	}

	g.index = &idx
	g.fetchedAt = time.Now()
	return g.index, nil
}

// FetchEntry 按 ID 查询条目（SNAPSHOT-3）。
//
// 未找到返回 (nil, nil)（非 error），调用方按需处理。
// 内部调用 FetchIndex（享受 TTL 缓存）。
func (g *gitMarketplace) FetchEntry(ctx context.Context, id string) (*MarketEntry, error) {
	idx, err := g.FetchIndex(ctx)
	if err != nil {
		return nil, err
	}
	for i := range idx.Entries {
		if idx.Entries[i].ID == id {
			return &idx.Entries[i], nil
		}
	}
	return nil, nil
}

// Download 返回插件可安装源（SNAPSHOT-4）。
//
// 行为：
// - 查询 entry，未找到返回 error
// - entry.Repository 作为 git URL 返回（由 Manager.Install 处理 clone）
// - 不在此处执行 clone（职责分离，clone 由 cloneFromGit 完成）
func (g *gitMarketplace) Download(ctx context.Context, id string) (string, error) {
	entry, err := g.FetchEntry(ctx, id)
	if err != nil {
		return "", err
	}
	if entry == nil {
		return "", fmt.Errorf("marketplace: plugin %q not found in index", id)
	}
	if entry.Repository == "" {
		return "", fmt.Errorf("marketplace: plugin %q has empty repository", id)
	}
	return entry.Repository, nil
}

// ParseMarketIndex 从 JSON 字节切片解析市场索引（导出供测试与外部调用）。
//
// 用于测试场景构造索引，或从本地缓存读取。
func ParseMarketIndex(data []byte) (*MarketIndex, error) {
	var idx MarketIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse market index: %w", err)
	}
	return &idx, nil
}

// FormatMarketIndex 格式化市场索引为可读字符串（CLI 展示用）。
func FormatMarketIndex(idx *MarketIndex) string {
	if idx == nil || len(idx.Entries) == 0 {
		return "No plugins available in marketplace."
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Marketplace: %d plugins (updated %s)\n", len(idx.Entries), time.Unix(idx.Updated, 0).Format("2006-01-02 15:04")))
	for _, e := range idx.Entries {
		sb.WriteString(fmt.Sprintf("  - %s (%s) v%s: %s\n", e.Name, e.ID, e.Version, e.Description))
	}
	return sb.String()
}
