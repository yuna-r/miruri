package fsutil

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalPath resolves symlinks through the deepest existing ancestor and
// then appends any not-yet-created suffix. It provides a stable filesystem
// boundary for source roots and generated output paths without requiring the
// destination itself to exist.
func CanonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	current := absolute
	var suffix []string
	for {
		_, statErr := os.Lstat(current)
		if statErr == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", fmt.Errorf("resolve symlinks in %s: %w", current, err)
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return "", err
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(statErr) {
			return "", fmt.Errorf("inspect path %s: %w", current, statErr)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("cannot resolve an existing ancestor for %s", absolute)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func WriteJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

type CopyTreeOptions struct {
	ExcludePaths []string
}

func CopyTree(source, destination string) error {
	return CopyTreeWithOptions(source, destination, CopyTreeOptions{})
}

func CopyTreeWithOptions(source, destination string, options CopyTreeOptions) error {
	sourceAbs, err := CanonicalPath(source)
	if err != nil {
		return err
	}
	destinationAbs, err := CanonicalPath(destination)
	if err != nil {
		return err
	}
	if sourceAbs == destinationAbs || strings.HasPrefix(destinationAbs+string(os.PathSeparator), sourceAbs+string(os.PathSeparator)) {
		return fmt.Errorf("destination must not be inside source: %s", destinationAbs)
	}
	excludedPaths, err := normalizeCopyExclusions(sourceAbs, options.ExcludePaths)
	if err != nil {
		return err
	}
	return filepath.WalkDir(sourceAbs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceAbs, path)
		if err != nil {
			return err
		}
		if rel != "." && excludedPaths[filepath.Clean(path)] {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() && rel != "." && skipDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		targetPath := filepath.Join(destinationAbs, rel)
		if entry.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			return os.Symlink(linkTarget, targetPath)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return CopyFile(path, targetPath, info.Mode().Perm())
	})
}

func normalizeCopyExclusions(sourceRoot string, values []string) (map[string]bool, error) {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		candidate := filepath.FromSlash(value)
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(sourceRoot, candidate)
		}
		absolute, err := CanonicalPath(candidate)
		if err != nil {
			return nil, fmt.Errorf("resolve copy exclusion %q: %w", value, err)
		}
		relative, err := filepath.Rel(sourceRoot, absolute)
		if err != nil {
			return nil, fmt.Errorf("relativize copy exclusion %q: %w", value, err)
		}
		if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
			continue
		}
		result[absolute] = true
	}
	return result, nil
}

func CopyFile(source, destination string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func skipDirectory(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", ".codex", ".miruri", "dist", "build", "out", "node_modules", "target", "__pycache__":
		return true
	default:
		return false
	}
}

// ValidateSymlinksWithin rejects symlinks that escape root. Codex repair runs
// inside a copied overlay, so an escaping link could otherwise expose or modify
// files from the user's original checkout through the workspace boundary.
func ValidateSymlinksWithin(root string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	return filepath.WalkDir(rootAbs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		resolved := filepath.Clean(target)
		rel, err := filepath.Rel(rootAbs, resolved)
		if err != nil {
			return err
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("symlink escapes isolated workspace: %s -> %s", path, resolved)
		}
		return nil
	})
}
