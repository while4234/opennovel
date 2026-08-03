import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  analyzeCharacters, applyFoundation, characterCandidateDigest, confirmCharacterCandidate, foundationError, loadCharacterWorkspace,
  previewFoundation, retryFoundation, reviewCharacters
} from './foundationApi.js';

afterEach(() => vi.unstubAllGlobals());

describe('foundation API adapter', () => {
  it('preview 只发送基线与完整 candidate', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ preview: { id: 'p' } }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    const candidate = { schema_version: 1, premise: '目标', characters: [], relationships: [], world_rules: [] };
    await previewFoundation('project/a', { baseRevision: 7, baseAuditSignature: 'audit' }, candidate);
    const [url, options] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/projects/project%2Fa/foundation/preview');
    expect(JSON.parse(options.body)).toEqual({ expected_base_revision: 7, expected_base_audit_signature: 'audit', candidate });
  });

  it('apply 只发送 preview ID 与传入的同一个 idempotency key', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ revision: {} }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    await applyFoundation('p', 'preview-7', 'stable-key');
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ preview_id: 'preview-7', idempotency_key: 'stable-key' });
  });

  it('retry 不提交 candidate 或 preview', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ revision: {} }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    await retryFoundation('p', 'retry-key');
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ idempotency_key: 'retry-key' });
  });

  it('保留统一错误 envelope 的 code 与安全 message', () => {
    expect(foundationError({ data: { error: { code: 'foundation_stale', message: 'changed' } } })).toEqual({ code: 'foundation_stale', message: 'changed' });
  });

  it('Character confirm 只发送审核通过候选的版本、digest 与幂等键', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ character: {} }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    await confirmCharacterCandidate('p', { revision: 8, digest: 'abc123' }, 'confirm-key');
    expect(fetchMock.mock.calls[0][0]).toBe('/api/projects/p/character-cards/confirm');
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({
      expected_candidate_revision: 8,
      candidate_digest: 'abc123',
      idempotency_key: 'confirm-key'
    });
  });

  it('Character analyze 发送 target-only 草稿、稳定 scope、幂等键和 digest', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ character_workspace: {} }), { status: 202 }));
    vi.stubGlobal('fetch', fetchMock);
    const candidate = {
      schema_version: 3, revision: 7, premise: '目标',
      characters: [{ id: 'hero', name: '林舟', role: '主角', description: '', arc: '', traits: [], constraints: [], contrast_details: [{ surface: '冷静', depth: '恐惧' }] }],
      relationships: [], relationships_reviewed: true, world_rules: [],
      source_foundation: { premise: '绝不能写回' }
    };
    await analyzeCharacters('p', { baseRevision: 7, baseAuditSignature: 'audit' }, candidate, {
      characterIDs: ['hero'], instruction: '补全动机', allowSupportingCharacters: true, idempotencyKey: 'analyze-key'
    });
    const body = JSON.parse(fetchMock.mock.calls[0][1].body);
    expect(body).toEqual(expect.objectContaining({
      expected_base_revision: 7, expected_base_audit_signature: 'audit', idempotency_key: 'analyze-key',
      scope: { character_ids: ['hero'] }, instruction: '补全动机', allow_supporting_characters: true
    }));
    expect(body.candidate).not.toHaveProperty('source_foundation');
    expect(body.candidate.characters[0].contrast_details).toEqual([{ surface: '冷静', depth: '恐惧' }]);
    expect(body.candidate_digest).toMatch(/^[a-f0-9]{64}$/);
  });

  it('Character review 保留来源映射引用但不发送 SourceFoundation，并透传 abort signal', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ character_workspace: {} }), { status: 202 }));
    vi.stubGlobal('fetch', fetchMock);
    const controller = new AbortController();
    const candidate = { schema_version: 3, revision: 1, premise: '目标', characters: [{ id: 'hero', name: '林舟', role: '主角', traits: [] }], relationships: [], world_rules: [] };
    await reviewCharacters('p', { baseRevision: 1, baseAuditSignature: 'audit' }, candidate, {
      sourceMappings: [{ id: 'map', action: 'keep', source_character_ids: ['source'], target_character_ids: ['hero'] }],
      idempotencyKey: 'review-key', signal: controller.signal
    });
    const [, options] = fetchMock.mock.calls[0];
    expect(options.signal).toBe(controller.signal);
    expect(JSON.parse(options.body).source_mappings[0].source_character_ids).toEqual(['source']);
    await loadCharacterWorkspace('p', 'run/a', controller.signal);
    expect(fetchMock.mock.calls[1][0]).toBe('/api/projects/p/foundation/characters?run_id=run%2Fa');
  });

  it('复审持久化候选时只提交 revision 和 digest，避免数组规范化触发越权校验', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ character_workspace: {} }), { status: 202 }));
    vi.stubGlobal('fetch', fetchMock);
    const candidate = {
      schema_version: 3,
      revision: 0,
      premise: '',
      characters: [{ id: 'hero', name: '林舟', role: '主角', traits: [] }],
      relationships: [],
      world_rules: []
    };
    await reviewCharacters('p', { baseRevision: 0, baseAuditSignature: 'audit' }, candidate, {
      candidateRevision: 7,
      idempotencyKey: 'persisted-review'
    });
    const body = JSON.parse(fetchMock.mock.calls[0][1].body);
    expect(body.candidate_revision).toBe(7);
    expect(body.candidate_digest).toMatch(/^[a-f0-9]{64}$/);
    expect(body).not.toHaveProperty('candidate');
  });

  it('重新分析持久化候选时只提交 revision 和 digest，避免规范化误改非角色字段', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ character_workspace: {} }), { status: 202 }));
    vi.stubGlobal('fetch', fetchMock);
    const candidate = {
      schema_version: 3,
      revision: 0,
      premise: '',
      characters: [{ id: 'hero', name: '林舟', role: '主角', traits: [] }],
      relationships: [],
      world_rules: []
    };
    await analyzeCharacters('p', { baseRevision: 0, baseAuditSignature: 'audit' }, candidate, {
      candidateRevision: 8,
      instruction: '仅修正关系标签',
      idempotencyKey: 'persisted-analyze'
    });
    const body = JSON.parse(fetchMock.mock.calls[0][1].body);
    expect(body.candidate_revision).toBe(8);
    expect(body.candidate_digest).toMatch(/^[a-f0-9]{64}$/);
    expect(body).not.toHaveProperty('candidate');
  });

  it('candidate digest 对服务端规范化排序稳定', async () => {
    const left = { characters: [{ id: 'b', name: '乙', role: '配角', traits: ['冷静', '敏锐'] }, { id: 'a', name: '甲', role: '主角', traits: [] }], relationships: [] };
    const right = { characters: [{ id: 'a', name: '甲', role: '主角', traits: [] }, { id: 'b', name: '乙', role: '配角', traits: ['敏锐', '冷静'] }], relationships: [] };
    expect(await characterCandidateDigest(left)).toBe(await characterCandidateDigest(right));
  });
});
