package simeval

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOfflineEvaluationPassesOriginalFixture(t *testing.T) {
	report := Run(context.Background(), fixturePath(t))
	if report.Status != "pass" || report.Summary.Failed != 0 {
		t.Fatalf("offline report = %+v", report)
	}
	if report.Summary.Passed != 9 {
		t.Fatalf("passed invariants = %d, want 9", report.Summary.Passed)
	}
}

func TestBoundaryLeakDetectorRejectsInjectedLocalFields(t *testing.T) {
	payloads := map[string][]byte{
		"agent": []byte(`{"source_reports":[{"summary":"injected"}]}`),
	}
	err := rejectBoundaryLeaks(payloads, []string{`"source_reports":`})
	if err == nil || !strings.Contains(err.Error(), "source_reports") {
		t.Fatalf("injected leak was not rejected: %v", err)
	}
	delete(payloads, "agent")
	payloads["agent"] = []byte(`{"features":[{"id":"portable-only"}]}`)
	if err := rejectBoundaryLeaks(payloads, []string{`"source_reports":`}); err != nil {
		t.Fatalf("clean payload rejected after injection was removed: %v", err)
	}
}

func fixturePath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "testdata", "simulation-e2e"))
}
