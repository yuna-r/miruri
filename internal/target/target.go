package target

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"runtime"
	"sort"
	"strings"

	"github.com/yuna-r/miruri/internal/model"
)

//go:embed profiles/*.json
var profileFS embed.FS

func List() ([]model.TargetProfile, error) {
	entries, err := fs.ReadDir(profileFS, "profiles")
	if err != nil {
		return nil, fmt.Errorf("read embedded target profiles: %w", err)
	}
	var profiles []model.TargetProfile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := profileFS.ReadFile("profiles/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read target profile %s: %w", entry.Name(), err)
		}
		var profile model.TargetProfile
		if err := json.Unmarshal(data, &profile); err != nil {
			return nil, fmt.Errorf("parse target profile %s: %w", entry.Name(), err)
		}
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })
	return profiles, nil
}

func Resolve(id string) (model.TargetProfile, error) {
	if id == "" || id == "host" {
		return Host()
	}
	profiles, err := List()
	if err != nil {
		return model.TargetProfile{}, err
	}
	for _, profile := range profiles {
		if profile.ID == id {
			return profile, nil
		}
	}
	var available []string
	for _, profile := range profiles {
		available = append(available, profile.ID)
	}
	return model.TargetProfile{}, fmt.Errorf("unknown target %q; available targets: %s", id, strings.Join(available, ", "))
}

func Host() (model.TargetProfile, error) {
	id := hostID(runtime.GOOS, runtime.GOARCH)
	if id == "" {
		return model.TargetProfile{}, fmt.Errorf("unsupported Miruri host: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	profiles, err := List()
	if err != nil {
		return model.TargetProfile{}, err
	}
	for _, profile := range profiles {
		if profile.ID == id {
			profile.RequiresSysroot = false
			return profile, nil
		}
	}
	return model.TargetProfile{}, fmt.Errorf("host target profile %s is missing", id)
}

func IsNative(profile model.TargetProfile) bool {
	return normalizeOS(profile.OS) == normalizeOS(runtime.GOOS) && normalizeArch(profile.Arch) == normalizeArch(runtime.GOARCH)
}

func normalizeOS(osName string) string {
	switch strings.ToLower(osName) {
	case "macos", "darwin":
		return "darwin"
	default:
		return strings.ToLower(osName)
	}
}

func normalizeArch(arch string) string {
	switch strings.ToLower(arch) {
	case "amd64", "x86-64", "x64":
		return "x86_64"
	case "aarch64":
		return "arm64"
	case "powerpc64le":
		return "ppc64le"
	default:
		return strings.ToLower(arch)
	}
}

func hostID(goos, goarch string) string {
	switch goos + "/" + goarch {
	case "darwin/arm64":
		return "macos-arm64"
	case "darwin/amd64":
		return "macos-x86_64"
	case "linux/amd64":
		return "linux-x86_64"
	case "linux/arm64":
		return "linux-arm64"
	case "windows/amd64":
		return "windows-x86_64"
	case "windows/arm64":
		return "windows-arm64"
	default:
		return ""
	}
}
