package sysroot

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type pendingHardlink struct {
	destination string
	target      string
}

type directoryMode struct {
	path string
	mode fs.FileMode
}

func applyLayer(root, blobPath, mediaType string) error {
	if strings.Contains(mediaType, "+zstd") || strings.Contains(mediaType, ".zstd") {
		return fmt.Errorf("zstd-compressed OCI layers are not supported by this Miruri build")
	}
	file, err := os.Open(blobPath)
	if err != nil {
		return err
	}
	defer file.Close()
	buffered := bufio.NewReader(file)
	var reader io.Reader = buffered
	magic, _ := buffered.Peek(4)
	if len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gzipReader, err := gzip.NewReader(buffered)
		if err != nil {
			return err
		}
		defer gzipReader.Close()
		reader = gzipReader
	} else if len(magic) == 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd {
		return fmt.Errorf("zstd-compressed OCI layers are not supported by this Miruri build")
	}

	tarReader := tar.NewReader(reader)
	var hardlinks []pendingHardlink
	var directories []directoryMode
	created := make(map[string]bool)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		relative, err := normalizeArchivePath(header.Name)
		if err != nil {
			return err
		}
		if relative == "" {
			continue
		}
		base := path.Base(relative)
		if strings.HasPrefix(base, ".wh.") {
			if err := applyWhiteout(root, relative, created); err != nil {
				return err
			}
			continue
		}
		destination := filepath.Join(root, filepath.FromSlash(relative))
		if err := ensureParentWithinRoot(root, destination); err != nil {
			return fmt.Errorf("unsafe layer path %s: %w", header.Name, err)
		}
		mode := safeMode(header.Mode)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := replaceNonDirectory(destination); err != nil {
				return err
			}
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return err
			}
			directories = append(directories, directoryMode{path: destination, mode: directoryPermission(mode)})
			created[relative] = true
		case tar.TypeReg, tar.TypeRegA:
			if err := replacePath(destination); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				return err
			}
			output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, filePermission(mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(output, tarReader)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if err := os.Chmod(destination, filePermission(mode)); err != nil {
				return err
			}
			created[relative] = true
		case tar.TypeSymlink:
			linkTarget, err := safeSymlinkTarget(root, relative, header.Linkname)
			if err != nil {
				return err
			}
			if err := replacePath(destination); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(linkTarget, destination); err != nil {
				return fmt.Errorf("create sysroot symlink %s -> %s: %w", relative, linkTarget, err)
			}
			created[relative] = true
		case tar.TypeLink:
			targetRelative, err := normalizeArchivePath(header.Linkname)
			if err != nil || targetRelative == "" {
				return fmt.Errorf("unsafe hardlink target %q for %s", header.Linkname, header.Name)
			}
			targetPath := filepath.Join(root, filepath.FromSlash(targetRelative))
			if err := ensurePathWithinRoot(root, targetPath); err != nil {
				return err
			}
			if err := replacePath(destination); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				return err
			}
			if err := os.Link(targetPath, destination); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					hardlinks = append(hardlinks, pendingHardlink{destination: destination, target: targetPath})
				} else {
					return err
				}
			}
			created[relative] = true
		case tar.TypeXHeader, tar.TypeXGlobalHeader, tar.TypeGNULongName, tar.TypeGNULongLink:
			// archive/tar consumes the metadata for the following entry.
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			// Device nodes and FIFOs are not needed for a compile/link sysroot and
			// are intentionally not materialized on the host.
		default:
			// Sockets and unknown special nodes are intentionally skipped.
		}
	}
	for _, hardlink := range hardlinks {
		if err := os.Link(hardlink.target, hardlink.destination); err != nil {
			return fmt.Errorf("resolve deferred hardlink %s -> %s: %w", hardlink.destination, hardlink.target, err)
		}
	}
	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i].path, string(os.PathSeparator)) > strings.Count(directories[j].path, string(os.PathSeparator))
	})
	for _, directory := range directories {
		if err := os.Chmod(directory.path, directory.mode); err != nil {
			return err
		}
	}
	return nil
}

func applyWhiteout(root, relative string, created map[string]bool) error {
	base := path.Base(relative)
	parentRelative := path.Dir(relative)
	if parentRelative == "." {
		parentRelative = ""
	}
	parent := filepath.Join(root, filepath.FromSlash(parentRelative))
	if err := ensurePathWithinRoot(root, parent); err != nil {
		return err
	}
	if base == ".wh..wh..opq" {
		entries, err := os.ReadDir(parent)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			childRelative := path.Join(parentRelative, entry.Name())
			if err := removeLowerPathPreservingCreated(root, childRelative, created); err != nil {
				return err
			}
		}
		return nil
	}
	name := strings.TrimPrefix(base, ".wh.")
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return fmt.Errorf("invalid OCI whiteout %q", relative)
	}
	target := filepath.Join(parent, name)
	if err := ensurePathWithinRoot(root, target); err != nil {
		return err
	}
	return removeLowerPathPreservingCreated(root, path.Join(parentRelative, name), created)
}

func removeLowerPathPreservingCreated(root, relative string, created map[string]bool) error {
	destination := filepath.Join(root, filepath.FromSlash(relative))
	if err := ensurePathWithinRoot(root, destination); err != nil {
		return err
	}
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if created[relative] {
			return nil
		}
		return os.RemoveAll(destination)
	}
	if !created[relative] && !hasCreatedDescendant(created, relative) {
		return os.RemoveAll(destination)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		child := path.Join(relative, entry.Name())
		if err := removeLowerPathPreservingCreated(root, child, created); err != nil {
			return err
		}
	}
	return nil
}

func hasCreatedDescendant(created map[string]bool, relative string) bool {
	prefix := strings.TrimSuffix(relative, "/") + "/"
	for candidate := range created {
		if strings.HasPrefix(candidate, prefix) {
			return true
		}
	}
	return false
}

func normalizeArchivePath(value string) (string, error) {
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.TrimPrefix(value, "./")
	clean := path.Clean(value)
	if clean == "." || clean == "" {
		return "", nil
	}
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("archive entry escapes sysroot: %q", value)
	}
	return clean, nil
}

func safeSymlinkTarget(root, relative, linkname string) (string, error) {
	linkname = strings.ReplaceAll(linkname, "\\", "/")
	if linkname == "" {
		return "", fmt.Errorf("empty symlink target for %s", relative)
	}
	if path.IsAbs(linkname) {
		targetRelative, err := normalizeArchivePath(strings.TrimPrefix(linkname, "/"))
		if err != nil {
			return "", err
		}
		destinationParent := filepath.Join(root, filepath.FromSlash(path.Dir(relative)))
		targetPath := filepath.Join(root, filepath.FromSlash(targetRelative))
		linkTarget, err := filepath.Rel(destinationParent, targetPath)
		if err != nil {
			return "", err
		}
		return linkTarget, nil
	}
	virtualTarget := path.Clean(path.Join(path.Dir(relative), linkname))
	if virtualTarget == ".." || strings.HasPrefix(virtualTarget, "../") || path.IsAbs(virtualTarget) {
		return "", fmt.Errorf("symlink escapes sysroot: %s -> %s", relative, linkname)
	}
	return filepath.FromSlash(linkname), nil
}

func ensureParentWithinRoot(root, destination string) error {
	if err := ensurePathWithinRoot(root, destination); err != nil {
		return err
	}
	parent := filepath.Dir(destination)
	ancestor := parent
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		next := filepath.Dir(ancestor)
		if next == ancestor {
			return fmt.Errorf("no existing ancestor for %s", destination)
		}
		ancestor = next
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	return ensurePathWithinRoot(resolvedRoot, resolved)
}

func ensurePathWithinRoot(root, candidate string) error {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	candidateAbsolute, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbsolute, candidateAbsolute)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("path escapes sysroot: %s", candidate)
	}
	return nil
}

func replaceNonDirectory(destination string) error {
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}
	return os.RemoveAll(destination)
}

func replacePath(destination string) error {
	if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return os.RemoveAll(destination)
}

func safeMode(mode int64) fs.FileMode {
	return fs.FileMode(mode) & 0o777
}

func filePermission(mode fs.FileMode) fs.FileMode {
	if mode == 0 {
		return 0o644
	}
	return mode
}

func directoryPermission(mode fs.FileMode) fs.FileMode {
	if mode == 0 {
		return 0o755
	}
	return mode
}

func validateManagedRootfs(root string) error {
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return fmt.Errorf("rootfs directory is missing")
	}
	required := []struct {
		name     string
		patterns []string
	}{
		{name: "C headers", patterns: []string{"usr/include/stdio.h", "include/stdio.h"}},
		{name: "C runtime startup objects", patterns: []string{"usr/lib/*/crt1.o", "usr/lib/crt1.o", "lib/*/crt1.o"}},
		{name: "C library", patterns: []string{"usr/lib/*/libc.so", "usr/lib/libc.so", "lib/*/libc.so.6", "lib/libc.so.6"}},
		{name: "GCC runtime", patterns: []string{"usr/lib/gcc/*/*/libgcc.a", "usr/local/lib/gcc/*/*/libgcc.a", "lib/gcc/*/*/libgcc.a"}},
	}
	for _, requirement := range required {
		found := false
		for _, pattern := range requirement.patterns {
			matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
			for _, match := range matches {
				if info, err := os.Stat(match); err == nil && !info.IsDir() {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return fmt.Errorf("%s not found", requirement.name)
		}
	}
	return nil
}
