package codex

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yuna-r/miruri/internal/model"
	"github.com/yuna-r/miruri/internal/repairworkspace"
)

func TestCheckAndRepairWithFakeCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell fixture")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	output := filepath.Join(root, "output")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := repairworkspace.Init(workspace); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(root, "codex")
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
  echo "codex-cli 9.9.9-test"
  exit 0
fi
if [ "${1:-}" = "login" ] && [ "${2:-}" = "status" ]; then
  echo "Logged in using ChatGPT"
  exit 0
fi
if [ "${1:-}" = "--ask-for-approval" ] && [ "${2:-}" = "never" ]; then
  shift 2
fi
if [ "${1:-}" != "exec" ]; then
  echo "unexpected command" >&2
  exit 9
fi
shift
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message)
      shift
      out="$1"
      ;;
  esac
  shift || true
done
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"thread_test"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution","command":"clang -c main.c"}}'
printf '%s\n' '{"type":"turn.completed","turn_id":"turn_test","usage":{"input_tokens":120,"cached_input_tokens":40,"output_tokens":30,"reasoning_output_tokens":7}}'
echo "Codex test progress" >&2
printf '%s\n' 'int repaired = 1;' > repaired.c
cat >"$out" <<'JSON'
{"status":"repaired","summary":"Added a portable source file.","changed_files":["repaired.c"],"assumptions":[],"remaining_risks":[]}
JSON
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	status, err := Check(context.Background(), fake, AuthChatGPT)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Authenticated || status.AuthMode != "chatgpt" {
		t.Fatalf("unexpected status: %+v", status)
	}

	var progress []ProgressEvent
	result, err := Repair(context.Background(), RepairRequest{
		Binary:        fake,
		Workspace:     workspace,
		Target:        model.TargetProfile{ID: "linux-arm64", OS: "linux", Arch: "arm64", Triple: "aarch64-linux-gnu", ObjectFormat: "elf"},
		BuildSystem:   model.BuildSystemCMake,
		BuildLog:      "fatal error: x86 intrinsic unavailable",
		Attempt:       1,
		OutputDir:     output,
		Timeout:       time.Minute,
		AuthMode:      AuthChatGPT,
		MiruriVersion: "test",
		Progress: func(event ProgressEvent) {
			progress = append(progress, event)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Status != "repaired" {
		t.Fatalf("unexpected response: %+v", result.Response)
	}
	joinedCommand := strings.Join(result.Command, " ")
	approvalIndex := indexOf(result.Command, "--ask-for-approval")
	execIndex := indexOf(result.Command, "exec")
	if approvalIndex < 0 || execIndex < 0 || approvalIndex > execIndex {
		t.Fatalf("--ask-for-approval must precede exec: %v", result.Command)
	}
	for _, expected := range []string{
		"exec", "--json", "--ephemeral", "--ignore-rules", "--ignore-user-config",
		"--sandbox workspace-write", "--ask-for-approval never",
		`web_search="disabled"`, "sandbox_workspace_write.network_access=false",
		"allow_login_shell=false", "check_for_update_on_startup=false", `history.persistence="none"`,
		"project_doc_max_bytes=0", "project_doc_fallback_filenames=[]", `trust_level="untrusted"`,
		`shell_environment_policy.inherit="core"`, "shell_environment_policy.ignore_default_excludes=false",
		"features.apps=false", "features.hooks=false", "features.multi_agent=false",
		"features.skill_mcp_dependency_install=false", "agents.enabled=false",
		`forced_login_method="chatgpt"`, "--output-schema",
	} {
		if !strings.Contains(joinedCommand, expected) {
			t.Fatalf("Codex command is missing %q: %s", expected, joinedCommand)
		}
	}
	if count := strings.Count(joinedCommand, `forced_login_method="chatgpt"`); count != 1 {
		t.Fatalf("ChatGPT auth was not pinned exactly once: count=%d command=%s", count, joinedCommand)
	}
	if result.Events.ThreadID != "thread_test" || result.Events.TurnID != "turn_test" {
		t.Fatalf("missing event IDs: %+v", result.Events)
	}
	if result.Events.InputTokens != 120 || result.Events.CachedInputTokens != 40 || result.Events.OutputTokens != 30 || result.Events.ReasoningOutputTokens != 7 {
		t.Fatalf("unexpected token summary: %+v", result.Events)
	}
	if len(result.Events.Commands) != 1 || result.Events.Commands[0] != "clang -c main.c" {
		t.Fatalf("unexpected commands: %#v", result.Events.Commands)
	}
	if !strings.Contains(result.Stderr, "Codex test progress") {
		t.Fatalf("stderr was not captured: %q", result.Stderr)
	}
	if len(progress) < 2 {
		t.Fatalf("expected progress events, got %#v", progress)
	}
	for _, path := range []string{result.PromptPath, result.DiagnosticsPath, result.DiagnosticsJSONPath, result.EventsPath, result.StderrPath, result.FinalResponsePath, result.SchemaPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing output %s: %v", path, err)
		}
	}
	prompt, err := os.ReadFile(result.PromptPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(prompt), "MIRURI_REPAIR_NOTES.md") && !strings.Contains(string(prompt), "Do not create MIRURI_REPAIR_NOTES.md") {
		t.Fatalf("legacy repair-notes requirement remained in prompt:\n%s", prompt)
	}
	if !strings.Contains(string(prompt), "Miruri-selected build diagnostics") {
		t.Fatalf("structured diagnostic packet missing from prompt:\n%s", prompt)
	}
	if _, err := os.Stat(filepath.Join(workspace, "repaired.c")); err != nil {
		t.Fatal("fake Codex did not edit workspace")
	}
}

func indexOf(values []string, needle string) int {
	for i, value := range values {
		if value == needle {
			return i
		}
	}
	return -1
}

func TestChatGPTModeRemovesAPIKeyEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "should-not-leak")
	t.Setenv("CODEX_API_KEY", "should-not-leak")
	t.Setenv("GITHUB_TOKEN", "should-not-leak")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/should-not-leak")
	t.Setenv("VENDOR_TOKEN", "should-not-leak")
	t.Setenv("OPENAI_BASE_URL", "https://should-not-leak.invalid")
	t.Setenv("MIRURI_SAFE_VALUE", "preserve-me")
	env := codexEnvironment(AuthChatGPT, nil)
	foundSafe := false
	for _, value := range env {
		for _, blocked := range []string{"OPENAI_API_KEY=", "CODEX_API_KEY=", "GITHUB_TOKEN=", "SSH_AUTH_SOCK=", "VENDOR_TOKEN=", "OPENAI_BASE_URL="} {
			if strings.HasPrefix(value, blocked) {
				t.Fatalf("credential-like environment variable leaked into ChatGPT auth mode: %s", value)
			}
		}
		if value == "MIRURI_SAFE_VALUE=preserve-me" {
			foundSafe = true
		}
	}
	if !foundSafe {
		t.Fatal("non-sensitive environment value was removed")
	}
}

func TestCheckRejectsAPIKeyWhenChatGPTIsRequired(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell fixture")
	}
	root := t.TempDir()
	fake := filepath.Join(root, "codex")
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
if [ "${1:-}" = "login" ] && [ "${2:-}" = "status" ]; then echo "Logged in using API key"; exit 0; fi
exit 9
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Check(context.Background(), fake, AuthChatGPT)
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("expected API-key authentication rejection, got %v", err)
	}
	status, err := Check(context.Background(), fake, AuthInherit)
	if err != nil {
		t.Fatal(err)
	}
	if status.AuthMode != "api-key" {
		t.Fatalf("unexpected inherited auth mode: %+v", status)
	}
}

func TestArtifactOnlyViolations(t *testing.T) {
	violations := ArtifactOnlyViolations([]string{"clang -c main.c", "qemu-riscv64 ./app", "wine app.exe"})
	if len(violations) != 2 {
		t.Fatalf("expected two violations, got %#v", violations)
	}
}

func TestCheckRejectsIncompatibleCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell fixture")
	}
	root := t.TempDir()
	fake := filepath.Join(root, "codex")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then echo "codex-cli old"; exit 0; fi
if [ "${1:-}" = "--help" ]; then echo "--ask-for-approval"; exit 0; fi
if [ "${1:-}" = "exec" ] && [ "${2:-}" = "--help" ]; then echo "--json --sandbox"; exit 0; fi
if [ "${1:-}" = "login" ] && [ "${2:-}" = "status" ]; then echo "Logged in using ChatGPT"; exit 0; fi
exit 9
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	status, err := Check(context.Background(), fake, AuthChatGPT)
	if err == nil {
		t.Fatal("expected incompatible CLI rejection")
	}
	if status.Compatible || len(status.MissingFeatures) == 0 {
		t.Fatalf("missing compatibility details: %+v", status)
	}
	if !strings.Contains(err.Error(), "update Codex CLI") {
		t.Fatalf("unexpected compatibility error: %v", err)
	}
}

func TestPortAndAutoPromptsAuthorizeNewPlatformBackend(t *testing.T) {
	base := RepairRequest{
		Target:        model.TargetProfile{ID: "linux-x86_64", OS: "linux", Arch: "x86_64", Triple: "x86_64-unknown-linux-gnu", ObjectFormat: "elf"},
		BuildSystem:   model.BuildSystemCMake,
		Attempt:       1,
		MiruriVersion: "test",
	}
	for _, mode := range []TaskMode{TaskPort, TaskAuto} {
		request := base
		request.Mode = mode
		prompt := buildPrompt(request, "fatal error: windows.h: No such file or directory")
		for _, expected := range []string{
			"A requirement for a new backend is NOT by itself a reason to return \"blocked\"",
			"Port GUI, editor, rendering, audio, input, networking, persistence, printing, shell integration",
		} {
			if !strings.Contains(prompt, expected) {
				t.Fatalf("mode %s prompt missing %q:\n%s", mode, expected, prompt)
			}
		}
	}
}
