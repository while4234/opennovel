package store

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const manuscriptContextDiagnosticsPath = "meta/manuscript/context-diagnostics.json"

type ManuscriptContextDiagnostic struct {
	Version             int            `json:"version"`
	Task                string         `json:"task"`
	RevisionID          string         `json:"revision_id,omitempty"`
	ChapterID           string         `json:"chapter_id,omitempty"`
	Batch               int            `json:"batch,omitempty"`
	Segment             int            `json:"segment,omitempty"`
	LayerBytes          map[string]int `json:"layer_bytes"`
	InputBytes          int            `json:"input_bytes"`
	InputTokenEstimate  int            `json:"input_token_estimate"`
	OutputRunes         int            `json:"output_runes"`
	OutputTokenEstimate int            `json:"output_token_estimate"`
	ActualInputTokens   int            `json:"actual_input_tokens,omitempty"`
	ActualOutputTokens  int            `json:"actual_output_tokens,omitempty"`
	ActualTotalTokens   int            `json:"actual_total_tokens,omitempty"`
	UsagePresent        bool           `json:"usage_present"`
	OutputSignature     string         `json:"output_signature,omitempty"`
	InputLimitBytes     int            `json:"input_limit_bytes"`
	OutputLimitTokens   int            `json:"output_limit_tokens"`
	SelectorCounts      map[string]int `json:"selector_counts,omitempty"`
	SplitReason         string         `json:"split_reason,omitempty"`
	ContentSignature    string         `json:"content_signature"`
	ContractSignature   string         `json:"contract_signature,omitempty"`
	Status              string         `json:"status"`
	RecordedAt          string         `json:"recorded_at"`
}

func (s *Store) AppendManuscriptContextDiagnostic(record ManuscriptContextDiagnostic) error {
	if strings.TrimSpace(record.Task) == "" || record.InputBytes < 0 || record.OutputRunes < 0 || len(record.ContentSignature) != 64 {
		return fmt.Errorf("invalid manuscript context diagnostic")
	}
	if record.InputLimitBytes > 0 && record.InputBytes > record.InputLimitBytes && record.Status != "rejected_budget" {
		return fmt.Errorf("over-budget manuscript diagnostic must be fail-closed")
	}
	if record.ContractSignature != "" && len(record.ContractSignature) != 64 {
		return fmt.Errorf("invalid manuscript diagnostic contract signature")
	}
	if record.OutputSignature != "" && len(record.OutputSignature) != 64 {
		return fmt.Errorf("invalid manuscript diagnostic output signature")
	}
	if record.ActualInputTokens < 0 || record.ActualOutputTokens < 0 || record.ActualTotalTokens < 0 {
		return fmt.Errorf("invalid manuscript diagnostic actual usage")
	}
	for _, identifier := range []string{record.Task, record.RevisionID, record.ChapterID} {
		if len(identifier) > 160 || strings.ContainsAny(identifier, "\r\n/\\") {
			return fmt.Errorf("manuscript diagnostic identity is not metadata-only")
		}
	}
	if len(record.SplitReason) > 240 || strings.ContainsAny(record.SplitReason, "\r\n/\\") {
		return fmt.Errorf("manuscript diagnostic split reason is not metadata-only")
	}
	record.Version = 1
	record.RecordedAt = domain.RevisionTimestamp()
	return s.ManuscriptRevisions.io.WithWriteLock(func() error {
		var records []ManuscriptContextDiagnostic
		if err := s.ManuscriptRevisions.io.ReadJSONUnlocked(manuscriptContextDiagnosticsPath, &records); err != nil && !os.IsNotExist(err) {
			return err
		}
		records = append(records, record)
		if len(records) > 1000 {
			records = records[len(records)-1000:]
		}
		return s.ManuscriptRevisions.io.WriteJSONUnlocked(manuscriptContextDiagnosticsPath, records)
	})
}
