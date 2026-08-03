package adaptaudit

import (
	"slices"
	"strings"
	"testing"
)

func TestAuditChapterValidatesLongSourceSegments(t *testing.T) {
	input := chapterAuditInput()
	report := Audit(input)
	if report.Status != "pass" {
		t.Fatalf("status=%s findings=%+v", report.Status, report.Findings)
	}

	input.SourceSegments[1].FromRune = 1100
	report = Audit(input)
	if !hasFindingCode(report, "long_split_gap") {
		t.Fatalf("expected gap finding, got %+v", report.Findings)
	}

	input = chapterAuditInput()
	input.SourceSegments[1].FromRune = 900
	report = Audit(input)
	if !hasFindingCode(report, "long_split_overlap") {
		t.Fatalf("expected overlap finding, got %+v", report.Findings)
	}
}

func TestAuditChapterRejectsMissingAndInsufficientSegmentContracts(t *testing.T) {
	input := chapterAuditInput()
	input.SourceSegments = []SourceSegment{{
		ID: "legacy", Chapter: 1, Sequence: 1, TargetChapter: 1,
		FromRune: 0, ToRune: 10_000, TotalRunes: 10_000, MaxRunes: 5_000,
		Required: true, ContractPresent: false,
	}}
	report := Audit(input)
	for _, code := range []string{"segment_contract_missing", "insufficient_segments"} {
		if !hasFindingCode(report, code) {
			t.Fatalf("missing %s: %+v", code, report.Findings)
		}
	}
}

func TestAuditChapterRejectsSequenceAndStateDiscontinuity(t *testing.T) {
	input := chapterAuditInput()
	input.SourceSegments[1].Sequence = 3
	input.SourceSegments[1].EntryState = map[string]string{"relationship": "strangers"}
	report := Audit(input)
	for _, code := range []string{"segment_sequence", "segment_state_discontinuity"} {
		if !hasFindingCode(report, code) {
			t.Fatalf("missing %s: %+v", code, report.Findings)
		}
	}
}

func TestAuditArcBlocksMissingPromisedMeetAndAddedDisplacement(t *testing.T) {
	input := Input{
		Mode: ModeArc,
		Artifacts: []Artifact{
			{ID: "volume", Kind: ArtifactHighPlan, Text: "百里冰遇劫，林逸飞出手并共同救助皮二，二人由此相识。案件线索随后出现。"},
			{ID: "plan-case", Kind: ArtifactTargetPlan, Chapter: 21, Text: "追查案件线索"},
			{ID: "body-case", Kind: ArtifactTargetChapter, Chapter: 21, Text: "他沿着案件线索追到医院。"},
			{ID: "plan-added", Kind: ArtifactTargetPlan, Chapter: 22, Text: "新增绑架支线"},
			{ID: "body-added", Kind: ArtifactTargetChapter, Chapter: 22, Text: "绑匪打来电话。"},
		},
		Events: []Event{
			{ID: "meet", Origin: OriginSource, Class: ClassMainline, Required: true, HighPlanEvidence: []Evidence{{ArtifactID: "volume", Quote: "百里冰遇劫"}}},
			{ID: "case", Origin: OriginSource, Class: ClassMainline, Required: true, HighPlanEvidence: []Evidence{{ArtifactID: "volume", Quote: "案件线索"}}},
			{ID: "kidnap", Origin: OriginAdded, Class: ClassOrdinary},
		},
		Bindings: []Binding{
			{EventID: "case", TargetChapters: []int{21}, PlanEvidence: []Evidence{{ArtifactID: "plan-case", Quote: "案件线索"}}, BodyEvidence: []Evidence{{ArtifactID: "body-case", Quote: "案件线索"}}},
			{EventID: "kidnap", TargetChapters: []int{22}, PlanEvidence: []Evidence{{ArtifactID: "plan-added", Quote: "绑架"}}, BodyEvidence: []Evidence{{ArtifactID: "body-added", Quote: "绑匪"}}},
		},
	}
	report := Audit(input)
	for _, code := range []string{"missing_mainline_plan_binding", "missing_mainline_body_evidence", "added_event_displaces_mainline"} {
		if !hasFindingCode(report, code) {
			t.Fatalf("missing %s in %+v", code, report.Findings)
		}
	}
}

func TestAuditArcRejectsMainlineBoundToAdjacentChapters(t *testing.T) {
	input := Input{
		Mode: ModeArc,
		Artifacts: []Artifact{
			{ID: "plan-1", Kind: ArtifactTargetPlan, Chapter: 1, Text: "first meeting"},
			{ID: "body-1", Kind: ArtifactTargetChapter, Chapter: 1, Text: "They meet."},
		},
		Events: []Event{{ID: "meet", Origin: OriginSource, Class: ClassMainline, Required: true}},
		Bindings: []Binding{{
			EventID: "meet", TargetChapters: []int{1, 2},
			PlanEvidence: []Evidence{{ArtifactID: "plan-1", Quote: "first meeting"}},
			BodyEvidence: []Evidence{{ArtifactID: "body-1", Quote: "meet"}},
		}},
	}
	if report := Audit(input); !hasFindingCode(report, "duplicate_event_reuse") {
		t.Fatalf("duplicate mainline binding should fail: %+v", report.Findings)
	}
}

func TestAuditFreeIgnoresOrdinarySourceButRejectsRelationshipJump(t *testing.T) {
	base := Input{
		Mode:   ModeFree,
		Events: []Event{{ID: "old", Origin: OriginSource, Class: ClassOrdinary}},
	}
	if report := Audit(base); report.Status != "pass" {
		t.Fatalf("ordinary source deletion should pass: %+v", report.Findings)
	}

	base.Events = append(base.Events, Event{
		ID: "love", Origin: OriginTarget, Class: ClassRelationship,
		Relationship: &RelationshipChange{Pair: "林逸飞|百里冰", From: "strangers", To: "lovers", AllowedFrom: []string{"trust"}, RequiresEventIDs: []string{"meet"}},
	})
	base.Bindings = []Binding{{EventID: "love", TargetChapters: []int{2}}}
	report := Audit(base)
	if !hasFindingCode(report, "relationship_state_jump") {
		t.Fatalf("expected relationship jump: %+v", report.Findings)
	}
}

func TestEvidenceMustExistInArtifact(t *testing.T) {
	input := chapterAuditInput()
	input.Bindings[0].BodyEvidence[0].Quote = "正文里不存在"
	report := Audit(input)
	if !hasFindingCode(report, "invalid_evidence") || report.Status != "fail" {
		t.Fatalf("invalid evidence should fail: %+v", report.Findings)
	}
}

func TestInputDigestIsOrderIndependent(t *testing.T) {
	input := chapterAuditInput()
	left := ComputeInputDigest(input)
	slices.Reverse(input.Artifacts)
	slices.Reverse(input.Bindings)
	slices.Reverse(input.SourceSegments)
	if right := ComputeInputDigest(input); left != right {
		t.Fatalf("digest changed with slice order: %s != %s", left, right)
	}
}

func TestConfirmationRequiresFreshDigestAndBlockingAcknowledgement(t *testing.T) {
	input := chapterAuditInput()
	input.Bindings = input.Bindings[:1]
	report := Audit(input)
	if report.Status != "fail" || len(report.Confirmation.BlockingFindingIDs) == 0 {
		t.Fatalf("expected blocking report: %+v", report)
	}
	request := ConfirmationRequest{ReportDigest: "stale", Decision: "apply"}
	if err := ValidateConfirmationRequest(report, request); err == nil {
		t.Fatal("expected stale digest rejection")
	}
	request.ReportDigest = report.Digest
	if err := ValidateConfirmationRequest(report, request); err == nil {
		t.Fatal("expected missing acknowledgement rejection")
	}
	request.AcknowledgedFindingIDs = append([]string(nil), report.Confirmation.BlockingFindingIDs...)
	if err := ValidateConfirmationRequest(report, request); err != nil {
		t.Fatalf("valid confirmation rejected: %v", err)
	}
}

func TestConfirmationRejectsInconclusiveEvidenceOnlyReport(t *testing.T) {
	report := AuditEvidenceOnly(Input{Mode: ModeArc}, "audit_contract_unavailable", "legacy contract")
	if report.Confirmation.Required {
		t.Fatal("evidence-only report must not offer repair confirmation")
	}
	if !strings.Contains(report.Confirmation.SuggestedAction, "canonical audit contracts") {
		t.Fatalf("suggested action = %q, want canonical-contract guidance", report.Confirmation.SuggestedAction)
	}
	request := ConfirmationRequest{ReportDigest: report.Digest, Decision: "apply"}
	if err := ValidateConfirmationRequest(report, request); err == nil {
		t.Fatal("inconclusive evidence-only report must never be eligible for automatic repair")
	}
}

func chapterAuditInput() Input {
	return Input{
		Mode: ModeChapter,
		Artifacts: []Artifact{
			{ID: "plan-1", Kind: ArtifactTargetPlan, Chapter: 1, Text: "承接前半场景"},
			{ID: "body-1", Kind: ArtifactTargetChapter, Chapter: 1, Text: "前半场景在雨里结束。"},
			{ID: "plan-2", Kind: ArtifactTargetPlan, Chapter: 2, Text: "承接后半场景"},
			{ID: "body-2", Kind: ArtifactTargetChapter, Chapter: 2, Text: "后半场景从医院继续。"},
		},
		SourceSegments: []SourceSegment{
			{ID: "s1", Chapter: 1, Sequence: 1, TargetChapter: 1, FromRune: 0, ToRune: 1000, TotalRunes: 2000, MaxRunes: 1000, LongPart: true, Required: true, ContractPresent: true, EntryState: map[string]string{}, ExitState: map[string]string{}},
			{ID: "s2", Chapter: 1, Sequence: 2, TargetChapter: 2, FromRune: 1000, ToRune: 2000, TotalRunes: 2000, MaxRunes: 1000, LongPart: true, Required: true, ContractPresent: true, EntryState: map[string]string{}, ExitState: map[string]string{}},
		},
		Bindings: []Binding{
			{SourceSegmentIDs: []string{"s1"}, TargetChapters: []int{1}, PlanEvidence: []Evidence{{ArtifactID: "plan-1", Quote: "前半场景"}}, BodyEvidence: []Evidence{{ArtifactID: "body-1", Quote: "前半场景"}}},
			{SourceSegmentIDs: []string{"s2"}, TargetChapters: []int{2}, PlanEvidence: []Evidence{{ArtifactID: "plan-2", Quote: "后半场景"}}, BodyEvidence: []Evidence{{ArtifactID: "body-2", Quote: "后半场景"}}},
		},
	}
}

func hasFindingCode(report Report, code string) bool {
	for _, finding := range report.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
