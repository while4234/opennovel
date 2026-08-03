package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAggregateSimulationEvidenceIsOrderInvariant(t *testing.T) {
	coverage := 1.0
	reports := []SimulationSourceReport{
		structuredEvidenceReport("b.txt", "sha-b", "alternate sentence lengths", coverage),
		structuredEvidenceReport("a.txt", "sha-a", "alternate sentence lengths", coverage),
		structuredEvidenceReport("c.txt", "sha-c", "delay explanations until after action", coverage),
	}
	firstFeatures, firstRefs, _ := AggregateSimulationEvidence(reports, time.Unix(100, 0))
	reversed := []SimulationSourceReport{reports[2], reports[0], reports[1]}
	secondFeatures, secondRefs, _ := AggregateSimulationEvidence(reversed, time.Unix(100, 0))
	if !reflect.DeepEqual(firstFeatures, secondFeatures) || !reflect.DeepEqual(firstRefs, secondRefs) {
		t.Fatalf("aggregate depends on input order:\nfirst=%+v\nsecond=%+v", firstFeatures, secondFeatures)
	}
	if len(firstFeatures) != 2 {
		t.Fatalf("features = %d, want 2", len(firstFeatures))
	}
	var stable *SimulationFeature
	for i := range firstFeatures {
		if firstFeatures[i].Statement == "alternate sentence lengths" {
			stable = &firstFeatures[i]
		}
	}
	if stable == nil || stable.Classification != "stable" || stable.SupportCount != 2 || stable.Coverage == nil || *stable.Coverage != 0.666667 {
		t.Fatalf("stable feature statistics = %+v", stable)
	}
}

func TestAggregateSimulationEvidenceTreatsPhasesAsApplicability(t *testing.T) {
	coverage := 1.0
	first := structuredEvidenceReport("a.txt", "sha-a", "alternate sentence lengths", coverage)
	second := structuredEvidenceReport("b.txt", "sha-b", "alternate sentence lengths", coverage)
	first.Candidates[0].Phases = []string{"chapter"}
	second.Candidates[0].Phases = []string{"chapter"}

	features, _, _ := AggregateSimulationEvidence(
		[]SimulationSourceReport{first, second},
		time.Unix(100, 0),
	)

	if len(features) != 1 || features[0].Classification != "stable" {
		t.Fatalf("phase-scoped stable evidence was misclassified: %+v", features)
	}
}

func TestSimulationSynthesisGuidanceFeaturesAreCanonicalAdvisories(t *testing.T) {
	features := SimulationSynthesisGuidanceFeatures(SimulationSynthesis{
		PlotDesign: SimulationPlotDesign{
			OpeningPatterns: []string{"begin with an unresolved choice"},
		},
		Style: SimulationStyle{
			DoNotCopy: []string{"do not repeat source-specific set pieces"},
		},
	})

	if len(features) != 2 {
		t.Fatalf("features = %d, want 2", len(features))
	}
	for _, feature := range features {
		if feature.Classification != "local" || feature.SupportCount != 0 ||
			!strings.HasPrefix(feature.ID, "synthesis-") {
			t.Fatalf("synthesis feature claimed evidence statistics: %+v", feature)
		}
	}
}

func TestAggregateSimulationEvidenceExcludesNonBodyFromStableSupport(t *testing.T) {
	coverage := 1.0
	body := structuredEvidenceReport("body.txt", "sha-body", "use action before explanation", coverage)
	notice := structuredEvidenceReport("notice.txt", "sha-notice", "use action before explanation", coverage)
	notice.ContentType = "announcement"
	notice.Health = "excluded"
	features, _, _ := AggregateSimulationEvidence([]SimulationSourceReport{body, notice}, time.Unix(100, 0))
	if len(features) != 1 || features[0].Classification == "stable" || features[0].SupportCount != 2 {
		t.Fatalf("non-body evidence incorrectly became stable: %+v", features)
	}
}

func TestAggregateSimulationEvidencePreservesScopedContradictions(t *testing.T) {
	coverage := 1.0
	opening := structuredEvidenceReport("opening.txt", "sha-opening", "reveal the threat before context", coverage)
	opening.Candidates[0].Scope = "opening"
	opening.Candidates[0].Contradicts = []string{"delay the threat until context is clear"}
	middle := structuredEvidenceReport("middle.txt", "sha-middle", "delay the threat until context is clear", coverage)
	middle.Candidates[0].Scope = "middle"
	features, _, _ := AggregateSimulationEvidence([]SimulationSourceReport{opening, middle}, time.Unix(100, 0))
	if len(features) != 2 {
		t.Fatalf("features = %+v", features)
	}
	for _, feature := range features {
		if feature.Classification != "contradictory" || len(feature.ContradictionRefs) != 1 || len(feature.Scopes) != 1 {
			t.Fatalf("scoped contradiction lost: %+v", feature)
		}
	}
}

func TestNormalizeSimulationSourceReportRejectsSurfaceAndMarkerLeaks(t *testing.T) {
	coverage := 1.0
	base := structuredEvidenceReport("a.txt", "sha-a", "alternate sentence lengths", coverage)
	base.SafetyMarkers = []SimulationSafetyMarker{{Kind: "proper_noun", Value: "CloudCity"}}

	surface := base
	surface.Candidates = append([]SimulationTechniqueCandidate(nil), base.Candidates...)
	surface.Candidates[0].Dimension = "lexicon.signature_phrases"
	if err := NormalizeAndValidateSimulationSourceReport(&surface); err == nil || !strings.Contains(err.Error(), "surface lexicon") {
		t.Fatalf("surface candidate error = %v", err)
	}

	leak := base
	leak.Candidates = append([]SimulationTechniqueCandidate(nil), base.Candidates...)
	leak.Candidates[0].Statement = "repeat CloudCity at every reveal"
	if err := NormalizeAndValidateSimulationSourceReport(&leak); err == nil || !strings.Contains(err.Error(), "local safety material") {
		t.Fatalf("marker leak error = %v", err)
	}
}

func TestBuildSimulationAnalysisArtifactsKeepsSafetyValuesLocal(t *testing.T) {
	coverage := 1.0
	report := structuredEvidenceReport("a.txt", "sha-a", "alternate sentence lengths", coverage)
	report.SafetyMarkers = []SimulationSafetyMarker{{Kind: "proper_noun", Value: "CloudCity"}}
	features, refs, index := AggregateSimulationEvidence([]SimulationSourceReport{report}, time.Unix(100, 0).UTC())
	profile := SimulationProfile{
		Version:   SimulationProfileVersion,
		CreatedAt: time.Unix(100, 0).UTC().Format(time.RFC3339),
		UpdatedAt: time.Unix(100, 0).UTC().Format(time.RFC3339),
		Corpus: SimulationCorpusManifest{Sources: []SimulationSource{{
			RelativePath: "a.txt", SHA256: "sha-a", Fingerprint: SimulationSourceFingerprint("a.txt", "sha-a"),
		}}},
		SourceReports: []SimulationSourceReport{report},
	}
	signature := strings.Repeat("a", 64)
	portable, local, err := BuildSimulationAnalysisArtifacts(profile, SimulationAnalysisMetadata{
		SourceAnalysisSignature: signature,
		SplitterSignature:       signature,
		SchemaSignature:         signature,
		SynthesisSignature:      signature,
		AggregationSignature:    signature,
		ModelIdentity:           "test/model",
	}, features, refs, SimulationProfileHealth{State: "fresh"}, index)
	if err != nil {
		t.Fatalf("BuildSimulationAnalysisArtifacts: %v", err)
	}
	portableJSON, _ := json.Marshal(portable)
	localJSON, _ := json.Marshal(local)
	if strings.Contains(string(portableJSON), "CloudCity") {
		t.Fatalf("portable profile leaked safety marker: %s", portableJSON)
	}
	if !strings.Contains(string(localJSON), "CloudCity") || !portable.Capabilities.SafetyIndex {
		t.Fatalf("local safety boundary missing marker: %s", localJSON)
	}
}

func TestMergeSimulationPortableProfilesUsesFeatureIdentityAndRejectsIncompatibleAnalysis(t *testing.T) {
	left := portableAnalysisFixture(t, "feature-a", "alternate sentence lengths", "a")
	right := portableAnalysisFixture(t, "feature-b", "delay explanations until after action", "a")
	merged, err := MergeSimulationPortableProfiles(left, right, time.Unix(200, 0))
	if err != nil {
		t.Fatalf("MergeSimulationPortableProfiles: %v", err)
	}
	if len(merged.Features) != 2 || merged.Corpus.SourceCount != 2 || merged.Health.State != "portable_only" || merged.Capabilities.LocalEvidence {
		t.Fatalf("merged portable profile = %+v", merged)
	}
	incompatible := portableAnalysisFixture(t, "feature-c", "compress transitions", "b")
	if _, err := MergeSimulationPortableProfiles(left, incompatible, time.Unix(200, 0)); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("incompatible merge error = %v", err)
	}
}

func structuredEvidenceReport(path, sha, statement string, coverage float64) SimulationSourceReport {
	return SimulationSourceReport{
		RelativePath: path,
		SHA256:       sha,
		Fingerprint:  SimulationSourceFingerprint(path, sha),
		ContentType:  "body",
		Coverage:     &coverage,
		Health:       "complete",
		Summary:      "synthetic evidence",
		Candidates: []SimulationTechniqueCandidate{{
			Dimension:  "style.sentence_rhythm",
			Statement:  statement,
			Scope:      "global",
			Confidence: 0.9,
			Tendency:   "stable",
			Safety:     "guidance",
		}},
	}
}

func portableAnalysisFixture(t *testing.T, id, statement, signatureSeed string) SimulationProfileV2 {
	t.Helper()
	signature := strings.Repeat(signatureSeed, 64)
	coverage := 1.0
	confidence := 0.9
	profile := SimulationProfileV2{
		Version:   SimulationPortableProfileVersion,
		CreatedAt: time.Unix(100, 0).UTC().Format(time.RFC3339),
		UpdatedAt: time.Unix(100, 0).UTC().Format(time.RFC3339),
		Corpus: SimulationPortableCorpus{
			Digest: strings.Repeat("c", 64), SourceCount: 1,
		},
		Analysis: SimulationAnalysisMetadata{
			SourceAnalysisSignature: signature,
			SplitterSignature:       signature,
			SchemaSignature:         signature,
			SynthesisSignature:      signature,
			AggregationSignature:    signature,
			ModelIdentity:           "test/model",
		},
		EvidenceRefs: []string{"report-" + id},
		Features: []SimulationFeature{{
			ID: id, Dimension: "style.sentence_rhythm", Statement: statement,
			Classification: "local", SupportCount: 1, Coverage: &coverage, Confidence: &confidence,
			EvidenceRefs: []string{"report-" + id}, Safety: "guidance",
		}},
		Capabilities: SimulationCapabilities{Portable: true, AnalysisSigned: true},
		Health:       SimulationProfileHealth{State: "portable_only", Reasons: []string{"local_evidence_unavailable"}},
	}
	if err := SetSimulationProfileDigest(&profile); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSimulationProfileV2(&profile); err != nil {
		t.Fatal(err)
	}
	return profile
}
