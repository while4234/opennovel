package web

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/utils"
)

const simulationAutoSplitTargetRunes = 8000
const simulationSuspiciousLongNovelRunes = 500000
const simulationSuspiciousFewChapterCount = 20
const simulationSuspiciousOutlierRatio = 6
const simulationSuspiciousOutlierMinRunes = simulationAutoSplitTargetRunes * 3

var simulationGeneratedPartNameRegex = regexp.MustCompile(`(?i)^(.*)\.part_\d{3}_ch(\d{4})-(\d{4})\.txt$`)

type simulationSplitReport struct {
	SplitFiles int
	Parts      int
}

type simulationChapterRef struct {
	Number int
	Title  string
	Body   string
	Runes  int
}

type simulationChapterPart struct {
	Chapters []simulationChapterRef
	Runes    int
}

func prepareSimulationSourcesForAnalysis(sourceDir string) (simulationSplitReport, error) {
	files, err := projectSimulationSourceFiles(sourceDir)
	if err != nil {
		return simulationSplitReport{}, err
	}
	var report simulationSplitReport
	generatedReport, err := normalizeGeneratedSimulationParts(sourceDir, files)
	if err != nil {
		return report, err
	}
	report.SplitFiles += generatedReport.SplitFiles
	report.Parts += generatedReport.Parts
	if generatedReport.Parts > 0 {
		files, err = projectSimulationSourceFiles(sourceDir)
		if err != nil {
			return report, err
		}
	}
	for _, file := range files {
		if isGeneratedSimulationPart(file.Name) {
			continue
		}
		path := filepath.Join(sourceDir, file.Name)
		data, err := os.ReadFile(path)
		if err != nil {
			return report, fmt.Errorf("read simulation source %s: %w", file.Name, err)
		}
		if utf8.RuneCountInString(strings.TrimSpace(utils.DecodeText(data))) <= simulationAutoSplitTargetRunes {
			continue
		}
		parts, err := splitSimulationSourceFile(path)
		if err != nil {
			return report, fmt.Errorf("split simulation source %s: %w", file.Name, err)
		}
		if len(parts) == 0 {
			continue
		}
		if err := replaceSimulationSourceWithParts(sourceDir, file.Name, parts); err != nil {
			return report, err
		}
		report.SplitFiles++
		report.Parts += len(parts)
	}
	return report, nil
}

func splitSimulationSourceFile(path string) ([]simulationChapterPart, error) {
	return splitSimulationSourceFileFrom(path, 1)
}

func splitSimulationSourceFileFrom(path string, firstChapterNumber int) ([]simulationChapterPart, error) {
	chapters, err := imp.SplitFile(path)
	if err != nil {
		return nil, err
	}
	if len(chapters) == 0 {
		return nil, nil
	}
	refs := make([]simulationChapterRef, 0, len(chapters))
	for i, chapter := range chapters {
		ref := simulationChapterRef{
			Number: firstChapterNumber + i,
			Title:  strings.TrimSpace(chapter.Title),
			Body:   strings.TrimSpace(chapter.Content),
		}
		ref.Runes = utf8.RuneCountInString(renderSimulationChapter(ref))
		refs = append(refs, ref)
	}
	if err := validateSimulationSplitQuality(refs); err != nil {
		return nil, err
	}
	return makeSimulationChapterParts(refs, simulationAutoSplitTargetRunes), nil
}

type generatedSimulationPartInfo struct {
	File apiUploadedFile
	Stem string
	From int
	To   int
}

func normalizeGeneratedSimulationParts(sourceDir string, files []apiUploadedFile) (simulationSplitReport, error) {
	groups := make(map[string][]generatedSimulationPartInfo)
	for _, file := range files {
		info, ok := parseGeneratedSimulationPartInfo(file)
		if !ok {
			continue
		}
		groups[info.Stem] = append(groups[info.Stem], info)
	}
	var report simulationSplitReport
	for stem, group := range groups {
		sort.Slice(group, func(i, j int) bool {
			if group[i].From == group[j].From {
				return group[i].File.Name < group[j].File.Name
			}
			return group[i].From < group[j].From
		})
		oversize := false
		for _, info := range group {
			data, err := os.ReadFile(filepath.Join(sourceDir, info.File.Name))
			if err != nil {
				return report, fmt.Errorf("read generated simulation part %s: %w", info.File.Name, err)
			}
			if utf8.RuneCountInString(strings.TrimSpace(utils.DecodeText(data))) > simulationAutoSplitTargetRunes {
				oversize = true
				break
			}
		}
		if !oversize {
			continue
		}

		refs := make([]simulationChapterRef, 0)
		for _, info := range group {
			parts, err := splitSimulationSourceFileFrom(filepath.Join(sourceDir, info.File.Name), info.From)
			if err != nil {
				return report, fmt.Errorf("split generated simulation part %s: %w", info.File.Name, err)
			}
			for _, part := range parts {
				refs = append(refs, part.Chapters...)
			}
		}
		if len(refs) == 0 {
			continue
		}
		parts := makeSimulationChapterParts(refs, simulationAutoSplitTargetRunes)
		if len(parts) == 0 {
			continue
		}
		oldNames := make([]string, 0, len(group))
		for _, info := range group {
			oldNames = append(oldNames, info.File.Name)
		}
		newNames := make([]string, 0, len(parts))
		for i, part := range parts {
			newNames = append(newNames, simulationPartFilenameFromStem(stem, i+1, part))
		}
		if sameStringSet(oldNames, newNames) {
			continue
		}
		if err := replaceGeneratedSimulationParts(sourceDir, oldNames, stem, parts); err != nil {
			return report, err
		}
		report.SplitFiles += len(oldNames)
		report.Parts += len(parts)
	}
	return report, nil
}

func parseGeneratedSimulationPartInfo(file apiUploadedFile) (generatedSimulationPartInfo, bool) {
	m := simulationGeneratedPartNameRegex.FindStringSubmatch(file.Name)
	if len(m) != 4 {
		return generatedSimulationPartInfo{}, false
	}
	from, err := strconv.Atoi(m[2])
	if err != nil {
		return generatedSimulationPartInfo{}, false
	}
	to, err := strconv.Atoi(m[3])
	if err != nil || to < from {
		return generatedSimulationPartInfo{}, false
	}
	return generatedSimulationPartInfo{File: file, Stem: m[1], From: from, To: to}, true
}

func validateSimulationSplitQuality(chapters []simulationChapterRef) error {
	if len(chapters) == 0 {
		return nil
	}
	totalRunes := 0
	lengths := make([]int, 0, len(chapters))
	for _, chapter := range chapters {
		totalRunes += chapter.Runes
		lengths = append(lengths, chapter.Runes)
	}
	if totalRunes >= simulationSuspiciousLongNovelRunes && len(chapters) <= simulationSuspiciousFewChapterCount {
		return simulationRunError{message: fmt.Sprintf(
			"chapter split looks suspicious: recognized only %d chapters from about %d characters; the chapter-title pattern may not match this novel, so analysis was not started",
			len(chapters),
			totalRunes,
		)}
	}
	outlier, median, ok := suspiciousSimulationChapterOutlier(chapters, lengths)
	if !ok {
		return nil
	}
	title := strings.TrimSpace(outlier.Title)
	if title == "" {
		title = fmt.Sprintf("chapter %d", outlier.Number)
	}
	return simulationRunError{message: fmt.Sprintf(
		"chapter split looks suspicious: chapter %d (%s) has about %d characters, much larger than the median chapter size %d; the chapter-title pattern may have missed boundaries, so analysis was not started",
		outlier.Number,
		title,
		outlier.Runes,
		median,
	)}
}

func suspiciousSimulationChapterOutlier(chapters []simulationChapterRef, lengths []int) (simulationChapterRef, int, bool) {
	if len(chapters) < 3 {
		return simulationChapterRef{}, 0, false
	}
	sorted := append([]int(nil), lengths...)
	sort.Ints(sorted)
	median := sorted[len(sorted)/2]
	if median <= 0 {
		return simulationChapterRef{}, 0, false
	}
	maxIndex := 0
	for i := 1; i < len(chapters); i++ {
		if chapters[i].Runes > chapters[maxIndex].Runes {
			maxIndex = i
		}
	}
	minRunes := simulationSuspiciousOutlierMinRunes
	ratio := simulationSuspiciousOutlierRatio
	if len(chapters) < 5 {
		minRunes = simulationAutoSplitTargetRunes * 8
		ratio = 10
	}
	outlier := chapters[maxIndex]
	if outlier.Runes >= minRunes && outlier.Runes >= median*ratio {
		return outlier, median, true
	}
	return simulationChapterRef{}, median, false
}

func makeSimulationChapterParts(chapters []simulationChapterRef, targetRunes int) []simulationChapterPart {
	var parts []simulationChapterPart
	current := simulationChapterPart{}
	for _, chapter := range chapters {
		separatorRunes := 0
		if len(current.Chapters) > 0 {
			separatorRunes = 2
		}
		if len(current.Chapters) > 0 && current.Runes+separatorRunes+chapter.Runes > targetRunes {
			parts = append(parts, current)
			current = simulationChapterPart{}
			separatorRunes = 0
		}
		current.Chapters = append(current.Chapters, chapter)
		current.Runes += separatorRunes + chapter.Runes
	}
	if len(current.Chapters) > 0 {
		parts = append(parts, current)
	}
	return parts
}

func replaceSimulationSourceWithParts(sourceDir, sourceName string, parts []simulationChapterPart) error {
	sourcePath := filepath.Join(sourceDir, sourceName)
	targets := make([]string, 0, len(parts))
	for i, part := range parts {
		name := simulationPartFilename(sourceName, i+1, part)
		target, err := safeUploadTarget(sourceDir, name)
		if err != nil {
			return err
		}
		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("split target %q already exists", name)
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("check split target %s: %w", name, err)
		}
		targets = append(targets, target)
	}

	written := make([]string, 0, len(parts))
	for i, part := range parts {
		target := targets[i]
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			cleanupSimulationPartFiles(written)
			return fmt.Errorf("write split part %s: %w", filepath.Base(target), err)
		}
		if _, err := file.WriteString(renderSimulationPart(part)); err != nil {
			_ = file.Close()
			cleanupSimulationPartFiles(append(written, target))
			return fmt.Errorf("write split part %s: %w", filepath.Base(target), err)
		}
		if err := file.Close(); err != nil {
			cleanupSimulationPartFiles(append(written, target))
			return fmt.Errorf("write split part %s: %w", filepath.Base(target), err)
		}
		written = append(written, target)
	}
	if err := os.Remove(sourcePath); err != nil {
		cleanupSimulationPartFiles(written)
		return fmt.Errorf("remove original split source %s: %w", sourceName, err)
	}
	return nil
}

func replaceGeneratedSimulationParts(sourceDir string, oldNames []string, stem string, parts []simulationChapterPart) error {
	oldSet := make(map[string]struct{}, len(oldNames))
	for _, name := range oldNames {
		oldSet[name] = struct{}{}
	}
	targets := make([]string, 0, len(parts))
	for i, part := range parts {
		name := simulationPartFilenameFromStem(stem, i+1, part)
		target, err := safeUploadTarget(sourceDir, name)
		if err != nil {
			return err
		}
		if _, ok := oldSet[name]; !ok {
			if _, err := os.Stat(target); err == nil {
				return fmt.Errorf("split target %q already exists", name)
			} else if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("check split target %s: %w", name, err)
			}
		}
		targets = append(targets, target)
	}

	tempPaths := make([]string, 0, len(parts))
	for i, part := range parts {
		temp, err := os.CreateTemp(sourceDir, ".simulation-resplit-*.tmp")
		if err != nil {
			cleanupSimulationPartFiles(tempPaths)
			return fmt.Errorf("create temporary split part: %w", err)
		}
		tempPath := temp.Name()
		if _, err := temp.WriteString(renderSimulationPart(part)); err != nil {
			_ = temp.Close()
			cleanupSimulationPartFiles(append(tempPaths, tempPath))
			return fmt.Errorf("write temporary split part %d: %w", i+1, err)
		}
		if err := temp.Close(); err != nil {
			cleanupSimulationPartFiles(append(tempPaths, tempPath))
			return fmt.Errorf("close temporary split part %d: %w", i+1, err)
		}
		tempPaths = append(tempPaths, tempPath)
	}

	for _, name := range oldNames {
		if err := os.Remove(filepath.Join(sourceDir, name)); err != nil && !os.IsNotExist(err) {
			cleanupSimulationPartFiles(tempPaths)
			return fmt.Errorf("remove old split part %s: %w", name, err)
		}
	}
	written := make([]string, 0, len(targets))
	for i, target := range targets {
		if err := os.Rename(tempPaths[i], target); err != nil {
			cleanupSimulationPartFiles(tempPaths[i:])
			cleanupSimulationPartFiles(written)
			return fmt.Errorf("rename split part %s: %w", filepath.Base(target), err)
		}
		written = append(written, target)
	}
	return nil
}

func cleanupSimulationPartFiles(paths []string) {
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

func simulationPartFilename(sourceName string, index int, part simulationChapterPart) string {
	stem := sanitizeSimulationSplitStem(strings.TrimSuffix(sourceName, filepath.Ext(sourceName)))
	return simulationPartFilenameFromStem(stem, index, part)
}

func simulationPartFilenameFromStem(stem string, index int, part simulationChapterPart) string {
	stem = sanitizeSimulationSplitStem(stem)
	from := part.Chapters[0].Number
	to := part.Chapters[len(part.Chapters)-1].Number
	return fmt.Sprintf("%s.part_%03d_ch%04d-%04d.txt", stem, index, from, to)
}

func sanitizeSimulationSplitStem(stem string) string {
	stem = strings.Trim(stem, ". ")
	if stem == "" {
		stem = "source"
	}
	const maxRunes = 90
	if utf8.RuneCountInString(stem) > maxRunes {
		stem = string([]rune(stem)[:maxRunes])
	}
	return stem
}

func renderSimulationPart(part simulationChapterPart) string {
	var b strings.Builder
	for i, chapter := range part.Chapters {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(renderSimulationChapter(chapter))
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderSimulationChapter(chapter simulationChapterRef) string {
	heading := fmt.Sprintf("%s%d%s", "\u7b2c", chapter.Number, "\u7ae0")
	if chapter.Title != "" {
		heading += " " + chapter.Title
	}
	if chapter.Body == "" {
		return heading + "\n"
	}
	return heading + "\n\n" + chapter.Body + "\n"
}

func isGeneratedSimulationPart(name string) bool {
	return simulationGeneratedPartNameRegex.MatchString(name)
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, item := range a {
		seen[item]++
	}
	for _, item := range b {
		if seen[item] == 0 {
			return false
		}
		seen[item]--
	}
	return true
}
