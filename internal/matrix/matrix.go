package matrix

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yuna-r/miruri/internal/analyze"
	"github.com/yuna-r/miruri/internal/builder"
	"github.com/yuna-r/miruri/internal/fsutil"
	"github.com/yuna-r/miruri/internal/model"
	"github.com/yuna-r/miruri/internal/planner"
	"github.com/yuna-r/miruri/internal/sysroot"
	"github.com/yuna-r/miruri/internal/target"
)

const SchemaVersion = "miruri.matrix.v1"

type Config struct {
	ProjectDir string
	Targets    []model.TargetProfile
	Jobs       int
	FailFast   bool
	PlanOnly   bool
	OutDir     string
	ReportPath string
	Build      builder.Config
	Progress   io.Writer
}

type TargetResult struct {
	Target         model.TargetProfile     `json:"target"`
	Status         string                  `json:"status"`
	StartedAt      time.Time               `json:"started_at,omitempty"`
	DurationMillis int64                   `json:"duration_ms,omitempty"`
	Plan           *model.PortingPlan      `json:"plan,omitempty"`
	PackageDir     string                  `json:"package_dir,omitempty"`
	ManifestPath   string                  `json:"manifest_path,omitempty"`
	BuildID        string                  `json:"build_id,omitempty"`
	BuildStatus    string                  `json:"build_status,omitempty"`
	Assurance      model.ArtifactAssurance `json:"assurance,omitempty"`
	ArtifactCount  int                     `json:"artifact_count,omitempty"`
	Reused         bool                    `json:"reused,omitempty"`
	Error          string                  `json:"error,omitempty"`
}

type Summary struct {
	Total     int `json:"total"`
	Planned   int `json:"planned"`
	Succeeded int `json:"succeeded"`
	Reused    int `json:"reused"`
	Failed    int `json:"failed"`
	Canceled  int `json:"canceled"`
	Blocked   int `json:"blocked"`
}

type Report struct {
	SchemaVersion  string         `json:"schema_version"`
	GeneratedAt    time.Time      `json:"generated_at"`
	StartedAt      time.Time      `json:"started_at"`
	DurationMillis int64          `json:"duration_ms"`
	ProjectName    string         `json:"project_name"`
	ProjectPath    string         `json:"project_path"`
	ProjectDigest  string         `json:"project_digest"`
	Mode           string         `json:"mode"`
	Jobs           int            `json:"jobs"`
	FailFast       bool           `json:"fail_fast"`
	Results        []TargetResult `json:"results"`
	Summary        Summary        `json:"summary"`
	ReportPath     string         `json:"report_path,omitempty"`
}

// Run analyzes the source tree once, then plans or builds every target with a
// bounded worker pool. Results preserve the caller's target order.
func Run(ctx context.Context, config Config) (Report, error) {
	startedAt := time.Now().UTC()
	if len(config.Targets) == 0 {
		return Report{}, fmt.Errorf("matrix requires at least one target")
	}
	if config.Jobs <= 0 {
		config.Jobs = runtime.NumCPU()
		if config.Jobs > 4 {
			config.Jobs = 4
		}
	}
	if config.Jobs > len(config.Targets) {
		config.Jobs = len(config.Targets)
	}
	if config.Jobs < 1 {
		config.Jobs = 1
	}

	projectDir := config.ProjectDir
	if strings.TrimSpace(projectDir) == "" {
		projectDir = "."
	}
	projectAbs, err := fsutil.CanonicalPath(projectDir)
	if err != nil {
		return Report{}, fmt.Errorf("resolve project: %w", err)
	}
	outDir := strings.TrimSpace(config.OutDir)
	if outDir == "" {
		outDir = strings.TrimSpace(config.Build.OutDir)
	}
	if outDir == "" {
		outDir = filepath.Join(projectAbs, "dist")
	}
	outDir, err = fsutil.CanonicalPath(outDir)
	if err != nil {
		return Report{}, fmt.Errorf("resolve matrix output: %w", err)
	}
	if filepath.Clean(outDir) == filepath.Clean(projectAbs) {
		return Report{}, fmt.Errorf("matrix output directory must not be the project root: %s", outDir)
	}
	reportPath := strings.TrimSpace(config.ReportPath)
	if reportPath == "" {
		reportPath = filepath.Join(outDir, "matrix.json")
	}
	reportPath, err = fsutil.CanonicalPath(reportPath)
	if err != nil {
		return Report{}, fmt.Errorf("resolve matrix report: %w", err)
	}
	projectExclusions := append([]string(nil), config.Build.ExcludePaths...)
	projectExclusions = append(projectExclusions, outDir, reportPath)
	analysis, err := analyze.Project(projectAbs, analyze.Options{ExcludePaths: projectExclusions})
	if err != nil {
		return Report{}, err
	}

	report := Report{
		SchemaVersion: SchemaVersion,
		StartedAt:     startedAt,
		ProjectName:   analysis.ProjectName,
		ProjectPath:   analysis.ProjectPath,
		ProjectDigest: analysis.ProjectDigest,
		Mode:          "build",
		Jobs:          config.Jobs,
		FailFast:      config.FailFast,
		Results:       make([]TargetResult, len(config.Targets)),
		ReportPath:    reportPath,
	}
	if config.PlanOnly {
		report.Mode = "plan"
	}

	progress := newSharedProgress(config.Progress)
	progress.printf("Miruri matrix: project=%s digest=%s targets=%d jobs=%d mode=%s\n", analysis.ProjectName, short(analysis.ProjectDigest, 16), len(config.Targets), config.Jobs, report.Mode)

	workerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	tasks := make(chan int)
	var workers sync.WaitGroup
	var resultMu sync.Mutex

	worker := func() {
		defer workers.Done()
		for index := range tasks {
			select {
			case <-workerContext.Done():
				resultMu.Lock()
				if report.Results[index].Status == "" {
					report.Results[index] = TargetResult{Target: config.Targets[index], Status: "canceled", Error: workerContext.Err().Error()}
				}
				resultMu.Unlock()
				continue
			default:
			}

			profile := config.Targets[index]
			started := time.Now().UTC()
			progress.printf("[%s] starting %s\n", profile.ID, report.Mode)
			var targetResult TargetResult
			if config.PlanOnly {
				targetResult = runPlan(profile, analysis, config.Build, progress.writer(profile.ID))
			} else {
				targetResult = runBuild(workerContext, profile, analysis, projectAbs, outDir, projectExclusions, config.Build, progress.writer(profile.ID))
			}
			targetResult.Target = profile
			targetResult.StartedAt = started
			targetResult.DurationMillis = time.Since(started).Milliseconds()
			resultMu.Lock()
			report.Results[index] = targetResult
			if targetResult.Status == "failed" || targetResult.Status == "blocked" {
				if config.FailFast {
					cancel()
				}
			}
			resultMu.Unlock()
			progress.printf("[%s] finished status=%s duration=%s\n", profile.ID, targetResult.Status, time.Since(started).Round(time.Millisecond))
		}
	}

	workers.Add(config.Jobs)
	for i := 0; i < config.Jobs; i++ {
		go worker()
	}

sendLoop:
	for index := range config.Targets {
		select {
		case tasks <- index:
		case <-workerContext.Done():
			resultMu.Lock()
			for remaining := index; remaining < len(config.Targets); remaining++ {
				if report.Results[remaining].Status == "" {
					report.Results[remaining] = TargetResult{Target: config.Targets[remaining], Status: "canceled", Error: workerContext.Err().Error()}
				}
			}
			resultMu.Unlock()
			break sendLoop
		}
	}
	close(tasks)
	workers.Wait()

	for index := range report.Results {
		if report.Results[index].Status == "" {
			report.Results[index] = TargetResult{Target: config.Targets[index], Status: "canceled", Error: "matrix canceled before target started"}
		}
	}
	report.GeneratedAt = time.Now().UTC()
	report.DurationMillis = time.Since(startedAt).Milliseconds()
	report.Summary = summarize(report.Results)
	if err := fsutil.WriteJSON(reportPath, report); err != nil {
		return report, fmt.Errorf("write matrix report: %w", err)
	}
	progress.printf("Miruri matrix: report=%s succeeded=%d reused=%d failed=%d blocked=%d canceled=%d\n", reportPath, report.Summary.Succeeded, report.Summary.Reused, report.Summary.Failed, report.Summary.Blocked, report.Summary.Canceled)
	return report, nil
}

func runPlan(profile model.TargetProfile, analysis model.AnalysisReport, base builder.Config, progress io.Writer) TargetResult {
	resolved, automatic, err := resolvePlanSysroot(profile, base.Sysroot, base.CacheDir)
	if err != nil {
		return TargetResult{Status: "failed", Error: err.Error()}
	}
	plan := planner.CreateWithOptions(analysis, profile, planner.Options{Sysroot: resolved, AutomaticSysroot: automatic})
	status := "planned"
	if plan.Status == "blocked" {
		status = "blocked"
	}
	if progress != nil {
		_, _ = fmt.Fprintf(progress, "plan status=%s items=%d environment=%d\n", plan.Status, len(plan.Items), len(plan.Environment))
	}
	return TargetResult{Status: status, Plan: &plan}
}

func runBuild(ctx context.Context, profile model.TargetProfile, analysis model.AnalysisReport, projectAbs, outDir string, exclusions []string, base builder.Config, progress io.Writer) TargetResult {
	buildConfig := base
	buildConfig.ProjectDir = projectAbs
	buildConfig.Target = profile
	buildConfig.OutDir = outDir
	buildConfig.Analysis = &analysis
	buildConfig.ExcludePaths = append(append([]string(nil), base.ExcludePaths...), exclusions...)
	buildConfig.Progress = progress
	result, err := builder.Build(ctx, buildConfig)
	targetResult := TargetResult{
		Status:        "succeeded",
		PackageDir:    result.PackageDir,
		ManifestPath:  result.ManifestPath,
		BuildID:       result.Manifest.BuildID,
		BuildStatus:   result.Manifest.BuildStatus,
		Assurance:     result.Manifest.Assurance,
		ArtifactCount: len(result.Manifest.Artifacts),
		Reused:        result.Reused,
	}
	if result.Reused {
		targetResult.Status = "reused"
	}
	if err != nil {
		targetResult.Status = "failed"
		targetResult.Error = err.Error()
	}
	return targetResult
}

func resolvePlanSysroot(profile model.TargetProfile, explicit, cacheDir string) (string, bool, error) {
	if value := strings.TrimSpace(explicit); value != "" {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return "", false, err
		}
		return absolute, false, nil
	}
	if value := strings.TrimSpace(os.Getenv(sysroot.EnvName(profile.ID))); value != "" {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return "", false, err
		}
		return absolute, false, nil
	}
	if !profile.RequiresSysroot || target.IsNative(profile) {
		return "", false, nil
	}
	manager := sysroot.New(sysroot.Options{CacheDir: cacheDir})
	if resolution, found, err := manager.Lookup(profile); err != nil {
		return "", false, err
	} else if found {
		return resolution.Path, true, nil
	}
	_, automatic := manager.Provider(profile)
	return "", automatic, nil
}

func summarize(results []TargetResult) Summary {
	summary := Summary{Total: len(results)}
	for _, result := range results {
		switch result.Status {
		case "planned":
			summary.Planned++
		case "succeeded":
			summary.Succeeded++
		case "reused":
			summary.Reused++
		case "failed":
			summary.Failed++
		case "canceled":
			summary.Canceled++
		case "blocked":
			summary.Blocked++
		}
	}
	return summary
}

func short(value string, length int) string {
	if len(value) <= length {
		return value
	}
	return value[:length]
}

type sharedProgress struct {
	destination io.Writer
	mu          sync.Mutex
}

func newSharedProgress(destination io.Writer) *sharedProgress {
	return &sharedProgress{destination: destination}
}

func (progress *sharedProgress) printf(format string, args ...any) {
	if progress == nil || progress.destination == nil {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	_, _ = fmt.Fprintf(progress.destination, format, args...)
}

func (progress *sharedProgress) writer(targetID string) io.Writer {
	if progress == nil || progress.destination == nil {
		return nil
	}
	return &prefixWriter{shared: progress, prefix: "[" + targetID + "] ", lineStart: true}
}

type prefixWriter struct {
	shared    *sharedProgress
	prefix    string
	lineStart bool
}

func (writer *prefixWriter) Write(data []byte) (int, error) {
	writer.shared.mu.Lock()
	defer writer.shared.mu.Unlock()
	var output strings.Builder
	for _, value := range data {
		if writer.lineStart {
			output.WriteString(writer.prefix)
			writer.lineStart = false
		}
		output.WriteByte(value)
		if value == '\n' {
			writer.lineStart = true
		}
	}
	_, err := io.WriteString(writer.shared.destination, output.String())
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

// SortedTargetIDs is useful for stable human-readable reporting and tests.
func SortedTargetIDs(results []TargetResult) []string {
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.Target.ID)
	}
	sort.Strings(ids)
	return ids
}
