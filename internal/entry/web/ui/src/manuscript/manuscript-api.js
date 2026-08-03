import { request } from '../api.js';
import { manuscriptCacheKey } from './manuscript-state.js';

const base = (projectId) => `/api/projects/${encodeURIComponent(projectId)}/manuscript/workspace`;
const manuscriptCache = new Map();
const manuscriptRequestSequence = new Map();
const manuscriptRelations = new Map();

function requireDTO(condition, message) {
  if (!condition) throw new Error(message);
}

function validateTreeDTO(data) {
  requireDTO(data && Array.isArray(data.nodes), 'invalid manuscript tree response');
  const chapters = new Map();
  for (const volume of data.nodes) {
    requireDTO(volume?.kind === 'volume' && volume.stable_id, 'invalid manuscript volume');
    for (const arc of volume.children || []) {
      requireDTO(arc?.kind === 'arc' && arc.stable_id, 'invalid manuscript arc');
      for (const chapter of arc.children || []) {
        requireDTO(chapter?.kind === 'chapter' && chapter.stable_id, 'invalid manuscript chapter');
        chapters.set(chapter.stable_id, { volumeId: volume.stable_id, arcId: arc.stable_id });
      }
    }
  }
  return chapters;
}

function validateChapterDTO(data, expected) {
  const chapter = data?.chapter;
  requireDTO(chapter?.stable_id === expected.stableId, 'manuscript chapter identity mismatch');
  requireDTO(chapter?.view === expected.view, 'manuscript chapter view mismatch');
  if (expected.version) requireDTO(chapter?.version_id === expected.version || chapter?.revision_id === expected.version, 'manuscript chapter version mismatch');
  requireDTO(typeof chapter?.content_signature === 'string' && chapter.content_signature.length > 0, 'unsigned manuscript chapter response');
  requireDTO(Array.isArray(chapter?.paragraphs), 'invalid manuscript chapter paragraphs');
  requireDTO(chapter.paragraphs.length > 0 || chapter.empty === true, 'empty manuscript chapter response lacks an explicit empty contract');
}

async function cachedRequest(path, key, signal, validate = (data) => requireDTO(data !== null, 'empty manuscript response'), onCommit) {
  const cached = manuscriptCache.get(key);
  const sequence = (manuscriptRequestSequence.get(key) || 0) + 1;
  manuscriptRequestSequence.set(key, sequence);
  const response = await fetch(path, { signal, headers: cached?.etag ? { 'If-None-Match': cached.etag } : {} });
  if (response.status === 304 && cached) return cached.data;
  const text = await response.text();
  const data = text ? JSON.parse(text) : null;
  if (!response.ok) {
    const error = new Error(data?.error?.message || data?.error || `${response.status} ${response.statusText}`);
    error.data = data;
    error.status = response.status;
    if (response.status === 410) manuscriptCache.delete(key);
    throw error;
  }
  const validated = validate(data);
  if (manuscriptRequestSequence.get(key) === sequence) {
    manuscriptCache.set(key, { etag: response.headers.get('ETag') || '', data });
    onCommit?.(validated);
  }
  return data;
}

async function readPost(path, payload, signal) {
  const response = await fetch(path, { method: 'POST', signal, headers: { 'content-type': 'application/json' }, body: JSON.stringify(payload) });
  const text = await response.text();
  const data = text ? JSON.parse(text) : null;
  if (!response.ok) {
    const error = new Error(data?.error?.message || data?.error || `${response.status} ${response.statusText}`);
    error.data = data; error.status = response.status;
    throw error;
  }
  return data;
}

export function invalidateManuscriptCache(projectId) {
  for (const key of manuscriptCache.keys()) if (key.startsWith(`${projectId}|`)) manuscriptCache.delete(key);
  for (const key of manuscriptRequestSequence.keys()) if (key.startsWith(`${projectId}|`)) manuscriptRequestSequence.delete(key);
  manuscriptRelations.delete(projectId);
}

export function invalidateManuscriptViews(projectId, event = {}) {
  const scope = event.scope || event.kind || '';
  const stableId = event.stable_id || '';
  const kinds = scope === 'generation' ? ['tree', 'prose:']
    : scope === 'structure_publish' ? ['tree', 'display', 'outline', 'volume']
      : scope === 'prose_publish' ? ['tree', 'prose:', 'history', 'review', 'review-detail']
        : scope === 'cancel' ? ['tree', 'prose:']
          : scope === 'audit' ? ['tree', 'review', 'review-detail'] : [];
  if (!kinds.length || !stableId) return;
  for (const key of manuscriptCache.keys()) {
    if (!key.startsWith(`${projectId}|`)) continue;
    const [, keyStableId, kind, view] = key.split('|');
    if (kind === 'tree') {
      manuscriptCache.delete(key);
      continue;
    }
    const relation = manuscriptRelations.get(projectId)?.get(stableId);
    const ownsStructureArtifact = scope === 'structure_publish'
      && ((kind === 'volume' && keyStableId === relation?.volumeId)
        || (kind === 'outline' && keyStableId === stableId)
        || (kind === 'display' && (keyStableId === stableId || keyStableId === relation?.volumeId)));
    if (keyStableId !== stableId && !ownsStructureArtifact) continue;
    if (scope === 'generation' && kind.startsWith('prose:') && view !== 'candidate') continue;
    if (scope === 'cancel' && kind.startsWith('prose:') && view !== 'candidate') continue;
    if (scope === 'prose_publish' && kind.startsWith('prose:') && view !== 'current') continue;
    if (kinds.some((prefix) => kind === prefix || kind.startsWith(prefix))) manuscriptCache.delete(key);
  }
}

export const loadManuscriptTree = (projectId, { signal } = {}) => cachedRequest(`${base(projectId)}/tree`, manuscriptCacheKey(projectId, '', 'tree', 'metadata', '', ''), signal, validateTreeDTO, (chapters) => manuscriptRelations.set(projectId, chapters));
export const loadManuscriptRecovery = (projectId, signal) => request(`${base(projectId)}/recovery`, { signal });
export const retryManuscriptRecovery = (projectId, signal) => request(`${base(projectId)}/recovery`, { method: 'POST', signal, body: JSON.stringify({}) });
export const loadManuscriptChunk = (projectId, stableId, { view = 'current', version = '', signature = '', cursor = 0, limit = 40, signal } = {}) => cachedRequest(`${base(projectId)}/chapters/${encodeURIComponent(stableId)}/content?view=${encodeURIComponent(view)}&version=${encodeURIComponent(version)}&cursor=${cursor}&limit=${limit}`, manuscriptCacheKey(projectId, stableId, `prose:${cursor}`, view, version, signature), signal, (data) => validateChapterDTO(data, { stableId, view, version }));
export const saveManualManuscriptCandidate = (projectId, stableId, payload, signal) => readPost(`${base(projectId)}/chapters/${encodeURIComponent(stableId)}/manual-candidate`, payload, signal);
export const loadManuscriptHistory = (projectId, stableId, cursor = 0, signal) => cachedRequest(`${base(projectId)}/history?chapter_id=${encodeURIComponent(stableId)}&cursor=${cursor}&limit=20`, manuscriptCacheKey(projectId, stableId, `history:${cursor}`, 'metadata', '', ''), signal, (data) => requireDTO(Array.isArray(data?.items), 'invalid manuscript history response'));
export const loadManuscriptVersion = (projectId, revisionId, stableId, cursor = 0, signal) => cachedRequest(`${base(projectId)}/versions/${encodeURIComponent(revisionId)}?chapter_id=${encodeURIComponent(stableId)}&cursor=${cursor}&limit=40`, manuscriptCacheKey(projectId, stableId, `history-prose:${cursor}`, 'history', revisionId, ''), signal, (data) => validateChapterDTO(data, { stableId, view: 'history', version: revisionId }));
export const loadManuscriptArtifact = (projectId, kind, stableId, signature = '', signal) => cachedRequest(`${base(projectId)}/artifacts/${encodeURIComponent(kind)}/${encodeURIComponent(stableId)}`, manuscriptCacheKey(projectId, stableId, kind, 'artifact', '', signature), signal, (data) => requireDTO(data?.artifact?.kind === kind && data.artifact.stable_id === stableId && data.artifact.signature, 'invalid manuscript artifact response'));
export const loadManuscriptReviewPage = (projectId, stableId, cursor = 0, signal) => cachedRequest(`${base(projectId)}/artifacts/review/${encodeURIComponent(stableId)}?cursor=${cursor}&limit=20`, manuscriptCacheKey(projectId, stableId, `review:${cursor}`, 'artifact', '', ''), signal, (data) => requireDTO(data?.artifact?.kind === 'review' && data.artifact.stable_id === stableId && data.artifact.signature && Array.isArray(data.artifact.content?.revisions), 'invalid manuscript review page'));
export const loadManuscriptReviewDetail = (projectId, stableId, revisionId, signature = '', signal) => cachedRequest(`${base(projectId)}/artifacts/review/${encodeURIComponent(stableId)}/${encodeURIComponent(revisionId)}`, manuscriptCacheKey(projectId, stableId, 'review-detail', 'artifact', revisionId, signature), signal, (data) => requireDTO(data?.artifact?.kind === 'review_detail' && data.artifact.stable_id === stableId && data.artifact.signature, 'invalid manuscript review response'));
export const previewManuscriptRestore = (projectId, payload, signal) => readPost(`${base(projectId)}/restore/preview`, payload, signal);
export const restoreManuscriptVersion = async (projectId, payload, signal) => {
  const result = await request(`${base(projectId)}/restore`, { method: 'POST', signal, body: JSON.stringify(payload) });
  invalidateManuscriptViews(projectId, { scope: 'generation', stable_id: payload.chapter_id });
  return result;
};
const manuscriptActionBase = (projectId) => `/api/projects/${encodeURIComponent(projectId)}/manuscript/actions/dialogues`;
export const loadActiveManuscriptActionDialogue = (projectId, signal) => request(`${manuscriptActionBase(projectId)}/active`, { signal });
export const createManuscriptActionDialogue = (projectId, payload, signal) => readPost(manuscriptActionBase(projectId), payload, signal);
export const replyManuscriptActionDialogue = (projectId, dialogueId, payload, signal) => readPost(`${manuscriptActionBase(projectId)}/${encodeURIComponent(dialogueId)}/reply`, payload, signal);
export const executeManuscriptActionDialogue = (projectId, dialogueId, payload, signal) => readPost(`${manuscriptActionBase(projectId)}/${encodeURIComponent(dialogueId)}/execute`, payload, signal);
export const cancelManuscriptActionDialogue = (projectId, dialogueId, payload, signal) => readPost(`${manuscriptActionBase(projectId)}/${encodeURIComponent(dialogueId)}/cancel`, payload, signal);

const expansionBase = (projectId) => `/api/projects/${encodeURIComponent(projectId)}/manuscript/expansion`;
export const planManuscriptExpansion = (projectId, payload, signal) => readPost(`${expansionBase(projectId)}/plan`, payload, signal);
export const adjustManuscriptExpansion = (projectId, payload, signal) => readPost(`${expansionBase(projectId)}/adjust`, payload, signal);
export const confirmManuscriptExpansion = (projectId, payload, signal) => readPost(`${expansionBase(projectId)}/confirm`, payload, signal);
export const cancelManuscriptExpansion = (projectId, previewId, expectedRevision, idempotencyKey, signal) => readPost(`${expansionBase(projectId)}/${encodeURIComponent(previewId)}/cancel`, { expected_revision: expectedRevision, idempotency_key: idempotencyKey }, signal);
export const getManuscriptExpansion = (projectId, previewId, signal) => request(`${expansionBase(projectId)}/${encodeURIComponent(previewId)}`, { signal });
export const getExpansionRevision = (projectId, signal) => request(`${expansionBase(projectId)}/revision`, { signal });
export const commandExpansionRevision = (projectId, action, expectedRevision, idempotencyKey, message = '', signal) => readPost(`${expansionBase(projectId)}/revision/command`, { action, message, expected_revision: expectedRevision, idempotency_key: idempotencyKey }, signal);
export const processExpansionRevisionAudit = (projectId, signal) => readPost(`${expansionBase(projectId)}/revision/auditor/process`, {}, signal);
