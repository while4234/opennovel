package host

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type resumeOutlineRepairResult struct {
	Batch *storepkg.OutlineRepairBatch
}

func prepareResumeOutlineRepair(st *storepkg.Store) (*resumeOutlineRepairResult, error) {
	if st == nil {
		return nil, nil
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return nil, err
	}
	if progress.Phase != domain.PhaseWriting {
		return nil, nil
	}

	batch, err := st.FindDuplicateOutlineRepairBatch(progress)
	if err != nil || batch == nil {
		return nil, err
	}
	return &resumeOutlineRepairResult{Batch: batch}, nil
}

func formatResumeOutlineRepairNotice(result *resumeOutlineRepairResult) string {
	if result == nil || result.Batch == nil {
		return ""
	}
	batch := result.Batch
	duplicate := batch.Duplicate
	if !batch.Repairable() {
		return fmt.Sprintf(
			"恢复前发现重复大纲：第 %d 章重复第 %d 章，但当前大纲无法自动定位到已展开弧，请先人工修复大纲。",
			duplicate.Chapter,
			duplicate.ExistingChapter,
		)
	}
	if len(batch.CompletedChapters) == 0 {
		return fmt.Sprintf(
			"恢复前发现重复大纲：第 %d 章重复第 %d 章；将先单独修复 V%d A%d 批次大纲，该批次暂无已完成章节，修复后继续写作。",
			duplicate.Chapter,
			duplicate.ExistingChapter,
			batch.Volume,
			batch.Arc,
		)
	}
	return fmt.Sprintf(
		"恢复前发现重复大纲：第 %d 章重复第 %d 章；将先单独修复 V%d A%d 批次大纲，repair_arc 成功后删除该批次旧稿并重写已完成章节 %v。",
		duplicate.Chapter,
		duplicate.ExistingChapter,
		batch.Volume,
		batch.Arc,
		batch.CompletedChapters,
	)
}
