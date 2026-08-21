package sysroot

import (
	"fmt"
	"sort"

	"github.com/yuna-r/miruri/internal/model"
)

// Provider declares a trusted, architecture-specific source for a managed
// sysroot. Providers are deliberately target-centric rather than host/target
// pair mappings: OCI platform selection makes the same declaration usable from
// every Miruri host.
type Provider struct {
	ID           string `json:"id"`
	TargetID     string `json:"target_id"`
	Image        string `json:"image"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
	Description  string `json:"description"`
}

var builtinProviders = []Provider{
	{
		ID:           "docker-official-buildpack-deps-bookworm",
		TargetID:     "linux-x86_64",
		Image:        "docker.io/library/buildpack-deps:bookworm",
		OS:           "linux",
		Architecture: "amd64",
		Description:  "Docker Official Image buildpack-deps:bookworm development rootfs",
	},
	{
		ID:           "docker-official-buildpack-deps-bookworm",
		TargetID:     "linux-arm64",
		Image:        "docker.io/library/buildpack-deps:bookworm",
		OS:           "linux",
		Architecture: "arm64",
		Description:  "Docker Official Image buildpack-deps:bookworm development rootfs",
	},
	{
		ID:           "docker-official-buildpack-deps-bookworm",
		TargetID:     "linux-ppc64le",
		Image:        "docker.io/library/buildpack-deps:bookworm",
		OS:           "linux",
		Architecture: "ppc64le",
		Description:  "Docker Official Image buildpack-deps:bookworm development rootfs",
	},
	{
		ID:           "docker-official-buildpack-deps-trixie",
		TargetID:     "linux-riscv64",
		Image:        "docker.io/library/buildpack-deps:trixie",
		OS:           "linux",
		Architecture: "riscv64",
		Description:  "Docker Official Image buildpack-deps:trixie development rootfs",
	},
}

func BuiltinProviders() []Provider {
	providers := append([]Provider(nil), builtinProviders...)
	sort.Slice(providers, func(i, j int) bool {
		if providers[i].TargetID == providers[j].TargetID {
			return providers[i].ID < providers[j].ID
		}
		return providers[i].TargetID < providers[j].TargetID
	})
	return providers
}

func ProviderFor(profile model.TargetProfile) (Provider, bool) {
	return providerForID(profile.ID, builtinProviders)
}

func providerForID(targetID string, providers []Provider) (Provider, bool) {
	for _, provider := range providers {
		if provider.TargetID == targetID {
			return provider, true
		}
	}
	return Provider{}, false
}

func PlatformString(provider Provider) string {
	platform := fmt.Sprintf("%s/%s", provider.OS, provider.Architecture)
	if provider.Variant != "" {
		platform += "/" + provider.Variant
	}
	return platform
}
