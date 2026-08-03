package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/web"
)

type novelSeed struct {
	Key        string
	Title      string
	SourcePath string
	SourceType string
}

var novelSeeds = []novelSeed{
	{Key: "xfk", Title: "大学刑法课", SourcePath: `D:\AINovel\package\xfk\output\novel\meta\adaptation`, SourceType: "adaptation_backup"},
	{Key: "gaz", Title: "诡案组", SourcePath: `D:\AINovel\novel\gaz`, SourceType: "project_dir"},
	{Key: "jqmq_1", Title: "娇妻美妾任君尝", SourcePath: `D:\AINovel\novel\jqmq_1`, SourceType: "project_dir"},
	{Key: "mzdnh", Title: "梦中的女孩", SourcePath: `D:\AINovel\novel\mzdnh`, SourceType: "project_dir"},
	{Key: "nsgl", Title: "女神攻略", SourcePath: `D:\AINovel\novel\nsgl`, SourceType: "project_dir"},
}

func main() {
	runtimeRoot := flag.String("runtime-root", "", "web runtime root that contains novel_library")
	verifyOnly := flag.Bool("verify-only", false, "verify seeded novel library entries without importing")
	force := flag.Bool("force", false, "replace existing seeded entries before importing")
	flag.Parse()

	if strings.TrimSpace(*runtimeRoot) == "" {
		die("runtime-root is required")
	}
	root, err := filepath.Abs(*runtimeRoot)
	if err != nil {
		die("resolve runtime root: %v", err)
	}
	if err := web.EnsureRuntimeRoot(root); err != nil {
		die("%v", err)
	}

	if *verifyOnly {
		if err := verifyNovelSeeds(root); err != nil {
			die("%v", err)
		}
		fmt.Printf("Verified %d novel library entries in %s\n", len(novelSeeds), root)
		return
	}

	service := web.NewLibraryService(root)
	for _, seed := range novelSeeds {
		if err := assertAllowedSeed(seed); err != nil {
			die("%v", err)
		}
		entryRoot := filepath.Join(service.NovelDir(), seed.Title)
		if _, err := os.Stat(entryRoot); err == nil {
			if !*force {
				fmt.Printf("Novel library entry already present, skipping: %s\n", seed.Title)
				continue
			}
			if err := os.RemoveAll(entryRoot); err != nil {
				die("replace existing entry %s: %v", seed.Title, err)
			}
		} else if !os.IsNotExist(err) {
			die("stat existing entry %s: %v", seed.Title, err)
		}

		adaptationRoot, err := adaptationRootForSeed(seed)
		if err != nil {
			die("%v", err)
		}
		sourcePath, err := sourcePathFromAdaptationRoot(adaptationRoot)
		if err != nil {
			die("%v", err)
		}
		item, err := service.SaveNovelFromPreparedRoot(seed.Title, adaptationRoot, sourcePath)
		if err != nil {
			die("seed %s: %v", seed.Title, err)
		}
		fmt.Printf("Seeded novel library entry: %s (%d chapters)\n", item.Name, item.ChapterCount)
	}
	if err := verifyNovelSeeds(root); err != nil {
		die("%v", err)
	}
	fmt.Printf("Verified %d novel library entries in %s\n", len(novelSeeds), root)
}

func adaptationRootForSeed(seed novelSeed) (string, error) {
	switch seed.SourceType {
	case "adaptation_backup":
		return requireDir(seed.SourcePath, seed.Title+" adaptation backup")
	case "project_dir":
		candidates := []string{
			filepath.Join(seed.SourcePath, "output", "novel", "meta", "adaptation"),
			filepath.Join(seed.SourcePath, "output", "meta", "adaptation"),
			filepath.Join(seed.SourcePath, "meta", "adaptation"),
		}
		for _, candidate := range candidates {
			if path, err := requireDir(candidate, ""); err == nil {
				return path, nil
			}
		}
		return "", fmt.Errorf("cannot find prepared adaptation root for %s under %s", seed.Title, seed.SourcePath)
	default:
		return "", fmt.Errorf("unsupported source type %q for %s", seed.SourceType, seed.Title)
	}
}

func sourcePathFromAdaptationRoot(adaptationRoot string) (string, error) {
	var manifest domain.AdaptationSourceManifest
	manifestPath := filepath.Join(adaptationRoot, "source_manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("read source manifest %s: %w", manifestPath, err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("decode source manifest %s: %w", manifestPath, err)
	}
	sourcePath := strings.TrimSpace(manifest.SourcePath)
	if sourcePath == "" {
		return "", fmt.Errorf("source manifest %s has empty source_path", manifestPath)
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return "", fmt.Errorf("stat source file %s: %w", sourcePath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("source path is a directory: %s", sourcePath)
	}
	return sourcePath, nil
}

func verifyNovelSeeds(runtimeRoot string) error {
	service := web.NewLibraryService(runtimeRoot)
	items, err := service.ListNovelEntries("")
	if err != nil {
		return err
	}
	found := make(map[string]bool, len(items))
	for _, item := range items {
		found[item.Name] = true
	}
	for _, seed := range novelSeeds {
		if err := assertAllowedSeed(seed); err != nil {
			return err
		}
		if !found[seed.Title] {
			return fmt.Errorf("novel library entry missing: %s", seed.Title)
		}
		entryRoot := filepath.Join(service.NovelDir(), seed.Title)
		librarySource := filepath.Join(entryRoot, "source", "source.txt")
		manifest, err := readSourceManifest(filepath.Join(entryRoot, "meta", "adaptation", "source_manifest.json"))
		if err != nil {
			return err
		}
		if filepath.Clean(manifest.SourcePath) != filepath.Clean(librarySource) {
			return fmt.Errorf("%s source_path = %s, want %s", seed.Title, manifest.SourcePath, librarySource)
		}
		if _, err := os.Stat(librarySource); err != nil {
			return fmt.Errorf("%s library source missing: %w", seed.Title, err)
		}
	}
	return nil
}

func readSourceManifest(path string) (domain.AdaptationSourceManifest, error) {
	var manifest domain.AdaptationSourceManifest
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest, fmt.Errorf("read source manifest %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, fmt.Errorf("decode source manifest %s: %w", path, err)
	}
	return manifest, nil
}

func assertAllowedSeed(seed novelSeed) error {
	for _, path := range []string{seed.Key, seed.Title, seed.SourcePath} {
		if hasPathSegment(path, "lhk") {
			return fmt.Errorf("refusing excluded lhk seed path or name: %s", path)
		}
	}
	return nil
}

func hasPathSegment(path, segment string) bool {
	parts := strings.FieldsFunc(filepath.Clean(path), func(r rune) bool {
		return r == '\\' || r == '/'
	})
	for _, part := range parts {
		if strings.EqualFold(part, segment) {
			return true
		}
	}
	return false
}

func requireDir(path, description string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		if description == "" {
			description = path
		}
		return "", fmt.Errorf("%s is not a directory", description)
	}
	return filepath.Abs(path)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
