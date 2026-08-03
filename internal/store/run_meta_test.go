package store

import (
	"fmt"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestSaveAndLoadRunMeta(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	meta := domain.RunMeta{
		StartedAt:  "2026-03-07T10:00:00+08:00",
		Provider:   "openrouter",
		Style:      "fantasy",
		Model:      "gpt-4o",
		WordBudget: domain.NewWordBudget(5000, "test"),
	}
	if err := store.RunMeta.Save(meta); err != nil {
		t.Fatalf("SaveRunMeta: %v", err)
	}

	loaded, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if loaded.Style != "fantasy" {
		t.Errorf("style mismatch: %s", loaded.Style)
	}
	if loaded.Provider != "openrouter" {
		t.Errorf("provider mismatch: %s", loaded.Provider)
	}
	if loaded.Model != "gpt-4o" {
		t.Errorf("model mismatch: %s", loaded.Model)
	}
	if loaded.WordBudget == nil || loaded.WordBudget.TargetTotalWords != 5000 {
		t.Errorf("word budget mismatch: %+v", loaded.WordBudget)
	}
}

func TestLoadRunMeta_Empty(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	meta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("LoadRunMeta on empty: %v", err)
	}
	if meta != nil {
		t.Fatalf("expected nil, got %+v", meta)
	}
}

func TestAppendSteerEntry(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// 首次追加（meta/run.json 不存在）
	e1 := domain.SteerEntry{Input: "主角改成女性", Timestamp: "2026-03-07T10:01:00+08:00"}
	if err := store.RunMeta.AppendSteerEntry(e1); err != nil {
		t.Fatalf("AppendSteerEntry 1: %v", err)
	}

	meta, _ := store.RunMeta.Load()
	if len(meta.SteerHistory) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(meta.SteerHistory))
	}
	if meta.SteerHistory[0].Input != "主角改成女性" {
		t.Errorf("input mismatch: %s", meta.SteerHistory[0].Input)
	}

	// 追加第二条
	e2 := domain.SteerEntry{Input: "加入反转", Timestamp: "2026-03-07T10:02:00+08:00"}
	_ = store.RunMeta.AppendSteerEntry(e2)

	meta, _ = store.RunMeta.Load()
	if len(meta.SteerHistory) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(meta.SteerHistory))
	}
}

func TestAppendSteerEntryConcurrent(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	const workers = 32
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			entry := domain.SteerEntry{
				Input:     fmt.Sprintf("steer-%02d", i),
				Timestamp: fmt.Sprintf("ts-%02d", i),
			}
			if err := store.RunMeta.AppendSteerEntry(entry); err != nil {
				t.Errorf("AppendSteerEntry(%d): %v", i, err)
			}
		}(i)
	}

	close(start)
	wg.Wait()

	meta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if meta == nil {
		t.Fatal("expected run meta to exist")
	}
	if len(meta.SteerHistory) != workers {
		t.Fatalf("expected %d steer entries, got %d", workers, len(meta.SteerHistory))
	}

	seen := make(map[string]struct{}, workers)
	for _, entry := range meta.SteerHistory {
		seen[entry.Input] = struct{}{}
	}
	if len(seen) != workers {
		t.Fatalf("expected %d unique steer entries, got %d", workers, len(seen))
	}
}

func TestAppendSteerEntry_PreservesExistingMeta(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// 先保存 RunMeta
	_ = store.RunMeta.Save(domain.RunMeta{
		StartedAt: "2026-03-07T10:00:00+08:00",
		Provider:  "openrouter",
		Style:     "suspense",
		Model:     "gpt-4o",
	})

	// 追加 Steer 不应覆盖其他字段
	_ = store.RunMeta.AppendSteerEntry(domain.SteerEntry{Input: "test", Timestamp: "now"})

	meta, _ := store.RunMeta.Load()
	if meta.Style != "suspense" {
		t.Errorf("style should be preserved, got %s", meta.Style)
	}
	if meta.Provider != "openrouter" {
		t.Errorf("provider should be preserved, got %s", meta.Provider)
	}
	if meta.Model != "gpt-4o" {
		t.Errorf("model should be preserved, got %s", meta.Model)
	}
	if len(meta.SteerHistory) != 1 {
		t.Errorf("expected 1 steer entry, got %d", len(meta.SteerHistory))
	}
}

func TestInitRunMeta_PreservesHistory(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// 先建立带历史的 RunMeta
	_ = store.RunMeta.Save(domain.RunMeta{
		StartedAt:    "old",
		Provider:     "openai",
		Style:        "fantasy",
		Model:        "old-model",
		WordBudget:   domain.NewWordBudget(5000, "test"),
		SteerHistory: []domain.SteerEntry{{Input: "历史干预", Timestamp: "ts"}},
		PendingSteer: "待处理",
	})

	// InitRunMeta 应保留 SteerHistory 和 PendingSteer
	_ = store.RunMeta.Init("suspense", "openrouter", "new-model")

	meta, _ := store.RunMeta.Load()
	if meta.Style != "suspense" {
		t.Errorf("style should be updated, got %s", meta.Style)
	}
	if meta.Provider != "openrouter" {
		t.Errorf("provider should be updated, got %s", meta.Provider)
	}
	if meta.Model != "new-model" {
		t.Errorf("model should be updated, got %s", meta.Model)
	}
	if len(meta.SteerHistory) != 1 || meta.SteerHistory[0].Input != "历史干预" {
		t.Errorf("steer history should be preserved, got %v", meta.SteerHistory)
	}
	if meta.PendingSteer != "待处理" {
		t.Errorf("pending steer should be preserved, got %s", meta.PendingSteer)
	}
}

func TestInitRunMeta_PreservesWordBudget(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	_ = store.RunMeta.Save(domain.RunMeta{
		StartedAt:  "old",
		Provider:   "openai",
		Style:      "fantasy",
		Model:      "old-model",
		WordBudget: domain.NewWordBudget(5000, "test"),
	})
	if err := store.RunMeta.Init("suspense", "openrouter", "new-model"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	meta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if meta == nil || meta.WordBudget == nil || meta.WordBudget.TargetTotalWords != 5000 {
		t.Fatalf("word budget should be preserved, got %+v", meta)
	}
}

func TestSetWordBudget(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	budget := domain.NewWordBudget(5000, "quick_start").WithPlannedChapters(5)
	if err := store.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	meta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if meta == nil || meta.WordBudget == nil {
		t.Fatal("expected word budget")
	}
	if meta.WordBudget.TargetTotalWords != 5000 ||
		meta.WordBudget.TotalMinWords != 4500 ||
		meta.WordBudget.TotalMaxWords != 5500 ||
		meta.WordBudget.PlannedChapters != 5 {
		t.Fatalf("unexpected word budget: %+v", meta.WordBudget)
	}
	if meta.WordBudget.ChapterMinWords <= 0 || meta.WordBudget.ChapterMaxWords <= meta.WordBudget.ChapterMinWords {
		t.Fatalf("unexpected chapter range: %+v", meta.WordBudget)
	}
}

func TestSetAndClearPendingSteer(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// 设置 PendingSteer
	if err := store.RunMeta.SetPendingSteer("主角改成女性"); err != nil {
		t.Fatalf("SetPendingSteer: %v", err)
	}
	meta, _ := store.RunMeta.Load()
	if meta.PendingSteer != "主角改成女性" {
		t.Errorf("expected pending steer, got %s", meta.PendingSteer)
	}

	// 清除
	if err := store.RunMeta.ClearPendingSteer(); err != nil {
		t.Fatalf("ClearPendingSteer: %v", err)
	}
	meta, _ = store.RunMeta.Load()
	if meta.PendingSteer != "" {
		t.Errorf("expected empty pending steer, got %s", meta.PendingSteer)
	}
}

func TestClearHandledSteerIfPreservesNewerPendingSteer(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := store.RunMeta.SetPendingSteer("旧干预"); err != nil {
		t.Fatalf("SetPendingSteer old: %v", err)
	}
	if err := store.RunMeta.SetPendingSteer("新干预"); err != nil {
		t.Fatalf("SetPendingSteer new: %v", err)
	}
	if err := store.ClearHandledSteerIf("旧干预"); err != nil {
		t.Fatalf("ClearHandledSteerIf stale: %v", err)
	}
	meta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("Load after stale clear: %v", err)
	}
	if meta == nil || meta.PendingSteer != "新干预" {
		t.Fatalf("newer pending steer was cleared: %+v", meta)
	}
	if err := store.ClearHandledSteerIf("新干预"); err != nil {
		t.Fatalf("ClearHandledSteerIf current: %v", err)
	}
	meta, err = store.RunMeta.Load()
	if err != nil {
		t.Fatalf("Load after current clear: %v", err)
	}
	if meta == nil || meta.PendingSteer != "" {
		t.Fatalf("current pending steer was not cleared: %+v", meta)
	}
}

func TestSetPlanningTier(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.RunMeta.SetPlanningTier(domain.PlanningTierLong); err != nil {
		t.Fatalf("SetPlanningTier: %v", err)
	}

	meta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if meta == nil {
		t.Fatal("expected run meta to exist")
	}
	if meta.PlanningTier != domain.PlanningTierLong {
		t.Fatalf("expected planning tier %q, got %q", domain.PlanningTierLong, meta.PlanningTier)
	}
}

func TestClearPendingSteer_Noop(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// 空 meta 上调用不报错
	if err := store.RunMeta.ClearPendingSteer(); err != nil {
		t.Fatalf("ClearPendingSteer on empty: %v", err)
	}
}
