package domain

// Novel 小说元信息。
type Novel struct {
	Name          string `json:"name"`
	TotalChapters int    `json:"total_chapters"`
}

// OutlineEntry 大纲条目，对应一章。
type OutlineEntry struct {
	ID                string                    `json:"id,omitempty"`
	Chapter           int                       `json:"chapter"`
	Title             string                    `json:"title"`
	CoreEvent         string                    `json:"core_event"`
	Hook              string                    `json:"hook"`
	Scenes            []string                  `json:"scenes"`
	CharacterIDs      []string                  `json:"character_ids,omitempty"`
	CharacterBeats    []OutlineCharacterBeat    `json:"character_beats,omitempty"`
	RelationshipBeats []OutlineRelationshipBeat `json:"relationship_beats,omitempty"`
	TemporaryRoles    []TemporaryCharacterNeed  `json:"temporary_roles,omitempty"`
	DramaticFacts     *ExpansionDramaticFactSet `json:"dramatic_facts,omitempty"`
	ExpansionOrigin   *ExpansionOrigin          `json:"expansion_origin,omitempty"`
}

// OutlineCharacterBeat binds chapter intent to a confirmed character ID.
type OutlineCharacterBeat struct {
	CharacterID string `json:"character_id"`
	Scene       string `json:"scene,omitempty"`
	Goal        string `json:"goal,omitempty"`
	Obstacle    string `json:"obstacle,omitempty"`
	ChoiceCost  string `json:"choice_cost,omitempty"`
	Advance     string `json:"advance,omitempty"`
}

// OutlineRelationshipBeat describes intended runtime relationship progress.
type OutlineRelationshipBeat struct {
	RelationshipID    string `json:"relationship_id,omitempty"`
	SourceCharacterID string `json:"source_character_id"`
	TargetCharacterID string `json:"target_character_id"`
	Scene             string `json:"scene,omitempty"`
	Start             string `json:"start,omitempty"`
	ExpectedAdvance   string `json:"expected_advance,omitempty"`
	ForbiddenJump     string `json:"forbidden_jump,omitempty"`
}

// TemporaryCharacterNeed authorizes a bounded unnamed or decorative role.
type TemporaryCharacterNeed struct {
	Role      string `json:"role"`
	Scene     string `json:"scene,omitempty"`
	Purpose   string `json:"purpose,omitempty"`
	Important bool   `json:"important,omitempty"`
}

// Character 角色档案。
type Character struct {
	ID                string                      `json:"id,omitempty"`
	Name              string                      `json:"name"`
	Aliases           []string                    `json:"aliases,omitempty"` // 别名/称号/绰号（如"废物少年"、"炎哥"）
	Role              string                      `json:"role"`
	Gender            string                      `json:"gender,omitempty"` // male / female / nonbinary / unspecified
	Description       string                      `json:"description"`
	Arc               string                      `json:"arc"`
	Traits            []string                    `json:"traits"`
	Tier              string                      `json:"tier,omitempty"` // core / important / secondary / decorative（默认 important）
	Faction           string                      `json:"faction,omitempty"`
	Goal              string                      `json:"goal,omitempty"`
	Motivation        string                      `json:"motivation,omitempty"`
	Conflict          string                      `json:"conflict,omitempty"`
	Voice             string                      `json:"voice,omitempty"`
	Constraints       []string                    `json:"constraints,omitempty"`
	ContrastDetails   []CharacterContrastDetail   `json:"contrast_details,omitempty"`
	KeyBackstory      []CharacterBackstory        `json:"key_backstory,omitempty"`
	InitialState      *CharacterInitialState      `json:"initial_state,omitempty"`
	KnowledgeBoundary *CharacterKnowledgeBoundary `json:"knowledge_boundary,omitempty"`
	Notes             string                      `json:"notes,omitempty"`
}

// CharacterContrastDetail captures a usable difference between a character's
// outward presentation and less visible behavior or motives.
type CharacterContrastDetail struct {
	Surface string `json:"surface"`
	Depth   string `json:"depth"`
}

// CharacterBackstory keeps only past events that affect present choices,
// relationships, or beliefs.
type CharacterBackstory struct {
	Event  string `json:"event"`
	Impact string `json:"impact"`
}

// CharacterInitialState is the chapter-zero baseline for a character.
type CharacterInitialState struct {
	Identity      string   `json:"identity,omitempty"`
	Situation     string   `json:"situation,omitempty"`
	Emotion       string   `json:"emotion,omitempty"`
	Resources     []string `json:"resources,omitempty"`
	Relationships string   `json:"relationships,omitempty"`
}

// CharacterKnowledgeBoundary separates known facts from unknown,
// misunderstood, and deliberately withheld information.
type CharacterKnowledgeBoundary struct {
	Known          []string `json:"known,omitempty"`
	Unknown        []string `json:"unknown,omitempty"`
	Misconceptions []string `json:"misconceptions,omitempty"`
	Forbidden      []string `json:"forbidden,omitempty"`
}

// VolumeOutline 卷级大纲（长篇分层模式）。
type VolumeOutline struct {
	ID    string       `json:"id,omitempty"`
	Index int          `json:"index"`
	Title string       `json:"title"`
	Theme string       `json:"theme"` // 本卷核心冲突/主题
	Arcs  []ArcOutline `json:"arcs"`
}

// IsExpanded 判断卷是否已展开（有弧级结构）。
func (v *VolumeOutline) IsExpanded() bool { return len(v.Arcs) > 0 }

// StoryCompass 终局方向指南针，替代固定的骨架卷列表。
// Architect 在每次卷边界时可更新，允许故事方向随创作演化。
type StoryCompass struct {
	EndingDirection string   `json:"ending_direction"`          // 终局方向（主题性描述）
	OpenThreads     []string `json:"open_threads,omitempty"`    // 活跃长线（需收束才能结局）
	EstimatedScale  string   `json:"estimated_scale,omitempty"` // 模糊规模（如"预计 4-6 卷"）
	LastUpdated     int      `json:"last_updated,omitempty"`    // 更新时的已完成章节数
}

// ArcOutline 弧级大纲。
type ArcOutline struct {
	ID                string         `json:"id,omitempty"`
	Index             int            `json:"index"` // 卷内弧序号
	Title             string         `json:"title"`
	Goal              string         `json:"goal"`                         // 弧目标（起承转合）
	EstimatedChapters int            `json:"estimated_chapters,omitempty"` // 骨架弧的预估章数（展开后清零）
	Chapters          []OutlineEntry `json:"chapters"`
}

// IsExpanded 判断弧是否已展开（有详细章节）。
func (a *ArcOutline) IsExpanded() bool { return len(a.Chapters) > 0 }

// TotalChapters 计算分层大纲的当前规划总章数。
// 已展开弧按真实章节数计，骨架弧按 EstimatedChapters 计。
// Progress.TotalChapters 用它判断长篇上下文策略；真正可写章节仍来自 FlattenOutline。
func TotalChapters(volumes []VolumeOutline) int {
	n := 0
	for _, v := range volumes {
		for _, a := range v.Arcs {
			if a.IsExpanded() {
				n += len(a.Chapters)
			} else {
				n += a.EstimatedChapters
			}
		}
	}
	return n
}

// FlattenOutline 将分层大纲展开为扁平章节列表，保持全局章节号连续。
func FlattenOutline(volumes []VolumeOutline) []OutlineEntry {
	var result []OutlineEntry
	ch := 1
	for _, v := range volumes {
		for _, a := range v.Arcs {
			for _, e := range a.Chapters {
				e.Chapter = ch
				result = append(result, e)
				ch++
			}
		}
	}
	return result
}

// ProjectOutlineOrder derives display chapter numbers from slice order while
// preserving each target chapter's stable identity.
func ProjectOutlineOrder(entries []OutlineEntry) []OutlineEntry {
	projected := make([]OutlineEntry, len(entries))
	for i := range entries {
		projected[i] = entries[i]
		projected[i].Scenes = append([]string(nil), entries[i].Scenes...)
		projected[i].CharacterIDs = append([]string(nil), entries[i].CharacterIDs...)
		projected[i].CharacterBeats = append([]OutlineCharacterBeat(nil), entries[i].CharacterBeats...)
		projected[i].RelationshipBeats = append([]OutlineRelationshipBeat(nil), entries[i].RelationshipBeats...)
		projected[i].TemporaryRoles = append([]TemporaryCharacterNeed(nil), entries[i].TemporaryRoles...)
		projected[i].Chapter = i + 1
	}
	return projected
}

// ProjectLayeredOutlineOrder derives volume, arc, and display chapter ordinals
// from the current slice order. IDs are never rewritten by this projection.
func ProjectLayeredOutlineOrder(volumes []VolumeOutline) []VolumeOutline {
	projected := make([]VolumeOutline, len(volumes))
	chapter := 1
	for volumeIndex := range volumes {
		projected[volumeIndex] = volumes[volumeIndex]
		projected[volumeIndex].Index = volumeIndex + 1
		projected[volumeIndex].Arcs = make([]ArcOutline, len(volumes[volumeIndex].Arcs))
		for arcIndex := range volumes[volumeIndex].Arcs {
			arc := volumes[volumeIndex].Arcs[arcIndex]
			projected[volumeIndex].Arcs[arcIndex] = arc
			projected[volumeIndex].Arcs[arcIndex].Index = arcIndex + 1
			projected[volumeIndex].Arcs[arcIndex].Chapters = make([]OutlineEntry, len(arc.Chapters))
			for chapterIndex := range arc.Chapters {
				entry := arc.Chapters[chapterIndex]
				entry.Scenes = append([]string(nil), entry.Scenes...)
				entry.CharacterIDs = append([]string(nil), entry.CharacterIDs...)
				entry.CharacterBeats = append([]OutlineCharacterBeat(nil), entry.CharacterBeats...)
				entry.RelationshipBeats = append([]OutlineRelationshipBeat(nil), entry.RelationshipBeats...)
				entry.TemporaryRoles = append([]TemporaryCharacterNeed(nil), entry.TemporaryRoles...)
				entry.Chapter = chapter
				projected[volumeIndex].Arcs[arcIndex].Chapters[chapterIndex] = entry
				chapter++
			}
		}
	}
	return projected
}

// WorldRule 世界观规则条目。
type WorldRule struct {
	ID       string            `json:"id,omitempty"`
	Category string            `json:"category"` // magic / technology / geography / society / other
	Title    string            `json:"title,omitempty"`
	Rule     string            `json:"rule"`     // 规则描述
	Boundary string            `json:"boundary"` // 不可违反的边界
	Strength WorldRuleStrength `json:"strength,omitempty"`
	Priority int               `json:"priority,omitempty"`
	Tags     []string          `json:"tags,omitempty"`
}
