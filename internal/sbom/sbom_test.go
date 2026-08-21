package sbom

import (
	"testing"

	"github.com/yuna-r/miruri/internal/licenses"
	"github.com/yuna-r/miruri/internal/model"
)

func TestGenerateUsesConservativeSPDXPackageSemantics(t *testing.T) {
	manifest := model.BuildManifest{
		ProjectName:   "demo",
		ProjectDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BuildID:       "build-id",
		Target:        model.TargetProfile{ID: "linux-x86_64"},
		Artifacts: []model.ArtifactInfo{{
			PackagePath: "artifacts/demo",
			Kind:        "executable",
			SHA256:      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		}},
	}
	licenseReport := licenses.Report{
		PrimaryExpression: "MIT",
		Evidence: []licenses.Evidence{{
			Path:        "LICENSE",
			SHA256:      "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			Expressions: []string{"MIT"},
		}},
	}
	document := Generate(manifest, licenseReport, "test")
	if len(document.Packages) != 1 {
		t.Fatalf("unexpected package count: %d", len(document.Packages))
	}
	if document.Packages[0].FilesAnalyzed {
		t.Fatal("partial file inventory must not claim filesAnalyzed=true")
	}
	if len(document.Files) != 2 {
		t.Fatalf("unexpected file count: %d", len(document.Files))
	}
	if len(document.Relationships) != 3 {
		t.Fatalf("unexpected relationship count: %+v", document.Relationships)
	}
	first := document.Relationships[0]
	if first.SPDXElementID != "SPDXRef-DOCUMENT" || first.RelationshipType != "DESCRIBES" || first.RelatedSPDXElement != document.Packages[0].SPDXID {
		t.Fatalf("document description relationship missing: %+v", first)
	}
	for _, relationship := range document.Relationships[1:] {
		if relationship.RelationshipType != "CONTAINS" {
			t.Fatalf("unexpected package relationship: %+v", relationship)
		}
	}
}
