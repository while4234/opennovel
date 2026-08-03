package web

import (
	"slices"
	"testing"

	"github.com/voocel/ainovel-cli/internal/entry/startup"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestPrepareNormalFoundationGenerationSeedsConfirmedCoCreatePremise(t *testing.T) {
	outputDir := t.TempDir()
	fake := newFakeProjectHost()
	session, err := NewProjectSession(ProjectManifest{ID: "confirmed-premise", OutputDir: outputDir}, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	const brief = "  一名记者必须在家人与真相之间作出选择。  "
	if err := session.prepareNormalFoundationGeneration(startup.Plan{
		RawPrompt:   brief,
		StartPrompt: "start",
	}, ""); err != nil {
		t.Fatal(err)
	}

	st := storepkg.NewStore(outputDir)
	foundation, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if foundation.Premise != "一名记者必须在家人与真相之间作出选择。" {
		t.Fatalf("premise = %q", foundation.Premise)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		t.Fatal(err)
	}
	if review == nil || review.FoundationBaseRevision != foundation.Revision ||
		!slices.Contains(review.FoundationSections, "premise") {
		t.Fatalf("review did not bind the confirmed premise: %+v", review)
	}
}
