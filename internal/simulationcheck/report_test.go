package simulationcheck

import (
	"strings"
	"testing"
	"time"
)

func TestReportCurrentRejectsEveryBindingChange(t *testing.T) {
	digest := strings.Repeat("a", 64)
	report, err := Finalize(Report{
		Version: ReportVersion, Revision: 1, ProjectDigest: digest,
		Chapter: 2, DraftDigest: digest, ProfileDigest: strings.Repeat("b", 64),
		ContractRevision: 3, ContractDigest: strings.Repeat("c", 64),
		EffectiveMode: "reinforced", CheckerVersion: CheckerVersion,
		CheckerDigest: ConfigurationDigest(), SafetyIndexDigest: strings.Repeat("d", 64),
		CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		Capability: Capability{State: CapabilityFull, LocalIndex: true, ContractChecks: true},
		CopyStatus: StatusPass, ContractStatus: StatusPass, Passed: true,
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	binding := Binding{
		ProjectDigest: report.ProjectDigest, Chapter: report.Chapter,
		DraftDigest: report.DraftDigest, ProfileDigest: report.ProfileDigest,
		ContractRevision: report.ContractRevision, ContractDigest: report.ContractDigest,
		EffectiveMode: report.EffectiveMode, CheckerDigest: report.CheckerDigest,
		SafetyIndexDigest: report.SafetyIndexDigest,
	}
	if ok, reason := Current(&report, binding); !ok {
		t.Fatalf("current report rejected: %s", reason)
	}
	cases := []struct {
		name string
		edit func(*Binding)
	}{
		{"draft", func(b *Binding) { b.DraftDigest = strings.Repeat("e", 64) }},
		{"profile", func(b *Binding) { b.ProfileDigest = strings.Repeat("e", 64) }},
		{"contract revision", func(b *Binding) { b.ContractRevision++ }},
		{"contract digest", func(b *Binding) { b.ContractDigest = strings.Repeat("e", 64) }},
		{"mode", func(b *Binding) { b.EffectiveMode = "normal" }},
		{"checker", func(b *Binding) { b.CheckerDigest = strings.Repeat("e", 64) }},
		{"index", func(b *Binding) { b.SafetyIndexDigest = strings.Repeat("e", 64) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			changed := binding
			test.edit(&changed)
			if ok, _ := Current(&report, changed); ok {
				t.Fatal("changed binding was accepted")
			}
		})
	}
}
