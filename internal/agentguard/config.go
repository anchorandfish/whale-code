// Package agentguard — Agent 防护体系 P0（失控循环治理，2026-08-31 移交文档）。
//
// 两大同类 CLI 高成本事故（936 轮 / 1,308 轮零干预）的根因是"控制流无出口"：
// agent loop 无轮次上限、"先问我"是软建议、自我审查无终止条件。本包落地
// 五道防线（均走 TDD）：
//
//	P0-1 轮次上限：单次用户请求内模型调用轮次上限（默认 40，
//	     config max_turns_per_request / env WHALE_MAX_TURNS_PER_REQUEST）。
//	     到限暂停交回控制权并输出结构化进度快照（非报错退出）。
//	P0-2 无进展熔断：连续 3 轮工具调用全失败 → 停下问用户。
//	P0-3 审查子循环提醒：连续纯读轮（无文件改动）达到阈值时注入软提醒，
//	     **不停机**（信号「无外部效果」只是空转的有偏代理，误报代价高；
//	     不可靠信号不得赋予不可逆权力）。强制停机交给 P0-1 与 P0-4。
//	P0-4 进度检测器：收敛信号（失败测试数单调下降/改动文件数稳定）与
//	     空转信号（输出高度相似/同文件重读超阈值/同一工具 + 同一参数
//	     反复调用超阈值/diff 震荡）。
//	     自主模式（WHALE_AUTONOMY=full）下到限自动测进度：收敛则静默
//	     续跑下一预算段，空转才停；默认自主模式关闭（P0-5）。
//	P0-5 自主模式插口：WHALE_AUTONOMY=full 预留，默认关闭。
//	     顺序不可反：先边界后自动化，否则自动放大的是失控。
package agentguard

import "os"

// EnvAutonomy 是自主模式的环境变量插口（P0-5）。
const EnvAutonomy = "WHALE_AUTONOMY"

// Config P0 防护体系配置。
type Config struct {
	// MaxTurns 单次用户请求内模型调用轮次上限（P0-1，默认 40）。
	MaxTurns int
	// MaxConsecutiveToolFailures 连续"整轮工具全失败"熔断阈值（P0-2，默认 3）。
	MaxConsecutiveToolFailures int
	// MaxReviewRounds 连续纯读轮（无文件改动）**软提醒间隔**（P0-3，默认 8）。
	// 达到即注入一条提醒并重新计数；不再触发停机（停机归 P0-1 轮次 / P0-4 重复检测）。
	// 值取得比旧阈值 3 大：提醒误报代价低，但也不该频繁打扰模型。
	MaxReviewRounds int
	// MaxSegments 自主模式下收敛续跑的最大预算段数（P0-4，默认 3，防无限续跑）。
	MaxSegments int
	// Autonomy 自主模式（P0-5）："full" 开启静默续跑，其余值（含空）关闭。
	Autonomy string
}

// DefaultConfig 返回 P0 防护默认配置。
// MaxTurns=40 与 config.DefaultConfig().MaxTurnsPerRequest 保持一致
// （文档建议 30~50，取 40）；MaxSegments=3 意味着自主模式下单请求最多
// 40×(1+3)=160 轮后必然停机。
func DefaultConfig() Config {
	return Config{
		MaxTurns:                   40,
		MaxConsecutiveToolFailures: 3,
		MaxReviewRounds:            8,
		MaxSegments:                3,
	}
}

// ConfigFromEnv 在 base 之上应用环境变量覆盖（当前仅 WHALE_AUTONOMY）。
// 轮次上限的 env 覆盖（WHALE_MAX_TURNS_PER_REQUEST）由 config 包收口后
// 经 base.MaxTurns 传入，避免两处读同一 env。
func ConfigFromEnv(base Config) Config {
	base.Autonomy = os.Getenv(EnvAutonomy)
	return base
}

// AutonomyFull 报告自主模式是否开启（仅认小写 "full"）。
func (c Config) AutonomyFull() bool {
	return c.Autonomy == "full"
}
