package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// ReadChapterTool 读取章节原文，让 Agent 能回读自己和前文的文字。
type ReadChapterTool struct {
	store *store.Store
}

func NewReadChapterTool(store *store.Store) *ReadChapterTool {
	return &ReadChapterTool{store: store}
}

func (t *ReadChapterTool) Name() string { return "read_chapter" }
func (t *ReadChapterTool) Description() string {
	return "读取章节正文。可读终稿、草稿；改编模式下 source=source 可读取原文章节快照；也可提取角色对话片段"
}
func (t *ReadChapterTool) Label() string { return "读取章节" }

// 纯读工具，可被并发调度（editor 审阅时常一次读多章）。
func (t *ReadChapterTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *ReadChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *ReadChapterTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号（读单章时必填）")),
		schema.Property("from", schema.Int("起始章节号（读范围时使用）")),
		schema.Property("to", schema.Int("结束章节号（读范围时使用）")),
		schema.Property("source", schema.Enum("来源", "final", "draft", "source")).Required(),
		schema.Property("character", schema.String("角色名（提取对话片段时使用）")),
		schema.Property("from_line", schema.Int("仅限 Host 明确发出的字数超限恢复；单章草稿分段读取起始行（1-based）")),
		schema.Property("to_line", schema.Int("仅限 Host 明确发出的字数超限恢复；单章草稿分段读取结束行（包含该行）")),
		schema.Property("max_runes", schema.Int("最大字符数；范围读取时表示每章上限，单章读取时表示该章上限；不传则不截断章节")),
		schema.Property("max_total_runes", schema.Int("范围读取总字符预算；只在章节边界处分批，单章超预算时返回该整章作为独立批次")),
	)
}

func (t *ReadChapterTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter       int    `json:"chapter"`
		From          int    `json:"from"`
		To            int    `json:"to"`
		Source        string `json:"source"`
		Character     string `json:"character"`
		FromLine      int    `json:"from_line"`
		ToLine        int    `json:"to_line"`
		MaxRunes      int    `json:"max_runes"`
		MaxTotalRunes int    `json:"max_total_runes"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	// 模式 1：提取角色对话
	if a.Character != "" {
		chars, _ := t.store.Characters.Load()
		var aliases []string
		for _, c := range chars {
			if c.Name == a.Character {
				aliases = c.Aliases
				break
			}
		}
		var maxCompleted int
		if p, _ := t.store.Progress.Load(); p != nil {
			maxCompleted = maxCompletedChapter(p.CompletedChapters)
		}
		samples := t.store.Drafts.ExtractDialogue(a.Character, aliases, 8, maxCompleted)
		result := map[string]any{
			"character": a.Character,
			"samples":   samples,
		}
		if len(samples) == 0 {
			result["hint"] = "该角色暂无对话样本，无需重试，直接进入下一步"
		}
		return json.Marshal(result)
	}

	// 模式 2：范围读取
	if a.From > 0 && a.To > 0 {
		var load func(int) (string, error)
		if a.Source == "source" {
			if !t.store.Adaptation.Active() {
				return nil, fmt.Errorf("当前项目不是改编模式，无法读取 source 章节")
			}
			load = func(chapter int) (string, error) {
				text, _, err := t.store.Adaptation.LoadSourceChapter(chapter)
				return text, err
			}
		} else {
			load = t.store.Drafts.LoadChapterText
		}
		maxRunes := a.MaxRunes
		if a.MaxTotalRunes > 0 {
			maxRunes = 0
		}
		payload, err := buildChapterRangePayload(a.From, a.To, maxRunes, a.MaxTotalRunes, load)
		if err != nil {
			return nil, fmt.Errorf("load chapter range: %w", err)
		}
		return json.Marshal(payload)
	}

	// 模式 3：单章读取
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter is required")
	}

	var content string
	var title string
	loadedDraft := false
	var err error
	switch a.Source {
	case "source":
		if !t.store.Adaptation.Active() {
			return nil, fmt.Errorf("当前项目不是改编模式，无法读取 source 章节")
		}
		var src *domain.AdaptationSource
		content, src, err = t.store.Adaptation.LoadSourceChapter(a.Chapter)
		if src != nil {
			title = src.Title
		}
	case "draft":
		content, err = t.store.Drafts.LoadDraft(a.Chapter)
		loadedDraft = true
	default: // final
		content, err = t.store.Drafts.LoadChapterText(a.Chapter)
		if err == nil && content == "" {
			slog.Warn("read_chapter 读取终稿为空，回退到草稿", "module", "tool", "chapter", a.Chapter)
			content, err = t.store.Drafts.LoadDraft(a.Chapter)
			loadedDraft = true
		}
	}
	if err != nil {
		return nil, fmt.Errorf("read chapter %d: %w", a.Chapter, err)
	}
	if content == "" {
		return json.Marshal(map[string]any{
			"chapter": a.Chapter,
			"exists":  false,
			"hint":    "该章节尚未写入，如需写作请先调用 draft_chapter",
		})
	}
	fullContent := content
	originalRunes := len([]rune(fullContent))
	budgetWindow, outOfBudgetDraft := currentWriterBudgetWindow(t.store, a.Chapter, fullContent)
	outOfBudgetDraft = loadedDraft && outOfBudgetDraft
	segmentFrom, segmentTo := 0, 0
	if a.FromLine > 0 || a.ToLine > 0 {
		if !outOfBudgetDraft {
			return nil, fmt.Errorf("from_line/to_line are only available for the current out-of-budget draft")
		}
		if a.FromLine != budgetWindow.FromLine || a.ToLine != budgetWindow.ToLine || a.MaxRunes > 0 {
			return json.Marshal(map[string]any{
				"chapter": a.Chapter, "source": a.Source, "word_count": originalRunes,
				"deferred_to_host":        true,
				"expected_budget_segment": budgetWindow.Segment,
				"expected_from_line":      budgetWindow.FromLine, "expected_to_line": budgetWindow.ToLine,
				"next_step": "请求的行段不是 Host 当前指定的唯一字数恢复段，未返回任何正文。立即结束本轮，由 Host 重新派发。",
			})
		}
		lines := strings.Split(fullContent, "\n")
		segmentFrom = a.FromLine
		segmentTo = a.ToLine
		content = strings.Join(lines[segmentFrom-1:segmentTo], "\n")
	} else if outOfBudgetDraft {
		return json.Marshal(map[string]any{
			"chapter":          a.Chapter,
			"source":           a.Source,
			"word_count":       originalRunes,
			"deferred_to_host": true,
			"next_step":        "当前进行中草稿超出字数预算，整章回读未执行。立即结束本轮；Host 将指定唯一行段后再派发读取与局部编辑。",
		})
	}
	var truncated bool
	if a.MaxRunes > 0 {
		content, truncated = truncateReadText(content, a.MaxRunes)
	}

	result := map[string]any{
		"chapter":        a.Chapter,
		"title":          title,
		"source":         a.Source,
		"content":        content,
		"word_count":     originalRunes,
		"content_sha256": store.TextSHA256(fullContent),
	}
	if segmentFrom > 0 {
		result["segment_from_line"] = segmentFrom
		result["segment_to_line"] = segmentTo
		result["segment_runes"] = len([]rune(content))
		result["segment_complete"] = !truncated
		if !truncated {
			result["hint"] = "这是指定的完整行段；只依据本段做局部编辑，不要为了更多原文重读整章。"
		}
	}
	if a.Source == "draft" {
		final, finalErr := t.store.Drafts.LoadChapterText(a.Chapter)
		if finalErr != nil {
			return nil, fmt.Errorf("compare draft with final chapter %d: %w", a.Chapter, finalErr)
		}
		if final != "" {
			differs := fullContent != final
			result["final_sha256"] = store.TextSHA256(final)
			result["differs_from_final"] = differs
			if differs {
				result["polish_state"] = "modified"
				result["polish_hint"] = "当前草稿已包含相对终稿的打磨改动；恢复流程应保留该版本并进入同版校验，不要为满足修改次数重复改稿。"
			} else {
				result["polish_state"] = "unchanged"
				result["polish_hint"] = "当前草稿与终稿相同；打磨任务需先依据 rewrite_brief 做局部实质改动。"
			}
		}
	}
	if truncated {
		result["truncated"] = true
		result["returned_runes"] = len([]rune(content))
		result["hint"] = "内容已按 max_runes 截断；如需完整章节请重新读取并提高上限"
	}
	return json.Marshal(result)
}

type chapterRangePayload struct {
	Chapters          map[int]string `json:"chapters"`
	From              int            `json:"from"`
	To                int            `json:"to"`
	ReturnedFrom      int            `json:"returned_from,omitempty"`
	ReturnedTo        int            `json:"returned_to,omitempty"`
	ReturnedChapters  []int          `json:"returned_chapters,omitempty"`
	TotalRunes        int            `json:"total_runes"`
	MaxRunes          int            `json:"max_runes,omitempty"`
	MaxTotalRunes     int            `json:"max_total_runes,omitempty"`
	TruncatedChapters []int          `json:"truncated_chapters,omitempty"`
	OversizedChapters []int          `json:"oversized_chapters,omitempty"`
	OmittedChapters   []int          `json:"omitted_chapters,omitempty"`
	NextFrom          int            `json:"next_from,omitempty"`
	Complete          bool           `json:"complete"`
	Hint              string         `json:"hint,omitempty"`
}

func buildChapterRangePayload(from, to, maxRunes, maxTotalRunes int, load func(int) (string, error)) (chapterRangePayload, error) {
	if from <= 0 || to < from {
		return chapterRangePayload{}, fmt.Errorf("invalid chapter range %d-%d", from, to)
	}
	payload := chapterRangePayload{
		Chapters:      make(map[int]string),
		From:          from,
		To:            to,
		MaxRunes:      maxRunes,
		MaxTotalRunes: maxTotalRunes,
		Complete:      true,
	}
	for ch := from; ch <= to; ch++ {
		text, err := load(ch)
		if err != nil {
			return chapterRangePayload{}, err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}

		clipped, truncated := truncateReadText(text, maxRunes)
		clippedRunes := len([]rune(clipped))
		if maxTotalRunes > 0 && payload.TotalRunes > 0 && payload.TotalRunes+clippedRunes > maxTotalRunes {
			payload.OmittedChapters = intRange(ch, to)
			payload.NextFrom = ch
			payload.Complete = false
			break
		}

		payload.Chapters[ch] = clipped
		payload.ReturnedChapters = append(payload.ReturnedChapters, ch)
		if payload.ReturnedFrom == 0 {
			payload.ReturnedFrom = ch
		}
		payload.ReturnedTo = ch
		payload.TotalRunes += clippedRunes
		if truncated {
			payload.TruncatedChapters = appendUniqueInt(payload.TruncatedChapters, ch)
		}
		if maxTotalRunes > 0 && clippedRunes > maxTotalRunes {
			payload.OversizedChapters = appendUniqueInt(payload.OversizedChapters, ch)
			if ch < to {
				payload.OmittedChapters = intRange(ch+1, to)
				payload.NextFrom = ch + 1
				payload.Complete = false
				break
			}
		}
	}
	if payload.NextFrom > 0 {
		payload.Hint = fmt.Sprintf("范围读取已按 max_total_runes 在章节边界处分批停止；继续调用 read_chapter(source=同上, from=%d, to=%d, max_total_runes=%d) 读取下一批。单章超预算时该章会作为独立批次返回，不会从章节中间切开。", payload.NextFrom, to, maxTotalRunes)
	}
	return payload, nil
}

func truncateReadText(text string, maxRunes int) (string, bool) {
	if maxRunes <= 0 {
		return text, false
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text, false
	}
	return string(runes[:maxRunes]) + "...", true
}

func intRange(from, to int) []int {
	if from <= 0 || to < from {
		return nil
	}
	out := make([]int, 0, to-from+1)
	for value := from; value <= to; value++ {
		out = append(out, value)
	}
	return out
}

func appendUniqueInt(values []int, next int) []int {
	for _, value := range values {
		if value == next {
			return values
		}
	}
	return append(values, next)
}

// maxCompletedChapter 返回已完成章节列表中的最大章节号。
func maxCompletedChapter(completed []int) int {
	m := 0
	for _, ch := range completed {
		if ch > m {
			m = ch
		}
	}
	return m
}
