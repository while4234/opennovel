const MUTATION_SCOPES = new Set(['generation', 'structure_publish', 'prose_publish', 'cancel', 'audit']);

function requestPayload(options = {}) {
  if (!options.body || typeof options.body !== 'string') return {};
  try { return JSON.parse(options.body); } catch { return {}; }
}

function responseStableId(response) {
  return response?.revision?.baseline?.chapter_id
    || response?.revision?.queue?.[0]?.chapter_id
    || response?.chapter?.stable_id
    || '';
}

export function normalizeManuscriptMutationEvent(event) {
	const projection = event?.manuscript_mutation;
	const scope = String(projection?.scope || '');
	const stableId = String(projection?.stable_id || '');
  if (!MUTATION_SCOPES.has(scope)) return null;
  if (!stableId) return null;
  return { scope, stable_id: stableId };
}

export function classifyManuscriptMutation(path, options = {}, response = null) {
  const payload = requestPayload(options);
  const action = String(payload.action || '');
  let scope = '';
  if (path.includes('/manuscript/revision/command')) {
    if (action === 'generate') scope = 'generation';
    else if (action === 'audit') scope = 'audit';
    else if (action === 'publish' || action === 'approve' || action === 'revalidate_completion') scope = 'prose_publish';
    else if (action === 'cancel') scope = 'cancel';
    else if (action === 'confirm_impacts') scope = 'structure_publish';
  } else if (path.endsWith('/manuscript/workspace/restore')) {
    scope = 'generation';
  }
  return normalizeManuscriptMutationEvent({ manuscript_mutation: {
		scope,
		stable_id: payload.chapter_id || payload.stable_id || responseStableId(response)
	} });
}

export function manuscriptMutationSSEData(event) {
	return JSON.stringify(manuscriptMutationWebEvent(event));
}

export function manuscriptMutationWebEvent(event, { seq = 1, projectId = 'browser-project', time = '2026-07-16T00:00:00Z' } = {}) {
	const scope = String(event?.scope || '');
	const stableId = String(event?.stable_id || '');
	if (!MUTATION_SCOPES.has(scope) || !stableId) throw new Error('invalid manuscript mutation event');
	return { seq, type: 'action', project_id: projectId, time, manuscript_mutation: { scope, stable_id: stableId } };
}
