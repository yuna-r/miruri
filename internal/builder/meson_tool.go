package builder

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	managedMesonVersion     = "1.12.0"
	managedMesonWheelName   = "meson-1.12.0-py3-none-any.whl"
	managedMesonWheelSHA256 = "71f133147fa0fcfe8f4df49fa1045771064947834538409e5d97b3613aac8b4e"
	managedMesonWheelURL    = "https://files.pythonhosted.org/packages/07/68/b0117422eb0a46d9d8d9e328f0c5b5c835179bfc058688bca35c90c89eba/meson-1.12.0-py3-none-any.whl"
	maxMesonWheelBytes      = 16 << 20
	maxMesonExtractedBytes  = 64 << 20
)

type mesonSpec struct {
	Version string
	Name    string
	URL     string
	SHA256  string
}

type mesonInvocation struct {
	Executable string
	PrefixArgs []string
	PythonPath string
	Managed    bool
}

var defaultMesonSpec = mesonSpec{
	Version: managedMesonVersion,
	Name:    managedMesonWheelName,
	URL:     managedMesonWheelURL,
	SHA256:  managedMesonWheelSHA256,
}

func (bc *buildContext) resolveMeson(ctx context.Context) (mesonInvocation, error) {
	if configured := strings.TrimSpace(os.Getenv("MIRURI_MESON")); configured != "" {
		path, err := filepath.Abs(configured)
		if err != nil {
			return mesonInvocation{}, fmt.Errorf("resolve MIRURI_MESON: %w", err)
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return mesonInvocation{}, fmt.Errorf("MIRURI_MESON does not point to an executable file: %s", path)
		}
		return mesonInvocation{Executable: path}, nil
	}
	if path, err := exec.LookPath("meson"); err == nil {
		return mesonInvocation{Executable: path}, nil
	}

	python, err := findManagedMesonPython(ctx)
	if err != nil {
		return mesonInvocation{}, err
	}
	siteDir, err := ensureManagedMeson(ctx, bc.cacheDir, bc.config.Offline, defaultMesonSpec, bc.outputWriter())
	if err != nil {
		return mesonInvocation{}, err
	}
	bc.logf("Miruri tool: using managed Meson %s from %s\n", managedMesonVersion, siteDir)
	return mesonInvocation{
		Executable: python,
		PrefixArgs: []string{"-m", "mesonbuild.mesonmain"},
		PythonPath: siteDir,
		Managed:    true,
	}, nil
}

func findManagedMesonPython(ctx context.Context) (string, error) {
	var candidates []string
	if configured := strings.TrimSpace(os.Getenv("MIRURI_PYTHON")); configured != "" {
		candidates = append(candidates, configured)
	}
	candidates = append(candidates, "python3")
	if runtime.GOOS == "darwin" {
		candidates = append(candidates, "/opt/homebrew/bin/python3", "/usr/local/bin/python3", "/usr/bin/python3")
	}

	seen := make(map[string]bool)
	for _, candidate := range candidates {
		path := candidate
		if !strings.ContainsRune(candidate, filepath.Separator) {
			resolved, err := exec.LookPath(candidate)
			if err != nil {
				continue
			}
			path = resolved
		}
		absolute, err := filepath.Abs(path)
		if err == nil {
			path = absolute
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		command := exec.CommandContext(ctx, path, "-c", "import sys; raise SystemExit(0 if sys.version_info >= (3, 10) else 1)")
		if err := command.Run(); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("Meson is not installed and Miruri could not find Python 3.10+ for its managed Meson runtime; install Python 3 or set MIRURI_PYTHON")
}

func ensureManagedMeson(ctx context.Context, cacheRoot string, offline bool, spec mesonSpec, progress io.Writer) (string, error) {
	if cacheRoot == "" {
		return "", fmt.Errorf("managed Meson requires a Miruri cache directory")
	}
	toolDir := filepath.Join(cacheRoot, "tools", "meson", spec.Version)
	wheelPath := filepath.Join(toolDir, spec.Name)
	siteDir := filepath.Join(toolDir, "site")
	markerPath := filepath.Join(siteDir, ".miruri-managed-meson")

	if managedMesonCacheValid(wheelPath, siteDir, markerPath, spec) {
		return siteDir, nil
	}
	if offline {
		return "", fmt.Errorf("Meson is not on PATH and managed Meson %s is not cached; --offline forbids downloading it", spec.Version)
	}
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		return "", fmt.Errorf("create managed Meson cache: %w", err)
	}

	release, err := acquireManagedToolLock(ctx, filepath.Join(toolDir, ".install.lock"))
	if err != nil {
		return "", err
	}
	defer release()

	if managedMesonCacheValid(wheelPath, siteDir, markerPath, spec) {
		return siteDir, nil
	}

	if !fileSHA256Matches(wheelPath, spec.SHA256) {
		_ = os.Remove(wheelPath)
		if progress != nil {
			fmt.Fprintf(progress, "Miruri tool: downloading Meson %s\n", spec.Version)
		}
		if err := downloadVerifiedFile(ctx, spec.URL, wheelPath, spec.SHA256, maxMesonWheelBytes); err != nil {
			return "", fmt.Errorf("provision managed Meson %s: %w", spec.Version, err)
		}
	}

	_ = os.RemoveAll(siteDir)
	partial, err := os.MkdirTemp(toolDir, ".site-partial-")
	if err != nil {
		return "", fmt.Errorf("create Meson extraction directory: %w", err)
	}
	defer os.RemoveAll(partial)
	if err := extractMesonWheel(wheelPath, partial); err != nil {
		return "", fmt.Errorf("extract managed Meson wheel: %w", err)
	}
	marker := fmt.Sprintf("version=%s\nwheel_sha256=%s\n", spec.Version, strings.ToLower(spec.SHA256))
	if err := os.WriteFile(filepath.Join(partial, ".miruri-managed-meson"), []byte(marker), 0o644); err != nil {
		return "", fmt.Errorf("write managed Meson marker: %w", err)
	}
	if err := os.Rename(partial, siteDir); err != nil {
		return "", fmt.Errorf("publish managed Meson runtime: %w", err)
	}
	if !managedMesonCacheValid(wheelPath, siteDir, markerPath, spec) {
		return "", fmt.Errorf("managed Meson cache failed post-install validation")
	}
	return siteDir, nil
}

func managedMesonCacheValid(wheelPath, siteDir, markerPath string, spec mesonSpec) bool {
	if !fileSHA256Matches(wheelPath, spec.SHA256) {
		return false
	}
	if info, err := os.Stat(filepath.Join(siteDir, "mesonbuild", "mesonmain.py")); err != nil || info.IsDir() {
		return false
	}
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		return false
	}
	expected := fmt.Sprintf("version=%s\nwheel_sha256=%s\n", spec.Version, strings.ToLower(spec.SHA256))
	return string(marker) == expected
}

func fileSHA256Matches(path, expected string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), strings.TrimSpace(expected))
}

func downloadVerifiedFile(ctx context.Context, url, destination, expectedSHA256 string, maxBytes int64) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxBytes {
		return fmt.Errorf("download is too large: %d bytes", response.ContentLength)
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".download-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	hash := sha256.New()
	limited := io.LimitReader(response.Body, maxBytes+1)
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), limited)
	closeErr := temporary.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxBytes {
		return fmt.Errorf("download exceeds %d bytes", maxBytes)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, strings.TrimSpace(expectedSHA256)) {
		return fmt.Errorf("SHA-256 mismatch: got %s, want %s", actual, expectedSHA256)
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	return nil
}

func extractMesonWheel(wheelPath, destination string) error {
	archive, err := zip.OpenReader(wheelPath)
	if err != nil {
		return err
	}
	defer archive.Close()

	var total uint64
	for _, entry := range archive.File {
		name := filepath.Clean(filepath.FromSlash(entry.Name))
		if name == "." || name == "" {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe wheel path %q", entry.Name)
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("wheel contains unsupported symlink %q", entry.Name)
		}
		total += entry.UncompressedSize64
		if total > maxMesonExtractedBytes {
			return fmt.Errorf("wheel expands beyond %d bytes", maxMesonExtractedBytes)
		}
		target := filepath.Join(destination, name)
		relative, err := filepath.Rel(destination, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe wheel path %q", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !entry.Mode().IsRegular() {
			return fmt.Errorf("wheel contains unsupported file type %q", entry.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		inputErr := input.Close()
		outputErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputErr != nil {
			return inputErr
		}
		if outputErr != nil {
			return outputErr
		}
	}
	if info, err := os.Stat(filepath.Join(destination, "mesonbuild", "mesonmain.py")); err != nil || info.IsDir() {
		return fmt.Errorf("wheel does not contain mesonbuild/mesonmain.py")
	}
	return nil
}

func acquireManagedToolLock(ctx context.Context, lockPath string) (func(), error) {
	for {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "pid=%d\ncreated=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
			_ = file.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire managed tool lock: %w", err)
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > 15*time.Minute {
			_ = os.Remove(lockPath)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
