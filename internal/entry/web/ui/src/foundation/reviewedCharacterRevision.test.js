import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  analyzeCharacters,
  foundationIdempotencyKey,
  loadCharacterWorkspace,
  loadFoundation
} from './foundationApi.js';
import {
  reviewedCharacterCandidate,
  startReviewedCharacterRevision
} from './reviewedCharacterRevision.js';

vi.mock('./foundationApi.js', () => ({
  analyzeCharacters: vi.fn(),
  foundationIdempotencyKey: vi.fn(() => 'revision-key'),
  loadCharacterWorkspace: vi.fn(),
  loadFoundation: vi.fn()
}));

const review = {
  characterCandidateRevision: 3,
  characterCandidateDigest: 'candidate-digest'
};

const candidate = {
  revision: 3,
  digest: 'candidate-digest',
  foundation: {
    premise: '女主调查失踪真相。',
    characters: [{
      id: 'lead',
      name: '林澈',
      role: '调查者',
      gender: 'female',
      description: '主动寻找真相。',
      arc: '从怀疑走向承担。',
      traits: [],
      constraints: []
    }],
    relationships: [],
    world_rules: []
  }
};

beforeEach(() => {
  vi.clearAllMocks();
  loadFoundation.mockResolvedValue({
    foundation: {
      mode: 'normal',
      target_foundation: candidate.foundation,
      editable: true,
      base_revision: 2,
      base_audit_signature: 'audit-signature'
    }
  });
  loadCharacterWorkspace.mockResolvedValue({
    character_workspace: {
      mode: 'original',
      base_revision: 2,
      base_audit_signature: 'audit-signature',
      candidate,
      allowed_operations: ['analyze']
    }
  });
  analyzeCharacters.mockResolvedValue({ character_workspace: { run: { status: 'queued' } } });
});

describe('reviewed character revision', () => {
  it('submits the visible reviewed candidate and author intent directly to Character Agent', async () => {
    await startReviewedCharacterRevision('project-1', review, {
      feedback: '  强化女主主动调查真相的目标  ',
      allowSupportingCharacters: true
    });

    expect(loadFoundation).toHaveBeenCalledWith('project-1');
    expect(loadCharacterWorkspace).toHaveBeenCalledWith('project-1');
    expect(foundationIdempotencyKey).toHaveBeenCalledWith('review-character-revise');
    expect(analyzeCharacters).toHaveBeenCalledWith(
      'project-1',
      expect.objectContaining({
        baseRevision: 2,
        baseAuditSignature: 'audit-signature'
      }),
      expect.objectContaining({
        characters: [expect.objectContaining({ id: 'lead', name: '林澈' })]
      }),
      {
        candidateRevision: 3,
        characterIDs: [],
        instruction: '强化女主主动调查真相的目标',
        allowSupportingCharacters: true,
        idempotencyKey: 'revision-key'
      }
    );
  });

  it('rejects a stale reviewed candidate before starting a new run', () => {
    expect(() => reviewedCharacterCandidate({
      candidate: { ...candidate, revision: 4 },
      allowedOperations: ['analyze']
    }, review)).toThrow('角色候选状态已经变化');
  });

  it('requires an explicit author instruction', async () => {
    await expect(startReviewedCharacterRevision('project-1', review, {
      feedback: '   '
    })).rejects.toThrow('请输入角色卡修改意见');
    expect(loadFoundation).not.toHaveBeenCalled();
    expect(analyzeCharacters).not.toHaveBeenCalled();
  });
});
