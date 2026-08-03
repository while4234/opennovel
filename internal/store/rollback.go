package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const rollbackLogFile = "meta/rollback_log.jsonl"

type rollbackState struct {
	progress       *domain.Progress
	review         *domain.PlanningReview
	plan           *domain.AdaptationPlan
	proposal       *domain.AdaptationPlan
	volumeReview   *domain.AdaptationVolumeReview
	runtime        *domain.AdaptationProposalRuntime
	planningFlow   *domain.AdaptationPlanningWorkflow
	sourceManifest *domain.AdaptationSourceManifest
	flatOutline    []domain.OutlineEntry
	layeredOutline []domain.VolumeOutline
	premise        string
	meta           *domain.RunMeta
}

func (s *Store) RollbackPreview() (domain.RollbackPreview, error) {
	state, err := s.inspectRollbackState()
	if err != nil {
		return domain.RollbackPreview{}, err
	}
	return domain.RollbackPreviewWithHash(state.preview()), nil
}

func (s *Store) Rollback(req domain.RollbackRequest) (domain.RollbackResult, error) {
	if !req.Confirm {
		return domain.RollbackResult{}, fmt.Errorf("rollback confirmation is required")
	}
	var result domain.RollbackResult
	err := s.Revisions.withLegacyMigrationMutation("roll back project structure and adaptation state", s.Outline.migration, func() error {
		s.crossMu.Lock()
		defer s.crossMu.Unlock()
		if s.Foundation != nil {
			// Lock order is revision migration -> crossMu -> foundation projectMu
			// -> section I/O. Foundation saves acquire only projectMu -> I/O.
			if err := s.Foundation.lifecycle.beginRollback(); err != nil {
				return err
			}
			defer s.Foundation.lifecycle.endRollback()
			s.Foundation.runLifecycleHook(foundationLifecycleRollbackStarted)
			s.Foundation.projectMu.Lock()
			defer s.Foundation.projectMu.Unlock()
			s.Foundation.runLifecycleHook(foundationLifecycleRollbackLocked)
			if err := s.Foundation.recoverUnlocked(); err != nil {
				return fmt.Errorf("recover story foundation before rollback: %w", err)
			}
		}

		state, err := s.inspectRollbackStateForMutation()
		if err != nil {
			return err
		}
		preview := domain.RollbackPreviewWithHash(state.preview())
		if !preview.CanRollback {
			result = domain.RollbackResult{Preview: preview}
			return fmt.Errorf("project cannot roll back: %s", preview.Reason)
		}
		if strings.TrimSpace(req.PreviewHash) != "" && req.PreviewHash != preview.PreviewHash {
			result = domain.RollbackResult{Preview: preview}
			return fmt.Errorf("rollback preview expired; refresh and confirm again")
		}

		deleted, err := s.migrateRollbackStructure(preview.TargetStage, state)
		if err != nil {
			result = domain.RollbackResult{Preview: preview, DeletedPaths: deleted}
			return err
		}
		if err := s.Checkpoints.Reset(); err != nil {
			result = domain.RollbackResult{Preview: preview, DeletedPaths: deleted}
			return fmt.Errorf("reset checkpoint cache: %w", err)
		}
		if err := s.Init(); err != nil {
			result = domain.RollbackResult{Preview: preview, DeletedPaths: deleted}
			return fmt.Errorf("recreate project directories: %w", err)
		}
		if err := s.appendRollbackLog(preview, deleted); err != nil {
			result = domain.RollbackResult{Preview: preview, DeletedPaths: deleted}
			return fmt.Errorf("append rollback log: %w", err)
		}
		result = domain.RollbackResult{Preview: preview, DeletedPaths: deleted}
		return nil
	})
	return result, err
}

func (s *Store) inspectRollbackState() (rollbackState, error) {
	return s.inspectRollbackStateWithPremise(s.Outline.LoadPremise)
}

func (s *Store) inspectRollbackStateForMutation() (rollbackState, error) {
	if s.Foundation == nil {
		return s.inspectRollbackState()
	}
	return s.inspectRollbackStateWithPremise(func() (string, error) {
		foundation, err := s.Foundation.loadCurrentUnlocked(true)
		return foundation.Premise, err
	})
}

func (s *Store) inspectRollbackStateWithPremise(loadPremise func() (string, error)) (rollbackState, error) {
	var state rollbackState
	var err error
	if state.progress, err = s.Progress.Load(); err != nil {
		return state, fmt.Errorf("load progress: %w", err)
	}
	if state.review, err = s.RunMeta.PlanningReview(); err != nil {
		return state, fmt.Errorf("load planning review: %w", err)
	}
	if state.meta, err = s.RunMeta.Load(); err != nil {
		return state, fmt.Errorf("load run meta: %w", err)
	}
	if state.plan, err = s.Adaptation.LoadPlan(); err != nil {
		return state, fmt.Errorf("load adaptation plan: %w", err)
	}
	if state.proposal, err = s.Adaptation.LoadProposal(); err != nil {
		return state, fmt.Errorf("load adaptation proposal: %w", err)
	}
	if state.volumeReview, err = s.Adaptation.LoadVolumeReview(); err != nil {
		return state, fmt.Errorf("load adaptation volume review: %w", err)
	}
	if state.runtime, err = s.Adaptation.LoadProposalRuntime(); err != nil {
		return state, fmt.Errorf("load adaptation proposal runtime: %w", err)
	}
	if state.planningFlow, err = s.Adaptation.LoadPlanningWorkflow(); err != nil {
		return state, fmt.Errorf("load adaptation planning workflow: %w", err)
	}
	if state.sourceManifest, err = s.Adaptation.LoadSourceManifest(); err != nil {
		return state, fmt.Errorf("load adaptation source manifest: %w", err)
	}
	if state.flatOutline, err = s.Outline.LoadOutline(); err != nil {
		return state, fmt.Errorf("load outline: %w", err)
	}
	if state.layeredOutline, err = s.Outline.LoadLayeredOutline(); err != nil {
		return state, fmt.Errorf("load layered outline: %w", err)
	}
	state.premise, err = loadPremise()
	if err != nil {
		return state, fmt.Errorf("load premise: %w", err)
	}
	return state, nil
}

func (state rollbackState) preview() domain.RollbackPreview {
	if state.hasAdaptationState() {
		return state.adaptationPreview()
	}
	return state.normalPreview()
}

func (state rollbackState) hasAdaptationState() bool {
	return state.plan != nil ||
		state.proposal != nil ||
		state.volumeReview != nil ||
		state.runtime != nil ||
		state.sourceManifest != nil
}

func (state rollbackState) adaptationPreview() domain.RollbackPreview {
	switch {
	case state.plan != nil:
		return state.readyPreview("adaptation", "writing", domain.RollbackStageProposal,
			"改编提案完成待审核",
			adaptationWritingDeletePaths(),
			[]string{"meta/adaptation/proposal.json", "meta/adaptation/source_*", "uploads/"})
	case state.proposal != nil || state.runtime != nil:
		if state.volumeReview != nil || adaptationProposalHasVolumes(state.proposal) {
			return state.readyPreview("adaptation", "chapter_outline", domain.RollbackStageVolumeOutline,
				"分卷骨架规划完成，待审核",
				[]string{adaptationProposalFile, adaptationProposalRuntimeFile},
				[]string{adaptationVolumeReviewFile, "meta/adaptation/source_*", "uploads/"})
		}
		return state.readyPreview("adaptation", "proposal", domain.RollbackStageDraft,
			"共创草稿已完成，可生成改编提案",
			adaptationGeneratedDeletePaths(),
			[]string{"meta/adaptation/source_*", "meta/adaptation/cocreate_*", "uploads/"})
	case state.volumeReview != nil:
		return state.readyPreview("adaptation", "volume_outline", domain.RollbackStageDraft,
			"共创草稿已完成，可生成改编提案",
			adaptationGeneratedDeletePaths(),
			[]string{"meta/adaptation/source_*", "meta/adaptation/cocreate_*", "uploads/"})
	case state.sourceManifest != nil:
		return state.readyPreview("adaptation", "draft", domain.RollbackStageBlank,
			"空白项目",
			blankDeletePaths(),
			[]string{"project manifest", "style/config", "uploads/"})
	default:
		return state.blockedPreview("adaptation", "blank", "当前项目没有可回退的改编阶段")
	}
}

func (state rollbackState) normalPreview() domain.RollbackPreview {
	if state.hasWritingProgress() {
		if state.hasDetailedOutline() {
			return state.readyPreview("normal", "writing", domain.RollbackStageChapterOutline,
				"详细章节提纲完成待审核",
				writingDeletePaths(),
				[]string{"premise.md", "outline.json", "layered_outline.json", "characters.json", "world_rules.json"})
		}
		return state.blockedPreview("normal", "writing", "缺少可回退到的章节提纲")
	}
	if state.hasDetailedOutline() {
		if len(state.layeredOutline) > 0 {
			return state.readyPreview("normal", "chapter_outline", domain.RollbackStageVolumeOutline,
				"分卷提纲完成待审核",
				[]string{"outline.json", "outline.md", "layered_outline.json 中的章节细纲"},
				[]string{"layered_outline.json", "premise.md", "characters.json", "world_rules.json"})
		}
		return state.readyPreview("normal", "chapter_outline", domain.RollbackStageDraft,
			"共创 draft",
			foundationDeletePaths(),
			[]string{"meta/run.json 中的 planning_review"})
	}
	if len(state.layeredOutline) > 0 || state.hasFoundation() {
		return state.readyPreview("normal", "volume_outline", domain.RollbackStageDraft,
			"共创 draft",
			foundationDeletePaths(),
			[]string{"meta/run.json 中的 planning_review"})
	}
	if state.review != nil && strings.TrimSpace(state.review.Brief) != "" {
		return state.readyPreview("normal", "draft", domain.RollbackStageBlank,
			"空白项目",
			blankDeletePaths(),
			[]string{"project manifest", "style/config", "uploads/"})
	}
	return state.blockedPreview("normal", "blank", "当前项目已经是空白状态")
}

func (state rollbackState) readyPreview(mode, current string, target domain.RollbackStage, label string, deletePaths, preservePaths []string) domain.RollbackPreview {
	return domain.RollbackPreview{
		CanRollback:    true,
		Mode:           mode,
		CurrentStage:   current,
		TargetStage:    target,
		TargetLabel:    label,
		Warning:        "回退会删除目标阶段之后产生的项目文件，此操作不可撤销。",
		DeletePaths:    append([]string(nil), deletePaths...),
		PreservePaths:  append([]string(nil), preservePaths...),
		StateSignature: state.signature(),
	}
}

func (state rollbackState) blockedPreview(mode, current, reason string) domain.RollbackPreview {
	return domain.RollbackPreview{
		CanRollback:    false,
		Mode:           mode,
		CurrentStage:   current,
		Reason:         reason,
		StateSignature: state.signature(),
	}
}

func (state rollbackState) hasWritingProgress() bool {
	if state.progress == nil {
		return false
	}
	return state.progress.Phase == domain.PhaseWriting ||
		state.progress.Phase == domain.PhaseComplete ||
		state.progress.InProgressChapter > 0 ||
		len(state.progress.CompletedChapters) > 0
}

func (state rollbackState) hasDetailedOutline() bool {
	return len(state.flatOutline) > 0 || layeredOutlineHasExpandedArcs(state.layeredOutline)
}

func (state rollbackState) hasFoundation() bool {
	return strings.TrimSpace(state.premise) != "" ||
		len(state.flatOutline) > 0 ||
		len(state.layeredOutline) > 0
}

func (state rollbackState) signature() string {
	progress := "nil"
	if state.progress != nil {
		progress = fmt.Sprintf("%s:%s:%d:%d:%d:%d",
			state.progress.Phase,
			state.progress.Flow,
			state.progress.CurrentChapter,
			state.progress.InProgressChapter,
			state.progress.TotalChapters,
			len(state.progress.CompletedChapters),
		)
	}
	review := "nil"
	if state.review != nil {
		review = fmt.Sprintf("%s:%s:%s:%d", state.review.Status, state.review.Kind, state.review.UpdatedAt, len(state.review.Brief))
	}
	return strings.Join([]string{
		"progress=" + progress,
		"review=" + review,
		fmt.Sprintf("plan=%t:%d", state.plan != nil, adaptationPlanChapterCount(state.plan)),
		fmt.Sprintf("proposal=%t:%d", state.proposal != nil, adaptationPlanChapterCount(state.proposal)),
		fmt.Sprintf("volume=%t:%d", state.volumeReview != nil, adaptationVolumeTargetCount(state.volumeReview)),
		fmt.Sprintf("runtime=%t:%d:%d", state.runtime != nil, adaptationRuntimeTargetCount(state.runtime), adaptationRuntimeCompletedCount(state.runtime)),
		fmt.Sprintf("source=%t", state.sourceManifest != nil),
		fmt.Sprintf("outline=%d:%d:%t", len(state.flatOutline), len(state.layeredOutline), layeredOutlineHasExpandedArcs(state.layeredOutline)),
	}, ";")
}

func (s *Store) executeRollback(target domain.RollbackStage, state rollbackState) ([]string, error) {
	switch target {
	case domain.RollbackStageProposal:
		return s.rollbackToAdaptationProposal(state)
	case domain.RollbackStageChapterOutline:
		return s.rollbackToChapterOutline(state)
	case domain.RollbackStageVolumeOutline:
		return s.rollbackToVolumeOutline(state)
	case domain.RollbackStageDraft:
		return s.rollbackToDraft(state)
	case domain.RollbackStageBlank:
		return s.rollbackToBlank()
	default:
		return nil, fmt.Errorf("unsupported rollback target %q", target)
	}
}

func (s *Store) migrateRollbackStructure(targetStage domain.RollbackStage, state rollbackState) ([]string, error) {
	if s.Outline.migration == nil {
		return nil, fmt.Errorf("structure migration is required for rollback")
	}
	source := s.Outline.identity.sourceIndex(state.flatOutline, state.layeredOutline)
	if state.plan != nil {
		source = structureIndexFromAdaptation(state.plan)
	}
	target := source
	removePaths := append([]string(nil), writingDeletePaths()...)
	removePaths = append(removePaths, "meta/continuation", structureRequestReceiptDir)
	var payloads []migrationPayload
	var err error
	switch targetStage {
	case domain.RollbackStageProposal:
		proposal := state.proposal
		if proposal == nil {
			proposal = state.plan
		}
		if proposal != nil {
			target = structureIndexFromAdaptation(proposal)
			prepared := *proposal
			s.Adaptation.normalizeAdaptationPlan(&prepared)
			prepared.Status = domain.AdaptationPlanStatusProposal
			if err := s.Adaptation.identity.prepareAdaptationPlanForSave(&prepared, state.proposal); err != nil {
				return nil, err
			}
			payloads, err = appendJSONMigrationPayload(payloads, adaptationProposalFile, prepared)
			if err != nil {
				return nil, err
			}
			payloads, err = appendRollbackAdaptationWorkflow(payloads, state, domain.AdaptationPlanningStageProposalReviewPending)
			if err != nil {
				return nil, err
			}
			payloads, err = appendJSONMigrationPayload(payloads, "meta/progress.json", rollbackPlanningProgress(domain.PhaseOutline, len(prepared.Chapters), false, state))
			if err != nil {
				return nil, err
			}
		}
		removePaths = append(removePaths, adaptationConfirmedDeletePaths()...)
		removePaths = append(removePaths, adaptationProposalRuntimeFile, adaptationVolumeReviewFile)
		removePaths = append(removePaths, foundationDeletePaths()...)
	case domain.RollbackStageChapterOutline:
		if len(state.layeredOutline) > 0 {
			payloads, err = layeredOutlineMigrationPayloads(state.layeredOutline)
			target = structureIndexFromLayered(state.layeredOutline)
		} else {
			payloads, err = outlineMigrationPayloads(state.flatOutline, nil)
			target = structureIndexFromOutline(state.flatOutline)
		}
		if err == nil {
			total := len(state.flatOutline)
			layered := len(state.layeredOutline) > 0
			if total == 0 && layered {
				total = domain.TotalChapters(state.layeredOutline)
			}
			payloads, err = appendJSONMigrationPayload(payloads, "meta/progress.json", rollbackPlanningProgress(domain.PhaseOutline, total, layered, state))
		}
		if err == nil {
			payloads, err = appendRollbackRunMeta(payloads, domain.PlanningReviewKindChapterOutline, state, false)
		}
	case domain.RollbackStageVolumeOutline:
		if state.hasAdaptationState() {
			review := state.volumeReview
			if review == nil {
				review = adaptationVolumeReviewFromProposal(state.proposal, state.sourceManifest)
			}
			target = structureIndexFromAdaptationVolumeReview(review)
			if review != nil {
				prepared := *review
				prepared.Status = domain.AdaptationPlanStatusVolumeReview
				if err := s.Adaptation.identity.prepareAdaptationVolumeReviewForSave(&prepared, state.volumeReview); err != nil {
					return nil, err
				}
				payloads, err = appendJSONMigrationPayload(payloads, adaptationVolumeReviewFile, prepared)
				if err == nil {
					payloads, err = appendRollbackAdaptationWorkflow(payloads, state, domain.AdaptationPlanningStageVolumeReviewPending)
				}
				if err == nil {
					payloads, err = appendJSONMigrationPayload(payloads, "meta/progress.json", rollbackPlanningProgress(domain.PhaseOutline, prepared.TargetChapterCount, true, state))
				}
			}
			removePaths = append(removePaths, adaptationPlanFile, adaptationProposalFile, adaptationProposalRuntimeFile, adaptationCheckDir)
		} else {
			collapsed := collapseLayeredOutline(state.layeredOutline)
			payloads, err = layeredOutlineMigrationPayloads(collapsed)
			target = structureIndexFromLayered(collapsed)
			removePaths = append(removePaths, "outline.json", "outline.md")
			if err == nil {
				payloads, err = appendJSONMigrationPayload(payloads, "meta/progress.json", rollbackPlanningProgress(domain.PhaseOutline, domain.TotalChapters(collapsed), true, state))
			}
			if err == nil {
				payloads, err = appendRollbackRunMeta(payloads, domain.PlanningReviewKindVolumeSplit, state, false)
			}
		}
	case domain.RollbackStageDraft:
		target = structureIndex{Version: structureSchemaVersion}
		removePaths = append(removePaths, adaptationGeneratedDeletePaths()...)
		removePaths = append(removePaths, foundationDeletePaths()...)
		removePaths = append(removePaths, "meta/progress.json")
		payloads, err = appendRollbackRunMeta(payloads, domain.PlanningReviewKindBlueprint, state, state.hasAdaptationState())
	case domain.RollbackStageBlank:
		target = structureIndex{Version: structureSchemaVersion}
		removePaths = append(removePaths, blankDeletePaths()...)
		payloads, err = appendRollbackRunMeta(payloads, "", state, true)
		if err == nil {
			var meta domain.RunMeta
			if state.meta != nil {
				meta = *state.meta
			}
			meta.PlanningReview = nil
			meta.WordBudget = nil
			meta.PendingSteer = ""
			payloads = removeMigrationPayload(payloads, "meta/run.json")
			payloads, err = appendJSONMigrationPayload(payloads, "meta/run.json", meta)
		}
	default:
		return nil, fmt.Errorf("unsupported rollback target %q", targetStage)
	}
	if err != nil {
		return nil, err
	}
	removePaths = removePayloadPaths(sortedUnique(removePaths), payloads)
	deleted := make([]string, 0, len(removePaths))
	for _, rel := range removePaths {
		if s.pathExists(rel) {
			deleted = append(deleted, filepath.ToSlash(rel))
		}
	}
	if err := s.Outline.migration.saveWithRemovals("rollback_"+string(targetStage), source, target, payloads, removePaths); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func appendJSONMigrationPayload(payloads []migrationPayload, rel string, value any) ([]migrationPayload, error) {
	payload, err := jsonMigrationPayload(rel, value)
	if err != nil {
		return payloads, err
	}
	return append(payloads, payload), nil
}

func appendRollbackAdaptationWorkflow(payloads []migrationPayload, state rollbackState, stage domain.AdaptationPlanningStage) ([]migrationPayload, error) {
	revision := 1
	if state.planningFlow != nil {
		revision = state.planningFlow.Revision + 1
	}
	workflow := domain.AdaptationPlanningWorkflow{
		Version:   domain.AdaptationPlanningWorkflowVersion,
		Stage:     stage,
		Revision:  revision,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return appendJSONMigrationPayload(payloads, adaptationPlanningWorkflowFile, workflow)
}

func appendRollbackRunMeta(payloads []migrationPayload, reviewKind string, state rollbackState, clear bool) ([]migrationPayload, error) {
	var meta domain.RunMeta
	if state.meta != nil {
		meta = *state.meta
	}
	if clear {
		meta.PlanningReview = nil
	} else {
		review, err := rollbackPlanningReview(reviewKind, state)
		if err != nil {
			return payloads, err
		}
		meta.PlanningReview = review
	}
	return appendJSONMigrationPayload(payloads, "meta/run.json", meta)
}

func removeMigrationPayload(payloads []migrationPayload, rel string) []migrationPayload {
	result := payloads[:0]
	for _, payload := range payloads {
		if filepath.ToSlash(payload.Rel) != filepath.ToSlash(rel) {
			result = append(result, payload)
		}
	}
	return result
}

func removePayloadPaths(paths []string, payloads []migrationPayload) []string {
	installed := make(map[string]struct{}, len(payloads))
	for _, payload := range payloads {
		installed[filepath.ToSlash(filepath.Clean(filepath.FromSlash(payload.Rel)))] = struct{}{}
	}
	result := paths[:0]
	for _, rel := range paths {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
		if _, exists := installed[clean]; !exists {
			result = append(result, rel)
		}
	}
	return result
}

func rollbackPlanningProgress(phase domain.Phase, total int, layered bool, state rollbackState) *domain.Progress {
	if total < 0 {
		total = 0
	}
	currentChapter := 0
	if total > 0 {
		currentChapter = 1
	}
	return &domain.Progress{
		NovelName:      rollbackNovelName(state),
		Phase:          phase,
		CurrentChapter: currentChapter,
		TotalChapters:  total,
		Layered:        layered,
	}
}

func structureIndexFromAdaptationVolumeReview(review *domain.AdaptationVolumeReview) structureIndex {
	index := structureIndex{Version: structureSchemaVersion}
	if review == nil {
		return index
	}
	for i, volume := range review.Volumes {
		index.Volumes = append(index.Volumes, structureVolumeRef{ID: volume.ID, Number: i + 1})
	}
	return index
}

func (s *Store) rollbackToAdaptationProposal(state rollbackState) ([]string, error) {
	source := state.proposal
	if source == nil {
		source = state.plan
	}
	if source == nil || len(source.Chapters) == 0 {
		return nil, fmt.Errorf("adaptation proposal cannot be restored")
	}
	proposal := *source
	proposal.Status = domain.AdaptationPlanStatusProposal
	if err := s.Adaptation.saveProposal(proposal); err != nil {
		return nil, fmt.Errorf("restore adaptation proposal: %w", err)
	}
	deleted, err := s.removeWritingArtifacts()
	if err != nil {
		return deleted, err
	}
	if removed, err := s.removePaths(adaptationConfirmedDeletePaths()); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	if removed, err := s.removePaths(foundationDeletePaths()); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	if err := s.savePlanningProgress(domain.PhaseOutline, len(proposal.Chapters), false, state); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func (s *Store) rollbackToChapterOutline(state rollbackState) ([]string, error) {
	if !state.hasDetailedOutline() {
		return nil, fmt.Errorf("chapter outline is missing")
	}
	deleted, err := s.removeWritingArtifacts()
	if err != nil {
		return deleted, err
	}
	if err := s.ensureNormalPlanningReview(domain.PlanningReviewKindChapterOutline, state); err != nil {
		return deleted, err
	}
	total := len(state.flatOutline)
	layered := len(state.layeredOutline) > 0
	if total == 0 && layered {
		total = domain.TotalChapters(state.layeredOutline)
	}
	if err := s.savePlanningProgress(domain.PhaseOutline, total, layered, state); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func (s *Store) rollbackToVolumeOutline(state rollbackState) ([]string, error) {
	if state.volumeReview != nil || state.proposal != nil || state.runtime != nil || state.plan != nil {
		review := state.volumeReview
		if review == nil {
			review = adaptationVolumeReviewFromProposal(state.proposal, state.sourceManifest)
		}
		if review == nil {
			return nil, fmt.Errorf("adaptation volume outline is missing")
		}
		var deleted []string
		for _, rel := range []string{adaptationProposalFile, adaptationProposalRuntimeFile} {
			if s.pathExists(rel) {
				deleted = append(deleted, rel)
			}
		}
		if err := s.Adaptation.restoreVolumeReviewForRollback(*review); err != nil {
			return deleted, fmt.Errorf("restore adaptation volume review: %w", err)
		}
		removed, err := s.removePaths([]string{adaptationPlanFile})
		if err != nil {
			return deleted, err
		}
		deleted = append(deleted, removed...)
		if removed, err := s.removePaths([]string{adaptationCheckDir}); err != nil {
			return deleted, err
		} else {
			deleted = append(deleted, removed...)
		}
		if err := s.savePlanningProgress(domain.PhaseOutline, review.TargetChapterCount, true, state); err != nil {
			return deleted, err
		}
		return deleted, nil
	}
	if len(state.layeredOutline) == 0 {
		return nil, fmt.Errorf("volume outline is missing")
	}
	collapsed := collapseLayeredOutline(state.layeredOutline)
	if err := s.Outline.saveLayeredOutline(collapsed); err != nil {
		return nil, fmt.Errorf("collapse layered outline: %w", err)
	}
	deleted, err := s.removeWritingArtifacts()
	if err != nil {
		return deleted, err
	}
	if removed, err := s.removePaths([]string{"outline.json", "outline.md"}); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	if err := s.ensureNormalPlanningReview(domain.PlanningReviewKindVolumeSplit, state); err != nil {
		return deleted, err
	}
	if err := s.savePlanningProgress(domain.PhaseOutline, domain.TotalChapters(collapsed), true, state); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func (s *Store) rollbackToDraft(state rollbackState) ([]string, error) {
	deleted, err := s.removeWritingArtifacts()
	if err != nil {
		return deleted, err
	}
	if removed, err := s.removePaths(adaptationGeneratedDeletePaths()); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	if removed, err := s.removePaths(foundationDeletePaths()); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	if removed, err := s.removePaths([]string{"meta/progress.json"}); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	if state.hasAdaptationState() {
		if err := s.RunMeta.ClearPlanningReview(); err != nil {
			return deleted, fmt.Errorf("clear planning review: %w", err)
		}
	} else {
		if err := s.ensureNormalPlanningReview(domain.PlanningReviewKindBlueprint, state); err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func (s *Store) rollbackToBlank() ([]string, error) {
	deleted, err := s.removeWritingArtifacts()
	if err != nil {
		return deleted, err
	}
	if removed, err := s.removePaths(blankDeletePaths()); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	if err := s.RunMeta.ClearPlanningReview(); err != nil {
		return deleted, fmt.Errorf("clear planning review: %w", err)
	}
	if err := s.RunMeta.SetWordBudget(nil); err != nil {
		return deleted, fmt.Errorf("clear word budget: %w", err)
	}
	if err := s.RunMeta.ClearPendingSteer(); err != nil {
		return deleted, fmt.Errorf("clear pending steer: %w", err)
	}
	return deleted, nil
}

func (s *Store) removeWritingArtifacts() ([]string, error) {
	var deleted []string
	if s.pathExists(checkpointsFile) {
		deleted = append(deleted, checkpointsFile)
	}
	if err := s.Checkpoints.Reset(); err != nil {
		return deleted, fmt.Errorf("reset checkpoints: %w", err)
	}
	if s.pathExists("meta/runtime") {
		deleted = append(deleted, "meta/runtime/")
	}
	if err := s.Runtime.Reset(); err != nil {
		return deleted, fmt.Errorf("reset runtime: %w", err)
	}
	if removed, err := s.removePaths(writingDeletePathsWithoutRuntime()); err != nil {
		return deleted, err
	} else {
		deleted = append(deleted, removed...)
	}
	return deleted, nil
}

func (s *Store) removePaths(paths []string) ([]string, error) {
	var deleted []string
	io := newIO(s.dir)
	for _, rel := range paths {
		cleanRel := strings.TrimSpace(rel)
		if cleanRel == "" || strings.Contains(cleanRel, "*") || strings.Contains(cleanRel, " 中的") {
			continue
		}
		existed, err := io.RemoveAllRel(cleanRel)
		if err != nil {
			return deleted, fmt.Errorf("remove %s: %w", cleanRel, err)
		}
		if existed {
			deleted = append(deleted, filepath.ToSlash(cleanRel))
		}
	}
	return deleted, nil
}

func (s *Store) savePlanningProgress(phase domain.Phase, total int, layered bool, state rollbackState) error {
	return s.Progress.saveOwned(rollbackPlanningProgress(phase, total, layered, state))
}

func (s *Store) ensureNormalPlanningReview(kind string, state rollbackState) error {
	review, err := rollbackPlanningReview(kind, state)
	if err != nil {
		return err
	}
	return s.RunMeta.setPlanningReviewAuthoritative(review)
}

func rollbackPlanningReview(kind string, state rollbackState) (*domain.PlanningReview, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	review := &domain.PlanningReview{
		Status:      domain.PlanningReviewStatusPending,
		Kind:        kind,
		Brief:       fallbackPlanningBrief(state),
		StartPrompt: fallbackPlanningStartPrompt(state),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if state.review != nil {
		cp := *state.review
		review = &cp
		review.Status = domain.PlanningReviewStatusPending
		review.Kind = kind
		if strings.TrimSpace(review.Brief) == "" {
			review.Brief = fallbackPlanningBrief(state)
		}
		if review.CreatedAt == "" {
			review.CreatedAt = now
		}
		review.UpdatedAt = now
	} else if state.meta != nil && state.meta.WordBudget != nil {
		review.TargetTotalWords = state.meta.WordBudget.TargetTotalWords
	}
	if kind == domain.PlanningReviewKindBlueprint {
		clearFoundationReviewBinding(review)
	}
	if strings.TrimSpace(review.Brief) == "" && kind == domain.PlanningReviewKindBlueprint {
		return nil, fmt.Errorf("cannot restore draft without a planning brief")
	}
	return review, nil
}

func (s *Store) appendRollbackLog(preview domain.RollbackPreview, deleted []string) error {
	entry := struct {
		Time         string               `json:"time"`
		Mode         string               `json:"mode,omitempty"`
		TargetStage  domain.RollbackStage `json:"target_stage"`
		TargetLabel  string               `json:"target_label,omitempty"`
		DeletedPaths []string             `json:"deleted_paths,omitempty"`
	}{
		Time:         time.Now().UTC().Format(time.RFC3339),
		Mode:         preview.Mode,
		TargetStage:  preview.TargetStage,
		TargetLabel:  preview.TargetLabel,
		DeletedPaths: append([]string(nil), deleted...),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return newIO(s.dir).AppendLine(rollbackLogFile, data)
}

func (s *Store) pathExists(rel string) bool {
	_, err := os.Lstat(filepath.Join(s.dir, filepath.FromSlash(rel)))
	return err == nil
}

func writingDeletePaths() []string {
	return append([]string{checkpointsFile, "meta/runtime/"}, writingDeletePathsWithoutRuntime()...)
}

func writingDeletePathsWithoutRuntime() []string {
	return []string{
		"chapters",
		"drafts",
		"summaries",
		"reviews",
		structureRootDir + "/chapters",
		structureRootDir + "/volumes",
		structureRootDir + "/arcs",
		structureFactsFile,
		adaptationCheckDir,
		"timeline.json",
		"timeline.md",
		"foreshadow_ledger.json",
		"foreshadow_ledger.md",
		"relationship_state.json",
		"relationship_state.md",
		"meta/state_changes.json",
		"meta/cast_ledger.json",
		"meta/last_commit.json",
		"meta/pending_commit.json",
		"meta/last_review.json",
		"meta/outline_duplicate_scan.json",
		"meta/outline_repair_finalization.json",
		"meta/sessions/coordinator.jsonl",
		"meta/sessions/agents",
	}
}

func adaptationWritingDeletePaths() []string {
	paths := writingDeletePaths()
	paths = append(paths, adaptationPlanFile)
	paths = append(paths, foundationDeletePaths()...)
	return paths
}

func adaptationConfirmedDeletePaths() []string {
	return []string{adaptationPlanFile}
}

func adaptationGeneratedDeletePaths() []string {
	return []string{
		adaptationProposalFile,
		adaptationVolumeReviewFile,
		adaptationProposalRuntimeFile,
		adaptationPlanFile,
		adaptationPlanningWorkflowFile,
		adaptationCheckDir,
	}
}

func foundationDeletePaths() []string {
	return []string{
		foundationCanonicalFile,
		"premise.md",
		"outline.json",
		"outline.md",
		"layered_outline.json",
		"layered_outline.md",
		"characters.json",
		"characters.md",
		"world_rules.json",
		"world_rules.md",
		"planned_relationships.json",
		"planned_relationships.md",
		foundationRootDir,
		"meta/compass.json",
		"meta/snapshots",
	}
}

func blankDeletePaths() []string {
	paths := append([]string{}, foundationDeletePaths()...)
	paths = append(paths,
		"meta/progress.json",
		"meta/user_rules.json",
		"meta/style_rules.json",
		adaptationRootDir,
	)
	paths = append(paths, adaptationGeneratedDeletePaths()...)
	return paths
}

func adaptationRuntimeHasSkeleton(runtime *domain.AdaptationProposalRuntime) bool {
	return runtime != nil && (runtime.Skeleton != nil || len(runtime.SkeletonBatches) > 0)
}

func adaptationRuntimeTargetCount(runtime *domain.AdaptationProposalRuntime) int {
	if runtime == nil {
		return 0
	}
	if runtime.TargetChapterCount > 0 {
		return runtime.TargetChapterCount
	}
	if runtime.Skeleton != nil {
		return runtime.Skeleton.TargetChapterCount
	}
	return 0
}

func adaptationRuntimeCompletedCount(runtime *domain.AdaptationProposalRuntime) int {
	if runtime == nil {
		return 0
	}
	return len(runtime.CompletedBatches)
}

func adaptationPlanChapterCount(plan *domain.AdaptationPlan) int {
	if plan == nil {
		return 0
	}
	return len(plan.Chapters)
}

func adaptationVolumeTargetCount(review *domain.AdaptationVolumeReview) int {
	if review == nil {
		return 0
	}
	return review.TargetChapterCount
}

func adaptationProposalHasVolumes(proposal *domain.AdaptationPlan) bool {
	return proposal != nil && len(proposal.Volumes) > 0
}

func adaptationVolumeReviewFromProposal(proposal *domain.AdaptationPlan, manifest *domain.AdaptationSourceManifest) *domain.AdaptationVolumeReview {
	if !adaptationProposalHasVolumes(proposal) {
		return nil
	}
	review := &domain.AdaptationVolumeReview{
		Status:             domain.AdaptationPlanStatusVolumeReview,
		UpdatedAt:          time.Now().UTC().Format(time.RFC3339),
		Brief:              proposal.Brief,
		Granularity:        proposal.Granularity,
		RewritePolicy:      proposal.RewritePolicy,
		WordTolerance:      proposal.WordTolerance,
		TargetChapterCount: len(proposal.Chapters),
		MainlineRules:      append([]string(nil), proposal.MainlineRules...),
		RelationshipGoals:  append([]string(nil), proposal.RelationshipGoals...),
		Volumes:            append([]domain.AdaptationVolumePlan(nil), proposal.Volumes...),
		Planner:            proposal.Planner,
	}
	if manifest != nil {
		review.SourcePath = manifest.SourcePath
		review.SourceChapterCount = manifest.ChapterCount
	}
	return review
}

func layeredOutlineHasExpandedArcs(volumes []domain.VolumeOutline) bool {
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			if len(arc.Chapters) > 0 {
				return true
			}
		}
	}
	return false
}

func collapseLayeredOutline(volumes []domain.VolumeOutline) []domain.VolumeOutline {
	out := append([]domain.VolumeOutline(nil), volumes...)
	for vIdx := range out {
		out[vIdx].Arcs = append([]domain.ArcOutline(nil), out[vIdx].Arcs...)
		for aIdx := range out[vIdx].Arcs {
			arc := &out[vIdx].Arcs[aIdx]
			if len(arc.Chapters) > 0 && arc.EstimatedChapters <= 0 {
				arc.EstimatedChapters = len(arc.Chapters)
			}
			arc.Chapters = nil
		}
	}
	return out
}

func rollbackNovelName(state rollbackState) string {
	if state.progress != nil && strings.TrimSpace(state.progress.NovelName) != "" {
		return strings.TrimSpace(state.progress.NovelName)
	}
	return domain.ExtractNovelNameFromPremise(state.premise)
}

func fallbackPlanningBrief(state rollbackState) string {
	if state.review != nil && strings.TrimSpace(state.review.Brief) != "" {
		return strings.TrimSpace(state.review.Brief)
	}
	if strings.TrimSpace(state.premise) != "" {
		return strings.TrimSpace(state.premise)
	}
	return ""
}

func fallbackPlanningStartPrompt(state rollbackState) string {
	if state.review == nil {
		return ""
	}
	return strings.TrimSpace(state.review.StartPrompt)
}
