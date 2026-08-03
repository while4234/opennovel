package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	web "github.com/voocel/ainovel-cli/internal/entry/web"
)

type config struct {
	RuntimeRoot         string `json:"runtime_root"`
	ValidationRoot      string `json:"validation_root"`
	NormalProjectID     string `json:"normal_project_id"`
	AdaptationProjectID string `json:"adaptation_project_id"`
	ReportPath          string `json:"report_path"`
	KeepClones          bool   `json:"keep_clones"`
}

type report struct {
	Version   int                         `json:"version"`
	CreatedAt string                      `json:"created_at"`
	Clones    []web.ValidationCloneReport `json:"clones"`
	Scenarios []scenario                  `json:"scenarios"`
	Complete  bool                        `json:"complete"`
}

type scenario struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

var failureStage = "configuration"

func main() {
	configPath := flag.String("config", "", "path to explicit validation config")
	flag.Parse()
	if *configPath == "" {
		fatal(fmt.Errorf("--config is required; projects are never auto-selected"))
	}
	payload, err := os.ReadFile(*configPath)
	if err != nil {
		fatal(err)
	}
	var cfg config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		fatal(err)
	}
	if cfg.RuntimeRoot == "" || cfg.ValidationRoot == "" || cfg.NormalProjectID == "" || cfg.AdaptationProjectID == "" || cfg.ReportPath == "" {
		fatal(fmt.Errorf("runtime_root, validation_root, both explicit project ids, and report_path are required"))
	}
	if cfg.NormalProjectID == cfg.AdaptationProjectID {
		fatal(fmt.Errorf("normal and adaptation projects must be selected independently"))
	}
	store := web.NewProjectStore(cfg.RuntimeRoot)
	clones := make([]web.ProjectManifest, 0, 2)
	reports := make([]web.ValidationCloneReport, 0, 2)
	for _, selected := range []struct{ kind, id string }{{"normal", cfg.NormalProjectID}, {"adaptation", cfg.AdaptationProjectID}} {
		failureStage = "prepare_" + selected.kind + "_clone"
		anonymousID := selected.kind + "-" + randomSuffix()
		clone, cloneReport, err := store.CloneProjectForValidation(selected.id, cfg.ValidationRoot, anonymousID)
		if err != nil {
			cleanup(clones)
			fatal(fmt.Errorf("prepare %s validation clone: %w", selected.kind, err))
		}
		clones = append(clones, clone)
		reports = append(reports, cloneReport)
	}
	if !cfg.KeepClones {
		defer cleanup(clones)
	}
	result := report{Version: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339), Clones: reports, Scenarios: scenarioCatalog()}
	encoded, _ := json.MarshalIndent(result, "", "  ")
	if err := os.MkdirAll(filepath.Dir(cfg.ReportPath), 0o700); err != nil {
		failureStage = "write_report"
		fatal(err)
	}
	if err := os.WriteFile(cfg.ReportPath, append(encoded, '\n'), 0o600); err != nil {
		fatal(err)
	}
	fmt.Printf("prepared %d anonymous validation clones; 15 scenarios require operator execution and signed evidence\n", len(clones))
}

func cleanup(clones []web.ProjectManifest) {
	for _, clone := range clones {
		_ = os.RemoveAll(clone.RootDir)
	}
}

func randomSuffix() string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		fatal(err)
	}
	return hex.EncodeToString(value[:])
}

func scenarioCatalog() []scenario {
	names := []string{
		"read completed prose while writing", "insert chapter", "add arc", "add volume", "single-outline change rewrites only one chapter",
		"polish formal prose", "rerun volume and whole-book audits", "append to completed book", "rejected candidate preserves current", "resume revision after restart",
		"normal mode source firewall", "adaptation coverage and source identity", "resume cannot bypass human node", "one-line expansion full flow", "long context remains batched",
	}
	items := make([]scenario, 0, len(names))
	for index, name := range names {
		items = append(items, scenario{ID: index + 1, Name: name, Status: "pending", Reason: "requires operator-selected real clone execution with precondition/action/expected/actual/signature evidence"})
	}
	return items
}

func fatal(err error) {
	_ = err
	encoded, _ := json.Marshal(map[string]any{"error": map[string]string{"code": "validation_clone_failed", "stage": failureStage}})
	fmt.Fprintln(os.Stderr, string(encoded))
	os.Exit(1)
}
