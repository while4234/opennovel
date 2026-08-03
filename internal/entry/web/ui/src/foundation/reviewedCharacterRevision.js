import {
  analyzeCharacters,
  foundationIdempotencyKey,
  loadCharacterWorkspace,
  loadFoundation
} from './foundationApi.js';
import {
  normalizeCharacterWorkspace,
  normalizeFoundationResponse
} from './foundationModel.js';

export async function startReviewedCharacterRevision(projectId, review, {
  feedback = '',
  allowSupportingCharacters = false,
  isCurrent = () => true
} = {}) {
  const instruction = String(feedback || '').trim();
  if (!instruction) {
    throw new Error('请输入角色卡修改意见');
  }

  const [foundationResponse, characterResponse] = await Promise.all([
    loadFoundation(projectId),
    loadCharacterWorkspace(projectId)
  ]);
  if (!isCurrent()) return { skipped: true };
  const server = normalizeFoundationResponse(foundationResponse);
  const workspace = normalizeCharacterWorkspace(characterResponse);
  const candidate = reviewedCharacterCandidate(workspace, review);

  return analyzeCharacters(projectId, server, candidate.foundation, {
    candidateRevision: candidate.revision,
    characterIDs: [],
    instruction,
    allowSupportingCharacters,
    idempotencyKey: foundationIdempotencyKey('review-character-revise')
  });
}

export function reviewedCharacterCandidate(workspace = {}, review = {}) {
  const candidate = workspace.candidate;
  const expectedRevision = Number(review.characterCandidateRevision || 0);
  const expectedDigest = String(review.characterCandidateDigest || '');
  const candidateChanged = !candidate?.foundation ||
    !workspace.allowedOperations?.includes('analyze') ||
    (expectedRevision > 0 && candidate.revision !== expectedRevision) ||
    (expectedDigest && candidate.digest !== expectedDigest);

  if (candidateChanged) {
    throw new Error('角色候选状态已经变化，请刷新审核页后重新提交修改意见');
  }
  return candidate;
}
