import {
  analyzeProjectFoundationCharacters,
  applyProjectFoundation,
  confirmProjectCharacterCards,
  discardProjectFoundationCharacters,
  getProjectFoundation,
  getProjectFoundationCharacters,
  previewProjectFoundation,
  retryProjectFoundation,
  retryProjectFoundationCharacters,
  reviewProjectFoundationCharacters
} from '../api.js';
import { normalizeFoundation } from './foundationModel.js';

export async function loadFoundation(projectId, signal) {
  return getProjectFoundation(projectId, { signal });
}

export async function previewFoundation(projectId, server, candidate, signal) {
  return previewProjectFoundation(projectId, server.baseRevision, server.baseAuditSignature, candidate, { signal });
}

export async function applyFoundation(projectId, previewId, idempotencyKey, signal) {
  return applyProjectFoundation(projectId, previewId, idempotencyKey, { signal });
}

export async function retryFoundation(projectId, idempotencyKey, signal) {
  return retryProjectFoundation(projectId, idempotencyKey, { signal });
}

export function loadCharacterWorkspace(projectId, runId = '', signal) {
  return getProjectFoundationCharacters(projectId, runId, { signal });
}

export async function analyzeCharacters(projectId, server, candidate, {
  candidateRevision = 0, characterIDs = [], instruction = '', allowSupportingCharacters = false, idempotencyKey, signal
} = {}) {
  const candidateDigest = await characterCandidateDigest(candidate);
  const persistedCandidateRevision = Number(candidateRevision || 0);
  return analyzeProjectFoundationCharacters(projectId, {
    expected_base_revision: server.baseRevision,
    expected_base_audit_signature: server.baseAuditSignature,
    idempotency_key: idempotencyKey,
    scope: { character_ids: characterIDs },
    instruction: String(instruction || '').trim(),
    allow_supporting_characters: Boolean(allowSupportingCharacters),
    ...(persistedCandidateRevision > 0 ? {} : { candidate: stripSourceFields(candidate) }),
    candidate_revision: persistedCandidateRevision,
    candidate_digest: candidateDigest
  }, { signal });
}

export async function reviewCharacters(projectId, server, candidate, {
  candidateRevision = 0, sourceMappings = [], idempotencyKey, signal
} = {}) {
  const candidateDigest = await characterCandidateDigest(candidate);
  const persistedCandidateRevision = Number(candidateRevision || 0);
  return reviewProjectFoundationCharacters(projectId, {
    expected_base_revision: server.baseRevision,
    expected_base_audit_signature: server.baseAuditSignature,
    idempotency_key: idempotencyKey,
    ...(persistedCandidateRevision > 0 ? {} : { candidate: stripSourceFields(candidate) }),
    candidate_revision: persistedCandidateRevision,
    candidate_digest: candidateDigest,
    source_mappings: sourceMappings
  }, { signal });
}

export function retryCharacterWorkspace(projectId, server, run, idempotencyKey, signal) {
  return retryProjectFoundationCharacters(projectId, {
    expected_base_revision: server.baseRevision,
    expected_base_audit_signature: server.baseAuditSignature,
    run_id: run.run_id,
    candidate_digest: run.input_candidate_digest || server.currentDigest,
    idempotency_key: idempotencyKey
  }, { signal });
}

export function discardCharacterWorkspace(projectId, server, workspace, idempotencyKey, signal) {
  return discardProjectFoundationCharacters(projectId, {
    expected_base_revision: server.baseRevision,
    expected_base_audit_signature: server.baseAuditSignature,
    run_id: workspace.run?.run_id || '',
    candidate_digest: workspace.candidate?.digest || workspace.currentDigest,
    idempotency_key: idempotencyKey
  }, { signal });
}

export function confirmCharacterCandidate(projectId, candidate, idempotencyKey, signal) {
  return confirmProjectCharacterCards(projectId, {
    expected_candidate_revision: Number(candidate?.revision || 0),
    candidate_digest: String(candidate?.digest || ''),
    idempotency_key: idempotencyKey
  }, { signal });
}

export async function characterCandidateDigest(value) {
  const canonical = characterDigestPayload(value);
  const goCompatibleJSON = JSON.stringify(canonical)
    .replaceAll('&', '\\u0026')
    .replaceAll('<', '\\u003c')
    .replaceAll('>', '\\u003e')
    .replaceAll('\u2028', '\\u2028')
    .replaceAll('\u2029', '\\u2029');
  const bytes = new TextEncoder().encode(goCompatibleJSON);
  const digest = await globalThis.crypto.subtle.digest('SHA-256', bytes);
  return [...new Uint8Array(digest)].map((item) => item.toString(16).padStart(2, '0')).join('');
}

export function foundationError(error) {
  const envelope = error?.data?.error;
  return {
    code: String(envelope?.code || 'foundation_network_error'),
    message: String(envelope?.message || error?.message || 'Foundation 请求失败')
  };
}

export function foundationIdempotencyKey(scope) {
  const random = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return `foundation-${scope}:${random}`;
}

function stripSourceFields(value) {
  const foundation = normalizeFoundation(value);
  delete foundation.source_foundation;
  delete foundation.source_mappings;
  return foundation;
}

function characterDigestPayload(value) {
  const foundation = normalizeFoundation(value);
  return {
    characters: foundation.characters
      .map(canonicalCharacter)
      .sort((left, right) => lexical(left.id, right.id)),
    relationships: foundation.relationships
      .map(canonicalRelationship)
      .sort((left, right) => lexical(left.id, right.id))
  };
}

function canonicalCharacter(value) {
  const result = {
    ...(value.id ? { id: value.id.trim() } : {}),
    name: value.name.trim(),
    ...(value.aliases.length ? { aliases: canonicalStrings(value.aliases) } : {}),
    role: value.role.trim(),
    gender: value.gender,
    description: value.description.trim(),
    arc: value.arc.trim(),
    traits: canonicalStrings(value.traits),
    ...(value.tier ? { tier: value.tier.trim() } : {}),
    ...(value.faction ? { faction: value.faction.trim() } : {}),
    ...(value.goal ? { goal: value.goal.trim() } : {}),
    ...(value.motivation ? { motivation: value.motivation.trim() } : {}),
    ...(value.conflict ? { conflict: value.conflict.trim() } : {}),
    ...(value.voice ? { voice: value.voice.trim() } : {}),
    ...(value.constraints.length ? { constraints: canonicalStrings(value.constraints) } : {})
  };
  const contrasts = value.contrast_details
    .map((item) => ({ surface: item.surface.trim(), depth: item.depth.trim() }))
    .filter((item) => item.surface || item.depth)
    .sort((left, right) => lexical(`${left.surface}\0${left.depth}`, `${right.surface}\0${right.depth}`));
  const backstory = value.key_backstory
    .map((item) => ({ event: item.event.trim(), impact: item.impact.trim() }))
    .filter((item) => item.event || item.impact)
    .sort((left, right) => lexical(`${left.event}\0${left.impact}`, `${right.event}\0${right.impact}`));
  if (contrasts.length) result.contrast_details = contrasts;
  if (backstory.length) result.key_backstory = backstory;
  if (value.initial_state) {
    const initial = compactObject({
      identity: value.initial_state.identity.trim(),
      situation: value.initial_state.situation.trim(),
      emotion: value.initial_state.emotion.trim(),
      resources: canonicalStrings(value.initial_state.resources),
      relationships: value.initial_state.relationships.trim()
    });
    if (Object.keys(initial).length) result.initial_state = initial;
  }
  if (value.knowledge_boundary) {
    const knowledge = compactObject({
      known: canonicalStrings(value.knowledge_boundary.known),
      unknown: canonicalStrings(value.knowledge_boundary.unknown),
      misconceptions: canonicalStrings(value.knowledge_boundary.misconceptions),
      forbidden: canonicalStrings(value.knowledge_boundary.forbidden)
    });
    if (Object.keys(knowledge).length) result.knowledge_boundary = knowledge;
  }
  if (value.notes) result.notes = value.notes.trim();
  return result;
}

function canonicalRelationship(value) {
  return {
    ...(value.id ? { id: value.id.trim() } : {}),
    source_character_id: value.source_character_id.trim(),
    target_character_id: value.target_character_id.trim(),
    type: value.type,
    ...(value.label ? { label: value.label.trim() } : {}),
    direction: value.direction,
    status: value.status,
    ...(value.description ? { description: value.description.trim() } : {}),
    ...(value.since ? { since: value.since.trim() } : {}),
    ...(value.tags.length ? { tags: canonicalStrings(value.tags) } : {}),
    ...(value.constraints.length ? { constraints: canonicalStrings(value.constraints) } : {})
  };
}

function canonicalStrings(values) {
  return [...new Map(values.map((item) => {
    const trimmed = String(item || '').trim();
    return [trimmed.toLocaleLowerCase().replace(/\s+/g, ' '), trimmed];
  }).filter(([, item]) => item)).values()]
    .sort((left, right) => lexical(left.toLocaleLowerCase().replace(/\s+/g, ' '), right.toLocaleLowerCase().replace(/\s+/g, ' ')));
}

function compactObject(value) {
  return Object.fromEntries(Object.entries(value).filter(([, item]) => Array.isArray(item) ? item.length : item));
}

function lexical(left, right) {
  return left < right ? -1 : left > right ? 1 : 0;
}
