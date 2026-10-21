// Package util provides common utility functions for the whale project
package util

import (
	"os"
	"path/filepath"
	"runtime"
)

// AtomicWriteFile writes data to path atomically using temp file + fsync + rename.
// This prevents data corruption if the process crashes during write:
// the original file remains intact until the rename succeeds.
//
// The fsync (file.Sync) ensures data is flushed to disk before rename, guarding
// against power-loss. The parent directory is also fsync'd on Linux/macOS so
// the rename itself is durable. On Windows, directory fsync is a no-op
// (NTFS guarantees rename durability without an explicit flush).
//
// Use AtomicWriteFileNoFsync for non-critical paths where the fsync cost is
// not justified (e.g. caches, indexes that can be rebuilt).
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	return atomicWrite(path, data, perm, true /* fsync */)
}

// AtomicWriteFileNoFsync writes data atomically without fsync.
// Suitable for non-critical paths (caches, rebuildable indexes) where
// the fsync throughput cost is not justified. The temp+rename pattern still
// guards against process crashes (the original file stays intact until rename).
func AtomicWriteFileNoFsync(path string, data []byte, perm os.FileMode) error {
	return atomicWrite(path, data, perm, false /* no fsync */)
}

// atomicWrite is the shared implementation for AtomicWriteFile and
// AtomicWriteFileNoFsync. When doFsync is true, both the temp file and its
// parent directory are fsync'd before the rename.
func atomicWrite(path string, data []byte, perm os.FileMode, doFsync bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	// On every failure path below, ensure the temp file is cleaned up so
	// we do not leak .tmp files (which LoadWithReport would later treat as
	// crash-recovery candidates).
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}

	if doFsync {
		// fsync the temp file's data before close so it is durable on disk.
		if err := tmp.Sync(); err != nil {
			cleanup()
			return err
		}
	}

	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	if err := os.Chmod(tmpPath, perm); err != nil {
		os.Remove(tmpPath)
		return err
	}

	// On Windows, os.Rename fails if the destination already exists; remove
	// it first. On Unix, rename atomically replaces the destination.
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(path); err == nil {
			// Best-effort removal; if it fails (e.g. file is locked) the
			// Rename below will surface the error.
			os.Remove(path)
		}
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}

	// fsync the parent directory so the rename is durable on Linux/macOS.
	// Windows/NTFS does not require this and does not support fsync on
	// directories in a portable way, so we skip it there.
	if doFsync && runtime.GOOS != "windows" {
		if d, err := os.Open(dir); err == nil {
			// Best-effort: ignore errors since the data file is already renamed.
			_ = d.Sync()
			d.Close()
		}
	}

	return nil
}
