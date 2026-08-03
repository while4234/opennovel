package imp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	"github.com/voocel/ainovel-cli/internal/store"
)

// FoundationResult 是 Foundation 反推的结构化产物。
type FoundationResult struct {
	Premise       string                         // Markdown 字符串
	Characters    []domain.Character             // 角色档案
	Relationships []domain.CharacterRelationship // 来源小说中已有证据支持的角色关系
	WorldRules    []domain.WorldRule             // 世界规则
	Volumes       []domain.VolumeOutline         // 分层大纲：导入正文作为第一卷（可续写、可扩展）
	Compass       *domain.StoryCompass           // 续写方向锚点（ending_direction / open_threads / estimated_scale）
}

// LLMChat 是 imp 包对 ChatModel 的最小依赖：仅需要一次普通文本生成。
// 抽出独立接口便于单测注入 mock，避免直接耦合 agentcore 客户端。
type LLMChat interface {
	Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error)
}

// ReverseFoundation 用一次 LLM 调用，从已切分的章节正文反推 foundation。
// 不调用 save_foundation，纯函数；持久化由调用方决定。
func ReverseFoundation(ctx context.Context, llm LLMChat, systemPrompt string, chapters []Chapter) (*FoundationResult, error) {
	if len(chapters) == 0 {
		return nil, fmt.Errorf("no chapters to analyze")
	}
	if llm == nil {
		return nil, fmt.Errorf("llm is nil")
	}

	system := cleanLLMText(strings.ReplaceAll(systemPrompt, "${chapter_count}", fmt.Sprintf("%d", len(chapters))))
	user := cleanLLMText(buildFoundationUserPrompt(chapters))
	if len(system)+len(user) > 60*1024 {
		return reverseFoundationMapReduce(ctx, llm, systemPrompt, chapters)
	}

	messages := []agentcore.Message{
		agentcore.SystemMsg(system),
		agentcore.UserMsg(user),
	}
	recorder, beginErr := beginIMPDiagnostic(ctx, "import_foundation", 0, messages, 0, 0)
	if beginErr != nil {
		return nil, beginErr
	}
	resp, err := llm.Generate(ctx, messages, nil)
	if err != nil {
		_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
		return nil, fmt.Errorf("llm generate: %w", err)
	}
	if resp == nil {
		_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", nil)
		return nil, fmt.Errorf("llm returned nil response")
	}
	output := resp.Message.TextContent()
	if strings.TrimSpace(output) == "" {
		_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", resp.Message.Usage)
		return nil, fmt.Errorf("llm returned empty response")
	}
	parsed, parseErr := parseFoundationOutput(output, len(chapters))
	if parseErr != nil {
		_ = recorder.Finish(structuredDiagnosticStatus(parseErr), output, resp.Message.Usage)
		return nil, parseErr
	}
	if diagnosticErr := recorder.Finish(modeldiag.StatusCompleted, output, resp.Message.Usage); diagnosticErr != nil {
		return nil, diagnosticErr
	}
	return parsed, nil
}

func reverseFoundationMapReduce(ctx context.Context, llm LLMChat, systemPrompt string, chapters []Chapter) (*FoundationResult, error) {
	batches := foundationChapterBatches(systemPrompt, chapters, 48*1024)
	if len(batches) <= 1 {
		return nil, fmt.Errorf("import foundation chapter exceeds the 60 KiB compiled request budget")
	}
	partials := make([]FoundationMergePartial, 0, len(batches))
	from := 1
	for index, batch := range batches {
		result, err := ReverseFoundation(ctx, llm, systemPrompt, batch)
		if err != nil {
			return nil, fmt.Errorf("import foundation map batch %d/%d: %w", index+1, len(batches), err)
		}
		partials = append(partials, FoundationMergePartial{Index: index + 1, From: from, To: from + len(batch) - 1, Result: result})
		from += len(batch)
	}
	result, err := MergeFoundationPartialsBatched(ctx, llm, systemPrompt, partials, len(chapters), StructuredCallOptions{DisableStream: true}, 12_000, nil)
	if err != nil {
		return nil, fmt.Errorf("import foundation reduce: %w", err)
	}
	reports := make([]domain.AdaptationSourceReport, len(chapters))
	for index, chapter := range chapters {
		reports[index] = domain.AdaptationSourceReport{Chapter: index + 1, Title: chapter.Title, Summary: "imported chapter"}
	}
	result.Volumes = BuildSourceOutlineFromReports(reports)
	return result, nil
}

func foundationChapterBatches(systemPrompt string, chapters []Chapter, limit int) [][]Chapter {
	if limit <= 0 {
		limit = 48 * 1024
	}
	var batches [][]Chapter
	for start := 0; start < len(chapters); {
		end := start
		for end < len(chapters) {
			candidate := chapters[start : end+1]
			system := strings.ReplaceAll(systemPrompt, "${chapter_count}", fmt.Sprintf("%d", len(candidate)))
			if len(system)+len(buildFoundationUserPrompt(candidate)) > limit && end > start {
				break
			}
			if len(system)+len(buildFoundationUserPrompt(candidate)) > limit {
				end++
				break
			}
			end++
		}
		batches = append(batches, chapters[start:end])
		start = end
	}
	return batches
}

// buildFoundationUserPrompt 拼装用户提示：所有章节顺序拼接，附章号锚点便于 LLM 引用。
func buildFoundationUserPrompt(chapters []Chapter) string {
	var sb strings.Builder
	sb.WriteString("以下是已完成的 ")
	fmt.Fprintf(&sb, "%d", len(chapters))
	sb.WriteString(" 章正文。请严格按系统提示反推 foundation，输出五个 === TAG === 段。\n\n")
	for i, ch := range chapters {
		fmt.Fprintf(&sb, "## 第 %d 章：%s\n\n", i+1, cleanLLMText(ch.Title))
		sb.WriteString(cleanLLMText(ch.Content))
		sb.WriteString("\n\n---\n\n")
	}
	return sb.String()
}

// parseFoundationOutput 解析 LLM 输出的 envelope 并校验关键约束。
func parseFoundationOutput(text string, expectChapters int) (*FoundationResult, error) {
	text = cleanLLMText(text)
	env := parseTaggedEnvelope(text)
	if env == nil {
		return nil, fmt.Errorf("no === TAG === envelope found in LLM output")
	}
	if err := requireTags(env, "PREMISE", "CHARACTERS", "WORLD_RULES", "LAYERED_OUTLINE", "COMPASS"); err != nil {
		return nil, err
	}

	premise := stripFences(env["PREMISE"])
	if !strings.HasPrefix(strings.TrimLeft(premise, " \t\n"), "#") {
		return nil, fmt.Errorf("premise must start with a Markdown heading line (# 书名)")
	}

	var characters []domain.Character
	if err := decodeCharactersJSON("characters", env["CHARACTERS"], &characters, false); err != nil {
		return nil, err
	}

	var worldRules []domain.WorldRule
	if err := decodeJSON("world_rules", env["WORLD_RULES"], &worldRules); err != nil {
		return nil, err
	}

	var volumes []domain.VolumeOutline
	if err := decodeJSON("layered_outline", env["LAYERED_OUTLINE"], &volumes); err != nil {
		return nil, err
	}
	// 导入大纲必须把全部 N 章实展开（FlattenOutline 只数真实章节，骨架弧不计），
	// 否则逐章 commit 时会有章节落在大纲范围外、被越界守卫拒绝。
	if got := len(domain.FlattenOutline(volumes)); got != expectChapters {
		return nil, fmt.Errorf("layered outline chapter count mismatch: got %d, want %d", got, expectChapters)
	}

	var compass domain.StoryCompass
	if err := decodeJSON("compass", env["COMPASS"], &compass); err != nil {
		return nil, err
	}

	return &FoundationResult{
		Premise:    premise,
		Characters: characters,
		WorldRules: worldRules,
		Volumes:    volumes,
		Compass:    &compass,
	}, nil
}

// PersistFoundation 把反推结果写入 Store，顺序与 Architect 长篇 prompt 一致：
// premise → characters → world_rules → layered_outline → compass。导入正文作为第一卷
// 落成分层大纲，使导入的书可被续写、可扩展。每步都触发 save_foundation 同款落盘逻辑。
//
// 不直接调 SaveFoundationTool 是因为这里是确定性回放，无需走 LLM 工具调度。
// 但保持与 SaveFoundationTool 相同的副作用：phase 推进、checkpoint 追加。
func PersistFoundation(ctx context.Context, st *store.Store, scale domain.PlanningTier, fr *FoundationResult) error {
	return persistFoundation(ctx, st, scale, fr, false)
}

// PersistFoundationPreservingCast persists adaptation planning products while
// retaining the already-published target CoreCast characters/relationships.
func PersistFoundationPreservingCast(ctx context.Context, st *store.Store, scale domain.PlanningTier, fr *FoundationResult) error {
	return persistFoundation(ctx, st, scale, fr, true)
}

// PersistAdaptationOutline materializes only the confirmed proposal's outline,
// compass, and progress.  The already-approved target StoryFoundation is read
// as an input and is never rewritten during proposal confirmation.
func PersistAdaptationOutline(_ context.Context, st *store.Store, scale domain.PlanningTier, fr *FoundationResult) error {
	if st == nil || fr == nil {
		return fmt.Errorf("store and adaptation outline are required")
	}
	if err := st.RunMeta.SetPlanningTier(scale); err != nil {
		return fmt.Errorf("save planning tier: %w", err)
	}
	if err := st.Outline.SaveLayeredOutline(fr.Volumes); err != nil {
		return fmt.Errorf("save layered outline: %w", err)
	}
	if err := st.Outline.SaveOutline(domain.FlattenOutline(fr.Volumes)); err != nil {
		return fmt.Errorf("save flattened outline: %w", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
		return fmt.Errorf("set outline phase: %w", err)
	}
	if err := st.Progress.SetTotalChapters(domain.TotalChapters(fr.Volumes)); err != nil {
		return fmt.Errorf("set total chapters: %w", err)
	}
	if err := st.Progress.SetLayered(true); err != nil {
		return fmt.Errorf("set layered planning: %w", err)
	}
	if len(fr.Volumes) > 0 && len(fr.Volumes[0].Arcs) > 0 {
		if err := st.Progress.UpdateVolumeArc(fr.Volumes[0].Index, fr.Volumes[0].Arcs[0].Index); err != nil {
			return fmt.Errorf("set current volume and arc: %w", err)
		}
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "layered_outline", "layered_outline.json"); err != nil {
		return fmt.Errorf("checkpoint layered outline: %w", err)
	}
	if fr.Compass == nil {
		return fmt.Errorf("adaptation compass is required")
	}
	if err := st.Outline.SaveCompass(*fr.Compass); err != nil {
		return fmt.Errorf("save compass: %w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "compass", "meta/compass.json"); err != nil {
		return fmt.Errorf("checkpoint compass: %w", err)
	}
	return nil
}

func persistFoundation(ctx context.Context, st *store.Store, scale domain.PlanningTier, fr *FoundationResult, preserveCast bool) error {
	if fr == nil {
		return fmt.Errorf("nil foundation result")
	}
	if err := st.RunMeta.SetPlanningTier(scale); err != nil {
		return fmt.Errorf("save planning tier: %w", err)
	}
	current, err := st.Foundation.Load()
	if err != nil {
		return fmt.Errorf("load story foundation: %w", err)
	}
	candidate := domain.CloneStoryFoundation(current)
	candidate.Premise = fr.Premise
	if !preserveCast {
		candidate.Characters = fr.Characters
	}
	candidate.WorldRules = fr.WorldRules
	if _, err := st.Foundation.SaveCAS(candidate, current.Revision); err != nil {
		return fmt.Errorf("save story foundation: %w", err)
	}

	// 1. premise
	if name := domain.ExtractNovelNameFromPremise(fr.Premise); name != "" {
		if err := persistAdaptationProgress(preserveCast, "set novel name", func() error { return st.Progress.SetNovelName(name) }); err != nil {
			return err
		}
	}
	if err := persistAdaptationProgress(preserveCast, "set premise phase", func() error { return st.Progress.UpdatePhase(domain.PhasePremise) }); err != nil {
		return err
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "premise", "premise.md"); err != nil {
		return fmt.Errorf("checkpoint premise: %w", err)
	}

	// 2. characters
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "characters", "characters.json"); err != nil {
		return fmt.Errorf("checkpoint characters: %w", err)
	}

	// 3. world_rules
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "world_rules", "world_rules.json"); err != nil {
		return fmt.Errorf("checkpoint world_rules: %w", err)
	}

	// 4. layered outline（导入正文作为第一卷 → 分层模式，可续写、可扩展）
	if err := st.Outline.SaveLayeredOutline(fr.Volumes); err != nil {
		return fmt.Errorf("save layered outline: %w", err)
	}
	if err := st.Outline.SaveOutline(domain.FlattenOutline(fr.Volumes)); err != nil {
		return fmt.Errorf("save flattened outline: %w", err)
	}
	if err := persistAdaptationProgress(preserveCast, "set outline phase", func() error { return st.Progress.UpdatePhase(domain.PhaseOutline) }); err != nil {
		return err
	}
	if err := persistAdaptationProgress(preserveCast, "set total chapters", func() error { return st.Progress.SetTotalChapters(domain.TotalChapters(fr.Volumes)) }); err != nil {
		return err
	}
	if err := persistAdaptationProgress(preserveCast, "set layered planning", func() error { return st.Progress.SetLayered(true) }); err != nil {
		return err
	}
	if len(fr.Volumes) > 0 && len(fr.Volumes[0].Arcs) > 0 {
		if err := persistAdaptationProgress(preserveCast, "set current volume and arc", func() error {
			return st.Progress.UpdateVolumeArc(fr.Volumes[0].Index, fr.Volumes[0].Arcs[0].Index)
		}); err != nil {
			return err
		}
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "layered_outline", "layered_outline.json"); err != nil {
		return fmt.Errorf("checkpoint layered outline: %w", err)
	}

	// 5. compass（续写方向锚点）：让 layeredBookComplete 据 open_threads 判定，
	//    避免导入即被判完结；也给续写时的方向/篇幅一个基准。
	if err := st.Outline.SaveCompass(*fr.Compass); err != nil {
		return fmt.Errorf("save compass: %w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "compass", "meta/compass.json"); err != nil {
		return fmt.Errorf("checkpoint compass: %w", err)
	}

	// 6. foundation 完整 → 推进到 writing 阶段（与 save_foundation 末尾逻辑一致）
	if len(st.FoundationMissing()) == 0 {
		p, loadErr := st.Progress.Load()
		if loadErr != nil && preserveCast {
			return fmt.Errorf("load progress before writing phase: %w", loadErr)
		}
		if p != nil &&
			p.Phase != domain.PhaseWriting && p.Phase != domain.PhaseComplete {
			if err := persistAdaptationProgress(preserveCast, "set writing phase", func() error { return st.Progress.UpdatePhase(domain.PhaseWriting) }); err != nil {
				return err
			}
		}
	}
	return nil
}

func persistAdaptationProgress(strict bool, action string, fn func() error) error {
	err := fn()
	if err != nil && strict {
		return fmt.Errorf("%s: %w", action, err)
	}
	return nil
}

// decodeJSON 解析 JSON（数组或对象）并附上标签，便于调试。
func decodeJSON(label, body string, out any) error {
	body = stripFences(body)
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("%s body is empty", label)
	}
	segment, err := extractJSONSegment(body)
	if err != nil {
		return fmt.Errorf("extract %s JSON: %w", label, err)
	}
	if err := json.Unmarshal([]byte(segment), out); err != nil {
		return fmt.Errorf("parse %s JSON: %w", label, err)
	}
	return nil
}

// extractJSONSegment returns the first complete JSON object or array in an LLM section.
func extractJSONSegment(body string) (string, error) {
	body = strings.TrimSpace(body)
	var firstInvalid string

	for start := 0; start < len(body); start++ {
		if body[start] != '{' && body[start] != '[' {
			continue
		}
		end, ok := scanJSONEnd(body[start:])
		if !ok {
			continue
		}

		candidate := strings.TrimSpace(body[start : start+end])
		if json.Valid([]byte(candidate)) {
			return candidate, nil
		}
		if firstInvalid == "" {
			firstInvalid = candidate
		}
	}

	if firstInvalid != "" {
		return firstInvalid, nil
	}
	return "", fmt.Errorf("no complete JSON object or array found")
}

func scanJSONEnd(s string) (int, bool) {
	if len(s) == 0 {
		return 0, false
	}

	stack := []byte{matchingJSONClose(s[0])}
	inString := false
	escaped := false

	for i := 1; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, matchingJSONClose(c))
		case '}', ']':
			if len(stack) == 0 || c != stack[len(stack)-1] {
				return 0, false
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

func matchingJSONClose(open byte) byte {
	if open == '{' {
		return '}'
	}
	return ']'
}

// stripFences 去掉首尾 ``` 代码围栏（含语言标签），LLM 偶尔会自作主张包一层。
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if j := strings.LastIndex(s, "```"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}
