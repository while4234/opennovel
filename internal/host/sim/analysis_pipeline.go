package sim

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

const (
	simulationSplitterVersion     = "runner-multi-window.v1"
	simulationSourceSchemaVersion = "structured-technique.v1"
	simulationAnalyzerVersion     = "simulation-source-analyzer.v2"
	simulationReducerVersion      = "evidence-reducer.v2"
	simulationSelectionVersion    = "feature-selection.v2"
)

type simulationAnalysisSignatures struct {
	metadata domain.SimulationAnalysisMetadata
}

func buildSimulationAnalysisSignatures(deps Deps) simulationAnalysisSignatures {
	modelIdentity := strings.TrimSpace(deps.ModelIdentity)
	if modelIdentity == "" {
		modelIdentity = "unspecified"
	}
	splitter := simulationSignature(simulationSplitterVersion)
	schema := simulationSignature(simulationSourceSchemaVersion)
	source := simulationSignature(
		deps.Prompts.Source,
		splitter,
		schema,
		simulationAnalyzerVersion,
		modelIdentity,
	)
	synthesis := simulationSignature(
		deps.Prompts.Merge,
		"simulation-merge-schema.v2",
		simulationReducerVersion,
		simulationSelectionVersion,
	)
	aggregation := simulationSignature(simulationReducerVersion, simulationSelectionVersion)
	return simulationAnalysisSignatures{metadata: domain.SimulationAnalysisMetadata{
		SourceAnalysisSignature: source,
		SplitterSignature:       splitter,
		SchemaSignature:         schema,
		SynthesisSignature:      synthesis,
		AggregationSignature:    aggregation,
		ModelIdentity:           modelIdentity,
	}}
}

func simulationSignature(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

type sourceAnalysisWindow struct {
	Label     string `json:"label"`
	StartRune int    `json:"start_rune"`
	EndRune   int    `json:"end_rune"`
	Content   string `json:"content"`
}

type sourceAnalysisCoverage struct {
	Strategy   string                 `json:"strategy"`
	TotalRunes int                    `json:"total_runes"`
	UsedRunes  int                    `json:"used_runes"`
	Ratio      float64                `json:"ratio"`
	Windows    []sourceAnalysisWindow `json:"windows"`
}

func buildSourceAnalysisCoverage(content string) sourceAnalysisCoverage {
	runes := []rune(content)
	total := len(runes)
	if total <= maxSourceRunes {
		return sourceAnalysisCoverage{
			Strategy:   "full",
			TotalRunes: total,
			UsedRunes:  total,
			Ratio:      1,
			Windows: []sourceAnalysisWindow{{
				Label: "full", StartRune: 0, EndRune: total, Content: content,
			}},
		}
	}
	windowSize := maxSourceRunes / 3
	starts := []int{0, (total - windowSize) / 2, total - windowSize}
	labels := []string{"head", "middle", "tail"}
	windows := make([]sourceAnalysisWindow, 0, len(starts))
	used := 0
	for i, start := range starts {
		end := start + windowSize
		windows = append(windows, sourceAnalysisWindow{
			Label: labels[i], StartRune: start, EndRune: end, Content: string(runes[start:end]),
		})
		used += end - start
	}
	return sourceAnalysisCoverage{
		Strategy:   "head_middle_tail",
		TotalRunes: total,
		UsedRunes:  used,
		Ratio:      float64(used) / float64(total),
		Windows:    windows,
	}
}

func buildStructuredSourceUserPrompt(source scannedSource) (string, sourceAnalysisCoverage) {
	coverage := buildSourceAnalysisCoverage(source.content)
	payload := map[string]any{
		"source_identity": map[string]any{
			"report_ref": "report-" + simulationSignature(source.Fingerprint)[:24],
			"size_bytes": source.SizeBytes,
		},
		"coverage": coverage,
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	return "Analyze this simulation corpus source and return only the requested JSON object.\n\n" + string(data), coverage
}

func reportHealthForCoverage(contentType string, coverage sourceAnalysisCoverage) string {
	switch contentType {
	case "body":
		if coverage.Ratio >= 0.8 {
			return "complete"
		}
		return "low_coverage"
	case "mixed":
		return "questionable"
	default:
		return "excluded"
	}
}

func saveSimulationAnalysisState(
	st *store.SimulationStore,
	profile domain.SimulationProfile,
	metadata domain.SimulationAnalysisMetadata,
	health domain.SimulationProfileHealth,
	now time.Time,
) error {
	if st == nil {
		return fmt.Errorf("simulation store is nil")
	}
	features, refs, safetyIndex := domain.AggregateSimulationEvidence(profile.SourceReports, now)
	features = append(features, domain.SimulationSynthesisGuidanceFeatures(profile.Synthesis)...)
	portable, evidence, err := domain.BuildSimulationAnalysisArtifacts(
		profile,
		metadata,
		features,
		refs,
		health,
		safetyIndex,
	)
	if err != nil {
		return err
	}
	return st.SaveAnalysis(portable, evidence)
}

func pendingSourcesForSignature(existing *domain.SimulationProfile, sources []scannedSource, sourceSignature string, allowLegacyReports bool) []scannedSource {
	if existing == nil {
		return append([]scannedSource(nil), sources...)
	}
	known := make(map[string]struct{}, len(existing.SourceReports))
	for _, report := range existing.SourceReports {
		if strings.TrimSpace(report.Summary) == "" ||
			(report.AnalysisSignature != sourceSignature && !(allowLegacyReports && report.AnalysisSignature == "")) {
			continue
		}
		fingerprint := strings.TrimSpace(report.Fingerprint)
		if fingerprint == "" && report.RelativePath != "" && report.SHA256 != "" {
			fingerprint = domain.SimulationSourceFingerprint(report.RelativePath, report.SHA256)
		}
		if fingerprint != "" {
			known[fingerprint] = struct{}{}
		}
	}
	var pending []scannedSource
	for _, source := range sources {
		if _, ok := known[source.Fingerprint]; !ok {
			pending = append(pending, source)
		}
	}
	return pending
}

func canonicalReportIdentities(reports []domain.SimulationSourceReport) []domain.SimulationReportIdentity {
	identities := reportIdentitiesForMerge(reports)
	sort.Slice(identities, func(i, j int) bool {
		if identities[i].Fingerprint == identities[j].Fingerprint {
			return identities[i].RelativePath < identities[j].RelativePath
		}
		return identities[i].Fingerprint < identities[j].Fingerprint
	})
	return identities
}
