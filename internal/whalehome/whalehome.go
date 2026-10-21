// Package whalehome 是 whale 状态目录（默认 ~/.whale）的唯一权威解析点（SSOT， 方案C）。
//
// 背景（，2026-10-15）：此前状态目录路径散落在 main.go / config.go / sandbox /
// tui / tools / doctor 等 20+ 个文件里各自 `filepath.Join(home, ".whale", ...)`，
// 同语义多处实现。本包收口后，"whale 自己的状态写到哪"只有一个答案：Dir()。
//
// WHALE_HOME（对齐同类 CLI 的状态目录环境变量语义，取证见 竞品对照_Agent状态目录与沙箱可写根_W-103_2026-10-15.md）：
// - 设置后 Dir() 返回该路径 —— 状态目录整体搬家（典型场景：外层沙箱挡住 ~/.whale
// 写入时，把它搬进可写根，如工作区内或 /tmp 下）。
// - fail-fast：WHALE_HOME 一旦设置，必须存在、必须是目录、必须是绝对路径，
// 否则启动即拒（Validate，main() 最早处接线）。对齐同类 CLI 的
// "set but invalid ⇒ refuse" 语义 —— 宁可拒绝启动，也不静默写一个不可用路径
// （复刻 的"无声攒 59 天"）。
// - WHALE_HOME 只认进程环境变量，不从 ~/.whale/.env 读取：.env 本身就住在状态
// 目录里，用它的内容来定位状态目录是循环依赖。
//
// 测试注入约定：统一用 t.Setenv("WHALE_HOME", ...) 或 t.Setenv("HOME", ...)，
// 不再通过函数参数传"用户家目录"来间接决定状态目录。
package whalehome

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnvVar 是覆盖状态目录位置的环境变量名。
// ★ 对外契约字面量（用户要在 shell 里 export 的名字），有契约钉死测试
// （TestEnvVarContract）；改名必须同步文档与该测试。
const EnvVar = "WHALE_HOME"

// Dir 返回 whale 状态目录（SSOT；每次调用实时读环境，无缓存——
// 与 os.UserHomeDir 的既有调用习惯一致，也保证测试 Setenv 立即生效）。
//
// 优先级：$WHALE_HOME > $HOME/.whale。
// 返回空串表示 home 不可得（历史调用方各自处理空串，行为不变）。
func Dir() string {
	if override := os.Getenv(EnvVar); override != "" {
		return filepath.Clean(override)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// os.UserHomeDir 在 unix 上即 $HOME；显式回退一次以兼容异常 shell 环境。
		home = os.Getenv("HOME")
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".whale")
}

// Validate 对 WHALE_HOME 做 fail-fast 校验（main() 在一切 I/O 之前调用）。
//
// 规则（仅当 WHALE_HOME 非空时生效）：
// 1. 必须是绝对路径（相对路径会让状态目录随 cwd 漂移，多项目下不可预期）；
// 2. 必须存在且是目录（对齐 CODEX_HOME：set but invalid ⇒ refuse）。
//
// 未设置 ⇒ 返回 nil，默认路径行为完全不变（首次创建仍由各写入点 MkdirAll）。
func Validate() error {
	override := os.Getenv(EnvVar)
	if override == "" {
		return nil
	}
	if !filepath.IsAbs(override) {
		return fmt.Errorf("%s must be an absolute path, got %q", EnvVar, override)
	}
	fi, err := os.Stat(override)
	if err != nil {
		return fmt.Errorf("%s=%q is not accessible: %w (create the directory first, or unset %s)", EnvVar, override, err, EnvVar)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s=%q is not a directory", EnvVar, override)
	}
	return nil
}
