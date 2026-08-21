package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuna-r/miruri/internal/artifactset"
	"github.com/yuna-r/miruri/internal/fsutil"
	artifactinspect "github.com/yuna-r/miruri/internal/inspect"
	"github.com/yuna-r/miruri/internal/licenses"
	"github.com/yuna-r/miruri/internal/model"
	"github.com/yuna-r/miruri/internal/sbom"
	"github.com/yuna-r/miruri/internal/target"
)

func TestArtifactSetDetectsArtifactMutation(t *testing.T) {
	root := t.TempDir()
	artifactPath := filepath.Join(root, "artifacts", "payload.tar")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("portable payload")
	if err := os.WriteFile(artifactPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	analysis := model.AnalysisReport{SchemaVersion: "miruri.analysis.v1", GeneratedAt: time.Now(), ProjectName: "demo", ProjectDigest: "sha256:project", Languages: map[string]int{}, Graph: model.ProjectGraph{}}
	plan := model.PortingPlan{SchemaVersion: "miruri.plan.v1", GeneratedAt: time.Now(), ProjectName: "demo", Target: model.TargetProfile{ID: "test", Arch: "portable"}, Status: "ready", Items: []model.PlanItem{}, Environment: []model.EnvironmentRequirement{}}
	if err := fsutil.WriteJSON(filepath.Join(root, "analysis.json"), analysis); err != nil {
		t.Fatal(err)
	}
	if err := fsutil.WriteJSON(filepath.Join(root, "plan.json"), plan); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "build.log"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := model.BuildManifest{
		SchemaVersion:     "miruri.manifest.v1",
		GeneratedAt:       time.Now(),
		MiruriVersion:     "test",
		ProjectName:       "demo",
		ProjectDigest:     "sha256:project",
		Target:            plan.Target,
		BuildSystem:       model.BuildSystemCMake,
		Assurance:         model.AssurancePackaged,
		Artifacts:         []model.ArtifactInfo{{PackagedPath: artifactPath, PackagePath: "artifacts/payload.tar", Format: "tar", Architecture: "portable", Kind: "install-tree", Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]), ArchitectureOK: true}},
		BuildLog:          "build.log",
		AnalysisFile:      "analysis.json",
		PlanFile:          "plan.json",
		LicenseReportFile: "licenses.json",
		SBOMFile:          "sbom.spdx.json",
		IntegrityFile:     artifactset.IntegrityName,
	}
	writeLicenseAndSBOM(t, root, manifest)
	if err := fsutil.WriteJSON(filepath.Join(root, artifactset.ManifestName), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := artifactset.WriteIntegrity(root); err != nil {
		t.Fatal(err)
	}
	report, err := ArtifactSet(root, Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Valid {
		t.Fatalf("valid artifact set rejected: %+v", report.Findings)
	}

	licensePath := filepath.Join(root, "licenses.json")
	licenseData, err := os.ReadFile(licensePath)
	if err != nil {
		t.Fatal(err)
	}
	var licenseReport licenses.Report
	if err := json.Unmarshal(licenseData, &licenseReport); err != nil {
		t.Fatal(err)
	}
	licenseReport.ProjectDigest = "sha256:different"
	if err := fsutil.WriteJSON(licensePath, licenseReport); err != nil {
		t.Fatal(err)
	}
	if _, err := artifactset.WriteIntegrity(root); err != nil {
		t.Fatal(err)
	}
	report, err = ArtifactSet(root, Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid || !containsFinding(report.Findings, "metadata.license-report.digest") {
		t.Fatalf("license identity mismatch was not detected: %+v", report.Findings)
	}
	licenseReport.ProjectDigest = manifest.ProjectDigest
	if err := fsutil.WriteJSON(licensePath, licenseReport); err != nil {
		t.Fatal(err)
	}

	sbomPath := filepath.Join(root, "sbom.spdx.json")
	sbomData, err := os.ReadFile(sbomPath)
	if err != nil {
		t.Fatal(err)
	}
	var document sbom.Document
	if err := json.Unmarshal(sbomData, &document); err != nil {
		t.Fatal(err)
	}
	for index := range document.Files {
		if document.Files[index].FileName == "artifacts/payload.tar" {
			document.Files[index].Checksums[0].ChecksumValue = strings.Repeat("f", 64)
		}
	}
	if err := fsutil.WriteJSON(sbomPath, document); err != nil {
		t.Fatal(err)
	}
	if _, err := artifactset.WriteIntegrity(root); err != nil {
		t.Fatal(err)
	}
	report, err = ArtifactSet(root, Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid || !containsFinding(report.Findings, "metadata.sbom.artifact-checksum") {
		t.Fatalf("SPDX artifact mismatch was not detected: %+v", report.Findings)
	}
	writeLicenseAndSBOM(t, root, manifest)
	if _, err := artifactset.WriteIntegrity(root); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(artifactPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err = ArtifactSet(root, Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid {
		t.Fatal("mutated artifact set was accepted")
	}
}

func TestArtifactSetRejectsEscapingArtifactPath(t *testing.T) {
	root := t.TempDir()
	manifest := model.BuildManifest{
		SchemaVersion: "miruri.manifest.v1",
		GeneratedAt:   time.Now(),
		MiruriVersion: "test",
		ProjectName:   "demo",
		Target:        model.TargetProfile{ID: "test"},
		BuildSystem:   model.BuildSystemCMake,
		Assurance:     model.AssuranceGenerated,
		Artifacts:     []model.ArtifactInfo{{PackagedPath: "../outside", PackagePath: "../outside", SHA256: strings.Repeat("0", 64)}},
	}
	if err := fsutil.WriteJSON(filepath.Join(root, artifactset.ManifestName), manifest); err != nil {
		t.Fatal(err)
	}
	report, err := ArtifactSet(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid {
		t.Fatal("escaping artifact path was accepted")
	}
}

func TestArtifactSetDetectsDependencyMetadataMismatch(t *testing.T) {
	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	artifact, recognized, err := artifactinspect.InspectFile(executable, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !recognized {
		t.Skip("test executable is not a supported artifact format")
	}

	root := t.TempDir()
	artifactPath := filepath.Join(root, "artifacts", "verify-test")
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsutil.CopyFile(executable, artifactPath, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	artifact.SourcePath = executable
	artifact.PackagedPath = artifactPath
	artifact.PackagePath = "artifacts/verify-test"
	artifact.Dependencies = append(append([]string(nil), artifact.Dependencies...), "miruri-fake-dependency")

	analysis := model.AnalysisReport{
		SchemaVersion: "miruri.analysis.v1",
		GeneratedAt:   time.Now(),
		ProjectName:   "demo",
		ProjectDigest: "sha256:project",
		Languages:     map[string]int{},
		Requirements:  []model.CapabilityRequirement{},
		Graph:         model.ProjectGraph{Nodes: []model.GraphNode{}, Edges: []model.GraphEdge{}},
	}
	plan := model.PortingPlan{
		SchemaVersion: "miruri.plan.v1",
		GeneratedAt:   time.Now(),
		ProjectName:   "demo",
		Target:        profile,
		Status:        "ready",
		Items:         []model.PlanItem{},
		Environment:   []model.EnvironmentRequirement{},
	}
	if err := fsutil.WriteJSON(filepath.Join(root, "analysis.json"), analysis); err != nil {
		t.Fatal(err)
	}
	if err := fsutil.WriteJSON(filepath.Join(root, "plan.json"), plan); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "build.log"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := model.BuildManifest{
		SchemaVersion:     "miruri.manifest.v1",
		GeneratedAt:       time.Now(),
		MiruriVersion:     "test",
		ProjectName:       "demo",
		ProjectDigest:     "sha256:project",
		Target:            profile,
		BuildSystem:       model.BuildSystemCMake,
		Assurance:         model.AssuranceStaticValidated,
		Artifacts:         []model.ArtifactInfo{artifact},
		BuildLog:          "build.log",
		AnalysisFile:      "analysis.json",
		PlanFile:          "plan.json",
		LicenseReportFile: "licenses.json",
		SBOMFile:          "sbom.spdx.json",
		IntegrityFile:     artifactset.IntegrityName,
	}
	writeLicenseAndSBOM(t, root, manifest)
	if err := fsutil.WriteJSON(filepath.Join(root, artifactset.ManifestName), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := artifactset.WriteIntegrity(root); err != nil {
		t.Fatal(err)
	}

	report, err := ArtifactSet(root, Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid {
		t.Fatal("dependency metadata mismatch was accepted")
	}
	for _, finding := range report.Findings {
		if finding.Code == "artifact.dependencies" {
			return
		}
	}
	t.Fatalf("dependency mismatch finding missing: %+v", report.Findings)
}

func containsFinding(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func writeLicenseAndSBOM(t *testing.T, root string, manifest model.BuildManifest) {
	t.Helper()
	report := licenses.Report{
		SchemaVersion:     "miruri.licenses.v1",
		GeneratedAt:       time.Now(),
		ProjectName:       manifest.ProjectName,
		ProjectDigest:     manifest.ProjectDigest,
		PrimaryExpression: "NOASSERTION",
		Evidence:          []licenses.Evidence{},
	}
	if err := fsutil.WriteJSON(filepath.Join(root, manifest.LicenseReportFile), report); err != nil {
		t.Fatal(err)
	}
	document := sbom.Generate(manifest, report, manifest.MiruriVersion)
	if err := fsutil.WriteJSON(filepath.Join(root, manifest.SBOMFile), document); err != nil {
		t.Fatal(err)
	}
}
