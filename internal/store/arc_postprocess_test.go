package store

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestArcReviewBatchRangesKeepNormalChaptersInSeparateRequests(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	for chapter := 1; chapter <= 4; chapter++ {
		if err := st.Drafts.SaveFinalChapter(chapter, strings.Repeat("正文", 1500)); err != nil {
			t.Fatalf("SaveFinalChapter(%d): %v", chapter, err)
		}
	}

	ranges, err := st.ArcReviewBatchRanges(1, 4, domain.ArcReviewBatchRuneBudget)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 4 {
		t.Fatalf("ranges = %+v, want one complete chapter per batch", ranges)
	}
	for index, got := range ranges {
		chapter := index + 1
		if got.From != chapter || got.To != chapter {
			t.Fatalf("ranges[%d] = %+v, want chapter %d only", index, got, chapter)
		}
	}
}
