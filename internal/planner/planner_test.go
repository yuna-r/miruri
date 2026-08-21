package planner

import (
	"strings"
	"testing"

	"github.com/yuna-r/miruri/internal/model"
	"github.com/yuna-r/miruri/internal/target"
)

func TestPlanSeparatesTranslationAndBinaryBlocker(t *testing.T) {
	profile, err := target.Resolve("macos-arm64")
	if err != nil {
		t.Fatal(err)
	}
	analysis := model.AnalysisReport{
		ProjectName: "game",
		Requirements: []model.CapabilityRequirement{
			{ID: "graphics.d3d11", Domain: "graphics", Hard: true},
			{ID: "dependency.binary-only", Domain: "dependency", Hard: true},
		},
	}
	plan := Create(analysis, profile, "")
	if plan.Status != "blocked" {
		t.Fatalf("expected blocked plan, got %s", plan.Status)
	}
	strategies := map[string]model.StrategyKind{}
	for _, item := range plan.Items {
		strategies[item.Requirement] = item.Strategy
	}
	if strategies["graphics.d3d11"] != model.StrategyAPITranslation {
		t.Fatalf("unexpected Direct3D strategy: %s", strategies["graphics.d3d11"])
	}
	if strategies["dependency.binary-only"] != model.StrategyUnresolved {
		t.Fatalf("unexpected binary dependency strategy: %s", strategies["dependency.binary-only"])
	}
}

func TestAutomaticSysrootProviderRemovesMissingEnvironmentWarning(t *testing.T) {
	profile, err := target.Resolve("linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	analysis := model.AnalysisReport{ProjectName: "fixture", BuildSystems: []model.BuildSystem{model.BuildSystemCMake}}
	plan := CreateWithOptions(analysis, profile, Options{AutomaticSysroot: true})
	for _, requirement := range plan.Environment {
		if requirement.Name == "sysroot" {
			if requirement.Required && requirement.Reason == "" {
				t.Fatal("automatic sysroot requirement has no explanation")
			}
			if containsFold(requirement.Reason, "missing") {
				t.Fatalf("automatic sysroot was reported missing: %+v", requirement)
			}
			return
		}
	}
	t.Fatal("sysroot environment requirement missing")
}

func containsFold(value, fragment string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(fragment))
}

func TestPlanMapsCrossBuildAndPlatformCapabilities(t *testing.T) {
	profile, err := target.Resolve("linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	analysis := model.AnalysisReport{
		ProjectName: "portable-app",
		Requirements: []model.CapabilityRequirement{
			{ID: "build.native-cpu-flags", Domain: "build", Hard: true},
			{ID: "build.target-execution-probe", Domain: "build", Hard: true},
			{ID: "memory.win32-virtual", Domain: "memory", Hard: false},
			{ID: "os.windows.registry", Domain: "os-service", Hard: true},
			{ID: "cpu.arm.sve", Domain: "cpu", Hard: true},
		},
	}
	plan := CreateWithOptions(analysis, profile, Options{AutomaticSysroot: true})
	items := map[string]model.PlanItem{}
	for _, item := range plan.Items {
		items[item.Requirement] = item
	}
	checks := map[string]model.StrategyKind{
		"build.native-cpu-flags":       model.StrategySourceRewrite,
		"build.target-execution-probe": model.StrategyGeneratedAdapter,
		"memory.win32-virtual":         model.StrategyGeneratedAdapter,
		"os.windows.registry":          model.StrategyGeneratedAdapter,
		"cpu.arm.sve":                  model.StrategySourceRewrite,
	}
	for requirement, strategy := range checks {
		if items[requirement].Strategy != strategy {
			t.Errorf("%s: expected %s, got %+v", requirement, strategy, items[requirement])
		}
	}
	if plan.Status == "blocked" {
		t.Fatalf("mapped portability islands should remain actionable: %+v", plan.Items)
	}
}
