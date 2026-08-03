// @vitest-environment jsdom
import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createExpansionOperationRegistry, ExpansionLauncher, reconcileExpansionRevision } from './ExpansionLauncher.jsx';
import * as api from './manuscript-api.js';

vi.mock('./manuscript-api.js');
let root, container;
beforeEach(() => { globalThis.IS_REACT_ACT_ENVIRONMENT = true; container = document.createElement('div'); document.body.append(container); root = createRoot(container); });
afterEach(async () => { await act(async () => root.unmount()); container.remove(); vi.resetAllMocks(); });
const preview = { preview_id: 'exp_1', base_revision: 7, mode: 'normal', form: 'insert_one', reason: '需要独立选择与代价', location: 'after', chapter_count: 1, chapter_min_words: 2200, chapter_max_words: 3600, total_min_words: 2200, total_max_words: 3600, assessment: { goal: '取证', conflict: '信任破裂', choice: '公开', cost: '失去盟友', result: '新联盟', character_stage_change: '从被动到主动', volume_pacing_effect: '中点转折' }, impacts: [{ level: 'required', change: '更新相邻提纲', evidence: ['因果承接'] }] };
async function settle() { await act(async () => { await Promise.resolve(); await Promise.resolve(); }); }

describe('ExpansionLauncher', () => {
	it.each(['approve', 'feedback', 'request_audit', 'publish'])('keeps the %s key across lost response and poll/SSE revision advancement until explicit completion', (action) => {
		let sequence = 0;
		const registry = createExpansionOperationRegistry(() => `key-${++sequence}`);
		const message = action === 'feedback' ? 'repair this finding' : '';
		const identityBeforePoll = JSON.stringify(['p', message]);
		const first = registry.acquire(`revision-${action}`, identityBeforePoll, { expected_revision: 7, message });
		// Poll/SSE advanced the server revision from 7 to 8 after the response
		// was lost. The logical identity deliberately excludes that revision.
		const retry = registry.acquire(`revision-${action}`, JSON.stringify(['p', message]), { expected_revision: 8, message });
		expect(retry.key).toBe(first.key);
		expect(retry.payload).toEqual({ expected_revision: 7, message });
		registry.complete(retry);
		const nextLogicalOperation = registry.acquire(`revision-${action}`, JSON.stringify(['p', message]));
		expect(nextLogicalOperation.key).not.toBe(first.key);
	});

	it('keeps feedback and feedback-adjust keys stable independently across lost responses and poll/SSE updates', () => {
		let sequence = 0;
		const registry = createExpansionOperationRegistry(() => `key-${++sequence}`);
		const feedback = registry.acquire('revision-feedback', JSON.stringify(['p', 'repair']));
		expect(registry.acquire('revision-feedback', JSON.stringify(['p', 'repair'])).key).toBe(feedback.key);
		registry.complete(feedback);
		const repairFingerprint = JSON.stringify(['p', 'preview-1', 7, 'repair sentence']);
		const adjust = registry.acquire('feedback-repair', repairFingerprint);
		expect(registry.acquire('feedback-repair', repairFingerprint).key).toBe(adjust.key);
		registry.complete(adjust);
		expect(registry.acquire('feedback-repair', repairFingerprint).key).not.toBe(adjust.key);
	});

	it('resumes an accepted feedback repair with the exact frozen adjust payload after its response is lost', async () => {
		api.planManuscriptExpansion.mockResolvedValue({ preview });
		api.confirmManuscriptExpansion.mockResolvedValue({ confirmation: { preview_id: preview.preview_id, revision: { revision: 7, stage: 'candidate_audit_pending', findings: ['repair causal link'] } } });
		api.commandExpansionRevision.mockResolvedValue({ revision: { revision: 8, stage: 'candidate_generating', approval_stage: 'structure' } });
		api.adjustManuscriptExpansion.mockRejectedValueOnce(new Error('repair response lost')).mockResolvedValueOnce({ preview: { ...preview, preview_id: 'exp_repaired' } });
		api.getExpansionRevision.mockImplementation(() => new Promise(() => {}));
		await act(async () => root.render(<ExpansionLauncher projectId="p" phase="writing" structureRevision={7} structureSignature="structure-sha" selectedId="ch-1" />));
		await act(async () => container.querySelector('button').click());
		await act(async () => container.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))); await settle();
		await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent.includes('确认影响')).click()); await settle();
		await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent.includes('提交定向反馈')).click()); await settle();
		const firstRepair = api.adjustManuscriptExpansion.mock.calls[0][1];
		api.getExpansionRevision.mockResolvedValue({ revision: { revision: 8, stage: 'candidate_generating', approval_stage: 'structure' } });
		await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent === '重试').click()); await settle();
		const retryRepair = api.adjustManuscriptExpansion.mock.calls[1][1];
		expect(api.commandExpansionRevision).toHaveBeenCalledTimes(1);
		expect(api.commandExpansionRevision.mock.calls[0].slice(1, 5)).toEqual(['feedback', 7, expect.any(String), '请按当前审核发现定向修复']);
		expect(retryRepair).toEqual(firstRepair);
	});
	it('reuses the exact idempotency key after a lost plan response and rotates it for a new operation', async () => {
		api.planManuscriptExpansion.mockRejectedValueOnce(new Error('response lost')).mockResolvedValueOnce({ preview }).mockResolvedValueOnce({ preview: { ...preview, preview_id: 'exp_3' } });
		await act(async () => root.render(<ExpansionLauncher projectId="p" phase="writing" mode="normal" structureRevision={7} structureSignature="structure-sha" selectedId="ch-1" />));
		await act(async () => container.querySelector('button').click());
		const textarea = container.querySelector('textarea');
		await act(async () => { textarea.value = 'stable retry'; textarea.dispatchEvent(new Event('input', { bubbles: true })); });
		await act(async () => container.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))); await settle();
		const firstKey = api.planManuscriptExpansion.mock.calls[0][1].idempotency_key;
		await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent === '重试').click()); await settle();
		expect(api.planManuscriptExpansion.mock.calls[1][1].idempotency_key).toBe(firstKey);
		await act(async () => { textarea.value = 'new logical operation'; textarea.dispatchEvent(new Event('input', { bubbles: true })); });
		await act(async () => container.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))); await settle();
		expect(api.planManuscriptExpansion.mock.calls[2][1].idempotency_key).not.toBe(firstKey);
	});

  it('keeps a completed command result visible after active-revision polling returns empty', () => {
    const completed = { revision_id: 'rev-completed', revision: 12, stage: 'completed', terminal: true };
    expect(reconcileExpansionRevision(completed, null)).toBe(completed);
    expect(reconcileExpansionRevision({ stage: 'candidate_audit_pending', terminal: false }, null)).toBeNull();
  });

  it('keeps the default form minimal and plans, adjusts, confirms, and cancels through signed preview IDs', async () => {
    api.planManuscriptExpansion.mockResolvedValue({ preview });
    api.adjustManuscriptExpansion.mockResolvedValue({ preview: { ...preview, preview_id: 'exp_2', form: 'insert_multiple' } });
    api.confirmManuscriptExpansion.mockResolvedValue({ confirmation: { preview_id: 'exp_2', revision: { id: 'rev-1' } } });
    api.cancelManuscriptExpansion.mockResolvedValue({ preview: { ...preview, preview_id: 'exp_2', cancelled: true } });
    await act(async () => root.render(<ExpansionLauncher projectId="p" phase="writing" mode="normal" structureRevision={7} structureSignature="structure-sha" selectedId="ch-1" />));
    await act(async () => container.querySelector('button').click());
    expect(container.querySelector('details').open).toBe(false);
    expect(container.textContent).toContain('原创模式：不会读取或携带原著字段');
    const textarea = container.querySelector('textarea');
    await act(async () => { textarea.value = '让盟友隐瞒的证据迫使主角公开站队'; textarea.dispatchEvent(new Event('input', { bubbles: true })); });
    await act(async () => container.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))); await settle();
    expect(api.planManuscriptExpansion).toHaveBeenCalledWith('p', expect.objectContaining({ reference_ids: ['ch-1'], expected_structure_revision: 7, expected_structure_signature: 'structure-sha' }), expect.any(AbortSignal));
    expect(container.textContent).toContain('需要独立选择与代价');
    await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent === '更充分').click()); await settle();
    expect(api.adjustManuscriptExpansion).toHaveBeenCalledWith('p', expect.objectContaining({ preview_id: 'exp_1', adjustment: 'full' }), expect.any(AbortSignal));
    await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent.includes('确认影响')).click()); await settle();
    expect(api.confirmManuscriptExpansion).toHaveBeenCalledWith('p', expect.objectContaining({ preview_id: 'exp_2', expected_revision: 7 }), expect.any(AbortSignal));
    expect(container.textContent).toContain('修订进度');
    await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent === '取消预览').click()); await settle();
    expect(api.cancelManuscriptExpansion).toHaveBeenCalledWith('p', 'exp_2', 7, expect.any(String), expect.any(AbortSignal));
  });

  it('opens the between-chapter entry, exposes adaptation copy, and blocks confirmation when a revision is active', async () => {
    await act(async () => root.render(<ExpansionLauncher projectId="p" phase="complete" mode="adaptation" structureSignature="sha" launchRequest={{ location: 'after', referenceIds: ['ch-a'], nonce: 1 }} activeRevision={{ revision_id: 'rev-active' }} />));
    expect(container.textContent).toContain('继续扩写');
    expect(container.textContent).toContain('改编模式：会校验原著覆盖与受保护合同');
    expect(container.textContent).toContain('已有修订正在进行');
  });
});
