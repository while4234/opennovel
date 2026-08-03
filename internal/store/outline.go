package store

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// OutlineStore 管理故事前提、大纲（扁平/分层）和指南针。
type OutlineStore struct {
	io                        *IO
	foundation                *FoundationStore
	identity                  structureIdentity
	migration                 *structureMigration
	withLegacyMutation        func(string, *structureMigration, func() error) error
	withFormalGate            func(string, func() error) error
	guardFoundationGeneration func(string, func() error) error
}

func NewOutlineStore(io *IO, identity structureIdentity, migrations ...*structureMigration) *OutlineStore {
	var migration *structureMigration
	if len(migrations) > 0 {
		migration = migrations[0]
	}
	return &OutlineStore{io: io, identity: identity, migration: migration}
}

// SavePremise 保存故事前提到 premise.md。
func (s *OutlineStore) SavePremise(content string) error {
	if s.foundation != nil {
		return s.withFoundationGenerationGuard("save premise", func() error { return s.foundation.updatePremise(content) })
	}
	return s.io.WriteMarkdown("premise.md", content)
}

// LoadPremise 读取 premise.md。不存在时返回空字符串。
func (s *OutlineStore) LoadPremise() (string, error) {
	if s.foundation != nil {
		foundation, err := s.foundation.Load()
		return foundation.Premise, err
	}
	data, err := s.io.ReadFile("premise.md")
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

// SaveOutline 同时保存 outline.json 和 outline.md（原子写入）。
func (s *OutlineStore) SaveOutline(entries []domain.OutlineEntry) error {
	return s.withLegacyFormalMutation("save flat outline", func() error {
		return s.withAuthoritativeFormalGate("save flat outline", func() error { return s.saveOutline(entries) })
	})
}

func (s *OutlineStore) saveOutline(entries []domain.OutlineEntry) error {
	if s.migration != nil {
		return s.saveOutlineWithMigration(entries)
	}
	return s.io.WithWriteLock(func() error {
		var existing []domain.OutlineEntry
		if err := s.io.ReadJSONUnlocked("outline.json", &existing); err != nil && !os.IsNotExist(err) {
			return err
		}
		var layered []domain.VolumeOutline
		if err := s.io.ReadJSONUnlocked("layered_outline.json", &layered); err != nil && !os.IsNotExist(err) {
			return err
		}
		prepared, err := s.identity.prepareOutlineForSave(entries, existing, layered)
		if err != nil {
			return err
		}
		if err := s.io.WriteJSONUnlocked("outline.json", prepared); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("outline.md", renderOutline(prepared))
	})
}

func (s *OutlineStore) saveOutlineWithMigration(entries []domain.OutlineEntry) error {
	var existing []domain.OutlineEntry
	if err := s.io.ReadJSON("outline.json", &existing); err != nil && !os.IsNotExist(err) {
		return err
	}
	var layered []domain.VolumeOutline
	if err := s.io.ReadJSON("layered_outline.json", &layered); err != nil && !os.IsNotExist(err) {
		return err
	}
	prepared, err := s.identity.prepareOutlineForSave(entries, existing, layered)
	if err != nil {
		return err
	}
	source := s.identity.sourceIndex(existing, layered)
	target, syncedLayered, err := s.identity.targetIndexForOutline(prepared, layered)
	if err != nil {
		return err
	}
	payloads, err := outlineMigrationPayloads(prepared, syncedLayered)
	if err != nil {
		return err
	}
	return s.migration.save("outline", source, target, payloads)
}

// LoadOutline 从 outline.json 读取结构化大纲。
func (s *OutlineStore) LoadOutline() ([]domain.OutlineEntry, error) {
	var result []domain.OutlineEntry
	err := s.withStructureRead(func() error {
		s.io.mu.RLock()
		defer s.io.mu.RUnlock()
		var entries []domain.OutlineEntry
		if err := s.io.ReadJSONUnlocked("outline.json", &entries); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		var layered []domain.VolumeOutline
		_ = s.io.ReadJSONUnlocked("layered_outline.json", &layered)
		hydrated := s.identity.hydrateOutline(entries)
		layeredIDs := make(map[int]string)
		for _, entry := range domain.FlattenOutline(s.identity.hydrateLayeredOutline(layered)) {
			layeredIDs[entry.Chapter] = entry.ID
		}
		for i := range hydrated {
			if strings.TrimSpace(entries[i].ID) == "" && layeredIDs[entries[i].Chapter] != "" {
				hydrated[i].ID = layeredIDs[entries[i].Chapter]
			}
		}
		if outlineChapterNumbersUseOrderProjection(entries) {
			result = domain.ProjectOutlineOrder(hydrated)
		} else {
			result = hydrated
		}
		return nil
	})
	return result, err
}

// GetChapterOutline 获取指定章节的大纲条目。
func (s *OutlineStore) GetChapterOutline(chapter int) (*domain.OutlineEntry, error) {
	entries, err := s.LoadOutline()
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].Chapter == chapter {
			return &entries[i], nil
		}
	}
	return nil, fmt.Errorf("chapter %d not found in outline", chapter)
}

// SaveLayeredOutline 保存分层大纲（长篇模式，原子写入）。
func (s *OutlineStore) SaveLayeredOutline(volumes []domain.VolumeOutline) error {
	return s.withLegacyFormalMutation("save layered outline", func() error {
		return s.withAuthoritativeFormalGate("save layered outline", func() error { return s.saveLayeredOutline(volumes) })
	})
}

func (s *OutlineStore) saveLayeredOutline(volumes []domain.VolumeOutline) error {
	if s.migration != nil {
		return s.saveLayeredOutlineWithMigration(volumes)
	}
	return s.io.WithWriteLock(func() error {
		var existing []domain.VolumeOutline
		if err := s.io.ReadJSONUnlocked("layered_outline.json", &existing); err != nil && !os.IsNotExist(err) {
			return err
		}
		var flat []domain.OutlineEntry
		if err := s.io.ReadJSONUnlocked("outline.json", &flat); err != nil && !os.IsNotExist(err) {
			return err
		}
		prepared, err := s.identity.prepareLayeredOutlineForSave(volumes, existing, flat)
		if err != nil {
			return err
		}
		if err := s.io.WriteJSONUnlocked("layered_outline.json", prepared); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("layered_outline.md", renderLayeredOutline(prepared))
	})
}

func (s *OutlineStore) saveLayeredOutlineWithMigration(volumes []domain.VolumeOutline) error {
	var existing []domain.VolumeOutline
	if err := s.io.ReadJSON("layered_outline.json", &existing); err != nil && !os.IsNotExist(err) {
		return err
	}
	var flat []domain.OutlineEntry
	if err := s.io.ReadJSON("outline.json", &flat); err != nil && !os.IsNotExist(err) {
		return err
	}
	prepared, err := s.identity.prepareLayeredOutlineForSave(volumes, existing, flat)
	if err != nil {
		return err
	}
	source := s.identity.sourceIndex(flat, existing)
	target := structureIndexFromLayered(prepared)
	payloads, err := layeredOutlineMigrationPayloads(prepared)
	if err != nil {
		return err
	}
	return s.migration.save("layered_outline", source, target, payloads)
}

// LoadLayeredOutline 读取分层大纲。
func (s *OutlineStore) LoadLayeredOutline() ([]domain.VolumeOutline, error) {
	var result []domain.VolumeOutline
	err := s.withStructureRead(func() error {
		s.io.mu.RLock()
		defer s.io.mu.RUnlock()
		var volumes []domain.VolumeOutline
		if err := s.io.ReadJSONUnlocked("layered_outline.json", &volumes); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		var flat []domain.OutlineEntry
		_ = s.io.ReadJSONUnlocked("outline.json", &flat)
		hydrated := s.identity.hydrateLayeredOutline(volumes)
		flatIDs := make(map[int]string)
		for _, entry := range s.identity.hydrateOutline(flat) {
			flatIDs[entry.Chapter] = entry.ID
		}
		for volumeIndex := range hydrated {
			for arcIndex := range hydrated[volumeIndex].Arcs {
				for chapterIndex := range hydrated[volumeIndex].Arcs[arcIndex].Chapters {
					raw := volumes[volumeIndex].Arcs[arcIndex].Chapters[chapterIndex]
					if strings.TrimSpace(raw.ID) == "" && flatIDs[raw.Chapter] != "" {
						hydrated[volumeIndex].Arcs[arcIndex].Chapters[chapterIndex].ID = flatIDs[raw.Chapter]
					}
				}
			}
		}
		result = domain.ProjectLayeredOutlineOrder(hydrated)
		return nil
	})
	return result, err
}

func (s *OutlineStore) withStructureRead(fn func() error) error {
	if s.migration == nil {
		return fn()
	}
	return s.migration.withRead(fn)
}

func (s *OutlineStore) withLegacyFormalMutation(operation string, mutation func() error) error {
	if s == nil {
		return fmt.Errorf("outline store is required before %s", operation)
	}
	if s.withLegacyMutation == nil {
		return mutation()
	}
	return s.withLegacyMutation(operation, s.migration, mutation)
}

func (s *OutlineStore) withAuthoritativeFormalGate(operation string, mutation func() error) error {
	if s.withFormalGate == nil {
		return mutation()
	}
	return s.withFormalGate(operation, mutation)
}

func (s *OutlineStore) withFoundationGenerationGuard(operation string, mutation func() error) error {
	if s.guardFoundationGeneration == nil {
		return mutation()
	}
	return s.guardFoundationGeneration(operation, mutation)
}

func outlineMigrationPayloads(entries []domain.OutlineEntry, layered []domain.VolumeOutline) ([]migrationPayload, error) {
	payloads := make([]migrationPayload, 0, 4)
	outlineJSON, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return nil, err
	}
	payloads = append(payloads,
		migrationPayload{Rel: "outline.json", Data: outlineJSON},
		migrationPayload{Rel: "outline.md", Data: []byte(renderOutline(entries))},
	)
	if len(layered) == 0 {
		return payloads, nil
	}
	layeredJSON, err := json.MarshalIndent(layered, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(payloads,
		migrationPayload{Rel: "layered_outline.json", Data: layeredJSON},
		migrationPayload{Rel: "layered_outline.md", Data: []byte(renderLayeredOutline(layered))},
	), nil
}

func layeredOutlineMigrationPayloads(volumes []domain.VolumeOutline) ([]migrationPayload, error) {
	flat := domain.FlattenOutline(volumes)
	layeredJSON, err := json.MarshalIndent(volumes, "", "  ")
	if err != nil {
		return nil, err
	}
	flatJSON, err := json.MarshalIndent(flat, "", "  ")
	if err != nil {
		return nil, err
	}
	return []migrationPayload{
		{Rel: "layered_outline.json", Data: layeredJSON},
		{Rel: "layered_outline.md", Data: []byte(renderLayeredOutline(volumes))},
		{Rel: "outline.json", Data: flatJSON},
		{Rel: "outline.md", Data: []byte(renderOutline(flat))},
	}, nil
}

// ClearLayeredOutline 清理分层大纲文件。
func (s *OutlineStore) ClearLayeredOutline() error {
	return s.withLegacyFormalMutation("clear layered outline", func() error {
		return s.withAuthoritativeFormalGate("clear layered outline", s.clearLayeredOutline)
	})
}

func (s *OutlineStore) clearLayeredOutline() error {
	return s.io.WithWriteLock(func() error {
		if err := s.io.RemoveFileUnlocked("layered_outline.json"); err != nil {
			return err
		}
		return s.io.RemoveFileUnlocked("layered_outline.md")
	})
}

// GetChapterFromLayered 从分层大纲中按全局章节号查找。
func (s *OutlineStore) GetChapterFromLayered(chapter int) (*domain.OutlineEntry, error) {
	volumes, err := s.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	ch := 1
	for _, v := range volumes {
		for _, a := range v.Arcs {
			for i := range a.Chapters {
				if ch == chapter {
					e := a.Chapters[i]
					e.Chapter = ch
					return &e, nil
				}
				ch++
			}
		}
	}
	return nil, fmt.Errorf("chapter %d not found in layered outline", chapter)
}

// LocateChapter 根据全局章节号定位所在的卷和弧。
func (s *OutlineStore) LocateChapter(chapter int) (volume, arc int, err error) {
	volumes, err := s.LoadLayeredOutline()
	if err != nil {
		return 0, 0, err
	}
	ch := 1
	for _, v := range volumes {
		for _, a := range v.Arcs {
			for range a.Chapters {
				if ch == chapter {
					return v.Index, a.Index, nil
				}
				ch++
			}
		}
	}
	return 0, 0, fmt.Errorf("chapter %d not found in layered outline", chapter)
}

// ArcBoundary 弧边界信息。
type ArcBoundary struct {
	IsArcEnd       bool
	IsVolumeEnd    bool
	Volume         int
	Arc            int
	FirstChapter   int
	LastChapter    int
	ChapterCount   int
	NextVolume     int
	NextArc        int
	NeedsExpansion bool
	NeedsNewVolume bool // 卷末且当前 layered_outline 没有下一卷
}

// HasNextArc 是否还有后续弧。
func (b *ArcBoundary) HasNextArc() bool {
	return b.NextVolume > 0 || b.NextArc > 0
}

// CheckArcBoundary 检查某章是否为弧/卷的最后一章。
func (s *OutlineStore) CheckArcBoundary(chapter int) (*ArcBoundary, error) {
	volumes, err := s.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return nil, err
	}

	type arcPos struct {
		volIdx, arcIdx int
		volume, arc    int
		chInArc        int
		arcLen         int
	}

	ch := 1
	var cur *arcPos
	for vi, v := range volumes {
		for ai, a := range v.Arcs {
			for ci := range a.Chapters {
				if ch == chapter {
					cur = &arcPos{
						volIdx:  vi,
						arcIdx:  ai,
						volume:  v.Index,
						arc:     a.Index,
						chInArc: ci,
						arcLen:  len(a.Chapters),
					}
				}
				ch++
			}
		}
	}
	if cur == nil {
		return nil, nil
	}

	b := &ArcBoundary{
		Volume:       cur.volume,
		Arc:          cur.arc,
		FirstChapter: chapter - cur.chInArc,
		ChapterCount: cur.arcLen,
	}

	isLastChInArc := cur.chInArc == cur.arcLen-1
	isLastArcInVol := cur.arcIdx == len(volumes[cur.volIdx].Arcs)-1

	// Next*/NeedsExpansion/NeedsNewVolume 只在弧末才有意义，否则会让协调者误以为要提前展开下一弧。
	if !isLastChInArc {
		return b, nil
	}

	b.IsArcEnd = true
	b.LastChapter = chapter
	if isLastArcInVol {
		b.IsVolumeEnd = true
	}

	found := false
	for vi := cur.volIdx; vi < len(volumes); vi++ {
		startArc := 0
		if vi == cur.volIdx {
			startArc = cur.arcIdx + 1
		}
		for ai := startArc; ai < len(volumes[vi].Arcs); ai++ {
			b.NextVolume = volumes[vi].Index
			b.NextArc = volumes[vi].Arcs[ai].Index
			b.NeedsExpansion = !volumes[vi].Arcs[ai].IsExpanded()
			found = true
			break
		}
		if found {
			break
		}
	}

	if b.IsVolumeEnd && !found {
		b.NeedsNewVolume = true
	}

	return b, nil
}

// expandArcUnlocked 内部方法，在 Store.ExpandArc 跨域协调中调用。
func (s *OutlineStore) expandArcUnlocked(volumeIdx, arcIdx int, chapters []domain.OutlineEntry) ([]domain.VolumeOutline, error) {
	var volumes []domain.VolumeOutline
	if err := s.io.ReadJSONUnlocked("layered_outline.json", &volumes); err != nil {
		return nil, fmt.Errorf("load layered_outline: %w", err)
	}
	return s.expandArc(volumes, volumeIdx, arcIdx, chapters)
}

func (s *OutlineStore) expandArc(volumes []domain.VolumeOutline, volumeIdx, arcIdx int, chapters []domain.OutlineEntry) ([]domain.VolumeOutline, error) {
	volumes = s.identity.hydrateLayeredOutline(volumes)
	location := locateOutlineArc(volumes, volumeIdx, arcIdx)
	if !location.found {
		return nil, fmt.Errorf("arc not found: volume=%d, arc=%d", volumeIdx, arcIdx)
	}
	expanded := numberedOutlineEntries(chapters, location.startChapter)
	if err := assignMissingOutlineIDs(expanded); err != nil {
		return nil, err
	}
	if location.estimatedChapters > 0 && len(expanded) != location.estimatedChapters {
		return nil, fmt.Errorf(
			"expand_arc V%d A%d must contain exactly %d chapters from the approved volume plan, got %d",
			volumeIdx, arcIdx, location.estimatedChapters, len(expanded),
		)
	}
	if err := validateOutlineBatchEntries(fmt.Sprintf("expand_arc V%d A%d", volumeIdx, arcIdx), expanded); err != nil {
		return nil, err
	}

	found := false
	for vi := range volumes {
		if volumes[vi].Index != volumeIdx {
			continue
		}
		for ai := range volumes[vi].Arcs {
			if volumes[vi].Arcs[ai].Index != arcIdx {
				continue
			}
			volumes[vi].Arcs[ai].Chapters = expanded
			// Preserve the approved batch size for audit-directed repairs.
			found = true
			break
		}
		if found {
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("arc not found: volume=%d, arc=%d", volumeIdx, arcIdx)
	}
	if duplicate, ok := domain.FindDuplicateOutlineEntries(domain.FlattenOutline(volumes)); ok {
		return nil, fmt.Errorf("expanded outline repeats an existing chapter promise: %w", duplicate)
	}
	return domain.ProjectLayeredOutlineOrder(volumes), nil
}

// replaceArcChaptersUnlocked 内部方法，在 Store.RepairArcOutline 跨域协调中调用。
func (s *OutlineStore) replaceArcChaptersUnlocked(volumeIdx, arcIdx int, chapters []domain.OutlineEntry) ([]domain.VolumeOutline, []domain.OutlineEntry, error) {
	return s.replaceArcChapterRangeUnlocked(volumeIdx, arcIdx, 0, 0, chapters)
}

// replaceArcChapterRangeUnlocked replaces either the whole arc or a
// global-chapter window inside it. The merged arc is validated as a whole so a
// shortened repair cannot hide a duplicate against untouched chapters.
func (s *OutlineStore) replaceArcChapterRangeUnlocked(volumeIdx, arcIdx, fromChapter, toChapter int, chapters []domain.OutlineEntry) ([]domain.VolumeOutline, []domain.OutlineEntry, error) {
	var volumes []domain.VolumeOutline
	if err := s.io.ReadJSONUnlocked("layered_outline.json", &volumes); err != nil {
		return nil, nil, fmt.Errorf("load layered_outline: %w", err)
	}
	volumes = s.identity.hydrateLayeredOutline(volumes)

	location := locateOutlineArc(volumes, volumeIdx, arcIdx)
	if !location.found {
		return nil, nil, fmt.Errorf("arc not found: volume=%d arc=%d", volumeIdx, arcIdx)
	}
	if location.chapterCount == 0 {
		return nil, nil, fmt.Errorf("arc V%d A%d is not expanded", volumeIdx, arcIdx)
	}
	startChapter, expectedChapters, err := repairArcRangeBounds(volumeIdx, arcIdx, location, fromChapter, toChapter)
	if err != nil {
		return nil, nil, err
	}
	if len(chapters) != expectedChapters {
		if fromChapter > 0 || toChapter > 0 {
			return nil, nil, fmt.Errorf("repair_arc V%d A%d chapters %d-%d must keep %d chapters, got %d", volumeIdx, arcIdx, fromChapter, toChapter, expectedChapters, len(chapters))
		}
		return nil, nil, fmt.Errorf("repair_arc V%d A%d must keep %d chapters, got %d", volumeIdx, arcIdx, expectedChapters, len(chapters))
	}
	repaired := numberedOutlineEntries(chapters, startChapter)

	found := false
	for vi := range volumes {
		for ai := range volumes[vi].Arcs {
			arc := &volumes[vi].Arcs[ai]
			if volumes[vi].Index != volumeIdx || arc.Index != arcIdx {
				continue
			}
			existingOffset := 0
			if fromChapter > 0 || toChapter > 0 {
				existingOffset = fromChapter - location.startChapter
			}
			for i := range repaired {
				if strings.TrimSpace(repaired[i].ID) == "" {
					repaired[i].ID = arc.Chapters[existingOffset+i].ID
				}
			}
			merged := numberedOutlineEntries(arc.Chapters, location.startChapter)
			if fromChapter > 0 || toChapter > 0 {
				offset := fromChapter - location.startChapter
				copy(merged[offset:offset+len(repaired)], repaired)
			} else {
				merged = repaired
			}
			if err := validateOutlineBatchEntries(fmt.Sprintf("repair_arc V%d A%d", volumeIdx, arcIdx), merged); err != nil {
				return nil, nil, err
			}
			arc.Chapters = merged
			found = true
			break
		}
		if found {
			break
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("arc not found: volume=%d arc=%d", volumeIdx, arcIdx)
	}
	volumes = domain.ProjectLayeredOutlineOrder(volumes)
	if err := s.io.WriteJSONUnlocked("layered_outline.json", volumes); err != nil {
		return nil, nil, err
	}
	if err := s.io.WriteMarkdownUnlocked("layered_outline.md", renderLayeredOutline(volumes)); err != nil {
		return nil, nil, err
	}
	flat := domain.FlattenOutline(volumes)
	if err := s.io.WriteJSONUnlocked("outline.json", flat); err != nil {
		return nil, nil, err
	}
	if err := s.io.WriteMarkdownUnlocked("outline.md", renderOutline(flat)); err != nil {
		return nil, nil, err
	}
	return volumes, repaired, nil
}

func repairArcRangeBounds(volumeIdx, arcIdx int, location outlineArcLocation, fromChapter, toChapter int) (int, int, error) {
	if fromChapter <= 0 && toChapter <= 0 {
		return location.startChapter, location.chapterCount, nil
	}
	if fromChapter <= 0 || toChapter <= 0 {
		return 0, 0, fmt.Errorf("repair_arc V%d A%d requires both from_chapter and to_chapter for partial repair", volumeIdx, arcIdx)
	}
	arcEnd := location.startChapter + location.chapterCount - 1
	if fromChapter < location.startChapter || toChapter > arcEnd || toChapter < fromChapter {
		return 0, 0, fmt.Errorf("repair_arc V%d A%d chapter range %d-%d outside arc range %d-%d", volumeIdx, arcIdx, fromChapter, toChapter, location.startChapter, arcEnd)
	}
	return fromChapter, toChapter - fromChapter + 1, nil
}

func (s *OutlineStore) appendVolumeUnlocked(vol domain.VolumeOutline) ([]domain.VolumeOutline, error) {
	var volumes []domain.VolumeOutline
	if err := s.io.ReadJSONUnlocked("layered_outline.json", &volumes); err != nil {
		return nil, fmt.Errorf("load layered_outline: %w", err)
	}
	return s.appendVolume(volumes, vol)
}

func (s *OutlineStore) appendVolume(volumes []domain.VolumeOutline, vol domain.VolumeOutline) ([]domain.VolumeOutline, error) {
	volumes = s.identity.hydrateLayeredOutline(volumes)
	if err := validateAppendVolume(volumes, vol); err != nil {
		return nil, err
	}
	numberedVolume, _ := numberedAppendedVolume(volumes, vol)
	preparedVolume := []domain.VolumeOutline{numberedVolume}
	if err := assignMissingLayeredIDs(preparedVolume); err != nil {
		return nil, err
	}
	numberedVolume = preparedVolume[0]
	if err := validateOutlineVolumeBatches(fmt.Sprintf("append_volume V%d", vol.Index), numberedVolume); err != nil {
		return nil, err
	}
	volumes = append(volumes, numberedVolume)
	return domain.ProjectLayeredOutlineOrder(volumes), nil
}

func (s *OutlineStore) appendSkeletonVolumeUnlocked(vol domain.VolumeOutline) ([]domain.VolumeOutline, error) {
	var volumes []domain.VolumeOutline
	if err := s.io.ReadJSONUnlocked("layered_outline.json", &volumes); err != nil {
		return nil, fmt.Errorf("load layered_outline: %w", err)
	}
	return s.appendSkeletonVolume(volumes, vol)
}

func (s *OutlineStore) appendSkeletonVolume(volumes []domain.VolumeOutline, vol domain.VolumeOutline) ([]domain.VolumeOutline, error) {
	volumes = s.identity.hydrateLayeredOutline(volumes)
	if err := validateAppendSkeletonVolume(volumes, vol); err != nil {
		return nil, err
	}
	newVolume := cloneVolumeOutline(vol)
	preparedVolume := []domain.VolumeOutline{newVolume}
	if err := assignMissingLayeredIDs(preparedVolume); err != nil {
		return nil, err
	}
	newVolume = preparedVolume[0]
	volumes = append(volumes, newVolume)
	return domain.ProjectLayeredOutlineOrder(volumes), nil
}

type outlineArcLocation struct {
	found             bool
	startChapter      int
	chapterCount      int
	estimatedChapters int
}

func locateOutlineArc(volumes []domain.VolumeOutline, volumeIdx, arcIdx int) outlineArcLocation {
	nextChapter := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			chapterCount := arcOutlineChapterCount(arc)
			if volume.Index == volumeIdx && arc.Index == arcIdx {
				return outlineArcLocation{
					found:             true,
					startChapter:      nextChapter,
					chapterCount:      len(arc.Chapters),
					estimatedChapters: arc.EstimatedChapters,
				}
			}
			nextChapter += chapterCount
		}
	}
	return outlineArcLocation{}
}

func arcOutlineChapterCount(arc domain.ArcOutline) int {
	if len(arc.Chapters) > 0 {
		return len(arc.Chapters)
	}
	return arc.EstimatedChapters
}

func numberedOutlineEntries(entries []domain.OutlineEntry, startChapter int) []domain.OutlineEntry {
	out := make([]domain.OutlineEntry, len(entries))
	for i := range entries {
		out[i] = cloneOutlineEntry(entries[i])
		out[i].Chapter = startChapter + i
	}
	return out
}

func numberedAppendedVolume(existing []domain.VolumeOutline, volume domain.VolumeOutline) (domain.VolumeOutline, []domain.OutlineEntry) {
	nextChapter := domain.TotalChapters(existing) + 1
	numbered := cloneVolumeOutline(volume)
	var entries []domain.OutlineEntry
	for arcIdx := range numbered.Arcs {
		if len(numbered.Arcs[arcIdx].Chapters) == 0 {
			continue
		}
		chapterEntries := numberedOutlineEntries(numbered.Arcs[arcIdx].Chapters, nextChapter)
		numbered.Arcs[arcIdx].Chapters = chapterEntries
		entries = append(entries, chapterEntries...)
		nextChapter += len(chapterEntries)
	}
	return numbered, entries
}

func validateOutlineBatchEntries(context string, entries []domain.OutlineEntry) error {
	if duplicate, ok := domain.FindDuplicateOutlineEntries(entries); ok {
		return fmt.Errorf("%s contains duplicate chapter outline: %w", context, duplicate)
	}
	return nil
}

func validateOutlineVolumeBatches(context string, volume domain.VolumeOutline) error {
	for _, arc := range volume.Arcs {
		if len(arc.Chapters) == 0 {
			continue
		}
		if err := validateOutlineBatchEntries(fmt.Sprintf("%s A%d", context, arc.Index), arc.Chapters); err != nil {
			return err
		}
	}
	return nil
}

func cloneVolumeOutline(volume domain.VolumeOutline) domain.VolumeOutline {
	cloned := volume
	cloned.Arcs = make([]domain.ArcOutline, len(volume.Arcs))
	for i := range volume.Arcs {
		cloned.Arcs[i] = volume.Arcs[i]
		cloned.Arcs[i].Chapters = cloneOutlineEntries(volume.Arcs[i].Chapters)
	}
	return cloned
}

func cloneOutlineEntries(entries []domain.OutlineEntry) []domain.OutlineEntry {
	out := make([]domain.OutlineEntry, len(entries))
	for i := range entries {
		out[i] = cloneOutlineEntry(entries[i])
	}
	return out
}

func cloneOutlineEntry(entry domain.OutlineEntry) domain.OutlineEntry {
	entry.Scenes = append([]string(nil), entry.Scenes...)
	return entry
}

func validateAppendVolume(existing []domain.VolumeOutline, vol domain.VolumeOutline) error {
	if err := validateAppendSkeletonVolume(existing, vol); err != nil {
		return err
	}
	if !vol.Arcs[0].IsExpanded() {
		return fmt.Errorf("新卷的首弧必须包含详细章节")
	}
	return nil
}

func validateAppendSkeletonVolume(existing []domain.VolumeOutline, vol domain.VolumeOutline) error {
	if len(existing) > 0 {
		maxIdx := existing[len(existing)-1].Index
		if vol.Index != maxIdx+1 {
			return fmt.Errorf("卷 Index %d 必须紧接现有卷 %d", vol.Index, maxIdx)
		}
	} else if vol.Index != 1 {
		return fmt.Errorf("首卷 Index 必须为 1")
	}
	if len(vol.Arcs) == 0 {
		return fmt.Errorf("新卷必须至少包含一个弧")
	}
	return nil
}

// SaveCompass 保存终局方向指南针。
func (s *OutlineStore) SaveCompass(compass domain.StoryCompass) error {
	if compass.EndingDirection == "" {
		return fmt.Errorf("ending_direction 不能为空")
	}
	return s.withLegacyFormalMutation("save story compass", func() error {
		return s.withAuthoritativeFormalGate("save story compass", func() error {
			return s.io.WriteJSON("meta/compass.json", compass)
		})
	})
}

// LoadCompass 读取终局方向指南针。
func (s *OutlineStore) LoadCompass() (*domain.StoryCompass, error) {
	var c domain.StoryCompass
	if err := s.io.ReadJSON("meta/compass.json", &c); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

func renderLayeredOutline(volumes []domain.VolumeOutline) string {
	var b strings.Builder
	b.WriteString("# 分层大纲\n\n")
	ch := 1
	for _, v := range volumes {
		fmt.Fprintf(&b, "## 第 %d 卷：%s\n\n", v.Index, v.Title)
		fmt.Fprintf(&b, "**主题**：%s\n\n", v.Theme)
		for _, a := range v.Arcs {
			fmt.Fprintf(&b, "### 第 %d 弧：%s\n\n", a.Index, a.Title)
			fmt.Fprintf(&b, "**目标**：%s\n\n", a.Goal)
			if !a.IsExpanded() {
				fmt.Fprintf(&b, "*（待展开，预估 %d 章）*\n\n", a.EstimatedChapters)
				continue
			}
			for _, e := range a.Chapters {
				fmt.Fprintf(&b, "#### 第 %d 章：%s\n\n", ch, e.Title)
				fmt.Fprintf(&b, "**核心事件**：%s\n\n", e.CoreEvent)
				if e.Hook != "" {
					fmt.Fprintf(&b, "**钩子**：%s\n\n", e.Hook)
				}
				ch++
			}
		}
	}
	return b.String()
}

func renderOutline(entries []domain.OutlineEntry) string {
	var b strings.Builder
	b.WriteString("# 大纲\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "## 第 %d 章：%s\n\n", e.Chapter, e.Title)
		fmt.Fprintf(&b, "**核心事件**：%s\n\n", e.CoreEvent)
		if e.Hook != "" {
			fmt.Fprintf(&b, "**钩子**：%s\n\n", e.Hook)
		}
		if len(e.Scenes) > 0 {
			b.WriteString("**场景**：\n")
			for i, sc := range e.Scenes {
				fmt.Fprintf(&b, "%d. %s\n", i+1, sc)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}
