package headless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/voocel/ainovel-cli/internal/tools"
)

const (
	errorCodeAnswersFileRead    = "answers_file_read_failed"
	errorCodeAnswersFileInvalid = "answers_file_invalid"
	errorCodeAnswersMissing     = "answers_missing"
)

// AnswersFile is the non-interactive answer contract consumed by headless runs.
// Keys may be either the complete question text or its short header.
type AnswersFile struct {
	Answers map[string]string `json:"answers"`
	Notes   map[string]string `json:"notes,omitempty"`
}

// StructuredError is emitted as JSON so automation can distinguish missing
// answers from malformed input and ordinary model/runtime failures.
type StructuredError struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	Questions []string `json:"questions,omitempty"`
}

func (e *StructuredError) Error() string { return e.Message }

func FormatError(err error) string {
	var structured *StructuredError
	if errors.As(err, &structured) {
		payload := struct {
			Error *StructuredError `json:"error"`
		}{Error: structured}
		if data, marshalErr := json.Marshal(payload); marshalErr == nil {
			return string(data)
		}
	}
	return "error: " + err.Error()
}

func LoadAnswersFile(path string, stdin io.Reader) (AnswersFile, error) {
	answers := AnswersFile{
		Answers: map[string]string{},
		Notes:   map[string]string{},
	}
	if strings.TrimSpace(path) == "" {
		return answers, nil
	}

	var (
		data []byte
		err  error
	)
	if path == "-" {
		if stdin == nil {
			stdin = os.Stdin
		}
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return AnswersFile{}, &StructuredError{
			Code:    errorCodeAnswersFileRead,
			Message: fmt.Sprintf("读取答案文件失败: %v", err),
		}
	}

	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&answers); err != nil {
		return AnswersFile{}, &StructuredError{
			Code:    errorCodeAnswersFileInvalid,
			Message: fmt.Sprintf("答案文件必须是 {\"answers\":{...},\"notes\":{...}} 格式的 JSON: %v", err),
		}
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return AnswersFile{}, &StructuredError{
			Code:    errorCodeAnswersFileInvalid,
			Message: err.Error(),
		}
	}
	if answers.Answers == nil {
		answers.Answers = map[string]string{}
	}
	if answers.Notes == nil {
		answers.Notes = map[string]string{}
	}
	return answers, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("答案文件包含无效的尾随内容: %v", err)
	}
	return fmt.Errorf("答案文件只能包含一个 JSON 对象")
}

type answerFileHandler struct {
	answers AnswersFile
	abort   func()

	mu  sync.Mutex
	err error
}

func newAnswerFileHandler(answers AnswersFile, abort func()) *answerFileHandler {
	return &answerFileHandler{answers: answers, abort: abort}
}

func (h *answerFileHandler) handle(ctx context.Context, questions []tools.Question) (*tools.AskUserResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	response := &tools.AskUserResponse{
		Answers: make(map[string]string, len(questions)),
		Notes:   make(map[string]string),
	}
	var missing []string
	for _, question := range questions {
		answer, note, ok := h.lookup(question)
		if !ok {
			missing = append(missing, question.Question)
			continue
		}
		response.Answers[question.Question] = answer
		if note != "" {
			response.Notes[question.Question] = note
		}
	}
	if len(missing) == 0 {
		return response, nil
	}

	missingErr := &StructuredError{
		Code:      errorCodeAnswersMissing,
		Message:   "答案文件缺少本次 headless 运行所需的问题答案",
		Questions: missing,
	}
	h.err = missingErr
	if h.abort != nil {
		go h.abort()
	}
	return nil, missingErr
}

func (h *answerFileHandler) lookup(question tools.Question) (string, string, bool) {
	keys := []string{question.Question, question.Header}
	for _, key := range keys {
		answer := strings.TrimSpace(h.answers.Answers[key])
		if answer == "" {
			continue
		}
		return answer, strings.TrimSpace(h.answers.Notes[key]), true
	}
	return "", "", false
}

func (h *answerFileHandler) Err() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}
