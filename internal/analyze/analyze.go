package analyze

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuna-r/miruri/internal/fingerprint"
	"github.com/yuna-r/miruri/internal/fsutil"
	"github.com/yuna-r/miruri/internal/model"
)

const maxScannedTextBytes int64 = 2 << 20

//go:embed packs/*.json
var packFS embed.FS

type PackManifest struct {
	ID          string          `json:"id"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Rules       []DetectionRule `json:"rules"`
}

type DetectionRule struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Domain      string   `json:"domain"`
	Capability  string   `json:"capability"`
	Hard        bool     `json:"hard"`
	Patterns    []string `json:"patterns"`
	Extensions  []string `json:"extensions"`
	FileNames   []string `json:"file_names"`
	PathParts   []string `json:"path_parts"`
	AllowText   bool     `json:"allow_text"`
}

type Options struct {
	FollowSymlinks bool
	ExcludePaths   []string
}

type scannedFile struct {
	AbsPath  string
	RelPath  string
	Ext      string
	Language string
	Text     string
	IsText   bool
	Size     int64
}

func Project(root string, opts Options) (model.AnalysisReport, error) {
	abs, err := fsutil.CanonicalPath(root)
	if err != nil {
		return model.AnalysisReport{}, fmt.Errorf("resolve project path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return model.AnalysisReport{}, fmt.Errorf("stat project path: %w", err)
	}
	if !info.IsDir() {
		return model.AnalysisReport{}, fmt.Errorf("project path is not a directory: %s", abs)
	}

	fingerprintResult, err := fingerprint.Project(abs, fingerprint.Options{ExcludePaths: opts.ExcludePaths})
	if err != nil {
		return model.AnalysisReport{}, err
	}

	packs, err := loadPacks()
	if err != nil {
		return model.AnalysisReport{}, err
	}

	files, warnings, err := scanFiles(abs, opts)
	if err != nil {
		return model.AnalysisReport{}, err
	}

	report := model.AnalysisReport{
		SchemaVersion:  "miruri.analysis.v1",
		GeneratedAt:    time.Now().UTC(),
		ProjectName:    filepath.Base(abs),
		ProjectPath:    abs,
		ProjectDigest:  fingerprintResult.Digest,
		ProjectEntries: fingerprintResult.FileCount,
		ProjectBytes:   fingerprintResult.ByteCount,
		Languages:      map[string]int{},
		Requirements:   []model.CapabilityRequirement{},
		Graph: model.ProjectGraph{
			Nodes: []model.GraphNode{{
				ID:   "project:root",
				Kind: "project",
				Name: filepath.Base(abs),
				Path: abs,
			}},
			Edges: []model.GraphEdge{},
		},
		Warnings: append(append([]string(nil), fingerprintResult.Warnings...), warnings...),
	}

	report.BuildSystems = detectBuildSystems(abs)
	capabilityByID := map[string]*model.CapabilityRequirement{}
	capNodeAdded := map[string]bool{}

	for i, f := range files {
		report.FileCount++
		if f.IsText {
			report.TextFileCount++
		} else {
			report.BinaryCount++
		}
		if f.Language != "" {
			report.Languages[f.Language]++
		}

		fileID := fmt.Sprintf("file:%d", i)
		report.Graph.Nodes = append(report.Graph.Nodes, model.GraphNode{
			ID:   fileID,
			Kind: fileNodeKind(f),
			Name: filepath.Base(f.RelPath),
			Path: filepath.ToSlash(f.RelPath),
			Metadata: map[string]string{
				"language":  f.Language,
				"extension": f.Ext,
			},
		})
		report.Graph.Edges = append(report.Graph.Edges, model.GraphEdge{From: "project:root", To: fileID, Kind: "contains"})

		for _, pack := range packs {
			for _, rule := range pack.Rules {
				matched, detail, line := matchRule(f, rule)
				if !matched {
					continue
				}
				req := capabilityByID[rule.Capability]
				if req == nil {
					req = &model.CapabilityRequirement{
						ID:          rule.Capability,
						Domain:      rule.Domain,
						Description: rule.Description,
						Hard:        rule.Hard,
					}
					capabilityByID[rule.Capability] = req
				}
				req.Hard = req.Hard || rule.Hard
				req.Evidence = appendEvidence(req.Evidence, model.Evidence{
					Path:   filepath.ToSlash(f.RelPath),
					Line:   line,
					RuleID: pack.ID + "/" + rule.ID,
					Detail: detail,
				})

				capID := "capability:" + rule.Capability
				if !capNodeAdded[capID] {
					report.Graph.Nodes = append(report.Graph.Nodes, model.GraphNode{
						ID:   capID,
						Kind: "capability-requirement",
						Name: rule.Capability,
						Metadata: map[string]string{
							"domain": rule.Domain,
							"hard":   fmt.Sprintf("%t", rule.Hard),
						},
					})
					capNodeAdded[capID] = true
				}
				report.Graph.Edges = append(report.Graph.Edges, model.GraphEdge{From: fileID, To: capID, Kind: "requires"})
			}
		}
	}

	for _, req := range capabilityByID {
		sort.Slice(req.Evidence, func(i, j int) bool {
			if req.Evidence[i].Path == req.Evidence[j].Path {
				return req.Evidence[i].Line < req.Evidence[j].Line
			}
			return req.Evidence[i].Path < req.Evidence[j].Path
		})
		report.Requirements = append(report.Requirements, *req)
	}
	sort.Slice(report.Requirements, func(i, j int) bool { return report.Requirements[i].ID < report.Requirements[j].ID })
	sort.Slice(report.BuildSystems, func(i, j int) bool { return report.BuildSystems[i] < report.BuildSystems[j] })
	return report, nil
}

func loadPacks() ([]PackManifest, error) {
	entries, err := fs.ReadDir(packFS, "packs")
	if err != nil {
		return nil, fmt.Errorf("read embedded domain packs: %w", err)
	}
	var packs []PackManifest
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := packFS.ReadFile("packs/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read domain pack %s: %w", entry.Name(), err)
		}
		var pack PackManifest
		if err := json.Unmarshal(data, &pack); err != nil {
			return nil, fmt.Errorf("parse domain pack %s: %w", entry.Name(), err)
		}
		packs = append(packs, pack)
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i].ID < packs[j].ID })
	return packs, nil
}

func scanFiles(root string, opts Options) ([]scannedFile, []string, error) {
	var files []scannedFile
	var warnings []string
	excludedPaths, err := normalizeExcludedPaths(root, opts.ExcludePaths)
	if err != nil {
		return nil, warnings, err
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			warnings = append(warnings, fmt.Sprintf("cannot access %s: %v", path, walkErr))
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel != "." && excludedPaths[filepath.Clean(path)] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if rel != "." && shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 && !opts.FollowSymlinks {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("cannot stat %s: %v", rel, err))
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		lang := languageForExtension(ext)
		f := scannedFile{AbsPath: path, RelPath: rel, Ext: ext, Language: lang, Size: info.Size()}
		if info.Size() <= maxScannedTextBytes && likelyTextExtension(ext) {
			content, err := os.ReadFile(path)
			if err == nil && !containsNUL(content) {
				f.Text = string(content)
				f.IsText = true
			}
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		return nil, warnings, fmt.Errorf("scan project: %w", err)
	}
	return files, warnings, nil
}

func matchRule(f scannedFile, rule DetectionRule) (bool, string, int) {
	hasCriteria := len(rule.Extensions) > 0 || len(rule.FileNames) > 0 || len(rule.Patterns) > 0 || len(rule.PathParts) > 0
	if !hasCriteria {
		return false, "", 0
	}
	if len(rule.Extensions) > 0 {
		matched := false
		for _, ext := range rule.Extensions {
			if strings.EqualFold(f.Ext, ext) {
				matched = true
				break
			}
		}
		if !matched {
			return false, "", 0
		}
	}
	if len(rule.FileNames) > 0 {
		matched := false
		name := filepath.Base(f.RelPath)
		for _, candidate := range rule.FileNames {
			if strings.EqualFold(name, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return false, "", 0
		}
	}
	if len(rule.PathParts) > 0 {
		rel := strings.ToLower(filepath.ToSlash(f.RelPath))
		matched := false
		for _, part := range rule.PathParts {
			if strings.Contains(rel, strings.ToLower(part)) {
				matched = true
				break
			}
		}
		if !matched {
			return false, "", 0
		}
	}
	if len(rule.Patterns) > 0 {
		// API and intrinsic signatures are matched only in source-like units.
		// README and design documents frequently mention APIs and would otherwise
		// create false project requirements. Build-file detection will use explicit
		// build-domain rules in a later pack.
		if !f.IsText || (f.Language == "" && !rule.AllowText) {
			return false, "", 0
		}
		lower := strings.ToLower(f.Text)
		for _, pattern := range rule.Patterns {
			idx := strings.Index(lower, strings.ToLower(pattern))
			if idx >= 0 {
				return true, pattern, lineNumber(f.Text, idx)
			}
		}
		return false, "", 0
	}
	return true, f.Ext, 0
}

func lineNumber(text string, byteIndex int) int {
	if byteIndex <= 0 {
		return 1
	}
	return 1 + strings.Count(text[:byteIndex], "\n")
}

func appendEvidence(existing []model.Evidence, candidate model.Evidence) []model.Evidence {
	for _, e := range existing {
		if e.Path == candidate.Path && e.Line == candidate.Line && e.RuleID == candidate.RuleID {
			return existing
		}
	}
	if len(existing) >= 32 {
		return existing
	}
	return append(existing, candidate)
}

func detectBuildSystems(root string) []model.BuildSystem {
	var out []model.BuildSystem
	if exists(filepath.Join(root, "CMakeLists.txt")) {
		out = append(out, model.BuildSystemCMake)
	}
	if exists(filepath.Join(root, "Makefile")) || exists(filepath.Join(root, "makefile")) || exists(filepath.Join(root, "GNUmakefile")) {
		out = append(out, model.BuildSystemMake)
	}
	if exists(filepath.Join(root, "meson.build")) {
		out = append(out, model.BuildSystemMeson)
	}
	if exists(filepath.Join(root, "configure.ac")) || exists(filepath.Join(root, "configure.in")) || exists(filepath.Join(root, "configure")) {
		out = append(out, model.BuildSystemAutotools)
	}
	if len(out) == 0 {
		out = append(out, model.BuildSystemUnknown)
	}
	return out
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func normalizeExcludedPaths(root string, values []string) (map[string]bool, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve project exclusion root: %w", err)
	}
	result := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		candidate := filepath.FromSlash(value)
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(rootAbs, candidate)
		}
		absolute, err := fsutil.CanonicalPath(candidate)
		if err != nil {
			return nil, fmt.Errorf("resolve project exclusion %q: %w", value, err)
		}
		relative, err := filepath.Rel(rootAbs, absolute)
		if err != nil {
			return nil, fmt.Errorf("relativize project exclusion %q: %w", value, err)
		}
		if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
			continue
		}
		result[absolute] = true
	}
	return result, nil
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", ".codex", ".miruri", "dist", "build", "out", "node_modules", ".idea", ".vscode", "target", "__pycache__":
		return true
	default:
		return false
	}
}

func fileNodeKind(f scannedFile) string {
	if f.Language != "" {
		return "source-unit"
	}
	if isResourceExtension(f.Ext) {
		return "resource"
	}
	if f.IsText {
		return "text-file"
	}
	return "binary-file"
}

func languageForExtension(ext string) string {
	switch ext {
	case ".c":
		return "C"
	case ".h":
		return "C header"
	case ".cc", ".cpp", ".cxx", ".hpp", ".hh", ".hxx":
		return "C++"
	case ".m":
		return "Objective-C"
	case ".mm":
		return "Objective-C++"
	case ".s", ".S", ".asm":
		return "Assembly"
	case ".hlsl", ".fx", ".fxh":
		return "HLSL"
	case ".glsl", ".vert", ".frag", ".geom", ".comp", ".tesc", ".tese":
		return "GLSL"
	case ".metal":
		return "Metal Shading Language"
	case ".spv":
		return "SPIR-V"
	case ".rc", ".rc2":
		return "Windows Resource Script"
	default:
		return ""
	}
}

func likelyTextExtension(ext string) bool {
	switch ext {
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hh", ".hxx", ".m", ".mm", ".s", ".S", ".asm",
		".hlsl", ".fx", ".fxh", ".glsl", ".vert", ".frag", ".geom", ".comp", ".tesc", ".tese", ".metal",
		".rc", ".rc2", ".plist", ".cmake", ".txt", ".md", ".json", ".toml", ".yaml", ".yml", ".xml", ".ini", "":
		return true
	default:
		return false
	}
}

func isResourceExtension(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".wav", ".ogg", ".flac", ".ttf", ".otf", ".ico", ".icns", ".plist", ".rc", ".rc2", ".spv":
		return true
	default:
		return false
	}
}

func containsNUL(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

// ReadLine is exposed for focused diagnostics without loading an entire large file.
func ReadLine(path string, line int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	for current := 1; ; current++ {
		text, readErr := reader.ReadString('\n')
		if current == line {
			return strings.TrimRight(text, "\r\n"), nil
		}
		if readErr != nil {
			if readErr == io.EOF {
				return "", fmt.Errorf("line %d not found", line)
			}
			return "", readErr
		}
	}
}
