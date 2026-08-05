import {
  Activity,
  BookOpen,
  Check,
  ChevronLeft,
  ChevronRight,
  CircleDot,
  Clock3,
  Copy,
  Database,
  Download,
  FileText,
  FileJson,
  ListRestart,
  MessageSquareText,
  MoreHorizontal,
  PauseCircle,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  RotateCcw,
  Send,
  Server,
  Settings,
  SlidersHorizontal,
  SquarePen,
  TestTube2,
  Trash2,
  Upload,
  WandSparkles,
  X
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  MobileToolDetailHeader,
  MobileToolMenu,
  MobileWorkspaceChrome,
  mobileToolLabel
} from './components/MobileWorkspaceChrome.jsx';
import { shouldShowWorkflowProgressPanel, WorkflowProgressPanel } from './workflow-progress.jsx';
import { ManuscriptWorkspace } from './manuscript/ManuscriptWorkspace.jsx';
import {
  analyzeAdaptationSource,
  analyzeSimulation,
  applyAdaptationAudit,
  cancelSemanticAdaptationAudit,
  addGlobalProviderModel,
  approveContinuationOutlines,
  approveContinuationProposal,
  approveContinuationVolumes,
  beginCoCreate,
  buildAdaptationProposal,
  cancelCoCreate,
  commitCoCreate,
  confirmCoreCast,
	confirmCoCreateFoundation,
  confirmCoCreatePlanning,
  confirmAdaptationProposal,
  confirmAdaptationProposalDetails,
  completeGrokLogin,
  completeSetup,
  continueProject,
  cloneProject,
  createProject,
  deleteGlobalProviderModel,
  deleteProviderModel,
  discoverGlobalProviderModels,
  discoverProjectProviderModels,
  downloadSimulationSource,
  emptyTrashProjects,
  exportProjectDownload,
  generateContinuationProposal,
  getBackendStatus,
  getCodexAuthStatus,
  getContinuation,
  getAdaptationAudit,
  getAdaptationAuditRun,
  getSemanticAdaptationAudit,
  getGlobalModels,
  getChapter,
  getGrokLoginStatus,
  getObservabilityRecommendations,
  getObservabilityUsage,
  getProjectModels,
  getProjectResumeSchedule,
  getResumeSchedule,
  getRuntime,
  getSetupStatus,
  getSnapshot,
  getSummarySnapshot,
  inheritGlobalModel,
  inheritProjectModel,
  importSimulationProfile,
  listNovelLibrary,
  listAdaptationAuditRuns,
  listProjects,
  listSimulationLibrary,
  listStyles,
  listTrashProjects,
  loadNovelFromLibrary,
  loadSimulationFromLibrary,
  pauseProject,
  pollGrokLogin,
  previewProjectRollback,
  renameProject,
  restoreTrashProject,
  retrySemanticAdaptationAudit,
  reviseAdaptationProposal,
  reviseAdaptationVolumeReview,
  reviseChapter,
  reviseChapterOutline,
  reviseCoCreatePlanning,
	reviseCoCreateFoundation,
  reviseCoCreate,
  reviseContinuationOutlines,
  reviseContinuationProposal,
  reviseContinuationVolumes,
  resolveCoCreateDecisions,
  resolveCoCreateDecision,
  resumeProject,
  rollbackProject,
  runProjectDiagnostic,
  runAdaptationAudit,
  compareAdaptationAuditRuns,
  estimateSemanticAdaptationAudit,
  saveNovelToLibrary,
  saveSimulationToLibrary,
  sendCoCreate,
  searchSimulationSources,
  setGlobalCoCreateMaxTokens,
  setGlobalCoCreateTimeout,
  setGlobalRetrySettings,
  setGlobalThinking,
  setProjectCoCreateMaxTokens,
  setProjectCoCreateTimeout,
  setProjectRetrySettings,
  setProjectSimulationMode,
  setProjectResumeSchedule,
  setProjectStyle,
  setResumeSchedule,
  setProjectThinking,
  startContinuation,
  startSemanticAdaptationAudit,
  startProject,
  startGrokLogin,
  steerProject,
  switchGlobalDefaultModel,
  switchGlobalModel,
  switchProjectModel,
  testBackend,
  testGlobalProviderModel,
  testProjectProviderModel,
  testSetupModel,
  trashProject,
  uploadAdaptationSource,
  uploadCodexAuthFile,
  uploadContinuationSource,
  uploadSimulationLibrary,
  uploadSimulationFiles,
  updateCoreCast,
  unconfirmCoreCast
} from './api.js';
import { AuditRunWorkbench } from './AuditRunWorkbench.jsx';
import {
  adaptationAuditApplicationText,
  adaptationAuditScopeText,
  buildAdaptationAuditApplyRequest,
  buildAdaptationAuditOptions,
  defaultAdaptationAuditScope,
  normalizedAuditSourceChapters,
  normalizedAdaptationAuditReport
} from './adaptation-audit.js';
import {
  appendCoCreateInput,
  applyCoCreateSuggestion,
  coCreateStateFromError,
  coCreateStateFromEvent,
  coCreateStateFromResponse,
  createCoCreateState,
  shouldShowGlobalCoCreate
} from './cocreate.js';
import {
  buildContinuationOutlineScopePayload,
  continuationCanResume,
  continuationCanRetry,
  continuationNeedsConfirmation,
  continuationNeedsReview,
  continuationReviewKind,
  continuationSnapshotFrom,
  continuationUploadSuccessMessage,
  deriveContinuationSteps,
  normalizeContinuationSnapshot,
  withExpectedRevision
} from './continuation.js';
import { createWorkbenchState, eventStatus, mergeSnapshotUpdate, reduceWebEvent, reduceWebEvents, visibleStreamRounds } from './events.js';
import { isSSEConnectionStale, nextSSEReconnectDelay, parseSSEMessage } from './sse.js';
import { cacheHitLabel, usageConfidence, usageCoverage } from './usage-ui.js';
import { UsageObservabilityTable } from './usage-observability.jsx';
import { CoreCastEditor } from './components/CoreCastEditor.jsx';
import { FoundationCenter } from './foundation/FoundationCenter.jsx';
import { projectNextAction } from './foundation/characterConfirmation.js';
import {
  confirmCharacterCandidate,
  foundationError,
  foundationIdempotencyKey
} from './foundation/foundationApi.js';
import { foundationReadonlyReasonLabel } from './foundation/foundationModel.js';
import FoundationReviewWorkspace from './foundation/FoundationReviewWorkspace.jsx';
import { startReviewedCharacterRevision } from './foundation/reviewedCharacterRevision.js';

const eventTypes = ['host_event', 'stream_delta', 'stream_clear', 'snapshot', 'cocreate_state'];
const SSE_WATCHDOG_INTERVAL_MS = 5_000;
export const PROJECT_OPEN_TIMEOUT_MS = 90_000;

const coCreateTargetWordChoices = [
  { value: '5000', label: '短篇 5,000', hint: '通常不分章节' },
  { value: '30000', label: '中篇 30,000', hint: '约 6-10 章' },
  { value: '100000', label: '长篇 100,000', hint: '约 20-30 章' },
  { value: 'custom', label: '自定义', hint: '输入总字数' }
];

const coCreateStructureChoices = [
  { value: 'single', label: '不分章节', hint: '一气呵成' },
  { value: 'auto', label: 'AI 判断', hint: '按篇幅规划' },
  { value: 'chapters', label: '分章节', hint: '每章 3000-5000 字' }
];

function readWindowScrollPosition() {
  if (typeof window === 'undefined') {
    return null;
  }
  return { x: window.scrollX || 0, y: window.scrollY || 0 };
}

function restoreWindowScrollPosition(position) {
  if (!position || typeof window === 'undefined') {
    return;
  }
  const restore = () => window.scrollTo(position.x, position.y);
  restore();
  if (typeof window.requestAnimationFrame === 'function') {
    window.requestAnimationFrame(restore);
  }
  window.setTimeout(restore, 0);
}

function runWithWindowScrollPreserved(action) {
  const position = readWindowScrollPosition();
  const result = action?.();
  restoreWindowScrollPosition(position);
  return result;
}

function createSimulationState() {
  return {
    files: [],
    uploadMessage: '',
    analysisStatus: 'idle',
    analysisEvents: [],
    importStatus: 'idle',
    importEvents: [],
    importMessage: '',
    sourceSearchQuery: '',
    sourceSearchStatus: 'idle',
    sourceSearchResults: [],
    sourceSearchMessage: '',
    sourceSearchError: '',
    sourceDownloadID: '',
    libraryQuery: '',
    libraryStatus: 'idle',
    libraryItems: [],
    libraryMessage: '',
    libraryError: '',
    saveName: '',
    saveStatus: 'idle',
    saveError: '',
    error: ''
  };
}

function createAdaptationState() {
  return {
    sourceFile: null,
    uploadStatus: 'idle',
    uploadMessage: '',
    analysisStatus: 'idle',
    analysisEvents: [],
    mode: 'chapter',
    brief: '',
    proposalKey: '',
    startStatus: 'idle',
    startMessage: '',
    revisionMode: 'chapter',
    revisionChapter: '1',
    revisionFromChapter: '1',
    revisionToChapter: '1',
    revisionVolume: '1',
    revisionInstruction: '',
    revisionStatus: 'idle',
    revisionMessage: '',
    libraryQuery: '',
    libraryStatus: 'idle',
    libraryItems: [],
    libraryMessage: '',
    libraryError: '',
    librarySaveName: '',
    libraryLoadedName: '',
    librarySaveStatus: 'idle',
    librarySaveError: '',
    error: ''
  };
}

function createChapterRevisionState() {
  return {
    chapter: '1',
    mode: 'rewrite',
    instruction: '',
    status: 'idle',
    message: '',
    error: ''
  };
}

function createAdaptationAuditState() {
  return {
    ...defaultAdaptationAuditScope,
    sourceChapters: [],
    auditableScope: null,
    report: null,
    runs: [],
    selectedRunId: '',
    comparison: null,
    semantic: {
      provider: '',
      model: '',
      reasoningEffort: '',
      maxCostUsd: '',
      maxCalls: '100',
      acknowledgeUnknownPrice: false,
      compareBaseRunId: '',
      estimate: null,
      run: null,
      error: ''
    },
    application: null,
    status: 'idle',
    message: '',
    error: '',
    acknowledged: false
  };
}

function createOutlineRevisionState() {
  return {
    chapter: '1',
    instruction: '',
    active: false,
    status: 'idle',
    message: '',
    error: ''
  };
}

function createCoCreatePlanningRevisionState() {
  return {
    feedback: '',
    characterFeedback: '',
    allowSupportingCharacters: false,
    instruction: '',
    scope: 'all',
    chapter: '1',
    volumeIndex: '1',
    status: 'idle',
    message: '',
    error: ''
  };
}

function createChapterContentState() {
  return {
    projectId: '',
    chapter: '',
    status: 'idle',
    content: '',
    wordCount: 0,
    source: '',
    error: ''
  };
}

function resetSimulationProjectState(previous) {
  return {
    ...createSimulationState(),
    libraryQuery: previous.libraryQuery,
    libraryStatus: previous.libraryStatus,
    libraryItems: previous.libraryItems,
    libraryMessage: previous.libraryMessage,
    libraryError: previous.libraryError
  };
}

export function restoreSimulationProjectState(previous, status) {
  const next = resetSimulationProjectState(previous);
  if (!status) {
    return next;
  }
  const importedFile = status.imported_file || status.importedFile || null;
  const analysisEvents = status.analysis_events || status.analysisEvents;
  const analysisStatus = String(status.analysis_status || status.analysisStatus || next.analysisStatus || 'idle').trim() || 'idle';
  const importEvents = status.import_events || status.importEvents;
  const importStatus = String(status.import_status || status.importStatus || (importedFile ? 'done' : next.importStatus) || 'idle').trim() || 'idle';
  return {
    ...next,
    files: simulationFilesFromResponse(status),
    analysisStatus,
    analysisEvents: Array.isArray(analysisEvents) ? analysisEvents : [],
    importStatus,
    importEvents: Array.isArray(importEvents) ? importEvents : [],
    importMessage: importedFile ? `已恢复画像：${simulationProfileLabel(importedFile)}` : ''
  };
}

function simulationProfileLabel(file) {
  const name = textValue(file, 'name', 'Name', 'relative_path', 'RelativePath');
  const fileName = fileNameFromPath(name) || name;
  return fileName.replace(/\.json$/i, '') || fileName;
}

function resetAdaptationProjectState(previous) {
  return {
    ...createAdaptationState(),
    libraryQuery: previous.libraryQuery,
    libraryStatus: previous.libraryStatus,
    libraryItems: previous.libraryItems,
    libraryMessage: previous.libraryMessage,
    libraryError: previous.libraryError
  };
}

function restoreAdaptationProjectState(previous, status, snapshot) {
  const next = resetAdaptationProjectState(previous);
  if (!status) {
    return applyAdaptationProposalSnapshot(next, snapshot);
  }
  const sourceFile = status.source_file || status.sourceFile || null;
  const analysisStatus = String(status.analysis_status || status.analysisStatus || next.analysisStatus || 'idle').trim() || 'idle';
  return applyAdaptationProposalSnapshot({
    ...next,
    sourceFile,
    uploadMessage: status.message || (sourceFile ? '已恢复上传原文' : ''),
    analysisStatus,
    analysisEvents: Array.isArray(status.analysis_events || status.analysisEvents)
      ? (status.analysis_events || status.analysisEvents)
    : []
  }, snapshot);
}

function appendWorkflowEvent(events, event, limit = 200) {
  const next = [...(Array.isArray(events) ? events : []), event];
  return next.slice(-limit);
}

function workflowEventFromHostEvent(webEvent) {
  const event = webEvent?.event || {};
  const failed = event.failed || event.level === 'error';
  return {
    time: event.time || webEvent?.time || new Date().toISOString(),
    stage: event.kind || '',
    current: 0,
    total: 0,
    message: event.summary || event.detail || '',
    error: failed ? (event.detail || event.summary || '') : ''
  };
}

function workflowStatusFromHostEvent(webEvent) {
  const event = webEvent?.event || {};
  const stage = String(event.kind || '').toLowerCase();
  if (event.failed || event.level === 'error' || stage === 'error') {
    return 'error';
  }
  if (stage === 'paused') {
    return 'paused';
  }
  if (event.level === 'success' || stage === 'done') {
    return 'done';
  }
  return 'running';
}

export function applyHostEventToSimulationState(previous, webEvent) {
  const event = webEvent?.event || {};
  if (String(event.category || '').toUpperCase() !== 'SIMULATE') {
    return previous;
  }
  const stage = String(event.kind || '').toLowerCase();
  const workflowEvent = workflowEventFromHostEvent(webEvent);
  const status = workflowStatusFromHostEvent(webEvent);
  const importEvent = stage === 'import' ||
    (previous.importStatus === 'running' && previous.analysisStatus !== 'running' && ['merge', 'done', 'error'].includes(stage));
  if (importEvent) {
    return {
      ...previous,
      importStatus: status,
      importEvents: appendWorkflowEvent(previous.importEvents, workflowEvent),
      importMessage: status === 'done' ? (workflowEvent.message || previous.importMessage) : previous.importMessage,
      error: status === 'error' ? (workflowEvent.error || workflowEvent.message) : previous.error
    };
  }
  return {
    ...previous,
    analysisStatus: status,
    analysisEvents: appendWorkflowEvent(previous.analysisEvents, workflowEvent),
    saveStatus: status === 'done' ? 'idle' : previous.saveStatus,
    saveError: status === 'done' ? '' : previous.saveError,
    error: status === 'error' ? (workflowEvent.error || workflowEvent.message) : previous.error
  };
}

function applyHostEventToAdaptationState(previous, webEvent) {
  const event = webEvent?.event || {};
  if (String(event.category || '').toUpperCase() !== 'ADAPT') {
    return previous;
  }
  const stage = String(event.kind || '').toLowerCase();
  const sourceAnalysisStage = ['splitting', 'foundation', 'chapter', 'dossier', 'paused'].includes(stage) ||
    ((stage === 'done' || stage === 'error') && previous.analysisStatus === 'running');
  if (!sourceAnalysisStage) {
    return previous;
  }
  const workflowEvent = workflowEventFromHostEvent(webEvent);
  const status = workflowStatusFromHostEvent(webEvent);
  return {
    ...previous,
    analysisStatus: status,
    analysisEvents: appendWorkflowEvent(previous.analysisEvents, workflowEvent),
    error: status === 'error' ? (workflowEvent.error || workflowEvent.message) : previous.error
  };
}

export function applyAdaptationProposalSnapshot(previous, snapshot) {
  const review = getAdaptationProposalReview(snapshot);
  if (!review.loaded) {
    return previous;
  }
  const mode = review.granularity || previous.mode;
  const brief = review.brief || previous.brief;
  const sourceFile = previous.sourceFile;
  const next = {
    ...previous,
    mode,
    brief,
    startStatus: 'done',
    startMessage: review.confirmed ? '提案已确认，写作已启动' : '提案已生成，可在改编面板确认启动',
    revisionChapter: clampChapterSelection(previous.revisionChapter, review.chapterCount),
    revisionFromChapter: clampChapterSelection(previous.revisionFromChapter, review.chapterCount),
    revisionToChapter: clampChapterSelection(previous.revisionToChapter, review.chapterCount),
    revisionVolume: clampVolumeSelection(previous.revisionVolume, review.volumeReview?.volumes?.length || review.volumes.length),
    error: ''
  };
  if (mode && brief) {
    next.proposalKey = buildAdaptationProposalKey({ sourceFile, mode, brief });
  }
  return next;
}

function createContinuationState() {
  return {
    workflow: null,
    sourceFile: null,
    status: 'idle',
    events: [],
    message: '',
    error: '',
    actionStatus: 'idle',
    actionMessage: '',
    instruction: '',
    scope: 'all',
    chapter: '',
    fromChapter: '',
    toChapter: '',
    volumeIndex: '1'
  };
}

function createSetupState() {
  return {
    loading: true,
    required: false,
    providers: [],
    provider: 'openrouter',
    type: 'openai',
    model: '',
    apiKey: '',
    baseURL: 'https://openrouter.ai/api/v1',
    testStatus: 'idle',
    message: '',
    error: ''
  };
}

function restoreContinuationState(previous, response, snapshot = null) {
  const workflow = continuationSnapshotFrom(response, continuationSnapshotFrom(snapshot, previous.workflow));
  const normalized = normalizeContinuationSnapshot(workflow);
  const sourceFile = response?.source_file || response?.sourceFile || normalized.sourceFile || previous.sourceFile;
  return {
    ...previous,
    workflow,
    sourceFile,
    status: normalized.exists ? 'done' : previous.status,
    error: '',
    actionStatus: 'idle'
  };
}

function createExportState() {
  return {
    path: '',
    format: 'txt',
    from: '',
    to: '',
    purpose: 'preview',
    audit: null,
    status: 'idle',
    result: null,
    message: '',
    error: ''
  };
}

function createDiagnosticState() {
  return {
    status: 'idle',
    report: null,
    runtime: null,
    exportPath: '',
    error: ''
  };
}

export function createProjectSettingsState() {
  return {
    styles: [],
    defaultStyle: '',
    selectedStyle: '',
    selectedSimulationMode: 'normal',
    loadStatus: 'idle',
    saveStatus: 'idle',
    simulationModeSaveStatus: 'idle',
    scheduledResumeEnabled: true,
    scheduledResumeLoadStatus: 'idle',
    scheduledResumeSaveStatus: 'idle',
    scheduledResumeError: '',
    message: '',
    error: '',
    simulationModeMessage: '',
    simulationModeError: ''
  };
}

function resetProjectSettingsForProject(previous) {
  return {
    ...previous,
    selectedStyle: '',
    selectedSimulationMode: 'normal',
    saveStatus: 'idle',
    simulationModeSaveStatus: 'idle',
    scheduledResumeEnabled: true,
    scheduledResumeLoadStatus: 'idle',
    scheduledResumeSaveStatus: 'idle',
    scheduledResumeError: '',
    message: '',
    error: '',
    simulationModeMessage: '',
    simulationModeError: ''
  };
}

export function normalizeDailyResumeTime(value) {
  const time = String(value || '').trim();
  return /^(?:[01]\d|2[0-3]):[0-5]\d$/.test(time) ? time : '';
}

export function normalizeDailyResumeTimes(values) {
  return [...new Set((Array.isArray(values) ? values : [])
    .map(normalizeDailyResumeTime)
    .filter(Boolean))].sort();
}

function createResumeScheduleState() {
  return {
    dailyTimes: [],
    draftTime: '15:00',
    timezone: 'Asia/Shanghai',
    nextTriggerAt: '',
    lastBatch: null,
    status: 'idle',
    message: '',
    error: ''
  };
}

export function normalizeResumeScheduleResponse(data) {
  const schedule = data?.schedule || data?.config || data || {};
  return {
    dailyTimes: normalizeDailyResumeTimes(schedule.daily_times || schedule.dailyTimes),
    timezone: String(schedule.timezone || 'Asia/Shanghai').trim() || 'Asia/Shanghai',
    nextTriggerAt: String(data?.next_trigger_at || data?.next_run_at || data?.nextTriggerAt || ''),
    lastBatch: data?.last_batch || data?.lastBatch || null
  };
}

export function projectScheduledResumeEnabled(data) {
  const value = data?.scheduled_resume_enabled ?? data?.enabled;
  return value !== false;
}

const grokOAuthDefaults = {
  provider: 'grok-oauth',
  type: 'grok',
  auth: 'grok_oauth',
  account_id: 'default',
  account_name: 'Default',
  model: 'grok-4.3-latest'
};

const codexAuthDefaults = {
  provider: 'codex-login',
  type: 'openai',
  auth: 'codex',
  template_provider: 'codex',
  model: 'gpt-5.5',
  base_url: 'https://chatgpt.com/backend-api/codex'
};

const defaultModelRequestTimeoutSeconds = '120';
const defaultModelConnectivityTimeoutSeconds = '12';
const defaultModelNetworkDisconnectMaxAttempts = '7';

function createCustomModelState() {
  return {
    mode: 'existing',
    role: 'default',
    provider: '',
    original_provider: '',
    preset: 'deepseek',
    label: '',
    template_provider: 'deepseek',
    type: 'openai',
    model: '',
	model_reasoning_effort: '',
    base_url: '',
    api_key: '',
    api: 'chat',
    use_proxy: false,
    request_timeout_seconds: defaultModelRequestTimeoutSeconds,
    connectivity_timeout_seconds: defaultModelConnectivityTimeoutSeconds,
    network_disconnect_max_attempts: defaultModelNetworkDisconnectMaxAttempts,
    auto_switch_candidate_pool: false,
    discovered_models: [],
    test_status: 'idle',
    test_scope: '',
    test_message: '',
    account_id: grokOAuthDefaults.account_id,
    account_name: grokOAuthDefaults.account_name,
    callback_input: '',
    grok_login: null,
    grok_status: null,
    grok_message: '',
    auth_file: '',
    auth_file_name: '',
    codex_status: null,
    codex_message: ''
  };
}

const providerPresets = [
  { label: 'DeepSeek', provider: 'deepseek', type: 'deepseek', model: 'deepseek-chat', requiresKey: true, useProxy: false },
  { label: 'Codex', provider: 'codex', type: 'openai', api: 'responses', model: 'gpt-5.1-codex', requiresKey: true, useProxy: true },
  { label: 'Grok API Key', provider: 'grok', type: 'grok', model: 'grok-4.3-latest', requiresKey: true, useProxy: true },
  { label: 'OpenAI', provider: 'openai', type: 'openai', model: 'gpt-4.1', requiresKey: true, useProxy: false },
  { label: 'Anthropic', provider: 'anthropic', type: 'anthropic', model: 'claude-sonnet-4-5', requiresKey: true, useProxy: false },
  { label: 'Gemini', provider: 'gemini', type: 'gemini', model: 'gemini-2.5-pro', requiresKey: true, useProxy: false },
  { label: 'Qwen', provider: 'qwen', type: 'qwen', model: 'qwen-max', requiresKey: true, useProxy: false },
  { label: 'GLM', provider: 'glm', type: 'glm', model: 'glm-4.5', requiresKey: true, useProxy: false },
  { label: 'OpenRouter', provider: 'openrouter', type: 'openrouter', model: 'openai/gpt-4.1', requiresKey: true, useProxy: false },
  { label: 'Ollama', provider: 'ollama', type: 'ollama', base_url: 'http://localhost:11434', model: 'qwen3:8b', requiresKey: false, useProxy: false }
];

const customProviderTypes = ['openai', 'anthropic', 'gemini', 'grok'];

export function restoreProjectWorkbenchSnapshot(previous = createWorkbenchState(), snapshot = null, events = [], latestEventSeq = 0) {
  const restored = reduceWebEvents(previous, events);
  return {
    ...restored,
    lastSeq: Math.max(restored.lastSeq, Number(latestEventSeq) || 0),
    snapshot: snapshot || restored.snapshot
  };
}

export function isProjectScopedResponseCurrent(projectId, activeProjectId, requestEpoch, currentEpoch) {
  const expectedProjectId = String(projectId || '').trim();
  if (!expectedProjectId || expectedProjectId !== String(activeProjectId || '').trim()) {
    return false;
  }
  if (requestEpoch === undefined || currentEpoch === undefined) {
    return true;
  }
  return requestEpoch === currentEpoch;
}

export function resumeNoOpMessage(response = {}) {
  if (response?.running !== false) {
    return '';
  }
  const label = String(response?.label || '').trim();
  return label ? `恢复未启动：${label}` : '恢复未启动，请检查当前阶段后重试。';
}

export function prepareProjectOpenSnapshot(requestSeq, currentRequestSeq, snapshotData) {
  if (requestSeq !== currentRequestSeq || !snapshotData?.project?.id) {
    return null;
  }
  return {
    project: snapshotData.project,
    workbench: restoreProjectWorkbenchSnapshot(
      createWorkbenchState(),
      snapshotData.snapshot,
      snapshotData.events,
      snapshotData.latest_event_seq
    )
  };
}

export function shouldHydrateProjectOpenSnapshot(snapshot) {
  return getCoCreatePlanningReview(snapshot).active;
}

export function shouldHydratePendingPlanningReviewDetails(snapshot) {
  const review = getCoCreatePlanningReview(snapshot);
  if (!review.pending || review.kind !== 'chapter_outline' || review.chapters.length === 0) {
    return false;
  }
  return review.chapters.some((chapter) => (
    !chapter.coreEvent && !chapter.hook && chapter.scenes.length === 0
  ));
}

export default function App() {
  const [setup, setSetup] = useState(() => ({
    ...createSetupState(),
    loading: typeof window !== 'undefined'
  }));
  const [runtime, setRuntime] = useState(null);
  const [projects, setProjects] = useState([]);
  const [trashProjects, setTrashProjects] = useState([]);
  const [trashOpen, setTrashOpen] = useState(false);
  const [activeProject, setActiveProject] = useState(null);
  const [projectOpen, setProjectOpen] = useState({ status: 'idle', project: null, error: '' });
  const [workbench, setWorkbench] = useState(createWorkbenchState);
  const [newProjectName, setNewProjectName] = useState('');
  const [projectMenu, setProjectMenu] = useState(null);
  const [renameDialog, setRenameDialog] = useState(null);
  const [cloneDialog, setCloneDialog] = useState(null);
  const [deleteDialog, setDeleteDialog] = useState(null);
  const [rollbackDialog, setRollbackDialog] = useState(null);
  const [composerText, setComposerText] = useState('');
  const [eventsOpen, setEventsOpen] = useState(true);
  const [steerText, setSteerText] = useState('');
  const [sideView, setSideView] = useState('status');
  const [centerView, setCenterView] = useState('writing');
  const [foundationDraftDirty, setFoundationDraftDirty] = useState(false);
  const [foundationNavigation, setFoundationNavigation] = useState({
    projectId: '', tab: '', anchor: '', requestId: 0
  });
  const [pendingFoundationProject, setPendingFoundationProject] = useState(null);
  const [manuscriptControlsTarget, setManuscriptControlsTarget] = useState(null);
  const [projectDrawerOpen, setProjectDrawerOpen] = useState(false);
  const [toolDrawerOpen, setToolDrawerOpen] = useState(false);
  const [mobileActionsOpen, setMobileActionsOpen] = useState(false);
  const [mobileToolView, setMobileToolView] = useState('menu');
  const [simulation, setSimulation] = useState(createSimulationState);
  const [adaptation, setAdaptation] = useState(createAdaptationState);
  const [adaptationAudit, setAdaptationAudit] = useState(createAdaptationAuditState);
  const [chapterRevision, setChapterRevision] = useState(createChapterRevisionState);
  const [outlineRevision, setOutlineRevision] = useState(createOutlineRevisionState);
  const [chapterContent, setChapterContent] = useState(createChapterContentState);
  const [continuation, setContinuation] = useState(createContinuationState);
  const [exportJob, setExportJob] = useState(createExportState);
  const [diagnostic, setDiagnostic] = useState(createDiagnosticState);
  const [coCreate, setCoCreate] = useState(createCoCreateState);
  const [planningRevision, setPlanningRevision] = useState(createCoCreatePlanningRevisionState);
  const [foundationReviewTab, setFoundationReviewTab] = useState('overview');
  const [projectSettings, setProjectSettings] = useState(createProjectSettingsState);
  const [resumeSchedule, setResumeScheduleState] = useState(createResumeScheduleState);
  const [modelConfig, setModelConfig] = useState(null);
  const [customModel, setCustomModel] = useState(createCustomModelState);
  const [backendStatus, setBackendStatus] = useState(null);
  const [connection, setConnection] = useState('idle');
  const [usageAnalytics, setUsageAnalytics] = useState({ status: 'idle', report: null, recommendations: [], error: '' });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
	const lastSeqRef = useRef(0);
	const projectOpenSeqRef = useRef(0);
  const projectOpenAbortRef = useRef(null);
  const activeProjectIdRef = useRef('');
  const planningReviewHydrationProjectRef = useRef('');
	const planningMutationSeqRef = useRef(0);
	const planningRevisionInFlightProjectRef = useRef('');
  const outlineRevisionHydrationRef = useRef('');
  const forceFoundationProjectOpenRef = useRef('');
  const mobileOverlayTriggerRef = useRef(null);

  const snapshot = workbench.snapshot;
  const currentProjectNextAction = useMemo(() => projectNextAction(snapshot), [snapshot]);
  const continuationSnapshot = useMemo(
    () => normalizeContinuationSnapshot(snapshot, continuation.workflow),
    [snapshot, continuation.workflow]
  );
  const quickStartAvailable = Boolean(activeProject && isFreshProject(snapshot));
  const projectRunning = isProjectRunning(snapshot);
  const adaptationProposalReview = useMemo(
    () => getVisibleAdaptationProposalReview(snapshot, adaptation),
    [snapshot, adaptation]
  );
  const coCreatePlanningReview = useMemo(
    () => getCoCreatePlanningReview(snapshot),
    [snapshot]
  );
  useEffect(() => {
    if (coCreatePlanningReview.kind === 'foundation') {
      setFoundationReviewTab(coCreatePlanningReview.characterConfirmationRequired ? 'characters' : 'overview');
    }
  }, [
    coCreatePlanningReview.characterConfirmationRequired,
    coCreatePlanningReview.foundationGeneration,
    coCreatePlanningReview.foundationRevision,
    coCreatePlanningReview.kind
  ]);
  const coCreateRequestBusy = isCoCreateRequestBusy(coCreate);
  const projectBusy = busy || coCreateRequestBusy || projectOpen.status === 'loading';
  const currentProjectStyle = useMemo(
    () => resolveProjectStyleID(snapshot, runtime, projectSettings),
    [snapshot, runtime, projectSettings.defaultStyle, projectSettings.styles]
  );
  const currentProjectSimulationMode = useMemo(
    () => resolveProjectSimulationMode(snapshot, runtime),
    [snapshot, runtime]
  );
  const showAdaptationProposalWorkspace = sideView === 'adapt' && adaptationProposalReview.proposalReady;
  const selectedChapterRevisionView = useMemo(
    () => getCompletedBookSelectedChapterView(snapshot, chapterRevision),
    [snapshot, chapterRevision.chapter]
  );
  const selectedOutlineRevisionView = useMemo(
    () => getOutlineRevisionView(snapshot, outlineRevision),
    [snapshot, outlineRevision.active, outlineRevision.chapter]
  );
  const showOutlineRevisionWorkspace = sideView === 'status' && selectedOutlineRevisionView.active;
  const showChapterRevisionWorkspace = sideView === 'status' && !showOutlineRevisionWorkspace && selectedChapterRevisionView.visible;
  const continuationCoCreateActive = coCreate.kind === 'continuation' && coCreate.status !== 'started' && Boolean(coCreate.active || coCreate.ready || coCreate.draftPrompt || hasCoCreateWorkspaceContent(coCreate));
  const showContinuationCoCreateWorkspace = sideView === 'continuation' && continuationCoCreateActive;
  const showContinuationWorkspace = sideView === 'continuation' && continuationSnapshot.exists && !showContinuationCoCreateWorkspace;

	const resetProjectScopedState = useCallback((clearProject = false) => {
		lastSeqRef.current = 0;
    outlineRevisionHydrationRef.current = '';
    setBusy(false);
    setWorkbench(createWorkbenchState());
    setSimulation(resetSimulationProjectState);
    setAdaptation(resetAdaptationProjectState);
    setAdaptationAudit(createAdaptationAuditState());
    setChapterRevision(createChapterRevisionState());
    setOutlineRevision(createOutlineRevisionState());
    setChapterContent(createChapterContentState());
    setContinuation(createContinuationState());
    setExportJob(createExportState());
    setDiagnostic(createDiagnosticState());
    setCoCreate(createCoCreateState());
    setPlanningRevision(createCoCreatePlanningRevisionState());
    setFoundationReviewTab('overview');
    setFoundationNavigation({ projectId: '', tab: '', anchor: '', requestId: 0 });
    setRollbackDialog(null);
    setProjectSettings(resetProjectSettingsForProject);
    setModelConfig(null);
    setCustomModel(createCustomModelState());
    setBackendStatus(null);
    if (clearProject) {
      activeProjectIdRef.current = '';
      setActiveProject(null);
    }
  }, []);

  useEffect(() => {
    activeProjectIdRef.current = activeProject?.id || '';
  }, [activeProject?.id]);

  useEffect(() => {
    setProjectSettings((previous) => {
      if (!activeProject?.id || previous.selectedStyle === currentProjectStyle) {
        return previous;
      }
      return {
        ...previous,
        selectedStyle: currentProjectStyle,
        saveStatus: 'idle',
        message: '',
        error: ''
      };
    });
  }, [activeProject?.id, currentProjectStyle]);

  useEffect(() => {
    setProjectSettings((previous) => {
      if (!activeProject?.id || previous.selectedSimulationMode === currentProjectSimulationMode) {
        return previous;
      }
      return {
        ...previous,
        selectedSimulationMode: currentProjectSimulationMode,
        simulationModeSaveStatus: 'idle',
        simulationModeMessage: '',
        simulationModeError: ''
      };
    });
  }, [activeProject?.id, currentProjectSimulationMode]);

  const isCurrentProject = useCallback((projectId) => (
    isProjectScopedResponseCurrent(projectId, activeProjectIdRef.current)
  ), []);

  const pendingPlanningReviewNeedsHydration = shouldHydratePendingPlanningReviewDetails(snapshot);
  const outlineRevisionNeedsHydration = shouldHydrateOutlineRevisionDetails(snapshot, outlineRevision);

  useEffect(() => {
    const projectId = activeProject?.id;
    if (!projectId || projectOpen.status === 'loading' || !pendingPlanningReviewNeedsHydration ||
        planningReviewHydrationProjectRef.current === projectId) {
      return undefined;
    }

    const controller = new AbortController();
    planningReviewHydrationProjectRef.current = projectId;
    getSnapshot(projectId, { signal: controller.signal })
      .then((data) => {
        if (controller.signal.aborted || !isCurrentProject(projectId) || !data?.snapshot) {
          return;
        }
        setWorkbench((previous) => {
          if (!shouldHydratePendingPlanningReviewDetails(previous.snapshot)) {
            return previous;
          }
          return {
            ...previous,
            snapshot: mergeSnapshotUpdate(data.snapshot, previous.snapshot)
          };
        });
      })
      .catch((err) => {
        if (!controller.signal.aborted && isCurrentProject(projectId)) {
          setError(`详细章节大纲加载失败：${err.message}`);
        }
      })
      .finally(() => {
        if (planningReviewHydrationProjectRef.current === projectId) {
          planningReviewHydrationProjectRef.current = '';
        }
      });

    return () => controller.abort();
  }, [activeProject?.id, isCurrentProject, pendingPlanningReviewNeedsHydration, projectOpen.status]);

  useEffect(() => {
    const projectId = activeProject?.id;
    const chapter = selectedOutlineRevisionView.chapter;
    const hydrationKey = projectId && chapter ? `${projectId}:${chapter}` : '';
    if (!hydrationKey || projectOpen.status === 'loading' || !outlineRevisionNeedsHydration ||
        outlineRevisionHydrationRef.current === hydrationKey) {
      return undefined;
    }

    const controller = new AbortController();
    outlineRevisionHydrationRef.current = hydrationKey;
    getSnapshot(projectId, { signal: controller.signal })
      .then((data) => {
        if (controller.signal.aborted || !isCurrentProject(projectId) || !data?.snapshot) {
          return;
        }
        setWorkbench((previous) => {
          const selection = { active: true, chapter: String(chapter) };
          if (!shouldHydrateOutlineRevisionDetails(previous.snapshot, selection)) {
            return previous;
          }
          return {
            ...previous,
            snapshot: mergeSnapshotUpdate(data.snapshot, previous.snapshot)
          };
        });
      })
      .catch((err) => {
        if (!controller.signal.aborted && isCurrentProject(projectId)) {
          setError(`详细章节细纲加载失败：${err.message}`);
        }
      })
      .finally(() => {
        if (outlineRevisionHydrationRef.current === hydrationKey) {
          outlineRevisionHydrationRef.current = '';
        }
      });

    return () => controller.abort();
  }, [
    activeProject?.id,
    isCurrentProject,
    outlineRevisionNeedsHydration,
    projectOpen.status,
    selectedOutlineRevisionView.chapter
  ]);

  const refreshCurrentProjectSnapshot = useCallback(async (projectId) => {
    if (!isCurrentProject(projectId)) {
      return;
    }
    try {
      const snapshotData = await getSummarySnapshot(projectId);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({ ...previous, snapshot: snapshotData.snapshot || previous.snapshot }));
    } catch {
      // Keep the original workflow error visible when snapshot refresh also fails.
    }
  }, [isCurrentProject]);

  const loadAdaptationAuditReport = useCallback(async (projectId) => {
    if (!projectId || !isCurrentProject(projectId)) {
      return;
    }
    setAdaptationAudit((previous) => ({
      ...previous,
      status: 'loading',
      error: ''
    }));
    try {
      // OpenProject refreshes the project manifest. Keep these requests
      // sequential so Windows never races two atomic project.json renames.
      const data = await getAdaptationAudit(projectId);
      const history = await listAdaptationAuditRuns(projectId, { limit: 50 });
      if (!isCurrentProject(projectId)) {
        return;
      }
      const report = normalizedAdaptationAuditReport(data?.report);
      setAdaptationAudit((previous) => ({
        ...previous,
        sourceChapters: normalizedAuditSourceChapters(data?.source_chapters),
        auditableScope: data?.auditable_scope || null,
        report,
        runs: Array.isArray(history?.runs) ? history.runs : [],
        selectedRunId: history?.runs?.[0]?.run_id || '',
        comparison: null,
        application: null,
        acknowledged: false,
        status: 'done',
        message: report ? '已加载最近一次只读审计报告。' : '尚未生成审计报告。',
        error: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setAdaptationAudit((previous) => ({
        ...previous,
        status: 'error',
        error: err.message
      }));
    }
  }, [isCurrentProject]);

  const runAdaptationAuditReport = async () => {
    const projectId = activeProject?.id;
    if (!projectId) {
      return;
    }
    if (projectRunning) {
      setAdaptationAudit((previous) => ({
        ...previous,
        status: 'error',
        error: '请先暂停项目。审计不会自动暂停、恢复或生成正文。'
      }));
      return;
    }
    if (coCreate.active) {
      setAdaptationAudit((previous) => ({
        ...previous,
        status: 'error',
        error: '请先结束或取消共创流程，再运行改编审计。'
      }));
      return;
    }
    const request = buildAdaptationAuditOptions(adaptationAudit);
    if (!request.ok) {
      setAdaptationAudit((previous) => ({ ...previous, status: 'error', error: request.error }));
      return;
    }
    setAdaptationAudit((previous) => ({
      ...previous,
      status: 'running',
      message: '',
      error: '',
      application: null,
      acknowledged: false
    }));
    try {
      const data = await runAdaptationAudit(projectId, request.options);
      if (!isCurrentProject(projectId)) {
        return;
      }
      const history = await listAdaptationAuditRuns(projectId, { limit: 50 });
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setAdaptationAudit((previous) => ({
        ...previous,
        report: normalizedAdaptationAuditReport(data?.report),
        runs: Array.isArray(history?.runs) ? history.runs : previous.runs,
        selectedRunId: data?.run_id || data?.run?.run_id || history?.runs?.[0]?.run_id || previous.selectedRunId,
        comparison: null,
        auditableScope: data?.auditable_scope || data?.report?.scope || previous.auditableScope,
        status: 'done',
        message: '只读审计已完成；尚未修改计划或正文。',
        error: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setAdaptationAudit((previous) => ({ ...previous, status: 'error', error: err.message }));
    }
  };

  const applyAdaptationAuditRepair = async () => {
    const projectId = activeProject?.id;
    if (!projectId) {
      return;
    }
    if (projectRunning) {
      setAdaptationAudit((previous) => ({
        ...previous,
        status: 'error',
        error: '请先暂停项目；修复计划不会自动暂停或恢复项目。'
      }));
      return;
    }
    if (coCreate.active) {
      setAdaptationAudit((previous) => ({
        ...previous,
        status: 'error',
        error: '请先结束或取消共创流程，再应用改编修复计划。'
      }));
      return;
    }
    const request = buildAdaptationAuditApplyRequest(adaptationAudit.report, adaptationAudit.acknowledged);
    if (!request.ok) {
      setAdaptationAudit((previous) => ({ ...previous, status: 'error', error: request.error }));
      return;
    }
    if (adaptationAudit.selectedRunId) {
      request.confirmation.run_id = adaptationAudit.selectedRunId;
    }
    const blockingCount = request.confirmation.acknowledged_finding_ids.length;
    if (!window.confirm(`将创建改编元数据备份、修订章节计划，并把受影响的既有章节排入返工队列。\n\n已确认 ${blockingCount} 个阻塞问题。此操作不会立即改写正文，也不会自动恢复项目。\n\n确认应用修复计划？`)) {
      return;
    }
    setAdaptationAudit((previous) => ({ ...previous, status: 'running', message: '', error: '' }));
    try {
      const data = await applyAdaptationAudit(projectId, request.confirmation);
      if (!isCurrentProject(projectId)) {
        return;
      }
      const application = data?.application || null;
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setAdaptationAudit((previous) => ({
        ...previous,
        application,
        status: 'done',
        message: adaptationAuditApplicationText(application),
        error: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setAdaptationAudit((previous) => ({ ...previous, status: 'error', error: err.message }));
    }
  };

  useEffect(() => {
    if (sideView !== 'audit' || !activeProject?.id) {
      return;
    }
    loadAdaptationAuditReport(activeProject.id);
  }, [activeProject?.id, loadAdaptationAuditReport, sideView]);

  useEffect(() => {
    const projectId = activeProject?.id;
    if (sideView !== 'continuation' || !projectId || projectOpen.status === 'loading') {
      return undefined;
    }
    let disposed = false;
    setContinuation((previous) => ({ ...previous, status: 'loading', error: '' }));
    getContinuation(projectId).then((data) => {
      if (disposed || !isCurrentProject(projectId)) {
        return;
      }
      setContinuation((previous) => {
        const restored = restoreContinuationState(previous, data);
        return {
          ...restored,
          status: normalizeContinuationSnapshot(restored.workflow).exists ? 'done' : 'idle'
        };
      });
    }).catch((err) => {
      if (disposed || !isCurrentProject(projectId)) {
        return;
      }
      setContinuation((previous) => ({
        ...previous,
        status: normalizeContinuationSnapshot(previous.workflow).exists ? 'done' : 'idle',
        error: err.message
      }));
    });
    return () => {
      disposed = true;
    };
  }, [activeProject?.id, isCurrentProject, projectOpen.status, sideView]);

  useEffect(() => {
    const projectId = activeProject?.id;
    const run = adaptationAudit.semantic?.run;
    if (!projectId || !run?.run_id || !['queued', 'running'].includes(String(run.status || '').toLowerCase())) return undefined;
    let disposed = false;
    const poll = async () => {
      try {
        const data = await getSemanticAdaptationAudit(projectId, run.run_id);
        if (disposed || !isCurrentProject(projectId)) return;
        const nextRun = data?.run || data;
        setAdaptationAudit((previous) => ({ ...previous, semantic: { ...previous.semantic, run: nextRun, error: '' } }));
        if (String(nextRun?.status || '').toLowerCase() === 'completed') {
          const history = await listAdaptationAuditRuns(projectId, { limit: 50 });
          if (!disposed) setAdaptationAudit((previous) => ({ ...previous, runs: history?.runs || previous.runs }));
        }
      } catch (err) {
        if (!disposed) setAdaptationAudit((previous) => ({ ...previous, semantic: { ...previous.semantic, error: err.message } }));
      }
    };
    const timer = window.setInterval(poll, 1500);
    poll();
    return () => { disposed = true; window.clearInterval(timer); };
  }, [activeProject?.id, adaptationAudit.semantic?.run?.run_id, adaptationAudit.semantic?.run?.status, isCurrentProject]);

  useEffect(() => {
    if (sideView !== 'cache') {
      return undefined;
    }
    let disposed = false;
    setUsageAnalytics((previous) => ({ ...previous, status: 'loading', error: '' }));
    Promise.all([
      getObservabilityUsage({ projectId: activeProject?.id || '', groupBy: 'model' }),
      getObservabilityRecommendations({ projectId: activeProject?.id || '' })
    ]).then(([report, advice]) => {
      if (disposed) return;
      setUsageAnalytics({
        status: 'done',
        report,
        recommendations: Array.isArray(advice?.recommendations) ? advice.recommendations : [],
        error: ''
      });
    }).catch((err) => {
      if (disposed) return;
      setUsageAnalytics((previous) => ({ ...previous, status: 'error', error: err.message }));
    });
    return () => { disposed = true; };
  }, [activeProject?.id, sideView]);

  useEffect(() => {
    const projectId = activeProject?.id || '';
    const chapter = selectedChapterRevisionView.chapter;
    if (!showChapterRevisionWorkspace || !projectId || !chapter) {
      setChapterContent(createChapterContentState());
      return undefined;
    }
    let cancelled = false;
    const chapterKey = String(chapter);
    setChapterContent((previous) => {
      if (previous.projectId === projectId && previous.chapter === chapterKey && previous.status === 'done') {
        return previous;
      }
      return {
        projectId,
        chapter: chapterKey,
        status: 'loading',
        content: '',
        wordCount: 0,
        source: '',
        error: ''
      };
    });
    getChapter(projectId, chapter)
      .then((data) => {
        if (cancelled) {
          return;
        }
        const body = data?.chapter || {};
        setChapterContent({
          projectId,
          chapter: String(body.chapter || chapter),
          status: 'done',
          content: String(body.content || ''),
          wordCount: Number(body.word_count || body.wordCount || 0),
          source: String(body.source || ''),
          error: ''
        });
      })
      .catch((err) => {
        if (cancelled) {
          return;
        }
        setChapterContent({
          projectId,
          chapter: chapterKey,
          status: 'error',
          content: '',
          wordCount: 0,
          source: '',
          error: err.message
        });
      });
    return () => {
      cancelled = true;
    };
  }, [activeProject?.id, selectedChapterRevisionView.chapter, showChapterRevisionWorkspace]);

  const refreshProjects = useCallback(async () => {
    const data = await listProjects();
    setProjects(data.projects || []);
  }, []);

  const refreshTrashProjects = useCallback(async () => {
    const data = await listTrashProjects();
    setTrashProjects(data.projects || []);
  }, []);

  const loadInitialLibraries = useCallback(async () => {
    setSimulation((previous) => ({
      ...previous,
      libraryStatus: 'running',
      libraryError: '',
      libraryMessage: ''
    }));
    setAdaptation((previous) => ({
      ...previous,
      libraryStatus: 'running',
      libraryError: '',
      libraryMessage: ''
    }));

    const [simulationResult, novelResult] = await Promise.allSettled([
      listSimulationLibrary(''),
      listNovelLibrary('')
    ]);

    setSimulation((previous) => (
      simulationResult.status === 'fulfilled'
        ? {
          ...previous,
          libraryStatus: 'done',
          libraryItems: libraryItemsFromResponse(simulationResult.value),
          libraryMessage: libraryMessageFromResponse(simulationResult.value),
          libraryError: ''
        }
        : {
          ...previous,
          libraryStatus: 'error',
          libraryError: simulationResult.reason?.message || String(simulationResult.reason || '')
        }
    ));
    setAdaptation((previous) => (
      novelResult.status === 'fulfilled'
        ? {
          ...previous,
          libraryStatus: 'done',
          libraryItems: libraryItemsFromResponse(novelResult.value),
          libraryMessage: libraryMessageFromResponse(novelResult.value),
          libraryError: ''
        }
        : {
          ...previous,
          libraryStatus: 'error',
          libraryError: novelResult.reason?.message || String(novelResult.reason || '')
        }
    ));
  }, []);

  const loadProjectStyles = useCallback(async () => {
    setProjectSettings((previous) => ({
      ...previous,
      loadStatus: 'running',
      error: ''
    }));
    try {
      const data = await listStyles();
      const catalog = normalizeProjectStyleCatalog(data);
      setProjectSettings((previous) => ({
        ...previous,
        styles: catalog.styles,
        defaultStyle: catalog.defaultStyle,
        loadStatus: 'done',
        error: ''
      }));
    } catch (err) {
      setProjectSettings((previous) => ({
        ...previous,
        loadStatus: 'error',
        error: err.message
      }));
    }
  }, []);

  const loadResumeSchedule = useCallback(async () => {
    setResumeScheduleState((previous) => ({ ...previous, status: 'loading', error: '' }));
    try {
      const data = await getResumeSchedule();
      const schedule = normalizeResumeScheduleResponse(data);
      setResumeScheduleState((previous) => ({
        ...previous,
        ...schedule,
        status: 'done',
        message: '',
        error: ''
      }));
    } catch (err) {
      setResumeScheduleState((previous) => ({
        ...previous,
        status: 'error',
        error: err.message
      }));
    }
  }, []);

  const refreshGlobalModels = useCallback(async () => {
    const data = await getGlobalModels();
    setModelConfig(data.models || null);
    return data.models || null;
  }, []);

  const loadShell = useCallback(async () => {
    setError('');
    try {
      const [setupData, runtimeData, projectsData, modelData] = await Promise.all([getSetupStatus(), getRuntime(), listProjects(), getGlobalModels()]);
      setSetup((previous) => ({
        ...previous,
        loading: false,
        required: Boolean(setupData?.setup_required),
        providers: Array.isArray(setupData?.providers) ? setupData.providers : [],
        error: ''
      }));
      setRuntime(runtimeData);
      setProjects(projectsData.projects || []);
      if (!activeProjectIdRef.current) {
        setModelConfig(modelData.models || null);
      }
    } catch (err) {
      setError(err.message);
      setSetup((previous) => ({ ...previous, loading: false, error: err.message }));
    }
  }, []);

  useEffect(() => {
    loadShell();
  }, [loadShell]);

  useEffect(() => {
    loadInitialLibraries();
  }, [loadInitialLibraries]);

  useEffect(() => {
    loadProjectStyles();
  }, [loadProjectStyles]);

  useEffect(() => {
    loadResumeSchedule();
  }, [loadResumeSchedule]);

  useEffect(() => {
    if (!projectMenu) {
      return undefined;
    }
    const closeMenu = () => setProjectMenu(null);
    const closeOnEscape = (event) => {
      if (event.key === 'Escape') {
        closeMenu();
      }
    };
    window.addEventListener('click', closeMenu);
    window.addEventListener('keydown', closeOnEscape);
    return () => {
      window.removeEventListener('click', closeMenu);
      window.removeEventListener('keydown', closeOnEscape);
    };
  }, [projectMenu]);

  const openProject = useCallback(async (project) => {
    const projectId = project?.id;
    if (!projectId) {
      return;
    }
    const forced = forceFoundationProjectOpenRef.current === projectId;
    if (forced) forceFoundationProjectOpenRef.current = '';
    if (!forced && centerView === 'foundation' && foundationDraftDirty && projectId !== activeProject?.id) {
      setPendingFoundationProject({ project, trigger: document.activeElement });
      return;
    }
    const requestSeq = projectOpenSeqRef.current + 1;
    projectOpenSeqRef.current = requestSeq;
    projectOpenAbortRef.current?.abort();
    const controller = new AbortController();
    projectOpenAbortRef.current = controller;
    let timedOut = false;
    const timeoutID = window.setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, PROJECT_OPEN_TIMEOUT_MS);
    activeProjectIdRef.current = projectId;
    setError('');
    resetProjectScopedState();
    setActiveProject(project);
    setProjectOpen({ status: 'loading', project, error: '' });
    setProjectDrawerOpen(false);
    try {
      const snapshotData = await getSummarySnapshot(projectId, { signal: controller.signal });
      const prepared = prepareProjectOpenSnapshot(requestSeq, projectOpenSeqRef.current, snapshotData);
      if (!prepared) {
        return;
      }
      lastSeqRef.current = prepared.workbench.lastSeq;
      setWorkbench(prepared.workbench);
      setActiveProject(prepared.project);
      setProjectOpen({ status: 'idle', project: null, error: '' });
      setCoCreate((previous) => coCreateStateFromResponse(snapshotData, previous));
      setSimulation((previous) => restoreSimulationProjectState(previous, snapshotData.simulation));
      setAdaptation((previous) => restoreAdaptationProjectState(previous, snapshotData.adaptation, snapshotData.snapshot));
      const nextAction = projectNextAction(snapshotData.snapshot);
      if (nextAction?.code === 'confirm_character_candidate') {
        setCenterView('writing');
        setSideView('cocreate');
        setFoundationReviewTab(nextAction.tab || 'characters');
        setMobileToolView('detail');
        setToolDrawerOpen(true);
      }

      // The summary snapshot makes ordinary project navigation fast, but a
      // pending planning review must render the complete volume/chapter plan
      // in the center workspace. Hydrate that heavyweight data only for the
      // review state that actually needs it; compact SSE updates will retain
      // these fields through mergeSnapshotUpdate.
      let openedSnapshot = snapshotData.snapshot;
      if (shouldHydrateProjectOpenSnapshot(openedSnapshot)) {
        planningReviewHydrationProjectRef.current = projectId;
        try {
          const detailedSnapshotData = await getSnapshot(projectId, { signal: controller.signal });
          if (projectOpenSeqRef.current !== requestSeq || !isCurrentProject(projectId)) {
            return;
          }
          openedSnapshot = detailedSnapshotData.snapshot || openedSnapshot;
          setWorkbench((previous) => ({ ...previous, snapshot: openedSnapshot }));
        } finally {
          if (planningReviewHydrationProjectRef.current === projectId) {
            planningReviewHydrationProjectRef.current = '';
          }
        }
      }

      // Project-scoped reads update last_accessed_at. Keep them sequential on
      // Windows, but hydrate them only after the latest workspace is visible.
      // The continuation endpoint includes the complete continuation plan and
      // can be several megabytes; fetch it only when the user opens that tool.
      const modelData = await getProjectModels(projectId);
      const backendData = await getBackendStatus(projectId);
      const projectScheduleResult = await getProjectResumeSchedule(projectId)
        .then((data) => ({ data, error: '' }))
        .catch((err) => ({ data: null, error: err.message }));
      if (projectOpenSeqRef.current !== requestSeq || !isCurrentProject(projectId)) {
        return;
      }
      setContinuation((previous) => restoreContinuationState(previous, null, openedSnapshot));
      setModelConfig(modelData.models || null);
      setBackendStatus(backendData.backend || null);
      setProjectSettings((previous) => ({
        ...previous,
        scheduledResumeEnabled: projectScheduledResumeEnabled(projectScheduleResult.data),
        scheduledResumeLoadStatus: projectScheduleResult.error ? 'error' : 'done',
        scheduledResumeSaveStatus: 'idle',
        scheduledResumeError: projectScheduleResult.error
      }));
    } catch (err) {
      if (projectOpenSeqRef.current === requestSeq) {
        activeProjectIdRef.current = projectId;
        const message = timedOut
          ? `打开“${project.name || projectId}”超时，请重试`
          : (err?.name === 'AbortError' ? '项目打开请求已取消' : err.message);
        setError(message);
        setProjectOpen({ status: 'error', project, error: message });
      }
    } finally {
      window.clearTimeout(timeoutID);
      if (projectOpenAbortRef.current === controller) {
        projectOpenAbortRef.current = null;
      }
    }
  }, [activeProject?.id, centerView, foundationDraftDirty, isCurrentProject, resetProjectScopedState]);

  useEffect(() => {
    if (!activeProject?.id) {
      setConnection('idle');
      return undefined;
    }

    if (projectOpen.status === 'loading' && projectOpen.project?.id === activeProject.id) {
      setConnection('connecting');
      return undefined;
    }

    const projectId = activeProject.id;
    let disposed = false;
    let source = null;
    let retryTimer = null;
    let reconnectAttempt = 0;
    let connectionGeneration = 0;
    let lastActivityAt = Date.now();

    const clearRetryTimer = () => {
      if (!retryTimer) {
        return;
      }
      window.clearTimeout(retryTimer);
      retryTimer = null;
    };

    const closeSource = () => {
      if (!source) {
        return;
      }
      source.close();
      source = null;
    };

    const markActivity = () => {
      lastActivityAt = Date.now();
    };

    function applyEvent(event) {
      if (!event || activeProjectIdRef.current !== projectId ||
          (event.project_id && event.project_id !== projectId)) {
        return;
      }
      const sequence = Number(event.seq || 0);
      if (!sequence || sequence <= lastSeqRef.current) {
        return;
      }
      // Sequence values are a cursor, not a contiguous packet counter. A
      // restarted session, retained runtime queue, or coalesced host-event
      // update can legitimately leave holes. The SSE subscription itself
      // starts with a fresh snapshot, so accepting the newer event avoids a
      // gap -> summary fetch -> reconnect feedback loop.
      setWorkbench((previous) => {
        const next = reduceWebEvent(previous, event);
        lastSeqRef.current = next.lastSeq;
        return next;
      });
      if (event.type === 'cocreate_state') {
        setCoCreate((previous) => coCreateStateFromEvent(event, previous));
      }
      if (event.type === 'host_event') {
        setSimulation((previous) => applyHostEventToSimulationState(previous, event));
        setAdaptation((previous) => applyHostEventToAdaptationState(previous, event));
        const hostEvent = event.event || {};
        if (String(hostEvent.category || '').toUpperCase() === 'LIBRARY' &&
            ['novel_auto_save', 'novel_sync'].includes(String(hostEvent.kind || '').toLowerCase())) {
          listNovelLibrary('').then((data) => {
            if (!disposed && isCurrentProject(projectId)) {
              setAdaptation((previous) => ({
                ...previous,
                libraryStatus: 'done',
                libraryItems: libraryItemsFromResponse(data),
                libraryMessage: hostEvent.summary || previous.libraryMessage,
                libraryError: ''
              }));
            }
          }).catch((err) => {
            if (!disposed && isCurrentProject(projectId)) {
              setAdaptation((previous) => ({ ...previous, libraryStatus: 'error', libraryError: err.message }));
            }
          });
        }
        if (String(hostEvent.category || '').toUpperCase() === 'LIBRARY' &&
            String(hostEvent.kind || '').toLowerCase() === 'simulation_auto_sync') {
          listSimulationLibrary('').then((data) => {
            if (!disposed && isCurrentProject(projectId)) {
              setSimulation((previous) => ({
                ...previous,
                libraryStatus: 'done',
                libraryItems: libraryItemsFromResponse(data),
                libraryMessage: hostEvent.summary || previous.libraryMessage,
                libraryError: ''
              }));
            }
          }).catch((err) => {
            if (!disposed && isCurrentProject(projectId)) {
              setSimulation((previous) => ({ ...previous, libraryStatus: 'error', libraryError: err.message }));
            }
          });
        }
      }
      setConnection('live');
      reconnectAttempt = 0;
    }

    const connect = async (reconcile = false) => {
      if (disposed) {
        return;
      }
      const generation = ++connectionGeneration;
      if (reconcile) {
        // Reconcile only after a broken connection or an offline transition.
        // The project-open request has already supplied this same snapshot, so
        // doing another read for the first EventSource connection doubles the
        // largest payload on every project switch.
        try {
          const snapshotData = await getSummarySnapshot(projectId);
          const latest = Number(snapshotData?.latest_event_seq || 0);
          if (!disposed && latest > 0 && latest < lastSeqRef.current) {
            const restored = restoreProjectWorkbenchSnapshot(
              createWorkbenchState(),
              snapshotData.snapshot,
              snapshotData.events || [],
              latest
            );
            lastSeqRef.current = restored.lastSeq;
            setWorkbench(restored);
          }
        } catch {
          // Continue with SSE even when the reconciliation snapshot is unavailable.
        }
      }
      if (disposed || generation !== connectionGeneration) {
        return;
      }
      const after = lastSeqRef.current;
      const url = '/api/projects/' + encodeURIComponent(projectId) + '/events?after=' + after;
      const currentSource = new EventSource(url);
      source = currentSource;
      markActivity();
      setConnection(after > 0 ? 'reconnecting' : 'connecting');
      currentSource.onopen = () => {
        if (disposed || source !== currentSource) {
          return;
        }
        markActivity();
        reconnectAttempt = 0;
        setConnection('live');
      };
      const apply = (message) => {
        if (disposed || source !== currentSource) {
          return;
        }
        markActivity();
        const parsed = parseSSEMessage(message);
        if (!parsed.event) {
          setConnection('degraded');
          return;
        }
        applyEvent(parsed.event);
      };
      for (const type of eventTypes) {
        currentSource.addEventListener(type, apply);
      }
      currentSource.addEventListener('heartbeat', () => {
        if (disposed || source !== currentSource) {
          return;
        }
        markActivity();
        reconnectAttempt = 0;
        setConnection('live');
      });
      currentSource.onerror = () => {
        if (disposed || source !== currentSource) {
          return;
        }
        closeSource();
        setConnection('reconnecting');
        const delay = nextSSEReconnectDelay(reconnectAttempt);
        reconnectAttempt += 1;
        clearRetryTimer();
        retryTimer = window.setTimeout(() => {
          retryTimer = null;
          connect(true);
        }, delay);
      };
    };

    const handleOffline = () => {
      connectionGeneration += 1;
      clearRetryTimer();
      closeSource();
      setConnection('offline');
    };
    const handleOnline = () => {
      reconnectAttempt = 0;
      clearRetryTimer();
      closeSource();
      connect(true);
    };

    connect();
    const watchdogTimer = window.setInterval(() => {
      if (disposed || !source || source.readyState !== EventSource.OPEN ||
          !isSSEConnectionStale(lastActivityAt)) {
        return;
      }
      reconnectAttempt = 0;
      closeSource();
      setConnection('reconnecting');
      connect(true);
    }, SSE_WATCHDOG_INTERVAL_MS);
    window.addEventListener('offline', handleOffline);
    window.addEventListener('online', handleOnline);
    return () => {
      disposed = true;
      connectionGeneration += 1;
      window.clearInterval(watchdogTimer);
      clearRetryTimer();
      closeSource();
      window.removeEventListener('offline', handleOffline);
      window.removeEventListener('online', handleOnline);
    };
  }, [activeProject?.id, projectOpen.project?.id, projectOpen.status, refreshCurrentProjectSnapshot]);

  const createAndOpen = async (event) => {
    event.preventDefault();
    setBusy(true);
    setError('');
    try {
      const project = await createProject(newProjectName);
      setNewProjectName('');
      await refreshProjects();
      await openProject(project);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const openProjectContextMenu = (event, project) => {
    event.preventDefault();
    event.stopPropagation();
    const menuWidth = 220;
    const menuHeight = 152;
    setProjectMenu({
      project,
      x: Math.min(event.clientX, Math.max(8, window.innerWidth - menuWidth - 8)),
      y: Math.min(event.clientY, Math.max(8, window.innerHeight - menuHeight - 8))
    });
  };

  const openProjectMoreMenu = (event, project) => {
    event.preventDefault();
    event.stopPropagation();
    const rect = event.currentTarget.getBoundingClientRect();
    const menuWidth = 220;
    const menuHeight = 152;
    setProjectMenu({
      project,
      x: Math.min(rect.left, Math.max(8, window.innerWidth - menuWidth - 8)),
      y: Math.min(rect.bottom + 6, Math.max(8, window.innerHeight - menuHeight - 8))
    });
  };

  const selectAdaptationAuditRun = async (runId) => {
    const projectId = activeProject?.id;
    if (!projectId) return;
    if (!runId) {
      await loadAdaptationAuditReport(projectId);
      return;
    }
    try {
      const data = await getAdaptationAuditRun(projectId, runId);
      if (!isCurrentProject(projectId)) return;
      setAdaptationAudit((previous) => ({
        ...previous,
        selectedRunId: runId,
        report: normalizedAdaptationAuditReport(data?.run?.report),
        comparison: null,
        application: null,
        acknowledged: false,
        error: ''
      }));
    } catch (err) {
      setAdaptationAudit((previous) => ({ ...previous, error: err.message }));
    }
  };

  const compareAdaptationAudits = async (baseRunId, candidateRunId) => {
    const projectId = activeProject?.id;
    if (!projectId) return;
    try {
      const data = await compareAdaptationAuditRuns(projectId, baseRunId, candidateRunId);
      if (!isCurrentProject(projectId)) return;
      setAdaptationAudit((previous) => ({ ...previous, comparison: data?.comparison || null, error: '' }));
    } catch (err) {
      setAdaptationAudit((previous) => ({ ...previous, error: err.message }));
    }
  };

  const estimateSemanticAudit = async (payload) => {
    const projectId = activeProject?.id;
    if (!projectId) return;
    try {
      const data = await estimateSemanticAdaptationAudit(projectId, payload);
      if (!isCurrentProject(projectId)) return;
      setAdaptationAudit((previous) => ({ ...previous, semantic: { ...previous.semantic, estimate: data?.estimate || data, error: '' } }));
    } catch (err) {
      setAdaptationAudit((previous) => ({ ...previous, semantic: { ...previous.semantic, error: err.message } }));
    }
  };

  const startSemanticAudit = async (payload) => {
    const projectId = activeProject?.id;
    if (!projectId) return;
    try {
      const data = await startSemanticAdaptationAudit(projectId, payload);
      if (!isCurrentProject(projectId)) return;
      setAdaptationAudit((previous) => ({ ...previous, semantic: { ...previous.semantic, run: data?.run || data, error: '' } }));
    } catch (err) {
      setAdaptationAudit((previous) => ({ ...previous, semantic: { ...previous.semantic, error: err.message } }));
    }
  };

  const cancelSemanticAudit = async (runId) => {
    const projectId = activeProject?.id;
    if (!projectId || !runId) return;
    try {
      const data = await cancelSemanticAdaptationAudit(projectId, runId);
      if (!isCurrentProject(projectId)) return;
      setAdaptationAudit((previous) => ({ ...previous, semantic: { ...previous.semantic, run: data?.run || previous.semantic.run, error: '' } }));
    } catch (err) {
      setAdaptationAudit((previous) => ({ ...previous, semantic: { ...previous.semantic, error: err.message } }));
    }
  };

  const retrySemanticAudit = async (runId) => {
    const projectId = activeProject?.id;
    if (!projectId || !runId) return;
    try {
      const data = await retrySemanticAdaptationAudit(projectId, runId);
      if (!isCurrentProject(projectId)) return;
      setAdaptationAudit((previous) => ({ ...previous, semantic: { ...previous.semantic, run: data?.run || data, error: '' } }));
    } catch (err) {
      setAdaptationAudit((previous) => ({ ...previous, semantic: { ...previous.semantic, error: err.message } }));
    }
  };

  const beginProjectRename = (project) => {
    setProjectMenu(null);
    setRenameDialog({ project, name: project.name || project.id });
  };

  const submitProjectRename = async (event) => {
    event.preventDefault();
    const project = renameDialog?.project;
    const name = String(renameDialog?.name || '').trim();
    if (!project || !name) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const updated = await renameProject(project.id, name);
      setProjects((previous) => previous.map((item) => (item.id === updated.id ? updated : item)));
      setActiveProject((previous) => (previous?.id === updated.id ? updated : previous));
      setRenameDialog(null);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const beginProjectClone = (project) => {
    const projectName = project.name || project.id;
    setProjectMenu(null);
    setCloneDialog({ project, name: `${projectName} - 副本`, openAfterClone: true });
  };

  const submitProjectClone = async (event) => {
    event.preventDefault();
    const sourceProject = cloneDialog?.project;
    const name = String(cloneDialog?.name || '').trim();
    if (!sourceProject || !name) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const response = await cloneProject(sourceProject.id, name);
      const clonedProject = response?.project || response;
      if (!clonedProject?.id) {
        throw new Error('项目克隆成功，但响应中缺少新项目信息');
      }
      await refreshProjects();
      const shouldOpen = cloneDialog.openAfterClone;
      setCloneDialog(null);
      if (shouldOpen) {
        await openProject(clonedProject);
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const beginProjectDelete = (project) => {
    setProjectMenu(null);
    setDeleteDialog({ project });
  };

  const confirmProjectDelete = async () => {
    const project = deleteDialog?.project;
    if (!project) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      await trashProject(project.id);
      setProjects((previous) => previous.filter((item) => item.id !== project.id));
      if (trashOpen) {
        await refreshTrashProjects();
      }
      if (activeProject?.id === project.id) {
        resetProjectScopedState(true);
        await refreshGlobalModels();
      }
      setDeleteDialog(null);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const toggleTrash = async () => {
    const nextOpen = !trashOpen;
    setTrashOpen(nextOpen);
    if (!nextOpen) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      await refreshTrashProjects();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const restoreProjectFromTrash = async (project) => {
    if (!project?.id) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = await restoreTrashProject(project.id);
      const restored = data.project || project;
      setTrashProjects((previous) => previous.filter((item) => item.id !== restored.id));
      await refreshProjects();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const emptyProjectTrash = async () => {
    if (!window.confirm('清空回收站后无法从这里恢复，确定清空吗？')) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      await emptyTrashProjects();
      setTrashProjects([]);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const runAction = async (action, { reportResumeNoOp = false } = {}) => {
    if (!activeProject?.id) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = await action(activeProject.id);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      if (reportResumeNoOp) {
        setError(resumeNoOpMessage(data));
        if (data.recovery?.required) {
          setCenterView('manuscript');
          setSideView('manuscript');
          setToolDrawerOpen(true);
        }
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const saveProjectStyle = async (event) => {
    event.preventDefault();
    const request = buildProjectStyleSaveRequest(activeProject, projectSettings);
    if (!request.ok) {
      setProjectSettings((previous) => ({
        ...previous,
        saveStatus: 'error',
        message: '',
        error: request.error
      }));
      return;
    }
    setBusy(true);
    setProjectSettings((previous) => ({
      ...previous,
      saveStatus: 'running',
      message: '',
      error: ''
    }));
    try {
      const data = await setProjectStyle(request.projectId, request.style);
      if (!isCurrentProject(request.projectId)) {
        return;
      }
      setActiveProject(data.project || activeProject);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setProjectSettings((previous) => ({
        ...previous,
        selectedStyle: String(data.style || request.style || '').trim(),
        saveStatus: 'done',
        message: '文风已保存',
        error: ''
      }));
    } catch (err) {
      setProjectSettings((previous) => ({
        ...previous,
        saveStatus: 'error',
        message: '',
        error: err.message
      }));
    } finally {
      setBusy(false);
    }
  };

  const saveProjectSimulationMode = async (event) => {
    event.preventDefault();
    const request = buildProjectSimulationModeSaveRequest(activeProject, projectSettings);
    if (!request.ok) {
      setProjectSettings((previous) => ({
        ...previous,
        simulationModeSaveStatus: 'error',
        simulationModeMessage: '',
        simulationModeError: request.error
      }));
      return;
    }
    setBusy(true);
    setProjectSettings((previous) => ({
      ...previous,
      simulationModeSaveStatus: 'running',
      simulationModeMessage: '',
      simulationModeError: ''
    }));
    try {
      const data = await setProjectSimulationMode(request.projectId, request.mode);
      if (!isCurrentProject(request.projectId)) {
        return;
      }
      const nextMode = normalizeSimulationMode(data.simulation_mode || data.simulationMode || request.mode);
      setActiveProject(data.project || activeProject);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setProjectSettings((previous) => ({
        ...previous,
        selectedSimulationMode: nextMode,
        simulationModeSaveStatus: 'done',
        simulationModeMessage: '仿写模式已保存',
        simulationModeError: ''
      }));
    } catch (err) {
      setProjectSettings((previous) => ({
        ...previous,
        simulationModeSaveStatus: 'error',
        simulationModeMessage: '',
        simulationModeError: err.message
      }));
    } finally {
      setBusy(false);
    }
  };

  const saveResumeSchedule = async (event) => {
    event.preventDefault();
    const dailyTimes = normalizeDailyResumeTimes(resumeSchedule.dailyTimes);
    setResumeScheduleState((previous) => ({ ...previous, status: 'saving', message: '', error: '' }));
    try {
      const data = await setResumeSchedule(dailyTimes, resumeSchedule.timezone);
      const schedule = normalizeResumeScheduleResponse(data);
      setResumeScheduleState((previous) => ({
        ...previous,
        ...schedule,
        status: 'done',
        message: dailyTimes.length ? '定时启动时间已保存' : '定时启动已关闭',
        error: ''
      }));
    } catch (err) {
      setResumeScheduleState((previous) => ({ ...previous, status: 'error', message: '', error: err.message }));
    }
  };

  const changeProjectResumeSchedule = async (enabled) => {
    const projectId = activeProject?.id;
    if (!projectId) return;
    setProjectSettings((previous) => ({
      ...previous,
      scheduledResumeEnabled: enabled,
      scheduledResumeSaveStatus: 'running',
      scheduledResumeError: ''
    }));
    try {
      const data = await setProjectResumeSchedule(projectId, enabled);
      if (!isCurrentProject(projectId)) return;
      setProjectSettings((previous) => ({
        ...previous,
        scheduledResumeEnabled: projectScheduledResumeEnabled(data),
        scheduledResumeSaveStatus: 'done',
        scheduledResumeError: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) return;
      setProjectSettings((previous) => ({
        ...previous,
        scheduledResumeEnabled: !enabled,
        scheduledResumeSaveStatus: 'error',
        scheduledResumeError: err.message
      }));
    }
  };

  const submitContinue = async (event) => {
    event.preventDefault();
    if (projectRunning) {
      await pauseWriting();
      return;
    }
    const text = composerText.trim();
    if (!text) {
      return;
    }
    await runAction((projectId) => (quickStartAvailable ? startProject(projectId, text) : continueProject(projectId, text)));
    setComposerText('');
  };

  const pauseWriting = async () => {
    await runAction((projectId) => pauseProject(projectId));
  };

  const openRollbackDialog = async () => {
    if (!activeProject?.id) {
      return;
    }
    const projectId = activeProject.id;
    setBusy(true);
    setError('');
    try {
      const data = await previewProjectRollback(projectId);
      if (!isCurrentProject(projectId)) {
        return;
      }
      const preview = data?.rollback || {};
      if (!(preview.can_rollback === true || preview.canRollback === true)) {
        setError(textValue(preview, 'reason', 'Reason') || '当前项目没有可回退的阶段');
        return;
      }
      setRollbackDialog({ preview, error: '' });
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const confirmRollback = async () => {
    if (!activeProject?.id || !rollbackDialog?.preview) {
      return;
    }
    const projectId = activeProject.id;
    const preview = rollbackDialog.preview;
    setBusy(true);
    setRollbackDialog((previous) => previous ? { ...previous, error: '' } : previous);
    try {
      const data = await rollbackProject(projectId, {
        confirm: true,
        preview_hash: textValue(preview, 'preview_hash', 'previewHash', 'PreviewHash')
      });
      if (!isCurrentProject(projectId)) {
        return;
      }
      const nextSnapshot = data.snapshot || snapshot;
      setActiveProject(data.project || activeProject);
      setWorkbench((previous) => ({ ...previous, snapshot: nextSnapshot || previous.snapshot }));
      setCoCreate((previous) => data.cocreate
        ? coCreateStateFromResponse(data, previous)
        : createCoCreateState());
      setPlanningRevision(createCoCreatePlanningRevisionState());
      if (data.cocreate || getCoCreatePlanningReview(nextSnapshot).active) {
        setSideView('cocreate');
      }
      setRollbackDialog(null);
    } catch (err) {
      setRollbackDialog((previous) => previous ? { ...previous, error: err.message } : previous);
    } finally {
      setBusy(false);
    }
  };

  const submitSteer = async (event) => {
    event.preventDefault();
    const text = steerText.trim();
    if (!text) {
      return;
    }
    await runAction((projectId) => steerProject(projectId, text));
    setSteerText('');
  };

  const submitOutlineRevision = async () => {
    const projectId = activeProject?.id;
    if (!projectId) {
      return;
    }
    const payload = buildOutlineRevisionPayload(outlineRevision, snapshot);
    if (!payload.ok) {
      setOutlineRevision((previous) => ({
        ...previous,
        active: true,
        status: 'error',
        message: '',
        error: payload.error
      }));
      return;
    }
    setBusy(true);
    setOutlineRevision((previous) => ({
      ...previous,
      active: true,
      status: 'running',
      message: '',
      error: ''
    }));
    try {
      const data = await reviseChapterOutline(projectId, payload.body);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({ ...previous, snapshot: data?.snapshot || previous.snapshot }));
      setOutlineRevision((previous) => ({
        ...previous,
        chapter: String(payload.body.chapter),
        active: true,
        status: 'done',
        message: outlineRevisionSuccessMessage(data?.revision, payload.body.chapter),
        error: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      await refreshCurrentProjectSnapshot(projectId);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setOutlineRevision((previous) => ({
        ...previous,
        active: true,
        status: 'error',
        message: '',
        error: err.message
      }));
    } finally {
      if (isCurrentProject(projectId)) {
        setBusy(false);
      }
    }
  };

  const refreshOutlineRevision = async () => {
    const projectId = activeProject?.id;
    if (!projectId) {
      return;
    }
    setBusy(true);
    setOutlineRevision((previous) => ({
      ...previous,
      active: true,
      status: 'running',
      message: '',
      error: ''
    }));
    try {
      const data = await getSnapshot(projectId);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({ ...previous, snapshot: data?.snapshot || previous.snapshot }));
      setOutlineRevision((previous) => ({
        ...previous,
        active: true,
        status: 'done',
        message: '章节细纲已刷新',
        error: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setOutlineRevision((previous) => ({
        ...previous,
        active: true,
        status: 'error',
        message: '',
        error: err.message
      }));
    } finally {
      if (isCurrentProject(projectId)) {
        setBusy(false);
      }
    }
  };

  const reviseCompletedChapter = async () => {
    if (!activeProject?.id) {
      return;
    }
    const payload = buildChapterRevisionPayload(chapterRevision, snapshot);
    if (!payload.ok) {
      setChapterRevision((previous) => ({
        ...previous,
        status: 'error',
        message: '',
        error: payload.error
      }));
      return;
    }
    setBusy(true);
    setChapterRevision((previous) => ({
      ...previous,
      status: 'running',
      message: '',
      error: ''
    }));
    try {
      const data = await reviseChapter(activeProject.id, payload.body);
      setWorkbench((previous) => ({ ...previous, snapshot: data?.snapshot || previous.snapshot }));
      const revisionLabel = data?.revision?.label || `第 ${payload.body.chapter} 章已提交${chapterRevisionModeLabel(payload.body.mode)}`;
      setChapterRevision((previous) => ({
        ...previous,
        status: 'done',
        message: revisionLabel,
        error: ''
      }));
    } catch (err) {
      try {
        const snapshotData = await getSnapshot(activeProject.id);
        setWorkbench((previous) => ({ ...previous, snapshot: snapshotData.snapshot || previous.snapshot }));
      } catch {
        // Keep the original revision error visible when snapshot refresh also fails.
      }
      setChapterRevision((previous) => ({
        ...previous,
        status: 'error',
        message: '',
        error: err.message
      }));
    } finally {
      setBusy(false);
    }
  };

  const uploadContinuationNovel = async (event) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!activeProject?.id || !file) {
      return;
    }
    setBusy(true);
    setContinuation((previous) => ({
      ...previous,
      sourceFile: null,
      status: 'running',
      events: [],
      message: '',
      error: ''
    }));
    try {
      const data = await uploadContinuationSource(activeProject.id, file);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setContinuation((previous) => ({
        ...restoreContinuationState(previous, data.continuation || data, data.snapshot),
        sourceFile: data.source_file || data.sourceFile || { name: file.name, size: file.size },
        status: 'done',
        events: data.events || [],
        message: continuationUploadSuccessMessage(data.continuation || data, file.name),
        error: ''
      }));
    } catch (err) {
      setContinuation((previous) => ({
        ...previous,
        status: 'error',
        events: err.data?.events || previous.events,
        error: err.message
      }));
    } finally {
      setBusy(false);
    }
  };

  const runContinuationAction = async (action, payload = {}, successMessage = '') => {
    if (!activeProject?.id || busy) {
      return;
    }
    const projectId = activeProject.id;
    setBusy(true);
    setContinuation((previous) => ({
      ...previous,
      actionStatus: 'running',
      actionMessage: '',
      error: ''
    }));
    try {
      const requestPayload = withExpectedRevision(continuationSnapshot, payload);
      const data = await action(projectId, requestPayload);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setContinuation((previous) => ({
        ...restoreContinuationState(previous, data.continuation || data, data.snapshot),
        instruction: '',
        actionStatus: 'done',
        actionMessage: data.label || successMessage,
        error: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      await refreshCurrentProjectSnapshot(projectId);
      setContinuation((previous) => ({
        ...previous,
        actionStatus: 'error',
        actionMessage: '',
        error: err.message
      }));
    } finally {
      if (isCurrentProject(projectId)) {
        setBusy(false);
      }
    }
  };

  const beginContinuationDraft = () => {
    runWithWindowScrollPreserved(() => {
      return beginCoCreateFlow('continuation');
    });
  };

  const generateContinuationProposalRun = () => runContinuationAction(
    generateContinuationProposal,
    {},
    '续写提案正在生成'
  );

  const reviseContinuationReview = () => {
    const instruction = continuation.instruction.trim();
    if (!instruction) {
      setContinuation((previous) => ({ ...previous, error: '请填写修改要求' }));
      return;
    }
    const reviewKind = continuationReviewKind(continuationSnapshot);
    if (reviewKind === 'proposal') {
      runContinuationAction(reviseContinuationProposal, { instruction }, '提案已重新生成');
      return;
    }
    if (reviewKind === 'volumes') {
      runContinuationAction(reviseContinuationVolumes, {
        instruction,
        ...(continuation.scope === 'volume' ? { volume_index: Number(continuation.volumeIndex) || 1 } : {})
      }, '分卷规划已重新生成');
      return;
    }
    if (reviewKind === 'outlines') {
      const scopePayload = buildContinuationOutlineScopePayload(continuation);
      if (scopePayload.error) {
        setContinuation((previous) => ({ ...previous, error: scopePayload.error }));
        return;
      }
      runContinuationAction(reviseContinuationOutlines, {
        instruction,
        ...scopePayload.body
      }, '章节细纲已重新生成');
    }
  };

  const approveContinuationReview = () => {
    const reviewKind = continuationReviewKind(continuationSnapshot);
    if (reviewKind === 'proposal') {
      runContinuationAction(approveContinuationProposal, {}, '提案审核通过');
    } else if (reviewKind === 'volumes') {
      runContinuationAction(approveContinuationVolumes, {}, '分卷规划审核通过');
    } else if (reviewKind === 'outlines') {
      runContinuationAction(approveContinuationOutlines, {}, '章节细纲审核通过，可以开始续写');
    }
  };

  const startContinuationRun = () => runContinuationAction(startContinuation, {}, '续写已启动');

  const runExport = async () => {
    if (!activeProject?.id) {
      return;
    }
    let from;
    let to;
    try {
      from = optionalNonNegativeInt(exportJob.from, 'from');
      to = optionalNonNegativeInt(exportJob.to, 'to');
    } catch (err) {
      setExportJob((previous) => ({ ...previous, error: err.message }));
      return;
    }
    const suggestedName = buildExportSuggestedName(exportJob, activeProject);
    const target = { kind: 'download', name: suggestedName };
    setExportJob((previous) => ({ ...previous, status: 'running', result: null, message: '', error: '' }));
    try {
      const data = await exportProjectDownload(activeProject.id, {
        path: suggestedName,
        format: exportJob.format,
        from,
        to,
        purpose: exportJob.purpose || 'preview',
        overwrite: true
      });
      const savedName = await saveExportBlob(data.blob, target, data.export?.name || suggestedName);
      const result = {
        ...(data.export || {}),
        name: data.export?.name || suggestedName,
        path: target?.kind === 'picker' ? `已保存到所选位置：${savedName}` : `已下载：${savedName}`
      };
      setExportJob((previous) => ({
        ...previous,
        status: 'done',
        result,
        message: target?.kind === 'picker' ? `已导出到所选位置：${savedName}` : `浏览器不支持选择位置，已下载 ${savedName}`,
        error: ''
      }));
    } catch (err) {
      const report = err.data?.report || err.data?.audit || null;
      const canForce = exportJob.purpose === 'publish' && err.data?.force_allowed && report?.digest;
      if (canForce && window.confirm(`正式导出审计状态为 ${report.status || '未通过'}。\n\n确认仍按报告 ${report.digest.slice(0, 12)} 导出，并保留未解决审计状态？`)) {
        try {
          const data = await exportProjectDownload(activeProject.id, {
            path: suggestedName,
            format: exportJob.format,
            from,
            to,
            purpose: 'publish',
            overwrite: true,
            force_export: true,
            acknowledged_report_digest: report.digest,
            override_reason: 'user_confirmed_publish_with_unresolved_audit'
          });
          const savedName = await saveExportBlob(data.blob, target, data.export?.name || suggestedName);
          setExportJob((previous) => ({ ...previous, status: 'done', audit: report, result: { ...(data.export || {}), path: `已导出：${savedName}` }, message: `已确认审计状态并导出 ${savedName}`, error: '' }));
          return;
        } catch (forceErr) {
          setExportJob((previous) => ({ ...previous, status: 'error', audit: report, error: forceErr.message }));
          return;
        }
      }
      setExportJob((previous) => ({ ...previous, status: 'error', audit: report, error: err.message }));
    }
  };

  const runDiagnostic = async () => {
    if (!activeProject?.id) {
      return;
    }
    setBusy(true);
    setDiagnostic((previous) => ({ ...previous, status: 'running', error: '' }));
    try {
      const data = await runProjectDiagnostic(activeProject.id);
      setDiagnostic({
        status: 'done',
        report: data.report || null,
        runtime: data.runtime || null,
        exportPath: data.export_path || '',
        error: ''
      });
    } catch (err) {
      setDiagnostic((previous) => ({ ...previous, status: 'error', error: err.message }));
    } finally {
      setBusy(false);
    }
  };

  const uploadSimulationSources = async (event) => {
    const files = Array.from(event.target.files || []);
    event.target.value = '';
    if (!activeProject?.id || files.length === 0) {
      return;
    }
    setBusy(true);
    setSimulation((previous) => ({ ...previous, uploadMessage: '', error: '' }));
    try {
      const data = await uploadSimulationFiles(activeProject.id, files);
      setSimulation((previous) => ({
        ...previous,
        files: simulationFilesFromResponse(data),
        uploadMessage: data.message || `已上传 ${files.length} 个文件`,
        error: ''
      }));
    } catch (err) {
      setSimulation((previous) => ({ ...previous, error: err.message }));
    } finally {
      setBusy(false);
    }
  };

  const runSimulationAnalysis = async (action = 'scan') => {
    const projectId = activeProject?.id;
    if (!projectId) {
      return;
    }
    setSimulation((previous) => ({
      ...previous,
      analysisStatus: 'running',
      analysisEvents: [],
      error: ''
    }));
    try {
      const data = await analyzeSimulation(projectId, action);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      if (data.accepted || data.running) {
        const responseEvents = data.simulation?.analysis_events || data.simulation?.analysisEvents || data.events || [];
        setSimulation((previous) => ({
          ...previous,
          analysisStatus: 'running',
          analysisEvents: Array.isArray(responseEvents) ? responseEvents : previous.analysisEvents,
          error: ''
        }));
        return;
      }
      setSimulation((previous) => ({
        ...previous,
        analysisStatus: 'done',
        analysisEvents: data.events || [],
        saveStatus: 'idle',
        saveError: '',
        error: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setSimulation((previous) => ({
        ...previous,
        analysisStatus: 'error',
        analysisEvents: err.data?.events || previous.analysisEvents,
        error: err.message
      }));
    }
  };

  const searchSimulationCorpus = async () => {
    const projectId = activeProject?.id;
    const fileName = simulation.sourceSearchQuery.trim();
    if (!projectId || !fileName) {
      return;
    }
    setSimulation((previous) => ({
      ...previous,
      sourceSearchStatus: 'running',
      sourceSearchResults: [],
      sourceSearchMessage: '',
      sourceSearchError: ''
    }));
    try {
      const data = await searchSimulationSources(projectId, fileName);
      if (!isCurrentProject(projectId)) {
        return;
      }
      const results = Array.isArray(data.results) ? data.results : [];
      setSimulation((previous) => ({
        ...previous,
        sourceSearchStatus: 'done',
        sourceSearchResults: results,
        sourceSearchMessage: results.length ? `找到 ${results.length} 个 TXT 结果` : '没有找到 TXT 文件',
        sourceSearchError: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setSimulation((previous) => ({
        ...previous,
        sourceSearchStatus: 'error',
        sourceSearchError: err.message
      }));
    }
  };

  const downloadSimulationCorpus = async (result) => {
    const projectId = activeProject?.id;
    if (!projectId || !result?.id) {
      return;
    }
    if (String(result.file_type || '').toLowerCase() !== 'txt') {
      setSimulation((previous) => ({ ...previous, sourceSearchError: '请选择 TXT 文件进行下载' }));
      return;
    }
    setSimulation((previous) => ({
      ...previous,
      sourceDownloadID: result.id,
      sourceSearchMessage: '',
      sourceSearchError: ''
    }));
    try {
      const data = await downloadSimulationSource(projectId, result.id);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setSimulation((previous) => ({
        ...previous,
        files: simulationFilesFromResponse(data),
        sourceDownloadID: '',
        sourceSearchMessage: data.message || `已加入语料：${result.name}`,
        sourceSearchError: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setSimulation((previous) => ({
        ...previous,
        sourceDownloadID: '',
        sourceSearchError: err.message
      }));
    }
  };

  const refreshSimulationLibrary = async () => {
    const query = simulation.libraryQuery;
    setSimulation((previous) => ({
      ...previous,
      libraryStatus: 'running',
      libraryError: '',
      libraryMessage: ''
    }));
    try {
      const data = await listSimulationLibrary(query);
      setSimulation((previous) => ({
        ...previous,
        libraryStatus: 'done',
        libraryItems: libraryItemsFromResponse(data),
        libraryMessage: libraryMessageFromResponse(data),
        libraryError: ''
      }));
    } catch (err) {
      setSimulation((previous) => ({
        ...previous,
        libraryStatus: 'error',
        libraryError: err.message
      }));
    }
  };

  const uploadSimulationProfilesToLibrary = async (event) => {
    const files = Array.from(event.target.files || []);
    event.target.value = '';
    if (files.length === 0) {
      return;
    }
    const query = simulation.libraryQuery;
    setSimulation((previous) => ({
      ...previous,
      libraryStatus: 'running',
      libraryError: '',
      libraryMessage: ''
    }));
    try {
      const uploadData = await uploadSimulationLibrary(files);
      const listData = await listSimulationLibrary(query);
      setSimulation((previous) => ({
        ...previous,
        libraryStatus: 'done',
        libraryItems: libraryItemsFromResponse(listData),
        libraryMessage: libraryMessageFromResponse(uploadData) || `已上传 ${files.length} 个画像 JSON`,
        libraryError: ''
      }));
    } catch (err) {
      setSimulation((previous) => ({
        ...previous,
        libraryStatus: 'error',
        libraryError: err.message
      }));
    }
  };

  const saveSimulationProfileToLibrary = async () => {
    const name = simulation.saveName.trim();
    if (!activeProject?.id || !name) {
      return;
    }
    const query = simulation.libraryQuery;
    setSimulation((previous) => ({ ...previous, saveStatus: 'running', saveError: '' }));
    try {
      const saveData = await saveSimulationToLibrary(activeProject.id, name);
      const listData = await listSimulationLibrary(query);
      setSimulation((previous) => ({
        ...previous,
        libraryStatus: 'done',
        libraryItems: libraryItemsFromResponse(listData),
        libraryMessage: libraryMessageFromResponse(saveData) || `已添加画像：${name}`,
        libraryError: '',
        saveName: '',
        saveStatus: 'done',
        saveError: ''
      }));
    } catch (err) {
      setSimulation((previous) => ({ ...previous, saveStatus: 'error', saveError: err.message }));
    }
  };

  const loadSimulationProfileFromLibrary = async (entry) => {
    const name = libraryEntryName(entry);
    if (!activeProject?.id || !name) {
      return;
    }
    setSimulation((previous) => ({
      ...previous,
      libraryStatus: 'running',
      importStatus: 'running',
      importEvents: [],
      importMessage: '',
      libraryError: '',
      libraryMessage: ''
    }));
    try {
      const data = await loadSimulationFromLibrary(activeProject.id, name);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      if (data.accepted || data.running) {
        const responseEvents = data.simulation?.import_events || data.simulation?.importEvents || data.events || [];
        setSimulation((previous) => ({
          ...previous,
          libraryStatus: 'done',
          libraryMessage: libraryMessageFromResponse(data) || `已开始从仿写画像库加载：${name}`,
          importStatus: 'running',
          importEvents: Array.isArray(responseEvents) ? responseEvents : previous.importEvents,
          importMessage: `正在加载画像：${name}`,
          error: '',
          libraryError: ''
        }));
        return;
      }
      setSimulation((previous) => ({
        ...previous,
        libraryStatus: 'done',
        libraryMessage: libraryMessageFromResponse(data) || `已从仿写画像库加载：${name}`,
        importStatus: 'done',
        importEvents: data.events || [],
        importMessage: libraryMessageFromResponse(data) || `已加载画像：${name}`,
        error: '',
        libraryError: ''
      }));
    } catch (err) {
      setSimulation((previous) => ({
        ...previous,
        libraryStatus: 'error',
        importStatus: 'error',
        libraryError: err.message
      }));
    }
  };

  const importSimulation = async (event) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!activeProject?.id || !file) {
      return;
    }
    setSimulation((previous) => ({
      ...previous,
      importStatus: 'running',
      importEvents: [],
      importMessage: '',
      error: ''
    }));
    try {
      const data = await importSimulationProfile(activeProject.id, file);
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      const libraryNote = data.library_saved
        ? '，已同步到仿写画像库'
        : data.warning
          ? `，画像库未同步：${data.warning}`
          : '';
      if (data.accepted || data.running) {
        const responseEvents = data.simulation?.import_events || data.simulation?.importEvents || data.events || [];
        setSimulation((previous) => ({
          ...previous,
          importStatus: 'running',
          importEvents: Array.isArray(responseEvents) ? responseEvents : previous.importEvents,
          importMessage: data.imported_file?.name ? `正在导入 ${data.imported_file.name}${libraryNote}` : `画像正在导入${libraryNote}`,
          error: ''
        }));
        return;
      }
      setSimulation((previous) => ({
        ...previous,
        importStatus: 'done',
        importEvents: data.events || [],
        importMessage: data.imported_file?.name ? `已导入 ${data.imported_file.name}${libraryNote}` : `画像已导入${libraryNote}`,
        error: ''
      }));
    } catch (err) {
      setSimulation((previous) => ({
        ...previous,
        importStatus: 'error',
        importEvents: err.data?.events || previous.importEvents,
        error: err.message
      }));
    }
  };

  const uploadAdaptation = async (event) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    const projectId = activeProject?.id;
    if (!projectId || !file) {
      return;
    }
    setWorkbench((previous) => ({
      ...previous,
      snapshot: clearAdaptationProposalSnapshot(previous.snapshot)
    }));
    setAdaptation((previous) => ({
      ...previous,
      sourceFile: null,
      uploadStatus: 'running',
      uploadMessage: '',
      analysisStatus: 'idle',
      analysisEvents: [],
      libraryLoadedName: '',
      proposalKey: '',
      startStatus: 'idle',
      startMessage: '',
      error: ''
    }));
    try {
      const data = await uploadAdaptationSource(projectId, file);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setAdaptation((previous) => ({
        ...previous,
        sourceFile: data.source_file || null,
        uploadStatus: 'done',
        uploadMessage: data.message || `已上传 ${file.name}`,
        error: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setAdaptation((previous) => ({ ...previous, uploadStatus: 'error', error: err.message }));
    }
  };

  const runAdaptationAnalysis = async ({ force = false } = {}) => {
    const projectId = activeProject?.id;
    const sourcePath = adaptation.sourceFile?.relative_path;
    if (!projectId || !sourcePath) {
      return;
    }
    setAdaptation((previous) => ({
      ...previous,
      analysisStatus: 'running',
      analysisEvents: [],
      startStatus: 'idle',
      startMessage: '',
      error: ''
    }));
    try {
      const data = await analyzeAdaptationSource(projectId, sourcePath, { force });
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({
        ...previous,
        snapshot: clearAdaptationProposalSnapshot(data.snapshot || previous.snapshot)
      }));
      if (data.accepted || data.running) {
        const responseEvents = data.adaptation?.analysis_events || data.adaptation?.analysisEvents || data.events || [];
        setAdaptation((previous) => ({
          ...previous,
          analysisStatus: 'running',
          analysisEvents: Array.isArray(responseEvents) ? responseEvents : previous.analysisEvents,
          error: ''
        }));
        return;
      }
      setAdaptation((previous) => ({
        ...previous,
        analysisStatus: 'done',
        analysisEvents: data.events || [],
        libraryItems: data.library_item
          ? [data.library_item, ...previous.libraryItems.filter((item) => libraryEntryName(item) !== libraryEntryName(data.library_item))]
          : previous.libraryItems,
        libraryMessage: data.library_saved && data.library_item
          ? `已自动保存小说：${libraryEntryName(data.library_item)}`
          : previous.libraryMessage,
        librarySaveName: data.library_saved && data.library_item
          ? libraryEntryName(data.library_item)
          : previous.librarySaveName,
        libraryLoadedName: data.library_saved && data.library_item
          ? libraryEntryName(data.library_item)
          : previous.libraryLoadedName,
        error: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setAdaptation((previous) => ({
        ...previous,
        analysisStatus: 'error',
        analysisEvents: err.data?.events || previous.analysisEvents,
        error: err.message
      }));
    }
  };

  const refreshNovelLibrary = async () => {
    const query = adaptation.libraryQuery;
    setAdaptation((previous) => ({
      ...previous,
      libraryStatus: 'running',
      libraryError: '',
      libraryMessage: ''
    }));
    try {
      const data = await listNovelLibrary(query);
      setAdaptation((previous) => ({
        ...previous,
        libraryStatus: 'done',
        libraryItems: libraryItemsFromResponse(data),
        libraryMessage: libraryMessageFromResponse(data),
        libraryError: ''
      }));
    } catch (err) {
      setAdaptation((previous) => ({
        ...previous,
        libraryStatus: 'error',
        libraryError: err.message
      }));
    }
  };

  const saveAnalyzedNovelToLibrary = async () => {
    const projectId = activeProject?.id;
    const loadedName = adaptation.libraryLoadedName.trim();
    const name = adaptation.librarySaveName.trim() || loadedName;
    const sourceFile = adaptation.sourceFile?.relative_path;
    if (!projectId || !name || !sourceFile || adaptation.analysisStatus !== 'done') {
      return;
    }
    const replace = Boolean(loadedName && name === loadedName);
    const query = adaptation.libraryQuery;
    setAdaptation((previous) => ({ ...previous, librarySaveStatus: 'running', librarySaveError: '' }));
    try {
      const saveData = await saveNovelToLibrary(projectId, name, sourceFile, { replace });
      const listData = await listNovelLibrary(query);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setAdaptation((previous) => ({
        ...previous,
        libraryStatus: 'done',
        libraryItems: libraryItemsFromResponse(listData),
        libraryMessage: libraryMessageFromResponse(saveData) || `已保存小说：${name}`,
        libraryError: '',
        librarySaveName: name,
        libraryLoadedName: name,
        librarySaveStatus: 'done',
        librarySaveError: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setAdaptation((previous) => ({ ...previous, librarySaveStatus: 'error', librarySaveError: err.message }));
    }
  };

  const loadNovelFromLibraryEntry = async (entry) => {
    const projectId = activeProject?.id;
    const name = libraryEntryName(entry);
    if (!projectId || !name) {
      return;
    }
    setBusy(true);
    setAdaptation((previous) => ({
      ...previous,
      libraryStatus: 'running',
      libraryError: '',
      libraryMessage: ''
    }));
    try {
      const data = await loadNovelFromLibrary(projectId, name);
      if (!isCurrentProject(projectId)) {
        return;
      }
      const sourceFile = sourceFileFromNovelLoad(data, entry, name);
      const analysisStatus = adaptationStatusFromNovelLoad(data);
      const analysisEvents = adaptationEventsFromNovelLoad(data);
      setCoCreate(createCoCreateState());
      setPlanningRevision(createCoCreatePlanningRevisionState());
      setWorkbench((previous) => ({
        ...previous,
        snapshot: clearAdaptationProposalSnapshot(data.snapshot || previous.snapshot)
      }));
      setAdaptation((previous) => ({
        ...previous,
        sourceFile,
        uploadMessage: libraryMessageFromResponse(data) || `已从小说仓库加载：${name}`,
        analysisStatus,
        analysisEvents,
        proposalKey: '',
        startStatus: 'idle',
        startMessage: '',
        libraryStatus: 'done',
        libraryMessage: libraryMessageFromResponse(data) || `已加载已分析小说：${name}`,
        libraryError: '',
        librarySaveName: name,
        libraryLoadedName: name,
        librarySaveStatus: 'idle',
        librarySaveError: '',
        error: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setAdaptation((previous) => ({
        ...previous,
        libraryStatus: 'error',
        libraryError: err.message
      }));
    } finally {
      if (isCurrentProject(projectId)) {
        setBusy(false);
      }
    }
  };

  const startAdaptationRun = async () => {
    const projectId = activeProject?.id;
    const sourcePath = adaptation.sourceFile?.relative_path;
    const proposalMode = adaptation.mode;
    const proposalBrief = adaptation.brief;
    if (!projectId || !sourcePath || adaptation.analysisStatus !== 'done') {
      return;
    }
    const proposalKey = buildAdaptationProposalKey(adaptation);
    setBusy(true);
    setWorkbench((previous) => ({
      ...previous,
      snapshot: clearAdaptationProposalSnapshot(previous.snapshot)
    }));
    setAdaptation((previous) => ({
      ...previous,
      proposalKey: '',
      startStatus: 'running',
      startMessage: '',
      error: ''
    }));
    try {
      const data = await buildAdaptationProposal(projectId, sourcePath, proposalMode, proposalBrief);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({ ...previous, snapshot: snapshotFromAdaptationProposalResponse(data, previous.snapshot) }));
      setAdaptation((previous) => ({
        ...previous,
        proposalKey,
        startStatus: 'done',
        startMessage: `提案已生成：${adaptationModeLabel(data.mode)} / ${data.rewrite_policy}`,
        error: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      await refreshCurrentProjectSnapshot(projectId);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setAdaptation((previous) => ({
        ...previous,
        startStatus: 'error',
        startMessage: '',
        error: err.message
      }));
    } finally {
      if (isCurrentProject(projectId)) {
        setBusy(false);
      }
    }
  };

  const confirmAdaptationRun = async () => {
    const projectId = activeProject?.id;
    if (!projectId || !isAdaptationProposalCurrent(adaptation)) {
      return;
    }
    const isVolumeReview = adaptationProposalReview.volumeReviewReady;
    setBusy(true);
    setAdaptation((previous) => ({
      ...previous,
      startStatus: 'running',
      startMessage: '',
      error: ''
    }));
    try {
      const data = isVolumeReview
        ? await confirmAdaptationProposalDetails(projectId)
        : await confirmAdaptationProposal(projectId);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({ ...previous, snapshot: snapshotFromAdaptationProposalResponse(data, previous.snapshot) }));
      setAdaptation((previous) => ({
        ...previous,
        startStatus: 'done',
        startMessage: isVolumeReview ? '章节细纲已生成，可继续审阅完整提案' : '提案已确认，写作已启动',
        error: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      await refreshCurrentProjectSnapshot(projectId);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setAdaptation((previous) => ({
        ...previous,
        startStatus: 'error',
        startMessage: '',
        error: err.message
      }));
    } finally {
      if (isCurrentProject(projectId)) {
        setBusy(false);
      }
    }
  };

  const reviseAdaptationProposalRun = async () => {
    const projectId = activeProject?.id;
    if (!projectId || !isAdaptationProposalCurrent(adaptation)) {
      return;
    }
    const payload = adaptationProposalReview.volumeReviewReady
      ? buildVolumeReviewRevisionPayload(adaptation, adaptationProposalReview.volumeReview)
      : buildAdaptationRevisionPayload(adaptation, adaptationProposalReview);
    if (!payload.ok) {
      setAdaptation((previous) => ({
        ...previous,
        revisionStatus: 'error',
        revisionMessage: '',
        error: payload.error
      }));
      return;
    }
    setBusy(true);
    setAdaptation((previous) => ({
      ...previous,
      revisionStatus: 'running',
      revisionMessage: '',
      error: ''
    }));
    try {
      const data = adaptationProposalReview.volumeReviewReady
        ? await reviseAdaptationVolumeReview(projectId, payload.body)
        : await reviseAdaptationProposal(projectId, payload.body);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({ ...previous, snapshot: snapshotFromAdaptationProposalResponse(data, previous.snapshot) }));
      setAdaptation((previous) => ({
        ...previous,
        revisionStatus: 'done',
        revisionMessage: '提案已修订',
        startStatus: 'done',
        startMessage: '提案已修订，可继续审稿或确认启动',
        error: ''
      }));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      await refreshCurrentProjectSnapshot(projectId);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setAdaptation((previous) => ({
        ...previous,
        revisionStatus: 'error',
        revisionMessage: '',
        error: err.message
      }));
    } finally {
      if (isCurrentProject(projectId)) {
        setBusy(false);
      }
    }
  };

  const beginCoCreateFlow = async (kind, options = {}) => {
    if (!activeProject?.id) {
      return;
    }
    const projectId = activeProject.id;
    const hasBackendSession = coCreate.active || (coCreate.messages.length > 0 && !coCreate.intakeActive);
    let initial = String(options.initial ?? coCreate.input).trim();
    if (kind === 'normal' && !initial) {
      if (coCreate.intakeActive && options.confirmIntake) {
        initial = coCreate.intakeInitial.trim();
      }
    }
    if (kind === 'normal' && !initial) {
      setCoCreate((previous) => ({ ...previous, error: '先输入一个核心想法' }));
      return;
    }
    let targetTotalWords = resolveCoCreateTargetTotalWords(coCreate);
    let structureChoice = resolveCoCreateStructureChoice(coCreate);
    if (kind === 'normal' && !hasBackendSession && !coCreate.intakeActive && !options.confirmIntake) {
      const inferred = inferCoCreateIntakeFromInitial(initial);
      if (inferred.targetTotalWords > 0) {
        targetTotalWords = inferred.targetTotalWords;
        structureChoice = inferred.structureChoice;
        initial = buildCoCreateIntakeInitial(initial, {
          targetTotalWords,
          structureChoice
        });
      } else {
        setCoCreate((previous) => ({
          ...previous,
          kind: 'normal',
          intakeActive: true,
          intakeInitial: initial,
          targetTotalWordsChoice: inferred.targetTotalWordsChoice,
          customTargetTotalWords: previous.customTargetTotalWords || inferred.customTargetTotalWords || '',
          structureChoice: inferred.structureChoice || 'single',
          input: '',
          messages: buildCoCreateIntakeMessages(initial),
          ready: false,
          suggestions: [],
          streamThinking: '',
          streamReply: '',
          status: 'idle',
          startMessage: '',
          error: ''
        }));
        return;
      }
    }
    if (kind === 'normal' && coCreate.intakeActive) {
      if (targetTotalWords <= 0) {
        setCoCreate((previous) => ({ ...previous, error: '先确认目标字数' }));
        return;
      }
      initial = buildCoCreateIntakeInitial(coCreate.intakeInitial || initial, {
        targetTotalWords,
        structureChoice
      });
    }
    if (kind === 'adapt' && (!adaptation.sourceFile?.relative_path || adaptation.analysisStatus !== 'done')) {
      setCoCreate((previous) => ({ ...previous, error: '先完成原文分析并选择模式' }));
      return;
    }
    setCoCreate((previous) => ({ ...previous, kind, status: 'running', error: '', startMessage: '', suggestions: [] }));
    try {
      const payload = buildBeginCoCreatePayload({
        kind,
        initial,
        fallbackInitial: kind === 'adapt' ? adaptation.brief : '',
        sourceFile: adaptation.sourceFile?.relative_path,
        mode: adaptation.mode,
        targetTotalWords
      });
      const data = await beginCoCreate(projectId, payload);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setCoCreate((previous) => coCreateStateFromResponse(data, previous));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setCoCreate((previous) => coCreateStateFromError(err, previous));
    }
  };

  const submitCoCreate = async (event, options = {}) => {
    event?.preventDefault?.();
    const text = coCreate.input.trim();
    const hasBackendSession = coCreate.active || (coCreate.messages.length > 0 && !coCreate.intakeActive);
    if (!activeProject?.id || !text || !hasBackendSession || busy || coCreateRequestBusy) {
      return;
    }
    const projectId = activeProject.id;
    setCoCreate((previous) => ({ ...previous, status: 'running', error: '', suggestions: [] }));
    try {
      const data = await sendCoCreate(projectId, text, coCreate.inputSource || 'custom', {
        forceRebrief: Boolean(options.forceRebrief)
      });
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setCoCreate((previous) => coCreateStateFromResponse(data, previous));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setCoCreate((previous) => coCreateStateFromError(err, previous));
    }
  };

  const submitCoCreateSuggestion = (suggestion) => {
    const text = String(suggestion || '').trim();
    const hasBackendSession = coCreate.active || (coCreate.messages.length > 0 && !coCreate.intakeActive);
    if (!activeProject?.id || !text || !hasBackendSession || busy || coCreateRequestBusy) {
      return;
    }
    setCoCreate((previous) => applyCoCreateSuggestion({ ...previous, error: '' }, text));
  };

  const reviseCoCreateMessage = async (messageId, text) => {
    if (!activeProject?.id || !messageId || !String(text || '').trim() || busy || coCreateRequestBusy) {
      return;
    }
    const projectId = activeProject.id;
    setCoCreate((previous) => ({ ...previous, status: 'running', error: '' }));
    try {
      const data = await reviseCoCreate(projectId, messageId, text);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setCoCreate((previous) => coCreateStateFromResponse(data, previous));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setCoCreate((previous) => coCreateStateFromError(err, previous));
    }
  };

  const resolveCoCreateDecisionFlow = async (decisionId, optionId = '', customAnswer = '') => {
    const decisions = Array.isArray(decisionId) ? decisionId : null;
    const hasDecision = decisions ? decisions.length > 0 : Boolean(decisionId);
    if (!activeProject?.id || !hasDecision || busy || coCreateRequestBusy) {
      return;
    }
    const projectId = activeProject.id;
    setCoCreate((previous) => ({ ...previous, status: 'running', error: '' }));
    try {
      const data = decisions
        ? await resolveCoCreateDecisions(projectId, decisions)
        : await resolveCoCreateDecision(projectId, decisionId, optionId, customAnswer);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setCoCreate((previous) => coCreateStateFromResponse(data, previous));
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setCoCreate((previous) => coCreateStateFromError(err, previous));
    }
  };

  const commitCoCreateFlow = async () => {
    if (!activeProject?.id || !coCreate.draftPrompt.trim() || busy || coCreateRequestBusy) {
      return;
    }
    const projectId = activeProject.id;
    setCoCreate((previous) => ({ ...previous, status: 'running', error: '' }));
    try {
      const data = await commitCoCreate(projectId);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setCoCreate((previous) => coCreateStateFromResponse(data, previous));
      if ((data.cocreate?.kind || coCreate.kind) === 'adapt') {
        setAdaptation((previous) => applyAdaptationProposalSnapshot(previous, data.snapshot));
        setSideView('adapt');
      } else if ((data.cocreate?.kind || coCreate.kind) === 'continuation') {
        let continuationData = data.continuation || null;
        if (!continuationData) {
          try {
            continuationData = await getContinuation(projectId);
          } catch {
            continuationData = null;
          }
        }
        setContinuation((previous) => restoreContinuationState(previous, continuationData, data.snapshot));
        setSideView('continuation');
      }
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setCoCreate((previous) => coCreateStateFromError(err, previous));
    }
  };

  const updateCoreCastFlow = async (contract) => {
    if (!activeProject?.id || !contract || busy || coCreateRequestBusy) return;
    const projectId = activeProject.id;
    try {
      const data = await updateCoreCast(projectId, contract, coCreate.coreCast?.revision || 0);
      if (isCurrentProject(projectId)) setCoCreate((previous) => coCreateStateFromResponse(data, previous));
    } catch (err) {
      if (isCurrentProject(projectId)) setCoCreate((previous) => coCreateStateFromError(err, previous));
    }
  };

  const confirmCoreCastFlow = async () => {
    if (!activeProject?.id || !coCreate.coreCast || busy || coCreateRequestBusy) return;
    const projectId = activeProject.id;
    try {
      const data = await confirmCoreCast(projectId, coCreate.coreCast.revision, coCreate.castSignature);
      if (isCurrentProject(projectId)) setCoCreate((previous) => coCreateStateFromResponse(data, previous));
    } catch (err) {
      if (isCurrentProject(projectId)) setCoCreate((previous) => coCreateStateFromError(err, previous));
    }
  };

  const unconfirmCoreCastFlow = async () => {
    if (!activeProject?.id || !coCreate.coreCast || busy || coCreateRequestBusy) return;
    const projectId = activeProject.id;
    try {
      const data = await unconfirmCoreCast(projectId, coCreate.coreCast.revision);
      if (isCurrentProject(projectId)) setCoCreate((previous) => coCreateStateFromResponse(data, previous));
    } catch (err) {
      if (isCurrentProject(projectId)) setCoCreate((previous) => coCreateStateFromError(err, previous));
    }
  };

  const confirmCoCreatePlanningRun = async () => {
    if (!activeProject?.id || !coCreatePlanningReview.pending || busy || projectRunning) {
      return;
    }
    setBusy(true);
    setCoCreate((previous) => ({ ...previous, status: 'running', error: '', startMessage: '' }));
    const projectId = activeProject.id;
    const projectEpoch = projectOpenSeqRef.current;
    const requestSeq = ++planningMutationSeqRef.current;
    const requestIsCurrent = () => isCurrentProject(projectId) &&
      isProjectScopedResponseCurrent(projectId, activeProjectIdRef.current, projectEpoch, projectOpenSeqRef.current) &&
      requestSeq === planningMutationSeqRef.current;
    try {
      const data = coCreatePlanningReview.kind === 'foundation'
        ? await confirmCoCreateFoundation(projectId, coCreatePlanningReview.foundationRevision, coCreatePlanningReview.foundationAuditSignature)
        : await confirmCoCreatePlanning(projectId);
      if (!requestIsCurrent()) return;
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setCoCreate((previous) => ({
        ...previous,
        active: false,
        status: 'started',
        canStart: false,
        startMessage: data.label || '规划已通过，创作已启动',
        error: ''
      }));
    } catch (err) {
      try {
        const snapshotData = await getSnapshot(projectId);
        if (requestIsCurrent()) setWorkbench((previous) => ({ ...previous, snapshot: snapshotData.snapshot || previous.snapshot }));
      } catch {
        // Keep the confirmation error visible if snapshot refresh also fails.
      }
      if (requestIsCurrent()) setCoCreate((previous) => ({ ...previous, status: 'error', error: err.message }));
    } finally {
      if (requestIsCurrent()) setBusy(false);
    }
  };

  const reviseCoCreatePlanningRun = async (manualFeedback = '') => {
    if (
      !activeProject?.id ||
      !coCreatePlanningReview.pending ||
      busy ||
      projectRunning ||
      planningRevisionInFlightProjectRef.current === activeProject.id
    ) {
      return;
    }
    const foundationFeedback = typeof manualFeedback === 'string' && manualFeedback.trim()
      ? manualFeedback.trim()
      : String(planningRevision?.feedback || '').trim();
    const payload = coCreatePlanningReview.kind === 'foundation'
      ? { ok: Boolean(foundationFeedback), error: '请输入 Foundation 修改意见', body: { feedback: foundationFeedback } }
      : buildCoCreatePlanningRevisionPayload(planningRevision, coCreatePlanningReview);
    if (!payload.ok) {
      setPlanningRevision((previous) => ({
        ...previous,
        status: 'error',
        message: '',
        error: payload.error
      }));
      return;
    }
    setBusy(true);
    const projectId = activeProject.id;
    planningRevisionInFlightProjectRef.current = projectId;
    const projectEpoch = projectOpenSeqRef.current;
    const requestSeq = ++planningMutationSeqRef.current;
    const requestIsCurrent = () => isCurrentProject(projectId) &&
      isProjectScopedResponseCurrent(projectId, activeProjectIdRef.current, projectEpoch, projectOpenSeqRef.current) &&
      requestSeq === planningMutationSeqRef.current;
    setPlanningRevision((previous) => ({
      ...previous,
      status: 'running',
      message: '',
      error: ''
    }));
    try {
      const data = coCreatePlanningReview.kind === 'foundation'
        ? await reviseCoCreateFoundation(projectId, foundationFeedback)
        : await reviseCoCreatePlanning(projectId, payload.body);
      if (!requestIsCurrent()) return;
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setPlanningRevision({
        feedback: '',
        characterFeedback: '',
        allowSupportingCharacters: false,
        instruction: '',
        scope: 'all',
        chapter: '1',
        volumeIndex: '1',
        status: 'done',
        message: '审核意见已提交，AI 正在重新生成规划',
        error: ''
      });
      setCoCreate((previous) => ({
        ...previous,
        status: 'started',
        startMessage: '审核意见已提交，AI 正在重新生成规划',
        error: ''
      }));
    } catch (err) {
      try {
        const snapshotData = await getSnapshot(projectId);
        if (requestIsCurrent()) setWorkbench((previous) => ({ ...previous, snapshot: snapshotData.snapshot || previous.snapshot }));
      } catch {
        // Keep the revision error visible if snapshot refresh also fails.
      }
      if (requestIsCurrent()) setPlanningRevision((previous) => ({
        ...previous,
        status: 'error',
        message: '',
        error: err.message
      }));
    } finally {
      if (planningRevisionInFlightProjectRef.current === projectId) {
        planningRevisionInFlightProjectRef.current = '';
      }
      if (requestIsCurrent()) setBusy(false);
    }
  };

  const cancelCoCreateFlow = async () => {
    if (!activeProject?.id || !coCreate.active) {
      setCoCreate(createCoCreateState());
      return;
    }
    const projectId = activeProject.id;
    try {
      const data = await cancelCoCreate(projectId);
      if (!isCurrentProject(projectId)) {
        return;
      }
      setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      setCoCreate(createCoCreateState());
    } catch (err) {
      if (!isCurrentProject(projectId)) {
        return;
      }
      setCoCreate((previous) => coCreateStateFromError(err, previous));
    }
  };

  const refreshBackendStatus = async () => {
    if (!activeProject?.id) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = await getBackendStatus(activeProject.id);
      setBackendStatus(data.backend || null);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const runBackendTest = async () => {
    if (!activeProject?.id) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = await testBackend(activeProject.id);
      setBackendStatus(data.backend || null);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const switchModelRoute = async (role, provider, model) => {
    if (!role || !provider || !model) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = activeProject?.id
        ? await switchProjectModel(activeProject.id, role, provider, model)
        : await switchGlobalModel(role, provider, model);
      setModelConfig(data.models || modelConfig);
      if (data.runtime) {
        setRuntime(data.runtime);
      }
      if (data.snapshot) {
        setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      }
      if (activeProject?.id) {
        const status = await getBackendStatus(activeProject.id);
        setBackendStatus(status.backend || null);
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const inheritModelRoute = async (role) => {
    if (!role || role === 'default') {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = activeProject?.id
        ? await inheritProjectModel(activeProject.id, role)
        : await inheritGlobalModel(role);
      setModelConfig(data.models || modelConfig);
      if (data.runtime) {
        setRuntime(data.runtime);
      }
      if (data.snapshot) {
        setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      }
      if (activeProject?.id) {
        const status = await getBackendStatus(activeProject.id);
        setBackendStatus(status.backend || null);
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const switchDefaultModel = async (provider, model) => {
    if (!provider || !model) {
      return;
    }
    if (activeProject?.id) {
      await switchModelRoute('default', provider, model);
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = await switchGlobalDefaultModel(provider, model);
      if (data.runtime) {
        setRuntime(data.runtime);
      }
      if (!activeProject?.id) {
        setModelConfig(data.models || null);
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const changeThinking = async (role, level) => {
    setBusy(true);
    setError('');
    try {
      const data = activeProject?.id
        ? await setProjectThinking(activeProject.id, role, level)
        : await setGlobalThinking(role, level);
      setModelConfig(data.models || modelConfig);
      if (data.runtime) {
        setRuntime(data.runtime);
      }
      if (data.snapshot) {
        setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const changeCoCreateTimeout = async (seconds) => {
    const value = Number(seconds);
    if (!Number.isInteger(value) || value < 1 || value > 3600) {
      setError('共创超时必须是 1-3600 秒之间的整数');
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = activeProject?.id
        ? await setProjectCoCreateTimeout(activeProject.id, value)
        : await setGlobalCoCreateTimeout(value);
      setModelConfig(data.models || modelConfig);
      if (data.runtime) {
        setRuntime(data.runtime);
      }
      if (data.snapshot) {
        setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const changeCoCreateMaxTokens = async (tokens) => {
    const value = Number(tokens);
    if (!Number.isInteger(value) || value < 512 || value > 32768) {
      setError('共创输出 tokens 必须是 512-32768 之间的整数');
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = activeProject?.id
        ? await setProjectCoCreateMaxTokens(activeProject.id, value)
        : await setGlobalCoCreateMaxTokens(value);
      setModelConfig(data.models || modelConfig);
      if (data.runtime) {
        setRuntime(data.runtime);
      }
      if (data.snapshot) {
        setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const changeRetrySettings = async (modelCallMaxAttempts, structureRepairMaxAttempts, budgetQualityMaxAttempts, adaptationOutlineAuditRetryMaxAttempts) => {
    const modelAttempts = Number(modelCallMaxAttempts);
    const repairAttempts = Number(structureRepairMaxAttempts);
    const budgetAttempts = Number(budgetQualityMaxAttempts);
    const auditAttempts = Number(adaptationOutlineAuditRetryMaxAttempts);
    if (!Number.isInteger(modelAttempts) || modelAttempts < 1 || modelAttempts > 30) {
      setError('模型调用总尝试次数必须是 1-30 之间的整数');
      return;
    }
    if (!Number.isInteger(repairAttempts) || repairAttempts < 1 || repairAttempts > 15) {
      setError('结构修复次数必须是 1-15 之间的整数');
      return;
    }
    if (!Number.isInteger(auditAttempts) || auditAttempts < 1 || auditAttempts > 15) {
      setError('改编章节提纲审计重试次数必须是 1-15 之间的整数');
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = activeProject?.id
        ? await setProjectRetrySettings(activeProject.id, modelAttempts, repairAttempts, budgetAttempts, auditAttempts)
        : await setGlobalRetrySettings(modelAttempts, repairAttempts, budgetAttempts, auditAttempts);
      if (activeProject?.id) {
        const modelData = await getProjectModels(activeProject.id);
        setModelConfig(modelData.models || data.models || modelConfig);
        const status = await getBackendStatus(activeProject.id);
        setBackendStatus(status.backend || null);
      } else {
        setModelConfig(data.models || modelConfig);
      }
      if (data.runtime) {
        setRuntime(data.runtime);
      }
      if (data.snapshot) {
        setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const deleteModelRoute = async (provider, model) => {
    if (!provider || !model) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const data = activeProject?.id
        ? await deleteProviderModel(activeProject.id, provider, model)
        : await deleteGlobalProviderModel(provider, model);
      setModelConfig(data.models || modelConfig);
      if (data.runtime) {
        setRuntime(data.runtime);
      }
      if (data.snapshot) {
        setWorkbench((previous) => ({ ...previous, snapshot: data.snapshot || previous.snapshot }));
      }
      if (activeProject?.id) {
        const status = await getBackendStatus(activeProject.id);
        setBackendStatus(status.backend || null);
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const submitCustomModel = async (event) => {
    event.preventDefault();
    const payload = buildModelAddPayload(customModel, modelConfig);
    const validationMessage = modelAddValidationMessage(customModel, modelConfig);
    if (validationMessage) {
      setError(validationMessage);
      setCustomModel((previous) => ({
        ...previous,
        test_scope: 'editor',
        test_status: 'error',
        test_message: validationMessage
      }));
      return;
    }
    setBusy(true);
    setError('');
    setCustomModel((previous) => ({
      ...previous,
      test_scope: 'editor',
      test_status: 'running',
      test_message: customModel.mode === 'existing' ? '正在保存配置...' : '正在保存模型...'
    }));
    try {
      let nextModels = modelConfig;
      const saveTarget = modelAddSaveTarget(activeProject, payload);
      const data = await addGlobalProviderModel(payload);
      nextModels = data.models || modelConfig;
      if (data.runtime) {
        setRuntime(data.runtime);
      }
      if (saveTarget.projectId) {
        const modelData = await getProjectModels(saveTarget.projectId);
        nextModels = modelData.models || nextModels;
        if (isCurrentProject(saveTarget.projectId)) {
          setModelConfig(nextModels);
          const status = await getBackendStatus(saveTarget.projectId);
          setBackendStatus(status.backend || null);
        }
      } else {
        setModelConfig(nextModels);
      }
      setCustomModel(modelAddExistingProviderDefaults({
        ...createCustomModelState(),
        test_scope: 'editor',
        test_status: 'ok',
        test_message: customModel.mode === 'existing' ? '配置已保存' : '模型已保存'
      }, nextModels?.providers || [], payload.provider, payload.model));
    } catch (err) {
      setError(err.message);
      setCustomModel((previous) => ({
        ...previous,
        test_scope: 'editor',
        test_status: 'error',
        test_message: err.message
      }));
    } finally {
      setBusy(false);
    }
  };

  const discoverCustomModelModels = async (payloadOverride = null, scope = 'editor') => {
    const usesOverride = Boolean(payloadOverride);
    const payload = usesOverride ? payloadOverride : buildModelAddPayload(customModel, modelConfig);
    if (!payload.provider) {
      return;
    }
    setBusy(true);
    setError('');
    setCustomModel((previous) => ({
      ...previous,
      test_scope: scope,
      test_status: 'running',
      test_message: '正在测试连接...'
    }));
    try {
      const data = activeProject?.id
        ? await discoverProjectProviderModels(activeProject.id, payload)
        : await discoverGlobalProviderModels(payload);
      const discovery = data.discovery || {};
      const models = Array.isArray(discovery.models) ? discovery.models : [];
      setCustomModel((previous) => ({
        ...previous,
        ...(usesOverride ? {} : {
          discovered_models: models,
          model: previous.model || models[0] || ''
        }),
        test_scope: scope,
        test_status: discovery.status || 'ok',
        test_message: modelDiscoveryMessage(discovery, models)
      }));
    } catch (err) {
      setError(err.message);
      setCustomModel((previous) => ({
        ...previous,
        test_scope: scope,
        test_status: 'error',
        test_message: err.message
      }));
    } finally {
      setBusy(false);
    }
  };

  const testCustomModelConnection = async (payloadOverride = null, scope = 'editor') => {
    const payload = payloadOverride || buildModelAddPayload(customModel, modelConfig);
    if (!payload.provider || !payload.model) {
      return;
    }
    setBusy(true);
    setError('');
    setCustomModel((previous) => ({
      ...previous,
      test_scope: scope,
      test_status: 'running',
      test_message: '正在测试模型...'
    }));
    try {
      const data = activeProject?.id
        ? await testProjectProviderModel(activeProject.id, payload)
        : await testGlobalProviderModel(payload);
      const result = data.test || {};
      setCustomModel((previous) => ({
        ...previous,
        test_scope: scope,
        test_status: result.status || 'ok',
        test_message: result.message || '模型测试完成'
      }));
    } catch (err) {
      setError(err.message);
      setCustomModel((previous) => ({
        ...previous,
        test_scope: scope,
        test_status: 'error',
        test_message: err.message
      }));
    } finally {
      setBusy(false);
    }
  };

  const startGrokOAuthLogin = async () => {
    const authWindow = openPendingGrokAuthWindow();
    setError('');
    setCustomModel((previous) => ({ ...previous, grok_message: '正在创建 Grok OAuth 登录会话...' }));
    try {
      const data = await startGrokLogin(activeProject?.id, customModel.account_id, customModel.account_name, true);
      const login = data.login || null;
      const authorizeURL = grokAuthorizeURL(login);
      const browserOpened = Boolean(data.browser_opened || data.browserOpened);
      const browserOpenError = String(data.browser_open_error || data.browserOpenError || '').trim();
      let openedAuthorize = browserOpened;
      if (browserOpened) {
        closeGrokAuthWindow(authWindow);
      } else {
        openedAuthorize = navigateGrokAuthWindow(authWindow, authorizeURL);
      }
      setCustomModel((previous) => ({
        ...previous,
        grok_login: login,
        grok_status: grokLoggedIn(previous.grok_status) ? previous.grok_status : grokStatusFromLogin(login) || previous.grok_status,
        grok_message: grokLoginMessage(login) || grokOpenMessage(openedAuthorize, browserOpenError)
      }));
    } catch (err) {
      closeGrokAuthWindow(authWindow);
      setError(err.message);
      setCustomModel((previous) => ({ ...previous, grok_message: err.message }));
    }
  };

  const pollGrokOAuthLogin = async () => {
    setError('');
    try {
      const data = await pollGrokLogin(activeProject?.id);
      const login = data.login || null;
      setCustomModel((previous) => ({
        ...previous,
        grok_login: login,
        grok_status: grokLoggedIn(previous.grok_status) ? previous.grok_status : grokStatusFromLogin(login) || previous.grok_status,
        grok_message: grokLoginMessage(login) || (grokLoginDone(login) ? 'Grok OAuth 已完成' : '等待 Grok OAuth 回调')
      }));
    } catch (err) {
      setError(err.message);
      setCustomModel((previous) => ({ ...previous, grok_message: err.message }));
    }
  };

  const completeGrokOAuthLogin = async () => {
    const callback = String(customModel.callback_input || '').trim();
    if (!callback) {
      setCustomModel((previous) => ({ ...previous, grok_message: '请粘贴 callback URL、query string 或一次性 code' }));
      return;
    }
    setError('');
    try {
      const data = await completeGrokLogin(activeProject?.id, callback);
      setCustomModel((previous) => ({
        ...previous,
        callback_input: '',
        grok_status: data.status || null,
        grok_message: grokLoggedIn(data.status) ? 'Grok OAuth 已登录' : grokAuthSummary(data.status)
      }));
    } catch (err) {
      setError(err.message);
      setCustomModel((previous) => ({ ...previous, grok_message: err.message }));
    }
  };

  const refreshGrokOAuthStatus = async () => {
    setError('');
    try {
      const data = await getGrokLoginStatus(activeProject?.id, customModel.account_id);
      setCustomModel((previous) => ({
        ...previous,
        grok_status: data.status || null,
        grok_message: grokAuthSummary(data.status)
      }));
    } catch (err) {
      setError(err.message);
      setCustomModel((previous) => ({ ...previous, grok_message: err.message }));
    }
  };

  const refreshCodexAuthStatus = async () => {
    setError('');
    try {
      const data = await getCodexAuthStatus(activeProject?.id, customModel.auth_file);
      setCustomModel((previous) => ({
        ...previous,
        codex_status: data.status || null,
        codex_message: codexAuthSummary(data.status)
      }));
    } catch (err) {
      setError(err.message);
      setCustomModel((previous) => ({ ...previous, codex_message: err.message }));
    }
  };

  const uploadCodexCredential = async (event) => {
    const input = event.currentTarget;
    const file = input.files?.[0];
    if (!file) {
      return;
    }
    setBusy(true);
    setError('');
    setCustomModel((previous) => ({ ...previous, codex_message: '正在上传并校验 auth.json...' }));
    try {
      const data = await uploadCodexAuthFile(file);
      const status = data.status || null;
      setCustomModel((previous) => ({
        ...previous,
        auth_file: data.auth_file || '',
        auth_file_name: data.file?.name || file.name,
        codex_status: status,
        codex_message: `auth.json 上传成功；${codexAuthSummary(status)}`,
        test_status: 'idle',
        test_message: ''
      }));
    } catch (err) {
      setError(err.message);
      setCustomModel((previous) => ({ ...previous, codex_message: err.message }));
    } finally {
      input.value = '';
      setBusy(false);
    }
  };

  const sortedEvents = useMemo(
    () => workbench.eventRows.slice().sort((a, b) => b.seq - a.seq),
    [workbench.eventRows]
  );
  const showCoCreatePlanningWorkspace = sideView === 'cocreate' && coCreatePlanningReview.active;
  const showCoCreateWorkspace = sideView === 'cocreate' && !showCoCreatePlanningWorkspace && hasCoCreateWorkspaceContent(coCreate);
  const showWorkflowProgress = shouldShowWorkflowProgressPanel({
    centerView,
    adaptationProposalReviewVisible: showAdaptationProposalWorkspace,
    continuationConfirmationVisible:
      showContinuationWorkspace && continuationNeedsConfirmation(continuationSnapshot),
    planningReviewCollecting: coCreatePlanningReview.collecting,
    planningReviewVisible: showCoCreatePlanningWorkspace,
    reviewActionRunning: projectBusy
  });
  const globalCoCreateVisible = Boolean(activeProject && shouldShowGlobalCoCreate({ snapshot, coCreate, planningReview: coCreatePlanningReview }));
  useEffect(() => {
    if (sideView !== 'cocreate' || globalCoCreateVisible) return;
    setSideView(coCreate.kind === 'adapt' ? 'adapt' : 'status');
  }, [sideView, globalCoCreateVisible, coCreate.kind]);
  const openTool = (view) => {
    setSideView(view);
    if (view === 'manuscript') setCenterView('manuscript');
    setMobileToolView('detail');
    setToolDrawerOpen(true);
  };
  const openFoundationCenter = () => {
    setFoundationNavigation({ projectId: '', tab: '', anchor: '', requestId: 0 });
    setCenterView('foundation');
    setProjectDrawerOpen(false);
    setToolDrawerOpen(false);
    setMobileActionsOpen(false);
  };
  const resumeFromCurrentState = () => {
    if (currentProjectNextAction?.code === 'confirm_character_candidate') {
      openFoundationCharacterConfirmation();
      return;
    }
    if (continuationNeedsReview(continuationSnapshot) ||
      (continuationSnapshot.exists && !continuationCanResume(continuationSnapshot))) {
      openTool('continuation');
      return;
    }
    runAction(resumeProject, { reportResumeNoOp: true });
  };
  const openMobileProjects = () => {
    mobileOverlayTriggerRef.current = globalThis.document?.activeElement || null;
    setMobileActionsOpen(false);
    setToolDrawerOpen(false);
    setProjectDrawerOpen(true);
  };
  const openMobileActions = () => {
    setProjectDrawerOpen(false);
    setToolDrawerOpen(false);
    setMobileActionsOpen((open) => !open);
  };
  const selectMobileWriting = () => {
    setCenterView('writing');
    if (sideView === 'manuscript') setSideView('status');
    setProjectDrawerOpen(false);
    setToolDrawerOpen(false);
    setMobileActionsOpen(false);
  };
  const selectMobileManuscript = () => {
    setCenterView('manuscript');
    setSideView('manuscript');
    setProjectDrawerOpen(false);
    setToolDrawerOpen(false);
    setMobileActionsOpen(false);
  };
  const openMobileToolMenu = () => {
    mobileOverlayTriggerRef.current = globalThis.document?.activeElement || null;
    setProjectDrawerOpen(false);
    setMobileActionsOpen(false);
    setMobileToolView('menu');
    setToolDrawerOpen(true);
  };
  const openMobileToolDetail = (view) => {
    setMobileActionsOpen(false);
    setProjectDrawerOpen(false);
    openTool(view);
  };
  const closeMobileDrawers = () => {
    setProjectDrawerOpen(false);
    setToolDrawerOpen(false);
    globalThis.requestAnimationFrame?.(() => mobileOverlayTriggerRef.current?.focus?.());
  };
  useEffect(() => {
    if (!projectDrawerOpen && !toolDrawerOpen) return undefined;
    const closeOnEscape = (event) => {
      if (event.key !== 'Escape' || event.defaultPrevented) return;
      event.preventDefault();
      closeMobileDrawers();
    };
    globalThis.document?.addEventListener('keydown', closeOnEscape);
    return () => globalThis.document?.removeEventListener('keydown', closeOnEscape);
  }, [projectDrawerOpen, toolDrawerOpen]);
  const openFoundationReviewTab = (tab = 'overview') => {
    setCenterView('writing');
    setSideView('cocreate');
    setFoundationReviewTab(tab);
    setMobileToolView('detail');
    setToolDrawerOpen(true);
    globalThis.requestAnimationFrame?.(() => {
      globalThis.document?.getElementById(`foundation-review-tab-${tab}`)?.scrollIntoView({
        block: 'nearest',
        inline: 'nearest'
      });
    });
  };
  const openFoundationCharacterConfirmation = () => {
    openFoundationReviewTab('characters');
    globalThis.requestAnimationFrame?.(() => {
      const target = globalThis.document?.getElementById('foundation-review-confirm-character');
      target?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
      target?.focus?.();
    });
  };
  const openFoundationCharacterRevision = () => {
    setFoundationNavigation({
      projectId: activeProject?.id || '',
      tab: 'characters',
      anchor: 'foundation-character-agent',
      requestId: Date.now()
    });
    setCenterView('foundation');
    setToolDrawerOpen(false);
  };
  const reviseReviewedCharacterCandidate = async ({
    feedback = '',
    allowSupportingCharacters = false
  } = {}) => {
    const instruction = String(feedback || '').trim();
    if (!activeProject?.id ||
      !coCreatePlanningReview.characterConfirmationRequired ||
      busy ||
      projectRunning) {
      return;
    }
    if (!instruction) {
      setPlanningRevision((previous) => ({
        ...previous,
        status: 'error',
        message: '',
        error: '请输入角色卡修改意见'
      }));
      return;
    }
    const projectId = activeProject.id;
    const projectEpoch = projectOpenSeqRef.current;
    const requestSeq = ++planningMutationSeqRef.current;
    const requestIsCurrent = () => isCurrentProject(projectId) &&
      isProjectScopedResponseCurrent(projectId, activeProjectIdRef.current, projectEpoch, projectOpenSeqRef.current) &&
      requestSeq === planningMutationSeqRef.current;
    setBusy(true);
    setPlanningRevision((previous) => ({
      ...previous,
      status: 'running',
      message: '正在提交角色卡修改意见…',
      error: ''
    }));
    try {
      await startReviewedCharacterRevision(projectId, coCreatePlanningReview, {
        feedback: instruction,
        allowSupportingCharacters,
        isCurrent: requestIsCurrent
      });
      if (!requestIsCurrent()) return;
      setPlanningRevision((previous) => ({
        ...previous,
        characterFeedback: '',
        status: 'done',
        message: '修改意见已提交；Character Agent 正在生成新候选，完成后请重新审核',
        error: ''
      }));
      setFoundationNavigation({
        projectId,
        tab: 'characters',
        anchor: 'foundation-character-agent',
        requestId: Date.now()
      });
      setCenterView('foundation');
      setToolDrawerOpen(false);
    } catch (err) {
      const classified = foundationError(err);
      if (requestIsCurrent()) {
        setPlanningRevision((previous) => ({
          ...previous,
          status: 'error',
          message: '',
          error: classified.message
        }));
      }
    } finally {
      if (requestIsCurrent()) setBusy(false);
    }
  };
  const confirmReviewedCharacterCandidate = async () => {
    if (!activeProject?.id || !coCreatePlanningReview.characterConfirmationRequired || busy || projectRunning) {
      return;
    }
    const projectId = activeProject.id;
    const projectEpoch = projectOpenSeqRef.current;
    const requestSeq = ++planningMutationSeqRef.current;
    const requestIsCurrent = () => isCurrentProject(projectId) &&
      isProjectScopedResponseCurrent(projectId, activeProjectIdRef.current, projectEpoch, projectOpenSeqRef.current) &&
      requestSeq === planningMutationSeqRef.current;
    setBusy(true);
    setError('');
    try {
      const response = await confirmCharacterCandidate(
        projectId,
        {
          revision: coCreatePlanningReview.characterCandidateRevision,
          digest: coCreatePlanningReview.characterCandidateDigest
        },
        foundationIdempotencyKey('review-character-confirm')
      );
      if (!requestIsCurrent()) return;
      const nextSnapshot = response.runtime || response.snapshot || snapshot;
      setWorkbench((previous) => ({ ...previous, snapshot: nextSnapshot || previous.snapshot }));
      setCoCreate((previous) => ({
        ...previous,
        status: 'started',
        error: '',
        startMessage: '角色卡已确认，正在生成完整 StoryFoundation'
      }));
      setFoundationReviewTab('overview');
    } catch (err) {
      try {
        const snapshotData = await getSnapshot(projectId);
        if (requestIsCurrent()) {
          setWorkbench((previous) => ({ ...previous, snapshot: snapshotData.snapshot || previous.snapshot }));
        }
      } catch {
        // Keep the confirmation error visible if snapshot refresh also fails.
      }
      if (requestIsCurrent()) {
        setError(err.message || '角色卡确认失败，请稍后重试');
      }
    } finally {
      if (requestIsCurrent()) setBusy(false);
    }
  };

  const updateSetupProvider = (providerID) => {
    const option = setup.providers.find((item) => item.id === providerID) || {};
    setSetup((previous) => ({
      ...previous,
      provider: providerID,
      type: option.type || previous.type || 'openai',
      baseURL: option.base_url || '',
      testStatus: 'idle',
      message: '',
      error: ''
    }));
  };

  const setupPayload = () => ({
    role: 'default',
    provider: setup.provider,
    label: setup.providers.find((item) => item.id === setup.provider)?.label || setup.provider,
    template_provider: setup.provider === 'custom' ? setup.type : setup.provider,
    type: setup.type,
    model: setup.model.trim(),
    api_key: setup.apiKey.trim(),
    base_url: setup.baseURL.trim(),
    select_after_save: true
  });

  const testInitialModel = async () => {
    setSetup((previous) => ({ ...previous, testStatus: 'running', message: '', error: '' }));
    try {
      const data = await testSetupModel(setupPayload());
      const passed = data?.test?.status === 'ok';
      setSetup((previous) => ({
        ...previous,
        testStatus: passed ? 'passed' : 'failed',
        message: passed ? '连接测试通过，可以保存配置。' : '',
        error: passed ? '' : (data?.test?.message || '连接测试失败')
      }));
    } catch (err) {
      setSetup((previous) => ({ ...previous, testStatus: 'failed', error: err.message }));
    }
  };

  const finishInitialSetup = async () => {
    setSetup((previous) => ({ ...previous, testStatus: 'saving', error: '' }));
    try {
      await completeSetup(setupPayload());
      setSetup((previous) => ({ ...previous, required: false, testStatus: 'passed', message: '配置已保存。' }));
      await loadShell();
    } catch (err) {
      setSetup((previous) => ({ ...previous, testStatus: 'failed', error: err.message }));
    }
  };

  if (setup.loading) {
    return <div className="setup-loading" role="status">正在准备小说工作台…</div>;
  }

  if (setup.required) {
    return (
      <SetupWizard
        setup={setup}
        setSetup={setSetup}
        onProviderChange={updateSetupProvider}
        onTest={testInitialModel}
        onComplete={finishInitialSetup}
      />
    );
  }

  return (
    <div className={`app-shell ${centerView === 'foundation' ? 'foundation-focus' : ''}`}>
      <MobileWorkspaceChrome
        actionsOpen={mobileActionsOpen}
        connection={connection}
        currentView={toolDrawerOpen ? 'tools' : centerView}
        projectName={activeProject?.name || projectOpen.project?.name || ''}
        onCloseActions={() => setMobileActionsOpen(false)}
        onOpenActions={openMobileActions}
        onOpenProjects={openMobileProjects}
        onOpenTools={openMobileToolMenu}
        onSelectManuscript={selectMobileManuscript}
        onSelectWriting={selectMobileWriting}
        actions={(
          <>
            <button className="tool-button" disabled={!activeProject || projectOpen.status === 'loading'} onClick={openFoundationCenter} type="button">
              <Database size={17} />
              设定中心
            </button>
            {centerView === 'manuscript' ? (
              <button className="tool-button" disabled={!activeProject} onClick={() => openMobileToolDetail('manuscript')} type="button">
                <BookOpen size={17} />
                稿件控制
              </button>
            ) : null}
            <button
              className="tool-button"
              disabled={!activeProject || projectBusy}
              onClick={() => {
                setMobileActionsOpen(false);
                openProject(activeProject);
              }}
              type="button"
            >
              <ListRestart size={17} />
              保存快照
            </button>
            <button
              className="tool-button"
              disabled={!activeProject || !projectRunning}
              onClick={() => {
                setMobileActionsOpen(false);
                pauseWriting();
              }}
              type="button"
            >
              <PauseCircle size={17} />
              暂停
            </button>
            <button
              className="tool-button accent"
              disabled={!activeProject || projectBusy}
              onClick={() => {
                setMobileActionsOpen(false);
                resumeFromCurrentState();
              }}
              type="button"
            >
              <Play size={17} />
              恢复
            </button>
            <button
              className="tool-button danger-ghost"
              disabled={!activeProject || projectBusy}
              onClick={() => {
                setMobileActionsOpen(false);
                openRollbackDialog();
              }}
              type="button"
            >
              <RotateCcw size={17} />
              回退
            </button>
          </>
        )}
      />
      <nav className="mobile-workspace-nav" aria-label="移动端工作台导航">
        <button aria-expanded={projectDrawerOpen} onClick={() => {
          setToolDrawerOpen(false);
          setProjectDrawerOpen((open) => !open);
        }} type="button">
          <BookOpen size={17} />
          项目
        </button>
        <strong>{activeProject?.name || '小说工作台'}</strong>
        <button aria-expanded={toolDrawerOpen} onClick={() => {
          setProjectDrawerOpen(false);
          setToolDrawerOpen((open) => !open);
        }} type="button">
          <SlidersHorizontal size={17} />
          工具
        </button>
      </nav>
      {(projectDrawerOpen || toolDrawerOpen) ? (
        <button
          aria-label="关闭侧栏"
          className="mobile-drawer-backdrop"
          onClick={closeMobileDrawers}
          type="button"
        />
      ) : null}
      <aside className={`project-pane ${projectDrawerOpen ? 'mobile-open' : ''}`} aria-label="项目导航">
        <div className="pane-header">
          <div className="brand-lockup">
            <div className="brand-mark">
              <BookOpen size={20} />
            </div>
            <div>
              <div className="eyebrow">ainovel</div>
              <h1>小说工作台</h1>
            </div>
          </div>
          <button className="icon-button" onClick={loadShell} title="刷新项目列表" type="button">
            <RefreshCw size={18} />
          </button>
        </div>

        <form className="create-project" onSubmit={createAndOpen}>
          <input
            aria-label="新项目名称"
            placeholder="新小说项目"
            value={newProjectName}
            onChange={(event) => setNewProjectName(event.target.value)}
          />
          <button className="icon-button primary" disabled={busy} title="创建项目" type="submit">
            <Plus size={18} />
          </button>
        </form>

        <div className="project-list" aria-label="项目列表">
          {projects.length === 0 ? (
            <div className="empty-state">暂无项目</div>
          ) : (
            projects.map((project) => (
              <div
                key={project.id}
                className={project.id === activeProject?.id ? 'project-row active' : 'project-row'}
                onContextMenu={(event) => openProjectContextMenu(event, project)}
              >
                <button className="project-open-button" onClick={() => openProject(project)} type="button">
                  <BookOpen size={17} />
                  <span>
                    <strong>{project.name || project.id}</strong>
                    <small>{formatDate(project.last_accessed_at || project.created_at)}</small>
                  </span>
                </button>
                <button
                  className="project-more-button"
                  onClick={(event) => openProjectMoreMenu(event, project)}
                  title="项目操作"
                  type="button"
                >
                  <MoreHorizontal size={17} />
                </button>
              </div>
            ))
          )}
        </div>

        <section className="trash-panel">
          <button className="trash-toggle" disabled={busy} onClick={toggleTrash} type="button">
            <Trash2 size={16} />
            <span>回收站</span>
            <small>{trashOpen ? '收起' : '查看'}</small>
          </button>
          {trashOpen ? (
            <div className="trash-list">
              <div className="trash-items">
                {trashProjects.length === 0 ? (
                  <div className="empty-state">回收站为空</div>
                ) : (
                  trashProjects.map((project) => (
                    <div className="trash-row" key={project.id}>
                      <span>
                        <strong>{project.name || project.id}</strong>
                        <small>{formatDate(project.deleted_at || project.updated_at)}</small>
                      </span>
                      <button
                        className="icon-button"
                        disabled={busy}
                        onClick={() => restoreProjectFromTrash(project)}
                        title="恢复项目"
                        type="button"
                      >
                        <ListRestart size={16} />
                      </button>
                    </div>
                  ))
                )}
              </div>
              <button className="tool-button full-width" disabled={busy || trashProjects.length === 0} onClick={emptyProjectTrash} type="button">
                <Trash2 size={16} />
                清空回收站
              </button>
            </div>
          ) : null}
        </section>

        {projectMenu ? (
          <div
            className="project-menu"
            style={{ left: projectMenu.x, top: projectMenu.y }}
            onClick={(event) => event.stopPropagation()}
          >
            <button onClick={() => beginProjectRename(projectMenu.project)} type="button">
              <Pencil size={16} />
              <span>重命名项目</span>
            </button>
            <button onClick={() => beginProjectClone(projectMenu.project)} type="button">
              <Copy size={16} />
              <span>克隆项目</span>
            </button>
            <button className="danger" onClick={() => beginProjectDelete(projectMenu.project)} type="button">
              <Trash2 size={16} />
              <span>移入回收站</span>
            </button>
          </div>
        ) : null}
      </aside>

      {renameDialog ? (
        <div className="dialog-backdrop" onMouseDown={() => setRenameDialog(null)}>
          <form aria-labelledby="rename-dialog-title" aria-modal="true" className="compact-dialog" onMouseDown={(event) => event.stopPropagation()} onSubmit={submitProjectRename} role="dialog">
            <div className="dialog-title">
              <Pencil size={17} />
              <strong id="rename-dialog-title">重命名项目</strong>
            </div>
            <input
              autoFocus
              disabled={busy}
              value={renameDialog.name}
              onChange={(event) => setRenameDialog((previous) => ({ ...previous, name: event.target.value }))}
              onKeyDown={(event) => {
                if (event.key === 'Escape') {
                  event.preventDefault();
                  setRenameDialog(null);
                }
              }}
            />
            <div className="dialog-actions">
              <button className="tool-button" disabled={busy} onClick={() => setRenameDialog(null)} type="button">
                <X size={16} />
                取消
              </button>
              <button className="tool-button accent" disabled={busy || !String(renameDialog.name || '').trim()} type="submit">
                <Check size={16} />
                保存
              </button>
            </div>
          </form>
        </div>
      ) : null}

      {cloneDialog ? (
        <div className="dialog-backdrop" onMouseDown={() => setCloneDialog(null)}>
          <form aria-labelledby="clone-dialog-title" aria-modal="true" className="compact-dialog clone-dialog" onMouseDown={(event) => event.stopPropagation()} onSubmit={submitProjectClone} role="dialog">
            <div className="dialog-title">
              <Copy size={17} />
              <strong id="clone-dialog-title">克隆项目</strong>
            </div>
            <p className="dialog-copy">将完整复制当前项目的文档和配置。克隆项目与原项目完全独立，后续修改不会影响原项目。</p>
            <label className="clone-name-field">
              <span>项目名称</span>
              <input
                autoFocus
                disabled={busy}
                value={cloneDialog.name}
                onChange={(event) => setCloneDialog((previous) => ({ ...previous, name: event.target.value }))}
                onKeyDown={(event) => {
                  if (event.key === 'Escape') {
                    event.preventDefault();
                    setCloneDialog(null);
                  }
                }}
              />
            </label>
            <label className="clone-open-option">
              <input
                checked={cloneDialog.openAfterClone}
                disabled={busy}
                onChange={(event) => setCloneDialog((previous) => ({ ...previous, openAfterClone: event.target.checked }))}
                type="checkbox"
              />
              <span>克隆完成后打开新项目</span>
            </label>
            <div className="dialog-actions">
              <button className="tool-button" disabled={busy} onClick={() => setCloneDialog(null)} type="button">
                <X size={16} />
                取消
              </button>
              <button className="tool-button accent" disabled={busy || !String(cloneDialog.name || '').trim()} type="submit">
                <Copy size={16} />
                创建副本
              </button>
            </div>
          </form>
        </div>
      ) : null}

      {deleteDialog ? (
        <div className="dialog-backdrop" onMouseDown={() => setDeleteDialog(null)}>
          <div aria-labelledby="delete-dialog-title" aria-modal="true" className="compact-dialog" onMouseDown={(event) => event.stopPropagation()} role="dialog">
            <div className="dialog-title danger">
              <Trash2 size={17} />
              <strong id="delete-dialog-title">移入回收站？</strong>
            </div>
            <p className="dialog-copy">{deleteDialog.project.name || deleteDialog.project.id}</p>
            <div className="dialog-actions">
              <button className="tool-button" disabled={busy} onClick={() => setDeleteDialog(null)} type="button">
                <X size={16} />
                取消
              </button>
              <button className="tool-button danger-action" disabled={busy} onClick={confirmProjectDelete} type="button">
                <Trash2 size={16} />
                移入回收站
              </button>
            </div>
          </div>
        </div>
      ) : null}

      {rollbackDialog ? (
        <div className="dialog-backdrop" onMouseDown={() => setRollbackDialog(null)}>
          <div aria-labelledby="rollback-dialog-title" aria-modal="true" className="compact-dialog rollback-dialog" onMouseDown={(event) => event.stopPropagation()} role="dialog">
            <div className="dialog-title danger">
              <RotateCcw size={17} />
              <strong id="rollback-dialog-title">确认回退？</strong>
            </div>
            <p className="dialog-copy">
              将回退到：{textValue(rollbackDialog.preview, 'target_label', 'targetLabel', 'TargetLabel') || '上一阶段'}
            </p>
            <p className="dialog-copy danger-copy">
              {textValue(rollbackDialog.preview, 'warning', 'Warning') || '此操作不可撤销。'}
            </p>
            {arrayValue(rollbackDialog.preview, 'delete_paths', 'deletePaths', 'DeletePaths').length ? (
              <div className="rollback-paths">
                <strong>将删除</strong>
                <ul>
                  {arrayValue(rollbackDialog.preview, 'delete_paths', 'deletePaths', 'DeletePaths').map((path) => (
                    <li key={path}>{path}</li>
                  ))}
                </ul>
              </div>
            ) : null}
            {rollbackDialog.error ? <div className="error-banner compact">{rollbackDialog.error}</div> : null}
            <div className="dialog-actions">
              <button className="tool-button" disabled={busy} onClick={() => setRollbackDialog(null)} type="button">
                <X size={16} />
                取消
              </button>
              <button className="tool-button danger-action" disabled={busy} onClick={confirmRollback} type="button">
                <RotateCcw size={16} />
                确认回退
              </button>
            </div>
          </div>
        </div>
      ) : null}

      <main className="writing-pane">
        <header className="workspace-toolbar">
          <div className="workspace-heading">
            <div className="eyebrow">当前项目</div>
            <h2>{activeProject?.name || projectOpen.project?.name || ''}</h2>
          </div>
          <div className="toolbar-actions">
            <StatusPill status={connection} />
            <button
              aria-pressed={centerView === 'foundation'}
              className={`tool-button ${centerView === 'foundation' ? 'accent' : ''}`}
              disabled={!activeProject || projectOpen.status === 'loading'}
              onClick={openFoundationCenter}
              type="button"
            >
              <Database size={16} />
              设定中心
            </button>
            <button
              className="tool-button"
              disabled={!activeProject || projectBusy}
              onClick={() => openProject(activeProject)}
              type="button"
            >
              <ListRestart size={16} />
                保存快照
            </button>
            <button
              className="tool-button"
              disabled={!activeProject || !projectRunning}
              onClick={pauseWriting}
              type="button"
            >
              <PauseCircle size={16} />
              暂停
            </button>
            <button
              className="tool-button accent"
              disabled={!activeProject || projectBusy}
              onClick={resumeFromCurrentState}
              type="button"
            >
              <Play size={16} />
              恢复
            </button>
            <button
              className="tool-button danger-ghost"
              disabled={!activeProject || projectBusy}
              onClick={openRollbackDialog}
              type="button"
            >
              <RotateCcw size={16} />
              回退
            </button>
          </div>
        </header>

        {projectOpen.status === 'loading' ? (
          <div className="project-open-status" role="status" aria-live="polite">
            <RefreshCw aria-hidden="true" className="is-spinning" size={17} />
            <div>
              <strong>正在打开“{projectOpen.project?.name || projectOpen.project?.id}”</strong>
              <span>已切换到目标项目，正在加载详细状态。</span>
            </div>
          </div>
        ) : null}

        {showWorkflowProgress ? (
          <WorkflowProgressPanel
            projectId={activeProject?.id || ''}
            snapshot={snapshot}
          />
        ) : null}
        <ManuscriptWorkspace
          active={centerView === 'manuscript'}
          controlsTarget={manuscriptControlsTarget}
          key={activeProject?.id || 'no-project-manuscript'}
          projectId={activeProject?.id || ''}
          onReturnToWriting={() => {
            setCenterView('writing');
            if (sideView === 'manuscript') setSideView('status');
          }}
        />

        {centerView === 'foundation' ? <FoundationCenter
          key={activeProject?.id || 'no-project-foundation'}
          projectId={activeProject?.id || ''}
          requestedNavigation={foundationNavigation}
          onNavigationHandled={(requestId) => setFoundationNavigation((current) =>
            current.requestId === requestId
              ? { projectId: '', tab: '', anchor: '', requestId: 0 }
              : current)}
          onDirtyChange={setFoundationDraftDirty}
          onClose={() => setCenterView('writing')}
          onOpenReview={() => openFoundationReviewTab('overview')}
        /> : null}
        {pendingFoundationProject ? <FoundationProjectSwitchDialog
          project={pendingFoundationProject.project}
          onCancel={() => {
            const trigger = pendingFoundationProject.trigger;
            setPendingFoundationProject(null);
            globalThis.requestAnimationFrame?.(() => trigger?.focus());
          }}
          onConfirm={() => {
            const project = pendingFoundationProject.project;
            forceFoundationProjectOpenRef.current = project.id;
            setPendingFoundationProject(null);
            setFoundationDraftDirty(false);
            openProject(project);
          }}
        /> : null}

        {error ? <div className="error-banner">{error}</div> : null}

        {centerView === 'writing' ? <><div className="workbench-stack">
          <section
            className={`stream-area ${showContinuationWorkspace || showAdaptationProposalWorkspace || showCoCreatePlanningWorkspace || showOutlineRevisionWorkspace ? 'proposal-workspace-output' : showCoCreateWorkspace || showContinuationCoCreateWorkspace ? 'cocreate-workspace-output' : showChapterRevisionWorkspace ? 'chapter-revision-workspace-output' : ''}`}
            aria-label={showContinuationCoCreateWorkspace ? '续写 Draft 共创区' : showContinuationWorkspace ? '小说续写审稿区' : showAdaptationProposalWorkspace ? '改编提案审稿区' : showCoCreatePlanningWorkspace ? 'Foundation 与规划审核区' : showOutlineRevisionWorkspace ? '章节细纲预览区' : showChapterRevisionWorkspace ? '单章返工预览区' : '实时创作流'}
          >
            {activeProject ? (
              showContinuationCoCreateWorkspace ? (
                <CoCreateWorkspace coCreate={coCreate} />
              ) : showContinuationWorkspace ? (
                <ContinuationWorkspace continuation={continuationSnapshot} />
              ) : showAdaptationProposalWorkspace ? (
                <AdaptationProposalWorkspace proposal={adaptationProposalReview} />
              ) : showCoCreatePlanningWorkspace ? (
                 <CoCreatePlanningWorkspace
                   busy={projectBusy}
                   onConfirm={confirmCoCreatePlanningRun}
                   onConfirmCharacterCandidate={confirmReviewedCharacterCandidate}
                   onOpenCharacterConfirmation={openFoundationCharacterConfirmation}
                   onOpenCharacterRevision={openFoundationCharacterRevision}
                   onManualRevise={reviseCoCreatePlanningRun}
                   onRevise={reviseCoCreatePlanningRun}
                   onReviseCharacterCandidate={reviseReviewedCharacterCandidate}
                   planningRevision={planningRevision}
                   review={coCreatePlanningReview}
                   selectedFoundationTab={foundationReviewTab}
                   setFoundationReviewTab={setFoundationReviewTab}
                   setPlanningRevision={setPlanningRevision}
                 />
              ) : showCoCreateWorkspace ? (
                <CoCreateWorkspace
                  busy={projectBusy}
                  coCreate={coCreate}
                  onCoreCastSave={updateCoreCastFlow}
                  onCoreCastConfirm={confirmCoreCastFlow}
                  onCoreCastUnconfirm={unconfirmCoreCastFlow}
                />
              ) : showOutlineRevisionWorkspace ? (
                <OutlineChapterRevisionWorkspace selected={selectedOutlineRevisionView} />
              ) : showChapterRevisionWorkspace ? (
                <CompletedChapterRevisionWorkspace selected={selectedChapterRevisionView} content={chapterContent} />
              ) : (
              visibleStreamRounds(workbench.streamRounds).map((round) => (
                <article className="stream-round" key={round.id}>
                  {round.text ? <pre>{round.text}</pre> : <span className="muted">等待流式输出</span>}
                </article>
              ))
              )
            ) : projectOpen.status === 'loading' ? (
              <div className="no-project project-loading-state">
                <RefreshCw aria-hidden="true" className="is-spinning" size={28} />
                <strong>正在载入项目</strong>
                <span>加载完成后会自动进入详细界面，无需切换其他项目。</span>
              </div>
            ) : (
              <div className="no-project">
                <SquarePen size={28} />
                <strong>打开或创建一本小说</strong>
                <span>从左侧选择项目，或创建一本新小说开始工作。</span>
              </div>
            )}
          </section>

          <details className="event-feed" open={eventsOpen} onToggle={(event) => setEventsOpen(event.currentTarget.open)}>
            <summary className="section-title">
              <Activity size={17} />
              <span>事件</span>
              <small>{sortedEvents.length}</small>
            </summary>
            <div className="event-list">
              {sortedEvents.length === 0 ? (
                <div className="empty-state">暂无事件</div>
              ) : (
                sortedEvents.map((event) => (
                  <div className={`event-row ${eventStatus(event)}`} key={event.host_event_id || event.seq}>
                    <span className="event-dot" />
                    <span className="event-time">{formatTime(event.time)}</span>
                    <strong>{event.event?.category || 'EVENT'}</strong>
                    <span>{eventFeedSummary(event)}</span>
                  </div>
                ))
              )}
            </div>
          </details>
        </div>

        <form className="composer" onSubmit={submitContinue}>
          <input
            aria-label={quickStartAvailable ? '快速启动输入' : '继续创作输入'}
            disabled={!activeProject || projectBusy || projectRunning}
            placeholder={quickStartAvailable ? '写下新书核心想法，直接启动...' : '继续、补充或要求下一步...'}
            value={composerText}
            onChange={(event) => setComposerText(event.target.value)}
          />
          <button className={`tool-button ${projectRunning ? '' : 'accent'}`} disabled={!activeProject || (busy && !projectRunning)} type="submit">
            {projectRunning ? <PauseCircle size={16} /> : quickStartAvailable ? <Play size={16} /> : <Send size={16} />}
            {projectRunning ? '暂停' : quickStartAvailable ? '启动' : '继续'}
          </button>
        </form></> : null}
      </main>

      <aside
        className={`status-pane ${sideView === 'manuscript' ? 'manuscript-active' : ''} ${toolDrawerOpen ? 'mobile-open' : ''}`}
        data-mobile-view={mobileToolView}
        aria-label="创作与高级工具"
      >
        <MobileToolMenu coCreateVisible={globalCoCreateVisible} onClose={closeMobileDrawers} onSelectTool={openMobileToolDetail} />
        <div className="side-tabs" role="tablist" aria-label="工作台工具">
          <button aria-selected={sideView === 'status'} className={sideView === 'status' ? 'active' : ''} onClick={() => openTool('status')} role="tab" title="状态" type="button">
            <CircleDot size={16} />
            状态
          </button>
          <button aria-selected={sideView === 'manuscript'} className={sideView === 'manuscript' ? 'active' : ''} onClick={() => openTool('manuscript')} role="tab" title="专业稿件" type="button">
            <BookOpen size={16} />
            稿件
          </button>
          {globalCoCreateVisible ? <button aria-selected={sideView === 'cocreate'} className={sideView === 'cocreate' ? 'active' : ''} onClick={() => openTool('cocreate')} role="tab" title="共创" type="button">
            <MessageSquareText size={16} />
            共创
          </button> : null}
          <button aria-selected={sideView === 'simulate'} className={sideView === 'simulate' ? 'active' : ''} onClick={() => openTool('simulate')} role="tab" title="画像" type="button">
            <WandSparkles size={16} />
            画像
          </button>
          <button aria-selected={sideView === 'settings'} className={sideView === 'settings' ? 'active' : ''} onClick={() => { setSideView('settings'); setToolDrawerOpen(true); }} role="tab" title="设定" type="button">
            <SlidersHorizontal size={16} />
            设定
          </button>
          <button aria-selected={sideView === 'schedule'} className={sideView === 'schedule' ? 'active' : ''} onClick={() => openTool('schedule')} role="tab" title="定时" type="button">
            <Clock3 size={16} />
            定时
          </button>
          <button aria-selected={sideView === 'adapt'} className={sideView === 'adapt' ? 'active' : ''} onClick={() => openTool('adapt')} role="tab" title="改编" type="button">
            <FileText size={16} />
            改编
          </button>
          <button aria-selected={sideView === 'audit'} className={sideView === 'audit' ? 'active' : ''} onClick={() => openTool('audit')} role="tab" title="改编审计" type="button">
            <TestTube2 size={16} />
            审计
          </button>
          <button aria-selected={sideView === 'continuation'} className={sideView === 'continuation' ? 'active' : ''} onClick={() => openTool('continuation')} role="tab" title="续写" type="button">
            <Upload size={16} />
            续写
          </button>
          <button aria-selected={sideView === 'export'} className={sideView === 'export' ? 'active' : ''} onClick={() => openTool('export')} role="tab" title="导出" type="button">
            <Download size={16} />
            导出
          </button>
          <button aria-selected={sideView === 'diag'} className={sideView === 'diag' ? 'active' : ''} onClick={() => openTool('diag')} role="tab" title="诊断" type="button">
            <Activity size={16} />
            诊断
          </button>
          <button aria-selected={sideView === 'cache'} className={sideView === 'cache' ? 'active' : ''} onClick={() => openTool('cache')} role="tab" title="缓存" type="button">
            <Database size={16} />
            缓存
          </button>
          <button aria-selected={sideView === 'backend'} className={sideView === 'backend' ? 'active' : ''} onClick={() => openTool('backend')} role="tab" title="后端" type="button">
            <Server size={16} />
            后端
          </button>
          <button aria-selected={sideView === 'models'} className={sideView === 'models' ? 'active' : ''} onClick={() => openTool('models')} role="tab" title="模型" type="button">
            <Settings size={16} />
            模型
          </button>
        </div>

        <MobileToolDetailHeader
          title={mobileToolLabel(sideView)}
          onBack={() => setMobileToolView('menu')}
          onClose={closeMobileDrawers}
        />
        <div className="side-panel" role="tabpanel">
          <button className="mobile-drawer-close tablet-drawer-close" onClick={() => setToolDrawerOpen(false)} type="button">
            <X size={16} /> 关闭工具
          </button>
          {sideView === 'manuscript' ? (
            <div ref={setManuscriptControlsTarget} className="manuscript-controls-host" />
          ) : sideView === 'status' ? (
            <StatusPanel
              snapshot={snapshot}
              activeProject={activeProject}
              chapterRevision={chapterRevision}
              outlineRevision={outlineRevision}
              setChapterRevision={setChapterRevision}
              setOutlineRevision={setOutlineRevision}
              onPause={pauseWriting}
              onRefreshOutline={refreshOutlineRevision}
              onReviseOutline={submitOutlineRevision}
              onReviseChapter={reviseCompletedChapter}
              onSteer={submitSteer}
              steerText={steerText}
              setSteerText={setSteerText}
              busy={projectBusy}
            />
          ) : sideView === 'cocreate' ? (
            <CoCreatePanel
              activeProject={activeProject}
              busy={projectBusy}
              coCreate={coCreate}
              planningRevision={planningRevision}
              planningReview={coCreatePlanningReview}
              setCoCreate={setCoCreate}
              setPlanningRevision={setPlanningRevision}
              adaptation={adaptation}
              onBegin={beginCoCreateFlow}
              onConfirmIntake={() => beginCoCreateFlow('normal', { confirmIntake: true })}
              onSubmit={submitCoCreate}
              onRebrief={(event) => submitCoCreate(event, { forceRebrief: true })}
              onSuggestion={submitCoCreateSuggestion}
              onRevise={reviseCoCreateMessage}
              onResolveDecision={resolveCoCreateDecisionFlow}
              onCommit={commitCoCreateFlow}
              onConfirmPlanning={confirmCoCreatePlanningRun}
              onOpenFoundationReview={openFoundationReviewTab}
              onRevisePlanning={reviseCoCreatePlanningRun}
              onCancel={cancelCoCreateFlow}
              workspaceTranscript={showCoCreateWorkspace}
            />
          ) : sideView === 'simulate' ? (
            <SimulationPanel
              activeProject={activeProject}
              busy={projectBusy}
              snapshot={snapshot}
              simulation={simulation}
              setSimulation={setSimulation}
              onUploadSources={uploadSimulationSources}
              onSearchSources={searchSimulationCorpus}
              onDownloadSource={downloadSimulationCorpus}
              onAnalyze={runSimulationAnalysis}
              onImportProfile={importSimulation}
              onRefreshLibrary={refreshSimulationLibrary}
              onUploadLibrary={uploadSimulationProfilesToLibrary}
              onLoadLibrary={loadSimulationProfileFromLibrary}
              onSaveToLibrary={saveSimulationProfileToLibrary}
            />
          ) : sideView === 'settings' ? (
            <ProjectSettingsPanel
              activeProject={activeProject}
              busy={projectBusy}
              coCreateActive={coCreate.active === true}
              modelConfig={modelConfig}
              currentSimulationMode={currentProjectSimulationMode}
              currentStyle={currentProjectStyle}
              globalBusy={busy}
              projectSettings={projectSettings}
              setProjectSettings={setProjectSettings}
              snapshot={snapshot}
              onSaveSimulationMode={saveProjectSimulationMode}
              onSaveStyle={saveProjectStyle}
              onToggleScheduledResume={changeProjectResumeSchedule}
              onRefreshStyles={loadProjectStyles}
            />
          ) : sideView === 'schedule' ? (
            <ResumeSchedulePanel
              schedule={resumeSchedule}
              setSchedule={setResumeScheduleState}
              onRefresh={loadResumeSchedule}
              onSave={saveResumeSchedule}
            />
          ) : sideView === 'adapt' ? (
            <AdaptationPanel
              activeProject={activeProject}
              busy={projectBusy}
              snapshot={snapshot}
              adaptation={adaptation}
              simulation={simulation}
              setAdaptation={setAdaptation}
              onUploadSource={uploadAdaptation}
              onAnalyze={runAdaptationAnalysis}
              onStart={startAdaptationRun}
              onRevise={reviseAdaptationProposalRun}
              onConfirm={confirmAdaptationRun}
              onRefreshLibrary={refreshNovelLibrary}
              onLoadLibrary={loadNovelFromLibraryEntry}
              onSaveNovel={saveAnalyzedNovelToLibrary}
              onCoCreate={() => {
                runWithWindowScrollPreserved(() => {
                  setSideView('cocreate');
                  return beginCoCreateFlow('adapt', { initial: adaptation.brief });
                });
              }}
            />
          ) : sideView === 'audit' ? (
            <AdaptationAuditPanel
              activeProject={activeProject}
              audit={adaptationAudit}
              busy={projectBusy}
              coCreateActive={coCreate.active === true}
              modelConfig={modelConfig}
              projectRunning={projectRunning}
              setAudit={setAdaptationAudit}
              onApply={applyAdaptationAuditRepair}
              onCancelSemantic={cancelSemanticAudit}
              onCompare={compareAdaptationAudits}
              onEstimateSemantic={estimateSemanticAudit}
              onReload={() => loadAdaptationAuditReport(activeProject?.id)}
              onRetrySemantic={retrySemanticAudit}
              onRun={runAdaptationAuditReport}
              onSelectRun={selectAdaptationAuditRun}
              onStartSemantic={startSemanticAudit}
            />
          ) : sideView === 'continuation' ? (
            <ContinuationPanel
              activeProject={activeProject}
              busy={projectBusy}
              continuation={continuation}
              workflow={continuationSnapshot}
              setContinuation={setContinuation}
              onUpload={uploadContinuationNovel}
              onBeginDraft={beginContinuationDraft}
              onGenerateProposal={generateContinuationProposalRun}
              onRevise={reviseContinuationReview}
              onApprove={approveContinuationReview}
              onStart={startContinuationRun}
              coCreate={coCreate}
              planningRevision={planningRevision}
              planningReview={coCreatePlanningReview}
              setCoCreate={setCoCreate}
              setPlanningRevision={setPlanningRevision}
              adaptation={adaptation}
              onCoCreateBegin={beginCoCreateFlow}
              onCoCreateSubmit={submitCoCreate}
              onCoCreateSuggestion={submitCoCreateSuggestion}
              onCoCreateRevise={reviseCoCreateMessage}
              onCoCreateResolveDecision={resolveCoCreateDecisionFlow}
              onCoCreateCommit={commitCoCreateFlow}
              onCoCreateCancel={cancelCoCreateFlow}
            />
          ) : sideView === 'export' ? (
            <ExportPanel
              activeProject={activeProject}
              busy={isExportActionBusy(exportJob)}
              exportJob={exportJob}
              setExportJob={setExportJob}
              onExport={runExport}
            />
          ) : sideView === 'diag' ? (
            <DiagnosticPanel
              activeProject={activeProject}
              busy={projectBusy}
              diagnostic={diagnostic}
              onRun={runDiagnostic}
            />
          ) : sideView === 'cache' ? (
            <CachePanel analytics={usageAnalytics} snapshot={snapshot} />
          ) : sideView === 'backend' ? (
            <BackendPanel backend={backendStatus} busy={projectBusy} onRefresh={refreshBackendStatus} onTest={runBackendTest} />
          ) : (
            <ModelPanel
              activeProject={activeProject}
              runtime={runtime}
              modelConfig={modelConfig}
              customModel={customModel}
              setCustomModel={setCustomModel}
              busy={busy}
              onSwitchDefault={switchDefaultModel}
              onSwitch={switchModelRoute}
              onInherit={inheritModelRoute}
              onThinking={changeThinking}
              onCoCreateTimeout={changeCoCreateTimeout}
              onCoCreateMaxTokens={changeCoCreateMaxTokens}
              onRetrySettings={changeRetrySettings}
              onDeleteModel={deleteModelRoute}
              onAddCustom={submitCustomModel}
              onTestConnection={discoverCustomModelModels}
              onTestModel={testCustomModelConnection}
              onStartGrokLogin={startGrokOAuthLogin}
              onPollGrokLogin={pollGrokOAuthLogin}
              onCompleteGrokLogin={completeGrokOAuthLogin}
              onRefreshGrokStatus={refreshGrokOAuthStatus}
              onRefreshCodexStatus={refreshCodexAuthStatus}
              onUploadCodexAuth={uploadCodexCredential}
            />
          )}
        </div>
      </aside>
    </div>
  );
}

function hasCoCreateWorkspaceContent(coCreate) {
  return Boolean(
    coCreate?.streamThinking ||
      coCreate?.streamReply ||
      coCreate?.coreCast ||
      (Array.isArray(coCreate?.messages) && coCreate.messages.some((message) => message?.role !== 'system' && message?.content))
  );
}

function CoCreatePlanningWorkspace({
  busy,
  onConfirm,
  onConfirmCharacterCandidate,
  onOpenCharacterConfirmation,
  onOpenCharacterRevision,
  onManualRevise,
  onRevise,
  onReviseCharacterCandidate,
  planningRevision,
  review,
  selectedFoundationTab,
  setFoundationReviewTab,
  setPlanningRevision
}) {
  if (review.kind === 'foundation') {
    return (
      <FoundationReviewWorkspace
        busy={busy}
        onConfirm={onConfirm}
        onConfirmCharacterCandidate={onConfirmCharacterCandidate}
        onOpenCharacterConfirmation={onOpenCharacterConfirmation}
        onOpenCharacterRevision={onOpenCharacterRevision}
        onManualRevise={onManualRevise}
        onRevise={onRevise}
        onReviseCharacterCandidate={onReviseCharacterCandidate}
        planningRevision={planningRevision}
        review={review}
        selectedTab={selectedFoundationTab}
        onTabChange={setFoundationReviewTab}
        setPlanningRevision={setPlanningRevision}
      />
    );
  }
  const groups = coCreatePlanningWorkspaceGroups(review, planningRevision);
  const showBriefOnly = review.kind === 'blueprint' || groups.length === 0;
  return (
    <div className="proposal-workspace cocreate-planning-workspace">
      <header className="proposal-workspace-header">
        <div>
          <div className="eyebrow">普通共创审核</div>
          <h3>{coCreatePlanningKindLabel(review.kind)}</h3>
        </div>
        <div className="proposal-workspace-metrics">
          <span>{review.status || 'pending'}</span>
          {review.chapterCount ? <span>{review.chapterCount} 章</span> : null}
          {review.targetTotalWords ? <span>{formatCompact(review.targetTotalWords)} 字</span> : null}
          {review.volumes?.length ? <span>{review.volumes.length} 卷</span> : null}
        </div>
      </header>
      {showBriefOnly && review.brief ? <p className="proposal-brief">{review.brief}</p> : null}
      <div className="proposal-volume-stack">
        {groups.map((group) => (
          <section className="proposal-volume-block" key={`cocreate-planning-${group.key}`}>
            <div className="proposal-volume-head">
              <div>
                <strong>{group.title}</strong>
                <span>第 {group.from || '?'}-{group.to || '?'} 章</span>
              </div>
              {group.theme ? <p>{group.theme}</p> : null}
              {group.summary ? <p>{group.summary}</p> : null}
            </div>
            <div className="proposal-chapter-grid">
              {group.chapters.map((chapter) => (
                <ProposalChapterCard chapter={chapter} key={`cocreate-planning-card-${chapter.chapter}-${chapter.title}`} />
              ))}
            </div>
          </section>
        ))}
      </div>
    </div>
  );
}

function coCreatePlanningWorkspaceGroups(review = {}, revision = {}) {
  const groups = proposalVolumeGroups({
    chapters: review.chapters || [],
    volumes: review.volumes || []
  });
  const kind = String(review?.kind || '').trim();
  if (kind === 'volume_split') {
    const selected = coCreatePlanningRevisionVolumeSelection(revision?.volumeIndex, review.volumes || []);
    const group = groups.find((item) => String(item.key) === selected);
    return group ? [group] : groups.slice(0, 1);
  }
  if (kind !== 'chapter_outline') {
    return groups;
  }
  const hasVolumes = Array.isArray(review.volumes) && review.volumes.length > 0;
  const scope = coCreatePlanningRevisionScope(revision?.scope, kind, hasVolumes);
  if (scope !== 'chapter') {
    return groups;
  }
  const selected = Number.parseInt(coCreatePlanningRevisionChapterSelection(
    revision?.chapter || revision?.fromChapter,
    coCreatePlanningRevisionChapterOptions(review)
  ), 10);
  if (!Number.isInteger(selected) || selected <= 0) {
    return groups;
  }
  const group = groups.find((item) => item.chapters.some((chapter) => chapter.chapter === selected));
  if (!group) {
    const chapter = coCreatePlanningRevisionChapterOptions(review).find((item) => item.chapter === selected);
    return chapter ? [{
      key: `chapter-${selected}`,
      title: '选中章节',
      from: selected,
      to: selected,
      theme: '',
      summary: '',
      chapters: [chapter]
    }] : groups;
  }
  return [{
    ...group,
    key: `${group.key}-chapter-${selected}`,
    from: selected,
    to: selected,
    chapters: group.chapters.filter((chapter) => chapter.chapter === selected)
  }];
}

function coCreatePlanningConfirmAction(review = {}) {
  switch (String(review?.kind || '').trim()) {
    case 'blueprint':
      return { visible: true, label: '生成详细提案' };
    case 'volume_split':
      return { visible: true, label: '审核通过并生成章节细纲' };
    default:
      return { visible: true, label: '审核通过并启动创作' };
  }
}

function coCreatePlanningRunningCopy(review = {}) {
  switch (String(review?.kind || '').trim()) {
    case 'blueprint':
      return 'AI 正在生成分卷提案或短篇章节细纲。';
	case 'volume_split':
		return 'AI 正在按已审核分卷逐弧生成（每批最多 4 章），并依次进行弧审、卷审和分批全书总审；未通过会自动定点返修。';
    default:
      return 'AI 正在根据审核意见重新生成规划。';
  }
}

function coCreatePlanningRevisionScope(scope, kind, hasVolumes) {
  if (kind === 'volume_split') {
    return 'volume';
  }
  if (kind === 'chapter_outline' && hasVolumes) {
    return 'chapter';
  }
  const normalized = String(scope || '').trim();
  return normalized === 'chapter' ? 'chapter' : 'all';
}

function coCreatePlanningRevisionVolumeSelection(value, volumes = []) {
  if (!volumes.length) {
    return '';
  }
  const selected = Number.parseInt(String(value || ''), 10);
  const volume = volumes.find((item) => item.index === selected) || volumes[0];
  return String(volume.index);
}

function coCreatePlanningRevisionChapterSelection(value, chapters = []) {
  if (!chapters.length) {
    return '';
  }
  const selected = Number.parseInt(String(value || ''), 10);
  const chapter = chapters.find((item) => item.chapter === selected) || chapters[0];
  return String(chapter.chapter);
}

function coCreatePlanningRevisionChapterOptions(review = {}) {
  const chapters = Array.isArray(review?.chapters) ? review.chapters : [];
  if (chapters.length) {
    return chapters.map((chapter, index) => ({
      chapter: numberValue(chapter, 'Chapter', 'chapter') || index + 1,
      title: textValue(chapter, 'Title', 'title')
    }));
  }
  const chapterCount = numberValue(review, 'chapterCount', 'ChapterCount', 'chapter_count');
  return Array.from({ length: Math.max(0, chapterCount) }, (_, index) => ({
    chapter: index + 1,
    title: ''
  }));
}

function coCreatePlanningRevisionVolumeLabel(volume = {}) {
  const index = numberValue(volume, 'Index', 'index');
  const title = textValue(volume, 'Title', 'title');
  return title ? `第 ${index} 卷：${title}` : `第 ${index} 卷`;
}

function coCreatePlanningRevisionChapterLabel(chapter = {}) {
  const number = numberValue(chapter, 'Chapter', 'chapter');
  const title = textValue(chapter, 'Title', 'title');
  return title ? `第 ${number} 章：${title}` : `第 ${number} 章`;
}

function PlanningRevisionControls({ busy, compact = false, disabled = false, onRevise, review = {}, revision, setRevision }) {
  const instruction = String(revision?.instruction || revision?.feedback || '');
  const volumes = Array.isArray(review?.volumes) ? review.volumes : [];
  const chapterOptions = coCreatePlanningRevisionChapterOptions(review);
  const kind = String(review?.kind || '').trim();
  const hasVolumes = volumes.length > 0;
  const scope = coCreatePlanningRevisionScope(revision?.scope, kind, hasVolumes);
  const selectedVolume = coCreatePlanningRevisionVolumeSelection(revision?.volumeIndex, volumes);
  const selectedChapter = coCreatePlanningRevisionChapterSelection(revision?.chapter, chapterOptions);
  const canSubmit = !busy && !disabled && Boolean(instruction.trim());
  const updateRevision = (changes) => {
    setRevision((previous) => ({
      ...previous,
      ...changes,
      status: 'idle',
      message: '',
      error: ''
    }));
  };
  const updateInstruction = (event) => {
    const nextInstruction = event.target.value;
    updateRevision({
      feedback: nextInstruction,
      instruction: nextInstruction
    });
  };
  const showVolumeTarget = kind === 'volume_split' && volumes.length > 0;
  const showScopeTarget = kind === 'chapter_outline' && !hasVolumes && chapterOptions.length > 0;
  const showChapterTarget = kind === 'chapter_outline' && chapterOptions.length > 0 && (hasVolumes || scope === 'chapter');
  return (
    <div className={`planning-revision-controls ${compact ? 'compact' : ''}`}>
      {showVolumeTarget ? (
        <label className="field-label proposal-select-line">
          <span>修改范围</span>
          <select
            disabled={busy || disabled}
            value={selectedVolume}
            onChange={(event) => updateRevision({ scope: 'volume', volumeIndex: event.target.value })}
          >
            {volumes.map((volume) => (
              <option key={`planning-revision-volume-${volume.index}`} value={volume.index}>
                {coCreatePlanningRevisionVolumeLabel(volume)}
              </option>
            ))}
          </select>
        </label>
      ) : null}
      {showScopeTarget ? (
        <div className="proposal-revision-mode-grid planning-revision-target-grid" role="radiogroup" aria-label="规划修改范围">
          {[
            { value: 'all', label: '全卷' },
            { value: 'chapter', label: '单章' }
          ].map((item) => (
            <button
              className={scope === item.value ? 'revision-mode-button active' : 'revision-mode-button'}
              disabled={busy || disabled}
              key={item.value}
              onClick={() => updateRevision({ scope: item.value })}
              type="button"
            >
              {item.label}
            </button>
          ))}
        </div>
      ) : null}
      {showChapterTarget ? (
        <label className="field-label proposal-select-line">
          <span>章节</span>
          <select
            disabled={busy || disabled}
            value={selectedChapter}
            onChange={(event) => updateRevision({
              chapter: event.target.value,
              fromChapter: event.target.value,
              scope: 'chapter',
              toChapter: event.target.value
            })}
          >
            {chapterOptions.map((chapter) => (
              <option key={`planning-revision-chapter-${chapter.chapter}`} value={chapter.chapter}>
                {coCreatePlanningRevisionChapterLabel(chapter)}
              </option>
            ))}
          </select>
        </label>
      ) : null}
      <label className="field-label proposal-select-line">
        <span>审核意见</span>
        <textarea
          className="proposal-revision-textarea"
          disabled={busy || disabled}
          placeholder="写下希望 AI 修改的设定、结构、节奏、分卷或章节意见..."
          value={instruction}
          onChange={updateInstruction}
        />
      </label>
      <button className="tool-button full-width" disabled={!canSubmit} onClick={() => runWithWindowScrollPreserved(onRevise)} type="button">
        <WandSparkles size={16} />
        提交意见并让 AI 修改
      </button>
    </div>
  );
}

function CoCreateWorkspace({ coCreate, busy = false, onCoreCastSave, onCoreCastConfirm, onCoreCastUnconfirm }) {
  const threadRef = useRef(null);
  const bottomRef = useRef(null);
  const messages = Array.isArray(coCreate.messages)
    ? coCreate.messages.filter((message) => message?.role !== 'system' && message?.content)
    : [];
  const scrollSignature = [
    messages.length,
    messages.map((message) => String(message.content || '').length).join(','),
    String(coCreate.streamThinking || '').length,
    String(coCreate.streamReply || '').length
  ].join(':');

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: 'end' });
  }, [scrollSignature]);

  return (
    <div className="cocreate-workspace-thread" ref={threadRef}>
      {messages.length === 0 && !coCreate.streamThinking && !coCreate.streamReply ? (
        <article className="stream-round">
          <span className="muted">等待共创输出</span>
        </article>
      ) : null}
      {messages.map((message, index) => (
        <article className={`stream-round cocreate-workspace-message ${message.role}`} key={message.id || `${message.role}-${index}`}>
          <div className="cocreate-workspace-head">
            <strong>{coCreateRoleLabel(message.role)}</strong>
            {message.source ? <span>{message.source === 'suggestion' ? '选项' : '补充'}</span> : null}
          </div>
          <pre>{message.content}</pre>
        </article>
      ))}
      {coCreate.streamThinking ? (
        <article className="stream-round cocreate-workspace-message thinking">
          <div className="cocreate-workspace-head">
            <strong>Thinking</strong>
          </div>
          <pre>{coCreate.streamThinking}</pre>
        </article>
      ) : null}
      {coCreate.streamReply ? (
        <article className="stream-round cocreate-workspace-message assistant">
          <div className="cocreate-workspace-head">
            <strong>AI</strong>
          </div>
          <pre>{coCreate.streamReply}</pre>
        </article>
      ) : null}
      {coCreate.draftPrompt ? (
        <section className="cocreate-workspace-draft" aria-labelledby="cocreate-workspace-draft-title">
          <div>
            <span className="eyebrow">AI 整理结果</span>
            <h2 id="cocreate-workspace-draft-title">当前创作方案</h2>
            <p>这是即将交给写作流程的方案摘要。继续在右侧补充意见，AI 会更新这里的内容。</p>
          </div>
          <pre>{coCreate.draftPrompt}</pre>
        </section>
      ) : null}
      {coCreate.coreCast && (coCreate.kind === 'normal' || coCreate.kind === 'adapt') ? (
        <CoreCastEditor
          mode={coCreate.kind}
          value={coCreate.coreCast}
          completion={coCreate.castCompletion}
          confirmed={coCreate.castConfirmed}
          sourceMajorCharacters={coCreate.sourceMajorCharacters}
          busy={busy}
          onSave={onCoreCastSave}
          onConfirm={onCoreCastConfirm}
          onUnconfirm={onCoreCastUnconfirm}
        />
      ) : null}
      <div className="cocreate-workspace-bottom" aria-hidden="true" ref={bottomRef} />
    </div>
  );
}

function SetupWizard({ setup, setSetup, onProviderChange, onTest, onComplete }) {
  const selected = setup.providers.find((item) => item.id === setup.provider) || {};
  const canTest = Boolean(setup.provider && setup.model.trim() && (selected.api_key_optional || setup.apiKey.trim()));
  return (
    <main className="setup-shell">
      <section className="setup-card" aria-labelledby="setup-title">
        <div className="setup-brand">
          <div className="brand-mark"><BookOpen size={22} /></div>
          <div>
            <span className="eyebrow">ainovel Web-only</span>
            <h1 id="setup-title">连接你的创作模型</h1>
          </div>
        </div>
        <ol className="setup-steps" aria-label="首次设置进度">
          <li className="complete">选择服务商</li>
          <li className={setup.model ? 'complete' : 'active'}>填写模型</li>
          <li className={setup.testStatus === 'passed' ? 'complete' : 'active'}>测试并保存</li>
        </ol>
        <label className="model-field">
          <span>模型服务商</span>
          <select value={setup.provider} onChange={(event) => onProviderChange(event.target.value)}>
            {setup.providers.map((provider) => <option key={provider.id} value={provider.id}>{provider.label}</option>)}
          </select>
        </label>
        {setup.provider === 'custom' ? (
          <label className="model-field">
            <span>API 协议</span>
            <select value={setup.type} onChange={(event) => setSetup((previous) => ({ ...previous, type: event.target.value, testStatus: 'idle' }))}>
              <option value="openai">OpenAI 兼容</option>
              <option value="anthropic">Anthropic 兼容</option>
              <option value="gemini">Gemini 兼容</option>
            </select>
          </label>
        ) : null}
        <label className="model-field">
          <span>模型名称</span>
          <input autoFocus placeholder="例如 gpt-5 / claude-sonnet / deepseek-chat" value={setup.model} onChange={(event) => setSetup((previous) => ({ ...previous, model: event.target.value, testStatus: 'idle' }))} />
        </label>
        <label className="model-field">
          <span>API Key {selected.api_key_optional ? '（可留空）' : ''}</span>
          <input autoComplete="off" placeholder="仅保存在本机配置中" type="password" value={setup.apiKey} onChange={(event) => setSetup((previous) => ({ ...previous, apiKey: event.target.value, testStatus: 'idle' }))} />
        </label>
        <label className="model-field">
          <span>Base URL {selected.base_url ? '（已填默认值）' : '（可选）'}</span>
          <input placeholder="使用官方地址时可留空" value={setup.baseURL} onChange={(event) => setSetup((previous) => ({ ...previous, baseURL: event.target.value, testStatus: 'idle' }))} />
        </label>
        {setup.error ? <div className="error-banner" role="alert">{setup.error}</div> : null}
        {setup.message ? <div className="success-note" role="status">{setup.message}</div> : null}
        <div className="setup-actions">
          <button className="tool-button" disabled={!canTest || setup.testStatus === 'running' || setup.testStatus === 'saving'} onClick={onTest} type="button">
            <TestTube2 size={16} /> {setup.testStatus === 'running' ? '测试中…' : '测试连接'}
          </button>
          <button className="tool-button accent" disabled={setup.testStatus !== 'passed'} onClick={onComplete} type="button">
            <Check size={16} /> 保存并进入工作台
          </button>
        </div>
        <p className="setup-privacy">密钥不会出现在接口响应、用量账本或诊断日志中。</p>
      </section>
    </main>
  );
}

function OutlineChapterRevisionWorkspace({ selected }) {
  const chapter = selected?.chapter || 0;
  const outlineRow = selected?.outlineRow || null;
  if (!outlineRow) {
    return (
      <div className="proposal-workspace">
        <article className="stream-round chapter-revision-empty">
          <strong>暂无章节细纲</strong>
          <span className="muted">请刷新项目快照后重试。</span>
        </article>
      </div>
    );
  }
  return (
    <div className="proposal-workspace outline-revision-workspace">
      <header className="proposal-workspace-header">
        <div>
          <div className="eyebrow">章节细纲修改</div>
          <h3>{`第 ${chapter} 章：${outlineRow.title}`}</h3>
        </div>
        <div className="proposal-workspace-metrics">
          {outlineRow.writtenWordCount > 0 ? <span>已写 {formatCompact(outlineRow.writtenWordCount)} 字</span> : <span>未写正文</span>}
          {outlineRow.scenes?.length ? <span>{outlineRow.scenes.length} 场</span> : null}
        </div>
      </header>
      <div className="proposal-chapter-grid">
        <ProposalChapterCard chapter={outlineRow} granularity={selected.granularity} />
      </div>
    </div>
  );
}

function CompletedChapterRevisionWorkspace({ selected, content }) {
  const chapter = selected?.chapter || 0;
  const title = selected?.title || `第 ${chapter || '?'} 章`;
  const contentMatches = content?.chapter === String(chapter);
  const status = contentMatches ? content.status : 'loading';
  const body = status === 'done' ? String(content.content || selected?.content || '') : '';
  const wordCount = contentMatches && content.wordCount > 0 ? content.wordCount : selected?.outlineRow?.writtenWordCount || 0;
  const outlineNotes = [
    selected?.outlineRow?.coreEvent,
    selected?.outlineRow?.hook,
    ...(selected?.outlineRow?.scenes || [])
  ].filter(Boolean);
  return (
    <div className="chapter-revision-workspace">
      <header className="proposal-workspace-header">
        <div>
          <div className="eyebrow">完本单章返工</div>
          <h3>{`第 ${chapter} 章：${title}`}</h3>
        </div>
        <div className="proposal-workspace-metrics">
          {wordCount > 0 ? <span>{formatCompact(wordCount)} 字</span> : null}
          {content?.source ? <span>{content.source === 'draft' ? '草稿' : '终稿'}</span> : null}
        </div>
      </header>
      {status === 'loading' ? (
        <article className="stream-round chapter-revision-empty">
          <span className="muted">正在加载章节正文...</span>
        </article>
      ) : status === 'error' ? (
        <article className="stream-round chapter-revision-empty error">
          <strong>章节正文读取失败</strong>
          <span>{content.error}</span>
        </article>
      ) : body ? (
        <article className="chapter-revision-body">
          <pre>{body}</pre>
        </article>
      ) : (
        <article className="stream-round chapter-revision-empty">
          <strong>暂无章节正文</strong>
          {outlineNotes.length ? <pre>{outlineNotes.join('\n')}</pre> : <span className="muted">当前章节只有大纲信息</span>}
        </article>
      )}
    </div>
  );
}

function formatScheduleTimestamp(value, timezone) {
  if (!value) return '尚未安排';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  try {
    return new Intl.DateTimeFormat('zh-CN', {
      timeZone: timezone,
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false
    }).format(date);
  } catch {
    return date.toLocaleString('zh-CN');
  }
}

function batchCount(batch, name) {
  const value = batch?.[name] ?? batch?.[`${name}_count`];
  return Number.isFinite(Number(value)) ? Number(value) : 0;
}

function ResumeSchedulePanel({ schedule, setSchedule, onRefresh, onSave }) {
  const saving = schedule.status === 'saving';
  const addDraftTime = () => {
    const time = normalizeDailyResumeTime(schedule.draftTime);
    if (!time) {
      setSchedule((previous) => ({ ...previous, error: '请选择有效的每日时间' }));
      return;
    }
    setSchedule((previous) => ({
      ...previous,
      dailyTimes: normalizeDailyResumeTimes([...previous.dailyTimes, time]),
      error: '',
      message: ''
    }));
  };
  const lastBatchAt = schedule.lastBatch?.triggered_at || schedule.lastBatch?.started_at || schedule.lastBatch?.at || '';
  return (
    <div className="side-content schedule-panel">
      <section>
        <div className="section-title">
          <Clock3 size={17} />
          <span>定时启动</span>
        </div>
        <p className="settings-note">每天到达配置时间后，系统会扫描所有项目，只恢复已开始但未完成的中间态。</p>
        <p className="settings-note warning">不会越过建议选择、分卷骨架、详细提案或续写提案等人工审核节点。</p>
      </section>

      <section className="settings-summary">
        <Metric label="时区" value={schedule.timezone} />
        <Metric label="下次检查" value={formatScheduleTimestamp(schedule.nextTriggerAt, schedule.timezone)} />
      </section>

      <form className="project-settings-form" onSubmit={onSave}>
        <label className="field-label">
          <span>添加每日时间</span>
          <div className="schedule-time-entry">
            <input
              aria-label="每日启动时间"
              disabled={saving}
              onChange={(event) => setSchedule((previous) => ({ ...previous, draftTime: event.target.value, error: '' }))}
              step="60"
              type="time"
              value={schedule.draftTime}
            />
            <button className="tool-button" disabled={saving} onClick={addDraftTime} type="button">
              <Plus size={16} /> 添加
            </button>
          </div>
        </label>

        <div className="schedule-time-list" aria-label="已配置的每日启动时间">
          {schedule.dailyTimes.length ? schedule.dailyTimes.map((time) => (
            <div className="schedule-time-item" key={time}>
              <strong>{time}</strong>
              <button
                aria-label={`删除 ${time}`}
                disabled={saving}
                onClick={() => setSchedule((previous) => ({
                  ...previous,
                  dailyTimes: previous.dailyTimes.filter((item) => item !== time),
                  message: '',
                  error: ''
                }))}
                type="button"
              >
                <Trash2 size={15} />
              </button>
            </div>
          )) : <div className="empty-state compact">没有配置时间；保存后将关闭全局定时启动。</div>}
        </div>

        {schedule.error ? <div className="error-banner compact">{schedule.error}</div> : null}
        {schedule.message ? <div className="success-note">{schedule.message}</div> : null}
        <div className="project-settings-actions">
          <button className="tool-button" disabled={schedule.status === 'loading' || saving} onClick={onRefresh} type="button">
            <RefreshCw size={16} /> 刷新
          </button>
          <button className="tool-button accent" disabled={saving} type="submit">
            <Check size={16} /> {saving ? '保存中…' : '统一保存'}
          </button>
        </div>
      </form>

      <section>
        <div className="section-title"><Activity size={17} /><span>最近批次</span></div>
        {schedule.lastBatch ? (
          <div className="settings-summary">
            <Metric label="执行时间" value={formatScheduleTimestamp(lastBatchAt, schedule.timezone)} />
            <div className="schedule-batch-counts">
              <span>启动 <strong>{batchCount(schedule.lastBatch, 'started')}</strong></span>
              <span>跳过 <strong>{batchCount(schedule.lastBatch, 'skipped')}</strong></span>
              <span>失败 <strong>{batchCount(schedule.lastBatch, 'failed')}</strong></span>
            </div>
          </div>
        ) : <div className="empty-state compact">尚无定时执行记录</div>}
      </section>
    </div>
  );
}

function ProjectSettingsPanel({
  activeProject,
  busy,
  coCreateActive = false,
  currentSimulationMode,
  currentStyle,
  globalBusy = false,
  projectSettings,
  setProjectSettings,
  snapshot,
  onSaveSimulationMode,
  onSaveStyle,
  onToggleScheduledResume,
  onRefreshStyles
}) {
  const styles = projectSettings.styles;
  const selectedStyle = projectSettings.selectedStyle || currentStyle;
  const selectedStyleInCatalog = styles.some((style) => style.id === selectedStyle);
  const currentStyleLabel = projectStyleLabel(styles, currentStyle);
  const styleLocked = isProjectStyleLocked(snapshot);
  const styleTemporarilyBusy = isProjectRunning(snapshot) && !styleLocked;
  const styleEditable = canEditProjectStyle({ activeProject, busy, projectSettings, snapshot });
  const canSaveStyle = canSubmitProjectStyle({ activeProject, busy, currentStyle, projectSettings, snapshot });
  const selectedSimulationMode = normalizeSimulationMode(projectSettings.selectedSimulationMode);
  const currentSimulationModeLabel = simulationModeLabel(currentSimulationMode);
  const simulationProfile = getSimulationProfileStatus(snapshot);
  const simulationModeNotice = projectSimulationModeNotice({
    currentMode: currentSimulationMode,
    profile: simulationProfile,
    selectedMode: selectedSimulationMode
  });
  const canSaveSimulationMode = canSubmitProjectSimulationMode({
    activeProject,
    busy: globalBusy,
    coCreateActive,
    currentSimulationMode,
    projectSettings,
    snapshot
  });
  return (
    <div className="side-content">
      <section>
        <div className="section-title">
          <SlidersHorizontal size={17} />
          <span>项目设定</span>
        </div>
        {!activeProject ? (
          <div className="empty-state">请选择一个项目</div>
        ) : (
          <div className="settings-summary">
            <Metric label="当前文风" value={currentStyleLabel} />
            <Metric label="当前仿写模式" value={currentSimulationModeLabel} />
          </div>
        )}
      </section>

      {activeProject ? (
        <>
        <section>
          <div className="section-title">
            <Clock3 size={17} />
            <span>定时恢复</span>
          </div>
          <label className="settings-toggle">
            <input
              checked={projectSettings.scheduledResumeEnabled}
              disabled={projectSettings.scheduledResumeLoadStatus === 'running' || projectSettings.scheduledResumeSaveStatus === 'running'}
              onChange={(event) => onToggleScheduledResume(event.target.checked)}
              role="switch"
              type="checkbox"
            />
            <span>
              <strong>允许此项目定时恢复</strong>
              <small>默认开启。关闭后，全局定时检查会跳过此项目，但不会暂停当前正在运行的任务。</small>
            </span>
          </label>
          <div className="settings-note">只恢复未完成的执行态；需要你审核或选择的阶段不会自动推进。</div>
          {projectSettings.scheduledResumeSaveStatus === 'done' ? <div className="success-note">项目定时恢复设置已保存</div> : null}
          {projectSettings.scheduledResumeError ? <div className="error-banner compact">{projectSettings.scheduledResumeError}</div> : null}
        </section>
        <section>
          <div className="section-title">
            <FileText size={17} />
            <span>文风</span>
          </div>
          <form className="project-settings-form" onSubmit={onSaveStyle}>
            <label className="field-label">
              <span>协作风格</span>
              <select
                disabled={!styleEditable}
                value={selectedStyle}
                onChange={(event) => {
                  const nextStyle = event.target.value;
                  setProjectSettings((previous) => ({
                    ...previous,
                    selectedStyle: nextStyle,
                    saveStatus: 'idle',
                    message: '',
                    error: ''
                  }));
                }}
              >
                {styles.length === 0 ? (
                  <option value={selectedStyle}>{projectStyleLabel(styles, selectedStyle)}</option>
                ) : (
                  <>
                    {!selectedStyleInCatalog && selectedStyle ? (
                      <option value={selectedStyle}>{projectStyleLabel(styles, selectedStyle)}</option>
                    ) : null}
                    {styles.map((style) => (
                      <option key={style.id} value={style.id}>{projectStyleLabel(styles, style.id)}</option>
                    ))}
                  </>
                )}
              </select>
            </label>

            {styleLocked ? (
              <div className="settings-note warning">正式开始创作后文风不可变更</div>
            ) : null}
            {styleTemporarilyBusy ? (
              <div className="settings-note">当前任务运行中，完成或暂停后可修改文风</div>
            ) : null}
            {projectSettings.loadStatus === 'error' ? (
              <div className="error-banner compact">{projectSettings.error}</div>
            ) : null}
            {projectSettings.saveStatus === 'error' && projectSettings.error ? (
              <div className="error-banner compact">{projectSettings.error}</div>
            ) : null}
            {projectSettings.saveStatus === 'done' && projectSettings.message ? (
              <div className="success-note">{projectSettings.message}</div>
            ) : null}

            <div className="project-settings-actions">
              <button className="tool-button" disabled={projectSettings.loadStatus === 'running'} onClick={onRefreshStyles} type="button">
                <RefreshCw size={16} />
                刷新
              </button>
              <button className="tool-button accent" disabled={!canSaveStyle} type="submit">
                <Check size={16} />
                保存
              </button>
            </div>
          </form>
        </section>

        <section>
          <div className="section-title">
            <WandSparkles size={17} />
            <span>仿写画像</span>
          </div>
          <form className="project-settings-form" onSubmit={onSaveSimulationMode}>
            <div className="settings-summary">
              <Metric label="已选模式" value={currentSimulationModeLabel} />
              <Metric label="实际模式" value={simulationModeLabel(simulationProfile.effectiveMode)} />
            </div>
            <fieldset className="settings-radio-group" disabled={!activeProject || globalBusy || projectSettings.simulationModeSaveStatus === 'running'}>
              <legend>仿写模式</legend>
              {['normal', 'reinforced'].map((mode) => (
                <label key={mode} className={`settings-radio-option ${selectedSimulationMode === mode ? 'active' : ''}`}>
                  <input
                    checked={selectedSimulationMode === mode}
                    name="simulation-mode"
                    onChange={() => {
                      setProjectSettings((previous) => ({
                        ...previous,
                        selectedSimulationMode: mode,
                        simulationModeSaveStatus: 'idle',
                        simulationModeMessage: '',
                        simulationModeError: ''
                      }));
                    }}
                    type="radio"
                    value={mode}
                  />
                  <span>
                    <strong>{simulationModeLabel(mode)}</strong>
                    <small>{mode === 'reinforced' ? '使用更高预算的 Architect、Writer、Editor 共享契约；不复制来源内容，也不承诺法律结论。' : '低干预建议模式；用户要求、Foundation 与章节契约始终优先。'}</small>
                  </span>
                </label>
              ))}
            </fieldset>

            {simulationModeNotice ? (
              <div className={`settings-note ${simulationModeNotice.tone}`}>
                {simulationModeNotice.message}
              </div>
            ) : null}
            {projectSettings.simulationModeSaveStatus === 'error' && projectSettings.simulationModeError ? (
              <div className="error-banner compact">{projectSettings.simulationModeError}</div>
            ) : null}
            {projectSettings.simulationModeSaveStatus === 'done' && projectSettings.simulationModeMessage ? (
              <div className="success-note">{projectSettings.simulationModeMessage}</div>
            ) : null}

            <div className="project-settings-actions">
              <button className="tool-button accent" disabled={!canSaveSimulationMode} type="submit">
                <Check size={16} />
                保存
              </button>
            </div>
          </form>
        </section>
        </>
      ) : null}
    </div>
  );
}

function StatusPanel({
  snapshot,
  activeProject,
  chapterRevision,
  outlineRevision,
  setChapterRevision,
  setOutlineRevision,
  onPause,
  onRefreshOutline,
  onReviseOutline,
  onReviseChapter,
  onSteer,
  steerText,
  setSteerText,
  busy
}) {
  const outlineStructure = getSnapshotOutlineStructure(snapshot);
  const outline = outlineStructure.chapters;
  const agents = snapshot?.Agents || snapshot?.agents || [];
  const premise = textValue(snapshot, 'PremiseFull', 'premise_full', 'Premise', 'premise');
  const characterDetails = arrayValue(snapshot, 'CharacterDetails', 'character_details');
  const worldRules = arrayValue(snapshot, 'WorldRules', 'world_rules');
  const blueprint = getCreativeBlueprint(snapshot);
  const hasFoundation = Boolean(premise || outline.length || characterDetails.length || worldRules.length);
  const running = isProjectRunning(snapshot);
  return (
    <div className="side-content">
      <section className="metric-grid">
        <Metric label="状态" value={snapshot?.RuntimeState || snapshot?.runtime_state || 'idle'} />
        <Metric label="章节" value={`${snapshot?.CompletedCount || 0}/${snapshot?.TotalChapters || 0}`} />
        <Metric label="当前" value={snapshot?.CurrentChapter || snapshot?.InProgressChapter || 0} />
        <Metric label="字数" value={snapshot?.TotalWordCount || 0} />
      </section>

      <div className="runtime-actions">
        <button className="tool-button" disabled={!activeProject || !running} onClick={onPause} type="button">
          <PauseCircle size={16} />
          暂停
        </button>
      </div>

      <form className="steer-form" onSubmit={onSteer}>
        <textarea
          aria-label="干预输入"
          disabled={!activeProject || busy}
          placeholder="运行中即时干预；未运行时保存为下次生效"
          value={steerText}
          onChange={(event) => setSteerText(event.target.value)}
        />
        <button className="tool-button" disabled={!activeProject || busy} type="submit">
          <Send size={16} />
          干预
        </button>
      </form>

      {blueprint.loaded ? (
        <section className="snapshot-summary-card">
          <div className="section-title">
            <BookOpen size={17} />
            <span>创作蓝图</span>
          </div>
          <div className="summary-metrics">
            <Metric label="大纲" value={blueprint.outlineChapters} />
            <Metric label="角色" value={blueprint.characterCount} />
            <Metric label="规则" value={blueprint.worldRuleCount} />
            <Metric label="结构" value={blueprint.layered ? '分层' : '扁平'} />
          </div>
          {blueprint.premise ? <p>{blueprint.premise}</p> : null}
          {blueprint.compassDirection || blueprint.compassScale ? (
            <small>{[blueprint.compassDirection, blueprint.compassScale].filter(Boolean).join(' / ')}</small>
          ) : null}
        </section>
      ) : null}

      <OutlineChapterRevisionControls
        activeProject={activeProject}
        busy={busy}
        outline={outline}
        revision={outlineRevision}
        running={running}
        setRevision={setOutlineRevision}
        onRefresh={onRefreshOutline}
        onRevise={onReviseOutline}
      />

      <CompletedChapterRevisionControls
        activeProject={activeProject}
        busy={busy}
        revision={chapterRevision}
        setRevision={setChapterRevision}
        snapshot={snapshot}
        onRevise={onReviseChapter}
      />

      <section>
        <div className="section-title">
          <BookOpen size={17} />
          <span>章节</span>
        </div>
        <OutlineStructureTree
          emptyText="暂无大纲"
          granularity={getSnapshotOutlineGranularity(snapshot)}
          structure={outlineStructure}
        />
      </section>

      <section>
        <div className="section-title">
          <Activity size={17} />
          <span>Agents</span>
        </div>
        <div className="agent-list">
          {agents.length === 0 ? (
            <div className="empty-state">暂无 agent 状态</div>
          ) : (
            agents.map((agent) => (
              <div className="agent-row" key={agent.Name || agent.name}>
                <strong>{agent.Name || agent.name}</strong>
                <span>{agent.State || agent.state || 'idle'}</span>
              </div>
            ))
          )}
        </div>
      </section>
    </div>
  );
}

const chapterRevisionModes = [
  { value: 'rewrite', label: '重写' },
  { value: 'polish', label: '打磨' }
];

function OutlineChapterRevisionControls({
  activeProject,
  busy,
  outline,
  revision,
  running,
  setRevision,
  onRefresh,
  onRevise
}) {
  const selectedChapter = clampOutlineChapterSelection(revision.chapter, outline);
  useEffect(() => {
    if (!outline.length) {
      if (revision.active) {
        setRevision((previous) => ({ ...previous, active: false }));
      }
      return;
    }
    if (selectedChapter !== String(revision.chapter || '')) {
      setRevision((previous) => ({ ...previous, chapter: selectedChapter }));
    }
  }, [outline.length, revision.active, revision.chapter, selectedChapter, setRevision]);
  if (!outline.length) {
    return null;
  }
  const instruction = String(revision.instruction || '');
  const canSubmit = Boolean(activeProject && !busy && !running && instruction.trim());
  const canRefresh = Boolean(activeProject && !busy);
  const update = (changes) => setRevision((previous) => ({
    ...previous,
    ...changes,
    active: true,
    status: 'idle',
    message: '',
    error: ''
  }));
  const activatePreview = () => setRevision((previous) => (
    previous.active ? previous : { ...previous, active: true }
  ));
  return (
    <section className="simulation-section proposal-revision-section">
      <div className="section-title">
        <Pencil size={17} />
        <span>章节细纲修改</span>
      </div>
      <label className="field-label proposal-select-line">
        <span>章节</span>
        <select
          disabled={busy}
          value={selectedChapter}
          onFocus={activatePreview}
          onChange={(event) => update({ chapter: event.target.value })}
        >
          {outline.map((item) => (
            <option key={`outline-revision-${item.chapter}`} value={item.chapter}>
              第 {item.chapter} 章：{item.title}
            </option>
          ))}
        </select>
      </label>
      <label className="field-label proposal-select-line">
        <span>修改要求</span>
        <textarea
          className="proposal-revision-textarea"
          disabled={busy}
          placeholder="写下这一章的剧情、节奏、伏笔、人物关系或场景调整要求..."
          value={instruction}
          onFocus={activatePreview}
          onChange={(event) => update({ instruction: event.target.value })}
        />
      </label>
      {running ? <div className="settings-note warning">创作运行中，请先暂停后再提交章节细纲修改。</div> : null}
      <div className="simulation-actions">
        <button className="tool-button" disabled={!canRefresh} onClick={onRefresh} type="button">
          <RefreshCw size={16} />
          刷新细纲
        </button>
        <button className="tool-button accent" disabled={!canSubmit} onClick={() => runWithWindowScrollPreserved(onRevise)} type="button">
          <WandSparkles size={16} />
          提交修改
        </button>
      </div>
      <div className={`workflow-status ${revision.status || 'idle'}`}>
        <strong>{workflowStatusText(revision.status || 'idle')}</strong>
        <span>{revision.error || revision.message || '选择章节后可在中间查看当前细纲'}</span>
      </div>
    </section>
  );
}

function CompletedChapterRevisionControls({
  activeProject,
  busy,
  revision,
  setRevision,
  snapshot,
  onRevise
}) {
  const view = getCompletedBookChapterRevisionView(snapshot);
  const selectedChapter = clampOutlineChapterSelection(revision.chapter, view.outline);
  useEffect(() => {
    if (!view.visible || selectedChapter === String(revision.chapter || '')) {
      return;
    }
    setRevision((previous) => ({ ...previous, chapter: selectedChapter }));
  }, [revision.chapter, selectedChapter, setRevision, view.visible]);
  if (!view.visible) {
    return null;
  }
  const instruction = String(revision.instruction || '');
  const mode = normalizeChapterRevisionMode(revision.mode);
  const canSubmit = Boolean(activeProject && !busy && instruction.trim());
  const update = (changes) => setRevision((previous) => ({
    ...previous,
    ...changes,
    status: 'idle',
    message: '',
    error: ''
  }));
  return (
    <section className="simulation-section proposal-revision-section">
      <div className="section-title">
        <Pencil size={17} />
        <span>完本单章返工</span>
      </div>
      <label className="field-label proposal-select-line">
        <span>章节</span>
        <select
          disabled={busy}
          value={selectedChapter}
          onChange={(event) => update({ chapter: event.target.value })}
        >
          {view.outline.map((item) => (
            <option key={`completed-revise-${item.chapter}`} value={item.chapter}>
              第 {item.chapter} 章：{item.title}
            </option>
          ))}
        </select>
      </label>
      <div className="proposal-revision-mode-grid" role="radiogroup" aria-label="单章返工方式">
        {chapterRevisionModes.map((item) => (
          <button
            className={mode === item.value ? 'revision-mode-button active' : 'revision-mode-button'}
            disabled={busy}
            key={item.value}
            onClick={() => update({ mode: item.value })}
            type="button"
          >
            {item.label}
          </button>
        ))}
      </div>
      <label className="field-label proposal-select-line">
        <span>修改意见</span>
        <textarea
          className="proposal-revision-textarea"
          disabled={busy}
          placeholder="写下这一章需要重写或打磨的具体方向..."
          value={instruction}
          onChange={(event) => update({ instruction: event.target.value })}
        />
      </label>
      <button className="tool-button accent full-width" disabled={!canSubmit} onClick={() => runWithWindowScrollPreserved(onRevise)} type="button">
        <WandSparkles size={16} />
        提交单章返工
      </button>
      <div className={`workflow-status ${revision.status || 'idle'}`}>
        <strong>{workflowStatusText(revision.status || 'idle')}</strong>
        <span>{revision.error || revision.message || '等待提交'}</span>
      </div>
    </section>
  );
}

const adaptationModes = [
  { value: 'chapter', label: 'chapter' },
  { value: 'arc', label: 'arc' },
  { value: 'free', label: 'free' }
];

function CoCreatePanel({
  activeProject,
  busy,
  coCreate,
  planningRevision,
  planningReview = {},
  setCoCreate,
  setPlanningRevision,
  adaptation,
  onBegin,
  onConfirmIntake,
  onSubmit,
  onRebrief = () => {},
  onSuggestion,
  onRevise,
  onResolveDecision = () => {},
  onCommit,
  onConfirmPlanning = () => {},
  onOpenFoundationReview = () => {},
  onRevisePlanning = () => {},
  onCancel,
  workspaceTranscript = false,
  continuationOnly = false
}) {
  const [editing, setEditing] = useState(null);
  const hasConversation = coCreate.messages.length > 0;
  const hasBackendSession = coCreate.active || (hasConversation && !coCreate.intakeActive);
  const showIntakeControls = coCreate.intakeActive && !hasBackendSession;
  const targetTotalWords = resolveCoCreateTargetTotalWords(coCreate);
  const canBeginNormal = Boolean(activeProject && !busy && !hasBackendSession && !coCreate.intakeActive && coCreate.input.trim());
  const canBeginAdapt = Boolean(
    activeProject &&
      !busy &&
      !hasBackendSession &&
      !coCreate.intakeActive &&
      adaptation.sourceFile?.relative_path &&
      adaptation.analysisStatus === 'done'
  );
  const hasPendingDecisions = Array.isArray(coCreate.pendingDecisions) && coCreate.pendingDecisions.length > 0;
  const pendingDecisionTotalCount = Math.max(
    Number(coCreate.briefing?.pending_decision_count || 0),
    coCreate.pendingDecisions.length
  );
  const canSend = Boolean(activeProject && !busy && hasBackendSession && coCreate.input.trim() && !hasPendingDecisions);
  const canRebrief = Boolean(canSend && coCreate.kind === 'adapt');
  const rebriefDisabledReason = adaptationRebriefDisabledReason({
    activeProject,
    busy,
    coCreate,
    hasBackendSession,
    hasPendingDecisions
  });
  const canResume = Boolean(activeProject && !busy && hasBackendSession && coCreate.failed && !hasPendingDecisions);
  const canConfirmIntake = Boolean(activeProject && !busy && showIntakeControls && targetTotalWords > 0);
  const hasDraftPrompt = Boolean(coCreate.draftPrompt.trim());
  const canCommit = Boolean(activeProject && !busy && hasDraftPrompt && coCreate.canStart);
  const canCancel = canCancelCoCreateFlow({ activeProject, busy, coCreate });
  const canConfirmPlanning = Boolean(activeProject && !busy && planningReview.pending);
  const visibleSuggestions = coCreate.suggestions.slice(0, 3);
  const showDraftWorkspace = Boolean(coCreate.ready || hasDraftPrompt);
  const showSideDraftPreview = showDraftWorkspace && !workspaceTranscript;
  const suggestionList = visibleSuggestions.length ? (
    <div
      className={`suggestion-list ${workspaceTranscript ? 'cocreate-side-suggestions' : 'cocreate-dialog-suggestions'}`}
      aria-label="AI 建议"
    >
      {visibleSuggestions.map((suggestion) => (
        <button
          className="suggestion-option"
          disabled={busy}
          key={suggestion}
          onClick={() => onSuggestion(suggestion)}
          type="button"
        >
          <SquarePen size={15} />
          {suggestion}
        </button>
      ))}
    </div>
  ) : null;
  const title = continuationOnly ? '续写 Draft 共创' : coCreateTitle(coCreate.kind);
  const handleCoCreateFormSubmit = (event) => {
    event.preventDefault();
    if (hasBackendSession) {
      onSubmit(event);
    } else if (showIntakeControls) {
      onConfirmIntake();
    } else {
      onBegin('normal');
    }
  };
  if (planningReview.active) {
    const confirmAction = coCreatePlanningConfirmAction(planningReview);
    const revisionStatus = planningRevision?.status || 'idle';
    const reviewStatus = planningReview.collecting ? 'running' : planningReview.pending ? 'ready' : 'idle';
    if (planningReview.kind === 'foundation') {
      return (
        <div className="side-content proposal-side-panel foundation-review-panel">
          {coCreate.error ? <div className="error-banner compact">{coCreate.error}</div> : null}
          <section className="simulation-section planning-review-card">
            <div className="section-title">
              <BookOpen size={17} />
              <span>{planningReview.adaptation ? '改编 Foundation 审核' : 'StoryFoundation 审核'}</span>
            </div>
            <div className="proposal-side-summary">
              <strong>{planningReview.collecting ? '生成中' : planningReview.characterConfirmationRequired ? '角色卡待确认' : '完整设定待确认'}</strong>
              <span>Revision {planningReview.foundationRevision || '—'} / Generation {planningReview.foundationGeneration || 1}</span>
              <span>{planningReview.characterConfirmationRequired
                ? '角色候选审核：已通过，尚未发布'
                : `CoreCast ${planningReview.adaptation ? '保持' : '基线'}校验：${planningReview.coreCastPreserved ? '通过' : '等待生成'}`}</span>
            </div>
            <div className="foundation-review-sidebar-guide">
              <BookOpen size={20} />
              <div>
                <strong>{planningReview.characterConfirmationRequired ? '请在中央工作区确认角色候选' : '完整审核位于中央工作区'}</strong>
                <p>{planningReview.characterConfirmationRequired
                  ? '当前角色不是空白；确认后 Architect 会继续生成故事前提、完整关系和世界规则。'
                  : '使用页签分别检查概要、角色、关系和世界规则；修改意见与确认集中在最后一页。'}</p>
              </div>
            </div>
            <ul className="foundation-review-sidebar-list">
              <li><span>角色</span><strong>{planningReview.coreCharacters.length + planningReview.supportingCharacters.length}</strong></li>
              <li><span>计划关系</span><strong>{planningReview.plannedRelationships.length}</strong></li>
              <li><span>世界规则</span><strong>{planningReview.hardWorldRules.length + planningReview.softWorldRules.length}</strong></li>
              {planningReview.adaptation ? <li><span>原著证据</span><strong>只读</strong></li> : null}
            </ul>
            <button
              className="tool-button accent full-width foundation-review-open-actions"
              disabled={planningReview.collecting}
              onClick={() => onOpenFoundationReview(planningReview.characterConfirmationRequired ? 'characters' : 'actions')}
              type="button"
            >
              <Check size={16} />
              {planningReview.characterConfirmationRequired ? '查看并确认角色卡' : '前往确认与修改'}
            </button>
            <small>Audit {planningReview.foundationAuditSignature ? `${planningReview.foundationAuditSignature.slice(0, 12)}…` : '待生成'}</small>
          </section>
          {planningReview.collecting ? (
            <div className="workflow-status running">
              <strong>生成中</strong>
              <span>Architect 正在补全完整 Foundation；任何正式 outline 均已在服务端封锁。</span>
            </div>
          ) : null}
          {planningReview.readonly ? <div className="error-banner compact">只读：{foundationReadonlyReasonLabel(planningReview.readonlyReason)}</div> : null}
        </div>
      );
    }
    return (
      <div className="side-content proposal-side-panel">
        {coCreate.error ? <div className="error-banner compact">{coCreate.error}</div> : null}
        {planningRevision?.error ? <div className="error-banner compact">{planningRevision.error}</div> : null}
        <section className="simulation-section proposal-control-section planning-review-card">
          <div className="section-title">
            <Check size={17} />
            <span>规划审核</span>
          </div>
          <div className="proposal-side-summary">
            <strong>{planningReview.collecting ? '生成中' : '待审核'}</strong>
            <span>
              {coCreatePlanningKindLabel(planningReview.kind)}
              {planningReview.chapterCount ? ` / ${planningReview.chapterCount} 章` : ''}
              {planningReview.targetTotalWords ? ` / ${formatCompact(planningReview.targetTotalWords)} 字` : ''}
            </span>
            {planningReview.volumes?.length ? <span>{planningReview.volumes.length} 卷</span> : null}
          </div>
          {planningReview.collecting ? (
            <div className="workflow-status running">
              <strong>生成中</strong>
              <span>{coCreatePlanningRunningCopy(planningReview)}</span>
            </div>
          ) : null}
        </section>
        {!planningReview.collecting ? (
          <section className="simulation-section proposal-revision-section">
            <div className="section-title">
              <Pencil size={17} />
              <span>修改规划</span>
            </div>
            <PlanningRevisionControls
              busy={busy}
              disabled={!planningReview.pending}
              onRevise={onRevisePlanning}
              review={planningReview}
              revision={planningRevision}
              setRevision={setPlanningRevision}
            />
          </section>
        ) : null}
        <section className="simulation-section proposal-confirm-section">
          {confirmAction.visible ? (
            <button className="tool-button accent full-width" disabled={!canConfirmPlanning} onClick={() => runWithWindowScrollPreserved(onConfirmPlanning)} type="button">
              <Check size={16} />
              {confirmAction.label}
            </button>
          ) : null}
          <div className={`workflow-status ${revisionStatus !== 'idle' ? revisionStatus : reviewStatus}`}>
            <strong>{workflowStatusText(revisionStatus !== 'idle' ? revisionStatus : reviewStatus)}</strong>
            <span>{planningRevision?.message || (confirmAction.visible ? '等待审稿' : '请先提交修改意见重新生成规划')}</span>
          </div>
        </section>
      </div>
    );
  }
  return (
    <div className="side-content cocreate-panel">
      {coCreate.error ? <div className="error-banner compact">{coCreate.error}</div> : null}

      <section className="cocreate-section">
        <div className="section-title">
          <MessageSquareText size={17} />
          <span>{title}</span>
        </div>
        {!continuationOnly ? <div className="cocreate-mode-grid">
          <button className="tool-button" disabled={!canBeginNormal} onClick={() => onBegin('normal')} type="button">
            <SquarePen size={16} />
            普通
          </button>
          <button className="tool-button" disabled={!canBeginAdapt} onClick={() => onBegin('adapt')} type="button">
            <FileText size={16} />
            改编
          </button>
        </div> : null}
        {coCreate.modeLocked ? (
          <div className="success-note">已锁定 {coCreate.adaptMode} / {coCreate.rewritePolicy}</div>
        ) : null}
        {hasPendingDecisions ? (
          <CoCreateDecisionQueue
            decisions={coCreate.pendingDecisions}
            busy={busy}
            totalCount={pendingDecisionTotalCount}
            onResolve={onResolveDecision}
          />
        ) : null}
        {canResume ? (
          <div className="cocreate-resume-card">
            <div>
              <strong>上次生成失败，进度已保存</strong>
              <span>点击工作区顶部“恢复”，会沿用当前共创上下文和已确认决策继续生成。</span>
            </div>
          </div>
        ) : null}
      </section>

      {workspaceTranscript ? (
        suggestionList ? <section className="cocreate-section cocreate-side-suggestion-section">{suggestionList}</section> : null
      ) : (
      <section className="cocreate-section cocreate-dialog">
        <div className="cocreate-messages">
          {coCreate.messages.length === 0 ? (
            <div className="empty-state">暂无共创对话</div>
          ) : (
            coCreate.messages.map((message, index) => (
              <div className={`cocreate-message ${message.role}`} key={message.id || `${message.role}-${index}`}>
                <div className="cocreate-message-head">
                  <strong>{coCreateRoleLabel(message.role)}</strong>
                  {message.source ? <span>{message.source === 'suggestion' ? '选项' : '补充'}</span> : null}
                  {message.editable ? (
                    <button
                      className="icon-button inline"
                      disabled={busy}
                      onClick={() => setEditing({ id: message.id, text: message.content })}
                      title="修改这次选择"
                      type="button"
                    >
                      <Pencil size={14} />
                    </button>
                  ) : null}
                </div>
                {editing?.id === message.id ? (
                  <form
                    className="cocreate-edit-form"
                    onSubmit={async (event) => {
                      event.preventDefault();
                      const text = editing.text.trim();
                      if (!text) {
                        return;
                      }
                      await onRevise(message.id, text);
                      setEditing(null);
                    }}
                  >
                    <textarea
                      autoFocus
                      disabled={busy}
                      value={editing.text}
                      onChange={(event) => setEditing((previous) => ({ ...previous, text: event.target.value }))}
                      onKeyDown={(event) => {
                        if (event.key === 'Escape') {
                          event.preventDefault();
                          setEditing(null);
                        }
                      }}
                    />
                    <div className="inline-actions">
                      <button className="icon-button" disabled={busy} onClick={() => setEditing(null)} title="取消" type="button">
                        <X size={15} />
                      </button>
                      <button className="icon-button primary" disabled={busy || !editing.text.trim()} title="保存并重跑" type="submit">
                        <Check size={15} />
                      </button>
                    </div>
                  </form>
                ) : (
                  <p>{message.content}</p>
                )}
              </div>
            ))
          )}
        </div>
        {coCreate.streamThinking || coCreate.streamReply ? (
          <div className="cocreate-progress">
            {coCreate.streamThinking ? (
              <div className="cocreate-preview thinking">
                <strong>Thinking</strong>
                <p>{coCreate.streamThinking}</p>
              </div>
            ) : null}
            {coCreate.streamReply ? (
              <div className="cocreate-preview reply">
                <strong>AI</strong>
                <p>{coCreate.streamReply}</p>
              </div>
            ) : null}
          </div>
        ) : null}
        {suggestionList}
      </section>
      )}

      <div className="cocreate-sticky-workspace">
        <form className="cocreate-form" onSubmit={handleCoCreateFormSubmit}>
        <textarea
          aria-label="共创输入"
          disabled={!activeProject || busy || showIntakeControls || hasPendingDecisions}
          placeholder={
            showIntakeControls
              ? '先确认篇幅和结构...'
              : hasPendingDecisions ? '先处理上方的共创前置问题...' : hasBackendSession ? '继续补充你的想法...' : '输入你的核心想法，或先进入 Stage/Adapt 共创'
          }
          value={coCreate.input}
          onChange={(event) => setCoCreate((previous) => appendCoCreateInput(previous, event.target.value))}
        />
        {showIntakeControls ? (
          <div className="cocreate-intake">
            <div className="intake-question">
              <strong>目标字数</strong>
              <div className="cocreate-target-options" aria-label="Target total words">
              {coCreateTargetWordChoices.map((choice) => (
                <label
                  className={`target-option ${coCreate.targetTotalWordsChoice === choice.value ? 'active' : ''}`}
                  key={choice.value}
                >
                  <input
                    checked={coCreate.targetTotalWordsChoice === choice.value}
                    disabled={!activeProject || busy}
                    name="cocreate-target-total-words"
                    type="radio"
                    value={choice.value}
                    onChange={(event) => {
                      const value = event.target.value;
                      setCoCreate((previous) => ({
                        ...previous,
                        targetTotalWordsChoice: value,
                        customTargetTotalWords: value === 'custom' ? previous.customTargetTotalWords || '' : previous.customTargetTotalWords,
                        structureChoice:
                          previous.structureChoice === 'single' && value !== '5000' && value !== 'custom'
                            ? 'auto'
                            : previous.structureChoice
                      }));
                    }}
                  />
                  <span>{choice.label}</span>
                  <small>{choice.hint}</small>
                </label>
              ))}
              </div>
            </div>
            {coCreate.targetTotalWordsChoice === 'custom' ? (
              <label className="field-label">
                <span>总字数</span>
                <input
                  disabled={!activeProject || busy}
                  inputMode="numeric"
                  min="1"
                  placeholder="12000"
                  type="number"
                  value={coCreate.customTargetTotalWords || ''}
                  onChange={(event) => setCoCreate((previous) => ({ ...previous, customTargetTotalWords: event.target.value }))}
                />
              </label>
            ) : null}
            <div className="intake-question">
              <strong>结构形式</strong>
              <div className="cocreate-target-options structure" aria-label="Story structure">
                {coCreateStructureChoices.map((choice) => (
                  <label
                    className={`target-option ${resolveCoCreateStructureChoice(coCreate) === choice.value ? 'active' : ''}`}
                    key={choice.value}
                  >
                    <input
                      checked={resolveCoCreateStructureChoice(coCreate) === choice.value}
                      disabled={!activeProject || busy}
                      name="cocreate-structure"
                      type="radio"
                      value={choice.value}
                      onChange={(event) => {
                        setCoCreate((previous) => ({
                          ...previous,
                          structureChoice: event.target.value
                        }));
                      }}
                    />
                    <span>{choice.label}</span>
                    <small>{choice.hint}</small>
                  </label>
                ))}
              </div>
            </div>
          </div>
        ) : null}
        <div className="cocreate-submit-row">
        <button className="tool-button accent full-width" disabled={hasBackendSession ? !canSend : showIntakeControls ? !canConfirmIntake : !canBeginNormal} type="submit">
          <Send size={16} />
          {hasBackendSession ? '发送' : showIntakeControls ? '确认并开始共创' : '开始普通共创'}
        </button>
        {hasBackendSession && coCreate.kind === 'adapt' ? (
          <button className="tool-button" disabled={!canRebrief} onClick={onRebrief} title={rebriefDisabledReason} type="button">
            <RefreshCw size={16} />
            重新生成共创简报
          </button>
        ) : null}
        </div>
        </form>

        <section className={`cocreate-section ${showSideDraftPreview ? '' : 'cocreate-status-compact'}`}>
        <div className={`workflow-status ${coCreate.status}`}>
          <strong>{coCreateStatusText(coCreate.status, coCreate.ready, hasDraftPrompt)}</strong>
          <span>{coCreateStatusDetail(coCreate)}</span>
        </div>
        {showSideDraftPreview ? (
        <div className="draft-preview">
          {coCreate.draftPrompt ? <pre>{coCreate.draftPrompt}</pre> : <span className="muted">AI 会在这里整理 draft prompt</span>}
        </div>
        ) : null}
        <div className="cocreate-actions">
          {showDraftWorkspace ? (
            <button className="tool-button accent" disabled={!canCommit} onClick={onCommit} type="button">
              <Play size={16} />
              {continuationOnly ? '确认 Draft' : '启动'}
            </button>
          ) : null}
          <button className="tool-button" disabled={!canCancel} onClick={onCancel} type="button">
            <ListRestart size={16} />
            取消
          </button>
        </div>
        </section>
      </div>
    </div>
  );
}

function CoCreateDecisionQueue({ decisions = [], busy, totalCount = 0, onResolve }) {
  const normalizedDecisions = Array.isArray(decisions) ? decisions : [];
  const decisionCount = normalizedDecisions.length;
  const parsedTotalCount = Number.parseInt(String(totalCount || ''), 10);
  const remainingCount = Math.max(Number.isFinite(parsedTotalCount) ? parsedTotalCount : 0, decisionCount);
  const [answers, setAnswers] = useState({});
  const [pageIndex, setPageIndex] = useState(0);
  const decisionListKey = normalizedDecisions
    .map((decision, index) => {
      const optionsKey = (decision.options || []).map((option) => option.id || '').join(',');
      return `${decision.id || index}:${decision.recommended_option_id || ''}:${optionsKey}`;
    })
    .join('|');

  useEffect(() => {
    setAnswers((previous) => normalizeCoCreateDecisionAnswers(normalizedDecisions, previous));
  }, [decisionListKey]);

  useEffect(() => {
    setPageIndex((previous) => clampCoCreateDecisionPageIndex(previous, decisionCount));
  }, [decisionCount]);

  if (!decisionCount) {
    return null;
  }

  const activeIndex = clampCoCreateDecisionPageIndex(pageIndex, decisionCount);
  const activeDecision = normalizedDecisions[activeIndex] || {};
  const activeKey = coCreateDecisionKey(activeDecision, activeIndex);
  const activeAnswer = answers[activeKey] || {};
  const activeAnswerComplete = isCoCreateDecisionAnswerComplete(activeAnswer);
  const answeredCount = normalizedDecisions.filter((decision, index) => {
    const key = coCreateDecisionKey(decision, index);
    return isCoCreateDecisionAnswerComplete(answers[key]);
  }).length;
  const payload = buildCoCreateDecisionPayload(normalizedDecisions, answers);
  const canSubmit = payload.length === decisionCount && payload.every(isCoCreateDecisionPayloadComplete);
  const canGoPrevious = activeIndex > 0;
  const canGoNext = activeIndex < decisionCount - 1 && activeAnswerComplete;
  const selectedSkip = activeAnswer.optionId === CO_CREATE_DECISION_SKIP_OPTION_ID;
  const customValue = selectedSkip ? '' : activeAnswer.customAnswer || '';
  const recommendedOptionId = String(activeDecision.recommended_option_id || '').trim();
  const recommendedOptionLetter = coCreateDecisionRecommendedLetter(activeDecision);

  const setActiveAnswer = (answer) => {
    setAnswers((previous) => ({
      ...previous,
      [activeKey]: answer
    }));
    if (activeIndex < decisionCount - 1) {
      setPageIndex(activeIndex + 1);
    }
  };

  return (
    <div className="cocreate-decisions">
      <div className="decision-queue-head">
        <div className="decision-queue-title">
          <strong>共创前置决策</strong>
          <span>第 {activeIndex + 1} / {decisionCount} 个，已处理 {answeredCount} 个，共 {remainingCount} 项待确认</span>
        </div>
        <div className="decision-pager" aria-label="共创决策分页">
          <button
            aria-label="上一个决策"
            className="icon-button"
            disabled={busy || !canGoPrevious}
            onClick={() => setPageIndex(activeIndex - 1)}
            type="button"
          >
            <ChevronLeft size={16} />
          </button>
          <button
            aria-label="下一个决策"
            className="icon-button"
            disabled={busy || !canGoNext}
            onClick={() => setPageIndex(activeIndex + 1)}
            type="button"
          >
            <ChevronRight size={16} />
          </button>
        </div>
      </div>
      <div className="decision-list">
        <article className="decision-card" key={activeDecision.id || activeDecision.question || activeIndex}>
          <div className="decision-card-head">
            <strong>{activeDecision.question}</strong>
            {recommendedOptionId ? <span>推荐 {recommendedOptionLetter || recommendedOptionId}</span> : null}
          </div>
          {activeDecision.evidence ? <p className="decision-evidence">{activeDecision.evidence}</p> : null}
          {activeDecision.impact ? <p className="decision-impact">{activeDecision.impact}</p> : null}
          <div className="decision-options">
            {(activeDecision.options || []).map((option, optionIndex) => {
              const selected = option.id === activeAnswer.optionId && !activeAnswer.customAnswer;
              const recommended = String(option.id || '').trim() === recommendedOptionId;
              return (
                <button
                  className={['tool-button', selected ? 'accent' : '', recommended && !selected ? 'recommended' : ''].filter(Boolean).join(' ')}
                  disabled={busy || !String(activeDecision.id || '').trim()}
                  key={option.id}
                  onClick={() => setActiveAnswer({ optionId: option.id, customAnswer: '' })}
                  title={option.label}
                  type="button"
                >
                  <span className="decision-option-letter">{coCreateDecisionOptionLetter(optionIndex)}</span>
                  <span className="decision-option-label">{option.label}</span>
                  {recommended ? <span className="decision-option-recommended">推荐</span> : null}
                </button>
              );
            })}
            <button
              className={`tool-button ${selectedSkip ? 'accent' : ''}`}
              disabled={busy || !String(activeDecision.id || '').trim()}
              onClick={() => setActiveAnswer({ optionId: CO_CREATE_DECISION_SKIP_OPTION_ID, customAnswer: '' })}
              type="button"
            >
              <span className="decision-option-letter">{coCreateDecisionOptionLetter((activeDecision.options || []).length)}</span>
              <span className="decision-option-label">跳过，不修改</span>
            </button>
          </div>
          <div className="decision-custom">
            <textarea
              disabled={busy || !String(activeDecision.id || '').trim()}
              placeholder="输入自定义处理方式..."
              value={customValue}
              onChange={(event) => setAnswers((previous) => ({
                ...previous,
                [activeKey]: { optionId: '', customAnswer: event.target.value }
              }))}
            />
          </div>
        </article>
      </div>
      {activeIndex === decisionCount - 1 ? (
        <button
          className="tool-button accent full-width"
          disabled={busy || !canSubmit}
          onClick={() => onResolve(payload)}
          type="button"
        >
          <Send size={15} />
          提交决策
        </button>
      ) : null}
    </div>
  );
}

function ChapterRow({ item, granularity }) {
  const scenes = item.scenes || [];
  const budget = item.wordBudget;
  const coverage = item.sourceCoverage;
  const budgetLabel = budget?.targetRunes
    ? `${formatCompact(budget.targetRunes)} 字`
    : budget?.targetWords ? `${formatCompact(budget.targetWords)} 字` : '';
  const rangeLabel = budget?.minRunes && budget?.maxRunes
    ? `${formatCompact(budget.minRunes)}-${formatCompact(budget.maxRunes)}`
    : budget?.minWords && budget?.maxWords ? `${formatCompact(budget.minWords)}-${formatCompact(budget.maxWords)}` : '';
  const sourceLabel = formatAdaptationSourceCoverageLabel(coverage, granularity);
  return (
    <div className="chapter-row">
      <span>{item.chapter || '-'}</span>
      <div className="chapter-row-main">
        <strong>{item.title || '未命名章节'}</strong>
        {item.coreEvent ? <small>{item.coreEvent}</small> : null}
        {item.hook ? <small>钩子：{item.hook}</small> : null}
        <div className="chapter-meta-line">
          {sourceLabel ? <em>{sourceLabel}</em> : null}
          {budgetLabel ? <em>预算 {budgetLabel}{rangeLabel ? ` (${rangeLabel})` : ''}</em> : null}
          {item.writtenWordCount > 0 ? <em>已写 {formatCompact(item.writtenWordCount)}</em> : null}
          {scenes.length ? <em>{scenes.length} 场</em> : null}
        </div>
        {scenes.length ? <p>{scenes.join(' / ')}</p> : null}
      </div>
    </div>
  );
}

function OutlineStructureTree({ structure, granularity, emptyText = '暂无章节' }) {
  const groups = structure?.groups || [];
  const chapters = structure?.chapters || [];
  const hasVolumes = Boolean(structure?.hasVolumes);
  const [expandedByKey, setExpandedByKey] = useState({});
  const volumeKeys = hasVolumes ? groups.map((group) => String(group.key)).join('|') : '';

  useEffect(() => {
    if (!hasVolumes) {
      setExpandedByKey({});
      return;
    }
    const currentKeys = new Set(volumeKeys ? volumeKeys.split('|') : []);
    setExpandedByKey((previous) => {
      const next = {};
      for (const [key, expanded] of Object.entries(previous)) {
        if (expanded && currentKeys.has(key)) {
          next[key] = true;
        }
      }
      return Object.keys(next).length === Object.keys(previous).length ? previous : next;
    });
  }, [hasVolumes, volumeKeys]);

  if (!chapters.length && !groups.length) {
    return <div className="empty-state">{emptyText}</div>;
  }

  if (!hasVolumes) {
    return (
      <div className="chapter-list outline-flat-list">
        {chapters.map((item) => (
          <ChapterRow item={item} granularity={granularity} key={`${item.chapter}-${item.title}`} />
        ))}
      </div>
    );
  }

  return (
    <div className="outline-volume-tree">
      {groups.map((group) => {
        const key = String(group.key);
        const expanded = Boolean(expandedByKey[key]);
        const rangeLabel = outlineGroupRangeLabel(group);
        const chapterCount = outlineGroupChapterCount(group);
        return (
          <article className="outline-volume-block" key={`outline-volume-${key}`}>
            <button
              aria-expanded={expanded}
              className="outline-volume-toggle"
              onClick={() => setExpandedByKey((previous) => ({ ...previous, [key]: !previous[key] }))}
              title={expanded ? '收起章节' : '展开章节'}
              type="button"
            >
              <ChevronRight className={expanded ? 'outline-chevron expanded' : 'outline-chevron'} size={16} />
              <span className="outline-volume-title">
                <strong>{group.title}</strong>
                <small>{[rangeLabel, group.theme].filter(Boolean).join(' / ')}</small>
              </span>
              <span className="outline-volume-count">{chapterCount ? `${chapterCount} 章` : '待展开'}</span>
            </button>
            {group.summary ? <p className="outline-volume-summary">{group.summary}</p> : null}
            {expanded ? (
              <div className="outline-volume-chapters">
                {group.chapters.length ? (
                  group.chapters.map((chapter) => (
                    <ChapterRow
                      granularity={granularity}
                      item={chapter}
                      key={`outline-volume-${key}-chapter-${chapter.chapter}-${chapter.title}`}
                    />
                  ))
                ) : (
                  <div className="empty-state compact">暂无章节细纲</div>
                )}
              </div>
            ) : null}
          </article>
        );
      })}
    </div>
  );
}

function ProposalOutlineTree({ proposal }) {
  const structure = outlineStructureFromParts(proposal?.chapters || [], proposal?.volumes || []);
  return (
    <OutlineStructureTree
      emptyText="暂无章节细纲"
      granularity={proposal?.granularity}
      structure={structure}
    />
  );
}

function ContinuationWorkspace({ continuation }) {
  const proposal = continuation.proposal || {};
  const reviewKind = continuationReviewKind(continuation);
  const title = reviewKind === 'volumes'
    ? '续写分卷规划'
    : reviewKind === 'outlines'
      ? '续写章节细纲'
      : reviewKind === 'proposal'
        ? '续写提案'
        : '小说续写工作区';
  return (
    <div className="proposal-workspace continuation-workspace">
      <header className="proposal-workspace-header">
        <div>
          <div className="eyebrow">小说续写 · Revision {continuation.revision}</div>
          <h3>{title}</h3>
        </div>
        <div className="proposal-workspace-metrics">
          <span>原作 {continuation.baseChapterCount || '-'} 章</span>
          <span>{continuation.shortStory ? '短篇 / 不分卷' : continuation.structure === 'volumes' ? '长篇 / 分卷' : '结构待确认'}</span>
          {continuation.nextChapter ? <span>从第 {continuation.nextChapter} 章续写</span> : null}
        </div>
      </header>
      {continuation.draft ? (
        <section className="continuation-draft-card">
          <strong>已确认 Draft</strong>
          <p>{continuation.draft}</p>
        </section>
      ) : null}
      {proposal && Object.keys(proposal).length > 0 ? <ContinuationProposalSummary proposal={proposal} /> : null}
      {reviewKind === 'volumes' || continuation.volumes.length > 0 ? (
        <ContinuationVolumeList volumes={continuation.volumes} />
      ) : null}
      {reviewKind === 'outlines' || continuation.outlines.length > 0 ? (
        <ContinuationOutlineList outlines={continuation.outlines} />
      ) : null}
      {!continuation.draft && !continuation.proposal && continuation.volumes.length === 0 && continuation.outlines.length === 0 ? (
        <div className="empty-state">原作已建立基线。下一步先通过共创确定续写 Draft。</div>
      ) : null}
    </div>
  );
}

function ContinuationProposalSummary({ proposal }) {
  const summary = textValue(proposal, 'summary', 'Summary');
  const direction = textValue(proposal, 'direction', 'Direction');
  const targetChapters = Number(proposal.target_chapter_count || proposal.targetChapterCount || proposal.TargetChapterCount || 0);
  const targetRunes = Number(proposal.target_total_runes || proposal.targetTotalRunes || proposal.TargetTotalRunes || 0);
  const notes = arrayValue(proposal, 'notes', 'Notes');
  return (
    <section className="continuation-proposal-summary">
      <div className="proposal-workspace-metrics">
        {targetChapters > 0 ? <span>计划续写 {targetChapters} 章</span> : null}
        {targetRunes > 0 ? <span>目标 {formatCompact(targetRunes)} 字</span> : null}
      </div>
      {summary ? <p><b>核心提案</b>{summary}</p> : null}
      {direction ? <p><b>续写方向</b>{direction}</p> : null}
      {notes.length ? <ProposalChipList label="规划约束" values={notes} /> : null}
    </section>
  );
}

function ContinuationVolumeList({ volumes }) {
  if (!volumes.length) {
    return <div className="empty-state">分卷规划生成中</div>;
  }
  return (
    <div className="proposal-volume-stack">
      {volumes.map((volume, volumePosition) => {
        const index = Number(volume.index || volume.Index || volumePosition + 1);
        const arcs = arrayValue(volume, 'arcs', 'Arcs');
        return (
          <section className="proposal-volume-block volume-review-block" key={`continuation-volume-${index}`}>
            <div className="proposal-volume-head">
              <div>
                <strong>{textValue(volume, 'title', 'Title') || `第 ${index} 卷`}</strong>
                <span>{arcs.length ? `${arcs.length} 个故事弧` : '等待故事弧规划'}</span>
              </div>
              {textValue(volume, 'theme', 'Theme') ? <p>{textValue(volume, 'theme', 'Theme')}</p> : null}
            </div>
            {arcs.length ? (
              <div className="continuation-arc-grid">
                {arcs.map((arc, arcPosition) => (
                  <article key={`continuation-arc-${index}-${arc.index || arc.Index || arcPosition}`}>
                    <strong>{textValue(arc, 'title', 'Title') || `故事弧 ${arcPosition + 1}`}</strong>
                    {textValue(arc, 'goal', 'Goal') ? <p>{textValue(arc, 'goal', 'Goal')}</p> : null}
                    <span>预计 {arc.estimated_chapters || arc.estimatedChapters || arc.EstimatedChapters || '-'} 章</span>
                  </article>
                ))}
              </div>
            ) : null}
          </section>
        );
      })}
    </div>
  );
}

function ContinuationOutlineList({ outlines }) {
  if (!outlines.length) {
    return <div className="empty-state">章节细纲生成中</div>;
  }
  return (
    <div className="proposal-chapter-grid continuation-outline-grid">
      {outlines.map((outline, position) => {
        const chapter = Number(outline.chapter || outline.Chapter || outline.index || outline.Index || position + 1);
        const scenes = arrayValue(outline, 'scenes', 'Scenes');
        return (
          <article className="proposal-chapter-card" key={`continuation-outline-${chapter}-${position}`}>
            <div className="proposal-chapter-title">
              <span>{chapter}</span>
              <strong>{textValue(outline, 'title', 'Title') || '未命名章节'}</strong>
            </div>
            {textValue(outline, 'summary', 'Summary', 'goal', 'Goal', 'core_event', 'coreEvent', 'CoreEvent') ? (
              <p><b>本章目标</b>{textValue(outline, 'summary', 'Summary', 'goal', 'Goal', 'core_event', 'coreEvent', 'CoreEvent')}</p>
            ) : null}
            {textValue(outline, 'hook', 'Hook') ? <p><b>章末钩子</b>{textValue(outline, 'hook', 'Hook')}</p> : null}
            {scenes.length ? <p><b>场景</b>{scenes.map((scene) => typeof scene === 'string' ? scene : textValue(scene, 'summary', 'Summary', 'location', 'Location')).filter(Boolean).join(' / ')}</p> : null}
          </article>
        );
      })}
    </div>
  );
}

function AdaptationProposalWorkspace({ proposal }) {
  if (proposal.volumeReviewReady) {
    return <AdaptationVolumeReviewWorkspace proposal={proposal} />;
  }
  const groups = proposalVolumeGroups(proposal);
  return (
    <div className="proposal-workspace">
      <header className="proposal-workspace-header">
        <div>
          <div className="eyebrow">改编提案</div>
          <h3>{proposal.chapterCount} 章细纲</h3>
        </div>
        <div className="proposal-workspace-metrics">
          <span>{proposal.granularity || '-'}</span>
          <span>{proposal.rewritePolicy || '-'}</span>
          <span>{proposal.volumes.length ? `${proposal.volumes.length} 卷` : '未分卷'}</span>
        </div>
      </header>
      {proposal.brief ? <p className="proposal-brief">{proposal.brief}</p> : null}
      {proposal.rules.length ? (
        <div className="proposal-rule-list">
          {proposal.rules.slice(0, 6).map((rule, index) => <span key={`proposal-workspace-rule-${index}`}>{rule}</span>)}
        </div>
      ) : null}
      <div className="proposal-volume-stack">
        {groups.map((group) => (
          <section className="proposal-volume-block" key={`proposal-volume-${group.key}`}>
            <div className="proposal-volume-head">
              <div>
                <strong>{group.title}</strong>
                <span>第 {group.from}-{group.to} 章</span>
              </div>
              {group.theme ? <p>{group.theme}</p> : null}
              {group.summary ? <p>{group.summary}</p> : null}
            </div>
            <div className="proposal-chapter-grid">
              {group.chapters.map((chapter) => (
                <ProposalChapterCard chapter={chapter} granularity={proposal.granularity} key={`proposal-card-${chapter.chapter}-${chapter.title}`} />
              ))}
            </div>
          </section>
        ))}
      </div>
    </div>
  );
}

function AdaptationVolumeReviewWorkspace({ proposal }) {
  const review = proposal.volumeReview || {};
  const volumes = review.volumes || [];
  return (
    <div className="proposal-workspace">
      <header className="proposal-workspace-header">
        <div>
          <div className="eyebrow">改编提案</div>
          <h3>{volumes.length || proposal.volumes.length} 卷剧情审阅</h3>
        </div>
        <div className="proposal-workspace-metrics">
          <span>{proposal.granularity || '-'}</span>
          <span>{proposal.rewritePolicy || '-'}</span>
          <span>{volumes.length ? `${volumes.length} 卷` : '未分卷'}</span>
        </div>
      </header>
      {proposal.brief ? <p className="proposal-brief">{proposal.brief}</p> : null}
      <div className="proposal-volume-stack">
        {volumes.map((volume) => {
          const sourceLabel = formatAdaptationVolumeSourceLabel(volume, proposal.granularity);
          return (
            <section className="proposal-volume-block volume-review-block" key={`volume-review-${volume.index}`}>
              <div className="proposal-volume-head">
                <div>
                  <strong>{volume.title || `第 ${volume.index} 卷`}</strong>
                  <span>第 {volume.targetFrom || '?'}-{volume.targetTo || '?'} 章</span>
                </div>
                {volume.theme ? <p>{volume.theme}</p> : null}
                {volume.summary ? <p>{volume.summary}</p> : null}
              </div>
              <div className="volume-review-detail-grid">
                {sourceLabel ? <p><b>原作范围</b>{sourceLabel}</p> : null}
                {volume.plot ? <p><b>剧情走向</b>{volume.plot}</p> : null}
                {volume.goal ? <p><b>改编目标</b>{volume.goal}</p> : null}
                {volume.beats.length ? <ProposalChipList label="关键节点" values={volume.beats} /> : null}
              </div>
            </section>
          );
        })}
      </div>
    </div>
  );
}

function ProposalChapterCard({ chapter, granularity }) {
  const scenes = chapter.scenes || [];
  const preserve = chapter.preserveEvents || [];
  const required = chapter.requiredChanges || [];
  const forbidden = chapter.forbiddenMoves || [];
  const budget = chapter.wordBudget;
  const coverage = chapter.sourceCoverage;
  const budgetText = budget?.targetRunes
    ? `${formatCompact(budget.targetRunes)} 字`
    : budget?.targetWords ? `${formatCompact(budget.targetWords)} 字` : '';
  const sourceText = formatAdaptationSourceCoverageLabel(coverage, granularity, { addedLabel: '新增桥段' });
  return (
    <article className="proposal-chapter-card">
      <div className="proposal-chapter-title">
        <span>{chapter.chapter || '-'}</span>
        <strong>{chapter.title || '未命名章节'}</strong>
      </div>
      <div className="chapter-meta-line">
        {sourceText ? <em>{sourceText}</em> : null}
        {budgetText ? <em>预算 {budgetText}</em> : null}
        {scenes.length ? <em>{scenes.length} 场</em> : null}
      </div>
      {chapter.coreEvent ? <p><b>核心事件</b>{chapter.coreEvent}</p> : null}
      {chapter.hook ? <p><b>钩子</b>{chapter.hook}</p> : null}
      {scenes.length ? <p><b>场景</b>{scenes.join(' / ')}</p> : null}
      {preserve.length ? <ProposalChipList label="保留" values={preserve} /> : null}
      {required.length ? <ProposalChipList label="改动" values={required} /> : null}
      {forbidden.length ? <ProposalChipList label="禁区" values={forbidden} /> : null}
    </article>
  );
}

function ProposalChipList({ label, values }) {
  return (
    <div className="proposal-chip-list">
      <b>{label}</b>
      {values.slice(0, 4).map((value, index) => <span key={`${label}-${index}`}>{value}</span>)}
    </div>
  );
}

function VolumeReviewControls({ adaptation, busy, proposal, setAdaptation, onRevise }) {
  const volumes = proposal.volumeReview?.volumes || [];
  const disabled = busy || !volumes.length;
  const selected = clampVolumeSelection(adaptation.revisionVolume, volumes.length);
  const instruction = String(adaptation.revisionInstruction || '');
  const canSubmit = !disabled && instruction.trim();
  const update = (changes) => setAdaptation((previous) => ({ ...previous, ...changes, revisionStatus: 'idle', revisionMessage: '', error: '' }));
  return (
    <section className="simulation-section proposal-revision-section">
      <div className="section-title">
        <Pencil size={17} />
        <span>修改分卷剧情</span>
      </div>
      <label className="field-label proposal-select-line">
        <span>卷</span>
        <select
          disabled={disabled}
          value={selected}
          onChange={(event) => update({ revisionVolume: event.target.value })}
        >
          {volumes.map((volume) => (
            <option key={`volume-review-option-${volume.index}`} value={volume.index}>
              第 {volume.index} 卷：{volume.title}
            </option>
          ))}
        </select>
      </label>
      <label className="field-label proposal-select-line">
        <span>修改意见</span>
        <textarea
          className="proposal-revision-textarea"
          disabled={busy}
          placeholder="写下这一卷需要调整的剧情、人物关系、节奏或新增桥段..."
          value={instruction}
          onChange={(event) => update({ revisionInstruction: event.target.value })}
        />
      </label>
      <button className="tool-button accent full-width" disabled={!canSubmit} onClick={() => runWithWindowScrollPreserved(onRevise)} type="button">
        <WandSparkles size={16} />
        修订分卷
      </button>
    </section>
  );
}

function ProposalRevisionControls({ adaptation, busy, proposal, setAdaptation, onRevise }) {
  const chapterOptions = Array.from({ length: Math.max(1, proposal.chapterCount) }, (_, index) => index + 1);
  const volumeOptions = proposal.volumes.length ? proposal.volumes : [];
  const mode = adaptation.revisionMode || 'chapter';
  const disabled = busy || proposal.chapterCount <= 0;
  const instruction = String(adaptation.revisionInstruction || '');
  const canSubmit = !disabled && instruction.trim();
  const update = (changes) => setAdaptation((previous) => ({ ...previous, ...changes, revisionStatus: 'idle', revisionMessage: '', error: '' }));
  return (
    <section className="simulation-section proposal-revision-section">
      <div className="section-title">
        <Pencil size={17} />
        <span>修改提案</span>
      </div>
      <div className="proposal-revision-mode-grid" role="radiogroup" aria-label="提案修改模式">
        {[
          ['chapter', '单章'],
          ['range', '多章'],
          ['volume', '整卷']
        ].map(([value, label]) => (
          <button
            className={mode === value ? 'revision-mode-button active' : 'revision-mode-button'}
            disabled={busy}
            key={value}
            onClick={() => update({ revisionMode: value })}
            type="button"
          >
            {label}
          </button>
        ))}
      </div>
      {mode === 'chapter' ? (
        <label className="field-label proposal-select-line">
          <span>章节</span>
          <select
            disabled={disabled}
            value={clampChapterSelection(adaptation.revisionChapter, proposal.chapterCount)}
            onChange={(event) => update({ revisionChapter: event.target.value })}
          >
            {chapterOptions.map((chapter) => <option key={`rev-ch-${chapter}`} value={chapter}>第 {chapter} 章</option>)}
          </select>
        </label>
      ) : null}
      {mode === 'range' ? (
        <>
          <label className="field-label proposal-select-line">
            <span>首章</span>
            <select
              disabled={disabled}
              value={clampChapterSelection(adaptation.revisionFromChapter, proposal.chapterCount)}
              onChange={(event) => update({ revisionFromChapter: event.target.value })}
            >
              {chapterOptions.map((chapter) => <option key={`rev-from-${chapter}`} value={chapter}>第 {chapter} 章</option>)}
            </select>
          </label>
          <label className="field-label proposal-select-line">
            <span>尾章</span>
            <select
              disabled={disabled}
              value={clampChapterSelection(adaptation.revisionToChapter, proposal.chapterCount)}
              onChange={(event) => update({ revisionToChapter: event.target.value })}
            >
              {chapterOptions.map((chapter) => <option key={`rev-to-${chapter}`} value={chapter}>第 {chapter} 章</option>)}
            </select>
          </label>
        </>
      ) : null}
      {mode === 'volume' ? (
        <label className="field-label proposal-select-line">
          <span>卷</span>
          <select
            disabled={disabled}
            value={volumeOptions.length ? clampVolumeSelection(adaptation.revisionVolume, volumeOptions.length) : 'all'}
            onChange={(event) => update({ revisionVolume: event.target.value })}
          >
            <option value="all">全卷</option>
            {volumeOptions.map((volume) => (
              <option key={`rev-vol-${volume.index}`} value={volume.index}>
                第 {volume.index} 卷：{volume.title}
              </option>
            ))}
          </select>
        </label>
      ) : null}
      <label className="field-label proposal-select-line">
        <span>修改意见</span>
        <textarea
          className="proposal-revision-textarea"
          disabled={busy}
          placeholder="写下这次要调整的剧情、节奏、伏笔或人物关系..."
          value={instruction}
          onChange={(event) => update({ revisionInstruction: event.target.value })}
        />
      </label>
      <button className="tool-button accent full-width" disabled={!canSubmit} onClick={() => runWithWindowScrollPreserved(onRevise)} type="button">
        <WandSparkles size={16} />
        修订提案
      </button>
    </section>
  );
}

function AdaptationAuditPanel({
  activeProject,
  audit,
  busy,
  coCreateActive,
  modelConfig,
  projectRunning,
  setAudit,
  onApply,
  onCancelSemantic,
  onCompare,
  onEstimateSemantic,
  onReload,
  onRetrySemantic,
  onRun,
  onSelectRun,
  onStartSemantic
}) {
  const report = normalizedAdaptationAuditReport(audit.report);
  const findings = report?.findings || [];
  const metrics = report?.metrics || {};
  const confirmation = report?.confirmation || {};
  const application = audit.application;
  const actionBusy = busy || audit.status === 'running' || audit.status === 'loading';
  const blocked = projectRunning || coCreateActive;
  const canRun = Boolean(activeProject?.id) && !actionBusy && !blocked;
  const applyRequest = buildAdaptationAuditApplyRequest(report, audit.acknowledged);
  const latestRunId = audit.runs?.[0]?.run_id || '';
  const selectedRunIsCurrent = !audit.selectedRunId || !latestRunId || audit.selectedRunId === latestRunId;
  const canApply = !actionBusy && !blocked && selectedRunIsCurrent && applyRequest.ok;
  const modeLabel = {
    chapter: 'Chapter：保留细节与分段覆盖',
    arc: 'Arc：保留主线，可合并支线',
    free: 'Free：校验目标故事自洽'
  }[String(report?.mode || '').toLowerCase()] || '等待审计结果';
  const sourceChapters = normalizedAuditSourceChapters(audit.sourceChapters);
  const maximumScope = audit.auditableScope || report?.scope || {};
  const updateScope = (field, value) => setAudit((previous) => ({
    ...previous,
    [field]: value,
    report: null,
    application: null,
    acknowledged: false,
    status: 'idle',
    message: '',
    error: ''
  }));

  return (
    <div className="side-content adaptation-audit-panel">
      {audit.error ? <div className="error-banner compact">{audit.error}</div> : null}

      <section className="simulation-section">
        <div className="section-title">
          <TestTube2 size={17} />
          <span>改编质量审计</span>
        </div>
        <p className="audit-intro">
          逐章核对来源事件、分卷主线承诺、完整章节细纲与已写正文。审计本身只读，不会自动暂停、恢复或改写正文。
        </p>
        {projectRunning ? <div className="settings-note warning">项目正在运行。请先点击顶部“暂停”，再运行审计或应用修复计划。</div> : null}
        {coCreateActive ? <div className="settings-note warning">共创流程正在进行。请先结束或取消共创，再运行审计或应用修复计划。</div> : null}
      </section>

      <section className="simulation-section">
        <div className="section-title">
          <BookOpen size={17} />
          <span>审计范围</span>
        </div>
        <div className="field-grid audit-scope-grid">
          <label className="field-label">
            <span>原著结束章</span>
            <input inputMode="numeric" list="adaptation-audit-source-chapters" min="1" onChange={(event) => updateScope('sourceTo', event.target.value)} placeholder="自动取最近完整范围" type="number" value={audit.sourceTo} />
            <datalist id="adaptation-audit-source-chapters">
              {sourceChapters.map((chapter) => <option key={chapter.chapter} label={`第 ${chapter.chapter} 章 ${chapter.title || '（无标题）'}`} value={chapter.chapter} />)}
            </datalist>
          </label>
        </div>
        <div className="audit-scope-help">
          留空会自动审计到最近完整改编边界；选择尚未完成的章节也会自动回退。Chapter 模式会排除 1:N 拆分的中间态，Arc 模式按完整批次向下取整。
          {maximumScope.source_to ? ` 当前最多可审：${adaptationAuditScopeText(maximumScope)}。` : ''}
        </div>
        <div className="project-settings-actions">
          <button className="tool-button" disabled={!activeProject?.id || actionBusy} onClick={onReload} type="button">
            <RefreshCw size={16} />
            查看最近报告
          </button>
          <button className="tool-button accent" disabled={!canRun} onClick={onRun} type="button">
            <TestTube2 size={16} />
            生成只读审计报告
          </button>
        </div>
      </section>

      <section className="simulation-section audit-result-section">
        <div className="section-title">
          <FileText size={17} />
          <span>审计结果</span>
        </div>
        {!report ? (
          <div className="empty-state">尚未生成报告。新项目在细纲生成时已有模式化门禁；这里用于查看完整报告和审计既有章节。</div>
        ) : (
          <>
            <div className={`workflow-status ${report.status === 'pass' ? 'done' : report.status === 'inconclusive' ? 'idle' : 'error'}`}>
              <strong>{report.status === 'pass' ? '通过' : report.status === 'inconclusive' ? '证据不足，未下结论' : '发现阻塞问题'}</strong>
              <span>{modeLabel} · {report.read_only ? '只读报告' : '报告'}</span>
            </div>
            <div className="audit-report-meta">
              <span>{adaptationAuditScopeText(report.scope)}</span>
              <span>{findings.length} 项发现 / {confirmation.blocking_finding_ids.length} 项阻塞</span>
            </div>
            <section className="metric-grid audit-metrics">
              <Metric label="必需事件" value={`${metrics.bound_events || 0}/${metrics.required_events || 0}`} />
              <Metric label="来源分段" value={`${metrics.covered_segments || 0}/${metrics.source_segments || 0}`} />
              <Metric label="有效证据" value={metrics.valid_evidence_items || 0} />
              <Metric label="事件总数" value={metrics.events || 0} />
            </section>
            {findings.length ? (
              <div className="audit-finding-list">
                {findings.map((finding, index) => (
                  <div className={`finding-row audit-finding-row ${String(finding.severity || '').toLowerCase()}`} key={finding.id || `${finding.code}-${index}`}>
                    <strong>{finding.code || 'adaptation_finding'}{finding.blocking ? ' · 阻塞' : ''}</strong>
                    <span>{finding.event_id ? `事件：${finding.event_id}` : finding.segment_id ? `分段：${finding.segment_id}` : '章节契约'}</span>
                    {Array.isArray(finding.target_chapters) && finding.target_chapters.length ? <span>目标章：{formatAuditChapterList(finding.target_chapters)}</span> : null}
                    <small>{finding.message || '没有附加说明。'}</small>
                  </div>
                ))}
              </div>
            ) : <div className="success-note">没有发现需要应用的改编修复。</div>}
          </>
        )}
      </section>

      {report?.confirmation?.required ? (
        <section className="simulation-section audit-apply-section">
          <div className="section-title">
            <Check size={17} />
            <span>确认应用修复计划</span>
          </div>
          <div className="audit-apply-help">{confirmation.suggested_action || '应用后会创建备份、修订计划，并把受影响的既有章节排入返工队列。不会立即改写正文。'}</div>
          <label className="checkbox-row audit-confirmation-row">
            <input checked={audit.acknowledged} disabled={actionBusy || blocked} onChange={(event) => setAudit((previous) => ({ ...previous, acknowledged: event.target.checked, error: '' }))} type="checkbox" />
            <span>我已了解全部 {confirmation.blocking_finding_ids.length} 个阻塞问题，以及应用后仍需手动点击顶部“恢复”执行返工。</span>
          </label>
          <button className="tool-button accent full-width" disabled={!canApply} onClick={onApply} type="button">
            <Check size={16} />
            确认应用修复计划
          </button>
        </section>
      ) : null}

      {application ? (
        <section className="simulation-section audit-application-section">
          <div className="section-title">
            <Check size={17} />
            <span>修复计划已应用</span>
          </div>
          <div className="audit-application-summary">
            <span>状态：{application.status || 'applied'}</span>
            <span>影响章节：{formatAuditChapterList(application.affected_chapters)}</span>
            <span>返工队列：{formatAuditChapterList(application.queued_chapters)}</span>
          </div>
          <div className="settings-note">{adaptationAuditApplicationText(application)}</div>
        </section>
      ) : null}

      <AuditRunWorkbench
        audit={audit}
        disabled={actionBusy || blocked}
        providers={modelConfig?.providers || []}
        onCancel={onCancelSemantic}
        onCompare={onCompare}
        onEstimate={onEstimateSemantic}
        onRetry={onRetrySemantic}
        onSelect={onSelectRun}
        onStart={onStartSemantic}
        setAudit={setAudit}
      />

      {audit.message ? <div className="success-note">{audit.message}</div> : null}
    </div>
  );
}

function formatAuditChapterList(chapters) {
  if (!Array.isArray(chapters) || chapters.length === 0) {
    return '无';
  }
  return chapters.join('、');
}

function AdaptationPanel({
  activeProject,
  busy,
  snapshot,
  adaptation,
  simulation,
  setAdaptation,
  onUploadSource,
  onAnalyze,
  onStart,
  onRevise,
  onConfirm,
  onRefreshLibrary,
  onLoadLibrary,
  onSaveNovel,
  onCoCreate
}) {
  const latestAnalysis = latestSimulationEvent(adaptation.analysisEvents);
  const analyzed = adaptation.analysisStatus === 'done';
  const proposal = getVisibleAdaptationProposalReview(snapshot, adaptation);
  const libraryBusy = adaptation.libraryStatus === 'running';
  const simulationProfileBusy = isSimulationProfileActionBusy(simulation);
  const workflowBusy = busy || simulationProfileBusy;
  const uploadBusy = adaptation.uploadStatus === 'running';
  const canAnalyze = canRunAdaptationAnalysis({ activeProject, busy, adaptation });
  const analysisDisabledReason = adaptationAnalysisDisabledReason({ activeProject, busy, adaptation });
  const canCoCreate = Boolean(activeProject && adaptation.sourceFile?.relative_path && analyzed && !workflowBusy);
  const canStart = Boolean(activeProject && adaptation.sourceFile?.relative_path && analyzed && !workflowBusy && adaptation.brief.trim());
  const canConfirm = Boolean(activeProject && !workflowBusy && proposal.proposalReady && isAdaptationProposalCurrent(adaptation));
  const canSaveNovel = canSaveAnalyzedNovelToLibrary({ activeProject, busy, adaptation });
  const isVolumeReview = proposal.volumeReviewReady;
  const updateProposalInput = (changes, options = {}) => {
    const scrollPosition = options.preserveWindowScroll ? readWindowScrollPosition() : null;
    setAdaptation((previous) => ({
      ...previous,
      ...changes,
      proposalKey: '',
      startStatus: 'idle',
      startMessage: ''
    }));
    restoreWindowScrollPosition(scrollPosition);
  };
  if (proposal.proposalReady) {
    return (
      <div className="side-content proposal-side-panel">
        {adaptation.error ? <div className="error-banner compact">{adaptation.error}</div> : null}
        <section className="simulation-section proposal-control-section">
          <div className="section-title">
            <FileText size={17} />
            <span>提案审稿</span>
          </div>
          <div className="proposal-side-summary">
            <strong>待确认</strong>
            <span>{proposal.chapterCount} 章 / {proposal.granularity || '-'} / {proposal.rewritePolicy || '-'}</span>
            {proposal.volumes.length ? <span>{proposal.volumes.length} 卷</span> : null}
          </div>
        </section>
        {proposal.chapters.length ? (
          <section className="simulation-section proposal-outline-section">
            <div className="section-title">
              <BookOpen size={17} />
              <span>章节结构</span>
            </div>
            <ProposalOutlineTree proposal={proposal} />
          </section>
        ) : null}
        {isVolumeReview ? (
          <VolumeReviewControls
            adaptation={adaptation}
            busy={workflowBusy}
            proposal={proposal}
            setAdaptation={setAdaptation}
            onRevise={onRevise}
          />
        ) : (
          <ProposalRevisionControls
            adaptation={adaptation}
            busy={workflowBusy}
            proposal={proposal}
            setAdaptation={setAdaptation}
            onRevise={onRevise}
          />
        )}
        <section className="simulation-section proposal-confirm-section">
          <button className="tool-button accent full-width" disabled={!canConfirm} onClick={() => runWithWindowScrollPreserved(onConfirm)} type="button">
            <Check size={16} />
            {isVolumeReview ? '生成章节细纲' : '确认并启动'}
          </button>
          <div className={`workflow-status ${adaptation.revisionStatus || adaptation.startStatus}`}>
            <strong>{workflowStatusText(adaptation.revisionStatus !== 'idle' ? adaptation.revisionStatus : adaptation.startStatus)}</strong>
            <span>{adaptation.revisionMessage || adaptation.startMessage || '等待审稿'}</span>
          </div>
        </section>
      </div>
    );
  }
  return (
    <div className="side-content">
      {adaptation.error ? <div className="error-banner compact">{adaptation.error}</div> : null}

      <section className="simulation-section">
        <div className="section-title">
          <Database size={17} />
          <span>小说仓库</span>
        </div>
        <LibrarySearch
          disabled={busy || libraryBusy}
          label="搜索小说仓库"
          placeholder="搜索已分析小说"
          query={adaptation.libraryQuery}
          onQueryChange={(value) => setAdaptation((previous) => ({ ...previous, libraryQuery: value }))}
          onRefresh={onRefreshLibrary}
        />
        <LibraryList
          canLoad={Boolean(activeProject && !busy && !libraryBusy && !simulationProfileBusy)}
          emptyText="暂无小说条目"
          items={adaptation.libraryItems}
          loading={libraryBusy}
          onLoad={onLoadLibrary}
        />
        <LibraryFeedback error={adaptation.libraryError} message={adaptation.libraryMessage} />
      </section>

      <section className="simulation-section">
        <div className="section-title">
          <Upload size={17} />
          <span>小说改编</span>
        </div>
        <div className="simulation-actions">
          <label className={`tool-button file-picker full ${!activeProject || uploadBusy ? 'disabled' : ''}`}>
            <Upload size={16} />
            上传原文
            <input
              accept=".txt,.md,.markdown,text/plain,text/markdown"
              disabled={!activeProject || uploadBusy}
              onChange={onUploadSource}
              type="file"
            />
          </label>
        </div>
        {adaptation.uploadMessage ? <div className="success-note">{adaptation.uploadMessage}</div> : null}
        <div className="file-list">
          {adaptation.sourceFile ? (
            <div className="file-row">
              <span>{adaptation.sourceFile.name}</span>
              <strong>{formatBytes(adaptation.sourceFile.size)}</strong>
            </div>
          ) : (
            <div className="empty-state">暂无上传原文</div>
          )}
        </div>
      </section>

      <section className="simulation-section">
        <div className="section-title">
          <WandSparkles size={17} />
          <span>原文分析</span>
        </div>
        <button
          className="tool-button accent full-width"
          disabled={!canAnalyze}
          onClick={() => onAnalyze({ force: analyzed })}
          title={analysisDisabledReason}
          type="button"
        >
          <WandSparkles size={16} />
          {analyzed ? '增量重新分析并升级' : '分析'}
        </button>
        <div className={`workflow-status ${adaptation.analysisStatus}`}>
          <strong>{workflowStatusText(adaptation.analysisStatus)}</strong>
          <span>{latestAnalysis?.message || '等待分析'}</span>
        </div>
        {analyzed ? (
          <LibrarySaveRow
            allowEmptyName={Boolean(adaptation.libraryLoadedName.trim())}
            canSave={canSaveNovel}
            error={adaptation.librarySaveError}
            nameLabel="小说仓库名称"
            name={adaptation.librarySaveName}
            placeholder="小说仓库名称"
            saveLabel="保存当前小说到库"
            saving={adaptation.librarySaveStatus === 'running'}
            onNameChange={(value) => setAdaptation((previous) => ({ ...previous, librarySaveName: value, librarySaveError: '' }))}
            onSave={onSaveNovel}
          />
        ) : null}
        <SimulationEventList events={adaptation.analysisEvents} />
      </section>

      {analyzed ? (
        <section className="simulation-section">
          <div className="section-title">
            <BookOpen size={17} />
            <span>模式与启动</span>
          </div>
          <div className="adapt-mode-grid" role="radiogroup" aria-label="改编模式">
            {adaptationModes.map((mode) => (
              <label className={adaptation.mode === mode.value ? 'adapt-mode active' : 'adapt-mode'} key={mode.value}>
                <input
                  checked={adaptation.mode === mode.value}
                  disabled={busy}
                  name="adapt-mode"
                  onChange={() => updateProposalInput({ mode: mode.value }, { preserveWindowScroll: true })}
                  type="radio"
                  value={mode.value}
                />
                <span>{mode.label}</span>
              </label>
            ))}
          </div>
          <textarea
            className="adapt-brief"
            aria-label="改编方向"
            disabled={busy}
            placeholder="改编方向..."
            value={adaptation.brief}
            onChange={(event) => updateProposalInput({ brief: event.target.value })}
          />
          <div className="adapt-action-help" role="note">
            <span><strong>生成提案</strong>：结局、关系线和改写重点已经明确时，直接拆成可确认的逐章策略。</span>
            <span><strong>进入共创</strong>：还在纠结结局、女主戏份、虐心/纯爱比重或悬疑侧重时，先让 AI 给选项再补充。</span>
          </div>
          <button className="tool-button accent full-width" disabled={!canStart} onClick={() => runWithWindowScrollPreserved(onStart)} type="button">
            <Play size={16} />
            生成提案
          </button>
          {proposal.loaded ? (
            <div className="proposal-review">
              <div className="section-title">
                <FileText size={17} />
                <span>提案确认</span>
              </div>
              <div className={`workflow-status ${proposal.proposalReady ? 'ready' : proposal.confirmed ? 'done' : 'idle'}`}>
                <strong>{proposal.confirmed ? '已确认' : proposal.proposalReady ? '待确认' : proposal.status || '等待提案'}</strong>
                <span>{proposal.chapterCount} 章 / {proposal.granularity || '-'} / {proposal.rewritePolicy || '-'}</span>
              </div>
              {proposal.brief ? <p>{proposal.brief}</p> : null}
              {proposal.rules.length ? (
                <ul>
                  {proposal.rules.slice(0, 4).map((rule, index) => <li key={`proposal-rule-${index}`}>{rule}</li>)}
                </ul>
              ) : null}
              <ProposalOutlineTree proposal={proposal} />
              <button className="tool-button accent full-width" disabled={!canConfirm} onClick={() => runWithWindowScrollPreserved(onConfirm)} type="button">
                <Check size={16} />
                确认并启动
              </button>
            </div>
          ) : null}
          <button className="tool-button full-width" disabled={!canCoCreate} onClick={() => runWithWindowScrollPreserved(onCoCreate)} type="button">
            <MessageSquareText size={16} />
            进入共创
          </button>
          <div className={`workflow-status ${adaptation.startStatus}`}>
            <strong>{workflowStatusText(adaptation.startStatus)}</strong>
            <span>{adaptation.startMessage || '等待启动'}</span>
          </div>
        </section>
      ) : null}
    </div>
  );
}

function SimulationPanel({
  activeProject,
  busy,
  snapshot,
  simulation,
  setSimulation,
  onUploadSources,
  onSearchSources,
  onDownloadSource,
  onAnalyze,
  onImportProfile,
  onRefreshLibrary,
  onUploadLibrary,
  onLoadLibrary,
  onSaveToLibrary
}) {
  const latestAnalysis = latestSimulationEvent(simulation.analysisEvents);
  const latestImport = latestSimulationEvent(simulation.importEvents);
  const profile = getSimulationProfileStatus(snapshot);
  const profileDisplay = simulationProfileDisplayState({
    profile,
    analysisStatus: simulation.analysisStatus,
    latestAnalysis
  });
  const libraryBusy = simulation.libraryStatus === 'running';
  const simulationProfileBusy = isSimulationProfileActionBusy(simulation);
  const simulationActionDisabled = simulationProfileBusy;
  const canSaveCurrentProfile = Boolean(activeProject && profile.loaded && !busy && !libraryBusy && !simulationProfileBusy);
  const canAnalyze = canRunSimulationAnalysis({ activeProject, busy, simulation });
  const canStartAnalysis = canRunSimulationPrimaryAnalysis({
    activeProject,
    profile,
    simulation
  });
  const analysisActionLabel = simulationAnalysisActionLabel({
    analysisStatus: simulation.analysisStatus,
    fileCount: simulation.files.length,
    profileLoaded: profile.loaded,
    scanAvailable: canStartAnalysis
  });
  const analysisNextStep = simulationAnalysisNextStep({
    analysisStatus: simulation.analysisStatus,
    fileCount: simulation.files.length,
    profileLoaded: profile.loaded,
    scanAvailable: canStartAnalysis
  });
  return (
    <>
      <div className="side-content">
        {simulation.error ? <div className="error-banner compact">{simulation.error}</div> : null}

      <section className="snapshot-summary-card">
        <div className="section-title">
          <FileJson size={17} />
          <span>当前画像</span>
        </div>
        <div className={`workflow-status profile-status ${profileDisplay.tone}`}>
          <strong>{profileDisplay.label}</strong>
          <span title={profileDisplay.summary}>{profileDisplay.summary}</span>
        </div>
        {profileDisplay.running && profile.loaded ? (
          <p className="simulation-running-note">正在生成新画像；为避免误判，分析完成前不展示上一版过期诊断。</p>
        ) : null}
        {profile.loaded && !profileDisplay.running ? (
          <SimulationDiagnostics profile={profile} disabled={!canAnalyze} onAnalyze={onAnalyze} />
        ) : null}
      </section>

      <section className="simulation-section">
        <div className="section-title">
          <Database size={17} />
          <span>仿写画像库</span>
        </div>
        <LibrarySearch
          disabled={busy || libraryBusy}
          label="搜索仿写画像库"
          placeholder="搜索画像名称"
          query={simulation.libraryQuery}
          onQueryChange={(value) => setSimulation((previous) => ({ ...previous, libraryQuery: value }))}
          onRefresh={onRefreshLibrary}
        />
        <label className={`tool-button file-picker full ${busy || libraryBusy ? 'disabled' : ''}`}>
          <Upload size={16} />
          上传 JSON 到库
          <input accept=".json,application/json" disabled={busy || libraryBusy} multiple onChange={onUploadLibrary} type="file" />
        </label>
        <LibrarySaveRow
          canSave={canSaveCurrentProfile}
          error={simulation.saveError}
          name={simulation.saveName}
          placeholder={profile.loaded ? '输入画像名称' : '当前项目暂无画像'}
          saving={simulation.saveStatus === 'running'}
          onNameChange={(value) => setSimulation((previous) => ({
            ...previous,
            saveName: value,
            saveError: '',
            saveStatus: previous.saveStatus === 'error' ? 'idle' : previous.saveStatus
          }))}
          onSave={onSaveToLibrary}
        />
        <LibraryList
          canLoad={Boolean(activeProject && !busy && !libraryBusy && !simulationProfileBusy)}
          emptyText="暂无画像条目"
          items={simulation.libraryItems}
          loading={libraryBusy}
          onLoad={onLoadLibrary}
        />
        <LibraryFeedback error={simulation.libraryError} message={simulation.libraryMessage} />
      </section>

      <section className="simulation-section">
        <div className="section-title">
          <FileJson size={17} />
          <span>加载画像</span>
        </div>
        <label className={`tool-button file-picker full ${!activeProject || simulationActionDisabled ? 'disabled' : ''}`}>
          <FileJson size={16} />
          上传 JSON
          <input accept=".json,application/json" disabled={!activeProject || simulationActionDisabled} onChange={onImportProfile} type="file" />
        </label>
        <div className={`workflow-status ${simulation.importStatus}`}>
          <strong>{workflowStatusText(simulation.importStatus)}</strong>
          <span>{simulation.importMessage || latestImport?.message || '等待导入'}</span>
        </div>
        <SimulationEventList events={simulation.importEvents} />
      </section>

      <section className="simulation-section">
        <div className="section-title">
          <Upload size={17} />
          <span>仿写画像</span>
        </div>
        <form className="library-search" onSubmit={(event) => { event.preventDefault(); onSearchSources(); }}>
          <label>
            <span>搜索下载语料</span>
            <input
              disabled={!activeProject || simulationActionDisabled || simulation.sourceSearchStatus === 'running'}
              onChange={(event) => setSimulation((previous) => ({
                ...previous,
                sourceSearchQuery: event.target.value,
                sourceSearchError: ''
              }))}
              placeholder="输入文件名，只显示 TXT 结果"
              value={simulation.sourceSearchQuery}
            />
          </label>
          <button
            className="tool-button"
            disabled={!activeProject || simulationActionDisabled || simulation.sourceSearchStatus === 'running' || !simulation.sourceSearchQuery.trim()}
            type="submit"
          >
            <RefreshCw size={15} className={simulation.sourceSearchStatus === 'running' ? 'spin' : ''} />
            搜索
          </button>
        </form>
        {simulation.sourceSearchResults.length ? (
          <div className="library-list source-search-results">
            {simulation.sourceSearchResults.slice(0, 5).map((result) => {
              const isTxt = String(result.file_type || '').toLowerCase() === 'txt';
              const downloading = simulation.sourceDownloadID === result.id;
              return (
                <div className="library-row" key={result.id}>
                  <div className="library-row-copy">
                    <strong>{result.name}</strong>
                    <span>{result.size || result.file_type || '未知类型'}</span>
                  </div>
                  <button
                    className="tool-button small"
                    disabled={!isTxt || simulationActionDisabled || Boolean(simulation.sourceDownloadID)}
                    onClick={() => onDownloadSource(result)}
                    title={isTxt ? '下载并加入仿写语料' : '请选择 TXT 文件进行下载'}
                    type="button"
                  >
                    <Download size={14} />
                    {downloading ? '下载中' : '下载'}
                  </button>
                </div>
              );
            })}
          </div>
        ) : null}
        {simulation.sourceSearchError ? <div className="error-banner compact">{simulation.sourceSearchError}</div> : null}
        {simulation.sourceSearchMessage ? <div className="success-note">{simulation.sourceSearchMessage}</div> : null}
        <div className="simulation-actions">
          <label className={`tool-button file-picker ${!activeProject || simulationActionDisabled ? 'disabled' : ''}`}>
            <Upload size={16} />
            上传语料
            <input
              accept=".txt,.md,.markdown,text/plain,text/markdown"
              disabled={!activeProject || simulationActionDisabled}
              multiple
              onChange={onUploadSources}
              type="file"
            />
          </label>
          <button className="tool-button accent" disabled={!canStartAnalysis} onClick={() => onAnalyze('scan')} type="button">
            <WandSparkles size={16} />
            {analysisActionLabel}
          </button>
        </div>
        {simulation.uploadMessage ? <div className="success-note">{simulation.uploadMessage}</div> : null}
        <p className={`simulation-next-step ${simulation.files.length ? 'ready' : ''}`}>
          <strong>下一步：</strong>{analysisNextStep}
        </p>
        <div className="file-list">
          {simulation.files.length === 0 ? (
            <div className="empty-state">暂无上传语料</div>
          ) : (
            simulation.files.map((file) => (
              <div className="file-row" key={file.name}>
                <span>{file.name}</span>
                <strong>{formatBytes(file.size)}</strong>
              </div>
            ))
          )}
        </div>
      </section>

      <section className="simulation-section">
        <div className="section-title">
          <WandSparkles size={17} />
          <span>分析进度</span>
        </div>
        <div className={`workflow-status ${simulation.analysisStatus}`}>
          <strong>{workflowStatusText(simulation.analysisStatus)}</strong>
          <span>{latestAnalysis?.message || '等待分析'}</span>
        </div>
        <SimulationEventList events={simulation.analysisEvents} />
      </section>
      </div>
    </>
  );
}

function SimulationDiagnostics({ profile, disabled, onAnalyze }) {
  const actions = profile.actions || {};
  return (
    <div className="simulation-diagnostics">
      <dl className="simulation-metrics" aria-label="仿写画像健康摘要">
        <div><dt>版本</dt><dd>{profile.version || '未知'}{profile.profileDigest ? ` · ${profile.profileDigest}` : ''}</dd></div>
        <div><dt>覆盖</dt><dd>{profile.reportCount}/{profile.sourceCount}（{profile.coveragePercent}%）</dd></div>
        <div><dt>更新时间</dt><dd>{formatSimulationTimestamp(profile.updatedAt)}</dd></div>
        <div><dt>能力</dt><dd>{profile.localEvidence ? '本地证据' : '仅 portable'} · {profile.safetyIndex ? '本地安全索引' : '安全检查部分能力'}</dd></div>
      </dl>
      {profile.healthReasons.length ? (
        <p className="simulation-reason" title={profile.healthReasons.join('；')}>
          原因：{profile.healthReasons.join('；')}
        </p>
      ) : null}
      <div className="simulation-feature-counts" aria-label="画像特征分类">
        <span>稳定 {profile.featureCounts.stable}</span>
        <span>局部 {profile.featureCounts.local}</span>
        <span>离群 {profile.featureCounts.outlier}</span>
        <span>冲突 {profile.featureCounts.contradictory}</span>
      </div>
      <div className="simulation-actions diagnostic-actions" aria-label="画像刷新操作">
        <SimulationActionButton action="scan" capability={actions.rescan} disabled={disabled} label="重新扫描" onAnalyze={onAnalyze} />
        <SimulationActionButton action="resynthesize" capability={actions.resynthesize} disabled={disabled} label="仅重合成" onAnalyze={onAnalyze} />
        <SimulationActionButton action="reanalyze" capability={actions.reanalyze} disabled={disabled} label="全量重分析" onAnalyze={onAnalyze} />
      </div>
      <SimulationModePreview profile={profile} />
      <SimulationContractDiagnostics contract={profile.contract} />
      <SimulationCheckDiagnostics check={profile.check} />
      {profile.diagnosticError ? <div className="error-banner compact">{profile.diagnosticError}</div> : null}
      <p className="simulation-boundary-note">检查用于降低复制风险，不构成法律结论，也不会展示来源原文。</p>
    </div>
  );
}

function SimulationActionButton({ action, capability, disabled, label, onAnalyze }) {
  const enabled = Boolean(capability?.enabled);
  const reason = capability?.reason || '';
  return (
    <button
      aria-label={label}
      className="tool-button small"
      disabled={disabled || !enabled}
      onClick={() => onAnalyze(action)}
      title={!enabled ? reason : label}
      type="button"
    >
      {label}
    </button>
  );
}

function SimulationModePreview({ profile }) {
  return (
    <section className="simulation-diagnostic-block" aria-label="模式实际注入预览">
      <h4>模式状态</h4>
      <p>
        已选：{simulationModeLabel(profile.selectedMode)} · 实际：
        {simulationModeLabel(profile.effectiveMode)}
        {profile.effectiveReason ? `（${profile.effectiveReason}）` : ''}
      </p>
      <div className="simulation-mode-preview-grid">
        {profile.modePreviews.map((preview) => (
          <div className="simulation-mode-preview" key={preview.mode}>
            <strong>{simulationModeLabel(preview.mode)}</strong>
            <span>{simulationContractStatusLabel(preview.status)}</span>
            {(preview.roles || []).map((role) => (
              <span key={`${preview.mode}-${role.role}-${role.phase}`}>
                {simulationRoleLabel(role.role)}：{role.featureCount} 项（must {role.must} / should {role.should} / avoid {role.avoid}，预算 {role.byteBudget}B）
              </span>
            ))}
          </div>
        ))}
      </div>
    </section>
  );
}

function SimulationContractDiagnostics({ contract }) {
  if (!contract) {
    return <section className="simulation-diagnostic-block"><h4>共享契约</h4><p>尚未生成可用契约。</p></section>;
  }
  return (
    <section className="simulation-diagnostic-block" aria-label="仿写契约状态">
      <h4>共享契约 r{contract.revision || 0}</h4>
      <p>
        {simulationContractStatusLabel(contract.status)}
        {!contract.current ? ` · 已过期：${contract.staleReason || '绑定已变化'}` : ''}
        {` · Foundation r${contract.foundationRevision || 0}`}
      </p>
      <p>排除/降级 {contract.exclusionCount || 0} 项{formatReasonCounts(contract.exclusionReasons)}</p>
      {(contract.views || []).map((view) => (
        <details key={`${view.role}-${view.phase}`}>
          <summary>{simulationRoleLabel(view.role)} · must {view.must} / should {view.should} / avoid {view.avoid}</summary>
          <ul className="simulation-feature-list">
            {(view.features || []).map((feature) => (
              <li key={`${view.role}-${feature.level}-${feature.id}`} title={feature.statement}>
                <strong>{feature.level}</strong> [{feature.dimension}] {feature.statement}
              </li>
            ))}
          </ul>
        </details>
      ))}
    </section>
  );
}

function SimulationCheckDiagnostics({ check }) {
  if (!check) {
    return null;
  }
  return (
    <section className="simulation-diagnostic-block" aria-label="最近仿写检查">
      <h4>最近章节检查 · {simulationCheckStateLabel(check.state)}</h4>
      <p>
        {check.chapter ? `第 ${check.chapter} 章 · ` : ''}
        草稿绑定{check.draftCurrent ? '匹配' : '不匹配'} · 风险 {check.riskCount || 0} ·
        must {check.mustMet || 0}/{check.mustTotal || 0} · 建议 {check.advisoryCount || 0}
      </p>
      {check.capability ? <p>能力：{check.capability}{check.capabilityReason ? `（${check.capabilityReason}）` : ''}</p> : null}
      {check.reason ? <p>原因：{check.reason}</p> : null}
      {(check.risks || []).map((risk, index) => (
        <p className="simulation-risk-location" key={`${risk.type}-${risk.startRune}-${index}`}>
          {risk.type} · 当前草稿字符 {risk.startRune + 1}–{risk.startRune + risk.lengthRunes}：{risk.draftExcerpt}
        </p>
      ))}
    </section>
  );
}

function LibrarySearch({ disabled, label, placeholder, query, onQueryChange, onRefresh }) {
  return (
    <form className="library-search" onSubmit={(event) => {
      event.preventDefault();
      onRefresh();
    }}>
      <input
        aria-label={label}
        disabled={disabled}
        placeholder={placeholder}
        value={query}
        onChange={(event) => onQueryChange(event.target.value)}
      />
      <button className="icon-button" disabled={disabled} title="刷新" type="submit">
        <RefreshCw size={16} />
      </button>
    </form>
  );
}

function LibraryList({ canLoad, emptyText, items, loading, onLoad }) {
  if (loading && items.length === 0) {
    return <div className="empty-state">加载中...</div>;
  }
  if (items.length === 0) {
    return <div className="empty-state">{emptyText}</div>;
  }
  return (
    <div className="library-list">
      {items.map((entry, index) => {
        const name = libraryEntryName(entry);
        const meta = libraryEntryMeta(entry);
        return (
          <button
            className="library-row"
            disabled={!canLoad || !name}
            key={`${name || 'library-entry'}-${index}`}
            onClick={() => onLoad(entry)}
            title={name}
            type="button"
          >
            <span className="library-entry">
              <strong>{name || '未命名条目'}</strong>
              <small>{meta || '可加载到当前项目'}</small>
            </span>
            <span className="library-load">加载</span>
          </button>
        );
      })}
    </div>
  );
}

function LibraryFeedback({ error, message }) {
  if (error) {
    return <div className="error-banner compact">{error}</div>;
  }
  if (message) {
    return <div className="success-note">{message}</div>;
  }
  return null;
}

function LibrarySaveRow({
  allowEmptyName = false,
  canSave,
  error,
  name,
  nameLabel = '画像名称',
  placeholder,
  saveLabel = '保存当前画像到库',
  saving,
  onNameChange,
  onSave
}) {
  const canSubmit = Boolean(canSave && (allowEmptyName || name.trim()) && !saving);
  return (
    <div className="library-save-stack">
      <input
        aria-label={nameLabel}
        disabled={!canSave || saving}
        placeholder={placeholder}
        value={name}
        onChange={(event) => onNameChange(event.target.value)}
      />
      <button className="tool-button full-width" disabled={!canSubmit} onClick={onSave} type="button">
        <Plus size={16} />
        {saveLabel}
      </button>
      {error ? <div className="error-banner compact">{error}</div> : null}
    </div>
  );
}

function SimulationEventList({ events }) {
  if (!events.length) {
    return null;
  }
  return (
    <div className="simulation-events">
      {events.map((event, index) => (
        <div className={event.error ? 'simulation-event error' : 'simulation-event'} key={`${event.stage}-${index}`}>
          <span>{event.stage || 'event'}</span>
          <strong>{event.current && event.total ? `${event.current}/${event.total}` : ''}</strong>
          <p>{event.error || event.message}</p>
        </div>
      ))}
    </div>
  );
}

function ContinuationPanel({
  activeProject,
  busy,
  continuation,
  workflow,
  setContinuation,
  onUpload,
  onBeginDraft,
  onGenerateProposal,
  onRevise,
  onApprove,
  onStart,
  coCreate,
  planningRevision,
  setCoCreate,
  setPlanningRevision,
  adaptation,
  onCoCreateBegin,
  onCoCreateSubmit,
  onCoCreateSuggestion,
  onCoCreateRevise,
  onCoCreateResolveDecision,
  onCoCreateCommit,
  onCoCreateCancel
}) {
  const latest = latestSimulationEvent(continuation.events);
  const steps = deriveContinuationSteps(workflow);
  const reviewKind = continuationReviewKind(workflow);
  const showUpload = !workflow.exists || workflow.stage === 'source_importing';
  const actionBusy = busy || continuation.actionStatus === 'running';
  const showDraftDialogue = coCreate?.kind === 'continuation' && coCreate.status !== 'started' && Boolean(coCreate.active || coCreate.ready || coCreate.draftPrompt || hasCoCreateWorkspaceContent(coCreate));
  return (
    <div className="side-content continuation-panel">
      {continuation.error || workflow.lastError ? <div className="error-banner compact">{continuation.error || workflow.lastError}</div> : null}

      <section className="continuation-stepper" aria-label="小说续写流程">
        {steps.map((step, index) => (
          <div className={`continuation-step ${step.status}`} key={step.id}>
            <span>{step.status === 'complete' ? <Check size={13} /> : index + 1}</span>
            <strong>{step.label}</strong>
          </div>
        ))}
      </section>

      {showDraftDialogue ? <CoCreatePanel
        activeProject={activeProject}
        busy={busy}
        coCreate={coCreate}
        planningRevision={planningRevision}
        planningReview={{}}
        setCoCreate={setCoCreate}
        setPlanningRevision={setPlanningRevision}
        adaptation={adaptation}
        onBegin={onCoCreateBegin}
        onConfirmIntake={() => {}}
        onSubmit={onCoCreateSubmit}
        onRebrief={onCoCreateSubmit}
        onSuggestion={onCoCreateSuggestion}
        onRevise={onCoCreateRevise}
        onResolveDecision={onCoCreateResolveDecision}
        onCommit={onCoCreateCommit}
        onCancel={onCoCreateCancel}
        continuationOnly
        workspaceTranscript
      /> : null}

      <section className="simulation-section continuation-source-section">
        <div className="section-title">
          <Upload size={17} />
          <span>原作基线</span>
        </div>
        {showUpload ? (
          <div className="tool-form">
            <p className="muted continuation-copy">上传只建立原作基线，不会自动开始续写。</p>
            <label className={`tool-button file-picker full ${!activeProject || busy ? 'disabled' : ''}`}>
              <Upload size={16} />
              上传并建立原作基线
              <input
                accept=".txt,.md,.markdown,text/plain,text/markdown"
                disabled={!activeProject || busy}
                onChange={onUpload}
                type="file"
              />
            </label>
          </div>
        ) : (
          <div className="continuation-baseline">
            <strong>原作已锁定</strong>
            <span>{workflow.baseChapterCount > 0 ? `第 1–${workflow.baseChapterCount} 章` : '原作章节'}不会被续写规划覆盖</span>
            {workflow.nextChapter > 0 ? <span>续写从第 {workflow.nextChapter} 章开始</span> : null}
          </div>
        )}
        {continuation.message ? <div className="success-note">{continuation.message}</div> : null}
        {showUpload || continuation.status === 'running' ? (
          <div className={`workflow-status ${continuation.status}`}>
            <strong>{workflowStatusText(continuation.status)}</strong>
            <span>{latest?.message || '等待导入原作文本'}</span>
          </div>
        ) : null}
        {continuation.sourceFile ? (
          <div className="file-list">
            <div className="file-row">
              <span>{continuation.sourceFile.name}</span>
              <strong>{continuation.sourceFile.size ? formatBytes(continuation.sourceFile.size) : '已导入'}</strong>
            </div>
          </div>
        ) : null}
        <SimulationEventList events={continuation.events} />
      </section>

      {workflow.exists && workflow.stage !== 'source_importing' ? (
        <section className="simulation-section continuation-action-section">
          <div className="section-title">
            <BookOpen size={17} />
            <span>{continuationStageTitle(workflow)}</span>
          </div>
          <p className="muted continuation-copy">{continuationStageDescription(workflow)}</p>

          {workflow.stage === 'source_ready' || (workflow.stage === 'draft_collecting' && !workflow.draft) ? (
            <button className="tool-button accent full-width" disabled={actionBusy} onClick={onBeginDraft} type="button">
              <MessageSquareText size={16} />
              进入 Draft 共创
            </button>
          ) : null}

          {workflow.stage === 'draft_collecting' && workflow.draft ? (
            <button className="tool-button accent full-width" disabled={actionBusy} onClick={onGenerateProposal} type="button">
              <WandSparkles size={16} />
              根据 Draft 生成续写提案
            </button>
          ) : null}

          {reviewKind ? (
            <ContinuationReviewForm
              busy={actionBusy}
              continuation={continuation}
              reviewKind={reviewKind}
              setContinuation={setContinuation}
              workflow={workflow}
              onApprove={onApprove}
              onRevise={onRevise}
            />
          ) : null}

          {continuationCanRetry(workflow) ? (
            <div className="settings-note warning">当前任务可从检查点继续，请点击工作区顶部“恢复”。</div>
          ) : null}

          {workflow.stage === 'ready_to_write' ? (
            <div className="continuation-start-card">
              <strong>全部规划已审核通过</strong>
              <span>确认后才会从第 {workflow.nextChapter || workflow.baseChapterCount + 1} 章开始续写。</span>
              <button className="tool-button accent full-width" disabled={actionBusy} onClick={onStart} type="button">
                <Play size={16} />
                确认并开始续写
              </button>
            </div>
          ) : null}

          {workflow.stage === 'writing' ? <div className="success-note">续写已启动，Writer 正在按审核通过的细纲创作。</div> : null}
          {continuation.actionMessage ? <div className="success-note">{continuation.actionMessage}</div> : null}
        </section>
      ) : null}
    </div>
  );
}

function ContinuationReviewForm({ busy, continuation, reviewKind, setContinuation, workflow, onApprove, onRevise }) {
  const approveLabel = reviewKind === 'proposal'
    ? workflow.shortStory ? '通过提案，进入章节细纲' : '通过提案，进入分卷审核'
    : reviewKind === 'volumes'
      ? '通过分卷，进入章节细纲'
      : '通过细纲，允许准备开写';
  return (
    <div className="continuation-review-form">
      {reviewKind === 'volumes' ? (
        <div className="continuation-scope-row">
          <button className={continuation.scope === 'all' ? 'active' : ''} onClick={() => setContinuation((previous) => ({ ...previous, scope: 'all' }))} type="button">全部</button>
          <button className={continuation.scope === 'volume' ? 'active' : ''} onClick={() => setContinuation((previous) => ({ ...previous, scope: 'volume' }))} type="button">单卷</button>
          {continuation.scope === 'volume' ? (
            <input aria-label="分卷序号" min="1" type="number" value={continuation.volumeIndex} onChange={(event) => setContinuation((previous) => ({ ...previous, volumeIndex: event.target.value }))} />
          ) : null}
        </div>
      ) : null}
      {reviewKind === 'outlines' ? (
        <ContinuationOutlineScope continuation={continuation} setContinuation={setContinuation} />
      ) : null}
      <label className="field-label">
        <span>审核修改要求</span>
        <textarea
          disabled={busy}
          placeholder={reviewKind === 'outlines' ? '说明需要修改的章节、节奏或情节...' : '说明需要调整的方向、结构或分卷...'}
          rows="5"
          value={continuation.instruction}
          onChange={(event) => setContinuation((previous) => ({ ...previous, instruction: event.target.value, error: '' }))}
        />
      </label>
      <div className="continuation-review-actions">
        <button className="tool-button" disabled={busy || !continuation.instruction.trim()} onClick={onRevise} type="button">
          <Pencil size={16} />
          提交修改
        </button>
        <button className="tool-button accent" disabled={busy} onClick={onApprove} type="button">
          <Check size={16} />
          {approveLabel}
        </button>
      </div>
    </div>
  );
}

function ContinuationOutlineScope({ continuation, setContinuation }) {
  return (
    <div className="continuation-outline-scope">
      <label className="field-label">
        <span>修改范围</span>
        <select value={continuation.scope} onChange={(event) => setContinuation((previous) => ({ ...previous, scope: event.target.value }))}>
          <option value="all">全部细纲</option>
          <option value="volume">单卷</option>
          <option value="chapter">单章</option>
          <option value="range">章节范围</option>
        </select>
      </label>
      {continuation.scope === 'volume' ? (
        <input aria-label="分卷序号" min="1" placeholder="卷" type="number" value={continuation.volumeIndex} onChange={(event) => setContinuation((previous) => ({ ...previous, volumeIndex: event.target.value }))} />
      ) : null}
      {continuation.scope === 'chapter' ? (
        <input aria-label="章节序号" min="1" placeholder="章" type="number" value={continuation.chapter} onChange={(event) => setContinuation((previous) => ({ ...previous, chapter: event.target.value }))} />
      ) : null}
      {continuation.scope === 'range' ? (
        <div className="field-grid">
          <input aria-label="起始章节" min="1" placeholder="起始章" type="number" value={continuation.fromChapter} onChange={(event) => setContinuation((previous) => ({ ...previous, fromChapter: event.target.value }))} />
          <input aria-label="结束章节" min="1" placeholder="结束章" type="number" value={continuation.toChapter} onChange={(event) => setContinuation((previous) => ({ ...previous, toChapter: event.target.value }))} />
        </div>
      ) : null}
    </div>
  );
}

function ExportPanel({ activeProject, busy, exportJob, setExportJob, onExport }) {
  const result = exportJob.result;
  const suggestedName = buildExportSuggestedName(exportJob, activeProject);
  return (
    <div className="side-content">
      {exportJob.error ? <div className="error-banner compact">{exportJob.error}</div> : null}

      <section className="simulation-section">
        <div className="section-title">
          <Download size={17} />
          <span>小说导出</span>
        </div>
        <div className="tool-form">
          <label className="field-label">
            <span>文件名</span>
            <input
              disabled={busy}
              placeholder={suggestedName}
              value={exportJob.path}
              onChange={(event) => setExportJob((previous) => ({ ...previous, path: event.target.value }))}
            />
          </label>
          <label className="field-label">
            <span>格式</span>
            <select
              disabled={busy}
              value={exportJob.format}
              onChange={(event) => setExportJob((previous) => ({ ...previous, format: event.target.value }))}
            >
              <option value="txt">txt</option>
              <option value="epub">epub</option>
            </select>
          </label>
          <label className="field-label">
            <span>导出用途</span>
            <select
              disabled={busy}
              value={exportJob.purpose || 'preview'}
              onChange={(event) => setExportJob((previous) => ({ ...previous, purpose: event.target.value, audit: null, error: '' }))}
            >
              <option value="preview">阶段预览（不执行发布门禁）</option>
              <option value="publish">正式发布（自动全书审计）</option>
            </select>
          </label>
          {exportJob.purpose === 'publish' ? <div className="settings-note warning">正式发布必须覆盖全书、无待返工章节且项目已暂停。审计未通过时会展示当前报告，只有再次确认后才能导出。</div> : null}
          <div className="field-grid">
            <label className="field-label">
              <span>from</span>
              <input
                disabled={busy}
                inputMode="numeric"
                min="0"
                placeholder="0"
                type="number"
                value={exportJob.from}
                onChange={(event) => setExportJob((previous) => ({ ...previous, from: event.target.value, purpose: event.target.value ? 'preview' : previous.purpose }))}
              />
            </label>
            <label className="field-label">
              <span>to</span>
              <input
                disabled={busy}
                inputMode="numeric"
                min="0"
                placeholder="0"
                type="number"
                value={exportJob.to}
                onChange={(event) => setExportJob((previous) => ({ ...previous, to: event.target.value, purpose: event.target.value ? 'preview' : previous.purpose }))}
              />
            </label>
          </div>
          <button className="tool-button accent full-width" disabled={!activeProject || busy} onClick={onExport} type="button">
            <Download size={16} />
            导出已完成章节
          </button>
        </div>
        {exportJob.message ? <div className="success-note">{exportJob.message}</div> : null}
        {exportJob.audit ? <div className="settings-note warning">导出审计：{exportJob.audit.status || 'unknown'} · {String(exportJob.audit.digest || '').slice(0, 12)}</div> : null}
        <div className={`workflow-status ${exportJob.status}`}>
          <strong>{workflowStatusText(exportJob.status)}</strong>
          <span>{result?.path || '等待导出'}</span>
        </div>
        {result ? (
          <section className="metric-grid">
            <Metric label="Chapters" value={result.chapters || 0} />
            <Metric label="Bytes" value={formatBytes(result.bytes || 0)} />
          </section>
        ) : null}
        {result?.skipped?.length ? <div className="success-note">跳过章节：{result.skipped.join(', ')}</div> : null}
      </section>
    </div>
  );
}

function DiagnosticPanel({ activeProject, busy, diagnostic, onRun }) {
  const report = diagnostic.report || {};
  const runtime = diagnostic.runtime || {};
  const stats = report.Stats || report.stats || {};
  const findings = report.Findings || report.findings || [];
  const actions = report.Actions || report.actions || [];
  const repeats = runtime.Repeats || runtime.repeats || [];
  return (
    <div className="side-content">
      {diagnostic.error ? <div className="error-banner compact">{diagnostic.error}</div> : null}

      <section className="simulation-section">
        <div className="section-title">
          <Activity size={17} />
          <span>诊断</span>
        </div>
        <button className="tool-button accent full-width" disabled={!activeProject || busy} onClick={onRun} type="button">
          <Activity size={16} />
          Run diag
        </button>
        <div className={`workflow-status ${diagnostic.status}`}>
          <strong>{workflowStatusText(diagnostic.status)}</strong>
          <span>{diagnostic.exportPath || '等待诊断'}</span>
        </div>
      </section>

      <section className="metric-grid">
        <Metric label="章节" value={`${stats.CompletedChapters || stats.completed_chapters || 0}/${stats.TotalChapters || stats.total_chapters || 0}`} />
        <Metric label="字数" value={stats.TotalWords || stats.total_words || 0} />
        <Metric label="评分" value={formatScore(stats.AvgReviewScore || stats.avg_review_score || 0)} />
        <Metric label="Actions" value={actions.length} />
      </section>

      <section>
        <div className="section-title">
          <Activity size={17} />
          <span>发现</span>
        </div>
        <div className="finding-list">
          {findings.length === 0 ? (
            <div className="empty-state">暂无诊断发现</div>
          ) : (
            findings.slice(0, 12).map((finding, index) => (
              <div className={`finding-row ${String(finding.Severity || finding.severity || '').toLowerCase()}`} key={`${finding.Rule || finding.rule}-${index}`}>
                <strong>{finding.Title || finding.title || 'Untitled'}</strong>
                <span>{finding.Category || finding.category || '-'} · {finding.Severity || finding.severity || '-'}</span>
                <small>{finding.Suggestion || finding.suggestion || finding.Evidence || finding.evidence || '-'}</small>
              </div>
            ))
          )}
        </div>
      </section>

      <section>
        <div className="section-title">
          <ListRestart size={17} />
          <span>运行信号</span>
        </div>
        <div className="agent-list">
          <div className="backend-row">
            <strong>{runtime.CurrentStep || runtime.current_step || 'no checkpoint'}</strong>
            <span>{runtime.StuckStep || runtime.stuck_step ? `stuck ×${runtime.StuckCount || runtime.stuck_count || 0}` : 'no stuck signal'}</span>
            <small>log errors ×{runtime.LogErrors || runtime.log_errors || 0} · warn ×{runtime.LogWarns || runtime.log_warns || 0}</small>
          </div>
          {repeats.slice(0, 4).map((repeat, index) => (
            <div className="backend-row" key={`${repeat.Sig || repeat.sig}-${index}`}>
              <strong>{repeat.Sig || repeat.sig}</strong>
              <span>×{repeat.Count || repeat.count || 0}</span>
              <small>近端重复信号</small>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}

function CachePanel({ snapshot, analytics = {} }) {
  const roles = snapshot?.CachePerAgent || snapshot?.cache_per_agent || [];
  const models = snapshot?.CachePerModel || snapshot?.cache_per_model || [];
  const input = snapshot?.TotalInputTokens || 0;
  const cacheRead = snapshot?.TotalCacheReadTokens || 0;
  const cacheWrite = snapshot?.TotalCacheWriteTokens || 0;
  const missing = snapshot?.MissingAssistantUsage || 0;
  const observedCalls = Number(snapshot?.OverallRecentSamples || 0);
  const coverage = usageCoverage(missing, observedCalls);
  const confidence = usageConfidence(coverage, observedCalls);
  const cacheCapable = Boolean(snapshot?.OverallCacheCapable || cacheRead > 0 || cacheWrite > 0);
  return (
    <div className="side-content usage-dashboard">
      <header className="usage-dashboard-header">
        <div>
          <span className="eyebrow">模型与成本</span>
          <strong>Prompt Cache 用量</strong>
        </div>
        <span className={`confidence-badge ${confidence.level}`}>{confidence.label}</span>
      </header>
      <section className="metric-grid">
        <Metric label="输入" value={formatCompact(input)} />
        <Metric label="非缓存输入" value={formatCompact(Math.max(0, input - cacheRead))} />
        <Metric label="缓存读取" value={formatCompact(cacheRead)} />
        <Metric label="缓存写入" value={formatCompact(cacheWrite)} />
        <Metric label="输出" value={formatCompact(snapshot?.TotalOutputTokens || 0)} />
        <Metric label="费用" value={formatUSD(snapshot?.TotalCostUSD || 0)} />
      </section>
      <section className="model-summary">
        <Metric label="缓存命中" value={cacheHitLabel({ cacheRead, input, cacheCapable })} />
        <Metric label="节省费用" value={formatUSD(snapshot?.TotalSavedUSD || 0)} />
        <Metric label="近期命中" value={cacheHitLabel({
          cacheRead: snapshot?.OverallRecentCacheRead || 0,
          input: snapshot?.OverallRecentInput || 0,
          cacheCapable
        })} />
        <Metric label="Usage 缺口" value={missing} />
        <Metric label="数据覆盖" value={coverage === null ? '待采集' : `${Math.round(coverage * 100)}%`} />
      </section>
      <UsageList title="按角色" items={roles} labelKey="Role" />
      <UsageList title="按模型" items={models} labelKey="Model" />
      <UsageTrend report={analytics.report} />
      <UsageObservabilityTable report={analytics.report} />
      <UsageRecommendations analytics={analytics} />
    </div>
  );
}

function UsageTrend({ report }) {
  const trend = Array.isArray(report?.trend) ? report.trend.slice(-14) : [];
  if (!trend.length) {
    return null;
  }
  const maximum = Math.max(...trend.map((item) => Number(item.input_tokens || 0)), 1);
  return (
    <section aria-label="最近十四天模型输入趋势">
      <div className="section-title"><Activity size={17} /><span>最近趋势</span></div>
      <div className="usage-trend">
        {trend.map((item) => (
          <div className="usage-trend-row" key={item.date}>
            <span>{item.date.slice(5)}</span>
            <div><i style={{ width: `${Math.max(2, Math.round((Number(item.input_tokens || 0) / maximum) * 100))}%` }} /></div>
            <small>{formatCompact(item.input_tokens || 0)} · {formatUSD(item.cost_usd || 0)}</small>
          </div>
        ))}
      </div>
    </section>
  );
}

function UsageRecommendations({ analytics }) {
  if (analytics.status === 'loading') {
    return <div className="empty-state" role="status">正在分析模型用量…</div>;
  }
  if (analytics.error) {
    return <div className="error-banner compact" role="alert">{analytics.error}</div>;
  }
  const recommendations = Array.isArray(analytics.recommendations) ? analytics.recommendations : [];
  return (
    <section>
      <div className="section-title"><WandSparkles size={17} /><span>优化建议</span></div>
      <div className="recommendation-list">
        {recommendations.length ? recommendations.map((item) => (
          <article key={item.id}>
            <strong>{item.model || '当前模型'}</strong>
            <span>{item.evidence}</span>
            <small>{item.action}</small>
          </article>
        )) : <div className="empty-state">数据不足或当前无需优化；系统不会自动切换模型。</div>}
      </div>
    </section>
  );
}

function UsageList({ title, items, labelKey }) {
  return (
    <section>
      <div className="section-title">
        <Database size={17} />
        <span>{title}</span>
      </div>
      <div className="agent-list">
        {items.length === 0 ? (
          <div className="empty-state">暂无 usage 数据</div>
        ) : (
          items.map((item) => {
            const label = item[labelKey] || item[labelKey.toLowerCase()] || 'unknown';
            const input = item.Input || item.input || 0;
            const cacheRead = item.CacheRead || item.cache_read || 0;
            const cacheWrite = item.CacheWrite || item.cache_write || 0;
            const cacheCapable = Boolean(item.CacheCapable || item.cache_capable || cacheRead > 0 || cacheWrite > 0);
            return (
              <div className="usage-row" key={label}>
                <strong>{label}</strong>
                <span>{cacheHitLabel({ cacheRead, input, cacheCapable })}</span>
                <small>{formatCompact(input)} 输入 / {formatCompact(item.Output || item.output || 0)} 输出</small>
                <small>{formatCompact(cacheRead)} 读取 / {formatCompact(cacheWrite)} 写入 · {formatUSD(item.Cost || item.cost || 0)}</small>
              </div>
            );
          })
        )}
      </div>
    </section>
  );
}

function BackendPanel({ backend, busy, onRefresh, onTest }) {
  const calls = backend?.recent_calls || [];
  return (
    <div className="side-content">
      <section className="model-summary">
        <Metric label="Status" value={backend?.status || 'unknown'} />
        <Metric label="Provider" value={backend?.provider || '-'} />
        <Metric label="Model" value={backend?.model || '-'} />
        <Metric label="Runtime" value={backend?.runtime_state || '-'} />
      </section>
      <div className="simulation-actions">
        <button className="tool-button" disabled={busy} onClick={onRefresh} type="button">
          <RefreshCw size={16} />
          Refresh
        </button>
        <button className="tool-button accent" disabled={busy} onClick={onTest} type="button">
          <TestTube2 size={16} />
          Test
        </button>
      </div>
      {backend?.manual_test ? (
        <div className="success-note">{backend.manual_test.message}</div>
      ) : null}
      <section>
        <div className="section-title">
          <Activity size={17} />
          <span>最近调用</span>
        </div>
        <div className="agent-list">
          {calls.length === 0 ? (
            <div className="empty-state">暂无已完成调用</div>
          ) : (
            calls.map((call, index) => (
              <div className={`backend-row ${call.failed ? 'error' : call.running ? 'running' : ''}`} key={`${call.time}-${index}`}>
                <strong>{call.category || 'CALL'} / {call.agent || 'host'}</strong>
                <span>{call.running ? 'running' : call.failed ? 'failed' : 'ok'} · {call.duration_ms || 0}ms</span>
                <small>{call.summary || '-'}</small>
              </div>
            ))
          )}
        </div>
      </section>
    </div>
  );
}

function ModelPanel({
  activeProject,
  runtime,
  modelConfig,
  customModel,
  setCustomModel,
  busy,
  onSwitchDefault,
  onSwitch,
  onInherit,
  onThinking,
  onCoCreateTimeout,
  onCoCreateMaxTokens,
  onRetrySettings,
  onDeleteModel,
  onAddCustom,
  onTestConnection,
  onTestModel,
  onStartGrokLogin,
  onPollGrokLogin,
  onCompleteGrokLogin,
  onRefreshGrokStatus,
  onRefreshCodexStatus,
  onUploadCodexAuth
}) {
  const config = runtime?.config || {};
  const stages = modelConfig?.stages || [];
  const providers = modelConfig?.providers || [];
  const stageModelRouteOptions = buildStageModelRouteOptions(providers);
  const providerChips = providers.map((provider) => ({
    name: provider.name,
    label: provider.label || provider.name,
    models: provider.models || [],
    useProxy: provider.use_proxy === true
  }));
  const visibleDefault = resolveVisibleDefaultModel(activeProject, runtime, modelConfig);
  const activeDefaultRoute = visibleDefault.route;
  const defaultProvider = visibleDefault.provider;
  const defaultModels = visibleDefault.models;
  const defaultModel = visibleDefault.model;
  const [existingTarget, setExistingTarget] = useState({ provider: '', model: '' });
  const existingProvider = existingTarget.provider || providers[0]?.name || '';
  const existingModels = modelOptionsForProvider(providers, existingProvider, existingTarget.model);
  const existingModel = existingTarget.model || existingModels[0] || '';
  const existingIsDefault = activeDefaultRoute.provider === existingProvider && activeDefaultRoute.model === existingModel;
  const existingModelPayload = buildExistingModelActionPayload('default', existingProvider, existingModel);
  const canDeleteExistingModel = Boolean(existingProvider && existingModel && !existingIsDefault);
  const coCreateTimeoutSeconds = modelConfig?.cocreate_timeout_seconds || config.cocreate_timeout_seconds || 60;
  const coCreateMaxTokens = modelConfig?.cocreate_max_tokens || config.cocreate_max_tokens || 4096;
  const modelAutoSwitch = modelConfig?.model_auto_switch || {};
  const existingUsesOpenAIEndpoint = providerUsesOpenAIEndpoint(customModel);
  const modelCallMaxAttempts =
    modelAutoSwitch.model_call_max_attempts ||
    modelAutoSwitch.network_max_attempts ||
    config.model_call_max_attempts ||
    7;
  const structureRepairMaxAttempts =
    modelConfig?.structure_repair_max_attempts ||
    config.structure_repair_max_attempts ||
    2;
  const budgetQualityMaxAttempts =
    modelConfig?.budget_quality_max_attempts ||
    config.budget_quality_max_attempts ||
    2;
  const adaptationOutlineAuditRetryMaxAttempts =
    modelConfig?.adaptation_outline_audit_retry_max_attempts ||
    config.adaptation_outline_audit_retry_max_attempts ||
    2;
  const [coCreateTimeoutDraft, setCoCreateTimeoutDraft] = useState(String(coCreateTimeoutSeconds));
  const [coCreateMaxTokensDraft, setCoCreateMaxTokensDraft] = useState(String(coCreateMaxTokens));
  const [modelCallAttemptsDraft, setModelCallAttemptsDraft] = useState(String(modelCallMaxAttempts));
  const [structureRepairAttemptsDraft, setStructureRepairAttemptsDraft] = useState(String(structureRepairMaxAttempts));
  const [budgetQualityAttemptsDraft, setBudgetQualityAttemptsDraft] = useState(String(budgetQualityMaxAttempts));
  const [adaptationOutlineAuditRetryAttemptsDraft, setAdaptationOutlineAuditRetryAttemptsDraft] = useState(String(adaptationOutlineAuditRetryMaxAttempts));
  useEffect(() => {
    setCoCreateTimeoutDraft(String(coCreateTimeoutSeconds));
  }, [coCreateTimeoutSeconds]);
  useEffect(() => {
    setCoCreateMaxTokensDraft(String(coCreateMaxTokens));
  }, [coCreateMaxTokens]);
  useEffect(() => {
    setModelCallAttemptsDraft(String(modelCallMaxAttempts));
    setStructureRepairAttemptsDraft(String(structureRepairMaxAttempts));
    setBudgetQualityAttemptsDraft(String(budgetQualityMaxAttempts));
    setAdaptationOutlineAuditRetryAttemptsDraft(String(adaptationOutlineAuditRetryMaxAttempts));
  }, [modelCallMaxAttempts, structureRepairMaxAttempts, budgetQualityMaxAttempts, adaptationOutlineAuditRetryMaxAttempts]);
  useEffect(() => {
    if (providers.length === 0) {
      if (existingTarget.provider || existingTarget.model) {
        setExistingTarget({ provider: '', model: '' });
      }
      return;
    }
    const provider = providers.some((item) => item.name === existingTarget.provider)
      ? existingTarget.provider
      : providers[0].name;
    const models = modelOptionsForProvider(providers, provider, existingTarget.model);
    const model = models.includes(existingTarget.model) ? existingTarget.model : models[0] || '';
    if (provider !== existingTarget.provider || model !== existingTarget.model) {
      setExistingTarget({ provider, model });
    }
    const editorProviderAvailable = providers.some((item) => item.name === customModel.original_provider);
    if (customModel.mode === 'existing' && (!customModel.original_provider || !editorProviderAvailable)) {
      setCustomModel((previous) => modelAddExistingProviderDefaults(previous, providers, provider, model));
    }
  }, [providers, existingTarget.provider, existingTarget.model, customModel.mode, customModel.original_provider, setCustomModel]);
  const selectExistingProvider = (provider, model = '') => {
    const models = modelOptionsForProvider(providers, provider, model);
    const selectedModel = model || models[0] || '';
    setExistingTarget({ provider, model: selectedModel });
    setCustomModel((previous) => modelAddExistingProviderDefaults(previous, providers, provider, selectedModel));
  };
  const coCreateTimeoutValue = Number(coCreateTimeoutDraft);
  const canSaveCoCreateTimeout = Number.isInteger(coCreateTimeoutValue) &&
    coCreateTimeoutValue >= 1 &&
    coCreateTimeoutValue <= 3600 &&
    coCreateTimeoutValue !== coCreateTimeoutSeconds;
  const coCreateMaxTokensValue = Number(coCreateMaxTokensDraft);
  const canSaveCoCreateMaxTokens = Number.isInteger(coCreateMaxTokensValue) &&
    coCreateMaxTokensValue >= 512 &&
    coCreateMaxTokensValue <= 32768 &&
    coCreateMaxTokensValue !== coCreateMaxTokens;
  const modelCallAttemptsValue = Number(modelCallAttemptsDraft);
  const structureRepairAttemptsValue = Number(structureRepairAttemptsDraft);
  const budgetQualityAttemptsValue = Number(budgetQualityAttemptsDraft);
  const adaptationOutlineAuditRetryAttemptsValue = Number(adaptationOutlineAuditRetryAttemptsDraft);
  const canSaveRetrySettings =
    Number.isInteger(modelCallAttemptsValue) &&
    Number.isInteger(structureRepairAttemptsValue) &&
    Number.isInteger(budgetQualityAttemptsValue) &&
    Number.isInteger(adaptationOutlineAuditRetryAttemptsValue) &&
    modelCallAttemptsValue >= 1 &&
    modelCallAttemptsValue <= 30 &&
    structureRepairAttemptsValue >= 1 &&
    structureRepairAttemptsValue <= 15 &&
    budgetQualityAttemptsValue >= 1 &&
    budgetQualityAttemptsValue <= 15 &&
    adaptationOutlineAuditRetryAttemptsValue >= 1 &&
    adaptationOutlineAuditRetryAttemptsValue <= 15 &&
    (modelCallAttemptsValue !== modelCallMaxAttempts ||
      structureRepairAttemptsValue !== structureRepairMaxAttempts ||
      budgetQualityAttemptsValue !== budgetQualityMaxAttempts ||
      adaptationOutlineAuditRetryAttemptsValue !== adaptationOutlineAuditRetryMaxAttempts);
  const selectedPreset = providerPresets.find((preset) => preset.provider === customModel.preset) || providerPresets[0];
  const grokURL = grokAuthorizeURL(customModel.grok_login);
  const grokReady = grokLoggedIn(customModel.grok_status);
  const codexReady = codexLoggedIn(customModel.codex_status);
	const isCodexEditor = customModel.mode === 'codex_auth' || String(customModel.auth || '').trim().toLowerCase() === 'codex';
	const isGrokOAuthEditor = customModel.mode === 'grok_oauth' || String(customModel.auth || '').trim().toLowerCase() === 'grok_oauth';
  const addValidationMessage = modelAddValidationMessage(customModel, modelConfig);
  const canAdd = !addValidationMessage;
  const stageInheritedModelOption = '__stage_inherited_model__';
  const editorModelProvider = customModel.mode === 'existing'
    ? customModel.original_provider || customModel.provider
    : customModel.provider;
  const selectedProviderModels = modelOptionsForProvider(providers, editorModelProvider, customModel.model);
  const discoveredModels = Array.isArray(customModel.discovered_models) ? customModel.discovered_models : [];
  const modelSuggestions = mergeModelOptions(discoveredModels, selectedProviderModels, customModel.model);
	const editorReasoningLevels = reasoningLevelsForModel(customModel.model, {
		name: editorModelProvider,
		type: customModel.type,
		auth: customModel.auth
	});
  const providerEditorID = String(customModel.mode === 'existing' ? customModel.provider : customModel.provider || selectedPreset.provider || '').trim();
  const providerEditorLabel = String(customModel.label || (customModel.mode === 'existing' ? customModel.provider : selectedPreset.label) || '').trim();
  const editingExistingModel = customModel.mode === 'existing';
  const startNewModel = () => {
    setCustomModel((previous) => createNewModelDraft(previous, providers, 'custom'));
  };
  const canTestEditorConnection = Boolean(providerEditorID);
  const canTestEditorModel = Boolean(providerEditorID && String(customModel.model || '').trim());
  const editorTestMessage = customModel.test_message;
  return (
    <div className="side-content">
      <section>
        <div className="section-title">
          <Settings size={17} />
          <span>当前默认模型</span>
        </div>
        <div className="default-model-controls">
          <select
            disabled={busy || providers.length === 0}
            value={defaultProvider}
            onChange={(event) => {
              const provider = event.target.value;
              const models = modelOptionsForProvider(providers, provider, '');
              onSwitchDefault(provider, models[0] || defaultModel);
            }}
          >
            {providers.length === 0 ? <option value="">无 provider</option> : null}
            {providers.map((provider) => (
              <option key={provider.name} value={provider.name}>{provider.name}</option>
            ))}
          </select>
          <select
            disabled={busy || !defaultProvider || defaultModels.length === 0}
            value={defaultModel}
            onChange={(event) => onSwitchDefault(defaultProvider, event.target.value)}
          >
            {defaultModels.length === 0 ? <option value="">无 model</option> : null}
            {defaultModels.map((model) => (
              <option key={model} value={model}>{model}</option>
            ))}
          </select>
        </div>
      </section>
      <section className="model-summary">
        <Metric label="Style" value={config.style || 'default'} />
        <Metric label="Runtime" value={runtime?.runtime_root || '-'} />
      </section>
      <section>
        <div className="section-title">
          <Activity size={17} />
          <span>共创请求超时</span>
        </div>
        <form
          className="model-timeout-form"
          onSubmit={(event) => {
            event.preventDefault();
            onCoCreateTimeout(coCreateTimeoutValue);
          }}
        >
          <label className="field-label">
            <span>秒</span>
            <input
              disabled={busy}
              inputMode="numeric"
              max="3600"
              min="1"
              type="number"
              value={coCreateTimeoutDraft}
              onChange={(event) => setCoCreateTimeoutDraft(event.target.value)}
            />
          </label>
          <button className="tool-button" disabled={busy || !canSaveCoCreateTimeout} type="submit">
            <Check size={16} />
            保存
          </button>
        </form>
      </section>
      <section>
        <div className="section-title">
          <Activity size={17} />
          <span>共创输出 tokens</span>
        </div>
        <form
          className="model-timeout-form"
          onSubmit={(event) => {
            event.preventDefault();
            onCoCreateMaxTokens(coCreateMaxTokensValue);
          }}
        >
          <label className="field-label">
            <span>tokens</span>
            <input
              disabled={busy}
              inputMode="numeric"
              max="32768"
              min="512"
              type="number"
              value={coCreateMaxTokensDraft}
              onChange={(event) => setCoCreateMaxTokensDraft(event.target.value)}
            />
          </label>
          <button className="tool-button" disabled={busy || !canSaveCoCreateMaxTokens} type="submit">
            <Check size={16} />
            保存
          </button>
        </form>
      </section>
      <section>
        <div className="section-title">
          <ListRestart size={17} />
          <span>重试设置</span>
        </div>
        <form
          className="model-timeout-form retry-settings-form"
          onSubmit={(event) => {
            event.preventDefault();
            onRetrySettings(modelCallAttemptsValue, structureRepairAttemptsValue, budgetQualityAttemptsValue, adaptationOutlineAuditRetryAttemptsValue);
          }}
        >
          <label className="field-label">
            <span>模型调用</span>
            <input
              disabled={busy}
              inputMode="numeric"
              max="30"
              min="1"
              type="number"
              value={modelCallAttemptsDraft}
              onChange={(event) => setModelCallAttemptsDraft(event.target.value)}
            />
          </label>
          <label className="field-label">
            <span>结构修复</span>
            <input
              disabled={busy}
              inputMode="numeric"
              max="15"
              min="1"
              type="number"
              value={structureRepairAttemptsDraft}
              onChange={(event) => setStructureRepairAttemptsDraft(event.target.value)}
            />
          </label>
          <label className="field-label">
            <span>预算复核</span>
            <input
              disabled={busy}
              inputMode="numeric"
              max="15"
              min="1"
              type="number"
              value={budgetQualityAttemptsDraft}
              onChange={(event) => setBudgetQualityAttemptsDraft(event.target.value)}
            />
          </label>
          <label className="field-label">
            <span>改编章节纲审计重试</span>
            <input
              disabled={busy}
              inputMode="numeric"
              max="15"
              min="1"
              type="number"
              value={adaptationOutlineAuditRetryAttemptsDraft}
              onChange={(event) => setAdaptationOutlineAuditRetryAttemptsDraft(event.target.value)}
            />
          </label>
          <button className="tool-button" disabled={busy || !canSaveRetrySettings} type="submit">
            <Check size={16} />
            保存
          </button>
        </form>
        <p className="muted retry-settings-help">仅在改编详细章节提纲未通过质量审计时重新生成；不占用结构修复或网络重试次数。</p>
      </section>
      <section>
        <div className="section-title">
          <SlidersHorizontal size={17} />
          <span>创作阶段模型</span>
        </div>
        <p className="muted">
          {activeProject?.id
            ? '这里保存当前项目的阶段路由；显式设置会覆盖全局默认。'
            : '当前未打开项目：这里保存全局阶段默认，新建项目会使用这些设置。'}
        </p>
        {stages.length === 0 ? (
          <div className="empty-state">当前配置没有可用的创作阶段模型</div>
        ) : (
          <div className="stage-model-list">
            {stages.map((route) => {
              const stageProvider = providers.find((provider) => provider.name === route.provider) || {};
              const stageReasoningLevels = reasoningLevelsForModel(route.model, stageProvider);
              return (
                <div className="stage-model-route" key={route.role}>
                  <label className="field-label">
                    <span>{route.label || route.role}</span>
                    <select
                      disabled={busy || stageModelRouteOptions.length === 0}
                      value={route.explicit ? modelRouteOptionValue(route.provider, route.model) : stageInheritedModelOption}
                      onChange={(event) => {
                        const value = event.target.value;
                        if (value === stageInheritedModelOption) {
                          onInherit?.(route.role);
                          return;
                        }
                        const target = stageModelRouteOptions.find((option) => option.value === value);
                        if (target) {
                          onSwitch(route.role, target.provider, target.model);
                        }
                      }}
                    >
                      <option value={stageInheritedModelOption}>
                        继承默认（{route.provider} / {route.model}）
                      </option>
                      {stageModelRouteOptions.map((option) => (
                        <option key={option.value} value={option.value}>{option.label}</option>
                      ))}
                    </select>
                  </label>
                  {stageReasoningLevels ? (
                    <label className="field-label stage-thinking-route">
                      <span>思考深度</span>
                      <select
                        disabled={busy}
                        value={route.reasoning_effort || ''}
                        onChange={(event) => onThinking(route.role, event.target.value)}
                      >
                        {stageReasoningLevels.map((level) => (
                          <option key={level || 'inherit'} value={level}>{level || '继承模型默认'}</option>
                        ))}
                      </select>
                    </label>
                  ) : null}
                </div>
              );
            })}
            <p className="muted">每个阶段可继承项目默认路由，或明确选择“后端 / 模型”组合。一次 Architect 规划可能连续完成骨架和首批细纲，此时全程使用“骨架规划”模型；独立细纲生成与修订使用“详细提纲”模型。</p>
          </div>
        )}
      </section>
      <form className="custom-model-form" onSubmit={onAddCustom}>
        <div className="section-title">
          <Settings size={17} />
          <span>配置模型</span>
        </div>
        <div className="provider-chip-list">
          {providerChips.length === 0 ? (
            <div className="empty-state">暂无模型</div>
          ) : (
            providerChips.map((provider) => (
              <button
                className={`provider-chip ${editingExistingModel && existingProvider === provider.name ? 'active' : ''}`}
                disabled={busy}
                key={provider.name}
                onClick={() => selectExistingProvider(provider.name, provider.models[0] || '')}
                type="button"
              >
                <span>{provider.label}</span>
                {provider.useProxy ? <small>PROXY</small> : null}
              </button>
            ))
          )}
        </div>
        <div className="backend-picker-row">
          <label className="field-label">
            <span>已有配置</span>
            <select
              disabled={busy || providers.length === 0}
              value={editingExistingModel ? existingProvider : ''}
              onChange={(event) => {
                const provider = event.target.value;
                const models = modelOptionsForProvider(providers, provider, '');
                selectExistingProvider(provider, models[0] || '');
              }}
            >
              <option value="">{editingExistingModel ? '选择配置' : '正在新建'}</option>
              {providers.length === 0 ? <option value="">无模型</option> : null}
              {providers.map((provider) => (
                <option key={provider.name} value={provider.name}>{provider.label || provider.name}</option>
              ))}
            </select>
          </label>
          <button className="tool-button" disabled={busy} onClick={startNewModel} type="button">
            <Plus size={16} />
            新建
          </button>
        </div>
        {editingExistingModel ? (
          <label className="field-label">
            <span>当前模型</span>
            <select
              disabled={busy || !existingProvider || existingModels.length === 0}
              value={existingModel}
              onChange={(event) => selectExistingProvider(existingProvider, event.target.value)}
            >
              {existingModels.length === 0 ? <option value="">无模型</option> : null}
              {existingModels.map((model) => (
                <option key={model} value={model}>{model}</option>
              ))}
            </select>
          </label>
        ) : null}
        {editingExistingModel ? (
          <div className="model-test-note">
            编辑已有配置时，API key 留空会保留旧 key；额度用完会立即切换候选模型，只有网络中断会先按最大尝试次数重试。
          </div>
        ) : (
          <div className="model-test-note">
            新建只登记一个新的配置 ID，不会改变当前默认模型或任何创作阶段模型；配置 ID 只是本地名称，不是 API key。
          </div>
        )}
        {!editingExistingModel ? (
          <>
            <select
              disabled={busy}
              value={customModel.mode}
              onChange={(event) => setCustomModel((previous) => modelAddModeDefaults({ ...previous, mode: event.target.value }, providers, previous.mode))}
            >
              <option value="custom">自定义模型</option>
              <option value="preset">内置模板</option>
              <option value="codex_auth">Codex 登录</option>
              <option value="grok_oauth">Grok 登录</option>
            </select>
            <label className="model-field">
              <span>模板厂商</span>
              <select
                disabled={busy}
                value={customModel.mode === 'preset' ? customModel.preset : customModel.template_provider || 'custom'}
                onChange={(event) => {
                  const value = event.target.value;
                  const preset = providerPresets.find((item) => item.provider === value);
                  if (preset) {
                    setCustomModel((previous) => modelAddModeDefaults({ ...previous, mode: 'preset', preset: value }, providers, previous.mode));
                    return;
                  }
                  setCustomModel((previous) => modelAddModeDefaults({ ...previous, mode: 'custom', template_provider: value }, providers, previous.mode));
                }}
              >
                <option value="custom">custom</option>
                {providerPresets.map((preset) => (
                  <option key={preset.provider} value={preset.provider}>{preset.label}</option>
                ))}
              </select>
            </label>
          </>
        ) : null}
        <label className="model-field">
          <span>显示名称</span>
          <input
            disabled={busy}
            placeholder="例如 DeepSeek / Codex"
            value={providerEditorLabel}
            onChange={(event) => setCustomModel((previous) => ({ ...previous, label: event.target.value }))}
          />
        </label>
        <label className="model-checkbox-field">
          <input
            checked={Boolean(customModel.use_proxy)}
            disabled={busy}
            type="checkbox"
            onChange={(event) => setCustomModel((previous) => ({ ...previous, use_proxy: event.target.checked }))}
          />
          <span>使用代理访问</span>
        </label>
        <label className="model-field">
          <span>请求超时</span>
          <input
            disabled={busy}
            inputMode="numeric"
            min="1"
            type="number"
            value={customModel.request_timeout_seconds}
            onChange={(event) => setCustomModel((previous) => ({ ...previous, request_timeout_seconds: event.target.value }))}
          />
        </label>
        <label className="model-field">
          <span>连通性超时</span>
          <input
            disabled={busy}
            inputMode="numeric"
            min="1"
            type="number"
            value={customModel.connectivity_timeout_seconds}
            onChange={(event) => setCustomModel((previous) => ({ ...previous, connectivity_timeout_seconds: event.target.value }))}
          />
        </label>
        <label className="model-field">
          <span>网络中断最大尝试次数</span>
          <input
            disabled={busy}
            inputMode="numeric"
            max="11"
            min="1"
            type="number"
            value={customModel.network_disconnect_max_attempts}
            onChange={(event) => setCustomModel((previous) => ({ ...previous, network_disconnect_max_attempts: event.target.value }))}
          />
        </label>
        <label className="model-checkbox-field">
          <input
            checked={Boolean(customModel.auto_switch_candidate_pool)}
            disabled={busy}
            type="checkbox"
            onChange={(event) => setCustomModel((previous) => ({ ...previous, auto_switch_candidate_pool: event.target.checked }))}
          />
          <span>加入项目运行时自动切换</span>
        </label>
        {customModel.mode === 'existing' ? (
          <>
            <label className="model-field">
              <span>配置 ID (provider key)</span>
              <input
                disabled={busy}
                placeholder="例如 deepseek2；不是 API key"
                value={customModel.provider}
                onChange={(event) => setCustomModel((previous) => ({ ...previous, provider: event.target.value }))}
              />
            </label>
            <label className="model-field">
              <span>协议类型</span>
              <input
                disabled={busy}
                placeholder="openai / anthropic / gemini / grok"
                value={customModel.type}
                onChange={(event) => {
                  const type = event.target.value;
                  setCustomModel((previous) => ({
                    ...previous,
                    type,
                    api: type.trim().toLowerCase() === 'openai' ? (previous.api || 'chat') : ''
                  }));
                }}
              />
            </label>
            {existingUsesOpenAIEndpoint ? (
              <label className="model-field">
                <span>OpenAI endpoint</span>
                <select
                  disabled={busy}
                  value={customModel.api || 'chat'}
                  onChange={(event) => setCustomModel((previous) => ({ ...previous, api: event.target.value }))}
                >
                  <option value="chat">chat</option>
                  <option value="responses">responses</option>
                </select>
              </label>
            ) : null}
            <label className="model-field">
              <span>认证模式</span>
              <input
                disabled={busy}
                placeholder="api_key / grok_oauth"
                value={customModel.auth}
                onChange={(event) => setCustomModel((previous) => ({ ...previous, auth: event.target.value }))}
              />
            </label>
            <input
              disabled={busy}
              placeholder="API key 留空则保留旧 key"
              type="password"
              value={customModel.api_key}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, api_key: event.target.value }))}
            />
            <input
              disabled={busy}
              placeholder="base URL"
              value={customModel.base_url}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, base_url: event.target.value }))}
            />
          </>
        ) : null}
        {customModel.mode === 'preset' ? (
          <>
            <select
              disabled={busy}
              value={customModel.preset}
              onChange={(event) => setCustomModel((previous) => modelAddPresetDefaults({ ...previous, preset: event.target.value }))}
            >
              {providerPresets.map((preset) => (
                <option key={preset.provider} value={preset.provider}>{preset.label}</option>
              ))}
            </select>
            <label className="model-field">
              <span>配置 ID (provider key)</span>
              <input
                disabled={busy}
                placeholder="例如 deepseek2；不是 API key"
                value={customModel.provider || selectedPreset.provider}
                onChange={(event) => setCustomModel((previous) => ({ ...previous, provider: event.target.value }))}
              />
            </label>
            <input
              disabled={busy}
              placeholder={selectedPreset.requiresKey ? 'API key' : 'API key 可空'}
              type="password"
              value={customModel.api_key}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, api_key: event.target.value }))}
            />
            <input
              disabled={busy}
              placeholder="base URL"
              value={customModel.base_url}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, base_url: event.target.value }))}
            />
          </>
        ) : null}
        {customModel.mode === 'custom' ? (
          <>
            <label className="model-field">
              <span>配置 ID (provider key)</span>
              <input
                disabled={busy}
                placeholder="例如 deepseek2；不是 API key"
                value={customModel.provider}
                onChange={(event) => setCustomModel((previous) => ({ ...previous, provider: event.target.value }))}
              />
            </label>
            <select
              disabled={busy}
              value={customModel.type}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, type: event.target.value }))}
            >
              {customProviderTypes.map((type) => (
                <option key={type} value={type}>{type}</option>
              ))}
            </select>
            {customModel.type === 'openai' ? (
              <select
                disabled={busy}
                value={customModel.api}
                onChange={(event) => setCustomModel((previous) => ({ ...previous, api: event.target.value }))}
              >
                <option value="chat">chat</option>
                <option value="responses">responses</option>
              </select>
            ) : null}
            <input
              disabled={busy}
              placeholder="API key 可空"
              type="password"
              value={customModel.api_key}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, api_key: event.target.value }))}
            />
            <input
              disabled={busy}
              placeholder="base URL"
              value={customModel.base_url}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, base_url: event.target.value }))}
            />
          </>
        ) : null}
		{isCodexEditor ? (
          <>
            {customModel.mode === 'codex_auth' ? (
              <label className="model-field">
                <span>配置 ID (provider key)</span>
                <input
                  disabled={busy}
                  placeholder="例如 codex-login；不是 API key"
                  value={customModel.provider}
                  onChange={(event) => setCustomModel((previous) => ({ ...previous, provider: event.target.value }))}
                />
              </label>
            ) : null}
            <label className="model-field">
              <span>Codex auth.json</span>
              <input
                disabled={busy}
                placeholder="auth.json path (optional)"
                value={customModel.auth_file}
                onChange={(event) => setCustomModel((previous) => ({
                  ...previous,
                  auth_file: event.target.value,
                  auth_file_name: '',
                  codex_status: null
                }))}
              />
            </label>
            <div className="grok-action-grid">
				<label className={`tool-button codex-auth-upload ${busy ? 'disabled' : ''}`}>
					<Upload size={16} />
					上传 auth.json
					<input
						accept="application/json,.json"
						disabled={busy}
						onChange={onUploadCodexAuth}
						type="file"
					/>
				</label>
              <button className="tool-button" onClick={onRefreshCodexStatus} type="button">
                <RefreshCw size={16} />
				检查登录
              </button>
            </div>
			{customModel.auth_file_name ? <small>已上传：{customModel.auth_file_name}</small> : null}
            <div className={`grok-status ${codexReady ? 'ready' : ''}`}>
              <strong>{codexReady ? 'Codex 已登录' : 'Codex 未登录'}</strong>
              <span>{customModel.codex_message || codexAuthSummary(customModel.codex_status)}</span>
            </div>
          </>
        ) : null}
		{isGrokOAuthEditor ? (
          <>
            {customModel.mode === 'grok_oauth' ? (
              <label className="model-field">
                <span>配置 ID (provider key)</span>
                <input
                  disabled={busy}
                  placeholder="例如 grok-oauth-work；不是 API key"
                  value={customModel.provider}
                  onChange={(event) => setCustomModel((previous) => ({ ...previous, provider: event.target.value }))}
                />
              </label>
            ) : null}
            <input
              disabled={busy}
              placeholder="account ID"
              value={customModel.account_id}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, account_id: event.target.value, grok_status: null }))}
            />
            <input
              disabled={busy}
              placeholder="account name"
              value={customModel.account_name}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, account_name: event.target.value }))}
            />
            <div className="grok-action-grid">
              <button className="tool-button" onClick={onStartGrokLogin} type="button">
                <Play size={16} />
                Login
              </button>
              <button className="tool-button" onClick={onPollGrokLogin} type="button">
                <CircleDot size={16} />
                Poll
              </button>
              <button className="tool-button" onClick={onRefreshGrokStatus} type="button">
                <RefreshCw size={16} />
                Status
              </button>
              <button
                className="tool-button"
                disabled={!String(customModel.callback_input || '').trim()}
                onClick={onCompleteGrokLogin}
                type="button"
              >
                <Send size={16} />
                Complete
              </button>
            </div>
            {grokURL ? (
              <a className="grok-login-link" href={grokURL} rel="noreferrer" target="_blank">
                打开 Grok 授权页面
              </a>
            ) : null}
            <textarea
              disabled={busy}
              placeholder="callback URL / query string / one-time code"
              value={customModel.callback_input}
              onChange={(event) => setCustomModel((previous) => ({ ...previous, callback_input: event.target.value }))}
            />
            <div className={`grok-status ${grokReady ? 'ready' : ''}`}>
              <strong>{grokReady ? 'Grok 已登录' : 'Grok 未登录'}</strong>
              <span>{customModel.grok_message || grokAuthSummary(customModel.grok_status)}</span>
            </div>
          </>
        ) : null}
        <label className="model-field">
          <span>模型名称</span>
          {discoveredModels.length > 0 ? (
            <select
              aria-label="模型名称"
              disabled={busy}
              value={customModel.model || modelSuggestions[0] || ''}
			  onChange={(event) => setCustomModel((previous) => ({
				...previous,
				model: event.target.value,
				model_reasoning_effort: modelReasoningEffortForProvider(providers, editorModelProvider, event.target.value)
			  }))}
            >
              {modelSuggestions.map((model) => (
                <option key={model} value={model}>{model}</option>
              ))}
            </select>
          ) : (
            <input
              aria-label="模型名称"
              disabled={busy}
              placeholder="model"
			  value={customModel.model || (customModel.mode === 'preset' ? selectedPreset.model : isCodexEditor ? codexAuthDefaults.model : isGrokOAuthEditor ? grokOAuthDefaults.model : '')}
			  onChange={(event) => setCustomModel((previous) => ({ ...previous, model: event.target.value }))}
            />
          )}
        </label>
		{editorReasoningLevels ? (
		  <label className="model-field">
			<span>模型默认思考深度</span>
			<select
			  aria-label="模型默认思考深度"
			  disabled={busy}
			  value={customModel.model_reasoning_effort || ''}
			  onChange={(event) => setCustomModel((previous) => ({ ...previous, model_reasoning_effort: event.target.value }))}
			>
			  {editorReasoningLevels.map((level) => (
				<option key={level || 'provider-default'} value={level}>{level || '服务默认'}</option>
			  ))}
			</select>
			<small>{String(customModel.model || '').toLowerCase() === 'grok-4.5' ? 'Grok 4.5 支持 low / medium / high / xhigh，默认 high，不能关闭思考。' : '切换到该模型时自动使用此深度；项目或创作阶段可以单独覆盖。'}</small>
		  </label>
		) : null}
        {editorTestMessage ? (
          <div className={`model-test-note ${customModel.test_status || 'idle'}`}>
            {editorTestMessage}
          </div>
        ) : null}
        <div className="model-editor-actions">
          <button className="tool-button" disabled={busy || !canTestEditorConnection} onClick={() => onTestConnection()} type="button">
            <TestTube2 size={16} />
            测试并发现模型
          </button>
          <button className="tool-button" disabled={busy || !canTestEditorModel} onClick={() => onTestModel()} type="button">
            <ListRestart size={16} />
            模型测试
          </button>
          {customModel.mode === 'existing' ? (
            <button
              className="tool-button danger"
              disabled={busy || !canDeleteExistingModel}
              title={existingIsDefault ? '请先切换默认模型' : '删除配置'}
              onClick={() => onDeleteModel(existingModelPayload.provider, existingModelPayload.model)}
              type="button"
            >
              <Trash2 size={16} />
              删除配置
            </button>
          ) : null}
          <button className="tool-button accent" disabled={busy} title={canAdd ? '' : addValidationMessage} type="submit">
            <Plus size={16} />
            {customModel.mode === 'existing' ? '保存配置' : '保存模型'}
          </button>
        </div>
      </form>
      {modelConfig?.thinking_rule ? <div className="success-note">{modelConfig.thinking_rule}</div> : null}
    </div>
  );
}

function Metric({ label, value }) {
  return (
    <div className="metric">
      <span>{label}</span>
      <strong title={String(value)}>{String(value)}</strong>
    </div>
  );
}

function StatusPill({ status }) {
  return <span aria-live="polite" className={`status-pill ${status}`} role="status">{connectionStatusLabel(status)}</span>;
}

function connectionStatusLabel(status) {
  switch (status) {
    case 'live': return '已连接';
    case 'connecting': return '连接中';
    case 'reconnecting': return '正在重连';
    case 'offline': return '网络离线';
    case 'degraded': return '数据异常';
    default: return '未连接';
  }
}

function formatDate(value) {
  if (!value) {
    return '未访问';
  }
  return new Date(value).toLocaleDateString();
}

function formatTime(value) {
  if (!value) {
    return '--:--:--';
  }
  return new Date(value).toLocaleTimeString();
}

function eventFeedSummary(event) {
  const status = eventStatus(event);
  if (status === 'error' || status === 'warn') {
    const summary = event.event?.summary || '';
    const detail = event.event?.detail || '';
    if (summary && detail && summary !== detail) {
      return `${summary}：${detail}`;
    }
    return summary || detail || '无摘要';
  }
  return event.event?.summary || '无摘要';
}

function latestSimulationEvent(events) {
  return events.length ? events[events.length - 1] : null;
}

function workflowStatusText(status) {
  switch (status) {
    case 'running':
      return '进行中';
    case 'done':
      return '已完成';
    case 'error':
      return '出错';
    case 'paused':
      return '未完成';
    default:
      return '待处理';
  }
}

function coCreateTitle(kind) {
  switch (kind) {
    case 'stage':
      return '阶段共创';
    case 'adapt':
      return '改编共创';
    default:
      return '普通共创';
  }
}

function coCreateRoleLabel(role) {
  switch (role) {
    case 'assistant':
      return 'AI';
    case 'system':
      return '系统';
    default:
      return '你';
  }
}

function coCreateStatusText(status, ready, hasDraftPrompt = false) {
  if (status === 'running') {
    return '进行中';
  }
  if (status === 'waiting') {
    return '待补充';
  }
  if (status === 'deciding') {
    return '待决策';
  }
  if (status === 'error') {
    return '出错';
  }
  if (status === 'started') {
    return '已启动';
  }
  if (ready || status === 'ready' || hasDraftPrompt) {
    return '已就绪';
  }
  return '待处理';
}

function coCreateStatusDetail(coCreate) {
  if (coCreate.startMessage) {
    return coCreate.startMessage;
  }
  if (coCreate.canStart) {
    return 'draft prompt 已就绪';
  }
  if (coCreate.status === 'deciding') {
    const count = coCreate.briefing?.pending_decision_count || coCreate.pendingDecisions.length || 0;
    return count > 0 ? `还有 ${count} 个共创前置问题待确认` : '等待确认共创前置问题';
  }
  if (coCreate.failed || coCreate.status === 'error') {
    return '共创进度已保存，可恢复生成';
  }
  if (coCreate.draftPrompt?.trim()) {
    return '已有 draft，但还需继续补充最新方向';
  }
  if (coCreate.status === 'running') {
    return 'AI 正在整理方向';
  }
  if (coCreate.status === 'waiting') {
    return coCreate.suggestions.length ? '可点选 AI 建议，或直接补充你的想法' : '等待你补充方向';
  }
  return '等待共创开始';
}

function adaptationModeLabel(mode) {
  return adaptationModes.find((item) => item.value === mode)?.label || mode || '-';
}

function grokStatusFromLogin(login) {
  return login?.status || login?.Status || null;
}

function grokAuthorizeURL(login) {
  return String(login?.authorize_url || login?.AuthorizeURL || '').trim();
}

function openPendingGrokAuthWindow() {
  if (typeof window === 'undefined') {
    return null;
  }
  try {
    const authWindow = window.open('about:blank', '_blank');
    if (!authWindow) {
      return null;
    }
    authWindow.document.title = 'Grok OAuth';
    authWindow.document.body.innerHTML = '<main style="font-family: system-ui, sans-serif; padding: 24px; line-height: 1.5;"><strong>正在打开 Grok 授权页面...</strong><p>如果这个页面没有自动跳转，请回到 AINovel 点击授权链接。</p></main>';
    return authWindow;
  } catch {
    return null;
  }
}

function closeGrokAuthWindow(authWindow) {
  if (!authWindow) {
    return;
  }
  try {
    authWindow.close();
  } catch {
    // Ignore browser popup cleanup failures.
  }
}

function navigateGrokAuthWindow(authWindow, authorizeURL) {
  const url = String(authorizeURL || '').trim();
  if (!url) {
    closeGrokAuthWindow(authWindow);
    return false;
  }
  if (authWindow) {
    try {
      authWindow.opener = null;
      authWindow.location.replace(url);
      return true;
    } catch {
      closeGrokAuthWindow(authWindow);
    }
  }
  if (typeof window === 'undefined') {
    return false;
  }
  try {
    return Boolean(window.open(url, '_blank', 'noopener,noreferrer'));
  } catch {
    return false;
  }
}

function grokOpenMessage(openedAuthorize, browserOpenError) {
  if (openedAuthorize) {
    return '已打开 Grok 授权页面';
  }
  if (browserOpenError) {
    return `授权链接已生成；后端打开浏览器失败：${browserOpenError}`;
  }
  return '授权链接已生成，请点击打开 Grok 授权页面';
}

function grokLoginDone(login) {
  return Boolean(login?.done || login?.Done);
}

function grokLoginMessage(login) {
  const status = grokStatusFromLogin(login);
  return String(login?.message || login?.Message || status?.message || status?.Message || '').trim();
}

function grokLoggedIn(status) {
  return Boolean(status?.logged_in || status?.LoggedIn);
}

function grokAuthSummary(status) {
  if (!status) {
    return '点击 Status 检查当前账号';
  }
  if (grokLoggedIn(status)) {
    const account = status.account_id || status.AccountID || '';
    return account ? `已登录账号 ${account}` : '已登录';
  }
  const message = String(status.message || status.Message || '').trim();
  if (message) {
    return message;
  }
  const activeLogin = String(status.active_login || status.ActiveLogin || '').trim();
  if (activeLogin) {
    return `登录状态：${activeLogin}`;
  }
  if (status.needs_reauth || status.NeedsReauth) {
    return '需要登录或重新授权';
  }
  return '未登录';
}

function codexLoggedIn(status) {
  return Boolean(status?.logged_in || status?.LoggedIn);
}

function codexAuthSummary(status) {
  if (!status) {
    return '点击 Status 检查当前 Codex 登录';
  }
  const message = String(status.message || status.Message || '').trim();
  if (codexLoggedIn(status)) {
    const account = status.account_id || status.AccountID || '';
    return account ? `已登录账号 ${account}` : (message || '已登录');
  }
  return message || '未登录';
}

export function createNewModelDraft(previous = {}, providers = [], mode = 'custom') {
  return modelAddModeDefaults({
    ...createCustomModelState(),
    role: previous.role || 'default',
    mode,
    preset: previous.preset || 'deepseek'
  }, providers, 'existing');
}

export function modelAddModeDefaults(state, providers = [], previousMode = '') {
  if (state.mode === 'preset') {
    return modelAddPresetDefaults(state, providers, previousMode);
  }
  if (state.mode === 'custom') {
    const fromExisting = previousMode === 'existing' || Boolean(String(state.original_provider || '').trim());
    const provider = uniqueProviderKey(fromExisting ? 'custom-openai' : (state.provider || 'custom-openai'), providers);
    return {
      ...state,
      original_provider: '',
      provider,
      label: fromExisting ? 'Custom' : (state.label || 'Custom'),
      template_provider: state.template_provider || 'custom',
      type: state.type || 'openai',
      model: fromExisting ? '' : (state.model || ''),
	  model_reasoning_effort: '',
      api: state.api || 'chat',
      use_proxy: Boolean(state.use_proxy),
      auth: '',
      api_key: fromExisting ? '' : (state.api_key || ''),
      base_url: fromExisting ? '' : (state.base_url || ''),
      request_timeout_seconds: state.request_timeout_seconds || defaultModelRequestTimeoutSeconds,
      connectivity_timeout_seconds: state.connectivity_timeout_seconds || defaultModelConnectivityTimeoutSeconds,
      network_disconnect_max_attempts: state.network_disconnect_max_attempts || defaultModelNetworkDisconnectMaxAttempts,
      auto_switch_candidate_pool: Boolean(state.auto_switch_candidate_pool),
      test_status: 'idle',
      test_message: ''
    };
  }
  if (state.mode === 'codex_auth') {
    const fromExisting = previousMode === 'existing' || Boolean(String(state.original_provider || '').trim());
    const model = fromExisting || !state.model || state.model === 'model-name' ? codexAuthDefaults.model : state.model;
    const rawProvider = String(state.provider || '').trim();
    const baseProvider = fromExisting || !rawProvider.toLowerCase().includes('codex')
      ? codexAuthDefaults.provider
      : rawProvider;
    const provider = uniqueProviderKey(baseProvider, providers);
    return {
      ...state,
      original_provider: '',
      provider,
      label: fromExisting ? 'Codex' : (state.label || 'Codex'),
      template_provider: codexAuthDefaults.template_provider,
      type: codexAuthDefaults.type,
      auth: codexAuthDefaults.auth,
      model,
	  model_reasoning_effort: '',
      api: 'responses',
      use_proxy: true,
      api_key: '',
      base_url: codexAuthDefaults.base_url,
      auth_file: state.auth_file || '',
      auth_file_name: state.auth_file_name || '',
      request_timeout_seconds: state.request_timeout_seconds || defaultModelRequestTimeoutSeconds,
      connectivity_timeout_seconds: state.connectivity_timeout_seconds || defaultModelConnectivityTimeoutSeconds,
      network_disconnect_max_attempts: state.network_disconnect_max_attempts || defaultModelNetworkDisconnectMaxAttempts,
      auto_switch_candidate_pool: Boolean(state.auto_switch_candidate_pool),
      test_status: 'idle',
      test_message: ''
    };
  }
  if (state.mode === 'grok_oauth') {
    const fromExisting = previousMode === 'existing' || Boolean(String(state.original_provider || '').trim());
    const model = fromExisting || !state.model || state.model === 'model-name' ? grokOAuthDefaults.model : state.model;
    const rawProvider = String(state.provider || '').trim();
    const baseProvider = fromExisting || !rawProvider.toLowerCase().includes('grok')
      ? grokOAuthDefaults.provider
      : rawProvider;
    const provider = uniqueProviderKey(baseProvider, providers);
    return {
      ...state,
      original_provider: '',
      provider,
      label: fromExisting ? 'Grok' : (state.label || 'Grok'),
      template_provider: 'grok',
      type: grokOAuthDefaults.type,
      auth: grokOAuthDefaults.auth,
      model,
	  model_reasoning_effort: '',
      api: '',
      use_proxy: true,
      api_key: '',
      base_url: '',
      request_timeout_seconds: state.request_timeout_seconds || defaultModelRequestTimeoutSeconds,
      connectivity_timeout_seconds: state.connectivity_timeout_seconds || defaultModelConnectivityTimeoutSeconds,
      network_disconnect_max_attempts: state.network_disconnect_max_attempts || defaultModelNetworkDisconnectMaxAttempts,
      account_id: state.account_id || grokOAuthDefaults.account_id,
      account_name: state.account_name || grokOAuthDefaults.account_name,
      test_status: 'idle',
      test_message: ''
    };
  }
  return modelAddExistingProviderDefaults(state, providers);
}

function modelAddExistingProviderDefaults(state, providers = [], providerName = '', modelName = '') {
  const selectedName = String(providerName || state.original_provider || state.provider || '').trim();
  const provider = providers.find((item) => item.name === selectedName) || providers[0] || null;
  const providerKey = String(provider?.name || selectedName).trim();
  const models = Array.isArray(provider?.models) ? provider.models : [];
  const selectedModel = String(modelName || state.model || models[0] || '').trim();
  const api = providerUsesOpenAIEndpoint({ ...provider, provider: providerKey }) ? (provider?.api || 'chat') : '';
  return {
    ...state,
    mode: 'existing',
    original_provider: providerKey,
    provider: providerKey,
    label: provider?.label || '',
    template_provider: provider?.template_provider || '',
    type: provider?.type || '',
    auth: provider?.auth || '',
    auth_file: '',
    auth_file_name: '',
    api,
    api_key: '',
    base_url: provider?.base_url || '',
    model: selectedModel,
	model_reasoning_effort: String(provider?.model_reasoning_efforts?.[selectedModel] || '').trim(),
    use_proxy: provider?.use_proxy === true,
    request_timeout_seconds: providerNumberDraft(provider, 'request_timeout_seconds', 'requestTimeoutSeconds'),
    connectivity_timeout_seconds: providerNumberDraft(provider, 'connectivity_timeout_seconds', 'connectivityTimeoutSeconds'),
    network_disconnect_max_attempts: providerNumberDraft(provider, 'network_disconnect_max_attempts', 'networkDisconnectMaxAttempts'),
    auto_switch_candidate_pool: Boolean(provider?.auto_switch_candidate_pool || provider?.autoSwitchCandidatePool),
    discovered_models: []
  };
}

function providerNumberDraft(provider, ...keys) {
  const value = numberValue(provider, ...keys);
  return value > 0 ? String(value) : '';
}

export function modelOptionsForProvider(providers = [], providerName = '', currentModel = '') {
  const provider = providers.find((item) => item.name === providerName);
  const models = [...(provider?.models || [])];
  const selected = String(currentModel || '').trim();
  if (selected && !models.includes(selected)) {
    return [selected, ...models];
  }
  return models;
}

export function buildStageModelRouteOptions(providers = []) {
  return providers.flatMap((provider) => (provider.models || []).map((model) => ({
    provider: provider.name,
    model,
    value: modelRouteOptionValue(provider.name, model),
    label: `${provider.name} / ${model}`
  })));
}

function modelRouteOptionValue(provider, model) {
  return JSON.stringify([provider, model]);
}

export function resolveVisibleDefaultModel(activeProject, runtime, modelConfig) {
  const providers = modelConfig?.providers || [];
  const roles = modelConfig?.roles || [];
  const config = runtime?.config || {};
  const runtimeProvider = config.provider || providers[0]?.name || '';
  const runtimeModels = modelOptionsForProvider(providers, runtimeProvider, config.model);
  const runtimeModel = config.model || runtimeModels[0] || '';
  const route = roles.find((item) => item.role === 'default') || {
    role: 'default',
    provider: runtimeProvider,
    model: runtimeModel,
    explicit: false
  };
  const provider = activeProject?.id ? route.provider : runtimeProvider;
  const models = modelOptionsForProvider(providers, provider, activeProject?.id ? route.model : runtimeModel);
  const model = (activeProject?.id ? route.model : runtimeModel) || models[0] || '';
  return {
    route,
    provider,
    model,
    models
  };
}

function mergeModelOptions(...groups) {
  const seen = new Set();
  const out = [];
  for (const group of groups) {
    const values = Array.isArray(group) ? group : [group];
    for (const value of values) {
      const model = String(value || '').trim();
      if (!model || seen.has(model)) {
        continue;
      }
      seen.add(model);
      out.push(model);
    }
  }
  return out;
}

function modelReasoningEffortForProvider(providers = [], providerName = '', model = '') {
	const provider = providers.find((item) => item.name === providerName);
	return String(provider?.model_reasoning_efforts?.[model] || '').trim();
}

export function reasoningLevelsForModel(model = '', provider = {}) {
	const normalizedModel = String(model || '').trim().toLowerCase();
	const providerType = String(provider?.type || '').trim().toLowerCase();
	const providerAuth = String(provider?.auth || '').trim().toLowerCase();
	if (normalizedModel === 'grok-4.5') {
		return ['', 'low', 'medium', 'high', 'xhigh'];
	}
	if (normalizedModel.startsWith('gpt-') || providerAuth === 'codex' || (providerType === 'openai' && normalizedModel.includes('codex'))) {
		return ['', 'off', 'low', 'medium', 'high', 'xhigh'];
	}
	return null;
}

export function modelDiscoveryMessage(discovery = {}, models = []) {
  const count = Array.isArray(models) ? models.length : 0;
  if (discovery.status === 'error') {
    return discovery.message || '模型探测失败';
  }
  if (discovery.supported === false) {
    return count > 0
      ? `服务不支持在线探测，已加载 ${count} 个已配置模型`
      : '服务不支持在线探测，且没有已配置模型';
  }
  return count > 0
    ? `测试完成，发现 ${count} 个支持模型，请在“模型名称”下拉列表中选择`
    : '测试完成，但服务未返回可用模型';
}

function modelAddPresetDefaults(state, providers = [], previousMode = '') {
  const preset = providerPresets.find((item) => item.provider === state.preset) || providerPresets[0];
  const fromExisting = previousMode === 'existing' || Boolean(String(state.original_provider || '').trim());
  return {
    ...state,
    original_provider: '',
    provider: uniqueProviderKey(fromExisting ? preset.provider : (state.provider || preset.provider), providers),
    label: preset.label,
    template_provider: preset.provider,
    type: preset.type,
    auth: '',
    base_url: preset.base_url || '',
    model: preset.model,
	model_reasoning_effort: '',
    api: preset.api || 'chat',
    use_proxy: Boolean(preset.useProxy),
    api_key: fromExisting ? '' : (state.api_key || ''),
    request_timeout_seconds: state.request_timeout_seconds || defaultModelRequestTimeoutSeconds,
    connectivity_timeout_seconds: state.connectivity_timeout_seconds || defaultModelConnectivityTimeoutSeconds,
    network_disconnect_max_attempts: state.network_disconnect_max_attempts || defaultModelNetworkDisconnectMaxAttempts,
    auto_switch_candidate_pool: Boolean(state.auto_switch_candidate_pool),
    test_status: 'idle',
    test_message: ''
  };
}

function uniqueProviderKey(base, providers = []) {
  const configured = new Set(providers.map((provider) => String(provider.name || '').trim()).filter(Boolean));
  const seed = slugProviderKey(base, 'custom-openai');
  if (!configured.has(seed)) {
    return seed;
  }
  for (let index = 2; index < 1000; index += 1) {
    const candidate = `${seed}-${index}`;
    if (!configured.has(candidate)) {
      return candidate;
    }
  }
  return `${seed}-${Date.now()}`;
}

function slugProviderKey(value, fallback) {
  const slug = String(value || '')
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return slug || fallback;
}

function providerUsesOpenAIEndpoint(provider = {}) {
  const providerType = String(provider.type || '').trim().toLowerCase();
  const providerName = String(provider.provider || provider.name || '').trim().toLowerCase();
  const templateProvider = String(provider.template_provider || '').trim().toLowerCase();
  return providerType === 'openai' || (providerType === '' && (providerName === 'openai' || templateProvider === 'openai'));
}

function optionalModelTimeout(value) {
  const parsed = Number(String(value ?? '').trim());
  return Number.isInteger(parsed) && parsed > 0 ? parsed : 0;
}

function providerPayloadFields(state, fallback = {}) {
	const fields = {
    label: String(state.label || fallback.label || '').trim(),
    template_provider: String(state.template_provider || fallback.template_provider || fallback.provider || '').trim(),
    use_proxy: Boolean(state.use_proxy),
    request_timeout_seconds: optionalModelTimeout(state.request_timeout_seconds),
    connectivity_timeout_seconds: optionalModelTimeout(state.connectivity_timeout_seconds),
    network_disconnect_max_attempts: optionalModelTimeout(state.network_disconnect_max_attempts),
	auto_switch_candidate_pool: Boolean(state.auto_switch_candidate_pool)
  };
	if (Object.prototype.hasOwnProperty.call(state, 'model_reasoning_effort')) {
		fields.model_reasoning_effort = String(state.model_reasoning_effort || '').trim();
	}
	return fields;
}

export function buildExistingModelActionPayload(role, provider, model) {
  return {
    role: String(role || 'default').trim() || 'default',
    provider: String(provider || '').trim(),
    model: String(model || '').trim()
  };
}

export function modelAddSaveTarget(activeProject = null, payload = {}) {
  const projectId = String(activeProject?.id || '').trim();
  return {
    persistScope: 'global',
    projectId,
    refreshProjectModels: Boolean(projectId),
    selectProjectAfterSave: false
  };
}

export function buildModelAddPayload(state, modelConfig) {
  if (state.mode === 'existing') {
    const role = state.role || 'default';
    const providers = modelConfig?.providers || [];
    const provider = String(state.provider || providers[0]?.name || '').trim();
    const payload = {
      role,
      select_after_save: false,
      original_provider: String(state.original_provider || provider).trim(),
      provider,
      model: String(state.model || '').trim(),
      ...providerPayloadFields(state),
      type: String(state.type || '').trim(),
      auth: String(state.auth || '').trim(),
      api: providerUsesOpenAIEndpoint({ ...state, provider }) ? String(state.api || 'chat').trim() : '',
      base_url: String(state.base_url || '').trim()
    };
		const apiKey = String(state.api_key || '').trim();
		if (apiKey) {
			payload.api_key = apiKey;
		}
		if (String(state.auth || '').trim().toLowerCase() === 'codex') {
			const authFile = String(state.auth_file || '').trim();
			if (authFile) {
				payload.auth_file = authFile;
			}
		}
		if (String(state.auth || '').trim().toLowerCase() === 'grok_oauth') {
			payload.account_id = String(state.account_id || grokOAuthDefaults.account_id).trim();
		}
		return payload;
  }
  if (state.mode === 'grok_oauth') {
    return {
      select_after_save: false,
      provider: String(state.provider || grokOAuthDefaults.provider).trim(),
      model: String(state.model || grokOAuthDefaults.model).trim(),
      ...providerPayloadFields({ ...state, use_proxy: state.use_proxy ?? true }, { label: 'Grok', provider: 'grok' }),
      type: grokOAuthDefaults.type,
      auth: grokOAuthDefaults.auth,
      account_id: String(state.account_id || grokOAuthDefaults.account_id).trim()
    };
  }
  if (state.mode === 'codex_auth') {
    const payload = {
      select_after_save: false,
      provider: String(state.provider || codexAuthDefaults.provider).trim(),
      model: String(state.model || codexAuthDefaults.model).trim(),
      ...providerPayloadFields({ ...state, use_proxy: state.use_proxy ?? true }, { label: 'Codex', provider: 'codex' }),
      type: codexAuthDefaults.type,
      auth: codexAuthDefaults.auth,
      api: 'responses',
      base_url: String(state.base_url || codexAuthDefaults.base_url).trim()
    };
    const authFile = String(state.auth_file || '').trim();
    if (authFile) {
      payload.auth_file = authFile;
    }
    return payload;
  }
  if (state.mode === 'preset') {
    const preset = providerPresets.find((item) => item.provider === state.preset) || providerPresets[0];
    return {
      select_after_save: false,
      provider: String(state.provider || preset.provider).trim(),
      model: String(state.model || preset.model).trim(),
      ...providerPayloadFields(state, { label: preset.label, provider: preset.provider }),
      type: preset.type,
      api: preset.api || '',
      api_key: String(state.api_key || '').trim(),
      base_url: String(state.base_url || preset.base_url || '').trim()
    };
  }
  return {
    select_after_save: false,
    provider: String(state.provider || '').trim(),
    model: String(state.model || '').trim(),
    ...providerPayloadFields(state, { label: 'Custom', provider: 'custom' }),
    type: String(state.type || 'openai').trim(),
    api: providerUsesOpenAIEndpoint(state) ? String(state.api || 'chat').trim() : '',
    api_key: String(state.api_key || '').trim(),
    base_url: String(state.base_url || '').trim()
  };
}

export function canSubmitModelAdd(state, modelConfig) {
  return modelAddValidationMessage(state, modelConfig) === '';
}

export function modelAddValidationMessage(state, modelConfig) {
  const payload = buildModelAddPayload(state, modelConfig);
  const provider = String(payload.provider || '').trim();
  const model = String(payload.model || '').trim();
  const hasProviderList = Array.isArray(modelConfig?.providers);
  const providers = hasProviderList ? modelConfig.providers : [];
  const providerExists = (name) => providers.some((item) => String(item.name || '').trim() === name);
  if (!provider) {
    return '请填写配置 ID (provider key)。';
  }
  if (!model) {
    return '请填写模型名称。';
  }
  if (state.mode === 'existing') {
    const originalProvider = String(payload.original_provider || '').trim();
    if (!originalProvider || (hasProviderList && !providerExists(originalProvider))) {
      return '请选择要编辑的已有配置。';
    }
    if (provider !== originalProvider && providerExists(provider)) {
      return `配置 ID "${provider}" 已存在，请换一个 ID。`;
    }
  } else if (providerExists(provider)) {
    return `配置 ID "${provider}" 已存在，请换一个 ID，或从“已有配置”里编辑它。`;
  }
  if (!payload.provider || !payload.model) {
    return '请填写配置 ID 和模型名称。';
  }
  if (state.mode === 'grok_oauth') {
    if (!payload.account_id) {
      return '请填写 Grok 账号 ID。';
    }
    if (!grokLoggedIn(state.grok_status)) {
      return '请先完成 Grok 登录，再保存配置。';
    }
  }
  if (state.mode === 'codex_auth' && !codexLoggedIn(state.codex_status)) {
    return '请先确认 Codex 登录状态，再保存配置。';
  }
  if (state.mode === 'preset') {
    const preset = providerPresets.find((item) => item.provider === state.preset) || providerPresets[0];
    if (preset.requiresKey && !payload.api_key) {
      return '请填写 API key。';
    }
  }
  if (state.mode === 'custom' && !payload.base_url) {
    return '请填写 base URL。';
  }
  if (state.mode === 'custom' && !payload.type) {
    return '请选择协议类型。';
  }
  return '';
}

export function buildBeginCoCreatePayload({ kind, initial = '', fallbackInitial = '', sourceFile = '', mode = '', targetTotalWords = 0 } = {}) {
  const payload = {
    kind,
    initial: String(initial || fallbackInitial || '').trim()
  };
  if (kind === 'adapt') {
    payload.source_file = String(sourceFile || '').trim();
    payload.mode = String(mode || '').trim();
    return payload;
  }
  if (kind === 'normal' && targetTotalWords > 0) {
    payload.target_total_words = targetTotalWords;
  }
  return payload;
}

export function resolveCoCreateTargetTotalWords(state = {}) {
  const choice = String(state.targetTotalWordsChoice || '');
  const raw = choice === 'custom' ? state.customTargetTotalWords : choice;
  const value = Number(String(raw ?? '').trim());
  return Number.isInteger(value) && value > 0 ? value : 0;
}

export function resolveCoCreateStructureChoice(state = {}) {
  const choice = String(state.structureChoice || 'single');
  return coCreateStructureChoices.some((item) => item.value === choice) ? choice : 'single';
}

export function inferCoCreateIntakeFromInitial(initial = '') {
  const text = String(initial || '').trim();
  const targetTotalWords = explicitTotalWordsFromText(text);
  const structureChoice = inferCoCreateStructureFromText(text, targetTotalWords);
  const wordChoice = coCreateWordChoiceFromTarget(targetTotalWords);
  return {
    targetTotalWords,
    targetTotalWordsChoice: wordChoice.choice,
    customTargetTotalWords: wordChoice.custom,
    structureChoice
  };
}

export function buildCoCreateIntakeMessages(initial = '') {
  const content = String(initial || '').trim();
  return [
    {
      id: 'intake-user',
      role: 'user',
      content
    },
    {
      id: 'intake-assistant',
      role: 'assistant',
      content: '开始前先确认两个问题：目标字数是多少？结构要不分章节、由 AI 判断，还是明确分章节？'
    }
  ];
}

export function buildCoCreateIntakeInitial(initial = '', options = {}) {
  const targetTotalWords = Number(options.targetTotalWords || 0);
  const structureChoice = resolveCoCreateStructureChoice({ structureChoice: options.structureChoice });
  const structureText = {
    single: '不分章节，一气呵成；如工具必须保存 outline，只保存 1 个正文条目',
    auto: '由 AI 根据总字数自动判断章节数',
    chapters: '分章节；常规单章正文按 3000-5000 字估算'
  }[structureChoice];
  const shortRule = targetTotalWords > 0 && targetTotalWords <= 8000
    ? '\n- 该目标属于短篇篇幅；除非用户明确选择分章节，否则按一篇连续短篇规划，不要拆成多个章节。'
    : '';
  return `${String(initial || '').trim()}\n\n[共创前确认]\n- target_total_words=${targetTotalWords}，这是全书总字数，不是每章字数。\n- 结构偏好：${structureText}。\n- 常规小说单章正文约 3000-5000 字；规划章节数必须按总字数估算。${shortRule}`;
}

function explicitTotalWordsFromText(text) {
  const normalized = normalizeCoCreateNumberText(text);
  const patterns = [
    /([0-9]+(?:\.[0-9]+)?)\s*(万|萬|w|W|k|K|千)?\s*(?:字|words?|runes?)/g,
    /([0-9]+(?:\.[0-9]+)?)\s*(万|萬|w|W|k|K|千)/g
  ];
  for (const pattern of patterns) {
    pattern.lastIndex = 0;
    let match;
    while ((match = pattern.exec(normalized)) !== null) {
      if (coCreateWordCountLooksPerChapter(normalized, match.index, pattern.lastIndex)) {
        continue;
      }
      const words = coCreateNumberWithUnitToWords(match[1], match[2] || '');
      if (words > 0) {
        return words;
      }
    }
  }
  return 0;
}

function inferCoCreateStructureFromText(text, targetTotalWords) {
  if (/不分章|不分章节|不要分章|不要分章节|单篇|一篇/.test(text)) {
    return 'single';
  }
  if (/分章|分章节|章节|章回/.test(text)) {
    return 'chapters';
  }
  if (/短篇/.test(text)) {
    return 'single';
  }
  if (/中篇|长篇|長篇/.test(text)) {
    return 'auto';
  }
  return targetTotalWords > 0 && targetTotalWords <= 8000 ? 'single' : 'auto';
}

function coCreateWordChoiceFromTarget(targetTotalWords) {
  const value = String(Number(targetTotalWords || 0));
  if (coCreateTargetWordChoices.some((choice) => choice.value === value)) {
    return { choice: value, custom: '' };
  }
  return targetTotalWords > 0 ? { choice: 'custom', custom: value } : { choice: '', custom: '' };
}

function normalizeCoCreateNumberText(text) {
  return String(text || '')
    .replace(/[，,](?=\d{3}\b)/g, '')
    .replace(/[０-９]/g, (char) => String.fromCharCode(char.charCodeAt(0) - 0xfee0))
    .replace(/[零〇一二两兩三四五六七八九十百千]+(?:万|萬)?(?=\s*(?:字|words?|runes?|万|萬|w|W|k|K|千))/g, (match) => {
      const value = chineseNumberToArabic(match);
      return value > 0 ? String(value) : match;
    });
}

function coCreateWordCountLooksPerChapter(text, start, end) {
  const window = text.slice(Math.max(0, start - 8), Math.min(text.length, end + 8));
  return /每章|单章|單章|章字数|章节字数|\/章|per\s+chapter|each\s+chapter/i.test(window);
}

function coCreateNumberWithUnitToWords(rawNumber, rawUnit) {
  const value = Number(String(rawNumber || '').trim());
  if (!Number.isFinite(value) || value <= 0) {
    return 0;
  }
  const unit = String(rawUnit || '').trim();
  const multiplier = {
    万: 10000,
    萬: 10000,
    w: 10000,
    W: 10000,
    k: 1000,
    K: 1000,
    千: 1000
  }[unit] || 1;
  return Math.round(value * multiplier);
}

function chineseNumberToArabic(text) {
  const digits = {
    零: 0,
    〇: 0,
    一: 1,
    二: 2,
    两: 2,
    兩: 2,
    三: 3,
    四: 4,
    五: 5,
    六: 6,
    七: 7,
    八: 8,
    九: 9
  };
  const units = { 十: 10, 百: 100, 千: 1000 };
  let total = 0;
  let section = 0;
  let number = 0;
  for (const char of String(text || '')) {
    if (Object.prototype.hasOwnProperty.call(digits, char)) {
      number = digits[char];
      continue;
    }
    if (char === '万' || char === '萬') {
      section += number;
      total += (section || 1) * 10000;
      section = 0;
      number = 0;
      continue;
    }
    const unit = units[char];
    if (unit) {
      section += (number || 1) * unit;
      number = 0;
    }
  }
  return total + section + number;
}

export function getSimulationProfileStatus(snapshot) {
  const summary = objectValue(snapshot, 'SimulationSummary', 'simulationSummary', 'simulation_summary');
  const featureCounts = objectValue(summary, 'FeatureCounts', 'featureCounts', 'feature_counts') || {};
  const actions = objectValue(summary, 'Actions', 'actions') || {};
  return {
    loaded: Boolean(valueByKey(summary, 'Loaded', 'loaded') || summary),
    version: textValue(summary, 'Version', 'version'),
    profileDigest: textValue(summary, 'ProfileDigest', 'profileDigest', 'profile_digest'),
    updatedAt: textValue(summary, 'UpdatedAt', 'updatedAt', 'updated_at'),
    sourceCount: numberValue(summary, 'SourceCount', 'sourceCount', 'source_count'),
    reportCount: numberValue(summary, 'ReportCount', 'reportCount', 'report_count'),
    coveragePercent: numberValue(summary, 'CoveragePercent', 'coveragePercent', 'coverage_percent'),
    healthState: textValue(summary, 'HealthState', 'healthState', 'health_state') || 'unknown',
    healthReasons: arrayValue(summary, 'HealthReasons', 'healthReasons', 'health_reasons'),
    portable: Boolean(valueByKey(summary, 'Portable', 'portable')),
    localEvidence: Boolean(valueByKey(summary, 'LocalEvidence', 'localEvidence', 'local_evidence')),
    safetyIndex: Boolean(valueByKey(summary, 'SafetyIndex', 'safetyIndex', 'safety_index')),
    analysisSigned: Boolean(valueByKey(summary, 'AnalysisSigned', 'analysisSigned', 'analysis_signed')),
    synthesisSigned: Boolean(valueByKey(summary, 'SynthesisSigned', 'synthesisSigned', 'synthesis_signed')),
    selectedMode: normalizeSimulationMode(textValue(summary, 'SelectedMode', 'selectedMode', 'selected_mode')),
    effectiveMode: normalizeSimulationMode(textValue(summary, 'EffectiveMode', 'effectiveMode', 'effective_mode')),
    effectiveReason: textValue(summary, 'EffectiveReason', 'effectiveReason', 'effective_reason'),
    featureCounts: {
      total: numberValue(featureCounts, 'Total', 'total'),
      stable: numberValue(featureCounts, 'Stable', 'stable'),
      local: numberValue(featureCounts, 'Local', 'local'),
      outlier: numberValue(featureCounts, 'Outlier', 'outlier'),
      contradictory: numberValue(featureCounts, 'Contradictory', 'contradictory')
    },
    contract: normalizeSimulationContractSummary(objectValue(summary, 'Contract', 'contract')),
    check: normalizeSimulationCheckSummary(objectValue(summary, 'Check', 'check')),
    actions: {
      rescan: normalizeSimulationCapability(objectValue(actions, 'Rescan', 'rescan')),
      resynthesize: normalizeSimulationCapability(objectValue(actions, 'Resynthesize', 'resynthesize')),
      reanalyze: normalizeSimulationCapability(objectValue(actions, 'Reanalyze', 'reanalyze'))
    },
    modePreviews: normalizeSimulationModePreviews(arrayValue(summary, 'ModePreviews', 'modePreviews', 'mode_previews')),
    diagnosticError: textValue(summary, 'DiagnosticError', 'diagnosticError', 'diagnostic_error')
  };
}

export function simulationProfileSummaryText(profile) {
  if (!profile?.loaded) {
    return '上传或导入画像后会出现在这里';
  }
  if (!profile.healthState && !Number.isFinite(profile.reportCount)) {
    return profile.sourceCount ? `${profile.sourceCount} 篇语料` : '画像已加载';
  }
  const reportCount = Number.isFinite(profile.reportCount) ? profile.reportCount : 0;
  const coverage = profile.sourceCount ? `${reportCount}/${profile.sourceCount} 篇报告` : '无本地语料统计';
  return `${simulationHealthLabel(profile.healthState)} · ${coverage}`;
}

export function simulationProfileDisplayState({ profile = {}, analysisStatus, latestAnalysis } = {}) {
  const running = String(analysisStatus || '').toLowerCase() === 'running';
  if (running) {
    const progress = latestAnalysis && Number(latestAnalysis.total) > 0
      ? `${Number(latestAnalysis.current) || 0}/${Number(latestAnalysis.total)}`
      : '';
    const message = String(latestAnalysis?.message || '').trim();
    const summary = message || (progress
      ? `正在分析语料 ${progress}；完成后将自动刷新画像`
      : '正在分析语料；完成后将自动刷新画像');
    return {
      running: true,
      tone: 'running',
      label: profile.loaded ? '正在重新分析' : '正在生成画像',
      summary
    };
  }
  return {
    running: false,
    tone: simulationHealthTone(profile.healthState),
    label: profile.loaded ? simulationHealthLabel(profile.healthState) : '未加载',
    summary: simulationProfileSummaryText(profile)
  };
}

function normalizeSimulationCapability(capability) {
  if (!capability) return null;
  return {
    enabled: Boolean(valueByKey(capability, 'Enabled', 'enabled')),
    reason: textValue(capability, 'Reason', 'reason')
  };
}

function normalizeSimulationContractSummary(contract) {
  if (!contract) return null;
  return {
    revision: numberValue(contract, 'Revision', 'revision'),
    status: textValue(contract, 'Status', 'status'),
    current: Boolean(valueByKey(contract, 'Current', 'current')),
    staleReason: textValue(contract, 'StaleReason', 'staleReason', 'stale_reason'),
    profileDigest: textValue(contract, 'ProfileDigest', 'profileDigest', 'profile_digest'),
    foundationRevision: numberValue(contract, 'FoundationRevision', 'foundationRevision', 'foundation_revision'),
    creativeBriefBound: Boolean(valueByKey(contract, 'CreativeBriefBound', 'creativeBriefBound', 'creative_brief_bound')),
    exclusionCount: numberValue(contract, 'ExclusionCount', 'exclusionCount', 'exclusion_count'),
    exclusionReasons: objectValue(contract, 'ExclusionReasons', 'exclusionReasons', 'exclusion_reasons'),
    views: arrayValue(contract, 'Views', 'views').map((view) => ({
      role: textValue(view, 'Role', 'role'),
      phase: textValue(view, 'Phase', 'phase'),
      must: numberValue(view, 'Must', 'must'),
      should: numberValue(view, 'Should', 'should'),
      avoid: numberValue(view, 'Avoid', 'avoid'),
      byteBudget: numberValue(view, 'ByteBudget', 'byteBudget', 'byte_budget'),
      features: arrayValue(view, 'Features', 'features').map((feature) => ({
        id: textValue(feature, 'ID', 'Id', 'id'),
        dimension: textValue(feature, 'Dimension', 'dimension'),
        statement: textValue(feature, 'Statement', 'statement'),
        level: textValue(feature, 'Level', 'level')
      }))
    }))
  };
}

function normalizeSimulationCheckSummary(check) {
  if (!check) return null;
  return {
    state: textValue(check, 'State', 'state'),
    reason: textValue(check, 'Reason', 'reason'),
    chapter: numberValue(check, 'Chapter', 'chapter'),
    checkedAt: textValue(check, 'CheckedAt', 'checkedAt', 'checked_at'),
    draftCurrent: Boolean(valueByKey(check, 'DraftCurrent', 'draftCurrent', 'draft_current')),
    capability: textValue(check, 'Capability', 'capability'),
    capabilityReason: textValue(check, 'CapabilityReason', 'capabilityReason', 'capability_reason'),
    copyStatus: textValue(check, 'CopyStatus', 'copyStatus', 'copy_status'),
    contractStatus: textValue(check, 'ContractStatus', 'contractStatus', 'contract_status'),
    riskCount: numberValue(check, 'RiskCount', 'riskCount', 'risk_count'),
    mustTotal: numberValue(check, 'MustTotal', 'mustTotal', 'must_total'),
    mustMet: numberValue(check, 'MustMet', 'mustMet', 'must_met'),
    mustMissing: numberValue(check, 'MustMissing', 'mustMissing', 'must_missing'),
    advisoryCount: numberValue(check, 'AdvisoryCount', 'advisoryCount', 'advisory_count'),
    risks: arrayValue(check, 'Risks', 'risks').map((risk) => ({
      type: textValue(risk, 'Type', 'type'),
      draftExcerpt: textValue(risk, 'DraftExcerpt', 'draftExcerpt', 'draft_excerpt'),
      startRune: numberValue(risk, 'StartRune', 'startRune', 'start_rune'),
      lengthRunes: numberValue(risk, 'LengthRunes', 'lengthRunes', 'length_runes')
    }))
  };
}

function normalizeSimulationModePreviews(previews) {
  return previews.map((preview) => ({
    mode: normalizeSimulationMode(textValue(preview, 'Mode', 'mode')),
    status: textValue(preview, 'Status', 'status'),
    reason: textValue(preview, 'Reason', 'reason'),
    roles: arrayValue(preview, 'Roles', 'roles').map((role) => ({
      role: textValue(role, 'Role', 'role'),
      phase: textValue(role, 'Phase', 'phase'),
      featureCount: numberValue(role, 'FeatureCount', 'featureCount', 'feature_count'),
      must: numberValue(role, 'Must', 'must'),
      should: numberValue(role, 'Should', 'should'),
      avoid: numberValue(role, 'Avoid', 'avoid'),
      byteBudget: numberValue(role, 'ByteBudget', 'byteBudget', 'byte_budget'),
      dimensions: arrayValue(role, 'Dimensions', 'dimensions')
    }))
  }));
}

export function simulationHealthLabel(state) {
  return ({
    fresh: '新鲜可用',
    stale: '已过期',
    portable_only: '仅 portable',
    legacy: 'legacy 兼容',
    invalid: '无效',
    unknown: '状态未知'
  })[String(state || '').toLowerCase()] || '状态未知';
}

export function simulationHealthTone(state) {
  if (state === 'fresh') return 'done';
  if (state === 'stale' || state === 'invalid') return 'error';
  if (state === 'portable_only' || state === 'legacy') return 'paused';
  return 'idle';
}

export function simulationContractStatusLabel(status) {
  return ({ active: '已生效', degraded: '降级生效', inactive: '未生效' })[String(status || '').toLowerCase()] || '状态未知';
}

export function projectSimulationModeNotice({ currentMode, profile = {}, selectedMode } = {}) {
  if (normalizeSimulationMode(selectedMode) !== 'reinforced') {
    return null;
  }
  const contractStatus = String(profile.contract?.status || '').toLowerCase();
  const reason = profile.effectiveReason || profile.contract?.staleReason || '需要可用画像与当前共享契约';
  const portable = profile.healthState === 'portable_only' || !profile.localEvidence;
  if (normalizeSimulationMode(currentMode) !== 'reinforced') {
    return {
      tone: 'warning',
      message: profile.loaded && portable
        ? '保存后将以降级强化模式生效：portable 画像会使用更高的角色建议预算；从含原语料的画像库重新加载并分析后，才会恢复本地证据与完整安全检查。'
        : '强化仿写尚未保存；保存后系统会重新生成当前共享契约。'
    };
  }
  const effective = profile.loaded &&
    profile.selectedMode === 'reinforced' &&
    profile.effectiveMode === 'reinforced' &&
    profile.contract?.current &&
    contractStatus !== 'inactive';
  if (!effective) {
    return {
      tone: 'warning',
      message: `强化仿写尚未生效：${reason}。`
    };
  }
  if (contractStatus === 'degraded') {
    return {
      tone: 'warning',
      message: `强化仿写已降级生效：${reason}。当前仍使用强化预算，但缺失的本地证据能力不会被冒充为可用。`
    };
  }
  return null;
}

export function simulationCheckStateLabel(state) {
  return ({
    pass: '检测通过',
    partial: '部分能力',
    not_run: '未运行',
    stale: '结果过期',
    fail: '检测失败',
    error: '检查不可用'
  })[String(state || '').toLowerCase()] || '状态未知';
}

function simulationRoleLabel(role) {
  return ({ architect: 'Architect', writer: 'Writer', editor: 'Editor' })[String(role || '').toLowerCase()] || role || '未知角色';
}

function formatSimulationTimestamp(value) {
  if (!value) return '未知';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function formatReasonCounts(reasons) {
  if (!reasons || typeof reasons !== 'object') return '';
  const entries = Object.entries(reasons);
  return entries.length ? `（${entries.map(([reason, count]) => `${reason} ${count}`).join('；')}）` : '';
}

export function isSimulationProfileActionBusy(simulation = {}) {
  return String(simulation.analysisStatus || '').toLowerCase() === 'running' ||
    String(simulation.importStatus || '').toLowerCase() === 'running';
}

export function isCoCreateRequestBusy(coCreate = {}) {
  return String(coCreate.status || '').toLowerCase() === 'running';
}

export function canCancelCoCreateFlow({ activeProject, busy = false, coCreate = {} } = {}) {
  const messages = Array.isArray(coCreate.messages) ? coCreate.messages : [];
  const hasSession = coCreate.active || coCreate.intakeActive || messages.length > 0;
  return Boolean(activeProject && hasSession && (!busy || isCoCreateRequestBusy(coCreate)));
}

export function canRunSimulationAnalysis({ activeProject, busy, simulation } = {}) {
  return Boolean(activeProject && !isSimulationProfileActionBusy(simulation));
}

export function canRunSimulationPrimaryAnalysis({ activeProject, profile = {}, simulation = {} } = {}) {
  if (!canRunSimulationAnalysis({ activeProject, simulation })) {
    return false;
  }
  const fileCount = Array.isArray(simulation.files) ? simulation.files.length : 0;
  if (fileCount === 0) {
    return false;
  }
  if (!profile.loaded) {
    return true;
  }
  const sourceCount = Number(profile.sourceCount) || 0;
  const reportCount = Number(profile.reportCount) || 0;
  return profile.healthState !== 'fresh' || sourceCount !== fileCount || reportCount !== sourceCount;
}

export function simulationAnalysisActionLabel({ analysisStatus, fileCount, profileLoaded, scanAvailable = true } = {}) {
  if (String(analysisStatus || '').toLowerCase() === 'running') {
    return '正在分析并入库…';
  }
  if ((Number(fileCount) || 0) === 0) {
    return '请先上传语料';
  }
  if (profileLoaded && !scanAvailable) {
    return '画像已是最新';
  }
  return profileLoaded ? '重新扫描并入库' : '开始分析并入库';
}

export function simulationAnalysisNextStep({ analysisStatus, fileCount, profileLoaded, scanAvailable = true } = {}) {
  const sourceCount = Number.isFinite(Number(fileCount)) ? Math.max(0, Number(fileCount)) : 0;
  if (String(analysisStatus || '').toLowerCase() === 'running') {
    return `正在分析 ${sourceCount} 份语料并更新画像库，请等待完成。`;
  }
  if (sourceCount === 0) {
    return profileLoaded
      ? '当前画像没有绑定本地语料；请先上传旧语料，再点击“重新扫描并入库”。'
      : '先上传 TXT 或 Markdown 语料，再点击“开始分析并入库”。';
  }
  if (profileLoaded && !scanAvailable) {
    return `全部 ${sourceCount} 份语料已按当前流程完成分析，无需重复扫描。`;
  }
  if (profileLoaded) {
    return `已准备 ${sourceCount} 份语料；点击“重新扫描并入库”只分析旧版本、新增或变化的语料，并自动更新画像库。`;
  }
  return `已准备 ${sourceCount} 份语料，请点击“开始分析并入库”生成画像并自动保存到画像库。`;
}

export function canRunAdaptationAnalysis({ activeProject, busy, adaptation } = {}) {
  return Boolean(activeProject && adaptation?.sourceFile?.relative_path && adaptation.analysisStatus !== 'running' && adaptation.uploadStatus !== 'running');
}

export function adaptationAnalysisDisabledReason({ activeProject, busy, adaptation } = {}) {
  if (!activeProject) return '请先选择项目';
  if (!adaptation?.sourceFile?.relative_path) return '请先上传或从小说仓库加载原文';
  if (adaptation.uploadStatus === 'running') return '原文仍在上传';
  if (adaptation.analysisStatus === 'running') return '原文分析正在进行';
  return '';
}

export function adaptationRebriefDisabledReason({
  activeProject,
  busy,
  coCreate,
  hasBackendSession,
  hasPendingDecisions
} = {}) {
  if (!activeProject) return '请先选择项目';
  if (coCreate?.kind !== 'adapt') return '仅改编共创可以重新生成简报';
  if (!hasBackendSession) return '请先开始改编共创';
  if (busy) return 'AI 正在处理当前任务';
  if (hasPendingDecisions) return '请先处理全部共创前置决策';
  if (!coCreate?.input?.trim()) return '请输入希望重新整理的方向';
  return '';
}

export function canSaveAnalyzedNovelToLibrary({ activeProject, busy, adaptation } = {}) {
  return Boolean(activeProject && adaptation?.sourceFile?.relative_path && adaptation.analysisStatus === 'done' && !busy);
}

export function getCreativeBlueprint(snapshot) {
  const summary = objectValue(snapshot, 'CreativeBlueprint', 'creativeBlueprint', 'creative_blueprint');
  const outline = getSnapshotOutlineRows(snapshot);
  const loaded = Boolean(valueByKey(summary, 'Loaded', 'loaded') || summary || outline.length);
  return {
    loaded,
    novelName: textValue(summary, 'NovelName', 'novelName', 'novel_name') ||
      textValue(snapshot, 'NovelName', 'novelName', 'novel_name'),
    premise: textValue(summary, 'Premise', 'premise') ||
      textValue(snapshot, 'Premise', 'premise'),
    outlineChapters: numberValue(summary, 'OutlineChapters', 'outlineChapters', 'outline_chapters') || outline.length,
    characterCount: numberValue(summary, 'CharacterCount', 'characterCount', 'character_count') ||
      arrayValue(snapshot, 'CharacterDetails', 'characterDetails', 'character_details').length,
    worldRuleCount: numberValue(summary, 'WorldRuleCount', 'worldRuleCount', 'world_rule_count') ||
      arrayValue(snapshot, 'WorldRules', 'worldRules', 'world_rules').length,
    layered: Boolean(valueByKey(summary, 'Layered', 'layered') || valueByKey(snapshot, 'Layered', 'layered')),
    compassDirection: textValue(summary, 'CompassDirection', 'compassDirection', 'compass_direction') ||
      textValue(snapshot, 'CompassDirection', 'compassDirection', 'compass_direction'),
    compassScale: textValue(summary, 'CompassScale', 'compassScale', 'compass_scale') ||
      textValue(snapshot, 'CompassScale', 'compassScale', 'compass_scale')
  };
}

export function getCoCreatePlanningReview(snapshot) {
	const adaptationReview = objectValue(snapshot, 'AdaptationFoundationReview', 'adaptationFoundationReview', 'adaptation_foundation_review');
	const adaptationState = textValue(adaptationReview, 'State', 'state');
	const adaptationActive = ['pending', 'generating', 'readonly'].includes(adaptationState);
	const review = adaptationActive ? adaptationReview : objectValue(snapshot, 'PlanningReview', 'planningReview', 'planning_review');
	const status = textValue(review, 'Status', 'status');
  const characterWorkflow = objectValue(snapshot, 'CharacterWorkflow', 'characterWorkflow', 'character_workflow');
  const characterCandidate = objectValue(characterWorkflow, 'Candidate', 'candidate');
  const characterConfirmationRequired =
    textValue(characterWorkflow, 'AnalysisStatus', 'analysisStatus', 'analysis_status') === 'candidate_ready' &&
    textValue(characterWorkflow, 'ReviewStatus', 'reviewStatus', 'review_status') === 'passed' &&
    textValue(characterWorkflow, 'ConfirmationStatus', 'confirmationStatus', 'confirmation_status') === 'unconfirmed';
  const chapters = getSnapshotOutlineRows(snapshot);
  const volumes = normalizeCoCreatePlanningVolumes(
    arrayValue(snapshot, 'LayeredOutline', 'layeredOutline', 'layered_outline'),
    chapters
	);
	const kind = adaptationActive ? 'foundation' : textValue(review, 'Kind', 'kind');
  const reviewPending = adaptationActive ? adaptationState === 'pending' : status === 'pending';
  const reviewCollecting = adaptationActive ? adaptationState === 'generating' : status === 'collecting';
	const pending = characterConfirmationRequired || reviewPending;
	const collecting = characterConfirmationRequired ? false : reviewCollecting;
	const targetFoundation = objectValue(snapshot, 'TargetFoundation', 'targetFoundation', 'target_foundation');
	const sourceFoundation = objectValue(snapshot, 'AdaptationSourceFoundation', 'adaptationSourceFoundation', 'adaptation_source_foundation', 'SourceFoundation', 'sourceFoundation', 'source_foundation');
	const adaptationCoreCast = objectValue(snapshot, 'AdaptationCoreCast', 'adaptationCoreCast', 'adaptation_core_cast');
	const adaptationBinding = objectValue(adaptationReview, 'Binding', 'binding');
	const coreCharacterIDs = arrayValue(snapshot, 'CoreCharacterIDs', 'coreCharacterIds', 'core_character_ids').map(String);
  const publishedCharacters = adaptationActive
    ? arrayValue(targetFoundation, 'Characters', 'characters')
    : arrayValue(snapshot, 'CharacterDetails', 'characterDetails', 'character_details');
  const candidateCharacters = arrayValue(characterCandidate, 'Characters', 'characters');
	const characters = characterConfirmationRequired && candidateCharacters.length
    ? candidateCharacters
    : publishedCharacters;
  const coreCharacterSet = new Set([
    ...coreCharacterIDs,
    ...characters
      .filter((character) => textValue(character, 'Tier', 'tier') === 'core')
      .map((character) => textValue(character, 'ID', 'id'))
  ]);
  const publishedRelationships = adaptationActive
    ? arrayValue(targetFoundation, 'Relationships', 'relationships')
    : arrayValue(snapshot, 'PlannedRelationships', 'plannedRelationships', 'planned_relationships');
  const candidateRelationships = arrayValue(characterCandidate, 'Relationships', 'relationships');
  const relationships = characterConfirmationRequired && candidateRelationships.length
    ? candidateRelationships
    : publishedRelationships;
  const publishedWorldRules = adaptationActive
    ? arrayValue(targetFoundation, 'WorldRules', 'worldRules', 'world_rules')
    : arrayValue(snapshot, 'WorldRules', 'worldRules', 'world_rules');
  const candidateWorldRules = arrayValue(characterCandidate, 'WorldRules', 'worldRules', 'world_rules');
  const worldRules = characterConfirmationRequired && candidateWorldRules.length
    ? candidateWorldRules
    : publishedWorldRules;
	return {
		loaded: Boolean(review),
		active: adaptationActive || pending || collecting,
		adaptation: adaptationActive,
		readonly: adaptationState === 'readonly',
		readonlyReason: textValue(adaptationReview, 'ReadonlyReason', 'readonlyReason', 'readonly_reason'),
    pending,
    collecting,
    characterConfirmationRequired,
    characterCandidateRevision: numberValue(
      characterWorkflow,
      'CandidateRevision',
      'candidateRevision',
      'candidate_revision'
    ),
    characterCandidateDigest: textValue(
      characterCandidate,
      'Digest',
      'digest'
    ) || textValue(
      characterWorkflow,
      'CandidateDigest',
      'candidateDigest',
      'candidate_digest'
    ),
    revising: collecting,
    status,
    kind,
    kindLabel: coCreatePlanningKindLabel(kind),
	foundationStatus: textValue(review, 'FoundationStatus', 'foundationStatus', 'foundation_status'),
		foundationRevision: numberValue(review, 'FoundationRevision', 'foundationRevision', 'foundation_revision'),
		foundationAuditSignature: textValue(review, 'FoundationAuditSignature', 'foundationAuditSignature', 'foundation_audit_signature') || textValue(adaptationBinding, 'TargetFoundationAuditSignature', 'targetFoundationAuditSignature', 'target_foundation_audit_signature'),
		coreCastSignature: textValue(review, 'CoreCastSignature', 'coreCastSignature', 'core_cast_signature') || textValue(adaptationBinding, 'CoreCastSignature', 'coreCastSignature', 'core_cast_signature'),
		sourceSignature: textValue(adaptationBinding, 'SourceSignature', 'sourceSignature', 'source_signature'),
	foundationGeneration: numberValue(review, 'FoundationGeneration', 'foundationGeneration', 'foundation_generation'),
	foundationFeedback: textValue(review, 'FoundationFeedback', 'foundationFeedback', 'foundation_feedback'),
	foundationConfirmedAt: textValue(review, 'FoundationConfirmedAt', 'foundationConfirmedAt', 'foundation_confirmed_at'),
		premise: characterConfirmationRequired
      ? textValue(characterCandidate, 'Premise', 'premise') || textValue(review, 'Brief', 'brief')
      : adaptationActive
        ? textValue(targetFoundation, 'Premise', 'premise')
        : textValue(snapshot, 'PremiseFull', 'premiseFull', 'premise_full', 'Premise', 'premise'),
		sourcePremise: textValue(sourceFoundation, 'Premise', 'premise'),
		sourceWorldRules: arrayValue(sourceFoundation, 'WorldRules', 'worldRules', 'world_rules'),
		sourceDispositions: arrayValue(adaptationCoreCast, 'SourceDispositions', 'sourceDispositions', 'source_dispositions'),
	coreCharacters: characters.filter((character) => coreCharacterSet.has(textValue(character, 'ID', 'id'))),
	supportingCharacters: characters.filter((character) => !coreCharacterSet.has(textValue(character, 'ID', 'id'))),
		plannedRelationships: relationships,
		hardWorldRules: worldRules.filter((rule) => coCreateWorldRuleStrength(rule) === 'hard'),
		softWorldRules: worldRules.filter((rule) => coCreateWorldRuleStrength(rule) === 'soft'),
		coreCastPreserved: adaptationActive || Boolean(valueByKey(snapshot, 'CoreCastPreserved', 'coreCastPreserved', 'core_cast_preserved')),
    brief: textValue(review, 'Brief', 'brief'),
    targetTotalWords: numberValue(review, 'TargetTotalWords', 'targetTotalWords', 'target_total_words'),
    createdAt: textValue(review, 'CreatedAt', 'createdAt', 'created_at'),
    updatedAt: textValue(review, 'UpdatedAt', 'updatedAt', 'updated_at'),
    chapterCount: chapters.length,
    chapters,
    volumes
  };
}

export function coCreateWorldRuleStrength(rule) {
  const strength = textValue(rule, 'Strength', 'strength').toLowerCase();
  const id = textValue(rule, 'ID', 'id').toLowerCase();
  // One historical retry path serialized grouped hr_/sr_ rules as a bare
  // array and defaulted every item to hard. Preserve explicit non-legacy
  // values, but recover the stable semantic group for those known IDs.
  if (id.startsWith('sr_')) return 'soft';
  if (id.startsWith('hr_')) return 'hard';
  return strength;
}

function normalizeCoCreatePlanningVolumes(volumes, chapters = []) {
  const firstChapter = chapters[0]?.chapter || 1;
  let nextChapter = firstChapter;
  return (volumes || [])
    .map((volume, index) => {
      const count = numberValue(volume, 'ChapterCount', 'chapterCount', 'chapter_count');
      let targetFrom = numberValue(volume, 'TargetFrom', 'targetFrom', 'target_from', 'From', 'from');
      let targetTo = numberValue(volume, 'TargetTo', 'targetTo', 'target_to', 'To', 'to');
      if (!targetFrom && count > 0) {
        targetFrom = nextChapter;
      }
      if (!targetTo && targetFrom && count > 0) {
        targetTo = targetFrom + count - 1;
      }
      if (targetTo >= targetFrom && targetFrom > 0) {
        nextChapter = targetTo + 1;
      }
      return {
        index: numberValue(volume, 'Index', 'index') || index + 1,
        title: textValue(volume, 'Title', 'title') || `第 ${index + 1} 卷`,
        theme: textValue(volume, 'Theme', 'theme'),
        targetFrom,
        targetTo,
        summary: textValue(volume, 'Summary', 'summary')
      };
    })
    .filter((volume) => volume.index > 0 && volume.targetFrom > 0 && volume.targetTo >= volume.targetFrom)
    .sort((a, b) => (a.targetFrom || a.index) - (b.targetFrom || b.index) || a.index - b.index);
}

function coCreatePlanningKindLabel(kind = '') {
	switch (String(kind || '').trim()) {
	case 'foundation':
		return 'StoryFoundation 待审核';
    case 'volume_split':
      return '分卷规划待审核';
    case 'chapter_outline':
      return '章节细纲待审核';
    default:
      return '创作规划待审核';
  }
}

export function getAdaptationProposalReview(snapshot) {
  const proposalSummary = objectValue(snapshot, 'ProposalSummary', 'proposalSummary', 'proposal_summary');
  const adaptationSummary = objectValue(snapshot, 'AdaptationSummary', 'adaptationSummary', 'adaptation_summary');
  const volumeReviewSummary = objectValue(snapshot, 'VolumeReviewSummary', 'volumeReviewSummary', 'volume_review_summary');
  const rawProposal = objectValue(snapshot, 'AdaptationProposal', 'adaptationProposal', 'adaptation_proposal');
  const rawPlan = objectValue(snapshot, 'AdaptationPlan', 'adaptationPlan', 'adaptation_plan');
  const rawVolumeReview = objectValue(snapshot, 'AdaptationVolumeReview', 'adaptationVolumeReview', 'adaptation_volume_review', 'VolumeReview', 'volumeReview', 'volume_review');
  const hasVolumeReview = Boolean(rawVolumeReview || volumeReviewSummary);
  const summary = proposalSummary || adaptationSummary;
  const reviewSummary = volumeReviewSummary || summary;
  const plan = rawProposal || rawPlan;
  const status = textValue(summary, 'Status', 'status') || textValue(volumeReviewSummary, 'Status', 'status') || textValue(plan, 'Status', 'status') || textValue(rawVolumeReview, 'Status', 'status');
  const chapters = getSnapshotOutlineRows(snapshot);
  const fallbackChapters = chapters.length ? chapters : rowsFromAdaptationPlan(plan);
  const volumeReview = normalizeVolumeReview(rawVolumeReview, reviewSummary || plan);
  const rules = [
    ...arrayValue(summary, 'MainlineRules', 'mainlineRules', 'mainline_rules'),
    ...arrayValue(summary, 'RelationshipGoals', 'relationshipGoals', 'relationship_goals'),
    ...arrayValue(volumeReviewSummary, 'MainlineRules', 'mainlineRules', 'mainline_rules'),
    ...arrayValue(volumeReviewSummary, 'RelationshipGoals', 'relationshipGoals', 'relationship_goals'),
    ...arrayValue(plan, 'MainlineRules', 'mainlineRules', 'mainline_rules'),
    ...arrayValue(plan, 'RelationshipGoals', 'relationshipGoals', 'relationship_goals')
  ].filter(Boolean);
  const volumes = normalizeProposalVolumes([
    ...arrayValue(summary, 'Volumes', 'volumes'),
    ...arrayValue(plan, 'Volumes', 'volumes')
  ], fallbackChapters.length);
  const loaded = Boolean(summary || plan || hasVolumeReview);
  const confirmed = status === 'confirmed' || Boolean(rawPlan && !rawProposal);
  const volumeReviewReady = hasVolumeReview && loaded && !confirmed && volumeReview.volumes.length > 0 && fallbackChapters.length === 0;
  return {
    loaded,
    confirmed,
    proposalReady: volumeReviewReady || (loaded && !confirmed && (status === 'proposal' || Boolean(rawProposal))),
    volumeReviewReady,
    status,
    granularity: textValue(summary, 'Granularity', 'granularity') || textValue(volumeReviewSummary, 'Granularity', 'granularity') || textValue(plan, 'Granularity', 'granularity') || volumeReview.granularity,
    rewritePolicy: textValue(summary, 'RewritePolicy', 'rewritePolicy', 'rewrite_policy') || textValue(volumeReviewSummary, 'RewritePolicy', 'rewritePolicy', 'rewrite_policy') || textValue(plan, 'RewritePolicy', 'rewritePolicy', 'rewrite_policy') || volumeReview.rewritePolicy,
    brief: textValue(summary, 'Brief', 'brief') || textValue(volumeReviewSummary, 'Brief', 'brief') || textValue(plan, 'Brief', 'brief') || volumeReview.brief,
    chapterCount: numberValue(summary, 'ChapterCount', 'chapterCount', 'chapter_count') || numberValue(volumeReviewSummary, 'TargetChapterCount', 'targetChapterCount', 'target_chapter_count') || fallbackChapters.length || volumeReview.chapterCount,
    sourceTotalRunes: numberValue(summary, 'SourceTotalRunes', 'sourceTotalRunes', 'source_total_runes') ||
      numberValue(plan, 'SourceTotalRunes', 'sourceTotalRunes', 'source_total_runes'),
    targetTotalRunes: numberValue(summary, 'TargetTotalRunes', 'targetTotalRunes', 'target_total_runes') ||
      numberValue(plan, 'TargetTotalRunes', 'targetTotalRunes', 'target_total_runes'),
    rules,
    volumes,
    volumeReview,
    chapters: fallbackChapters
  };
}

export function getVisibleAdaptationProposalReview(snapshot, adaptation = {}) {
  const review = getAdaptationProposalReview(snapshot);
  if (!review.loaded || isAdaptationProposalCurrent(adaptation)) {
    return { ...review, stale: false };
  }
  return {
    ...review,
    loaded: false,
    proposalReady: false,
    stale: true,
    rules: [],
    volumes: [],
    volumeReview: normalizeVolumeReview(null),
    chapters: []
  };
}

export function isAdaptationProposalCurrent(adaptation = {}) {
  const proposalKey = String(adaptation.proposalKey || '');
  return Boolean(proposalKey && proposalKey === buildAdaptationProposalKey(adaptation));
}

export function buildAdaptationProposalKey({ sourceFile = '', mode = '', brief = '' } = {}) {
  const sourcePath = typeof sourceFile === 'string' ? sourceFile : sourceFile?.relative_path || '';
  return JSON.stringify([
    String(sourcePath || '').trim(),
    String(mode || '').trim(),
    String(brief || '').trim()
  ]);
}

export function shouldShowAdaptationSourceMapping(granularity = '') {
  return String(granularity || '').trim().toLowerCase() !== 'free';
}

export function formatAdaptationSourceCoverageLabel(coverage, granularity, { addedLabel = '\u65b0\u589e' } = {}) {
  if (!coverage) {
    return '';
  }
  if (coverage.isAdded) {
    return addedLabel;
  }
  if (!shouldShowAdaptationSourceMapping(granularity)) {
    return '';
  }
  if (coverage.from && coverage.to) {
    return `\u539f ${coverage.from}-${coverage.to}`;
  }
  if (coverage.chapters?.length) {
    return `\u539f ${coverage.chapters.join(',')}`;
  }
  return '';
}

export function formatAdaptationVolumeSourceLabel(volume, granularity) {
  if (!volume || !shouldShowAdaptationSourceMapping(granularity)) {
    return '';
  }
  const sourceLabel = textValue(volume, 'SourceLabel', 'sourceLabel', 'source_label');
  if (sourceLabel) {
    return sourceLabel;
  }
  const sourceFrom = numberValue(volume, 'SourceFrom', 'sourceFrom', 'source_from');
  const sourceTo = numberValue(volume, 'SourceTo', 'sourceTo', 'source_to');
  return sourceFrom || sourceTo
    ? `\u539f ${sourceFrom || '?'}-${sourceTo || sourceFrom || '?'}`
    : '';
}

export function clearAdaptationProposalSnapshot(snapshot) {
  if (!snapshot || typeof snapshot !== 'object') {
    return snapshot;
  }
  const proposalKeys = [
    'AdaptationProposal',
    'adaptationProposal',
    'adaptation_proposal',
    'ProposalSummary',
    'proposalSummary',
    'proposal_summary',
    'AdaptationVolumeReview',
    'adaptationVolumeReview',
    'adaptation_volume_review',
    'VolumeReviewSummary',
    'volumeReviewSummary',
    'volume_review_summary',
    'VolumeReview',
    'volumeReview',
    'volume_review'
  ];
  if (!proposalKeys.some((key) => Object.prototype.hasOwnProperty.call(snapshot, key))) {
    return snapshot;
  }
  const next = { ...snapshot };
  for (const key of proposalKeys) {
    delete next[key];
  }
  return next;
}

function snapshotFromAdaptationProposalResponse(data, previousSnapshot) {
  const snapshot = data?.snapshot || previousSnapshot;
  if (!snapshot || typeof snapshot !== 'object') {
    return snapshot;
  }
  const next = { ...snapshot };
  const proposal = objectValue(data, 'proposal', 'Proposal', 'adaptation_proposal', 'AdaptationProposal');
  const volumeReview = objectValue(data, 'volume_review', 'volumeReview', 'VolumeReview', 'adaptation_volume_review', 'adaptationVolumeReview', 'AdaptationVolumeReview');
  if (proposal && !objectValue(next, 'AdaptationProposal', 'adaptationProposal', 'adaptation_proposal')) {
    next.adaptation_proposal = proposal;
  }
  if (volumeReview && !objectValue(next, 'AdaptationVolumeReview', 'adaptationVolumeReview', 'adaptation_volume_review', 'VolumeReview', 'volumeReview', 'volume_review')) {
    next.volume_review = volumeReview;
  }
  return next;
}

export function getSnapshotOutlineRows(snapshot) {
  const rows = arrayValue(snapshot, 'Outline', 'outline');
  if (rows.length) {
    return rows.map(normalizeOutlineRow);
  }
  return rowsFromAdaptationPlan(
    objectValue(snapshot, 'AdaptationProposal', 'adaptationProposal', 'adaptation_proposal') ||
    objectValue(snapshot, 'AdaptationPlan', 'adaptationPlan', 'adaptation_plan')
  );
}

export function getSnapshotOutlineStructure(snapshot) {
  const chapters = getSnapshotOutlineRows(snapshot);
  const volumes = getSnapshotOutlineVolumes(snapshot, chapters.length);
  return outlineStructureFromParts(chapters, volumes);
}

function getSnapshotOutlineVolumes(snapshot, chapterCount = 0) {
  const layeredVolumes = normalizeLayeredOutlineVolumes(
    arrayValue(snapshot, 'LayeredOutline', 'layeredOutline', 'layered_outline'),
    chapterCount
  );
  if (layeredVolumes.length) {
    return layeredVolumes;
  }
  const proposalSummary = objectValue(snapshot, 'ProposalSummary', 'proposalSummary', 'proposal_summary');
  const adaptationSummary = objectValue(snapshot, 'AdaptationSummary', 'adaptationSummary', 'adaptation_summary');
  const plan = objectValue(snapshot, 'AdaptationPlan', 'adaptationPlan', 'adaptation_plan') ||
    objectValue(snapshot, 'AdaptationProposal', 'adaptationProposal', 'adaptation_proposal');
  return normalizeProposalVolumes([
    ...arrayValue(proposalSummary, 'Volumes', 'volumes'),
    ...arrayValue(adaptationSummary, 'Volumes', 'volumes'),
    ...arrayValue(plan, 'Volumes', 'volumes')
  ], chapterCount);
}

function getSnapshotOutlineGranularity(snapshot) {
  const proposalSummary = objectValue(snapshot, 'ProposalSummary', 'proposalSummary', 'proposal_summary');
  const adaptationSummary = objectValue(snapshot, 'AdaptationSummary', 'adaptationSummary', 'adaptation_summary');
  const plan = objectValue(snapshot, 'AdaptationPlan', 'adaptationPlan', 'adaptation_plan') ||
    objectValue(snapshot, 'AdaptationProposal', 'adaptationProposal', 'adaptation_proposal');
  return textValue(proposalSummary, 'Granularity', 'granularity') ||
    textValue(adaptationSummary, 'Granularity', 'granularity') ||
    textValue(plan, 'Granularity', 'granularity');
}

function normalizeLayeredOutlineVolumes(volumes, chapterCount = 0) {
  const normalized = (volumes || [])
    .map((volume, index) => {
      const targetFrom = numberValue(volume, 'TargetFrom', 'targetFrom', 'target_from', 'From', 'from');
      let targetTo = numberValue(volume, 'TargetTo', 'targetTo', 'target_to', 'To', 'to');
      const plannedCount = numberValue(volume, 'ChapterCount', 'chapterCount', 'chapter_count');
      if (!targetTo && targetFrom > 0 && plannedCount > 0) {
        targetTo = targetFrom + plannedCount - 1;
      }
      return {
        index: numberValue(volume, 'Index', 'index') || index + 1,
        title: textValue(volume, 'Title', 'title') || `第 ${index + 1} 卷`,
        theme: textValue(volume, 'Theme', 'theme'),
        goal: textValue(volume, 'Goal', 'goal'),
        summary: textValue(volume, 'Summary', 'summary'),
        targetFrom,
        targetTo,
        sourceFrom: numberValue(volume, 'SourceFrom', 'sourceFrom', 'source_from'),
        sourceTo: numberValue(volume, 'SourceTo', 'sourceTo', 'source_to')
      };
    })
    .filter((volume) => volume.targetFrom > 0 && volume.targetTo >= volume.targetFrom && (!chapterCount || volume.targetFrom <= chapterCount))
    .sort((a, b) => a.targetFrom - b.targetFrom || a.index - b.index);
  return dedupeProposalVolumes(normalized);
}

function outlineStructureFromParts(chapters = [], volumes = []) {
  const normalizedChapters = Array.isArray(chapters) ? chapters : [];
  const normalizedVolumes = Array.isArray(volumes) ? volumes : [];
  const groups = normalizedChapters.length || normalizedVolumes.length
    ? proposalVolumeGroups({ chapters: normalizedChapters, volumes: normalizedVolumes })
    : [];
  return {
    chapters: normalizedChapters,
    volumes: normalizedVolumes,
    hasVolumes: normalizedVolumes.length > 0,
    groups
  };
}

function outlineGroupRangeLabel(group) {
  if (!group?.from || !group?.to) {
    return '';
  }
  return group.from === group.to ? `第 ${group.from} 章` : `第 ${group.from}-${group.to} 章`;
}

function outlineGroupChapterCount(group) {
  if (!group) {
    return 0;
  }
  if (group.chapters?.length) {
    return group.chapters.length;
  }
  if (group.from > 0 && group.to >= group.from) {
    return group.to - group.from + 1;
  }
  return 0;
}

export function isProjectRunning(snapshot) {
  if (!snapshot) {
    return false;
  }
  if (snapshot.IsRunning === true || snapshot.is_running === true) {
    return true;
  }
  const status = textValue(snapshot, 'StatusLabel', 'status_label', 'RuntimeState', 'runtime_state').toLowerCase();
  if (['running', 'working', 'busy'].includes(status)) {
    return true;
  }
  if (['paused', 'ready', 'idle', 'done', 'complete', 'completed', 'error'].includes(status)) {
    return false;
  }
  return arrayValue(snapshot, 'Agents', 'agents').some(isRunningAgent);
}

function isRunningAgent(agent) {
  const state = textValue(agent, 'State', 'state').toLowerCase();
  const tool = textValue(agent, 'Tool', 'tool');
  if (tool) {
    return true;
  }
  return Boolean(state && !['idle', 'done', 'complete', 'completed', 'paused'].includes(state));
}

function rowsFromAdaptationPlan(plan) {
  return arrayValue(plan, 'Chapters', 'chapters').map(normalizeOutlineRow);
}

function proposalVolumeGroups(proposal) {
  const chapters = proposal?.chapters || [];
  const volumes = proposal?.volumes || [];
  if (!volumes.length) {
    return [{
      key: 'all',
      title: '未分卷提案',
      from: chapters[0]?.chapter || 1,
      to: chapters[chapters.length - 1]?.chapter || chapters.length,
      theme: '',
      summary: '',
      chapters
    }];
  }
  return volumes.map((volume) => ({
    key: volume.index || `${volume.targetFrom}-${volume.targetTo}`,
    title: `第 ${volume.index} 卷：${volume.title}`,
    from: volume.targetFrom,
    to: volume.targetTo,
    theme: volume.theme || volume.goal || '',
    summary: volume.summary || '',
    chapters: chapters.filter((chapter) => chapter.chapter >= volume.targetFrom && chapter.chapter <= volume.targetTo)
  }));
}

function normalizeVolumeReview(rawReview, fallback = {}) {
  const volumes = normalizeVolumeReviewVolumes([
    ...arrayValue(rawReview, 'Volumes', 'volumes'),
    ...arrayValue(fallback, 'Volumes', 'volumes')
  ]);
  return {
    loaded: Boolean(rawReview || volumes.length),
    status: textValue(rawReview, 'Status', 'status') || textValue(fallback, 'Status', 'status'),
    granularity: textValue(rawReview, 'Granularity', 'granularity') || textValue(fallback, 'Granularity', 'granularity'),
    rewritePolicy: textValue(rawReview, 'RewritePolicy', 'rewritePolicy', 'rewrite_policy') ||
      textValue(fallback, 'RewritePolicy', 'rewritePolicy', 'rewrite_policy'),
    brief: textValue(rawReview, 'Brief', 'brief') || textValue(fallback, 'Brief', 'brief'),
    chapterCount: numberValue(rawReview, 'ChapterCount', 'chapterCount', 'chapter_count') ||
      numberValue(fallback, 'ChapterCount', 'chapterCount', 'chapter_count') ||
      volumes.reduce((max, volume) => Math.max(max, volume.targetTo || 0), 0),
    volumes
  };
}

function normalizeVolumeReviewVolumes(volumes) {
  const normalized = (volumes || [])
    .map((volume, index) => {
      const targetFrom = numberValue(volume, 'TargetFrom', 'targetFrom', 'target_from');
      const targetTo = numberValue(volume, 'TargetTo', 'targetTo', 'target_to');
      const sourceFrom = numberValue(volume, 'SourceFrom', 'sourceFrom', 'source_from');
      const sourceTo = numberValue(volume, 'SourceTo', 'sourceTo', 'source_to');
      const sourceLabel = sourceFrom || sourceTo
        ? `原 ${sourceFrom || '?'}-${sourceTo || sourceFrom || '?'}`
        : textValue(volume, 'SourceLabel', 'sourceLabel', 'source_label');
      return {
        index: numberValue(volume, 'Index', 'index') || index + 1,
        title: textValue(volume, 'Title', 'title') || `第 ${index + 1} 卷`,
        theme: textValue(volume, 'Theme', 'theme'),
        goal: textValue(volume, 'Goal', 'goal', 'AdaptationGoal', 'adaptationGoal', 'adaptation_goal'),
        summary: textValue(volume, 'Summary', 'summary'),
        plot: textValue(volume, 'Plot', 'plot', 'TargetPlot', 'targetPlot', 'target_plot', 'Synopsis', 'synopsis'),
        targetFrom,
        targetTo,
        sourceFrom,
        sourceTo,
        sourceLabel,
        beats: [
          ...arrayValue(volume, 'Beats', 'beats'),
          ...arrayValue(volume, 'KeyBeats', 'keyBeats', 'key_beats')
        ].filter(Boolean)
      };
    })
    .filter((volume) => volume.index > 0)
    .sort((a, b) => (a.targetFrom || a.index) - (b.targetFrom || b.index) || a.index - b.index);
  return dedupeVolumeReviewVolumes(normalized);
}

function dedupeVolumeReviewVolumes(volumes) {
  const out = [];
  const seen = new Map();
  for (const volume of volumes) {
    const key = volumeReviewVolumeKey(volume);
    if (!key) {
      out.push(volume);
      continue;
    }
    const existingIndex = seen.get(key);
    if (existingIndex === undefined) {
      seen.set(key, out.length);
      out.push(volume);
      continue;
    }
    out[existingIndex] = mergeVolumeReviewVolume(out[existingIndex], volume);
  }
  return out;
}

function volumeReviewVolumeKey(volume) {
  if (volume.targetFrom > 0 && volume.targetTo >= volume.targetFrom) {
    return `range:${volume.targetFrom}:${volume.targetTo}`;
  }
  if (volume.index > 0) {
    return `index:${volume.index}`;
  }
  const title = String(volume.title || '').trim();
  return title ? `title:${title}` : '';
}

function mergeVolumeReviewVolume(existing, next) {
  return {
    ...next,
    ...existing,
    index: existing.index || next.index,
    title: existing.title || next.title,
    theme: existing.theme || next.theme,
    goal: existing.goal || next.goal,
    summary: existing.summary || next.summary,
    plot: existing.plot || next.plot,
    targetFrom: existing.targetFrom || next.targetFrom,
    targetTo: existing.targetTo || next.targetTo,
    sourceFrom: existing.sourceFrom || next.sourceFrom,
    sourceTo: existing.sourceTo || next.sourceTo,
    sourceLabel: existing.sourceLabel || next.sourceLabel,
    beats: existing.beats?.length ? existing.beats : (next.beats || [])
  };
}

export function buildVolumeReviewRevisionPayload(adaptation = {}, volumeReview = {}) {
  const instruction = String(adaptation.revisionInstruction || '').trim();
  if (!instruction) {
    return { ok: false, error: '请输入修改意见' };
  }
  const volumes = volumeReview.volumes || [];
  if (!volumes.length) {
    return { ok: false, error: '当前分卷审阅没有可修改的卷' };
  }
  const selected = Number.parseInt(String(adaptation.revisionVolume || ''), 10);
  const volume = volumes.find((item) => item.index === selected) || volumes[0];
  if (!volume?.index) {
    return { ok: false, error: '请选择要修改的卷' };
  }
  return {
    ok: true,
    body: {
      volume_index: volume.index,
      instruction
    }
  };
}

export function buildCoCreatePlanningRevisionPayload(revision = {}, review = {}) {
  const instruction = String(revision.instruction || revision.feedback || '').trim();
  if (!instruction) {
    return { ok: false, error: '请输入审核意见' };
  }
  if (!review?.kind) {
    return {
      ok: true,
      body: {
        feedback: instruction
      }
    };
  }
  const body = {
    feedback: instruction,
    instruction
  };
  const kind = String(review.kind || '').trim();
  if (kind === 'volume_split') {
    const volumes = Array.isArray(review.volumes) ? review.volumes : [];
    if (!volumes.length) {
      return { ok: false, error: '当前分卷规划没有可修改的卷' };
    }
    const selected = Number.parseInt(String(revision.volumeIndex || ''), 10);
    const volume = volumes.find((item) => item.index === selected) || volumes[0];
    if (!volume?.index) {
      return { ok: false, error: '请选择要修改的卷' };
    }
    return {
      ok: true,
      body: {
        ...body,
        scope: 'volume',
        target: coCreatePlanningRevisionVolumeLabel(volume),
        volume_index: volume.index
      }
    };
  }
  if (kind === 'chapter_outline') {
    const chapters = coCreatePlanningRevisionChapterOptions(review);
    if (!chapters.length) {
      return { ok: false, error: '当前章节细纲没有可修改的章节' };
    }
    const hasVolumes = Array.isArray(review.volumes) && review.volumes.length > 0;
    const scope = coCreatePlanningRevisionScope(revision.scope, kind, hasVolumes);
    if (scope === 'all') {
      return {
        ok: true,
        body: {
          ...body,
          scope: 'all',
          target: '全卷'
        }
      };
    }
    const selected = Number.parseInt(String(revision.chapter || revision.fromChapter || ''), 10);
    const chapter = chapters.find((item) => item.chapter === selected) || chapters[0];
    if (!chapter?.chapter) {
      return { ok: false, error: '请选择要修改的章节' };
    }
    return {
      ok: true,
      body: {
        ...body,
        scope: 'chapter',
        target: coCreatePlanningRevisionChapterLabel(chapter),
        chapter: chapter.chapter,
        from_chapter: chapter.chapter,
        to_chapter: chapter.chapter
      }
    };
  }
  return {
    ok: true,
    body: {
      ...body,
      scope: 'all',
      target: '全局 draft'
    }
  };
}

export function buildAdaptationRevisionPayload(adaptation = {}, proposal = {}) {
  const instruction = String(adaptation.revisionInstruction || '').trim();
  if (!instruction) {
    return { ok: false, error: '请输入修改意见' };
  }
  const mode = String(adaptation.revisionMode || 'chapter');
  const chapterCount = Number(proposal.chapterCount || proposal.chapters?.length || 0);
  if (chapterCount <= 0) {
    return { ok: false, error: '当前提案没有可修改的章节' };
  }
  if (mode === 'range') {
    let from = Number.parseInt(String(adaptation.revisionFromChapter || ''), 10);
    let to = Number.parseInt(String(adaptation.revisionToChapter || ''), 10);
    if (!Number.isInteger(from) || !Number.isInteger(to)) {
      return { ok: false, error: '请选择首章和尾章' };
    }
    if (from > to) {
      [from, to] = [to, from];
    }
    if (from < 1 || to > chapterCount) {
      return { ok: false, error: '章节范围超出当前提案' };
    }
    return {
      ok: true,
      body: {
        target: `第${from}-${to}章`,
        from_chapter: from,
        to_chapter: to,
        instruction
      }
    };
  }
  if (mode === 'volume') {
    const selected = String(adaptation.revisionVolume || 'all');
    if (selected === 'all') {
      return {
        ok: true,
        body: {
          target: '全卷',
          volume_index: -1,
          instruction
        }
      };
    }
    const volumeIndex = Number.parseInt(selected, 10);
    if (!Number.isInteger(volumeIndex) || volumeIndex <= 0) {
      return { ok: false, error: '请选择要修改的卷' };
    }
    return {
      ok: true,
      body: {
        target: `第${volumeIndex}卷`,
        volume_index: volumeIndex,
        instruction
      }
    };
  }
  const chapter = Number.parseInt(String(adaptation.revisionChapter || ''), 10);
  if (!Number.isInteger(chapter) || chapter < 1 || chapter > chapterCount) {
    return { ok: false, error: '请选择要修改的章节' };
  }
  return {
    ok: true,
    body: {
      target: `第${chapter}章`,
      from_chapter: chapter,
      to_chapter: chapter,
      instruction
    }
  };
}

function continuationStageTitle(workflow) {
  return {
    source_ready: '确定续写 Draft',
    draft_collecting: '确定续写 Draft',
    proposal_generating: '生成续写提案',
    proposal_review_pending: '审核续写提案',
    volume_review_pending: '审核分卷规划',
    outline_generating: '生成章节细纲',
    outline_review_pending: '审核章节细纲',
    ready_to_write: '最终开写确认',
    writing: '续写进行中',
    paused: '续写流程已暂停',
    failed: '续写流程需要恢复'
  }[workflow.stage] || '小说续写';
}

function continuationStageDescription(workflow) {
  return {
    source_ready: '先在共创中明确续写方向、目标冲突、人物弧和篇幅，再生成提案。',
    draft_collecting: workflow.draft ? 'Draft 已保存，可以据此生成续写提案。' : '继续通过共创收敛 Draft，确认后再进入提案。',
    proposal_generating: '系统将以已确认 Draft 和原作基线生成可审核的续写提案。',
    proposal_review_pending: workflow.shortStory ? '短篇无需分卷；提案通过后仍须生成并审核章节细纲。' : '先审核总体方向；长篇提案通过后进入分卷规划审核。',
    volume_review_pending: '可修改单卷或全部分卷；审核通过后才生成章节细纲。',
    outline_generating: '生成正式章节细纲，完成后仍需逐项审核。',
    outline_review_pending: '可按单章、章节范围、整卷或全部修改；通过后才允许开写。',
    ready_to_write: 'Draft、提案、分卷和章节细纲均已通过审核，等待最终显式确认。',
    writing: 'Writer 只能按审核通过的规划从原作下一章继续。',
    paused: '恢复只会继续当前规划任务，不会跳过审核。',
    failed: '修复错误后从上次阶段继续，不会自动跨过审核。'
  }[workflow.stage] || '';
}

export function getOutlineRevisionView(snapshot, revision = {}) {
  const outline = getSnapshotOutlineRows(snapshot).filter((item) => item.chapter > 0);
  const selectedChapter = clampOutlineChapterSelection(revision.chapter, outline);
  const chapter = Number.parseInt(selectedChapter, 10);
  const outlineRow = outline.find((item) => item.chapter === chapter) || outline[0] || null;
  return {
    active: Boolean(revision.active && outlineRow),
    selectedChapter,
    chapter: outlineRow?.chapter || chapter,
    outlineRow,
    outline,
    granularity: getSnapshotOutlineGranularity(snapshot)
  };
}

export function shouldHydrateOutlineRevisionDetails(snapshot, revision = {}) {
  if (!revision.active) {
    return false;
  }
  const selected = getOutlineRevisionView(snapshot, revision);
  const row = selected.outlineRow;
  return Boolean(row && !row.coreEvent && !row.hook && row.scenes.length === 0);
}

export function buildOutlineRevisionPayload(revision = {}, snapshot = {}) {
  const outline = getSnapshotOutlineRows(snapshot).filter((item) => item.chapter > 0);
  if (!outline.length) {
    return { ok: false, error: '当前项目没有可修改的章节细纲' };
  }
  const instruction = String(revision.instruction || '').trim();
  if (!instruction) {
    return { ok: false, error: '请输入修改要求' };
  }
  const chapter = Number.parseInt(String(revision.chapter || ''), 10);
  if (!Number.isInteger(chapter) || !outline.some((item) => item.chapter === chapter)) {
    return { ok: false, error: '请选择要修改的章节' };
  }
  return {
    ok: true,
    body: {
      chapter,
      instruction
    }
  };
}

export function outlineRevisionSuccessMessage(revision = {}, chapter = 0) {
  const prefix = `第 ${chapter} 章细纲已修改`;
  const rewriteQueued = Boolean(revision?.rewrite_queued || revision?.rewriteQueued || revision?.RewriteQueued);
  const draftReset = Boolean(revision?.draft_reset || revision?.draftReset || revision?.DraftReset);
  if (rewriteQueued && draftReset) {
    return `${prefix}，草稿已重置并加入重写队列`;
  }
  if (rewriteQueued) {
    return `${prefix}，已加入重写队列`;
  }
  if (draftReset) {
    return `${prefix}，草稿已重置`;
  }
  return prefix;
}

export function getCompletedBookChapterRevisionView(snapshot) {
  const outline = getSnapshotOutlineRows(snapshot).filter((item) => item.chapter > 0);
  const phase = textValue(snapshot, 'Phase', 'phase').toLowerCase();
  return {
    visible: phase === 'complete' && outline.length > 0,
    phase,
    outline
  };
}

export function getCompletedBookSelectedChapterView(snapshot, revision = {}) {
  const view = getCompletedBookChapterRevisionView(snapshot);
  if (!view.visible) {
    return { ...view, chapter: 0, title: '', outlineRow: null, content: '' };
  }
  const selectedChapter = clampOutlineChapterSelection(revision.chapter, view.outline);
  const chapter = Number.parseInt(selectedChapter, 10);
  const outlineRow = view.outline.find((item) => item.chapter === chapter) || view.outline[0] || null;
  return {
    ...view,
    selectedChapter,
    chapter: outlineRow?.chapter || chapter,
    title: outlineRow?.title || '',
    outlineRow,
    content: outlineRow?.content || ''
  };
}

export function buildChapterRevisionPayload(revision = {}, snapshot = {}) {
  const view = getCompletedBookChapterRevisionView(snapshot);
  if (!view.visible) {
    return { ok: false, error: '全书完成且有章节大纲后才能返工单章' };
  }
  const instruction = String(revision.instruction || '').trim();
  if (!instruction) {
    return { ok: false, error: '请输入修改意见' };
  }
  const chapter = Number.parseInt(String(revision.chapter || ''), 10);
  const chapters = view.outline.map((item) => item.chapter);
  if (!Number.isInteger(chapter) || !chapters.includes(chapter)) {
    return { ok: false, error: '请选择要返工的章节' };
  }
  return {
    ok: true,
    body: {
      chapter,
      mode: normalizeChapterRevisionMode(revision.mode),
      instruction
    }
  };
}

function normalizeProposalVolumes(volumes, chapterCount = 0) {
  const normalized = (volumes || [])
    .map((volume, index) => {
      const targetFrom = numberValue(volume, 'TargetFrom', 'targetFrom', 'target_from');
      const targetTo = numberValue(volume, 'TargetTo', 'targetTo', 'target_to');
      return {
        index: numberValue(volume, 'Index', 'index') || index + 1,
        title: textValue(volume, 'Title', 'title') || `第 ${targetFrom || '?'}-${targetTo || '?'} 章`,
        theme: textValue(volume, 'Theme', 'theme'),
        goal: textValue(volume, 'Goal', 'goal'),
        summary: textValue(volume, 'Summary', 'summary'),
        targetFrom,
        targetTo,
        sourceFrom: numberValue(volume, 'SourceFrom', 'sourceFrom', 'source_from'),
        sourceTo: numberValue(volume, 'SourceTo', 'sourceTo', 'source_to')
      };
    })
    .filter((volume) => volume.targetFrom > 0 && volume.targetTo >= volume.targetFrom && (!chapterCount || volume.targetTo <= chapterCount))
    .sort((a, b) => a.targetFrom - b.targetFrom || a.index - b.index);
  return dedupeProposalVolumes(normalized);
}

function dedupeProposalVolumes(volumes) {
  const out = [];
  const seen = new Map();
  for (const volume of volumes) {
    const key = `${volume.targetFrom}:${volume.targetTo}`;
    const existingIndex = seen.get(key);
    if (existingIndex === undefined) {
      seen.set(key, out.length);
      out.push(volume);
      continue;
    }
    out[existingIndex] = mergeProposalVolume(out[existingIndex], volume);
  }
  return out;
}

function mergeProposalVolume(existing, next) {
  return {
    ...existing,
    ...next,
    index: existing.index,
    targetFrom: existing.targetFrom,
    targetTo: existing.targetTo,
    title: next.title || existing.title,
    theme: next.theme || existing.theme,
    goal: next.goal || existing.goal,
    summary: next.summary || existing.summary,
    sourceFrom: next.sourceFrom || existing.sourceFrom,
    sourceTo: next.sourceTo || existing.sourceTo
  };
}

function clampChapterSelection(value, chapterCount = 0) {
  const parsed = Number.parseInt(String(value || ''), 10);
  if (!chapterCount) {
    return parsed > 0 ? String(parsed) : '1';
  }
  if (!Number.isInteger(parsed) || parsed < 1) {
    return '1';
  }
  return String(Math.min(parsed, chapterCount));
}

const CO_CREATE_DECISION_SKIP_OPTION_ID = '__skip__';
export const CO_CREATE_DECISION_SKIP_ANSWER = '跳过：保留原作处理，不针对该问题做修改。';

function coCreateDecisionKey(decision = {}, index = 0) {
  const id = String(decision.id || '').trim();
  return id || `decision-${index}`;
}

function normalizeCoCreateDecisionAnswer(answer = {}) {
  return {
    optionId: String(answer.optionId || '').trim(),
    customAnswer: String(answer.customAnswer || '')
  };
}

export function normalizeCoCreateDecisionAnswers(decisions = [], previous = {}) {
  const normalizedDecisions = Array.isArray(decisions) ? decisions : [];
  return normalizedDecisions.reduce((next, decision, index) => {
    const key = coCreateDecisionKey(decision, index);
    next[key] = normalizeCoCreateDecisionAnswer(previous[key]);
    return next;
  }, {});
}

export function coCreateDecisionOptionLetter(index = 0) {
  let value = Number.parseInt(String(index || 0), 10);
  if (!Number.isInteger(value) || value < 0) {
    value = 0;
  }
  let label = '';
  do {
    label = String.fromCharCode(65 + (value % 26)) + label;
    value = Math.floor(value / 26) - 1;
  } while (value >= 0);
  return label;
}

export function coCreateDecisionRecommendedLetter(decision = {}) {
  const recommendedOptionId = String(decision.recommended_option_id || '').trim();
  if (!recommendedOptionId || !Array.isArray(decision.options)) {
    return '';
  }
  const index = decision.options.findIndex((option) => String(option.id || '').trim() === recommendedOptionId);
  return index >= 0 ? coCreateDecisionOptionLetter(index) : '';
}

export function isCoCreateDecisionAnswerComplete(answer = {}) {
  const normalized = normalizeCoCreateDecisionAnswer(answer);
  return Boolean(normalized.optionId || normalized.customAnswer.trim());
}

export function buildCoCreateDecisionPayload(decisions = [], answers = {}) {
  const normalizedDecisions = Array.isArray(decisions) ? decisions : [];
  return normalizedDecisions.map((decision, index) => {
    const key = coCreateDecisionKey(decision, index);
    const answer = normalizeCoCreateDecisionAnswer(answers[key]);
    const skipped = answer.optionId === CO_CREATE_DECISION_SKIP_OPTION_ID;
    const customAnswer = skipped ? CO_CREATE_DECISION_SKIP_ANSWER : answer.customAnswer.trim();
    return {
      decision_id: String(decision.id || '').trim(),
      option_id: customAnswer ? '' : answer.optionId,
      custom_answer: customAnswer
    };
  });
}

export function isCoCreateDecisionPayloadComplete(item = {}) {
  return Boolean(
    String(item.decision_id || '').trim()
      && (String(item.option_id || '').trim() || String(item.custom_answer || '').trim())
  );
}

export function clampCoCreateDecisionPageIndex(value, decisionCount = 0) {
  const count = Number.parseInt(String(decisionCount || ''), 10);
  if (!Number.isInteger(count) || count < 1) {
    return 0;
  }
  const parsed = Number.parseInt(String(value || ''), 10);
  if (!Number.isInteger(parsed) || parsed < 0) {
    return 0;
  }
  return Math.min(parsed, count - 1);
}

function clampOutlineChapterSelection(value, outline = []) {
  const chapters = outline.map((item) => item.chapter).filter((chapter) => chapter > 0);
  const parsed = Number.parseInt(String(value || ''), 10);
  if (!chapters.length) {
    return parsed > 0 ? String(parsed) : '1';
  }
  return String(chapters.includes(parsed) ? parsed : chapters[0]);
}

function normalizeChapterRevisionMode(mode) {
  const value = String(mode || '').trim();
  return chapterRevisionModes.some((item) => item.value === value) ? value : 'rewrite';
}

function chapterRevisionModeLabel(mode) {
  return normalizeChapterRevisionMode(mode) === 'polish' ? '打磨' : '重写';
}

function clampVolumeSelection(value, volumeCount = 0) {
  if (String(value || '') === 'all') {
    return 'all';
  }
  const parsed = Number.parseInt(String(value || ''), 10);
  if (!volumeCount) {
    return 'all';
  }
  if (!Number.isInteger(parsed) || parsed < 1) {
    return 'all';
  }
  return String(Math.min(parsed, volumeCount));
}

function normalizeOutlineRow(row) {
  const chapter = numberValue(row, 'Chapter', 'chapter');
  const wordBudget = normalizeChapterBudget(row);
  const sourceCoverage = normalizeSourceCoverage(row);
  return {
    chapter,
    title: textValue(row, 'Title', 'title') || `第 ${chapter || '?'} 章`,
    coreEvent: textValue(row, 'CoreEvent', 'coreEvent', 'core_event'),
    hook: textValue(row, 'Hook', 'hook'),
    scenes: arrayValue(row, 'Scenes', 'scenes'),
    content: textValue(row, 'Content', 'content', 'Text', 'text', 'Body', 'body'),
    writtenWordCount: numberValue(row, 'WrittenWordCount', 'writtenWordCount', 'written_word_count'),
    wordBudget,
    sourceCoverage,
    preserveEvents: arrayValue(row, 'PreserveEvents', 'preserveEvents', 'preserve_events'),
    requiredChanges: arrayValue(row, 'RequiredChanges', 'requiredChanges', 'required_changes'),
    forbiddenMoves: arrayValue(row, 'ForbiddenMoves', 'forbiddenMoves', 'forbidden_moves'),
    coverageNote: textValue(row, 'CoverageNote', 'coverageNote', 'coverage_note') || sourceCoverage?.note || ''
  };
}

function normalizeChapterBudget(row) {
  const budget = objectValue(row, 'WordBudget', 'wordBudget', 'word_budget') || row;
  const normalized = {
    targetWords: numberValue(budget, 'TargetWords', 'targetWords', 'target_words'),
    minWords: numberValue(budget, 'MinWords', 'minWords', 'min_words'),
    maxWords: numberValue(budget, 'MaxWords', 'maxWords', 'max_words'),
    sourceRunes: numberValue(budget, 'SourceRunes', 'sourceRunes', 'source_runes') ||
      numberValue(row, 'SourceRunes', 'sourceRunes', 'source_runes'),
    targetRunes: numberValue(budget, 'TargetRunes', 'targetRunes', 'target_runes') ||
      numberValue(row, 'TargetRunes', 'targetRunes', 'target_runes'),
    minRunes: numberValue(budget, 'MinRunes', 'minRunes', 'min_runes') ||
      numberValue(row, 'TargetMinRunes', 'targetMinRunes', 'target_min_runes'),
    maxRunes: numberValue(budget, 'MaxRunes', 'maxRunes', 'max_runes') ||
      numberValue(row, 'TargetMaxRunes', 'targetMaxRunes', 'target_max_runes'),
    tolerance: numberValue(budget, 'Tolerance', 'tolerance')
  };
  return Object.values(normalized).some((value) => Number(value) > 0) ? normalized : null;
}

function normalizeSourceCoverage(row) {
  const coverage = objectValue(row, 'SourceCoverage', 'sourceCoverage', 'source_coverage') || row;
  const chapters = arrayValue(coverage, 'Chapters', 'chapters', 'SourceChapters', 'sourceChapters', 'source_chapters');
  const from = numberValue(coverage, 'From', 'from') || numberValue(row, 'SourceRangeFrom', 'sourceRangeFrom', 'source_range_from');
  const to = numberValue(coverage, 'To', 'to') || numberValue(row, 'SourceRangeTo', 'sourceRangeTo', 'source_range_to');
  const range = objectValue(row, 'SourceRange', 'sourceRange', 'source_range');
  const normalized = {
    chapters,
    from: from || numberValue(range, 'From', 'from'),
    to: to || numberValue(range, 'To', 'to'),
    runes: numberValue(coverage, 'Runes', 'runes') || numberValue(row, 'SourceRunes', 'sourceRunes', 'source_runes'),
    isAdded: Boolean(valueByKey(coverage, 'IsAdded', 'isAdded', 'is_added') || valueByKey(row, 'IsAdded', 'isAdded', 'is_added')),
    note: textValue(coverage, 'Note', 'note') || textValue(row, 'CoverageNote', 'coverageNote', 'coverage_note')
  };
  return normalized.chapters.length || normalized.from || normalized.to || normalized.runes || normalized.isAdded || normalized.note
    ? normalized
    : null;
}

export function normalizeProjectStyleCatalog(data = {}) {
  const styles = (Array.isArray(data?.styles) ? data.styles : [])
    .map((style) => ({
      id: String(style?.id || '').trim(),
      label: String(style?.label || '').trim()
    }))
    .filter((style) => style.id);
  const defaultStyle = String(data?.default_style || data?.defaultStyle || '').trim() ||
    (styles.some((style) => style.id === 'default') ? 'default' : styles[0]?.id || 'default');
  return { styles, defaultStyle };
}

export function resolveProjectStyleID(snapshot, runtime, projectSettings = {}) {
  return textValue(snapshot, 'Style', 'style') ||
    textValue(runtime?.config, 'style', 'Style') ||
    String(projectSettings.defaultStyle || '').trim() ||
    projectSettings.styles?.[0]?.id ||
    'default';
}

export function projectStyleLabel(styles = [], styleID = '') {
  const id = String(styleID || '').trim() || 'default';
  const match = styles.find((style) => style.id === id);
  return String(match?.label || '').trim() || id;
}

export function normalizeSimulationMode(mode) {
  const normalized = String(mode || '').trim().toLowerCase();
  return normalized === 'reinforced' ? 'reinforced' : 'normal';
}

export function simulationModeLabel(mode) {
  return normalizeSimulationMode(mode) === 'reinforced' ? '强化仿写' : '普通仿写';
}

export function resolveProjectSimulationMode(snapshot, runtime) {
  return normalizeSimulationMode(
    textValue(snapshot, 'SimulationMode', 'simulation_mode') ||
      textValue(runtime?.config, 'simulation_mode', 'SimulationMode')
  );
}

export function snapshotHasStartedWritingContent(snapshot) {
  return Boolean(
    numberValue(snapshot, 'CompletedCount', 'completed_count') ||
      numberValue(snapshot, 'TotalWordCount', 'total_word_count')
  );
}

export function isProjectStyleLocked(snapshot) {
  return snapshotHasStartedWritingContent(snapshot);
}

export function canEditProjectStyle({ activeProject, busy = false, projectSettings = {}, snapshot = null } = {}) {
  return Boolean(activeProject?.id) &&
    Boolean(snapshot) &&
    !busy &&
    !isProjectRunning(snapshot) &&
    !isProjectStyleLocked(snapshot) &&
    projectSettings.loadStatus !== 'running' &&
    projectSettings.saveStatus !== 'running' &&
    Array.isArray(projectSettings.styles) &&
    projectSettings.styles.length > 0;
}

export function canSubmitProjectStyle({
  activeProject,
  busy = false,
  currentStyle = '',
  projectSettings = {},
  snapshot = null
} = {}) {
  const selectedStyle = String(projectSettings.selectedStyle || '').trim();
  return canEditProjectStyle({ activeProject, busy, projectSettings, snapshot }) &&
    Boolean(selectedStyle) &&
    selectedStyle !== String(currentStyle || '').trim();
}

export function buildProjectStyleSaveRequest(activeProject, projectSettings = {}) {
  const projectId = String(activeProject?.id || '').trim();
  if (!projectId) {
    return { ok: false, error: '请选择一个项目' };
  }
  const style = String(projectSettings.selectedStyle || '').trim();
  if (!style) {
    return { ok: false, error: '请选择文风' };
  }
  return { ok: true, projectId, style };
}

export function canSubmitProjectSimulationMode({
  activeProject,
  busy = false,
  coCreateActive = false,
  currentSimulationMode = '',
  projectSettings = {},
  snapshot = null
} = {}) {
  const selectedMode = normalizeSimulationMode(projectSettings.selectedSimulationMode);
  return Boolean(activeProject?.id) &&
    !busy &&
    !isProjectRunning(snapshot) &&
    coCreateActive !== true &&
    projectSettings.simulationModeSaveStatus !== 'running' &&
    selectedMode !== normalizeSimulationMode(currentSimulationMode);
}

export function buildProjectSimulationModeSaveRequest(activeProject, projectSettings = {}) {
  const projectId = String(activeProject?.id || '').trim();
  if (!projectId) {
    return { ok: false, error: '请选择一个项目' };
  }
  const mode = normalizeSimulationMode(projectSettings.selectedSimulationMode);
  return { ok: true, projectId, mode };
}

function arrayValue(source, ...keys) {
  for (const key of keys) {
    if (Array.isArray(source?.[key])) {
      return source[key];
    }
  }
  return [];
}

function objectValue(source, ...keys) {
  for (const key of keys) {
    const value = source?.[key];
    if (value && typeof value === 'object' && !Array.isArray(value)) {
      return value;
    }
  }
  return null;
}

function valueByKey(source, ...keys) {
  for (const key of keys) {
    if (source?.[key] !== undefined && source?.[key] !== null) {
      return source[key];
    }
  }
  return null;
}

function numberValue(source, ...keys) {
  for (const key of keys) {
    const value = numberFromValue(source?.[key]);
    if (value > 0) {
      return value;
    }
  }
  return 0;
}

function numberFromValue(value) {
  const number = Number(value);
  return Number.isFinite(number) && number > 0 ? number : 0;
}

function textValue(source, ...keys) {
  for (const key of keys) {
    const value = String(source?.[key] ?? '').trim();
    if (value) {
      return value;
    }
  }
  return '';
}

function libraryItemsFromResponse(data) {
  const candidates = [
    data?.items,
    data?.entries,
    data?.profiles,
    data?.simulations,
    data?.novels,
    data?.library
  ];
  return candidates.find((candidate) => Array.isArray(candidate)) || [];
}

function libraryMessageFromResponse(data) {
  return String(data?.message || data?.Message || '').trim();
}

export function simulationFilesFromResponse(data) {
  const candidates = [
    data?.files,
    data?.Files,
    data?.uploaded_files,
    data?.uploadedFiles,
    data?.source_files,
    data?.sourceFiles
  ];
  const rawFiles = candidates.find((candidate) => Array.isArray(candidate)) || [];
  return rawFiles
    .map((file) => {
      if (typeof file === 'string') {
        const relativePath = file.trim();
        const name = fileNameFromPath(relativePath) || relativePath;
        return name ? {
          name,
          original_name: name,
          size: 0,
          relative_path: relativePath
        } : null;
      }
      if (!file || typeof file !== 'object') {
        return null;
      }
      const relativePath = textValue(file, 'relative_path', 'relativePath', 'RelativePath', 'path', 'Path', 'name', 'Name');
      const name = textValue(file, 'name', 'Name') || fileNameFromPath(relativePath);
      const originalName = textValue(file, 'original_name', 'originalName', 'OriginalName') || name;
      const size = Number(file.size || file.Size || 0);
      return name ? {
        name,
        original_name: originalName,
        size: Number.isFinite(size) && size > 0 ? size : 0,
        relative_path: relativePath || name
      } : null;
    })
    .filter(Boolean);
}

function libraryEntryName(entry) {
  if (typeof entry === 'string') {
    return entry.trim();
  }
  const sourceFile = entry?.source_file || entry?.sourceFile || entry?.file;
  return textValue(entry, 'name', 'Name', 'title', 'Title', 'id', 'ID', 'profile_name', 'novel_name') ||
    textValue(sourceFile, 'name', 'Name', 'relative_path', 'RelativePath');
}

export function libraryEntryMeta(entry) {
  if (!entry || typeof entry === 'string') {
    return '';
  }
  const sourceFile = entry.source_file || entry.sourceFile || entry.file;
  const profileVersion = textValue(entry, 'profile_version', 'profileVersion', 'ProfileVersion');
  const profileHealth = textValue(entry, 'health_state', 'healthState', 'HealthState');
  const migrated = Boolean(valueByKey(entry, 'migrated', 'Migrated'));
  const sourceArchived = Boolean(valueByKey(entry, 'source_archived', 'sourceArchived', 'SourceArchived'));
  const archivedSourceCount = numberValue(
    entry,
    'archived_source_count',
    'archivedSourceCount',
    'ArchivedSourceCount'
  );
  const profileMeta = profileVersion
    ? `${profileVersion} · ${sourceArchived ? `含原语料 ${archivedSourceCount || ''} 篇`.trim() : simulationHealthLabel(profileHealth)}${migrated ? ' · v1 已迁移' : ''}`
    : '';
  const parts = [
    profileMeta,
    textValue(entry, 'description', 'Description', 'summary', 'Summary'),
    textValue(sourceFile, 'name', 'Name'),
    formatOptionalBytes(entry.size || entry.Size || sourceFile?.size || sourceFile?.Size),
    formatOptionalDate(entry.updated_at || entry.UpdatedAt || entry.created_at || entry.CreatedAt)
  ].filter(Boolean);
  return parts.slice(0, 2).join(' · ');
}

export function adaptationStatusFromNovelLoad(data) {
  const status = data?.adaptation || {};
  const value = status.analysis_status || status.analysisStatus ||
    (data?.running ? 'running' : data?.analyzed ? 'done' : 'paused');
  return String(value || 'paused').trim() || 'paused';
}

export function adaptationEventsFromNovelLoad(data) {
  const status = data?.adaptation || {};
  const events = status.analysis_events || status.analysisEvents || data?.events || [];
  return Array.isArray(events) ? events : [];
}

function sourceFileFromNovelLoad(data, entry, name) {
  const source = data?.source_file || data?.sourceFile || data?.file || entry?.source_file || entry?.sourceFile || entry?.file || entry;
  if (source && typeof source === 'object') {
    const relativePath = textValue(source, 'relative_path', 'RelativePath', 'path', 'Path', 'name', 'Name') || name;
    return {
      name: textValue(source, 'name', 'Name') || fileNameFromPath(relativePath),
      original_name: textValue(source, 'original_name', 'OriginalName') || textValue(source, 'name', 'Name') || name,
      size: Number(source.size || source.Size || 0),
      relative_path: relativePath
    };
  }
  const relativePath = String(source || name || '').trim();
  return {
    name: fileNameFromPath(relativePath) || name,
    original_name: fileNameFromPath(relativePath) || name,
    size: 0,
    relative_path: relativePath || name
  };
}

function fileNameFromPath(path) {
  const value = String(path || '').trim();
  if (!value) {
    return '';
  }
  const parts = value.split(/[\\/]/);
  return parts[parts.length - 1] || value;
}

function formatOptionalBytes(value) {
  const size = Number(value || 0);
  return size > 0 ? formatBytes(size) : '';
}

function formatOptionalDate(value) {
  if (!value) {
    return '';
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleDateString();
}

function isFreshProject(snapshot) {
  if (!snapshot) {
    return false;
  }
  return !String(snapshot.NovelName || snapshot.novel_name || '').trim() &&
    !String(snapshot.Phase || snapshot.phase || '').trim() &&
    Number(snapshot.TotalChapters || snapshot.total_chapters || 0) === 0 &&
    Number(snapshot.CompletedCount || snapshot.completed_count || 0) === 0 &&
    Number(snapshot.TotalWordCount || snapshot.total_word_count || 0) === 0;
}

function optionalNonNegativeInt(value, label) {
  const raw = String(value || '').trim();
  if (!raw) {
    return 0;
  }
  const parsed = Number(raw);
  if (!Number.isInteger(parsed) || parsed < 0) {
    throw new Error(`${label} 必须是非负整数`);
  }
  return parsed;
}

export function buildExportSuggestedName(exportJob = {}, activeProject = {}) {
  const ext = exportExtensionForFormat(exportJob.format);
  const raw = String(exportJob.path || activeProject?.name || 'novel').trim() || 'novel';
  let name = sanitizeExportSuggestedName(fileNameFromPath(raw)) || 'novel';
  name = name.replace(/\.(txt|epub)$/i, '');
  return `${name}${ext}`;
}

export function isExportActionBusy(exportJob = {}) {
  return exportJob.status === 'running';
}

function exportExtensionForFormat(format) {
  return String(format || '').toLowerCase() === 'epub' ? '.epub' : '.txt';
}

function sanitizeExportSuggestedName(name) {
  return String(name || '')
    .replace(/[\\/:*?"<>|\u0000-\u001f]/g, '_')
    .replace(/^\.+|\.+$/g, '')
    .trim();
}

async function saveExportBlob(blob, target, fallbackName) {
  const fileName = target?.name || fallbackName || 'novel.txt';
  triggerBrowserDownload(blob, fileName);
  return fileName;
}

function triggerBrowserDownload(blob, fileName) {
  if (typeof document === 'undefined' || typeof URL === 'undefined') {
    return;
  }
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = fileName;
  link.style.display = 'none';
  document.body.appendChild(link);
  link.click();
  link.remove();
  globalThis.setTimeout?.(() => URL.revokeObjectURL(url), 1000);
}

function formatCompact(value) {
  return new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(Number(value || 0));
}

function formatPercent(part, total) {
  const denominator = Number(total || 0);
  if (!denominator) {
    return 'n/a';
  }
  return `${Math.round((Number(part || 0) / denominator) * 100)}%`;
}

function formatUSD(value) {
  return `$${Number(value || 0).toFixed(4)}`;
}

function formatScore(value) {
  const score = Number(value || 0);
  if (!score) {
    return 'n/a';
  }
  return score.toFixed(1);
}

function formatBytes(value) {
  const bytes = Number(value || 0);
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  if (bytes < 1024 * 1024) {
    return `${(bytes / 1024).toFixed(1)} KB`;
  }
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function FoundationProjectSwitchDialog({ project, onCancel, onConfirm }) {
  const cancelRef = useRef(null);
  const dialogRef = useRef(null);
  useEffect(() => {
    cancelRef.current?.focus();
    const keydown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onCancel();
      }
      if (event.key === 'Tab') {
        const buttons = [...(dialogRef.current?.querySelectorAll('button:not(:disabled)') || [])];
        if (event.shiftKey && document.activeElement === buttons[0]) {
          event.preventDefault();
          buttons.at(-1)?.focus();
        } else if (!event.shiftKey && document.activeElement === buttons.at(-1)) {
          event.preventDefault();
          buttons[0]?.focus();
        }
      }
    };
    document.addEventListener('keydown', keydown);
    return () => document.removeEventListener('keydown', keydown);
  }, [onCancel]);
  return <div className="foundation-dialog-backdrop" role="presentation"><div aria-describedby="foundation-project-switch-description" aria-labelledby="foundation-project-switch-title" aria-modal="true" className="foundation-dialog" ref={dialogRef} role="alertdialog">
    <h3 id="foundation-project-switch-title">切换项目并离开未发布草稿？</h3>
    <p id="foundation-project-switch-description">切换到“{project?.name || project?.id}”会关闭当前设定中心；未通过 preview/apply 的页面草稿不会写入服务器。</p>
    <div className="inline-actions"><button ref={cancelRef} className="tool-button" type="button" onClick={onCancel}>留在当前项目</button><button className="tool-button danger" type="button" onClick={onConfirm}>确认切换项目</button></div>
  </div></div>;
}
