package store

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

var adaptationConfirmationPaths = []string{
	adaptationProposalFile, adaptationVolumeReviewFile, adaptationProposalRuntimeFile,
	adaptationPlanFile, adaptationPlanningWorkflowFile, adaptationCheckDir,
	adaptationTargetFoundationReviewFile,
	structureRootDir,
	"meta/run.json", "meta/progress.json", checkpointsFile,
	foundationCanonicalFile, foundationRootDir,
	"premise.md", "characters.json", "characters.md", "world_rules.json", "world_rules.md",
	"planned_relationships.json", "planned_relationships.md",
	"outline.json", "outline.md", "layered_outline.json", "layered_outline.md", "meta/compass.json",
}

type adaptationConfirmationFile struct {
	data fs.FileMode
	body []byte
}

// WithAdaptationConfirmationTransaction restores the exact before-image when
// any confirmation persistence step fails. Normal-flow ownership serializes
// callers; crossMu prevents same-process compound store transactions crossing it.
func (s *Store) WithAdaptationConfirmationTransaction(fn func() error) error {
	if s == nil || fn == nil {
		return fmt.Errorf("adaptation confirmation transaction requires store and callback")
	}
	s.adaptationConfirmationMu.Lock()
	defer s.adaptationConfirmationMu.Unlock()
	s.crossMu.Lock()
	snapshot, err := s.captureAdaptationConfirmationBeforeImage()
	s.crossMu.Unlock()
	if err != nil {
		return err
	}
	if err := fn(); err != nil {
		s.crossMu.Lock()
		defer s.crossMu.Unlock()
		if rollbackErr := s.restoreAdaptationConfirmationBeforeImage(snapshot); rollbackErr != nil {
			return fmt.Errorf("adaptation confirmation failed: %v; rollback failed: %w", err, rollbackErr)
		}
		return err
	}
	return nil
}

func (s *Store) captureAdaptationConfirmationBeforeImage() (map[string]adaptationConfirmationFile, error) {
	files := make(map[string]adaptationConfirmationFile)
	for _, rel := range adaptationConfirmationPaths {
		root := filepath.Join(s.dir, filepath.FromSlash(rel))
		info, err := os.Stat(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("snapshot adaptation confirmation path %s: %w", rel, err)
		}
		if !info.IsDir() {
			body, readErr := os.ReadFile(root)
			if readErr != nil {
				return nil, readErr
			}
			files[filepath.ToSlash(rel)] = adaptationConfirmationFile{data: info.Mode().Perm(), body: body}
			continue
		}
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			fileInfo, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			fileRel, relErr := filepath.Rel(s.dir, path)
			if relErr != nil {
				return relErr
			}
			files[filepath.ToSlash(fileRel)] = adaptationConfirmationFile{data: fileInfo.Mode().Perm(), body: body}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("snapshot adaptation confirmation tree %s: %w", rel, err)
		}
	}
	return files, nil
}

func (s *Store) restoreAdaptationConfirmationBeforeImage(files map[string]adaptationConfirmationFile) error {
	for _, rel := range adaptationConfirmationPaths {
		if err := os.RemoveAll(filepath.Join(s.dir, filepath.FromSlash(rel))); err != nil {
			return err
		}
	}
	for rel, file := range files {
		path := filepath.Join(s.dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, file.body, file.data); err != nil {
			return err
		}
	}
	if s.Checkpoints != nil {
		s.Checkpoints = newCheckpointStore(s.Checkpoints.io, true)
	}
	if s.Foundation != nil {
		if err := s.Foundation.Recover(); err != nil {
			return err
		}
	}
	return nil
}
