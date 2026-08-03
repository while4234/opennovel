package host

import "github.com/voocel/ainovel-cli/internal/store"

// SimulationCoCreatePromptForEvaluation exposes the final, sanitized co-create
// prompt to the offline regression harness. It is read-only and delegates to
// the production builder so the harness cannot silently reimplement policy.
func SimulationCoCreatePromptForEvaluation(st *store.Store, mode string) string {
	return coCreateSystemPromptWithSimulation(st, mode)
}

// SimulationProfileSummaryForEvaluation returns the same bounded summary used
// by Host snapshots and the Web API.
func SimulationProfileSummaryForEvaluation(st *store.Store, mode string, chapter int) *SimulationProfileSummary {
	return buildSimulationProfileSummary(st, mode, chapter)
}
