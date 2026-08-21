package compare

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yuna-r/miruri/internal/artifactset"
	"github.com/yuna-r/miruri/internal/licenses"
	"github.com/yuna-r/miruri/internal/model"
)

const SchemaVersion = "miruri.comparison.v1"

type ArtifactSet struct {
	PackageDir   string `json:"package_dir"`
	ManifestPath string `json:"manifest_path"`
	ProjectName  string `json:"project_name"`
	TargetID     string `json:"target_id"`
	BuildID      string `json:"build_id,omitempty"`
	BuildStatus  string `json:"build_status,omitempty"`
}

type Difference struct {
	Category string `json:"category"`
	Path     string `json:"path"`
	Left     string `json:"left,omitempty"`
	Right    string `json:"right,omitempty"`
}

type ArtifactSummary struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Changed int `json:"changed"`
	Same    int `json:"same"`
}

type Report struct {
	SchemaVersion      string          `json:"schema_version"`
	ComparedAt         time.Time       `json:"compared_at"`
	Left               ArtifactSet     `json:"left"`
	Right              ArtifactSet     `json:"right"`
	Equivalent         bool            `json:"equivalent"`
	ArtifactEquivalent bool            `json:"artifact_equivalent"`
	ArtifactSummary    ArtifactSummary `json:"artifact_summary"`
	Differences        []Difference    `json:"differences"`
}

type loadedSet struct {
	location artifactset.Location
	manifest model.BuildManifest
	analysis *model.AnalysisReport
	plan     *model.PortingPlan
	licenses *licenses.Report
}

func ArtifactSets(leftInput, rightInput string) (Report, error) {
	left, err := load(leftInput)
	if err != nil {
		return Report{}, fmt.Errorf("load left artifact set: %w", err)
	}
	right, err := load(rightInput)
	if err != nil {
		return Report{}, fmt.Errorf("load right artifact set: %w", err)
	}
	report := Report{
		SchemaVersion: SchemaVersion,
		ComparedAt:    time.Now().UTC(),
		Left:          describe(left),
		Right:         describe(right),
		Differences:   []Difference{},
	}

	add := func(category, path string, leftValue, rightValue any) {
		leftText := stringify(leftValue)
		rightText := stringify(rightValue)
		if leftText == rightText {
			return
		}
		report.Differences = append(report.Differences, Difference{Category: category, Path: path, Left: leftText, Right: rightText})
	}

	compareManifest(left.manifest, right.manifest, add)
	compareAnalysis(left.analysis, right.analysis, add)
	comparePlan(left.plan, right.plan, add)
	compareLicenses(left.licenses, right.licenses, add)
	compareArtifacts(left, right, &report, add)

	sort.SliceStable(report.Differences, func(i, j int) bool {
		if report.Differences[i].Category != report.Differences[j].Category {
			return report.Differences[i].Category < report.Differences[j].Category
		}
		return report.Differences[i].Path < report.Differences[j].Path
	})
	report.Equivalent = len(report.Differences) == 0
	report.ArtifactEquivalent = report.ArtifactSummary.Added == 0 && report.ArtifactSummary.Removed == 0 && report.ArtifactSummary.Changed == 0
	return report, nil
}

func load(input string) (loadedSet, error) {
	location, manifest, err := artifactset.LoadManifest(input)
	if err != nil {
		return loadedSet{}, err
	}
	loaded := loadedSet{location: location, manifest: manifest}
	if manifest.AnalysisFile != "" {
		var analysis model.AnalysisReport
		if err := readJSON(location.PackageDir, manifest.AnalysisFile, &analysis); err != nil {
			return loadedSet{}, fmt.Errorf("read analysis metadata: %w", err)
		}
		loaded.analysis = &analysis
	}
	if manifest.PlanFile != "" {
		var plan model.PortingPlan
		if err := readJSON(location.PackageDir, manifest.PlanFile, &plan); err != nil {
			return loadedSet{}, fmt.Errorf("read plan metadata: %w", err)
		}
		loaded.plan = &plan
	}
	if manifest.LicenseReportFile != "" {
		var report licenses.Report
		if err := readJSON(location.PackageDir, manifest.LicenseReportFile, &report); err != nil {
			return loadedSet{}, fmt.Errorf("read license metadata: %w", err)
		}
		loaded.licenses = &report
	}
	return loaded, nil
}

func readJSON(packageDir, value string, destination any) error {
	path, err := artifactset.ResolvePath(packageDir, value)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, destination)
}

func describe(set loadedSet) ArtifactSet {
	return ArtifactSet{
		PackageDir:   set.location.PackageDir,
		ManifestPath: set.location.ManifestPath,
		ProjectName:  set.manifest.ProjectName,
		TargetID:     set.manifest.Target.ID,
		BuildID:      set.manifest.BuildID,
		BuildStatus:  set.manifest.BuildStatus,
	}
}

func compareManifest(left, right model.BuildManifest, add func(string, string, any, any)) {
	add("identity", "project.name", left.ProjectName, right.ProjectName)
	add("identity", "project.digest", left.ProjectDigest, right.ProjectDigest)
	add("identity", "request.digest", left.RequestDigest, right.RequestDigest)
	add("identity", "build.id", left.BuildID, right.BuildID)
	add("build", "status", left.BuildStatus, right.BuildStatus)
	add("build", "miruri.version", left.MiruriVersion, right.MiruriVersion)
	add("build", "system", left.BuildSystem, right.BuildSystem)
	add("build", "assurance", left.Assurance, right.Assurance)
	add("target", "id", left.Target.ID, right.Target.ID)
	add("target", "os", left.Target.OS, right.Target.OS)
	add("target", "arch", left.Target.Arch, right.Target.Arch)
	add("target", "triple", left.Target.Triple, right.Target.Triple)
	add("target", "object_format", left.Target.ObjectFormat, right.Target.ObjectFormat)
	compareSysroot(left.Sysroot, right.Sysroot, add)
	compareToolchain(left.Toolchain, right.Toolchain, add)
}

func compareSysroot(left, right *model.SysrootProvenance, add func(string, string, any, any)) {
	if left == nil || right == nil {
		add("sysroot", "present", left != nil, right != nil)
		return
	}
	add("sysroot", "mode", left.Mode, right.Mode)
	add("sysroot", "target", left.TargetID, right.TargetID)
	add("sysroot", "provider", left.Provider, right.Provider)
	add("sysroot", "source", left.Source, right.Source)
	add("sysroot", "manifest_digest", left.ManifestDigest, right.ManifestDigest)
	add("sysroot", "platform", left.Platform, right.Platform)
}

func compareToolchain(left, right *model.ToolchainProvenance, add func(string, string, any, any)) {
	if left == nil || right == nil {
		add("toolchain", "present", left != nil, right != nil)
		return
	}
	add("toolchain", "c_compiler", left.CCompiler, right.CCompiler)
	add("toolchain", "cxx_compiler", left.CXXCompiler, right.CXXCompiler)
	add("toolchain", "archiver", left.Archiver, right.Archiver)
	add("toolchain", "ranlib", left.Ranlib, right.Ranlib)
	add("toolchain", "strip", left.Strip, right.Strip)
	add("toolchain", "linker", left.Linker, right.Linker)
	add("toolchain", "gcc_toolchain", left.GCCToolchain, right.GCCToolchain)
}

func compareAnalysis(left, right *model.AnalysisReport, add func(string, string, any, any)) {
	if left == nil || right == nil {
		add("capability", "analysis.present", left != nil, right != nil)
		return
	}
	leftRequirements := make(map[string]model.CapabilityRequirement, len(left.Requirements))
	rightRequirements := make(map[string]model.CapabilityRequirement, len(right.Requirements))
	for _, requirement := range left.Requirements {
		leftRequirements[requirement.ID] = requirement
	}
	for _, requirement := range right.Requirements {
		rightRequirements[requirement.ID] = requirement
	}
	for _, id := range unionKeys(leftRequirements, rightRequirements) {
		leftRequirement, leftOK := leftRequirements[id]
		rightRequirement, rightOK := rightRequirements[id]
		if !leftOK || !rightOK {
			add("capability", id+".present", leftOK, rightOK)
			continue
		}
		add("capability", id+".hard", leftRequirement.Hard, rightRequirement.Hard)
		add("capability", id+".domain", leftRequirement.Domain, rightRequirement.Domain)
		add("capability", id+".evidence_count", len(leftRequirement.Evidence), len(rightRequirement.Evidence))
	}
}

func comparePlan(left, right *model.PortingPlan, add func(string, string, any, any)) {
	if left == nil || right == nil {
		add("strategy", "plan.present", left != nil, right != nil)
		return
	}
	add("strategy", "plan.status", left.Status, right.Status)
	leftItems := make(map[string]model.PlanItem, len(left.Items))
	rightItems := make(map[string]model.PlanItem, len(right.Items))
	for _, item := range left.Items {
		leftItems[item.Requirement] = item
	}
	for _, item := range right.Items {
		rightItems[item.Requirement] = item
	}
	for _, id := range unionKeys(leftItems, rightItems) {
		leftItem, leftOK := leftItems[id]
		rightItem, rightOK := rightItems[id]
		if !leftOK || !rightOK {
			add("strategy", id+".present", leftOK, rightOK)
			continue
		}
		add("strategy", id+".kind", leftItem.Strategy, rightItem.Strategy)
		add("strategy", id+".provider", leftItem.Provider, rightItem.Provider)
		add("strategy", id+".blocking", leftItem.Blocking, rightItem.Blocking)
	}
}

func compareLicenses(left, right *licenses.Report, add func(string, string, any, any)) {
	if left == nil || right == nil {
		add("license", "report.present", left != nil, right != nil)
		return
	}
	add("license", "primary_expression", left.PrimaryExpression, right.PrimaryExpression)
	add("license", "root_evidence", rootLicenseExpressions(*left), rootLicenseExpressions(*right))
}

func rootLicenseExpressions(report licenses.Report) []string {
	var values []string
	for _, evidence := range report.Evidence {
		if !evidence.Root {
			continue
		}
		values = append(values, evidence.Path+"="+strings.Join(evidence.Expressions, " OR "))
	}
	sort.Strings(values)
	return values
}

func compareArtifacts(left, right loadedSet, report *Report, add func(string, string, any, any)) {
	leftArtifacts := make(map[string]model.ArtifactInfo, len(left.manifest.Artifacts))
	rightArtifacts := make(map[string]model.ArtifactInfo, len(right.manifest.Artifacts))
	for _, artifact := range left.manifest.Artifacts {
		leftArtifacts[artifactset.StableArtifactPath(left.location.PackageDir, artifact)] = artifact
	}
	for _, artifact := range right.manifest.Artifacts {
		rightArtifacts[artifactset.StableArtifactPath(right.location.PackageDir, artifact)] = artifact
	}
	for _, path := range unionKeys(leftArtifacts, rightArtifacts) {
		leftArtifact, leftOK := leftArtifacts[path]
		rightArtifact, rightOK := rightArtifacts[path]
		if !leftOK {
			report.ArtifactSummary.Added++
			add("artifact", path+".present", false, true)
			continue
		}
		if !rightOK {
			report.ArtifactSummary.Removed++
			add("artifact", path+".present", true, false)
			continue
		}
		changed := false
		check := func(field string, leftValue, rightValue any) {
			before := stringify(leftValue)
			after := stringify(rightValue)
			if before != after {
				changed = true
				add("artifact", path+"."+field, leftValue, rightValue)
			}
		}
		check("sha256", leftArtifact.SHA256, rightArtifact.SHA256)
		check("size", leftArtifact.Size, rightArtifact.Size)
		check("format", leftArtifact.Format, rightArtifact.Format)
		check("architecture", leftArtifact.Architecture, rightArtifact.Architecture)
		check("kind", leftArtifact.Kind, rightArtifact.Kind)
		check("architecture_ok", leftArtifact.ArchitectureOK, rightArtifact.ArchitectureOK)
		check("dependencies", sortedCopy(leftArtifact.Dependencies), sortedCopy(rightArtifact.Dependencies))
		if changed {
			report.ArtifactSummary.Changed++
		} else {
			report.ArtifactSummary.Same++
		}
	}
}

func unionKeys[T any](left, right map[string]T) []string {
	seen := make(map[string]bool, len(left)+len(right))
	for key := range left {
		seen[key] = true
	}
	for key := range right {
		seen[key] = true
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedCopy(values []string) []string {
	copyValues := append([]string(nil), values...)
	sort.Strings(copyValues)
	return copyValues
}

func stringify(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	}
	if reflect.ValueOf(value).Kind() == reflect.Slice {
		data, err := json.Marshal(value)
		if err == nil {
			return string(data)
		}
	}
	data, err := json.Marshal(value)
	if err == nil {
		return strings.TrimSpace(string(data))
	}
	return fmt.Sprint(value)
}

// RelativeManifestPath provides a stable path for human-readable callers.
func RelativeManifestPath(set ArtifactSet) string {
	if relative, err := filepath.Rel(set.PackageDir, set.ManifestPath); err == nil {
		return filepath.ToSlash(relative)
	}
	return set.ManifestPath
}
