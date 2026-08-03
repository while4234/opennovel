package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type formalDeleteCase struct {
	name   string
	rel    string
	delete func(*Store) error
}

func formalDeleteCases() []formalDeleteCase {
	return []formalDeleteCase{
		{name: "chapter artifacts", rel: "drafts/01.draft.md", delete: func(s *Store) error { return s.Drafts.DeleteChapterArtifacts(1) }},
		{name: "chapter summary", rel: "summaries/01.json", delete: func(s *Store) error { return s.Summaries.DeleteChapterSummary(1) }},
		{name: "arc summary", rel: "summaries/arc-v01a01.json", delete: func(s *Store) error { return s.Summaries.DeleteArcSummary(1, 1) }},
		{name: "volume summary", rel: "summaries/vol-v01.json", delete: func(s *Store) error { return s.Summaries.DeleteVolumeSummary(1) }},
		{name: "review", rel: "reviews/01.json", delete: func(s *Store) error { return s.World.DeleteReview(1) }},
	}
}

func TestFormalDeletesRejectActiveRevisionWithoutChangingFiles(t *testing.T) {
	for _, tc := range formalDeleteCases() {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			st := NewStore(dir)
			seedFormalDeleteTarget(t, dir, tc.rel)
			if _, err := st.Revisions.Start(fakeRevisionPolicy{}, StartRevisionInput{
				Intent: "hold formal state", Impact: mustRevisionImpact(t), IdempotencyKey: "active-delete-gate",
			}); err != nil {
				t.Fatal(err)
			}

			if err := tc.delete(st); !errors.Is(err, ErrActiveRevisionBlocksNormalFlow) {
				t.Fatalf("delete error=%v, want active revision rejection", err)
			}
			if data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(tc.rel))); err != nil || string(data) != "sentinel" {
				t.Fatalf("rejected delete changed %s: data=%q err=%v", tc.rel, data, err)
			}
		})
	}
}

func TestFormalDeletesWaitForConcurrentRevisionTransaction(t *testing.T) {
	for _, tc := range formalDeleteCases() {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			st := NewStore(dir)
			seedFormalDeleteTarget(t, dir, tc.rel)
			held := make(chan struct{})
			release := make(chan struct{})
			ownerDone := make(chan error, 1)
			go func() {
				ownerDone <- st.Revisions.withRevisionTransaction(func() error {
					close(held)
					<-release
					return nil
				})
			}()
			<-held

			deleteDone := make(chan error, 1)
			go func() { deleteDone <- tc.delete(st) }()
			select {
			case err := <-deleteDone:
				t.Fatalf("delete bypassed live revision transaction: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			if err := <-ownerDone; err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-deleteDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("delete did not resume after revision transaction released")
			}
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(tc.rel))); !os.IsNotExist(err) {
				t.Fatalf("delete did not remove %s: %v", tc.rel, err)
			}
		})
	}
}

func TestFormalDeleteWaitsForCrossProcessRevisionTransaction(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	target := "summaries/01.json"
	seedFormalDeleteTarget(t, dir, target)
	signals := t.TempDir()
	heldPath := filepath.Join(signals, "held")
	releasePath := filepath.Join(signals, "release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestFormalDeleteRevisionTransactionProcessHelper$")
	var childOutput bytes.Buffer
	cmd.Stdout, cmd.Stderr = &childOutput, &childOutput
	cmd.Env = append(os.Environ(),
		"AINOVEL_TEST_DELETE_ROOT="+dir,
		"AINOVEL_TEST_DELETE_HELD="+heldPath,
		"AINOVEL_TEST_DELETE_RELEASE="+releasePath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	waitForDeleteTestSignal(t, heldPath, "cross-process revision owner")

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- st.Summaries.DeleteChapterSummary(1) }()
	select {
	case err := <-deleteDone:
		t.Fatalf("delete bypassed cross-process revision transaction: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cross-process revision owner failed: %v\n%s", err, childOutput.String())
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete did not resume after cross-process revision transaction released")
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(target))); !os.IsNotExist(err) {
		t.Fatalf("delete did not remove %s: %v", target, err)
	}
}

func TestFormalDeleteRevisionTransactionProcessHelper(t *testing.T) {
	root := os.Getenv("AINOVEL_TEST_DELETE_ROOT")
	if root == "" {
		t.Skip("helper process only")
	}
	heldPath := os.Getenv("AINOVEL_TEST_DELETE_HELD")
	releasePath := os.Getenv("AINOVEL_TEST_DELETE_RELEASE")
	if err := NewRevisionStore(root).withRevisionTransaction(func() error {
		if err := os.WriteFile(heldPath, []byte("held"), 0o600); err != nil {
			return err
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(releasePath); err == nil {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timed out waiting for delete test release")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}); err != nil {
		t.Fatal(err)
	}
}

func seedFormalDeleteTarget(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForDeleteTestSignal(t *testing.T, path, owner string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not acquire the revision transaction", owner)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
