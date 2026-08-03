package domain

import (
	"fmt"
	"strings"
	"time"
)

// ContinuationStage is the durable state of an imported-novel continuation.
// It is deliberately separate from Progress.Flow: imported source chapters
// remain normal story facts while this workflow controls whether new prose may
// be generated.
type ContinuationStage string

const (
	ContinuationStageSourceReady           ContinuationStage = "source_ready"
	ContinuationStageDraftCollecting       ContinuationStage = "draft_collecting"
	ContinuationStageProposalGenerating    ContinuationStage = "proposal_generating"
	ContinuationStageProposalReviewPending ContinuationStage = "proposal_review_pending"
	ContinuationStageVolumeReviewPending   ContinuationStage = "volume_review_pending"
	ContinuationStageOutlineGenerating     ContinuationStage = "outline_generating"
	ContinuationStageOutlineReviewPending  ContinuationStage = "outline_review_pending"
	ContinuationStageReadyToWrite          ContinuationStage = "ready_to_write"
	ContinuationStageWriting               ContinuationStage = "writing"
	ContinuationStagePaused                ContinuationStage = "paused"
	ContinuationStageFailed                ContinuationStage = "failed"
)

type ContinuationStructure string

const (
	ContinuationStructureSingle  ContinuationStructure = "single"
	ContinuationStructureVolumes ContinuationStructure = "volumes"
)

const ContinuationSchemaVersion = 1

// ContinuationWorkflow stores only workflow identity and lifecycle state.
// Candidate planning artifacts are stored separately so they can be replaced
// or invalidated without touching the canonical story outline.
type ContinuationWorkflow struct {
	Version          int               `json:"version"`
	Stage            ContinuationStage `json:"stage"`
	SourceSignature  string            `json:"source_signature"`
	BaseChapterCount int               `json:"base_chapter_count"`
	Revision         int               `json:"revision"`
	Draft            string            `json:"draft,omitempty"`
	DraftRevision    int               `json:"draft_revision,omitempty"`
	ResumeStage      ContinuationStage `json:"resume_stage,omitempty"`
	LastError        string            `json:"last_error,omitempty"`
	UpdatedAt        string            `json:"updated_at,omitempty"`
}

// ContinuationProposal is the reviewed high-level direction. Structure is a
// planner decision: single skips volume review, volumes requires it.
type ContinuationProposal struct {
	Summary            string                `json:"summary"`
	Direction          string                `json:"direction"`
	TargetChapterCount int                   `json:"target_chapter_count"`
	TargetTotalRunes   int                   `json:"target_total_runes,omitempty"`
	Structure          ContinuationStructure `json:"structure"`
	Notes              []string              `json:"notes,omitempty"`
}

// ContinuationOutline is the candidate detailed outline. Short continuations
// use Chapters. Volume-shaped continuations use Volumes with expanded arcs.
type ContinuationOutline struct {
	Structure ContinuationStructure `json:"structure"`
	Volumes   []VolumeOutline       `json:"volumes,omitempty"`
	Chapters  []OutlineEntry        `json:"chapters,omitempty"`
}

// ContinuationPlan is the final, approved continuation planning contract.
// It contains canonical-ready data but starting Writer remains a Host concern.
type ContinuationPlan struct {
	SourceSignature  string               `json:"source_signature"`
	BaseChapterCount int                  `json:"base_chapter_count"`
	ApprovedRevision int                  `json:"approved_revision"`
	Proposal         ContinuationProposal `json:"proposal"`
	Volumes          []VolumeOutline      `json:"volumes,omitempty"`
	Outlines         ContinuationOutline  `json:"outlines"`
	Chapters         []OutlineEntry       `json:"chapters"`
}

// ContinuationSnapshot is the complete read model used by TUI/Web callers.
type ContinuationSnapshot struct {
	Workflow ContinuationWorkflow  `json:"workflow"`
	Proposal *ContinuationProposal `json:"proposal,omitempty"`
	Volumes  []VolumeOutline       `json:"volumes,omitempty"`
	Outlines *ContinuationOutline  `json:"outlines,omitempty"`
	Plan     *ContinuationPlan     `json:"plan,omitempty"`
}

func NewContinuationWorkflow(sourceSignature string, baseChapterCount int) (ContinuationWorkflow, error) {
	sourceSignature = strings.TrimSpace(sourceSignature)
	if sourceSignature == "" {
		return ContinuationWorkflow{}, fmt.Errorf("continuation source signature is required")
	}
	if baseChapterCount <= 0 {
		return ContinuationWorkflow{}, fmt.Errorf("continuation base chapter count must be > 0")
	}
	return ContinuationWorkflow{
		Version:          ContinuationSchemaVersion,
		Stage:            ContinuationStageSourceReady,
		SourceSignature:  sourceSignature,
		BaseChapterCount: baseChapterCount,
		Revision:         1,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func (s ContinuationStage) Valid() bool {
	switch s {
	case ContinuationStageSourceReady,
		ContinuationStageDraftCollecting,
		ContinuationStageProposalGenerating,
		ContinuationStageProposalReviewPending,
		ContinuationStageVolumeReviewPending,
		ContinuationStageOutlineGenerating,
		ContinuationStageOutlineReviewPending,
		ContinuationStageReadyToWrite,
		ContinuationStageWriting,
		ContinuationStagePaused,
		ContinuationStageFailed:
		return true
	default:
		return false
	}
}

func (s ContinuationStructure) Valid() bool {
	return s == ContinuationStructureSingle || s == ContinuationStructureVolumes
}

// ValidateContinuationTransition enforces user-review gates. Revisions that
// keep the same stage do not call this function.
func ValidateContinuationTransition(from, to ContinuationStage) error {
	if !from.Valid() || !to.Valid() {
		return fmt.Errorf("invalid continuation transition %q -> %q", from, to)
	}
	allowed := map[ContinuationStage]map[ContinuationStage]bool{
		ContinuationStageSourceReady: {
			ContinuationStageDraftCollecting: true,
		},
		ContinuationStageDraftCollecting: {
			ContinuationStageProposalGenerating: true,
		},
		ContinuationStageProposalGenerating: {
			ContinuationStageProposalReviewPending: true,
			ContinuationStageVolumeReviewPending:   true,
			ContinuationStagePaused:                true,
			ContinuationStageFailed:                true,
		},
		ContinuationStageProposalReviewPending: {
			ContinuationStageProposalGenerating:  true,
			ContinuationStageVolumeReviewPending: true,
			ContinuationStageOutlineGenerating:   true,
		},
		ContinuationStageVolumeReviewPending: {
			ContinuationStageProposalGenerating: true,
			ContinuationStageOutlineGenerating:  true,
		},
		ContinuationStageOutlineGenerating: {
			ContinuationStageOutlineReviewPending: true,
			ContinuationStagePaused:               true,
			ContinuationStageFailed:               true,
		},
		ContinuationStageOutlineReviewPending: {
			ContinuationStageProposalGenerating: true,
			ContinuationStageOutlineGenerating:  true,
			ContinuationStageReadyToWrite:       true,
		},
		ContinuationStageReadyToWrite: {
			ContinuationStageWriting: true,
		},
		ContinuationStageWriting: {
			ContinuationStagePaused: true,
			ContinuationStageFailed: true,
		},
		ContinuationStagePaused: {
			ContinuationStageProposalGenerating: true,
			ContinuationStageOutlineGenerating:  true,
			ContinuationStageWriting:            true,
		},
		ContinuationStageFailed: {
			ContinuationStageProposalGenerating: true,
			ContinuationStageOutlineGenerating:  true,
			ContinuationStageWriting:            true,
		},
	}
	if !allowed[from][to] {
		return fmt.Errorf("continuation transition %q -> %q is not allowed", from, to)
	}
	return nil
}

func (w *ContinuationWorkflow) Transition(to ContinuationStage) error {
	if w == nil {
		return fmt.Errorf("continuation workflow is required")
	}
	if err := ValidateContinuationTransition(w.Stage, to); err != nil {
		return err
	}
	if to == ContinuationStagePaused || to == ContinuationStageFailed {
		w.ResumeStage = w.Stage
	} else if w.Stage == ContinuationStagePaused || w.Stage == ContinuationStageFailed {
		if w.ResumeStage != "" && w.ResumeStage != to {
			return fmt.Errorf("continuation must resume at %q, got %q", w.ResumeStage, to)
		}
		w.ResumeStage = ""
		w.LastError = ""
	}
	w.Stage = to
	return nil
}

func (p ContinuationProposal) Validate() error {
	if strings.TrimSpace(p.Summary) == "" {
		return fmt.Errorf("continuation proposal summary is required")
	}
	if strings.TrimSpace(p.Direction) == "" {
		return fmt.Errorf("continuation proposal direction is required")
	}
	if p.TargetChapterCount <= 0 {
		return fmt.Errorf("continuation target chapter count must be > 0")
	}
	if !p.Structure.Valid() {
		return fmt.Errorf("invalid continuation structure %q", p.Structure)
	}
	return nil
}

// ValidateContinuationVolumes checks a high-level volume skeleton without
// requiring detailed chapters. Estimated arc counts must cover the proposal.
func ValidateContinuationVolumes(proposal ContinuationProposal, volumes []VolumeOutline) error {
	if err := proposal.Validate(); err != nil {
		return err
	}
	if proposal.Structure != ContinuationStructureVolumes {
		if len(volumes) != 0 {
			return fmt.Errorf("single continuation must not define volume skeletons")
		}
		return nil
	}
	if len(volumes) == 0 {
		return fmt.Errorf("volume continuation requires at least one volume")
	}
	total := 0
	for volumeIndex, volume := range volumes {
		if volume.Index != volumeIndex+1 {
			return fmt.Errorf("continuation volume index must be %d, got %d", volumeIndex+1, volume.Index)
		}
		if strings.TrimSpace(volume.Title) == "" {
			return fmt.Errorf("continuation volume %d title is required", volume.Index)
		}
		if len(volume.Arcs) == 0 {
			return fmt.Errorf("continuation volume %d requires at least one arc", volume.Index)
		}
		for arcIndex, arc := range volume.Arcs {
			if arc.Index != arcIndex+1 {
				return fmt.Errorf("continuation volume %d arc index must be %d, got %d", volume.Index, arcIndex+1, arc.Index)
			}
			count := arc.EstimatedChapters
			if len(arc.Chapters) > 0 {
				count = len(arc.Chapters)
			}
			if count <= 0 {
				return fmt.Errorf("continuation volume %d arc %d chapter count must be > 0", volume.Index, arc.Index)
			}
			total += count
		}
	}
	if total != proposal.TargetChapterCount {
		return fmt.Errorf("continuation volume skeleton covers %d chapters, want %d", total, proposal.TargetChapterCount)
	}
	return nil
}

// FlattenContinuationOutline preserves global chapter numbers and validates
// that every planned chapter starts at base+1 and remains continuous.
func FlattenContinuationOutline(baseChapterCount int, outline ContinuationOutline) ([]OutlineEntry, error) {
	if baseChapterCount <= 0 {
		return nil, fmt.Errorf("continuation base chapter count must be > 0")
	}
	if !outline.Structure.Valid() {
		return nil, fmt.Errorf("invalid continuation outline structure %q", outline.Structure)
	}
	var chapters []OutlineEntry
	switch outline.Structure {
	case ContinuationStructureSingle:
		if len(outline.Volumes) > 0 {
			return nil, fmt.Errorf("single continuation outline must not contain volumes")
		}
		chapters = append(chapters, outline.Chapters...)
	case ContinuationStructureVolumes:
		if len(outline.Chapters) > 0 {
			return nil, fmt.Errorf("volume continuation outline must not contain flat chapters")
		}
		for _, volume := range outline.Volumes {
			for _, arc := range volume.Arcs {
				chapters = append(chapters, arc.Chapters...)
			}
		}
	}
	if len(chapters) == 0 {
		return nil, fmt.Errorf("continuation detailed outline is empty")
	}
	for index, chapter := range chapters {
		want := baseChapterCount + index + 1
		if chapter.Chapter != want {
			return nil, fmt.Errorf("continuation chapter number must be %d, got %d", want, chapter.Chapter)
		}
		if strings.TrimSpace(chapter.Title) == "" {
			return nil, fmt.Errorf("continuation chapter %d title is required", chapter.Chapter)
		}
		if strings.TrimSpace(chapter.CoreEvent) == "" {
			return nil, fmt.Errorf("continuation chapter %d core event is required", chapter.Chapter)
		}
	}
	return chapters, nil
}
