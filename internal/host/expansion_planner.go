package host

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

var (
	ErrExpansionPreviewNotFound        = errors.New("expansion preview not found")
	ErrExpansionDependencyTaskNotFound = errors.New("expansion dependency task not found")
	ErrExpansionPreviewStale           = errors.New("expansion preview is stale")
	ErrExpansionPreviewExpired         = errors.New("expansion preview expired")
	ErrExpansionPreviewCancelled       = errors.New("expansion preview cancelled")
	ErrExpansionPreviewSealInvalidated = errors.New("expansion preview seal invalidated by restart")
)

type ExpansionRecommendationPlanner interface {
	RecommendExpansion(context.Context, ExpansionContext, domain.ExpansionRequest) (domain.ExpansionRecommendation, error)
}

type ExpansionConfirmation struct {
	PreviewID string                  `json:"preview_id"`
	Revision  *domain.RevisionSession `json:"revision"`
	Replay    bool                    `json:"replay"`
}

type ExpansionPlanner struct {
	store            *storepkg.Store
	recommender      ExpansionRecommendationPlanner
	kernel           StructurePlanningKernel
	now              func() time.Time
	ttl              time.Duration
	seal             []byte
	auditorPublicKey ed25519.PublicKey

	mu                      sync.Mutex
	previews                map[string]*domain.ExpansionPreview
	receipts                map[string]storepkg.ExpansionCommandReceipt
	pendingAudits           map[string]ExpansionAuditTask
	pendingDependencyAudits map[string]ExpansionDependencyAuditTask
	auditArtifacts          map[string]ExpansionAuditArtifact
	dependencyReviews       map[string]domain.ExpansionDependencyReview
	dependencyReviewIndex   map[string]string
	pendingAdjustments      map[string]storepkg.ExpansionAdjustmentTransaction
	initErr                 error
}

func NewExpansionPlanner(st *storepkg.Store, recommender ExpansionRecommendationPlanner) *ExpansionPlanner {
	trust, trustErr := st.LoadExpansionAuditorTrust()
	key, decodeErr := hex.DecodeString(trust.PublicKeyHex)
	return newExpansionPlannerWithPublicKey(st, recommender, ed25519.PublicKey(key), errors.Join(trustErr, decodeErr))
}

func newExpansionPlannerWithPublicKey(st *storepkg.Store, recommender ExpansionRecommendationPlanner, publicKey ed25519.PublicKey, trustErr error) *ExpansionPlanner {
	runtime, loadErr := st.LoadExpansionRuntime()
	seal, decodeErr := hex.DecodeString(runtime.SealHex)
	if decodeErr != nil || len(seal) != 32 {
		seal = make([]byte, 32)
		if _, err := rand.Read(seal); err != nil {
			panic(fmt.Sprintf("initialize expansion preview seal: %v", err))
		}
	}
	pending := make(map[string]ExpansionAuditTask, len(runtime.PendingAudits))
	for id, payload := range runtime.PendingAudits {
		var task ExpansionAuditTask
		if err := json.Unmarshal(payload, &task); err != nil {
			loadErr = errors.Join(loadErr, err)
			continue
		}
		pending[id] = task
	}
	artifacts := make(map[string]ExpansionAuditArtifact, len(runtime.AuditArtifacts))
	for id, payload := range runtime.AuditArtifacts {
		var artifact ExpansionAuditArtifact
		if err := json.Unmarshal(payload, &artifact); err != nil {
			loadErr = errors.Join(loadErr, err)
			continue
		}
		artifacts[id] = artifact
	}
	pendingDependencies := make(map[string]ExpansionDependencyAuditTask, len(runtime.PendingDependencyAudits))
	for id, payload := range runtime.PendingDependencyAudits {
		var task ExpansionDependencyAuditTask
		if err := json.Unmarshal(payload, &task); err != nil {
			loadErr = errors.Join(loadErr, err)
			continue
		}
		pendingDependencies[id] = task
	}
	planner := &ExpansionPlanner{store: st, recommender: recommender, now: time.Now, ttl: 30 * time.Minute, seal: seal, auditorPublicKey: slices.Clone(publicKey), previews: runtime.Previews, receipts: runtime.Receipts, pendingAudits: pending, pendingDependencyAudits: pendingDependencies, auditArtifacts: artifacts, dependencyReviews: runtime.DependencyReviews, dependencyReviewIndex: runtime.DependencyReviewIndex, pendingAdjustments: runtime.PendingAdjustments, initErr: errors.Join(loadErr, decodeErr, trustErr)}
	if planner.pendingAdjustments == nil {
		planner.pendingAdjustments = make(map[string]storepkg.ExpansionAdjustmentTransaction)
	}
	if len(publicKey) != ed25519.PublicKeySize {
		planner.initErr = errors.Join(planner.initErr, fmt.Errorf("trusted expansion auditor public key is unavailable"))
	}
	kernelSeal, kernelDecodeErr := hex.DecodeString(runtime.KernelSealHex)
	if kernelDecodeErr == nil && len(kernelSeal) == 32 {
		planner.kernel.restoreSigningKey(kernelSeal)
	} else {
		planner.kernel.restoreSigningKey(newStructurePreviewSigningKey())
	}
	planner.initErr = errors.Join(planner.initErr, kernelDecodeErr)
	if recoverErr := planner.recoverAdjustmentsLocked(); recoverErr != nil {
		planner.initErr = errors.Join(planner.initErr, recoverErr)
	}
	if loadErr == nil && (runtime.SealHex == "" || runtime.KernelSealHex == "") {
		planner.initErr = planner.persistLocked()
	}
	return planner
}

func (planner *ExpansionPlanner) persistLocked() error {
	if planner == nil || planner.store == nil {
		return fmt.Errorf("expansion planner runtime is unavailable")
	}
	pending := make(map[string]json.RawMessage, len(planner.pendingAudits))
	for id, task := range planner.pendingAudits {
		pending[id], _ = json.Marshal(task)
	}
	artifacts := make(map[string]json.RawMessage, len(planner.auditArtifacts))
	for id, artifact := range planner.auditArtifacts {
		artifacts[id], _ = json.Marshal(artifact)
	}
	pendingDependencies := make(map[string]json.RawMessage, len(planner.pendingDependencyAudits))
	for id, task := range planner.pendingDependencyAudits {
		pendingDependencies[id], _ = json.Marshal(task)
	}
	return planner.store.SaveExpansionRuntime(storepkg.ExpansionRuntime{Version: 1, SealHex: hex.EncodeToString(planner.seal), KernelSealHex: hex.EncodeToString(planner.kernel.signingKeyCopy()), Previews: planner.previews, Receipts: planner.receipts, PendingAudits: pending, PendingDependencyAudits: pendingDependencies, AuditArtifacts: artifacts, DependencyReviews: planner.dependencyReviews, DependencyReviewIndex: planner.dependencyReviewIndex, PendingAdjustments: planner.pendingAdjustments})
}

func (planner *ExpansionPlanner) recoverAdjustmentsLocked() error {
	if planner == nil || len(planner.pendingAdjustments) == 0 {
		return nil
	}
	active, err := planner.store.Revisions.Active()
	if err != nil {
		return fmt.Errorf("recover expansion adjustment revision: %w", err)
	}
	changed := false
	for key, transaction := range planner.pendingAdjustments {
		source := planner.previews[transaction.SourcePreviewID]
		next := planner.previews[transaction.NextPreviewID]
		if source == nil || next == nil || active == nil || active.ID != transaction.RevisionID {
			return fmt.Errorf("recover expansion adjustment %s: durable preview or revision binding is missing", key)
		}
		switch active.PreviewSignature {
		case transaction.PreviousRevisionSignature:
			// Rebind never committed. Keep the old revision binding and discard
			// the unbound prepared successor.
			source.Obsolete = false
			delete(planner.previews, transaction.NextPreviewID)
			delete(planner.pendingAdjustments, key)
			changed = true
		case transaction.NextRevisionSignature:
			// Rebind committed before the expansion runtime commit. Complete the
			// exact prepared result and receipt; this is safe to replay.
			source.Obsolete = true
			payload, marshalErr := json.Marshal(next)
			if marshalErr != nil {
				return marshalErr
			}
			planner.receipts[key] = storepkg.ExpansionCommandReceipt{
				Operation: "adjust", Fingerprint: transaction.Fingerprint, PreviewID: next.ID,
				Result: payload, ResultSignature: domain.JSONContentSignature(payload),
			}
			delete(planner.pendingAdjustments, key)
			changed = true
		default:
			return fmt.Errorf("recover expansion adjustment %s: revision preview binding is neither prepared predecessor nor successor", key)
		}
	}
	if changed {
		return planner.persistLocked()
	}
	return nil
}

func expansionFingerprint(value any) string {
	payload, _ := json.Marshal(value)
	return domain.JSONContentSignature(payload)
}

func (planner *ExpansionPlanner) replayReceiptLocked(key, operation, fingerprint string) (storepkg.ExpansionCommandReceipt, bool, error) {
	receipt, ok := planner.receipts[key]
	if !ok {
		return storepkg.ExpansionCommandReceipt{}, false, nil
	}
	if receipt.Operation != operation || receipt.Fingerprint != fingerprint {
		return storepkg.ExpansionCommandReceipt{}, false, storepkg.ErrManuscriptIdempotencyConflict
	}
	return receipt, true, nil
}

func (planner *ExpansionPlanner) recordReceiptLocked(key, operation, fingerprint, previewID, revisionID string) error {
	planner.receipts[key] = storepkg.ExpansionCommandReceipt{Operation: operation, Fingerprint: fingerprint, PreviewID: previewID, RevisionID: revisionID}
	return planner.persistLocked()
}

func (planner *ExpansionPlanner) recordReceiptResultLocked(key, operation, fingerprint, previewID, revisionID string, result any) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	planner.receipts[key] = storepkg.ExpansionCommandReceipt{
		Operation: operation, Fingerprint: fingerprint, PreviewID: previewID, RevisionID: revisionID,
		Result: payload, ResultSignature: domain.JSONContentSignature(payload),
	}
	return planner.persistLocked()
}

func decodeExpansionReceiptResult[T any](receipt storepkg.ExpansionCommandReceipt) (*T, error) {
	if len(receipt.Result) == 0 || domain.JSONContentSignature(receipt.Result) != receipt.ResultSignature {
		return nil, ErrExpansionPreviewSealInvalidated
	}
	var result T
	if err := json.Unmarshal(receipt.Result, &result); err != nil {
		return nil, errors.Join(ErrExpansionPreviewSealInvalidated, err)
	}
	return &result, nil
}

func (planner *ExpansionPlanner) Plan(ctx context.Context, request domain.ExpansionRequest) (*domain.ExpansionPreview, error) {
	if err := planner.requireWriteReady(); err != nil {
		return nil, err
	}
	if planner == nil || planner.store == nil || planner.recommender == nil {
		return nil, fmt.Errorf("expansion planner runtime is unavailable")
	}
	if request.Adjustment == "" {
		request.Adjustment = domain.ExpansionAdjustmentDefault
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	contextSnapshot, err := BuildExpansionContext(planner.store, request)
	if err != nil {
		return nil, err
	}
	if err := planner.reviewExpansionContextDependencies(ctx, &contextSnapshot); err != nil {
		return nil, err
	}
	if request.ExpectedStructureSignature != contextSnapshot.StructureSignature {
		return nil, ErrExpansionPreviewStale
	}
	if request.ExpectedStructureRevision != domain.StructureRevision(contextSnapshot.Structure) {
		return nil, ErrExpansionPreviewStale
	}
	fingerprint := expansionFingerprint(request)
	planner.mu.Lock()
	defer planner.mu.Unlock()
	if planner.initErr != nil {
		return nil, planner.initErr
	}
	receiptKey := "plan:" + request.IdempotencyKey
	if receipt, ok, receiptErr := planner.replayReceiptLocked(receiptKey, "plan", fingerprint); receiptErr != nil {
		return nil, receiptErr
	} else if ok {
		existing := planner.previews[receipt.PreviewID]
		if existing != nil {
			copy := cloneExpansionPreview(*existing)
			return &copy, nil
		}
		return nil, ErrExpansionPreviewSealInvalidated
	}

	preview, err := planner.prepareExpansionPreview(ctx, request, contextSnapshot)
	if err != nil {
		return nil, err
	}
	// Re-check authoritative structure after model/kernel work and before the
	// single durable runtime write.
	current, err := planner.store.Outline.LoadLayeredOutline()
	if err != nil || domain.StructureSignature(current) != contextSnapshot.StructureSignature {
		return nil, errors.Join(ErrExpansionPreviewStale, err)
	}
	planner.previews[preview.ID] = preview
	if err := planner.recordReceiptLocked(receiptKey, "plan", fingerprint, preview.ID, ""); err != nil {
		delete(planner.previews, preview.ID)
		return nil, err
	}
	copy := cloneExpansionPreview(*preview)
	return &copy, nil
}

func (planner *ExpansionPlanner) prepareExpansionPreview(ctx context.Context, request domain.ExpansionRequest, contextSnapshot ExpansionContext) (*domain.ExpansionPreview, error) {
	recommendation, err := planner.recommender.RecommendExpansion(ctx, contextSnapshot, request)
	if err != nil {
		return nil, fmt.Errorf("recommend expansion: %w", err)
	}
	recommendation.Location = request.Location
	if err := recommendation.Validate(contextSnapshot.Mode); err != nil {
		return nil, fmt.Errorf("validate expansion recommendation: %w", err)
	}
	for _, batch := range contextSnapshot.DependencyBatches {
		for _, review := range batch.ReviewReceipts {
			if err := verifyExpansionDependencyReview(review, planner.auditorPublicKey); err != nil {
				return nil, fmt.Errorf("dependency audit gate: %w", err)
			}
			if review.Decision != "pass" {
				return nil, &domain.ManuscriptRevisionError{Class: "dependency_audit_needs_fix", Err: fmt.Errorf("dependency audit gate %s/%s needs fix: %s", review.Stage, review.ScopeID, strings.Join(review.Findings, "; "))}
			}
			recommendation.AuditChain = append(recommendation.AuditChain, "dependency-review:"+review.Stage+":"+review.ScopeID+":"+review.ArtifactSignature)
		}
	}
	if contextSnapshot.Mode == domain.RevisionModeNormal {
		if err := rejectNormalExpansionModeFields(recommendation); err != nil {
			return nil, err
		}
	} else {
		if contextSnapshot.Adaptation == nil || contextSnapshot.Adaptation.Plan == nil || contextSnapshot.Adaptation.Manifest == nil || recommendation.AdaptationCandidate == nil {
			return nil, fmt.Errorf("adaptation expansion contract is incomplete")
		}
		if err := domain.ValidateAdaptationRevisionPlan(*contextSnapshot.Adaptation.Plan, *recommendation.AdaptationCandidate, contextSnapshot.Adaptation.Manifest); err != nil {
			return nil, fmt.Errorf("validate adaptation expansion contracts: %w", err)
		}
	}
	if err := validateExpansionForm(recommendation); err != nil {
		return nil, err
	}
	candidate, sealedSteps, impacts, err := planner.executeBundle(ctx, contextSnapshot, request, recommendation)
	if err != nil {
		return nil, err
	}
	if err := validateExpansionActualDelta(recommendation, contextSnapshot.Structure, candidate); err != nil {
		return nil, err
	}
	if contextSnapshot.Mode == domain.RevisionModeAdaptation {
		candidate, err = canonicalAdaptationExpansionCandidate(candidate, *recommendation.AdaptationCandidate)
		if err != nil {
			return nil, err
		}
		if err := validateAdaptationExpansionProjection(candidate, *recommendation.AdaptationCandidate); err != nil {
			return nil, err
		}
	}
	previewID := newExpansionPreviewID()
	candidate, err = bindExpansionOrigins(previewID, contextSnapshot.Structure, candidate, recommendation.OrderedOperations, recommendation.Assessment.TypedClaims)
	if err != nil {
		return nil, err
	}
	if len(sealedSteps) == 0 {
		return nil, fmt.Errorf("one-line expansion has no sealed structure step")
	}
	sealedSteps[len(sealedSteps)-1].Proposal.Candidate = domain.CloneStructureSnapshot(candidate)
	sealedSteps[len(sealedSteps)-1].Signature, err = planner.kernel.signStructureRevisionPreview(sealedSteps[len(sealedSteps)-1])
	if err != nil {
		return nil, err
	}
	if recommendation.AdaptationCandidate != nil {
		bindAdaptationExpansionOrigins(recommendation.AdaptationCandidate, candidate)
	}
	recommendation.Impacts = impacts
	preview := &domain.ExpansionPreview{
		ID: previewID, Mode: contextSnapshot.Mode, Request: request, Recommendation: recommendation,
		Candidate: candidate, BaseRevision: request.ExpectedStructureRevision,
		BaseStructureSignature: contextSnapshot.StructureSignature, CandidateSignature: domain.StructureSignature(candidate),
		ExpiresAt: planner.now().UTC().Add(planner.ttl), KernelPreviews: sealedSteps,
	}
	for _, batch := range contextSnapshot.DependencyBatches {
		preview.DependencyReviews = append(preview.DependencyReviews, batch.ReviewReceipts...)
	}
	if preview.Mode == domain.RevisionModeAdaptation {
		sealedAdaptation, sealErr := NewAdaptationRevisionService(planner.store).SealExpansionCandidate(AdaptationRevisionPreviewRequest{Intent: request.Sentence, Candidate: *recommendation.AdaptationCandidate, RequireAddedProse: true})
		if sealErr != nil {
			return nil, sealErr
		}
		preview.RevisionPreviewSignature = sealedAdaptation.Signature
	}
	preview.Signature, err = planner.signPreview(*preview)
	if err != nil {
		return nil, err
	}
	return preview, nil
}

func bindAdaptationExpansionOrigins(plan *domain.AdaptationPlan, candidate []domain.VolumeOutline) {
	if plan == nil {
		return
	}
	byID := make(map[string]domain.OutlineEntry)
	for _, chapter := range domain.FlattenOutline(candidate) {
		byID[chapter.ID] = chapter
	}
	for index := range plan.Chapters {
		if chapter, ok := byID[plan.Chapters[index].ID]; ok {
			plan.Chapters[index].OutlineEntry.ExpansionOrigin = chapter.ExpansionOrigin
			plan.Chapters[index].OutlineEntry.DramaticFacts = chapter.DramaticFacts
		}
	}
}

func bindExpansionOrigins(previewID string, base, candidate []domain.VolumeOutline, operations []domain.ExpansionOperation, facts *domain.ExpansionDramaticFactSet) ([]domain.VolumeOutline, error) {
	if facts == nil || facts.Validate() != nil {
		return nil, fmt.Errorf("one-line expansion requires a complete dramatic source contract")
	}
	baseChapters := make(map[string]struct{})
	for _, chapter := range domain.FlattenOutline(domain.ProjectLayeredOutlineOrder(base)) {
		baseChapters[chapter.ID] = struct{}{}
	}
	affected := make(map[string]struct{})
	for _, operation := range operations {
		if id := strings.TrimSpace(operation.TargetID); id != "" &&
			(operation.Operation == domain.StructureRevisionExpandChapter || operation.Operation == domain.StructureRevisionSplitChapter) {
			affected[id] = struct{}{}
		}
	}
	for _, chapter := range domain.FlattenOutline(domain.ProjectLayeredOutlineOrder(candidate)) {
		if _, existed := baseChapters[chapter.ID]; !existed {
			affected[chapter.ID] = struct{}{}
		}
	}
	result := domain.CloneStructureSnapshot(candidate)
	changed := 0
	for volumeIndex := range result {
		for arcIndex := range result[volumeIndex].Arcs {
			for chapterIndex := range result[volumeIndex].Arcs[arcIndex].Chapters {
				chapter := &result[volumeIndex].Arcs[arcIndex].Chapters[chapterIndex]
				if _, isExpansionChapter := affected[chapter.ID]; !isExpansionChapter {
					continue
				}
				if chapter.DramaticFacts == nil {
					copy := *facts
					chapter.DramaticFacts = &copy
				}
				if chapter.DramaticFacts.Validate() != nil {
					return nil, fmt.Errorf("one-line expansion chapter %q is not bound to its dramatic source contract", chapter.ID)
				}
				origin, err := domain.NewExpansionOrigin(previewID, *chapter.DramaticFacts)
				if err != nil {
					return nil, err
				}
				chapter.ExpansionOrigin = &origin
				changed++
			}
		}
	}
	if changed == 0 {
		return nil, fmt.Errorf("one-line expansion did not produce a provenance-bound chapter")
	}
	return domain.ProjectLayeredOutlineOrder(result), nil
}

// validateAdaptationExpansionProjection makes the kernel candidate and the
// contract candidate one authoritative artifact: stable target identities,
// target display order, and volume ownership must describe the same result.
// Source chapter numbers deliberately remain outside this projection.
func validateAdaptationExpansionProjection(candidate []domain.VolumeOutline, plan domain.AdaptationPlan) error {
	chapters := domain.FlattenOutline(domain.ProjectLayeredOutlineOrder(candidate))
	if len(chapters) != len(plan.Chapters) {
		return fmt.Errorf("adaptation expansion kernel/contract chapter count mismatch: %d != %d", len(chapters), len(plan.Chapters))
	}
	for index := range chapters {
		contract := plan.Chapters[index]
		if chapters[index].ID != contract.ID || chapters[index].Chapter != contract.Chapter ||
			chapters[index].Title != contract.Title || chapters[index].CoreEvent != contract.CoreEvent ||
			chapters[index].Hook != contract.Hook || !slices.Equal(chapters[index].Scenes, contract.Scenes) {
			return fmt.Errorf("adaptation expansion kernel/contract target projection mismatch at display chapter %d", index+1)
		}
	}
	if len(plan.Volumes) > 0 {
		ordered := domain.ProjectLayeredOutlineOrder(candidate)
		if len(ordered) != len(plan.Volumes) {
			return fmt.Errorf("adaptation expansion kernel/contract volume count mismatch")
		}
		for index := range ordered {
			if ordered[index].ID != plan.Volumes[index].ID || ordered[index].Index != plan.Volumes[index].Index ||
				ordered[index].Title != plan.Volumes[index].Title || ordered[index].Theme != plan.Volumes[index].Theme {
				return fmt.Errorf("adaptation expansion kernel/contract volume projection mismatch at %d", index+1)
			}
		}
	}
	return nil
}

// canonicalAdaptationExpansionCandidate removes the former dual-candidate
// ambiguity by projecting every shared structural field from the validated
// adaptation contract into the kernel artifact. Non-shared arc identities stay
// kernel-owned; all source/coverage/ownership contracts stay plan-owned and are
// covered by the adaptation preview signature.
func canonicalAdaptationExpansionCandidate(candidate []domain.VolumeOutline, plan domain.AdaptationPlan) ([]domain.VolumeOutline, error) {
	result := domain.CloneStructureSnapshot(candidate)
	chapterByID := make(map[string]domain.AdaptationChapterPlan, len(plan.Chapters))
	for _, chapter := range plan.Chapters {
		chapterByID[chapter.ID] = chapter
	}
	volumeByID := make(map[string]domain.AdaptationVolumePlan, len(plan.Volumes))
	for _, volume := range plan.Volumes {
		volumeByID[volume.ID] = volume
	}
	for volumeIndex := range result {
		if len(plan.Volumes) > 0 {
			contract, ok := volumeByID[result[volumeIndex].ID]
			if !ok {
				return nil, fmt.Errorf("adaptation contract is missing kernel volume %q", result[volumeIndex].ID)
			}
			result[volumeIndex].Index, result[volumeIndex].Title, result[volumeIndex].Theme = contract.Index, contract.Title, contract.Theme
		}
		for arcIndex := range result[volumeIndex].Arcs {
			for chapterIndex := range result[volumeIndex].Arcs[arcIndex].Chapters {
				entry := &result[volumeIndex].Arcs[arcIndex].Chapters[chapterIndex]
				contract, ok := chapterByID[entry.ID]
				if !ok {
					return nil, fmt.Errorf("adaptation contract is missing kernel chapter %q", entry.ID)
				}
				*entry = contract.OutlineEntry
				entry.Chapter = contract.Chapter
				entry.Title = contract.Title
			}
		}
	}
	return domain.ProjectLayeredOutlineOrder(result), nil
}

func (planner *ExpansionPlanner) Adjust(ctx context.Context, previewID string, expectedRevision int, adjustment domain.ExpansionAdjustment, sentence, idempotencyKey string) (*domain.ExpansionPreview, error) {
	if err := planner.requireWriteReady(); err != nil {
		return nil, err
	}
	planner.mu.Lock()
	storedSource := planner.previews[strings.TrimSpace(previewID)]
	if storedSource == nil {
		planner.mu.Unlock()
		return nil, ErrExpansionPreviewNotFound
	}
	previous := cloneExpansionPreview(*storedSource)
	planner.mu.Unlock()
	if expectedRevision <= 0 || expectedRevision != previous.BaseRevision {
		return nil, ErrExpansionPreviewStale
	}
	request := previous.Request
	request.Adjustment = adjustment
	if strings.TrimSpace(sentence) != "" {
		request.Sentence = strings.TrimSpace(sentence)
	}
	request.IdempotencyKey = strings.TrimSpace(idempotencyKey)
	request.ClientRequestID = ""
	if err := request.Validate(); err != nil {
		return nil, err
	}
	fingerprint := expansionFingerprint(struct {
		SourcePreviewID        string                     `json:"source_preview_id"`
		SourcePreviewSignature string                     `json:"source_preview_signature"`
		BaseSignature          string                     `json:"base_structure_signature"`
		ExpectedRevision       int                        `json:"expected_revision"`
		Adjustment             domain.ExpansionAdjustment `json:"adjustment"`
		Sentence               string                     `json:"sentence"`
	}{previous.ID, previous.Signature, previous.BaseStructureSignature, expectedRevision, adjustment, request.Sentence})
	receiptKey := "adjust:" + request.IdempotencyKey
	planner.mu.Lock()
	if err := planner.recoverAdjustmentsLocked(); err != nil {
		planner.mu.Unlock()
		return nil, err
	}
	if receipt, ok, replayErr := planner.replayReceiptLocked(receiptKey, "adjust", fingerprint); replayErr != nil {
		planner.mu.Unlock()
		return nil, replayErr
	} else if ok {
		result, decodeErr := decodeExpansionReceiptResult[domain.ExpansionPreview](receipt)
		planner.mu.Unlock()
		return result, decodeErr
	}
	planner.mu.Unlock()
	if previous.Obsolete || previous.Cancelled {
		return nil, ErrExpansionPreviewStale
	}

	snapshot, err := BuildExpansionContext(planner.store, request)
	if err != nil {
		return nil, err
	}
	if err := planner.reviewExpansionContextDependencies(ctx, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.StructureSignature != previous.BaseStructureSignature || domain.StructureRevision(snapshot.Structure) != expectedRevision {
		return nil, ErrExpansionPreviewStale
	}
	next, err := planner.prepareExpansionPreview(ctx, request, snapshot)
	if err != nil {
		return nil, err
	}
	planner.mu.Lock()
	defer planner.mu.Unlock()
	if receipt, ok, replayErr := planner.replayReceiptLocked(receiptKey, "adjust", fingerprint); replayErr != nil {
		return nil, replayErr
	} else if ok {
		return decodeExpansionReceiptResult[domain.ExpansionPreview](receipt)
	}
	currentSource := planner.previews[previous.ID]
	if currentSource == nil || currentSource.Signature != previous.Signature || currentSource.Obsolete || currentSource.Cancelled {
		return nil, ErrExpansionPreviewStale
	}
	current, err := planner.store.Outline.LoadLayeredOutline()
	if err != nil || domain.StructureSignature(current) != previous.BaseStructureSignature {
		return nil, errors.Join(ErrExpansionPreviewStale, err)
	}
	next.ConfirmedRevisionID = previous.ConfirmedRevisionID
	if next.ConfirmedRevisionID != "" {
		next.Signature, err = planner.signPreview(*next)
		if err != nil {
			currentSource.Obsolete = false
			return nil, err
		}
	}
	if previous.ConfirmedRevisionID != "" {
		active, loadErr := planner.store.Revisions.Active()
		if loadErr != nil || active == nil || active.ID != previous.ConfirmedRevisionID {
			return nil, errors.Join(ErrExpansionPreviewStale, loadErr)
		}
		transaction := storepkg.ExpansionAdjustmentTransaction{
			OperationKey: receiptKey, Fingerprint: fingerprint, SourcePreviewID: currentSource.ID,
			NextPreviewID: next.ID, RevisionID: active.ID, PreviousRevisionSignature: active.PreviewSignature,
			NextRevisionSignature: next.RevisionPreviewSignature, RebindIdempotencyKey: request.IdempotencyKey + ":rebind",
		}
		if active.Mode != domain.RevisionModeAdaptation {
			transaction.NextRevisionSignature = next.Signature
		}
		planner.previews[next.ID] = next
		planner.pendingAdjustments[receiptKey] = transaction
		if err := planner.persistLocked(); err != nil {
			delete(planner.previews, next.ID)
			delete(planner.pendingAdjustments, receiptKey)
			return nil, err
		}
		var rebindErr error
		if active.Mode == domain.RevisionModeAdaptation {
			_, rebindErr = NewAdaptationRevisionService(planner.store).RebindExpansionPreviewAfterFeedback(active, transaction.PreviousRevisionSignature, transaction.NextRevisionSignature, transaction.RebindIdempotencyKey)
		} else {
			_, rebindErr = planner.store.Revisions.RebindPreviewAfterFeedback(domain.NormalRevisionPolicy{}, storepkg.RebindRevisionPreviewInput{RevisionMutationInput: storepkg.RevisionMutationInput{SessionID: active.ID, ExpectedRevision: active.Revision, IdempotencyKey: transaction.RebindIdempotencyKey}, PreviousSignature: transaction.PreviousRevisionSignature, NextSignature: transaction.NextRevisionSignature})
		}
		if rebindErr != nil {
			// The revision store may have atomically installed the successor and
			// then failed while cleaning its replacement backup. Re-read the
			// authoritative binding before deciding whether to commit or roll back
			// the prepared expansion transaction.
			if reconcileErr := planner.reconcileAdjustmentAfterRebindErrorLocked(receiptKey, transaction); reconcileErr != nil {
				return nil, errors.Join(rebindErr, reconcileErr)
			}
			return nil, rebindErr
		}
		currentSource.Obsolete = true
		payload, marshalErr := json.Marshal(next)
		if marshalErr != nil {
			return nil, marshalErr
		}
		planner.receipts[receiptKey] = storepkg.ExpansionCommandReceipt{Operation: "adjust", Fingerprint: fingerprint, PreviewID: next.ID, Result: payload, ResultSignature: domain.JSONContentSignature(payload)}
		delete(planner.pendingAdjustments, receiptKey)
		if err := planner.persistLocked(); err != nil {
			// Keep the in-memory state aligned with the durable prepare record.
			// A retry or restart observes the revision's new binding and commits.
			currentSource.Obsolete = false
			delete(planner.receipts, receiptKey)
			planner.pendingAdjustments[receiptKey] = transaction
			return nil, err
		}
	} else {
		// Without a confirmed revision the source tombstone, successor and
		// receipt remain one expansion-runtime atomic write.
		currentSource.Obsolete = true
		planner.previews[next.ID] = next
		if err := planner.recordReceiptResultLocked(receiptKey, "adjust", fingerprint, next.ID, "", next); err != nil {
			currentSource.Obsolete = false
			delete(planner.previews, next.ID)
			delete(planner.receipts, receiptKey)
			return nil, err
		}
	}
	copy := cloneExpansionPreview(*next)
	return &copy, nil
}

func (planner *ExpansionPlanner) reconcileAdjustmentAfterRebindErrorLocked(key string, transaction storepkg.ExpansionAdjustmentTransaction) error {
	active, loadErr := planner.store.Revisions.Active()
	if loadErr != nil || active == nil || active.ID != transaction.RevisionID {
		// Keep the prepare record intact. A restart can safely resolve it once
		// the authoritative revision becomes readable again.
		return errors.Join(fmt.Errorf("re-read authoritative adjustment binding"), loadErr, planner.persistLocked())
	}
	source := planner.previews[transaction.SourcePreviewID]
	next := planner.previews[transaction.NextPreviewID]
	if source == nil || next == nil {
		return fmt.Errorf("prepared adjustment preview binding is missing")
	}
	switch active.PreviewSignature {
	case transaction.PreviousRevisionSignature:
		source.Obsolete = false
		delete(planner.previews, transaction.NextPreviewID)
		delete(planner.pendingAdjustments, key)
		return planner.persistLocked()
	case transaction.NextRevisionSignature:
		source.Obsolete = true
		payload, err := json.Marshal(next)
		if err != nil {
			return err
		}
		planner.receipts[key] = storepkg.ExpansionCommandReceipt{Operation: "adjust", Fingerprint: transaction.Fingerprint, PreviewID: next.ID, Result: payload, ResultSignature: domain.JSONContentSignature(payload)}
		delete(planner.pendingAdjustments, key)
		return planner.persistLocked()
	default:
		return fmt.Errorf("authoritative adjustment binding is neither predecessor nor successor")
	}
}

func (planner *ExpansionPlanner) Get(previewID string) (*domain.ExpansionPreview, error) {
	if planner == nil {
		return nil, ErrExpansionPreviewSealInvalidated
	}
	planner.mu.Lock()
	defer planner.mu.Unlock()
	preview := planner.previews[strings.TrimSpace(previewID)]
	if preview == nil {
		return nil, ErrExpansionPreviewNotFound
	}
	copy := cloneExpansionPreview(*preview)
	return &copy, nil
}

func (planner *ExpansionPlanner) ActiveRevision() (*domain.RevisionSession, error) {
	if planner == nil || planner.store == nil {
		return nil, fmt.Errorf("expansion planner runtime is unavailable")
	}
	return planner.store.Revisions.Active()
}

func (planner *ExpansionPlanner) RevisionCommand(action, message string, expectedRevision int, idempotencyKey string) (*domain.RevisionSession, error) {
	if err := planner.requireWriteReady(); err != nil {
		return nil, err
	}
	if planner == nil || planner.store == nil || strings.TrimSpace(idempotencyKey) == "" || expectedRevision <= 0 {
		return nil, fmt.Errorf("expansion revision command, expected revision, and idempotency key are required")
	}
	planner.mu.Lock()
	defer planner.mu.Unlock()
	operation := "revision:" + strings.TrimSpace(action)
	fingerprint := expansionFingerprint(struct {
		Action           string `json:"action"`
		Message          string `json:"message,omitempty"`
		ExpectedRevision int    `json:"expected_revision"`
	}{strings.TrimSpace(action), strings.TrimSpace(message), expectedRevision})
	receiptKey := operation + ":" + strings.TrimSpace(idempotencyKey)
	if receipt, ok, err := planner.replayReceiptLocked(receiptKey, operation, fingerprint); err != nil {
		return nil, err
	} else if ok {
		return decodeExpansionReceiptResult[domain.RevisionSession](receipt)
	}
	active, err := planner.store.Revisions.Active()
	if err != nil || active == nil {
		return nil, fmt.Errorf("load active expansion revision: %w", err)
	}
	if active.Revision != expectedRevision {
		return nil, &storepkg.RevisionConflictError{Expected: expectedRevision, Actual: active.Revision}
	}
	preview := planner.previewForRevisionLocked(active.ID)
	var updated *domain.RevisionSession
	if active.Mode == domain.RevisionModeAdaptation {
		service := NewAdaptationRevisionService(planner.store)
		switch strings.TrimSpace(action) {
		case "retry", "resume":
			updated, err = service.Resume(active, idempotencyKey)
		case "cancel":
			updated, err = service.Cancel(active, idempotencyKey)
		case "feedback":
			updated, err = service.SubmitFeedback(active, active.Impact.Signature, message, idempotencyKey)
		case "request_audit":
			updated, err = planner.enqueueExpansionAuditLocked(active, preview)
		case "approve":
			updated, err = service.ApproveStage(active, idempotencyKey)
		case "outline":
			if preview == nil || preview.Recommendation.AdaptationCandidate == nil {
				return nil, ErrExpansionPreviewSealInvalidated
			}
			updated, err = service.SubmitDetailedOutlineCandidate(*preview.Recommendation.AdaptationCandidate, active, idempotencyKey)
		case "structure":
			if preview == nil || preview.Recommendation.AdaptationCandidate == nil {
				return nil, ErrExpansionPreviewSealInvalidated
			}
			sealed, sealErr := service.SealExpansionCandidate(AdaptationRevisionPreviewRequest{Intent: preview.Request.Sentence, Candidate: *preview.Recommendation.AdaptationCandidate, RequireAddedProse: true})
			if sealErr != nil {
				return nil, sealErr
			}
			updated, err = service.SubmitStructureCandidate(*sealed, active, idempotencyKey)
		case "prose":
			updated, err = service.SubmitProseReworkCandidate(active, idempotencyKey)
		case "publish", "recomplete":
			if preview == nil || preview.Recommendation.AdaptationCandidate == nil {
				return nil, ErrExpansionPreviewSealInvalidated
			}
			sealed, sealErr := service.SealExpansionCandidate(AdaptationRevisionPreviewRequest{Intent: preview.Request.Sentence, Candidate: *preview.Recommendation.AdaptationCandidate, RequireAddedProse: true})
			if sealErr != nil {
				return nil, sealErr
			}
			updated, err = service.Publish(*sealed, active, idempotencyKey)
		default:
			return nil, fmt.Errorf("unsupported expansion revision command %q", action)
		}
		if err != nil {
			if strings.TrimSpace(action) == "approve" {
				return nil, &domain.ManuscriptRevisionError{Class: "human_confirmation_required", Err: err}
			}
			return nil, err
		}
		if err := planner.recordReceiptResultLocked(receiptKey, operation, fingerprint, "", updated.ID, updated); err != nil {
			return nil, err
		}
		return updated, nil
	}
	policy := domain.NormalRevisionPolicy{}
	input := storepkg.RevisionMutationInput{SessionID: active.ID, ExpectedRevision: active.Revision, IdempotencyKey: idempotencyKey}
	switch strings.TrimSpace(action) {
	case "retry", "resume":
		updated, err = planner.store.Revisions.Resume(policy, input)
	case "cancel":
		updated, err = planner.store.Revisions.Cancel(policy, input)
	case "feedback":
		updated, err = NewNormalRevisionServiceWithKernel(planner.store, &planner.kernel).SubmitFeedback(active, active.Impact.Signature, message, idempotencyKey)
	case "request_audit":
		updated, err = planner.enqueueExpansionAuditLocked(active, preview)
	case "approve":
		updated, err = NewNormalRevisionServiceWithKernel(planner.store, &planner.kernel).ApproveStage(active, idempotencyKey)
	case "outline":
		if preview == nil {
			return nil, ErrExpansionPreviewSealInvalidated
		}
		updated, err = NewNormalRevisionServiceWithKernel(planner.store, &planner.kernel).SubmitDetailedOutlineCandidate(preview.Candidate, active, idempotencyKey)
	case "structure":
		if preview == nil {
			return nil, ErrExpansionPreviewSealInvalidated
		}
		updated, err = NewNormalRevisionServiceWithKernel(planner.store, &planner.kernel).SubmitSealedExpansionCandidate(*preview, active, idempotencyKey)
	case "prose":
		updated, err = NewNormalRevisionServiceWithKernel(planner.store, &planner.kernel).SubmitProseReworkCandidate(active, idempotencyKey)
	case "publish", "recomplete":
		if preview == nil {
			return nil, ErrExpansionPreviewSealInvalidated
		}
		updated, err = NewNormalRevisionServiceWithKernel(planner.store, &planner.kernel).PublishSealedExpansion(*preview, active, idempotencyKey)
	default:
		return nil, fmt.Errorf("unsupported expansion revision command %q", action)
	}
	if err != nil {
		if strings.TrimSpace(action) == "approve" {
			return nil, &domain.ManuscriptRevisionError{Class: "human_confirmation_required", Err: err}
		}
		return nil, err
	}
	if err := planner.recordReceiptResultLocked(receiptKey, operation, fingerprint, "", updated.ID, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

func (planner *ExpansionPlanner) previewForRevisionLocked(revisionID string) *domain.ExpansionPreview {
	for _, preview := range planner.previews {
		if preview != nil && preview.ConfirmedRevisionID == revisionID && !preview.Obsolete && !preview.Cancelled {
			copy := cloneExpansionPreview(*preview)
			return &copy
		}
	}
	return nil
}

func (planner *ExpansionPlanner) enqueueExpansionAuditLocked(session *domain.RevisionSession, preview *domain.ExpansionPreview) (*domain.RevisionSession, error) {
	if session == nil || session.Stage != domain.RevisionStageCandidateAudit || preview == nil {
		return nil, fmt.Errorf("expansion audit requires a candidate_audit_pending revision and its sealed preview")
	}
	taskID := fmt.Sprintf("%s:%d", session.ID, session.Revision)
	if existing, ok := planner.pendingAudits[taskID]; ok && existing.CandidateSignature == session.CandidateSignature && existing.Status == "pending" {
		copy := *session
		return &copy, nil
	}
	task := ExpansionAuditTask{
		ID: taskID, RevisionID: session.ID, Revision: session.Revision, Stage: session.Stage,
		Scope: "revision_candidate", ScopeID: session.ID, CandidateSignature: session.CandidateSignature,
		StructureSignature: preview.CandidateSignature,
		Candidate:          domain.CloneStructureSnapshot(preview.Candidate),
		ExpectationSet:     append([]domain.RevisionAuditExpectation(nil), session.AuditExpectations...),
		DependencyReviews:  append([]domain.ExpansionDependencyReview(nil), preview.DependencyReviews...),
		Mode:               session.Mode,
		Impact:             session.Impact,
		DramaticAssessment: preview.Recommendation.Assessment,
		ExpansionForm:      preview.Recommendation.Form,
		PolicyVersion:      expansionAuditorPolicyVersion, Status: "pending", CreatedAt: planner.now().UTC(),
	}
	for _, versionID := range session.CandidateVersionIDs {
		version, err := planner.store.Revisions.LoadVersion(versionID)
		if err != nil {
			return nil, err
		}
		task.CandidateVersions = append(task.CandidateVersions, *version)
	}
	task.DramaticVersions = append(task.DramaticVersions, task.CandidateVersions...)
	seenDramaticVersion := make(map[string]struct{}, len(task.DramaticVersions))
	for _, version := range task.DramaticVersions {
		seenDramaticVersion[version.ID] = struct{}{}
	}
	for _, versionID := range session.AcceptedVersionIDs {
		if _, exists := seenDramaticVersion[versionID]; exists {
			continue
		}
		version, err := planner.store.Revisions.LoadVersion(versionID)
		if err != nil {
			return nil, err
		}
		task.DramaticVersions = append(task.DramaticVersions, *version)
		seenDramaticVersion[version.ID] = struct{}{}
	}
	task.DramaticContract = buildExpansionDramaticContract(task.Candidate, task.DramaticVersions, task.Impact, task.DramaticAssessment)
	if session.Mode == domain.RevisionModeAdaptation {
		if preview.Recommendation.AdaptationCandidate != nil {
			candidate := *preview.Recommendation.AdaptationCandidate
			task.AdaptationCandidate = &candidate
		}
		manifest, manifestErr := planner.store.Adaptation.LoadSourceManifest()
		if manifestErr != nil {
			return nil, manifestErr
		}
		task.AdaptationSourceSignature = domain.AdaptationSourceManifestContractSignature(*manifest)
		runtime, err := planner.store.Adaptation.LoadRevisionRuntime()
		if err != nil {
			return nil, err
		}
		// Persist one real signed leaf per chapter candidate. Batch checkpoints
		// can therefore prove coverage by loading child artifacts rather than by
		// trusting a list of chapter IDs embedded in a parent summary.
		chapterPayloads := make(map[string]json.RawMessage)
		for _, version := range task.CandidateVersions {
			if version.ArtifactKind != domain.StructureKindChapter {
				continue
			}
			var detail domain.AdaptationDetailedOutlineCandidate
			if json.Unmarshal(version.Payload, &detail) == nil && detail.ChapterID != "" {
				chapterPayloads[detail.ChapterID] = append(json.RawMessage(nil), version.Payload...)
			}
		}
		if preview.Recommendation.AdaptationCandidate != nil {
			for _, chapter := range preview.Recommendation.AdaptationCandidate.Chapters {
				if _, exists := chapterPayloads[chapter.ID]; exists {
					continue
				}
				volumeID := ""
				for _, volume := range preview.Recommendation.AdaptationCandidate.Volumes {
					if chapter.Chapter >= volume.TargetFrom && chapter.Chapter <= volume.TargetTo {
						volumeID = volume.ID
						break
					}
				}
				detail := domain.AdaptationDetailedOutlineCandidate{ChapterID: chapter.ID, CurrentNumber: chapter.Chapter, VolumeID: volumeID, ArcID: "adaptation-expansion", Outline: chapter}
				payload, _ := json.Marshal(detail)
				chapterPayloads[chapter.ID] = payload
			}
		}
		for _, batch := range runtime.BatchPlan.Batches {
			for _, chapterID := range batch.ChapterIDs {
				payload, ok := chapterPayloads[chapterID]
				if !ok {
					return nil, fmt.Errorf("adaptation checkpoint chapter %s has no durable candidate version", chapterID)
				}
				task.CheckpointScopes = append(task.CheckpointScopes, ExpansionAuditCheckpoint{Stage: "adaptation_chapter", ScopeID: chapterID, Signature: domain.JSONContentSignature(payload), Output: string(payload)})
			}
			payload, _ := json.Marshal(batch)
			task.CheckpointScopes = append(task.CheckpointScopes, ExpansionAuditCheckpoint{Stage: "adaptation_batch", ScopeID: batch.ID, Signature: domain.JSONContentSignature(payload), Output: string(payload), DependencyIDs: append([]string(nil), batch.ChapterIDs...)})
		}
		for _, volume := range runtime.BatchPlan.VolumeReviews {
			batches := make([]domain.BatchWork, 0)
			dependencies := make([]string, 0)
			for _, batch := range runtime.BatchPlan.Batches {
				if batch.VolumeID == volume.ScopeID {
					batches = append(batches, batch)
					dependencies = append(dependencies, batch.ID)
				}
			}
			payload, _ := json.Marshal(struct {
				Review  domain.BatchAggregateReview `json:"review"`
				Batches []domain.BatchWork          `json:"batches"`
			}{volume, batches})
			task.CheckpointScopes = append(task.CheckpointScopes, ExpansionAuditCheckpoint{Stage: "adaptation_volume", ScopeID: volume.ScopeID, Signature: domain.JSONContentSignature(payload), Output: string(payload), DependencyIDs: dependencies})
		}
		volumeIDs := make([]string, 0, len(runtime.BatchPlan.VolumeReviews))
		for _, volume := range runtime.BatchPlan.VolumeReviews {
			volumeIDs = append(volumeIDs, volume.ScopeID)
		}
		payload, _ := json.Marshal(struct {
			Plan              domain.BatchPlan         `json:"plan"`
			CandidateVersions []domain.ArtifactVersion `json:"candidate_versions"`
		}{runtime.BatchPlan, task.CandidateVersions})
		task.CheckpointScopes = append(task.CheckpointScopes, ExpansionAuditCheckpoint{Stage: "adaptation_whole_book", ScopeID: "whole-book", Signature: domain.JSONContentSignature(payload), Output: string(payload), DependencyIDs: volumeIDs})
	}
	if len(task.CheckpointScopes) > 0 {
		referenced := make(map[string]struct{})
		taskIDByScope := make(map[string]string, len(task.CheckpointScopes))
		for _, checkpoint := range task.CheckpointScopes {
			taskIDByScope[checkpoint.ScopeID] = taskID + ":checkpoint:" + checkpoint.ScopeID
		}
		for _, checkpoint := range task.CheckpointScopes {
			for _, dependencyID := range checkpoint.DependencyIDs {
				referenced[dependencyID] = struct{}{}
			}
		}
		for _, checkpoint := range task.CheckpointScopes {
			childTaskIDs := make([]string, 0, len(checkpoint.DependencyIDs))
			for _, dependencyID := range checkpoint.DependencyIDs {
				childID, ok := taskIDByScope[dependencyID]
				if !ok {
					return nil, fmt.Errorf("checkpoint %s references unknown child scope %s", checkpoint.ScopeID, dependencyID)
				}
				childTaskIDs = append(childTaskIDs, childID)
			}
			dependencyTask := ExpansionDependencyAuditTask{
				ID: taskIDByScope[checkpoint.ScopeID], RootAuditTaskID: taskID, Stage: checkpoint.Stage, ScopeID: checkpoint.ScopeID,
				InputSignature: checkpoint.Signature, Output: checkpoint.Output, DependencyIDs: append([]string(nil), checkpoint.DependencyIDs...),
				ChildTaskIDs:  childTaskIDs,
				PolicyVersion: expansionDependencyPolicyVersion, Status: "pending", CreatedAt: planner.now().UTC(),
			}
			dependencyTask.ContractSignature = expansionDependencyContractSignature(dependencyTask)
			planner.pendingDependencyAudits[dependencyTask.ID] = dependencyTask
			if _, isChild := referenced[checkpoint.ScopeID]; !isChild {
				task.CheckpointTaskIDs = append(task.CheckpointTaskIDs, dependencyTask.ID)
			}
		}
		// Top-level tasks never persist checkpoint outputs or embedded child summaries.
		// CheckpointTaskIDs permanently retain exact root identities; CheckpointArtifacts
		// only holds one-to-one signed root refs after recursive validation on every replay.
		task.CheckpointScopes = nil
	}
	planner.pendingAudits[taskID] = task
	if err := planner.persistLocked(); err != nil {
		delete(planner.pendingAudits, taskID)
		return nil, err
	}
	copy := *session
	return &copy, nil
}

func buildExpansionDramaticContract(candidate []domain.VolumeOutline, versions []domain.ArtifactVersion, impact domain.RevisionImpact, assessment domain.ExpansionDramaticAssessment) ExpansionDramaticContract {
	chapters := domain.FlattenOutline(domain.ProjectLayeredOutlineOrder(candidate))
	contract := ExpansionDramaticContract{
		Claims:          authoritativeExpansionClaimBindings(versions, assessment, impact),
		CharacterBefore: assessment.CharacterBeforeStage, CharacterAfter: assessment.CharacterAfterStage,
		ClimaxTrigger: assessment.IndependentClimax, IrreversibleExit: assessment.IrreversibleExit,
	}
	if len(chapters) > 0 {
		first, last := chapters[0].ID, chapters[len(chapters)-1].ID
		contract.GoalChapterID, contract.ConflictChapterID, contract.ChoiceChapterID = first, first, first
		contract.CostChapterID, contract.ResultChapterID = last, last
		contract.ClimaxChapterID, contract.ExitChapterID = last, last
	}
	for _, item := range impact.Items {
		switch item.Requirement {
		case domain.StructureImpactRequired:
			contract.RequiredDependencyIDs = append(contract.RequiredDependencyIDs, item.ArtifactID)
		case domain.StructureImpactRecommended:
			contract.RecommendedDependencyIDs = append(contract.RecommendedDependencyIDs, item.ArtifactID)
		}
	}
	slices.Sort(contract.RequiredDependencyIDs)
	slices.Sort(contract.RecommendedDependencyIDs)
	return contract
}

// AcceptAuditArtifact is the public-key-only ingestion boundary. The caller
// must obtain the artifact from an independently composed ExpansionAuditRunner.
func (planner *ExpansionPlanner) AcceptAuditArtifact(revisionID string, artifact ExpansionAuditArtifact) (*domain.RevisionSession, error) {
	if err := planner.requireWriteReady(); err != nil {
		return nil, err
	}
	if planner == nil {
		return nil, fmt.Errorf("expansion planner is unavailable")
	}
	planner.mu.Lock()
	defer planner.mu.Unlock()
	active, err := planner.store.Revisions.Active()
	if err != nil || active == nil || active.ID != strings.TrimSpace(revisionID) {
		return nil, fmt.Errorf("load active expansion audit revision: %w", err)
	}
	taskID := fmt.Sprintf("%s:%d", active.ID, active.Revision)
	task, ok := planner.pendingAudits[taskID]
	if !ok {
		for candidateID, candidate := range planner.pendingAudits {
			if candidate.RevisionID == active.ID && candidate.Status != "pending" && (!ok || candidate.Revision > task.Revision) {
				taskID, task, ok = candidateID, candidate, true
			}
		}
	}
	if ok && task.Status != "pending" {
		stored, artifactOK := planner.auditArtifacts[taskID]
		if artifactOK && stored.Signature == artifact.Signature {
			copy := *active
			return &copy, nil
		}
	}
	if !ok || task.Status != "pending" {
		return nil, ErrExpansionDependencyTaskNotFound
	}
	active, err = planner.store.Revisions.Active()
	if err != nil || active == nil || active.ID != task.RevisionID || active.Revision != task.Revision || active.Stage != task.Stage {
		return nil, errors.Join(ErrExpansionPreviewStale, err)
	}
	if err := planner.validateExpansionAuditArtifact(artifact, task, active); err != nil {
		return nil, err
	}
	now := planner.now().UTC()
	task.CompletedAt = &now
	task.Findings = append([]string(nil), artifact.Findings...)
	task.Status = artifact.Decision
	planner.pendingAudits[taskID] = task
	planner.auditArtifacts[taskID] = artifact
	if artifact.Decision != "pass" {
		if err := planner.persistLocked(); err != nil {
			return nil, err
		}
		copy := *active
		return &copy, nil
	}
	var updated *domain.RevisionSession
	if active.Mode == domain.RevisionModeAdaptation {
		service := NewAdaptationRevisionService(planner.store)
		if err := completeAdaptationAuditCheckpoints(service, artifact, planner.auditorPublicKey); err != nil {
			return nil, err
		}
		updated, err = service.RecordAuditSet(active, artifact.Evidence, "auditor:"+task.ID)
	} else {
		updated, err = NewNormalRevisionServiceWithKernel(planner.store, &planner.kernel).RecordAuditSet(active, artifact.Evidence, "auditor:"+task.ID)
	}
	if err != nil {
		return nil, err
	}
	if err := planner.persistLocked(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (planner *ExpansionPlanner) AuditTask(revisionID string) *ExpansionAuditTask {
	if planner == nil {
		return nil
	}
	planner.mu.Lock()
	defer planner.mu.Unlock()
	var newest *ExpansionAuditTask
	for _, task := range planner.pendingAudits {
		if task.RevisionID != revisionID {
			continue
		}
		copy := task
		if newest == nil || copy.Revision > newest.Revision {
			newest = &copy
		}
	}
	return newest
}

func (planner *ExpansionPlanner) Cancel(previewID string, expectedRevision int, idempotencyKey string) (*domain.ExpansionPreview, error) {
	if err := planner.requireWriteReady(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(idempotencyKey) == "" || expectedRevision <= 0 {
		return nil, fmt.Errorf("cancel expected revision and idempotency key are required")
	}
	planner.mu.Lock()
	defer planner.mu.Unlock()
	fingerprint := expansionFingerprint(struct {
		PreviewID        string `json:"preview_id"`
		ExpectedRevision int    `json:"expected_revision"`
	}{strings.TrimSpace(previewID), expectedRevision})
	receiptKey := "cancel:" + strings.TrimSpace(idempotencyKey)
	if receipt, ok, err := planner.replayReceiptLocked(receiptKey, "cancel", fingerprint); err != nil {
		return nil, err
	} else if ok {
		preview := planner.previews[receipt.PreviewID]
		if preview == nil {
			return nil, ErrExpansionPreviewSealInvalidated
		}
		copy := cloneExpansionPreview(*preview)
		return &copy, nil
	}
	preview := planner.previews[strings.TrimSpace(previewID)]
	if preview == nil {
		return nil, ErrExpansionPreviewNotFound
	}
	if preview.BaseRevision != expectedRevision {
		return nil, ErrExpansionPreviewStale
	}
	if preview.ConfirmedRevisionID != "" {
		return nil, fmt.Errorf("confirmed revision must be cancelled through the revision workflow")
	}
	preview.Cancelled = true
	if err := planner.recordReceiptLocked(receiptKey, "cancel", fingerprint, preview.ID, ""); err != nil {
		return nil, err
	}
	copy := cloneExpansionPreview(*preview)
	return &copy, nil
}

func (planner *ExpansionPlanner) Confirm(ctx context.Context, previewID string, expectedRevision int, idempotencyKey string) (*ExpansionConfirmation, error) {
	if err := planner.requireWriteReady(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(idempotencyKey) == "" || expectedRevision <= 0 {
		return nil, fmt.Errorf("expected revision and idempotency key are required")
	}
	planner.mu.Lock()
	defer planner.mu.Unlock()
	stored := planner.previews[strings.TrimSpace(previewID)]
	if stored == nil {
		return nil, ErrExpansionPreviewNotFound
	}
	preview := cloneExpansionPreview(*stored)
	if expectedRevision != preview.BaseRevision {
		return nil, ErrExpansionPreviewStale
	}
	expectedSeal, err := planner.signPreview(preview)
	if err != nil || !hmac.Equal([]byte(preview.Signature), []byte(expectedSeal)) {
		return nil, ErrExpansionPreviewSealInvalidated
	}
	fingerprint := expansionFingerprint(struct {
		PreviewID              string `json:"preview_id"`
		PreviewSignature       string `json:"preview_signature"`
		BaseStructureSignature string `json:"base_structure_signature"`
		CandidateSignature     string `json:"candidate_signature"`
		ExpectedRevision       int    `json:"expected_revision"`
	}{preview.ID, preview.Signature, preview.BaseStructureSignature, preview.CandidateSignature, expectedRevision})
	receiptKey := "confirm:" + strings.TrimSpace(idempotencyKey)
	if receipt, ok, receiptErr := planner.replayReceiptLocked(receiptKey, "confirm", fingerprint); receiptErr != nil {
		return nil, receiptErr
	} else if ok {
		result, decodeErr := decodeExpansionReceiptResult[ExpansionConfirmation](receipt)
		if decodeErr != nil {
			return nil, decodeErr
		}
		result.Replay = true
		return result, nil
	}
	if preview.Cancelled {
		return nil, ErrExpansionPreviewCancelled
	}
	if preview.Obsolete || planner.now().After(preview.ExpiresAt) {
		if preview.Obsolete {
			return nil, ErrExpansionPreviewStale
		}
		return nil, ErrExpansionPreviewExpired
	}
	current, err := planner.store.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	if domain.StructureSignature(current) != preview.BaseStructureSignature || domain.StructureRevision(current) != preview.BaseRevision {
		return nil, ErrExpansionPreviewStale
	}
	if stored.ConfirmedRevisionID != "" {
		active, activeErr := planner.store.Revisions.Active()
		if activeErr != nil {
			return nil, activeErr
		}
		if active == nil || active.ID != stored.ConfirmedRevisionID || active.Stage.Terminal() {
			return nil, ErrExpansionPreviewStale
		}
		for _, receipt := range planner.receipts {
			if receipt.Operation != "confirm" || receipt.PreviewID != stored.ID || receipt.RevisionID != stored.ConfirmedRevisionID {
				continue
			}
			result, decodeErr := decodeExpansionReceiptResult[ExpansionConfirmation](receipt)
			if decodeErr != nil {
				continue
			}
			result.Replay = true
			if err := planner.recordReceiptResultLocked(receiptKey, "confirm", fingerprint, stored.ID, stored.ConfirmedRevisionID, result); err != nil {
				return nil, err
			}
			return result, nil
		}
		return nil, ErrExpansionPreviewSealInvalidated
	}
	var revision *domain.RevisionSession
	// Recover the crash window where the durable PR-01 start committed but the
	// in-memory preview receipt was not updated. Exact aggregate signature
	// equality is required; an unrelated active revision remains a conflict.
	active, activeErr := planner.store.Revisions.Active()
	if activeErr != nil {
		return nil, activeErr
	}
	if active != nil {
		expectedPreviewSignature := preview.Signature
		if preview.Mode == domain.RevisionModeAdaptation {
			expectedPreviewSignature = preview.RevisionPreviewSignature
		}
		if active.PreviewSignature != expectedPreviewSignature {
			return nil, storepkg.ErrActiveRevisionExists
		}
		recovered, recoverErr := planner.completeExpansionConfirmation(ctx, preview, active, idempotencyKey)
		if recoverErr != nil {
			return nil, recoverErr
		}
		stored.ConfirmedRevisionID = recovered.ID
		result := &ExpansionConfirmation{PreviewID: preview.ID, Revision: recovered, Replay: true}
		if err := planner.recordReceiptResultLocked(receiptKey, "confirm", fingerprint, stored.ID, recovered.ID, result); err != nil {
			return nil, err
		}
		return result, nil
	}
	revision, err = planner.completeExpansionConfirmation(ctx, preview, nil, idempotencyKey)
	if err != nil {
		return nil, err
	}
	stored.ConfirmedRevisionID = revision.ID
	result := &ExpansionConfirmation{PreviewID: preview.ID, Revision: revision}
	if err := planner.recordReceiptResultLocked(receiptKey, "confirm", fingerprint, stored.ID, revision.ID, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (planner *ExpansionPlanner) requireWriteReady() error {
	if planner == nil || planner.store == nil {
		return fmt.Errorf("publication_recovery_required: expansion store is unavailable")
	}
	return planner.store.RequireManuscriptWriteReady()
}

// completeExpansionConfirmation is deliberately resumable at each durable
// PR-01 boundary. Calling it after a crash inspects the actual session stage
// and executes only the missing impact/candidate command.
func (planner *ExpansionPlanner) completeExpansionConfirmation(_ context.Context, preview domain.ExpansionPreview, active *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	var revision = active
	var err error
	if preview.Mode == domain.RevisionModeAdaptation {
		candidate := preview.Recommendation.AdaptationCandidate
		if candidate == nil {
			return nil, fmt.Errorf("adaptation expansion candidate is missing")
		}
		service := NewAdaptationRevisionService(planner.store)
		sealed, sealErr := service.SealExpansionCandidate(AdaptationRevisionPreviewRequest{Intent: preview.Request.Sentence, Candidate: *candidate, RequireAddedProse: true})
		if sealErr != nil || sealed.Signature != preview.RevisionPreviewSignature {
			return nil, errors.Join(ErrExpansionPreviewStale, sealErr)
		}
		if revision == nil {
			result, previewErr := service.Preview(AdaptationRevisionPreviewRequest{Intent: preview.Request.Sentence, Candidate: *candidate, RequireAddedProse: true}, idempotencyKey)
			if previewErr != nil {
				return nil, previewErr
			}
			revision = result.Session
		}
		if revision.Stage == domain.RevisionStageImpactReviewPending {
			revision, err = service.ApproveImpact(revision.ID, revision.Revision, idempotencyKey+":impact")
			if err != nil {
				return nil, err
			}
		}
		if revision.Stage == domain.RevisionStageCandidateGenerating && len(revision.CandidateVersionIDs) == 0 {
			revision, err = service.SubmitStructureCandidate(*sealed, revision, idempotencyKey+":structure")
			if err != nil {
				return nil, err
			}
		}
	} else {
		impact, impactErr := normalExpansionImpact(preview)
		if impactErr != nil {
			return nil, impactErr
		}
		service := NewNormalRevisionServiceWithKernel(planner.store, &planner.kernel)
		if revision == nil {
			revision, err = service.PreviewSealedExpansion(preview, impact, idempotencyKey)
			if err != nil {
				return nil, err
			}
		}
		// The user confirmation covers the signed recommendation and its visible
		// impact list, so advance the ordinary impact-review gate before placing
		// the sealed structure candidate in the candidate stage.
		if revision.Stage == domain.RevisionStageImpactReviewPending {
			revision, err = service.ApproveImpact(revision.ID, revision.Revision, idempotencyKey+":impact")
			if err != nil {
				return nil, err
			}
		}
		if revision.Stage == domain.RevisionStageCandidateGenerating && len(revision.CandidateVersionIDs) == 0 {
			revision, err = service.SubmitSealedExpansionCandidate(preview, revision, idempotencyKey+":structure")
			if err != nil {
				return nil, err
			}
		}
	}
	return revision, nil
}

func (planner *ExpansionPlanner) executeBundle(ctx context.Context, snapshot ExpansionContext, request domain.ExpansionRequest, recommendation domain.ExpansionRecommendation) ([]domain.VolumeOutline, []domain.StructureRevisionPreview, []domain.StructureImpactItem, error) {
	current := domain.CloneStructureSnapshot(snapshot.Structure)
	sealed := make([]domain.StructureRevisionPreview, 0, len(recommendation.OrderedOperations))
	impactByID := make(map[string]domain.StructureImpactItem)
	for index, operation := range recommendation.OrderedOperations {
		structureRequest := domain.StructureRevisionRequest{Operation: operation.Operation, Intent: strings.TrimSpace(operation.Intent), Stage: snapshot.Stage, TargetID: operation.TargetID, DestinationID: operation.DestinationID, BaseRevision: request.ExpectedStructureRevision + index, Current: current, CompletedChapterIDs: snapshot.CompletedChapterIDs}
		preview, err := planner.kernel.Plan(ctx, fixedExpansionStructurePlanner{proposal: operation.Proposal}, structureRequest)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("expansion bundle step %d: %w", index+1, err)
		}
		next, err := planner.kernel.Confirm(*preview, structureRequest.BaseRevision, current)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("confirm expansion bundle step %d: %w", index+1, err)
		}
		for _, impact := range preview.Proposal.Impacts {
			impactByID[impact.ArtifactID] = impact
		}
		sealed = append(sealed, *preview)
		current = next
	}
	impacts := make([]domain.StructureImpactItem, 0, len(impactByID))
	for _, impact := range impactByID {
		impacts = append(impacts, impact)
	}
	slices.SortFunc(impacts, func(left, right domain.StructureImpactItem) int {
		return strings.Compare(left.ArtifactID, right.ArtifactID)
	})
	return current, sealed, impacts, nil
}

type fixedExpansionStructurePlanner struct {
	proposal domain.StructureRevisionProposal
}

func (planner fixedExpansionStructurePlanner) PlanStructure(context.Context, domain.StructureRevisionRequest) (domain.StructureRevisionProposal, error) {
	return cloneExpansionProposal(planner.proposal), nil
}

func validateExpansionForm(recommendation domain.ExpansionRecommendation) error {
	operations := recommendation.OrderedOperations
	last := operations[len(operations)-1].Operation
	switch recommendation.Form {
	case domain.ExpansionFormExpandCurrent:
		if len(operations) != 1 || last != domain.StructureRevisionExpandChapter {
			return fmt.Errorf("expand-current recommendation must contain one expand_chapter operation")
		}
	case domain.ExpansionFormInsertOne:
		if len(operations) != 1 || !isExpansionChapterInsertion(operations[0].Operation) {
			return fmt.Errorf("insert-one recommendation must contain exactly one chapter insertion")
		}
	case domain.ExpansionFormInsertMany:
		if len(operations) <= 1 || !everyExpansionOperation(operations, isExpansionChapterInsertion) {
			return fmt.Errorf("insert-many recommendation must contain only multiple chapter insertions")
		}
	case domain.ExpansionFormNewArc:
		if len(operations) != 1 || last != domain.StructureRevisionAppendArc {
			return fmt.Errorf("new-arc recommendation must contain only append_arc")
		}
	case domain.ExpansionFormNewVolume, domain.ExpansionFormEpilogue:
		if len(operations) != 1 || last != domain.StructureRevisionAppendVolume {
			return fmt.Errorf("new-volume and epilogue recommendations must contain only append_volume")
		}
	}
	if recommendation.ChapterCount != recommendation.SoftBudgetDelta.EstimatedChapters ||
		recommendation.ChapterMinWords != recommendation.SoftBudgetDelta.ChapterMinWords ||
		recommendation.ChapterMaxWords != recommendation.SoftBudgetDelta.ChapterMaxWords ||
		recommendation.TotalMinWords != recommendation.SoftBudgetDelta.TotalMinWords ||
		recommendation.TotalMaxWords != recommendation.SoftBudgetDelta.TotalMaxWords {
		return fmt.Errorf("expansion chapter counts and word ranges must match the dynamic soft budget")
	}
	if recommendation.Form == domain.ExpansionFormExpandCurrent && recommendation.ChapterCount != 1 ||
		recommendation.Form == domain.ExpansionFormInsertOne && recommendation.ChapterCount != 1 ||
		recommendation.Form == domain.ExpansionFormInsertMany && recommendation.ChapterCount != len(operations) {
		return fmt.Errorf("expansion form, chapter count, and operation count are inconsistent")
	}
	return nil
}

func validateExpansionActualDelta(recommendation domain.ExpansionRecommendation, before, after []domain.VolumeOutline) error {
	beforeChapters := domain.FlattenOutline(domain.ProjectLayeredOutlineOrder(before))
	afterChapters := domain.FlattenOutline(domain.ProjectLayeredOutlineOrder(after))
	actualAdded := len(afterChapters) - len(beforeChapters)
	expectedAdded := recommendation.ChapterCount
	if recommendation.Form == domain.ExpansionFormExpandCurrent {
		expectedAdded = 0
	}
	if actualAdded != expectedAdded {
		return fmt.Errorf("expansion form %s declares %d chapters but proposal delta adds %d", recommendation.Form, recommendation.ChapterCount, actualAdded)
	}
	if recommendation.NewArc != (recommendation.Form == domain.ExpansionFormNewArc) {
		return fmt.Errorf("expansion new_arc flag does not match form %s", recommendation.Form)
	}
	wantsVolume := recommendation.Form == domain.ExpansionFormNewVolume || recommendation.Form == domain.ExpansionFormEpilogue
	if recommendation.NewVolume != wantsVolume {
		return fmt.Errorf("expansion new_volume flag does not match form %s", recommendation.Form)
	}
	if recommendation.TotalMinWords != recommendation.ChapterCount*recommendation.ChapterMinWords ||
		recommendation.TotalMaxWords != recommendation.ChapterCount*recommendation.ChapterMaxWords {
		return fmt.Errorf("expansion aggregate word range does not equal the real chapter delta")
	}
	beforeIDs := make(map[string]struct{}, len(beforeChapters))
	for _, chapter := range beforeChapters {
		beforeIDs[chapter.ID] = struct{}{}
	}
	for _, chapter := range afterChapters {
		delete(beforeIDs, chapter.ID)
	}
	if len(beforeIDs) != 0 {
		return fmt.Errorf("expansion proposal deletes or replaces existing stable chapter identities")
	}
	previous := before
	for index, operation := range recommendation.OrderedOperations {
		proposal := operation.Proposal.Candidate
		if len(proposal) == 0 {
			return fmt.Errorf("expansion operation %d has no executable proposal candidate", index+1)
		}
		delta := len(domain.FlattenOutline(proposal)) - len(domain.FlattenOutline(previous))
		switch operation.Operation {
		case domain.StructureRevisionInsertChapter, domain.StructureRevisionAppendChapter:
			if delta != 1 {
				return fmt.Errorf("expansion chapter operation %d must add exactly one chapter, got %d", index+1, delta)
			}
		case domain.StructureRevisionExpandChapter:
			if delta != 0 {
				return fmt.Errorf("expand_chapter operation must not change chapter count")
			}
		case domain.StructureRevisionAppendArc, domain.StructureRevisionAppendVolume:
			if delta <= 0 {
				return fmt.Errorf("expansion container operation %d must add real chapter slots", index+1)
			}
		}
		previous = proposal
	}
	return nil
}

func isExpansionChapterInsertion(operation domain.StructureRevisionOperation) bool {
	return operation == domain.StructureRevisionInsertChapter || operation == domain.StructureRevisionAppendChapter
}

func everyExpansionOperation(operations []domain.ExpansionOperation, allowed func(domain.StructureRevisionOperation) bool) bool {
	return !slices.ContainsFunc(operations, func(operation domain.ExpansionOperation) bool { return !allowed(operation.Operation) })
}

func rejectNormalExpansionModeFields(recommendation domain.ExpansionRecommendation) error {
	payload, err := json.Marshal(recommendation)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return err
	}
	var walk func(any) error
	walk = func(node any) error {
		switch typed := node.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ToLower(strings.TrimSpace(key))
				if strings.Contains(normalized, "source") || strings.Contains(normalized, "adaptation") || strings.Contains(normalized, "coverage") || strings.Contains(normalized, "event_ledger") {
					return fmt.Errorf("normal expansion recursively forbids mode field %q", key)
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}

func normalExpansionImpact(preview domain.ExpansionPreview) (domain.RevisionImpact, error) {
	items := make([]domain.RevisionImpactItem, 0, len(preview.Recommendation.Impacts))
	for _, item := range preview.Recommendation.Impacts {
		requiresProse := item.RequiresBodyRewrite || (item.ArtifactKind == domain.StructureKindChapter && item.Level == domain.StructureImpactRequired)
		items = append(items, domain.RevisionImpactItem{ArtifactID: item.ArtifactID, ArtifactKind: item.ArtifactKind, Change: item.Change, Requirement: item.Level, Cause: item.Cause, RequiresBodyRewrite: requiresProse, DependencyEvidence: append([]string(nil), item.DependencyEvidence...)})
	}
	impact, err := domain.NewRevisionImpact(preview.Request.Sentence, items)
	if err != nil {
		return domain.RevisionImpact{}, err
	}
	impact, err = withoutNormalDependencySourceIDs(impact)
	if err != nil {
		return domain.RevisionImpact{}, err
	}
	impact, err = withNormalAdjacentVolumeImpacts(impact, preview.Candidate)
	if err != nil {
		return domain.RevisionImpact{}, err
	}
	impact, err = withNormalBatchPlanImpact(impact)
	if err != nil {
		return domain.RevisionImpact{}, err
	}
	return withNormalPersistentArtifacts(impact)
}

func (planner *ExpansionPlanner) signPreview(preview domain.ExpansionPreview) (string, error) {
	preview.Signature = ""
	// Lifecycle flags and the confirmed revision pointer are journal state, not
	// recommendation content. Keeping them outside the immutable seal allows a
	// cancelled/obsolete/confirmed preview to retain a verifiable origin.
	preview.Obsolete = false
	preview.Cancelled = false
	preview.ConfirmedRevisionID = ""
	payload, err := json.Marshal(preview)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, planner.seal)
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func newExpansionPreviewID() string {
	value := make([]byte, 16)
	_, _ = rand.Read(value)
	return "exp_" + hex.EncodeToString(value)
}

func cloneExpansionProposal(proposal domain.StructureRevisionProposal) domain.StructureRevisionProposal {
	payload, _ := json.Marshal(proposal)
	var result domain.StructureRevisionProposal
	_ = json.Unmarshal(payload, &result)
	return result
}

func cloneExpansionPreview(preview domain.ExpansionPreview) domain.ExpansionPreview {
	result := preview
	result.Request.ReferenceIDs = append([]string(nil), preview.Request.ReferenceIDs...)
	result.Candidate = domain.CloneStructureSnapshot(preview.Candidate)
	result.KernelPreviews = append([]domain.StructureRevisionPreview(nil), preview.KernelPreviews...)
	return result
}
