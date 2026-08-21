package builder

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuna-r/miruri/internal/fsutil"
	"github.com/yuna-r/miruri/internal/model"
)

// packageSimpleMacOSApp creates a deliberately small .app wrapper around a
// staged interpreted Meson application. It does not vendor Python/GTK; it only
// makes the staged resources relocatable inside the bundle.
func packageSimpleMacOSApp(stageRoot, packageDir, projectName string) (model.ArtifactInfo, bool, error) {
	launcher, ok, err := findStagedLauncher(stageRoot, projectName)
	if err != nil || !ok {
		return model.ArtifactInfo{}, false, err
	}

	launcherName := filepath.Base(launcher)
	appName := macOSAppName(projectName, launcherName)
	artifactDir := filepath.Join(packageDir, "artifacts")
	appDir := filepath.Join(artifactDir, appName+".app")
	contentsDir := filepath.Join(appDir, "Contents")
	macOSDir := filepath.Join(contentsDir, "MacOS")
	resourcesDir := filepath.Join(contentsDir, "Resources")
	if err := os.RemoveAll(appDir); err != nil {
		return model.ArtifactInfo{}, false, err
	}
	if err := os.MkdirAll(macOSDir, 0o755); err != nil {
		return model.ArtifactInfo{}, false, err
	}
	if err := os.MkdirAll(resourcesDir, 0o755); err != nil {
		return model.ArtifactInfo{}, false, err
	}

	shareRoot, ok := findStagedShareRoot(stageRoot)
	if !ok {
		return model.ArtifactInfo{}, false, fmt.Errorf("staged application has no share directory")
	}
	if err := fsutil.CopyTree(shareRoot, filepath.Join(resourcesDir, "share")); err != nil {
		return model.ArtifactInfo{}, false, fmt.Errorf("copy staged resources into app bundle: %w", err)
	}

	launcherData, err := os.ReadFile(launcher)
	if err != nil {
		return model.ArtifactInfo{}, false, err
	}
	rewritten, changed := rewritePythonLauncherForBundle(launcherData)
	if !changed {
		return model.ArtifactInfo{}, false, fmt.Errorf("launcher %s is not a supported Python install-prefix launcher", launcherName)
	}
	const bundleEntryName = "__miruri_bundle_entry__.py"
	bundlePythonLauncher := filepath.Join(macOSDir, bundleEntryName)
	if err := os.WriteFile(bundlePythonLauncher, rewritten, 0o755); err != nil {
		return model.ArtifactInfo{}, false, err
	}
	preferredPython := pythonShebangExecutable(rewritten)
	bundleLauncher := filepath.Join(macOSDir, launcherName)
	wrapper := pythonBundleRuntimeWrapper(bundleEntryName, preferredPython)
	if err := os.WriteFile(bundleLauncher, []byte(wrapper), 0o755); err != nil {
		return model.ArtifactInfo{}, false, err
	}

	bundleID, displayName := stagedDesktopMetadata(filepath.Join(resourcesDir, "share"), appName)
	plist := simpleInfoPlist(bundleID, displayName, launcherName)
	if err := os.WriteFile(filepath.Join(contentsDir, "Info.plist"), []byte(plist), 0o644); err != nil {
		return model.ArtifactInfo{}, false, err
	}

	notes := []string{
		"minimal relocatable macOS app wrapper for an interpreted Meson install payload",
		"Python/GTK/GObject runtime dependencies are not vendored and must be available on the target Mac",
		"direct bundle directory: " + filepath.ToSlash(appDir),
	}
	if schemaDir := filepath.Join(resourcesDir, "share", "glib-2.0", "schemas"); dirExists(schemaDir) {
		if tool, lookupErr := exec.LookPath("glib-compile-schemas"); lookupErr == nil {
			cmd := exec.Command(tool, schemaDir)
			if output, compileErr := cmd.CombinedOutput(); compileErr != nil {
				notes = append(notes, "GSettings schema compilation failed: "+strings.TrimSpace(string(output)))
			} else {
				notes = append(notes, "compiled bundled GSettings schemas")
			}
		} else {
			notes = append(notes, "glib-compile-schemas was unavailable; bundled schema XML was left uncompiled")
		}
	}

	tarPath := filepath.Join(artifactDir, appName+".app.tar")
	size, digest, err := deterministicTarDirectory(appDir, tarPath, filepath.Base(appDir))
	if err != nil {
		return model.ArtifactInfo{}, false, err
	}
	return model.ArtifactInfo{
		SourcePath:     filepath.ToSlash(appDir),
		PackagedPath:   filepath.ToSlash(tarPath),
		Format:         "macos-app-tar",
		Architecture:   "portable",
		Kind:           "application-bundle",
		Size:           size,
		SHA256:         digest,
		ArchitectureOK: true,
		Notes:          notes,
	}, true, nil
}

func findStagedLauncher(stageRoot, projectName string) (string, bool, error) {
	var candidates []string
	for _, rel := range []string{"usr/local/bin", "opt/homebrew/bin", "usr/bin"} {
		dir := filepath.Join(stageRoot, filepath.FromSlash(rel))
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", false, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			info, err := entry.Info()
			if err != nil {
				return "", false, err
			}
			if info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
				candidates = append(candidates, path)
			}
		}
	}
	if len(candidates) == 0 {
		return "", false, nil
	}
	sort.Strings(candidates)
	for _, candidate := range candidates {
		if strings.EqualFold(filepath.Base(candidate), projectName) {
			return candidate, true, nil
		}
	}
	if len(candidates) == 1 {
		return candidates[0], true, nil
	}
	return "", false, fmt.Errorf("multiple staged launchers found and none matches project name %q", projectName)
}

func findStagedShareRoot(stageRoot string) (string, bool) {
	for _, rel := range []string{"usr/local/share", "opt/homebrew/share", "usr/share"} {
		path := filepath.Join(stageRoot, filepath.FromSlash(rel))
		if dirExists(path) {
			return path, true
		}
	}
	return "", false
}

func rewritePythonLauncherForBundle(data []byte) ([]byte, bool) {
	text := string(data)
	firstLine := text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		firstLine = text[:i]
	}
	if !strings.HasPrefix(firstLine, "#!") || !strings.Contains(strings.ToLower(firstLine), "python") {
		return data, false
	}

	lines := strings.Split(text, "\n")
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		eq := strings.Index(trimmed, "=")
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(trimmed[:eq])
		value := strings.TrimSpace(trimmed[eq+1:])
		if !isPythonIdentifier(name) || len(value) < 2 {
			continue
		}
		quote := value[0]
		if (quote != '\'' && quote != '"') || value[len(value)-1] != quote {
			continue
		}
		pathValue := value[1 : len(value)-1]
		const prefix = "/usr/local/share"
		if pathValue != prefix && !strings.HasPrefix(pathValue, prefix+"/") {
			continue
		}
		rel := strings.TrimPrefix(pathValue, prefix)
		rel = strings.TrimPrefix(rel, "/")
		parts := []string{"_miruri_resources", "'share'"}
		if rel != "" {
			for _, part := range strings.Split(rel, "/") {
				parts = append(parts, pythonSingleQuoted(part))
			}
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + name + " = os.path.join(" + strings.Join(parts, ", ") + ")"
		changed = true
	}
	if !changed {
		return data, false
	}

	injected := []string{
		lines[0],
		"import os",
		"import site",
		"import sys",
		"_miruri_pyver = f\"python{sys.version_info.major}.{sys.version_info.minor}\"",
		"for _miruri_brew_prefix in ('/opt/homebrew', '/usr/local'):",
		"    for _miruri_formula in ('pygobject3', 'py3cairo'):",
		"        _miruri_site = os.path.join(_miruri_brew_prefix, 'opt', _miruri_formula, 'lib', _miruri_pyver, 'site-packages')",
		"        if os.path.isdir(_miruri_site):",
		"            site.addsitedir(_miruri_site)",
		"_miruri_resources = os.path.abspath(os.path.join(os.path.dirname(__file__), '..', 'Resources'))",
		"_miruri_share = os.path.join(_miruri_resources, 'share')",
		"os.environ['XDG_DATA_DIRS'] = _miruri_share + os.pathsep + os.environ.get('XDG_DATA_DIRS', '/usr/local/share:/usr/share')",
		"_miruri_schema_dir = os.path.join(_miruri_share, 'glib-2.0', 'schemas')",
		"if os.path.isdir(_miruri_schema_dir):",
		"    os.environ['GSETTINGS_SCHEMA_DIR'] = _miruri_schema_dir",
	}
	lines = append(injected, lines[1:]...)
	text = strings.Join(lines, "\n")
	// Python's macOS _locale module does not expose the GNU gettext helpers
	// that some Linux/GNOME launchers call through locale. The gettext module
	// provides the portable equivalents, so rewrite only the bundled launcher.
	text = strings.ReplaceAll(text, "locale.bindtextdomain(", "gettext.bindtextdomain(")
	text = strings.ReplaceAll(text, "locale.textdomain(", "gettext.textdomain(")
	return []byte(text), true
}

func pythonShebangExecutable(data []byte) string {
	line := string(data)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "#!") {
		return ""
	}
	value := strings.TrimSpace(strings.TrimPrefix(line, "#!"))
	if value == "" || strings.ContainsAny(value, " \t") {
		return ""
	}
	return value
}

func pythonBundleRuntimeWrapper(payloadName, preferredPython string) string {
	payloadName = strings.ReplaceAll(payloadName, "'", "'\\''")
	preferredPython = strings.ReplaceAll(preferredPython, "'", "'\\''")
	return fmt.Sprintf(`#!/bin/sh
SELF_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
APP="$SELF_DIR/%s"
PREFERRED_PYTHON='%s'

append_site() {
    [ -d "$1" ] || return 0
    case ":$MIRURI_PYTHONPATH:" in
        *:"$1":*) return 0 ;;
    esac
    if [ -n "$MIRURI_PYTHONPATH" ]; then
        MIRURI_PYTHONPATH="$MIRURI_PYTHONPATH:$1"
    else
        MIRURI_PYTHONPATH="$1"
    fi
}

try_python() {
    py="$1"
    shift
    [ -x "$py" ] || return 1
    ver=$("$py" -c 'import sys; print("%%d.%%d" %% sys.version_info[:2])' 2>/dev/null) || return 1
    MIRURI_PYTHONPATH=""
    for root in /opt/homebrew /usr/local; do
        append_site "$root/lib/python$ver/site-packages"
        for formula in pygobject3 py3cairo; do
            append_site "$root/opt/$formula/lib/python$ver/site-packages"
            append_site "$root/opt/$formula/libexec/lib/python$ver/site-packages"
            for d in "$root"/Cellar/"$formula"/*/lib/python"$ver"/site-packages "$root"/Cellar/"$formula"/*/libexec/lib/python"$ver"/site-packages; do
                append_site "$d"
            done
        done
    done
    if [ -n "${PYTHONPATH-}" ]; then
        if [ -n "$MIRURI_PYTHONPATH" ]; then
            MIRURI_PYTHONPATH="$MIRURI_PYTHONPATH:$PYTHONPATH"
        else
            MIRURI_PYTHONPATH="$PYTHONPATH"
        fi
    fi
    if env PYTHONPATH="$MIRURI_PYTHONPATH" "$py" -c 'import gi; import cairo' >/dev/null 2>&1; then
        exec env PYTHONPATH="$MIRURI_PYTHONPATH" "$py" "$APP" "$@"
    fi
    return 1
}

[ -z "$PREFERRED_PYTHON" ] || try_python "$PREFERRED_PYTHON" "$@"
for py in \n    /opt/homebrew/bin/python3 \n    /opt/homebrew/opt/python@3.14/bin/python3.14 \n    /opt/homebrew/opt/python@3.13/bin/python3.13 \n    /usr/local/bin/python3 \n    /usr/local/opt/python@3.14/bin/python3.14 \n    /usr/local/opt/python@3.13/bin/python3.13 \n    /opt/homebrew/opt/python@3.*/bin/python3.* \n    /usr/local/opt/python@3.*/bin/python3.*
do
    try_python "$py" "$@"
done

echo "Miruri: no Homebrew Python runtime capable of importing gi and cairo was found." >&2
echo "Install or refresh dependencies with: brew install pygobject3 py3cairo gtk+3" >&2
exit 127
`, payloadName, preferredPython)
}

func isPythonIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func pythonSingleQuoted(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "'", "\\'")
	return "'" + value + "'"
}

func macOSAppName(projectName, launcherName string) string {
	name := strings.TrimSpace(projectName)
	if name == "" {
		name = launcherName
	}
	if name == "" {
		return "MiruriApp"
	}
	runes := []rune(name)
	if runes[0] >= 'a' && runes[0] <= 'z' {
		runes[0] = runes[0] - ('a' - 'A')
	}
	return string(runes)
}

func stagedDesktopMetadata(shareRoot, fallbackName string) (string, string) {
	applications := filepath.Join(shareRoot, "applications")
	entries, _ := filepath.Glob(filepath.Join(applications, "*.desktop"))
	sort.Strings(entries)
	bundleID := "org.miruri." + sanitizeBundleIDPart(strings.ToLower(fallbackName))
	displayName := fallbackName
	if len(entries) == 0 {
		return bundleID, displayName
	}
	base := strings.TrimSuffix(filepath.Base(entries[0]), ".desktop")
	if strings.Contains(base, ".") {
		bundleID = base
	}
	if data, err := os.ReadFile(entries[0]); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "Name=") {
				if value := strings.TrimSpace(strings.TrimPrefix(line, "Name=")); value != "" {
					displayName = value
				}
				break
			}
		}
	}
	return bundleID, displayName
}

func sanitizeBundleIDPart(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "app"
	}
	return b.String()
}

func simpleInfoPlist(bundleID, displayName, executable string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key><string>en</string>
  <key>CFBundleDisplayName</key><string>%s</string>
  <key>CFBundleExecutable</key><string>%s</string>
  <key>CFBundleIdentifier</key><string>%s</string>
  <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
  <key>CFBundleName</key><string>%s</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>1.0</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
`, html.EscapeString(displayName), html.EscapeString(executable), html.EscapeString(bundleID), html.EscapeString(displayName))
}

func deterministicTarDirectory(root, destination, topLevel string) (int64, string, error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return 0, "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".app-*.tar")
	if err != nil {
		return 0, "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	tw := tar.NewWriter(tmp)
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		linkTarget := ""
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return err
		}
		name := topLevel
		if rel != "." {
			name = filepath.ToSlash(filepath.Join(topLevel, rel))
		}
		header.Name = name
		header.ModTime = time.Unix(0, 0).UTC()
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		header.Uid, header.Gid = 0, 0
		header.Uname, header.Gname = "", ""
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		}
		return nil
	})
	closeTarErr := tw.Close()
	closeFileErr := tmp.Close()
	if walkErr != nil {
		return 0, "", walkErr
	}
	if closeTarErr != nil {
		return 0, "", closeTarErr
	}
	if closeFileErr != nil {
		return 0, "", closeFileErr
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return 0, "", err
	}
	file, err := os.Open(destination)
	if err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return 0, "", copyErr
	}
	if closeErr != nil {
		return 0, "", closeErr
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
