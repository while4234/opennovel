package store

import (
	"fmt"
	"os"
	"reflect"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	outlineRepairFinalizationFile                 = "meta/outline_repair_finalization.json"
	outlineRepairFinalizationVersion              = 1
	outlineRepairFinalizationStagePrepared        = "prepared"
	outlineRepairFinalizationStageOutlineReplaced = "outline_replaced"
)

type outlineRepairFinalizationMarker struct {
	Version     int                   `json:"version"`
	Stage       string                `json:"stage"`
	Volume      int                   `json:"volume"`
	Arc         int                   `json:"arc"`
	FromChapter int                   `json:"from_chapter,omitempty"`
	ToChapter   int                   `json:"to_chapter,omitempty"`
	Repaired    []domain.OutlineEntry `json:"repaired"`
	CreatedAt   string                `json:"created_at"`
	UpdatedAt   string                `json:"updated_at"`
}

func (s *Store) completePendingOutlineRepairFinalization(progress *domain.Progress) (*domain.Progress, error) {
	marker, err := s.loadOutlineRepairFinalization()
	if err != nil || marker == nil {
		return progress, err
	}

	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	marker, err = s.loadOutlineRepairFinalization()
	if err != nil || marker == nil {
		return progress, err
	}
	if err := validateOutlineRepairFinalizationMarker(marker); err != nil {
		return progress, err
	}

	repaired := append([]domain.OutlineEntry(nil), marker.Repaired...)
	if len(repaired) == 0 {
		repaired, err = s.loadNumberedArcEntries(marker.Volume, marker.Arc)
		if err != nil {
			return progress, err
		}
	}

	switch marker.Stage {
	case outlineRepairFinalizationStagePrepared:
		current, err := s.loadNumberedArcEntriesRange(marker.Volume, marker.Arc, marker.FromChapter, marker.ToChapter)
		if err != nil {
			return progress, err
		}
		if !reflect.DeepEqual(current, repaired) {
			if err := s.clearOutlineRepairFinalization(); err != nil {
				return progress, err
			}
			return progress, nil
		}
	case outlineRepairFinalizationStageOutlineReplaced:
	default:
		return progress, fmt.Errorf("unknown outline repair finalization stage %q", marker.Stage)
	}

	return s.finalizeOutlineRepair(marker.Volume, marker.Arc, repaired)
}

func (s *Store) saveOutlineRepairFinalization(volumeIdx, arcIdx int, repaired []domain.OutlineEntry, stage string) error {
	return s.saveOutlineRepairFinalizationRange(volumeIdx, arcIdx, 0, 0, repaired, stage)
}

func (s *Store) saveOutlineRepairFinalizationRange(volumeIdx, arcIdx, fromChapter, toChapter int, repaired []domain.OutlineEntry, stage string) error {
	now := timeNowUTCString()
	marker, err := s.loadOutlineRepairFinalization()
	if err != nil {
		return err
	}
	createdAt := now
	if marker != nil && marker.Volume == volumeIdx && marker.Arc == arcIdx &&
		marker.FromChapter == fromChapter && marker.ToChapter == toChapter &&
		marker.CreatedAt != "" {
		createdAt = marker.CreatedAt
	}
	return s.Outline.io.WithWriteLock(func() error {
		return s.Outline.io.WriteJSONUnlocked(outlineRepairFinalizationFile, outlineRepairFinalizationMarker{
			Version:     outlineRepairFinalizationVersion,
			Stage:       stage,
			Volume:      volumeIdx,
			Arc:         arcIdx,
			FromChapter: fromChapter,
			ToChapter:   toChapter,
			Repaired:    append([]domain.OutlineEntry(nil), repaired...),
			CreatedAt:   createdAt,
			UpdatedAt:   now,
		})
	})
}

func (s *Store) loadOutlineRepairFinalization() (*outlineRepairFinalizationMarker, error) {
	var marker outlineRepairFinalizationMarker
	if err := s.Outline.io.ReadJSON(outlineRepairFinalizationFile, &marker); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &marker, nil
}

func (s *Store) clearOutlineRepairFinalization() error {
	return s.Outline.io.RemoveFile(outlineRepairFinalizationFile)
}

func validateOutlineRepairFinalizationMarker(marker *outlineRepairFinalizationMarker) error {
	if marker == nil {
		return nil
	}
	if marker.Version != outlineRepairFinalizationVersion {
		return fmt.Errorf("unsupported outline repair finalization version %d", marker.Version)
	}
	if marker.Volume <= 0 || marker.Arc <= 0 {
		return fmt.Errorf("invalid outline repair finalization target V%d A%d", marker.Volume, marker.Arc)
	}
	return nil
}

func (s *Store) loadNumberedArcEntries(volumeIdx, arcIdx int) ([]domain.OutlineEntry, error) {
	return s.loadNumberedArcEntriesRange(volumeIdx, arcIdx, 0, 0)
}

func (s *Store) loadNumberedArcEntriesRange(volumeIdx, arcIdx, fromChapter, toChapter int) ([]domain.OutlineEntry, error) {
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	globalChapter := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			arcLen := arcOutlineChapterCount(arc)
			if volume.Index == volumeIdx && arc.Index == arcIdx {
				if len(arc.Chapters) == 0 {
					return nil, fmt.Errorf("arc V%d A%d is not expanded", volumeIdx, arcIdx)
				}
				numbered := numberedOutlineEntries(arc.Chapters, globalChapter)
				if fromChapter <= 0 && toChapter <= 0 {
					return numbered, nil
				}
				startChapter, expectedChapters, err := repairArcRangeBounds(volumeIdx, arcIdx, outlineArcLocation{
					found:        true,
					startChapter: globalChapter,
					chapterCount: len(arc.Chapters),
				}, fromChapter, toChapter)
				if err != nil {
					return nil, err
				}
				offset := startChapter - globalChapter
				return append([]domain.OutlineEntry(nil), numbered[offset:offset+expectedChapters]...), nil
			}
			globalChapter += arcLen
		}
	}
	return nil, fmt.Errorf("arc not found: volume=%d arc=%d", volumeIdx, arcIdx)
}
