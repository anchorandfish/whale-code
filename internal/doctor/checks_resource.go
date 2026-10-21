package doctor

import (
	"fmt"
	"runtime"
)

// checkResource 检测系统资源（内存/CPU/磁盘），低资源时警告（V3-4-2 v1.0 轻量版）。
// v1.0 轻量版：仅检测+警告+配置项，不做模块降级接线（v1.1 再做）。
//
// 检测项：
//   - 总物理内存 + 可用内存（跨平台：Windows GlobalMemoryStatusEx / Linux /proc/meminfo）
//   - CPU 核数（runtime.NumCPU）
//   - ~/.whale/ 所在磁盘剩余空间（跨平台：Windows GetDiskFreeSpaceEx / Linux syscall.Statfs）
//
// 阈值：
//   - 低内存（总内存 <2GB 或 可用内存 <512MB）→ Warn + 降级建议
//   - 低磁盘（剩余 <100MB）→ Warn
//   - 低 CPU（<2 核）→ OK（Info 级，doctor 无 Info 严重度，用 OK 表示）
//
// 降级策略：检测失败（如 syscall 不支持）→ 跳过该项检测，不报错。
// 用于 doctor.Run() 和 `whale doctor --resource` CLI 子命令。
func (d *Doctor) checkResource() []CheckResult {
	var results []CheckResult

	// 1. 内存检测
	results = append(results, d.checkMemory()...)

	// 2. CPU 检测
	results = append(results, d.checkCPU()...)

	// 3. 磁盘检测（~/.whale/ 所在磁盘）
	results = append(results, d.checkDisk()...)

	return results
}

// checkMemory 检测系统内存，低内存时返回警告 + 降级建议。
// 阈值：总内存 <2GB 或 可用内存 <512MB → Warn。
func (d *Doctor) checkMemory() []CheckResult {
	totalMB, availMB, err := getMemoryInfo()
	if err != nil {
		// 检测失败时跳过，不报错（降级策略）
		return []CheckResult{{
			Category: "Resource",
			Name:     "Memory",
			Severity: SeverityOK,
			Message:  "内存检测跳过（系统不支持）",
			Detail:   err.Error(),
		}}
	}

	// 低内存判定
	const lowTotalThresholdMB = 2048 // 2GB
	const lowAvailThresholdMB = 512  // 512MB
	if totalMB < lowTotalThresholdMB || availMB < lowAvailThresholdMB {
		return []CheckResult{{
			Category:   "Resource",
			Name:       "Memory",
			Severity:   SeverityWarn,
			Message:    fmt.Sprintf("低内存：总 %dMB / 可用 %dMB", totalMB, availMB),
			Detail:     fmt.Sprintf("阈值：总<%dMB 或 可用<%dMB", lowTotalThresholdMB, lowAvailThresholdMB),
			FixSuggest: "建议在 ~/.whale/settings.json 中设置 resource.low_memory=true 启用低资源模式",
		}}
	}

	return []CheckResult{{
		Category: "Resource",
		Name:     "Memory",
		Severity: SeverityOK,
		Message:  fmt.Sprintf("总 %dMB / 可用 %dMB", totalMB, availMB),
	}}
}

// checkCPU 检测 CPU 核数，低 CPU 时返回信息提示。
// 阈值：<2 核 → OK（Info 级，doctor 无 Info 严重度，用 OK 表示）。
func (d *Doctor) checkCPU() []CheckResult {
	cpuCores := runtime.NumCPU()
	if cpuCores < 2 {
		return []CheckResult{{
			Category:   "Resource",
			Name:       "CPU",
			Severity:   SeverityOK,
			Message:    fmt.Sprintf("CPU 核数较低：%d 核", cpuCores),
			FixSuggest: "低 CPU 环境下建议在 ~/.whale/settings.json 中设置 resource.low_memory=true",
		}}
	}

	return []CheckResult{{
		Category: "Resource",
		Name:     "CPU",
		Severity: SeverityOK,
		Message:  fmt.Sprintf("%d 核", cpuCores),
	}}
}

// checkDisk 检测 ~/.whale/ 所在磁盘剩余空间，低磁盘时返回警告。
// 阈值：剩余 <100MB → Warn。
func (d *Doctor) checkDisk() []CheckResult {
	whaleDir := defaultWhaleDir()
	if whaleDir == "" {
		return []CheckResult{{
			Category:   "Resource",
			Name:       "Disk",
			Severity:   SeverityWarn,
			Message:    "home directory unavailable, skip disk check",
			FixSuggest: "set HOME or USERPROFILE environment variable",
		}}
	}

	freeMB, err := getDiskInfo(whaleDir)
	if err != nil {
		// 检测失败时跳过，不报错（降级策略）
		return []CheckResult{{
			Category: "Resource",
			Name:     "Disk",
			Severity: SeverityOK,
			Message:  "磁盘检测跳过（系统不支持）",
			Detail:   err.Error(),
		}}
	}

	const lowDiskThresholdMB = 100 // 100MB
	if freeMB < lowDiskThresholdMB {
		return []CheckResult{{
			Category:   "Resource",
			Name:       "Disk",
			Severity:   SeverityWarn,
			Message:    fmt.Sprintf("磁盘空间不足：~/.whale/ 剩余 %dMB", freeMB),
			Detail:     fmt.Sprintf("阈值：剩余<%dMB", lowDiskThresholdMB),
			FixSuggest: "清理 ~/.whale/ 下的旧会话/日志文件，或扩展磁盘空间",
		}}
	}

	return []CheckResult{{
		Category: "Resource",
		Name:     "Disk",
		Severity: SeverityOK,
		Message:  fmt.Sprintf("~/.whale/ 剩余 %dMB", freeMB),
	}}
}
