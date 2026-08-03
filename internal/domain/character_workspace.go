package domain

import (
	"fmt"
	"strings"
)

const CharacterWorkspaceRunVersion = 1

type CharacterWorkspaceRunMode string

const (
	CharacterWorkspaceAnalyze CharacterWorkspaceRunMode = "analyze"
	CharacterWorkspaceReview  CharacterWorkspaceRunMode = "review"
)

type CharacterWorkspaceRunStatus string

const (
	CharacterWorkspaceQueued      CharacterWorkspaceRunStatus = "queued"
	CharacterWorkspaceRunning     CharacterWorkspaceRunStatus = "running"
	CharacterWorkspaceCompleted   CharacterWorkspaceRunStatus = "completed"
	CharacterWorkspaceFailed      CharacterWorkspaceRunStatus = "failed"
	CharacterWorkspaceInterrupted CharacterWorkspaceRunStatus = "interrupted"
	CharacterWorkspaceStale       CharacterWorkspaceRunStatus = "stale"
	CharacterWorkspaceDiscarded   CharacterWorkspaceRunStatus = "discarded"
)

type CharacterWorkspaceReceipt struct {
	IdempotencyKey string `json:"idempotency_key"`
	Fingerprint    string `json:"fingerprint"`
	Attempt        int    `json:"attempt"`
}

// CharacterWorkspaceRun persists compact orchestration state. InputCandidate is
// target-story data only; immutable SourceFoundation text is never copied here.
type CharacterWorkspaceRun struct {
	Version                   int                             `json:"version"`
	Revision                  int64                           `json:"revision"`
	RunID                     string                          `json:"run_id"`
	Mode                      CharacterWorkspaceRunMode       `json:"mode"`
	Status                    CharacterWorkspaceRunStatus     `json:"status"`
	Stage                     string                          `json:"stage"`
	ProjectMode               CharacterCardProjectMode        `json:"project_mode"`
	Base                      CharacterCardBinding            `json:"base"`
	IdempotencyKey            string                          `json:"idempotency_key"`
	RequestFingerprint        string                          `json:"request_fingerprint"`
	RequestedCharacterIDs     []string                        `json:"requested_character_ids"`
	Instruction               string                          `json:"instruction,omitempty"`
	AllowSupportingCharacters bool                            `json:"allow_supporting_characters"`
	InputCandidate            StoryFoundation                 `json:"input_candidate"`
	InputCandidateDigest      string                          `json:"input_candidate_digest"`
	ResultCandidate           CharacterCardCandidateReference `json:"result_candidate"`
	CandidateRevision         int64                           `json:"candidate_revision,omitempty"`
	LifecycleRevision         int64                           `json:"lifecycle_revision,omitempty"`
	Attempt                   int                             `json:"attempt"`
	ModelRoute                string                          `json:"model_route,omitempty"`
	CharacterCount            int                             `json:"character_count"`
	RelationshipCount         int                             `json:"relationship_count"`
	Error                     *CharacterCardError             `json:"error,omitempty"`
	RetryReceipts             []CharacterWorkspaceReceipt     `json:"retry_receipts"`
	DiscardReceipt            *CharacterWorkspaceReceipt      `json:"discard_receipt,omitempty"`
	CreatedAt                 string                          `json:"created_at"`
	StartedAt                 string                          `json:"started_at,omitempty"`
	UpdatedAt                 string                          `json:"updated_at"`
	FinishedAt                string                          `json:"finished_at,omitempty"`
	DurationMS                int64                           `json:"duration_ms,omitempty"`
}

func (r CharacterWorkspaceRun) Active() bool {
	return r.Status == CharacterWorkspaceQueued || r.Status == CharacterWorkspaceRunning
}

func NormalizeCharacterWorkspaceRun(value CharacterWorkspaceRun) (CharacterWorkspaceRun, error) {
	out := value
	if out.Version == 0 {
		out.Version = CharacterWorkspaceRunVersion
	}
	out.RunID = strings.TrimSpace(out.RunID)
	out.Stage = strings.TrimSpace(out.Stage)
	out.IdempotencyKey = strings.TrimSpace(out.IdempotencyKey)
	out.RequestFingerprint = strings.TrimSpace(out.RequestFingerprint)
	out.Instruction = strings.TrimSpace(out.Instruction)
	out.InputCandidateDigest = strings.TrimSpace(out.InputCandidateDigest)
	out.ModelRoute = strings.TrimSpace(out.ModelRoute)
	out.CreatedAt = strings.TrimSpace(out.CreatedAt)
	out.StartedAt = strings.TrimSpace(out.StartedAt)
	out.UpdatedAt = strings.TrimSpace(out.UpdatedAt)
	out.FinishedAt = strings.TrimSpace(out.FinishedAt)
	out.RequestedCharacterIDs = compactNonEmpty(out.RequestedCharacterIDs)
	if out.RetryReceipts == nil {
		out.RetryReceipts = []CharacterWorkspaceReceipt{}
	}
	for i := range out.RetryReceipts {
		out.RetryReceipts[i].IdempotencyKey = strings.TrimSpace(out.RetryReceipts[i].IdempotencyKey)
		out.RetryReceipts[i].Fingerprint = strings.TrimSpace(out.RetryReceipts[i].Fingerprint)
	}
	if out.DiscardReceipt != nil {
		out.DiscardReceipt.IdempotencyKey = strings.TrimSpace(out.DiscardReceipt.IdempotencyKey)
		out.DiscardReceipt.Fingerprint = strings.TrimSpace(out.DiscardReceipt.Fingerprint)
	}
	if out.Error != nil {
		out.Error.Class = strings.TrimSpace(out.Error.Class)
		out.Error.Message = strings.TrimSpace(out.Error.Message)
	}
	if err := validateCharacterWorkspaceRun(out); err != nil {
		return CharacterWorkspaceRun{}, err
	}
	return out, nil
}

func validateCharacterWorkspaceRun(value CharacterWorkspaceRun) error {
	if value.Version != CharacterWorkspaceRunVersion || value.Revision < 0 {
		return fmt.Errorf("character workspace run contract is invalid")
	}
	if value.RunID == "" || value.IdempotencyKey == "" || len(value.RequestFingerprint) != 64 {
		return fmt.Errorf("character workspace run identity is incomplete")
	}
	if value.Mode != CharacterWorkspaceAnalyze && value.Mode != CharacterWorkspaceReview {
		return fmt.Errorf("character workspace run mode %q is invalid", value.Mode)
	}
	switch value.Status {
	case CharacterWorkspaceQueued, CharacterWorkspaceRunning, CharacterWorkspaceCompleted,
		CharacterWorkspaceFailed, CharacterWorkspaceInterrupted, CharacterWorkspaceStale,
		CharacterWorkspaceDiscarded:
	default:
		return fmt.Errorf("character workspace run status %q is invalid", value.Status)
	}
	if value.Stage == "" || value.Attempt <= 0 {
		return fmt.Errorf("character workspace run stage and attempt are required")
	}
	if value.ProjectMode != CharacterCardProjectOriginal && value.ProjectMode != CharacterCardProjectAdaptation {
		return fmt.Errorf("character workspace project mode %q is invalid", value.ProjectMode)
	}
	if err := validateCharacterCardCandidate(value.Base.Candidate); err != nil {
		return fmt.Errorf("character workspace base: %w", err)
	}
	if len(value.Base.InputDigest) != 64 || len(value.InputCandidateDigest) != 64 {
		return fmt.Errorf("character workspace input binding is incomplete")
	}
	if value.CharacterCount < 0 || value.RelationshipCount < 0 || value.DurationMS < 0 {
		return fmt.Errorf("character workspace counters cannot be negative")
	}
	if value.Error != nil && (value.Error.Class == "" || value.Error.Message == "") {
		return fmt.Errorf("character workspace error envelope is incomplete")
	}
	for _, receipt := range value.RetryReceipts {
		if receipt.IdempotencyKey == "" || len(receipt.Fingerprint) != 64 || receipt.Attempt <= 0 {
			return fmt.Errorf("character workspace retry receipt is incomplete")
		}
	}
	if value.DiscardReceipt != nil &&
		(value.DiscardReceipt.IdempotencyKey == "" || len(value.DiscardReceipt.Fingerprint) != 64) {
		return fmt.Errorf("character workspace discard receipt is incomplete")
	}
	return nil
}
