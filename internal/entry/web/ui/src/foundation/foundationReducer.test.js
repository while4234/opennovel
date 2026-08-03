import { describe, expect, it } from 'vitest';
import { canApplyFoundation, createFoundationState, foundationReducer } from './foundationReducer.js';

const candidate = {
  schema_version: 1, revision: 1, premise: '基础',
  characters: [{ id: 'hero', name: '林舟', role: '主角', description: '', arc: '', traits: [] }],
  relationships: [], relationships_reviewed: true,
  world_rules: [{ id: 'rule', category: 'other', rule: '代价', boundary: '', strength: 'hard' }]
};
const response = (overrides = {}) => ({ foundation: { mode: 'normal', target_foundation: candidate, editable: true, base_revision: 1, base_audit_signature: 'audit', allowed_operations: ['get', 'preview', 'apply'], ...overrides } });

function loaded(overrides = {}) {
  return foundationReducer({ ...createFoundationState('p1'), requestVersion: 2 }, { type: 'load_success', projectId: 'p1', requestVersion: 2, response: response(overrides) });
}

describe('foundation reducer', () => {
  it('覆盖 loading/clean/dirty 且语义编辑清除 preview', () => {
    let state = loaded();
    expect(state.status).toBe('clean');
    state = foundationReducer(state, { type: 'edit', projectId: 'p1', requestVersion: 2, draft: { ...candidate, premise: '新前提' } });
    expect(state.status).toBe('dirty');
    state = { ...state, status: 'preview_ready', preview: { id: 'old' }, previewFingerprint: state.draftFingerprint };
    state = foundationReducer(state, { type: 'edit', projectId: 'p1', requestVersion: 2, draft: { ...state.draft, premise: '再次修改' } });
    expect(state.preview).toBeNull();
  });

  it('apply 只接受与当前 draft 一致且服务端 can_apply 的 preview', () => {
    let state = foundationReducer(loaded(), { type: 'edit', projectId: 'p1', requestVersion: 2, draft: { ...candidate, premise: '新前提' } });
    state = foundationReducer(state, { type: 'preview_start', projectId: 'p1', requestVersion: 2 });
    state = foundationReducer(state, { type: 'preview_success', projectId: 'p1', requestVersion: 2, preview: { id: 'preview-1', candidate: state.draft, can_apply: true } });
    expect(canApplyFoundation(state)).toBe(true);
    const applying = foundationReducer(state, { type: 'apply_start', projectId: 'p1', requestVersion: 2, idempotencyKey: 'same-key' });
    expect(applying.status).toBe('applying');
    expect(applying.applyIdempotencyKey).toBe('same-key');
    const rejected = foundationReducer({ ...state, draftFingerprint: 'changed' }, { type: 'apply_start', projectId: 'p1', requestVersion: 2, idempotencyKey: 'bad' });
    expect(rejected.status).toBe('preview_ready');
    expect(rejected.illegalAction).toMatch(/完全一致/);
  });

  it('stale 保留草稿并可绑定最新 base 再比较', () => {
    let state = foundationReducer(loaded(), { type: 'edit', projectId: 'p1', requestVersion: 2, draft: { ...candidate, premise: '用户未保存内容' } });
    state = foundationReducer(state, { type: 'preview_failed', projectId: 'p1', requestVersion: 2, error: { code: 'foundation_stale', message: 'stale' } });
    const latest = response({ base_revision: 2, target_foundation: { ...candidate, revision: 2, premise: '服务器新内容' } });
    state = foundationReducer(state, { type: 'stale_server_loaded', projectId: 'p1', requestVersion: 2, response: latest });
    state = foundationReducer(state, { type: 'rebase_stale', projectId: 'p1', requestVersion: 2 });
    expect(state.base.premise).toBe('服务器新内容');
    expect(state.draft.premise).toBe('用户未保存内容');
    expect(state.status).toBe('dirty');
  });

  it('active revision、readonly、failed/retry 与完成态映射正确', () => {
    expect(loaded({ active_revision: { stage: 'regenerating' }, editable: false }).status).toBe('regenerating');
    expect(loaded({ editable: false, readonly_reason: 'body_started' }).status).toBe('readonly');
    let failed = loaded({ active_revision: { stage: 'failed' }, editable: false, allowed_operations: ['get', 'retry'] });
    failed = foundationReducer(failed, { type: 'retry_start', projectId: 'p1', requestVersion: 2 });
    expect(failed.status).toBe('regenerating');
    const completed = foundationReducer(failed, { type: 'retry_success', projectId: 'p1', requestVersion: 2, revision: { stage: 'completed' } });
    expect(completed.status).toBe('completed');
    const dirty = foundationReducer(loaded(), { type: 'edit', projectId: 'p1', requestVersion: 2, draft: { ...candidate, premise: '保留我的草稿' } });
    const busy = foundationReducer(dirty, { type: 'busy_server_loaded', projectId: 'p1', requestVersion: 2, response: response({ editable: false, active_revision: { stage: 'regenerating' } }), error: { code: 'foundation_busy' } });
    expect(busy.status).toBe('regenerating');
    expect(busy.draft.premise).toBe('保留我的草稿');
  });

  it('project ID 与 request version fencing 丢弃迟到响应', () => {
    const state = loaded();
    const oldProject = foundationReducer(state, { type: 'load_failed', projectId: 'p2', requestVersion: 2, error: { message: 'late' } });
    const oldVersion = foundationReducer(state, { type: 'load_failed', projectId: 'p1', requestVersion: 1, error: { message: 'late' } });
    expect(oldProject).toBe(state);
    expect(oldVersion).toBe(state);
    const switched = foundationReducer(state, { type: 'load_start', projectId: 'p2', requestVersion: 3 });
    expect(switched.projectId).toBe('p2');
    expect(switched.status).toBe('loading');
  });

  it('Character workspace 轮询只更新 sidecar，绝不覆盖正在编辑的 Foundation draft', () => {
    let state = foundationReducer(loaded(), { type: 'edit', projectId: 'p1', requestVersion: 2, draft: { ...candidate, characters: [{ ...candidate.characters[0], voice: '用户草稿' }] } });
    state = foundationReducer(state, {
      type: 'character_workspace_success', projectId: 'p1', requestVersion: 2,
      response: { character_workspace: {
        current: { foundation: candidate }, candidate: { foundation: { ...candidate, characters: [{ ...candidate.characters[0], voice: '旧服务器候选' }] } },
        run: { run_id: 'run', mode: 'analyze', status: 'running', stage: 'running' }, completeness: [], findings: [], source_mappings: []
      } }
    });
    expect(state.draft.characters[0].voice).toBe('用户草稿');
    expect(state.characterWorkspace.candidate.foundation.characters[0].voice).toBe('旧服务器候选');
  });

  it('编辑后立即把已有 Character review 标为 stale', () => {
    let state = foundationReducer(loaded(), {
      type: 'character_workspace_success', projectId: 'p1', requestVersion: 2,
      response: { character_workspace: { current: { foundation: candidate }, completeness: [], findings: [{ id: 'finding', character_id: 'hero' }], source_mappings: [] } }
    });
    state = foundationReducer(state, { type: 'edit', projectId: 'p1', requestVersion: 2, draft: { ...candidate, premise: '修改后' } });
    expect(state.characterReviewStale).toBe(true);
    const polled = foundationReducer(state, {
      type: 'character_workspace_success', projectId: 'p1', requestVersion: 2, preserveReviewStale: true,
      response: { character_workspace: { current: { foundation: candidate }, completeness: [], findings: [], source_mappings: [] } }
    });
    expect(polled.characterReviewStale).toBe(true);
  });
});
