package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestLoadLatestPlanBackupReadsCopiedPlan(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		Status:      domain.AdaptationPlanStatusConfirmed,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter: 1,
			Title:   "Budget legacy",
			OutlineEntry: domain.OutlineEntry{
				Scenes: []string{"one", "two", "three", "four", "five", "six"},
			},
			TargetRunes:    800,
			TargetMinRunes: 600,
			TargetMaxRunes: 1000,
		}},
	}
	if err := s.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	wantPath, err := s.Adaptation.Backup("auto-budget-density-repair")
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	loaded, gotPath, err := s.Adaptation.LoadLatestPlanBackup("auto-budget-density-repair")
	if err != nil {
		t.Fatalf("LoadLatestPlanBackup: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadLatestPlanBackup returned nil")
	}
	if gotPath != wantPath || loaded.Chapters[0].Title != "Budget legacy" {
		t.Fatalf("loaded backup = path %q plan=%+v, want path %q", gotPath, loaded, wantPath)
	}
	if issues := domain.ValidateArcChapterBudgetDensity(*loaded); len(issues) != 1 || issues[0].Chapter != 1 {
		t.Fatalf("backup density issues = %+v", issues)
	}
}
