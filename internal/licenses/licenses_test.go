package licenses

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanPrefersRootDeclaredLicense(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte("SPDX-License-Identifier: MIT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "vendor", "dep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vendor", "dep", "COPYING"), []byte("SPDX-License-Identifier: Apache-2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(root, "demo", "sha256:demo")
	if err != nil {
		t.Fatal(err)
	}
	if report.PrimaryExpression != "MIT" {
		t.Fatalf("primary expression = %q", report.PrimaryExpression)
	}
	if len(report.Evidence) != 2 {
		t.Fatalf("evidence = %#v", report.Evidence)
	}
}

func TestScanRecognizesMITFullText(t *testing.T) {
	root := t.TempDir()
	text := `MIT License

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files.
THE SOFTWARE IS PROVIDED "AS IS".
`
	if err := os.WriteFile(filepath.Join(root, "LICENSE.txt"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(root, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if report.PrimaryExpression != "MIT" || report.Evidence[0].Confidence != "text-match" {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestScanWithOptionsExcludesGeneratedLicenseEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte("SPDX-License-Identifier: MIT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(root, "custom-results")
	if err := os.MkdirAll(generated, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generated, "LICENSE.generated"), []byte("SPDX-License-Identifier: Apache-2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := ScanWithOptions(root, "demo", "sha256:demo", Options{ExcludePaths: []string{generated}})
	if err != nil {
		t.Fatal(err)
	}
	if report.PrimaryExpression != "MIT" || len(report.Evidence) != 1 || report.Evidence[0].Path != "LICENSE" {
		t.Fatalf("generated license evidence was not excluded: %+v", report)
	}
}
