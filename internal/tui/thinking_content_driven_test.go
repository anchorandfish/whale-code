package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestThinkingRenderIsContentDriven 断言「流式期间是否渲染思考内容」由「思考内容有没有到达」
// 决定，而不是由客户端开关 ThinkingEnabled() 决定。
//
// 背景（2026-09-24 业界对照复核，见仓库根
// thinking开关语义_业界对照与第一性原理复核_2026-09-24.md）：
// - 服务端在首字 0.47s 起就流式下发 reasoning_content（累计 1689 字符，实测）；
// - ai 侧已解析（adapter_openai.go:155）→ 事件侧已【无条件】累积（stream.go:672-677）；
// - 但渲染门槛是 ThinkingEnabled()（stream.go:803/836/851），开关为 false 时内容被挡住，
// 用户只看到 "AI is thinking..." + spinner 干等 ⇒ 这就是「卡」的直接来源。
// - 同类 CLI（只渲染服务端回传的 reasoning summary）/ 同类 CLI（有 thinking block 即渲染）
// 均由「内容是否到达」驱动，不看开关。
//
// 判别力：改前第一条子测试必红（内容被门槛挡住），改后绿。
func TestThinkingRenderIsContentDriven(t *testing.T) {
	// 生产契约 app.go:1156：viewport.Width = 终端宽 - 4（此处模拟终端 120）。
	const viewportWidth = 116

	newStreamingModel := func(t *testing.T) *Model {
		t.Helper()
		m := newTUIRegressionModel(t)
		m.reassignServices()
		m.uiService.Viewport().Width = viewportWidth
		m.state = StateStreaming
		// thinking 保持「未指定」（nil）——模拟 ~/.whale/settings.json 未配置
		// thinking_enabled 的情形；渲染仍必须由内容驱动，与开关无关。
		m.sessionService.SetThinkingEnabled(nil)
		m.activeCell = NewActiveCell()
		return m
	}

	t.Run("开关关闭时思考内容仍应可见（内容驱动）", func(t *testing.T) {
		m := newStreamingModel(t)
		m.activeCell.AppendThinking("我在核对写入路径")
		m.renderStreamContent()

		content := ansi.Strip(m.viewportContent)
		if !strings.Contains(content, "我在核对写入路径") {
			t.Fatalf("开关关闭时思考内容仍应可见（内容驱动）\n实际输出：\n%s", content)
		}
	})

	t.Run("无思考内容时保留等待提示", func(t *testing.T) {
		m := newStreamingModel(t)
		// 既无 thinking 也无 text：此时才该显示等待提示。
		m.renderStreamContent()

		content := ansi.Strip(m.viewportContent)
		// ★ 2026-10-04 措辞同步（现象②）：等待文案由 "AI is thinking..." 升级为
		// "Sent, waiting for response...（+已等待秒数）"。
		// 本条断言的**语义未变**（「无思考内容时应保留等待提示」），变的只是主行措辞——
		// 钉住新主行里的稳定子串，不钉秒数（秒数由 waiting_indicator_test.go 单独判）。
		// 详见 内部工作回执 §1。
		if !strings.Contains(content, "Sent, waiting for response") {
			t.Fatalf("无思考内容时应保留等待提示\n实际输出：\n%s", content)
		}
	})

	t.Run("思考与正文并存时两者都出现", func(t *testing.T) {
		m := newStreamingModel(t)
		m.activeCell.AppendThinking("先看目录结构")
		m.activeCell.AppendText("我来分析一下")
		m.renderStreamContent()

		content := ansi.Strip(m.viewportContent)
		if !strings.Contains(content, "先看目录结构") {
			t.Fatalf("思考内容应可见\n实际输出：\n%s", content)
		}
		if !strings.Contains(content, "我来分析一下") {
			t.Fatalf("正文应可见\n实际输出：\n%s", content)
		}
	})
}
