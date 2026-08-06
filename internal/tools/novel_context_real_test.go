package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

func TestRealProjectPlanningContextBreakdown(t *testing.T) {
	source := strings.TrimSpace(os.Getenv("AINOVEL_REAL_PLANNING_OUTPUT"))
	if source == "" {
		t.Skip("set AINOVEL_REAL_PLANNING_OUTPUT to a project output directory")
	}
	copyRoot := filepath.Join(t.TempDir(), "output")
	if err := copyPlanningFixture(source, copyRoot); err != nil {
		t.Fatalf("copy real planning fixture: %v", err)
	}

	st := store.NewStore(copyRoot)
	bundle := assets.Load("suspense")
	tool := tools.NewContextToolWithOptions(st, bundle.References, "suspense", tools.ContextToolOptions{
		Role: "architect",
	})
	for _, request := range []string{
		`{"scope":"planning"}`,
		`{"scope":"planning_volume","volume":1}`,
		`{"scope":"planning_review","volume":1}`,
		`{"scope":"planning_review","volume":3}`,
	} {
		var raw json.RawMessage
		var err error
		if strings.Contains(request, `"scope":"planning_review"`) {
			volume := 1
			if strings.Contains(request, `"volume":3`) {
				volume = 3
			}
			raw = executeRealPlanningReview(t, st, bundle, volume)
		} else {
			raw, err = tool.Execute(context.Background(), json.RawMessage(request))
		}
		if err != nil {
			t.Fatalf("execute real context %s: %v", request, err)
		}
		t.Logf("request=%s total=%d", request, len(raw))

		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("decode planning context: %v", err)
		}
		logJSONSectionSizes(t, "", payload)
		if request == `{"scope":"planning_review","volume":3}` {
			assertRealPlanningReviewTargetExact(t, st, raw, 3)
		}
	}
}

func executeRealPlanningReview(t *testing.T, st *store.Store, bundle assets.Bundle, volume int) json.RawMessage {
	t.Helper()
	selector := tools.PlanningReviewSelector{Volume: volume}
	registry := tools.NewPlanningReviewRunRegistry()
	reviewID := fmt.Sprintf("real-review-volume-%d", volume)
	if err := registry.Authorize(reviewID, selector); err != nil {
		t.Fatal(err)
	}
	tool := tools.NewContextToolWithOptions(st, bundle.References, "suspense", tools.ContextToolOptions{
		Role: "editor", PlanningReviews: registry,
	})
	root := any(map[string]any{})
	cursor := ""
	pageIndex := 0
	for {
		args, _ := json.Marshal(map[string]any{
			"scope": "planning_review", "volume": volume, "review_id": reviewID, "cursor": cursor,
		})
		pageRaw, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("execute real planning review page %d: %v", pageIndex, err)
		}
		t.Logf("planning_review(volume=%d) page=%d bytes=%d", volume, pageIndex, len(pageRaw))
		var envelope struct {
			Page struct {
				Complete   bool   `json:"complete"`
				NextCursor string `json:"next_cursor"`
			} `json:"context_page"`
			Items []struct {
				Path  string `json:"path"`
				Value any    `json:"value"`
				Chunk *struct {
					Index int `json:"index"`
				} `json:"chunk"`
			} `json:"evidence_items"`
		}
		if err := json.Unmarshal(pageRaw, &envelope); err != nil {
			t.Fatal(err)
		}
		for _, item := range envelope.Items {
			segments := decodeRealPlanningReviewPath(t, item.Path)
			root = setRealPlanningReviewPath(root, segments, item.Value, item.Chunk != nil && item.Chunk.Index > 0)
		}
		if envelope.Page.Complete {
			break
		}
		cursor = envelope.Page.NextCursor
		pageIndex++
	}
	packet, ok := root.(map[string]any)
	if !ok {
		t.Fatalf("real planning review root = %T", root)
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodeRealPlanningReviewPath(t *testing.T, path string) []string {
	t.Helper()
	if path == "" || path[0] != '/' {
		t.Fatalf("invalid planning review path %q", path)
	}
	parts := strings.Split(path[1:], "/")
	for index, part := range parts {
		part = strings.ReplaceAll(part, "~1", "/")
		parts[index] = strings.ReplaceAll(part, "~0", "~")
	}
	return parts
}

func setRealPlanningReviewPath(node any, segments []string, value any, appendString bool) any {
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
		list[index] = setRealPlanningReviewPath(list[index], segments[1:], value, appendString)
		return list
	}
	object, _ := node.(map[string]any)
	if object == nil {
		object = make(map[string]any)
	}
	object[segment] = setRealPlanningReviewPath(object[segment], segments[1:], value, appendString)
	return object
}

func assertRealPlanningReviewTargetExact(t *testing.T, st *store.Store, raw json.RawMessage, targetVolume int) {
	t.Helper()
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("load real layered outline: %v", err)
	}
	var canonical *domain.VolumeOutline
	for index := range volumes {
		if volumes[index].Index == targetVolume {
			canonical = &volumes[index]
			break
		}
	}
	if canonical == nil {
		t.Fatalf("real fixture has no volume %d", targetVolume)
	}
	var payload struct {
		Planning struct {
			Layered []struct {
				Index int    `json:"index"`
				Theme string `json:"theme"`
				Arcs  []struct {
					Index int    `json:"index"`
					Goal  string `json:"goal"`
				} `json:"arcs"`
			} `json:"layered_outline"`
		} `json:"planning_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode real planning review: %v", err)
	}
	for _, projected := range payload.Planning.Layered {
		if projected.Index != targetVolume {
			continue
		}
		if projected.Theme != canonical.Theme {
			t.Fatalf("real volume %d theme changed: got %q want %q", targetVolume, projected.Theme, canonical.Theme)
		}
		if len(projected.Arcs) != len(canonical.Arcs) {
			t.Fatalf("real volume %d arcs=%d, want %d", targetVolume, len(projected.Arcs), len(canonical.Arcs))
		}
		for index, arc := range projected.Arcs {
			if arc.Index != canonical.Arcs[index].Index || arc.Goal != canonical.Arcs[index].Goal {
				t.Fatalf("real volume %d arc %d changed", targetVolume, canonical.Arcs[index].Index)
			}
		}
		return
	}
	t.Fatalf("real planning review omitted target volume %d", targetVolume)
}

func logJSONSectionSizes(t *testing.T, prefix string, payload map[string]any) {
	t.Helper()
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		raw, err := json.Marshal(payload[key])
		if err != nil {
			t.Fatalf("marshal %s: %v", path, err)
		}
		t.Logf("section %s=%d", path, len(raw))
		if nested, ok := payload[key].(map[string]any); ok {
			logJSONSectionSizes(t, path, nested)
		}
	}
}

func copyPlanningFixture(source, target string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if info.IsDir() {
			return os.MkdirAll(destination, info.Mode())
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}
