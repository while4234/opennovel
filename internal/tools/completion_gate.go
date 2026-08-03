package tools

// CompletionAuditResult is the small, package-neutral contract returned by a
// completion audit. ReportDigest lets callers surface the exact immutable
// report that made the decision without coupling tools to the audit package.
type CompletionAuditResult struct {
	Applicable   bool   `json:"applicable"`
	Allowed      bool   `json:"allowed"`
	Status       string `json:"status"`
	ReportDigest string `json:"report_digest,omitempty"`
	Warning      string `json:"warning,omitempty"`
}

// CompletionGate runs the deterministic full-book audit immediately before a
// durable transition to PhaseComplete. Non-adaptation projects return an
// allowed, non-applicable result.
type CompletionGate interface {
	EvaluateCompletion() (CompletionAuditResult, error)
}

func completionGateFrom(values []CompletionGate) CompletionGate {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}
