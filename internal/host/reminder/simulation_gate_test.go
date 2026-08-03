package reminder

import (
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestWriterSimulationCheckBlockMessageRequiresCurrentReport(t *testing.T) {
	st := newTestStore(t)
	confidence, coverage := 0.95, 0.8
	profile := domain.SimulationProfileV2{
		Version:   domain.SimulationPortableProfileVersion,
		CreatedAt: time.Unix(1, 0).UTC().Format(time.RFC3339),
		UpdatedAt: time.Unix(1, 0).UTC().Format(time.RFC3339),
		Corpus: domain.SimulationPortableCorpus{
			Digest: strings.Repeat("a", 64), SourceCount: 4,
		},
		Analysis: domain.SimulationAnalysisMetadata{
			SourceAnalysisSignature: "source", SchemaSignature: "schema",
			AggregationSignature: "aggregation",
		},
		Features: []domain.SimulationFeature{{
			ID: "style-1", Dimension: "style.sentence_rhythm",
			Statement: "vary sentence rhythm", Classification: "stable",
			SupportCount: 4, Confidence: &confidence, Coverage: &coverage, Safety: "guidance",
		}},
		Capabilities: domain.SimulationCapabilities{
			Portable: true, AnalysisSigned: true,
		},
		Health: domain.SimulationProfileHealth{State: "portable_only"},
	}
	if err := domain.SetSimulationProfileDigest(&profile); err != nil {
		t.Fatal(err)
	}
	if err := st.Simulation.SavePortable(profile); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.EnsureSimulationContract(domain.SimulationModeNormal); err != nil {
		t.Fatal(err)
	}
	if message := writerSimulationCheckBlockMessage(st, 1, "原创草稿"); !strings.Contains(message, "check_simulation") ||
		!strings.Contains(message, "report_missing") {
		t.Fatalf("missing report reminder = %q", message)
	}
}
