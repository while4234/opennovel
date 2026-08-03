package store

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// WorldStore 管理时间线、伏笔、人物关系、状态变化、世界规则、风格规则、审阅和交接。
type WorldStore struct {
	io                            *IO
	foundation                    *FoundationStore
	migration                     *structureMigration
	withFormalMutation            func(string, *structureMigration, func() error) error
	withFoundationGenerationGuard func(string, func() error) error
}

func NewWorldStore(io *IO, migrations ...*structureMigration) *WorldStore {
	var migration *structureMigration
	if len(migrations) > 0 {
		migration = migrations[0]
	}
	return &WorldStore{io: io, migration: migration}
}

// ── 时间线 ──

// SaveTimeline 全量写入 timeline.json + timeline.md（原子写入）。
func (s *WorldStore) SaveTimeline(events []domain.TimelineEvent) error {
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if migrated {
				canonical, err := canonicalTimeline(events, index)
				if err != nil {
					return err
				}
				return s.updateCanonicalFacts(index, func(facts *canonicalFacts) error { facts.Timeline = canonical; return nil })
			}
			return s.saveTimelineLegacy(events)
		})
	}
	return s.saveTimelineLegacy(events)
}

func (s *WorldStore) saveTimelineLegacy(events []domain.TimelineEvent) error {
	return s.io.WithWriteLock(func() error {
		if err := s.io.WriteJSONUnlocked("timeline.json", events); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("timeline.md", renderTimeline(events))
	})
}

// LoadTimeline 读取时间线。
func (s *WorldStore) LoadTimeline() ([]domain.TimelineEvent, error) {
	if s.migration != nil {
		var result []domain.TimelineEvent
		found := false
		err := s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return nil
			}
			found = true
			var facts canonicalFacts
			if err := s.io.ReadJSON(structureFactsFile, &facts); err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			projected, _, _, _, err := projectCanonicalFacts(facts, index)
			if err != nil {
				return err
			}
			result = projected
			return nil
		})
		if err != nil {
			return nil, err
		}
		if found {
			return result, nil
		}
	}
	var events []domain.TimelineEvent
	if err := s.io.ReadJSON("timeline.json", &events); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return events, nil
}

// AppendTimelineEvents 追加时间线事件。
func (s *WorldStore) AppendTimelineEvents(newEvents []domain.TimelineEvent) error {
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if migrated {
				canonical, err := canonicalTimeline(newEvents, index)
				if err != nil {
					return err
				}
				return s.updateCanonicalFacts(index, func(facts *canonicalFacts) error { facts.Timeline = append(facts.Timeline, canonical...); return nil })
			}
			return s.appendTimelineLegacy(newEvents)
		})
	}
	return s.appendTimelineLegacy(newEvents)
}

func (s *WorldStore) appendTimelineLegacy(newEvents []domain.TimelineEvent) error {
	return s.io.WithWriteLock(func() error {
		var existing []domain.TimelineEvent
		if err := s.io.ReadJSONUnlocked("timeline.json", &existing); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		}
		all := append(existing, newEvents...)
		if err := s.io.WriteJSONUnlocked("timeline.json", all); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("timeline.md", renderTimeline(all))
	})
}

// LoadRecentTimeline 返回最近 window 章内的时间线事件。
func (s *WorldStore) LoadRecentTimeline(current, window int) ([]domain.TimelineEvent, error) {
	all, err := s.LoadTimeline()
	if err != nil {
		return nil, err
	}
	minCh := max(current-window, 1)
	var filtered []domain.TimelineEvent
	for _, e := range all {
		if e.Chapter >= minCh {
			filtered = append(filtered, e)
		}
	}
	return filtered, nil
}

func (s *WorldStore) DeleteChapterFacts(chapters []int) error {
	chapterSet := positiveIntSet(chapters)
	if len(chapterSet) == 0 {
		return nil
	}
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return s.deleteChapterFactsLegacy(chapterSet)
			}
			ids := make(map[string]struct{}, len(chapterSet))
			for chapter := range chapterSet {
				id, ok := index.chapterID(chapter)
				if !ok {
					return fmt.Errorf("chapter %d is outside current structure", chapter)
				}
				ids[id] = struct{}{}
			}
			return s.updateCanonicalFacts(index, func(facts *canonicalFacts) error {
				timeline := facts.Timeline[:0]
				for _, item := range facts.Timeline {
					if _, remove := ids[item.ChapterID]; !remove {
						timeline = append(timeline, item)
					}
				}
				facts.Timeline = timeline
				foreshadow := facts.Foreshadow[:0]
				for _, item := range facts.Foreshadow {
					if _, remove := ids[item.PlantedChapterID]; remove {
						continue
					}
					if _, clear := ids[item.ResolvedChapterID]; clear {
						item.ResolvedChapterID = ""
						if item.Entry.Status == "resolved" {
							item.Entry.Status = "planted"
						}
					}
					foreshadow = append(foreshadow, item)
				}
				facts.Foreshadow = foreshadow
				relationships := facts.Relationships[:0]
				for _, item := range facts.Relationships {
					if _, remove := ids[item.ChapterID]; !remove {
						relationships = append(relationships, item)
					}
				}
				facts.Relationships = relationships
				changes := facts.StateChanges[:0]
				for _, item := range facts.StateChanges {
					if _, remove := ids[item.ChapterID]; !remove {
						changes = append(changes, item)
					}
				}
				facts.StateChanges = changes
				return nil
			})
		})
	}
	return s.deleteChapterFactsLegacy(chapterSet)
}

func (s *WorldStore) deleteChapterFactsLegacy(chapterSet map[int]struct{}) error {
	return s.io.WithWriteLock(func() error {
		if err := s.deleteTimelineFactsUnlocked(chapterSet); err != nil {
			return err
		}
		if err := s.deleteForeshadowFactsUnlocked(chapterSet); err != nil {
			return err
		}
		if err := s.deleteRelationshipFactsUnlocked(chapterSet); err != nil {
			return err
		}
		return s.deleteStateChangeFactsUnlocked(chapterSet)
	})
}

func (s *WorldStore) deleteTimelineFactsUnlocked(chapters map[int]struct{}) error {
	var events []domain.TimelineEvent
	if err := s.io.ReadJSONUnlocked("timeline.json", &events); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	filtered := events[:0]
	for _, event := range events {
		if _, remove := chapters[event.Chapter]; remove {
			continue
		}
		filtered = append(filtered, event)
	}
	if err := s.io.WriteJSONUnlocked("timeline.json", filtered); err != nil {
		return err
	}
	return s.io.WriteMarkdownUnlocked("timeline.md", renderTimeline(filtered))
}

func (s *WorldStore) deleteForeshadowFactsUnlocked(chapters map[int]struct{}) error {
	var entries []domain.ForeshadowEntry
	if err := s.io.ReadJSONUnlocked("foreshadow_ledger.json", &entries); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if _, remove := chapters[entry.PlantedAt]; remove {
			continue
		}
		if _, remove := chapters[entry.ResolvedAt]; remove {
			entry.ResolvedAt = 0
			if entry.Status == "resolved" {
				entry.Status = "planted"
			}
		}
		filtered = append(filtered, entry)
	}
	if err := s.io.WriteJSONUnlocked("foreshadow_ledger.json", filtered); err != nil {
		return err
	}
	return s.io.WriteMarkdownUnlocked("foreshadow_ledger.md", renderForeshadow(filtered))
}

func (s *WorldStore) deleteRelationshipFactsUnlocked(chapters map[int]struct{}) error {
	var entries []domain.RelationshipEntry
	if err := s.io.ReadJSONUnlocked("relationship_state.json", &entries); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if _, remove := chapters[entry.Chapter]; remove {
			continue
		}
		filtered = append(filtered, entry)
	}
	if err := s.io.WriteJSONUnlocked("relationship_state.json", filtered); err != nil {
		return err
	}
	return s.io.WriteMarkdownUnlocked("relationship_state.md", renderRelationships(filtered))
}

func (s *WorldStore) deleteStateChangeFactsUnlocked(chapters map[int]struct{}) error {
	var changes []domain.StateChange
	if err := s.io.ReadJSONUnlocked("meta/state_changes.json", &changes); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	filtered := changes[:0]
	for _, change := range changes {
		if _, remove := chapters[change.Chapter]; remove {
			continue
		}
		filtered = append(filtered, change)
	}
	return s.io.WriteJSONUnlocked("meta/state_changes.json", filtered)
}

// ── 伏笔 ──

// SaveForeshadowLedger 全量写入 foreshadow_ledger.json + foreshadow_ledger.md（原子写入）。
func (s *WorldStore) SaveForeshadowLedger(entries []domain.ForeshadowEntry) error {
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if migrated {
				canonical, err := canonicalForeshadow(entries, index)
				if err != nil {
					return err
				}
				return s.updateCanonicalFacts(index, func(facts *canonicalFacts) error { facts.Foreshadow = canonical; return nil })
			}
			return s.saveForeshadowLegacy(entries)
		})
	}
	return s.saveForeshadowLegacy(entries)
}

func (s *WorldStore) saveForeshadowLegacy(entries []domain.ForeshadowEntry) error {
	return s.io.WithWriteLock(func() error {
		if err := s.io.WriteJSONUnlocked("foreshadow_ledger.json", entries); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("foreshadow_ledger.md", renderForeshadow(entries))
	})
}

// LoadForeshadowLedger 读取伏笔账本。
func (s *WorldStore) LoadForeshadowLedger() ([]domain.ForeshadowEntry, error) {
	if s.migration != nil {
		var result []domain.ForeshadowEntry
		found := false
		err := s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return nil
			}
			found = true
			var facts canonicalFacts
			if err := s.io.ReadJSON(structureFactsFile, &facts); err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			_, projected, _, _, err := projectCanonicalFacts(facts, index)
			if err != nil {
				return err
			}
			result = projected
			return nil
		})
		if err != nil {
			return nil, err
		}
		if found {
			return result, nil
		}
	}
	var entries []domain.ForeshadowEntry
	if err := s.io.ReadJSON("foreshadow_ledger.json", &entries); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}

// UpdateForeshadow 批量应用伏笔增量操作。
func (s *WorldStore) UpdateForeshadow(chapter int, updates []domain.ForeshadowUpdate) error {
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return s.updateForeshadowLegacy(chapter, updates)
			}
			chapterID, mapped := index.chapterID(chapter)
			return s.updateCanonicalFacts(index, func(facts *canonicalFacts) error {
				positions := make(map[string]int, len(facts.Foreshadow))
				for i, item := range facts.Foreshadow {
					positions[item.Entry.ID] = i
				}
				for _, update := range updates {
					switch update.Action {
					case "plant":
						positions[update.ID] = len(facts.Foreshadow)
						plantedAt := 0
						if !mapped {
							plantedAt = chapter
						}
						facts.Foreshadow = append(facts.Foreshadow, canonicalForeshadowEntry{PlantedChapterID: chapterID, Entry: domain.ForeshadowEntry{ID: update.ID, Description: update.Description, PlantedAt: plantedAt, Status: "planted"}})
					case "advance":
						if i, ok := positions[update.ID]; ok {
							facts.Foreshadow[i].Entry.Status = "advanced"
						}
					case "resolve":
						if i, ok := positions[update.ID]; ok {
							facts.Foreshadow[i].Entry.Status = "resolved"
							facts.Foreshadow[i].ResolvedChapterID = chapterID
							if mapped {
								facts.Foreshadow[i].Entry.ResolvedAt = 0
							} else {
								facts.Foreshadow[i].Entry.ResolvedAt = chapter
							}
						}
					}
				}
				return nil
			})
		})
	}
	return s.updateForeshadowLegacy(chapter, updates)
}

func (s *WorldStore) updateForeshadowLegacy(chapter int, updates []domain.ForeshadowUpdate) error {
	return s.io.WithWriteLock(func() error {
		var entries []domain.ForeshadowEntry
		if err := s.io.ReadJSONUnlocked("foreshadow_ledger.json", &entries); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		}
		idx := make(map[string]int, len(entries))
		for i, e := range entries {
			idx[e.ID] = i
		}
		for _, u := range updates {
			switch u.Action {
			case "plant":
				idx[u.ID] = len(entries)
				entries = append(entries, domain.ForeshadowEntry{
					ID:          u.ID,
					Description: u.Description,
					PlantedAt:   chapter,
					Status:      "planted",
				})
			case "advance":
				if i, ok := idx[u.ID]; ok {
					entries[i].Status = "advanced"
				}
			case "resolve":
				if i, ok := idx[u.ID]; ok {
					entries[i].Status = "resolved"
					entries[i].ResolvedAt = chapter
				}
			}
		}
		if err := s.io.WriteJSONUnlocked("foreshadow_ledger.json", entries); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("foreshadow_ledger.md", renderForeshadow(entries))
	})
}

// LoadActiveForeshadow 返回未回收的伏笔条目。
func (s *WorldStore) LoadActiveForeshadow() ([]domain.ForeshadowEntry, error) {
	all, err := s.LoadForeshadowLedger()
	if err != nil {
		return nil, err
	}
	var active []domain.ForeshadowEntry
	for _, e := range all {
		if e.Status != "resolved" {
			active = append(active, e)
		}
	}
	return active, nil
}

// ── 人物关系 ──

// SaveRelationships 全量写入 relationship_state.json + relationship_state.md（原子写入）。
func (s *WorldStore) SaveRelationships(entries []domain.RelationshipEntry) error {
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if migrated {
				canonical, err := canonicalRelationships(entries, index)
				if err != nil {
					return err
				}
				return s.updateCanonicalFacts(index, func(facts *canonicalFacts) error { facts.Relationships = canonical; return nil })
			}
			return s.saveRelationshipsLegacy(entries)
		})
	}
	return s.saveRelationshipsLegacy(entries)
}

func (s *WorldStore) saveRelationshipsLegacy(entries []domain.RelationshipEntry) error {
	return s.io.WithWriteLock(func() error {
		if err := s.io.WriteJSONUnlocked("relationship_state.json", entries); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("relationship_state.md", renderRelationships(entries))
	})
}

// LoadRelationships 读取人物关系状态。
func (s *WorldStore) LoadRelationships() ([]domain.RelationshipEntry, error) {
	if s.migration != nil {
		var result []domain.RelationshipEntry
		found := false
		err := s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return nil
			}
			found = true
			var facts canonicalFacts
			if err := s.io.ReadJSON(structureFactsFile, &facts); err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			_, _, projected, _, err := projectCanonicalFacts(facts, index)
			if err != nil {
				return err
			}
			result = projected
			return nil
		})
		if err != nil {
			return nil, err
		}
		if found {
			return result, nil
		}
	}
	var entries []domain.RelationshipEntry
	if err := s.io.ReadJSON("relationship_state.json", &entries); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}

// UpdateRelationships 合并关系变化。
func (s *WorldStore) UpdateRelationships(changes []domain.RelationshipEntry) error {
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return s.updateRelationshipsLegacy(changes)
			}
			canonical, err := canonicalRelationships(changes, index)
			if err != nil {
				return err
			}
			return s.updateCanonicalFacts(index, func(facts *canonicalFacts) error {
				positions := make(map[string]int, len(facts.Relationships))
				for i, item := range facts.Relationships {
					positions[pairKey(item.Entry.CharacterA, item.Entry.CharacterB)] = i
				}
				for _, change := range canonical {
					key := pairKey(change.Entry.CharacterA, change.Entry.CharacterB)
					if i, ok := positions[key]; ok {
						facts.Relationships[i] = change
					} else {
						positions[key] = len(facts.Relationships)
						facts.Relationships = append(facts.Relationships, change)
					}
				}
				return nil
			})
		})
	}
	return s.updateRelationshipsLegacy(changes)
}

func (s *WorldStore) updateRelationshipsLegacy(changes []domain.RelationshipEntry) error {
	return s.io.WithWriteLock(func() error {
		var existing []domain.RelationshipEntry
		if err := s.io.ReadJSONUnlocked("relationship_state.json", &existing); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		}
		idx := make(map[string]int, len(existing))
		for i, e := range existing {
			idx[pairKey(e.CharacterA, e.CharacterB)] = i
		}
		for _, c := range changes {
			key := pairKey(c.CharacterA, c.CharacterB)
			if i, ok := idx[key]; ok {
				existing[i].Relation = c.Relation
				existing[i].Chapter = c.Chapter
			} else {
				idx[key] = len(existing)
				existing = append(existing, c)
			}
		}
		if err := s.io.WriteJSONUnlocked("relationship_state.json", existing); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("relationship_state.md", renderRelationships(existing))
	})
}

// ── 状态变化 ──

// AppendStateChanges 追加角色状态变化。
func (s *WorldStore) AppendStateChanges(changes []domain.StateChange) error {
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if migrated {
				canonical, err := canonicalStateChanges(changes, index)
				if err != nil {
					return err
				}
				return s.updateCanonicalFacts(index, func(facts *canonicalFacts) error {
					facts.StateChanges = append(facts.StateChanges, canonical...)
					return nil
				})
			}
			return s.appendStateChangesLegacy(changes)
		})
	}
	return s.appendStateChangesLegacy(changes)
}

func (s *WorldStore) appendStateChangesLegacy(changes []domain.StateChange) error {
	return s.io.WithWriteLock(func() error {
		var existing []domain.StateChange
		if err := s.io.ReadJSONUnlocked("meta/state_changes.json", &existing); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		}
		return s.io.WriteJSONUnlocked("meta/state_changes.json", append(existing, changes...))
	})
}

// LoadStateChanges 读取全部状态变化记录。
func (s *WorldStore) LoadStateChanges() ([]domain.StateChange, error) {
	if s.migration != nil {
		var result []domain.StateChange
		found := false
		err := s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return nil
			}
			found = true
			var facts canonicalFacts
			if err := s.io.ReadJSON(structureFactsFile, &facts); err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			_, _, _, projected, err := projectCanonicalFacts(facts, index)
			if err != nil {
				return err
			}
			result = projected
			return nil
		})
		if err != nil {
			return nil, err
		}
		if found {
			return result, nil
		}
	}
	var changes []domain.StateChange
	if err := s.io.ReadJSON("meta/state_changes.json", &changes); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return changes, nil
}

// ── 世界规则 ──

// SaveWorldRules 全量写入 world_rules.json + world_rules.md（原子写入）。
func (s *WorldStore) SaveWorldRules(rules []domain.WorldRule) error {
	if s.foundation != nil {
		if s.withFoundationGenerationGuard != nil {
			return s.withFoundationGenerationGuard("save foundation world rules", func() error { return s.foundation.updateWorldRules(rules) })
		}
		return s.foundation.updateWorldRules(rules)
	}
	return s.io.WithWriteLock(func() error {
		if err := s.io.WriteJSONUnlocked("world_rules.json", rules); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("world_rules.md", renderWorldRules(rules))
	})
}

// LoadWorldRules 读取世界规则。
func (s *WorldStore) LoadWorldRules() ([]domain.WorldRule, error) {
	if s.foundation != nil {
		foundation, err := s.foundation.Load()
		return foundation.WorldRules, err
	}
	var rules []domain.WorldRule
	if err := s.io.ReadJSON("world_rules.json", &rules); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return rules, nil
}

// ── 风格规则 ──

// SaveStyleRules 保存写作风格规则。
func (s *WorldStore) SaveStyleRules(rules domain.WritingStyleRules) error {
	return s.io.WriteJSON("meta/style_rules.json", rules)
}

// LoadStyleRules 读取写作风格规则。
func (s *WorldStore) LoadStyleRules() (*domain.WritingStyleRules, error) {
	var rules domain.WritingStyleRules
	if err := s.io.ReadJSON("meta/style_rules.json", &rules); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &rules, nil
}

// ── 审阅 ──

// SaveReview 保存审阅结果。
func (s *WorldStore) SaveReview(r domain.ReviewEntry) error {
	if s.withFormalMutation != nil {
		return s.withFormalMutation("save manuscript review", s.migration, func() error { return s.saveReviewOwned(r) })
	}
	return s.saveReviewOwned(r)
}

func (s *WorldStore) saveReviewOwned(r domain.ReviewEntry) error {
	rel := fmt.Sprintf("reviews/%02d.json", r.Chapter)
	switch r.Scope {
	case "global":
		rel = fmt.Sprintf("reviews/%02d-global.json", r.Chapter)
	case "arc_batch":
		rel = ArcBatchReviewRelPath(r.Volume, r.Arc, r.BatchFrom, r.BatchTo)
	}
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return s.io.WriteJSON(rel, r)
			}
			if r.Scope == "arc_batch" {
				canonical, err := canonicalizeArcBatchReview(r, index)
				if err != nil {
					return err
				}
				return writeJSONProjectionPair(s.io, arcBatchCanonicalRel(canonical), canonical, rel, r)
			}
			ref, ok := index.chapterRef(r.Chapter)
			if !ok {
				return s.io.WriteJSON(rel, r)
			}
			raw, err := json.Marshal(r)
			if err != nil {
				return err
			}
			canonicalData, err := canonicalizeChapterReview(raw, index, ref)
			if err != nil {
				return err
			}
			var canonical canonicalChapterReview
			if err := json.Unmarshal(canonicalData, &canonical); err != nil {
				return err
			}
			name := "review.json"
			if r.Scope == "global" {
				name = "review-global.json"
			}
			return writeJSONProjectionPair(s.io, chapterCanonicalRel(ref.ID, name), canonical, rel, r)
		})
	}
	return s.io.WriteJSON(rel, r)
}

// HasArcReview 检查指定章节（弧末章）是否已保存 scope=arc 的评审。
// 读失败按"未保存"处理，让 Router 倾向于重派而不是跳过。
func (s *WorldStore) HasArcReview(chapter int) bool {
	rv, err := s.LoadReview(chapter)
	return err == nil && rv != nil && rv.Scope == "arc"
}

// LoadReview 读取章节审阅结果。
func (s *WorldStore) LoadReview(chapter int) (*domain.ReviewEntry, error) {
	if s.migration != nil {
		var result *domain.ReviewEntry
		err := s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return nil
			}
			ref, ok := index.chapterRef(chapter)
			if !ok {
				return nil
			}
			data, err := s.io.ReadFile(chapterCanonicalRel(ref.ID, "review.json"))
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			projected, err := projectChapterReview(data, index, ref)
			if err != nil {
				return err
			}
			var review domain.ReviewEntry
			if err := json.Unmarshal(projected, &review); err != nil {
				return err
			}
			result = &review
			return nil
		})
		if err != nil || result != nil {
			return result, err
		}
	}
	var r domain.ReviewEntry
	if err := s.io.ReadJSON(fmt.Sprintf("reviews/%02d.json", chapter), &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// LoadGlobalReview reads the independently persisted whole-book review. It is
// intentionally separate from LoadReview so a chapter/arc review cannot be
// mistaken for the final completion receipt.
func (s *WorldStore) LoadGlobalReview(chapter int) (*domain.ReviewEntry, error) {
	if s.migration != nil {
		var result *domain.ReviewEntry
		err := s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return nil
			}
			ref, ok := index.chapterRef(chapter)
			if !ok {
				return nil
			}
			data, err := s.io.ReadFile(chapterCanonicalRel(ref.ID, "review-global.json"))
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			projected, err := projectChapterReview(data, index, ref)
			if err != nil {
				return err
			}
			var review domain.ReviewEntry
			if err := json.Unmarshal(projected, &review); err != nil {
				return err
			}
			result = &review
			return nil
		})
		if err != nil || result != nil {
			return result, err
		}
	}
	var review domain.ReviewEntry
	if err := s.io.ReadJSON(chapterReviewRel(chapter, true), &review); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &review, nil
}

func (s *WorldStore) LoadArcBatchReviews(volume, arc int) ([]domain.ReviewEntry, error) {
	if s.migration != nil {
		var result []domain.ReviewEntry
		handled := false
		err := s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return nil
			}
			handled = true
			arcRef, ok := index.arcRef(volume, arc)
			if !ok {
				return nil
			}
			entries, err := os.ReadDir(s.io.path(arcBatchCanonicalDir(arcRef.VolumeID, arcRef.ID)))
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
					continue
				}
				var canonical canonicalChapterReview
				rel := arcBatchCanonicalDir(arcRef.VolumeID, arcRef.ID) + "/" + entry.Name()
				if err := s.io.ReadJSON(rel, &canonical); err != nil {
					return err
				}
				review, err := projectArcBatchReview(canonical, index)
				if err != nil {
					return err
				}
				if review.Scope == "arc_batch" {
					result = append(result, review)
				}
			}
			sortReviewsByBatch(result)
			return nil
		})
		if err != nil {
			return nil, err
		}
		if handled {
			return result, nil
		}
	}
	dir := arcBatchReviewDir(volume, arc)
	entries, err := os.ReadDir(s.io.path(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	reviews := make([]domain.ReviewEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		var review domain.ReviewEntry
		if err := s.io.ReadJSON(dir+"/"+entry.Name(), &review); err != nil {
			return nil, err
		}
		if review.Scope != "arc_batch" {
			continue
		}
		reviews = append(reviews, review)
	}
	sortReviewsByBatch(reviews)
	return reviews, nil
}

func ArcBatchReviewRelPath(volume, arc, from, to int) string {
	return fmt.Sprintf("%s/%02d-%02d.json", arcBatchReviewDir(volume, arc), from, to)
}

func arcBatchReviewDir(volume, arc int) string {
	return fmt.Sprintf("reviews/arc_batches/v%02d/a%02d", volume, arc)
}

func sortReviewsByBatch(reviews []domain.ReviewEntry) {
	sort.SliceStable(reviews, func(i, j int) bool {
		if reviews[i].BatchFrom != reviews[j].BatchFrom {
			return reviews[i].BatchFrom < reviews[j].BatchFrom
		}
		return reviews[i].BatchTo < reviews[j].BatchTo
	})
}

func (s *WorldStore) loadCanonicalFactsUnlocked() (canonicalFacts, bool, error) {
	var facts canonicalFacts
	if err := s.io.ReadJSONUnlocked(structureFactsFile, &facts); err != nil {
		if os.IsNotExist(err) {
			return canonicalFacts{}, false, nil
		}
		return canonicalFacts{}, false, err
	}
	return facts, true, nil
}

func (s *WorldStore) updateCanonicalFacts(index structureIndex, update func(*canonicalFacts) error) error {
	return s.io.WithWriteLock(func() error {
		facts, _, err := s.loadCanonicalFactsUnlocked()
		if err != nil {
			return err
		}
		if err := update(&facts); err != nil {
			return err
		}
		return s.writeCanonicalFactsUnlocked(index, facts)
	})
}

func (s *WorldStore) writeCanonicalFactsUnlocked(index structureIndex, facts canonicalFacts) error {
	if err := s.io.WriteJSONUnlocked(structureFactsFile, facts); err != nil {
		return err
	}
	timeline, foreshadow, relationships, changes, err := projectCanonicalFacts(facts, index)
	if err != nil {
		return err
	}
	if err := s.io.WriteJSONUnlocked("timeline.json", timeline); err != nil {
		return err
	}
	if err := s.io.WriteMarkdownUnlocked("timeline.md", renderTimeline(timeline)); err != nil {
		return err
	}
	if err := s.io.WriteJSONUnlocked("foreshadow_ledger.json", foreshadow); err != nil {
		return err
	}
	if err := s.io.WriteMarkdownUnlocked("foreshadow_ledger.md", renderForeshadow(foreshadow)); err != nil {
		return err
	}
	if err := s.io.WriteJSONUnlocked("relationship_state.json", relationships); err != nil {
		return err
	}
	if err := s.io.WriteMarkdownUnlocked("relationship_state.md", renderRelationships(relationships)); err != nil {
		return err
	}
	return s.io.WriteJSONUnlocked("meta/state_changes.json", changes)
}

func canonicalTimeline(events []domain.TimelineEvent, index structureIndex) ([]canonicalTimelineEvent, error) {
	result := make([]canonicalTimelineEvent, 0, len(events))
	for _, event := range events {
		id, ok := index.chapterID(event.Chapter)
		if ok {
			event.Chapter = 0
		}
		result = append(result, canonicalTimelineEvent{ChapterID: id, Event: event})
	}
	return result, nil
}

func canonicalForeshadow(entries []domain.ForeshadowEntry, index structureIndex) ([]canonicalForeshadowEntry, error) {
	result := make([]canonicalForeshadowEntry, 0, len(entries))
	for _, entry := range entries {
		planted, plantedOK := index.chapterID(entry.PlantedAt)
		if plantedOK {
			entry.PlantedAt = 0
		}
		resolved, resolvedOK := index.chapterID(entry.ResolvedAt)
		if resolvedOK {
			entry.ResolvedAt = 0
		}
		result = append(result, canonicalForeshadowEntry{PlantedChapterID: planted, ResolvedChapterID: resolved, Entry: entry})
	}
	return result, nil
}

func canonicalRelationships(entries []domain.RelationshipEntry, index structureIndex) ([]canonicalRelationshipEntry, error) {
	result := make([]canonicalRelationshipEntry, 0, len(entries))
	for _, entry := range entries {
		id, ok := index.chapterID(entry.Chapter)
		if ok {
			entry.Chapter = 0
		}
		result = append(result, canonicalRelationshipEntry{ChapterID: id, Entry: entry})
	}
	return result, nil
}

func canonicalStateChanges(changes []domain.StateChange, index structureIndex) ([]canonicalStateChange, error) {
	result := make([]canonicalStateChange, 0, len(changes))
	for _, change := range changes {
		id, ok := index.chapterID(change.Chapter)
		if ok {
			change.Chapter = 0
		}
		result = append(result, canonicalStateChange{ChapterID: id, Change: change})
	}
	return result, nil
}

func (s *WorldStore) DeleteReview(chapter int) error {
	if chapter <= 0 {
		return nil
	}
	if s.withFormalMutation != nil {
		return s.withFormalMutation("delete manuscript review", s.migration, func() error {
			return s.deleteReviewOwned(chapter)
		})
	}
	return s.deleteReviewOwned(chapter)
}

// deleteReviewOwned is used by compound store transactions that already hold
// meta/revisions/transaction.lock.
func (s *WorldStore) deleteReviewOwned(chapter int) error {
	deleteReview := func(id string) error {
		return s.io.WithWriteLock(func() error {
			if err := s.io.RemoveFileUnlocked(fmt.Sprintf("reviews/%02d.json", chapter)); err != nil {
				return err
			}
			if err := s.io.RemoveFileUnlocked(fmt.Sprintf("reviews/%02d-global.json", chapter)); err != nil {
				return err
			}
			if id != "" {
				if err := s.io.RemoveFileUnlocked(chapterCanonicalRel(id, "review.json")); err != nil {
					return err
				}
				return s.io.RemoveFileUnlocked(chapterCanonicalRel(id, "review-global.json"))
			}
			return nil
		})
	}
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return deleteReview("")
			}
			id, _ := index.chapterID(chapter)
			return deleteReview(id)
		})
	}
	return deleteReview("")
}

// LoadLastReview 读取最近一次全局审阅。
func (s *WorldStore) LoadLastReview(fromChapter int) (*domain.ReviewEntry, error) {
	for ch := fromChapter; ch >= 1; ch-- {
		var r domain.ReviewEntry
		if err := s.io.ReadJSON(fmt.Sprintf("reviews/%02d-global.json", ch), &r); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		return &r, nil
	}
	return nil, nil
}

// ── render helpers ──

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

func positiveIntSet(values []int) map[int]struct{} {
	out := make(map[int]struct{}, len(values))
	for _, value := range values {
		if value > 0 {
			out[value] = struct{}{}
		}
	}
	return out
}

func renderTimeline(events []domain.TimelineEvent) string {
	var b strings.Builder
	b.WriteString("# 时间线\n\n")
	for _, e := range events {
		chars := ""
		if len(e.Characters) > 0 {
			chars = "（" + strings.Join(e.Characters, "、") + "）"
		}
		fmt.Fprintf(&b, "- **第 %d 章 [%s]**：%s%s\n", e.Chapter, e.Time, e.Event, chars)
	}
	return b.String()
}

func renderForeshadow(entries []domain.ForeshadowEntry) string {
	var b strings.Builder
	b.WriteString("# 伏笔账本\n\n")
	for _, e := range entries {
		status := e.Status
		if e.ResolvedAt > 0 {
			status = fmt.Sprintf("已回收（第 %d 章）", e.ResolvedAt)
		}
		fmt.Fprintf(&b, "- **[%s]** %s — 埋设于第 %d 章，状态：%s\n",
			e.ID, e.Description, e.PlantedAt, status)
	}
	return b.String()
}

func renderRelationships(entries []domain.RelationshipEntry) string {
	var b strings.Builder
	b.WriteString("# 人物关系\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- **%s ↔ %s**：%s（第 %d 章）\n",
			e.CharacterA, e.CharacterB, e.Relation, e.Chapter)
	}
	return b.String()
}

func renderWorldRules(rules []domain.WorldRule) string {
	grouped := make(map[string][]domain.WorldRule)
	var order []string
	for _, r := range rules {
		cat := r.Category
		if cat == "" {
			cat = "other"
		}
		if _, exists := grouped[cat]; !exists {
			order = append(order, cat)
		}
		grouped[cat] = append(grouped[cat], r)
	}

	var b strings.Builder
	b.WriteString("# 世界观规则\n\n")
	for _, cat := range order {
		fmt.Fprintf(&b, "## %s\n\n", cat)
		for _, r := range grouped[cat] {
			fmt.Fprintf(&b, "- **规则**：%s\n", r.Rule)
			if r.Boundary != "" {
				fmt.Fprintf(&b, "  - 边界：%s\n", r.Boundary)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
