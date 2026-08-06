package tools

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

const (
	planningReviewPageMaxBytes = 14 * 1024
	planningReviewItemMaxBytes = 6 * 1024
	planningReviewRegistryMax  = 64
)

// PlanningReviewSelector identifies the exact skeleton review assigned by Host.
type PlanningReviewSelector struct {
	Volume     int `json:"volume,omitempty"`
	FromVolume int `json:"from_volume,omitempty"`
	ToVolume   int `json:"to_volume,omitempty"`
}

func (s PlanningReviewSelector) validate() error {
	if s.Volume > 0 {
		if s.FromVolume != 0 || s.ToVolume != 0 {
			return fmt.Errorf("planning review selector accepts volume or from/to, not both")
		}
		return nil
	}
	if (s.FromVolume == 0) != (s.ToVolume == 0) || s.FromVolume < 0 || s.ToVolume < s.FromVolume {
		return fmt.Errorf("planning review selector requires a valid inclusive volume range")
	}
	return nil
}

func (s PlanningReviewSelector) key() string {
	return fmt.Sprintf("v=%d;from=%d;to=%d", s.Volume, s.FromVolume, s.ToVolume)
}

type planningReviewBinding struct {
	Selector            PlanningReviewSelector `json:"selector"`
	FoundationRevision  int64                  `json:"foundation_revision"`
	FoundationSignature string                 `json:"foundation_signature"`
	OutlineDigest       string                 `json:"outline_digest"`
}

func (b planningReviewBinding) key() string {
	payload, _ := json.Marshal(b)
	return string(payload)
}

type sequentialPageState struct {
	SelectorKey    string
	BindingKey     string
	EvidenceDigest string
	TotalPages     int
	NextPage       int
	Complete       bool
	Sequence       uint64
}

// PlanningReviewRunRegistry authorizes Host-created review IDs and enforces
// ordered, lossless page consumption before an audit can be saved.
type PlanningReviewRunRegistry struct {
	mu       sync.Mutex
	runs     map[string]sequentialPageState
	sequence uint64
}

func NewPlanningReviewRunRegistry() *PlanningReviewRunRegistry {
	return &PlanningReviewRunRegistry{runs: make(map[string]sequentialPageState)}
}

func (r *PlanningReviewRunRegistry) Authorize(reviewID string, selector PlanningReviewSelector) error {
	if r == nil {
		return fmt.Errorf("planning review registry is required: %w", errs.ErrToolPrecondition)
	}
	reviewID = strings.TrimSpace(reviewID)
	if reviewID == "" {
		return fmt.Errorf("planning review_id is required: %w", errs.ErrToolArgs)
	}
	if err := selector.validate(); err != nil {
		return fmt.Errorf("invalid planning review selector: %w: %w", errs.ErrToolArgs, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	selectorKey := selector.key()
	for id, state := range r.runs {
		if state.SelectorKey == selectorKey {
			delete(r.runs, id)
		}
	}
	r.sequence++
	r.runs[reviewID] = sequentialPageState{SelectorKey: selectorKey, Sequence: r.sequence}
	r.pruneLocked()
	return nil
}

// ResolveActive returns the canonical ID for the only active Host authorization
// matching selector. Tool calls may omit the opaque ID, but they never create
// authorization themselves.
func (r *PlanningReviewRunRegistry) ResolveActive(selector PlanningReviewSelector) (string, error) {
	if r == nil {
		return "", fmt.Errorf("planning review registry is required: %w", errs.ErrToolPrecondition)
	}
	if err := selector.validate(); err != nil {
		return "", fmt.Errorf("invalid planning review selector: %w: %w", errs.ErrToolArgs, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	selectorKey := selector.key()
	matchedID := ""
	for id, state := range r.runs {
		if state.SelectorKey != selectorKey {
			continue
		}
		if matchedID != "" {
			return "", fmt.Errorf("multiple active Host planning reviews match selector: %w", errs.ErrToolConflict)
		}
		matchedID = id
	}
	if matchedID == "" {
		return "", fmt.Errorf("no active Host planning review matches selector: %w", errs.ErrToolPrecondition)
	}
	return matchedID, nil
}

// ResolveAuthorizedSelector verifies that a signed cursor still belongs to an
// active Host run before its selector is used to rebuild the next evidence page.
func (r *PlanningReviewRunRegistry) ResolveAuthorizedSelector(
	reviewID string,
	cursorSelector PlanningReviewSelector,
) (PlanningReviewSelector, error) {
	if r == nil {
		return PlanningReviewSelector{}, fmt.Errorf("planning review registry is required: %w", errs.ErrToolPrecondition)
	}
	if err := cursorSelector.validate(); err != nil {
		return PlanningReviewSelector{}, fmt.Errorf("invalid planning review cursor selector: %w: %w", errs.ErrToolArgs, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.runs[strings.TrimSpace(reviewID)]
	if !ok {
		return PlanningReviewSelector{}, fmt.Errorf("planning review cursor is not authorized by the current Host dispatch: %w", errs.ErrToolPrecondition)
	}
	if state.SelectorKey != cursorSelector.key() {
		return PlanningReviewSelector{}, fmt.Errorf("planning review cursor selector differs from the Host dispatch: %w", errs.ErrToolConflict)
	}
	return cursorSelector, nil
}

func (r *PlanningReviewRunRegistry) consume(
	reviewID string,
	selector PlanningReviewSelector,
	binding planningReviewBinding,
	evidenceDigest string,
	page int,
	totalPages int,
) error {
	if r == nil {
		return fmt.Errorf("planning review registry is required: %w", errs.ErrToolPrecondition)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.runs[strings.TrimSpace(reviewID)]
	if !ok {
		return fmt.Errorf("planning review_id is not authorized by the current Host dispatch: %w", errs.ErrToolPrecondition)
	}
	if state.SelectorKey != selector.key() {
		return fmt.Errorf("planning review selector differs from the Host dispatch: %w", errs.ErrToolConflict)
	}
	if totalPages <= 0 || page < 0 || page >= totalPages {
		return fmt.Errorf("invalid planning review page %d/%d: %w", page, totalPages, errs.ErrToolArgs)
	}
	bindingKey := binding.key()
	if state.BindingKey != "" && (state.BindingKey != bindingKey || state.EvidenceDigest != evidenceDigest || state.TotalPages != totalPages) {
		return fmt.Errorf("planning review evidence snapshot changed while paging: %w", errs.ErrToolConflict)
	}
	if page != state.NextPage {
		return fmt.Errorf("planning review must read page %d next, got %d: %w", state.NextPage, page, errs.ErrToolConflict)
	}
	state.BindingKey = bindingKey
	state.EvidenceDigest = evidenceDigest
	state.TotalPages = totalPages
	state.NextPage = page + 1
	state.Complete = state.NextPage == totalPages
	r.runs[strings.TrimSpace(reviewID)] = state
	return nil
}

func (r *PlanningReviewRunRegistry) requireComplete(reviewID string, selector PlanningReviewSelector, binding planningReviewBinding) error {
	if r == nil {
		return fmt.Errorf("planning review registry is required: %w", errs.ErrToolPrecondition)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.runs[strings.TrimSpace(reviewID)]
	if !ok {
		return fmt.Errorf("planning review_id is not authorized by the current Host dispatch: %w", errs.ErrToolPrecondition)
	}
	if state.SelectorKey != selector.key() || state.BindingKey != binding.key() {
		return fmt.Errorf("planning review evidence belongs to a stale or different snapshot: %w", errs.ErrToolConflict)
	}
	if !state.Complete {
		return fmt.Errorf("planning review must read all %d pages before saving (next page %d): %w", state.TotalPages, state.NextPage, errs.ErrToolPrecondition)
	}
	return nil
}

func (r *PlanningReviewRunRegistry) Complete(reviewID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runs, strings.TrimSpace(reviewID))
}

func (r *PlanningReviewRunRegistry) Clear() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.runs)
}

func (r *PlanningReviewRunRegistry) ActiveRunCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs)
}

func (r *PlanningReviewRunRegistry) pruneLocked() {
	for len(r.runs) > planningReviewRegistryMax {
		var oldestID string
		oldestSequence := ^uint64(0)
		for id, state := range r.runs {
			if state.Sequence < oldestSequence {
				oldestID = id
				oldestSequence = state.Sequence
			}
		}
		delete(r.runs, oldestID)
	}
}

type planningReviewCursor struct {
	Version            int                    `json:"version"`
	ReviewID           string                 `json:"review_id"`
	Page               int                    `json:"page"`
	Selector           PlanningReviewSelector `json:"selector"`
	FoundationRevision int64                  `json:"foundation_revision"`
	FoundationSig      string                 `json:"foundation_signature"`
	OutlineDigest      string                 `json:"outline_digest"`
	EvidenceDigest     string                 `json:"evidence_digest"`
}

type planningReviewEvidenceItem struct {
	Path  string               `json:"path"`
	Value any                  `json:"value"`
	Chunk *planningReviewChunk `json:"chunk,omitempty"`
}

type planningReviewChunk struct {
	Index int `json:"index"`
	Total int `json:"total"`
}

func loadPlanningReviewBinding(st *store.Store, selector PlanningReviewSelector) (planningReviewBinding, error) {
	if st == nil {
		return planningReviewBinding{}, fmt.Errorf("store is required")
	}
	if err := selector.validate(); err != nil {
		return planningReviewBinding{}, err
	}
	foundation, err := st.Foundation.Load()
	if err != nil {
		return planningReviewBinding{}, fmt.Errorf("load Foundation for planning review: %w", err)
	}
	foundationSignature, err := domain.FoundationAuditSignature(foundation)
	if err != nil {
		return planningReviewBinding{}, fmt.Errorf("sign Foundation for planning review: %w", err)
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return planningReviewBinding{}, fmt.Errorf("load outline for planning review: %w", err)
	}
	outlinePayload, err := json.Marshal(domain.OriginalPlanningSkeletonProjection(volumes))
	if err != nil {
		return planningReviewBinding{}, fmt.Errorf("marshal outline for planning review: %w", err)
	}
	return planningReviewBinding{
		Selector: selector, FoundationRevision: foundation.Revision,
		FoundationSignature: foundationSignature,
		OutlineDigest:       domain.ContentSignature(outlinePayload),
	}, nil
}

func buildPlanningReviewPage(
	packet map[string]any,
	reviewID string,
	cursorValue string,
	selector PlanningReviewSelector,
	binding planningReviewBinding,
	registry *PlanningReviewRunRegistry,
) (json.RawMessage, error) {
	reviewID = strings.TrimSpace(reviewID)
	var cursor planningReviewCursor
	if strings.TrimSpace(cursorValue) != "" {
		var decodeErr error
		cursor, decodeErr = decodePlanningReviewCursor(cursorValue)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if reviewID == "" {
			reviewID = cursor.ReviewID
		}
	}
	if reviewID == "" {
		var resolveErr error
		reviewID, resolveErr = registry.ResolveActive(selector)
		if resolveErr != nil {
			return nil, resolveErr
		}
	}
	evidencePayload, err := json.Marshal(packet)
	if err != nil {
		return nil, err
	}
	evidenceDigest := domain.ContentSignature(evidencePayload)
	items, err := splitPlanningReviewEvidence(packet)
	if err != nil {
		return nil, err
	}
	pages, err := packPlanningReviewPages(items, reviewID, selector, binding, evidenceDigest)
	if err != nil {
		return nil, err
	}
	pageIndex := 0
	if strings.TrimSpace(cursorValue) != "" {
		if err := validatePlanningReviewCursor(cursor, reviewID, selector, binding, evidenceDigest); err != nil {
			return nil, err
		}
		pageIndex = cursor.Page
	}
	if pageIndex < 0 || pageIndex >= len(pages) {
		return nil, fmt.Errorf("planning review cursor page %d is outside page count %d: %w", pageIndex, len(pages), errs.ErrToolConflict)
	}
	if err := registry.consume(reviewID, selector, binding, evidenceDigest, pageIndex, len(pages)); err != nil {
		return nil, err
	}
	return json.Marshal(pages[pageIndex])
}

func splitPlanningReviewEvidence(packet map[string]any) ([]planningReviewEvidenceItem, error) {
	keys := make([]string, 0, len(packet))
	for key := range packet {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]planningReviewEvidenceItem, 0, len(keys))
	for _, key := range keys {
		var err error
		items, err = appendPlanningReviewEvidence(items, "/"+escapePlanningReviewPath(key), packet[key])
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

func appendPlanningReviewEvidence(items []planningReviewEvidenceItem, path string, value any) ([]planningReviewEvidenceItem, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal planning review evidence %s: %w", path, err)
	}
	var normalized any
	if err := json.Unmarshal(payload, &normalized); err != nil {
		return nil, fmt.Errorf("normalize planning review evidence %s: %w", path, err)
	}
	item := planningReviewEvidenceItem{Path: path, Value: normalized}
	if planningReviewEvidenceSize(item) <= planningReviewItemMaxBytes {
		return append(items, item), nil
	}
	switch typed := normalized.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			items, err = appendPlanningReviewEvidence(items, path+"/"+escapePlanningReviewPath(key), typed[key])
			if err != nil {
				return nil, err
			}
		}
		return items, nil
	case []any:
		for index, entry := range typed {
			items, err = appendPlanningReviewEvidence(items, path+"/"+strconv.Itoa(index), entry)
			if err != nil {
				return nil, err
			}
		}
		return items, nil
	case string:
		return append(items, splitPlanningReviewString(path, typed)...), nil
	default:
		return nil, fmt.Errorf("planning review evidence %s cannot fit one page: %w", path, errs.ErrToolPrecondition)
	}
}

func splitPlanningReviewString(path, value string) []planningReviewEvidenceItem {
	runes := []rune(value)
	if len(runes) == 0 {
		return []planningReviewEvidenceItem{{Path: path, Value: ""}}
	}
	chunks := make([]string, 0, 2)
	for len(runes) > 0 {
		length := min(len(runes), 2048)
		for length > 1 && planningReviewEvidenceSize(planningReviewEvidenceItem{Path: path, Value: string(runes[:length])}) > planningReviewItemMaxBytes {
			length /= 2
		}
		chunks = append(chunks, string(runes[:length]))
		runes = runes[length:]
	}
	items := make([]planningReviewEvidenceItem, 0, len(chunks))
	for index, chunk := range chunks {
		items = append(items, planningReviewEvidenceItem{
			Path: path, Value: chunk, Chunk: &planningReviewChunk{Index: index, Total: len(chunks)},
		})
	}
	return items
}

func planningReviewEvidenceSize(item planningReviewEvidenceItem) int {
	payload, _ := json.Marshal(item)
	return len(payload)
}

func packPlanningReviewPages(
	items []planningReviewEvidenceItem,
	reviewID string,
	selector PlanningReviewSelector,
	binding planningReviewBinding,
	evidenceDigest string,
) ([]map[string]any, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("planning review has no evidence: %w", errs.ErrToolPrecondition)
	}
	groups := make([][]planningReviewEvidenceItem, 0, 2)
	current := make([]planningReviewEvidenceItem, 0)
	for _, item := range items {
		trial := append(append([]planningReviewEvidenceItem(nil), current...), item)
		page := newPlanningReviewPage(trial, len(groups), 9999, reviewID, selector, binding, evidenceDigest)
		if planningReviewPageSize(page) <= planningReviewPageMaxBytes {
			current = trial
			continue
		}
		if len(current) == 0 {
			return nil, fmt.Errorf("planning review evidence %s exceeds page budget: %w", item.Path, errs.ErrToolPrecondition)
		}
		groups = append(groups, current)
		current = []planningReviewEvidenceItem{item}
	}
	groups = append(groups, current)
	pages := make([]map[string]any, 0, len(groups))
	for index, group := range groups {
		page := newPlanningReviewPage(group, index, len(groups), reviewID, selector, binding, evidenceDigest)
		if size := planningReviewPageSize(page); size > planningReviewPageMaxBytes {
			return nil, fmt.Errorf("planning review page %d is %d bytes, exceeds %d: %w", index, size, planningReviewPageMaxBytes, errs.ErrToolPrecondition)
		}
		pages = append(pages, page)
	}
	return pages, nil
}

func newPlanningReviewPage(
	items []planningReviewEvidenceItem,
	index int,
	totalPages int,
	reviewID string,
	selector PlanningReviewSelector,
	binding planningReviewBinding,
	evidenceDigest string,
) map[string]any {
	page := map[string]any{
		"index": index, "total": totalPages, "complete": index == totalPages-1,
		"instruction": "Read every page in order. Reconstruct evidence by JSON Pointer path and concatenate string chunks by chunk.index. Use the exact next_cursor until complete=true; do not save before completion.",
	}
	if index < totalPages-1 {
		page["next_cursor"] = encodePlanningReviewCursor(planningReviewCursor{
			Version: 1, ReviewID: reviewID, Page: index + 1, Selector: selector,
			FoundationRevision: binding.FoundationRevision, FoundationSig: binding.FoundationSignature,
			OutlineDigest: binding.OutlineDigest, EvidenceDigest: evidenceDigest,
		})
	}
	return map[string]any{
		"review_id": reviewID,
		"context_manifest": map[string]any{
			"version": 1, "selector": selector, "foundation_revision": binding.FoundationRevision,
			"foundation_signature": binding.FoundationSignature, "outline_digest": binding.OutlineDigest,
			"evidence_digest": evidenceDigest, "total_pages": totalPages, "lossless": true,
			"path_format": "JSON Pointer", "page_max_bytes": planningReviewPageMaxBytes,
		},
		"context_page":   page,
		"evidence_items": items,
	}
}

func planningReviewPageSize(page map[string]any) int {
	payload, _ := json.Marshal(page)
	return len(payload)
}

func encodePlanningReviewCursor(cursor planningReviewCursor) string {
	payload, _ := json.Marshal(cursor)
	signature := planningReviewCursorSignature(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func decodePlanningReviewCursor(value string) (planningReviewCursor, error) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != 2 {
		return planningReviewCursor{}, fmt.Errorf("planning review cursor is malformed: %w", errs.ErrToolArgs)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return planningReviewCursor{}, fmt.Errorf("decode planning review cursor: %w: %w", errs.ErrToolArgs, err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return planningReviewCursor{}, fmt.Errorf("decode planning review cursor signature: %w: %w", errs.ErrToolArgs, err)
	}
	expected := planningReviewCursorSignature(payload)
	if len(signature) != len(expected) || subtle.ConstantTimeCompare(signature, expected) != 1 {
		return planningReviewCursor{}, fmt.Errorf("planning review cursor signature is invalid: %w", errs.ErrToolArgs)
	}
	var cursor planningReviewCursor
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return planningReviewCursor{}, fmt.Errorf("decode planning review cursor payload: %w: %w", errs.ErrToolArgs, err)
	}
	return cursor, nil
}

func planningReviewCursorSignature(payload []byte) []byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte("planning-review-cursor-v1\x00"))
	_, _ = digest.Write(payload)
	return digest.Sum(nil)
}

func validatePlanningReviewCursor(
	cursor planningReviewCursor,
	reviewID string,
	selector PlanningReviewSelector,
	binding planningReviewBinding,
	evidenceDigest string,
) error {
	if cursor.Version != 1 || cursor.ReviewID != strings.TrimSpace(reviewID) || cursor.Selector != selector ||
		cursor.FoundationRevision != binding.FoundationRevision || cursor.FoundationSig != binding.FoundationSignature ||
		cursor.OutlineDigest != binding.OutlineDigest || cursor.EvidenceDigest != evidenceDigest {
		return fmt.Errorf("planning review cursor belongs to a stale or different evidence snapshot: %w", errs.ErrToolConflict)
	}
	return nil
}

func escapePlanningReviewPath(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}
