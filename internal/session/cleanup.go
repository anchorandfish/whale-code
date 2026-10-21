// Package session — graceful lock release on process exit.
//
// When a whale-code process holding a session lock is killed (SIGINT/SIGTERM),
// the .lock file would otherwise linger until the 30s timeout expires, during
// which no other process can acquire the lock. To avoid this window we
// register each held lock with a process-wide cleanup that runs on
// SIGINT/SIGTERM and removes the lock files before the process exits.
//
// Design notes:
//   - The cleanup registry is process-global (package-level vars guarded by a
//     mutex) because signal handlers are inherently process-global.
//   - We install the signal handler exactly once (sync.Once) on the first
//     lock registration; subsequent registrations just append to the list.
//   - On the signal we call Release() on each held lock, which re-reads the
//     lock file and only removes it if the caller still owns it (defends
//     against the lock having been stolen by another process in the meantime).
//   - We deliberately re-raise the signal via os.Exit(130) so the parent
//     shell sees the conventional 128+SIGINT exit status.
package session

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

var (
	cleanupOnce sync.Once
	heldLocks   = make([]*SessionLock, 0)
	heldLocksMu sync.Mutex
)

// registerLockForCleanup adds lock to the process-wide cleanup registry and
// installs the SIGINT/SIGTERM handler on first call. Safe to call from any
// goroutine.
func registerLockForCleanup(lock *SessionLock) {
	if lock == nil {
		return
	}
	heldLocksMu.Lock()
	heldLocks = append(heldLocks, lock)
	heldLocksMu.Unlock()

	cleanupOnce.Do(installCleanupSignalHandler)
}

// unregisterLock removes lock from the cleanup registry. Called when a lock is
// released normally (so we don't waste time trying to release it again on
// exit). Safe to call with a nil lock or a lock that was never registered.
func unregisterLock(lock *SessionLock) {
	if lock == nil {
		return
	}
	heldLocksMu.Lock()
	defer heldLocksMu.Unlock()
	for i, l := range heldLocks {
		if l == lock {
			heldLocks = append(heldLocks[:i], heldLocks[i+1:]...)
			return
		}
	}
}

// releaseAllHeldLocks releases every lock currently held by this process.
// Called from the signal handler. Errors are ignored because we are exiting
// anyway; the worst case is a stale lock file that the 30s timeout will
// eventually clean up.
func releaseAllHeldLocks() {
	heldLocksMu.Lock()
	locks := heldLocks
	heldLocks = nil
	heldLocksMu.Unlock()
	for _, l := range locks {
		l.Release()
	}
}

// installCleanupSignalHandler registers the SIGINT/SIGTERM handler that
// releases all held locks before the process terminates. Installed at most
// once via cleanupOnce.
func installCleanupSignalHandler() {
	sigCh := make(chan os.Signal, 1)
	// SIGINT and SIGTERM are the common termination signals on Unix. On
	// Windows, syscall.SIGTERM is not deliverable but os.Interrupt (Ctrl+C)
	// is; signal.Notify accepts both and ignores unsupported signals.
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		releaseAllHeldLocks()
		// 128 + signal number is the conventional shell exit status for a
		// process killed by a signal. SIGINT = 2, so 130.
		os.Exit(130)
	}()
}
