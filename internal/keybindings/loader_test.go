package keybindings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromFile(t *testing.T) {
	// D2 修复后路径需在白名单内（~/.whale/ 或 <dir>/.whale/）
	// 测试用 <tmpDir>/.whale/ 模拟 project 配置目录
	tmpDir := t.TempDir()
	whaleDir := filepath.Join(tmpDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0755); err != nil {
		t.Fatal(err)
	}

	t.Run("file not found returns empty config", func(t *testing.T) {
		path := filepath.Join(whaleDir, "nonexistent.json")
		cfg, err := LoadFromFile(path)
		if err != nil {
			t.Errorf("LoadFromFile() error = %v, want nil", err)
		}
		if !cfg.IsEmpty() {
			t.Errorf("LoadFromFile() = %v, want empty config", cfg)
		}
	})

	t.Run("invalid json returns error", func(t *testing.T) {
		path := filepath.Join(whaleDir, "invalid.json")
		err := os.WriteFile(path, []byte("invalid json"), 0644)
		if err != nil {
			t.Fatal(err)
		}
		_, err = LoadFromFile(path)
		if err == nil {
			t.Error("LoadFromFile() error = nil, want error for invalid JSON")
		}
	})

	t.Run("valid config", func(t *testing.T) {
		path := filepath.Join(whaleDir, "valid.json")
		content := `{"mode": "vim", "bindings": [{"key": "j", "action": "cursor_down", "mode": "normal"}]}`
		err := os.WriteFile(path, []byte(content), 0644)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadFromFile(path)
		if err != nil {
			t.Errorf("LoadFromFile() error = %v, want nil", err)
		}
		if cfg.Mode != "vim" {
			t.Errorf("LoadFromFile() Mode = %v, want vim", cfg.Mode)
		}
		if len(cfg.Bindings) != 1 {
			t.Errorf("LoadFromFile() Bindings length = %d, want 1", len(cfg.Bindings))
		}
	})

	// D2 反例测试：路径穿越被阻断
	t.Run("path traversal blocked", func(t *testing.T) {
		// 在白名单目录外创建文件，应被路径校验拒绝
		outsidePath := filepath.Join(tmpDir, "outside.json")
		if err := os.WriteFile(outsidePath, []byte(`{"mode":"vim"}`), 0644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadFromFile(outsidePath)
		if err == nil {
			t.Error("LoadFromFile() error = nil, want error for path outside .whale/")
		}
	})
}

func TestMergeWithDefault(t *testing.T) {
	tests := []struct {
		name         string
		cfg          Config
		wantBindings int
	}{
		{"empty config uses empty bindings", Config{}, 0},
		{"vim mode with defaults", Config{Mode: "vim"}, len(DefaultVimBindings())},
		{"emacs mode", Config{Mode: "emacs"}, len(DefaultEmacsBindings())},
		{"custom mode no defaults", Config{Mode: "custom"}, 0},
		{"vim with user bindings", Config{Mode: "vim", Bindings: []Binding{{Key: "j", Action: "cursor_up", Mode: "normal"}}}, len(DefaultVimBindings())},
		{"custom with user bindings", Config{Mode: "custom", Bindings: []Binding{{Key: "j", Action: "cursor_down", Mode: "normal"}}}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bindings := MergeWithDefault(tt.cfg)
			if len(bindings) != tt.wantBindings {
				t.Errorf("MergeWithDefault() = %d bindings, want %d", len(bindings), tt.wantBindings)
			}
		})
	}
}

func TestMergeWithDefaultOverride(t *testing.T) {
	cfg := Config{
		Mode: "vim",
		Bindings: []Binding{
			{Key: "j", Action: "cursor_up", Mode: "normal"},
		},
	}

	bindings := MergeWithDefault(cfg)

	var jBinding *Binding
	for i := range bindings {
		if bindings[i].Key == "j" && bindings[i].Mode == "normal" {
			jBinding = &bindings[i]
			break
		}
	}

	if jBinding == nil {
		t.Fatal("j binding not found")
	}
	if jBinding.Action != "cursor_up" {
		t.Errorf("j binding Action = %v, want cursor_up", jBinding.Action)
	}
}

func TestLoadAll(t *testing.T) {
	// D2 修复后路径需在白名单内，用两个不同的 .whale/ 目录模拟 user 和 project
	tmpDir := t.TempDir()
	userWhaleDir := filepath.Join(tmpDir, "userhome", ".whale")
	projectWhaleDir := filepath.Join(tmpDir, "project", ".whale")
	os.MkdirAll(userWhaleDir, 0755)
	os.MkdirAll(projectWhaleDir, 0755)

	userPath := filepath.Join(userWhaleDir, "keybindings.json")
	userContent := `{"mode": "vim", "bindings": [{"key": "j", "action": "cursor_down", "mode": "normal"}]}`
	os.WriteFile(userPath, []byte(userContent), 0644)

	projectPath := filepath.Join(projectWhaleDir, "keybindings.json")
	projectContent := `{"mode": "emacs", "bindings": [{"key": "ctrl+a", "action": "line_start", "mode": "insert"}]}`
	os.WriteFile(projectPath, []byte(projectContent), 0644)

	cfg, err := LoadAll(userPath, projectPath)
	if err != nil {
		t.Errorf("LoadAll() error = %v, want nil", err)
	}

	if cfg.Mode != "emacs" {
		t.Errorf("LoadAll() Mode = %v, want emacs (project overrides user)", cfg.Mode)
	}

	if len(cfg.Bindings) != 2 {
		t.Errorf("LoadAll() Bindings length = %d, want 2", len(cfg.Bindings))
	}
}
