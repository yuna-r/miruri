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
	if !strings.Contains(stdout.String(), "Authenticated: true") || !strings.Contains(stdout.String(), "Auth mode:     chatgpt") {
		t.Fatalf("unexpected output:\n%s", stdout.String())
	}
}
