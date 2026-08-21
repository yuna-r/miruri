package inspect

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/yuna-r/miruri/internal/fsutil"
	"github.com/yuna-r/miruri/internal/model"
)

type Result struct {
	Artifacts []model.ArtifactInfo
	Warnings  []string
}

func CollectAndPackage(searchRoot, packageRoot string, target model.TargetProfile) (Result, error) {
	var result Result
	copiedAppBundles := map[string]bool{}
	err := filepath.WalkDir(searchRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("cannot inspect %s: %v", path, walkErr))
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == ".miruri" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.Contains(filepath.ToSlash(path), "/CMakeFiles/") || strings.Contains(filepath.ToSlash(path), "/CompilerId") {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		if shouldIgnoreExtension(strings.ToLower(filepath.Ext(path))) {
			return nil
		}
		artifact, recognized, err := InspectFile(path, target)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("cannot parse candidate %s: %v", path, err))
			return nil
		}
		if !recognized {
			return nil
		}
		rel, err := filepath.Rel(searchRoot, path)
		if err != nil {
			return err
		}
		if target.OS == "darwin" {
			if appRoot, ok := macOSAppBundleRoot(path, searchRoot); ok && !copiedAppBundles[appRoot] {
				appRel, relErr := filepath.Rel(searchRoot, appRoot)
				if relErr != nil {
					return relErr
				}
				appDestination := filepath.Join(packageRoot, "artifacts", appRel)
				if copyErr := fsutil.CopyTree(appRoot, appDestination); copyErr != nil {
					return fmt.Errorf("copy macOS app bundle %s: %w", appRoot, copyErr)
				}
				copiedAppBundles[appRoot] = true
			}
		}
		destination := filepath.Join(packageRoot, "artifacts", rel)
		if err := fsutil.CopyFile(path, destination, info.Mode().Perm()); err != nil {
			return err
		}
		artifact.SourcePath = filepath.ToSlash(path)
		artifact.PackagedPath = filepath.ToSlash(destination)
		result.Artifacts = append(result.Artifacts, artifact)
		return nil
	})
	if err != nil {
		return result, err
	}
	sort.Slice(result.Artifacts, func(i, j int) bool { return result.Artifacts[i].PackagedPath < result.Artifacts[j].PackagedPath })
	return result, nil
}

func macOSAppBundleRoot(path, searchRoot string) (string, bool) {
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rootAbs, err := filepath.Abs(searchRoot)
	if err != nil {
		return "", false
	}
	current := filepath.Dir(pathAbs)
	for {
		if strings.HasSuffix(strings.ToLower(filepath.Base(current)), ".app") {
			rel, relErr := filepath.Rel(rootAbs, current)
			if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel) {
				return current, true
			}
			return "", false
		}
		if current == rootAbs || current == filepath.Dir(current) {
			return "", false
		}
		parent := filepath.Dir(current)
		rel, relErr := filepath.Rel(rootAbs, parent)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", false
		}
		current = parent
	}
}

func InspectFile(path string, target model.TargetProfile) (model.ArtifactInfo, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.ArtifactInfo{}, false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return model.ArtifactInfo{}, false, err
	}
	artifact, recognized, err := inspectBytes(data, filepath.Ext(path), info.Mode())
	if err != nil || !recognized {
		return artifact, recognized, err
	}
	digest := sha256.Sum256(data)
	artifact.Size = info.Size()
	artifact.SHA256 = hex.EncodeToString(digest[:])
	artifact.ArchitectureOK = architectureMatches(artifact.Architecture, target.Arch)
	if !artifact.ArchitectureOK && artifact.Architecture != "unknown" && artifact.Architecture != "multiple" {
		artifact.Notes = append(artifact.Notes, fmt.Sprintf("expected target architecture %s", target.Arch))
	}
	return artifact, true, nil
}

func inspectBytes(data []byte, extension string, mode fs.FileMode) (model.ArtifactInfo, bool, error) {
	if len(data) < 4 {
		return model.ArtifactInfo{}, false, nil
	}
	if bytes.HasPrefix(data, []byte("!<arch>\n")) {
		return inspectArchive(data)
	}
	if bytes.HasPrefix(data, []byte("!<thin>\n")) {
		return model.ArtifactInfo{Format: "archive", Architecture: "unknown", Kind: "static-library", Notes: []string{"thin archive architecture was not resolved"}}, true, nil
	}
	if bytes.HasPrefix(data, []byte{0x7f, 'E', 'L', 'F'}) {
		return inspectELF(data, extension, mode)
	}
	if bytes.HasPrefix(data, []byte{'M', 'Z'}) {
		return inspectPE(data, extension)
	}
	magic := uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
	switch magic {
	case 0xfeedface, 0xcefaedfe, 0xfeedfacf, 0xcffaedfe, 0xcafebabe, 0xbebafeca, 0xcafebabf, 0xbfbafeca:
		return inspectMachO(data, extension, mode)
	default:
		return model.ArtifactInfo{}, false, nil
	}
}

func inspectELF(data []byte, extension string, mode fs.FileMode) (model.ArtifactInfo, bool, error) {
	file, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return model.ArtifactInfo{}, false, err
	}
	defer file.Close()
	deps, _ := file.ImportedLibraries()
	kind := "object"
	switch file.Type {
	case elf.ET_EXEC:
		kind = "executable"
	case elf.ET_DYN:
		if strings.Contains(extension, ".so") || len(deps) > 0 && mode&0o111 == 0 {
			kind = "shared-library"
		} else {
			kind = "executable"
		}
	case elf.ET_REL:
		kind = "object"
	}
	return model.ArtifactInfo{
		Format:       "elf",
		Architecture: elfArchitecture(file.Machine, file.ByteOrder == nil),
		Kind:         kind,
		Dependencies: deps,
	}, true, nil
}

func inspectMachO(data []byte, extension string, mode fs.FileMode) (model.ArtifactInfo, bool, error) {
	if fat, err := macho.NewFatFile(bytes.NewReader(data)); err == nil {
		defer fat.Close()
		archSet := map[string]bool{}
		var deps []string
		for _, arch := range fat.Arches {
			archSet[machoArchitecture(arch.Cpu)] = true
			for _, load := range arch.Loads {
				if dylib, ok := load.(*macho.Dylib); ok {
					deps = append(deps, dylib.Name)
				}
			}
		}
		var architectures []string
		for arch := range archSet {
			architectures = append(architectures, arch)
		}
		sort.Strings(architectures)
		archName := "multiple"
		if len(architectures) == 1 {
			archName = architectures[0]
		}
		return model.ArtifactInfo{Format: "macho-fat", Architecture: archName, Kind: machoKind(extension, mode), Dependencies: uniqueStrings(deps), Notes: []string{"architectures: " + strings.Join(architectures, ", ")}}, true, nil
	}
	file, err := macho.NewFile(bytes.NewReader(data))
	if err != nil {
		return model.ArtifactInfo{}, false, err
	}
	defer file.Close()
	var deps []string
	for _, load := range file.Loads {
		if dylib, ok := load.(*macho.Dylib); ok {
			deps = append(deps, dylib.Name)
		}
	}
	return model.ArtifactInfo{Format: "macho", Architecture: machoArchitecture(file.Cpu), Kind: machoKind(extension, mode), Dependencies: uniqueStrings(deps)}, true, nil
}

func inspectPE(data []byte, extension string) (model.ArtifactInfo, bool, error) {
	file, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		return model.ArtifactInfo{}, false, err
	}
	defer file.Close()
	deps, _ := file.ImportedLibraries()
	kind := "executable"
	if strings.EqualFold(extension, ".dll") {
		kind = "shared-library"
	}
	return model.ArtifactInfo{Format: "pe", Architecture: peArchitecture(file.Machine), Kind: kind, Dependencies: deps}, true, nil
}

func inspectArchive(data []byte) (model.ArtifactInfo, bool, error) {
	reader := bytes.NewReader(data[8:])
	architectures := map[string]bool{}
	members := 0
	for reader.Len() >= 60 && members < 64 {
		header := make([]byte, 60)
		if _, err := io.ReadFull(reader, header); err != nil {
			break
		}
		if string(header[58:60]) != "`\n" {
			return model.ArtifactInfo{}, false, fmt.Errorf("invalid ar member header")
		}
		sizeText := strings.TrimSpace(string(header[48:58]))
		size, err := strconv.Atoi(sizeText)
		if err != nil || size < 0 || size > reader.Len() {
			return model.ArtifactInfo{}, false, fmt.Errorf("invalid ar member size %q", sizeText)
		}
		member := make([]byte, size)
		if _, err := io.ReadFull(reader, member); err != nil {
			return model.ArtifactInfo{}, false, err
		}
		if size%2 != 0 && reader.Len() > 0 {
			_, _ = reader.Seek(1, io.SeekCurrent)
		}
		members++
		name := strings.TrimSpace(string(header[:16]))
		if name == "/" || name == "//" || strings.HasPrefix(name, "/SYM") || strings.HasPrefix(name, "__.SYMDEF") {
			continue
		}
		child, recognized, _ := inspectBytes(member, ".o", 0)
		if recognized && child.Architecture != "" && child.Architecture != "unknown" {
			architectures[child.Architecture] = true
		}
	}
	var archNames []string
	for arch := range architectures {
		archNames = append(archNames, arch)
	}
	sort.Strings(archNames)
	arch := "unknown"
	if len(archNames) == 1 {
		arch = archNames[0]
	} else if len(archNames) > 1 {
		arch = "multiple"
	}
	artifact := model.ArtifactInfo{Format: "archive", Architecture: arch, Kind: "static-library"}
	if len(archNames) > 0 {
		artifact.Notes = append(artifact.Notes, "member architectures: "+strings.Join(archNames, ", "))
	}
	return artifact, true, nil
}

func elfArchitecture(machine elf.Machine, _ bool) string {
	switch machine {
	case elf.EM_X86_64:
		return "x86_64"
	case elf.EM_386:
		return "x86"
	case elf.EM_AARCH64:
		return "arm64"
	case elf.EM_ARM:
		return "arm"
	case elf.EM_RISCV:
		return "riscv"
	case elf.EM_PPC64:
		return "ppc64le"
	default:
		return strings.ToLower(strings.TrimPrefix(machine.String(), "EM_"))
	}
}

func machoArchitecture(cpu macho.Cpu) string {
	switch cpu {
	case macho.CpuAmd64:
		return "x86_64"
	case macho.Cpu386:
		return "x86"
	case macho.CpuArm64:
		return "arm64"
	case macho.CpuArm:
		return "arm"
	default:
		return strings.ToLower(cpu.String())
	}
}

func peArchitecture(machine uint16) string {
	switch machine {
	case pe.IMAGE_FILE_MACHINE_AMD64:
		return "x86_64"
	case pe.IMAGE_FILE_MACHINE_I386:
		return "x86"
	case pe.IMAGE_FILE_MACHINE_ARM64:
		return "arm64"
	case pe.IMAGE_FILE_MACHINE_ARM:
		return "arm"
	default:
		return fmt.Sprintf("pe-machine-0x%04x", machine)
	}
}

func machoKind(extension string, mode fs.FileMode) string {
	if strings.EqualFold(extension, ".dylib") || strings.EqualFold(extension, ".bundle") {
		return "shared-library"
	}
	if mode&0o111 != 0 || extension == "" || strings.EqualFold(extension, ".app") {
		return "executable"
	}
	return "object"
}

func architectureMatches(actual, expected string) bool {
	normalize := func(value string) string {
		switch strings.ToLower(value) {
		case "amd64", "x64":
			return "x86_64"
		case "aarch64":
			return "arm64"
		case "powerpc64le", "ppc64":
			return "ppc64le"
		case "riscv":
			if strings.HasPrefix(strings.ToLower(expected), "riscv") {
				return strings.ToLower(expected)
			}
			return "riscv"
		default:
			return strings.ToLower(value)
		}
	}
	return normalize(actual) == normalize(expected)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func shouldIgnoreExtension(extension string) bool {
	switch extension {
	case ".o", ".obj", ".d", ".pdb", ".ilk", ".map", ".cmake", ".txt", ".json", ".log":
		return true
	default:
		return false
	}
}
