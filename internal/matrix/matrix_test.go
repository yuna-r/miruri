package matrix

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yuna-r/miruri/internal/builder"
	"github.com/yuna-r/miruri/internal/licenses"
	"github.com/yuna-r/miruri/internal/model"
	"github.com/yuna-r/miruri/internal/target"
)

func TestPlanOnlyMatrixPreservesTargetOrderAndWritesReport(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "CMakeLists.txt"), []byte("cmake_minimum_required(VERSION 3.16)\nproject(matrix C)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	arm, err := target.Resolve("linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	report, err := Run(context.Background(), Config{
		ProjectDir: project,
		Targets:    []model.TargetProfile{host, arm},
		Jobs:       2,
		PlanOnly:   true,
		OutDir:     out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 2 || report.Results[0].Target.ID != host.ID || report.Results[1].Target.ID != arm.ID {
		t.Fatalf("target order changed: %+v", report.Results)
	}
	if report.Summary.Planned != 2 || report.Summary.Failed != 0 {
		t.Fatalf("unexpected summary: %+v", report.Summary)
	}
	if _, err := os.Stat(filepath.Join(out, "matrix.json")); err != nil {
		t.Fatalf("matrix report was not written: %v", err)
	}
}

func TestPlanOnlyMatrixIgnoresCustomInProjectOutputs(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "CMakeLists.txt"), []byte("cmake_minimum_required(VERSION 3.16)\nproject(matrix_identity C)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(project, "release-matrix")
	reportPath := filepath.Join(project, "reports", "latest.json")
	config := Config{
		ProjectDir: project,
		Targets:    []model.TargetProfile{host},
		Jobs:       1,
		PlanOnly:   true,
		OutDir:     outDir,
		ReportPath: reportPath,
	}
	first, err := Run(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "generated.c"), []byte("#include <cuda_runtime.h>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Run(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProjectDigest != second.ProjectDigest {
		t.Fatalf("matrix outputs changed project identity: %s != %s", first.ProjectDigest, second.ProjectDigest)
	}
	if first.Results[0].Status != "planned" || second.Results[0].Status != "planned" {
		t.Fatalf("unexpected matrix statuses: first=%+v second=%+v", first.Results, second.Results)
	}
}

func TestBuildMatrixPropagatesReportExclusionIntoArtifactMetadata(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "CMakeLists.txt"), []byte("cmake_minimum_required(VERSION 3.16)\nproject(matrix_exclusion C)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(project, "generated", "LICENSE")
	if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, []byte("SPDX-License-Identifier: Apache-2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(project, "matrix-artifacts")
	report, err := Run(context.Background(), Config{
		ProjectDir: project,
		Targets:    []model.TargetProfile{host},
		Jobs:       1,
		OutDir:     outDir,
		ReportPath: reportPath,
		Build: builder.Config{
			DryRun:  true,
			Version: "test",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 || report.Results[0].Status != "succeeded" {
		t.Fatalf("unexpected matrix result: %+v", report.Results)
	}
	data, err := os.ReadFile(filepath.Join(report.Results[0].PackageDir, "licenses.json"))
	if err != nil {
		t.Fatal(err)
	}
	var licenseReport licenses.Report
	if err := json.Unmarshal(data, &licenseReport); err != nil {
		t.Fatal(err)
	}
	if len(licenseReport.Evidence) != 0 || licenseReport.PrimaryExpression != "NOASSERTION" {
		t.Fatalf("matrix report path leaked into license evidence: %+v", licenseReport)
	}
}
