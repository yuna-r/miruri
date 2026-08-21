package builder

import (
	"context"
	"encoding/json"
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
if [ "${1:-}" = "--help" ]; then
  echo "--ask-for-approval"
  exit 0
fi
if [ "${1:-}" = "exec" ] && [ "${2:-}" = "--help" ]; then
  echo "--json --ephemeral --ignore-user-config --ignore-rules --sandbox --output-schema --output-last-message"
  exit 0
fi
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
printf '%s\n' 'temporary notes' > MIRURI_REPAIR_NOTES.md
printf 'object\000payload' > generated.o
printf 'binary\000payload' > codex-built
chmod +x codex-built
printf '%s\n' '{"type":"thread.started","thread_id":"builder_thread"}'
printf '%s\n' '{"type":"turn.completed","turn_id":"builder_turn","usage":{"input_tokens":50,"cached_input_tokens":10,"output_tokens":20}}'
cat >"$out" <<'JSON'
{"status":"repaired","summary":"Removed the intentional compile blocker.","changed_files":["main.c","MIRURI_REPAIR_NOTES.md","generated.o","codex-built"],"assumptions":["fixture blocker was intentional"],"remaining_risks":[]}
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
	if len(repair.DiscardedChanges) != 3 {
		t.Fatalf("generated changes were not recorded as discarded: %+v", repair.DiscardedChanges)
	}
	if repair.DiagnosticsFile == "" || repair.DiagnosticsJSONFile == "" {
		t.Fatalf("diagnostic provenance missing: %+v", repair)
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
	for _, forbidden := range []string{"MIRURI_REPAIR_NOTES", "generated.o", "codex-built", "GIT binary patch"} {
		if strings.Contains(string(patch), forbidden) {
			t.Fatalf("generated change %q leaked into repair patch:\n%s", forbidden, patch)
		}
	}
	for _, relative := range []string{repair.DiagnosticsFile, repair.DiagnosticsJSONFile} {
		if _, err := os.Stat(filepath.Join(result.PackageDir, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("missing diagnostic artifact %s: %v", relative, err)
		}
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
if [ "${1:-}" = "--help" ]; then
  echo "--ask-for-approval"
  exit 0
fi
if [ "${1:-}" = "exec" ] && [ "${2:-}" = "--help" ]; then
  echo "--json --ephemeral --ignore-user-config --ignore-rules --sandbox --output-schema --output-last-message"
  exit 0
fi
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

func TestCodexRepairRejectsEscapingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX symlinks and a shell fixture")
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
	if err := os.WriteFile(filepath.Join(project, "CMakeLists.txt"), []byte("cmake_minimum_required(VERSION 3.16)\nproject(symlink_repair C)\nadd_executable(symlink-repair main.c)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	original := "#error SYMLINK_REPAIR_REQUIRED\nint main(void) { return 0; }\n"
	if err := os.WriteFile(filepath.Join(project, "main.c"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeCodex := filepath.Join(root, "codex")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--help" ]; then echo "--ask-for-approval"; exit 0; fi
if [ "${1:-}" = "exec" ] && [ "${2:-}" = "--help" ]; then echo "--json --ephemeral --ignore-user-config --ignore-rules --sandbox --output-schema --output-last-message"; exit 0; fi
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
ln -s /tmp miruri-escape
cat >"$out" <<'JSON'
{"status":"repaired","summary":"Changed source and added a link.","changed_files":["main.c","miruri-escape"],"assumptions":[],"remaining_risks":[]}
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
		t.Fatal("expected escaping symlink rejection")
	}
	if len(result.Manifest.CodexRepairs) != 1 || !strings.Contains(result.Manifest.CodexRepairs[0].Error, "unsafe repair workspace") {
		t.Fatalf("unexpected repair result: %+v", result.Manifest.CodexRepairs)
	}
	if _, statErr := os.Lstat(filepath.Join(result.WorkDir, "source", "miruri-escape")); !os.IsNotExist(statErr) {
		t.Fatalf("escaping symlink survived rollback: %v", statErr)
	}
	restored, readErr := os.ReadFile(filepath.Join(result.WorkDir, "source", "main.c"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(restored) != original {
		t.Fatalf("source was not restored after unsafe repair: %q", restored)
	}
}

func TestMakeCodexRepairCompactsDiagnosticsAndExcludesValidationBuildProducts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX Make and shell fixture")
	}
	for _, tool := range []string{"git", "clang", "make"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available", tool)
		}
	}
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	makefile := `CC ?= clang
CFLAGS ?= -std=c99 -Wall

all: program

program: main.o
	$(CC) $(CFLAGS) -o $@ $^

main.o: main.c
	@i=0; while [ $$i -lt 120 ]; do echo "system.h:$$i:1: warning: synthetic warning flood" >&2; i=$$((i+1)); done
	$(CC) $(CFLAGS) -c main.c -o main.o

clean:
	rm -f program main.o
`
	if err := os.WriteFile(filepath.Join(project, "Makefile"), []byte(makefile), 0o644); err != nil {
		t.Fatal(err)
	}
	original := "#error MIRURI_MAKE_REPAIR_REQUIRED\nint main(void) { return 0; }\n"
	if err := os.WriteFile(filepath.Join(project, "main.c"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeCodex := filepath.Join(root, "codex")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--help" ]; then echo "--ask-for-approval"; exit 0; fi
if [ "${1:-}" = "exec" ] && [ "${2:-}" = "--help" ]; then echo "--json --ephemeral --ignore-user-config --ignore-rules --sandbox --output-schema --output-last-message"; exit 0; fi
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
printf '%s\n' 'do not retain me' > MIRURI_REPAIR_NOTES.md
make -j1 >/dev/null 2>&1
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution","command":"make -j1"}}'
cat >"$out" <<'JSON'
{"status":"repaired","summary":"Added a portable implementation and linked for validation.","changed_files":["main.c","main.o","program","MIRURI_REPAIR_NOTES.md"],"assumptions":[],"remaining_risks":[]}
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
	})
	if err != nil {
		t.Fatal(err)
	}
	repair := result.Manifest.CodexRepairs[0]
	if len(repair.ChangedFiles) != 1 || repair.ChangedFiles[0] != "main.c" {
		t.Fatalf("unexpected accepted patch files: %+v", repair)
	}
	if len(repair.DiscardedChanges) != 3 {
		t.Fatalf("expected object, executable and notes to be discarded: %+v", repair.DiscardedChanges)
	}
	patch, err := os.ReadFile(filepath.Join(result.PackageDir, filepath.FromSlash(repair.PatchFile)))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"main.o", "program", "MIRURI_REPAIR_NOTES", "GIT binary patch"} {
		if strings.Contains(string(patch), forbidden) {
			t.Fatalf("%s leaked into source patch:\n%s", forbidden, patch)
		}
	}
	diagnosticsData, err := os.ReadFile(filepath.Join(result.PackageDir, filepath.FromSlash(repair.DiagnosticsJSONFile)))
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics struct {
		WarningLines           int    `json:"warning_lines"`
		SuppressedWarningLines int    `json:"suppressed_warning_lines"`
		Text                   string `json:"text"`
	}
	if err := json.Unmarshal(diagnosticsData, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if diagnostics.WarningLines < 100 || diagnostics.SuppressedWarningLines < 100 {
		t.Fatalf("warning flood was not compacted: %+v", diagnostics)
	}
	if strings.Count(diagnostics.Text, "synthetic warning flood") > 3 {
		t.Fatalf("warning flood leaked into Codex diagnostic packet:\n%s", diagnostics.Text)
	}
	if len(result.Manifest.Artifacts) != 1 || result.Manifest.Artifacts[0].Kind != "executable" {
		t.Fatalf("final artifact was not produced: %+v", result.Manifest.Artifacts)
	}
}
