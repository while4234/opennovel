package adaptaudit

import (
	"fmt"
	"sort"
	"strings"
)

// ComputeFindingFingerprint deliberately excludes model-written prose,
// severity and quotes. It identifies the audited contract location.
func ComputeFindingFingerprint(finding Finding) string {
	chapters := sortedPositiveInts(finding.TargetChapters)
	artifacts := make([]string, 0, len(finding.Evidence))
	for _, evidence := range finding.Evidence {
		if id := strings.TrimSpace(evidence.ArtifactID); id != "" {
			artifacts = append(artifacts, id)
		}
	}
	sort.Strings(artifacts)
	base := strings.Join([]string{
		strings.TrimSpace(finding.Code),
		strings.TrimSpace(finding.EventID),
		strings.TrimSpace(finding.SegmentID),
		fmt.Sprint(chapters),
		strings.Join(artifacts, ","),
	}, "|")
	return shortDigest(base)
}

func CompareAuditRuns(base, candidate AuditRun) (AuditComparison, error) {
	if base.RunID == "" || candidate.RunID == "" {
		return AuditComparison{}, fmt.Errorf("both audit run ids are required")
	}
	if base.Report.Mode != candidate.Report.Mode {
		return AuditComparison{}, fmt.Errorf("audit modes do not match: %s and %s", base.Report.Mode, candidate.Report.Mode)
	}
	overlap, ok := intersectScope(base.Scope, candidate.Scope)
	if !ok {
		return AuditComparison{}, fmt.Errorf("audit scopes do not overlap")
	}
	comparison := AuditComparison{
		BaseRunID: base.RunID, CandidateRunID: candidate.RunID,
		ComparedScope: overlap, Confidence: "high",
		AttributableToModel: base.InputDigest != "" && base.InputDigest == candidate.InputDigest,
	}
	if base.Scope != candidate.Scope {
		comparison.Confidence = "partial"
		comparison.Warnings = append(comparison.Warnings, "only the overlapping audit scope was compared")
	}
	if !comparison.AttributableToModel {
		comparison.Confidence = "context_changed"
		comparison.Warnings = append(comparison.Warnings, "inputs changed; differences cannot be attributed to the model")
	}
	allowUnlocated := base.Scope == candidate.Scope
	before, excludedBefore := findingsByFingerprint(base.Report.Findings, overlap, allowUnlocated)
	after, excludedAfter := findingsByFingerprint(candidate.Report.Findings, overlap, allowUnlocated)
	if excludedBefore+excludedAfter > 0 {
		comparison.Warnings = append(comparison.Warnings, fmt.Sprintf("%d findings without a locator were excluded from the partial-scope comparison", excludedBefore+excludedAfter))
	}
	keys := make([]string, 0, len(before)+len(after))
	seen := make(map[string]bool, len(before)+len(after))
	for fingerprint := range before {
		seen[fingerprint] = true
		keys = append(keys, fingerprint)
	}
	for fingerprint := range after {
		if !seen[fingerprint] {
			keys = append(keys, fingerprint)
		}
	}
	sort.Strings(keys)
	for _, fingerprint := range keys {
		leftItems, rightItems := before[fingerprint], after[fingerprint]
		count := max(len(leftItems), len(rightItems))
		for index := 0; index < count; index++ {
			change := FindingChange{Fingerprint: fingerprint}
			switch {
			case index >= len(leftItems):
				change.Change, change.After = "introduced", cloneFinding(rightItems[index])
			case index >= len(rightItems):
				change.Change, change.Before = "resolved", cloneFinding(leftItems[index])
			default:
				change.Before, change.After = cloneFinding(leftItems[index]), cloneFinding(rightItems[index])
				change.Change = classifyFindingChange(leftItems[index], rightItems[index])
			}
			comparison.Changes = append(comparison.Changes, change)
		}
	}
	return comparison, nil
}

func findingsByFingerprint(findings []Finding, scope Scope, allowUnlocated bool) (map[string][]Finding, int) {
	out := make(map[string][]Finding)
	excluded := 0
	for _, finding := range findings {
		overlaps, located := findingOverlapsScope(finding, scope)
		if !located && !allowUnlocated {
			excluded++
			continue
		}
		if located && !overlaps {
			continue
		}
		fingerprint := finding.Fingerprint
		if fingerprint == "" {
			fingerprint = ComputeFindingFingerprint(finding)
		}
		out[fingerprint] = append(out[fingerprint], finding)
	}
	for fingerprint := range out {
		sort.SliceStable(out[fingerprint], func(i, j int) bool {
			return findingOrderKey(out[fingerprint][i]) < findingOrderKey(out[fingerprint][j])
		})
	}
	return out, excluded
}

func findingOrderKey(finding Finding) string {
	artifacts := make([]string, 0, len(finding.Evidence))
	for _, evidence := range finding.Evidence {
		artifacts = append(artifacts, evidence.ArtifactID)
	}
	sort.Strings(artifacts)
	return strings.Join([]string{finding.EventID, finding.SegmentID, fmt.Sprint(sortedPositiveInts(finding.TargetChapters)), strings.Join(artifacts, ","), finding.Message, finding.ID}, "|")
}

func classifyFindingChange(before, after Finding) string {
	left, right := severityRank(before.Severity), severityRank(after.Severity)
	if right > left || after.Blocking && !before.Blocking {
		return "worsened"
	}
	if right < left || before.Blocking && !after.Blocking {
		return "improved"
	}
	if before.Message != after.Message || fmt.Sprint(before.Evidence) != fmt.Sprint(after.Evidence) {
		return "explanation_changed"
	}
	return "unchanged"
}

func severityRank(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return 4
	case "error", "high":
		return 3
	case "warning", "medium":
		return 2
	case "info", "low":
		return 1
	default:
		return 0
	}
}

func intersectScope(left, right Scope) (Scope, bool) {
	overlap := Scope{
		SourceFrom: maxPositive(left.SourceFrom, right.SourceFrom),
		SourceTo:   minPositive(left.SourceTo, right.SourceTo),
		TargetFrom: maxPositive(left.TargetFrom, right.TargetFrom),
		TargetTo:   minPositive(left.TargetTo, right.TargetTo),
	}
	if overlap.SourceFrom > 0 && overlap.SourceTo > 0 && overlap.SourceFrom > overlap.SourceTo {
		return Scope{}, false
	}
	if overlap.TargetFrom > 0 && overlap.TargetTo > 0 && overlap.TargetFrom > overlap.TargetTo {
		return Scope{}, false
	}
	return overlap, true
}

func findingOverlapsScope(finding Finding, scope Scope) (bool, bool) {
	if len(finding.TargetChapters) == 0 {
		for _, evidence := range finding.Evidence {
			if chapter, kind, ok := auditArtifactChapter(evidence.ArtifactID); ok {
				if kind == "source" {
					return (scope.SourceFrom <= 0 || chapter >= scope.SourceFrom) && (scope.SourceTo <= 0 || chapter <= scope.SourceTo), true
				}
				return (scope.TargetFrom <= 0 || chapter >= scope.TargetFrom) && (scope.TargetTo <= 0 || chapter <= scope.TargetTo), true
			}
		}
		var sourceChapter int
		if _, err := fmt.Sscanf(finding.SegmentID, "src-%04d-seg", &sourceChapter); err == nil && sourceChapter > 0 {
			return (scope.SourceFrom <= 0 || sourceChapter >= scope.SourceFrom) && (scope.SourceTo <= 0 || sourceChapter <= scope.SourceTo), true
		}
		return false, false
	}
	for _, chapter := range finding.TargetChapters {
		if (scope.TargetFrom <= 0 || chapter >= scope.TargetFrom) && (scope.TargetTo <= 0 || chapter <= scope.TargetTo) {
			return true, true
		}
	}
	return false, true
}

func auditArtifactChapter(id string) (int, string, bool) {
	for _, candidate := range []struct{ format, kind string }{{"target-body-%04d", "target"}, {"target-plan-%04d", "target"}, {"source-%04d", "source"}} {
		var chapter int
		if _, err := fmt.Sscanf(id, candidate.format, &chapter); err == nil && chapter > 0 {
			return chapter, candidate.kind, true
		}
	}
	return 0, "", false
}

func maxPositive(a, b int) int {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	return max(a, b)
}
func minPositive(a, b int) int {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	return min(a, b)
}
func cloneFinding(f Finding) *Finding { copy := f; return &copy }
