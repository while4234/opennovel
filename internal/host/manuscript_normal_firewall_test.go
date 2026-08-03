package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestNormalManuscriptFirewallRejectsForbiddenKeysRecursively(t *testing.T) {
	tests := []string{
		`{"SOURCE":{"id":"source-1"}}`,
		`{"outer":{"Source_Metadata":"copied"}}`,
		`{"outer":[{"source-id":"copied"}]}`,
		`{"outer":[{"ADAPTATION":{"status":"claimed"}}]}`,
		`{"outer":{"Adaptation_Result":"claimed"}}`,
		`{"outer":{"adaptation-proof":"claimed"}}`,
	}
	for _, payload := range tests {
		if err := validateNormalManuscriptJSON([]byte(payload)); err == nil {
			t.Fatalf("forbidden payload accepted: %s", payload)
		}
	}
}

func TestNormalManuscriptFirewallAllowsSimilarBusinessKeys(t *testing.T) {
	payload := []byte(`{"resource":{"sourcebook":"catalog"},"adaptability":"high","adaptationist":"editor","sourcing":"local","relationship":{"source_character_id":"lin","target_character_id":"su"}}`)
	if err := validateNormalManuscriptJSON(payload); err != nil {
		t.Fatalf("legal similar fields rejected: %v", err)
	}
	if err := validateNormalManuscriptJSON([]byte(`{"source_character_ids":["source-lin"]}`)); err == nil {
		t.Fatal("adaptation source character collection was accepted on the normal path")
	}
}

func TestNormalManuscriptWriterEnvelopeAndSidecarSchemaFailClosed(t *testing.T) {
	model := &scriptedManuscriptModel{responses: []string{
		`{"chapter_id":"ch_expected","attempt":1,"segment":1,"prose":"candidate","complete":false,"truncated":false,"extra":"unknown"}`,
	}}
	writer := &modelManuscriptWriter{model: model, prompts: assets.Load("default").Prompts}
	_, err := writer.GenerateManuscriptSegment(t.Context(), domain.ManuscriptRevisionRuntime{Mode: domain.RevisionModeNormal}, domain.ManuscriptReworkItem{ChapterID: "ch_expected"}, ManuscriptGenerationContext{}, 1, 1, "")
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown normal envelope field err=%v", err)
	}

	sidecars := completeManuscriptSidecars()
	sidecars["editor_notes"] = json.RawMessage(`{"note":"not in the contract"}`)
	if err := validateNormalManuscriptSidecars(sidecars, true); err == nil || !strings.Contains(err.Error(), "unknown top-level sidecar") {
		t.Fatalf("unknown normal sidecar err=%v", err)
	}
}

func TestAdaptationManuscriptWriterKeepsSourceAndAdaptationFields(t *testing.T) {
	sidecars := completeManuscriptSidecars()
	sidecars["events"] = json.RawMessage(`[{"event":"retained","source_id":"source-1","adaptation-proof":"verified"}]`)
	response, err := json.Marshal(map[string]any{
		"chapter_id": "ch_expected",
		"attempt":    1,
		"segment":    1,
		"prose":      "candidate",
		"complete":   true,
		"truncated":  false,
		"sidecars":   sidecars,
		"source_context": map[string]any{
			"adaptation_id": "adapt-1",
		},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	model := &scriptedManuscriptModel{responses: []string{string(response)}}
	writer := &modelManuscriptWriter{model: model, prompts: assets.Load("default").Prompts}
	generated, err := writer.GenerateManuscriptSegment(t.Context(), domain.ManuscriptRevisionRuntime{Mode: domain.RevisionModeAdaptation}, domain.ManuscriptReworkItem{ChapterID: "ch_expected"}, ManuscriptGenerationContext{}, 1, 1, "")
	if err != nil {
		t.Fatalf("adaptation writer rejected required fields: %v", err)
	}
	if !strings.Contains(string(generated.Sidecars["events"]), "source_id") || !strings.Contains(string(generated.Sidecars["events"]), "adaptation-proof") {
		t.Fatalf("adaptation fields were not preserved: %s", generated.Sidecars["events"])
	}
}

func TestNormalSubmitCandidateRejectsBeforeCASOrRuntimeCandidateMutation(t *testing.T) {
	root := t.TempDir()
	st, chapterID := seedManuscriptRevisionProjectAt(t, root)
	service := NewManuscriptRevisionService(st)
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "polish"}, "recursive-firewall-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	beforeCAS := countRevisionCASFiles(t, root)
	sidecars := completeManuscriptSidecars()
	sidecars["events"] = json.RawMessage(`[{"event":"forged","context":[{"Source-Excerpt":"copied"}]}]`)
	_, err = service.SubmitCandidate(preview.Runtime.RevisionID, preview.Runtime.Revision, "recursive-firewall-candidate", ManuscriptCandidateInput{ChapterID: chapterID, Prose: "must not enter CAS", Sidecars: sidecars})
	if err == nil || !strings.Contains(err.Error(), "Source-Excerpt") {
		t.Fatalf("recursive source field err=%v", err)
	}
	assertNormalFirewallLeftNoCandidatePollution(t, st, preview.Runtime.RevisionID, preview.Runtime.Revision, beforeCAS, root)
}

func TestNormalGenerationRejectsBeforeReceiptContentOrCandidatePersistence(t *testing.T) {
	root := t.TempDir()
	st, chapterID := seedManuscriptRevisionProjectAt(t, root)
	service := NewManuscriptRevisionServiceWithRuntime(st, forbiddenSidecarManuscriptWriter{}, passingManuscriptAuditor{})
	preview, err := service.Preview(ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "rewrite"}, "generation-firewall-preview")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	beforeCAS := countRevisionCASFiles(t, root)
	failed, err := service.GenerateCandidates(t.Context(), preview.Runtime.RevisionID, preview.Runtime.Revision, 1, "generation-firewall")
	if err == nil || failed == nil || failed.LastErrorClass != "invalid_schema" {
		t.Fatalf("generation firewall runtime=%+v err=%v", failed, err)
	}
	if len(failed.Candidates) != 0 {
		t.Fatalf("forbidden generation persisted candidates: %+v", failed.Candidates)
	}
	for _, batch := range failed.Batches {
		if len(batch.Receipts) != 0 {
			t.Fatalf("forbidden generation persisted receipts: %+v", batch.Receipts)
		}
	}
	if afterCAS := countRevisionCASFiles(t, root); afterCAS != beforeCAS {
		t.Fatalf("forbidden generation polluted CAS: before=%d after=%d", beforeCAS, afterCAS)
	}
}

type forbiddenSidecarManuscriptWriter struct{}

func (forbiddenSidecarManuscriptWriter) PlanManuscriptRevision(_ context.Context, _ domain.ManuscriptBaseline, _ string, _ domain.ManuscriptInstructionKind) (ManuscriptPlan, error) {
	return ManuscriptPlan{}, nil
}

func (forbiddenSidecarManuscriptWriter) GenerateManuscriptSegment(_ context.Context, _ domain.ManuscriptRevisionRuntime, item domain.ManuscriptReworkItem, _ ManuscriptGenerationContext, attempt, segment int, _ string) (ManuscriptGeneratedSegment, error) {
	sidecars := completeManuscriptSidecars()
	sidecars["timeline"] = json.RawMessage(`[{"event":"forged","metadata":{"adaptation_claim":"copied"}}]`)
	return ManuscriptGeneratedSegment{ChapterID: item.ChapterID, Attempt: attempt, Segment: segment, Prose: "must not enter CAS", Sidecars: sidecars, Complete: true}, nil
}

func assertNormalFirewallLeftNoCandidatePollution(t *testing.T, st *storepkg.Store, revisionID string, expectedRevision, beforeCAS int, root string) {
	t.Helper()
	runtime, err := st.ManuscriptRevisions.Load(revisionID)
	if err != nil {
		t.Fatalf("Load runtime: %v", err)
	}
	if runtime.Revision != expectedRevision || len(runtime.Candidates) != 0 {
		t.Fatalf("normal firewall mutated runtime candidate state: %+v", runtime)
	}
	if afterCAS := countRevisionCASFiles(t, root); afterCAS != beforeCAS {
		t.Fatalf("normal firewall polluted CAS: before=%d after=%d", beforeCAS, afterCAS)
	}
}

func countRevisionCASFiles(t *testing.T, root string) int {
	t.Helper()
	count := 0
	casRoot := filepath.Join(root, "meta", "revisions", "content", "sha256")
	err := filepath.WalkDir(casRoot, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk revision CAS: %v", err)
	}
	return count
}
