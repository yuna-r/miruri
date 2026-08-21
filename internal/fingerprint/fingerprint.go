package fingerprint

import (
	"crypto/sha256"
	"encoding/binary"
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
)

// Result is a deterministic content identity for a project tree. Modification
// times, ownership and absolute paths are intentionally excluded. The execute
// bit is retained because it can affect packaging and build behavior.
type Result struct {
	Digest    string   `json:"digest"`
	FileCount int      `json:"file_count"`
	ByteCount int64    `json:"byte_count"`
	Warnings  []string `json:"warnings,omitempty"`
}

// Options controls project fingerprinting. Directory names are compared at
// every depth, matching Miruri's analyzer and isolated-overlay exclusions.
type Options struct {
	ExcludeDirs  []string
	ExcludePaths []string
}

// Project computes a stable SHA-256 identity for all regular files and
// symlinks in root. Symlink targets are hashed as link text and are never
// followed, which keeps the identity independent of files outside the project.
func Project(root string, options Options) (Result, error) {
	rootAbs, err := fsutil.CanonicalPath(root)
	if err != nil {
		return Result{}, fmt.Errorf("resolve project path: %w", err)
	}
	info, err := os.Stat(rootAbs)
	if err != nil {
		return Result{}, fmt.Errorf("stat project path: %w", err)
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("project path is not a directory: %s", rootAbs)
	}

	excluded := defaultExcludedDirectories()
	for _, name := range options.ExcludeDirs {
		name = strings.TrimSpace(name)
		if name != "" {
			excluded[name] = true
		}
	}
	excludedPaths, err := normalizeExcludedPaths(rootAbs, options.ExcludePaths)
	if err != nil {
		return Result{}, err
	}

	type entry struct {
		path string
		mode fs.FileMode
		kind byte
	}
	var entries []entry
	var warnings []string
	err = filepath.WalkDir(rootAbs, func(path string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			warnings = append(warnings, fmt.Sprintf("cannot fingerprint %s: %v", path, walkErr))
			return nil
		}
		rel, relErr := filepath.Rel(rootAbs, path)
		if relErr != nil {
			return relErr
		}
		if rel != "." && excludedPaths[filepath.Clean(path)] {
			if dirEntry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if dirEntry.IsDir() {
			if rel != "." && excluded[dirEntry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		info, infoErr := dirEntry.Info()
		if infoErr != nil {
			warnings = append(warnings, fmt.Sprintf("cannot fingerprint %s: %v", rel, infoErr))
			return nil
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			entries = append(entries, entry{path: filepath.ToSlash(rel), mode: info.Mode(), kind: 'L'})
		case info.Mode().IsRegular():
			entries = append(entries, entry{path: filepath.ToSlash(rel), mode: info.Mode(), kind: 'F'})
		}
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("walk project for fingerprint: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })

	hash := sha256.New()
	if _, err := io.WriteString(hash, "miruri.project-fingerprint.v1"); err != nil {
		return Result{}, err
	}
	result := Result{Warnings: warnings}
	for _, candidate := range entries {
		absolute := filepath.Join(rootAbs, filepath.FromSlash(candidate.path))
		if _, err := hash.Write([]byte{candidate.kind}); err != nil {
			return Result{}, err
		}
		if err := writeFramedBytes(hash, []byte(candidate.path)); err != nil {
			return Result{}, err
		}
		if err := binary.Write(hash, binary.BigEndian, uint32(candidate.mode.Perm()&0o111)); err != nil {
			return Result{}, err
		}
		switch candidate.kind {
		case 'L':
			target, err := os.Readlink(absolute)
			if err != nil {
				return Result{}, fmt.Errorf("read symlink %s: %w", candidate.path, err)
			}
			payload := []byte(target)
			if err := writeFramedBytes(hash, payload); err != nil {
				return Result{}, err
			}
			result.ByteCount += int64(len(payload))
		case 'F':
			file, err := os.Open(absolute)
			if err != nil {
				return Result{}, fmt.Errorf("open %s for fingerprint: %w", candidate.path, err)
			}
			info, statErr := file.Stat()
			if statErr != nil {
				_ = file.Close()
				return Result{}, fmt.Errorf("stat %s for fingerprint: %w", candidate.path, statErr)
			}
			if err := binary.Write(hash, binary.BigEndian, uint64(info.Size())); err != nil {
				_ = file.Close()
				return Result{}, err
			}
			n, copyErr := io.Copy(hash, file)
			closeErr := file.Close()
			if copyErr != nil {
				return Result{}, fmt.Errorf("hash %s: %w", candidate.path, copyErr)
			}
			if closeErr != nil {
				return Result{}, fmt.Errorf("close %s after fingerprint: %w", candidate.path, closeErr)
			}
			if n != info.Size() {
				return Result{}, fmt.Errorf("file changed while fingerprinting %s: expected %d bytes, read %d", candidate.path, info.Size(), n)
			}
			result.ByteCount += n
		}
		result.FileCount++
	}
	result.Digest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	return result, nil
}

func writeFramedBytes(destination io.Writer, payload []byte) error {
	if err := binary.Write(destination, binary.BigEndian, uint64(len(payload))); err != nil {
		return err
	}
	_, err := destination.Write(payload)
	return err
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
			return nil, fmt.Errorf("resolve fingerprint exclusion %q: %w", value, err)
		}
		relative, err := filepath.Rel(root, absolute)
		if err != nil {
			return nil, fmt.Errorf("relativize fingerprint exclusion %q: %w", value, err)
		}
		if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
			continue
		}
		result[absolute] = true
	}
	return result, nil
}

func defaultExcludedDirectories() map[string]bool {
	return map[string]bool{
		".git":         true,
		".hg":          true,
		".codex":       true,
		".svn":         true,
		".miruri":      true,
		"dist":         true,
		"build":        true,
		"out":          true,
		"node_modules": true,
		"target":       true,
		"__pycache__":  true,
	}
}

// JSON returns a SHA-256 digest of Go's deterministic JSON encoding for value.
// It is used for build-request identities after all target and sysroot inputs
// have been resolved.
func JSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode fingerprint input: %w", err)
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// Short returns at most hexadecimalLength hexadecimal characters from a
// sha256:<hex> digest. Invalid or non-SHA-256 values are returned unchanged.
func Short(digest string, hexadecimalLength int) string {
	const prefix = "sha256:"
	if !strings.HasPrefix(digest, prefix) || hexadecimalLength <= 0 {
		return digest
	}
	hexPart := strings.TrimPrefix(digest, prefix)
	if len(hexPart) <= hexadecimalLength {
		return hexPart
	}
	return hexPart[:hexadecimalLength]
}
