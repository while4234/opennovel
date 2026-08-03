package store

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SummaryStore 管理章节、弧、卷摘要。
type SummaryStore struct {
	io                 *IO
	outline            *OutlineStore // 只读依赖，用于获取弧/卷数量
	migration          *structureMigration
	withFormalMutation func(string, *structureMigration, func() error) error
}

func NewSummaryStore(io *IO, outline *OutlineStore, migrations ...*structureMigration) *SummaryStore {
	var migration *structureMigration
	if len(migrations) > 0 {
		migration = migrations[0]
	}
	return &SummaryStore{io: io, outline: outline, migration: migration}
}

// SaveSummary 保存章节摘要到 summaries/{ch}.json。
func (s *SummaryStore) SaveSummary(sum domain.ChapterSummary) error {
	if s.withFormalMutation != nil {
		return s.withFormalMutation("save chapter summary", s.migration, func() error { return s.saveSummaryOwned(sum) })
	}
	return s.saveSummaryOwned(sum)
}

func (s *SummaryStore) saveSummaryOwned(sum domain.ChapterSummary) error {
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return s.io.WriteJSON(chapterSummaryRel(sum.Chapter), sum)
			}
			ref, ok := index.chapterRef(sum.Chapter)
			if !ok {
				return s.io.WriteJSON(chapterSummaryRel(sum.Chapter), sum)
			}
			canonical := sum
			canonical.Chapter = 0
			return writeJSONProjectionPair(s.io,
				chapterCanonicalRel(ref.ID, "summary.json"), canonicalChapterSummary{ChapterID: ref.ID, Summary: canonical},
				chapterSummaryRel(sum.Chapter), sum,
			)
		})
	}
	return s.io.WriteJSON(fmt.Sprintf("summaries/%02d.json", sum.Chapter), sum)
}

// LoadSummary 读取指定章节的摘要。
func (s *SummaryStore) LoadSummary(chapter int) (*domain.ChapterSummary, error) {
	if s.migration != nil {
		var result *domain.ChapterSummary
		err := s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return nil
			}
			ref, ok := index.chapterRef(chapter)
			if !ok {
				return nil
			}
			var canonical canonicalChapterSummary
			if err := s.io.ReadJSON(chapterCanonicalRel(ref.ID, "summary.json"), &canonical); err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if canonical.ChapterID != ref.ID {
				return fmt.Errorf("chapter summary identity mismatch for chapter %d", chapter)
			}
			canonical.Summary.Chapter = chapter
			result = &canonical.Summary
			return nil
		})
		if err != nil || result != nil {
			return result, err
		}
	}
	var sum domain.ChapterSummary
	if err := s.io.ReadJSON(fmt.Sprintf("summaries/%02d.json", chapter), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

func (s *SummaryStore) DeleteChapterSummary(chapter int) error {
	if chapter <= 0 {
		return nil
	}
	if s.withFormalMutation != nil {
		return s.withFormalMutation("delete chapter summary", s.migration, func() error {
			return s.deleteChapterSummaryOwned(chapter)
		})
	}
	return s.deleteChapterSummaryOwned(chapter)
}

func (s *SummaryStore) deleteChapterSummaryOwned(chapter int) error {
	return s.removeScopedSummaryOwned(chapterSummaryRel(chapter), func(index structureIndex) (string, bool) {
		ref, ok := index.chapterRef(chapter)
		if !ok {
			return "", false
		}
		return chapterCanonicalRel(ref.ID, "summary.json"), true
	})
}

// LoadRecentSummaries 加载 current 章之前最近 count 章的摘要。
func (s *SummaryStore) LoadRecentSummaries(current, count int) ([]domain.ChapterSummary, error) {
	var result []domain.ChapterSummary
	start := max(current-count, 1)
	for ch := start; ch < current; ch++ {
		sum, err := s.LoadSummary(ch)
		if err != nil {
			return nil, err
		}
		if sum != nil {
			result = append(result, *sum)
		}
	}
	return result, nil
}

// SaveArcSummary 保存弧级摘要。
func (s *SummaryStore) SaveArcSummary(sum domain.ArcSummary) error {
	if s.withFormalMutation != nil {
		return s.withFormalMutation("save arc summary", s.migration, func() error { return s.saveArcSummaryOwned(sum) })
	}
	return s.saveArcSummaryOwned(sum)
}

func (s *SummaryStore) saveArcSummaryOwned(sum domain.ArcSummary) error {
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return s.io.WriteJSON(arcSummaryRel(sum.Volume, sum.Arc), sum)
			}
			ref, ok := index.arcRef(sum.Volume, sum.Arc)
			if !ok {
				return s.io.WriteJSON(arcSummaryRel(sum.Volume, sum.Arc), sum)
			}
			canonical := sum
			canonical.Volume, canonical.Arc = 0, 0
			return writeJSONProjectionPair(s.io, arcCanonicalRel(ref.ID, "summary.json"), canonicalArcSummary{ArcID: ref.ID, Summary: canonical}, arcSummaryRel(sum.Volume, sum.Arc), sum)
		})
	}
	return s.io.WriteJSON(fmt.Sprintf("summaries/arc-v%02da%02d.json", sum.Volume, sum.Arc), sum)
}

// HasArcSummary 检查指定弧是否已保存摘要。读失败按"未保存"处理。
func (s *SummaryStore) HasArcSummary(volume, arc int) bool {
	sum, err := s.LoadArcSummary(volume, arc)
	return err == nil && sum != nil
}

// HasVolumeSummary 检查指定卷是否已保存摘要。读失败按"未保存"处理。
func (s *SummaryStore) HasVolumeSummary(volume int) bool {
	sum, err := s.LoadVolumeSummary(volume)
	return err == nil && sum != nil
}

// LoadArcSummary 读取指定弧的摘要。
func (s *SummaryStore) LoadArcSummary(volume, arc int) (*domain.ArcSummary, error) {
	if s.migration != nil {
		var result *domain.ArcSummary
		var indexed bool
		err := s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			indexed = migrated
			if !migrated {
				return nil
			}
			ref, ok := index.arcRef(volume, arc)
			if !ok {
				return nil
			}
			var canonical canonicalArcSummary
			if err := s.io.ReadJSON(arcCanonicalRel(ref.ID, "summary.json"), &canonical); err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if canonical.ArcID != ref.ID {
				return fmt.Errorf("arc summary identity mismatch for V%d A%d", volume, arc)
			}
			canonical.Summary.Volume, canonical.Summary.Arc = volume, arc
			result = &canonical.Summary
			return nil
		})
		if err != nil || result != nil || indexed {
			return result, err
		}
	}
	var sum domain.ArcSummary
	if err := s.io.ReadJSON(fmt.Sprintf("summaries/arc-v%02da%02d.json", volume, arc), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

func (s *SummaryStore) DeleteArcSummary(volume, arc int) error {
	if volume <= 0 || arc <= 0 {
		return nil
	}
	if s.withFormalMutation != nil {
		return s.withFormalMutation("delete arc summary", s.migration, func() error {
			return s.deleteArcSummaryOwned(volume, arc)
		})
	}
	return s.deleteArcSummaryOwned(volume, arc)
}

func (s *SummaryStore) deleteArcSummaryOwned(volume, arc int) error {
	return s.removeScopedSummaryOwned(arcSummaryRel(volume, arc), func(index structureIndex) (string, bool) {
		ref, ok := index.arcRef(volume, arc)
		if !ok {
			return "", false
		}
		return arcCanonicalRel(ref.ID, "summary.json"), true
	})
}

// LoadArcSummaries 加载一卷内所有已有弧摘要。
func (s *SummaryStore) LoadArcSummaries(volume int) ([]domain.ArcSummary, error) {
	maxArc := s.arcCountForVolume(volume)
	var result []domain.ArcSummary
	for arc := 1; arc <= maxArc; arc++ {
		sum, err := s.LoadArcSummary(volume, arc)
		if err != nil {
			return nil, err
		}
		if sum != nil {
			result = append(result, *sum)
		}
	}
	return result, nil
}

// SaveVolumeSummary 保存卷级摘要。
func (s *SummaryStore) SaveVolumeSummary(sum domain.VolumeSummary) error {
	if s.withFormalMutation != nil {
		return s.withFormalMutation("save volume summary", s.migration, func() error { return s.saveVolumeSummaryOwned(sum) })
	}
	return s.saveVolumeSummaryOwned(sum)
}

func (s *SummaryStore) saveVolumeSummaryOwned(sum domain.VolumeSummary) error {
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return s.io.WriteJSON(volumeSummaryRel(sum.Volume), sum)
			}
			ref, ok := index.volumeRef(sum.Volume)
			if !ok {
				return s.io.WriteJSON(volumeSummaryRel(sum.Volume), sum)
			}
			canonical := sum
			canonical.Volume = 0
			return writeJSONProjectionPair(s.io, volumeCanonicalRel(ref.ID, "summary.json"), canonicalVolumeSummary{VolumeID: ref.ID, Summary: canonical}, volumeSummaryRel(sum.Volume), sum)
		})
	}
	return s.io.WriteJSON(fmt.Sprintf("summaries/vol-v%02d.json", sum.Volume), sum)
}

// LoadVolumeSummary 读取指定卷的摘要。
func (s *SummaryStore) LoadVolumeSummary(volume int) (*domain.VolumeSummary, error) {
	if s.migration != nil {
		var result *domain.VolumeSummary
		var indexed bool
		err := s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			indexed = migrated
			if !migrated {
				return nil
			}
			ref, ok := index.volumeRef(volume)
			if !ok {
				return nil
			}
			var canonical canonicalVolumeSummary
			if err := s.io.ReadJSON(volumeCanonicalRel(ref.ID, "summary.json"), &canonical); err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if canonical.VolumeID != ref.ID {
				return fmt.Errorf("volume summary identity mismatch for volume %d", volume)
			}
			canonical.Summary.Volume = volume
			result = &canonical.Summary
			return nil
		})
		if err != nil || result != nil || indexed {
			return result, err
		}
	}
	var sum domain.VolumeSummary
	if err := s.io.ReadJSON(fmt.Sprintf("summaries/vol-v%02d.json", volume), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

func (s *SummaryStore) DeleteVolumeSummary(volume int) error {
	if volume <= 0 {
		return nil
	}
	if s.withFormalMutation != nil {
		return s.withFormalMutation("delete volume summary", s.migration, func() error {
			return s.deleteVolumeSummaryOwned(volume)
		})
	}
	return s.deleteVolumeSummaryOwned(volume)
}

func (s *SummaryStore) deleteVolumeSummaryOwned(volume int) error {
	return s.removeScopedSummaryOwned(volumeSummaryRel(volume), func(index structureIndex) (string, bool) {
		ref, ok := index.volumeRef(volume)
		if !ok {
			return "", false
		}
		return volumeCanonicalRel(ref.ID, "summary.json"), true
	})
}

func (s *SummaryStore) removeScopedSummaryOwned(legacyRel string, canonicalRel func(structureIndex) (string, bool)) error {
	if s.migration == nil {
		return s.io.RemoveFile(legacyRel)
	}
	return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
		return s.io.WithWriteLock(func() error {
			if migrated {
				if rel, ok := canonicalRel(index); ok {
					if err := s.io.RemoveFileUnlocked(rel); err != nil {
						return err
					}
				}
			}
			return s.io.RemoveFileUnlocked(legacyRel)
		})
	})
}

func writeJSONProjectionPair(io *IO, canonicalRel string, canonical any, legacyRel string, legacy any) error {
	canonicalData, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return err
	}
	legacyData, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		return err
	}
	return io.WithWriteLock(func() error {
		if err := io.WriteFileUnlocked(canonicalRel, canonicalData); err != nil {
			return err
		}
		return io.WriteFileUnlocked(legacyRel, legacyData)
	})
}

// LoadAllVolumeSummaries 加载所有已有卷摘要。
func (s *SummaryStore) LoadAllVolumeSummaries() ([]domain.VolumeSummary, error) {
	maxVol := s.volumeCount()
	var result []domain.VolumeSummary
	for vol := 1; vol <= maxVol; vol++ {
		sum, err := s.LoadVolumeSummary(vol)
		if err != nil {
			return nil, err
		}
		if sum != nil {
			result = append(result, *sum)
		}
	}
	return result, nil
}

// FindCharacterAppearances 批量查找多个角色的最后出场章节号。
func (s *SummaryStore) FindCharacterAppearances(names []string, endChapter, recentWindow int) map[string]int {
	result := make(map[string]int, len(names))
	remaining := make(map[string]struct{}, len(names))
	for _, n := range names {
		remaining[n] = struct{}{}
	}
	for ch := endChapter - recentWindow; ch >= 1; ch-- {
		if len(remaining) == 0 {
			break
		}
		sum, err := s.LoadSummary(ch)
		if err != nil || sum == nil {
			continue
		}
		for _, c := range sum.Characters {
			if _, need := remaining[c]; need {
				result[c] = ch
				delete(remaining, c)
			}
		}
	}
	return result
}

func (s *SummaryStore) volumeCount() int {
	volumes, err := s.outline.LoadLayeredOutline()
	if err == nil && len(volumes) > 0 {
		return len(volumes)
	}
	return 20
}

func (s *SummaryStore) arcCountForVolume(volume int) int {
	volumes, err := s.outline.LoadLayeredOutline()
	if err == nil {
		for _, v := range volumes {
			if v.Index == volume {
				return len(v.Arcs)
			}
		}
	}
	return 20
}
