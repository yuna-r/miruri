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
	"github.com/yuna-r/miruri/internal/target"
)

var (
	Version = "0.1.0-alpha.5"
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
	case "analyze":
		return runAnalyze(args[1:], stdout, stderr)
	case "plan":
		return runPlan(args[1:], stdout, stderr)
	case "build":
		return runBuild(args[1:], stdout, stderr)
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
	sysroot := set.String("sysroot", "", "target sysroot path")
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
	plan := planner.Create(report, profile, *sysroot)
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
	sysroot := set.String("sysroot", "", "target sysroot path")
	outDir := set.String("out", "", "output directory; default: <project>/dist")
	generator := set.String("generator", "", "CMake generator; default: Ninja when available")
	useCodex := set.Bool("codex", false, "allow constrained Codex repair attempts in the isolated source overlay")
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
	profile, err := target.Resolve(*targetID)
	if err != nil {
		fmt.Fprintln(stderr, "miruri build:", err)
		return 1
	}
	project := positionalPath(set.Args())
	result, err := builder.Build(context.Background(), builder.Config{
		ProjectDir:   project,
		Target:       profile,
		Sysroot:      *sysroot,
		OutDir:       *outDir,
		Generator:    *generator,
		UseCodex:     *useCodex,
		MaxRepairs:   *maxRepairs,
		CodexBinary:  *codexBin,
		CodexModel:   *codexModel,
		CodexProfile: *codexProfile,
		CodexAuth:    authMode,
		CodexTimeout: *codexTimeout,
		KeepWork:     *keepWork,
		DryRun:       *dryRun,
		Version:      Version,
		Timeout:      *timeout,
		Progress:     stderr,
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
  inspect   inspect one ELF, Mach-O, PE or archive artifact
  doctor    inspect the local build environment
  codex     verify Codex CLI installation and ChatGPT authentication
  targets   list built-in target profiles
  version   print version information

Examples:
  %s analyze .
  %s plan --target linux-riscv64 .
  %s build --target host fixtures/hello-c
  %s codex status
  %s inspect --target host dist/host/artifacts/program
  %s build --target linux-arm64 --sysroot /opt/sysroots/aarch64 .
  %s build --target linux-arm64 --sysroot /opt/sysroots/aarch64 --codex .

Miruri v0.1 supports CMake and Make projects. GUI, graphics, shader, audio,
input, plugin and asset requirements are represented from the first release,
while the initial builders focus on producing and statically inspecting linked
C/C++ artifacts.
`, name, name, name, name, name, name, name, name, name)
}
