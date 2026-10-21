// ask_threshold_presence_test.go —— 提问阈值「存在性」回归判据。
//
// 缺陷（2026-10-06 实锤， 取证）：
//
//	settings.json 里的 interaction 块被 Save 静默删除后，LoadAskSettings 用 int 反序列化 ——
//	「块缺失」「键缺失」与「显式写 0」三者得到同一个值 0，而 0 的语义是**完全禁用提问**
//	⇒ tool_exec.go:555 / :834 的拦截条件 `askCount(0) >= AskThreshold(0)` **恒真**
//	⇒ 模型每次调用 AskUser 都被静默跳过，面板永不弹（用户根本没机会作答）。
//
// 本文件钉住的不变量（与"谁改的"无关）：
//
//	**「键不存在」必须回落到默认值 1；只有「显式写 0」才是禁用提问。**
//
// 判别性：修复前两条 FallsBackToDefault 用例都会得到 0（= 禁用），故**必然红** ——
// 它们是本缺陷的直接判别器。既有 ask_settings_test.go 只覆盖了「文件不存在」这一格，
// 漏掉的正是「文件在、interaction 块不在」——即缺陷真实发生的形态。
//
// 配对反证（R4-12 成因①「夹具坍缩」的对策）：只跑"缺失 ⇒ 1"会把"永远返回 1"的劣化实现
// 误判为修好；故本文件同时钉住"显式 0 仍是 0"。两条同时成立才算证明。
package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/whalehome"
)

// writeRawSettingsJSON 在本测试独占的状态目录写入任意 settings.json 正文（夹具）。
//
// ★ 必须逐测试隔离（WHALE_HOME → t.TempDir）：此前直接写 TestMain 那个**包级共享** HOME，
// 且写完不还原。结果是本文件的 TestW038_ExplicitZero_StillDisables 把 ask_threshold=0
// 留在共享目录里，随后所有新建 Model 的测试（askuser_free_text / typing / answer_note 等）
// 读到阈值 0 ⇒ checkAskUserChoice 恒返回 false ⇒ 集体「前置失败」。
// 这是**测试间状态污染**（顺序相关 flaky），不是产品缺陷：单独跑那些用例是绿的。
// 用 WHALE_HOME 隔离后，每个用例自带一份 settings.json，读与写都被限制在 TempDir 内。
func writeRawSettingsJSON(t *testing.T, body map[string]interface{}) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(whalehome.EnvVar, dir)

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
}

// TestW038_MissingInteractionBlock_FallsBackToDefault 是 缺陷的**直接判别器**。
//
// 形态：settings.json 存在、其他键都在，但没有 interaction 块（= 被 Save 抹掉后的真实形态）。
// 期望：回落到默认 1。修复前：得 0 ⇒ 提问被彻底禁用 ⇒ 本用例红。
func TestW038_MissingInteractionBlock_FallsBackToDefault(t *testing.T) {
	writeRawSettingsJSON(t, map[string]interface{}{
		"model":               "some-model",
		"permission_mode":     "workspace-write",
		"auto_memory_enabled": true,
		"compact_archive":     true,
	})

	s := LoadAskSettings()
	if s.AskThreshold != AskThresholdDefault {
		t.Errorf(": interaction 块缺失（= 被保存抹掉后的形态）时必须回落到默认值 %d；"+
			"实得 %d。若为 0 ⇒ 复现「AskUser 永不弹面板」缺陷（tool_exec.go:555 的 "+
			"askCount >= AskThreshold 恒真，模型调用被静默跳过）。",
			AskThresholdDefault, s.AskThreshold)
	}
}

// TestW038_MissingAskThresholdKey_FallsBackToDefault 覆盖同一格的第二形态：
// interaction 块在，但没写 ask_threshold 键（手写配置的常见形态）。
// 期望同样是默认 1；修复前为 0。
func TestW038_MissingAskThresholdKey_FallsBackToDefault(t *testing.T) {
	writeRawSettingsJSON(t, map[string]interface{}{
		"interaction": map[string]interface{}{},
	})

	s := LoadAskSettings()
	if s.AskThreshold != AskThresholdDefault {
		t.Errorf(": interaction 存在但 ask_threshold 键缺失时必须回落到默认值 %d；实得 %d。"+
			"（「键不存在」与「显式写 0」是两件不同的事，不得共用零值判定）",
			AskThresholdDefault, s.AskThreshold)
	}
}

// TestW038_ExplicitZero_StillDisables —— 配对反证 / 防过度修复。
//
// 显式写 0 是「完全禁用日常对话提问」的合法用户意图（ask_settings.go 注释与
// ask_settings_test.go:TestLoadAskSettings_ValidZero 均已声明）。本条防止把修复做成
// "只要读到 0 就当成未设置" —— 那会把用户主动关掉提问的配置吞掉。
// 注：本条在修复前后都应保持绿，它的作用是**限定修复方向**，不承担判别性。
func TestW038_ExplicitZero_StillDisables(t *testing.T) {
	writeRawSettingsJSON(t, map[string]interface{}{
		"interaction": map[string]interface{}{"ask_threshold": 0},
	})

	s := LoadAskSettings()
	if s.AskThreshold != 0 {
		t.Errorf(": 显式 ask_threshold=0（用户意图=禁用提问）必须保持 0；实得 %d。"+
			"（若要为 1 ⇒ 用户显式关闭提问的配置被忽略，属过度修复）", s.AskThreshold)
	}
}
