package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestArchitectSaveFoundationRejectsCharacterOwnedSections(t *testing.T) {
	tool := NewArchitectSaveFoundationTool(store.NewStore(t.TempDir()))
	for _, section := range []string{"characters", "planned_relationships"} {
		args, err := json.Marshal(map[string]any{
			"type":    section,
			"content": []any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tool.Execute(context.Background(), args); !errors.Is(err, errs.ErrToolPrecondition) {
			t.Fatalf("Architect write %q error = %v", section, err)
		}
	}
}
