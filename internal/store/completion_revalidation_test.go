package store

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestEnsureNormalCompletionRevalidationCheckpointMigratesOnlyCompleteLegacyProject(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := []domain.VolumeOutline{{
		ID: domain.LegacyStructureID(root, domain.StructureKindVolume, "volume"), Index: 1, Title: "Volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{
			ID: domain.LegacyStructureID(root, domain.StructureKindArc, "arc"), Index: 1, Title: "Arc", Goal: "goal",
			Chapters: []domain.OutlineEntry{{
				ID: domain.LegacyStructureID(root, domain.StructureKindChapter, "chapter"), Chapter: 1, Title: "End",
				CoreEvent: "event", Hook: "hook", Scenes: []string{"scene"},
			}},
		}},
	}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase: domain.PhaseWriting, Flow: domain.FlowWriting, Layered: true,
		TotalChapters: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureNormalCompletionRevalidationCheckpoint(); err == nil {
		t.Fatal("incomplete legacy project received a completion checkpoint")
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	progress.CompletedChapters = []int{1}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureNormalCompletionRevalidationCheckpoint(); err != nil {
		t.Fatal(err)
	}
	progress, err = st.Progress.Load()
	if err != nil || progress.CompletionRevalidation == nil {
		t.Fatalf("completion checkpoint=%+v err=%v", progress, err)
	}
	checkpoint := progress.CompletionRevalidation
	if checkpoint.Mode != domain.RevisionModeNormal || checkpoint.Status != "pending" ||
		checkpoint.AcceptedVersionSignature != domain.StructureSignature(volumes) ||
		!slices.Equal(checkpoint.CurrentStableOrder, stableChapterOrder(volumes)) {
		t.Fatalf("unexpected migrated checkpoint: %+v", checkpoint)
	}
}

func TestRepairLegacyNormalCompletionEvidenceEnrichesOnlyBaselineCheckpoint(t *testing.T) {
	st := newCompletionAuditFixture(t, completionAuditFixtureOptions{})
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	progress.CompletionRevalidation.AcceptedRevisionID = "normal-completion-baseline-0123456789abcdef"
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveArcSummary(domain.ArcSummary{
		Volume: 1, Arc: 1, Summary: "the arc is resolved", KeyEvents: []string{"a concise event"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveVolumeSummary(domain.VolumeSummary{
		Volume: 1, Summary: "the volume is resolved", KeyEvents: []string{"a concise event"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.World.SaveReview(domain.ReviewEntry{
		Chapter: 1, Scope: "global", Verdict: "accept", Summary: "the book is complete",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.World.DeleteReview(1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RunNormalCompletionAudit(); err == nil {
		t.Fatal("legacy summaries unexpectedly met the current exact coverage contract")
	}
	changed, err := st.RepairLegacyNormalCompletionEvidence()
	if err != nil || !changed {
		t.Fatalf("repair changed=%v err=%v", changed, err)
	}
	if _, err := st.RunNormalCompletionAudit(); err != nil {
		t.Fatalf("repaired legacy evidence failed current audit: %v", err)
	}
	changed, err = st.RepairLegacyNormalCompletionEvidence()
	if err != nil || changed {
		t.Fatalf("idempotent repair changed=%v err=%v", changed, err)
	}
}

func TestLegacyBaselineCompletionUsesIndependentAuditWhenOldReviewsAreStale(t *testing.T) {
	st := newCompletionAuditFixture(t, completionAuditFixtureOptions{})
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	progress.CompletionRevalidation.AcceptedRevisionID = "normal-completion-baseline-0123456789abcdef"
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	if err := st.World.SaveReview(domain.ReviewEntry{
		Chapter: 1, Scope: "chapter", Verdict: "polish",
		Issues: []domain.ConsistencyIssue{{Severity: "error", Description: "stale pre-audit review"}},
	}); err != nil {
		t.Fatal(err)
	}
	digest, err := st.RunNormalCompletionAudit()
	if err != nil {
		t.Fatal(err)
	}
	signNormalCompletionAuditForTest(t, st)
	if err := st.Progress.SetCompletionAudit("pass", digest); err != nil {
		t.Fatal(err)
	}
	if err := st.RefreshCompletionRevalidationEvidence(); err != nil {
		t.Fatalf("signed independent audit did not supersede stale legacy review: %v", err)
	}
}

func TestCompletionAuditTextValidAllowsNarrativeUseOfPendingSupplement(t *testing.T) {
	if !completionAuditTextValid("第三份是一份待补充的风险告知函草稿，按流程补发。", 8) {
		t.Fatal("ordinary narrative use of 待补充 was mistaken for an unfinished manuscript marker")
	}
	for _, placeholder := range []string{"TODO", "[待补]", "【待补】", "待补全文", "未完成占位"} {
		if completionAuditTextValid("正文内容 "+placeholder, 4) {
			t.Fatalf("unfinished placeholder %q was accepted", placeholder)
		}
	}
}

func TestCompletionContractContradictionIgnoresMetaphoricalClosure(t *testing.T) {
	chapter := domain.OutlineEntry{
		CoreEvent: "外援心证行为性死亡，幻觉死后进入唯一现实。",
		Hook:      "次日继续谈条件，完成新的生活安排。",
	}
	if completionContractContradiction(chapter, "她确认旧期待已经结束。", "这一夜完成心理收束。") {
		t.Fatal("metaphorical closure was mistaken for a dead character returning")
	}
	chapter.CoreEvent = "hero dies in the terminal battle"
	chapter.Hook = "hero returns alive next time"
	if !completionContractContradiction(chapter, "the hero is dead", "the terminal battle ends") {
		t.Fatal("literal death-and-return contradiction was not detected")
	}
}

func TestCompletionRevalidationSurvivesRestartAndFailsClosedUntilEvidence(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumeID := domain.LegacyStructureID(root, domain.StructureKindVolume, "volume")
	arcID := domain.LegacyStructureID(root, domain.StructureKindArc, "arc")
	chapterOne := domain.LegacyStructureID(root, domain.StructureKindChapter, "chapter-one")
	chapterTwo := domain.LegacyStructureID(root, domain.StructureKindChapter, "chapter-two")
	previous := []domain.VolumeOutline{{
		ID: volumeID, Index: 1, Title: "Volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{
			ID: arcID, Index: 1, Title: "Arc", Goal: "goal",
			Chapters: []domain.OutlineEntry{{ID: chapterOne, Chapter: 1, Title: "One", CoreEvent: "event", Hook: "hook", Scenes: []string{"scene"}}},
		}},
	}}
	current := domain.CloneStructureSnapshot(previous)
	current[0].Arcs[0].Chapters = append(current[0].Arcs[0].Chapters, domain.OutlineEntry{ID: chapterTwo, Chapter: 2, Title: "Two", CoreEvent: "event two", Hook: "hook two", Scenes: []string{"scene two"}})
	if err := st.Outline.SaveLayeredOutline(current); err != nil {
		t.Fatal(err)
	}
	checkpoint := newCompletionRevalidationCheckpoint(domain.RevisionModeNormal, "rev-accepted", domain.StructureSignature(current), previous, current)
	progress := &domain.Progress{Phase: domain.PhaseWriting, Flow: domain.FlowWriting, Layered: true, TotalChapters: 2, CompletedChapters: []int{1, 2}, ReopenedFromComplete: true, CompletionRevalidation: checkpoint}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}

	restarted := NewStore(root)
	loaded, err := restarted.Progress.Load()
	if err != nil || loaded.CompletionRevalidation == nil || loaded.CompletionRevalidation.Status != "pending" {
		t.Fatalf("checkpoint after restart = %+v err=%v", loaded, err)
	}
	if err := restarted.Progress.MarkComplete(); err == nil {
		t.Fatal("pending checkpoint allowed completion without evidence")
	}
	for chapter, id := range map[int]string{1: chapterOne, 2: chapterTwo} {
		if err := restarted.Drafts.SaveFinalChapter(chapter, "formal prose "+id); err != nil {
			t.Fatal(err)
		}
		entry := current[0].Arcs[0].Chapters[chapter-1]
		if err := restarted.Summaries.SaveSummary(domain.ChapterSummary{Chapter: chapter, Summary: "summary " + id, KeyEvents: []string{entry.CoreEvent, entry.Hook}}); err != nil {
			t.Fatal(err)
		}
		scope := "chapter"
		if chapter == 2 {
			scope = "arc"
		}
		if err := restarted.World.SaveReview(domain.ReviewEntry{Chapter: chapter, Scope: scope, Verdict: "accept", Summary: "accepted " + id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := restarted.RefreshCompletionRevalidationEvidence(); err == nil {
		t.Fatal("checkpoint accepted prose without a new completion audit")
	}
	if err := restarted.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Summary: "goal event hook event two hook two", KeyEvents: []string{"event", "hook", "event two", "hook two"}}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: 1, Summary: "theme goal event hook event two hook two", KeyEvents: []string{"goal", "event", "hook", "event two", "hook two"}}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.World.SaveReview(domain.ReviewEntry{Chapter: 2, Scope: "global", Verdict: "accept", Summary: "theme goal event hook event two hook two"}); err != nil {
		t.Fatal(err)
	}
	auditDigest, err := restarted.RunNormalCompletionAudit()
	if err != nil {
		t.Fatal(err)
	}
	var layeredReceipt normalCompletionAuditReceipt
	if err := newIO(root).ReadJSON(normalCompletionAuditReceiptFile, &layeredReceipt); err != nil {
		t.Fatal(err)
	}
	layerKinds := make(map[string]bool)
	for _, layer := range layeredReceipt.Layers {
		layerKinds[layer.Layer] = true
		if len(layer.InputSignature) != 64 || len(layer.ReportDigest) != 64 || len(layer.Coverage) == 0 || len(layer.RuleFindings) == 0 {
			t.Fatalf("incomplete layered audit receipt: %+v", layer)
		}
	}
	for _, kind := range []string{"postprocess", "chapter", "arc", "volume", "existing_completion", "book"} {
		if !layerKinds[kind] {
			t.Fatalf("fresh audit omitted %s layer: %+v", kind, layerKinds)
		}
	}
	signNormalCompletionAuditForTest(t, restarted)
	if err := restarted.Progress.SetCompletionAudit("pass", auditDigest); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Drafts.SaveFinalChapter(1, "formal prose changed after audit"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RefreshCompletionRevalidationEvidence(); err == nil {
		t.Fatal("post-audit prose drift reused signed layered receipts")
	}
	auditDigest, err = restarted.RunNormalCompletionAudit()
	if err != nil {
		t.Fatal(err)
	}
	signNormalCompletionAuditForTest(t, restarted)
	if err := restarted.Progress.SetCompletionAudit("pass", auditDigest); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Summary: "goal event hook event two hook two current", KeyEvents: []string{"event", "hook", "event two", "hook two"}}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RefreshCompletionRevalidationEvidence(); err == nil {
		t.Fatal("post-audit parent-summary drift replayed an old signed receipt")
	}
	auditDigest, err = restarted.RunNormalCompletionAudit()
	if err != nil {
		t.Fatal(err)
	}
	signNormalCompletionAuditForTest(t, restarted)
	if err := restarted.Progress.SetCompletionAudit("pass", auditDigest); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RefreshCompletionRevalidationEvidence(); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Progress.MarkComplete(); err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Progress.Load()
	if err != nil || completed.Phase != domain.PhaseComplete || completed.CompletionRevalidation.Status != "completed" || len(completed.CompletionRevalidation.BookAuditSignature) != 64 {
		t.Fatalf("completed checkpoint = %+v err=%v", completed, err)
	}
}

func TestNormalCompletionAuditRejectsSemanticContractContradiction(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := []domain.VolumeOutline{{
		ID: domain.LegacyStructureID(root, domain.StructureKindVolume, "volume"), Index: 1, Title: "Volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{
			ID: domain.LegacyStructureID(root, domain.StructureKindArc, "arc"), Index: 1, Title: "Arc", Goal: "goal",
			Chapters: []domain.OutlineEntry{{
				ID: domain.LegacyStructureID(root, domain.StructureKindChapter, "chapter"), Chapter: 1, Title: "End",
				CoreEvent: "hero dies in the terminal battle", Hook: "hero returns alive next time", Scenes: []string{"last battle"},
			}},
		}},
	}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "the hero is dead after the terminal battle"); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 1, Summary: "the hero dies"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Summary: "terminal arc summary"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: 1, Summary: "terminal volume summary"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, CompletedChapters: []int{1}, CompletionRevalidation: &domain.CompletionRevalidationCheckpoint{Version: 1, Status: "pending", Mode: domain.RevisionModeNormal}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RunNormalCompletionAudit(); err == nil {
		t.Fatal("semantic contradiction received a signed-pass audit input")
	}
}

func TestNormalCompletionAuditBindsDramaticFactsThroughEveryParentLayer(t *testing.T) {
	st := newCompletionAuditFixture(t, completionAuditFixtureOptions{dramaticFacts: true})
	digest, err := st.RunNormalCompletionAudit()
	if err != nil {
		t.Fatal(err)
	}
	var receipt normalCompletionAuditReceipt
	if err := newIO(st.dir).ReadJSON(normalCompletionAuditReceiptFile, &receipt); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"chapter", "arc", "volume", "book"} {
		found := false
		for _, layer := range receipt.Layers {
			if layer.Layer != want {
				continue
			}
			found = slices.ContainsFunc(layer.Coverage, func(value string) bool { return strings.Contains(value, "dramatic") }) &&
				slices.ContainsFunc(layer.RuleFindings, func(value string) bool { return strings.Contains(value, "dramatic") })
		}
		if !found {
			t.Fatalf("%s receipt omitted dramatic coverage/findings: %+v", want, receipt.Layers)
		}
	}
	signNormalCompletionAuditForTest(t, st)
	if err := st.Progress.SetCompletionAudit("pass", digest); err != nil {
		t.Fatal(err)
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	volumes[0].Arcs[0].Chapters[0].DramaticFacts.ResultState = "failed"
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.RefreshCompletionRevalidationEvidence(); err == nil {
		t.Fatal("post-signature dramatic-fact drift reused an old pass receipt")
	}
}

func TestNormalCompletionAuditRequiresAuthoritativeExpansionOriginFacts(t *testing.T) {
	for _, test := range []struct {
		name      string
		opts      completionAuditFixtureOptions
		mutate    func([]domain.VolumeOutline)
		wantError bool
	}{
		{"authoritative expansion", completionAuditFixtureOptions{dramaticFacts: true, expansionOrigin: true}, nil, false},
		{"expansion facts removed", completionAuditFixtureOptions{dramaticFacts: true, expansionOrigin: true}, func(volumes []domain.VolumeOutline) {
			volumes[0].Arcs[0].Chapters[0].DramaticFacts = nil
		}, true},
		{"expansion contract mismatch", completionAuditFixtureOptions{dramaticFacts: true, expansionOrigin: true}, func(volumes []domain.VolumeOutline) {
			volumes[0].Arcs[0].Chapters[0].ExpansionOrigin.DramaticContractSignature = strings.Repeat("b", 64)
		}, true},
		{"ordinary legacy nil", completionAuditFixtureOptions{}, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			st := newCompletionAuditFixture(t, test.opts)
			if test.mutate != nil {
				volumes, loadErr := st.Outline.LoadLayeredOutline()
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				test.mutate(volumes)
				if saveErr := st.Outline.SaveLayeredOutline(volumes); saveErr != nil {
					t.Fatal(saveErr)
				}
			}
			_, err := st.RunNormalCompletionAudit()
			if (err != nil) != test.wantError {
				t.Fatalf("RunNormalCompletionAudit error = %v, wantError=%v", err, test.wantError)
			}
			if err == nil && test.opts.expansionOrigin {
				volumes, loadErr := st.Outline.LoadLayeredOutline()
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				volumes[0].Arcs[0].Chapters[0].DramaticFacts.ResultState = "failed"
				if saveErr := st.Outline.SaveLayeredOutline(volumes); saveErr != nil {
					t.Fatal(saveErr)
				}
				if _, driftErr := st.RunNormalCompletionAudit(); driftErr == nil {
					t.Fatal("expansion dramatic-fact drift received a fresh audit receipt")
				}
			}
		})
	}
}

func TestNormalCompletionAuditRejectsExpansionProvenanceErasureAndRuntimeForgery(t *testing.T) {
	t.Run("origin and facts erased together", func(t *testing.T) {
		st := newCompletionAuditFixture(t, completionAuditFixtureOptions{dramaticFacts: true, expansionOrigin: true})
		volumes, err := st.Outline.LoadLayeredOutline()
		if err != nil {
			t.Fatal(err)
		}
		volumes[0].Arcs[0].Chapters[0].ExpansionOrigin = nil
		volumes[0].Arcs[0].Chapters[0].DramaticFacts = nil
		if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
			t.Fatal(err)
		}
		if _, err := st.RunNormalCompletionAudit(); err == nil {
			t.Fatal("removing formal origin and facts bypassed accepted publication provenance")
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*ExpansionRuntime)
	}{
		{"preview replacement with forged same-file seal", func(runtime *ExpansionRuntime) {
			preview := runtime.Previews["exp-test"]
			preview.Candidate[0].Arcs[0].Chapters[0].Title = "forged"
			seal := bytes.Repeat([]byte{0x5b}, 32)
			runtime.SealHex = hex.EncodeToString(seal)
			preview.Signature = signCompletionPreviewForTest(t, *preview, seal)
		}},
		{"wrong mode", func(runtime *ExpansionRuntime) {
			preview := runtime.Previews["exp-test"]
			preview.Mode = domain.RevisionModeAdaptation
			seal, _ := hex.DecodeString(runtime.SealHex)
			preview.Signature = signCompletionPreviewForTest(t, *preview, seal)
		}},
		{"forged confirmation receipt", func(runtime *ExpansionRuntime) {
			runtime.Receipts = map[string]ExpansionCommandReceipt{"confirm:forged": {Operation: "confirm", PreviewID: "exp-test", RevisionID: "another-revision"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			st := newCompletionAuditFixture(t, completionAuditFixtureOptions{dramaticFacts: true, expansionOrigin: true})
			runtime, err := st.LoadExpansionRuntime()
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&runtime)
			if err := st.SaveExpansionRuntime(runtime); err != nil {
				t.Fatal(err)
			}
			if _, err := st.RunNormalCompletionAudit(); err == nil {
				t.Fatal("forged expansion runtime was accepted")
			}
		})
	}
}

func TestNormalCompletionAuditUsesAcceptedPublicationAfterValidationCloneDropsPrivateRuntime(t *testing.T) {
	st := newCompletionAuditFixture(t, completionAuditFixtureOptions{dramaticFacts: true, expansionOrigin: true})
	if err := st.Revisions.io.RemoveFile(expansionRuntimePath); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RunNormalCompletionAudit(); err != nil {
		t.Fatalf("fresh audit could not rebuild expansion provenance from accepted publication: %v", err)
	}
}

func TestAcceptedExpansionPublicationSupportsAdaptationMode(t *testing.T) {
	st := newCompletionAuditFixture(t, completionAuditFixtureOptions{dramaticFacts: true, expansionOrigin: true})
	runtime, err := st.LoadExpansionRuntime()
	if err != nil {
		t.Fatal(err)
	}
	preview := runtime.Previews["exp-test"]
	preview.Mode = domain.RevisionModeAdaptation
	preview.RevisionPreviewSignature = strings.Repeat("c", 64)
	seal, _ := hex.DecodeString(runtime.SealHex)
	preview.Signature = signCompletionPreviewForTest(t, *preview, seal)
	if err := st.SaveExpansionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	state, err := st.Revisions.loadUnlocked()
	if err != nil {
		t.Fatal(err)
	}
	session := state.Sessions[preview.ConfirmedRevisionID]
	session.Mode = domain.RevisionModeAdaptation
	session.PolicyID = domain.AdaptationRevisionPolicyID
	session.PolicyVersion = domain.AdaptationRevisionPolicyVersion
	session.PreviewSignature = preview.RevisionPreviewSignature
	state.Sessions[session.ID] = session
	if err := st.Revisions.io.WriteJSON(revisionStateFile, state); err != nil {
		t.Fatal(err)
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil || progress.CompletionRevalidation == nil {
		t.Fatalf("load completion checkpoint: %v", err)
	}
	chapter := volumes[0].Arcs[0].Chapters[0]
	plan := domain.AdaptationPlan{
		Granularity: "chapter", Status: "confirmed", RewritePolicy: "rewrite", Brief: "test",
		Volumes:  []domain.AdaptationVolumePlan{{ID: volumes[0].ID, Index: volumes[0].Index, Title: volumes[0].Title, Theme: volumes[0].Theme, TargetFrom: 1, TargetTo: 1}},
		Chapters: []domain.AdaptationChapterPlan{{OutlineEntry: chapter, Chapter: chapter.Chapter, Title: chapter.Title, SourceChapters: []int{1}}},
	}
	if err := st.Revisions.io.WriteJSON(adaptationPlanFile, plan); err != nil {
		t.Fatal(err)
	}
	adaptationVolumes := adaptationPublicationStructure(plan)
	progress.CompletionRevalidation.Mode = domain.RevisionModeAdaptation
	progress.CompletionRevalidation.AcceptedVersionSignature = domain.StructureSignature(adaptationVolumes)
	progress.CompletionRevalidation.CurrentStructureSignature = domain.StructureSignature(adaptationVolumes)
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	if err := st.Revisions.writeExpansionPublicationReceipt(state, session); err != nil {
		t.Fatal(err)
	}
	if _, err := st.validateAuthoritativeExpansionOrigins(progress.CompletionRevalidation, adaptationVolumes); err != nil {
		t.Fatalf("adaptation accepted publication provenance rejected: %v", err)
	}
}

func TestValidationCloneRevisionAuthorityRejectsLiveCapabilities(t *testing.T) {
	st := newCompletionAuditFixture(t, completionAuditFixtureOptions{dramaticFacts: true, expansionOrigin: true})
	state, err := st.Revisions.loadUnlocked()
	if err != nil {
		t.Fatal(err)
	}
	clean, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRevisionStateForClone(clean); err != nil {
		t.Fatalf("quiescent accepted revision authority rejected: %v", err)
	}
	state.NormalLease = &NormalFlowLease{Token: "private-capability", Generation: state.Generation, Owner: "test", PID: os.Getpid(), AcquiredAt: "2026-07-17T00:00:00Z"}
	live, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRevisionStateForClone(live); err == nil {
		t.Fatal("validation clone accepted a live revision capability")
	}
}

func TestNormalCompletionAuditFailsClosedAtEveryLayer(t *testing.T) {
	cases := []struct {
		name string
		opts completionAuditFixtureOptions
	}{
		{"postprocess", completionAuditFixtureOptions{chapterSummary: "TODO"}},
		{"chapter", completionAuditFixtureOptions{terminalContradiction: true}},
		{"arc", completionAuditFixtureOptions{arcSummary: "TODO"}},
		{"arc_semantic_contradiction", completionAuditFixtureOptions{arcSummary: "goal hero dies in the terminal battle a new mystery appears"}},
		{"arc_child_coverage", completionAuditFixtureOptions{arcSummary: "goal hero solves the conflict", omitArcKeyEvents: true}},
		{"volume", completionAuditFixtureOptions{volumeSummary: "TODO"}},
		{"volume_semantic_contradiction", completionAuditFixtureOptions{volumeSummary: "theme goal hero dies in the terminal battle a new mystery appears"}},
		{"existing_completion", completionAuditFixtureOptions{omitCompletedChapter: true}},
		{"existing_completion_extra", completionAuditFixtureOptions{extraCompletedChapter: true}},
		{"existing_completion_duplicate", completionAuditFixtureOptions{duplicateCompleted: true}},
		{"existing_completion_reversed", completionAuditFixtureOptions{reverseCompleted: true}},
		{"existing_completion_zero_total", completionAuditFixtureOptions{zeroTotalChapters: true}},
		{"dramatic_parent_conflict", completionAuditFixtureOptions{dramaticFacts: true, arcSummary: "goal hero solves the conflict a new mystery appears choice_state=deferred"}},
		{"dramatic_choice_cost_result_missing", completionAuditFixtureOptions{dramaticMissing: true}},
		{"dramatic_character_regression", completionAuditFixtureOptions{dramaticRegression: true}},
		{"book_coverage", completionAuditFixtureOptions{bookCountMismatch: true}},
		{"book_semantic_contradiction", completionAuditFixtureOptions{globalReview: "theme goal hero dies in the terminal battle a new mystery appears"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newCompletionAuditFixture(t, tc.opts)
			if _, err := st.RunNormalCompletionAudit(); err == nil {
				t.Fatalf("%s-invalid candidate received a pass receipt", tc.name)
			}
		})
	}
}

func TestNormalCompletionAuditWaitsForRevisionWriterLock(t *testing.T) {
	st := newCompletionAuditFixture(t, completionAuditFixtureOptions{})
	held := make(chan struct{})
	release := make(chan struct{})
	writerDone := make(chan error, 1)
	var once sync.Once
	st.Drafts.io.writeFault = func(_, stage string) error {
		if stage == "after_temp_sync" {
			once.Do(func() { close(held) })
			<-release
		}
		return nil
	}
	go func() {
		writerDone <- st.Drafts.SaveFinalChapter(1, "formal prose chapter-one safely rewritten")
	}()
	<-held
	auditDone := make(chan error, 1)
	go func() {
		_, err := st.RunNormalCompletionAudit()
		auditDone <- err
	}()
	select {
	case err := <-auditDone:
		t.Fatalf("completion audit bypassed the production writer lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	st.Drafts.io.writeFault = nil
	select {
	case err := <-auditDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completion audit did not resume after writer released the lock")
	}
}

func TestNormalCompletionAuditWaitsForEveryFormalContentWriter(t *testing.T) {
	tests := []struct {
		name  string
		opts  completionAuditFixtureOptions
		setup func(*Store) (*IO, func() error)
	}{
		{name: "chapter_summary", setup: func(st *Store) (*IO, func() error) {
			summary, _ := st.Summaries.LoadSummary(1)
			return st.Summaries.io, func() error { return st.Summaries.SaveSummary(*summary) }
		}},
		{name: "arc_summary", setup: func(st *Store) (*IO, func() error) {
			summary, _ := st.Summaries.LoadArcSummary(1, 1)
			return st.Summaries.io, func() error { return st.Summaries.SaveArcSummary(*summary) }
		}},
		{name: "volume_summary", setup: func(st *Store) (*IO, func() error) {
			summary, _ := st.Summaries.LoadVolumeSummary(1)
			return st.Summaries.io, func() error { return st.Summaries.SaveVolumeSummary(*summary) }
		}},
		{name: "review", setup: func(st *Store) (*IO, func() error) {
			review, _ := st.World.LoadGlobalReview(1)
			return st.World.io, func() error { return st.World.SaveReview(*review) }
		}},
		{name: "expansion_runtime", opts: completionAuditFixtureOptions{expansionOrigin: true}, setup: func(st *Store) (*IO, func() error) {
			runtime, _ := st.LoadExpansionRuntime()
			return st.Revisions.io, func() error { return st.SaveExpansionRuntime(runtime) }
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newCompletionAuditFixture(t, tc.opts)
			writerIO, write := tc.setup(st)
			held, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			writerIO.writeFault = func(_, stage string) error {
				if stage == "after_temp_sync" {
					once.Do(func() { close(held) })
					<-release
				}
				return nil
			}
			writerDone := make(chan error, 1)
			go func() { writerDone <- write() }()
			<-held
			auditDone := make(chan error, 1)
			go func() { _, err := st.RunNormalCompletionAudit(); auditDone <- err }()
			select {
			case err := <-auditDone:
				t.Fatalf("audit bypassed %s writer: %v", tc.name, err)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			if err := <-writerDone; err != nil {
				t.Fatal(err)
			}
			writerIO.writeFault = nil
			select {
			case err := <-auditDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("audit did not resume after writer released revision lock")
			}
		})
	}
}

func TestNormalCompletionAuditWaitsForCrossProcessDraftWriter(t *testing.T) {
	st := newCompletionAuditFixture(t, completionAuditFixtureOptions{})
	signalPath := filepath.Join(t.TempDir(), "writer-held")
	releasePath := filepath.Join(t.TempDir(), "writer-release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCompletionDraftWriterProcessHelper$")
	var childOutput bytes.Buffer
	cmd.Stdout, cmd.Stderr = &childOutput, &childOutput
	cmd.Env = append(os.Environ(),
		"AINOVEL_TEST_WRITER_ROOT="+st.dir,
		"AINOVEL_TEST_WRITER_SIGNAL="+signalPath,
		"AINOVEL_TEST_WRITER_RELEASE="+releasePath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(signalPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("cross-process writer did not acquire the revision lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	auditDone := make(chan error, 1)
	go func() { _, err := st.RunNormalCompletionAudit(); auditDone <- err }()
	select {
	case err := <-auditDone:
		t.Fatalf("audit bypassed cross-process production writer: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cross-process writer failed: %v\n%s", err, childOutput.String())
	}
	select {
	case err := <-auditDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("audit did not resume after cross-process writer")
	}
}

func TestCompletionDraftWriterProcessHelper(t *testing.T) {
	root := os.Getenv("AINOVEL_TEST_WRITER_ROOT")
	if root == "" {
		t.Skip("helper process only")
	}
	signalPath := os.Getenv("AINOVEL_TEST_WRITER_SIGNAL")
	releasePath := os.Getenv("AINOVEL_TEST_WRITER_RELEASE")
	st := NewStore(root)
	var once sync.Once
	st.Drafts.io.writeFault = func(_, stage string) error {
		if stage == "after_temp_sync" {
			once.Do(func() { _ = os.WriteFile(signalPath, []byte("held"), 0o600) })
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(releasePath); err == nil {
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("timed out waiting for writer release")
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		return nil
	}
	if err := st.Drafts.SaveFinalChapter(1, "the hero resolves the current conflict safely after rewrite"); err != nil {
		t.Fatal(err)
	}
}

type completionAuditFixtureOptions struct {
	chapterSummary        string
	arcSummary            string
	volumeSummary         string
	terminalContradiction bool
	omitCompletedChapter  bool
	bookCountMismatch     bool
	extraCompletedChapter bool
	duplicateCompleted    bool
	zeroTotalChapters     bool
	globalReview          string
	omitArcKeyEvents      bool
	reverseCompleted      bool
	dramaticFacts         bool
	dramaticMissing       bool
	dramaticRegression    bool
	expansionOrigin       bool
}

func newCompletionAuditFixture(t *testing.T, opts completionAuditFixtureOptions) *Store {
	t.Helper()
	useExpansionAuthorityRootForTest(t, filepath.Join(t.TempDir(), "completion-authority"))
	root := t.TempDir()
	writeStoreTestProjectManifest(t, root, "completion-audit-project")
	st := NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	chapterID := domain.LegacyStructureID(root, domain.StructureKindChapter, "chapter")
	chapter := domain.OutlineEntry{ID: chapterID, Chapter: 1, Title: "Chapter", CoreEvent: "hero solves the conflict", Hook: "a new mystery appears", Scenes: []string{"resolution"}}
	if opts.dramaticFacts || opts.dramaticMissing || opts.dramaticRegression {
		facts := domain.ExpansionDramaticFactSet{SchemaVersion: domain.ExpansionDramaticFactsSchemaV1, GoalState: "pursued", ConflictState: "active", ChoiceState: "committed", CostState: "paid", ResultState: "achieved", CharacterBefore: "passive", CharacterAfter: "active", ClimaxState: "occurred", ExitState: "irreversible", ImpactState: "required"}
		if opts.dramaticMissing {
			facts.ChoiceState, facts.CostState, facts.ResultState = "", "", ""
		}
		if opts.dramaticRegression {
			facts.CharacterBefore, facts.CharacterAfter = "active", "reactive"
		}
		chapter.DramaticFacts = &facts
		if opts.expansionOrigin {
			origin, err := domain.NewExpansionOrigin("exp-test", facts)
			if err != nil {
				t.Fatal(err)
			}
			chapter.ExpansionOrigin = &origin
		}
	}
	prose := "the hero resolves the current conflict safely"
	if opts.terminalContradiction {
		chapter.CoreEvent = "hero dies in the terminal battle"
		chapter.Hook = "hero returns alive next time"
		prose = "the hero is dead after the terminal battle"
	}
	chapters := []domain.OutlineEntry{chapter}
	completed := []int{1}
	volumes := []domain.VolumeOutline{{
		ID: domain.LegacyStructureID(root, domain.StructureKindVolume, "volume"), Index: 1, Title: "Volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{ID: domain.LegacyStructureID(root, domain.StructureKindArc, "arc"), Index: 1, Title: "Arc", Goal: "goal", Chapters: chapters}},
	}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if opts.expansionOrigin {
		saveCompletionExpansionRuntime(t, st, volumes, factsForCompletionFixture())
	}
	chapterSummary := opts.chapterSummary
	if chapterSummary == "" {
		chapterSummary = "current chapter summary"
	}
	for _, number := range completed {
		if err := st.Drafts.SaveFinalChapter(number, prose); err != nil {
			t.Fatal(err)
		}
		if err := st.Summaries.SaveSummary(domain.ChapterSummary{Chapter: number, Summary: chapterSummary, KeyEvents: []string{chapter.CoreEvent, chapter.Hook}}); err != nil {
			t.Fatal(err)
		}
	}
	arcSummary := opts.arcSummary
	if arcSummary == "" {
		arcSummary = "goal hero solves the conflict a new mystery appears"
	}
	volumeSummary := opts.volumeSummary
	if volumeSummary == "" {
		volumeSummary = "theme goal hero solves the conflict a new mystery appears"
	}
	arcEvents := []string{"hero solves the conflict", "a new mystery appears"}
	if opts.omitArcKeyEvents {
		arcEvents = nil
	}
	if err := st.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Summary: arcSummary, KeyEvents: arcEvents}); err != nil {
		t.Fatal(err)
	}
	volumeEvents := []string{"goal", "hero solves the conflict", "a new mystery appears"}
	if err := st.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: 1, Summary: volumeSummary, KeyEvents: volumeEvents}); err != nil {
		t.Fatal(err)
	}
	if opts.omitCompletedChapter {
		completed = nil
	}
	totalChapters := len(chapters)
	if opts.bookCountMismatch {
		totalChapters++
	}
	if opts.extraCompletedChapter {
		completed = append(completed, 99)
	}
	if opts.duplicateCompleted {
		completed = append(completed, 1)
	}
	if opts.reverseCompleted {
		secondID := domain.LegacyStructureID(root, domain.StructureKindChapter, "chapter-two")
		second := chapter
		second.ID, second.Chapter, second.Title = secondID, 2, "Chapter Two"
		second.CoreEvent, second.Hook = "hero pays the cost", "the consequence remains"
		volumes[0].Arcs[0].Chapters = append(volumes[0].Arcs[0].Chapters, second)
		if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
			t.Fatal(err)
		}
		if err := st.Drafts.SaveFinalChapter(2, "the hero pays the cost and accepts the consequence"); err != nil {
			t.Fatal(err)
		}
		if err := st.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 2, Summary: "current second chapter summary", KeyEvents: []string{second.CoreEvent, second.Hook}}); err != nil {
			t.Fatal(err)
		}
		arcEvents = append(arcEvents, second.CoreEvent, second.Hook)
		if err := st.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Summary: arcSummary + " " + second.CoreEvent + " " + second.Hook, KeyEvents: arcEvents}); err != nil {
			t.Fatal(err)
		}
		volumeEvents = append(volumeEvents, second.CoreEvent, second.Hook)
		if err := st.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: 1, Summary: volumeSummary + " " + second.CoreEvent + " " + second.Hook, KeyEvents: volumeEvents}); err != nil {
			t.Fatal(err)
		}
		completed = []int{2, 1}
		totalChapters = 2
	}
	if opts.zeroTotalChapters {
		totalChapters = 0
	}
	acceptedRevisionID := "accepted-revision"
	if opts.expansionOrigin {
		acceptedRevisionID = "revision-test"
	}
	checkpoint := newCompletionRevalidationCheckpoint(domain.RevisionModeNormal, acceptedRevisionID, domain.StructureSignature(volumes), volumes, volumes)
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, TotalChapters: totalChapters, CompletedChapters: completed, CompletionRevalidation: checkpoint}); err != nil {
		t.Fatal(err)
	}
	if opts.expansionOrigin {
		state, err := st.Revisions.loadUnlocked()
		if err != nil {
			t.Fatal(err)
		}
		session := state.Sessions["revision-test"]
		if err := st.Revisions.writeExpansionPublicationReceipt(state, session); err != nil {
			t.Fatal(err)
		}
	}
	globalReview := opts.globalReview
	if globalReview == "" {
		globalReview = "theme goal hero solves the conflict a new mystery appears"
		if opts.reverseCompleted {
			globalReview += " hero pays the cost the consequence remains"
		}
	}
	if err := st.World.SaveReview(domain.ReviewEntry{Chapter: 1, Scope: "global", Verdict: "accept", Summary: globalReview}); err != nil {
		t.Fatal(err)
	}
	return st
}

func writeStoreTestProjectManifest(t *testing.T, outputDir, projectID string) {
	t.Helper()
	projectRoot := filepath.Dir(outputDir)
	now := time.Now().UTC()
	manifest := publicationProjectManifest{Version: 1, ID: projectID, Name: projectID, RootDir: projectRoot, OutputDir: outputDir, CreatedAt: now, UpdatedAt: now, LastAccessedAt: now}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "project.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func factsForCompletionFixture() domain.ExpansionDramaticFactSet {
	return domain.ExpansionDramaticFactSet{
		SchemaVersion: domain.ExpansionDramaticFactsSchemaV1, GoalState: "pursued", ConflictState: "active",
		ChoiceState: "committed", CostState: "paid", ResultState: "achieved", CharacterBefore: "passive",
		CharacterAfter: "active", ClimaxState: "occurred", ExitState: "irreversible", ImpactState: "required",
	}
}

func saveCompletionExpansionRuntime(t *testing.T, st *Store, formal []domain.VolumeOutline, facts domain.ExpansionDramaticFactSet) {
	t.Helper()
	candidate := domain.CloneStructureSnapshot(formal)
	chapter := &candidate[0].Arcs[0].Chapters[0]
	chapter.DramaticFacts = &facts
	origin, err := domain.NewExpansionOrigin("exp-test", facts)
	if err != nil {
		t.Fatal(err)
	}
	chapter.ExpansionOrigin = &origin
	preview := domain.ExpansionPreview{
		ID: "exp-test", Mode: domain.RevisionModeNormal, Candidate: candidate, CandidateSignature: domain.StructureSignature(candidate),
		Recommendation:      domain.ExpansionRecommendation{Assessment: domain.ExpansionDramaticAssessment{TypedClaims: &facts}},
		ConfirmedRevisionID: "revision-test",
	}
	seal := bytes.Repeat([]byte{0x2a}, 32)
	unsigned := preview
	unsigned.Signature = ""
	unsigned.Obsolete = false
	unsigned.Cancelled = false
	unsigned.ConfirmedRevisionID = ""
	payload, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, seal)
	_, _ = mac.Write(payload)
	preview.Signature = hex.EncodeToString(mac.Sum(nil))
	seedAcceptedExpansionRevision(t, st, candidate, preview)
	if err := st.SaveExpansionRuntime(ExpansionRuntime{
		Version: 1, SealHex: hex.EncodeToString(seal), KernelSealHex: hex.EncodeToString(seal),
		Previews: map[string]*domain.ExpansionPreview{preview.ID: &preview},
		Receipts: map[string]ExpansionCommandReceipt{"confirm:test": {Operation: "confirm", PreviewID: preview.ID, RevisionID: preview.ConfirmedRevisionID}},
	}); err != nil {
		t.Fatal(err)
	}
}

func signCompletionPreviewForTest(t *testing.T, preview domain.ExpansionPreview, seal []byte) string {
	t.Helper()
	preview.Signature = ""
	preview.Obsolete = false
	preview.Cancelled = false
	preview.ConfirmedRevisionID = ""
	payload, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, seal)
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func seedAcceptedExpansionRevision(t *testing.T, st *Store, candidate []domain.VolumeOutline, preview domain.ExpansionPreview) {
	t.Helper()
	payload, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	version := domain.ArtifactVersion{
		ID: "version-expansion-test", ArtifactID: candidate[0].ID, ArtifactKind: domain.StructureKindVolume,
		RevisionID: preview.ConfirmedRevisionID, Sequence: 1, Round: 1, Payload: payload,
		ContentSignature: domain.JSONContentSignature(payload), CreatedAt: "2026-07-17T00:00:00Z",
	}
	impact, err := domain.NewRevisionImpact("one-line expansion", []domain.RevisionImpactItem{{
		ArtifactID: candidate[0].ID, ArtifactKind: domain.StructureKindVolume, Change: "publish expansion",
		Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactStructureChange,
		DependencyEvidence: []string{"accepted expansion candidate"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	candidateSignature := domain.CandidateSignature([]domain.ArtifactVersion{version})
	session := domain.RevisionSession{
		Version: domain.RevisionSchemaVersion, ID: preview.ConfirmedRevisionID, Mode: domain.RevisionModeNormal,
		Stage: domain.RevisionStageCompleted, Revision: 4, Generation: 1,
		PolicyID: domain.NormalRevisionPolicyID, PolicyVersion: domain.NormalRevisionPolicyVersion, Intent: "expand",
		Impact: impact, PreviewSignature: preview.Signature,
		ApprovalStages:      []domain.RevisionApprovalStage{{ID: "structure", Label: "Structure"}},
		Approvals:           []domain.RevisionApproval{{StageID: "structure", ApprovedAt: "2026-07-17T00:00:00Z"}},
		CandidateVersionIDs: []string{version.ID}, CandidateSignature: candidateSignature, Round: 1,
		Audits:    []domain.RevisionAudit{{Round: 1, CandidateSignature: candidateSignature, Passed: true, CreatedAt: "2026-07-17T00:00:00Z"}},
		CreatedAt: "2026-07-17T00:00:00Z", UpdatedAt: "2026-07-17T00:00:00Z", CompletedAt: "2026-07-17T00:00:00Z",
	}
	state := newRevisionState()
	state.Sessions[session.ID] = session
	state.Versions[version.ID] = version
	state.CurrentArtifacts[version.ArtifactID] = version.ID
	if err := st.Revisions.io.WriteJSON(revisionStateFile, state); err != nil {
		t.Fatal(err)
	}
}

func signNormalCompletionAuditForTest(t *testing.T, st *Store) {
	t.Helper()
	var receipt normalCompletionAuditReceipt
	if err := newIO(st.dir).ReadJSON(normalCompletionAuditReceiptFile, &receipt); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := canonicalIndependentCompletionReceiptPayload(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	if err := newIO(st.dir).WriteJSON(normalCompletionAuditReceiptFile, receipt); err != nil {
		t.Fatal(err)
	}
	trust := struct {
		Version   int    `json:"version"`
		Algorithm string `json:"algorithm"`
		PublicKey string `json:"public_key"`
	}{1, "ed25519", base64.StdEncoding.EncodeToString(publicKey)}
	if err := newIO(st.dir).WriteJSON(normalCompletionAuditorTrustFile, trust); err != nil {
		t.Fatal(err)
	}
}
