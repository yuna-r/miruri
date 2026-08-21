package model

import "time"

type BuildSystem string

const (
	BuildSystemCMake     BuildSystem = "cmake"
	BuildSystemMake      BuildSystem = "make"
	BuildSystemMeson     BuildSystem = "meson"
	BuildSystemAutotools BuildSystem = "autotools"
	BuildSystemUnknown   BuildSystem = "unknown"
)

type ProjectGraph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

type GraphNode struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Name     string            `json:"name"`
	Path     string            `json:"path,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

type Evidence struct {
	Path   string `json:"path"`
	Line   int    `json:"line,omitempty"`
	RuleID string `json:"rule_id"`
	Detail string `json:"detail,omitempty"`
}

type CapabilityRequirement struct {
	ID          string     `json:"id"`
	Domain      string     `json:"domain"`
	Description string     `json:"description"`
	Hard        bool       `json:"hard"`
	Evidence    []Evidence `json:"evidence"`
}

type AnalysisReport struct {
	SchemaVersion  string                  `json:"schema_version"`
	GeneratedAt    time.Time               `json:"generated_at"`
	ProjectName    string                  `json:"project_name"`
	ProjectPath    string                  `json:"project_path"`
	ProjectDigest  string                  `json:"project_digest,omitempty"`
	ProjectEntries int                     `json:"project_entries,omitempty"`
	ProjectBytes   int64                   `json:"project_bytes,omitempty"`
	BuildSystems   []BuildSystem           `json:"build_systems"`
	Languages      map[string]int          `json:"languages"`
	FileCount      int                     `json:"file_count"`
	TextFileCount  int                     `json:"text_file_count"`
	BinaryCount    int                     `json:"binary_count"`
	Requirements   []CapabilityRequirement `json:"requirements"`
	Graph          ProjectGraph            `json:"graph"`
	Warnings       []string                `json:"warnings,omitempty"`
}

type TargetProfile struct {
	ID                  string   `json:"id"`
	DisplayName         string   `json:"display_name"`
	OS                  string   `json:"os"`
	Arch                string   `json:"arch"`
	Triple              string   `json:"triple"`
	ObjectFormat        string   `json:"object_format"`
	CMakeSystemName     string   `json:"cmake_system_name"`
	CMakeProcessor      string   `json:"cmake_processor"`
	DefaultLinker       string   `json:"default_linker"`
	RequiresSysroot     bool     `json:"requires_sysroot"`
	RequiresPlatformSDK bool     `json:"requires_platform_sdk"`
	Capabilities        []string `json:"capabilities"`
}

type StrategyKind string

const (
	StrategyNativeRebuild        StrategyKind = "native-rebuild"
	StrategySourceRewrite        StrategyKind = "source-rewrite"
	StrategyGeneratedAdapter     StrategyKind = "generated-adapter"
	StrategyCompatibilityRuntime StrategyKind = "compatibility-runtime"
	StrategyAPITranslation       StrategyKind = "api-translation"
	StrategyReplacement          StrategyKind = "replacement-dependency"
	StrategyReview               StrategyKind = "manual-review"
	StrategyUnresolved           StrategyKind = "unresolved"
)

type PlanItem struct {
	Requirement string       `json:"requirement"`
	Strategy    StrategyKind `json:"strategy"`
	Provider    string       `json:"provider,omitempty"`
	Reason      string       `json:"reason"`
	Blocking    bool         `json:"blocking"`
}

type EnvironmentRequirement struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Reason   string `json:"reason"`
}

type PortingPlan struct {
	SchemaVersion string                   `json:"schema_version"`
	GeneratedAt   time.Time                `json:"generated_at"`
	ProjectName   string                   `json:"project_name"`
	Target        TargetProfile            `json:"target"`
	Status        string                   `json:"status"`
	Items         []PlanItem               `json:"items"`
	Environment   []EnvironmentRequirement `json:"environment"`
	Warnings      []string                 `json:"warnings,omitempty"`
}

type ArtifactAssurance string

const (
	AssuranceGenerated          ArtifactAssurance = "generated"
	AssuranceLinked             ArtifactAssurance = "linked"
	AssurancePackaged           ArtifactAssurance = "packaged"
	AssuranceStaticValidated    ArtifactAssurance = "statically-validated"
	AssuranceRuntimeUnvalidated ArtifactAssurance = "runtime-unvalidated"
)

type ArtifactInfo struct {
	SourcePath     string   `json:"source_path"`
	PackagedPath   string   `json:"packaged_path"`
	PackagePath    string   `json:"package_path,omitempty"`
	Format         string   `json:"format"`
	Architecture   string   `json:"architecture"`
	Kind           string   `json:"kind"`
	Size           int64    `json:"size"`
	SHA256         string   `json:"sha256"`
	Dependencies   []string `json:"dependencies,omitempty"`
	ArchitectureOK bool     `json:"architecture_ok"`
	Notes          []string `json:"notes,omitempty"`
}

type DiscardedChange struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type CodexUsage struct {
	InputTokens           int64 `json:"input_tokens,omitempty"`
	CachedInputTokens     int64 `json:"cached_input_tokens,omitempty"`
	OutputTokens          int64 `json:"output_tokens,omitempty"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens,omitempty"`
}

type CodexRepairAttempt struct {
	Attempt             int               `json:"attempt"`
	Mode                string            `json:"mode,omitempty"`
	Status              string            `json:"status"`
	DurationMillis      int64             `json:"duration_ms"`
	ThreadID            string            `json:"thread_id,omitempty"`
	TurnID              string            `json:"turn_id,omitempty"`
	Summary             string            `json:"summary,omitempty"`
	ChangedFiles        []string          `json:"changed_files,omitempty"`
	Assumptions         []string          `json:"assumptions,omitempty"`
	RemainingRisks      []string          `json:"remaining_risks,omitempty"`
	PromptFile          string            `json:"prompt_file,omitempty"`
	DiagnosticsFile     string            `json:"diagnostics_file,omitempty"`
	DiagnosticsJSONFile string            `json:"diagnostics_json_file,omitempty"`
	EventLog            string            `json:"event_log,omitempty"`
	StderrLog           string            `json:"stderr_log,omitempty"`
	FinalMessageFile    string            `json:"final_message_file,omitempty"`
	ResultFile          string            `json:"result_file,omitempty"`
	PatchFile           string            `json:"patch_file,omitempty"`
	DiscardedChanges    []DiscardedChange `json:"discarded_changes,omitempty"`
	Usage               CodexUsage        `json:"usage,omitempty"`
	Error               string            `json:"error,omitempty"`
}

type SysrootProvenance struct {
	Mode           string `json:"mode"`
	TargetID       string `json:"target_id"`
	Path           string `json:"path,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Source         string `json:"source,omitempty"`
	ManifestDigest string `json:"manifest_digest,omitempty"`
	Platform       string `json:"platform,omitempty"`
	LockFile       string `json:"lock_file,omitempty"`
}

type ToolchainProvenance struct {
	CCompiler    string `json:"c_compiler"`
	CXXCompiler  string `json:"cxx_compiler"`
	Archiver     string `json:"archiver,omitempty"`
	Ranlib       string `json:"ranlib,omitempty"`
	Strip        string `json:"strip,omitempty"`
	Linker       string `json:"linker,omitempty"`
	GCCToolchain string `json:"gcc_toolchain,omitempty"`
}

type BuildManifest struct {
	SchemaVersion     string               `json:"schema_version"`
	GeneratedAt       time.Time            `json:"generated_at"`
	StartedAt         time.Time            `json:"started_at,omitempty"`
	DurationMillis    int64                `json:"duration_ms,omitempty"`
	MiruriVersion     string               `json:"miruri_version"`
	BuildID           string               `json:"build_id,omitempty"`
	BuildStatus       string               `json:"build_status,omitempty"`
	ProjectName       string               `json:"project_name"`
	ProjectDigest     string               `json:"project_digest,omitempty"`
	RequestDigest     string               `json:"request_digest,omitempty"`
	Reused            bool                 `json:"reused,omitempty"`
	Target            TargetProfile        `json:"target"`
	BuildSystem       BuildSystem          `json:"build_system"`
	Sysroot           *SysrootProvenance   `json:"sysroot,omitempty"`
	Toolchain         *ToolchainProvenance `json:"toolchain,omitempty"`
	Assurance         ArtifactAssurance    `json:"assurance"`
	Artifacts         []ArtifactInfo       `json:"artifacts"`
	CodexRepairs      []CodexRepairAttempt `json:"codex_repairs,omitempty"`
	Warnings          []string             `json:"warnings,omitempty"`
	BuildLog          string               `json:"build_log"`
	AnalysisFile      string               `json:"analysis_file"`
	PlanFile          string               `json:"plan_file"`
	LicenseReportFile string               `json:"license_report_file,omitempty"`
	SBOMFile          string               `json:"sbom_file,omitempty"`
	IntegrityFile     string               `json:"integrity_file,omitempty"`
	PortedSourceDir   string               `json:"ported_source_dir,omitempty"`
}
