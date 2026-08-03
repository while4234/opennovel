package sim

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestImportPortableProfilePreservesIdentityWithoutLocalEvidence(t *testing.T) {
	root := t.TempDir()
	st := store.NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	legacy := testProfile("portable.txt", "sha-portable", "synthetic portable report")
	portable, _, err := domain.ProjectSimulationProfileV1(legacy)
	if err != nil {
		t.Fatal(err)
	}
	data, err := domain.MarshalSimulationPortableProfile(portable)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "portable.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ImportProfile(context.Background(), Deps{Store: st}, path)
	if err != nil {
		t.Fatalf("ImportProfile: %v", err)
	}
	if result.ImportedSources != portable.Corpus.SourceCount {
		t.Fatalf("ImportedSources = %d, want %d", result.ImportedSources, portable.Corpus.SourceCount)
	}
	saved, err := st.Simulation.LoadPortable()
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.ProfileDigest != portable.ProfileDigest || saved.Corpus != portable.Corpus {
		t.Fatalf("portable identity changed: %+v", saved)
	}
	runtimeProfile, err := st.Simulation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeProfile.SourceReports) != 0 || runtimeProfile.Corpus.SourceDir != "" {
		t.Fatal("portable import acquired local evidence")
	}
	if !reflect.DeepEqual(runtimeProfile.Synthesis, legacy.Synthesis) {
		t.Fatal("portable import changed compatibility synthesis")
	}

	repeated, err := ImportProfile(context.Background(), Deps{Store: st}, path)
	if err != nil {
		t.Fatalf("repeat ImportProfile: %v", err)
	}
	if repeated.ImportedSources != 0 || repeated.SkippedSources != portable.Corpus.SourceCount {
		t.Fatalf("repeated import result = %+v, want all sources skipped", repeated)
	}
	repeatedPortable, err := st.Simulation.LoadPortable()
	if err != nil {
		t.Fatal(err)
	}
	if repeatedPortable == nil || repeatedPortable.ProfileDigest != portable.ProfileDigest ||
		repeatedPortable.Corpus.SourceCount != portable.Corpus.SourceCount {
		t.Fatalf("repeated portable import changed identity: %+v", repeatedPortable)
	}
}

func TestImportMergesCompatiblePortableOnlyProfilesByFeatureIdentity(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(filepath.Join(dir, "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	left, _, err := domain.ProjectSimulationProfileV1(testProfile("a.txt", strings.Repeat("a", 64), "left"))
	if err != nil {
		t.Fatal(err)
	}
	right, _, err := domain.ProjectSimulationProfileV1(testProfile("b.txt", strings.Repeat("b", 64), "right"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Simulation.SavePortable(left); err != nil {
		t.Fatal(err)
	}
	data, err := domain.MarshalSimulationPortableProfile(right)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "portable.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ImportProfile(context.Background(), Deps{Store: st}, path)
	if err != nil {
		t.Fatalf("ImportProfile: %v", err)
	}
	merged, err := st.Simulation.LoadPortable()
	if err != nil {
		t.Fatal(err)
	}
	if result.ImportedSources != 1 || merged == nil || merged.Corpus.SourceCount != 2 ||
		merged.Health.State != "portable_only" || merged.Capabilities.LocalEvidence {
		t.Fatalf("merged result=%+v profile=%+v", result, merged)
	}
}

func TestImportRejectsIncompatiblePortableProfileMerge(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(filepath.Join(dir, "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	left, _, _ := domain.ProjectSimulationProfileV1(testProfile("a.txt", strings.Repeat("a", 64), "left"))
	right, _, _ := domain.ProjectSimulationProfileV1(testProfile("b.txt", strings.Repeat("b", 64), "right"))
	left.Analysis.AggregationSignature = "aggregation-a"
	right.Analysis.AggregationSignature = "aggregation-b"
	if err := domain.SetSimulationProfileDigest(&left); err != nil {
		t.Fatal(err)
	}
	if err := domain.SetSimulationProfileDigest(&right); err != nil {
		t.Fatal(err)
	}
	if err := st.Simulation.SavePortable(left); err != nil {
		t.Fatal(err)
	}
	data, err := domain.MarshalSimulationPortableProfile(right)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "incompatible.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportProfile(context.Background(), Deps{Store: st}, path); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("incompatible import error = %v", err)
	}
}

func TestImportProfileValidatesSchemaAndMergesByFingerprint(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	existing := testProfile("a.txt", "sha-a", "old")
	if err := st.Simulation.Save(existing); err != nil {
		t.Fatal(err)
	}

	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badPath, []byte(`{"version":"wrong"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportProfile(context.Background(), Deps{Store: st}, badPath); err == nil || !strings.Contains(err.Error(), "unsupported simulation profile") {
		t.Fatalf("expected schema validation error, got %v", err)
	}

	if err := st.Simulation.SaveMergeCheckpoint(testMergeCheckpoint("a.txt", "sha-a")); err != nil {
		t.Fatal(err)
	}

	imported := testProfile("b.txt", "sha-b", "new")
	imported.Corpus.Sources = append(imported.Corpus.Sources, existing.Corpus.Sources[0])
	imported.SourceReports = append(imported.SourceReports, existing.SourceReports[0])
	importPath := filepath.Join(dir, "profile.json")
	data, err := domain.MarshalSimulationProfile(imported)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(importPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	llm := &scriptedLLM{responses: []string{validSynthesisJSON("model merged import")}}
	result, err := ImportProfile(context.Background(), Deps{
		Store:   st,
		LLM:     llm,
		Prompts: Prompts{Merge: "merge prompt"},
	}, importPath)
	if err != nil {
		t.Fatalf("ImportProfile: %v", err)
	}
	if result.ImportedSources != 1 || result.SkippedSources != 1 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	if !result.ModelMerged {
		t.Fatal("expected import to re-synthesize with model")
	}
	if got := llm.calls.Load(); got != 1 {
		t.Fatalf("LLM calls = %d, want 1", got)
	}
	merged, err := st.Simulation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Corpus.Sources) != 2 || len(merged.SourceReports) != 2 {
		t.Fatalf("expected duplicate fingerprint to be skipped, got %+v", merged)
	}
	if got := merged.Synthesis.Style.ProseTexture; len(got) != 1 || got[0] != "model merged import" {
		t.Fatalf("synthesis was not replaced by model merge: %+v", merged.Synthesis)
	}
	checkpoint, err := st.Simulation.LoadMergeCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint != nil {
		t.Fatalf("import should clear merge checkpoint: %+v", checkpoint)
	}
}

func TestImportProfileBatchesLargeImportedProfiles(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	existing := testProfile("existing.txt", "sha-existing", "old")
	if err := st.Simulation.Save(existing); err != nil {
		t.Fatal(err)
	}

	reports := make([]domain.SimulationSourceReport, maxMergeReportsPerBatch+1)
	for i := range reports {
		reports[i] = verboseSourceReport(i + 1)
	}
	imported := profileFromReports(reports)
	importPath := filepath.Join(dir, "large-profile.json")
	data, err := domain.MarshalSimulationProfile(imported)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(importPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	responses := make([]string, len(reports)+1)
	for i := range responses {
		responses[i] = validSynthesisJSON("batched import")
	}
	llm := &scriptedLLM{responses: responses}

	result, err := ImportProfile(context.Background(), Deps{
		Store:   st,
		LLM:     llm,
		Prompts: Prompts{Merge: "merge prompt"},
	}, importPath)
	if err != nil {
		t.Fatalf("ImportProfile: %v", err)
	}
	if !result.ModelMerged {
		t.Fatal("expected model merge for imported sources")
	}
	if got := llm.calls.Load(); got <= 1 {
		t.Fatalf("LLM calls = %d, want batched calls", got)
	}
	merged, err := st.Simulation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Corpus.Sources) != len(reports)+1 {
		t.Fatalf("source count = %d, want %d", len(merged.Corpus.Sources), len(reports)+1)
	}
	checkpoint, err := st.Simulation.LoadMergeCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint != nil {
		t.Fatalf("final import should clear merge checkpoint: %+v", checkpoint)
	}
}

func testProfile(path, sha, summary string) domain.SimulationProfile {
	fp := domain.SimulationSourceFingerprint(path, sha)
	return domain.SimulationProfile{
		Version: domain.SimulationProfileVersion,
		Corpus: domain.SimulationCorpusManifest{
			Sources: []domain.SimulationSource{{
				RelativePath: path,
				SHA256:       sha,
				Fingerprint:  fp,
			}},
		},
		SourceReports: []domain.SimulationSourceReport{{
			RelativePath: path,
			SHA256:       sha,
			Fingerprint:  fp,
			Summary:      summary,
		}},
		Synthesis: domain.SimulationSynthesis{
			Style: domain.SimulationStyle{
				NarrativeVoice: []string{"close narration"},
			},
			RoleGuidance: domain.SimulationRoleGuidance{
				Writer: []string{"borrow structure only"},
			},
		},
	}
}

func profileFromReports(reports []domain.SimulationSourceReport) domain.SimulationProfile {
	profile := domain.SimulationProfile{
		Version:   domain.SimulationProfileVersion,
		Synthesis: synthesisFixture("imported profile"),
	}
	for _, report := range reports {
		profile.Corpus.Sources = append(profile.Corpus.Sources, domain.SimulationSource{
			RelativePath: report.RelativePath,
			SHA256:       report.SHA256,
			Fingerprint:  report.Fingerprint,
		})
		profile.SourceReports = append(profile.SourceReports, report)
	}
	return profile
}

func testMergeCheckpoint(path, sha string) domain.SimulationMergeCheckpoint {
	return domain.SimulationMergeCheckpoint{
		Version:              domain.SimulationMergeCheckpointVersion,
		TotalReportCount:     1,
		ProcessedReportCount: 1,
		ProcessedBatchCount:  1,
		Reports: []domain.SimulationReportIdentity{{
			RelativePath: path,
			SHA256:       sha,
			Fingerprint:  domain.SimulationSourceFingerprint(path, sha),
		}},
		RollingSynthesis: domain.SimulationSynthesis{
			Style: domain.SimulationStyle{
				NarrativeVoice: []string{"close narration"},
			},
		},
	}
}
