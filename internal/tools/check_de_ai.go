package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/deai"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// CheckDeAITool is the explicit post-writing prose stage. It is intentionally
// separate from consistency/adaptation checks: those validate story facts and
// source obligations, while this tool validates export cleanliness and the
// recurrent prose symptoms observed across long-running generated novels.
type CheckDeAITool struct{ store *store.Store }

type checkDeAIResult struct {
	deai.Audit
	RepairPlan    deai.RepairPlan       `json:"repair_plan"`
	CommitContext *chapterCommitContext `json:"commit_context,omitempty"`
}

type chapterCommitContext struct {
	DraftSHA256                 string   `json:"draft_sha256"`
	Title                       string   `json:"title"`
	CoreEvent                   string   `json:"core_event"`
	Hook                        string   `json:"hook"`
	CharacterIDs                []string `json:"character_ids"`
	Characters                  []string `json:"characters"`
	Scenes                      []string `json:"scenes"`
	AllowedCharacterStateFields []string `json:"allowed_character_state_fields"`
}

func NewCheckDeAITool(store *store.Store) *CheckDeAITool { return &CheckDeAITool{store: store} }

func (t *CheckDeAITool) Name() string { return "check_de_ai" }
func (t *CheckDeAITool) Description() string {
	return "独立去AI化审校：初次运行前当前草稿必须已通过 check_consistency；由 repair_de_ai_batch 产生的受约束精确修订可直接复检，去AI通过后再统一运行一次最终 check_consistency。除标题泄漏、破折号、排比、模板反应、比喻和缓冲词外，还检查相邻肯否矛盾、同句首连发、连续主语段首、说明书式参数堆砌、房号等数字锚点过度复述和严重碎段；返回可直接定位的原文 examples，修改后必须重新调用。"
}
func (t *CheckDeAITool) Label() string                          { return "去AI化审校" }
func (t *CheckDeAITool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *CheckDeAITool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *CheckDeAITool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("要做去AI化审校的章节号")).Required(),
	)
}

func (t *CheckDeAITool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Chapter int `json:"chapter"`
	}
	if err := json.Unmarshal(args, &request); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if request.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if err := t.store.DeAI.Enable(); err != nil {
		return nil, fmt.Errorf("enable de-AI stage: %w: %w", errs.ErrStoreWrite, err)
	}
	content, _, err := t.store.Drafts.LoadChapterContent(request.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load chapter content: %w: %w", errs.ErrStoreRead, err)
	}
	if content == "" {
		return nil, fmt.Errorf("no content found for chapter %d: %w", request.Chapter, errs.ErrToolPrecondition)
	}
	draftDigest := "sha256:" + store.TextSHA256(content)
	consistency := t.store.Checkpoints.LatestByStep(domain.ChapterScope(request.Chapter), "consistency_check")
	latest := t.store.Checkpoints.Latest(domain.ChapterScope(request.Chapter))
	boundedDeAIRecheck := latest != nil && latest.Step == "de_ai_batch_repair" && latest.Digest == draftDigest
	if (consistency == nil || consistency.Digest != draftDigest) && !boundedDeAIRecheck {
		return nil, fmt.Errorf(
			"第 %d 章当前草稿尚未通过一致性审核，也不是刚由 repair_de_ai_batch 产生的受约束去AI修订。先调用 novel_context(chapter=%d)，逐场景核对章节契约中的时间、地点、视角、人物、事件顺序与不可逆结果，再 read_chapter 并调用 check_consistency；修正全部 critical/error finding 后才能首次 check_de_ai: %w",
			request.Chapter, request.Chapter, errs.ErrToolPrecondition,
		)
	}

	report := deai.Analyze(content)
	audit := deai.Audit{
		Version:     deai.PolicyVersion,
		Chapter:     request.Chapter,
		DraftSHA256: store.TextSHA256(content),
		Passed:      report.Passed(),
		Report:      report,
		CheckedAt:   time.Now(),
	}
	if err := t.store.DeAI.SaveAudit(audit); err != nil {
		return nil, fmt.Errorf("save de-AI audit: %w: %w", errs.ErrStoreWrite, err)
	}
	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(request.Chapter), "de_ai_check", t.store.DeAI.AuditPath(request.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint de-AI audit: %w: %w", errs.ErrStoreWrite, err)
	}
	return json.Marshal(checkDeAIResult{
		Audit:         audit,
		RepairPlan:    report.RepairPlan(),
		CommitContext: t.buildChapterCommitContext(request.Chapter, audit.DraftSHA256),
	})
}

func (t *CheckDeAITool) buildChapterCommitContext(chapter int, digest string) *chapterCommitContext {
	outline, err := t.store.Outline.GetChapterOutline(chapter)
	if err != nil || outline == nil {
		return nil
	}
	foundation, err := t.store.Foundation.Load()
	if err != nil {
		return nil
	}
	names := make(map[string]string, len(foundation.Characters))
	for _, character := range foundation.Characters {
		names[character.ID] = character.Name
	}
	characterIDs := append([]string(nil), outline.CharacterIDs...)
	characters := make([]string, 0, len(characterIDs))
	for _, id := range characterIDs {
		if name := names[id]; name != "" {
			characters = append(characters, name)
		}
	}
	return &chapterCommitContext{
		DraftSHA256:                 digest,
		Title:                       outline.Title,
		CoreEvent:                   outline.CoreEvent,
		Hook:                        outline.Hook,
		CharacterIDs:                characterIDs,
		Characters:                  characters,
		Scenes:                      append([]string(nil), outline.Scenes...),
		AllowedCharacterStateFields: append([]string(nil), runtimeCharacterFieldNames...),
	}
}
