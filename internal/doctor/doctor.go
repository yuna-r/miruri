package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/yuna-r/miruri/internal/model"
)

type Check struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Found    bool   `json:"found"`
	Path     string `json:"path,omitempty"`
	Purpose  string `json:"purpose"`
}

type Report struct {
	HostOS   string  `json:"host_os"`
	HostArch string  `json:"host_arch"`
	Checks   []Check `json:"checks"`
	Ready    bool    `json:"ready"`
}

func Run() Report {
	definitions := []Check{
		{Name: "git", Required: true, Purpose: "source control and patch review"},
		{Name: "clang", Required: true, Purpose: "C frontend and target code generation"},
		{Name: "clang++", Required: true, Purpose: "C++ frontend and target code generation"},
		{Name: "cmake", Required: false, Purpose: "CMake projects"},
		{Name: "meson", Required: false, Purpose: "Meson projects; Miruri can provision a managed runtime when Python 3.10+ is available"},
		{Name: "python3", Required: false, Purpose: "managed Meson runtime and Python-based ports"},
		{Name: "ninja", Required: false, Purpose: "preferred CMake build backend"},
		{Name: "make", Required: false, Purpose: "Make and Autotools projects"},
		{Name: "autoreconf", Required: false, Purpose: "bootstrap Autotools Git source trees"},
		{Name: "pkg-config", Required: false, Purpose: "Autotools dependency discovery"},
		{Name: "llvm-ar", Required: false, Purpose: "cross-target static archives"},
		{Name: "llvm-ranlib", Required: false, Purpose: "cross-target archive index"},
		{Name: "llvm-strip", Required: false, Purpose: "target artifact stripping"},
		{Name: "ld.lld", Required: false, Purpose: "cross-Linux linking"},
		{Name: "codex", Required: false, Purpose: "constrained portability repair agent"},
		{Name: "docker", Required: false, Purpose: "isolated build worker; not required for managed sysroots"},
	}
	if runtime.GOOS == "darwin" {
		definitions = append(definitions, Check{Name: "xcrun", Required: true, Purpose: "Apple SDK and tool discovery"})
	}
	if runtime.GOOS == "windows" {
		definitions = append(definitions, Check{Name: "cl", Required: false, Purpose: "MSVC target builds"})
	}

	report := Report{HostOS: runtime.GOOS, HostArch: runtime.GOARCH, Ready: true}
	for _, check := range definitions {
		path, err := lookTool(check.Name)
		check.Found = err == nil
		if check.Found {
			check.Path = path
		}
		if check.Required && !check.Found {
			report.Ready = false
		}
		report.Checks = append(report.Checks, check)
	}
	if runtime.GOOS == "darwin" {
		sdk := Check{Name: "macos-sdk", Required: true, Purpose: "usable macOS SDK for native artifact linking"}
		if output, err := exec.Command("xcrun", "--sdk", "macosx", "--show-sdk-path").Output(); err == nil {
			sdk.Path = strings.TrimSpace(string(output))
			if info, statErr := os.Stat(sdk.Path); statErr == nil && info.IsDir() {
				sdk.Found = true
			}
		}
		if !sdk.Found {
			report.Ready = false
		}
		report.Checks = append(report.Checks, sdk)
	}
	sort.SliceStable(report.Checks, func(i, j int) bool {
		if report.Checks[i].Required != report.Checks[j].Required {
			return report.Checks[i].Required
		}
		return report.Checks[i].Name < report.Checks[j].Name
	})
	return report
}

func TargetNotes(profile model.TargetProfile) []string {
	var notes []string
	if profile.RequiresSysroot {
		notes = append(notes, "A target sysroot is required when the target differs from the current host; trusted Linux profiles can be provisioned automatically.")
	}
	if profile.RequiresPlatformSDK {
		notes = append(notes, "A matching platform SDK and build worker are required.")
	}
	return notes
}

func lookTool(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	if runtime.GOOS == "darwin" {
		for _, directory := range []string{"/opt/homebrew/opt/llvm/bin", "/usr/local/opt/llvm/bin"} {
			candidate := filepath.Join(directory, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", exec.ErrNotFound
}
