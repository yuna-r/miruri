package builder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/yuna-r/miruri/internal/analyze"
	"github.com/yuna-r/miruri/internal/codex"
	"github.com/yuna-r/miruri/internal/fsutil"
	"github.com/yuna-r/miruri/internal/inspect"
	"github.com/yuna-r/miruri/internal/model"
	"github.com/yuna-r/miruri/internal/planner"
	"github.com/yuna-r/miruri/internal/repairworkspace"
	"github.com/yuna-r/miruri/internal/target"
)

type Config struct {
	ProjectDir   string
	Target       model.TargetProfile
	Sysroot      string
	OutDir       string
	Generator    string
	UseCodex     bool
	MaxRepairs   int
	CodexBinary  string
	CodexModel   string
	CodexProfile string
	CodexAuth    codex.AuthMode
	CodexTimeout time.Duration
	KeepWork     bool
	DryRun       bool
	Version      string
	Timeout      time.Duration
	Progress     io.Writer
}

type Result struct {
	Manifest     model.BuildManifest
	ManifestPath string
	PackageDir   string
	WorkDir      string
}

type buildContext struct {
	config       Config
	analysis     model.AnalysisReport
	plan         model.PortingPlan
	buildSystem  model.BuildSystem
	projectAbs   string
	workDir      string
	sourceDir    string
	buildDir     string
	packageDir   string
	logPath      string
	logBuffer    bytes.Buffer
	codexRepairs []model.CodexRepairAttempt
	repairRepo   *repairworkspace.Repository
}

func Build(ctx context.Context, config Config) (Result, error) {
	if config.MaxRepairs < 0 {
		return Result{}, fmt.Errorf("max repairs must be non-negative")
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Minute
	}
	projectAbs, err := filepath.Abs(config.ProjectDir)
	if err != nil {
		return Result{}, err
	}
	analysisReport, err := analyze.Project(projectAbs, analyze.Options{})
	if err != nil {
		return Result{}, err
	}
	resolvedSysroot := resolveSysroot(config.Target, config.Sysroot)
	config.Sysroot = resolvedSysroot
	portingPlan := planner.Create(analysisReport, config.Target, resolvedSysroot)
	buildSystem, err := chooseBuildSystem(analysisReport.BuildSystems)
	if err != nil {
		return Result{}, err
	}

	outDir := config.OutDir
	if outDir == "" {
		outDir = filepath.Join(projectAbs, "dist")
	}
	outDir, err = filepath.Abs(outDir)
	if err != nil {
		return Result{}, err
	}
	packageDir := filepath.Join(outDir, config.Target.ID)
	if err := os.RemoveAll(packageDir); err != nil {
		return Result{}, fmt.Errorf("clean package directory: %w", err)
	}
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		return Result{}, err
	}

	cacheRoot, err := os.UserCacheDir()
	if err != nil || cacheRoot == "" {
		cacheRoot = os.TempDir()
	}
	projectHash := sha256.Sum256([]byte(projectAbs))
	workDir := filepath.Join(cacheRoot, "miruri", "work", hex.EncodeToString(projectHash[:8]), config.Target.ID)
	if err := os.RemoveAll(workDir); err != nil {
		return Result{}, fmt.Errorf("clean work directory: %w", err)
	}
	sourceDir := filepath.Join(workDir, "source")
	buildDir := filepath.Join(workDir, "build")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return Result{}, err
	}

	bc := &buildContext{
		config:      config,
		analysis:    analysisReport,
		plan:        portingPlan,
		buildSystem: buildSystem,
		projectAbs:  projectAbs,
		workDir:     workDir,
		sourceDir:   sourceDir,
		buildDir:    buildDir,
		packageDir:  packageDir,
		logPath:     filepath.Join(packageDir, "build.log"),
	}

	analysisPath := filepath.Join(packageDir, "analysis.json")
	planPath := filepath.Join(packageDir, "plan.json")
	if err := fsutil.WriteJSON(analysisPath, analysisReport); err != nil {
		return Result{}, err
	}
	if err := fsutil.WriteJSON(planPath, portingPlan); err != nil {
		return Result{}, err
	}

	if config.DryRun {
		manifest := baseManifest(bc, analysisPath, planPath)
		manifest.Assurance = model.AssuranceGenerated
		manifest.Warnings = append(manifest.Warnings, "dry run: no compiler or linker was invoked")
		manifestPath := filepath.Join(packageDir, "manifest.json")
		if err := fsutil.WriteJSON(manifestPath, manifest); err != nil {
			return Result{}, err
		}
		return Result{Manifest: manifest, ManifestPath: manifestPath, PackageDir: packageDir, WorkDir: workDir}, nil
	}

	if err := validateEnvironment(config.Target, resolvedSysroot); err != nil {
		return Result{}, err
	}
	if err := fsutil.CopyTree(projectAbs, sourceDir); err != nil {
		return Result{}, fmt.Errorf("create isolated source overlay: %w", err)
	}
	if config.UseCodex && config.MaxRepairs > 0 {
		if err := fsutil.ValidateSymlinksWithin(sourceDir); err != nil {
			return Result{}, fmt.Errorf("Codex repair workspace is unsafe: %w", err)
		}
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		status, checkErr := codex.Check(checkCtx, config.CodexBinary, config.CodexAuth)
		cancel()
		if checkErr != nil {
			return Result{}, checkErr
		}
		bc.logf("Codex CLI: %s; auth: %s\n", status.Version, status.AuthMode)
		repo, err := repairworkspace.Init(sourceDir)
		if err != nil {
			return Result{}, err
		}
		bc.repairRepo = repo
		bc.logf("Codex isolated Git workspace prepared\n")
	}

	var buildErr error
	for attempt := 0; attempt <= config.MaxRepairs; attempt++ {
		if attempt > 0 {
			bc.logf("\n=== rebuild after Codex repair attempt %d ===\n", attempt)
		}
		attemptLogStart := bc.logBuffer.Len()
		buildErr = bc.runBuild(ctx)
		if buildErr == nil {
			break
		}
		latestBuildLog := bc.logBuffer.String()[attemptLogStart:]
		if !config.UseCodex || attempt >= config.MaxRepairs {
			break
		}

		bc.logf("\n=== Codex repair attempt %d ===\n", attempt+1)
		checkpoint, checkpointErr := bc.repairRepo.CaptureAndCommit(fmt.Sprintf("Miruri failed-build checkpoint %d", attempt+1))
		if checkpointErr != nil {
			buildErr = fmt.Errorf("%w; checkpoint failed build state: %v", buildErr, checkpointErr)
			break
		}
		if len(checkpoint.Files) > 0 {
			bc.logf("Checkpointed %d build-generated workspace change(s) before Codex.\n", len(checkpoint.Files))
		}
		repairBase, checkpointErr := bc.repairRepo.Head()
		if checkpointErr != nil {
			buildErr = fmt.Errorf("%w; read pre-repair checkpoint: %v", buildErr, checkpointErr)
			break
		}
		attemptDir := filepath.Join(packageDir, "codex", fmt.Sprintf("attempt-%02d", attempt+1))
		result, repairErr := codex.Repair(ctx, codex.RepairRequest{
			Binary:        config.CodexBinary,
			Workspace:     sourceDir,
			OutputDir:     attemptDir,
			Target:        config.Target,
			BuildSystem:   buildSystem,
			BuildLog:      latestBuildLog,
			Attempt:       attempt + 1,
			Model:         config.CodexModel,
			Profile:       config.CodexProfile,
			AuthMode:      config.CodexAuth,
			Timeout:       config.CodexTimeout,
			MiruriVersion: config.Version,
			Progress: func(event codex.ProgressEvent) {
				bc.logf("[codex] %s\n", event.Message)
			},
		})
		patchPath := filepath.Join(attemptDir, "repair.patch")
		resultPath := filepath.Join(attemptDir, "result.json")
		var changedFiles []string
		if repairErr == nil {
			if result.Response.Status != "repaired" {
				repairErr = fmt.Errorf("Codex returned status %q: %s", result.Response.Status, result.Response.Summary)
			} else if violations := codex.ArtifactOnlyViolations(result.Events.Commands); len(violations) > 0 {
				repairErr = fmt.Errorf("Codex violated artifact-only policy: %s", strings.Join(violations, "; "))
			} else if symlinkErr := fsutil.ValidateSymlinksWithin(sourceDir); symlinkErr != nil {
				repairErr = fmt.Errorf("Codex created an unsafe repair workspace: %w", symlinkErr)
			} else {
				changes, captureErr := bc.repairRepo.CaptureAndCommitWithOptions(
					fmt.Sprintf("Miruri Codex repair attempt %d", attempt+1),
					repairworkspace.CaptureOptions{Filter: codexRepairChangeFilter(sourceDir)},
				)
				if captureErr != nil {
					repairErr = captureErr
				} else {
					for _, discarded := range changes.Discarded {
						result.DiscardedChanges = append(result.DiscardedChanges, model.DiscardedChange{
							Path: discarded.Path, Reason: discarded.Reason,
						})
					}
					if len(changes.Files) == 0 {
						repairErr = fmt.Errorf("Codex reported a repair but made no accepted source or build-script changes")
					} else {
						changedFiles = changes.Files
						result.ChangedFiles = append([]string(nil), changedFiles...)
						result.PatchPath = patchPath
						if err := os.WriteFile(patchPath, changes.Patch, 0o600); err != nil {
							repairErr = fmt.Errorf("write Codex repair patch: %w", err)
						}
					}
				}
			}
		}
		if repairErr != nil {
			if resetErr := bc.repairRepo.ResetTo(repairBase); resetErr != nil {
				repairErr = fmt.Errorf("%v; restore pre-repair checkpoint: %w", repairErr, resetErr)
			}
			result.Error = repairErr.Error()
		}
		resultWriteErr := codex.WriteResult(resultPath, result)
		if resultWriteErr != nil {
			if repairErr == nil {
				repairErr = fmt.Errorf("write Codex repair result: %w", resultWriteErr)
				if resetErr := bc.repairRepo.ResetTo(repairBase); resetErr != nil {
					repairErr = fmt.Errorf("%v; restore pre-repair checkpoint: %w", repairErr, resetErr)
				}
			} else {
				repairErr = fmt.Errorf("%v; write Codex repair result: %w", repairErr, resultWriteErr)
			}
			result.Error = repairErr.Error()
		}
		status := result.Response.Status
		if repairErr != nil && (status == "" || status == "repaired") {
			status = "error"
		}
		repairInfo := model.CodexRepairAttempt{
			Attempt:             attempt + 1,
			Status:              status,
			DurationMillis:      result.DurationMillis,
			ThreadID:            result.Events.ThreadID,
			TurnID:              result.Events.TurnID,
			Summary:             result.Response.Summary,
			ChangedFiles:        changedFiles,
			Assumptions:         result.Response.Assumptions,
			RemainingRisks:      result.Response.RemainingRisks,
			PromptFile:          packageRelative(packageDir, result.PromptPath),
			DiagnosticsFile:     packageRelative(packageDir, result.DiagnosticsPath),
			DiagnosticsJSONFile: packageRelative(packageDir, result.DiagnosticsJSONPath),
			EventLog:            packageRelative(packageDir, result.EventsPath),
			StderrLog:           packageRelative(packageDir, result.StderrPath),
			FinalMessageFile:    packageRelative(packageDir, result.FinalResponsePath),
			DiscardedChanges:    append([]model.DiscardedChange(nil), result.DiscardedChanges...),
			Usage: model.CodexUsage{
				InputTokens:           result.Events.InputTokens,
				CachedInputTokens:     result.Events.CachedInputTokens,
				OutputTokens:          result.Events.OutputTokens,
				ReasoningOutputTokens: result.Events.ReasoningOutputTokens,
			},
		}
		if _, statErr := os.Stat(resultPath); statErr == nil {
			repairInfo.ResultFile = packageRelative(packageDir, resultPath)
		}
		if _, statErr := os.Stat(patchPath); statErr == nil {
			repairInfo.PatchFile = packageRelative(packageDir, patchPath)
		}
		if repairErr != nil {
			repairInfo.Error = repairErr.Error()
		}
		bc.codexRepairs = append(bc.codexRepairs, repairInfo)
		bc.logf("Codex status: %s; duration: %s; changed files: %d\n", status, result.Duration.Round(time.Millisecond), len(changedFiles))
		if result.Response.Summary != "" {
			bc.logf("Codex summary: %s\n", result.Response.Summary)
		}
		for _, risk := range result.Response.RemainingRisks {
			bc.logf("  Codex remaining risk: %s\n", risk)
		}
		for _, changed := range changedFiles {
			bc.logf("  Codex changed: %s\n", changed)
		}
		for _, discarded := range result.DiscardedChanges {
			bc.logf("  Miruri discarded generated change: %s (%s)\n", discarded.Path, discarded.Reason)
		}
		if repairErr != nil {
			buildErr = fmt.Errorf("%w; %v", buildErr, repairErr)
			break
		}
		if err := bc.resetAfterRepair(ctx); err != nil {
			buildErr = fmt.Errorf("reset build state after repair: %w", err)
			break
		}

	}

	if err := os.WriteFile(bc.logPath, bc.logBuffer.Bytes(), 0o644); err != nil {
		return Result{}, err
	}
	if buildErr != nil {
		manifest := baseManifest(bc, analysisPath, planPath)
		manifest.Assurance = model.AssuranceGenerated
		manifest.Warnings = append(manifest.Warnings, "build failed: "+buildErr.Error())
		manifestPath := filepath.Join(packageDir, "manifest.json")
		_ = fsutil.WriteJSON(manifestPath, manifest)
		return Result{Manifest: manifest, ManifestPath: manifestPath, PackageDir: packageDir, WorkDir: workDir}, fmt.Errorf("build failed; see %s: %w", bc.logPath, buildErr)
	}

	searchRoot := buildDir
	if buildSystem == model.BuildSystemMake {
		searchRoot = sourceDir
	}
	inspection, err := inspect.CollectAndPackage(searchRoot, packageDir, config.Target)
	if err != nil {
		return Result{}, fmt.Errorf("collect artifacts: %w", err)
	}
	if len(inspection.Artifacts) == 0 {
		return Result{}, fmt.Errorf("build completed but no linked executable or library artifact was found under %s", searchRoot)
	}

	manifest := baseManifest(bc, analysisPath, planPath)
	manifest.Artifacts = inspection.Artifacts
	manifest.Warnings = append(manifest.Warnings, inspection.Warnings...)
	manifest.Assurance = model.AssuranceStaticValidated
	for _, artifact := range inspection.Artifacts {
		if !artifact.ArchitectureOK {
			manifest.Assurance = model.AssuranceLinked
			manifest.Warnings = append(manifest.Warnings, fmt.Sprintf("foreign or unresolved architecture: %s (%s)", artifact.PackagedPath, artifact.Architecture))
		}
	}
	manifest.Warnings = append(manifest.Warnings, "target artifacts were not executed; runtime and behavioral validation are intentionally outside Miruri v0.1")

	manifestPath := filepath.Join(packageDir, "manifest.json")
	if err := fsutil.WriteJSON(manifestPath, manifest); err != nil {
		return Result{}, err
	}
	if !config.KeepWork {
		_ = os.RemoveAll(workDir)
	}
	return Result{Manifest: manifest, ManifestPath: manifestPath, PackageDir: packageDir, WorkDir: workDir}, nil
}

func (bc *buildContext) runBuild(parent context.Context) error {
	if err := os.MkdirAll(bc.buildDir, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, bc.config.Timeout)
	defer cancel()
	switch bc.buildSystem {
	case model.BuildSystemCMake:
		return bc.runCMake(ctx)
	case model.BuildSystemMake:
		return bc.runMake(ctx)
	default:
		return fmt.Errorf("build system %s is not implemented", bc.buildSystem)
	}
}

func (bc *buildContext) runCMake(ctx context.Context) error {
	toolchainPath := filepath.Join(bc.workDir, "miruri-toolchain.cmake")
	content := generateCMakeToolchain(bc.config.Target, bc.config.Sysroot)
	if err := os.WriteFile(toolchainPath, []byte(content), 0o644); err != nil {
		return err
	}
	generator := bc.config.Generator
	if generator == "" {
		if _, err := exec.LookPath("ninja"); err == nil {
			generator = "Ninja"
		} else {
			generator = "Unix Makefiles"
		}
	}
	configureArgs := []string{
		"-S", bc.sourceDir,
		"-B", bc.buildDir,
		"-G", generator,
		"-DCMAKE_TOOLCHAIN_FILE=" + toolchainPath,
		"-DCMAKE_BUILD_TYPE=Release",
		"-DCMAKE_EXPORT_COMPILE_COMMANDS=ON",
	}
	if err := bc.runCommand(ctx, bc.workDir, nil, "cmake", configureArgs...); err != nil {
		return err
	}
	return bc.runCommand(ctx, bc.workDir, nil, "cmake", "--build", bc.buildDir, "--parallel")
}

func (bc *buildContext) runMake(ctx context.Context) error {
	jobs := runtime.NumCPU()
	if jobs > 8 {
		jobs = 8
	}
	return bc.runCommand(ctx, bc.sourceDir, bc.makeEnvironment(), "make", fmt.Sprintf("-j%d", jobs))
}

func (bc *buildContext) makeEnvironment() []string {
	env := append([]string{}, os.Environ()...)
	cc := compilerCommand("clang", bc.config.Target, bc.config.Sysroot)
	cxx := compilerCommand("clang++", bc.config.Target, bc.config.Sysroot)
	return append(env,
		"CC="+cc,
		"CXX="+cxx,
		"AR=llvm-ar",
		"RANLIB=llvm-ranlib",
		"STRIP=llvm-strip",
		"MIRURI_TARGET="+bc.config.Target.ID,
		"MIRURI_TARGET_TRIPLE="+bc.config.Target.Triple,
		"MIRURI_SYSROOT="+bc.config.Sysroot,
	)
}

func (bc *buildContext) resetAfterRepair(parent context.Context) error {
	if err := os.RemoveAll(bc.buildDir); err != nil {
		return err
	}
	if bc.buildSystem != model.BuildSystemMake {
		return nil
	}
	makePath, err := exec.LookPath("make")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	bc.logf("$ %s clean  # best-effort repair cleanup\n", makePath)
	command := exec.CommandContext(ctx, makePath, "clean")
	command.Dir = bc.sourceDir
	command.Env = bc.makeEnvironment()
	command.Stdout = &bc.logBuffer
	command.Stderr = &bc.logBuffer
	if err := command.Run(); err != nil {
		bc.logf("Miruri note: make clean was unavailable or failed; continuing with the repaired workspace: %v\n", err)
	}
	return nil
}

func (bc *buildContext) runCommand(ctx context.Context, dir string, env []string, name string, args ...string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("required tool %s is not on PATH", name)
	}
	bc.logf("$ %s %s\n", path, shellJoin(args))
	command := exec.CommandContext(ctx, path, args...)
	command.Dir = dir
	if env != nil {
		command.Env = env
	}
	writer := bc.outputWriter()
	command.Stdout = writer
	command.Stderr = writer
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("command timed out after %s", bc.config.Timeout)
		}
		return fmt.Errorf("command failed: %w", err)
	}
	return nil
}

func (bc *buildContext) logf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	_, _ = bc.logBuffer.WriteString(message)
	if bc.config.Progress != nil {
		_, _ = io.WriteString(bc.config.Progress, message)
	}
}

func (bc *buildContext) outputWriter() io.Writer {
	if bc.config.Progress == nil {
		return &bc.logBuffer
	}
	return io.MultiWriter(&bc.logBuffer, bc.config.Progress)
}

func chooseBuildSystem(systems []model.BuildSystem) (model.BuildSystem, error) {
	for _, preferred := range []model.BuildSystem{model.BuildSystemCMake, model.BuildSystemMake} {
		for _, detected := range systems {
			if detected == preferred {
				return preferred, nil
			}
		}
	}
	var values []string
	for _, system := range systems {
		values = append(values, string(system))
	}
	return "", fmt.Errorf("no supported build system detected; v0.1 supports CMake and Make (detected: %s)", strings.Join(values, ", "))
}

func validateEnvironment(profile model.TargetProfile, sysroot string) error {
	for _, tool := range []string{"clang"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("required tool %s is not on PATH", tool)
		}
	}
	if profile.OS == "windows" && runtime.GOOS != "windows" {
		return fmt.Errorf("Windows artifacts require a Windows build worker with the Windows SDK in Miruri v0.1")
	}
	if profile.OS == "darwin" && runtime.GOOS != "darwin" {
		return fmt.Errorf("macOS artifacts require a macOS build worker with Xcode/Apple SDK in Miruri v0.1")
	}
	if profile.RequiresSysroot && !target.IsNative(profile) && sysroot == "" {
		return fmt.Errorf("target %s requires a sysroot; pass --sysroot or set %s", profile.ID, sysrootEnvName(profile.ID))
	}
	if sysroot != "" {
		if info, err := os.Stat(sysroot); err != nil || !info.IsDir() {
			return fmt.Errorf("sysroot is not a readable directory: %s", sysroot)
		}
	}
	return nil
}

func resolveSysroot(profile model.TargetProfile, explicit string) string {
	if explicit != "" {
		if abs, err := filepath.Abs(explicit); err == nil {
			return abs
		}
		return explicit
	}
	if value := os.Getenv(sysrootEnvName(profile.ID)); value != "" {
		if abs, err := filepath.Abs(value); err == nil {
			return abs
		}
		return value
	}
	return ""
}

func sysrootEnvName(targetID string) string {
	replacer := strings.NewReplacer("-", "_", ".", "_", "/", "_")
	return "MIRURI_SYSROOT_" + strings.ToUpper(replacer.Replace(targetID))
}

func generateCMakeToolchain(profile model.TargetProfile, sysroot string) string {
	var lines []string
	lines = append(lines,
		"# Generated by Miruri. Target executables must not be run during artifact-only builds.",
		"set(CMAKE_TRY_COMPILE_TARGET_TYPE STATIC_LIBRARY)",
		"set(CMAKE_C_COMPILER clang)",
		"set(CMAKE_CXX_COMPILER clang++)",
	)
	if !target.IsNative(profile) {
		lines = append(lines,
			fmt.Sprintf("set(CMAKE_SYSTEM_NAME \"%s\")", cmakeEscape(profile.CMakeSystemName)),
			fmt.Sprintf("set(CMAKE_SYSTEM_PROCESSOR \"%s\")", cmakeEscape(profile.CMakeProcessor)),
			fmt.Sprintf("set(CMAKE_C_COMPILER_TARGET \"%s\")", cmakeEscape(profile.Triple)),
			fmt.Sprintf("set(CMAKE_CXX_COMPILER_TARGET \"%s\")", cmakeEscape(profile.Triple)),
		)
	}
	if profile.OS == "darwin" {
		arch := profile.Arch
		if arch == "x86_64" {
			arch = "x86_64"
		}
		lines = append(lines, fmt.Sprintf("set(CMAKE_OSX_ARCHITECTURES \"%s\")", cmakeEscape(arch)))
	}
	if sysroot != "" {
		lines = append(lines,
			fmt.Sprintf("set(CMAKE_SYSROOT \"%s\")", cmakeEscape(sysroot)),
			fmt.Sprintf("set(CMAKE_FIND_ROOT_PATH \"%s\")", cmakeEscape(sysroot)),
			"set(CMAKE_FIND_ROOT_PATH_MODE_PROGRAM NEVER)",
			"set(CMAKE_FIND_ROOT_PATH_MODE_LIBRARY ONLY)",
			"set(CMAKE_FIND_ROOT_PATH_MODE_INCLUDE ONLY)",
			"set(CMAKE_FIND_ROOT_PATH_MODE_PACKAGE ONLY)",
		)
	}
	if profile.DefaultLinker == "lld" {
		lines = append(lines,
			"set(CMAKE_EXE_LINKER_FLAGS_INIT \"-fuse-ld=lld\")",
			"set(CMAKE_SHARED_LINKER_FLAGS_INIT \"-fuse-ld=lld\")",
		)
	}
	return strings.Join(lines, "\n") + "\n"
}

func compilerCommand(compiler string, profile model.TargetProfile, sysroot string) string {
	parts := []string{compiler}
	if !target.IsNative(profile) {
		parts = append(parts, "--target="+profile.Triple)
	}
	if sysroot != "" {
		parts = append(parts, "--sysroot="+shellQuote(sysroot))
	}
	if profile.DefaultLinker == "lld" {
		parts = append(parts, "-fuse-ld=lld")
	}
	if profile.OS == "darwin" {
		parts = append(parts, "-arch", profile.Arch)
	}
	return strings.Join(parts, " ")
}

func baseManifest(bc *buildContext, analysisPath, planPath string) model.BuildManifest {
	version := bc.config.Version
	if version == "" {
		version = "dev"
	}
	return model.BuildManifest{
		SchemaVersion: "miruri.manifest.v1",
		GeneratedAt:   time.Now().UTC(),
		MiruriVersion: version,
		ProjectName:   bc.analysis.ProjectName,
		Target:        bc.config.Target,
		BuildSystem:   bc.buildSystem,
		CodexRepairs:  append([]model.CodexRepairAttempt(nil), bc.codexRepairs...),
		BuildLog:      filepath.ToSlash(bc.logPath),
		AnalysisFile:  filepath.ToSlash(analysisPath),
		PlanFile:      filepath.ToSlash(planPath),
	}
}

func packageRelative(packageDir, path string) string {
	if path == "" {
		return ""
	}
	rel, err := filepath.Rel(packageDir, path)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func cmakeEscape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\\", "/"), "\"", "\\\"")
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n\"'\\$`!&|;()<>*?[]{}") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func SortedEnvironmentNames(requirements []model.EnvironmentRequirement) []string {
	var names []string
	for _, requirement := range requirements {
		names = append(names, requirement.Name)
	}
	sort.Strings(names)
	return names
}
