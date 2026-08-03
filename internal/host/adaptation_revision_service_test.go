package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

var (
	adaptationTestVolumeID = domain.LegacyStructureID("adaptation-revision-test", domain.StructureKindVolume, "volume/1")
	adaptationTestChapter1 = domain.LegacyStructureID("adaptation-revision-test", domain.StructureKindChapter, "chapter/1")
	adaptationTestChapter2 = domain.LegacyStructureID("adaptation-revision-test", domain.StructureKindChapter, "chapter/2")
	adaptationTestAddedID  = domain.LegacyStructureID("adaptation-revision-test", domain.StructureKindChapter, "chapter/added")
)

func TestAdaptationRevisionServiceRunsFourStagesAndThreeGranularities(t *testing.T) {
	stages := []domain.ManuscriptStage{domain.ManuscriptStageProposalComplete, domain.ManuscriptStageOutlineComplete, domain.ManuscriptStageWriting, domain.ManuscriptStageComplete}
	granularities := []string{domain.AdaptationGranularityChapter, domain.AdaptationGranularityArc, domain.AdaptationGranularityFree}
	for _, stage := range stages {
		for _, granularity := range granularities {
			t.Run(string(stage)+"/"+granularity, func(t *testing.T) {
				st, base, candidate := seedAdaptationRevisionProject(t, stage, granularity, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append an original bridge chapter", Candidate: candidate}, "preview")
				if err != nil {
					t.Fatal(err)
				}
				if previewed.Preview.Stage != stage || previewed.Preview.BasePlanSignature != adaptationPlanSignature(base) {
					t.Fatalf("store-derived preview drifted: %+v", previewed.Preview)
				}
				session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
				published, err := service.Publish(*previewed.Preview, session, "publish")
				if err != nil {
					t.Fatal(err)
				}
				if published.Stage != domain.RevisionStageCompleted {
					t.Fatalf("published revision=%+v", published)
				}
				formal, err := st.Adaptation.LoadPlan()
				if err != nil || formal == nil || len(formal.Chapters) != 3 || formal.Chapters[2].ID != adaptationTestAddedID {
					t.Fatalf("formal plan was not atomically published: plan=%+v err=%v", formal, err)
				}
			})
		}
	}
}

func TestAdaptationRevisionServiceTreatsDetailsGeneratingWithOutlineProgressAsProposalComplete(t *testing.T) {
	for _, granularity := range []string{domain.AdaptationGranularityChapter, domain.AdaptationGranularityArc, domain.AdaptationGranularityFree} {
		t.Run(granularity, func(t *testing.T) {
			st, _, _ := seedAdaptationRevisionProject(t, domain.ManuscriptStageProposalComplete, granularity, false)
			if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageDetailsGenerating, -1); err != nil {
				t.Fatal(err)
			}
			stage, err := NewAdaptationRevisionService(st).CurrentManuscriptStage()
			if err != nil || stage != domain.ManuscriptStageProposalComplete {
				t.Fatalf("details-generating production state stage=%q err=%v", stage, err)
			}
		})
	}
}

func TestAdaptationRevisionServicePersistsBatchFailurePauseAndRestart(t *testing.T) {
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchFail, "adaptation-batch-001", "context overflow"); err != nil {
		t.Fatal(err)
	}
	paused, err := service.Pause(session, "pause")
	if err != nil {
		t.Fatal(err)
	}

	restarted := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
	resumed, err := restarted.Resume(paused, "resume-session")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err = restarted.RunBatchCommand(resumed.ID, domain.AdaptationRevisionBatchResume, "adaptation-batch-001", "")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.BatchPlan.Batches[0].Status != domain.BatchStatusPending || runtime.BatchPlan.Batches[0].Attempts != 1 || runtime.Paused {
		t.Fatalf("durable batch checkpoint drifted after restart: %+v", runtime)
	}
}

func TestAdaptationRevisionServiceReplaysRuntimeAndTerminalSideEffects(t *testing.T) {
	t.Run("preview detail and structure approval preserve progressed runtime", func(t *testing.T) {
		st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		service := NewAdaptationRevisionService(st)
		request := AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}
		previewed, err := service.Preview(request, "preview-replay")
		if err != nil {
			t.Fatal(err)
		}
		session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
		if err != nil {
			t.Fatal(err)
		}
		session, err = service.SubmitStructureCandidate(*previewed.Preview, session, "structure")
		if err != nil {
			t.Fatal(err)
		}
		completeAdaptationRuntime(t, service, session.ID)
		evidence := adaptationPassingEvidence(session)
		audited, err := service.RecordAuditSet(session, evidence, "structure-audit")
		if err != nil {
			t.Fatal(err)
		}
		approved, err := service.ApproveStage(audited, "structure-approve-replay")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.RunBatchCommand(approved.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
			t.Fatal(err)
		}
		beforeApprovalReplay, _ := st.Adaptation.LoadRevisionRuntime()
		service.saveRevisionRuntime = func(domain.AdaptationRevisionRuntime) error { return errors.New("replay must not persist runtime") }
		if replay, err := service.ApproveStage(audited, "structure-approve-replay"); err != nil || !reflect.DeepEqual(replay, approved) {
			t.Fatalf("structure approval replay=%+v err=%v", replay, err)
		}
		if replay, err := service.Preview(request, "preview-replay"); err != nil || !reflect.DeepEqual(replay, previewed) {
			t.Fatalf("preview replay=%+v err=%v", replay, err)
		}
		afterApprovalReplay, _ := st.Adaptation.LoadRevisionRuntime()
		if !reflect.DeepEqual(beforeApprovalReplay, afterApprovalReplay) {
			t.Fatal("replay reset progressed BatchPlan")
		}
		service.saveRevisionRuntime = nil
		detailed, err := service.SubmitDetailedOutlineCandidate(candidate, approved, "details-replay")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.RunBatchCommand(detailed.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
			t.Fatal(err)
		}
		beforeDetailReplay, _ := st.Adaptation.LoadRevisionRuntime()
		service.saveRevisionRuntime = func(domain.AdaptationRevisionRuntime) error {
			return errors.New("replay must not replace details runtime")
		}
		if replay, err := service.SubmitDetailedOutlineCandidate(candidate, approved, "details-replay"); err != nil || !reflect.DeepEqual(replay, detailed) {
			t.Fatalf("detail replay=%+v err=%v", replay, err)
		}
		afterDetailReplay, _ := st.Adaptation.LoadRevisionRuntime()
		if !reflect.DeepEqual(beforeDetailReplay, afterDetailReplay) {
			t.Fatal("detail replay replaced progressed BatchPlan")
		}
	})

	t.Run("cancel and publish replay after runtime is gone", func(t *testing.T) {
		st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		service := NewAdaptationRevisionService(st)
		previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "cancel", Candidate: candidate}, "cancel-preview")
		if err != nil {
			t.Fatal(err)
		}
		cancelled, err := service.Cancel(previewed.Session, "cancel-replay")
		if err != nil {
			t.Fatal(err)
		}
		service.clearRevisionRuntime = func(string) error { return errors.New("replay must not clean runtime") }
		if replay, err := service.Cancel(previewed.Session, "cancel-replay"); err != nil || !reflect.DeepEqual(replay, cancelled) {
			t.Fatalf("cancel replay=%+v err=%v", replay, err)
		}

		st, _, candidate = seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		service = NewAdaptationRevisionService(st)
		previewed, err = service.Preview(AdaptationRevisionPreviewRequest{Intent: "publish", Candidate: candidate}, "publish-preview")
		if err != nil {
			t.Fatal(err)
		}
		session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
		published, err := service.Publish(*previewed.Preview, session, "publish-replay")
		if err != nil {
			t.Fatal(err)
		}
		service.clearRevisionRuntime = func(string) error { return errors.New("replay must not clean runtime") }
		if replay, err := service.Publish(*previewed.Preview, session, "publish-replay"); err != nil || !reflect.DeepEqual(replay, published) {
			t.Fatalf("publish replay=%+v err=%v", replay, err)
		}
	})
}

func TestAdaptationRevisionNonPublishOperationIdentityMatrix(t *testing.T) {
	t.Run("staged operations", func(t *testing.T) {
		st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		service := NewAdaptationRevisionService(st)
		previewed, err := service.Preview(AdaptationRevisionPreviewRequest{
			Intent: "exercise durable non-publish identities", Candidate: candidate, RequireAddedProse: true,
		}, "identity-preview")
		if err != nil {
			t.Fatal(err)
		}
		session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "identity-impact")
		if err != nil {
			t.Fatal(err)
		}
		session, err = service.SubmitStructureCandidate(*previewed.Preview, session, "identity-structure")
		if err != nil {
			t.Fatal(err)
		}
		completeAdaptationRuntime(t, service, session.ID)
		evidence := adaptationPassingEvidence(session)
		session, err = service.RecordAuditSet(session, evidence, "identity-structure-audit")
		if err != nil {
			t.Fatal(err)
		}
		session, err = service.ApproveStage(session, "identity-structure-approve")
		if err != nil {
			t.Fatal(err)
		}
		session, err = service.SubmitDetailedOutlineCandidate(candidate, session, "identity-details")
		if err != nil {
			t.Fatal(err)
		}
		completeAdaptationRuntime(t, service, session.ID)
		session, err = service.RecordAuditSet(session, adaptationPassingEvidence(session), "identity-details-audit")
		if err != nil {
			t.Fatal(err)
		}
		session, err = service.ApproveStage(session, "identity-details-approve")
		if err != nil {
			t.Fatal(err)
		}
		if adaptationServiceApprovalStage(*session) == domain.AdaptationApprovalProse {
			session, err = service.SubmitProseReworkCandidate(session, "identity-prose")
			if err != nil {
				t.Fatal(err)
			}
			session, err = service.RecordAuditSet(session, adaptationPassingEvidence(session), "identity-prose-audit")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.ApproveStage(session, "identity-prose-approve"); err != nil {
				t.Fatal(err)
			}
		}
		for key, operation := range map[string]string{
			"identity-preview": "preview", "identity-impact": "approve_impact",
			"identity-structure": "submit_structure", "identity-structure-audit": "record_audit",
			"identity-structure-approve": "approve_stage", "identity-details": "submit_details",
			"identity-details-audit": "record_audit", "identity-details-approve": "approve_stage",
			"identity-prose": "submit_prose_intents", "identity-prose-audit": "record_audit",
			"identity-prose-approve": "approve_stage",
		} {
			assertAdaptationNonPublishReceiptIdentity(t, st, key, operation)
		}
	})

	t.Run("feedback pause resume fail cancel", func(t *testing.T) {
		cases := []struct {
			name       string
			operations func(*AdaptationRevisionService, *AdaptationRevisionPreview) map[string]string
		}{
			{name: "feedback", operations: func(service *AdaptationRevisionService, previewed *AdaptationRevisionPreview) map[string]string {
				session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "feedback-impact")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.SubmitFeedback(session, session.Impact.Signature, "revise candidate", "identity-feedback"); err != nil {
					t.Fatal(err)
				}
				return map[string]string{"identity-feedback": "feedback"}
			}},
			{name: "pause-resume", operations: func(service *AdaptationRevisionService, previewed *AdaptationRevisionPreview) map[string]string {
				paused, err := service.Pause(previewed.Session, "identity-pause")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.Resume(paused, "identity-resume"); err != nil {
					t.Fatal(err)
				}
				return map[string]string{"identity-pause": "pause", "identity-resume": "resume"}
			}},
			{name: "fail", operations: func(service *AdaptationRevisionService, previewed *AdaptationRevisionPreview) map[string]string {
				if _, err := service.Fail(previewed.Session, "durable failure", "identity-fail"); err != nil {
					t.Fatal(err)
				}
				return map[string]string{"identity-fail": "fail"}
			}},
			{name: "cancel", operations: func(service *AdaptationRevisionService, previewed *AdaptationRevisionPreview) map[string]string {
				if _, err := service.Cancel(previewed.Session, "identity-cancel"); err != nil {
					t.Fatal(err)
				}
				return map[string]string{"identity-cancel": "cancel"}
			}},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: test.name, Candidate: candidate}, "setup-"+test.name)
				if err != nil {
					t.Fatal(err)
				}
				for key, operation := range test.operations(service, previewed) {
					assertAdaptationNonPublishReceiptIdentity(t, st, key, operation)
				}
			})
		}
	})

	if _, _, err := NewAdaptationRevisionService(&storepkg.Store{}).LoadCommandReceipt(AdaptationRevisionCommandReceiptRequest{Action: "unknown"}, "unknown"); err == nil {
		t.Fatal("unknown non-publish operation was accepted")
	}
}

func assertAdaptationNonPublishReceiptIdentity(t *testing.T, st *storepkg.Store, key, operation string) {
	t.Helper()
	servicePath := filepath.Join(st.Dir(), "meta", "adaptation", "revision_service_receipts.json")
	serviceData, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatal(err)
	}
	var serviceState map[string]any
	if err := json.Unmarshal(serviceData, &serviceState); err != nil {
		t.Fatal(err)
	}
	serviceReceipt, ok := serviceState["receipts"].(map[string]any)[key].(map[string]any)
	if !ok || serviceReceipt["operation"] != operation {
		t.Fatalf("service receipt %q/%q is absent", key, operation)
	}
	serviceFingerprint := serviceReceipt["fingerprint"].(string)

	statePath := filepath.Join(st.Dir(), "meta", "revisions", "state.json")
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var revisionState map[string]any
	if err := json.Unmarshal(stateData, &revisionState); err != nil {
		t.Fatal(err)
	}
	internal := revisionState["receipts"].(map[string]any)[key].(map[string]any)
	if internal["service_operation"] != operation || internal["service_fingerprint"] != serviceFingerprint {
		t.Fatalf("internal receipt %q did not atomically bind service identity", key)
	}
	internalFingerprint := internal["fingerprint"].(string)
	assertLoad := func(store *storepkg.Store, fingerprint string) error {
		if operation == "preview" {
			var result *AdaptationRevisionPreview
			_, err := store.LoadVerifiedAdaptationRevisionServiceReceipt(key, operation, fingerprint, &result)
			return err
		}
		var result *domain.RevisionSession
		_, err := store.LoadVerifiedAdaptationRevisionServiceReceipt(key, operation, fingerprint, &result)
		return err
	}
	if err := assertLoad(st, serviceFingerprint); err != nil {
		t.Fatalf("exact service receipt %q failed verification: %v", key, err)
	}
	if err := assertLoad(storepkg.NewStore(st.Dir()), serviceFingerprint); err != nil {
		t.Fatalf("restarted exact service receipt %q failed verification: %v", key, err)
	}

	changedFingerprint := strings.Repeat("f", 64)
	serviceReceipt["fingerprint"] = changedFingerprint
	writeJSONTestFile(t, servicePath, serviceState)
	if err := assertLoad(storepkg.NewStore(st.Dir()), changedFingerprint); err == nil {
		t.Fatalf("coherent outer fingerprint substitution for %q was accepted", key)
	}
	if err := os.WriteFile(servicePath, serviceData, 0o644); err != nil {
		t.Fatal(err)
	}

	serviceReceipt["fingerprint"] = changedFingerprint
	internal["service_fingerprint"] = changedFingerprint
	writeJSONTestFile(t, servicePath, serviceState)
	writeJSONTestFile(t, statePath, revisionState)
	if err := assertLoad(storepkg.NewStore(st.Dir()), changedFingerprint); err == nil {
		t.Fatalf("paired outer/internal service fingerprint substitution for %q was accepted", key)
	}
	if err := os.WriteFile(servicePath, serviceData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, stateData, 0o644); err != nil {
		t.Fatal(err)
	}

	tamperedState := []byte(strings.Replace(string(stateData), internalFingerprint, strings.Repeat("e", 64), 1))
	if err := os.WriteFile(statePath, tamperedState, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := assertLoad(storepkg.NewStore(st.Dir()), serviceFingerprint); err == nil {
		t.Fatalf("internal fingerprint substitution for %q was accepted", key)
	}
	if err := os.WriteFile(statePath, stateData, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAdaptationRevisionReceiptFailureRollsBackAndRestartsEveryTransition(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) error)
	}{
		{
			name: "preview",
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) error) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				request := AdaptationRevisionPreviewRequest{Intent: "receipt failure preview", Candidate: candidate}
				return st, NewAdaptationRevisionService(st), func(service *AdaptationRevisionService) error {
					_, err := service.Preview(request, "receipt-failure-preview")
					return err
				}
			},
		},
		{
			name: "structure approval",
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) error) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "receipt failure structure", Candidate: candidate}, "receipt-structure-preview")
				if err != nil {
					t.Fatal(err)
				}
				session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "receipt-structure-impact")
				if err != nil {
					t.Fatal(err)
				}
				session, err = service.SubmitStructureCandidate(*previewed.Preview, session, "receipt-structure-submit")
				if err != nil {
					t.Fatal(err)
				}
				completeAdaptationRuntime(t, service, session.ID)
				audited, err := service.RecordAuditSet(session, adaptationPassingEvidence(session), "receipt-structure-audit")
				if err != nil {
					t.Fatal(err)
				}
				return st, service, func(service *AdaptationRevisionService) error {
					_, err := service.ApproveStage(audited, "receipt-failure-structure-approve")
					return err
				}
			},
		},
		{
			name: "detail submission",
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) error) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "receipt failure details", Candidate: candidate}, "receipt-details-preview")
				if err != nil {
					t.Fatal(err)
				}
				session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "receipt-details-impact")
				if err != nil {
					t.Fatal(err)
				}
				session, err = service.SubmitStructureCandidate(*previewed.Preview, session, "receipt-details-structure")
				if err != nil {
					t.Fatal(err)
				}
				completeAdaptationRuntime(t, service, session.ID)
				audited, err := service.RecordAuditSet(session, adaptationPassingEvidence(session), "receipt-details-audit")
				if err != nil {
					t.Fatal(err)
				}
				approved, err := service.ApproveStage(audited, "receipt-details-approve")
				if err != nil {
					t.Fatal(err)
				}
				return st, service, func(service *AdaptationRevisionService) error {
					_, err := service.SubmitDetailedOutlineCandidate(candidate, approved, "receipt-failure-details")
					return err
				}
			},
		},
		{
			name: "cancel",
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) error) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "receipt failure cancel", Candidate: candidate}, "receipt-cancel-preview")
				if err != nil {
					t.Fatal(err)
				}
				return st, service, func(service *AdaptationRevisionService) error {
					_, err := service.Cancel(previewed.Session, "receipt-failure-cancel")
					return err
				}
			},
		},
		{
			name: "publish",
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) error) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "receipt failure publish", Candidate: candidate}, "receipt-publish-preview")
				if err != nil {
					t.Fatal(err)
				}
				session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
				return st, service, func(service *AdaptationRevisionService) error {
					_, err := service.Publish(*previewed.Preview, session, "receipt-failure-publish")
					return err
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name+"/write failure", func(t *testing.T) {
			st, service, command := test.setup(t)
			before := adaptationRevisionProjectBytes(t, st.Dir())
			registryBefore := snapshotPublicationAuthorityRegistry(t)
			service.saveRevisionReceipt = func(string, string, string, any) error {
				return errors.New("injected receipt write failure")
			}
			if err := command(service); err == nil || !strings.Contains(err.Error(), "injected receipt write failure") {
				t.Fatalf("receipt failure was not returned: %v", err)
			}
			after := adaptationRevisionProjectBytes(t, st.Dir())
			if test.name == "publish" {
				if reflect.DeepEqual(before, after) {
					t.Fatal("committed publish was rolled back after service receipt failure")
				}
				if registryAfter := snapshotPublicationAuthorityRegistry(t); len(registryAfter) != len(registryBefore)+1 {
					t.Fatalf("committed publish authority count before=%d after=%d", len(registryBefore), len(registryAfter))
				}
				restarted := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
				if err := command(restarted); err != nil {
					t.Fatalf("committed publish receipt was not recoverable: %v", err)
				}
				return
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("receipt failure did not restore the exact pre-command project snapshot")
			}
			if registryAfter := snapshotPublicationAuthorityRegistry(t); !reflect.DeepEqual(registryBefore, registryAfter) {
				t.Fatalf("receipt failure changed external authority registry\nbefore=%+v\nafter=%+v", registryBefore, registryAfter)
			}
			restarted := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
			if err := command(restarted); err != nil {
				t.Fatalf("same command was not safely retryable after restart: %v", err)
			}
		})
		t.Run(test.name+"/interrupted before receipt", func(t *testing.T) {
			st, service, command := test.setup(t)
			before := adaptationRevisionProjectBytes(t, st.Dir())
			registryBefore := snapshotPublicationAuthorityRegistry(t)
			service.saveRevisionReceipt = func(string, string, string, any) error {
				panic("simulated process interruption before receipt")
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("command did not reach the simulated interruption")
					}
				}()
				_ = command(service)
			}()
			restarted := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
			if test.name == "publish" {
				if err := command(restarted); err != nil {
					t.Fatalf("committed interrupted publish was not recoverable: %v", err)
				}
				if after := adaptationRevisionProjectBytes(t, st.Dir()); reflect.DeepEqual(before, after) {
					t.Fatal("committed interrupted publish restored its pre-command snapshot")
				}
				if registryAfter := snapshotPublicationAuthorityRegistry(t); len(registryAfter) != len(registryBefore)+1 {
					t.Fatalf("committed interrupted publish authority count before=%d after=%d", len(registryBefore), len(registryAfter))
				}
				if err := command(NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))); err != nil {
					t.Fatalf("committed interrupted publish did not replay: %v", err)
				}
				return
			}
			restarted.saveRevisionReceipt = func(string, string, string, any) error {
				return errors.New("stop after restart recovery")
			}
			if err := command(restarted); err == nil || !strings.Contains(err.Error(), "stop after restart recovery") {
				t.Fatalf("restart did not reach the recovered command boundary: %v", err)
			}
			if after := adaptationRevisionProjectBytes(t, st.Dir()); !reflect.DeepEqual(before, after) {
				t.Fatal("restart recovery did not restore the exact pre-command project snapshot")
			}
			if registryAfter := snapshotPublicationAuthorityRegistry(t); !reflect.DeepEqual(registryBefore, registryAfter) {
				t.Fatalf("restart recovery changed external authority registry\nbefore=%+v\nafter=%+v", registryBefore, registryAfter)
			}
			if err := command(NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))); err != nil {
				t.Fatalf("same command was not retryable after interrupted-command recovery: %v", err)
			}
		})
	}
}

func TestAdaptationRevisionPreparedOwnershipExcludesNormalFlowAcrossRestart(t *testing.T) {
	t.Run("preview preparation", func(t *testing.T) {
		st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		service := NewAdaptationRevisionService(st)
		competitor := storepkg.NewStore(st.Dir())
		before := adaptationRevisionProjectBytes(t, st.Dir())
		competitorImpact, err := domain.NewRevisionImpact("competing preview revision", []domain.RevisionImpactItem{{
			ArtifactID: "chapter-1", ArtifactKind: "outline", Change: "compete",
		}})
		if err != nil {
			t.Fatal(err)
		}
		var acquireErr, revisionErr error
		service.afterCommandPrepared = func() {
			_, acquireErr = competitor.Revisions.AcquireNormalFlow("preview-race")
			_, revisionErr = competitor.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
				Intent: "competing preview", Impact: competitorImpact, IdempotencyKey: "preview-ownership-race",
			})
			panic("interrupt preview after durable preparation")
		}
		assertAdaptationCommandPanics(t, func() {
			_, _ = service.Preview(AdaptationRevisionPreviewRequest{Intent: "preview ownership race", Candidate: candidate}, "preview-ownership-race")
		})
		if !errors.Is(acquireErr, storepkg.ErrRevisionCommandInProgress) {
			t.Fatalf("normal flow entered prepared preview: %v", acquireErr)
		}
		if !errors.Is(revisionErr, storepkg.ErrRevisionCommandInProgress) {
			t.Fatalf("competing revision entered prepared preview: %v", revisionErr)
		}
		restartedStore := storepkg.NewStore(st.Dir())
		if after := adaptationRevisionProjectBytes(t, st.Dir()); !reflect.DeepEqual(before, after) {
			t.Fatal("restart did not exactly roll back interrupted preview preparation")
		}
		restarted := NewAdaptationRevisionService(restartedStore)
		previewed, err := restarted.Preview(AdaptationRevisionPreviewRequest{Intent: "preview ownership race", Candidate: candidate}, "preview-ownership-race")
		if err != nil {
			t.Fatalf("recovered preview was not replayable: %v", err)
		}
		if _, err := restarted.Cancel(previewed.Session, "preview-ownership-cleanup"); err != nil {
			t.Fatal(err)
		}
		lease, err := competitor.Revisions.AcquireNormalFlow("preview-successor")
		if err != nil {
			t.Fatalf("normal flow remained fenced after terminal receipt cleanup: %v", err)
		}
		if err := competitor.Revisions.ReleaseNormalFlow(lease.Token); err != nil {
			t.Fatal(err)
		}
	})

	for _, test := range []struct {
		name        string
		key         string
		nonterminal bool
		committed   bool
		setup       func(*testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) (*domain.RevisionSession, error))
	}{
		{
			name: "nonterminal approval before receipt", key: "approve-ownership-race", nonterminal: true,
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) (*domain.RevisionSession, error)) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "approval ownership race", Candidate: candidate}, "approval-ownership-preview")
				if err != nil {
					t.Fatal(err)
				}
				return st, service, func(service *AdaptationRevisionService) (*domain.RevisionSession, error) {
					return service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "approve-ownership-race")
				}
			},
		},
		{
			name: "cancel before receipt", key: "cancel-ownership-race",
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) (*domain.RevisionSession, error)) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "cancel ownership race", Candidate: candidate}, "cancel-ownership-preview")
				if err != nil {
					t.Fatal(err)
				}
				return st, service, func(service *AdaptationRevisionService) (*domain.RevisionSession, error) {
					return service.Cancel(previewed.Session, "cancel-ownership-race")
				}
			},
		},
		{
			name: "publish before receipt", key: "publish-ownership-race", committed: true,
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) (*domain.RevisionSession, error)) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "publish ownership race", Candidate: candidate}, "publish-ownership-preview")
				if err != nil {
					t.Fatal(err)
				}
				session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
				return st, service, func(service *AdaptationRevisionService) (*domain.RevisionSession, error) {
					return service.Publish(*previewed.Preview, session, "publish-ownership-race")
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, service, command := test.setup(t)
			competitor := storepkg.NewStore(st.Dir())
			before := adaptationRevisionProjectBytes(t, st.Dir())
			var acquireErr, mutationErr error
			service.saveRevisionReceipt = func(_ string, _ string, _ string, result any) error {
				_, acquireErr = competitor.Revisions.AcquireNormalFlow(test.name)
				resultSession, ok := result.(*domain.RevisionSession)
				if !ok || resultSession == nil {
					t.Fatalf("prepared command result = %T, want revision session", result)
				}
				if test.nonterminal {
					policy, _, policyErr := service.boundPolicy(resultSession.ID)
					if policyErr != nil {
						t.Fatal(policyErr)
					}
					_, mutationErr = competitor.Revisions.Pause(policy, storepkg.RevisionMutationInput{
						SessionID: resultSession.ID, ExpectedRevision: resultSession.Revision, IdempotencyKey: test.key,
					})
				} else {
					_, mutationErr = competitor.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
						Intent: "terminal same-key competitor", Impact: adaptationStoreFenceImpactForHost(t), IdempotencyKey: test.key,
					})
				}
				panic("interrupt terminal command before durable receipt")
			}
			assertAdaptationCommandPanics(t, func() { _, _ = command(service) })
			if !errors.Is(acquireErr, storepkg.ErrRevisionCommandInProgress) &&
				!(test.nonterminal && errors.Is(acquireErr, storepkg.ErrActiveRevisionBlocksNormalFlow)) {
				t.Fatalf("normal flow entered terminal command before receipt: %v", acquireErr)
			}
			if !errors.Is(mutationErr, storepkg.ErrRevisionCommandInProgress) {
				t.Fatalf("same-key direct mutation entered prepared command: %v", mutationErr)
			}
			restartedStore := storepkg.NewStore(st.Dir())
			after := adaptationRevisionProjectBytes(t, st.Dir())
			if test.committed {
				if reflect.DeepEqual(before, after) {
					t.Fatal("restart recovery rolled back a committed terminal publication")
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatal("restart recovery overwrote or failed to restore the exact terminal-command snapshot")
			}
			result, err := command(NewAdaptationRevisionService(restartedStore))
			if err != nil {
				t.Fatalf("terminal command was not replayable after recovery: %v", err)
			}
			replay, err := command(NewAdaptationRevisionService(storepkg.NewStore(st.Dir())))
			if err != nil || !reflect.DeepEqual(replay, result) {
				t.Fatalf("durable terminal receipt did not replay: replay=%+v result=%+v err=%v", replay, result, err)
			}
			if test.nonterminal {
				successorService := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
				policy, _, policyErr := successorService.boundPolicy(result.ID)
				if policyErr != nil {
					t.Fatal(policyErr)
				}
				paused, pauseErr := competitor.Revisions.Pause(policy, storepkg.RevisionMutationInput{
					SessionID: result.ID, ExpectedRevision: result.Revision, IdempotencyKey: test.key + "-successor",
				})
				if pauseErr != nil {
					t.Fatalf("successor mutation remained fenced after nonterminal receipt cleanup: %v", pauseErr)
				}
				if _, err := successorService.Cancel(paused, test.key+"-cleanup"); err != nil {
					t.Fatal(err)
				}
				return
			}
			lease, err := competitor.Revisions.AcquireNormalFlow(test.name + " successor")
			if err != nil {
				t.Fatalf("normal flow remained fenced after terminal receipt cleanup: %v", err)
			}
			if err := competitor.Revisions.ReleaseNormalFlow(lease.Token); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdaptationRevisionPreparedOwnerGuardsTerminalReceiptAndRuntime(t *testing.T) {
	st, base, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "guard terminal durability", Candidate: candidate}, "durability-preview")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := st.Adaptation.LoadRevisionRuntime()
	if err != nil || runtime == nil {
		t.Fatalf("preview runtime missing: runtime=%+v err=%v", runtime, err)
	}
	competitor := storepkg.NewStore(st.Dir())
	otherProject := storepkg.NewStore(t.TempDir())
	formalSnapshot, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		t.Fatalf("load progress: progress=%+v err=%v", progress, err)
	}
	layered := preparedOwnerLayeredFixture("host-formal-owner")
	var staleOwner *storepkg.RevisionStore
	if err := st.WithPreparedAdaptationRevisionCommand("host-stale-formal", "publish", "host-stale-formal-fingerprint", func(owner *storepkg.RevisionStore) error {
		staleOwner = owner
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	key := "guarded-cancel"
	request := adaptationCommandReceiptRequest("cancel", previewed.Session.Revision)
	operation, payload, err := adaptationRevisionCommandReceiptIdentity(request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := domain.ContentSignature(encoded)
	var receiptErr, saveErr, clearErr error
	var formalErrors []error
	var formalBytesBefore, formalBytesAfter map[string][]byte
	service.SetCommandPreparedHookForTesting(func() {
		formalBytesBefore = adaptationRevisionProjectBytes(t, st.Dir())
		receiptErr = competitor.SaveAdaptationRevisionServiceReceipt(competitor.Revisions, key, operation, fingerprint, previewed.Session)
		corrupted := *runtime
		corrupted.Paused = true
		saveErr = competitor.SaveAdaptationRevisionRuntime(competitor.Revisions, corrupted)
		clearErr = competitor.ClearAdaptationRevisionRuntime(competitor.Revisions, runtime.SessionID)
		attempts := []func() error{
			func() error {
				return competitor.SaveAdaptationPlanForRevision(competitor.Revisions, candidate, previewed.Session.ID)
			},
			func() error {
				return competitor.RestoreAdaptationPlanForRevision(competitor.Revisions, base, previewed.Session.ID)
			},
			func() error {
				return competitor.RestoreAdaptationFormalSnapshot(competitor.Revisions, formalSnapshot, previewed.Session.ID)
			},
			func() error {
				return competitor.ClearAdaptationRevisionAudits(competitor.Revisions, previewed.Session.ID)
			},
			func() error {
				return competitor.SaveAdaptationRevisionProgress(competitor.Revisions, progress, previewed.Session.ID)
			},
			func() error {
				return st.SaveAdaptationPlanForRevision(otherProject.Revisions, candidate, previewed.Session.ID)
			},
			func() error { return st.SaveAdaptationPlanForRevision(staleOwner, candidate, previewed.Session.ID) },
			func() error {
				return st.PublishLayeredStructureForRevision(nil, layered, "host-forged-layered-publish")
			},
			func() error { return st.RestoreLayeredStructureForRevision(nil, layered, progress) },
		}
		formalErrors = make([]error, len(attempts))
		var wg sync.WaitGroup
		for index := range attempts {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				formalErrors[index] = attempts[index]()
			}(index)
		}
		wg.Wait()
		formalBytesAfter = adaptationRevisionProjectBytes(t, st.Dir())
	})
	cancelled, err := service.Cancel(previewed.Session, key)
	if err != nil {
		t.Fatal(err)
	}
	for name, forged := range map[string]error{"matching receipt": receiptErr, "runtime save": saveErr, "runtime clear": clearErr} {
		if !errors.Is(forged, storepkg.ErrRevisionCommandInProgress) {
			t.Fatalf("forged %s bypassed prepared owner: %v", name, forged)
		}
	}
	for index, forged := range formalErrors {
		if !errors.Is(forged, storepkg.ErrRevisionCommandInProgress) {
			t.Fatalf("forged Host formal write %d bypassed prepared owner: %v", index, forged)
		}
	}
	if !reflect.DeepEqual(formalBytesBefore, formalBytesAfter) {
		t.Fatal("rejected Host same-session/cross-project/stale races changed project bytes")
	}
	if active, err := st.Revisions.Active(); err != nil || active != nil {
		t.Fatalf("terminal owner did not clear active session: active=%+v err=%v", active, err)
	}
	if persisted, err := st.Adaptation.LoadRevisionRuntime(); err != nil || persisted != nil {
		t.Fatalf("terminal owner did not clear runtime: runtime=%+v err=%v", persisted, err)
	}
	lease, err := competitor.Revisions.AcquireNormalFlow("terminal-owner-successor")
	if err != nil {
		t.Fatalf("terminal receipt cleanup did not release successor: %v", err)
	}
	if err := competitor.Revisions.ReleaseNormalFlow(lease.Token); err != nil {
		t.Fatal(err)
	}
	service.SetCommandPreparedHookForTesting(nil)
	replayed, err := service.Cancel(previewed.Session, key)
	if err != nil || !reflect.DeepEqual(replayed, cancelled) {
		t.Fatalf("terminal receipt replay drifted: replay=%+v want=%+v err=%v", replayed, cancelled, err)
	}
}

func assertAdaptationCommandPanics(t *testing.T, command func()) {
	t.Helper()
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		command()
	}()
	if !panicked {
		t.Fatal("command did not reach the simulated process interruption")
	}
}

func adaptationStoreFenceImpactForHost(t *testing.T) domain.RevisionImpact {
	t.Helper()
	impact, err := domain.NewRevisionImpact("same-key prepared command competitor", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "outline", Change: "compete",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return impact
}

func preparedOwnerLayeredFixture(project string) []domain.VolumeOutline {
	volumeID := domain.LegacyStructureID(project, domain.StructureKindVolume, "volume-1")
	arcID := domain.LegacyStructureID(project, domain.StructureKindArc, "volume-1/arc-1")
	chapterID := domain.LegacyStructureID(project, domain.StructureKindChapter, "volume-1/arc-1/chapter-1")
	return []domain.VolumeOutline{{
		ID: volumeID, Index: 1, Title: "Volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{
			ID: arcID, Index: 1, Title: "Arc", Goal: "goal",
			Chapters: []domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "One", CoreEvent: "event", Hook: "hook", Scenes: []string{"scene"}}},
		}},
	}}
}

func TestAdaptationRevisionHostReadCannotRecoverPendingMigration(t *testing.T) {
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
	service := NewAdaptationRevisionService(st)
	if _, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "fence Host read", Candidate: candidate}, "host-read-preview"); err != nil {
		t.Fatal(err)
	}
	migrationLog := filepath.Join(st.Dir(), "meta", "structure", "migration.json")
	if err := os.MkdirAll(filepath.Dir(migrationLog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(migrationLog, []byte(`{"version":1,"stage":"planned"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before := adaptationRevisionProjectBytes(t, st.Dir())
	reopened := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
	if _, err := reopened.CurrentManuscriptStage(); err == nil || !strings.Contains(err.Error(), "active revision") {
		t.Fatalf("Host read recovered a pending migration through revision ownership: %v", err)
	}
	if after := adaptationRevisionProjectBytes(t, st.Dir()); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected Host read changed the pending formal/derived snapshot")
	}
}

func adaptationRevisionProjectBytes(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".lock") || strings.Contains(rel, "adaptation-command-journal") || strings.Contains(rel, "adaptation-command-snapshot") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func adaptationPassingEvidence(session *domain.RevisionSession) []domain.RevisionAuditEvidence {
	evidence := make([]domain.RevisionAuditEvidence, 0, len(session.AuditExpectations))
	for _, expected := range session.AuditExpectations {
		evidence = append(evidence, domain.RevisionAuditEvidence{Scope: expected.Scope, ScopeID: expected.ScopeID, FromChapter: expected.FromChapter, ToChapter: expected.ToChapter, ContentSignature: expected.ContentSignature, Passed: true})
	}
	return evidence
}

func TestAdaptationRevisionBatchPlanUsesImmutableBoundedContext(t *testing.T) {
	base, manifest := adaptationRevisionServiceFixture(domain.AdaptationGranularityArc)
	base.Chapters[0].SourceSegments = nil
	base.Chapters[1].SourceSegments = nil
	base.Chapters[0].SourceRunes = 999999
	base.Chapters[1].SourceRunes = 1
	manifest.Chapters[0].Runes = 6000
	manifest.Chapters[1].Runes = 6000
	impact, err := domain.NewRevisionImpact("two risky chapters", []domain.RevisionImpactItem{
		{ArtifactID: base.Chapters[0].ID, ArtifactKind: domain.StructureKindChapter, Change: "revise", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"one"}},
		{ArtifactID: base.Chapters[1].ID, ArtifactKind: domain.StructureKindChapter, Change: "revise", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"two"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := deriveAdaptationRevisionBatchPlan(base, impact, &manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Batches) != 2 || plan.Batches[0].ContextUnits > domain.AdaptationRevisionBatchContextMaxUnits || plan.Batches[1].ContextUnits > domain.AdaptationRevisionBatchContextMaxUnits {
		t.Fatalf("still-risky pair was not iteratively split: %+v", plan.Batches)
	}
	if plan.Batches[0].Context[0].Units != 6000 || strings.Contains(plan.Batches[0].Context[0].ID, "source:2") {
		t.Fatalf("batch context trusted forged runes or loaded non-local source: %+v", plan.Batches[0])
	}
	manifest.Chapters[0].Runes = domain.AdaptationRevisionBatchContextMaxUnits + 1
	hugeImpact, err := domain.NewRevisionImpact("huge single source", impact.Items[:1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deriveAdaptationRevisionBatchPlan(base, hugeImpact, &manifest); err == nil {
		t.Fatal("huge single immutable source context was accepted")
	}
}

func TestAdaptationRevisionServiceAllowsOnlyWhollyUnwrittenMoves(t *testing.T) {
	for _, test := range []struct {
		name        string
		markWritten func(*testing.T, *storepkg.Store)
		wantError   bool
	}{
		{name: "unwritten move allowed"},
		{name: "completed number without final body is written", wantError: true, markWritten: func(t *testing.T, st *storepkg.Store) {
			progress, _ := st.Progress.Load()
			progress.CompletedChapters = []int{2}
			if err := st.Progress.Save(progress); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "non-empty partial draft is written", wantError: true, markWritten: func(t *testing.T, st *storepkg.Store) {
			if err := st.Drafts.SaveDraft(2, "partial real draft"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
			if test.markWritten != nil {
				test.markWritten(t, st)
			}
			moved := adaptationRevisionTestClone(t, candidate)
			moved.TargetEventLedger[0].DependsOn = []string{"source-event-1"}
			moved.Chapters[1], moved.Chapters[2] = moved.Chapters[2], moved.Chapters[1]
			for index := range moved.Chapters {
				moved.Chapters[index].Chapter = index + 1
				moved.Chapters[index].OutlineEntry.Chapter = index + 1
			}
			service := NewAdaptationRevisionService(st)
			previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "move target", Candidate: moved}, "move")
			if test.wantError && (err == nil || !strings.Contains(err.Error(), "cannot be moved")) {
				t.Fatalf("written move was not rejected: %v", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("wholly unwritten move was rejected: %v", err)
			}
			if !test.wantError {
				session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, moved)
				if _, err := service.Publish(*previewed.Preview, session, "publish"); err != nil {
					t.Fatalf("wholly unwritten move could not be published: %v", err)
				}
			}
		})
	}
}

func TestAdaptationRevisionDetailedOutlineCannotReplaceSealedOwnership(t *testing.T) {
	st, base, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityChapter, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
	if err != nil {
		t.Fatal(err)
	}
	session, err = service.SubmitStructureCandidate(*previewed.Preview, session, "structure")
	if err != nil {
		t.Fatal(err)
	}
	completeAdaptationRuntime(t, service, session.ID)
	session = passAdaptationAuditAndApprove(t, service, session, "structure")

	mutations := map[string]func(*domain.AdaptationPlan){
		"source chapters": func(plan *domain.AdaptationPlan) { plan.Chapters[0].SourceChapters = []int{2} },
		"source segments": func(plan *domain.AdaptationPlan) { plan.Chapters[0].SourceSegments[0].SourceChapter = 2 },
		"source event owner": func(plan *domain.AdaptationPlan) {
			plan.Chapters[0].EventIDs, plan.Chapters[1].EventIDs = plan.Chapters[1].EventIDs, plan.Chapters[0].EventIDs
		},
		"added event owner": func(plan *domain.AdaptationPlan) {
			plan.Chapters[2].AddedEventIDs = nil
			plan.Chapters[1].AddedEventIDs = []string{"added-event"}
		},
		"coverage":           func(plan *domain.AdaptationPlan) { plan.Chapters[0].CoverageNote = "replaced coverage" },
		"volume ownership":   func(plan *domain.AdaptationPlan) { plan.Volumes[0].TargetFrom = 2 },
		"protected contract": func(plan *domain.AdaptationPlan) { plan.Chapters[0].ForbiddenMoves = []string{"changed"} },
		"target event ledger": func(plan *domain.AdaptationPlan) {
			plan.TargetEventLedger[0].Origin = domain.AdaptationEventOriginSource
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			adversarial := adaptationRevisionTestClone(t, candidate)
			mutate(&adversarial)
			if _, err := service.SubmitDetailedOutlineCandidate(adversarial, session, "details-"+strings.ReplaceAll(name, " ", "-")); err == nil || !strings.Contains(err.Error(), "accepted structure skeleton") {
				t.Fatalf("sealed ownership substitution was not rejected: %v", err)
			}
		})
	}
	formal, _ := st.Adaptation.LoadPlan()
	active, _ := st.Revisions.Active()
	if !reflect.DeepEqual(*formal, base) || active == nil || active.Revision != session.Revision || active.Stage != domain.RevisionStageCandidateGenerating {
		t.Fatalf("rejected detail changed formal/accepted state: formal=%+v active=%+v", formal, active)
	}
}

func TestAdaptationRevisionPublishMergesRewritesAndPreservesReopenState(t *testing.T) {
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageComplete, domain.AdaptationGranularityArc, true)
	progress, _ := st.Progress.Load()
	progress.PendingRewrites = []int{2}
	progress.RewriteReason = "existing repair"
	progress.ReopenedFromComplete = true
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	candidate = adaptationRevisionTestClone(t, candidate)
	candidate.Chapters[0].CoreEvent = "revised written event"
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "revise written target", Candidate: candidate}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
	session, err = service.SubmitProseReworkCandidate(session, "prose")
	if err != nil {
		t.Fatal(err)
	}
	session = passAdaptationAuditAndApprove(t, service, session, "prose")
	if _, err := service.Publish(*previewed.Preview, session, "publish"); err != nil {
		t.Fatal(err)
	}
	progress, _ = st.Progress.Load()
	if !reflect.DeepEqual(progress.PendingRewrites, []int{2, 1}) || progress.Phase != domain.PhaseWriting || !progress.ReopenedFromComplete || progress.Flow != domain.FlowRewriting || !strings.Contains(progress.RewriteReason, "existing repair") {
		t.Fatalf("published progress did not preserve/merge rewrite state: %+v", progress)
	}
}

func TestAdaptationRevisionServiceCommittedPublicationFinalizeFaultRetryAndUnavailableGC(t *testing.T) {
	for _, fault := range []struct {
		name      string
		pathPart  string
		stage     string
		writeCall int
	}{
		{name: "before acceptance replace", pathPart: "/acceptances/", stage: "after_sync", writeCall: 1},
		{name: "after acceptance replace", pathPart: "/acceptances/", stage: "after_replace", writeCall: 1},
		{name: "before accepted journal replace", pathPart: "/publications/", stage: "after_sync", writeCall: 2},
		{name: "after accepted journal replace", pathPart: "/publications/", stage: "after_replace", writeCall: 2},
	} {
		t.Run(fault.name, func(t *testing.T) {
			st, service, preview, session, candidate := prepareAdaptationServicePublication(t)
			calls := 0
			restoreFault := storepkg.SetExpansionAuthorityWriteFaultForTesting(func(path, stage string) error {
				if strings.Contains(filepath.ToSlash(path), fault.pathPart) && stage == fault.stage {
					calls++
					if calls == fault.writeCall {
						return errors.New("injected adaptation service finalize fault")
					}
				}
				return nil
			})
			published, err := service.Publish(preview, session, "publish-finalize")
			restoreFault()
			if err == nil || published == nil || published.Stage != domain.RevisionStageCompleted {
				t.Fatalf("committed finalize fault result=%+v err=%v", published, err)
			}
			formal, loadErr := st.Adaptation.LoadPlan()
			if loadErr != nil || formal == nil || len(formal.Chapters) != len(candidate.Chapters) || formal.Chapters[len(formal.Chapters)-1].ID != adaptationTestAddedID {
				t.Fatalf("committed finalize fault rolled back formal adaptation: plan=%+v err=%v", formal, loadErr)
			}

			restarted := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
			replayed, err := restarted.Publish(preview, session, "publish-finalize")
			if err != nil || replayed == nil || replayed.Stage != domain.RevisionStageCompleted || replayed.ID != published.ID {
				t.Fatalf("exact restart retry result=%+v err=%v", replayed, err)
			}
			replayedAgain, err := NewAdaptationRevisionService(storepkg.NewStore(st.Dir())).Publish(preview, session, "publish-finalize")
			if err != nil || !reflect.DeepEqual(replayed, replayedAgain) {
				t.Fatalf("completed service receipt replay=%+v err=%v", replayedAgain, err)
			}
			changedPreview := preview
			changedPreview.Stage = domain.ManuscriptStageWriting
			if _, err := restarted.Publish(changedPreview, session, "publish-finalize"); err == nil {
				t.Fatal("same-key different adaptation service fingerprint bypassed the committed receipt")
			}
		})
	}

	for _, unavailable := range []string{"moved", "deleted"} {
		t.Run("committed service publication survives "+unavailable+" project GC", func(t *testing.T) {
			restoreRetention := storepkg.SetExpansionAuthorityOrphanRetentionForTesting(0)
			defer restoreRetention()
			st, service, preview, session, _ := prepareAdaptationServicePublication(t)
			before := snapshotPublicationAuthorityRegistry(t)
			restoreFault := storepkg.SetExpansionAuthorityWriteFaultForTesting(func(path, stage string) error {
				if strings.Contains(filepath.ToSlash(path), "/acceptances/") && stage == "after_replace" {
					return errors.New("injected committed adaptation service finalize fault")
				}
				return nil
			})
			published, err := service.Publish(preview, session, "publish-unavailable")
			restoreFault()
			if err == nil || published == nil || published.Stage != domain.RevisionStageCompleted {
				t.Fatalf("committed unavailable setup result=%+v err=%v", published, err)
			}
			committedRegistry := snapshotPublicationAuthorityRegistry(t)
			if len(committedRegistry) != len(before)+1 {
				t.Fatalf("committed service publication authority count before=%d after=%d", len(before), len(committedRegistry))
			}
			projectRoot := filepath.Dir(st.Dir())
			if unavailable == "moved" {
				if err := os.Rename(projectRoot, projectRoot+"-moved"); err != nil {
					t.Fatal(err)
				}
			} else if err := os.RemoveAll(projectRoot); err != nil {
				t.Fatal(err)
			}
			report, err := storepkg.ReconcileExpansionAuthorityOrphans()
			if err != nil || report.Finalized != 1 {
				t.Fatalf("unavailable adaptation service reconciliation=%+v err=%v", report, err)
			}
			if after := snapshotPublicationAuthorityRegistry(t); !reflect.DeepEqual(committedRegistry, after) {
				t.Fatalf("unavailable GC changed committed adaptation owner capability\nbefore=%+v\nafter=%+v", committedRegistry, after)
			}
		})
	}
}

func TestAdaptationRevisionCommittedFinalizeServiceReceiptRecoveryMatrix(t *testing.T) {
	tests := []struct {
		name         string
		authorityDir string
		authorityAt  string
		writeCall    int
		receiptFault string
	}{
		{name: "acceptance before replace plus receipt before replace", authorityDir: "/acceptances/", authorityAt: "after_sync", writeCall: 1, receiptFault: "before_replace"},
		{name: "acceptance after replace plus receipt before replace", authorityDir: "/acceptances/", authorityAt: "after_replace", writeCall: 1, receiptFault: "before_replace"},
		{name: "accepted journal before replace plus receipt panic", authorityDir: "/publications/", authorityAt: "after_sync", writeCall: 2, receiptFault: "panic"},
		{name: "accepted journal after replace plus receipt panic", authorityDir: "/publications/", authorityAt: "after_replace", writeCall: 2, receiptFault: "panic"},
		{name: "ambiguous durable service receipt replace", authorityDir: "/acceptances/", authorityAt: "after_replace", writeCall: 1, receiptFault: "after_replace"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st, service, preview, session, candidate := prepareAdaptationServicePublication(t)
			registryBefore := snapshotPublicationAuthorityRegistry(t)
			authorityCalls := 0
			restoreAuthority := storepkg.SetExpansionAuthorityWriteFaultForTesting(func(path, stage string) error {
				if strings.Contains(filepath.ToSlash(path), test.authorityDir) && stage == test.authorityAt {
					authorityCalls++
					if authorityCalls == test.writeCall {
						return errors.New("injected committed finalize failure")
					}
				}
				return nil
			})
			var restoreWrite func()
			switch test.receiptFault {
			case "before_replace":
				restoreWrite = st.SetExpansionWriteFaultForTesting(func(rel, stage string) error {
					if rel == "meta/adaptation/revision_service_receipts.json" && stage == "after_temp_sync" {
						return errors.New("injected service receipt before replace failure")
					}
					return nil
				})
			case "after_replace":
				restoreWrite = st.SetExpansionWriteFaultForTesting(func(rel, stage string) error {
					if rel == "meta/adaptation/revision_service_receipts.json" && stage == "after_replace" {
						return errors.New("injected service receipt ambiguous replace failure")
					}
					return nil
				})
			case "panic":
				service.saveRevisionReceipt = func(string, string, string, any) error {
					panic("simulated service receipt process interruption")
				}
			}

			var published *domain.RevisionSession
			var publishErr error
			panicked := false
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				published, publishErr = service.Publish(preview, session, "publish-receipt-recovery")
			}()
			restoreAuthority()
			if restoreWrite != nil {
				restoreWrite()
			}
			if test.receiptFault == "panic" {
				if !panicked {
					t.Fatal("service receipt panic did not interrupt the prepared command")
				}
			} else if publishErr == nil || published == nil || published.Stage != domain.RevisionStageCompleted {
				t.Fatalf("committed receipt fault result=%+v err=%v", published, publishErr)
			}
			assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)

			replayed, err := service.Publish(preview, session, "publish-receipt-recovery")
			if err != nil || replayed == nil || replayed.Stage != domain.RevisionStageCompleted {
				t.Fatalf("same-process committed recovery result=%+v err=%v", replayed, err)
			}
			assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)
			restarted, err := NewAdaptationRevisionService(storepkg.NewStore(st.Dir())).Publish(preview, session, "publish-receipt-recovery")
			if err != nil || !reflect.DeepEqual(replayed, restarted) {
				t.Fatalf("restart committed replay result=%+v err=%v", restarted, err)
			}
			changed := preview
			changed.Stage = domain.ManuscriptStageWriting
			if _, err := NewAdaptationRevisionService(storepkg.NewStore(st.Dir())).Publish(changed, session, "publish-receipt-recovery"); err == nil {
				t.Fatal("different service fingerprint bypassed repaired receipt")
			}
		})
	}
}

func TestAdaptationRevisionCommittedFinalizePreparedCleanupRecoveryMatrix(t *testing.T) {
	for _, fault := range []struct{ path, stage string }{
		{path: "meta/revisions/adaptation-command-journal.json", stage: "before_delete"},
		{path: "meta/revisions/adaptation-command-journal.json", stage: "after_delete"},
		{path: "meta/revisions/adaptation-command-snapshot", stage: "before_delete"},
		{path: "meta/revisions/adaptation-command-snapshot", stage: "after_delete"},
	} {
		t.Run(fault.path+"/"+fault.stage, func(t *testing.T) {
			st, service, preview, session, candidate := prepareAdaptationServicePublication(t)
			registryBefore := snapshotPublicationAuthorityRegistry(t)
			restoreAuthority := storepkg.SetExpansionAuthorityWriteFaultForTesting(func(path, stage string) error {
				if strings.Contains(filepath.ToSlash(path), "/acceptances/") && stage == "after_replace" {
					return errors.New("injected committed finalize failure")
				}
				return nil
			})
			fired := false
			restoreCleanup := storepkg.SetAdaptationRevisionCommandCleanupFaultForTesting(func(path, stage string) error {
				if path == fault.path && stage == fault.stage && !fired {
					fired = true
					return errors.New("injected prepared cleanup failure")
				}
				return nil
			})
			published, err := service.Publish(preview, session, "publish-cleanup-recovery")
			restoreAuthority()
			restoreCleanup()
			if err == nil || published == nil || published.Stage != domain.RevisionStageCompleted || !fired {
				t.Fatalf("prepared cleanup fault result=%+v err=%v fired=%v", published, err, fired)
			}
			assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)
			replayed, err := NewAdaptationRevisionService(storepkg.NewStore(st.Dir())).Publish(preview, session, "publish-cleanup-recovery")
			if err != nil || replayed == nil || replayed.Stage != domain.RevisionStageCompleted {
				t.Fatalf("prepared cleanup restart result=%+v err=%v", replayed, err)
			}
			assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)
		})
	}
}

func TestAdaptationRevisionCommittedFinalizeRuntimeCleanupRecovery(t *testing.T) {
	st, service, preview, session, candidate := prepareAdaptationServicePublication(t)
	registryBefore := snapshotPublicationAuthorityRegistry(t)
	runtimePath := filepath.Join(st.Dir(), "meta", "adaptation", "revision_runtime.json")
	runtimeBytes, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	restoreAuthority := storepkg.SetExpansionAuthorityWriteFaultForTesting(func(path, stage string) error {
		if strings.Contains(filepath.ToSlash(path), "/acceptances/") && stage == "after_replace" {
			return errors.New("injected committed finalize failure")
		}
		return nil
	})
	service.saveRevisionReceipt = func(string, string, string, any) error {
		if err := os.WriteFile(runtimePath, runtimeBytes, 0o644); err != nil {
			return err
		}
		// Simulate an interrupted caller that did not know whether the receipt
		// was durable. Recovery must reconstruct it from the internal receipt.
		return nil
	}
	published, publishErr := service.Publish(preview, session, "publish-runtime-recovery")
	restoreAuthority()
	if publishErr == nil || published == nil || published.Stage != domain.RevisionStageCompleted {
		t.Fatalf("runtime recovery setup result=%+v err=%v", published, publishErr)
	}
	assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)
	fired := false
	restoreCleanup := storepkg.SetAdaptationRevisionCommandCleanupFaultForTesting(func(path, stage string) error {
		if path == "meta/adaptation/revision_runtime.json" && stage == "before_delete" {
			fired = true
			return errors.New("injected committed runtime cleanup failure")
		}
		return nil
	})
	_, cleanupErr := NewAdaptationRevisionService(storepkg.NewStore(st.Dir())).Publish(preview, session, "publish-runtime-recovery")
	if cleanupErr == nil || !fired {
		t.Fatalf("runtime cleanup fault was not retained for retry: fired=%v err=%v", fired, cleanupErr)
	}
	restoreCleanup()
	assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)
	replayed, err := NewAdaptationRevisionService(storepkg.NewStore(st.Dir())).Publish(preview, session, "publish-runtime-recovery")
	if err != nil || replayed == nil || replayed.Stage != domain.RevisionStageCompleted {
		t.Fatalf("runtime cleanup restart result=%+v err=%v", replayed, err)
	}
	if _, err := os.Stat(runtimePath); !os.IsNotExist(err) {
		t.Fatalf("committed runtime remains after recovery: %v", err)
	}
}

func TestAdaptationRevisionCommittedRuntimeRestoreWriteFailureRecoveryMatrix(t *testing.T) {
	for _, cleanupStage := range []string{"before_delete", "after_delete"} {
		t.Run(cleanupStage, func(t *testing.T) {
			st, service, preview, session, candidate := prepareAdaptationServicePublication(t)
			registryBefore := snapshotPublicationAuthorityRegistry(t)
			runtimePath := filepath.Join(st.Dir(), "meta", "adaptation", "revision_runtime.json")
			restoreAuthority := storepkg.SetExpansionAuthorityWriteFaultForTesting(func(path, stage string) error {
				if strings.Contains(filepath.ToSlash(path), "/acceptances/") && stage == "after_replace" {
					return errors.New("injected committed authority finalize failure")
				}
				return nil
			})
			runtimeRestoreFault := false
			restoreWrite := st.SetExpansionWriteFaultForTesting(func(rel, stage string) error {
				if rel == "meta/adaptation/revision_runtime.json" && stage == "after_replace" {
					runtimeRestoreFault = true
					return errors.New("injected committed runtime restore write failure")
				}
				return nil
			})
			published, publishErr := service.Publish(preview, session, "publish-runtime-restore-failure")
			restoreWrite()
			restoreAuthority()
			if publishErr == nil || published == nil || published.Stage != domain.RevisionStageCompleted || !runtimeRestoreFault {
				t.Fatalf("committed runtime restore fault result=%+v err=%v fired=%v", published, publishErr, runtimeRestoreFault)
			}
			assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)
			if _, err := os.Stat(runtimePath); err != nil {
				t.Fatalf("ambiguous runtime restore did not leave a cleanup target: %v", err)
			}
			for _, path := range []string{
				filepath.Join(st.Dir(), "meta", "revisions", "adaptation-command-journal.json"),
				filepath.Join(st.Dir(), "meta", "revisions", "adaptation-command-snapshot"),
			} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("runtime restore failure lost prepared recovery evidence %s: %v", filepath.Base(path), err)
				}
			}
			found, err := st.Adaptation.HasRevisionServiceReceipt(
				"publish-runtime-restore-failure", "publish", adaptationPublishServiceFingerprint(t, preview, session),
			)
			if err != nil || found {
				t.Fatalf("runtime restore failure forged a service receipt: found=%v err=%v", found, err)
			}

			cleanupFired := false
			restoreCleanup := storepkg.SetAdaptationRevisionCommandCleanupFaultForTesting(func(path, stage string) error {
				if path == "meta/adaptation/revision_runtime.json" && stage == cleanupStage {
					cleanupFired = true
					return errors.New("injected committed runtime cleanup retry failure")
				}
				return nil
			})
			restarted := storepkg.NewStore(st.Dir())
			firstRetryErr := restarted.RecoverStructureMigration()
			if !cleanupFired || (cleanupStage == "before_delete" && firstRetryErr == nil) || (cleanupStage == "after_delete" && firstRetryErr != nil) {
				restoreCleanup()
				t.Fatalf("runtime cleanup %s recovery mismatch: fired=%v err=%v", cleanupStage, cleanupFired, firstRetryErr)
			}
			restoreCleanup()
			assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)
			if err := restarted.RecoverStructureMigration(); err != nil {
				t.Fatalf("runtime cleanup %s exact retry failed: %v", cleanupStage, err)
			}
			replayed, err := NewAdaptationRevisionService(restarted).Publish(preview, session, "publish-runtime-restore-failure")
			if err != nil || !reflect.DeepEqual(published, replayed) {
				t.Fatalf("runtime restore/cleanup %s replay=%+v err=%v", cleanupStage, replayed, err)
			}
			if _, err := os.Stat(runtimePath); !os.IsNotExist(err) {
				t.Fatalf("runtime cleanup %s left checkpoint after retry: %v", cleanupStage, err)
			}
		})
	}
}

func adaptationPublishServiceFingerprint(t *testing.T, preview AdaptationStructureRevisionPreview, session *domain.RevisionSession) string {
	t.Helper()
	request := adaptationCommandReceiptRequest("publish", adaptationRevisionNumber(session))
	request.Preview = &preview
	_, payload, err := adaptationRevisionCommandReceiptIdentity(request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return domain.ContentSignature(encoded)
}

func TestAdaptationRevisionCommittedFinalizePreparedBindingTamperFailsClosed(t *testing.T) {
	for _, tamper := range []string{"missing publication", "different internal fingerprint", "unknown journal field", "duplicate journal field", "path escape"} {
		t.Run(tamper, func(t *testing.T) {
			st, service, preview, session, candidate := prepareAdaptationServicePublication(t)
			registryBefore := snapshotPublicationAuthorityRegistry(t)
			restoreAuthority := storepkg.SetExpansionAuthorityWriteFaultForTesting(func(path, stage string) error {
				if strings.Contains(filepath.ToSlash(path), "/acceptances/") && stage == "after_replace" {
					return errors.New("injected committed finalize failure")
				}
				return nil
			})
			service.saveRevisionReceipt = func(string, string, string, any) error {
				panic("interrupt before service receipt")
			}
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				_, _ = service.Publish(preview, session, "publish-binding-tamper")
			}()
			restoreAuthority()
			if !panicked {
				t.Fatal("service receipt interruption did not retain prepared state")
			}
			assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)

			journalPath := filepath.Join(st.Dir(), "meta", "revisions", "adaptation-command-journal.json")
			data, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			var journal map[string]any
			if err := json.Unmarshal(data, &journal); err != nil {
				t.Fatal(err)
			}
			switch tamper {
			case "missing publication":
				delete(journal, "publication")
			case "different internal fingerprint":
				publication := journal["publication"].(map[string]any)
				publication["internal_receipt_fingerprint"] = strings.Repeat("0", 64)
			case "unknown journal field":
				journal["unexpected"] = true
			case "path escape":
				journal["files"] = []any{"../escaped"}
			}
			data, err = json.MarshalIndent(journal, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if tamper == "duplicate journal field" {
				data = append([]byte("{\"version\":2,"), data[1:]...)
			}
			if err := os.WriteFile(journalPath, append(data, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			restarted := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
			if _, err := restarted.Publish(preview, session, "publish-binding-tamper"); err == nil {
				t.Fatal("tampered prepared publication binding was recovered")
			}
			assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)
		})
	}
}

func TestAdaptationRevisionV1PublishUpgradeRecoveryMatrix(t *testing.T) {
	t.Run("precommit rollback", func(t *testing.T) {
		st, service, preview, session, _ := prepareAdaptationServicePublication(t)
		before, err := st.CaptureAdaptationFormalSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		service.SetCommandPreparedHookForTesting(func() { rewritePreparedAdaptationJournalAsV1(t, st.Dir()) })
		service.clearRevisionRuntime = func(string) error { return errors.New("injected precommit runtime cleanup failure") }
		if _, err := service.Publish(preview, session, "v1-publish-precommit"); err == nil {
			t.Fatal("v1 precommit publish did not fail")
		}
		after, err := st.CaptureAdaptationFormalSnapshot()
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("v1 precommit rollback changed formal snapshot: err=%v", err)
		}
	})

	t.Run("committed service receipt cleanup", func(t *testing.T) {
		st, service, preview, session, candidate := prepareAdaptationServicePublication(t)
		registryBefore := snapshotPublicationAuthorityRegistry(t)
		service.SetCommandPreparedHookForTesting(func() { rewritePreparedAdaptationJournalAsV1(t, st.Dir()) })
		fired := false
		restoreCleanup := storepkg.SetAdaptationRevisionCommandCleanupFaultForTesting(func(path, stage string) error {
			if path == "meta/revisions/adaptation-command-journal.json" && stage == "before_delete" && !fired {
				fired = true
				return errors.New("retain v1 prepared journal after service receipt")
			}
			return nil
		})
		published, publishErr := service.Publish(preview, session, "v1-publish-service-receipt")
		restoreCleanup()
		if publishErr == nil || published == nil || !fired {
			t.Fatalf("v1 receipt cleanup setup result=%+v err=%v fired=%v", published, publishErr, fired)
		}
		assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)
		restarted := storepkg.NewStore(st.Dir())
		if err := restarted.RecoverStructureMigration(); err != nil {
			t.Fatalf("v1 receipt-backed NewStore cleanup failed: %v", err)
		}
		replayed, err := NewAdaptationRevisionService(restarted).Publish(preview, session, "v1-publish-service-receipt")
		if err != nil || !reflect.DeepEqual(published, replayed) {
			t.Fatalf("v1 receipt-backed exact replay=%+v err=%v", replayed, err)
		}
	})

	t.Run("internal receipt forward without service receipt", func(t *testing.T) {
		st, service, preview, session, candidate := prepareAdaptationServicePublication(t)
		registryBefore := snapshotPublicationAuthorityRegistry(t)
		service.SetCommandPreparedHookForTesting(func() { rewritePreparedAdaptationJournalAsV1(t, st.Dir()) })
		restoreAuthority := storepkg.SetExpansionAuthorityWriteFaultForTesting(func(path, stage string) error {
			if strings.Contains(filepath.ToSlash(path), "/acceptances/") && stage == "after_replace" {
				return errors.New("interrupt v1 committed authority finalize")
			}
			return nil
		})
		service.saveRevisionReceipt = func(string, string, string, any) error {
			panic("interrupt before v1 service receipt")
		}
		panicked := false
		func() {
			defer func() { panicked = recover() != nil }()
			_, _ = service.Publish(preview, session, "v1-publish-internal-receipt")
		}()
		restoreAuthority()
		if !panicked {
			t.Fatal("v1 committed publication did not stop before service receipt")
		}
		assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)

		restarted := storepkg.NewStore(st.Dir())
		if err := restarted.RecoverStructureMigration(); err != nil {
			t.Fatalf("v1 internal-receipt NewStore forward recovery failed: %v", err)
		}
		replayed, err := NewAdaptationRevisionService(restarted).Publish(preview, session, "v1-publish-internal-receipt")
		if err != nil || replayed == nil || replayed.Stage != domain.RevisionStageCompleted {
			t.Fatalf("v1 internal-receipt exact replay=%+v err=%v", replayed, err)
		}
	})

	t.Run("unprovable internal receipt fails closed", func(t *testing.T) {
		st, service, preview, session, candidate := prepareAdaptationServicePublication(t)
		registryBefore := snapshotPublicationAuthorityRegistry(t)
		service.SetCommandPreparedHookForTesting(func() { rewritePreparedAdaptationJournalAsV1(t, st.Dir()) })
		restoreAuthority := storepkg.SetExpansionAuthorityWriteFaultForTesting(func(path, stage string) error {
			if strings.Contains(filepath.ToSlash(path), "/acceptances/") && stage == "after_replace" {
				return errors.New("interrupt unprovable v1 authority finalize")
			}
			return nil
		})
		service.saveRevisionReceipt = func(string, string, string, any) error { panic("interrupt before service receipt") }
		func() {
			defer func() { _ = recover() }()
			_, _ = service.Publish(preview, session, "v1-publish-unprovable")
		}()
		restoreAuthority()
		assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)

		statePath := filepath.Join(st.Dir(), "meta", "revisions", "state.json")
		var state map[string]any
		data, err := os.ReadFile(statePath)
		if err != nil || json.Unmarshal(data, &state) != nil {
			t.Fatal(err)
		}
		receipts := state["receipts"].(map[string]any)
		receipt := receipts["v1-publish-unprovable"].(map[string]any)
		originalFingerprint := receipt["fingerprint"].(string)
		tampered := strings.Replace(string(data), originalFingerprint, strings.Repeat("0", 64), 1)
		if tampered == string(data) {
			t.Fatal("internal receipt fingerprint was not found for exact tamper")
		}
		if err := os.WriteFile(statePath, []byte(tampered), 0o644); err != nil {
			t.Fatal(err)
		}
		restarted := storepkg.NewStore(st.Dir())
		if err := restarted.RecoverStructureMigration(); err == nil {
			t.Fatal("unprovable v1 committed publication did not fail closed")
		}
		if _, err := os.Stat(filepath.Join(st.Dir(), "meta", "revisions", "adaptation-command-journal.json")); err != nil {
			t.Fatalf("unprovable v1 diagnostic journal was removed: %v", err)
		}
		if err := os.WriteFile(statePath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)
	})
}

func TestAdaptationRevisionLegacyPublicationServiceBindingFailsClosed(t *testing.T) {
	for _, journalVersion := range []int{1, 2} {
		for _, serviceReceiptPresent := range []bool{false, true} {
			t.Run(fmt.Sprintf("journal-v%d/service-receipt-%t", journalVersion, serviceReceiptPresent), func(t *testing.T) {
				st, service, preview, session, candidate := prepareAdaptationServicePublication(t)
				key := fmt.Sprintf("legacy-service-binding-v%d-%t", journalVersion, serviceReceiptPresent)
				registryBefore := snapshotPublicationAuthorityRegistry(t)
				var rawInternalFingerprint string
				service.SetCommandPreparedHookForTesting(func() {
					rawInternalFingerprint = preparedAdaptationPublicationInternalFingerprint(t, st.Dir())
					if journalVersion == 1 {
						rewritePreparedAdaptationJournalAsV1(t, st.Dir())
					}
				})
				restoreLegacy, err := storepkg.SetAdaptationPublicationLegacyBindingForTesting(true)
				if err != nil {
					t.Fatal(err)
				}
				service.saveRevisionReceipt = func(string, string, string, any) error {
					panic("interrupt after legacy signed receipt commit")
				}
				panicked := false
				func() {
					defer func() { panicked = recover() != nil }()
					_, _ = service.Publish(preview, session, key)
				}()
				restoreLegacy()
				if !panicked {
					t.Fatal("legacy migration setup did not stop before service receipt")
				}

				receiptPath := filepath.Join(st.Dir(), "meta", "revisions", "expansion-publication-receipt.json")
				signedReceipt, err := os.ReadFile(receiptPath)
				var receipt storepkg.ExpansionPublicationReceipt
				if err != nil || json.Unmarshal(signedReceipt, &receipt) != nil {
					t.Fatal(err)
				}
				if receipt.AdaptationServiceBinding != "" {
					t.Fatal("legacy publication fixture unexpectedly contained a service binding")
				}
				registryCommitted := snapshotPublicationAuthorityRegistry(t)

				changed := preview
				changed.Stage = domain.ManuscriptStageWriting
				originalFingerprint := adaptationPublishServiceFingerprint(t, preview, session)
				changedFingerprint := adaptationPublishServiceFingerprint(t, changed, session)
				journalPath := filepath.Join(st.Dir(), "meta", "revisions", "adaptation-command-journal.json")
				journalData, err := os.ReadFile(journalPath)
				if err != nil {
					t.Fatal(err)
				}
				var journal map[string]any
				if err := json.Unmarshal(journalData, &journal); err != nil {
					t.Fatal(err)
				}
				journal["fingerprint"] = changedFingerprint
				writeJSONTestFile(t, journalPath, journal)

				statePath := filepath.Join(st.Dir(), "meta", "revisions", "state.json")
				stateData, err := os.ReadFile(statePath)
				if err != nil {
					t.Fatal(err)
				}
				var revisionState map[string]any
				if err := json.Unmarshal(stateData, &revisionState); err != nil {
					t.Fatal(err)
				}
				internalReceipt := revisionState["receipts"].(map[string]any)[key].(map[string]any)
				if internalReceipt["service_fingerprint"] != originalFingerprint || rawInternalFingerprint == "" {
					t.Fatal("legacy service fingerprint was absent from durable state")
				}
				originalBoundFingerprint := internalReceipt["fingerprint"].(string)
				changedBoundFingerprint := adaptationServiceBoundInternalFingerprintForTest(
					key, "publish", rawInternalFingerprint, "publish", changedFingerprint,
				)
				tamperedState := strings.ReplaceAll(string(stateData), originalFingerprint, changedFingerprint)
				tamperedState = strings.Replace(tamperedState, originalBoundFingerprint, changedBoundFingerprint, 1)
				if tamperedState == string(stateData) {
					t.Fatal("legacy service identity substitution did not change durable state")
				}
				if err := os.WriteFile(statePath, []byte(tamperedState), 0o644); err != nil {
					t.Fatal(err)
				}

				if serviceReceiptPresent {
					committed, err := st.Revisions.LoadSession(session.ID)
					if err != nil {
						t.Fatal(err)
					}
					serviceReceiptPath := filepath.Join(st.Dir(), "meta", "adaptation", "revision_service_receipts.json")
					writeJSONTestFile(t, serviceReceiptPath, map[string]any{
						"version": 1,
						"receipts": map[string]any{key: map[string]any{
							"operation": "publish", "fingerprint": changedFingerprint, "result": committed,
						}},
					})
				}

				restarted := storepkg.NewStore(st.Dir())
				if err := restarted.RecoverStructureMigration(); err == nil {
					t.Fatalf("legacy coherent substitution did not fail closed: %v", err)
				}
				if _, err := NewAdaptationRevisionService(restarted).Publish(changed, session, key); err == nil {
					t.Fatal("legacy coherent P' replay bypassed manual recovery")
				}
				request := adaptationCommandReceiptRequest("publish", session.Revision)
				request.Preview = &changed
				if _, _, err := NewAdaptationRevisionService(restarted).LoadCommandReceipt(request, key); err == nil {
					t.Fatal("legacy coherent P' LoadCommandReceipt bypassed manual recovery")
				}
				if err := restarted.WithPreparedAdaptationRevisionCommand(key, "publish", changedFingerprint, func(owner *storepkg.RevisionStore) error {
					return restarted.CompleteAdaptationRevisionCommand(owner, key, "publish", changedFingerprint)
				}); err == nil {
					t.Fatal("legacy coherent P' Complete bypassed manual recovery")
				}
				if after, err := os.ReadFile(receiptPath); err != nil || !reflect.DeepEqual(after, signedReceipt) {
					t.Fatalf("legacy signed receipt changed while failing closed: err=%v", err)
				}
				if _, err := os.Stat(journalPath); err != nil {
					t.Fatalf("legacy diagnostic journal was removed: %v", err)
				}
				assertAdaptationPublicationRemainedCommitted(t, st, session.ID, candidate, registryBefore)
				if registryAfter := snapshotPublicationAuthorityRegistry(t); !reflect.DeepEqual(registryCommitted, registryAfter) {
					t.Fatal("legacy failure changed protected authority facts")
				}
			})
		}
	}
}

func writeJSONTestFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func preparedAdaptationPublicationInternalFingerprint(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "meta", "revisions", "adaptation-command-journal.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var journal struct {
		Publication *struct {
			InternalReceiptFingerprint string `json:"internal_receipt_fingerprint"`
		} `json:"publication"`
	}
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	if journal.Publication == nil || journal.Publication.InternalReceiptFingerprint == "" {
		t.Fatal("prepared publication omitted its internal receipt fingerprint")
	}
	return journal.Publication.InternalReceiptFingerprint
}

func adaptationServiceBoundInternalFingerprintForTest(
	key, internalOperation, internalFingerprint, serviceOperation, serviceFingerprint string,
) string {
	payload, _ := json.Marshal(struct {
		Key                 string
		InternalOperation   string
		InternalFingerprint string
		ServiceOperation    string
		ServiceFingerprint  string
	}{
		Key:                 strings.TrimSpace(key),
		InternalOperation:   strings.TrimSpace(internalOperation),
		InternalFingerprint: strings.TrimSpace(internalFingerprint),
		ServiceOperation:    strings.TrimSpace(serviceOperation),
		ServiceFingerprint:  strings.TrimSpace(serviceFingerprint),
	})
	return domain.JSONContentSignature(payload)
}

func rewritePreparedAdaptationJournalAsV1(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "meta", "revisions", "adaptation-command-journal.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var current struct {
		Key, Operation, Fingerprint string
		Files                       []string
	}
	if err := json.Unmarshal(data, &current); err != nil {
		t.Fatal(err)
	}
	legacyFiles := make([]string, 0, len(current.Files))
	for _, rel := range current.Files {
		if rel == "meta/revisions/expansion-publication-trust.json" || rel == "meta/revisions/expansion-publication-receipt.json" {
			continue
		}
		legacyFiles = append(legacyFiles, rel)
	}
	legacy := struct {
		Version     int      `json:"version"`
		Key         string   `json:"key"`
		Operation   string   `json:"operation"`
		Fingerprint string   `json:"fingerprint"`
		Files       []string `json:"files"`
	}{1, current.Key, current.Operation, current.Fingerprint, legacyFiles}
	data, err = json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAdaptationRevisionTerminalPublishReceiptTamperFailsClosed(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(map[string]any, map[string]any, map[string]any)
	}{
		{name: "result id", mutate: func(_, _, result map[string]any) { result["id"] = "forged-session" }},
		{name: "result mode", mutate: func(_, _, result map[string]any) { result["mode"] = "normal" }},
		{name: "result stage", mutate: func(_, _, result map[string]any) { result["stage"] = "failed" }},
		{name: "result revision", mutate: func(_, _, result map[string]any) { result["revision"] = result["revision"].(float64) + 1 }},
		{name: "result generation", mutate: func(_, _, result map[string]any) { result["generation"] = result["generation"].(float64) + 1 }},
		{name: "session content", mutate: func(_, _, result map[string]any) { result["intent"] = "forged intent" }},
		{name: "operation", mutate: func(_, receipt, _ map[string]any) { receipt["operation"] = "cancel" }},
		{name: "fingerprint", mutate: func(_, receipt, _ map[string]any) { receipt["fingerprint"] = strings.Repeat("0", 64) }},
		{name: "state version", mutate: func(state, _, _ map[string]any) { state["version"] = float64(2) }},
		{name: "unknown state", mutate: func(state, _, _ map[string]any) { state["unexpected"] = true }},
		{name: "unknown receipt", mutate: func(_, receipt, _ map[string]any) { receipt["unexpected"] = true }},
		{name: "unknown result", mutate: func(_, _, result map[string]any) { result["unexpected"] = true }},
		{name: "partial result", mutate: func(_, _, result map[string]any) { delete(result, "policy_id") }},
		{name: "null result", mutate: func(_, receipt, _ map[string]any) { receipt["result"] = nil }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			st, service, preview, session, _ := prepareAdaptationServicePublication(t)
			key := "terminal-receipt-tamper"
			if _, err := service.Publish(preview, session, key); err != nil {
				t.Fatal(err)
			}
			assertPreparedAdaptationEvidenceAbsent(t, st.Dir())
			path := filepath.Join(st.Dir(), "meta", "adaptation", "revision_service_receipts.json")
			var state map[string]any
			data, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(data, &state) != nil {
				t.Fatal(err)
			}
			receipt := state["receipts"].(map[string]any)[key].(map[string]any)
			result := receipt["result"].(map[string]any)
			mutation.mutate(state, receipt, result)
			tampered, err := json.MarshalIndent(state, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			tampered = append(tampered, '\n')
			if err := os.WriteFile(path, tampered, 0o644); err != nil {
				t.Fatal(err)
			}
			formalBefore, err := st.CaptureAdaptationFormalSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			registryBefore := snapshotPublicationAuthorityRegistry(t)
			if _, err := service.Publish(preview, session, key); err == nil {
				t.Fatal("tampered terminal publish receipt was returned")
			}
			if after, err := os.ReadFile(path); err != nil || !reflect.DeepEqual(after, tampered) {
				t.Fatalf("tampered diagnostic receipt changed: err=%v", err)
			}
			formalAfter, err := st.CaptureAdaptationFormalSnapshot()
			if err != nil || !reflect.DeepEqual(formalBefore, formalAfter) {
				t.Fatalf("tampered receipt replay changed formal facts: err=%v", err)
			}
			if registryAfter := snapshotPublicationAuthorityRegistry(t); !reflect.DeepEqual(registryBefore, registryAfter) {
				t.Fatal("tampered receipt replay changed authority facts")
			}
		})
	}

	for _, mutation := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "duplicate JSON", mutate: func(data []byte) []byte {
			return []byte(strings.Replace(string(data), "\"version\": 1,", "\"version\": 1, \"version\": 1,", 1))
		}},
		{name: "duplicate result JSON", mutate: func(data []byte) []byte {
			return []byte(strings.Replace(string(data), "\"id\":", "\"id\": \"duplicate\", \"id\":", 1))
		}},
		{name: "multiple JSON values", mutate: func(data []byte) []byte { return append(data, []byte("{}\n")...) }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			st, service, preview, session, _ := prepareAdaptationServicePublication(t)
			key := "terminal-receipt-json-tamper"
			if _, err := service.Publish(preview, session, key); err != nil {
				t.Fatal(err)
			}
			assertPreparedAdaptationEvidenceAbsent(t, st.Dir())
			path := filepath.Join(st.Dir(), "meta", "adaptation", "revision_service_receipts.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			tampered := mutation.mutate(data)
			if err := os.WriteFile(path, tampered, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Publish(preview, session, key); err == nil {
				t.Fatal("invalid terminal receipt JSON was returned")
			}
			if after, err := os.ReadFile(path); err != nil || !reflect.DeepEqual(after, tampered) {
				t.Fatalf("invalid terminal receipt evidence changed: err=%v", err)
			}
		})
	}
}

func TestAdaptationRevisionTerminalPublishDurableFactTamperFailsClosed(t *testing.T) {
	for _, fact := range []string{"internal receipt", "formal plan", "publication trust", "publication receipt", "authority record"} {
		t.Run(fact, func(t *testing.T) {
			st, service, preview, session, _ := prepareAdaptationServicePublication(t)
			key := "terminal-fact-tamper"
			registryBeforePublish := snapshotPublicationAuthorityRegistry(t)
			published, err := service.Publish(preview, session, key)
			if err != nil || published == nil {
				t.Fatal(err)
			}
			assertPreparedAdaptationEvidenceAbsent(t, st.Dir())
			formalBefore, err := st.CaptureAdaptationFormalSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			registryCommitted := snapshotPublicationAuthorityRegistry(t)

			var restore func()
			switch fact {
			case "internal receipt":
				path := filepath.Join(st.Dir(), "meta", "revisions", "state.json")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var state map[string]any
				if err := json.Unmarshal(data, &state); err != nil {
					t.Fatal(err)
				}
				receipts := state["receipts"].(map[string]any)
				internalFingerprint := receipts[key].(map[string]any)["fingerprint"].(string)
				tampered := strings.Replace(string(data), internalFingerprint, strings.Repeat("0", 64), 1)
				if tampered == string(data) {
					t.Fatal("internal receipt fingerprint not found")
				}
				if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
					t.Fatal(err)
				}
				restore = func() { _ = os.WriteFile(path, data, 0o644) }
			case "formal plan":
				path := filepath.Join(st.Dir(), "meta", "adaptation", "plan.json")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				restore = func() { _ = os.WriteFile(path, data, 0o644) }
			case "publication trust", "publication receipt":
				name := "expansion-publication-trust.json"
				if fact == "publication receipt" {
					name = "expansion-publication-receipt.json"
				}
				path := filepath.Join(st.Dir(), "meta", "revisions", name)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var document map[string]any
				if err := json.Unmarshal(data, &document); err != nil {
					t.Fatal(err)
				}
				document["project_id"] = "forged-project"
				tampered, _ := json.MarshalIndent(document, "", "  ")
				if err := os.WriteFile(path, append(tampered, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				restore = func() { _ = os.WriteFile(path, data, 0o644) }
			case "authority record":
				var recordName string
				for name := range registryCommitted {
					if _, existed := registryBeforePublish[name]; !existed {
						recordName = name
						break
					}
				}
				if recordName == "" {
					t.Fatal("committed authority record was not identified")
				}
				path := filepath.Join(publicationTestAuthorityRoot, "projects", recordName)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("tampered-authority-record"), 0o600); err != nil {
					t.Fatal(err)
				}
				restore = func() { _ = os.WriteFile(path, data, 0o600) }
			}

			if _, err := service.Publish(preview, session, key); err == nil {
				restore()
				t.Fatalf("%s tamper returned a terminal publish result", fact)
			}
			restore()
			formalAfter, err := st.CaptureAdaptationFormalSnapshot()
			if err != nil || !reflect.DeepEqual(formalBefore, formalAfter) {
				t.Fatalf("%s tamper replay changed formal facts after restore: err=%v", fact, err)
			}
			if registryAfter := snapshotPublicationAuthorityRegistry(t); !reflect.DeepEqual(registryCommitted, registryAfter) {
				t.Fatalf("%s tamper replay changed authority facts", fact)
			}
		})
	}
}

func TestAdaptationRevisionTerminalPublishExactReplayAfterPreparedCleanup(t *testing.T) {
	st, service, preview, session, _ := prepareAdaptationServicePublication(t)
	key := "terminal-exact-replay"
	published, err := service.Publish(preview, session, key)
	if err != nil {
		t.Fatal(err)
	}
	assertPreparedAdaptationEvidenceAbsent(t, st.Dir())
	lease, err := st.Revisions.AcquireNormalFlow("terminal-exact-replay-successor")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Revisions.ReleaseNormalFlow(lease.Token); err != nil {
		t.Fatal(err)
	}
	replayed, err := service.Publish(preview, session, key)
	if err != nil || !reflect.DeepEqual(published, replayed) {
		t.Fatalf("same-process terminal exact replay=%+v err=%v", replayed, err)
	}
	restarted, err := NewAdaptationRevisionService(storepkg.NewStore(st.Dir())).Publish(preview, session, key)
	if err != nil || !reflect.DeepEqual(published, restarted) {
		t.Fatalf("restart terminal exact replay=%+v err=%v", restarted, err)
	}
	changed := preview
	changed.Stage = domain.ManuscriptStageWriting
	if _, err := NewAdaptationRevisionService(storepkg.NewStore(st.Dir())).Publish(changed, session, key); err == nil {
		t.Fatal("same-key different terminal fingerprint did not conflict")
	}
}

func TestAdaptationRevisionTerminalPublishServiceIdentityBindingFailsClosed(t *testing.T) {
	t.Run("coherent payload fingerprint substitution", func(t *testing.T) {
		st, service, preview, session, _ := prepareAdaptationServicePublication(t)
		key := "terminal-coherent-substitution"
		if _, err := service.Publish(preview, session, key); err != nil {
			t.Fatal(err)
		}
		assertPreparedAdaptationEvidenceAbsent(t, st.Dir())
		changed := preview
		changed.Stage = domain.ManuscriptStageWriting
		changedFingerprint := adaptationPublishServiceFingerprint(t, changed, session)
		serviceReceiptPath := filepath.Join(st.Dir(), "meta", "adaptation", "revision_service_receipts.json")
		data, err := os.ReadFile(serviceReceiptPath)
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]any
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		state["receipts"].(map[string]any)[key].(map[string]any)["fingerprint"] = changedFingerprint
		tampered, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		tampered = append(tampered, '\n')
		if err := os.WriteFile(serviceReceiptPath, tampered, 0o644); err != nil {
			t.Fatal(err)
		}
		formalBefore, err := st.CaptureAdaptationFormalSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		authorityBefore := snapshotPublicationAuthorityRegistry(t)
		if _, err := service.Publish(changed, session, key); err == nil {
			t.Fatal("same-process coherent service fingerprint substitution was accepted")
		}
		if _, err := NewAdaptationRevisionService(storepkg.NewStore(st.Dir())).Publish(changed, session, key); err == nil {
			t.Fatal("restart coherent service fingerprint substitution was accepted")
		}
		request := adaptationCommandReceiptRequest("publish", session.Revision)
		request.Preview = &changed
		if _, _, err := NewAdaptationRevisionService(storepkg.NewStore(st.Dir())).LoadCommandReceipt(request, key); err == nil {
			t.Fatal("LoadCommandReceipt accepted coherent service fingerprint substitution")
		}
		if after, err := os.ReadFile(serviceReceiptPath); err != nil || !reflect.DeepEqual(after, tampered) {
			t.Fatalf("coherent tamper evidence changed: err=%v", err)
		}
		formalAfter, err := st.CaptureAdaptationFormalSnapshot()
		if err != nil || !reflect.DeepEqual(formalBefore, formalAfter) {
			t.Fatalf("coherent substitution changed formal facts: err=%v", err)
		}
		if authorityAfter := snapshotPublicationAuthorityRegistry(t); !reflect.DeepEqual(authorityBefore, authorityAfter) {
			t.Fatal("coherent substitution changed protected authority facts")
		}
	})

	t.Run("key remap", func(t *testing.T) {
		st, service, preview, session, _ := prepareAdaptationServicePublication(t)
		key, remapped := "terminal-key-binding", "terminal-key-remapped"
		if _, err := service.Publish(preview, session, key); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(st.Dir(), "meta", "adaptation", "revision_service_receipts.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]any
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		receipts := state["receipts"].(map[string]any)
		receipts[remapped] = receipts[key]
		delete(receipts, key)
		tampered, _ := json.MarshalIndent(state, "", "  ")
		if err := os.WriteFile(path, append(tampered, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := NewAdaptationRevisionService(storepkg.NewStore(st.Dir())).Publish(preview, session, remapped); err == nil {
			t.Fatal("service receipt key remap bypassed signed publication identity")
		}
	})

	t.Run("binding digest", func(t *testing.T) {
		st, service, preview, session, _ := prepareAdaptationServicePublication(t)
		key := "terminal-binding-digest"
		if _, err := service.Publish(preview, session, key); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(st.Dir(), "meta", "revisions", "expansion-publication-receipt.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var receipt map[string]any
		if err := json.Unmarshal(data, &receipt); err != nil {
			t.Fatal(err)
		}
		if binding, _ := receipt["adaptation_service_binding"].(string); len(binding) != 64 {
			t.Fatalf("new adaptation publication omitted service binding: %q", binding)
		}
		receipt["adaptation_service_binding"] = strings.Repeat("0", 64)
		tampered, _ := json.MarshalIndent(receipt, "", "  ")
		if err := os.WriteFile(path, append(tampered, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Publish(preview, session, key); err == nil {
			t.Fatal("tampered signed service binding was accepted")
		}
	})
}

func assertPreparedAdaptationEvidenceAbsent(t *testing.T, dir string) {
	t.Helper()
	for _, path := range []string{
		filepath.Join(dir, "meta", "revisions", "adaptation-command-journal.json"),
		filepath.Join(dir, "meta", "revisions", "adaptation-command-snapshot"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("prepared adaptation evidence remains after terminal commit: %s err=%v", filepath.Base(path), err)
		}
	}
}

func assertAdaptationPublicationRemainedCommitted(
	t *testing.T,
	st *storepkg.Store,
	sessionID string,
	candidate domain.AdaptationPlan,
	registryBefore map[string]publicationAuthorityRegistrySnapshot,
) {
	t.Helper()
	formal, err := st.Adaptation.LoadPlan()
	if err != nil || formal == nil || len(formal.Chapters) != len(candidate.Chapters) ||
		formal.Chapters[len(formal.Chapters)-1].ID != candidate.Chapters[len(candidate.Chapters)-1].ID {
		t.Fatalf("committed formal adaptation rolled back: plan=%+v err=%v", formal, err)
	}
	committed, err := st.Revisions.LoadSession(sessionID)
	if err != nil || committed == nil || committed.Stage != domain.RevisionStageCompleted || committed.Generation == 0 {
		t.Fatalf("committed internal revision rolled back: session=%+v err=%v", committed, err)
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil || progress.TotalChapters != len(candidate.Chapters) {
		t.Fatalf("committed progress rolled back: progress=%+v err=%v", progress, err)
	}
	registryAfter := snapshotPublicationAuthorityRegistry(t)
	if len(registryAfter) != len(registryBefore)+1 {
		t.Fatalf("committed authority record count before=%d after=%d", len(registryBefore), len(registryAfter))
	}
}

func prepareAdaptationServicePublication(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, AdaptationStructureRevisionPreview, *domain.RevisionSession, domain.AdaptationPlan) {
	t.Helper()
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageProposalComplete, domain.AdaptationGranularityChapter, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append a committed bridge chapter", Candidate: candidate}, "preview-finalize")
	if err != nil {
		t.Fatal(err)
	}
	session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
	return st, service, *previewed.Preview, session, candidate
}

func TestAdaptationRevisionRuntimeTransitionsAreRollbackSafeAndSerialized(t *testing.T) {
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
	if err != nil {
		t.Fatal(err)
	}
	session, err = service.SubmitStructureCandidate(*previewed.Preview, session, "structure")
	if err != nil {
		t.Fatal(err)
	}
	completeAdaptationRuntime(t, service, session.ID)
	session = passAdaptationAuditAndApprove(t, service, session, "structure")
	before, _ := st.Adaptation.LoadRevisionRuntime()
	stale := *session
	stale.Revision--
	if _, err := service.SubmitDetailedOutlineCandidate(candidate, &stale, "stale-details"); err == nil {
		t.Fatal("stale candidate submission unexpectedly succeeded")
	}
	after, _ := st.Adaptation.LoadRevisionRuntime()
	if !reflect.DeepEqual(before.BatchPlan, after.BatchPlan) {
		t.Fatalf("failed candidate left a new BatchPlan: before=%+v after=%+v", before.BatchPlan, after.BatchPlan)
	}

	service.saveRevisionRuntime = func(domain.AdaptationRevisionRuntime) error {
		return errors.New("injected runtime persistence failure")
	}
	if _, err := service.Pause(session, "pause-fail"); err == nil {
		t.Fatal("pause persistence failure was not returned")
	}
	active, _ := st.Revisions.Active()
	after, _ = st.Adaptation.LoadRevisionRuntime()
	if active.Stage == domain.RevisionStagePaused || after.Paused {
		t.Fatalf("failed pause split session/runtime: active=%+v runtime=%+v", active, after)
	}
	service.saveRevisionRuntime = nil
	paused, err := service.Pause(session, "pause")
	if err != nil {
		t.Fatal(err)
	}
	service.saveRevisionRuntime = func(domain.AdaptationRevisionRuntime) error {
		return errors.New("injected runtime persistence failure")
	}
	if _, err := service.Resume(paused, "resume-fail"); err == nil {
		t.Fatal("resume persistence failure was not returned")
	}
	active, _ = st.Revisions.Active()
	after, _ = st.Adaptation.LoadRevisionRuntime()
	if active.Stage != domain.RevisionStagePaused || !after.Paused {
		t.Fatalf("failed resume split session/runtime: active=%+v runtime=%+v", active, after)
	}
	restarted := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
	resumed, err := restarted.Resume(active, "resume-after-restart")
	if err != nil || resumed.Stage == domain.RevisionStagePaused {
		t.Fatalf("restart resume failed: resumed=%+v err=%v", resumed, err)
	}
	failed, err := restarted.Fail(resumed, "model failure", "fail-session")
	if err != nil || failed.Stage != domain.RevisionStageFailed {
		t.Fatalf("persist failure checkpoint: failed=%+v err=%v", failed, err)
	}
	restarted = NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
	resumed, err = restarted.Resume(failed, "resume-failed-after-restart")
	if err != nil || resumed.Stage == domain.RevisionStageFailed {
		t.Fatalf("restart resume from failure failed: resumed=%+v err=%v", resumed, err)
	}

	if _, err := restarted.RunBatchCommand(resumed.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 16)
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, commandErr := restarted.RunBatchCommand(resumed.ID, domain.AdaptationRevisionBatchFail, "adaptation-batch-001", "concurrent failure")
			results <- commandErr
		}()
	}
	wait.Wait()
	close(results)
	successes := 0
	for commandErr := range results {
		if commandErr == nil {
			successes++
		}
	}
	after, _ = st.Adaptation.LoadRevisionRuntime()
	if successes != 1 || after.BatchPlan.Batches[0].Attempts != 1 || after.BatchPlan.Batches[0].Status != domain.BatchStatusFailed {
		t.Fatalf("concurrent checkpoint commands lost status/attempts: successes=%d runtime=%+v", successes, after.BatchPlan.Batches[0])
	}
}

func TestAdaptationRevisionTwoServiceRacesDoNotResurrectRuntimeOrLoseAttempts(t *testing.T) {
	t.Run("approve preserves concurrent batch attempt", func(t *testing.T) {
		st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		first := NewAdaptationRevisionService(st)
		previewed, err := first.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := first.RunBatchCommand(previewed.Session.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
			t.Fatal(err)
		}
		second := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
		batchErr, approveErr := runConcurrentAdaptationCommands(
			func() error {
				_, err := first.RunBatchCommand(previewed.Session.ID, domain.AdaptationRevisionBatchFail, "adaptation-batch-001", "model failure")
				return err
			},
			func() error {
				_, err := second.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "approve")
				return err
			},
		)
		if batchErr != nil || approveErr != nil {
			t.Fatalf("serialized approve/batch commands failed: batch=%v approve=%v", batchErr, approveErr)
		}
		runtime, _ := st.Adaptation.LoadRevisionRuntime()
		active, _ := st.Revisions.Active()
		if runtime == nil || runtime.BatchPlan.Batches[0].Attempts != 1 || runtime.BatchPlan.Batches[0].Status != domain.BatchStatusFailed || active == nil || active.Revision != previewed.Session.Revision+1 {
			t.Fatalf("approve race lost batch/session state: runtime=%+v active=%+v", runtime, active)
		}
	})

	for _, transition := range []struct {
		name string
		run  func(*AdaptationRevisionService, *domain.RevisionSession) error
		want domain.RevisionStage
	}{
		{name: "pause", want: domain.RevisionStagePaused, run: func(service *AdaptationRevisionService, session *domain.RevisionSession) error {
			_, err := service.Pause(session, "pause")
			return err
		}},
		{name: "fail", want: domain.RevisionStageFailed, run: func(service *AdaptationRevisionService, session *domain.RevisionSession) error {
			_, err := service.Fail(session, "session failure", "fail")
			return err
		}},
	} {
		t.Run(transition.name+" serializes batch attempt", func(t *testing.T) {
			st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
			first := NewAdaptationRevisionService(st)
			previewed, err := first.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
			if err != nil {
				t.Fatal(err)
			}
			session, err := first.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := first.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
				t.Fatal(err)
			}
			second := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
			batchErr, transitionErr := runConcurrentAdaptationCommands(
				func() error {
					_, err := first.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchFail, "adaptation-batch-001", "batch failure")
					return err
				},
				func() error { return transition.run(second, session) },
			)
			if transitionErr != nil {
				t.Fatalf("%s transition failed: %v", transition.name, transitionErr)
			}
			runtime, _ := st.Adaptation.LoadRevisionRuntime()
			active, _ := st.Revisions.Active()
			wantStatus := domain.BatchStatusGenerating
			if batchErr == nil {
				wantStatus = domain.BatchStatusFailed
			}
			if runtime == nil || !runtime.Paused || runtime.BatchPlan.Batches[0].Attempts != 1 || runtime.BatchPlan.Batches[0].Status != wantStatus || active == nil || active.Stage != transition.want {
				t.Fatalf("%s race split state: batchErr=%v runtime=%+v active=%+v", transition.name, batchErr, runtime, active)
			}
		})
	}

	t.Run("cancel cannot be followed by stale runtime save", func(t *testing.T) {
		st, base, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		first := NewAdaptationRevisionService(st)
		previewed, err := first.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
		if err != nil {
			t.Fatal(err)
		}
		session, err := first.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := first.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
			t.Fatal(err)
		}
		second := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
		_, cancelErr := runConcurrentAdaptationCommands(
			func() error {
				_, err := first.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchFail, "adaptation-batch-001", "batch failure")
				return err
			},
			func() error {
				_, err := second.Cancel(session, "cancel")
				return err
			},
		)
		if cancelErr != nil {
			t.Fatal(cancelErr)
		}
		runtime, _ := st.Adaptation.LoadRevisionRuntime()
		active, _ := st.Revisions.Active()
		formal, _ := st.Adaptation.LoadPlan()
		if runtime != nil || active != nil || formal == nil || !reflect.DeepEqual(*formal, base) {
			t.Fatalf("cancel race resurrected runtime or drifted formal state: runtime=%+v active=%+v formal=%+v", runtime, active, formal)
		}
	})

	t.Run("cancel runtime cleanup failure keeps the session active", func(t *testing.T) {
		st, base, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		service := NewAdaptationRevisionService(st)
		previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
		if err != nil {
			t.Fatal(err)
		}
		session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
		if err != nil {
			t.Fatal(err)
		}
		service.clearRevisionRuntime = func(string) error { return errors.New("injected runtime cleanup failure") }
		if _, err := service.Cancel(session, "cancel"); err == nil {
			t.Fatal("cancel ignored runtime cleanup failure")
		}
		runtime, _ := st.Adaptation.LoadRevisionRuntime()
		active, _ := st.Revisions.Active()
		formal, _ := st.Adaptation.LoadPlan()
		if runtime == nil || active == nil || active.ID != session.ID || active.Stage != session.Stage || formal == nil || !reflect.DeepEqual(*formal, base) {
			t.Fatalf("failed cancel split session/runtime/formal state: runtime=%+v active=%+v formal=%+v", runtime, active, formal)
		}
	})

	t.Run("failed runtime save cannot overwrite pause", func(t *testing.T) {
		st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		first := NewAdaptationRevisionService(st)
		previewed, err := first.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
		if err != nil {
			t.Fatal(err)
		}
		session, err := first.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := first.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
			t.Fatal(err)
		}
		first.saveRevisionRuntime = func(domain.AdaptationRevisionRuntime) error { return errors.New("injected runtime save failure") }
		second := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
		batchErr, pauseErr := runConcurrentAdaptationCommands(
			func() error {
				_, err := first.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchFail, "adaptation-batch-001", "not persisted")
				return err
			},
			func() error {
				_, err := second.Pause(session, "pause")
				return err
			},
		)
		if batchErr == nil || pauseErr != nil {
			t.Fatalf("persistence race results: batch=%v pause=%v", batchErr, pauseErr)
		}
		runtime, _ := st.Adaptation.LoadRevisionRuntime()
		active, _ := st.Revisions.Active()
		if runtime == nil || !runtime.Paused || runtime.BatchPlan.Batches[0].Attempts != 1 || runtime.BatchPlan.Batches[0].Status != domain.BatchStatusGenerating || active == nil || active.Stage != domain.RevisionStagePaused {
			t.Fatalf("failed runtime save split or resurrected state: runtime=%+v active=%+v", runtime, active)
		}
	})

	t.Run("publish cannot be followed by stale runtime save", func(t *testing.T) {
		st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		first := NewAdaptationRevisionService(st)
		previewed, err := first.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
		if err != nil {
			t.Fatal(err)
		}
		session := runAdaptationStructureAndDetailApproval(t, first, *previewed.Preview, previewed.Session, candidate)
		second := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
		_, publishErr := runConcurrentAdaptationCommands(
			func() error {
				_, err := first.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchFail, "adaptation-batch-001", "stale failure")
				return err
			},
			func() error {
				_, err := second.Publish(*previewed.Preview, session, "publish")
				return err
			},
		)
		if publishErr != nil {
			t.Fatal(publishErr)
		}
		runtime, _ := st.Adaptation.LoadRevisionRuntime()
		active, _ := st.Revisions.Active()
		formal, _ := st.Adaptation.LoadPlan()
		if runtime != nil || active != nil || formal == nil || len(formal.Chapters) != len(candidate.Chapters) {
			t.Fatalf("publish race resurrected runtime or lost formal publish: runtime=%+v active=%+v formal=%+v", runtime, active, formal)
		}
	})
}

func runConcurrentAdaptationCommands(left, right func() error) (error, error) {
	start := make(chan struct{})
	results := make(chan struct {
		index int
		err   error
	}, 2)
	for index, command := range []func() error{left, right} {
		go func(index int, command func() error) {
			<-start
			results <- struct {
				index int
				err   error
			}{index: index, err: command()}
		}(index, command)
	}
	close(start)
	errs := make([]error, 2)
	for range 2 {
		result := <-results
		errs[result.index] = result.err
	}
	return errs[0], errs[1]
}

func TestAdaptationRevisionServiceRejectsWrittenMoveAndQueuesExactRework(t *testing.T) {
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, true)
	service := NewAdaptationRevisionService(st)
	moved := adaptationRevisionTestClone(t, candidate)
	moved.Chapters[0], moved.Chapters[1] = moved.Chapters[1], moved.Chapters[0]
	for index := range moved.Chapters {
		moved.Chapters[index].Chapter = index + 1
		moved.Volumes[0].TargetTo = len(moved.Chapters)
	}
	if _, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "move written chapter", Candidate: moved}, "move"); err == nil || !strings.Contains(err.Error(), "cannot be moved") {
		t.Fatalf("written move was not structurally rejected: %v", err)
	}

	candidate = adaptationRevisionTestClone(t, candidate)
	candidate.Chapters[0].CoreEvent = "revised exact written event"
	candidate.Chapters[0].OutlineEntry.CoreEvent = candidate.Chapters[0].CoreEvent
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "revise exact written chapter", Candidate: candidate}, "rework-preview")
	if err != nil {
		t.Fatal(err)
	}
	session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
	if adaptationServiceApprovalStage(*session) != domain.AdaptationApprovalProse {
		t.Fatalf("missing exact prose approval stage: %+v", session)
	}
	session, err = service.SubmitProseReworkCandidate(session, "prose")
	if err != nil {
		t.Fatal(err)
	}
	if len(session.AuditExpectations) != 2 || session.AuditExpectations[0].ScopeID != adaptationTestChapter1 {
		t.Fatalf("prose intents are not exact stable-ID scopes: %+v", session.AuditExpectations)
	}
}

func TestAdaptationRevisionServiceExcludesConcurrentRevisionUntilPublishReceipt(t *testing.T) {
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageComplete, domain.AdaptationGranularityFree, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "complete-book expansion", Candidate: candidate}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
	policy, _, err := service.boundPolicy(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var pauseErr error
	service.beforeRevisionCommit = func() {
		_, pauseErr = st.Revisions.Pause(policy, storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "concurrent-pause"})
	}
	if _, err := service.Publish(*previewed.Preview, session, "publish"); err != nil {
		t.Fatalf("fenced publication failed: %v", err)
	}
	if !errors.Is(pauseErr, storepkg.ErrRevisionCommandInProgress) {
		t.Fatalf("competing revision entered publication before its receipt: %v", pauseErr)
	}
	formal, _ := st.Adaptation.LoadPlan()
	active, _ := st.Revisions.Active()
	if formal == nil || len(formal.Chapters) != len(candidate.Chapters) ||
		formal.Chapters[len(formal.Chapters)-1].ID != candidate.Chapters[len(candidate.Chapters)-1].ID || active != nil {
		t.Fatalf("fenced publication did not commit exactly: formal=%+v active=%+v", formal, active)
	}
}

func TestAdaptationRevisionPublishRuntimeCleanupFailureRollsBackBeforeCommit(t *testing.T) {
	st, base, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageComplete, domain.AdaptationGranularityArc, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "complete-book expansion", Candidate: candidate}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
	progressBefore, _ := st.Progress.Load()
	service.clearRevisionRuntime = func(string) error { return errors.New("injected runtime cleanup failure") }
	if _, err := service.Publish(*previewed.Preview, session, "publish"); err == nil {
		t.Fatal("publish ignored runtime cleanup failure")
	}
	formal, _ := st.Adaptation.LoadPlan()
	progressAfter, _ := st.Progress.Load()
	runtime, _ := st.Adaptation.LoadRevisionRuntime()
	active, _ := st.Revisions.Active()
	if formal == nil || !reflect.DeepEqual(*formal, base) || !reflect.DeepEqual(progressAfter, progressBefore) || runtime == nil || active == nil || active.ID != session.ID || active.Revision != session.Revision {
		t.Fatalf("runtime cleanup failure split publish state: formal=%+v progress=%+v runtime=%+v active=%+v", formal, progressAfter, runtime, active)
	}
}

func TestAdaptationRevisionServiceBlocksLegacyWritesAndNormalProjects(t *testing.T) {
	normal := storepkg.NewStore(t.TempDir())
	if _, err := NewAdaptationRevisionService(normal).CurrentManuscriptStage(); err == nil {
		t.Fatal("adaptation service accepted a normal project")
	}
	st, base, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityChapter, false)
	service := NewAdaptationRevisionService(st)
	if _, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview"); err != nil {
		t.Fatal(err)
	}
	formalBefore, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{store: st}
	if _, err := h.Rollback(domain.RollbackRequest{Confirm: true}); err == nil {
		t.Fatal("Host rollback bypassed the active adaptation revision")
	}
	formalAfter, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil || !reflect.DeepEqual(formalBefore, formalAfter) {
		t.Fatalf("rejected Host rollback changed formal state: err=%v", err)
	}
	if err := st.Adaptation.SavePlan(base); err == nil || !strings.Contains(err.Error(), "blocked by active revision") {
		t.Fatalf("legacy formal plan write was not blocked: %v", err)
	}
	if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageConfirmed, -1); err == nil || !strings.Contains(err.Error(), "blocked by active revision") {
		t.Fatalf("legacy workflow write was not blocked: %v", err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{}); err == nil || !strings.Contains(err.Error(), "blocked by active revision") {
		t.Fatalf("immutable source replacement was not blocked: %v", err)
	}
	for operation, mutate := range map[string]func() error{
		"reset":                st.Adaptation.Reset,
		"reset generated":      st.Adaptation.ResetGenerated,
		"delete check":         func() error { return st.Adaptation.DeleteCheck(1) },
		"clear source batches": st.Adaptation.ClearSourceFoundationBatches,
	} {
		if err := mutate(); err == nil || !strings.Contains(err.Error(), "blocked by active revision") {
			t.Fatalf("legacy %s bypass was not blocked: %v", operation, err)
		}
	}
}

func TestAdaptationRevisionServiceRejectsRestartAfterManifestTamper(t *testing.T) {
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := st.Adaptation.LoadSourceManifest()
	manifest.Chapters[0].SHA256 = "tampered"
	payload, _ := json.MarshalIndent(manifest, "", "  ")
	path := filepath.Join(st.Dir(), "meta", "adaptation", "source_manifest.json")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	restarted := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
	if _, err := restarted.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact"); err == nil || !strings.Contains(err.Error(), "restart binding") {
		t.Fatalf("manifest drift was not rejected after restart: %v", err)
	}
}

func runAdaptationStructureAndDetailApproval(t *testing.T, service *AdaptationRevisionService, preview AdaptationStructureRevisionPreview, session *domain.RevisionSession, candidate domain.AdaptationPlan) *domain.RevisionSession {
	t.Helper()
	var err error
	session, err = service.ApproveImpact(session.ID, session.Revision, "impact")
	if err != nil {
		t.Fatal(err)
	}
	if adaptationServiceApprovalStage(*session) == domain.AdaptationApprovalStructure {
		session, err = service.SubmitStructureCandidate(preview, session, "structure")
		if err != nil {
			t.Fatal(err)
		}
		completeAdaptationRuntime(t, service, session.ID)
		session = passAdaptationAuditAndApprove(t, service, session, "structure")
	}
	if adaptationServiceApprovalStage(*session) == domain.AdaptationApprovalOutline {
		session, err = service.SubmitDetailedOutlineCandidate(candidate, session, "details")
		if err != nil {
			t.Fatal(err)
		}
		completeAdaptationRuntime(t, service, session.ID)
		session = passAdaptationAuditAndApprove(t, service, session, "details")
	}
	return session
}

func completeAdaptationRuntime(t *testing.T, service *AdaptationRevisionService, sessionID string) {
	t.Helper()
	runtime, err := service.store.Adaptation.LoadRevisionRuntime()
	if err != nil {
		t.Fatal(err)
	}
	for _, batch := range runtime.BatchPlan.Batches {
		if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionBatchStart, batch.ID, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionBatchGenerated, batch.ID, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionBatchAuditPass, batch.ID, "passed"); err != nil {
			t.Fatal(err)
		}
	}
	for _, review := range runtime.BatchPlan.VolumeReviews {
		if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionVolumeReviewStart, review.ScopeID, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionVolumeReviewPass, review.ScopeID, "passed"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionGlobalReviewStart, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionGlobalReviewPass, "", "passed"); err != nil {
		t.Fatal(err)
	}
}

func passAdaptationAuditAndApprove(t *testing.T, service *AdaptationRevisionService, session *domain.RevisionSession, prefix string) *domain.RevisionSession {
	t.Helper()
	evidence := make([]domain.RevisionAuditEvidence, 0, len(session.AuditExpectations))
	for _, expected := range session.AuditExpectations {
		evidence = append(evidence, domain.RevisionAuditEvidence{Scope: expected.Scope, ScopeID: expected.ScopeID, FromChapter: expected.FromChapter, ToChapter: expected.ToChapter, ContentSignature: expected.ContentSignature, Passed: true})
	}
	audited, err := service.RecordAuditSet(session, evidence, prefix+"-audit")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := service.ApproveStage(audited, prefix+"-approve")
	if err != nil {
		t.Fatal(err)
	}
	return approved
}

func seedAdaptationRevisionProject(t *testing.T, stage domain.ManuscriptStage, granularity string, completed bool) (*storepkg.Store, domain.AdaptationPlan, domain.AdaptationPlan) {
	t.Helper()
	st := newPublicationTestStore(t)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	base, manifest := adaptationRevisionServiceFixture(granularity)
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatal(err)
	}
	for _, source := range manifest.Chapters {
		events := make([]domain.AdaptationEvent, 0, 1)
		for _, event := range base.SourceEvents {
			if event.SourceChapter == source.Chapter {
				events = append(events, event)
			}
		}
		if err := st.Adaptation.SaveSourceReport(domain.AdaptationSourceReport{Chapter: source.Chapter, Title: source.Title, SourceSHA256: source.SHA256, Summary: source.Title, KeyEvents: []string{source.Title}, SourceEvents: events}); err != nil {
			t.Fatal(err)
		}
	}
	fullBase := adaptationRevisionTestClone(t, base)
	var basePtr *domain.AdaptationPlan
	var err error
	switch stage {
	case domain.ManuscriptStageProposalComplete:
		review := domain.AdaptationVolumeReview{Status: domain.AdaptationPlanStatusVolumeReview, Brief: base.Brief, SourceChapterCount: manifest.ChapterCount, Granularity: base.Granularity, RewritePolicy: base.RewritePolicy, WordTolerance: base.WordTolerance, TargetChapterCount: len(base.Chapters), MainlineRules: base.MainlineRules, RelationshipGoals: base.RelationshipGoals, Volumes: base.Volumes}
		if err := st.Adaptation.SaveVolumeReview(review); err != nil {
			t.Fatal(err)
		}
		basePtr, err = adaptationPlanFromVolumeReview(review, manifest, []domain.AdaptationSourceReport{
			{Chapter: 1, SourceEvents: []domain.AdaptationEvent{base.SourceEvents[0]}},
			{Chapter: 2, SourceEvents: []domain.AdaptationEvent{base.SourceEvents[1]}},
		})
	case domain.ManuscriptStageOutlineComplete:
		if err := st.Adaptation.SaveProposal(base); err != nil {
			t.Fatal(err)
		}
		basePtr, err = st.Adaptation.LoadProposal()
	default:
		if err := st.Adaptation.SavePlan(base); err != nil {
			t.Fatal(err)
		}
		basePtr, err = st.Adaptation.LoadPlan()
	}
	if err != nil || basePtr == nil {
		t.Fatalf("load stage adaptation contract: plan=%+v err=%v", basePtr, err)
	}
	base = *basePtr
	progress := &domain.Progress{NovelName: "adaptation", Phase: domain.PhaseInit, TotalChapters: 2, ChapterWordCounts: map[int]int{}}
	switch stage {
	case domain.ManuscriptStageProposalComplete:
		progress.Phase = domain.PhaseOutline
		if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageVolumeReviewPending, -1); err != nil {
			t.Fatal(err)
		}
	case domain.ManuscriptStageOutlineComplete:
		progress.Phase = domain.PhaseOutline
	case domain.ManuscriptStageWriting:
		progress.Phase, progress.Flow = domain.PhaseWriting, domain.FlowWriting
	case domain.ManuscriptStageComplete:
		progress.Phase, progress.Flow = domain.PhaseComplete, domain.FlowWriting
	}
	if completed {
		progress.CompletedChapters = []int{1}
		progress.ChapterWordCounts[1] = 100
		if err := st.Drafts.SaveFinalChapter(1, "completed body"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	candidate := adaptationRevisionTestClone(t, fullBase)
	candidate.TargetEventLedger = append(candidate.TargetEventLedger, domain.AdaptationEvent{ID: "added-event", Description: "original bridge", Origin: domain.AdaptationEventOriginAdded, Importance: domain.AdaptationEventSupporting, Required: true, DependsOn: []string{"source-event-2"}})
	candidate.Chapters = append(candidate.Chapters, domain.AdaptationChapterPlan{OutlineEntry: domain.OutlineEntry{ID: adaptationTestAddedID, Chapter: 3, Title: "Bridge", CoreEvent: "open the next phase", Hook: "new threat", Scenes: []string{"aftermath", "new conflict"}}, Chapter: 3, Title: "Bridge", IsAdded: true, AddedEventIDs: []string{"added-event"}, CoverageNote: "new story does not replace source coverage", TargetRunes: 3500, TargetMinRunes: 2500, TargetMaxRunes: 4500, RequiredChanges: []string{"add bridge"}, ForbiddenMoves: []string{"preserve source ending"}})
	candidate.Volumes[0].TargetTo = 3
	candidate.TargetTotalRunes += 3500
	candidate.TargetMaxRunes += 4500
	return st, base, candidate
}

func adaptationRevisionServiceFixture(granularity string) (domain.AdaptationPlan, domain.AdaptationSourceManifest) {
	chapters := []domain.AdaptationChapterPlan{
		{OutlineEntry: domain.OutlineEntry{ID: adaptationTestChapter1, Chapter: 1, Title: "One", CoreEvent: "meeting", Hook: "clue", Scenes: []string{"meet"}}, Chapter: 1, Title: "One", SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1}, SourceRunes: 1000, EventIDs: []string{"source-event-1"}, CoverageNote: "persisted source chapter one", TargetRunes: 4000, TargetMinRunes: 3000, TargetMaxRunes: 5000, PreserveEvents: []string{"meeting"}, RequiredChanges: []string{"adapt"}, ForbiddenMoves: []string{"do not drop meeting"}},
		{OutlineEntry: domain.OutlineEntry{ID: adaptationTestChapter2, Chapter: 2, Title: "Two", CoreEvent: "answer", Hook: "ending", Scenes: []string{"answer"}}, Chapter: 2, Title: "Two", SourceChapters: []int{2}, SourceRange: domain.SourceRange{From: 2, To: 2}, SourceRunes: 1000, EventIDs: []string{"source-event-2"}, DependsOnEventIDs: []string{"source-event-1"}, CoverageNote: "persisted source chapter two", TargetRunes: 4000, TargetMinRunes: 3000, TargetMaxRunes: 5000, PreserveEvents: []string{"answer"}, RequiredChanges: []string{"adapt"}, ForbiddenMoves: []string{"do not drop answer"}},
	}
	if granularity == domain.AdaptationGranularityChapter {
		chapters[0].SourceSegments = []domain.AdaptationSourceSegment{{SourceChapter: 1, Sequence: 1, EventIDs: []string{"source-event-1"}, RuneShare: domain.AdaptationSourceRuneShare{Start: 0, End: 1000}, EntryState: domain.AdaptationSegmentState{}, ExitState: domain.AdaptationSegmentState{}}}
		chapters[1].SourceSegments = []domain.AdaptationSourceSegment{{SourceChapter: 2, Sequence: 1, EventIDs: []string{"source-event-2"}, RuneShare: domain.AdaptationSourceRuneShare{Start: 0, End: 1000}, EntryState: domain.AdaptationSegmentState{}, ExitState: domain.AdaptationSegmentState{}}}
	}
	plan := domain.AdaptationPlan{Granularity: granularity, ModePolicy: domain.AdaptationModePolicyForGranularity(granularity), Status: domain.AdaptationPlanStatusConfirmed, RewritePolicy: domain.AdaptationRewritePolicyForGranularity(granularity), Brief: "preserve source", WordTolerance: 0.15, SourceTotalRunes: 2000, TargetTotalRunes: 8000, TargetMinRunes: 6000, TargetMaxRunes: 10000, SourceEvents: []domain.AdaptationEvent{{ID: "source-event-1", Description: "meeting", Origin: domain.AdaptationEventOriginSource, Importance: domain.AdaptationEventMainline, SourceChapter: 1, Required: true}, {ID: "source-event-2", Description: "answer", Origin: domain.AdaptationEventOriginSource, Importance: domain.AdaptationEventSupporting, SourceChapter: 2, Required: true, DependsOn: []string{"source-event-1"}}}, Volumes: []domain.AdaptationVolumePlan{{ID: adaptationTestVolumeID, Index: 1, Title: "Source", TargetFrom: 1, TargetTo: 2, SourceFrom: 1, SourceTo: 2, MainlineEventIDs: []string{"source-event-1"}}}, Chapters: chapters}
	plan.Rules = domain.CompileAdaptationRules(plan.Brief, granularity)
	for index := range plan.Chapters {
		plan.Chapters[index].RuleIDs = domain.AdaptationRuleIDs(domain.ApplicableAdaptationRules(plan.Rules, granularity, plan.Chapters[index].Chapter))
	}
	manifest := domain.AdaptationSourceManifest{ChapterCount: 2, Chapters: []domain.AdaptationSource{{Chapter: 1, Title: "One", SHA256: "one", Runes: 1000}, {Chapter: 2, Title: "Two", SHA256: "two", Runes: 1000}}}
	return plan, manifest
}

func adaptationRevisionTestClone(t *testing.T, plan domain.AdaptationPlan) domain.AdaptationPlan {
	t.Helper()
	payload, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var clone domain.AdaptationPlan
	if err := json.Unmarshal(payload, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
