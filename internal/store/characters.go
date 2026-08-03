package store

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// CharacterStore 管理角色档案和状态快照。
type CharacterStore struct {
	io                            *IO
	outline                       *OutlineStore // 只读依赖，用于快照遍历
	foundation                    *FoundationStore
	withFoundationGenerationGuard func(string, func() error) error
}

func NewCharacterStore(io *IO, outline *OutlineStore) *CharacterStore {
	return &CharacterStore{io: io, outline: outline}
}

// Save 同时保存 characters.json 和 characters.md（原子写入）。
func (s *CharacterStore) Save(chars []domain.Character) error {
	if s.foundation != nil {
		if s.withFoundationGenerationGuard != nil {
			return s.withFoundationGenerationGuard("save foundation characters", func() error { return s.foundation.updateCharacters(chars) })
		}
		return s.foundation.updateCharacters(chars)
	}
	return s.io.WithWriteLock(func() error {
		if err := s.io.WriteJSONUnlocked("characters.json", chars); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("characters.md", renderCharacters(chars))
	})
}

// Load 从 characters.json 读取角色档案。
func (s *CharacterStore) Load() ([]domain.Character, error) {
	if s.foundation != nil {
		foundation, err := s.foundation.Load()
		return foundation.Characters, err
	}
	var chars []domain.Character
	if err := s.io.ReadJSON("characters.json", &chars); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return chars, nil
}

// SaveSnapshots 保存角色状态快照到 meta/snapshots/v{vol}a{arc}.json。
func (s *CharacterStore) SaveSnapshots(volume, arc int, snapshots []domain.CharacterSnapshot) error {
	return s.io.WriteJSON(fmt.Sprintf("meta/snapshots/v%02da%02d.json", volume, arc), snapshots)
}

// LoadSnapshots 读取指定卷弧的角色快照。
func (s *CharacterStore) LoadSnapshots(volume, arc int) ([]domain.CharacterSnapshot, error) {
	var snapshots []domain.CharacterSnapshot
	if err := s.io.ReadJSON(fmt.Sprintf("meta/snapshots/v%02da%02d.json", volume, arc), &snapshots); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return snapshots, nil
}

// LoadLatestSnapshots 加载最近一次角色快照（按卷弧倒序查找）。
func (s *CharacterStore) LoadLatestSnapshots() ([]domain.CharacterSnapshot, error) {
	volumes, _ := s.outline.LoadLayeredOutline()
	if len(volumes) == 0 {
		return nil, nil
	}
	for vi := len(volumes) - 1; vi >= 0; vi-- {
		v := volumes[vi]
		for ai := len(v.Arcs) - 1; ai >= 0; ai-- {
			snaps, err := s.LoadSnapshots(v.Index, v.Arcs[ai].Index)
			if err != nil {
				return nil, err
			}
			if len(snaps) > 0 {
				return snaps, nil
			}
		}
	}
	return nil, nil
}

func renderCharacters(chars []domain.Character) string {
	var b strings.Builder
	b.WriteString("# 角色档案\n\n")
	for _, c := range chars {
		fmt.Fprintf(&b, "## %s（%s）\n\n", c.Name, c.Role)
		fmt.Fprintf(&b, "%s\n\n", c.Description)
		if c.Arc != "" {
			fmt.Fprintf(&b, "**角色弧线**：%s\n\n", c.Arc)
		}
		if len(c.Traits) > 0 {
			fmt.Fprintf(&b, "**特征**：%s\n\n", strings.Join(c.Traits, "、"))
		}
		for _, contrast := range c.ContrastDetails {
			fmt.Fprintf(&b, "**反差**：%s → %s\n\n", contrast.Surface, contrast.Depth)
		}
		for _, backstory := range c.KeyBackstory {
			fmt.Fprintf(&b, "**关键过往**：%s（影响：%s）\n\n", backstory.Event, backstory.Impact)
		}
		if c.InitialState != nil {
			parts := []string{c.InitialState.Identity, c.InitialState.Situation, c.InitialState.Emotion}
			parts = append(parts, c.InitialState.Resources...)
			parts = append(parts, c.InitialState.Relationships)
			fmt.Fprintf(&b, "**初始状态**：%s\n\n", strings.Join(nonEmptyCharacterCardParts(parts), "；"))
		}
		if c.KnowledgeBoundary != nil {
			fmt.Fprintf(
				&b,
				"**知识边界**：已知[%s]；未知[%s]；误解[%s]；禁止提前知道[%s]\n\n",
				strings.Join(c.KnowledgeBoundary.Known, "、"),
				strings.Join(c.KnowledgeBoundary.Unknown, "、"),
				strings.Join(c.KnowledgeBoundary.Misconceptions, "、"),
				strings.Join(c.KnowledgeBoundary.Forbidden, "、"),
			)
		}
	}
	return b.String()
}

func nonEmptyCharacterCardParts(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
