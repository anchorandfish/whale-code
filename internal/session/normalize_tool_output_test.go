package session

import "testing"

// TestNormalizeToolOutput 是「空输出 ⇒ 占位」的**函数语义**判据（表驱动）。
//
// 定位（R2）：**这不是生产入口判据** —— 生产入口判据在
// `internal/tui/empty_tool_output_test.go`（经 `Model.handleToolResults` 覆盖
// 回传面与显示面两条接线）。本条只钉住这个函数自身的语义边界，尤其是
// **失败态必须原样透传**：不得被顺手改成"任何空输出都填占位"——
// 那会让"命令失败但没给原因"看起来像"命令成功但没输出"。
//
// 判别性：把函数体改成直接 `return output`（去掉兜底）⇒ 前两行必红、后三行保持绿。
func TestNormalizeToolOutput(t *testing.T) {
	cases := []struct {
		name    string
		output  string
		isError bool
		want    string
	}{
		{"成功+空串 ⇒ 占位", "", false, EmptyToolOutputPlaceholder},
		{"成功+纯空白 ⇒ 占位", "  \n\t ", false, EmptyToolOutputPlaceholder},
		{"成功+有内容 ⇒ 原样", "hi\n", false, "hi\n"},
		{"失败+空串 ⇒ 原样（不替失败编文案）", "", true, ""},
		{"失败+有内容 ⇒ 原样", "boom", true, "boom"},
	}
	for _, c := range cases {
		if got := NormalizeToolOutput(c.output, c.isError); got != c.want {
			t.Errorf("%s: NormalizeToolOutput(%q, %v) = %q, want %q",
				c.name, c.output, c.isError, got, c.want)
		}
	}
}
