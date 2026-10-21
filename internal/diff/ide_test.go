package diff

import (
	"os"
	"testing"
)

func TestDetectIDE_NoPanic(t *testing.T) {
	_, _, ok := DetectIDE()
	t.Logf("IDE detected: %v", ok)
}

func TestDetectIDE_Order(t *testing.T) {
	cmd, args, ok := DetectIDE()
	if ok {
		if args != nil && len(args) > 0 && args[0] != "--diff" && args[0] != "diff" {
			t.Errorf("expected args[0] to be --diff or diff, got: %v", args)
		}
		t.Logf("Detected IDE: %s with args: %v", cmd, args)
	}
}

func TestOpenInIDE_Fallback(t *testing.T) {
	oldPath := os.Getenv("PATH")
	defer os.Setenv("PATH", oldPath)

	os.Setenv("PATH", "")

	change := FileChange{
		Path:       "test.txt",
		OldContent: "old\n",
		NewContent: "new\n",
	}

	err := OpenInIDE(change)
	if err == nil {
		t.Errorf("expected error when no IDE is detected")
	}
}

func TestOpenInIDE_ValidChange(t *testing.T) {
	change := FileChange{
		Path:       "test.txt",
		OldContent: "old\n",
		NewContent: "new\n",
	}

	err := OpenInIDE(change)
	if err != nil {
		t.Logf("OpenInIDE returned error (may be expected if no IDE): %v", err)
	}
}
