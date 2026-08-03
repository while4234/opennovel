import { describe, expect, it } from 'vitest';
import {
  acceptAllCharacterCandidates, candidateFingerprint, characterFieldDiff, duplicateFoundationCharacter,
  filterAndSortCharacters, foundationReadonlyReasonLabel, mergeCharacterField, newFoundationCharacter, normalizeCharacter,
  normalizeCharacterWorkspace, normalizeFoundationResponse, normalizeWorldRule, reviewStatusForCharacter,
  sourceMajorCharacters, validateFoundationDraft
} from './foundationModel.js';

const complete = {
  schema_version: 1, revision: 4, premise: '目标故事',
  characters: [{ id: 'hero', name: '林舟', aliases: ['阿舟', '阿舟'], role: '主角', traits: [], constraints: [] }],
  relationships: [], relationships_reviewed: true,
  world_rules: [{ id: 'rule-1', category: 'magic', rule: '能力有代价', boundary: '不可复活', strength: 'hard' }]
};

describe('foundation model', () => {
  it('将服务端只读状态码转换为用户可理解的中文原因', () => {
    expect(foundationReadonlyReasonLabel('planning_stage_not_editable')).toContain('中央审核区');
    expect(foundationReadonlyReasonLabel('adaptation_plan_unavailable')).toContain('改编方案');
    expect(foundationReadonlyReasonLabel('')).toBe('当前阶段不允许手工编辑');
  });

  it('非阻塞审核建议不把已通过角色误标为需修订', () => {
    const advisory = [{ character_id: 'hero', severity: 'warning', blocking: false }];
    const blocking = [{ character_id: 'hero', severity: 'blocking', blocking: true }];
    expect(reviewStatusForCharacter('hero', advisory, false, true)).toBe('passed');
    expect(reviewStatusForCharacter('hero', advisory, false, false)).toBe('not_reviewed');
    expect(reviewStatusForCharacter('hero', blocking, false, true)).toBe('needs_revision');
  });

  it('按稳定 sr_/hr_ ID 恢复旧项目规则强度，不让错误重试把 soft 全显示成 hard', () => {
    expect(normalizeWorldRule({ id: 'sr_escape_tension', strength: 'hard' }).strength).toBe('soft');
    expect(normalizeWorldRule({ id: 'hr_identity_lock', strength: 'soft' }).strength).toBe('hard');
    expect(normalizeWorldRule({ id: 'rule-neutral', strength: 'soft' }).strength).toBe('soft');
  });

  it('映射 normal/adaptation DTO 且保留 source 只读数据', () => {
    const normal = normalizeFoundationResponse({ foundation: { mode: 'normal', target_foundation: complete, editable: true, allowed_operations: ['get', 'preview'] } });
    expect(normal.mode).toBe('normal');
    expect(normal.sourceFoundation).toBeNull();
    const adaptation = normalizeFoundationResponse({ foundation: { mode: 'adaptation', source_foundation: { premise: '原著' }, target_foundation: complete, editable: true } });
    expect(adaptation.mode).toBe('adaptation');
    expect(adaptation.sourceFoundation.premise).toBe('原著');
    expect(adaptation.targetFoundation.premise).toBe('目标故事');
  });

  it('对别名去重，并让 UI 状态不进入 candidate fingerprint', () => {
    const response = normalizeFoundationResponse({ foundation: { target_foundation: complete } });
    expect(response.targetFoundation.characters[0].aliases).toEqual(['阿舟']);
    expect(candidateFingerprint({ ...complete, updated_at: 'before' })).toBe(candidateFingerprint({ ...complete, updated_at: 'after' }));
  });

  it('来源主要角色保留分析生成的完整人物资料', () => {
    expect(sourceMajorCharacters({ characters: [{
      name: '原著主角', role: '主角 / 调查者', description: '追查失踪真相', arc: '从怀疑到承担',
      traits: ['冷静', '冷静', '执着'], goal: '找到证据', motivation: '守护家人', conflict: '不敢信任同伴',
      voice: '克制', constraints: ['不主动伤害无辜'], faction: '调查组', notes: '保留左手旧伤'
    }] })[0]).toEqual(expect.objectContaining({
      role: '主角 / 调查者', description: '追查失踪真相', arc: '从怀疑到承担', traits: ['冷静', '执着'],
      goal: '找到证据', motivation: '守护家人', conflict: '不敢信任同伴', voice: '克制',
      constraints: ['不主动伤害无辜'], faction: '调查组', notes: '保留左手旧伤'
    }));
  });

  it('定位悬空关系与缺失规则字段', () => {
    const validation = validateFoundationDraft({ ...complete, relationships: [{ id: 'rel', source_character_id: 'hero', target_character_id: 'missing', type: 'ally' }], world_rules: [{ id: 'rule-1', rule: '' }] });
    expect(validation.valid).toBe(false);
    expect(validation.fields['relationships.0.target_character_id']).toMatch(/悬空/);
    expect(validation.fields['world_rules.0.rule']).toMatch(/不能为空/);
  });

  it('新建角色直接使用不会与服务端稳定前缀冲突的 UUID ID', () => {
    expect(newFoundationCharacter().id).toMatch(/^char-/);
  });

  it('完整角色字段 round-trip，不丢反差、小传、初始状态和知识边界', () => {
    const character = normalizeCharacter({
      id: 'hero', name: '林舟', role: '主角', tier: 'core',
      contrast_details: [{ surface: '冷静', depth: '害怕失去同伴' }],
      key_backstory: [{ event: '旧城失守', impact: '拒绝轻信承诺' }],
      initial_state: { identity: '守夜人', situation: '被停职', emotion: '克制', resources: ['旧地图'], relationships: '与导师决裂' },
      knowledge_boundary: { known: ['灾变将至'], unknown: ['导师身份'], misconceptions: ['父亲背叛'], forbidden: ['终局真相'] }
    });
    expect(normalizeCharacter(character)).toEqual(character);
    expect(character.initial_state.resources).toEqual(['旧地图']);
    expect(character.knowledge_boundary.forbidden).toEqual(['终局真相']);
  });

  it('候选按稳定 ID 对齐并支持逐字段与安全全量合并', () => {
    const current = [normalizeCharacter({ id: 'hero', name: '林舟', role: '主角', voice: '克制' })];
    const candidate = normalizeCharacter({ ...current[0], voice: '短句', goal: '守城' });
    expect(characterFieldDiff(current[0], candidate).map((item) => item.field)).toEqual(['goal', 'voice']);
    expect(mergeCharacterField(current, candidate, 'voice')[0]).toEqual(expect.objectContaining({ id: 'hero', voice: '短句', goal: '' }));
    const merged = acceptAllCharacterCandidates(current, { characters: [candidate, { id: 'ally', name: '夏岚', role: '盟友' }] });
    expect(merged.map((item) => item.id)).toEqual(['hero', 'ally']);
    expect(duplicateFoundationCharacter(current[0]).id).not.toBe('hero');
  });

  it('服务端完整度、finding、来源映射和未知可选字段安全降级', () => {
    const workspace = normalizeCharacterWorkspace({ character_workspace: {
      mode: 'adaptation', current: { foundation: complete }, current_digest: 'digest',
      completeness: [{ character_id: 'hero', tier: 'core', status: 'complete', missing: [] }],
      source_mappings: [{ id: 'map', action: 'rename', source_character_ids: ['source'], target_character_ids: ['hero'], evidence: [{ reference: '第3章', summary: '守城' }] }],
      findings: [{ id: 'f', scope: 'character', character_id: 'hero', location: 'voice', severity: 'unexpected', description: '描述' }],
      allowed_operations: ['analyze']
    } });
    expect(workspace.completenessByID.hero.status).toBe('complete');
    expect(workspace.sourceMappings[0].action).toBe('rename');
    expect(workspace.findings[0].severity).toBe('warning');
  });

  it('搜索/筛选/排序保持稳定 ID 选择所需身份', () => {
    const characters = [
      normalizeCharacter({ id: 'support', name: '夏岚', aliases: ['小岚'], role: '调查者', faction: '守夜人', tier: 'secondary' }),
      normalizeCharacter({ id: 'hero', name: '林舟', role: '主角', faction: '旧王庭', tier: 'core' })
    ];
    expect(filterAndSortCharacters(characters, { query: '小岚', coreIDs: new Set(['hero']) }).map((item) => item.id)).toEqual(['support']);
    expect(filterAndSortCharacters(characters, { tier: 'core', coreIDs: new Set(['hero']) }).map((item) => item.id)).toEqual(['hero']);
    expect(filterAndSortCharacters(characters, { sort: 'core', coreIDs: new Set(['hero']) })[0].id).toBe('hero');
  });
});
