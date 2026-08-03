//go:build acceptance

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/expansionauditorclient"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// ExpansionBrowserAcceptanceHandler serves the real production web handler,
// ExpansionPlanner and PR-01 revision services against a disposable project.
// Only the recommendation model is deterministic; mapping, CAS, audit stages,
// findings, persistence and publication all come from production Go code.
type ExpansionBrowserAcceptanceHandler struct {
	mu               sync.Mutex
	root             string
	app              *Server
	id               string
	mode             domain.RevisionMode
	restoreAuthority func()
}

func NewExpansionBrowserAcceptanceHandler(root string) (*ExpansionBrowserAcceptanceHandler, error) {
	handler := &ExpansionBrowserAcceptanceHandler{root: filepath.Clean(root)}
	restoreAuthority, err := storepkg.ConfigureExpansionAuthorityForAcceptanceProcess(filepath.Join(handler.root, "publication-authority"))
	if err != nil {
		return nil, err
	}
	handler.restoreAuthority = restoreAuthority
	if err := handler.reset(domain.RevisionModeNormal); err != nil {
		restoreAuthority()
		return nil, err
	}
	return handler, nil
}

func (handler *ExpansionBrowserAcceptanceHandler) Close() {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.app != nil {
		handler.app.Close()
	}
	if handler.restoreAuthority != nil {
		handler.restoreAuthority()
		handler.restoreAuthority = nil
	}
}

func (handler *ExpansionBrowserAcceptanceHandler) reset(mode domain.RevisionMode) error {
	if handler.app != nil {
		handler.app.Close()
	}
	if err := os.RemoveAll(handler.root); err != nil {
		return err
	}
	if _, err := storepkg.InitializeExpansionAuthorityRoot(); err != nil {
		return fmt.Errorf("initialize browser acceptance publication authority: %w", err)
	}
	cfg := bootstrap.Config{Provider: "openai", ModelName: "acceptance-model", PersistPath: filepath.Join(handler.root, "config.json"), Providers: map[string]bootstrap.ProviderConfig{"openai": {Type: "openai", APIKey: "acceptance-not-a-credential"}}}
	app := NewServer(cfg, assets.Load("default"), filepath.Join(handler.root, "runtime"))
	if mode != domain.RevisionModeAdaptation {
		mode = domain.RevisionModeNormal
	}
	manifest, err := app.store.CreateProject("browser expansion production contract " + string(mode))
	if err != nil {
		app.Close()
		return err
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		app.Close()
		return err
	}
	auditor, err := expansionauditorclient.New()
	if err != nil {
		app.Close()
		return err
	}
	if err := auditor.Init(context.Background(), manifest.OutputDir); err != nil {
		app.Close()
		return err
	}
	structure := acceptanceExpansionStructure()
	if mode == domain.RevisionModeAdaptation {
		plan, source, reports := acceptanceAdaptationExpansionFixture(manifest.ID)
		if err := st.Adaptation.SaveSourceManifest(source); err != nil {
			app.Close()
			return err
		}
		if err := st.Adaptation.SaveSourceReports(reports); err != nil {
			app.Close()
			return err
		}
		if err := st.Adaptation.SavePlan(plan); err != nil {
			app.Close()
			return err
		}
		structure = acceptanceAdaptationExpansionStructure(plan)
	}
	if err := st.Outline.SaveLayeredOutline(structure); err != nil {
		app.Close()
		return err
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, Layered: true, TotalChapters: 1, CurrentChapter: 1}); err != nil {
		app.Close()
		return err
	}
	cfg.OutputDir = manifest.OutputDir
	projectHost, err := host.New(cfg, assets.Load("default"))
	if err != nil {
		app.Close()
		return err
	}
	session, err := NewProjectSession(manifest, projectHost)
	if err != nil {
		projectHost.Close()
		app.Close()
		return err
	}
	session.expansionPlanner = host.NewExpansionPlanner(st, acceptanceExpansionRecommender{})
	app.sessions.mu.Lock()
	app.sessions.sessions[manifest.ID] = session
	app.sessions.mu.Unlock()
	handler.app, handler.id, handler.mode = app, manifest.ID, mode
	return nil
}

func (handler *ExpansionBrowserAcceptanceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if r.URL.Path == "/health" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if r.URL.Path == "/api/test/expansion-metadata" {
		st := storepkg.NewStore(handler.app.sessions.Project(handler.id).manifest.OutputDir)
		structure, err := st.Outline.LoadLayeredOutline()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		chapters := domain.FlattenOutline(structure)
		referenceID := ""
		if len(chapters) > 0 {
			referenceID = chapters[len(chapters)-1].ID
		}
		writeJSON(w, http.StatusOK, map[string]any{"structure_revision": domain.StructureRevision(structure), "structure_signature": domain.StructureSignature(structure), "reference_id": referenceID, "mode": handler.mode})
		return
	}
	if r.URL.Path == "/api/test/adaptation-contract" {
		st := storepkg.NewStore(handler.app.sessions.Project(handler.id).manifest.OutputDir)
		manifest, manifestErr := st.Adaptation.LoadSourceManifest()
		plan, planErr := st.Adaptation.LoadPlan()
		if manifestErr != nil || planErr != nil || manifest == nil || plan == nil {
			writeError(w, http.StatusInternalServerError, "adaptation contract unavailable")
			return
		}
		last := plan.Chapters[len(plan.Chapters)-1]
		writeJSON(w, http.StatusOK, map[string]any{"mode": handler.mode, "source_chapter_count": manifest.ChapterCount, "target_chapter_count": len(plan.Chapters), "last_target_display": last.Chapter, "last_is_added": last.IsAdded, "last_source_chapters": last.SourceChapters, "last_source_range": last.SourceRange})
		return
	}
	if r.URL.Path == "/api/test/reset-expansion" && r.Method == http.MethodPost {
		mode := domain.RevisionMode(strings.TrimSpace(r.URL.Query().Get("mode")))
		if err := handler.reset(mode); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"reset": true})
		return
	}
	if r.URL.Path == "/api/test/restart-expansion" && r.Method == http.MethodPost {
		session := handler.app.sessions.Project(handler.id)
		session.expansionPlanner = host.NewExpansionPlanner(storepkg.NewStore(session.manifest.OutputDir), acceptanceExpansionRecommender{})
		writeJSON(w, http.StatusOK, map[string]bool{"restarted": true})
		return
	}
	if r.URL.Path == "/api/test/process-expansion-audit" && r.Method == http.MethodPost {
		session := handler.app.sessions.Project(handler.id)
		auditor, err := expansionauditorclient.New()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		active, err := session.expansionPlanner.ActiveRevision()
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		artifact, err := auditor.ReviewRevision(r.Context(), session.manifest.OutputDir, active.ID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		updated, err := session.expansionPlanner.AcceptAuditArtifact(active.ID, artifact)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revision": publicExpansionRevision(updated), "audit_decision": artifact.Decision, "findings": artifact.Findings})
		return
	}
	if r.URL.Path == "/api/test/expire-expansion" && r.Method == http.MethodPost {
		session := handler.app.sessions.Project(handler.id)
		st := storepkg.NewStore(session.manifest.OutputDir)
		runtime, err := st.LoadExpansionRuntime()
		if err == nil {
			for _, preview := range runtime.Previews {
				preview.ExpiresAt = time.Now().Add(-time.Minute)
			}
			err = st.SaveExpansionRuntime(runtime)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		session.expansionPlanner = host.NewExpansionPlanner(st, acceptanceExpansionRecommender{})
		writeJSON(w, http.StatusOK, map[string]bool{"expired": true})
		return
	}
	r.URL.Path = strings.Replace(r.URL.Path, "/api/projects/browser-project/", "/api/projects/"+handler.id+"/", 1)
	handler.app.Handler().ServeHTTP(w, r)
}

type acceptanceExpansionRecommender struct{}

func (acceptanceExpansionRecommender) RecommendExpansion(_ context.Context, snapshot host.ExpansionContext, request domain.ExpansionRequest) (domain.ExpansionRecommendation, error) {
	current := domain.CloneStructureSnapshot(snapshot.Structure)
	if len(current) == 0 || len(current[0].Arcs) == 0 {
		return domain.ExpansionRecommendation{}, fmt.Errorf("acceptance structure is incomplete")
	}
	count := 1
	form := domain.ExpansionFormInsertOne
	operation := domain.StructureRevisionAppendChapter
	if request.Adjustment == domain.ExpansionAdjustmentFull {
		count, form = 2, domain.ExpansionFormInsertMany
	}
	if request.Adjustment == domain.ExpansionAdjustmentSeparateVolume {
		form, operation = domain.ExpansionFormNewVolume, domain.StructureRevisionAppendVolume
	}
	operations := make([]domain.ExpansionOperation, 0, count)
	working := current
	for index := 0; index < count; index++ {
		next := domain.CloneStructureSnapshot(working)
		facts := domain.ExpansionDramaticFactSet{SchemaVersion: domain.ExpansionDramaticFactsSchemaV1, GoalState: "pursued", ConflictState: "active", ChoiceState: "committed", CostState: "paid", ResultState: "achieved", CharacterBefore: "reactive", CharacterAfter: "proactive", ClimaxState: "occurred", ExitState: "irreversible", ImpactState: "required"}
		chapter := domain.OutlineEntry{ID: domain.LegacyStructureID("browser-acceptance", domain.StructureKindChapter, fmt.Sprintf("added-%d-%s", index, request.Location)), Chapter: len(domain.FlattenOutline(next)) + 1, Title: "公开代价", CoreEvent: "主角公开站队并承担代价", Hook: "联盟是否仍然可信", Scenes: []string{"独立审查"}, DramaticFacts: &facts}
		// The deterministic model emits one structurally valid but semantically
		// contradictory first result. Feedback changes the real model input and
		// produces a distinct repaired candidate; no pending task is mutated.
		if !strings.Contains(request.Sentence, "按审核意见修复") {
			chapter.CoreEvent = "the protagonist dies after the irreversible sacrifice"
			chapter.Hook = "the protagonist continues acting in the next scene"
		}
		if operation == domain.StructureRevisionAppendVolume {
			next = append(next, domain.VolumeOutline{ID: domain.LegacyStructureID("browser-acceptance", domain.StructureKindVolume, string(request.Location)), Index: len(next) + 1, Title: "新卷", Theme: "公开选择", Arcs: []domain.ArcOutline{{ID: domain.LegacyStructureID("browser-acceptance", domain.StructureKindArc, string(request.Location)), Index: 1, Title: "代价", Goal: "完成不可逆选择", Chapters: []domain.OutlineEntry{chapter}}}})
		} else {
			next[0].Arcs[0].Chapters = append(next[0].Arcs[0].Chapters, chapter)
		}
		budget, _ := domain.NewDynamicSoftBudget(len(domain.FlattenOutline(next)), 2200, 3600)
		operations = append(operations, domain.ExpansionOperation{Operation: operation, Intent: request.Sentence, Proposal: domain.StructureRevisionProposal{Assessment: domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "independent dramatic unit"}, Candidate: next, SoftBudget: budget}})
		working = next
	}
	delta, _ := domain.NewDynamicSoftBudget(count, 2200, 3600)
	recommendation := domain.ExpansionRecommendation{
		Form: form, Reason: "独立的选择与代价需要结构空间", Location: request.Location,
		ChapterCount: count, ChapterMinWords: 2200, ChapterMaxWords: 3600, TotalMinWords: delta.TotalMinWords, TotalMaxWords: delta.TotalMaxWords,
		NewVolume: form == domain.ExpansionFormNewVolume, OldSummary: "evidence remains hidden", NewSummary: "the protagonist chooses disclosure",
		Assessment: domain.ExpansionDramaticAssessment{Goal: "obtain evidence", Conflict: "trust fractures", Choice: "disclose", Cost: "lose an ally", Result: "a new alliance forms", CharacterStageChange: "reactive to proactive", CharacterBeforeStage: "reactive", CharacterAfterStage: "proactive", IndependentClimax: "the evidence is disclosed", IrreversibleExit: "the former ally leaves", CurrentFit: "independent unit", VolumePacingEffect: "midpoint reversal", TypedClaims: &domain.ExpansionDramaticFactSet{SchemaVersion: domain.ExpansionDramaticFactsSchemaV1, GoalState: "pursued", ConflictState: "active", ChoiceState: "committed", CostState: "paid", ResultState: "achieved", CharacterBefore: "reactive", CharacterAfter: "proactive", ClimaxState: "occurred", ExitState: "irreversible", ImpactState: "required"}},
		AuditChain: []string{"structure independent review", "outline independent review"}, ModeConstraints: []string{"normal mode source firewall"}, OrderedOperations: operations, SoftBudgetDelta: delta,
	}
	if snapshot.Mode == domain.RevisionModeAdaptation {
		if snapshot.Adaptation == nil || snapshot.Adaptation.Plan == nil {
			return domain.ExpansionRecommendation{}, fmt.Errorf("acceptance adaptation plan is unavailable")
		}
		payload, _ := json.Marshal(snapshot.Adaptation.Plan)
		var candidate domain.AdaptationPlan
		_ = json.Unmarshal(payload, &candidate)
		baseCount := len(candidate.Chapters)
		for index, outline := range domain.FlattenOutline(working) {
			if index < baseCount {
				continue
			}
			eventID := fmt.Sprintf("acceptance-added-event-%d", index-baseCount+1)
			candidate.TargetEventLedger = append(candidate.TargetEventLedger, domain.AdaptationEvent{ID: eventID, Description: outline.CoreEvent, Origin: domain.AdaptationEventOriginAdded, Required: true, DependsOn: []string{"source-event-2"}})
			candidate.Chapters = append(candidate.Chapters, domain.AdaptationChapterPlan{OutlineEntry: outline, Chapter: outline.Chapter, Title: outline.Title, IsAdded: true, AddedEventIDs: []string{eventID}, CoverageNote: "new expansion does not replace source coverage", TargetRunes: 2900, TargetMinRunes: 2200, TargetMaxRunes: 3600, RequiredChanges: []string{"connect the added dramatic unit"}, ForbiddenMoves: []string{"do not replace protected source events"}})
		}
		if len(candidate.Volumes) > 0 {
			candidate.Volumes[len(candidate.Volumes)-1].TargetTo = len(candidate.Chapters)
		}
		candidate.TargetTotalRunes += count * 2900
		candidate.TargetMinRunes += count * 2200
		candidate.TargetMaxRunes += count * 3600
		recommendation.AdaptationCandidate = &candidate
		recommendation.Assessment.AdaptationEffect = "source coverage and ownership remain complete; added target chapters have no source display number"
		recommendation.ModeConstraints = []string{"adaptation source coverage and protected events are mandatory"}
	}
	return recommendation, nil
}

func acceptanceExpansionStructure() []domain.VolumeOutline {
	return []domain.VolumeOutline{{ID: domain.LegacyStructureID("browser-acceptance", domain.StructureKindVolume, "volume"), Index: 1, Title: "第一卷", Theme: "trust", Arcs: []domain.ArcOutline{{ID: domain.LegacyStructureID("browser-acceptance", domain.StructureKindArc, "arc"), Index: 1, Title: "evidence", Goal: "choose", Chapters: []domain.OutlineEntry{{ID: "ch_0123456789abcdef0123456789abcdef", Chapter: 1, Title: "hidden evidence", CoreEvent: "find evidence", Hook: "disclose?", Scenes: []string{"secret"}}}}}}}
}

func acceptanceAdaptationExpansionFixture(projectID string) (domain.AdaptationPlan, domain.AdaptationSourceManifest, []domain.AdaptationSourceReport) {
	volumeID := domain.LegacyStructureID(projectID, domain.StructureKindVolume, "adaptation-volume-1")
	chapters := []domain.AdaptationChapterPlan{
		{OutlineEntry: domain.OutlineEntry{ID: domain.LegacyStructureID(projectID, domain.StructureKindChapter, "adaptation-chapter-1"), Chapter: 1, Title: "Target One", CoreEvent: "meeting", Hook: "clue", Scenes: []string{"meet"}}, Chapter: 1, Title: "Target One", SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1}, SourceRunes: 1000, EventIDs: []string{"source-event-1"}, TargetRunes: 4000, TargetMinRunes: 3000, TargetMaxRunes: 5000, PreserveEvents: []string{"meeting"}, RequiredChanges: []string{"adapt"}, ForbiddenMoves: []string{"keep meeting"}},
		{OutlineEntry: domain.OutlineEntry{ID: domain.LegacyStructureID(projectID, domain.StructureKindChapter, "adaptation-chapter-2"), Chapter: 2, Title: "Target Two", CoreEvent: "answer", Hook: "ending", Scenes: []string{"answer"}}, Chapter: 2, Title: "Target Two", SourceChapters: []int{2}, SourceRange: domain.SourceRange{From: 2, To: 2}, SourceRunes: 1000, EventIDs: []string{"source-event-2"}, DependsOnEventIDs: []string{"source-event-1"}, TargetRunes: 4000, TargetMinRunes: 3000, TargetMaxRunes: 5000, PreserveEvents: []string{"answer"}, RequiredChanges: []string{"adapt"}, ForbiddenMoves: []string{"keep answer"}},
	}
	plan := domain.AdaptationPlan{Granularity: domain.AdaptationGranularityArc, ModePolicy: domain.AdaptationModePolicyForGranularity(domain.AdaptationGranularityArc), Status: domain.AdaptationPlanStatusConfirmed, RewritePolicy: domain.AdaptationRewriteFullRewrite, Brief: "preserve source contracts", SourceTotalRunes: 2000, TargetTotalRunes: 8000, TargetMinRunes: 6000, TargetMaxRunes: 10000, SourceEvents: []domain.AdaptationEvent{{ID: "source-event-1", Description: "meeting", Origin: domain.AdaptationEventOriginSource, Importance: domain.AdaptationEventMainline, SourceChapter: 1, Required: true}, {ID: "source-event-2", Description: "answer", Origin: domain.AdaptationEventOriginSource, SourceChapter: 2, Required: true, DependsOn: []string{"source-event-1"}}}, Volumes: []domain.AdaptationVolumePlan{{ID: volumeID, Index: 1, Title: "Target Volume", Theme: "trust", TargetFrom: 1, TargetTo: 2, SourceFrom: 1, SourceTo: 2, MainlineEventIDs: []string{"source-event-1"}}}, Chapters: chapters}
	manifest := domain.AdaptationSourceManifest{SourcePath: "acceptance-source.txt", ChapterCount: 2, Chapters: []domain.AdaptationSource{{Chapter: 1, Title: "Source One", SHA256: domain.ContentSignature([]byte("source-one")), Path: "meta/adaptation/source_chapters/0001.md", Runes: 1000}, {Chapter: 2, Title: "Source Two", SHA256: domain.ContentSignature([]byte("source-two")), Path: "meta/adaptation/source_chapters/0002.md", Runes: 1000}}}
	reports := []domain.AdaptationSourceReport{{Chapter: 1, Title: "Source One", SourceSHA256: manifest.Chapters[0].SHA256, Summary: "meeting establishes trust", KeyEvents: []string{"meeting"}}, {Chapter: 2, Title: "Source Two", SourceSHA256: manifest.Chapters[1].SHA256, Summary: "answer resolves clue", KeyEvents: []string{"answer"}}}
	return plan, manifest, reports
}

func acceptanceAdaptationExpansionStructure(plan domain.AdaptationPlan) []domain.VolumeOutline {
	chapters := make([]domain.OutlineEntry, 0, len(plan.Chapters))
	for _, chapter := range plan.Chapters {
		chapters = append(chapters, chapter.OutlineEntry)
	}
	volume := plan.Volumes[0]
	return []domain.VolumeOutline{{ID: volume.ID, Index: volume.Index, Title: volume.Title, Theme: volume.Theme, Arcs: []domain.ArcOutline{{ID: domain.LegacyStructureID(volume.ID, domain.StructureKindArc, "acceptance-adaptation-arc"), Index: 1, Title: "Source Contract Arc", Goal: "preserve coverage", Chapters: chapters}}}}
}
