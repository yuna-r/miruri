package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateSymlinksWithin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSymlinksWithin(root); err == nil {
		t.Fatal("expected escaping symlink to be rejected")
	}
	if err := os.Remove(filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("sub", "file"), filepath.Join(root, "inside")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSymlinksWithin(root); err != nil {
		t.Fatalf("internal symlink should be allowed: %v", err)
	}
}

func TestCopyTreeExcludesAgentControlDirectories(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.MkdirAll(filepath.Join(source, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".codex", "config.toml"), []byte("hooks = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CopyTree(source, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "main.c")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".codex")); !os.IsNotExist(err) {
		t.Fatalf("project-local Codex controls were copied into repair workspace: %v", err)
	}
}
