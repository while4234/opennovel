export function createCoCreateState() {
  return {
    kind: 'normal',
    active: false,
    input: '',
    inputSource: '',
    messages: [],
    draftPrompt: '',
    ready: false,
    canStart: false,
    suggestions: [],
    streamThinking: '',
    streamReply: '',
    intakeActive: false,
    intakeInitial: '',
    targetTotalWordsChoice: '',
    customTargetTotalWords: '',
    structureChoice: 'single',
    status: 'idle',
    startMessage: '',
    error: '',
    failed: false,
    adaptMode: '',
    rewritePolicy: '',
    modeLocked: false,
    briefing: null,
    pendingDecisions: [],
    blockedReason: '',
    coreCast: null,
    sourceMajorCharacters: [],
    castCompletion: { complete: false, missing: [], blocking_reasons: [] },
    castConfirmed: false,
    castSignature: '',
    blockingReasons: []
  };
}

export function shouldShowGlobalCoCreate(options = {}) {
  const { snapshot, coCreate, planningReview } = options || {};
  const safeSnapshot = snapshot || {};
  const safeCoCreate = coCreate || {};
  const safePlanningReview = planningReview || {};
  const phase = String(safeSnapshot.Phase || safeSnapshot.phase || '').trim().toLowerCase();
  const kind = String(safeCoCreate.kind || 'normal').trim().toLowerCase();
  if (kind === 'continuation') return false;
  const supportedFlow = kind === 'normal' || kind === 'adapt';
  const unfinished = supportedFlow && safeCoCreate.status !== 'started' && Boolean(
    safeCoCreate.active ||
    safeCoCreate.intakeActive ||
    safeCoCreate.failed ||
    safeCoCreate.streamThinking ||
    safeCoCreate.streamReply ||
    (Array.isArray(safeCoCreate.messages) && safeCoCreate.messages.length)
  );
  const firstCreationWindow = phase === '' || phase === 'init' || phase === 'premise' || phase === 'outline';
  return Boolean(safePlanningReview.active || unfinished || firstCreationWindow);
}

export function coCreateStateFromResponse(response, previous = createCoCreateState(), options = {}) {
  return coCreateStateFromBackend(response?.cocreate || {}, previous, options);
}

export function coCreateStateFromEvent(event, previous = createCoCreateState()) {
  return coCreateStateFromBackend(event?.cocreate || {}, previous, {
    preserveError: true,
    preserveInput: true
  });
}

export function coCreateStateFromError(error, previous = createCoCreateState()) {
  return coCreateStateFromBackend(error?.data?.cocreate || {}, previous, {
    preserveInput: true,
    status: 'error',
    error: error?.message || 'co-create failed'
  });
}

export function coCreateStateFromBackend(data, previous = createCoCreateState(), options = {}) {
  const hasBackendState = Boolean(data && Object.keys(data).length);
  if (!hasBackendState) {
    return {
      ...previous,
      status: options.status || previous.status,
      failed: options.status === 'error' ? true : previous.failed,
      error: options.error ?? previous.error
    };
  }
  const messages = Array.isArray(data.messages) ? data.messages : [];
  const streamThinking = data.stream_thinking || '';
  const streamReply = data.stream_reply || '';
  const canStart = coCreateCanStartFromBackend(data);
  const suggestions = visibleCoCreateSuggestions({
    messages,
    suggestions: data.suggestions,
    streamReply
  });
  const pendingDecisions = Array.isArray(data.pending_decisions) ? data.pending_decisions : [];
  const status = options.status || coCreateStatusFromBackend(data, streamThinking, streamReply, canStart);
  return {
    ...previous,
    kind: data.kind || previous.kind || 'normal',
    active: Boolean(data.active && !data.committed_label),
    messages,
    draftPrompt: data.draft_prompt || '',
    ready: Boolean(data.ready),
    canStart,
    suggestions,
    streamThinking,
    streamReply,
    intakeActive: false,
    intakeInitial: '',
    targetTotalWordsChoice: previous.targetTotalWordsChoice || '',
    customTargetTotalWords: previous.customTargetTotalWords || '',
    structureChoice: previous.structureChoice || 'single',
    status,
    startMessage: data.committed_label || previous.startMessage || '',
    failed: Boolean(data.failed),
    error: options.error ?? (options.preserveError ? previous.error : ''),
    adaptMode: data.adapt_mode || '',
    rewritePolicy: data.rewrite_policy || '',
    modeLocked: Boolean(data.mode_locked),
    briefing: data.briefing || null,
    pendingDecisions,
    blockedReason: data.blocked_reason || '',
    coreCast: data.core_cast || null,
    sourceMajorCharacters: Array.isArray(data.source_major_characters) ? data.source_major_characters : [],
    castCompletion: data.cast_completion || { complete: false, missing: [], blocking_reasons: [] },
    castConfirmed: Boolean(data.cast_confirmed),
    castSignature: data.cast_signature || '',
    blockingReasons: Array.isArray(data.blocking_reasons) ? data.blocking_reasons : [],
    input: options.preserveInput ? previous.input : '',
    inputSource: options.preserveInput ? previous.inputSource || '' : ''
  };
}

function coCreateCanStartFromBackend(data) {
  if (Object.prototype.hasOwnProperty.call(data, 'can_start')) {
    return Boolean(data.can_start);
  }
  return Boolean(data.ready || String(data.draft_prompt || '').trim());
}

function coCreateStatusFromBackend(data, streamThinking, streamReply, canStart) {
  if (data.committed_label) {
    return 'started';
  }
  if (canStart) {
    return 'ready';
  }
  if (data.failed) {
    return 'error';
  }
  if (Array.isArray(data.pending_decisions) && data.pending_decisions.length > 0) {
    return 'deciding';
  }
  if (streamThinking || streamReply) {
    return 'running';
  }
  return data.active ? 'waiting' : 'idle';
}

export function visibleCoCreateSuggestions({ suggestions = [] } = {}) {
  return normalizeCoCreateSuggestions(suggestions);
}

function normalizeCoCreateSuggestions(suggestions) {
  if (!Array.isArray(suggestions)) {
    return [];
  }
  return uniqueNonEmpty(suggestions.map(cleanSuggestionText)).slice(0, 3);
}

function cleanSuggestionText(text) {
  return String(text || '')
    .replace(/\*\*/g, '')
    .replace(/^[：:，,、\s]+/g, '')
    .replace(/[？?。；;：:，,、]+$/g, '')
    .trim();
}

function uniqueNonEmpty(values) {
  const seen = new Set();
  const out = [];
  for (const value of values) {
    const text = cleanSuggestionText(value);
    if (!text || seen.has(text)) {
      continue;
    }
    seen.add(text);
    out.push(text);
  }
  return out;
}

export function applyCoCreateSuggestion(state, suggestion) {
  return {
    ...state,
    input: String(suggestion || ''),
    inputSource: 'suggestion'
  };
}

export function appendCoCreateInput(state, text) {
  return {
    ...state,
    input: text,
    inputSource: 'custom'
  };
}
