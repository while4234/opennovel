package store

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const continuationCommitJournalFile = "meta/continuation/commit_journal.json"

const (
	continuationJournalPrepared  = "prepared"
	continuationJournalCommitted = "committed"
)

type continuationCommitJournal struct {
	Stage          string                      `json:"stage"`
	OutlineExisted bool                        `json:"outline_existed"`
	Outline        []domain.OutlineEntry       `json:"outline,omitempty"`
	LayeredExisted bool                        `json:"layered_existed"`
	Layered        []domain.VolumeOutline      `json:"layered,omitempty"`
	Progress       *domain.Progress            `json:"progress,omitempty"`
	Workflow       domain.ContinuationWorkflow `json:"workflow"`
	Plan           *domain.ContinuationPlan    `json:"plan,omitempty"`
}

// CommitContinuationPlan is the final approval transaction. It verifies that
// the canonical outline still ends at the imported baseline, appends only
// approved N+1..M chapters, updates Progress.TotalChapters, and opens the
// ready_to_write gate. It never calls Resume or starts Writer.
//
// A durable before-image journal plus in-process rollback protects the
// multi-file transaction. If a process dies mid-commit, the next attempt first
// restores the before-image and can then safely retry.
func (s *Store) CommitContinuationPlan(expectedRevision int) (*domain.ContinuationSnapshot, error) {
	if s == nil || s.Revisions == nil {
		return nil, fmt.Errorf("revision store is required before continuation commit")
	}
	var committed *domain.ContinuationSnapshot
	err := s.Revisions.withLegacyMigrationMutation("commit continuation plan", s.Outline.migration, func() error {
		var commitErr error
		committed, commitErr = s.commitContinuationPlanOwned(expectedRevision)
		return commitErr
	})
	return committed, err
}

func (s *Store) commitContinuationPlanOwned(expectedRevision int) (*domain.ContinuationSnapshot, error) {
	if s == nil || s.Continuation == nil {
		return nil, fmt.Errorf("continuation store is required")
	}
	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	s.Outline.io.mu.Lock()
	defer s.Outline.io.mu.Unlock()
	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()
	s.Continuation.io.mu.Lock()
	defer s.Continuation.io.mu.Unlock()

	if err := s.recoverContinuationCommitUnlocked(); err != nil {
		return nil, fmt.Errorf("recover interrupted continuation commit: %w", err)
	}
	snapshot, err := s.Continuation.loadSnapshotUnlocked()
	if err != nil {
		return nil, err
	}
	if snapshot.Workflow.Revision != expectedRevision {
		return nil, &ContinuationRevisionConflictError{Expected: expectedRevision, Actual: snapshot.Workflow.Revision}
	}
	if snapshot.Workflow.Stage != domain.ContinuationStageOutlineReviewPending &&
		snapshot.Workflow.Stage != domain.ContinuationStageReadyToWrite {
		return nil, fmt.Errorf("continuation stage is %q, expected outline_review_pending or ready_to_write", snapshot.Workflow.Stage)
	}
	plan, err := continuationPlanForCommit(snapshot)
	if err != nil {
		return nil, err
	}

	canonical, outlineExisted, err := loadOptionalOutlineUnlocked(s.Outline.io)
	if err != nil {
		return nil, err
	}
	layered, layeredExisted, err := loadOptionalLayeredOutlineUnlocked(s.Outline.io)
	if err != nil {
		return nil, err
	}
	progress, err := s.Progress.loadUnlocked()
	if err != nil {
		return nil, err
	}
	if progress == nil {
		return nil, fmt.Errorf("continuation progress is not initialized")
	}

	if continuationPlanAlreadyCommitted(canonical, progress, plan) {
		return snapshot, nil
	}
	if err := validateContinuationBaseline(snapshot.Workflow.BaseChapterCount, canonical, layered, layeredExisted, progress); err != nil {
		return nil, err
	}
	mergedOutline := append(cloneOutlineEntries(canonical), cloneOutlineEntries(plan.Chapters)...)
	mergedLayered := layered
	if layeredExisted {
		mergedLayered, err = mergeContinuationLayeredOutline(layered, plan)
		if err != nil {
			return nil, err
		}
		mergedOutline = domain.FlattenOutline(mergedLayered)
	}
	if len(mergedOutline) != snapshot.Workflow.BaseChapterCount+len(plan.Chapters) {
		return nil, fmt.Errorf("committed continuation outline has %d chapters, want %d", len(mergedOutline), snapshot.Workflow.BaseChapterCount+len(plan.Chapters))
	}
	if !reflect.DeepEqual(mergedOutline[:snapshot.Workflow.BaseChapterCount], canonical) {
		return nil, fmt.Errorf("continuation commit would modify imported chapters 1-%d", snapshot.Workflow.BaseChapterCount)
	}
	if layeredExisted {
		stableLayered, err := s.Outline.identity.prepareLayeredOutlineForSave(layered, layered, canonical)
		if err != nil {
			return nil, err
		}
		prepared, err := s.Outline.identity.prepareLayeredOutlineForSave(mergedLayered, stableLayered, domain.FlattenOutline(stableLayered))
		if err != nil {
			return nil, err
		}
		layered = stableLayered
		canonical = domain.FlattenOutline(stableLayered)
		mergedLayered = prepared
		mergedOutline = domain.FlattenOutline(prepared)
	} else {
		stableCanonical, err := s.Outline.identity.prepareOutlineForSave(canonical, canonical, nil)
		if err != nil {
			return nil, err
		}
		prepared, err := s.Outline.identity.prepareOutlineForSave(mergedOutline, stableCanonical, nil)
		if err != nil {
			return nil, err
		}
		canonical = stableCanonical
		mergedOutline = prepared
	}
	plan.Chapters = cloneOutlineEntries(mergedOutline[snapshot.Workflow.BaseChapterCount:])

	journal := continuationCommitJournal{
		Stage:          continuationJournalPrepared,
		OutlineExisted: outlineExisted,
		Outline:        cloneOutlineEntries(canonical),
		LayeredExisted: layeredExisted,
		Layered:        cloneVolumeOutlines(layered),
		Progress:       cloneProgress(progress),
		Workflow:       snapshot.Workflow,
		Plan:           cloneContinuationPlan(snapshot.Plan),
	}
	if err := s.Continuation.io.WriteJSONUnlocked(continuationCommitJournalFile, journal); err != nil {
		return nil, err
	}

	updatedProgress := cloneProgress(progress)
	updatedProgress.TotalChapters = len(mergedOutline)
	updatedProgress.Layered = layeredExisted
	if updatedProgress.CurrentChapter <= snapshot.Workflow.BaseChapterCount {
		updatedProgress.CurrentChapter = snapshot.Workflow.BaseChapterCount + 1
	}
	snapshot.Plan = plan
	if snapshot.Workflow.Stage == domain.ContinuationStageOutlineReviewPending {
		if err := snapshot.Workflow.Transition(domain.ContinuationStageReadyToWrite); err != nil {
			return nil, s.rollbackContinuationCommitUnlocked(journal, err)
		}
	}
	snapshot.Workflow.Version = domain.ContinuationSchemaVersion
	snapshot.Workflow.Revision++
	snapshot.Workflow.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	snapshot.Plan.ApprovedRevision = snapshot.Workflow.Revision

	commitErr := s.writeContinuationCommitUnlocked(mergedOutline, mergedLayered, layeredExisted, updatedProgress, snapshot)
	if commitErr != nil {
		return nil, s.rollbackContinuationCommitUnlocked(journal, commitErr)
	}
	if err := s.Continuation.io.RemoveFileUnlocked(continuationCommitJournalFile); err != nil {
		return nil, fmt.Errorf("continuation plan committed but journal cleanup failed: %w", err)
	}
	return snapshot, nil
}

func continuationPlanForCommit(snapshot *domain.ContinuationSnapshot) (*domain.ContinuationPlan, error) {
	if snapshot == nil || snapshot.Proposal == nil || snapshot.Outlines == nil {
		return nil, fmt.Errorf("continuation proposal and detailed outline are required")
	}
	chapters, err := domain.FlattenContinuationOutline(snapshot.Workflow.BaseChapterCount, *snapshot.Outlines)
	if err != nil {
		return nil, err
	}
	if len(chapters) != snapshot.Proposal.TargetChapterCount {
		return nil, fmt.Errorf("continuation outline has %d chapters, want %d", len(chapters), snapshot.Proposal.TargetChapterCount)
	}
	if snapshot.Plan != nil {
		if !continuationPlanMatchesCandidate(snapshot.Plan.Chapters, chapters) {
			return nil, fmt.Errorf("approved continuation plan differs from candidate outline")
		}
		return cloneContinuationPlan(snapshot.Plan), nil
	}
	return &domain.ContinuationPlan{
		SourceSignature:  snapshot.Workflow.SourceSignature,
		BaseChapterCount: snapshot.Workflow.BaseChapterCount,
		Proposal:         *snapshot.Proposal,
		Volumes:          cloneVolumeOutlines(snapshot.Volumes),
		Outlines:         cloneContinuationOutline(*snapshot.Outlines),
		Chapters:         cloneOutlineEntries(chapters),
	}, nil
}

func continuationPlanMatchesCandidate(plan, candidate []domain.OutlineEntry) bool {
	if len(plan) != len(candidate) {
		return false
	}
	for i := range plan {
		planned := cloneOutlineEntry(plan[i])
		proposed := cloneOutlineEntry(candidate[i])
		if proposed.ID == "" {
			proposed.ID = planned.ID
		}
		if !reflect.DeepEqual(planned, proposed) {
			return false
		}
	}
	return true
}

func validateContinuationBaseline(base int, canonical []domain.OutlineEntry, layered []domain.VolumeOutline, layeredExisted bool, progress *domain.Progress) error {
	if len(canonical) != base {
		return fmt.Errorf("canonical outline has %d chapters; continuation baseline requires exactly %d", len(canonical), base)
	}
	for index, chapter := range canonical {
		if chapter.Chapter != index+1 {
			return fmt.Errorf("canonical chapter number must be %d, got %d", index+1, chapter.Chapter)
		}
	}
	if layeredExisted {
		flattened := domain.FlattenOutline(layered)
		if !reflect.DeepEqual(flattened, canonical) {
			return fmt.Errorf("canonical flat and layered outlines differ at continuation baseline")
		}
	}
	completed := make(map[int]bool, len(progress.CompletedChapters))
	for _, chapter := range progress.CompletedChapters {
		completed[chapter] = true
	}
	for chapter := 1; chapter <= base; chapter++ {
		if !completed[chapter] {
			return fmt.Errorf("imported baseline chapter %d is not completed", chapter)
		}
	}
	return nil
}

func mergeContinuationLayeredOutline(existing []domain.VolumeOutline, plan *domain.ContinuationPlan) ([]domain.VolumeOutline, error) {
	merged := cloneVolumeOutlines(existing)
	nextVolume := 1
	if len(merged) > 0 {
		nextVolume = merged[len(merged)-1].Index + 1
	}
	var additions []domain.VolumeOutline
	if plan.Outlines.Structure == domain.ContinuationStructureVolumes {
		additions = cloneVolumeOutlines(plan.Outlines.Volumes)
	} else {
		additions = []domain.VolumeOutline{{
			Index: nextVolume,
			Title: "续写",
			Theme: plan.Proposal.Direction,
			Arcs: []domain.ArcOutline{{
				Index:    1,
				Title:    "续写主线",
				Goal:     plan.Proposal.Summary,
				Chapters: cloneOutlineEntries(plan.Chapters),
			}},
		}}
	}
	for index := range additions {
		additions[index].Index = nextVolume + index
		if len(additions[index].Arcs) == 0 {
			return nil, fmt.Errorf("continuation volume %d has no arcs", index+1)
		}
	}
	return append(merged, additions...), nil
}

func continuationPlanAlreadyCommitted(canonical []domain.OutlineEntry, progress *domain.Progress, plan *domain.ContinuationPlan) bool {
	if plan == nil || progress == nil {
		return false
	}
	want := plan.BaseChapterCount + len(plan.Chapters)
	if len(canonical) != want || progress.TotalChapters != want {
		return false
	}
	return reflect.DeepEqual(canonical[plan.BaseChapterCount:], plan.Chapters)
}

func (s *Store) writeContinuationCommitUnlocked(outline []domain.OutlineEntry, layered []domain.VolumeOutline, layeredExisted bool, progress *domain.Progress, snapshot *domain.ContinuationSnapshot) error {
	if s.Outline.migration == nil {
		return fmt.Errorf("structure migration is required for continuation commit")
	}
	var payloads []migrationPayload
	var target structureIndex
	var err error
	if layeredExisted {
		payloads, err = layeredOutlineMigrationPayloads(layered)
		target = structureIndexFromLayered(layered)
	} else {
		payloads, err = outlineMigrationPayloads(outline, nil)
		target = structureIndexFromOutline(outline)
	}
	if err != nil {
		return err
	}
	progressPayload, err := jsonMigrationPayload("meta/progress.json", progress)
	if err != nil {
		return err
	}
	workflowPayload, err := jsonMigrationPayload(continuationWorkflowFile, snapshot.Workflow)
	if err != nil {
		return err
	}
	planPayload, err := jsonMigrationPayload(continuationPlanFile, snapshot.Plan)
	if err != nil {
		return err
	}
	var journal continuationCommitJournal
	if err := s.Continuation.io.ReadJSONUnlocked(continuationCommitJournalFile, &journal); err != nil {
		return err
	}
	journal.Stage = continuationJournalCommitted
	journalPayload, err := jsonMigrationPayload(continuationCommitJournalFile, journal)
	if err != nil {
		return err
	}
	payloads = append(payloads, progressPayload, workflowPayload, planPayload, journalPayload)
	legacySource := structureIndexFromOutline(journal.Outline)
	if journal.LayeredExisted {
		legacySource = structureIndexFromLayered(journal.Layered)
	}
	return s.Outline.migration.save("continuation_commit", legacySource, target, payloads)
}

func (s *Store) recoverContinuationCommitUnlocked() error {
	var journal continuationCommitJournal
	if err := s.Continuation.io.ReadJSONUnlocked(continuationCommitJournalFile, &journal); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if journal.Stage == continuationJournalCommitted {
		return s.Continuation.io.RemoveFileUnlocked(continuationCommitJournalFile)
	}
	if err := s.restoreContinuationCommitUnlocked(journal); err != nil {
		return err
	}
	return s.Continuation.io.RemoveFileUnlocked(continuationCommitJournalFile)
}

func (s *Store) rollbackContinuationCommitUnlocked(journal continuationCommitJournal, cause error) error {
	restoreErr := s.restoreContinuationCommitUnlocked(journal)
	cleanupErr := s.Continuation.io.RemoveFileUnlocked(continuationCommitJournalFile)
	return errors.Join(cause, restoreErr, cleanupErr)
}

func (s *Store) restoreContinuationCommitUnlocked(journal continuationCommitJournal) error {
	if s.Outline.migration == nil {
		return fmt.Errorf("structure migration is required for continuation recovery")
	}
	var payloads []migrationPayload
	var removePaths []string
	target := structureIndex{Version: structureSchemaVersion}
	if journal.LayeredExisted {
		var err error
		payloads, err = layeredOutlineMigrationPayloads(journal.Layered)
		if err != nil {
			return err
		}
		target = structureIndexFromLayered(journal.Layered)
	} else {
		removePaths = append(removePaths, "layered_outline.json", "layered_outline.md")
		if journal.OutlineExisted {
			var err error
			payloads, err = outlineMigrationPayloads(journal.Outline, nil)
			if err != nil {
				return err
			}
			target = structureIndexFromOutline(journal.Outline)
		}
	}
	if !journal.OutlineExisted {
		removePaths = append(removePaths, "outline.json", "outline.md")
	}
	if journal.Progress != nil {
		payload, err := jsonMigrationPayload("meta/progress.json", journal.Progress)
		if err != nil {
			return err
		}
		payloads = append(payloads, payload)
	}
	workflowPayload, err := jsonMigrationPayload(continuationWorkflowFile, journal.Workflow)
	if err != nil {
		return err
	}
	payloads = append(payloads, workflowPayload)
	if journal.Plan != nil {
		planPayload, err := jsonMigrationPayload(continuationPlanFile, journal.Plan)
		if err != nil {
			return err
		}
		payloads = append(payloads, planPayload)
	} else {
		removePaths = append(removePaths, continuationPlanFile)
	}
	return s.Outline.migration.saveWithRemovals("continuation_recovery", target, target, payloads, removePaths)
}

func loadOptionalOutlineUnlocked(io *IO) ([]domain.OutlineEntry, bool, error) {
	var outline []domain.OutlineEntry
	if err := io.ReadJSONUnlocked("outline.json", &outline); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return outline, true, nil
}

func loadOptionalLayeredOutlineUnlocked(io *IO) ([]domain.VolumeOutline, bool, error) {
	var volumes []domain.VolumeOutline
	if err := io.ReadJSONUnlocked("layered_outline.json", &volumes); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return volumes, true, nil
}

func cloneVolumeOutlines(volumes []domain.VolumeOutline) []domain.VolumeOutline {
	if len(volumes) == 0 {
		return nil
	}
	out := make([]domain.VolumeOutline, len(volumes))
	for index := range volumes {
		out[index] = cloneVolumeOutline(volumes[index])
	}
	return out
}

func cloneContinuationOutline(outline domain.ContinuationOutline) domain.ContinuationOutline {
	outline.Chapters = cloneOutlineEntries(outline.Chapters)
	outline.Volumes = cloneVolumeOutlines(outline.Volumes)
	return outline
}

func cloneContinuationPlan(plan *domain.ContinuationPlan) *domain.ContinuationPlan {
	if plan == nil {
		return nil
	}
	clone := *plan
	clone.Volumes = cloneVolumeOutlines(plan.Volumes)
	clone.Outlines = cloneContinuationOutline(plan.Outlines)
	clone.Chapters = cloneOutlineEntries(plan.Chapters)
	clone.Proposal.Notes = append([]string(nil), plan.Proposal.Notes...)
	return &clone
}

func cloneProgress(progress *domain.Progress) *domain.Progress {
	if progress == nil {
		return nil
	}
	clone := *progress
	clone.CompletedChapters = append([]int(nil), progress.CompletedChapters...)
	clone.CompletedScenes = append([]int(nil), progress.CompletedScenes...)
	clone.PendingRewrites = append([]int(nil), progress.PendingRewrites...)
	clone.PendingArcPost = append([]domain.ArcPostprocessTarget(nil), progress.PendingArcPost...)
	clone.StrandHistory = append([]string(nil), progress.StrandHistory...)
	clone.HookHistory = append([]string(nil), progress.HookHistory...)
	if progress.ChapterWordCounts != nil {
		clone.ChapterWordCounts = make(map[int]int, len(progress.ChapterWordCounts))
		for chapter, count := range progress.ChapterWordCounts {
			clone.ChapterWordCounts[chapter] = count
		}
	}
	return &clone
}
