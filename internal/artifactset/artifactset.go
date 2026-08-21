package artifactset

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yuna-r/miruri/internal/fsutil"
	"github.com/yuna-r/miruri/internal/model"
)

const ManifestName = "manifest.json"
const IntegrityName = "checksums.sha256"

// Location resolves either an artifact-set directory or a manifest path.
type Location struct {
	PackageDir   string
	ManifestPath string
}

func Resolve(input string) (Location, error) {
	if strings.TrimSpace(input) == "" {
		input = "."
	}
	absolute, err := filepath.Abs(input)
	if err != nil {
		return Location{}, fmt.Errorf("resolve artifact set: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Location{}, fmt.Errorf("stat artifact set: %w", err)
	}
	if info.IsDir() {
		return Location{PackageDir: absolute, ManifestPath: filepath.Join(absolute, ManifestName)}, nil
	}
	if !info.Mode().IsRegular() {
		return Location{}, fmt.Errorf("artifact set input is neither a directory nor a regular manifest: %s", absolute)
	}
	return Location{PackageDir: filepath.Dir(absolute), ManifestPath: absolute}, nil
}

func LoadManifest(input string) (Location, model.BuildManifest, error) {
	location, err := Resolve(input)
	if err != nil {
		return Location{}, model.BuildManifest{}, err
	}
	data, err := os.ReadFile(location.ManifestPath)
	if err != nil {
		return Location{}, model.BuildManifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest model.BuildManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Location{}, model.BuildManifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	return location, manifest, nil
}

// ResolvePath accepts modern package-relative paths and legacy absolute paths,
// but always requires the resolved object to remain within the artifact set.
func ResolvePath(packageDir, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("empty artifact-set path")
	}
	packageAbs, err := filepath.Abs(packageDir)
	if err != nil {
		return "", err
	}
	packageAbs = filepath.Clean(packageAbs)
	candidate := filepath.FromSlash(value)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(packageAbs, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	candidate = filepath.Clean(candidate)

	// Reject lexical traversal before resolving filesystem aliases. This keeps
	// package-relative paths constrained even when the destination does not yet
	// exist.
	if err := ensureWithin(packageAbs, candidate); err != nil {
		return "", err
	}

	// Canonicalize both sides of the containment comparison. On macOS, /var is
	// an alias of /private/var, so comparing an unresolved package root with a
	// resolved child incorrectly classifies ordinary files as symlink escapes.
	// CanonicalPath also resolves the deepest existing ancestor, preserving the
	// same boundary check for not-yet-created paths.
	packageCanonical, err := fsutil.CanonicalPath(packageAbs)
	if err != nil {
		return "", fmt.Errorf("canonicalize artifact-set root: %w", err)
	}
	candidateCanonical, err := fsutil.CanonicalPath(candidate)
	if err != nil {
		return "", fmt.Errorf("canonicalize artifact-set path %s: %w", value, err)
	}
	if err := ensureWithin(packageCanonical, candidateCanonical); err != nil {
		return "", fmt.Errorf("artifact-set path escapes through symlink: %w", err)
	}
	return candidate, nil
}

func RelativePath(packageDir, path string) string {
	if path == "" {
		return ""
	}
	packageAbs, packageErr := filepath.Abs(packageDir)
	pathAbs, pathErr := filepath.Abs(path)
	if packageErr == nil && pathErr == nil {
		if rel, err := filepath.Rel(packageAbs, pathAbs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}

func StableArtifactPath(packageDir string, artifact model.ArtifactInfo) string {
	if artifact.PackagePath != "" {
		return filepath.ToSlash(artifact.PackagePath)
	}
	return RelativePath(packageDir, artifact.PackagedPath)
}

func WriteIntegrity(packageDir string) (string, error) {
	packageAbs, err := filepath.Abs(packageDir)
	if err != nil {
		return "", err
	}
	destination := filepath.Join(packageAbs, IntegrityName)
	entries, err := calculateIntegrityEntries(packageAbs, destination)
	if err != nil {
		return "", err
	}
	temporary := destination + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	writer := bufio.NewWriter(file)
	for _, entry := range entries {
		if _, err := fmt.Fprintf(writer, "%s  %s\n", entry.SHA256, entry.Path); err != nil {
			_ = file.Close()
			_ = os.Remove(temporary)
			return "", err
		}
	}
	if err := writer.Flush(); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	return IntegrityName, nil
}

type IntegrityEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func ReadIntegrity(packageDir, path string) ([]IntegrityEntry, error) {
	absolute, err := ResolvePath(packageDir, path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var entries []IntegrityEntry
	seen := map[string]bool{}
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 2<<20)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 67 || line[64:66] != "  " {
			return nil, fmt.Errorf("invalid integrity line %d", lineNumber)
		}
		digest := line[:64]
		if _, err := hex.DecodeString(digest); err != nil {
			return nil, fmt.Errorf("invalid SHA-256 on integrity line %d: %w", lineNumber, err)
		}
		pathValue := filepath.ToSlash(strings.TrimSpace(line[66:]))
		if err := validateRelativePath(pathValue); err != nil {
			return nil, fmt.Errorf("invalid path on integrity line %d: %w", lineNumber, err)
		}
		if seen[pathValue] {
			return nil, fmt.Errorf("duplicate integrity path %q", pathValue)
		}
		seen[pathValue] = true
		entries = append(entries, IntegrityEntry{Path: pathValue, SHA256: digest})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func ActualFiles(packageDir string) ([]string, error) {
	packageAbs, err := filepath.Abs(packageDir)
	if err != nil {
		return nil, err
	}
	var paths []string
	err = filepath.WalkDir(packageAbs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(packageAbs, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func HashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func calculateIntegrityEntries(packageDir, excludedPath string) ([]IntegrityEntry, error) {
	var entries []IntegrityEntry
	err := filepath.WalkDir(packageDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if path == excludedPath || path == excludedPath+".tmp" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact set contains unsupported symlink: %s", path)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		digest, size, err := HashFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(packageDir, path)
		if err != nil {
			return err
		}
		entries = append(entries, IntegrityEntry{Path: filepath.ToSlash(rel), SHA256: digest, Size: size})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func ensureWithin(root, candidate string) error {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("path %s is outside artifact set %s", candidate, root)
	}
	return nil
}

func validateRelativePath(value string) error {
	if value == "" || filepath.IsAbs(filepath.FromSlash(value)) {
		return fmt.Errorf("path must be relative")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path escapes artifact set")
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("path contains a newline")
	}
	return nil
}
