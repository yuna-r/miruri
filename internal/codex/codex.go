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

	"github.com/yuna-r/miruri/internal/fsutil"
	"github.com/yuna-r/miruri/internal/model"
)

const defaultRepairTimeout = 20 * time.Minute

type AuthMode string

const (
	// AuthChatGPT strips API-key environment variables so Codex reuses the
	// locally stored ChatGPT sign-in instead of accidentally creating API usage.
	AuthChatGPT AuthMode = "chatgpt"
	// AuthInherit leaves the caller's Codex/API authentication environment alone.
	AuthInherit AuthMode = "inherit"
)

type Status struct {
	Binary        string `json:"binary"`
	Version       string `json:"version,omitempty"`
	Authenticated bool   `json:"authenticated"`
	AuthMode      string `json:"auth_mode,omitempty"`
	AuthOutput    string `json:"auth_output,omitempty"`
}

type RepairRequest struct {
	Binary        string
	Workspace     string
	Target        model.TargetProfile
	BuildSystem   model.BuildSystem
	BuildLog      string
	Attempt       int
	OutputDir     string
	Timeout       time.Duration
	Model         string
	Profile       string
	AuthMode      AuthMode
	MiruriVersion string
	Progress      func(ProgressEvent)
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
	Command           []string       `json:"command"`
	Duration          time.Duration  `json:"-"`
	DurationMillis    int64          `json:"duration_ms"`
	PromptPath        string         `json:"prompt_path"`
	EventsPath        string         `json:"events_path"`
	StderrPath        string         `json:"stderr_path"`
	FinalResponsePath string         `json:"final_response_path"`
	SchemaPath        string         `json:"schema_path"`
	PatchPath         string         `json:"patch_path,omitempty"`
	ChangedFiles      []string       `json:"changed_files,omitempty"`
	Events            EventSummary   `json:"events"`
	Response          RepairResponse `json:"response"`
	Error             string         `json:"error,omitempty"`
	Stderr            string         `json:"-"`
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
	prompt := buildPrompt(request)
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return RepairResult{}, err
	}
	if err := os.WriteFile(schemaPath, []byte(repairResponseSchema), 0o600); err != nil {
		return RepairResult{}, err
	}

	args := []string{
		"exec",
		"--json",
		"--ephemeral",
		"--ignore-rules",
		"--color", "never",
		"--sandbox", "workspace-write",
		"--ask-for-approval", "never",
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
	command.Env = codexEnvironment(request.AuthMode, map[string]string{
		"MIRURI_CODEX_TASK":    "portability-repair",
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
		Command:           append([]string{binary}, args...),
		PromptPath:        promptPath,
		EventsPath:        eventsPath,
		StderrPath:        stderrPath,
		FinalResponsePath: finalPath,
		SchemaPath:        schemaPath,
		Events:            EventSummary{Types: map[string]int{}},
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
			_ = command.Wait()
			stderrWG.Wait()
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
	waitErr := command.Wait()
	stderrWG.Wait()
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
	case "repaired", "blocked", "no-change":
	default:
		result.Error = fmt.Sprintf("Codex structured response has invalid status %q", result.Response.Status)
		return result, errors.New(result.Error)
	}
	result.Response.ChangedFiles = normalizePaths(result.Response.ChangedFiles)
	return result, nil
}

func buildPrompt(request RepairRequest) string {
	log := request.BuildLog
	const maxLog = 48_000
	if len(log) > maxLog {
		log = log[len(log)-maxLog:]
	}
	version := request.MiruriVersion
	if version == "" {
		version = "dev"
	}
	return fmt.Sprintf(`You are Miruri %s's constrained portability repair agent.

Target contract:
- ID: %s
- OS: %s
- architecture: %s
- compiler triple: %s
- object format: %s

Build system: %s
Repair attempt: %d

Goal:
Repair this isolated copied source workspace so the existing build system can produce a linked target artifact. Do not execute target artifacts.

Mandatory constraints:
- Work only inside the current Git workspace. It is a disposable copy, not the user's original repository.
- Preserve public APIs, file formats, protocols, data layouts, observable behavior and license notices.
- Preserve optimized architecture-specific paths behind correct compile-time feature guards.
- Prefer portable ISO C/C++ fallbacks before adding target-specific intrinsics.
- Do not silently disable GUI, rendering, shaders, audio, input, networking, plugins, assets or any other product feature.
- Do not replace third-party code unless the replacement is license-compatible and the decision is documented.
- Do not use emulators or compatibility runners, including QEMU, Wine or Rosetta.
- Do not run target executables, target test binaries, configure probes or generated target tools.
- Host-side analysis and host-native code generators may run only when clearly separate from target artifacts.
- Do not fetch network resources. Use only files and tools already present.
- Make the smallest coherent repair that addresses the supplied diagnostics.
- Add or update tests only when they can be compiled without executing target artifacts.
- Record material assumptions in MIRURI_REPAIR_NOTES.md.

Before finishing:
1. Review the actual diff.
2. Ensure no feature was deleted merely to make compilation pass.
3. Return the required structured JSON summary. The changed_files field is advisory; Miruri independently computes the authoritative Git patch.

Build diagnostics:

%s
`, version, request.Target.ID, request.Target.OS, request.Target.Arch, request.Target.Triple, request.Target.ObjectFormat, request.BuildSystem, request.Attempt, strings.TrimSpace(log))
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
			lower := strings.ToLower(childKey)
			if text, ok := stringValue(child); ok {
				switch lower {
				case "thread_id", "session_id":
					if summary.ThreadID == "" {
						summary.ThreadID = text
					}
				case "turn_id":
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
				case "input_tokens":
					if number > summary.InputTokens {
						summary.InputTokens = number
					}
				case "cached_input_tokens", "cached_tokens":
					if number > summary.CachedInputTokens {
						summary.CachedInputTokens = number
					}
				case "output_tokens":
					if number > summary.OutputTokens {
						summary.OutputTokens = number
					}
				case "reasoning_output_tokens":
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
      "enum": ["repaired", "blocked", "no-change"]
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
