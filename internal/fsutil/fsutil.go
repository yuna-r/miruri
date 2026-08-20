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

func CopyTree(source, destination string) error {
	sourceAbs, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	destinationAbs, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if sourceAbs == destinationAbs || strings.HasPrefix(destinationAbs+string(os.PathSeparator), sourceAbs+string(os.PathSeparator)) {
		return fmt.Errorf("destination must not be inside source: %s", destinationAbs)
	}
	return filepath.WalkDir(sourceAbs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceAbs, path)
		if err != nil {
			return err
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
