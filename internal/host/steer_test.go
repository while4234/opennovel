package host

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/store"
)

func TestSteerReturnsPendingSteerPersistenceError(t *testing.T) {
	tests := []struct {
		name  string
		block func(*testing.T, string)
	}{
		{
			name: "normal flow lease acquisition",
			block: func(t *testing.T, dir string) {
				if err := os.RemoveAll(filepath.Join(dir, "meta")); err != nil {
					t.Fatalf("remove meta dir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "meta"), []byte("not a directory"), 0o644); err != nil {
					t.Fatalf("write meta blocker: %v", err)
				}
			},
		},
		{
			name: "pending steer write",
			block: func(t *testing.T, dir string) {
				runMetaPath := filepath.Join(dir, "meta", "run.json")
				if err := os.Remove(runMetaPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("remove run meta: %v", err)
				}
				if err := os.Mkdir(runMetaPath, 0o755); err != nil {
					t.Fatalf("create run meta blocker: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			st := store.NewStore(dir)
			if err := st.Init(); err != nil {
				t.Fatalf("Init: %v", err)
			}
			test.block(t, dir)

			h := &Host{
				store:     st,
				events:    make(chan Event, 4),
				lifecycle: lifecycleIdle,
			}
			err := h.Steer("make the protagonist more cautious")
			if err == nil {
				t.Fatal("Steer returned nil error, want persistence failure")
			}
			if !strings.Contains(err.Error(), "set pending steer") {
				t.Fatalf("Steer error = %v, want pending steer context", err)
			}
			var pathErr *os.PathError
			if !errors.As(err, &pathErr) {
				t.Fatalf("Steer error = %v, want wrapped path error", err)
			}
			if !errors.Is(err, pathErr.Err) {
				t.Fatalf("Steer error = %v, want errors.Is match for %v", err, pathErr.Err)
			}
		})
	}
}
