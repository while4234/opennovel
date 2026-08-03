package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/cmd/expansion-auditor/internal/runner"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fatalf("usage: expansion-auditor init|dependency|revision --project-root <path>")
	}
	operation := os.Args[1]
	flags := flag.NewFlagSet(operation, flag.ContinueOnError)
	projectRoot := flags.String("project-root", "", "project output root")
	taskID := flags.String("task-id", "", "durable dependency task ID")
	revisionID := flags.String("revision-id", "", "durable revision ID")
	if err := flags.Parse(os.Args[2:]); err != nil {
		fatalf("flags: %v", err)
	}
	if strings.TrimSpace(*projectRoot) == "" {
		fatalf("project-root is required")
	}
	store := storepkg.NewStore(*projectRoot)
	if err := store.Init(); err != nil {
		fatalf("open project store: %v", err)
	}
	auditRunner, err := runner.New(*projectRoot, store)
	if err != nil {
		fatalf("open private auditor identity: %v", err)
	}
	ctx := context.Background()
	switch operation {
	case "init":
		return
	case "dependency":
		if strings.TrimSpace(*taskID) == "" {
			fatalf("task-id is required")
		}
		review, err := auditRunner.ProcessDependencyTask(ctx, *taskID)
		if err != nil {
			if errors.Is(err, host.ErrExpansionDependencyTaskNotFound) {
				fmt.Fprintln(os.Stderr, "dependency task is no longer pending")
				os.Exit(3)
			}
			fatalf("review dependency task: %v", err)
		}
		writeJSON(review)
	case "revision":
		if strings.TrimSpace(*revisionID) == "" {
			fatalf("revision-id is required")
		}
		artifact, err := auditRunner.ProcessRevisionTask(ctx, *revisionID)
		if err != nil {
			fatalf("review revision task: %v", err)
		}
		writeJSON(artifact)
	default:
		fatalf("unsupported operation %q", operation)
	}
}

func writeJSON(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fatalf("encode response: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
