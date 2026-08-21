// SPDX-License-Identifier: MPL-2.0

package builder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yuna-r/miruri/internal/codex"
)

type portFidelityBaseline struct {
	TranslationUnits map[string]struct{}
	Assets           []string
}

type compileCommandEntry struct {
	Directory string `json:"directory"`
	File      string `json:"file"`
}

func capturePortFidelityBaseline(root string, exclusions []string) (portFidelityBaseline, error) {
	baseline := portFidelityBaseline{TranslationUnits: map[string]struct{}{}}
	excluded := make([]string, 0, len(exclusions))
	for _, value := range exclusions {
		if strings.TrimSpace(value) == "" {
			continue
		}
		absolute := value
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(root, absolute)
		}
		absolute, err := filepath.Abs(absolute)
		if err != nil {
			return baseline, err
		}
		excluded = append(excluded, filepath.Clean(absolute))
	}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		absolute = filepath.Clean(absolute)
		if path != root && pathExcludedForFidelity(absolute, excluded) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path != root && conventionalGeneratedDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if isTranslationUnitExtension(ext) {
			baseline.TranslationUnits[rel] = struct{}{}
		}
		if isPortAssetExtension(ext) {
			baseline.Assets = append(baseline.Assets, rel)
		}
		return nil
	})
	if err != nil {
		return baseline, err
	}
	sort.Strings(baseline.Assets)
	return baseline, nil
}

func pathExcludedForFidelity(path string, exclusions []string) bool {
	for _, excluded := range exclusions {
		if path == excluded || strings.HasPrefix(path+string(os.PathSeparator), excluded+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func conventionalGeneratedDir(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "cmake-build-") {
		return true
	}
	switch lower {
	case ".git", ".hg", ".svn", ".codex", ".miruri", ".cache", ".vs", "dist", "build", "out", "target", "node_modules", "__pycache__":
		return true
	default:
		return false
	}
}

func isTranslationUnitExtension(ext string) bool {
	switch ext {
	case ".c", ".cc", ".cpp", ".cxx", ".m", ".mm":
		return true
	default:
		return false
	}
}

func isPortAssetExtension(ext string) bool {
	switch ext {
	case ".dds", ".sdkmesh", ".wav", ".wma", ".mp3", ".ogg", ".flac",
		".png", ".jpg", ".jpeg", ".bmp", ".tga", ".gif", ".webp",
		".obj", ".fbx", ".gltf", ".glb", ".mesh", ".x",
		".hlsl", ".glsl", ".vert", ".frag", ".geom", ".metal",
		".ttf", ".otf", ".json", ".xml":
		return true
	default:
		return false
	}
}

func (baseline portFidelityBaseline) promptSummary(limit int) string {
	if limit <= 0 {
		limit = 80
	}
	sources := make([]string, 0, len(baseline.TranslationUnits))
	for path := range baseline.TranslationUnits {
		sources = append(sources, path)
	}
	sort.Strings(sources)
	var out strings.Builder
	fmt.Fprintf(&out, "Original translation units: %d\n", len(sources))
	for _, path := range truncatePathList(sources, limit) {
		fmt.Fprintf(&out, "  - %s\n", path)
	}
	if len(sources) > limit {
		fmt.Fprintf(&out, "  - ... %d more\n", len(sources)-limit)
	}
	fmt.Fprintf(&out, "Original asset/shader files: %d\n", len(baseline.Assets))
	for _, path := range truncatePathList(baseline.Assets, limit) {
		fmt.Fprintf(&out, "  - %s\n", path)
	}
	if len(baseline.Assets) > limit {
		fmt.Fprintf(&out, "  - ... %d more\n", len(baseline.Assets)-limit)
	}
	return strings.TrimSpace(out.String())
}

func truncatePathList(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

func (bc *buildContext) validatePortFidelityAfterBuild() error {
	if len(bc.codexRepairs) == 0 || (bc.config.CodexMode != codex.TaskPort && bc.config.CodexMode != codex.TaskAuto) {
		return nil
	}
	latest := bc.codexRepairs[len(bc.codexRepairs)-1]
	var violations []string
	switch latest.Status {
	case "blocked", "no-change":
		violations = append(violations, fmt.Sprintf("latest Codex port attempt is incomplete (status %q); another implementation attempt is required", latest.Status))
	}
	blockingRisks := 0
	advisoryRisks := 0
	for _, risk := range latest.RemainingRisks {
		risk = strings.TrimSpace(risk)
		if risk == "" {
			continue
		}
		if nonBlockingPortCaveat(risk) {
			advisoryRisks++
			bc.logf("Miruri fidelity note: non-blocking Codex caveat after successful rebuild: %s\n", risk)
			continue
		}
		// remaining_risks is a structured field whose contract is narrowed by the
		// Codex prompt to project-relevant unresolved fidelity blockers. Treat an
		// unknown entry conservatively as blocking instead of guessing from prose.
		blockingRisks++
		violations = append(violations, "Codex declared unresolved project-relevant risk: "+risk)
	}
	for _, assumption := range latest.Assumptions {
		if declaredFeatureLoss(assumption) && !nonBlockingPortCaveat(assumption) {
			blockingRisks++
			violations = append(violations, "Codex declared unresolved project-relevant feature loss in assumptions: "+assumption)
		}
	}
	if latest.Status == "progress" && blockingRisks == 0 {
		bc.logf("Miruri port completion: promoted Codex status \"progress\" after successful rebuild/fidelity checks; no project-relevant unresolved feature-loss risk remains (%d advisory caveat(s)).\n", advisoryRisks)
	}

	reused, compiled, checked, err := bc.compiledOriginalTranslationUnits()
	if err != nil {
		return fmt.Errorf("feature-fidelity gate could not inspect compiled sources: %w", err)
	}
	if checked {
		required := minimumOriginalTranslationUnits(len(bc.fidelityBaseline.TranslationUnits))
		if required > 0 && reused < required {
			violations = append(violations, fmt.Sprintf(
				"target build compiles %d pre-existing translation unit(s) out of %d original source file(s); at least %d must remain in the target build (compiled translation units total: %d)",
				reused, len(bc.fidelityBaseline.TranslationUnits), required, compiled,
			))
		}
	}
	if len(violations) == 0 {
		if checked {
			bc.logf("Miruri feature-fidelity gate: PASS; target build reuses %d/%d original translation unit(s).\n", reused, len(bc.fidelityBaseline.TranslationUnits))
		} else {
			bc.logf("Miruri feature-fidelity gate: PASS; compile database unavailable for %s, declared feature-loss claims only were checked.\n", bc.buildSystem)
		}
		return nil
	}
	var message strings.Builder
	message.WriteString("Miruri feature-fidelity gate rejected linked artifact")
	for _, violation := range violations {
		message.WriteString("\n- ")
		message.WriteString(violation)
	}
	message.WriteString("\nA Miruri port must preserve the original product behavior and shipped content semantics. Target-native orchestration or source restructuring is allowed when platform coupling makes direct reuse impractical; simplified, procedural, placeholder, or look-alike behavior is not a valid port.")
	return fmt.Errorf("%s", message.String())
}

func (bc *buildContext) compiledOriginalTranslationUnits() (reused int, compiled int, checked bool, err error) {
	compileDB := filepath.Join(bc.buildDir, "compile_commands.json")
	data, readErr := os.ReadFile(compileDB)
	if os.IsNotExist(readErr) {
		return 0, 0, false, nil
	}
	if readErr != nil {
		return 0, 0, false, readErr
	}
	var entries []compileCommandEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return 0, 0, false, fmt.Errorf("parse %s: %w", compileDB, err)
	}
	compiledPaths := map[string]struct{}{}
	reusedPaths := map[string]struct{}{}
	for _, entry := range entries {
		if strings.TrimSpace(entry.File) == "" {
			continue
		}
		path := entry.File
		if !filepath.IsAbs(path) {
			directory := entry.Directory
			if directory == "" {
				directory = bc.buildDir
			}
			path = filepath.Join(directory, path)
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return 0, 0, false, err
		}
		rel, relErr := filepath.Rel(bc.sourceDir, path)
		if relErr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			continue
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		if !isTranslationUnitExtension(strings.ToLower(filepath.Ext(rel))) {
			continue
		}
		compiledPaths[rel] = struct{}{}
		if _, ok := bc.fidelityBaseline.TranslationUnits[rel]; ok {
			reusedPaths[rel] = struct{}{}
		}
	}
	return len(reusedPaths), len(compiledPaths), true, nil
}

func minimumOriginalTranslationUnits(total int) int {
	switch {
	case total >= 30:
		return 3
	case total >= 10:
		return 2
	case total >= 1:
		return 1
	default:
		return 0
	}
}

func declaredFeatureLoss(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"approximat", "placeholder", "stand-in", "stand in", "stubbed", "stub ",
		"not implemented", "unimplemented", "does not yet", "doesn't yet", "not yet",
		"not played", "not rendered", "not decoded", "does not decode", "doesn't decode",
		"omitted", "disabled", "dropped", "closest locally implementable", "substitute implementation",
		"unsupported", "not supported", "cannot reproduce", "cannot preserve", "cannot match",
		"missing", "does not preserve", "fails to preserve", "behavior differs", "behaviour differs",
		"semantics differ", "differs from the original",
		"未実装", "未対応", "未サポート", "省略", "無効化", "代替", "近似", "欠落",
		"再現できません", "再現できない", "維持できません", "維持できない", "完全には再現",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// nonBlockingPortCaveat recognizes caveats that may be useful to report but do
// not mean the just-built target artifact is an incomplete port. This function
// is intentionally narrow: unknown feature-loss statements remain blocking.
func nonBlockingPortCaveat(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}

	// Miruri deliberately does not execute target artifacts. Runtime/real-device
	// validation therefore remains an assurance note rather than a port blocker.
	for _, marker := range []string{
		"not executed", "was not executed", "not run", "not runtime-tested",
		"runtime validation", "runtime behavior", "real-device", "real device",
		"not validated on hardware", "not verified on hardware", "untested on hardware",
		"未実行", "未検証", "実機検証", "実機挙動",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	// Performance/efficiency opportunities are not feature loss.
	for _, marker := range []string{
		"optimization", "optimisation", "optimize", "optimise", "performance opportunity",
		"performance could", "could be faster", "efficiency", "最適化", "性能改善", "高速化",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	// Source-topology or architectural duplication by itself is not a product
	// fidelity failure. Platform-coupled orchestration may need to be re-expressed
	// in a native adapter/controller while preserving the original state machine,
	// calculations, content semantics, and observable behavior. Do not force
	// endless refactoring merely to make the target source tree resemble the
	// source-platform class layout. Explicit behavioral loss still blocks.
	if structuralPortDebtCaveat(lower) && !declaredFeatureLoss(lower) {
		return true
	}

	// An optional physical capability being absent from some machines is an
	// environment limitation, not an implementation omission.
	hardwareAbsence := []string{
		"without a controller", "without controller", "no controller", "controller is unavailable",
		"hardware is unavailable", "hardware unavailable", "device is unavailable",
		"does not have", "doesn't have", "not present on",
		"controllerがない", "コントローラがない", "ハードウェアがない", "非搭載",
	}
	for _, marker := range hardwareAbsence {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	// Codex works inside an artifact-only sandbox and may be unable to reuse the
	// build directory. validatePortFidelityAfterBuild runs only after Miruri has
	// successfully rebuilt the accepted changes, so such verification caveats
	// have already been superseded by stronger evidence.
	hasBuildWord := containsAny(lower, []string{
		"relink", "re-link", "rebuild", "build directory", "build dir",
		"再リンク", "再ビルド", "build directory", "ビルドディレクトリ",
	})
	hasVerificationWord := containsAny(lower, []string{
		"permission", "read-only", "read only", "not verified", "unverified", "not confirmed",
		"書き込み権限", "権限", "未確認", "確認でき",
	})
	if hasBuildWord && hasVerificationWord {
		return true
	}

	return false
}

func structuralPortDebtCaveat(lower string) bool {
	if lower == "" {
		return false
	}
	markers := []string{
		"duplicates substantial orchestration",
		"duplicates orchestration",
		"duplicate orchestration",
		"duplicates logic",
		"duplicate logic",
		"code duplication",
		"source duplication",
		"structural duplication",
		"source-topology",
		"source topology",
		"architectural debt",
		"refactoring opportunity",
		"could be refactored",
		"can be refactored",
		"not reused directly",
		"is not reused directly",
		"remain unported to native",
		"remains unported to native",
		"original implementation remains unported",
		"original implementations remain unported",
		"重複して",
		"重複実装",
		"コード重複",
		"直接再利用していない",
		"直接再利用されていない",
		"構造上",
		"リファクタリング余地",
	}
	return containsAny(lower, markers)
}

func containsAny(text string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
