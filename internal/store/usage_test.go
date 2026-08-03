package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// TestUsageStore_LoadMissing 验证文件不存在时返回 (nil, nil)，由调用方走 replay。
func TestUsageStore_LoadMissing(t *testing.T) {
	dir := t.TempDir()
	us := NewUsageStore(newIO(dir))

	state, err := us.Load()
	if err != nil {
		t.Fatalf("Load missing file should not error, got %v", err)
	}
	if state != nil {
		t.Fatalf("Load missing file should return nil state, got %+v", state)
	}
}

func TestUsageStoreMigratesSchemaTwoAsLegacyAggregate(t *testing.T) {
	dir := t.TempDir()
	us := NewUsageStore(newIO(dir))
	raw := []byte(`{"schema":2,"overall":{"input":123}}`)
	if err := os.MkdirAll(filepath.Join(dir, "meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "usage.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := us.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || !state.LegacyAggregate || !state.CoverageUnknown || state.Overall.Input != 123 {
		t.Fatalf("legacy migration = %+v", state)
	}
}

func TestUsageStoreCompactsOldCallsIntoDailyAggregates(t *testing.T) {
	dir := t.TempDir()
	us := NewUsageStore(newIO(dir))
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	old := domain.UsageCallEvent{ID: "old", Timestamp: now.Add(-91 * 24 * time.Hour), Role: "writer", Provider: "p", Model: "m", Input: 100, CacheRead: 60, CostUSD: 1, Status: "ok", UsagePresent: true, Attempt: 1}
	recent := domain.UsageCallEvent{ID: "recent", Timestamp: now.Add(-24 * time.Hour), Role: "writer", Provider: "p", Model: "m", Input: 50, Status: "ok", UsagePresent: true, Attempt: 1}
	if err := us.AppendCall(old); err != nil {
		t.Fatal(err)
	}
	if err := us.AppendCall(recent); err != nil {
		t.Fatal(err)
	}
	if err := us.CompactCalls(now, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	calls, err := us.LoadCalls()
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].ID != "recent" {
		t.Fatalf("calls = %+v", calls)
	}
	daily, err := us.LoadDailyAggregates()
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 1 || daily[0].Calls != 1 || daily[0].CacheRead != 60 {
		t.Fatalf("daily = %+v", daily)
	}
}

// TestUsageStore_RoundTrip 写入再读取，验证累计数据原样回来。
func TestUsageStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	us := NewUsageStore(newIO(dir))

	in := domain.UsageState{
		Overall: domain.AgentUsageTotals{
			Input: 12000, Output: 3400, CacheRead: 8000, CacheWrite: 1500,
			Cost: 1.234, Saved: 0.5, CacheCapable: true,
		},
		PerAgent: map[string]domain.AgentUsageTotals{
			"writer": {Input: 10000, Output: 3000, CacheRead: 7500, Cost: 1.0, CacheCapable: true},
			"editor": {Input: 2000, Output: 400, CacheRead: 500, Cost: 0.234},
		},
		MissingUsage: 3,
	}
	if err := us.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := us.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil {
		t.Fatalf("Load returned nil after Save")
	}
	if got.Schema != domain.UsageSchemaVersion {
		t.Errorf("schema = %d want %d", got.Schema, domain.UsageSchemaVersion)
	}
	if got.Overall != in.Overall {
		t.Errorf("overall mismatch:\n got  %+v\n want %+v", got.Overall, in.Overall)
	}
	if got.PerAgent["writer"] != in.PerAgent["writer"] {
		t.Errorf("writer totals mismatch:\n got  %+v\n want %+v", got.PerAgent["writer"], in.PerAgent["writer"])
	}
	if got.MissingUsage != in.MissingUsage {
		t.Errorf("missing_usage = %d want %d", got.MissingUsage, in.MissingUsage)
	}
}

// TestUsageStore_LoadSchemaMismatch 验证未来 schema 升级时旧文件被丢弃（让 host 走 replay 重建），
// 不会把不兼容的字段错误地塞回 tracker。
func TestUsageStore_LoadSchemaMismatch(t *testing.T) {
	dir := t.TempDir()
	us := NewUsageStore(newIO(dir))

	// 手写一份 schema=0 的旧数据
	raw, err := json.Marshal(map[string]any{
		"schema":  0,
		"overall": map[string]any{"input": 999},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "meta"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "usage.json"), raw, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := us.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != nil {
		t.Errorf("schema mismatch should return nil, got %+v", got)
	}
}
