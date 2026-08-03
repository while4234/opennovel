package completionauditorclient

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestIndependentCompletionAuditorSignsCurrentLayeredEvidence(t *testing.T) {
	root := t.TempDir()
	st := store.NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumeID := domain.LegacyStructureID(root, domain.StructureKindVolume, "volume")
	arcID := domain.LegacyStructureID(root, domain.StructureKindArc, "arc")
	chapterID := domain.LegacyStructureID(root, domain.StructureKindChapter, "chapter")
	volumes := []domain.VolumeOutline{{ID: volumeID, Index: 1, Title: "Volume", Theme: "theme", Arcs: []domain.ArcOutline{{ID: arcID, Index: 1, Title: "Arc", Goal: "goal", Chapters: []domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "Chapter", CoreEvent: "event", Hook: "hook", Scenes: []string{"scene"}}}}}}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "current formal prose"); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 1, Summary: "current chapter summary", KeyEvents: []string{"event", "hook"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Summary: "goal event hook", KeyEvents: []string{"event", "hook"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: 1, Summary: "theme goal event hook", KeyEvents: []string{"goal", "event", "hook"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.World.SaveReview(domain.ReviewEntry{Chapter: 1, Scope: "global", Verdict: "accept", Summary: "theme goal event hook"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase: domain.PhaseWriting, TotalChapters: 1, CompletedChapters: []int{1},
		CompletionRevalidation: &domain.CompletionRevalidationCheckpoint{
			Version: 1, Status: "pending", Mode: domain.RevisionModeNormal,
			AcceptedRevisionID: "accepted-revision", AcceptedVersionSignature: domain.StructureSignature(volumes),
			CurrentStructureSignature: domain.StructureSignature(volumes), CurrentStableOrder: []string{chapterID},
		},
	}); err != nil {
		t.Fatal(err)
	}

	executable := filepath.Join(t.TempDir(), "manuscript-completion-auditor")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	goCommand := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goCommand += ".exe"
	}
	build := exec.Command(goCommand, "build", "-trimpath", "-o", executable, "./cmd/manuscript-completion-auditor")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build completion auditor: %v\n%s", err, output)
	}
	t.Setenv(commandEnvironment, executable)
	client, err := New()
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Audit(context.Background(), root)
	if err != nil || len(result.ReportDigest) != 64 {
		t.Fatalf("audit result=%+v err=%v", result, err)
	}
	for _, rel := range []string{"meta/manuscript/completion-audit-receipt.json", "meta/manuscript/completion-auditor-trust.json", ".completion-auditor/ed25519.key"} {
		if info, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); statErr != nil || info.IsDir() {
			t.Fatalf("missing sealed artifact %s: %v", rel, statErr)
		}
	}
}
