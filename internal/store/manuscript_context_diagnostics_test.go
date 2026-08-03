package store

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestManuscriptContextDiagnosticsContainOnlyBoundedMetadata(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	record := ManuscriptContextDiagnostic{Task: "writer_segment", LayerBytes: map[string]int{"context": 1200}, InputBytes: 1400, InputTokenEstimate: 350, OutputRunes: 900, OutputTokenEstimate: 450, InputLimitBytes: 60 * 1024, OutputLimitTokens: 7000, ContentSignature: domain.ContentSignature([]byte("payload")), Status: "completed"}
	if err := st.AppendManuscriptContextDiagnostic(record); err != nil {
		t.Fatal(err)
	}
	payload, err := st.ManuscriptRevisions.io.ReadFile(manuscriptContextDiagnosticsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "payload") || !strings.Contains(string(payload), "content_signature") {
		t.Fatalf("diagnostics leaked content or omitted signature: %s", payload)
	}
}

func TestManuscriptContextDiagnosticsRejectSensitiveIdentityAndUnclassifiedBudgetOverflow(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	base := ManuscriptContextDiagnostic{Task: "writer_segment", InputBytes: 20, InputLimitBytes: 10, ContentSignature: domain.ContentSignature([]byte("payload")), Status: "completed"}
	if err := st.AppendManuscriptContextDiagnostic(base); err == nil {
		t.Fatal("over-budget completed diagnostic was accepted")
	}
	base.Status = "rejected_budget"
	base.ChapterID = `C:\private\novel\hero-name`
	if err := st.AppendManuscriptContextDiagnostic(base); err == nil {
		t.Fatal("path-like diagnostic identity was accepted")
	}
}
