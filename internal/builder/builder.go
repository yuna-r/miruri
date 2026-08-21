package builder

import (
	"archive/tar"
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
	"unicode/utf8"

	"github.com/yuna-r/miruri/internal/analyze"
	"github.com/yuna-r/miruri/internal/artifactset"
	"github.com/yuna-r/miruri/internal/codex"
	"github.com/yuna-r/miruri/internal/fingerprint"
	"github.com/yuna-r/miruri/internal/fsutil"
	"github.com/yuna-r/miruri/internal/inspect"
	"github.com/yuna-r/miruri/internal/licenses"
	"github.com/yuna-r/miruri/internal/model"
	"github.com/yuna-r/miruri/internal/planner"
	"github.com/yuna-r/miruri/internal/repairworkspace"
	"github.com/yuna-r/miruri/internal/sbom"
	"github.com/yuna-r/miruri/internal/sysroot"
	"github.com/yuna-r/miruri/internal/target"
	"github.com/yuna-r/miruri/internal/verify"
)

type Config struct {
	ProjectDir              string
	Target                  model.TargetProfile
	Sysroot                 string
	CacheDir                string
	Offline                 bool
	RefreshSysroot          bool
	SysrootTimeout          time.Duration
	OutDir                  string
	Generator               string
	UseCodex                bool
	CodexMode               codex.TaskMode
	MaxRepairs              int
	CodexBinary             string
	CodexModel              string
	CodexProfile            string
	CodexAuth               codex.AuthMode
	CodexTimeout            time.Duration
	CodexInstructions       string
	CodexInstructionsInline string
	CodexInstructionsFile   string
	KeepWork                bool
	DryRun                  bool
	Version                 string
	Timeout                 time.Duration
	Progress                io.Writer
	Analysis                *model.AnalysisReport
	ExcludePaths            []string
	Reuse                   bool
}

type Result struct {
	Manifest     model.BuildManifest
	ManifestPath string
	PackageDir   string
	WorkDir      string
	Reused       bool
}

type buildContext struct {
	config            Config
	analysis          model.AnalysisReport
	plan              model.PortingPlan
	buildSystem       model.BuildSystem
	projectAbs        string
	workDir           string
	sourceDir         string
	buildDir          string
	packageDir        string
	finalPackageDir   string
	cacheDir          string
	logPath           string
	logBuffer         bytes.Buffer
	codexRepairs      []model.CodexRepairAttempt
	repairRepo        *repairworkspace.Repository
	sysrootInfo       sysroot.Resolution
	sysrootLockPath   string
	toolchain         llvmToolchain
	startedAt         time.Time
	requestDigest     string
	buildID           string
	projectExclusions []string
	fidelityBaseline  portFidelityBaseline
}

func Build(ctx context.Context, config Config) (Result, error) {
	startedAt := time.Now().UTC()
	if config.MaxRepairs < 0 {
		return Result{}, fmt.Errorf("max repairs must be non-negative")
	}
	if config.CodexMode == "" {
		config.CodexMode = codex.TaskRepair
	}
	switch config.CodexMode {
	case codex.TaskRepair, codex.TaskPort, codex.TaskAuto:
	default:
		return Result{}, fmt.Errorf("invalid Codex mode %q", config.CodexMode)
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Minute
	}
	if config.SysrootTimeout <= 0 {
		config.SysrootTimeout = 45 * time.Minute
	}
	projectAbs, err := fsutil.CanonicalPath(config.ProjectDir)
	if err != nil {
		return Result{}, err
	}
	outDir := config.OutDir
	if outDir == "" {
		outDir = filepath.Join(projectAbs, "dist")
	}
	outDir, err = fsutil.CanonicalPath(outDir)
	if err != nil {
		return Result{}, err
	}
	if projectAbs == outDir {
		return Result{}, fmt.Errorf("output directory must not be the project root: %s", outDir)
	}
	projectExclusions := append([]string(nil), config.ExcludePaths...)
	projectExclusions = append(projectExclusions, outDir)
	fidelityBaseline, err := capturePortFidelityBaseline(projectAbs, projectExclusions)
	if err != nil {
		return Result{}, fmt.Errorf("capture original port-fidelity baseline: %w", err)
	}

	var analysisReport model.AnalysisReport
	if config.Analysis != nil {
		analysisReport = *config.Analysis
		if analysisReport.ProjectPath != "" {
			analysisPath, pathErr := fsutil.CanonicalPath(analysisReport.ProjectPath)
			if pathErr != nil || analysisPath != projectAbs {
				return Result{}, fmt.Errorf("precomputed analysis belongs to %q, not %q", analysisReport.ProjectPath, projectAbs)
			}
		}
	} else {
		analysisReport, err = analyze.Project(projectAbs, analyze.Options{ExcludePaths: projectExclusions})
		if err != nil {
			return Result{}, err
		}
	}
	if analysisReport.ProjectDigest == "" {
		projectFingerprint, fingerprintErr := fingerprint.Project(projectAbs, fingerprint.Options{ExcludePaths: projectExclusions})
		if fingerprintErr != nil {
			return Result{}, fingerprintErr
		}
		analysisReport.ProjectDigest = projectFingerprint.Digest
		analysisReport.ProjectEntries = projectFingerprint.FileCount
		analysisReport.ProjectBytes = projectFingerprint.ByteCount
		analysisReport.Warnings = append(analysisReport.Warnings, projectFingerprint.Warnings...)
	}
	var sysrootLog bytes.Buffer
	var sysrootProgress io.Writer = &sysrootLog
	if config.Progress != nil {
		sysrootProgress = io.MultiWriter(&sysrootLog, config.Progress)
	}
	sysrootManager := sysroot.New(sysroot.Options{CacheDir: config.CacheDir, Progress: sysrootProgress})
	sysrootContext, cancelSysroot := context.WithTimeout(ctx, config.SysrootTimeout)
	sysrootInfo, automaticSysroot, err := resolveSysroot(sysrootContext, config, sysrootManager)
	cancelSysroot()
	if err != nil {
		return Result{}, err
	}
	config.Sysroot = sysrootInfo.Path
	portingPlan := planner.CreateWithOptions(analysisReport, config.Target, planner.Options{
		Sysroot:          config.Sysroot,
		AutomaticSysroot: automaticSysroot,
	})
	buildSystem, err := chooseBuildSystem(analysisReport.BuildSystems)
	if err != nil {
		bootstrapPort := config.UseCodex && config.MaxRepairs > 0 && (config.CodexMode == codex.TaskPort || config.CodexMode == codex.TaskAuto)
		if !bootstrapPort {
			return Result{}, err
		}
		buildSystem = model.BuildSystemUnknown
		if config.Progress != nil {
			fmt.Fprintf(config.Progress, "Miruri port bootstrap: no supported native build system detected; Codex will create or revise a portable build system.\n")
		}
	}
	if strings.TrimSpace(config.CodexInstructionsFile) != "" && config.Reuse {
		progressf(config.Progress, "Miruri reuse: disabled because --instructions-file is live-reloaded between Codex attempts.\n")
		config.Reuse = false
	}
	requestDigest, err := buildRequestDigest(config, analysisReport, buildSystem, sysrootInfo)
	if err != nil {
		return Result{}, err
	}
	buildID := fingerprint.Short(requestDigest, 20)

	finalPackageDir := filepath.Join(outDir, config.Target.ID)
	if config.Reuse {
		if reused, ok := reuseArtifactSet(finalPackageDir, analysisReport.ProjectDigest, requestDigest, config.Progress); ok {
			reused.Reused = true
			return Result{
				Manifest:     reused,
				ManifestPath: filepath.Join(finalPackageDir, artifactset.ManifestName),
				PackageDir:   finalPackageDir,
				Reused:       true,
			}, nil
		}
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return Result{}, err
	}
	stagingRoot := filepath.Join(outDir, ".miruri-staging")
	if err := os.MkdirAll(stagingRoot, 0o755); err != nil {
		return Result{}, fmt.Errorf("create artifact staging root: %w", err)
	}
	packageDir, err := os.MkdirTemp(stagingRoot, config.Target.ID+"-"+buildID+"-")
	if err != nil {
		return Result{}, fmt.Errorf("create artifact staging directory: %w", err)
	}

	cacheRoot := sysrootManager.CacheDir()
	projectHash := sha256.Sum256([]byte(projectAbs))
	workDir := filepath.Join(cacheRoot, "work", hex.EncodeToString(projectHash[:8]), config.Target.ID)
	if err := os.RemoveAll(workDir); err != nil {
		return Result{}, fmt.Errorf("clean work directory: %w", err)
	}
	sourceDir := filepath.Join(workDir, "source")
	buildDir := filepath.Join(workDir, "build")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return Result{}, err
	}

	sysrootLockPath := ""
	if sysrootInfo.LockFile != "" {
		sysrootLockPath = filepath.Join(packageDir, "sysroot.lock.json")
		if err := fsutil.CopyFile(sysrootInfo.LockFile, sysrootLockPath, 0o644); err != nil {
			return Result{}, fmt.Errorf("copy sysroot lock into artifact set: %w", err)
		}
	}

	bc := &buildContext{
		config:            config,
		analysis:          analysisReport,
		plan:              portingPlan,
		buildSystem:       buildSystem,
		projectAbs:        projectAbs,
		workDir:           workDir,
		sourceDir:         sourceDir,
		buildDir:          buildDir,
		packageDir:        packageDir,
		finalPackageDir:   finalPackageDir,
		cacheDir:          cacheRoot,
		logPath:           filepath.Join(packageDir, "build.log"),
		sysrootInfo:       sysrootInfo,
		sysrootLockPath:   sysrootLockPath,
		startedAt:         startedAt,
		requestDigest:     requestDigest,
		buildID:           buildID,
		projectExclusions: projectExclusions,
		fidelityBaseline:  fidelityBaseline,
	}
	_, _ = bc.logBuffer.Write(sysrootLog.Bytes())
	if sysrootInfo.Mode != "" {
		bc.logf("Miruri sysroot: selected mode=%s target=%s", sysrootInfo.Mode, config.Target.ID)
		if sysrootInfo.ManifestDigest != "" {
			bc.logf(" digest=%s", sysrootInfo.ManifestDigest)
		}
		if sysrootInfo.Path != "" {
			bc.logf(" path=%s", sysrootInfo.Path)
		}
		bc.logf("\n")
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
		bc.logf("Miruri dry run: analysis and plan completed; build tools were not invoked.\n")
		if err := os.WriteFile(bc.logPath, bc.logBuffer.Bytes(), 0o644); err != nil {
			return Result{}, err
		}
		manifest := baseManifest(bc, analysisPath, planPath)
		manifest.Assurance = model.AssuranceGenerated
		manifest.Warnings = append(manifest.Warnings, "dry run: no compiler or linker was invoked")
		if sysrootInfo.Mode == "managed-pending" {
			manifest.Warnings = append(manifest.Warnings, "dry run: the managed sysroot provider was selected but no registry data was downloaded")
		}
		manifest, _, err := bc.finalizeManifest(manifest, "dry-run")
		if err != nil {
			return Result{}, err
		}
		if err := publishArtifactSet(packageDir, finalPackageDir); err != nil {
			return Result{}, err
		}
		manifestPath := filepath.Join(finalPackageDir, artifactset.ManifestName)
		return Result{Manifest: manifest, ManifestPath: manifestPath, PackageDir: finalPackageDir, WorkDir: workDir}, nil
	}

	toolchain, err := discoverLLVMToolchain(config.Target, config.Sysroot)
	if err != nil {
		return Result{}, err
	}
	bc.toolchain = toolchain
	if err := validateEnvironment(config.Target, config.Sysroot, toolchain); err != nil {
		return Result{}, err
	}
	if err := fsutil.CopyTreeWithOptions(projectAbs, sourceDir, fsutil.CopyTreeOptions{ExcludePaths: projectExclusions}); err != nil {
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

	var codexSession *codex.AppServerSession
	var codexAppServerTried bool

	var buildErr error
	continuationDirective := ""
	lastInstructionsDigest := digestText(config.CodexInstructions)
	for attempt := 0; attempt <= config.MaxRepairs; attempt++ {
		linkedBuildBeforeCodex := false
		if attempt > 0 {
			bc.logf("\n=== rebuild after Codex repair attempt %d ===\n", attempt)
		}
		attemptLogStart := bc.logBuffer.Len()
		buildErr = bc.runBuild(ctx)
		if buildErr == nil {
			linkedBuildBeforeCodex = true
			if fidelityErr := bc.validatePortFidelityAfterBuild(); fidelityErr != nil {
				bc.logf("%v\n", fidelityErr)
				buildErr = fidelityErr
			} else {
				break
			}
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
		attemptInstructions, instructionsErr := loadCodexInstructionsForAttempt(config)
		if instructionsErr != nil {
			buildErr = fmt.Errorf("%w; reload Codex instructions: %v", buildErr, instructionsErr)
			break
		}
		attemptInstructionsDigest := digestText(attemptInstructions)
		if strings.TrimSpace(config.CodexInstructionsFile) != "" && attemptInstructionsDigest != lastInstructionsDigest {
			bc.logf("Miruri Codex instructions: reloaded %s for attempt %d (%s).\n", config.CodexInstructionsFile, attempt+1, attemptInstructionsDigest)
		}
		lastInstructionsDigest = attemptInstructionsDigest
		if codexSession == nil && !codexAppServerTried && (config.CodexMode == codex.TaskPort || config.CodexMode == codex.TaskAuto) {
			codexAppServerTried = true
			session, sessionErr := codex.StartAppServerSession(ctx, codex.AppServerSessionConfig{
				Binary:    config.CodexBinary,
				Workspace: sourceDir,
				Model:     config.CodexModel,
				Profile:   config.CodexProfile,
				AuthMode:  config.CodexAuth,
			})
			if sessionErr != nil {
				bc.logf("Codex persistent app-server unavailable; falling back to per-attempt codex exec: %v\n", sessionErr)
			} else {
				codexSession = session
				defer codexSession.Close()
				bc.logf("Codex persistent app-server ready; thread %s will be reused across port attempts.\n", codexSession.ThreadID())
			}
		}
		result, repairErr := codex.Repair(ctx, codex.RepairRequest{
			Session:               codexSession,
			Mode:                  config.CodexMode,
			Binary:                config.CodexBinary,
			Workspace:             sourceDir,
			OutputDir:             attemptDir,
			Target:                config.Target,
			BuildSystem:           buildSystem,
			BuildLog:              latestBuildLog,
			Attempt:               attempt + 1,
			Model:                 config.CodexModel,
			Profile:               config.CodexProfile,
			AuthMode:              config.CodexAuth,
			Timeout:               config.CodexTimeout,
			MiruriVersion:         config.Version,
			PreservationBaseline:  bc.fidelityBaseline.promptSummary(80),
			ContinuationDirective: continuationDirective,
			CustomInstructions:    attemptInstructions,
			Progress: func(event codex.ProgressEvent) {
				bc.logf("[codex] %s\n", event.Message)
			},
		})
		patchPath := filepath.Join(attemptDir, "repair.patch")
		resultPath := filepath.Join(attemptDir, "result.json")
		var changedFiles []string
		continuationStatus := portContinuationStatus(config.CodexMode, result.Response.Status)
		if repairErr == nil {
			acceptedStatus := result.Response.Status == "repaired" || result.Response.Status == "ported" || continuationStatus
			if !acceptedStatus {
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
						completionReassessment := linkedBuildBeforeCodex &&
							(config.CodexMode == codex.TaskPort || config.CodexMode == codex.TaskAuto) &&
							(result.Response.Status == "ported" || result.Response.Status == "repaired")
						if !continuationStatus && !completionReassessment {
							repairErr = fmt.Errorf("Codex reported a repair but made no accepted source or build-script changes")
						} else if completionReassessment {
							bc.logf("Miruri port completion: accepting zero-change status %q as a fidelity reassessment because the immediately preceding Miruri build linked successfully.\n", result.Response.Status)
						}
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
			Mode:                string(config.CodexMode),
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

		if continuationStatus {
			continuationDirective = portContinuationDirective(attempt+1, result.Response, changedFiles)
			if len(changedFiles) == 0 {
				bc.logf("Miruri port continuation: Codex status %q is non-terminal while retry budget remains; no source changes were made, so the next attempt will receive an explicit implementation directive.\n", result.Response.Status)
			} else {
				bc.logf("Miruri port continuation: accepted %d incremental source/build change(s) with status %q; rebuilding before the next implementation attempt.\n", len(changedFiles), result.Response.Status)
			}
		} else {
			continuationDirective = ""
		}

		// A full-platform port may replace an unsupported/native-only project file
		// (for example Visual Studio .sln/.vcxproj) with a portable CMake, Meson,
		// Autotools, or Make build. Re-detect after every accepted Codex change so
		// the next attempt actually uses the newly generated build system.
		repairedAnalysis, analyzeErr := analyze.Project(sourceDir, analyze.Options{})
		if analyzeErr != nil {
			buildErr = fmt.Errorf("re-analyze repaired workspace: %w", analyzeErr)
			break
		}
		repairedBuildSystem, detectErr := chooseBuildSystem(repairedAnalysis.BuildSystems)
		if detectErr != nil {
			if bc.buildSystem == model.BuildSystemUnknown {
				bc.logf("Miruri port bootstrap: Codex has not created a supported portable build system yet; another port attempt is required.\n")
			} else {
				bc.logf("Miruri note: repaired workspace build-system re-detection failed; keeping %s: %v\n", bc.buildSystem, detectErr)
			}
		} else if repairedBuildSystem != bc.buildSystem {
			bc.logf("Miruri build system: %s -> %s after Codex port\n", bc.buildSystem, repairedBuildSystem)
			bc.buildSystem = repairedBuildSystem
			buildSystem = repairedBuildSystem
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
		portedSourceDir, snapshotErr := bc.preservePortedSource()
		_ = os.WriteFile(bc.logPath, bc.logBuffer.Bytes(), 0o644)
		manifest := baseManifest(bc, analysisPath, planPath)
		manifest.PortedSourceDir = portedSourceDir
		if snapshotErr != nil {
			manifest.Warnings = append(manifest.Warnings, "preserve final Codex source snapshot: "+snapshotErr.Error())
		}
		manifest.Assurance = model.AssuranceGenerated
		manifest.Warnings = append(manifest.Warnings, "build failed: "+buildErr.Error())
		manifest, manifestPath, finalizeErr := bc.finalizeManifest(manifest, "failed")
		if finalizeErr != nil {
			buildErr = fmt.Errorf("%w; finalize failed artifact set: %v", buildErr, finalizeErr)
		}
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
	packagedInstallTree := false
	if len(inspection.Artifacts) == 0 && buildSystem == model.BuildSystemMeson {
		stageRoot, stageErr := bc.stageMesonInstall(ctx)
		if stageErr != nil {
			_ = os.WriteFile(bc.logPath, bc.logBuffer.Bytes(), 0o644)
			return Result{}, fmt.Errorf("build completed without a linked binary and Meson install staging failed: %w", stageErr)
		}
		installArtifact, ok, packageErr := packageInstallTree(stageRoot, packageDir)
		if packageErr != nil {
			return Result{}, fmt.Errorf("package Meson install tree: %w", packageErr)
		}
		if ok {
			inspection.Artifacts = append(inspection.Artifacts, installArtifact)
			inspection.Warnings = append(inspection.Warnings, "no linked executable or library was produced; packaged the Meson staged install tree instead")
			packagedInstallTree = true
			if bc.config.Target.OS == "darwin" {
				appArtifact, appOK, appErr := packageSimpleMacOSApp(stageRoot, packageDir, bc.analysis.ProjectName)
				if appErr != nil {
					inspection.Warnings = append(inspection.Warnings, "macOS app bundle fallback was skipped: "+appErr.Error())
				} else if appOK {
					inspection.Artifacts = append(inspection.Artifacts, appArtifact)
					inspection.Warnings = append(inspection.Warnings, "generated a minimal macOS .app bundle from the staged interpreted application payload")
				}
			}
		}
	}
	if err := os.WriteFile(bc.logPath, bc.logBuffer.Bytes(), 0o644); err != nil {
		return Result{}, err
	}
	if len(inspection.Artifacts) == 0 {
		return Result{}, fmt.Errorf("build completed but no linked executable, library, or installable Meson payload was found under %s", searchRoot)
	}

	for index := range inspection.Artifacts {
		relative := packageRelative(packageDir, inspection.Artifacts[index].PackagedPath)
		inspection.Artifacts[index].PackagePath = relative
		inspection.Artifacts[index].PackagedPath = filepath.Join(finalPackageDir, filepath.FromSlash(relative))
	}
	portedSourceDir, err := bc.preservePortedSource()
	if err != nil {
		return Result{}, fmt.Errorf("preserve final Codex source snapshot: %w", err)
	}
	if err := os.WriteFile(bc.logPath, bc.logBuffer.Bytes(), 0o644); err != nil {
		return Result{}, err
	}
	manifest := baseManifest(bc, analysisPath, planPath)
	manifest.PortedSourceDir = portedSourceDir
	manifest.Artifacts = inspection.Artifacts
	manifest.Warnings = append(manifest.Warnings, inspection.Warnings...)
	manifest.Assurance = model.AssuranceStaticValidated
	if packagedInstallTree {
		manifest.Assurance = model.AssurancePackaged
	}
	for _, artifact := range inspection.Artifacts {
		if !artifact.ArchitectureOK {
			manifest.Assurance = model.AssuranceLinked
			manifest.Warnings = append(manifest.Warnings, fmt.Sprintf("foreign or unresolved architecture: %s (%s)", artifact.PackagedPath, artifact.Architecture))
		}
	}
	manifest.Warnings = append(manifest.Warnings, "target artifacts were not executed; runtime and behavioral validation are intentionally outside Miruri v0.1")

	manifest, _, err = bc.finalizeManifest(manifest, "succeeded")
	if err != nil {
		return Result{}, err
	}
	if err := publishArtifactSet(packageDir, finalPackageDir); err != nil {
		return Result{}, err
	}
	manifestPath := filepath.Join(finalPackageDir, artifactset.ManifestName)
	if !config.KeepWork {
		_ = os.RemoveAll(workDir)
	}
	return Result{Manifest: manifest, ManifestPath: manifestPath, PackageDir: finalPackageDir, WorkDir: workDir}, nil
}

func (bc *buildContext) preservePortedSource() (string, error) {
	if len(bc.codexRepairs) == 0 {
		return "", nil
	}
	info, err := os.Stat(bc.sourceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("isolated source workspace is not a directory: %s", bc.sourceDir)
	}
	relative := "ported-source"
	destination := filepath.Join(bc.packageDir, relative)
	if err := os.RemoveAll(destination); err != nil {
		return "", err
	}
	if err := fsutil.CopyTree(bc.sourceDir, destination); err != nil {
		return "", err
	}
	bc.logf("Miruri source snapshot: preserved final Codex workspace at %s\\n", relative)
	return relative, nil
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
	case model.BuildSystemMeson:
		return bc.runMeson(ctx)
	case model.BuildSystemAutotools:
		return bc.runAutotools(ctx)
	case model.BuildSystemMake:
		return bc.runMake(ctx)
	default:
		return fmt.Errorf("build system %s is not implemented", bc.buildSystem)
	}
}

func (bc *buildContext) runCMake(ctx context.Context) error {
	toolchainPath := filepath.Join(bc.workDir, "miruri-toolchain.cmake")
	content := generateCMakeToolchain(bc.config.Target, bc.config.Sysroot, bc.toolchain)
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
	environment := bc.toolchainEnvironment()
	if err := bc.runCommand(ctx, bc.workDir, environment, "cmake", configureArgs...); err != nil {
		return err
	}
	return bc.runCommand(ctx, bc.workDir, environment, "cmake", "--build", bc.buildDir, "--parallel")
}

func (bc *buildContext) runMeson(ctx context.Context) error {
	environment := bc.mesonEnvironment()
	meson, err := bc.resolveMeson(ctx)
	if err != nil {
		return fmt.Errorf("Meson setup failed: %w", err)
	}
	if meson.PythonPath != "" {
		environment = prependEnvironmentPath(environment, "PYTHONPATH", []string{meson.PythonPath})
	}
	setupArgs := []string{"setup", bc.buildDir, bc.sourceDir, "--buildtype=release"}
	if !target.IsNative(bc.config.Target) {
		crossFile := filepath.Join(bc.workDir, "miruri-meson-cross.ini")
		content, err := generateMesonCrossFile(bc.config.Target, bc.config.Sysroot, bc.toolchain)
		if err != nil {
			return err
		}
		if err := os.WriteFile(crossFile, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write Meson cross file: %w", err)
		}
		setupArgs = append(setupArgs, "--cross-file", crossFile)
	}
	setupCommandArgs := append(append([]string{}, meson.PrefixArgs...), setupArgs...)
	if err := bc.runCommand(ctx, bc.workDir, environment, meson.Executable, setupCommandArgs...); err != nil {
		return fmt.Errorf("Meson setup failed: %w", err)
	}
	jobs := runtime.NumCPU()
	if jobs > 8 {
		jobs = 8
	}
	compileArgs := append(append([]string{}, meson.PrefixArgs...), "compile", "-C", bc.buildDir, "-j", fmt.Sprintf("%d", jobs))
	if err := bc.runCommand(ctx, bc.workDir, environment, meson.Executable, compileArgs...); err != nil {
		return fmt.Errorf("Meson compile failed: %w", err)
	}
	return nil
}

func (bc *buildContext) stageMesonInstall(parent context.Context) (string, error) {
	stageRoot := filepath.Join(bc.workDir, "install-root")
	if err := os.RemoveAll(stageRoot); err != nil {
		return "", fmt.Errorf("clean Meson install staging directory: %w", err)
	}
	if err := os.MkdirAll(stageRoot, 0o755); err != nil {
		return "", fmt.Errorf("create Meson install staging directory: %w", err)
	}

	ctx, cancel := context.WithTimeout(parent, bc.config.Timeout)
	defer cancel()
	meson, err := bc.resolveMeson(ctx)
	if err != nil {
		return "", err
	}
	environment := bc.mesonEnvironment()
	if meson.PythonPath != "" {
		environment = prependEnvironmentPath(environment, "PYTHONPATH", []string{meson.PythonPath})
	}
	// DESTDIR keeps install scripts in packaging mode. In particular, GNOME
	// projects can skip host cache/database mutation while still producing the
	// complete install payload.
	environment = setEnvironment(environment, "DESTDIR", stageRoot)
	args := append(append([]string{}, meson.PrefixArgs...), "install", "-C", bc.buildDir, "--no-rebuild")
	if err := bc.runCommand(ctx, bc.workDir, environment, meson.Executable, args...); err != nil {
		return "", fmt.Errorf("Meson install failed: %w", err)
	}
	return stageRoot, nil
}

func packageInstallTree(stageRoot, packageDir string) (model.ArtifactInfo, bool, error) {
	entries := 0
	artifactDir := filepath.Join(packageDir, "artifacts")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return model.ArtifactInfo{}, false, err
	}
	finalPath := filepath.Join(artifactDir, "install-root.tar")
	temporary, err := os.CreateTemp(artifactDir, ".install-root-*.tar")
	if err != nil {
		return model.ArtifactInfo{}, false, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	tw := tar.NewWriter(temporary)
	walkErr := filepath.WalkDir(stageRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(stageRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		linkTarget := ""
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(path)
			if err != nil {
				return err
			}
			if filepath.IsAbs(linkTarget) {
				return fmt.Errorf("install tree contains absolute symlink %s -> %s", rel, linkTarget)
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(rel), linkTarget))
			if resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
				return fmt.Errorf("install tree symlink escapes package root: %s -> %s", rel, linkTarget)
			}
		}
		header, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		header.ModTime = time.Unix(0, 0).UTC()
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		header.Uid = 0
		header.Gid = 0
		header.Uname = ""
		header.Gname = ""
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			input, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, input)
			closeErr := input.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			entries++
		} else if info.Mode()&os.ModeSymlink != 0 {
			entries++
		}
		return nil
	})
	closeTarErr := tw.Close()
	closeFileErr := temporary.Close()
	if walkErr != nil {
		return model.ArtifactInfo{}, false, walkErr
	}
	if closeTarErr != nil {
		return model.ArtifactInfo{}, false, closeTarErr
	}
	if closeFileErr != nil {
		return model.ArtifactInfo{}, false, closeFileErr
	}
	if entries == 0 {
		return model.ArtifactInfo{}, false, nil
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return model.ArtifactInfo{}, false, err
	}
	file, err := os.Open(finalPath)
	if err != nil {
		return model.ArtifactInfo{}, false, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return model.ArtifactInfo{}, false, copyErr
	}
	if closeErr != nil {
		return model.ArtifactInfo{}, false, closeErr
	}
	return model.ArtifactInfo{
		SourcePath:     filepath.ToSlash(stageRoot),
		PackagedPath:   filepath.ToSlash(finalPath),
		Format:         "tar",
		Architecture:   "portable",
		Kind:           "install-tree",
		Size:           size,
		SHA256:         hex.EncodeToString(hash.Sum(nil)),
		ArchitectureOK: true,
		Notes: []string{
			"Meson DESTDIR install payload; extract preserving paths under the target installation root",
			fmt.Sprintf("contains %d regular file(s) or symlink(s)", entries),
		},
	}, true, nil
}

func generateMesonCrossFile(profile model.TargetProfile, sysrootPath string, toolchain llvmToolchain) (string, error) {
	if target.IsNative(profile) {
		return "", nil
	}
	if toolchain.CC == "" || toolchain.CXX == "" {
		return "", fmt.Errorf("Meson cross build requires Clang C/C++ compilers")
	}

	c := []string{toolchain.CC, "--target=" + profile.Triple}
	cpp := []string{toolchain.CXX, "--target=" + profile.Triple}
	if sysrootPath != "" {
		flag := "--sysroot=" + sysrootPath
		c = append(c, flag)
		cpp = append(cpp, flag)
	}
	if toolchain.GCCToolchain != "" {
		flag := "--gcc-toolchain=" + toolchain.GCCToolchain
		c = append(c, flag)
		cpp = append(cpp, flag)
	}
	if profile.DefaultLinker == "lld" && toolchain.Linker != "" {
		c = append(c, "-fuse-ld=lld")
		cpp = append(cpp, "-fuse-ld=lld")
	}

	var b strings.Builder
	b.WriteString("[binaries]\n")
	fmt.Fprintf(&b, "c = %s\n", mesonArray(c))
	fmt.Fprintf(&b, "cpp = %s\n", mesonArray(cpp))
	if toolchain.AR != "" {
		fmt.Fprintf(&b, "ar = %s\n", mesonString(toolchain.AR))
	}
	if toolchain.Strip != "" {
		fmt.Fprintf(&b, "strip = %s\n", mesonString(toolchain.Strip))
	}
	if pkg, err := exec.LookPath("pkg-config"); err == nil {
		fmt.Fprintf(&b, "pkg-config = %s\n", mesonString(pkg))
	} else if pkg, err := exec.LookPath("pkgconf"); err == nil {
		fmt.Fprintf(&b, "pkg-config = %s\n", mesonString(pkg))
	}
	b.WriteString("\n[host_machine]\n")
	fmt.Fprintf(&b, "system = %s\n", mesonString(mesonSystem(profile.OS)))
	fmt.Fprintf(&b, "cpu_family = %s\n", mesonString(mesonCPUFamily(profile.Arch)))
	fmt.Fprintf(&b, "cpu = %s\n", mesonString(mesonCPU(profile.Arch)))
	b.WriteString("endian = 'little'\n")
	if sysrootPath != "" {
		b.WriteString("\n[properties]\n")
		fmt.Fprintf(&b, "sys_root = %s\n", mesonString(sysrootPath))
	}
	return b.String(), nil
}

func mesonArray(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, mesonString(value))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func mesonString(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "'", "\\'")
	return "'" + value + "'"
}

func mesonSystem(osName string) string {
	switch strings.ToLower(osName) {
	case "macos", "darwin":
		return "darwin"
	default:
		return strings.ToLower(osName)
	}
}

func mesonCPUFamily(arch string) string {
	switch strings.ToLower(arch) {
	case "arm64", "aarch64":
		return "aarch64"
	case "x86_64", "amd64", "x64":
		return "x86_64"
	case "riscv64", "riscv32":
		return "riscv64"
	case "ppc64le", "powerpc64le":
		return "ppc64"
	default:
		return strings.ToLower(arch)
	}
}

func mesonCPU(arch string) string {
	switch strings.ToLower(arch) {
	case "arm64":
		return "aarch64"
	case "amd64", "x64":
		return "x86_64"
	default:
		return strings.ToLower(arch)
	}
}

func (bc *buildContext) runAutotools(ctx context.Context) error {
	environment := bc.autotoolsEnvironment()
	if needsAutoreconf(bc.sourceDir) {
		if err := bc.runCommand(ctx, bc.sourceDir, environment, "autoreconf", "-fi"); err != nil {
			return fmt.Errorf("Autotools bootstrap failed: %w", err)
		}
	}

	configurePath := filepath.Join(bc.sourceDir, "configure")
	if info, err := os.Stat(configurePath); err != nil || info.IsDir() {
		return fmt.Errorf("Autotools project has no generated configure script; install autoconf/automake/gettext development tools or provide a release source tree containing configure")
	}

	configureArgs := []string{configurePath}
	if !target.IsNative(bc.config.Target) {
		configureArgs = append(configureArgs, "--host="+bc.config.Target.Triple)
		if buildTriplet := detectAutotoolsBuildTriplet(ctx, bc.sourceDir, environment); buildTriplet != "" {
			configureArgs = append(configureArgs, "--build="+buildTriplet)
		}
	}
	if err := bc.runCommand(ctx, bc.buildDir, environment, "sh", configureArgs...); err != nil {
		return fmt.Errorf("Autotools configure failed: %w", err)
	}

	jobs := runtime.NumCPU()
	if jobs > 8 {
		jobs = 8
	}
	return bc.runCommand(ctx, bc.buildDir, environment, "make", fmt.Sprintf("-j%d", jobs))
}

func needsAutoreconf(sourceDir string) bool {
	configurePath := filepath.Join(sourceDir, "configure")
	configureInfo, configureErr := os.Stat(configurePath)
	if configureErr != nil || configureInfo.IsDir() {
		return existsAny(filepath.Join(sourceDir, "configure.ac"), filepath.Join(sourceDir, "configure.in"))
	}
	for _, input := range []string{"configure.ac", "configure.in", "Makefile.am"} {
		info, err := os.Stat(filepath.Join(sourceDir, input))
		if err == nil && info.ModTime().After(configureInfo.ModTime()) {
			return true
		}
	}
	return false
}

func existsAny(paths ...string) bool {
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func detectAutotoolsBuildTriplet(ctx context.Context, sourceDir string, environment []string) string {
	guess := filepath.Join(sourceDir, "config.guess")
	if info, err := os.Stat(guess); err != nil || info.IsDir() {
		return ""
	}
	command := exec.CommandContext(ctx, "sh", guess)
	command.Dir = sourceDir
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func (bc *buildContext) runMake(ctx context.Context) error {
	jobs := runtime.NumCPU()
	if jobs > 8 {
		jobs = 8
	}
	return bc.runCommand(ctx, bc.sourceDir, bc.makeEnvironment(), "make", fmt.Sprintf("-j%d", jobs))
}

func (bc *buildContext) toolchainEnvironment() []string {
	environment := append([]string{}, os.Environ()...)
	if bc.toolchain.BinDir != "" {
		environment = setEnvironment(environment, "PATH", bc.toolchain.BinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return environment
}

func (bc *buildContext) mesonEnvironment() []string {
	environment := bc.makeEnvironment()
	if runtime.GOOS == "darwin" && bc.config.Target.OS == "darwin" {
		if sdk := discoverAppleSDK(); sdk != "" {
			environment = setEnvironment(environment, "SDKROOT", sdk)
			environment = appendCompilerFlag(environment, "CC", "-isysroot "+shellQuote(sdk))
			environment = appendCompilerFlag(environment, "CXX", "-isysroot "+shellQuote(sdk))
		}
		if target.IsNative(bc.config.Target) {
			environment = appendEnvironmentPath(environment, "PKG_CONFIG_PATH", homebrewGlobDirectories(
				"/opt/homebrew/opt/*/lib/pkgconfig",
				"/opt/homebrew/opt/*/share/pkgconfig",
				"/usr/local/opt/*/lib/pkgconfig",
				"/usr/local/opt/*/share/pkgconfig",
			))
		}
	}
	return environment
}

func (bc *buildContext) autotoolsEnvironment() []string {
	environment := bc.makeEnvironment()
	if runtime.GOOS == "darwin" {
		if bc.config.Target.OS == "darwin" {
			if sdk := discoverAppleSDK(); sdk != "" {
				environment = setEnvironment(environment, "SDKROOT", sdk)
				environment = appendCompilerFlag(environment, "CC", "-isysroot "+shellQuote(sdk))
				environment = appendCompilerFlag(environment, "CXX", "-isysroot "+shellQuote(sdk))
			}
		}
		environment = appendEnvironmentPath(environment, "ACLOCAL_PATH", homebrewGlobDirectories(
			"/opt/homebrew/opt/*/share/aclocal",
			"/usr/local/opt/*/share/aclocal",
		))
		if target.IsNative(bc.config.Target) {
			environment = appendEnvironmentPath(environment, "PKG_CONFIG_PATH", homebrewGlobDirectories(
				"/opt/homebrew/opt/*/lib/pkgconfig",
				"/opt/homebrew/opt/*/share/pkgconfig",
				"/usr/local/opt/*/lib/pkgconfig",
				"/usr/local/opt/*/share/pkgconfig",
			))
		}
	}
	return environment
}

func discoverAppleSDK() string {
	path, err := exec.LookPath("xcrun")
	if err != nil {
		return ""
	}
	output, err := exec.Command(path, "--sdk", "macosx", "--show-sdk-path").Output()
	if err != nil {
		return ""
	}
	sdk := strings.TrimSpace(string(output))
	if info, err := os.Stat(sdk); err != nil || !info.IsDir() {
		return ""
	}
	return sdk
}

func appendCompilerFlag(environment []string, name, flag string) []string {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			value := strings.TrimPrefix(entry, prefix)
			if value == "" {
				return environment
			}
			return setEnvironment(environment, name, value+" "+flag)
		}
	}
	return environment
}

func prependEnvironmentPath(environment []string, name string, additions []string) []string {
	if len(additions) == 0 {
		return environment
	}
	existing := ""
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			existing = strings.TrimPrefix(entry, prefix)
			break
		}
	}
	parts := append([]string{}, additions...)
	if existing != "" {
		parts = append(parts, existing)
	}
	return setEnvironment(environment, name, strings.Join(parts, string(os.PathListSeparator)))
}

func appendEnvironmentPath(environment []string, name string, additions []string) []string {
	if len(additions) == 0 {
		return environment
	}
	existing := ""
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			existing = strings.TrimPrefix(entry, prefix)
			break
		}
	}
	parts := make([]string, 0, len(additions)+1)
	if existing != "" {
		parts = append(parts, existing)
	}
	parts = append(parts, additions...)
	return setEnvironment(environment, name, strings.Join(parts, string(os.PathListSeparator)))
}

func homebrewGlobDirectories(patterns ...string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		sort.Strings(matches)
		for _, candidate := range matches {
			if seen[candidate] {
				continue
			}
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				seen[candidate] = true
				out = append(out, candidate)
			}
		}
	}
	return out
}

func (bc *buildContext) makeEnvironment() []string {
	environment := bc.toolchainEnvironment()
	environment = setEnvironment(environment, "CC", compilerCommand(bc.toolchain.CC, bc.config.Target, bc.config.Sysroot, bc.toolchain))
	environment = setEnvironment(environment, "CXX", compilerCommand(bc.toolchain.CXX, bc.config.Target, bc.config.Sysroot, bc.toolchain))
	if bc.toolchain.AR != "" {
		environment = setEnvironment(environment, "AR", shellQuote(bc.toolchain.AR))
	}
	if bc.toolchain.Ranlib != "" {
		environment = setEnvironment(environment, "RANLIB", shellQuote(bc.toolchain.Ranlib))
	}
	if bc.toolchain.Strip != "" {
		environment = setEnvironment(environment, "STRIP", shellQuote(bc.toolchain.Strip))
	}
	environment = setEnvironment(environment, "MIRURI_TARGET", bc.config.Target.ID)
	environment = setEnvironment(environment, "MIRURI_TARGET_TRIPLE", bc.config.Target.Triple)
	environment = setEnvironment(environment, "MIRURI_SYSROOT", bc.config.Sysroot)
	if bc.config.Sysroot != "" {
		environment = setEnvironment(environment, "PKG_CONFIG_SYSROOT_DIR", bc.config.Sysroot)
		if libraryPath := pkgConfigLibraryPath(bc.config.Sysroot); libraryPath != "" {
			environment = setEnvironment(environment, "PKG_CONFIG_LIBDIR", libraryPath)
		}
	}
	return environment
}

func setEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	filtered := environment[:0]
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, prefix+value)
}

func pkgConfigLibraryPath(sysrootPath string) string {
	patterns := []string{
		filepath.Join(sysrootPath, "usr", "lib", "pkgconfig"),
		filepath.Join(sysrootPath, "usr", "share", "pkgconfig"),
		filepath.Join(sysrootPath, "usr", "lib", "*", "pkgconfig"),
	}
	var paths []string
	seen := make(map[string]bool)
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		if !strings.Contains(pattern, "*") {
			matches = []string{pattern}
		}
		for _, candidate := range matches {
			if seen[candidate] {
				continue
			}
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				seen[candidate] = true
				paths = append(paths, candidate)
			}
		}
	}
	return strings.Join(paths, string(os.PathListSeparator))
}

func portContinuationStatus(mode codex.TaskMode, status string) bool {
	if mode != codex.TaskPort && mode != codex.TaskAuto {
		return false
	}
	switch status {
	case "progress", "blocked", "no-change":
		return true
	default:
		return false
	}
}

func portContinuationDirective(attempt int, response codex.RepairResponse, changedFiles []string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Previous port attempt %d returned status %q.\n", attempt, response.Status)
	switch response.Status {
	case "blocked", "no-change":
		out.WriteString("Miruri is NOT treating that status as terminal while the port retry budget remains. A broad native backend, many platform-specific APIs, or a large refactor is implementation work, not a terminal blocker. Unless a concrete prerequisite outside the workspace/toolchain prevents every meaningful next step, make concrete source/build changes in this attempt instead of only describing required work.\n")
	case "progress":
		out.WriteString("Miruri accepted the previous changes as incremental porting progress. Continue from them; do not discard or replace preserved original behavior merely to finish faster. Make another coherent implementation slice and return progress again if more fidelity-preserving work remains.\n")
	}
	if summary := strings.TrimSpace(response.Summary); summary != "" {
		fmt.Fprintf(&out, "Previous summary: %s\n", summary)
	}
	if len(changedFiles) == 0 {
		out.WriteString("Previous accepted source/build changes: none. This attempt must make edits if any locally implementable porting work remains.\n")
	} else {
		fmt.Fprintf(&out, "Previous accepted source/build changes: %s\n", strings.Join(changedFiles, ", "))
	}
	if len(response.RemainingRisks) > 0 {
		out.WriteString("Review the previous remaining risks before doing more work. Only implement a risk if the shipped project actually exercises that case. Do not expand scope to support hypothetical formats/cases that are absent from this repository, and do not keep advisory caveats in remaining_risks.\n")
		out.WriteString("Previous remaining risks:\n")
		for _, risk := range response.RemainingRisks {
			if nonBlockingPortCaveat(risk) {
				fmt.Fprintf(&out, "  - [advisory; do not treat as a completion blocker after Miruri rebuild] %s\n", risk)
			} else {
				fmt.Fprintf(&out, "  - [re-evaluate project relevance, then fix if actually exercised] %s\n", risk)
			}
		}
	}
	return strings.TrimSpace(out.String())
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
	for _, preferred := range []model.BuildSystem{model.BuildSystemCMake, model.BuildSystemMeson, model.BuildSystemAutotools, model.BuildSystemMake} {
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
	return "", fmt.Errorf("no supported build system detected; v0.1 supports CMake, Meson, Autotools, and Make (detected: %s)", strings.Join(values, ", "))
}

func validateEnvironment(profile model.TargetProfile, sysrootPath string, toolchain llvmToolchain) error {
	if toolchain.CC == "" || toolchain.CXX == "" {
		return fmt.Errorf("Clang C/C++ toolchain is incomplete")
	}
	if profile.OS == "windows" && runtime.GOOS != "windows" {
		return fmt.Errorf("Windows artifacts require a Windows build worker with the Windows SDK in Miruri v0.1")
	}
	if profile.OS == "darwin" && runtime.GOOS != "darwin" {
		return fmt.Errorf("macOS artifacts require a macOS build worker with Xcode/Apple SDK in Miruri v0.1")
	}
	if profile.RequiresSysroot && !target.IsNative(profile) && sysrootPath == "" {
		return fmt.Errorf("target %s requires a sysroot; Miruri could not provision one automatically, so pass --sysroot or set %s", profile.ID, sysroot.EnvName(profile.ID))
	}
	if sysrootPath != "" {
		if info, err := os.Stat(sysrootPath); err != nil || !info.IsDir() {
			return fmt.Errorf("sysroot is not a readable directory: %s", sysrootPath)
		}
	}
	if profile.OS == "linux" && !target.IsNative(profile) {
		if toolchain.AR == "" || toolchain.Ranlib == "" {
			return fmt.Errorf("cross-Linux target %s requires llvm-ar and llvm-ranlib; install a complete LLVM toolchain or set MIRURI_LLVM_PREFIX", profile.ID)
		}
		if profile.DefaultLinker == "lld" && toolchain.Linker == "" {
			return fmt.Errorf("cross-Linux target %s requires ld.lld; install LLVM/LLD or set MIRURI_LLVM_PREFIX", profile.ID)
		}
		if toolchain.GCCToolchain == "" {
			return fmt.Errorf("cross-Linux target %s requires a GCC runtime installation inside the sysroot", profile.ID)
		}
	}
	return nil
}

func resolveSysroot(ctx context.Context, config Config, manager *sysroot.Manager) (sysroot.Resolution, bool, error) {
	if explicit := strings.TrimSpace(config.Sysroot); explicit != "" {
		return sysroot.Resolution{
			Mode:     "explicit",
			TargetID: config.Target.ID,
			Path:     absolutePath(explicit),
		}, false, nil
	}
	if value := strings.TrimSpace(os.Getenv(sysroot.EnvName(config.Target.ID))); value != "" {
		return sysroot.Resolution{
			Mode:     "environment",
			TargetID: config.Target.ID,
			Path:     absolutePath(value),
		}, false, nil
	}
	if !config.Target.RequiresSysroot || target.IsNative(config.Target) {
		return sysroot.Resolution{}, false, nil
	}
	provider, automatic := manager.Provider(config.Target)
	if resolution, found, err := manager.Lookup(config.Target); err != nil {
		if config.Offline || config.DryRun {
			return sysroot.Resolution{}, automatic, err
		}
	} else if found && !config.RefreshSysroot {
		return resolution, automatic, nil
	}
	if config.DryRun {
		if !automatic {
			return sysroot.Resolution{}, false, nil
		}
		if config.Offline {
			return sysroot.Resolution{}, true, fmt.Errorf("automatic sysroot for %s is not available in the local cache and --offline forbids registry access", config.Target.ID)
		}
		return sysroot.Resolution{
			Mode:     "managed-pending",
			TargetID: config.Target.ID,
			Provider: provider.ID,
			Source:   provider.Image,
			Platform: sysroot.PlatformString(provider),
		}, true, nil
	}
	resolution, err := manager.Ensure(ctx, config.Target, sysroot.EnsureOptions{
		Offline: config.Offline,
		Refresh: config.RefreshSysroot,
	})
	return resolution, automatic, err
}

func absolutePath(value string) string {
	if absolute, err := filepath.Abs(value); err == nil {
		return absolute
	}
	return value
}

func generateCMakeToolchain(profile model.TargetProfile, sysrootPath string, toolchain llvmToolchain) string {
	var lines []string
	lines = append(lines,
		"# Generated by Miruri. Target executables must not be run during artifact-only builds.",
		"# try_compile must link test executables so CMake's function/library probes remain trustworthy.",
		"# Cross-compiling does not execute them unless a project explicitly uses try_run().",
		"set(CMAKE_TRY_COMPILE_TARGET_TYPE EXECUTABLE)",
		fmt.Sprintf("set(CMAKE_C_COMPILER \"%s\")", cmakeEscape(toolchain.CC)),
		fmt.Sprintf("set(CMAKE_CXX_COMPILER \"%s\")", cmakeEscape(toolchain.CXX)),
	)
	if toolchain.AR != "" {
		lines = append(lines, fmt.Sprintf("set(CMAKE_AR \"%s\")", cmakeEscape(toolchain.AR)))
	}
	if toolchain.Ranlib != "" {
		lines = append(lines, fmt.Sprintf("set(CMAKE_RANLIB \"%s\")", cmakeEscape(toolchain.Ranlib)))
	}
	if toolchain.Strip != "" {
		lines = append(lines, fmt.Sprintf("set(CMAKE_STRIP \"%s\")", cmakeEscape(toolchain.Strip)))
	}
	if toolchain.Linker != "" {
		lines = append(lines, fmt.Sprintf("set(CMAKE_LINKER \"%s\")", cmakeEscape(toolchain.Linker)))
	}
	if !target.IsNative(profile) {
		lines = append(lines,
			fmt.Sprintf("set(CMAKE_SYSTEM_NAME \"%s\")", cmakeEscape(profile.CMakeSystemName)),
			fmt.Sprintf("set(CMAKE_SYSTEM_PROCESSOR \"%s\")", cmakeEscape(profile.CMakeProcessor)),
			fmt.Sprintf("set(CMAKE_C_COMPILER_TARGET \"%s\")", cmakeEscape(profile.Triple)),
			fmt.Sprintf("set(CMAKE_CXX_COMPILER_TARGET \"%s\")", cmakeEscape(profile.Triple)),
		)
	}
	if profile.OS == "darwin" {
		lines = append(lines, fmt.Sprintf("set(CMAKE_OSX_ARCHITECTURES \"%s\")", cmakeEscape(profile.Arch)))
	}
	if sysrootPath != "" {
		lines = append(lines,
			fmt.Sprintf("set(CMAKE_SYSROOT \"%s\")", cmakeEscape(sysrootPath)),
			fmt.Sprintf("set(CMAKE_FIND_ROOT_PATH \"%s\")", cmakeEscape(sysrootPath)),
			"set(CMAKE_FIND_ROOT_PATH_MODE_PROGRAM NEVER)",
			"set(CMAKE_FIND_ROOT_PATH_MODE_LIBRARY ONLY)",
			"set(CMAKE_FIND_ROOT_PATH_MODE_INCLUDE ONLY)",
			"set(CMAKE_FIND_ROOT_PATH_MODE_PACKAGE ONLY)",
		)
	}
	if toolchain.GCCToolchain != "" {
		lines = append(lines,
			fmt.Sprintf("set(CMAKE_C_COMPILER_EXTERNAL_TOOLCHAIN \"%s\")", cmakeEscape(toolchain.GCCToolchain)),
			fmt.Sprintf("set(CMAKE_CXX_COMPILER_EXTERNAL_TOOLCHAIN \"%s\")", cmakeEscape(toolchain.GCCToolchain)),
		)
	}
	if profile.DefaultLinker == "lld" && toolchain.Linker != "" {
		lines = append(lines,
			"set(CMAKE_EXE_LINKER_FLAGS_INIT \"-fuse-ld=lld\")",
			"set(CMAKE_SHARED_LINKER_FLAGS_INIT \"-fuse-ld=lld\")",
			"set(CMAKE_MODULE_LINKER_FLAGS_INIT \"-fuse-ld=lld\")",
		)
	}
	return strings.Join(lines, "\n") + "\n"
}

func compilerCommand(compiler string, profile model.TargetProfile, sysrootPath string, toolchain llvmToolchain) string {
	parts := []string{shellQuote(compiler)}
	if !target.IsNative(profile) {
		parts = append(parts, "--target="+profile.Triple)
	}
	if sysrootPath != "" {
		parts = append(parts, "--sysroot="+shellQuote(sysrootPath))
	}
	if toolchain.GCCToolchain != "" {
		parts = append(parts, "--gcc-toolchain="+shellQuote(toolchain.GCCToolchain))
	}
	if profile.DefaultLinker == "lld" && toolchain.Linker != "" {
		parts = append(parts, "-fuse-ld=lld")
	}
	if profile.OS == "darwin" {
		parts = append(parts, "-arch", profile.Arch)
	}
	return strings.Join(parts, " ")
}

func publishArtifactSet(stagingDir, finalDir string) error {
	if stagingDir == "" || finalDir == "" {
		return fmt.Errorf("artifact publication requires staging and final directories")
	}
	stagingAbs, err := filepath.Abs(stagingDir)
	if err != nil {
		return err
	}
	finalAbs, err := filepath.Abs(finalDir)
	if err != nil {
		return err
	}
	if filepath.Dir(stagingAbs) == finalAbs || stagingAbs == finalAbs {
		return fmt.Errorf("invalid artifact publication paths: staging=%s final=%s", stagingAbs, finalAbs)
	}
	if _, err := os.Stat(filepath.Join(stagingAbs, artifactset.ManifestName)); err != nil {
		return fmt.Errorf("staged artifact set is incomplete: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(finalAbs), 0o755); err != nil {
		return err
	}
	backup := finalAbs + fmt.Sprintf(".previous-%d", time.Now().UnixNano())
	hadPrevious := false
	if _, err := os.Lstat(finalAbs); err == nil {
		if err := os.Rename(finalAbs, backup); err != nil {
			return fmt.Errorf("preserve previous artifact set: %w", err)
		}
		hadPrevious = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect previous artifact set: %w", err)
	}
	if err := os.Rename(stagingAbs, finalAbs); err != nil {
		if hadPrevious {
			_ = os.Rename(backup, finalAbs)
		}
		return fmt.Errorf("publish staged artifact set: %w", err)
	}
	if hadPrevious {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func loadCodexInstructionsForAttempt(config Config) (string, error) {
	if strings.TrimSpace(config.CodexInstructionsFile) == "" {
		return config.CodexInstructions, nil
	}
	payload, err := os.ReadFile(config.CodexInstructionsFile)
	if err != nil {
		return "", fmt.Errorf("read instructions file %q: %w", config.CodexInstructionsFile, err)
	}
	if !utf8.Valid(payload) {
		return "", fmt.Errorf("instructions file %q is not valid UTF-8", config.CodexInstructionsFile)
	}
	sections := make([]string, 0, 2)
	if value := strings.TrimSpace(string(payload)); value != "" {
		sections = append(sections, value)
	}
	if value := strings.TrimSpace(config.CodexInstructionsInline); value != "" {
		sections = append(sections, value)
	}
	return strings.Join(sections, "\n\n"), nil
}

type buildRequestIdentity struct {
	SchemaVersion string              `json:"schema_version"`
	ProjectDigest string              `json:"project_digest"`
	Target        model.TargetProfile `json:"target"`
	BuildSystem   model.BuildSystem   `json:"build_system"`
	Sysroot       struct {
		Mode           string `json:"mode,omitempty"`
		Path           string `json:"path,omitempty"`
		Provider       string `json:"provider,omitempty"`
		Source         string `json:"source,omitempty"`
		ManifestDigest string `json:"manifest_digest,omitempty"`
		Platform       string `json:"platform,omitempty"`
	} `json:"sysroot"`
	Generator               string         `json:"generator,omitempty"`
	UseCodex                bool           `json:"use_codex"`
	CodexMode               codex.TaskMode `json:"codex_mode,omitempty"`
	CodexModel              string         `json:"codex_model,omitempty"`
	CodexProfile            string         `json:"codex_profile,omitempty"`
	CodexInstructionsDigest string         `json:"codex_instructions_digest,omitempty"`
	MaxRepairs              int            `json:"max_repairs"`
	DryRun                  bool           `json:"dry_run"`
	MiruriVersion           string         `json:"miruri_version"`
}

func buildRequestDigest(config Config, analysis model.AnalysisReport, buildSystem model.BuildSystem, resolution sysroot.Resolution) (string, error) {
	version := config.Version
	if version == "" {
		version = "dev"
	}
	identity := buildRequestIdentity{
		SchemaVersion:           "miruri.build-request.v1",
		ProjectDigest:           analysis.ProjectDigest,
		Target:                  config.Target,
		BuildSystem:             buildSystem,
		Generator:               config.Generator,
		UseCodex:                config.UseCodex,
		CodexMode:               config.CodexMode,
		CodexModel:              config.CodexModel,
		CodexProfile:            config.CodexProfile,
		CodexInstructionsDigest: digestText(config.CodexInstructions),
		MaxRepairs:              config.MaxRepairs,
		DryRun:                  config.DryRun,
		MiruriVersion:           version,
	}
	identity.Sysroot.Mode = resolution.Mode
	identity.Sysroot.Provider = resolution.Provider
	identity.Sysroot.Source = resolution.Source
	identity.Sysroot.ManifestDigest = resolution.ManifestDigest
	identity.Sysroot.Platform = resolution.Platform
	if resolution.ManifestDigest == "" {
		identity.Sysroot.Path = filepath.Clean(resolution.Path)
	}
	return fingerprint.JSON(identity)
}

func digestText(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func reuseArtifactSet(packageDir, projectDigest, requestDigest string, progress io.Writer) (model.BuildManifest, bool) {
	if _, err := os.Stat(filepath.Join(packageDir, artifactset.ManifestName)); err != nil {
		return model.BuildManifest{}, false
	}
	_, manifest, err := artifactset.LoadManifest(packageDir)
	if err != nil {
		progressf(progress, "Miruri reuse: existing artifact set is unreadable: %v\n", err)
		return model.BuildManifest{}, false
	}
	if manifest.BuildStatus != "succeeded" && manifest.BuildStatus != "dry-run" {
		progressf(progress, "Miruri reuse: existing artifact set status is %q; rebuilding.\n", manifest.BuildStatus)
		return model.BuildManifest{}, false
	}
	if manifest.ProjectDigest != projectDigest || manifest.RequestDigest != requestDigest {
		progressf(progress, "Miruri reuse: build identity changed; rebuilding target %s.\n", manifest.Target.ID)
		return model.BuildManifest{}, false
	}
	report, err := verify.ArtifactSet(packageDir, verify.Options{Strict: true})
	if err != nil || !report.Valid {
		if err != nil {
			progressf(progress, "Miruri reuse: verification failed: %v\n", err)
		} else {
			progressf(progress, "Miruri reuse: existing artifact set has %d verification finding(s); rebuilding.\n", len(report.Findings))
		}
		return model.BuildManifest{}, false
	}
	progressf(progress, "Miruri reuse: verified matching artifact set for %s (%s).\n", manifest.Target.ID, manifest.BuildID)
	return manifest, true
}

func progressf(progress io.Writer, format string, args ...any) {
	if progress != nil {
		_, _ = fmt.Fprintf(progress, format, args...)
	}
}

func (bc *buildContext) finalizeManifest(manifest model.BuildManifest, status string) (model.BuildManifest, string, error) {
	manifest.GeneratedAt = time.Now().UTC()
	manifest.StartedAt = bc.startedAt
	manifest.DurationMillis = time.Since(bc.startedAt).Milliseconds()
	manifest.BuildStatus = status
	manifest.BuildID = bc.buildID
	manifest.ProjectDigest = bc.analysis.ProjectDigest
	manifest.RequestDigest = bc.requestDigest
	manifest.LicenseReportFile = "licenses.json"
	manifest.SBOMFile = "sbom.spdx.json"
	manifest.IntegrityFile = artifactset.IntegrityName

	licenseReport, err := licenses.ScanWithOptions(bc.projectAbs, bc.analysis.ProjectName, bc.analysis.ProjectDigest, licenses.Options{ExcludePaths: bc.projectExclusions})
	if err != nil {
		return manifest, "", fmt.Errorf("scan license evidence: %w", err)
	}
	if err := fsutil.WriteJSON(filepath.Join(bc.packageDir, manifest.LicenseReportFile), licenseReport); err != nil {
		return manifest, "", fmt.Errorf("write license report: %w", err)
	}
	document := sbom.Generate(manifest, licenseReport, manifest.MiruriVersion)
	if err := fsutil.WriteJSON(filepath.Join(bc.packageDir, manifest.SBOMFile), document); err != nil {
		return manifest, "", fmt.Errorf("write SPDX SBOM: %w", err)
	}
	manifestPath := filepath.Join(bc.packageDir, artifactset.ManifestName)
	if err := fsutil.WriteJSON(manifestPath, manifest); err != nil {
		return manifest, "", fmt.Errorf("write manifest: %w", err)
	}
	if _, err := artifactset.WriteIntegrity(bc.packageDir); err != nil {
		return manifest, manifestPath, fmt.Errorf("write integrity index: %w", err)
	}
	verification, err := verify.ArtifactSet(bc.packageDir, verify.Options{Strict: true})
	if err != nil {
		return manifest, manifestPath, fmt.Errorf("verify staged artifact set: %w", err)
	}
	if !verification.Valid {
		return manifest, manifestPath, fmt.Errorf("verify staged artifact set: %d finding(s)", len(verification.Findings))
	}
	return manifest, manifestPath, nil
}

func baseManifest(bc *buildContext, analysisPath, planPath string) model.BuildManifest {
	version := bc.config.Version
	if version == "" {
		version = "dev"
	}
	manifest := model.BuildManifest{
		SchemaVersion: "miruri.manifest.v1",
		GeneratedAt:   time.Now().UTC(),
		StartedAt:     bc.startedAt,
		MiruriVersion: version,
		BuildID:       bc.buildID,
		ProjectName:   bc.analysis.ProjectName,
		ProjectDigest: bc.analysis.ProjectDigest,
		RequestDigest: bc.requestDigest,
		Target:        bc.config.Target,
		BuildSystem:   bc.buildSystem,
		Artifacts:     []model.ArtifactInfo{},
		CodexRepairs:  append([]model.CodexRepairAttempt(nil), bc.codexRepairs...),
		BuildLog:      packageRelative(bc.packageDir, bc.logPath),
		AnalysisFile:  packageRelative(bc.packageDir, analysisPath),
		PlanFile:      packageRelative(bc.packageDir, planPath),
	}
	if bc.sysrootInfo.Mode != "" {
		provenance := bc.sysrootInfo.Provenance()
		if bc.sysrootLockPath != "" {
			provenance.LockFile = packageRelative(bc.packageDir, bc.sysrootLockPath)
		}
		manifest.Sysroot = &provenance
	}
	if bc.toolchain.CC != "" {
		provenance := bc.toolchain.provenance()
		manifest.Toolchain = &provenance
	}
	return manifest
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
