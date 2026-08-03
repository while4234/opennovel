package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
	"github.com/voocel/ainovel-cli/internal/store"
)

type ChapterOutlineRevisionRequest struct {
	Chapter     int    `json:"chapter"`
	Instruction string `json:"instruction"`
}

type ChapterOutlineRevisionResult struct {
	Chapter         int                 `json:"chapter"`
	Instruction     string              `json:"instruction"`
	Label           string              `json:"label,omitempty"`
	Outline         domain.OutlineEntry `json:"outline"`
	RewriteQueued   bool                `json:"rewrite_queued,omitempty"`
	DraftReset      bool                `json:"draft_reset,omitempty"`
	PendingRewrites []int               `json:"pending_rewrites,omitempty"`
	StaleNotice     string              `json:"stale_notice,omitempty"`
}

type chapterOutlineRevisionContext struct {
	Current     domain.OutlineEntry           `json:"current"`
	Previous    *domain.OutlineEntry          `json:"previous,omitempty"`
	Next        *domain.OutlineEntry          `json:"next,omitempty"`
	Volume      *chapterOutlineVolume         `json:"volume,omitempty"`
	Arc         *chapterOutlineArc            `json:"arc,omitempty"`
	Adaptation  *domain.AdaptationChapterPlan `json:"adaptation_contract,omitempty"`
	Instruction string                        `json:"instruction"`
}

type chapterOutlineVolume struct {
	Index int    `json:"index"`
	Title string `json:"title"`
	Theme string `json:"theme"`
}

type chapterOutlineArc struct {
	Index int    `json:"index"`
	Title string `json:"title"`
	Goal  string `json:"goal"`
}

var chapterOutlineRevisionRetrySleep = retrypolicy.Wait

const normalChapterOutlineRevisionSystemPrompt = `你是原创小说项目的章节细纲编辑。根据用户的修改要求，只重写指定章节的正式详细提纲。

硬性规则：
- 只输出一个 JSON 对象，不要 Markdown、解释或代码围栏。
- JSON 必须严格使用字段：chapter、title、core_event、hook、scenes。
- chapter 必须保持原章节号；不得增加、删除、合并或拆分章节。
- title、core_event、hook 必须是非空字符串；scenes 必须是非空字符串数组。
- 保持与前后章节、所属卷主题和所属弧目标连续。
- 必须落实用户指令，不能原样返回旧细纲。`

const adaptationChapterOutlineRevisionSystemPrompt = `你是小说改编项目的章节细纲编辑。根据用户的修改要求，只重写指定章节的正式详细提纲。

硬性规则：
- 只输出一个 JSON 对象，不要 Markdown、解释或代码围栏。
- JSON 必须严格使用字段：chapter、title、core_event、hook、scenes。
- chapter 必须保持原章节号；不得增加、删除、合并或拆分章节。
- 必须保留 adaptation_contract 中的来源覆盖、字数预算、保留事件、必要改动和禁止项；只修改章节细纲五项。
- 必须落实用户指令，不能原样返回旧细纲。`

func (h *Host) ReviseChapterOutline(ctx context.Context, req ChapterOutlineRevisionRequest) (ChapterOutlineRevisionResult, error) {
	req.Instruction = strings.TrimSpace(req.Instruction)
	if req.Chapter <= 0 {
		return ChapterOutlineRevisionResult{}, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if req.Instruction == "" {
		return ChapterOutlineRevisionResult{}, fmt.Errorf("instruction is required: %w", errs.ErrToolArgs)
	}
	if err := h.guardExclusive("修改章节细纲"); err != nil {
		return ChapterOutlineRevisionResult{}, fmt.Errorf("revise chapter outline: %w: %w", err, errs.ErrToolConflict)
	}
	// Abort marks the lifecycle paused before the coordinator has necessarily
	// finished unwinding its in-flight tool call. Wait for the old run to become
	// fully idle before invalidating drafts and formal outline projections.
	if h.coordinator != nil {
		h.coordinator.WaitForIdle()
	}
	if err := h.budget.Refuse(); err != nil {
		return ChapterOutlineRevisionResult{}, err
	}
	h.releaseNormalFlowRunOwnership()
	ownership, err := h.acquireNormalFlowOwnership("host:revise-chapter-outline")
	if err != nil {
		return ChapterOutlineRevisionResult{}, err
	}
	defer ownership.Release()

	progressBefore, err := h.store.Progress.Load()
	if err != nil {
		return ChapterOutlineRevisionResult{}, err
	}
	current, err := h.store.Outline.GetChapterOutline(req.Chapter)
	if err != nil {
		return ChapterOutlineRevisionResult{}, fmt.Errorf("load chapter outline: %w", err)
	}
	revisionContext, prompt, err := h.buildChapterOutlineRevisionContext(*current, req.Instruction)
	if err != nil {
		return ChapterOutlineRevisionResult{}, err
	}

	h.mu.Lock()
	structureAttempts := h.cfg.EffectiveStructureRepairMaxAttempts()
	h.mu.Unlock()
	model := h.models.ForStageWithFailover(bootstrap.StageDetailOutline, h.reportChapterOutlineRevisionFailover)
	revised, err := generateChapterOutlineRevision(ctx, model, prompt, revisionContext, structureAttempts, h.store)
	if err != nil {
		return ChapterOutlineRevisionResult{}, err
	}
	if equalFormalOutlineEntry(*current, revised) {
		return ChapterOutlineRevisionResult{}, fmt.Errorf("model returned an unchanged chapter outline: %w", errs.ErrToolPrecondition)
	}
	if err := h.store.ReviseChapterOutline(req.Chapter, revised); err != nil {
		return ChapterOutlineRevisionResult{}, fmt.Errorf("save revised chapter outline: %w", err)
	}
	h.refreshWriterRestore()

	result := buildChapterOutlineRevisionResult(req, revised, progressBefore)
	if progressAfter, loadErr := h.store.Progress.Load(); loadErr == nil && progressAfter != nil {
		result.PendingRewrites = append([]int(nil), progressAfter.PendingRewrites...)
	}
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  result.Label,
		Detail:   req.Instruction,
		Level:    "info",
	})
	return result, nil
}

func (h *Host) buildChapterOutlineRevisionContext(current domain.OutlineEntry, instruction string) (chapterOutlineRevisionContext, string, error) {
	entries, err := h.store.Outline.LoadOutline()
	if err != nil {
		return chapterOutlineRevisionContext{}, "", err
	}
	ctx := chapterOutlineRevisionContext{Current: current, Instruction: instruction}
	for i := range entries {
		if entries[i].Chapter != current.Chapter {
			continue
		}
		if i > 0 {
			previous := entries[i-1]
			ctx.Previous = &previous
		}
		if i+1 < len(entries) {
			next := entries[i+1]
			ctx.Next = &next
		}
		break
	}
	volumes, err := h.store.Outline.LoadLayeredOutline()
	if err != nil {
		return chapterOutlineRevisionContext{}, "", err
	}
	nextChapter := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			arcCount := len(arc.Chapters)
			if arcCount == 0 {
				arcCount = arc.EstimatedChapters
			}
			if current.Chapter >= nextChapter && current.Chapter < nextChapter+arcCount {
				ctx.Volume = &chapterOutlineVolume{Index: volume.Index, Title: volume.Title, Theme: volume.Theme}
				ctx.Arc = &chapterOutlineArc{Index: arc.Index, Title: arc.Title, Goal: arc.Goal}
			}
			nextChapter += arcCount
		}
	}
	if !h.store.Adaptation.Exists() {
		return ctx, normalChapterOutlineRevisionSystemPrompt, nil
	}
	plan, err := h.store.Adaptation.LoadPlan()
	if err != nil {
		return chapterOutlineRevisionContext{}, "", fmt.Errorf("load adaptation plan: %w", err)
	}
	if plan != nil {
		for i := range plan.Chapters {
			if plan.Chapters[i].Chapter == current.Chapter {
				contract := plan.Chapters[i]
				ctx.Adaptation = &contract
				break
			}
		}
	}
	return ctx, adaptationChapterOutlineRevisionSystemPrompt, nil
}

func generateChapterOutlineRevision(ctx context.Context, model agentcore.ChatModel, systemPrompt string, revisionContext chapterOutlineRevisionContext, maxAttempts int, diagnosticStores ...*store.Store) (domain.OutlineEntry, error) {
	if model == nil {
		return domain.OutlineEntry{}, fmt.Errorf("architect model is unavailable")
	}
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	payload, err := json.Marshal(revisionContext)
	if err != nil {
		return domain.OutlineEntry{}, err
	}
	messages := []agentcore.Message{
		agentcore.SystemMsg(systemPrompt),
		agentcore.UserMsg(string(payload)),
	}
	var lastErr error
	var diagnosticStore *store.Store
	if len(diagnosticStores) > 0 {
		diagnosticStore = diagnosticStores[0]
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return domain.OutlineEntry{}, err
		}
		compiledUser, _ := json.Marshal(messages[1:])
		recorder, beginErr := modeldiag.Begin(modeldiag.Request{Store: diagnosticStore, Task: "chapter_outline_revision", ChapterID: revisionContext.Current.ID, Batch: attempt, System: systemPrompt, User: compiledUser, InputLimitBytes: manuscriptCompiledRequestBudgetBytes, OutputLimitTokens: 1800, SelectorCounts: map[string]int{"chapters": 1}})
		if beginErr != nil {
			return domain.OutlineEntry{}, beginErr
		}
		response, err := model.Generate(ctx, messages, nil, agentcore.WithMaxTokens(1800), agentcore.WithJSONMode())
		if err != nil {
			_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
			return domain.OutlineEntry{}, fmt.Errorf("revise chapter outline model call: %w", err)
		}
		if response == nil {
			_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", nil)
			lastErr = errors.New("model returned nil response")
		} else {
			output := response.Message.TextContent()
			if strings.TrimSpace(output) == "" {
				_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", response.Message.Usage)
				lastErr = errors.New("model returned empty response")
			} else if revised, parseErr := parseChapterOutlineRevision(output, revisionContext.Current.Chapter); parseErr == nil {
				if diagnosticErr := recorder.Finish(modeldiag.StatusCompleted, output, response.Message.Usage); diagnosticErr != nil {
					return domain.OutlineEntry{}, diagnosticErr
				}
				return revised, nil
			} else {
				_ = recorder.Finish(chapterOutlineDiagnosticStatus(parseErr), output, response.Message.Usage)
				lastErr = parseErr
			}
		}
		if attempt == maxAttempts {
			break
		}
		messages = append(messages, agentcore.UserMsg(
			"上一次输出无法解析："+lastErr.Error()+"。请重新输出且只输出一个完整、合法的 JSON 对象。",
		))
		if err := chapterOutlineRevisionRetrySleep(ctx, retrypolicy.Delay(attempt)); err != nil {
			return domain.OutlineEntry{}, err
		}
	}
	return domain.OutlineEntry{}, fmt.Errorf("invalid chapter outline after %d attempts: %w", maxAttempts, lastErr)
}

func chapterOutlineDiagnosticStatus(err error) string {
	if err == nil {
		return modeldiag.StatusCompleted
	}
	if strings.Contains(err.Error(), "decode JSON") || strings.Contains(err.Error(), "JSON object") {
		return modeldiag.StatusDecodeError
	}
	return modeldiag.StatusInvalidSchema
}

func parseChapterOutlineRevision(raw string, chapter int) (domain.OutlineEntry, error) {
	raw = extractJSONObject(raw)
	if raw == "" {
		return domain.OutlineEntry{}, errors.New("response does not contain a JSON object")
	}
	var entry domain.OutlineEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return domain.OutlineEntry{}, fmt.Errorf("decode JSON: %w", err)
	}
	entry.Chapter = chapter
	entry.Title = strings.TrimSpace(entry.Title)
	entry.CoreEvent = strings.TrimSpace(entry.CoreEvent)
	entry.Hook = strings.TrimSpace(entry.Hook)
	scenes := make([]string, 0, len(entry.Scenes))
	for _, scene := range entry.Scenes {
		if scene = strings.TrimSpace(scene); scene != "" {
			scenes = append(scenes, scene)
		}
	}
	entry.Scenes = scenes
	switch {
	case entry.Title == "":
		return domain.OutlineEntry{}, errors.New("title is required")
	case entry.CoreEvent == "":
		return domain.OutlineEntry{}, errors.New("core_event is required")
	case entry.Hook == "":
		return domain.OutlineEntry{}, errors.New("hook is required")
	case len(entry.Scenes) == 0:
		return domain.OutlineEntry{}, errors.New("scenes are required")
	}
	return entry, nil
}

func equalFormalOutlineEntry(a, b domain.OutlineEntry) bool {
	if strings.TrimSpace(a.Title) != strings.TrimSpace(b.Title) ||
		strings.TrimSpace(a.CoreEvent) != strings.TrimSpace(b.CoreEvent) ||
		strings.TrimSpace(a.Hook) != strings.TrimSpace(b.Hook) {
		return false
	}
	return equalTrimmedStrings(a.Scenes, b.Scenes)
}

func equalTrimmedStrings(a, b []string) bool {
	trim := func(values []string) []string {
		out := make([]string, 0, len(values))
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				out = append(out, value)
			}
		}
		return out
	}
	a, b = trim(a), trim(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func buildChapterOutlineRevisionResult(req ChapterOutlineRevisionRequest, revised domain.OutlineEntry, progress *domain.Progress) ChapterOutlineRevisionResult {
	result := ChapterOutlineRevisionResult{Chapter: req.Chapter, Instruction: req.Instruction, Outline: revised}
	completed := false
	if progress != nil {
		for _, chapter := range progress.CompletedChapters {
			if chapter == req.Chapter {
				completed = true
				break
			}
		}
	}
	switch {
	case completed:
		result.RewriteQueued = true
		result.Label = fmt.Sprintf("第 %d 章细纲已更新；已加入返工队列，恢复后将优先重写并重新审核", req.Chapter)
		result.StaleNotice = "旧正文、章节摘要、事实记录及受影响的弧/卷审核结果已失效，将在返工后重建。"
	case progress != nil && progress.InProgressChapter == req.Chapter:
		result.DraftReset = true
		result.Label = fmt.Sprintf("第 %d 章细纲已更新；旧草稿已清理，恢复后将按新细纲重新开发", req.Chapter)
	default:
		result.Label = fmt.Sprintf("第 %d 章细纲已更新；写到该章时将使用新细纲", req.Chapter)
	}
	return result
}

func (h *Host) reportChapterOutlineRevisionFailover(event bootstrap.FailoverEvent) {
	from := event.FromProvider + "/" + event.FromModel
	to := event.ToProvider + "/" + event.ToModel
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("章节细纲修改模型切换：%s -> %s（%s）", from, to, event.Reason),
		Level:    "warn",
	})
}
