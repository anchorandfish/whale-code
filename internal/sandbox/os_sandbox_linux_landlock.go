//go:build linux

// Package sandbox — Linux Landlock 文件系统沙箱实现（P1-SEC / / T1-P1-B9）。
//
// Landlock 是 Linux 内核 LSM（≥ 5.13，ABI v1；≥ 5.19 叠加 REFER/TRUNCATE），
// 提供 unprivileged 的文件访问强制访问控制：ruleset 声明 handled 权限后，
// 所有路径对这些权限 deny-by-default，仅显式 path-beneath 规则授权。
//
// workspace-write 姿态（与 macOS Seatbelt 对齐）：
// - 读/执行白名单：系统目录（/usr /etc /dev /proc /sys /var /run ...）
// - 工作区 + 用户缓存 + $HOME 下常见工具目录（.cargo/.npm/.m2 ...）。
// $HOME 其余路径（含 ~/.ssh ~/.gnupg ~/.aws）不在白名单 → 默认拒绝。
// - 写白名单：工作区 + 系统临时目录 + 用户缓存 + /dev/null /dev/zero /dev/full。
// - 网络：不由 Landlock 管（默认由 CLONE_NEWNET namespace 隔离；
// 门 3 WHALE_SANDBOX_ALLOW_NETWORK=1 时 NewManager 过滤 "net"，
// 不创建 CLONE_NEWNET，沙箱内可访问宿主网络）。
//
// 应用时机（关键约束）：landlock_restrict_self(2) 作用于调用线程，
// 且限制在 execve 后继续生效。Go 的 os/exec 不支持 fork 后/exec 前钩子，
// 因此采用 re-exec helper 模式——helper 的契约与流程见
// os_sandbox_linux_preexec.go（本文件只负责「Landlock 规则怎么建、怎么施加」，
// 不再承担 helper 入口；pre-exec 阶段同时承载 seccomp，二者共用同一信封）。
//
// 降级链：
// 1. 首选：namespaces + Landlock（内核 ≥ 5.13）
// 2. Landlock 不支持：仅 namespaces + LogDegradation（fail-open 记录可观测）
// 3. 配置禁用：degradedSandbox
//
// 官方源：
// - Landlock: https://docs.kernel.org/userspace-api/landlock.html @2026-08-31
// - https://man7.org/linux/man-pages/man7/landlock.7.html @2026-08-31
// - https://man7.org/linux/man-pages/man7/user_namespaces.7.html @2026-08-31
package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// LandlockSpec 是传递给 re-exec helper 的文件访问规则（JSON 序列化经 env 传递）。
type LandlockSpec struct {
	// Workspace 可写工作区根（绝对路径，已解析符号链接）。
	Workspace string `json:"workspace"`
	// ExtraWriteDirs 额外可写根（系统临时目录、用户缓存）。
	ExtraWriteDirs []string `json:"extraWriteDirs,omitempty"`
	// WritableFiles 单文件可写白名单（/dev/null 等设备文件）。
	WritableFiles []string `json:"writableFiles,omitempty"`
	// ReadOnlyDirs 读+执行白名单目录（系统目录、工具目录）。
	ReadOnlyDirs []string `json:"readOnlyDirs,omitempty"`
	// ReadOnlyFiles 单文件读+执行白名单（~/.gitconfig ~/.npmrc 等）。
	ReadOnlyFiles []string `json:"readOnlyFiles,omitempty"`
}

// landlockV1AccessFS 是 ABI v1（内核 5.13+）全部可 handled 的 FS 权限。
func landlockV1AccessFS() uint64 {
	return unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM
}

// landlockV2Extra 是 ABI v2（内核 5.19+）新增权限（目录间引用/截断）。
func landlockV2Extra() uint64 {
	return unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE
}

// landlockSupported 探测内核 Landlock 支持（ABI 版本查询）。
//
// landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION) 返回
// 支持的最高 ABI 版本（errno==0）；ENOSYS/EOPNOTSUPP 表示不支持。
func landlockSupported() bool {
	_, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0,
		uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION))
	return errno == 0
}

// bestHandledAccess 探测内核支持的最大 handled 权限集。
//
// 返回 (handled, ok)：先尝试 ABI v2 权限（含 REFER/TRUNCATE），EINVAL 则
// 回退 ABI v1，再失败返回 ok=false。
func bestHandledAccess() (uint64, bool) {
	v1 := landlockV1AccessFS()
	v2 := v1 | landlockV2Extra()
	if tryCreateRuleset(v2) {
		return v2, true
	}
	if tryCreateRuleset(v1) {
		return v1, true
	}
	return 0, false
}

// tryCreateRuleset 尝试以 handled 权限集创建 ruleset（立即关闭，仅探测）。
func tryCreateRuleset(handled uint64) bool {
	attr := unix.LandlockRulesetAttr{Access_fs: handled}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return false
	}
	_ = unix.Close(int(fd))
	return true
}

// buildLandlockSpec 构造 workspace-write 姿态的 Landlock 规则。
//
// 读白名单设计权衡（Landlock 规则只能"授予"不能"排除"，授予 "/" 会让
// ~/.ssh 等敏感目录跟着可读，与 SBPL 的后置 deny 语义不同）：
// 不授予 "/" 读，而是授予系统目录白名单 + $HOME 内常见工具目录/文件。
// 代价是极少数读取任意 home 路径的命令会失败（可观测的 EACCES），
// 收益是 ~/.ssh ~/.gnupg ~/.aws 等密钥目录内核层不可读。
func buildLandlockSpec(workspace string) (*LandlockSpec, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, fmt.Errorf("landlock spec: workspace is empty")
	}
	resolvedWs, err := resolveSandboxPath(workspace)
	if err != nil {
		return nil, fmt.Errorf("landlock spec: resolve workspace: %w", err)
	}

	spec := &LandlockSpec{Workspace: resolvedWs}

	// 可写：系统临时目录 + 用户缓存
	if tmp, err := resolveSandboxPath(os.TempDir()); err == nil && tmp != "" {
		spec.ExtraWriteDirs = append(spec.ExtraWriteDirs, tmp)
	}
	if cache, err := os.UserCacheDir(); err == nil && cache != "" {
		if resolved, rerr := resolveSandboxPath(cache); rerr == nil && resolved != "" {
			spec.ExtraWriteDirs = append(spec.ExtraWriteDirs, resolved)
		}
	}
	// 设备文件可写（/dev/null 重定向是命令可用性基线）
	for _, dev := range []string{"/dev/null", "/dev/zero", "/dev/full"} {
		if _, statErr := os.Stat(dev); statErr == nil {
			spec.WritableFiles = append(spec.WritableFiles, dev)
		}
	}

	// 系统目录读+执行白名单（存在性过滤，避免规则加在不存在的路径上报错）
	spec.ReadOnlyDirs = existingPaths(
		"/usr", "/bin", "/sbin", "/lib", "/lib64", "/lib32", "/libx32",
		"/etc", "/opt", "/dev", "/proc", "/sys", "/var", "/run",
	)
	// XDG_RUNTIME_DIR（pulse/dbus 等用户态 socket 所在，只读即可）
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		if resolved, rerr := resolveSandboxPath(xdg); rerr == nil {
			spec.ReadOnlyDirs = append(spec.ReadOnlyDirs, resolved)
		}
	}
	// $HOME 下常见工具目录（读+执行）：构建工具链需要
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		toolDirs := []string{
			".config", ".local", ".cargo", ".rustup", ".npm", ".nvm",
			".m2", ".gradle", ".ivy2", ".dotnet", ".java", ".pki",
			".cache", // os.UserCacheDir 未设 XDG_CACHE_HOME 时的位置（可写根已含）
		}
		for _, sub := range toolDirs {
			p := filepath.Join(home, sub)
			// 存在性过滤（VM 实测教训：~/.local 不存在时规则 fd 打不开，
			// 曾致 helper 硬失败、整个沙箱起不来）
			if _, statErr := os.Stat(p); statErr != nil {
				continue
			}
			if resolved, rerr := resolveSandboxPath(p); rerr == nil {
				spec.ReadOnlyDirs = append(spec.ReadOnlyDirs, resolved)
			}
		}
		// 单文件：git/npm 配置
		for _, f := range []string{".gitconfig", ".npmrc"} {
			p := filepath.Join(home, f)
			if _, statErr := os.Stat(p); statErr == nil {
				if resolved, rerr := resolveSandboxPath(p); rerr == nil {
					spec.ReadOnlyFiles = append(spec.ReadOnlyFiles, resolved)
				}
			}
		}
	}
	return spec, nil
}

// existingPaths 过滤出真实存在的路径（不存在路径无法打开 fd 建规则）。
func existingPaths(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// applyLandlockSpec 在当前线程应用 Landlock 规则（restrict self）。
//
// 前置：调用者必须是即将 exec 目标命令的进程（helper）；限制随 execve 继承。
// 后置：调用线程（及其后所有线程/子进程）受规则约束，不可逆。
func applyLandlockSpec(spec *LandlockSpec) error {
	if spec == nil || spec.Workspace == "" {
		return fmt.Errorf("landlock: empty spec")
	}
	handled, ok := bestHandledAccess()
	if !ok {
		return errors.New("landlock: kernel does not support landlock")
	}

	attr := unix.LandlockRulesetAttr{Access_fs: handled}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock: create ruleset: %w", errno)
	}
	defer unix.Close(int(fd))

	// 读+执行权限（授予只读白名单）
	roAccess := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR)
	for _, d := range dedupe(spec.ReadOnlyDirs) {
		if err := addPathBeneathRule(int(fd), d, roAccess); err != nil {
			// 只读白名单容忍 ENOENT（fail-closed：跳过授权只会更严；
			// spec 构建与应用间路径可能消失）
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("landlock: read dir %s: %w", d, err)
		}
	}
	for _, f := range dedupe(spec.ReadOnlyFiles) {
		if err := addPathBeneathRule(int(fd), f,
			unix.LANDLOCK_ACCESS_FS_EXECUTE|unix.LANDLOCK_ACCESS_FS_READ_FILE); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("landlock: read file %s: %w", f, err)
		}
	}
	// 全权限（授予可写白名单；非目录 fd 自动裁剪目录专属权限）
	// Workspace 是核心可写根：不存在即配置错误，硬失败
	if err := addPathBeneathRule(int(fd), spec.Workspace, handled); err != nil {
		return fmt.Errorf("landlock: write dir %s: %w", spec.Workspace, err)
	}
	// 便利性可写目录（tmp/cache）：容忍 ENOENT（fail-closed：少授权只会更严）
	for _, d := range dedupe(spec.ExtraWriteDirs) {
		if err := addPathBeneathRule(int(fd), d, handled); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("landlock: write dir %s: %w", d, err)
		}
	}
	for _, f := range dedupe(spec.WritableFiles) {
		if err := addPathBeneathRule(int(fd), f, handled); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("landlock: write file %s: %w", f, err)
		}
	}

	// restrict self 前必须 set no_new_privs（内核强制，防提权绕过）
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("landlock: PR_SET_NO_NEW_PRIVS: %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF,
		uintptr(fd), 0, 0); errno != 0 {
		return fmt.Errorf("landlock: restrict self: %w", errno)
	}
	return nil
}

// addPathBeneathRule 向 ruleset 添加 path-beneath 规则。
//
// path 以 O_PATH 打开（Landlock 要求）；非目录 fd 时裁剪目录专属权限
// （READ_DIR/REMOVE_*/MAKE_*/REFER），否则内核 EINVAL。
func addPathBeneathRule(rulesetFd int, path string, allowed uint64) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(fd)

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("fstat %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		allowed &= uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
			unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_READ_FILE |
			unix.LANDLOCK_ACCESS_FS_TRUNCATE)
	}

	rule := unix.LandlockPathBeneathAttr{
		Parent_fd:      int32(fd),
		Allowed_access: allowed,
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFd), uintptr(unix.LANDLOCK_RULE_PATH_BENEATH),
		uintptr(unsafe.Pointer(&rule))); errno != 0 {
		return fmt.Errorf("add rule %s: %w", path, errno)
	}
	return nil
}

// dedupe 去重（保持顺序），避免同一目录重复加规则。
func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
