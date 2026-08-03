package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/deai"
)

func TestDeAIStoreEnablesAndPersistsVersionedAudit(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if enabled, err := s.DeAI.Enabled(); err != nil || enabled {
		t.Fatalf("initial enabled = %v, %v", enabled, err)
	}
	if err := s.DeAI.Enable(); err != nil {
		t.Fatal(err)
	}
	if enabled, err := s.DeAI.Enabled(); err != nil || !enabled {
		t.Fatalf("enabled = %v, %v", enabled, err)
	}
	audit := deai.Audit{Version: deai.PolicyVersion, Chapter: 7, DraftSHA256: "sha256:test", Passed: true}
	if err := s.DeAI.SaveAudit(audit); err != nil {
		t.Fatal(err)
	}
	got, err := s.DeAI.LoadAudit(7)
	if err != nil || got == nil || got.DraftSHA256 != audit.DraftSHA256 {
		t.Fatalf("LoadAudit = %+v, %v", got, err)
	}
}
