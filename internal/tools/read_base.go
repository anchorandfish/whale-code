package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
)

// 会话级「读基准」登记表。
//
// 根因定位：edit_file / apply_unified_diff 都以**盘上现值**为基准（内容锚定），
// 唯独 write_file 以**模型记忆**为基准 —— 它是唯一能凭几轮前的旧认知整篇盖掉
// 他人写入的工具（并行会话 / cron 会话 / 用户手工编辑）。
//
// 本登记表是 同类 CLI readFileState 的同构物：模型经 read_file 读过的文件，
// 服务端**自动**登记盘上内容的指纹；write_file 落盘前自动比对。修复前的 T6
// ExpectedMtime 守卫要模型「读时主动要 mtime → 自己记 → 写时主动传」三重自觉
// 且 prompt 零教学 ⇒ 生产路径死防线；本表把三步全部收归服务端。
//
// 语义约定（务必与调用点一致）：
// - 基准 = **盘上原始字节**的 sha256（不是净化/加行号后的显示文本）；
// - 成功写入（write/edit/apply）后必须**随写更新**基准为新内容，
// 否则 edit→write 连招会误拒（这是正确性要求，不是优化）；
// - 未登记（模型没经 read_file 读过、或文件已消失）⇒ 放行 —— 与修复前行为
// 一致，不把「新建 / 重写小文件 / 经 bash 读过的文件」的正常流堵死。
// 诚实边界：经 bash cat/sed 读写的文件登记不到（与 同类 CLI readFileState 同限）。
//
// ⚠️ 比对用内容 hash，不用 mtime：mtime 有秒级粒度、touch 可无内容改变它，
// 且它对「谁写的」不敏感。hash 是可靠信号 ⇒ 按本仓「信号可靠性 → 权限匹配」
// 原则可以驱动不可逆的拒绝。
//
// ⚠️ 刻意**不用**跨进程 flock 兜底：flock 只约束 whale-vs-whale，挡不住
// vim/IDE/其他工具；hash 基准覆盖所有写入者。残留窗口仅「check 与写之间」的
// 微秒级（与 同类 CLI apply_patch 的 RMW 同级），v1 不上锁（调研报告 §五.F）。

// writeBaseVerdict 是 write_file 写基准守卫的三值裁决。
type writeBaseVerdict int

const (
	// writeBaseUnregistered 未登记（模型没经 read_file 读过 / 文件已不存在）⇒ 放行
	writeBaseUnregistered writeBaseVerdict = iota
	// writeBaseFresh 盘上现值仍等于登记基准 ⇒ 放行
	writeBaseFresh
	// writeBaseStale 盘上已被他人改过 ⇒ 拒绝（FailureKindPatchConflict，可自愈）
	writeBaseStale
)

// contentFingerprint 返回内容的 sha256 十六进制指纹。
// 本仓无共享内容 hash SSOT（config/memory、permission、sandbox 各有本地实现），
// 按既有惯例本地实现；不与 filehistory 的内容存储 hash 混用（语义不同：
// 那是「会话写入史」，这是「模型最近一次看到什么」）。
func contentFingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// recordReadBase 登记（或覆盖）某文件的读基准。absPath 必须是绝对路径。
func (e *Executor) recordReadBase(absPath string, content []byte) {
	e.recordBase(absPath, content)
}

// recordWriteBase 成功写入后把基准更新为新内容（edit→write 连招不误拒的前提）。
func (e *Executor) recordWriteBase(absPath string, content []byte) {
	e.recordBase(absPath, content)
}

func (e *Executor) recordBase(absPath string, content []byte) {
	if e == nil || absPath == "" {
		return
	}
	e.readBasesMu.Lock()
	defer e.readBasesMu.Unlock()
	if e.readBases == nil {
		e.readBases = make(map[string]string)
	}
	e.readBases[absPath] = contentFingerprint(content)
}

// checkWriteBase 对已登记文件做写前比对。惰性初始化 ⇒ 零值 Executor 安全
// （Executor 存在多处构造点，含字面量；与 baseCtx 同一套惰性纪律）。
func (e *Executor) checkWriteBase(absPath string) writeBaseVerdict {
	if e == nil || absPath == "" {
		return writeBaseUnregistered
	}
	e.readBasesMu.Lock()
	base, ok := e.readBases[absPath]
	e.readBasesMu.Unlock()
	if !ok {
		return writeBaseUnregistered
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		// 文件已消失：写入将按「新建」进行，无盖写对象 ⇒ 按未登记处理
		return writeBaseUnregistered
	}
	if contentFingerprint(data) == base {
		return writeBaseFresh
	}
	return writeBaseStale
}
