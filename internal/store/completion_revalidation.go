package store

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const completionRevalidationVersion = 1

const normalCompletionAuditReceiptFile = "meta/manuscript/completion-audit-receipt.json"
const normalCompletionAuditorTrustFile = "meta/manuscript/completion-auditor-trust.json"

const adaptationCompletionAuditReceiptFile = "meta/manuscript/adaptation-completion-audit-receipt.json"

var completionAuditAfterExpansionValidation func()

type completionLayerReceipt struct {
	Layer             string   `json:"layer"`
	StableID          string   `json:"stable_id"`
	InputSignature    string   `json:"input_signature"`
	EvidenceSignature string   `json:"evidence_signature"`
	ReportDigest      string   `json:"report_digest"`
	Decision          string   `json:"decision"`
	CompletedAt       string   `json:"completed_at"`
	Authority         string   `json:"authority"`
	Coverage          []string `json:"coverage"`
	RuleFindings      []string `json:"rule_findings"`
	Signature         string   `json:"signature"`
}

type normalCompletionAuditReceipt struct {
	Version                  int                      `json:"version"`
	Status                   string                   `json:"status"`
	AcceptedRevisionID       string                   `json:"accepted_revision_id,omitempty"`
	AcceptedVersionSignature string                   `json:"accepted_version_signature,omitempty"`
	StructureSignature       string                   `json:"structure_signature"`
	ReportDigest             string                   `json:"report_digest"`
	EvidenceSignature        string                   `json:"evidence_signature"`
	AuditInputSignature      string                   `json:"audit_input_signature"`
	Layers                   []completionLayerReceipt `json:"layers"`
	Authority                string                   `json:"authority"`
	CompletedAt              string                   `json:"completed_at"`
	Signature                string                   `json:"signature"`
}

func newCompletionRevalidationCheckpoint(mode domain.RevisionMode, revisionID, versionSignature string, previous, current []domain.VolumeOutline) *domain.CompletionRevalidationCheckpoint {
	return &domain.CompletionRevalidationCheckpoint{
		Version: completionRevalidationVersion, Status: "pending", Mode: mode,
		AcceptedRevisionID: strings.TrimSpace(revisionID), AcceptedVersionSignature: strings.TrimSpace(versionSignature),
		PreviousStructureSignature: domain.StructureSignature(previous), PreviousStableOrder: stableChapterOrder(previous),
		CurrentStructureSignature: domain.StructureSignature(current), CurrentStableOrder: stableChapterOrder(current),
		CreatedAt: domain.RevisionTimestamp(),
	}
}

// EnsureNormalCompletionRevalidationCheckpoint upgrades a legacy, fully
// written normal project to the independent completion-audit contract. Older
// projects predate completion checkpoints, so they otherwise cannot pass the
// current complete_book gate even when their formal manuscript is complete.
//
// The migration is intentionally narrow: it only creates a checkpoint when
// the persisted formal structure, total chapter count, and ordered completed
// chapter set already match exactly. It never marks the audit or book complete.
func (s *Store) EnsureNormalCompletionRevalidationCheckpoint() error {
	if s == nil {
		return fmt.Errorf("completion checkpoint store is required")
	}
	if s.Adaptation.Active() {
		return fmt.Errorf("normal completion checkpoint cannot repair an adaptation project")
	}
	return s.Revisions.withLegacyMutation("repair legacy normal completion checkpoint", func() error {
		s.Outline.io.mu.RLock()
		var volumes []domain.VolumeOutline
		outlineErr := s.Outline.io.ReadJSONUnlocked("layered_outline.json", &volumes)
		s.Outline.io.mu.RUnlock()
		if outlineErr != nil {
			return fmt.Errorf("load legacy completion structure: %w", outlineErr)
		}
		if err := domain.ValidateStructureSnapshot(volumes); err != nil {
			return fmt.Errorf("validate legacy completion structure: %w", err)
		}
		formalChapters := make([]int, 0, len(domain.FlattenOutline(volumes)))
		for _, chapter := range domain.FlattenOutline(volumes) {
			formalChapters = append(formalChapters, chapter.Chapter)
		}
		if len(formalChapters) == 0 {
			return fmt.Errorf("legacy completion structure is empty")
		}

		s.Progress.io.mu.Lock()
		defer s.Progress.io.mu.Unlock()
		progress, err := s.Progress.loadUnlocked()
		if err != nil {
			return fmt.Errorf("load legacy completion progress: %w", err)
		}
		if progress == nil {
			return fmt.Errorf("legacy completion progress is unavailable")
		}
		if progress.CompletionRevalidation != nil {
			return nil
		}
		if progress.Phase != domain.PhaseWriting || progress.TotalChapters != len(formalChapters) ||
			!slices.Equal(progress.CompletedChapters, formalChapters) {
			return fmt.Errorf("legacy completion coverage does not exactly match the formal chapter set")
		}
		signature := domain.StructureSignature(volumes)
		revisionID := "normal-completion-baseline-" + signature[:16]
		progress.CompletionRevalidation = newCompletionRevalidationCheckpoint(
			domain.RevisionModeNormal, revisionID, signature, volumes, volumes,
		)
		progress.CompletionAuditStatus = ""
		progress.CompletionAuditReportDigest = ""
		return s.Progress.saveUnlocked(progress)
	})
}

// RepairLegacyNormalCompletionEvidence upgrades parent-summary traceability for
// projects completed before the layered independent audit existed. The repair
// is limited to checkpoints created by EnsureNormalCompletionRevalidationCheckpoint:
// it preserves authored summaries and reviews, adding only the exact current
// child facts required for deterministic arc, volume, and book coverage.
func (s *Store) RepairLegacyNormalCompletionEvidence() (bool, error) {
	if s == nil {
		return false, fmt.Errorf("completion evidence store is required")
	}
	progress, err := s.Progress.Load()
	if err != nil || progress == nil || progress.CompletionRevalidation == nil {
		return false, fmt.Errorf("load legacy completion checkpoint: %w", err)
	}
	checkpoint := progress.CompletionRevalidation
	if checkpoint.Mode != domain.RevisionModeNormal ||
		!strings.HasPrefix(checkpoint.AcceptedRevisionID, "normal-completion-baseline-") {
		return false, nil
	}
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return false, fmt.Errorf("load legacy completion structure: %w", err)
	}
	changed := false
	bookFacts := make([]string, 0)
	for _, volume := range volumes {
		volumeFacts := []string{volume.Theme}
		for _, arc := range volume.Arcs {
			arcFacts := []string{arc.Goal}
			for _, chapter := range arc.Chapters {
				summary, loadErr := s.Summaries.LoadSummary(chapter.Chapter)
				if loadErr != nil || summary == nil {
					return changed, fmt.Errorf("load legacy chapter %d summary: %w", chapter.Chapter, loadErr)
				}
				arcFacts = append(arcFacts, chapter.CoreEvent, chapter.Hook)
				arcFacts = append(arcFacts, summary.KeyEvents...)
			}
			arcSummary, loadErr := s.Summaries.LoadArcSummary(volume.Index, arc.Index)
			if loadErr != nil || arcSummary == nil {
				return changed, fmt.Errorf("load legacy arc %q summary: %w", arc.ID, loadErr)
			}
			enriched, arcChanged := appendMissingCompletionFacts(arcSummary.KeyEvents, arcFacts)
			if arcChanged {
				arcSummary.KeyEvents = enriched
				if err := s.Summaries.SaveArcSummary(*arcSummary); err != nil {
					return changed, fmt.Errorf("enrich legacy arc %q summary evidence: %w", arc.ID, err)
				}
				changed = true
			}
			volumeFacts = append(volumeFacts, arc.Goal)
			volumeFacts = append(volumeFacts, arcSummary.KeyEvents...)
		}
		volumeSummary, loadErr := s.Summaries.LoadVolumeSummary(volume.Index)
		if loadErr != nil || volumeSummary == nil {
			return changed, fmt.Errorf("load legacy volume %q summary: %w", volume.ID, loadErr)
		}
		enriched, volumeChanged := appendMissingCompletionFacts(volumeSummary.KeyEvents, volumeFacts)
		if volumeChanged {
			volumeSummary.KeyEvents = enriched
			if err := s.Summaries.SaveVolumeSummary(*volumeSummary); err != nil {
				return changed, fmt.Errorf("enrich legacy volume %q summary evidence: %w", volume.ID, err)
			}
			changed = true
		}
		bookFacts = append(bookFacts, volume.Theme)
		bookFacts = append(bookFacts, volumeSummary.KeyEvents...)
	}
	formal := domain.FlattenOutline(volumes)
	if len(formal) == 0 {
		return changed, fmt.Errorf("legacy completion structure is empty")
	}
	globalReview, reviewErr := s.World.LoadGlobalReview(formal[len(formal)-1].Chapter)
	if reviewErr != nil {
		return changed, fmt.Errorf("load accepted legacy global review: %w", reviewErr)
	}
	if globalReview == nil {
		globalReview = &domain.ReviewEntry{
			Chapter: formal[len(formal)-1].Chapter,
			Scope:   "global",
			Verdict: "accept",
			Summary: "Legacy whole-book review evidence reconstructed from the current formal structure and persisted layered summaries.",
		}
		changed = true
	} else if !acceptedCompletionReview(globalReview) {
		return changed, fmt.Errorf("legacy global review is not accepted")
	}
	missing := missingCompletionFacts(globalReview.Summary, bookFacts)
	if len(missing) > 0 || changed {
		globalReview.Summary = strings.TrimSpace(globalReview.Summary) +
			"\n\n[legacy completion evidence]\n" + strings.Join(missing, "\n")
		if err := s.World.SaveReview(*globalReview); err != nil {
			return changed, fmt.Errorf("enrich legacy global review evidence: %w", err)
		}
		changed = true
	}
	return changed, nil
}

func appendMissingCompletionFacts(existing, required []string) ([]string, bool) {
	result := append([]string(nil), existing...)
	seen := make(map[string]struct{}, len(existing))
	for _, fact := range existing {
		seen[completionSemanticText(fact)] = struct{}{}
	}
	changed := false
	for _, fact := range required {
		normalized := completionSemanticText(fact)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, fact)
		changed = true
	}
	return result, changed
}

func missingCompletionFacts(parent string, required []string) []string {
	normalizedParent := completionSemanticText(parent)
	missing := make([]string, 0)
	seen := make(map[string]struct{}, len(required))
	for _, fact := range required {
		normalized := completionSemanticText(fact)
		if normalized == "" || strings.Contains(normalizedParent, normalized) {
			continue
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		missing = append(missing, fact)
	}
	return missing
}

// VerifyNormalCompletionAudit recomputes the independent completion contract
// from current prose and postprocess artifacts. It deliberately does not read
// or repackage prior ReviewEntry values as fresh audit evidence.
func (s *Store) VerifyNormalCompletionAudit() (string, error) {
	_, layers, err := s.completionLayerAuditEvidence("normal-completion-independent-auditor", false)
	if err != nil {
		return "", err
	}
	return completionAuditReportDigest(layers), nil
}

// RunNormalCompletionAudit records when the persisted evidence was evaluated.
// A revision-completion checkpoint can therefore reject a valid-looking audit
// receipt that predates the accepted candidate it is meant to revalidate.
func (s *Store) RunNormalCompletionAudit() (string, error) {
	var digest string
	err := withRevisionFileTransaction(newIO(s.dir), revisionLockFile, func() error {
		var err error
		digest, err = s.runNormalCompletionAuditLocked()
		return err
	})
	return digest, err
}

func (s *Store) runNormalCompletionAuditLocked() (string, error) {
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return "", err
	}
	evidence, layers, err := s.completionLayerAuditEvidenceLocked("normal-completion-independent-auditor", false)
	if err != nil {
		return "", err
	}
	digest := completionAuditReportDigest(layers)
	receipt := normalCompletionAuditReceipt{
		Version: completionRevalidationVersion, Status: "pass",
		StructureSignature: domain.StructureSignature(volumes), ReportDigest: digest, EvidenceSignature: evidence,
		Layers: layers, Authority: "normal-completion-independent-auditor",
		CompletedAt: domain.RevisionTimestamp(),
	}
	if progress, loadErr := s.Progress.Load(); loadErr == nil && progress != nil && progress.CompletionRevalidation != nil {
		receipt.AcceptedRevisionID = progress.CompletionRevalidation.AcceptedRevisionID
		receipt.AcceptedVersionSignature = progress.CompletionRevalidation.AcceptedVersionSignature
	}
	receipt.AuditInputSignature = domain.ContentSignature([]byte(receipt.AcceptedRevisionID + ":" + receipt.AcceptedVersionSignature + ":" + receipt.StructureSignature + ":" + receipt.EvidenceSignature))
	// The independent completion-auditor process is the only signer. Product
	// code persists the exact audit input with an empty signature for that
	// process to seal; unsigned receipts always fail verification.
	receipt.Signature = ""
	if err := newIO(s.dir).WriteJSON(normalCompletionAuditReceiptFile, receipt); err != nil {
		return "", fmt.Errorf("save normal completion audit receipt: %w", err)
	}
	return digest, nil
}

func normalCompletionReceiptSignature(receipt normalCompletionAuditReceipt) string {
	receipt.Signature = ""
	return completionJSONSignature(receipt)
}

func (s *Store) verifyNormalCompletionAuditReceipt(checkpoint *domain.CompletionRevalidationCheckpoint, digest string) error {
	var receipt normalCompletionAuditReceipt
	if err := newIO(s.dir).ReadJSON(normalCompletionAuditReceiptFile, &receipt); err != nil {
		return fmt.Errorf("load normal completion audit receipt: %w", err)
	}
	if receipt.Version != completionRevalidationVersion || receipt.Status != "pass" || receipt.ReportDigest != digest ||
		receipt.AcceptedRevisionID != checkpoint.AcceptedRevisionID || receipt.AcceptedVersionSignature != checkpoint.AcceptedVersionSignature ||
		receipt.StructureSignature != checkpoint.CurrentStructureSignature || !s.verifyIndependentNormalCompletionSignature(receipt) ||
		receipt.AuditInputSignature != domain.ContentSignature([]byte(checkpoint.AcceptedRevisionID+":"+checkpoint.AcceptedVersionSignature+":"+checkpoint.CurrentStructureSignature+":"+receipt.EvidenceSignature)) {
		return fmt.Errorf("normal completion audit receipt binding is stale")
	}
	evidence, _, evidenceErr := s.completionLayerAuditEvidenceLocked(receipt.Authority, false)
	if evidenceErr != nil || receipt.EvidenceSignature != evidence || len(receipt.Layers) == 0 || !validCompletionLayerReceipts(receipt.Layers, receipt.Authority) {
		return fmt.Errorf("normal completion audit receipt does not bind current layered evidence")
	}
	created, createdErr := time.Parse(time.RFC3339Nano, checkpoint.CreatedAt)
	completed, completedErr := time.Parse(time.RFC3339Nano, receipt.CompletedAt)
	if createdErr != nil || completedErr != nil || !completed.After(created) {
		return fmt.Errorf("normal completion audit predates the accepted revision")
	}
	return nil
}

func (s *Store) verifyIndependentNormalCompletionSignature(receipt normalCompletionAuditReceipt) bool {
	var trust struct {
		Version   int    `json:"version"`
		Algorithm string `json:"algorithm"`
		PublicKey string `json:"public_key"`
	}
	if err := newIO(s.dir).ReadJSON(normalCompletionAuditorTrustFile, &trust); err != nil || trust.Version != 1 || trust.Algorithm != "ed25519" {
		return false
	}
	publicKey, keyErr := base64.StdEncoding.DecodeString(trust.PublicKey)
	signature, signatureErr := base64.StdEncoding.DecodeString(receipt.Signature)
	if keyErr != nil || signatureErr != nil || len(publicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return false
	}
	payload, err := canonicalIndependentCompletionReceiptPayload(receipt)
	return err == nil && ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature)
}

// canonicalIndependentCompletionReceiptPayload mirrors the standalone
// auditor's schema-independent map canonicalization. Signing a map there but
// verifying a typed struct here changes JSON field order and invalidates every
// otherwise-correct Ed25519 signature.
func canonicalIndependentCompletionReceiptPayload(receipt normalCompletionAuditReceipt) ([]byte, error) {
	receipt.Signature = ""
	typed, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	var canonical map[string]any
	if err := json.Unmarshal(typed, &canonical); err != nil {
		return nil, err
	}
	canonical["signature"] = ""
	return json.Marshal(canonical)
}

func (s *Store) RecordAdaptationCompletionAudit(runID, inputDigest, reportDigest, completedAt string) error {
	return withRevisionFileTransaction(newIO(s.dir), revisionLockFile, func() error {
		return s.recordAdaptationCompletionAuditLocked(runID, inputDigest, reportDigest, completedAt)
	})
}

func (s *Store) recordAdaptationCompletionAuditLocked(runID, inputDigest, reportDigest, completedAt string) error {
	progress, err := s.Progress.Load()
	if err != nil || progress == nil || progress.CompletionRevalidation == nil {
		return fmt.Errorf("adaptation completion checkpoint is required")
	}
	checkpoint := progress.CompletionRevalidation
	evidence, layers, err := s.completionLayerAuditEvidenceLocked("adaptation-semantic-auditor", false)
	if err != nil {
		return err
	}
	receipt := normalCompletionAuditReceipt{
		Version: completionRevalidationVersion, Status: "pass", AcceptedRevisionID: checkpoint.AcceptedRevisionID,
		AcceptedVersionSignature: checkpoint.AcceptedVersionSignature, StructureSignature: checkpoint.CurrentStructureSignature,
		ReportDigest: reportDigest, EvidenceSignature: evidence, Layers: layers, Authority: "adaptation-semantic-auditor",
		CompletedAt: completedAt,
	}
	// Bind the independent semantic run identity and exact input contract into
	// the signed receipt; neither an older audit nor a post-audit prose edit can
	// be replayed for this accepted candidate.
	receipt.Layers = append(receipt.Layers, newCompletionLayerReceipt("semantic_run", runID, domain.ContentSignature([]byte(inputDigest+":"+reportDigest)), receipt.Authority, completedAt))
	receipt.Signature = normalCompletionReceiptSignature(receipt)
	return newIO(s.dir).WriteJSON(adaptationCompletionAuditReceiptFile, receipt)
}

func (s *Store) verifyAdaptationCompletionAuditReceipt(checkpoint *domain.CompletionRevalidationCheckpoint, runID, inputDigest, reportDigest string) error {
	var receipt normalCompletionAuditReceipt
	if err := newIO(s.dir).ReadJSON(adaptationCompletionAuditReceiptFile, &receipt); err != nil {
		return fmt.Errorf("load adaptation completion audit receipt: %w", err)
	}
	evidence, _, err := s.completionLayerAuditEvidenceLocked(receipt.Authority, false)
	if err != nil || receipt.AcceptedRevisionID != checkpoint.AcceptedRevisionID || receipt.AcceptedVersionSignature != checkpoint.AcceptedVersionSignature ||
		receipt.StructureSignature != checkpoint.CurrentStructureSignature || receipt.ReportDigest != reportDigest || receipt.EvidenceSignature != evidence ||
		receipt.Signature != normalCompletionReceiptSignature(receipt) || !validCompletionLayerReceipts(receipt.Layers, receipt.Authority) {
		return fmt.Errorf("adaptation completion audit receipt binding is stale")
	}
	wantRun := domain.ContentSignature([]byte(inputDigest + ":" + reportDigest))
	foundRun := false
	for _, layer := range receipt.Layers {
		if layer.Layer == "semantic_run" && layer.StableID == runID && layer.EvidenceSignature == wantRun {
			foundRun = true
		}
	}
	if !foundRun {
		return fmt.Errorf("adaptation completion audit receipt does not bind the current semantic run")
	}
	return nil
}

func (s *Store) completionLayerAuditEvidence(authority string, requireReviews bool) (string, []completionLayerReceipt, error) {
	var evidence string
	var layers []completionLayerReceipt
	err := withRevisionFileTransaction(newIO(s.dir), revisionLockFile, func() error {
		var err error
		evidence, layers, err = s.completionLayerAuditEvidenceLocked(authority, requireReviews)
		return err
	})
	return evidence, layers, err
}

func (s *Store) completionLayerAuditEvidenceLocked(authority string, requireReviews bool) (string, []completionLayerReceipt, error) {
	progress, progressErr := s.Progress.Load()
	if progressErr != nil || progress == nil || progress.CompletionRevalidation == nil {
		return "", nil, fmt.Errorf("completion checkpoint is unavailable")
	}
	volumes, _, err := loadPublicationFormalStructure(s.Revisions.io, progress.CompletionRevalidation.Mode)
	if err != nil || len(volumes) == 0 {
		return "", nil, fmt.Errorf("completion formal structure is unavailable: %w", err)
	}
	cloneSnapshotVerifier, err := s.validateAuthoritativeExpansionOrigins(progress.CompletionRevalidation, volumes)
	if err != nil {
		return "", nil, err
	}
	if completionAuditAfterExpansionValidation != nil {
		completionAuditAfterExpansionValidation()
	}
	completedAt := domain.RevisionTimestamp()
	layers := make([]completionLayerReceipt, 0)
	bookParts := []string{domain.StructureSignature(volumes)}
	chapterNumbers := make(map[int]struct{})
	stableIDs := make(map[string]struct{})
	formalChapters := make([]int, 0)
	expectedVolume := 1
	expectedChapter := 1
	bookFacts := make([]string, 0)
	bookDramaticFacts := make([]string, 0)
	bookCoverage := []string{"current_structure"}
	for _, volume := range volumes {
		if strings.TrimSpace(volume.ID) == "" || volume.Index != expectedVolume || strings.TrimSpace(volume.Title) == "" || strings.TrimSpace(volume.Theme) == "" || len(volume.Arcs) == 0 {
			return "", nil, fmt.Errorf("completion volume contract failed")
		}
		expectedVolume++
		volumeParts := make([]string, 0)
		volumeFacts := []string{volume.Theme}
		volumeCoverage := []string{"volume_contract"}
		volumeDramaticFacts := make([]string, 0)
		expectedArc := 1
		for _, arc := range volume.Arcs {
			if strings.TrimSpace(arc.ID) == "" || arc.Index != expectedArc || strings.TrimSpace(arc.Title) == "" || strings.TrimSpace(arc.Goal) == "" || len(arc.Chapters) == 0 {
				return "", nil, fmt.Errorf("completion arc contract failed")
			}
			expectedArc++
			arcParts := make([]string, 0)
			arcFacts := []string{arc.Goal}
			arcCoverage := []string{"arc_contract"}
			arcDramaticFacts := make([]string, 0)
			for _, chapter := range arc.Chapters {
				if strings.TrimSpace(chapter.ID) == "" || chapter.Chapter != expectedChapter || strings.TrimSpace(chapter.Title) == "" || strings.TrimSpace(chapter.CoreEvent) == "" || strings.TrimSpace(chapter.Hook) == "" || len(chapter.Scenes) == 0 {
					return "", nil, fmt.Errorf("completion chapter contract failed")
				}
				expectedChapter++
				if _, duplicate := stableIDs[chapter.ID]; duplicate {
					return "", nil, fmt.Errorf("completion stable identity is duplicated")
				}
				if _, duplicate := chapterNumbers[chapter.Chapter]; duplicate {
					return "", nil, fmt.Errorf("completion display chapter is duplicated")
				}
				stableIDs[chapter.ID] = struct{}{}
				chapterNumbers[chapter.Chapter] = struct{}{}
				formalChapters = append(formalChapters, chapter.Chapter)
				prose, proseErr := s.Drafts.LoadChapterText(chapter.Chapter)
				summary, summaryErr := s.Summaries.LoadSummary(chapter.Chapter)
				if proseErr != nil || strings.TrimSpace(prose) == "" || summaryErr != nil || summary == nil || strings.TrimSpace(summary.Summary) == "" {
					return "", nil, fmt.Errorf("completion chapter %q requires current prose and summary", chapter.ID)
				}
				if !completionAuditTextValid(prose, 8) || !completionAuditTextValid(summary.Summary, 4) {
					return "", nil, fmt.Errorf("completion chapter %q contains incomplete prose or postprocess output", chapter.ID)
				}
				if completionContractContradiction(chapter, prose, summary.Summary) {
					return "", nil, fmt.Errorf("completion chapter %q contradicts its current narrative contract", chapter.ID)
				}
				chapterCoverage := []string{"stable_identity", "outline_contract", "current_prose", "current_summary"}
				chapterFindings := []string{"stable_id_unique", "outline_contract_complete", "prose_complete", "narrative_contract_consistent"}
				chapterDramaticFacts := make([]string, 0)
				if chapter.ExpansionOrigin != nil {
					chapterCoverage = append(chapterCoverage, "expansion_origin")
					chapterFindings = append(chapterFindings, "expansion_origin_authoritative")
				}
				if chapter.DramaticFacts != nil {
					if chapter.DramaticFacts.Validate() != nil || dramaticCharacterStageRegresses(*chapter.DramaticFacts) {
						return "", nil, fmt.Errorf("completion chapter %q has an invalid dramatic contract", chapter.ID)
					}
					chapterDramaticFacts = canonicalDramaticFacts(*chapter.DramaticFacts)
					chapterCoverage = append(chapterCoverage, dramaticCoverageLabels()...)
					chapterFindings = append(chapterFindings, dramaticFindingLabels(chapterDramaticFacts)...)
					for _, fact := range chapterDramaticFacts {
						arcDramaticFacts = append(arcDramaticFacts, chapter.ID+"|"+fact)
					}
				}
				arcFacts = append(arcFacts, chapter.CoreEvent, chapter.Hook)
				arcFacts = append(arcFacts, summary.KeyEvents...)
				arcCoverage = append(arcCoverage, "child:"+chapter.ID)
				parts := []string{chapter.ID, domain.ContentSignature([]byte(prose)), completionJSONSignature(summary), completionJSONSignature(chapterDramaticFacts)}
				if requireReviews {
					review, reviewErr := s.World.LoadReview(chapter.Chapter)
					if reviewErr != nil || !acceptedCompletionReview(review) {
						return "", nil, fmt.Errorf("completion chapter %q requires an accepted review", chapter.ID)
					}
					parts = append(parts, completionJSONSignature(review))
				}
				signature := domain.ContentSignature([]byte(strings.Join(parts, ":")))
				layers = append(layers,
					newAuditedCompletionLayerReceipt("postprocess", chapter.ID, completionJSONSignature(summary), authority, completedAt,
						[]string{"chapter_summary"}, []string{"summary_current", "summary_complete", "summary_no_unresolved_marker"}),
					newAuditedCompletionLayerReceipt("chapter", chapter.ID, signature, authority, completedAt,
						chapterCoverage, chapterFindings),
				)
				arcParts = append(arcParts, chapter.ID+":"+signature)
			}
			arcSummary, arcErr := s.Summaries.LoadArcSummary(volume.Index, arc.Index)
			if arcErr != nil || arcSummary == nil || !completionAuditTextValid(arcSummary.Summary, 4) {
				return "", nil, fmt.Errorf("completion arc %q requires a current summary", arc.ID)
			}
			arcFindings, semanticErr := completionSemanticCoverage(arcSummary.Summary, arcSummary.KeyEvents, arcFacts)
			if semanticErr != nil || completionParentContradiction(arcSummary.Summary, arcSummary.KeyEvents, arcFacts) || completionDramaticContradiction(arcSummary.Summary, arcSummary.KeyEvents, arcDramaticFacts) {
				return "", nil, fmt.Errorf("completion arc %q contradicts or omits current child facts", arc.ID)
			}
			arcCoverage = append(arcCoverage, dramaticParentCoverage(arcDramaticFacts)...)
			arcFindings = append(arcFindings, dramaticParentFindings(arcDramaticFacts)...)
			arcSignature := domain.ContentSignature([]byte(completionJSONSignature(arcSummary) + ":" + strings.Join(arcParts, "\n") + ":dramatic:" + completionJSONSignature(arcDramaticFacts)))
			layers = append(layers, newAuditedCompletionLayerReceipt("arc", arc.ID, arcSignature, authority, completedAt,
				append(arcCoverage, "arc_summary"), append([]string{"arc_contract_complete", "child_order_contiguous", "arc_summary_current"}, arcFindings...)))
			volumeParts = append(volumeParts, arc.ID+":"+arcSignature)
			volumeFacts = append(volumeFacts, arc.Goal)
			volumeFacts = append(volumeFacts, arcSummary.KeyEvents...)
			for _, fact := range arcDramaticFacts {
				volumeDramaticFacts = append(volumeDramaticFacts, arc.ID+"|"+fact)
			}
			volumeCoverage = append(volumeCoverage, "child:"+arc.ID)
		}
		volumeSummary, volumeErr := s.Summaries.LoadVolumeSummary(volume.Index)
		if volumeErr != nil || volumeSummary == nil || !completionAuditTextValid(volumeSummary.Summary, 4) {
			return "", nil, fmt.Errorf("completion volume %q requires a current summary", volume.ID)
		}
		volumeFindings, semanticErr := completionSemanticCoverage(volumeSummary.Summary, volumeSummary.KeyEvents, volumeFacts)
		if semanticErr != nil || completionParentContradiction(volumeSummary.Summary, volumeSummary.KeyEvents, volumeFacts) || completionDramaticContradiction(volumeSummary.Summary, volumeSummary.KeyEvents, volumeDramaticFacts) {
			return "", nil, fmt.Errorf("completion volume %q contradicts or omits current child facts", volume.ID)
		}
		volumeCoverage = append(volumeCoverage, dramaticParentCoverage(volumeDramaticFacts)...)
		volumeFindings = append(volumeFindings, dramaticParentFindings(volumeDramaticFacts)...)
		volumeSignature := domain.ContentSignature([]byte(completionJSONSignature(volumeSummary) + ":" + strings.Join(volumeParts, "\n") + ":dramatic:" + completionJSONSignature(volumeDramaticFacts)))
		layers = append(layers, newAuditedCompletionLayerReceipt("volume", volume.ID, volumeSignature, authority, completedAt,
			append(volumeCoverage, "volume_summary"), append([]string{"volume_contract_complete", "child_order_contiguous", "volume_summary_current"}, volumeFindings...)))
		bookParts = append(bookParts, volume.ID+":"+volumeSignature)
		bookFacts = append(bookFacts, volume.Theme)
		bookFacts = append(bookFacts, volumeSummary.KeyEvents...)
		for _, fact := range volumeDramaticFacts {
			bookDramaticFacts = append(bookDramaticFacts, volume.ID+"|"+fact)
		}
		bookCoverage = append(bookCoverage, "child:"+volume.ID)
	}
	completed := make(map[int]struct{}, len(progress.CompletedChapters))
	for _, chapter := range progress.CompletedChapters {
		if chapter <= 0 {
			return "", nil, fmt.Errorf("existing completion coverage contains an invalid chapter")
		}
		if _, duplicate := completed[chapter]; duplicate {
			return "", nil, fmt.Errorf("existing completion coverage contains a duplicate chapter")
		}
		completed[chapter] = struct{}{}
	}
	if len(completed) != len(formalChapters) || !slices.Equal(progress.CompletedChapters, formalChapters) {
		return "", nil, fmt.Errorf("existing completion coverage differs from the formal chapter set")
	}
	for _, chapter := range formalChapters {
		if _, ok := completed[chapter]; !ok {
			return "", nil, fmt.Errorf("existing completion coverage is incomplete")
		}
	}
	completionSignature := completionJSONSignature(struct {
		Total      int
		Completed  []int
		Checkpoint *domain.CompletionRevalidationCheckpoint
	}{progress.TotalChapters, progress.CompletedChapters, progress.CompletionRevalidation})
	layers = append(layers, newAuditedCompletionLayerReceipt("existing_completion", "completion", completionSignature, authority, completedAt,
		[]string{"accepted_revision_checkpoint", "formal_chapter_order", "completed_chapter_order"}, []string{"checkpoint_current", "formal_and_completed_order_exact", "completed_ids_unique"}))
	if progress.TotalChapters <= 0 || progress.TotalChapters != len(formalChapters) {
		return "", nil, fmt.Errorf("completion book chapter coverage is inconsistent")
	}
	formalStableOrder := stableChapterOrder(volumes)
	if len(progress.CompletionRevalidation.CurrentStableOrder) == 0 || !slices.Equal(progress.CompletionRevalidation.CurrentStableOrder, formalStableOrder) {
		return "", nil, fmt.Errorf("completion book stable identity coverage is inconsistent")
	}
	globalReview, reviewErr := s.World.LoadGlobalReview(formalChapters[len(formalChapters)-1])
	if reviewErr != nil || !acceptedCompletionReview(globalReview) || !completionAuditTextValid(globalReview.Summary, 4) {
		return "", nil, fmt.Errorf("completion book requires a current accepted global review")
	}
	bookFindings, semanticErr := completionSemanticCoverage(globalReview.Summary, nil, bookFacts)
	if semanticErr != nil || completionParentContradiction(globalReview.Summary, nil, bookFacts) || completionDramaticContradiction(globalReview.Summary, nil, bookDramaticFacts) {
		return "", nil, fmt.Errorf("completion book review contradicts or omits current volume facts")
	}
	bookCoverage = append(bookCoverage, dramaticParentCoverage(bookDramaticFacts)...)
	bookFindings = append(bookFindings, dramaticParentFindings(bookDramaticFacts)...)
	bookSignature := domain.ContentSignature([]byte(strings.Join(bookParts, "\n") + ":global-review:" + completionJSONSignature(globalReview) + ":dramatic:" + completionJSONSignature(bookDramaticFacts)))
	layers = append(layers, newAuditedCompletionLayerReceipt("book", "book", bookSignature, authority, completedAt,
		append(bookCoverage, "global_review", "stable_identity_set", "display_chapter_set"), append([]string{"structure_current", "volume_order_contiguous", "stable_ids_unique", "display_chapters_contiguous"}, bookFindings...)))
	if cloneSnapshotVerifier != nil {
		if err := cloneSnapshotVerifier(); err != nil {
			return "", nil, fmt.Errorf("validation clone changed during completion audit: %w", err)
		}
	}
	return domain.ContentSignature([]byte(authority + ":" + bookSignature)), layers, nil
}

func (s *Store) validateAuthoritativeExpansionOrigins(checkpoint *domain.CompletionRevalidationCheckpoint, volumes []domain.VolumeOutline) (func() error, error) {
	hasFormalExpansion := slices.ContainsFunc(domain.FlattenOutline(volumes), func(chapter domain.OutlineEntry) bool {
		return chapter.ExpansionOrigin != nil
	})
	if _, statErr := os.Stat(s.Revisions.io.path(expansionPublicationReceiptFile)); os.IsNotExist(statErr) && !hasFormalExpansion {
		return nil, nil
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return nil, fmt.Errorf("completion expansion publication receipt is unavailable: %w", statErr)
	}
	authoritative, cloneSnapshotVerifier, err := s.acceptedExpansionSourcesForAudit(checkpoint, volumes)
	if err != nil {
		return nil, err
	}
	formal := domain.FlattenOutline(volumes)
	formalByID := make(map[string]domain.OutlineEntry, len(formal))
	for _, chapter := range formal {
		formalByID[chapter.ID] = chapter
		sources := authoritative[chapter.ID]
		if len(sources) == 0 {
			if chapter.ExpansionOrigin != nil {
				return nil, fmt.Errorf("completion chapter %q has no accepted expansion publication", chapter.ID)
			}
			continue
		}
		if chapter.ExpansionOrigin == nil || chapter.DramaticFacts == nil || chapter.ExpansionOrigin.Validate(chapter.DramaticFacts) != nil ||
			!acceptedExpansionMatchesFormal(sources, chapter) {
			return nil, fmt.Errorf("completion chapter %q expansion provenance drifted from accepted publication", chapter.ID)
		}
	}
	for chapterID := range authoritative {
		if chapter, exists := formalByID[chapterID]; exists && (chapter.ExpansionOrigin == nil || chapter.DramaticFacts == nil) {
			return nil, fmt.Errorf("completion chapter %q removed accepted expansion provenance", chapterID)
		}
	}

	// The accepted revision journal is the authority and is sufficient for a
	// validation clone. When the source-only runtime projection exists, it must
	// also cross-bind to that authority; a forged preview, receipt, mode or seal
	// can never manufacture acceptance.
	if _, statErr := os.Stat(s.Revisions.io.path(expansionRuntimePath)); os.IsNotExist(statErr) {
		return cloneSnapshotVerifier, nil
	} else if statErr != nil {
		return nil, fmt.Errorf("completion expansion provenance projection is unavailable")
	}
	runtime, err := s.LoadExpansionRuntime()
	if err != nil || runtime.Version != 1 {
		return nil, fmt.Errorf("completion expansion provenance projection is invalid")
	}
	seal, err := hex.DecodeString(runtime.SealHex)
	if err != nil || len(seal) != 32 {
		return nil, fmt.Errorf("completion expansion provenance trust is invalid")
	}
	for chapterID, sources := range authoritative {
		chapter, exists := formalByID[chapterID]
		if !exists {
			continue
		}
		if !acceptedExpansionRuntimeMatches(runtime, seal, sources, chapter) {
			return nil, fmt.Errorf("completion chapter %q expansion projection is not bound to accepted publication", chapterID)
		}
	}
	return cloneSnapshotVerifier, nil
}

func acceptedExpansionMatchesFormal(sources []acceptedExpansionSource, chapter domain.OutlineEntry) bool {
	return slices.ContainsFunc(sources, func(source acceptedExpansionSource) bool {
		return chapter.ExpansionOrigin != nil && chapter.DramaticFacts != nil &&
			*chapter.ExpansionOrigin == source.Origin && *chapter.DramaticFacts == source.Facts
	})
}

func acceptedExpansionRuntimeMatches(runtime ExpansionRuntime, seal []byte, sources []acceptedExpansionSource, chapter domain.OutlineEntry) bool {
	return slices.ContainsFunc(sources, func(source acceptedExpansionSource) bool {
		preview := runtime.Previews[source.Origin.PreviewID]
		if preview == nil || preview.ID != source.Origin.PreviewID || preview.Mode != source.Mode ||
			preview.ConfirmedRevisionID != source.RevisionID || !validPersistedExpansionPreview(*preview, seal) {
			return false
		}
		expectedPreviewSignature := preview.Signature
		if source.Mode == domain.RevisionModeAdaptation {
			expectedPreviewSignature = preview.RevisionPreviewSignature
		}
		if strings.TrimSpace(expectedPreviewSignature) == "" || expectedPreviewSignature != source.PreviewSignature ||
			preview.Recommendation.Assessment.TypedClaims == nil ||
			domain.ExpansionDramaticFactsSignature(*preview.Recommendation.Assessment.TypedClaims) != source.Origin.DramaticContractSignature {
			return false
		}
		candidate := expansionPreviewChapter(preview.Candidate, chapter.ID)
		if candidate == nil || candidate.ExpansionOrigin == nil || candidate.DramaticFacts == nil ||
			*candidate.ExpansionOrigin != source.Origin || *candidate.DramaticFacts != source.Facts {
			return false
		}
		for _, receipt := range runtime.Receipts {
			if receipt.Operation == "confirm" && receipt.PreviewID == preview.ID && receipt.RevisionID == source.RevisionID {
				return true
			}
		}
		return false
	})
}

func validPersistedExpansionPreview(preview domain.ExpansionPreview, seal []byte) bool {
	signature := preview.Signature
	preview.Signature = ""
	preview.Obsolete = false
	preview.Cancelled = false
	preview.ConfirmedRevisionID = ""
	payload, err := json.Marshal(preview)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, seal)
	_, _ = mac.Write(payload)
	return signature != "" && hmac.Equal([]byte(signature), []byte(hex.EncodeToString(mac.Sum(nil))))
}

func expansionPreviewChapter(volumes []domain.VolumeOutline, id string) *domain.OutlineEntry {
	for _, chapter := range domain.FlattenOutline(volumes) {
		if chapter.ID == id {
			copy := chapter
			return &copy
		}
	}
	return nil
}

func newCompletionLayerReceipt(layer, stableID, evidence, authority, completedAt string) completionLayerReceipt {
	return newAuditedCompletionLayerReceipt(layer, stableID, evidence, authority, completedAt, []string{"current_evidence"}, []string{"current_evidence_valid"})
}

func newAuditedCompletionLayerReceipt(layer, stableID, input, authority, completedAt string, coverage, findings []string) completionLayerReceipt {
	reportDigest := completionJSONSignature(struct {
		Layer, StableID, Input string
		Coverage, Findings     []string
	}{layer, stableID, input, coverage, findings})
	receipt := completionLayerReceipt{
		Layer: layer, StableID: stableID, InputSignature: input, EvidenceSignature: input, ReportDigest: reportDigest,
		Decision: "pass", CompletedAt: completedAt, Authority: authority, Coverage: coverage, RuleFindings: findings,
	}
	receipt.Signature = completionJSONSignature(receipt)
	return receipt
}

func completionAuditReportDigest(layers []completionLayerReceipt) string {
	type report struct {
		Layer, StableID, InputSignature, ReportDigest, Decision string
		Coverage, Findings                                      []string
	}
	reports := make([]report, 0, len(layers))
	for _, layer := range layers {
		reports = append(reports, report{layer.Layer, layer.StableID, layer.InputSignature, layer.ReportDigest, layer.Decision, layer.Coverage, layer.RuleFindings})
	}
	return completionJSONSignature(reports)
}

func completionAuditTextValid(value string, minimumRunes int) bool {
	text := strings.TrimSpace(value)
	if len([]rune(text)) < minimumRunes {
		return false
	}
	lower := strings.ToLower(text)
	for _, marker := range []string{"todo", "tbd", "[待补]", "【待补】", "待补全文", "未完成占位"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

func completionContractContradiction(chapter domain.OutlineEntry, prose, summary string) bool {
	body := strings.ToLower(prose + "\n" + summary + "\n" + chapter.CoreEvent)
	hook := strings.ToLower(chapter.Hook)
	// Bare “死亡/继续” vocabulary is common in metaphorical arc language
	// (for example “外援心证行为性死亡，次日继续谈条件”). Only a strong
	// literal-death claim paired with an explicit return can prove a
	// deterministic contradiction without semantic inference.
	terminal := containsAny(body, "死去", "身亡", "永远死去", " dies ", " dead ")
	continues := containsAny(hook, "归来", "复活", "死而复生", "returns", "alive again")
	return terminal && continues
}

func completionSemanticCoverage(summary string, keyEvents, childFacts []string) ([]string, error) {
	parent := completionSemanticText(append([]string{summary}, keyEvents...)...)
	findings := make([]string, 0, len(childFacts))
	seen := make(map[string]struct{}, len(childFacts))
	for _, fact := range childFacts {
		normalized := completionSemanticText(fact)
		if normalized == "" {
			continue
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		if !strings.Contains(parent, normalized) {
			return nil, fmt.Errorf("parent summary does not cover a current child fact")
		}
		// Findings bind the executed rule result without persisting prose.
		findings = append(findings, "fact_covered:"+domain.ContentSignature([]byte(normalized))[:12])
	}
	if len(findings) == 0 {
		return nil, fmt.Errorf("semantic coverage has no executable facts")
	}
	return findings, nil
}

func completionSemanticText(values ...string) string {
	return strings.ToLower(strings.Join(values, "\n"))
}

func completionParentContradiction(summary string, keyEvents, childFacts []string) bool {
	parent := completionSemanticText(append([]string{summary}, keyEvents...)...)
	children := completionSemanticText(childFacts...)
	parentTerminal := containsAny(parent, "死亡", "死去", "身亡", "终局", " dies ", " dead ")
	parentContinues := containsAny(parent, "继续", "归来", "复活", "下一次", "returns", "alive", "continues")
	childTerminal := containsAny(children, "死亡", "死去", "身亡", "终局", " dies ", " dead ")
	childContinues := containsAny(children, "继续", "归来", "复活", "下一次", "returns", "alive", "continues")
	return parentTerminal != childTerminal || parentContinues != childContinues
}

var dramaticFactFields = []string{
	"goal_state", "conflict_state", "choice_state", "cost_state", "result_state",
	"character_before", "character_after", "climax_state", "exit_state", "impact_state",
}

func canonicalDramaticFacts(facts domain.ExpansionDramaticFactSet) []string {
	return []string{
		"goal_state=" + facts.GoalState,
		"conflict_state=" + facts.ConflictState,
		"choice_state=" + facts.ChoiceState,
		"cost_state=" + facts.CostState,
		"result_state=" + facts.ResultState,
		"character_before=" + facts.CharacterBefore,
		"character_after=" + facts.CharacterAfter,
		"climax_state=" + facts.ClimaxState,
		"exit_state=" + facts.ExitState,
		"impact_state=" + facts.ImpactState,
	}
}

func dramaticCoverageLabels() []string {
	labels := make([]string, 0, len(dramaticFactFields))
	for _, field := range dramaticFactFields {
		labels = append(labels, "dramatic_fact:"+field)
	}
	return labels
}

func dramaticFindingLabels(facts []string) []string {
	labels := make([]string, 0, len(facts))
	for _, fact := range facts {
		labels = append(labels, "dramatic_fact_valid:"+domain.ContentSignature([]byte(fact))[:12])
	}
	return labels
}

func dramaticParentCoverage(facts []string) []string {
	if len(facts) == 0 {
		return nil
	}
	return []string{"dramatic_child_facts", "dramatic_fact_order", "dramatic_character_transition"}
}

func dramaticParentFindings(facts []string) []string {
	if len(facts) == 0 {
		return nil
	}
	return []string{"dramatic_children_covered:" + domain.ContentSignature([]byte(strings.Join(facts, "\n")))[:12], "dramatic_parent_consistent"}
}

func dramaticCharacterStageRegresses(facts domain.ExpansionDramaticFactSet) bool {
	rank := map[string]int{"dependent": 0, "passive": 0, "reactive": 1, "active": 2, "proactive": 3, "independent": 4}
	before, beforeOK := rank[facts.CharacterBefore]
	after, afterOK := rank[facts.CharacterAfter]
	return !beforeOK || !afterOK || after <= before
}

// completionDramaticContradiction rejects an explicit parent claim for a
// different typed state. Parents are not allowed to silently rewrite a
// child's signed expansion contract, while ordinary literary prose remains
// independent from the machine-readable state vocabulary.
func completionDramaticContradiction(summary string, keyEvents, childFacts []string) bool {
	if len(childFacts) == 0 {
		return false
	}
	parent := completionSemanticText(append([]string{summary}, keyEvents...)...)
	for _, scopedFact := range childFacts {
		fact := scopedFact
		if separator := strings.LastIndex(fact, "|"); separator >= 0 {
			fact = fact[separator+1:]
		}
		parts := strings.SplitN(fact, "=", 2)
		if len(parts) != 2 {
			return true
		}
		marker := parts[0] + "="
		position := strings.Index(parent, marker)
		if position < 0 {
			continue
		}
		claimed := parent[position+len(marker):]
		if end := strings.IndexAny(claimed, " \t\r\n,;|]"); end >= 0 {
			claimed = claimed[:end]
		}
		if claimed != parts[1] {
			return true
		}
	}
	return false
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func validCompletionLayerReceipts(receipts []completionLayerReceipt, authority string) bool {
	for _, receipt := range receipts {
		signature := receipt.Signature
		receipt.Signature = ""
		if signature == "" || receipt.Authority != authority || receipt.Decision != "pass" ||
			len(receipt.InputSignature) != 64 || receipt.InputSignature != receipt.EvidenceSignature || len(receipt.ReportDigest) != 64 ||
			len(receipt.Coverage) == 0 || len(receipt.RuleFindings) == 0 || signature != completionJSONSignature(receipt) {
			return false
		}
	}
	return true
}

func completionJSONSignature(value any) string {
	payload, _ := json.Marshal(value)
	return domain.ContentSignature(payload)
}

func acceptedCompletionReview(review *domain.ReviewEntry) bool {
	return review != nil && review.Verdict == "accept" && review.CriticalCount() == 0 && review.ErrorCount() == 0
}

func NewAdaptationCompletionRevalidationCheckpoint(revisionID, versionSignature string, previous, current []domain.VolumeOutline) *domain.CompletionRevalidationCheckpoint {
	checkpoint := newCompletionRevalidationCheckpoint(domain.RevisionModeAdaptation, revisionID, versionSignature, previous, current)
	// The adaptation candidate has its own signed contract in addition to the
	// canonical structure signature; preserve it as accepted-version identity.
	checkpoint.AcceptedVersionSignature = versionSignature
	return checkpoint
}

func stableChapterOrder(volumes []domain.VolumeOutline) []string {
	entries := domain.FlattenOutline(volumes)
	order := make([]string, 0, len(entries))
	for _, entry := range entries {
		order = append(order, strings.TrimSpace(entry.ID))
	}
	return order
}

// RefreshCompletionRevalidationEvidence recomputes evidence from authoritative
// current files. The checkpoint remains pending unless every formal chapter,
// its summary, and a new signed completion audit are present.
func (s *Store) RefreshCompletionRevalidationEvidence() error {
	return withRevisionFileTransaction(newIO(s.dir), revisionLockFile, s.refreshCompletionRevalidationEvidenceLocked)
}

func (s *Store) refreshCompletionRevalidationEvidenceLocked() error {
	progress, err := s.Progress.Load()
	if err != nil || progress == nil || progress.CompletionRevalidation == nil {
		return err
	}
	checkpoint := progress.CompletionRevalidation
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return err
	}
	if domain.StructureSignature(volumes) != checkpoint.CurrentStructureSignature || len(checkpoint.AcceptedVersionSignature) != 64 {
		return fmt.Errorf("completion revalidation structure binding is stale")
	}
	if progress.CompletionAuditStatus != "pass" || len(strings.TrimSpace(progress.CompletionAuditReportDigest)) != 64 {
		return fmt.Errorf("completion revalidation requires a new signed completion audit")
	}
	if checkpoint.Mode == domain.RevisionModeNormal {
		_, layers, verifyErr := s.completionLayerAuditEvidenceLocked("normal-completion-independent-auditor", false)
		digest := completionAuditReportDigest(layers)
		if verifyErr != nil || digest != progress.CompletionAuditReportDigest {
			return fmt.Errorf("normal completion audit receipt is stale: %w", verifyErr)
		}
		if receiptErr := s.verifyNormalCompletionAuditReceipt(checkpoint, digest); receiptErr != nil {
			return receiptErr
		}
	} else {
		run, runErr := s.Adaptation.LatestAuditRun()
		if runErr != nil || run == nil || run.Trigger != "completion" || run.Status != "pass" || run.ReportDigest != progress.CompletionAuditReportDigest {
			return fmt.Errorf("adaptation completion audit receipt is stale")
		}
		created, createdErr := time.Parse(time.RFC3339Nano, checkpoint.CreatedAt)
		completed, completedErr := time.Parse(time.RFC3339Nano, run.CompletedAt)
		if createdErr != nil || completedErr != nil || completed.Before(created) {
			return fmt.Errorf("adaptation completion audit predates the accepted revision")
		}
		if receiptErr := s.verifyAdaptationCompletionAuditReceipt(checkpoint, run.RunID, run.InputDigest, run.ReportDigest); receiptErr != nil {
			return receiptErr
		}
	}
	chapterSignatures := make(map[string]string)
	postprocess := make([]string, 0)
	arcSignatures := make([]string, 0)
	volumeSignatures := make([]string, 0)
	for _, volume := range volumes {
		volumeEvidence := make([]string, 0)
		for _, arc := range volume.Arcs {
			arcEvidence := make([]string, 0)
			for _, chapter := range arc.Chapters {
				prose, proseErr := s.Drafts.LoadChapterText(chapter.Chapter)
				summary, summaryErr := s.Summaries.LoadSummary(chapter.Chapter)
				if proseErr != nil || strings.TrimSpace(prose) == "" || summaryErr != nil || summary == nil || strings.TrimSpace(summary.Summary) == "" {
					return fmt.Errorf("completion revalidation evidence is incomplete for chapter %q", chapter.ID)
				}
				summaryPayload, _ := json.Marshal(summary)
				proseSignature := domain.ContentSignature([]byte(prose))
				summarySignature := domain.ContentSignature(summaryPayload)
				review, reviewErr := s.World.LoadReview(chapter.Chapter)
				if reviewErr != nil {
					return fmt.Errorf("load completion revalidation review for chapter %q: %w", chapter.ID, reviewErr)
				}
				legacyBaseline := checkpoint.Mode == domain.RevisionModeNormal &&
					strings.HasPrefix(checkpoint.AcceptedRevisionID, "normal-completion-baseline-")
				if checkpoint.Mode == domain.RevisionModeNormal && !acceptedCompletionReview(review) && !legacyBaseline {
					return fmt.Errorf("completion revalidation review is incomplete for chapter %q", chapter.ID)
				}
				reviewSignature := "adaptation-completion-run"
				if acceptedCompletionReview(review) {
					reviewSignature = completionJSONSignature(review)
				} else if legacyBaseline {
					reviewSignature = domain.ContentSignature([]byte(
						"legacy-independent-completion-audit:" + progress.CompletionAuditReportDigest + ":" + chapter.ID,
					))
				}
				dramaticSignature := completionJSONSignature([]string(nil))
				if chapter.DramaticFacts != nil {
					if chapter.DramaticFacts.Validate() != nil || dramaticCharacterStageRegresses(*chapter.DramaticFacts) {
						return fmt.Errorf("completion revalidation dramatic facts are invalid for chapter %q", chapter.ID)
					}
					dramaticSignature = completionJSONSignature(canonicalDramaticFacts(*chapter.DramaticFacts))
				}
				binding := domain.ContentSignature([]byte(checkpoint.AcceptedRevisionID + ":" + checkpoint.CurrentStructureSignature + ":" + chapter.ID + ":" + proseSignature + ":" + summarySignature + ":" + reviewSignature + ":" + dramaticSignature))
				chapterSignatures[chapter.ID] = binding
				postprocess = append(postprocess, chapter.ID+":"+summarySignature)
				arcEvidence = append(arcEvidence, chapter.ID+":"+binding)
			}
			arcSummary, _ := s.Summaries.LoadArcSummary(volume.Index, arc.Index)
			arcSignature := domain.ContentSignature([]byte(checkpoint.AcceptedRevisionID + ":" + checkpoint.CurrentStructureSignature + ":arc:" + arc.ID + ":" + completionJSONSignature(arcSummary) + ":" + strings.Join(arcEvidence, "\n")))
			arcSignatures = append(arcSignatures, arc.ID+":"+arcSignature)
			volumeEvidence = append(volumeEvidence, arc.ID+":"+arcSignature)
		}
		volumeSummary, _ := s.Summaries.LoadVolumeSummary(volume.Index)
		volumeSignature := domain.ContentSignature([]byte(checkpoint.AcceptedRevisionID + ":" + checkpoint.CurrentStructureSignature + ":volume:" + volume.ID + ":" + completionJSONSignature(volumeSummary) + ":" + strings.Join(volumeEvidence, "\n")))
		volumeSignatures = append(volumeSignatures, volume.ID+":"+volumeSignature)
	}
	checkpoint.ChapterSignatures = chapterSignatures
	checkpoint.PostprocessSignature = domain.ContentSignature([]byte(checkpoint.CurrentStructureSignature + ":postprocess:" + strings.Join(postprocess, "\n")))
	checkpoint.ArcAuditSignature = domain.ContentSignature([]byte(checkpoint.CurrentStructureSignature + ":arcs:" + strings.Join(arcSignatures, "\n")))
	checkpoint.VolumeAuditSignature = domain.ContentSignature([]byte(checkpoint.CurrentStructureSignature + ":volumes:" + strings.Join(volumeSignatures, "\n")))
	checkpoint.BookAuditSignature = domain.ContentSignature([]byte(checkpoint.AcceptedRevisionID + ":" + checkpoint.AcceptedVersionSignature + ":" + checkpoint.CurrentStructureSignature + ":book:" + checkpoint.VolumeAuditSignature + ":" + progress.CompletionAuditReportDigest))
	checkpoint.Status = "ready"
	return s.Progress.saveOwned(progress)
}
