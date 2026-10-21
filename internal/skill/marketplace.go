// marketplace.go — M25-03 skill 市场远程仓库拉取。
//
// 设计目标：扩展现有 skill.Manager（仅支持本地目录加载）的远程仓库能力，
// 通过 HTTP 下载 zip 包 + 本地解压 + 复用现有 loadFromDir/validateSkill 加载。
// 不引入 git 依赖（跨平台 + CGO_ENABLED=0 约束，Windows 无 git 场景兼容）。
//
// 拉取流程：
// 1. AddSource 注册远程仓库（URL + AuthToken + Name）
// 2. FetchAll 遍历所有 source，HTTP GET 下载 zip 到临时文件
// 3. 解压 zip 到 cacheDir/<source.Name>/ 目录
// 4. 复用 Manager.loadFromDir 加载（source="plugin"，与本地 builtin/user/project 区分）
// 5. 失败降级：单个 source 拉取失败不阻塞其他 source（仅 log 告警）
//
// 市场远程仓库拉取；落地：RemoteSource + Marketplace
// 82-loadFromDir 复用；落地：解压后调 loadFromDir(source="plugin")
// REF-外部-同类 CLI@2026-03-31-无 skill 市场；落地：N25 创新点（对标 VS Code Marketplace）
// REF-外部-cursor@2026-03-31-无 skill 远程拉取；落地：超越维度=单用户场景适配（离线降级）
// SNAPSHOT: pkg=internal/skill; type=RemoteSource; fields={Name,URL,AuthToken,Branch}; type=Marketplace; methods={AddSource,FetchAll,FetchOne,ListRemote,ClearCache}; integration=Marketplace.manager.loadFromDir(source="plugin"); fatal_counterexample={1:单个 source 失败不阻塞其他,2:zip 解压失败降级跳过,3:无网络时降级用缓存}

package skill

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RemoteSource 远程 skill 仓库源（M25-03）。
//
// 支持 HTTP/HTTPS URL 下载 zip 包，不依赖 git（跨平台兼容）。
// AuthToken 用于私有仓库认证（Bearer token，可选）。
//
// B7（S19-G7）：传输与完整性收紧 ——
// - **强制 HTTPS**（回环地址除外，供本地开发/测试；见 validateSourceURL）；
// - 可选 `SHA256`（zip 的十六进制摘要）：给定时校验，不符即拒（内容寻址，防篡改/防投毒）。
//
// ⚠️ 仍**没有**签名校验：内容按 `Source="plugin"` 入表 ⇒ 在 skill 侧信任门（B1）里属"需首次确认"的来源。
type RemoteSource struct {
	// Name 仓库名称（用作缓存子目录名，必填，需合法文件名）。
	Name string `json:"name"`
	// URL 仓库地址（HTTPS；返回 zip 格式，必填）。
	URL string `json:"url"`
	// AuthToken Bearer 认证 token（可选，私有仓库用）。
	AuthToken string `json:"auth_token,omitempty"`
	// Branch Git 分支（预留字段，当前 HTTP 模式不用，未来 git 协议扩展用）。
	Branch string `json:"branch,omitempty"`
	// SHA256 是期望的 zip 内容摘要（64 位十六进制 sha256，可选）。
	// 给定时下载后必须匹配，否则拒绝加载（B7：远程内容不得"无凭据进入本机"）。
	SHA256 string `json:"sha256,omitempty"`
}

// RemoteSkillInfo 远程 skill 元信息（ListRemote 返回，从 manifest.json 或 zip 文件列表提取）。
type RemoteSkillInfo struct {
	// Name skill 名称。
	Name string `json:"name"`
	// Source 所属仓库名称。
	Source string `json:"source"`
	// FilePath 在 zip 内的相对路径。
	FilePath string `json:"file_path"`
}

// Marketplace skill 远程仓库管理器（M25-03）。
//
// 持有 *Manager 引用，下载并解压远程 skill 到本地缓存目录后，
// 复用 Manager.loadFromDir 加载（source="plugin" 标记来源）。
// 并发安全：mu 保护 sources 列表 + cacheDir 操作。
type Marketplace struct {
	manager *Manager // 关联的 skill.Manager（必填）

	// cacheDir 本地缓存根目录（解压 zip 的目标路径，必填）。
	// 每个源解压到 cacheDir/<source.Name>/ 子目录。
	cacheDir string

	// sources 已注册的远程源列表。
	sources []RemoteSource

	// httpClient HTTP 客户端（可注入，默认 30s 超时）。
	httpClient *http.Client

	// mu 保护 sources + cacheDir 写操作。
	mu sync.Mutex
}

// marketplaceDefaultTimeout 默认 HTTP 超时（30 秒，对标同类 CLI remote fetch timeout）。
const marketplaceDefaultTimeout = 30 * time.Second

// marketplaceDefaultUserAgent 默认 User-Agent（标识 whale-code skill 拉取）。
const marketplaceDefaultUserAgent = "whale-code-skill-marketplace/1.0"

// NewMarketplace 创建 skill 市场管理器（M25-03）。
//
// 参数：
// - manager: skill.Manager 实例（必填，nil 返回 nil）
// - cacheDir: 本地缓存目录（必填，不存在时自动创建）
//
// 默认 HTTP 超时 30 秒，可通过 SetHTTPClient 替换。
func NewMarketplace(manager *Manager, cacheDir string) *Marketplace {
	if manager == nil || cacheDir == "" {
		return nil
	}
	return &Marketplace{
		manager:  manager,
		cacheDir: cacheDir,
		httpClient: &http.Client{
			Timeout: marketplaceDefaultTimeout,
		},
	}
}

// SetHTTPClient 替换 HTTP 客户端（测试用，支持 mock）。
func (m *Marketplace) SetHTTPClient(client *http.Client) {
	if client != nil {
		m.httpClient = client
	}
}

// AddSource 注册远程仓库源（M25-03）。
//
// 参数：
// - source: 远程源（Name/URL 必填，AuthToken/Branch 可选）
//
// 同名 source 覆盖旧配置（URL/AuthToken 更新）。
// 返回 error 仅当 Name 或 URL 为空。
func (m *Marketplace) AddSource(source RemoteSource) error {
	if source.Name == "" {
		return fmt.Errorf("marketplace: source Name is empty")
	}
	if source.URL == "" {
		return fmt.Errorf("marketplace: source URL is empty")
	}
	// B7：传输安全在注册处就拦（不再只认 "http/https 前缀"）
	if err := validateSourceURL(source.URL); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// 同名覆盖
	for i, s := range m.sources {
		if s.Name == source.Name {
			m.sources[i] = source
			return nil
		}
	}
	m.sources = append(m.sources, source)
	return nil
}

// RemoveSource 移除已注册的远程源（M25-03）。
func (m *Marketplace) RemoveSource(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, s := range m.sources {
		if s.Name == name {
			m.sources = append(m.sources[:i], m.sources[i+1:]...)
			return
		}
	}
}

// ListSources 列出所有已注册的远程源（M25-03）。
func (m *Marketplace) ListSources() []RemoteSource {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]RemoteSource, len(m.sources))
	copy(result, m.sources)
	return result
}

// FetchAll 拉取所有已注册源（M25-03）。
//
// 遍历 m.sources，对每个源调 FetchOne。
// 单个源失败不阻塞其他源（致命反例 1：仅 log.Printf 告警 + continue）。
// 返回 nil 表示全部成功，返回 error 表示有失败（含失败源列表）。
// 已下载的缓存不重复下载（除非 ForceRefresh=true）。
//
// 参数：
// - ctx: 上下文（用于超时控制）
// - forceRefresh: true 强制重新下载（忽略缓存）
func (m *Marketplace) FetchAll(ctx context.Context, forceRefresh bool) error {
	m.mu.Lock()
	sources := make([]RemoteSource, len(m.sources))
	copy(sources, m.sources)
	m.mu.Unlock()

	if len(sources) == 0 {
		return nil // 无源，no-op
	}

	var failedSources []string
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("marketplace: context cancelled: %w", err)
		}
		if err := m.FetchOne(ctx, source, forceRefresh); err != nil {
			// 致命反例 1：单个源失败不阻塞其他源
			log.Printf("[marketplace] fetch source %s failed: %v (continuing)", source.Name, err)
			failedSources = append(failedSources, source.Name)
		}
	}

	if len(failedSources) > 0 {
		return fmt.Errorf("marketplace: %d source(s) failed: %s", len(failedSources), strings.Join(failedSources, ", "))
	}
	return nil
}

// FetchOne 拉取单个远程源（M25-03）。
//
// 流程：
// 1. 检查缓存（cacheDir/<name>/ 存在且非 forceRefresh → 直接 loadFromDir）
// 2. HTTP GET 下载 zip 到临时文件
// 3. 解压 zip 到 cacheDir/<name>/
// 4. 调 manager.loadFromDir(cacheDir/<name>/, "plugin") 加载
//
// 致命反例 2：zip 解压失败降级跳过（不删除已有缓存，返回 error 让 FetchAll 记录）。
// 致命反例 3：无网络时降级用缓存（HTTP 失败 + 缓存存在 → 加载缓存）。
func (m *Marketplace) FetchOne(ctx context.Context, source RemoteSource, forceRefresh bool) error {
	if source.Name == "" || source.URL == "" {
		return fmt.Errorf("marketplace: source Name or URL empty")
	}

	targetDir := filepath.Join(m.cacheDir, source.Name)

	// 步骤 1：检查缓存（非 forceRefresh 且目录存在 → 直接加载）
	if !forceRefresh {
		if dirExists(targetDir) {
			log.Printf("[marketplace] source %s: using cached version at %s", source.Name, targetDir)
			return m.manager.loadFromDir(targetDir, "plugin")
		}
	}

	// 步骤 2：HTTP GET 下载 zip
	zipPath, err := m.downloadZip(ctx, source)
	if err != nil {
		// 致命反例 3：无网络时降级用缓存
		if dirExists(targetDir) {
			log.Printf("[marketplace] source %s: download failed (%v), falling back to cache at %s", source.Name, err, targetDir)
			return m.manager.loadFromDir(targetDir, "plugin")
		}
		return fmt.Errorf("download zip: %w", err)
	}
	defer os.Remove(zipPath) // 清理临时文件

	// 步骤 3：解压 zip 到 targetDir
	if err := m.extractZip(zipPath, targetDir); err != nil {
		// 致命反例 2：解压失败降级（若已有缓存，加载缓存；否则返回错误）
		if dirExists(targetDir) {
			log.Printf("[marketplace] source %s: extract failed (%v), falling back to existing cache", source.Name, err)
			return m.manager.loadFromDir(targetDir, "plugin")
		}
		return fmt.Errorf("extract zip: %w", err)
	}

	// 步骤 4：加载解压后的 skill 文件
	if err := m.manager.loadFromDir(targetDir, "plugin"); err != nil {
		return fmt.Errorf("load from dir: %w", err)
	}

	log.Printf("[marketplace] source %s: fetched and loaded successfully", source.Name)
	return nil
}

// downloadZip 下载 zip 到临时文件（M25-03，内部方法）。
//
// 返回临时文件路径，调用方负责删除。
// 含 AuthToken 时添加 Authorization: Bearer <token> 头。
func (m *Marketplace) downloadZip(ctx context.Context, source RemoteSource) (string, error) {
	// B7：FetchOne 可以被直接调用（不经过 AddSource），因此校验也放在这里（防御纵深）。
	if err := validateSourceURL(source.URL); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", marketplaceDefaultUserAgent)
	req.Header.Set("Accept", "application/zip")
	if source.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+source.AuthToken)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http status %d: %s", resp.StatusCode, resp.Status)
	}

	// 检查 Content-Type（宽松校验，允许 application/zip、application/octet-stream）
	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "zip") && !strings.Contains(ct, "octet-stream") && !strings.Contains(ct, "binary") {
		log.Printf("[marketplace] source %s: unexpected Content-Type %s, continuing", source.Name, ct)
	}

	// 写入临时文件
	tmpFile, err := os.CreateTemp("", "whale-skill-*.zip")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("write temp file: %w", err)
	}
	tmpFile.Close()

	// B7：给定期望摘要时必须校验（fail-closed：不符即删临时文件并拒绝）。
	if source.SHA256 != "" {
		if err := verifyFileSHA256(tmpPath, source.SHA256); err != nil {
			os.Remove(tmpPath)
			return "", err
		}
	}
	return tmpPath, nil
}

// validateSourceURL 校验远程源的传输安全（B7）。
//
// 规则：只接受 https；http **仅限回环地址**（127.0.0.0/8、::1、localhost）——
// 回环不经过网络，明文不构成中间人面，保留它是为了本地开发与 httptest 场景。
func validateSourceURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("marketplace: invalid source URL %q: %w", rawURL, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("marketplace: plaintext http is not allowed for non-loopback host %q — use https (B7)；回环地址豁免（本地开发/测试）", u.Hostname())
	default:
		return fmt.Errorf("marketplace: unsupported URL scheme %q (want https), got %s", u.Scheme, rawURL)
	}
}

// isLoopbackHost 判断主机名是否为回环（含 "localhost" 与 127.0.0.0/8、::1）。
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// verifyFileSHA256 校验文件摘要（大小写不敏感；期望值非法即视为配置错误，fail-closed）。
func verifyFileSHA256(path, want string) error {
	want = strings.ToLower(strings.TrimSpace(want))
	if len(want) != 64 || !isHex(want) {
		return fmt.Errorf("marketplace: source SHA256 %q is not a valid sha256 hex digest (refusing to load)", want)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("marketplace: open downloaded file: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("marketplace: hash downloaded file: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("marketplace: content digest mismatch (want sha256:%s, got sha256:%s) — refusing to load remote content (B7)", want, got)
	}
	return nil
}

// isHex 判断字符串是否全为十六进制字符。
func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// extractZip 解压 zip 到目标目录（M25-03，内部方法）。
//
// 安全防护：
// - 路径遍历防护（Zip Slip 攻击）：校验每个文件路径不包含 .. 且不绝对路径
// - 仅解压 .md 和 .skill 文件（过滤可执行文件等危险内容）
// - 先清空目标目录（forceRefresh 场景），再解压
func (m *Marketplace) extractZip(zipPath, targetDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	// 创建目标目录（含父目录）
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}

	for _, f := range r.File {
		// 仅解压 .md 和 .skill 文件（安全过滤）
		name := f.Name
		if !strings.HasSuffix(name, ".md") && !strings.HasSuffix(name, ".skill") {
			continue
		}

		// 路径遍历防护（Zip Slip）
		if strings.Contains(name, "..") || filepath.IsAbs(name) {
			log.Printf("[marketplace] skip unsafe path in zip: %s", name)
			continue
		}

		// 提取文件名（忽略 zip 内目录结构，扁平化到 targetDir）
		fileName := filepath.Base(name)
		if fileName == "." || fileName == "/" || fileName == "" {
			continue
		}

		targetPath := filepath.Join(targetDir, fileName)

		// 防止目标路径逃逸 targetDir
		if !strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(targetDir)+string(filepath.Separator)) {
			log.Printf("[marketplace] skip path escaping target dir: %s", targetPath)
			continue
		}

		if err := extractZipFile(f, targetPath); err != nil {
			log.Printf("[marketplace] extract file %s failed: %v (continuing)", name, err)
			continue
		}
	}
	return nil
}

// extractZipFile 解压单个 zip 文件条目到目标路径（M25-03，内部辅助函数）。
func extractZipFile(f *zip.File, targetPath string) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open zip entry: %w", err)
	}
	defer rc.Close()

	out, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create target file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("copy file: %w", err)
	}
	return nil
}

// ListRemote 列出远程仓库中的 skill（M25-03）。
//
// 不下载完整 zip，仅获取文件列表（若服务器支持目录列表 API）。
// 当前实现：下载 zip 后列出文件名（简单实现，未来可优化为 HEAD 请求或 manifest.json）。
// 返回 RemoteSkillInfo 列表（Name 从文件名提取，Source=仓库名）。
func (m *Marketplace) ListRemote(ctx context.Context, source RemoteSource) ([]RemoteSkillInfo, error) {
	zipPath, err := m.downloadZip(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("download for list: %w", err)
	}
	defer os.Remove(zipPath)

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	var infos []RemoteSkillInfo
	for _, f := range r.File {
		name := f.Name
		if !strings.HasSuffix(name, ".md") && !strings.HasSuffix(name, ".skill") {
			continue
		}
		fileName := filepath.Base(name)
		skillName := strings.TrimSuffix(fileName, filepath.Ext(fileName))
		infos = append(infos, RemoteSkillInfo{
			Name:     skillName,
			Source:   source.Name,
			FilePath: name,
		})
	}
	return infos, nil
}

// ClearCache 清除指定源的本地缓存（M25-03）。
//
// 删除 cacheDir/<sourceName>/ 目录。若源不存在或目录不存在，no-op。
func (m *Marketplace) ClearCache(sourceName string) error {
	if sourceName == "" {
		return fmt.Errorf("marketplace: sourceName is empty")
	}
	targetDir := filepath.Join(m.cacheDir, sourceName)
	if !dirExists(targetDir) {
		return nil // 不存在，no-op
	}
	return os.RemoveAll(targetDir)
}

// ClearAllCache 清除所有源的本地缓存（M25-03）。
func (m *Marketplace) ClearAllCache() error {
	return os.RemoveAll(m.cacheDir)
}

// dirExists 检查目录是否存在（M25-03，内部辅助函数）。
func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
