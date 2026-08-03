package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SimulationPortableProfileVersion   = "simulation_profile.v2"
	SimulationLocalEvidenceVersion     = "simulation_evidence.v1"
	SimulationSafetyIndexVersion       = "simulation_safety_index.v1"
	MaxSimulationProfileBytes          = 4 << 20
	MaxSimulationFeatures              = 2048
	MaxSimulationEvidenceSources       = 10000
	MaxSimulationFeatureStatementRunes = 500
	MaxSimulationEvidenceItemRunes     = 8000
	MaxSimulationEvidenceItemsPerField = 512
)

type SimulationProfileV2 struct {
	Version       string                     `json:"version"`
	CreatedAt     string                     `json:"created_at"`
	UpdatedAt     string                     `json:"updated_at"`
	ProfileDigest string                     `json:"profile_digest"`
	Corpus        SimulationPortableCorpus   `json:"corpus"`
	Analysis      SimulationAnalysisMetadata `json:"analysis"`
	EvidenceRefs  []string                   `json:"evidence_refs,omitempty"`
	Features      []SimulationFeature        `json:"features"`
	Capabilities  SimulationCapabilities     `json:"capabilities"`
	Health        SimulationProfileHealth    `json:"health"`
}

type SimulationPortableCorpus struct {
	Digest      string `json:"digest"`
	SourceCount int    `json:"source_count"`
}

type SimulationAnalysisMetadata struct {
	SourceAnalysisSignature string `json:"source_analysis_signature,omitempty"`
	SplitterSignature       string `json:"splitter_signature,omitempty"`
	SchemaSignature         string `json:"schema_signature,omitempty"`
	SynthesisSignature      string `json:"synthesis_signature,omitempty"`
	AggregationSignature    string `json:"aggregation_signature,omitempty"`
	ModelIdentity           string `json:"model_identity,omitempty"`
	Legacy                  bool   `json:"legacy,omitempty"`
}

type SimulationFeature struct {
	ID                string   `json:"id"`
	Dimension         string   `json:"dimension"`
	Statement         string   `json:"statement"`
	Classification    string   `json:"classification"`
	Phases            []string `json:"phases,omitempty"`
	Scopes            []string `json:"scopes,omitempty"`
	Roles             []string `json:"roles,omitempty"`
	SupportCount      int      `json:"support_count"`
	Coverage          *float64 `json:"coverage,omitempty"`
	Confidence        *float64 `json:"confidence,omitempty"`
	EvidenceRefs      []string `json:"evidence_refs,omitempty"`
	ContradictionRefs []string `json:"contradiction_refs,omitempty"`
	Safety            string   `json:"safety"`
	Disabled          bool     `json:"disabled,omitempty"`
}

type SimulationCapabilities struct {
	Portable       bool `json:"portable"`
	LocalEvidence  bool `json:"local_evidence"`
	AnalysisSigned bool `json:"analysis_signed"`
	SafetyIndex    bool `json:"safety_index"`
}

type SimulationProfileHealth struct {
	State   string   `json:"state"`
	Reasons []string `json:"reasons,omitempty"`
}

type SimulationLocalEvidence struct {
	Version       string                   `json:"version"`
	ProfileDigest string                   `json:"profile_digest"`
	SourceDir     string                   `json:"source_dir,omitempty"`
	Sources       []SimulationSource       `json:"sources,omitempty"`
	SourceReports []SimulationSourceReport `json:"source_reports,omitempty"`
	SafetyIndex   *SimulationSafetyIndex   `json:"safety_index,omitempty"`
}

type SimulationSafetyIndex struct {
	Version   string                       `json:"version"`
	UpdatedAt string                       `json:"updated_at"`
	Entries   []SimulationSafetyIndexEntry `json:"entries,omitempty"`
}

type SimulationSafetyIndexEntry struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Value        string   `json:"value"`
	EvidenceRefs []string `json:"evidence_refs"`
}

func ProjectSimulationProfileV1(profile SimulationProfile) (SimulationProfileV2, SimulationLocalEvidence, error) {
	if profile.Version == "" {
		profile.Version = SimulationProfileVersion
	}
	if err := ValidateSimulationProfile(&profile); err != nil {
		return SimulationProfileV2{}, SimulationLocalEvidence{}, err
	}
	createdAt, updatedAt := normalizedSimulationTimes(profile.CreatedAt, profile.UpdatedAt)
	portable := SimulationProfileV2{
		Version:   SimulationPortableProfileVersion,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		Corpus: SimulationPortableCorpus{
			Digest:      simulationCorpusDigest(profile.Corpus.Sources),
			SourceCount: len(profile.Corpus.Sources),
		},
		Analysis: SimulationAnalysisMetadata{Legacy: true},
		Features: synthesisToSimulationFeatures(profile.Synthesis),
		Capabilities: SimulationCapabilities{
			Portable:       true,
			LocalEvidence:  len(profile.Corpus.Sources) > 0 || len(profile.SourceReports) > 0,
			AnalysisSigned: false,
			SafetyIndex:    false,
		},
		Health: SimulationProfileHealth{
			State:   "legacy",
			Reasons: []string{"analysis_signature_unknown"},
		},
	}
	if err := SetSimulationProfileDigest(&portable); err != nil {
		return SimulationProfileV2{}, SimulationLocalEvidence{}, err
	}
	evidence := SimulationLocalEvidence{
		Version:       SimulationLocalEvidenceVersion,
		ProfileDigest: portable.ProfileDigest,
		SourceDir:     profile.Corpus.SourceDir,
		Sources:       append([]SimulationSource(nil), profile.Corpus.Sources...),
		SourceReports: append([]SimulationSourceReport(nil), profile.SourceReports...),
	}
	if err := ValidateSimulationProfileV2(&portable); err != nil {
		return SimulationProfileV2{}, SimulationLocalEvidence{}, err
	}
	if err := ValidateSimulationLocalEvidence(&evidence); err != nil {
		return SimulationProfileV2{}, SimulationLocalEvidence{}, err
	}
	return portable, evidence, nil
}

func SimulationProfileV2CompatibilityProfile(portable SimulationProfileV2, evidence *SimulationLocalEvidence) (SimulationProfile, error) {
	if err := ValidateSimulationProfileV2(&portable); err != nil {
		return SimulationProfile{}, err
	}
	profile := SimulationProfile{
		Version:   SimulationProfileVersion,
		CreatedAt: portable.CreatedAt,
		UpdatedAt: portable.UpdatedAt,
		Synthesis: simulationFeaturesToSynthesis(portable.Features),
	}
	if evidence != nil {
		if err := ValidateSimulationLocalEvidence(evidence); err != nil {
			return SimulationProfile{}, err
		}
		if evidence.ProfileDigest == portable.ProfileDigest {
			profile.Corpus = SimulationCorpusManifest{
				SourceDir: evidence.SourceDir,
				Sources:   append([]SimulationSource(nil), evidence.Sources...),
			}
			profile.SourceReports = append([]SimulationSourceReport(nil), evidence.SourceReports...)
		}
	}
	return profile, nil
}

func ValidateSimulationProfileV2(profile *SimulationProfileV2) error {
	if profile == nil {
		return fmt.Errorf("simulation portable profile is nil")
	}
	if profile.Version != SimulationPortableProfileVersion {
		return fmt.Errorf("unsupported simulation portable profile version")
	}
	if err := validateSimulationTimestamp("created_at", profile.CreatedAt); err != nil {
		return err
	}
	if err := validateSimulationTimestamp("updated_at", profile.UpdatedAt); err != nil {
		return err
	}
	if !validSimulationDigest(profile.ProfileDigest) || !validSimulationDigest(profile.Corpus.Digest) {
		return fmt.Errorf("simulation portable profile requires valid digests")
	}
	if profile.Corpus.SourceCount < 0 || profile.Corpus.SourceCount > MaxSimulationEvidenceSources {
		return fmt.Errorf("simulation portable corpus source_count is out of range")
	}
	if !profile.Capabilities.Portable {
		return fmt.Errorf("simulation portable capability is required")
	}
	for _, signature := range []string{
		profile.Analysis.SourceAnalysisSignature,
		profile.Analysis.SplitterSignature,
		profile.Analysis.SchemaSignature,
		profile.Analysis.SynthesisSignature,
		profile.Analysis.AggregationSignature,
		profile.Analysis.ModelIdentity,
	} {
		if utf8.RuneCountInString(signature) > 256 {
			return fmt.Errorf("simulation analysis metadata exceeds length limit")
		}
	}
	switch profile.Health.State {
	case "fresh", "stale", "legacy", "unknown", "portable_only":
	default:
		return fmt.Errorf("simulation portable health state is invalid")
	}
	if profile.Health.State == "fresh" {
		if !profile.Capabilities.AnalysisSigned ||
			strings.TrimSpace(profile.Analysis.SourceAnalysisSignature) == "" ||
			strings.TrimSpace(profile.Analysis.SchemaSignature) == "" ||
			strings.TrimSpace(profile.Analysis.AggregationSignature) == "" {
			return fmt.Errorf("fresh simulation profile requires analysis signatures")
		}
	}
	if len(profile.Features) > MaxSimulationFeatures {
		return fmt.Errorf("simulation portable profile has too many features")
	}
	if hasDuplicateSimulationStrings(profile.EvidenceRefs) {
		return fmt.Errorf("simulation portable evidence refs must be unique and non-empty")
	}
	evidenceRefs := make(map[string]struct{}, len(profile.EvidenceRefs))
	for _, ref := range profile.EvidenceRefs {
		evidenceRefs[ref] = struct{}{}
	}
	ids := make(map[string]struct{}, len(profile.Features))
	for i, feature := range profile.Features {
		if err := validateSimulationFeature(i, feature); err != nil {
			return err
		}
		if _, exists := ids[feature.ID]; exists {
			return fmt.Errorf("simulation feature ids must be unique")
		}
		ids[feature.ID] = struct{}{}
	}
	for _, feature := range profile.Features {
		for _, ref := range feature.EvidenceRefs {
			if _, exists := evidenceRefs[ref]; !exists {
				return fmt.Errorf("simulation feature contains an unknown evidence reference")
			}
		}
		for _, ref := range feature.ContradictionRefs {
			if _, exists := ids[ref]; !exists {
				return fmt.Errorf("simulation feature contains an unknown contradiction reference")
			}
		}
	}
	expected, err := SimulationProfileDigest(*profile)
	if err != nil {
		return err
	}
	if expected != profile.ProfileDigest {
		return fmt.Errorf("simulation portable profile digest mismatch")
	}
	return nil
}

func ValidateSimulationLocalEvidence(evidence *SimulationLocalEvidence) error {
	if evidence == nil {
		return fmt.Errorf("simulation local evidence is nil")
	}
	if evidence.Version != SimulationLocalEvidenceVersion {
		return fmt.Errorf("unsupported simulation local evidence version")
	}
	if !validSimulationDigest(evidence.ProfileDigest) {
		return fmt.Errorf("simulation local evidence requires profile_digest")
	}
	if len(evidence.Sources) > MaxSimulationEvidenceSources || len(evidence.SourceReports) > MaxSimulationEvidenceSources {
		return fmt.Errorf("simulation local evidence exceeds item limits")
	}
	if evidence.SafetyIndex != nil {
		if err := validateSimulationSafetyIndex(evidence.SafetyIndex); err != nil {
			return err
		}
	}
	sourceIDs := make(map[string]struct{}, len(evidence.Sources))
	for i, source := range evidence.Sources {
		if strings.TrimSpace(source.RelativePath) == "" || strings.TrimSpace(source.SHA256) == "" {
			return fmt.Errorf("simulation local source[%d] is incomplete", i)
		}
		if filepath.IsAbs(source.RelativePath) {
			return fmt.Errorf("simulation local source relative_path must be relative")
		}
		cleanPath := filepath.Clean(filepath.FromSlash(source.RelativePath))
		if cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
			return fmt.Errorf("simulation local source relative_path is invalid")
		}
		identity := SimulationSourceFingerprint(source.RelativePath, source.SHA256)
		if source.Fingerprint != "" && source.Fingerprint != identity {
			return fmt.Errorf("simulation local source fingerprint is invalid")
		}
		if _, exists := sourceIDs[identity]; exists {
			return fmt.Errorf("simulation local source identities must be unique")
		}
		sourceIDs[identity] = struct{}{}
	}
	reportIDs := make(map[string]struct{}, len(evidence.SourceReports))
	for i, report := range evidence.SourceReports {
		identity := SimulationSourceFingerprint(report.RelativePath, report.SHA256)
		if strings.TrimSpace(report.RelativePath) == "" || strings.TrimSpace(report.SHA256) == "" {
			return fmt.Errorf("simulation local report[%d] is incomplete", i)
		}
		if _, exists := sourceIDs[identity]; !exists {
			return fmt.Errorf("simulation local report has no matching source")
		}
		if report.Fingerprint != "" && report.Fingerprint != identity {
			return fmt.Errorf("simulation local report fingerprint is invalid")
		}
		if err := validateSimulationSourceReportSize(report); err != nil {
			return err
		}
		if _, exists := reportIDs[identity]; exists {
			return fmt.Errorf("simulation local report identities must be unique")
		}
		reportIDs[identity] = struct{}{}
	}
	return nil
}

func MarshalSimulationPortableProfile(profile SimulationProfileV2) ([]byte, error) {
	if err := ValidateSimulationProfileV2(&profile); err != nil {
		return nil, err
	}
	return json.MarshalIndent(profile, "", "  ")
}

func UnmarshalSimulationPortableProfile(data []byte) (SimulationProfileV2, error) {
	if len(data) == 0 || len(data) > MaxSimulationProfileBytes {
		return SimulationProfileV2{}, fmt.Errorf("simulation portable profile size is invalid")
	}
	var profile SimulationProfileV2
	if err := decodeStrictSimulationJSON(data, &profile); err != nil {
		return SimulationProfileV2{}, fmt.Errorf("invalid simulation portable profile JSON")
	}
	if err := ValidateSimulationProfileV2(&profile); err != nil {
		return SimulationProfileV2{}, err
	}
	return profile, nil
}

func MarshalSimulationLocalEvidence(evidence SimulationLocalEvidence) ([]byte, error) {
	if err := ValidateSimulationLocalEvidence(&evidence); err != nil {
		return nil, err
	}
	return json.MarshalIndent(evidence, "", "  ")
}

func UnmarshalSimulationLocalEvidence(data []byte) (SimulationLocalEvidence, error) {
	if len(data) == 0 || len(data) > MaxSimulationProfileBytes*4 {
		return SimulationLocalEvidence{}, fmt.Errorf("simulation local evidence size is invalid")
	}
	var evidence SimulationLocalEvidence
	if err := decodeStrictSimulationJSON(data, &evidence); err != nil {
		return SimulationLocalEvidence{}, fmt.Errorf("invalid simulation local evidence JSON")
	}
	if err := ValidateSimulationLocalEvidence(&evidence); err != nil {
		return SimulationLocalEvidence{}, err
	}
	return evidence, nil
}

func UnmarshalSimulationProfileForCompatibility(data []byte) (SimulationProfile, *SimulationProfileV2, error) {
	if len(data) == 0 || len(data) > MaxSimulationProfileBytes {
		return SimulationProfile{}, nil, fmt.Errorf("simulation profile size is invalid")
	}
	var header struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return SimulationProfile{}, nil, fmt.Errorf("invalid simulation profile JSON")
	}
	if header.Version == SimulationPortableProfileVersion {
		portable, err := UnmarshalSimulationPortableProfile(data)
		if err != nil {
			return SimulationProfile{}, nil, err
		}
		profile, err := SimulationProfileV2CompatibilityProfile(portable, nil)
		return profile, &portable, err
	}
	var profile SimulationProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return SimulationProfile{}, nil, fmt.Errorf("invalid simulation profile JSON")
	}
	if err := ValidateSimulationProfile(&profile); err != nil {
		return SimulationProfile{}, nil, err
	}
	return profile, nil, nil
}

func SetSimulationProfileDigest(profile *SimulationProfileV2) error {
	if profile == nil {
		return fmt.Errorf("simulation portable profile is nil")
	}
	digest, err := SimulationProfileDigest(*profile)
	if err != nil {
		return err
	}
	profile.ProfileDigest = digest
	return nil
}

func SimulationProfileDigest(profile SimulationProfileV2) (string, error) {
	profile.ProfileDigest = ""
	data, err := json.Marshal(profile)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func decodeStrictSimulationJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

func validateSimulationFeature(index int, feature SimulationFeature) error {
	if strings.TrimSpace(feature.ID) == "" || strings.TrimSpace(feature.Dimension) == "" || strings.TrimSpace(feature.Statement) == "" {
		return fmt.Errorf("simulation feature[%d] is missing required fields", index)
	}
	if utf8.RuneCountInString(feature.ID) > 128 || utf8.RuneCountInString(feature.Dimension) > 128 {
		return fmt.Errorf("simulation feature identity exceeds length limit")
	}
	if utf8.RuneCountInString(feature.Statement) > MaxSimulationFeatureStatementRunes {
		return fmt.Errorf("simulation feature statement exceeds length limit")
	}
	if looksLikeAbsoluteSimulationPath(feature.Statement) {
		return fmt.Errorf("simulation feature statement contains a local path")
	}
	switch feature.Classification {
	case "stable", "local", "outlier", "contradictory", "legacy_unknown":
	default:
		return fmt.Errorf("simulation feature classification is invalid")
	}
	switch feature.Safety {
	case "guidance", "avoid", "blocked":
	default:
		return fmt.Errorf("simulation feature safety is invalid")
	}
	if feature.SupportCount < 0 {
		return fmt.Errorf("simulation feature support_count is invalid")
	}
	if len(feature.EvidenceRefs) > 0 && feature.SupportCount < len(feature.EvidenceRefs) {
		return fmt.Errorf("simulation feature support_count is smaller than evidence")
	}
	if feature.Coverage != nil && (*feature.Coverage < 0 || *feature.Coverage > 1) {
		return fmt.Errorf("simulation feature coverage is out of range")
	}
	if feature.Confidence != nil && (*feature.Confidence < 0 || *feature.Confidence > 1) {
		return fmt.Errorf("simulation feature confidence is out of range")
	}
	if hasDuplicateSimulationStrings(feature.Phases) || hasDuplicateSimulationStrings(feature.Scopes) || hasDuplicateSimulationStrings(feature.Roles) ||
		hasDuplicateSimulationStrings(feature.EvidenceRefs) || hasDuplicateSimulationStrings(feature.ContradictionRefs) {
		return fmt.Errorf("simulation feature contains duplicate entries")
	}
	for _, phase := range feature.Phases {
		switch phase {
		case "cocreate", "planning", "chapter", "review":
		default:
			return fmt.Errorf("simulation feature phase is invalid")
		}
	}
	for _, role := range feature.Roles {
		switch role {
		case "coordinator", "architect", "writer", "editor":
		default:
			return fmt.Errorf("simulation feature role is invalid")
		}
	}
	for _, scope := range feature.Scopes {
		switch scope {
		case "global", "opening", "middle", "ending", "scene":
		default:
			return fmt.Errorf("simulation feature scope is invalid")
		}
	}
	return nil
}

func validateSimulationSourceReportSize(report SimulationSourceReport) error {
	scalars := []string{report.Title, report.Summary}
	for _, value := range scalars {
		if utf8.RuneCountInString(value) > MaxSimulationEvidenceItemRunes {
			return fmt.Errorf("simulation local report text exceeds length limit")
		}
	}
	fields := [][]string{
		report.StyleObservations,
		report.CommonWords,
		report.PlotPatterns,
		report.HookPatterns,
		report.PacingNotes,
		report.ReaderAppeal,
		report.ReusableTechniques,
		report.Warnings,
	}
	for _, items := range fields {
		if len(items) > MaxSimulationEvidenceItemsPerField {
			return fmt.Errorf("simulation local report array exceeds item limit")
		}
		for _, item := range items {
			if strings.TrimSpace(item) == "" {
				return fmt.Errorf("simulation local report contains an empty item")
			}
			if utf8.RuneCountInString(item) > MaxSimulationEvidenceItemRunes {
				return fmt.Errorf("simulation local report item exceeds length limit")
			}
		}
	}
	if len(report.Candidates) > MaxSimulationCandidatesPerReport || len(report.SafetyMarkers) > MaxSimulationSafetyMarkers {
		return fmt.Errorf("simulation local report structured evidence exceeds item limit")
	}
	if len(report.Candidates) > 0 {
		copyReport := report
		copyReport.Candidates = append([]SimulationTechniqueCandidate(nil), report.Candidates...)
		copyReport.SafetyMarkers = append([]SimulationSafetyMarker(nil), report.SafetyMarkers...)
		if err := NormalizeAndValidateSimulationSourceReport(&copyReport); err != nil {
			return err
		}
	}
	return nil
}

func hasDuplicateSimulationStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return true
		}
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func validSimulationDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func looksLikeAbsoluteSimulationPath(value string) bool {
	for _, token := range strings.Fields(value) {
		token = strings.Trim(token, `"'()[]{}<>,;`)
		if filepath.IsAbs(token) {
			return true
		}
		if len(token) >= 3 &&
			((token[0] >= 'A' && token[0] <= 'Z') || (token[0] >= 'a' && token[0] <= 'z')) &&
			token[1] == ':' && (token[2] == '\\' || token[2] == '/') {
			return true
		}
	}
	return false
}

func validateSimulationTimestamp(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("simulation portable profile requires %s", field)
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("simulation portable profile %s is invalid", field)
	}
	return nil
}

func normalizedSimulationTimes(createdAt, updatedAt string) (string, string) {
	fallback := time.Unix(0, 0).UTC().Format(time.RFC3339)
	if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
		createdAt = fallback
	}
	if _, err := time.Parse(time.RFC3339, updatedAt); err != nil {
		updatedAt = createdAt
	}
	return createdAt, updatedAt
}

func simulationCorpusDigest(sources []SimulationSource) string {
	identities := make([]string, 0, len(sources))
	for _, source := range sources {
		identities = append(identities, SimulationSourceFingerprint(source.RelativePath, source.SHA256))
	}
	sort.Strings(identities)
	sum := sha256.Sum256([]byte(strings.Join(identities, "\n")))
	return hex.EncodeToString(sum[:])
}

type simulationSynthesisField struct {
	dimension string
	items     []string
}

func synthesisToSimulationFeatures(s SimulationSynthesis) []SimulationFeature {
	fields := simulationSynthesisFields(s)
	features := make([]SimulationFeature, 0)
	seen := make(map[string]struct{})
	for _, field := range fields {
		for _, statement := range field.items {
			statement = strings.TrimSpace(statement)
			if statement == "" || looksLikeAbsoluteSimulationPath(statement) {
				continue
			}
			sum := sha256.Sum256([]byte(field.dimension + "\x00" + statement))
			id := "legacy-" + hex.EncodeToString(sum[:8])
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			safety := "guidance"
			if strings.HasSuffix(field.dimension, ".do_not_copy") || strings.HasSuffix(field.dimension, ".anti_patterns") {
				safety = "avoid"
			}
			features = append(features, SimulationFeature{
				ID:             id,
				Dimension:      field.dimension,
				Statement:      statement,
				Classification: "legacy_unknown",
				SupportCount:   0,
				Safety:         safety,
			})
		}
	}
	return features
}

func simulationFeaturesToSynthesis(features []SimulationFeature) SimulationSynthesis {
	byDimension := make(map[string][]string)
	for _, feature := range features {
		if feature.Disabled || feature.Safety == "blocked" {
			continue
		}
		byDimension[feature.Dimension] = append(byDimension[feature.Dimension], feature.Statement)
	}
	return synthesisFromSimulationDimensions(byDimension)
}

func simulationSynthesisFields(s SimulationSynthesis) []simulationSynthesisField {
	return []simulationSynthesisField{
		{"style.narrative_voice", s.Style.NarrativeVoice}, {"style.sentence_rhythm", s.Style.SentenceRhythm},
		{"style.prose_texture", s.Style.ProseTexture}, {"style.perspective", s.Style.Perspective},
		{"style.mood", s.Style.Mood}, {"style.do_not_copy", s.Style.DoNotCopy},
		{"lexicon.common_words", s.Lexicon.CommonWords}, {"lexicon.emotion_words", s.Lexicon.EmotionWords},
		{"lexicon.scene_words", s.Lexicon.SceneWords}, {"lexicon.transition_words", s.Lexicon.TransitionWords},
		{"lexicon.signature_phrases", s.Lexicon.SignaturePhrases},
		{"plot_design.opening_patterns", s.PlotDesign.OpeningPatterns}, {"plot_design.escalation_patterns", s.PlotDesign.EscalationPatterns},
		{"plot_design.turning_point_patterns", s.PlotDesign.TurningPointPatterns}, {"plot_design.payoff_patterns", s.PlotDesign.PayoffPatterns},
		{"hook_design.hook_types", s.HookDesign.HookTypes}, {"hook_design.placement", s.HookDesign.Placement},
		{"hook_design.cliffhanger_patterns", s.HookDesign.CliffhangerPatterns}, {"hook_design.payoff_rules", s.HookDesign.PayoffRules},
		{"pacing_density.scene_density", s.PacingDensity.SceneDensity}, {"pacing_density.information_release", s.PacingDensity.InformationRelease},
		{"pacing_density.dialogue_action_ratio", s.PacingDensity.DialogueActionRatio}, {"pacing_density.compression_rules", s.PacingDensity.CompressionRules},
		{"reader_engagement.methods", s.ReaderEngagement.Methods}, {"reader_engagement.emotional_drivers", s.ReaderEngagement.EmotionalDrivers},
		{"reader_engagement.progression_rewards", s.ReaderEngagement.ProgressionRewards}, {"reader_engagement.anti_patterns", s.ReaderEngagement.AntiPatterns},
		{"role_guidance.coordinator", s.RoleGuidance.Coordinator}, {"role_guidance.architect", s.RoleGuidance.Architect},
		{"role_guidance.writer", s.RoleGuidance.Writer}, {"role_guidance.editor", s.RoleGuidance.Editor},
	}
}

func synthesisFromSimulationDimensions(v map[string][]string) SimulationSynthesis {
	return SimulationSynthesis{
		Style: SimulationStyle{
			NarrativeVoice: v["style.narrative_voice"], SentenceRhythm: v["style.sentence_rhythm"],
			ProseTexture: v["style.prose_texture"], Perspective: v["style.perspective"],
			Mood: v["style.mood"], DoNotCopy: v["style.do_not_copy"],
		},
		Lexicon: SimulationLexicon{
			CommonWords: v["lexicon.common_words"], EmotionWords: v["lexicon.emotion_words"],
			SceneWords: v["lexicon.scene_words"], TransitionWords: v["lexicon.transition_words"],
			SignaturePhrases: v["lexicon.signature_phrases"],
		},
		PlotDesign: SimulationPlotDesign{
			OpeningPatterns: v["plot_design.opening_patterns"], EscalationPatterns: v["plot_design.escalation_patterns"],
			TurningPointPatterns: v["plot_design.turning_point_patterns"], PayoffPatterns: v["plot_design.payoff_patterns"],
		},
		HookDesign: SimulationHookDesign{
			HookTypes: v["hook_design.hook_types"], Placement: v["hook_design.placement"],
			CliffhangerPatterns: v["hook_design.cliffhanger_patterns"], PayoffRules: v["hook_design.payoff_rules"],
		},
		PacingDensity: SimulationPacingDensity{
			SceneDensity: v["pacing_density.scene_density"], InformationRelease: v["pacing_density.information_release"],
			DialogueActionRatio: v["pacing_density.dialogue_action_ratio"], CompressionRules: v["pacing_density.compression_rules"],
		},
		ReaderEngagement: SimulationReaderEngagement{
			Methods: v["reader_engagement.methods"], EmotionalDrivers: v["reader_engagement.emotional_drivers"],
			ProgressionRewards: v["reader_engagement.progression_rewards"], AntiPatterns: v["reader_engagement.anti_patterns"],
		},
		RoleGuidance: SimulationRoleGuidance{
			Coordinator: v["role_guidance.coordinator"], Architect: v["role_guidance.architect"],
			Writer: v["role_guidance.writer"], Editor: v["role_guidance.editor"],
		},
	}
}
