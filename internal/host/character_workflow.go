package host

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

type CharacterConfirmationRequest struct {
	ExpectedCandidateRevision int64
	CandidateDigest           string
	IdempotencyKey            string
}

type CharacterConfirmationResult struct {
	CandidateRevision  int64
	FoundationRevision int64
	CoreCastRevision   int64
	CandidateDigest    string
	Idempotent         bool
}

type CharacterCandidateEditRequest struct {
	ExpectedCandidateRevision int64
	Characters                []domain.Character
	Relationships             []domain.CharacterRelationship
	RelationshipsReviewed     bool
}

// CharacterConfirmationRequired reports the explicit user gate after the
// current Character candidate has passed deterministic and independent review
// but before it has been published as the canonical StoryFoundation/CoreCast.
func CharacterConfirmationRequired(st *storepkg.Store) (bool, error) {
	if err := tools.RepairCharacterWorkflowForResume(st); err != nil {
		return false, fmt.Errorf("repair Character workflow for resume: %w", err)
	}
	candidate, lifecycle, binding, err := tools.CurrentCharacterWorkflow(st)
	if err != nil {
		return false, err
	}
	if candidate == nil || lifecycle == nil ||
		lifecycle.ConfirmationStatus == domain.CharacterCardConfirmed {
		return false, nil
	}
	return currentCharacterReviewPassed(*lifecycle, binding), nil
}

// EditOriginalCharacterCandidate changes only staged Character-owned fields
// and deterministically invalidates the previous independent review.
func EditOriginalCharacterCandidate(
	st *storepkg.Store,
	request CharacterCandidateEditRequest,
) (domain.CharacterCardCandidate, domain.CharacterCardLifecycle, error) {
	if st == nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, fmt.Errorf("character edit store is nil")
	}
	candidate, lifecycle, _, err := tools.CurrentCharacterWorkflow(st)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, err
	}
	if candidate == nil || lifecycle == nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, fmt.Errorf("character candidate or lifecycle is missing")
	}
	if candidate.Revision != request.ExpectedCandidateRevision {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, fmt.Errorf(
			"character candidate revision conflict: expected %d, actual %d",
			request.ExpectedCandidateRevision,
			candidate.Revision,
		)
	}
	editedFoundation := domain.CloneStoryFoundation(candidate.Foundation)
	editedFoundation.Characters = append([]domain.Character(nil), request.Characters...)
	editedFoundation.Relationships = append([]domain.CharacterRelationship(nil), request.Relationships...)
	editedFoundation.RelationshipsReviewed = request.RelationshipsReviewed
	projected, findings, err := domain.ProjectCharacterCandidateCoreCast(editedFoundation, currentCoreCast(st))
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, err
	}
	completeness, err := domain.EvaluateCharacterCardCompleteness(editedFoundation, &projected)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, err
	}
	candidate.Foundation = editedFoundation
	candidate.ProjectedCast = projected
	savedCandidate, err := st.CharacterCards.SaveCandidateCAS(*candidate, candidate.Revision)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, fmt.Errorf("save edited character candidate: %w", err)
	}
	editedBinding, err := domain.CharacterCardBindingFromFoundation(savedCandidate.Foundation, lifecycle.Inputs)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, err
	}
	editedLifecycle := *lifecycle
	editedLifecycle.Candidate = editedBinding.Candidate
	editedLifecycle.Inputs = editedBinding.Inputs
	editedLifecycle.InputDigest = editedBinding.InputDigest
	editedLifecycle.Completeness = completeness
	editedLifecycle.AnalysisStatus = domain.CharacterCardAnalysisCandidateReady
	if lifecycle.ReviewStatus == domain.CharacterCardReviewNotReviewed {
		editedLifecycle.ReviewStatus = domain.CharacterCardReviewNotReviewed
		editedLifecycle.ReviewedCandidate = domain.CharacterCardCandidateReference{}
		editedLifecycle.ReviewedInputDigest = ""
		editedLifecycle.ReviewSummary = ""
	} else {
		editedLifecycle.ReviewStatus = domain.CharacterCardReviewStale
	}
	editedLifecycle.ConfirmationStatus = domain.CharacterCardUnconfirmed
	editedLifecycle.Findings = findings
	editedLifecycle.Error = nil
	savedLifecycle, err := st.CharacterCards.SaveCAS(editedLifecycle, lifecycle.Revision, editedBinding)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, fmt.Errorf("invalidate edited character review: %w", err)
	}
	return savedCandidate, savedLifecycle, nil
}

// ConfirmOriginalCharacterCandidate is the explicit user boundary that
// confirms the reviewed full candidate, its deterministic CoreCast projection,
// and the canonical StoryFoundation publication as one recoverable operation.
func ConfirmOriginalCharacterCandidate(
	st *storepkg.Store,
	request CharacterConfirmationRequest,
) (CharacterConfirmationResult, error) {
	if st == nil {
		return CharacterConfirmationResult{}, fmt.Errorf("character confirmation store is nil")
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return CharacterConfirmationResult{}, fmt.Errorf("character confirmation idempotency_key is required")
	}
	candidate, lifecycle, binding, err := tools.CurrentCharacterWorkflow(st)
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	if candidate == nil || lifecycle == nil {
		return CharacterConfirmationResult{}, fmt.Errorf("character candidate or lifecycle is missing")
	}
	if lifecycle.ConfirmationStatus == domain.CharacterCardConfirmed &&
		lifecycle.Candidate == binding.Candidate &&
		lifecycle.IdempotencyKey == strings.TrimSpace(request.IdempotencyKey) &&
		strings.TrimSpace(request.CandidateDigest) == binding.Candidate.CharacterContentDigest {
		repairedCandidate := *candidate
		repairedLifecycle := *lifecycle
		coreCast, loadErr := st.CoreCast.Load()
		if loadErr != nil {
			return CharacterConfirmationResult{}, loadErr
		}
		if lifecycle.Mode == domain.CharacterCardProjectAdaptation {
			var repairErr error
			var repaired domain.CoreCastContract
			repairedCandidate, repairedLifecycle, repaired, repairErr =
				repairConfirmedCharacterCoreCastProjection(st, *candidate, *lifecycle)
			if repairErr != nil {
				return CharacterConfirmationResult{}, repairErr
			}
			coreCast = &repaired
		}
		if coreCast == nil {
			return CharacterConfirmationResult{}, fmt.Errorf("confirmed character CoreCast is missing")
		}
		return CharacterConfirmationResult{
			CandidateRevision:  repairedCandidate.Revision,
			FoundationRevision: repairedLifecycle.Candidate.FoundationRevision,
			CoreCastRevision:   coreCast.Revision,
			CandidateDigest:    repairedLifecycle.Candidate.CharacterContentDigest,
			Idempotent:         true,
		}, nil
	}
	if candidate.Revision != request.ExpectedCandidateRevision {
		return CharacterConfirmationResult{}, fmt.Errorf(
			"character candidate revision conflict: expected %d, actual %d",
			request.ExpectedCandidateRevision,
			candidate.Revision,
		)
	}
	if strings.TrimSpace(request.CandidateDigest) != binding.Candidate.CharacterContentDigest {
		return CharacterConfirmationResult{}, fmt.Errorf("character candidate digest is stale")
	}
	if lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady ||
		lifecycle.ReviewStatus != domain.CharacterCardReviewPassed ||
		lifecycle.ReviewedCandidate != binding.Candidate ||
		lifecycle.ReviewedInputDigest != binding.InputDigest {
		return CharacterConfirmationResult{}, fmt.Errorf("character candidate requires a current passing independent review")
	}
	for _, completeness := range lifecycle.Completeness {
		if completeness.Status != domain.CharacterCardComplete {
			return CharacterConfirmationResult{}, fmt.Errorf("character candidate deterministic completeness is not passing")
		}
	}
	if lifecycle.Mode == domain.CharacterCardProjectAdaptation &&
		(lifecycle.Coverage == nil || lifecycle.Coverage.BlockingGaps > 0) {
		return CharacterConfirmationResult{}, fmt.Errorf("adaptation character candidate source coverage is incomplete")
	}
	projected, conflicts, err := domain.ProjectCharacterCandidateCoreCast(candidate.Foundation, currentCoreCast(st))
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	if err := bindConfirmedCharacterCoreCast(st, &projected, lifecycle.Mode); err != nil {
		return CharacterConfirmationResult{}, err
	}
	var sourceCharacters []domain.SourceMajorCharacter
	if lifecycle.Mode == domain.CharacterCardProjectAdaptation {
		sourceCharacters, err = adaptationCoreSourceCharacters(st, lifecycle.SourceMappings, projected)
		if err != nil {
			return CharacterConfirmationResult{}, err
		}
		projected, err = domain.ApplyCharacterSourceMappingsToCoreCast(
			projected,
			lifecycle.SourceMappings,
			sourceMajorCharacterIDs(sourceCharacters),
		)
		if err != nil {
			return CharacterConfirmationResult{}, fmt.Errorf("project Character source mappings into CoreCast: %w", err)
		}
	}
	for _, finding := range conflicts {
		if finding.Blocking || finding.Severity == domain.CharacterCardSeverityBlocking {
			return CharacterConfirmationResult{}, fmt.Errorf("CoreCast conflict blocks character confirmation: %s", finding.Description)
		}
	}
	completion := domain.CoreCastCompletion(projected, sourceCharacters, sourceCharacters)
	if !completion.Complete {
		return CharacterConfirmationResult{}, fmt.Errorf("projected CoreCast is incomplete: %s", strings.Join(completion.BlockingReasons, "; "))
	}
	canonical, canonicalBinding, _, _, err := tools.CurrentCharacterCanonicalBinding(st)
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	canonicalDigest, digestErr := domain.CharacterCardContentDigest(canonical)
	if digestErr != nil {
		return CharacterConfirmationResult{}, digestErr
	}
	candidateDigest, digestErr := domain.CharacterCardContentDigest(candidate.Foundation)
	if digestErr != nil {
		return CharacterConfirmationResult{}, digestErr
	}
	if candidate.Base.Candidate != canonicalBinding.Candidate && canonicalDigest != candidateDigest {
		return CharacterConfirmationResult{}, fmt.Errorf("canonical Foundation changed before character publication")
	}
	currentCast, err := st.CoreCast.Load()
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	expectedCoreRevision := int64(0)
	if currentCast != nil {
		expectedCoreRevision = currentCast.Revision
	}
	savedCast, err := st.CoreCast.SaveCAS(projected, expectedCoreRevision)
	if err != nil {
		return CharacterConfirmationResult{}, fmt.Errorf("save projected CoreCast: %w", err)
	}
	confirmedCast, _, err := st.CoreCast.ConfirmCAS(
		savedCast.Revision,
		savedCast.ContentSignature,
		sourceCharacters,
		sourceCharacters,
		nil,
	)
	if err != nil {
		return CharacterConfirmationResult{}, fmt.Errorf("confirm projected CoreCast: %w", err)
	}

	var published domain.StoryFoundation
	if lifecycle.Mode == domain.CharacterCardProjectAdaptation {
		published, err = st.Foundation.SaveCAS(candidate.Foundation, canonical.Revision)
		if err != nil {
			return CharacterConfirmationResult{}, fmt.Errorf("publish adaptation character candidate: %w", err)
		}
	} else {
		review, reviewErr := st.RunMeta.PlanningReview()
		if reviewErr != nil {
			return CharacterConfirmationResult{}, reviewErr
		}
		if review == nil {
			return CharacterConfirmationResult{}, fmt.Errorf("Foundation generation is missing")
		}
		candidate.Foundation = originalCandidateWithConfirmedBrief(candidate.Foundation, review.Brief)
		published, _, err = st.PublishOriginalCharacterCandidate(
			storepkg.FoundationGenerationFence{
				Generation:   review.FoundationGeneration,
				BaseRevision: review.FoundationBaseRevision,
			},
			candidate.Foundation,
			candidate.Base.Candidate.FoundationRevision,
		)
		if err != nil {
			return CharacterConfirmationResult{}, fmt.Errorf("publish character candidate: %w", err)
		}
	}
	published, err = st.Foundation.Load()
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	_, rebound, inputs, _, err := tools.CurrentCharacterCanonicalBinding(st)
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	rebound, err = domain.CharacterCardBindingFromFoundation(published, inputs)
	if err != nil {
		return CharacterConfirmationResult{}, err
	}
	candidate.Base = rebound
	candidate.Foundation = published
	candidate.ProjectedCast = confirmedCast
	savedCandidate, err := st.CharacterCards.SaveCandidateCAS(*candidate, candidate.Revision)
	if err != nil {
		return CharacterConfirmationResult{}, fmt.Errorf("rebind published character candidate: %w", err)
	}
	confirmedLifecycle := *lifecycle
	confirmedLifecycle.Candidate = rebound.Candidate
	confirmedLifecycle.Inputs = rebound.Inputs
	confirmedLifecycle.InputDigest = rebound.InputDigest
	confirmedLifecycle.ReviewedCandidate = rebound.Candidate
	confirmedLifecycle.ReviewedInputDigest = rebound.InputDigest
	confirmedLifecycle.ConfirmationStatus = domain.CharacterCardConfirmed
	confirmedLifecycle.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	savedLifecycle, err := st.CharacterCards.SaveCAS(confirmedLifecycle, lifecycle.Revision, rebound)
	if err != nil {
		return CharacterConfirmationResult{}, fmt.Errorf("confirm character lifecycle: %w", err)
	}
	return CharacterConfirmationResult{
		CandidateRevision:  savedCandidate.Revision,
		FoundationRevision: rebound.Candidate.FoundationRevision,
		CoreCastRevision:   confirmedCast.Revision,
		CandidateDigest:    savedLifecycle.Candidate.CharacterContentDigest,
	}, nil
}

func repairConfirmedCharacterCoreCastProjection(
	st *storepkg.Store,
	candidate domain.CharacterCardCandidate,
	lifecycle domain.CharacterCardLifecycle,
) (domain.CharacterCardCandidate, domain.CharacterCardLifecycle, domain.CoreCastContract, error) {
	current, err := st.CoreCast.Load()
	if err != nil {
		return candidate, lifecycle, domain.CoreCastContract{}, err
	}
	if current == nil {
		return candidate, lifecycle, domain.CoreCastContract{}, fmt.Errorf("confirmed character CoreCast is missing")
	}
	projected, conflicts, err := domain.ProjectCharacterCandidateCoreCast(candidate.Foundation, current)
	if err != nil {
		return candidate, lifecycle, domain.CoreCastContract{}, err
	}
	for _, finding := range conflicts {
		if finding.Blocking || finding.Severity == domain.CharacterCardSeverityBlocking {
			return candidate, lifecycle, domain.CoreCastContract{}, fmt.Errorf(
				"CoreCast conflict blocks projection repair: %s",
				finding.Description,
			)
		}
	}
	if err := bindConfirmedCharacterCoreCast(st, &projected, lifecycle.Mode); err != nil {
		return candidate, lifecycle, domain.CoreCastContract{}, err
	}
	var sourceCharacters []domain.SourceMajorCharacter
	if lifecycle.Mode == domain.CharacterCardProjectAdaptation {
		sourceCharacters, err = adaptationCoreSourceCharacters(st, lifecycle.SourceMappings, projected)
		if err != nil {
			return candidate, lifecycle, domain.CoreCastContract{}, err
		}
		projected, err = domain.ApplyCharacterSourceMappingsToCoreCast(
			projected,
			lifecycle.SourceMappings,
			sourceMajorCharacterIDs(sourceCharacters),
		)
		if err != nil {
			return candidate, lifecycle, domain.CoreCastContract{}, err
		}
	}
	if projected.ContentSignature == current.ContentSignature {
		return candidate, lifecycle, *current, nil
	}
	saved, err := st.CoreCast.SaveCAS(projected, current.Revision)
	if err != nil {
		return candidate, lifecycle, domain.CoreCastContract{}, err
	}
	_, _, err = st.CoreCast.ConfirmCAS(
		saved.Revision,
		saved.ContentSignature,
		sourceCharacters,
		sourceCharacters,
		nil,
	)
	if err != nil {
		return candidate, lifecycle, domain.CoreCastContract{}, err
	}
	published, err := st.CoreCast.PublishConfirmed(
		st.Foundation,
		sourceCharacters,
		sourceCharacters,
		nil,
	)
	if err != nil {
		return candidate, lifecycle, domain.CoreCastContract{}, err
	}
	_, rebound, _, _, err := tools.CurrentCharacterCanonicalBinding(st)
	if err != nil {
		return candidate, lifecycle, domain.CoreCastContract{}, err
	}
	candidate.Base = rebound
	candidate.Foundation.Revision = rebound.Candidate.FoundationRevision
	candidate.ProjectedCast = published
	candidate, err = st.CharacterCards.SaveCandidateCAS(candidate, candidate.Revision)
	if err != nil {
		return candidate, lifecycle, domain.CoreCastContract{}, err
	}
	lifecycle.Candidate = rebound.Candidate
	lifecycle.Inputs = rebound.Inputs
	lifecycle.InputDigest = rebound.InputDigest
	lifecycle.ReviewedCandidate = rebound.Candidate
	lifecycle.ReviewedInputDigest = rebound.InputDigest
	lifecycle, err = st.CharacterCards.SaveCAS(lifecycle, lifecycle.Revision, rebound)
	if err != nil {
		return candidate, lifecycle, domain.CoreCastContract{}, err
	}
	return candidate, lifecycle, published, nil
}

func adaptationCoreSourceCharacters(
	st *storepkg.Store,
	mappings []domain.CharacterSourceMapping,
	contract domain.CoreCastContract,
) ([]domain.SourceMajorCharacter, error) {
	source, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, fmt.Errorf("adaptation SourceFoundation is missing")
	}
	formal := domain.ResolveSourceCharacters(*source)
	formalByID := make(map[string]domain.SourceMajorCharacter, len(formal))
	for _, character := range formal {
		formalByID[character.ID] = character
	}
	coreIDs := make(map[string]struct{}, len(contract.Members))
	for _, member := range contract.Members {
		coreIDs[member.Character.ID] = struct{}{}
	}
	selected := make(map[string]struct{}, len(formal))
	for _, mapping := range mappings {
		coreRelevant := mapping.Action == domain.CharacterSourceExclude
		if !coreRelevant {
			for _, targetID := range mapping.TargetCharacterIDs {
				if _, core := coreIDs[targetID]; core {
					coreRelevant = true
					break
				}
			}
		}
		if !coreRelevant {
			continue
		}
		for _, sourceID := range mapping.SourceCharacterIDs {
			if _, exists := formalByID[sourceID]; exists {
				selected[sourceID] = struct{}{}
			}
		}
	}
	out := make([]domain.SourceMajorCharacter, 0, len(selected))
	for _, character := range formal {
		if _, exists := selected[character.ID]; exists {
			out = append(out, character)
		}
	}
	return out, nil
}

func sourceMajorCharacterIDs(values []domain.SourceMajorCharacter) []string {
	ids := make([]string, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.ID)
	}
	return ids
}

func originalCandidateWithConfirmedBrief(candidate domain.StoryFoundation, brief string) domain.StoryFoundation {
	if strings.TrimSpace(candidate.Premise) == "" {
		candidate.Premise = strings.TrimSpace(brief)
	}
	return candidate
}

func currentCoreCast(st *storepkg.Store) *domain.CoreCastContract {
	value, err := st.CoreCast.Load()
	if err != nil {
		return nil
	}
	return value
}

func bindConfirmedCharacterCoreCast(
	st *storepkg.Store,
	projected *domain.CoreCastContract,
	mode domain.CharacterCardProjectMode,
) error {
	gate, err := st.CoreCast.LoadGateBinding()
	if err != nil {
		return fmt.Errorf("load Character confirmation binding: %w", err)
	}
	if gate == nil {
		if mode == domain.CharacterCardProjectOriginal {
			return nil
		}
		if projected.Mode == domain.CoreCastModeAdaptation &&
			projected.DraftRevision > 0 &&
			strings.TrimSpace(projected.DraftHash) != "" &&
			len(strings.TrimSpace(projected.SourceSignature)) == 64 &&
			len(strings.TrimSpace(projected.AdaptationIntentHash)) == 64 {
			return nil
		}
		return fmt.Errorf("adaptation Character confirmation binding is missing")
	}
	expectedMode := domain.CoreCastModeNormal
	if mode == domain.CharacterCardProjectAdaptation {
		expectedMode = domain.CoreCastModeAdaptation
	}
	if gate.Mode != expectedMode {
		return fmt.Errorf("Character confirmation binding mode is stale")
	}
	projected.Mode = expectedMode
	projected.DraftRevision = gate.DraftRevision
	projected.DraftHash = gate.DraftHash
	if expectedMode == domain.CoreCastModeAdaptation {
		projected.SourceSignature = gate.SourceSignature
		projected.AdaptationIntentHash = gate.AdaptationIntentHash
	} else {
		projected.SourceSignature = ""
		projected.AdaptationIntentHash = ""
	}
	return nil
}
