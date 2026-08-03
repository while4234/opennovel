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
	continuationflow "github.com/voocel/ainovel-cli/internal/host/continuation"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
)

const continuationProposalPrompt = `你是小说续写总策划。根据已完成原作的结构化事实和用户确认的续写 Draft，生成可审核的高层续写提案。

只输出一个 JSON 对象，字段必须为：
summary、direction、target_chapter_count、target_total_runes、structure、notes。

规则：
- structure 只能是 single 或 volumes。
- 情节较短、单一主线且不需要多个阶段时选 single；包含多条阶段性主线、明显篇章转换或较多章节时选 volumes。
- target_chapter_count 必须足以完整实现 Draft，但不要无依据拉长。
- 延续原作人物、世界规则、未收伏笔和时间线，不能改写已经完成的章节。
- 不输出章节细纲。`

const continuationVolumePrompt = `你是小说续写分卷策划。根据已审核提案生成完整分卷骨架。

只输出 JSON：{"volumes":[...] }。每个 volume 使用 index、title、theme、arcs；每个 arc 使用 index、title、goal、estimated_chapters、chapters。此阶段 chapters 必须为空数组。

规则：
- volume/arc index 均从 1 连续编号。
- 所有 estimated_chapters 之和必须严格等于提案 target_chapter_count。
- 每卷主题、冲突升级和收束职责必须清楚，延续原作而不是重写原作。`

const continuationOutlinePrompt = `你是小说续写章节细纲策划。根据已审核提案和分卷骨架生成可直接交给 Writer 的详细章节提纲。

只输出一个 JSON 对象。single 结构使用 {"structure":"single","chapters":[...]}；volumes 结构使用 {"structure":"volumes","volumes":[...]}。
每章严格使用 chapter、title、core_event、hook、scenes；scenes 是非空字符串数组。

规则：
- 章节号必须使用给定的绝对范围，从原作最后一章的下一章开始连续编号。
- 章节数量严格匹配提案；不得覆盖、插入或重写原作章节。
- 每章核心事件必须有实质推进，相邻章不得重复同一剧情承诺。
- 保持人物动机、世界规则、未收伏笔和情绪发展连续。
- volumes 模式必须保留已审核的卷/弧边界和章节配额。`

const continuationRevisionPrompt = `你是小说续写规划审稿编辑。按 revision_instruction 修改 current，返回同一 schema 的完整替换 JSON。
不得改变原作基线；未被修改要求涉及的内容应保持稳定。只输出 JSON，不要 Markdown 或解释。`

type continuationPlanningContext struct {
	Premise          string                  `json:"premise,omitempty"`
	StoryState       string                  `json:"story_state,omitempty"`
	Characters       []domain.Character      `json:"characters,omitempty"`
	WorldRules       []domain.WorldRule      `json:"world_rules,omitempty"`
	RecentSummaries  []domain.ChapterSummary `json:"recent_summaries,omitempty"`
	TailOutline      []domain.OutlineEntry   `json:"tail_outline,omitempty"`
	ActiveForeshadow any                     `json:"active_foreshadow,omitempty"`
}

type continuationModelGenerator struct {
	host *Host
}

func (h *Host) continuationPlanner() *continuationflow.Service {
	return continuationflow.NewService(h.store.Continuation, &continuationModelGenerator{host: h})
}

func (g *continuationModelGenerator) GenerateProposal(ctx context.Context, input continuationflow.ProposalInput) (domain.ContinuationProposal, error) {
	var proposal domain.ContinuationProposal
	err := g.generate(ctx, continuationProposalPrompt, map[string]any{
		"source": g.context(input.BaseChapterCount),
		"input":  input,
	}, 3200, &proposal)
	return proposal, err
}

func (g *continuationModelGenerator) ReviseProposal(ctx context.Context, input continuationflow.ProposalRevisionInput) (domain.ContinuationProposal, error) {
	var proposal domain.ContinuationProposal
	err := g.generate(ctx, continuationRevisionPrompt+"\n当前 schema 为 ContinuationProposal。", map[string]any{
		"source":               g.context(input.BaseChapterCount),
		"input":                input.ProposalInput,
		"current":              input.Current,
		"revision_instruction": input.Instruction,
	}, 3200, &proposal)
	return proposal, err
}

func (g *continuationModelGenerator) GenerateVolumes(ctx context.Context, input continuationflow.VolumeInput) ([]domain.VolumeOutline, error) {
	var envelope struct {
		Volumes []domain.VolumeOutline `json:"volumes"`
	}
	err := g.generate(ctx, continuationVolumePrompt, map[string]any{
		"source": g.context(input.BaseChapterCount),
		"input":  input,
	}, 8000, &envelope)
	return envelope.Volumes, err
}

func (g *continuationModelGenerator) ReviseVolumes(ctx context.Context, input continuationflow.VolumeRevisionInput) ([]domain.VolumeOutline, error) {
	var envelope struct {
		Volumes []domain.VolumeOutline `json:"volumes"`
	}
	err := g.generate(ctx, continuationRevisionPrompt+"\n只输出 {\"volumes\":[...]}，并保持总章节配额不变。volume_index=0 表示全局修改。", map[string]any{
		"source":               g.context(input.BaseChapterCount),
		"input":                input.VolumeInput,
		"current":              input.Current,
		"volume_index":         input.VolumeIndex,
		"revision_instruction": input.Instruction,
	}, 8000, &envelope)
	return envelope.Volumes, err
}

func (g *continuationModelGenerator) GenerateOutlines(ctx context.Context, input continuationflow.OutlineInput) (domain.ContinuationOutline, error) {
	if input.Proposal.Structure == domain.ContinuationStructureSingle {
		var outline domain.ContinuationOutline
		err := g.generate(ctx, continuationOutlinePrompt, map[string]any{
			"source":            g.context(input.BaseChapterCount),
			"input":             input,
			"absolute_from":     input.BaseChapterCount + 1,
			"absolute_to":       input.BaseChapterCount + input.Proposal.TargetChapterCount,
			"required_chapters": input.Proposal.TargetChapterCount,
		}, 20000, &outline)
		return outline, err
	}

	out := domain.ContinuationOutline{Structure: domain.ContinuationStructureVolumes}
	nextChapter := input.BaseChapterCount + 1
	for _, volume := range input.Volumes {
		chapterCount := 0
		for _, arc := range volume.Arcs {
			chapterCount += arc.EstimatedChapters
		}
		var envelope struct {
			Volume domain.VolumeOutline `json:"volume"`
		}
		if err := g.generate(ctx, continuationOutlinePrompt+"\n本次只生成给定的一卷，输出 {\"volume\":{...}}。", map[string]any{
			"source":            g.context(input.BaseChapterCount),
			"draft":             input.Draft,
			"proposal":          input.Proposal,
			"volume_skeleton":   volume,
			"absolute_from":     nextChapter,
			"absolute_to":       nextChapter + chapterCount - 1,
			"required_chapters": chapterCount,
		}, 20000, &envelope); err != nil {
			return domain.ContinuationOutline{}, err
		}
		out.Volumes = append(out.Volumes, envelope.Volume)
		nextChapter += chapterCount
	}
	return out, nil
}

func (g *continuationModelGenerator) ReviseOutlines(ctx context.Context, input continuationflow.OutlineRevisionInput) (domain.ContinuationOutline, error) {
	var outline domain.ContinuationOutline
	err := g.generate(ctx, continuationRevisionPrompt+"\n当前 schema 为 ContinuationOutline；章节数、绝对编号、分卷结构必须保持合法。", map[string]any{
		"source":               g.context(input.BaseChapterCount),
		"input":                input.OutlineInput,
		"current":              input.Current,
		"volume_index":         input.VolumeIndex,
		"chapter_from":         input.ChapterFrom,
		"chapter_to":           input.ChapterTo,
		"revision_instruction": input.Instruction,
	}, 20000, &outline)
	return outline, err
}

func (g *continuationModelGenerator) context(baseChapterCount int) continuationPlanningContext {
	h := g.host
	ctx := continuationPlanningContext{StoryState: buildStoryStateSummary(h.store)}
	ctx.Premise, _ = h.store.Outline.LoadPremise()
	ctx.Characters, _ = h.store.Characters.Load()
	if len(ctx.Characters) > 24 {
		ctx.Characters = ctx.Characters[:24]
	}
	ctx.WorldRules, _ = h.store.World.LoadWorldRules()
	if len(ctx.WorldRules) > 32 {
		ctx.WorldRules = ctx.WorldRules[:32]
	}
	ctx.RecentSummaries, _ = h.store.Summaries.LoadRecentSummaries(baseChapterCount+1, 24)
	outline, _ := h.store.Outline.LoadOutline()
	if len(outline) > 20 {
		outline = outline[len(outline)-20:]
	}
	ctx.TailOutline = outline
	foreshadow, _ := h.store.World.LoadActiveForeshadow()
	if len(foreshadow) > 20 {
		foreshadow = foreshadow[:20]
	}
	ctx.ActiveForeshadow = foreshadow
	return ctx
}

func (g *continuationModelGenerator) generate(ctx context.Context, system string, payload any, maxTokens int, target any) error {
	if g == nil || g.host == nil || g.host.models == nil {
		return fmt.Errorf("continuation architect model is unavailable")
	}
	ctx = g.host.continuationActionContext(ctx)
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	g.host.mu.Lock()
	maxAttempts := g.host.cfg.EffectiveStructureRepairMaxAttempts()
	g.host.mu.Unlock()
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	model := g.host.models.ForStageWithFailover(bootstrap.StageDetailOutline, g.host.reportContinuationFailover)
	messages := []agentcore.Message{agentcore.SystemMsg(system), agentcore.UserMsg(string(data))}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		compiledUser, _ := json.Marshal(messages[1:])
		recorder, beginErr := modeldiag.Begin(modeldiag.Request{Store: g.host.store, Task: "continuation_planner", Batch: attempt, System: system, User: compiledUser, InputLimitBytes: manuscriptCompiledRequestBudgetBytes, OutputLimitTokens: maxTokens})
		if beginErr != nil {
			return beginErr
		}
		response, callErr := model.Generate(ctx, messages, nil, agentcore.WithMaxTokens(maxTokens), agentcore.WithJSONMode())
		if callErr != nil {
			_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
			return fmt.Errorf("continuation planner model call: %w", callErr)
		}
		if response == nil {
			_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", nil)
			lastErr = errors.New("model returned nil response")
		} else {
			output := response.Message.TextContent()
			raw := extractJSONObject(output)
			if raw == "" {
				status := modeldiag.StatusDecodeError
				if strings.TrimSpace(output) == "" {
					status = modeldiag.StatusEmptyResponse
				}
				_ = recorder.Finish(status, output, response.Message.Usage)
				lastErr = errors.New("response does not contain a JSON object")
			} else if err := json.Unmarshal([]byte(raw), target); err != nil {
				_ = recorder.Finish(modeldiag.StatusDecodeError, output, response.Message.Usage)
				lastErr = fmt.Errorf("decode continuation JSON: %w", err)
			} else {
				if diagnosticErr := recorder.Finish(modeldiag.StatusCompleted, output, response.Message.Usage); diagnosticErr != nil {
					return diagnosticErr
				}
				return nil
			}
		}
		if attempt == maxAttempts {
			break
		}
		messages = append(messages, agentcore.UserMsg("上一次输出不合法："+lastErr.Error()+"。请只输出完整合法 JSON。"))
		if err := retrypolicy.Wait(ctx, retrypolicy.Delay(attempt)); err != nil {
			return err
		}
	}
	return fmt.Errorf("invalid continuation planner output after %d attempts: %w", maxAttempts, lastErr)
}

func (h *Host) reportContinuationFailover(event bootstrap.FailoverEvent) {
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("续写规划模型切换：%s/%s -> %s/%s（%s）", event.FromProvider, event.FromModel, event.ToProvider, event.ToModel, event.Reason),
		Level:    "warn",
	})
}

func continuationInstruction(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("continuation revision instruction is required")
	}
	return value, nil
}
