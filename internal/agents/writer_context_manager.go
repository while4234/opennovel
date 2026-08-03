package agents

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
)

// writerContextManager treats validation receipts as durable phase boundaries.
// Once a validation tool has inspected the chapter, source payloads from the
// preceding phase no longer belong in every later provider request. The
// chapter itself and its contract remain durable in the Store and can be read
// again if a receipt asks for a repair.
type writerContextManager struct {
	*corecontext.ContextEngine
	phase *writerValidationPhaseStrategy
}

func newWriterContextManager(engine *corecontext.ContextEngine, cfg corecontext.ToolResultMicrocompactConfig) *writerContextManager {
	return &writerContextManager{
		ContextEngine: engine,
		phase:         newWriterValidationPhaseStrategy(cfg),
	}
}

func (m *writerContextManager) Project(ctx context.Context, msgs []agentcore.AgentMessage) (agentcore.ContextProjection, error) {
	active := latestWriterDispatchTurn(msgs)
	view, result, err := m.phase.Apply(ctx, active, active, corecontext.Budget{})
	if err != nil {
		return agentcore.ContextProjection{}, err
	}
	if !result.Applied {
		if len(active) == len(msgs) {
			return m.ContextEngine.Project(ctx, msgs)
		}
		projection, projectErr := m.ContextEngine.Project(ctx, active)
		if projectErr != nil {
			return agentcore.ContextProjection{}, projectErr
		}
		if projection.Messages == nil {
			projection.Messages = active
		}
		projection.ShouldCommit = true
		projection.CommitMessages = projection.Messages
		logWriterPhaseRewrite("host_dispatch_boundary", msgs, projection.Messages)
		return projection, nil
	}

	projection, err := m.ContextEngine.Project(ctx, view)
	if err != nil {
		return agentcore.ContextProjection{}, err
	}
	if projection.Messages == nil {
		projection.Messages = view
	}
	projection.ShouldCommit = true
	projection.CommitMessages = projection.Messages
	logWriterPhaseRewrite("validation_boundary", msgs, projection.Messages)
	return projection, nil
}

func (m *writerContextManager) RecoverOverflow(ctx context.Context, msgs []agentcore.AgentMessage, cause error) (agentcore.ContextRecoveryResult, error) {
	active := latestWriterDispatchTurn(msgs)
	view, result, err := m.phase.ForceApply(ctx, active, active, corecontext.Budget{})
	if err != nil {
		return agentcore.ContextRecoveryResult{}, err
	}
	if !result.Applied {
		return m.ContextEngine.RecoverOverflow(ctx, msgs, cause)
	}

	m.ContextEngine.Sync(view)
	logWriterPhaseRewrite("production_boundary", msgs, view)
	return agentcore.ContextRecoveryResult{
		View:           view,
		CommitMessages: view,
		Usage:          m.ContextEngine.Usage(),
		Changed:        true,
		ShouldCommit:   true,
		Strategy:       result.Name,
		CompactedCount: countCompactedToolResults(view),
		KeptCount:      1,
	}, nil
}

func latestWriterDispatchTurn(msgs []agentcore.AgentMessage) []agentcore.AgentMessage {
	for index := len(msgs) - 1; index >= 0; index-- {
		message, ok := msgs[index].(agentcore.Message)
		if !ok || message.Role != agentcore.RoleUser {
			continue
		}
		task := strings.TrimSpace(message.TextContent())
		for _, prefix := range []string{"写第 ", "恢复第", "重写第 ", "打磨第 "} {
			if strings.HasPrefix(task, prefix) {
				return msgs[index:]
			}
		}
	}
	return msgs
}

type writerValidationPhaseStrategy struct {
	keepRecent     int
	clearedMessage string
}

func newWriterValidationPhaseStrategy(cfg corecontext.ToolResultMicrocompactConfig) *writerValidationPhaseStrategy {
	if cfg.KeepRecent <= 0 {
		cfg.KeepRecent = 2
	}
	if cfg.ClearedMessage == "" {
		cfg.ClearedMessage = "[Prior Writer phase cleared; durable project data remains available through tools.]"
	}
	return &writerValidationPhaseStrategy{keepRecent: cfg.KeepRecent, clearedMessage: cfg.ClearedMessage}
}

func (s *writerValidationPhaseStrategy) Name() string { return "writer_validation_phase" }

func (s *writerValidationPhaseStrategy) Apply(ctx context.Context, transcript, view []agentcore.AgentMessage, budget corecontext.Budget) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	currentTurn := currentWriterTurn(view)
	cleanRecovery := writerCleanRecoveryState(currentTurn)
	if cleanRecovery.exhausted {
		return view, corecontext.StrategyResult{Name: "writer_clean_error_recovery_exhausted"}, errWriterCleanRecoveryExhausted
	}
	if cleanRecovery.restart {
		return cleanRestartWriterTurn(view)
	}
	hasValidation := hasWriterValidationReceipt(currentTurn)
	hasPersistedDraft := hasPersistedWriterDraftReceipt(currentTurn)
	hasDuplicateEvidence := hasDuplicateWriterContextResults(currentTurn)
	hasCompletedRationale := hasCompletedWriterToolRationale(currentTurn)
	if !hasValidation && !hasPersistedDraft && !hasDuplicateEvidence && !hasCompletedRationale {
		return view, corecontext.StrategyResult{Name: s.Name()}, nil
	}
	return s.compact(ctx, transcript, view, budget, hasDuplicateEvidence && !hasValidation && !hasPersistedDraft)
}

func hasCompletedWriterToolRationale(msgs []agentcore.AgentMessage) bool {
	completed := make(map[string]struct{})
	for _, item := range msgs {
		message, ok := item.(agentcore.Message)
		if !ok || message.Role != agentcore.RoleTool {
			continue
		}
		callID, _ := message.Metadata["tool_call_id"].(string)
		if callID != "" {
			completed[callID] = struct{}{}
		}
	}
	for _, item := range msgs {
		message, ok := item.(agentcore.Message)
		if !ok || message.Role != agentcore.RoleAssistant {
			continue
		}
		calls := message.ToolCalls()
		if len(calls) == 0 {
			continue
		}
		allCompleted := true
		for _, call := range calls {
			if _, ok := completed[call.ID]; !ok {
				allCompleted = false
				break
			}
		}
		if allCompleted && (message.ThinkingContent() != "" || message.TextContent() != "") {
			return true
		}
	}
	return false
}

func (s *writerValidationPhaseStrategy) ForceApply(ctx context.Context, transcript, view []agentcore.AgentMessage, budget corecontext.Budget) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	_ = ctx
	_ = transcript
	_ = budget
	// Crossing the exact production boundary is an emergency phase split. Keep
	// only the newest tool result; every chapter/context payload is durable and
	// can be re-read, while retrying the same oversized pair cannot recover.
	return compactWriterPhase(view, 1, s.clearedMessage, false, true)
}

func (s *writerValidationPhaseStrategy) compact(ctx context.Context, transcript, view []agentcore.AgentMessage, budget corecontext.Budget, preferOriginalContext bool) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	_ = ctx
	_ = transcript
	_ = budget
	return compactWriterPhase(view, s.keepRecent, s.clearedMessage, preferOriginalContext, hasWriterValidationReceipt(currentWriterTurn(view)))
}

func currentWriterTurn(msgs []agentcore.AgentMessage) []agentcore.AgentMessage {
	for index := len(msgs) - 1; index >= 0; index-- {
		message, ok := msgs[index].(agentcore.Message)
		if ok && message.Role == agentcore.RoleUser {
			return msgs[index:]
		}
	}
	return msgs
}

func hasWriterValidationReceipt(msgs []agentcore.AgentMessage) bool {
	return hasSuccessfulToolResult(msgs, func(name string) bool {
		switch name {
		case "check_consistency", "check_adaptation", "check_de_ai", "check_simulation":
			return true
		default:
			return false
		}
	})
}

func hasPersistedWriterDraftReceipt(msgs []agentcore.AgentMessage) bool {
	return hasToolResult(msgs, isPersistedWriterDraftTool)
}

func hasDuplicateWriterContextResults(msgs []agentcore.AgentMessage) bool {
	seen := make(map[string]struct{})
	for _, candidate := range collectWriterToolResults(msgs) {
		if candidate.alreadyCleared || (candidate.toolName != "novel_context" && candidate.toolName != "read_chapter") {
			continue
		}
		if _, duplicate := seen[candidate.key]; duplicate {
			return true
		}
		seen[candidate.key] = struct{}{}
	}
	return false
}

func isPersistedWriterDraftTool(name string) bool {
	switch name {
	case "draft_chapter", "edit_chapter", "repair_de_ai_batch":
		return true
	default:
		return false
	}
}

type writerToolResultCandidate struct {
	resultIndex      int
	assistantIndex   int
	callID           string
	toolName         string
	key              string
	alreadyCleared   bool
	isError          bool
	errorFingerprint string
	resultText       string
}

func compactWriterPhase(
	view []agentcore.AgentMessage,
	keepRecent int,
	clearedMessage string,
	preferOriginalContext bool,
	dropNovelContext bool,
) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	out := cloneWriterMessages(view)
	candidates := collectWriterToolResults(out)
	if len(candidates) == 0 {
		return view, corecontext.StrategyResult{Name: "writer_validation_phase"}, nil
	}

	protected := protectRecentWriterResults(candidates, keepRecent, preferOriginalContext, dropNovelContext)
	compactCalls := make(map[string]struct{})
	collapseCalls := make(map[string]struct{})
	completedCalls := make(map[string]struct{}, len(candidates))
	writeReceipts := latestWriterWriteReceipt(out, candidates)
	applied := false
	for _, candidate := range candidates {
		completedCalls[candidate.callID] = struct{}{}
		if candidate.alreadyCleared {
			compactCalls[candidate.callID] = struct{}{}
			continue
		}
		if isPersistedWriterDraftTool(candidate.toolName) {
			compactCalls[candidate.callID] = struct{}{}
			collapseCalls[candidate.callID] = struct{}{}
		}
		if dropNovelContext && candidate.toolName == "novel_context" {
			compactCalls[candidate.callID] = struct{}{}
		}
		if _, keep := protected[candidate.resultIndex]; keep {
			continue
		}
		message := out[candidate.resultIndex].(agentcore.Message)
		message.Content = []agentcore.ContentBlock{agentcore.TextBlock(clearedMessage)}
		message.Metadata = cloneWriterMetadata(message.Metadata)
		if candidate.isError {
			message.Metadata[writerToolErrorFingerprintMetadata] = writerToolErrorFingerprint(candidate.toolName, candidate.resultText)
		}
		message.Metadata["compacted_tool_result"] = true
		message.Metadata["compacted_tool_name"] = candidate.toolName
		out[candidate.resultIndex] = message
		compactCalls[candidate.callID] = struct{}{}
		applied = true
	}

	var historyChanged bool
	out, historyChanged = compactWriterToolHistory(out, compactCalls, collapseCalls, completedCalls, writeReceipts, clearedMessage)
	applied = applied || historyChanged
	if !applied {
		return view, corecontext.StrategyResult{Name: "writer_validation_phase"}, nil
	}
	return out, corecontext.StrategyResult{
		Applied:     true,
		TokensSaved: max(0, corecontext.EstimateTotal(view)-corecontext.EstimateTotal(out)),
		Name:        "writer_validation_phase",
	}, nil
}

func latestWriterWriteReceipt(msgs []agentcore.AgentMessage, candidates []writerToolResultCandidate) map[string]string {
	for index := len(candidates) - 1; index >= 0; index-- {
		candidate := candidates[index]
		if candidate.alreadyCleared || !isPersistedWriterDraftTool(candidate.toolName) {
			continue
		}
		message, ok := msgs[candidate.resultIndex].(agentcore.Message)
		if !ok {
			return nil
		}
		result := strings.TrimSpace(message.TextContent())
		if result == "" {
			return nil
		}
		return map[string]string{
			candidate.callID: "[Latest completed Writer write receipt; do not copy this text as tool arguments.]\n" +
				"tool=" + candidate.toolName + "\nresult=" + result,
		}
	}
	return nil
}

func collectWriterToolResults(msgs []agentcore.AgentMessage) []writerToolResultCandidate {
	type pendingCall struct {
		assistantIndex int
		toolName       string
		key            string
	}
	pending := make(map[string]pendingCall)
	var candidates []writerToolResultCandidate
	for index, item := range msgs {
		message, ok := item.(agentcore.Message)
		if !ok {
			continue
		}
		if message.Role == agentcore.RoleAssistant {
			for _, call := range message.ToolCalls() {
				key := writerToolResultKey(call.Name, call.Args)
				pending[call.ID] = pendingCall{assistantIndex: index, toolName: call.Name, key: key}
			}
			continue
		}
		if message.Role != agentcore.RoleTool {
			continue
		}
		callID, _ := message.Metadata["tool_call_id"].(string)
		call := pending[callID]
		if call.toolName == "" {
			continue
		}
		candidates = append(candidates, writerToolResultCandidate{
			resultIndex:      index,
			assistantIndex:   call.assistantIndex,
			callID:           callID,
			toolName:         call.toolName,
			key:              call.key,
			alreadyCleared:   message.Metadata["compacted_tool_result"] == true,
			isError:          message.Metadata["is_error"] == true,
			errorFingerprint: writerToolErrorFingerprintFromMetadata(message.Metadata),
			resultText:       strings.TrimSpace(message.TextContent()),
		})
	}
	return candidates
}

func writerToolErrorFingerprintFromMetadata(metadata map[string]any) string {
	fingerprint, _ := metadata[writerToolErrorFingerprintMetadata].(string)
	return strings.TrimSpace(fingerprint)
}

func writerToolResultKey(name string, args json.RawMessage) string {
	switch name {
	case "novel_context":
		// There is only one active Writer work package. Repeating the call cannot
		// add evidence and must not multiply the largest payload in the turn.
		return name
	case "read_chapter":
		var raw struct {
			Source string `json:"source"`
		}
		if json.Unmarshal(args, &raw) == nil && strings.EqualFold(strings.TrimSpace(raw.Source), "source") {
			// Adaptation can legitimately need distinct source chapters. Exact
			// duplicate reads are still collapsible, while different source refs
			// retain their own evidence.
			return name + "\x00source\x00" + string(args)
		}
		// Normal creation already receives previous_tail and recent summaries in
		// novel_context. Older/final/draft reads are a single bounded continuity
		// evidence slot, so only the newest result crosses the provider boundary.
		return name + "\x00continuity"
	default:
		return name + "\x00" + string(args)
	}
}

func protectRecentWriterResults(
	candidates []writerToolResultCandidate,
	keepRecent int,
	preferOriginalContext bool,
	dropNovelContext bool,
) map[int]struct{} {
	protected := make(map[int]struct{}, keepRecent)
	seen := make(map[string]struct{}, keepRecent)
	if preferOriginalContext && keepRecent > 1 {
		// The first active work package is authoritative for the turn. Keeping a
		// later duplicate also keeps the extra lookup rationale that led to it,
		// which can make an otherwise identical package cross the byte boundary.
		// Retain the original package and clear the redundant call/result instead.
		for _, candidate := range candidates {
			if candidate.toolName != "novel_context" || candidate.alreadyCleared {
				continue
			}
			protected[candidate.resultIndex] = struct{}{}
			seen[candidate.key] = struct{}{}
			break
		}
	}
	for index := len(candidates) - 1; index >= 0 && len(protected) < keepRecent; index-- {
		candidate := candidates[index]
		if candidate.alreadyCleared || (dropNovelContext && candidate.toolName == "novel_context") {
			continue
		}
		if _, duplicate := seen[candidate.key]; duplicate {
			continue
		}
		seen[candidate.key] = struct{}{}
		protected[candidate.resultIndex] = struct{}{}
	}
	return protected
}

// compactWriterToolHistory clears stale rationale while preserving schema-valid
// arguments for read/validation calls. Persisted write exchanges are removed as
// complete call/result pairs: their payload already lives in the Store, and
// leaving a fabricated argument object in history teaches weaker models to
// copy that invalid object into their next real tool call.
func compactWriterToolHistory(
	msgs []agentcore.AgentMessage,
	callIDs map[string]struct{},
	collapseCalls map[string]struct{},
	completedCalls map[string]struct{},
	writeReceipts map[string]string,
	clearedMessage string,
) ([]agentcore.AgentMessage, bool) {
	out := make([]agentcore.AgentMessage, 0, len(msgs))
	changed := false
	for _, item := range msgs {
		message, ok := item.(agentcore.Message)
		if !ok {
			out = append(out, item)
			continue
		}
		if message.Role == agentcore.RoleTool {
			callID, _ := message.Metadata["tool_call_id"].(string)
			if _, collapse := collapseCalls[callID]; collapse {
				changed = true
				continue
			}
			out = append(out, message)
			continue
		}
		if message.Role != agentcore.RoleAssistant {
			out = append(out, message)
			continue
		}

		content := append([]agentcore.ContentBlock(nil), message.Content...)
		allCallsCompacted := true
		allCallsCompleted := true
		hasCall := false
		collapsedCall := false
		for _, block := range content {
			if block.Type != agentcore.ContentToolCall || block.ToolCall == nil {
				continue
			}
			hasCall = true
			if _, compact := callIDs[block.ToolCall.ID]; !compact {
				allCallsCompacted = false
			}
			if _, completed := completedCalls[block.ToolCall.ID]; !completed {
				allCallsCompleted = false
			}
			if _, collapse := collapseCalls[block.ToolCall.ID]; collapse {
				collapsedCall = true
			}
		}

		messageChanged := false
		if collapsedCall || (hasCall && (allCallsCompacted || allCallsCompleted)) {
			filtered := make([]agentcore.ContentBlock, 0, len(content))
			var receiptTexts []string
			for _, block := range content {
				if block.Type == agentcore.ContentToolCall && block.ToolCall != nil {
					if _, collapse := collapseCalls[block.ToolCall.ID]; collapse {
						if receipt := writeReceipts[block.ToolCall.ID]; receipt != "" {
							receiptTexts = append(receiptTexts, receipt)
						}
						messageChanged = true
						continue
					}
				}
				// Once every call in this assistant turn has a result, its tool
				// arguments and receipts are the durable decision record. Keeping a
				// long private rationale beside that completed record can make the
				// very next request cross the byte boundary without adding evidence.
				if hasCall && allCallsCompleted && (block.Type == agentcore.ContentText || block.Type == agentcore.ContentThinking) {
					messageChanged = true
					continue
				}
				filtered = append(filtered, block)
			}
			content = filtered
			for _, receipt := range receiptTexts {
				content = append(content, agentcore.TextBlock(receipt))
			}
		}
		if hasCall && allCallsCompacted {
			if len(content) == 0 {
				content = append(content, agentcore.TextBlock(clearedMessage))
				messageChanged = true
			}
		}
		if messageChanged {
			message.Content = content
			message.Metadata = cloneWriterMetadata(message.Metadata)
			message.Metadata["compacted_tool_turn"] = true
			changed = true
		}
		out = append(out, message)
	}
	return out, changed
}

func cloneWriterMessages(msgs []agentcore.AgentMessage) []agentcore.AgentMessage {
	out := append([]agentcore.AgentMessage(nil), msgs...)
	for index, item := range out {
		message, ok := item.(agentcore.Message)
		if !ok {
			continue
		}
		message.Content = append([]agentcore.ContentBlock(nil), message.Content...)
		message.Metadata = cloneWriterMetadata(message.Metadata)
		out[index] = message
	}
	return out
}

func cloneWriterMetadata(metadata map[string]any) map[string]any {
	clone := make(map[string]any, len(metadata)+2)
	for key, value := range metadata {
		clone[key] = value
	}
	return clone
}

func hasToolResult(msgs []agentcore.AgentMessage, matches func(string) bool) bool {
	return hasMatchingToolResult(msgs, matches, false)
}

func hasSuccessfulToolResult(msgs []agentcore.AgentMessage, matches func(string) bool) bool {
	return hasMatchingToolResult(msgs, matches, true)
}

func hasMatchingToolResult(msgs []agentcore.AgentMessage, matches func(string) bool, requireSuccess bool) bool {
	pending := make(map[string]string)
	for _, item := range msgs {
		message, ok := item.(agentcore.Message)
		if !ok {
			continue
		}
		if message.Role == agentcore.RoleAssistant {
			for _, call := range message.ToolCalls() {
				pending[call.ID] = call.Name
			}
			continue
		}
		if message.Role != agentcore.RoleTool {
			continue
		}
		callID, _ := message.Metadata["tool_call_id"].(string)
		if matches(pending[callID]) && (!requireSuccess || message.Metadata["is_error"] != true) {
			return true
		}
	}
	return false
}

const writerCleanRecoveryMarker = "[Writer clean recovery]"
const writerToolErrorFingerprintMetadata = "writer_tool_error_fingerprint"

var errWriterCleanRecoveryExhausted = errors.New("writer repeated tool-contract errors after clean recovery; restart from durable state")

type writerCleanRecoveryDecision struct {
	restart   bool
	exhausted bool
}

func writerCleanRecoveryState(msgs []agentcore.AgentMessage) writerCleanRecoveryDecision {
	afterCleanRecovery := false
	for _, item := range msgs {
		message, ok := item.(agentcore.Message)
		if ok && message.Role == agentcore.RoleUser && strings.Contains(message.TextContent(), writerCleanRecoveryMarker) {
			afterCleanRecovery = true
			break
		}
	}
	counts := make(map[string]int)
	total := 0
	for _, candidate := range collectWriterToolResults(msgs) {
		if !candidate.isError {
			continue
		}
		total++
		fingerprint := candidate.errorFingerprint
		if fingerprint == "" {
			// Legacy compacted results predate persisted error fingerprints and
			// no longer contain enough detail to classify safely.
			if candidate.alreadyCleared {
				continue
			}
			fingerprint = writerToolErrorFingerprint(candidate.toolName, candidate.resultText)
		}
		counts[fingerprint]++
		if counts[fingerprint] >= 2 {
			return writerCleanRecoveryDecision{restart: !afterCleanRecovery, exhausted: afterCleanRecovery}
		}
	}
	if total >= 4 {
		return writerCleanRecoveryDecision{restart: !afterCleanRecovery, exhausted: afterCleanRecovery}
	}
	return writerCleanRecoveryDecision{}
}

func writerToolErrorFingerprint(toolName, result string) string {
	lower := strings.ToLower(result)
	switch {
	case strings.Contains(lower, "not an exact current-draft quote"):
		return toolName + ":exact_quote"
	case strings.Contains(lower, "scene_checks") && strings.Contains(lower, "count"):
		return toolName + ":scene_count"
	case strings.Contains(lower, "from_line/to_line"):
		return toolName + ":line_range"
	case strings.Contains(lower, "not an allowed dynamic character field"):
		return toolName + ":dynamic_character_field"
	case strings.Contains(lower, "character contract"):
		return toolName + ":character_contract"
	case strings.Contains(lower, "only available") || strings.Contains(lower, "must call"):
		return toolName + ":gate_order"
	case strings.Contains(lower, "invalid args") || strings.Contains(lower, "tool args invalid"):
		return toolName + ":invalid_args"
	default:
		return toolName + ":other"
	}
}

func cleanRestartWriterTurn(view []agentcore.AgentMessage) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	current := currentWriterTurn(view)
	if len(current) == 0 {
		return view, corecontext.StrategyResult{Name: "writer_clean_error_recovery"}, nil
	}
	clean := []agentcore.AgentMessage{current[0], agentcore.Message{
		Role: agentcore.RoleUser,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(writerCleanRecoveryMarker + `
Repeated tool-contract errors were detected. The failed arguments and repair conversation have been discarded.
Resume only from durable project state: call novel_context exactly once, obey its current_stage and next_step, and copy structured values from the latest successful tool receipt instead of memory.
If a draft already exists, do not re-plan or rewrite it. Read the current draft once only when the durable stage requires evidence, then continue the required validation/commit sequence.
Use each registered tool schema literally. Do not retry a rejected argument with synonyms.`)},
	}}
	return clean, corecontext.StrategyResult{
		Applied:     true,
		TokensSaved: max(0, corecontext.EstimateTotal(view)-corecontext.EstimateTotal(clean)),
		Name:        "writer_clean_error_recovery",
	}, nil
}

func countCompactedToolResults(msgs []agentcore.AgentMessage) int {
	count := 0
	for _, item := range msgs {
		message, ok := item.(agentcore.Message)
		if ok && message.Metadata["compacted_tool_result"] == true {
			count++
		}
	}
	return count
}

func logWriterPhaseRewrite(reason string, before, after []agentcore.AgentMessage) {
	slog.Warn("Writer validation phase advanced",
		"module", "context",
		"agent", "writer",
		"reason", reason,
		"strategy", "writer_validation_phase",
		"tokens_before", corecontext.EstimateTotal(before),
		"tokens_after", corecontext.EstimateTotal(after),
		"compacted", countCompactedToolResults(after),
	)
}
