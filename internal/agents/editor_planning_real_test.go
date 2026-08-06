package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/ainovel-cli/internal/store"
	toolpkg "github.com/voocel/ainovel-cli/internal/tools"
)

func TestRealPlanningReviewPagesFitEveryCompiledEditorRound(t *testing.T) {
	source := strings.TrimSpace(os.Getenv("AINOVEL_REAL_PLANNING_OUTPUT"))
	if source == "" {
		t.Skip("set AINOVEL_REAL_PLANNING_OUTPUT to a project output directory")
	}
	copyRoot := filepath.Join(t.TempDir(), "output")
	if err := copyArchitectPlanningFixture(source, copyRoot); err != nil {
		t.Fatalf("copy real planning fixture: %v", err)
	}

	st := store.NewStore(copyRoot)
	bundle := assets.Load("suspense")
	registry := toolpkg.NewPlanningReviewRunRegistry()
	selector := toolpkg.PlanningReviewSelector{Volume: 3}
	const reviewID = "real-editor-review-volume-3"
	if err := registry.Authorize(reviewID, selector); err != nil {
		t.Fatal(err)
	}
	contextTool := toolpkg.NewContextToolWithOptions(st, bundle.References, "suspense", toolpkg.ContextToolOptions{
		Role: "editor", PlanningReviews: registry,
	})
	saveTool := toolpkg.NewSaveOriginalPlanningAuditTool(st, registry)
	toolSpecs := []agentcore.ToolSpec{
		{Name: contextTool.Name(), Description: contextTool.Description(), Parameters: contextTool.Schema()},
		{Name: saveTool.Name(), Description: saveTool.Description(), Parameters: saveTool.Schema()},
	}
	messages := []agentcore.AgentMessage{
		agentcore.SystemMsg(globalprompt.ApplyForModel("grok-4.5", bundle.Prompts.Editor)),
		agentcore.UserMsg("Audit skeleton volume 3. Read page zero without guessing an ID, then request each later planning_review page with only its exact signed next_cursor. Save with the canonical page-zero review_id."),
	}
	strategy := newForceToolResultMicrocompact(*editorToolResultMicrocompactConfig())
	cursor := ""
	pageCount := 0
	maxCompiled := 0
	for {
		request := map[string]any{"scope": "planning_review"}
		if cursor == "" {
			request["volume"] = 3
		} else {
			request["cursor"] = cursor
		}
		args, _ := json.Marshal(request)
		page, err := contextTool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("execute page %d: %v", pageCount, err)
		}
		callID := "planning-review-page-" + string(rune('0'+pageCount))
		messages = append(messages,
			agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
				agentcore.ToolCallBlock(agentcore.ToolCall{ID: callID, Name: contextTool.Name(), Args: args}),
			}},
			agentcore.ToolResultMsg(callID, page, false),
		)
		view, _, err := strategy.ForceApply(t.Context(), messages, messages, corecontext.Budget{})
		if err != nil {
			t.Fatalf("microcompact page %d: %v", pageCount, err)
		}
		messages = view
		compiled, err := compileAgentInput(toLLMMessages(t, view), toolSpecs)
		if err != nil {
			t.Fatalf("compile Editor page %d: %v", pageCount, err)
		}
		if len(compiled) >= architectLongInputLimitBytes {
			t.Fatalf("compiled Editor page %d=%d bytes, want <%d", pageCount, len(compiled), architectLongInputLimitBytes)
		}
		maxCompiled = max(maxCompiled, len(compiled))
		t.Logf("page=%d result=%d compiled=%d", pageCount, len(page), len(compiled))

		var envelope struct {
			Page struct {
				Complete   bool   `json:"complete"`
				NextCursor string `json:"next_cursor"`
			} `json:"context_page"`
		}
		if err := json.Unmarshal(page, &envelope); err != nil {
			t.Fatal(err)
		}
		pageCount++
		if envelope.Page.Complete {
			break
		}
		cursor = envelope.Page.NextCursor
	}
	if pageCount < 2 {
		t.Fatalf("real review used %d page, want pagination", pageCount)
	}
	if maxCompiled >= architectLongInputLimitBytes {
		t.Fatalf("max compiled Editor request=%d", maxCompiled)
	}
}
