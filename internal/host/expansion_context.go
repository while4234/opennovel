package host

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const (
	expansionContextBudgetBytes = 48 * 1024
	expansionSourceBudgetUnits  = 10_000
)

type ExpansionContext struct {
	Mode                domain.RevisionMode          `json:"mode"`
	Stage               domain.ManuscriptStage       `json:"stage"`
	Location            domain.ExpansionLocationKind `json:"location"`
	ReferenceIDs        []string                     `json:"reference_ids,omitempty"`
	Sentence            string                       `json:"sentence"`
	Structure           []domain.VolumeOutline       `json:"structure"`
	StructureSignature  string                       `json:"structure_signature"`
	CompletedChapterIDs []string                     `json:"completed_chapter_ids,omitempty"`
	LocalSummaries      []string                     `json:"local_summaries,omitempty"`
	VolumeSummaries     []string                     `json:"volume_summaries,omitempty"`
	DependencyBatches   []ExpansionDependencyBatch   `json:"dependency_batches"`
	SkeletonSummary     string                       `json:"skeleton_summary"`
	Adaptation          *ExpansionAdaptationContext  `json:"adaptation,omitempty"`
	Diagnostics         ExpansionContextDiagnostics  `json:"diagnostics"`
}

type ExpansionAdaptationContext struct {
	Plan              *domain.AdaptationPlan           `json:"plan"`
	Manifest          *domain.AdaptationSourceManifest `json:"manifest"`
	RelevantSummaries []string                         `json:"relevant_source_summaries,omitempty"`
}

type ExpansionContextDiagnostics struct {
	Bytes             int      `json:"bytes"`
	SourceUnits       int      `json:"source_units,omitempty"`
	Dropped           []string `json:"dropped,omitempty"`
	DependencyBatches int      `json:"dependency_batches"`
}

type ExpansionDependencyBatch struct {
	ID                 string                             `json:"id"`
	VolumeIDs          []string                           `json:"volume_ids"`
	HighRisk           bool                               `json:"high_risk"`
	Summary            string                             `json:"summary"`
	InputSignature     string                             `json:"input_signature"`
	ReductionSignature string                             `json:"reduction_signature"`
	ArcBatches         []ExpansionArcDependencyBatch      `json:"arc_batches"`
	ReviewReceipts     []domain.ExpansionDependencyReview `json:"review_receipts"`
}

type ExpansionArcDependencyBatch struct {
	ID             string   `json:"id"`
	ArcID          string   `json:"arc_id"`
	ChapterIDs     []string `json:"chapter_ids"`
	Summary        string   `json:"summary"`
	InputSignature string   `json:"input_signature"`
}

func BuildExpansionContext(st *storepkg.Store, request domain.ExpansionRequest) (ExpansionContext, error) {
	if st == nil {
		return ExpansionContext{}, fmt.Errorf("expansion store is required")
	}
	structure, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return ExpansionContext{}, err
	}
	if err := domain.ValidateStructureSnapshot(structure); err != nil {
		return ExpansionContext{}, fmt.Errorf("load expansion structure: %w", err)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		return ExpansionContext{}, err
	}
	stage := domain.ManuscriptStageOutlineComplete
	if progress != nil {
		switch progress.Phase {
		case domain.PhaseWriting:
			stage = domain.ManuscriptStageWriting
		case domain.PhaseComplete:
			stage = domain.ManuscriptStageComplete
		}
	}
	completed := completedStableChapterIDs(structure, progress)
	context := ExpansionContext{
		Mode: domain.RevisionModeNormal, Stage: stage, Location: request.Location,
		ReferenceIDs: append([]string(nil), request.ReferenceIDs...), Sentence: strings.TrimSpace(request.Sentence),
		Structure: domain.CloneStructureSnapshot(structure), StructureSignature: domain.StructureSignature(structure),
		CompletedChapterIDs: completed,
	}
	context.LocalSummaries, context.VolumeSummaries, context.DependencyBatches, context.SkeletonSummary = buildExpansionMapReduce(structure, request.ReferenceIDs, completed)
	enrichExpansionDependencies(st, &context)
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil {
		return ExpansionContext{}, err
	}
	if manifest != nil {
		context.Mode = domain.RevisionModeAdaptation
		plan, loadErr := st.Adaptation.LoadPlan()
		if loadErr != nil {
			return ExpansionContext{}, loadErr
		}
		if plan == nil {
			plan, loadErr = st.Adaptation.LoadProposal()
			if loadErr != nil {
				return ExpansionContext{}, loadErr
			}
		}
		manifestView := *manifest
		manifestView.SourcePath = ""
		manifestView.Chapters = append([]domain.AdaptationSource(nil), manifest.Chapters...)
		for index := range manifestView.Chapters {
			manifestView.Chapters[index].Path = ""
		}
		planView := compactExpansionAdaptationPlan(plan)
		context.Adaptation = &ExpansionAdaptationContext{Plan: planView, Manifest: &manifestView}
		reports, reportsErr := st.Adaptation.LoadSourceReports()
		if reportsErr != nil {
			return ExpansionContext{}, reportsErr
		}
		relevantSources := expansionRelevantSourceChapters(plan, request.ReferenceIDs)
		for _, report := range reports {
			if len(relevantSources) > 0 {
				if _, ok := relevantSources[report.Chapter]; !ok {
					continue
				}
			}
			summary := struct {
				Chapter        int      `json:"chapter"`
				Title          string   `json:"title"`
				Summary        string   `json:"summary"`
				KeyEvents      []string `json:"key_events,omitempty"`
				CharacterFacts []string `json:"character_facts,omitempty"`
				WorldRules     []string `json:"world_rules,omitempty"`
			}{report.Chapter, report.Title, report.Summary, append([]string(nil), report.KeyEvents...), append([]string(nil), report.CharacterFacts...), append([]string(nil), report.WorldRules...)}
			payload, _ := json.Marshal(summary)
			if context.Diagnostics.SourceUnits+len([]rune(string(payload))) > expansionSourceBudgetUnits {
				context.Diagnostics.Dropped = append(context.Diagnostics.Dropped, "source summaries beyond 10,000 units")
				break
			}
			context.Diagnostics.SourceUnits += len([]rune(string(payload)))
			context.Adaptation.RelevantSummaries = append(context.Adaptation.RelevantSummaries, string(payload))
		}
	}
	context.Diagnostics.DependencyBatches = len(context.DependencyBatches)
	payload, _ := json.Marshal(context)
	context.Diagnostics.Bytes = len(payload)
	if len(payload) > expansionContextBudgetBytes {
		// Source summaries are optional synthesis. Contracts and current structure
		// remain authoritative and are never silently truncated.
		if context.Adaptation != nil && len(context.Adaptation.RelevantSummaries) > 0 {
			context.Adaptation.RelevantSummaries = nil
			context.Diagnostics.Dropped = append(context.Diagnostics.Dropped, "source summaries to satisfy 60KiB context budget")
			payload, _ = json.Marshal(context)
			context.Diagnostics.Bytes = len(payload)
		}
	}
	if context.Diagnostics.Bytes > expansionContextBudgetBytes {
		// Preserve the real dependency graph and signatures while reducing the
		// human-readable summaries. This is a deterministic map/reduce fallback,
		// not silent loss of a dependency edge.
		context.LocalSummaries = compactSignedSummaries(context.LocalSummaries, "local")
		context.VolumeSummaries = compactSignedSummaries(context.VolumeSummaries, "volume")
		for index := range context.DependencyBatches {
			context.DependencyBatches[index].Summary = "signed dependency reduction " + context.DependencyBatches[index].ReductionSignature
			for arc := range context.DependencyBatches[index].ArcBatches {
				context.DependencyBatches[index].ArcBatches[arc].Summary = "signed arc reduction " + context.DependencyBatches[index].ArcBatches[arc].InputSignature
			}
		}
		context.Diagnostics.Dropped = append(context.Diagnostics.Dropped, "verbose dependency summaries reduced to signed artifacts")
		payload, _ = json.Marshal(context)
		context.Diagnostics.Bytes = len(payload)
	}
	if context.Diagnostics.Bytes > expansionContextBudgetBytes {
		return ExpansionContext{}, fmt.Errorf("expansion context is %d bytes (limit %d); compact authoritative structure before retry", context.Diagnostics.Bytes, expansionContextBudgetBytes)
	}
	return context, nil
}

func buildExpansionMapReduce(structure []domain.VolumeOutline, references, completed []string) ([]string, []string, []ExpansionDependencyBatch, string) {
	selected := make(map[string]struct{}, len(references)+len(completed))
	for _, id := range append(append([]string(nil), references...), completed...) {
		selected[strings.TrimSpace(id)] = struct{}{}
	}
	var local, volumes []string
	var batches []ExpansionDependencyBatch
	for index, volume := range domain.ProjectLayeredOutlineOrder(structure) {
		chapterCount, highRisk := 0, false
		for _, arc := range volume.Arcs {
			for _, chapter := range arc.Chapters {
				chapterCount++
				if _, ok := selected[chapter.ID]; ok {
					highRisk = true
					local = append(local, fmt.Sprintf("第%d章 %s（所在卷：%s）", chapter.Chapter, strings.TrimSpace(chapter.Title), strings.TrimSpace(volume.Title)))
				}
			}
		}
		summary := fmt.Sprintf("第%d卷 %s：%d个结构章节、%d条故事弧", index+1, strings.TrimSpace(volume.Title), chapterCount, len(volume.Arcs))
		volumes = append(volumes, summary)
		limit := 4
		if highRisk {
			limit = 2
		}
		if len(batches) == 0 || batches[len(batches)-1].HighRisk != highRisk || len(batches[len(batches)-1].VolumeIDs) >= limit {
			batches = append(batches, ExpansionDependencyBatch{ID: fmt.Sprintf("dependency-batch-%03d", len(batches)+1), HighRisk: highRisk})
		}
		batch := &batches[len(batches)-1]
		batch.VolumeIDs = append(batch.VolumeIDs, volume.ID)
		if batch.Summary == "" {
			batch.Summary = summary
		} else {
			batch.Summary += "；" + summary
		}
		for _, arc := range volume.Arcs {
			for offset := 0; offset < len(arc.Chapters); offset += 3 {
				end := min(len(arc.Chapters), offset+3)
				arcBatch := ExpansionArcDependencyBatch{ID: fmt.Sprintf("%s-arc-%03d-part-%03d", volume.ID, arc.Index, offset/3+1), ArcID: arc.ID}
				for _, chapter := range arc.Chapters[offset:end] {
					arcBatch.ChapterIDs = append(arcBatch.ChapterIDs, chapter.ID)
					arcBatch.Summary += fmt.Sprintf("[%s|%s|%s]", strings.TrimSpace(chapter.Title), strings.TrimSpace(chapter.CoreEvent), strings.TrimSpace(chapter.Hook))
				}
				arcBatch.Summary = fmt.Sprintf("arc=%s goal=%s local=%s", strings.TrimSpace(arc.Title), strings.TrimSpace(arc.Goal), arcBatch.Summary)
				arcBatch.InputSignature = domain.JSONContentSignature([]byte(arcBatch.Summary))
				batch.ArcBatches = append(batch.ArcBatches, arcBatch)
			}
		}
	}
	for index := range batches {
		payload, _ := json.Marshal(struct {
			Volumes []string
			Arcs    []ExpansionArcDependencyBatch
		}{batches[index].VolumeIDs, batches[index].ArcBatches})
		batches[index].InputSignature = domain.JSONContentSignature(payload)
		arcIDs := make([]string, 0, len(batches[index].ArcBatches))
		for _, arc := range batches[index].ArcBatches {
			arcIDs = append(arcIDs, arc.ID)
		}
		batches[index].ReductionSignature = domain.JSONContentSignature(payload)
	}
	skeleton := strings.Join(volumes, "；")
	return local, volumes, batches, skeleton
}

type ExpansionDependencyAuditPendingError struct{ TaskID string }

func (err *ExpansionDependencyAuditPendingError) Error() string {
	return "expansion dependency audit pending: " + err.TaskID
}

// reviewExpansionContextDependencies only consumes trusted signed receipts or
// durably enqueues the next missing node. It cannot execute or sign a review.
// The independent runner processes one task, the caller ingests its artifact,
// and retries Plan; unchanged sibling receipts remain reusable across restart.
func (planner *ExpansionPlanner) reviewExpansionContextDependencies(ctx context.Context, snapshot *ExpansionContext) error {
	if planner == nil || snapshot == nil || len(planner.auditorPublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("independent expansion dependency auditor is unavailable")
	}
	planner.mu.Lock()
	defer planner.mu.Unlock()
	reviewOne := func(task ExpansionDependencyAuditTask) (domain.ExpansionDependencyReview, error) {
		task.ChildArtifacts = expansionDependencyRefs(task.ChildReviews)
		key := task.Stage + "\x00" + task.ScopeID
		outputSignature := domain.JSONContentSignature([]byte(task.Output))
		priorID := planner.dependencyReviewIndex[key]
		if prior, ok := planner.dependencyReviews[priorID]; ok && prior.Decision == "pass" && prior.InputSignature == task.InputSignature &&
			prior.OutputSignature == outputSignature && slices.Equal(prior.DependencyIDs, task.DependencyIDs) &&
			verifyExpansionDependencyReview(prior, planner.auditorPublicKey) == nil {
			return prior, nil
		}
		task.ID = expansionDependencyTaskID(task)
		task.RootAuditTaskID = task.ID
		task.ContractSignature = expansionDependencyContractSignature(task)
		task.PolicyVersion = expansionDependencyPolicyVersion
		task.Status = "pending"
		task.CreatedAt = planner.now().UTC()
		planner.pendingDependencyAudits[task.ID] = task
		if err := planner.persistLocked(); err != nil {
			return domain.ExpansionDependencyReview{}, err
		}
		return domain.ExpansionDependencyReview{}, &ExpansionDependencyAuditPendingError{TaskID: task.ID}
	}
	for batchIndex := range snapshot.DependencyBatches {
		batch := &snapshot.DependencyBatches[batchIndex]
		batch.ReviewReceipts = nil
		localSignatures := make([]string, 0, len(batch.ArcBatches))
		for _, arc := range batch.ArcBatches {
			review, err := reviewOne(ExpansionDependencyAuditTask{Stage: "local", ScopeID: arc.ID, InputSignature: arc.InputSignature, Output: arc.Summary, DependencyIDs: append([]string(nil), arc.ChapterIDs...)})
			if err != nil {
				return fmt.Errorf("local dependency review %s: %w", arc.ID, err)
			}
			batch.ReviewReceipts = append(batch.ReviewReceipts, review)
			localSignatures = append(localSignatures, review.ArtifactSignature)
		}
		volumeInput, _ := json.Marshal(struct {
			Base     string
			Children []string
		}{batch.InputSignature, localSignatures})
		arcIDs := make([]string, 0, len(batch.ArcBatches))
		for _, arc := range batch.ArcBatches {
			arcIDs = append(arcIDs, arc.ID)
		}
		volume, err := reviewOne(ExpansionDependencyAuditTask{Stage: "volume", ScopeID: batch.ID, InputSignature: domain.JSONContentSignature(volumeInput), Output: batch.Summary, DependencyIDs: arcIDs, ChildReviews: append([]domain.ExpansionDependencyReview(nil), batch.ReviewReceipts...)})
		if err != nil {
			return fmt.Errorf("volume dependency review %s: %w", batch.ID, err)
		}
		batch.ReviewReceipts = append(batch.ReviewReceipts, volume)
		reductionPayload, _ := json.Marshal(struct {
			BatchID string   `json:"batch_id"`
			Summary string   `json:"summary"`
			ArcIDs  []string `json:"arc_ids"`
		}{batch.ID, batch.Summary, arcIDs})
		aggregate, err := reviewOne(ExpansionDependencyAuditTask{Stage: "batch", ScopeID: batch.ID + "-aggregate", InputSignature: domain.JSONContentSignature(reductionPayload), Output: string(reductionPayload), DependencyIDs: []string{batch.ID}, ChildReviews: []domain.ExpansionDependencyReview{volume}})
		if err != nil {
			return fmt.Errorf("batch dependency review %s: %w", batch.ID, err)
		}
		batch.ReviewReceipts = append(batch.ReviewReceipts, aggregate)
		batch.ReductionSignature = domain.JSONContentSignature(reductionPayload)
	}
	aggregateIDs, aggregateSignatures := make([]string, 0, len(snapshot.DependencyBatches)), make([]string, 0, len(snapshot.DependencyBatches))
	for _, batch := range snapshot.DependencyBatches {
		for _, review := range batch.ReviewReceipts {
			if review.Stage == "batch" {
				aggregateIDs = append(aggregateIDs, review.ScopeID)
				aggregateSignatures = append(aggregateSignatures, review.ArtifactSignature)
			}
		}
	}
	skeletonInput, _ := json.Marshal(struct {
		Structure string
		Children  []string
	}{snapshot.StructureSignature, aggregateSignatures})
	children := make([]domain.ExpansionDependencyReview, 0, len(aggregateIDs))
	for _, batch := range snapshot.DependencyBatches {
		for _, review := range batch.ReviewReceipts {
			if review.Stage == "batch" {
				children = append(children, review)
			}
		}
	}
	skeleton, err := reviewOne(ExpansionDependencyAuditTask{Stage: "skeleton", ScopeID: "whole-structure", InputSignature: domain.JSONContentSignature(skeletonInput), Output: snapshot.SkeletonSummary, DependencyIDs: aggregateIDs, ChildReviews: children})
	if err != nil {
		return fmt.Errorf("skeleton dependency review: %w", err)
	}
	if len(snapshot.DependencyBatches) > 0 {
		snapshot.DependencyBatches[len(snapshot.DependencyBatches)-1].ReviewReceipts = append(snapshot.DependencyBatches[len(snapshot.DependencyBatches)-1].ReviewReceipts, skeleton)
	}
	return planner.persistLocked()
}

func expansionDependencyTaskID(task ExpansionDependencyAuditTask) string {
	payload, _ := json.Marshal(struct {
		Stage, ScopeID, InputSignature, OutputSignature string
		Dependencies                                    []string
	}{task.Stage, task.ScopeID, task.InputSignature, domain.JSONContentSignature([]byte(task.Output)), task.DependencyIDs})
	return "dep-" + domain.JSONContentSignature(payload)
}

func (planner *ExpansionPlanner) AcceptDependencyReview(taskID string, review domain.ExpansionDependencyReview) error {
	if planner == nil {
		return fmt.Errorf("expansion planner is unavailable")
	}
	planner.mu.Lock()
	defer planner.mu.Unlock()
	task, ok := planner.pendingDependencyAudits[strings.TrimSpace(taskID)]
	if !ok {
		return ErrExpansionDependencyTaskNotFound
	}
	if review.Stage != task.Stage || review.ScopeID != task.ScopeID || review.InputSignature != task.InputSignature || review.OutputSignature != domain.JSONContentSignature([]byte(task.Output)) || !slices.Equal(review.DependencyIDs, task.DependencyIDs) {
		return fmt.Errorf("dependency review does not match its durable task")
	}
	if err := verifyExpansionDependencyReview(review, planner.auditorPublicKey); err != nil {
		return err
	}
	key := task.Stage + "\x00" + task.ScopeID
	planner.dependencyReviews[review.ArtifactSignature] = review
	planner.dependencyReviewIndex[key] = review.ArtifactSignature
	delete(planner.pendingDependencyAudits, task.ID)
	if err := planner.persistLocked(); err != nil {
		return err
	}
	if review.Decision != "pass" {
		return fmt.Errorf("dependency audit %s/%s needs fix: %s", review.Stage, review.ScopeID, strings.Join(review.Findings, "; "))
	}
	return nil
}

// ExpansionAffectedDependencyScopes compares signed graph inputs and then
// propagates staleness only through declared dependency edges. Unrelated
// batches remain reusable.
func ExpansionAffectedDependencyScopes(previous, current []domain.ExpansionDependencyReview) []string {
	before := make(map[string]domain.ExpansionDependencyReview, len(previous))
	for _, review := range previous {
		before[review.Stage+"\x00"+review.ScopeID] = review
	}
	stale := make(map[string]struct{})
	for _, review := range current {
		key := review.Stage + "\x00" + review.ScopeID
		old, ok := before[key]
		if !ok || old.InputSignature != review.InputSignature || old.ArtifactSignature != review.ArtifactSignature {
			stale[review.ScopeID] = struct{}{}
		}
	}
	changed := true
	for changed {
		changed = false
		for _, review := range current {
			if _, already := stale[review.ScopeID]; already {
				continue
			}
			for _, dependency := range review.DependencyIDs {
				if _, affected := stale[dependency]; affected {
					stale[review.ScopeID] = struct{}{}
					changed = true
					break
				}
			}
		}
	}
	result := make([]string, 0, len(stale))
	for scope := range stale {
		result = append(result, scope)
	}
	slices.Sort(result)
	return result
}

func enrichExpansionDependencies(st *storepkg.Store, context *ExpansionContext) {
	if snapshots, err := st.Characters.LoadLatestSnapshots(); err == nil {
		for _, snapshot := range snapshots {
			context.LocalSummaries = append(context.LocalSummaries, fmt.Sprintf("character=%s status=%s motivation=%s relations=%s", snapshot.Name, snapshot.Status, snapshot.Motivation, snapshot.Relations))
		}
	}
	if compass, err := st.Outline.LoadCompass(); err == nil && compass != nil {
		context.SkeletonSummary += fmt.Sprintf(" ending=%s open_threads=%s", compass.EndingDirection, strings.Join(compass.OpenThreads, ";"))
	}
	for _, batch := range context.DependencyBatches {
		context.SkeletonSummary += " dependency=" + batch.ID + ":" + batch.ReductionSignature
	}
}

func compactSignedSummaries(values []string, scope string) []string {
	result := make([]string, 0, len(values))
	for index, value := range values {
		result = append(result, fmt.Sprintf("%s-%03d:%s", scope, index+1, domain.JSONContentSignature([]byte(value))))
	}
	return result
}

func compactExpansionAdaptationPlan(plan *domain.AdaptationPlan) *domain.AdaptationPlan {
	if plan == nil {
		return nil
	}
	payload, _ := json.Marshal(plan)
	var result domain.AdaptationPlan
	_ = json.Unmarshal(payload, &result)
	result.Planner = nil
	result.OutlineQualityAudit = nil
	result.BudgetRepair = nil
	return &result
}

func expansionRelevantSourceChapters(plan *domain.AdaptationPlan, references []string) map[int]struct{} {
	if plan == nil {
		return nil
	}
	referenceSet := make(map[string]struct{}, len(references))
	for _, id := range references {
		referenceSet[strings.TrimSpace(id)] = struct{}{}
	}
	result := make(map[int]struct{})
	for index, chapter := range plan.Chapters {
		_, selected := referenceSet[strings.TrimSpace(chapter.ID)]
		if !selected && len(referenceSet) > 0 {
			continue
		}
		for adjacent := max(0, index-1); adjacent <= min(len(plan.Chapters)-1, index+1); adjacent++ {
			for _, source := range plan.Chapters[adjacent].SourceChapters {
				result[source] = struct{}{}
			}
			from, to := plan.Chapters[adjacent].SourceRange.From, plan.Chapters[adjacent].SourceRange.To
			for source := from; source > 0 && source <= to; source++ {
				result[source] = struct{}{}
			}
		}
	}
	if len(result) == 0 && len(plan.Chapters) > 0 {
		last := plan.Chapters[len(plan.Chapters)-1]
		for _, source := range last.SourceChapters {
			result[source] = struct{}{}
		}
	}
	return result
}

func completedStableChapterIDs(structure []domain.VolumeOutline, progress *domain.Progress) []string {
	if progress == nil {
		return nil
	}
	completedNumbers := make(map[int]struct{}, len(progress.CompletedChapters))
	for _, number := range progress.CompletedChapters {
		completedNumbers[number] = struct{}{}
	}
	var result []string
	for _, chapter := range domain.FlattenOutline(structure) {
		if _, ok := completedNumbers[chapter.Chapter]; ok {
			result = append(result, chapter.ID)
		}
	}
	return result
}

func expansionDependencyBatches(volumeCount int, mode domain.RevisionMode) int {
	limit := 4
	if mode == domain.RevisionModeAdaptation {
		limit = 4
	}
	if volumeCount <= 0 {
		return 1
	}
	return (volumeCount + limit - 1) / limit
}
