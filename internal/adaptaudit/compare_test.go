package adaptaudit

import (
	"testing"
	"time"
)

func TestFindingFingerprintIgnoresMessageAndSeverity(t *testing.T) {
	left := Finding{Code: "missing_event", Severity: "warning", Message: "first wording", EventID: "evt-1", TargetChapters: []int{3, 2}}
	right := Finding{Code: "missing_event", Severity: "critical", Message: "rewritten wording", EventID: "evt-1", TargetChapters: []int{2, 3}}
	if ComputeFindingFingerprint(left) != ComputeFindingFingerprint(right) {
		t.Fatal("fingerprint changed with presentation-only fields")
	}
}

func TestCompareAuditRunsRejectsModesAndClassifiesUnchanged(t *testing.T) {
	report := Audit(Input{Mode: ModeFree, Scope: Scope{TargetFrom: 1, TargetTo: 2}})
	base, err := NewAuditRun(report, AuditKindContract, AuditTriggerManual, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := NewAuditRun(report, AuditKindModelSecondPass, AuditTriggerManual, &ModelSnapshot{Model: "strong"}, time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := CompareAuditRuns(base, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !comparison.AttributableToModel || comparison.Confidence != "high" {
		t.Fatalf("comparison=%+v", comparison)
	}

	arc := Audit(Input{Mode: ModeArc, Scope: report.Scope})
	arcRun, _ := NewAuditRun(arc, AuditKindContract, AuditTriggerManual, nil, time.Unix(3, 0))
	if _, err := CompareAuditRuns(base, arcRun); err == nil {
		t.Fatal("expected mode mismatch")
	}
}

func TestFindingsByFingerprintKeepsAmbiguousDuplicates(t *testing.T) {
	finding := Finding{Code: "duplicate", TargetChapters: []int{1}}
	finding.Fingerprint = ComputeFindingFingerprint(finding)
	groups, _ := findingsByFingerprint([]Finding{finding, finding}, Scope{TargetFrom: 1, TargetTo: 1}, true)
	if got := len(groups[finding.Fingerprint]); got != 2 {
		t.Fatalf("duplicate findings collapsed: %d", got)
	}
}

func TestPartialScopeComparisonExcludesUnlocatedFindings(t *testing.T) {
	unlocated := Finding{Code: "global", Message: "no locator"}
	unlocated.Fingerprint = ComputeFindingFingerprint(unlocated)
	groups, excluded := findingsByFingerprint([]Finding{unlocated}, Scope{TargetFrom: 2, TargetTo: 3}, false)
	if excluded != 1 || len(groups) != 0 {
		t.Fatalf("groups=%v excluded=%d", groups, excluded)
	}
	located := unlocated
	located.Evidence = []Evidence{{ArtifactID: "target-body-0002", Quote: "x"}}
	located.Fingerprint = ComputeFindingFingerprint(located)
	groups, excluded = findingsByFingerprint([]Finding{located}, Scope{TargetFrom: 2, TargetTo: 3}, false)
	if excluded != 0 || len(groups) != 1 {
		t.Fatalf("located groups=%v excluded=%d", groups, excluded)
	}
}
