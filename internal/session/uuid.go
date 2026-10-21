// Package session — UUID generation for the message chain.
//
// We generate UUID v4 (random) for new messages and UUID v5 (SHA-1 namespaced)
// for deterministic backfill of legacy JSONL files that predate the UUID chain.
// Both are implemented with the standard library (crypto/rand + crypto/sha1)
// to avoid adding github.com/google/uuid as a dependency, in line with the
// project's "zero external dependencies preferred" convention.
package session

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// namespaceOID is the pre-defined "DNS" namespace UUID used as the v5 namespace
// here (RFC 4122 §4.3). The specific namespace does not matter for our use —
// we only need determinism: the same (message, line) input must always produce
// the same UUID across loads so the ParentUUID chain stays stable.
var namespaceOID = [16]byte{0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}

// generateUUID returns a random UUID v4 string (lowercase, with hyphens).
// Panics on rand.Read failure, which only happens if the system's CSPRNG is
// unavailable — at that point we cannot guarantee uniqueness anyway.
func generateUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback: this should never happen in practice. If it does, a
		// panic is preferable to silently producing a duplicate UUID that
		// would corrupt the message chain.
		panic(fmt.Sprintf("crypto/rand.Read failed: %v", err))
	}
	// RFC 4122 §4.4: set version (4) and variant (10xx) bits.
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	return formatUUID(b[:])
}

// deterministicUUID returns a UUID v5 (SHA-1 namespaced) string for a message
// at a given line number. Used when loading legacy JSONL files whose messages
// predate the UUID chain: we synthesize a stable UUID so the chain can be
// reconstructed. The same (message, line) input always yields the same UUID,
// so repeated loads produce identical chains.
//
// BUG FIX (S9): the previous implementation built the hash input with
// string(rune(lineNum)) and string(rune(msg.Timestamp)), which produces
// multi-byte UTF-8 sequences for values > 0x10FFFF and invalid bytes for
// negative or very large integers, making the hash input unstable and
// non-deterministic across runs. We now use strconv.Itoa / FormatInt so the
// hash input is always a stable ASCII decimal representation.
func deterministicUUID(msg Message, lineNum int) string {
	h := sha1.New()
	h.Write(namespaceOID[:])
	// Stable ASCII fields. Order matters for determinism.
	h.Write([]byte(string(msg.Role)))
	h.Write([]byte{'|'})
	h.Write([]byte(msg.TextContent()))
	h.Write([]byte{'|'})
	h.Write([]byte(strconv.Itoa(lineNum))) // FIX: was string(rune(lineNum))
	h.Write([]byte{'|'})
	h.Write([]byte(strconv.FormatInt(msg.Timestamp, 10))) // FIX: was string(rune(msg.Timestamp))
	sum := h.Sum(nil)                                     // 20 bytes; we only need the first 16

	var b [16]byte
	copy(b[:], sum[:16])
	// RFC 4122 §4.3: set version (5) and variant (10xx) bits.
	b[6] = (b[6] & 0x0f) | 0x50 // version 5
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	return formatUUID(b[:])
}

// NewSessionID 生成新的会话主键：UUID v7 风格（时间有序 + 74 bit 随机）。
//
// 为什么会话主键必须是 UUID，而不是「用户输入 + 时间戳」这类派生名：
// 1. 可点名：稳定主键才能在命令行/TUI 里精确指定一个会话（派生名做不到）；
// 2. 多开不撞：多实例同时开会话时，派生名会在「同输入 + 同秒」时完全相同，
// 两个对话写进同一个文件，触发锁争用（表现为间歇 Save failed）；
// 3. 不该从内容派生：会话身份是**身份**，不是**摘要**，两者耦合会让改名、
// 重复提问等无害操作变成数据冲突。
//
// v7（毫秒时间戳打头）相比 v4 的额外好处：文件名按创建时间大致有序，
// 便于目录内排序与人工排查，同时保留 74 bit 随机保证唯一性。
func NewSessionID() string {
	var b [16]byte

	// 前 48 bit：Unix 毫秒时间戳（big-endian）。
	ms := uint64(time.Now().UnixMilli())
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (8 * uint(5-i)))
	}

	// 后 10 字节：随机数（rand_a 12 bit + rand_b 62 bit）。
	var rnd [10]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		// 与 generateUUID 一致：拿不到 CSPRNG 时 panic 优于产出重复主键。
		panic(fmt.Sprintf("crypto/rand.Read failed: %v", err))
	}
	copy(b[6:], rnd[:])

	// RFC 9562 §5.7: version(7) 占 b[6] 高 4 位；variant(RFC 4122) 占 b[8] 高 2 位。
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80

	return formatUUID(b[:])
}

// ShortIDLen 是会话展示短码的长度（8 位 hex）。
//
// 8 位足以在几百个会话的量级上区分彼此，又不至于挤爆 `/session list` 的行宽。
const ShortIDLen = 8

// ShortID 返回会话主键的**展示短码**（列表里 `[xxxx]` 的取值，也是可回填点名的句柄）。
//
// BUG FIX：短码原先取主键的**前** 8 位，而主键是 UUIDv7 —— 前 48 bit 是
// Unix 毫秒时间戳，占满前 12 位 hex ⇒ 前 8 位 hex 恰是时间戳的高 32 位，
// 每 2^16 ms = 65.536 秒才跳一次。后果是「同一分钟内开的几个会话短码完全一样」：
// - 真机 2026-10-08：`01a11aa4` 对应 4 个会话（16:32:24 / 16:32:33 / …）；
// - 真机 2026-10-14：`01a13661` 对应 4 个会话（01:48:39 / 01:48:50 / …）。
//
// 前 8 位对「区分近期会话」的贡献是**零**（它只编码创建时刻），所以改取**随机尾段**：
// UUIDv7 的最后 4 字节（b[12:16]）是 32 bit 纯随机，不含 version/variant 位。
// 8 位 hex = 32 bit ⇒ 135 个会话的碰撞概率约 2e-6，且不暴露创建顺序（创建时间另有
// 一列单独展示，本就不该混进身份短码）。Resolve 同步接受该短码（见 store.Resolve）。
//
// 非 UUID 形状的旧式会话名（存量文件）原样返回，避免把可读名截成无意义片段。
//
// ★ 这是**最短**句柄，供单条上下文（日志/单行展示）使用。渲染**列表**时不要逐条调它，
// 要用 Handles 整组一起算 —— 固定 8 位在两行碰巧同尾段时仍会分不清（见 Handles 注释）。
func ShortID(id string) string {
	if !IsSessionID(id) {
		return id
	}
	// 最后一个分组（12 位 hex）的末 8 位 —— 对应 b[12:16]，纯随机。
	return id[len(id)-ShortIDLen:]
}

// Handles 为一组会话主键分配**组内互不相同**的展示句柄。
//
// 为什么需要「一组一起算」而不是每条各取 8 位：短码取的是随机尾段（ShortID），
// 但 8 位 hex = 32 bit ⇒ 随机**只压低概率、不消除碰撞**（n 个会话的生日碰撞概率
// 约 n²/2³³：135 个 ≈ 2e-6，1000 个 ≈ 1.2e-4）。一旦撞上，列表里又会出现两行
// 一模一样的 `[xxxx]` —— 与 原症状完全相同，只是成因从「时间戳前缀趋同」
// 换成了「随机撞车」。
//
// 所以唯一性**不能**由某一种取值规则来保证，只能由「按需加长」保证：
// 从 ShortIDLen 位随机尾段起，对在**本组内**撞车的会话逐位加长（9、10、…），
// 直到组内两两不同。全长 32 位 hex 必然互异 ⇒ 循环一定终止。
// 这就是 git 短哈希（`find_unique_abbrev`）的做法：短码是「给人看的句柄」，
// 长度可以随上下文变，唯一性由「在本上下文里不撞」来保证。
//
// 调用方每渲染一次列表就调一次，句柄只在**该列表内**保证唯一。
// 非 UUID 形状的旧式名原样返回（不参与 hex 尾段计算）。
func Handles(ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	var uuids []string
	for _, id := range ids {
		if IsSessionID(id) {
			uuids = append(uuids, id)
			continue
		}
		out[id] = id // 旧式可读名不参与 hex 尾段计算，原样返回
	}
	if len(uuids) == 0 {
		return out
	}

	// width[id] = 该会话当前使用的尾段位数。先取最短（ShortIDLen），
	// 每轮把所有「在本组内仍撞车」的会话 +1 位，直到没有撞车为止。
	//
	// 终止性：撞车组的成员是**同步**加宽的，所以它们会一直同组直到某一轮分开为止；
	// 而 32 位 = 全长 hex，不同主键在该宽度下必然互异 ⇒ 最迟在 32 位停下。
	// （同一主键在入参里重复出现时始终"撞车"，用 width<32 兜住，不无限循环。）
	width := make(map[string]int, len(uuids))
	for _, id := range uuids {
		width[id] = ShortIDLen
	}
	for {
		count := make(map[string]int, len(uuids))
		for _, id := range uuids {
			count[hexTail(id, width[id])]++
		}
		widened := false
		for _, id := range uuids {
			if count[hexTail(id, width[id])] > 1 && width[id] < 32 {
				width[id]++
				widened = true
			}
		}
		if !widened {
			break
		}
	}
	for _, id := range uuids {
		out[id] = hexTail(id, width[id])
	}
	return out
}

// hexTail 取主键 hex 形态（去掉连字符）的末 width 位。
func hexTail(id string, width int) string {
	h := strings.ReplaceAll(id, "-", "")
	if width >= len(h) {
		return h
	}
	return h[len(h)-width:]
}

// matchHexTail 报告 tail 是否为主键 hex 形态的**尾段**（即展示句柄的判别）。
//
// 长度下限 ShortIDLen（更短的信息量不足、易撞），上限 32（全长 hex）。
// 含连字符的写法不算尾段 —— 那种形态由 Resolve 的「UUID 前缀」那一条处理。
func matchHexTail(id, tail string) bool {
	if !IsSessionID(id) || len(tail) < ShortIDLen || len(tail) > 32 || strings.Contains(tail, "-") {
		return false
	}
	flat := strings.ReplaceAll(id, "-", "")
	return strings.EqualFold(flat[len(flat)-len(tail):], tail)
}

// IsSessionID 报告该字符串是否形如会话主键（UUID）。
// 用于把「用户给的是 UUID 还是可读名」分开处理：UUID 直接命中，
// 可读名需要按别名或文件名匹配。
func IsSessionID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range []byte(s) {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// formatUUID renders 16 raw bytes as the canonical 8-4-4-4-12 hex string.
func formatUUID(b []byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(b[0:4]),
		binary.BigEndian.Uint16(b[4:6]),
		binary.BigEndian.Uint16(b[6:8]),
		binary.BigEndian.Uint16(b[8:10]),
		b[10:16],
	)
}
