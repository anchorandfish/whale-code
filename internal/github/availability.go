// Package github — GitHub 工具的能力面装配（S3 · 裁定 C=②「工具面 = 能力面」的 GitHub 面）。
//
// 与 WebSearch 同源、同一条链路：
//
//	启动期装配（本文件，唯一"写"点）→ tools 的唯一谓词（tools.SetToolAvailability）
//
// → 三面各自"读"它：① 注册点（specs.go RegisterSpecs 不建 spec）
// ② 档位目录（toollevel.go 出口过滤，对任何已注册名字生效）
// ③ 执行门（tools.go RegisterHandlers 不注册 handler）
//
// 本文件只负责**写**，不含任何第二套判断：判据是"此刻有没有凭据"，
// 消费者只有 tools.ToolAvailable 一个出口。
//
// 修的是 （`检索联网_STATUS.md` §2.4 问题① / §3）：此前无 GITHUB_TOKEN 时
// 12 个 `github_*` 仍无条件进模型工具面，模型真去调才拿到
// `GITHUB_TOKEN environment variable not set` —— 对不存在的能力做出承诺。
package github

import (
	"os"
	"strings"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// GitTokenEnvVar 是 GitHub 凭据的**唯一**环境变量名。
//
// 全包只有这一处字面量：`NewGitHubTools`（真实读凭据的地方）与能力面装配都读它。
// 注意只认 `GITHUB_TOKEN`，**不认 `GH_TOKEN`**（`gh` CLI / 同类 CLI 两者都认）——
// 若要放宽，改这一处即可让"凭据存在性"与"客户端拿到的凭据"继续同源。
const GitTokenEnvVar = "GITHUB_TOKEN"

// ApplyToolAvailability 把「此刻有没有凭据」这一个事实写进工具面唯一谓词。
//
// 生产调用点唯一：main.go 的 `loadEnvFiles` 咽喉点（与 WebSearch 同一处装配）——
// 那是三条入口（启动 / `whale doctor` / `whale --check-api`）共用的 env 加载点，
// 放这里可**结构性**排除"新加一条入口却忘了接线 ⇒ 工具面与实际能力不一致"这类静默假修复。
func ApplyToolAvailability() {
	applyToolAvailabilityFrom(os.Getenv)
}

// applyToolAvailabilityFrom 是可注入形态：判据用固定 getenv 驱动、不依赖进程环境
// （否则判据会随开发机的 GITHUB_TOKEN 而飘，属"环境相关假绿" —— 见 webSearchChannelFrom 同款理由）。
func applyToolAvailabilityFrom(getenv func(string) string) {
	available := strings.TrimSpace(getenv(GitTokenEnvVar)) != ""
	for _, spec := range toolSpecs {
		tools.SetToolAvailability(spec.Name, available)
	}
}
