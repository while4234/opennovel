package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
)

const manuscriptActionClarificationPrompt = `你是小说稿件修改需求澄清助手。你的职责不是修改正文，而是判断用户意见是否足以安全执行。

只有当歧义会实质改变人物动机、剧情事实、结构安排或修改范围时才提问；措辞、节奏等普通细节直接按合理方式执行。不得询问系统已经提供的事实，不得要求用户重复正文。涉及跨章连续性时，必须先使用 context.recent_summaries；已有摘要足以判断时直接给出 ready，不得反问作者上一章发生了什么。

输出严格 JSON：
{
  "status": "needs_input" 或 "ready",
  "assistant_message": "简短说明",
  "questions": [{"id":"q1","prompt":"问题"}],
  "resolved_instruction": "可直接交给修改或扩写服务的完整意见"
}

规则：每轮最多 3 个问题；ready 时 questions 必须为空且 resolved_instruction 必须完整；needs_input 时 resolved_instruction 为空。扩写的 resolved_instruction 不得超过 500 个汉字。不要输出 Markdown。`

// ManuscriptActionContext is built exclusively from authoritative project
// artifacts. Browser-supplied prose never crosses this boundary.
type ManuscriptActionContext struct {
	Mode               string                  `json:"mode"`
	ChapterID          string                  `json:"chapter_id"`
	DisplayChapter     int                     `json:"display_chapter"`
	Prose              string                  `json:"prose"`
	ProseCropped       bool                    `json:"prose_cropped"`
	ChapterOutline     any                     `json:"chapter_outline,omitempty"`
	VolumeTitle        string                  `json:"volume_title,omitempty"`
	ArcTitle           string                  `json:"arc_title,omitempty"`
	RevisionSummary    any                     `json:"revision_summary,omitempty"`
	RecentSummaries    []domain.ChapterSummary `json:"recent_summaries,omitempty"`
	AdaptationEvidence any                     `json:"adaptation_evidence,omitempty"`
	ContextSignature   string                  `json:"context_signature"`
}

type ManuscriptActionMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	QuestionID string `json:"question_id,omitempty"`
}

type ManuscriptActionClarificationRequest struct {
	Action       string                    `json:"action"`
	InitialInput string                    `json:"initial_input"`
	Context      ManuscriptActionContext   `json:"context"`
	Messages     []ManuscriptActionMessage `json:"messages,omitempty"`
}

type ManuscriptActionQuestion struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
}

type ManuscriptActionClarification struct {
	Status              string                     `json:"status"`
	AssistantMessage    string                     `json:"assistant_message"`
	Questions           []ManuscriptActionQuestion `json:"questions"`
	ResolvedInstruction string                     `json:"resolved_instruction"`
}

// ClarifyManuscriptAction asks the configured model only for material
// ambiguity detection. Generation and publication remain owned by the
// existing manuscript revision and expansion services.
func (h *Host) ClarifyManuscriptAction(ctx context.Context, request ManuscriptActionClarificationRequest) (ManuscriptActionClarification, error) {
	if h == nil || h.models == nil || h.store == nil {
		return ManuscriptActionClarification{}, fmt.Errorf("manuscript action clarification model is unavailable")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return ManuscriptActionClarification{}, fmt.Errorf("encode manuscript action clarification: %w", err)
	}
	recorder, err := modeldiag.Begin(modeldiag.Request{
		Store: h.store, Task: "manuscript_action_clarification", ChapterID: request.Context.ChapterID,
		System: manuscriptActionClarificationPrompt, User: payload, InputLimitBytes: 60 * 1024,
		OutputLimitTokens: 1400, SelectorCounts: map[string]int{"chapters": 1, "messages": len(request.Messages)},
		ContractSignature: request.Context.ContextSignature,
	})
	if err != nil {
		return ManuscriptActionClarification{}, err
	}
	model := h.models.ForStageWithFailover(bootstrap.StageSkeleton, nil)
	response, err := model.Generate(ctx, []agentcore.Message{
		agentcore.SystemMsg(manuscriptActionClarificationPrompt),
		agentcore.UserMsg(string(payload)),
	}, nil, agentcore.WithMaxTokens(1400), agentcore.WithJSONMode())
	if err != nil {
		_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
		return ManuscriptActionClarification{}, err
	}
	if response == nil || strings.TrimSpace(response.Message.TextContent()) == "" {
		_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", nil)
		return ManuscriptActionClarification{}, fmt.Errorf("empty manuscript action clarification")
	}
	text := strings.TrimSpace(response.Message.TextContent())
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	var result ManuscriptActionClarification
	if err := decoder.Decode(&result); err != nil {
		_ = recorder.Finish(modeldiag.StatusDecodeError, text, response.Message.Usage)
		return ManuscriptActionClarification{}, fmt.Errorf("decode manuscript action clarification: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		_ = recorder.Finish(modeldiag.StatusDecodeError, text, response.Message.Usage)
		return ManuscriptActionClarification{}, fmt.Errorf("manuscript action clarification contains trailing JSON")
	}
	if err := validateManuscriptActionClarification(&result, request.Action); err != nil {
		_ = recorder.Finish(modeldiag.StatusInvalidSchema, text, response.Message.Usage)
		return ManuscriptActionClarification{}, err
	}
	if err := recorder.Finish(modeldiag.StatusCompleted, text, response.Message.Usage); err != nil {
		return ManuscriptActionClarification{}, err
	}
	return result, nil
}

func validateManuscriptActionClarification(result *ManuscriptActionClarification, action string) error {
	result.Status = strings.TrimSpace(result.Status)
	result.AssistantMessage = strings.TrimSpace(result.AssistantMessage)
	result.ResolvedInstruction = strings.TrimSpace(result.ResolvedInstruction)
	if result.Status != "needs_input" && result.Status != "ready" {
		return fmt.Errorf("unsupported manuscript clarification status %q", result.Status)
	}
	if len(result.Questions) > 3 {
		return fmt.Errorf("manuscript clarification exceeds three questions")
	}
	seen := make(map[string]struct{}, len(result.Questions))
	for index := range result.Questions {
		question := &result.Questions[index]
		question.ID = strings.TrimSpace(question.ID)
		question.Prompt = strings.TrimSpace(question.Prompt)
		if question.ID == "" {
			question.ID = fmt.Sprintf("q%d", index+1)
		}
		if question.Prompt == "" {
			return fmt.Errorf("manuscript clarification question is empty")
		}
		if _, duplicate := seen[question.ID]; duplicate {
			return fmt.Errorf("duplicate manuscript clarification question %q", question.ID)
		}
		seen[question.ID] = struct{}{}
	}
	if result.Status == "needs_input" {
		if len(result.Questions) == 0 || result.ResolvedInstruction != "" {
			return fmt.Errorf("needs_input clarification must contain questions only")
		}
		return nil
	}
	if len(result.Questions) != 0 || result.ResolvedInstruction == "" {
		return fmt.Errorf("ready clarification must contain a resolved instruction only")
	}
	if action == "expand" && len([]rune(result.ResolvedInstruction)) > 500 {
		return fmt.Errorf("resolved expansion instruction exceeds 500 characters")
	}
	return nil
}
