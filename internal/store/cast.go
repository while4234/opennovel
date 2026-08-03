package store

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// CastStore 管理配角名册（meta/cast_ledger.json）。
//
// 配角名册记录"出现过的有名字的次要角色"，与 characters.json（核心角色档案）正交：
//   - characters.json：Architect 显式设计的主角 + 关键配角，写作期不修改
//   - cast_ledger.json：commit_chapter 工具自动累加，所有有名字的非核心配角
//
// MergeAppearances 是幂等的：同一章重复 commit 不会重复累加 AppearanceCount。
type CastStore struct{ io *IO }

func NewCastStore(io *IO) *CastStore { return &CastStore{io: io} }

const castLedgerPath = "meta/cast_ledger.json"

const CastPromotionAppearanceThreshold = 3

// Load 读取配角名册。文件不存在时返回空切片。
func (s *CastStore) Load() ([]domain.CastEntry, error) {
	var entries []domain.CastEntry
	if err := s.io.ReadJSON(castLedgerPath, &entries); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}

// Save 整体保存配角名册（原子写入）。
func (s *CastStore) Save(entries []domain.CastEntry) error {
	return s.io.WriteJSON(castLedgerPath, entries)
}

func (s *CastStore) DeleteChapterAppearances(chapters []int) error {
	chapterSet := positiveIntSet(chapters)
	if len(chapterSet) == 0 {
		return nil
	}
	return s.io.WithWriteLock(func() error {
		var entries []domain.CastEntry
		if err := s.io.ReadJSONUnlocked(castLedgerPath, &entries); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		filtered := entries[:0]
		for _, entry := range entries {
			appearances := entry.AppearanceChapters[:0]
			for _, chapter := range entry.AppearanceChapters {
				if _, remove := chapterSet[chapter]; remove {
					continue
				}
				appearances = append(appearances, chapter)
			}
			if len(appearances) == 0 {
				continue
			}
			slices.Sort(appearances)
			entry.AppearanceChapters = append([]int(nil), appearances...)
			entry.AppearanceCount = len(entry.AppearanceChapters)
			entry.FirstSeenChapter = entry.AppearanceChapters[0]
			entry.LastSeenChapter = entry.AppearanceChapters[len(entry.AppearanceChapters)-1]
			if entry.PromotionStatus == "pending" &&
				entry.PromotionReason == "repeated_appearance" &&
				entry.AppearanceCount < CastPromotionAppearanceThreshold {
				entry.PromotionStatus = ""
				entry.PromotionReason = ""
			}
			filtered = append(filtered, entry)
		}
		return s.io.WriteJSONUnlocked(castLedgerPath, filtered)
	})
}

// MergeAppearances 把本章出场记录合并进名册。
//
// 参数:
//   - chapter: 本章号
//   - characters: 本章出场名字数组（来自 commit_chapter.Characters）
//   - intros: Writer 显式声明的新角色简介（首次出场或补全 BriefRole）
//   - knownCore: characters.json 中已有的核心角色名集合（这些跳过 ledger 写入）
//
// 行为:
//   - 名字在 knownCore 中：跳过（核心角色档案是其唯一记录入口）
//   - 名字已在 ledger 且 chapter 已在 AppearanceChapters：完全跳过（幂等）
//   - 名字已在 ledger 但 chapter 是新的：更新 LastSeenChapter + 追加 chapter + count++
//   - 名字未在 ledger：新增条目
//   - intros 中的 BriefRole 仅在 ledger 条目 BriefRole 仍为空时采用，避免覆盖更早的简介
func (s *CastStore) MergeAppearances(
	chapter int,
	characters []string,
	intros []domain.CastIntro,
	knownCore map[string]bool,
) error {
	if chapter <= 0 || len(characters) == 0 {
		return nil
	}
	return s.io.WithWriteLock(func() error {
		var entries []domain.CastEntry
		if err := s.io.ReadJSONUnlocked(castLedgerPath, &entries); err != nil && !os.IsNotExist(err) {
			return err
		}

		introMap := make(map[string]string, len(intros))
		for _, in := range intros {
			if in.Name != "" {
				introMap[in.Name] = in.BriefRole
			}
		}

		index := make(map[string]int, len(entries))
		for i, e := range entries {
			index[e.Name] = i
			for _, alias := range e.Aliases {
				index[alias] = i
			}
		}

		seen := make(map[string]bool, len(characters))
		for _, name := range characters {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			if knownCore[name] {
				continue
			}
			if i, ok := index[name]; ok {
				entry := &entries[i]
				if !slices.Contains(entry.AppearanceChapters, chapter) {
					entry.AppearanceChapters = append(entry.AppearanceChapters, chapter)
					entry.AppearanceCount = len(entry.AppearanceChapters)
					if chapter > entry.LastSeenChapter {
						entry.LastSeenChapter = chapter
					}
					if chapter < entry.FirstSeenChapter || entry.FirstSeenChapter == 0 {
						entry.FirstSeenChapter = chapter
					}
				}
				if entry.BriefRole == "" {
					if br, ok := introMap[name]; ok && br != "" {
						entry.BriefRole = br
					}
				}
				if !entry.Promoted && entry.PromotionStatus == "" &&
					entry.AppearanceCount >= CastPromotionAppearanceThreshold {
					entry.PromotionStatus = "pending"
					entry.PromotionReason = "repeated_appearance"
				}
				continue
			}
			entries = append(entries, domain.CastEntry{
				Name:               name,
				BriefRole:          introMap[name],
				FirstSeenChapter:   chapter,
				LastSeenChapter:    chapter,
				AppearanceCount:    1,
				AppearanceChapters: []int{chapter},
			})
		}
		return s.io.WriteJSONUnlocked(castLedgerPath, entries)
	})
}

// RequestPromotion marks a ledger character for the shared Character Agent.
// Exact retries are idempotent and never create a second ledger entry.
func (s *CastStore) RequestPromotion(name, reason string) error {
	name = strings.TrimSpace(name)
	reason = strings.TrimSpace(reason)
	if name == "" {
		return fmt.Errorf("cast promotion name is required")
	}
	if reason == "" {
		reason = "user_requested"
	}
	return s.io.WithWriteLock(func() error {
		var entries []domain.CastEntry
		if err := s.io.ReadJSONUnlocked(castLedgerPath, &entries); err != nil {
			return err
		}
		for index := range entries {
			if entries[index].Name != name && !slices.Contains(entries[index].Aliases, name) {
				continue
			}
			if entries[index].Promoted {
				return nil
			}
			entries[index].PromotionStatus = "pending"
			entries[index].PromotionReason = reason
			return s.io.WriteJSONUnlocked(castLedgerPath, entries)
		}
		return fmt.Errorf("cast entry %q not found", name)
	})
}

// PendingPromotions returns deterministic work for the same Character Agent.
func (s *CastStore) PendingPromotions() ([]domain.CastEntry, error) {
	entries, err := s.Load()
	if err != nil {
		return nil, err
	}
	pending := make([]domain.CastEntry, 0)
	for _, entry := range entries {
		if !entry.Promoted && entry.PromotionStatus == "pending" {
			pending = append(pending, entry)
		}
	}
	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].FirstSeenChapter != pending[j].FirstSeenChapter {
			return pending[i].FirstSeenChapter < pending[j].FirstSeenChapter
		}
		return pending[i].Name < pending[j].Name
	})
	return pending, nil
}

// MarkPromoted links the ledger entry to the explicitly confirmed canonical
// Character ID. It never publishes or edits the character card itself.
func (s *CastStore) MarkPromoted(name, characterID string) error {
	name = strings.TrimSpace(name)
	characterID = strings.TrimSpace(characterID)
	if name == "" || characterID == "" {
		return fmt.Errorf("cast promotion name and character ID are required")
	}
	return s.io.WithWriteLock(func() error {
		var entries []domain.CastEntry
		if err := s.io.ReadJSONUnlocked(castLedgerPath, &entries); err != nil {
			return err
		}
		for index := range entries {
			entry := &entries[index]
			if entry.Name != name && !slices.Contains(entry.Aliases, name) {
				continue
			}
			if entry.Promoted {
				if entry.TargetCharacterID == characterID {
					return nil
				}
				return fmt.Errorf("cast entry %q is already linked to %q", name, entry.TargetCharacterID)
			}
			entry.Promoted = true
			entry.PromotionStatus = "promoted"
			entry.TargetCharacterID = characterID
			return s.io.WriteJSONUnlocked(castLedgerPath, entries)
		}
		return fmt.Errorf("cast entry %q not found", name)
	})
}

// RecentActive 返回最近活跃的 N 条配角条目（按 LastSeenChapter 倒序）。
// 用于 novel_context 召回 Writer 写下一章时可能需要的"近期出场配角"。
//
// 已升格到 characters.json 的条目（Promoted=true）会被跳过，避免与核心档案重复召回。
func (s *CastStore) RecentActive(limit int) ([]domain.CastEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	entries, err := s.Load()
	if err != nil {
		return nil, err
	}
	active := entries[:0:0]
	for _, e := range entries {
		if e.Promoted {
			continue
		}
		active = append(active, e)
	}
	if len(active) == 0 {
		return nil, nil
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].LastSeenChapter != active[j].LastSeenChapter {
			return active[i].LastSeenChapter > active[j].LastSeenChapter
		}
		return active[i].AppearanceCount > active[j].AppearanceCount
	})
	if len(active) > limit {
		active = active[:limit]
	}
	return active, nil
}
