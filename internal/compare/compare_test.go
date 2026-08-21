package compare

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yuna-r/miruri/internal/fsutil"
	"github.com/yuna-r/miruri/internal/model"
)

func TestArtifactSetsDetectsArtifactContentChange(t *testing.T) {
	left := writeFixtureSet(t, "aaa")
	right := writeFixtureSet(t, "bbb")
	report, err := ArtifactSets(left, right)
	if err != nil {
		t.Fatal(err)
	}
	if report.Equivalent || report.ArtifactEquivalent {
		t.Fatalf("changed artifact was considered equivalent: %+v", report)
	}
	if report.ArtifactSummary.Changed != 1 {
		t.Fatalf("unexpected artifact summary: %+v", report.ArtifactSummary)
	}
	found := false
	for _, difference := range report.Differences {
		if difference.Category == "artifact" && difference.Path == "artifacts/demo.sha256" {
			found = true
		}
	}
	if !found {
		t.Fatalf("artifact hash difference missing: %+v", report.Differences)
	}
}

func TestArtifactSetsEquivalentForSameManifestContent(t *testing.T) {
	left := writeFixtureSet(t, "aaa")
	right := writeFixtureSet(t, "aaa")
	report, err := ArtifactSets(left, right)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Equivalent || !report.ArtifactEquivalent {
		t.Fatalf("equivalent sets differ: %+v", report.Differences)
	}
}

func writeFixtureSet(t *testing.T, digest string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "demo"), []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := model.BuildManifest{
		SchemaVersion: "miruri.manifest.v1",
		MiruriVersion: "test",
		BuildID:       "same-build",
		BuildStatus:   "succeeded",
		ProjectName:   "fixture",
		ProjectDigest: "sha256:project",
		RequestDigest: "sha256:request",
		Target: model.TargetProfile{
			ID:           "linux-x86_64",
			OS:           "linux",
			Arch:         "x86_64",
			Triple:       "x86_64-linux-gnu",
			ObjectFormat: "elf",
		},
		BuildSystem: model.BuildSystemCMake,
		Assurance:   model.AssuranceStaticValidated,
		Artifacts: []model.ArtifactInfo{{
			PackagePath:    "artifacts/demo",
			PackagedPath:   filepath.Join(root, "artifacts", "demo"),
			Format:         "elf",
			Architecture:   "x86_64",
			Kind:           "executable",
			Size:           7,
			SHA256:         digest,
			ArchitectureOK: true,
		}},
	}
	if err := fsutil.WriteJSON(filepath.Join(root, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestArtifactSetsRejectMalformedReferencedMetadata(t *testing.T) {
	left := t.TempDir()
	right := writeFixtureSet(t, "aaa")
	manifest := model.BuildManifest{
		SchemaVersion: "miruri.manifest.v1",
		ProjectName:   "fixture",
		Target:        model.TargetProfile{ID: "linux-x86_64"},
		AnalysisFile:  "analysis.json",
		Artifacts:     []model.ArtifactInfo{},
	}
	if err := fsutil.WriteJSON(filepath.Join(left, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(left, "analysis.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ArtifactSets(left, right); err == nil {
		t.Fatal("malformed referenced metadata was silently ignored")
	}
}
