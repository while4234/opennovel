package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type RollbackStage string

const (
	RollbackStageBlank          RollbackStage = "blank"
	RollbackStageDraft          RollbackStage = "draft"
	RollbackStageVolumeOutline  RollbackStage = "volume_outline"
	RollbackStageChapterOutline RollbackStage = "chapter_outline"
	RollbackStageProposal       RollbackStage = "proposal"
)

type RollbackRequest struct {
	Confirm     bool   `json:"confirm"`
	PreviewHash string `json:"preview_hash,omitempty"`
}

type RollbackPreview struct {
	CanRollback    bool          `json:"can_rollback"`
	Mode           string        `json:"mode,omitempty"`
	CurrentStage   string        `json:"current_stage,omitempty"`
	TargetStage    RollbackStage `json:"target_stage,omitempty"`
	TargetLabel    string        `json:"target_label,omitempty"`
	Warning        string        `json:"warning,omitempty"`
	DeletePaths    []string      `json:"delete_paths,omitempty"`
	PreservePaths  []string      `json:"preserve_paths,omitempty"`
	Reason         string        `json:"reason,omitempty"`
	PreviewHash    string        `json:"preview_hash,omitempty"`
	StateSignature string        `json:"-"`
}

type RollbackResult struct {
	Preview      RollbackPreview `json:"preview"`
	DeletedPaths []string        `json:"deleted_paths,omitempty"`
}

func RollbackPreviewWithHash(preview RollbackPreview) RollbackPreview {
	preview.PreviewHash = RollbackPreviewHash(preview)
	return preview
}

func RollbackPreviewHash(preview RollbackPreview) string {
	payload := struct {
		CanRollback    bool          `json:"can_rollback"`
		Mode           string        `json:"mode,omitempty"`
		CurrentStage   string        `json:"current_stage,omitempty"`
		TargetStage    RollbackStage `json:"target_stage,omitempty"`
		TargetLabel    string        `json:"target_label,omitempty"`
		DeletePaths    []string      `json:"delete_paths,omitempty"`
		PreservePaths  []string      `json:"preserve_paths,omitempty"`
		StateSignature string        `json:"state_signature,omitempty"`
	}{
		CanRollback:    preview.CanRollback,
		Mode:           preview.Mode,
		CurrentStage:   preview.CurrentStage,
		TargetStage:    preview.TargetStage,
		TargetLabel:    preview.TargetLabel,
		DeletePaths:    append([]string(nil), preview.DeletePaths...),
		PreservePaths:  append([]string(nil), preview.PreservePaths...),
		StateSignature: preview.StateSignature,
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
