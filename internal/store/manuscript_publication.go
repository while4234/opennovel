package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const manuscriptPublicationJournalPath = "meta/revisions/manuscript/publication.json"

type manuscriptPublicationJournal struct {
	RevisionID         string                                 `json:"revision_id"`
	ExpectedRevision   int                                    `json:"expected_revision"`
	IdempotencyKey     string                                 `json:"idempotency_key"`
	PublishFingerprint string                                 `json:"publish_fingerprint"`
	CandidateSignature string                                 `json:"candidate_signature"`
	ChapterID          string                                 `json:"chapter_id"`
	DisplayChapter     int                                    `json:"display_chapter"`
	DisplayChapters    []int                                  `json:"display_chapters,omitempty"`
	Status             domain.ManuscriptPublicationStatus     `json:"status"`
	PreviousFiles      map[string]domain.ManuscriptContentRef `json:"previous_files"`
	Candidate          domain.ManuscriptCandidate             `json:"candidate"`
	Candidates         []domain.ManuscriptCandidate           `json:"candidates,omitempty"`
	UpdatedAt          string                                 `json:"updated_at"`
}

func (s *Store) PublishManuscriptCandidate(runtime *domain.ManuscriptRevisionRuntime, idempotencyKey string) (*domain.ManuscriptRevisionRuntime, error) {
	if s == nil || runtime == nil || len(runtime.Candidates) == 0 || runtime.Stage != "ready_to_publish" {
		return nil, fmt.Errorf("manuscript candidate is not ready to publish")
	}
	candidate := runtime.Candidates[0]
	chapters := make([]int, 0, len(runtime.Candidates))
	signatures := make([]string, 0, len(runtime.Candidates))
	for _, queuedCandidate := range runtime.Candidates {
		if queuedCandidate.AuditSignature == "" || queuedCandidate.AuditArtifact == nil || queuedCandidate.AuditArtifact.Validate() != nil {
			return nil, fmt.Errorf("manuscript publication binding is invalid")
		}
		candidateForSignature := queuedCandidate
		candidateForSignature.AuditSignature = ""
		candidateForSignature.AuditArtifact = nil
		candidatePayload, _ := json.Marshal(candidateForSignature)
		if queuedCandidate.AuditArtifact.CandidateSignature != domain.ContentSignature(candidatePayload) {
			return nil, fmt.Errorf("manuscript publication audit artifact is stale")
		}
		chapters = append(chapters, queuedCandidate.DisplayChapter)
		signatures = append(signatures, queuedCandidate.AuditSignature)
	}
	if err := s.validateManuscriptPublicationPlan(runtime.Candidates); err != nil {
		return nil, err
	}
	fingerprint, err := manuscriptFingerprint("publish", signatures)
	if err != nil {
		return nil, err
	}
	var result *domain.ManuscriptRevisionRuntime
	err = s.Revisions.withRevisionTransaction(func() error {
		index, err := s.ManuscriptRevisions.loadUnlocked()
		if err != nil {
			return err
		}
		if replay, replayErr := replayManuscriptReceipt(index, idempotencyKey, "publish", fingerprint); replay != nil || replayErr != nil {
			result = replay
			return replayErr
		}
		current, ok := index.Revisions[runtime.RevisionID]
		if !ok {
			return ErrManuscriptRevisionNotFound
		}
		if current.Revision != runtime.Revision {
			return fmt.Errorf("%w: expected %d actual %d", ErrManuscriptRevisionConflict, runtime.Revision, current.Revision)
		}
		for _, queuedCandidate := range current.Candidates {
			formalProse, loadErr := s.Drafts.LoadChapterText(queuedCandidate.DisplayChapter)
			if loadErr != nil {
				return loadErr
			}
			expected := ""
			for _, item := range current.Queue {
				if item.ChapterID == queuedCandidate.ChapterID && len(item.ExpectedSignatures) > 0 {
					expected = item.ExpectedSignatures[0]
					break
				}
			}
			if expected == "" || domain.ContentSignature([]byte(formalProse)) != expected {
				return fmt.Errorf("stale baseline for stable chapter %q", queuedCandidate.ChapterID)
			}
		}
		if journal, loadErr := s.loadManuscriptPublicationJournal(); loadErr != nil {
			return loadErr
		} else if journal != nil {
			return fmt.Errorf("publication recovery required")
		}
		// Invalidate the rebuildable projection before any formal bytes change.
		// Failure is safe here because no publication journal or formal write has
		// started yet; a successful publication can never leave a stale index.
		if err := s.ManuscriptRevisions.io.RemoveFile(manuscriptContentIndexPath); err != nil {
			return fmt.Errorf("invalidate manuscript content index: %w", err)
		}
		formalFiles, err := s.snapshotManuscriptFormalFiles(chapters)
		if err != nil {
			return err
		}
		journal := manuscriptPublicationJournal{
			RevisionID: runtime.RevisionID, ExpectedRevision: runtime.Revision, IdempotencyKey: idempotencyKey,
			PublishFingerprint: fingerprint, CandidateSignature: candidate.AuditSignature, ChapterID: candidate.ChapterID,
			DisplayChapter: candidate.DisplayChapter, DisplayChapters: chapters, Status: domain.ManuscriptPublicationPrepared,
			PreviousFiles: formalFiles, Candidate: candidate, Candidates: append([]domain.ManuscriptCandidate(nil), runtime.Candidates...), UpdatedAt: domain.RevisionTimestamp(),
		}
		if err := s.ManuscriptRevisions.io.WriteJSON(manuscriptPublicationJournalPath, journal); err != nil {
			return err
		}
		if err := s.manuscriptPublicationFail("prepared"); err != nil {
			return err
		}
		for _, queuedCandidate := range current.Candidates {
			if err := s.applyManuscriptCandidate(current, queuedCandidate); err != nil {
				return s.rollbackManuscriptPublication(journal, err)
			}
		}
		journal.Status, journal.UpdatedAt = domain.ManuscriptPublicationFormalApplied, domain.RevisionTimestamp()
		if err := s.ManuscriptRevisions.io.WriteJSON(manuscriptPublicationJournalPath, journal); err != nil {
			return s.rollbackManuscriptPublication(journal, err)
		}
		if err := s.manuscriptPublicationFail("formal_applied"); err != nil {
			return s.rollbackManuscriptPublication(journal, err)
		}
		// This journal is durable commit intent. Startup finishes runtime CAS if
		// the process exits after this write and before the index write.
		journal.Status, journal.UpdatedAt = domain.ManuscriptPublicationCompleted, domain.RevisionTimestamp()
		if err := s.ManuscriptRevisions.io.WriteJSON(manuscriptPublicationJournalPath, journal); err != nil {
			return s.rollbackManuscriptPublication(journal, err)
		}
		if err := s.manuscriptPublicationFail("completed"); err != nil {
			return err
		}
		result, err = completeManuscriptRuntime(index, journal)
		if err != nil {
			return err
		}
		if err := s.ManuscriptRevisions.io.WriteJSON(manuscriptRuntimeIndexPath, index); err != nil {
			return err
		}
		if err := s.manuscriptPublicationFail("runtime_completed"); err != nil {
			return err
		}
		return s.ManuscriptRevisions.io.RemoveFile(manuscriptPublicationJournalPath)
	})
	return result, err
}

func (s *Store) validateManuscriptPublicationPlan(candidates []domain.ManuscriptCandidate) error {
	seenStableIDs := make(map[string]struct{}, len(candidates))
	seenChapters := make(map[int]struct{}, len(candidates))
	for _, candidate := range candidates {
		if err := candidate.Validate(); err != nil {
			return fmt.Errorf("invalid manuscript publication candidate %q: %w", candidate.ChapterID, err)
		}
		if _, duplicate := seenStableIDs[candidate.ChapterID]; duplicate {
			return fmt.Errorf("duplicate stable ID %q in manuscript publication plan", candidate.ChapterID)
		}
		if _, duplicate := seenChapters[candidate.DisplayChapter]; duplicate {
			return fmt.Errorf("duplicate display chapter %d in manuscript publication plan", candidate.DisplayChapter)
		}
		seenStableIDs[candidate.ChapterID] = struct{}{}
		seenChapters[candidate.DisplayChapter] = struct{}{}
		if candidate.PreserveSemanticSidecars {
			continue
		}
		var summary domain.ChapterSummary
		var events []string
		var timeline []domain.TimelineEvent
		var states []domain.StateChange
		var relationships []domain.RelationshipEntry
		var foreshadow []domain.ForeshadowEntry
		var worldFacts []domain.WorldRule
		var carry manuscriptCarryForward
		for _, sidecar := range []struct {
			ref    domain.ManuscriptContentRef
			target any
		}{
			{candidate.Sidecar.Summary, &summary}, {candidate.Sidecar.Events, &events}, {candidate.Sidecar.Timeline, &timeline},
			{candidate.Sidecar.CastState, &states}, {candidate.Sidecar.Relationships, &relationships},
			{candidate.Sidecar.Foreshadow, &foreshadow}, {candidate.Sidecar.WorldFacts, &worldFacts}, {candidate.Sidecar.CarryForward, &carry},
		} {
			payload, err := s.ManuscriptRevisions.Content().Read(sidecar.ref)
			if err != nil {
				return err
			}
			decoder := json.NewDecoder(bytes.NewReader(payload))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(sidecar.target); err != nil {
				return fmt.Errorf("decode manuscript publication plan for %q: %w", candidate.ChapterID, err)
			}
		}
		if strings.TrimSpace(summary.Summary) == "" || len(events) == 0 || len(timeline) == 0 || len(states) == 0 || len(relationships) == 0 || len(foreshadow) == 0 || len(worldFacts) == 0 || (len(carry.CharacterSnapshots) == 0 && carry.ArcSummary == nil && carry.VolumeSummary == nil) {
			return fmt.Errorf("manuscript publication plan for %q has an empty semantic delta", candidate.ChapterID)
		}
	}
	return nil
}

func completeManuscriptRuntime(index *manuscriptRuntimeIndex, journal manuscriptPublicationJournal) (*domain.ManuscriptRevisionRuntime, error) {
	runtime, ok := index.Revisions[journal.RevisionID]
	if !ok {
		return nil, ErrManuscriptRevisionNotFound
	}
	if runtime.Revision != journal.ExpectedRevision && runtime.Revision != journal.ExpectedRevision+1 {
		return nil, fmt.Errorf("%w: publication expected %d actual %d", ErrManuscriptRevisionConflict, journal.ExpectedRevision, runtime.Revision)
	}
	if runtime.Revision == journal.ExpectedRevision {
		runtime.Revision++
		runtime.PublicationStatus = domain.ManuscriptPublicationCompleted
		if runtime.RequiresCompletionRevalidation {
			runtime.Stage, runtime.CompletionRevalidationStatus = "completion_revalidation_pending", "pending"
		} else {
			runtime.Stage, index.ActiveRevisionID = "completed", ""
		}
		runtime.UpdatedAt = domain.RevisionTimestamp()
		index.Revisions[journal.RevisionID] = runtime
	}
	index.Receipts[journal.IdempotencyKey] = manuscriptRuntimeReceipt{Operation: "publish", Fingerprint: journal.PublishFingerprint, RevisionID: journal.RevisionID, Revision: runtime.Revision}
	copy := runtime
	return &copy, nil
}

func replayManuscriptReceipt(index *manuscriptRuntimeIndex, key, operation, fingerprint string) (*domain.ManuscriptRevisionRuntime, error) {
	receipt, ok := index.Receipts[strings.TrimSpace(key)]
	if !ok {
		return nil, nil
	}
	if receipt.Operation != operation || receipt.Fingerprint != fingerprint {
		return nil, ErrManuscriptIdempotencyConflict
	}
	runtime, ok := index.Revisions[receipt.RevisionID]
	if !ok || runtime.Revision < receipt.Revision {
		return nil, ErrManuscriptRevisionNotFound
	}
	copy := runtime
	return &copy, nil
}

type manuscriptCarryForward struct {
	CharacterSnapshots []domain.CharacterSnapshot `json:"character_snapshots"`
	ArcSummary         *domain.ArcSummary         `json:"arc_summary,omitempty"`
	VolumeSummary      *domain.VolumeSummary      `json:"volume_summary,omitempty"`
}

func (s *Store) applyManuscriptCandidate(runtime domain.ManuscriptRevisionRuntime, candidate domain.ManuscriptCandidate) error {
	prose, err := s.ManuscriptRevisions.Content().Read(candidate.Prose)
	if err != nil {
		return err
	}
	if candidate.PreserveSemanticSidecars {
		if runtime.OutlinePreview != nil {
			return fmt.Errorf("prose-only author save cannot publish an outline change")
		}
		return s.Drafts.writeTextForChapter(candidate.DisplayChapter, "final.md", chapterFinalRel(candidate.DisplayChapter), prose)
	}
	var summary domain.ChapterSummary
	var events []string
	var timeline []domain.TimelineEvent
	var states []domain.StateChange
	var relationships []domain.RelationshipEntry
	var foreshadow []domain.ForeshadowEntry
	var worldFacts []domain.WorldRule
	var carry manuscriptCarryForward
	sidecars := []struct {
		ref    domain.ManuscriptContentRef
		target any
	}{
		{candidate.Sidecar.Summary, &summary}, {candidate.Sidecar.Events, &events}, {candidate.Sidecar.Timeline, &timeline},
		{candidate.Sidecar.CastState, &states}, {candidate.Sidecar.Relationships, &relationships},
		{candidate.Sidecar.Foreshadow, &foreshadow}, {candidate.Sidecar.WorldFacts, &worldFacts}, {candidate.Sidecar.CarryForward, &carry},
	}
	for _, sidecar := range sidecars {
		ref, target := sidecar.ref, sidecar.target
		payload, readErr := s.ManuscriptRevisions.Content().Read(ref)
		if readErr != nil {
			return readErr
		}
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		if decodeErr := decoder.Decode(target); decodeErr != nil {
			return fmt.Errorf("decode manuscript sidecar: %w", decodeErr)
		}
	}
	if strings.TrimSpace(summary.Summary) == "" {
		return fmt.Errorf("candidate chapter summary is empty")
	}
	summary.Chapter, summary.KeyEvents = candidate.DisplayChapter, append([]string(nil), events...)
	for i := range timeline {
		timeline[i].Chapter = candidate.DisplayChapter
	}
	for i := range states {
		states[i].Chapter = candidate.DisplayChapter
	}
	for i := range relationships {
		relationships[i].Chapter = candidate.DisplayChapter
	}
	for i := range foreshadow {
		if foreshadow[i].PlantedAt == 0 {
			foreshadow[i].PlantedAt = candidate.DisplayChapter
		}
	}
	if runtime.OutlinePreview != nil {
		if err := s.applyManuscriptOutlinePreview(*runtime.OutlinePreview); err != nil {
			return err
		}
	}
	if err := s.Drafts.writeTextForChapter(candidate.DisplayChapter, "final.md", chapterFinalRel(candidate.DisplayChapter), prose); err != nil {
		return err
	}
	if err := s.Summaries.saveSummaryOwned(summary); err != nil {
		return err
	}
	if err := s.World.DeleteChapterFacts([]int{candidate.DisplayChapter}); err != nil {
		return err
	}
	if err := s.World.AppendTimelineEvents(timeline); err != nil {
		return err
	}
	if err := s.World.AppendStateChanges(states); err != nil {
		return err
	}
	if err := s.World.UpdateRelationships(relationships); err != nil {
		return err
	}
	if err := s.World.SaveForeshadowLedger(mergeForeshadowForChapter(s, candidate.DisplayChapter, foreshadow)); err != nil {
		return err
	}
	existingRules, err := s.World.LoadWorldRules()
	if err != nil {
		return err
	}
	if err := s.World.SaveWorldRules(mergeManuscriptWorldRules(existingRules, worldFacts)); err != nil {
		return err
	}
	volume, arc := s.chapterVolumeArc(candidate.DisplayChapter)
	if len(carry.CharacterSnapshots) > 0 {
		for i := range carry.CharacterSnapshots {
			carry.CharacterSnapshots[i].Volume, carry.CharacterSnapshots[i].Arc = volume, arc
		}
		existingSnapshots, loadErr := s.Characters.LoadSnapshots(volume, arc)
		if loadErr != nil {
			return loadErr
		}
		if err := s.Characters.SaveSnapshots(volume, arc, mergeManuscriptSnapshots(existingSnapshots, carry.CharacterSnapshots)); err != nil {
			return err
		}
	}
	if carry.ArcSummary != nil {
		carry.ArcSummary.Volume, carry.ArcSummary.Arc = volume, arc
		if err := s.Summaries.saveArcSummaryOwned(*carry.ArcSummary); err != nil {
			return err
		}
	}
	if carry.VolumeSummary != nil {
		carry.VolumeSummary.Volume = volume
		if err := s.Summaries.saveVolumeSummaryOwned(*carry.VolumeSummary); err != nil {
			return err
		}
	}
	return nil
}

func mergeManuscriptWorldRules(existing, replacement []domain.WorldRule) []domain.WorldRule {
	result := append([]domain.WorldRule(nil), existing...)
	positions := make(map[string]int, len(result))
	for index, rule := range result {
		positions[strings.TrimSpace(rule.Category)+"\x00"+strings.TrimSpace(rule.Rule)] = index
	}
	for _, rule := range replacement {
		key := strings.TrimSpace(rule.Category) + "\x00" + strings.TrimSpace(rule.Rule)
		if index, found := positions[key]; found {
			result[index] = rule
			continue
		}
		positions[key] = len(result)
		result = append(result, rule)
	}
	return result
}

func mergeManuscriptSnapshots(existing, replacement []domain.CharacterSnapshot) []domain.CharacterSnapshot {
	result := append([]domain.CharacterSnapshot(nil), existing...)
	positions := make(map[string]int, len(result))
	for index, snapshot := range result {
		positions[strings.TrimSpace(snapshot.Name)] = index
	}
	for _, snapshot := range replacement {
		key := strings.TrimSpace(snapshot.Name)
		if index, found := positions[key]; found {
			result[index] = snapshot
			continue
		}
		positions[key] = len(result)
		result = append(result, snapshot)
	}
	return result
}

func mergeForeshadowForChapter(s *Store, chapter int, replacement []domain.ForeshadowEntry) []domain.ForeshadowEntry {
	existing, _ := s.World.LoadForeshadowLedger()
	kept := existing[:0]
	for _, item := range existing {
		if item.PlantedAt != chapter {
			kept = append(kept, item)
		}
	}
	return append(kept, replacement...)
}

func (s *Store) chapterVolumeArc(chapter int) (int, int) {
	volumes, _ := s.Outline.LoadLayeredOutline()
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			for _, entry := range arc.Chapters {
				if entry.Chapter == chapter {
					return volume.Index, arc.Index
				}
			}
		}
	}
	return 1, 1
}

func (s *Store) applyManuscriptOutlinePreview(preview domain.ManuscriptOutlinePreview) error {
	return s.Outline.io.WithWriteLock(func() error {
		var flat []domain.OutlineEntry
		if err := s.Outline.io.ReadJSONUnlocked("outline.json", &flat); err != nil {
			return err
		}
		found := false
		for i := range flat {
			if flat[i].ID == preview.ChapterID {
				flat[i] = preview.Outline
				found = true
			}
		}
		if !found {
			return fmt.Errorf("stable chapter ID %q not found during outline publication", preview.ChapterID)
		}
		if err := s.Outline.io.WriteJSONUnlocked("outline.json", flat); err != nil {
			return err
		}
		if err := s.Outline.io.WriteMarkdownUnlocked("outline.md", renderOutline(flat)); err != nil {
			return err
		}
		var layered []domain.VolumeOutline
		if err := s.Outline.io.ReadJSONUnlocked("layered_outline.json", &layered); err == nil {
			for vi := range layered {
				for ai := range layered[vi].Arcs {
					for ci := range layered[vi].Arcs[ai].Chapters {
						if layered[vi].Arcs[ai].Chapters[ci].ID == preview.ChapterID {
							layered[vi].Arcs[ai].Chapters[ci] = preview.Outline
						}
					}
				}
			}
			if err := s.Outline.io.WriteJSONUnlocked("layered_outline.json", layered); err != nil {
				return err
			}
			if err := s.Outline.io.WriteMarkdownUnlocked("layered_outline.md", renderLayeredOutline(layered)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) snapshotManuscriptFormalFiles(chapters []int) (map[string]domain.ManuscriptContentRef, error) {
	result := make(map[string]domain.ManuscriptContentRef)
	err := filepath.Walk(s.Dir(), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.Dir(), path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !isManuscriptFormalFile(rel, chapters) {
			return nil
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var ref domain.ManuscriptContentRef
		if strings.HasSuffix(rel, ".json") {
			ref, err = s.ManuscriptRevisions.Content().PutRawJSON(payload)
		} else {
			ref, err = s.ManuscriptRevisions.Content().PutMarkdown(string(payload))
		}
		if err != nil {
			return err
		}
		result[rel] = ref
		return nil
	})
	return result, err
}

func isManuscriptFormalFile(rel string, chapters []int) bool {
	if strings.HasPrefix(rel, "meta/revisions/") || strings.HasPrefix(rel, "meta/runtime/") {
		return false
	}
	chapterMatch := false
	for _, chapter := range chapters {
		if rel == chapterFinalRel(chapter) || strings.HasPrefix(rel, fmt.Sprintf("chapters/%04d/", chapter)) {
			chapterMatch = true
			break
		}
	}
	return chapterMatch || strings.HasPrefix(rel, "summaries/") ||
		rel == "outline.json" || rel == "outline.md" || rel == "layered_outline.json" || rel == "layered_outline.md" ||
		strings.HasPrefix(rel, "timeline.") || strings.HasPrefix(rel, "foreshadow_ledger.") || strings.HasPrefix(rel, "relationship_state.") ||
		strings.HasPrefix(rel, "world_rules.") || rel == "meta/state_changes.json" || strings.HasPrefix(rel, "meta/snapshots/") ||
		strings.Contains(rel, "/final.md") || strings.Contains(rel, "/summary.json") || strings.Contains(rel, "/facts.json")
}

func (s *Store) rollbackManuscriptPublication(journal manuscriptPublicationJournal, cause error) error {
	chapters := journal.DisplayChapters
	if len(chapters) == 0 {
		chapters = []int{journal.DisplayChapter}
	}
	current, walkErr := s.currentManuscriptFormalFiles(chapters)
	if walkErr != nil {
		return fmt.Errorf("%v; rollback scan failed: %w", cause, walkErr)
	}
	for rel := range current {
		if _, existed := journal.PreviousFiles[rel]; !existed {
			_ = s.ManuscriptRevisions.io.RemoveFile(rel)
		}
	}
	for rel, ref := range journal.PreviousFiles {
		payload, err := s.ManuscriptRevisions.Content().Read(ref)
		if err != nil {
			return fmt.Errorf("%v; rollback read failed: %w", cause, err)
		}
		if err := s.ManuscriptRevisions.io.WithWriteLock(func() error { return s.ManuscriptRevisions.io.WriteFileUnlocked(rel, payload) }); err != nil {
			return fmt.Errorf("%v; rollback write failed: %w", cause, err)
		}
	}
	_ = s.ManuscriptRevisions.io.RemoveFile(manuscriptPublicationJournalPath)
	return cause
}

func (s *Store) currentManuscriptFormalFiles(chapters []int) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	err := filepath.Walk(s.Dir(), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.Dir(), path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if isManuscriptFormalFile(rel, chapters) {
			result[rel] = struct{}{}
		}
		return nil
	})
	return result, err
}

func (s *Store) recoverManuscriptPublication() error {
	journal, err := s.loadManuscriptPublicationJournal()
	if err != nil || journal == nil {
		return err
	}
	return s.Revisions.withRevisionTransaction(func() error {
		if journal.Status != domain.ManuscriptPublicationCompleted {
			return s.rollbackManuscriptPublication(*journal, nil)
		}
		index, err := s.ManuscriptRevisions.loadUnlocked()
		if err != nil {
			return err
		}
		if _, err := completeManuscriptRuntime(index, *journal); err != nil {
			return err
		}
		if err := s.ManuscriptRevisions.io.WriteJSON(manuscriptRuntimeIndexPath, index); err != nil {
			return err
		}
		return s.ManuscriptRevisions.io.RemoveFile(manuscriptPublicationJournalPath)
	})
}

func (s *Store) loadManuscriptPublicationJournal() (*manuscriptPublicationJournal, error) {
	var journal manuscriptPublicationJournal
	err := s.ManuscriptRevisions.io.ReadJSON(manuscriptPublicationJournalPath, &journal)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(journal.RevisionID) == "" || journal.DisplayChapter <= 0 || strings.TrimSpace(journal.IdempotencyKey) == "" {
		return nil, fmt.Errorf("invalid manuscript publication journal")
	}
	return &journal, nil
}

func (s *Store) manuscriptPublicationFail(point string) error {
	if s.manuscriptPublicationFailpoint == nil {
		return nil
	}
	return s.manuscriptPublicationFailpoint(point)
}

func (s *Store) ClearCompletedManuscriptPublication(revisionID string) error {
	journal, err := s.loadManuscriptPublicationJournal()
	if err != nil || journal == nil {
		return err
	}
	if journal.RevisionID != revisionID || journal.Status != domain.ManuscriptPublicationCompleted {
		return fmt.Errorf("publication recovery required")
	}
	return s.ManuscriptRevisions.io.RemoveFile(manuscriptPublicationJournalPath)
}
