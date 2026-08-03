package adaptaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

func ComputeInputDigest(input Input) string {
	normalized := normalizeInput(input)
	raw, _ := json.Marshal(normalized)
	return fullDigest(raw)
}

func ValidateReportDigest(report Report) error {
	if report.Digest == "" {
		return fmt.Errorf("report digest is empty")
	}
	want := computeReportDigest(report)
	if report.Digest != want {
		return fmt.Errorf("report digest mismatch: got %s want %s", report.Digest, want)
	}
	if report.Confirmation.ReportDigest != report.Digest {
		return fmt.Errorf("confirmation report digest is stale")
	}
	return nil
}

func computeReportDigest(report Report) string {
	copyReport := report
	copyReport.Digest = ""
	copyReport.Confirmation.ReportDigest = ""
	sortFindings(copyReport.Findings)
	copyReport.Confirmation.BlockingFindingIDs = append([]string(nil), copyReport.Confirmation.BlockingFindingIDs...)
	sort.Strings(copyReport.Confirmation.BlockingFindingIDs)
	raw, _ := json.Marshal(copyReport)
	return fullDigest(raw)
}

func shortDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

func fullDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func normalizeInput(input Input) Input {
	out := input
	out.Artifacts = append([]Artifact(nil), input.Artifacts...)
	sort.Slice(out.Artifacts, func(i, j int) bool { return out.Artifacts[i].ID < out.Artifacts[j].ID })
	out.Events = append([]Event(nil), input.Events...)
	for i := range out.Events {
		sort.Strings(out.Events[i].SourceSegmentIDs)
		sort.Strings(out.Events[i].DependsOn)
		sortEvidence(out.Events[i].HighPlanEvidence)
		for j := range out.Events[i].SettingClaims {
			sortEvidence(out.Events[i].SettingClaims[j].Evidence)
		}
		sort.Slice(out.Events[i].SettingClaims, func(a, b int) bool {
			return out.Events[i].SettingClaims[a].Key < out.Events[i].SettingClaims[b].Key
		})
	}
	sort.Slice(out.Events, func(i, j int) bool { return out.Events[i].ID < out.Events[j].ID })
	out.Bindings = append([]Binding(nil), input.Bindings...)
	for i := range out.Bindings {
		sort.Strings(out.Bindings[i].SourceSegmentIDs)
		out.Bindings[i].TargetChapters = sortedPositiveInts(out.Bindings[i].TargetChapters)
		sortEvidence(out.Bindings[i].PlanEvidence)
		sortEvidence(out.Bindings[i].BodyEvidence)
		sort.Strings(out.Bindings[i].ServesEventIDs)
	}
	sort.Slice(out.Bindings, func(i, j int) bool {
		if out.Bindings[i].EventID != out.Bindings[j].EventID {
			return out.Bindings[i].EventID < out.Bindings[j].EventID
		}
		return fmt.Sprint(out.Bindings[i].SourceSegmentIDs) < fmt.Sprint(out.Bindings[j].SourceSegmentIDs)
	})
	out.SourceSegments = append([]SourceSegment(nil), input.SourceSegments...)
	sort.Slice(out.SourceSegments, func(i, j int) bool { return out.SourceSegments[i].ID < out.SourceSegments[j].ID })
	out.SettingLocks = append([]SettingLock(nil), input.SettingLocks...)
	sort.Slice(out.SettingLocks, func(i, j int) bool { return out.SettingLocks[i].Key < out.SettingLocks[j].Key })
	return out
}

func sortEvidence(items []Evidence) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].ArtifactID != items[j].ArtifactID {
			return items[i].ArtifactID < items[j].ArtifactID
		}
		return items[i].Quote < items[j].Quote
	})
}
