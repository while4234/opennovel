package flow

import (
	"fmt"

	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func formatOutlineRepairTask(batch *storepkg.OutlineRepairBatch) string {
	duplicate := batch.Duplicate
	return fmt.Sprintf(
		"Repair duplicated outline window V%d A%d only (global chapters %d-%d). Chapter %d duplicates chapter %d (%s, title %q). Call save_foundation(type=\"repair_arc\", volume=%d, arc=%d, from_chapter=%d, to_chapter=%d) with exactly %d chapter outline entries for that window. Keep the window chapter count unchanged, preserve continuity with earlier and later chapters, and make every title/core_event/hook/scenes detail distinct under the duplicate rule: identical long titles or highly similar detailed outline text are still duplicates. The tool will merge this small window back into the arc and validate the whole arc, so shortening a repeated outline is not a fix. If the tool reports borderline similar pairs, perform the required similarity_review judgment for this same window; if any pair is duplicate, rewrite the full window again before saving. Do not repair or include any other window in this call; after repair_arc succeeds the host will clean stale articles for this window, rescan, and dispatch the next window if needed. If this is adaptation mode, keep source anchors and word budgets conceptually unchanged; the tool will preserve those fields in the confirmed plan. Do not dispatch writer until repair_arc succeeds.",
		batch.Volume,
		batch.Arc,
		batch.FromChapter,
		batch.ToChapter,
		duplicate.Chapter,
		duplicate.ExistingChapter,
		duplicate.Reason,
		duplicate.Title,
		batch.Volume,
		batch.Arc,
		batch.FromChapter,
		batch.ToChapter,
		batch.ChapterCount,
	)
}
