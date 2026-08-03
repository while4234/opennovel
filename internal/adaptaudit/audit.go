package adaptaudit

import (
	"fmt"
	"sort"
	"strings"
)

const reportVersion = 1

type auditor struct {
	input         Input
	artifacts     map[string]Artifact
	events        map[string]Event
	bindings      map[string][]Binding
	validEvidence int
	findings      []Finding
	metrics       Metrics
}

func Audit(input Input) Report {
	a := newAuditor(input)
	a.validateAllEvidence()
	switch input.Mode {
	case ModeChapter:
		a.auditChapter()
	case ModeArc:
		a.auditArc()
	case ModeFree:
		a.auditFree()
	default:
		a.addFinding("invalid_mode", "critical", true, fmt.Sprintf("unsupported adaptation mode %q", input.Mode), "", "", nil)
	}
	a.auditCausalOrder()
	a.metrics.ValidEvidenceItems = a.validEvidence
	sortFindings(a.findings)
	status := "pass"
	if hasBlockingFinding(a.findings) {
		status = "fail"
	}
	return a.report(status, true)
}

// AuditEvidenceOnly validates stored prose quotes without interpreting missing
// canonical bindings as missing story content. It is used for legacy plans
// that predate the current audit contract and must never offer auto-repair.
func AuditEvidenceOnly(input Input, code, message string) Report {
	a := newAuditor(input)
	a.validateAllEvidence()
	for index := range a.findings {
		a.findings[index].Blocking = false
	}
	a.addFinding(code, "warning", false, message, "", "", nil)
	a.metrics.ValidEvidenceItems = a.validEvidence
	sortFindings(a.findings)
	return a.report("inconclusive", false)
}

func (a *auditor) report(status string, allowConfirmation bool) Report {
	report := Report{
		Version:     reportVersion,
		Mode:        a.input.Mode,
		InputDigest: ComputeInputDigest(a.input),
		Status:      status,
		ReadOnly:    true,
		Scope:       a.input.Scope,
		Findings:    a.findings,
		Metrics:     a.metrics,
	}
	blocking := blockingFindingIDs(a.findings)
	if !allowConfirmation {
		blocking = nil
	}
	action := suggestedAction(a.input.Mode, allowConfirmation && len(blocking) > 0)
	if status == "inconclusive" && !allowConfirmation {
		action = "read-only evidence report; canonical audit contracts are required before any repair"
	}
	report.Confirmation = Confirmation{
		Required:           allowConfirmation && len(blocking) > 0,
		BlockingFindingIDs: blocking,
		SuggestedAction:    action,
	}
	report.Digest = computeReportDigest(report)
	report.Confirmation.ReportDigest = report.Digest
	return report
}

func newAuditor(input Input) *auditor {
	a := &auditor{
		input:     input,
		artifacts: make(map[string]Artifact, len(input.Artifacts)),
		events:    make(map[string]Event, len(input.Events)),
		bindings:  make(map[string][]Binding),
		metrics: Metrics{
			Events:         len(input.Events),
			SourceSegments: len(input.SourceSegments),
		},
	}
	for _, artifact := range input.Artifacts {
		a.artifacts[artifact.ID] = artifact
	}
	for _, event := range input.Events {
		a.events[event.ID] = event
		if event.Required {
			a.metrics.RequiredEvents++
		}
	}
	bound := make(map[string]bool)
	for _, binding := range input.Bindings {
		if binding.EventID != "" {
			a.bindings[binding.EventID] = append(a.bindings[binding.EventID], binding)
			if _, exists := a.events[binding.EventID]; exists {
				bound[binding.EventID] = true
			}
		}
	}
	a.metrics.BoundEvents = len(bound)
	return a
}

func (a *auditor) validateAllEvidence() {
	check := func(owner string, evidence []Evidence) {
		for _, item := range evidence {
			if a.evidenceValid(item) {
				a.validEvidence++
				continue
			}
			a.addFinding("invalid_evidence", "error", true,
				fmt.Sprintf("%s cites text that is absent from artifact %q", owner, item.ArtifactID), owner, "", nil)
		}
	}
	for _, event := range a.input.Events {
		check(event.ID, event.HighPlanEvidence)
		for _, claim := range event.SettingClaims {
			check(event.ID, claim.Evidence)
		}
	}
	for _, binding := range a.input.Bindings {
		owner := binding.EventID
		if owner == "" && len(binding.SourceSegmentIDs) > 0 {
			owner = binding.SourceSegmentIDs[0]
		}
		check(owner, binding.PlanEvidence)
		check(owner, binding.BodyEvidence)
	}
}

func (a *auditor) evidenceValid(item Evidence) bool {
	artifact, ok := a.artifacts[item.ArtifactID]
	quote := strings.TrimSpace(item.Quote)
	return ok && quote != "" && strings.Contains(artifact.Text, item.Quote)
}

func (a *auditor) evidenceSetValid(items []Evidence, kind ArtifactKind) bool {
	for _, item := range items {
		artifact, ok := a.artifacts[item.ArtifactID]
		if !ok || artifact.Kind != kind || !a.evidenceValid(item) {
			continue
		}
		return true
	}
	return false
}

func (a *auditor) auditChapter() {
	byChapter := make(map[int][]SourceSegment)
	for _, segment := range a.input.SourceSegments {
		byChapter[segment.Chapter] = append(byChapter[segment.Chapter], segment)
	}
	for chapter, segments := range byChapter {
		for index, segment := range segments {
			chapters := []int{segment.TargetChapter}
			if !segment.ContractPresent {
				a.addFinding("segment_contract_missing", "critical", true,
					fmt.Sprintf("target chapter %d has no durable SourceSegment contract for source chapter %d", segment.TargetChapter, chapter), "", segment.ID, chapters)
			}
			if segment.Sequence != index+1 {
				a.addFinding("segment_sequence", "critical", true,
					fmt.Sprintf("source chapter %d segment sequence is %d at position %d", chapter, segment.Sequence, index+1), "", segment.ID, chapters)
			}
			if segment.EntryState == nil || segment.ExitState == nil {
				a.addFinding("segment_state_missing", "critical", true,
					fmt.Sprintf("source segment %s is missing entry or exit state", segment.ID), "", segment.ID, chapters)
			}
			if index > 0 && !statesEqual(segments[index-1].ExitState, segment.EntryState) {
				a.addFinding("segment_state_discontinuity", "critical", true,
					fmt.Sprintf("source segment %s entry state does not match the previous exit state", segment.ID), "", segment.ID, chapters)
			}
		}
		if totalRunes, maxRunes := sourceSegmentLimits(segments); totalRunes > 0 && maxRunes > 0 {
			minimum := (totalRunes-1)/maxRunes + 1
			if len(segments) < minimum {
				var targetChapters []int
				for _, segment := range segments {
					targetChapters = append(targetChapters, segment.TargetChapter)
				}
				a.addFinding("insufficient_segments", "critical", true,
					fmt.Sprintf("source chapter %d has %d segments; at least %d are required for %d runes", chapter, len(segments), minimum, totalRunes), "", "", targetChapters)
			}
		}
		sort.Slice(segments, func(i, j int) bool {
			if segments[i].FromRune != segments[j].FromRune {
				return segments[i].FromRune < segments[j].FromRune
			}
			return segments[i].ToRune < segments[j].ToRune
		})
		expectedFrom := 0
		totalRunes := 0
		for _, segment := range segments {
			if segment.FromRune > expectedFrom {
				a.addFinding("long_split_gap", "critical", true,
					fmt.Sprintf("source chapter %d has an uncovered rune gap %d-%d", chapter, expectedFrom, segment.FromRune), "", segment.ID, nil)
			}
			if segment.FromRune < expectedFrom {
				a.addFinding("long_split_overlap", "critical", true,
					fmt.Sprintf("source chapter %d has overlapping segments near rune %d", chapter, segment.FromRune), "", segment.ID, nil)
			}
			if segment.ToRune <= segment.FromRune {
				a.addFinding("invalid_source_segment", "critical", true,
					fmt.Sprintf("source segment %s has invalid rune range", segment.ID), "", segment.ID, nil)
			}
			if segment.ToRune > expectedFrom {
				expectedFrom = segment.ToRune
			}
			if segment.TotalRunes > totalRunes {
				totalRunes = segment.TotalRunes
			}
		}
		if totalRunes > 0 && expectedFrom < totalRunes {
			a.addFinding("long_split_gap", "critical", true,
				fmt.Sprintf("source chapter %d ends at rune %d but source has %d runes", chapter, expectedFrom, totalRunes), "", "", nil)
		}
	}

	lastTarget := 0
	segments := append([]SourceSegment(nil), a.input.SourceSegments...)
	sort.Slice(segments, func(i, j int) bool {
		if segments[i].Chapter != segments[j].Chapter {
			return segments[i].Chapter < segments[j].Chapter
		}
		return segments[i].FromRune < segments[j].FromRune
	})
	for _, segment := range segments {
		bindings := a.bindingsForSegment(segment.ID)
		if segment.Required && len(bindings) == 0 {
			a.addFinding("missing_segment_coverage", "critical", true,
				fmt.Sprintf("required source segment %s is not assigned to a target chapter", segment.ID), "", segment.ID, nil)
			continue
		}
		covered := false
		for _, binding := range bindings {
			chapters := sortedPositiveInts(binding.TargetChapters)
			if len(chapters) > 0 && chapters[0] < lastTarget {
				a.addFinding("segment_target_order", "critical", true,
					fmt.Sprintf("source segment %s maps before an earlier source segment", segment.ID), "", segment.ID, chapters)
			}
			if len(chapters) > 0 && chapters[len(chapters)-1] > lastTarget {
				lastTarget = chapters[len(chapters)-1]
			}
			if a.evidenceSetValid(binding.PlanEvidence, ArtifactTargetPlan) &&
				a.evidenceSetValid(binding.BodyEvidence, ArtifactTargetChapter) {
				covered = true
			}
		}
		if segment.Required && !covered {
			a.addFinding("missing_segment_coverage", "critical", true,
				fmt.Sprintf("source segment %s lacks target-plan or body evidence", segment.ID), "", segment.ID, nil)
		} else if covered {
			a.metrics.CoveredSegments++
		}
	}
}

func sourceSegmentLimits(segments []SourceSegment) (int, int) {
	totalRunes, maxRunes := 0, 0
	for _, segment := range segments {
		if segment.TotalRunes > totalRunes {
			totalRunes = segment.TotalRunes
		}
		if segment.MaxRunes > maxRunes {
			maxRunes = segment.MaxRunes
		}
	}
	return totalRunes, maxRunes
}

func statesEqual(left, right map[string]string) bool {
	if left == nil || right == nil || len(left) != len(right) {
		return false
	}
	for key, value := range left {
		rightValue, exists := right[key]
		if !exists || rightValue != value {
			return false
		}
	}
	return true
}

func (a *auditor) auditArc() {
	missingMainline := make(map[string]bool)
	for _, event := range a.input.Events {
		if event.Class != ClassMainline || !event.Required {
			continue
		}
		bindings := a.bindings[event.ID]
		planOK, bodyOK := false, false
		for _, binding := range bindings {
			planOK = planOK || a.evidenceSetValid(binding.PlanEvidence, ArtifactTargetPlan)
			bodyOK = bodyOK || a.evidenceSetValid(binding.BodyEvidence, ArtifactTargetChapter)
		}
		if !planOK {
			missingMainline[event.ID] = true
			a.addFinding("missing_mainline_plan_binding", "critical", true,
				fmt.Sprintf("mainline event %s is promised above chapter level but absent from target chapter plans", event.ID), event.ID, "", nil)
		}
		if !bodyOK {
			missingMainline[event.ID] = true
			a.addFinding("missing_mainline_body_evidence", "critical", true,
				fmt.Sprintf("mainline event %s has no verifiable target prose evidence", event.ID), event.ID, "", nil)
		}
		chapters := bindingChapters(bindings)
		if len(chapters) > 1 {
			a.addFinding("duplicate_event_reuse", "error", true,
				fmt.Sprintf("mainline event %s is assigned to more than one target chapter", event.ID), event.ID, "", chapters)
		}
	}
	if len(missingMainline) == 0 {
		return
	}
	for _, event := range a.input.Events {
		if event.Origin != OriginAdded {
			continue
		}
		bindings := a.bindings[event.ID]
		servesMissing := false
		for _, binding := range bindings {
			for _, served := range binding.ServesEventIDs {
				if missingMainline[served] {
					servesMissing = true
				}
			}
		}
		if !servesMissing {
			a.addFinding("added_event_displaces_mainline", "critical", true,
				fmt.Sprintf("added event %s occupies target space while required mainline remains unassigned", event.ID), event.ID, "", bindingChapters(bindings))
		}
	}
}

func (a *auditor) auditFree() {
	states := make(map[string]string, len(a.input.RelationshipStates))
	for key, value := range a.input.RelationshipStates {
		states[key] = value
	}
	events := append([]Event(nil), a.input.Events...)
	sort.SliceStable(events, func(i, j int) bool {
		return firstBindingChapter(a.bindings[events[i].ID]) < firstBindingChapter(a.bindings[events[j].ID])
	})
	for _, event := range events {
		// Ordinary source events are references in Free mode, not obligations.
		if event.Origin == OriginSource && event.Class == ClassOrdinary {
			continue
		}
		change := event.Relationship
		if change == nil {
			continue
		}
		current := states[change.Pair]
		if current == "" {
			current = change.From
		}
		allowed := len(change.AllowedFrom) == 0 || contains(change.AllowedFrom, current)
		requirementsMet := true
		for _, requiredEventID := range change.RequiresEventIDs {
			if firstBindingChapter(a.bindings[requiredEventID]) <= 0 ||
				firstBindingChapter(a.bindings[requiredEventID]) >= firstBindingChapter(a.bindings[event.ID]) {
				requirementsMet = false
			}
		}
		if !allowed || !requirementsMet {
			a.addFinding("relationship_state_jump", "critical", true,
				fmt.Sprintf("relationship %s jumps from %s to %s without an allowed prior state or prerequisite event", change.Pair, current, change.To), event.ID, "", bindingChapters(a.bindings[event.ID]))
			continue
		}
		states[change.Pair] = change.To
	}

	locks := make(map[string]string, len(a.input.SettingLocks))
	for _, lock := range a.input.SettingLocks {
		locks[lock.Key] = lock.Value
	}
	for _, event := range a.input.Events {
		for _, claim := range event.SettingClaims {
			locked, ok := locks[claim.Key]
			if !ok || locked == claim.Value || !a.hasAnyValidEvidence(claim.Evidence) {
				continue
			}
			a.addFinding("setting_lock_conflict", "critical", true,
				fmt.Sprintf("setting %s is locked to %q but target prose claims %q", claim.Key, locked, claim.Value), event.ID, "", bindingChapters(a.bindings[event.ID]))
		}
	}
}

func (a *auditor) auditCausalOrder() {
	for _, event := range a.input.Events {
		// Free mode deliberately ignores unbound ordinary source events.
		if a.input.Mode == ModeFree && event.Origin == OriginSource && event.Class == ClassOrdinary {
			continue
		}
		eventChapter := firstBindingChapter(a.bindings[event.ID])
		if eventChapter <= 0 {
			continue
		}
		for _, dependencyID := range event.DependsOn {
			dependencyChapter := firstBindingChapter(a.bindings[dependencyID])
			if dependencyChapter > 0 && dependencyChapter < eventChapter {
				continue
			}
			a.addFinding("causal_order_violation", "critical", true,
				fmt.Sprintf("event %s appears before prerequisite %s", event.ID, dependencyID), event.ID, "", []int{eventChapter})
		}
	}
}

func (a *auditor) bindingsForSegment(segmentID string) []Binding {
	var out []Binding
	for _, binding := range a.input.Bindings {
		if contains(binding.SourceSegmentIDs, segmentID) {
			out = append(out, binding)
		}
	}
	return out
}

func (a *auditor) hasAnyValidEvidence(items []Evidence) bool {
	for _, item := range items {
		if a.evidenceValid(item) {
			return true
		}
	}
	return false
}

func (a *auditor) addFinding(code, severity string, blocking bool, message, eventID, segmentID string, chapters []int) {
	base := code + ":" + eventID + ":" + segmentID + ":" + strings.TrimSpace(message)
	finding := Finding{
		ID:             shortDigest(base),
		Code:           code,
		Severity:       severity,
		Blocking:       blocking,
		Message:        message,
		EventID:        eventID,
		SegmentID:      segmentID,
		TargetChapters: sortedPositiveInts(chapters),
	}
	finding.Fingerprint = ComputeFindingFingerprint(finding)
	a.findings = append(a.findings, finding)
}

func bindingChapters(bindings []Binding) []int {
	var chapters []int
	for _, binding := range bindings {
		chapters = append(chapters, binding.TargetChapters...)
	}
	return sortedPositiveInts(chapters)
}

func firstBindingChapter(bindings []Binding) int {
	chapters := bindingChapters(bindings)
	if len(chapters) == 0 {
		return 0
	}
	return chapters[0]
}

func sortedPositiveInts(values []int) []int {
	seen := make(map[int]bool, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		if value > 0 && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Ints(out)
	return out
}

func contains[T comparable](values []T, want T) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Code != findings[j].Code {
			return findings[i].Code < findings[j].Code
		}
		return findings[i].ID < findings[j].ID
	})
}

func hasBlockingFinding(findings []Finding) bool {
	for _, finding := range findings {
		if finding.Blocking {
			return true
		}
	}
	return false
}

func blockingFindingIDs(findings []Finding) []string {
	var ids []string
	for _, finding := range findings {
		if finding.Blocking {
			ids = append(ids, finding.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func suggestedAction(mode Mode, failed bool) string {
	if !failed {
		return "no repair required"
	}
	switch mode {
	case ModeChapter:
		return "repair source-segment allocation before rewriting affected target chapters"
	case ModeArc:
		return "restore missing mainline events in chapter plans before rewriting affected chapters"
	case ModeFree:
		return "repair target-story causality, relationship transitions, or setting conflicts"
	default:
		return "repair invalid audit input"
	}
}
