import { describe, expect, it } from 'vitest';
import {
  auditRunLabel,
  buildSemanticAuditRequest,
  normalizeAuditComparison,
  normalizeAuditRunList,
  semanticAuditProgress,
  semanticAuditTerminal
} from './audit-runs.js';

describe('audit run UI contracts', () => {
  it('normalizes and deduplicates immutable audit runs', () => {
    const runs = normalizeAuditRunList({ runs: [
      { run_id: 'one', audit_kind: 'contract', report_status: 'pass' },
      { run_id: 'one', kind: 'model_second_pass', status: 'fail' },
      { run_id: 'two', kind: 'model_second_pass', status: 'completed', evaluator: { model: 'strong' } }
    ] });
    expect(runs).toHaveLength(2);
    expect(auditRunLabel(runs[1])).toContain('strong');
  });

  it('keeps comparison categories deterministic', () => {
    const result = normalizeAuditComparison({ level: 'exact', groups: { introduced: [{ fingerprint: 'a' }] } });
    expect(result.comparability).toBe('exact');
    expect(result.groups.introduced).toHaveLength(1);
    expect(result.groups.resolved).toEqual([]);
  });

  it('validates semantic audit scope and cost', () => {
    expect(buildSemanticAuditRequest({ sourceTo: '12', maxCostUsd: '3.5', provider: 'p', model: 'm' })).toMatchObject({
      ok: true,
      request: { source_to: 12, max_cost_usd: 3.5, provider: 'p', model: 'm' }
    });
    expect(buildSemanticAuditRequest({ model: 'm' }).ok).toBe(false);
    expect(buildSemanticAuditRequest({ sourceTo: 'x' }).ok).toBe(false);
    expect(buildSemanticAuditRequest({ maxCostUsd: '0' }).ok).toBe(false);
  });

  it('tracks progress and terminal states', () => {
    expect(semanticAuditProgress({ completed_units: 3, total_units: 4 }).percent).toBe(75);
    expect(semanticAuditTerminal('stale')).toBe(true);
    expect(semanticAuditTerminal('interrupted')).toBe(true);
    expect(semanticAuditTerminal('running')).toBe(false);
  });
});
