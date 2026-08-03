package store

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
)

func TestAdaptationAuditReportRoundTrip(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	report := adaptaudit.Audit(adaptaudit.Input{Mode: adaptaudit.ModeFree})
	if err := st.Adaptation.SaveAuditReport(report); err != nil {
		t.Fatalf("SaveAuditReport: %v", err)
	}
	loaded, err := st.Adaptation.LoadAuditReport()
	if err != nil {
		t.Fatalf("LoadAuditReport: %v", err)
	}
	if loaded == nil || loaded.Digest != report.Digest || !loaded.ReadOnly {
		t.Fatalf("loaded=%+v", loaded)
	}
}

func TestAdaptationAuditRunIsImmutableAndLegacyIsMigrated(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	report := adaptaudit.Audit(adaptaudit.Input{Mode: adaptaudit.ModeFree})
	if err := st.Adaptation.SaveAuditReport(report); err != nil {
		t.Fatal(err)
	}
	entries, err := st.Adaptation.ListAuditRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Trigger != adaptaudit.AuditTriggerLegacy {
		t.Fatalf("entries=%+v", entries)
	}

	run, err := adaptaudit.NewAuditRun(report, adaptaudit.AuditKindContract, adaptaudit.AuditTriggerManual, nil, time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveAuditRun(run); err != nil {
		t.Fatal(err)
	}
	changed := run
	changed.EngineVersion = "changed"
	if err := st.Adaptation.SaveAuditRun(changed); err == nil {
		t.Fatal("expected immutable run rejection")
	}
}

func TestAdaptationAuditIndexRebuildsFromImmutableRuns(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	report := adaptaudit.Audit(adaptaudit.Input{Mode: adaptaudit.ModeFree})
	run, _ := adaptaudit.NewAuditRun(report, adaptaudit.AuditKindContract, adaptaudit.AuditTriggerManual, nil, time.Unix(20, 0))
	cost := 1.25
	run.Usage = adaptaudit.AuditUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120, CallCount: 3, CostUSD: &cost, PriceKnown: true}
	if err := st.Adaptation.SaveAuditRun(run); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{"completion", "pre_export", "repair"} {
		if err := st.Adaptation.ProtectAuditRun(run.RunID, reason); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Adaptation.MarkAuditRunApplied(run.RunID, "2026-07-11T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.Adaptation.io.path(adaptationAuditIndexFile), []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := st.Adaptation.ListAuditRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].RunID != run.RunID {
		t.Fatalf("rebuilt entries=%+v", entries)
	}
	if !slices.Contains(entries[0].ProtectedReasons, "completion") || !slices.Contains(entries[0].ProtectedReasons, "pre_export") || !slices.Contains(entries[0].ProtectedReasons, "repair") || entries[0].AppliedAt == "" {
		t.Fatalf("durable protection was not rebuilt: %+v", entries[0])
	}
	if entries[0].Usage.CallCount != 3 || entries[0].Usage.CostUSD == nil || *entries[0].Usage.CostUSD != cost {
		t.Fatalf("usage was not indexed: %+v", entries[0].Usage)
	}
}
