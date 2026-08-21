// SPDX-License-Identifier: MPL-2.0

package builder

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/yuna-r/miruri/internal/repairworkspace"
)

func codexRepairChangeFilter(root string) repairworkspace.ChangeFilter {
	return func(path string) (bool, string) {
		path = filepath.ToSlash(filepath.Clean(path))
		if path == "MIRURI_REPAIR_NOTES.md" {
			return false, "legacy Miruri repair notes belong in result.json, not the target project"
		}
		if reason := generatedPathReason(path); reason != "" {
			return false, reason
		}
		absolute := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Lstat(absolute)
		if os.IsNotExist(err) {
			if knownTextRepairPath(path) {
				return true, ""
			}
			return false, "deletion of an unclassified or binary path is outside the source-repair boundary"
		}
		if err != nil {
			return false, fmt.Sprintf("cannot inspect changed path: %v", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true, ""
		}
		if !info.Mode().IsRegular() {
			return false, "non-regular filesystem entry"
		}
		compiled, err := hasCompiledArtifactMagic(absolute)
		if err != nil {
			return false, fmt.Sprintf("cannot inspect changed file header: %v", err)
		}
		if compiled {
			return false, "compiled executable/library/object produced during Codex validation"
		}
		text, err := isTextRepairFile(absolute)
		if err != nil {
			return false, fmt.Sprintf("cannot inspect changed file content: %v", err)
		}
		if !text {
			return false, "binary file changes are outside the source-repair patch boundary"
		}
		return true, ""
	}
}

func knownTextRepairPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "makefile", "gnumakefile", "cmakelists.txt", "configure", "meson.build", "meson_options.txt":
		return true
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".h++", ".hh", ".hpp", ".hxx",
		".m", ".mm", ".s", ".asm", ".inc", ".inl", ".ipp",
		".cmake", ".mk", ".in", ".ac", ".am", ".pc", ".def", ".rc",
		".hlsl", ".glsl", ".vert", ".frag", ".geom", ".tesc", ".tese", ".comp", ".metal",
		".sh", ".bash", ".zsh", ".fish", ".py", ".pl", ".rb", ".ps1", ".bat", ".cmd",
		".json", ".toml", ".yaml", ".yml", ".xml", ".plist", ".ini", ".cfg", ".conf",
		".txt", ".md", ".rst":
		return true
	default:
		return false
	}
}

func generatedPathReason(path string) string {
	lower := strings.ToLower(filepath.ToSlash(path))
	parts := strings.Split(lower, "/")
	for _, part := range parts[:max(0, len(parts)-1)] {
		if part == "build" || part == "dist" || part == "out" || part == "target" ||
			part == "cmakefiles" || part == "testing" || part == "_deps" ||
			part == ".cache" || part == ".miruri" || part == "__pycache__" {
			return "file is inside a conventional generated-build directory"
		}
		if strings.HasPrefix(part, "cmake-build-") {
			return "file is inside a CMake generated-build directory"
		}
	}
	base := filepath.Base(lower)
	switch base {
	case "cmakecache.txt", "cmake_install.cmake", "compile_commands.json",
		"install_manifest.txt", ".ninja_deps", ".ninja_log", ".ds_store":
		return "generated build metadata"
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".o", ".obj", ".lo", ".a", ".lib", ".so", ".dylib", ".dll",
		".exe", ".out", ".d", ".dep", ".pch", ".gch", ".pdb", ".ilk",
		".exp", ".map", ".gcno", ".gcda", ".profraw", ".profdata", ".pyc",
		".class", ".jar", ".wasm", ".bc":
		return "generated compiler/linker output"
	}
	return ""
}

func hasCompiledArtifactMagic(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	buffer := make([]byte, 8)
	n, err := io.ReadFull(file, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false, err
	}
	data := buffer[:n]
	if bytes.HasPrefix(data, []byte{0x7f, 'E', 'L', 'F'}) ||
		bytes.HasPrefix(data, []byte{'M', 'Z'}) ||
		bytes.HasPrefix(data, []byte("!<arch>\n")) ||
		bytes.HasPrefix(data, []byte("!<thin>\n")) {
		return true, nil
	}
	if len(data) >= 4 {
		magic := [4]byte{data[0], data[1], data[2], data[3]}
		switch magic {
		case [4]byte{0xfe, 0xed, 0xfa, 0xce}, [4]byte{0xce, 0xfa, 0xed, 0xfe},
			[4]byte{0xfe, 0xed, 0xfa, 0xcf}, [4]byte{0xcf, 0xfa, 0xed, 0xfe},
			[4]byte{0xca, 0xfe, 0xba, 0xbe}, [4]byte{0xbe, 0xba, 0xfe, 0xca},
			[4]byte{0xca, 0xfe, 0xba, 0xbf}, [4]byte{0xbf, 0xba, 0xfe, 0xca}:
			return true, nil
		}
	}
	return false, nil
}

func isTextRepairFile(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	buffer := make([]byte, 32*1024)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return false, err
	}
	data := buffer[:n]
	if bytes.IndexByte(data, 0) >= 0 {
		return false, nil
	}
	return utf8.Valid(data), nil
}
