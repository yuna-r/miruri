package builder

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuna-r/miruri/internal/codex"
	"github.com/yuna-r/miruri/internal/model"
	"github.com/yuna-r/miruri/internal/sysroot"
	"github.com/yuna-r/miruri/internal/target"
	"github.com/yuna-r/miruri/internal/verify"
)

func TestBuildRequestDigestIncludesCodexInstructions(t *testing.T) {
	base := Config{
		Target:     model.TargetProfile{ID: "macos-arm64", OS: "macos", Arch: "arm64", Triple: "arm64-apple-darwin", ObjectFormat: "mach-o"},
		UseCodex:   true,
		CodexMode:  codex.TaskPort,
		MaxRepairs: 12,
		Version:    "test",
	}
	analysis := model.AnalysisReport{ProjectDigest: "sha256:project"}
	resolution := sysroot.Resolution{Mode: "host", TargetID: "macos-arm64", Path: "/"}

	first := base
	first.CodexInstructions = "Prioritize controller input."
	firstDigest, err := buildRequestDigest(first, analysis, model.BuildSystemCMake, resolution)
	if err != nil {
		t.Fatal(err)
	}

	second := base
	second.CodexInstructions = "Prioritize audio fidelity."
	secondDigest, err := buildRequestDigest(second, analysis, model.BuildSystemCMake, resolution)
	if err != nil {
		t.Fatal(err)
	}

	if firstDigest == secondDigest {
		t.Fatal("different Codex instructions produced the same build request digest")
	}
}

func requireNativeCLinker(t *testing.T) {
	t.Helper()
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang is not available")
	}
	root := t.TempDir()
	source := filepath.Join(root, "probe.c")
	binary := filepath.Join(root, "probe")
	if err := os.WriteFile(source, []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(clang, source, "-o", binary)
	if output, err := command.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 600 {
			message = message[:600] + "..."
		}
		t.Skipf("native C linker is unavailable in this host environment: %v: %s", err, message)
	}
}

func TestBuildHelloFixtureForHost(t *testing.T) {
	requireNativeCLinker(t)
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
	requireNativeCLinker(t)
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
	for _, tool := range []string{"git", "clang", "make", "llvm-ar"} {
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

all: libprogram.a

libprogram.a: main.o
	$(AR) rcs $@ $^

main.o: main.c
	@i=0; while [ $$i -lt 120 ]; do echo "system.h:$$i:1: warning: synthetic warning flood" >&2; i=$$((i+1)); done
	$(CC) $(CFLAGS) -c main.c -o main.o

clean:
	rm -f libprogram.a main.o
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
{"status":"repaired","summary":"Added a portable implementation and linked for validation.","changed_files":["main.c","main.o","libprogram.a","MIRURI_REPAIR_NOTES.md"],"assumptions":[],"remaining_risks":[]}
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
	for _, forbidden := range []string{"main.o", "libprogram.a", "MIRURI_REPAIR_NOTES", "GIT binary patch"} {
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
	if len(result.Manifest.Artifacts) != 1 || result.Manifest.Artifacts[0].Kind != "static-library" {
		t.Fatalf("final static-library artifact was not produced: %+v", result.Manifest.Artifacts)
	}
}

func TestBuildAcceptsCodexPortedStatus(t *testing.T) {
	requireNativeCLinker(t)
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
	if err := os.WriteFile(filepath.Join(project, "CMakeLists.txt"), []byte("cmake_minimum_required(VERSION 3.16)\nproject(ported_fixture C)\nadd_executable(ported main.c)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.c"), []byte("#error TARGET_BACKEND_MISSING\nint main(void) { return 0; }\n"), 0o644); err != nil {
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
  case "$1" in --output-last-message) shift; out="$1" ;; esac
  shift || true
done
prompt="$(cat)"
printf '%s' "$prompt" | grep -q 'create a new target platform backend'
cat > main.c <<'C'
#include <stdio.h>
int main(void) { puts("ported"); return 0; }
C
printf '%s\n' '{"type":"thread.started","thread_id":"port_thread"}'
printf '%s\n' '{"type":"turn.completed","turn_id":"port_turn","usage":{"input_tokens":10,"output_tokens":5}}'
cat >"$out" <<'JSON'
{"status":"ported","summary":"Created the authorized target backend path.","changed_files":["main.c"],"assumptions":[],"remaining_risks":[]}
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
		CodexMode:    codex.TaskPort,
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
		t.Fatalf("expected one port attempt: %+v", result.Manifest.CodexRepairs)
	}
	attempt := result.Manifest.CodexRepairs[0]
	if attempt.Status != "ported" || attempt.Mode != string(codex.TaskPort) {
		t.Fatalf("unexpected port provenance: %+v", attempt)
	}
}

func TestBuildDryRunSelectsManagedSysrootWithoutRegistryAccess(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	fixture := filepath.Join(repoRoot, "fixtures", "hello-c")
	profile, err := target.Resolve("linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Build(context.Background(), Config{
		ProjectDir: fixture,
		Target:     profile,
		CacheDir:   t.TempDir(),
		OutDir:     t.TempDir(),
		DryRun:     true,
		MaxRepairs: 0,
		Version:    "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.Sysroot == nil || result.Manifest.Sysroot.Mode != "managed-pending" {
		t.Fatalf("managed sysroot provider was not selected: %+v", result.Manifest.Sysroot)
	}
	if result.Manifest.Sysroot.Source != "docker.io/library/buildpack-deps:bookworm" {
		t.Fatalf("unexpected provider source: %+v", result.Manifest.Sysroot)
	}
	if result.Manifest.Toolchain != nil {
		t.Fatalf("dry run should not require or resolve a local compiler: %+v", result.Manifest.Toolchain)
	}
	if len(result.Manifest.Warnings) < 2 {
		t.Fatalf("dry-run sysroot warning missing: %+v", result.Manifest.Warnings)
	}
	encoded, err := json.Marshal(result.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"artifacts":[]`) {
		t.Fatalf("dry-run manifest must serialize artifacts as an empty array: %s", encoded)
	}
}

func TestDiscoverGCCToolchainAndGenerateCrossFlags(t *testing.T) {
	root := t.TempDir()
	libgcc := filepath.Join(root, "usr", "lib", "gcc", "aarch64-linux-gnu", "13", "libgcc.a")
	if err := os.MkdirAll(filepath.Dir(libgcc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(libgcc, []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile, err := target.Resolve("linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := discoverGCCToolchain(root, profile)
	if err != nil {
		t.Fatal(err)
	}
	expectedPrefix := filepath.Join(root, "usr")
	if prefix != expectedPrefix {
		t.Fatalf("unexpected GCC prefix: got %s, want %s", prefix, expectedPrefix)
	}
	toolchain := llvmToolchain{
		CC:           "/opt/llvm/bin/clang",
		CXX:          "/opt/llvm/bin/clang++",
		AR:           "/opt/llvm/bin/llvm-ar",
		Ranlib:       "/opt/llvm/bin/llvm-ranlib",
		Linker:       "/opt/llvm/bin/ld.lld",
		GCCToolchain: prefix,
	}
	command := compilerCommand(toolchain.CC, profile, root, toolchain)
	for _, required := range []string{
		"--target=aarch64-unknown-linux-gnu",
		"--sysroot=" + root,
		"--gcc-toolchain=" + prefix,
		"-fuse-ld=lld",
	} {
		if !strings.Contains(command, required) {
			t.Fatalf("compiler command missing %q: %s", required, command)
		}
	}
	cmake := generateCMakeToolchain(profile, root, toolchain)
	for _, required := range []string{
		`set(CMAKE_TRY_COMPILE_TARGET_TYPE EXECUTABLE)`,
		`set(CMAKE_C_COMPILER "/opt/llvm/bin/clang")`,
		`set(CMAKE_SYSROOT "` + filepath.ToSlash(root) + `")`,
		`set(CMAKE_C_COMPILER_EXTERNAL_TOOLCHAIN "` + filepath.ToSlash(prefix) + `")`,
		`set(CMAKE_LINKER "/opt/llvm/bin/ld.lld")`,
	} {
		if !strings.Contains(cmake, required) {
			t.Fatalf("CMake toolchain missing %q:\n%s", required, cmake)
		}
	}
	if strings.Contains(cmake, "CMAKE_TRY_COMPILE_TARGET_TYPE STATIC_LIBRARY") {
		t.Fatalf("CMake cross toolchain must not disable linker-based feature probes:\n%s", cmake)
	}
}

func TestDiscoverGCCToolchainChoosesPrefixWithHighestNumericVersion(t *testing.T) {
	root := t.TempDir()
	installations := []struct {
		prefix  string
		version string
	}{
		{prefix: filepath.Join(root, "usr"), version: "9"},
		{prefix: filepath.Join(root, "usr", "local"), version: "12"},
		{prefix: root, version: "11.4.0"},
	}
	for _, installation := range installations {
		libgcc := filepath.Join(installation.prefix, "lib", "gcc", "aarch64-linux-gnu", installation.version, "libgcc.a")
		if err := os.MkdirAll(filepath.Dir(libgcc), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(libgcc, []byte(installation.version), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	profile, err := target.Resolve("linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := discoverGCCToolchain(root, profile)
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(root, "usr", "local")
	if prefix != expected {
		t.Fatalf("numeric GCC version ordering selected %s, want %s", prefix, expected)
	}
	if compareVersion("12", "9") <= 0 || compareVersion("11.4.0", "11.3.1") <= 0 {
		t.Fatal("numeric GCC version comparator is not monotonic")
	}
}

func TestCrossToolchainDoesNotFallBackToHostArchiveTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses executable files without PE headers")
	}
	bin := t.TempDir()
	for _, name := range []string{"clang", "clang++", "ld.lld", "ar", "ranlib", "strip"} {
		path := filepath.Join(bin, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)

	root := t.TempDir()
	libgcc := filepath.Join(root, "usr", "lib", "gcc", "aarch64-linux-gnu", "13", "libgcc.a")
	if err := os.MkdirAll(filepath.Dir(libgcc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(libgcc, []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile, err := target.Resolve("linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	toolchain, err := discoverLLVMToolchainWithSearchDirs(profile, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if toolchain.AR != "" || toolchain.Ranlib != "" || toolchain.Strip != "" {
		t.Fatalf("cross toolchain reused host binutils: %+v", toolchain)
	}
	if err := validateEnvironment(profile, root, toolchain); err == nil || !strings.Contains(err.Error(), "llvm-ar") {
		t.Fatalf("missing cross-safe archive tools were not rejected: %v", err)
	}
}

func TestBuildDryRunOfflineRequiresCachedManagedSysroot(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	profile, err := target.Resolve("linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Build(context.Background(), Config{
		ProjectDir: filepath.Join(repoRoot, "fixtures", "hello-c"),
		Target:     profile,
		CacheDir:   t.TempDir(),
		OutDir:     t.TempDir(),
		DryRun:     true,
		Offline:    true,
		MaxRepairs: 0,
	})
	if err == nil || !strings.Contains(err.Error(), "--offline") {
		t.Fatalf("uncached offline dry run did not fail clearly: %v", err)
	}
}

func TestChooseBuildSystemSupportsMeson(t *testing.T) {
	selected, err := chooseBuildSystem([]model.BuildSystem{model.BuildSystemMake, model.BuildSystemMeson})
	if err != nil {
		t.Fatal(err)
	}
	if selected != model.BuildSystemMeson {
		t.Fatalf("selected %s, want meson", selected)
	}
}

func TestGenerateMesonCrossFileUsesSelectedToolchain(t *testing.T) {
	profile, err := target.Resolve("linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	toolchain := llvmToolchain{
		CC:           "/llvm/bin/clang",
		CXX:          "/llvm/bin/clang++",
		AR:           "/llvm/bin/llvm-ar",
		Strip:        "/llvm/bin/llvm-strip",
		Linker:       "/llvm/bin/ld.lld",
		GCCToolchain: "/sysroot/usr",
	}
	cross, err := generateMesonCrossFile(profile, "/sysroot", toolchain)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"c = ['/llvm/bin/clang', '--target=aarch64-unknown-linux-gnu', '--sysroot=/sysroot', '--gcc-toolchain=/sysroot/usr', '-fuse-ld=lld']",
		"ar = '/llvm/bin/llvm-ar'",
		"strip = '/llvm/bin/llvm-strip'",
		"system = 'linux'",
		"cpu_family = 'aarch64'",
		"sys_root = '/sysroot'",
	} {
		if !strings.Contains(cross, want) {
			t.Fatalf("Meson cross file missing %q:\n%s", want, cross)
		}
	}
}

func TestBuildMesonFixtureForHost(t *testing.T) {
	requireNativeCLinker(t)
	if runtime.GOOS == "windows" {
		t.Skip("Meson fixture currently assumes POSIX-style compiler discovery")
	}
	for _, tool := range []string{"clang", "meson"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available", tool)
		}
	}

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "main.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	meson := `project('miruri-meson-fixture', 'c')
executable('miruri-meson-fixture', 'main.c')
`
	if err := os.WriteFile(filepath.Join(project, "meson.build"), []byte(meson), 0o644); err != nil {
		t.Fatal(err)
	}

	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Build(context.Background(), Config{
		ProjectDir: project,
		Target:     profile,
		OutDir:     t.TempDir(),
		MaxRepairs: 0,
		Version:    "test",
		Timeout:    2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.BuildSystem != model.BuildSystemMeson {
		t.Fatalf("build system = %s, want meson", result.Manifest.BuildSystem)
	}
	if len(result.Manifest.Artifacts) == 0 {
		t.Fatal("Meson build produced no packaged artifact")
	}
}

func TestBuildMesonScriptOnlyPackagesInstallTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Meson fixture uses a POSIX shell")
	}
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("clang is not available")
	}

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "meson.build"), []byte("project('script-only')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeMeson := filepath.Join(t.TempDir(), "meson")
	script := `#!/bin/sh
set -eu
cmd="$1"
shift
case "$cmd" in
  setup)
    mkdir -p "$1"
    ;;
  compile)
    ;;
  install)
    : "${DESTDIR:?DESTDIR must be set}"
    mkdir -p "$DESTDIR/usr/local/bin" "$DESTDIR/usr/local/share/script-only"
    printf '#!/bin/sh\necho script-only\n' > "$DESTDIR/usr/local/bin/script-only"
    chmod 755 "$DESTDIR/usr/local/bin/script-only"
    printf 'payload\n' > "$DESTDIR/usr/local/share/script-only/data.txt"
    ;;
  *)
    echo "unexpected fake meson command: $cmd" >&2
    exit 2
    ;;
esac
`
	if err := os.WriteFile(fakeMeson, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIRURI_MESON", fakeMeson)

	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Build(context.Background(), Config{
		ProjectDir: project,
		Target:     profile,
		OutDir:     t.TempDir(),
		MaxRepairs: 0,
		Version:    "test",
		Timeout:    2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.Assurance != model.AssurancePackaged {
		t.Fatalf("assurance = %s, want %s", result.Manifest.Assurance, model.AssurancePackaged)
	}
	if len(result.Manifest.Artifacts) != 1 {
		t.Fatalf("artifacts = %d, want 1", len(result.Manifest.Artifacts))
	}
	artifact := result.Manifest.Artifacts[0]
	if artifact.Kind != "install-tree" || artifact.Format != "tar" || artifact.Architecture != "portable" || !artifact.ArchitectureOK {
		t.Fatalf("unexpected install-tree artifact: %+v", artifact)
	}
	if _, err := os.Stat(artifact.PackagedPath); err != nil {
		t.Fatalf("install-tree archive missing: %v", err)
	}
	logData, err := os.ReadFile(filepath.Join(result.PackageDir, "build.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), " install -C ") {
		t.Fatalf("build log does not contain Meson install fallback:\n%s", logData)
	}
}

func TestPackageInstallTreeIsDeterministic(t *testing.T) {
	stage := t.TempDir()
	binDir := filepath.Join(stage, "usr", "local", "bin")
	shareDir := filepath.Join(stage, "usr", "local", "share", "demo")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "demo"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "data.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	packageOne := t.TempDir()
	first, ok, err := packageInstallTree(stage, packageOne)
	if err != nil || !ok {
		t.Fatalf("first package: ok=%v err=%v", ok, err)
	}
	if err := os.Chtimes(filepath.Join(shareDir, "data.txt"), time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	packageTwo := t.TempDir()
	second, ok, err := packageInstallTree(stage, packageTwo)
	if err != nil || !ok {
		t.Fatalf("second package: ok=%v err=%v", ok, err)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatalf("install tree archive is not deterministic: %s != %s", first.SHA256, second.SHA256)
	}

	file, err := os.Open(first.PackagedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	tr := tar.NewReader(file)
	seen := map[string]bool{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen[header.Name] = true
	}
	for _, expected := range []string{"usr/local/bin/demo", "usr/local/share/demo/data.txt"} {
		if !seen[expected] {
			t.Fatalf("archive missing %s: %#v", expected, seen)
		}
	}
}

func TestChooseBuildSystemPrefersAutotoolsOverGeneratedMakefile(t *testing.T) {
	selected, err := chooseBuildSystem([]model.BuildSystem{model.BuildSystemMake, model.BuildSystemAutotools})
	if err != nil {
		t.Fatal(err)
	}
	if selected != model.BuildSystemAutotools {
		t.Fatalf("selected %s, want autotools", selected)
	}
}

func TestBuildAutotoolsFixtureForHost(t *testing.T) {
	requireNativeCLinker(t)
	if runtime.GOOS == "windows" {
		t.Skip("Autotools adapter currently requires a POSIX shell")
	}
	for _, tool := range []string{"clang", "make", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available", tool)
		}
	}

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "main.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configure := `#!/bin/sh
set -eu
srcdir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cat > Makefile <<MAKE
SRCDIR := $srcdir
all: autotools-fixture

autotools-fixture:
	\$(CC) "\$(SRCDIR)/main.c" -o autotools-fixture
MAKE
`
	if err := os.WriteFile(filepath.Join(project, "configure"), []byte(configure), 0o755); err != nil {
		t.Fatal(err)
	}

	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Build(context.Background(), Config{
		ProjectDir: project,
		Target:     profile,
		OutDir:     t.TempDir(),
		MaxRepairs: 0,
		Version:    "test",
		Timeout:    2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.BuildSystem != model.BuildSystemAutotools {
		t.Fatalf("build system = %s, want autotools", result.Manifest.BuildSystem)
	}
	if len(result.Manifest.Artifacts) == 0 {
		t.Fatal("Autotools build produced no packaged artifact")
	}
}

func TestNeedsAutoreconfWhenConfigureIsMissing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "configure.ac"), []byte("AC_INIT([fixture], [1])\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !needsAutoreconf(root) {
		t.Fatal("configure.ac without configure must require autoreconf")
	}
}

func TestEnsureManagedMesonDownloadsVerifiesAndReusesCacheOffline(t *testing.T) {
	var wheel bytes.Buffer
	archive := zip.NewWriter(&wheel)
	for name, content := range map[string]string{
		"mesonbuild/__init__.py":  "",
		"mesonbuild/mesonmain.py": "def main():\n    return 0\n",
	} {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(wheel.Bytes())
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write(wheel.Bytes())
	}))
	defer server.Close()

	spec := mesonSpec{
		Version: "test",
		Name:    "meson-test-py3-none-any.whl",
		URL:     server.URL + "/meson.whl",
		SHA256:  fmt.Sprintf("%x", digest),
	}
	cache := t.TempDir()
	site, err := ensureManagedMeson(context.Background(), cache, false, spec, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("download count = %d, want 1", requests.Load())
	}
	if _, err := os.Stat(filepath.Join(site, "mesonbuild", "mesonmain.py")); err != nil {
		t.Fatalf("managed Meson entry point missing: %v", err)
	}

	reused, err := ensureManagedMeson(context.Background(), cache, true, spec, io.Discard)
	if err != nil {
		t.Fatalf("offline reuse failed: %v", err)
	}
	if reused != site {
		t.Fatalf("offline reuse path = %s, want %s", reused, site)
	}
	if requests.Load() != 1 {
		t.Fatalf("offline reuse unexpectedly downloaded again: %d requests", requests.Load())
	}

	wheelPath := filepath.Join(cache, "tools", "meson", spec.Version, spec.Name)
	if err := os.WriteFile(wheelPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureManagedMeson(context.Background(), cache, true, spec, io.Discard); err == nil || !strings.Contains(err.Error(), "--offline") {
		t.Fatalf("tampered offline cache was not rejected clearly: %v", err)
	}
}

func TestEnsureManagedMesonRejectsDigestMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("not-the-expected-wheel"))
	}))
	defer server.Close()

	spec := mesonSpec{
		Version: "bad",
		Name:    "meson-bad.whl",
		URL:     server.URL + "/meson.whl",
		SHA256:  strings.Repeat("0", 64),
	}
	_, err := ensureManagedMeson(context.Background(), t.TempDir(), false, spec, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("digest mismatch was not rejected: %v", err)
	}
}

func TestPrependEnvironmentPathPrefersManagedMeson(t *testing.T) {
	environment := []string{"PYTHONPATH=/existing", "PATH=/bin"}
	updated := prependEnvironmentPath(environment, "PYTHONPATH", []string{"/managed"})
	for _, entry := range updated {
		if strings.HasPrefix(entry, "PYTHONPATH=") {
			want := "PYTHONPATH=/managed" + string(os.PathListSeparator) + "/existing"
			if entry != want {
				t.Fatalf("PYTHONPATH = %q, want %q", entry, want)
			}
			return
		}
	}
	t.Fatal("PYTHONPATH missing")
}

func TestPackageSimpleMacOSAppRewritesPythonLauncherAndResources(t *testing.T) {
	stage := t.TempDir()
	binDir := filepath.Join(stage, "usr", "local", "bin")
	shareDir := filepath.Join(stage, "usr", "local", "share")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(shareDir, "drawing", "drawing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(shareDir, "applications"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(shareDir, "locale"), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := `#!/opt/homebrew/bin/python3
import os
import gettext
import locale
pkgdatadir = '/usr/local/share/drawing'
localedir = '/usr/local/share/locale'
locale.bindtextdomain('drawing', localedir)
locale.textdomain('drawing')
print(pkgdatadir, localedir)
`
	if err := os.WriteFile(filepath.Join(binDir, "drawing"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "drawing", "drawing", "main.py"), []byte("pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	desktop := "[Desktop Entry]\nName=Drawing\nExec=drawing\n"
	if err := os.WriteFile(filepath.Join(shareDir, "applications", "com.github.maoschanz.drawing.desktop"), []byte(desktop), 0o644); err != nil {
		t.Fatal(err)
	}

	packageDir := t.TempDir()
	artifact, ok, err := packageSimpleMacOSApp(stage, packageDir, "drawing")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected macOS app bundle")
	}
	if artifact.Kind != "application-bundle" || artifact.Format != "macos-app-tar" || artifact.SHA256 == "" {
		t.Fatalf("unexpected artifact: %+v", artifact)
	}
	appDir := filepath.Join(packageDir, "artifacts", "Drawing.app")
	bundleLauncher := filepath.Join(appDir, "Contents", "MacOS", "drawing")
	wrapperData, err := os.ReadFile(bundleLauncher)
	if err != nil {
		t.Fatal(err)
	}
	wrapperText := string(wrapperData)
	for _, want := range []string{
		"try_python",
		"import gi; import cairo",
		"python@3.14",
		"python@3.13",
		"Cellar",
		"pygobject3",
		"py3cairo",
		"__miruri_bundle_entry__.py",
	} {
		if !strings.Contains(wrapperText, want) {
			t.Fatalf("runtime wrapper missing %q:\n%s", want, wrapperText)
		}
	}
	bundlePythonLauncher := filepath.Join(appDir, "Contents", "MacOS", "__miruri_bundle_entry__.py")
	data, err := os.ReadFile(bundlePythonLauncher)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(appDir, "Contents", "MacOS", "drawing.py")); !os.IsNotExist(err) {
		t.Fatalf("bundle entrypoint shadows Python package drawing: err=%v", err)
	}
	text := string(data)
	for _, want := range []string{
		"_miruri_resources = os.path.abspath",
		"pkgdatadir = os.path.join(_miruri_resources, 'share', 'drawing')",
		"localedir = os.path.join(_miruri_resources, 'share', 'locale')",
		"GSETTINGS_SCHEMA_DIR",
		"gettext.bindtextdomain('drawing', localedir)",
		"gettext.textdomain('drawing')",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rewritten Python payload missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "pkgdatadir = '/usr/local/share/drawing'") {
		t.Fatalf("Python payload still contains fixed pkgdatadir:\n%s", text)
	}
	if strings.Contains(text, "locale.bindtextdomain(") || strings.Contains(text, "locale.textdomain(") {
		t.Fatalf("Python payload still contains non-portable locale gettext calls:\n%s", text)
	}
	plistData, err := os.ReadFile(filepath.Join(appDir, "Contents", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	plist := string(plistData)
	if !strings.Contains(plist, "com.github.maoschanz.drawing") || !strings.Contains(plist, "<string>Drawing</string>") {
		t.Fatalf("unexpected Info.plist:\n%s", plist)
	}
	if _, err := os.Stat(filepath.Join(appDir, "Contents", "Resources", "share", "drawing", "drawing", "main.py")); err != nil {
		t.Fatalf("bundled resource missing: %v", err)
	}
	if _, err := os.Stat(artifact.PackagedPath); err != nil {
		t.Fatalf("app tar missing: %v", err)
	}
}

func TestDryRunArtifactSetIsStrictlyVerifiedAndReusable(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "CMakeLists.txt"), []byte("cmake_minimum_required(VERSION 3.16)\nproject(reuse C)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	config := Config{
		ProjectDir: project,
		Target:     profile,
		OutDir:     filepath.Join(root, "dist"),
		DryRun:     true,
		Version:    "test",
	}
	first, err := Build(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	report, err := verify.ArtifactSet(first.PackageDir, verify.Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Valid || len(report.Findings) != 0 {
		t.Fatalf("fresh artifact set failed strict verification: %+v", report.Findings)
	}
	config.Reuse = true
	second, err := Build(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reused || second.Manifest.BuildID != first.Manifest.BuildID {
		t.Fatalf("matching artifact set was not reused: first=%+v second=%+v", first.Manifest, second.Manifest)
	}
}

func TestPublishFailurePreservesPreviousArtifactSet(t *testing.T) {
	root := t.TempDir()
	finalDir := filepath.Join(root, "target")
	stagingDir := filepath.Join(root, "staging")
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(finalDir, "marker"), []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A missing manifest makes the staging set ineligible for publication.
	if err := publishArtifactSet(stagingDir, finalDir); err == nil {
		t.Fatal("expected incomplete staging publication to fail")
	}
	data, err := os.ReadFile(filepath.Join(finalDir, "marker"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "previous" {
		t.Fatalf("previous artifact set was modified: %q", data)
	}
}

func TestDryRunReuseIgnoresCustomOutputDirectoryInsideProject(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "CMakeLists.txt"), []byte("cmake_minimum_required(VERSION 3.16)\nproject(custom_output C)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	customOut := filepath.Join(project, "release-cache")
	config := Config{
		ProjectDir: project,
		Target:     profile,
		OutDir:     customOut,
		DryRun:     true,
		Version:    "test",
	}
	first, err := Build(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.ProjectDigest == "" {
		t.Fatal("first build did not record a project digest")
	}
	if err := os.WriteFile(filepath.Join(customOut, "generated-source.c"), []byte("#include <cuda_runtime.h>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	config.Reuse = true
	second, err := Build(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reused {
		t.Fatal("custom in-project output prevented verified reuse")
	}
	if second.Manifest.ProjectDigest != first.Manifest.ProjectDigest || second.Manifest.BuildID != first.Manifest.BuildID {
		t.Fatalf("custom output changed build identity: first=%+v second=%+v", first.Manifest, second.Manifest)
	}
}

func TestBuildRejectsOutputSymlinkResolvingToProjectRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated privileges on Windows")
	}
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "CMakeLists.txt"), []byte("cmake_minimum_required(VERSION 3.16)\nproject(output_boundary C)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	aliasParent := t.TempDir()
	outAlias := filepath.Join(aliasParent, "project-output")
	if err := os.Symlink(project, outAlias); err != nil {
		t.Fatal(err)
	}
	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), Config{
		ProjectDir: project,
		Target:     profile,
		OutDir:     outAlias,
		DryRun:     true,
		Version:    "test",
	}); err == nil || !strings.Contains(err.Error(), "must not be the project root") {
		t.Fatalf("output symlink resolving to project root was not rejected: %v", err)
	}
}

func TestBuildPortBootstrapsUnknownBuildSystemAndRedetectsCMake(t *testing.T) {
	requireNativeCLinker(t)
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
	// Mimic a Visual Studio/UWP-only source tree: source exists, but none of
	// Miruri's portable build systems are present yet.
	if err := os.WriteFile(filepath.Join(project, "MarbleMaze.vcxproj"), []byte("<Project></Project>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
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
  case "$1" in --output-last-message) shift; out="$1" ;; esac
  shift || true
done
prompt="$(cat)"
printf '%s' "$prompt" | grep -q 'feature-preserving platform port'
cat > CMakeLists.txt <<'CMAKE'
cmake_minimum_required(VERSION 3.16)
project(miruri_bootstrap_port C)
add_executable(miruri-bootstrap main.c)
CMAKE
printf '%s\n' '{"type":"thread.started","thread_id":"bootstrap_thread"}'
printf '%s\n' '{"type":"turn.completed","turn_id":"bootstrap_turn","usage":{"input_tokens":10,"output_tokens":5}}'
cat >"$out" <<'JSON'
{"status":"ported","summary":"Generated a portable CMake build system.","changed_files":["CMakeLists.txt"],"assumptions":[],"remaining_risks":[]}
JSON
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	result, err := Build(context.Background(), Config{
		ProjectDir:   project,
		Target:       profile,
		OutDir:       filepath.Join(root, "dist"),
		UseCodex:     true,
		CodexMode:    codex.TaskPort,
		MaxRepairs:   1,
		CodexBinary:  fakeCodex,
		CodexAuth:    codex.AuthChatGPT,
		CodexTimeout: time.Minute,
		Version:      "test",
		Timeout:      2 * time.Minute,
		Progress:     &progress,
	})
	if err != nil {
		t.Fatalf("bootstrap port failed: %v\nprogress:\n%s", err, progress.String())
	}
	if result.Manifest.BuildSystem != model.BuildSystemCMake {
		t.Fatalf("build system = %s, want cmake", result.Manifest.BuildSystem)
	}
	if len(result.Manifest.Artifacts) == 0 {
		t.Fatal("bootstrap port produced no artifacts")
	}
	if !strings.Contains(progress.String(), "no supported native build system detected") {
		t.Fatalf("bootstrap progress message missing:\n%s", progress.String())
	}
	if !strings.Contains(progress.String(), "unknown -> cmake after Codex port") {
		t.Fatalf("build-system re-detection message missing:\n%s", progress.String())
	}
}

func TestBuildPortFidelityGateRejectsReplacementAndRetries(t *testing.T) {
	requireNativeCLinker(t)
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
	if err := os.WriteFile(filepath.Join(project, "LegacyOnly.vcxproj"), []byte("<Project></Project>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "original_a.c"), []byte("int original_helper(void); int main(void) { return original_helper() == 7 ? 0 : 1; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "original_b.c"), []byte("int original_helper(void) { return 7; }\n"), 0o644); err != nil {
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
  case "$1" in --output-last-message) shift; out="$1" ;; esac
  shift || true
done
prompt="$(cat)"
printf '%s' "$prompt" | grep -q 'NOT a clone, remake, visual approximation'
printf '%s' "$prompt" | grep -q 'original_a.c'
printf '%s' "$prompt" | grep -q 'original_b.c'
if [ ! -f replacement.c ]; then
  cat > replacement.c <<'C'
int main(void) { return 0; }
C
  cat > CMakeLists.txt <<'CMAKE'
cmake_minimum_required(VERSION 3.16)
project(replacement_port C)
add_executable(port replacement.c)
CMAKE
  summary='Created a target backend.'
  changed='["replacement.c","CMakeLists.txt"]'
else
  printf '%s' "$prompt" | grep -q 'feature-fidelity gate rejected linked artifact'
  cat > CMakeLists.txt <<'CMAKE'
cmake_minimum_required(VERSION 3.16)
project(preserved_port C)
add_executable(port original_a.c original_b.c)
CMAKE
  summary='Rewired the target build to preserve the original implementation.'
  changed='["CMakeLists.txt"]'
fi
printf '%s\n' '{"type":"thread.started","thread_id":"fidelity_thread"}'
printf '%s\n' '{"type":"turn.completed","turn_id":"fidelity_turn","usage":{"input_tokens":10,"output_tokens":5}}'
printf '{"status":"ported","summary":"%s","changed_files":%s,"assumptions":[],"remaining_risks":[]}\n' "$summary" "$changed" > "$out"
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	result, err := Build(context.Background(), Config{
		ProjectDir:   project,
		Target:       profile,
		OutDir:       filepath.Join(root, "dist"),
		UseCodex:     true,
		CodexMode:    codex.TaskPort,
		MaxRepairs:   2,
		CodexBinary:  fakeCodex,
		CodexAuth:    codex.AuthChatGPT,
		CodexTimeout: time.Minute,
		Version:      "test",
		Timeout:      2 * time.Minute,
		Progress:     &progress,
	})
	if err != nil {
		t.Fatalf("fidelity retry port failed: %v\nprogress:\n%s", err, progress.String())
	}
	if len(result.Manifest.CodexRepairs) != 2 {
		t.Fatalf("expected two Codex attempts, got %+v", result.Manifest.CodexRepairs)
	}
	log := progress.String()
	if !strings.Contains(log, "feature-fidelity gate rejected linked artifact") {
		t.Fatalf("fidelity rejection missing from progress:\n%s", log)
	}
	if !strings.Contains(log, "target build compiles 0 pre-existing translation unit(s) out of 2") {
		t.Fatalf("original-source reuse diagnostic missing:\n%s", log)
	}
	if !strings.Contains(log, "feature-fidelity gate: PASS; target build reuses 2/2") {
		t.Fatalf("fidelity pass missing after retry:\n%s", log)
	}
}

func TestBuildReloadsInstructionsFileBetweenCodexAttempts(t *testing.T) {
	requireNativeCLinker(t)
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
	if err := os.WriteFile(filepath.Join(project, "LegacyOnly.vcxproj"), []byte("<Project></Project>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "original.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	instructionsFile := filepath.Join(root, "live.md")
	if err := os.WriteFile(instructionsFile, []byte("FIRST LIVE INSTRUCTION\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fakeCodex := filepath.Join(root, "codex")
	script := fmt.Sprintf(`#!/bin/sh
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
  case "$1" in --output-last-message) shift; out="$1" ;; esac
  shift || true
done
prompt="$(cat)"
if printf '%%s' "$prompt" | grep -q 'Attempt: 1'; then
  printf '%%s' "$prompt" | grep -q 'FIRST LIVE INSTRUCTION'
  printf '%%s' "$prompt" | grep -q 'FIXED INLINE INSTRUCTION'
  printf 'SECOND LIVE INSTRUCTION\n' > %q
  printf '%%s\n' '{"type":"thread.started","thread_id":"live_1"}'
  printf '%%s\n' '{"type":"turn.completed","turn_id":"live_turn_1","usage":{"input_tokens":10,"output_tokens":5}}'
  printf '%%s\n' '{"status":"blocked","summary":"Continue after live instruction edit.","changed_files":[],"assumptions":[],"remaining_risks":["Portable build system remains."]}' > "$out"
  exit 0
fi
printf '%%s' "$prompt" | grep -q 'SECOND LIVE INSTRUCTION'
if printf '%%s' "$prompt" | grep -q 'FIRST LIVE INSTRUCTION'; then exit 19; fi
printf '%%s' "$prompt" | grep -q 'FIXED INLINE INSTRUCTION'
cat > CMakeLists.txt <<'CMAKE'
cmake_minimum_required(VERSION 3.16)
project(live_reload C)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
add_executable(port original.c)
CMAKE
printf '%%s\n' '{"type":"thread.started","thread_id":"live_2"}'
printf '%%s\n' '{"type":"turn.completed","turn_id":"live_turn_2","usage":{"input_tokens":10,"output_tokens":5}}'
printf '%%s\n' '{"status":"ported","summary":"Portable build created after live reload.","changed_files":["CMakeLists.txt"],"assumptions":[],"remaining_risks":[]}' > "$out"
`, instructionsFile)
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := os.ReadFile(instructionsFile)
	if err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	result, err := Build(context.Background(), Config{
		ProjectDir:              project,
		Target:                  profile,
		OutDir:                  filepath.Join(root, "dist"),
		UseCodex:                true,
		CodexMode:               codex.TaskPort,
		MaxRepairs:              2,
		CodexBinary:             fakeCodex,
		CodexAuth:               codex.AuthChatGPT,
		CodexTimeout:            time.Minute,
		CodexInstructions:       strings.TrimSpace(string(initial)) + "\n\nFIXED INLINE INSTRUCTION",
		CodexInstructionsInline: "FIXED INLINE INSTRUCTION",
		CodexInstructionsFile:   instructionsFile,
		Version:                 "test",
		Timeout:                 2 * time.Minute,
		Progress:                &progress,
	})
	if err != nil {
		t.Fatalf("live instructions reload build failed: %v\nprogress:\n%s", err, progress.String())
	}
	if len(result.Manifest.CodexRepairs) != 2 {
		t.Fatalf("expected two Codex attempts, got %+v", result.Manifest.CodexRepairs)
	}
	if !strings.Contains(progress.String(), "Miruri Codex instructions: reloaded") {
		t.Fatalf("live reload diagnostic missing:\n%s", progress.String())
	}
}

func TestDeclaredFeatureLossRecognizesReplacementAdmissions(t *testing.T) {
	bad := []string{
		"The original physics implementation is approximated by native gameplay logic.",
		"The renderer does not yet decode the original SDKMesh assets.",
		"The WMA background track is packaged but not played.",
		"A procedural scene is the closest locally implementable equivalent.",
	}
	for _, value := range bad {
		if !declaredFeatureLoss(value) {
			t.Fatalf("expected fidelity loss to be recognized: %q", value)
		}
	}
	good := []string{
		"The target artifact was compiled but not executed.",
		"Runtime validation was intentionally not performed.",
	}
	for _, value := range good {
		if declaredFeatureLoss(value) {
			t.Fatalf("runtime-only caveat must not be treated as feature loss: %q", value)
		}
	}
}

func TestBuildPortRetriesBlockedWithoutChanges(t *testing.T) {
	requireNativeCLinker(t)
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
	if err := os.WriteFile(filepath.Join(project, "LegacyOnly.vcxproj"), []byte("<Project></Project>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "original.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
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
  case "$1" in --output-last-message) shift; out="$1" ;; esac
  shift || true
done
prompt="$(cat)"
printf '%s' "$prompt" | grep -q 'NOT a reason to stop'
printf '%s' "$prompt" | grep -q 'status "progress"'
if printf '%s' "$prompt" | grep -q 'Attempt: 1'; then
  printf '%s\n' '{"type":"thread.started","thread_id":"blocked_thread"}'
  printf '%s\n' '{"type":"turn.completed","turn_id":"blocked_turn","usage":{"input_tokens":10,"output_tokens":5}}'
  printf '%s\n' '{"status":"blocked","summary":"A broad native backend is required.","changed_files":[],"assumptions":[],"remaining_risks":["Rendering and input still require target-native adapters."]}' > "$out"
  exit 0
fi
printf '%s' "$prompt" | grep -q 'Previous port attempt 1 returned status "blocked"'
printf '%s' "$prompt" | grep -q 'This attempt must make edits'
cat > CMakeLists.txt <<'CMAKE'
cmake_minimum_required(VERSION 3.16)
project(blocked_retry C)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
add_executable(port original.c)
CMAKE
printf '%s\n' '{"type":"thread.started","thread_id":"ported_thread"}'
printf '%s\n' '{"type":"turn.completed","turn_id":"ported_turn","usage":{"input_tokens":10,"output_tokens":5}}'
printf '%s\n' '{"status":"ported","summary":"Created the portable build while preserving the original implementation.","changed_files":["CMakeLists.txt"],"assumptions":[],"remaining_risks":[]}' > "$out"
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	result, err := Build(context.Background(), Config{
		ProjectDir:   project,
		Target:       profile,
		OutDir:       filepath.Join(root, "dist"),
		UseCodex:     true,
		CodexMode:    codex.TaskPort,
		MaxRepairs:   2,
		CodexBinary:  fakeCodex,
		CodexAuth:    codex.AuthChatGPT,
		CodexTimeout: time.Minute,
		Version:      "test",
		Timeout:      2 * time.Minute,
		Progress:     &progress,
	})
	if err != nil {
		t.Fatalf("blocked port should continue and recover: %v\nprogress:\n%s", err, progress.String())
	}
	if len(result.Manifest.CodexRepairs) != 2 {
		t.Fatalf("expected two Codex attempts, got %+v", result.Manifest.CodexRepairs)
	}
	if result.Manifest.CodexRepairs[0].Status != "blocked" || result.Manifest.CodexRepairs[1].Status != "ported" {
		t.Fatalf("unexpected Codex status sequence: %+v", result.Manifest.CodexRepairs)
	}
	log := progress.String()
	if !strings.Contains(log, `Codex status "blocked" is non-terminal`) {
		t.Fatalf("blocked continuation diagnostic missing:\n%s", log)
	}
	if !strings.Contains(log, "Miruri build system: unknown -> cmake after Codex port") {
		t.Fatalf("build-system bootstrap after blocked retry missing:\n%s", log)
	}
}

func TestNonBlockingPortCaveatClassification(t *testing.T) {
	advisory := []string{
		"今回の変更を含む完全な再リンクはbuild directoryの書き込み権限不足により未確認です。",
		"WMA decoder fallback、UI orientation/layout、multi-touch、Metal描画、入力、物理、音声の実機挙動は未検証です。",
		"motion対応controllerがないMacにはaccelerometer相当入力がありません。",
		"UI overlayは毎frame CPU bitmapとMetal textureを再生成しており、最適化余地があります。",
		"The target artifact was not executed.",
		"MacGame still duplicates substantial orchestration from MarbleMazeMain, including state transitions, checkpoint progression, scoring, camera updates, and UI state. The original UserInterface, SampleOverlay, and LoadScreen implementations also remain unported to native macOS services.",
	}
	for _, value := range advisory {
		if !nonBlockingPortCaveat(value) {
			t.Fatalf("expected advisory caveat: %q", value)
		}
	}
	blocking := []string{
		"AVAudioEnvironmentNodeでは元の独立room sendと4-channel output matrixを完全には再現できません。",
		"Rendering and input still require target-native adapters.",
		"Palette-indexed DDS files used by the shipped project are unsupported.",
		"MacGame duplicates orchestration and the checkpoint scoring behavior differs from the original.",
		"MacGame duplicates orchestration but the original pause state is missing.",
	}
	for _, value := range blocking {
		if nonBlockingPortCaveat(value) {
			t.Fatalf("project-relevant risk must not be advisory: %q", value)
		}
	}
}

func TestBuildPromotesProgressAfterSuccessfulRebuildWithOnlyAdvisoryRisks(t *testing.T) {
	requireNativeCLinker(t)
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
	if err := os.WriteFile(filepath.Join(project, "LegacyOnly.vcxproj"), []byte("<Project></Project>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "original.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
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
  case "$1" in --output-last-message) shift; out="$1" ;; esac
  shift || true
done
prompt="$(cat)"
printf '%s' "$prompt" | grep -q 'remaining_risks array is reserved for known, project-relevant unresolved fidelity blockers'
cat > CMakeLists.txt <<'CMAKE'
cmake_minimum_required(VERSION 3.16)
project(progress_advisory C)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
add_executable(port original.c)
CMAKE
printf '%s\n' '{"type":"thread.started","thread_id":"progress_advisory"}'
printf '%s\n' '{"type":"turn.completed","turn_id":"progress_advisory_turn","usage":{"input_tokens":10,"output_tokens":5}}'
printf '%s\n' '{"status":"progress","summary":"Port implementation is linked; advisory validation caveats remain.","changed_files":["CMakeLists.txt"],"assumptions":[],"remaining_risks":["今回の変更を含む完全な再リンクはbuild directoryの書き込み権限不足により未確認です。","実機挙動は未検証です。","motion対応controllerがないMacにはaccelerometer相当入力がありません。","UI overlayには最適化余地があります。","MacGame still duplicates substantial orchestration from MarbleMazeMain, including state transitions, checkpoint progression, scoring, camera updates, and UI state. The original UserInterface, SampleOverlay, and LoadScreen implementations also remain unported to native macOS services."]}' > "$out"
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	result, err := Build(context.Background(), Config{
		ProjectDir:   project,
		Target:       profile,
		OutDir:       filepath.Join(root, "dist"),
		UseCodex:     true,
		CodexMode:    codex.TaskPort,
		MaxRepairs:   2,
		CodexBinary:  fakeCodex,
		CodexAuth:    codex.AuthChatGPT,
		CodexTimeout: time.Minute,
		Version:      "test",
		Timeout:      2 * time.Minute,
		Progress:     &progress,
	})
	if err != nil {
		t.Fatalf("advisory progress should be promoted after Miruri rebuild: %v\nprogress:\n%s", err, progress.String())
	}
	if len(result.Manifest.CodexRepairs) != 1 {
		t.Fatalf("expected one Codex attempt, got %+v", result.Manifest.CodexRepairs)
	}
	log := progress.String()
	if !strings.Contains(log, `promoted Codex status "progress"`) {
		t.Fatalf("progress promotion diagnostic missing:\n%s", log)
	}
	if !strings.Contains(log, "feature-fidelity gate: PASS") {
		t.Fatalf("fidelity pass missing:\n%s", log)
	}
}

func TestBuildProgressStillRetriesProjectRelevantRemainingRisk(t *testing.T) {
	requireNativeCLinker(t)
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
	if err := os.WriteFile(filepath.Join(project, "LegacyOnly.vcxproj"), []byte("<Project></Project>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "original.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
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
  case "$1" in --output-last-message) shift; out="$1" ;; esac
  shift || true
done
prompt="$(cat)"
if printf '%s' "$prompt" | grep -q 'Attempt: 1'; then
  cat > CMakeLists.txt <<'CMAKE'
cmake_minimum_required(VERSION 3.16)
project(progress_blocker C)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
add_executable(port original.c)
CMAKE
  printf '%s\n' '{"type":"thread.started","thread_id":"progress_blocker_1"}'
  printf '%s\n' '{"type":"turn.completed","turn_id":"progress_blocker_turn_1","usage":{"input_tokens":10,"output_tokens":5}}'
  printf '%s\n' '{"status":"progress","summary":"A real fidelity gap remains.","changed_files":["CMakeLists.txt"],"assumptions":[],"remaining_risks":["AVAudioEnvironmentNodeでは元の独立room sendと4-channel output matrixを完全には再現できません。"]}' > "$out"
  exit 0
fi
printf '%s' "$prompt" | grep -q 're-evaluate project relevance, then fix if actually exercised'
printf '%s\n' '{"type":"thread.started","thread_id":"progress_blocker_2"}'
printf '%s\n' '{"type":"turn.completed","turn_id":"progress_blocker_turn_2","usage":{"input_tokens":10,"output_tokens":5}}'
printf '%s\n' '{"status":"ported","summary":"Project-relevant fidelity gap resolved.","changed_files":[],"assumptions":[],"remaining_risks":[]}' > "$out"
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	result, err := Build(context.Background(), Config{
		ProjectDir:   project,
		Target:       profile,
		OutDir:       filepath.Join(root, "dist"),
		UseCodex:     true,
		CodexMode:    codex.TaskPort,
		MaxRepairs:   2,
		CodexBinary:  fakeCodex,
		CodexAuth:    codex.AuthChatGPT,
		CodexTimeout: time.Minute,
		Version:      "test",
		Timeout:      2 * time.Minute,
		Progress:     &progress,
	})
	if err != nil {
		t.Fatalf("project-relevant risk should retry and recover: %v\nprogress:\n%s", err, progress.String())
	}
	if len(result.Manifest.CodexRepairs) != 2 {
		t.Fatalf("expected two Codex attempts, got %+v", result.Manifest.CodexRepairs)
	}
	if !strings.Contains(progress.String(), "unresolved project-relevant risk") {
		t.Fatalf("project-relevant blocker diagnostic missing:\n%s", progress.String())
	}
}

func TestPreservePortedSourceCopiesFinalCodexWorkspace(t *testing.T) {
	sourceDir := filepath.Join(t.TempDir(), "source")
	packageDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourceDir, "C++", "Mac"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "C++", "Mac", "MacGame.mm"), []byte("// final ported source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sourceDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, ".git", "config"), []byte("temporary git metadata"), 0o644); err != nil {
		t.Fatal(err)
	}
	bc := &buildContext{
		sourceDir:  sourceDir,
		packageDir: packageDir,
		codexRepairs: []model.CodexRepairAttempt{{
			Attempt: 1,
			Status:  "ported",
		}},
	}
	rel, err := bc.preservePortedSource()
	if err != nil {
		t.Fatal(err)
	}
	if rel != "ported-source" {
		t.Fatalf("ported source dir = %q, want ported-source", rel)
	}
	if _, err := os.Stat(filepath.Join(packageDir, "ported-source", "C++", "Mac", "MacGame.mm")); err != nil {
		t.Fatalf("final ported source was not preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(packageDir, "ported-source", ".git", "config")); !os.IsNotExist(err) {
		t.Fatalf("disposable repair git metadata should not be published, stat err=%v", err)
	}
}
