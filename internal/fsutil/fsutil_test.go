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

func TestCopyTreeWithOptionsExcludesExplicitGeneratedPath(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	generated := filepath.Join(source, "custom-results")
	if err := os.MkdirAll(generated, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generated, "stale.c"), []byte("#error generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CopyTreeWithOptions(source, destination, CopyTreeOptions{ExcludePaths: []string{generated}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "main.c")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "custom-results")); !os.IsNotExist(err) {
		t.Fatalf("explicitly excluded output was copied: %v", err)
	}
}

func TestCanonicalPathResolvesSymlinkedExistingAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated privileges on Windows")
	}
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Fatal(err)
	}
	resolved, err := CanonicalPath(filepath.Join(alias, "missing", "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	canonicalRealDir, err := CanonicalPath(realDir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalRealDir, "missing", "report.json")
	if resolved != want {
		t.Fatalf("canonical path = %q, want %q", resolved, want)
	}
}

func TestCopyTreeRejectsDestinationSymlinkedInsideSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated privileges on Windows")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "destination-alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	if err := CopyTree(source, filepath.Join(alias, "copy")); err == nil {
		t.Fatal("destination resolving inside source was accepted")
	}
}
