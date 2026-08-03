package store

import (
	"encoding/json"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// CaptureManuscriptContentProvenance freezes the generation contract beside
// the exact formal prose bytes. Later discussion requests compare this trusted
// record with freshly read plan/manifest state instead of comparing two live
// reads with each other.
func (s *Store) CaptureManuscriptContentProvenance(chapter int, prose string) error {
	outline, err := s.Outline.LoadOutline()
	if err != nil {
		return err
	}
	var entry *domain.OutlineEntry
	for i := range outline {
		if outline[i].Chapter == chapter {
			copy := outline[i]
			entry = &copy
			break
		}
	}
	if entry == nil {
		entry = &domain.OutlineEntry{Chapter: chapter}
	}
	if entry.ID == "" {
		entry.ID = domain.LegacyStructureID(s.Dir(), domain.StructureKindChapter, fmt.Sprintf("chapters/%04d", chapter))
	}
	outlinePayload, _ := json.Marshal(entry)
	provenance := ManuscriptContentProvenance{
		ChapterID: entry.ID, ContentSHA256: domain.ContentSignature([]byte(prose)),
		ApprovedOutlineSHA256: domain.ContentSignature(outlinePayload), Mode: domain.RevisionModeNormal,
	}
	manifest, err := s.Adaptation.LoadSourceManifest()
	if err != nil {
		return err
	}
	if manifest != nil {
		plan, planErr := s.Adaptation.LoadPlan()
		if planErr != nil || plan == nil || plan.Status != domain.AdaptationPlanStatusConfirmed {
			return fmt.Errorf("adaptation content requires confirmed plan provenance: %w", planErr)
		}
		planPayload, _ := json.Marshal(plan)
		manifestPayload, _ := json.Marshal(manifest)
		provenance.Mode = domain.RevisionModeAdaptation
		provenance.AdaptationPlanSHA256 = domain.ContentSignature(planPayload)
		provenance.SourceManifestSHA256 = domain.ContentSignature(manifestPayload)
	}
	return s.ManuscriptRevisions.BindContentProvenance(provenance)
}
