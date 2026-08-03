package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

const maxDeAIBatchRepairs = 8

// RepairDeAIBatchTool applies a small, model-authored set of exact prose
// replacements against the matching failed de-AI audit. It deliberately does
// not automate synonym substitution: every replacement carries the Writer's
// own revised sentence or paragraph and remains auditable as one batch.
type RepairDeAIBatchTool struct{ store *store.Store }

type deAIBatchReplacement struct {
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

type deAIBatchRepairRequest struct {
	Chapter int                    `json:"chapter"`
	Repairs []deAIBatchReplacement `json:"repairs"`
}

func NewRepairDeAIBatchTool(s *store.Store) *RepairDeAIBatchTool {
	return &RepairDeAIBatchTool{store: s}
}

func (t *RepairDeAIBatchTool) Name() string  { return "repair_de_ai_batch" }
func (t *RepairDeAIBatchTool) Label() string { return "去AI化分批修订" }
func (t *RepairDeAIBatchTool) Description() string {
	return "按最新 failed check_de_ai 报告做一小批精确文本修订（1-8 处）。每个 old_string 必须在当前草稿中唯一出现，new_string 必须是保留剧情信息后的真实重写；每批落盘后只重新 check_de_ai，去AI通过后再统一运行一次最终 check_consistency。不要用它做整章重写或机械同义词替换。"
}

func (t *RepairDeAIBatchTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *RepairDeAIBatchTool) ConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *RepairDeAIBatchTool) ActivityDescription(_ json.RawMessage) string {
	return "分批修订去AI化问题"
}

func (t *RepairDeAIBatchTool) Schema() map[string]any {
	repair := schema.Object(
		schema.Property("old_string", schema.String("当前草稿中唯一出现的原文句子或短段；请从 check_de_ai examples 或回读正文精确复制")).Required(),
		schema.Property("new_string", schema.String("保留事件、人物关系和必要信息后的新句子或短段")).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
		schema.Property("repairs", schema.Array("同一问题类别的一小批 1-8 处精确修订；改完只重新 check_de_ai，去AI通过后再统一运行最终 check_consistency", repair)).Required(),
	)
}

func (t *RepairDeAIBatchTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var request deAIBatchRepairRequest
	if err := json.Unmarshal(args, &request); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if request.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if len(request.Repairs) == 0 || len(request.Repairs) > maxDeAIBatchRepairs {
		return nil, fmt.Errorf("repairs must contain 1-%d items: %w", maxDeAIBatchRepairs, errs.ErrToolArgs)
	}
	if t == nil || t.store == nil || t.store.DeAI == nil {
		return nil, fmt.Errorf("de-AI store is unavailable: %w", errs.ErrToolPrecondition)
	}
	enabled, err := t.store.DeAI.Enabled()
	if err != nil {
		return nil, fmt.Errorf("read de-AI policy: %w: %w", errs.ErrStoreRead, err)
	}
	if !enabled {
		return nil, fmt.Errorf("第 %d 章尚未进入去AI化阶段；先调用 check_de_ai: %w", request.Chapter, errs.ErrToolPrecondition)
	}
	audit, err := t.store.DeAI.LoadAudit(request.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load de-AI audit: %w: %w", errs.ErrStoreRead, err)
	}
	if audit == nil || audit.Passed {
		return nil, fmt.Errorf("第 %d 章没有与当前草稿匹配的 failed 去AI化报告；先调用 check_de_ai: %w", request.Chapter, errs.ErrToolPrecondition)
	}
	content, _, err := t.store.Drafts.LoadChapterContent(request.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load draft: %w: %w", errs.ErrStoreRead, err)
	}
	if content == "" || audit.DraftSHA256 != store.TextSHA256(content) {
		return nil, fmt.Errorf(
			"第 %d 章草稿已在旧审核报告后变化；旧 repair_de_ai_batch 已失效，禁止重试修订。立即对当前草稿调用 check_de_ai；只有新的 check_de_ai 返回 failed 报告后才能再次 repair_de_ai_batch，去AI通过后再统一运行最终 check_consistency: %w",
			request.Chapter, errs.ErrToolPrecondition,
		)
	}
	if _, outside := currentWriterBudgetWindow(t.store, request.Chapter, content); outside {
		return json.Marshal(map[string]any{
			"chapter": request.Chapter, "changed": false, "deferred_to_host": true,
			"word_count": len([]rune(content)),
			"next_step":  "当前进行中草稿超出字数预算，去AI化批量修订未执行。立即结束本轮；Host 会先按唯一行段完成字数恢复，进入预算后再在同一草稿上重新检查去AI化。",
		})
	}

	updated, repairedCount, staleIndices, err := applyDeAIBatch(content, request.Repairs)
	if err != nil {
		return nil, err
	}
	nextStep := "本批已落盘，旧的去AI化、一致性和改编检查均已失效。立即只重跑 check_de_ai。若仍有 repair finding，按下一类别再做一小批修订并继续只复检 check_de_ai；去AI通过后统一运行一次最终 check_consistency，再跑 check_adaptation（如适用）。只有最终提交版本的全部检查都通过，最后才 commit_chapter。"
	if len(staleIndices) > 0 {
		nextStep = "本批中已跳过当前草稿里不再存在的过期 old_string，其余唯一匹配项已安全处理。不要重放旧批次；立即只重跑 check_de_ai。若仍有 repair finding，按最新报告做下一小批精确修订；去AI通过后统一运行一次最终 check_consistency。全部检查必须在同一版草稿上通过后才能 commit_chapter。"
	}
	if repairedCount == 0 {
		return json.Marshal(map[string]any{
			"chapter":               request.Chapter,
			"changed":               false,
			"repaired_count":        0,
			"skipped_stale_count":   len(staleIndices),
			"skipped_stale_indices": staleIndices,
			"next_step":             nextStep,
		})
	}
	if err := t.store.Drafts.SaveDraft(request.Chapter, updated); err != nil {
		return nil, fmt.Errorf("save repaired draft: %w: %w", errs.ErrStoreWrite, err)
	}
	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(request.Chapter), "de_ai_batch_repair", fmt.Sprintf("drafts/%02d.draft.md", request.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint de-AI batch repair: %w: %w", errs.ErrStoreWrite, err)
	}

	return json.Marshal(map[string]any{
		"chapter":               request.Chapter,
		"changed":               true,
		"repaired_count":        repairedCount,
		"skipped_stale_count":   len(staleIndices),
		"skipped_stale_indices": staleIndices,
		"next_step":             nextStep,
	})
}

func applyDeAIBatch(content string, repairs []deAIBatchReplacement) (string, int, []int, error) {
	type patch struct {
		index       int
		start, end  int
		replacement string
	}
	patches := make([]patch, 0, len(repairs))
	staleIndices := make([]int, 0)
	seen := make(map[string]struct{}, len(repairs))
	for index, repair := range repairs {
		if strings.TrimSpace(repair.OldString) == "" || strings.TrimSpace(repair.NewString) == "" {
			return "", 0, nil, fmt.Errorf("repairs[%d] old_string and new_string cannot be empty: %w", index, errs.ErrToolArgs)
		}
		if repair.OldString == repair.NewString {
			return "", 0, nil, fmt.Errorf("repairs[%d] does not change the text: %w", index, errs.ErrToolArgs)
		}
		if _, duplicate := seen[repair.OldString]; duplicate {
			return "", 0, nil, fmt.Errorf("repairs[%d] duplicates an earlier old_string: %w", index, errs.ErrToolArgs)
		}
		seen[repair.OldString] = struct{}{}
		matches := strings.Count(content, repair.OldString)
		if matches == 0 {
			// A model can carry one already-repaired sentence into the next
			// bounded batch. Skipping that stale entry is safe because there is
			// no match to mutate; the mandatory next check decides what remains.
			staleIndices = append(staleIndices, index)
			continue
		}
		if matches != 1 {
			return "", 0, nil, fmt.Errorf("repairs[%d] old_string must match exactly once in the current draft, got %d: %w", index, matches, errs.ErrToolPrecondition)
		}
		start := strings.Index(content, repair.OldString)
		patches = append(patches, patch{
			index:       index,
			start:       start,
			end:         start + len(repair.OldString),
			replacement: repair.NewString,
		})
	}
	sort.Slice(patches, func(left, right int) bool {
		return patches[left].start < patches[right].start
	})
	for index := 1; index < len(patches); index++ {
		previous, current := patches[index-1], patches[index]
		if current.start < previous.end {
			return "", 0, nil, fmt.Errorf("repairs[%d] overlaps repairs[%d]; use one non-overlapping exact replacement: %w", current.index, previous.index, errs.ErrToolArgs)
		}
	}
	sort.Slice(patches, func(left, right int) bool {
		return patches[left].start > patches[right].start
	})
	updated := content
	for _, patch := range patches {
		updated = updated[:patch.start] + patch.replacement + updated[patch.end:]
	}
	return updated, len(patches), staleIndices, nil
}
