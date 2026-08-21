package sbom

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yuna-r/miruri/internal/licenses"
	"github.com/yuna-r/miruri/internal/model"
)

type Checksum struct {
	Algorithm     string `json:"algorithm"`
	ChecksumValue string `json:"checksumValue"`
}

type CreationInfo struct {
	Created  time.Time `json:"created"`
	Creators []string  `json:"creators"`
}

type Package struct {
	Name             string     `json:"name"`
	SPDXID           string     `json:"SPDXID"`
	DownloadLocation string     `json:"downloadLocation"`
	FilesAnalyzed    bool       `json:"filesAnalyzed"`
	LicenseConcluded string     `json:"licenseConcluded"`
	LicenseDeclared  string     `json:"licenseDeclared"`
	CopyrightText    string     `json:"copyrightText"`
	Checksums        []Checksum `json:"checksums,omitempty"`
}

type File struct {
	FileName           string     `json:"fileName"`
	SPDXID             string     `json:"SPDXID"`
	Checksums          []Checksum `json:"checksums"`
	FileTypes          []string   `json:"fileTypes,omitempty"`
	LicenseConcluded   string     `json:"licenseConcluded"`
	LicenseInfoInFiles []string   `json:"licenseInfoInFiles"`
	CopyrightText      string     `json:"copyrightText"`
}

type Relationship struct {
	SPDXElementID      string `json:"spdxElementId"`
	RelationshipType   string `json:"relationshipType"`
	RelatedSPDXElement string `json:"relatedSpdxElement"`
}

type Document struct {
	SPDXVersion       string         `json:"spdxVersion"`
	DataLicense       string         `json:"dataLicense"`
	SPDXID            string         `json:"SPDXID"`
	Name              string         `json:"name"`
	DocumentNamespace string         `json:"documentNamespace"`
	CreationInfo      CreationInfo   `json:"creationInfo"`
	Packages          []Package      `json:"packages"`
	Files             []File         `json:"files"`
	Relationships     []Relationship `json:"relationships"`
}

var nonIdentifier = regexp.MustCompile(`[^A-Za-z0-9.-]+`)

// Generate creates an SPDX 2.3 document for the package produced by Miruri.
// It intentionally describes known packaged artifacts and recorded source
// license evidence; it does not claim that unresolved source dependencies form
// a complete dependency closure.
func Generate(manifest model.BuildManifest, licenseReport licenses.Report, miruriVersion string) Document {
	buildID := manifest.BuildID
	if buildID == "" {
		buildID = strings.TrimPrefix(manifest.RequestDigest, "sha256:")
	}
	if buildID == "" {
		buildID = "unidentified"
	}
	packageID := "SPDXRef-Package-" + identifier(manifest.ProjectName)
	document := Document{
		SPDXVersion:       "SPDX-2.3",
		DataLicense:       "CC0-1.0",
		SPDXID:            "SPDXRef-DOCUMENT",
		Name:              fmt.Sprintf("%s-%s", manifest.ProjectName, manifest.Target.ID),
		DocumentNamespace: "https://miruri.dev/spdx/" + buildID,
		CreationInfo: CreationInfo{
			Created:  time.Now().UTC(),
			Creators: []string{"Tool: Miruri-" + emptyAs(miruriVersion, "dev")},
		},
		Packages:      []Package{},
		Files:         []File{},
		Relationships: []Relationship{},
	}
	declared := licenseReport.PrimaryExpression
	if declared == "" {
		declared = "NOASSERTION"
	}
	packageEntry := Package{
		Name:             manifest.ProjectName,
		SPDXID:           packageID,
		DownloadLocation: "NOASSERTION",
		FilesAnalyzed:    false,
		LicenseConcluded: "NOASSERTION",
		LicenseDeclared:  declared,
		CopyrightText:    "NOASSERTION",
	}
	if strings.HasPrefix(manifest.ProjectDigest, "sha256:") {
		packageEntry.Checksums = []Checksum{{Algorithm: "SHA256", ChecksumValue: strings.TrimPrefix(manifest.ProjectDigest, "sha256:")}}
	}
	document.Packages = append(document.Packages, packageEntry)
	document.Relationships = append(document.Relationships, Relationship{
		SPDXElementID:      "SPDXRef-DOCUMENT",
		RelationshipType:   "DESCRIBES",
		RelatedSPDXElement: packageID,
	})

	artifacts := append([]model.ArtifactInfo(nil), manifest.Artifacts...)
	sort.Slice(artifacts, func(i, j int) bool { return stableArtifactPath(artifacts[i]) < stableArtifactPath(artifacts[j]) })
	for index, artifact := range artifacts {
		path := stableArtifactPath(artifact)
		fileID := fmt.Sprintf("SPDXRef-Artifact-%03d-%s", index+1, identifier(filepath.Base(path)))
		entry := File{
			FileName:           path,
			SPDXID:             fileID,
			Checksums:          []Checksum{{Algorithm: "SHA256", ChecksumValue: artifact.SHA256}},
			FileTypes:          []string{spdxFileType(artifact)},
			LicenseConcluded:   "NOASSERTION",
			LicenseInfoInFiles: []string{"NOASSERTION"},
			CopyrightText:      "NOASSERTION",
		}
		document.Files = append(document.Files, entry)
		document.Relationships = append(document.Relationships, Relationship{
			SPDXElementID:      packageID,
			RelationshipType:   "CONTAINS",
			RelatedSPDXElement: fileID,
		})
	}
	for index, evidence := range licenseReport.Evidence {
		fileID := fmt.Sprintf("SPDXRef-LicenseEvidence-%03d-%s", index+1, identifier(filepath.Base(evidence.Path)))
		licenseInfo := evidence.Expressions
		if len(licenseInfo) == 0 {
			licenseInfo = []string{"NOASSERTION"}
		}
		document.Files = append(document.Files, File{
			FileName:           evidence.Path,
			SPDXID:             fileID,
			Checksums:          []Checksum{{Algorithm: "SHA256", ChecksumValue: evidence.SHA256}},
			FileTypes:          []string{"TEXT"},
			LicenseConcluded:   "NOASSERTION",
			LicenseInfoInFiles: append([]string(nil), licenseInfo...),
			CopyrightText:      "NOASSERTION",
		})
		document.Relationships = append(document.Relationships, Relationship{
			SPDXElementID:      packageID,
			RelationshipType:   "CONTAINS",
			RelatedSPDXElement: fileID,
		})
	}
	return document
}

func stableArtifactPath(artifact model.ArtifactInfo) string {
	if artifact.PackagePath != "" {
		return artifact.PackagePath
	}
	return filepath.ToSlash(artifact.PackagedPath)
}

func identifier(value string) string {
	value = nonIdentifier.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-.")
	if value == "" {
		return "item"
	}
	return value
}

func spdxFileType(artifact model.ArtifactInfo) string {
	switch artifact.Kind {
	case "executable", "shared-library", "object":
		return "BINARY"
	case "static-library", "install-tree", "app-bundle":
		return "ARCHIVE"
	default:
		return "OTHER"
	}
}

func emptyAs(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
