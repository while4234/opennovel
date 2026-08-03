package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

const (
	RevisionModeAdaptation          RevisionMode = "adaptation"
	AdaptationRevisionPolicyID                   = "ainovel.adaptation-revision"
	AdaptationRevisionPolicyVersion              = "1"

	AdaptationApprovalStructure = "adaptation_structure"
	AdaptationApprovalOutline   = "adaptation_detailed_outline"
	AdaptationApprovalProse     = "adaptation_prose"

	AdaptationRevisionArtifactBatchPlan         = "adaptation_batch_plan"
	AdaptationRevisionArtifactPlanSnapshot      = "adaptation_plan_snapshot"
	AdaptationRevisionArtifactProseReworkIntent = "adaptation_prose_rework_intent"
	AdaptationRevisionArtifactProseReworkQueue  = "adaptation_prose_rework_queue"

	AdaptationRevisionPlanSnapshotID = "adaptation-plan-snapshot"
	AdaptationRevisionBatchPlanID    = "adaptation-batch-plan"
	AdaptationRevisionProseQueueID   = "adaptation-prose-rework-queue"
)

// AdaptationRevisionPolicy owns adaptation-only revision contracts. BasePlan
// and SourceManifest are immutable inputs loaded from the active project; they
// are intentionally not persisted inside candidate artifacts.
type AdaptationRevisionPolicy struct {
	Stage           ManuscriptStage
	BasePlan        *AdaptationPlan
	SourceManifest  *AdaptationSourceManifest
	CompletedTarget []string
}

type AdaptationPlanRevisionCandidate struct {
	Stage           ManuscriptStage `json:"stage"`
	SourceSignature string          `json:"source_signature,omitempty"`
	Plan            AdaptationPlan  `json:"plan"`
}

type AdaptationDetailedOutlineCandidate struct {
	ChapterID     string                `json:"chapter_id"`
	CurrentNumber int                   `json:"current_number"`
	VolumeID      string                `json:"volume_id"`
	ArcID         string                `json:"arc_id"`
	Outline       AdaptationChapterPlan `json:"outline"`
}

type AdaptationRevisionArcCandidate struct {
	ID               string   `json:"id"`
	VolumeID         string   `json:"volume_id"`
	TargetFrom       int      `json:"target_from"`
	TargetTo         int      `json:"target_to"`
	SourceChapters   []int    `json:"source_chapters,omitempty"`
	MainlineEventIDs []string `json:"mainline_event_ids,omitempty"`
}

type AdaptationProseReworkIntent struct {
	ChapterID     string `json:"chapter_id"`
	CurrentNumber int    `json:"current_number"`
	VolumeID      string `json:"volume_id"`
	ArcID         string `json:"arc_id"`
	Reason        string `json:"reason"`
}

type AdaptationProseReworkQueue struct {
	ChapterIDs []string `json:"chapter_ids"`
}

// AdaptationContractChangeRequiredError is returned when a requested or
// generated revision would weaken the protected source-story contract. The
// revision must stop; silently changing the contract is never a repair.
type AdaptationContractChangeRequiredError struct {
	EventIDs []string
	Reason   string
}

func (e *AdaptationContractChangeRequiredError) Error() string {
	if e == nil {
		return "adaptation contract change is required"
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "protected source mainline would be changed"
	}
	if len(e.EventIDs) == 0 {
		return reason + "; change the adaptation contract before retrying"
	}
	return fmt.Sprintf("%s (events: %s); change the adaptation contract before retrying", reason, strings.Join(e.EventIDs, ", "))
}

func IsAdaptationContractChangeRequired(err error) bool {
	var target *AdaptationContractChangeRequiredError
	return errors.As(err, &target)
}

func NewAdaptationRevisionPolicy(stage ManuscriptStage, base *AdaptationPlan, manifest *AdaptationSourceManifest) AdaptationRevisionPolicy {
	return AdaptationRevisionPolicy{Stage: stage, BasePlan: base, SourceManifest: manifest}
}

func (AdaptationRevisionPolicy) Mode() RevisionMode { return RevisionModeAdaptation }

func (AdaptationRevisionPolicy) Identity() (string, string) {
	return AdaptationRevisionPolicyID, AdaptationRevisionPolicyVersion
}

func (AdaptationRevisionPolicy) ContinueAfterApproval(session RevisionSession, approved RevisionApprovalStage) bool {
	return approved.ID != "" && len(session.Approvals) < len(session.ApprovalStages)
}

func (p AdaptationRevisionPolicy) ApprovalStages(impact RevisionImpact) ([]RevisionApprovalStage, error) {
	if err := p.ValidateImpact(impact); err != nil {
		return nil, err
	}
	stages := make([]RevisionApprovalStage, 0, 3)
	if adaptationImpactChangesStructure(impact) {
		stages = append(stages, RevisionApprovalStage{ID: AdaptationApprovalStructure, Label: "Adaptation volume structure and source coverage"})
	}
	stages = append(stages, RevisionApprovalStage{ID: AdaptationApprovalOutline, Label: "Affected adaptation detailed outlines"})
	if adaptationImpactRewritesProse(impact) {
		stages = append(stages, RevisionApprovalStage{ID: AdaptationApprovalProse, Label: "Affected adaptation chapter prose"})
	}
	return stages, nil
}

func (p AdaptationRevisionPolicy) ValidateImpact(impact RevisionImpact) error {
	if err := impact.Validate(); err != nil {
		return err
	}
	if p.Stage != "" && !p.Stage.Valid() {
		return fmt.Errorf("unsupported adaptation manuscript stage %q", p.Stage)
	}
	batchPlans, snapshots := 0, 0
	protected := p.protectedMainlineEvents()
	completed := make(map[string]struct{}, len(p.CompletedTarget))
	for _, id := range p.CompletedTarget {
		completed[strings.TrimSpace(id)] = struct{}{}
	}
	for _, item := range impact.Items {
		if item.RequiresBodyRewrite && (item.ArtifactKind != StructureKindChapter || item.Requirement != StructureImpactRequired) {
			return fmt.Errorf("adaptation prose rewrite %q must be a required chapter impact", item.ArtifactID)
		}
		if item.Cause != StructureImpactDisplayRenumber && len(item.DependencyEvidence) == 0 {
			return fmt.Errorf("adaptation impact %q requires dependency evidence", item.ArtifactID)
		}
		if item.ArtifactKind == AdaptationRevisionArtifactBatchPlan {
			batchPlans++
			if item.ArtifactID != AdaptationRevisionBatchPlanID || item.Requirement != StructureImpactRequired {
				return fmt.Errorf("adaptation revision batch plan must use the fixed required identity")
			}
		}
		if item.ArtifactKind == AdaptationRevisionArtifactPlanSnapshot {
			snapshots++
			if item.ArtifactID != AdaptationRevisionPlanSnapshotID || item.Requirement != StructureImpactRequired {
				return fmt.Errorf("adaptation plan snapshot must use the fixed required identity")
			}
		}
		if item.ArtifactKind == AdaptationRevisionArtifactProseReworkQueue && item.ArtifactID != AdaptationRevisionProseQueueID {
			return fmt.Errorf("adaptation prose queue has invalid identity %q", item.ArtifactID)
		}
		if _, isCompleted := completed[item.ArtifactID]; isCompleted && strings.Contains(strings.ToLower(item.Change), "move") {
			return fmt.Errorf("written adaptation target chapter %q cannot be moved", item.ArtifactID)
		}
		if destructiveAdaptationChange(item.Change) {
			var affected []string
			for _, sourceID := range item.DependencySourceIDs {
				if protected[sourceID] {
					affected = append(affected, sourceID)
				}
			}
			if len(affected) > 0 {
				slices.Sort(affected)
				return &AdaptationContractChangeRequiredError{EventIDs: slices.Compact(affected), Reason: "revision feedback conflicts with protected source mainline"}
			}
		}
	}
	if batchPlans != 1 {
		return fmt.Errorf("adaptation revision impact requires exactly one mandatory BatchPlan, got %d", batchPlans)
	}
	if snapshots != 1 {
		return fmt.Errorf("adaptation revision impact requires exactly one immutable plan snapshot, got %d", snapshots)
	}
	return nil
}

func (p AdaptationRevisionPolicy) ValidateCandidate(session RevisionSession, versions []ArtifactVersion) error {
	if session.Mode != RevisionModeAdaptation {
		return fmt.Errorf("adaptation policy cannot validate revision mode %q", session.Mode)
	}
	if len(versions) == 0 {
		return fmt.Errorf("adaptation revision candidate is required")
	}
	stage, err := adaptationCurrentApprovalStage(session, p)
	if err != nil {
		return err
	}
	impactByID := make(map[string]RevisionImpactItem, len(session.Impact.Items))
	for _, item := range session.Impact.Items {
		impactByID[item.ArtifactID] = item
	}
	provided := make(map[string]struct{}, len(versions))
	byKind := make(map[string][]ArtifactVersion)
	var batchPlan *BatchPlan
	var candidatePlan *AdaptationPlan
	for _, version := range versions {
		if err := version.Validate(); err != nil {
			return err
		}
		impact, ok := impactByID[version.ArtifactID]
		if !ok || impact.ArtifactKind != version.ArtifactKind || !adaptationStageIncludesImpact(stage, impact) {
			return fmt.Errorf("adaptation revision stage %q cannot include artifact %q", stage, version.ArtifactID)
		}
		if _, duplicate := provided[version.ArtifactID]; duplicate {
			return fmt.Errorf("adaptation revision duplicates artifact %q", version.ArtifactID)
		}
		provided[version.ArtifactID] = struct{}{}
		byKind[version.ArtifactKind] = append(byKind[version.ArtifactKind], version)

		switch version.ArtifactKind {
		case AdaptationRevisionArtifactBatchPlan:
			var plan BatchPlan
			if err := decodeAdaptationStrict(version.Payload, &plan); err != nil {
				return fmt.Errorf("decode adaptation BatchPlan: %w", err)
			}
			if err := ValidateAdaptationRevisionBatchPlan(plan); err != nil {
				return err
			}
			batchPlan = &plan
		case AdaptationRevisionArtifactPlanSnapshot:
			plan, err := p.decodePlanCandidate(version.Payload)
			if err != nil {
				return err
			}
			candidatePlan = &plan
		case StructureKindChapter:
			if stage == AdaptationApprovalOutline || stage == "publish" {
				if _, err := decodeAdaptationDetailedOutline(version); err != nil {
					return err
				}
			}
		case AdaptationRevisionArtifactProseReworkIntent:
			var intent AdaptationProseReworkIntent
			if err := decodeAdaptationStrict(version.Payload, &intent); err != nil {
				return fmt.Errorf("decode adaptation prose rework intent: %w", err)
			}
			if strings.TrimSpace(intent.ChapterID) == "" || intent.CurrentNumber <= 0 || strings.TrimSpace(intent.VolumeID) == "" || strings.TrimSpace(intent.ArcID) == "" || strings.TrimSpace(intent.Reason) == "" {
				return fmt.Errorf("adaptation prose rework intent %q is incomplete", version.ArtifactID)
			}
			if version.ArtifactID != "rework:"+intent.ChapterID {
				return fmt.Errorf("adaptation prose rework intent %q has stable target drift", version.ArtifactID)
			}
		}
	}
	for _, item := range session.Impact.Items {
		if item.Requirement != StructureImpactRequired || !adaptationStageIncludesImpact(stage, item) {
			continue
		}
		if _, ok := provided[item.ArtifactID]; !ok {
			return fmt.Errorf("adaptation revision candidate is missing required artifact %q", item.ArtifactID)
		}
	}

	switch stage {
	case AdaptationApprovalStructure:
		if candidatePlan == nil || batchPlan == nil {
			return fmt.Errorf("adaptation structure revision requires a plan snapshot and bounded BatchPlan")
		}
		if err := validateAdaptationStructureSkeleton(*candidatePlan, session.Impact); err != nil {
			return err
		}
		if err := validateAdaptationStructuralArtifacts(*candidatePlan, byKind); err != nil {
			return err
		}
		if err := p.validatePlan(*candidatePlan); err != nil {
			return err
		}
		if err := validateAdaptationBatchCoverage(*batchPlan, session.Impact, stage, candidatePlan); err != nil {
			return err
		}
	case AdaptationApprovalOutline:
		if batchPlan == nil {
			return fmt.Errorf("adaptation detailed-outline revision requires a bounded BatchPlan")
		}
		var merged AdaptationPlan
		if candidatePlan != nil {
			merged, err = mergeAdaptationDetailedOutlines(*candidatePlan, byKind[StructureKindChapter])
		} else {
			merged, err = p.mergeDetailedCandidates(byKind[StructureKindChapter])
		}
		if err != nil {
			return err
		}
		if err := p.validatePlan(merged); err != nil {
			return err
		}
		if err := validateAdaptationBatchCoverage(*batchPlan, session.Impact, stage, &merged); err != nil {
			return err
		}
	case AdaptationApprovalProse:
		if err := validateAdaptationProseQueue(byKind); err != nil {
			return err
		}
	case "publish":
		if candidatePlan == nil {
			return fmt.Errorf("published adaptation revision is missing the signed plan snapshot")
		}
		merged := *candidatePlan
		if details := byKind[StructureKindChapter]; len(details) > 0 {
			merged, err = mergeAdaptationDetailedOutlines(merged, details)
			if err != nil {
				return err
			}
		}
		if err := p.validatePlan(merged); err != nil {
			return err
		}
		if adaptationImpactRewritesProse(session.Impact) {
			if err := validateAdaptationProseQueue(byKind); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p AdaptationRevisionPolicy) ValidateAuditSet(session RevisionSession, evidence []RevisionAuditEvidence) error {
	if len(evidence) == 0 || len(session.AuditExpectations) == 0 {
		return fmt.Errorf("adaptation revision requires a complete signed audit set")
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
			return fmt.Errorf("duplicate adaptation revision audit %s/%s", item.Scope, item.ScopeID)
		}
		expected, exists := want[key]
		if !exists {
			return fmt.Errorf("adaptation revision audit contains fictional scope %s/%s", item.Scope, item.ScopeID)
		}
		if item.FromChapter != expected.FromChapter || item.ToChapter != expected.ToChapter || item.ContentSignature != expected.ContentSignature {
			return fmt.Errorf("adaptation revision audit %s/%s does not match its scope-local signature", item.Scope, item.ScopeID)
		}
		seen[key] = struct{}{}
	}
	for key, expected := range want {
		if _, ok := seen[key]; !ok {
			return fmt.Errorf("adaptation revision audit is missing %s/%s", expected.Scope, expected.ScopeID)
		}
	}
	return nil
}

func (p AdaptationRevisionPolicy) AuditExpectations(session RevisionSession, versions []ArtifactVersion) ([]RevisionAuditExpectation, error) {
	stage, err := adaptationCurrentApprovalStage(session, p)
	if err != nil {
		return nil, err
	}
	byKind := make(map[string][]ArtifactVersion)
	for _, version := range versions {
		byKind[version.ArtifactKind] = append(byKind[version.ArtifactKind], version)
	}
	var expectations []RevisionAuditExpectation
	if snapshots := byKind[AdaptationRevisionArtifactPlanSnapshot]; len(snapshots) == 1 {
		expectations = append(expectations, RevisionAuditExpectation{Scope: "adaptation_contract", ScopeID: snapshots[0].ArtifactID, ContentSignature: snapshots[0].ContentSignature})
	}
	switch stage {
	case AdaptationApprovalStructure:
		structural := append(append([]ArtifactVersion(nil), byKind[StructureKindVolume]...), byKind[StructureKindArc]...)
		if len(structural) == 0 {
			structural = append(structural, byKind[AdaptationRevisionArtifactPlanSnapshot]...)
		}
		if len(structural) == 0 {
			return nil, fmt.Errorf("adaptation structure audit requires signed structure artifacts")
		}
		for _, version := range structural {
			expectations = append(expectations, RevisionAuditExpectation{Scope: "structure_local", ScopeID: version.ArtifactID, ContentSignature: version.ContentSignature})
		}
		for offset := 0; offset < len(structural); offset += 4 {
			end := min(offset+4, len(structural))
			expectations = append(expectations, compositeAuditExpectation("structure_parent_batch", structural[offset:end]))
		}
		expectations = append(expectations, compositeAuditExpectation("structure_global", structural))
	case AdaptationApprovalOutline:
		details := byKind[StructureKindChapter]
		if len(details) == 0 {
			return nil, fmt.Errorf("adaptation detailed-outline audit requires exact chapter candidates")
		}
		publishable, err := p.adaptationPublishableAuditPlan(byKind, details)
		if err != nil {
			return nil, err
		}
		byChapter := make(map[string]ArtifactVersion, len(publishable.Chapters))
		byVolume := make(map[string][]ArtifactVersion)
		for _, chapter := range publishable.Chapters {
			payload, _ := json.Marshal(chapter)
			byChapter[chapter.ID] = ArtifactVersion{ArtifactID: chapter.ID, ArtifactKind: StructureKindChapter, Payload: payload, ContentSignature: JSONContentSignature(payload)}
		}
		for _, version := range details {
			detail, err := decodeAdaptationDetailedOutline(version)
			if err != nil {
				return nil, err
			}
			chapterVersion := byChapter[detail.ChapterID]
			expectations = append(expectations, RevisionAuditExpectation{
				Scope: "chapter_deterministic", ScopeID: detail.ChapterID, FromChapter: detail.CurrentNumber,
				ToChapter: detail.CurrentNumber, ContentSignature: chapterVersion.ContentSignature,
			})
			expectations = append(expectations, RevisionAuditExpectation{
				Scope: "chapter_semantic", ScopeID: detail.ChapterID, FromChapter: detail.CurrentNumber,
				ToChapter: detail.CurrentNumber, ContentSignature: chapterVersion.ContentSignature,
			})
		}
		plan, err := adaptationCandidateBatchPlan(byKind[AdaptationRevisionArtifactBatchPlan])
		if err != nil {
			return nil, err
		}
		for _, batch := range plan.Batches {
			batchVersions := make([]ArtifactVersion, 0, len(batch.ChapterIDs))
			for _, chapterID := range batch.ChapterIDs {
				version, ok := byChapter[chapterID]
				if !ok {
					return nil, fmt.Errorf("adaptation audit batch %q references missing chapter %q", batch.ID, chapterID)
				}
				batchVersions = append(batchVersions, version)
			}
			expectation := compositeAuditExpectation("parent_batch", batchVersions)
			expectation.ScopeID = batch.ID
			expectations = append(expectations, expectation)
		}
		for _, volume := range publishable.Volumes {
			for chapterNumber := volume.TargetFrom; chapterNumber <= volume.TargetTo; chapterNumber++ {
				chapter, ok := adaptationChapterByNumber(publishable.Chapters, chapterNumber)
				if ok {
					byVolume[volume.ID] = append(byVolume[volume.ID], byChapter[chapter.ID])
				}
			}
		}
		volumeIDs := make([]string, 0, len(byVolume))
		for volumeID := range byVolume {
			volumeIDs = append(volumeIDs, volumeID)
		}
		slices.Sort(volumeIDs)
		for _, volumeID := range volumeIDs {
			expectation := compositeAuditExpectation("volume", byVolume[volumeID])
			expectation.ScopeID = volumeID
			expectations = append(expectations, expectation)
		}
		globalPayload, _ := json.Marshal(publishable)
		expectations = append(expectations, RevisionAuditExpectation{Scope: "global", ScopeID: "whole-book", FromChapter: 1, ToChapter: len(publishable.Chapters), ContentSignature: JSONContentSignature(globalPayload)})
	case AdaptationApprovalProse:
		intents := byKind[AdaptationRevisionArtifactProseReworkIntent]
		queues := byKind[AdaptationRevisionArtifactProseReworkQueue]
		if len(intents) == 0 || len(queues) != 1 {
			return nil, fmt.Errorf("adaptation prose stage requires exact intents and one queue")
		}
		for _, version := range intents {
			var intent AdaptationProseReworkIntent
			if err := decodeAdaptationStrict(version.Payload, &intent); err != nil {
				return nil, err
			}
			expectations = append(expectations, RevisionAuditExpectation{
				Scope: "adaptation_rework_intent", ScopeID: intent.ChapterID, FromChapter: intent.CurrentNumber,
				ToChapter: intent.CurrentNumber, ContentSignature: version.ContentSignature,
			})
		}
		expectations = append(expectations, RevisionAuditExpectation{Scope: "adaptation_rework_queue", ScopeID: queues[0].ArtifactID, ContentSignature: queues[0].ContentSignature})
	default:
		return nil, fmt.Errorf("cannot derive audits for adaptation revision stage %q", stage)
	}
	return expectations, nil
}

func (p AdaptationRevisionPolicy) adaptationPublishableAuditPlan(byKind map[string][]ArtifactVersion, details []ArtifactVersion) (AdaptationPlan, error) {
	base := p.BasePlan
	if snapshots := byKind[AdaptationRevisionArtifactPlanSnapshot]; len(snapshots) == 1 {
		candidate, err := p.decodePlanCandidate(snapshots[0].Payload)
		if err != nil {
			return AdaptationPlan{}, err
		}
		base = &candidate
	}
	if base == nil {
		return AdaptationPlan{}, fmt.Errorf("adaptation audit requires the accepted structure snapshot")
	}
	return mergeAdaptationDetailedOutlines(*base, details)
}

func (p AdaptationRevisionPolicy) Route(session RevisionSession) (*RevisionRoute, error) {
	switch session.Stage {
	case RevisionStageCandidateGenerating:
		stage, err := adaptationCurrentApprovalStage(session, p)
		if err != nil {
			return nil, err
		}
		task := "generate only affected adaptation details in bounded BatchPlan work using batch-local source segments and event contracts"
		if stage == AdaptationApprovalStructure {
			task = "revise adaptation volume structure and source coverage in bounded BatchPlan work; wait for structure approval before details"
		} else if stage == AdaptationApprovalProse {
			task = "prepare only exact adaptation prose rework intents; do not generate prose"
		}
		return &RevisionRoute{Agent: "architect_long", Task: task, Reason: "source-contract-bound adaptation revision"}, nil
	case RevisionStageCandidateAudit:
		stage, err := adaptationCurrentApprovalStage(session, p)
		if err != nil {
			return nil, err
		}
		task := "run deterministic and independent semantic audits, then parent-batch, volume, and global signature audits"
		if stage == AdaptationApprovalStructure {
			task = "audit changed adaptation structure locally, by bounded parent batch, and globally against immutable source coverage"
		} else if stage == AdaptationApprovalProse {
			task = "audit exact adaptation prose rework intents and queue signatures"
		}
		return &RevisionRoute{Agent: "editor", Task: task, Reason: "signature-bound adaptation audit"}, nil
	default:
		return nil, nil
	}
}

func ValidateAdaptationRevisionBatchPlan(plan BatchPlan) error {
	if len(plan.Batches) == 0 {
		return fmt.Errorf("adaptation revision BatchPlan requires at least one batch")
	}
	seenBatches := make(map[string]struct{}, len(plan.Batches))
	seenChapters := make(map[string]struct{})
	volumeIDs := make(map[string]struct{})
	for index, batch := range plan.Batches {
		if strings.TrimSpace(batch.ID) == "" || batch.Index != index+1 || len(batch.ChapterIDs) == 0 || len(batch.ChapterIDs) > 4 || strings.TrimSpace(batch.VolumeID) == "" || strings.TrimSpace(batch.ArcID) == "" {
			return fmt.Errorf("adaptation revision batch %d has invalid or unbounded identity", index+1)
		}
		if batch.Constrained && len(batch.ChapterIDs) > 2 {
			return fmt.Errorf("constrained adaptation batch %q must shrink to at most two chapters", batch.ID)
		}
		if batch.Status != BatchStatusPending || batch.EstimatedOutputWords <= 0 || batch.ContextUnits < 0 {
			return fmt.Errorf("adaptation revision batch %q must be a startable pending batch", batch.ID)
		}
		if _, duplicate := seenBatches[batch.ID]; duplicate {
			return fmt.Errorf("adaptation revision batch %q is duplicated", batch.ID)
		}
		seenBatches[batch.ID] = struct{}{}
		volumeIDs[batch.VolumeID] = struct{}{}
		for _, chapterID := range batch.ChapterIDs {
			chapterID = strings.TrimSpace(chapterID)
			if chapterID == "" {
				return fmt.Errorf("adaptation revision batch %q contains an empty target chapter ID", batch.ID)
			}
			if _, duplicate := seenChapters[chapterID]; duplicate {
				return fmt.Errorf("adaptation target chapter %q appears in multiple batches", chapterID)
			}
			seenChapters[chapterID] = struct{}{}
		}
		seenContext := make(map[string]struct{}, len(batch.Context))
		units := 0
		for _, item := range batch.Context {
			if strings.TrimSpace(item.ID) == "" || item.Units <= 0 || !item.Necessary {
				return fmt.Errorf("adaptation batch %q contains unnecessary or unbounded context", batch.ID)
			}
			key := string(item.Kind) + "\x00" + item.ID
			if _, duplicate := seenContext[key]; duplicate {
				return fmt.Errorf("adaptation batch %q duplicates context %q", batch.ID, item.ID)
			}
			contextID := strings.ToLower(strings.TrimSpace(item.ID))
			if item.Kind == BatchContextSourceAnchor && (strings.Contains(contextID, "whole") || strings.Contains(contextID, "entire") || strings.Contains(contextID, "all-source") || strings.Contains(contextID, "all_source") || strings.Contains(contextID, "全书") || strings.Contains(contextID, "整部")) {
				return fmt.Errorf("adaptation batch %q attempts to load the whole source instead of batch-local anchors", batch.ID)
			}
			seenContext[key] = struct{}{}
			units += item.Units
		}
		if batch.ContextUnits != 0 && batch.ContextUnits != units {
			return fmt.Errorf("adaptation batch %q context unit total drifted", batch.ID)
		}
		if units > AdaptationRevisionBatchContextMaxUnits {
			return fmt.Errorf("adaptation batch %q exceeds the immutable-source context ceiling: %d > %d", batch.ID, units, AdaptationRevisionBatchContextMaxUnits)
		}
	}
	if len(plan.VolumeReviews) != len(volumeIDs) || plan.WholeBookReview.Status != BatchReviewPending || strings.TrimSpace(plan.WholeBookReview.ScopeID) == "" {
		return fmt.Errorf("adaptation revision BatchPlan requires exact pending volume and global reviews")
	}
	for _, review := range plan.VolumeReviews {
		if review.Status != BatchReviewPending {
			return fmt.Errorf("adaptation volume review %q is not pending", review.ScopeID)
		}
		if _, ok := volumeIDs[review.ScopeID]; !ok {
			return fmt.Errorf("adaptation BatchPlan has fictional volume review %q", review.ScopeID)
		}
		delete(volumeIDs, review.ScopeID)
	}
	return nil
}

// ValidateAdaptationRevisionPlan validates a complete candidate against the
// immutable imported source and the currently accepted adaptation contract.
func ValidateAdaptationRevisionPlan(base AdaptationPlan, candidate AdaptationPlan, manifest *AdaptationSourceManifest) error {
	if granularity, ok := StrictAdaptationGranularity(candidate.Granularity); !ok {
		return fmt.Errorf("adaptation revision has invalid granularity %q", candidate.Granularity)
	} else if candidate.RewritePolicy != AdaptationRewritePolicyForGranularity(granularity) {
		return fmt.Errorf("adaptation revision granularity %q requires rewrite policy %q", granularity, AdaptationRewritePolicyForGranularity(granularity))
	}
	if base.Granularity != "" && NormalizeAdaptationGranularity(base.Granularity) != NormalizeAdaptationGranularity(candidate.Granularity) {
		return fmt.Errorf("adaptation revision cannot change granularity without a new contract")
	}
	if err := ValidateAdaptationRules(candidate.Rules); err != nil {
		return err
	}
	if candidate.TargetTotalRunes <= 0 || candidate.TargetMinRunes <= 0 || candidate.TargetMaxRunes < candidate.TargetMinRunes || candidate.TargetTotalRunes < candidate.TargetMinRunes || candidate.TargetTotalRunes > candidate.TargetMaxRunes {
		return fmt.Errorf("adaptation revision has an invalid total word contract")
	}
	if candidate.RewritePolicy == AdaptationRewritePreserveDetails && (candidate.SourceTotalRunes <= 0 || candidate.WordTolerance <= 0 || candidate.WordTolerance > 1) {
		return fmt.Errorf("preserve-details adaptation requires source total runes and a bounded word tolerance")
	}
	if err := validateAdaptationSourceContractImmutable(base, candidate); err != nil {
		return err
	}
	if err := validateAdaptationStableTargets(base, candidate); err != nil {
		return err
	}
	if err := validateAdaptationSourceCoverage(base, candidate, manifest); err != nil {
		return err
	}
	if err := validateAdaptationEventContract(base, candidate); err != nil {
		return err
	}
	if err := validateAdaptationRelationshipAndSettings(candidate); err != nil {
		return err
	}
	return validateAdaptationChapterRevisionContracts(base, candidate)
}

// ValidateAdaptationRevisionPreviewCandidate validates the complete candidate
// persisted in a service receipt against its durable base and source manifest.
// Stage-specific structure skeletons are validated later when they are
// submitted; a preview may still contain the currently approved detail facts.
func ValidateAdaptationRevisionPreviewCandidate(
	base AdaptationPlan,
	candidate AdaptationPlan,
	manifest *AdaptationSourceManifest,
	impact RevisionImpact,
) error {
	if err := impact.Validate(); err != nil {
		return fmt.Errorf("adaptation revision preview impact is invalid: %w", err)
	}
	if len(candidate.Chapters) == 0 || len(candidate.Volumes) == 0 {
		return fmt.Errorf("adaptation revision preview candidate requires chapters and volumes")
	}
	if err := ValidateAdaptationRevisionPlan(base, candidate, manifest); err != nil {
		return err
	}
	baseByID := make(map[string]AdaptationChapterPlan, len(base.Chapters))
	for _, chapter := range base.Chapters {
		baseByID[chapter.ID] = chapter
	}
	for _, chapter := range candidate.Chapters {
		if err := validateAdaptationChapterWordBudget(candidate, chapter); err != nil {
			return err
		}
		for label, values := range map[string][]string{
			"preserve_events":  chapter.PreserveEvents,
			"required_changes": chapter.RequiredChanges,
			"forbidden_moves":  chapter.ForbiddenMoves,
		} {
			if err := validateAdaptationContractStrings(chapter.ID, label, values); err != nil {
				return err
			}
		}
		prior, existed := baseByID[chapter.ID]
		changed := !existed || adaptationJSONSignature(prior) != adaptationJSONSignature(chapter)
		if adaptationChapterHasSourceLineage(chapter) && len(nonBlankAdaptationStrings(chapter.PreserveEvents)) == 0 {
			return fmt.Errorf("source-backed preview chapter %q requires preserve_events", chapter.ID)
		}
		if changed && len(nonBlankAdaptationStrings(chapter.RequiredChanges)) == 0 {
			return fmt.Errorf("changed preview chapter %q requires required_changes", chapter.ID)
		}
		if changed && len(nonBlankAdaptationStrings(chapter.ForbiddenMoves)) == 0 {
			return fmt.Errorf("changed preview chapter %q requires forbidden_moves", chapter.ID)
		}
	}
	return nil
}

// AdaptationPlanFromVolumeReview reconstructs the immutable proposal-complete
// contract from durable review, manifest, and source-report facts. Keeping the
// conversion in domain lets both the service and receipt recovery verify the
// same base without trusting request data.
func AdaptationPlanFromVolumeReview(
	review AdaptationVolumeReview,
	manifest AdaptationSourceManifest,
	reports []AdaptationSourceReport,
) (*AdaptationPlan, error) {
	seenEvents := make(map[string]struct{})
	events := make([]AdaptationEvent, 0)
	for _, report := range reports {
		for _, event := range report.SourceEvents {
			id := strings.TrimSpace(event.ID)
			if id == "" {
				return nil, fmt.Errorf("source report chapter %d contains an event without stable identity", report.Chapter)
			}
			if _, exists := seenEvents[id]; exists {
				return nil, fmt.Errorf("source event %q has duplicate persisted ownership", id)
			}
			seenEvents[id] = struct{}{}
			events = append(events, event)
		}
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("persisted source event ledger is required at proposal-complete stage")
	}
	sourceRunes := 0
	for _, chapter := range manifest.Chapters {
		sourceRunes += chapter.Runes
	}
	return &AdaptationPlan{
		Granularity:       review.Granularity,
		ModePolicy:        AdaptationModePolicyForGranularity(review.Granularity),
		Status:            AdaptationPlanStatusVolumeReview,
		RewritePolicy:     review.RewritePolicy,
		Brief:             review.Brief,
		Volumes:           append([]AdaptationVolumePlan(nil), review.Volumes...),
		WordTolerance:     review.WordTolerance,
		SourceTotalRunes:  sourceRunes,
		MainlineRules:     append([]string(nil), review.MainlineRules...),
		RelationshipGoals: append([]string(nil), review.RelationshipGoals...),
		Rules:             CompileAdaptationRules(review.Brief, review.Granularity),
		SourceEvents:      events,
		Chapters:          []AdaptationChapterPlan{},
	}, nil
}

func (p AdaptationRevisionPolicy) validatePlan(candidate AdaptationPlan) error {
	base := AdaptationPlan{}
	if p.BasePlan != nil {
		base = *p.BasePlan
	}
	return ValidateAdaptationRevisionPlan(base, candidate, p.SourceManifest)
}

func (p AdaptationRevisionPolicy) decodePlanCandidate(payload json.RawMessage) (AdaptationPlan, error) {
	var envelope AdaptationPlanRevisionCandidate
	if !hasJSONField(payload, "plan") {
		return AdaptationPlan{}, fmt.Errorf("adaptation plan snapshot requires the strict stage/source-signature envelope")
	}
	if err := decodeAdaptationStrict(payload, &envelope); err != nil {
		return AdaptationPlan{}, fmt.Errorf("decode adaptation plan candidate: %w", err)
	}
	if envelope.Stage == "" || p.Stage == "" || envelope.Stage != p.Stage {
		return AdaptationPlan{}, fmt.Errorf("adaptation plan candidate manuscript stage drift: %s != %s", envelope.Stage, p.Stage)
	}
	if p.SourceManifest == nil || strings.TrimSpace(envelope.SourceSignature) == "" || envelope.SourceSignature != AdaptationSourceManifestContractSignature(*p.SourceManifest) {
		return AdaptationPlan{}, fmt.Errorf("adaptation plan candidate immutable source signature mismatch")
	}
	return envelope.Plan, nil
}

func (p AdaptationRevisionPolicy) mergeDetailedCandidates(versions []ArtifactVersion) (AdaptationPlan, error) {
	if p.BasePlan == nil {
		return AdaptationPlan{}, fmt.Errorf("adaptation detailed-outline revision requires the accepted structure plan")
	}
	return mergeAdaptationDetailedOutlines(*p.BasePlan, versions)
}

func mergeAdaptationDetailedOutlines(base AdaptationPlan, versions []ArtifactVersion) (AdaptationPlan, error) {
	plan := base
	plan.Chapters = append([]AdaptationChapterPlan(nil), base.Chapters...)
	indexByID := make(map[string]int, len(plan.Chapters))
	for index, chapter := range plan.Chapters {
		indexByID[strings.TrimSpace(chapter.ID)] = index
	}
	for _, version := range versions {
		detail, err := decodeAdaptationDetailedOutline(version)
		if err != nil {
			return AdaptationPlan{}, err
		}
		index, exists := indexByID[detail.ChapterID]
		if !exists {
			return AdaptationPlan{}, fmt.Errorf("adaptation detailed outline %q is absent from the accepted structure", detail.ChapterID)
		}
		sealed := plan.Chapters[index]
		if adaptationChapterStructureSignature(sealed) != adaptationChapterStructureSignature(detail.Outline) {
			return AdaptationPlan{}, fmt.Errorf("adaptation detailed outline %q changes the accepted structure/source ownership", detail.ChapterID)
		}
		volumeID := adaptationChapterVolumeID(plan, sealed.Chapter)
		if detail.VolumeID != volumeID || detail.ArcID != volumeID+":revision-arc" {
			return AdaptationPlan{}, fmt.Errorf("adaptation detailed outline %q changes accepted volume/arc ownership", detail.ChapterID)
		}
		plan.Chapters[index] = overlayAdaptationNarrativeDetail(sealed, detail.Outline)
	}
	return plan, nil
}

func overlayAdaptationNarrativeDetail(sealed, detail AdaptationChapterPlan) AdaptationChapterPlan {
	sealed.Title = detail.Title
	sealed.OutlineEntry.Title = detail.OutlineEntry.Title
	sealed.OutlineEntry.CoreEvent = detail.OutlineEntry.CoreEvent
	sealed.OutlineEntry.Hook = detail.OutlineEntry.Hook
	sealed.OutlineEntry.Scenes = append([]string(nil), detail.OutlineEntry.Scenes...)
	return sealed
}

func adaptationChapterStructureSignature(chapter AdaptationChapterPlan) string {
	chapter.Title = ""
	chapter.OutlineEntry.Title = ""
	chapter.OutlineEntry.CoreEvent = ""
	chapter.OutlineEntry.Hook = ""
	chapter.OutlineEntry.Scenes = nil
	return adaptationJSONSignature(chapter)
}

func adaptationChapterVolumeID(plan AdaptationPlan, chapter int) string {
	for _, volume := range plan.Volumes {
		if volume.TargetFrom <= chapter && chapter <= volume.TargetTo {
			return volume.ID
		}
	}
	return "unassigned-volume"
}

func decodeAdaptationDetailedOutline(version ArtifactVersion) (AdaptationDetailedOutlineCandidate, error) {
	var detail AdaptationDetailedOutlineCandidate
	if err := decodeAdaptationStrict(version.Payload, &detail); err != nil {
		return detail, fmt.Errorf("decode adaptation detailed outline %q: %w", version.ArtifactID, err)
	}
	if strings.TrimSpace(detail.ChapterID) != version.ArtifactID || detail.CurrentNumber <= 0 || strings.TrimSpace(detail.VolumeID) == "" || strings.TrimSpace(detail.ArcID) == "" || detail.Outline.ID != detail.ChapterID || detail.Outline.Chapter != detail.CurrentNumber || strings.TrimSpace(detail.Outline.Title) == "" || strings.TrimSpace(detail.Outline.CoreEvent) == "" || strings.TrimSpace(detail.Outline.Hook) == "" || len(detail.Outline.Scenes) == 0 {
		return detail, fmt.Errorf("adaptation detailed outline %q is incomplete or identity-drifted", version.ArtifactID)
	}
	return detail, nil
}

func validateAdaptationProseQueue(byKind map[string][]ArtifactVersion) error {
	intents := byKind[AdaptationRevisionArtifactProseReworkIntent]
	queues := byKind[AdaptationRevisionArtifactProseReworkQueue]
	if len(queues) != 1 || len(intents) == 0 {
		return fmt.Errorf("adaptation prose stage requires exact rework intents and one queue")
	}
	var queue AdaptationProseReworkQueue
	if err := decodeAdaptationStrict(queues[0].Payload, &queue); err != nil {
		return err
	}
	want := make(map[string]struct{}, len(intents))
	for _, version := range intents {
		var intent AdaptationProseReworkIntent
		if err := decodeAdaptationStrict(version.Payload, &intent); err != nil {
			return err
		}
		want[intent.ChapterID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(queue.ChapterIDs))
	for _, chapterID := range queue.ChapterIDs {
		if _, ok := want[chapterID]; !ok {
			return fmt.Errorf("adaptation prose queue contains untargeted chapter %q", chapterID)
		}
		if _, duplicate := seen[chapterID]; duplicate {
			return fmt.Errorf("adaptation prose queue duplicates chapter %q", chapterID)
		}
		seen[chapterID] = struct{}{}
	}
	if len(seen) != len(want) {
		return fmt.Errorf("adaptation prose queue does not exactly cover rework intents")
	}
	return nil
}

func validateAdaptationStableTargets(base, candidate AdaptationPlan) error {
	baseChapters := make(map[string]AdaptationChapterPlan, len(base.Chapters))
	for _, chapter := range base.Chapters {
		if id := strings.TrimSpace(chapter.ID); id != "" {
			baseChapters[id] = chapter
		}
	}
	seen := make(map[string]struct{}, len(candidate.Chapters))
	for index, chapter := range candidate.Chapters {
		id := strings.TrimSpace(chapter.ID)
		if id == "" {
			return fmt.Errorf("adaptation target chapter %d requires a stable ID", index+1)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("adaptation target chapter stable ID %q is duplicated", id)
		}
		seen[id] = struct{}{}
		if chapter.Chapter != index+1 {
			return fmt.Errorf("adaptation target %q display chapter=%d, want current order %d", id, chapter.Chapter, index+1)
		}
		prior, existing := baseChapters[id]
		if existing && prior.IsAdded != chapter.IsAdded {
			return fmt.Errorf("adaptation target chapter %q cannot change immutable IsAdded ownership", id)
		}
		if chapter.IsAdded {
			if adaptationChapterHasSourceLineage(chapter) {
				return fmt.Errorf("added target chapter %q cannot own source anchors, segments, runes, or source events", id)
			}
		}
		if !existing {
			hasSource := adaptationChapterHasSourceLineage(chapter)
			if chapter.IsAdded {
				if hasSource {
					return fmt.Errorf("added target chapter %q must remain independent from immutable source coverage", id)
				}
				if strings.TrimSpace(chapter.CoverageNote) == "" || len(nonBlankAdaptationStrings(chapter.AddedEventIDs)) == 0 {
					return fmt.Errorf("added target chapter %q requires IsAdded explanation and added event ownership", id)
				}
			} else if !hasSource {
				return fmt.Errorf("new target chapter %q must receive reassigned source coverage or declare IsAdded", id)
			} else if strings.TrimSpace(chapter.CoverageNote) == "" {
				return fmt.Errorf("new source-backed target chapter %q must explain its source coverage reassignment", id)
			}
		}
	}
	for id := range baseChapters {
		if _, exists := seen[id]; !exists {
			return fmt.Errorf("adaptation revision cannot delete or merge existing target chapter %q", id)
		}
	}
	baseVolumes := make(map[string]AdaptationVolumePlan, len(base.Volumes))
	for _, volume := range base.Volumes {
		if id := strings.TrimSpace(volume.ID); id != "" {
			baseVolumes[id] = volume
		}
	}
	seenVolumes := make(map[string]struct{}, len(candidate.Volumes))
	expectedTargetFrom := 1
	for index, volume := range candidate.Volumes {
		id := strings.TrimSpace(volume.ID)
		if id == "" || volume.Index != index+1 || volume.TargetFrom != expectedTargetFrom || volume.TargetTo < volume.TargetFrom || volume.TargetTo > len(candidate.Chapters) {
			return fmt.Errorf("adaptation volume %d has invalid stable identity or target range", index+1)
		}
		if _, duplicate := seenVolumes[id]; duplicate {
			return fmt.Errorf("adaptation volume stable ID %q is duplicated", id)
		}
		if prior, existed := baseVolumes[id]; existed && (prior.SourceFrom != volume.SourceFrom || prior.SourceTo != volume.SourceTo || adaptationJSONSignature(prior.MainlineEventIDs) != adaptationJSONSignature(volume.MainlineEventIDs)) {
			return &AdaptationContractChangeRequiredError{EventIDs: append([]string(nil), prior.MainlineEventIDs...), Reason: fmt.Sprintf("protected source/mainline ownership changed for volume %q", id)}
		}
		seenVolumes[id] = struct{}{}
		expectedTargetFrom = volume.TargetTo + 1
	}
	if len(candidate.Volumes) > 0 && expectedTargetFrom != len(candidate.Chapters)+1 {
		return fmt.Errorf("adaptation volume ranges do not continuously cover every target chapter")
	}
	for id := range baseVolumes {
		if _, exists := seenVolumes[id]; !exists {
			return fmt.Errorf("adaptation revision cannot drop existing volume %q", id)
		}
	}
	return nil
}

func validateAdaptationStructureSkeleton(candidate AdaptationPlan, impact RevisionImpact) error {
	affected := make(map[string]struct{})
	for _, item := range impact.Items {
		if item.Requirement == StructureImpactRequired && item.ArtifactKind == StructureKindChapter {
			affected[item.ArtifactID] = struct{}{}
		}
	}
	for _, chapter := range candidate.Chapters {
		if _, ok := affected[chapter.ID]; !ok {
			continue
		}
		if strings.TrimSpace(chapter.Title) != "" || strings.TrimSpace(chapter.CoreEvent) != "" || strings.TrimSpace(chapter.Hook) != "" || len(chapter.Scenes) != 0 {
			return fmt.Errorf("adaptation structure candidate %q contains detailed outline content before structure approval", chapter.ID)
		}
	}
	return nil
}

func validateAdaptationStructuralArtifacts(candidate AdaptationPlan, byKind map[string][]ArtifactVersion) error {
	volumes := make(map[string]AdaptationVolumePlan, len(candidate.Volumes))
	for _, volume := range candidate.Volumes {
		volumes[volume.ID] = volume
	}
	for _, version := range byKind[StructureKindVolume] {
		var volume AdaptationVolumePlan
		if err := decodeAdaptationStrict(version.Payload, &volume); err != nil {
			return fmt.Errorf("decode adaptation volume candidate %q: %w", version.ArtifactID, err)
		}
		expected, exists := volumes[version.ArtifactID]
		if !exists || volume.ID != version.ArtifactID || adaptationJSONSignature(volume) != adaptationJSONSignature(expected) {
			return fmt.Errorf("adaptation volume candidate %q does not match the signed plan snapshot", version.ArtifactID)
		}
	}
	for _, version := range byKind[StructureKindArc] {
		var arc AdaptationRevisionArcCandidate
		if err := decodeAdaptationStrict(version.Payload, &arc); err != nil {
			return fmt.Errorf("decode adaptation arc candidate %q: %w", version.ArtifactID, err)
		}
		if arc.ID != version.ArtifactID || strings.TrimSpace(arc.VolumeID) == "" || arc.TargetFrom <= 0 || arc.TargetTo < arc.TargetFrom {
			return fmt.Errorf("adaptation arc candidate %q has invalid stable identity or target range", version.ArtifactID)
		}
		volume, exists := volumes[arc.VolumeID]
		if !exists || arc.TargetFrom < volume.TargetFrom || arc.TargetTo > volume.TargetTo {
			return fmt.Errorf("adaptation arc candidate %q lies outside volume %q", arc.ID, arc.VolumeID)
		}
	}
	return nil
}

func validateAdaptationSourceContractImmutable(base, candidate AdaptationPlan) error {
	for label, left := range map[string]any{
		"mainline rules":       base.MainlineRules,
		"adaptation rules":     base.Rules,
		"mode policy":          base.ModePolicy,
		"relationship goals":   base.RelationshipGoals,
		"target relationships": base.TargetRelationshipStates,
		"target setting locks": base.TargetSettingLocks,
	} {
		var right any
		switch label {
		case "mainline rules":
			right = candidate.MainlineRules
		case "adaptation rules":
			right = candidate.Rules
		case "mode policy":
			right = candidate.ModePolicy
		case "relationship goals":
			right = candidate.RelationshipGoals
		case "target relationships":
			right = candidate.TargetRelationshipStates
		case "target setting locks":
			right = candidate.TargetSettingLocks
		}
		if adaptationJSONSignature(left) != adaptationJSONSignature(right) {
			return &AdaptationContractChangeRequiredError{Reason: "protected " + label + " changed"}
		}
	}
	if len(base.SourceEvents) > 0 && adaptationJSONSignature(base.SourceEvents) != adaptationJSONSignature(candidate.SourceEvents) {
		return &AdaptationContractChangeRequiredError{EventIDs: changedProtectedEventIDs(base.SourceEvents, candidate.SourceEvents), Reason: "immutable source event ledger changed"}
	}
	baseSegments := collectAdaptationSegments(base.Chapters)
	if len(baseSegments) > 0 {
		candidateSegments := collectAdaptationSegments(candidate.Chapters)
		if adaptationJSONSignature(baseSegments) != adaptationJSONSignature(candidateSegments) {
			return fmt.Errorf("immutable adaptation SourceSegments changed; reassign complete existing segments instead")
		}
	}
	return nil
}

func collectAdaptationSegments(chapters []AdaptationChapterPlan) []AdaptationSourceSegment {
	var segments []AdaptationSourceSegment
	for _, chapter := range chapters {
		segments = append(segments, chapter.SourceSegments...)
	}
	sort.Slice(segments, func(left, right int) bool {
		if segments[left].SourceChapter != segments[right].SourceChapter {
			return segments[left].SourceChapter < segments[right].SourceChapter
		}
		return segments[left].Sequence < segments[right].Sequence
	})
	return segments
}

func validateAdaptationSourceCoverage(base, candidate AdaptationPlan, manifest *AdaptationSourceManifest) error {
	knownRunes := make(map[int]int)
	if manifest != nil {
		for _, source := range manifest.Chapters {
			if source.Chapter <= 0 || source.Runes <= 0 {
				return fmt.Errorf("immutable source manifest contains invalid chapter metadata")
			}
			knownRunes[source.Chapter] = source.Runes
		}
	}
	if len(knownRunes) == 0 {
		for _, chapter := range base.Chapters {
			for _, source := range chapter.SourceChapters {
				knownRunes[source] = max(knownRunes[source], chapter.SourceRunes)
			}
			for _, segment := range chapter.SourceSegments {
				knownRunes[segment.SourceChapter] = max(knownRunes[segment.SourceChapter], segment.RuneShare.End)
			}
		}
		for _, event := range base.SourceEvents {
			if event.SourceChapter > 0 {
				if _, ok := knownRunes[event.SourceChapter]; !ok {
					knownRunes[event.SourceChapter] = 0
				}
			}
		}
	}
	covered := make(map[int]bool)
	segments := make(map[int][]AdaptationSourceSegment)
	for _, chapter := range candidate.Chapters {
		for _, source := range chapter.SourceChapters {
			if source <= 0 {
				return fmt.Errorf("target chapter %q contains invalid source chapter %d", chapter.ID, source)
			}
			if len(knownRunes) > 0 {
				if _, exists := knownRunes[source]; !exists {
					return fmt.Errorf("target chapter %q references unknown immutable source chapter %d", chapter.ID, source)
				}
			}
			covered[source] = true
		}
		if chapter.SourceRange.From > 0 || chapter.SourceRange.To > 0 {
			if chapter.SourceRange.From <= 0 || chapter.SourceRange.To < chapter.SourceRange.From {
				return fmt.Errorf("target chapter %q has invalid source range", chapter.ID)
			}
			for source := chapter.SourceRange.From; source <= chapter.SourceRange.To; source++ {
				if len(knownRunes) > 0 {
					if _, exists := knownRunes[source]; !exists {
						return fmt.Errorf("target chapter %q source range references unknown chapter %d", chapter.ID, source)
					}
				}
				covered[source] = true
			}
		}
		for _, segment := range chapter.SourceSegments {
			segments[segment.SourceChapter] = append(segments[segment.SourceChapter], segment)
			covered[segment.SourceChapter] = true
		}
	}
	for source := range knownRunes {
		if !covered[source] {
			return fmt.Errorf("adaptation revision omits immutable source chapter %d", source)
		}
	}
	if NormalizeAdaptationGranularity(candidate.Granularity) == AdaptationGranularityChapter {
		for source, sourceRunes := range knownRunes {
			if sourceRunes <= 0 || len(segments[source]) == 0 {
				continue
			}
			sort.Slice(segments[source], func(left, right int) bool { return segments[source][left].Sequence < segments[source][right].Sequence })
			if err := ValidateAdaptationSourceSegments(source, sourceRunes, segments[source]); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateAdaptationEventContract(base, candidate AdaptationPlan) error {
	baseLedger := make(map[string]AdaptationEvent, len(base.TargetEventLedger))
	for _, event := range base.TargetEventLedger {
		baseLedger[strings.TrimSpace(event.ID)] = event
	}
	candidateLedger := make(map[string]AdaptationEvent, len(candidate.TargetEventLedger))
	for _, event := range candidate.TargetEventLedger {
		eventID := strings.TrimSpace(event.ID)
		if _, existed := baseLedger[eventID]; !existed && event.Origin != AdaptationEventOriginAdded {
			return fmt.Errorf("target event %q must retain added-event origin", eventID)
		}
		candidateLedger[eventID] = event
	}
	for eventID, event := range baseLedger {
		current, exists := candidateLedger[eventID]
		if !exists {
			return fmt.Errorf("existing target event %q cannot be deleted", eventID)
		}
		if adaptationJSONSignature(event) != adaptationJSONSignature(current) {
			return fmt.Errorf("existing target event %q is immutable", eventID)
		}
	}

	known := make(map[string]AdaptationEvent)
	for _, event := range append(append([]AdaptationEvent(nil), candidate.SourceEvents...), candidate.TargetEventLedger...) {
		id := strings.TrimSpace(event.ID)
		if id == "" {
			return fmt.Errorf("adaptation event ledger contains a blank event ID")
		}
		if _, duplicate := known[id]; duplicate {
			return fmt.Errorf("adaptation event ledger duplicates event %q", id)
		}
		known[id] = event
	}
	owners := make(map[string][]int)
	for _, chapter := range candidate.Chapters {
		seen := make(map[string]struct{})
		for _, eventID := range chapter.EventIDs {
			eventID = strings.TrimSpace(eventID)
			if eventID == "" {
				continue
			}
			if _, exists := known[eventID]; !exists {
				return fmt.Errorf("target chapter %q owns unknown event %q", chapter.ID, eventID)
			}
			if _, duplicate := seen[eventID]; duplicate {
				return fmt.Errorf("target chapter %q duplicates event %q", chapter.ID, eventID)
			}
			seen[eventID] = struct{}{}
			owners[eventID] = append(owners[eventID], chapter.Chapter)
		}
		for _, eventID := range chapter.AddedEventIDs {
			eventID = strings.TrimSpace(eventID)
			if eventID == "" {
				continue
			}
			event, exists := known[eventID]
			if !exists {
				return fmt.Errorf("target chapter %q owns unknown added event %q", chapter.ID, eventID)
			}
			if event.Origin == AdaptationEventOriginSource {
				return fmt.Errorf("target chapter %q cannot relabel source event %q as added", chapter.ID, eventID)
			}
			if _, duplicate := seen[eventID]; duplicate {
				return fmt.Errorf("target chapter %q duplicates event %q", chapter.ID, eventID)
			}
			seen[eventID] = struct{}{}
			owners[eventID] = append(owners[eventID], chapter.Chapter)
		}
	}
	baseOwners := adaptationStableEventOwners(base.Chapters)
	candidateOwners := adaptationStableEventOwners(candidate.Chapters)
	for eventID := range baseLedger {
		baseOwner, baseOK := baseOwners[eventID]
		candidateOwner, candidateOK := candidateOwners[eventID]
		if !baseOK || !candidateOK || baseOwner != candidateOwner {
			return fmt.Errorf("existing target event %q ownership changed from %s/%s to %s/%s", eventID, baseOwner.ChapterID, baseOwner.Category, candidateOwner.ChapterID, candidateOwner.Category)
		}
	}
	for eventID, event := range known {
		if len(owners[eventID]) == 0 {
			if event.Origin == AdaptationEventOriginSource && event.Importance == AdaptationEventMainline {
				return &AdaptationContractChangeRequiredError{EventIDs: []string{eventID}, Reason: "protected source mainline event lost target ownership"}
			}
			return fmt.Errorf("adaptation event %q has no target owner", eventID)
		}
		if len(owners[eventID]) > 1 {
			return fmt.Errorf("adaptation event %q has multiple target owners %v", eventID, owners[eventID])
		}
		if len(owners[eventID]) == 1 {
			owner, _ := adaptationChapterByNumber(candidate.Chapters, owners[eventID][0])
			if event.Origin == AdaptationEventOriginSource {
				if owner.IsAdded || event.SourceChapter <= 0 || !adaptationChapterCoversSource(owner, event.SourceChapter) {
					return fmt.Errorf("source event %q is owned outside its source lineage by target %q", eventID, owner.ID)
				}
			}
			for _, dependency := range event.DependsOn {
				dependencyOwner := owners[strings.TrimSpace(dependency)]
				if len(dependencyOwner) != 1 || dependencyOwner[0] >= owners[eventID][0] {
					return fmt.Errorf("adaptation event %q dependency %q is not owned by an earlier target chapter", eventID, dependency)
				}
			}
		}
	}
	for _, chapter := range candidate.Chapters {
		for _, dependency := range chapter.DependsOnEventIDs {
			dependencyOwner := owners[strings.TrimSpace(dependency)]
			if len(dependencyOwner) != 1 || dependencyOwner[0] >= chapter.Chapter {
				return fmt.Errorf("target chapter %q dependency event %q is not established earlier", chapter.ID, dependency)
			}
		}
	}
	return nil
}

type adaptationStableEventOwnership struct {
	ChapterID string
	Category  string
}

func adaptationStableEventOwners(chapters []AdaptationChapterPlan) map[string]adaptationStableEventOwnership {
	owners := make(map[string]adaptationStableEventOwnership)
	for _, chapter := range chapters {
		for _, eventID := range chapter.EventIDs {
			owners[strings.TrimSpace(eventID)] = adaptationStableEventOwnership{ChapterID: strings.TrimSpace(chapter.ID), Category: "event_ids"}
		}
		for _, eventID := range chapter.AddedEventIDs {
			owners[strings.TrimSpace(eventID)] = adaptationStableEventOwnership{ChapterID: strings.TrimSpace(chapter.ID), Category: "added_event_ids"}
		}
	}
	return owners
}

func validateAdaptationRelationshipAndSettings(candidate AdaptationPlan) error {
	type transition struct {
		chapter int
		eventID string
		value   AdaptationRelationshipTransition
	}
	var transitions []transition
	owners := make(map[string]int)
	for _, chapter := range candidate.Chapters {
		for _, eventID := range append(append([]string(nil), chapter.EventIDs...), chapter.AddedEventIDs...) {
			owners[strings.TrimSpace(eventID)] = chapter.Chapter
		}
	}
	for _, chapter := range candidate.Chapters {
		if chapter.Relationship != nil {
			transitions = append(transitions, transition{chapter: chapter.Chapter, eventID: chapter.ID, value: *chapter.Relationship})
		}
	}
	for _, event := range append(append([]AdaptationEvent(nil), candidate.SourceEvents...), candidate.TargetEventLedger...) {
		if event.Relationship == nil {
			continue
		}
		chapter := adaptationEventOwner(candidate.Chapters, event.ID)
		if chapter > 0 {
			transitions = append(transitions, transition{chapter: chapter, eventID: event.ID, value: *event.Relationship})
		}
	}
	sort.SliceStable(transitions, func(left, right int) bool {
		if transitions[left].chapter != transitions[right].chapter {
			return transitions[left].chapter < transitions[right].chapter
		}
		return transitions[left].eventID < transitions[right].eventID
	})
	states := make(map[string]string)
	for _, item := range transitions {
		pair, from, to := strings.TrimSpace(item.value.Pair), strings.TrimSpace(item.value.From), strings.TrimSpace(item.value.To)
		if pair == "" || to == "" {
			return fmt.Errorf("relationship transition %q requires pair and target state", item.eventID)
		}
		if previous, exists := states[pair]; exists && previous != from && !slices.Contains(item.value.AllowedFrom, previous) {
			return fmt.Errorf("relationship %q transition at chapter %d starts from %q after %q", pair, item.chapter, from, previous)
		}
		for _, dependencyID := range item.value.RequiresEventIDs {
			if owner := owners[strings.TrimSpace(dependencyID)]; owner <= 0 || owner >= item.chapter {
				return fmt.Errorf("relationship transition %q requires earlier event %q", item.eventID, dependencyID)
			}
		}
		states[pair] = to
	}
	for pair, expected := range candidate.TargetRelationshipStates {
		if strings.TrimSpace(pair) == "" || strings.TrimSpace(expected) == "" {
			return fmt.Errorf("target relationship states contain a blank pair or state")
		}
		if actual, exists := states[pair]; !exists || actual != expected {
			return fmt.Errorf("relationship %q ends at %q, want %q", pair, actual, expected)
		}
	}
	locks := make(map[string]string)
	for _, lock := range candidate.TargetSettingLocks {
		key, value := strings.TrimSpace(lock.Key), strings.TrimSpace(lock.Value)
		if key == "" || value == "" {
			return fmt.Errorf("target setting lock contains a blank key or value")
		}
		if prior, exists := locks[key]; exists && prior != value {
			return fmt.Errorf("target setting lock %q conflicts", key)
		}
		locks[key] = value
	}
	type settingClaim struct {
		chapter int
		owner   string
		claim   AdaptationSettingClaim
	}
	var orderedClaims []settingClaim
	for _, chapter := range candidate.Chapters {
		for _, claim := range chapter.SettingClaims {
			orderedClaims = append(orderedClaims, settingClaim{chapter: chapter.Chapter, owner: chapter.ID, claim: claim})
		}
	}
	for _, event := range append(append([]AdaptationEvent(nil), candidate.SourceEvents...), candidate.TargetEventLedger...) {
		for _, claim := range event.SettingClaims {
			orderedClaims = append(orderedClaims, settingClaim{chapter: owners[event.ID], owner: event.ID, claim: claim})
		}
	}
	sort.SliceStable(orderedClaims, func(left, right int) bool {
		if orderedClaims[left].chapter != orderedClaims[right].chapter {
			return orderedClaims[left].chapter < orderedClaims[right].chapter
		}
		return orderedClaims[left].owner < orderedClaims[right].owner
	})
	claims := make(map[string]string)
	for _, item := range orderedClaims {
		claim := item.claim
		key, value := strings.TrimSpace(claim.Key), strings.TrimSpace(claim.Value)
		if key == "" || value == "" {
			return fmt.Errorf("adaptation artifact %q contains a blank setting claim", item.owner)
		}
		if locked, exists := locks[key]; exists && locked != value {
			return fmt.Errorf("adaptation artifact %q setting %s=%s violates lock %s", item.owner, key, value, locked)
		}
		if prior, exists := claims[key]; exists && prior != value {
			return fmt.Errorf("target setting %q changes from %q to %q without a declared transition", key, prior, value)
		}
		claims[key] = value
	}
	return nil
}

func validateAdaptationChapterRevisionContracts(base, candidate AdaptationPlan) error {
	baseByID := make(map[string]AdaptationChapterPlan, len(base.Chapters))
	for _, chapter := range base.Chapters {
		baseByID[chapter.ID] = chapter
	}
	for _, chapter := range candidate.Chapters {
		prior, existed := baseByID[chapter.ID]
		changed := !existed || adaptationJSONSignature(prior) != adaptationJSONSignature(chapter)
		if !changed {
			continue
		}
		if existed {
			if adaptationJSONSignature(prior.PreserveEvents) != adaptationJSONSignature(chapter.PreserveEvents) ||
				adaptationJSONSignature(prior.ForbiddenMoves) != adaptationJSONSignature(chapter.ForbiddenMoves) ||
				adaptationJSONSignature(prior.RuleIDs) != adaptationJSONSignature(chapter.RuleIDs) ||
				adaptationJSONSignature(prior.Relationship) != adaptationJSONSignature(chapter.Relationship) ||
				adaptationJSONSignature(prior.SettingClaims) != adaptationJSONSignature(chapter.SettingClaims) {
				return &AdaptationContractChangeRequiredError{Reason: fmt.Sprintf("protected preserve/forbidden/rule/relationship/setting ownership changed for target chapter %q", chapter.ID)}
			}
		}
		if len(nonBlankAdaptationStrings(chapter.RequiredChanges)) == 0 {
			return fmt.Errorf("revised target chapter %q requires required_changes", chapter.ID)
		}
		if len(nonBlankAdaptationStrings(chapter.ForbiddenMoves)) == 0 {
			return fmt.Errorf("revised target chapter %q requires forbidden_moves", chapter.ID)
		}
		if adaptationChapterHasSourceLineage(chapter) && len(nonBlankAdaptationStrings(chapter.PreserveEvents)) == 0 {
			return fmt.Errorf("revised source-backed target chapter %q requires preserve_events", chapter.ID)
		}
		if err := validateAdaptationChapterWordBudget(candidate, chapter); err != nil {
			return err
		}
		for label, values := range map[string][]string{"preserve_events": chapter.PreserveEvents, "required_changes": chapter.RequiredChanges, "forbidden_moves": chapter.ForbiddenMoves} {
			if err := validateAdaptationContractStrings(chapter.ID, label, values); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateAdaptationContractStrings(chapterID, label string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("target chapter %q %s contains a blank contract item", chapterID, label)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("target chapter %q %s duplicates %q", chapterID, label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateAdaptationChapterWordBudget(plan AdaptationPlan, chapter AdaptationChapterPlan) error {
	if chapter.TargetRunes <= 0 || chapter.TargetMinRunes <= 0 || chapter.TargetMaxRunes < chapter.TargetMinRunes || chapter.TargetRunes < chapter.TargetMinRunes || chapter.TargetRunes > chapter.TargetMaxRunes {
		return fmt.Errorf("target chapter %q has an invalid adaptation word contract", chapter.ID)
	}
	if chapter.TargetMaxRunes > AdaptationModelChapterMaxRunes {
		return fmt.Errorf("target chapter %q exceeds the %d-rune model chapter contract", chapter.ID, AdaptationModelChapterMaxRunes)
	}
	if chapter.WordBudget != nil {
		budget := chapter.WordBudget
		if budget.SourceRunes != 0 && budget.SourceRunes != chapter.SourceRunes || budget.TargetRunes != 0 && budget.TargetRunes != chapter.TargetRunes || budget.MinRunes != 0 && budget.MinRunes != chapter.TargetMinRunes || budget.MaxRunes != 0 && budget.MaxRunes != chapter.TargetMaxRunes {
			return fmt.Errorf("target chapter %q nested word budget drifted from the chapter contract", chapter.ID)
		}
	}
	if plan.RewritePolicy == AdaptationRewritePreserveDetails && !chapter.IsAdded && chapter.SourceRunes <= 0 && len(chapter.SourceSegments) == 0 {
		return fmt.Errorf("preserve-details chapter %q requires source rune ownership", chapter.ID)
	}
	return nil
}

func validateAdaptationBatchCoverage(plan BatchPlan, impact RevisionImpact, stage string, candidate *AdaptationPlan) error {
	want := make(map[string]struct{})
	for _, item := range impact.Items {
		if item.Requirement == StructureImpactRequired && item.ArtifactKind == StructureKindChapter && adaptationStageIncludesImpact(stage, item) {
			want[item.ArtifactID] = struct{}{}
		}
	}
	got := make(map[string]struct{})
	for _, batch := range plan.Batches {
		for _, chapterID := range batch.ChapterIDs {
			got[chapterID] = struct{}{}
		}
		if candidate != nil {
			for _, chapterID := range batch.ChapterIDs {
				chapter, ok := adaptationChapterByID(candidate.Chapters, chapterID)
				if !ok {
					return fmt.Errorf("adaptation batch %q references unknown target chapter %q", batch.ID, chapterID)
				}
				hasSourceContext := false
				for _, item := range batch.Context {
					if item.Kind == BatchContextSourceAnchor {
						hasSourceContext = true
					}
				}
				if !chapter.IsAdded && !hasSourceContext {
					return fmt.Errorf("adaptation batch %q must load batch-local source anchors for target %q", batch.ID, chapterID)
				}
			}
		}
	}
	if stage == AdaptationApprovalStructure && len(want) == 0 {
		return nil
	}
	if len(want) != len(got) {
		return fmt.Errorf("adaptation BatchPlan must exactly cover %d affected target chapters, got %d", len(want), len(got))
	}
	for chapterID := range want {
		if _, exists := got[chapterID]; !exists {
			return fmt.Errorf("adaptation BatchPlan is missing affected target chapter %q", chapterID)
		}
	}
	return nil
}

func adaptationCandidateBatchPlan(versions []ArtifactVersion) (BatchPlan, error) {
	if len(versions) != 1 {
		return BatchPlan{}, fmt.Errorf("adaptation candidate requires exactly one BatchPlan")
	}
	var plan BatchPlan
	if err := decodeAdaptationStrict(versions[0].Payload, &plan); err != nil {
		return BatchPlan{}, err
	}
	return plan, nil
}

func adaptationCurrentApprovalStage(session RevisionSession, p AdaptationRevisionPolicy) (string, error) {
	stages := session.ApprovalStages
	if len(stages) == 0 {
		var err error
		stages, err = p.ApprovalStages(session.Impact)
		if err != nil {
			return "", err
		}
	}
	if len(session.Approvals) >= len(stages) {
		return "publish", nil
	}
	return stages[len(session.Approvals)].ID, nil
}

func adaptationStageIncludesImpact(stage string, item RevisionImpactItem) bool {
	switch stage {
	case AdaptationApprovalStructure:
		return item.ArtifactKind == StructureKindVolume || item.ArtifactKind == StructureKindArc || item.ArtifactKind == AdaptationRevisionArtifactPlanSnapshot || item.ArtifactKind == AdaptationRevisionArtifactBatchPlan
	case AdaptationApprovalOutline:
		return item.ArtifactKind == StructureKindChapter || item.ArtifactKind == AdaptationRevisionArtifactPlanSnapshot || item.ArtifactKind == AdaptationRevisionArtifactBatchPlan
	case AdaptationApprovalProse:
		return item.ArtifactKind == AdaptationRevisionArtifactProseReworkIntent || item.ArtifactKind == AdaptationRevisionArtifactProseReworkQueue
	case "publish":
		return true
	default:
		return false
	}
}

func adaptationImpactChangesStructure(impact RevisionImpact) bool {
	for _, item := range impact.Items {
		if item.Cause == StructureImpactStructureChange || item.ArtifactKind == StructureKindVolume || item.ArtifactKind == StructureKindArc {
			return true
		}
	}
	return false
}

func adaptationImpactRewritesProse(impact RevisionImpact) bool {
	for _, item := range impact.Items {
		if item.RequiresBodyRewrite {
			return true
		}
	}
	return false
}

func (p AdaptationRevisionPolicy) protectedMainlineEvents() map[string]bool {
	protected := make(map[string]bool)
	if p.BasePlan == nil {
		return protected
	}
	for _, event := range p.BasePlan.SourceEvents {
		if event.Required || event.Importance == AdaptationEventMainline {
			protected[strings.TrimSpace(event.ID)] = true
		}
	}
	return protected
}

func destructiveAdaptationChange(change string) bool {
	change = strings.ToLower(strings.TrimSpace(change))
	for _, fragment := range []string{"remove", "delete", "drop", "omit", "erase", "weaken mainline", "删除", "删去", "移除", "省略", "遗漏", "弱化主线"} {
		if strings.Contains(change, fragment) {
			return true
		}
	}
	return false
}

func changedProtectedEventIDs(base, candidate []AdaptationEvent) []string {
	candidateByID := make(map[string]AdaptationEvent, len(candidate))
	for _, event := range candidate {
		candidateByID[event.ID] = event
	}
	var changed []string
	for _, event := range base {
		if !event.Required && event.Importance != AdaptationEventMainline {
			continue
		}
		other, exists := candidateByID[event.ID]
		if !exists || adaptationJSONSignature(event) != adaptationJSONSignature(other) {
			changed = append(changed, event.ID)
		}
	}
	slices.Sort(changed)
	return changed
}

func adaptationEventOwner(chapters []AdaptationChapterPlan, eventID string) int {
	for _, chapter := range chapters {
		if slices.Contains(chapter.EventIDs, eventID) || slices.Contains(chapter.AddedEventIDs, eventID) {
			return chapter.Chapter
		}
	}
	return 0
}

func adaptationChapterByID(chapters []AdaptationChapterPlan, id string) (AdaptationChapterPlan, bool) {
	for _, chapter := range chapters {
		if strings.TrimSpace(chapter.ID) == strings.TrimSpace(id) {
			return chapter, true
		}
	}
	return AdaptationChapterPlan{}, false
}

func nonBlankAdaptationStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func adaptationChapterHasSourceLineage(chapter AdaptationChapterPlan) bool {
	return len(chapter.SourceChapters) > 0 || len(chapter.SourceSegments) > 0 ||
		chapter.SourceRange.From > 0 || chapter.SourceRange.To > 0 || chapter.SourceRunes > 0 ||
		len(chapter.EventIDs) > 0
}

func adaptationJSONSignature(value any) string {
	payload, _ := json.Marshal(value)
	return JSONContentSignature(payload)
}

// AdaptationSourceManifestContractSignature binds revision work to immutable
// source chapter identities without copying source paths or prose into a
// candidate payload.
func AdaptationSourceManifestContractSignature(manifest AdaptationSourceManifest) string {
	type sourceIdentity struct {
		Chapter int    `json:"chapter"`
		Title   string `json:"title"`
		SHA256  string `json:"sha256"`
		Runes   int    `json:"runes"`
	}
	identities := make([]sourceIdentity, 0, len(manifest.Chapters))
	for _, source := range manifest.Chapters {
		identities = append(identities, sourceIdentity{Chapter: source.Chapter, Title: source.Title, SHA256: source.SHA256, Runes: source.Runes})
	}
	sort.Slice(identities, func(left, right int) bool { return identities[left].Chapter < identities[right].Chapter })
	return adaptationJSONSignature(struct {
		ChapterCount int              `json:"chapter_count"`
		Chapters     []sourceIdentity `json:"chapters"`
	}{ChapterCount: manifest.ChapterCount, Chapters: identities})
}

func decodeAdaptationStrict(payload json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func hasJSONField(payload json.RawMessage, field string) bool {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(payload, &value); err != nil {
		return false
	}
	_, exists := value[field]
	return exists
}
