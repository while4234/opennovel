export function normalizeAuditRunSummary(run) {
  if (!run || typeof run !== 'object') return null;
  const runId = String(run.run_id || '').trim();
  if (!runId) return null;
  return {
    ...run,
    run_id: runId,
    kind: String(run.kind || run.audit_kind || 'contract'),
    status: String(run.status || run.report_status || 'unknown'),
    created_at: String(run.created_at || run.started_at || ''),
    completed_at: String(run.completed_at || ''),
    finding_count: Number(run.finding_count || 0),
    blocking_count: Number(run.blocking_count || 0),
    scope: run.scope || run.effective_scope || {},
    evaluator: run.evaluator || run.model || null
  };
}

export function normalizeAuditRunList(data) {
  const source = Array.isArray(data) ? data : (data?.runs || data?.items || []);
  const seen = new Set();
  return source
    .map(normalizeAuditRunSummary)
    .filter((run) => run && !seen.has(run.run_id) && seen.add(run.run_id));
}

export function auditRunLabel(run) {
  const normalized = normalizeAuditRunSummary(run);
  if (!normalized) return '未知审计';
  const model = normalized.evaluator?.model || normalized.evaluator?.model_name || '';
  const timestamp = normalized.completed_at || normalized.created_at;
  const timeLabel = timestamp ? new Date(timestamp).toLocaleString() : '时间未知';
  const kindLabel = normalized.kind === 'model_second_pass' ? '强模型二审' : normalized.kind === 'hybrid' ? '综合审计' : '合同审计';
  return `${timeLabel} · ${kindLabel}${model ? ` · ${model}` : ''} · ${normalized.status}`;
}

export function normalizeAuditComparison(comparison) {
  if (!comparison || typeof comparison !== 'object') return null;
  const groups = {};
  for (const key of ['introduced', 'resolved', 'worsened', 'improved', 'explanation_changed', 'unchanged']) {
    groups[key] = Array.isArray(comparison[key])
      ? comparison[key]
      : (Array.isArray(comparison.groups?.[key]) ? comparison.groups[key] : []);
  }
  for (const change of Array.isArray(comparison.changes) ? comparison.changes : []) {
    const key = String(change?.change || 'unchanged');
    if (groups[key]) groups[key].push(change);
  }
  const inferredComparability = comparison.confidence === 'context_changed'
    ? 'content_changed'
    : comparison.confidence === 'partial'
      ? 'scope_changed'
      : comparison.attributable_to_model
        ? 'exact'
        : 'incompatible';
  return {
    ...comparison,
    comparability: String(comparison.comparability || comparison.level || inferredComparability),
    groups
  };
}

export function buildSemanticAuditRequest(state = {}) {
  const sourceTo = String(state.sourceTo || '').trim();
  const maxCost = String(state.maxCostUsd || '').trim();
  const maxCalls = String(state.maxCalls || '').trim();
  const request = {
    provider: String(state.provider || '').trim(),
    model: String(state.model || '').trim(),
    reasoning_effort: String(state.reasoningEffort || '').trim(),
    acknowledge_unknown_price: state.acknowledgeUnknownPrice === true,
    quality_mode: 'full_coverage'
  };
  if ((request.provider && !request.model) || (!request.provider && request.model)) {
    return { ok: false, error: '临时覆盖时必须同时选择 Provider 和 Model' };
  }
  if (sourceTo) {
    const parsed = Number(sourceTo);
    if (!Number.isInteger(parsed) || parsed < 1) return { ok: false, error: '原著结束章必须是正整数' };
    request.source_to = parsed;
  }
  if (maxCost) {
    const parsed = Number(maxCost);
    if (!Number.isFinite(parsed) || parsed <= 0) return { ok: false, error: '费用上限必须大于 0' };
    request.max_cost_usd = parsed;
  }
  if (maxCalls) {
    const parsed = Number(maxCalls);
    if (!Number.isInteger(parsed) || parsed < 1) return { ok: false, error: '调用次数上限必须是正整数' };
    request.max_calls = parsed;
  }
  return { ok: true, request };
}

export function semanticAuditProgress(run) {
  const completed = Number(run?.completed_units || run?.progress?.completed_calls || 0);
  const total = Number(run?.total_units || run?.progress?.total_calls || 0);
  const percent = total > 0 ? Math.min(100, Math.round((completed / total) * 100)) : 0;
  return { completed, total, percent };
}

export function semanticAuditTerminal(status) {
  return ['completed', 'failed', 'canceled', 'interrupted', 'stale'].includes(String(status || '').toLowerCase());
}
