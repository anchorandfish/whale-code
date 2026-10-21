package onboarding

import (
	"context"
	"fmt"
	"time"

	"github.com/anchorandfish/whale-code/internal/doctor"
	"github.com/anchorandfish/whale-code/internal/util"
)

// RunStepEnvironment 步骤 1：环境检测（复用 N17 doctor.Run）。
// doctor 为 nil 时安全跳过（不阻塞 Onboarding），对标改进方案 §1.5 与 N17 依赖降级处理。
// fatal 项阻塞继续并显示修复建议；warn 项仅提示但不阻塞。
// 对标同类 CLI Onboarding.tsx 的 preflight 步骤。
func (o *Onboarding) RunStepEnvironment(state *OnboardingState) error {
	// doctor 未初始化时跳过（N17 未实施或 aiClient 为 nil 场景）
	if o.doctor == nil {
		fmt.Println(util.Bi("  (skipped: doctor not initialized)", "  (跳过：doctor 未初始化)"))
		return nil
	}

	// 30 秒超时防止检测卡死（doctor.Run 内部含 API 连通性检测，可能较慢）
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results := o.doctor.Run(ctx)

	// 分类 fatal 和 warn（error 归入 fatal 一起展示，因为 error 也影响核心功能）
	// state.FatalErrors 用于渲染修复建议；fatal 阻塞判断复用 doctor.HasFatal()
	var fatals, warns []doctor.CheckResult
	for _, r := range results {
		switch r.Severity {
		case doctor.SeverityFatal, doctor.SeverityError:
			fatals = append(fatals, r)
		case doctor.SeverityWarn:
			warns = append(warns, r)
		}
	}
	state.FatalErrors = fatals

	// 复用 doctor.HasFatal() 判断是否阻塞（审查 v4 修订项 2：避免手动迭代判断）
	if o.doctor.HasFatal(results) {
		fmt.Print(o.doctor.Render(results))
		return fmt.Errorf("environment check found fatal errors — fix them and re-run /onboarding")
	}

	// warn 时提示但继续
	if len(warns) > 0 {
		fmt.Println(util.BiSame("  ⚠️  Environment check found %d warnings — fix recommended, not blocking",
			"  ⚠️  环境检测发现 %d 项警告，建议修复但不阻塞 Onboarding", len(warns)))
	} else {
		fmt.Println(util.Bi("  ✅ Environment check passed", "  ✅ 环境检测通过"))
	}
	return nil
}
