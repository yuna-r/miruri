package repairworkspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureAndReset(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	path := filepath.Join(root, "main.c")
	if err := os.WriteFile(path, []byte("int value = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("int value = 2;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := repo.CaptureAndCommit("repair 1")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Files) != 1 || changes.Files[0] != "main.c" {
		t.Fatalf("unexpected files: %#v", changes.Files)
	}
	if !strings.Contains(string(changes.Patch), "+int value = 2;") {
		t.Fatalf("patch did not contain modification:\n%s", changes.Patch)
	}
	if err := os.WriteFile(path, []byte("broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repo.Reset(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "int value = 2;\n" {
		t.Fatalf("reset restored wrong content: %q", data)
	}
	ignored := filepath.Join(root, "ignored.tmp")
	if err := os.WriteFile(ignored, []byte("discard me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repo.ResetTo(baseline); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ignored); !os.IsNotExist(err) {
		t.Fatalf("ignored untracked file survived checkpoint reset: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "int value = 1;\n" {
		t.Fatalf("checkpoint reset restored wrong content: %q", data)
	}
}
