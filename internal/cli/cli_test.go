package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yuna-r/miruri/internal/model"
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

func TestRootHelpListsArtifactLifecycleCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("unexpected exit %d: %s", code, stderr.String())
	}
	for _, command := range []string{"matrix", "verify", "compare"} {
		if !strings.Contains(stdout.String(), command) {
			t.Fatalf("root help is missing %q:\n%s", command, stdout.String())
		}
	}
}

func TestArtifactLifecycleCommands(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	fixture := filepath.Join(repoRoot, "fixtures", "hello-c")
	outDir := t.TempDir()

	var buildOut, buildErr bytes.Buffer
	code := Run([]string{
		"build",
		"--target", "host",
		"--out", outDir,
		"--dry-run",
		fixture,
	}, &buildOut, &buildErr)
	if code != 0 {
		t.Fatalf("dry-run build exit %d: %s\n%s", code, buildErr.String(), buildOut.String())
	}

	manifestMatches, err := filepath.Glob(filepath.Join(outDir, "*", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifestMatches) != 1 {
		t.Fatalf("expected one manifest, found %d under %s", len(manifestMatches), outDir)
	}
	artifactSet := filepath.Dir(manifestMatches[0])

	var verifyOut, verifyErr bytes.Buffer
	code = Run([]string{"verify", "--strict", artifactSet}, &verifyOut, &verifyErr)
	if code != 0 {
		t.Fatalf("verify exit %d: %s\n%s", code, verifyErr.String(), verifyOut.String())
	}
	if !strings.Contains(verifyOut.String(), "Status:        VALID") {
		t.Fatalf("unexpected verification output:\n%s", verifyOut.String())
	}

	var reuseOut, reuseErr bytes.Buffer
	code = Run([]string{
		"build",
		"--target", "host",
		"--out", outDir,
		"--dry-run",
		"--reuse",
		fixture,
	}, &reuseOut, &reuseErr)
	if code != 0 {
		t.Fatalf("reuse exit %d: %s\n%s", code, reuseErr.String(), reuseOut.String())
	}
	if !strings.Contains(reuseOut.String(), "Reused:              true") {
		t.Fatalf("artifact set was not reused:\n%s", reuseOut.String())
	}

	var compareOut, compareErr bytes.Buffer
	code = Run([]string{"compare", artifactSet, artifactSet}, &compareOut, &compareErr)
	if code != 0 {
		t.Fatalf("compare exit %d: %s\n%s", code, compareErr.String(), compareOut.String())
	}
	if !strings.Contains(compareOut.String(), "Equivalent:           true") {
		t.Fatalf("unexpected comparison output:\n%s", compareOut.String())
	}

	matrixOutDir := filepath.Join(outDir, "matrix-run")
	var matrixOut, matrixErr bytes.Buffer
	code = Run([]string{
		"matrix",
		"--plan-only",
		"--targets", "host,linux-arm64",
		"--jobs", "2",
		"--out", matrixOutDir,
		fixture,
	}, &matrixOut, &matrixErr)
	if code != 0 {
		t.Fatalf("matrix exit %d: %s\n%s", code, matrixErr.String(), matrixOut.String())
	}
	if !strings.Contains(matrixOut.String(), "Summary: planned=2") {
		t.Fatalf("unexpected matrix output:\n%s", matrixOut.String())
	}
	if _, err := os.Stat(filepath.Join(matrixOutDir, "matrix.json")); err != nil {
		t.Fatalf("matrix report missing: %v", err)
	}
}

func TestAnalyzeAndPlanExcludeTheirCustomOutputFiles(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "CMakeLists.txt"), []byte("cmake_minimum_required(VERSION 3.16)\nproject(cli_output C)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	generatedDir := filepath.Join(project, "generated-reports")
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		t.Fatal(err)
	}

	analysisPath := filepath.Join(generatedDir, "analysis.c")
	if err := os.WriteFile(analysisPath, []byte("#include <cuda_runtime.h>\nvoid f(void) { cudaMalloc(0, 1); }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var analyzeOut, analyzeErr bytes.Buffer
	code := Run([]string{"analyze", "--output", analysisPath, project}, &analyzeOut, &analyzeErr)
	if code != 0 {
		t.Fatalf("analyze exit %d: %s\n%s", code, analyzeErr.String(), analyzeOut.String())
	}
	var first model.AnalysisReport
	data, err := os.ReadFile(analysisPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &first); err != nil {
		t.Fatal(err)
	}
	for _, requirement := range first.Requirements {
		if requirement.ID == "compute.cuda" {
			t.Fatalf("analyze output file contaminated its own analysis: %+v", requirement)
		}
	}

	analyzeOut.Reset()
	analyzeErr.Reset()
	code = Run([]string{"analyze", "--output", analysisPath, project}, &analyzeOut, &analyzeErr)
	if code != 0 {
		t.Fatalf("second analyze exit %d: %s\n%s", code, analyzeErr.String(), analyzeOut.String())
	}
	var second model.AnalysisReport
	data, err = os.ReadFile(analysisPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &second); err != nil {
		t.Fatal(err)
	}
	if first.ProjectDigest != second.ProjectDigest || first.ProjectEntries != second.ProjectEntries {
		t.Fatalf("analyze output changed repeated source identity: first=%+v second=%+v", first, second)
	}

	planPath := filepath.Join(generatedDir, "plan.c")
	if err := os.WriteFile(planPath, []byte("#include <cuda_runtime.h>\nvoid g(void) { cudaMalloc(0, 1); }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var planOut, planErr bytes.Buffer
	code = Run([]string{"plan", "--target", "host", "--output", planPath, project}, &planOut, &planErr)
	if code != 0 {
		t.Fatalf("plan exit %d: %s\n%s", code, planErr.String(), planOut.String())
	}
	var plan model.PortingPlan
	data, err = os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	for _, item := range plan.Items {
		if item.Requirement == "compute.cuda" {
			t.Fatalf("plan output file contaminated its own plan: %+v", item)
		}
	}
}

func TestLoadCodexInstructionsCombinesFileThenInline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "port.md")
	if err := os.WriteFile(path, []byte("Preserve original shaders.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadCodexInstructions("Prioritize controller input.", path)
	if err != nil {
		t.Fatal(err)
	}
	want := "Preserve original shaders.\n\nPrioritize controller input."
	if got != want {
		t.Fatalf("instructions = %q, want %q", got, want)
	}
}

func TestLoadCodexInstructionsRejectsInvalidUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.bin")
	if err := os.WriteFile(path, []byte{0xff, 0xfe, 0xfd}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCodexInstructions("", path); err == nil {
		t.Fatal("expected invalid UTF-8 instructions file to be rejected")
	}
}
