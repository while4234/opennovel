// @vitest-environment jsdom
import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ManuscriptActionPanel } from './ManuscriptActionPanel.jsx';
import { ManuscriptManualEditor } from './ManuscriptManualEditor.jsx';
import * as api from './manuscript-api.js';

vi.mock('./manuscript-api.js');
vi.mock('../ManuscriptRevisionWorkbench.jsx', () => ({ ManuscriptRevisionWorkbench: ({ initialRevision }) => <div data-testid="revision-safety">安全修订：{initialRevision?.revision_id}</div> }));
vi.mock('./ExpansionLauncher.jsx', () => ({ ExpansionLauncher: ({ initialPreview }) => <div data-testid="expansion-safety">安全扩写：{initialPreview?.preview_id}</div> }));

let container;
let root;
const props = {
  projectId: 'project-1', selectedId: 'ch-1', current: { content_signature: 'prose-sha' }, phase: 'writing', mode: 'normal',
  structureRevision: 7, structureSignature: 'structure-sha', activeRevision: null,
};
const readyDialogue = { id: 'dialogue-1', type: 'polish', status: 'ready', chapter_id: 'ch-1', original_chapter_label: '第 1 章', version: 2, round: 1, messages: [{ role: 'user', content: '压缩重复' }], resolved_instruction: '保留事实，压缩重复' };

async function settle() {
  await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
}

function setTextareaValue(textarea, value) {
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(textarea, value);
  textarea.dispatchEvent(new Event('input', { bubbles: true }));
}

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement('div'); document.body.append(container); root = createRoot(container);
  api.loadActiveManuscriptActionDialogue.mockResolvedValue({ dialogue: null });
});

afterEach(async () => {
  await act(async () => root.unmount()); container.remove(); vi.resetAllMocks();
});

describe('ManuscriptActionPanel', () => {
  it('keeps direct-ready execution and the signed revision preview inside the manuscript panel', async () => {
    const changed = vi.fn();
    api.createManuscriptActionDialogue.mockResolvedValue({ dialogue: readyDialogue });
    api.executeManuscriptActionDialogue.mockResolvedValue({ dialogue: { ...readyDialogue, status: 'completed', version: 4, result: { kind: 'revision', preview: { runtime: { revision_id: 'revision-1' } } } } });
    await act(async () => root.render(<ManuscriptActionPanel {...props} onChanged={changed} />)); await settle();
    const textarea = container.querySelector('.manuscript-action-form textarea');
    await act(async () => setTextareaValue(textarea, '压缩重复'));
    await act(async () => container.querySelector('.manuscript-action-form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))); await settle();
    expect(api.createManuscriptActionDialogue).toHaveBeenCalledWith('project-1', expect.objectContaining({ chapter_id: 'ch-1', content_signature: 'prose-sha', type: 'polish', initial_input: '压缩重复' }), expect.any(AbortSignal));
    expect(container.textContent).toContain('意见已明确，可以生成安全预览');
    await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent === '生成签名预览').click()); await settle();
    expect(container.textContent).toContain('安全修订：revision-1');
    expect(container.textContent).not.toContain('带当前上下文去讨论');
    expect(changed).toHaveBeenCalled();
  });

  it('answers material AI questions in place and recovers the same dialogue after refresh', async () => {
    const needsInput = { id: 'dialogue-2', type: 'rewrite', status: 'needs_input', chapter_id: 'ch-1', original_chapter_label: '第 1 章', version: 3, round: 1, messages: [{ role: 'assistant', content: '范围会影响剧情' }], questions: [{ id: 'r1-scope', prompt: '只改冲突场景，还是整章？' }] };
    api.loadActiveManuscriptActionDialogue.mockResolvedValue({ dialogue: needsInput });
    api.replyManuscriptActionDialogue.mockResolvedValue({ dialogue: { ...readyDialogue, id: 'dialogue-2', type: 'rewrite', version: 4 } });
    await act(async () => root.render(<ManuscriptActionPanel {...props} />)); await settle();
    expect(container.textContent).toContain('只改冲突场景，还是整章？');
    const answer = container.querySelector('.manuscript-action-dialogue textarea');
    await act(async () => setTextareaValue(answer, '只改冲突场景'));
    await act(async () => answer.closest('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))); await settle();
    expect(api.replyManuscriptActionDialogue).toHaveBeenCalledWith('project-1', 'dialogue-2', expect.objectContaining({ question_id: 'r1-scope', answer: '只改冲突场景', expected_version: 3 }), expect.any(AbortSignal));
    expect(container.textContent).toContain('意见已明确，可以生成安全预览');
  });

  it('reuses the exact create idempotency key after a lost response', async () => {
    api.createManuscriptActionDialogue.mockRejectedValueOnce(new Error('response lost')).mockResolvedValueOnce({ dialogue: readyDialogue });
    await act(async () => root.render(<ManuscriptActionPanel {...props} />)); await settle();
    const textarea = container.querySelector('.manuscript-action-form textarea');
    await act(async () => setTextareaValue(textarea, '压缩重复'));
    const submit = () => container.querySelector('.manuscript-action-form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await act(async () => submit()); await settle();
    await act(async () => submit()); await settle();
    expect(api.createManuscriptActionDialogue).toHaveBeenCalledTimes(2);
    expect(api.createManuscriptActionDialogue.mock.calls[1][1].idempotency_key).toBe(api.createManuscriptActionDialogue.mock.calls[0][1].idempotency_key);
  });

  it('keeps an active dialogue bound to its original chapter', async () => {
    const returnChapter = vi.fn();
    api.loadActiveManuscriptActionDialogue.mockResolvedValue({ dialogue: { ...readyDialogue, chapter_id: 'ch-origin', original_chapter_label: '第 3 章' } });
    api.cancelManuscriptActionDialogue.mockResolvedValue({ dialogue: { ...readyDialogue, status: 'cancelled', chapter_id: 'ch-origin' } });
    await act(async () => root.render(<ManuscriptActionPanel {...props} onReturnChapter={returnChapter} />)); await settle();
    expect(container.textContent).toContain('操作仍绑定在第 3 章');
    await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent === '返回原章节').click());
    expect(returnChapter).toHaveBeenCalledWith('ch-origin');
  });

  it('opens manual editing in the central prose workspace instead of the side panel', async () => {
    const openEditor = vi.fn();
    await act(async () => root.render(<ManuscriptActionPanel {...props} onManualEdit={openEditor} />)); await settle();
    await act(async () => [...container.querySelectorAll('[role="tab"]')].find((button) => button.textContent === '手动编辑').click());
    expect(container.querySelector('textarea[aria-label="章节正文"]')).toBeNull();
    await act(async () => [...container.querySelectorAll('button')].find((button) => button.textContent === '在正文区开始编辑').click());
    expect(openEditor).toHaveBeenCalledTimes(1);
    expect(api.saveManualManuscriptCandidate).not.toHaveBeenCalled();
  });

  it('saves central manual edits as a signed candidate without using an AI dialogue', async () => {
    const saved = vi.fn();
    api.saveManualManuscriptCandidate.mockResolvedValue({ revision: { revision_id: 'manual-revision', stage: 'audit_pending' } });
    await act(async () => root.render(<ManuscriptManualEditor projectId="project-1" selectedId="ch-1" chapter={{ content_signature: 'prose-sha', paragraphs: ['# 第一章', '原正文。'], total_paragraphs: 2 }} onSaved={saved} />)); await settle();
    const textarea = container.querySelector('textarea[aria-label="章节正文"]');
    await act(async () => setTextareaValue(textarea, '# 第一章\n\n作者改过的正文。'));
    await act(async () => container.querySelector('#manuscript-manual-form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))); await settle();
    expect(api.saveManualManuscriptCandidate).toHaveBeenCalledWith('project-1', 'ch-1', expect.objectContaining({
      content_signature: 'prose-sha',
      prose: '# 第一章\n\n作者改过的正文。',
    }), expect.any(AbortSignal));
    expect(saved).toHaveBeenCalledWith(expect.objectContaining({ revision_id: 'manual-revision' }));
    expect(api.createManuscriptActionDialogue).not.toHaveBeenCalled();
  });
});
