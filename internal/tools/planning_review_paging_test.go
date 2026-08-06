package tools

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func executePlanningReviewPacket(
	t *testing.T,
	st *store.Store,
	refs References,
	style string,
	opts ContextToolOptions,
	selector PlanningReviewSelector,
) map[string]any {
	t.Helper()
	registry := NewPlanningReviewRunRegistry()
	const reviewID = "test-planning-review"
	if err := registry.Authorize(reviewID, selector); err != nil {
		t.Fatal(err)
	}
	opts.PlanningReviews = registry
	tool := NewContextToolWithOptions(st, refs, style, opts)
	root := any(map[string]any{})
	cursor := ""
	for {
		request := map[string]any{"scope": "planning_review"}
		if cursor == "" {
			request["volume"] = selector.Volume
			request["from_volume"] = selector.FromVolume
			request["to_volume"] = selector.ToVolume
		} else {
			request["cursor"] = cursor
		}
		args, _ := json.Marshal(request)
		raw, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Page struct {
				Complete   bool   `json:"complete"`
				NextCursor string `json:"next_cursor"`
			} `json:"context_page"`
			Items []planningReviewEvidenceItem `json:"evidence_items"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		for _, item := range envelope.Items {
			segments := decodePlanningReviewPath(t, item.Path)
			root = setPlanningReviewPath(root, segments, item.Value, item.Chunk != nil && item.Chunk.Index > 0)
		}
		if envelope.Page.Complete {
			break
		}
		cursor = envelope.Page.NextCursor
	}
	packet, ok := root.(map[string]any)
	if !ok {
		t.Fatalf("reconstructed planning review root = %T", root)
	}
	return packet
}

func decodePlanningReviewPath(t *testing.T, path string) []string {
	t.Helper()
	if path == "" || path[0] != '/' {
		t.Fatalf("invalid planning review JSON Pointer %q", path)
	}
	parts := strings.Split(path[1:], "/")
	for index, part := range parts {
		part = strings.ReplaceAll(part, "~1", "/")
		parts[index] = strings.ReplaceAll(part, "~0", "~")
	}
	return parts
}

func setPlanningReviewPath(node any, segments []string, value any, appendString bool) any {
	if len(segments) == 0 {
		if appendString {
			if existing, ok := node.(string); ok {
				return existing + value.(string)
			}
		}
		return value
	}
	segment := segments[0]
	if index, err := strconv.Atoi(segment); err == nil {
		list, _ := node.([]any)
		for len(list) <= index {
			list = append(list, nil)
		}
		list[index] = setPlanningReviewPath(list[index], segments[1:], value, appendString)
		return list
	}
	object, _ := node.(map[string]any)
	if object == nil {
		object = make(map[string]any)
	}
	object[segment] = setPlanningReviewPath(object[segment], segments[1:], value, appendString)
	return object
}

func TestPlanningReviewPagesAreDeterministicUTF8AndStrictlyOrdered(t *testing.T) {
	selector := PlanningReviewSelector{Volume: 3}
	binding := planningReviewBinding{
		Selector: selector, FoundationRevision: 7,
		FoundationSignature: strings.Repeat("f", 64), OutlineDigest: strings.Repeat("o", 64),
	}
	packet := map[string]any{
		"planning_memory": map[string]any{
			"layered_outline": []any{
				map[string]any{"index": 3, "theme": strings.Repeat("樱牢终卷主题与不可逆承诺。", 1800)},
			},
		},
		"foundation_memory": map[string]any{
			"characters": []any{
				map[string]any{"id": "character-1", "contract": strings.Repeat("角色因果约束。", 1600)},
				map[string]any{"id": "character-2", "contract": strings.Repeat("关系与知识边界。", 1600)},
			},
		},
	}

	firstRegistry := NewPlanningReviewRunRegistry()
	secondRegistry := NewPlanningReviewRunRegistry()
	for _, registry := range []*PlanningReviewRunRegistry{firstRegistry, secondRegistry} {
		if err := registry.Authorize("review-run-1", selector); err != nil {
			t.Fatal(err)
		}
	}
	first, err := buildPlanningReviewPage(packet, "", "", selector, binding, firstRegistry)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildPlanningReviewPage(packet, "review-run-1", "", selector, binding, secondRegistry)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("same snapshot produced nondeterministic first page")
	}
	if !utf8.Valid(first) || len(first) > planningReviewPageMaxBytes {
		t.Fatalf("first page bytes=%d valid_utf8=%v", len(first), utf8.Valid(first))
	}

	page := decodePlanningReviewPage(t, first)
	if page.ReviewID != "review-run-1" {
		t.Fatalf("page zero review_id = %q", page.ReviewID)
	}
	totalPages := int(page.Manifest.TotalPages)
	if totalPages < 2 {
		t.Fatalf("total pages=%d, want multi-page evidence", totalPages)
	}
	cursor := page.Page.NextCursor
	for index := 1; index < totalPages; index++ {
		raw, pageErr := buildPlanningReviewPage(packet, "", cursor, selector, binding, firstRegistry)
		if pageErr != nil {
			t.Fatalf("read page %d: %v", index, pageErr)
		}
		if !utf8.Valid(raw) || len(raw) > planningReviewPageMaxBytes {
			t.Fatalf("page %d bytes=%d valid_utf8=%v", index, len(raw), utf8.Valid(raw))
		}
		page = decodePlanningReviewPage(t, raw)
		cursor = page.Page.NextCursor
	}
	if !page.Page.Complete || cursor != "" {
		t.Fatalf("last page = %+v", page.Page)
	}
	if err := firstRegistry.requireComplete("review-run-1", selector, binding); err != nil {
		t.Fatalf("completed review rejected: %v", err)
	}
}

func TestPlanningReviewPageZeroRequiresUniqueHostAuthorization(t *testing.T) {
	selector := PlanningReviewSelector{Volume: 3}
	binding := planningReviewBinding{Selector: selector}
	packet := map[string]any{"target": "evidence"}
	registry := NewPlanningReviewRunRegistry()
	if _, err := buildPlanningReviewPage(packet, "", "", selector, binding, registry); err == nil {
		t.Fatal("page zero without Host authorization was accepted")
	}

	registry.runs["first"] = sequentialPageState{SelectorKey: selector.key()}
	registry.runs["second"] = sequentialPageState{SelectorKey: selector.key()}
	if _, err := buildPlanningReviewPage(packet, "", "", selector, binding, registry); err == nil {
		t.Fatal("ambiguous Host authorizations were accepted")
	}
}

func TestPlanningReviewRegistryRejectsDuplicateSkipCrossRunAndStaleSnapshot(t *testing.T) {
	selector := PlanningReviewSelector{Volume: 3}
	binding := planningReviewBinding{
		Selector: selector, FoundationRevision: 4,
		FoundationSignature: strings.Repeat("a", 64), OutlineDigest: strings.Repeat("b", 64),
	}
	packet := map[string]any{"target": strings.Repeat("完整目标卷证据。", 5000)}
	registry := NewPlanningReviewRunRegistry()
	if err := registry.Authorize("review-a", selector); err != nil {
		t.Fatal(err)
	}
	first, err := buildPlanningReviewPage(packet, "review-a", "", selector, binding, registry)
	if err != nil {
		t.Fatal(err)
	}
	page := decodePlanningReviewPage(t, first)
	if _, err := buildPlanningReviewPage(packet, "review-a", "", selector, binding, registry); err == nil {
		t.Fatal("duplicate page zero was accepted")
	}

	evidencePayload, _ := json.Marshal(packet)
	evidenceDigest := domain.ContentSignature(evidencePayload)
	skipped := encodePlanningReviewCursor(planningReviewCursor{
		Version: 1, ReviewID: "review-a", Page: 2, Selector: selector,
		FoundationRevision: binding.FoundationRevision, FoundationSig: binding.FoundationSignature,
		OutlineDigest: binding.OutlineDigest, EvidenceDigest: evidenceDigest,
	})
	if _, err := buildPlanningReviewPage(packet, "review-a", skipped, selector, binding, registry); err == nil {
		t.Fatal("skipped page was accepted")
	}
	if err := registry.Authorize("review-b", selector); err != nil {
		t.Fatal(err)
	}
	if _, err := buildPlanningReviewPage(packet, "review-b", page.Page.NextCursor, selector, binding, registry); err == nil {
		t.Fatal("cross-run cursor was accepted")
	}
	if _, err := buildPlanningReviewPage(packet, "review-a", page.Page.NextCursor, selector, binding, registry); err == nil {
		t.Fatal("superseded review run remained authorized")
	}
	if err := registry.Authorize("review-c", selector); err != nil {
		t.Fatal(err)
	}
	first, err = buildPlanningReviewPage(packet, "review-c", "", selector, binding, registry)
	if err != nil {
		t.Fatal(err)
	}
	page = decodePlanningReviewPage(t, first)
	stale := binding
	stale.OutlineDigest = strings.Repeat("c", 64)
	if _, err := buildPlanningReviewPage(packet, "review-c", page.Page.NextCursor, selector, stale, registry); err == nil {
		t.Fatal("stale snapshot cursor was accepted")
	}
}

func TestSaveOriginalPlanningSkeletonAuditRequiresCompleteAuthorizedReview(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	approveFoundationToolFixture(t, st)
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		ID:    domain.LegacyStructureID("planning-review-save", domain.StructureKindVolume, "volume-1"),
		Index: 1, Title: "Opening", Theme: strings.Repeat("完整主题。", 1200),
		Arcs: []domain.ArcOutline{{
			ID:    domain.LegacyStructureID("planning-review-save", domain.StructureKindArc, "volume-1/arc-1"),
			Index: 1, Title: "Promise", Goal: strings.Repeat("完整弧目标。", 1200), EstimatedChapters: 4,
		}},
	}}); err != nil {
		t.Fatal(err)
	}

	registry := NewPlanningReviewRunRegistry()
	selector := PlanningReviewSelector{Volume: 1}
	const reviewID = "save-gated-review"
	if err := registry.Authorize(reviewID, selector); err != nil {
		t.Fatal(err)
	}
	auditArgs := planningSkeletonAuditArgs(t, reviewID)
	saveTool := NewSaveOriginalPlanningAuditTool(st, registry)
	if _, err := saveTool.Execute(context.Background(), auditArgs); err == nil {
		t.Fatal("audit save succeeded before reading planning review")
	}

	contextTool := NewContextToolWithOptions(st, References{}, "default", ContextToolOptions{PlanningReviews: registry})
	cursor := ""
	canonicalReviewID := ""
	pageCount := 0
	for {
		requestMap := map[string]any{"scope": "planning_review"}
		if cursor == "" {
			requestMap["volume"] = 1
		} else {
			requestMap["cursor"] = cursor
		}
		request, _ := json.Marshal(requestMap)
		raw, err := contextTool.Execute(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		page := decodePlanningReviewPage(t, raw)
		pageCount++
		if canonicalReviewID == "" {
			canonicalReviewID = page.ReviewID
		}
		if page.ReviewID != canonicalReviewID {
			t.Fatalf("review ID changed while paging: %q -> %q", canonicalReviewID, page.ReviewID)
		}
		if page.Page.Complete {
			break
		}
		cursor = page.Page.NextCursor
		if pageCount == 1 {
			wrongVolume, _ := json.Marshal(map[string]any{
				"scope": "planning_review", "cursor": cursor, "volume": 2,
			})
			if _, err := contextTool.Execute(context.Background(), wrongVolume); err == nil {
				t.Fatal("cursor accepted an explicit conflicting volume")
			}
			wrongID, _ := json.Marshal(map[string]any{
				"scope": "planning_review", "cursor": cursor, "review_id": "guessed-review-id",
			})
			if _, err := contextTool.Execute(context.Background(), wrongID); err == nil {
				t.Fatal("cursor accepted an explicit conflicting review_id")
			}
		}
	}
	if pageCount < 3 {
		t.Fatalf("planning review used %d pages, want at least three", pageCount)
	}
	if canonicalReviewID != reviewID {
		t.Fatalf("page zero returned review_id %q, want %q", canonicalReviewID, reviewID)
	}
	if _, err := saveTool.Execute(context.Background(), planningSkeletonAuditArgs(t, canonicalReviewID)); err != nil {
		t.Fatalf("audit save after complete review: %v", err)
	}
	if _, err := saveTool.Execute(context.Background(), auditArgs); err == nil {
		t.Fatal("submitted review_id was reusable")
	}
}

type decodedPlanningReviewPage struct {
	ReviewID string `json:"review_id"`
	Manifest struct {
		TotalPages float64 `json:"total_pages"`
	} `json:"context_manifest"`
	Page struct {
		Complete   bool   `json:"complete"`
		NextCursor string `json:"next_cursor"`
	} `json:"context_page"`
}

func decodePlanningReviewPage(t *testing.T, raw json.RawMessage) decodedPlanningReviewPage {
	t.Helper()
	var page decodedPlanningReviewPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode planning review page: %v", err)
	}
	return page
}

func planningSkeletonAuditArgs(t *testing.T, reviewID string) json.RawMessage {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"scope": "skeleton_volume", "scope_id": "", "volume": 1, "arc": 0,
		"from_volume": 0, "to_volume": 0, "from_chapter": 0, "to_chapter": 0,
		"review_id": reviewID, "verdict": "pass", "summary": "volume contract is complete",
		"dimensions": originalAuditTestDimensions(
			"volume_function", "arc_causality", "character_progression",
			"conflict_escalation", "budget_capacity", "payoff_and_handoff",
		),
		"issues": []map[string]any{}, "observed_scene_counts": []map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return args
}
