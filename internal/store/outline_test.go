package store

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func setupLayered(t *testing.T, volumes []domain.VolumeOutline) *Store {
	t.Helper()
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := s.Progress.SetLayered(true); err != nil {
		t.Fatalf("SetLayered: %v", err)
	}
	return s
}

func TestFindDuplicateOutlineRepairBatchMapsDuplicateToLaterArc(t *testing.T) {
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "第一卷",
		Theme: "试炼",
		Arcs: []domain.ArcOutline{
			{
				Index: 1,
				Title: "入局",
				Goal:  "立住目标",
				Chapters: []domain.OutlineEntry{
					{Title: "鹰符潜入", CoreEvent: "良逸发现妖风为幻象，找到祭台入口。", Hook: "苏幼仪被困。"},
					{Title: "地宫追击", CoreEvent: "三人追入地宫，夺回半枚阵旗。", Hook: "阵旗反噬。"},
				},
			},
			{
				Index: 2,
				Title: "破局",
				Goal:  "识破骗局",
				Chapters: []domain.OutlineEntry{
					{Title: "鹰符潜入", CoreEvent: "良逸发现妖风为幻象，找到祭台入口。", Hook: "苏幼仪被困。"},
					{Title: "鹰符潜入", CoreEvent: "良逸发现妖风为幻象，找到祭台入口。", Hook: "苏幼仪被困。"},
				},
			},
		},
	}}
	s := setupLayered(t, volumes)
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           true,
		CompletedChapters: []int{1, 2, 3},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}

	batch, err := s.FindDuplicateOutlineRepairBatch(progress)
	if err != nil {
		t.Fatalf("FindDuplicateOutlineRepairBatch: %v", err)
	}
	if batch == nil || !batch.Repairable() {
		t.Fatalf("expected repairable batch, got %+v", batch)
	}
	if batch.Volume != 1 || batch.Arc != 2 {
		t.Fatalf("expected V1 A2, got V%d A%d", batch.Volume, batch.Arc)
	}
	if batch.FromChapter != 3 || batch.ToChapter != 4 || batch.ChapterCount != 2 {
		t.Fatalf("unexpected chapter range: %+v", batch)
	}
	if len(batch.CompletedChapters) != 1 || batch.CompletedChapters[0] != 3 {
		t.Fatalf("completed chapters = %v, want [3]", batch.CompletedChapters)
	}
	if batch.Duplicate.Chapter != 4 || batch.Duplicate.ExistingChapter != 3 {
		t.Fatalf("duplicate = %+v, want chapter 4 repeating chapter 3", batch.Duplicate)
	}
}

func TestFindDuplicateOutlineRepairBatchUsesSmallWindowForLargeArc(t *testing.T) {
	motifs := []struct {
		title string
		core  string
		hook  string
	}{
		{"Harbor Cipher", "A salt-stained ferry ledger exposes the smuggler's coded tide schedule.", "The tide bell rings from a locked boathouse."},
		{"Glass Observatory", "A cracked telescope lens reveals that the comet signal was forged before sunrise.", "The repaired lens shows two moons."},
		{"Market Intercept", "A forged pass changes hands in the spice market and redirects the pursuit.", "The ribbon carries the wrong family crest."},
		{"Cistern Descent", "A dry cistern opens into flooded service tunnels beneath the courthouse.", "Water rises inside a room marked dry."},
		{"Theater Switch", "A poisoned stage prop is swapped before the masked patron can signal the actor.", "The applause hides a second command."},
		{"Rooftop Accord", "A rival scout bargains across tiled roofs and trades protection for a lantern code.", "The rival asks for shelter instead of coin."},
		{"Archive Lock", "Burned archive slips reconstruct the missing index and expose a living witness.", "The wax tube contains a fresh fingerprint."},
		{"Garden Trial", "Courtyard footprints disprove the gardener's alibi and uncover a seed-jar message.", "The seed jar rattles with metal."},
		{"Train Cipher", "A night-train baggage tag leads the cast into the silent mail car.", "The mailbag is stitched with a royal warning."},
		{"Quarry Signal", "The marble quarry's blasting horn reveals a chalk route behind the powder shed.", "The next blast is scheduled too early."},
		{"Clockmaker Visit", "A stopped pendulum and a coded gear expose the clockmaker's hidden buyer.", "The gear turns without a spring."},
		{"Monastery Ledger", "A bell keeper's ledger traces the missing donation into a sealed crypt.", "The bell rings thirteen times at noon."},
		{"Canal Ambush", "A courier boat is tricked in canal fog and loses a tin cylinder from its tow rope.", "The tow rope is cut from the wrong bank."},
		{"Library Trial", "Three marginal notes in the atlas room overturn a planted confession.", "The atlas page shows an impossible border."},
		{"Foundry Bargain", "A stolen bronze mold is cooled before the foreman names the midnight buyer.", "The mold carries a fresh thumbprint."},
		{"Clinic Ledger", "A charity clinic ledger exposes medicine hidden inside a prayer box.", "The medicine label has tomorrow's date."},
		{"Lighthouse Code", "A lighthouse shutter rhythm reveals a waiting ship beyond the reef.", "The answering light comes from an empty sea."},
		{"Museum Swap", "A replica idol switch catches the curator signaling through a cracked mirror.", "The mirror shows the vault from outside."},
	}
	largeArc := make([]domain.OutlineEntry, 18)
	for i := range largeArc {
		largeArc[i] = domain.OutlineEntry{
			Title:     motifs[i].title,
			CoreEvent: motifs[i].core,
			Hook:      motifs[i].hook,
		}
	}
	largeArc[0] = domain.OutlineEntry{
		Title:     "Million Ant Army",
		CoreEvent: "The heroine discovers the sealed chamber and faces a wall of engineered ants.",
		Hook:      "The ants begin moving as one intelligence.",
	}
	largeArc[12] = largeArc[0]

	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "Volume",
		Theme: "Theme",
		Arcs: []domain.ArcOutline{
			{Index: 1, Title: "Earlier Arc", Goal: "Earlier work", EstimatedChapters: 49},
			{Index: 2, Title: "Large Arc", Goal: "Repair only a window", Chapters: largeArc},
		},
	}}
	s := setupLayered(t, volumes)
	if err := s.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           true,
		CompletedChapters: []int{50, 51, 62, 64},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}

	batch, err := s.FindDuplicateOutlineRepairBatch(progress)
	if err != nil {
		t.Fatalf("FindDuplicateOutlineRepairBatch: %v", err)
	}
	if batch == nil || !batch.Repairable() {
		t.Fatalf("expected repairable batch, got %+v", batch)
	}
	if batch.Volume != 1 || batch.Arc != 2 {
		t.Fatalf("expected V1 A2, got V%d A%d", batch.Volume, batch.Arc)
	}
	if batch.FromChapter != 62 || batch.ToChapter != 64 || batch.ChapterCount != 3 {
		t.Fatalf("repair window = %+v, want chapters 62-64", batch)
	}
	if got := batch.CompletedChapters; len(got) != 2 || got[0] != 62 || got[1] != 64 {
		t.Fatalf("completed chapters in window = %v, want [62 64]", got)
	}
	if batch.Duplicate.Chapter != 62 || batch.Duplicate.ExistingChapter != 50 {
		t.Fatalf("duplicate = %+v, want chapter 62 repeating chapter 50", batch.Duplicate)
	}
}

func TestFindDuplicateOutlineRepairBatchChecksAdaptationParentVolume(t *testing.T) {
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "Parent Batch",
		Theme: "One generated parent range",
		Arcs: []domain.ArcOutline{
			{
				Index: 1,
				Title: "Parent Batch (1-3)",
				Goal:  "First small detail batch",
				Chapters: []domain.OutlineEntry{
					{Title: "Million Ant Army", CoreEvent: "The heroine enters the sealed chamber and faces engineered ants.", Hook: "The ants begin moving as one intelligence."},
					{Title: "Copper Witness", CoreEvent: "A witness trades a copper badge for protection.", Hook: "The badge carries a hidden serial number."},
					{Title: "Rooftop Ledger", CoreEvent: "The team finds a ledger on the rooftop laundry line.", Hook: "The ink changes under rainwater."},
				},
			},
			{
				Index: 2,
				Title: "Parent Batch (4-6)",
				Goal:  "Second small detail batch",
				Chapters: []domain.OutlineEntry{
					{Title: "Clinic Tunnel", CoreEvent: "A tunnel under the clinic redirects the escape route.", Hook: "The tunnel lights switch on by themselves."},
					{Title: "Million Ant Army", CoreEvent: "The heroine enters the sealed chamber and faces engineered ants.", Hook: "The ants begin moving as one intelligence."},
					{Title: "Glass Verdict", CoreEvent: "A glass report exposes who signed the false order.", Hook: "The signature belongs to a dead official."},
				},
			},
		},
	}}
	s := setupLayered(t, volumes)
	if err := s.Adaptation.SavePlan(domain.AdaptationPlan{
		Status: domain.AdaptationPlanStatusConfirmed,
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "Million Ant Army"},
		},
	}); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           true,
		CompletedChapters: []int{4, 5},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}

	batch, err := s.FindDuplicateOutlineRepairBatch(progress)
	if err != nil {
		t.Fatalf("FindDuplicateOutlineRepairBatch: %v", err)
	}
	if batch == nil || !batch.Repairable() {
		t.Fatalf("expected repairable batch, got %+v", batch)
	}
	if batch.Volume != 1 || batch.Arc != 2 {
		t.Fatalf("expected V1 A2, got V%d A%d", batch.Volume, batch.Arc)
	}
	if batch.FromChapter != 4 || batch.ToChapter != 6 || batch.ChapterCount != 3 {
		t.Fatalf("repair window = %+v, want chapters 4-6", batch)
	}
	if batch.Duplicate.Chapter != 5 || batch.Duplicate.ExistingChapter != 1 {
		t.Fatalf("duplicate = %+v, want chapter 5 repeating chapter 1 inside parent batch", batch.Duplicate)
	}
}

func TestRepairArcOutlineUpdatesFlatLayeredAndAdaptationPlan(t *testing.T) {
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "第一卷",
		Theme: "试炼",
		Arcs: []domain.ArcOutline{{
			Index: 1,
			Title: "首弧",
			Goal:  "立住目标",
			Chapters: []domain.OutlineEntry{
				{Title: "旧一", CoreEvent: "旧事件一", Hook: "旧钩子一"},
				{Title: "旧二", CoreEvent: "旧事件二", Hook: "旧钩子二"},
			},
		}},
	}}
	s := setupLayered(t, volumes)
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Adaptation.SavePlan(domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityChapter,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Chapters: []domain.AdaptationChapterPlan{
			{
				Chapter:        1,
				Title:          "旧一",
				OutlineEntry:   domain.OutlineEntry{CoreEvent: "旧事件一", Hook: "旧钩子一"},
				SourceChapters: []int{10},
				WordBudget:     &domain.AdaptationChapterWordBudget{SourceRunes: 1000, TargetRunes: 2000, MinRunes: 1700, MaxRunes: 2300, Tolerance: 0.15},
			},
			{
				Chapter:         2,
				Title:           "旧二",
				OutlineEntry:    domain.OutlineEntry{CoreEvent: "旧事件二", Hook: "旧钩子二"},
				SourceChapters:  []int{11, 12},
				RequiredChanges: []string{"保留旧线索"},
				WordBudget:      &domain.AdaptationChapterWordBudget{SourceRunes: 1200, TargetRunes: 2200, MinRunes: 1900, MaxRunes: 2500, Tolerance: 0.15},
			},
		},
	}); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}

	repaired := []domain.OutlineEntry{
		{Title: "新一", CoreEvent: "良逸改从侧门潜入。", Hook: "侧门留下青色符痕。", Scenes: []string{"侧门", "符痕"}},
		{Title: "新二", CoreEvent: "苏幼仪主动设局反制。", Hook: "她把钥匙交给敌人。", Scenes: []string{"赌局", "钥匙"}},
	}
	if err := s.RepairArcOutline(1, 1, repaired); err != nil {
		t.Fatalf("RepairArcOutline: %v", err)
	}

	flat, err := s.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("LoadOutline: %v", err)
	}
	if len(flat) != 2 || flat[1].Chapter != 2 || flat[1].Title != "新二" {
		t.Fatalf("flat outline not repaired: %+v", flat)
	}
	layered, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("LoadLayeredOutline: %v", err)
	}
	if got := layered[0].Arcs[0].Chapters[1]; got.Chapter != 2 || got.CoreEvent != "苏幼仪主动设局反制。" {
		t.Fatalf("layered outline not repaired: %+v", got)
	}
	plan, err := s.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if plan.Chapters[1].Title != "新二" || plan.Chapters[1].OutlineEntry.Hook != "她把钥匙交给敌人。" {
		t.Fatalf("adaptation plan outline not repaired: %+v", plan.Chapters[1])
	}
	if len(plan.Chapters[1].SourceChapters) != 2 || plan.Chapters[1].SourceChapters[0] != 11 || plan.Chapters[1].SourceChapters[1] != 12 {
		t.Fatalf("source anchors should be preserved: %+v", plan.Chapters[1].SourceChapters)
	}
	if len(plan.Chapters[1].RequiredChanges) != 1 || plan.Chapters[1].RequiredChanges[0] != "保留旧线索" {
		t.Fatalf("required changes should be preserved: %+v", plan.Chapters[1].RequiredChanges)
	}
	if plan.Chapters[1].WordBudget == nil || plan.Chapters[1].WordBudget.TargetRunes != 2200 {
		t.Fatalf("word budget should be preserved: %+v", plan.Chapters[1].WordBudget)
	}
}

func TestRepairArcOutlineRejectsDuplicateReplacement(t *testing.T) {
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "Volume",
		Theme: "Theme",
		Arcs: []domain.ArcOutline{
			{
				Index: 1,
				Title: "Arc One",
				Goal:  "Goal",
				Chapters: []domain.OutlineEntry{
					{Title: "Mirror Door Signal", CoreEvent: "The first chapter opens a distinct clue trail.", Hook: "The signal points to a sealed door."},
				},
			},
			{
				Index: 2,
				Title: "Arc Two",
				Goal:  "Goal",
				Chapters: []domain.OutlineEntry{
					{Title: "Old Chapter", CoreEvent: "The second arc starts elsewhere.", Hook: "A different clue appears."},
					{Title: "Older Chapter", CoreEvent: "The second arc keeps moving.", Hook: "Another clue appears."},
				},
			},
		},
	}}
	s := setupLayered(t, volumes)
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}

	err := s.RepairArcOutline(1, 2, []domain.OutlineEntry{
		{Title: "Mirror Door Signal", CoreEvent: "The repaired chapter follows another suspect path.", Hook: "A new witness withholds evidence."},
		{Title: "Mirror Door Signal", CoreEvent: "The repaired chapter follows a later suspect path.", Hook: "Another witness withholds evidence."},
	})
	if err == nil {
		t.Fatal("expected duplicate replacement to be rejected")
	}
	if !strings.Contains(err.Error(), "duplicate chapter outline") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestRepairArcOutlineRangeRejectsDuplicateAgainstAdaptationParentVolume(t *testing.T) {
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "Parent Batch",
		Theme: "One generated parent range",
		Arcs: []domain.ArcOutline{
			{
				Index: 1,
				Title: "Parent Batch (1-3)",
				Goal:  "First small detail batch",
				Chapters: []domain.OutlineEntry{
					{Title: "Million Ant Army", CoreEvent: "The heroine enters the sealed chamber and faces engineered ants.", Hook: "The ants begin moving as one intelligence."},
					{Title: "Copper Witness", CoreEvent: "A witness trades a copper badge for protection.", Hook: "The badge carries a hidden serial number."},
					{Title: "Rooftop Ledger", CoreEvent: "The team finds a ledger on the rooftop laundry line.", Hook: "The ink changes under rainwater."},
				},
			},
			{
				Index: 2,
				Title: "Parent Batch (4-6)",
				Goal:  "Second small detail batch",
				Chapters: []domain.OutlineEntry{
					{Title: "Clinic Tunnel", CoreEvent: "A tunnel under the clinic redirects the escape route.", Hook: "The tunnel lights switch on by themselves."},
					{Title: "Paper Swarm", CoreEvent: "A paper swarm blocks the rear archive.", Hook: "Every page carries the same address."},
					{Title: "Glass Verdict", CoreEvent: "A glass report exposes who signed the false order.", Hook: "The signature belongs to a dead official."},
				},
			},
		},
	}}
	s := setupLayered(t, volumes)
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Adaptation.SavePlan(domain.AdaptationPlan{
		Status: domain.AdaptationPlanStatusConfirmed,
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "Million Ant Army"},
		},
	}); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}

	err := s.RepairArcOutlineRange(1, 2, 5, 6, []domain.OutlineEntry{
		{Title: "Million Ant Army", CoreEvent: "The heroine enters the sealed chamber and faces engineered ants.", Hook: "The ants begin moving as one intelligence."},
		{Title: "Glass Verdict Renewed", CoreEvent: "The repaired report moves the accusation to a living conspirator.", Hook: "The witness refuses the obvious name."},
	})
	if err == nil {
		t.Fatal("expected parent-batch duplicate replacement to be rejected")
	}
	if !strings.Contains(err.Error(), "repair_arc V1 parent batch contains duplicate chapter outline") {
		t.Fatalf("expected parent batch duplicate error, got %v", err)
	}
}

func TestRepairArcOutlineDeletesBatchArtifactsAndQueuesCompletedChapters(t *testing.T) {
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "Volume",
		Theme: "Theme",
		Arcs: []domain.ArcOutline{{
			Index: 1,
			Title: "Arc",
			Goal:  "Goal",
			Chapters: []domain.OutlineEntry{
				{Title: "Old One", CoreEvent: "Old event one", Hook: "Old hook one"},
				{Title: "Old Two", CoreEvent: "Old event two", Hook: "Old hook two"},
				{Title: "Old Three", CoreEvent: "Old event three", Hook: "Old hook three"},
			},
		}},
	}}
	s := setupLayered(t, volumes)
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowPolishing,
		Layered:           true,
		CompletedChapters: []int{1, 2},
		PendingRewrites:   []int{99},
		RewriteReason:     "old queue",
		InProgressChapter: 3,
		CompletedScenes:   []int{1, 2},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	for chapter := 1; chapter <= 3; chapter++ {
		if err := s.Drafts.SaveChapterPlan(domain.ChapterPlan{Chapter: chapter, Title: "old plan"}); err != nil {
			t.Fatalf("SaveChapterPlan %d: %v", chapter, err)
		}
		if err := s.Drafts.SaveDraft(chapter, "old draft"); err != nil {
			t.Fatalf("SaveDraft %d: %v", chapter, err)
		}
		if err := s.Drafts.SaveFinalChapter(chapter, "old final"); err != nil {
			t.Fatalf("SaveFinalChapter %d: %v", chapter, err)
		}
		if err := s.Summaries.SaveSummary(domain.ChapterSummary{Chapter: chapter, Summary: "old summary"}); err != nil {
			t.Fatalf("SaveSummary %d: %v", chapter, err)
		}
		if err := s.World.SaveReview(domain.ReviewEntry{Chapter: chapter, Scope: "chapter", Verdict: "accept"}); err != nil {
			t.Fatalf("SaveReview %d: %v", chapter, err)
		}
		if err := s.Adaptation.SaveCheck(domain.AdaptationCheck{Chapter: chapter, DraftSHA256: "old", CheckedAt: "now"}); err != nil {
			t.Fatalf("SaveCheck %d: %v", chapter, err)
		}
	}
	if err := s.World.SaveReview(domain.ReviewEntry{Chapter: 3, Scope: "global", Verdict: "accept"}); err != nil {
		t.Fatalf("Save global review: %v", err)
	}
	if err := s.World.AppendTimelineEvents([]domain.TimelineEvent{
		{Chapter: 1, Time: "old", Event: "old chapter 1 event"},
		{Chapter: 3, Time: "old", Event: "old chapter 3 event"},
		{Chapter: 4, Time: "keep", Event: "unrelated event"},
	}); err != nil {
		t.Fatalf("AppendTimelineEvents: %v", err)
	}
	if err := s.World.UpdateForeshadow(2, []domain.ForeshadowUpdate{{ID: "old-plant", Action: "plant", Description: "old clue"}}); err != nil {
		t.Fatalf("UpdateForeshadow plant: %v", err)
	}
	if err := s.World.UpdateForeshadow(4, []domain.ForeshadowUpdate{{ID: "keep-plant", Action: "plant", Description: "keep clue"}}); err != nil {
		t.Fatalf("UpdateForeshadow keep plant: %v", err)
	}
	if err := s.World.UpdateForeshadow(4, []domain.ForeshadowUpdate{{ID: "keep-plant", Action: "resolve"}}); err != nil {
		t.Fatalf("UpdateForeshadow keep resolve: %v", err)
	}
	if err := s.World.UpdateRelationships([]domain.RelationshipEntry{
		{CharacterA: "A", CharacterB: "B", Relation: "old", Chapter: 2},
		{CharacterA: "A", CharacterB: "C", Relation: "keep", Chapter: 4},
	}); err != nil {
		t.Fatalf("UpdateRelationships: %v", err)
	}
	if err := s.World.AppendStateChanges([]domain.StateChange{
		{Chapter: 3, Entity: "A", Field: "mood", NewValue: "old"},
		{Chapter: 4, Entity: "A", Field: "mood", NewValue: "keep"},
	}); err != nil {
		t.Fatalf("AppendStateChanges: %v", err)
	}
	if err := s.Cast.MergeAppearances(2, []string{"Old Side"}, []domain.CastIntro{{Name: "Old Side", BriefRole: "old"}}, nil); err != nil {
		t.Fatalf("MergeAppearances old: %v", err)
	}
	if err := s.Cast.MergeAppearances(4, []string{"Keep Side"}, []domain.CastIntro{{Name: "Keep Side", BriefRole: "keep"}}, nil); err != nil {
		t.Fatalf("MergeAppearances keep: %v", err)
	}
	if err := s.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Summary: "old arc"}); err != nil {
		t.Fatalf("SaveArcSummary: %v", err)
	}
	if err := s.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: 1, Summary: "old volume"}); err != nil {
		t.Fatalf("SaveVolumeSummary: %v", err)
	}

	repaired := []domain.OutlineEntry{
		{Title: "New One", CoreEvent: "A copper compass exposes the first route through the abandoned station.", Hook: "The compass needle points underground."},
		{Title: "New Two", CoreEvent: "A winter courier forces the cast to bargain inside the flooded market.", Hook: "The courier names a price no one expected."},
		{Title: "New Three", CoreEvent: "A glass timetable reveals the final delay before the eastern bridge opens.", Hook: "The timetable changes while everyone watches."},
	}
	if err := s.RepairArcOutline(1, 1, repaired); err != nil {
		t.Fatalf("RepairArcOutline: %v", err)
	}

	for chapter := 1; chapter <= 3; chapter++ {
		if plan, err := s.Drafts.LoadChapterPlan(chapter); err != nil || plan != nil {
			t.Fatalf("chapter %d plan = %+v, err=%v; want nil", chapter, plan, err)
		}
		if draft, err := s.Drafts.LoadDraft(chapter); err != nil || draft != "" {
			t.Fatalf("chapter %d draft = %q, err=%v; want empty", chapter, draft, err)
		}
		if final, err := s.Drafts.LoadChapterText(chapter); err != nil || final != "" {
			t.Fatalf("chapter %d final = %q, err=%v; want empty", chapter, final, err)
		}
		if summary, err := s.Summaries.LoadSummary(chapter); err != nil || summary != nil {
			t.Fatalf("chapter %d summary = %+v, err=%v; want nil", chapter, summary, err)
		}
		if review, err := s.World.LoadReview(chapter); err != nil || review != nil {
			t.Fatalf("chapter %d review = %+v, err=%v; want nil", chapter, review, err)
		}
		if check, err := s.Adaptation.LoadCheck(chapter); err != nil || check != nil {
			t.Fatalf("chapter %d check = %+v, err=%v; want nil", chapter, check, err)
		}
	}
	if review, err := s.World.LoadLastReview(3); err != nil || review != nil {
		t.Fatalf("global review = %+v, err=%v; want nil", review, err)
	}
	if summary, err := s.Summaries.LoadArcSummary(1, 1); err != nil || summary != nil {
		t.Fatalf("arc summary = %+v, err=%v; want nil", summary, err)
	}
	if summary, err := s.Summaries.LoadVolumeSummary(1); err != nil || summary != nil {
		t.Fatalf("volume summary = %+v, err=%v; want nil", summary, err)
	}
	timeline, err := s.World.LoadTimeline()
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}
	if len(timeline) != 1 || timeline[0].Chapter != 4 {
		t.Fatalf("timeline after repair = %+v, want only chapter 4 event", timeline)
	}
	foreshadow, err := s.World.LoadForeshadowLedger()
	if err != nil {
		t.Fatalf("LoadForeshadowLedger: %v", err)
	}
	if len(foreshadow) != 1 || foreshadow[0].ID != "keep-plant" || foreshadow[0].Status != "resolved" || foreshadow[0].ResolvedAt != 4 {
		t.Fatalf("foreshadow after repair = %+v, want unrelated resolved clue", foreshadow)
	}
	relationships, err := s.World.LoadRelationships()
	if err != nil {
		t.Fatalf("LoadRelationships: %v", err)
	}
	if len(relationships) != 1 || relationships[0].Chapter != 4 {
		t.Fatalf("relationships after repair = %+v, want only chapter 4 relationship", relationships)
	}
	stateChanges, err := s.World.LoadStateChanges()
	if err != nil {
		t.Fatalf("LoadStateChanges: %v", err)
	}
	if len(stateChanges) != 1 || stateChanges[0].Chapter != 4 {
		t.Fatalf("state changes after repair = %+v, want only chapter 4 change", stateChanges)
	}
	castEntries, err := s.Cast.Load()
	if err != nil {
		t.Fatalf("Load cast: %v", err)
	}
	if len(castEntries) != 1 || castEntries[0].Name != "Keep Side" {
		t.Fatalf("cast after repair = %+v, want only unrelated cast entry", castEntries)
	}

	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	if progress.Flow != domain.FlowRewriting {
		t.Fatalf("flow = %s, want rewriting", progress.Flow)
	}
	if len(progress.PendingRewrites) != 3 || progress.PendingRewrites[0] != 1 || progress.PendingRewrites[1] != 2 || progress.PendingRewrites[2] != 99 {
		t.Fatalf("pending rewrites = %v, want [1 2 99]", progress.PendingRewrites)
	}
	if !strings.Contains(progress.RewriteReason, "old queue") || !strings.Contains(progress.RewriteReason, "outline repair V1 A1") {
		t.Fatalf("rewrite reason did not preserve old reason and append repair reason: %q", progress.RewriteReason)
	}
	if progress.InProgressChapter != 0 || len(progress.CompletedScenes) != 0 {
		t.Fatalf("in-progress state not cleared: chapter=%d scenes=%v", progress.InProgressChapter, progress.CompletedScenes)
	}
	if len(progress.CompletedChapters) != 2 || progress.CompletedChapters[0] != 1 || progress.CompletedChapters[1] != 2 {
		t.Fatalf("completed chapters should be preserved, got %v", progress.CompletedChapters)
	}
}

func TestRepairArcOutlineClearsOldWordCountsForRewrittenChapters(t *testing.T) {
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "Volume",
		Theme: "Theme",
		Arcs: []domain.ArcOutline{{
			Index: 1,
			Title: "Arc",
			Goal:  "Goal",
			Chapters: []domain.OutlineEntry{
				{Title: "Old One", CoreEvent: "Old event one", Hook: "Old hook one"},
				{Title: "Old Two", CoreEvent: "Old event two", Hook: "Old hook two"},
			},
		}},
	}}
	s := setupLayered(t, volumes)
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete 1: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, 2000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete 2: %v", err)
	}
	for chapter := 1; chapter <= 2; chapter++ {
		if err := s.Drafts.SaveFinalChapter(chapter, "old final"); err != nil {
			t.Fatalf("SaveFinalChapter %d: %v", chapter, err)
		}
	}

	repaired := []domain.OutlineEntry{
		{Title: "New One", CoreEvent: "A brass letter starts a new route through the station.", Hook: "The letter changes hands."},
		{Title: "New Two", CoreEvent: "A winter signal reveals a different suspect network.", Hook: "The signal points below ground."},
	}
	if err := s.RepairArcOutline(1, 1, repaired); err != nil {
		t.Fatalf("RepairArcOutline: %v", err)
	}

	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	if progress.TotalWordCount != 0 {
		t.Fatalf("total word count = %d, want 0 after stale finals are deleted", progress.TotalWordCount)
	}
	if len(progress.ChapterWordCounts) != 0 {
		t.Fatalf("chapter word counts = %v, want empty", progress.ChapterWordCounts)
	}
	if len(progress.PendingRewrites) != 2 || progress.PendingRewrites[0] != 1 || progress.PendingRewrites[1] != 2 {
		t.Fatalf("pending rewrites = %v, want [1 2]", progress.PendingRewrites)
	}
}

func TestFindDuplicateOutlineRepairBatchCompletesPendingFinalizationBeforeCleanMarker(t *testing.T) {
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "Volume",
		Theme: "Theme",
		Arcs: []domain.ArcOutline{{
			Index: 1,
			Title: "Arc",
			Goal:  "Goal",
			Chapters: []domain.OutlineEntry{
				{Title: "New One", CoreEvent: "A brass letter starts a new route through the station.", Hook: "The letter changes hands."},
				{Title: "New Two", CoreEvent: "A winter signal reveals a different suspect network.", Hook: "The signal points below ground."},
			},
		}},
	}}
	s := setupLayered(t, volumes)
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete 1: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, 2000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete 2: %v", err)
	}
	for chapter := 1; chapter <= 2; chapter++ {
		if err := s.Drafts.SaveFinalChapter(chapter, "old final"); err != nil {
			t.Fatalf("SaveFinalChapter %d: %v", chapter, err)
		}
	}
	repaired := []domain.OutlineEntry{
		{Chapter: 1, Title: "New One", CoreEvent: "A brass letter starts a new route through the station.", Hook: "The letter changes hands."},
		{Chapter: 2, Title: "New Two", CoreEvent: "A winter signal reveals a different suspect network.", Hook: "The signal points below ground."},
	}
	if err := s.saveOutlineRepairFinalization(1, 1, repaired, outlineRepairFinalizationStageOutlineReplaced); err != nil {
		t.Fatalf("saveOutlineRepairFinalization: %v", err)
	}

	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	batch, err := s.FindDuplicateOutlineRepairBatch(progress)
	if err != nil {
		t.Fatalf("FindDuplicateOutlineRepairBatch: %v", err)
	}
	if batch != nil {
		t.Fatalf("batch = %+v, want nil after finalization", batch)
	}
	if marker, err := s.loadOutlineRepairFinalization(); err != nil || marker != nil {
		t.Fatalf("pending finalization marker = %+v, err=%v; want cleared", marker, err)
	}
	progress, err = s.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress after finalization: %v", err)
	}
	if progress.TotalWordCount != 0 {
		t.Fatalf("total word count = %d, want 0", progress.TotalWordCount)
	}
	if len(progress.PendingRewrites) != 2 || progress.PendingRewrites[0] != 1 || progress.PendingRewrites[1] != 2 {
		t.Fatalf("pending rewrites = %v, want [1 2]", progress.PendingRewrites)
	}
	if !s.outlineDuplicateScanCurrent(progress) {
		t.Fatal("clean duplicate scan marker should be saved only after finalization")
	}
}

func TestReconcilePendingRewriteProgressClearsDeletedChapterWordCounts(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		NovelName:         "test",
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowRewriting,
		CompletedChapters: []int{1, 2, 3},
		PendingRewrites:   []int{2, 3},
		TotalWordCount:    600,
		ChapterWordCounts: map[int]int{1: 100, 2: 200, 3: 300},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(1, "existing final"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}

	progress, err := s.ReconcilePendingRewriteProgress()
	if err != nil {
		t.Fatalf("ReconcilePendingRewriteProgress: %v", err)
	}
	if progress.TotalWordCount != 100 {
		t.Fatalf("total word count = %d, want 100", progress.TotalWordCount)
	}
	if _, ok := progress.ChapterWordCounts[2]; ok {
		t.Fatalf("chapter 2 word count should be cleared: %v", progress.ChapterWordCounts)
	}
	if _, ok := progress.ChapterWordCounts[3]; ok {
		t.Fatalf("chapter 3 word count should be cleared: %v", progress.ChapterWordCounts)
	}
	if warnings := s.CheckConsistency(); len(warnings) != 0 {
		t.Fatalf("CheckConsistency warnings = %v, want none for pending rewrite gaps", warnings)
	}
}

func TestCheckArcBoundaryNeedsNewVolume(t *testing.T) {
	// 只有 1 卷 1 弧 1 章，且非 Final → 应触发 NeedsNewVolume
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "第一卷", Theme: "起步",
		Arcs: []domain.ArcOutline{{
			Index: 1, Title: "首弧", Goal: "目标",
			Chapters: []domain.OutlineEntry{{Title: "第一章", CoreEvent: "开局", Hook: "继续"}},
		}},
	}})

	b, err := s.Outline.CheckArcBoundary(1) // 第 1 章 = 弧/卷最后一章
	if err != nil {
		t.Fatalf("CheckArcBoundary: %v", err)
	}
	if b == nil {
		t.Fatal("expected boundary, got nil")
	}
	if !b.IsArcEnd || !b.IsVolumeEnd {
		t.Fatalf("expected arc+volume end, got arc=%v vol=%v", b.IsArcEnd, b.IsVolumeEnd)
	}
	if !b.NeedsNewVolume {
		t.Fatal("expected NeedsNewVolume=true")
	}
	if b.NextVolume != 0 || b.NextArc != 0 {
		t.Fatalf("expected no next, got vol=%d arc=%d", b.NextVolume, b.NextArc)
	}
}

func TestCheckArcBoundaryLastVolumeRequiresDecision(t *testing.T) {
	// 单卷最后一章 → 触发 NeedsNewVolume，让 Router 让架构师二选一：
	// append_volume 续写 / complete_book 收尾。
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "唯一卷", Theme: "主题",
		Arcs: []domain.ArcOutline{{
			Index: 1, Title: "唯一弧", Goal: "收束",
			Chapters: []domain.OutlineEntry{{Title: "终章", CoreEvent: "结局", Hook: "无"}},
		}},
	}})

	b, err := s.Outline.CheckArcBoundary(1)
	if err != nil {
		t.Fatalf("CheckArcBoundary: %v", err)
	}
	if !b.NeedsNewVolume {
		t.Fatal("expected NeedsNewVolume=true at last expanded chapter")
	}
	if b.HasNextArc() {
		t.Fatal("expected no next arc")
	}
}

func TestCheckArcBoundaryNextArcInSameVolume(t *testing.T) {
	// 2 弧：第 1 弧结束应指向第 2 弧，不触发 NeedsNewVolume
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "第一卷", Theme: "起步",
		Arcs: []domain.ArcOutline{
			{Index: 1, Title: "首弧", Goal: "目标", Chapters: []domain.OutlineEntry{{Title: "章一", CoreEvent: "事件", Hook: "钩子"}}},
			{Index: 2, Title: "次弧", Goal: "目标2", EstimatedChapters: 10},
		},
	}})

	b, err := s.Outline.CheckArcBoundary(1)
	if err != nil {
		t.Fatalf("CheckArcBoundary: %v", err)
	}
	if !b.IsArcEnd {
		t.Fatal("expected arc end")
	}
	if b.IsVolumeEnd {
		t.Fatal("expected not volume end (second arc exists)")
	}
	if b.NeedsNewVolume {
		t.Fatal("expected NeedsNewVolume=false")
	}
	if b.NextVolume != 1 || b.NextArc != 2 {
		t.Fatalf("expected next vol=1 arc=2, got vol=%d arc=%d", b.NextVolume, b.NextArc)
	}
	if !b.NeedsExpansion {
		t.Fatal("expected NeedsExpansion=true for skeleton arc")
	}
}

func TestAppendVolumeValidation(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "第一卷", Theme: "起步",
		Arcs: []domain.ArcOutline{{
			Index: 1, Title: "首弧", Goal: "目标",
			Chapters: []domain.OutlineEntry{{Title: "章", CoreEvent: "事件", Hook: "钩子"}},
		}},
	}})

	validVol := domain.VolumeOutline{
		Index: 2, Title: "第二卷", Theme: "升级",
		Arcs: []domain.ArcOutline{{
			Index: 1, Title: "弧一", Goal: "目标",
			Chapters: []domain.OutlineEntry{{Title: "新章", CoreEvent: "推进", Hook: "钩子"}},
		}},
	}

	// 正常追加应成功
	if err := s.AppendVolume(validVol); err != nil {
		t.Fatalf("AppendVolume valid: %v", err)
	}

	// Index 不递增 → 失败
	if err := s.AppendVolume(domain.VolumeOutline{
		Index: 1, Title: "重复", Theme: "x",
		Arcs: []domain.ArcOutline{{Index: 1, Title: "弧", Goal: "g", Chapters: []domain.OutlineEntry{{Title: "ch", CoreEvent: "e", Hook: "h"}}}},
	}); err == nil {
		t.Fatal("expected error for non-increasing index")
	}

	// 无弧 → 失败
	if err := s.AppendVolume(domain.VolumeOutline{Index: 3, Title: "空", Theme: "x"}); err == nil {
		t.Fatal("expected error for volume with no arcs")
	}

	// 首弧无章节 → 失败
	if err := s.AppendVolume(domain.VolumeOutline{
		Index: 3, Title: "骨架", Theme: "x",
		Arcs: []domain.ArcOutline{{Index: 1, Title: "弧", Goal: "g", EstimatedChapters: 10}},
	}); err == nil {
		t.Fatal("expected error for first arc without chapters")
	}
}

// 注：原先用 Final 卷拒绝 append 的语义已下沉到 save_foundation 层（Phase=Complete 拒绝），
// 见 save_foundation_test.go::TestSaveFoundationAppendVolumeRejectsAfterComplete。
// store 层只保留结构性校验（Index 递增 / 首弧含章节等）。

func TestSaveAndLoadCompass(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// 空 direction 应失败
	if err := s.Outline.SaveCompass(domain.StoryCompass{EstimatedScale: "3 卷"}); err == nil {
		t.Fatal("expected error for empty ending_direction")
	}

	// 正常保存
	compass := domain.StoryCompass{
		EndingDirection: "主角面对最终抉择",
		OpenThreads:     []string{"线索A", "关系B"},
		EstimatedScale:  "预计 4-6 卷",
		LastUpdated:     12,
	}
	if err := s.Outline.SaveCompass(compass); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}

	loaded, err := s.Outline.LoadCompass()
	if err != nil {
		t.Fatalf("LoadCompass: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected compass, got nil")
	}
	if loaded.EndingDirection != "主角面对最终抉择" {
		t.Fatalf("expected direction %q, got %q", "主角面对最终抉择", loaded.EndingDirection)
	}
	if len(loaded.OpenThreads) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(loaded.OpenThreads))
	}
}
