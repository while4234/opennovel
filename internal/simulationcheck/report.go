package simulationcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	ReportVersion  = "simulation_check_report.v1"
	CheckerVersion = "simulation_checker.v1"

	CapabilityFull        = "full"
	CapabilityPartial     = "partial"
	CapabilityUnavailable = "unavailable"

	StatusPass       = "pass"
	StatusFail       = "fail"
	StatusAdvisory   = "advisory"
	StatusUnverified = "unverified"
)

// Report is a durable receipt for one exact draft, profile, contract and
// checker configuration. It never stores source wording or local paths.
type Report struct {
	Version           string           `json:"version"`
	Revision          int64            `json:"revision"`
	ReportDigest      string           `json:"report_digest"`
	ProjectDigest     string           `json:"project_digest"`
	Chapter           int              `json:"chapter"`
	DraftDigest       string           `json:"draft_digest"`
	ProfileDigest     string           `json:"profile_digest"`
	ContractRevision  int64            `json:"contract_revision"`
	ContractDigest    string           `json:"contract_digest"`
	EffectiveMode     string           `json:"effective_mode"`
	CheckerVersion    string           `json:"checker_version"`
	CheckerDigest     string           `json:"checker_digest"`
	SafetyIndexDigest string           `json:"safety_index_digest,omitempty"`
	CheckedAt         string           `json:"checked_at"`
	Capability        Capability       `json:"capability"`
	CopyStatus        string           `json:"copy_status"`
	ContractStatus    string           `json:"contract_status"`
	Passed            bool             `json:"passed"`
	Risks             []Risk           `json:"risks,omitempty"`
	MustChecks        []MustCheck      `json:"must_checks,omitempty"`
	ShouldAdvisories  []ShouldAdvisory `json:"should_advisories,omitempty"`
	Warnings          []string         `json:"warnings,omitempty"`
	Remediation       []string         `json:"remediation,omitempty"`
}

type Capability struct {
	State          string `json:"state"`
	LocalIndex     bool   `json:"local_index"`
	ContractChecks bool   `json:"contract_checks"`
	Reason         string `json:"reason,omitempty"`
}

// Risk contains only the matching excerpt from the user's current draft.
// SourceRefs are opaque report hashes produced by the evidence layer.
type Risk struct {
	ID              string   `json:"id"`
	Type            string   `json:"type"`
	Severity        string   `json:"severity"`
	DraftExcerpt    string   `json:"draft_excerpt"`
	StartRune       int      `json:"start_rune"`
	LengthRunes     int      `json:"length_runes"`
	SourceRefs      []string `json:"source_refs,omitempty"`
	EvidenceSupport int      `json:"evidence_support"`
}

type MustCheck struct {
	FeatureID   string `json:"feature_id"`
	Dimension   string `json:"dimension"`
	Status      string `json:"status"`
	Evidence    string `json:"evidence,omitempty"`
	Remediation string `json:"remediation,omitempty"`
}

type ShouldAdvisory struct {
	FeatureID string `json:"feature_id"`
	Status    string `json:"status"`
}

type Binding struct {
	ProjectDigest     string
	Chapter           int
	DraftDigest       string
	ProfileDigest     string
	ContractRevision  int64
	ContractDigest    string
	EffectiveMode     string
	CheckerDigest     string
	SafetyIndexDigest string
}

func Finalize(report Report) (Report, error) {
	if report.Version == "" {
		report.Version = ReportVersion
	}
	if report.CheckerVersion == "" {
		report.CheckerVersion = CheckerVersion
	}
	digest, err := Digest(report)
	if err != nil {
		return Report{}, err
	}
	report.ReportDigest = digest
	if err := Validate(&report); err != nil {
		return Report{}, err
	}
	return report, nil
}

func Digest(report Report) (string, error) {
	report.ReportDigest = ""
	data, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func Validate(report *Report) error {
	if report == nil || report.Version != ReportVersion {
		return fmt.Errorf("unsupported simulation check report version")
	}
	if report.Revision <= 0 || report.Chapter <= 0 {
		return fmt.Errorf("simulation check report revision and chapter must be positive")
	}
	for name, value := range map[string]string{
		"report digest": report.ReportDigest, "project digest": report.ProjectDigest,
		"draft digest": report.DraftDigest, "checker digest": report.CheckerDigest,
	} {
		if !isSHA256(value) {
			return fmt.Errorf("simulation check %s is invalid", name)
		}
	}
	if report.ProfileDigest != "" && !isSHA256(report.ProfileDigest) {
		return fmt.Errorf("simulation check profile digest is invalid")
	}
	if report.ContractDigest != "" && !isSHA256(report.ContractDigest) {
		return fmt.Errorf("simulation check contract digest is invalid")
	}
	if report.SafetyIndexDigest != "" && !isSHA256(report.SafetyIndexDigest) {
		return fmt.Errorf("simulation check safety index digest is invalid")
	}
	if _, err := time.Parse(time.RFC3339, report.CheckedAt); err != nil {
		return fmt.Errorf("simulation check checked_at is invalid")
	}
	switch report.Capability.State {
	case CapabilityFull, CapabilityPartial, CapabilityUnavailable:
	default:
		return fmt.Errorf("simulation check capability is invalid")
	}
	if report.CopyStatus != StatusPass && report.CopyStatus != StatusFail && report.CopyStatus != StatusUnverified {
		return fmt.Errorf("simulation check copy status is invalid")
	}
	if report.ContractStatus != StatusPass && report.ContractStatus != StatusFail &&
		report.ContractStatus != StatusAdvisory && report.ContractStatus != StatusUnverified {
		return fmt.Errorf("simulation check contract status is invalid")
	}
	for _, risk := range report.Risks {
		if strings.TrimSpace(risk.ID) == "" || strings.TrimSpace(risk.Type) == "" ||
			risk.Severity != "blocking" || risk.StartRune < 0 || risk.LengthRunes <= 0 ||
			len([]rune(risk.DraftExcerpt)) > 220 {
			return fmt.Errorf("simulation check risk is invalid")
		}
		for _, ref := range risk.SourceRefs {
			if !strings.HasPrefix(ref, "source-") || strings.ContainsAny(ref, `/\`) {
				return fmt.Errorf("simulation check source reference is not sanitized")
			}
		}
	}
	for _, check := range report.MustChecks {
		if strings.TrimSpace(check.FeatureID) == "" || strings.TrimSpace(check.Dimension) == "" {
			return fmt.Errorf("simulation must check is incomplete")
		}
		switch check.Status {
		case "met", "missing", "unverifiable":
		default:
			return fmt.Errorf("simulation must check status is invalid")
		}
	}
	expected, err := Digest(*report)
	if err != nil {
		return err
	}
	if expected != report.ReportDigest {
		return fmt.Errorf("simulation check report digest mismatch")
	}
	return nil
}

func Current(report *Report, binding Binding) (bool, string) {
	if report == nil {
		return false, "report_missing"
	}
	if err := Validate(report); err != nil {
		return false, "report_invalid"
	}
	switch {
	case report.CheckerVersion != CheckerVersion || report.CheckerDigest != binding.CheckerDigest:
		return false, "checker_changed"
	case report.ProjectDigest != binding.ProjectDigest || report.Chapter != binding.Chapter:
		return false, "project_or_chapter_changed"
	case report.DraftDigest != binding.DraftDigest:
		return false, "draft_changed"
	case report.ProfileDigest != binding.ProfileDigest:
		return false, "profile_changed"
	case report.ContractRevision != binding.ContractRevision || report.ContractDigest != binding.ContractDigest:
		return false, "contract_changed"
	case report.EffectiveMode != binding.EffectiveMode:
		return false, "mode_changed"
	case report.SafetyIndexDigest != binding.SafetyIndexDigest:
		return false, "safety_index_changed"
	default:
		return true, ""
	}
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(strings.ToLower(value))
	return err == nil
}
