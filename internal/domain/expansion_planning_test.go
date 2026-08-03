package domain

import "testing"

func TestExpansionRequestRejectsModeAndInvalidBoundariesByConstruction(t *testing.T) {
	request := ExpansionRequest{Location: ExpansionBetween, ReferenceIDs: []string{"ch-a", "ch-b"}, Sentence: "让误会升级为一次不可逆的选择", Adjustment: ExpansionAdjustmentDefault, ExpectedStructureRevision: 3, ExpectedStructureSignature: "sha", IdempotencyKey: "plan-1"}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	request.ReferenceIDs = []string{"ch-a", "ch-a"}
	if err := request.Validate(); err == nil {
		t.Fatal("duplicate stable references were accepted")
	}
}

func TestExpansionRecommendationNewVolumeRequiresDramaticStageEvidence(t *testing.T) {
	budget, err := NewDynamicSoftBudget(3, 2500, 4000)
	if err != nil {
		t.Fatal(err)
	}
	recommendation := ExpansionRecommendation{
		Form: ExpansionFormNewVolume, Reason: "独立阶段", Location: ExpansionBookEnd,
		ChapterCount: 3, ChapterMinWords: 2500, ChapterMaxWords: 4000, TotalMinWords: 7500, TotalMaxWords: 12000,
		NewVolume: true, OldSummary: "旧冲突已结束", NewSummary: "代价形成新阶段", SoftBudgetDelta: budget,
		Assessment: ExpansionDramaticAssessment{Goal: "守住成果", Conflict: "继承冲突", Choice: "公开站队", Cost: "失去盟友", Result: "新秩序", CharacterStageChange: "从求生到执政", CharacterBeforeStage: "求生", CharacterAfterStage: "执政", CurrentFit: "当前卷已闭合", VolumePacingEffect: "形成独立升级"},
		AuditChain: []string{"structure", "outline"}, ModeConstraints: []string{"normal source firewall"}, OrderedOperations: []ExpansionOperation{{Operation: StructureRevisionAppendVolume, Intent: "新增一卷"}},
	}
	if err := recommendation.Validate(RevisionModeNormal); err == nil {
		t.Fatal("new volume without climax/exit evidence was accepted")
	}
	recommendation.Assessment.IndependentClimax = "公开决裂"
	recommendation.Assessment.IrreversibleExit = "旧联盟永久解体"
	if err := recommendation.Validate(RevisionModeNormal); err != nil {
		t.Fatalf("complete new-volume evidence rejected: %v", err)
	}
}

func TestNormalExpansionRecommendationRejectsAdaptationMaterial(t *testing.T) {
	assessment := ExpansionDramaticAssessment{Goal: "g", Conflict: "c", Choice: "x", Cost: "k", Result: "r", CharacterStageChange: "s", CharacterBeforeStage: "before", CharacterAfterStage: "after", IndependentClimax: "climax", IrreversibleExit: "exit", CurrentFit: "f", VolumePacingEffect: "p", AdaptationEffect: "source event"}
	if err := assessment.Validate(RevisionModeNormal); err == nil {
		t.Fatal("normal assessment accepted adaptation material")
	}
}
