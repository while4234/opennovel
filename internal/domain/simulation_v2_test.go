package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestProjectSimulationProfileV1SeparatesPortableAndLocalEvidence(t *testing.T) {
	legacy := syntheticSimulationV1Fixture()

	portable, evidence, err := ProjectSimulationProfileV1(legacy)
	if err != nil {
		t.Fatalf("ProjectSimulationProfileV1: %v", err)
	}
	data, err := MarshalSimulationPortableProfile(portable)
	if err != nil {
		t.Fatalf("MarshalSimulationPortableProfile: %v", err)
	}
	for _, forbidden := range []string{legacy.Corpus.SourceDir, "source_reports", "synthetic report", "chapter-01.txt"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("portable profile contains local-only value %q", forbidden)
		}
	}
	if portable.Health.State != "legacy" || !portable.Analysis.Legacy || portable.Capabilities.AnalysisSigned {
		t.Fatalf("legacy health metadata = %+v %+v", portable.Health, portable.Analysis)
	}
	if len(evidence.Sources) != 1 || len(evidence.SourceReports) != 1 || evidence.SourceDir == "" {
		t.Fatalf("local evidence was not preserved: %+v", evidence)
	}

	again, againEvidence, err := ProjectSimulationProfileV1(legacy)
	if err != nil {
		t.Fatalf("repeat projection: %v", err)
	}
	if !reflect.DeepEqual(portable, again) || !reflect.DeepEqual(evidence, againEvidence) {
		t.Fatal("v1 projection is not deterministic")
	}
}

func TestSimulationProfileV2CompatibilityRestoresSynthesisAndBoundEvidence(t *testing.T) {
	legacy := syntheticSimulationV1Fixture()
	portable, evidence, err := ProjectSimulationProfileV1(legacy)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := SimulationProfileV2CompatibilityProfile(portable, &evidence)
	if err != nil {
		t.Fatalf("SimulationProfileV2CompatibilityProfile: %v", err)
	}
	if !reflect.DeepEqual(restored.Synthesis, legacy.Synthesis) {
		t.Fatalf("synthesis changed during compatibility projection\n got: %#v\nwant: %#v", restored.Synthesis, legacy.Synthesis)
	}
	if !reflect.DeepEqual(restored.Corpus, legacy.Corpus) || !reflect.DeepEqual(restored.SourceReports, legacy.SourceReports) {
		t.Fatal("matching local evidence was not restored")
	}

	evidence.ProfileDigest = strings.Repeat("0", 64)
	restored, err = SimulationProfileV2CompatibilityProfile(portable, &evidence)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Corpus.Sources) != 0 || len(restored.SourceReports) != 0 {
		t.Fatal("mismatched local evidence must not be attached")
	}
}

func TestValidateSimulationProfileV2RejectsMalformedFeatures(t *testing.T) {
	portable, _, err := ProjectSimulationProfileV1(syntheticSimulationV1Fixture())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*SimulationProfileV2)
	}{
		{"duplicate id", func(p *SimulationProfileV2) { p.Features = append(p.Features, p.Features[0]) }},
		{"coverage out of range", func(p *SimulationProfileV2) {
			value := 1.1
			p.Features[0].Coverage = &value
		}},
		{"confidence out of range", func(p *SimulationProfileV2) {
			value := -0.1
			p.Features[0].Confidence = &value
		}},
		{"unknown evidence", func(p *SimulationProfileV2) { p.Features[0].EvidenceRefs = []string{"missing"} }},
		{"invalid enum", func(p *SimulationProfileV2) { p.Features[0].Classification = "certain" }},
		{"missing statement", func(p *SimulationProfileV2) { p.Features[0].Statement = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := portable
			candidate.Features = append([]SimulationFeature(nil), portable.Features...)
			tt.mutate(&candidate)
			_ = SetSimulationProfileDigest(&candidate)
			if err := ValidateSimulationProfileV2(&candidate); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateSimulationProfileV2DoesNotMutateCaller(t *testing.T) {
	portable, _, err := ProjectSimulationProfileV1(syntheticSimulationV1Fixture())
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(portable)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSimulationProfileV2(&portable); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(portable)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("validation mutated the caller")
	}
}

func TestUnmarshalSimulationPortableProfileRejectsUnknownFieldsAndOversize(t *testing.T) {
	portable, _, err := ProjectSimulationProfileV1(syntheticSimulationV1Fixture())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(portable)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data[:len(data)-1], []byte(`,"source_dir":"private"}`)...)
	if _, err := UnmarshalSimulationPortableProfile(data); err == nil {
		t.Fatal("unknown field was accepted")
	}
	if _, err := UnmarshalSimulationPortableProfile(make([]byte, MaxSimulationProfileBytes+1)); err == nil {
		t.Fatal("oversized profile was accepted")
	}
}

func TestValidateSimulationLocalEvidenceRejectsDuplicateAndDanglingReports(t *testing.T) {
	_, evidence, err := ProjectSimulationProfileV1(syntheticSimulationV1Fixture())
	if err != nil {
		t.Fatal(err)
	}
	duplicate := evidence
	duplicate.Sources = append(duplicate.Sources, duplicate.Sources[0])
	if err := ValidateSimulationLocalEvidence(&duplicate); err == nil {
		t.Fatal("duplicate source identity was accepted")
	}
	dangling := evidence
	dangling.SourceReports = append([]SimulationSourceReport(nil), dangling.SourceReports...)
	dangling.SourceReports[0].RelativePath = "missing.txt"
	dangling.SourceReports[0].Fingerprint = ""
	if err := ValidateSimulationLocalEvidence(&dangling); err == nil {
		t.Fatal("dangling report was accepted")
	}
}

func syntheticSimulationV1Fixture() SimulationProfile {
	fingerprint := SimulationSourceFingerprint("chapter-01.txt", strings.Repeat("a", 64))
	return SimulationProfile{
		Version:   SimulationProfileVersion,
		CreatedAt: "2026-01-01T00:00:00Z",
		UpdatedAt: "2026-01-02T00:00:00Z",
		Corpus: SimulationCorpusManifest{
			SourceDir: `C:\private\synthetic`,
			Sources: []SimulationSource{{
				RelativePath: "chapter-01.txt",
				SHA256:       strings.Repeat("a", 64),
				Fingerprint:  fingerprint,
			}},
		},
		SourceReports: []SimulationSourceReport{{
			RelativePath: "chapter-01.txt",
			SHA256:       strings.Repeat("a", 64),
			Fingerprint:  fingerprint,
			Summary:      "synthetic report",
		}},
		Synthesis: SimulationSynthesis{
			Style: SimulationStyle{
				NarrativeVoice: []string{"Use close narration with varied sentence length."},
				DoNotCopy:      []string{"Avoid source-specific wording."},
			},
			PlotDesign: SimulationPlotDesign{OpeningPatterns: []string{"Open with a concrete decision."}},
			RoleGuidance: SimulationRoleGuidance{
				Writer: []string{"Advance the scene through observable choices."},
			},
		},
	}
}
