// @vitest-environment jsdom

import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { renderToStaticMarkup } from 'react-dom/server';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  commandManuscriptRevision,
  getManuscriptChapter,
  getManuscriptRevisionBatches,
  getManuscriptTree,
  previewManuscriptRevision
} from './api.js';
import { isTerminalManuscriptRevision, ManuscriptRevisionWorkbench, resolveRefreshedManuscriptRevision } from './ManuscriptRevisionWorkbench.jsx';

vi.mock('./api.js', () => ({
  commandManuscriptRevision: vi.fn(),
  getManuscriptChapter: vi.fn(),
  getManuscriptRevisionBatches: vi.fn(),
  getManuscriptTree: vi.fn(),
  previewManuscriptRevision: vi.fn()
}));

let root;
let container;

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

async function settle() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe('ManuscriptRevisionWorkbench', () => {
  it('exposes the complete approval flow with accessible current/candidate regions', () => {
    const html = renderToStaticMarkup(<ManuscriptRevisionWorkbench projectId="project-1" />);
    for (const label of ['生成签名预览', '生成候选', '独立审核', '批准', '原子发布', '取消', '刷新/恢复', '当前正式正文', '候选正文']) {
      expect(html).toContain(label);
    }
    expect(html).toContain('aria-live="polite"');
    expect(html).toContain('aria-label="选择稳定章节"');
		expect(html).toContain('签名审核结果');
  });

	it('releases completed and cancelled runtime state for a second revision', () => {
		expect(isTerminalManuscriptRevision({ stage: 'completed' })).toBe(true);
		expect(isTerminalManuscriptRevision({ stage: 'cancelled' })).toBe(true);
		expect(isTerminalManuscriptRevision({ stage: 'failed' })).toBe(false);
		for (const terminal of ['completed', 'cancelled']) {
			const second = { revision_id: `second-${terminal}`, revision: 1, stage: 'approval_pending' };
			expect(resolveRefreshedManuscriptRevision({ revision_id: 'first', stage: terminal }, second)).toEqual(second);
		}
	});

	it('keeps durable recovery classification after refresh', () => {
		const refreshed = resolveRefreshedManuscriptRevision(
			{ revision_id: 'active', revision: 2, stage: 'failed' },
			null,
			{ revision: 3, stage: 'failed', publication_status: 'none', last_error_class: 'auditor_failure', recovery_class: 'auditor_failure', queue: [], batches: [] }
		);
		expect(refreshed.recovery_class).toBe('auditor_failure');
	});

  it.each([
    ['completed', 'publish', '原子发布'],
    ['cancelled', 'cancel', '取消']
  ])('mounts the %s refresh flow and starts a second revision', async (terminalStage, action, buttonLabel) => {
    const chapterId = 'ch_0123456789abcdef0123456789abcdef';
    const first = { revision_id: `first-${terminalStage}`, revision: 7, stage: action === 'publish' ? 'ready_to_publish' : 'approval_pending', publication_status: 'none', baseline: { chapter_id: chapterId }, queue: [], recovery_class: 'publication_recovery_required' };
    const terminal = { ...first, revision: 8, stage: terminalStage, publication_status: terminalStage === 'completed' ? 'completed' : 'none' };
    const second = { revision_id: `second-${terminalStage}`, revision: 1, stage: 'approval_pending', publication_status: 'none', baseline: { chapter_id: chapterId }, queue: [], recovery_class: 'auditor_failure' };
    let active = first;
    getManuscriptTree.mockImplementation(async () => ({ tree: [{ arcs: [{ chapters: [{ id: chapterId, chapter: 1, title: '第一章' }] }] }], active_revision: active }));
    getManuscriptChapter.mockResolvedValue({ chapter: { content: '正式正文' } });
    getManuscriptRevisionBatches.mockImplementation(async (_projectId, revisionId) => ({ ...(revisionId === second.revision_id ? second : first), candidate_views: [] }));
    commandManuscriptRevision.mockImplementation(async (_projectId, payload) => {
      expect(payload.action).toBe(action);
      active = null;
      return { revision: terminal };
    });
    previewManuscriptRevision.mockImplementation(async () => {
      active = second;
      return { preview: { runtime: second } };
    });

    await act(async () => root.render(<ManuscriptRevisionWorkbench projectId="project-1" />));
    const details = container.querySelector('details');
    await act(async () => {
      details.open = true;
      details.dispatchEvent(new Event('toggle', { bubbles: true }));
    });
    await settle();
    expect(container.querySelector('[role="alert"]').textContent).toContain('publication_recovery_required');

    const terminalButton = [...container.querySelectorAll('button')].find((button) => button.textContent === buttonLabel);
    await act(async () => terminalButton.click());
    await settle();
    expect(container.querySelector('[role="status"]').textContent).toContain(`上一修订已${terminalStage}`);

    const instruction = container.querySelector('textarea');
    await act(async () => {
	  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(instruction, '开始第二次修订');
      instruction.dispatchEvent(new Event('input', { bubbles: true }));
    });
    const previewButton = [...container.querySelectorAll('button')].find((button) => button.textContent === '生成签名预览');
    expect(previewButton.disabled).toBe(false);
    await act(async () => previewButton.click());
    await settle();
    expect(getManuscriptRevisionBatches).toHaveBeenCalledWith('project-1', second.revision_id);
    expect(container.querySelector('[role="alert"]').textContent).toContain('auditor_failure');
  });
});
