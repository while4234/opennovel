package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/simulationcheck"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestCheckSimulationNormalBlocksCopyButLeavesShouldAdvisory(t *testing.T) {
	st, profile := simulationCheckTestStore(t, true)
	if err := st.Drafts.SaveDraft(1, "原创开场后，银蓝钟摆越过七级石阶后停在逆风里。"); err != nil {
		t.Fatal(err)
	}
	service := NewSimulationCheckService(st, domain.SimulationModeNormal)
	report, err := service.Check(context.Background(), 1)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.Passed || report.CopyStatus != simulationcheck.StatusFail || len(report.Risks) == 0 {
		t.Fatalf("normal copy risk did not block: %+v", report)
	}
	if report.ContractStatus == simulationcheck.StatusFail || len(report.MustChecks) != 0 {
		t.Fatalf("normal mode promoted style obligations to blockers: %+v", report)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "simulation_sources/") || strings.Contains(string(data), profile.Features[0].Statement) {
		t.Fatalf("report leaked source path or portable feature prose: %s", data)
	}
}

func TestCheckSimulationReinforcedBlocksOnlyMeasurableMust(t *testing.T) {
	st, _ := simulationCheckTestStore(t, true)
	if err := st.Drafts.SaveDraft(1, "完全原创的章节正文，没有命中本地安全条目。"); err != nil {
		t.Fatal(err)
	}
	service := NewSimulationCheckService(st, domain.SimulationModeReinforced)
	missing, err := service.Check(context.Background(), 1)
	if err != nil {
		t.Fatalf("Check missing must: %v", err)
	}
	if missing.Passed || missing.ContractStatus != simulationcheck.StatusFail {
		t.Fatalf("reinforced measurable must did not block: %+v", missing)
	}
	if len(missing.MustChecks) != 1 || missing.MustChecks[0].Status != "missing" {
		t.Fatalf("unexpected must checks: %+v", missing.MustChecks)
	}

	if err := st.Drafts.SaveChapterPlan(domain.ChapterPlan{
		Chapter: 1,
		Contract: domain.ChapterContract{
			RequiredBeats:    []string{"reveal one fact"},
			ContinuityChecks: []string{"record who learned it"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	passed, err := service.Check(context.Background(), 1)
	if err != nil {
		t.Fatalf("Check met must: %v", err)
	}
	if !passed.Passed || passed.ContractStatus != simulationcheck.StatusPass ||
		passed.MustChecks[0].Status != "met" {
		t.Fatalf("reinforced measurable must did not pass: %+v", passed)
	}
}

func TestCheckSimulationPortableOnlyIsTruthfulPartialAndCommitAcceptsCurrentReport(t *testing.T) {
	st, _ := simulationCheckTestStore(t, false)
	const draft = "完全原创的 portable-only 草稿。"
	if err := st.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatal(err)
	}
	service := NewSimulationCheckService(st, domain.SimulationModeNormal)
	report, err := service.Check(context.Background(), 1)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.Passed || report.Capability.State != simulationcheck.CapabilityPartial ||
		report.CopyStatus != simulationcheck.StatusUnverified {
		t.Fatalf("portable-only capability was overstated or blocked: %+v", report)
	}
	if err := service.EnsureCurrent(context.Background(), 1, draft); err != nil {
		t.Fatalf("current partial report should allow legacy-compatible commit: %v", err)
	}
}

func TestSimulationCommitGateRejectsDraftChangedAfterCheck(t *testing.T) {
	st, _ := simulationCheckTestStore(t, true)
	const original = "完全原创的当前草稿。"
	if err := st.Drafts.SaveDraft(1, original); err != nil {
		t.Fatal(err)
	}
	service := NewSimulationCheckService(st, domain.SimulationModeNormal)
	report, err := service.Check(context.Background(), 1)
	if err != nil || !report.Passed {
		t.Fatalf("initial check: report=%+v err=%v", report, err)
	}
	if err := service.EnsureCurrent(context.Background(), 1, original+"正文修改"); err == nil ||
		!strings.Contains(err.Error(), "draft_changed") {
		t.Fatalf("changed draft reused stale report: %v", err)
	}
	if err := st.Drafts.SaveDraft(1, original+"正文修改"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Check(context.Background(), 1); err != nil {
		t.Fatalf("recheck: %v", err)
	}
	if err := service.EnsureCurrent(context.Background(), 1, original+"正文修改"); err != nil {
		t.Fatalf("rechecked draft rejected: %v", err)
	}
}

func TestSimulationCommitGateDoesNotAffectNoProfileProject(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	service := NewSimulationCheckService(st, domain.SimulationModeNormal)
	if err := service.EnsureCurrent(context.Background(), 1, "existing creation path"); err != nil {
		t.Fatalf("no-profile project was blocked: %v", err)
	}
}

func TestMeasurableSimulationMustUsesStructuredChapterEvidence(t *testing.T) {
	plan := &domain.ChapterPlan{
		Hook: "saved hook",
		Contract: domain.ChapterContract{
			RequiredBeats:    []string{"reveal"},
			ContinuityChecks: []string{"knowledge checkpoint"},
		},
	}
	outline := &domain.OutlineEntry{
		CoreEvent: "choice changes the route", Scenes: []string{"choice", "cost"},
	}
	for _, feature := range []domain.SimulationFeature{
		{ID: "hook", Dimension: "hook_design.placement"},
		{ID: "plot", Dimension: "plot_design.escalation_patterns"},
		{ID: "release", Dimension: "pacing_density.information_release"},
		{ID: "density", Dimension: "pacing_density.scene_density"},
	} {
		if check := measurableSimulationMust(feature, outline, plan); check.Status != "met" {
			t.Fatalf("%s structured check = %+v", feature.Dimension, check)
		}
	}
	if check := measurableSimulationMust(
		domain.SimulationFeature{ID: "style", Dimension: "style.mood"}, outline, plan,
	); check.Status != "unverifiable" {
		t.Fatalf("subjective feature became deterministic: %+v", check)
	}
}

func simulationCheckTestStore(t *testing.T, localIndex bool) (*store.Store, domain.SimulationProfileV2) {
	t.Helper()
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	confidence, coverage := 0.96, 0.8
	profile := domain.SimulationProfileV2{
		Version:   domain.SimulationPortableProfileVersion,
		CreatedAt: time.Unix(1, 0).UTC().Format(time.RFC3339),
		UpdatedAt: time.Unix(1, 0).UTC().Format(time.RFC3339),
		Corpus: domain.SimulationPortableCorpus{
			Digest: strings.Repeat("a", 64), SourceCount: 12,
		},
		Analysis: domain.SimulationAnalysisMetadata{
			SourceAnalysisSignature: "source", SchemaSignature: "schema",
			AggregationSignature: "aggregate",
		},
		Features: []domain.SimulationFeature{{
			ID: "pace-info", Dimension: "pacing_density.information_release",
			Statement: "alternate reveal and consequence", Classification: "stable",
			SupportCount: 8, Confidence: &confidence, Coverage: &coverage, Safety: "guidance",
		}, {
			ID: "style-advisory", Dimension: "style.sentence_rhythm",
			Statement: "vary sentence rhythm", Classification: "stable",
			SupportCount: 8, Confidence: &confidence, Coverage: &coverage, Safety: "guidance",
		}},
		Capabilities: domain.SimulationCapabilities{
			Portable: true, LocalEvidence: localIndex, AnalysisSigned: true, SafetyIndex: localIndex,
		},
		Health: domain.SimulationProfileHealth{State: "fresh"},
	}
	if !localIndex {
		profile.Health.State = "portable_only"
	}
	if err := domain.SetSimulationProfileDigest(&profile); err != nil {
		t.Fatal(err)
	}
	if localIndex {
		evidence := domain.SimulationLocalEvidence{
			Version: domain.SimulationLocalEvidenceVersion, ProfileDigest: profile.ProfileDigest,
			SafetyIndex: &domain.SimulationSafetyIndex{
				Version:   domain.SimulationSafetyIndexVersion,
				UpdatedAt: time.Unix(1, 0).UTC().Format(time.RFC3339),
				Entries: []domain.SimulationSafetyIndexEntry{{
					ID: "rare-long", Kind: "rare_phrase",
					Value:        "银蓝钟摆越过七级石阶后停在逆风里",
					EvidenceRefs: []string{"report-0123456789abcdef01234567"},
				}},
			},
		}
		if err := st.Simulation.SaveAnalysis(profile, evidence); err != nil {
			t.Fatal(err)
		}
	} else if err := st.Simulation.SavePortable(profile); err != nil {
		t.Fatal(err)
	}
	return st, profile
}
