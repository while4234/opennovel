package adaptaudit

// NewModelSecondPassReport builds a digest-valid, read-only report from
// server-verified model findings. It deliberately never offers auto-repair.
func NewModelSecondPassReport(mode Mode, scope Scope, inputDigest string, findings []Finding) Report {
	status := "pass"
	for _, finding := range findings {
		if finding.Blocking {
			status = "fail"
			break
		}
		if finding.Source == "model_assessment" && status == "pass" {
			status = "inconclusive"
		}
	}
	report := Report{
		Version: reportVersion, Mode: mode, Scope: scope, InputDigest: inputDigest,
		Status: status, ReadOnly: true, Findings: findings,
		Confirmation: Confirmation{Required: false, SuggestedAction: "review model findings before queuing any repair"},
	}
	report.Digest = computeReportDigest(report)
	report.Confirmation.ReportDigest = report.Digest
	return report
}
