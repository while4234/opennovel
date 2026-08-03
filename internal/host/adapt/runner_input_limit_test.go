package adapt

import "testing"

func TestPlannerInputLimitUsesCompiledTokenBudget(t *testing.T) {
	if got := plannerInputLimitBytes(StagePlan); got != 0 {
		t.Fatalf("plan input byte limit = %d, want disabled after tokenizer-aware compilation", got)
	}
	if got := plannerInputLimitBytes(StageDossier); got != adaptationPlannerDefaultInputLimitBytes {
		t.Fatalf("dossier input byte limit = %d, want %d", got, adaptationPlannerDefaultInputLimitBytes)
	}
	if got := plannerInputLimitBytes(StageAudit); got != adaptationPlannerAuditInputLimitBytes {
		t.Fatalf("audit input byte limit = %d, want %d", got, adaptationPlannerAuditInputLimitBytes)
	}
	if got := plannerInputLimitBytes(StageAudit); got != 128*1024 {
		t.Fatalf("audit input byte limit = %d, want 128 KiB for bounded full-book outline audits", got)
	}
}
