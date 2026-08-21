package builder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/yuna-r/miruri/internal/model"
	"github.com/yuna-r/miruri/internal/target"
)

type llvmToolchain struct {
	CC           string
	CXX          string
	AR           string
	Ranlib       string
	Strip        string
	Linker       string
	GCCToolchain string
	BinDir       string
}

func discoverLLVMToolchain(profile model.TargetProfile, sysroot string) (llvmToolchain, error) {
	return discoverLLVMToolchainWithSearchDirs(profile, sysroot, llvmSearchDirectories())
}

func discoverLLVMToolchainWithSearchDirs(profile model.TargetProfile, sysroot string, searchDirs []string) (llvmToolchain, error) {
	native := target.IsNative(profile)
	cc, err := findTool("clang", searchDirs)
	if err != nil {
		return llvmToolchain{}, err
	}
	cxx, err := findTool("clang++", searchDirs)
	if err != nil {
		return llvmToolchain{}, err
	}
	toolchain := llvmToolchain{
		CC:     cc,
		CXX:    cxx,
		AR:     findOptionalTool("llvm-ar", searchDirs),
		Ranlib: findOptionalTool("llvm-ranlib", searchDirs),
		Strip:  findOptionalTool("llvm-strip", searchDirs),
		BinDir: filepath.Dir(cc),
	}
	if native && toolchain.AR == "" {
		toolchain.AR = findOptionalTool("ar", nil)
	}
	if native && toolchain.Ranlib == "" {
		toolchain.Ranlib = findOptionalTool("ranlib", nil)
	}
	if native && toolchain.Strip == "" {
		toolchain.Strip = findOptionalTool("strip", nil)
	}
	if profile.DefaultLinker == "lld" {
		toolchain.Linker = findOptionalTool("ld.lld", append([]string{toolchain.BinDir}, searchDirs...))
	} else if profile.DefaultLinker == "lld-link" {
		toolchain.Linker = findOptionalTool("lld-link", append([]string{toolchain.BinDir}, searchDirs...))
	}
	if sysroot != "" && profile.OS == "linux" && !target.IsNative(profile) {
		toolchain.GCCToolchain, err = discoverGCCToolchain(sysroot, profile)
		if err != nil {
			return llvmToolchain{}, err
		}
	}
	return toolchain, nil
}

func llvmSearchDirectories() []string {
	var directories []string
	if prefix := strings.TrimSpace(os.Getenv("MIRURI_LLVM_PREFIX")); prefix != "" {
		directories = append(directories, filepath.Join(prefix, "bin"))
	}
	if runtime.GOOS == "darwin" {
		directories = append(directories,
			"/opt/homebrew/opt/llvm/bin",
			"/usr/local/opt/llvm/bin",
		)
	}
	seen := make(map[string]bool)
	var unique []string
	for _, directory := range directories {
		if directory == "" || seen[directory] {
			continue
		}
		seen[directory] = true
		unique = append(unique, directory)
	}
	return unique
}

func findTool(name string, directories []string) (string, error) {
	if path := findOptionalTool(name, directories); path != "" {
		return path, nil
	}
	return "", fmt.Errorf("required tool %s is not available; install LLVM or set MIRURI_LLVM_PREFIX", name)
}

func findOptionalTool(name string, directories []string) string {
	for _, directory := range directories {
		candidate := filepath.Join(directory, executableName(name))
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			if absolute, err := filepath.Abs(candidate); err == nil {
				return absolute
			}
			return candidate
		}
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}

func executableName(name string) string {
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		return name + ".exe"
	}
	return name
}

func discoverGCCToolchain(sysroot string, profile model.TargetProfile) (string, error) {
	prefixes := []string{
		filepath.Join(sysroot, "usr"),
		filepath.Join(sysroot, "usr", "local"),
		sysroot,
	}
	preferred := gccTargetDirectory(profile)
	type candidate struct {
		prefix  string
		path    string
		version string
		score   int
	}
	var candidates []candidate
	for _, prefix := range prefixes {
		matches, _ := filepath.Glob(filepath.Join(prefix, "lib", "gcc", "*", "*", "libgcc.a"))
		for _, match := range matches {
			score := 1
			parts := strings.Split(filepath.ToSlash(match), "/")
			if len(parts) >= 3 && preferred != "" && parts[len(parts)-3] == preferred {
				score = 3
			} else if preferred != "" && strings.Contains(filepath.ToSlash(match), "/"+preferred+"/") {
				score = 2
			}
			candidates = append(candidates, candidate{
				prefix:  prefix,
				path:    match,
				version: filepath.Base(filepath.Dir(match)),
				score:   score,
			})
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("sysroot %s contains no GCC runtime installation (expected lib/gcc/<target>/<version>/libgcc.a)", sysroot)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if comparison := compareVersion(candidates[i].version, candidates[j].version); comparison != 0 {
			return comparison > 0
		}
		return candidates[i].path > candidates[j].path
	})
	if preferred != "" && candidates[0].score < 2 {
		return "", fmt.Errorf("sysroot %s has a GCC runtime, but none matches target %s", sysroot, preferred)
	}
	return candidates[0].prefix, nil
}

func compareVersion(left, right string) int {
	leftParts := numericVersionParts(left)
	rightParts := numericVersionParts(right)
	length := len(leftParts)
	if len(rightParts) > length {
		length = len(rightParts)
	}
	for index := 0; index < length; index++ {
		leftValue := 0
		if index < len(leftParts) {
			leftValue = leftParts[index]
		}
		rightValue := 0
		if index < len(rightParts) {
			rightValue = rightParts[index]
		}
		if leftValue < rightValue {
			return -1
		}
		if leftValue > rightValue {
			return 1
		}
	}
	return strings.Compare(left, right)
}

func numericVersionParts(value string) []int {
	parts := strings.FieldsFunc(value, func(char rune) bool { return char < '0' || char > '9' })
	values := make([]int, 0, len(parts))
	for _, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err == nil {
			values = append(values, parsed)
		}
	}
	return values
}

func gccTargetDirectory(profile model.TargetProfile) string {
	switch profile.Arch {
	case "x86_64":
		return "x86_64-linux-gnu"
	case "arm64":
		return "aarch64-linux-gnu"
	case "ppc64le":
		return "powerpc64le-linux-gnu"
	case "riscv64":
		return "riscv64-linux-gnu"
	case "riscv32":
		return "riscv32-linux-gnu"
	default:
		return ""
	}
}

func (toolchain llvmToolchain) provenance() model.ToolchainProvenance {
	return model.ToolchainProvenance{
		CCompiler:    toolchain.CC,
		CXXCompiler:  toolchain.CXX,
		Archiver:     toolchain.AR,
		Ranlib:       toolchain.Ranlib,
		Strip:        toolchain.Strip,
		Linker:       toolchain.Linker,
		GCCToolchain: toolchain.GCCToolchain,
	}
}
