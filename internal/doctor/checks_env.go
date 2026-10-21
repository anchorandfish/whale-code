package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
	versionpkg "github.com/anchorandfish/whale-code/internal/version"
	"golang.org/x/mod/semver"
)

// checkEnvironment 执行环境检测项（4 项）。
// 检测：OS + 架构、Go 版本、whale-code 版本、工作目录可读写。
// 对标同类 CLI installationType + version 区块。
// 环境检测无外部依赖，最先执行。
func (d *Doctor) checkEnvironment() []CheckResult {
	var results []CheckResult

	// 1. OS + 架构（runtime.GOOS/GOARCH）
	results = append(results, CheckResult{
		Category: "Environment",
		Name:     "OS",
		Severity: SeverityOK,
		Message:  runtime.GOOS + "/" + runtime.GOARCH,
	})

	// 2. Go 版本（runtime.Version）
	results = append(results, CheckResult{
		Category: "Environment",
		Name:     "Go",
		Severity: SeverityOK,
		Message:  runtime.Version(),
	})

	// 3. whale-code 版本（internal/version 单一真源）
	results = append(results, d.checkVersion())

	// 4. 工作目录可读写
	results = append(results, d.checkWorkDir())

	return results
}

// checkVersion 检测 whale-code 版本信息。
// 版本号来自 internal/version.Version（与 --version / 启动横幅同源）。
func (d *Doctor) checkVersion() CheckResult {
	return CheckResult{
		Category: "Environment",
		Name:     "Version",
		Severity: SeverityOK,
		Message:  "whale v" + versionpkg.Version,
	}
}

// checkWorkDir 检测工作目录是否可读写。
// 不可读或不可写时标记为 Fatal（阻塞使用），因为工具执行依赖工作目录。
func (d *Doctor) checkWorkDir() CheckResult {
	if d.workDir == "" {
		return CheckResult{
			Category:   "Environment",
			Name:       "WorkDir",
			Severity:   SeverityFatal,
			Message:    "work directory is empty",
			FixSuggest: "start whale-code from a valid project directory",
		}
	}

	// 检测可读性
	info, err := os.Stat(d.workDir)
	if err != nil {
		return CheckResult{
			Category:   "Environment",
			Name:       "WorkDir",
			Severity:   SeverityFatal,
			Message:    "not readable: " + err.Error(),
			Detail:     d.workDir,
			FixSuggest: "check directory permissions or path",
		}
	}
	if !info.IsDir() {
		return CheckResult{
			Category: "Environment",
			Name:     "WorkDir",
			Severity: SeverityFatal,
			Message:  "not a directory",
			Detail:   d.workDir,
		}
	}

	// 检测可写性：尝试创建临时文件
	probeFile := d.workDir + string(os.PathSeparator) + ".whale-doctor-probe"
	if err := os.WriteFile(probeFile, []byte("probe"), 0644); err != nil {
		return CheckResult{
			Category:   "Environment",
			Name:       "WorkDir",
			Severity:   SeverityFatal,
			Message:    "not writable: " + err.Error(),
			Detail:     d.workDir,
			FixSuggest: "check directory write permissions",
		}
	}
	os.Remove(probeFile)

	return CheckResult{
		Category: "Environment",
		Name:     "WorkDir",
		Severity: SeverityOK,
		Message:  d.workDir + " (readable, writable)",
	}
}

// networkCheckEndpoint 根据当前实际 Provider 配置选择网络探测端点。
// 显式 WHALE_NETWORK_CHECK_ENDPOINT 优先；随后与主程序的 Provider 选择顺序保持一致。
func (d *Doctor) networkCheckEndpoint() string {
	if endpoint := strings.TrimSpace(os.Getenv("WHALE_NETWORK_CHECK_ENDPOINT")); endpoint != "" {
		return endpoint
	}

	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return "https://api.anthropic.com"
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		if endpoint := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")); endpoint != "" {
			return endpoint
		}
		return "https://api.openai.com"
	}

	if d.cfg != nil {
		switch d.cfg.Provider {
		case "openai":
			if endpoint := strings.TrimSpace(d.cfg.CustomBaseURL); endpoint != "" {
				return endpoint
			}
			return "https://api.openai.com"
		case "anthropic":
			return "https://api.anthropic.com"
		}
		if endpoint := strings.TrimSpace(d.cfg.CustomBaseURL); endpoint != "" {
			return endpoint
		}
	}

	return "https://api.anthropic.com"
}

// checkNetwork 检测当前实际 API Provider 的网络连通性。
// V3-4-1：为 `whale doctor --network` 提供独立的网络检测入口。
// 连接失败仅返回 Warning；API 是否真正可用仍由 checkAPI 的最小请求判定。
func (d *Doctor) checkNetwork() []CheckResult {
	var results []CheckResult
	endpoint := d.networkCheckEndpoint()

	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodHead, endpoint, nil)
	if err != nil {
		results = append(results, CheckResult{
			Category:   "Network",
			Name:       "Connectivity",
			Severity:   SeverityWarn,
			Message:    "📡 离线模式：当前 API 地址不可达，AI 对话可能不可用，本地工具仍可使用",
			Detail:     "invalid endpoint: " + err.Error(),
			FixSuggest: "检查 API Base URL、网络连接或 HTTP_PROXY/HTTPS_PROXY",
		})
		return results
	}

	resp, err := client.Do(req)
	if err != nil {
		results = append(results, CheckResult{
			Category:   "Network",
			Name:       "Connectivity",
			Severity:   SeverityWarn,
			Message:    "📡 离线模式：当前 API 地址不可达，AI 对话可能不可用，本地工具仍可使用",
			Detail:     err.Error(),
			FixSuggest: "检查 API Base URL、网络连接或 HTTP_PROXY/HTTPS_PROXY",
		})
		return results
	}
	defer resp.Body.Close()

	results = append(results, CheckResult{
		Category: "Network",
		Name:     "Connectivity",
		Severity: SeverityOK,
		Message:  "在线（已连接 " + endpoint + "）",
	})
	return results
}

// checkUpdate 检查 GitHub Release 最新版本。
// V3-1-2：为 `whale doctor --update-check` 提供独立的更新检查入口。
// 不修改 doctor.Run() 调用顺序（避免并行冲突），仅作为 CLI 子命令的专用 check。
// 行为：
// - 请求 GitHub API 获取 latest release tag_name
// - 与当前版本对比（用 golang.org/x/mod/semver）
// - 有新版本 → 返回 Info 级别提示用户下载
// - 网络失败 → 返回 Warning，不影响使用
//
// 当前版本来自 internal/version.Version（与 --version 同源；本地 dev 构建为 "dev"）。
func (d *Doctor) checkUpdate() []CheckResult {
	var results []CheckResult

	// 1. 获取当前版本
	currentVersion := getCurrentVersion()
	results = append(results, CheckResult{
		Category: "Network",
		Name:     "Current Version",
		Severity: SeverityOK,
		Message:  "v" + currentVersion,
	})

	// dev 构建无法进行版本对比，直接跳过
	// dev 构建无版本号是客观事实而非"问题"，降级为 OK 避免 Summary 出现
	// 用户找不到来源的 warning（原 SeverityWarn 会让 "1 warnings" 计入但不显示在白名单渲染中）。
	if currentVersion == "dev" {
		results = append(results, CheckResult{
			Category: "Network",
			Name:     "Update Check",
			Severity: SeverityOK,
			Message:  "跳过（dev 构建，无版本号）",
		})
		return results
	}

	// 2. 请求 GitHub API
	latestVersion, err := fetchLatestRelease()
	if err != nil {
		results = append(results, CheckResult{
			Category:   "Network",
			Name:       "Update Check",
			Severity:   SeverityWarn,
			Message:    "无法检查更新（网络不可达或 GitHub API 限流）",
			Detail:     err.Error(),
			FixSuggest: "稍后重试，或手动查看 https://github.com/anchorandfish/whale-code/releases",
		})
		return results
	}

	results = append(results, CheckResult{
		Category: "Network",
		Name:     "Latest Version",
		Severity: SeverityOK,
		Message:  "v" + latestVersion,
	})

	// 3. 版本对比（semver 要求 v 前缀）
	currentV := normalizeSemver(currentVersion)
	latestV := normalizeSemver(latestVersion)
	switch semver.Compare(currentV, latestV) {
	case -1:
		// 当前版本 < 最新版本 → 有新版本可用
		results = append(results, CheckResult{
			Category:   "Network",
			Name:       "Update Check",
			Severity:   SeverityOK, // Info 级别用 OK 表示（doctor 无 Info 严重度）
			Message:    fmt.Sprintf("🆕 新版本可用: v%s，请到 GitHub Release 下载", latestVersion),
			FixSuggest: "前往 https://github.com/anchorandfish/whale-code/releases 下载最新版本",
		})
	case 0:
		results = append(results, CheckResult{
			Category: "Network",
			Name:     "Update Check",
			Severity: SeverityOK,
			Message:  "已是最新版本",
		})
	case 1:
		results = append(results, CheckResult{
			Category: "Network",
			Name:     "Update Check",
			Severity: SeverityOK,
			Message:  fmt.Sprintf("当前版本 v%s 比最新 release v%s 更新（可能是开发版）", currentVersion, latestVersion),
		})
	}
	return results
}

// getCurrentVersion 返回当前 whale-code 版本号。
// 返回不含 v 前缀的版本号（如 "0.1.0"），dev 构建返回 "dev"。
func getCurrentVersion() string {
	return strings.TrimPrefix(versionpkg.Version, "v")
}

// fetchLatestRelease 请求 GitHub API 获取最新 release 的 tag_name。
// 超时 5 秒，返回不含 v 前缀的版本号（如 "0.2.0"）。
func fetchLatestRelease() (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://api.github.com/repos/anchorandfish/whale-code/releases/latest", nil)
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}
	// 设置 Accept header 避免 GitHub API 返回非 JSON
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 GitHub API 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API 返回非 200 状态码: %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("解析 GitHub API 响应失败: %w", err)
	}
	if release.TagName == "" {
		return "", fmt.Errorf("GitHub API 返回空 tag_name")
	}
	return strings.TrimPrefix(release.TagName, "v"), nil
}

// normalizeSemver 规范化版本号为 semver 格式（添加 v 前缀）。
// golang.org/x/mod/semver 要求版本号必须以 v 开头（如 v0.1.0）。
func normalizeSemver(version string) string {
	if version == "" {
		return "v0.0.0"
	}
	if !strings.HasPrefix(version, "v") {
		return "v" + version
	}
	return version
}

// checkInstanceLock 检测多实例并发运行情况（V3-5-3）。
// 只读检测 ~/.whale/.lock 文件，判断是否已有 whale 实例运行。
// 行为：
// - ~/.whale/.lock 不存在 → OK（无并发实例）
// - 锁文件存在但 PID 不存活（stale）→ OK（stale 锁，可被抢占）
// - 锁文件存在且 PID 存活 → Warn（多实例并发警告）
// - home 目录不可用 → Warn（跳过检测）
//
// 用于 doctor.Run() 和 `whale doctor --lock-test` CLI 子命令。
func (d *Doctor) checkInstanceLock() []CheckResult {
	whaleDir := defaultWhaleDir()
	if whaleDir == "" {
		return []CheckResult{{
			Category:   "Lock",
			Name:       "InstanceLock",
			Severity:   SeverityWarn,
			Message:    "home directory unavailable, skip instance lock check",
			FixSuggest: "set HOME or USERPROFILE environment variable",
		}}
	}

	lockPath := filepath.Join(whaleDir, ".lock")
	existingPID, err := util.CheckExistingInstance(lockPath)
	if err != nil {
		return []CheckResult{{
			Category:   "Lock",
			Name:       "InstanceLock",
			Severity:   SeverityWarn,
			Message:    "lock file read failed: " + err.Error(),
			FixSuggest: "check ~/.whale/.lock permissions",
		}}
	}

	if existingPID > 0 {
		return []CheckResult{{
			Category:   "Lock",
			Name:       "InstanceLock",
			Severity:   SeverityWarn,
			Message:    fmt.Sprintf("检测到另一个 whale 实例正在运行 (PID: %d)", existingPID),
			Detail:     "多实例并发写 ~/.whale/ 可能导致数据冲突",
			FixSuggest: "如确需多开，请确保操作不同项目；否则关闭其他实例",
		}}
	}

	return []CheckResult{{
		Category: "Lock",
		Name:     "InstanceLock",
		Severity: SeverityOK,
		Message:  "无其他并发实例（锁文件正常或已清理）",
	}}
}
