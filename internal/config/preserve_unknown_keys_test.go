// preserve_unknown_keys_test.go —— 「保存不得删除未建模键」回归判据。
//
// 缺陷形态（一句话）：Save 把内存里的 *Config 整份序列化后覆盖写回 ⇒
// **结构体没有建模的键被静默删除**。
//
// 实锤（取证）：2026-10-06 16:48:56 一次保存把 `interaction` 块抹掉
// （同一时刻的备份 .20261006-164856.bak 里它还在），此后 tui.LoadAskSettings
// 把它读成 0 ⇒ 「模型要问用户」这件事被静默关掉，面板永不弹。
//
// 当时的补丁 preserveAuthFields 只挑了 4 个 auth 键进白名单 —— 默认仍是「丢弃」，
// 所以任何**别的子系统读的键**（如 tui 读的 interaction）都会再被吃一次，
// 这正是它一周内复发一次的原因。
//
// 本文件钉住的不变量（**与键叫什么无关**）：
//
//	Save / SaveToFile 必须保留「磁盘上存在、而结构体未声明」的键，对嵌套对象递归。
//
// 判别性：修复前 3 条 preserves 用例全红（键被删），另 2 条方向约束用例保持绿 ——
// 后者限定修复方向，不承担判别性（已在各自注释里标明）。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// seedSettingsBody 在隔离目录写一份 settings.json，返回其绝对路径（夹具）。
// 与既有 S12 夹具同构：userDir/.whale/settings.json。
func seedSettingsBody(t *testing.T, dir, body string) string {
	t.Helper()
	whaleDir := filepath.Join(dir, "user", ".whale")
	// 收口：Loader.Save / Load 的目标目录从 whalehome.Dir()（$WHALE_HOME > $HOME/.whale）
	// 解析，不再取 l.userDir ⇒ 只把夹具写进 dir 已不构成隔离。
	// 隔离在此处一次到位（本文件全部用例都经此 helper 造夹具），
	// 否则 Save 会落到宿主的**真实** ~/.whale/settings.json（实测覆写用户 model / api_key）。
	t.Setenv("WHALE_HOME", whaleDir)
	if err := os.MkdirAll(whaleDir, 0700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	p := filepath.Join(whaleDir, "settings.json")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	return p
}

// readSettingsMap 读回 settings.json 顶层 map。
func readSettingsMap(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("保存后的 settings.json 不是合法 JSON: %v\n%s", err, string(data))
	}
	return raw
}

// nestedValue 取 raw[k1][k2]（k1 为对象时）。
// 返回值保留 json.Unmarshal 的原始类型（数字是 float64），故数字比较走 asInt，别用字符串断言。
func nestedValue(t *testing.T, raw map[string]interface{}, k1, k2 string) (interface{}, bool) {
	t.Helper()
	obj, ok := raw[k1].(map[string]interface{})
	if !ok {
		return nil, false
	}
	v, ok := obj[k2]
	return v, ok
}

// asInt 把 JSON 数字（float64）归一为 int64 便于比较。
func asInt(v interface{}) (int64, bool) {
	if f, ok := v.(float64); ok {
		return int64(f), true
	}
	return 0, false
}

// TestW038_Save_PreservesUnknownTopLevelKey 是本缺陷的**直接判别器**（真实键 interaction）。
//
// 修复前：保存后 `interaction` 整个消失 ⇒ 红。
func TestW038_Save_PreservesUnknownTopLevelKey(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	t.Setenv("WHALE_HOME", filepath.Join(dir, "user", ".whale"))
	p := seedSettingsBody(t, dir,
		`{"interaction":{"ask_threshold":2},"permission_mode":"read-only"}`)

	cfg := DefaultConfig()
	cfg.PermissionMode = PermWorkspaceWrite // 一次普通的配置保存（如 /model、/permissions）

	l := NewLoader(filepath.Join(dir, "user"), "")
	if err := l.Save(&cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	raw := readSettingsMap(t, p)
	if _, ok := raw["interaction"]; !ok {
		t.Fatalf(": Save 删除了结构体未建模的键 interaction —— 这正是「AskUser 永不弹面板」"+
			"的根因（tui.LoadAskSettings 随后把它读成 0 = 禁用提问）。\n"+
			"修复方向：Save 必须保留磁盘上存在、而 Config 未声明的键（读-改-写），\n"+
			"而不是把内存结构体整份覆盖写回。\n保存后的键：%v", keysOf(raw))
	}
	av, ok := nestedValue(t, raw, "interaction", "ask_threshold")
	if !ok {
		t.Fatalf(": interaction 保留了但 ask_threshold 子键丢失（嵌套未递归保留）")
	}
	if n, isNum := asInt(av); !isNum || n != 2 {
		t.Errorf(": interaction.ask_threshold 应保真为 2，实得 %v", raw["interaction"])
	}
	// 结构体声明过的键必须仍然被写入（防止"为了保留旧值而整体不写"这种劣化修复）
	if raw["permission_mode"] != string(PermWorkspaceWrite) {
		t.Errorf(": 结构体声明的 permission_mode 应按新值写入 %q，实得 %v",
			PermWorkspaceWrite, raw["permission_mode"])
	}
}

// TestW038_Save_PreservesUnknownNestedKey 钉住"对嵌套对象递归保留"。
//
// 为什么必须递归：同一族缺陷在下一层会照样复发 —— approval / hooks / mcp_servers
// 这类**已被建模的对象**内部，同样可能有结构体不认识的子键（例如 mcp_servers.<名>.env）。
// 只做顶层保留 = 只修了一半。
func TestW038_Save_PreservesUnknownNestedKey(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	p := seedSettingsBody(t, dir,
		`{"approval":{"policy":"on-request","custom_extra":"keep-me"},"permission_mode":"read-only"}`)

	cfg := DefaultConfig()
	l := NewLoader(filepath.Join(dir, "user"), "")
	if err := l.Save(&cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	raw := readSettingsMap(t, p)
	if _, ok := raw["approval"]; !ok {
		t.Fatalf(": approval 整体丢失；保存后的键：%v", keysOf(raw))
	}
	got, ok := nestedValue(t, raw, "approval", "custom_extra")
	if !ok || got != "keep-me" {
		t.Errorf(": approval.custom_extra 是结构体未建模的子键，必须递归保留；实得 %v\n"+
			"（只做顶层保留会让同一缺陷在下一层复发）", raw["approval"])
	}
}

// TestW038_SaveToFile_PreservesUnknownKey 覆盖**第二条生产保存路径**。
//
// 为什么必须单独判：SaveToFile 被 /output-style（tui/runner_core.go:1294）与
// bridge（tui/bridge.go:191/202）直接使用，且**它此前连 preserveAuthFields 白名单都没有**
// ⇒ 是比 Loader.Save 更漏的一条。两条路径必须由同一个函数供能，不能各修各的。
func TestW038_SaveToFile_PreservesUnknownKey(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	p := seedSettingsBody(t, dir,
		`{"interaction":{"ask_threshold":3},"permission_mode":"read-only"}`)

	cfg := DefaultConfig()
	if err := SaveToFile(&cfg, p); err != nil {
		t.Fatalf("SaveToFile failed: %v", err)
	}

	raw := readSettingsMap(t, p)
	if _, ok := raw["interaction"]; !ok {
		t.Fatalf(": SaveToFile（/output-style、bridge 的生产路径）删除了 interaction。\n"+
			"该路径此前连 auth 白名单都没有 ⇒ 必须与 Loader.Save 共用同一套保留逻辑。\n"+
			"保存后的键：%v", keysOf(raw))
	}
	av, _ := nestedValue(t, raw, "interaction", "ask_threshold")
	if n, isNum := asInt(av); !isNum || n != 3 {
		t.Errorf(": SaveToFile 应保留 interaction.ask_threshold=3，实得 %v", raw["interaction"])
	}
}

// TestW038_Save_StructStillWins —— 方向约束（防"过度保留"）。
//
// 保留未知键**不得**退化成"无条件保留磁盘旧值"：结构体声明过的键必须以新值为准，
// 否则 /permissions、/model 这类修改会被旧值覆盖回去。
// 注：本条在修复前后都应保持绿（不承担判别性），它的价值是**限定修复方向**。
func TestW038_Save_StructStillWins(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	t.Setenv("WHALE_HOME", filepath.Join(dir, "user", ".whale"))
	p := seedSettingsBody(t, dir, `{"permission_mode":"read-only"}`)

	cfg := DefaultConfig()
	cfg.PermissionMode = PermDangerFullAccess

	l := NewLoader(filepath.Join(dir, "user"), "")
	if err := l.Save(&cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	raw := readSettingsMap(t, p)
	if raw["permission_mode"] != string(PermDangerFullAccess) {
		t.Errorf(": 结构体声明过的键必须被新值覆盖：想要 %q，实得 %v"+
			"（保留未知键 ≠ 无条件保留旧值）", PermDangerFullAccess, raw["permission_mode"])
	}
}

// TestW038_Save_DoesNotInventKeys —— 方向约束（防"把 interaction 硬塞进 DefaultConfig"）。
//
// 如果修复被做成"在默认配置里凭空写出 interaction:{ask_threshold:1}"，
// 那么一份从未有过该键、且用户并不想要提问的配置会被悄悄改写。
// 正解是**保留已有的**，不是**发明没有的**。
// 注：本条在修复前后都应保持绿（不承担判别性）。
func TestW038_Save_DoesNotInventKeys(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	p := seedSettingsBody(t, dir, `{"permission_mode":"read-only"}`)

	cfg := DefaultConfig()
	l := NewLoader(filepath.Join(dir, "user"), "")
	if err := l.Save(&cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	raw := readSettingsMap(t, p)
	if _, ok := raw["interaction"]; ok {
		t.Errorf(": 磁盘上本来没有 interaction，保存不得凭空发明该键（实得 %v）。"+
			"修复方向是「保留磁盘已有的未知键」，不是「在默认配置里补齐它们」。", raw["interaction"])
	}
}

// keysOf 返回 map 的键列表（仅用于失败信息，便于一眼看出丢了什么）。
func keysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
