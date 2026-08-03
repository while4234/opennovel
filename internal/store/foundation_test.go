package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestFoundationLegacyLoadIsReadOnlyAndFirstSaveMigrates(t *testing.T) {
	dir := t.TempDir()
	io := newIO(dir)
	if err := io.WriteMarkdown("premise.md", "Legacy premise"); err != nil {
		t.Fatal(err)
	}
	if err := io.WriteJSON("characters.json", []domain.Character{{Name: "Lin", Role: "hero"}}); err != nil {
		t.Fatal(err)
	}
	if err := io.WriteJSON("world_rules.json", []domain.WorldRule{{Category: "magic", Rule: "Memory is the price"}}); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dir)
	loaded, err := store.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 0 || len(loaded.Relationships) != 0 || loaded.RelationshipsReviewed {
		t.Fatalf("legacy aggregate = %+v", loaded)
	}
	if _, err := os.Stat(filepath.Join(dir, foundationCanonicalFile)); !os.IsNotExist(err) {
		t.Fatalf("read-only legacy load migrated canonical: %v", err)
	}
	if _, err := store.Foundation.SaveCAS(loaded, 0); err != nil {
		t.Fatal(err)
	}
	migrated, err := store.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Revision != 1 {
		t.Fatalf("migration revision = %d", migrated.Revision)
	}
	for _, rel := range append([]string{foundationCanonicalFile, foundationManifestFile}, foundationProjectionPaths()...) {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("migration did not write %s: %v", rel, err)
		}
	}
	again, err := store.Foundation.SaveCAS(migrated, migrated.Revision)
	if err != nil || again.Revision != migrated.Revision {
		t.Fatalf("idempotent save = revision %d, err %v", again.Revision, err)
	}
}

func TestFoundationSectionUpdatesAreIsolatedAndCASProtected(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Outline.SavePremise("premise one"); err != nil {
		t.Fatal(err)
	}
	if err := store.Characters.Save([]domain.Character{{Name: "Lin"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.World.SaveWorldRules([]domain.WorldRule{{Rule: "No reset"}}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Outline.SavePremise("premise two"); err != nil {
		t.Fatal(err)
	}
	after, err := store.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Characters) != 1 || len(after.WorldRules) != 1 || after.Premise != "premise two" {
		t.Fatalf("section update overwrote another section: %+v", after)
	}
	if after.Revision != before.Revision+1 {
		t.Fatalf("section revision = %d, want %d", after.Revision, before.Revision+1)
	}
	stale := domain.CloneStoryFoundation(before)
	stale.Premise = "stale"
	if _, err := store.Foundation.SaveCAS(stale, before.Revision); err == nil {
		t.Fatal("stale CAS was accepted")
	} else {
		var conflict *FoundationConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("CAS error = %T %v", err, err)
		}
	}
}

func TestFoundationReviewMarkerDoesNotIncreaseSemanticRevision(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Characters.Save([]domain.Character{{ID: "lin", Name: "Lin"}, {ID: "mara", Name: "Mara"}}); err != nil {
		t.Fatal(err)
	}
	relationships := []domain.CharacterRelationship{{
		ID: "bond", SourceCharacterID: "lin", TargetCharacterID: "mara", Type: domain.RelationshipTypeAlly,
	}}
	if err := store.Foundation.updateRelationships(relationships, false); err != nil {
		t.Fatal(err)
	}
	before, err := store.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Foundation.updateRelationships(relationships, true); err != nil {
		t.Fatal(err)
	}
	after, err := store.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || !after.RelationshipsReviewed {
		t.Fatalf("review-only update changed revision or was not saved: before=%+v after=%+v", before, after)
	}
}

func TestFoundationV1DirectionMigrationIsReadOnlyUntilSaveAndKeepsRevision(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	saved, err := store.Foundation.SaveCAS(domain.StoryFoundation{
		Characters: []domain.Character{{ID: "lin", Name: "Lin"}, {ID: "mara", Name: "Mara"}},
		Relationships: []domain.CharacterRelationship{{
			ID: "bond", SourceCharacterID: "lin", TargetCharacterID: "mara", Type: domain.RelationshipTypeAlly,
			Direction: domain.RelationshipDirectionBidirectional,
		}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var legacy domain.StoryFoundation
	if err := newIO(dir).ReadJSON(foundationCanonicalFile, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.SchemaVersion = 1
	legacy.Relationships[0].Direction = domain.RelationshipDirectionMutual
	canonical, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	relationships, err := json.MarshalIndent(legacy.Relationships, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, foundationCanonicalFile), canonical, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "planned_relationships.json"), relationships, 0o644); err != nil {
		t.Fatal(err)
	}
	var manifest foundationProjectionManifest
	if err := newIO(dir).ReadJSON(foundationManifestFile, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Files[foundationCanonicalFile] = fileDigest(canonical)
	manifest.Files["planned_relationships.json"] = fileDigest(relationships)
	if err := newIO(dir).WriteJSON(foundationManifestFile, manifest); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, foundationCanonicalFile))
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != domain.StoryFoundationSchemaVersion || loaded.Relationships[0].Direction != domain.RelationshipDirectionBidirectional {
		t.Fatalf("loaded migration = %+v", loaded)
	}
	afterLoad, err := os.ReadFile(filepath.Join(dir, foundationCanonicalFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, afterLoad) {
		t.Fatal("read-only load wrote the schema migration")
	}
	migrated, err := store.Foundation.SaveCAS(loaded, loaded.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Revision != saved.Revision {
		t.Fatalf("representation-only migration changed revision: %d -> %d", saved.Revision, migrated.Revision)
	}
	var persisted domain.StoryFoundation
	if err := newIO(dir).ReadJSON(foundationCanonicalFile, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.SchemaVersion != domain.StoryFoundationSchemaVersion || persisted.Relationships[0].Direction != domain.RelationshipDirectionBidirectional {
		t.Fatalf("persisted migration = %+v", persisted)
	}
}

func TestFoundationRecoveryRollsForwardEveryDurableFailurePoint(t *testing.T) {
	for _, failure := range []string{
		foundationFailAfterJournal,
		foundationFailAfterCanonical,
		foundationFailAfterProjection,
		foundationFailAfterManifest,
	} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			store := NewStore(dir)
			store.Foundation.failpoint = func(stage string) error {
				if stage == failure {
					return errors.New("crash")
				}
				return nil
			}
			candidate := domain.StoryFoundation{Premise: "recover me", Characters: []domain.Character{{Name: "Lin"}}}
			if _, err := store.Foundation.SaveCAS(candidate, 0); err == nil {
				t.Fatal("fault injection did not interrupt save")
			}
			reopened := NewStore(dir)
			loaded, err := reopened.Foundation.Load()
			if err != nil {
				t.Fatalf("recovery failed: %v", err)
			}
			if loaded.Premise != "recover me" || loaded.Revision != 1 {
				t.Fatalf("recovered foundation = %+v", loaded)
			}
			if pending, err := reopened.Foundation.PendingTransaction(); err != nil || pending {
				t.Fatalf("pending after recovery = %v, %v", pending, err)
			}
		})
	}
}

func TestFoundationRecoveryAcceptsBaseAndCandidateCanonicalStates(t *testing.T) {
	for _, test := range []struct {
		name      string
		failpoint string
	}{
		{name: "base canonical", failpoint: foundationFailAfterJournal},
		{name: "candidate canonical", failpoint: foundationFailAfterCanonical},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			store := NewStore(dir)
			base, err := store.Foundation.SaveCAS(domain.StoryFoundation{Premise: "base", Characters: []domain.Character{{Name: "Lin"}}}, 0)
			if err != nil {
				t.Fatal(err)
			}
			store.Foundation.failpoint = func(stage string) error {
				if stage == test.failpoint {
					return errors.New("crash")
				}
				return nil
			}
			candidate := domain.CloneStoryFoundation(base)
			candidate.Premise = "candidate"
			if _, err := store.Foundation.SaveCAS(candidate, base.Revision); err == nil {
				t.Fatal("fault injection did not interrupt save")
			}

			recovered := newFoundationStore(newIO(dir))
			if err := recovered.Recover(); err != nil {
				t.Fatalf("recover %s: %v", test.name, err)
			}
			loaded, err := recovered.Load()
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Premise != "candidate" || loaded.Revision != base.Revision+1 {
				t.Fatalf("recovered foundation = %+v", loaded)
			}
		})
	}
}

func TestFoundationRecoveryRejectsStaleJournalWithoutChangingBytes(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	base, err := store.Foundation.SaveCAS(domain.StoryFoundation{Premise: "base", Characters: []domain.Character{{Name: "Lin"}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	store.Foundation.failpoint = func(stage string) error {
		if stage == foundationFailAfterJournal {
			return errors.New("crash")
		}
		return nil
	}
	candidate := domain.CloneStoryFoundation(base)
	candidate.Premise = "stale candidate"
	if _, err := store.Foundation.SaveCAS(candidate, base.Revision); err == nil {
		t.Fatal("fault injection did not interrupt save")
	}

	newer := domain.CloneStoryFoundation(base)
	newer.Premise = "newer canonical"
	newer.Revision = base.Revision + 2
	newer.UpdatedAt = "2030-01-02T03:04:05Z"
	files, _, err := foundationTransactionPayloads(newer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, foundationCanonicalFile), files[foundationCanonicalFile], 0o644); err != nil {
		t.Fatal(err)
	}
	before := foundationTestFileBytes(t, dir)

	recovered := newFoundationStore(newIO(dir))
	recoveryErr := recovered.Recover()
	if recoveryErr == nil || !strings.Contains(recoveryErr.Error(), "journal conflict") {
		t.Fatalf("stale journal recovery error = %v", recoveryErr)
	}
	for _, secret := range []string{"stale candidate", "newer canonical"} {
		if strings.Contains(recoveryErr.Error(), secret) {
			t.Fatalf("stale journal recovery leaked foundation content %q: %v", secret, recoveryErr)
		}
	}
	after := foundationTestFileBytes(t, dir)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("stale journal recovery changed files\nbefore=%v\nafter=%v", foundationTestFileDigests(before), foundationTestFileDigests(after))
	}
}

func TestFoundationFailureBeforeJournalDoesNotMigrate(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	store.Foundation.failpoint = func(stage string) error {
		if stage == foundationFailAfterStage {
			return errors.New("crash")
		}
		return nil
	}
	if _, err := store.Foundation.SaveCAS(domain.StoryFoundation{Premise: "not committed"}, 0); err == nil {
		t.Fatal("fault injection did not interrupt save")
	}
	reopened := NewStore(dir)
	loaded, err := reopened.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 0 || loaded.Premise != "" {
		t.Fatalf("pre-journal stage became visible: %+v", loaded)
	}
}

func TestFoundationMixedProjectionFailsClosedAndCanBeRepairedWithoutRevision(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	saved, err := store.Foundation.SaveCAS(domain.StoryFoundation{Premise: "canonical", Characters: []domain.Character{{Name: "Lin"}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "premise.md"), []byte("mixed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Foundation.Load(); err == nil {
		t.Fatal("mixed canonical and projection were returned")
	}
	repaired, err := store.Foundation.SaveCAS(saved, saved.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Revision != saved.Revision {
		t.Fatalf("projection repair changed semantic revision: %d -> %d", saved.Revision, repaired.Revision)
	}
	if _, err := store.Foundation.Load(); err != nil {
		t.Fatalf("repaired projection did not validate: %v", err)
	}
}

func TestFoundationConcurrentSectionUpdatesPreserveBothSections(t *testing.T) {
	store := NewStore(t.TempDir())
	var wait sync.WaitGroup
	wait.Add(2)
	errs := make(chan error, 2)
	go func() {
		defer wait.Done()
		errs <- store.Outline.SavePremise("concurrent premise")
	}()
	go func() {
		defer wait.Done()
		errs <- store.Characters.Save([]domain.Character{{Name: "Lin"}})
	}()
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Premise != "concurrent premise" || len(loaded.Characters) != 1 {
		t.Fatalf("concurrent update lost a section: %+v", loaded)
	}
}

func TestPlannedRelationshipsDoNotTouchRuntimeOrAdaptationSource(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Characters.Save([]domain.Character{{ID: "lin", Name: "Lin"}, {ID: "mara", Name: "Mara"}}); err != nil {
		t.Fatal(err)
	}
	runtime := []domain.RelationshipEntry{{CharacterA: "Lin", CharacterB: "Mara", Relation: "met", Chapter: 2}}
	if err := store.World.SaveRelationships(runtime); err != nil {
		t.Fatal(err)
	}
	source := domain.AdaptationSourceFoundation{Premise: "source evidence", Characters: []domain.Character{}, WorldRules: []domain.WorldRule{}}
	if err := store.Adaptation.SaveSourceFoundation(source); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(store.dir, filepath.FromSlash(adaptationSourceFoundationFile))
	sourceBytesBefore, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceSignatureBefore := fileDigest(sourceBytesBefore)
	planned := []domain.CharacterRelationship{{SourceCharacterID: "lin", TargetCharacterID: "mara", Type: domain.RelationshipTypeAlly}}
	if err := store.Foundation.updateRelationships(planned, false); err != nil {
		t.Fatal(err)
	}
	gotRuntime, err := store.World.LoadRelationships()
	if err != nil || len(gotRuntime) != 1 || gotRuntime[0].Chapter != 2 {
		t.Fatalf("runtime relationship changed: %+v, %v", gotRuntime, err)
	}
	gotSource, err := store.Adaptation.LoadSourceFoundation()
	if err != nil || gotSource == nil || gotSource.Premise != source.Premise {
		t.Fatalf("adaptation source foundation changed: %+v, %v", gotSource, err)
	}
	sourceBytesAfter, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sourceBytesAfter, sourceBytesBefore) || fileDigest(sourceBytesAfter) != sourceSignatureBefore {
		t.Fatal("planned relationships changed adaptation source foundation bytes or signature")
	}
}

func foundationTestFileBytes(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)], err = os.ReadFile(path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func foundationTestFileDigests(files map[string][]byte) map[string]string {
	digests := make(map[string]string, len(files))
	for rel, data := range files {
		digests[rel] = fileDigest(data)
	}
	return digests
}
