package sim

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type importProfileOptions struct {
	Call         structuredJSONCallOptions
	OnBatch      func(mergeSynthesisProgress)
	OnCheckpoint func(mergeSynthesisCheckpoint) error
}

func ImportProfile(ctx context.Context, deps Deps, path string) (ImportResult, error) {
	return importProfileWithOptions(ctx, deps, path, importProfileOptions{})
}

func importProfileWithOptions(ctx context.Context, deps Deps, path string, opts importProfileOptions) (ImportResult, error) {
	if deps.Store == nil {
		return ImportResult{}, fmt.Errorf("store is nil")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return ImportResult{}, fmt.Errorf("profile path is required")
	}
	if err := ctx.Err(); err != nil {
		return ImportResult{}, err
	}
	signatures := buildSimulationAnalysisSignatures(deps)
	data, err := os.ReadFile(path)
	if err != nil {
		return ImportResult{}, fmt.Errorf("read simulation profile failed")
	}
	imported, importedPortable, err := domain.UnmarshalSimulationProfileForCompatibility(data)
	if err != nil {
		return ImportResult{}, err
	}
	existing, err := deps.Store.Simulation.Load()
	if err != nil {
		return ImportResult{}, err
	}
	if importedPortable != nil {
		if existing == nil {
			if err := deps.Store.Simulation.SavePortable(*importedPortable); err != nil {
				return ImportResult{}, err
			}
			return ImportResult{
				ProfilePath:     path,
				ImportedSources: importedPortable.Corpus.SourceCount,
			}, nil
		}
		existingPortable, err := deps.Store.Simulation.LoadPortable()
		if err != nil {
			return ImportResult{}, err
		}
		localEvidence, err := deps.Store.Simulation.LoadLocalEvidence()
		if err != nil {
			return ImportResult{}, err
		}
		if existingPortable == nil {
			return ImportResult{}, fmt.Errorf("existing simulation profile is not portable")
		}
		if existingPortable.ProfileDigest == importedPortable.ProfileDigest {
			return ImportResult{
				ProfilePath:    path,
				SkippedSources: importedPortable.Corpus.SourceCount,
			}, nil
		}
		if localEvidence != nil && localEvidence.ProfileDigest == existingPortable.ProfileDigest &&
			(len(localEvidence.Sources) > 0 || len(localEvidence.SourceReports) > 0) {
			return ImportResult{}, fmt.Errorf("cannot merge a portable-only profile into a project with local simulation evidence")
		}
		mergedPortable, err := domain.MergeSimulationPortableProfiles(*existingPortable, *importedPortable, time.Now())
		if err != nil {
			return ImportResult{}, err
		}
		if err := deps.Store.Simulation.SavePortable(mergedPortable); err != nil {
			return ImportResult{}, err
		}
		return ImportResult{
			ProfilePath:     path,
			ImportedSources: importedPortable.Corpus.SourceCount,
		}, nil
	}

	merged, result := mergeImportedProfile(existing, imported, time.Now())
	result.ProfilePath = path
	if shouldResynthesizeImportedProfile(existing, result, merged) {
		if deps.LLM == nil {
			return ImportResult{}, fmt.Errorf("llm is nil")
		}
		checkpoint, err := deps.Store.Simulation.LoadMergeCheckpoint()
		if err != nil {
			return ImportResult{}, fmt.Errorf("read merge checkpoint: %w", err)
		}
		if checkpoint != nil {
			if _, ok := validMergeCheckpointWithSignature(checkpoint, merged.SourceReports, signatures.metadata.SynthesisSignature); !ok {
				if err := deps.Store.Simulation.ClearMergeCheckpoint(); err != nil {
					return ImportResult{}, fmt.Errorf("clear stale merge checkpoint: %w", err)
				}
				checkpoint = nil
			}
		}
		onCheckpoint := opts.OnCheckpoint
		if onCheckpoint == nil {
			onCheckpoint = func(checkpoint mergeSynthesisCheckpoint) error {
				domainCheckpoint := buildSimulationMergeCheckpointWithSignature(merged.SourceReports, maxMergePromptBytes, signatures.metadata.SynthesisSignature, checkpoint, time.Now())
				if domainCheckpoint == nil {
					return nil
				}
				return deps.Store.Simulation.SaveMergeCheckpoint(*domainCheckpoint)
			}
		}
		synthesis, err := mergeSynthesisBatchedWithOptions(ctx, deps.LLM, deps.Prompts.Merge, &merged, merged.SourceReports, mergeSynthesisOptions{
			Call:               opts.Call,
			Checkpoint:         checkpoint,
			SynthesisSignature: signatures.metadata.SynthesisSignature,
			OnBatch:            opts.OnBatch,
			OnCheckpoint:       onCheckpoint,
		})
		if err != nil {
			return ImportResult{}, err
		}
		merged.Synthesis = *synthesis
		result.ModelMerged = true
	}
	if err := saveFinalSimulationProfile(deps.Store.Simulation, merged); err != nil {
		return ImportResult{}, err
	}
	return result, nil
}

func RunImport(ctx context.Context, deps Deps, path string) (<-chan Event, error) {
	if deps.Store == nil {
		return nil, fmt.Errorf("store is nil")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("profile path is required")
	}
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		emit := func(stage Stage, current, total int, msg string, err error) {
			ev := Event{Time: time.Now(), Stage: stage, Current: current, Total: total, Message: msg, Err: err}
			select {
			case events <- ev:
			case <-ctx.Done():
			}
		}
		emit(StageImport, 0, 1, "导入仿写画像...", nil)
		mergeCurrent, mergeTotal := 0, 0
		result, err := importProfileWithOptions(ctx, deps, path, importProfileOptions{
			Call: structuredJSONCallOptions{
				ModelCallMaxAttempts:       deps.modelCallMaxAttempts(),
				StructureRepairMaxAttempts: deps.structureRepairMaxAttempts(),
				OnRetry: func(ev structuredJSONRetryEvent) {
					emit(StageMerge, mergeCurrent, mergeTotal, formatStructuredJSONRetryMessage(ev), ev.Err)
				},
			},
			OnBatch: func(progress mergeSynthesisProgress) {
				mergeCurrent, mergeTotal = progress.Current, progress.Total
				msg := "重合成仿写画像..."
				if progress.BatchTotal > 1 {
					msg = fmt.Sprintf("分批重合成仿写画像 %d/%d（批次 %d/%d，%d 篇）...", progress.Current, progress.Total, progress.BatchIndex, progress.BatchTotal, progress.BatchSize)
				}
				emit(StageMerge, progress.Current, progress.Total, msg, nil)
			},
		})
		if err != nil {
			emit(StageError, mergeCurrent, mergeTotal, "导入仿写画像失败", err)
			return
		}
		message := fmt.Sprintf("仿写画像已导入：新增 %d 篇，跳过重复 %d 篇", result.ImportedSources, result.SkippedSources)
		if result.ModelMerged {
			message += "，已分批重合成画像"
		}
		emit(StageDone, result.ImportedSources, result.ImportedSources+result.SkippedSources, message, nil)
	}()
	return events, nil
}

func shouldResynthesizeImportedProfile(existing *domain.SimulationProfile, result ImportResult, merged domain.SimulationProfile) bool {
	return existing != nil && result.ImportedSources > 0 && len(merged.SourceReports) > 0
}

func mergeImportedProfile(existing *domain.SimulationProfile, imported domain.SimulationProfile, now time.Time) (domain.SimulationProfile, ImportResult) {
	stamp := now.Format(time.RFC3339)
	merged := domain.SimulationProfile{
		Version:   domain.SimulationProfileVersion,
		CreatedAt: imported.CreatedAt,
		UpdatedAt: stamp,
		Corpus: domain.SimulationCorpusManifest{
			SourceDir: imported.Corpus.SourceDir,
		},
		Synthesis: imported.Synthesis,
	}
	if merged.CreatedAt == "" {
		merged.CreatedAt = stamp
	}
	if existing != nil {
		merged.CreatedAt = existing.CreatedAt
		if merged.CreatedAt == "" {
			merged.CreatedAt = stamp
		}
		merged.Corpus.SourceDir = existing.Corpus.SourceDir
		merged.Corpus.Sources = append(merged.Corpus.Sources, existing.Corpus.Sources...)
		merged.SourceReports = append(merged.SourceReports, existing.SourceReports...)
		merged.Synthesis = domain.MergeSimulationSynthesis(existing.Synthesis, imported.Synthesis)
	}

	known := make(map[string]struct{}, len(merged.Corpus.Sources))
	for _, source := range merged.Corpus.Sources {
		known[source.Fingerprint] = struct{}{}
	}
	result := ImportResult{}
	for _, source := range imported.Corpus.Sources {
		if _, ok := known[source.Fingerprint]; ok {
			result.SkippedSources++
			continue
		}
		known[source.Fingerprint] = struct{}{}
		merged.Corpus.Sources = append(merged.Corpus.Sources, source)
		result.ImportedSources++
	}

	reportKnown := make(map[string]struct{}, len(merged.SourceReports))
	for _, report := range merged.SourceReports {
		reportKnown[report.Fingerprint] = struct{}{}
	}
	for _, report := range imported.SourceReports {
		if _, ok := reportKnown[report.Fingerprint]; ok {
			continue
		}
		reportKnown[report.Fingerprint] = struct{}{}
		merged.SourceReports = append(merged.SourceReports, report)
	}
	sortProfile(&merged)
	sort.Slice(merged.SourceReports, func(i, j int) bool {
		return merged.SourceReports[i].Fingerprint < merged.SourceReports[j].Fingerprint
	})
	return merged, result
}
