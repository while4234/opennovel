package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
	"weak"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type fakeNormalStructurePlanner struct {
	proposal domain.StructureRevisionProposal
	received domain.StructureRevisionRequest
}

func (p *fakeNormalStructurePlanner) PlanStructure(_ context.Context, request domain.StructureRevisionRequest) (domain.StructureRevisionProposal, error) {
	p.received = request
	return cloneStructureRevisionProposal(p.proposal), nil
}

type fakeAdaptationStructurePlanner struct {
	proposal domain.StructureRevisionProposal
}

func (p fakeAdaptationStructurePlanner) PlanStructure(context.Context, domain.StructureRevisionRequest) (domain.StructureRevisionProposal, error) {
	return cloneStructureRevisionProposal(p.proposal), nil
}

func TestStructurePlanningKernelInsertStaysInVolumeAndDoesNotMutateFormalStructure(t *testing.T) {
	formal := testStructure()
	original := domain.CloneStructureSnapshot(formal)
	candidate := domain.CloneStructureSnapshot(formal)
	candidate[0].Arcs[0].Chapters = append(
		candidate[0].Arcs[0].Chapters[:1],
		append([]domain.OutlineEntry{testChapter("ch-new", "Inserted")}, candidate[0].Arcs[0].Chapters[1:]...)...,
	)
	planner := &fakeNormalStructurePlanner{proposal: testStructureProposal(t, candidate, domain.ContentAdditionAssessment{
		NeedsAdditionalChapters: true,
		Reason:                  "the requested reversal needs its own scene sequence but still belongs to the current conflict",
	})}

	preview, err := new(StructurePlanningKernel).Plan(context.Background(), planner, testStructureRequest(formal, domain.StructureRevisionInsertChapter))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(preview.Proposal.Candidate) != 1 || len(preview.Proposal.Candidate[0].Arcs[0].Chapters) != 4 {
		t.Fatalf("candidate should insert within the existing volume: %+v", preview.Proposal.Candidate)
	}
	if !reflect.DeepEqual(formal, original) {
		t.Fatalf("formal structure changed before confirmation\n got: %+v\nwant: %+v", formal, original)
	}
	if planner.received.Current[0].Arcs[0].Chapters[0].ID != formal[0].Arcs[0].Chapters[0].ID {
		t.Fatal("planner did not receive stable chapter identities")
	}
}

func TestStructurePlanningKernelInsertionMayCreateVolumeOnlyWithDramaticStageEvidence(t *testing.T) {
	formal := testStructure()
	candidate := domain.CloneStructureSnapshot(formal)
	candidate = append(candidate, domain.VolumeOutline{
		ID: "vol-new", Title: "Aftermath", Theme: "the cost becomes a new conflict",
		Arcs: []domain.ArcOutline{{
			ID: "arc-new", Title: "No Return", Goal: "survive the irreversible political break",
			Chapters: []domain.OutlineEntry{testChapter("ch-new", "The Break")},
		}},
	})
	evidence := &domain.DramaticStageEvidence{
		EntryState:             "the old alliance survives the prior ending but loses political legitimacy",
		IndependentConflict:    "the alliance becomes the new antagonist",
		ArcProgression:         "the survivors split, compete for the provinces, and choose irreversible sides",
		Climax:                 "the capital falls",
		IrreversibleOutcome:    "the old regime is permanently dissolved",
		CannotFitCurrentVolume: "the current volume's conflict already resolves before this stage begins",
	}
	planner := fakeNormalStructurePlanner{proposal: testStructureProposal(t, candidate, domain.ContentAdditionAssessment{
		NeedsAdditionalChapters: true, Reason: "a separate dramatic stage is necessary", NewVolume: evidence,
	})}
	if _, err := new(StructurePlanningKernel).Plan(context.Background(), &planner, testStructureRequest(formal, domain.StructureRevisionInsertChapter)); err != nil {
		t.Fatalf("Plan with independent volume evidence: %v", err)
	}

	invalid := planner.proposal
	invalid.Assessment.NewVolume = nil
	if _, err := new(StructurePlanningKernel).Plan(context.Background(), &fakeNormalStructurePlanner{proposal: invalid}, testStructureRequest(formal, domain.StructureRevisionInsertChapter)); err == nil {
		t.Fatal("expected a new volume without dramatic-stage evidence to be rejected")
	}
}

func TestStructurePlanningKernelAllowsRepeatedRevisionAtEveryManuscriptStage(t *testing.T) {
	for _, stage := range []domain.ManuscriptStage{
		domain.ManuscriptStageProposalComplete,
		domain.ManuscriptStageOutlineComplete,
		domain.ManuscriptStageWriting,
		domain.ManuscriptStageComplete,
	} {
		t.Run(string(stage), func(t *testing.T) {
			formal := testStructure()
			candidate := domain.CloneStructureSnapshot(formal)
			candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, testChapter("ch-appended", "Afterword Conflict"))
			planner := fakeAdaptationStructurePlanner{proposal: testStructureProposal(t, candidate, domain.ContentAdditionAssessment{
				NeedsAdditionalChapters: true, Reason: "the requested follow-up is not covered by the current ending",
			})}
			request := testStructureRequest(formal, domain.StructureRevisionAppendChapter)
			request.Stage = stage
			preview, err := new(StructurePlanningKernel).Plan(context.Background(), planner, request)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if got := len(domain.StructureChapterIDs(preview.Proposal.Candidate)); got != 4 {
				t.Fatalf("candidate chapters = %d, want 4", got)
			}
		})
	}
}

func TestStructurePlanningKernelSupportsExpandSplitAppendArcAppendVolumeAndMove(t *testing.T) {
	formal := testStructure()
	volumeEvidence := &domain.DramaticStageEvidence{
		EntryState: "the prior trial leaves succession unresolved", IndependentConflict: "a successor war begins",
		ArcProgression: "rivals gather claims, fracture alliances, and fight for the capital", Climax: "the heir defeats the council",
		IrreversibleOutcome: "the realm permanently divides", CannotFitCurrentVolume: "the opening trial is already resolved",
	}
	tests := []struct {
		name      string
		operation domain.StructureRevisionOperation
		candidate []domain.VolumeOutline
		assess    domain.ContentAdditionAssessment
		configure func(*domain.StructureRevisionRequest)
	}{
		{
			name: "expand chapter", operation: domain.StructureRevisionExpandChapter,
			candidate: func() []domain.VolumeOutline {
				candidate := domain.CloneStructureSnapshot(formal)
				candidate[0].Arcs[0].Chapters[1].Scenes = append(candidate[0].Arcs[0].Chapters[1].Scenes, "an added consequence scene")
				return candidate
			}(),
			assess: domain.ContentAdditionAssessment{Reason: "the existing chapter can contain the expansion"},
		},
		{
			name: "split chapter", operation: domain.StructureRevisionSplitChapter,
			candidate: func() []domain.VolumeOutline {
				candidate := domain.CloneStructureSnapshot(formal)
				candidate[0].Arcs[0].Chapters[1].CoreEvent = "the trial begins before its consequence separates"
				candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters[:2], append([]domain.OutlineEntry{testChapter("ch-split", "Trial Consequence")}, candidate[0].Arcs[0].Chapters[2:]...)...)
				return candidate
			}(),
			assess: domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "the trial and consequence are separate dramatic units"},
		},
		{
			name: "append arc", operation: domain.StructureRevisionAppendArc,
			candidate: func() []domain.VolumeOutline {
				candidate := domain.CloneStructureSnapshot(formal)
				candidate[0].Arcs = append(candidate[0].Arcs, domain.ArcOutline{ID: "arc-002", Title: "Second Trial", Goal: "pay the alliance cost", Chapters: []domain.OutlineEntry{testChapter("ch-arc", "The Cost")}})
				return candidate
			}(),
			assess: domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "a new conflict arc is required inside the current volume"},
		},
		{
			name: "append volume", operation: domain.StructureRevisionAppendVolume,
			candidate: func() []domain.VolumeOutline {
				candidate := domain.CloneStructureSnapshot(formal)
				candidate = append(candidate, domain.VolumeOutline{ID: "vol-002", Title: "Successor War", Theme: "inheritance", Arcs: []domain.ArcOutline{{ID: "arc-002", Title: "Division", Goal: "survive succession", Chapters: []domain.OutlineEntry{testChapter("ch-volume", "The Heir")}}}})
				return candidate
			}(),
			assess: domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "a new dramatic stage is required", NewVolume: volumeEvidence},
		},
		{
			name: "move unwritten chapter", operation: domain.StructureRevisionMoveChapter,
			candidate: func() []domain.VolumeOutline {
				candidate := domain.CloneStructureSnapshot(formal)
				chapters := candidate[0].Arcs[0].Chapters
				candidate[0].Arcs[0].Chapters = []domain.OutlineEntry{chapters[2], chapters[0], chapters[1]}
				return candidate
			}(),
			assess: domain.ContentAdditionAssessment{Reason: "no extra chapter is needed; only the unwritten order changes"},
			configure: func(request *domain.StructureRevisionRequest) {
				request.TargetID = "ch-003"
				request.DestinationID = "ch-001"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := testStructureRequest(formal, tt.operation)
			if tt.configure != nil {
				tt.configure(&request)
			}
			planner := &fakeNormalStructurePlanner{proposal: testStructureProposal(t, tt.candidate, tt.assess)}
			if _, err := new(StructurePlanningKernel).Plan(context.Background(), planner, request); err != nil {
				t.Fatalf("Plan: %v", err)
			}
		})
	}
}

func TestStructurePlanningKernelRejectsStalePreview(t *testing.T) {
	formal := testStructure()
	candidate := domain.CloneStructureSnapshot(formal)
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, testChapter("ch-appended", "Aftermath"))
	planner := fakeNormalStructurePlanner{proposal: testStructureProposal(t, candidate, domain.ContentAdditionAssessment{
		NeedsAdditionalChapters: true, Reason: "the aftermath needs a final chapter",
	})}
	kernel := new(StructurePlanningKernel)
	preview, err := kernel.Plan(context.Background(), &planner, testStructureRequest(formal, domain.StructureRevisionAppendChapter))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := kernel.Confirm(*preview, preview.BaseRevision+1, formal); !errors.Is(err, domain.ErrStructurePreviewStale) {
		t.Fatalf("Confirm stale preview error = %v", err)
	}
	changed := domain.CloneStructureSnapshot(formal)
	changed[0].Theme = "changed concurrently"
	if _, err := kernel.Confirm(*preview, preview.BaseRevision, changed); !errors.Is(err, domain.ErrStructurePreviewStale) {
		t.Fatalf("Confirm changed structure error = %v", err)
	}
	confirmed, err := kernel.Confirm(*preview, preview.BaseRevision, formal)
	if err != nil {
		t.Fatalf("Confirm fresh preview: %v", err)
	}
	confirmed[0].Theme = "caller mutation"
	if preview.Proposal.Candidate[0].Theme == "caller mutation" {
		t.Fatal("confirmed structure aliases the preview candidate")
	}
}

func TestStructurePlanningKernelDependencyLevelsAndRenumberDoNotExpandRewriteScope(t *testing.T) {
	formal := testStructure()
	candidate := domain.CloneStructureSnapshot(formal)
	candidate[0].Arcs[0].Chapters[0].CoreEvent = "the first chapter now reveals the hidden pact"
	candidate[0].Arcs[0].Chapters = append(
		[]domain.OutlineEntry{candidate[0].Arcs[0].Chapters[0], testChapter("ch-new", "Interlude")},
		candidate[0].Arcs[0].Chapters[1:]...,
	)
	proposal := testStructureProposal(t, candidate, domain.ContentAdditionAssessment{
		NeedsAdditionalChapters: true, Reason: "the pact reveal needs a dedicated bridge",
	})
	proposal.Impacts = []domain.StructureImpactItem{
		{
			ArtifactID: "ch-001", ArtifactKind: domain.StructureKindChapter,
			Change: "rewrite for the changed reveal", Level: domain.StructureImpactRequired,
			Cause: domain.StructureImpactContentDependency, RequiresBodyRewrite: true,
			DependencyEvidence:  []string{"ch-new changes the timing, so the completed prose contradicts the revised reveal"},
			DependencySourceIDs: []string{"ch-new"},
		},
		{
			ArtifactID: "ch-003", ArtifactKind: domain.StructureKindChapter,
			Change: "review the later callback", Level: domain.StructureImpactRecommended,
			Cause:              domain.StructureImpactContentDependency,
			DependencyEvidence: []string{"chapter 3 mentions the old timing but may remain valid"},
		},
	}
	request := testStructureRequest(formal, domain.StructureRevisionInsertChapter)
	request.CompletedChapterIDs = []string{"ch-001", "ch-002", "ch-003"}
	preview, err := new(StructurePlanningKernel).Plan(context.Background(), &fakeNormalStructurePlanner{proposal: proposal}, request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := domain.BodyRewriteChapterIDs(preview.Proposal.Impacts); !reflect.DeepEqual(got, []string{"ch-001"}) {
		t.Fatalf("body rewrite scope = %v, want only changed chapter", got)
	}
	revisionImpact, err := preview.Proposal.RevisionImpact("candidate structure impact")
	if err != nil {
		t.Fatalf("RevisionImpact: %v", err)
	}
	var rewriteImpact *domain.RevisionImpactItem
	for index := range revisionImpact.Items {
		if revisionImpact.Items[index].ArtifactID == "ch-001" {
			rewriteImpact = &revisionImpact.Items[index]
			break
		}
	}
	if rewriteImpact == nil || rewriteImpact.Requirement == "" || len(rewriteImpact.DependencyEvidence) == 0 || len(rewriteImpact.DependencySourceIDs) == 0 {
		t.Fatalf("revision contract lost dependency level or evidence: %+v", rewriteImpact)
	}
	for _, impact := range preview.Proposal.Impacts {
		if impact.Cause == domain.StructureImpactDisplayRenumber && impact.RequiresBodyRewrite {
			t.Fatalf("display renumber expanded body rewrite scope: %+v", impact)
		}
	}
}

func TestStructurePlanningKernelSealsNormalizedPreview(t *testing.T) {
	formal := testStructure()
	scopeA := new(StructurePlanningKernel)
	scopeB := new(StructurePlanningKernel)
	newPreview := func(t *testing.T) domain.StructureRevisionPreview {
		t.Helper()
		candidate := domain.CloneStructureSnapshot(formal)
		candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, testChapter("ch-appended", "Aftermath"))
		proposal := testStructureProposal(t, candidate, domain.ContentAdditionAssessment{
			NeedsAdditionalChapters: true, Reason: "the aftermath needs a final chapter",
		})
		preview, err := scopeA.Plan(context.Background(), &fakeNormalStructurePlanner{proposal: proposal}, testStructureRequest(formal, domain.StructureRevisionAppendChapter))
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		return *preview
	}
	first, second := newPreview(t), newPreview(t)
	if first.Signature != second.Signature {
		t.Fatalf("normalized preview signature is not stable: %s != %s", first.Signature, second.Signature)
	}
	payload, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("Marshal preview: %v", err)
	}
	var roundTripped domain.StructureRevisionPreview
	if err := json.Unmarshal(payload, &roundTripped); err != nil {
		t.Fatalf("Unmarshal preview: %v", err)
	}
	if _, err := scopeA.Confirm(roundTripped, first.BaseRevision, formal); err != nil {
		t.Fatalf("Confirm JSON-round-tripped preview: %v", err)
	}
	if _, err := scopeB.Confirm(roundTripped, first.BaseRevision, formal); !errors.Is(err, domain.ErrStructurePreviewTampered) {
		t.Fatalf("Confirm preview through another planner scope error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*domain.StructureRevisionPreview)
	}{
		{"candidate", func(preview *domain.StructureRevisionPreview) { preview.Proposal.Candidate[0].Title = "mutated" }},
		{"budget", func(preview *domain.StructureRevisionPreview) { preview.Proposal.SoftBudget.TargetTotalWords++ }},
		{"impacts", func(preview *domain.StructureRevisionPreview) { preview.Proposal.Impacts[0].Change = "mutated" }},
		{"operation context", func(preview *domain.StructureRevisionPreview) {
			preview.Operation = domain.StructureRevisionAppendVolume
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preview := newPreview(t)
			tt.mutate(&preview)
			if _, err := scopeA.Confirm(preview, preview.BaseRevision, formal); !errors.Is(err, domain.ErrStructurePreviewTampered) {
				t.Fatalf("Confirm mutated preview error = %v", err)
			}
		})
	}

	forged := newPreview(t)
	forged.Proposal.Candidate[0].Title = "caller-forged candidate"
	forged.Signature = ""
	payload, err = json.Marshal(forged)
	if err != nil {
		t.Fatalf("Marshal forged preview: %v", err)
	}
	forged.Signature = domain.ContentSignature(payload)
	if _, err := scopeA.Confirm(forged, forged.BaseRevision, formal); !errors.Is(err, domain.ErrStructurePreviewTampered) {
		t.Fatalf("Confirm caller-rehashed forged preview error = %v", err)
	}
}

func TestStructurePlanningKernelCopiesHaveIndependentScopes(t *testing.T) {
	formal := testStructure()
	candidate := domain.CloneStructureSnapshot(formal)
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, testChapter("ch-appended", "Aftermath"))
	proposal := testStructureProposal(t, candidate, domain.ContentAdditionAssessment{
		NeedsAdditionalChapters: true, Reason: "the aftermath needs a final chapter",
	})

	var scopeA StructurePlanningKernel
	preUseCopy := scopeA
	previewA, err := scopeA.Plan(context.Background(), &fakeNormalStructurePlanner{proposal: proposal}, testStructureRequest(formal, domain.StructureRevisionAppendChapter))
	if err != nil {
		t.Fatalf("Plan through scope A: %v", err)
	}
	postUseCopy := scopeA
	for name, copiedScope := range map[string]*StructurePlanningKernel{
		"pre-use copy":  &preUseCopy,
		"post-use copy": &postUseCopy,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := copiedScope.Confirm(*previewA, previewA.BaseRevision, formal); !errors.Is(err, domain.ErrStructurePreviewTampered) {
				t.Fatalf("copied scope confirmed scope A preview: %v", err)
			}
			ownPreview, err := copiedScope.Plan(context.Background(), &fakeNormalStructurePlanner{proposal: proposal}, testStructureRequest(formal, domain.StructureRevisionAppendChapter))
			if err != nil {
				t.Fatalf("Plan through copied scope: %v", err)
			}
			if _, err := copiedScope.Confirm(*ownPreview, ownPreview.BaseRevision, formal); err != nil {
				t.Fatalf("Confirm copied scope's own preview: %v", err)
			}
			if _, err := scopeA.Confirm(*ownPreview, ownPreview.BaseRevision, formal); !errors.Is(err, domain.ErrStructurePreviewTampered) {
				t.Fatalf("scope A confirmed copied scope preview: %v", err)
			}
		})
	}
}

func TestStructurePlanningKernelZeroValueConcurrentUse(t *testing.T) {
	formal := testStructure()
	candidate := domain.CloneStructureSnapshot(formal)
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, testChapter("ch-appended", "Aftermath"))
	proposal := testStructureProposal(t, candidate, domain.ContentAdditionAssessment{
		NeedsAdditionalChapters: true, Reason: "the aftermath needs a final chapter",
	})

	var kernel StructurePlanningKernel
	const workers = 32
	start := make(chan struct{})
	errorsByWorker := make(chan error, workers)
	signatures := make(chan string, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			<-start
			preview, err := kernel.Plan(context.Background(), &fakeNormalStructurePlanner{proposal: proposal}, testStructureRequest(formal, domain.StructureRevisionAppendChapter))
			if err != nil {
				errorsByWorker <- fmt.Errorf("Plan: %w", err)
				return
			}
			if _, err := kernel.Confirm(*preview, preview.BaseRevision, formal); err != nil {
				errorsByWorker <- fmt.Errorf("Confirm: %w", err)
				return
			}
			signatures <- preview.Signature
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByWorker)
	close(signatures)
	for err := range errorsByWorker {
		t.Error(err)
	}
	var firstSignature string
	for signature := range signatures {
		if firstSignature == "" {
			firstSignature = signature
			continue
		}
		if signature != firstSignature {
			t.Fatalf("concurrent zero-value plans used different scopes: %s != %s", signature, firstSignature)
		}
	}
}

func TestStructurePlanningKernelDeadScopesConvergeWithoutLaterKernelCall(t *testing.T) {
	waitForStructurePreviewScopeCount(t, 0)

	const kernelCount = 512
	kernels := make([]*StructurePlanningKernel, kernelCount)
	for index := range kernels {
		kernels[index] = new(StructurePlanningKernel)
		kernels[index].structurePreviewScope()
	}
	if got := structurePreviewScopeCount(); got != kernelCount {
		t.Fatalf("registered scopes = %d, want %d", got, kernelCount)
	}
	runtime.KeepAlive(kernels)

	kernels = nil
	waitForStructurePreviewScopeCount(t, 0)
}

func TestStructurePlanningKernelCleanupPreservesLiveAndReplacementScopes(t *testing.T) {
	kernel := new(StructurePlanningKernel)
	identity := weak.Make(kernel)
	liveScope := kernel.structurePreviewScope()
	defer func() {
		structurePreviewScopes.mu.Lock()
		structurePreviewScopes.scopes[identity] = liveScope
		structurePreviewScopes.mu.Unlock()
		runtime.KeepAlive(kernel)
	}()

	cleanupStructurePreviewScope(structurePreviewScopeCleanup{
		identity: identity,
		scope:    &structurePreviewScope{signingKey: newStructurePreviewSigningKey()},
	})
	if got := registeredStructurePreviewScope(identity); got != liveScope {
		t.Fatal("cleanup with a mismatched scope deleted the live scope")
	}

	replacementScope := &structurePreviewScope{signingKey: newStructurePreviewSigningKey()}
	structurePreviewScopes.mu.Lock()
	structurePreviewScopes.scopes[identity] = replacementScope
	structurePreviewScopes.mu.Unlock()
	cleanupStructurePreviewScope(structurePreviewScopeCleanup{identity: identity, scope: liveScope})
	if got := registeredStructurePreviewScope(identity); got != replacementScope {
		t.Fatal("old cleanup deleted a replacement scope")
	}
}

func structurePreviewScopeCount() int {
	structurePreviewScopes.mu.Lock()
	defer structurePreviewScopes.mu.Unlock()
	return len(structurePreviewScopes.scopes)
}

func registeredStructurePreviewScope(identity weak.Pointer[StructurePlanningKernel]) *structurePreviewScope {
	structurePreviewScopes.mu.Lock()
	defer structurePreviewScopes.mu.Unlock()
	return structurePreviewScopes.scopes[identity]
}

func waitForStructurePreviewScopeCount(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		runtime.GC()
		runtime.Gosched()
		if got := structurePreviewScopeCount(); got == want {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("registered scopes = %d after GC convergence, want %d", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStructurePlanningKernelRejectsUnauthorizedOperationDeltas(t *testing.T) {
	formal := testStructure()
	tests := []struct {
		name      string
		operation domain.StructureRevisionOperation
		candidate func() []domain.VolumeOutline
	}{
		{
			name: "insert multiple chapters", operation: domain.StructureRevisionInsertChapter,
			candidate: func() []domain.VolumeOutline {
				candidate := domain.CloneStructureSnapshot(formal)
				candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters[:1], append([]domain.OutlineEntry{testChapter("ch-extra-1", "One"), testChapter("ch-extra-2", "Two")}, candidate[0].Arcs[0].Chapters[1:]...)...)
				return candidate
			},
		},
		{
			name: "split into multiple chapters", operation: domain.StructureRevisionSplitChapter,
			candidate: func() []domain.VolumeOutline {
				candidate := domain.CloneStructureSnapshot(formal)
				candidate[0].Arcs[0].Chapters[1].CoreEvent = "retained split event"
				candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters[:2], append([]domain.OutlineEntry{testChapter("ch-split-1", "One"), testChapter("ch-split-2", "Two")}, candidate[0].Arcs[0].Chapters[2:]...)...)
				return candidate
			},
		},
		{
			name: "append multiple chapters", operation: domain.StructureRevisionAppendChapter,
			candidate: func() []domain.VolumeOutline {
				candidate := domain.CloneStructureSnapshot(formal)
				candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, testChapter("ch-append-1", "One"), testChapter("ch-append-2", "Two"))
				return candidate
			},
		},
		{
			name: "append multiple arcs", operation: domain.StructureRevisionAppendArc,
			candidate: func() []domain.VolumeOutline {
				candidate := domain.CloneStructureSnapshot(formal)
				candidate[0].Arcs = append(candidate[0].Arcs,
					domain.ArcOutline{ID: "arc-extra-1", Title: "One", Goal: "one", Chapters: []domain.OutlineEntry{testChapter("ch-arc-1", "One")}},
					domain.ArcOutline{ID: "arc-extra-2", Title: "Two", Goal: "two", Chapters: []domain.OutlineEntry{testChapter("ch-arc-2", "Two")}},
				)
				return candidate
			},
		},
		{
			name: "insert at wrong position", operation: domain.StructureRevisionInsertChapter,
			candidate: func() []domain.VolumeOutline {
				candidate := domain.CloneStructureSnapshot(formal)
				candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters[:2], append([]domain.OutlineEntry{testChapter("ch-extra", "Wrong Side")}, candidate[0].Arcs[0].Chapters[2:]...)...)
				return candidate
			},
		},
		{
			name: "reuse volume ID as chapter", operation: domain.StructureRevisionInsertChapter,
			candidate: func() []domain.VolumeOutline {
				candidate := domain.CloneStructureSnapshot(formal)
				candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters[:1], append([]domain.OutlineEntry{testChapter("vol-001", "Collision")}, candidate[0].Arcs[0].Chapters[1:]...)...)
				return candidate
			},
		},
		{
			name: "reorder unrelated chapters", operation: domain.StructureRevisionInsertChapter,
			candidate: func() []domain.VolumeOutline {
				candidate := domain.CloneStructureSnapshot(formal)
				chapters := candidate[0].Arcs[0].Chapters
				candidate[0].Arcs[0].Chapters = []domain.OutlineEntry{chapters[2], testChapter("ch-extra", "Inserted"), chapters[1], chapters[0]}
				return candidate
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proposal := testStructureProposal(t, tt.candidate(), domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "new content requested"})
			if _, err := new(StructurePlanningKernel).Plan(context.Background(), &fakeNormalStructurePlanner{proposal: proposal}, testStructureRequest(formal, tt.operation)); err == nil {
				t.Fatal("expected unauthorized structural delta to be rejected")
			}
		})
	}

	withSecondArc := domain.CloneStructureSnapshot(formal)
	withSecondArc[0].Arcs = append(withSecondArc[0].Arcs, domain.ArcOutline{ID: "arc-002", Title: "Second", Goal: "continue", Chapters: []domain.OutlineEntry{testChapter("ch-004", "Later")}})
	candidate := domain.CloneStructureSnapshot(withSecondArc)
	moved := candidate[0].Arcs[0].Chapters[2]
	candidate[0].Arcs[0].Chapters = candidate[0].Arcs[0].Chapters[:2]
	candidate[0].Arcs[1].Chapters = append([]domain.OutlineEntry{moved}, candidate[0].Arcs[1].Chapters...)
	candidate[0].Arcs[0].Chapters[1].Scenes = append(candidate[0].Arcs[0].Chapters[1].Scenes, "expanded")
	proposal := testStructureProposal(t, candidate, domain.ContentAdditionAssessment{Reason: "expand in place"})
	if _, err := new(StructurePlanningKernel).Plan(context.Background(), &fakeNormalStructurePlanner{proposal: proposal}, testStructureRequest(withSecondArc, domain.StructureRevisionExpandChapter)); err == nil {
		t.Fatal("expected an unauthorized parent change to be rejected")
	}
}

func TestStructurePlanningKernelRequiresCausalEvidenceAndIncludesParentImpacts(t *testing.T) {
	formal := testStructure()
	candidate := domain.CloneStructureSnapshot(formal)
	candidate[0].Arcs[0].Chapters[1].CoreEvent = "expanded target event"
	candidate[0].Arcs[0].Goal = "goal changed because ch-002 changes the outcome"
	candidate[0].Theme = "theme changed because ch-002 changes the outcome"
	proposal := testStructureProposal(t, candidate, domain.ContentAdditionAssessment{Reason: "expand the target"})
	proposal.Impacts = []domain.StructureImpactItem{
		{ArtifactID: "arc-001", ArtifactKind: domain.StructureKindArc, Change: "revise arc goal", Level: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"ch-002 changes the arc outcome"}, DependencySourceIDs: []string{"ch-002"}},
		{ArtifactID: "vol-001", ArtifactKind: domain.StructureKindVolume, Change: "revise volume theme", Level: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"ch-002 changes the volume outcome"}, DependencySourceIDs: []string{"ch-002"}},
	}
	preview, err := new(StructurePlanningKernel).Plan(context.Background(), &fakeNormalStructurePlanner{proposal: proposal}, testStructureRequest(formal, domain.StructureRevisionExpandChapter))
	if err != nil {
		t.Fatalf("Plan complete hierarchy impacts: %v", err)
	}
	for _, id := range []string{"ch-002", "arc-001", "vol-001"} {
		if !hasStructureImpact(preview.Proposal.Impacts, id) {
			t.Fatalf("missing impact for %s: %+v", id, preview.Proposal.Impacts)
		}
	}

	invalidCandidate := domain.CloneStructureSnapshot(formal)
	invalidCandidate[0].Arcs[0].Chapters[1].CoreEvent = "expanded target event"
	invalid := testStructureProposal(t, invalidCandidate, domain.ContentAdditionAssessment{Reason: "expand the target"})
	invalid.Impacts = []domain.StructureImpactItem{{
		ArtifactID: "ch-001", ArtifactKind: domain.StructureKindChapter, Change: "rewrite dependent prose",
		Level: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, RequiresBodyRewrite: true,
		DependencyEvidence: []string{"ch-0020 looks similar but is not the changed stable ID"}, DependencySourceIDs: []string{"ch-0020"},
	}}
	request := testStructureRequest(formal, domain.StructureRevisionExpandChapter)
	request.CompletedChapterIDs = []string{"ch-001"}
	if _, err := new(StructurePlanningKernel).Plan(context.Background(), &fakeNormalStructurePlanner{proposal: invalid}, request); err == nil {
		t.Fatal("expected short-ID substring evidence to be rejected")
	}

	inserted := domain.CloneStructureSnapshot(formal)
	inserted[0].Arcs[0].Chapters = append(inserted[0].Arcs[0].Chapters[:1], append([]domain.OutlineEntry{testChapter("ch-new", "Inserted")}, inserted[0].Arcs[0].Chapters[1:]...)...)
	insertPreview, err := new(StructurePlanningKernel).Plan(context.Background(), &fakeNormalStructurePlanner{proposal: testStructureProposal(t, inserted, domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "insert bridge"})}, testStructureRequest(formal, domain.StructureRevisionInsertChapter))
	if err != nil {
		t.Fatalf("Plan insertion: %v", err)
	}
	for _, id := range []string{"ch-new", "arc-001", "vol-001"} {
		if !hasStructureImpact(insertPreview.Proposal.Impacts, id) {
			t.Fatalf("insertion missing child/parent impact for %s: %+v", id, insertPreview.Proposal.Impacts)
		}
	}

	mixedCandidate := domain.CloneStructureSnapshot(formal)
	mixedCandidate[0].Arcs[0].Goal = "the inserted bridge changes the arc outcome"
	mixedCandidate[0].Arcs[0].Chapters = append(mixedCandidate[0].Arcs[0].Chapters[:1], append([]domain.OutlineEntry{testChapter("ch-mixed", "Mixed")}, mixedCandidate[0].Arcs[0].Chapters[1:]...)...)
	mixedProposal := testStructureProposal(t, mixedCandidate, domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "insert a causal bridge"})
	mixedProposal.Impacts = []domain.StructureImpactItem{{
		ArtifactID: "arc-001", ArtifactKind: domain.StructureKindArc, Change: "revise arc goal for the bridge",
		Level: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency,
		DependencyEvidence: []string{"the new bridge changes the arc outcome"}, DependencySourceIDs: []string{"ch-mixed"},
	}}
	mixedPreview, err := new(StructurePlanningKernel).Plan(context.Background(), &fakeNormalStructurePlanner{proposal: mixedProposal}, testStructureRequest(formal, domain.StructureRevisionInsertChapter))
	if err != nil {
		t.Fatalf("Plan mixed content and parent impact: %v", err)
	}
	foundMixedParent := false
	for _, impact := range mixedPreview.Proposal.Impacts {
		if impact.ArtifactID == "arc-001" && (impact.Cause != domain.StructureImpactContentDependency || !reflect.DeepEqual(impact.DependencySourceIDs, []string{"ch-mixed"})) {
			t.Fatalf("mixed parent impact lost content causality: %+v", impact)
		}
		foundMixedParent = foundMixedParent || impact.ArtifactID == "arc-001"
	}
	if !foundMixedParent {
		t.Fatal("mixed parent impact was not emitted")
	}
}

func TestAdaptiveBatchPlannerShrinksForContextRiskAndLoadsOnlyNecessaryContext(t *testing.T) {
	planner := NewAdaptiveBatchPlanner(AdaptiveBatchConfig{MaxChaptersPerBatch: 3, MaxOutputWords: 9_000, MaxContextUnits: 1_000})
	chapters := []domain.BatchChapter{
		testBatchChapter("ch-1", "arc-1", 3_000, 300, 1, 2, 0),
		testBatchChapter("ch-2", "arc-1", 3_000, 800, 4, 8, 6),
		testBatchChapter("ch-3", "arc-1", 3_000, 700, 2, 4, 3),
		testBatchChapter("ch-4", "arc-2", 3_000, 200, 1, 2, 0),
	}
	plan, err := planner.Build(chapters)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(plan.Batches) != 4 {
		t.Fatalf("batches = %+v, want context/risk shrink plus arc boundary", plan.Batches)
	}
	for _, batch := range plan.Batches {
		if len(batch.ChapterIDs) != 1 {
			t.Fatalf("expected singleton after automatic shrink: %+v", batch)
		}
		for _, item := range batch.Context {
			if !item.Necessary || item.ID == "whole-book" {
				t.Fatalf("batch loaded unnecessary context: %+v", batch.Context)
			}
		}
	}
}

func TestAdaptiveBatchPlannerRejectsIrreducibleOversizedChapter(t *testing.T) {
	planner := NewAdaptiveBatchPlanner(AdaptiveBatchConfig{MaxOutputWords: 2_000, MaxContextUnits: 500})
	if _, err := planner.Build([]domain.BatchChapter{testBatchChapter("ch-large", "arc-1", 2_001, 100, 1, 1, 0)}); err == nil {
		t.Fatal("expected an irreducible output-oversized chapter to be rejected")
	}
	if _, err := planner.Build([]domain.BatchChapter{testBatchChapter("ch-context", "arc-1", 1_000, 501, 1, 1, 0)}); err == nil {
		t.Fatal("expected an irreducible context-oversized chapter to be rejected")
	}
}

func TestAdaptiveBatchPlannerUsesConservativeMaxForConflictingContextUnits(t *testing.T) {
	planner := NewAdaptiveBatchPlanner(AdaptiveBatchConfig{MaxOutputWords: 2_000, MaxContextUnits: 700})
	chapter := testBatchChapter("ch-conflict", "arc-1", 1_000, 100, 1, 1, 0)
	chapter.Context = append(chapter.Context, domain.BatchContextItem{
		ID: "ch-conflict-necessary", Kind: domain.BatchContextCharacterState, Units: 600, Necessary: true,
	})
	plan, err := planner.Build([]domain.BatchChapter{chapter})
	if err != nil {
		t.Fatalf("Build conflicting context estimates: %v", err)
	}
	if got := plan.Batches[0].ContextUnits; got != 600 {
		t.Fatalf("context units = %d, want conservative maximum 600", got)
	}
	if got := len(plan.Batches[0].Context); got != 1 {
		t.Fatalf("deduplicated context items = %d, want 1", got)
	}
}

func TestAdaptiveBatchPlannerRejectsOverflowingEstimates(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	planner := NewAdaptiveBatchPlanner(AdaptiveBatchConfig{MaxChaptersPerBatch: 3, MaxOutputWords: maxInt, MaxContextUnits: maxInt})
	if _, err := planner.Build([]domain.BatchChapter{
		testBatchChapter("ch-output-1", "arc-1", maxInt, 0, 0, 0, 0),
		testBatchChapter("ch-output-2", "arc-1", 1, 0, 0, 0, 0),
	}); err == nil {
		t.Fatal("expected overflowing output total to be rejected")
	}
	contextOverflow := testBatchChapter("ch-context-overflow", "arc-1", 1, maxInt, 0, 0, 0)
	contextOverflow.Context = append(contextOverflow.Context, domain.BatchContextItem{
		ID: "another-context", Kind: domain.BatchContextFact, Units: 1, Necessary: true,
	})
	if _, err := planner.Build([]domain.BatchChapter{contextOverflow}); err == nil {
		t.Fatal("expected overflowing context total to be rejected")
	}
}

func TestBatchPlanFailedBatchIsOrderedBarrier(t *testing.T) {
	plan, err := NewAdaptiveBatchPlanner(AdaptiveBatchConfig{MaxChaptersPerBatch: 1}).Build([]domain.BatchChapter{
		testBatchChapter("ch-1", "arc-1", 1_000, 100, 1, 1, 0),
		testBatchChapter("ch-2", "arc-1", 1_000, 100, 1, 1, 0),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	first, _ := plan.StartNext()
	if err := plan.Fail(first.ID, "model failure"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if next, err := plan.StartNext(); err == nil || next != nil {
		t.Fatalf("failed batch did not block later work: next=%+v err=%v", next, err)
	}
}

func TestBatchPlanOrdersLocalVolumeAndWholeBookReviews(t *testing.T) {
	firstVolume := testBatchChapter("ch-1", "arc-1", 1_000, 100, 1, 1, 0)
	secondVolume := testBatchChapter("ch-2", "arc-2", 1_000, 100, 1, 1, 0)
	secondVolume.VolumeID = "vol-2"
	plan, err := NewAdaptiveBatchPlanner(AdaptiveBatchConfig{MaxChaptersPerBatch: 1}).Build([]domain.BatchChapter{firstVolume, secondVolume})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	first, _ := plan.StartNext()
	if err := plan.MarkGenerated(first.ID); err != nil {
		t.Fatal(err)
	}
	if err := plan.MarkLocalAudit(first.ID, true, "pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.StartNext(); err == nil {
		t.Fatal("second volume started before first-volume review")
	}
	if err := plan.StartVolumeReview("vol-1"); err != nil {
		t.Fatalf("StartVolumeReview: %v", err)
	}
	if err := plan.MarkVolumeReview("vol-1", true, "pass"); err != nil {
		t.Fatalf("MarkVolumeReview: %v", err)
	}
	second, err := plan.StartNext()
	if err != nil || second == nil || second.VolumeID != "vol-2" {
		t.Fatalf("second-volume batch = %+v, err=%v", second, err)
	}
	if err := plan.MarkGenerated(second.ID); err != nil {
		t.Fatal(err)
	}
	if err := plan.MarkLocalAudit(second.ID, true, "pass"); err != nil {
		t.Fatal(err)
	}
	if err := plan.StartWholeBookReview(); err == nil {
		t.Fatal("whole-book review started before every volume review")
	}
	if err := plan.StartVolumeReview("vol-2"); err != nil {
		t.Fatalf("StartVolumeReview vol-2: %v", err)
	}
	if err := plan.MarkVolumeReview("vol-2", true, "pass"); err != nil {
		t.Fatalf("MarkVolumeReview vol-2: %v", err)
	}
	if err := plan.StartWholeBookReview(); err != nil {
		t.Fatalf("StartWholeBookReview: %v", err)
	}
	if err := plan.MarkWholeBookReview(true, "pass"); err != nil {
		t.Fatalf("MarkWholeBookReview: %v", err)
	}
	if plan.WholeBookReview.Status != domain.BatchReviewCompleted {
		t.Fatalf("whole-book status = %q", plan.WholeBookReview.Status)
	}
}

func TestBatchPlanFailureRecoveryAndAggregateReviewGate(t *testing.T) {
	planner := NewAdaptiveBatchPlanner(AdaptiveBatchConfig{MaxChaptersPerBatch: 1})
	plan, err := planner.Build([]domain.BatchChapter{
		testBatchChapter("ch-1", "arc-1", 2_000, 100, 1, 2, 0),
		testBatchChapter("ch-2", "arc-1", 2_000, 100, 1, 2, 0),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	first, err := plan.StartNext()
	if err != nil {
		t.Fatalf("StartNext: %v", err)
	}
	if err := plan.MarkGenerated(first.ID); err != nil {
		t.Fatalf("MarkGenerated: %v", err)
	}
	if err := plan.MarkLocalAudit(first.ID, true, "pass"); err != nil {
		t.Fatalf("MarkLocalAudit: %v", err)
	}
	second, err := plan.StartNext()
	if err != nil {
		t.Fatalf("StartNext second: %v", err)
	}
	if err := plan.Fail(second.ID, "temporary model failure"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if plan.CanRunAggregateReview() {
		t.Fatal("aggregate review must wait for every local batch audit")
	}
	if err := plan.Resume(second.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	resumed, err := plan.StartNext()
	if err != nil || resumed == nil || resumed.ID != second.ID || resumed.Attempts != 2 {
		t.Fatalf("resumed batch = %+v, err=%v", resumed, err)
	}
	if err := plan.MarkGenerated(second.ID); err != nil {
		t.Fatalf("MarkGenerated resumed: %v", err)
	}
	if err := plan.MarkLocalAudit(second.ID, true, "pass"); err != nil {
		t.Fatalf("MarkLocalAudit resumed: %v", err)
	}
	if plan.CanRunAggregateReview() {
		t.Fatal("whole-book review must remain gated until the volume review passes")
	}
	if err := plan.StartVolumeReview("vol-1"); err != nil {
		t.Fatalf("StartVolumeReview: %v", err)
	}
	if err := plan.MarkVolumeReview("vol-1", true, "pass"); err != nil {
		t.Fatalf("MarkVolumeReview: %v", err)
	}
	if !plan.CanRunAggregateReview() {
		t.Fatal("whole-book aggregate review should unlock after every volume review passes")
	}
}

func testStructureRequest(formal []domain.VolumeOutline, operation domain.StructureRevisionOperation) domain.StructureRevisionRequest {
	request := domain.StructureRevisionRequest{
		Operation: operation, Intent: "add the missing turn", Stage: domain.ManuscriptStageWriting,
		BaseRevision: 7, Current: formal,
	}
	if operation == domain.StructureRevisionInsertChapter || operation == domain.StructureRevisionExpandChapter || operation == domain.StructureRevisionSplitChapter {
		request.TargetID = "ch-002"
	}
	return request
}

func testStructure() []domain.VolumeOutline {
	return []domain.VolumeOutline{{
		ID: "vol-001", Index: 1, Title: "Opening", Theme: "trust",
		Arcs: []domain.ArcOutline{{
			ID: "arc-001", Index: 1, Title: "First Trial", Goal: "test the alliance",
			Chapters: []domain.OutlineEntry{
				testChapter("ch-001", "Arrival"),
				testChapter("ch-002", "Trial"),
				testChapter("ch-003", "Choice"),
			},
		}},
	}}
}

func testChapter(id, title string) domain.OutlineEntry {
	facts := domain.ExpansionDramaticFactSet{SchemaVersion: domain.ExpansionDramaticFactsSchemaV1, GoalState: "pursued", ConflictState: "active", ChoiceState: "committed", CostState: "paid", ResultState: "achieved", CharacterBefore: "passive", CharacterAfter: "active", ClimaxState: "occurred", ExitState: "irreversible", ImpactState: "required"}
	return domain.OutlineEntry{
		ID: id, Title: title, CoreEvent: title + " event", Hook: title + " hook", Scenes: []string{title + " scene"}, DramaticFacts: &facts,
	}
}

func testStructureProposal(t *testing.T, candidate []domain.VolumeOutline, assessment domain.ContentAdditionAssessment) domain.StructureRevisionProposal {
	t.Helper()
	budget, err := domain.NewDynamicSoftBudget(domain.TotalChapters(candidate), 1_800, 2_600)
	if err != nil {
		t.Fatalf("NewDynamicSoftBudget: %v", err)
	}
	return domain.StructureRevisionProposal{Assessment: assessment, Candidate: candidate, SoftBudget: budget}
}

func testBatchChapter(id, arcID string, outputWords, contextUnits, complexity, characters, anchors int) domain.BatchChapter {
	return domain.BatchChapter{
		ID: id, VolumeID: "vol-1", ArcID: arcID, EstimatedOutputWords: outputWords,
		Complexity: complexity, CharacterCount: characters, SourceAnchorCount: anchors,
		Context: []domain.BatchContextItem{
			{ID: id + "-necessary", Kind: domain.BatchContextCharacterState, Units: contextUnits, Necessary: true},
			{ID: "whole-book", Kind: domain.BatchContextFact, Units: 50_000, Necessary: false},
		},
	}
}

func hasStructureImpact(impacts []domain.StructureImpactItem, id string) bool {
	for _, impact := range impacts {
		if impact.ArtifactID == id {
			return true
		}
	}
	return false
}
