package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestResumeSchedulePutNormalizesAndPersists(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(t.TempDir(), "config.json")
	server := NewServer(cfg, assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	req := httptest.NewRequest(http.MethodPut, "/api/resume-schedule", bytes.NewBufferString(`{"daily_times":["16:00","15:00","16:00"],"timezone":"Asia/Shanghai"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	saved, err := bootstrap.LoadConfigFile(cfg.PersistPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if len(saved.ResumeSchedule.DailyTimes) != 2 || saved.ResumeSchedule.DailyTimes[0] != "15:00" || saved.ResumeSchedule.DailyTimes[1] != "16:00" {
		t.Fatalf("saved schedule=%+v", saved.ResumeSchedule)
	}
}

func TestProjectResumeScheduleDefaultsEnabledAndPreservesOverlay(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Schedule Toggle")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	get := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/resume-schedule", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, get)
	var initial struct {
		Enabled bool `json:"enabled"`
	}
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&initial) != nil || !initial.Enabled {
		t.Fatalf("default response status=%d body=%s", rec.Code, rec.Body.String())
	}

	original := bootstrap.Config{Style: "romance"}
	if err := bootstrap.SaveConfig(ProjectConfigPath(manifest), original); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	put := httptest.NewRequest(http.MethodPut, "/api/projects/"+manifest.ID+"/resume-schedule", bytes.NewBufferString(`{"enabled":false}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, put)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	overlay, err := bootstrap.LoadConfigFile(ProjectConfigPath(manifest))
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if overlay.EffectiveScheduledResumeEnabled() || overlay.Style != original.Style {
		t.Fatalf("overlay=%+v", overlay)
	}
}

func TestScheduledProjectCheckDoesNotTouchLastAccessedAt(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Scheduled Read Only Open")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	before, err := readProjectManifest(filepath.Join(manifest.RootDir, "project.json"))
	if err != nil {
		t.Fatalf("read manifest before: %v", err)
	}
	result, err := server.resumeScheduledProject(context.Background(), manifest)
	if err != nil {
		t.Fatalf("resumeScheduledProject: %v", err)
	}
	if result.Outcome != ScheduledResumeSkipped {
		t.Fatalf("result=%+v, want skipped empty project", result)
	}
	after, err := readProjectManifest(filepath.Join(manifest.RootDir, "project.json"))
	if err != nil {
		t.Fatalf("read manifest after: %v", err)
	}
	if !after.LastAccessedAt.Equal(before.LastAccessedAt) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("scheduled check touched project timestamps: before=%+v after=%+v", before, after)
	}
}
