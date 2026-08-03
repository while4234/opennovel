package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxSimulationCandidatesPerReport = 128
	MaxSimulationSafetyMarkers       = 256
	maxSimulationCandidateRunes      = 240
	maxSimulationSafetyMarkerRunes   = 80
)

// BuildSimulationAnalysisArtifacts creates the canonical portable/local pair
// after source reports have been reduced by code. The compatibility synthesis
// is deliberately not used as the source of feature statistics.
func BuildSimulationAnalysisArtifacts(
	profile SimulationProfile,
	metadata SimulationAnalysisMetadata,
	features []SimulationFeature,
	evidenceRefs []string,
	health SimulationProfileHealth,
	safetyIndex *SimulationSafetyIndex,
) (SimulationProfileV2, SimulationLocalEvidence, error) {
	createdAt, updatedAt := normalizedSimulationTimes(profile.CreatedAt, profile.UpdatedAt)
	portable := SimulationProfileV2{
		Version:   SimulationPortableProfileVersion,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		Corpus: SimulationPortableCorpus{
			Digest:      simulationCorpusDigest(profile.Corpus.Sources),
			SourceCount: len(profile.Corpus.Sources),
		},
		Analysis:     metadata,
		EvidenceRefs: append([]string(nil), evidenceRefs...),
		Features:     append([]SimulationFeature(nil), features...),
		Capabilities: SimulationCapabilities{
			Portable:      true,
			LocalEvidence: len(profile.Corpus.Sources) > 0 || len(profile.SourceReports) > 0,
			AnalysisSigned: strings.TrimSpace(metadata.SourceAnalysisSignature) != "" &&
				strings.TrimSpace(metadata.SplitterSignature) != "" &&
				strings.TrimSpace(metadata.SchemaSignature) != "" &&
				strings.TrimSpace(metadata.SynthesisSignature) != "" &&
				strings.TrimSpace(metadata.AggregationSignature) != "",
			SafetyIndex: safetyIndex != nil && len(safetyIndex.Entries) > 0,
		},
		Health: health,
	}
	sort.Strings(portable.EvidenceRefs)
	sort.Slice(portable.Features, func(i, j int) bool { return portable.Features[i].ID < portable.Features[j].ID })
	if err := SetSimulationProfileDigest(&portable); err != nil {
		return SimulationProfileV2{}, SimulationLocalEvidence{}, err
	}
	evidence := SimulationLocalEvidence{
		Version:       SimulationLocalEvidenceVersion,
		ProfileDigest: portable.ProfileDigest,
		SourceDir:     profile.Corpus.SourceDir,
		Sources:       append([]SimulationSource(nil), profile.Corpus.Sources...),
		SourceReports: append([]SimulationSourceReport(nil), profile.SourceReports...),
		SafetyIndex:   safetyIndex,
	}
	if err := ValidateSimulationProfileV2(&portable); err != nil {
		return SimulationProfileV2{}, SimulationLocalEvidence{}, err
	}
	if err := ValidateSimulationLocalEvidence(&evidence); err != nil {
		return SimulationProfileV2{}, SimulationLocalEvidence{}, err
	}
	return portable, evidence, nil
}

// NormalizeAndValidateSimulationSourceReport enforces the structured analyzer
// contract before a report is allowed into local evidence.
func NormalizeAndValidateSimulationSourceReport(report *SimulationSourceReport) error {
	if report == nil {
		return fmt.Errorf("simulation source report is nil")
	}
	report.ContentType = strings.ToLower(strings.TrimSpace(report.ContentType))
	report.Health = strings.ToLower(strings.TrimSpace(report.Health))
	report.Summary = strings.TrimSpace(report.Summary)
	if report.Summary == "" {
		return fmt.Errorf("simulation source report requires summary")
	}
	switch report.ContentType {
	case "body", "preface", "announcement", "appendix", "interaction", "catalog", "metadata", "fanwork", "mixed":
	default:
		return fmt.Errorf("simulation source report content_type is invalid")
	}
	switch report.Health {
	case "complete", "low_coverage", "questionable", "excluded":
	default:
		return fmt.Errorf("simulation source report health is invalid")
	}
	if report.Coverage == nil || *report.Coverage <= 0 || *report.Coverage > 1 {
		return fmt.Errorf("simulation source report coverage is invalid")
	}
	if len(report.Candidates) == 0 || len(report.Candidates) > MaxSimulationCandidatesPerReport {
		return fmt.Errorf("simulation source report requires bounded candidates")
	}
	if len(report.SafetyMarkers) > MaxSimulationSafetyMarkers {
		return fmt.Errorf("simulation source report has too many safety markers")
	}
	markers := make([]string, 0, len(report.SafetyMarkers))
	for i := range report.SafetyMarkers {
		marker := &report.SafetyMarkers[i]
		marker.Kind = strings.ToLower(strings.TrimSpace(marker.Kind))
		marker.Value = strings.TrimSpace(marker.Value)
		switch marker.Kind {
		case "proper_noun", "rare_phrase", "signature_phrase":
		default:
			return fmt.Errorf("simulation safety marker kind is invalid")
		}
		if utf8.RuneCountInString(marker.Value) < 2 || utf8.RuneCountInString(marker.Value) > maxSimulationSafetyMarkerRunes {
			return fmt.Errorf("simulation safety marker value is invalid")
		}
		markers = append(markers, strings.ToLower(marker.Value))
	}
	seen := make(map[string]struct{}, len(report.Candidates))
	for i := range report.Candidates {
		candidate := &report.Candidates[i]
		for _, phase := range candidate.Phases {
			if !validSimulationAnalysisPhase(phase) {
				return fmt.Errorf("simulation candidate[%d] phase is invalid", i)
			}
		}
		candidate.Dimension = strings.ToLower(strings.TrimSpace(candidate.Dimension))
		candidate.Statement = normalizeSimulationStatement(candidate.Statement)
		candidate.Scope = strings.ToLower(strings.TrimSpace(candidate.Scope))
		candidate.Tendency = strings.ToLower(strings.TrimSpace(candidate.Tendency))
		candidate.Safety = strings.ToLower(strings.TrimSpace(candidate.Safety))
		candidate.Phases = normalizedSimulationPhases(candidate.Phases)
		candidate.Contradicts = normalizedSimulationStatements(candidate.Contradicts)
		if candidate.Dimension == "" || candidate.Statement == "" || utf8.RuneCountInString(candidate.Statement) < 4 {
			return fmt.Errorf("simulation candidate[%d] is incomplete", i)
		}
		if utf8.RuneCountInString(candidate.Statement) > maxSimulationCandidateRunes ||
			looksLikeAbsoluteSimulationPath(candidate.Statement) ||
			containsSuspiciousSimulationQuote(candidate.Statement) {
			return fmt.Errorf("simulation candidate[%d] contains unsafe source content", i)
		}
		switch candidate.Dimension {
		case "lexicon.common_words", "lexicon.scene_words", "lexicon.signature_phrases":
			return fmt.Errorf("simulation candidate[%d] uses surface lexicon guidance", i)
		}
		switch candidate.Scope {
		case "", "global", "opening", "middle", "ending", "scene":
		default:
			return fmt.Errorf("simulation candidate[%d] scope is invalid", i)
		}
		if candidate.Scope == "" && len(candidate.Phases) == 0 {
			return fmt.Errorf("simulation candidate[%d] requires an applicable phase or scope", i)
		}
		switch candidate.Tendency {
		case "stable", "local":
		default:
			return fmt.Errorf("simulation candidate[%d] tendency is invalid", i)
		}
		switch candidate.Safety {
		case "guidance", "avoid", "blocked":
		default:
			return fmt.Errorf("simulation candidate[%d] safety is invalid", i)
		}
		if candidate.Confidence < 0 || candidate.Confidence > 1 {
			return fmt.Errorf("simulation candidate[%d] confidence is invalid", i)
		}
		lowerStatement := strings.ToLower(candidate.Statement)
		for _, marker := range markers {
			if strings.Contains(lowerStatement, marker) {
				return fmt.Errorf("simulation candidate[%d] contains local safety material", i)
			}
		}
		key := simulationCandidateKey(*candidate)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("simulation source report contains duplicate candidates")
		}
		seen[key] = struct{}{}
	}
	sort.Slice(report.Candidates, func(i, j int) bool {
		return simulationCandidateKey(report.Candidates[i]) < simulationCandidateKey(report.Candidates[j])
	})
	return nil
}

func validSimulationAnalysisPhase(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "cocreate", "planning", "chapter", "review":
		return true
	default:
		return false
	}
}

// AggregateSimulationEvidence computes all portable statistics before any LLM
// wording compression. Its output is independent of report input order.
func AggregateSimulationEvidence(reports []SimulationSourceReport, now time.Time) ([]SimulationFeature, []string, *SimulationSafetyIndex) {
	canonical := append([]SimulationSourceReport(nil), reports...)
	sort.Slice(canonical, func(i, j int) bool { return simulationReportRef(canonical[i]) < simulationReportRef(canonical[j]) })

	eligibleReports := 0
	for _, report := range canonical {
		if simulationReportSupportsStableEvidence(report) {
			eligibleReports++
		}
	}
	type aggregate struct {
		candidate    SimulationTechniqueCandidate
		refs         map[string]struct{}
		eligibleRefs map[string]struct{}
		confTotal    float64
		confWeight   float64
		safety       string
	}
	groups := make(map[string]*aggregate)
	allRefs := make(map[string]struct{})
	for _, report := range canonical {
		ref := simulationReportRef(report)
		allRefs[ref] = struct{}{}
		for _, candidate := range report.Candidates {
			key := simulationCandidateKey(candidate)
			group := groups[key]
			if group == nil {
				group = &aggregate{
					candidate:    candidate,
					refs:         make(map[string]struct{}),
					eligibleRefs: make(map[string]struct{}),
					safety:       candidate.Safety,
				}
				groups[key] = group
			}
			if _, exists := group.refs[ref]; !exists {
				group.refs[ref] = struct{}{}
				weight := simulationEvidenceWeight(report)
				group.confTotal += candidate.Confidence * weight
				group.confWeight += weight
			}
			if simulationReportSupportsStableEvidence(report) {
				group.eligibleRefs[ref] = struct{}{}
			}
			group.safety = stricterSimulationSafety(group.safety, candidate.Safety)
		}
	}

	features := make([]SimulationFeature, 0, len(groups))
	featureByStatement := make(map[string]string, len(groups))
	groupByFeatureID := make(map[string]*aggregate, len(groups))
	for key, group := range groups {
		refs := sortedSimulationSet(group.refs)
		coverage := 0.0
		if eligibleReports > 0 {
			coverage = float64(len(group.eligibleRefs)) / float64(eligibleReports)
			if coverage > 1 {
				coverage = 1
			}
		}
		confidence := 0.0
		if group.confWeight > 0 {
			confidence = group.confTotal / group.confWeight
		}
		classification := classifySimulationAggregate(group.candidate, len(group.eligibleRefs), eligibleReports, coverage)
		id := stableSimulationFeatureID(key)
		feature := SimulationFeature{
			ID:             id,
			Dimension:      group.candidate.Dimension,
			Statement:      group.candidate.Statement,
			Classification: classification,
			Phases:         append([]string(nil), group.candidate.Phases...),
			Scopes:         simulationCandidateScopes(group.candidate),
			SupportCount:   len(refs),
			Coverage:       floatPointer(coverage),
			Confidence:     floatPointer(confidence),
			EvidenceRefs:   refs,
			Safety:         group.safety,
		}
		features = append(features, feature)
		featureByStatement[group.candidate.Dimension+"\x00"+normalizeSimulationStatement(group.candidate.Statement)] = id
		groupByFeatureID[id] = group
	}
	for i := range features {
		group := groupByFeatureID[features[i].ID]
		if group == nil {
			continue
		}
		contradictions := make(map[string]struct{})
		for _, statement := range group.candidate.Contradicts {
			if id := featureByStatement[group.candidate.Dimension+"\x00"+statement]; id != "" && id != features[i].ID {
				contradictions[id] = struct{}{}
			}
		}
		features[i].ContradictionRefs = sortedSimulationSet(contradictions)
		if len(features[i].ContradictionRefs) > 0 {
			features[i].Classification = "contradictory"
		}
	}
	featureIndexes := make(map[string]int, len(features))
	for i := range features {
		featureIndexes[features[i].ID] = i
	}
	for i := range features {
		for _, contradictionID := range features[i].ContradictionRefs {
			targetIndex, ok := featureIndexes[contradictionID]
			if !ok {
				continue
			}
			features[targetIndex].ContradictionRefs = mergeSimulationStrings(
				features[targetIndex].ContradictionRefs,
				[]string{features[i].ID},
			)
			features[targetIndex].Classification = "contradictory"
		}
	}
	sort.Slice(features, func(i, j int) bool { return features[i].ID < features[j].ID })
	evidenceRefs := sortedSimulationSet(allRefs)
	return features, evidenceRefs, buildSimulationSafetyIndex(canonical, now)
}

// SimulationSynthesisGuidanceFeatures converts the corpus-wide synthesis into
// canonical advisory features. The deterministic source-report reducer remains
// the only authority for support and coverage statistics, so synthesized
// guidance is never eligible for a must obligation.
func SimulationSynthesisGuidanceFeatures(synthesis SimulationSynthesis) []SimulationFeature {
	fields := simulationSynthesisFields(synthesis)
	features := make([]SimulationFeature, 0)
	seen := make(map[string]struct{})
	for _, field := range fields {
		for _, rawStatement := range field.items {
			statement := normalizeSimulationStatement(rawStatement)
			if statement == "" || looksLikeAbsoluteSimulationPath(statement) ||
				containsSuspiciousSimulationQuote(statement) {
				continue
			}
			key := field.dimension + "\x00" + statement
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			safety := "guidance"
			if strings.HasSuffix(field.dimension, ".do_not_copy") ||
				strings.HasSuffix(field.dimension, ".anti_patterns") {
				safety = "avoid"
			}
			features = append(features, SimulationFeature{
				ID:             "synthesis-" + stableSimulationFeatureID(key)[len("feature-"):],
				Dimension:      field.dimension,
				Statement:      statement,
				Classification: "local",
				Safety:         safety,
			})
		}
	}
	sort.Slice(features, func(i, j int) bool { return features[i].ID < features[j].ID })
	return features
}

func SimulationCorpusDigest(sources []SimulationSource) string {
	return simulationCorpusDigest(sources)
}

// MergeSimulationPortableProfiles combines compatible portable-only artifacts
// by stable feature identity. It refuses analysis metadata mismatches instead
// of falling back to legacy string concatenation.
func MergeSimulationPortableProfiles(left, right SimulationProfileV2, now time.Time) (SimulationProfileV2, error) {
	if err := ValidateSimulationProfileV2(&left); err != nil {
		return SimulationProfileV2{}, err
	}
	if err := ValidateSimulationProfileV2(&right); err != nil {
		return SimulationProfileV2{}, err
	}
	if !compatibleSimulationAnalysis(left.Analysis, right.Analysis) {
		return SimulationProfileV2{}, fmt.Errorf("portable simulation profiles use incompatible analysis signatures")
	}
	left = namespacePortableSimulationEvidence(left)
	right = namespacePortableSimulationEvidence(right)
	merged := left
	merged.UpdatedAt = now.UTC().Format(time.RFC3339)
	merged.Corpus.SourceCount = left.Corpus.SourceCount + right.Corpus.SourceCount
	digests := []string{left.Corpus.Digest, right.Corpus.Digest}
	sort.Strings(digests)
	sum := sha256.Sum256([]byte(strings.Join(digests, "\n")))
	merged.Corpus.Digest = hex.EncodeToString(sum[:])
	merged.Capabilities.LocalEvidence = false
	merged.Capabilities.SafetyIndex = false
	merged.Health = SimulationProfileHealth{State: "portable_only", Reasons: []string{"local_evidence_unavailable"}}

	evidenceRefs := make(map[string]struct{})
	for _, ref := range append(append([]string(nil), left.EvidenceRefs...), right.EvidenceRefs...) {
		evidenceRefs[ref] = struct{}{}
	}
	merged.EvidenceRefs = sortedSimulationSet(evidenceRefs)
	features := make(map[string]SimulationFeature, len(left.Features)+len(right.Features))
	for _, feature := range append(append([]SimulationFeature(nil), left.Features...), right.Features...) {
		existing, ok := features[feature.ID]
		if !ok {
			features[feature.ID] = feature
			continue
		}
		if existing.Dimension != feature.Dimension || normalizeSimulationStatement(existing.Statement) != normalizeSimulationStatement(feature.Statement) {
			return SimulationProfileV2{}, fmt.Errorf("portable simulation feature identity collision")
		}
		features[feature.ID] = mergePortableSimulationFeature(existing, feature)
	}
	merged.Features = make([]SimulationFeature, 0, len(features))
	for _, feature := range features {
		merged.Features = append(merged.Features, feature)
	}
	sort.Slice(merged.Features, func(i, j int) bool { return merged.Features[i].ID < merged.Features[j].ID })
	if err := SetSimulationProfileDigest(&merged); err != nil {
		return SimulationProfileV2{}, err
	}
	if err := ValidateSimulationProfileV2(&merged); err != nil {
		return SimulationProfileV2{}, err
	}
	return merged, nil
}

func namespacePortableSimulationEvidence(profile SimulationProfileV2) SimulationProfileV2 {
	prefix := "profile-" + profile.ProfileDigest[:12] + ":"
	profile.EvidenceRefs = append([]string(nil), profile.EvidenceRefs...)
	profile.Features = append([]SimulationFeature(nil), profile.Features...)
	replacements := make(map[string]string, len(profile.EvidenceRefs))
	for _, ref := range profile.EvidenceRefs {
		replacements[ref] = prefix + ref
	}
	for i, ref := range profile.EvidenceRefs {
		profile.EvidenceRefs[i] = replacements[ref]
	}
	for i := range profile.Features {
		profile.Features[i].EvidenceRefs = append([]string(nil), profile.Features[i].EvidenceRefs...)
		for j, ref := range profile.Features[i].EvidenceRefs {
			profile.Features[i].EvidenceRefs[j] = replacements[ref]
		}
	}
	return profile
}

func simulationReportSupportsStableEvidence(report SimulationSourceReport) bool {
	if report.ContentType != "body" && report.ContentType != "mixed" {
		return false
	}
	if report.Health != "complete" || report.Coverage == nil || *report.Coverage < 0.8 {
		return false
	}
	return true
}

func simulationEvidenceWeight(report SimulationSourceReport) float64 {
	if simulationReportSupportsStableEvidence(report) {
		return 1
	}
	if report.Coverage != nil && *report.Coverage > 0 {
		return 0.25 * *report.Coverage
	}
	return 0.1
}

func compatibleSimulationAnalysis(left, right SimulationAnalysisMetadata) bool {
	return left.SourceAnalysisSignature == right.SourceAnalysisSignature &&
		left.SplitterSignature == right.SplitterSignature &&
		left.SchemaSignature == right.SchemaSignature &&
		left.SynthesisSignature == right.SynthesisSignature &&
		left.AggregationSignature == right.AggregationSignature &&
		left.ModelIdentity == right.ModelIdentity
}

func mergePortableSimulationFeature(left, right SimulationFeature) SimulationFeature {
	leftSupport := left.SupportCount
	rightSupport := right.SupportCount
	refs := make(map[string]struct{}, len(left.EvidenceRefs)+len(right.EvidenceRefs))
	for _, ref := range append(append([]string(nil), left.EvidenceRefs...), right.EvidenceRefs...) {
		refs[ref] = struct{}{}
	}
	left.EvidenceRefs = sortedSimulationSet(refs)
	if len(left.EvidenceRefs) > 0 {
		left.SupportCount = len(left.EvidenceRefs)
	} else {
		left.SupportCount = leftSupport + rightSupport
	}
	left.Safety = stricterSimulationSafety(left.Safety, right.Safety)
	left.Disabled = left.Disabled || right.Disabled
	left.ContradictionRefs = mergeSimulationStrings(left.ContradictionRefs, right.ContradictionRefs)
	left.Phases = mergeSimulationStrings(left.Phases, right.Phases)
	left.Scopes = mergeSimulationStrings(left.Scopes, right.Scopes)
	left.Roles = mergeSimulationStrings(left.Roles, right.Roles)
	if left.Classification != right.Classification {
		left.Classification = "contradictory"
	}
	if left.Confidence != nil && right.Confidence != nil {
		left.Confidence = floatPointer((*left.Confidence + *right.Confidence) / 2)
	}
	if left.Coverage != nil && right.Coverage != nil {
		left.Coverage = floatPointer((*left.Coverage + *right.Coverage) / 2)
	}
	return left
}

func simulationCandidateScopes(candidate SimulationTechniqueCandidate) []string {
	if candidate.Scope == "" {
		return nil
	}
	return []string{candidate.Scope}
}

func mergeSimulationStrings(left, right []string) []string {
	values := make(map[string]struct{}, len(left)+len(right))
	for _, value := range append(append([]string(nil), left...), right...) {
		values[value] = struct{}{}
	}
	return sortedSimulationSet(values)
}

func classifySimulationAggregate(candidate SimulationTechniqueCandidate, support, eligible int, coverage float64) string {
	if candidate.Tendency == "local" || candidate.Scope != "" && candidate.Scope != "global" {
		return "local"
	}
	if support >= 2 && coverage >= 0.5 {
		return "stable"
	}
	if eligible >= 3 && support == 1 {
		return "outlier"
	}
	return "local"
}

func buildSimulationSafetyIndex(reports []SimulationSourceReport, now time.Time) *SimulationSafetyIndex {
	type entryAggregate struct {
		kind  string
		value string
		refs  map[string]struct{}
	}
	entries := make(map[string]*entryAggregate)
	for _, report := range reports {
		ref := simulationReportRef(report)
		for _, marker := range report.SafetyMarkers {
			key := marker.Kind + "\x00" + strings.ToLower(strings.TrimSpace(marker.Value))
			entry := entries[key]
			if entry == nil {
				entry = &entryAggregate{kind: marker.Kind, value: marker.Value, refs: make(map[string]struct{})}
				entries[key] = entry
			}
			entry.refs[ref] = struct{}{}
		}
	}
	if len(entries) == 0 {
		return nil
	}
	index := &SimulationSafetyIndex{
		Version:   SimulationSafetyIndexVersion,
		UpdatedAt: now.UTC().Format(time.RFC3339),
		Entries:   make([]SimulationSafetyIndexEntry, 0, len(entries)),
	}
	for key, aggregate := range entries {
		sum := sha256.Sum256([]byte(key))
		index.Entries = append(index.Entries, SimulationSafetyIndexEntry{
			ID:           "safety-" + hex.EncodeToString(sum[:8]),
			Kind:         aggregate.kind,
			Value:        aggregate.value,
			EvidenceRefs: sortedSimulationSet(aggregate.refs),
		})
	}
	sort.Slice(index.Entries, func(i, j int) bool { return index.Entries[i].ID < index.Entries[j].ID })
	return index
}

func validateSimulationSafetyIndex(index *SimulationSafetyIndex) error {
	if index.Version != SimulationSafetyIndexVersion {
		return fmt.Errorf("unsupported simulation safety index version")
	}
	if _, err := time.Parse(time.RFC3339, index.UpdatedAt); err != nil {
		return fmt.Errorf("simulation safety index updated_at is invalid")
	}
	if len(index.Entries) > MaxSimulationEvidenceSources*MaxSimulationSafetyMarkers {
		return fmt.Errorf("simulation safety index exceeds item limits")
	}
	ids := make(map[string]struct{}, len(index.Entries))
	for _, entry := range index.Entries {
		if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Value) == "" || len(entry.EvidenceRefs) == 0 {
			return fmt.Errorf("simulation safety index entry is incomplete")
		}
		if utf8.RuneCountInString(entry.Value) > maxSimulationSafetyMarkerRunes || hasDuplicateSimulationStrings(entry.EvidenceRefs) {
			return fmt.Errorf("simulation safety index entry is invalid")
		}
		switch entry.Kind {
		case "proper_noun", "rare_phrase", "signature_phrase":
		default:
			return fmt.Errorf("simulation safety index entry kind is invalid")
		}
		if _, exists := ids[entry.ID]; exists {
			return fmt.Errorf("simulation safety index ids must be unique")
		}
		ids[entry.ID] = struct{}{}
	}
	return nil
}

func simulationCandidateKey(candidate SimulationTechniqueCandidate) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(candidate.Dimension)),
		normalizeSimulationStatement(candidate.Statement),
		strings.ToLower(strings.TrimSpace(candidate.Scope)),
		strings.Join(normalizedSimulationPhases(candidate.Phases), ","),
	}, "\x00")
}

func simulationReportRef(report SimulationSourceReport) string {
	identity := strings.TrimSpace(report.Fingerprint)
	if identity == "" {
		identity = SimulationSourceFingerprint(report.RelativePath, report.SHA256)
	}
	sum := sha256.Sum256([]byte(identity))
	return "report-" + hex.EncodeToString(sum[:12])
}

func stableSimulationFeatureID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "feature-" + hex.EncodeToString(sum[:12])
}

func normalizedSimulationPhases(values []string) []string {
	allowed := map[string]bool{"cocreate": true, "planning": true, "chapter": true, "review": true}
	seen := make(map[string]struct{})
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if allowed[value] {
			seen[value] = struct{}{}
		}
	}
	return sortedSimulationSet(seen)
}

func normalizedSimulationStatements(values []string) []string {
	seen := make(map[string]struct{})
	for _, value := range values {
		if normalized := normalizeSimulationStatement(value); normalized != "" {
			seen[normalized] = struct{}{}
		}
	}
	return sortedSimulationSet(seen)
}

func normalizeSimulationStatement(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func containsSuspiciousSimulationQuote(value string) bool {
	runes := []rune(value)
	for i, r := range runes {
		var closing rune
		switch r {
		case '“':
			closing = '”'
		case '"':
			closing = '"'
		case '「':
			closing = '」'
		default:
			continue
		}
		for j := i + 1; j < len(runes); j++ {
			if runes[j] == closing && j-i-1 > 24 {
				return true
			}
		}
	}
	// A long punctuation-free candidate is likely a copied sentence rather than
	// an executable abstract technique.
	if utf8.RuneCountInString(value) > 100 {
		punctuation := 0
		for _, r := range value {
			if unicode.IsPunct(r) {
				punctuation++
			}
		}
		return punctuation == 0
	}
	return false
}

func stricterSimulationSafety(left, right string) string {
	rank := map[string]int{"guidance": 1, "avoid": 2, "blocked": 3}
	if rank[right] > rank[left] {
		return right
	}
	return left
}

func sortedSimulationSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func floatPointer(value float64) *float64 {
	rounded := float64(int(value*1000000+0.5)) / 1000000
	return &rounded
}
