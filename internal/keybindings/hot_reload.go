// hot_reload.go — keybindings 配置热重载（M11-02）。
//
// 使用 fsnotify 监听配置文件变更，防抖动后自动重载配置。
// 与 hook/hot_reload.go 同构（C14 复用模式）：
// - 500ms 防抖动（debounceInterval 与 hook 包一致）
// - 监听目录而非文件（兼容编辑器原子替换）
// - reloadCount + lastErr 指标（C14 同构要求）
// - Stop 关闭 watcher + 等待 watchLoop 退出
//
// 对标 VSCode keybindings.json 修改后自动生效。
package keybindings

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounceInterval 防抖动间隔：500ms 内多次变更只触发一次重载（与 hook 包一致）。
const debounceInterval = 500 * time.Millisecond

// HotReloader keybindings 配置热重载器，监听配置文件变化并自动重载。
//
// C14 复用：与 hook.HotReloader 同构，差异仅在 Reload 调用 keybindings.Manager.Reload
// （hook.HotReloader 调 hook.Manager.SetConfig）。
type HotReloader struct {
	manager     *Manager          // 关联的 keybindings 管理器
	userPath    string            // 用户配置文件路径（~/.whale/keybindings.json）
	projectPath string            // 项目配置文件路径（.whale/keybindings.json）
	watcher     *fsnotify.Watcher // fsnotify 文件监听器
	debouncer   *time.Timer       // 防抖动定时器
	debounceMu  sync.Mutex        // 保护 debouncer
	stopCh      chan struct{}     // 停止信号通道
	doneCh      chan struct{}     // 监听循环完成信号通道
	reloadCount int64             // 重载次数（atomic，测试用）
	lastErr     error             // 最后一次重载错误（测试用）
	errMu       sync.RWMutex      // 保护 lastErr
}

// NewHotReloader 创建热重载器，传入关联的 Manager 和监听的配置文件路径。
//
// userPath 和 projectPath 均可为空（空路径不监听）。
// 调用方应在 NewManager 成功后调用 NewHotReloader + Start。
func NewHotReloader(mgr *Manager, userPath, projectPath string) (*HotReloader, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}
	return &HotReloader{
		manager:     mgr,
		userPath:    userPath,
		projectPath: projectPath,
		watcher:     watcher,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}, nil
}

// Start 启动热重载监听，监听配置文件所在目录（兼容编辑器原子替换）。
//
// 空路径跳过监听；目录不存在时返回 error（调用方可降级为手动 Reload）。
func (h *HotReloader) Start() error {
	watched := make(map[string]bool)
	for _, path := range []string{h.userPath, h.projectPath} {
		if path == "" {
			continue
		}
		dir := filepath.Dir(path)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return fmt.Errorf("watch directory %s: %w", dir, err)
		}
		if !watched[dir] {
			if err := h.watcher.Add(dir); err != nil {
				return fmt.Errorf("watch directory %s: %w", dir, err)
			}
			watched[dir] = true
		}
	}
	go h.watchLoop()
	log.Printf("[keybindings] hot reload started for user=%s project=%s", h.userPath, h.projectPath)
	return nil
}

// Stop 停止热重载监听，等待监听 goroutine 退出。
func (h *HotReloader) Stop() {
	close(h.stopCh)
	<-h.doneCh
	_ = h.watcher.Close()
	// 停止可能挂起的防抖动定时器
	h.debounceMu.Lock()
	if h.debouncer != nil {
		h.debouncer.Stop()
		h.debouncer = nil
	}
	h.debounceMu.Unlock()
}

// ReloadCount 返回成功重载次数（测试用）。
func (h *HotReloader) ReloadCount() int64 {
	return atomic.LoadInt64(&h.reloadCount)
}

// LastReloadError 返回最后一次重载错误，成功时返回 nil（测试用）。
func (h *HotReloader) LastReloadError() error {
	h.errMu.RLock()
	defer h.errMu.RUnlock()
	return h.lastErr
}

// watchLoop 监听循环，处理 fsnotify 事件和错误。
func (h *HotReloader) watchLoop() {
	defer close(h.doneCh)
	for {
		select {
		case <-h.stopCh:
			return
		case event, ok := <-h.watcher.Events:
			if !ok {
				return
			}
			h.handleEvent(event)
		case err, ok := <-h.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("[keybindings] hot reload watcher error: %v", err)
		}
	}
}

// handleEvent 处理文件变更事件，按事件类型决定是否触发防抖动重载。
//
// 仅处理 userPath 和 projectPath 的变更（监听的是目录，需过滤）。
// 文件删除：不重载，保留旧配置。
func (h *HotReloader) handleEvent(event fsnotify.Event) {
	// 过滤：仅处理目标文件的事件
	isTarget := false
	if h.userPath != "" && filepath.Clean(event.Name) == filepath.Clean(h.userPath) {
		isTarget = true
	}
	if h.projectPath != "" && filepath.Clean(event.Name) == filepath.Clean(h.projectPath) {
		isTarget = true
	}
	if !isTarget {
		return
	}
	// 文件删除：不重载，保留旧配置
	if event.Op&fsnotify.Remove == fsnotify.Remove {
		log.Printf("[keybindings] hot reload: config file removed, keeping old config: %s", event.Name)
		h.setLastError(fmt.Errorf("config file removed: %s", event.Name))
		return
	}
	// 文件创建/写入/重命名：触发防抖动重载
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
		h.scheduleReload()
	}
}

// scheduleReload 安排防抖动重载，500ms 内多次变更只触发一次。
func (h *HotReloader) scheduleReload() {
	h.debounceMu.Lock()
	defer h.debounceMu.Unlock()
	if h.debouncer != nil {
		h.debouncer.Stop() // 重置定时器（防抖动核心逻辑）
	}
	h.debouncer = time.AfterFunc(debounceInterval, h.reloadNow)
}

// reloadNow 立即执行重载：调 Manager.Reload（内部加写锁 + 读文件 + 解析 + 校验 + 重建 Resolver）。
//
// 重载失败时保留旧配置（Manager.Reload 内部降级）+ 输出错误日志。
func (h *HotReloader) reloadNow() {
	if err := h.manager.Reload(); err != nil {
		log.Printf("[keybindings] hot reload: reload failed, keeping old config: %v", err)
		h.setLastError(err)
		return
	}
	atomic.AddInt64(&h.reloadCount, 1)
	h.setLastError(nil)
	log.Printf("[keybindings] hot reload: config reloaded successfully")
}

// setLastError 设置最后一次重载错误（线程安全）。
func (h *HotReloader) setLastError(err error) {
	h.errMu.Lock()
	defer h.errMu.Unlock()
	h.lastErr = err
}
