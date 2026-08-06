// @vitest-environment jsdom
import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ManuscriptWorkspace } from './ManuscriptWorkspace.jsx';
import * as api from './manuscript-api.js';

vi.mock('./manuscript-api.js');
vi.mock('../ManuscriptRevisionWorkbench.jsx', () => ({ ManuscriptRevisionWorkbench: () => <div>安全修订入口</div> }));
let root, container;
function WorkspaceFixture(props) {
  const [controlsTarget, setControlsTarget] = React.useState(null);
  return <><aside ref={setControlsTarget} /><ManuscriptWorkspace {...props} active controlsTarget={controlsTarget} /></>;
}
beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement('div'); document.body.append(container); root = createRoot(container);
  api.loadManuscriptRecovery.mockResolvedValue({ recovery: { required: false } });
  api.retryManuscriptRecovery.mockResolvedValue({ recovered: true, recovery: { required: false } });
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); vi.clearAllMocks(); });
const tree = { active_revision: { revision_id: 'revision-active', stage: 'audit_pending' }, nodes: [{ stable_id: 'v', display_label: '卷一', children: [{ stable_id: 'a', display_label: '弧一', children: [{ stable_id: 'c1', display_order: 1, display_label: '起点', state: 'completed', has_current: true, has_candidate: true, has_history: true }, { stable_id: 'c2', display_order: 2, display_label: '推进', state: 'writing', has_current: true }] }] }] };
const chapter = (id, view = 'current') => ({ chapter: { stable_id: id, view, content_signature: `${id}-${view}`, paragraphs: [`${id}-${view}`], next_cursor: null } });
async function settle() { await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); }); }

describe('ManuscriptWorkspace', () => {
  it('loads completed prose while writing and exposes ARIA tree/tabs/current-candidate copy', async () => {
    api.loadManuscriptTree.mockResolvedValue(tree); api.loadManuscriptChunk.mockImplementation(async (_p, id, options = {}) => chapter(id, options.view));
    await act(async () => root.render(<WorkspaceFixture projectId="p" />)); await settle();
    expect(container.querySelector('[role="tree"]')).not.toBeNull();
    expect(container.querySelector('[role="tablist"]')).not.toBeNull();
    expect(container.textContent).toContain('当前正式稿');
    expect(container.textContent).toContain('c1-current');
    await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent === '正文对比').click()); await settle();
    expect(container.textContent).toContain('候选稿'); expect(container.textContent).toContain('尚未发布'); expect(container.textContent).toContain('c1-candidate');
  });
  it('renders the complete chapter and enables chapter-end navigation only for completed formal prose', async () => {
    const completedTree = {
      ...tree,
      nodes: [{ ...tree.nodes[0], children: [{ ...tree.nodes[0].children[0], children: tree.nodes[0].children[0].children.map((item) => ({ ...item, state: 'completed', has_current: true, has_candidate: false })) }] }]
    };
    api.loadManuscriptTree.mockResolvedValue(completedTree);
    api.loadManuscriptChunk.mockImplementation(async (_p, id, options = {}) => chapter(id, options.view));
    await act(async () => root.render(<WorkspaceFixture projectId="p" />)); await settle();

    let navigation = container.querySelector('[aria-label="章末章节导航"]');
    expect(navigation.querySelectorAll('button')[0].disabled).toBe(true);
    expect(navigation.querySelectorAll('button')[1].disabled).toBe(false);
    await act(async () => navigation.querySelectorAll('button')[1].click()); await settle();
    expect(container.textContent).toContain('c2-current');

    navigation = container.querySelector('[aria-label="章末章节导航"]');
    expect(navigation.querySelectorAll('button')[0].disabled).toBe(false);
    expect(navigation.querySelectorAll('button')[1].disabled).toBe(true);
  });
  it('recovers an aborted outline load and keeps prose and outlines isolated across repeated chapter switches', async () => {
    api.loadManuscriptTree.mockResolvedValue(tree);
    api.loadManuscriptChunk.mockImplementation(async (_p, id, options = {}) => chapter(id, options.view));
    const abort = Object.assign(new Error('superseded'), { name: 'AbortError' });
    api.loadManuscriptArtifact
      .mockRejectedValueOnce(abort)
      .mockImplementation(async (_project, kind, stableId) => ({
        artifact: { kind, stable_id: stableId, signature: `${kind}-${stableId}`, content: { title: `OUTLINE-${stableId}`, core_event: `CORE-${stableId}`, hook: `HOOK-${stableId}`, scenes: [] } }
      }));
    await act(async () => root.render(<WorkspaceFixture projectId="p" />)); await settle();

    await act(async () => document.getElementById('manuscript-tab-outline').click()); await settle(); await settle();
    expect(container.textContent).toContain('OUTLINE-c1');
    expect(api.loadManuscriptArtifact.mock.calls.filter((call) => call[1] === 'outline' && call[2] === 'c1').length).toBeGreaterThanOrEqual(2);

    for (let round = 0; round < 2; round += 1) {
      await act(async () => container.querySelector('[data-tree-index="3"]').click()); await settle();
      expect(container.textContent).toContain('OUTLINE-c2');
      expect(container.textContent).not.toContain('OUTLINE-c1');
      await act(async () => document.getElementById('manuscript-tab-prose').click()); await settle();
      expect(container.textContent).toContain('c2-current');
      await act(async () => container.querySelector('[data-tree-index="2"]').click()); await settle();
      await act(async () => document.getElementById('manuscript-tab-outline').click()); await settle();
      expect(container.textContent).toContain('OUTLINE-c1');
      expect(container.textContent).not.toContain('OUTLINE-c2');
    }
  });
  it('cancels stale chapter reads so an old response cannot replace the new selection', async () => {
    let resolveOld; api.loadManuscriptTree.mockResolvedValue(tree); api.loadManuscriptChunk.mockImplementation((_p, id, options = {}) => id === 'c1' && options.view !== 'candidate' ? new Promise((resolve) => { resolveOld = resolve; }) : Promise.resolve(chapter(id, options.view)));
    await act(async () => root.render(<WorkspaceFixture projectId="p" />)); await settle();
    const second = [...container.querySelectorAll('button[role="treeitem"]')].find((b) => b.textContent?.includes('第 2 章'));
    await act(async () => second.click()); await settle();
    await act(async () => resolveOld(chapter('c1'))); await settle();
    expect(container.textContent).toContain('c2-current'); expect(container.textContent).not.toContain('c1-current');
  });
  it('loads the first page quickly and then completes the chapter in 100-paragraph batches', async () => {
    api.loadManuscriptTree.mockResolvedValue({ ...tree, nodes: [{ ...tree.nodes[0], children: [{ ...tree.nodes[0].children[0], children: [{ ...tree.nodes[0].children[0].children[0], has_candidate: false }] }] }] });
    api.loadManuscriptChunk.mockImplementation(async (_project, id, options = {}) => {
      if ((options.cursor || 0) === 0) return { chapter: { stable_id: id, view: 'current', content_signature: 'signed-current', paragraphs: Array.from({ length: 40 }, (_, index) => `p-${index + 1}`), next_cursor: 40, total_paragraphs: 140 } };
      return { chapter: { stable_id: id, view: 'current', content_signature: 'signed-current', paragraphs: Array.from({ length: 100 }, (_, index) => `p-${index + 41}`), next_cursor: null, total_paragraphs: 140 } };
    });
    await act(async () => root.render(<WorkspaceFixture projectId="p" />)); await settle();
    expect(container.textContent).toContain('已加载 140 / 140 段');
    expect(api.loadManuscriptChunk).toHaveBeenCalledWith('p', 'c1', expect.objectContaining({ cursor: 40, limit: 100 }));
    expect(container.querySelectorAll('.manuscript-prose p')).toHaveLength(140);
    expect(container.textContent).not.toContain('已显示本章真正结尾');
  });
  it('shows actionable manuscript recovery metadata in the right controls', async () => {
    api.loadManuscriptRecovery.mockResolvedValue({ recovery: { required: true, retryable: true, owners: ['manuscript_publication'] } });
    api.loadManuscriptTree.mockResolvedValue(tree); api.loadManuscriptChunk.mockImplementation(async (_p, id, options = {}) => chapter(id, options.view));
    await act(async () => root.render(<WorkspaceFixture projectId="p" />)); await settle();
    expect(container.textContent).toContain('正式稿件处于只读保护');
    expect(container.textContent).toContain('正文原子发布');
    await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent === '重新检查并恢复').click()); await settle();
    expect(api.retryManuscriptRecovery).toHaveBeenCalledWith('p', expect.any(AbortSignal));
    expect(container.textContent).toContain('正式稿件恢复已完成');
  });
  it.each([
    ['local mutation success', 'local', false],
    ['local mutation failure', 'local', true],
    ['SSE mutation success', 'sse', false],
    ['SSE mutation failure', 'sse', true]
  ])('keeps a newer chapter and its busy owner during delayed %s refresh', async (_name, sourceKind, failTree) => {
    const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };
    const oldEventSource = globalThis.EventSource;
    const sources = [];
    class FakeEventSource {
      constructor() { this.listeners = {}; sources.push(this); }
      addEventListener(name, listener) { this.listeners[name] = listener; }
      close() {}
      emit(detail) { this.listeners.action?.({ data: JSON.stringify(detail) }); }
    }
    globalThis.EventSource = FakeEventSource;
    const delayedTree = deferred(), delayedChapter = deferred();
    api.loadManuscriptTree.mockResolvedValue(tree);
    api.loadManuscriptChunk.mockImplementation((_project, id, options = {}) => id === 'c2' && options.view !== 'candidate' ? delayedChapter.promise : Promise.resolve(chapter(id, options.view)));
    try {
      await act(async () => root.render(<WorkspaceFixture projectId="p" />)); await settle();
      api.loadManuscriptTree.mockReturnValueOnce(delayedTree.promise);
      if (sourceKind === 'sse') {
        await act(async () => sources.at(-1).emit({ manuscript_mutation: { scope: 'prose_publish', stable_id: 'c1' } }));
      } else {
        await act(async () => window.dispatchEvent(new CustomEvent('ainovel:manuscript-mutated', { detail: { path: '/api/projects/p/manuscript/revision/command' } })));
      }
      await act(async () => container.querySelector('[data-tree-index="3"]').click()); await settle();
      await act(async () => failTree ? delayedTree.reject(new Error('STALE_TREE_ERROR')) : delayedTree.resolve(tree)); await settle();
      expect(container.querySelector('[data-tree-index="3"]').getAttribute('aria-selected')).toBe('true');
      expect(container.textContent).not.toContain('c1-current');
      expect(container.textContent).not.toContain('STALE_TREE_ERROR');
      expect(container.querySelector('[role="combobox"]').disabled).toBe(true);
      await act(async () => delayedChapter.resolve(chapter('c2'))); await settle();
      expect(container.textContent).toContain('c2-current');
      expect(container.querySelector('[role="combobox"]').disabled).toBe(false);
    } finally {
      globalThis.EventSource = oldEventSource;
    }
  });
  it('aborts and isolates all old project state when projectId changes', async () => {
    let resolveA, projectASignal;
    api.loadManuscriptTree.mockImplementation((project, options = {}) => project === 'A'
      ? new Promise((resolve) => { resolveA = resolve; projectASignal = options.signal; })
      : Promise.resolve({ ...tree, nodes: [{ ...tree.nodes[0], children: [{ ...tree.nodes[0].children[0], children: [{ ...tree.nodes[0].children[0].children[0], stable_id: 'b1', display_label: 'B chapter' }] }] }] }));
    api.loadManuscriptChunk.mockImplementation(async (_p, id, options = {}) => chapter(id, options.view));
    await act(async () => root.render(<WorkspaceFixture projectId="A" />)); await settle();
    await act(async () => root.render(<WorkspaceFixture projectId="B" />)); await settle();
    expect(projectASignal.aborted).toBe(true);
    await act(async () => resolveA(tree)); await settle();
    expect(container.textContent).toContain('b1-current');
    expect(container.textContent).not.toContain('c1-current');
    expect(api.invalidateManuscriptCache).toHaveBeenCalledWith('B');
  });
  it('keeps the last successful current prose when a same-chapter refresh fails', async () => {
    let currentLoads = 0;
    api.loadManuscriptTree.mockResolvedValue(tree);
    api.loadManuscriptChunk.mockImplementation(async (_p, id, options = {}) => {
      if (id === 'c1' && options.view !== 'candidate' && ++currentLoads > 1) throw new Error('temporary failure');
      return chapter(id, options.view);
    });
    await act(async () => root.render(<WorkspaceFixture projectId="p" />)); await settle();
    await act(async () => [...container.querySelectorAll('button[role="treeitem"]')].find((button) => button.textContent?.includes('第 1 章')).click()); await settle();
    expect(container.textContent).toContain('c1-current');
    expect(container.textContent).toContain('网络异常，保留上次成功正文');
  });
  it('keeps mobile drawer focus inside, closes with Escape, and restores the opener', async () => {
    api.loadManuscriptTree.mockResolvedValue(tree); api.loadManuscriptChunk.mockImplementation(async (_p, id, options = {}) => chapter(id, options.view));
    await act(async () => root.render(<WorkspaceFixture projectId="p" />)); await settle();
    const opener = container.querySelector('.manuscript-tree-open');
    await act(async () => opener.click()); await settle();
    const drawer = container.querySelector('[role="dialog"]');
    expect(drawer.getAttribute('aria-modal')).toBe('true');
    expect(document.activeElement).toBe(drawer.querySelector('[data-manuscript-drawer-initial]'));
    expect(container.querySelector('main').hasAttribute('inert')).toBe(true);
    await act(async () => drawer.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))); await settle();
    expect(drawer.getAttribute('aria-modal')).toBeNull();
    expect(document.activeElement).toBe(opener);
    expect(container.querySelector('main').hasAttribute('inert')).toBe(false);
  });
	it('keeps all manuscript operations in the manuscript panel without a discussion jump', async () => {
		api.loadManuscriptTree.mockResolvedValue(tree);
		api.loadManuscriptChunk.mockImplementation(async (_p, id, options = {}) => chapter(id, options.view));
		await act(async () => root.render(<WorkspaceFixture projectId="p" />)); await settle();
		expect(container.textContent).not.toContain('带当前上下文去讨论');
		expect(container.textContent).toContain('润色');
		expect(container.textContent).toContain('改写');
		expect(container.textContent).toContain('补剧情 / 扩写');
	});
	it('cursor-loads one signed history version and keeps its DOM window bounded', async () => {
		api.loadManuscriptTree.mockResolvedValue(tree);
		api.loadManuscriptChunk.mockImplementation(async (_p, id, options = {}) => chapter(id, options.view));
		api.loadManuscriptHistory.mockResolvedValue({ items: [{ revision_id: 'history-1', updated_at: '2026-07-16', stage: 'completed' }], has_more: false });
		const page = (cursor) => ({ chapter: { stable_id: 'c1', view: 'history', version_id: 'history-1', content_signature: 'history-signature', paragraphs: Array.from({ length: 120 }, (_, index) => `history-${cursor + index + 1}`), next_cursor: cursor === 0 ? 120 : null, total_paragraphs: 240 } });
		api.loadManuscriptVersion.mockImplementation(async (_project, _revision, _stable, cursor) => page(cursor));
		await act(async () => root.render(<WorkspaceFixture projectId="p" />)); await settle();
		await act(async () => [...container.querySelectorAll('[role="tab"]')].find((button) => button.textContent === '修订历史').click()); await settle();
		await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent.includes('2026-07-16')).click()); await settle();
		await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent === '继续加载').click()); await settle();
		expect(container.textContent).toContain('已加载 240 / 240 段');
		expect(container.querySelectorAll('.manuscript-prose p')).toHaveLength(240);
		expect(api.loadManuscriptVersion).toHaveBeenLastCalledWith('p', 'history-1', 'c1', 120, expect.any(AbortSignal));
	});
	it('recovers from a tombstoned history version without discarding current prose', async () => {
		api.loadManuscriptTree.mockResolvedValue(tree);
		api.loadManuscriptChunk.mockImplementation(async (_p, id, options = {}) => chapter(id, options.view));
		api.loadManuscriptHistory.mockResolvedValue({ items: [{ revision_id: 'history-gone', updated_at: '2026-07-16', stage: 'completed' }], has_more: false });
		const gone = Object.assign(new Error('historical version is no longer available'), { status: 410, data: { error: { code: 'version_gone', action: 'reload_history' } } });
		api.loadManuscriptVersion.mockRejectedValue(gone);
		await act(async () => root.render(<WorkspaceFixture projectId="p" />)); await settle();
		await act(async () => [...container.querySelectorAll('[role="tab"]')].find((button) => button.textContent === '修订历史').click()); await settle();
		await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent.includes('2026-07-16')).click()); await settle();
		expect(container.textContent).toContain('历史版本已被清理');
		expect(container.textContent).toContain('重新加载历史');
		expect(container.textContent).not.toContain('历史版本正文');
		await act(async () => [...container.querySelectorAll('[role="tab"]')].find((button) => button.textContent === '正文').click()); await settle();
		expect(container.textContent).toContain('c1-current');
	});
	it('cursor-loads review metadata beyond the first page', async () => {
		api.loadManuscriptTree.mockResolvedValue(tree);
		api.loadManuscriptChunk.mockImplementation(async (_p, id, options = {}) => chapter(id, options.view));
		api.loadManuscriptArtifact.mockResolvedValue({ artifact: { kind: 'review', stable_id: 'c1', signature: 'page-0', content: { status: 'completed', revisions: Array.from({ length: 20 }, (_, i) => ({ revision_id: `r-${i}` })), audits: [], next_cursor: 20, has_more: true } } });
		api.loadManuscriptReviewPage.mockImplementation(async (_project, _chapter, cursor) => {
			const revisions = Array.from({ length: Math.min(20, 126 - cursor) }, (_, index) => ({ revision_id: `r-${cursor + index}` }));
			return { artifact: { kind: 'review', stable_id: 'c1', signature: `page-${cursor}`, content: { status: 'completed', revisions, audits: cursor === 100 ? [{ revision_id: 'r-105', signature: 'audit-105', content_loaded: false }] : [], next_cursor: Math.min(126, cursor + revisions.length), has_more: cursor + revisions.length < 126 } } };
		});
		api.loadManuscriptReviewDetail.mockResolvedValue({ artifact: { kind: 'review_detail', stable_id: 'c1', signature: 'detail-105', content: { report: 'deep audit 105', findings: [] } } });
		await act(async () => root.render(<WorkspaceFixture projectId="p" />)); await settle();
		await act(async () => [...container.querySelectorAll('[role="tab"]')].find((button) => button.textContent === '审核').click()); await settle();
		expect(container.textContent).toContain('修订记录：20');
		await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent === '加载更多审核记录').click()); await settle();
		for (let page = 0; page < 5; page += 1) {
			await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent === '加载更多审核记录').click()); await settle();
		}
		expect(container.textContent).toContain('修订记录：126');
		expect(api.loadManuscriptReviewPage).toHaveBeenCalledWith('p', 'c1', 100, expect.any(AbortSignal));
		await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent === '加载审核报告与发现').click()); await settle();
		expect(container.textContent).toContain('deep audit 105');
	});
	it('isolates every selection-scoped response when a chapter changes', async () => {
		const pending = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };
		api.loadManuscriptTree.mockResolvedValue(tree);
		api.loadManuscriptChunk.mockImplementation(async (_p, id, options = {}) => chapter(id, options.view));
		const oldHistory = pending();
		api.loadManuscriptHistory.mockReturnValueOnce(oldHistory.promise);
		await act(async () => root.render(<WorkspaceFixture projectId="p" />)); await settle();
		await act(async () => document.getElementById('manuscript-tab-history').click()); await settle();
		await act(async () => container.querySelector('[data-tree-index="3"]').click()); await settle();
		await act(async () => oldHistory.resolve({ items: [{ revision_id: 'stale-history', updated_at: '2099-01-01' }], has_more: false })); await settle();
		expect(container.querySelector('[data-tree-index="3"]').getAttribute('aria-selected')).toBe('true');
		expect(container.textContent).not.toContain('2099-01-01');
		await act(async () => container.querySelector('[data-tree-index="2"]').click()); await settle();
		api.loadManuscriptHistory.mockResolvedValueOnce({ items: [{ revision_id: 'old-version', updated_at: '2098-01-01', stage: 'completed' }], has_more: false });
		await act(async () => document.getElementById('manuscript-tab-history').click()); await settle();
		const oldVersion = pending();
		api.loadManuscriptVersion.mockReturnValueOnce(oldVersion.promise);
		await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent.includes('2098-01-01')).click()); await settle();
		await act(async () => container.querySelector('[data-tree-index="3"]').click()); await settle();
		await act(async () => oldVersion.resolve({ chapter: { stable_id: 'c1', version_id: 'old-version', content_signature: 'stale-version', paragraphs: ['STALE_VERSION'], next_cursor: null } })); await settle();
		expect(container.textContent).not.toContain('STALE_VERSION');

		await act(async () => container.querySelector('[data-tree-index="2"]').click()); await settle();
		const oldArtifact = pending();
		api.loadManuscriptArtifact.mockReturnValueOnce(oldArtifact.promise);
		await act(async () => document.getElementById('manuscript-tab-outline').click()); await settle();
		await act(async () => container.querySelector('[data-tree-index="3"]').click()); await settle();
		await act(async () => oldArtifact.resolve({ artifact: { kind: 'outline', stable_id: 'c1', content: { title: 'STALE_ARTIFACT' } } })); await settle();
		expect(container.textContent).not.toContain('STALE_ARTIFACT');

		await act(async () => container.querySelector('[data-tree-index="2"]').click()); await settle();
		api.loadManuscriptArtifact.mockResolvedValueOnce({ artifact: { kind: 'review', stable_id: 'c1', content: { status: 'completed', revisions: [], audits: [{ revision_id: 'old-review', signature: 'old-signature' }], has_more: false } } });
		await act(async () => document.getElementById('manuscript-tab-review').click()); await settle();
		const oldReview = pending();
		api.loadManuscriptReviewDetail.mockReturnValueOnce(oldReview.promise);
		await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent === '加载审核报告与发现').click()); await settle();
		await act(async () => container.querySelector('[data-tree-index="3"]').click()); await settle();
		await act(async () => oldReview.reject(new Error('STALE_REVIEW_ERROR'))); await settle();
		expect(container.textContent).not.toContain('STALE_REVIEW_ERROR');
	});
});
