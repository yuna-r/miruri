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

	"github.com/yuna-r/miruri/internal/analyze"
	"github.com/yuna-r/miruri/internal/builder"
	"github.com/yuna-r/miruri/internal/codex"
	"github.com/yuna-r/miruri/internal/doctor"
	"github.com/yuna-r/miruri/internal/fsutil"
	artifactinspect "github.com/yuna-r/miruri/internal/inspect"
	"github.com/yuna-r/miruri/internal/model"
	"github.com/yuna-r/miruri/internal/planner"
	"github.com/yuna-r/miruri/internal/sysroot"
	"github.com/yuna-r/miruri/internal/target"
)

var (
	Version = "0.1.0-alpha.8.2"
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
	report, err := analyze.Project(project, analyze.Options{})
	if err != nil {
		fmt.Fprintln(stderr, "miruri analyze:", err)
		return 1
	}
	if *output != "" {
		if err := fsutil.WriteJSON(*output, report); err != nil {
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
	report, err := analyze.Project(positionalPath(set.Args()), analyze.Options{})
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
	if *output != "" {
		if err := fsutil.WriteJSON(*output, plan); err != nil {
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
	keepWork := set.Bool("keep-work", false, "keep the isolated work directory")
	dryRun := set.Bool("dry-run", false, "write analysis and plan without invoking build tools")
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
	profile, err := target.Resolve(*targetID)
	if err != nil {
		fmt.Fprintln(stderr, "miruri build:", err)
		return 1
	}
	project := positionalPath(set.Args())
	result, err := builder.Build(context.Background(), builder.Config{
		ProjectDir:     project,
		Target:         profile,
		Sysroot:        *sysrootPath,
		CacheDir:       *cacheDir,
		Offline:        *offline,
		RefreshSysroot: *refreshSysroot,
		SysrootTimeout: *sysrootTimeout,
		OutDir:         *outDir,
		Generator:      *generator,
		UseCodex:       *useCodex,
		CodexMode:      codexMode,
		MaxRepairs:     *maxRepairs,
		CodexBinary:    *codexBin,
		CodexModel:     *codexModel,
		CodexProfile:   *codexProfile,
		CodexAuth:      authMode,
		CodexTimeout:   *codexTimeout,
		KeepWork:       *keepWork,
		DryRun:         *dryRun,
		Version:        Version,
		Timeout:        *timeout,
		Progress:       stderr,
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
		fmt.Fprintf(stdout, "  [%s] %-15s %-10s %s\n", mark, artifact.Architecture, artifact.Kind, artifact.PackagedPath)
	}
	fmt.Fprintf(stdout, "Manifest:            %s\n", result.ManifestPath)
	return 0
}

func runPort(args []string, stdout, stderr io.Writer) int {
	// Port mode intentionally grants Codex broader source/backend authority than
	// `build --codex`, while retaining the same isolated-workspace and
	// artifact-only execution policy. User-supplied flags later in args may
	// override these defaults.
	defaults := []string{
		"--codex",
		"--codex-mode", string(codex.TaskAuto),
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
	fmt.Fprintf(out, `%s — architecture-aware software artifact synthesizer

Usage:
  %s <command> [options] [project-directory]

Commands:
  analyze   scan the complete project and build a capability graph
  plan      select portability strategies for a target contract
  build     create an isolated target artifact set without executing it
  port      perform an authorized full platform port with Codex, then build
  sysroot   provision and manage verified cross-Linux sysroots
  inspect   inspect one ELF, Mach-O, PE or archive artifact
  doctor    inspect the local build environment
  codex     verify Codex CLI installation and ChatGPT authentication
  targets   list built-in target profiles
  version   print version information

Examples:
  %s analyze .
  %s plan --target linux-riscv64 .
  %s build --target host fixtures/hello-c
  %s sysroot ensure --target linux-arm64
  %s build --target linux-arm64 .
  %s build --target linux-arm64 --offline --codex .
  %s port --target linux-x86_64 ./windows-app
  %s build --target linux-riscv32 --sysroot /opt/sysroots/riscv32 .
  %s codex status
  %s inspect --target host dist/host/artifacts/program

Miruri v0.1 supports CMake, Meson, Autotools, and Make projects. Trusted cross-Linux profiles use
managed OCI sysroots by default. GUI, graphics, shader, audio, input, plugin and
asset requirements remain represented independently from the initial builders.
`, name, name, name, name, name, name, name, name, name, name, name, name)
}
