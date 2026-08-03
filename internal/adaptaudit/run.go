package adaptaudit

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const AuditEngineVersion = "2"

func NewAuditRun(report Report, kind AuditKind, trigger AuditTrigger, model *ModelSnapshot, now time.Time) (AuditRun, error) {
	return NewAuditRunAt(report, kind, trigger, model, now, now)
}

func NewAuditRunAt(report Report, kind AuditKind, trigger AuditTrigger, model *ModelSnapshot, startedAt, completedAt time.Time) (AuditRun, error) {
	if err := ValidateReportDigest(report); err != nil {
		return AuditRun{}, err
	}
	if kind == "" {
		kind = AuditKindContract
	}
	if trigger == "" {
		trigger = AuditTriggerManual
	}
	if completedAt.IsZero() {
		completedAt = time.Now()
	}
	if startedAt.IsZero() {
		startedAt = completedAt
	}
	random := make([]byte, 5)
	if _, err := rand.Read(random); err != nil {
		return AuditRun{}, fmt.Errorf("generate audit run id: %w", err)
	}
	runID := completedAt.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random)
	return AuditRun{
		RunID: runID, Kind: kind, Trigger: trigger,
		StartedAt: startedAt.UTC().Format(time.RFC3339Nano), CompletedAt: completedAt.UTC().Format(time.RFC3339Nano),
		Status: report.Status, Scope: report.Scope, InputDigest: report.InputDigest,
		ReportDigest: report.Digest, EngineVersion: AuditEngineVersion, Model: model, Report: report,
	}, nil
}

func ValidateAuditRun(run AuditRun) error {
	if strings.TrimSpace(run.RunID) == "" {
		return fmt.Errorf("audit run id is required")
	}
	if strings.ContainsAny(run.RunID, `/\\`) || run.RunID == "." || run.RunID == ".." {
		return fmt.Errorf("invalid audit run id")
	}
	if err := ValidateReportDigest(run.Report); err != nil {
		return err
	}
	if run.ReportDigest != run.Report.Digest || run.InputDigest != run.Report.InputDigest || run.Scope != run.Report.Scope || run.Status != run.Report.Status {
		return fmt.Errorf("audit run metadata does not match report")
	}
	return nil
}
