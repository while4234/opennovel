package domain

import "testing"

func TestContinuationTransitionsEnforceReviewGates(t *testing.T) {
	valid := [][2]ContinuationStage{
		{ContinuationStageSourceReady, ContinuationStageDraftCollecting},
		{ContinuationStageDraftCollecting, ContinuationStageProposalGenerating},
		{ContinuationStageProposalGenerating, ContinuationStageProposalReviewPending},
		{ContinuationStageProposalReviewPending, ContinuationStageVolumeReviewPending},
		{ContinuationStageProposalReviewPending, ContinuationStageOutlineGenerating},
		{ContinuationStageVolumeReviewPending, ContinuationStageOutlineGenerating},
		{ContinuationStageOutlineGenerating, ContinuationStageOutlineReviewPending},
		{ContinuationStageOutlineReviewPending, ContinuationStageReadyToWrite},
		{ContinuationStageReadyToWrite, ContinuationStageWriting},
	}
	for _, transition := range valid {
		if err := ValidateContinuationTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("expected %s -> %s to be valid: %v", transition[0], transition[1], err)
		}
	}

	invalid := [][2]ContinuationStage{
		{ContinuationStageSourceReady, ContinuationStageWriting},
		{ContinuationStageProposalReviewPending, ContinuationStageReadyToWrite},
		{ContinuationStageVolumeReviewPending, ContinuationStageReadyToWrite},
		{ContinuationStageOutlineGenerating, ContinuationStageReadyToWrite},
	}
	for _, transition := range invalid {
		if err := ValidateContinuationTransition(transition[0], transition[1]); err == nil {
			t.Fatalf("expected %s -> %s to be rejected", transition[0], transition[1])
		}
	}
}

func TestFlattenContinuationOutlineRequiresBasePlusOneNumbering(t *testing.T) {
	outline := ContinuationOutline{
		Structure: ContinuationStructureSingle,
		Chapters: []OutlineEntry{
			{Chapter: 11, Title: "new one", CoreEvent: "event one"},
			{Chapter: 12, Title: "new two", CoreEvent: "event two"},
		},
	}
	chapters, err := FlattenContinuationOutline(10, outline)
	if err != nil {
		t.Fatalf("flatten: %v", err)
	}
	if got, want := chapters[0].Chapter, 11; got != want {
		t.Fatalf("first continuation chapter = %d, want %d", got, want)
	}

	outline.Chapters[0].Chapter = 10
	if _, err := FlattenContinuationOutline(10, outline); err == nil {
		t.Fatal("expected a non-N+1 first chapter to be rejected")
	}
}

func TestValidateContinuationVolumesCoversTargetChapterCount(t *testing.T) {
	proposal := ContinuationProposal{
		Summary:            "continue",
		Direction:          "resolve the open conflict",
		TargetChapterCount: 4,
		Structure:          ContinuationStructureVolumes,
	}
	volumes := []VolumeOutline{
		{Index: 1, Title: "first", Arcs: []ArcOutline{{Index: 1, Title: "a", EstimatedChapters: 2}}},
		{Index: 2, Title: "second", Arcs: []ArcOutline{{Index: 1, Title: "b", EstimatedChapters: 2}}},
	}
	if err := ValidateContinuationVolumes(proposal, volumes); err != nil {
		t.Fatalf("validate volumes: %v", err)
	}
	volumes[1].Arcs[0].EstimatedChapters = 1
	if err := ValidateContinuationVolumes(proposal, volumes); err == nil {
		t.Fatal("expected incomplete volume coverage to be rejected")
	}
}
