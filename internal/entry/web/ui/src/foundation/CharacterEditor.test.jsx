// @vitest-environment jsdom
import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CharacterEditor } from './CharacterEditor.jsx';
import { normalizeCharacterWorkspace } from './foundationModel.js';

const characters = [
  {
    id: 'hero', name: '林舟', aliases: ['阿舟'], role: '主角', description: '守城', arc: '学会信任',
    traits: ['坚定'], tier: 'core', faction: '守夜人', goal: '阻止灾难', motivation: '责任',
    conflict: '不信任盟友', voice: '克制', constraints: ['不背叛'], notes: ''
  },
  { id: 'ally', name: '夏岚', aliases: ['小岚'], role: '调查者', traits: [], tier: 'secondary', faction: '调查组' }
];
const coreCast = { members: [{ character: { id: 'hero' } }] };
const workspace = normalizeCharacterWorkspace({ character_workspace: {
  mode: 'adaptation', base_revision: 3, base_audit_signature: 'audit',
  current: { foundation: { characters, relationships: [] } },
  completeness: [
    { character_id: 'hero', tier: 'core', status: 'complete', missing: [] },
    { character_id: 'ally', tier: 'secondary', status: 'incomplete', missing: [{ code: 'goal', field: 'goal', severity: 'blocking', description: '需要目标' }] }
  ],
  source_coverage: { source_total: 1, decision_required: 1, mapped: 1, explicitly_excluded: 0, pending: 0, blocking_gaps: 0 },
  source_mappings: [{ id: 'map', action: 'rename', source_character_ids: ['source-hero'], target_character_ids: ['hero'], rationale: '改名', evidence: [{ reference: '第3章', summary: '守城证据' }] }],
  findings: [{ id: 'finding', scope: 'character', character_id: 'ally', location: 'goal', severity: 'blocking', description: '目标不完整', blocking: true }],
  allowed_operations: ['analyze', 'review']
} });

let root;
let container;

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.requestAnimationFrame = (callback) => setTimeout(callback, 0);
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
});

async function render(overrides = {}) {
  const props = {
    value: characters, coreCast, mode: 'adaptation',
    sourceFoundation: { characters: [{ id: 'source-hero', name: '原著林舟', role: '旧王继承人', description: '原著摘要' }] },
    relationships: [], workspace, errors: {}, disabled: false, onChange: vi.fn(),
    onAnalyze: vi.fn(), onReview: vi.fn(), ...overrides
  };
  await act(async () => root.render(<CharacterEditor {...props} />));
  await act(async () => {});
  return props;
}

function byText(selector, text) {
  return [...container.querySelectorAll(selector)].find((element) => element.textContent.includes(text));
}

describe('CharacterEditor workspace', () => {
  it('统一显示 core/non-core 状态、服务端完整度和只读来源证据', async () => {
    await render();
    expect(container.querySelectorAll('[role="option"]')).toHaveLength(2);
    expect(byText('[role="option"]', '林舟').textContent).toContain('core');
    expect(byText('[role="option"]', '夏岚').textContent).toContain('缺口 1');
    expect(container.textContent).toContain('来源映射与证据（只读）');
    expect(container.textContent).toContain('原著林舟');
    expect(container.textContent).toContain('守城证据');
  });

  it('搜索和层级筛选不改变稳定 ID 选择身份', async () => {
    await render();
    const search = container.querySelector('input[placeholder*="搜索姓名"]');
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(search, '小岚');
      search.dispatchEvent(new Event('input', { bubbles: true }));
    });
    expect(container.querySelectorAll('[role="option"]')).toHaveLength(1);
    expect(container.querySelector('[role="option"]').textContent).toContain('夏岚');
    await act(async () => container.querySelector('[role="option"]').click());
    expect(container.querySelector('[role="option"]').getAttribute('aria-selected')).toBe('true');
    expect(container.querySelector('.character-detail-header .eyebrow').textContent).toBe('ally');
  });

  it('当前/全部分析与审核传递明确 scope，候选不自动写入 onChange', async () => {
    const props = await render();
    const current = byText('button', '分析当前角色');
    await act(async () => current.click());
    expect(props.onAnalyze).toHaveBeenCalledWith(expect.objectContaining({ characterIDs: ['hero'] }));
    await act(async () => byText('button', '分析并补全全部角色').click());
    expect(props.onAnalyze).toHaveBeenLastCalledWith(expect.objectContaining({ characterIDs: [] }));
    await act(async () => byText('button', '审核全部角色').click());
    expect(props.onReview).toHaveBeenCalled();
    expect(props.onChange).not.toHaveBeenCalled();
  });

  it('角色任务完成后自动收起运行详情，保留角色双栏可视空间', async () => {
    const runningWorkspace = {
      ...workspace,
      candidate: { digest: 'candidate-1', foundation: { characters, relationships: [] } },
      run: { mode: 'review', status: 'running', stage: 'running', attempt: 1 }
    };
    await render({ workspace: runningWorkspace });
    expect(container.querySelector('.character-agent-toggle').getAttribute('aria-expanded')).toBe('true');
    expect(container.querySelector('.character-run-status')).not.toBeNull();

    await render({
      workspace: {
        ...runningWorkspace,
        allowedOperations: ['analyze', 'review', 'confirm'],
        run: { ...runningWorkspace.run, status: 'completed', stage: 'completed' }
      }
    });
    expect(container.querySelector('.character-agent-toggle').getAttribute('aria-expanded')).toBe('false');
    expect(container.querySelector('.character-run-status')).toBeNull();
  });

  it('从中央审核区进入 AI 调整时自动展开 Character Agent', async () => {
    await render({ agentOpenRequestId: 1 });
    expect(container.querySelector('.character-agent-toggle').getAttribute('aria-expanded')).toBe('true');
    expect(container.querySelector('.character-agent-controls')).not.toBeNull();
    expect(container.querySelector('.character-run-status')).toBeNull();
    expect(byText('button', '丢弃本轮候选')).not.toBeNull();
  });

  it('删除 core 使用可访问 dialog，Esc 返回触发按钮焦点', async () => {
    await render();
    const trigger = byText('button', '删除');
    await act(async () => trigger.click());
    const dialog = container.querySelector('[role="alertdialog"]');
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    expect(document.activeElement.textContent).toBe('取消');
    await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
    await act(async () => new Promise((resolve) => setTimeout(resolve, 0)));
    expect(container.querySelector('[role="alertdialog"]')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('原创模式不渲染空来源面板', async () => {
    await render({ mode: 'normal', sourceFoundation: null, workspace: { ...workspace, mode: 'original', coverage: null, sourceMappings: [] } });
    expect(container.textContent).not.toContain('来源映射与证据');
    expect(container.textContent).not.toContain('改编来源覆盖');
  });

  it('共创前目标角色为空时展示只读来源角色卡', async () => {
    const props = await render({
      value: [],
      disabled: true,
      workspace: null,
      sourceFoundation: {
        characters: [{
          id: 'source-hero',
          name: '原著林舟',
          role: '旧王继承人',
          description: '背负流亡王庭的秘密，追查城市灾变。',
          arc: '原著角色在灾变中守城。'
        }]
      }
    });

    expect(container.querySelectorAll('[role="option"]')).toHaveLength(1);
    expect(container.textContent).toContain('只读表示不可在此改写，并不表示必须共创后才能完整查看');
    expect(container.textContent).toContain('原著林舟');
    expect(container.textContent).toContain('旧王继承人');
    expect(container.textContent).toContain('背负流亡王庭的秘密，追查城市灾变。');
    expect(container.textContent).toContain('原著角色在灾变中守城。');
    expect(byText('button', '删除').disabled).toBe(true);
    expect(props.onChange).not.toHaveBeenCalled();
  });
});
