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
	"github.com/yuna-r/miruri/internal/model"
	"github.com/yuna-r/miruri/internal/target"
)

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
