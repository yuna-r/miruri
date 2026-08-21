package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yuna-r/miruri/internal/diagnostics"
	"github.com/yuna-r/miruri/internal/fsutil"
	"github.com/yuna-r/miruri/internal/model"
)

const defaultRepairTimeout = 20 * time.Minute

type AuthMode string

type TaskMode string

const (
	TaskRepair TaskMode = "repair"
	TaskPort   TaskMode = "port"
	TaskAuto   TaskMode = "auto"

	// AuthChatGPT strips API-key environment variables so Codex reuses the
	// locally stored ChatGPT sign-in instead of accidentally creating API usage.
	AuthChatGPT AuthMode = "chatgpt"
	// AuthInherit leaves the caller's Codex/API authentication environment alone.
	AuthInherit AuthMode = "inherit"
)

type Status struct {
	Binary          string   `json:"binary"`
	Version         string   `json:"version,omitempty"`
	Authenticated   bool     `json:"authenticated"`
	AuthMode        string   `json:"auth_mode,omitempty"`
	AuthOutput      string   `json:"auth_output,omitempty"`
	Compatible      bool     `json:"compatible"`
	MissingFeatures []string `json:"missing_features,omitempty"`
}

type RepairRequest struct {
	Session               *AppServerSession
	Binary                string
	Mode                  TaskMode
	Workspace             string
	Target                model.TargetProfile
	BuildSystem           model.BuildSystem
	BuildLog              string
	Attempt               int
	OutputDir             string
	Timeout               time.Duration
	Model                 string
	Profile               string
	AuthMode              AuthMode
	MiruriVersion         string
	PreservationBaseline  string
	ContinuationDirective string
	CustomInstructions    string
	Progress              func(ProgressEvent)
}

type ProgressEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type RepairResponse struct {
	Status         string   `json:"status"`
	Summary        string   `json:"summary"`
	ChangedFiles   []string `json:"changed_files"`
	Assumptions    []string `json:"assumptions"`
	RemainingRisks []string `json:"remaining_risks"`
}

type EventSummary struct {
	Count                 int            `json:"count"`
	Types                 map[string]int `json:"types,omitempty"`
	ThreadID              string         `json:"thread_id,omitempty"`
	TurnID                string         `json:"turn_id,omitempty"`
	InputTokens           int64          `json:"input_tokens,omitempty"`
	CachedInputTokens     int64          `json:"cached_input_tokens,omitempty"`
	OutputTokens          int64          `json:"output_tokens,omitempty"`
	ReasoningOutputTokens int64          `json:"reasoning_output_tokens,omitempty"`
	Commands              []string       `json:"commands,omitempty"`
	ParseErrors           int            `json:"parse_errors,omitempty"`
}

type RepairResult struct {
	Mode                TaskMode                `json:"mode"`
	Command             []string                `json:"command"`
	Duration            time.Duration           `json:"-"`
	DurationMillis      int64                   `json:"duration_ms"`
	PromptPath          string                  `json:"prompt_path"`
	EventsPath          string                  `json:"events_path"`
	StderrPath          string                  `json:"stderr_path"`
	FinalResponsePath   string                  `json:"final_response_path"`
	SchemaPath          string                  `json:"schema_path"`
	PatchPath           string                  `json:"patch_path,omitempty"`
	DiagnosticsPath     string                  `json:"diagnostics_path,omitempty"`
	DiagnosticsJSONPath string                  `json:"diagnostics_json_path,omitempty"`
	ChangedFiles        []string                `json:"changed_files,omitempty"`
	DiscardedChanges    []model.DiscardedChange `json:"discarded_changes,omitempty"`
	Events              EventSummary            `json:"events"`
	Response            RepairResponse          `json:"response"`
	Error               string                  `json:"error,omitempty"`
	Stderr              string                  `json:"-"`
}

func Available(binary string) bool {
	_, err := resolveBinary(binary)
	return err == nil
}

func Check(ctx context.Context, binary string, authMode AuthMode) (Status, error) {
	path, err := resolveBinary(binary)
	if err != nil {
		return Status{}, err
	}
	status := Status{Binary: path}
	version, err := runSmall(ctx, path, authMode, "--version")
	if err != nil {
		return status, fmt.Errorf("read Codex version: %w", err)
	}
	status.Version = strings.TrimSpace(version)
	status.MissingFeatures = checkRequiredCLIOptions(ctx, path, authMode)
	status.Compatible = len(status.MissingFeatures) == 0
	if !status.Compatible {
		return status, fmt.Errorf("Codex CLI is missing required automation options: %s; update Codex CLI before using Miruri repair", strings.Join(status.MissingFeatures, ", "))
	}
	authOutput, err := runSmall(ctx, path, authMode, "login", "status")
	status.AuthOutput = strings.TrimSpace(authOutput)
	if err != nil {
		return status, fmt.Errorf("Codex CLI is installed but not authenticated: %w: %s", err, status.AuthOutput)
	}
	status.Authenticated = true
	lower := strings.ToLower(status.AuthOutput)
	switch {
	case strings.Contains(lower, "chatgpt"):
		status.AuthMode = "chatgpt"
	case strings.Contains(lower, "api key") || strings.Contains(lower, "api-key") || strings.Contains(lower, "apikey"):
		status.AuthMode = "api-key"
	default:
		status.AuthMode = "authenticated"
	}
	if authMode == AuthChatGPT && status.AuthMode == "api-key" {
		return status, fmt.Errorf("Codex is authenticated with an API key; Miruri defaults to ChatGPT-managed auth to avoid API billing (run `codex logout` then `codex login`, or pass --codex-auth inherit intentionally)")
	}
	return status, nil
}

func Repair(parent context.Context, request RepairRequest) (RepairResult, error) {
	binary, err := resolveBinary(request.Binary)
	if err != nil {
		return RepairResult{}, err
	}
	if request.Mode == "" {
		request.Mode = TaskRepair
	}
	switch request.Mode {
	case TaskRepair, TaskPort, TaskAuto:
	default:
		return RepairResult{}, fmt.Errorf("invalid Codex task mode %q", request.Mode)
	}
	if request.Attempt <= 0 {
		return RepairResult{}, fmt.Errorf("repair attempt must be positive")
	}
	if request.OutputDir == "" {
		return RepairResult{}, fmt.Errorf("Codex output directory is required")
	}
	workspace, err := filepath.Abs(request.Workspace)
	if err != nil {
		return RepairResult{}, err
	}
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		return RepairResult{}, fmt.Errorf("Codex workspace is not a directory: %s", workspace)
	}
	if err := validateGitWorkspace(parent, workspace); err != nil {
		return RepairResult{}, err
	}
	outputDir, err := filepath.Abs(request.OutputDir)
	if err != nil {
		return RepairResult{}, err
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return RepairResult{}, err
	}

	promptPath := filepath.Join(outputDir, "prompt.md")
	eventsPath := filepath.Join(outputDir, "events.jsonl")
	stderrPath := filepath.Join(outputDir, "stderr.log")
	finalPath := filepath.Join(outputDir, "final.json")
	schemaPath := filepath.Join(outputDir, "response-schema.json")
	diagnosticsPath := filepath.Join(outputDir, "diagnostics.txt")
	diagnosticsJSONPath := filepath.Join(outputDir, "diagnostics.json")
	diagnosticReport := diagnostics.Summarize(request.BuildLog, diagnostics.DefaultMaxBytes)
	if err := os.WriteFile(diagnosticsPath, []byte(diagnosticReport.Text), 0o600); err != nil {
		return RepairResult{}, err
	}
	if err := fsutil.WriteJSON(diagnosticsJSONPath, diagnosticReport); err != nil {
		return RepairResult{}, err
	}
	prompt := buildPrompt(request, diagnosticReport.Text)
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return RepairResult{}, err
	}
	if err := os.WriteFile(schemaPath, []byte(repairResponseSchema), 0o600); err != nil {
		return RepairResult{}, err
	}

	if request.Session != nil {
		return repairViaAppServer(parent, request, diagnosticReport.Text, promptPath, eventsPath, stderrPath, finalPath, schemaPath, diagnosticsPath, diagnosticsJSONPath)
	}

	// --ask-for-approval is a top-level Codex CLI option. It must appear
	// before the `exec` subcommand on current Codex CLI releases.
	args := []string{
		"--ask-for-approval", "never",
		"exec",
		"--json",
		"--ephemeral",
		"--ignore-rules",
		"--color", "never",
		"--sandbox", "workspace-write",
		"--config", `web_search="disabled"`,
		"--config", "sandbox_workspace_write.network_access=false",
		"--config", "allow_login_shell=false",
		"--config", "check_for_update_on_startup=false",
		"--config", "feedback.enabled=false",
		"--config", `history.persistence="none"`,
		"--config", "project_doc_max_bytes=0",
		"--config", "project_doc_fallback_filenames=[]",
		"--config", fmt.Sprintf(`projects.%s.trust_level="untrusted"`, strconv.Quote(workspace)),
		"--config", `shell_environment_policy.inherit="core"`,
		"--config", "shell_environment_policy.ignore_default_excludes=false",
		"--config", "features.apps=false",
		"--config", "features.hooks=false",
		"--config", "features.multi_agent=false",
		"--config", "features.skill_mcp_dependency_install=false",
		"--config", "agents.enabled=false",
		"--output-schema", schemaPath,
		"--output-last-message", finalPath,
	}
	// Keep automation deterministic and prevent user-level MCP servers, hooks,
	// providers, or permission settings from changing the repair environment.
	// An explicit profile is an opt-in to user configuration and therefore
	// suppresses this flag.
	if request.Profile == "" {
		args = append(args, "--ignore-user-config")
	}
	if request.AuthMode == "" || request.AuthMode == AuthChatGPT {
		args = append(args, "--config", `forced_login_method="chatgpt"`)
	}
	if request.Model != "" {
		args = append(args, "--model", request.Model)
	}
	if request.Profile != "" {
		args = append(args, "--profile", request.Profile)
	}
	args = append(args, "-")

	if request.Timeout <= 0 {
		request.Timeout = defaultRepairTimeout
	}
	ctx, cancel := context.WithTimeout(parent, request.Timeout)
	defer cancel()

	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = workspace
	taskName := "portability-repair"
	if request.Mode == TaskPort {
		taskName = "platform-port"
	} else if request.Mode == TaskAuto {
		taskName = "portability-auto"
	}
	command.Env = codexEnvironment(request.AuthMode, map[string]string{
		"MIRURI_CODEX_TASK":    taskName,
		"MIRURI_ARTIFACT_ONLY": "1",
		"MIRURI_TARGET":        request.Target.ID,
		"MIRURI_TARGET_TRIPLE": request.Target.Triple,
	})
	command.Stdin = strings.NewReader(prompt)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return RepairResult{}, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return RepairResult{}, err
	}

	eventsFile, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return RepairResult{}, err
	}
	defer eventsFile.Close()
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return RepairResult{}, err
	}
	defer stderrFile.Close()

	result := RepairResult{
		Mode:                request.Mode,
		Command:             append([]string{binary}, args...),
		PromptPath:          promptPath,
		EventsPath:          eventsPath,
		StderrPath:          stderrPath,
		FinalResponsePath:   finalPath,
		SchemaPath:          schemaPath,
		DiagnosticsPath:     diagnosticsPath,
		DiagnosticsJSONPath: diagnosticsJSONPath,
		Events:              EventSummary{Types: map[string]int{}},
	}
	start := time.Now()
	if err := command.Start(); err != nil {
		return result, fmt.Errorf("start Codex repair: %w", err)
	}

	var stderrBuffer bytes.Buffer
	var stderrErr error
	var stderrWG sync.WaitGroup
	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		_, stderrErr = io.Copy(io.MultiWriter(stderrFile, &stderrBuffer), stderr)
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if _, err := eventsFile.Write(append(line, '\n')); err != nil {
			_ = command.Process.Kill()
			// StdoutPipe and StderrPipe are closed by Cmd.Wait. Drain stderr first
			// so a fast process exit cannot turn valid output into os.ErrClosed.
			stderrWG.Wait()
			_ = command.Wait()
			return result, fmt.Errorf("write Codex event log: %w", err)
		}
		consumeEvent(line, &result.Events)
		if request.Progress != nil {
			if event, ok := summarizeProgressEvent(line); ok {
				request.Progress(event)
			}
		}
	}
	scanErr := scanner.Err()
	// Cmd.Wait closes pipes after observing process exit. The stderr reader must
	// reach EOF before Wait, otherwise a fast-exiting Codex process can race with
	// pipe closure and surface a spurious "file already closed" read error.
	stderrWG.Wait()
	waitErr := command.Wait()
	result.Duration = time.Since(start)
	result.DurationMillis = result.Duration.Milliseconds()
	result.Stderr = stderrBuffer.String()
	if stderrErr != nil {
		return result, fmt.Errorf("read Codex stderr: %w", stderrErr)
	}
	if scanErr != nil {
		return result, fmt.Errorf("read Codex JSONL events: %w", scanErr)
	}
	if waitErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Error = fmt.Sprintf("Codex repair timed out after %s", request.Timeout)
			return result, errors.New(result.Error)
		}
		result.Error = fmt.Sprintf("Codex repair failed: %v: %s", waitErr, strings.TrimSpace(result.Stderr))
		return result, errors.New(result.Error)
	}

	data, err := os.ReadFile(finalPath)
	if err != nil {
		result.Error = fmt.Sprintf("Codex completed without a structured final response: %v", err)
		return result, errors.New(result.Error)
	}
	if err := json.Unmarshal(data, &result.Response); err != nil {
		result.Error = fmt.Sprintf("decode Codex structured response: %v", err)
		return result, errors.New(result.Error)
	}
	if strings.TrimSpace(result.Response.Status) == "" || strings.TrimSpace(result.Response.Summary) == "" {
		result.Error = "Codex structured response is missing status or summary"
		return result, errors.New(result.Error)
	}
	switch result.Response.Status {
	case "repaired", "ported", "progress", "blocked", "no-change":
	default:
		result.Error = fmt.Sprintf("Codex structured response has invalid status %q", result.Response.Status)
		return result, errors.New(result.Error)
	}
	result.Response.ChangedFiles = normalizePaths(result.Response.ChangedFiles)
	return result, nil
}

func checkRequiredCLIOptions(ctx context.Context, binary string, authMode AuthMode) []string {
	topHelp, topErr := runSmall(ctx, binary, authMode, "--help")
	execHelp, execErr := runSmall(ctx, binary, authMode, "exec", "--help")
	var missing []string
	if topErr != nil {
		missing = append(missing, "top-level --help")
	} else if !strings.Contains(topHelp, "--ask-for-approval") {
		missing = append(missing, "--ask-for-approval")
	}
	if execErr != nil {
		missing = append(missing, "exec --help")
		return uniqueStrings(missing)
	}
	for _, option := range []string{
		"--json",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--sandbox",
		"--output-schema",
		"--output-last-message",
	} {
		if !strings.Contains(execHelp, option) {
			missing = append(missing, option)
		}
	}
	return uniqueStrings(missing)
}

func buildPrompt(request RepairRequest, diagnosticSummary string) string {
	version := request.MiruriVersion
	if version == "" {
		version = "dev"
	}
	mode := request.Mode
	if mode == "" {
		mode = TaskRepair
	}

	mission := `Goal:
Repair this isolated copied source workspace so the existing build system can produce a linked target artifact. Do not execute target artifacts.

Scope policy:
- Make the smallest coherent source/build-script repair that addresses the supplied diagnostics.
- Do not introduce a new application/platform backend unless it is genuinely required by the failing portability island.
- If a feature-complete port requires a broad new platform backend, return status "blocked" and explain the backend that would be required.`
	if mode == TaskPort {
		mission = `Goal:
Perform a feature-preserving platform port of this isolated copied source workspace and make its existing or revised build system produce a linked target artifact. Do not execute target artifacts.

Full-port authorization and fidelity contract:
- You are explicitly authorized to create a new target platform backend and to make coherent multi-file architectural changes when required.
- You may add target-specific source directories, platform abstraction interfaces, adapters, build-system branches, resources, generated text metadata, and target-native entry points.
- A Miruri port is a migration of the existing product, NOT a clone, remake, visual approximation, clean-room rewrite, or new implementation that merely resembles it.
- Treat the original product/domain/game logic, data structures, algorithms, content, assets, file formats, shaders, level data, physics rules, and observable behavior as authoritative. Reuse them directly or port them in place behind platform abstractions/conditional compilation.
- New target-platform code should be adaptation glue for OS services and graphics/audio/input APIs. Do not move domain/game/application behavior into a new parallel backend when the existing implementation can be retained or ported.
- Do not replace original scenes, models, textures, shaders, levels, UI content, physics, AI, gameplay, business rules, or media with procedural, simplified, placeholder, substitute, or look-alike implementations just to obtain a linked artifact.
- Packaging an original asset without actually consuming it in the target implementation does not count as preserving that asset pipeline.
- Preserve the original source-platform backend instead of replacing or deleting it whenever practical.
- Port GUI, editor, rendering, audio, input, networking, persistence, printing, shell integration and other platform services to semantically appropriate target-native facilities while keeping the original higher-level implementation intact.
- A requirement for a new backend is NOT by itself a reason to return "blocked". Creating an adapter/backend is the task, but creating a different application is forbidden.
- Prefer existing dependencies already present in the workspace/sysroot/toolchain. You may wire in a dependency already available locally, but do not fetch network resources.
- This command is intentionally multi-attempt. A large backend, many Windows-only APIs, substantial refactoring, or inability to finish the whole port in one turn is NOT a reason to stop. Make the largest coherent fidelity-preserving implementation slice you can in this attempt.
- If meaningful local work was completed but the full port is not finished, return status "progress", keep the remaining fidelity gaps explicit, and let Miruri rebuild and continue in the next attempt.
- If an original feature or content pipeline cannot yet be preserved, continue implementing prerequisites/adapters that are possible from the local workspace rather than substituting the feature.
- Return status "ported" or "repaired" only when no known product feature is omitted, stubbed, substituted, simplified, or approximated. Such losses must not be hidden in assumptions/remaining_risks while reporting success.
- Return "blocked" only when a concrete prerequisite outside the local workspace/toolchain prevents ANY further meaningful implementation work, such as unavailable proprietary source, an unavailable required external SDK with no target-native equivalent, or an unspecified proprietary contract. Name the exact prerequisite and evidence. Never return "blocked" merely because the port is broad, difficult, native-backend-heavy, or too large for one turn.
- Do not spend an attempt only explaining what would need to be written. Unless truly externally blocked, edit source/build files and advance the port.`
	} else if mode == TaskAuto {
		mission = `Goal:
Produce a linked target artifact from this isolated copied source workspace without executing target artifacts.

Automatic escalation policy:
- Start with the smallest coherent portability repair.
- If the failure reveals that the source is tied to another OS/CPU architecture or lacks a target platform backend, automatically escalate within this same task to a full feature-preserving platform port. Do not wait for additional authorization.
- You are explicitly authorized to create target-specific source directories, platform abstraction interfaces, adapters, build-system branches, resources, generated text metadata, and target-native entry points when needed.
- A Miruri port is a migration of the existing product, NOT a clone, remake, visual approximation, clean-room rewrite, or new implementation that merely resembles it.
- Preserve/reuse the original product/domain/game logic, algorithms, data, content, assets, shaders, level data, physics rules and file-format semantics. New target code should adapt platform APIs rather than replace higher-level behavior.
- Do not replace original scenes, models, textures, shaders, levels, UI content, physics, AI, gameplay, business rules, or media with procedural, simplified, placeholder, substitute, or look-alike implementations just to obtain a linked artifact.
- Packaging an original asset without actually consuming it in the target implementation does not count as preserving that asset pipeline.
- Preserve the original source-platform backend instead of replacing or deleting it whenever practical.
- Port GUI, editor, rendering, audio, input, networking, persistence, printing, shell integration and other platform services to semantically appropriate target-native facilities while keeping the original higher-level implementation intact.
- A requirement for a new backend is NOT by itself a reason to return "blocked". Creating an adapter/backend is explicitly authorized; creating a different application is forbidden.
- Prefer existing dependencies already present in the workspace/sysroot/toolchain. You may wire in a dependency already available locally, but do not fetch network resources.
- This command is intentionally multi-attempt. A broad native backend, many platform APIs, substantial refactoring, or inability to finish in one turn is NOT a reason to stop. Make the largest coherent fidelity-preserving implementation slice you can now.
- If meaningful local work was completed but the full port is not finished, return status "progress", keep remaining fidelity gaps explicit, and let Miruri rebuild and continue.
- If an original feature or content pipeline cannot yet be preserved, continue implementing locally available prerequisites/adapters rather than substituting the feature.
- Return status "ported" or "repaired" only when no known product feature is omitted, stubbed, substituted, simplified, or approximated.
- Return "blocked" only when a concrete prerequisite outside the local workspace/toolchain prevents ANY further meaningful implementation work. Never return it merely because the port is broad, difficult, native-backend-heavy, or too large for one turn.
- Do not spend an attempt only explaining what would need to be written. Unless truly externally blocked, edit source/build files and advance the port.`
	}

	return fmt.Sprintf(`You are Miruri %s's Codex portability agent.

Task mode: %s

Target contract:
- ID: %s
- OS: %s
- architecture: %s
- compiler triple: %s
- object format: %s

Build system: %s
Attempt: %d

Original-project preservation baseline:
%s

Miruri continuation directive from earlier attempts:
%s

Target-specific port guidance:
%s

Operator-supplied custom instructions:
%s

%s

Mandatory constraints:
- Work only inside the current Git workspace. It is a disposable copy, not the user's original repository.
- Preserve public APIs, file formats, protocols, persistent data semantics, observable behavior and license notices unless a target OS requires a documented native equivalent.
- A target backend may adapt platform services, but it must not become a replacement implementation of the product/domain/game itself.
- Before adding new domain logic, search for and reuse/port the corresponding original implementation. Prefer adapters around original code over parallel rewrites.
- Judge fidelity primarily by preserved product semantics and observable behavior, not by whether the target source tree has the same class/file topology. When source-platform orchestration is inseparably coupled to platform APIs, a target-native controller/orchestration layer may faithfully re-express that control flow. Do not keep refactoring solely to eliminate code duplication once the original state transitions, calculations, content semantics, and observable behavior are preserved.
- Structural/source-reuse debt by itself is not a completion blocker. Do not place duplication, an original UI/controller class not being directly reused, or a native restructuring into remaining_risks unless it causes a concrete shipped behavior/content semantic to be missing, simplified, substituted, or observably different.
- Preserve optimized architecture-specific paths behind correct compile-time feature guards.
- Prefer portable ISO C/C++ fallbacks before adding target-specific intrinsics when performance is not the defining behavior.
- Do not silently disable GUI, rendering, shaders, audio, input, networking, plugins, assets or any other product feature.
- If an error occurs, do not only fix the line where the error occurred; also check related lines and related parts of other files to see whether errors could occur there as well, and fix them if necessary.
- Do not replace third-party code unless the replacement is license-compatible and the decision is documented in the structured response.
- Do not use emulators or compatibility runners, including QEMU, Wine or Rosetta.
- Do not run target executables, target test binaries, configure probes or generated target tools.
- Host-side analysis and host-native code generators may run only when clearly separate from target artifacts.
- Do not fetch network resources. Use only files and tools already present.
- You may compile or link repeatedly to validate progress, but do not intentionally retain object files, executables, libraries, caches or other generated build products as source changes.
- Do not create MIRURI_REPAIR_NOTES.md or other Miruri-specific files in the target project. Put assumptions and risks only in the structured response.
- Add or update tests only when they can be compiled without executing target artifacts.

Before finishing:
1. Review the actual source/build-script diff.
2. Verify the target build still compiles/reuses original translation units whenever the original project contains implementation source; a new target entry point plus unused packaged assets is not sufficient.
3. Verify shipped assets/content remain semantically consumed by the target implementation, not merely copied into the package.
4. Ensure no feature was deleted, approximated, substituted, simplified, or moved into an unrelated replacement implementation merely to make compilation pass. Target-native restructuring is acceptable when it preserves the original behavior/content semantics.
5. Ensure source-platform code remains usable where practical. Do not require identical class/file topology or direct reuse of platform-coupled UI/controller orchestration as a condition of completion.
6. Return the required structured JSON summary. The remaining_risks array is reserved for known, project-relevant unresolved fidelity blockers that are actually exercised by this repository's shipped source/assets/configuration. Do not put hypothetical unsupported input formats or edge cases that are not used by this project, optional hardware that is absent on some machines, performance/optimization opportunities, Miruri's intentional lack of runtime execution, or build/relink verification that Miruri's subsequent rebuild can supersede into remaining_risks; mention those in the summary only if useful. If at least one locally implementable project-relevant fidelity blocker remains after making concrete progress, return "progress". If the target is linked and only advisory caveats remain, return "ported"/"repaired" even though Miruri has not executed the artifact. Reserve "blocked" for a concrete external prerequisite that prevents any further meaningful local implementation work. The changed_files field is advisory; Miruri independently computes the authoritative source patch and discards generated build products.

Miruri-selected build diagnostics:

%s
`, version, mode, request.Target.ID, request.Target.OS, request.Target.Arch, request.Target.Triple, request.Target.ObjectFormat, request.BuildSystem, request.Attempt, baselineOrDefault(request.PreservationBaseline), continuationOrDefault(request.ContinuationDirective), platformPortGuidance(request.Target.OS), customInstructionsOrDefault(request.CustomInstructions), mission, strings.TrimSpace(diagnosticSummary))
}

func customInstructionsOrDefault(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(none)"
	}
	return strings.TrimSpace(`Apply these operator instructions in addition to Miruri's target contract, fidelity contract, and mandatory constraints. When an operator instruction conflicts with those contracts, preserve Miruri's contracts and follow the non-conflicting portion of the operator instruction.`) + "\n\n" + value
}

func platformPortGuidance(targetOS string) string {
	switch strings.ToLower(strings.TrimSpace(targetOS)) {
	case "macos", "darwin":
		return strings.TrimSpace(`- Windows-only C++/CX/UWP/WinRT coupling, Direct3D/D2D/DirectWrite, XAudio2/Media Foundation, and Windows input/lifecycle APIs are NOT terminal blockers merely because they require a substantial macOS backend.
- Preserve and compile the original C/C++ product/game/domain implementation wherever possible. Isolate Windows platform services behind interfaces/conditional compilation and implement target-native adapters using frameworks available in the macOS SDK, such as AppKit, Metal/MetalKit, CoreAudio/AVFoundation, GameController, CoreText, and Foundation where semantically appropriate.
- Objective-C++ (.mm) is allowed for thin macOS interop/adapters, but do NOT move the product/game/domain implementation into a new monolithic Objective-C++ replacement. Keep reusable logic in the original C/C++ translation units or port it in place.
- Treat HLSL, DDS, SDKMesh, media, level, and other shipped content as migration inputs to preserve, not permission to invent procedural substitutes. Implement/port readers, converters, or shader equivalents from local source/data as needed.`)
	case "linux":
		return strings.TrimSpace(`- Windows-only Win32/UWP/WinRT, DirectX, XAudio2/Media Foundation, and Windows input/lifecycle APIs are NOT terminal blockers merely because Linux adapters are substantial.
- Preserve and compile the original C/C++ product/game/domain implementation wherever possible. Isolate platform services and implement semantically equivalent Linux/backend code using dependencies and system facilities already available in the workspace/toolchain/sysroot.
- Do not replace shipped assets, shaders, levels, physics, or media with procedural/look-alike substitutes merely to get a linked ELF artifact.`)
	default:
		return "- A broad source-platform API dependency is not terminal by itself. Preserve higher-level implementation and migrate platform services through target-native adapters available in the local workspace/toolchain."
	}
}

func continuationOrDefault(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "  (none; this is the first implementation attempt)"
	}
	return value
}

func baselineOrDefault(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "  (baseline inventory unavailable)"
	}
	return value
}

func validateGitWorkspace(ctx context.Context, workspace string) error {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("git is required for Codex repair isolation: %w", err)
	}
	command := exec.CommandContext(ctx, gitPath, "rev-parse", "--is-inside-work-tree")
	command.Dir = workspace
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "true" {
		return fmt.Errorf("Codex repair workspace must be an isolated Git repository prepared by Miruri")
	}
	return nil
}

func summarizeProgressEvent(line []byte) (ProgressEvent, bool) {
	var root map[string]any
	if err := json.Unmarshal(line, &root); err != nil {
		return ProgressEvent{}, false
	}
	eventType, _ := root["type"].(string)
	switch eventType {
	case "thread.started":
		id, _ := root["thread_id"].(string)
		if id == "" {
			return ProgressEvent{}, false
		}
		return ProgressEvent{Type: eventType, Message: "thread " + id + " started"}, true
	case "item.started", "item.completed":
		item, _ := root["item"].(map[string]any)
		itemType, _ := item["type"].(string)
		switch itemType {
		case "command_execution":
			command, _ := item["command"].(string)
			if command == "" {
				return ProgressEvent{}, false
			}
			verb := "running"
			if eventType == "item.completed" {
				verb = "completed"
			}
			return ProgressEvent{Type: itemType, Message: verb + ": " + truncateOneLine(command, 220)}, true
		case "file_change", "file_changes":
			return ProgressEvent{Type: itemType, Message: "workspace files updated"}, true
		}
	case "turn.completed":
		usage, _ := root["usage"].(map[string]any)
		input, _ := intValue(usage["input_tokens"])
		output, _ := intValue(usage["output_tokens"])
		return ProgressEvent{Type: eventType, Message: fmt.Sprintf("turn completed (input=%d, output=%d tokens)", input, output)}, true
	case "turn.failed", "error":
		message := "Codex reported an error"
		if value, ok := root["message"].(string); ok && value != "" {
			message = truncateOneLine(value, 220)
		}
		return ProgressEvent{Type: eventType, Message: message}, true
	}
	return ProgressEvent{}, false
}

func truncateOneLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func resolveBinary(binary string) (string, error) {
	if binary == "" {
		binary = "codex"
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("Codex CLI is not installed or not on PATH: %w", err)
	}
	return path, nil
}

func runSmall(ctx context.Context, binary string, authMode AuthMode, args ...string) (string, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = codexEnvironment(authMode, nil)
	output, err := command.CombinedOutput()
	return string(output), err
}

func codexEnvironment(mode AuthMode, additions map[string]string) []string {
	if mode == "" {
		mode = AuthChatGPT
	}
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || (mode == AuthChatGPT && isSensitiveEnvironmentKey(key)) {
			continue
		}
		values[key] = value
	}
	for key, value := range additions {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func isSensitiveEnvironmentKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	if upper == "" {
		return false
	}
	for _, fragment := range []string{
		"API_KEY", "APIKEY", "ACCESS_KEY", "ACCESS_TOKEN", "AUTH_TOKEN",
		"ID_TOKEN", "REFRESH_TOKEN", "SESSION_TOKEN", "BEARER",
		"SECRET", "PRIVATE_KEY", "PASSWORD", "PASSWD", "CREDENTIAL",
		"COOKIE", "AUTHORIZATION", "SSH_AUTH_SOCK",
	} {
		if strings.Contains(upper, fragment) {
			return true
		}
	}
	if strings.HasSuffix(upper, "_TOKEN") {
		return true
	}
	for _, exact := range []string{
		"OPENAI_API_KEY", "CODEX_API_KEY", "GH_TOKEN", "GITHUB_TOKEN",
		"NPM_TOKEN", "PYPI_TOKEN", "AWS_SESSION_TOKEN",
		"OPENAI_BASE_URL", "OPENAI_API_BASE", "CHATGPT_BASE_URL",
		"CODEX_API_BASE", "AZURE_OPENAI_ENDPOINT",
	} {
		if upper == exact {
			return true
		}
	}
	return false
}

func consumeEvent(line []byte, summary *EventSummary) {
	summary.Count++
	var value any
	if err := json.Unmarshal(line, &value); err != nil {
		summary.ParseErrors++
		return
	}
	root, _ := value.(map[string]any)
	if eventType, ok := stringValue(root["type"]); ok {
		summary.Types[eventType]++
	}
	walkEvent(value, "", summary)
	summary.Commands = uniqueStrings(summary.Commands)
}

func walkEvent(value any, key string, summary *EventSummary) {
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			lower := strings.ToLower(strings.ReplaceAll(childKey, "_", ""))
			if text, ok := stringValue(child); ok {
				switch lower {
				case "threadid", "sessionid":
					if summary.ThreadID == "" {
						summary.ThreadID = text
					}
				case "turnid":
					if summary.TurnID == "" {
						summary.TurnID = text
					}
				case "command", "cmd":
					if text != "" {
						summary.Commands = append(summary.Commands, text)
					}
				}
			}
			if number, ok := intValue(child); ok {
				switch lower {
				case "inputtokens":
					if number > summary.InputTokens {
						summary.InputTokens = number
					}
				case "cachedinputtokens", "cachedtokens":
					if number > summary.CachedInputTokens {
						summary.CachedInputTokens = number
					}
				case "outputtokens":
					if number > summary.OutputTokens {
						summary.OutputTokens = number
					}
				case "reasoningoutputtokens":
					if number > summary.ReasoningOutputTokens {
						summary.ReasoningOutputTokens = number
					}
				}
			}
			walkEvent(child, childKey, summary)
		}
	case []any:
		for _, child := range typed {
			walkEvent(child, key, summary)
		}
	}
}

func stringValue(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok
}

func intValue(value any) (int64, bool) {
	switch number := value.(type) {
	case float64:
		return int64(number), true
	case json.Number:
		parsed, err := number.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(number, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func normalizePaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
		if path == "" || path == "." || strings.HasPrefix(path, "../") || filepath.IsAbs(path) {
			continue
		}
		result = append(result, path)
	}
	return uniqueStrings(result)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func WriteResult(path string, result RepairResult) error {
	return fsutil.WriteJSON(path, result)
}

const repairResponseSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "status": {
      "type": "string",
      "enum": ["repaired", "ported", "progress", "blocked", "no-change"]
    },
    "summary": {"type": "string", "minLength": 1},
    "changed_files": {
      "type": "array",
      "items": {"type": "string"}
    },
    "assumptions": {
      "type": "array",
      "items": {"type": "string"}
    },
    "remaining_risks": {
      "type": "array",
      "items": {"type": "string"}
    }
  },
  "required": ["status", "summary", "changed_files", "assumptions", "remaining_risks"],
  "additionalProperties": false
}
`

// ArtifactOnlyViolations performs a conservative post-run audit of commands
// reported by Codex's JSONL event stream. The sandbox and prompt remain the
// primary controls; this rejects explicit compatibility runners and common
// test targets that would execute newly built target programs.
func ArtifactOnlyViolations(commands []string) []string {
	var violations []string
	for _, command := range commands {
		lower := " " + strings.ToLower(strings.Join(strings.Fields(command), " ")) + " "
		for _, marker := range []string{
			" qemu-", " qemu ", " wine ", " wine64 ", " wineserver ",
			" rosetta ", " box64 ", " box86 ", " fexinterpreter ", " fexemu ",
		} {
			if strings.Contains(lower, marker) {
				violations = append(violations, fmt.Sprintf("forbidden compatibility runner in command %q", command))
				break
			}
		}
		for _, marker := range []string{
			" ctest ", " make test ", " ninja test ", " meson test ",
			" cargo test ", " go test ", " pytest ", " python -m pytest ",
			" npm test ", " npm run test ", " yarn test ", " pnpm test ",
			" --target test ", " --target check ",
		} {
			if strings.Contains(lower, marker) {
				violations = append(violations, fmt.Sprintf("forbidden runtime test command %q", command))
				break
			}
		}
	}
	return uniqueStrings(violations)
}

func repairViaAppServer(parent context.Context, request RepairRequest, diagnosticSummary, promptPath, eventsPath, stderrPath, finalPath, schemaPath, diagnosticsPath, diagnosticsJSONPath string) (RepairResult, error) {
	prompt := buildPrompt(request, diagnosticSummary)
	if request.Attempt > 1 {
		prompt = buildAppServerContinuationPrompt(request, diagnosticSummary)
	}
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return RepairResult{}, err
	}
	var outputSchema map[string]any
	decoder := json.NewDecoder(strings.NewReader(repairResponseSchema))
	decoder.UseNumber()
	if err := decoder.Decode(&outputSchema); err != nil {
		return RepairResult{}, fmt.Errorf("decode embedded Codex response schema: %w", err)
	}
	if request.Timeout <= 0 {
		request.Timeout = defaultRepairTimeout
	}
	ctx, cancel := context.WithTimeout(parent, request.Timeout)
	defer cancel()

	result := RepairResult{
		Mode:                request.Mode,
		Command:             []string{request.Session.binary, "app-server", "turn/start", request.Session.ThreadID()},
		PromptPath:          promptPath,
		EventsPath:          eventsPath,
		StderrPath:          stderrPath,
		FinalResponsePath:   finalPath,
		SchemaPath:          schemaPath,
		DiagnosticsPath:     diagnosticsPath,
		DiagnosticsJSONPath: diagnosticsJSONPath,
		Events:              EventSummary{Types: map[string]int{}, ThreadID: request.Session.ThreadID()},
	}
	start := time.Now()
	finalText, lines, turnID, err := request.Session.RunTurn(ctx, prompt, outputSchema, request.Progress)
	result.Duration = time.Since(start)
	result.DurationMillis = result.Duration.Milliseconds()
	result.Events.TurnID = turnID
	result.Stderr = request.Session.Stderr()
	_ = os.WriteFile(stderrPath, []byte(result.Stderr), 0o600)

	eventsFile, fileErr := os.OpenFile(eventsPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if fileErr != nil {
		return result, fileErr
	}
	for _, line := range lines {
		if _, fileErr = eventsFile.Write(append(append([]byte(nil), line...), '\n')); fileErr != nil {
			break
		}
		consumeEvent(line, &result.Events)
	}
	closeErr := eventsFile.Close()
	if fileErr != nil {
		return result, fmt.Errorf("write Codex app-server event log: %w", fileErr)
	}
	if closeErr != nil {
		return result, fmt.Errorf("close Codex app-server event log: %w", closeErr)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Error = fmt.Sprintf("Codex app-server turn timed out after %s", request.Timeout)
		} else {
			result.Error = fmt.Sprintf("Codex app-server turn failed: %v", err)
		}
		return result, errors.New(result.Error)
	}
	if err := os.WriteFile(finalPath, []byte(finalText), 0o600); err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(finalText), &result.Response); err != nil {
		result.Error = fmt.Sprintf("decode Codex app-server structured response: %v", err)
		return result, errors.New(result.Error)
	}
	if strings.TrimSpace(result.Response.Status) == "" || strings.TrimSpace(result.Response.Summary) == "" {
		result.Error = "Codex app-server structured response is missing status or summary"
		return result, errors.New(result.Error)
	}
	switch result.Response.Status {
	case "repaired", "ported", "progress", "blocked", "no-change":
	default:
		result.Error = fmt.Sprintf("Codex app-server structured response has invalid status %q", result.Response.Status)
		return result, errors.New(result.Error)
	}
	result.Response.ChangedFiles = normalizePaths(result.Response.ChangedFiles)
	return result, nil
}

func buildAppServerContinuationPrompt(request RepairRequest, diagnosticSummary string) string {
	return fmt.Sprintf(`Continue the same Miruri %s %s session for target %s. All fidelity, safety, artifact-only, and platform-port constraints from the first turn remain in force; do not reinterpret or weaken them.

This is attempt %d. The workspace already contains every accepted change from earlier turns and Miruri has rebuilt it since the previous turn.

Miruri continuation directive:
%s

Operator-supplied custom instructions for this attempt:
%s

Newest Miruri build diagnostics:
%s

Work from the current workspace state. Do not re-map or re-explain the repository unless needed for the new diagnostics. Inspect related code when an error indicates a wider issue, make the largest coherent fidelity-preserving implementation slice that is useful now, and return the same required structured JSON response schema.`,
		request.MiruriVersion,
		request.Mode,
		request.Target.ID,
		request.Attempt,
		continuationOrDefault(request.ContinuationDirective),
		customInstructionsOrDefault(request.CustomInstructions),
		strings.TrimSpace(diagnosticSummary),
	)
}
