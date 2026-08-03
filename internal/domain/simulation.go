package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	SimulationProfileVersion         = "simulation_profile.v1"
	SimulationMergeCheckpointVersion = "simulation_merge_checkpoint.v1"
	maxCompactSimulationSourceFiles  = 3
	maxCompactSimulationItems        = 2
	maxCompactSimulationItemRunes    = 60
	reinforcedSimulationSourceFiles  = 5
	reinforcedSimulationItems        = 4
	reinforcedSimulationItemRunes    = 120
)

type SimulationProfile struct {
	Version       string                   `json:"version"`
	CreatedAt     string                   `json:"created_at,omitempty"`
	UpdatedAt     string                   `json:"updated_at,omitempty"`
	Corpus        SimulationCorpusManifest `json:"corpus"`
	SourceReports []SimulationSourceReport `json:"source_reports"`
	Synthesis     SimulationSynthesis      `json:"synthesis"`
}

type SimulationCorpusManifest struct {
	SourceDir string             `json:"source_dir,omitempty"`
	Sources   []SimulationSource `json:"sources"`
}

type SimulationSource struct {
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256"`
	Fingerprint  string `json:"fingerprint"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	ModTime      string `json:"mod_time,omitempty"`
	AnalyzedAt   string `json:"analyzed_at,omitempty"`
}

type SimulationSourceReport struct {
	RelativePath       string                         `json:"relative_path,omitempty"`
	SHA256             string                         `json:"sha256,omitempty"`
	Fingerprint        string                         `json:"fingerprint,omitempty"`
	AnalyzedAt         string                         `json:"analyzed_at,omitempty"`
	AnalysisSignature  string                         `json:"analysis_signature,omitempty"`
	ContentType        string                         `json:"content_type,omitempty"`
	Coverage           *float64                       `json:"coverage,omitempty"`
	Health             string                         `json:"health,omitempty"`
	Title              string                         `json:"title,omitempty"`
	Summary            string                         `json:"summary,omitempty"`
	Candidates         []SimulationTechniqueCandidate `json:"candidates,omitempty"`
	SafetyMarkers      []SimulationSafetyMarker       `json:"safety_markers,omitempty"`
	StyleObservations  []string                       `json:"style_observations,omitempty"`
	CommonWords        []string                       `json:"common_words,omitempty"`
	PlotPatterns       []string                       `json:"plot_patterns,omitempty"`
	HookPatterns       []string                       `json:"hook_patterns,omitempty"`
	PacingNotes        []string                       `json:"pacing_notes,omitempty"`
	ReaderAppeal       []string                       `json:"reader_appeal,omitempty"`
	ReusableTechniques []string                       `json:"reusable_techniques,omitempty"`
	Warnings           []string                       `json:"warnings,omitempty"`
}

// SimulationTechniqueCandidate is the validated, source-local input to the
// deterministic evidence reducer. It contains an abstract technique only;
// source wording and proper nouns belong in SafetyMarkers.
type SimulationTechniqueCandidate struct {
	Dimension   string   `json:"dimension"`
	Statement   string   `json:"statement"`
	Phases      []string `json:"phases,omitempty"`
	Scope       string   `json:"scope,omitempty"`
	Confidence  float64  `json:"confidence"`
	Tendency    string   `json:"tendency"`
	Safety      string   `json:"safety"`
	Contradicts []string `json:"contradicts,omitempty"`
}

// SimulationSafetyMarker is project-local audit material. Portable profiles
// never serialize these values.
type SimulationSafetyMarker struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type SimulationMergeCheckpoint struct {
	Version              string                     `json:"version"`
	UpdatedAt            string                     `json:"updated_at,omitempty"`
	PromptLimitBytes     int                        `json:"prompt_limit_bytes,omitempty"`
	TotalReportCount     int                        `json:"total_report_count,omitempty"`
	ProcessedReportCount int                        `json:"processed_report_count,omitempty"`
	ProcessedBatchCount  int                        `json:"processed_batch_count,omitempty"`
	Reports              []SimulationReportIdentity `json:"reports,omitempty"`
	SynthesisSignature   string                     `json:"synthesis_signature,omitempty"`
	RollingSynthesis     SimulationSynthesis        `json:"rolling_synthesis,omitempty"`
}

type SimulationReportIdentity struct {
	RelativePath string `json:"relative_path,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	Fingerprint  string `json:"fingerprint,omitempty"`
}

type SimulationSynthesis struct {
	Style            SimulationStyle            `json:"style,omitempty"`
	Lexicon          SimulationLexicon          `json:"lexicon,omitempty"`
	PlotDesign       SimulationPlotDesign       `json:"plot_design,omitempty"`
	HookDesign       SimulationHookDesign       `json:"hook_design,omitempty"`
	PacingDensity    SimulationPacingDensity    `json:"pacing_density,omitempty"`
	ReaderEngagement SimulationReaderEngagement `json:"reader_engagement,omitempty"`
	RoleGuidance     SimulationRoleGuidance     `json:"role_guidance,omitempty"`
}

type SimulationStyle struct {
	NarrativeVoice []string `json:"narrative_voice,omitempty"`
	SentenceRhythm []string `json:"sentence_rhythm,omitempty"`
	ProseTexture   []string `json:"prose_texture,omitempty"`
	Perspective    []string `json:"perspective,omitempty"`
	Mood           []string `json:"mood,omitempty"`
	DoNotCopy      []string `json:"do_not_copy,omitempty"`
}

type SimulationLexicon struct {
	CommonWords      []string `json:"common_words,omitempty"`
	EmotionWords     []string `json:"emotion_words,omitempty"`
	SceneWords       []string `json:"scene_words,omitempty"`
	TransitionWords  []string `json:"transition_words,omitempty"`
	SignaturePhrases []string `json:"signature_phrases,omitempty"`
}

type SimulationPlotDesign struct {
	OpeningPatterns      []string `json:"opening_patterns,omitempty"`
	EscalationPatterns   []string `json:"escalation_patterns,omitempty"`
	TurningPointPatterns []string `json:"turning_point_patterns,omitempty"`
	PayoffPatterns       []string `json:"payoff_patterns,omitempty"`
}

type SimulationHookDesign struct {
	HookTypes           []string `json:"hook_types,omitempty"`
	Placement           []string `json:"placement,omitempty"`
	CliffhangerPatterns []string `json:"cliffhanger_patterns,omitempty"`
	PayoffRules         []string `json:"payoff_rules,omitempty"`
}

type SimulationPacingDensity struct {
	SceneDensity        []string `json:"scene_density,omitempty"`
	InformationRelease  []string `json:"information_release,omitempty"`
	DialogueActionRatio []string `json:"dialogue_action_ratio,omitempty"`
	CompressionRules    []string `json:"compression_rules,omitempty"`
}

type SimulationReaderEngagement struct {
	Methods            []string `json:"methods,omitempty"`
	EmotionalDrivers   []string `json:"emotional_drivers,omitempty"`
	ProgressionRewards []string `json:"progression_rewards,omitempty"`
	AntiPatterns       []string `json:"anti_patterns,omitempty"`
}

type SimulationRoleGuidance struct {
	Coordinator []string `json:"coordinator,omitempty"`
	Architect   []string `json:"architect,omitempty"`
	Writer      []string `json:"writer,omitempty"`
	Editor      []string `json:"editor,omitempty"`
}

type SimulationCompactProfile struct {
	Version          string                     `json:"version"`
	Mode             string                     `json:"mode,omitempty"`
	UpdatedAt        string                     `json:"updated_at,omitempty"`
	SourceCount      int                        `json:"source_count"`
	SourceFiles      []string                   `json:"source_files,omitempty"`
	Style            SimulationStyle            `json:"style,omitempty"`
	Lexicon          SimulationLexicon          `json:"lexicon,omitempty"`
	PlotDesign       SimulationPlotDesign       `json:"plot_design,omitempty"`
	HookDesign       SimulationHookDesign       `json:"hook_design,omitempty"`
	PacingDensity    SimulationPacingDensity    `json:"pacing_density,omitempty"`
	ReaderEngagement SimulationReaderEngagement `json:"reader_engagement,omitempty"`
	RoleGuidance     SimulationRoleGuidance     `json:"role_guidance,omitempty"`
}

type SimulationCompactOptions struct {
	Mode           string
	MaxSourceFiles int
	MaxItems       int
	MaxItemRunes   int
}

func SimulationSourceFingerprint(relativePath, sha256 string) string {
	return strings.TrimSpace(relativePath) + ":" + strings.TrimSpace(sha256)
}

func ValidateSimulationProfile(p *SimulationProfile) error {
	if p == nil {
		return fmt.Errorf("simulation profile is nil")
	}
	if p.Version != SimulationProfileVersion {
		return fmt.Errorf("unsupported simulation profile version %q", p.Version)
	}
	for i := range p.Corpus.Sources {
		source := &p.Corpus.Sources[i]
		if source.RelativePath == "" || source.SHA256 == "" {
			return fmt.Errorf("source[%d] requires relative_path and sha256", i)
		}
		if source.Fingerprint == "" {
			source.Fingerprint = SimulationSourceFingerprint(source.RelativePath, source.SHA256)
		}
	}
	for i := range p.SourceReports {
		report := &p.SourceReports[i]
		if report.Fingerprint == "" && report.RelativePath != "" && report.SHA256 != "" {
			report.Fingerprint = SimulationSourceFingerprint(report.RelativePath, report.SHA256)
		}
	}
	return nil
}

func ValidateSimulationMergeCheckpoint(p *SimulationMergeCheckpoint) error {
	if p == nil {
		return fmt.Errorf("simulation merge checkpoint is nil")
	}
	if p.Version != SimulationMergeCheckpointVersion {
		return fmt.Errorf("unsupported simulation merge checkpoint version %q", p.Version)
	}
	if p.TotalReportCount <= 0 {
		return fmt.Errorf("merge checkpoint requires total_report_count")
	}
	if p.ProcessedReportCount <= 0 {
		return fmt.Errorf("merge checkpoint requires processed_report_count")
	}
	if p.ProcessedReportCount > p.TotalReportCount {
		return fmt.Errorf("merge checkpoint processed_report_count %d exceeds total_report_count %d", p.ProcessedReportCount, p.TotalReportCount)
	}
	if len(p.Reports) != p.TotalReportCount {
		return fmt.Errorf("merge checkpoint reports len = %d, want %d", len(p.Reports), p.TotalReportCount)
	}
	for i := range p.Reports {
		identity := &p.Reports[i]
		if identity.RelativePath == "" || identity.SHA256 == "" {
			return fmt.Errorf("merge checkpoint report[%d] requires relative_path and sha256", i)
		}
		if identity.Fingerprint == "" {
			identity.Fingerprint = SimulationSourceFingerprint(identity.RelativePath, identity.SHA256)
		}
	}
	return nil
}

func MarshalSimulationProfile(p SimulationProfile) ([]byte, error) {
	if p.Version == "" {
		p.Version = SimulationProfileVersion
	}
	return json.MarshalIndent(p, "", "  ")
}

func CompactSimulationProfile(p *SimulationProfile) *SimulationCompactProfile {
	return CompactSimulationProfileWithOptions(p, SimulationCompactOptions{
		MaxSourceFiles: maxCompactSimulationSourceFiles,
		MaxItems:       maxCompactSimulationItems,
		MaxItemRunes:   maxCompactSimulationItemRunes,
	})
}

func CompactSimulationProfileForMode(p *SimulationProfile, mode string) *SimulationCompactProfile {
	if strings.EqualFold(strings.TrimSpace(mode), "reinforced") {
		return CompactSimulationProfileWithOptions(p, SimulationCompactOptions{
			Mode:           "reinforced",
			MaxSourceFiles: reinforcedSimulationSourceFiles,
			MaxItems:       reinforcedSimulationItems,
			MaxItemRunes:   reinforcedSimulationItemRunes,
		})
	}
	return CompactSimulationProfile(p)
}

func CompactSimulationProfileWithOptions(p *SimulationProfile, opts SimulationCompactOptions) *SimulationCompactProfile {
	if p == nil {
		return nil
	}
	opts = normalizeSimulationCompactOptions(opts)
	limit := len(p.Corpus.Sources)
	if limit > opts.MaxSourceFiles {
		limit = opts.MaxSourceFiles
	}
	files := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		files = append(files, p.Corpus.Sources[i].RelativePath)
	}
	synthesis := compactSimulationSynthesisWithOptions(p.Synthesis, opts)
	return &SimulationCompactProfile{
		Version:          p.Version,
		Mode:             normalizedSimulationCompactMode(opts.Mode),
		UpdatedAt:        p.UpdatedAt,
		SourceCount:      len(p.Corpus.Sources),
		SourceFiles:      files,
		Style:            synthesis.Style,
		Lexicon:          synthesis.Lexicon,
		PlotDesign:       synthesis.PlotDesign,
		HookDesign:       synthesis.HookDesign,
		PacingDensity:    synthesis.PacingDensity,
		ReaderEngagement: synthesis.ReaderEngagement,
		RoleGuidance:     synthesis.RoleGuidance,
	}
}

func normalizeSimulationCompactOptions(opts SimulationCompactOptions) SimulationCompactOptions {
	if opts.MaxSourceFiles <= 0 {
		opts.MaxSourceFiles = maxCompactSimulationSourceFiles
	}
	if opts.MaxItems <= 0 {
		opts.MaxItems = maxCompactSimulationItems
	}
	if opts.MaxItemRunes <= 0 {
		opts.MaxItemRunes = maxCompactSimulationItemRunes
	}
	return opts
}

func normalizedSimulationCompactMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" || mode == "normal" {
		return ""
	}
	return mode
}

func compactSimulationSynthesis(s SimulationSynthesis) SimulationSynthesis {
	return compactSimulationSynthesisWithOptions(s, SimulationCompactOptions{
		MaxItems:     maxCompactSimulationItems,
		MaxItemRunes: maxCompactSimulationItemRunes,
	})
}

func compactSimulationSynthesisWithOptions(s SimulationSynthesis, opts SimulationCompactOptions) SimulationSynthesis {
	opts = normalizeSimulationCompactOptions(opts)
	return SimulationSynthesis{
		Style: SimulationStyle{
			NarrativeVoice: compactSimulationItemsWithOptions(s.Style.NarrativeVoice, opts),
			SentenceRhythm: compactSimulationItemsWithOptions(s.Style.SentenceRhythm, opts),
			ProseTexture:   compactSimulationItemsWithOptions(s.Style.ProseTexture, opts),
			Perspective:    compactSimulationItemsWithOptions(s.Style.Perspective, opts),
			Mood:           compactSimulationItemsWithOptions(s.Style.Mood, opts),
			DoNotCopy:      compactSimulationItemsWithOptions(s.Style.DoNotCopy, opts),
		},
		Lexicon: SimulationLexicon{
			CommonWords:      compactSimulationItemsWithOptions(s.Lexicon.CommonWords, opts),
			EmotionWords:     compactSimulationItemsWithOptions(s.Lexicon.EmotionWords, opts),
			SceneWords:       compactSimulationItemsWithOptions(s.Lexicon.SceneWords, opts),
			TransitionWords:  compactSimulationItemsWithOptions(s.Lexicon.TransitionWords, opts),
			SignaturePhrases: compactSimulationItemsWithOptions(s.Lexicon.SignaturePhrases, opts),
		},
		PlotDesign: SimulationPlotDesign{
			OpeningPatterns:      compactSimulationItemsWithOptions(s.PlotDesign.OpeningPatterns, opts),
			EscalationPatterns:   compactSimulationItemsWithOptions(s.PlotDesign.EscalationPatterns, opts),
			TurningPointPatterns: compactSimulationItemsWithOptions(s.PlotDesign.TurningPointPatterns, opts),
			PayoffPatterns:       compactSimulationItemsWithOptions(s.PlotDesign.PayoffPatterns, opts),
		},
		HookDesign: SimulationHookDesign{
			HookTypes:           compactSimulationItemsWithOptions(s.HookDesign.HookTypes, opts),
			Placement:           compactSimulationItemsWithOptions(s.HookDesign.Placement, opts),
			CliffhangerPatterns: compactSimulationItemsWithOptions(s.HookDesign.CliffhangerPatterns, opts),
			PayoffRules:         compactSimulationItemsWithOptions(s.HookDesign.PayoffRules, opts),
		},
		PacingDensity: SimulationPacingDensity{
			SceneDensity:        compactSimulationItemsWithOptions(s.PacingDensity.SceneDensity, opts),
			InformationRelease:  compactSimulationItemsWithOptions(s.PacingDensity.InformationRelease, opts),
			DialogueActionRatio: compactSimulationItemsWithOptions(s.PacingDensity.DialogueActionRatio, opts),
			CompressionRules:    compactSimulationItemsWithOptions(s.PacingDensity.CompressionRules, opts),
		},
		ReaderEngagement: SimulationReaderEngagement{
			Methods:            compactSimulationItemsWithOptions(s.ReaderEngagement.Methods, opts),
			EmotionalDrivers:   compactSimulationItemsWithOptions(s.ReaderEngagement.EmotionalDrivers, opts),
			ProgressionRewards: compactSimulationItemsWithOptions(s.ReaderEngagement.ProgressionRewards, opts),
			AntiPatterns:       compactSimulationItemsWithOptions(s.ReaderEngagement.AntiPatterns, opts),
		},
		RoleGuidance: SimulationRoleGuidance{
			Coordinator: compactSimulationItemsWithOptions(s.RoleGuidance.Coordinator, opts),
			Architect:   compactSimulationItemsWithOptions(s.RoleGuidance.Architect, opts),
			Writer:      compactSimulationItemsWithOptions(s.RoleGuidance.Writer, opts),
			Editor:      compactSimulationItemsWithOptions(s.RoleGuidance.Editor, opts),
		},
	}
}

func compactSimulationItems(items []string) []string {
	return compactSimulationItemsWithOptions(items, SimulationCompactOptions{
		MaxItems:     maxCompactSimulationItems,
		MaxItemRunes: maxCompactSimulationItemRunes,
	})
}

func compactSimulationItemsWithOptions(items []string, opts SimulationCompactOptions) []string {
	if len(items) == 0 {
		return nil
	}
	opts = normalizeSimulationCompactOptions(opts)
	limit := len(items)
	if limit > opts.MaxItems {
		limit = opts.MaxItems
	}
	out := make([]string, limit)
	for i := 0; i < limit; i++ {
		out[i] = truncateSimulationRunes(items[i], opts.MaxItemRunes)
	}
	return out
}

func truncateSimulationRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

func MergeSimulationSynthesis(a, b SimulationSynthesis) SimulationSynthesis {
	return SimulationSynthesis{
		Style: SimulationStyle{
			NarrativeVoice: mergeStringSets(a.Style.NarrativeVoice, b.Style.NarrativeVoice),
			SentenceRhythm: mergeStringSets(a.Style.SentenceRhythm, b.Style.SentenceRhythm),
			ProseTexture:   mergeStringSets(a.Style.ProseTexture, b.Style.ProseTexture),
			Perspective:    mergeStringSets(a.Style.Perspective, b.Style.Perspective),
			Mood:           mergeStringSets(a.Style.Mood, b.Style.Mood),
			DoNotCopy:      mergeStringSets(a.Style.DoNotCopy, b.Style.DoNotCopy),
		},
		Lexicon: SimulationLexicon{
			CommonWords:      mergeStringSets(a.Lexicon.CommonWords, b.Lexicon.CommonWords),
			EmotionWords:     mergeStringSets(a.Lexicon.EmotionWords, b.Lexicon.EmotionWords),
			SceneWords:       mergeStringSets(a.Lexicon.SceneWords, b.Lexicon.SceneWords),
			TransitionWords:  mergeStringSets(a.Lexicon.TransitionWords, b.Lexicon.TransitionWords),
			SignaturePhrases: mergeStringSets(a.Lexicon.SignaturePhrases, b.Lexicon.SignaturePhrases),
		},
		PlotDesign: SimulationPlotDesign{
			OpeningPatterns:      mergeStringSets(a.PlotDesign.OpeningPatterns, b.PlotDesign.OpeningPatterns),
			EscalationPatterns:   mergeStringSets(a.PlotDesign.EscalationPatterns, b.PlotDesign.EscalationPatterns),
			TurningPointPatterns: mergeStringSets(a.PlotDesign.TurningPointPatterns, b.PlotDesign.TurningPointPatterns),
			PayoffPatterns:       mergeStringSets(a.PlotDesign.PayoffPatterns, b.PlotDesign.PayoffPatterns),
		},
		HookDesign: SimulationHookDesign{
			HookTypes:           mergeStringSets(a.HookDesign.HookTypes, b.HookDesign.HookTypes),
			Placement:           mergeStringSets(a.HookDesign.Placement, b.HookDesign.Placement),
			CliffhangerPatterns: mergeStringSets(a.HookDesign.CliffhangerPatterns, b.HookDesign.CliffhangerPatterns),
			PayoffRules:         mergeStringSets(a.HookDesign.PayoffRules, b.HookDesign.PayoffRules),
		},
		PacingDensity: SimulationPacingDensity{
			SceneDensity:        mergeStringSets(a.PacingDensity.SceneDensity, b.PacingDensity.SceneDensity),
			InformationRelease:  mergeStringSets(a.PacingDensity.InformationRelease, b.PacingDensity.InformationRelease),
			DialogueActionRatio: mergeStringSets(a.PacingDensity.DialogueActionRatio, b.PacingDensity.DialogueActionRatio),
			CompressionRules:    mergeStringSets(a.PacingDensity.CompressionRules, b.PacingDensity.CompressionRules),
		},
		ReaderEngagement: SimulationReaderEngagement{
			Methods:            mergeStringSets(a.ReaderEngagement.Methods, b.ReaderEngagement.Methods),
			EmotionalDrivers:   mergeStringSets(a.ReaderEngagement.EmotionalDrivers, b.ReaderEngagement.EmotionalDrivers),
			ProgressionRewards: mergeStringSets(a.ReaderEngagement.ProgressionRewards, b.ReaderEngagement.ProgressionRewards),
			AntiPatterns:       mergeStringSets(a.ReaderEngagement.AntiPatterns, b.ReaderEngagement.AntiPatterns),
		},
		RoleGuidance: SimulationRoleGuidance{
			Coordinator: mergeStringSets(a.RoleGuidance.Coordinator, b.RoleGuidance.Coordinator),
			Architect:   mergeStringSets(a.RoleGuidance.Architect, b.RoleGuidance.Architect),
			Writer:      mergeStringSets(a.RoleGuidance.Writer, b.RoleGuidance.Writer),
			Editor:      mergeStringSets(a.RoleGuidance.Editor, b.RoleGuidance.Editor),
		},
	}
}

func mergeStringSets(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, group := range groups {
		for _, item := range group {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			key := strings.ToLower(item)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}
