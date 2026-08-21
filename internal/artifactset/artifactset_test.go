package artifactset

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAndReadIntegrity(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "demo"), []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := WriteIntegrity(root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := ReadIntegrity(root, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Path != "artifacts/demo" || entries[1].Path != "manifest.json" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
}

func TestResolvePathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolvePath(root, "../outside"); err == nil {
		t.Fatal("expected traversal rejection")
	}
}

func TestResolvePathAcceptsArtifactRootThroughSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "manifest.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	resolved, err := ResolvePath(alias, "manifest.json")
	if err != nil {
		t.Fatalf("artifact root reached through symlinked ancestor was rejected: %v", err)
	}
	if resolved != filepath.Join(alias, "manifest.json") {
		t.Fatalf("resolved path = %q, want %q", resolved, filepath.Join(alias, "manifest.json"))
	}
}

func TestResolvePathStillRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ResolvePath(root, "escape/secret"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}
