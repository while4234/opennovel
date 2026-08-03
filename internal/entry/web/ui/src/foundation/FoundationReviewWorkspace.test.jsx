// @vitest-environment jsdom
import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import FoundationReviewWorkspace from './FoundationReviewWorkspace.jsx';

const review = {
  adaptation: true,
  pending: true,
  collecting: false,
  readonly: false,
  coreCastPreserved: true,
  foundationRevision: 2,
  foundationGeneration: 1,
  foundationAuditSignature: 'audit-signature',
  premise: '顾衡以私家侦探身份进入案件，并承担目标故事主视角。',
  sourcePremise: '原著围绕安少廷与袁可欣展开。',
  coreCharacters: [
    { id: 'gu_heng', name: '顾衡', role: '新男主（主视角）', tier: 'core', description: '目标原创角色' }
  ],
  supportingCharacters: [
    { id: 'an_shaoting', name: '安少廷', role: '源男主功能保留为对手', tier: 'core' }
  ],
  plannedRelationships: [
    { id: 'rel', source_character_id: '顾衡', target_character_id: '安少廷', label: '取证者与对手' }
  ],
  hardWorldRules: [{ id: 'hard', category: '视角', rule: '顾衡必须承担主视角。' }],
  softWorldRules: [{ id: 'soft', category: '节奏', rule: '前期保持克制调查。' }],
  sourceWorldRules: [{ id: 'source-rule', category: '原著', rule: '保留梦游因果。' }],
  sourceDispositions: [
    { source_character_id: 'an_shaoting', action: 'keep', target_character_ids: ['an_shaoting'] }
  ]
};

let container;
let root;

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.requestAnimationFrame = (callback) => callback();
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
});

function button(label) {
  return [...container.querySelectorAll('button')].find((item) => item.textContent.includes(label));
}

async function renderWorkspace(overrides = {}) {
  const props = {
    busy: false,
    onConfirm: vi.fn(),
    onConfirmCharacterCandidate: vi.fn(),
    onRevise: vi.fn(),
    planningRevision: { feedback: '加强新男主的调查动机', status: 'idle' },
    review,
    setPlanningRevision: vi.fn(),
    ...overrides
  };
  await act(async () => root.render(<FoundationReviewWorkspace {...props} />));
  return props;
}

describe('FoundationReviewWorkspace', () => {
  it('将完整设定拆成互斥页签，不在同一页堆叠所有内容', async () => {
    await renderWorkspace();

    expect(container.querySelectorAll('[role="tab"]')).toHaveLength(6);
    expect(button('概要').getAttribute('aria-selected')).toBe('true');
    expect(container.textContent).toContain('目标故事前提');
    expect(container.textContent).not.toContain('取证者与对手');
    expect(container.textContent).not.toContain('确认完整设定并继续');

    await act(async () => button('角色').click());
    expect(container.textContent).toContain('顾衡');
    expect(container.textContent).toContain('新男主（主视角）');
    expect(container.textContent).not.toContain('目标故事前提');

    await act(async () => button('关系').click());
    expect(container.textContent).toContain('取证者与对手');
    expect(container.textContent).not.toContain('顾衡必须承担主视角');
  });

  it('把原著证据和确认操作放在各自独立页签', async () => {
    const props = await renderWorkspace();

    await act(async () => button('原著证据').click());
    expect(container.textContent).toContain('只读原著证据');
    expect(container.textContent).toContain('保留梦游因果');
    expect(container.textContent).not.toContain('修改意见');
    expect(button('去确认')).toBeUndefined();

    await act(async () => button('确认与修改').click());
    expect(button('确认与修改').getAttribute('aria-selected')).toBe('true');
    expect(container.textContent).toContain('修改意见');
    expect(button('让 AI 按意见修订').disabled).toBe(false);
    expect(button('确认完整设定并继续').disabled).toBe(false);

    await act(async () => button('让 AI 按意见修订').click());
    await act(async () => button('确认完整设定并继续').click());
    expect(props.onRevise).toHaveBeenCalledTimes(1);
    expect(props.onConfirm).toHaveBeenCalledTimes(1);
  });

  it('角色卡待确认时工作区只保留一个真正的确认入口', async () => {
    const props = await renderWorkspace({
      review: { ...review, characterConfirmationRequired: true }
    });

    const confirmationButtons = [...container.querySelectorAll('button')]
      .filter((item) => item.textContent.trim() === '确认角色卡');
    expect(confirmationButtons.map((item) => item.textContent.trim())).toEqual(['确认角色卡']);
    expect(button('确认并继续')).toBeUndefined();

    await act(async () => confirmationButtons[0].click());
    expect(props.onConfirmCharacterCandidate).toHaveBeenCalledTimes(1);
  });
});
