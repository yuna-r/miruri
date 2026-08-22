package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuna-r/miruri/internal/analyze"
	"github.com/yuna-r/miruri/internal/builder"
	"github.com/yuna-r/miruri/internal/codex"
	artifactcompare "github.com/yuna-r/miruri/internal/compare"
	"github.com/yuna-r/miruri/internal/doctor"
	"github.com/yuna-r/miruri/internal/fsutil"
	artifactinspect "github.com/yuna-r/miruri/internal/inspect"
	"github.com/yuna-r/miruri/internal/matrix"
	"github.com/yuna-r/miruri/internal/model"
	"github.com/yuna-r/miruri/internal/planner"
	"github.com/yuna-r/miruri/internal/sysroot"
	"github.com/yuna-r/miruri/internal/target"
	"github.com/yuna-r/miruri/internal/verify"
)

var (
	Version = "0.1.0-alpha.9.12"
	Commit  = "dev"
	Date    = "unknown"
)

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printRootHelp(stdout)
		return 0
	}
	switch args[0] {
	case "help", "-h", "--help":
		printRootHelp(stdout)
		return 0
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "miruri %s (commit %s, built %s)\n", Version, Commit, Date)
		return 0
	case "targets":
		return runTargets(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "codex":
		return runCodex(args[1:], stdout, stderr)
	case "sysroot":
		return runSysroot(args[1:], stdout, stderr)
	case "analyze":
		return runAnalyze(args[1:], stdout, stderr)
	case "plan":
		return runPlan(args[1:], stdout, stderr)
	case "build":
		return runBuild(args[1:], stdout, stderr)
	case "port":
		return runPort(args[1:], stdout, stderr)
	case "inspect":
		return runInspect(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "compare":
		return runCompare(args[1:], stdout, stderr)
	case "matrix":
		return runMatrix(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "miruri: unknown command %q\n\n", args[0])
		printRootHelp(stderr)
		return 2
	}
}

func runTargets(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("targets", flag.ContinueOnError)
	set.SetOutput(stderr)
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return 2
	}
	profiles, err := target.List()
	if err != nil {
		fmt.Fprintln(stderr, "miruri targets:", err)
		return 1
	}
	if *jsonOutput {
		return writeJSON(stdout, stderr, profiles)
	}
	for _, profile := range profiles {
		fmt.Fprintf(stdout, "%-20s %-12s %-10s %s\n", profile.ID, profile.OS, profile.Arch, profile.DisplayName)
	}
	return 0
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("doctor", flag.ContinueOnError)
	set.SetOutput(stderr)
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return 2
	}
	report := doctor.Run()
	if *jsonOutput {
		if code := writeJSON(stdout, stderr, report); code != 0 {
			return code
		}
	} else {
		fmt.Fprintf(stdout, "Host: %s/%s\n\n", report.HostOS, report.HostArch)
		for _, check := range report.Checks {
			status := "missing"
			if check.Found {
				status = "found"
			}
			required := "optional"
			if check.Required {
				required = "required"
			}
			fmt.Fprintf(stdout, "%-12s %-8s %-8s %s\n", check.Name, status, required, check.Purpose)
		}
		fmt.Fprintf(stdout, "\nArtifact builder ready: %t\n", report.Ready)
	}
	if !report.Ready {
		return 1
	}
	return 0
}

func runCodex(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] != "status" {
		fmt.Fprintf(stderr, "miruri codex: unknown subcommand %q; only status is currently supported\n", args[0])
		return 2
	}
	if len(args) > 0 {
		args = args[1:]
	}
	set := flag.NewFlagSet("codex status", flag.ContinueOnError)
	set.SetOutput(stderr)
	binary := set.String("bin", "codex", "Codex CLI executable")
	auth := set.String("auth", string(codex.AuthChatGPT), "authentication policy: chatgpt or inherit")
	jsonOutput := set.Bool("json", false, "print JSON")
	timeout := set.Duration("timeout", 30*time.Second, "status command timeout")
	if err := set.Parse(args); err != nil {
		return 2
	}
	authMode, err := parseCodexAuth(*auth)
	if err != nil {
		fmt.Fprintln(stderr, "miruri codex:", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	status, err := codex.Check(ctx, *binary, authMode)
	if err != nil {
		if *jsonOutput {
			_ = writeJSON(stdout, stderr, status)
		}
		fmt.Fprintln(stderr, "miruri codex:", err)
		return 1
	}
	if *jsonOutput {
		return writeJSON(stdout, stderr, status)
	}
	fmt.Fprintf(stdout, "Binary:        %s\n", status.Binary)
	fmt.Fprintf(stdout, "Version:       %s\n", status.Version)
	fmt.Fprintf(stdout, "Compatible:    %t\n", status.Compatible)
	fmt.Fprintf(stdout, "Authenticated: %t\n", status.Authenticated)
	fmt.Fprintf(stdout, "Auth mode:     %s\n", status.AuthMode)
	if status.AuthOutput != "" {
		fmt.Fprintf(stdout, "CLI status:    %s\n", strings.ReplaceAll(status.AuthOutput, "\n", " "))
	}
	return 0
}

func runSysroot(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printSysrootHelp(stdout)
		return 0
	}
	subcommand := args[0]
	args = args[1:]
	switch subcommand {
	case "providers":
		set := flag.NewFlagSet("sysroot providers", flag.ContinueOnError)
		set.SetOutput(stderr)
		jsonOutput := set.Bool("json", false, "print JSON")
		if err := set.Parse(args); err != nil {
			return 2
		}
		providers := sysroot.BuiltinProviders()
		if *jsonOutput {
			return writeJSON(stdout, stderr, providers)
		}
		for _, provider := range providers {
			fmt.Fprintf(stdout, "%-20s %-44s %s\n", provider.TargetID, provider.Image, sysroot.PlatformString(provider))
		}
		return 0
	case "ensure":
		set := flag.NewFlagSet("sysroot ensure", flag.ContinueOnError)
		set.SetOutput(stderr)
		targetID := set.String("target", "", "target profile ID (required)")
		cacheDir := set.String("cache-dir", "", "Miruri cache root")
		offline := set.Bool("offline", false, "forbid registry access")
		refresh := set.Bool("refresh", false, "resolve the provider tag again and update the target lock")
		timeout := set.Duration("timeout", 45*time.Minute, "registry and extraction timeout")
		jsonOutput := set.Bool("json", false, "print JSON")
		if err := set.Parse(args); err != nil {
			return 2
		}
		if strings.TrimSpace(*targetID) == "" {
			fmt.Fprintln(stderr, "miruri sysroot ensure: --target is required")
			return 2
		}
		profile, err := target.Resolve(*targetID)
		if err != nil {
			fmt.Fprintln(stderr, "miruri sysroot ensure:", err)
			return 1
		}
		manager := sysroot.New(sysroot.Options{CacheDir: *cacheDir, Progress: stderr})
		ensureContext, cancel := context.WithTimeout(context.Background(), *timeout)
		resolution, err := manager.Ensure(ensureContext, profile, sysroot.EnsureOptions{Offline: *offline, Refresh: *refresh})
		cancel()
		if err != nil {
			fmt.Fprintln(stderr, "miruri sysroot ensure:", err)
			return 1
		}
		if *jsonOutput {
			return writeJSON(stdout, stderr, resolution)
		}
		fmt.Fprintf(stdout, "Target:   %s\n", resolution.TargetID)
		fmt.Fprintf(stdout, "Path:     %s\n", resolution.Path)
		fmt.Fprintf(stdout, "Provider: %s\n", resolution.Provider)
		fmt.Fprintf(stdout, "Source:   %s\n", resolution.Source)
		fmt.Fprintf(stdout, "Digest:   %s\n", resolution.ManifestDigest)
		fmt.Fprintf(stdout, "Lock:     %s\n", resolution.LockFile)
		return 0
	case "list":
		set := flag.NewFlagSet("sysroot list", flag.ContinueOnError)
		set.SetOutput(stderr)
		cacheDir := set.String("cache-dir", "", "Miruri cache root")
		jsonOutput := set.Bool("json", false, "print JSON")
		if err := set.Parse(args); err != nil {
			return 2
		}
		manager := sysroot.New(sysroot.Options{CacheDir: *cacheDir})
		resolutions, err := manager.List()
		if err != nil {
			fmt.Fprintln(stderr, "miruri sysroot list:", err)
			return 1
		}
		if *jsonOutput {
			return writeJSON(stdout, stderr, resolutions)
		}
		for _, resolution := range resolutions {
			fmt.Fprintf(stdout, "%-20s %-24s %s\n", resolution.TargetID, shortCLIValue(resolution.ManifestDigest, 24), resolution.Path)
		}
		return 0
	case "path":
		set := flag.NewFlagSet("sysroot path", flag.ContinueOnError)
		set.SetOutput(stderr)
		targetID := set.String("target", "", "target profile ID (required)")
		cacheDir := set.String("cache-dir", "", "Miruri cache root")
		jsonOutput := set.Bool("json", false, "print JSON")
		if err := set.Parse(args); err != nil {
			return 2
		}
		if strings.TrimSpace(*targetID) == "" {
			fmt.Fprintln(stderr, "miruri sysroot path: --target is required")
			return 2
		}
		profile, err := target.Resolve(*targetID)
		if err != nil {
			fmt.Fprintln(stderr, "miruri sysroot path:", err)
			return 1
		}
		manager := sysroot.New(sysroot.Options{CacheDir: *cacheDir})
		resolution, found, err := manager.Lookup(profile)
		if err != nil {
			fmt.Fprintln(stderr, "miruri sysroot path:", err)
			return 1
		}
		if !found {
			fmt.Fprintf(stderr, "miruri sysroot path: no managed sysroot is locked for %s\n", profile.ID)
			return 1
		}
		if *jsonOutput {
			return writeJSON(stdout, stderr, resolution)
		}
		fmt.Fprintln(stdout, resolution.Path)
		return 0
	case "remove":
		set := flag.NewFlagSet("sysroot remove", flag.ContinueOnError)
		set.SetOutput(stderr)
		targetID := set.String("target", "", "target profile ID (required)")
		cacheDir := set.String("cache-dir", "", "Miruri cache root")
		purge := set.Bool("purge", false, "also remove the unreferenced content-addressed rootfs")
		if err := set.Parse(args); err != nil {
			return 2
		}
		if strings.TrimSpace(*targetID) == "" {
			fmt.Fprintln(stderr, "miruri sysroot remove: --target is required")
			return 2
		}
		profile, err := target.Resolve(*targetID)
		if err != nil {
			fmt.Fprintln(stderr, "miruri sysroot remove:", err)
			return 1
		}
		manager := sysroot.New(sysroot.Options{CacheDir: *cacheDir})
		if err := manager.Remove(profile.ID, *purge); err != nil {
			fmt.Fprintln(stderr, "miruri sysroot remove:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Removed managed sysroot lock for %s\n", profile.ID)
		return 0
	default:
		fmt.Fprintf(stderr, "miruri sysroot: unknown subcommand %q\n\n", subcommand)
		printSysrootHelp(stderr)
		return 2
	}
}

func resolvePlanSysroot(profile model.TargetProfile, explicit, cacheDir string) (string, bool, error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		if absolute, err := filepath.Abs(explicit); err == nil {
			return absolute, false, nil
		}
		return explicit, false, nil
	}
	if environment := strings.TrimSpace(os.Getenv(sysroot.EnvName(profile.ID))); environment != "" {
		if absolute, err := filepath.Abs(environment); err == nil {
			return absolute, false, nil
		}
		return environment, false, nil
	}
	manager := sysroot.New(sysroot.Options{CacheDir: cacheDir})
	if resolution, found, err := manager.Lookup(profile); err != nil {
		return "", false, err
	} else if found {
		return resolution.Path, true, nil
	}
	_, automatic := manager.Provider(profile)
	automatic = automatic && profile.RequiresSysroot && !target.IsNative(profile)
	return "", automatic, nil
}

func shortCLIValue(value string, length int) string {
	if len(value) <= length {
		return value
	}
	return value[:length]
}

func printSysrootHelp(out io.Writer) {
	fmt.Fprint(out, `Usage:
  miruri sysroot providers [--json]
  miruri sysroot ensure --target <target> [--refresh] [--offline]
  miruri sysroot list [--json]
  miruri sysroot path --target <target> [--json]
  miruri sysroot remove --target <target> [--purge]

Managed sysroots are pulled as OCI image layers, verified by SHA-256, expanded
without running target code, and pinned by an immutable manifest digest.
`)
}

func runAnalyze(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("analyze", flag.ContinueOnError)
	set.SetOutput(stderr)
	jsonOutput := set.Bool("json", false, "print JSON")
	output := set.String("output", "", "write full JSON report to a file")
	if err := set.Parse(args); err != nil {
		return 2
	}
	project := positionalPath(set.Args())
	outputPath, exclusions, err := resolveAnalysisOutput(*output)
	if err != nil {
		fmt.Fprintln(stderr, "miruri analyze:", err)
		return 1
	}
	report, err := analyze.Project(project, analyze.Options{ExcludePaths: exclusions})
	if err != nil {
		fmt.Fprintln(stderr, "miruri analyze:", err)
		return 1
	}
	if outputPath != "" {
		if err := fsutil.WriteJSON(outputPath, report); err != nil {
			fmt.Fprintln(stderr, "miruri analyze:", err)
			return 1
		}
	}
	if *jsonOutput {
		return writeJSON(stdout, stderr, report)
	}
	printAnalysis(stdout, report)
	return 0
}

func runPlan(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("plan", flag.ContinueOnError)
	set.SetOutput(stderr)
	targetID := set.String("target", "host", "target profile ID")
	sysrootPath := set.String("sysroot", "", "target sysroot path; automatic managed sysroot is used when omitted")
	cacheDir := set.String("cache-dir", "", "Miruri cache root; default: MIRURI_CACHE_DIR or the OS user cache")
	jsonOutput := set.Bool("json", false, "print JSON")
	output := set.String("output", "", "write full JSON plan to a file")
	if err := set.Parse(args); err != nil {
		return 2
	}
	profile, err := target.Resolve(*targetID)
	if err != nil {
		fmt.Fprintln(stderr, "miruri plan:", err)
		return 1
	}
	project := positionalPath(set.Args())
	outputPath, exclusions, err := resolveAnalysisOutput(*output)
	if err != nil {
		fmt.Fprintln(stderr, "miruri plan:", err)
		return 1
	}
	report, err := analyze.Project(project, analyze.Options{ExcludePaths: exclusions})
	if err != nil {
		fmt.Fprintln(stderr, "miruri plan:", err)
		return 1
	}
	resolvedSysroot, automaticSysroot, err := resolvePlanSysroot(profile, *sysrootPath, *cacheDir)
	if err != nil {
		fmt.Fprintln(stderr, "miruri plan:", err)
		return 1
	}
	plan := planner.CreateWithOptions(report, profile, planner.Options{
		Sysroot:          resolvedSysroot,
		AutomaticSysroot: automaticSysroot,
	})
	if outputPath != "" {
		if err := fsutil.WriteJSON(outputPath, plan); err != nil {
			fmt.Fprintln(stderr, "miruri plan:", err)
			return 1
		}
	}
	if *jsonOutput {
		return writeJSON(stdout, stderr, plan)
	}
	printPlan(stdout, plan)
	if plan.Status == "blocked" {
		return 3
	}
	return 0
}

func runBuild(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("build", flag.ContinueOnError)
	set.SetOutput(stderr)
	targetID := set.String("target", "host", "target profile ID")
	sysrootPath := set.String("sysroot", "", "target sysroot path; automatic managed sysroot is used when omitted")
	cacheDir := set.String("cache-dir", "", "Miruri cache root; default: MIRURI_CACHE_DIR or the OS user cache")
	offline := set.Bool("offline", false, "forbid registry access and require an already cached managed sysroot")
	refreshSysroot := set.Bool("refresh-sysroot", false, "resolve the provider tag again and update the target sysroot lock")
	sysrootTimeout := set.Duration("sysroot-timeout", 45*time.Minute, "timeout for managed sysroot provisioning")
	outDir := set.String("out", "", "output directory; default: <project>/dist")
	generator := set.String("generator", "", "CMake generator; default: Ninja when available")
	useCodex := set.Bool("codex", false, "allow Codex portability work in the isolated source overlay")
	codexModeFlag := set.String("codex-mode", string(codex.TaskRepair), "Codex task mode: repair, auto, or port")
	maxRepairs := set.Int("max-repairs", 2, "maximum Codex repair attempts")
	codexBin := set.String("codex-bin", "codex", "Codex CLI executable")
	codexModel := set.String("codex-model", "", "optional Codex model override")
	codexProfile := set.String("codex-profile", "", "optional Codex CLI profile")
	codexAuth := set.String("codex-auth", string(codex.AuthChatGPT), "Codex authentication policy: chatgpt or inherit")
	codexTimeout := set.Duration("codex-timeout", 20*time.Minute, "timeout for each Codex repair attempt")
	codexInstructions := set.String("instructions", "", "additional Codex instructions applied to every attempt")
	codexInstructionsFile := set.String("instructions-file", "", "read additional Codex instructions from a UTF-8 text file")
	keepWork := set.Bool("keep-work", false, "keep the isolated work directory")
	dryRun := set.Bool("dry-run", false, "write analysis and plan without invoking build tools")
	reuse := set.Bool("reuse", false, "reuse an identity-matching artifact set only after strict verification")
	jsonOutput := set.Bool("json", false, "print the final manifest as JSON")
	timeout := set.Duration("timeout", 30*time.Minute, "per build-attempt timeout")
	if err := set.Parse(args); err != nil {
		return 2
	}
	authMode, err := parseCodexAuth(*codexAuth)
	if err != nil {
		fmt.Fprintln(stderr, "miruri build:", err)
		return 2
	}
	codexMode, err := parseCodexMode(*codexModeFlag)
	if err != nil {
		fmt.Fprintln(stderr, "miruri build:", err)
		return 2
	}
	customInstructions, err := loadCodexInstructions(*codexInstructions, *codexInstructionsFile)
	if err != nil {
		fmt.Fprintln(stderr, "miruri build:", err)
		return 2
	}
	instructionsFilePath, err := absoluteInstructionsFilePath(*codexInstructionsFile)
	if err != nil {
		fmt.Fprintln(stderr, "miruri build:", err)
		return 2
	}
	if customInstructions != "" && !*useCodex {
		fmt.Fprintln(stderr, "miruri build: --instructions/--instructions-file require --codex (or use miruri port)")
		return 2
	}
	profile, err := target.Resolve(*targetID)
	if err != nil {
		fmt.Fprintln(stderr, "miruri build:", err)
		return 1
	}
	project := positionalPath(set.Args())
	result, err := builder.Build(context.Background(), builder.Config{
		ProjectDir:              project,
		Target:                  profile,
		Sysroot:                 *sysrootPath,
		CacheDir:                *cacheDir,
		Offline:                 *offline,
		RefreshSysroot:          *refreshSysroot,
		SysrootTimeout:          *sysrootTimeout,
		OutDir:                  *outDir,
		Generator:               *generator,
		UseCodex:                *useCodex,
		CodexMode:               codexMode,
		MaxRepairs:              *maxRepairs,
		CodexBinary:             *codexBin,
		CodexModel:              *codexModel,
		CodexProfile:            *codexProfile,
		CodexAuth:               authMode,
		CodexTimeout:            *codexTimeout,
		CodexInstructions:       customInstructions,
		CodexInstructionsInline: *codexInstructions,
		CodexInstructionsFile:   instructionsFilePath,
		KeepWork:                *keepWork,
		DryRun:                  *dryRun,
		Reuse:                   *reuse,
		Version:                 Version,
		Timeout:                 *timeout,
		Progress:                stderr,
	})
	if err != nil {
		fmt.Fprintln(stderr, "miruri build:", err)
		return 1
	}
	if *jsonOutput {
		return writeJSON(stdout, stderr, result.Manifest)
	}
	fmt.Fprintf(stdout, "Miruri artifact set: %s\n", result.PackageDir)
	fmt.Fprintf(stdout, "Target:              %s\n", result.Manifest.Target.ID)
	fmt.Fprintf(stdout, "Build ID:            %s\n", result.Manifest.BuildID)
	fmt.Fprintf(stdout, "Build status:        %s\n", result.Manifest.BuildStatus)
	fmt.Fprintf(stdout, "Project digest:      %s\n", result.Manifest.ProjectDigest)
	fmt.Fprintf(stdout, "Reused:              %t\n", result.Reused)
	if result.Manifest.Sysroot != nil {
		fmt.Fprintf(stdout, "Sysroot mode:        %s\n", result.Manifest.Sysroot.Mode)
		if result.Manifest.Sysroot.Path != "" {
			fmt.Fprintf(stdout, "Sysroot:             %s\n", result.Manifest.Sysroot.Path)
		}
		if result.Manifest.Sysroot.ManifestDigest != "" {
			fmt.Fprintf(stdout, "Sysroot digest:      %s\n", result.Manifest.Sysroot.ManifestDigest)
		}
	}
	fmt.Fprintf(stdout, "Assurance:           %s\n", result.Manifest.Assurance)
	fmt.Fprintf(stdout, "Artifacts:           %d\n", len(result.Manifest.Artifacts))
	fmt.Fprintf(stdout, "Codex repairs:       %d\n", len(result.Manifest.CodexRepairs))
	discardedChanges := 0
	for _, repair := range result.Manifest.CodexRepairs {
		discardedChanges += len(repair.DiscardedChanges)
	}
	if discardedChanges > 0 {
		fmt.Fprintf(stdout, "Generated changes discarded: %d\n", discardedChanges)
	}
	for _, artifact := range result.Manifest.Artifacts {
		mark := "OK"
		if !artifact.ArchitectureOK {
			mark = "REVIEW"
		}
		artifactPath := artifact.PackagePath
		if artifactPath == "" {
			artifactPath = artifact.PackagedPath
		}
		fmt.Fprintf(stdout, "  [%s] %-15s %-10s %s\n", mark, artifact.Architecture, artifact.Kind, artifactPath)
	}
	fmt.Fprintf(stdout, "Manifest:            %s\n", result.ManifestPath)
	fmt.Fprintf(stdout, "SPDX SBOM:           %s\n", filepath.Join(result.PackageDir, filepath.FromSlash(result.Manifest.SBOMFile)))
	fmt.Fprintf(stdout, "License evidence:    %s\n", filepath.Join(result.PackageDir, filepath.FromSlash(result.Manifest.LicenseReportFile)))
	fmt.Fprintf(stdout, "Integrity index:     %s\n", filepath.Join(result.PackageDir, filepath.FromSlash(result.Manifest.IntegrityFile)))
	return 0
}

func runPort(args []string, stdout, stderr io.Writer) int {
	// Port mode intentionally grants Codex broader source/backend authority than
	// `build --codex`, while retaining the same isolated-workspace and
	// artifact-only execution policy. User-supplied flags later in args may
	// override these defaults.
	defaults := []string{
		"--codex",
		"--codex-mode", string(codex.TaskPort),
		"--max-repairs", "12",
		"--codex-timeout", "45m",
	}
	return runBuild(append(defaults, args...), stdout, stderr)
}

func runInspect(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("inspect", flag.ContinueOnError)
	set.SetOutput(stderr)
	targetID := set.String("target", "host", "expected target profile ID")
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return 2
	}
	if len(set.Args()) == 0 {
		fmt.Fprintln(stderr, "miruri inspect: artifact path is required")
		return 2
	}
	profile, err := target.Resolve(*targetID)
	if err != nil {
		fmt.Fprintln(stderr, "miruri inspect:", err)
		return 1
	}
	artifact, recognized, err := artifactinspect.InspectFile(set.Args()[0], profile)
	if err != nil {
		fmt.Fprintln(stderr, "miruri inspect:", err)
		return 1
	}
	if !recognized {
		fmt.Fprintln(stderr, "miruri inspect: file is not a recognized ELF, Mach-O, PE or archive artifact")
		return 1
	}
	if *jsonOutput {
		return writeJSON(stdout, stderr, artifact)
	}
	fmt.Fprintf(stdout, "Format:           %s\n", artifact.Format)
	fmt.Fprintf(stdout, "Architecture:     %s\n", artifact.Architecture)
	fmt.Fprintf(stdout, "Kind:             %s\n", artifact.Kind)
	fmt.Fprintf(stdout, "Expected target:  %s\n", profile.ID)
	fmt.Fprintf(stdout, "Architecture OK:  %t\n", artifact.ArchitectureOK)
	fmt.Fprintf(stdout, "SHA-256:          %s\n", artifact.SHA256)
	if len(artifact.Dependencies) > 0 {
		fmt.Fprintln(stdout, "Dependencies:")
		for _, dependency := range artifact.Dependencies {
			fmt.Fprintf(stdout, "  - %s\n", dependency)
		}
	}
	if !artifact.ArchitectureOK {
		return 3
	}
	return 0
}

func runVerify(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("verify", flag.ContinueOnError)
	set.SetOutput(stderr)
	strict := set.Bool("strict", false, "treat missing or unindexed metadata as verification errors")
	jsonOutput := set.Bool("json", false, "print JSON")
	output := set.String("output", "", "write the verification report to a JSON file")
	if err := set.Parse(args); err != nil {
		return 2
	}
	if len(set.Args()) != 1 {
		fmt.Fprintln(stderr, "miruri verify: artifact-set directory or manifest path is required")
		return 2
	}
	report, err := verify.ArtifactSet(set.Args()[0], verify.Options{Strict: *strict})
	if err != nil {
		fmt.Fprintln(stderr, "miruri verify:", err)
		return 1
	}
	if *output != "" {
		if err := fsutil.WriteJSON(*output, report); err != nil {
			fmt.Fprintln(stderr, "miruri verify:", err)
			return 1
		}
	}
	if *jsonOutput {
		if code := writeJSON(stdout, stderr, report); code != 0 {
			return code
		}
	} else {
		status := "VALID"
		if !report.Valid {
			status = "INVALID"
		}
		fmt.Fprintf(stdout, "Artifact set:  %s\n", report.PackageDir)
		fmt.Fprintf(stdout, "Target:        %s\n", report.TargetID)
		fmt.Fprintf(stdout, "Build ID:      %s\n", report.BuildID)
		fmt.Fprintf(stdout, "Status:        %s\n", status)
		fmt.Fprintf(stdout, "Checked files: %d\n", report.CheckedFiles)
		fmt.Fprintf(stdout, "Findings:      %d\n", len(report.Findings))
		for _, finding := range report.Findings {
			location := finding.Path
			if location != "" {
				location = " " + location
			}
			fmt.Fprintf(stdout, "  [%-7s] %-30s%s: %s\n", strings.ToUpper(string(finding.Severity)), finding.Code, location, finding.Message)
		}
	}
	if !report.Valid {
		return 3
	}
	return 0
}

func runCompare(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("compare", flag.ContinueOnError)
	set.SetOutput(stderr)
	jsonOutput := set.Bool("json", false, "print JSON")
	output := set.String("output", "", "write the comparison report to a JSON file")
	if err := set.Parse(args); err != nil {
		return 2
	}
	if len(set.Args()) != 2 {
		fmt.Fprintln(stderr, "miruri compare: exactly two artifact-set directories or manifests are required")
		return 2
	}
	report, err := artifactcompare.ArtifactSets(set.Args()[0], set.Args()[1])
	if err != nil {
		fmt.Fprintln(stderr, "miruri compare:", err)
		return 1
	}
	if *output != "" {
		if err := fsutil.WriteJSON(*output, report); err != nil {
			fmt.Fprintln(stderr, "miruri compare:", err)
			return 1
		}
	}
	if *jsonOutput {
		if code := writeJSON(stdout, stderr, report); code != 0 {
			return code
		}
	} else {
		fmt.Fprintf(stdout, "Left:                 %s (%s / %s)\n", report.Left.PackageDir, report.Left.TargetID, report.Left.BuildID)
		fmt.Fprintf(stdout, "Right:                %s (%s / %s)\n", report.Right.PackageDir, report.Right.TargetID, report.Right.BuildID)
		fmt.Fprintf(stdout, "Equivalent:           %t\n", report.Equivalent)
		fmt.Fprintf(stdout, "Artifacts equivalent: %t\n", report.ArtifactEquivalent)
		fmt.Fprintf(stdout, "Artifacts:            +%d -%d ~%d =%d\n", report.ArtifactSummary.Added, report.ArtifactSummary.Removed, report.ArtifactSummary.Changed, report.ArtifactSummary.Same)
		fmt.Fprintf(stdout, "Differences:          %d\n", len(report.Differences))
		for _, difference := range report.Differences {
			fmt.Fprintf(stdout, "  [%-10s] %s\n", difference.Category, difference.Path)
			fmt.Fprintf(stdout, "    left:  %s\n", emptyAs(difference.Left, "<empty>"))
			fmt.Fprintf(stdout, "    right: %s\n", emptyAs(difference.Right, "<empty>"))
		}
	}
	if !report.Equivalent {
		return 3
	}
	return 0
}

func runMatrix(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("matrix", flag.ContinueOnError)
	set.SetOutput(stderr)
	targetIDs := set.String("targets", "", "comma-separated target profile IDs; default: host")
	allTargets := set.Bool("all", false, "select every built-in target profile")
	excludeIDs := set.String("exclude", "", "comma-separated target profile IDs to exclude")
	jobs := set.Int("jobs", 0, "maximum concurrent target tasks; default: min(CPU, 4)")
	failFast := set.Bool("fail-fast", false, "cancel targets that have not started after the first failure or blocked plan")
	planOnly := set.Bool("plan-only", false, "create plans for all targets without provisioning or building")
	reportPath := set.String("output", "", "matrix report path; default: <out>/matrix.json")
	jsonOutput := set.Bool("json", false, "print the matrix report as JSON")

	sysrootPath := set.String("sysroot", "", "explicit sysroot path applied to selected targets")
	cacheDir := set.String("cache-dir", "", "Miruri cache root")
	offline := set.Bool("offline", false, "forbid registry access and require cached managed sysroots")
	refreshSysroot := set.Bool("refresh-sysroot", false, "refresh managed sysroot locks")
	sysrootTimeout := set.Duration("sysroot-timeout", 45*time.Minute, "timeout for each managed sysroot provisioning task")
	outDir := set.String("out", "", "artifact output directory; default: <project>/dist")
	generator := set.String("generator", "", "CMake generator")
	useCodex := set.Bool("codex", false, "allow Codex portability work in isolated target workspaces")
	codexModeFlag := set.String("codex-mode", string(codex.TaskRepair), "Codex task mode: repair, auto, or port")
	maxRepairs := set.Int("max-repairs", 2, "maximum Codex repair attempts per target")
	codexBin := set.String("codex-bin", "codex", "Codex CLI executable")
	codexModel := set.String("codex-model", "", "optional Codex model override")
	codexProfile := set.String("codex-profile", "", "optional Codex CLI profile")
	codexAuth := set.String("codex-auth", string(codex.AuthChatGPT), "Codex authentication policy: chatgpt or inherit")
	codexTimeout := set.Duration("codex-timeout", 20*time.Minute, "timeout for each Codex repair attempt")
	codexInstructions := set.String("instructions", "", "additional Codex instructions applied to every attempt")
	codexInstructionsFile := set.String("instructions-file", "", "read additional Codex instructions from a UTF-8 text file")
	keepWork := set.Bool("keep-work", false, "keep isolated work directories")
	dryRun := set.Bool("dry-run", false, "write verified metadata-only artifact sets without invoking build tools")
	reuse := set.Bool("reuse", false, "reuse identity-matching artifact sets after strict verification")
	timeout := set.Duration("timeout", 30*time.Minute, "per build-attempt timeout")
	if err := set.Parse(args); err != nil {
		return 2
	}

	profiles, err := resolveMatrixTargets(*targetIDs, *excludeIDs, *allTargets)
	if err != nil {
		fmt.Fprintln(stderr, "miruri matrix:", err)
		return 2
	}
	authMode, err := parseCodexAuth(*codexAuth)
	if err != nil {
		fmt.Fprintln(stderr, "miruri matrix:", err)
		return 2
	}
	codexMode, err := parseCodexMode(*codexModeFlag)
	if err != nil {
		fmt.Fprintln(stderr, "miruri matrix:", err)
		return 2
	}
	customInstructions, err := loadCodexInstructions(*codexInstructions, *codexInstructionsFile)
	if err != nil {
		fmt.Fprintln(stderr, "miruri matrix:", err)
		return 2
	}
	instructionsFilePath, err := absoluteInstructionsFilePath(*codexInstructionsFile)
	if err != nil {
		fmt.Fprintln(stderr, "miruri matrix:", err)
		return 2
	}
	if customInstructions != "" && !*useCodex && !*planOnly {
		fmt.Fprintln(stderr, "miruri matrix: --instructions/--instructions-file require --codex")
		return 2
	}
	project := positionalPath(set.Args())
	report, err := matrix.Run(context.Background(), matrix.Config{
		ProjectDir: project,
		Targets:    profiles,
		Jobs:       *jobs,
		FailFast:   *failFast,
		PlanOnly:   *planOnly,
		OutDir:     *outDir,
		ReportPath: *reportPath,
		Progress:   stderr,
		Build: builder.Config{
			Sysroot:                 *sysrootPath,
			CacheDir:                *cacheDir,
			Offline:                 *offline,
			RefreshSysroot:          *refreshSysroot,
			SysrootTimeout:          *sysrootTimeout,
			Generator:               *generator,
			UseCodex:                *useCodex,
			CodexMode:               codexMode,
			MaxRepairs:              *maxRepairs,
			CodexBinary:             *codexBin,
			CodexModel:              *codexModel,
			CodexProfile:            *codexProfile,
			CodexAuth:               authMode,
			CodexTimeout:            *codexTimeout,
			CodexInstructions:       customInstructions,
			CodexInstructionsInline: *codexInstructions,
			CodexInstructionsFile:   instructionsFilePath,
			KeepWork:                *keepWork,
			DryRun:                  *dryRun,
			Reuse:                   *reuse,
			Version:                 Version,
			Timeout:                 *timeout,
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, "miruri matrix:", err)
		return 1
	}
	if *jsonOutput {
		if code := writeJSON(stdout, stderr, report); code != 0 {
			return code
		}
	} else {
		fmt.Fprintf(stdout, "Project:       %s\n", report.ProjectName)
		fmt.Fprintf(stdout, "Digest:        %s\n", report.ProjectDigest)
		fmt.Fprintf(stdout, "Mode:          %s\n", report.Mode)
		fmt.Fprintf(stdout, "Concurrency:   %d\n", report.Jobs)
		fmt.Fprintf(stdout, "Matrix report: %s\n", report.ReportPath)
		fmt.Fprintln(stdout, "Targets:")
		for _, result := range report.Results {
			detail := ""
			if result.BuildID != "" {
				detail = " build=" + result.BuildID
			}
			if result.Plan != nil {
				detail = " plan=" + result.Plan.Status
			}
			if result.Error != "" {
				detail += " error=" + result.Error
			}
			fmt.Fprintf(stdout, "  %-20s %-10s %6dms%s\n", result.Target.ID, result.Status, result.DurationMillis, detail)
		}
		fmt.Fprintf(stdout, "Summary: planned=%d succeeded=%d reused=%d failed=%d blocked=%d canceled=%d\n", report.Summary.Planned, report.Summary.Succeeded, report.Summary.Reused, report.Summary.Failed, report.Summary.Blocked, report.Summary.Canceled)
	}
	if report.Summary.Failed > 0 || report.Summary.Canceled > 0 {
		return 1
	}
	if report.Summary.Blocked > 0 {
		return 3
	}
	return 0
}

func loadCodexInstructions(inline, filePath string) (string, error) {
	sections := make([]string, 0, 2)
	if strings.TrimSpace(filePath) != "" {
		payload, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("read instructions file %q: %w", filePath, err)
		}
		if !utf8.Valid(payload) {
			return "", fmt.Errorf("instructions file %q is not valid UTF-8", filePath)
		}
		if value := strings.TrimSpace(string(payload)); value != "" {
			sections = append(sections, value)
		}
	}
	if value := strings.TrimSpace(inline); value != "" {
		sections = append(sections, value)
	}
	return strings.Join(sections, "\n\n"), nil
}

func absoluteInstructionsFilePath(filePath string) (string, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return "", nil
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return "", fmt.Errorf("resolve instructions file %q: %w", filePath, err)
	}
	return filepath.Clean(abs), nil
}

func resolveMatrixTargets(values, exclusions string, all bool) ([]model.TargetProfile, error) {
	var profiles []model.TargetProfile
	if all {
		listed, err := target.List()
		if err != nil {
			return nil, err
		}
		profiles = listed
	} else {
		ids := splitCSV(values)
		if len(ids) == 0 {
			ids = []string{"host"}
		}
		for _, id := range ids {
			profile, err := target.Resolve(id)
			if err != nil {
				return nil, err
			}
			profiles = append(profiles, profile)
		}
	}
	excluded := map[string]bool{}
	for _, id := range splitCSV(exclusions) {
		profile, err := target.Resolve(id)
		if err != nil {
			return nil, fmt.Errorf("invalid excluded target: %w", err)
		}
		excluded[profile.ID] = true
	}
	seen := map[string]bool{}
	filtered := profiles[:0]
	for _, profile := range profiles {
		if excluded[profile.ID] || seen[profile.ID] {
			continue
		}
		seen[profile.ID] = true
		filtered = append(filtered, profile)
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("target selection is empty after exclusions")
	}
	return filtered, nil
}

func splitCSV(value string) []string {
	var values []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}

func parseCodexAuth(value string) (codex.AuthMode, error) {
	mode := codex.AuthMode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case codex.AuthChatGPT, codex.AuthInherit:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid Codex auth policy %q; expected chatgpt or inherit", value)
	}
}

func parseCodexMode(value string) (codex.TaskMode, error) {
	mode := codex.TaskMode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case codex.TaskRepair, codex.TaskAuto, codex.TaskPort:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid Codex mode %q; expected repair, auto, or port", value)
	}
}

func resolveAnalysisOutput(value string) (string, []string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil, nil
	}
	absolute, err := fsutil.CanonicalPath(value)
	if err != nil {
		return "", nil, fmt.Errorf("resolve output path: %w", err)
	}
	return absolute, []string{absolute}, nil
}

func positionalPath(args []string) string {
	if len(args) == 0 {
		return "."
	}
	return args[0]
}

func writeJSON(stdout, stderr io.Writer, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(stderr, "miruri:", err)
		return 1
	}
	return 0
}

func printAnalysis(out io.Writer, report model.AnalysisReport) {
	fmt.Fprintf(out, "Project:       %s\n", report.ProjectName)
	fmt.Fprintf(out, "Path:          %s\n", report.ProjectPath)
	fmt.Fprintf(out, "Digest:        %s\n", report.ProjectDigest)
	fmt.Fprintf(out, "Fingerprint:   %d entries / %d bytes\n", report.ProjectEntries, report.ProjectBytes)
	fmt.Fprintf(out, "Files:         %d (%d text, %d binary)\n", report.FileCount, report.TextFileCount, report.BinaryCount)
	var buildSystems []string
	for _, system := range report.BuildSystems {
		buildSystems = append(buildSystems, string(system))
	}
	fmt.Fprintf(out, "Build systems: %s\n", strings.Join(buildSystems, ", "))
	fmt.Fprintln(out, "Languages:")
	var languages []string
	for language := range report.Languages {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	for _, language := range languages {
		fmt.Fprintf(out, "  %-24s %d\n", language, report.Languages[language])
	}
	fmt.Fprintln(out, "Capabilities:")
	if len(report.Requirements) == 0 {
		fmt.Fprintln(out, "  none detected by the current domain packs")
	}
	for _, requirement := range report.Requirements {
		kind := "soft"
		if requirement.Hard {
			kind = "hard"
		}
		fmt.Fprintf(out, "  %-30s %-5s %s\n", requirement.ID, kind, requirement.Description)
		for i, evidence := range requirement.Evidence {
			if i >= 3 {
				fmt.Fprintf(out, "    + %d more evidence item(s)\n", len(requirement.Evidence)-i)
				break
			}
			location := evidence.Path
			if evidence.Line > 0 {
				location += fmt.Sprintf(":%d", evidence.Line)
			}
			fmt.Fprintf(out, "    - %s (%s)\n", location, evidence.RuleID)
		}
	}
}

func printPlan(out io.Writer, plan model.PortingPlan) {
	fmt.Fprintf(out, "Project: %s\n", plan.ProjectName)
	fmt.Fprintf(out, "Target:  %s (%s/%s)\n", plan.Target.ID, plan.Target.OS, plan.Target.Arch)
	fmt.Fprintf(out, "Status:  %s\n\n", strings.ToUpper(plan.Status))
	if len(plan.Items) == 0 {
		fmt.Fprintln(out, "No domain-specific portability requirements were detected.")
	}
	for _, item := range plan.Items {
		blocking := ""
		if item.Blocking {
			blocking = " BLOCKING"
		}
		fmt.Fprintf(out, "%-30s -> %-24s%s\n", item.Requirement, item.Strategy, blocking)
		fmt.Fprintf(out, "  provider: %s\n", emptyAs(item.Provider, "not selected"))
		fmt.Fprintf(out, "  reason:   %s\n", item.Reason)
	}
	fmt.Fprintln(out, "\nEnvironment:")
	for _, requirement := range plan.Environment {
		fmt.Fprintf(out, "  %-18s %s\n", requirement.Name, requirement.Reason)
	}
}

func emptyAs(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func printRootHelp(out io.Writer) {
	name := filepath.Base(os.Args[0])
	if name == "" {
		name = "miruri"
	}
	fmt.Fprintf(out, `%[1]s — architecture-aware software artifact synthesizer

Usage:
  %[1]s <command> [options] [project-directory]

Commands:
  analyze   scan the complete project and build a capability graph
  plan      select portability strategies for a target contract
  build     create and verify an isolated target artifact set
  matrix    plan or build multiple targets with bounded parallelism
  port      perform an authorized full platform port with Codex, then build
  verify    verify one artifact set without executing target code
  compare   structurally compare two artifact sets
  sysroot   provision and manage verified cross-Linux sysroots
  inspect   inspect one ELF, Mach-O, PE or archive artifact
  doctor    inspect the local build environment
  codex     verify Codex CLI installation and ChatGPT authentication
  targets   list built-in target profiles
  version   print version information

Examples:
  %[1]s analyze .
  %[1]s plan --target linux-riscv64 .
  %[1]s build --target host fixtures/hello-c
  %[1]s build --target linux-arm64 --reuse .
  %[1]s matrix --plan-only --targets linux-x86_64,linux-arm64 --jobs 2 .
  %[1]s verify --strict dist/linux-arm64
  %[1]s compare dist/old/linux-arm64 dist/new/linux-arm64
  %[1]s sysroot ensure --target linux-arm64
  %[1]s build --target linux-arm64 --offline --codex .
  %[1]s port --target linux-x86_64 ./windows-app
  %[1]s build --target linux-riscv32 --sysroot /opt/sysroots/riscv32 .
  %[1]s codex status
  %[1]s inspect --target host dist/host/artifacts/program

Miruri v0.1 supports CMake, Meson, Autotools, and Make projects. Each published
artifact set is staged, self-verified, indexed by SHA-256, and accompanied by
analysis, plan, license evidence, and an SPDX 2.3 SBOM. Trusted cross-Linux
profiles use managed OCI sysroots by default. Target artifacts are never
executed by the artifact-only builder or verifier.
`, name)
}
