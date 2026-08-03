package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	adaptationRootDir                    = "meta/adaptation"
	adaptationBackupDir                  = "meta/adaptation_backups"
	adaptationSourceChapterDir           = adaptationRootDir + "/source_chapters"
	adaptationSourceReportDir            = adaptationRootDir + "/source_reports"
	adaptationSourceReportsFile          = adaptationRootDir + "/source_reports.json"
	adaptationSourceFoundationFile       = adaptationRootDir + "/source_foundation.json"
	adaptationSourceFoundationDir        = adaptationRootDir + "/source_foundation_batches"
	adaptationCoCreateDossierFile        = adaptationRootDir + "/cocreate_dossier.json"
	adaptationCoCreateBatchDir           = adaptationRootDir + "/cocreate_dossier_batches"
	adaptationCoCreateIntentFile         = adaptationRootDir + "/cocreate_intent.json"
	adaptationCoCreateBriefingFile       = adaptationRootDir + "/cocreate_briefing.json"
	adaptationCoCreateBriefingDir        = adaptationRootDir + "/cocreate_briefing_batches"
	adaptationCheckDir                   = adaptationRootDir + "/checks"
	adaptationProposalFile               = adaptationRootDir + "/proposal.json"
	adaptationVolumeReviewFile           = adaptationRootDir + "/proposal_volume_review.json"
	adaptationProposalRuntimeFile        = adaptationRootDir + "/proposal_runtime.json"
	adaptationPlanFile                   = adaptationRootDir + "/plan.json"
	adaptationTargetFoundationReviewFile = adaptationRootDir + "/target_foundation_review.json"
	adaptationCharacterBriefFile         = adaptationRootDir + "/character_brief.json"
)

// AdaptationStore keeps source-novel snapshots and adaptation validation data.
type AdaptationStore struct {
	io                 *IO
	identity           structureIdentity
	migration          *structureMigration
	withLegacyMutation func(string, *structureMigration, func() error) error
}

func NewAdaptationStore(io *IO, identity structureIdentity, migrations ...*structureMigration) *AdaptationStore {
	var migration *structureMigration
	if len(migrations) > 0 {
		migration = migrations[0]
	}
	return &AdaptationStore{io: io, identity: identity, migration: migration}
}

func (s *AdaptationStore) Reset() error {
	return s.withLegacyFormalMutation("reset all adaptation state", func() error {
		return os.RemoveAll(s.io.path(adaptationRootDir))
	})
}

// Backup copies the current adaptation snapshot before destructive maintenance.
func (s *AdaptationStore) Backup(label string) (string, error) {
	sourceRoot := s.io.path(adaptationRootDir)
	if _, err := os.Stat(sourceRoot); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	label = safeAdaptationBackupLabel(label)
	if label == "" {
		label = "snapshot"
	}
	targetRoot := s.io.path(filepath.ToSlash(filepath.Join(
		adaptationBackupDir,
		time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+label,
	)))
	if err := copyAdaptationDir(sourceRoot, targetRoot); err != nil {
		return "", err
	}
	return targetRoot, nil
}

// LoadLatestPlanBackup returns the newest backup whose directory name ends in
// "-label". It is used only for one-time migration of legacy plans; the
// current plan remains the source of truth and callers must merge only the
// fields that the migration is allowed to change.
func (s *AdaptationStore) LoadLatestPlanBackup(label string) (*domain.AdaptationPlan, string, error) {
	if s == nil || s.io == nil {
		return nil, "", nil
	}
	label = safeAdaptationBackupLabel(label)
	if label == "" {
		return nil, "", nil
	}
	entries, err := os.ReadDir(s.io.path(adaptationBackupDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	suffix := "-" + label
	var newest os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		if newest == nil || entry.Name() > newest.Name() {
			newest = entry
		}
	}
	if newest == nil {
		return nil, "", nil
	}
	var plan domain.AdaptationPlan
	// Backup copies the contents of meta/adaptation into the backup root, so
	// plan.json is one level below the timestamped directory rather than under
	// a second meta/adaptation prefix.
	backupPlan := filepath.ToSlash(filepath.Join(adaptationBackupDir, newest.Name(), filepath.Base(adaptationPlanFile)))
	if err := s.io.ReadJSON(backupPlan, &plan); err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	s.normalizeAdaptationPlan(&plan)
	return &plan, s.io.path(filepath.ToSlash(filepath.Join(adaptationBackupDir, newest.Name()))), nil
}

// RepairLegacyArcChapterBudgetDensity expands an old confirmed arc plan
// before Writer or commit validation can reject its first dense chapter. The
// original adaptation snapshot is backed up once per repair so the migration
// remains reversible and auditable.
func (s *AdaptationStore) RepairLegacyArcChapterBudgetDensity(plan *domain.AdaptationPlan) (bool, error) {
	if s == nil || plan == nil || domain.NormalizeAdaptationGranularity(plan.Granularity) != domain.AdaptationGranularityArc {
		return false, nil
	}
	var repaired bool
	err := s.withLegacyFormalMutation("migrate legacy chapter budget density", func() error {
		issues := domain.ValidateArcChapterBudgetDensity(*plan)
		if len(issues) == 0 {
			return nil
		}
		wasPassed := domain.AdaptationOutlineQualityPassed(*plan)
		if _, err := s.Backup("auto-budget-density-repair"); err != nil {
			return fmt.Errorf("backup adaptation before automatic budget repair: %w", err)
		}
		if len(domain.RepairArcChapterBudgetDensity(plan)) == 0 {
			return nil
		}
		chapters := make([]int, 0, len(issues))
		for _, issue := range issues {
			chapters = append(chapters, issue.Chapter)
		}
		domain.MarkAdaptationBudgetRepair(plan, "deterministic_fallback", 0, chapters, "writer/commit safety-net fallback after preflight was missed")
		if wasPassed {
			domain.MarkAdaptationOutlineQualityPassed(plan)
		}
		if err := s.savePlan(*plan); err != nil {
			return fmt.Errorf("save adaptation after automatic budget repair: %w", err)
		}
		repaired = true
		return nil
	})
	return repaired, err
}

// ResetGenerated removes adaptation artifacts derived from a confirmed brief
// while preserving the analyzed source-novel snapshot.
func (s *AdaptationStore) ResetGenerated() error {
	return s.withLegacyFormalMutation("reset generated adaptation state", func() error {
		if err := s.io.WithWriteLock(func() error {
			err := os.Remove(s.io.path(adaptationPlanFile))
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			err = os.Remove(s.io.path(adaptationProposalFile))
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			err = os.Remove(s.io.path(adaptationVolumeReviewFile))
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			err = s.io.RemoveFileUnlocked(adaptationProposalRuntimeFile)
			if err != nil {
				return err
			}
			err = s.io.RemoveFileUnlocked(adaptationPlanningWorkflowFile)
			if err != nil {
				return err
			}
			return os.RemoveAll(s.io.path(adaptationCheckDir))
		}); err != nil {
			return err
		}
		return s.clearCanonicalChecks()
	})
}

// ResetConfirmedArtifactsForProposal clears output from an older confirmed
// adaptation run without deleting the proposal, target-Foundation review, or
// planning workflow that authenticate the proposal currently being confirmed.
func (s *AdaptationStore) ResetConfirmedArtifactsForProposal() error {
	return s.withLegacyFormalMutation("reset confirmed adaptation artifacts for proposal", func() error {
		if err := s.io.WithWriteLock(func() error {
			if err := s.io.RemoveFileUnlocked(adaptationPlanFile); err != nil {
				return err
			}
			return os.RemoveAll(s.io.path(adaptationCheckDir))
		}); err != nil {
			return err
		}
		return s.clearCanonicalChecks()
	})
}

func (s *AdaptationStore) clearCanonicalChecks() error {
	if s.migration == nil {
		return nil
	}
	return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
		if !migrated {
			return nil
		}
		return s.io.WithWriteLock(func() error {
			for _, ref := range index.Chapters {
				if err := s.io.RemoveFileUnlocked(chapterCanonicalRel(ref.ID, "adaptation-check.json")); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func (s *AdaptationStore) SaveSourceManifest(manifest domain.AdaptationSourceManifest) error {
	return s.withLegacyFormalMutation("replace immutable source manifest", func() error {
		return s.io.WriteJSON(adaptationRootDir+"/source_manifest.json", manifest)
	})
}

func (s *AdaptationStore) LoadSourceManifest() (*domain.AdaptationSourceManifest, error) {
	var manifest domain.AdaptationSourceManifest
	if err := s.io.ReadJSON(adaptationRootDir+"/source_manifest.json", &manifest); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &manifest, nil
}

func (s *AdaptationStore) SaveSourceChapter(chapter int, title, content string) (domain.AdaptationSource, error) {
	if chapter <= 0 {
		return domain.AdaptationSource{}, fmt.Errorf("chapter must be > 0")
	}
	rel := SourceChapterRelPath(chapter)
	content = strings.TrimSpace(content)
	if err := s.withLegacyFormalMutation("replace immutable source chapter", func() error {
		return s.io.WriteMarkdown(rel, content)
	}); err != nil {
		return domain.AdaptationSource{}, err
	}
	return domain.AdaptationSource{
		Chapter: chapter,
		Title:   strings.TrimSpace(title),
		SHA256:  TextSHA256(content),
		Path:    rel,
		Runes:   utf8.RuneCountInString(content),
	}, nil
}

func (s *AdaptationStore) LoadSourceChapter(chapter int) (string, *domain.AdaptationSource, error) {
	if chapter <= 0 {
		return "", nil, fmt.Errorf("chapter must be > 0")
	}
	manifest, err := s.LoadSourceManifest()
	if err != nil {
		return "", nil, err
	}
	if manifest == nil {
		return "", nil, nil
	}

	var source *domain.AdaptationSource
	for i := range manifest.Chapters {
		if manifest.Chapters[i].Chapter == chapter {
			source = &manifest.Chapters[i]
			break
		}
	}
	if source == nil {
		return "", nil, nil
	}

	rel := source.Path
	if strings.TrimSpace(rel) == "" {
		rel = SourceChapterRelPath(chapter)
	}
	data, err := s.io.ReadFile(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return "", source, nil
		}
		return "", nil, err
	}
	return string(data), source, nil
}

func (s *AdaptationStore) LoadSourceChapterRange(from, to, maxRunes int) (map[int]string, error) {
	if from <= 0 || to < from {
		return nil, fmt.Errorf("invalid source chapter range %d-%d", from, to)
	}
	result := make(map[int]string)
	for ch := from; ch <= to; ch++ {
		text, _, err := s.LoadSourceChapter(ch)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		result[ch] = truncateRunes(text, maxRunes)
	}
	return result, nil
}

func (s *AdaptationStore) SaveSourceReports(reports []domain.AdaptationSourceReport) error {
	return s.withLegacyFormalMutation("save source reports", func() error {
		return s.io.WriteJSON(adaptationSourceReportsFile, reports)
	})
}

func (s *AdaptationStore) SaveSourceReport(report domain.AdaptationSourceReport) error {
	if report.Chapter <= 0 {
		return fmt.Errorf("chapter must be > 0")
	}
	return s.withLegacyFormalMutation("save source report", func() error {
		return s.io.WriteJSON(SourceReportRelPath(report.Chapter), report)
	})
}

func (s *AdaptationStore) LoadSourceReport(chapter int) (*domain.AdaptationSourceReport, error) {
	if chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0")
	}
	var report domain.AdaptationSourceReport
	if err := s.io.ReadJSON(SourceReportRelPath(chapter), &report); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if report.Chapter == 0 {
		report.Chapter = chapter
	}
	return &report, nil
}

func (s *AdaptationStore) LoadSourceReports() ([]domain.AdaptationSourceReport, error) {
	dirReports, err := s.loadSourceReportDir()
	if err != nil {
		return nil, err
	}
	if len(dirReports) > 0 {
		return dirReports, nil
	}

	var reports []domain.AdaptationSourceReport
	if err := s.io.ReadJSON(adaptationSourceReportsFile, &reports); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return reports, nil
}

func (s *AdaptationStore) LoadCompleteSourceReports() ([]domain.AdaptationSourceReport, error) {
	manifest, err := s.LoadSourceManifest()
	if err != nil || manifest == nil {
		return nil, err
	}
	if manifest.ChapterCount <= 0 || len(manifest.Chapters) != manifest.ChapterCount {
		return nil, nil
	}

	reports := make([]domain.AdaptationSourceReport, 0, len(manifest.Chapters))
	for _, source := range manifest.Chapters {
		report, err := s.LoadSourceReport(source.Chapter)
		if err != nil || report == nil {
			return nil, err
		}
		if !sourceReportMatches(*report, source.SHA256) {
			return nil, nil
		}
		reports = append(reports, *report)
	}
	return reports, nil
}

func (s *AdaptationStore) loadSourceReportDir() ([]domain.AdaptationSourceReport, error) {
	entries, err := os.ReadDir(s.io.path(adaptationSourceReportDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	reports := make([]domain.AdaptationSourceReport, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		var report domain.AdaptationSourceReport
		rel := adaptationSourceReportDir + "/" + entry.Name()
		if err := s.io.ReadJSON(rel, &report); err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	sort.SliceStable(reports, func(i, j int) bool {
		return reports[i].Chapter < reports[j].Chapter
	})
	return reports, nil
}

func sourceReportMatches(report domain.AdaptationSourceReport, sha256 string) bool {
	return strings.TrimSpace(report.SourceSHA256) != "" &&
		report.SourceSHA256 == sha256 &&
		strings.TrimSpace(report.Summary) != "" &&
		len(report.KeyEvents) > 0
}

func (s *AdaptationStore) SaveSourceFoundation(foundation domain.AdaptationSourceFoundation) error {
	return s.withLegacyFormalMutation("save source foundation", func() error {
		return s.io.WithWriteLock(func() error {
			if err := s.io.WriteJSONUnlocked(adaptationSourceFoundationFile, foundation); err != nil {
				return err
			}
			return os.RemoveAll(s.io.path(adaptationSourceFoundationDir))
		})
	})
}

func (s *AdaptationStore) LoadSourceFoundation() (*domain.AdaptationSourceFoundation, error) {
	var foundation domain.AdaptationSourceFoundation
	if err := s.io.ReadJSON(adaptationSourceFoundationFile, &foundation); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &foundation, nil
}

func (s *AdaptationStore) SaveSourceFoundationBatch(batch domain.AdaptationSourceFoundationBatch) error {
	if batch.Level < 0 {
		return fmt.Errorf("batch level must be >= 0")
	}
	if batch.Index <= 0 {
		return fmt.Errorf("batch index must be > 0")
	}
	return s.withLegacyFormalMutation("save source foundation batch", func() error {
		return s.io.WriteJSON(SourceFoundationBatchRelPath(batch.Level, batch.Index), batch)
	})
}

func (s *AdaptationStore) LoadSourceFoundationBatch(level, index int) (*domain.AdaptationSourceFoundationBatch, error) {
	if level < 0 {
		return nil, fmt.Errorf("batch level must be >= 0")
	}
	if index <= 0 {
		return nil, fmt.Errorf("batch index must be > 0")
	}
	var batch domain.AdaptationSourceFoundationBatch
	if err := s.io.ReadJSON(SourceFoundationBatchRelPath(level, index), &batch); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if batch.Level == 0 && level > 0 {
		batch.Level = level
	}
	if batch.Index == 0 {
		batch.Index = index
	}
	return &batch, nil
}

func (s *AdaptationStore) ClearSourceFoundationBatches() error {
	return s.withLegacyFormalMutation("clear source foundation batches", func() error {
		return s.io.WithWriteLock(func() error { return os.RemoveAll(s.io.path(adaptationSourceFoundationDir)) })
	})
}

func (s *AdaptationStore) SaveCoCreateDossier(dossier domain.AdaptationCoCreateDossier) error {
	return s.withLegacyFormalMutation("save adaptation co-create dossier", func() error { return s.io.WriteJSON(adaptationCoCreateDossierFile, dossier) })
}

func (s *AdaptationStore) LoadCoCreateDossier() (*domain.AdaptationCoCreateDossier, error) {
	var dossier domain.AdaptationCoCreateDossier
	if err := s.io.ReadJSON(adaptationCoCreateDossierFile, &dossier); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &dossier, nil
}

func (s *AdaptationStore) SaveCoCreateDossierBatch(batch domain.AdaptationCoCreateDossierBatch) error {
	if batch.Index <= 0 {
		return fmt.Errorf("batch index must be > 0")
	}
	return s.withLegacyFormalMutation("save adaptation co-create dossier batch", func() error { return s.io.WriteJSON(CoCreateDossierBatchRelPath(batch.Index), batch) })
}

func (s *AdaptationStore) LoadCoCreateDossierBatch(index int) (*domain.AdaptationCoCreateDossierBatch, error) {
	if index <= 0 {
		return nil, fmt.Errorf("batch index must be > 0")
	}
	var batch domain.AdaptationCoCreateDossierBatch
	if err := s.io.ReadJSON(CoCreateDossierBatchRelPath(index), &batch); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if batch.Index == 0 {
		batch.Index = index
	}
	return &batch, nil
}

func (s *AdaptationStore) LoadCoCreateDossierBatches() ([]domain.AdaptationCoCreateDossierBatch, error) {
	entries, err := os.ReadDir(s.io.path(adaptationCoCreateBatchDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	batches := make([]domain.AdaptationCoCreateDossierBatch, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		var batch domain.AdaptationCoCreateDossierBatch
		rel := adaptationCoCreateBatchDir + "/" + entry.Name()
		if err := s.io.ReadJSON(rel, &batch); err != nil {
			return nil, err
		}
		batches = append(batches, batch)
	}
	sort.SliceStable(batches, func(i, j int) bool {
		return batches[i].Index < batches[j].Index
	})
	return batches, nil
}

func (s *AdaptationStore) SaveCoCreateIntent(intent domain.AdaptationCoCreateIntent) error {
	return s.withLegacyFormalMutation("save adaptation co-create intent", func() error { return s.io.WriteJSON(adaptationCoCreateIntentFile, intent) })
}

func (s *AdaptationStore) LoadCoCreateIntent() (*domain.AdaptationCoCreateIntent, error) {
	var intent domain.AdaptationCoCreateIntent
	if err := s.io.ReadJSON(adaptationCoCreateIntentFile, &intent); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &intent, nil
}

func (s *AdaptationStore) SaveCharacterBrief(brief domain.AdaptationCharacterBrief) error {
	brief.Brief = strings.TrimSpace(brief.Brief)
	brief.SourceSignature = strings.TrimSpace(brief.SourceSignature)
	brief.IntentHash = strings.TrimSpace(brief.IntentHash)
	brief.CoreCastSignature = strings.TrimSpace(brief.CoreCastSignature)
	if brief.Version != 1 || brief.Brief == "" || len(brief.SourceSignature) != 64 ||
		len(brief.IntentHash) != 64 ||
		(brief.CoreCastSignature != "" && len(brief.CoreCastSignature) != 64) {
		return fmt.Errorf("adaptation character brief is incomplete")
	}
	return s.withLegacyFormalMutation(
		"save adaptation character brief",
		func() error { return s.io.WriteJSON(adaptationCharacterBriefFile, brief) },
	)
}

func (s *AdaptationStore) LoadCharacterBrief() (*domain.AdaptationCharacterBrief, error) {
	var brief domain.AdaptationCharacterBrief
	if err := s.io.ReadJSON(adaptationCharacterBriefFile, &brief); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	brief.Brief = strings.TrimSpace(brief.Brief)
	brief.SourceSignature = strings.TrimSpace(brief.SourceSignature)
	brief.IntentHash = strings.TrimSpace(brief.IntentHash)
	brief.CoreCastSignature = strings.TrimSpace(brief.CoreCastSignature)
	if brief.Version != 1 || brief.Brief == "" || len(brief.SourceSignature) != 64 ||
		len(brief.IntentHash) != 64 ||
		(brief.CoreCastSignature != "" && len(brief.CoreCastSignature) != 64) {
		return nil, fmt.Errorf("adaptation character brief is incomplete")
	}
	return &brief, nil
}

func (s *AdaptationStore) SaveCoCreateBriefing(briefing domain.AdaptationCoCreateBriefing) error {
	return s.withLegacyFormalMutation("save adaptation co-create briefing", func() error { return s.io.WriteJSON(adaptationCoCreateBriefingFile, briefing) })
}

func (s *AdaptationStore) LoadCoCreateBriefing() (*domain.AdaptationCoCreateBriefing, error) {
	var briefing domain.AdaptationCoCreateBriefing
	if err := s.io.ReadJSON(adaptationCoCreateBriefingFile, &briefing); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &briefing, nil
}

func (s *AdaptationStore) SaveCoCreateBriefingBatch(batch domain.AdaptationCoCreateBriefingBatch) error {
	if batch.Index <= 0 {
		return fmt.Errorf("batch index must be > 0")
	}
	return s.withLegacyFormalMutation("save adaptation co-create briefing batch", func() error { return s.io.WriteJSON(CoCreateBriefingBatchRelPath(batch.Index), batch) })
}

func (s *AdaptationStore) LoadCoCreateBriefingBatch(index int) (*domain.AdaptationCoCreateBriefingBatch, error) {
	if index <= 0 {
		return nil, fmt.Errorf("batch index must be > 0")
	}
	var batch domain.AdaptationCoCreateBriefingBatch
	if err := s.io.ReadJSON(CoCreateBriefingBatchRelPath(index), &batch); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if batch.Index == 0 {
		batch.Index = index
	}
	return &batch, nil
}

func (s *AdaptationStore) LoadCoCreateBriefingBatches() ([]domain.AdaptationCoCreateBriefingBatch, error) {
	entries, err := os.ReadDir(s.io.path(adaptationCoCreateBriefingDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	batches := make([]domain.AdaptationCoCreateBriefingBatch, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		var batch domain.AdaptationCoCreateBriefingBatch
		rel := adaptationCoCreateBriefingDir + "/" + entry.Name()
		if err := s.io.ReadJSON(rel, &batch); err != nil {
			return nil, err
		}
		batches = append(batches, batch)
	}
	sort.SliceStable(batches, func(i, j int) bool {
		return batches[i].Index < batches[j].Index
	})
	return batches, nil
}

func (s *AdaptationStore) CoCreateBriefingCurrent(promptVersion string, dossierPromptVersion string, intentHash string) (bool, error) {
	manifest, err := s.LoadSourceManifest()
	if err != nil || manifest == nil {
		return false, err
	}
	dossier, err := s.LoadCoCreateDossier()
	if err != nil || dossier == nil {
		return false, err
	}
	briefing, err := s.LoadCoCreateBriefing()
	if err != nil || briefing == nil {
		return false, err
	}
	return CoCreateBriefingMatches(*briefing, *manifest, *dossier, promptVersion, dossierPromptVersion, intentHash), nil
}

func CoCreateBriefingMatches(
	briefing domain.AdaptationCoCreateBriefing,
	manifest domain.AdaptationSourceManifest,
	dossier domain.AdaptationCoCreateDossier,
	promptVersion string,
	dossierPromptVersion string,
	intentHash string,
) bool {
	if strings.TrimSpace(briefing.PromptVersion) != strings.TrimSpace(promptVersion) {
		return false
	}
	if strings.TrimSpace(briefing.DossierPromptVersion) != strings.TrimSpace(dossierPromptVersion) {
		return false
	}
	if strings.TrimSpace(briefing.IntentHash) != strings.TrimSpace(intentHash) {
		return false
	}
	if briefing.SourceChapterCount != manifest.ChapterCount {
		return false
	}
	if briefing.SourceSignature != AdaptationSourceSignature(manifest) {
		return false
	}
	if briefing.DossierBatchCount != len(dossier.Batches) {
		return false
	}
	return true
}

func (s *AdaptationStore) ResolveCoCreateBriefingDecision(decisionID, optionID, customAnswer string) (*domain.AdaptationCoCreateBriefing, error) {
	return s.ResolveCoCreateBriefingDecisions([]domain.AdaptationResolvedDecision{{
		DecisionID:   decisionID,
		OptionID:     optionID,
		CustomAnswer: customAnswer,
	}})
}

func (s *AdaptationStore) ResolveCoCreateBriefingDecisions(decisions []domain.AdaptationResolvedDecision) (*domain.AdaptationCoCreateBriefing, error) {
	if len(decisions) == 0 {
		return nil, fmt.Errorf("decisions are required")
	}
	briefing, err := s.LoadCoCreateBriefing()
	if err != nil {
		return nil, err
	}
	if briefing == nil {
		return nil, fmt.Errorf("co-create briefing is required")
	}
	next := *briefing
	next.Decisions = append([]domain.AdaptationBriefingDecision(nil), briefing.Decisions...)
	next.ResolvedDecisions = append([]domain.AdaptationResolvedDecision(nil), briefing.ResolvedDecisions...)
	for _, item := range decisions {
		decisionID := strings.TrimSpace(item.DecisionID)
		optionID := strings.TrimSpace(item.OptionID)
		customAnswer := strings.TrimSpace(item.CustomAnswer)
		if decisionID == "" {
			return nil, fmt.Errorf("decision_id is required")
		}
		if optionID == "" && customAnswer == "" {
			return nil, fmt.Errorf("option_id or custom_answer is required")
		}
		decision := findBriefingDecision(next, decisionID)
		if decision == nil {
			return nil, fmt.Errorf("decision not found")
		}
		if optionID != "" && !briefingDecisionHasOption(*decision, optionID) {
			return nil, fmt.Errorf("decision option not found")
		}
		resolved := domain.AdaptationResolvedDecision{
			DecisionID:   decisionID,
			OptionID:     optionID,
			CustomAnswer: customAnswer,
			ResolvedAt:   timeNowUTCString(),
		}
		next.ResolvedDecisions = upsertResolvedDecision(next.ResolvedDecisions, resolved)
		markBriefingDecisionResolved(&next, decisionID)
	}
	if err := s.SaveCoCreateBriefing(next); err != nil {
		return nil, err
	}
	return &next, nil
}

func (s *AdaptationStore) CoCreateDossierCurrent(promptVersion string, batchSize int, batchRuneLimit ...int) (bool, error) {
	manifest, err := s.LoadSourceManifest()
	if err != nil || manifest == nil {
		return false, err
	}
	dossier, err := s.LoadCoCreateDossier()
	if err != nil || dossier == nil {
		return false, err
	}
	return CoCreateDossierMatchesManifest(*dossier, *manifest, promptVersion, batchSize, batchRuneLimit...), nil
}

func CoCreateDossierMatchesManifest(dossier domain.AdaptationCoCreateDossier, manifest domain.AdaptationSourceManifest, promptVersion string, batchSize int, batchRuneLimit ...int) bool {
	if batchSize <= 0 {
		return false
	}
	if strings.TrimSpace(dossier.PromptVersion) != strings.TrimSpace(promptVersion) {
		return false
	}
	runeLimit := optionalDossierBatchRuneLimit(batchRuneLimit)
	if runeLimit > 0 && dossier.BatchRuneLimit != runeLimit {
		return false
	}
	if dossier.BatchSize != batchSize || dossier.SourceChapterCount != manifest.ChapterCount {
		return false
	}
	if dossier.SourceSignature != AdaptationSourceSignature(manifest) {
		return false
	}
	specs := AdaptationDossierBatchSpecs(manifest, batchSize, runeLimit)
	if len(dossier.Batches) != len(specs) {
		return false
	}
	batches := append([]domain.AdaptationCoCreateDossierBatch(nil), dossier.Batches...)
	sort.SliceStable(batches, func(i, j int) bool {
		return batches[i].Index < batches[j].Index
	})
	for i, spec := range specs {
		batch := batches[i]
		if batch.Index != spec.Index || batch.SourceFrom != spec.SourceFrom || batch.SourceTo != spec.SourceTo {
			return false
		}
		if strings.TrimSpace(batch.SourceSignature) != spec.SourceSignature {
			return false
		}
	}
	return true
}

type AdaptationDossierBatchSpec struct {
	Index           int
	SourceFrom      int
	SourceTo        int
	SourceSignature string
}

func AdaptationDossierBatchSpecs(manifest domain.AdaptationSourceManifest, batchSize int, batchRuneLimit int) []AdaptationDossierBatchSpec {
	if manifest.ChapterCount <= 0 || batchSize <= 0 {
		return nil
	}
	runesByChapter := make(map[int]int, len(manifest.Chapters))
	for _, source := range manifest.Chapters {
		runesByChapter[source.Chapter] = source.Runes
	}
	specs := make([]AdaptationDossierBatchSpec, 0, adaptationDossierBatchCount(manifest.ChapterCount, batchSize))
	from := 1
	index := 1
	batchRunes := 0
	batchChapters := 0
	for chapter := 1; chapter <= manifest.ChapterCount; chapter++ {
		runes := runesByChapter[chapter]
		if runes < 0 {
			runes = 0
		}
		if batchChapters > 0 && (batchChapters >= batchSize || (batchRuneLimit > 0 && batchRunes+runes > batchRuneLimit)) {
			specs = append(specs, adaptationDossierBatchSpec(manifest, index, from, chapter-1))
			index++
			from = chapter
			batchRunes = 0
			batchChapters = 0
		}
		batchRunes += runes
		batchChapters++
	}
	if batchChapters > 0 {
		specs = append(specs, adaptationDossierBatchSpec(manifest, index, from, manifest.ChapterCount))
	}
	return specs
}

func adaptationDossierBatchSpec(manifest domain.AdaptationSourceManifest, index, from, to int) AdaptationDossierBatchSpec {
	return AdaptationDossierBatchSpec{
		Index:           index,
		SourceFrom:      from,
		SourceTo:        to,
		SourceSignature: adaptationDossierSourceRangeSignature(manifest, from, to),
	}
}

func adaptationDossierSourceRangeSignature(manifest domain.AdaptationSourceManifest, from, to int) string {
	var sources []domain.AdaptationSource
	for _, ch := range manifest.Chapters {
		if ch.Chapter >= from && ch.Chapter <= to {
			sources = append(sources, ch)
		}
	}
	return AdaptationSourceSignature(domain.AdaptationSourceManifest{
		ChapterCount: len(sources),
		Chapters:     sources,
	})
}

func optionalDossierBatchRuneLimit(values []int) int {
	if len(values) == 0 || values[0] <= 0 {
		return 0
	}
	return values[0]
}

func AdaptationSourceSignature(manifest domain.AdaptationSourceManifest) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "chapters=%d\n", manifest.ChapterCount)
	for _, ch := range manifest.Chapters {
		fmt.Fprintf(&sb, "%d:%s\n", ch.Chapter, ch.SHA256)
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

func adaptationDossierBatchCount(chapterCount, batchSize int) int {
	if chapterCount <= 0 || batchSize <= 0 {
		return 0
	}
	return (chapterCount + batchSize - 1) / batchSize
}

func (s *AdaptationStore) SavePlan(plan domain.AdaptationPlan) error {
	return s.withLegacyFormalMutation("save confirmed plan", func() error { return s.savePlan(plan) })
}

func (s *AdaptationStore) savePlan(plan domain.AdaptationPlan) error {
	cloned, err := cloneAdaptationPlanRequest(plan)
	if err != nil {
		return err
	}
	plan = cloned
	s.normalizeAdaptationPlan(&plan)
	plan.Status = domain.AdaptationPlanStatusConfirmed
	if s.migration != nil {
		return s.savePlanWithMigration(plan)
	}
	return s.io.WithWriteLock(func() error {
		var existing domain.AdaptationPlan
		existingPlan := (*domain.AdaptationPlan)(nil)
		if err := s.io.ReadJSONUnlocked(adaptationPlanFile, &existing); err == nil {
			existingPlan = &existing
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := s.identity.prepareAdaptationPlanForSave(&plan, existingPlan); err != nil {
			return err
		}
		if err := s.io.RemoveFileUnlocked(adaptationProposalRuntimeFile); err != nil {
			return err
		}
		if err := s.io.RemoveFileUnlocked(adaptationVolumeReviewFile); err != nil {
			return err
		}
		if err := s.io.WriteJSONUnlocked(adaptationPlanFile, plan); err != nil {
			return err
		}
		return s.writePlanningWorkflowStageUnlocked(domain.AdaptationPlanningStageConfirmed)
	})
}

func cloneAdaptationPlanRequest(plan domain.AdaptationPlan) (domain.AdaptationPlan, error) {
	data, err := json.Marshal(plan)
	if err != nil {
		return domain.AdaptationPlan{}, fmt.Errorf("clone adaptation plan request: %w", err)
	}
	var cloned domain.AdaptationPlan
	if err := json.Unmarshal(data, &cloned); err != nil {
		return domain.AdaptationPlan{}, fmt.Errorf("clone adaptation plan request: %w", err)
	}
	return cloned, nil
}

func (s *AdaptationStore) savePlanWithMigration(plan domain.AdaptationPlan) error {
	requestID, err := migrationRequestIdentity("adaptation_plan", plan)
	if err != nil {
		return err
	}
	return s.migration.saveRequested("adaptation_plan", requestID, func(_ structureIndex, _ bool) (structureMigrationBuild, error) {
		s.io.mu.Lock()
		defer s.io.mu.Unlock()

		var existing domain.AdaptationPlan
		existingPlan := (*domain.AdaptationPlan)(nil)
		if err := s.io.ReadJSONUnlocked(adaptationPlanFile, &existing); err == nil {
			existingPlan = &existing
		} else if !os.IsNotExist(err) {
			return structureMigrationBuild{}, err
		}
		if err := s.identity.prepareAdaptationPlanForSave(&plan, existingPlan); err != nil {
			return structureMigrationBuild{}, err
		}
		legacySource := structureIndex{Version: structureSchemaVersion}
		if existingPlan != nil {
			s.identity.hydrateAdaptationPlan(existingPlan)
			legacySource = structureIndexFromAdaptation(existingPlan)
		}
		planPayload, err := jsonMigrationPayload(adaptationPlanFile, plan)
		if err != nil {
			return structureMigrationBuild{}, err
		}
		currentWorkflow, err := s.loadPlanningWorkflowUnlocked()
		if err != nil {
			return structureMigrationBuild{}, err
		}
		workflow := domain.AdaptationPlanningWorkflow{
			Version:   domain.AdaptationPlanningWorkflowVersion,
			Stage:     domain.AdaptationPlanningStageConfirmed,
			Revision:  currentWorkflow.Revision + 1,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		workflowPayload, err := jsonMigrationPayload(adaptationPlanningWorkflowFile, workflow)
		if err != nil {
			return structureMigrationBuild{}, err
		}
		return structureMigrationBuild{
			LegacySource: legacySource,
			Target:       structureIndexFromAdaptation(&plan),
			Payloads:     []migrationPayload{planPayload, workflowPayload},
			RemovePaths:  []string{adaptationProposalRuntimeFile, adaptationVolumeReviewFile},
		}, nil
	})
}

func (s *AdaptationStore) LoadPlan() (*domain.AdaptationPlan, error) {
	var result *domain.AdaptationPlan
	load := func() error {
		var plan domain.AdaptationPlan
		if err := s.io.ReadJSON(adaptationPlanFile, &plan); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		s.normalizeAdaptationPlan(&plan)
		s.identity.hydrateAdaptationPlan(&plan)
		result = &plan
		return nil
	}
	if s.migration != nil {
		if err := s.migration.withRead(load); err != nil {
			return nil, err
		}
	} else if err := load(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *AdaptationStore) SaveProposal(plan domain.AdaptationPlan) error {
	return s.withLegacyFormalMutation("save proposal", func() error { return s.saveProposal(plan) })
}

func (s *AdaptationStore) saveProposal(plan domain.AdaptationPlan) error {
	s.normalizeAdaptationPlan(&plan)
	plan.Status = domain.AdaptationPlanStatusProposal
	return s.io.WithWriteLock(func() error {
		var existing domain.AdaptationPlan
		existingPlan := (*domain.AdaptationPlan)(nil)
		if err := s.io.ReadJSONUnlocked(adaptationProposalFile, &existing); err == nil {
			existingPlan = &existing
		} else if !os.IsNotExist(err) {
			return err
		}
		if existingPlan == nil {
			var review domain.AdaptationVolumeReview
			if err := s.io.ReadJSONUnlocked(adaptationVolumeReviewFile, &review); err == nil {
				s.identity.hydrateAdaptationVolumeReview(&review)
				ids := make(map[int]string, len(review.Volumes))
				for _, volume := range review.Volumes {
					ids[volume.Index] = volume.ID
				}
				for i := range plan.Volumes {
					if strings.TrimSpace(plan.Volumes[i].ID) == "" {
						plan.Volumes[i].ID = ids[plan.Volumes[i].Index]
					}
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		if err := s.identity.prepareAdaptationPlanForSave(&plan, existingPlan); err != nil {
			return err
		}
		if err := s.io.RemoveFileUnlocked(adaptationProposalRuntimeFile); err != nil {
			return err
		}
		if err := s.io.RemoveFileUnlocked(adaptationVolumeReviewFile); err != nil {
			return err
		}
		if err := s.io.WriteJSONUnlocked(adaptationProposalFile, plan); err != nil {
			return err
		}
		return s.writePlanningWorkflowStageUnlocked(domain.AdaptationPlanningStageProposalReviewPending)
	})
}

func (s *AdaptationStore) LoadProposal() (*domain.AdaptationPlan, error) {
	var proposal domain.AdaptationPlan
	if err := s.io.ReadJSON(adaptationProposalFile, &proposal); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	s.normalizeAdaptationPlan(&proposal)
	s.identity.hydrateAdaptationPlan(&proposal)
	proposal.Status = domain.AdaptationPlanStatusProposal
	return &proposal, nil
}

func (s *AdaptationStore) SaveVolumeReview(review domain.AdaptationVolumeReview) error {
	review.Status = domain.AdaptationPlanStatusVolumeReview
	return s.withLegacyFormalMutation("save volume review", func() error {
		return s.io.WithWriteLock(func() error {
			var existing domain.AdaptationVolumeReview
			existingReview := (*domain.AdaptationVolumeReview)(nil)
			if err := s.io.ReadJSONUnlocked(adaptationVolumeReviewFile, &existing); err == nil {
				existingReview = &existing
			} else if !os.IsNotExist(err) {
				return err
			}
			if err := s.identity.prepareAdaptationVolumeReviewForSave(&review, existingReview); err != nil {
				return err
			}
			if err := s.io.RemoveFileUnlocked(adaptationProposalFile); err != nil {
				return err
			}
			if err := s.io.RemoveFileUnlocked(adaptationProposalRuntimeFile); err != nil {
				return err
			}
			if err := s.io.WriteJSONUnlocked(adaptationVolumeReviewFile, review); err != nil {
				return err
			}
			return s.writePlanningWorkflowStageUnlocked(domain.AdaptationPlanningStageVolumeReviewPending)
		})
	})
}

// RestoreVolumeReviewForRollback writes the earlier checkpoint before removing
// later proposal artifacts. If cleanup fails, the durable review remains
// available and a retry can safely finish the rollback.
func (s *AdaptationStore) RestoreVolumeReviewForRollback(review domain.AdaptationVolumeReview) error {
	return s.withLegacyFormalMutation("restore volume review", func() error { return s.restoreVolumeReviewForRollback(review) })
}

func (s *AdaptationStore) restoreVolumeReviewForRollback(review domain.AdaptationVolumeReview) error {
	review.Status = domain.AdaptationPlanStatusVolumeReview
	return s.io.WithWriteLock(func() error {
		var existing domain.AdaptationVolumeReview
		existingReview := (*domain.AdaptationVolumeReview)(nil)
		if err := s.io.ReadJSONUnlocked(adaptationVolumeReviewFile, &existing); err == nil {
			existingReview = &existing
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := s.identity.prepareAdaptationVolumeReviewForSave(&review, existingReview); err != nil {
			return err
		}
		if err := s.io.WriteJSONUnlocked(adaptationVolumeReviewFile, review); err != nil {
			return err
		}
		if err := s.writePlanningWorkflowStageUnlocked(domain.AdaptationPlanningStageVolumeReviewPending); err != nil {
			return err
		}
		if err := s.io.RemoveFileUnlocked(adaptationProposalFile); err != nil {
			return err
		}
		return s.io.RemoveFileUnlocked(adaptationProposalRuntimeFile)
	})
}

func (s *AdaptationStore) LoadVolumeReview() (*domain.AdaptationVolumeReview, error) {
	var review domain.AdaptationVolumeReview
	if err := s.io.ReadJSON(adaptationVolumeReviewFile, &review); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	review.Status = domain.AdaptationPlanStatusVolumeReview
	s.identity.hydrateAdaptationVolumeReview(&review)
	return &review, nil
}

func (s *AdaptationStore) ClearVolumeReview() error {
	return s.withLegacyFormalMutation("clear volume review", func() error {
		return s.io.WithWriteLock(func() error {
			if err := s.io.RemoveFileUnlocked(adaptationVolumeReviewFile); err != nil {
				return err
			}
			return s.io.RemoveFileUnlocked(adaptationProposalRuntimeFile)
		})
	})
}

func (s *AdaptationStore) SaveProposalRuntime(runtime domain.AdaptationProposalRuntime) error {
	return s.withLegacyFormalMutation("save proposal runtime", func() error { return s.io.WriteJSON(adaptationProposalRuntimeFile, runtime) })
}

func (s *AdaptationStore) LoadProposalRuntime() (*domain.AdaptationProposalRuntime, error) {
	var runtime domain.AdaptationProposalRuntime
	if err := s.io.ReadJSON(adaptationProposalRuntimeFile, &runtime); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &runtime, nil
}

func (s *AdaptationStore) ClearProposalRuntime() error {
	return s.withLegacyFormalMutation("clear proposal runtime", func() error { return s.io.RemoveFile(adaptationProposalRuntimeFile) })
}

func (s *AdaptationStore) ClearProposal() error {
	return s.withLegacyFormalMutation("clear proposal workflow", func() error {
		return s.io.WithWriteLock(func() error {
			if err := s.io.RemoveFileUnlocked(adaptationProposalFile); err != nil {
				return err
			}
			if err := s.io.RemoveFileUnlocked(adaptationVolumeReviewFile); err != nil {
				return err
			}
			if err := s.io.RemoveFileUnlocked(adaptationProposalRuntimeFile); err != nil {
				return err
			}
			return nil
		})
	})
}

func (s *AdaptationStore) Active() bool {
	plan, err := s.LoadPlan()
	return err == nil && plan != nil && plan.Status == domain.AdaptationPlanStatusConfirmed
}

// Exists is a mode boundary check that does not deserialize or expose any
// adaptation/source contract to normal-fiction code paths.
func (s *AdaptationStore) Exists() bool {
	if s == nil || s.io == nil {
		return false
	}
	_, err := os.Stat(s.io.path(adaptationPlanFile))
	return err == nil
}

func (s *AdaptationStore) SaveCheck(check domain.AdaptationCheck) error {
	if check.Chapter <= 0 {
		return fmt.Errorf("chapter must be > 0")
	}
	return s.withLegacyFormalMutation("save adaptation chapter audit", func() error {
		if s.migration != nil {
			return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
				if !migrated {
					return s.io.WriteJSON(checkRelPath(check.Chapter), check)
				}
				ref, ok := index.chapterRef(check.Chapter)
				if !ok {
					return s.io.WriteJSON(checkRelPath(check.Chapter), check)
				}
				canonical := check
				canonical.Chapter = 0
				return writeJSONProjectionPair(s.io, chapterCanonicalRel(ref.ID, "adaptation-check.json"), canonicalAdaptationCheck{ChapterID: ref.ID, Check: canonical}, checkRelPath(check.Chapter), check)
			})
		}
		return s.io.WriteJSON(checkRelPath(check.Chapter), check)
	})
}

func (s *AdaptationStore) LoadCheck(chapter int) (*domain.AdaptationCheck, error) {
	if chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0")
	}
	if s.migration != nil {
		var result *domain.AdaptationCheck
		err := s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			if !migrated {
				return nil
			}
			ref, ok := index.chapterRef(chapter)
			if !ok {
				return nil
			}
			var canonical canonicalAdaptationCheck
			if err := s.io.ReadJSON(chapterCanonicalRel(ref.ID, "adaptation-check.json"), &canonical); err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if canonical.ChapterID != ref.ID {
				return fmt.Errorf("adaptation check identity mismatch for chapter %d", chapter)
			}
			canonical.Check.Chapter = chapter
			result = &canonical.Check
			return nil
		})
		if err != nil || result != nil {
			return result, err
		}
	}
	var check domain.AdaptationCheck
	if err := s.io.ReadJSON(checkRelPath(chapter), &check); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &check, nil
}

func (s *AdaptationStore) DeleteCheck(chapter int) error {
	if chapter <= 0 {
		return nil
	}
	return s.withLegacyFormalMutation("delete adaptation chapter audit", func() error { return s.deleteCheck(chapter) })
}

func (s *AdaptationStore) deleteCheck(chapter int) error {
	if s.migration != nil {
		return s.migration.withIndexRead(func(index structureIndex, migrated bool) error {
			return s.io.WithWriteLock(func() error {
				if err := s.io.RemoveFileUnlocked(checkRelPath(chapter)); err != nil {
					return err
				}
				if !migrated {
					return nil
				}
				id, ok := index.chapterID(chapter)
				if !ok {
					return nil
				}
				return s.io.RemoveFileUnlocked(chapterCanonicalRel(id, "adaptation-check.json"))
			})
		})
	}
	return s.io.RemoveFile(checkRelPath(chapter))
}

func (s *AdaptationStore) HasPassingCheck(chapter int, draftSHA256 string) (bool, *domain.AdaptationCheck, error) {
	check, err := s.LoadCheck(chapter)
	if err != nil || check == nil {
		return false, check, err
	}
	return check.Passed && check.DraftSHA256 == draftSHA256, check, nil
}

func SourceChapterRelPath(chapter int) string {
	return fmt.Sprintf("%s/%04d.md", adaptationSourceChapterDir, chapter)
}

func SourceReportRelPath(chapter int) string {
	return fmt.Sprintf("%s/%04d.json", adaptationSourceReportDir, chapter)
}

func CoCreateDossierBatchRelPath(index int) string {
	return fmt.Sprintf("%s/%04d.json", adaptationCoCreateBatchDir, index)
}

func SourceFoundationBatchRelPath(level, index int) string {
	return fmt.Sprintf("%s/level_%02d_batch_%04d.json", adaptationSourceFoundationDir, level, index)
}

func CoCreateBriefingBatchRelPath(index int) string {
	return fmt.Sprintf("%s/%04d.json", adaptationCoCreateBriefingDir, index)
}

func findBriefingDecision(briefing domain.AdaptationCoCreateBriefing, decisionID string) *domain.AdaptationBriefingDecision {
	for i := range briefing.Decisions {
		if strings.TrimSpace(briefing.Decisions[i].ID) == decisionID {
			return &briefing.Decisions[i]
		}
	}
	return nil
}

func briefingDecisionHasOption(decision domain.AdaptationBriefingDecision, optionID string) bool {
	for _, option := range decision.Options {
		if strings.TrimSpace(option.ID) == optionID {
			return true
		}
	}
	return false
}

func upsertResolvedDecision(values []domain.AdaptationResolvedDecision, resolved domain.AdaptationResolvedDecision) []domain.AdaptationResolvedDecision {
	out := append([]domain.AdaptationResolvedDecision(nil), values...)
	for i := range out {
		if strings.TrimSpace(out[i].DecisionID) == strings.TrimSpace(resolved.DecisionID) {
			out[i] = resolved
			return out
		}
	}
	return append(out, resolved)
}

func markBriefingDecisionResolved(briefing *domain.AdaptationCoCreateBriefing, decisionID string) {
	if briefing == nil {
		return
	}
	for i := range briefing.Decisions {
		if strings.TrimSpace(briefing.Decisions[i].ID) == decisionID {
			briefing.Decisions[i].Status = "resolved"
		}
	}
	for i := range briefing.Batches {
		for j := range briefing.Batches[i].DecisionQuestions {
			if strings.TrimSpace(briefing.Batches[i].DecisionQuestions[j].ID) == decisionID {
				briefing.Batches[i].DecisionQuestions[j].Status = "resolved"
			}
		}
	}
}

func timeNowUTCString() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func safeAdaptationBackupLabel(label string) string {
	label = strings.TrimSpace(label)
	replacer := strings.NewReplacer(
		"\\", "-",
		"/", "-",
		":", "-",
		"*", "-",
		"?", "-",
		"\"", "-",
		"<", "-",
		">", "-",
		"|", "-",
		" ", "-",
	)
	label = replacer.Replace(label)
	label = strings.Trim(label, ".-")
	if len(label) > 80 {
		label = label[:80]
	}
	return label
}

func copyAdaptationDir(sourceRoot, targetRoot string) error {
	sourceRoot = filepath.Clean(sourceRoot)
	targetRoot = filepath.Clean(targetRoot)
	if sourceRoot == targetRoot {
		return fmt.Errorf("adaptation backup target must differ from source")
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(targetRoot, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyAdaptationFile(path, target, info.Mode().Perm())
	})
}

func copyAdaptationFile(source, target string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func TextSHA256(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func checkRelPath(chapter int) string {
	return fmt.Sprintf("%s/%04d.json", adaptationCheckDir, chapter)
}

func truncateRunes(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}

func (s *AdaptationStore) normalizeAdaptationPlan(plan *domain.AdaptationPlan) {
	manifest, _ := s.LoadSourceManifest()
	normalizeAdaptationPlan(plan, manifest)
}

func normalizeAdaptationPlan(plan *domain.AdaptationPlan, manifest *domain.AdaptationSourceManifest) {
	if plan == nil {
		return
	}
	plan.Granularity = domain.NormalizeAdaptationGranularity(plan.Granularity)
	plan.ModePolicy = domain.AdaptationModePolicyForGranularity(plan.Granularity)
	plan.Status = domain.NormalizeAdaptationPlanStatus(plan.Status)
	plan.RewritePolicy = domain.AdaptationRewritePolicyForGranularity(plan.Granularity)
	if len(plan.Rules) == 0 && strings.TrimSpace(plan.Brief) != "" {
		plan.Rules = domain.CompileAdaptationRules(plan.Brief, plan.Granularity)
	}
	deriveBudgets := shouldDeriveAdaptationBudgets(plan)
	tolerance := plan.WordTolerance
	if tolerance <= 0 && deriveBudgets {
		tolerance = defaultAdaptationWordTolerance
		plan.WordTolerance = tolerance
	}
	sourceRunes := adaptationSourceRunesByChapter(manifest)
	for i := range plan.Chapters {
		normalizeAdaptationChapterPlan(&plan.Chapters[i], tolerance, sourceRunes, deriveBudgets)
		if len(plan.Chapters[i].RuleIDs) == 0 {
			plan.Chapters[i].RuleIDs = domain.AdaptationRuleIDs(domain.ApplicableAdaptationRules(plan.Rules, plan.Granularity, plan.Chapters[i].Chapter))
		}
	}
	budgetsChanged := false
	if deriveBudgets {
		budgetsChanged = normalizeAdaptationSplitChapterBudgets(plan, sourceRunes)
	}
	plan.Volumes = normalizeAdaptationVolumes(plan.Volumes, len(plan.Chapters))
	if deriveBudgets {
		normalizeAdaptationPlanTotals(plan, budgetsChanged)
	}
}

func normalizeAdaptationVolumes(volumes []domain.AdaptationVolumePlan, chapterCount int) []domain.AdaptationVolumePlan {
	if len(volumes) == 0 || chapterCount <= 0 {
		return nil
	}
	out := make([]domain.AdaptationVolumePlan, 0, len(volumes))
	for _, volume := range volumes {
		if volume.TargetFrom <= 0 || volume.TargetTo < volume.TargetFrom || volume.TargetTo > chapterCount {
			continue
		}
		if volume.Index <= 0 {
			volume.Index = len(out) + 1
		}
		volume.Title = strings.TrimSpace(volume.Title)
		volume.Theme = strings.TrimSpace(volume.Theme)
		volume.Goal = strings.TrimSpace(volume.Goal)
		volume.Summary = strings.TrimSpace(volume.Summary)
		if volume.Title == "" {
			volume.Title = fmt.Sprintf("第 %d-%d 章", volume.TargetFrom, volume.TargetTo)
		}
		out = append(out, volume)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TargetFrom == out[j].TargetFrom {
			return out[i].Index < out[j].Index
		}
		return out[i].TargetFrom < out[j].TargetFrom
	})
	for i := range out {
		if out[i].Index <= 0 {
			out[i].Index = i + 1
		}
	}
	return out
}

func normalizeAdaptationChapterPlan(chapter *domain.AdaptationChapterPlan, tolerance float64, sourceRunes map[int]int, deriveBudgets bool) {
	if chapter == nil {
		return
	}
	chapter.OutlineEntry.Chapter = chapter.Chapter
	chapter.OutlineEntry.Title = chapter.Title

	if chapter.WordBudget == nil {
		if chapter.SourceRunes > 0 || chapter.TargetRunes > 0 || chapter.TargetMinRunes > 0 || chapter.TargetMaxRunes > 0 {
			chapter.WordBudget = &domain.AdaptationChapterWordBudget{}
		}
	}
	if deriveBudgets && chapter.SourceRunes <= 0 {
		chapter.SourceRunes = sumAdaptationSourceRunes(chapter.SourceChapters, sourceRunes)
	}
	if deriveBudgets && chapter.SourceRunes <= 0 && chapter.SourceRange.From > 0 && chapter.SourceRange.To >= chapter.SourceRange.From {
		for sourceChapter := chapter.SourceRange.From; sourceChapter <= chapter.SourceRange.To; sourceChapter++ {
			chapter.SourceRunes += sourceRunes[sourceChapter]
		}
	}
	if deriveBudgets && chapter.TargetRunes <= 0 && chapter.SourceRunes > 0 {
		chapter.TargetRunes = chapter.SourceRunes
	}
	if chapter.TargetMinRunes <= 0 && chapter.TargetMaxRunes <= 0 && chapter.TargetRunes > 0 {
		chapter.TargetMinRunes, chapter.TargetMaxRunes = adaptationRuneRange(chapter.TargetRunes, tolerance)
	}
	if chapter.WordBudget == nil && (chapter.SourceRunes > 0 || chapter.TargetRunes > 0 || chapter.TargetMinRunes > 0 || chapter.TargetMaxRunes > 0) {
		chapter.WordBudget = &domain.AdaptationChapterWordBudget{}
	}
	if chapter.WordBudget == nil {
		return
	}
	if chapter.WordBudget.SourceRunes <= 0 {
		chapter.WordBudget.SourceRunes = chapter.SourceRunes
	}
	if chapter.WordBudget.TargetRunes <= 0 {
		chapter.WordBudget.TargetRunes = chapter.TargetRunes
	}
	if chapter.WordBudget.MinRunes <= 0 {
		chapter.WordBudget.MinRunes = chapter.TargetMinRunes
	}
	if chapter.WordBudget.MaxRunes <= 0 {
		chapter.WordBudget.MaxRunes = chapter.TargetMaxRunes
	}
	if chapter.WordBudget.Tolerance <= 0 {
		chapter.WordBudget.Tolerance = tolerance
	}
	if chapter.SourceRunes <= 0 {
		chapter.SourceRunes = chapter.WordBudget.SourceRunes
	}
	if chapter.TargetRunes <= 0 {
		chapter.TargetRunes = chapter.WordBudget.TargetRunes
	}
	if chapter.TargetMinRunes <= 0 {
		chapter.TargetMinRunes = chapter.WordBudget.MinRunes
	}
	if chapter.TargetMaxRunes <= 0 {
		chapter.TargetMaxRunes = chapter.WordBudget.MaxRunes
	}
}

type adaptationSplitBudgetGroup struct {
	Indexes     []int
	SourceRunes int
}

func normalizeAdaptationSplitChapterBudgets(plan *domain.AdaptationPlan, sourceRunes map[int]int) bool {
	if plan == nil || domain.AdaptationRewritePolicyForGranularity(plan.Granularity) != domain.AdaptationRewriteFullRewrite {
		return false
	}
	changed := false
	groups := adaptationSplitBudgetGroups(plan.Chapters, sourceRunes)
	for _, group := range groups {
		if len(group.Indexes) <= 1 || group.SourceRunes <= 0 {
			continue
		}
		minChapters := ceilAdaptationPositiveDiv(group.SourceRunes, domain.AdaptationModelChapterMaxRunes)
		if minChapters > len(group.Indexes) {
			continue
		}
		if !adaptationSplitBudgetGroupNeedsNormalization(plan.Chapters, group) {
			continue
		}
		applyAdaptationSplitBudgetGroup(plan.Chapters, group)
		changed = true
	}
	return changed
}

func adaptationSplitBudgetGroups(chapters []domain.AdaptationChapterPlan, sourceRunes map[int]int) map[string]adaptationSplitBudgetGroup {
	groups := make(map[string]adaptationSplitBudgetGroup)
	for index := range chapters {
		chapter := chapters[index]
		key := adaptationSplitBudgetGroupKey(chapter)
		group := groups[key]
		group.Indexes = append(group.Indexes, index)
		if group.SourceRunes <= 0 {
			group.SourceRunes = adaptationCoverageSourceRunes(chapter, sourceRunes)
		}
		groups[key] = group
	}
	return groups
}

func adaptationSplitBudgetGroupKey(chapter domain.AdaptationChapterPlan) string {
	if chapter.SourceRange.From > 0 && chapter.SourceRange.To >= chapter.SourceRange.From {
		return fmt.Sprintf("range:%d:%d", chapter.SourceRange.From, chapter.SourceRange.To)
	}
	chapters := appendSortedPositiveInts(nil, chapter.SourceChapters...)
	if len(chapters) == 0 {
		return fmt.Sprintf("chapter:%d", chapter.Chapter)
	}
	parts := make([]string, 0, len(chapters))
	for _, sourceChapter := range chapters {
		parts = append(parts, fmt.Sprintf("%d", sourceChapter))
	}
	return "anchors:" + strings.Join(parts, ",")
}

func adaptationCoverageSourceRunes(chapter domain.AdaptationChapterPlan, sourceRunes map[int]int) int {
	if chapter.SourceRange.From > 0 && chapter.SourceRange.To >= chapter.SourceRange.From {
		total := 0
		for sourceChapter := chapter.SourceRange.From; sourceChapter <= chapter.SourceRange.To; sourceChapter++ {
			total += sourceRunes[sourceChapter]
		}
		if total > 0 {
			return total
		}
	}
	if total := sumAdaptationSourceRunes(chapter.SourceChapters, sourceRunes); total > 0 {
		return total
	}
	if chapter.WordBudget != nil && chapter.WordBudget.SourceRunes > 0 {
		return chapter.WordBudget.SourceRunes
	}
	return chapter.SourceRunes
}

func adaptationSplitBudgetGroupNeedsNormalization(chapters []domain.AdaptationChapterPlan, group adaptationSplitBudgetGroup) bool {
	for _, index := range group.Indexes {
		chapter := chapters[index]
		if chapter.SourceRunes > domain.AdaptationModelChapterMaxRunes ||
			chapter.TargetRunes > domain.AdaptationModelChapterMaxRunes ||
			chapter.TargetMaxRunes > domain.AdaptationModelChapterMaxRunes {
			return true
		}
		if chapter.WordBudget != nil &&
			(chapter.WordBudget.SourceRunes > domain.AdaptationModelChapterMaxRunes ||
				chapter.WordBudget.TargetRunes > domain.AdaptationModelChapterMaxRunes ||
				chapter.WordBudget.MaxRunes > domain.AdaptationModelChapterMaxRunes) {
			return true
		}
	}
	return false
}

func applyAdaptationSplitBudgetGroup(chapters []domain.AdaptationChapterPlan, group adaptationSplitBudgetGroup) {
	for offset, index := range group.Indexes {
		sourceRunes := splitAdaptationRunesForIndex(group.SourceRunes, len(group.Indexes), offset)
		targetRunes := sourceRunes
		if targetRunes <= 0 {
			targetRunes = domain.AdaptationModelChapterTargetRunes
		}
		if targetRunes > domain.AdaptationModelChapterMaxRunes {
			targetRunes = domain.AdaptationModelChapterMaxRunes
		}
		minRunes, maxRunes := adaptationModelChapterRuneRange(targetRunes)
		chapter := &chapters[index]
		chapter.SourceRunes = sourceRunes
		chapter.TargetRunes = targetRunes
		chapter.TargetMinRunes = minRunes
		chapter.TargetMaxRunes = maxRunes
		chapter.WordBudget = &domain.AdaptationChapterWordBudget{
			SourceRunes: sourceRunes,
			TargetRunes: targetRunes,
			MinRunes:    minRunes,
			MaxRunes:    maxRunes,
			Tolerance:   domain.AdaptationModelChapterTolerance,
		}
	}
}

func splitAdaptationRunesForIndex(totalRunes, count, index int) int {
	if totalRunes <= 0 || count <= 0 || index < 0 {
		return 0
	}
	base := totalRunes / count
	remainder := totalRunes % count
	if index < remainder {
		return base + 1
	}
	return base
}

func adaptationModelChapterRuneRange(targetRunes int) (int, int) {
	minRunes, maxRunes := adaptationRuneRange(targetRunes, domain.AdaptationModelChapterTolerance)
	if maxRunes > domain.AdaptationModelChapterMaxRunes {
		maxRunes = domain.AdaptationModelChapterMaxRunes
	}
	if minRunes > targetRunes {
		minRunes = targetRunes
	}
	if maxRunes < targetRunes {
		maxRunes = targetRunes
	}
	return minRunes, maxRunes
}

func ceilAdaptationPositiveDiv(value, divisor int) int {
	if value <= 0 || divisor <= 0 {
		return 0
	}
	return (value + divisor - 1) / divisor
}

func appendSortedPositiveInts(base []int, values ...int) []int {
	seen := make(map[int]bool, len(base)+len(values))
	out := make([]int, 0, len(base)+len(values))
	for _, value := range append(base, values...) {
		if value <= 0 || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

const defaultAdaptationWordTolerance = 0.15

func shouldDeriveAdaptationBudgets(plan *domain.AdaptationPlan) bool {
	if plan == nil {
		return false
	}
	if plan.RewritePolicy != domain.AdaptationRewritePreserveDetails {
		return true
	}
	if plan.WordTolerance > 0 ||
		plan.SourceTotalRunes > 0 ||
		plan.TargetTotalRunes > 0 ||
		plan.TargetMinRunes > 0 ||
		plan.TargetMaxRunes > 0 {
		return true
	}
	for _, chapter := range plan.Chapters {
		if chapter.WordBudget != nil ||
			chapter.SourceRunes > 0 ||
			chapter.TargetRunes > 0 ||
			chapter.TargetMinRunes > 0 ||
			chapter.TargetMaxRunes > 0 {
			return true
		}
	}
	return false
}

func normalizeAdaptationPlanTotals(plan *domain.AdaptationPlan, force bool) {
	if plan == nil {
		return
	}
	sourceTotal := 0
	targetTotal := 0
	targetMin := 0
	targetMax := 0
	for _, chapter := range plan.Chapters {
		sourceTotal += chapter.SourceRunes
		targetTotal += chapter.TargetRunes
		targetMin += chapter.TargetMinRunes
		targetMax += chapter.TargetMaxRunes
	}
	if force || plan.SourceTotalRunes <= 0 {
		plan.SourceTotalRunes = sourceTotal
	}
	if force || plan.TargetTotalRunes <= 0 {
		plan.TargetTotalRunes = targetTotal
	}
	if force || plan.TargetMinRunes <= 0 {
		plan.TargetMinRunes = targetMin
	}
	if force || plan.TargetMaxRunes <= 0 {
		plan.TargetMaxRunes = targetMax
	}
	if plan.TargetMinRunes <= 0 && plan.TargetMaxRunes <= 0 && plan.TargetTotalRunes > 0 {
		plan.TargetMinRunes, plan.TargetMaxRunes = adaptationRuneRange(plan.TargetTotalRunes, plan.WordTolerance)
	}
}

func adaptationSourceRunesByChapter(manifest *domain.AdaptationSourceManifest) map[int]int {
	if manifest == nil {
		return nil
	}
	out := make(map[int]int, len(manifest.Chapters))
	for _, source := range manifest.Chapters {
		if source.Chapter > 0 && source.Runes > 0 {
			out[source.Chapter] = source.Runes
		}
	}
	return out
}

func sumAdaptationSourceRunes(chapters []int, sourceRunes map[int]int) int {
	total := 0
	for _, chapter := range chapters {
		total += sourceRunes[chapter]
	}
	return total
}

func adaptationRuneRange(target int, tolerance float64) (int, int) {
	if target <= 0 {
		return 0, 0
	}
	if tolerance <= 0 {
		tolerance = defaultAdaptationWordTolerance
	}
	minRunes := int(math.Round(float64(target) * (1 - tolerance)))
	maxRunes := int(math.Round(float64(target) * (1 + tolerance)))
	if minRunes < 1 {
		minRunes = 1
	}
	if maxRunes < minRunes {
		maxRunes = minRunes
	}
	return minRunes, maxRunes
}
