package domain

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestValidateAdaptationRevisionPlanSupportsChapterInsertionWithoutSourceLoss(t *testing.T) {
	base, manifest := adaptationRevisionFixture(AdaptationGranularityChapter)
	candidate := cloneAdaptationRevisionPlan(t, base)
	candidate.TargetEventLedger = append(candidate.TargetEventLedger, AdaptationEvent{
		ID: "added-bridge", Description: "新增过桥情节", Origin: AdaptationEventOriginAdded,
		Importance: AdaptationEventSupporting, Required: true,
	})
	inserted := adaptationRevisionAddedChapter("target-added", 2, "added-bridge")
	candidate.Chapters = append(candidate.Chapters[:1], append([]AdaptationChapterPlan{inserted}, candidate.Chapters[1:]...)...)
	for index := range candidate.Chapters {
		candidate.Chapters[index].Chapter = index + 1
	}
	candidate.Volumes[0].TargetTo = 3
	candidate.TargetTotalRunes += inserted.TargetRunes
	candidate.TargetMaxRunes += inserted.TargetMaxRunes

	if err := ValidateAdaptationRevisionPlan(base, candidate, &manifest); err != nil {
		t.Fatalf("chapter insertion rejected: %v", err)
	}
}

func TestValidateAdaptationRevisionPlanSupportsArcSourceReallocation(t *testing.T) {
	base, manifest := adaptationRevisionFixture(AdaptationGranularityArc)
	candidate := cloneAdaptationRevisionPlan(t, base)
	inserted := adaptationRevisionSourceChapter("target-bridge", 2, 1, nil)
	inserted.EventIDs = nil
	inserted.SourceRunes = 400
	inserted.CoverageNote = "split source chapter one transition across two target chapters"
	inserted.RequiredChanges = []string{"insert a causal bridge"}
	inserted.PreserveEvents = []string{"preserve source chapter one outcome"}
	inserted.ForbiddenMoves = []string{"do not move the protected meeting"}
	candidate.Chapters = append(candidate.Chapters[:1], append([]AdaptationChapterPlan{inserted}, candidate.Chapters[1:]...)...)
	for index := range candidate.Chapters {
		candidate.Chapters[index].Chapter = index + 1
	}
	candidate.Volumes[0].TargetTo = 3

	if err := ValidateAdaptationRevisionPlan(base, candidate, &manifest); err != nil {
		t.Fatalf("arc source reallocation rejected: %v", err)
	}
}

func TestValidateAdaptationRevisionPreviewCandidateRequiresPreserveForRangeOnlySourceLineage(t *testing.T) {
	base, manifest := adaptationRevisionFixture(AdaptationGranularityArc)
	base.SourceEvents = nil
	base.Volumes[0].MainlineEventIDs = nil
	for index := range base.Chapters {
		base.Chapters[index].SourceChapters = nil
		base.Chapters[index].SourceSegments = nil
		base.Chapters[index].EventIDs = nil
	}
	base.Chapters[0].SourceRunes = 0
	base.Chapters[0].PreserveEvents = nil
	candidate := cloneAdaptationRevisionPlan(t, base)
	chapter := candidate.Chapters[0]
	if len(chapter.SourceChapters) != 0 || len(chapter.SourceSegments) != 0 || chapter.SourceRunes != 0 || len(chapter.EventIDs) != 0 ||
		chapter.SourceRange.From <= 0 || chapter.SourceRange.To < chapter.SourceRange.From {
		t.Fatalf("range-only fixture has another source carrier: %+v", chapter)
	}
	impact := adaptationRevisionImpact(t, []RevisionImpactItem{{
		ArtifactID: candidate.Chapters[0].ID, ArtifactKind: StructureKindChapter,
		Change: "validate range-only source ownership", Requirement: StructureImpactRequired,
		Cause: StructureImpactContentDependency, DependencyEvidence: []string{"durable source range"},
	}})

	err := ValidateAdaptationRevisionPreviewCandidate(base, candidate, &manifest, impact)
	if err == nil || !strings.Contains(err.Error(), "requires preserve_events") {
		t.Fatalf("range-only source lineage without preserve_events was accepted: %v", err)
	}
}

func TestValidateAdaptationRevisionPlanSupportsFreeOriginalVolume(t *testing.T) {
	base, manifest := adaptationRevisionFixture(AdaptationGranularityFree)
	base.TargetEventLedger = []AdaptationEvent{{
		ID: "target-opening", Description: "target opening", Origin: AdaptationEventOriginTarget,
		Importance: AdaptationEventMainline, Required: true,
	}}
	base.Chapters[0].EventIDs = append(base.Chapters[0].EventIDs, "target-opening")
	candidate := cloneAdaptationRevisionPlan(t, base)
	candidate.TargetEventLedger = append(candidate.TargetEventLedger, AdaptationEvent{
		ID: "target-afterstory", Description: "original afterstory", Origin: AdaptationEventOriginAdded,
		Importance: AdaptationEventSupporting, Required: true, DependsOn: []string{"target-opening"},
	})
	added := adaptationRevisionAddedChapter("target-afterstory-chapter", 3, "target-afterstory")
	candidate.Chapters = append(candidate.Chapters, added)
	candidate.Volumes = append(candidate.Volumes, AdaptationVolumePlan{
		ID: "volume-original", Index: 2, Title: "Original Volume", TargetFrom: 3, TargetTo: 3,
	})

	if err := ValidateAdaptationRevisionPlan(base, candidate, &manifest); err != nil {
		t.Fatalf("free original volume rejected: %v", err)
	}
}

func TestAdaptationRevisionPolicyRequiresContractChangeForProtectedMainline(t *testing.T) {
	base, manifest := adaptationRevisionFixture(AdaptationGranularityArc)
	candidate := cloneAdaptationRevisionPlan(t, base)
	candidate.SourceEvents = candidate.SourceEvents[1:]
	err := ValidateAdaptationRevisionPlan(base, candidate, &manifest)
	if !IsAdaptationContractChangeRequired(err) || !strings.Contains(err.Error(), "contract") {
		t.Fatalf("protected mainline change was not blocked: %v", err)
	}

	impact := adaptationRevisionImpact(t, []RevisionImpactItem{{
		ArtifactID: "target-1", ArtifactKind: StructureKindChapter, Change: "remove protected mainline",
		Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency,
		DependencyEvidence: []string{"user feedback"}, DependencySourceIDs: []string{"source-event-1"},
	}})
	policy := NewAdaptationRevisionPolicy(ManuscriptStageWriting, &base, &manifest)
	if err := policy.ValidateImpact(impact); !IsAdaptationContractChangeRequired(err) {
		t.Fatalf("destructive feedback preview was not blocked: %v", err)
	}
}

func TestAdaptationRevisionPolicyRejectsIdentityLineageAndProtectedContractTampering(t *testing.T) {
	base, manifest := adaptationRevisionFixture(AdaptationGranularityArc)
	tests := []struct {
		name string
		edit func(*AdaptationPlan)
		want string
	}{
		{"existing IsAdded flip", func(plan *AdaptationPlan) { plan.Chapters[0].IsAdded = true }, "IsAdded"},
		{"added source ownership", func(plan *AdaptationPlan) {
			plan.TargetEventLedger = append(plan.TargetEventLedger, AdaptationEvent{ID: "added", Origin: AdaptationEventOriginAdded, Required: true})
			chapter := adaptationRevisionAddedChapter("target-added", 3, "added")
			chapter.EventIDs = []string{"source-event-1"}
			plan.Chapters = append(plan.Chapters, chapter)
			plan.Volumes[0].TargetTo = 3
		}, "cannot own source"},
		{"missing complete ledger owner", func(plan *AdaptationPlan) { plan.Chapters[1].EventIDs = nil }, "no target owner"},
		{"wrong source lineage owner", func(plan *AdaptationPlan) {
			plan.Chapters[1].EventIDs = nil
			plan.Chapters[0].EventIDs = append(plan.Chapters[0].EventIDs, "source-event-2")
		}, "outside its source lineage"},
		{"protected rules", func(plan *AdaptationPlan) {
			plan.Rules = append(plan.Rules, AdaptationRule{ID: "new-rule", Kind: AdaptationRuleGuidance, Text: "replace protected rule"})
		}, "protected adaptation rules"},
		{"protected volume ownership", func(plan *AdaptationPlan) { plan.Volumes[0].SourceTo = 1 }, "volume"},
		{"protected preserve ledger", func(plan *AdaptationPlan) { plan.Chapters[0].PreserveEvents = []string{"different"} }, "preserve/forbidden/rule"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneAdaptationRevisionPlan(t, base)
			test.edit(&candidate)
			err := ValidateAdaptationRevisionPlan(base, candidate, &manifest)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("tampering accepted or wrong error: %v", err)
			}
		})
	}
}

func TestAdaptationRevisionPlanSnapshotRequiresStageAndSourceSignature(t *testing.T) {
	base, manifest := adaptationRevisionFixture(AdaptationGranularityArc)
	policy := NewAdaptationRevisionPolicy(ManuscriptStageWriting, &base, &manifest)
	payload := mustAdaptationRevisionJSON(t, AdaptationPlanRevisionCandidate{Stage: ManuscriptStageWriting, Plan: base})
	if _, err := policy.decodePlanCandidate(payload); err == nil || !strings.Contains(err.Error(), "source signature") {
		t.Fatalf("unsigned snapshot accepted: %v", err)
	}
	plain := mustAdaptationRevisionJSON(t, base)
	if _, err := policy.decodePlanCandidate(plain); err == nil || !strings.Contains(err.Error(), "strict") {
		t.Fatalf("legacy unwrapped snapshot accepted: %v", err)
	}
}

func TestAdaptationRevisionPolicyCoversEveryManuscriptStage(t *testing.T) {
	base, manifest := adaptationRevisionFixture(AdaptationGranularityArc)
	impact := adaptationRevisionImpact(t, []RevisionImpactItem{
		{ArtifactID: "volume-new", ArtifactKind: StructureKindVolume, Change: "append volume", Requirement: StructureImpactRequired, Cause: StructureImpactStructureChange, DependencyEvidence: []string{"new dramatic phase"}},
		{ArtifactID: "target-new", ArtifactKind: StructureKindChapter, Change: "append chapter", Requirement: StructureImpactRequired, Cause: StructureImpactStructureChange, DependencyEvidence: []string{"new volume slot"}},
	})
	for _, stage := range []ManuscriptStage{ManuscriptStageProposalComplete, ManuscriptStageOutlineComplete, ManuscriptStageWriting, ManuscriptStageComplete} {
		policy := NewAdaptationRevisionPolicy(stage, &base, &manifest)
		stages, err := policy.ApprovalStages(impact)
		if err != nil {
			t.Fatalf("stage %s: %v", stage, err)
		}
		if len(stages) != 2 || stages[0].ID != AdaptationApprovalStructure || stages[1].ID != AdaptationApprovalOutline {
			t.Fatalf("stage %s approval order = %+v", stage, stages)
		}
	}
}

func TestAdaptationRevisionBatchPlanShrinksAndResumes(t *testing.T) {
	plan := adaptationRevisionBatchPlan([]string{"target-1", "target-2"}, true)
	if err := ValidateAdaptationRevisionBatchPlan(plan); err != nil {
		t.Fatalf("constrained two-chapter batch rejected: %v", err)
	}
	active, err := plan.StartNext()
	if err != nil || active == nil {
		t.Fatalf("start constrained batch: active=%+v err=%v", active, err)
	}
	if err := plan.Fail(active.ID, "context risk"); err != nil {
		t.Fatal(err)
	}
	if err := plan.Resume(active.ID); err != nil {
		t.Fatalf("resume failed batch: %v", err)
	}
	if plan.Batches[0].Status != BatchStatusPending || plan.Batches[0].Attempts != 1 {
		t.Fatalf("resumed batch lost checkpoint: %+v", plan.Batches[0])
	}

	overwide := adaptationRevisionBatchPlan([]string{"target-1", "target-2", "target-3"}, true)
	if err := ValidateAdaptationRevisionBatchPlan(overwide); err == nil || !strings.Contains(err.Error(), "shrink") {
		t.Fatalf("overwide constrained batch accepted: %v", err)
	}
}

func TestAdaptationRevisionStructureCandidateCannotPreGenerateDetails(t *testing.T) {
	base, manifest := adaptationRevisionFixture(AdaptationGranularityArc)
	candidate := cloneAdaptationRevisionPlan(t, base)
	added := adaptationRevisionAddedChapter("target-new", 3, "added-bridge")
	added.Title = "generated too soon"
	added.CoreEvent = "generated too soon"
	added.Hook = "generated too soon"
	added.Scenes = []string{"generated too soon"}
	candidate.TargetEventLedger = append(candidate.TargetEventLedger, AdaptationEvent{ID: "added-bridge", Description: "bridge", Origin: AdaptationEventOriginAdded, Importance: AdaptationEventSupporting, Required: true})
	candidate.Chapters = append(candidate.Chapters, added)
	candidate.Volumes[0].TargetTo = 3
	impact := adaptationRevisionImpact(t, []RevisionImpactItem{
		{ArtifactID: "volume-1", ArtifactKind: StructureKindVolume, Change: "expand volume", Requirement: StructureImpactRequired, Cause: StructureImpactStructureChange, DependencyEvidence: []string{"new plot"}},
		{ArtifactID: "target-new", ArtifactKind: StructureKindChapter, Change: "append chapter", Requirement: StructureImpactRequired, Cause: StructureImpactStructureChange, DependencyEvidence: []string{"new slot"}},
	})
	policy := NewAdaptationRevisionPolicy(ManuscriptStageComplete, &base, &manifest)
	session := RevisionSession{Mode: RevisionModeAdaptation, Impact: impact}
	versions := []ArtifactVersion{
		adaptationRevisionVersion("volume-1", StructureKindVolume, mustAdaptationRevisionJSON(t, candidate.Volumes[0])),
		adaptationRevisionVersion(AdaptationRevisionPlanSnapshotID, AdaptationRevisionArtifactPlanSnapshot, mustAdaptationRevisionJSON(t, AdaptationPlanRevisionCandidate{Stage: ManuscriptStageComplete, SourceSignature: AdaptationSourceManifestContractSignature(manifest), Plan: candidate})),
		adaptationRevisionVersion(AdaptationRevisionBatchPlanID, AdaptationRevisionArtifactBatchPlan, mustAdaptationRevisionJSON(t, adaptationRevisionBatchPlan([]string{"target-new"}, false))),
	}
	if err := policy.ValidateCandidate(session, versions); err == nil || !strings.Contains(err.Error(), "before structure approval") {
		t.Fatalf("structure candidate pre-generated details: %v", err)
	}
}

func TestAdaptationRevisionDetailedCandidateRequiresLayeredSignedScopes(t *testing.T) {
	base, manifest := adaptationRevisionFixture(AdaptationGranularityArc)
	structure := cloneAdaptationRevisionPlan(t, base)
	structure.TargetEventLedger = append(structure.TargetEventLedger, AdaptationEvent{ID: "added-bridge", Description: "bridge", Origin: AdaptationEventOriginAdded, Importance: AdaptationEventSupporting, Required: true})
	structure.Chapters = append(structure.Chapters, adaptationRevisionAddedChapter("target-new", 3, "added-bridge"))
	structure.Volumes[0].TargetTo = 3
	detailPlan := cloneAdaptationRevisionPlan(t, structure)
	detailPlan.Chapters[2].Title = "Bridge"
	detailPlan.Chapters[2].OutlineEntry.Title = "Bridge"
	detailPlan.Chapters[2].CoreEvent = "bridge the old ending to the new phase"
	detailPlan.Chapters[2].Hook = "a new threat appears"
	detailPlan.Chapters[2].Scenes = []string{"resolve old aftermath", "open new conflict"}
	impact := adaptationRevisionImpact(t, []RevisionImpactItem{
		{ArtifactID: "volume-1", ArtifactKind: StructureKindVolume, Change: "expand volume", Requirement: StructureImpactRequired, Cause: StructureImpactStructureChange, DependencyEvidence: []string{"new plot"}},
		{ArtifactID: "target-new", ArtifactKind: StructureKindChapter, Change: "append chapter", Requirement: StructureImpactRequired, Cause: StructureImpactStructureChange, DependencyEvidence: []string{"new slot"}},
	})
	policy := NewAdaptationRevisionPolicy(ManuscriptStageWriting, &structure, &manifest)
	session := RevisionSession{
		Mode: RevisionModeAdaptation, Impact: impact,
		ApprovalStages: []RevisionApprovalStage{{ID: AdaptationApprovalStructure}, {ID: AdaptationApprovalOutline}},
		Approvals:      []RevisionApproval{{StageID: AdaptationApprovalStructure}},
	}
	versions := []ArtifactVersion{
		adaptationRevisionVersion("target-new", StructureKindChapter, mustAdaptationRevisionJSON(t, AdaptationDetailedOutlineCandidate{
			ChapterID: "target-new", CurrentNumber: 3, VolumeID: "volume-1", ArcID: "volume-1:revision-arc", Outline: detailPlan.Chapters[2],
		})),
		adaptationRevisionVersion(AdaptationRevisionPlanSnapshotID, AdaptationRevisionArtifactPlanSnapshot, mustAdaptationRevisionJSON(t, AdaptationPlanRevisionCandidate{
			Stage: ManuscriptStageWriting, SourceSignature: AdaptationSourceManifestContractSignature(manifest), Plan: structure,
		})),
		adaptationRevisionVersion(AdaptationRevisionBatchPlanID, AdaptationRevisionArtifactBatchPlan, mustAdaptationRevisionJSON(t, adaptationRevisionBatchPlan([]string{"target-new"}, false))),
	}
	if err := policy.ValidateCandidate(session, versions); err != nil {
		t.Fatalf("signed detailed candidate rejected: %v", err)
	}
	expectations, err := policy.AuditExpectations(session, versions)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"chapter_deterministic": false, "chapter_semantic": false, "parent_batch": false, "volume": false, "global": false, "adaptation_contract": false}
	for _, expectation := range expectations {
		if _, exists := want[expectation.Scope]; exists {
			want[expectation.Scope] = true
		}
	}
	for scope, present := range want {
		if !present {
			t.Fatalf("missing layered audit scope %q in %+v", scope, expectations)
		}
	}
}

func TestAdaptationRevisionExistingTargetLedgerIsAppendOnlyWithStableOwner(t *testing.T) {
	base, _ := adaptationRevisionFixture(AdaptationGranularityArc)
	base.TargetEventLedger = []AdaptationEvent{{ID: "existing-added", Description: "existing bridge", Origin: AdaptationEventOriginAdded, Importance: AdaptationEventSupporting, Required: true, DependsOn: []string{"source-event-2"}}}
	existingAdded := adaptationRevisionAddedChapter("target-existing-added", 3, "existing-added")
	existingAdded.Title = "Existing bridge"
	existingAdded.OutlineEntry.Title = existingAdded.Title
	existingAdded.CoreEvent = "continue the existing bridge"
	existingAdded.Hook = "existing hook"
	existingAdded.Scenes = []string{"existing scene"}
	base.Chapters = append(base.Chapters, existingAdded)
	base.Volumes[0].TargetTo = 3

	for name, mutate := range map[string]func(*AdaptationPlan){
		"delete ledger entry": func(plan *AdaptationPlan) { plan.TargetEventLedger = nil },
		"mutate ledger entry": func(plan *AdaptationPlan) { plan.TargetEventLedger[0].Origin = AdaptationEventOriginSource },
		"remove owner":        func(plan *AdaptationPlan) { plan.Chapters[2].AddedEventIDs = nil },
		"replace owner": func(plan *AdaptationPlan) {
			plan.Chapters[2].AddedEventIDs = nil
			plan.Chapters[1].AddedEventIDs = []string{"existing-added"}
		},
		"duplicate owner": func(plan *AdaptationPlan) { plan.Chapters[1].AddedEventIDs = []string{"existing-added"} },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneAdaptationRevisionPlan(t, base)
			mutate(&candidate)
			if err := validateAdaptationEventContract(base, candidate); err == nil {
				t.Fatal("existing target ledger/owner drift was accepted")
			}
		})
	}

	appended := cloneAdaptationRevisionPlan(t, base)
	appended.TargetEventLedger = append(appended.TargetEventLedger, AdaptationEvent{ID: "new-added", Description: "new bridge", Origin: AdaptationEventOriginAdded, Importance: AdaptationEventSupporting, Required: true, DependsOn: []string{"existing-added"}})
	appended.Chapters = append(appended.Chapters, adaptationRevisionAddedChapter("target-new-added", 4, "new-added"))
	if err := validateAdaptationEventContract(base, appended); err != nil {
		t.Fatalf("validated append-only target event was rejected: %v", err)
	}

	relationshipDrift := cloneAdaptationRevisionPlan(t, base)
	base.TargetRelationshipStates = map[string]string{"a:b": "trusted"}
	relationshipDrift.TargetRelationshipStates = map[string]string{"a:b": "hostile"}
	if err := validateAdaptationSourceContractImmutable(base, relationshipDrift); err == nil {
		t.Fatal("protected target relationship drift was accepted")
	}
	settingDrift := cloneAdaptationRevisionPlan(t, base)
	base.Chapters[0].SettingClaims = []AdaptationSettingClaim{{Key: "city", Value: "old"}}
	settingDrift.Chapters[0].SettingClaims = []AdaptationSettingClaim{{Key: "city", Value: "new"}}
	if err := validateAdaptationChapterRevisionContracts(base, settingDrift); err == nil {
		t.Fatal("protected chapter setting drift was accepted")
	}
}

func TestAdaptationRevisionExistingTargetLedgerOwnershipCategoryIsImmutable(t *testing.T) {
	base, _ := adaptationRevisionFixture(AdaptationGranularityArc)
	base.TargetEventLedger = []AdaptationEvent{
		{ID: "target-origin", Description: "target-owned event", Origin: AdaptationEventOriginAdded, Importance: AdaptationEventSupporting, Required: true},
		{ID: "added-origin", Description: "revision-added event", Origin: AdaptationEventOriginAdded, Importance: AdaptationEventSupporting, Required: true},
	}
	base.Chapters[0].EventIDs = append(base.Chapters[0].EventIDs, "target-origin")
	base.Chapters[1].AddedEventIDs = append(base.Chapters[1].AddedEventIDs, "added-origin")

	for name, mutate := range map[string]func(*AdaptationPlan){
		"target category to added": func(plan *AdaptationPlan) {
			plan.Chapters[0].EventIDs = slices.DeleteFunc(plan.Chapters[0].EventIDs, func(id string) bool { return id == "target-origin" })
			plan.Chapters[0].AddedEventIDs = append(plan.Chapters[0].AddedEventIDs, "target-origin")
		},
		"added category to target": func(plan *AdaptationPlan) {
			plan.Chapters[1].AddedEventIDs = slices.DeleteFunc(plan.Chapters[1].AddedEventIDs, func(id string) bool { return id == "added-origin" })
			plan.Chapters[1].EventIDs = append(plan.Chapters[1].EventIDs, "added-origin")
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneAdaptationRevisionPlan(t, base)
			mutate(&candidate)
			if err := validateAdaptationEventContract(base, candidate); err == nil || !strings.Contains(err.Error(), "ownership changed") {
				t.Fatalf("existing target-ledger category drift was accepted: %v", err)
			}
		})
	}

	appended := cloneAdaptationRevisionPlan(t, base)
	appended.TargetEventLedger = append(appended.TargetEventLedger, AdaptationEvent{ID: "new-event", Description: "valid append", Origin: AdaptationEventOriginAdded, Importance: AdaptationEventSupporting, Required: true})
	appended.Chapters[1].AddedEventIDs = append(appended.Chapters[1].AddedEventIDs, "new-event")
	if err := validateAdaptationEventContract(base, appended); err != nil {
		t.Fatalf("valid append-only event ownership was rejected: %v", err)
	}
}

func adaptationRevisionFixture(granularity string) (AdaptationPlan, AdaptationSourceManifest) {
	rewrite := AdaptationRewritePolicyForGranularity(granularity)
	events := []AdaptationEvent{
		{ID: "source-event-1", Description: "protected meeting", Origin: AdaptationEventOriginSource, Importance: AdaptationEventMainline, SourceChapter: 1, Required: true},
		{ID: "source-event-2", Description: "second clue", Origin: AdaptationEventOriginSource, Importance: AdaptationEventSupporting, SourceChapter: 2, Required: true, DependsOn: []string{"source-event-1"}},
	}
	chapters := []AdaptationChapterPlan{
		adaptationRevisionSourceChapter("target-1", 1, 1, []AdaptationSourceSegment{{SourceChapter: 1, Sequence: 1, EventIDs: []string{"source-event-1"}, RuneShare: AdaptationSourceRuneShare{Start: 0, End: 1000}, EntryState: AdaptationSegmentState{}, ExitState: AdaptationSegmentState{}}}),
		adaptationRevisionSourceChapter("target-2", 2, 2, []AdaptationSourceSegment{{SourceChapter: 2, Sequence: 1, EventIDs: []string{"source-event-2"}, RuneShare: AdaptationSourceRuneShare{Start: 0, End: 1000}, EntryState: AdaptationSegmentState{}, ExitState: AdaptationSegmentState{}}}),
	}
	if granularity != AdaptationGranularityChapter {
		for index := range chapters {
			chapters[index].SourceSegments = nil
		}
	}
	plan := AdaptationPlan{
		Granularity: granularity, ModePolicy: AdaptationModePolicyForGranularity(granularity), Status: AdaptationPlanStatusConfirmed,
		RewritePolicy: rewrite, Brief: "preserve protected events", WordTolerance: 0.15,
		SourceTotalRunes: 2000, TargetTotalRunes: 8000, TargetMinRunes: 6000, TargetMaxRunes: 10000,
		SourceEvents: events, Chapters: chapters,
		Volumes: []AdaptationVolumePlan{{ID: "volume-1", Index: 1, Title: "Source Volume", TargetFrom: 1, TargetTo: 2, SourceFrom: 1, SourceTo: 2, MainlineEventIDs: []string{"source-event-1"}}},
	}
	manifest := AdaptationSourceManifest{ChapterCount: 2, Chapters: []AdaptationSource{{Chapter: 1, SHA256: "one", Runes: 1000}, {Chapter: 2, SHA256: "two", Runes: 1000}}}
	return plan, manifest
}

func adaptationRevisionSourceChapter(id string, number, source int, segments []AdaptationSourceSegment) AdaptationChapterPlan {
	return AdaptationChapterPlan{
		OutlineEntry: OutlineEntry{ID: id, Chapter: number, Title: "Chapter " + id, CoreEvent: "advance " + id, Hook: "hook " + id, Scenes: []string{"scene " + id}},
		Chapter:      number, Title: "Chapter " + id, SourceChapters: []int{source}, SourceRunes: 1000,
		SourceRange: SourceRange{From: source, To: source}, SourceSegments: segments,
		EventIDs: []string{"source-event-" + string(rune('0'+source))}, TargetRunes: 4000, TargetMinRunes: 3000, TargetMaxRunes: 5000,
		PreserveEvents: []string{"preserve source event"}, RequiredChanges: []string{"apply adaptation request"}, ForbiddenMoves: []string{"do not drop source event"},
	}
}

func adaptationRevisionAddedChapter(id string, number int, eventID string) AdaptationChapterPlan {
	return AdaptationChapterPlan{
		OutlineEntry: OutlineEntry{ID: id, Chapter: number}, Chapter: number, IsAdded: true,
		CoverageNote: "original plot that does not replace source coverage", AddedEventIDs: []string{eventID},
		TargetRunes: 3500, TargetMinRunes: 2500, TargetMaxRunes: 4500,
		RequiredChanges: []string{"add the requested plot"}, ForbiddenMoves: []string{"do not replace protected source events"},
	}
}

func adaptationRevisionImpact(t *testing.T, items []RevisionImpactItem) RevisionImpact {
	t.Helper()
	items = append(items,
		RevisionImpactItem{ArtifactID: AdaptationRevisionBatchPlanID, ArtifactKind: AdaptationRevisionArtifactBatchPlan, Change: "bounded generation and review", Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency, DependencyEvidence: []string{"BatchPlan boundary"}},
		RevisionImpactItem{ArtifactID: AdaptationRevisionPlanSnapshotID, ArtifactKind: AdaptationRevisionArtifactPlanSnapshot, Change: "bind immutable source contract", Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency, DependencyEvidence: []string{"source contract signature"}},
	)
	impact, err := NewRevisionImpact("adaptation revision", items)
	if err != nil {
		t.Fatal(err)
	}
	return impact
}

func adaptationRevisionBatchPlan(chapterIDs []string, constrained bool) BatchPlan {
	return BatchPlan{
		Batches: []BatchWork{{
			ID: "batch-001", Index: 1, ChapterIDs: chapterIDs, VolumeID: "volume-1", ArcID: "arc-1",
			EstimatedOutputWords: len(chapterIDs) * 3500, ContextUnits: 100, Constrained: constrained, Status: BatchStatusPending,
			Context: []BatchContextItem{{ID: "source-chapters-1-2", Kind: BatchContextSourceAnchor, Units: 100, Necessary: true}},
		}},
		VolumeReviews:   []BatchAggregateReview{{ScopeID: "volume-1", Status: BatchReviewPending}},
		WholeBookReview: BatchAggregateReview{ScopeID: "whole-book", Status: BatchReviewPending},
	}
}

func adaptationRevisionVersion(id, kind string, payload json.RawMessage) ArtifactVersion {
	return ArtifactVersion{ID: "version-" + id, ArtifactID: id, ArtifactKind: kind, Sequence: 1, Round: 1, Payload: payload, ContentSignature: JSONContentSignature(payload), CreatedAt: RevisionTimestamp()}
}

func cloneAdaptationRevisionPlan(t *testing.T, plan AdaptationPlan) AdaptationPlan {
	t.Helper()
	payload := mustAdaptationRevisionJSON(t, plan)
	var clone AdaptationPlan
	if err := json.Unmarshal(payload, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func mustAdaptationRevisionJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
