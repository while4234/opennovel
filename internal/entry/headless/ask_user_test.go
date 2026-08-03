package headless

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/tools"
)

func TestLoadAnswersFileFromStdin(t *testing.T) {
	got, err := LoadAnswersFile("-", strings.NewReader(`{
		"answers":{"风格":"悬疑"},
		"notes":{"风格":"不要感情线"}
	}`))
	if err != nil {
		t.Fatalf("LoadAnswersFile: %v", err)
	}
	if got.Answers["风格"] != "悬疑" || got.Notes["风格"] != "不要感情线" {
		t.Fatalf("unexpected answers: %+v", got)
	}
}

func TestLoadAnswersFileRejectsUnknownFields(t *testing.T) {
	_, err := LoadAnswersFile("-", strings.NewReader(`{"answer":{"风格":"悬疑"}}`))
	if err == nil {
		t.Fatal("expected invalid answer document")
	}
	var payload struct {
		Error StructuredError `json:"error"`
	}
	if decodeErr := json.Unmarshal([]byte(FormatError(err)), &payload); decodeErr != nil {
		t.Fatalf("FormatError should be JSON: %v", decodeErr)
	}
	if payload.Error.Code != errorCodeAnswersFileInvalid {
		t.Fatalf("unexpected error code: %q", payload.Error.Code)
	}
}

func TestAnswerFileHandlerUsesQuestionThenHeader(t *testing.T) {
	handler := newAnswerFileHandler(AnswersFile{
		Answers: map[string]string{
			"你想要什么风格？": "悬疑",
			"篇幅":       "长篇",
		},
		Notes: map[string]string{"你想要什么风格？": "不要感情线"},
	}, nil)
	questions := []tools.Question{
		{Question: "你想要什么风格？", Header: "风格"},
		{Question: "计划写多长？", Header: "篇幅"},
	}
	response, err := handler.handle(context.Background(), questions)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if response.Answers[questions[0].Question] != "悬疑" || response.Answers[questions[1].Question] != "长篇" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.Notes[questions[0].Question] != "不要感情线" {
		t.Fatalf("unexpected notes: %+v", response.Notes)
	}
}

func TestAnswerFileHandlerReportsAllMissingQuestionsAndAborts(t *testing.T) {
	aborted := make(chan struct{}, 1)
	handler := newAnswerFileHandler(AnswersFile{Answers: map[string]string{}}, func() {
		aborted <- struct{}{}
	})
	questions := []tools.Question{
		{Question: "你想要什么风格？", Header: "风格"},
		{Question: "计划写多长？", Header: "篇幅"},
	}
	_, err := handler.handle(context.Background(), questions)
	if err == nil {
		t.Fatal("expected missing answers error")
	}
	structured, ok := err.(*StructuredError)
	if !ok || structured.Code != errorCodeAnswersMissing || len(structured.Questions) != 2 {
		t.Fatalf("unexpected error: %#v", err)
	}
	<-aborted
	if handler.Err() == nil {
		t.Fatal("expected handler to retain terminal error")
	}
}
