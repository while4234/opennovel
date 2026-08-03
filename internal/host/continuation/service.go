package continuation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Generator isolates model-specific planning from workflow orchestration.
// Implementations must return complete replacement candidates; Service owns
// validation, downstream invalidation, revision checks, and persistence.
type Generator interface {
	GenerateProposal(context.Context, ProposalInput) (domain.ContinuationProposal, error)
	ReviseProposal(context.Context, ProposalRevisionInput) (domain.ContinuationProposal, error)
	GenerateVolumes(context.Context, VolumeInput) ([]domain.VolumeOutline, error)
	ReviseVolumes(context.Context, VolumeRevisionInput) ([]domain.VolumeOutline, error)
	GenerateOutlines(context.Context, OutlineInput) (domain.ContinuationOutline, error)
	ReviseOutlines(context.Context, OutlineRevisionInput) (domain.ContinuationOutline, error)
}

type ProposalInput struct {
	SourceSignature  string
	BaseChapterCount int
	Draft            string
}

type ProposalRevisionInput struct {
	ProposalInput
	Current     domain.ContinuationProposal
	Instruction string
}

type VolumeInput struct {
	SourceSignature  string
	BaseChapterCount int
	Draft            string
	Proposal         domain.ContinuationProposal
}

type VolumeRevisionInput struct {
	VolumeInput
	Current     []domain.VolumeOutline
	Instruction string
	VolumeIndex int
}

type OutlineInput struct {
	SourceSignature  string
	BaseChapterCount int
	Draft            string
	Proposal         domain.ContinuationProposal
	Volumes          []domain.VolumeOutline
}

// OutlineRevisionInput supports whole-plan, one-volume, chapter-range, and
// single-chapter revisions. Chapter numbers are absolute story numbers.
type OutlineRevisionInput struct {
	OutlineInput
	Current     domain.ContinuationOutline
	Instruction string
	VolumeIndex int
	ChapterFrom int
	ChapterTo   int
}

type Service struct {
	store     *store.ContinuationStore
	generator Generator
}

func NewService(continuationStore *store.ContinuationStore, generator Generator) *Service {
	return &Service{store: continuationStore, generator: generator}
}

func (s *Service) Snapshot() (*domain.ContinuationSnapshot, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.store.LoadSnapshot()
}

func (s *Service) BeginDraft(expectedRevision int) (*domain.ContinuationSnapshot, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.store.Update(expectedRevision, func(snapshot *domain.ContinuationSnapshot) error {
		return snapshot.Workflow.Transition(domain.ContinuationStageDraftCollecting)
	})
}

// CommitDraft confirms the co-created direction and opens proposal generation.
func (s *Service) CommitDraft(draft string, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	draft = strings.TrimSpace(draft)
	if draft == "" {
		return nil, fmt.Errorf("continuation draft is required")
	}
	return s.store.Update(expectedRevision, func(snapshot *domain.ContinuationSnapshot) error {
		if snapshot.Workflow.Stage != domain.ContinuationStageDraftCollecting {
			return unexpectedStage(snapshot.Workflow.Stage, domain.ContinuationStageDraftCollecting)
		}
		snapshot.Workflow.Draft = draft
		snapshot.Workflow.DraftRevision++
		snapshot.Proposal = nil
		snapshot.Volumes = nil
		snapshot.Outlines = nil
		snapshot.Plan = nil
		return snapshot.Workflow.Transition(domain.ContinuationStageProposalGenerating)
	})
}

func (s *Service) GenerateProposal(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	snapshot, err := s.requireSnapshot(expectedRevision, domain.ContinuationStageProposalGenerating)
	if err != nil {
		return nil, err
	}
	proposal, err := s.generator.GenerateProposal(ctx, proposalInput(snapshot))
	if err != nil {
		return nil, s.failGeneration(expectedRevision, err)
	}
	return s.saveProposal(expectedRevision, proposal)
}

func (s *Service) ReviseProposal(ctx context.Context, expectedRevision int, instruction string) (*domain.ContinuationSnapshot, error) {
	instruction, err := requiredInstruction(instruction)
	if err != nil {
		return nil, err
	}
	generating, err := s.enterGeneratingFrom(expectedRevision, []domain.ContinuationStage{
		domain.ContinuationStageProposalReviewPending,
		domain.ContinuationStageVolumeReviewPending,
		domain.ContinuationStageOutlineReviewPending,
	}, domain.ContinuationStageProposalGenerating)
	if err != nil {
		return nil, err
	}
	if generating.Proposal == nil {
		return nil, fmt.Errorf("continuation proposal is missing")
	}
	proposal, err := s.generator.ReviseProposal(ctx, ProposalRevisionInput{
		ProposalInput: proposalInput(generating),
		Current:       *generating.Proposal,
		Instruction:   instruction,
	})
	if err != nil {
		return nil, s.failGeneration(generating.Workflow.Revision, err)
	}
	return s.saveProposal(generating.Workflow.Revision, proposal)
}

// ApproveProposal takes the conditional branch chosen by the proposal. Long
// form generates a reviewable volume skeleton; short form directly generates
// detailed chapter outlines.
func (s *Service) ApproveProposal(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	snapshot, err := s.requireSnapshot(expectedRevision, domain.ContinuationStageProposalReviewPending)
	if err != nil {
		return nil, err
	}
	if snapshot.Proposal == nil {
		return nil, fmt.Errorf("continuation proposal is missing")
	}
	if snapshot.Proposal.Structure == domain.ContinuationStructureVolumes {
		generating, err := s.enterGenerating(expectedRevision, domain.ContinuationStageProposalReviewPending, domain.ContinuationStageProposalGenerating)
		if err != nil {
			return nil, err
		}
		return s.GenerateVolumes(ctx, generating.Workflow.Revision)
	}
	return s.generateOutlines(ctx, expectedRevision, domain.ContinuationStageProposalReviewPending)
}

// GenerateVolumes resumes a proposal_generating stage that already contains
// an approved volume-shaped proposal. A proposal-generating snapshot without
// a proposal must instead use GenerateProposal.
func (s *Service) GenerateVolumes(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	snapshot, err := s.requireSnapshot(expectedRevision, domain.ContinuationStageProposalGenerating)
	if err != nil {
		return nil, err
	}
	if snapshot.Proposal == nil || snapshot.Proposal.Structure != domain.ContinuationStructureVolumes {
		return nil, fmt.Errorf("approved volume continuation proposal is required")
	}
	volumes, err := s.generator.GenerateVolumes(ctx, volumeInput(snapshot))
	if err != nil {
		return nil, s.failGeneration(expectedRevision, err)
	}
	return s.saveVolumes(expectedRevision, volumes)
}

func (s *Service) ReviseVolumes(ctx context.Context, expectedRevision int, instruction string, volumeIndex int) (*domain.ContinuationSnapshot, error) {
	instruction, err := requiredInstruction(instruction)
	if err != nil {
		return nil, err
	}
	generating, err := s.enterGeneratingFrom(expectedRevision, []domain.ContinuationStage{
		domain.ContinuationStageVolumeReviewPending,
		domain.ContinuationStageOutlineReviewPending,
	}, domain.ContinuationStageProposalGenerating)
	if err != nil {
		return nil, err
	}
	if len(generating.Volumes) == 0 {
		return nil, fmt.Errorf("continuation volume skeleton is missing")
	}
	if volumeIndex < 0 || volumeIndex > len(generating.Volumes) {
		return nil, fmt.Errorf("continuation volume index %d is out of range", volumeIndex)
	}
	volumes, err := s.generator.ReviseVolumes(ctx, VolumeRevisionInput{
		VolumeInput: volumeInput(generating),
		Current:     cloneVolumes(generating.Volumes),
		Instruction: instruction,
		VolumeIndex: volumeIndex,
	})
	if err != nil {
		return nil, s.failGeneration(generating.Workflow.Revision, err)
	}
	return s.saveVolumes(generating.Workflow.Revision, volumes)
}

func (s *Service) ApproveVolumes(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	return s.generateOutlines(ctx, expectedRevision, domain.ContinuationStageVolumeReviewPending)
}

// GenerateOutlines resumes an already-entered outline_generating stage. It is
// used after Retry restores a failed or paused outline request.
func (s *Service) GenerateOutlines(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	snapshot, err := s.requireSnapshot(expectedRevision, domain.ContinuationStageOutlineGenerating)
	if err != nil {
		return nil, err
	}
	outline, err := s.generator.GenerateOutlines(ctx, outlineInput(snapshot))
	if err != nil {
		return nil, s.failGeneration(expectedRevision, err)
	}
	return s.saveOutlines(expectedRevision, outline)
}

func (s *Service) ReviseOutlines(ctx context.Context, expectedRevision int, revision OutlineRevisionInput) (*domain.ContinuationSnapshot, error) {
	instruction, err := requiredInstruction(revision.Instruction)
	if err != nil {
		return nil, err
	}
	generating, err := s.enterGenerating(expectedRevision, domain.ContinuationStageOutlineReviewPending, domain.ContinuationStageOutlineGenerating)
	if err != nil {
		return nil, err
	}
	if generating.Outlines == nil || generating.Proposal == nil {
		return nil, fmt.Errorf("continuation detailed outline is missing")
	}
	revision.OutlineInput = outlineInput(generating)
	revision.Current = *generating.Outlines
	revision.Instruction = instruction
	if err := validateOutlineRevision(generating, revision); err != nil {
		return nil, err
	}
	outline, err := s.generator.ReviseOutlines(ctx, revision)
	if err != nil {
		return nil, s.failGeneration(generating.Workflow.Revision, err)
	}
	return s.saveOutlines(generating.Workflow.Revision, outline)
}

// ApproveOutlines creates final plan data and moves to ready_to_write. It does
// not call Resume or start Writer. Canonical outline integration is performed
// by Store.CommitContinuationPlan when the application chooses to commit it.
func (s *Service) ApproveOutlines(expectedRevision int) (*domain.ContinuationSnapshot, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.store.Update(expectedRevision, func(snapshot *domain.ContinuationSnapshot) error {
		if snapshot.Workflow.Stage != domain.ContinuationStageOutlineReviewPending {
			return unexpectedStage(snapshot.Workflow.Stage, domain.ContinuationStageOutlineReviewPending)
		}
		if snapshot.Proposal == nil || snapshot.Outlines == nil {
			return fmt.Errorf("continuation proposal and detailed outline are required")
		}
		chapters, err := domain.FlattenContinuationOutline(snapshot.Workflow.BaseChapterCount, *snapshot.Outlines)
		if err != nil {
			return err
		}
		if len(chapters) != snapshot.Proposal.TargetChapterCount {
			return fmt.Errorf("continuation outline has %d chapters, want %d", len(chapters), snapshot.Proposal.TargetChapterCount)
		}
		snapshot.Plan = &domain.ContinuationPlan{
			SourceSignature:  snapshot.Workflow.SourceSignature,
			BaseChapterCount: snapshot.Workflow.BaseChapterCount,
			ApprovedRevision: snapshot.Workflow.Revision + 1,
			Proposal:         *snapshot.Proposal,
			Volumes:          cloneVolumes(snapshot.Volumes),
			Outlines:         *snapshot.Outlines,
			Chapters:         append([]domain.OutlineEntry(nil), chapters...),
		}
		return snapshot.Workflow.Transition(domain.ContinuationStageReadyToWrite)
	})
}

func (s *Service) MarkWriting(expectedRevision int) (*domain.ContinuationSnapshot, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.store.Update(expectedRevision, func(snapshot *domain.ContinuationSnapshot) error {
		if snapshot.Plan == nil {
			return fmt.Errorf("continuation plan is not approved")
		}
		return snapshot.Workflow.Transition(domain.ContinuationStageWriting)
	})
}

func (s *Service) Pause(expectedRevision int) (*domain.ContinuationSnapshot, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.store.Update(expectedRevision, func(snapshot *domain.ContinuationSnapshot) error {
		return snapshot.Workflow.Transition(domain.ContinuationStagePaused)
	})
}

func (s *Service) Retry(expectedRevision int) (*domain.ContinuationSnapshot, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.store.Update(expectedRevision, func(snapshot *domain.ContinuationSnapshot) error {
		resume := snapshot.Workflow.ResumeStage
		if resume == "" {
			return fmt.Errorf("continuation resume stage is missing")
		}
		return snapshot.Workflow.Transition(resume)
	})
}

func (s *Service) generateOutlines(ctx context.Context, expectedRevision int, from domain.ContinuationStage) (*domain.ContinuationSnapshot, error) {
	generating, err := s.enterGenerating(expectedRevision, from, domain.ContinuationStageOutlineGenerating)
	if err != nil {
		return nil, err
	}
	return s.GenerateOutlines(ctx, generating.Workflow.Revision)
}

func (s *Service) enterGenerating(expectedRevision int, from, to domain.ContinuationStage) (*domain.ContinuationSnapshot, error) {
	return s.enterGeneratingFrom(expectedRevision, []domain.ContinuationStage{from}, to)
}

func (s *Service) enterGeneratingFrom(expectedRevision int, from []domain.ContinuationStage, to domain.ContinuationStage) (*domain.ContinuationSnapshot, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.store.Update(expectedRevision, func(snapshot *domain.ContinuationSnapshot) error {
		if !stageIn(snapshot.Workflow.Stage, from) {
			return unexpectedStage(snapshot.Workflow.Stage, from...)
		}
		return snapshot.Workflow.Transition(to)
	})
}

func (s *Service) saveProposal(expectedRevision int, proposal domain.ContinuationProposal) (*domain.ContinuationSnapshot, error) {
	if err := proposal.Validate(); err != nil {
		return nil, s.failGeneration(expectedRevision, err)
	}
	return s.store.Update(expectedRevision, func(snapshot *domain.ContinuationSnapshot) error {
		if snapshot.Workflow.Stage != domain.ContinuationStageProposalGenerating {
			return unexpectedStage(snapshot.Workflow.Stage, domain.ContinuationStageProposalGenerating)
		}
		snapshot.Proposal = &proposal
		snapshot.Volumes = nil
		snapshot.Outlines = nil
		snapshot.Plan = nil
		return snapshot.Workflow.Transition(domain.ContinuationStageProposalReviewPending)
	})
}

func (s *Service) saveVolumes(expectedRevision int, volumes []domain.VolumeOutline) (*domain.ContinuationSnapshot, error) {
	snapshot, err := s.requireSnapshot(expectedRevision, domain.ContinuationStageProposalGenerating)
	if err != nil {
		return nil, err
	}
	if snapshot.Proposal == nil {
		return nil, fmt.Errorf("continuation proposal is missing")
	}
	if err := domain.ValidateContinuationVolumes(*snapshot.Proposal, volumes); err != nil {
		return nil, s.failGeneration(expectedRevision, err)
	}
	return s.store.Update(expectedRevision, func(snapshot *domain.ContinuationSnapshot) error {
		snapshot.Volumes = cloneVolumes(volumes)
		snapshot.Outlines = nil
		snapshot.Plan = nil
		return snapshot.Workflow.Transition(domain.ContinuationStageVolumeReviewPending)
	})
}

func (s *Service) saveOutlines(expectedRevision int, outline domain.ContinuationOutline) (*domain.ContinuationSnapshot, error) {
	snapshot, err := s.requireSnapshot(expectedRevision, domain.ContinuationStageOutlineGenerating)
	if err != nil {
		return nil, err
	}
	if snapshot.Proposal == nil {
		return nil, fmt.Errorf("continuation proposal is missing")
	}
	chapters, err := domain.FlattenContinuationOutline(snapshot.Workflow.BaseChapterCount, outline)
	if err != nil {
		return nil, s.failGeneration(expectedRevision, err)
	}
	if len(chapters) != snapshot.Proposal.TargetChapterCount {
		return nil, s.failGeneration(expectedRevision, fmt.Errorf("continuation outline has %d chapters, want %d", len(chapters), snapshot.Proposal.TargetChapterCount))
	}
	return s.store.Update(expectedRevision, func(snapshot *domain.ContinuationSnapshot) error {
		copy := outline
		snapshot.Outlines = &copy
		snapshot.Plan = nil
		return snapshot.Workflow.Transition(domain.ContinuationStageOutlineReviewPending)
	})
}

func (s *Service) failGeneration(expectedRevision int, cause error) error {
	if cause == nil {
		return nil
	}
	_, err := s.store.Update(expectedRevision, func(snapshot *domain.ContinuationSnapshot) error {
		if transitionErr := snapshot.Workflow.Transition(domain.ContinuationStageFailed); transitionErr != nil {
			return transitionErr
		}
		snapshot.Workflow.LastError = cause.Error()
		return nil
	})
	if err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (s *Service) requireSnapshot(expectedRevision int, stage domain.ContinuationStage) (*domain.ContinuationSnapshot, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	snapshot, err := s.store.LoadSnapshot()
	if err != nil {
		return nil, err
	}
	if snapshot.Workflow.Revision != expectedRevision {
		return nil, &store.ContinuationRevisionConflictError{Expected: expectedRevision, Actual: snapshot.Workflow.Revision}
	}
	if snapshot.Workflow.Stage != stage {
		return nil, unexpectedStage(snapshot.Workflow.Stage, stage)
	}
	return snapshot, nil
}

func (s *Service) ready() error {
	if s == nil || s.store == nil {
		return fmt.Errorf("continuation store is required")
	}
	if s.generator == nil {
		return fmt.Errorf("continuation generator is required")
	}
	return nil
}

func proposalInput(snapshot *domain.ContinuationSnapshot) ProposalInput {
	return ProposalInput{
		SourceSignature:  snapshot.Workflow.SourceSignature,
		BaseChapterCount: snapshot.Workflow.BaseChapterCount,
		Draft:            snapshot.Workflow.Draft,
	}
}

func volumeInput(snapshot *domain.ContinuationSnapshot) VolumeInput {
	return VolumeInput{
		SourceSignature:  snapshot.Workflow.SourceSignature,
		BaseChapterCount: snapshot.Workflow.BaseChapterCount,
		Draft:            snapshot.Workflow.Draft,
		Proposal:         *snapshot.Proposal,
	}
}

func outlineInput(snapshot *domain.ContinuationSnapshot) OutlineInput {
	return OutlineInput{
		SourceSignature:  snapshot.Workflow.SourceSignature,
		BaseChapterCount: snapshot.Workflow.BaseChapterCount,
		Draft:            snapshot.Workflow.Draft,
		Proposal:         *snapshot.Proposal,
		Volumes:          cloneVolumes(snapshot.Volumes),
	}
}

func validateOutlineRevision(snapshot *domain.ContinuationSnapshot, revision OutlineRevisionInput) error {
	base := snapshot.Workflow.BaseChapterCount
	last := base + snapshot.Proposal.TargetChapterCount
	if revision.VolumeIndex < 0 || revision.VolumeIndex > len(snapshot.Outlines.Volumes) {
		return fmt.Errorf("continuation volume index %d is out of range", revision.VolumeIndex)
	}
	if revision.ChapterFrom == 0 && revision.ChapterTo == 0 {
		return nil
	}
	if revision.ChapterFrom < base+1 || revision.ChapterFrom > last {
		return fmt.Errorf("continuation chapter_from must be within %d-%d", base+1, last)
	}
	if revision.ChapterTo == 0 {
		revision.ChapterTo = revision.ChapterFrom
	}
	if revision.ChapterTo < revision.ChapterFrom || revision.ChapterTo > last {
		return fmt.Errorf("continuation chapter_to must be within %d-%d and >= chapter_from", base+1, last)
	}
	return nil
}

func requiredInstruction(instruction string) (string, error) {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return "", fmt.Errorf("continuation revision instruction is required")
	}
	return instruction, nil
}

func unexpectedStage(actual domain.ContinuationStage, expected ...domain.ContinuationStage) error {
	return fmt.Errorf("continuation stage is %q, expected %v", actual, expected)
}

func stageIn(stage domain.ContinuationStage, candidates []domain.ContinuationStage) bool {
	for _, candidate := range candidates {
		if stage == candidate {
			return true
		}
	}
	return false
}

func cloneVolumes(volumes []domain.VolumeOutline) []domain.VolumeOutline {
	if len(volumes) == 0 {
		return nil
	}
	out := make([]domain.VolumeOutline, len(volumes))
	for volumeIndex := range volumes {
		out[volumeIndex] = volumes[volumeIndex]
		out[volumeIndex].Arcs = make([]domain.ArcOutline, len(volumes[volumeIndex].Arcs))
		for arcIndex := range volumes[volumeIndex].Arcs {
			out[volumeIndex].Arcs[arcIndex] = volumes[volumeIndex].Arcs[arcIndex]
			chapters := volumes[volumeIndex].Arcs[arcIndex].Chapters
			out[volumeIndex].Arcs[arcIndex].Chapters = make([]domain.OutlineEntry, len(chapters))
			for chapterIndex := range chapters {
				out[volumeIndex].Arcs[arcIndex].Chapters[chapterIndex] = chapters[chapterIndex]
				out[volumeIndex].Arcs[arcIndex].Chapters[chapterIndex].Scenes = append([]string(nil), chapters[chapterIndex].Scenes...)
			}
		}
	}
	return out
}
