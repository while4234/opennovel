package host

import (
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
)

const (
	ChapterRevisionModeRewrite = "rewrite"
	ChapterRevisionModePolish  = "polish"
)

type ChapterRevisionRequest struct {
	Chapter     int
	Instruction string
	Mode        string
}

type ChapterRevisionResult struct {
	Chapter         int    `json:"chapter"`
	Instruction     string `json:"instruction"`
	Mode            string `json:"mode"`
	Label           string `json:"label,omitempty"`
	PendingRewrites []int  `json:"pending_rewrites,omitempty"`
	StaleNotice     string `json:"stale_notice,omitempty"`
}

func (h *Host) ReviseChapter(req ChapterRevisionRequest) (ChapterRevisionResult, error) {
	normalized, err := normalizeChapterRevisionRequest(req)
	if err != nil {
		return ChapterRevisionResult{}, err
	}

	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return ChapterRevisionResult{}, fmt.Errorf("already running: %w", errs.ErrToolConflict)
	}
	if h.cocreating {
		h.mu.Unlock()
		return ChapterRevisionResult{}, fmt.Errorf("co-create is active: %w", errs.ErrToolConflict)
	}
	h.mu.Unlock()

	if err := h.budget.Refuse(); err != nil {
		return ChapterRevisionResult{}, err
	}
	if h.coordinator != nil {
		h.coordinator.WaitForIdle()
	}
	h.releaseNormalFlowRunOwnership()
	ownership, err := h.acquireNormalFlowOwnership("host:revise-chapter")
	if err != nil {
		return ChapterRevisionResult{}, err
	}
	defer ownership.Release()

	flow := domain.FlowRewriting
	if normalized.Mode == ChapterRevisionModePolish {
		flow = domain.FlowPolishing
	}
	if err := h.store.Progress.QueuePendingRewrites([]int{normalized.Chapter}, normalized.Instruction, flow); err != nil {
		return ChapterRevisionResult{}, err
	}
	if _, err := h.store.Checkpoints.AppendArtifact(domain.GlobalScope(), "reopen", "meta/progress.json"); err != nil {
		return ChapterRevisionResult{}, fmt.Errorf("checkpoint reopen: %w: %w", errs.ErrStoreWrite, err)
	}

	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("章节返工已入队: 第 %d 章", normalized.Chapter),
		Detail:   normalized.Instruction,
		Level:    "info",
	})
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  chapterRevisionStaleNotice(),
		Level:    "warn",
	})

	label, err := h.resume(true)
	if err != nil {
		return ChapterRevisionResult{}, err
	}
	result := ChapterRevisionResult{
		Chapter:     normalized.Chapter,
		Instruction: normalized.Instruction,
		Mode:        normalized.Mode,
		Label:       label,
		StaleNotice: chapterRevisionStaleNotice(),
	}
	if progress, perr := h.store.Progress.Load(); perr == nil && progress != nil {
		result.PendingRewrites = append([]int(nil), progress.PendingRewrites...)
	}
	return result, nil
}

func normalizeChapterRevisionRequest(req ChapterRevisionRequest) (ChapterRevisionRequest, error) {
	req.Instruction = strings.TrimSpace(req.Instruction)
	req.Mode = strings.TrimSpace(strings.ToLower(req.Mode))
	if req.Mode == "" {
		req.Mode = ChapterRevisionModeRewrite
	}
	if req.Chapter <= 0 {
		return req, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if req.Instruction == "" {
		return req, fmt.Errorf("instruction is required: %w", errs.ErrToolArgs)
	}
	switch req.Mode {
	case ChapterRevisionModeRewrite, ChapterRevisionModePolish:
		return req, nil
	default:
		return req, fmt.Errorf("mode must be %q or %q: %w", ChapterRevisionModeRewrite, ChapterRevisionModePolish, errs.ErrToolArgs)
	}
}

func chapterRevisionStaleNotice() string {
	return "单章返工会覆盖正文与章节摘要；时间线、伏笔、人物关系、配角名册及弧/卷摘要可能需要后续重审。"
}
