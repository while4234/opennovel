package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const (
	RevisionModeNormal          RevisionMode = "normal"
	NormalRevisionPolicyID                   = "ainovel.normal-revision"
	NormalRevisionPolicyVersion              = "1"

	NormalApprovalStructure = "structure"
	NormalApprovalOutline   = "detailed_outline"
	NormalApprovalProse     = "prose"

	NormalArtifactBatchPlan         = "batch_plan"
	NormalArtifactStructureSnapshot = "structure_snapshot"
	NormalArtifactProseReworkIntent = "prose_rework_intent"
	NormalArtifactProseReworkQueue  = "prose_rework_queue"

	NormalStructureSnapshotID = "normal-structure-snapshot"
	NormalProseReworkQueueID  = "normal-prose-rework-queue"
)

type NormalDetailedOutlineCandidate struct {
	ChapterID     string       `json:"chapter_id"`
	CurrentNumber int          `json:"current_number"`
	VolumeID      string       `json:"volume_id"`
	ArcID         string       `json:"arc_id"`
	Outline       OutlineEntry `json:"outline"`
}

type NormalProseReworkIntent struct {
	ChapterID     string `json:"chapter_id"`
	CurrentNumber int    `json:"current_number"`
	VolumeID      string `json:"volume_id"`
	ArcID         string `json:"arc_id"`
	Reason        string `json:"reason"`
}

type NormalProseReworkQueue struct {
	ChapterIDs []string `json:"chapter_ids"`
}

type normalScopedDetailVersion struct {
	version ArtifactVersion
	detail  NormalDetailedOutlineCandidate
}

// NormalRevisionPolicy is the original-fiction implementation of the shared
// RevisionPolicy. Its zero value is ready to use and deliberately carries no
// adaptation configuration or original-novel material.
type NormalRevisionPolicy struct{}

func (NormalRevisionPolicy) Mode() RevisionMode { return RevisionModeNormal }

func (NormalRevisionPolicy) Identity() (string, string) {
	return NormalRevisionPolicyID, NormalRevisionPolicyVersion
}

func (NormalRevisionPolicy) ContinueAfterApproval(session RevisionSession, approved RevisionApprovalStage) bool {
	return approved.ID != "" && len(session.Approvals) < len(session.ApprovalStages)
}

func (NormalRevisionPolicy) ApprovalStages(impact RevisionImpact) ([]RevisionApprovalStage, error) {
	if err := (NormalRevisionPolicy{}).ValidateImpact(impact); err != nil {
		return nil, err
	}
	stages := make([]RevisionApprovalStage, 0, 3)
	if normalImpactChangesStructure(impact) {
		stages = append(stages, RevisionApprovalStage{
			ID: NormalApprovalStructure, Label: "Volume structure and cross-volume handoff",
		})
	}
	stages = append(stages, RevisionApprovalStage{
		ID: NormalApprovalOutline, Label: "Affected detailed outlines",
	})
	if normalImpactRewritesProse(impact) {
		stages = append(stages, RevisionApprovalStage{
			ID: NormalApprovalProse, Label: "Affected chapter prose",
		})
	}
	return stages, nil
}

func (NormalRevisionPolicy) ValidateImpact(impact RevisionImpact) error {
	if err := impact.Validate(); err != nil {
		return err
	}
	batchPlans := 0
	for _, item := range impact.Items {
		if len(item.DependencySourceIDs) != 0 {
			return fmt.Errorf("normal revision artifact %q cannot carry dependency source IDs", item.ArtifactID)
		}
		kind := strings.ToLower(strings.TrimSpace(item.ArtifactKind))
		if normalNameForbidden(kind) {
			return fmt.Errorf("normal revision artifact %q uses forbidden mode field %q", item.ArtifactID, item.ArtifactKind)
		}
		if item.RequiresBodyRewrite && (item.ArtifactKind != StructureKindChapter || item.Requirement != StructureImpactRequired) {
			return fmt.Errorf("normal prose rewrite %q must be a required chapter impact", item.ArtifactID)
		}
		if item.ArtifactKind == NormalArtifactBatchPlan {
			batchPlans++
			if item.Requirement != StructureImpactRequired {
				return fmt.Errorf("normal revision batch plan must be required")
			}
		}
		if item.ArtifactKind == NormalArtifactStructureSnapshot && item.ArtifactID != NormalStructureSnapshotID {
			return fmt.Errorf("normal revision structure snapshot has invalid identity %q", item.ArtifactID)
		}
		if item.ArtifactKind == NormalArtifactProseReworkQueue && item.ArtifactID != NormalProseReworkQueueID {
			return fmt.Errorf("normal revision prose queue has invalid identity %q", item.ArtifactID)
		}
	}
	if batchPlans != 1 {
		return fmt.Errorf("normal revision impact requires exactly one mandatory batch plan, got %d", batchPlans)
	}
	return nil
}

func (NormalRevisionPolicy) ValidateCandidate(session RevisionSession, versions []ArtifactVersion) error {
	if session.Mode != RevisionModeNormal {
		return fmt.Errorf("normal policy cannot validate revision mode %q", session.Mode)
	}
	if len(versions) == 0 {
		return fmt.Errorf("normal revision candidate is required")
	}
	currentStage, err := normalCurrentApprovalStage(session)
	if err != nil {
		return err
	}
	provided := make(map[string]struct{}, len(versions))
	var batchPlan *BatchPlan
	impactByID := make(map[string]RevisionImpactItem, len(session.Impact.Items))
	for _, item := range session.Impact.Items {
		impactByID[item.ArtifactID] = item
	}
	for _, version := range versions {
		if err := version.Validate(); err != nil {
			return err
		}
		if normalNameForbidden(strings.TrimSpace(version.ArtifactKind)) {
			return fmt.Errorf("normal revision candidate %q uses forbidden artifact kind %q", version.ArtifactID, version.ArtifactKind)
		}
		impactItem, ok := impactByID[version.ArtifactID]
		if !ok || !normalStageIncludesImpact(currentStage, impactItem) {
			return fmt.Errorf("normal revision stage %q cannot include artifact %q", currentStage, version.ArtifactID)
		}
		if err := validateNormalJSON(version.Payload); err != nil {
			return fmt.Errorf("normal revision candidate %q: %w", version.ArtifactID, err)
		}
		if currentStage == NormalApprovalStructure && version.ArtifactKind == StructureKindVolume && normalImpactAddsVolume(impactItem) {
			if err := validateNormalNewVolumeContract(version.Payload); err != nil {
				return fmt.Errorf("normal new-volume candidate %q: %w", version.ArtifactID, err)
			}
		}
		if version.ArtifactKind == NormalArtifactBatchPlan {
			var plan BatchPlan
			if err := json.Unmarshal(version.Payload, &plan); err != nil {
				return fmt.Errorf("decode normal batch plan: %w", err)
			}
			if err := ValidateNormalBatchPlan(plan); err != nil {
				return err
			}
			if batchPlan != nil {
				return fmt.Errorf("normal revision candidate contains multiple batch plans")
			}
			batchPlan = &plan
		}
		if version.ArtifactKind == NormalArtifactStructureSnapshot {
			var snapshot []VolumeOutline
			if err := json.Unmarshal(version.Payload, &snapshot); err != nil {
				return fmt.Errorf("decode normal structure snapshot: %w", err)
			}
			if err := ValidateStructureSnapshotForStage(snapshot, ManuscriptStageProposalComplete); err != nil {
				return fmt.Errorf("validate normal structure snapshot: %w", err)
			}
		}
		if version.ArtifactKind == StructureKindChapter && currentStage == NormalApprovalOutline {
			var detail NormalDetailedOutlineCandidate
			if err := decodeNormalStrict(version.Payload, &detail); err != nil {
				return fmt.Errorf("decode detailed outline candidate: %w", err)
			}
			if strings.TrimSpace(detail.ChapterID) != version.ArtifactID || detail.CurrentNumber <= 0 ||
				strings.TrimSpace(detail.VolumeID) == "" || strings.TrimSpace(detail.ArcID) == "" ||
				detail.Outline.ID != detail.ChapterID || detail.Outline.Chapter != detail.CurrentNumber ||
				strings.TrimSpace(detail.Outline.Title) == "" || strings.TrimSpace(detail.Outline.CoreEvent) == "" ||
				strings.TrimSpace(detail.Outline.Hook) == "" || len(detail.Outline.Scenes) == 0 {
				return fmt.Errorf("detailed outline candidate %q is incomplete or identity-drifted", version.ArtifactID)
			}
		}
		if version.ArtifactKind == NormalArtifactProseReworkIntent {
			var intent NormalProseReworkIntent
			if err := decodeNormalStrict(version.Payload, &intent); err != nil {
				return fmt.Errorf("decode prose rework intent: %w", err)
			}
			if intent.ChapterID == "" || intent.CurrentNumber <= 0 || intent.VolumeID == "" || intent.ArcID == "" || strings.TrimSpace(intent.Reason) == "" {
				return fmt.Errorf("prose rework intent %q is incomplete", version.ArtifactID)
			}
		}
		provided[version.ArtifactID] = struct{}{}
	}
	for _, item := range session.Impact.Items {
		if item.Requirement != StructureImpactRequired || !normalStageIncludesImpact(currentStage, item) {
			continue
		}
		if _, ok := provided[item.ArtifactID]; !ok {
			return fmt.Errorf("normal revision candidate is missing required artifact %q", item.ArtifactID)
		}
	}
	if currentStage == NormalApprovalOutline {
		if batchPlan == nil {
			return fmt.Errorf("normal revision stage %q requires a bounded batch plan", currentStage)
		}
		if err := validateNormalBatchPlanCoverage(*batchPlan, session.Impact, currentStage); err != nil {
			return err
		}
	}
	if currentStage == NormalApprovalProse {
		intents := make(map[string]struct{})
		var queue *NormalProseReworkQueue
		for _, version := range versions {
			switch version.ArtifactKind {
			case NormalArtifactProseReworkIntent:
				var intent NormalProseReworkIntent
				if err := decodeNormalStrict(version.Payload, &intent); err != nil {
					return err
				}
				if version.ArtifactID != "rework:"+intent.ChapterID {
					return fmt.Errorf("prose rework intent %q has stable target drift", version.ArtifactID)
				}
				intents[intent.ChapterID] = struct{}{}
			case NormalArtifactProseReworkQueue:
				var decoded NormalProseReworkQueue
				if err := decodeNormalStrict(version.Payload, &decoded); err != nil {
					return err
				}
				queue = &decoded
			}
		}
		if queue == nil || len(queue.ChapterIDs) != len(intents) {
			return fmt.Errorf("prose rework queue must exactly cover persisted intents")
		}
		seen := make(map[string]struct{}, len(queue.ChapterIDs))
		for _, chapterID := range queue.ChapterIDs {
			if _, ok := intents[chapterID]; !ok {
				return fmt.Errorf("prose rework queue contains untargeted chapter %q", chapterID)
			}
			if _, duplicate := seen[chapterID]; duplicate {
				return fmt.Errorf("prose rework queue duplicates chapter %q", chapterID)
			}
			seen[chapterID] = struct{}{}
		}
	}
	return nil
}

func normalImpactAddsVolume(item RevisionImpactItem) bool {
	change := strings.ToLower(strings.TrimSpace(item.Change))
	return item.Cause == StructureImpactStructureChange &&
		(strings.Contains(change, "add") || strings.Contains(change, "append") || strings.Contains(change, "insert") || strings.Contains(change, "new volume"))
}

func validateNormalNewVolumeContract(payload json.RawMessage) error {
	var contract struct {
		EntryState             string            `json:"entry_state"`
		IndependentConflict    string            `json:"independent_conflict"`
		ArcProgression         string            `json:"arc_progression"`
		Climax                 string            `json:"climax"`
		IrreversibleOutcome    string            `json:"irreversible_outcome"`
		CannotFitCurrentVolume string            `json:"cannot_fit_current_volume"`
		SoftBudget             DynamicSoftBudget `json:"soft_budget"`
	}
	if err := json.Unmarshal(payload, &contract); err != nil {
		return fmt.Errorf("decode dramatic contract: %w", err)
	}
	evidence := DramaticStageEvidence{
		EntryState: contract.EntryState, IndependentConflict: contract.IndependentConflict,
		ArcProgression: contract.ArcProgression, Climax: contract.Climax,
		IrreversibleOutcome: contract.IrreversibleOutcome, CannotFitCurrentVolume: contract.CannotFitCurrentVolume,
	}
	if !evidence.SupportsNewVolume() {
		return fmt.Errorf("complete dramatic evidence is required")
	}
	if err := contract.SoftBudget.Validate(); err != nil {
		return fmt.Errorf("complete dynamic soft budget is required: %w", err)
	}
	return nil
}

func (NormalRevisionPolicy) ValidateAuditSet(session RevisionSession, evidence []RevisionAuditEvidence) error {
	if len(evidence) == 0 {
		return fmt.Errorf("normal revision requires a complete signed audit set")
	}
	if len(session.AuditExpectations) == 0 {
		return fmt.Errorf("normal revision candidate has no persisted audit expectations")
	}
	want := make(map[string]RevisionAuditExpectation, len(session.AuditExpectations))
	for _, expected := range session.AuditExpectations {
		want[expected.Scope+"\x00"+expected.ScopeID] = expected
	}
	seen := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		if err := item.Validate(); err != nil {
			return err
		}
		key := item.Scope + "\x00" + item.ScopeID
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate normal revision audit %s/%s", item.Scope, item.ScopeID)
		}
		expected, exists := want[key]
		if !exists {
			return fmt.Errorf("normal revision audit set contains fictional scope %s/%s", item.Scope, item.ScopeID)
		}
		if item.FromChapter != expected.FromChapter || item.ToChapter != expected.ToChapter || item.ContentSignature != expected.ContentSignature {
			return fmt.Errorf("normal revision audit %s/%s does not match its scope-local signature", item.Scope, item.ScopeID)
		}
		seen[key] = struct{}{}
	}
	for key, expected := range want {
		if _, ok := seen[key]; !ok {
			return fmt.Errorf("normal revision audit set is missing %s/%s", expected.Scope, expected.ScopeID)
		}
	}
	return nil
}

func (NormalRevisionPolicy) AuditExpectations(session RevisionSession, versions []ArtifactVersion) ([]RevisionAuditExpectation, error) {
	stage, err := normalCurrentApprovalStage(session)
	if err != nil {
		return nil, err
	}
	byKind := make(map[string][]ArtifactVersion)
	for _, version := range versions {
		byKind[version.ArtifactKind] = append(byKind[version.ArtifactKind], version)
	}
	expectations := make([]RevisionAuditExpectation, 0)
	switch stage {
	case NormalApprovalStructure:
		structural := append(append([]ArtifactVersion(nil), byKind[StructureKindVolume]...), byKind[StructureKindArc]...)
		for _, version := range byKind[StructureKindVolume] {
			expectations = append(expectations, RevisionAuditExpectation{Scope: "skeleton_volume", ScopeID: version.ArtifactID, ContentSignature: version.ContentSignature})
		}
		for _, version := range byKind[StructureKindArc] {
			expectations = append(expectations, RevisionAuditExpectation{Scope: "skeleton_arc", ScopeID: version.ArtifactID, ContentSignature: version.ContentSignature})
		}
		if len(structural) == 0 {
			return nil, fmt.Errorf("structure audit requires exact changed or adjacent arc/volume candidates")
		}
		for offset := 0; offset < len(structural); offset += 4 {
			end := offset + 4
			if end > len(structural) {
				end = len(structural)
			}
			expectations = append(expectations, compositeAuditExpectation("skeleton_book_batch", structural[offset:end]))
		}
		expectations = append(expectations, compositeAuditExpectation("skeleton_book", structural))
	case NormalApprovalOutline:
		details := make([]normalScopedDetailVersion, 0, len(byKind[StructureKindChapter]))
		for _, version := range byKind[StructureKindChapter] {
			var detail NormalDetailedOutlineCandidate
			if err := json.Unmarshal(version.Payload, &detail); err != nil {
				return nil, err
			}
			details = append(details, normalScopedDetailVersion{version: version, detail: detail})
			expectations = append(expectations, RevisionAuditExpectation{
				Scope: "chapter", ScopeID: detail.ChapterID, FromChapter: detail.CurrentNumber,
				ToChapter: detail.CurrentNumber, ContentSignature: version.ContentSignature,
			})
		}
		if len(details) == 0 {
			return nil, fmt.Errorf("detailed-outline audit requires at least one exact chapter")
		}
		expectations = append(expectations, groupedDetailedExpectations("arc", details, func(item normalScopedDetailVersion) string { return item.detail.ArcID })...)
		expectations = append(expectations, groupedDetailedExpectations("volume", details, func(item normalScopedDetailVersion) string { return item.detail.VolumeID })...)
		plan, err := normalCandidateBatchPlan(byKind[NormalArtifactBatchPlan])
		if err != nil {
			return nil, err
		}
		byChapter := make(map[string]ArtifactVersion, len(details))
		for _, detail := range details {
			byChapter[detail.detail.ChapterID] = detail.version
		}
		for _, batch := range plan.Batches {
			batchVersions := make([]ArtifactVersion, 0, len(batch.ChapterIDs))
			for _, chapterID := range batch.ChapterIDs {
				batchVersions = append(batchVersions, byChapter[chapterID])
			}
			expectation := compositeAuditExpectation("book_batch", batchVersions)
			expectation.ScopeID = batch.ID
			expectations = append(expectations, expectation)
		}
		all := make([]ArtifactVersion, 0, len(details))
		for _, detail := range details {
			all = append(all, detail.version)
		}
		expectations = append(expectations, compositeAuditExpectation("book", all))
	case NormalApprovalProse:
		intents := byKind[NormalArtifactProseReworkIntent]
		if len(intents) == 0 || len(byKind[NormalArtifactProseReworkQueue]) != 1 {
			return nil, fmt.Errorf("prose stage requires exact rework intents and one queue")
		}
		for _, version := range intents {
			var intent NormalProseReworkIntent
			if err := json.Unmarshal(version.Payload, &intent); err != nil {
				return nil, err
			}
			expectations = append(expectations, RevisionAuditExpectation{
				Scope: "rework_intent", ScopeID: intent.ChapterID, FromChapter: intent.CurrentNumber,
				ToChapter: intent.CurrentNumber, ContentSignature: version.ContentSignature,
			})
		}
		queue := byKind[NormalArtifactProseReworkQueue][0]
		expectations = append(expectations, RevisionAuditExpectation{Scope: "rework_queue", ScopeID: queue.ArtifactID, ContentSignature: queue.ContentSignature})
	default:
		return nil, fmt.Errorf("cannot derive audits for normal revision stage %q", stage)
	}
	return expectations, nil
}

func normalCandidateBatchPlan(versions []ArtifactVersion) (BatchPlan, error) {
	if len(versions) != 1 {
		return BatchPlan{}, fmt.Errorf("normal candidate requires exactly one batch plan")
	}
	var plan BatchPlan
	if err := json.Unmarshal(versions[0].Payload, &plan); err != nil {
		return BatchPlan{}, err
	}
	return plan, nil
}

func compositeAuditExpectation(scope string, versions []ArtifactVersion) RevisionAuditExpectation {
	identities := make([]string, 0, len(versions))
	for _, version := range versions {
		identities = append(identities, version.ArtifactID+":"+version.ContentSignature)
	}
	slices.Sort(identities)
	payload, _ := json.Marshal(identities)
	return RevisionAuditExpectation{Scope: scope, ScopeID: strings.Join(artifactIDs(versions), "+"), ContentSignature: ContentSignature(payload)}
}

func artifactIDs(versions []ArtifactVersion) []string {
	ids := make([]string, 0, len(versions))
	for _, version := range versions {
		ids = append(ids, version.ArtifactID)
	}
	slices.Sort(ids)
	return ids
}

func groupedDetailedExpectations(scope string, items []normalScopedDetailVersion, group func(normalScopedDetailVersion) string) []RevisionAuditExpectation {
	grouped := make(map[string][]ArtifactVersion)
	for _, item := range items {
		grouped[group(item)] = append(grouped[group(item)], item.version)
	}
	ids := make([]string, 0, len(grouped))
	for id := range grouped {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	result := make([]RevisionAuditExpectation, 0, len(ids))
	for _, id := range ids {
		expectation := compositeAuditExpectation(scope, grouped[id])
		expectation.ScopeID = id
		result = append(result, expectation)
	}
	return result
}

func (NormalRevisionPolicy) Route(session RevisionSession) (*RevisionRoute, error) {
	switch session.Stage {
	case RevisionStageCandidateGenerating:
		stage, err := normalCurrentApprovalStage(session)
		if err != nil {
			return nil, err
		}
		task := "generate only affected detailed outlines in bounded BatchPlan work"
		if stage == NormalApprovalStructure {
			task = "generate the changed volume, adjacent handoff, bounded book batches, and whole-book skeleton before affected detailed outlines"
		} else if stage == NormalApprovalProse {
			task = "rewrite only chapters carrying required body-rewrite evidence"
		} else if normalImpactRewritesProse(session.Impact) {
			task += "; rewrite only chapters carrying required body-rewrite evidence"
		}
		return &RevisionRoute{Agent: "architect_long", Task: task, Reason: "original-fiction revision"}, nil
	case RevisionStageCandidateAudit:
		stage, err := normalCurrentApprovalStage(session)
		if err != nil {
			return nil, err
		}
		task := "audit current signatures at chapter, arc, volume, bounded book-batch, and whole-book levels"
		if stage == NormalApprovalStructure {
			task = "audit changed and adjacent volume handoffs, bounded book batches, and the whole-book skeleton; then audit affected detailed outlines"
		} else if stage == NormalApprovalProse {
			task = "audit only rewritten chapter prose and its affected aggregate scopes against current signatures"
		}
		return &RevisionRoute{Agent: "editor", Task: task, Reason: "signature-bound original-fiction audit"}, nil
	default:
		return nil, nil
	}
}

func normalCurrentApprovalStage(session RevisionSession) (string, error) {
	stages := session.ApprovalStages
	if len(stages) == 0 {
		var err error
		stages, err = (NormalRevisionPolicy{}).ApprovalStages(session.Impact)
		if err != nil {
			return "", err
		}
	}
	if len(session.Approvals) >= len(stages) {
		return "publish", nil
	}
	return stages[len(session.Approvals)].ID, nil
}

func normalStageIncludesImpact(stage string, item RevisionImpactItem) bool {
	switch stage {
	case NormalApprovalStructure:
		return item.ArtifactKind == StructureKindVolume || item.ArtifactKind == StructureKindArc ||
			item.ArtifactKind == NormalArtifactStructureSnapshot
	case NormalApprovalOutline:
		return item.ArtifactKind == StructureKindChapter || item.ArtifactKind == NormalArtifactStructureSnapshot || item.ArtifactKind == NormalArtifactBatchPlan
	case NormalApprovalProse:
		return item.ArtifactKind == NormalArtifactProseReworkIntent || item.ArtifactKind == NormalArtifactProseReworkQueue
	case "publish":
		return true
	default:
		return false
	}
}

func ValidateNormalBatchPlan(plan BatchPlan) error {
	if len(plan.Batches) == 0 {
		return fmt.Errorf("normal revision batch plan requires at least one batch")
	}
	seenBatches := make(map[string]struct{}, len(plan.Batches))
	seenChapters := make(map[string]struct{})
	for index, batch := range plan.Batches {
		if strings.TrimSpace(batch.ID) == "" || batch.Index != index+1 || len(batch.ChapterIDs) == 0 || len(batch.ChapterIDs) > 4 ||
			strings.TrimSpace(batch.VolumeID) == "" || strings.TrimSpace(batch.ArcID) == "" {
			return fmt.Errorf("normal revision batch %d has invalid identity or scope", index+1)
		}
		if _, duplicate := seenBatches[batch.ID]; duplicate {
			return fmt.Errorf("normal revision batch %q is duplicated", batch.ID)
		}
		seenBatches[batch.ID] = struct{}{}
		if batch.Status != BatchStatusPending || batch.EstimatedOutputWords <= 0 || batch.ContextUnits < 0 {
			return fmt.Errorf("normal revision batch %q must be a startable bounded pending batch", batch.ID)
		}
		for _, chapterID := range batch.ChapterIDs {
			chapterID = strings.TrimSpace(chapterID)
			if chapterID == "" {
				return fmt.Errorf("normal revision batch %q contains an empty chapter ID", batch.ID)
			}
			if _, duplicate := seenChapters[chapterID]; duplicate {
				return fmt.Errorf("normal revision chapter %q appears in multiple batches", chapterID)
			}
			seenChapters[chapterID] = struct{}{}
		}
		for _, item := range batch.Context {
			if item.Kind == BatchContextSourceAnchor || normalNameForbidden(strings.ToLower(string(item.Kind))) {
				return fmt.Errorf("normal revision batch %q contains forbidden context kind %q", batch.ID, item.Kind)
			}
		}
	}
	if len(plan.VolumeReviews) == 0 || strings.TrimSpace(plan.WholeBookReview.ScopeID) == "" ||
		plan.WholeBookReview.Status != BatchReviewPending {
		return fmt.Errorf("normal revision batch plan requires volume and whole-book reviews")
	}
	volumeIDs := make(map[string]struct{})
	for _, batch := range plan.Batches {
		volumeIDs[batch.VolumeID] = struct{}{}
	}
	if len(plan.VolumeReviews) != len(volumeIDs) {
		return fmt.Errorf("normal revision batch plan volume reviews do not exactly cover batch volumes")
	}
	for _, review := range plan.VolumeReviews {
		if _, ok := volumeIDs[review.ScopeID]; !ok || review.Status != BatchReviewPending {
			return fmt.Errorf("normal revision batch plan has invalid volume review %q", review.ScopeID)
		}
		delete(volumeIDs, review.ScopeID)
	}
	return nil
}

func validateNormalBatchPlanCoverage(plan BatchPlan, impact RevisionImpact, stage string) error {
	want := make(map[string]struct{})
	for _, item := range impact.Items {
		if item.Requirement == StructureImpactRequired && item.ArtifactKind == StructureKindChapter {
			want[item.ArtifactID] = struct{}{}
		}
	}
	got := make(map[string]struct{})
	for _, batch := range plan.Batches {
		for _, chapterID := range batch.ChapterIDs {
			got[chapterID] = struct{}{}
		}
	}
	if len(want) != len(got) {
		return fmt.Errorf("normal revision batch plan must exactly cover %d impacted chapters, got %d", len(want), len(got))
	}
	for chapterID := range want {
		if _, ok := got[chapterID]; !ok {
			return fmt.Errorf("normal revision batch plan is missing impacted chapter %q", chapterID)
		}
	}
	return nil
}

func validateNormalJSON(payload json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	return walkNormalJSON(value, "$")
}

func decodeNormalStrict(payload json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// ValidateNormalRevisionPayload rejects forbidden transport fields before
// decoding can silently ignore them.
func ValidateNormalRevisionPayload(payload json.RawMessage) error {
	return validateNormalJSON(payload)
}

func walkNormalJSON(value any, path string) error {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			if normalNameForbidden(strings.TrimSpace(key)) {
				return fmt.Errorf("forbidden original-novel or adaptation field at %s.%s", path, key)
			}
			if err := walkNormalJSON(typed[key], path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for index, item := range typed {
			if err := walkNormalJSON(item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalNameForbidden(name string) bool {
	words := normalFieldWords(name)
	for index, word := range words {
		if word == "source" || word == "adaptation" || word == "adapt" {
			return true
		}
		if word == "original" && index+1 < len(words) && words[index+1] == "novel" {
			return true
		}
	}
	return false
}

func normalFieldWords(name string) []string {
	var normalized strings.Builder
	for index, r := range strings.TrimSpace(name) {
		if r == '_' || r == '-' || r == '.' || r == ' ' {
			normalized.WriteByte(' ')
			continue
		}
		if index > 0 && r >= 'A' && r <= 'Z' {
			normalized.WriteByte(' ')
		}
		normalized.WriteRune(r)
	}
	return strings.Fields(strings.ToLower(normalized.String()))
}

func normalImpactChangesStructure(impact RevisionImpact) bool {
	for _, item := range impact.Items {
		if item.Cause == StructureImpactStructureChange || item.ArtifactKind == StructureKindVolume || item.ArtifactKind == StructureKindArc {
			return true
		}
	}
	return false
}

func normalImpactRewritesProse(impact RevisionImpact) bool {
	for _, item := range impact.Items {
		if item.RequiresBodyRewrite {
			return true
		}
	}
	return false
}
