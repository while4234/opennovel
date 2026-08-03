package host

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestPrepareResumeOutlineRepairReportsBatchWithoutQueueing(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
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
					{Title: "Shared Promise", CoreEvent: "The team enters the archive and finds the sealed ledger before dawn.", Hook: "The ledger names the missing witness."},
					{Title: "Shared Promise", CoreEvent: "The team enters the archive and finds the sealed ledger before dawn.", Hook: "The ledger names the missing witness."},
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
					{Title: "黑风审讯", CoreEvent: "良逸逼问出密道钥匙。", Hook: "钥匙裂出血纹。"},
				},
			},
		},
	}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := st.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		NovelName:         "女魔头",
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowPolishing,
		Layered:           true,
		CompletedChapters: []int{1, 2, 3},
		PendingRewrites:   []int{2},
		RewriteReason:     "old polish",
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	result, err := prepareResumeOutlineRepair(st)
	if err != nil {
		t.Fatalf("prepareResumeOutlineRepair: %v", err)
	}
	if result == nil || result.Batch == nil || !result.Batch.Repairable() {
		t.Fatalf("expected repairable result, got %+v", result)
	}
	if result.Batch.Volume != 1 || result.Batch.Arc != 1 {
		t.Fatalf("expected V1 A1, got V%d A%d", result.Batch.Volume, result.Batch.Arc)
	}

	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	if progress.Flow != domain.FlowPolishing {
		t.Fatalf("flow = %s, want polishing", progress.Flow)
	}
	if len(progress.PendingRewrites) != 1 || progress.PendingRewrites[0] != 2 {
		t.Fatalf("pending rewrites = %v, want [2]", progress.PendingRewrites)
	}
	if progress.RewriteReason != "old polish" {
		t.Fatalf("rewrite reason = %q, want old polish", progress.RewriteReason)
	}
}

func TestDescribeResumeShowsOutlineRepairBeforeRewriteQueue(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Arcs: []domain.ArcOutline{
			{
				Index: 1,
				Chapters: []domain.OutlineEntry{
					{Title: "Shared Promise", CoreEvent: "The team enters the archive and finds the sealed ledger before dawn.", Hook: "The ledger names the missing witness."},
					{Title: "Shared Promise", CoreEvent: "The team enters the archive and finds the sealed ledger before dawn.", Hook: "The ledger names the missing witness."},
					{Title: "旧一", CoreEvent: "同一事件", Hook: "同一钩子"},
				},
			},
			{
				Index: 2,
				Chapters: []domain.OutlineEntry{
					{Title: "旧一", CoreEvent: "同一事件", Hook: "同一钩子"},
				},
			},
		},
	}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := st.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	progress := &domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowRewriting,
		Layered:           true,
		CompletedChapters: []int{1},
		PendingRewrites:   []int{1},
	}

	if label := describeResume(st, progress); label != "恢复：重复大纲待修复（V1 A1）" {
		t.Fatalf("label = %q", label)
	}
}
