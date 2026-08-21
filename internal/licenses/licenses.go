package licenses

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yuna-r/miruri/internal/fsutil"
)

const maxLicenseBytes int64 = 4 << 20

var spdxIdentifierPattern = regexp.MustCompile(`(?im)SPDX-License-Identifier:\s*([^\r\n*]+)`)

type Options struct {
	ExcludePaths []string
}

type Evidence struct {
	Path        string   `json:"path"`
	SHA256      string   `json:"sha256"`
	Size        int64    `json:"size"`
	Root        bool     `json:"root"`
	Expressions []string `json:"expressions,omitempty"`
	Confidence  string   `json:"confidence"`
	Detection   string   `json:"detection"`
}

type Report struct {
	SchemaVersion     string     `json:"schema_version"`
	GeneratedAt       time.Time  `json:"generated_at"`
	ProjectName       string     `json:"project_name"`
	ProjectDigest     string     `json:"project_digest,omitempty"`
	PrimaryExpression string     `json:"primary_expression"`
	Evidence          []Evidence `json:"evidence"`
	Warnings          []string   `json:"warnings,omitempty"`
}

// Scan records license declarations and recognizable full-text licenses
// without treating heuristic matches as legal conclusions. Generated and VCS
// directories are excluded consistently with Miruri project analysis.
func Scan(root, projectName, projectDigest string) (Report, error) {
	return ScanWithOptions(root, projectName, projectDigest, Options{})
}

func ScanWithOptions(root, projectName, projectDigest string, options Options) (Report, error) {
	rootAbs, err := fsutil.CanonicalPath(root)
	if err != nil {
		return Report{}, fmt.Errorf("resolve project path: %w", err)
	}
	excludedPaths, err := normalizeExcludedPaths(rootAbs, options.ExcludePaths)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		SchemaVersion:     "miruri.licenses.v1",
		GeneratedAt:       time.Now().UTC(),
		ProjectName:       projectName,
		ProjectDigest:     projectDigest,
		PrimaryExpression: "NOASSERTION",
		Evidence:          []Evidence{},
	}
	err = filepath.WalkDir(rootAbs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("cannot inspect %s: %v", path, walkErr))
			return nil
		}
		rel, relErr := filepath.Rel(rootAbs, path)
		if relErr != nil {
			return relErr
		}
		if rel != "." && excludedPaths[filepath.Clean(path)] {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if rel != "." && skipDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !looksLikeLicenseFile(entry.Name()) {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("cannot stat license candidate %s: %v", rel, infoErr))
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > maxLicenseBytes {
			report.Warnings = append(report.Warnings, fmt.Sprintf("license candidate %s exceeds %d bytes and was skipped", filepath.ToSlash(rel), maxLicenseBytes))
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("cannot read license candidate %s: %v", rel, readErr))
			return nil
		}
		digest := sha256.Sum256(data)
		expressions, confidence, detection := detectExpressions(string(data))
		report.Evidence = append(report.Evidence, Evidence{
			Path:        filepath.ToSlash(rel),
			SHA256:      hex.EncodeToString(digest[:]),
			Size:        info.Size(),
			Root:        filepath.Dir(rel) == ".",
			Expressions: expressions,
			Confidence:  confidence,
			Detection:   detection,
		})
		return nil
	})
	if err != nil {
		return Report{}, fmt.Errorf("scan license evidence: %w", err)
	}
	sort.Slice(report.Evidence, func(i, j int) bool { return report.Evidence[i].Path < report.Evidence[j].Path })

	rootExpressions := map[string]bool{}
	for _, evidence := range report.Evidence {
		if !evidence.Root || len(evidence.Expressions) != 1 {
			continue
		}
		rootExpressions[evidence.Expressions[0]] = true
	}
	if len(rootExpressions) == 1 {
		for expression := range rootExpressions {
			report.PrimaryExpression = expression
		}
	}
	return report, nil
}

func detectExpressions(text string) ([]string, string, string) {
	seen := map[string]bool{}
	var expressions []string
	for _, match := range spdxIdentifierPattern.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		expression := strings.TrimSpace(match[1])
		expression = strings.TrimSuffix(expression, "-->")
		expression = strings.TrimSpace(expression)
		if expression != "" && !seen[expression] {
			seen[expression] = true
			expressions = append(expressions, expression)
		}
	}
	if len(expressions) > 0 {
		sort.Strings(expressions)
		return expressions, "declared", "SPDX-License-Identifier"
	}

	lower := strings.ToLower(text)
	type signature struct {
		id        string
		fragments []string
	}
	signatures := []signature{
		{"MIT", []string{"permission is hereby granted, free of charge, to any person obtaining a copy", "the software is provided \"as is\""}},
		{"Apache-2.0", []string{"apache license", "version 2.0, january 2004"}},
		{"MPL-2.0", []string{"mozilla public license version 2.0"}},
		{"GPL-3.0-only", []string{"gnu general public license", "version 3, 29 june 2007"}},
		{"GPL-2.0-only", []string{"gnu general public license", "version 2, june 1991"}},
		{"LGPL-3.0-only", []string{"gnu lesser general public license", "version 3, 29 june 2007"}},
		{"LGPL-2.1-only", []string{"gnu lesser general public license", "version 2.1, february 1999"}},
		{"BSD-3-Clause", []string{"redistribution and use in source and binary forms", "neither the name of"}},
		{"ISC", []string{"permission to use, copy, modify, and/or distribute this software for any purpose with or without fee"}},
		{"Zlib", []string{"this software is provided 'as-is', without any express or implied warranty", "the origin of this software must not be misrepresented"}},
		{"BSL-1.0", []string{"boost software license - version 1.0"}},
		{"Unlicense", []string{"this is free and unencumbered software released into the public domain"}},
		{"CC0-1.0", []string{"cc0 1.0 universal", "no copyright"}},
	}
	for _, candidate := range signatures {
		matched := true
		for _, fragment := range candidate.fragments {
			if !strings.Contains(lower, fragment) {
				matched = false
				break
			}
		}
		if matched {
			return []string{candidate.id}, "text-match", "full-text signature"
		}
	}
	return nil, "unknown", "no recognized declaration"
}

func looksLikeLicenseFile(name string) bool {
	upper := strings.ToUpper(name)
	for _, prefix := range []string{"LICENSE", "LICENCE", "COPYING", "NOTICE", "COPYRIGHT", "UNLICENSE"} {
		if upper == prefix || strings.HasPrefix(upper, prefix+".") || strings.HasPrefix(upper, prefix+"-") || strings.HasPrefix(upper, prefix+"_") {
			return true
		}
	}
	return false
}

func normalizeExcludedPaths(root string, values []string) (map[string]bool, error) {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		candidate := filepath.FromSlash(value)
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(root, candidate)
		}
		absolute, err := fsutil.CanonicalPath(candidate)
		if err != nil {
			return nil, fmt.Errorf("resolve license exclusion %q: %w", value, err)
		}
		relative, err := filepath.Rel(root, absolute)
		if err != nil {
			return nil, fmt.Errorf("relativize license exclusion %q: %w", value, err)
		}
		if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
			continue
		}
		result[absolute] = true
	}
	return result, nil
}

func skipDirectory(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", ".codex", ".miruri", ".miruri-staging", "dist", "build", "out", "node_modules", "target", "__pycache__":
		return true
	default:
		return false
	}
}
