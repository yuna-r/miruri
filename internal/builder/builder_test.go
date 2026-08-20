package builder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yuna-r/miruri/internal/codex"
	"github.com/yuna-r/miruri/internal/target"
)

func TestBuildHelloFixtureForHost(t *testing.T) {
	for _, tool := range []string{"clang", "cmake"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available", tool)
		}
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	fixture := filepath.Join(repoRoot, "fixtures", "hello-c")
	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Build(context.Background(), Config{
		ProjectDir: fixture,
		Target:     profile,
		OutDir:     t.TempDir(),
		MaxRepairs: 0,
		Version:    "test",
		Timeout:    2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Manifest.Artifacts) < 2 {
		t.Fatalf("expected executable and static library, got %d artifact(s)", len(result.Manifest.Artifacts))
	}
}

func TestBuildUsesCodexRepairInIsolatedWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX fake Codex CLI")
	}
	for _, tool := range []string{"git", "clang", "cmake"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available", tool)
		}
	}
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	cmake := "cmake_minimum_required(VERSION 3.16)\nproject(miruri_codex_fixture C)\nadd_executable(codex-fixture main.c)\n"
	if err := os.WriteFile(filepath.Join(project, "CMakeLists.txt"), []byte(cmake), 0o644); err != nil {
		t.Fatal(err)
	}
	original := "#error MIRURI_CODEX_REPAIR_REQUIRED\nint main(void) { return 0; }\n"
	if err := os.WriteFile(filepath.Join(project, "main.c"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeCodex := filepath.Join(root, "codex")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then
  echo "codex-cli test"
  exit 0
fi
if [ "${1:-}" = "login" ] && [ "${2:-}" = "status" ]; then
  echo "Logged in using ChatGPT"
  exit 0
fi
if [ "${1:-}" = "--ask-for-approval" ] && [ "${2:-}" = "never" ]; then shift 2; fi
if [ "${1:-}" != "exec" ]; then exit 8; fi
shift
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message) shift; out="$1" ;;
  esac
  shift || true
done
cat >/dev/null
cat > main.c <<'C'
#include <stdio.h>
int main(void) { puts("repaired"); return 0; }
C
printf '%s\n' '{"type":"thread.started","thread_id":"builder_thread"}'
printf '%s\n' '{"type":"turn.completed","turn_id":"builder_turn","usage":{"input_tokens":50,"cached_input_tokens":10,"output_tokens":20}}'
cat >"$out" <<'JSON'
{"status":"repaired","summary":"Removed the intentional compile blocker.","changed_files":["main.c"],"assumptions":[],"remaining_risks":[]}
JSON
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "dist")
	result, err := Build(context.Background(), Config{
		ProjectDir:   project,
		Target:       profile,
		OutDir:       out,
		UseCodex:     true,
		MaxRepairs:   1,
		CodexBinary:  fakeCodex,
		CodexAuth:    codex.AuthChatGPT,
		CodexTimeout: time.Minute,
		Version:      "test",
		Timeout:      2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Manifest.CodexRepairs) != 1 {
		t.Fatalf("expected one repair, got %#v", result.Manifest.CodexRepairs)
	}
	repair := result.Manifest.CodexRepairs[0]
	if repair.Status != "repaired" || len(repair.ChangedFiles) != 1 || repair.ChangedFiles[0] != "main.c" {
		t.Fatalf("unexpected repair manifest: %+v", repair)
	}
	if repair.ThreadID != "builder_thread" || repair.TurnID != "builder_turn" {
		t.Fatalf("event IDs were not persisted: %+v", repair)
	}
	if repair.ResultFile == "" {
		t.Fatal("repair result provenance was not persisted")
	}
	if _, err := os.Stat(filepath.Join(result.PackageDir, filepath.FromSlash(repair.ResultFile))); err != nil {
		t.Fatalf("missing repair result: %v", err)
	}
	patchPath := filepath.Join(result.PackageDir, filepath.FromSlash(repair.PatchFile))
	patch, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "MIRURI_CODEX_REPAIR_REQUIRED") || !strings.Contains(string(patch), "puts(\"repaired\")") {
		t.Fatalf("unexpected repair patch:\n%s", patch)
	}
	unchanged, err := os.ReadFile(filepath.Join(project, "main.c"))
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != original {
		t.Fatal("Codex repair modified the original project instead of the isolated overlay")
	}
}

func TestRejectedCodexRepairRestoresPreRepairCheckpoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX fake Codex CLI")
	}
	for _, tool := range []string{"git", "clang", "cmake"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available", tool)
		}
	}
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "CMakeLists.txt"), []byte("cmake_minimum_required(VERSION 3.16)\nproject(rejected_repair C)\nadd_executable(rejected main.c)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	original := "#error REPAIR_MUST_BE_REJECTED\nint main(void) { return 0; }\n"
	if err := os.WriteFile(filepath.Join(project, "main.c"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeCodex := filepath.Join(root, "codex")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then echo "codex-cli test"; exit 0; fi
if [ "${1:-}" = "login" ] && [ "${2:-}" = "status" ]; then echo "Logged in using ChatGPT"; exit 0; fi
if [ "${1:-}" = "--ask-for-approval" ] && [ "${2:-}" = "never" ]; then shift 2; fi
if [ "${1:-}" != "exec" ]; then exit 8; fi
shift
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message) shift; out="$1" ;;
  esac
  shift || true
done
cat >/dev/null
cat > main.c <<'C'
int main(void) { return 0; }
C
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution","command":"qemu-riscv64 ./rejected"}}'
cat >"$out" <<'JSON'
{"status":"repaired","summary":"Changed the source but violated artifact-only mode.","changed_files":["main.c"],"assumptions":[],"remaining_risks":[]}
JSON
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Build(context.Background(), Config{
		ProjectDir:   project,
		Target:       profile,
		OutDir:       filepath.Join(root, "dist"),
		UseCodex:     true,
		MaxRepairs:   1,
		CodexBinary:  fakeCodex,
		CodexAuth:    codex.AuthChatGPT,
		CodexTimeout: time.Minute,
		Version:      "test",
		Timeout:      2 * time.Minute,
		KeepWork:     true,
	})
	if err == nil {
		t.Fatal("expected artifact-only policy rejection")
	}
	if len(result.Manifest.CodexRepairs) != 1 {
		t.Fatalf("missing rejected repair provenance: %#v", result.Manifest.CodexRepairs)
	}
	if result.Manifest.CodexRepairs[0].Status != "error" || result.Manifest.CodexRepairs[0].Error == "" {
		t.Fatalf("unexpected rejected repair status: %+v", result.Manifest.CodexRepairs[0])
	}
	restored, readErr := os.ReadFile(filepath.Join(result.WorkDir, "source", "main.c"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(restored) != original {
		t.Fatalf("rejected repair was not rolled back: %q", restored)
	}
}
