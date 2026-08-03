// @vitest-environment jsdom
import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import FoundationReviewWorkspace, { manualRevisionFeedback } from './FoundationReviewWorkspace.jsx';

const character = {
  id: 'lin_yu',
  name: '林雨',
  role: '调查记者',
  description: '追查旧案的主角',
  goal: '找到失踪证人',
  motivation: '弥补一次失败报道',
  conflict: '真相会伤害家人',
  arc: '从独自承担到学会信任',
  voice: '克制、善于追问'
};

const review = {
  pending: true,
  readonly: false,
  premise: '林雨追查一宗被遗忘的旧案。',
  coreCharacters: [character],
  supportingCharacters: [],
  plannedRelationships: [],
  hardWorldRules: [],
  softWorldRules: [],
  coreCastPreserved: true,
  foundationRevision: 1,
  foundationGeneration: 1
};

function draft(overrides = {}) {
  return {
    premise: review.premise,
    characters: [{ ...character }],
    relationships: [],
    hardRules: [],
    softRules: [],
    ...overrides
  };
}

describe('manualRevisionFeedback', () => {
  it('只把用户真正改过的字段写成硬约束', () => {
    expect(manualRevisionFeedback(review, draft())).toBe('');

    const feedback = manualRevisionFeedback(review, draft({
      characters: [{ ...character, goal: '在三天内找到证人并公开证据' }]
    }));

    expect(feedback).toContain('用户逐项手工修改');
    expect(feedback).toContain('角色“林雨”的外部目标');
    expect(feedback).toContain('在三天内找到证人并公开证据');
    expect(feedback).toContain('重新执行角色、关系、世界规则与完整 Foundation 审核');
    expect(feedback).not.toContain('人物弧：');
  });
});

describe('FoundationReviewWorkspace character confirmation', () => {
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
  });

  it('在角色候选审核页直接收集 AI 调整意见，并保留逐项工作台入口', async () => {
    const onOpenCharacterRevision = vi.fn();
    const onReviseCharacterCandidate = vi.fn();
    const onConfirmCharacterCandidate = vi.fn();
    await act(async () => root.render(
      <FoundationReviewWorkspace
        onConfirmCharacterCandidate={onConfirmCharacterCandidate}
        onOpenCharacterRevision={onOpenCharacterRevision}
        onReviseCharacterCandidate={onReviseCharacterCandidate}
        planningRevision={{
          characterFeedback: '强化女主主动调查真相的目标',
          allowSupportingCharacters: true
        }}
        review={{
          ...review,
          characterConfirmationRequired: true
        }}
        selectedTab="actions"
      />
    ));

    const buttons = [...container.querySelectorAll('button')];
    const aiRevise = buttons.find((item) => item.textContent.includes('让 AI 按意见调整角色卡'));
    const openWorkbench = buttons.find((item) => item.textContent.includes('打开角色工作台逐项检查'));
    const confirm = buttons.find((item) => item.textContent.includes('确认角色卡并继续生成完整设定'));

    expect(container.querySelector('[aria-label="角色卡修改意见"]').value).toBe('强化女主主动调查真相的目标');
    expect(container.textContent).toContain('完整 StoryFoundation 会在角色确认后继续生成');
    expect(aiRevise).toBeTruthy();
    expect(aiRevise.disabled).toBe(false);
    expect(openWorkbench).toBeTruthy();
    expect(confirm).toBeTruthy();

    await act(async () => aiRevise.click());
    await act(async () => openWorkbench.click());
    await act(async () => confirm.click());
    expect(onReviseCharacterCandidate).toHaveBeenCalledWith({
      feedback: '强化女主主动调查真相的目标',
      allowSupportingCharacters: true
    });
    expect(onOpenCharacterRevision).toHaveBeenCalledTimes(1);
    expect(onConfirmCharacterCandidate).toHaveBeenCalledTimes(1);
  });

  it('手工精确修改使用独立编辑层并支持 Esc 退出', async () => {
    await act(async () => root.render(
      <FoundationReviewWorkspace
        planningRevision={{ feedback: '' }}
        review={review}
        selectedTab="actions"
        setPlanningRevision={vi.fn()}
      />
    ));

    const open = [...container.querySelectorAll('button')]
      .find((item) => item.textContent.includes('打开手动编辑'));
    await act(async () => open.click());
    expect(container.querySelector('.foundation-manual-revision.open')).not.toBeNull();
    expect(container.textContent).toContain('退出手动编辑');

    await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
    expect(container.querySelector('.foundation-manual-revision.open')).toBeNull();
  });
});
