package doctor

import (
	"context"
	"sync"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
)

// NewDoctor 创建诊断器实例。
// cfg 只读引用配置（不修改 config 模块）；aiClient 用于连通性测试（可为 nil，标记 fatal）；
// workDir 当前工作目录；settingsPath settings.json 路径（用于检测配置文件存在性）。
// 对标同类 CLI getDoctorDiagnostic 的入口。
func NewDoctor(cfg *config.Config, aiClient ai.Client, workDir, settingsPath string) *Doctor {
	return &Doctor{
		cfg:          cfg,
		aiClient:     aiClient,
		workDir:      workDir,
		settingsPath: settingsPath,
	}
}

// Run 执行所有检测项并返回结果列表。
// 检测顺序：环境（无依赖）→ 配置 → 依赖 → 平台特定 → 数据完整性 → 多实例锁 → 资源 → 网络连通性 → API 连通性 → 更新检查。
// 对标同类 CLI getDoctorDiagnostic + checkContextWarnings 的并发采集。
// 性能修复：将网络类检查（network/update/api）改为并行执行，
// 原串行最多 3s+30s+5s=38s，并行后上限降至 max(30s, 5s, 3s)=30s；
// 同时 checkPlatform 内部 buildProbeBinary 加 10s 超时，checkRecovery 加 5s 超时。
// 本地检查保持串行（无网络依赖，避免并发副作用），仅网络类并行。
// V3-5-1/V3-4-1/V3-1-2：新增数据完整性检测、网络检测、更新检查（仅 Info 级，不阻塞）。
// V3-5-3/V3-4-2：新增多实例锁检测、低资源警告检测（v1.0 轻量版，仅检测+警告）。
func (d *Doctor) Run(ctx context.Context) []CheckResult {
	var results []CheckResult
	// 1. 环境检测（无依赖，最先跑）
	results = append(results, d.checkEnvironment()...)
	// 2. 配置检测（settings.json/API Key/Model/permission_mode/context_window）
	results = append(results, d.checkConfig()...)
	// 3. 依赖检测（git/gcc/cgo）
	results = append(results, d.checkDependencies()...)
	// 4. 平台特定检测（Windows DLP/Winsock + Linux inotify）
	// buildProbeBinary 内部已加 10s 超时
	results = append(results, d.checkPlatform()...)
	// 5. 数据完整性检测（V3-5-1：扫描 ~/.whale/ 损坏 JSON 文件，只读检测）
	// scanCorruptedJSON 内部已加 5s 超时
	results = append(results, d.checkRecovery()...)
	// 5.5 状态目录可写性探针（方案A：真实 写→改名→删，把 的无声 EPERM 变可见）
	results = append(results, d.checkStateDirWritable()...)
	// 6. 多实例锁检测（V3-5-3：警告式，检测 ~/.whale/.lock）
	results = append(results, d.checkInstanceLock()...)
	// 7. 资源检测（V3-4-2：低资源警告，内存/CPU/磁盘）
	results = append(results, d.checkResource()...)

	// 8-10. 网络类检测并行执行（性能修复）
	// 原串行最多 3s(network) + 30s(api) + 5s(update) = 38s，并行后上限降至 max(30s, 5s, 3s) = 30s
	// 三者之间无共享状态，可安全并行；结果按 network → api → update 顺序追加以保持原输出顺序
	var (
		netResults    []CheckResult
		apiResults    []CheckResult
		updateResults []CheckResult
		wg            sync.WaitGroup
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		netResults = d.checkNetwork()
	}()
	go func() {
		defer wg.Done()
		apiResults = d.checkAPI(ctx)
	}()
	go func() {
		defer wg.Done()
		updateResults = d.checkUpdate()
	}()
	wg.Wait()

	results = append(results, netResults...)
	results = append(results, apiResults...)
	results = append(results, updateResults...)
	return results
}

// HasFatal 判断检测结果中是否含致命错误（阻塞使用）。
// 用于 /doctor 命令和 --check-api CLI 入口的退出码判断。
func (d *Doctor) HasFatal(results []CheckResult) bool {
	for _, r := range results {
		if r.Severity == SeverityFatal {
			return true
		}
	}
	return false
}

// CheckNetwork 公开包装方法，执行网络连通性单项检测。
// 为 `whale doctor --network` CLI 子命令提供入口，避免外部包直接调用私有 checkNetwork。
// 行为详见 checkNetwork（V3-4-1）。
func (d *Doctor) CheckNetwork() []CheckResult {
	return d.checkNetwork()
}

// CheckUpdate 公开包装方法，执行 GitHub Release 更新检查单项检测。
// 为 `whale doctor --update-check` CLI 子命令提供入口，避免外部包直接调用私有 checkUpdate。
// 行为详见 checkUpdate（V3-1-2）。
func (d *Doctor) CheckUpdate() []CheckResult {
	return d.checkUpdate()
}

// CheckConfigMigrate 公开包装方法，执行配置文件版本迁移单项检测。
// 为 `whale doctor --config-migrate` CLI 子命令提供入口，避免外部包直接调用私有 checkConfigMigrate。
// 行为详见 checkConfigMigrate（V3-1-3）：检测 settings.json 版本，落后时备份 + 迁移。
func (d *Doctor) CheckConfigMigrate() []CheckResult {
	return d.checkConfigMigrate()
}

// CheckInstanceLock 公开包装方法，执行多实例锁单项检测。
// 为 `whale doctor --lock-test` CLI 子命令提供入口，避免外部包直接调用私有 checkInstanceLock。
// 行为详见 checkInstanceLock（V3-5-3）：只读检测 ~/.whale/.lock，判断是否已有实例运行。
func (d *Doctor) CheckInstanceLock() []CheckResult {
	return d.checkInstanceLock()
}

// CheckResource 公开包装方法，执行系统资源单项检测。
// 为 `whale doctor --resource` CLI 子命令提供入口，避免外部包直接调用私有 checkResource。
// 行为详见 checkResource（V3-4-2）：检测内存/CPU/磁盘，低资源时返回警告 + 降级建议。
func (d *Doctor) CheckResource() []CheckResult {
	return d.checkResource()
}
