package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	structureSchemaVersion       = 1
	structureRootDir             = "meta/structure"
	structureIndexFile           = structureRootDir + "/index.json"
	structureMigrationLogFile    = structureRootDir + "/migration.json"
	structureMigrationStagingDir = structureRootDir + "/staging"
	structureRequestReceiptDir   = structureRootDir + "/requests"
	structureFactsFile           = structureRootDir + "/facts.json"

	migrationStagePlanned   = "planned"
	migrationStageWritten   = "written"
	migrationStageValidated = "validated"
	migrationStageSwitching = "switching"
	migrationStageSwitched  = "switched"

	migrationFailAfterWrite       = "after_write"
	migrationFailAfterValidate    = "after_validate"
	migrationFailDuringSwitch     = "during_switch"
	migrationFailDuringProjection = "during_projection"
	migrationFailDuringRemoval    = "during_removal"
	migrationFailBeforeIndexWrite = "before_index_write"
	migrationFailAfterIndexWrite  = "after_index_write"
	migrationFailBeforeLogCleanup = "before_log_cleanup"
)

type structureMigration struct {
	io                       *IO
	mu                       sync.RWMutex
	failpoint                func(string) error
	recoverWithRevisionFence func(string, func() error) error
}

type structureIndex struct {
	Version  int                   `json:"version"`
	Volumes  []structureVolumeRef  `json:"volumes,omitempty"`
	Arcs     []structureArcRef     `json:"arcs,omitempty"`
	Chapters []structureChapterRef `json:"chapters"`
}

type structureVolumeRef struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
}

type structureArcRef struct {
	ID       string `json:"id"`
	VolumeID string `json:"volume_id"`
	Number   int    `json:"number"`
}

type structureChapterRef struct {
	ID       string `json:"id"`
	Number   int    `json:"number"`
	VolumeID string `json:"volume_id,omitempty"`
	ArcID    string `json:"arc_id,omitempty"`
}

type structureMigrationLog struct {
	Version           int                   `json:"version"`
	OperationID       string                `json:"operation_id"`
	RequestID         string                `json:"request_id,omitempty"`
	Kind              string                `json:"kind"`
	Stage             string                `json:"stage"`
	CreatedAt         string                `json:"created_at"`
	HadIndex          bool                  `json:"had_index"`
	SourceIndex       structureIndex        `json:"source_index"`
	TargetIndex       structureIndex        `json:"target_index"`
	StructureData     []migrationPayload    `json:"structure_data"`
	RequestedRemovals []string              `json:"requested_removals,omitempty"`
	Files             []migrationFileRecord `json:"files,omitempty"`
	RemovePaths       []string              `json:"remove_paths,omitempty"`
}

type structureMigrationResume struct {
	OperationID string
	RequestID   string
	Kind        string
}

type structureRequestReceipt struct {
	Version       int            `json:"version"`
	RequestID     string         `json:"request_id"`
	RequestDigest string         `json:"request_digest"`
	OperationID   string         `json:"operation_id"`
	Kind          string         `json:"kind"`
	TargetDigest  string         `json:"target_digest"`
	TargetIndex   structureIndex `json:"target_index"`
}

type structureMigrationBuild struct {
	LegacySource structureIndex
	Target       structureIndex
	Payloads     []migrationPayload
	RemovePaths  []string
}

type migrationPayload struct {
	Rel  string `json:"rel"`
	Data []byte `json:"data"`
}

type migrationFileRecord struct {
	FinalRel string `json:"final_rel"`
	StageRel string `json:"stage_rel"`
	SHA256   string `json:"sha256"`
}

type canonicalChapterPlan struct {
	ChapterID string             `json:"chapter_id"`
	Plan      domain.ChapterPlan `json:"plan"`
}

type canonicalChapterSummary struct {
	ChapterID string                `json:"chapter_id"`
	Summary   domain.ChapterSummary `json:"summary"`
}

type canonicalChapterReview struct {
	ChapterID          string             `json:"chapter_id"`
	VolumeID           string             `json:"volume_id,omitempty"`
	ArcID              string             `json:"arc_id,omitempty"`
	BatchFromID        string             `json:"batch_from_id,omitempty"`
	BatchToID          string             `json:"batch_to_id,omitempty"`
	AffectedChapterIDs []string           `json:"affected_chapter_ids,omitempty"`
	Review             domain.ReviewEntry `json:"review"`
}

type canonicalAdaptationCheck struct {
	ChapterID string                 `json:"chapter_id"`
	Check     domain.AdaptationCheck `json:"check"`
}

type canonicalArcSummary struct {
	ArcID   string            `json:"arc_id"`
	Summary domain.ArcSummary `json:"summary"`
}

type canonicalVolumeSummary struct {
	VolumeID string               `json:"volume_id"`
	Summary  domain.VolumeSummary `json:"summary"`
}

type canonicalFacts struct {
	Timeline      []canonicalTimelineEvent     `json:"timeline,omitempty"`
	Foreshadow    []canonicalForeshadowEntry   `json:"foreshadow,omitempty"`
	Relationships []canonicalRelationshipEntry `json:"relationships,omitempty"`
	StateChanges  []canonicalStateChange       `json:"state_changes,omitempty"`
}

type canonicalTimelineEvent struct {
	ChapterID string               `json:"chapter_id,omitempty"`
	Event     domain.TimelineEvent `json:"event"`
}

type canonicalForeshadowEntry struct {
	PlantedChapterID  string                 `json:"planted_chapter_id,omitempty"`
	ResolvedChapterID string                 `json:"resolved_chapter_id,omitempty"`
	Entry             domain.ForeshadowEntry `json:"entry"`
}

type canonicalRelationshipEntry struct {
	ChapterID string                   `json:"chapter_id,omitempty"`
	Entry     domain.RelationshipEntry `json:"entry"`
}

type canonicalStateChange struct {
	ChapterID string             `json:"chapter_id,omitempty"`
	Change    domain.StateChange `json:"change"`
}

func newStructureMigration(dir string) *structureMigration {
	return &structureMigration{io: newIO(dir)}
}

func (m *structureMigration) withRead(fn func() error) error {
	if m == nil {
		return fn()
	}
	for {
		pending, err := m.pending()
		if err != nil {
			return fmt.Errorf("check structure migration before read: %w", err)
		}
		if pending {
			return m.withFencedRead(fn)
		}

		m.mu.Lock()
		pending, err = m.pending()
		if err != nil {
			m.mu.Unlock()
			return fmt.Errorf("check structure migration before read: %w", err)
		}
		if pending {
			m.mu.Unlock()
			continue
		}
		err = fn()
		m.mu.Unlock()
		return err
	}
}

func (m *structureMigration) withFencedRead(fn func() error) error {
	if m.recoverWithRevisionFence == nil {
		return fmt.Errorf("recover structure migration before read: revision fence is not configured")
	}
	return m.recoverWithRevisionFence("recover pending structure migration before read", func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, err := m.resumeUnlocked(); err != nil {
			return fmt.Errorf("recover structure migration before read: %w", err)
		}
		return fn()
	})
}

func (m *structureMigration) save(kind string, legacySource, target structureIndex, payloads []migrationPayload) error {
	return m.saveWithRemovals(kind, legacySource, target, payloads, nil)
}

func (m *structureMigration) saveWithRemovals(kind string, legacySource, target structureIndex, payloads []migrationPayload, removePaths []string) error {
	return m.saveBuilt(kind, func(_ structureIndex, _ bool) (structureMigrationBuild, error) {
		return structureMigrationBuild{
			LegacySource: legacySource,
			Target:       target,
			Payloads:     payloads,
			RemovePaths:  removePaths,
		}, nil
	})
}

func (m *structureMigration) saveBuilt(
	kind string,
	build func(current structureIndex, hadIndex bool) (structureMigrationBuild, error),
) error {
	return m.saveRequested(kind, "", build)
}

func (m *structureMigration) saveRequested(
	kind string,
	requestID string,
	build func(current structureIndex, hadIndex bool) (structureMigrationBuild, error),
) error {
	if m == nil {
		return fmt.Errorf("structure migration is not configured")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	recovered, err := m.resumeUnlocked()
	if err != nil {
		return err
	}
	if requestID != "" {
		if recovered.RequestID == requestID {
			return nil
		}
		completed, err := m.requestCompletedUnlocked(requestID)
		if err != nil {
			return err
		}
		if completed {
			return nil
		}
	}
	current, hadIndex, err := m.loadIndexUnlocked()
	if err != nil {
		return err
	}
	request, err := build(current, hadIndex)
	if err != nil {
		return err
	}
	if err := request.Target.validate(); err != nil {
		return err
	}
	source := current
	if !hadIndex {
		source = request.LegacySource
		if len(source.Chapters) == 0 && len(request.Target.Chapters) > 0 {
			source = request.Target
		}
		if err := source.validate(); err != nil {
			return fmt.Errorf("validate legacy structure: %w", err)
		}
	}
	log, err := newMigrationLog(kind, requestID, source, request.Target, hadIndex, request.Payloads, request.RemovePaths)
	if err != nil {
		return err
	}
	if recovered.OperationID != "" && recovered.OperationID == log.OperationID {
		return nil
	}
	if err := m.writeLogUnlocked(&log); err != nil {
		return err
	}
	return m.executeUnlocked(&log)
}

// recoverWithinRevisionTransaction is the raw migration recovery primitive for
// callers that already hold the project's revision transaction. It must never
// acquire the revision fence itself; doing so would re-enter the non-reentrant
// per-project transaction mutex.
func (m *structureMigration) recoverWithinRevisionTransaction() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.resumeUnlocked()
	return err
}

func (m *structureMigration) pending() (bool, error) {
	if m == nil {
		return false, nil
	}
	_, err := os.Stat(m.io.path(structureMigrationLogFile))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (m *structureMigration) resumeUnlocked() (structureMigrationResume, error) {
	var log structureMigrationLog
	if err := m.io.ReadJSON(structureMigrationLogFile, &log); err != nil {
		if os.IsNotExist(err) {
			return structureMigrationResume{}, nil
		}
		return structureMigrationResume{}, fmt.Errorf("read structure migration log: %w", err)
	}
	if log.Version != structureSchemaVersion || strings.TrimSpace(log.OperationID) == "" {
		return structureMigrationResume{}, fmt.Errorf("invalid structure migration log")
	}
	if err := m.executeUnlocked(&log); err != nil {
		return structureMigrationResume{}, err
	}
	return structureMigrationResume{OperationID: log.OperationID, RequestID: log.RequestID, Kind: log.Kind}, nil
}

func (m *structureMigration) executeUnlocked(log *structureMigrationLog) error {
	switch log.Stage {
	case migrationStagePlanned:
		if err := m.buildStageUnlocked(log); err != nil {
			return err
		}
		log.Stage = migrationStageWritten
		if err := m.writeLogUnlocked(log); err != nil {
			return err
		}
		if err := m.inject(migrationFailAfterWrite); err != nil {
			return err
		}
		fallthrough
	case migrationStageWritten:
		if err := m.validateStageUnlocked(log); err != nil {
			return err
		}
		log.Stage = migrationStageValidated
		if err := m.writeLogUnlocked(log); err != nil {
			return err
		}
		if err := m.inject(migrationFailAfterValidate); err != nil {
			return err
		}
		fallthrough
	case migrationStageValidated:
		log.Stage = migrationStageSwitching
		if err := m.writeLogUnlocked(log); err != nil {
			return err
		}
		fallthrough
	case migrationStageSwitching:
		if err := m.installCanonicalFilesUnlocked(log); err != nil {
			return err
		}
		if err := m.inject(migrationFailDuringSwitch); err != nil {
			return err
		}
		if err := m.installProjectionFilesUnlocked(log); err != nil {
			return err
		}
		log.Stage = migrationStageSwitched
		if err := m.writeLogUnlocked(log); err != nil {
			return err
		}
		fallthrough
	case migrationStageSwitched:
		if err := m.inject(migrationFailBeforeLogCleanup); err != nil {
			return err
		}
		if _, err := m.io.RemoveAllRel(structureMigrationStageRel(log.OperationID)); err != nil {
			return fmt.Errorf("clean structure migration staging: %w", err)
		}
		if err := m.io.RemoveFile(structureMigrationLogFile); err != nil {
			return fmt.Errorf("remove structure migration log: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown structure migration stage %q", log.Stage)
	}
}

func (m *structureMigration) buildStageUnlocked(log *structureMigrationLog) error {
	stageRoot := structureMigrationStageRel(log.OperationID)
	if _, err := m.io.RemoveAllRel(stageRoot); err != nil {
		return err
	}
	log.Files = nil
	log.RemovePaths = append([]string(nil), log.RequestedRemovals...)
	log.RemovePaths = append(log.RemovePaths, numericArtifactPathsNotInTarget(log.SourceIndex, log.TargetIndex)...)
	if err := m.stageChapterArtifactsUnlocked(log); err != nil {
		return err
	}
	if err := m.stageScopedSummariesUnlocked(log); err != nil {
		return err
	}
	if err := m.stageArcBatchReviewsUnlocked(log); err != nil {
		return err
	}
	if err := m.stageFactsUnlocked(log); err != nil {
		return err
	}
	for _, payload := range log.StructureData {
		if err := m.addStageFileUnlocked(log, payload.Rel, payload.Data); err != nil {
			return err
		}
	}
	if log.RequestID != "" {
		receipt := structureRequestReceipt{
			Version:       structureSchemaVersion,
			RequestID:     log.RequestID,
			RequestDigest: log.RequestID,
			OperationID:   log.OperationID,
			Kind:          log.Kind,
			TargetDigest:  structureIndexDigest(log.TargetIndex),
			TargetIndex:   log.TargetIndex,
		}
		data, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			return err
		}
		if err := m.addStageFileUnlocked(log, structureRequestReceiptRel(log.RequestID), data); err != nil {
			return err
		}
	}
	indexData, err := json.MarshalIndent(log.TargetIndex, "", "  ")
	if err != nil {
		return err
	}
	if err := m.addStageFileUnlocked(log, structureIndexFile, indexData); err != nil {
		return err
	}
	return m.stageNumericProjectionsUnlocked(log)
}

func (m *structureMigration) validateStageUnlocked(log *structureMigrationLog) error {
	if err := log.SourceIndex.validate(); err != nil {
		return fmt.Errorf("validate source structure index: %w", err)
	}
	if err := log.TargetIndex.validate(); err != nil {
		return fmt.Errorf("validate target structure index: %w", err)
	}
	seen := make(map[string]struct{}, len(log.Files))
	for _, file := range log.Files {
		if _, exists := seen[file.FinalRel]; exists {
			return fmt.Errorf("duplicate staged structure path %q", file.FinalRel)
		}
		seen[file.FinalRel] = struct{}{}
		if _, err := m.io.safeRelPath(file.FinalRel); err != nil {
			return err
		}
		data, err := m.io.ReadFile(file.StageRel)
		if err != nil {
			return fmt.Errorf("read staged structure file %s: %w", file.FinalRel, err)
		}
		if digest(data) != file.SHA256 {
			return fmt.Errorf("staged structure file checksum mismatch: %s", file.FinalRel)
		}
	}
	return nil
}

func (m *structureMigration) installCanonicalFilesUnlocked(log *structureMigrationLog) error {
	for _, file := range log.Files {
		if !isCanonicalStructurePath(file.FinalRel) || file.FinalRel == structureIndexFile {
			continue
		}
		data, err := m.io.ReadFile(file.StageRel)
		if err != nil {
			return err
		}
		if err := m.io.WriteFileUnlocked(file.FinalRel, data); err != nil {
			return fmt.Errorf("install canonical structure file %s: %w", file.FinalRel, err)
		}
	}
	return nil
}

func (m *structureMigration) installProjectionFilesUnlocked(log *structureMigrationLog) error {
	for _, file := range log.Files {
		if isCanonicalStructurePath(file.FinalRel) {
			continue
		}
		data, err := m.io.ReadFile(file.StageRel)
		if err != nil {
			return err
		}
		if err := m.io.WriteFileUnlocked(file.FinalRel, data); err != nil {
			return fmt.Errorf("install structure projection %s: %w", file.FinalRel, err)
		}
		if err := m.inject(migrationFailDuringProjection); err != nil {
			return err
		}
	}
	for _, rel := range log.RemovePaths {
		if _, err := m.io.safeRelPath(rel); err != nil {
			return err
		}
		if _, err := m.io.RemoveAllRel(rel); err != nil {
			return err
		}
		if err := m.inject(migrationFailDuringRemoval); err != nil {
			return err
		}
	}
	if err := m.inject(migrationFailBeforeIndexWrite); err != nil {
		return err
	}
	for _, file := range log.Files {
		if file.FinalRel != structureIndexFile {
			continue
		}
		data, err := m.io.ReadFile(file.StageRel)
		if err != nil {
			return err
		}
		if err := m.io.WriteFileUnlocked(structureIndexFile, data); err != nil {
			return fmt.Errorf("install structure index: %w", err)
		}
		if err := m.inject(migrationFailAfterIndexWrite); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("structure migration did not stage an index")
}

func (m *structureMigration) addStageFileUnlocked(log *structureMigrationLog, finalRel string, data []byte) error {
	if _, err := m.io.safeRelPath(finalRel); err != nil {
		return fmt.Errorf("unsafe structure target %q: %w", finalRel, err)
	}
	stageRel := filepath.ToSlash(filepath.Join(structureMigrationStageRel(log.OperationID), filepath.FromSlash(finalRel)))
	if _, err := m.io.safeRelPath(stageRel); err != nil {
		return err
	}
	if err := m.io.WriteFileUnlocked(stageRel, data); err != nil {
		return err
	}
	for i := range log.Files {
		if log.Files[i].FinalRel == finalRel {
			log.Files[i] = migrationFileRecord{FinalRel: finalRel, StageRel: stageRel, SHA256: digest(data)}
			return nil
		}
	}
	log.Files = append(log.Files, migrationFileRecord{FinalRel: finalRel, StageRel: stageRel, SHA256: digest(data)})
	return nil
}

func (m *structureMigration) stageChapterArtifactsUnlocked(log *structureMigrationLog) error {
	for _, ref := range log.SourceIndex.Chapters {
		artifacts := []struct {
			legacy    string
			canonical string
			convert   func([]byte, structureIndex, structureChapterRef) ([]byte, error)
		}{
			{chapterFinalRel(ref.Number), chapterCanonicalRel(ref.ID, "final.md"), nil},
			{chapterDraftRel(ref.Number), chapterCanonicalRel(ref.ID, "draft.md"), nil},
			{chapterPlanRel(ref.Number), chapterCanonicalRel(ref.ID, "plan.json"), canonicalizeChapterPlan},
			{chapterSummaryRel(ref.Number), chapterCanonicalRel(ref.ID, "summary.json"), canonicalizeChapterSummary},
			{chapterReviewRel(ref.Number, false), chapterCanonicalRel(ref.ID, "review.json"), canonicalizeChapterReview},
			{chapterReviewRel(ref.Number, true), chapterCanonicalRel(ref.ID, "review-global.json"), canonicalizeChapterReview},
			{adaptationCheckRel(ref.Number), chapterCanonicalRel(ref.ID, "adaptation-check.json"), canonicalizeAdaptationCheck},
		}
		for _, artifact := range artifacts {
			data, ok, fromCanonical, err := m.readCanonicalOrLegacyUnlocked(artifact.canonical, artifact.legacy, log.HadIndex)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if !fromCanonical && artifact.convert != nil {
				data, err = artifact.convert(data, log.SourceIndex, ref)
				if err != nil {
					return fmt.Errorf("canonicalize %s: %w", artifact.legacy, err)
				}
			}
			if err := m.addStageFileUnlocked(log, artifact.canonical, data); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *structureMigration) stageScopedSummariesUnlocked(log *structureMigrationLog) error {
	for _, ref := range log.SourceIndex.Volumes {
		canonical := volumeCanonicalRel(ref.ID, "summary.json")
		legacy := volumeSummaryRel(ref.Number)
		data, ok, fromCanonical, err := m.readScopedSummaryUnlocked(canonical, legacy, log.HadIndex)
		if err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		if !fromCanonical {
			var summary domain.VolumeSummary
			if err := json.Unmarshal(data, &summary); err != nil {
				return err
			}
			summary.Volume = 0
			data, err = json.MarshalIndent(canonicalVolumeSummary{VolumeID: ref.ID, Summary: summary}, "", "  ")
			if err != nil {
				return err
			}
		}
		if err := m.addStageFileUnlocked(log, canonical, data); err != nil {
			return err
		}
	}
	for _, ref := range log.SourceIndex.Arcs {
		volumeNumber, ok := log.SourceIndex.volumeNumber(ref.VolumeID)
		if !ok {
			return fmt.Errorf("arc %s references missing volume %s", ref.ID, ref.VolumeID)
		}
		canonical := arcCanonicalRel(ref.ID, "summary.json")
		legacy := arcSummaryRel(volumeNumber, ref.Number)
		data, exists, fromCanonical, err := m.readScopedSummaryUnlocked(canonical, legacy, log.HadIndex)
		if err != nil || !exists {
			if err != nil {
				return err
			}
			continue
		}
		if !fromCanonical {
			var summary domain.ArcSummary
			if err := json.Unmarshal(data, &summary); err != nil {
				return err
			}
			summary.Volume, summary.Arc = 0, 0
			data, err = json.MarshalIndent(canonicalArcSummary{ArcID: ref.ID, Summary: summary}, "", "  ")
			if err != nil {
				return err
			}
		}
		if err := m.addStageFileUnlocked(log, canonical, data); err != nil {
			return err
		}
	}
	return nil
}

func (m *structureMigration) stageArcBatchReviewsUnlocked(log *structureMigrationLog) error {
	for _, arc := range log.SourceIndex.Arcs {
		volumeNumber, ok := log.SourceIndex.volumeNumber(arc.VolumeID)
		if !ok {
			return fmt.Errorf("arc %s references missing volume %s", arc.ID, arc.VolumeID)
		}
		canonicalDir := arcBatchCanonicalDir(arc.VolumeID, arc.ID)
		foundCanonical, err := m.stageCanonicalReviewDirUnlocked(log, canonicalDir)
		if err != nil {
			return err
		}
		if foundCanonical {
			continue
		}
		legacyDir := arcBatchReviewDir(volumeNumber, arc.Number)
		entries, err := os.ReadDir(m.io.path(legacyDir))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
				continue
			}
			var review domain.ReviewEntry
			if err := m.io.ReadJSON(legacyDir+"/"+entry.Name(), &review); err != nil {
				return err
			}
			if review.Scope != "arc_batch" {
				continue
			}
			canonical, err := canonicalizeArcBatchReview(review, log.SourceIndex)
			if err != nil {
				return err
			}
			data, err := json.MarshalIndent(canonical, "", "  ")
			if err != nil {
				return err
			}
			if err := m.addStageFileUnlocked(log, arcBatchCanonicalRel(canonical), data); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *structureMigration) stageCanonicalReviewDirUnlocked(log *structureMigrationLog, rel string) (bool, error) {
	entries, err := os.ReadDir(m.io.path(rel))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	found := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		data, err := m.io.ReadFile(rel + "/" + entry.Name())
		if err != nil {
			return false, err
		}
		var canonical canonicalChapterReview
		if err := json.Unmarshal(data, &canonical); err != nil {
			return false, err
		}
		if canonical.Review.Scope != "arc_batch" {
			continue
		}
		if err := m.addStageFileUnlocked(log, rel+"/"+entry.Name(), data); err != nil {
			return false, err
		}
		found = true
	}
	return found, nil
}

func (m *structureMigration) stageFactsUnlocked(log *structureMigrationLog) error {
	if log.HadIndex {
		data, err := m.io.ReadFile(structureFactsFile)
		if err == nil {
			return m.addStageFileUnlocked(log, structureFactsFile, data)
		}
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	facts, found, err := loadLegacyCanonicalFacts(m.io, log.SourceIndex)
	if err != nil {
		return err
	}
	if !found {
		data, readErr := m.io.ReadFile(structureFactsFile)
		if readErr == nil {
			return m.addStageFileUnlocked(log, structureFactsFile, data)
		}
		if os.IsNotExist(readErr) {
			return nil
		}
		return readErr
	}
	data, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		return err
	}
	return m.addStageFileUnlocked(log, structureFactsFile, data)
}

func (m *structureMigration) stageNumericProjectionsUnlocked(log *structureMigrationLog) error {
	for _, target := range log.TargetIndex.Chapters {
		for _, spec := range []struct {
			canonical string
			legacy    string
			project   func([]byte, structureIndex, structureChapterRef) ([]byte, error)
		}{
			{chapterCanonicalRel(target.ID, "final.md"), chapterFinalRel(target.Number), nil},
			{chapterCanonicalRel(target.ID, "draft.md"), chapterDraftRel(target.Number), nil},
			{chapterCanonicalRel(target.ID, "plan.json"), chapterPlanRel(target.Number), projectChapterPlan},
			{chapterCanonicalRel(target.ID, "summary.json"), chapterSummaryRel(target.Number), projectChapterSummary},
			{chapterCanonicalRel(target.ID, "review.json"), chapterReviewRel(target.Number, false), projectChapterReview},
			{chapterCanonicalRel(target.ID, "review-global.json"), chapterReviewRel(target.Number, true), projectChapterReview},
			{chapterCanonicalRel(target.ID, "adaptation-check.json"), adaptationCheckRel(target.Number), projectAdaptationCheck},
		} {
			data, ok, err := m.readStagedFinalUnlocked(log, spec.canonical)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if spec.project != nil {
				data, err = spec.project(data, log.TargetIndex, target)
				if err != nil {
					return err
				}
			}
			if err := m.addStageFileUnlocked(log, spec.legacy, data); err != nil {
				return err
			}
		}
	}
	if err := m.stageScopedSummaryProjectionsUnlocked(log); err != nil {
		return err
	}
	if err := m.stageArcBatchReviewProjectionsUnlocked(log); err != nil {
		return err
	}
	if err := m.stageFactProjectionsUnlocked(log); err != nil {
		return err
	}
	m.markUnprojectedNumericArtifactsForRemoval(log)
	log.RemovePaths = sortedUnique(log.RemovePaths)
	return nil
}

func (m *structureMigration) stageScopedSummaryProjectionsUnlocked(log *structureMigrationLog) error {
	for _, ref := range log.TargetIndex.Volumes {
		data, ok, err := m.readStagedFinalUnlocked(log, volumeCanonicalRel(ref.ID, "summary.json"))
		if err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		var item canonicalVolumeSummary
		if err := json.Unmarshal(data, &item); err != nil {
			return err
		}
		item.Summary.Volume = ref.Number
		projected, err := json.MarshalIndent(item.Summary, "", "  ")
		if err != nil {
			return err
		}
		if err := m.addStageFileUnlocked(log, volumeSummaryRel(ref.Number), projected); err != nil {
			return err
		}
	}
	for _, ref := range log.TargetIndex.Arcs {
		data, ok, err := m.readStagedFinalUnlocked(log, arcCanonicalRel(ref.ID, "summary.json"))
		if err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		var item canonicalArcSummary
		if err := json.Unmarshal(data, &item); err != nil {
			return err
		}
		volumeNumber, exists := log.TargetIndex.volumeNumber(ref.VolumeID)
		if !exists {
			return fmt.Errorf("missing volume for arc %s", ref.ID)
		}
		item.Summary.Volume, item.Summary.Arc = volumeNumber, ref.Number
		projected, err := json.MarshalIndent(item.Summary, "", "  ")
		if err != nil {
			return err
		}
		if err := m.addStageFileUnlocked(log, arcSummaryRel(volumeNumber, ref.Number), projected); err != nil {
			return err
		}
	}
	existing, err := listJSONRelPaths(m.io, "summaries")
	if err != nil {
		return err
	}
	staged := make(map[string]struct{}, len(log.Files))
	for _, file := range log.Files {
		staged[file.FinalRel] = struct{}{}
	}
	for _, rel := range existing {
		if !isScopedSummaryProjectionPath(rel) {
			continue
		}
		if _, ok := staged[rel]; !ok {
			log.RemovePaths = append(log.RemovePaths, rel)
		}
	}
	return nil
}

func (m *structureMigration) stageArcBatchReviewProjectionsUnlocked(log *structureMigrationLog) error {
	canonicalFiles := append([]migrationFileRecord(nil), log.Files...)
	for _, file := range canonicalFiles {
		if !isArcBatchCanonicalReviewPath(file.FinalRel) {
			continue
		}
		data, err := m.io.ReadFile(file.StageRel)
		if err != nil {
			return err
		}
		var canonical canonicalChapterReview
		if err := json.Unmarshal(data, &canonical); err != nil {
			return err
		}
		review, err := projectArcBatchReview(canonical, log.TargetIndex)
		if err != nil {
			return err
		}
		projected, err := json.MarshalIndent(review, "", "  ")
		if err != nil {
			return err
		}
		if err := m.addStageFileUnlocked(log, ArcBatchReviewRelPath(review.Volume, review.Arc, review.BatchFrom, review.BatchTo), projected); err != nil {
			return err
		}
	}
	existing, err := listJSONRelPaths(m.io, "reviews/arc_batches")
	if err != nil {
		return err
	}
	staged := make(map[string]struct{}, len(log.Files))
	for _, file := range log.Files {
		staged[file.FinalRel] = struct{}{}
	}
	for _, rel := range existing {
		if _, ok := staged[rel]; !ok {
			log.RemovePaths = append(log.RemovePaths, rel)
		}
	}
	return nil
}

func listJSONRelPaths(io *IO, rootRel string) ([]string, error) {
	root, err := io.safeRelPath(rootRel)
	if err != nil {
		return nil, err
	}
	var paths []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(strings.ToLower(info.Name()), ".json") {
			return nil
		}
		rel, err := filepath.Rel(io.dir, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return paths, err
}

func (m *structureMigration) stageFactProjectionsUnlocked(log *structureMigrationLog) error {
	data, ok, err := m.readStagedFinalUnlocked(log, structureFactsFile)
	if err != nil || !ok {
		return err
	}
	var facts canonicalFacts
	if err := json.Unmarshal(data, &facts); err != nil {
		return err
	}
	timeline, foreshadow, relationships, changes, err := projectCanonicalFacts(facts, log.TargetIndex)
	if err != nil {
		return err
	}
	if err := m.addJSONAndMarkdownUnlocked(log, "timeline.json", timeline, "timeline.md", renderTimeline(timeline)); err != nil {
		return err
	}
	if err := m.addJSONAndMarkdownUnlocked(log, "foreshadow_ledger.json", foreshadow, "foreshadow_ledger.md", renderForeshadow(foreshadow)); err != nil {
		return err
	}
	if err := m.addJSONAndMarkdownUnlocked(log, "relationship_state.json", relationships, "relationship_state.md", renderRelationships(relationships)); err != nil {
		return err
	}
	changesData, err := json.MarshalIndent(changes, "", "  ")
	if err != nil {
		return err
	}
	return m.addStageFileUnlocked(log, "meta/state_changes.json", changesData)
}

func (m *structureMigration) addJSONAndMarkdownUnlocked(log *structureMigrationLog, jsonRel string, value any, markdownRel, markdown string) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := m.addStageFileUnlocked(log, jsonRel, data); err != nil {
		return err
	}
	return m.addStageFileUnlocked(log, markdownRel, []byte(markdown))
}

func (m *structureMigration) readCanonicalOrLegacyUnlocked(canonical, legacy string, preferCanonical bool) ([]byte, bool, bool, error) {
	if preferCanonical {
		data, err := m.io.ReadFile(canonical)
		if err == nil {
			return data, true, true, nil
		}
		if !os.IsNotExist(err) {
			return nil, false, false, err
		}
	}
	data, err := m.io.ReadFile(legacy)
	if err == nil {
		return data, true, false, nil
	}
	if os.IsNotExist(err) {
		return nil, false, false, nil
	}
	return nil, false, false, err
}

func (m *structureMigration) readScopedSummaryUnlocked(canonical, legacy string, hadIndex bool) ([]byte, bool, bool, error) {
	rel := legacy
	fromCanonical := false
	if hadIndex {
		rel = canonical
		fromCanonical = true
	}
	data, err := m.io.ReadFile(rel)
	if err == nil {
		return data, true, fromCanonical, nil
	}
	if os.IsNotExist(err) {
		return nil, false, fromCanonical, nil
	}
	return nil, false, fromCanonical, err
}

func (m *structureMigration) markUnprojectedNumericArtifactsForRemoval(log *structureMigrationLog) {
	staged := make(map[string]struct{}, len(log.Files))
	for _, file := range log.Files {
		staged[file.FinalRel] = struct{}{}
	}
	for _, ref := range log.TargetIndex.Chapters {
		for _, rel := range []string{
			chapterFinalRel(ref.Number), chapterDraftRel(ref.Number), chapterPlanRel(ref.Number),
			chapterSummaryRel(ref.Number), chapterReviewRel(ref.Number, false),
			chapterReviewRel(ref.Number, true), adaptationCheckRel(ref.Number),
		} {
			if _, ok := staged[rel]; !ok {
				log.RemovePaths = append(log.RemovePaths, rel)
			}
		}
	}
}

func (m *structureMigration) readStagedFinalUnlocked(log *structureMigrationLog, finalRel string) ([]byte, bool, error) {
	for _, file := range log.Files {
		if file.FinalRel != finalRel {
			continue
		}
		data, err := m.io.ReadFile(file.StageRel)
		return data, err == nil, err
	}
	data, err := m.io.ReadFile(finalRel)
	if err == nil {
		return data, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, err
}

func (m *structureMigration) loadIndexUnlocked() (structureIndex, bool, error) {
	var index structureIndex
	if err := m.io.ReadJSON(structureIndexFile, &index); err != nil {
		if os.IsNotExist(err) {
			return structureIndex{}, false, nil
		}
		return structureIndex{}, false, err
	}
	if err := index.validate(); err != nil {
		return structureIndex{}, false, err
	}
	index = structureIndexWithImplicitVolumeArcs(index)
	if err := index.validate(); err != nil {
		return structureIndex{}, false, err
	}
	return index, true, nil
}

func (m *structureMigration) requestCompletedUnlocked(requestID string) (bool, error) {
	var receipt structureRequestReceipt
	if err := m.io.ReadJSON(structureRequestReceiptRel(requestID), &receipt); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if receipt.Version != structureSchemaVersion || receipt.RequestID != requestID || receipt.RequestDigest != requestID {
		return false, fmt.Errorf("invalid structure request receipt %q", requestID)
	}
	if strings.TrimSpace(receipt.OperationID) == "" || strings.TrimSpace(receipt.Kind) == "" {
		return false, fmt.Errorf("incomplete structure request receipt %q", requestID)
	}
	if err := receipt.TargetIndex.validate(); err != nil {
		return false, fmt.Errorf("invalid structure request receipt target %q: %w", requestID, err)
	}
	if receipt.TargetDigest != structureIndexDigest(receipt.TargetIndex) {
		return false, fmt.Errorf("structure request receipt target digest mismatch %q", requestID)
	}
	return true, nil
}

func (m *structureMigration) loadIndex() (structureIndex, bool, error) {
	if m == nil {
		return structureIndex{}, false, nil
	}
	var index structureIndex
	var ok bool
	err := m.withRead(func() error {
		var err error
		index, ok, err = m.loadIndexUnlocked()
		return err
	})
	return index, ok, err
}

func (m *structureMigration) withIndexRead(fn func(structureIndex, bool) error) error {
	if m == nil {
		return fn(structureIndex{}, false)
	}
	return m.withRead(func() error {
		index, ok, err := m.loadIndexUnlocked()
		if err != nil {
			return err
		}
		return fn(index, ok)
	})
}

func (m *structureMigration) writeLogUnlocked(log *structureMigrationLog) error {
	if err := m.io.WriteJSON(structureMigrationLogFile, log); err != nil {
		return fmt.Errorf("write structure migration log: %w", err)
	}
	return nil
}

func (m *structureMigration) inject(point string) error {
	if m.failpoint == nil {
		return nil
	}
	if err := m.failpoint(point); err != nil {
		return fmt.Errorf("structure migration interrupted at %s: %w", point, err)
	}
	return nil
}

func newMigrationLog(kind, requestID string, source, target structureIndex, hadIndex bool, payloads []migrationPayload, removePaths []string) (structureMigrationLog, error) {
	removePaths = sortedUnique(removePaths)
	seed, err := json.Marshal(struct {
		Kind        string
		RequestID   string
		Target      structureIndex
		Payloads    []migrationPayload
		RemovePaths []string
	}{kind, requestID, target, payloads, removePaths})
	if err != nil {
		return structureMigrationLog{}, err
	}
	sum := sha256.Sum256(seed)
	return structureMigrationLog{
		Version: structureSchemaVersion, OperationID: hex.EncodeToString(sum[:12]), RequestID: requestID, Kind: kind,
		Stage: migrationStagePlanned, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		HadIndex: hadIndex, SourceIndex: source, TargetIndex: target, StructureData: payloads,
		RequestedRemovals: removePaths,
	}, nil
}

func migrationRequestIdentity(kind string, request any) (string, error) {
	data, err := json.Marshal(struct {
		Kind    string `json:"kind"`
		Request any    `json:"request"`
	}{Kind: kind, Request: request})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16]), nil
}

func structureIndexDigest(index structureIndex) string {
	data, _ := json.Marshal(index)
	return digest(data)
}

func jsonMigrationPayload(rel string, value any) (migrationPayload, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return migrationPayload{}, err
	}
	return migrationPayload{Rel: rel, Data: data}, nil
}

func structureIndexFromLayered(volumes []domain.VolumeOutline) structureIndex {
	index := structureIndex{Version: structureSchemaVersion}
	chapter := 1
	for volumeNumber, volume := range volumes {
		index.Volumes = append(index.Volumes, structureVolumeRef{ID: volume.ID, Number: volumeNumber + 1})
		for arcNumber, arc := range volume.Arcs {
			index.Arcs = append(index.Arcs, structureArcRef{ID: arc.ID, VolumeID: volume.ID, Number: arcNumber + 1})
			for _, entry := range arc.Chapters {
				index.Chapters = append(index.Chapters, structureChapterRef{ID: entry.ID, Number: chapter, VolumeID: volume.ID, ArcID: arc.ID})
				chapter++
			}
		}
	}
	return index
}

func structureIndexFromOutline(entries []domain.OutlineEntry) structureIndex {
	index := structureIndex{Version: structureSchemaVersion, Chapters: make([]structureChapterRef, len(entries))}
	for i, entry := range entries {
		index.Chapters[i] = structureChapterRef{ID: entry.ID, Number: i + 1}
	}
	return index
}

func structureIndexFromAdaptation(plan *domain.AdaptationPlan) structureIndex {
	index := structureIndex{Version: structureSchemaVersion}
	if plan == nil {
		return index
	}
	for i, volume := range plan.Volumes {
		index.Volumes = append(index.Volumes, structureVolumeRef{ID: volume.ID, Number: i + 1})
	}
	for i, chapter := range plan.Chapters {
		ref := structureChapterRef{ID: chapter.OutlineEntry.ID, Number: i + 1}
		for _, volume := range plan.Volumes {
			if i+1 >= volume.TargetFrom && i+1 <= volume.TargetTo {
				ref.VolumeID = volume.ID
				break
			}
		}
		index.Chapters = append(index.Chapters, ref)
	}
	return structureIndexWithImplicitVolumeArcs(index)
}

func structureIndexWithImplicitVolumeArcs(index structureIndex) structureIndex {
	for _, volume := range index.Volumes {
		hasArc := false
		for _, arc := range index.Arcs {
			if arc.VolumeID == volume.ID {
				hasArc = true
				break
			}
		}
		if hasArc {
			continue
		}

		hasChapter := false
		for _, chapter := range index.Chapters {
			if chapter.VolumeID == volume.ID {
				hasChapter = true
				break
			}
		}
		if !hasChapter {
			continue
		}

		arcID := domain.LegacyStructureID(volume.ID, domain.StructureKindArc, "implicit-volume-arc")
		index.Arcs = append(index.Arcs, structureArcRef{ID: arcID, VolumeID: volume.ID, Number: 1})
		for chapterIndex := range index.Chapters {
			if index.Chapters[chapterIndex].VolumeID == volume.ID && index.Chapters[chapterIndex].ArcID == "" {
				index.Chapters[chapterIndex].ArcID = arcID
			}
		}
	}
	return index
}

func (index structureIndex) validate() error {
	if index.Version != structureSchemaVersion {
		return fmt.Errorf("unsupported structure index version %d", index.Version)
	}
	seen := make(map[string]struct{})
	for i, ref := range index.Volumes {
		if ref.Number != i+1 {
			return fmt.Errorf("volume order is not contiguous at %d", ref.Number)
		}
		if err := validateStructureRefID(seen, ref.ID, domain.StructureKindVolume); err != nil {
			return err
		}
	}
	volumeIDs := make(map[string]struct{}, len(index.Volumes))
	for _, ref := range index.Volumes {
		volumeIDs[ref.ID] = struct{}{}
	}
	arcIDs := make(map[string]struct{}, len(index.Arcs))
	arcOrder := make(map[string]int)
	for _, ref := range index.Arcs {
		if _, ok := volumeIDs[ref.VolumeID]; !ok {
			return fmt.Errorf("arc %s references missing volume %s", ref.ID, ref.VolumeID)
		}
		arcOrder[ref.VolumeID]++
		if ref.Number != arcOrder[ref.VolumeID] {
			return fmt.Errorf("arc order is not contiguous in volume %s", ref.VolumeID)
		}
		if err := validateStructureRefID(seen, ref.ID, domain.StructureKindArc); err != nil {
			return err
		}
		arcIDs[ref.ID] = struct{}{}
	}
	for i, ref := range index.Chapters {
		if ref.Number != i+1 {
			return fmt.Errorf("chapter order is not contiguous at %d", ref.Number)
		}
		if ref.VolumeID != "" {
			if _, ok := volumeIDs[ref.VolumeID]; !ok {
				return fmt.Errorf("chapter %s references missing volume %s", ref.ID, ref.VolumeID)
			}
		}
		if ref.ArcID != "" {
			if _, ok := arcIDs[ref.ArcID]; !ok {
				return fmt.Errorf("chapter %s references missing arc %s", ref.ID, ref.ArcID)
			}
		}
		if err := validateStructureRefID(seen, ref.ID, domain.StructureKindChapter); err != nil {
			return err
		}
	}
	return nil
}

func validateStructureRefID(seen map[string]struct{}, id, kind string) error {
	if !validStructureID(id, kind) {
		return fmt.Errorf("invalid %s ID %q", kind, id)
	}
	if _, exists := seen[id]; exists {
		return fmt.Errorf("duplicate structure ID %q", id)
	}
	seen[id] = struct{}{}
	return nil
}

func (index structureIndex) chapterID(number int) (string, bool) {
	if number <= 0 || number > len(index.Chapters) {
		return "", false
	}
	return index.Chapters[number-1].ID, true
}

func (index structureIndex) chapterRef(number int) (structureChapterRef, bool) {
	if number <= 0 || number > len(index.Chapters) {
		return structureChapterRef{}, false
	}
	return index.Chapters[number-1], true
}

func (index structureIndex) chapterRefByID(id string) (structureChapterRef, bool) {
	for _, ref := range index.Chapters {
		if ref.ID == id {
			return ref, true
		}
	}
	return structureChapterRef{}, false
}

func (index structureIndex) chapterNumber(id string) (int, bool) {
	for _, ref := range index.Chapters {
		if ref.ID == id {
			return ref.Number, true
		}
	}
	return 0, false
}

func (index structureIndex) volumeNumber(id string) (int, bool) {
	for _, ref := range index.Volumes {
		if ref.ID == id {
			return ref.Number, true
		}
	}
	return 0, false
}

func (index structureIndex) volumeRef(number int) (structureVolumeRef, bool) {
	if number <= 0 || number > len(index.Volumes) {
		return structureVolumeRef{}, false
	}
	return index.Volumes[number-1], true
}

func (index structureIndex) arcRef(volume, arc int) (structureArcRef, bool) {
	volumeRef, ok := index.volumeRef(volume)
	if !ok {
		return structureArcRef{}, false
	}
	for _, ref := range index.Arcs {
		if ref.VolumeID == volumeRef.ID && ref.Number == arc {
			return ref, true
		}
	}
	return structureArcRef{}, false
}

func (index structureIndex) arcNumber(id string) (int, bool) {
	for _, ref := range index.Arcs {
		if ref.ID == id {
			return ref.Number, true
		}
	}
	return 0, false
}

func (index structureIndex) arcRefByID(id string) (structureArcRef, bool) {
	for _, ref := range index.Arcs {
		if ref.ID == id {
			return ref, true
		}
	}
	return structureArcRef{}, false
}

func canonicalizeChapterPlan(data []byte, _ structureIndex, ref structureChapterRef) ([]byte, error) {
	var value domain.ChapterPlan
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	value.Chapter = 0
	return json.MarshalIndent(canonicalChapterPlan{ChapterID: ref.ID, Plan: value}, "", "  ")
}

func projectChapterPlan(data []byte, _ structureIndex, ref structureChapterRef) ([]byte, error) {
	var value canonicalChapterPlan
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if value.ChapterID != ref.ID {
		return nil, fmt.Errorf("chapter plan identity mismatch: %s != %s", value.ChapterID, ref.ID)
	}
	value.Plan.Chapter = ref.Number
	return json.MarshalIndent(value.Plan, "", "  ")
}

func canonicalizeChapterSummary(data []byte, _ structureIndex, ref structureChapterRef) ([]byte, error) {
	var value domain.ChapterSummary
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	value.Chapter = 0
	return json.MarshalIndent(canonicalChapterSummary{ChapterID: ref.ID, Summary: value}, "", "  ")
}

func projectChapterSummary(data []byte, _ structureIndex, ref structureChapterRef) ([]byte, error) {
	var value canonicalChapterSummary
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if value.ChapterID != ref.ID {
		return nil, fmt.Errorf("chapter summary identity mismatch: %s != %s", value.ChapterID, ref.ID)
	}
	value.Summary.Chapter = ref.Number
	return json.MarshalIndent(value.Summary, "", "  ")
}

func canonicalizeChapterReview(data []byte, index structureIndex, ref structureChapterRef) ([]byte, error) {
	var value domain.ReviewEntry
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	canonical := canonicalChapterReview{ChapterID: ref.ID, Review: value}
	canonical.VolumeID, canonical.ArcID = ref.VolumeID, ref.ArcID
	var err error
	canonical.AffectedChapterIDs, err = canonicalizeChapterNumbers(index, value.AffectedChapters, "review affected chapter")
	if err != nil {
		return nil, err
	}
	if value.BatchFrom > 0 {
		canonical.BatchFromID, err = requiredChapterID(index, value.BatchFrom, "review batch_from")
		if err != nil {
			return nil, err
		}
	}
	if value.BatchTo > 0 {
		canonical.BatchToID, err = requiredChapterID(index, value.BatchTo, "review batch_to")
		if err != nil {
			return nil, err
		}
	}
	canonical.Review.Chapter, canonical.Review.Volume, canonical.Review.Arc = 0, 0, 0
	canonical.Review.BatchFrom, canonical.Review.BatchTo = 0, 0
	canonical.Review.AffectedChapters = nil
	return json.MarshalIndent(canonical, "", "  ")
}

func projectChapterReview(data []byte, index structureIndex, ref structureChapterRef) ([]byte, error) {
	var value canonicalChapterReview
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if value.ChapterID != ref.ID {
		return nil, fmt.Errorf("chapter review identity mismatch: %s != %s", value.ChapterID, ref.ID)
	}
	value.Review.Chapter = ref.Number
	if value.VolumeID != "" {
		value.Review.Volume, _ = index.volumeNumber(value.VolumeID)
	}
	if value.ArcID != "" {
		value.Review.Arc, _ = index.arcNumber(value.ArcID)
	}
	if value.BatchFromID != "" {
		value.Review.BatchFrom, _ = index.chapterNumber(value.BatchFromID)
	}
	if value.BatchToID != "" {
		value.Review.BatchTo, _ = index.chapterNumber(value.BatchToID)
	}
	for _, id := range value.AffectedChapterIDs {
		number, ok := index.chapterNumber(id)
		if !ok {
			return nil, fmt.Errorf("review references missing chapter %s", id)
		}
		value.Review.AffectedChapters = append(value.Review.AffectedChapters, number)
	}
	return json.MarshalIndent(value.Review, "", "  ")
}

func canonicalizeArcBatchReview(review domain.ReviewEntry, index structureIndex) (canonicalChapterReview, error) {
	chapter, ok := index.chapterRef(review.Chapter)
	if !ok {
		return canonicalChapterReview{}, fmt.Errorf("arc-batch review references chapter %d outside structure", review.Chapter)
	}
	volume, ok := index.volumeRef(review.Volume)
	if !ok {
		return canonicalChapterReview{}, fmt.Errorf("arc-batch review references volume %d outside structure", review.Volume)
	}
	arc, ok := index.arcRef(review.Volume, review.Arc)
	if !ok {
		return canonicalChapterReview{}, fmt.Errorf("arc-batch review references V%d A%d outside structure", review.Volume, review.Arc)
	}
	if chapter.VolumeID != volume.ID || chapter.ArcID != arc.ID {
		return canonicalChapterReview{}, fmt.Errorf("arc-batch review chapter %d is not in V%d A%d", review.Chapter, review.Volume, review.Arc)
	}
	fromID, err := requiredChapterID(index, review.BatchFrom, "arc-batch review batch_from")
	if err != nil {
		return canonicalChapterReview{}, err
	}
	toID, err := requiredChapterID(index, review.BatchTo, "arc-batch review batch_to")
	if err != nil {
		return canonicalChapterReview{}, err
	}
	affected, err := canonicalizeChapterNumbers(index, review.AffectedChapters, "arc-batch review affected chapter")
	if err != nil {
		return canonicalChapterReview{}, err
	}
	canonical := canonicalChapterReview{
		ChapterID:          chapter.ID,
		VolumeID:           volume.ID,
		ArcID:              arc.ID,
		BatchFromID:        fromID,
		BatchToID:          toID,
		AffectedChapterIDs: affected,
		Review:             review,
	}
	canonical.Review.Chapter = 0
	canonical.Review.Volume = 0
	canonical.Review.Arc = 0
	canonical.Review.BatchFrom = 0
	canonical.Review.BatchTo = 0
	canonical.Review.AffectedChapters = nil
	return canonical, nil
}

func projectArcBatchReview(canonical canonicalChapterReview, index structureIndex) (domain.ReviewEntry, error) {
	chapter, ok := index.chapterRefByID(canonical.ChapterID)
	if !ok {
		return domain.ReviewEntry{}, fmt.Errorf("arc-batch review references missing chapter %s", canonical.ChapterID)
	}
	volumeNumber, ok := index.volumeNumber(canonical.VolumeID)
	if !ok {
		return domain.ReviewEntry{}, fmt.Errorf("arc-batch review references missing volume %s", canonical.VolumeID)
	}
	arc, ok := index.arcRefByID(canonical.ArcID)
	if !ok || arc.VolumeID != canonical.VolumeID {
		return domain.ReviewEntry{}, fmt.Errorf("arc-batch review references missing arc %s", canonical.ArcID)
	}
	from, ok := index.chapterNumber(canonical.BatchFromID)
	if !ok {
		return domain.ReviewEntry{}, fmt.Errorf("arc-batch review references missing batch start %s", canonical.BatchFromID)
	}
	to, ok := index.chapterNumber(canonical.BatchToID)
	if !ok {
		return domain.ReviewEntry{}, fmt.Errorf("arc-batch review references missing batch end %s", canonical.BatchToID)
	}
	review := canonical.Review
	review.Chapter = chapter.Number
	review.Volume = volumeNumber
	review.Arc = arc.Number
	review.BatchFrom = from
	review.BatchTo = to
	for _, id := range canonical.AffectedChapterIDs {
		number, ok := index.chapterNumber(id)
		if !ok {
			return domain.ReviewEntry{}, fmt.Errorf("arc-batch review references missing affected chapter %s", id)
		}
		review.AffectedChapters = append(review.AffectedChapters, number)
	}
	return review, nil
}

func canonicalizeAdaptationCheck(data []byte, _ structureIndex, ref structureChapterRef) ([]byte, error) {
	var value domain.AdaptationCheck
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	value.Chapter = 0
	return json.MarshalIndent(canonicalAdaptationCheck{ChapterID: ref.ID, Check: value}, "", "  ")
}

func projectAdaptationCheck(data []byte, _ structureIndex, ref structureChapterRef) ([]byte, error) {
	var value canonicalAdaptationCheck
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if value.ChapterID != ref.ID {
		return nil, fmt.Errorf("adaptation check identity mismatch: %s != %s", value.ChapterID, ref.ID)
	}
	value.Check.Chapter = ref.Number
	return json.MarshalIndent(value.Check, "", "  ")
}

func loadLegacyCanonicalFacts(io *IO, index structureIndex) (canonicalFacts, bool, error) {
	var facts canonicalFacts
	found := false
	var timeline []domain.TimelineEvent
	if ok, err := readOptionalJSON(io, "timeline.json", &timeline); err != nil {
		return facts, false, err
	} else if ok {
		found = true
		for _, event := range timeline {
			id, err := optionalChapterID(index, event.Chapter, "timeline event")
			if err != nil {
				return facts, false, err
			}
			event.Chapter = 0
			facts.Timeline = append(facts.Timeline, canonicalTimelineEvent{ChapterID: id, Event: event})
		}
	}
	var foreshadow []domain.ForeshadowEntry
	if ok, err := readOptionalJSON(io, "foreshadow_ledger.json", &foreshadow); err != nil {
		return facts, false, err
	} else if ok {
		found = true
		for _, entry := range foreshadow {
			planted, err := optionalChapterID(index, entry.PlantedAt, "foreshadow planted_at")
			if err != nil {
				return facts, false, err
			}
			resolved, err := optionalChapterID(index, entry.ResolvedAt, "foreshadow resolved_at")
			if err != nil {
				return facts, false, err
			}
			entry.PlantedAt, entry.ResolvedAt = 0, 0
			facts.Foreshadow = append(facts.Foreshadow, canonicalForeshadowEntry{PlantedChapterID: planted, ResolvedChapterID: resolved, Entry: entry})
		}
	}
	var relationships []domain.RelationshipEntry
	if ok, err := readOptionalJSON(io, "relationship_state.json", &relationships); err != nil {
		return facts, false, err
	} else if ok {
		found = true
		for _, entry := range relationships {
			id, err := optionalChapterID(index, entry.Chapter, "relationship chapter")
			if err != nil {
				return facts, false, err
			}
			entry.Chapter = 0
			facts.Relationships = append(facts.Relationships, canonicalRelationshipEntry{ChapterID: id, Entry: entry})
		}
	}
	var changes []domain.StateChange
	if ok, err := readOptionalJSON(io, "meta/state_changes.json", &changes); err != nil {
		return facts, false, err
	} else if ok {
		found = true
		for _, change := range changes {
			id, err := optionalChapterID(index, change.Chapter, "state change chapter")
			if err != nil {
				return facts, false, err
			}
			change.Chapter = 0
			facts.StateChanges = append(facts.StateChanges, canonicalStateChange{ChapterID: id, Change: change})
		}
	}
	return facts, found, nil
}

func projectCanonicalFacts(facts canonicalFacts, index structureIndex) ([]domain.TimelineEvent, []domain.ForeshadowEntry, []domain.RelationshipEntry, []domain.StateChange, error) {
	timeline := make([]domain.TimelineEvent, 0, len(facts.Timeline))
	for _, item := range facts.Timeline {
		if item.ChapterID != "" {
			item.Event.Chapter = optionalChapterNumber(index, item.ChapterID)
		}
		timeline = append(timeline, item.Event)
	}
	foreshadow := make([]domain.ForeshadowEntry, 0, len(facts.Foreshadow))
	for _, item := range facts.Foreshadow {
		if item.PlantedChapterID != "" {
			item.Entry.PlantedAt = optionalChapterNumber(index, item.PlantedChapterID)
		}
		if item.ResolvedChapterID != "" {
			item.Entry.ResolvedAt = optionalChapterNumber(index, item.ResolvedChapterID)
		}
		foreshadow = append(foreshadow, item.Entry)
	}
	relationships := make([]domain.RelationshipEntry, 0, len(facts.Relationships))
	for _, item := range facts.Relationships {
		if item.ChapterID != "" {
			item.Entry.Chapter = optionalChapterNumber(index, item.ChapterID)
		}
		relationships = append(relationships, item.Entry)
	}
	changes := make([]domain.StateChange, 0, len(facts.StateChanges))
	for _, item := range facts.StateChanges {
		if item.ChapterID != "" {
			item.Change.Chapter = optionalChapterNumber(index, item.ChapterID)
		}
		changes = append(changes, item.Change)
	}
	return timeline, foreshadow, relationships, changes, nil
}

func readOptionalJSON(io *IO, rel string, value any) (bool, error) {
	if err := io.ReadJSON(rel, value); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func canonicalizeChapterNumbers(index structureIndex, numbers []int, context string) ([]string, error) {
	ids := make([]string, 0, len(numbers))
	for _, number := range numbers {
		id, err := requiredChapterID(index, number, context)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func requiredChapterID(index structureIndex, number int, context string) (string, error) {
	id, ok := index.chapterID(number)
	if !ok {
		return "", fmt.Errorf("%s references chapter %d outside structure", context, number)
	}
	return id, nil
}

func optionalChapterID(index structureIndex, number int, context string) (string, error) {
	if number <= 0 {
		return "", nil
	}
	return requiredChapterID(index, number, context)
}

func optionalChapterNumber(index structureIndex, id string) int {
	if id == "" {
		return 0
	}
	number, _ := index.chapterNumber(id)
	return number
}

func numericArtifactPathsNotInTarget(source, target structureIndex) []string {
	if len(source.Chapters) <= len(target.Chapters) {
		return nil
	}
	var paths []string
	for number := len(target.Chapters) + 1; number <= len(source.Chapters); number++ {
		paths = append(paths, chapterFinalRel(number), chapterDraftRel(number), chapterPlanRel(number), chapterSummaryRel(number), chapterReviewRel(number, false), chapterReviewRel(number, true), adaptationCheckRel(number))
	}
	return paths
}

func structureMigrationStageRel(operationID string) string {
	return structureMigrationStagingDir + "/" + operationID
}

func structureRequestReceiptRel(requestID string) string {
	return structureRequestReceiptDir + "/" + requestID + ".json"
}
func chapterCanonicalRel(id, name string) string {
	return structureRootDir + "/chapters/" + id + "/" + name
}
func volumeCanonicalRel(id, name string) string {
	return structureRootDir + "/volumes/" + id + "/" + name
}
func arcCanonicalRel(id, name string) string { return structureRootDir + "/arcs/" + id + "/" + name }
func arcBatchCanonicalDir(volumeID, arcID string) string {
	return structureRootDir + "/volumes/" + volumeID + "/arcs/" + arcID + "/reviews"
}
func arcBatchCanonicalRel(review canonicalChapterReview) string {
	return arcBatchCanonicalDir(review.VolumeID, review.ArcID) + "/" + review.BatchFromID + "-" + review.BatchToID + ".json"
}
func chapterFinalRel(number int) string   { return fmt.Sprintf("chapters/%02d.md", number) }
func chapterDraftRel(number int) string   { return fmt.Sprintf("drafts/%02d.draft.md", number) }
func chapterPlanRel(number int) string    { return fmt.Sprintf("drafts/%02d.plan.json", number) }
func chapterSummaryRel(number int) string { return fmt.Sprintf("summaries/%02d.json", number) }
func chapterReviewRel(number int, global bool) string {
	if global {
		return fmt.Sprintf("reviews/%02d-global.json", number)
	}
	return fmt.Sprintf("reviews/%02d.json", number)
}
func adaptationCheckRel(number int) string {
	return fmt.Sprintf("meta/adaptation/checks/%04d.json", number)
}
func volumeSummaryRel(number int) string { return fmt.Sprintf("summaries/vol-v%02d.json", number) }
func arcSummaryRel(volume, arc int) string {
	return fmt.Sprintf("summaries/arc-v%02da%02d.json", volume, arc)
}

func isScopedSummaryProjectionPath(rel string) bool {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	return strings.HasSuffix(strings.ToLower(clean), ".json") &&
		(strings.HasPrefix(clean, "summaries/vol-v") || strings.HasPrefix(clean, "summaries/arc-v"))
}

func isCanonicalStructurePath(rel string) bool {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	return clean == structureIndexFile || clean == structureFactsFile || strings.HasPrefix(clean, structureRequestReceiptDir+"/") || strings.HasPrefix(clean, structureRootDir+"/chapters/") || strings.HasPrefix(clean, structureRootDir+"/volumes/") || strings.HasPrefix(clean, structureRootDir+"/arcs/")
}

func isArcBatchCanonicalReviewPath(rel string) bool {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	return strings.HasPrefix(clean, structureRootDir+"/volumes/") && strings.Contains(clean, "/arcs/") && strings.Contains(clean, "/reviews/") && strings.HasSuffix(strings.ToLower(clean), ".json")
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func validStructureID(id, kind string) bool {
	prefix := "ch_"
	switch kind {
	case domain.StructureKindVolume:
		prefix = "vol_"
	case domain.StructureKindArc:
		prefix = "arc_"
	}
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+32 {
		return false
	}
	_, err := hex.DecodeString(id[len(prefix):])
	return err == nil
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
