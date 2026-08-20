package planner

import (
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
