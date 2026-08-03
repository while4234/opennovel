package modeldiag

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type storeContextKey struct{}

func WithStore(ctx context.Context, diagnosticStore *store.Store) context.Context {
	if diagnosticStore == nil {
		return ctx
	}
	return context.WithValue(ctx, storeContextKey{}, diagnosticStore)
}

func StoreFromContext(ctx context.Context) *store.Store {
	if ctx == nil {
		return nil
	}
	diagnosticStore, _ := ctx.Value(storeContextKey{}).(*store.Store)
	return diagnosticStore
}

const (
	StatusCompleted      = "completed"
	StatusRejectedBudget = "rejected_budget"
	StatusProviderError  = "provider_error"
	StatusEmptyResponse  = "empty_response"
	StatusTruncated      = "truncated_response"
	StatusDecodeError    = "decode_error"
	StatusInvalidSchema  = "invalid_schema"
)

type Request struct {
	Store             *store.Store
	Task              string
	RevisionID        string
	ChapterID         string
	Batch             int
	Segment           int
	System            string
	User              []byte
	InputLimitBytes   int
	OutputLimitTokens int
	SelectorCounts    map[string]int
	SplitReason       string
	ContractSignature string
}

type Recorder struct {
	request  Request
	recorded bool
}

func Begin(request Request) (*Recorder, error) {
	recorder := &Recorder{request: request}
	if request.InputLimitBytes > 0 && recorder.inputBytes() > request.InputLimitBytes {
		if err := recorder.Finish(StatusRejectedBudget, "", nil); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("compiled %s request is %d bytes (limit %d)", request.Task, recorder.inputBytes(), request.InputLimitBytes)
	}
	return recorder, nil
}

func (r *Recorder) Finish(status, output string, usage *agentcore.Usage) error {
	if r == nil || r.recorded || r.request.Store == nil {
		return nil
	}
	r.recorded = true
	record := store.ManuscriptContextDiagnostic{
		Task: r.request.Task, RevisionID: r.request.RevisionID, ChapterID: r.request.ChapterID,
		Batch: r.request.Batch, Segment: r.request.Segment,
		LayerBytes: map[string]int{"system": len(r.request.System), "user": len(r.request.User)},
		InputBytes: r.inputBytes(), InputTokenEstimate: estimateTokens(r.inputBytes()),
		OutputRunes: utf8.RuneCountInString(output), OutputTokenEstimate: estimateTokens(len(output)),
		InputLimitBytes: r.request.InputLimitBytes, OutputLimitTokens: r.request.OutputLimitTokens,
		SelectorCounts: r.request.SelectorCounts, SplitReason: r.request.SplitReason,
		ContentSignature: domain.ContentSignature(r.request.User), ContractSignature: r.request.ContractSignature,
		Status: status,
	}
	if output != "" {
		record.OutputSignature = domain.ContentSignature([]byte(output))
	}
	if usage != nil {
		record.UsagePresent = true
		record.ActualInputTokens = usage.Input
		record.ActualOutputTokens = usage.Output
		record.ActualTotalTokens = usage.TotalTokens
	}
	return r.request.Store.AppendManuscriptContextDiagnostic(record)
}

func (r *Recorder) inputBytes() int {
	return len(r.request.System) + len(r.request.User)
}

func estimateTokens(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	return (bytes + 3) / 4
}
