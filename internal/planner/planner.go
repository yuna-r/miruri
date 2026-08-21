package planner

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/yuna-r/miruri/internal/model"
	"github.com/yuna-r/miruri/internal/target"
)

type Options struct {
	Sysroot          string
	AutomaticSysroot bool
}

func Create(analysis model.AnalysisReport, targetProfile model.TargetProfile, sysroot string) model.PortingPlan {
	return CreateWithOptions(analysis, targetProfile, Options{Sysroot: sysroot})
}

func CreateWithOptions(analysis model.AnalysisReport, targetProfile model.TargetProfile, options Options) model.PortingPlan {
	provided := make(map[string]bool, len(targetProfile.Capabilities))
	for _, capability := range targetProfile.Capabilities {
		provided[capability] = true
	}

	plan := model.PortingPlan{
		SchemaVersion: "miruri.plan.v1",
		GeneratedAt:   time.Now().UTC(),
		ProjectName:   analysis.ProjectName,
		Target:        targetProfile,
		Status:        "ready",
	}

	for _, requirement := range analysis.Requirements {
		item := selectStrategy(requirement, targetProfile, provided)
		plan.Items = append(plan.Items, item)
		if item.Blocking {
			plan.Status = "blocked"
		} else if plan.Status == "ready" && item.Strategy != model.StrategyNativeRebuild {
			plan.Status = "review"
		}
	}

	plan.Environment = environmentRequirements(analysis, targetProfile, options)
	for _, requirement := range plan.Environment {
		if requirement.Required && strings.Contains(strings.ToLower(requirement.Reason), "missing") {
			if plan.Status == "ready" {
				plan.Status = "review"
			}
		}
	}

	sort.Slice(plan.Items, func(i, j int) bool { return plan.Items[i].Requirement < plan.Items[j].Requirement })
	return plan
}

func selectStrategy(req model.CapabilityRequirement, targetProfile model.TargetProfile, provided map[string]bool) model.PlanItem {
	if provided[req.ID] {
		return model.PlanItem{
			Requirement: req.ID,
			Strategy:    model.StrategyNativeRebuild,
			Provider:    req.ID,
			Reason:      "The target contract provides this capability directly.",
		}
	}

	item := model.PlanItem{Requirement: req.ID, Strategy: model.StrategyReview, Reason: "No direct provider was selected; inspect the referenced portability island."}
	switch {
	case req.ID == "cpu.x86.intrinsics":
		if targetProfile.Arch == "x86_64" {
			item.Strategy = model.StrategyNativeRebuild
			item.Provider = "target-x86-intrinsics"
			item.Reason = "The target is x86_64; retain the optimized path and verify feature guards."
		} else {
			item.Strategy = model.StrategySourceRewrite
			item.Provider = "portable-c-fallback"
			item.Reason = "Retain the x86 path under feature guards and generate a portable C fallback before target-specific optimization."
		}
	case req.ID == "cpu.arm.neon":
		if targetProfile.Arch == "arm64" {
			item.Strategy = model.StrategyNativeRebuild
			item.Provider = "target-neon"
			item.Reason = "The ARM64 target can retain the NEON path."
		} else {
			item.Strategy = model.StrategySourceRewrite
			item.Provider = "portable-c-fallback"
			item.Reason = "Generate a portable fallback and preserve the NEON path for ARM targets."
		}
	case req.ID == "cpu.inline-assembly":
		item.Strategy = model.StrategySourceRewrite
		item.Provider = "semantic-lowering"
		item.Reason = "Inline assembly must be classified and lowered to a portable operation or a target implementation."
		item.Blocking = req.Hard
	case req.ID == "gui.win32":
		if targetProfile.OS == "windows" {
			item.Strategy = model.StrategyNativeRebuild
			item.Provider = "win32"
			item.Reason = "The target provides Win32 GUI APIs."
		} else {
			item.Strategy = model.StrategyGeneratedAdapter
			item.Provider = nativeGUIProvider(targetProfile)
			item.Reason = "Recover the window/event contract and generate a target GUI adapter; pixel-fidelity and native-UI policies remain distinct."
		}
	case req.ID == "gui.appkit":
		if targetProfile.OS == "darwin" {
			item.Strategy = model.StrategyNativeRebuild
			item.Provider = "appkit"
			item.Reason = "The target provides AppKit."
		} else {
			item.Strategy = model.StrategyGeneratedAdapter
			item.Provider = nativeGUIProvider(targetProfile)
			item.Reason = "Generate an adapter from the recovered GUI interaction contract."
		}
	case req.ID == "gui.gtk" || req.ID == "gui.sdl":
		item.Strategy = model.StrategyNativeRebuild
		item.Provider = strings.TrimPrefix(req.ID, "gui.")
		item.Reason = "Rebuild the portable toolkit and its dependencies for the target."
	case strings.HasPrefix(req.ID, "graphics.d3d"):
		if targetProfile.OS == "windows" {
			item.Strategy = model.StrategyNativeRebuild
			item.Provider = req.ID
			item.Reason = "Direct3D is native on the Windows target."
		} else {
			item.Strategy = model.StrategyAPITranslation
			item.Provider = "graphics-translation-provider"
			item.Reason = "Select a declared Direct3D translation provider or synthesize a renderer backend; do not perform textual API substitution."
		}
	case req.ID == "graphics.vulkan":
		if targetProfile.OS == "darwin" {
			item.Strategy = model.StrategyAPITranslation
			item.Provider = "vulkan-portability-provider"
			item.Reason = "Use a Vulkan portability provider and validate the required feature subset against Metal."
		} else {
			item.Strategy = model.StrategyNativeRebuild
			item.Provider = "vulkan-loader"
			item.Reason = "Rebuild against the target Vulkan SDK/loader."
		}
	case req.ID == "graphics.opengl":
		if targetProfile.OS == "darwin" {
			item.Strategy = model.StrategyReview
			item.Provider = "opengl-or-modern-backend"
			item.Reason = "OpenGL exists on macOS but is a legacy path; policy must choose preservation or a modern backend."
		} else {
			item.Strategy = model.StrategyNativeRebuild
			item.Provider = "opengl"
			item.Reason = "Rebuild against the target OpenGL implementation."
		}
	case req.ID == "graphics.metal":
		item.Strategy = model.StrategySourceRewrite
		item.Provider = nativeGraphicsProvider(targetProfile)
		item.Reason = "A Metal-only renderer needs a target renderer backend or a portable graphics abstraction."
	case strings.HasPrefix(req.ID, "shader."):
		item.Strategy = model.StrategySourceRewrite
		item.Provider = "shader-pipeline"
		item.Reason = "Compile through the shader pipeline with reflection, feature validation and resource-binding remapping."
	case strings.HasPrefix(req.ID, "audio."):
		item.Strategy = model.StrategyGeneratedAdapter
		item.Provider = nativeAudioProvider(targetProfile)
		item.Reason = "Map the recovered audio device/stream contract to the target audio provider."
	case strings.HasPrefix(req.ID, "input."):
		item.Strategy = model.StrategyGeneratedAdapter
		item.Provider = nativeInputProvider(targetProfile)
		item.Reason = "Map keyboard, text, pointer and gamepad semantics separately to the target input provider."
	case req.ID == "network.winsock" && targetProfile.OS != "windows":
		item.Strategy = model.StrategyGeneratedAdapter
		item.Provider = "bsd-sockets"
		item.Reason = "Generate a narrow WinSock-to-BSD sockets adapter around the observed operations."
	case req.ID == "network.bsd-sockets" && targetProfile.OS == "windows":
		item.Strategy = model.StrategyGeneratedAdapter
		item.Provider = "winsock"
		item.Reason = "Generate a BSD sockets compatibility adapter over WinSock."
	case req.ID == "threads.win32" && targetProfile.OS != "windows":
		item.Strategy = model.StrategyGeneratedAdapter
		item.Provider = "posix-threads"
		item.Reason = "Map the threading contract to POSIX primitives and preserve synchronization semantics."
	case req.ID == "threads.posix" && targetProfile.OS == "windows":
		item.Strategy = model.StrategyCompatibilityRuntime
		item.Provider = "portable-thread-runtime"
		item.Reason = "Use a portable threading runtime or generate a constrained Win32 implementation."
	case req.ID == "dependency.binary-only":
		item.Strategy = model.StrategyUnresolved
		item.Provider = "license-and-architecture-resolver"
		item.Reason = "The binary must be inspected for CPU, OS, ABI, redistribution rights and available source or replacement providers."
		item.Blocking = true
	case strings.HasPrefix(req.ID, "plugin."):
		item.Strategy = model.StrategyGeneratedAdapter
		item.Provider = "plugin-contract"
		item.Reason = "Record export symbols, ABI, naming and runtime discovery before rebuilding each plugin for the target."
	case strings.HasPrefix(req.ID, "resource.") || strings.HasPrefix(req.ID, "asset."):
		item.Strategy = model.StrategyNativeRebuild
		item.Provider = "resource-pipeline"
		item.Reason = "Treat resources and assets as first-class content artifacts and package them with the target output."
	}
	return item
}

func environmentRequirements(analysis model.AnalysisReport, p model.TargetProfile, options Options) []model.EnvironmentRequirement {
	buildTools := map[model.BuildSystem]string{
		model.BuildSystemCMake:     "cmake and a build backend such as Ninja",
		model.BuildSystemMake:      "make",
		model.BuildSystemMeson:     "meson and Ninja",
		model.BuildSystemAutotools: "autoconf/configure tooling with a preseeded cross cache",
	}
	var requirements []model.EnvironmentRequirement
	for _, system := range analysis.BuildSystems {
		if tool, ok := buildTools[system]; ok {
			requirements = append(requirements, model.EnvironmentRequirement{Name: string(system), Required: true, Reason: tool})
		}
	}
	requirements = append(requirements, model.EnvironmentRequirement{Name: "clang", Required: true, Reason: "C/C++ frontend and target code generation"})
	if p.DefaultLinker != "" {
		requirements = append(requirements, model.EnvironmentRequirement{Name: p.DefaultLinker, Required: true, Reason: "target linker"})
	}
	if p.RequiresSysroot && !target.IsNative(p) {
		reason := "target headers, C runtime objects and libraries"
		if options.Sysroot == "" {
			if options.AutomaticSysroot {
				reason = "trusted managed sysroot provider is available and will be provisioned during build"
			} else {
				reason = "missing: target headers, C runtime objects and libraries; use an automatic provider, pass --sysroot, or set MIRURI_SYSROOT_<TARGET>"
			}
		}
		requirements = append(requirements, model.EnvironmentRequirement{Name: "sysroot", Required: true, Reason: reason})
	}
	if p.RequiresPlatformSDK {
		name := fmt.Sprintf("%s platform SDK", p.OS)
		reason := "required for framework/resource/link/packaging operations"
		if p.OS != runtime.GOOS {
			reason += "; use a compatible build worker"
		}
		requirements = append(requirements, model.EnvironmentRequirement{Name: name, Required: true, Reason: reason})
	}
	return requirements
}

func nativeGUIProvider(p model.TargetProfile) string {
	switch p.OS {
	case "darwin":
		return "appkit"
	case "windows":
		return "win32"
	default:
		return "gtk-or-sdl"
	}
}

func nativeGraphicsProvider(p model.TargetProfile) string {
	switch p.OS {
	case "darwin":
		return "metal"
	case "windows":
		return "d3d12-or-vulkan"
	default:
		return "vulkan"
	}
}

func nativeAudioProvider(p model.TargetProfile) string {
	switch p.OS {
	case "darwin":
		return "coreaudio"
	case "windows":
		return "wasapi"
	default:
		return "alsa-or-pipewire"
	}
}

func nativeInputProvider(p model.TargetProfile) string {
	switch p.OS {
	case "darwin":
		return "appkit-and-gamecontroller"
	case "windows":
		return "win32-rawinput-and-xinput"
	default:
		return "evdev-wayland-x11"
	}
}
