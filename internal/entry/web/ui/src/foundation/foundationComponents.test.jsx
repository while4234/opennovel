import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { CoreCastEditor } from '../components/CoreCastEditor.jsx';
import { FoundationOverview } from './FoundationOverview.jsx';
import { characterReviewInput, foundationValidationPresentation, FoundationLoadFailure } from './FoundationCenter.jsx';
import { CharacterEditor } from './CharacterEditor.jsx';

describe('foundation components', () => {
  it('已有候选的复审提交候选本身而不是空 canonical 草稿', () => {
    const candidateFoundation = { premise: '候选前提', characters: [{ id: 'hero' }] };
    expect(characterReviewInput({
      draft: { premise: '', characters: [] },
      characterWorkspace: { candidate: { revision: 7, foundation: candidateFoundation } }
    })).toEqual({ foundation: candidateFoundation, revision: 7 });
  });

  it('只读生成阶段把空基线校验解释为暂态，而不是可操作 warning', () => {
    const validation = { summary: ['故事前提不能为空', '至少需要一个角色', '至少需要一条世界规则'] };
    expect(foundationValidationPresentation({
      editable: false,
      readonlyReason: 'planning_stage_not_editable',
      baseRevision: 0
    }, validation)).toEqual({ kind: 'generating', messages: [] });
    expect(foundationValidationPresentation({ editable: true, baseRevision: 0 }, validation)).toEqual({
      kind: 'actionable',
      messages: validation.summary
    });
  });

  it('renders a recoverable error instead of an endless loading state', () => {
    const markup = renderToStaticMarkup(<FoundationLoadFailure error={{ message: 'signature mismatch' }} onRetry={() => {}} />);
    expect(markup).toContain('StoryFoundation 加载失败');
    expect(markup).toContain('signature mismatch');
    expect(markup).toContain('重试');
    expect(markup).not.toContain('正在加载 StoryFoundation');
  });

  it('adaptation 同时显示 source 只读与 target 数据', () => {
    const markup = renderToStaticMarkup(<FoundationOverview server={{ mode: 'adaptation', baseRevision: 2, baseAuditSignature: 'abcdef012345', editable: true, sourceFoundation: { premise: '原著前提', source_signature: 'source123456', characters: [{ id: 'source-hero', name: '原著主角', role: '主角', description: '推动原著主线' }], world_rules: [] }, modeSpecific: {}, coreCast: { members: [], source_dispositions: [] } }} draft={{ premise: '目标前提', characters: [], relationships: [], world_rules: [] }} onPremiseChange={() => {}} />);
    expect(markup).toContain('SourceFoundation（只读）');
    expect(markup).toContain('不可写入');
    expect(markup).toContain('原著前提');
    expect(markup).toContain('目标前提');
    expect(markup).toContain('目标故事前提');
    expect(markup).toContain('来源角色档案');
    expect(markup).toContain('原著主角');
  });

  it('原创生成阶段立即展示已确认的共创故事前提', () => {
    const markup = renderToStaticMarkup(<FoundationOverview
      server={{
        mode: 'normal',
        baseRevision: 0,
        baseAuditSignature: 'audit',
        editable: false,
        planningReview: { brief: '女主必须在真相与自由之间作出选择。' }
      }}
      draft={{ premise: '', characters: [], relationships: [], world_rules: [] }}
      disabled
      premiseError="故事前提不能为空"
      onPremiseChange={() => {}}
    />);
    expect(markup).toContain('目标故事前提（共创确认稿）');
    expect(markup).toContain('女主必须在真相与自由之间作出选择。');
    expect(markup).toContain('本轮角色确认后会随 StoryFoundation 正式发布');
    expect(markup).not.toContain('field-error');
  });

  it('核心角色只读模式展示中文重要性和来源角色', () => {
    const markup = renderToStaticMarkup(<CoreCastEditor readOnly mode="adapt" value={{ members: [{ character: { id: 'hero', name: '林舟', traits: [], constraints: [] }, importance: 'protagonist', origin: 'source', source_character_ids: ['source-1'] }] }} confirmed />);
    expect(markup).toContain('主角');
    expect(markup).toContain('source-1');
    expect(markup).not.toContain('保存修改');
  });

  it('目标核心角色尚未生成时展示来源分析角色', () => {
    const markup = renderToStaticMarkup(<CoreCastEditor readOnly mode="adapt" value={{ members: [] }} sourceMajorCharacters={[{
      id: 'source-hero', name: '原著主角', aliases: ['阿原'], role: '主角 / 调查者', description: '追查失踪真相',
      traits: ['冷静', '执着'], arc: '从怀疑到承担', goal: '找到证据', motivation: '守护家人', conflict: '不敢信任同伴',
      voice: '克制', constraints: ['不主动伤害无辜'], faction: '调查组', notes: '保留左手旧伤'
    }]} />);
    expect(markup).toContain('目标核心角色尚未生成');
    expect(markup).toContain('原著主角');
    expect(markup).toContain('来源分析角色');
    expect(markup).toContain('阿原');
    expect(markup).toContain('主角 / 调查者');
    expect(markup).toContain('追查失踪真相');
    expect(markup).toContain('冷静、执着');
    expect(markup).toContain('从怀疑到承担');
    expect(markup).toContain('不主动伤害无辜');
  });

  it('核心角色编辑器在工作区解释修改流程并隐藏原始 JSON', () => {
    const markup = renderToStaticMarkup(<CoreCastEditor mode="normal" value={{ members: [{ character: { id: 'hero', name: '林舟', role: '主角', traits: ['冷静'], constraints: ['不背叛同伴'] }, importance: 'protagonist', origin: 'original' }], planned_relationships: [] }} completion={{ complete: false, missing: [{ code: 'goal_required', member_id: 'hero', description: 'goal is required' }] }} />);
    expect(markup).toContain('核心角色工作区');
    expect(markup).toContain('先改角色');
    expect(markup).toContain('角色重要性');
    expect(markup).toContain('主角');
    expect(markup).toContain('请填写角色目标');
    expect(markup).not.toContain('核心计划关系（JSON）');
    expect(markup).not.toContain('&gt;protagonist&lt;');
  });

  it('角色较多时一页只渲染一个角色表单并提供直接分页', () => {
    const markup = renderToStaticMarkup(<CoreCastEditor mode="normal" value={{ members: [
      { character: { id: 'hero', name: '林舟', role: '主角', traits: ['冷静'], constraints: ['不背叛同伴'] }, importance: 'protagonist', origin: 'original' },
      { character: { id: 'rival', name: '顾念', role: '对手', traits: ['敏锐'], constraints: ['不轻易认输'] }, importance: 'antagonist', origin: 'original' }
    ], planned_relationships: [] }} completion={{ complete: false, missing: [] }} />);
    expect(markup).toContain('第 1 项，共 2 项');
    expect(markup).toContain('林舟');
    expect(markup).toContain('顾念');
    expect(markup).toContain('角色 1 代号');
    expect(markup).not.toContain('角色 2 代号');
    expect((markup.match(/core-cast-member-card/g) || [])).toHaveLength(1);
  });

  it('只读规划阶段仍展示待确认角色候选并允许显式确认', () => {
    const candidate = {
      id: 'support-shen',
      name: '沈辞',
      role: '男主心腹助理',
      tier: 'important',
      aliases: [],
      traits: ['可靠'],
      constraints: ['不替代主角决策']
    };
    const markup = renderToStaticMarkup(<CharacterEditor
      value={[]}
      relationships={[]}
      disabled
      onChange={() => {}}
      workspace={{
        mode: 'original',
        allowedOperations: ['confirm'],
        confirmationStatus: 'unconfirmed',
        candidate: { foundation: { characters: [candidate], relationships: [] } },
        run: { mode: 'review', status: 'completed' },
        completenessByID: { 'support-shen': { status: 'complete', missing: [] } },
        findings: []
      }}
    />);
    expect(markup).toContain('沈辞');
    expect(markup).toContain('男主心腹助理');
    expect(markup).toContain('角色目录');
    expect(markup).toContain('显示 1 / 共 1');
    expect(markup).toMatch(/<button id="character-confirm-action" class="tool-button accent" type="button"><svg[^>]*>[\s\S]*确认角色卡并继续<\/button>/);
  });

  it('已确认角色审核按稳定角色去重统计并显示通过', () => {
    const characters = [
      { id: 'lead', name: '主角', role: '主角', tier: 'core', aliases: [], traits: [], constraints: [] },
      { id: 'support', name: '助理', role: '配角', tier: 'important', aliases: [], traits: [], constraints: [] }
    ];
    const completeness = [
      { character_id: 'lead', status: 'complete', missing: [] },
      { character_id: 'support', status: 'complete', missing: [] },
      { character_id: 'lead', status: 'complete', missing: null },
      { character_id: 'support', status: 'complete', missing: null }
    ];
    const markup = renderToStaticMarkup(<CharacterEditor
      value={characters}
      relationships={[]}
      disabled
      onChange={() => {}}
      workspace={{
        mode: 'original',
        confirmationStatus: 'confirmed',
        completeness,
        completenessByID: Object.fromEntries(completeness.map((item) => [item.character_id, item])),
        findings: []
      }}
    />);

    expect(markup).toContain('完整 2/2');
    expect(markup).not.toContain('完整 4/2');
    expect(markup).toContain('通过');
    expect(markup).not.toContain('未审核');
  });
});
