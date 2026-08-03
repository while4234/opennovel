package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

// StructureTopologySignature signs stable identities, parentage, and order
// without conflating topology with editable outline prose.
func StructureTopologySignature(volumes []VolumeOutline) string {
	type chapterNode struct {
		ID string `json:"id"`
	}
	type arcNode struct {
		ID       string        `json:"id"`
		Chapters []chapterNode `json:"chapters"`
	}
	type volumeNode struct {
		ID   string    `json:"id"`
		Arcs []arcNode `json:"arcs"`
	}

	ordered := ProjectLayeredOutlineOrder(CloneStructureSnapshot(volumes))
	topology := make([]volumeNode, 0, len(ordered))
	for _, volume := range ordered {
		item := volumeNode{ID: strings.TrimSpace(volume.ID)}
		for _, arc := range volume.Arcs {
			arcItem := arcNode{ID: strings.TrimSpace(arc.ID)}
			for _, chapter := range arc.Chapters {
				arcItem.Chapters = append(arcItem.Chapters, chapterNode{ID: strings.TrimSpace(chapter.ID)})
			}
			item.Arcs = append(item.Arcs, arcItem)
		}
		topology = append(topology, item)
	}
	payload, _ := json.Marshal(topology)
	return ContentSignature(payload)
}

// BindOriginalPlanningAudit captures the exact topology and scoped content
// reviewed by an original-fiction audit. It never accepts or emits source-
// novel fields.
func BindOriginalPlanningAudit(audit *OriginalPlanningAudit, volumes []VolumeOutline, foundationSignatures ...string) error {
	if audit == nil {
		return fmt.Errorf("original planning audit is required")
	}
	if err := validateOriginalPlanningBindingStructure(audit.Scope, volumes); err != nil {
		return fmt.Errorf("bind original planning audit structure: %w", err)
	}
	boundVolumes := CloneStructureSnapshot(volumes)
	if isOriginalPlanningSkeletonScope(audit.Scope) {
		boundVolumes = OriginalPlanningSkeletonProjection(boundVolumes)
	}
	content, err := originalPlanningAuditContent(*audit, boundVolumes)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("marshal original planning audit content: %w", err)
	}
	structure, err := originalPlanningAuditStructure(*audit, boundVolumes)
	if err != nil {
		return err
	}
	audit.StructureSignature = StructureTopologySignature(structure)
	audit.ContentSignature = ContentSignature(payload)
	if len(foundationSignatures) > 0 {
		audit.FoundationSignature = strings.TrimSpace(foundationSignatures[0])
	}
	return nil
}

func isOriginalPlanningSkeletonScope(scope string) bool {
	return scope == "skeleton_volume" || scope == "skeleton_book_batch" || scope == "skeleton_book"
}

// OriginalPlanningSkeletonProjection strips every detailed chapter while
// preserving stable volume/arc identity and each arc's reserved chapter count.
// Skeleton re-review therefore signs only the contract it actually reviewed.
func OriginalPlanningSkeletonProjection(volumes []VolumeOutline) []VolumeOutline {
	projected := ProjectLayeredOutlineOrder(CloneStructureSnapshot(volumes))
	for volumeIndex := range projected {
		for arcIndex := range projected[volumeIndex].Arcs {
			arc := &projected[volumeIndex].Arcs[arcIndex]
			if len(arc.Chapters) > 0 {
				arc.EstimatedChapters = len(arc.Chapters)
			}
			arc.Chapters = nil
		}
	}
	return projected
}

func originalPlanningAuditStructure(audit OriginalPlanningAudit, volumes []VolumeOutline) ([]VolumeOutline, error) {
	ordered := ProjectLayeredOutlineOrder(CloneStructureSnapshot(volumes))
	switch audit.Scope {
	case "chapter":
		for _, volume := range ordered {
			for _, arc := range volume.Arcs {
				for _, chapter := range arc.Chapters {
					if chapter.Chapter == audit.FromChapter && (strings.TrimSpace(audit.ScopeID) == "" || chapter.ID == audit.ScopeID) {
						arc.Chapters = []OutlineEntry{chapter}
						volume.Arcs = []ArcOutline{arc}
						return []VolumeOutline{volume}, nil
					}
				}
			}
		}
	case "arc":
		for _, volume := range ordered {
			if volume.Index != audit.Volume {
				continue
			}
			for _, arc := range volume.Arcs {
				if arc.Index == audit.Arc {
					volume.Arcs = []ArcOutline{arc}
					return []VolumeOutline{volume}, nil
				}
			}
		}
	case "skeleton_volume", "volume":
		for _, volume := range ordered {
			if volume.Index == audit.Volume {
				return []VolumeOutline{volume}, nil
			}
		}
	case "skeleton_book_batch", "book_batch":
		selected := make([]VolumeOutline, 0, audit.ToVolume-audit.FromVolume+1)
		for _, volume := range ordered {
			if volume.Index >= audit.FromVolume && volume.Index <= audit.ToVolume {
				selected = append(selected, volume)
			}
		}
		if len(selected) == audit.ToVolume-audit.FromVolume+1 {
			return selected, nil
		}
	case "skeleton_book", "book":
		return ordered, nil
	}
	return nil, fmt.Errorf("original planning audit scope %q target is not present", audit.Scope)
}

func validateOriginalPlanningBindingStructure(scope string, volumes []VolumeOutline) error {
	if scope != "skeleton_volume" && scope != "skeleton_book_batch" && scope != "skeleton_book" {
		return ValidateStructureSnapshot(volumes)
	}
	if len(volumes) == 0 {
		return fmt.Errorf("structure must contain at least one volume")
	}
	seen := make(map[string]string)
	for _, volume := range volumes {
		if err := validateStructureNodeID(seen, volume.ID, StructureKindVolume); err != nil {
			return err
		}
		if len(volume.Arcs) == 0 {
			return fmt.Errorf("skeleton volume %q must contain at least one arc", volume.ID)
		}
		for _, arc := range volume.Arcs {
			if err := validateStructureNodeID(seen, arc.ID, StructureKindArc); err != nil {
				return err
			}
		}
	}
	return nil
}

func OriginalPlanningAuditCurrent(audit OriginalPlanningAudit, volumes []VolumeOutline, foundationSignature string) bool {
	if audit.Verdict != "pass" {
		return false
	}
	if strings.TrimSpace(audit.FoundationSignature) == "" {
		return false
	}
	if audit.FoundationSignature != strings.TrimSpace(foundationSignature) {
		return false
	}
	return OriginalPlanningAuditBindingCurrent(audit, volumes)
}

func OriginalPlanningAuditCurrentWithFoundation(audit OriginalPlanningAudit, volumes []VolumeOutline, foundation StoryFoundation, foundationSignature string) bool {
	if OriginalPlanningAuditCurrent(audit, volumes, foundationSignature) {
		return true
	}
	if audit.Verdict != "pass" || len(audit.FoundationEntityRefs) == 0 || strings.TrimSpace(audit.FoundationProjectionSignature) == "" || !OriginalPlanningAuditBindingCurrent(audit, volumes) {
		return false
	}
	current, err := FoundationProjectionSignature(foundation, audit.FoundationEntityRefs)
	return err == nil && current == audit.FoundationProjectionSignature
}

// OriginalPlanningAuditBindingCurrent reports whether the reviewed structure
// and content still match, independently of the audit verdict.
func OriginalPlanningAuditBindingCurrent(audit OriginalPlanningAudit, volumes []VolumeOutline) bool {
	if strings.TrimSpace(audit.StructureSignature) == "" || strings.TrimSpace(audit.ContentSignature) == "" {
		return false
	}
	current := audit
	if err := BindOriginalPlanningAudit(&current, volumes); err != nil {
		return false
	}
	return audit.StructureSignature == current.StructureSignature && audit.ContentSignature == current.ContentSignature
}

func originalPlanningAuditContent(audit OriginalPlanningAudit, volumes []VolumeOutline) (any, error) {
	ordered := ProjectLayeredOutlineOrder(CloneStructureSnapshot(volumes))
	switch audit.Scope {
	case "chapter":
		chapters := FlattenOutline(ordered)
		selected := make([]OutlineEntry, 0, audit.ToChapter-audit.FromChapter+1)
		for _, chapter := range chapters {
			if chapter.Chapter >= audit.FromChapter && chapter.Chapter <= audit.ToChapter &&
				(strings.TrimSpace(audit.ScopeID) == "" || chapter.ID == audit.ScopeID) {
				selected = append(selected, chapter)
			}
		}
		if audit.FromChapter <= 0 || audit.ToChapter < audit.FromChapter || len(selected) != audit.ToChapter-audit.FromChapter+1 {
			return nil, fmt.Errorf("chapter audit range %d-%d is not present", audit.FromChapter, audit.ToChapter)
		}
		return selected, nil
	case "arc":
		for _, volume := range ordered {
			if volume.Index != audit.Volume {
				continue
			}
			for _, arc := range volume.Arcs {
				if arc.Index == audit.Arc {
					return arc, nil
				}
			}
		}
		return nil, fmt.Errorf("arc audit target V%d A%d is not present", audit.Volume, audit.Arc)
	case "skeleton_volume", "volume":
		for _, volume := range ordered {
			if volume.Index == audit.Volume {
				return volume, nil
			}
		}
		return nil, fmt.Errorf("volume audit target V%d is not present", audit.Volume)
	case "skeleton_book_batch", "book_batch":
		selected := make([]VolumeOutline, 0, audit.ToVolume-audit.FromVolume+1)
		for _, volume := range ordered {
			if volume.Index >= audit.FromVolume && volume.Index <= audit.ToVolume {
				selected = append(selected, volume)
			}
		}
		if audit.FromVolume <= 0 || audit.ToVolume < audit.FromVolume || len(selected) != audit.ToVolume-audit.FromVolume+1 {
			return nil, fmt.Errorf("book batch audit range V%d-V%d is not present", audit.FromVolume, audit.ToVolume)
		}
		return selected, nil
	case "skeleton_book", "book":
		return ordered, nil
	default:
		return nil, fmt.Errorf("unsupported original planning audit scope %q", audit.Scope)
	}
}
