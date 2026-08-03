package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type structureIdentity struct {
	projectID string
}

func newStructureIdentity(dir string) structureIdentity {
	clean, err := filepath.Abs(strings.TrimSpace(dir))
	if err != nil {
		clean = filepath.Clean(strings.TrimSpace(dir))
	}
	projectID := filepath.ToSlash(clean)
	if strings.EqualFold(filepath.Base(clean), "output") {
		projectID = filepath.Base(filepath.Dir(clean))
	}
	return structureIdentity{projectID: projectID}
}

func (s structureIdentity) hydrateOutline(entries []domain.OutlineEntry) []domain.OutlineEntry {
	out := cloneOutlineEntries(entries)
	for i := range out {
		if strings.TrimSpace(out[i].ID) == "" {
			out[i].ID = s.legacyChapterID(out[i].Chapter, i+1)
		}
	}
	return out
}

func (s structureIdentity) hydrateLayeredOutline(volumes []domain.VolumeOutline) []domain.VolumeOutline {
	out := make([]domain.VolumeOutline, len(volumes))
	chapterOrder := 1
	for volumeIndex := range volumes {
		out[volumeIndex] = cloneVolumeOutline(volumes[volumeIndex])
		volumeNumber := positiveOr(volumes[volumeIndex].Index, volumeIndex+1)
		if strings.TrimSpace(out[volumeIndex].ID) == "" {
			out[volumeIndex].ID = domain.LegacyStructureID(
				s.projectID,
				domain.StructureKindVolume,
				fmt.Sprintf("volumes/%04d", volumeNumber),
			)
		}
		for arcIndex := range out[volumeIndex].Arcs {
			arcNumber := positiveOr(volumes[volumeIndex].Arcs[arcIndex].Index, arcIndex+1)
			arc := &out[volumeIndex].Arcs[arcIndex]
			if strings.TrimSpace(arc.ID) == "" {
				arc.ID = domain.LegacyStructureID(
					s.projectID,
					domain.StructureKindArc,
					fmt.Sprintf("volumes/%04d/arcs/%04d", volumeNumber, arcNumber),
				)
			}
			for chapterIndex := range arc.Chapters {
				entry := &arc.Chapters[chapterIndex]
				if strings.TrimSpace(entry.ID) == "" {
					entry.ID = s.legacyChapterID(entry.Chapter, chapterOrder)
				}
				chapterOrder++
			}
		}
	}
	return out
}

func (s structureIdentity) prepareOutlineForSave(
	entries []domain.OutlineEntry,
	existingEntries []domain.OutlineEntry,
	existingVolumes []domain.VolumeOutline,
) ([]domain.OutlineEntry, error) {
	out := cloneOutlineEntries(entries)
	legacyIDs := make(map[int]string)
	for _, entry := range s.hydrateOutline(existingEntries) {
		legacyIDs[entry.Chapter] = entry.ID
	}
	for _, entry := range domain.FlattenOutline(s.hydrateLayeredOutline(existingVolumes)) {
		if _, exists := legacyIDs[entry.Chapter]; !exists {
			legacyIDs[entry.Chapter] = entry.ID
		}
	}
	for i := range out {
		if strings.TrimSpace(out[i].ID) == "" {
			out[i].ID = legacyIDs[out[i].Chapter]
		}
	}
	if outlineChapterNumbersUseOrderProjection(out) {
		out = domain.ProjectOutlineOrder(out)
	}
	if err := assignMissingOutlineIDs(out); err != nil {
		return nil, err
	}
	if err := validateOutlineIDs(out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s structureIdentity) prepareLayeredOutlineForSave(
	volumes []domain.VolumeOutline,
	existingVolumes []domain.VolumeOutline,
	existingEntries []domain.OutlineEntry,
) ([]domain.VolumeOutline, error) {
	out := make([]domain.VolumeOutline, len(volumes))
	for i := range volumes {
		out[i] = cloneVolumeOutline(volumes[i])
	}
	legacyVolumes := s.hydrateLayeredOutline(existingVolumes)
	volumeIDs := make(map[int]string)
	arcIDs := make(map[[2]int]string)
	chapterIDs := make(map[int]string)
	for _, volume := range legacyVolumes {
		volumeIDs[volume.Index] = volume.ID
		for _, arc := range volume.Arcs {
			arcIDs[[2]int{volume.Index, arc.Index}] = arc.ID
			for _, entry := range arc.Chapters {
				chapterIDs[entry.Chapter] = entry.ID
			}
		}
	}
	for _, entry := range s.hydrateOutline(existingEntries) {
		if _, exists := chapterIDs[entry.Chapter]; !exists {
			chapterIDs[entry.Chapter] = entry.ID
		}
	}
	for volumeIndex := range out {
		volume := &out[volumeIndex]
		if strings.TrimSpace(volume.ID) == "" {
			volume.ID = volumeIDs[volume.Index]
		}
		for arcIndex := range volume.Arcs {
			arc := &volume.Arcs[arcIndex]
			if strings.TrimSpace(arc.ID) == "" {
				arc.ID = arcIDs[[2]int{volume.Index, arc.Index}]
			}
			for chapterIndex := range arc.Chapters {
				entry := &arc.Chapters[chapterIndex]
				if strings.TrimSpace(entry.ID) == "" {
					entry.ID = chapterIDs[entry.Chapter]
				}
			}
		}
	}
	out = domain.ProjectLayeredOutlineOrder(out)
	if err := assignMissingLayeredIDs(out); err != nil {
		return nil, err
	}
	if err := validateLayeredOutlineIDs(out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s structureIdentity) sourceIndex(entries []domain.OutlineEntry, volumes []domain.VolumeOutline) structureIndex {
	hydratedVolumes := domain.ProjectLayeredOutlineOrder(s.hydrateLayeredOutline(volumes))
	if len(hydratedVolumes) > 0 && len(domain.FlattenOutline(hydratedVolumes)) > 0 {
		return structureIndexFromLayered(hydratedVolumes)
	}
	return structureIndexFromOutline(domain.ProjectOutlineOrder(s.hydrateOutline(entries)))
}

func (s structureIdentity) targetIndexForOutline(
	entries []domain.OutlineEntry,
	volumes []domain.VolumeOutline,
) (structureIndex, []domain.VolumeOutline, error) {
	hydrated := domain.ProjectLayeredOutlineOrder(s.hydrateLayeredOutline(volumes))
	flat := domain.FlattenOutline(hydrated)
	if len(flat) == 0 {
		return structureIndexFromOutline(entries), nil, nil
	}
	if len(flat) != len(entries) {
		return structureIndex{}, nil, fmt.Errorf(
			"flat and layered outlines contain different chapter counts: %d != %d",
			len(entries), len(flat),
		)
	}
	chapter := 0
	for volumeIndex := range hydrated {
		for arcIndex := range hydrated[volumeIndex].Arcs {
			for chapterIndex := range hydrated[volumeIndex].Arcs[arcIndex].Chapters {
				hydrated[volumeIndex].Arcs[arcIndex].Chapters[chapterIndex] = entries[chapter]
				chapter++
			}
		}
	}
	hydrated = domain.ProjectLayeredOutlineOrder(hydrated)
	if err := validateLayeredOutlineIDs(hydrated); err != nil {
		return structureIndex{}, nil, err
	}
	return structureIndexFromLayered(hydrated), hydrated, nil
}

func (s structureIdentity) hydrateAdaptationPlan(plan *domain.AdaptationPlan) {
	if plan == nil {
		return
	}
	for i := range plan.Volumes {
		if strings.TrimSpace(plan.Volumes[i].ID) == "" {
			index := positiveOr(plan.Volumes[i].Index, i+1)
			plan.Volumes[i].ID = domain.LegacyStructureID(
				s.projectID,
				domain.StructureKindVolume,
				fmt.Sprintf("volumes/%04d", index),
			)
		}
	}
	for i := range plan.Chapters {
		if strings.TrimSpace(plan.Chapters[i].OutlineEntry.ID) == "" {
			plan.Chapters[i].OutlineEntry.ID = s.legacyChapterID(plan.Chapters[i].Chapter, i+1)
		}
	}
}

func (s structureIdentity) prepareAdaptationPlanForSave(plan *domain.AdaptationPlan, existing *domain.AdaptationPlan) error {
	if plan == nil {
		return nil
	}
	projectChapterNumbers := adaptationChapterNumbersUseOrderProjection(plan.Chapters)
	volumeIDs := make(map[string][]string)
	chapterIDs := make(map[string][]string)
	if existing != nil {
		s.hydrateAdaptationPlan(existing)
		for _, volume := range existing.Volumes {
			key, err := adaptationVolumeIdentityKey(volume)
			if err != nil {
				return err
			}
			volumeIDs[key] = append(volumeIDs[key], volume.ID)
		}
		for _, chapter := range existing.Chapters {
			key, err := adaptationChapterIdentityKey(chapter)
			if err != nil {
				return err
			}
			chapterIDs[key] = append(chapterIDs[key], chapter.OutlineEntry.ID)
		}
	}
	used := make(map[string]struct{})
	for i := range plan.Volumes {
		if strings.TrimSpace(plan.Volumes[i].ID) == "" {
			key, err := adaptationVolumeIdentityKey(plan.Volumes[i])
			if err != nil {
				return err
			}
			id, err := uniqueUnusedIdentity(volumeIDs[key], used, "adaptation volume")
			if err != nil {
				return err
			}
			plan.Volumes[i].ID = id
		}
		if strings.TrimSpace(plan.Volumes[i].ID) == "" {
			id, err := domain.NewStructureID(domain.StructureKindVolume)
			if err != nil {
				return err
			}
			plan.Volumes[i].ID = id
		}
		used[plan.Volumes[i].ID] = struct{}{}
		plan.Volumes[i].Index = i + 1
	}
	for i := range plan.Chapters {
		entry := &plan.Chapters[i].OutlineEntry
		if strings.TrimSpace(entry.ID) == "" {
			key, err := adaptationChapterIdentityKey(plan.Chapters[i])
			if err != nil {
				return err
			}
			id, err := uniqueUnusedIdentity(chapterIDs[key], used, "adaptation chapter")
			if err != nil {
				return err
			}
			entry.ID = id
		}
		if strings.TrimSpace(entry.ID) == "" {
			id, err := domain.NewStructureID(domain.StructureKindChapter)
			if err != nil {
				return err
			}
			entry.ID = id
		}
		used[entry.ID] = struct{}{}
		if projectChapterNumbers {
			plan.Chapters[i].Chapter = i + 1
		}
		plan.Chapters[i].OutlineEntry.Chapter = plan.Chapters[i].Chapter
	}
	return validateAdaptationPlanIDs(plan)
}

func adaptationChapterNumbersUseOrderProjection(chapters []domain.AdaptationChapterPlan) bool {
	seen := make(map[int]struct{}, len(chapters))
	for _, chapter := range chapters {
		if chapter.Chapter <= 0 {
			return true
		}
		if chapter.Chapter > len(chapters) {
			return false
		}
		if _, exists := seen[chapter.Chapter]; exists {
			return true
		}
		seen[chapter.Chapter] = struct{}{}
	}
	return len(seen) == len(chapters)
}

func outlineChapterNumbersUseOrderProjection(entries []domain.OutlineEntry) bool {
	seen := make(map[int]struct{}, len(entries))
	for _, entry := range entries {
		if entry.Chapter <= 0 {
			return true
		}
		if entry.Chapter > len(entries) {
			return false
		}
		if _, exists := seen[entry.Chapter]; exists {
			return true
		}
		seen[entry.Chapter] = struct{}{}
	}
	return len(seen) == len(entries)
}

func (s structureIdentity) hydrateAdaptationVolumeReview(review *domain.AdaptationVolumeReview) {
	if review == nil {
		return
	}
	for i := range review.Volumes {
		if strings.TrimSpace(review.Volumes[i].ID) == "" {
			index := positiveOr(review.Volumes[i].Index, i+1)
			review.Volumes[i].ID = domain.LegacyStructureID(
				s.projectID,
				domain.StructureKindVolume,
				fmt.Sprintf("volumes/%04d", index),
			)
		}
	}
}

func (s structureIdentity) prepareAdaptationVolumeReviewForSave(
	review *domain.AdaptationVolumeReview,
	existing *domain.AdaptationVolumeReview,
) error {
	if review == nil {
		return nil
	}
	existingIDs := make(map[string][]string)
	if existing != nil {
		s.hydrateAdaptationVolumeReview(existing)
		for _, volume := range existing.Volumes {
			key, err := adaptationVolumeIdentityKey(volume)
			if err != nil {
				return err
			}
			existingIDs[key] = append(existingIDs[key], volume.ID)
		}
	}
	seen := make(map[string]struct{}, len(review.Volumes))
	for i := range review.Volumes {
		if strings.TrimSpace(review.Volumes[i].ID) == "" {
			key, err := adaptationVolumeIdentityKey(review.Volumes[i])
			if err != nil {
				return err
			}
			id, err := uniqueUnusedIdentity(existingIDs[key], seen, "adaptation volume")
			if err != nil {
				return err
			}
			review.Volumes[i].ID = id
		}
		if strings.TrimSpace(review.Volumes[i].ID) == "" {
			id, err := domain.NewStructureID(domain.StructureKindVolume)
			if err != nil {
				return err
			}
			review.Volumes[i].ID = id
		}
		review.Volumes[i].Index = i + 1
		if err := addStructureID(seen, review.Volumes[i].ID, domain.StructureKindVolume); err != nil {
			return err
		}
	}
	return nil
}

func adaptationVolumeIdentityKey(volume domain.AdaptationVolumePlan) (string, error) {
	volume.ID = ""
	volume.Index = 0
	volume.TargetFrom = 0
	volume.TargetTo = 0
	data, err := json.Marshal(volume)
	if err != nil {
		return "", err
	}
	return identityDigest(data), nil
}

func adaptationChapterIdentityKey(chapter domain.AdaptationChapterPlan) (string, error) {
	chapter.ID = ""
	chapter.Chapter = 0
	chapter.OutlineEntry.Chapter = 0
	data, err := json.Marshal(chapter)
	if err != nil {
		return "", err
	}
	return identityDigest(data), nil
}

func identityDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func uniqueUnusedIdentity(candidates []string, used map[string]struct{}, kind string) (string, error) {
	available := make([]string, 0, len(candidates))
	for _, id := range candidates {
		if _, exists := used[id]; !exists {
			available = append(available, id)
		}
	}
	if len(available) > 1 {
		return "", fmt.Errorf("cannot unambiguously match ID-less %s", kind)
	}
	if len(available) == 1 {
		return available[0], nil
	}
	return "", nil
}

func assignMissingOutlineIDs(entries []domain.OutlineEntry) error {
	for i := range entries {
		if strings.TrimSpace(entries[i].ID) != "" {
			continue
		}
		id, err := domain.NewStructureID(domain.StructureKindChapter)
		if err != nil {
			return err
		}
		entries[i].ID = id
	}
	return nil
}

func assignMissingLayeredIDs(volumes []domain.VolumeOutline) error {
	for volumeIndex := range volumes {
		volume := &volumes[volumeIndex]
		if strings.TrimSpace(volume.ID) == "" {
			id, err := domain.NewStructureID(domain.StructureKindVolume)
			if err != nil {
				return err
			}
			volume.ID = id
		}
		for arcIndex := range volume.Arcs {
			arc := &volume.Arcs[arcIndex]
			if strings.TrimSpace(arc.ID) == "" {
				id, err := domain.NewStructureID(domain.StructureKindArc)
				if err != nil {
					return err
				}
				arc.ID = id
			}
			if err := assignMissingOutlineIDs(arc.Chapters); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateOutlineIDs(entries []domain.OutlineEntry) error {
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if err := addStructureID(seen, entry.ID, domain.StructureKindChapter); err != nil {
			return err
		}
	}
	return nil
}

func validateLayeredOutlineIDs(volumes []domain.VolumeOutline) error {
	seen := make(map[string]struct{})
	for _, volume := range volumes {
		if err := addStructureID(seen, volume.ID, domain.StructureKindVolume); err != nil {
			return err
		}
		for _, arc := range volume.Arcs {
			if err := addStructureID(seen, arc.ID, domain.StructureKindArc); err != nil {
				return err
			}
			for _, entry := range arc.Chapters {
				if err := addStructureID(seen, entry.ID, domain.StructureKindChapter); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateAdaptationPlanIDs(plan *domain.AdaptationPlan) error {
	seen := make(map[string]struct{})
	for _, volume := range plan.Volumes {
		if err := addStructureID(seen, volume.ID, domain.StructureKindVolume); err != nil {
			return err
		}
	}
	for _, chapter := range plan.Chapters {
		if err := addStructureID(seen, chapter.OutlineEntry.ID, domain.StructureKindChapter); err != nil {
			return err
		}
	}
	return nil
}

func addStructureID(seen map[string]struct{}, id, kind string) error {
	id = strings.TrimSpace(id)
	if !validStructureID(id, kind) {
		return fmt.Errorf("invalid %s ID %q", kind, id)
	}
	if _, exists := seen[id]; exists {
		return fmt.Errorf("duplicate structure ID %q", id)
	}
	seen[id] = struct{}{}
	return nil
}

func (s structureIdentity) legacyChapterID(chapter, fallback int) string {
	return domain.LegacyStructureID(
		s.projectID,
		domain.StructureKindChapter,
		fmt.Sprintf("chapters/%04d", positiveOr(chapter, fallback)),
	)
}

func positiveOr(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
