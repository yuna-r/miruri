package verify

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuna-r/miruri/internal/artifactset"
	"github.com/yuna-r/miruri/internal/inspect"
	"github.com/yuna-r/miruri/internal/licenses"
	"github.com/yuna-r/miruri/internal/model"
	"github.com/yuna-r/miruri/internal/sbom"
	"github.com/yuna-r/miruri/internal/sysroot"
)

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Finding struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Path     string   `json:"path,omitempty"`
	Message  string   `json:"message"`
}

type Report struct {
	SchemaVersion string              `json:"schema_version"`
	VerifiedAt    time.Time           `json:"verified_at"`
	PackageDir    string              `json:"package_dir"`
	ManifestPath  string              `json:"manifest_path"`
	TargetID      string              `json:"target_id,omitempty"`
	BuildID       string              `json:"build_id,omitempty"`
	Valid         bool                `json:"valid"`
	CheckedFiles  int                 `json:"checked_files"`
	Findings      []Finding           `json:"findings"`
	Manifest      model.BuildManifest `json:"manifest"`
}

type Options struct {
	Strict bool
}

// ArtifactSet verifies a Miruri artifact directory without executing any
// packaged target code. A non-nil error means the set could not be read; a
// readable but invalid set is returned with Report.Valid=false.
func ArtifactSet(input string, options Options) (Report, error) {
	location, manifest, err := artifactset.LoadManifest(input)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		SchemaVersion: "miruri.verification.v1",
		VerifiedAt:    time.Now().UTC(),
		PackageDir:    location.PackageDir,
		ManifestPath:  location.ManifestPath,
		TargetID:      manifest.Target.ID,
		BuildID:       manifest.BuildID,
		Valid:         true,
		Findings:      []Finding{},
		Manifest:      manifest,
	}
	add := func(severity Severity, code, path, message string) {
		report.Findings = append(report.Findings, Finding{Severity: severity, Code: code, Path: path, Message: message})
		if severity == SeverityError {
			report.Valid = false
		}
	}

	if manifest.SchemaVersion != "miruri.manifest.v1" {
		add(SeverityError, "manifest.schema", artifactset.ManifestName, fmt.Sprintf("unsupported manifest schema %q", manifest.SchemaVersion))
	}
	if strings.TrimSpace(manifest.Target.ID) == "" {
		add(SeverityError, "manifest.target", artifactset.ManifestName, "manifest target ID is empty")
	}
	if strings.TrimSpace(manifest.ProjectName) == "" {
		add(SeverityError, "manifest.project", artifactset.ManifestName, "manifest project name is empty")
	}

	checkMetadata(location.PackageDir, manifest.AnalysisFile, "analysis", options.Strict, add)
	checkMetadata(location.PackageDir, manifest.PlanFile, "plan", options.Strict, add)
	checkMetadata(location.PackageDir, manifest.BuildLog, "build-log", options.Strict, add)
	if manifest.LicenseReportFile != "" {
		checkLicenseReport(location.PackageDir, manifest.LicenseReportFile, manifest, add)
	} else if options.Strict {
		add(SeverityError, "metadata.license-report.missing", "", "manifest does not reference a license report")
	} else {
		add(SeverityWarning, "metadata.license-report.missing", "", "manifest does not reference a license report")
	}
	if manifest.SBOMFile != "" {
		checkSPDX(location.PackageDir, manifest.SBOMFile, manifest, add)
	} else if options.Strict {
		add(SeverityError, "metadata.sbom.missing", "", "manifest does not reference an SPDX SBOM")
	} else {
		add(SeverityWarning, "metadata.sbom.missing", "", "manifest does not reference an SPDX SBOM")
	}

	checkAnalysisAndPlan(location.PackageDir, manifest, add)
	checkSysrootLock(location.PackageDir, manifest, add)
	checkPortedSource(location.PackageDir, manifest.PortedSourceDir, add)
	checkArtifactFiles(location.PackageDir, manifest, &report, add)
	checkSymlinks(location.PackageDir, add)
	checkIntegrity(location.PackageDir, manifest, options.Strict, &report, add)

	sort.SliceStable(report.Findings, func(i, j int) bool {
		if report.Findings[i].Severity != report.Findings[j].Severity {
			return severityRank(report.Findings[i].Severity) > severityRank(report.Findings[j].Severity)
		}
		if report.Findings[i].Path != report.Findings[j].Path {
			return report.Findings[i].Path < report.Findings[j].Path
		}
		return report.Findings[i].Code < report.Findings[j].Code
	})
	return report, nil
}

func checkPortedSource(packageDir, value string, add func(Severity, string, string, string)) {
	if strings.TrimSpace(value) == "" {
		return
	}
	path, err := artifactset.ResolvePath(packageDir, value)
	if err != nil {
		add(SeverityError, "metadata.ported-source.path", value, err.Error())
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		add(SeverityError, "metadata.ported-source.unreadable", value, err.Error())
		return
	}
	if !info.IsDir() {
		add(SeverityError, "metadata.ported-source.type", value, "ported source path is not a directory")
	}
}

func checkMetadata(packageDir, value, role string, strict bool, add func(Severity, string, string, string)) {
	if value == "" {
		severity := SeverityWarning
		if strict {
			severity = SeverityError
		}
		add(severity, "metadata."+role+".missing", "", "manifest does not reference "+role)
		return
	}
	path, err := artifactset.ResolvePath(packageDir, value)
	if err != nil {
		add(SeverityError, "metadata."+role+".path", value, err.Error())
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		add(SeverityError, "metadata."+role+".unreadable", artifactset.RelativePath(packageDir, path), err.Error())
		return
	}
	if !info.Mode().IsRegular() {
		add(SeverityError, "metadata."+role+".type", artifactset.RelativePath(packageDir, path), "metadata path is not a regular file")
	}
}

func checkLicenseReport(packageDir, value string, manifest model.BuildManifest, add func(Severity, string, string, string)) {
	checkMetadata(packageDir, value, "license-report", true, add)
	path, err := artifactset.ResolvePath(packageDir, value)
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var report licenses.Report
	if err := json.Unmarshal(data, &report); err != nil {
		add(SeverityError, "metadata.license-report.json", artifactset.RelativePath(packageDir, path), err.Error())
		return
	}
	relative := artifactset.RelativePath(packageDir, path)
	if report.SchemaVersion != "miruri.licenses.v1" {
		add(SeverityError, "metadata.license-report.schema", relative, fmt.Sprintf("expected miruri.licenses.v1, found %q", report.SchemaVersion))
	}
	if report.ProjectName != manifest.ProjectName {
		add(SeverityError, "metadata.license-report.project", relative, fmt.Sprintf("project %q does not match manifest %q", report.ProjectName, manifest.ProjectName))
	}
	if manifest.ProjectDigest != "" && report.ProjectDigest != manifest.ProjectDigest {
		add(SeverityError, "metadata.license-report.digest", relative, "project digest does not match manifest")
	}
	if strings.TrimSpace(report.PrimaryExpression) == "" {
		add(SeverityError, "metadata.license-report.primary", relative, "primary_expression is empty")
	}
	seen := map[string]bool{}
	for _, evidence := range report.Evidence {
		pathValue := filepath.ToSlash(strings.TrimSpace(evidence.Path))
		if !validLogicalRelativePath(pathValue) {
			add(SeverityError, "metadata.license-report.evidence-path", pathValue, "license evidence path must be package-independent and relative")
			continue
		}
		if seen[pathValue] {
			add(SeverityError, "metadata.license-report.evidence-duplicate", pathValue, "license evidence path appears more than once")
		}
		seen[pathValue] = true
		if len(evidence.SHA256) != 64 {
			add(SeverityError, "metadata.license-report.evidence-sha256", pathValue, "license evidence SHA-256 must contain 64 hexadecimal characters")
		} else if _, err := hex.DecodeString(evidence.SHA256); err != nil {
			add(SeverityError, "metadata.license-report.evidence-sha256", pathValue, err.Error())
		}
	}
}

func checkSPDX(packageDir, value string, manifest model.BuildManifest, add func(Severity, string, string, string)) {
	checkMetadata(packageDir, value, "sbom", true, add)
	path, err := artifactset.ResolvePath(packageDir, value)
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var document sbom.Document
	if err := json.Unmarshal(data, &document); err != nil {
		add(SeverityError, "metadata.sbom.json", artifactset.RelativePath(packageDir, path), err.Error())
		return
	}
	relative := artifactset.RelativePath(packageDir, path)
	if document.SPDXVersion != "SPDX-2.3" || document.SPDXID != "SPDXRef-DOCUMENT" || document.DataLicense != "CC0-1.0" {
		add(SeverityError, "metadata.sbom.schema", relative, fmt.Sprintf("unexpected SPDX envelope %q / %q / %q", document.SPDXVersion, document.SPDXID, document.DataLicense))
	}
	expectedName := manifest.ProjectName + "-" + manifest.Target.ID
	if document.Name != expectedName {
		add(SeverityError, "metadata.sbom.name", relative, fmt.Sprintf("document name %q does not match %q", document.Name, expectedName))
	}
	if manifest.BuildID != "" {
		expectedNamespace := "https://miruri.dev/spdx/" + manifest.BuildID
		if document.DocumentNamespace != expectedNamespace {
			add(SeverityError, "metadata.sbom.namespace", relative, fmt.Sprintf("document namespace %q does not match %q", document.DocumentNamespace, expectedNamespace))
		}
	}

	var projectPackage *sbom.Package
	for index := range document.Packages {
		candidate := &document.Packages[index]
		if candidate.Name == manifest.ProjectName {
			if projectPackage != nil {
				add(SeverityError, "metadata.sbom.package-duplicate", relative, "project package appears more than once")
				continue
			}
			projectPackage = candidate
		}
	}
	if projectPackage == nil {
		add(SeverityError, "metadata.sbom.package-missing", relative, "project package is missing")
		return
	}
	if projectPackage.FilesAnalyzed {
		add(SeverityError, "metadata.sbom.files-analyzed", relative, "partial Miruri file inventory must use filesAnalyzed=false")
	}
	if strings.HasPrefix(manifest.ProjectDigest, "sha256:") {
		want := strings.TrimPrefix(manifest.ProjectDigest, "sha256:")
		if !hasSPDXChecksum(projectPackage.Checksums, "SHA256", want) {
			add(SeverityError, "metadata.sbom.project-checksum", relative, "project package checksum does not match manifest project digest")
		}
	}
	if !hasSPDXRelationship(document.Relationships, "SPDXRef-DOCUMENT", "DESCRIBES", projectPackage.SPDXID) {
		add(SeverityError, "metadata.sbom.describes", relative, "document does not DESCRIBE the project package")
	}

	filesByName := map[string][]sbom.File{}
	for _, file := range document.Files {
		name := filepath.ToSlash(file.FileName)
		filesByName[name] = append(filesByName[name], file)
	}
	for _, artifact := range manifest.Artifacts {
		artifactPath := artifactset.StableArtifactPath(packageDir, artifact)
		matches := filesByName[artifactPath]
		if len(matches) == 0 {
			add(SeverityError, "metadata.sbom.artifact-missing", artifactPath, "packaged artifact is missing from SPDX file inventory")
			continue
		}
		if len(matches) > 1 {
			add(SeverityError, "metadata.sbom.artifact-duplicate", artifactPath, "packaged artifact appears more than once in SPDX file inventory")
			continue
		}
		file := matches[0]
		if !hasSPDXChecksum(file.Checksums, "SHA256", artifact.SHA256) {
			add(SeverityError, "metadata.sbom.artifact-checksum", artifactPath, "SPDX artifact checksum does not match manifest")
		}
		if !hasSPDXRelationship(document.Relationships, projectPackage.SPDXID, "CONTAINS", file.SPDXID) {
			add(SeverityError, "metadata.sbom.artifact-relationship", artifactPath, "project package does not CONTAIN the SPDX artifact entry")
		}
	}
}

func hasSPDXChecksum(values []sbom.Checksum, algorithm, checksum string) bool {
	for _, value := range values {
		if strings.EqualFold(value.Algorithm, algorithm) && strings.EqualFold(value.ChecksumValue, checksum) {
			return true
		}
	}
	return false
}

func hasSPDXRelationship(values []sbom.Relationship, left, kind, right string) bool {
	for _, value := range values {
		if value.SPDXElementID == left && value.RelationshipType == kind && value.RelatedSPDXElement == right {
			return true
		}
	}
	return false
}

func validLogicalRelativePath(value string) bool {
	if value == "" || strings.ContainsAny(value, "\r\n") || filepath.IsAbs(filepath.FromSlash(value)) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func checkAnalysisAndPlan(packageDir string, manifest model.BuildManifest, add func(Severity, string, string, string)) {
	if manifest.AnalysisFile != "" {
		path, err := artifactset.ResolvePath(packageDir, manifest.AnalysisFile)
		if err == nil {
			data, readErr := os.ReadFile(path)
			if readErr == nil {
				var analysis model.AnalysisReport
				if jsonErr := json.Unmarshal(data, &analysis); jsonErr != nil {
					add(SeverityError, "analysis.json", artifactset.RelativePath(packageDir, path), jsonErr.Error())
				} else {
					if analysis.SchemaVersion != "miruri.analysis.v1" {
						add(SeverityError, "analysis.schema", artifactset.RelativePath(packageDir, path), fmt.Sprintf("unsupported analysis schema %q", analysis.SchemaVersion))
					}
					if analysis.ProjectName != manifest.ProjectName {
						add(SeverityError, "analysis.project", artifactset.RelativePath(packageDir, path), fmt.Sprintf("project %q does not match manifest %q", analysis.ProjectName, manifest.ProjectName))
					}
					if manifest.ProjectDigest != "" && analysis.ProjectDigest != "" && analysis.ProjectDigest != manifest.ProjectDigest {
						add(SeverityError, "analysis.digest", artifactset.RelativePath(packageDir, path), "project digest does not match manifest")
					}
				}
			}
		}
	}
	if manifest.PlanFile != "" {
		path, err := artifactset.ResolvePath(packageDir, manifest.PlanFile)
		if err == nil {
			data, readErr := os.ReadFile(path)
			if readErr == nil {
				var plan model.PortingPlan
				if jsonErr := json.Unmarshal(data, &plan); jsonErr != nil {
					add(SeverityError, "plan.json", artifactset.RelativePath(packageDir, path), jsonErr.Error())
				} else {
					if plan.SchemaVersion != "miruri.plan.v1" {
						add(SeverityError, "plan.schema", artifactset.RelativePath(packageDir, path), fmt.Sprintf("unsupported plan schema %q", plan.SchemaVersion))
					}
					if plan.ProjectName != manifest.ProjectName {
						add(SeverityError, "plan.project", artifactset.RelativePath(packageDir, path), fmt.Sprintf("project %q does not match manifest %q", plan.ProjectName, manifest.ProjectName))
					}
					if plan.Target.ID != manifest.Target.ID {
						add(SeverityError, "plan.target", artifactset.RelativePath(packageDir, path), fmt.Sprintf("target %q does not match manifest %q", plan.Target.ID, manifest.Target.ID))
					}
				}
			}
		}
	}
}

func checkSysrootLock(packageDir string, manifest model.BuildManifest, add func(Severity, string, string, string)) {
	if manifest.Sysroot == nil || manifest.Sysroot.LockFile == "" {
		return
	}
	path, err := artifactset.ResolvePath(packageDir, manifest.Sysroot.LockFile)
	if err != nil {
		add(SeverityError, "sysroot.lock.path", manifest.Sysroot.LockFile, err.Error())
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		add(SeverityError, "sysroot.lock.read", artifactset.RelativePath(packageDir, path), err.Error())
		return
	}
	var lock sysroot.Lock
	if err := json.Unmarshal(data, &lock); err != nil {
		add(SeverityError, "sysroot.lock.json", artifactset.RelativePath(packageDir, path), err.Error())
		return
	}
	if lock.SchemaVersion != "miruri.sysroot-lock.v1" {
		add(SeverityError, "sysroot.lock.schema", artifactset.RelativePath(packageDir, path), fmt.Sprintf("unsupported sysroot lock schema %q", lock.SchemaVersion))
	}
	if lock.TargetID != manifest.Target.ID {
		add(SeverityError, "sysroot.lock.target", artifactset.RelativePath(packageDir, path), fmt.Sprintf("target %q does not match manifest %q", lock.TargetID, manifest.Target.ID))
	}
	if manifest.Sysroot.ManifestDigest != "" && lock.ManifestDigest != manifest.Sysroot.ManifestDigest {
		add(SeverityError, "sysroot.lock.digest", artifactset.RelativePath(packageDir, path), "manifest digest does not match sysroot provenance")
	}
}

func checkArtifactFiles(packageDir string, manifest model.BuildManifest, report *Report, add func(Severity, string, string, string)) {
	seen := map[string]bool{}
	for _, artifact := range manifest.Artifacts {
		value := artifact.PackagePath
		if value == "" {
			value = artifact.PackagedPath
		}
		path, err := artifactset.ResolvePath(packageDir, value)
		if err != nil {
			add(SeverityError, "artifact.path", value, err.Error())
			continue
		}
		relative := artifactset.RelativePath(packageDir, path)
		if seen[relative] {
			add(SeverityError, "artifact.duplicate", relative, "artifact path appears more than once in manifest")
			continue
		}
		seen[relative] = true
		digest, size, err := artifactset.HashFile(path)
		if err != nil {
			add(SeverityError, "artifact.read", relative, err.Error())
			continue
		}
		report.CheckedFiles++
		if digest != artifact.SHA256 {
			add(SeverityError, "artifact.sha256", relative, fmt.Sprintf("expected %s, found %s", artifact.SHA256, digest))
		}
		if size != artifact.Size {
			add(SeverityError, "artifact.size", relative, fmt.Sprintf("expected %d bytes, found %d", artifact.Size, size))
		}
		inspected, recognized, inspectErr := inspect.InspectFile(path, manifest.Target)
		if inspectErr != nil {
			add(SeverityError, "artifact.inspect", relative, inspectErr.Error())
			continue
		}
		if !recognized {
			if artifact.Format == "tar" && (artifact.Kind == "install-tree" || artifact.Kind == "app-bundle") {
				continue
			}
			add(SeverityError, "artifact.format", relative, fmt.Sprintf("file is not recognized as declared format %q", artifact.Format))
			continue
		}
		if inspected.Format != artifact.Format {
			add(SeverityError, "artifact.format", relative, fmt.Sprintf("expected %s, inspected %s", artifact.Format, inspected.Format))
		}
		if inspected.Architecture != artifact.Architecture {
			add(SeverityError, "artifact.architecture", relative, fmt.Sprintf("expected %s, inspected %s", artifact.Architecture, inspected.Architecture))
		}
		if inspected.Kind != artifact.Kind {
			add(SeverityError, "artifact.kind", relative, fmt.Sprintf("expected %s, inspected %s", artifact.Kind, inspected.Kind))
		}
		if inspected.ArchitectureOK != artifact.ArchitectureOK {
			add(SeverityError, "artifact.architecture-ok", relative, fmt.Sprintf("manifest=%t, inspected=%t", artifact.ArchitectureOK, inspected.ArchitectureOK))
		}
		if !sameStrings(inspected.Dependencies, artifact.Dependencies) {
			add(SeverityError, "artifact.dependencies", relative, fmt.Sprintf("manifest=%v, inspected=%v", sortedStrings(artifact.Dependencies), sortedStrings(inspected.Dependencies)))
		}
	}
}

func sameStrings(left, right []string) bool {
	leftSorted := sortedStrings(left)
	rightSorted := sortedStrings(right)
	if len(leftSorted) != len(rightSorted) {
		return false
	}
	for index := range leftSorted {
		if leftSorted[index] != rightSorted[index] {
			return false
		}
	}
	return true
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func checkIntegrity(packageDir string, manifest model.BuildManifest, strict bool, report *Report, add func(Severity, string, string, string)) {
	if manifest.IntegrityFile == "" {
		severity := SeverityWarning
		if strict {
			severity = SeverityError
		}
		add(severity, "integrity.missing", "", "manifest does not reference an integrity index")
		return
	}
	entries, err := artifactset.ReadIntegrity(packageDir, manifest.IntegrityFile)
	if err != nil {
		add(SeverityError, "integrity.read", manifest.IntegrityFile, err.Error())
		return
	}
	indexed := map[string]bool{}
	for _, entry := range entries {
		path, err := artifactset.ResolvePath(packageDir, entry.Path)
		if err != nil {
			add(SeverityError, "integrity.path", entry.Path, err.Error())
			continue
		}
		digest, _, err := artifactset.HashFile(path)
		if err != nil {
			add(SeverityError, "integrity.file", entry.Path, err.Error())
			continue
		}
		report.CheckedFiles++
		indexed[entry.Path] = true
		if digest != entry.SHA256 {
			add(SeverityError, "integrity.sha256", entry.Path, fmt.Sprintf("expected %s, found %s", entry.SHA256, digest))
		}
	}
	actual, err := artifactset.ActualFiles(packageDir)
	if err != nil {
		add(SeverityError, "integrity.walk", "", err.Error())
		return
	}
	integrityPath := filepath.ToSlash(manifest.IntegrityFile)
	for _, path := range actual {
		if path == integrityPath {
			continue
		}
		if !indexed[path] {
			severity := SeverityWarning
			if strict {
				severity = SeverityError
			}
			add(severity, "integrity.unindexed", path, "regular file is not listed in the integrity index")
		}
	}
}

func checkSymlinks(packageDir string, add func(Severity, string, string, string)) {
	_ = filepath.WalkDir(packageDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			add(SeverityError, "artifact-set.walk", artifactset.RelativePath(packageDir, path), walkErr.Error())
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			add(SeverityError, "artifact-set.symlink", artifactset.RelativePath(packageDir, path), "artifact sets must not contain symlinks")
		}
		return nil
	})
}

func severityRank(value Severity) int {
	switch value {
	case SeverityError:
		return 3
	case SeverityWarning:
		return 2
	default:
		return 1
	}
}
