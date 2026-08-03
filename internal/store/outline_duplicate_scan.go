package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	outlineDuplicateScanFile    = "meta/outline_duplicate_scan.json"
	outlineDuplicateScanVersion = 2
	outlineDuplicateScanStatus  = "clean"
)

type outlineDuplicateScanMarker struct {
	Version        int    `json:"version"`
	Status         string `json:"status"`
	Mode           string `json:"mode"`
	Signature      string `json:"signature"`
	CheckedAt      string `json:"checked_at"`
	RepairedVolume int    `json:"repaired_volume,omitempty"`
	RepairedArc    int    `json:"repaired_arc,omitempty"`
}

func (s *Store) outlineDuplicateScanCurrent(progress *domain.Progress) bool {
	marker, err := s.loadOutlineDuplicateScanMarker()
	if err != nil || marker == nil {
		return false
	}
	signature, err := s.currentOutlineDuplicateSignature(progress)
	if err != nil || signature == "" {
		return false
	}
	return marker.Version == outlineDuplicateScanVersion &&
		marker.Status == outlineDuplicateScanStatus &&
		marker.Mode == "batch" &&
		marker.Signature == signature
}

func (s *Store) saveCleanOutlineDuplicateScan(progress *domain.Progress, repairedVolume, repairedArc int) error {
	signature, err := s.currentOutlineDuplicateSignature(progress)
	if err != nil || signature == "" {
		return err
	}
	return s.Outline.io.WithWriteLock(func() error {
		return s.Outline.io.WriteJSONUnlocked(outlineDuplicateScanFile, outlineDuplicateScanMarker{
			Version:        outlineDuplicateScanVersion,
			Status:         outlineDuplicateScanStatus,
			Mode:           "batch",
			Signature:      signature,
			CheckedAt:      timeNowUTCString(),
			RepairedVolume: repairedVolume,
			RepairedArc:    repairedArc,
		})
	})
}

func (s *Store) clearOutlineDuplicateScan() error {
	return s.Outline.io.RemoveFile(outlineDuplicateScanFile)
}

func (s *Store) loadOutlineDuplicateScanMarker() (*outlineDuplicateScanMarker, error) {
	var marker outlineDuplicateScanMarker
	if err := s.Outline.io.ReadJSON(outlineDuplicateScanFile, &marker); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &marker, nil
}

func (s *Store) currentOutlineDuplicateSignature(progress *domain.Progress) (string, error) {
	source := struct {
		Layered bool                   `json:"layered"`
		Volumes []domain.VolumeOutline `json:"volumes,omitempty"`
		Entries []domain.OutlineEntry  `json:"entries,omitempty"`
	}{}

	if progress != nil && progress.Layered {
		volumes, err := s.Outline.LoadLayeredOutline()
		if err != nil {
			return "", err
		}
		if len(volumes) > 0 {
			source.Layered = true
			source.Volumes = volumes
			return outlineDuplicateSignature(source)
		}
	}

	entries, err := s.Outline.LoadOutline()
	if err != nil || len(entries) == 0 {
		return "", err
	}
	source.Entries = entries
	return outlineDuplicateSignature(source)
}

func outlineDuplicateSignature(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal outline duplicate signature: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
