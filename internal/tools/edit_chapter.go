package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/voocel/agentcore/schema"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// EditChapterTool 对章节草稿做定点字符串替换，适用于打磨场景。
// 相比 draft_chapter 整章重写，token 节省 10x+。
//
// 落盘契约：只改 drafts/{ch:02d}.draft.md，禁止直接改 chapters/（终稿由 commit_chapter 独占）。
// Seed 语义：drafts 不存在但 chapters 有 → 自动把 chapters 复制到 drafts 作为起点。
// 归属检查：章节已完成时必须在 PendingRewrites 队列中，否则拒绝。
//
// 本工具是 agentcore.EditTool 的薄封装，找-换逻辑（多级容错匹配、diff 输出、行尾/BOM 保留）
// 全部复用上游实现。
type EditChapterTool struct {
	store *store.Store
	edit  *agentcoretools.EditTool
}

const maxChapterBatchEdits = 24

type chapterTextEdit struct {
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

type editChapterRequest struct {
	Chapter       int               `json:"chapter"`
	OldString     string            `json:"old_string"`
	NewString     string            `json:"new_string"`
	ReplaceAll    bool              `json:"replace_all"`
	Edits         []chapterTextEdit `json:"edits"`
	BudgetSegment *int              `json:"budget_segment"`
}

func NewEditChapterTool(s *store.Store) *EditChapterTool {
	return &EditChapterTool{
		store: s,
		edit:  agentcoretools.NewEdit(s.Dir(), nil),
	}
}

func (t *EditChapterTool) Name() string  { return "edit_chapter" }
func (t *EditChapterTool) Label() string { return "编辑章节" }

// ReadOnly 明确声明写工具（配合 ConcurrencySafeTool 防止被并发调度）。
func (t *EditChapterTool) ReadOnly(_ json.RawMessage) bool { return false }

// ConcurrencySafe 显式禁止并发：同章节多次 edit_chapter 并行会读-改-写竞态，
// 即使不同章节并行也会穿插 checkpoint 顺序。统一串行最稳。
func (t *EditChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

// ActivityDescription 供 UI/日志展示当前工具的活动描述。
func (t *EditChapterTool) ActivityDescription(_ json.RawMessage) string { return "编辑章节草稿" }

func (t *EditChapterTool) Description() string {
	return "对章节草稿做定点字符串替换（适合单处、唯一的精确打磨；同类去AI化问题优先用 repair_de_ai_batch 分批处理）。" +
		"找到 old_string 并替换为 new_string，要求精确匹配且唯一（多处匹配需 replace_all=true）。" +
		"写入 drafts/{ch}.draft.md；drafts 不存在时自动从 chapters 播种。" +
		"章节已完成且不在 PendingRewrites 队列中时拒绝执行。单处修改传 old_string/new_string；" +
		"一次回读已确定多处局部修改时，用 edits 一次原子落盘 1-24 处，不要为每处修改重读整章。"
}

func (t *EditChapterTool) Schema() map[string]any {
	edit := schema.Object(
		schema.Property("old_string", schema.String("当前草稿中唯一出现的原文精确片段")).Required(),
		schema.Property("new_string", schema.String("替换后的新文本，可为空以删除冗余段落")).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
		schema.Property("old_string", schema.String("单处替换的原文精确片段；与 edits 二选一")),
		schema.Property("new_string", schema.String("单处替换的新文本，可为空")),
		schema.Property("replace_all", schema.Bool("替换所有匹配（默认 false）")),
		schema.Property("edits", schema.Array("一次回读后确定的 1-24 处不重叠局部替换；与 old_string/new_string 二选一", edit)),
		schema.Property("budget_segment", schema.Int("仅用于 Host 字数恢复分段；原样传回恢复指令给出的非负段号")),
	)
}

func (t *EditChapterTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a editChapterRequest
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	batchMode := len(a.Edits) > 0
	singleMode := a.OldString != "" || a.NewString != "" || a.ReplaceAll
	if batchMode && singleMode {
		return nil, fmt.Errorf("use either edits or old_string/new_string, not both: %w", errs.ErrToolArgs)
	}
	if batchMode && len(a.Edits) > maxChapterBatchEdits {
		return nil, fmt.Errorf("edits must contain 1-%d items: %w", maxChapterBatchEdits, errs.ErrToolArgs)
	}
	if a.BudgetSegment != nil && *a.BudgetSegment < 0 {
		return nil, fmt.Errorf("budget_segment must be >= 0: %w", errs.ErrToolArgs)
	}
	if a.BudgetSegment != nil && !batchMode {
		return nil, fmt.Errorf("budget_segment requires edits batch mode: %w", errs.ErrToolArgs)
	}
	if !batchMode && a.OldString == "" {
		return nil, fmt.Errorf("old_string 不能为空: %w", errs.ErrToolArgs)
	}
	if !batchMode && a.OldString == a.NewString {
		return nil, fmt.Errorf("old_string 与 new_string 相同，无需修改: %w", errs.ErrToolArgs)
	}

	// 归属检查：已完成章节必须在重写队列中，避免污染终稿
	if t.store.Progress.IsChapterCompleted(a.Chapter) {
		progress, _ := t.store.Progress.Load()
		if progress == nil || !slices.Contains(progress.PendingRewrites, a.Chapter) {
			return nil, fmt.Errorf("第 %d 章已完成且不在 PendingRewrites 队列中，不能编辑；需修改请先由 editor 评审触发重写/打磨: %w", a.Chapter, errs.ErrToolPrecondition)
		}
	}

	// Seed：drafts 不存在时从 chapters 复制一份作为起点
	if err := t.ensureDraft(a.Chapter); err != nil {
		return nil, err
	}
	current, err := t.store.Drafts.LoadDraft(a.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load current draft before edit: %w: %w", errs.ErrStoreRead, err)
	}
	_, outsideWordBudget := currentWriterBudgetWindow(t.store, a.Chapter, current)
	if a.BudgetSegment == nil && outsideWordBudget {
		return json.Marshal(map[string]any{
			"chapter":          a.Chapter,
			"changed":          false,
			"word_count":       len([]rune(current)),
			"deferred_to_host": true,
			"next_step":        "当前进行中草稿超出字数预算，未执行非分段编辑。立即结束本轮；Host 将指定唯一行段与 budget_segment 后再派发局部编辑。",
		})
	}
	var skippedBudgetEdits []int
	if a.BudgetSegment != nil {
		window, outside := currentWriterBudgetWindow(t.store, a.Chapter, current)
		if !outside || *a.BudgetSegment != window.Segment {
			return json.Marshal(map[string]any{
				"chapter": a.Chapter, "changed": false, "word_count": len([]rune(current)),
				"deferred_to_host": true, "expected_budget_segment": window.Segment,
				"next_step": "budget_segment 不是 Host 当前指定的唯一恢复段，未执行修改。立即结束本轮，由 Host 重新派发。",
			})
		}
		lines := strings.Split(current, "\n")
		segmentText := strings.Join(lines[window.FromLine-1:window.ToLine], "\n")
		validEdits := make([]chapterTextEdit, 0, len(a.Edits))
		for index, edit := range a.Edits {
			if strings.Count(segmentText, edit.OldString) != 1 {
				skippedBudgetEdits = append(skippedBudgetEdits, index)
				continue
			}
			validEdits = append(validEdits, edit)
		}
		if len(validEdits) == 0 {
			return json.Marshal(map[string]any{
				"chapter": a.Chapter, "changed": false, "word_count": len([]rune(current)),
				"deferred_to_host": true, "expected_budget_segment": window.Segment,
				"skipped_budget_edit_indices": skippedBudgetEdits,
				"next_step":                   "没有任何 old_string 在 Host 当前指定行段内唯一精确匹配，未执行修改。立即结束本轮，由 Host 重新派发。",
			})
		}
		a.Edits = validEdits
	}
	var skippedStaleEdits []int
	if batchMode && a.BudgetSegment == nil && t.pendingPolishChapter(a.Chapter) {
		validEdits, skipped, filterErr := filterStalePolishEdits(current, a.Edits)
		if filterErr != nil {
			return nil, filterErr
		}
		skippedStaleEdits = skipped
		if len(validEdits) == 0 {
			return json.Marshal(map[string]any{
				"chapter":                    a.Chapter,
				"changed":                    false,
				"deferred_to_host":           true,
				"skipped_stale_edit_indices": skippedStaleEdits,
				"skipped_stale_edit_count":   len(skippedStaleEdits),
				"word_count":                 len([]rune(current)),
				"next_step":                  "本批 old_string 均已过期或不唯一，未修改草稿。不要重放本批；立即结束本轮，由 Host 从持久化草稿重新派发一次干净的打磨上下文。",
			})
		}
		a.Edits = validEdits
	}
	if batchMode {
		return t.executeBatch(a.Chapter, current, a.Edits, a.BudgetSegment, skippedBudgetEdits, skippedStaleEdits)
	}
	// Recovery and context compaction can make a weak model repeat the exact
	// same patch. Treat only a provably completed replacement as an idempotent
	// no-op: the old text is gone and the complete non-empty new text is already
	// present. All other mismatches still reach EditTool and remain hard errors.
	if a.NewString != "" && !strings.Contains(current, a.OldString) && strings.Contains(current, a.NewString) {
		payload := map[string]any{
			"chapter":         a.Chapter,
			"already_applied": true,
			"changed":         false,
			"message":         "相同的局部修改已存在于当前草稿，无需重复写入。",
			"next_step":       t.nextStepAfterEdit(),
		}
		t.addDraftStatus(payload, a.Chapter)
		return json.Marshal(payload)
	}
	replacementCount := 1
	if a.ReplaceAll {
		replacementCount = -1
	}
	if rejected := t.rejectRunawayEdit(a.Chapter, current, strings.Replace(current, a.OldString, a.NewString, replacementCount)); rejected != nil {
		return json.Marshal(rejected)
	}
	// 委托 agentcore.EditTool 完成找-换
	subArgs, _ := json.Marshal(map[string]any{
		"path":        fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
		"file_path":   fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
		"old_text":    a.OldString,
		"old_string":  a.OldString,
		"new_text":    a.NewString,
		"new_string":  a.NewString,
		"replace_all": a.ReplaceAll,
	})
	result, err := t.edit.Execute(ctx, subArgs)
	if err != nil {
		return nil, fmt.Errorf("apply edit: %w: %w", errs.ErrToolPrecondition, err)
	}
	if err := t.syncEditedDraft(a.Chapter); err != nil {
		return nil, err
	}

	if err := t.checkpointEdit(a.Chapter); err != nil {
		return nil, err
	}

	// 附加指引：让 writer 知道后续步骤，避免遗漏 check_consistency / commit_chapter
	var passthrough map[string]any
	if err := json.Unmarshal(result, &passthrough); err != nil {
		return result, nil
	}
	passthrough["chapter"] = a.Chapter
	passthrough["next_step"] = t.nextStepAfterEdit()
	t.addDraftStatus(passthrough, a.Chapter)
	return json.Marshal(passthrough)
}

func (t *EditChapterTool) executeBatch(
	chapter int,
	current string,
	edits []chapterTextEdit,
	budgetSegment *int,
	skippedBudgetEdits []int,
	skippedStaleEdits []int,
) (json.RawMessage, error) {
	updated, changed, alreadyApplied, err := applyChapterEditBatch(current, edits)
	if err != nil {
		return nil, err
	}
	if budgetSegment != nil && changed > 0 {
		window, outside := currentWriterBudgetWindow(t.store, chapter, current)
		before := wordBudgetDistance(window.WordCount, window.MinWords, window.MaxWords)
		after := wordBudgetDistance(len([]rune(updated)), window.MinWords, window.MaxWords)
		if !outside || after >= before {
			return json.Marshal(map[string]any{
				"chapter": chapter, "changed": false, "word_count": len([]rune(current)),
				"deferred_to_host": true, "expected_budget_segment": window.Segment,
				"next_step": "本段修改没有缩小与字数预算的距离，未执行任何落盘。立即结束本轮，由 Host 重新派发当前行段。",
			})
		}
	}
	if budgetSegment == nil {
		if rejected := t.rejectRunawayEdit(chapter, current, updated); rejected != nil {
			return json.Marshal(rejected)
		}
	}
	payload := map[string]any{
		"chapter":               chapter,
		"changed":               changed > 0,
		"edit_count":            changed,
		"already_applied_count": alreadyApplied,
		"next_step":             t.nextStepAfterEdit(),
	}
	if len(skippedBudgetEdits) > 0 {
		payload["skipped_budget_edit_indices"] = skippedBudgetEdits
		payload["skipped_budget_edit_count"] = len(skippedBudgetEdits)
	}
	if len(skippedStaleEdits) > 0 {
		payload["skipped_stale_edit_indices"] = skippedStaleEdits
		payload["skipped_stale_edit_count"] = len(skippedStaleEdits)
		payload["message"] = "已安全应用当前草稿中唯一匹配的修改，并跳过过期或不唯一的编辑项；不要重放被跳过的项。"
	}
	if changed == 0 {
		payload["already_applied"] = true
		payload["message"] = "这一批局部修改已全部存在于当前草稿，无需重复写入。"
		if budgetSegment != nil {
			payload["budget_segment"] = *budgetSegment
			if err := t.checkpointEdit(chapter, fmt.Sprintf("word_budget_edit_segment_%d", *budgetSegment)); err != nil {
				return nil, err
			}
		}
		t.addDraftStatus(payload, chapter)
		return json.Marshal(payload)
	}
	if err := t.store.Drafts.SaveDraft(chapter, updated); err != nil {
		return nil, fmt.Errorf("save batch-edited draft: %w: %w", errs.ErrStoreWrite, err)
	}
	checkpointStep := "edit"
	if budgetSegment != nil {
		checkpointStep = fmt.Sprintf("word_budget_edit_segment_%d", *budgetSegment)
		payload["budget_segment"] = *budgetSegment
	}
	if err := t.checkpointEdit(chapter, checkpointStep); err != nil {
		return nil, err
	}
	t.addDraftStatus(payload, chapter)
	return json.Marshal(payload)
}

func (t *EditChapterTool) pendingPolishChapter(chapter int) bool {
	if t == nil || t.store == nil {
		return false
	}
	progress, err := t.store.Progress.Load()
	return err == nil && progress != nil && progress.Flow == domain.FlowPolishing &&
		slices.Contains(progress.PendingRewrites, chapter)
}

func filterStalePolishEdits(content string, edits []chapterTextEdit) ([]chapterTextEdit, []int, error) {
	valid := make([]chapterTextEdit, 0, len(edits))
	skipped := make([]int, 0)
	seen := make(map[string]struct{}, len(edits))
	for index, edit := range edits {
		if edit.OldString == "" {
			return nil, nil, fmt.Errorf("edits[%d].old_string cannot be empty: %w", index, errs.ErrToolArgs)
		}
		if edit.OldString == edit.NewString {
			return nil, nil, fmt.Errorf("edits[%d] does not change the text: %w", index, errs.ErrToolArgs)
		}
		if _, duplicate := seen[edit.OldString]; duplicate {
			return nil, nil, fmt.Errorf("edits[%d] duplicates an earlier old_string: %w", index, errs.ErrToolArgs)
		}
		seen[edit.OldString] = struct{}{}

		matches := strings.Count(content, edit.OldString)
		alreadyApplied := matches == 0 && edit.NewString != "" && strings.Contains(content, edit.NewString)
		if matches == 1 || alreadyApplied {
			valid = append(valid, edit)
			continue
		}
		skipped = append(skipped, index)
	}
	return valid, skipped, nil
}

func applyChapterEditBatch(content string, edits []chapterTextEdit) (string, int, int, error) {
	type patch struct {
		item        int
		start, end  int
		replacement string
	}
	patches := make([]patch, 0, len(edits))
	alreadyApplied := 0
	seen := make(map[string]struct{}, len(edits))
	for index, edit := range edits {
		if edit.OldString == "" {
			return "", 0, 0, fmt.Errorf("edits[%d].old_string cannot be empty: %w", index, errs.ErrToolArgs)
		}
		if edit.OldString == edit.NewString {
			return "", 0, 0, fmt.Errorf("edits[%d] does not change the text: %w", index, errs.ErrToolArgs)
		}
		if _, duplicate := seen[edit.OldString]; duplicate {
			return "", 0, 0, fmt.Errorf("edits[%d] duplicates an earlier old_string: %w", index, errs.ErrToolArgs)
		}
		seen[edit.OldString] = struct{}{}
		matches := strings.Count(content, edit.OldString)
		if matches == 0 && edit.NewString != "" && strings.Contains(content, edit.NewString) {
			alreadyApplied++
			continue
		}
		if matches != 1 {
			return "", 0, 0, fmt.Errorf("edits[%d].old_string must match exactly once in the current draft, got %d: %w", index, matches, errs.ErrToolPrecondition)
		}
		start := strings.Index(content, edit.OldString)
		patches = append(patches, patch{item: index, start: start, end: start + len(edit.OldString), replacement: edit.NewString})
	}
	sort.Slice(patches, func(left, right int) bool { return patches[left].start < patches[right].start })
	for index := 1; index < len(patches); index++ {
		previous, current := patches[index-1], patches[index]
		if current.start < previous.end {
			return "", 0, 0, fmt.Errorf("edits[%d] overlaps edits[%d]: %w", current.item, previous.item, errs.ErrToolArgs)
		}
	}
	sort.Slice(patches, func(left, right int) bool { return patches[left].start > patches[right].start })
	updated := content
	for _, patch := range patches {
		updated = updated[:patch.start] + patch.replacement + updated[patch.end:]
	}
	return updated, len(patches), alreadyApplied, nil
}

func (t *EditChapterTool) checkpointEdit(chapter int, step ...string) error {
	checkpointStep := "edit"
	if len(step) > 0 && step[0] != "" {
		checkpointStep = step[0]
	}
	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(chapter), checkpointStep, fmt.Sprintf("drafts/%02d.draft.md", chapter),
	); err != nil {
		return fmt.Errorf("checkpoint edit: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}

func (t *EditChapterTool) addDraftStatus(payload map[string]any, chapter int) {
	content, wordCount, err := t.store.Drafts.LoadChapterContent(chapter)
	if err != nil || content == "" {
		return
	}
	payload["word_count"] = wordCount
	progress, err := t.store.Progress.Load()
	if err != nil || progress == nil || progress.Flow == domain.FlowPolishing || len(progress.PendingRewrites) > 0 {
		return
	}
	_, policy, ok, err := t.store.ChapterWordBudgetPolicy(progress, chapter)
	if err != nil || !ok {
		return
	}
	minWords := policy.HardMinWords
	maxWords := policy.HardMaxWords
	payload["word_budget"] = map[string]any{
		"safety_min_words":      minWords,
		"safety_max_words":      maxWords,
		"recommended_min_words": policy.RecommendedMinWords,
		"recommended_max_words": policy.RecommendedMaxWords,
	}
	payload["word_budget_recommended"] = policy.WithinRecommendation(wordCount)
	withinBudget := policy.WithinHardRange(wordCount)
	payload["runaway_safety_passed"] = withinBudget
	if segment, segmented := payload["budget_segment"]; segmented {
		if withinBudget {
			payload["next_step"] = fmt.Sprintf("字数分段 %v 已保存，当前草稿已进入预算。立即结束本轮；Host 将派发同一草稿的完整质量校验。", segment)
		} else {
			payload["next_step"] = fmt.Sprintf("字数分段 %v 已保存，当前草稿仍未进入预算。立即结束本轮；不要回读或继续编辑，Host 将派发下一行段。", segment)
		}
		return
	}
	if withinBudget {
		return
	}
	payload["next_step"] = fmt.Sprintf(
		"当前草稿 %d 字，仍不在 %d-%d 字区间。立即结束本轮，不要重新 read_chapter 或继续 edit_chapter；Host 将按行段派发下一次局部修复，保留关键情节、人物选择、情感落点和章末钩子。进入区间后再执行各项检查。",
		wordCount, minWords, maxWords,
	)
}

func (t *EditChapterTool) rejectRunawayEdit(chapter int, current, updated string) map[string]any {
	progress, err := t.store.Progress.Load()
	if err != nil || progress == nil || progress.Flow == domain.FlowPolishing || len(progress.PendingRewrites) > 0 {
		return nil
	}
	_, policy, ok, err := t.store.ChapterWordBudgetPolicy(progress, chapter)
	if err != nil || !ok {
		return nil
	}
	before := len([]rune(current))
	after := len([]rune(updated))
	if !policy.WithinHardRange(before) || policy.WithinHardRange(after) {
		return nil
	}
	return map[string]any{
		"chapter": chapter, "changed": false, "word_count": before,
		"candidate_word_count": after, "runaway_edit_rejected": true,
		"next_step": "本次局部修复会把原本正常的草稿推过生成后异常膨胀围栏，未落盘。缩小替换范围并保持局部修改，或立即结束本轮由 Host 重新派发。",
	}
}

func (t *EditChapterTool) nextStepAfterEdit() string {
	if t.store != nil && t.store.Adaptation.Active() {
		return "edit 已落盘，旧 check_consistency/check_adaptation 已失效；旧 check_de_ai 也已失效。先重新调用 check_consistency 和 check_adaptation；若任一检查还要求改稿，重复本轮直到它们在同一版草稿上通过。随后必须调用 check_de_ai；仍有 finding 时同类问题用 repair_de_ai_batch 做一小批精确修订并立即复检。去AI化通过后再次重跑 check_consistency 和 check_adaptation；任何后续改稿都会使去AI报告失效，必须重新 check_de_ai。只有同一版草稿全部通过才能 commit_chapter。"
	}
	return "edit 已落盘，旧 check_consistency/check_de_ai 均已失效。先重新调用 check_consistency；若它要求改稿，重复本轮直到通过。随后必须调用 check_de_ai，仍有 finding 时用 repair_de_ai_batch 做一小批精确修订并立即复检；去AI化通过后再次 check_consistency。任何后续改稿都会使去AI报告失效，必须重新 check_de_ai。只有同一版草稿全部通过才能 commit_chapter。"
}

// ensureDraft 保证 drafts/{ch}.draft.md 存在：
//   - 已有草稿 → 直接返回
//   - 无草稿但有终稿 → 把终稿复制到 drafts 作为修改起点（常见于打磨场景）
//   - 都没有 → 报错，提示先用 draft_chapter 创建初稿
func (t *EditChapterTool) ensureDraft(chapter int) error {
	draft, err := t.store.Drafts.LoadDraft(chapter)
	if err != nil {
		return fmt.Errorf("load draft: %w: %w", errs.ErrStoreRead, err)
	}
	if draft != "" {
		return t.saveEditableDraft(chapter, draft)
	}
	text, err := t.store.Drafts.LoadChapterText(chapter)
	if err != nil {
		return fmt.Errorf("load chapter: %w: %w", errs.ErrStoreRead, err)
	}
	if text == "" {
		return fmt.Errorf("第 %d 章无草稿也无终稿，请先调 draft_chapter(mode=write, chapter=%d) 创建初稿: %w", chapter, chapter, errs.ErrToolPrecondition)
	}
	if err := t.store.Drafts.SaveDraft(chapter, text); err != nil {
		return fmt.Errorf("seed draft from chapter: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}

func (t *EditChapterTool) saveEditableDraft(chapter int, content string) error {
	if err := t.store.Drafts.SaveDraft(chapter, content); err != nil {
		return fmt.Errorf("prepare editable draft: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}

func (t *EditChapterTool) syncEditedDraft(chapter int) error {
	path := filepath.Join(t.store.Dir(), "drafts", fmt.Sprintf("%02d.draft.md", chapter))
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read edited draft: %w: %w", errs.ErrStoreRead, err)
	}
	if err := t.store.Drafts.SaveDraft(chapter, string(content)); err != nil {
		return fmt.Errorf("sync edited draft: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}
