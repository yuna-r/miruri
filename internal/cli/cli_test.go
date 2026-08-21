package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCodexStatusCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell fixture")
	}
	root := t.TempDir()
	fake := filepath.Join(root, "codex")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--help" ]; then echo "--ask-for-approval"; exit 0; fi
if [ "${1:-}" = "exec" ] && [ "${2:-}" = "--help" ]; then echo "--json --ephemeral --ignore-user-config --ignore-rules --sandbox --output-schema --output-last-message"; exit 0; fi
if [ "${1:-}" = "--version" ]; then echo "codex-cli test"; exit 0; fi
if [ "${1:-}" = "login" ] && [ "${2:-}" = "status" ]; then echo "Logged in using ChatGPT"; exit 0; fi
exit 9
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"codex", "status", "--bin", fake}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("unexpected exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Compatible:    true") || !strings.Contains(stdout.String(), "Authenticated: true") || !strings.Contains(stdout.String(), "Auth mode:     chatgpt") {
		t.Fatalf("unexpected output:\n%s", stdout.String())
	}
}

func TestSysrootProvidersCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"sysroot", "providers"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("unexpected exit %d: %s", code, stderr.String())
	}
	for _, targetID := range []string{"linux-x86_64", "linux-arm64", "linux-ppc64le", "linux-riscv64"} {
		if !strings.Contains(stdout.String(), targetID) {
			t.Fatalf("provider output missing %s:\n%s", targetID, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "linux-riscv32") {
		t.Fatalf("riscv32 should remain manual-only:\n%s", stdout.String())
	}
}

func TestPlanReportsAutomaticSysrootProvider(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	fixture := filepath.Join(repoRoot, "fixtures", "hello-c")
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"plan",
		"--target", "linux-ppc64le",
		"--cache-dir", t.TempDir(),
		fixture,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("unexpected exit %d: %s\n%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "trusted managed sysroot provider") {
		t.Fatalf("automatic sysroot was not reported:\n%s", stdout.String())
	}
}

func TestBuildDryRunUsesManagedSysrootProvider(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	fixture := filepath.Join(repoRoot, "fixtures", "hello-c")
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"build",
		"--target", "linux-arm64",
		"--cache-dir", t.TempDir(),
		"--out", t.TempDir(),
		"--dry-run",
		fixture,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("unexpected exit %d: %s\n%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "Sysroot mode:        managed-pending") {
		t.Fatalf("managed dry-run sysroot missing:\n%s", stdout.String())
	}
}
