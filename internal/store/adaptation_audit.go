package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
)

const (
	adaptationAuditReportFile       = adaptationRootDir + "/audits/latest.json"
	adaptationAuditRunsDir          = adaptationRootDir + "/audits/runs"
	adaptationAuditIndexFile        = adaptationRootDir + "/audits/index.json"
	adaptationAuditProtectionsDir   = adaptationRootDir + "/audits/protections"
	adaptationRepairApplicationFile = adaptationRootDir + "/audits/latest_application.json"
	adaptationAuditRetention        = 50
)

func (s *AdaptationStore) SaveAuditReport(report adaptaudit.Report) error {
	if err := adaptaudit.ValidateReportDigest(report); err != nil {
		return err
	}
	return s.withLegacyFormalMutation("save adaptation audit", func() error { return s.io.WriteJSON(adaptationAuditReportFile, report) })
}

func (s *AdaptationStore) LoadAuditReport() (*adaptaudit.Report, error) {
	var report adaptaudit.Report
	if err := s.io.ReadJSON(adaptationAuditReportFile, &report); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := adaptaudit.ValidateReportDigest(report); err != nil {
		return nil, err
	}
	if err := s.ensureLegacyAuditRun(report); err != nil {
		return nil, err
	}
	return &report, nil
}

func (s *AdaptationStore) SaveAuditRun(run adaptaudit.AuditRun) error {
	if err := adaptaudit.ValidateAuditRun(run); err != nil {
		return err
	}
	return s.withLegacyFormalMutation("save adaptation audit run", func() error { return s.saveAuditRun(run) })
}

func (s *AdaptationStore) saveAuditRun(run adaptaudit.AuditRun) error {
	return s.io.WithWriteLock(func() error {
		path := auditRunFile(run.RunID)
		encoded, err := json.MarshalIndent(run, "", "  ")
		if err != nil {
			return err
		}
		if current, err := s.io.ReadFileUnlocked(path); err == nil {
			if bytes.Equal(bytes.TrimSpace(current), bytes.TrimSpace(encoded)) {
				// The immutable run may outlive a missing/corrupt index. Continue so
				// the index and latest alias can be repaired.
			} else {
				return fmt.Errorf("audit run %s is immutable", run.RunID)
			}
		} else if !os.IsNotExist(err) {
			return err
		} else if err := s.io.WriteFileUnlocked(path, encoded); err != nil {
			return err
		}
		index, err := s.loadAuditRunIndexUnlocked()
		if err != nil {
			return err
		}
		entry := auditRunIndexEntry(run)
		found := false
		for _, current := range index.Runs {
			if current.RunID == run.RunID {
				found = true
				break
			}
		}
		if !found {
			index.Runs = append([]adaptaudit.AuditRunIndexEntry{entry}, index.Runs...)
		}
		removed := pruneAuditRunIndex(&index)
		if err := s.io.WriteJSONUnlocked(adaptationAuditIndexFile, index); err != nil {
			return err
		}
		for _, runID := range removed {
			_ = s.io.RemoveFileUnlocked(auditRunFile(runID))
			_ = s.io.RemoveFileUnlocked(auditRunProtectionFile(runID))
		}
		return s.io.WriteJSONUnlocked(adaptationAuditReportFile, run.Report)
	})
}

func (s *AdaptationStore) LoadAuditRun(runID string) (*adaptaudit.AuditRun, error) {
	if !validAuditRunID(runID) {
		return nil, fmt.Errorf("invalid audit run id")
	}
	var run adaptaudit.AuditRun
	if err := s.io.ReadJSON(auditRunFile(runID), &run); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := adaptaudit.ValidateAuditRun(run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *AdaptationStore) ListAuditRuns() ([]adaptaudit.AuditRunIndexEntry, error) {
	if report, err := s.loadAuditReportWithoutMigration(); err != nil {
		return nil, err
	} else if report != nil {
		if err := s.ensureLegacyAuditRun(*report); err != nil {
			return nil, err
		}
	}
	index, err := s.loadAuditRunIndex()
	if err != nil {
		return nil, err
	}
	return append([]adaptaudit.AuditRunIndexEntry(nil), index.Runs...), nil
}

func (s *AdaptationStore) LatestAuditRun() (*adaptaudit.AuditRun, error) {
	entries, err := s.ListAuditRuns()
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	return s.LoadAuditRun(entries[0].RunID)
}

func (s *AdaptationStore) ProtectAuditRun(runID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("audit run protection reason is required")
	}
	return s.withLegacyFormalMutation("protect audit run", func() error { return s.protectAuditRun(runID, reason) })
}

func (s *AdaptationStore) protectAuditRun(runID, reason string) error {
	return s.io.WithWriteLock(func() error {
		protection, err := s.loadAuditRunProtectionUnlocked(runID)
		if err != nil {
			return err
		}
		if !slices.Contains(protection.Reasons, reason) {
			protection.Reasons = append(protection.Reasons, reason)
			sort.Strings(protection.Reasons)
		}
		if err := s.io.WriteJSONUnlocked(auditRunProtectionFile(runID), protection); err != nil {
			return err
		}
		index, err := s.loadAuditRunIndexUnlocked()
		if err != nil {
			return err
		}
		for i := range index.Runs {
			if index.Runs[i].RunID != runID {
				continue
			}
			index.Runs[i].ProtectedReasons = append([]string(nil), protection.Reasons...)
			index.Runs[i].AppliedAt = protection.AppliedAt
			return s.io.WriteJSONUnlocked(adaptationAuditIndexFile, index)
		}
		return fmt.Errorf("audit run %s not found", runID)
	})
}

func (s *AdaptationStore) MarkAuditRunApplied(runID, appliedAt string) error {
	if !validAuditRunID(runID) || strings.TrimSpace(appliedAt) == "" {
		return fmt.Errorf("audit run id and applied_at are required")
	}
	return s.withLegacyFormalMutation("mark audit run applied", func() error {
		return s.io.WithWriteLock(func() error {
			protection, err := s.loadAuditRunProtectionUnlocked(runID)
			if err != nil {
				return err
			}
			if !slices.Contains(protection.Reasons, "repair") {
				protection.Reasons = append(protection.Reasons, "repair")
				sort.Strings(protection.Reasons)
			}
			protection.AppliedAt = appliedAt
			if err := s.io.WriteJSONUnlocked(auditRunProtectionFile(runID), protection); err != nil {
				return err
			}
			index, err := s.loadAuditRunIndexUnlocked()
			if err != nil {
				return err
			}
			for i := range index.Runs {
				if index.Runs[i].RunID != runID {
					continue
				}
				index.Runs[i].ProtectedReasons = append([]string(nil), protection.Reasons...)
				index.Runs[i].AppliedAt = appliedAt
				return s.io.WriteJSONUnlocked(adaptationAuditIndexFile, index)
			}
			return fmt.Errorf("audit run %s not found", runID)
		})
	})
}

func (s *AdaptationStore) CompareAuditRuns(baseRunID, candidateRunID string) (*adaptaudit.AuditComparison, error) {
	base, err := s.LoadAuditRun(baseRunID)
	if err != nil {
		return nil, err
	}
	if base == nil {
		return nil, fmt.Errorf("audit run %s not found", baseRunID)
	}
	candidate, err := s.LoadAuditRun(candidateRunID)
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, fmt.Errorf("audit run %s not found", candidateRunID)
	}
	comparison, err := adaptaudit.CompareAuditRuns(*base, *candidate)
	if err != nil {
		return nil, err
	}
	return &comparison, nil
}

func (s *AdaptationStore) SaveRepairApplication(application adaptaudit.RepairApplication) error {
	return s.withLegacyFormalMutation("save adaptation audit repair", func() error { return s.io.WriteJSON(adaptationRepairApplicationFile, application) })
}
func (s *AdaptationStore) LoadRepairApplication() (*adaptaudit.RepairApplication, error) {
	var application adaptaudit.RepairApplication
	if err := s.io.ReadJSON(adaptationRepairApplicationFile, &application); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &application, nil
}

func (s *AdaptationStore) ensureLegacyAuditRun(report adaptaudit.Report) error {
	var existing adaptaudit.AuditRunIndex
	if err := s.io.ReadJSON(adaptationAuditIndexFile, &existing); err == nil {
		for _, entry := range existing.Runs {
			if entry.ReportDigest == report.Digest {
				return nil
			}
		}
	}
	return s.withLegacyFormalMutation("migrate legacy adaptation audit", func() error {
		entries, err := s.loadAuditRunIndexWithinLegacyMutation()
		if err != nil {
			return err
		}
		for _, entry := range entries.Runs {
			if entry.ReportDigest == report.Digest {
				return nil
			}
		}
		runID := "legacy-" + report.Digest[:16]
		run := adaptaudit.AuditRun{
			RunID: runID, Kind: adaptaudit.AuditKindContract, Trigger: adaptaudit.AuditTriggerLegacy,
			StartedAt: time.Unix(0, 0).UTC().Format(time.RFC3339), CompletedAt: time.Unix(0, 0).UTC().Format(time.RFC3339),
			Status: report.Status, Scope: report.Scope, InputDigest: report.InputDigest, ReportDigest: report.Digest,
			EngineVersion: "legacy", Report: report,
		}
		if err := s.saveAuditRun(run); err != nil {
			return err
		}
		return s.protectAuditRun(runID, "legacy")
	})
}

func (s *AdaptationStore) loadAuditRunIndexWithinLegacyMutation() (adaptaudit.AuditRunIndex, error) {
	var index adaptaudit.AuditRunIndex
	if err := s.io.ReadJSON(adaptationAuditIndexFile, &index); err == nil {
		return index, nil
	}
	err := s.io.WithWriteLock(func() error {
		var rebuildErr error
		index, rebuildErr = s.rebuildAuditRunIndexUnlocked()
		if rebuildErr != nil {
			return rebuildErr
		}
		return s.io.WriteJSONUnlocked(adaptationAuditIndexFile, index)
	})
	return index, err
}

func (s *AdaptationStore) loadAuditReportWithoutMigration() (*adaptaudit.Report, error) {
	var report adaptaudit.Report
	if err := s.io.ReadJSON(adaptationAuditReportFile, &report); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := adaptaudit.ValidateReportDigest(report); err != nil {
		return nil, err
	}
	return &report, nil
}
func (s *AdaptationStore) loadAuditRunIndex() (adaptaudit.AuditRunIndex, error) {
	var index adaptaudit.AuditRunIndex
	if err := s.io.ReadJSON(adaptationAuditIndexFile, &index); err != nil {
		return s.rebuildAuditRunIndex()
	}
	return index, nil
}
func (s *AdaptationStore) loadAuditRunIndexUnlocked() (adaptaudit.AuditRunIndex, error) {
	var index adaptaudit.AuditRunIndex
	if err := s.io.ReadJSONUnlocked(adaptationAuditIndexFile, &index); err != nil {
		return s.rebuildAuditRunIndexUnlocked()
	}
	return index, nil
}

func (s *AdaptationStore) rebuildAuditRunIndex() (adaptaudit.AuditRunIndex, error) {
	var rebuilt adaptaudit.AuditRunIndex
	err := s.withLegacyFormalMutation("rebuild adaptation audit index", func() error {
		s.io.mu.Lock()
		defer s.io.mu.Unlock()
		var err error
		rebuilt, err = s.rebuildAuditRunIndexUnlocked()
		if err != nil {
			return err
		}
		return s.io.WriteJSONUnlocked(adaptationAuditIndexFile, rebuilt)
	})
	return rebuilt, err
}

func (s *AdaptationStore) rebuildAuditRunIndexUnlocked() (adaptaudit.AuditRunIndex, error) {
	index := adaptaudit.AuditRunIndex{Version: 1}
	entries, err := os.ReadDir(s.io.path(adaptationAuditRunsDir))
	if err != nil {
		if os.IsNotExist(err) {
			return index, nil
		}
		return index, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var run adaptaudit.AuditRun
		if err := s.io.ReadJSONUnlocked(adaptationAuditRunsDir+"/"+entry.Name(), &run); err != nil {
			continue
		}
		if err := adaptaudit.ValidateAuditRun(run); err != nil {
			continue
		}
		item := auditRunIndexEntry(run)
		protection, protectionErr := s.loadAuditRunProtectionUnlocked(run.RunID)
		if protectionErr == nil {
			item.ProtectedReasons = append([]string(nil), protection.Reasons...)
			item.AppliedAt = protection.AppliedAt
		}
		index.Runs = append(index.Runs, item)
	}
	sort.SliceStable(index.Runs, func(i, j int) bool {
		if index.Runs[i].CompletedAt != index.Runs[j].CompletedAt {
			return index.Runs[i].CompletedAt > index.Runs[j].CompletedAt
		}
		return index.Runs[i].RunID > index.Runs[j].RunID
	})
	return index, nil
}
func auditRunIndexEntry(run adaptaudit.AuditRun) adaptaudit.AuditRunIndexEntry {
	return adaptaudit.AuditRunIndexEntry{RunID: run.RunID, Kind: run.Kind, Trigger: run.Trigger, CompletedAt: run.CompletedAt, Status: run.Status, Scope: run.Scope, InputDigest: run.InputDigest, ReportDigest: run.ReportDigest, Model: run.Model, Usage: run.Usage}
}
func pruneAuditRunIndex(index *adaptaudit.AuditRunIndex) []string {
	if len(index.Runs) <= adaptationAuditRetention {
		return nil
	}
	kept := make([]adaptaudit.AuditRunIndexEntry, 0, len(index.Runs))
	removed := []string{}
	unprotected := 0
	for _, entry := range index.Runs {
		if len(entry.ProtectedReasons) == 0 {
			unprotected++
		}
		if len(entry.ProtectedReasons) > 0 || unprotected <= adaptationAuditRetention {
			kept = append(kept, entry)
		} else {
			removed = append(removed, entry.RunID)
		}
	}
	index.Runs = kept
	return removed
}
func auditRunFile(runID string) string { return adaptationAuditRunsDir + "/" + runID + ".json" }

type auditRunProtection struct {
	RunID     string   `json:"run_id"`
	Reasons   []string `json:"reasons"`
	AppliedAt string   `json:"applied_at,omitempty"`
}

func auditRunProtectionFile(runID string) string {
	return adaptationAuditProtectionsDir + "/" + runID + ".json"
}
func (s *AdaptationStore) loadAuditRunProtectionUnlocked(runID string) (auditRunProtection, error) {
	protection := auditRunProtection{RunID: runID}
	if err := s.io.ReadJSONUnlocked(auditRunProtectionFile(runID), &protection); err != nil {
		if os.IsNotExist(err) {
			return protection, nil
		}
		return protection, err
	}
	return protection, nil
}
func validAuditRunID(runID string) bool {
	return strings.TrimSpace(runID) != "" && !strings.ContainsAny(runID, `/\\`) && runID != "." && runID != ".."
}
