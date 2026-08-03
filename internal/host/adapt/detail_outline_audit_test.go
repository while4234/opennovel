package adapt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type blockingOnceDetailAuditor struct{ calls int }

type formatRetryDetailAuditor struct{ calls int }

type alwaysBlockingDetailAuditor struct{ calls int }

func (m *alwaysBlockingDetailAuditor) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.calls++
	var payload struct {
		TargetFrom int                   `json:"target_from"`
		Artifacts  []detailAuditArtifact `json:"artifacts"`
	}
	_ = json.Unmarshal([]byte(messages[len(messages)-1].TextContent()), &payload)
	response := detailAuditModelResponse{Verdict: "fail", Summary: "same blocking issue"}
	for _, artifact := range payload.Artifacts {
		if artifact.ID != "candidate" || artifact.Text == "" {
			continue
		}
		quote := string([]rune(artifact.Text)[:1])
		response.Findings = []detailAuditModelFinding{{
			Code: "semantic_mismatch", Severity: "blocking", Message: "candidate still conflicts with source",
			RepairInstruction: "regenerate the complete batch", TargetChapters: []int{payload.TargetFrom},
			Evidence: []domain.AdaptationDetailAuditEvidence{{ArtifactID: artifact.ID, ArtifactSHA256: artifact.SHA256, Quote: quote, FromRune: 0, ToRune: 1}},
		}}
		break
	}
	data, _ := json.Marshal(response)
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(string(data))}, Timestamp: time.Now(),
	}}, nil
}

func (m *formatRetryDetailAuditor) Generate(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.calls++
	text := `{"verdict":"pass","summary":"broken" 中文}`
	if m.calls > 1 {
		text = `{"verdict":"pass","summary":"clean retry passed","findings":[]}`
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(text)}, Timestamp: time.Now(),
	}}, nil
}

func (m *blockingOnceDetailAuditor) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.calls++
	response := detailAuditModelResponse{Verdict: "pass", Summary: "re-audit passed"}
	if m.calls == 1 {
		var payload struct {
			TargetFrom int                   `json:"target_from"`
			Artifacts  []detailAuditArtifact `json:"artifacts"`
		}
		_ = json.Unmarshal([]byte(messages[len(messages)-1].TextContent()), &payload)
		for _, artifact := range payload.Artifacts {
			if artifact.ID != "candidate" || artifact.Text == "" {
				continue
			}
			quote := string([]rune(artifact.Text)[:1])
			response = detailAuditModelResponse{Verdict: "fail", Summary: "blocking issue", Findings: []detailAuditModelFinding{{
				Code: "semantic_mismatch", Severity: "blocking", Message: "candidate conflicts with source",
				RepairInstruction: "regenerate the complete batch", TargetChapters: []int{payload.TargetFrom},
				Evidence: []domain.AdaptationDetailAuditEvidence{{ArtifactID: artifact.ID, ArtifactSHA256: artifact.SHA256, Quote: quote, FromRune: 0, ToRune: 1}},
			}}}
			break
		}
	}
	data, _ := json.Marshal(response)
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(string(data))}, Timestamp: time.Now(),
	}}, nil
}

func TestValidateArcBatchEventCoverageAllowsStableSupportingAndRejectsInvented(t *testing.T) {
	batch := plannerSkeletonBatch{
		TargetFrom: 1, TargetTo: 1,
		MainlineEventIDs: []string{"src-main"},
		AllowedEventIDs:  []string{"src-main", "src-support"},
	}
	chapters := []domain.AdaptationChapterPlan{{Chapter: 1, EventIDs: []string{"src-main", "src-support"}}}
	if err := validateArcBatchEventCoverage(chapters, batch); err != nil {
		t.Fatalf("stable supporting event should be accepted: %v", err)
	}
	chapters[0].EventIDs = append(chapters[0].EventIDs, "src-invented")
	if err := validateArcBatchEventCoverage(chapters, batch); detailRepairErrorCategory(err) != "foreign_event_id" {
		t.Fatalf("invented event category=%q err=%v", detailRepairErrorCategory(err), err)
	}
}

func TestDetailRepairCategoryFingerprintIgnoresChangingForeignEventID(t *testing.T) {
	first := fmt.Errorf("arc mainline event EVT-NEW-A is not assigned to detail batch 203-205; remove it from event_ids")
	second := fmt.Errorf("arc mainline event EVT-NEW-B is not assigned to detail batch 203-205; remove it from event_ids")
	if detailRepairErrorCategory(first) != "foreign_event_id" || detailRepairErrorCategory(second) != "foreign_event_id" {
		t.Fatalf("foreign IDs should share one category: %q %q", detailRepairErrorCategory(first), detailRepairErrorCategory(second))
	}
	if detailOutlineTextSHA256(first.Error()) == detailOutlineTextSHA256(second.Error()) {
		t.Fatal("exact fingerprints should still distinguish concrete IDs")
	}
	firstCategory := detailOutlineTextSHA256("foreign_event_id:203-205")
	secondCategory := detailOutlineTextSHA256("foreign_event_id:203-205")
	if firstCategory != secondCategory {
		t.Fatal("category fingerprint should be stable across concrete IDs")
	}
}

func TestPersistDetailRepairFailureCountsAcrossCandidates(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedDirectConfirmedAdaptationTargetFoundation(t, st, 1, "persist repair failure")
	runtime := &domain.AdaptationProposalRuntime{Version: adaptationProposalRuntimeVersion}
	batch := plannerSkeletonBatch{Index: 54, TargetFrom: 203, TargetTo: 205, SourceFrom: 100, SourceTo: 101}
	observer := persistDetailRepairFailureObserver(Deps{Store: st}, runtime, batch)
	if err := observer(nil, fmt.Errorf("event EVT-A is not assigned to detail batch 203-205")); err != nil {
		t.Fatalf("persist first: %v", err)
	}
	first := plannerRuntimeRawBatch(runtime, batch)
	if first == nil || first.Audit == nil {
		t.Fatal("first structural failure was not persisted")
	}
	firstExact := first.Audit.ExactErrorFingerprint
	if err := observer(nil, fmt.Errorf("event EVT-B is not assigned to detail batch 203-205")); err != nil {
		t.Fatalf("persist second: %v", err)
	}
	second := plannerRuntimeRawBatch(runtime, batch)
	if second.Audit.RepairAttempts != 2 || second.Audit.ConsecutiveCategoryFailures != 2 {
		t.Fatalf("persisted audit=%+v, want two consecutive category failures", second.Audit)
	}
	if second.Audit.ExactErrorFingerprint == firstExact {
		t.Fatal("changing concrete ID should change exact fingerprint")
	}
	if second.Audit.LastErrorCategory != "foreign_event_id" {
		t.Fatalf("category=%q", second.Audit.LastErrorCategory)
	}
}

func TestVerifiedDetailAuditFindingsRequireExactEvidenceAndTarget(t *testing.T) {
	artifact := detailAuditArtifact{ID: "candidate", Text: "甲乙丙", TargetChapters: []int{7}}
	artifact.SHA256 = detailOutlineTextSHA256(artifact.Text)
	valid := detailAuditModelFinding{
		Code: "timeline", Severity: "blocking", Message: "顺序冲突", RepairInstruction: "调整顺序", TargetChapters: []int{7},
		Evidence: []domain.AdaptationDetailAuditEvidence{{ArtifactID: "candidate", ArtifactSHA256: artifact.SHA256, Quote: "乙", FromRune: 1, ToRune: 2}},
	}
	findings := verifiedDetailAuditFindings([]detailAuditModelFinding{valid}, []detailAuditArtifact{artifact}, 7, 8)
	if len(findings) != 1 || !findings[0].Blocking {
		t.Fatalf("valid evidence should remain blocking: %+v", findings)
	}
	invalid := valid
	invalid.Evidence[0].Quote = "丙"
	findings = verifiedDetailAuditFindings([]detailAuditModelFinding{invalid}, []detailAuditArtifact{artifact}, 7, 8)
	if findings[0].Blocking || findings[0].Severity != "warning" {
		t.Fatalf("invalid evidence should be downgraded: %+v", findings[0])
	}
}

func TestDecodeDetailOutlineAuditResponseReadsFirstCompleteJSONObject(t *testing.T) {
	response, err := decodeDetailOutlineAuditResponse("审核结果：\n" +
		`{"verdict":"pass","summary":"accepted","findings":[]}` +
		"\n补充说明 {\"unrelated\":true}")
	if err != nil {
		t.Fatalf("decode first JSON object: %v", err)
	}
	if response.Verdict != "pass" || response.Summary != "accepted" {
		t.Fatalf("response=%+v", response)
	}
}

func TestCallDetailOutlineAuditorRetriesMalformedResponseWithCleanContext(t *testing.T) {
	auditor := &formatRetryDetailAuditor{}
	var progress []string
	response, err := callDetailOutlineAuditor(
		context.Background(), Deps{StructureRepairMaxAttempts: 2, ModelCallMaxAttempts: 1}, auditor,
		"parent", []detailAuditArtifact{{ID: "candidate", Text: "{}"}}, 43, 50,
		func(_ Stage, _, _ int, message string, _ error) { progress = append(progress, message) }, 14, 370,
	)
	if err != nil {
		t.Fatalf("callDetailOutlineAuditor: %v", err)
	}
	if auditor.calls != 2 || response.Verdict != "pass" {
		t.Fatalf("calls=%d response=%+v", auditor.calls, response)
	}
	if joined := strings.Join(progress, "\n"); !strings.Contains(joined, "已丢弃该返回，使用干净上下文重试 2/2") {
		t.Fatalf("progress did not explain clean retry: %s", joined)
	}
}

func TestDetailOutlineAuditOutputMaxTokensExpandsGlobalReview(t *testing.T) {
	if got := detailOutlineAuditOutputMaxTokens("batch"); got != detailOutlineAuditMaxTokens {
		t.Fatalf("batch audit max tokens=%d, want %d", got, detailOutlineAuditMaxTokens)
	}
	if got := detailOutlineAuditOutputMaxTokens("global"); got != detailOutlineGlobalAuditMaxTokens {
		t.Fatalf("global audit max tokens=%d, want %d", got, detailOutlineGlobalAuditMaxTokens)
	}
	if detailOutlineGlobalAuditMaxTokens <= detailOutlineAuditMaxTokens {
		t.Fatalf("global audit max tokens=%d must exceed scoped audit max tokens=%d", detailOutlineGlobalAuditMaxTokens, detailOutlineAuditMaxTokens)
	}
}

func TestLayeredDetailAuditDigestRequiresEveryBatchAndGlobalCheckpoint(t *testing.T) {
	passed := func(signature string) *domain.AdaptationDetailBatchAudit {
		return &domain.AdaptationDetailBatchAudit{
			Version: domain.AdaptationDetailAuditVersion, Status: domain.AdaptationDetailAuditPassed,
			ContentSignature: signature, DeterministicPassed: true, SemanticPassed: true,
		}
	}
	runtime := &domain.AdaptationProposalRuntime{
		CompletedBatches: []domain.AdaptationProposalRuntimeBatch{{TargetFrom: 1, TargetTo: 2, Audit: passed("a")}},
		AuditCheckpoints: []domain.AdaptationDetailAuditCheckpoint{{
			Version: domain.AdaptationDetailAuditVersion, Kind: "global", ID: "global-outline", Status: domain.AdaptationDetailAuditPassed,
		}},
	}
	if digest := layeredDetailAuditDigest(runtime, 1); digest == "" {
		t.Fatal("complete layered audit should produce a digest")
	}
	runtime.CompletedBatches[0].Audit.Status = domain.AdaptationDetailAuditPending
	if digest := layeredDetailAuditDigest(runtime, 1); digest != "" {
		t.Fatalf("pending batch produced digest %q", digest)
	}
}

func TestDetailBatchAuditArtifactsExcludeOutOfRangeReports(t *testing.T) {
	reports := make([]domain.AdaptationSourceReport, 0, 100)
	for chapter := 1; chapter <= 100; chapter++ {
		reports = append(reports, domain.AdaptationSourceReport{Chapter: chapter, Title: fmt.Sprintf("source-%03d", chapter), Summary: strings.Repeat("摘要", 200)})
	}
	batch := plannerSkeletonBatch{TargetFrom: 1, TargetTo: 1, SourceFrom: 50, SourceTo: 50}
	artifacts := detailBatchAuditArtifacts(
		ProposalOptions{Granularity: domain.AdaptationGranularityArc}, reports,
		plannerSkeleton{Granularity: domain.AdaptationGranularityArc, Batches: []plannerSkeletonBatch{batch}}, batch, nil,
		plannerBatchPlans(1, 1, 50, 50),
	)
	var sourceText string
	for _, artifact := range artifacts {
		if artifact.ID == "source" {
			sourceText = artifact.Text
		}
	}
	if !strings.Contains(sourceText, "source-050") || strings.Contains(sourceText, "source-049") || strings.Contains(sourceText, "source-051") {
		t.Fatalf("audit source artifact was not range-scoped: %s", sourceText)
	}
}

func TestAuditAndRepairDetailBatchOnlyCompletesAfterSemanticReaudit(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedDirectConfirmedAdaptationTargetFoundation(t, st, 1, "semantic detail re-audit")
	batch := plannerSkeletonBatch{Index: 1, TargetFrom: 1, TargetTo: 1, SourceFrom: 1, SourceTo: 1}
	skeleton := plannerSkeleton{Granularity: domain.AdaptationGranularityFree, TargetChapterCount: 1, Batches: []plannerSkeletonBatch{batch}}
	opts := ProposalOptions{Brief: "free rewrite", Granularity: domain.AdaptationGranularityFree, RewritePolicy: domain.AdaptationRewriteFullRewrite}
	manifest := &domain.AdaptationSourceManifest{ChapterCount: 1, Chapters: []domain.AdaptationSource{{Chapter: 1, Runes: 1000}}}
	candidate := plannerBatchPlans(1, 1, 1, 1)
	planner := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: plannerBatchProposalJSON(1, 1, 1, 1)}}}
	auditor := &blockingOnceDetailAuditor{}
	deps := Deps{Store: st, LLM: planner, Auditor: auditor, AdaptationOutlineAuditRetryMaxAttempts: 2, StructureRepairMaxAttempts: 2}
	runtime := newPlannerProposalRuntime(opts, manifest, 1)
	validate := plannerBatchChapterValidator(opts, manifest, batch)
	var progress []string
	emit := func(_ Stage, _, _ int, message string, _ error) { progress = append(progress, message) }
	chapters, audit, err := auditAndRepairDetailBatch(
		withAdaptationPromptModeIfMissing(context.Background(), opts.Granularity), deps, opts, nil, manifest, skeleton, batch, nil, candidate, nil,
		"planner", "original", validate, runtime, emit, 1, 1, "章节详情第 1/1 批",
	)
	if err != nil {
		t.Fatalf("auditAndRepairDetailBatch: %v", err)
	}
	if len(chapters) != 1 || audit.Status != domain.AdaptationDetailAuditPassed || audit.RepairAttempts != 1 || auditor.calls != 2 {
		t.Fatalf("chapters=%+v audit=%+v auditor calls=%d", chapters, audit, auditor.calls)
	}
	joined := strings.Join(progress, "\n")
	for _, want := range []string{"审核发现 1 项阻断问题", "修复稿已生成，正在复审", "复审通过，本批次修复完成"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("progress missing %q: %s", want, joined)
		}
	}
}

func TestAuditAndRepairDetailBatchStopsAfterTwoUnchangedFindingRounds(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedDirectConfirmedAdaptationTargetFoundation(t, st, 1, "bounded unchanged repair")
	batch := plannerSkeletonBatch{Index: 1, TargetFrom: 1, TargetTo: 1, SourceFrom: 1, SourceTo: 1}
	skeleton := plannerSkeleton{Granularity: domain.AdaptationGranularityFree, TargetChapterCount: 1, Batches: []plannerSkeletonBatch{batch}}
	opts := ProposalOptions{Brief: "free rewrite", Granularity: domain.AdaptationGranularityFree, RewritePolicy: domain.AdaptationRewriteFullRewrite}
	manifest := &domain.AdaptationSourceManifest{ChapterCount: 1, Chapters: []domain.AdaptationSource{{Chapter: 1, Runes: 1000}}}
	candidate := plannerBatchPlans(1, 1, 1, 1)
	firstRepair := strings.Replace(plannerBatchProposalJSON(1, 1, 1, 1), "Harbor Cipher", "Repair One", 1)
	secondRepair := strings.Replace(plannerBatchProposalJSON(1, 1, 1, 1), "Harbor Cipher", "Repair Two", 1)
	planner := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: firstRepair}, {text: secondRepair}}}
	auditor := &alwaysBlockingDetailAuditor{}
	deps := Deps{Store: st, LLM: planner, Auditor: auditor, AdaptationOutlineAuditRetryMaxAttempts: 7, StructureRepairMaxAttempts: 7}
	runtime := newPlannerProposalRuntime(opts, manifest, 1)
	validate := plannerBatchChapterValidator(opts, manifest, batch)
	_, _, err := auditAndRepairDetailBatch(
		withAdaptationPromptModeIfMissing(context.Background(), opts.Granularity), deps, opts, nil, manifest, skeleton, batch, nil, candidate, nil,
		"planner", "original", validate, runtime, nil, 1, 1, "章节详情第 1/1 批",
	)
	if err == nil || !strings.Contains(err.Error(), "made no audit progress after 2 content repair attempts") {
		t.Fatalf("error=%v", err)
	}
	if planner.calls != 2 || auditor.calls != 3 {
		t.Fatalf("planner calls=%d auditor calls=%d, want 2/3", planner.calls, auditor.calls)
	}
}

func TestDeterministicDetailAuditRoutesDuplicateOwnershipToCurrentBatch(t *testing.T) {
	qualityErr := &AdaptationOutlineQualityError{Issues: []AdaptationOutlineQualityIssue{{
		Code: outlineQualityIssueArcDuplicateEvent, Detail: "source event src-event is bound to target chapters [461 462]",
		TargetChapter: 461, AlternativeChapters: []int{462},
	}}}
	batch := plannerSkeletonBatch{TargetFrom: 462, TargetTo: 465}
	findings := deterministicDetailAuditFindings(qualityErr, batch)
	if len(findings) != 1 {
		t.Fatalf("findings=%+v", findings)
	}
	targets := findings[0].TargetChapters
	if len(targets) != 1 || targets[0] != 462 {
		t.Fatalf("repair targets=%v, want current-batch chapter 462", targets)
	}
}

func TestResetStaleCrossBatchOwnershipAuditPreservesCandidateAndResetsOnlyOldScope(t *testing.T) {
	batch := plannerSkeletonBatch{TargetFrom: 462, TargetTo: 465}
	original := &domain.AdaptationDetailBatchAudit{
		Version: domain.AdaptationDetailAuditVersion, Status: domain.AdaptationDetailAuditRepairPending,
		RepairAttempts: 7, LastError: "old duplicate ownership scope", LastErrorCategory: "detail_contract",
		ExactErrorFingerprint: "old", CategoryFingerprint: "old", ConsecutiveCategoryFailures: 7,
		Findings: []domain.AdaptationDetailAuditFinding{{
			Code: outlineQualityIssueArcDuplicateEvent, Blocking: true, TargetChapters: []int{461},
		}},
	}
	migrated, changed := resetStaleCrossBatchOwnershipAudit(original, batch)
	if !changed {
		t.Fatal("expected old cross-batch ownership scope to migrate")
	}
	if original.RepairAttempts != 7 || original.Status != domain.AdaptationDetailAuditRepairPending {
		t.Fatalf("migration mutated persisted source audit: %+v", original)
	}
	if migrated.Status != domain.AdaptationDetailAuditPending || migrated.RepairAttempts != 0 || len(migrated.Findings) != 0 {
		t.Fatalf("migrated audit=%+v", migrated)
	}

	inScope := &domain.AdaptationDetailBatchAudit{
		Status: domain.AdaptationDetailAuditRepairPending,
		Findings: []domain.AdaptationDetailAuditFinding{{
			Code: outlineQualityIssueArcDuplicateEvent, Blocking: true, TargetChapters: []int{462},
		}},
	}
	if _, changed := resetStaleCrossBatchOwnershipAudit(inScope, batch); changed {
		t.Fatal("current-batch duplicate ownership must retain its established repair budget")
	}
}

func TestResetStaleDetailBatchContractAuditResetsOnlyChangedContract(t *testing.T) {
	batch := plannerSkeletonBatch{TargetFrom: 5, TargetTo: 5, SourceFrom: 3, SourceTo: 3, AllowedEventIDs: []string{"old-owner"}}
	chapters := []domain.AdaptationChapterPlan{{Chapter: 5, OutlineEntry: domain.OutlineEntry{Title: "chapter", CoreEvent: "event", Hook: "hook", Scenes: []string{"scene"}}}}
	audit := &domain.AdaptationDetailBatchAudit{
		Version: domain.AdaptationDetailAuditVersion, Status: domain.AdaptationDetailAuditRepairPending,
		RepairAttempts: 4, InputSignature: "legacy-contract", LastError: "duplicate binding",
		Findings: []domain.AdaptationDetailAuditFinding{{Code: outlineQualityIssueArcDuplicateEvent, Blocking: true}},
	}
	migrated, changed := resetStaleDetailBatchContractAudit(audit, ProposalOptions{Granularity: domain.AdaptationGranularityArc}, nil, batch, nil, chapters)
	if !changed || migrated.RepairAttempts != 0 || migrated.Status != domain.AdaptationDetailAuditPending || len(migrated.Findings) != 0 {
		t.Fatalf("migrated audit=%+v changed=%v", migrated, changed)
	}

	_, _, currentSignature := detailBatchAuditSignatures(ProposalOptions{Granularity: domain.AdaptationGranularityArc}, nil, batch, nil, chapters)
	audit.InputSignature = currentSignature
	if _, changed := resetStaleDetailBatchContractAudit(audit, ProposalOptions{Granularity: domain.AdaptationGranularityArc}, nil, batch, nil, chapters); changed {
		t.Fatal("matching detail contract must retain its existing repair budget")
	}
}
