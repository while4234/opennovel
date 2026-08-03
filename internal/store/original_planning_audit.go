package store

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const originalPlanningAuditsFile = "meta/original_planning/audits.json"

type OriginalPlanningAuditStore struct {
	io                     *IO
	outline                *OutlineStore
	foundation             *FoundationStore
	revisions              *RevisionStore
	withApprovedFoundation func(func(int64, string) error) error
}

func (s *OriginalPlanningAuditStore) SaveForFoundationRevision(owner *FoundationPlanningOwner, audit domain.OriginalPlanningAudit) error {
	if s == nil || s.revisions == nil {
		return fmt.Errorf("Foundation planning revision store is required")
	}
	return s.revisions.withFoundationPlanningMutation(owner, "save Foundation-owned planning audit", nil, func() error {
		return s.save(audit)
	})
}

type OriginalPlanningWork struct {
	Kind        string
	Volume      int
	Arc         int
	FromVolume  int
	ToVolume    int
	FromChapter int
	ToChapter   int
	Audit       *domain.OriginalPlanningAudit
	Evidence    string
}

func NewOriginalPlanningAuditStore(io *IO, outlines ...*OutlineStore) *OriginalPlanningAuditStore {
	var outline *OutlineStore
	if len(outlines) > 0 {
		outline = outlines[0]
	}
	return &OriginalPlanningAuditStore{io: io, outline: outline}
}

func (s *OriginalPlanningAuditStore) Load() ([]domain.OriginalPlanningAudit, error) {
	var audits []domain.OriginalPlanningAudit
	if err := s.io.ReadJSON(originalPlanningAuditsFile, &audits); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return audits, nil
}

func (s *OriginalPlanningAuditStore) Save(audit domain.OriginalPlanningAudit) error {
	if audit.Verdict == "pass" && s.outline != nil {
		if s.withApprovedFoundation == nil {
			return fmt.Errorf("approved story foundation authority is required to save a passing original planning audit")
		}
		return s.withApprovedFoundation(func(foundationRevision int64, foundationSignature string) error {
			volumes, err := s.outline.LoadLayeredOutline()
			if err != nil {
				return err
			}
			if err := domain.BindOriginalPlanningAudit(&audit, volumes, foundationSignature); err != nil {
				return err
			}
			audit.FoundationRevision = foundationRevision
			return s.save(audit)
		})
	}
	return s.save(audit)
}

func (s *OriginalPlanningAuditStore) save(audit domain.OriginalPlanningAudit) error {
	return s.io.WithWriteLock(func() error {
		var audits []domain.OriginalPlanningAudit
		if err := s.io.ReadJSONUnlocked(originalPlanningAuditsFile, &audits); err != nil && !os.IsNotExist(err) {
			return err
		}
		attempt := 1
		kept := audits[:0]
		for _, existing := range audits {
			if sameOriginalPlanningAuditScope(existing, audit) {
				attempt = existing.Attempt + 1
				continue
			}
			kept = append(kept, existing)
		}
		audit.Attempt = attempt
		audit.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		kept = append(kept, audit)
		sort.SliceStable(kept, func(i, j int) bool {
			if kept[i].Volume != kept[j].Volume {
				return kept[i].Volume < kept[j].Volume
			}
			if kept[i].Arc != kept[j].Arc {
				return kept[i].Arc < kept[j].Arc
			}
			return originalPlanningAuditScopeRank(kept[i].Scope) < originalPlanningAuditScopeRank(kept[j].Scope)
		})
		return s.io.WriteJSONUnlocked(originalPlanningAuditsFile, kept)
	})
}

func (s *OriginalPlanningAuditStore) Get(scope string, volume, arc int) (*domain.OriginalPlanningAudit, error) {
	audits, err := s.loadCurrent()
	if err != nil {
		return nil, err
	}
	needle := domain.OriginalPlanningAudit{Scope: scope, Volume: volume, Arc: arc}
	for i := range audits {
		if sameOriginalPlanningAuditScope(audits[i], needle) {
			return &audits[i], nil
		}
	}
	return nil, nil
}

func (s *OriginalPlanningAuditStore) GetBookBatch(fromVolume, toVolume int) (*domain.OriginalPlanningAudit, error) {
	audits, err := s.loadCurrent()
	if err != nil {
		return nil, err
	}
	for i := range audits {
		if audits[i].Scope == "book_batch" && audits[i].FromVolume == fromVolume && audits[i].ToVolume == toVolume {
			return &audits[i], nil
		}
	}
	return nil, nil
}

func (s *OriginalPlanningAuditStore) Reset() error {
	return s.io.RemoveFile(originalPlanningAuditsFile)
}

// loadCurrent keeps current revise instructions actionable while refusing to
// reuse any signed audit after its scoped outline content has changed.
func (s *OriginalPlanningAuditStore) loadCurrent() ([]domain.OriginalPlanningAudit, error) {
	audits, err := s.Load()
	if err != nil || s.outline == nil {
		return audits, err
	}
	if s.withApprovedFoundation == nil {
		return nil, fmt.Errorf("approved story foundation authority is required to load current original planning audits")
	}
	var current []domain.OriginalPlanningAudit
	err = s.withApprovedFoundation(func(foundationRevision int64, foundationSignature string) error {
		volumes, loadErr := s.outline.LoadLayeredOutline()
		if loadErr != nil {
			return loadErr
		}
		foundation, foundationErr := s.foundation.Load()
		if foundationErr != nil {
			return foundationErr
		}
		current = make([]domain.OriginalPlanningAudit, 0, len(audits))
		for _, audit := range audits {
			if audit.Verdict == "pass" && ((audit.FoundationRevision == foundationRevision && domain.OriginalPlanningAuditCurrent(audit, volumes, foundationSignature)) ||
				domain.OriginalPlanningAuditCurrentWithFoundation(audit, volumes, foundation, foundationSignature)) {
				current = append(current, audit)
				continue
			}
			legacyUnsignedRevise := audit.Verdict == "revise" && (audit.StructureSignature == "" || audit.ContentSignature == "")
			if audit.Verdict == "revise" && (legacyUnsignedRevise || domain.OriginalPlanningAuditBindingCurrent(audit, volumes)) {
				current = append(current, audit)
			}
		}
		return nil
	})
	return current, err
}

// InvalidateFoundationRevision removes affected audit scopes and attaches a
// stable-ID Foundation projection only to otherwise-current, unaffected
// evidence. It never rewrites the historical whole-Foundation signature.
func (s *OriginalPlanningAuditStore) InvalidateFoundationRevision(base, candidate domain.StoryFoundation, impact domain.FoundationImpact, dependencies *domain.FoundationDependencyManifest) error {
	return s.io.WithWriteLock(func() error {
		var audits []domain.OriginalPlanningAudit
		if err := s.io.ReadJSONUnlocked(originalPlanningAuditsFile, &audits); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if impact.FullBook || dependencies == nil {
			return s.io.RemoveFileUnlocked(originalPlanningAuditsFile)
		}
		changed := make(map[string]bool)
		for _, id := range impact.AffectedVolumeIDs {
			changed[id] = true
		}
		for _, id := range impact.AffectedArcIDs {
			changed[id] = true
		}
		for _, id := range impact.AffectedChapterIDs {
			changed[id] = true
		}
		kept := make([]domain.OriginalPlanningAudit, 0, len(audits))
		for _, audit := range audits {
			if audit.Scope == "book" || audit.Scope == "book_batch" || audit.Scope == "skeleton_book" || audit.Scope == "skeleton_book_batch" || changed[audit.ScopeID] {
				continue
			}
			refs := make([]string, 0)
			for _, entry := range dependencies.Entries {
				if entry.DependentArtifactID == audit.ScopeID {
					refs = append(refs, string(entry.SourceEntityType)+":"+entry.SourceEntityID)
				}
			}
			if len(refs) == 0 {
				continue
			}
			before, err1 := domain.FoundationProjectionSignature(base, refs)
			after, err2 := domain.FoundationProjectionSignature(candidate, refs)
			if err1 != nil || err2 != nil || before != after {
				continue
			}
			audit.FoundationEntityRefs = refs
			audit.FoundationProjectionSignature = before
			kept = append(kept, audit)
		}
		if len(kept) == 0 {
			return s.io.RemoveFileUnlocked(originalPlanningAuditsFile)
		}
		return s.io.WriteJSONUnlocked(originalPlanningAuditsFile, kept)
	})
}

// QueueFoundationRepair converts an approved Foundation impact into the
// existing original-planning repair queue. The normal router remains the only
// generator: these signed revise findings merely identify the scopes it must
// repair and subsequently re-audit.
func (s *OriginalPlanningAuditStore) QueueFoundationRepair(impact domain.FoundationImpact) (string, error) {
	if s == nil || s.outline == nil || s.foundation == nil {
		return "", fmt.Errorf("original planning repair stores are required")
	}
	volumes, err := s.outline.LoadLayeredOutline()
	if err != nil {
		return "", err
	}
	if len(volumes) == 0 {
		return domain.PlanningReviewKindBlueprint, nil
	}
	foundation, err := s.foundation.Load()
	if err != nil {
		return "", err
	}
	foundationSignature, err := domain.FoundationAuditSignature(foundation)
	if err != nil {
		return "", err
	}
	queue := func(audit domain.OriginalPlanningAudit) error {
		if err := domain.BindOriginalPlanningAudit(&audit, volumes, foundationSignature); err != nil {
			return err
		}
		audit.FoundationRevision = foundation.Revision
		return s.save(audit)
	}
	if impact.FullBook {
		for _, volume := range volumes {
			if err := queue(foundationRepairAudit("skeleton_volume", volume.ID, volume.Index, 1, 0, 0)); err != nil {
				return "", err
			}
		}
		return domain.PlanningReviewKindBlueprint, nil
	}
	volumeIDs := make(map[string]struct{}, len(impact.AffectedVolumeIDs))
	arcIDs := make(map[string]struct{}, len(impact.AffectedArcIDs))
	chapterIDs := make(map[string]struct{}, len(impact.AffectedChapterIDs))
	for _, id := range impact.AffectedVolumeIDs {
		volumeIDs[id] = struct{}{}
	}
	for _, id := range impact.AffectedArcIDs {
		arcIDs[id] = struct{}{}
	}
	for _, id := range impact.AffectedChapterIDs {
		chapterIDs[id] = struct{}{}
	}
	nextChapter := 1
	for _, volume := range volumes {
		if _, affected := volumeIDs[volume.ID]; affected {
			if err := queue(foundationRepairAudit("skeleton_volume", volume.ID, volume.Index, 1, 0, 0)); err != nil {
				return "", err
			}
		}
		for _, arc := range volume.Arcs {
			count := len(arc.Chapters)
			if count == 0 {
				count = arc.EstimatedChapters
			}
			from, to := nextChapter, nextChapter+count-1
			if _, affected := arcIDs[arc.ID]; affected {
				if err := queue(foundationRepairAudit("arc", arc.ID, volume.Index, arc.Index, from, to)); err != nil {
					return "", err
				}
			}
			for offset, chapter := range arc.Chapters {
				if _, affected := chapterIDs[chapter.ID]; affected {
					number := from + offset
					if err := queue(foundationRepairAudit("chapter", chapter.ID, volume.Index, arc.Index, number, number)); err != nil {
						return "", err
					}
				}
			}
			nextChapter = to + 1
		}
	}
	if len(volumeIDs) > 0 {
		return domain.PlanningReviewKindBlueprint, nil
	}
	return domain.PlanningReviewKindVolumeSplit, nil
}

func foundationRepairAudit(scope, scopeID string, volume, arc, fromChapter, toChapter int) domain.OriginalPlanningAudit {
	return domain.OriginalPlanningAudit{
		Scope: scope, ScopeID: scopeID, Volume: volume, Arc: arc, FromChapter: fromChapter, ToChapter: toChapter,
		Verdict: "revise", Summary: "StoryFoundation revision invalidated this planning scope",
		Issues: []domain.OriginalPlanningAuditIssue{{
			Severity: "major", Volume: volume, Arc: arc, FromChapter: fromChapter, ToChapter: toChapter,
			Description:       "the planning scope predates the approved StoryFoundation revision",
			RepairInstruction: "repair this scope against the current canonical StoryFoundation, then rerun every required signed planning audit",
		}},
	}
}

// NextWork returns the next bounded generation/audit action. The order is
// deliberately serial: expand one <=4 chapter arc, audit it, finish and audit
// its volume, then synthesize at most two volumes per book batch.
func (s *OriginalPlanningAuditStore) NextWork(outline *OutlineStore) (*OriginalPlanningWork, error) {
	if outline == nil {
		return nil, fmt.Errorf("outline store is required")
	}
	volumes, err := outline.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return nil, err
	}
	audits, err := s.loadCurrent()
	if err != nil {
		return nil, err
	}
	// A failed gate always repairs its first precisely located issue before any
	// later material is generated or accepted.
	for i := len(audits) - 1; i >= 0; i-- {
		audit := audits[i]
		if audit.Verdict != "revise" || len(audit.Issues) == 0 {
			continue
		}
		issue := audit.Issues[0]
		copyAudit := audit
		return &OriginalPlanningWork{Kind: "repair_arc", Volume: issue.Volume, Arc: issue.Arc, FromChapter: issue.FromChapter, ToChapter: issue.ToChapter, Audit: &copyAudit}, nil
	}
	nextChapter := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			count := len(arc.Chapters)
			if count == 0 {
				count = arc.EstimatedChapters
			}
			from, to := nextChapter, nextChapter+count-1
			nextChapter += count
			if len(arc.Chapters) == 0 {
				return &OriginalPlanningWork{Kind: "expand_arc", Volume: volume.Index, Arc: arc.Index, FromChapter: from, ToChapter: to}, nil
			}
			for offset, chapter := range arc.Chapters {
				number := from + offset
				audit := findOriginalPlanningChapterAudit(audits, chapter.ID, number)
				if audit == nil || audit.Verdict != "pass" {
					return &OriginalPlanningWork{Kind: "audit_chapter", Volume: volume.Index, Arc: arc.Index, FromChapter: number, ToChapter: number}, nil
				}
			}
			audit := findOriginalPlanningAudit(audits, "arc", volume.Index, arc.Index, 0, 0)
			if audit == nil || audit.Verdict != "pass" {
				return &OriginalPlanningWork{Kind: "audit_arc", Volume: volume.Index, Arc: arc.Index, FromChapter: from, ToChapter: to}, nil
			}
		}
		volumeAudit := findOriginalPlanningAudit(audits, "volume", volume.Index, 0, 0, 0)
		if volumeAudit == nil || volumeAudit.Verdict != "pass" {
			return &OriginalPlanningWork{Kind: "audit_volume", Volume: volume.Index, Evidence: originalPlanningAuditEvidenceJSON(filterOriginalPlanningAudits(audits, "arc", volume.Index))}, nil
		}
	}
	for start := 0; start < len(volumes); start += 2 {
		end := min(start+1, len(volumes)-1)
		fromVolume, toVolume := volumes[start].Index, volumes[end].Index
		batch := findOriginalPlanningAudit(audits, "book_batch", 0, 0, fromVolume, toVolume)
		if batch == nil || batch.Verdict != "pass" {
			return &OriginalPlanningWork{Kind: "audit_book_batch", FromVolume: fromVolume, ToVolume: toVolume, Evidence: originalPlanningAuditEvidenceJSON(filterOriginalPlanningVolumeAudits(audits, fromVolume, toVolume))}, nil
		}
	}
	book := findOriginalPlanningAudit(audits, "book", 0, 0, 0, 0)
	if book == nil || book.Verdict != "pass" {
		return &OriginalPlanningWork{Kind: "audit_book", Evidence: originalPlanningAuditEvidenceJSON(filterOriginalPlanningAudits(audits, "book_batch", 0))}, nil
	}
	return &OriginalPlanningWork{Kind: "complete"}, nil
}

func findOriginalPlanningChapterAudit(audits []domain.OriginalPlanningAudit, chapterID string, chapter int) *domain.OriginalPlanningAudit {
	for index := range audits {
		audit := &audits[index]
		if audit.Scope == "chapter" && audit.FromChapter == chapter && audit.ToChapter == chapter &&
			strings.TrimSpace(audit.ScopeID) == strings.TrimSpace(chapterID) {
			return audit
		}
	}
	return nil
}

// NextSkeletonWork runs before the volume plan is exposed to the user. It
// audits one bounded volume at a time, then at most two volumes per synthesis,
// and finally the whole-book promise/ending contract from audit digests.
func (s *OriginalPlanningAuditStore) NextSkeletonWork(outline *OutlineStore) (*OriginalPlanningWork, error) {
	if outline == nil {
		return nil, fmt.Errorf("outline store is required")
	}
	volumes, err := outline.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return nil, err
	}
	audits, err := s.loadCurrent()
	if err != nil {
		return nil, err
	}
	for i := len(audits) - 1; i >= 0; i-- {
		audit := audits[i]
		if !isSkeletonAuditScope(audit.Scope) || audit.Verdict != "revise" || len(audit.Issues) == 0 {
			continue
		}
		issue := audit.Issues[0]
		copyAudit := audit
		return &OriginalPlanningWork{Kind: "repair_skeleton_volume", Volume: issue.Volume, Arc: issue.Arc, Audit: &copyAudit}, nil
	}
	for _, volume := range volumes {
		audit := findOriginalPlanningAudit(audits, "skeleton_volume", volume.Index, 0, 0, 0)
		if audit == nil || audit.Verdict != "pass" {
			return &OriginalPlanningWork{Kind: "audit_skeleton_volume", Volume: volume.Index}, nil
		}
	}
	for start := 0; start < len(volumes); start += 2 {
		end := min(start+1, len(volumes)-1)
		fromVolume, toVolume := volumes[start].Index, volumes[end].Index
		batch := findOriginalPlanningAudit(audits, "skeleton_book_batch", 0, 0, fromVolume, toVolume)
		if batch == nil || batch.Verdict != "pass" {
			return &OriginalPlanningWork{
				Kind: "audit_skeleton_book_batch", FromVolume: fromVolume, ToVolume: toVolume,
				Evidence: originalPlanningAuditEvidenceJSON(filterOriginalPlanningAudits(audits, "skeleton_volume", 0)),
			}, nil
		}
	}
	book := findOriginalPlanningAudit(audits, "skeleton_book", 0, 0, 0, 0)
	if book == nil || book.Verdict != "pass" {
		return &OriginalPlanningWork{
			Kind:     "audit_skeleton_book",
			Evidence: originalPlanningAuditEvidenceJSON(filterOriginalPlanningAudits(audits, "skeleton_book_batch", 0)),
		}, nil
	}
	return &OriginalPlanningWork{Kind: "skeleton_complete"}, nil
}

func isSkeletonAuditScope(scope string) bool {
	return scope == "skeleton_volume" || scope == "skeleton_book_batch" || scope == "skeleton_book"
}

func filterOriginalPlanningAudits(audits []domain.OriginalPlanningAudit, scope string, volume int) []domain.OriginalPlanningAudit {
	var out []domain.OriginalPlanningAudit
	for _, audit := range audits {
		if audit.Scope == scope && (volume == 0 || audit.Volume == volume) && audit.Verdict == "pass" {
			out = append(out, audit)
		}
	}
	return out
}

func filterOriginalPlanningVolumeAudits(audits []domain.OriginalPlanningAudit, fromVolume, toVolume int) []domain.OriginalPlanningAudit {
	var out []domain.OriginalPlanningAudit
	for _, audit := range audits {
		if audit.Scope == "volume" && audit.Volume >= fromVolume && audit.Volume <= toVolume && audit.Verdict == "pass" {
			out = append(out, audit)
		}
	}
	return out
}

func originalPlanningAuditEvidenceJSON(audits []domain.OriginalPlanningAudit) string {
	data, _ := json.Marshal(audits)
	return string(data)
}

func findOriginalPlanningAudit(audits []domain.OriginalPlanningAudit, scope string, volume, arc, fromVolume, toVolume int) *domain.OriginalPlanningAudit {
	for i := range audits {
		audit := &audits[i]
		if audit.Scope == scope && audit.Volume == volume && audit.Arc == arc && audit.FromVolume == fromVolume && audit.ToVolume == toVolume {
			return audit
		}
	}
	return nil
}

// InvalidateRepair consumes chapter failures in the repaired window and
// removes the repaired arc plus enclosing volume/book audits. Earlier chapter
// passes outside the repair window remain valid; every enclosing gate reruns
// against the repaired causal chain.
func (s *OriginalPlanningAuditStore) InvalidateRepair(volume, arc, fromChapter, toChapter int) error {
	if volume <= 0 || arc <= 0 {
		return fmt.Errorf("repair audit invalidation requires volume and arc")
	}
	if (fromChapter == 0) != (toChapter == 0) || fromChapter < 0 || toChapter < fromChapter {
		return fmt.Errorf("repair audit invalidation requires a valid chapter window or 0-0")
	}
	return s.io.WithWriteLock(func() error {
		var audits []domain.OriginalPlanningAudit
		if err := s.io.ReadJSONUnlocked(originalPlanningAuditsFile, &audits); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		kept := audits[:0]
		for _, audit := range audits {
			removeChapter := audit.Scope == "chapter" && ((fromChapter > 0 && audit.FromChapter <= toChapter && audit.ToChapter >= fromChapter) ||
				(fromChapter == 0 && audit.Volume == volume && audit.Arc == arc))
			remove := removeChapter || audit.Scope == "book" ||
				(audit.Scope == "book_batch" && audit.FromVolume <= volume && audit.ToVolume >= volume) ||
				(audit.Scope == "volume" && audit.Volume == volume) ||
				(audit.Scope == "arc" && audit.Volume == volume && audit.Arc == arc) ||
				originalPlanningAuditRequestedRepair(audit, volume, arc)
			if !remove {
				kept = append(kept, audit)
			}
		}
		if len(kept) == 0 {
			return s.io.RemoveFileUnlocked(originalPlanningAuditsFile)
		}
		return s.io.WriteJSONUnlocked(originalPlanningAuditsFile, kept)
	})
}

func originalPlanningAuditRequestedRepair(audit domain.OriginalPlanningAudit, volume, arc int) bool {
	if audit.Verdict != "revise" {
		return false
	}
	for _, issue := range audit.Issues {
		if issue.Volume == volume && issue.Arc == arc {
			return true
		}
	}
	return false
}

// InvalidateSkeletonRepair reruns the changed volume, adjacent handoff
// volumes, and every synthesis that could depend on the replaced volume.
func (s *OriginalPlanningAuditStore) InvalidateSkeletonRepair(volume int) error {
	if volume <= 0 {
		return fmt.Errorf("skeleton repair audit invalidation requires volume")
	}
	return s.io.WithWriteLock(func() error {
		var audits []domain.OriginalPlanningAudit
		if err := s.io.ReadJSONUnlocked(originalPlanningAuditsFile, &audits); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		kept := audits[:0]
		for _, audit := range audits {
			remove := audit.Scope == "skeleton_book" ||
				(audit.Scope == "skeleton_book_batch" && audit.ToVolume >= volume-1 && audit.FromVolume <= volume+1) ||
				(audit.Scope == "skeleton_volume" && audit.Volume >= volume-1 && audit.Volume <= volume+1)
			if !remove {
				kept = append(kept, audit)
			}
		}
		if len(kept) == 0 {
			return s.io.RemoveFileUnlocked(originalPlanningAuditsFile)
		}
		return s.io.WriteJSONUnlocked(originalPlanningAuditsFile, kept)
	})
}

func sameOriginalPlanningAuditScope(a, b domain.OriginalPlanningAudit) bool {
	if a.Scope != b.Scope || a.Volume != b.Volume || a.Arc != b.Arc ||
		a.FromVolume != b.FromVolume || a.ToVolume != b.ToVolume {
		return false
	}
	if a.Scope == "chapter" {
		return a.ScopeID == b.ScopeID && a.FromChapter == b.FromChapter && a.ToChapter == b.ToChapter
	}
	return true
}

func originalPlanningAuditScopeRank(scope string) int {
	switch scope {
	case "skeleton_volume":
		return 0
	case "skeleton_book_batch":
		return 1
	case "skeleton_book":
		return 2
	case "arc":
		return 3
	case "volume":
		return 4
	case "book_batch":
		return 5
	case "book":
		return 6
	default:
		return 5
	}
}
