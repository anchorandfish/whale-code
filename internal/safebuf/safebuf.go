// Package safebuf 提供**并发安全**的字节缓冲，用于捕获子进程输出。
//
// 为什么需要它（不是"为了过 -race"）：
// `exec.Cmd.Stdout` 若不是 `*os.File`，Go 会另起 goroutine 把管道内容拷进这个
// `io.Writer`。调用方在 `Wait()` 返回后立刻读缓冲，通常有 happens-before；
// 但在**取消 / WaitDelay / 提前返回**等路径上，拷贝 goroutine 可能仍在写
// （实测 `go test -race ./internal/hook` 与 `./internal/tools` 稳定报
// "DATA RACE: bytes.Buffer.Len vs writerDescriptor.func1"）。
// 一旦重叠，`bytes.Buffer` 的内部切片会与读方同时被改 —— 这是 UB，可能读到撕裂内容。
//
// 因此把"谁可以同时读写"这件事显式化：读写都过同一把锁，`String()` 返回**拷贝**
// （`bytes.Buffer.String()` 在 Go 1.25 是零拷贝别名，解锁后仍会被后续写入改写）。
package safebuf

import (
	"bytes"
	"sync"
)

// Buffer 是并发安全的 bytes.Buffer 替代品（只实现子进程输出捕获需要的子集）。
//
// 零值可用。不要复制已使用的 Buffer。
type Buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// Write 实现 io.Writer。
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

// WriteString 写入字符串，避免调用方先转 []byte。
func (b *Buffer) WriteString(s string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.WriteString(s)
}

// Len 返回当前已写入字节数。
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Len()
}

// String 返回当前内容的**拷贝**快照。
func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.b.Len() == 0 {
		return ""
	}
	return string(b.b.Bytes())
}

// Bytes 返回当前内容的**拷贝**快照（调用方可安全持有）。
func (b *Buffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.b.Len() == 0 {
		return nil
	}
	out := make([]byte, b.b.Len())
	copy(out, b.b.Bytes())
	return out
}

// Reset 清空缓冲。
func (b *Buffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.b.Reset()
}
