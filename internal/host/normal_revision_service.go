package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// NormalRevisionService is the production boundary joining the shared
// structure kernel to the durable revision state machine. It has no source-
// novel dependency and refuses to operate on adaptation projects.
type NormalRevisionService struct {
	store                *storepkg.Store
	kernel               *StructurePlanningKernel
	beforeRevisionCommit func()
}

type NormalStructureRevisionPreview struct {
	Preview *domain.StructureRevisionPreview `json:"preview"`
	Session *domain.RevisionSession          `json:"session"`
}

func NewNormalRevisionService(st *storepkg.Store) *NormalRevisionService {
	return &NormalRevisionService{store: st, kernel: &StructurePlanningKernel{}}
}

// NewNormalRevisionServiceWithKernel joins an already sealed structure bundle
// to the normal revision state machine. Sharing the exact kernel scope is
// intentional: every step remains authenticated by the same private scope that
// produced it, so the aggregate boundary cannot weaken preview substitution.
func NewNormalRevisionServiceWithKernel(st *storepkg.Store, kernel *StructurePlanningKernel) *NormalRevisionService {
	if kernel == nil {
		kernel = &StructurePlanningKernel{}
	}
	return &NormalRevisionService{store: st, kernel: kernel}
}

// PreviewSealedExpansion validates every ordered kernel preview against the
// authoritative formal structure and starts exactly one normal revision bound
// to the aggregate expansion signature.
func (s *NormalRevisionService) PreviewSealedExpansion(preview domain.ExpansionPreview, impact domain.RevisionImpact, idempotencyKey string) (*domain.RevisionSession, error) {
	if s == nil || s.store == nil || s.kernel == nil {
		return nil, fmt.Errorf("normal revision store and kernel are required")
	}
	if s.store.Adaptation.Exists() || preview.Mode != domain.RevisionModeNormal {
		return nil, fmt.Errorf("normal sealed expansion requires a normal project")
	}
	current, err := s.store.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	if domain.StructureSignature(current) != preview.BaseStructureSignature {
		return nil, domain.ErrStructurePreviewStale
	}
	for index, sealed := range preview.KernelPreviews {
		current, err = s.kernel.Confirm(sealed, sealed.BaseRevision, current)
		if err != nil {
			return nil, fmt.Errorf("validate sealed expansion step %d: %w", index+1, err)
		}
	}
	if len(preview.KernelPreviews) == 0 || domain.StructureSignature(current) != preview.CandidateSignature ||
		domain.StructureSignature(current) != domain.StructureSignature(preview.Candidate) {
		return nil, fmt.Errorf("sealed expansion aggregate candidate mismatch")
	}
	return s.store.Revisions.Start(domain.NormalRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: preview.Request.Sentence, Impact: impact, PreviewSignature: preview.Signature, IdempotencyKey: idempotencyKey,
	})
}

// SubmitSealedExpansionCandidate revalidates the complete ordered bundle and
// persists its final candidate through the ordinary PR-01 candidate boundary.
func (s *NormalRevisionService) SubmitSealedExpansionCandidate(preview domain.ExpansionPreview, session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil || session.PreviewSignature != preview.Signature || normalServiceApprovalStage(*session) != domain.NormalApprovalStructure {
		return nil, fmt.Errorf("normal expansion preview substitution is not allowed")
	}
	current, err := s.store.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	for index, sealed := range preview.KernelPreviews {
		current, err = s.kernel.Confirm(sealed, sealed.BaseRevision, current)
		if err != nil {
			return nil, fmt.Errorf("confirm sealed expansion step %d: %w", index+1, err)
		}
	}
	if domain.StructureSignature(current) != preview.CandidateSignature {
		return nil, fmt.Errorf("normal expansion aggregate candidate signature mismatch")
	}
	proposal := preview.KernelPreviews[len(preview.KernelPreviews)-1].Proposal
	artifacts, err := normalStructureCandidateArtifacts(*session, proposal, current)
	if err != nil {
		return nil, err
	}
	return s.store.Revisions.SubmitCandidate(domain.NormalRevisionPolicy{}, storepkg.SubmitRevisionCandidateInput{
		SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey, Artifacts: artifacts,
	})
}

func (s *NormalRevisionService) CurrentManuscriptStage() (domain.ManuscriptStage, error) {
	if s == nil || s.store == nil {
		return "", fmt.Errorf("normal revision store is required")
	}
	return normalProjectManuscriptStage(s.store)
}

func (s *NormalRevisionService) Preview(
	ctx context.Context,
	planner StructureRevisionPlanner,
	request domain.StructureRevisionRequest,
	idempotencyKey string,
) (*NormalStructureRevisionPreview, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("normal revision store is required")
	}
	if s.store.Adaptation.Exists() {
		return nil, fmt.Errorf("normal revision service cannot revise an adaptation project")
	}
	currentStage, err := normalProjectManuscriptStage(s.store)
	if err != nil {
		return nil, err
	}
	if request.Stage != currentStage {
		return nil, fmt.Errorf("normal revision manuscript stage drift: request=%s current=%s; create a new preview", request.Stage, currentStage)
	}
	preview, err := s.kernel.Plan(ctx, normalSkeletonPlanner{planner: planner, current: request.Current}, request)
	if err != nil {
		return nil, err
	}
	impact, err := preview.Proposal.RevisionImpact(preview.Intent)
	if err != nil {
		return nil, err
	}
	impact, err = withoutNormalDependencySourceIDs(impact)
	if err != nil {
		return nil, err
	}
	impact, err = withNormalAdjacentVolumeImpacts(impact, preview.Proposal.Candidate)
	if err != nil {
		return nil, err
	}
	impact, err = withNormalBatchPlanImpact(impact)
	if err != nil {
		return nil, err
	}
	impact, err = withNormalPersistentArtifacts(impact)
	if err != nil {
		return nil, err
	}
	session, err := s.store.Revisions.Start(domain.NormalRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: preview.Intent, Impact: impact, PreviewSignature: preview.Signature, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return nil, err
	}
	return &NormalStructureRevisionPreview{Preview: preview, Session: session}, nil
}

func withoutNormalDependencySourceIDs(impact domain.RevisionImpact) (domain.RevisionImpact, error) {
	items := append([]domain.RevisionImpactItem(nil), impact.Items...)
	for index := range items {
		items[index].DependencySourceIDs = nil
	}
	return domain.NewRevisionImpact(impact.Summary, items)
}

type normalSkeletonPlanner struct {
	planner StructureRevisionPlanner
	current []domain.VolumeOutline
}

func (p normalSkeletonPlanner) PlanStructure(ctx context.Context, request domain.StructureRevisionRequest) (domain.StructureRevisionProposal, error) {
	proposal, err := p.planner.PlanStructure(ctx, request)
	if err != nil {
		return domain.StructureRevisionProposal{}, err
	}
	current := make(map[string]domain.OutlineEntry)
	currentArcs := make(map[string]struct{})
	for _, volume := range p.current {
		for _, arc := range volume.Arcs {
			currentArcs[arc.ID] = struct{}{}
		}
	}
	for _, chapter := range domain.FlattenOutline(p.current) {
		current[chapter.ID] = chapter
	}
	chapterNumber := 0
	for volumeIndex := range proposal.Candidate {
		for arcIndex := range proposal.Candidate[volumeIndex].Arcs {
			arc := &proposal.Candidate[volumeIndex].Arcs[arcIndex]
			if len(arc.Chapters) == 0 {
				if _, existing := currentArcs[arc.ID]; !existing && arc.EstimatedChapters > 0 {
					for slot := 1; slot <= arc.EstimatedChapters; slot++ {
						arc.Chapters = append(arc.Chapters, domain.OutlineEntry{
							ID: domain.LegacyStructureID("normal-revision-slot", domain.StructureKindChapter, fmt.Sprintf("%s/%d", arc.ID, slot)),
						})
					}
				}
			}
			for chapterIndex := range arc.Chapters {
				chapter := &arc.Chapters[chapterIndex]
				chapterNumber++
				chapter.Chapter = chapterNumber
				prior, exists := current[chapter.ID]
				if exists && normalOutlineContentEqual(prior, *chapter) {
					continue
				}
				*chapter = domain.OutlineEntry{ID: chapter.ID, Chapter: chapter.Chapter}
			}
			if len(arc.Chapters) == 0 {
				chapterNumber += arc.EstimatedChapters
			}
		}
	}
	return proposal, nil
}

func normalOutlineContentEqual(left, right domain.OutlineEntry) bool {
	left.ID, left.Chapter = "", 0
	right.ID, right.Chapter = "", 0
	leftPayload, _ := json.Marshal(left)
	rightPayload, _ := json.Marshal(right)
	return string(leftPayload) == string(rightPayload)
}

func withNormalPersistentArtifacts(impact domain.RevisionImpact) (domain.RevisionImpact, error) {
	items := append([]domain.RevisionImpactItem(nil), impact.Items...)
	items = append(items, domain.RevisionImpactItem{
		ArtifactID: domain.NormalStructureSnapshotID, ArtifactKind: domain.NormalArtifactStructureSnapshot,
		Change: "bind the accepted staged structure snapshot", Requirement: domain.StructureImpactRequired,
		Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"formal publish must derive from accepted candidate versions"},
	})
	queueAdded := false
	for _, item := range impact.Items {
		if item.ArtifactKind != domain.StructureKindChapter || item.Requirement != domain.StructureImpactRequired || !item.RequiresBodyRewrite {
			continue
		}
		items = append(items, domain.RevisionImpactItem{
			ArtifactID: "rework:" + item.ArtifactID, ArtifactKind: domain.NormalArtifactProseReworkIntent,
			Change: "queue exact stable-ID prose rework intent", Requirement: domain.StructureImpactRequired,
			Cause: item.Cause, DependencyEvidence: append([]string(nil), item.DependencyEvidence...),
		})
		queueAdded = true
	}
	if queueAdded {
		items = append(items, domain.RevisionImpactItem{
			ArtifactID: domain.NormalProseReworkQueueID, ArtifactKind: domain.NormalArtifactProseReworkQueue,
			Change: "persist minimal prose rework queue slots", Requirement: domain.StructureImpactRequired,
			Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"PR-06 will consume only approved stable-ID slots"},
		})
	}
	return domain.NewRevisionImpact(impact.Summary, items)
}

func withNormalAdjacentVolumeImpacts(impact domain.RevisionImpact, candidate []domain.VolumeOutline) (domain.RevisionImpact, error) {
	items := append([]domain.RevisionImpactItem(nil), impact.Items...)
	byID := make(map[string]struct{}, len(items))
	changed := make(map[string]struct{})
	for _, item := range items {
		byID[item.ArtifactID] = struct{}{}
		if item.Requirement == domain.StructureImpactRequired && item.ArtifactKind == domain.StructureKindVolume {
			changed[item.ArtifactID] = struct{}{}
		}
	}
	ordered := domain.ProjectLayeredOutlineOrder(domain.CloneStructureSnapshot(candidate))
	for index, volume := range ordered {
		if _, ok := changed[volume.ID]; !ok {
			continue
		}
		for _, adjacent := range []int{index - 1, index + 1} {
			if adjacent < 0 || adjacent >= len(ordered) {
				continue
			}
			neighbor := ordered[adjacent]
			if _, exists := byID[neighbor.ID]; exists {
				continue
			}
			byID[neighbor.ID] = struct{}{}
			items = append(items, domain.RevisionImpactItem{
				ArtifactID: neighbor.ID, ArtifactKind: domain.StructureKindVolume,
				Change: "re-audit adjacent volume handoff", Requirement: domain.StructureImpactRequired,
				Cause:              domain.StructureImpactContentDependency,
				DependencyEvidence: []string{"the neighboring volume handoff can change when the selected volume structure changes"},
			})
		}
	}
	for _, volume := range ordered {
		if _, required := byID[volume.ID]; !required {
			continue
		}
		for _, arc := range volume.Arcs {
			if _, exists := byID[arc.ID]; exists {
				continue
			}
			byID[arc.ID] = struct{}{}
			items = append(items, domain.RevisionImpactItem{
				ArtifactID: arc.ID, ArtifactKind: domain.StructureKindArc,
				Change: "re-audit arc inside changed or adjacent volume", Requirement: domain.StructureImpactRequired,
				Cause:              domain.StructureImpactContentDependency,
				DependencyEvidence: []string{"arc handoff belongs to an exactly audited changed or adjacent volume"},
			})
		}
	}
	return domain.NewRevisionImpact(impact.Summary, items)
}

func normalProjectManuscriptStage(st *storepkg.Store) (domain.ManuscriptStage, error) {
	progress, err := st.Progress.Load()
	if err != nil {
		return "", err
	}
	if progress != nil {
		switch progress.Phase {
		case domain.PhaseComplete:
			return domain.ManuscriptStageComplete, nil
		case domain.PhaseWriting:
			return domain.ManuscriptStageWriting, nil
		}
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		return "", err
	}
	if review != nil && review.Kind == domain.PlanningReviewKindChapterOutline {
		return domain.ManuscriptStageOutlineComplete, nil
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return "", err
	}
	if len(volumes) > 0 {
		allDetailed := true
		for _, volume := range volumes {
			for _, arc := range volume.Arcs {
				if len(arc.Chapters) == 0 {
					allDetailed = false
				}
			}
		}
		if allDetailed && (review == nil || review.Kind != domain.PlanningReviewKindVolumeSplit) {
			return domain.ManuscriptStageOutlineComplete, nil
		}
	}
	return domain.ManuscriptStageProposalComplete, nil
}

func withNormalBatchPlanImpact(impact domain.RevisionImpact) (domain.RevisionImpact, error) {
	for _, item := range impact.Items {
		if item.ArtifactID == "normal-batch-plan" {
			return impact, nil
		}
	}
	items := append([]domain.RevisionImpactItem(nil), impact.Items...)
	items = append(items, domain.RevisionImpactItem{
		ArtifactID: "normal-batch-plan", ArtifactKind: domain.NormalArtifactBatchPlan,
		Change: "bound generation and review scopes", Requirement: domain.StructureImpactRequired,
		Cause:              domain.StructureImpactContentDependency,
		DependencyEvidence: []string{"large or contextual revision work must use bounded batches and aggregate reviews"},
	})
	return domain.NewRevisionImpact(impact.Summary, items)
}

func (s *NormalRevisionService) ApproveImpact(sessionID string, revision int, idempotencyKey string) (*domain.RevisionSession, error) {
	return s.store.Revisions.ApproveImpact(domain.NormalRevisionPolicy{}, storepkg.RevisionMutationInput{
		SessionID: sessionID, ExpectedRevision: revision, IdempotencyKey: idempotencyKey,
	})
}

func (s *NormalRevisionService) SubmitFeedback(session *domain.RevisionSession, targetImpactSignature, message, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil {
		return nil, fmt.Errorf("normal revision session is required")
	}
	return s.store.Revisions.SubmitFeedback(domain.NormalRevisionPolicy{}, storepkg.RevisionFeedbackInput{
		RevisionMutationInput: storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey},
		StageID:               session.CurrentApprovalStageID(), ImpactSignature: targetImpactSignature, Message: message,
	})
}

func (s *NormalRevisionService) SubmitStructureCandidate(
	sealed domain.StructureRevisionPreview,
	sessionID string,
	revision int,
	idempotencyKey string,
) (*domain.RevisionSession, error) {
	active, err := s.store.Revisions.Active()
	if err != nil || active == nil {
		return nil, fmt.Errorf("load active normal revision: %w", err)
	}
	if active.ID != sessionID || normalServiceApprovalStage(*active) != domain.NormalApprovalStructure {
		return nil, fmt.Errorf("normal structure candidate is not the current staged command: active=%s requested=%s approval=%s", active.ID, sessionID, normalServiceApprovalStage(*active))
	}
	if active.PreviewSignature != sealed.Signature {
		return nil, fmt.Errorf("normal structure preview substitution is not allowed")
	}
	current, err := s.store.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	candidate, err := s.kernel.Confirm(sealed, sealed.BaseRevision, current)
	if err != nil {
		return nil, err
	}
	artifacts, err := normalStructureCandidateArtifacts(*active, sealed.Proposal, candidate)
	if err != nil {
		return nil, err
	}
	return s.store.Revisions.SubmitCandidate(domain.NormalRevisionPolicy{}, storepkg.SubmitRevisionCandidateInput{
		SessionID: sessionID, ExpectedRevision: revision, IdempotencyKey: idempotencyKey, Artifacts: artifacts,
	})
}

// SubmitDetailedOutlineCandidate is deliberately separate from structure
// previewing: detailed outlines cannot be persisted until structure audits and
// the structure human approval have both completed.
func (s *NormalRevisionService) SubmitDetailedOutlineCandidate(candidate []domain.VolumeOutline, session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil || normalServiceApprovalStage(*session) != domain.NormalApprovalOutline || len(session.Approvals) == 0 && normalImpactHasStructure(session.Impact) {
		return nil, fmt.Errorf("detailed outlines require the prior structure approval")
	}
	if err := domain.ValidateStructureSnapshot(candidate); err != nil {
		return nil, err
	}
	accepted, err := s.acceptedNormalSnapshot(session)
	if err != nil {
		return nil, err
	}
	formal, err := s.store.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	baseline := formal
	if accepted != nil {
		baseline = *accepted
	}
	if normalSkeletonSignature(baseline) != normalSkeletonSignature(candidate) ||
		domain.StructureTopologySignature(baseline) != domain.StructureTopologySignature(candidate) {
		return nil, fmt.Errorf("detailed outline candidate does not match the accepted structure skeleton")
	}
	if err := validateNormalDetailedOutlineChanges(candidate, baseline, formal, session.Impact); err != nil {
		return nil, err
	}
	artifacts, err := normalStructureCandidateArtifacts(*session, domain.StructureRevisionProposal{}, candidate)
	if err != nil {
		return nil, err
	}
	return s.store.Revisions.SubmitCandidate(domain.NormalRevisionPolicy{}, storepkg.SubmitRevisionCandidateInput{
		SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey, Artifacts: artifacts,
	})
}

func validateNormalDetailedOutlineChanges(candidate, baseline, formal []domain.VolumeOutline, impact domain.RevisionImpact) error {
	allowed := make(map[string]struct{})
	for _, item := range impact.Items {
		if item.Requirement == domain.StructureImpactRequired && item.ArtifactKind == domain.StructureKindChapter {
			allowed[item.ArtifactID] = struct{}{}
		}
	}
	candidateLocations := normalChapterLocations(candidate)
	formalLocations := normalChapterLocations(formal)
	for chapterID, baselineLocation := range normalChapterLocations(baseline) {
		if _, ok := allowed[chapterID]; ok {
			continue
		}
		candidateLocation, ok := candidateLocations[chapterID]
		if !ok {
			return fmt.Errorf("detailed outline candidate is missing non-impacted chapter %q", chapterID)
		}
		authoritative := baselineLocation.chapter
		if formalLocation, exists := formalLocations[chapterID]; exists {
			authoritative = formalLocation.chapter
		}
		if !normalOutlineContentEqual(authoritative, candidateLocation.chapter) {
			return fmt.Errorf("detailed outline candidate changes non-impacted chapter %q", chapterID)
		}
	}
	return nil
}

func normalImpactHasStructure(impact domain.RevisionImpact) bool {
	for _, item := range impact.Items {
		if item.ArtifactKind == domain.StructureKindVolume || item.ArtifactKind == domain.StructureKindArc {
			return true
		}
	}
	return false
}

func normalSkeletonSignature(candidate []domain.VolumeOutline) string {
	payload, _ := json.Marshal(domain.OriginalPlanningSkeletonProjection(candidate))
	return domain.ContentSignature(payload)
}

func (s *NormalRevisionService) acceptedNormalSnapshot(session *domain.RevisionSession) (*[]domain.VolumeOutline, error) {
	ids := append(append([]string(nil), session.AcceptedVersionIDs...), session.CandidateVersionIDs...)
	for index := len(ids) - 1; index >= 0; index-- {
		version, err := s.store.Revisions.LoadVersion(ids[index])
		if err != nil {
			return nil, err
		}
		if version.ArtifactKind != domain.NormalArtifactStructureSnapshot {
			continue
		}
		var snapshot []domain.VolumeOutline
		if err := json.Unmarshal(version.Payload, &snapshot); err != nil {
			return nil, err
		}
		return &snapshot, nil
	}
	return nil, nil
}

func (s *NormalRevisionService) SubmitProseReworkCandidate(session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil || normalServiceApprovalStage(*session) != domain.NormalApprovalProse {
		return nil, fmt.Errorf("normal prose rework intent stage is not active")
	}
	snapshot, err := s.acceptedNormalSnapshot(session)
	if err != nil || snapshot == nil {
		return nil, fmt.Errorf("load accepted detailed outline snapshot: %w", err)
	}
	locations := normalChapterLocations(*snapshot)
	artifacts := make([]storepkg.CandidateArtifactInput, 0)
	queue := domain.NormalProseReworkQueue{}
	for _, impact := range session.Impact.Items {
		if impact.ArtifactKind != domain.StructureKindChapter || !impact.RequiresBodyRewrite || impact.Requirement != domain.StructureImpactRequired {
			continue
		}
		location, ok := locations[impact.ArtifactID]
		if !ok {
			return nil, fmt.Errorf("prose rework target %q is absent from accepted outline", impact.ArtifactID)
		}
		intent := domain.NormalProseReworkIntent{
			ChapterID: impact.ArtifactID, CurrentNumber: location.chapter.Chapter,
			VolumeID: location.volumeID, ArcID: location.arcID, Reason: strings.Join(impact.DependencyEvidence, "; "),
		}
		payload, _ := json.Marshal(intent)
		artifacts = append(artifacts, storepkg.CandidateArtifactInput{ArtifactID: "rework:" + impact.ArtifactID, ArtifactKind: domain.NormalArtifactProseReworkIntent, Payload: payload})
		queue.ChapterIDs = append(queue.ChapterIDs, impact.ArtifactID)
	}
	if len(queue.ChapterIDs) == 0 {
		return nil, fmt.Errorf("normal prose stage has no exact rework targets")
	}
	queuePayload, _ := json.Marshal(queue)
	artifacts = append(artifacts, storepkg.CandidateArtifactInput{ArtifactID: domain.NormalProseReworkQueueID, ArtifactKind: domain.NormalArtifactProseReworkQueue, Payload: queuePayload})
	return s.store.Revisions.SubmitCandidate(domain.NormalRevisionPolicy{}, storepkg.SubmitRevisionCandidateInput{
		SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey, Artifacts: artifacts,
	})
}

func normalServiceApprovalStage(session domain.RevisionSession) string {
	if len(session.Approvals) >= len(session.ApprovalStages) {
		return ""
	}
	return session.ApprovalStages[len(session.Approvals)].ID
}

type normalChapterLocation struct {
	volumeID string
	arcID    string
	chapter  domain.OutlineEntry
}

func normalChapterLocations(candidate []domain.VolumeOutline) map[string]normalChapterLocation {
	locations := make(map[string]normalChapterLocation)
	for _, volume := range candidate {
		for _, arc := range volume.Arcs {
			for _, chapter := range arc.Chapters {
				locations[chapter.ID] = normalChapterLocation{volumeID: volume.ID, arcID: arc.ID, chapter: chapter}
			}
		}
	}
	return locations
}

func normalStructureCandidateArtifacts(session domain.RevisionSession, proposal domain.StructureRevisionProposal, candidate []domain.VolumeOutline) ([]storepkg.CandidateArtifactInput, error) {
	stage := session.ApprovalStages[len(session.Approvals)].ID
	byID := make(map[string]json.RawMessage)
	skeletonByID := make(map[string]json.RawMessage)
	chapters := make([]domain.BatchChapter, 0)
	locations := normalChapterLocations(candidate)
	for _, volume := range candidate {
		volumePayload, _ := json.Marshal(volume)
		byID[volume.ID] = volumePayload
		for _, arc := range volume.Arcs {
			arcPayload, _ := json.Marshal(arc)
			byID[arc.ID] = arcPayload
			for _, chapter := range arc.Chapters {
				chapterPayload, _ := json.Marshal(chapter)
				byID[chapter.ID] = chapterPayload
				chapters = append(chapters, domain.BatchChapter{
					ID: chapter.ID, VolumeID: volume.ID, ArcID: arc.ID, EstimatedOutputWords: 3000,
					Context: []domain.BatchContextItem{{ID: "adjacent:" + chapter.ID, Kind: domain.BatchContextAdjacentSummary, Units: 1, Necessary: true}},
				})
			}
		}
	}
	for _, volume := range domain.OriginalPlanningSkeletonProjection(candidate) {
		volumePayload, _ := json.Marshal(volume)
		skeletonByID[volume.ID] = volumePayload
		for _, arc := range volume.Arcs {
			arcPayload, _ := json.Marshal(arc)
			skeletonByID[arc.ID] = arcPayload
		}
	}
	planChapters := make([]domain.BatchChapter, 0)
	artifacts := make([]storepkg.CandidateArtifactInput, 0)
	snapshotPayload, _ := json.Marshal(candidate)
	artifacts = append(artifacts, storepkg.CandidateArtifactInput{ArtifactID: domain.NormalStructureSnapshotID, ArtifactKind: domain.NormalArtifactStructureSnapshot, Payload: snapshotPayload})
	for _, impact := range session.Impact.Items {
		if impact.Requirement != domain.StructureImpactRequired || impact.ArtifactKind != domain.StructureKindChapter ||
			(stage == domain.NormalApprovalProse && !impact.RequiresBodyRewrite) {
			continue
		}
		for _, chapter := range chapters {
			if chapter.ID == impact.ArtifactID {
				planChapters = append(planChapters, chapter)
				break
			}
		}
	}
	for _, impact := range session.Impact.Items {
		if impact.ArtifactKind == domain.NormalArtifactBatchPlan || !normalServiceStageIncludes(stage, impact) {
			continue
		}
		payload, ok := byID[impact.ArtifactID]
		if !ok {
			return nil, fmt.Errorf("normal revision candidate is missing impacted artifact %q", impact.ArtifactID)
		}
		if stage == domain.NormalApprovalStructure {
			payload = skeletonByID[impact.ArtifactID]
		} else if stage == domain.NormalApprovalOutline && impact.ArtifactKind == domain.StructureKindChapter {
			location := locations[impact.ArtifactID]
			detailPayload, _ := json.Marshal(domain.NormalDetailedOutlineCandidate{
				ChapterID: impact.ArtifactID, CurrentNumber: location.chapter.Chapter,
				VolumeID: location.volumeID, ArcID: location.arcID, Outline: location.chapter,
			})
			payload = detailPayload
		}
		if impact.ArtifactKind == domain.StructureKindVolume && normalServiceAddsVolume(impact) {
			if proposal.Assessment.NewVolume == nil {
				return nil, fmt.Errorf("new volume %q is missing dramatic-stage evidence", impact.ArtifactID)
			}
			payload, _ = json.Marshal(map[string]any{
				"entry_state":               proposal.Assessment.NewVolume.EntryState,
				"independent_conflict":      proposal.Assessment.NewVolume.IndependentConflict,
				"arc_progression":           proposal.Assessment.NewVolume.ArcProgression,
				"climax":                    proposal.Assessment.NewVolume.Climax,
				"irreversible_outcome":      proposal.Assessment.NewVolume.IrreversibleOutcome,
				"cannot_fit_current_volume": proposal.Assessment.NewVolume.CannotFitCurrentVolume,
				"soft_budget":               proposal.SoftBudget,
				"volume":                    json.RawMessage(payload),
			})
		}
		artifacts = append(artifacts, storepkg.CandidateArtifactInput{ArtifactID: impact.ArtifactID, ArtifactKind: impact.ArtifactKind, Payload: payload})
	}
	if stage == domain.NormalApprovalOutline {
		if len(planChapters) == 0 {
			return nil, fmt.Errorf("normal detailed-outline stage has no exact chapter slots")
		}
		plan, err := NewAdaptiveBatchPlanner(AdaptiveBatchConfig{}).Build(planChapters)
		if err != nil {
			return nil, err
		}
		planPayload, _ := json.Marshal(plan)
		artifacts = append(artifacts, storepkg.CandidateArtifactInput{ArtifactID: "normal-batch-plan", ArtifactKind: domain.NormalArtifactBatchPlan, Payload: planPayload})
	}
	return artifacts, nil
}

func normalServiceStageIncludes(stage string, item domain.RevisionImpactItem) bool {
	switch stage {
	case domain.NormalApprovalStructure:
		return item.ArtifactKind == domain.StructureKindVolume || item.ArtifactKind == domain.StructureKindArc
	case domain.NormalApprovalOutline:
		return item.ArtifactKind == domain.StructureKindChapter
	case domain.NormalApprovalProse:
		return item.ArtifactKind == domain.StructureKindChapter && item.RequiresBodyRewrite
	default:
		return false
	}
}

func normalServiceAddsVolume(item domain.RevisionImpactItem) bool {
	change := strings.ToLower(item.Change)
	return strings.Contains(change, "add") || strings.Contains(change, "append") || strings.Contains(change, "insert")
}

func (s *NormalRevisionService) RecordAuditSet(session *domain.RevisionSession, evidence []domain.RevisionAuditEvidence, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil {
		return nil, fmt.Errorf("normal revision session is required")
	}
	return s.store.Revisions.RecordAudit(domain.NormalRevisionPolicy{}, storepkg.RevisionAuditInput{
		RevisionMutationInput: storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey},
		CandidateSignature:    session.CandidateSignature, Evidence: evidence,
	})
}

func (s *NormalRevisionService) ApproveStage(session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil || session.CurrentApprovalStage() == nil {
		return nil, fmt.Errorf("normal revision approval stage is required")
	}
	return s.store.Revisions.ApproveStage(domain.NormalRevisionPolicy{}, storepkg.RevisionApprovalInput{
		RevisionMutationInput: storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey},
		StageID:               session.CurrentApprovalStage().ID,
	})
}

func (s *NormalRevisionService) PublishStructure(sealed domain.StructureRevisionPreview, session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil {
		return nil, fmt.Errorf("normal revision session is required")
	}
	if strings.TrimSpace(session.PreviewSignature) == "" || sealed.Signature != session.PreviewSignature {
		return nil, fmt.Errorf("normal revision publish preview substitution is not allowed")
	}
	return s.publishAcceptedStructure(session, idempotencyKey)
}

// PublishSealedExpansion keeps the aggregate expansion signature at the final
// publication gate while the accepted canonical snapshot remains the sole
// payload allowed to reach formal storage.
func (s *NormalRevisionService) PublishSealedExpansion(preview domain.ExpansionPreview, session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil || strings.TrimSpace(preview.Signature) == "" || session.PreviewSignature != preview.Signature {
		return nil, fmt.Errorf("normal expansion publish preview substitution is not allowed")
	}
	current, err := s.store.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	for index, sealed := range preview.KernelPreviews {
		current, err = s.kernel.Confirm(sealed, sealed.BaseRevision, current)
		if err != nil {
			return nil, fmt.Errorf("validate expansion publication step %d: %w", index+1, err)
		}
	}
	if domain.StructureSignature(current) != preview.CandidateSignature {
		return nil, fmt.Errorf("normal expansion publication candidate signature mismatch")
	}
	return s.publishAcceptedStructure(session, idempotencyKey)
}

func (s *NormalRevisionService) publishAcceptedStructure(session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	versions, publicationOwner, err := s.store.Revisions.ValidatePublishWithOwner(domain.NormalRevisionPolicy{}, storepkg.RevisionMutationInput{
		SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return nil, err
	}
	var candidate []domain.VolumeOutline
	for _, version := range versions {
		if version.ArtifactID != domain.NormalStructureSnapshotID || version.ArtifactKind != domain.NormalArtifactStructureSnapshot {
			continue
		}
		if err := json.Unmarshal(version.Payload, &candidate); err != nil {
			return nil, fmt.Errorf("decode accepted normal structure snapshot: %w", err)
		}
	}
	if err := domain.ValidateStructureSnapshot(candidate); err != nil {
		return nil, fmt.Errorf("accepted normal structure snapshot is not publishable: %w", err)
	}
	if err := s.store.PublishLayeredStructureForRevision(publicationOwner, candidate, idempotencyKey); err != nil {
		if rollbackErr := s.store.RollbackLayeredStructureForRevision(publicationOwner); rollbackErr != nil {
			return nil, fmt.Errorf("publish normal revision structure: %v; rollback exact formal state: %w", err, rollbackErr)
		}
		return nil, err
	}
	if s.beforeRevisionCommit != nil {
		s.beforeRevisionCommit()
	}
	published, publishErr := s.store.Revisions.PublishWithOwner(domain.NormalRevisionPolicy{}, storepkg.RevisionMutationInput{
		SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey,
	}, publicationOwner)
	if publishErr == nil {
		return published, nil
	}
	if rollbackErr := s.store.RollbackLayeredStructureForRevision(publicationOwner); rollbackErr != nil {
		return nil, fmt.Errorf("publish normal revision: %v; rollback exact formal structure/progress: %w", publishErr, rollbackErr)
	}
	return nil, publishErr
}
