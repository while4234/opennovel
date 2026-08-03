package host

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestHasIncompleteCreativeProgress(t *testing.T) {
	tests := []struct {
		name     string
		progress *domain.Progress
		want     bool
	}{
		{name: "nil", progress: nil, want: false},
		{name: "complete phase", progress: &domain.Progress{Phase: domain.PhaseComplete, TotalChapters: 10, CompletedChapters: []int{1}}, want: false},
		{name: "unfinished chapter", progress: &domain.Progress{Phase: domain.PhaseWriting, TotalChapters: 10, CompletedChapters: []int{1}, InProgressChapter: 2}, want: true},
		{name: "incomplete book", progress: &domain.Progress{Phase: domain.PhaseWriting, TotalChapters: 10, CompletedChapters: []int{1, 2}}, want: true},
		{name: "all chapters complete", progress: &domain.Progress{Phase: domain.PhaseWriting, TotalChapters: 2, CompletedChapters: []int{1, 2}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasIncompleteCreativeProgress(tt.progress); got != tt.want {
				t.Fatalf("hasIncompleteCreativeProgress() = %v, want %v", got, tt.want)
			}
		})
	}
}
