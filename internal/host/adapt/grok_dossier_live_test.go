package adapt

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/ainovel-cli/internal/store"
)

// TestLiveGrok45DossierBatch is an opt-in provider smoke test. It reads an
// existing project's source reports but does not mutate its dossier cache.
func TestLiveGrok45DossierBatch(t *testing.T) {
	if os.Getenv("AINOVEL_REAL_GROK_DOSSIER") != "1" {
		t.Skip("set AINOVEL_REAL_GROK_DOSSIER=1 to run the live Grok 4.5 dossier test")
	}
	projectRoot := os.Getenv("AINOVEL_REAL_GROK_PROJECT_ROOT")
	if projectRoot == "" {
		t.Fatal("AINOVEL_REAL_GROK_PROJECT_ROOT is required")
	}

	cfg, err := bootstrap.LoadConfig("")
	if err != nil {
		t.Fatalf("load global config: %v", err)
	}
	providerConfig, ok := cfg.Providers["grok-oauth"]
	if !ok {
		t.Fatal("global config has no grok-oauth provider")
	}
	model, err := bootstrap.NewProviderModelWithConfig(cfg, "grok-oauth", "grok-4.5", providerConfig)
	if err != nil {
		t.Fatalf("create Grok 4.5 model: %v", err)
	}

	st := store.NewStore(filepath.Join(projectRoot, "output"))
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil || manifest == nil {
		t.Fatalf("load source manifest: manifest=%v err=%v", manifest != nil, err)
	}
	reports, err := st.Adaptation.LoadSourceReports()
	if err != nil {
		t.Fatalf("load source reports: %v", err)
	}
	specs := dossierBatchSpecs(*manifest, CoCreateDossierBatchSize)
	if len(specs) == 0 {
		t.Fatal("source manifest produced no dossier batches")
	}
	batchIndex := 1
	if raw := os.Getenv("AINOVEL_REAL_GROK_BATCH"); raw != "" {
		batchIndex, err = strconv.Atoi(raw)
		if err != nil || batchIndex < 1 || batchIndex > len(specs) {
			t.Fatalf("invalid AINOVEL_REAL_GROK_BATCH %q for %d batches", raw, len(specs))
		}
	}

	reportByChapter := make(map[int]struct{}, len(reports))
	selectedReports := make([]int, 0, CoCreateDossierBatchSize)
	for _, report := range reports {
		reportByChapter[report.Chapter] = struct{}{}
	}
	spec := specs[batchIndex-1]
	for chapter := spec.SourceFrom; chapter <= spec.SourceTo; chapter++ {
		if _, ok := reportByChapter[chapter]; ok {
			selectedReports = append(selectedReports, chapter)
		}
	}
	if len(selectedReports) == 0 {
		t.Fatalf("batch %d has no source reports", batchIndex)
	}
	batchReports := reportsForChapterNumbers(reports, selectedReports)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	deps := Deps{
		Store:                      st,
		LLM:                        globalprompt.WrapModel(model),
		ModelCallMaxAttempts:       1,
		StructureRepairMaxAttempts: 7,
	}
	repairEvents := 0
	emit := func(stage Stage, _, _ int, message string, err error) {
		if stage == StageDossier && err != nil {
			repairEvents++
			t.Log(message)
		}
	}
	batch, err := buildCoCreateDossierBatch(ctx, deps, spec, batchReports, len(specs), emit)
	if err != nil {
		t.Fatalf("live Grok 4.5 dossier generation with 7 structure repairs: %v", err)
	}
	if !coCreateDossierBatchHasContent(batch) {
		t.Fatal("live Grok 4.5 dossier response has no usable content")
	}
	t.Logf("completed with %d repair/regeneration events", repairEvents)
}

func reportsForChapterNumbers(reports []domain.AdaptationSourceReport, chapters []int) []domain.AdaptationSourceReport {
	wanted := make(map[int]struct{}, len(chapters))
	for _, chapter := range chapters {
		wanted[chapter] = struct{}{}
	}
	selected := make([]domain.AdaptationSourceReport, 0, len(chapters))
	for _, report := range reports {
		if _, ok := wanted[report.Chapter]; ok {
			selected = append(selected, report)
		}
	}
	return selected
}
