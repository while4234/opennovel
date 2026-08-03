package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// DraftChapterTool 写入整章草稿，替代旧的 write_scene + polish_chapter 流水线。
// Agent 自主决定一次写完还是分批续写。
type DraftChapterTool struct {
	store *store.Store
}

func NewDraftChapterTool(store *store.Store) *DraftChapterTool {
	return &DraftChapterTool{store: store}
}

func (t *DraftChapterTool) Name() string { return "draft_chapter" }
func (t *DraftChapterTool) Description() string {
	return "写入章节正文。首次写作前必须按 working_memory.word_budget.current_chapter 的 recommended_min_words/recommended_max_words 分配各场景篇幅并预留余量，不要先生成超长正文再依赖删减。mode=write 覆盖写入整章，mode=append 追加到现有草稿（续写/修改）"
}
func (t *DraftChapterTool) Label() string { return "写入章节" }

// 写工具，禁止并发（读-改-写竞态）。
func (t *DraftChapterTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *DraftChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *DraftChapterTool) Schema() map[string]any {
	// mode 标 required 是为了兼容 OpenAI strict tool calling——strict 模式
	// 要求所有 properties 都在 required 列表中。原来的"省略 mode 走 write
	// 默认"行为现在需要模型显式传 mode="write"，Execute 的 default 分支不变。
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
		schema.Property("content", schema.String("章节正文")).Required(),
		schema.Property("mode", schema.Enum("写入模式", "write", "append")).Required(),
	)
}

// StrictSchema 启用 OpenAI 的 strict tool calling，让模型必须严格遵守
// schema：所有 required 字段必填，arguments 不能"提前 EOT"出现空对象。
// litellm 透传 strict 字段；OpenAI / xAI 等支持的后端会强制执行，其他后端
// 按 HTTP/JSON 惯例忽略未知字段。Anthropic/Gemini/Bedrock 走各自的转换链路
// 自然不会看到这个字段。
func (t *DraftChapterTool) StrictSchema() bool { return true }

func (t *DraftChapterTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter            int    `json:"chapter"`
		Content            string `json:"content"`
		Mode               string `json:"mode"`
		ReplaceOutOfBudget bool   `json:"replace_out_of_budget"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if a.Content == "" {
		return nil, fmt.Errorf("content must not be empty: %w", errs.ErrToolArgs)
	}
	if issue := repeatedDraftContentIssue(a.Content); issue != "" {
		return json.Marshal(repeatedDraftRejection(a.Chapter, a.Mode, issue))
	}
	if err := t.store.Progress.ValidateChapterWork(a.Chapter); err != nil {
		return nil, err
	}
	if err := EnsureAdaptationChapterPlanned(t.store, a.Chapter); err != nil {
		return nil, err
	}
	if err := EnsureChapterExpanded(t.store, a.Chapter); err != nil {
		return nil, err
	}
	backupPath := ""
	if existing, loadErr := t.store.Drafts.LoadDraft(a.Chapter); loadErr == nil && existing != "" {
		if window, outside := currentWriterBudgetWindow(t.store, a.Chapter, existing); outside {
			candidateCount := utf8.RuneCountInString(a.Content)
			candidateInBudget := candidateCount >= window.MinWords && candidateCount <= window.MaxWords
			if a.ReplaceOutOfBudget && a.Mode == "write" && candidateInBudget {
				var backupErr error
				backupPath, backupErr = t.store.Drafts.BackupDraftForRecovery(a.Chapter, existing)
				if backupErr != nil {
					return nil, fmt.Errorf("backup out-of-budget draft: %w", backupErr)
				}
			} else {
				nextStep := "当前进行中草稿超出字数预算，未覆盖或追加正文。立即结束本轮；Host 会选择局部字数恢复或干净上下文重生成。"
				if a.ReplaceOutOfBudget {
					nextStep = fmt.Sprintf(
						"重生成候选稿为 %d 字，未进入 %d-%d 字安全区间；旧草稿未被替换。立即结束本轮，由 Host 使用新的干净 Writer 重试。",
						candidateCount, window.MinWords, window.MaxWords,
					)
				}
				return json.Marshal(map[string]any{
					"chapter": a.Chapter, "written": false, "deferred_to_host": true,
					"word_count": candidateCount, "existing_word_count": len([]rune(existing)),
					"candidate_rejected": a.ReplaceOutOfBudget,
					"next_step":          nextStep,
				})
			}
		}
	}
	if t.store.Progress.IsChapterCompleted(a.Chapter) {
		// 打磨/重写路径：章节虽已完成，但仍在 pending_rewrites 中，允许覆盖草稿
		progress, _ := t.store.Progress.Load()
		inRewriteQueue := progress != nil && slices.Contains(progress.PendingRewrites, a.Chapter)
		if !inRewriteQueue {
			return json.Marshal(map[string]any{
				"chapter":   a.Chapter,
				"skipped":   true,
				"completed": true,
				"reason":    fmt.Sprintf("第 %d 章已提交完成，不能覆盖", a.Chapter),
			})
		}
	}
	if err := t.store.Progress.StartChapter(a.Chapter); err != nil {
		return nil, fmt.Errorf("mark chapter in progress: %w", err)
	}

	switch a.Mode {
	case "append":
		if err := t.store.Drafts.AppendDraft(a.Chapter, a.Content); err != nil {
			return nil, fmt.Errorf("append draft: %w", err)
		}
		full, err := t.store.Drafts.LoadDraft(a.Chapter)
		if err != nil {
			return nil, fmt.Errorf("load draft after append: %w", err)
		}
		if _, err := t.store.Checkpoints.AppendArtifact(
			domain.ChapterScope(a.Chapter), "draft",
			fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
		); err != nil {
			return nil, fmt.Errorf("checkpoint draft: %w", err)
		}
		return json.Marshal(t.buildDraftResult(a.Chapter, "append", utf8.RuneCountInString(full)))
	default: // write
		if err := t.store.Drafts.SaveDraft(a.Chapter, a.Content); err != nil {
			return nil, fmt.Errorf("save draft: %w", err)
		}
		if _, err := t.store.Checkpoints.AppendArtifact(
			domain.ChapterScope(a.Chapter), "draft",
			fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
		); err != nil {
			return nil, fmt.Errorf("checkpoint draft: %w", err)
		}
		result := t.buildDraftResult(a.Chapter, "write", utf8.RuneCountInString(a.Content))
		if backupPath != "" {
			result["replaced_out_of_budget"] = true
			result["recovery_backup"] = backupPath
		}
		return json.Marshal(result)
	}
}

func (t *DraftChapterTool) buildDraftResult(chapter int, mode string, wordCount int) map[string]any {
	result := map[string]any{
		"written":    true,
		"chapter":    chapter,
		"mode":       mode,
		"word_count": wordCount,
		"next_step":  t.validationNextStep(),
	}
	t.addNormalWordBudgetStatus(result, chapter, wordCount)
	contract, issues, ok := adaptationWordContractStatus(t.store, chapter, wordCount)
	if !ok {
		return result
	}
	result["adaptation_word_contract"] = contract
	result["word_contract_passed"] = len(issues) == 0
	if len(contract.Warnings) > 0 {
		result["word_contract_warnings"] = contract.Warnings
	}
	if len(issues) == 0 && normalWordBudgetAllowsDraftNextStep(result) {
		if contract.Hard {
			result["next_step"] = "字数硬契约已满足：" + t.validationNextStep()
		}
	}
	if len(issues) > 0 {
		result["word_contract_issues"] = issues
		if repair := adaptationWordContractRepairStep(contract, issues, chapter); repair != "" {
			result["next_step"] = repair
		}
	}
	if qualityIssues, ok := adaptationDraftQualityStatus(t.store, chapter, loadDraftTextForQuality(t.store, chapter)); ok && len(qualityIssues) > 0 {
		result["adaptation_quality_passed"] = false
		result["adaptation_quality_issues"] = qualityIssues
		if repair := adaptationQualityRepairStep(qualityIssues, chapter); repair != "" {
			result["next_step"] = repair
		}
	}
	return result
}

func (t *DraftChapterTool) validationNextStep() string {
	if t.store != nil && t.store.Adaptation.Active() {
		return "先 read_chapter(source=\"draft\") 回读当前草稿，再调用 check_consistency 和 check_adaptation；两项均针对当前草稿通过后才能 commit_chapter。任何后续修改都会使旧校验失效。"
	}
	return "先 read_chapter(source=\"draft\") 回读草稿，再调用 check_consistency，最后 commit_chapter"
}

func normalWordBudgetAllowsDraftNextStep(result map[string]any) bool {
	passed, ok := result["runaway_safety_passed"].(bool)
	return !ok || passed
}

func repeatedDraftRejection(chapter int, mode string, issue string) map[string]any {
	if mode == "" {
		mode = "write"
	}
	return map[string]any{
		"written":                 false,
		"chapter":                 chapter,
		"mode":                    mode,
		"repeated_draft_rejected": true,
		"reason":                  fmt.Sprintf("draft_chapter content appears to repeat existing prose (%s)", issue),
		"next_step": fmt.Sprintf(
			"不要追加或重复提交同一段正文。请调用 draft_chapter(mode=\"write\", chapter=%d) 进行干净的整章重写，删除重复句，并满足本章字数预算后再 read_chapter/check_consistency/commit_chapter。",
			chapter,
		),
	}
}

func (t *DraftChapterTool) addNormalWordBudgetStatus(result map[string]any, chapter int, wordCount int) {
	if t.store == nil {
		return
	}
	progress, err := t.store.Progress.Load()
	if err != nil {
		return
	}
	runtime, policy, ok, err := t.store.ChapterWordBudgetPolicy(progress, chapter)
	if err != nil || !ok {
		return
	}
	result["word_budget"] = map[string]any{
		"safety_min_words":       policy.HardMinWords,
		"safety_max_words":       policy.HardMaxWords,
		"range_policy":           "post_draft_runaway_review",
		"recommended_min_words":  policy.RecommendedMinWords,
		"recommended_max_words":  policy.RecommendedMaxWords,
		"target_total_words":     runtime.Target.TargetTotalWords,
		"completed_words":        runtime.Progress.CompletedWords,
		"remaining_target_words": runtime.Remaining.TargetWords,
		"remaining_chapters":     runtime.Remaining.Chapters,
	}
	result["word_budget_recommended"] = policy.WithinRecommendation(wordCount)
	if policy.WithinHardRange(wordCount) {
		result["runaway_safety_passed"] = true
		return
	}
	result["runaway_safety_passed"] = false
	result["deferred_to_host"] = true
	direction := "低于"
	if wordCount > policy.HardMaxWords {
		direction = "高于"
	}
	result["word_budget_issues"] = []string{
		fmt.Sprintf("第 %d 章草稿%s异常膨胀安全范围：当前 %d 字，安全范围 %d-%d 字；用户篇幅设置本身是软目标。", chapter, direction, wordCount, policy.HardMinWords, policy.HardMaxWords),
	}
	result["next_step"] = fmt.Sprintf(
		"当前草稿已经保存，但超出 %d-%d 字的异常膨胀安全范围。立即结束本轮，不要调用 read_chapter、edit_chapter、commit_chapter，不要再次调用 draft_chapter 或整章重写。Host 会按行段逐段派发局部修复，每段保留章节契约、关键情节、人物选择、情感落点和章末钩子；进入安全范围后再在同一草稿上执行完整质量校验。",
		policy.HardMinWords, policy.HardMaxWords,
	)
}

func loadDraftTextForQuality(st *store.Store, chapter int) string {
	if st == nil || chapter <= 0 {
		return ""
	}
	text, err := st.Drafts.LoadDraft(chapter)
	if err != nil {
		return ""
	}
	return text
}

func repeatedDraftContentIssue(content string) string {
	sentences := splitDraftSentences(content)
	seen := map[string]int{}
	repeatedSentences := 0
	repeatedRunes := 0
	for _, sentence := range sentences {
		normalized := normalizeDraftSentence(sentence)
		runes := utf8.RuneCountInString(normalized)
		if runes < 24 {
			continue
		}
		seen[normalized]++
		if seen[normalized] > 1 {
			repeatedSentences++
			repeatedRunes += runes
		}
	}
	if repeatedSentences >= 3 || repeatedRunes >= 180 {
		return fmt.Sprintf("%d repeated long sentence(s), about %d repeated characters", repeatedSentences, repeatedRunes)
	}
	return ""
}

func splitDraftSentences(content string) []string {
	var sentences []string
	var current strings.Builder
	for _, r := range content {
		current.WriteRune(r)
		switch r {
		case '。', '！', '？', '；', '.', '!', '?', ';', '\n':
			if sentence := strings.TrimSpace(current.String()); sentence != "" {
				sentences = append(sentences, sentence)
			}
			current.Reset()
		}
	}
	if sentence := strings.TrimSpace(current.String()); sentence != "" {
		sentences = append(sentences, sentence)
	}
	return sentences
}

func normalizeDraftSentence(sentence string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(sentence)), "")
}
