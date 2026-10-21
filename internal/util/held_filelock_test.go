package util

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestHeldFileLockLifetime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	first, err := TryAcquireFileLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.WriteMetadata([]byte("instance-one")); err != nil {
		t.Fatal(err)
	}
	if _, err := TryAcquireFileLock(path); !errors.Is(err, ErrFileLocked) {
		t.Fatalf("second acquisition error = %v, want ErrFileLocked", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := TryAcquireFileLock(path)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}
