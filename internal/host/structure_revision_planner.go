package host

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"weak"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// StructureRevisionPlanner is implemented separately by normal-fiction and
// adaptation policies. The kernel deliberately knows nothing about prompts,
// source fidelity, or mode-specific quality rules.
type StructureRevisionPlanner interface {
	PlanStructure(context.Context, domain.StructureRevisionRequest) (domain.StructureRevisionProposal, error)
}

// StructurePlanningKernel identifies the private scope that seals its previews.
// A preview must be confirmed through the same kernel instance that planned it.
// The marker keeps instances out of tiny-allocation batching so weak registry
// entries can be reclaimed promptly. Distinct values have distinct addresses,
// while all sensitive and synchronized state lives outside this copyable value.
type StructurePlanningKernel struct {
	instanceMarker [32]byte
}

type structurePreviewScope struct {
	signingKey []byte
}

type structurePreviewScopeRegistry struct {
	mu     sync.Mutex
	scopes map[weak.Pointer[StructurePlanningKernel]]*structurePreviewScope
}

type structurePreviewScopeCleanup struct {
	identity weak.Pointer[StructurePlanningKernel]
	scope    *structurePreviewScope
}

var structurePreviewScopes structurePreviewScopeRegistry

func newStructurePreviewSigningKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(fmt.Sprintf("initialize structure preview signing key: %v", err))
	}
	return key
}

func (kernel *StructurePlanningKernel) Plan(
	ctx context.Context,
	planner StructureRevisionPlanner,
	request domain.StructureRevisionRequest,
) (*domain.StructureRevisionPreview, error) {
	if planner == nil {
		return nil, fmt.Errorf("structure revision planner is required")
	}
	request.Intent = strings.TrimSpace(request.Intent)
	request.TargetID = strings.TrimSpace(request.TargetID)
	request.DestinationID = strings.TrimSpace(request.DestinationID)
	request.CompletedChapterIDs = append([]string(nil), request.CompletedChapterIDs...)
	for index := range request.CompletedChapterIDs {
		request.CompletedChapterIDs[index] = strings.TrimSpace(request.CompletedChapterIDs[index])
	}
	slices.Sort(request.CompletedChapterIDs)
	request.CompletedChapterIDs = slices.Compact(request.CompletedChapterIDs)
	if err := request.Validate(); err != nil {
		return nil, err
	}
	formal := domain.ProjectLayeredOutlineOrder(domain.CloneStructureSnapshot(request.Current))
	plannerRequest := request
	plannerRequest.Current = domain.CloneStructureSnapshot(formal)
	plannerRequest.CompletedChapterIDs = append([]string(nil), request.CompletedChapterIDs...)
	if request.CurrentSoftBudget != nil {
		budget := *request.CurrentSoftBudget
		plannerRequest.CurrentSoftBudget = &budget
	}

	proposal, err := planner.PlanStructure(ctx, plannerRequest)
	if err != nil {
		return nil, fmt.Errorf("plan structure revision: %w", err)
	}
	proposal.Candidate = domain.ProjectLayeredOutlineOrder(domain.CloneStructureSnapshot(proposal.Candidate))
	if err := normalizeStructureProposal(request, formal, &proposal); err != nil {
		return nil, err
	}
	preview := &domain.StructureRevisionPreview{
		BaseRevision:           request.BaseRevision,
		BaseStructureSignature: domain.StructureSignature(formal),
		Operation:              request.Operation,
		Stage:                  request.Stage,
		Intent:                 request.Intent,
		TargetID:               request.TargetID,
		DestinationID:          request.DestinationID,
		CompletedChapterIDs:    append([]string(nil), request.CompletedChapterIDs...),
		Proposal:               cloneStructureRevisionProposal(proposal),
	}
	if request.CurrentSoftBudget != nil {
		budget := *request.CurrentSoftBudget
		preview.CurrentSoftBudget = &budget
	}
	preview.Signature, err = kernel.signStructureRevisionPreview(*preview)
	if err != nil {
		return nil, err
	}
	return preview, nil
}

func (kernel *StructurePlanningKernel) Confirm(
	preview domain.StructureRevisionPreview,
	currentRevision int,
	current []domain.VolumeOutline,
) ([]domain.VolumeOutline, error) {
	if preview.BaseRevision != currentRevision ||
		preview.BaseStructureSignature != domain.StructureSignature(current) {
		return nil, domain.ErrStructurePreviewStale
	}
	expectedSignature, err := kernel.signStructureRevisionPreview(preview)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(preview.Signature) == "" || !hmac.Equal([]byte(preview.Signature), []byte(expectedSignature)) {
		return nil, domain.ErrStructurePreviewTampered
	}
	formal := domain.ProjectLayeredOutlineOrder(domain.CloneStructureSnapshot(current))
	request := domain.StructureRevisionRequest{
		Operation:           preview.Operation,
		Intent:              preview.Intent,
		Stage:               preview.Stage,
		TargetID:            preview.TargetID,
		DestinationID:       preview.DestinationID,
		BaseRevision:        preview.BaseRevision,
		Current:             formal,
		CompletedChapterIDs: append([]string(nil), preview.CompletedChapterIDs...),
	}
	if preview.CurrentSoftBudget != nil {
		budget := *preview.CurrentSoftBudget
		request.CurrentSoftBudget = &budget
	}
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("validate structure revision preview request: %w", err)
	}
	proposal := cloneStructureRevisionProposal(preview.Proposal)
	if err := normalizeStructureProposal(request, formal, &proposal); err != nil {
		return nil, fmt.Errorf("validate normalized structure revision preview: %w", err)
	}
	return domain.CloneStructureSnapshot(proposal.Candidate), nil
}

func (kernel *StructurePlanningKernel) signStructureRevisionPreview(preview domain.StructureRevisionPreview) (string, error) {
	if kernel == nil {
		return "", fmt.Errorf("structure planning kernel is required")
	}
	preview.Signature = ""
	payload, err := json.Marshal(preview)
	if err != nil {
		return "", fmt.Errorf("marshal structure revision preview: %w", err)
	}
	mac := hmac.New(sha256.New, kernel.structurePreviewScope().signingKey)
	if _, err := mac.Write(payload); err != nil {
		return "", fmt.Errorf("sign structure revision preview: %w", err)
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (kernel *StructurePlanningKernel) structurePreviewScope() *structurePreviewScope {
	structurePreviewScopes.mu.Lock()
	defer structurePreviewScopes.mu.Unlock()
	if structurePreviewScopes.scopes == nil {
		structurePreviewScopes.scopes = make(map[weak.Pointer[StructurePlanningKernel]]*structurePreviewScope)
	}
	identity := weak.Make(kernel)
	if scope, ok := structurePreviewScopes.scopes[identity]; ok {
		runtime.KeepAlive(kernel)
		return scope
	}
	scope := &structurePreviewScope{signingKey: newStructurePreviewSigningKey()}
	structurePreviewScopes.scopes[identity] = scope
	runtime.AddCleanup(kernel, cleanupStructurePreviewScope, structurePreviewScopeCleanup{
		identity: identity,
		scope:    scope,
	})
	runtime.KeepAlive(kernel)
	return scope
}

func (kernel *StructurePlanningKernel) restoreSigningKey(key []byte) {
	if kernel == nil || len(key) != 32 {
		return
	}
	scope := kernel.structurePreviewScope()
	scope.signingKey = append(scope.signingKey[:0], key...)
}

func (kernel *StructurePlanningKernel) signingKeyCopy() []byte {
	if kernel == nil {
		return nil
	}
	return append([]byte(nil), kernel.structurePreviewScope().signingKey...)
}

func cleanupStructurePreviewScope(cleanup structurePreviewScopeCleanup) {
	structurePreviewScopes.mu.Lock()
	defer structurePreviewScopes.mu.Unlock()
	if scope, ok := structurePreviewScopes.scopes[cleanup.identity]; ok && scope == cleanup.scope {
		delete(structurePreviewScopes.scopes, cleanup.identity)
	}
}

func normalizeStructureProposal(
	request domain.StructureRevisionRequest,
	formal []domain.VolumeOutline,
	proposal *domain.StructureRevisionProposal,
) error {
	if proposal == nil {
		return fmt.Errorf("structure revision proposal is required")
	}
	if err := proposal.Assessment.Validate(); err != nil {
		return err
	}
	if err := domain.ValidateStructureSnapshotForStage(proposal.Candidate, request.Stage); err != nil {
		return fmt.Errorf("validate candidate structure: %w", err)
	}
	if err := validateOperationDelta(request, formal, proposal.Candidate); err != nil {
		return err
	}
	formalChapters := domain.StructureChapterIDs(formal)
	candidateChapters := domain.StructureChapterIDs(proposal.Candidate)
	addedChapters := difference(candidateChapters, formalChapters)
	if !proposal.Assessment.NeedsAdditionalChapters && len(addedChapters) > 0 {
		return fmt.Errorf("planner added chapters after deciding that new content is unnecessary")
	}
	if proposal.Assessment.NeedsAdditionalChapters {
		switch request.Operation {
		case domain.StructureRevisionInsertChapter, domain.StructureRevisionAppendChapter, domain.StructureRevisionSplitChapter:
			if len(addedChapters) == 0 {
				return fmt.Errorf("planner decided additional chapters are needed but did not add one")
			}
		case domain.StructureRevisionAppendArc:
			if structureArcCount(proposal.Candidate) <= structureArcCount(formal) {
				return fmt.Errorf("planner decided an additional arc is needed but did not add one")
			}
		case domain.StructureRevisionAppendVolume:
			if len(proposal.Candidate) <= len(formal) {
				return fmt.Errorf("planner decided an additional volume is needed but did not add one")
			}
		}
	}
	newVolumes := len(proposal.Candidate) - len(formal)
	if newVolumes > 0 && (proposal.Assessment.NewVolume == nil || !proposal.Assessment.NewVolume.SupportsNewVolume()) {
		return fmt.Errorf("candidate adds a volume without independent dramatic-stage evidence")
	}
	if request.Operation == domain.StructureRevisionMoveChapter && len(addedChapters) > 0 {
		return fmt.Errorf("moving an unwritten chapter cannot add chapters")
	}
	if request.Operation == domain.StructureRevisionExpandChapter && len(addedChapters) > 0 {
		return fmt.Errorf("expanding an existing chapter cannot add chapters")
	}
	if request.Operation == domain.StructureRevisionExpandChapter {
		if outlineContentEqual(chapterEntriesByID(formal)[request.TargetID], chapterEntriesByID(proposal.Candidate)[request.TargetID]) {
			return fmt.Errorf("expanding chapter %q must change that chapter outline", request.TargetID)
		}
	}
	candidateCount := domain.TotalChapters(proposal.Candidate)
	if proposal.SoftBudget.EstimatedChapters != candidateCount {
		return fmt.Errorf("soft budget estimates %d chapters, candidate contains %d", proposal.SoftBudget.EstimatedChapters, candidateCount)
	}
	impacts, err := normalizeStructureImpacts(request, formal, proposal.Candidate, proposal.Impacts)
	if err != nil {
		return err
	}
	proposal.Impacts = impacts
	return proposal.ValidateForStage(request.Stage)
}

func requireStableStructureIdentities(current, candidate []domain.VolumeOutline) error {
	candidateIDs := make(map[string]struct{})
	for _, volume := range candidate {
		candidateIDs[volume.ID] = struct{}{}
		for _, arc := range volume.Arcs {
			candidateIDs[arc.ID] = struct{}{}
			for _, chapter := range arc.Chapters {
				candidateIDs[chapter.ID] = struct{}{}
			}
		}
	}
	for _, volume := range current {
		if _, exists := candidateIDs[volume.ID]; !exists {
			return fmt.Errorf("candidate removed or replaced stable volume ID %q", volume.ID)
		}
		for _, arc := range volume.Arcs {
			if _, exists := candidateIDs[arc.ID]; !exists {
				return fmt.Errorf("candidate removed or replaced stable arc ID %q", arc.ID)
			}
			for _, chapter := range arc.Chapters {
				if _, exists := candidateIDs[chapter.ID]; !exists {
					return fmt.Errorf("candidate removed or replaced stable chapter ID %q", chapter.ID)
				}
			}
		}
	}
	return nil
}

type structureNodeLocation struct {
	Kind     string
	ParentID string
	VolumeID string
	Index    int
}

func validateOperationDelta(request domain.StructureRevisionRequest, current, candidate []domain.VolumeOutline) error {
	if err := requireStableStructureIdentities(current, candidate); err != nil {
		return err
	}
	currentLocations := structureNodeLocations(current)
	candidateLocations := structureNodeLocations(candidate)
	for id, location := range currentLocations {
		candidateLocation := candidateLocations[id]
		if candidateLocation.Kind != location.Kind {
			return fmt.Errorf("stable structure ID %q changed kind from %s to %s", id, location.Kind, candidateLocation.Kind)
		}
		if candidateLocation.ParentID != location.ParentID && !(request.Operation == domain.StructureRevisionMoveChapter && id == request.TargetID) {
			return fmt.Errorf("structure node %q changed parent from %q to %q without an authorized move", id, location.ParentID, candidateLocation.ParentID)
		}
	}
	if err := requireUnchangedExistingOrder(request, current, candidate, currentLocations); err != nil {
		return err
	}
	added := addedStructureNodes(currentLocations, candidateLocations)
	requireAdded := func(volumes, arcs, chapters int) error {
		if len(added[domain.StructureKindVolume]) != volumes || len(added[domain.StructureKindArc]) != arcs || len(added[domain.StructureKindChapter]) != chapters {
			return fmt.Errorf("operation %q added volume/arc/chapter counts %d/%d/%d, want %d/%d/%d", request.Operation,
				len(added[domain.StructureKindVolume]), len(added[domain.StructureKindArc]), len(added[domain.StructureKindChapter]), volumes, arcs, chapters)
		}
		return nil
	}
	switch request.Operation {
	case domain.StructureRevisionExpandChapter:
		if err := requireAdded(0, 0, 0); err != nil {
			return err
		}
		if outlineContentEqual(chapterEntriesByID(current)[request.TargetID], chapterEntriesByID(candidate)[request.TargetID]) {
			return fmt.Errorf("expanding chapter %q must change that chapter outline", request.TargetID)
		}
	case domain.StructureRevisionInsertChapter:
		if len(added[domain.StructureKindVolume]) == 1 {
			if err := requireAdded(1, 1, 1); err != nil {
				return err
			}
			newVolumeID, newArcID, newChapterID := added[domain.StructureKindVolume][0], added[domain.StructureKindArc][0], added[domain.StructureKindChapter][0]
			targetVolumeIndex := currentLocations[request.TargetID].VolumeID
			if candidateLocations[newVolumeID].Index != candidateLocations[targetVolumeIndex].Index+1 ||
				candidateLocations[newArcID].ParentID != newVolumeID || candidateLocations[newChapterID].ParentID != newArcID {
				return fmt.Errorf("inserted dramatic-stage volume must contain exactly the inserted arc and chapter immediately after the target volume")
			}
		} else {
			if err := requireAdded(0, 0, 1); err != nil {
				return err
			}
			newID := added[domain.StructureKindChapter][0]
			newLocation, targetLocation := candidateLocations[newID], candidateLocations[request.TargetID]
			if newLocation.ParentID != targetLocation.ParentID || newLocation.Index+1 != targetLocation.Index {
				return fmt.Errorf("inserted chapter %q must be immediately before target chapter %q in the same arc", newID, request.TargetID)
			}
		}
	case domain.StructureRevisionSplitChapter:
		if err := requireAdded(0, 0, 1); err != nil {
			return err
		}
		newID := added[domain.StructureKindChapter][0]
		newLocation, targetLocation := candidateLocations[newID], candidateLocations[request.TargetID]
		if newLocation.ParentID != targetLocation.ParentID || newLocation.Index != targetLocation.Index+1 {
			return fmt.Errorf("split chapter %q must create exactly one chapter immediately after it in the same arc", request.TargetID)
		}
		if outlineContentEqual(chapterEntriesByID(current)[request.TargetID], chapterEntriesByID(candidate)[request.TargetID]) {
			return fmt.Errorf("split chapter %q must revise the retained chapter outline", request.TargetID)
		}
	case domain.StructureRevisionAppendChapter:
		if err := requireAdded(0, 0, 1); err != nil {
			return err
		}
		lastVolume := current[len(current)-1]
		if len(lastVolume.Arcs) == 0 {
			return fmt.Errorf("cannot append a chapter without an existing destination arc")
		}
		lastArc := lastVolume.Arcs[len(lastVolume.Arcs)-1]
		newLocation := candidateLocations[added[domain.StructureKindChapter][0]]
		if newLocation.ParentID != lastArc.ID || newLocation.Index != len(lastArc.Chapters) {
			return fmt.Errorf("appended chapter must be the final chapter of arc %q", lastArc.ID)
		}
	case domain.StructureRevisionAppendArc:
		if len(added[domain.StructureKindArc]) != 1 || len(added[domain.StructureKindVolume]) != 0 ||
			(len(added[domain.StructureKindChapter]) == 0 && request.Stage != domain.ManuscriptStageProposalComplete) {
			return fmt.Errorf("append_arc must add exactly one arc and its non-empty chapter set")
		}
		lastVolume := current[len(current)-1]
		newArcID := added[domain.StructureKindArc][0]
		newArcLocation := candidateLocations[newArcID]
		if newArcLocation.ParentID != lastVolume.ID || newArcLocation.Index != len(lastVolume.Arcs) {
			return fmt.Errorf("appended arc %q must be the final arc of volume %q", newArcID, lastVolume.ID)
		}
		for _, chapterID := range added[domain.StructureKindChapter] {
			if candidateLocations[chapterID].ParentID != newArcID {
				return fmt.Errorf("append_arc added unrelated chapter %q outside arc %q", chapterID, newArcID)
			}
		}
	case domain.StructureRevisionAppendVolume:
		if len(added[domain.StructureKindVolume]) != 1 || len(added[domain.StructureKindArc]) == 0 ||
			(len(added[domain.StructureKindChapter]) == 0 && request.Stage != domain.ManuscriptStageProposalComplete) {
			return fmt.Errorf("append_volume must add exactly one volume with non-empty arcs and chapters")
		}
		newVolumeID := added[domain.StructureKindVolume][0]
		if candidateLocations[newVolumeID].Index != len(current) {
			return fmt.Errorf("appended volume %q must be the final volume", newVolumeID)
		}
		newArcs := make(map[string]struct{}, len(added[domain.StructureKindArc]))
		for _, arcID := range added[domain.StructureKindArc] {
			if candidateLocations[arcID].ParentID != newVolumeID {
				return fmt.Errorf("append_volume added unrelated arc %q outside volume %q", arcID, newVolumeID)
			}
			newArcs[arcID] = struct{}{}
		}
		for _, chapterID := range added[domain.StructureKindChapter] {
			if _, belongs := newArcs[candidateLocations[chapterID].ParentID]; !belongs {
				return fmt.Errorf("append_volume added unrelated chapter %q outside volume %q", chapterID, newVolumeID)
			}
		}
	case domain.StructureRevisionMoveChapter:
		if err := requireAdded(0, 0, 0); err != nil {
			return err
		}
		if !outlineContentEqual(chapterEntriesByID(current)[request.TargetID], chapterEntriesByID(candidate)[request.TargetID]) {
			return fmt.Errorf("moving chapter %q cannot change its outline content", request.TargetID)
		}
		if err := requireMoveDestination(request, candidate, candidateLocations); err != nil {
			return err
		}
	}
	return nil
}

func structureNodeLocations(volumes []domain.VolumeOutline) map[string]structureNodeLocation {
	locations := make(map[string]structureNodeLocation)
	for volumeIndex, volume := range volumes {
		locations[volume.ID] = structureNodeLocation{Kind: domain.StructureKindVolume, Index: volumeIndex, VolumeID: volume.ID}
		for arcIndex, arc := range volume.Arcs {
			locations[arc.ID] = structureNodeLocation{Kind: domain.StructureKindArc, ParentID: volume.ID, VolumeID: volume.ID, Index: arcIndex}
			for chapterIndex, chapter := range arc.Chapters {
				locations[chapter.ID] = structureNodeLocation{Kind: domain.StructureKindChapter, ParentID: arc.ID, VolumeID: volume.ID, Index: chapterIndex}
			}
		}
	}
	return locations
}

func addedStructureNodes(current, candidate map[string]structureNodeLocation) map[string][]string {
	added := map[string][]string{
		domain.StructureKindVolume:  nil,
		domain.StructureKindArc:     nil,
		domain.StructureKindChapter: nil,
	}
	for id, location := range candidate {
		if _, exists := current[id]; !exists {
			added[location.Kind] = append(added[location.Kind], id)
		}
	}
	for kind := range added {
		slices.Sort(added[kind])
	}
	return added
}

func requireUnchangedExistingOrder(request domain.StructureRevisionRequest, current, candidate []domain.VolumeOutline, existing map[string]structureNodeLocation) error {
	currentChildren := structureChildrenByParent(current, existing, request)
	candidateChildren := structureChildrenByParent(candidate, existing, request)
	if !reflect.DeepEqual(currentChildren, candidateChildren) {
		return fmt.Errorf("operation %q reordered unrelated existing structure nodes", request.Operation)
	}
	return nil
}

func structureChildrenByParent(volumes []domain.VolumeOutline, existing map[string]structureNodeLocation, request domain.StructureRevisionRequest) map[string][]string {
	children := map[string][]string{"": {}}
	appendExisting := func(parentID, id string) {
		if _, exists := existing[id]; !exists || (request.Operation == domain.StructureRevisionMoveChapter && id == request.TargetID) {
			return
		}
		children[parentID] = append(children[parentID], id)
	}
	for _, volume := range volumes {
		appendExisting("", volume.ID)
		for _, arc := range volume.Arcs {
			appendExisting(volume.ID, arc.ID)
			for _, chapter := range arc.Chapters {
				appendExisting(arc.ID, chapter.ID)
			}
		}
	}
	return children
}

func requireMoveDestination(request domain.StructureRevisionRequest, candidate []domain.VolumeOutline, locations map[string]structureNodeLocation) error {
	target := locations[request.TargetID]
	destination := locations[request.DestinationID]
	if request.TargetID == request.DestinationID {
		return fmt.Errorf("move destination cannot be the target chapter")
	}
	switch destination.Kind {
	case domain.StructureKindChapter:
		if target.ParentID != destination.ParentID || target.Index+1 != destination.Index {
			return fmt.Errorf("moved chapter %q must be immediately before destination chapter %q", request.TargetID, request.DestinationID)
		}
	case domain.StructureKindArc:
		if target.ParentID != request.DestinationID || target.Index != len(chaptersForArc(candidate, request.DestinationID))-1 {
			return fmt.Errorf("moved chapter %q must be appended to destination arc %q", request.TargetID, request.DestinationID)
		}
	case domain.StructureKindVolume:
		var destinationArc *domain.ArcOutline
		for volumeIndex := range candidate {
			if candidate[volumeIndex].ID == request.DestinationID && len(candidate[volumeIndex].Arcs) > 0 {
				destinationArc = &candidate[volumeIndex].Arcs[len(candidate[volumeIndex].Arcs)-1]
				break
			}
		}
		if destinationArc == nil || target.ParentID != destinationArc.ID || target.Index != len(destinationArc.Chapters)-1 {
			return fmt.Errorf("moved chapter %q must be appended to the final arc of destination volume %q", request.TargetID, request.DestinationID)
		}
	default:
		return fmt.Errorf("move destination %q has unsupported kind %q", request.DestinationID, destination.Kind)
	}
	return nil
}

func chaptersForArc(volumes []domain.VolumeOutline, arcID string) []domain.OutlineEntry {
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			if arc.ID == arcID {
				return arc.Chapters
			}
		}
	}
	return nil
}

func normalizeStructureImpacts(
	request domain.StructureRevisionRequest,
	current, candidate []domain.VolumeOutline,
	provided []domain.StructureImpactItem,
) ([]domain.StructureImpactItem, error) {
	impacts := append([]domain.StructureImpactItem(nil), provided...)
	locations := structureNodeLocations(candidate)
	for index := range impacts {
		impacts[index].ArtifactID = strings.TrimSpace(impacts[index].ArtifactID)
		impacts[index].ArtifactKind = strings.TrimSpace(impacts[index].ArtifactKind)
		impacts[index].Change = strings.TrimSpace(impacts[index].Change)
		impacts[index].DependencyEvidence = append([]string(nil), impacts[index].DependencyEvidence...)
		impacts[index].DependencySourceIDs = append([]string(nil), impacts[index].DependencySourceIDs...)
		for evidenceIndex := range impacts[index].DependencyEvidence {
			impacts[index].DependencyEvidence[evidenceIndex] = strings.TrimSpace(impacts[index].DependencyEvidence[evidenceIndex])
		}
		for sourceIndex := range impacts[index].DependencySourceIDs {
			impacts[index].DependencySourceIDs[sourceIndex] = strings.TrimSpace(impacts[index].DependencySourceIDs[sourceIndex])
		}
		slices.Sort(impacts[index].DependencyEvidence)
		impacts[index].DependencyEvidence = slices.Compact(impacts[index].DependencyEvidence)
		slices.Sort(impacts[index].DependencySourceIDs)
		impacts[index].DependencySourceIDs = slices.Compact(impacts[index].DependencySourceIDs)
		if err := impacts[index].Validate(); err != nil {
			return nil, err
		}
		location, exists := locations[impacts[index].ArtifactID]
		if !exists {
			return nil, fmt.Errorf("structure impact references unknown artifact %q", impacts[index].ArtifactID)
		}
		if impacts[index].ArtifactKind != location.Kind {
			return nil, fmt.Errorf("structure impact %q kind %q does not match candidate kind %q", impacts[index].ArtifactID, impacts[index].ArtifactKind, location.Kind)
		}
	}
	byID := make(map[string]int, len(impacts))
	for index := range impacts {
		if _, duplicate := byID[impacts[index].ArtifactID]; duplicate {
			return nil, fmt.Errorf("duplicate structure impact for artifact %q", impacts[index].ArtifactID)
		}
		byID[impacts[index].ArtifactID] = index
	}
	currentLocations := structureNodeLocations(current)
	added := addedStructureNodes(currentLocations, locations)
	causalSourceIDs := append([]string(nil), request.TargetID)
	for _, kind := range []string{domain.StructureKindVolume, domain.StructureKindArc, domain.StructureKindChapter} {
		causalSourceIDs = append(causalSourceIDs, added[kind]...)
	}
	completed := make(map[string]struct{}, len(request.CompletedChapterIDs))
	for _, id := range request.CompletedChapterIDs {
		completed[strings.TrimSpace(id)] = struct{}{}
	}
	for index := range impacts {
		impact := impacts[index]
		if !impact.RequiresBodyRewrite {
			continue
		}
		if _, written := completed[impact.ArtifactID]; !written {
			return nil, fmt.Errorf("body rewrite impact %q is not a completed chapter", impact.ArtifactID)
		}
		if impact.ArtifactID == request.TargetID {
			continue
		}
		if impact.Level != domain.StructureImpactRequired || impact.Cause != domain.StructureImpactContentDependency || !structureSourceIDsReferenceAny(impact.DependencySourceIDs, causalSourceIDs) {
			return nil, fmt.Errorf("body rewrite for %q requires planner-supplied causal evidence naming a direct changed structure ID", impact.ArtifactID)
		}
	}
	for id, currentLocation := range currentLocations {
		if !structureNodeContentChanged(id, currentLocation.Kind, current, candidate) {
			continue
		}
		impactIndex, supplied := byID[id]
		if id != request.TargetID {
			if !supplied || impacts[impactIndex].Level != domain.StructureImpactRequired || impacts[impactIndex].Cause != domain.StructureImpactContentDependency ||
				!structureSourceIDsReferenceAny(impacts[impactIndex].DependencySourceIDs, causalSourceIDs) {
				return nil, fmt.Errorf("changed %s %q requires planner-supplied causal evidence naming a direct changed structure ID", currentLocation.Kind, id)
			}
		} else if !supplied {
			impactIndex = upsertStructureImpact(&impacts, byID, domain.StructureImpactItem{
				ArtifactID: id, ArtifactKind: currentLocation.Kind, Change: "directly revise targeted structure content",
				Level: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency,
				DependencyEvidence:  []string{fmt.Sprintf("operation %s directly targets stable ID %s", request.Operation, id)},
				DependencySourceIDs: []string{id},
			})
		}
		impacts[impactIndex].Level = domain.StructureImpactRequired
		impacts[impactIndex].Cause = domain.StructureImpactContentDependency
		if currentLocation.Kind == domain.StructureKindChapter {
			if _, written := completed[id]; written {
				if id != request.TargetID && !impacts[impactIndex].RequiresBodyRewrite {
					return nil, fmt.Errorf("changed completed chapter %q must explicitly require body rewrite", id)
				}
				impacts[impactIndex].RequiresBodyRewrite = true
			}
		}
	}

	currentNumbers := chapterNumbersByID(current)
	candidateNumbers := chapterNumbersByID(candidate)
	for _, orderedEntry := range domain.FlattenOutline(domain.ProjectLayeredOutlineOrder(current)) {
		id := orderedEntry.ID
		if currentNumbers[id] == candidateNumbers[id] {
			continue
		}
		evidence := fmt.Sprintf("display chapter changed from %d to %d while stable ID stayed %s", currentNumbers[id], candidateNumbers[id], id)
		if impactIndex, exists := byID[id]; exists {
			impacts[impactIndex].DependencyEvidence = appendUniqueStructureEvidence(impacts[impactIndex].DependencyEvidence, evidence)
			continue
		}
		upsertStructureImpact(&impacts, byID, domain.StructureImpactItem{
			ArtifactID: id, ArtifactKind: domain.StructureKindChapter,
			Change: "display chapter number changed", Level: domain.StructureImpactRecommended,
			Cause: domain.StructureImpactDisplayRenumber, DependencyEvidence: []string{evidence},
		})
	}
	addNewStructureImpacts(current, candidate, &impacts, byID)
	addParentStructureImpacts(current, candidate, &impacts, byID)
	for id, currentLocation := range currentLocations {
		candidateLocation := locations[id]
		if currentLocation.ParentID == candidateLocation.ParentID {
			continue
		}
		upsertStructureImpact(&impacts, byID, domain.StructureImpactItem{
			ArtifactID: id, ArtifactKind: currentLocation.Kind, Change: "move structure node to a different parent",
			Level: domain.StructureImpactRequired, Cause: domain.StructureImpactStructureChange,
			DependencyEvidence: []string{fmt.Sprintf("operation %s moves %s from %s to %s", request.Operation, id, currentLocation.ParentID, candidateLocation.ParentID)},
		})
	}
	slices.SortFunc(impacts, func(left, right domain.StructureImpactItem) int {
		return strings.Compare(left.ArtifactID, right.ArtifactID)
	})
	return impacts, nil
}

func addNewStructureImpacts(
	current, candidate []domain.VolumeOutline,
	impacts *[]domain.StructureImpactItem,
	byID map[string]int,
) {
	currentIDs := structureKindsByID(current)
	add := func(id, kind string) {
		if _, existed := currentIDs[id]; existed {
			return
		}
		if _, alreadyPresent := byID[id]; alreadyPresent {
			return
		}
		byID[id] = len(*impacts)
		*impacts = append(*impacts, domain.StructureImpactItem{
			ArtifactID: id, ArtifactKind: kind, Change: "add candidate structure node",
			Level: domain.StructureImpactRequired, Cause: domain.StructureImpactStructureChange,
			DependencyEvidence: []string{"candidate introduces this stable structure ID; formal structure remains unchanged until confirmation"},
		})
	}
	for _, volume := range candidate {
		add(volume.ID, domain.StructureKindVolume)
		for _, arc := range volume.Arcs {
			add(arc.ID, domain.StructureKindArc)
			for _, chapter := range arc.Chapters {
				add(chapter.ID, domain.StructureKindChapter)
			}
		}
	}
}

func addParentStructureImpacts(current, candidate []domain.VolumeOutline, impacts *[]domain.StructureImpactItem, byID map[string]int) {
	currentChildren := directStructureChildren(current)
	candidateChildren := directStructureChildren(candidate)
	locations := structureNodeLocations(candidate)
	impactedVolumes := make(map[string]struct{})
	parents := make(map[string]struct{}, len(currentChildren)+len(candidateChildren))
	for parentID := range currentChildren {
		parents[parentID] = struct{}{}
	}
	for parentID := range candidateChildren {
		parents[parentID] = struct{}{}
	}
	for parentID := range parents {
		currentIDs := currentChildren[parentID]
		candidateIDs := candidateChildren[parentID]
		if reflect.DeepEqual(currentIDs, candidateIDs) || parentID == "" {
			continue
		}
		location := locations[parentID]
		upsertStructureImpact(impacts, byID, domain.StructureImpactItem{
			ArtifactID: parentID, ArtifactKind: location.Kind, Change: "revise child membership or order",
			Level: domain.StructureImpactRequired, Cause: domain.StructureImpactStructureChange,
			DependencyEvidence: []string{fmt.Sprintf("direct children changed from %v to %v", currentIDs, candidateIDs)},
		})
		if location.Kind == domain.StructureKindArc {
			impactedVolumes[location.VolumeID] = struct{}{}
		}
	}
	for volumeID := range impactedVolumes {
		upsertStructureImpact(impacts, byID, domain.StructureImpactItem{
			ArtifactID: volumeID, ArtifactKind: domain.StructureKindVolume, Change: "review descendant structural change",
			Level: domain.StructureImpactRequired, Cause: domain.StructureImpactStructureChange,
			DependencyEvidence: []string{"a child arc changed chapter membership or order"},
		})
	}
}

func directStructureChildren(volumes []domain.VolumeOutline) map[string][]string {
	children := map[string][]string{"": {}}
	for _, volume := range volumes {
		children[""] = append(children[""], volume.ID)
		for _, arc := range volume.Arcs {
			children[volume.ID] = append(children[volume.ID], arc.ID)
			for _, chapter := range arc.Chapters {
				children[arc.ID] = append(children[arc.ID], chapter.ID)
			}
		}
	}
	return children
}

func upsertStructureImpact(impacts *[]domain.StructureImpactItem, byID map[string]int, incoming domain.StructureImpactItem) int {
	if index, exists := byID[incoming.ArtifactID]; exists {
		current := &(*impacts)[index]
		current.DependencyEvidence = appendUniqueStructureEvidence(current.DependencyEvidence, incoming.DependencyEvidence...)
		current.DependencySourceIDs = appendUniqueStructureEvidence(current.DependencySourceIDs, incoming.DependencySourceIDs...)
		if incoming.Level == domain.StructureImpactRequired {
			current.Level = incoming.Level
		}
		if current.Cause != domain.StructureImpactContentDependency || incoming.Cause == domain.StructureImpactContentDependency {
			current.Cause = incoming.Cause
		}
		current.RequiresBodyRewrite = current.RequiresBodyRewrite || incoming.RequiresBodyRewrite
		return index
	}
	byID[incoming.ArtifactID] = len(*impacts)
	*impacts = append(*impacts, incoming)
	return len(*impacts) - 1
}

func structureNodeContentChanged(id, kind string, current, candidate []domain.VolumeOutline) bool {
	switch kind {
	case domain.StructureKindVolume:
		left, right := volumeByID(current, id), volumeByID(candidate, id)
		left.ID, left.Index, left.Arcs = "", 0, nil
		right.ID, right.Index, right.Arcs = "", 0, nil
		return !reflect.DeepEqual(left, right)
	case domain.StructureKindArc:
		left, right := arcByID(current, id), arcByID(candidate, id)
		left.ID, left.Index, left.Chapters = "", 0, nil
		right.ID, right.Index, right.Chapters = "", 0, nil
		return !reflect.DeepEqual(left, right)
	default:
		return !outlineContentEqual(chapterEntriesByID(current)[id], chapterEntriesByID(candidate)[id])
	}
}

func volumeByID(volumes []domain.VolumeOutline, id string) domain.VolumeOutline {
	for _, volume := range volumes {
		if volume.ID == id {
			return volume
		}
	}
	return domain.VolumeOutline{}
}

func arcByID(volumes []domain.VolumeOutline, id string) domain.ArcOutline {
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			if arc.ID == id {
				return arc
			}
		}
	}
	return domain.ArcOutline{}
}

func structureSourceIDsReferenceAny(references, sourceIDs []string) bool {
	sources := make(map[string]struct{}, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		if sourceID != "" {
			sources[sourceID] = struct{}{}
		}
	}
	for _, reference := range references {
		if _, exists := sources[reference]; exists {
			return true
		}
	}
	return false
}

func structureKindsByID(volumes []domain.VolumeOutline) map[string]string {
	kinds := make(map[string]string)
	for _, volume := range volumes {
		kinds[volume.ID] = domain.StructureKindVolume
		for _, arc := range volume.Arcs {
			kinds[arc.ID] = domain.StructureKindArc
			for _, chapter := range arc.Chapters {
				kinds[chapter.ID] = domain.StructureKindChapter
			}
		}
	}
	return kinds
}

func structureArcCount(volumes []domain.VolumeOutline) int {
	total := 0
	for _, volume := range volumes {
		total += len(volume.Arcs)
	}
	return total
}

func chapterEntriesByID(volumes []domain.VolumeOutline) map[string]domain.OutlineEntry {
	entries := make(map[string]domain.OutlineEntry)
	for _, entry := range domain.FlattenOutline(domain.ProjectLayeredOutlineOrder(volumes)) {
		entries[entry.ID] = entry
	}
	return entries
}

func chapterNumbersByID(volumes []domain.VolumeOutline) map[string]int {
	numbers := make(map[string]int)
	for _, entry := range domain.FlattenOutline(domain.ProjectLayeredOutlineOrder(volumes)) {
		numbers[entry.ID] = entry.Chapter
	}
	return numbers
}

func outlineContentEqual(left, right domain.OutlineEntry) bool {
	left.ID, left.Chapter = "", 0
	right.ID, right.Chapter = "", 0
	return reflect.DeepEqual(left, right)
}

func difference(values, existing []string) []string {
	known := make(map[string]struct{}, len(existing))
	for _, value := range existing {
		known[value] = struct{}{}
	}
	result := make([]string, 0)
	for _, value := range values {
		if _, exists := known[value]; !exists {
			result = append(result, value)
		}
	}
	return result
}

func appendUniqueStructureEvidence(values []string, additions ...string) []string {
	for _, value := range additions {
		if slices.Contains(values, value) {
			continue
		}
		values = append(values, value)
	}
	return values
}

func cloneStructureRevisionProposal(proposal domain.StructureRevisionProposal) domain.StructureRevisionProposal {
	proposal.Candidate = domain.CloneStructureSnapshot(proposal.Candidate)
	proposal.Impacts = append([]domain.StructureImpactItem(nil), proposal.Impacts...)
	for index := range proposal.Impacts {
		proposal.Impacts[index].DependencyEvidence = append([]string(nil), proposal.Impacts[index].DependencyEvidence...)
		proposal.Impacts[index].DependencySourceIDs = append([]string(nil), proposal.Impacts[index].DependencySourceIDs...)
	}
	if proposal.Assessment.NewVolume != nil {
		evidence := *proposal.Assessment.NewVolume
		proposal.Assessment.NewVolume = &evidence
	}
	return proposal
}

type AdaptiveBatchConfig struct {
	MaxChaptersPerBatch int
	MaxOutputWords      int
	MaxContextUnits     int
}

type AdaptiveBatchPlanner struct {
	config AdaptiveBatchConfig
}

func NewAdaptiveBatchPlanner(config AdaptiveBatchConfig) AdaptiveBatchPlanner {
	if config.MaxChaptersPerBatch <= 0 {
		config.MaxChaptersPerBatch = 3
	}
	if config.MaxOutputWords <= 0 {
		config.MaxOutputWords = 12_000
	}
	if config.MaxContextUnits <= 0 {
		config.MaxContextUnits = 24_000
	}
	return AdaptiveBatchPlanner{config: config}
}

func (p AdaptiveBatchPlanner) Build(chapters []domain.BatchChapter) (*domain.BatchPlan, error) {
	if len(chapters) == 0 {
		return nil, fmt.Errorf("adaptive batch plan requires chapters")
	}
	seen := make(map[string]struct{}, len(chapters))
	arcVolumes := make(map[string]string)
	closedVolumes := make(map[string]struct{})
	closedArcs := make(map[string]struct{})
	lastVolumeID, lastArcID := "", ""
	plan := &domain.BatchPlan{}
	current := make([]domain.BatchChapter, 0, p.config.MaxChaptersPerBatch)
	constrained := false
	flush := func() error {
		if len(current) == 0 {
			return nil
		}
		batch, err := makeBatchWork(len(plan.Batches)+1, current, constrained)
		if err != nil {
			return err
		}
		plan.Batches = append(plan.Batches, batch)
		current = nil
		constrained = false
		return nil
	}
	for _, chapter := range chapters {
		if err := validateBatchChapter(chapter); err != nil {
			return nil, err
		}
		if _, exists := seen[chapter.ID]; exists {
			return nil, fmt.Errorf("duplicate batch chapter %q", chapter.ID)
		}
		seen[chapter.ID] = struct{}{}
		if volumeID, exists := arcVolumes[chapter.ArcID]; exists && volumeID != chapter.VolumeID {
			return nil, fmt.Errorf("batch arc %q cannot belong to both volumes %q and %q", chapter.ArcID, volumeID, chapter.VolumeID)
		}
		arcVolumes[chapter.ArcID] = chapter.VolumeID
		if lastVolumeID != "" && chapter.VolumeID != lastVolumeID {
			closedVolumes[lastVolumeID] = struct{}{}
			if _, closed := closedVolumes[chapter.VolumeID]; closed {
				return nil, fmt.Errorf("batch volume %q is not contiguous", chapter.VolumeID)
			}
		}
		if lastArcID != "" && chapter.ArcID != lastArcID {
			closedArcs[lastArcID] = struct{}{}
			if _, closed := closedArcs[chapter.ArcID]; closed {
				return nil, fmt.Errorf("batch arc %q is not contiguous", chapter.ArcID)
			}
		}
		lastVolumeID, lastArcID = chapter.VolumeID, chapter.ArcID
		if len(current) > 0 && (current[0].VolumeID != chapter.VolumeID || current[0].ArcID != chapter.ArcID) {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		candidate := append(append([]domain.BatchChapter(nil), current...), chapter)
		outputWords, contextUnits, err := batchSize(candidate)
		if err != nil {
			return nil, err
		}
		riskLimit := p.batchChapterLimit(candidate)
		if len(current) > 0 && (len(candidate) > riskLimit || outputWords > p.config.MaxOutputWords || contextUnits > p.config.MaxContextUnits) {
			if err := flush(); err != nil {
				return nil, err
			}
			candidate = []domain.BatchChapter{chapter}
			outputWords, contextUnits, err = batchSize(candidate)
			if err != nil {
				return nil, err
			}
			riskLimit = p.batchChapterLimit(candidate)
		}
		if outputWords > p.config.MaxOutputWords || contextUnits > p.config.MaxContextUnits {
			return nil, fmt.Errorf("chapter %q cannot fit a startable batch: output=%d/%d context=%d/%d", chapter.ID, outputWords, p.config.MaxOutputWords, contextUnits, p.config.MaxContextUnits)
		}
		current = append(current, chapter)
		if len(current) >= riskLimit {
			constrained = riskLimit < p.config.MaxChaptersPerBatch
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	seenVolumes := make(map[string]struct{})
	for _, batch := range plan.Batches {
		if _, exists := seenVolumes[batch.VolumeID]; exists {
			continue
		}
		seenVolumes[batch.VolumeID] = struct{}{}
		plan.VolumeReviews = append(plan.VolumeReviews, domain.BatchAggregateReview{ScopeID: batch.VolumeID, Status: domain.BatchReviewPending})
	}
	plan.WholeBookReview = domain.BatchAggregateReview{ScopeID: "whole-book", Status: domain.BatchReviewPending}
	return plan, nil
}

func (p AdaptiveBatchPlanner) chapterLimit(chapter domain.BatchChapter) int {
	limit := p.config.MaxChaptersPerBatch
	risk, ok := addNonNegativeInt(chapter.Complexity, chapter.CharacterCount/4)
	if !ok {
		return 1
	}
	risk, ok = addNonNegativeInt(risk, chapter.SourceAnchorCount/3)
	if !ok {
		return 1
	}
	if risk >= 8 {
		return 1
	}
	if risk >= 5 && limit > 2 {
		return 2
	}
	return limit
}

func (p AdaptiveBatchPlanner) batchChapterLimit(chapters []domain.BatchChapter) int {
	limit := p.config.MaxChaptersPerBatch
	for _, chapter := range chapters {
		if chapterLimit := p.chapterLimit(chapter); chapterLimit < limit {
			limit = chapterLimit
		}
	}
	return limit
}

func validateBatchChapter(chapter domain.BatchChapter) error {
	if strings.TrimSpace(chapter.ID) == "" || strings.TrimSpace(chapter.VolumeID) == "" || strings.TrimSpace(chapter.ArcID) == "" {
		return fmt.Errorf("batch chapter requires stable chapter, arc, and volume IDs")
	}
	if chapter.EstimatedOutputWords <= 0 || chapter.Complexity < 0 || chapter.CharacterCount < 0 || chapter.SourceAnchorCount < 0 {
		return fmt.Errorf("batch chapter %q has invalid estimates", chapter.ID)
	}
	for _, item := range chapter.Context {
		if strings.TrimSpace(item.ID) == "" || item.Units < 0 {
			return fmt.Errorf("batch chapter %q has invalid context", chapter.ID)
		}
	}
	return nil
}

func makeBatchWork(index int, chapters []domain.BatchChapter, constrained bool) (domain.BatchWork, error) {
	outputWords, contextUnits, err := batchSize(chapters)
	if err != nil {
		return domain.BatchWork{}, err
	}
	contextItems := necessaryBatchContext(chapters)
	chapterIDs := make([]string, 0, len(chapters))
	for _, chapter := range chapters {
		chapterIDs = append(chapterIDs, chapter.ID)
	}
	return domain.BatchWork{
		ID: fmt.Sprintf("batch-%03d", index), Index: index, ChapterIDs: chapterIDs,
		VolumeID: chapters[0].VolumeID, ArcID: chapters[0].ArcID, EstimatedOutputWords: outputWords, ContextUnits: contextUnits,
		Context:     contextItems,
		Constrained: constrained,
		Status:      domain.BatchStatusPending,
	}, nil
}

func batchSize(chapters []domain.BatchChapter) (int, int, error) {
	outputWords := 0
	for _, chapter := range chapters {
		var ok bool
		outputWords, ok = addNonNegativeInt(outputWords, chapter.EstimatedOutputWords)
		if !ok {
			return 0, 0, fmt.Errorf("batch output estimate overflows int")
		}
	}
	contextUnits := 0
	for _, item := range necessaryBatchContext(chapters) {
		var ok bool
		contextUnits, ok = addNonNegativeInt(contextUnits, item.Units)
		if !ok {
			return 0, 0, fmt.Errorf("batch context estimate overflows int")
		}
	}
	return outputWords, contextUnits, nil
}

func necessaryBatchContext(chapters []domain.BatchChapter) []domain.BatchContextItem {
	items := make([]domain.BatchContextItem, 0)
	byIdentity := make(map[string]int)
	for _, chapter := range chapters {
		for _, item := range chapter.Context {
			if !item.Necessary {
				continue
			}
			key := string(item.Kind) + "\x00" + item.ID
			if index, exists := byIdentity[key]; exists {
				if item.Units > items[index].Units {
					items[index].Units = item.Units
				}
				continue
			}
			byIdentity[key] = len(items)
			items = append(items, item)
		}
	}
	return items
}

func addNonNegativeInt(left, right int) (int, bool) {
	if left < 0 || right < 0 || left > int(^uint(0)>>1)-right {
		return 0, false
	}
	return left + right, true
}
