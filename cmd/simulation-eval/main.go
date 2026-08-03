package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/voocel/ainovel-cli/internal/simeval"
)

func main() {
	fixture := flag.String("fixture", "testdata/simulation-e2e", "original synthetic fixture directory")
	jsonPath := flag.String("json", "", "optional machine-readable report path")
	flag.Parse()

	report := simeval.Run(context.Background(), *fixture)
	for _, invariant := range report.Invariants {
		fmt.Printf("%-32s %s %dms", invariant.Name, invariant.Status, invariant.DurationMS)
		if invariant.Failure != "" {
			fmt.Printf(" - %s", invariant.Failure)
		}
		fmt.Println()
	}
	fmt.Printf("simulation-e2e: %s (%d passed, %d failed)\n", report.Status, report.Summary.Passed, report.Summary.Failed)

	if *jsonPath != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := os.MkdirAll(filepath.Dir(*jsonPath), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := os.WriteFile(*jsonPath, append(data, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if report.Status != "pass" {
		os.Exit(1)
	}
}
