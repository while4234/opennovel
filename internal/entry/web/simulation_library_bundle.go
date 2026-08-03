package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	simulationLibraryBundleVersion      = "simulation_library_bundle.v1"
	simulationLibraryBundleManifestName = "bundle.json"
	simulationLibraryBundleSourcesDir   = "sources"
)

type simulationLibraryBundleManifest struct {
	Version       string                          `json:"version"`
	Name          string                          `json:"name"`
	ProfileDigest string                          `json:"profile_digest"`
	SourceCount   int                             `json:"source_count"`
	Sources       []simulationLibraryBundleSource `json:"sources"`
	UpdatedAt     time.Time                       `json:"updated_at"`
}

type simulationLibraryBundleSource struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func (s *LibraryService) saveSimulationProfileBundleFromProject(
	project ProjectManifest,
	name string,
	replace bool,
) (apiLibraryItem, error) {
	sourcePath, err := findProjectSimulationProfile(project)
	if err != nil {
		return apiLibraryItem{}, err
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return apiLibraryItem{}, fmt.Errorf("read simulation profile: %w", err)
	}
	displayName, fileName, err := libraryJSONFilename(name)
	if err != nil {
		return apiLibraryItem{}, err
	}
	portableData, sourceCount, err := portableSimulationProfileData(data)
	if err != nil {
		return apiLibraryItem{}, err
	}
	_, entryName, err := libraryEntryName(displayName)
	if err != nil {
		return apiLibraryItem{}, err
	}
	if err := os.MkdirAll(s.SimulationDir(), 0o755); err != nil {
		return apiLibraryItem{}, fmt.Errorf("create simulation library: %w", err)
	}
	if err := os.MkdirAll(s.SimulationCorpusDir(), 0o755); err != nil {
		return apiLibraryItem{}, fmt.Errorf("create simulation corpus library: %w", err)
	}
	profileTarget, err := safeLibraryTarget(s.SimulationDir(), fileName)
	if err != nil {
		return apiLibraryItem{}, err
	}
	corpusTarget, err := safeLibraryTarget(s.SimulationCorpusDir(), entryName)
	if err != nil {
		return apiLibraryItem{}, err
	}
	if !replace {
		if pathExists(profileTarget) || pathExists(corpusTarget) {
			return apiLibraryItem{}, fmt.Errorf("library item %q already exists", displayName)
		}
	}

	tempProfile, err := writeTemporarySimulationProfile(s.SimulationDir(), fileName, portableData)
	if err != nil {
		return apiLibraryItem{}, err
	}
	defer os.Remove(tempProfile)
	tempCorpus, archiveCount, err := s.buildTemporarySimulationCorpusBundle(
		project,
		displayName,
		entryName,
		portableData,
		sourceCount,
	)
	if err != nil {
		return apiLibraryItem{}, err
	}
	defer os.RemoveAll(tempCorpus)
	if err := installSimulationLibraryBundle(profileTarget, corpusTarget, tempProfile, tempCorpus); err != nil {
		return apiLibraryItem{}, err
	}
	info, err := os.Stat(profileTarget)
	if err != nil {
		return apiLibraryItem{}, fmt.Errorf("stat simulation profile %s: %w", displayName, err)
	}
	return apiLibraryItem{
		Name:                displayName,
		FileName:            fileName,
		Size:                info.Size(),
		UpdatedAt:           info.ModTime(),
		SourceCount:         sourceCount,
		ProfileVersion:      domain.SimulationPortableProfileVersion,
		HealthState:         simulationPortableHealth(portableData),
		Migrated:            simulationPortableMigrated(portableData),
		SourceArchived:      true,
		ArchivedSourceCount: archiveCount,
	}, nil
}

func writeTemporarySimulationProfile(dir, fileName string, data []byte) (string, error) {
	file, err := os.CreateTemp(dir, "."+fileName+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create temporary simulation profile: %w", err)
	}
	path := file.Name()
	success := false
	defer func() {
		_ = file.Close()
		if !success {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(bytes.TrimSpace(data)); err != nil {
		return "", fmt.Errorf("write temporary simulation profile: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync temporary simulation profile: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close temporary simulation profile: %w", err)
	}
	success = true
	return path, nil
}

func (s *LibraryService) buildTemporarySimulationCorpusBundle(
	project ProjectManifest,
	displayName string,
	entryName string,
	portableData []byte,
	expectedSourceCount int,
) (string, int, error) {
	profile, err := domain.UnmarshalSimulationPortableProfile(portableData)
	if err != nil {
		return "", 0, err
	}
	files, err := projectSimulationSourceFiles(projectSimulateDir(project))
	if err != nil {
		return "", 0, fmt.Errorf("list project simulation sources: %w", err)
	}
	if len(files) == 0 {
		return "", 0, fmt.Errorf("project has no simulation source files to archive")
	}
	if expectedSourceCount > 0 && len(files) != expectedSourceCount {
		return "", 0, fmt.Errorf(
			"project simulation source count %d does not match profile source count %d",
			len(files),
			expectedSourceCount,
		)
	}
	tempRoot, err := os.MkdirTemp(s.SimulationCorpusDir(), "."+entryName+".tmp-*")
	if err != nil {
		return "", 0, fmt.Errorf("create temporary simulation corpus bundle: %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(tempRoot)
		}
	}()
	sourcesDir := filepath.Join(tempRoot, simulationLibraryBundleSourcesDir)
	if err := os.MkdirAll(sourcesDir, 0o755); err != nil {
		return "", 0, fmt.Errorf("create simulation corpus sources dir: %w", err)
	}
	bundle := simulationLibraryBundleManifest{
		Version:       simulationLibraryBundleVersion,
		Name:          displayName,
		ProfileDigest: profile.ProfileDigest,
		SourceCount:   len(files),
		UpdatedAt:     time.Now().UTC(),
	}
	for _, file := range files {
		source := filepath.Join(projectSimulateDir(project), file.Name)
		target := filepath.Join(sourcesDir, file.Name)
		if err := copyFileOverwrite(source, target); err != nil {
			return "", 0, err
		}
		digest, err := simulationSourceFileDigest(target)
		if err != nil {
			return "", 0, err
		}
		bundle.Sources = append(bundle.Sources, simulationLibraryBundleSource{
			Name:   file.Name,
			Size:   file.Size,
			SHA256: digest,
		})
	}
	sort.Slice(bundle.Sources, func(i, j int) bool {
		return strings.ToLower(bundle.Sources[i].Name) < strings.ToLower(bundle.Sources[j].Name)
	})
	if err := writeJSONFile(filepath.Join(tempRoot, simulationLibraryBundleManifestName), bundle); err != nil {
		return "", 0, err
	}
	success = true
	return tempRoot, len(files), nil
}

func installSimulationLibraryBundle(profileTarget, corpusTarget, tempProfile, tempCorpus string) error {
	stamp := fmt.Sprintf(".bundle-backup-%d", time.Now().UnixNano())
	profileBackup := profileTarget + stamp
	corpusBackup := corpusTarget + stamp
	profileExisted := pathExists(profileTarget)
	corpusExisted := pathExists(corpusTarget)
	if profileExisted {
		if err := os.Rename(profileTarget, profileBackup); err != nil {
			return fmt.Errorf("backup simulation profile: %w", err)
		}
	}
	if corpusExisted {
		if err := os.Rename(corpusTarget, corpusBackup); err != nil {
			if profileExisted {
				_ = os.Rename(profileBackup, profileTarget)
			}
			return fmt.Errorf("backup simulation corpus: %w", err)
		}
	}
	installedProfile := false
	installedCorpus := false
	defer func() {
		if installedProfile && installedCorpus {
			_ = os.Remove(profileBackup)
			_ = os.RemoveAll(corpusBackup)
			return
		}
		if installedProfile {
			_ = os.Remove(profileTarget)
		}
		if installedCorpus {
			_ = os.RemoveAll(corpusTarget)
		}
		if profileExisted {
			_ = os.Rename(profileBackup, profileTarget)
		}
		if corpusExisted {
			_ = os.Rename(corpusBackup, corpusTarget)
		}
	}()
	if err := os.Rename(tempProfile, profileTarget); err != nil {
		return fmt.Errorf("install simulation profile: %w", err)
	}
	installedProfile = true
	if err := os.Rename(tempCorpus, corpusTarget); err != nil {
		return fmt.Errorf("install simulation corpus: %w", err)
	}
	installedCorpus = true
	return nil
}

func (s *LibraryService) simulationCorpusArchiveStatus(name string, profileData []byte) (bool, int) {
	_, entryName, err := libraryEntryName(name)
	if err != nil {
		return false, 0
	}
	bundle, _, err := s.readSimulationCorpusBundle(entryName, profileData)
	if err != nil {
		return false, 0
	}
	return true, bundle.SourceCount
}

func (s *LibraryService) restoreSimulationCorpusIntoProject(
	project ProjectManifest,
	name string,
	profilePath string,
) (bool, int, error) {
	_, entryName, err := libraryEntryName(name)
	if err != nil {
		return false, 0, err
	}
	profileData, err := os.ReadFile(profilePath)
	if err != nil {
		return false, 0, fmt.Errorf("read simulation library profile: %w", err)
	}
	bundle, bundleRoot, err := s.readSimulationCorpusBundle(entryName, profileData)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil
		}
		return false, 0, err
	}
	targetDir := projectSimulateDir(project)
	exact, err := simulationCorpusMatchesDirectory(bundle, targetDir)
	if err != nil {
		return false, 0, err
	}
	if exact {
		return true, bundle.SourceCount, nil
	}
	if entries, readErr := os.ReadDir(targetDir); readErr == nil && len(entries) > 0 {
		return false, 0, fmt.Errorf("project already has different simulation source files; load the corpus into an empty project or remove them explicitly")
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return false, 0, fmt.Errorf("read project simulation sources: %w", readErr)
	}
	tempDir, err := os.MkdirTemp(project.RootDir, ".simulate-restore-*")
	if err != nil {
		return false, 0, fmt.Errorf("create temporary simulation restore dir: %w", err)
	}
	defer os.RemoveAll(tempDir)
	for _, source := range bundle.Sources {
		if err := copyFileOverwrite(
			filepath.Join(bundleRoot, simulationLibraryBundleSourcesDir, source.Name),
			filepath.Join(tempDir, source.Name),
		); err != nil {
			return false, 0, err
		}
	}
	if err := os.Remove(targetDir); err != nil && !os.IsNotExist(err) {
		return false, 0, fmt.Errorf("remove empty simulation source dir: %w", err)
	}
	if err := os.Rename(tempDir, targetDir); err != nil {
		return false, 0, fmt.Errorf("restore simulation corpus: %w", err)
	}
	return true, bundle.SourceCount, nil
}

func (s *LibraryService) readSimulationCorpusBundle(
	entryName string,
	profileData []byte,
) (simulationLibraryBundleManifest, string, error) {
	var bundle simulationLibraryBundleManifest
	profile, err := domain.UnmarshalSimulationPortableProfile(profileData)
	if err != nil {
		return bundle, "", err
	}
	root, err := safeLibraryTarget(s.SimulationCorpusDir(), entryName)
	if err != nil {
		return bundle, "", err
	}
	if err := readJSONFile(filepath.Join(root, simulationLibraryBundleManifestName), &bundle); err != nil {
		return bundle, "", err
	}
	if bundle.Version != simulationLibraryBundleVersion ||
		bundle.ProfileDigest != profile.ProfileDigest ||
		bundle.SourceCount <= 0 ||
		len(bundle.Sources) != bundle.SourceCount {
		return bundle, "", fmt.Errorf("simulation corpus archive is stale or invalid")
	}
	seen := make(map[string]struct{}, len(bundle.Sources))
	for _, source := range bundle.Sources {
		name, err := sanitizeUploadedFilename(source.Name, textUploadExtensions)
		if err != nil || name != source.Name || source.Size < 0 || len(source.SHA256) != 64 {
			return bundle, "", fmt.Errorf("simulation corpus archive contains invalid source metadata")
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			return bundle, "", fmt.Errorf("simulation corpus archive contains duplicate source names")
		}
		seen[key] = struct{}{}
		path := filepath.Join(root, simulationLibraryBundleSourcesDir, name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() != source.Size {
			return bundle, "", fmt.Errorf("simulation corpus archive source %q is missing or changed", name)
		}
		digest, err := simulationSourceFileDigest(path)
		if err != nil || digest != source.SHA256 {
			return bundle, "", fmt.Errorf("simulation corpus archive source %q failed integrity validation", name)
		}
	}
	return bundle, root, nil
}

func simulationCorpusMatchesDirectory(bundle simulationLibraryBundleManifest, dir string) (bool, error) {
	files, err := projectSimulationSourceFiles(dir)
	if err != nil {
		return false, err
	}
	if len(files) != bundle.SourceCount {
		return false, nil
	}
	expected := make(map[string]simulationLibraryBundleSource, len(bundle.Sources))
	for _, source := range bundle.Sources {
		expected[strings.ToLower(source.Name)] = source
	}
	for _, file := range files {
		source, ok := expected[strings.ToLower(file.Name)]
		if !ok || source.Size != file.Size {
			return false, nil
		}
		digest, err := simulationSourceFileDigest(filepath.Join(dir, file.Name))
		if err != nil {
			return false, err
		}
		if digest != source.SHA256 {
			return false, nil
		}
	}
	return true, nil
}

func simulationSourceFileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open simulation source %s: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash simulation source %s: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
