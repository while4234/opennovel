package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	manuscriptContentIndexVersion = 1
	manuscriptContentIndexPath    = "meta/manuscript/content-index.json"
)

// ManuscriptContentIndex is a rebuildable metadata projection. Formal prose,
// stable structure and revision journals remain authoritative.
type ManuscriptContentIndex struct {
	Version             int                           `json:"version"`
	GenerationSignature string                        `json:"generation_signature"`
	StructureSignature  string                        `json:"structure_signature"`
	CurrentSignature    string                        `json:"current_signature"`
	SummarySignature    string                        `json:"summary_signature"`
	HistorySignature    string                        `json:"history_signature"`
	Signature           string                        `json:"signature"`
	Entries             []ManuscriptContentIndexEntry `json:"entries"`
}

type ManuscriptContentIndexEntry struct {
	StableID      string `json:"stable_id"`
	DisplayOrder  int    `json:"display_order"`
	ChapterNumber int    `json:"chapter_number"`
	CurrentSHA256 string `json:"current_sha256,omitempty"`
	CurrentBytes  int    `json:"current_bytes,omitempty"`
	SummarySHA256 string `json:"summary_sha256,omitempty"`
	HistoryCount  int    `json:"history_count,omitempty"`
}

func (s *Store) LoadOrRebuildManuscriptContentIndex() (*ManuscriptContentIndex, error) {
	generation, err := s.manuscriptContentIndexGeneration()
	if err != nil {
		return nil, err
	}
	var index ManuscriptContentIndex
	if err := s.ManuscriptRevisions.io.ReadJSON(manuscriptContentIndexPath, &index); err == nil {
		if err := index.Validate(); err == nil {
			if index.GenerationSignature == generation {
				return &index, nil
			}
		}
	} else if !os.IsNotExist(err) {
		// A corrupt cache is never authoritative. Rebuild below.
	}
	truth, err := s.manuscriptContentIndexTruth()
	if err != nil {
		return nil, err
	}
	truth.GenerationSignature = generation
	return s.writeManuscriptContentIndex(truth)
}

func (s *Store) RebuildManuscriptContentIndex() (*ManuscriptContentIndex, error) {
	if s == nil {
		return nil, fmt.Errorf("manuscript store is unavailable")
	}
	truth, err := s.manuscriptContentIndexTruth()
	if err != nil {
		return nil, err
	}
	generation, err := s.manuscriptContentIndexGeneration()
	if err != nil {
		return nil, err
	}
	truth.GenerationSignature = generation
	return s.writeManuscriptContentIndex(truth)
}

// manuscriptContentIndexGeneration reads metadata only. Atomic authoritative
// writes replace files and therefore change a file or ancestor-directory
// generation. A matching cache can be served without loading the complete
// manuscript; a mismatch always falls back to authoritative content reads.
func (s *Store) manuscriptContentIndexGeneration() (string, error) {
	if s == nil {
		return "", fmt.Errorf("manuscript store is unavailable")
	}
	paths := []string{
		"outline.json", "layered_outline.json", "chapters", "summaries",
		filepath.FromSlash("meta/structure/chapters"),
		filepath.FromSlash("meta/revisions/manuscript"),
	}
	var metadata []string
	journalPath := filepath.Join(s.dir, filepath.FromSlash(manuscriptMutationGenerationPath))
	if journal, err := os.ReadFile(journalPath); err == nil {
		metadata = append(metadata, "journal:"+domain.ContentSignature(journal))
	} else if !os.IsNotExist(err) {
		return "", err
	} else {
		metadata = append(metadata, "journal:missing")
	}
	for _, relative := range paths {
		absolute := filepath.Join(s.dir, relative)
		info, err := os.Lstat(absolute)
		if os.IsNotExist(err) {
			metadata = append(metadata, filepath.ToSlash(relative)+":missing")
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			metadata = append(metadata, manuscriptGenerationMetadata(absolute, relative, info))
			continue
		}
		err = filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			entryInfo, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			rel, relErr := filepath.Rel(s.dir, path)
			if relErr != nil {
				return relErr
			}
			metadata = append(metadata, manuscriptGenerationMetadata(path, rel, entryInfo))
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	sort.Strings(metadata)
	return domain.ContentSignature([]byte(strings.Join(metadata, "\n"))), nil
}

func manuscriptGenerationMetadata(path, relative string, info os.FileInfo) string {
	kind := "file"
	if info.IsDir() {
		kind = "dir"
	} else if info.Mode()&os.ModeSymlink != 0 {
		kind = "symlink"
	}
	changeStamp, err := authoritativeFileChangeStamp(path, info)
	if err != nil {
		changeStamp = "unavailable"
	}
	return fmt.Sprintf("%s|%s|%d|%d|%o|%s", filepath.ToSlash(relative), kind, info.Size(), info.ModTime().UnixNano(), info.Mode(), changeStamp)
}

func (s *Store) writeManuscriptContentIndex(index ManuscriptContentIndex) (*ManuscriptContentIndex, error) {
	index.Version = manuscriptContentIndexVersion
	index.Signature = index.contentSignature()
	if err := s.ManuscriptRevisions.io.WriteJSON(manuscriptContentIndexPath, index); err != nil {
		return nil, err
	}
	return &index, nil
}

func (s *Store) manuscriptContentIndexTruth() (ManuscriptContentIndex, error) {
	outline, structureSignature, err := s.formalManuscriptChapters()
	if err != nil {
		return ManuscriptContentIndex{}, err
	}
	history, historySignature, err := s.manuscriptHistoryCountsAndSignature()
	if err != nil {
		return ManuscriptContentIndex{}, err
	}
	entries := make([]ManuscriptContentIndexEntry, 0, len(outline))
	for display, chapter := range outline {
		entry := ManuscriptContentIndexEntry{StableID: chapter.ID, DisplayOrder: display + 1, ChapterNumber: chapter.Chapter, HistoryCount: history[chapter.ID]}
		if prose, loadErr := s.Drafts.LoadChapterText(chapter.Chapter); loadErr == nil && strings.TrimSpace(prose) != "" {
			entry.CurrentSHA256 = domain.ContentSignature([]byte(prose))
			entry.CurrentBytes = len([]byte(prose))
		}
		if summary, loadErr := s.Summaries.LoadSummary(chapter.Chapter); loadErr == nil && summary != nil {
			payload, _ := json.Marshal(summary)
			entry.SummarySHA256 = domain.ContentSignature(payload)
		}
		entries = append(entries, entry)
	}
	currentPayload, _ := json.Marshal(struct {
		Entries []ManuscriptContentIndexEntry `json:"entries"`
	}{entries})
	summaryPayload, _ := json.Marshal(func() []string {
		values := make([]string, len(entries))
		for index := range entries {
			values[index] = entries[index].StableID + ":" + entries[index].SummarySHA256
		}
		return values
	}())
	return ManuscriptContentIndex{Version: manuscriptContentIndexVersion, StructureSignature: structureSignature, CurrentSignature: domain.ContentSignature(currentPayload), SummarySignature: domain.ContentSignature(summaryPayload), HistorySignature: historySignature, Entries: entries}, nil
}

func (s *Store) formalManuscriptChapters() ([]domain.OutlineEntry, string, error) {
	volumes, err := s.Outline.LoadLayeredOutline()
	if err == nil && len(volumes) > 0 {
		chapters := make([]domain.OutlineEntry, 0)
		for _, volume := range volumes {
			for _, arc := range volume.Arcs {
				chapters = append(chapters, arc.Chapters...)
			}
		}
		return chapters, domain.StructureSignature(volumes), nil
	}
	outline, outlineErr := s.Outline.LoadOutline()
	if outlineErr != nil {
		return nil, "", outlineErr
	}
	payload, _ := json.Marshal(outline)
	return outline, domain.ContentSignature(payload), nil
}

func (s *Store) manuscriptHistoryCountsAndSignature() (map[string]int, string, error) {
	index, err := s.ManuscriptRevisions.load()
	if err != nil {
		return nil, "", err
	}
	counts := make(map[string]int)
	for _, runtime := range index.Revisions {
		seen := make(map[string]struct{})
		for _, item := range runtime.Queue {
			seen[item.ChapterID] = struct{}{}
		}
		for stableID := range seen {
			counts[stableID]++
		}
	}
	payload, _ := json.Marshal(index.Revisions)
	return counts, domain.ContentSignature(payload), nil
}

func (index ManuscriptContentIndex) Validate() error {
	if index.Version != manuscriptContentIndexVersion || len(index.GenerationSignature) != 64 || len(index.StructureSignature) != 64 || len(index.CurrentSignature) != 64 ||
		len(index.SummarySignature) != 64 || len(index.HistorySignature) != 64 || len(index.Signature) != 64 {
		return fmt.Errorf("invalid manuscript content index header")
	}
	for position, entry := range index.Entries {
		if strings.TrimSpace(entry.StableID) == "" || entry.DisplayOrder != position+1 || entry.ChapterNumber <= 0 {
			return fmt.Errorf("invalid manuscript content index entry %d", position)
		}
		if entry.CurrentSHA256 != "" && len(entry.CurrentSHA256) != 64 {
			return fmt.Errorf("invalid current signature for %q", entry.StableID)
		}
	}
	if index.Signature != index.contentSignature() {
		return fmt.Errorf("manuscript content index signature mismatch")
	}
	return nil
}

func (index ManuscriptContentIndex) contentSignature() string {
	clone := index
	clone.Signature = ""
	payload, _ := json.Marshal(clone)
	return domain.ContentSignature(payload)
}
