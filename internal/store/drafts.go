package store

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// DraftStore 管理章节构思、草稿和终稿。
type DraftStore struct {
	io                 *IO
	migration          *structureMigration
	withFormalMutation func(string, *structureMigration, func() error) error
}

func NewDraftStore(io *IO, migrations ...*structureMigration) *DraftStore {
	var migration *structureMigration
	if len(migrations) > 0 {
		migration = migrations[0]
	}
	return &DraftStore{io: io, migration: migration}
}

// SaveChapterPlan 保存章节构思到 drafts/{ch}.plan.json。
func (s *DraftStore) SaveChapterPlan(plan domain.ChapterPlan) error {
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return s.io.WriteJSON(chapterPlanRel(plan.Chapter), plan)
			}
			ref, ok := index.chapterRef(plan.Chapter)
			if !ok {
				return s.io.WriteJSON(chapterPlanRel(plan.Chapter), plan)
			}
			canonical := plan
			canonical.Chapter = 0
			return s.writeJSONPair(
				chapterCanonicalRel(ref.ID, "plan.json"),
				canonicalChapterPlan{ChapterID: ref.ID, Plan: canonical},
				chapterPlanRel(plan.Chapter), plan,
			)
		})
	}
	return s.io.WriteJSON(fmt.Sprintf("drafts/%02d.plan.json", plan.Chapter), plan)
}

// LoadChapterPlan 读取章节构思。
func (s *DraftStore) LoadChapterPlan(chapter int) (*domain.ChapterPlan, error) {
	if s.migration != nil {
		var result *domain.ChapterPlan
		err := s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return nil
			}
			ref, ok := index.chapterRef(chapter)
			if !ok {
				return nil
			}
			var canonical canonicalChapterPlan
			if err := s.io.ReadJSON(chapterCanonicalRel(ref.ID, "plan.json"), &canonical); err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if canonical.ChapterID != ref.ID {
				return fmt.Errorf("chapter plan identity mismatch for chapter %d", chapter)
			}
			canonical.Plan.Chapter = chapter
			result = &canonical.Plan
			return nil
		})
		if err != nil || result != nil {
			return result, err
		}
	}
	var plan domain.ChapterPlan
	if err := s.io.ReadJSON(fmt.Sprintf("drafts/%02d.plan.json", chapter), &plan); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &plan, nil
}

// SaveDraft 保存整章草稿到 drafts/{ch}.draft.md。
func (s *DraftStore) SaveDraft(chapter int, content string) error {
	if s.migration != nil {
		return s.writeTextForChapter(chapter, "draft.md", chapterDraftRel(chapter), []byte(content))
	}
	return s.io.WriteMarkdown(fmt.Sprintf("drafts/%02d.draft.md", chapter), content)
}

func (s *DraftStore) BackupDraftForRecovery(chapter int, content string) (string, error) {
	if chapter <= 0 || content == "" {
		return "", fmt.Errorf("draft recovery backup requires chapter and content")
	}
	digest := TextSHA256(content)
	rel := fmt.Sprintf("drafts/backups/%02d.%s.draft.md", chapter, digest[:12])
	if existing, err := s.io.ReadFile(rel); err == nil && string(existing) == content {
		return rel, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := s.io.WriteMarkdown(rel, content); err != nil {
		return "", err
	}
	return rel, nil
}

// AppendDraft 追加内容到现有草稿（续写模式）。
func (s *DraftStore) AppendDraft(chapter int, content string) error {
	if s.migration != nil {
		existing, err := s.LoadDraft(chapter)
		if err != nil {
			return err
		}
		if existing != "" {
			content = existing + "\n\n" + content
		}
		return s.SaveDraft(chapter, content)
	}
	rel := fmt.Sprintf("drafts/%02d.draft.md", chapter)
	return s.io.WithWriteLock(func() error {
		existing, err := s.io.ReadFileUnlocked(rel)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		var merged string
		if len(existing) > 0 {
			merged = string(existing) + "\n\n" + content
		} else {
			merged = content
		}
		return s.io.WriteFileUnlocked(rel, []byte(merged))
	})
}

// LoadDraft 读取整章草稿。
func (s *DraftStore) LoadDraft(chapter int) (string, error) {
	if s.migration != nil {
		if data, migrated, err := s.readTextForChapter(chapter, "draft.md", chapterDraftRel(chapter)); err != nil {
			return "", err
		} else if migrated {
			return string(data), nil
		}
	}
	data, err := s.io.ReadFile(fmt.Sprintf("drafts/%02d.draft.md", chapter))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// LoadChapterContent 加载章节草稿正文及字数。
func (s *DraftStore) LoadChapterContent(chapter int) (string, int, error) {
	draft, err := s.LoadDraft(chapter)
	if err != nil {
		return "", 0, err
	}
	if draft != "" {
		return draft, utf8.RuneCountInString(draft), nil
	}
	return "", 0, nil
}

// SaveFinalChapter 保存最终章节正文到 chapters/{ch}.md。
func (s *DraftStore) SaveFinalChapter(chapter int, content string) error {
	if s.withFormalMutation != nil {
		return s.withFormalMutation("save final chapter", s.migration, func() error {
			return s.saveFinalChapterOwned(chapter, content)
		})
	}
	return s.saveFinalChapterOwned(chapter, content)
}

// saveFinalChapterOwned is used by compound store transactions that already
// hold meta/revisions/transaction.lock.
func (s *DraftStore) saveFinalChapterOwned(chapter int, content string) error {
	if s.migration != nil {
		return s.writeTextForChapter(chapter, "final.md", chapterFinalRel(chapter), []byte(content))
	}
	return s.io.WriteMarkdown(fmt.Sprintf("chapters/%02d.md", chapter), content)
}

// LoadChapterText 读取已提交的终稿原文。
func (s *DraftStore) LoadChapterText(chapter int) (string, error) {
	if s.migration != nil {
		if data, migrated, err := s.readTextForChapter(chapter, "final.md", chapterFinalRel(chapter)); err != nil {
			return "", err
		} else if migrated {
			return string(data), nil
		}
	}
	data, err := s.io.ReadFile(fmt.Sprintf("chapters/%02d.md", chapter))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *DraftStore) DeleteChapterArtifacts(chapter int) error {
	if chapter <= 0 {
		return nil
	}
	if s.withFormalMutation != nil {
		return s.withFormalMutation("delete chapter artifacts", s.migration, func() error {
			return s.deleteChapterArtifactsOwned(chapter)
		})
	}
	return s.deleteChapterArtifactsOwned(chapter)
}

// deleteChapterArtifactsOwned is used by compound store transactions that
// already hold meta/revisions/transaction.lock.
func (s *DraftStore) deleteChapterArtifactsOwned(chapter int) error {
	deleteArtifacts := func(canonicalID string) error {
		return s.io.WithWriteLock(func() error {
			paths := []string{
				fmt.Sprintf("drafts/%02d.plan.json", chapter),
				fmt.Sprintf("drafts/%02d.draft.md", chapter),
				fmt.Sprintf("chapters/%02d.md", chapter),
			}
			if canonicalID != "" {
				paths = append(paths,
					chapterCanonicalRel(canonicalID, "plan.json"),
					chapterCanonicalRel(canonicalID, "draft.md"),
					chapterCanonicalRel(canonicalID, "final.md"),
				)
			}
			for _, path := range paths {
				if err := s.io.RemoveFileUnlocked(path); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return deleteArtifacts("")
			}
			id, ok := index.chapterID(chapter)
			if !ok {
				return nil
			}
			return deleteArtifacts(id)
		})
	}
	return deleteArtifacts("")
}

func (s *DraftStore) writeTextForChapter(chapter int, name, legacyRel string, data []byte) error {
	return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
		if !migrated {
			return s.io.WithWriteLock(func() error { return s.io.WriteFileUnlocked(legacyRel, data) })
		}
		ref, ok := index.chapterRef(chapter)
		if !ok {
			return s.io.WithWriteLock(func() error { return s.io.WriteFileUnlocked(legacyRel, data) })
		}
		return s.io.WithWriteLock(func() error {
			if err := s.io.WriteFileUnlocked(chapterCanonicalRel(ref.ID, name), data); err != nil {
				return err
			}
			return s.io.WriteFileUnlocked(legacyRel, data)
		})
	})
}

func (s *DraftStore) readTextForChapter(chapter int, name, legacyRel string) ([]byte, bool, error) {
	var data []byte
	migrated := false
	err := s.migration.withIndexRead(func(index structureIndex, ok bool) error {
		if !ok {
			return nil
		}
		migrated = true
		ref, exists := index.chapterRef(chapter)
		if !exists {
			migrated = false
			return nil
		}
		var err error
		data, err = s.io.ReadFile(chapterCanonicalRel(ref.ID, name))
		if os.IsNotExist(err) {
			data, err = s.io.ReadFile(legacyRel)
			if os.IsNotExist(err) {
				data = nil
				return nil
			}
		}
		return err
	})
	return data, migrated, err
}

func (s *DraftStore) writeJSONPair(canonicalRel string, canonical any, legacyRel string, legacy any) error {
	canonicalData, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return err
	}
	legacyData, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		return err
	}
	return s.io.WithWriteLock(func() error {
		if err := s.io.WriteFileUnlocked(canonicalRel, canonicalData); err != nil {
			return err
		}
		return s.io.WriteFileUnlocked(legacyRel, legacyData)
	})
}

// LoadChapterRange 读取指定范围的终稿原文片段。
func (s *DraftStore) LoadChapterRange(from, to, maxRunes int) (map[int]string, error) {
	result := make(map[int]string)
	for ch := from; ch <= to; ch++ {
		text, err := s.LoadChapterText(ch)
		if err != nil {
			return nil, err
		}
		if text == "" {
			continue
		}
		if maxRunes > 0 {
			runes := []rune(text)
			if len(runes) > maxRunes {
				text = string(runes[:maxRunes]) + "..."
			}
		}
		result[ch] = text
	}
	return result, nil
}

var dialogueRe = regexp.MustCompile(`"[^"]*"`)

// ExtractDialogue 从已提交章节中提取指定角色的对话片段。
// maxCompletedChapter 由调用方传入，避免跨域依赖。
func (s *DraftStore) ExtractDialogue(characterName string, aliases []string, maxSamples, maxCompletedChapter int) []string {
	if maxSamples <= 0 {
		maxSamples = 5
	}
	names := append([]string{characterName}, aliases...)

	var samples []string
	for ch := maxCompletedChapter; ch >= 1 && len(samples) < maxSamples; ch-- {
		text, err := s.LoadChapterText(ch)
		if err != nil || text == "" {
			continue
		}
		paragraphs := strings.Split(text, "\n")
		for _, para := range paragraphs {
			if len(samples) >= maxSamples {
				break
			}
			found := false
			for _, name := range names {
				if strings.Contains(para, name) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
			matches := dialogueRe.FindAllString(para, -1)
			for _, m := range matches {
				if len(samples) >= maxSamples {
					break
				}
				if utf8.RuneCountInString(m) > 5 {
					samples = append(samples, characterName+": "+m)
				}
			}
		}
	}
	return samples
}

// ExtractStyleAnchors 从已提交章节中提取代表性段落作为风格锚点。
// maxCompletedChapter 由调用方传入，避免跨域依赖。
func (s *DraftStore) ExtractStyleAnchors(maxAnchors, maxCompletedChapter int) []string {
	if maxAnchors <= 0 {
		maxAnchors = 5
	}

	var anchors []string
	for ch := 1; ch <= maxCompletedChapter && len(anchors) < maxAnchors; ch++ {
		text, err := s.LoadChapterText(ch)
		if err != nil || text == "" {
			continue
		}
		paragraphs := strings.Split(text, "\n\n")
		for _, para := range paragraphs {
			if len(anchors) >= maxAnchors {
				break
			}
			para = strings.TrimSpace(para)
			runeCount := utf8.RuneCountInString(para)
			if runeCount < 50 || runeCount > 300 {
				continue
			}
			if strings.Count(para, "\u201c") > 2 {
				continue
			}
			anchors = append(anchors, para)
		}
	}
	return anchors
}
