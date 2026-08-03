import { classifyManuscriptMutation } from './manuscript/manuscript-events.js';

export async function request(path, options = {}) {
  const isFormData = options.body instanceof FormData;
  const response = await fetch(path, {
    ...options,
    headers: {
      ...(options.body && !isFormData ? { 'content-type': 'application/json' } : {}),
      ...options.headers
    }
  });
  const text = await response.text();
  const data = text ? JSON.parse(text) : null;
  if (!response.ok) {
    const error = new Error(data?.error?.message || data?.error || `${response.status} ${response.statusText}`);
    error.data = data;
    throw error;
  }
  if (String(options.method || 'GET').toUpperCase() !== 'GET' && path.includes('/manuscript/') && typeof window !== 'undefined') {
    const mutation = classifyManuscriptMutation(path, options, data);
    if (mutation) window.dispatchEvent(new CustomEvent('ainovel:manuscript-mutated', { detail: { path, ...mutation } }));
  }
  return data;
}

function queryPath(path, q) {
  const query = String(q || '').trim();
  return query ? `${path}?q=${encodeURIComponent(query)}` : path;
}

export function getRuntime() {
  return request('/api/runtime');
}

function newIdempotencyKey(scope) {
  const random = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return `${scope}:${random}`;
}

function wait(milliseconds) {
  return new Promise((resolve) => globalThis.setTimeout(resolve, milliseconds));
}

async function persistentAction(path, payload, completedResult) {
  const submitted = await request(path, {
    method: 'POST',
    body: JSON.stringify({
      ...(payload || {}),
      async: true,
      idempotency_key: payload?.idempotency_key || newIdempotencyKey(path)
    })
  });
  if (!submitted?.action_id) {
    return submitted;
  }

  let delay = 400;
  while (true) {
    await wait(delay);
    const current = await request(`${path}?action_id=${encodeURIComponent(submitted.action_id)}`);
    const status = current?.action?.status;
    if (status === 'completed') {
      const result = completedResult ? await completedResult(current) : current;
      return { ...result, action_id: submitted.action_id, action: current.action };
    }
    if (status === 'failed' || status === 'interrupted' || status === 'canceled') {
      const error = new Error(current?.action?.error || '后台任务未完成，可安全重试');
      error.data = current;
      throw error;
    }
    delay = Math.min(Math.round(delay * 1.5), 2000);
  }
}

export function getSetupStatus() {
  return request('/api/setup');
}

export function getObservabilityUsage({ projectId = '', groupBy = 'model', from = '', to = '' } = {}) {
  const params = new URLSearchParams();
  if (projectId) params.set('project_id', projectId);
  if (groupBy) params.set('group_by', groupBy);
  if (from) params.set('from', from);
  if (to) params.set('to', to);
  return request(`/api/observability/usage?${params.toString()}`);
}

export function getObservabilityRecommendations({ projectId = '' } = {}) {
  const params = new URLSearchParams();
  if (projectId) params.set('project_id', projectId);
  return request(`/api/observability/recommendations?${params.toString()}`);
}

export function testSetupModel(payload) {
  return request('/api/setup/test', {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function completeSetup(payload) {
  return request('/api/setup/complete', {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function listStyles() {
  return request('/api/styles');
}

export function listProjects() {
  return request('/api/projects');
}

export function getManuscriptTree(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/manuscript/tree`);
}

export function getManuscriptChapter(projectId, stableId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/manuscript/chapters/${encodeURIComponent(stableId)}`);
}

export function previewManuscriptRevision(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/manuscript/revision/preview`, { method: 'POST', body: JSON.stringify(payload) });
}

export function commandManuscriptRevision(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/manuscript/revision/command`, { method: 'POST', body: JSON.stringify(payload) });
}

export function getManuscriptRevisionBatches(projectId, revisionId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/manuscript/revisions/${encodeURIComponent(revisionId)}/batches`);
}

export function getResumeSchedule() {
  return request('/api/resume-schedule');
}

export function setResumeSchedule(dailyTimes, timezone = 'Asia/Shanghai') {
  return request('/api/resume-schedule', {
    method: 'PUT',
    body: JSON.stringify({ daily_times: dailyTimes, timezone })
  });
}

export function getProjectResumeSchedule(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/resume-schedule`);
}

export function getProjectFoundation(projectId, options = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/foundation`, options);
}

export function previewProjectFoundation(projectId, expectedBaseRevision, expectedBaseAuditSignature, candidate, options = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/foundation/preview`, {
    ...options,
    method: 'POST',
    body: JSON.stringify({
      expected_base_revision: expectedBaseRevision,
      expected_base_audit_signature: expectedBaseAuditSignature,
      candidate
    })
  });
}

export function applyProjectFoundation(projectId, previewId, idempotencyKey = newIdempotencyKey('foundation-apply'), options = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/foundation/apply`, {
    ...options,
    method: 'POST',
    body: JSON.stringify({ preview_id: previewId, idempotency_key: idempotencyKey })
  });
}

export function retryProjectFoundation(projectId, idempotencyKey = newIdempotencyKey('foundation-retry'), options = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/foundation/retry`, {
    ...options,
    method: 'POST',
    body: JSON.stringify({ idempotency_key: idempotencyKey })
  });
}

export function getProjectFoundationCharacters(projectId, runId = '', options = {}) {
  const query = runId ? `?run_id=${encodeURIComponent(runId)}` : '';
  return request(`/api/projects/${encodeURIComponent(projectId)}/foundation/characters${query}`, options);
}

export function analyzeProjectFoundationCharacters(projectId, payload, options = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/foundation/characters/analyze`, {
    ...options,
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function reviewProjectFoundationCharacters(projectId, payload, options = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/foundation/characters/review`, {
    ...options,
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function retryProjectFoundationCharacters(projectId, payload, options = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/foundation/characters/retry`, {
    ...options,
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function discardProjectFoundationCharacters(projectId, payload, options = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/foundation/characters/discard`, {
    ...options,
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function confirmProjectCharacterCards(projectId, payload, options = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/character-cards/confirm`, {
    ...options,
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function setProjectResumeSchedule(projectId, enabled) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/resume-schedule`, {
    method: 'PUT',
    body: JSON.stringify({ enabled })
  });
}

export function listTrashProjects() {
  return request('/api/trash/projects');
}

export function createProject(name, style) {
  return request('/api/projects', {
    method: 'POST',
    body: JSON.stringify({ name, style })
  });
}

export function renameProject(projectId, name) {
  return request(`/api/projects/${encodeURIComponent(projectId)}`, {
    method: 'PATCH',
    body: JSON.stringify({ name })
  });
}

export function cloneProject(projectId, name) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/clone`, {
    method: 'POST',
    body: JSON.stringify({ name })
  });
}

export function trashProject(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}`, {
    method: 'DELETE'
  });
}

export function listProjectTrash() {
  return request('/api/projects/trash');
}

export function clearProjectTrash() {
  return request('/api/projects/trash', {
    method: 'DELETE'
  });
}

export function restoreTrashProject(projectId) {
  return request(`/api/trash/projects/${encodeURIComponent(projectId)}/restore`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function emptyTrashProjects() {
  return request('/api/trash/projects', {
    method: 'DELETE'
  });
}

export function setProjectStyle(projectId, style) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/style`, {
    method: 'PUT',
    body: JSON.stringify({ style })
  });
}

export function setProjectSimulationMode(projectId, mode) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/simulation-mode`, {
    method: 'PUT',
    body: JSON.stringify({ simulation_mode: mode })
  });
}

export function getSnapshot(projectId, { detail = 'full', ...options } = {}) {
  const query = detail === 'summary' ? '?detail=summary' : '';
  return request(`/api/projects/${encodeURIComponent(projectId)}/snapshot${query}`, options);
}

// Project-open and reconnect only need the navigation snapshot. Keep this
// separate from getSnapshot so latency-sensitive paths cannot accidentally
// fall back to the full long-novel payload.
export function getSummarySnapshot(projectId, options = {}) {
  return getSnapshot(projectId, { ...options, detail: 'summary' });
}

export function getChapter(projectId, chapter) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/chapters/${encodeURIComponent(chapter)}`);
}

export function reviseChapter(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/chapters/revise`, {
    method: 'POST',
    body: JSON.stringify(payload || {})
  });
}

export function reviseChapterOutline(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/outline/chapters/revise`, {
    method: 'POST',
    body: JSON.stringify(payload || {})
  });
}

export function resumeProject(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/resume`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function startProject(projectId, text) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/start`, {
    method: 'POST',
    body: JSON.stringify({ text })
  });
}

export function pauseProject(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/pause`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function previewProjectRollback(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/rollback/preview`);
}

export function rollbackProject(projectId, payload = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/rollback`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function continueProject(projectId, text) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/continue`, {
    method: 'POST',
    body: JSON.stringify({ text })
  });
}

export function steerProject(projectId, text) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/steer`, {
    method: 'POST',
    body: JSON.stringify({ text })
  });
}

export function uploadSimulationFiles(projectId, files) {
  const body = new FormData();
  for (const file of files) {
    body.append('files', file);
  }
  return request(`/api/projects/${encodeURIComponent(projectId)}/simulate/files`, {
    method: 'POST',
    body
  });
}

export function searchSimulationSources(projectId, fileName) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/simulate/search`, {
    method: 'POST',
    body: JSON.stringify({ file_name: fileName })
  });
}

export function downloadSimulationSource(projectId, resultId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/simulate/search/download`, {
    method: 'POST',
    body: JSON.stringify({ result_id: resultId })
  });
}

export function analyzeSimulation(projectId, action = 'scan') {
  return request(`/api/projects/${encodeURIComponent(projectId)}/simulate/analyze`, {
    method: 'POST',
    body: JSON.stringify({ action })
  });
}

export function importSimulationProfile(projectId, file) {
  const body = new FormData();
  body.append('profile', file);
  return request(`/api/projects/${encodeURIComponent(projectId)}/simulate/import`, {
    method: 'POST',
    body
  });
}

export function listSimulationLibrary(q) {
  return request(queryPath('/api/libraries/simulation', q));
}

export function uploadSimulationLibrary(files) {
  const body = new FormData();
  for (const file of files) {
    body.append('files', file);
  }
  return request('/api/libraries/simulation/upload', {
    method: 'POST',
    body
  });
}

export function saveSimulationToLibrary(projectId, name) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/simulate/library/save`, {
    method: 'POST',
    body: JSON.stringify({ name })
  });
}

export function loadSimulationFromLibrary(projectId, name) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/simulate/library/load`, {
    method: 'POST',
    body: JSON.stringify({ name })
  });
}

export function importExternalNovel(projectId, file, from = '') {
  const body = new FormData();
  body.append('source', file);
  if (String(from || '').trim()) {
    body.append('from', String(from).trim());
  }
  return request(`/api/projects/${encodeURIComponent(projectId)}/import`, {
    method: 'POST',
    body
  });
}

export function getContinuation(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/continuation`);
}

export function uploadContinuationSource(projectId, file) {
  const body = new FormData();
  body.append('files', file);
  return request(`/api/projects/${encodeURIComponent(projectId)}/continuation/source`, {
    method: 'POST',
    body
  });
}

function continuationMutation(projectId, action, payload = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/continuation/${action}`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function generateContinuationProposal(projectId, payload) {
  const path = `/api/projects/${encodeURIComponent(projectId)}/continuation/proposal/generate`;
  return persistentAction(path, payload, () => getContinuation(projectId));
}

export function reviseContinuationProposal(projectId, payload) {
  return continuationMutation(projectId, 'proposal/revise', payload);
}

export function approveContinuationProposal(projectId, payload) {
  return continuationMutation(projectId, 'proposal/approve', payload);
}

export function reviseContinuationVolumes(projectId, payload) {
  return continuationMutation(projectId, 'volumes/revise', payload);
}

export function approveContinuationVolumes(projectId, payload) {
  return continuationMutation(projectId, 'volumes/approve', payload);
}

export function generateContinuationOutlines(projectId, payload) {
  const path = `/api/projects/${encodeURIComponent(projectId)}/continuation/outlines/generate`;
  return persistentAction(path, payload, () => getContinuation(projectId));
}

export function reviseContinuationOutlines(projectId, payload) {
  return continuationMutation(projectId, 'outlines/revise', payload);
}

export function approveContinuationOutlines(projectId, payload) {
  return continuationMutation(projectId, 'outlines/approve', payload);
}

export function startContinuation(projectId, payload) {
  return continuationMutation(projectId, 'start', payload);
}

export function retryContinuation(projectId, payload) {
  return continuationMutation(projectId, 'retry', payload);
}

export function exportProject(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/export`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export async function exportProjectDownload(projectId, payload) {
  const response = await fetch(`/api/projects/${encodeURIComponent(projectId)}/export/download`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(payload || {})
  });
  if (!response.ok) {
    let data = null;
    try {
      data = await response.json();
    } catch {
      data = null;
    }
    const error = new Error(data?.error || `${response.status} ${response.statusText}`);
    error.data = data;
    throw error;
  }
  const blob = await response.blob();
  const skipped = String(response.headers.get('x-ainovel-export-skipped') || '')
    .split(',')
    .map((item) => Number.parseInt(item, 10))
    .filter((item) => Number.isInteger(item));
  return {
    blob,
    export: {
      name: response.headers.get('x-ainovel-export-name') || '',
      chapters: Number.parseInt(response.headers.get('x-ainovel-export-chapters') || '0', 10) || 0,
      bytes: Number.parseInt(response.headers.get('x-ainovel-export-bytes') || String(blob.size), 10) || blob.size,
      skipped,
      audit_status: response.headers.get('x-ainovel-audit-status') || '',
      audit_digest: response.headers.get('x-ainovel-audit-digest') || ''
    }
  };
}

export function runProjectDiagnostic(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/diag`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function uploadAdaptationSource(projectId, file) {
  const body = new FormData();
  body.append('source', file);
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/source`, {
    method: 'POST',
    body
  });
}

export function analyzeAdaptationSource(projectId, sourceFile, { force = false } = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/analyze`, {
    method: 'POST',
    body: JSON.stringify({ source_file: sourceFile, force })
  });
}

export function startAdaptation(projectId, sourceFile, mode, brief) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/start`, {
    method: 'POST',
    body: JSON.stringify({ source_file: sourceFile, mode, brief })
  });
}

export function listNovelLibrary(q) {
  return request(queryPath('/api/libraries/novels', q));
}

export function saveNovelToLibrary(projectId, name, sourceFile, options = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/library/save`, {
    method: 'POST',
    body: JSON.stringify({ name, source_file: sourceFile, replace: options.replace === true })
  });
}

export function loadNovelFromLibrary(projectId, name) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/library/load`, {
    method: 'POST',
    body: JSON.stringify({ name })
  });
}

export function buildAdaptationProposal(projectId, sourceFile, mode, brief) {
  const path = `/api/projects/${encodeURIComponent(projectId)}/adapt/proposal/volumes`;
  return persistentAction(path, { source_file: sourceFile, mode, brief }, (current) => ({
    ...current,
    mode,
    rewrite_policy: current.snapshot?.adaptation_proposal?.rewrite_policy || ''
  }));
}

export function reviseAdaptationProposal(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/proposal/revise`, {
    method: 'POST',
    body: JSON.stringify(payload || {})
  });
}

export function reviseAdaptationVolumeReview(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/proposal/volumes/revise`, {
    method: 'POST',
    body: JSON.stringify(payload || {})
  });
}

export function confirmAdaptationProposalDetails(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/proposal/details`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function confirmAdaptationProposal(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/confirm`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function getAdaptationAudit(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/audit`);
}

export function runAdaptationAudit(projectId, options = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/audit`, {
    method: 'POST',
    body: JSON.stringify(options)
  });
}

export function applyAdaptationAudit(projectId, confirmation) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/audit/apply`, {
    method: 'POST',
    body: JSON.stringify(confirmation || {})
  });
}

export function listAdaptationAuditRuns(projectId, options = {}) {
  const params = new URLSearchParams();
  if (options.limit) params.set('limit', String(options.limit));
  if (options.cursor) params.set('cursor', String(options.cursor));
  if (options.status) params.set('status', String(options.status));
  if (options.kind) params.set('kind', String(options.kind));
  const query = params.toString();
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/audits${query ? `?${query}` : ''}`);
}

export function getAdaptationAuditRun(projectId, runId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/audits/${encodeURIComponent(runId)}`);
}

export function compareAdaptationAuditRuns(projectId, baseRunId, candidateRunId) {
  const params = new URLSearchParams({ base_run_id: baseRunId, candidate_run_id: candidateRunId });
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/audits/compare?${params.toString()}`);
}

export function estimateSemanticAdaptationAudit(projectId, payload = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/audits/semantic/estimate`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function startSemanticAdaptationAudit(projectId, payload = {}) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/audits/semantic`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function getSemanticAdaptationAudit(projectId, runId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/audits/semantic/${encodeURIComponent(runId)}`);
}

export function getSemanticAdaptationAuditReport(projectId, runId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/audits/semantic/${encodeURIComponent(runId)}/report`);
}

export function cancelSemanticAdaptationAudit(projectId, runId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/audits/semantic/${encodeURIComponent(runId)}`, {
    method: 'DELETE'
  });
}

export function retrySemanticAdaptationAudit(projectId, runId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/audits/semantic/${encodeURIComponent(runId)}/retry`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function beginCoCreate(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/begin`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function sendCoCreate(projectId, text, source = 'custom', options = {}) {
  const payload = { text, source };
  if (options.forceRebrief) {
    payload.force_rebrief = true;
  }
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/send`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function reviseCoCreate(projectId, messageId, text) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/revise`, {
    method: 'POST',
    body: JSON.stringify({ message_id: messageId, text })
  });
}

export function resolveCoCreateDecision(projectId, decisionId, optionId = '', customAnswer = '') {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/decision`, {
    method: 'POST',
    body: JSON.stringify({
      decision_id: decisionId,
      option_id: optionId,
      custom_answer: customAnswer
    })
  });
}

export function resolveCoCreateDecisions(projectId, decisions = []) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/decision`, {
    method: 'POST',
    body: JSON.stringify({ decisions })
  });
}

export function resumeCoCreate(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/resume`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function commitCoCreate(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/commit`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function updateCoreCast(projectId, coreCast, expectedRevision) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/core-cast`, {
    method: 'PUT',
    body: JSON.stringify({ core_cast: coreCast, expected_revision: expectedRevision })
  });
}

export function confirmCoreCast(projectId, expectedRevision, contentSignature) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/core-cast/confirm`, {
    method: 'POST',
    body: JSON.stringify({ expected_revision: expectedRevision, content_signature: contentSignature })
  });
}

export function unconfirmCoreCast(projectId, expectedRevision) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/core-cast/unconfirm`, {
    method: 'POST',
    body: JSON.stringify({ expected_revision: expectedRevision })
  });
}

export function confirmCoCreatePlanning(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/confirm`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function confirmCoCreateFoundation(projectId, expectedRevision, auditSignature) {
	return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/foundation/confirm`, {
		method: 'POST',
		body: JSON.stringify({ expected_revision: expectedRevision, audit_signature: auditSignature })
	});
}

export function reviseCoCreateFoundation(projectId, feedback) {
	return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/foundation/revise`, {
		method: 'POST',
		body: JSON.stringify({ feedback })
	});
}

export function reviseCoCreatePlanning(projectId, payload = {}) {
  return persistentAction(
    `/api/projects/${encodeURIComponent(projectId)}/cocreate/planning/revise`,
    payload
  );
}

export function cancelCoCreate(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/cancel`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function getProjectModels(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models`);
}

export function getProjectEvents(projectId, after = 0) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/events/history?after=${encodeURIComponent(after)}`);
}

export function getGlobalModels() {
  return request('/api/models');
}

export function switchGlobalModel(role, provider, model) {
  return request('/api/models/switch', {
    method: 'POST',
    body: JSON.stringify({ role, provider, model })
  });
}

export function inheritGlobalModel(role) {
  return request('/api/models/switch', {
    method: 'POST',
    body: JSON.stringify({ role, inherit: true })
  });
}

export function setGlobalThinking(role, level) {
  return request('/api/models/thinking', {
    method: 'POST',
    body: JSON.stringify({ role, level })
  });
}

export function switchGlobalDefaultModel(provider, model) {
  return request('/api/models/default', {
    method: 'POST',
    body: JSON.stringify({ provider, model })
  });
}

export function setGlobalCoCreateTimeout(seconds) {
  return request('/api/models/cocreate-timeout', {
    method: 'POST',
    body: JSON.stringify({ seconds })
  });
}

export function setGlobalCoCreateMaxTokens(tokens) {
  return request('/api/models/cocreate-max-tokens', {
    method: 'POST',
    body: JSON.stringify({ tokens })
  });
}

export function setGlobalRetrySettings(modelCallMaxAttempts, structureRepairMaxAttempts, budgetQualityMaxAttempts, adaptationOutlineAuditRetryMaxAttempts) {
  return request('/api/models/retry-settings', {
    method: 'POST',
    body: JSON.stringify({
      model_call_max_attempts: modelCallMaxAttempts,
      structure_repair_max_attempts: structureRepairMaxAttempts,
      budget_quality_max_attempts: budgetQualityMaxAttempts,
      adaptation_outline_audit_retry_max_attempts: adaptationOutlineAuditRetryMaxAttempts
    })
  });
}

export function deleteGlobalProviderModel(provider, model) {
  return request('/api/models', {
    method: 'DELETE',
    body: JSON.stringify({ provider, model })
  });
}

export function switchProjectModel(projectId, role, provider, model) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/switch`, {
    method: 'POST',
    body: JSON.stringify({ role, provider, model })
  });
}

export function inheritProjectModel(projectId, role) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/switch`, {
    method: 'POST',
    body: JSON.stringify({ role, inherit: true })
  });
}

export function setProjectThinking(projectId, role, level) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/thinking`, {
    method: 'POST',
    body: JSON.stringify({ role, level })
  });
}

export function setProjectCoCreateTimeout(projectId, seconds) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/cocreate-timeout`, {
    method: 'POST',
    body: JSON.stringify({ seconds })
  });
}

export function setProjectCoCreateMaxTokens(projectId, tokens) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/cocreate-max-tokens`, {
    method: 'POST',
    body: JSON.stringify({ tokens })
  });
}

export function setProjectRetrySettings(projectId, modelCallMaxAttempts, structureRepairMaxAttempts, budgetQualityMaxAttempts, adaptationOutlineAuditRetryMaxAttempts) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/retry-settings`, {
    method: 'POST',
    body: JSON.stringify({
      model_call_max_attempts: modelCallMaxAttempts,
      structure_repair_max_attempts: structureRepairMaxAttempts,
      budget_quality_max_attempts: budgetQualityMaxAttempts,
      adaptation_outline_audit_retry_max_attempts: adaptationOutlineAuditRetryMaxAttempts
    })
  });
}

export function deleteProviderModel(projectId, provider, model) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models`, {
    method: 'DELETE',
    body: JSON.stringify({ provider, model })
  });
}

export function addOpenAICompatibleModel(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/add-openai-compatible`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function addProviderModel(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/add`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function addGlobalProviderModel(payload) {
  return request('/api/models/add', {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function testGlobalProviderModel(payload) {
  return request('/api/models/test', {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function testProjectProviderModel(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/test`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function discoverGlobalProviderModels(payload) {
  return request('/api/models/discover', {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function discoverProjectProviderModels(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/discover`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

function grokLoginPath(projectId, action) {
  if (!projectId) {
    return `/api/models/grok-login/${action}`;
  }
  return `/api/projects/${encodeURIComponent(projectId)}/models/grok-login/${action}`;
}

export function startGrokLogin(projectId, accountId, accountName, openBrowser = false) {
  return request(grokLoginPath(projectId, 'start'), {
    method: 'POST',
    body: JSON.stringify({ account_id: accountId, account_name: accountName, open_browser: openBrowser })
  });
}

export function pollGrokLogin(projectId) {
  return request(grokLoginPath(projectId, 'poll'), {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function completeGrokLogin(projectId, callback) {
  return request(grokLoginPath(projectId, 'complete'), {
    method: 'POST',
    body: JSON.stringify({ callback })
  });
}

export function getGrokLoginStatus(projectId, accountId) {
  return request(grokLoginPath(projectId, 'status'), {
    method: 'POST',
    body: JSON.stringify({ account_id: accountId })
  });
}

function codexAuthStatusPath(projectId) {
  if (!projectId) {
    return '/api/models/codex-auth/status';
  }
  return `/api/projects/${encodeURIComponent(projectId)}/models/codex-auth/status`;
}

export function getCodexAuthStatus(projectId, authFile = '') {
  return request(codexAuthStatusPath(projectId), {
    method: 'POST',
    body: JSON.stringify({ auth_file: authFile })
  });
}

export function uploadCodexAuthFile(file) {
  const body = new FormData();
  body.append('auth_file', file);
  return request('/api/models/codex-auth/upload', {
    method: 'POST',
    body
  });
}

export function getBackendStatus(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/backend/status`);
}

export function testBackend(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/backend/test`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}
