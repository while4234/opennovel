import { candidateFingerprint, cloneFoundation, normalizeCharacterWorkspace, normalizeFoundationResponse, validateFoundationDraft } from './foundationModel.js';

export const foundationStatuses = [
  'loading', 'clean', 'dirty', 'previewing', 'preview_ready', 'applying', 'auditing',
  'regenerating', 'awaiting_outline_approval', 'completed', 'failed', 'stale', 'readonly'
];

export function createFoundationState(projectId = '') {
  return {
    projectId,
    requestVersion: 0,
    status: 'loading',
    server: null,
    base: null,
    draft: null,
    draftFingerprint: '',
    preview: null,
    previewFingerprint: '',
    validation: { valid: true, fields: {}, summary: [] },
    error: null,
    characterWorkspace: null,
    characterWorkspaceError: null,
    characterReviewStale: false,
    staleServer: null,
    applyIdempotencyKey: '',
    illegalAction: ''
  };
}

export function foundationReducer(state, action) {
  if (action.type === 'load_start') return { ...createFoundationState(action.projectId), requestVersion: action.requestVersion };
  if (action.projectId && state.projectId && action.projectId !== state.projectId) return state;
  if (action.requestVersion != null && action.requestVersion !== state.requestVersion && action.type !== 'load_start') return state;
  switch (action.type) {
    case 'load_success': {
      const server = normalizeFoundationResponse(action.response);
      const base = cloneFoundation(server.targetFoundation);
      const status = serverStatus(server);
      return {
        ...state,
        status,
        server,
        base,
        draft: cloneFoundation(base),
        draftFingerprint: candidateFingerprint(base),
        validation: validateFoundationDraft(base),
        error: null,
        characterWorkspace: null,
        characterWorkspaceError: null,
        characterReviewStale: false,
        staleServer: null
      };
    }
    case 'load_failed':
      return { ...state, status: 'failed', error: action.error };
    case 'edit': {
      if (!state.server?.editable || lockedStatus(state.status)) return reject(state, '当前状态不允许编辑');
      const draft = cloneFoundation(action.draft);
      const fingerprint = candidateFingerprint(draft);
      if (fingerprint === state.draftFingerprint) return state;
      return {
        ...state,
        status: fingerprint === candidateFingerprint(state.base) ? 'clean' : 'dirty',
        draft,
        draftFingerprint: fingerprint,
        preview: null,
        previewFingerprint: '',
        validation: validateFoundationDraft(draft),
        error: null,
        characterReviewStale: state.characterReviewStale || Boolean(state.characterWorkspace?.findings?.length || state.characterWorkspace?.run?.mode === 'review'),
        applyIdempotencyKey: '',
        illegalAction: ''
      };
    }
    case 'character_workspace_success':
      return {
        ...state,
        characterWorkspace: normalizeCharacterWorkspace(action.response),
        characterWorkspaceError: null,
        characterReviewStale: action.forceReviewStale ? true : action.preserveReviewStale ? state.characterReviewStale : false
      };
    case 'character_workspace_failed':
      return { ...state, characterWorkspaceError: action.error };
    case 'character_workspace_clear_error':
      return { ...state, characterWorkspaceError: null };
    case 'preview_start':
      if (state.status !== 'dirty' || !state.validation.valid) return reject(state, '只有通过本地校验的修改草稿可以预览');
      return { ...state, status: 'previewing', error: null, illegalAction: '' };
    case 'preview_success': {
      const previewFingerprint = candidateFingerprint(action.preview?.candidate);
      if (previewFingerprint !== state.draftFingerprint) return reject({ ...state, status: 'dirty' }, '预览候选与当前草稿不一致');
      return { ...state, status: 'preview_ready', preview: action.preview, previewFingerprint, error: null, illegalAction: '' };
    }
    case 'preview_failed':
      return failureState(state, action.error, 'dirty');
    case 'apply_start':
      if (state.status !== 'preview_ready' || !state.preview?.id || state.previewFingerprint !== state.draftFingerprint || !state.preview?.can_apply) {
        return reject(state, '只能应用与当前草稿完全一致且服务端允许的预览');
      }
      return { ...state, status: 'applying', applyIdempotencyKey: action.idempotencyKey, error: null, illegalAction: '' };
    case 'apply_success':
    case 'retry_success':
      return { ...state, status: runtimeStatus(action.revision), server: { ...state.server, activeRevision: action.revision }, error: null, illegalAction: '' };
    case 'apply_failed':
      return failureState(state, action.error, 'preview_ready');
    case 'retry_start':
      if (state.status !== 'failed' || !state.server?.allowedOperations?.includes('retry')) return reject(state, '当前没有可安全重试的修订');
      return { ...state, status: 'regenerating', error: null, illegalAction: '' };
    case 'retry_failed':
      return failureState(state, action.error, 'failed');
    case 'refresh_runtime': {
      const server = normalizeFoundationResponse(action.response);
      const nextStatus = serverStatus(server);
      if (nextStatus === 'completed') return { ...state, status: 'completed', server, error: null };
      return { ...state, status: nextStatus, server, error: null };
    }
    case 'stale_server_loaded':
      return { ...state, status: 'stale', staleServer: normalizeFoundationResponse(action.response), error: action.error || state.error };
    case 'busy_server_loaded': {
      const server = normalizeFoundationResponse(action.response);
      return { ...state, status: serverStatus(server), server, error: action.error || state.error };
    }
    case 'rebase_stale': {
      if (!state.staleServer || !state.draft) return reject(state, '没有可重新对比的服务器基线');
      const server = state.staleServer;
      return {
        ...state,
        status: 'dirty',
        server,
        base: cloneFoundation(server.targetFoundation),
        preview: null,
        previewFingerprint: '',
        staleServer: null,
        error: null,
        applyIdempotencyKey: '',
        validation: validateFoundationDraft(state.draft),
        illegalAction: ''
      };
    }
    default:
      return state;
  }
}

export function canApplyFoundation(state) {
  return state.status === 'preview_ready' && Boolean(state.preview?.id && state.preview?.can_apply) && state.previewFingerprint === state.draftFingerprint;
}

function failureState(state, error, fallback) {
  const code = error?.code || '';
  if (code === 'foundation_stale' || code === 'foundation_source_stale') return { ...state, status: 'stale', error };
  if (code === 'foundation_busy') return { ...state, status: 'readonly', error };
  if (code === 'foundation_readonly') return { ...state, status: 'readonly', error };
  return { ...state, status: fallback, error };
}

function serverStatus(server) {
  const runtime = server.activeRevision;
  if (runtime?.stage) return runtimeStatus(runtime);
  return server.editable ? 'clean' : 'readonly';
}

function runtimeStatus(runtime) {
  switch (runtime?.stage) {
    case 'applying': return 'applying';
    case 'auditing': return 'auditing';
    case 'candidate_generating':
    case 'regenerating': return 'regenerating';
    case 'awaiting_outline_approval': return 'awaiting_outline_approval';
    case 'completed': return 'completed';
    case 'failed': return 'failed';
    default: return runtime?.stage ? 'readonly' : 'clean';
  }
}

function lockedStatus(status) {
  return ['loading', 'previewing', 'applying', 'auditing', 'regenerating', 'awaiting_outline_approval', 'completed', 'failed', 'stale', 'readonly'].includes(status);
}

function reject(state, message) {
  return { ...state, illegalAction: message };
}
