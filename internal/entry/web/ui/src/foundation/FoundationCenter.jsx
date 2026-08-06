import { useCallback, useEffect, useReducer, useRef, useState } from 'react';
import {
  analyzeCharacters, applyFoundation, confirmCharacterCandidate, discardCharacterWorkspace, foundationError, foundationIdempotencyKey,
  loadCharacterWorkspace, loadFoundation, previewFoundation, retryCharacterWorkspace, retryFoundation, reviewCharacters
} from './foundationApi.js';
import { canApplyFoundation, createFoundationState, foundationReducer } from './foundationReducer.js';
import { cloneFoundation, foundationReadonlyReasonLabel } from './foundationModel.js';
import { characterConfirmationRequiredFromWorkspace } from './characterConfirmation.js';
import { FoundationOverview } from './FoundationOverview.jsx';
import { CharacterEditor } from './CharacterEditor.jsx';
import { RelationshipEditor } from './RelationshipEditor.jsx';
import { WorldRuleEditor } from './WorldRuleEditor.jsx';
import { FoundationPreview } from './FoundationPreview.jsx';
import { FoundationRevisionStatus } from './FoundationRevisionStatus.jsx';
import './foundation.css';

const tabs = [
  ['overview', '概览'], ['characters', '角色卡'], ['relationships', '计划关系'],
  ['rules', '世界规则'], ['preview', '差异与影响'], ['revision', '修订状态']
];

export function FoundationCenter({
  projectId,
  onClose,
  onOpenReview,
  onDirtyChange,
  initialTab = '',
  requestedNavigation,
  onNavigationHandled
}) {
  const [state, dispatch] = useReducer(foundationReducer, projectId, createFoundationState);
  const [tab, setTab] = useState(() => validFoundationTab(initialTab) ? initialTab : legacyFoundationTab());
  const [characterSubmitting, setCharacterSubmitting] = useState(false);
  const [characterAgentOpenRequestId, setCharacterAgentOpenRequestId] = useState(0);
  const versionRef = useRef(0);
  const stateRef = useRef(state);
  const characterSubmittingRef = useRef(false);
  const abortRef = useRef(null);
  const characterAbortRef = useRef(null);
  const applyKeyRef = useRef({ previewID: '', key: '' });
  const [closeRequested, setCloseRequested] = useState(null);
  stateRef.current = state;

  const requestContext = useCallback(() => ({ projectId, requestVersion: versionRef.current }), [projectId]);
  const load = useCallback(async ({ preserveStale = false, preserveBusy = false, classifiedError = null } = {}) => {
    const version = versionRef.current;
    const controller = new AbortController();
    abortRef.current?.abort();
    abortRef.current = controller;
    if (!preserveStale && !preserveBusy) dispatch({ type: 'load_start', projectId, requestVersion: version });
    try {
      const response = await loadFoundation(projectId, controller.signal);
      if (controller.signal.aborted || version !== versionRef.current) return;
      dispatch({ type: preserveStale ? 'stale_server_loaded' : preserveBusy ? 'busy_server_loaded' : 'load_success', projectId, requestVersion: version, response, error: classifiedError });
    } catch (error) {
      if (controller.signal.aborted || version !== versionRef.current) return;
      dispatch({ type: 'load_failed', projectId, requestVersion: version, error: foundationError(error) });
    }
  }, [projectId]);

  const loadCharacters = useCallback(async ({ preserveReviewStale = false, runId = '' } = {}) => {
    const version = versionRef.current;
    const controller = new AbortController();
    characterAbortRef.current?.abort();
    characterAbortRef.current = controller;
    try {
      const response = await loadCharacterWorkspace(projectId, runId, controller.signal);
      if (controller.signal.aborted || version !== versionRef.current) return;
      dispatch({ type: 'character_workspace_success', projectId, requestVersion: version, response, preserveReviewStale });
    } catch (error) {
      if (controller.signal.aborted || version !== versionRef.current) return;
      dispatch({ type: 'character_workspace_failed', projectId, requestVersion: version, error: foundationError(error) });
    }
  }, [projectId]);

  useEffect(() => {
    versionRef.current += 1;
    setTab(validFoundationTab(initialTab) ? initialTab : legacyFoundationTab());
    applyKeyRef.current = { previewID: '', key: '' };
    load();
    return () => { versionRef.current += 1; abortRef.current?.abort(); characterAbortRef.current?.abort(); };
  }, [initialTab, projectId, load]);

  useEffect(() => {
    if (!state.server || !state.draft) return;
    loadCharacters({ preserveReviewStale: state.characterReviewStale });
  }, [state.server?.baseRevision, state.server?.baseAuditSignature, loadCharacters]);

  useEffect(() => {
    if (!['applying', 'auditing', 'regenerating'].includes(state.status)) return undefined;
    let cancelled = false;
    const poll = async () => {
      const version = versionRef.current;
      try {
        const response = await loadFoundation(projectId);
        if (!cancelled && version === versionRef.current) dispatch({ type: 'refresh_runtime', ...requestContext(), response });
      } catch {
        // Poll failures remain recoverable via the explicit refresh action.
      }
    };
    const timer = globalThis.setInterval(poll, 1800);
    poll();
    return () => { cancelled = true; globalThis.clearInterval(timer); };
  }, [projectId, requestContext, state.status, state.server?.activeRevision?.updated_at]);

  useEffect(() => {
    const run = state.characterWorkspace?.run;
    if (!['queued', 'running'].includes(run?.status)) return undefined;
    const timer = globalThis.setInterval(() => loadCharacters({ preserveReviewStale: state.characterReviewStale, runId: run.run_id }), 1500);
    return () => globalThis.clearInterval(timer);
  }, [state.characterWorkspace?.run?.run_id, state.characterWorkspace?.run?.status, state.characterReviewStale, loadCharacters]);

  useEffect(() => {
    const protect = (event) => {
      if (!hasUnpublishedFoundationDraft(state.status)) return;
      event.preventDefault();
      event.returnValue = '';
    };
    globalThis.addEventListener?.('beforeunload', protect);
    return () => globalThis.removeEventListener?.('beforeunload', protect);
  }, [state.status]);

  useEffect(() => {
    onDirtyChange?.(hasUnpublishedFoundationDraft(state.status));
  }, [state.status, onDirtyChange]);

  useEffect(() => () => onDirtyChange?.(false), [onDirtyChange]);

  const characterConfirmationRequired = characterConfirmationRequiredFromWorkspace(state.characterWorkspace);
  useEffect(() => {
    const requestedTab = requestedNavigation?.tab;
    const requestId = Number(requestedNavigation?.requestId || 0);
    if (!requestId ||
      requestedNavigation?.projectId !== projectId ||
      !tabs.some(([id]) => id === requestedTab)) {
      return;
    }
    if (requestedNavigation?.anchor === 'character-confirm-action' && !characterConfirmationRequired) {
      return;
    }
    if (requestedNavigation?.anchor === 'foundation-character-agent') {
      setCharacterAgentOpenRequestId(requestId);
    }
    setTab(requestedTab);
    globalThis.requestAnimationFrame?.(() => {
      const target = requestedNavigation?.anchor
        ? globalThis.document?.getElementById(requestedNavigation.anchor)
        : globalThis.document?.getElementById(`foundation-tab-${requestedTab}`);
      target?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
      const characterAgentToggle = requestedNavigation?.anchor === 'foundation-character-agent'
        ? target?.querySelector('.character-agent-toggle')
        : null;
      if (characterAgentToggle?.getAttribute('aria-expanded') !== 'true') {
        characterAgentToggle?.click();
      }
      (characterAgentToggle || target)?.focus?.();
      onNavigationHandled?.(requestId);
    });
  }, [
    characterConfirmationRequired,
    onNavigationHandled,
    projectId,
    requestedNavigation?.anchor,
    requestedNavigation?.projectId,
    requestedNavigation?.requestId,
    requestedNavigation?.tab
  ]);

  useEffect(() => {
    if (state.status === 'completed') onOpenReview?.(state.server?.mode);
  }, [state.status, state.server?.mode, onOpenReview]);

  if (!projectId) return <div className="foundation-center empty-state">请先打开项目。</div>;
  if (state.projectId !== projectId) {
    return <div className="foundation-center foundation-loading" aria-live="polite" role="status">正在切换项目设定…</div>;
  }
  if (state.status === 'failed' && (!state.server || !state.draft)) {
    return <FoundationLoadFailure error={state.error} onRetry={() => load()} />;
  }
  if (state.status === 'loading' || !state.server || !state.draft) return <div className="foundation-center foundation-loading" aria-live="polite" role="status">正在加载 StoryFoundation…</div>;

  const disabled = !state.server.editable || ['previewing', 'applying', 'auditing', 'regenerating', 'awaiting_outline_approval', 'completed', 'failed', 'stale', 'readonly'].includes(state.status);
  const edit = (change) => dispatch({ type: 'edit', ...requestContext(), draft: { ...state.draft, ...change } });
  const runPreview = async () => {
    dispatch({ type: 'preview_start', ...requestContext() });
    if (state.status !== 'dirty' || !state.validation.valid) return;
    const version = versionRef.current;
    const controller = new AbortController(); abortRef.current = controller;
    try {
      const response = await previewFoundation(projectId, state.server, cloneFoundation(state.draft), controller.signal);
      if (version !== versionRef.current || controller.signal.aborted) return;
      dispatch({ type: 'preview_success', ...requestContext(), preview: response.preview });
      setTab('preview');
    } catch (error) {
      if (version !== versionRef.current || controller.signal.aborted) return;
      const classified = foundationError(error);
      dispatch({ type: 'preview_failed', ...requestContext(), error: classified });
      if (classified.code.includes('stale')) await load({ preserveStale: true });
      else if (['foundation_busy', 'foundation_readonly'].includes(classified.code)) await load({ preserveBusy: true, classifiedError: classified });
    }
  };
  const runApply = async () => {
    if (!canApplyFoundation(state)) return dispatch({ type: 'apply_start', ...requestContext(), idempotencyKey: '' });
    const previewID = state.preview.id;
    if (applyKeyRef.current.previewID !== previewID) applyKeyRef.current = { previewID, key: foundationIdempotencyKey('apply') };
    const key = applyKeyRef.current.key;
    dispatch({ type: 'apply_start', ...requestContext(), idempotencyKey: key });
    const version = versionRef.current;
    try {
      const response = await applyFoundation(projectId, previewID, key);
      if (version !== versionRef.current) return;
      dispatch({ type: 'apply_success', ...requestContext(), revision: response.revision }); setTab('revision');
      if (['awaiting_outline_approval', 'completed'].includes(response.revision?.stage)) onOpenReview?.(state.server.mode);
    } catch (error) {
      if (version !== versionRef.current) return;
      const classified = foundationError(error);
      dispatch({ type: 'apply_failed', ...requestContext(), error: classified });
      if (classified.code.includes('stale')) await load({ preserveStale: true });
      else if (['foundation_busy', 'foundation_readonly'].includes(classified.code)) await load({ preserveBusy: true, classifiedError: classified });
    }
  };
  const runRetry = async () => {
    const key = foundationIdempotencyKey('retry');
    dispatch({ type: 'retry_start', ...requestContext() });
    if (state.status !== 'failed' || !state.server.allowedOperations.includes('retry')) return;
    try {
      const response = await retryFoundation(projectId, key);
      dispatch({ type: 'retry_success', ...requestContext(), revision: response.revision });
    } catch (error) { dispatch({ type: 'retry_failed', ...requestContext(), error: foundationError(error) }); }
  };
  const runCharacterOperation = async (operation, mode = '') => {
    if (characterSubmittingRef.current) return;
    const version = versionRef.current;
    const submittedFingerprint = state.draftFingerprint;
    const controller = new AbortController();
    characterAbortRef.current?.abort();
    characterAbortRef.current = controller;
    characterSubmittingRef.current = true;
    setCharacterSubmitting(true);
    dispatch({ type: 'character_workspace_clear_error', ...requestContext() });
    try {
      const response = await operation(controller.signal);
      if (version !== versionRef.current || controller.signal.aborted) return;
      const changedDuringRequest = stateRef.current.draftFingerprint !== submittedFingerprint;
      dispatch({
        type: 'character_workspace_success', ...requestContext(), response,
        preserveReviewStale: stateRef.current.characterReviewStale,
        forceReviewStale: changedDuringRequest && mode === 'review'
      });
    } catch (error) {
      if (version !== versionRef.current || controller.signal.aborted) return;
      const classified = foundationError(error);
      dispatch({ type: 'character_workspace_failed', ...requestContext(), error: classified });
      if (classified.code.includes('stale')) await load({ preserveStale: true });
    } finally {
      if (version === versionRef.current) {
        characterSubmittingRef.current = false;
        setCharacterSubmitting(false);
      }
    }
  };
  const runCharacterAnalyze = (options) => {
    const analyzeInput = characterReviewInput(state);
    return runCharacterOperation((signal) => analyzeCharacters(
      projectId, state.server, cloneFoundation(analyzeInput.foundation),
      {
        ...options,
        candidateRevision: analyzeInput.revision,
        idempotencyKey: foundationIdempotencyKey('character-analyze'),
        signal
      }
    ), 'analyze');
  };
  const runCharacterReview = () => {
    const reviewInput = characterReviewInput(state);
    return runCharacterOperation((signal) => reviewCharacters(
      projectId, state.server, cloneFoundation(reviewInput.foundation),
      {
        candidateRevision: reviewInput.revision,
        sourceMappings: state.characterWorkspace?.sourceMappings || [],
        idempotencyKey: foundationIdempotencyKey('character-review'),
        signal
      }
    ), 'review');
  };
  const runCharacterRetry = () => runCharacterOperation((signal) => retryCharacterWorkspace(
    projectId, state.server, state.characterWorkspace.run,
    foundationIdempotencyKey('character-retry'), signal
  ), 'retry');
  const runCharacterDiscard = () => runCharacterOperation((signal) => discardCharacterWorkspace(
    projectId, state.server, state.characterWorkspace,
    foundationIdempotencyKey('character-discard'), signal
  ), 'discard');
  const runCharacterConfirm = () => runCharacterOperation(async (signal) => {
    await confirmCharacterCandidate(
      projectId,
      state.characterWorkspace?.candidate,
      foundationIdempotencyKey('character-confirm'),
      signal
    );
    await load();
    return loadCharacterWorkspace(projectId, '', signal);
  }, 'confirm');
  const requestClose = (event) => {
    if (hasUnpublishedFoundationDraft(state.status)) setCloseRequested(event.currentTarget);
    else onClose?.();
  };
  const characterWorkspace = state.characterWorkspace
    ? { ...state.characterWorkspace, reviewStale: state.characterReviewStale, error: state.characterWorkspaceError || state.characterWorkspace.error }
    : state.characterWorkspaceError ? { error: state.characterWorkspaceError, reviewStale: state.characterReviewStale } : null;
  const showingSourceOnly = state.server.mode === 'adaptation' &&
    !state.draft.characters.length &&
    Boolean(state.server.sourceFoundation?.characters?.length);
  const displayedRelationships = showingSourceOnly ? state.server.sourceFoundation?.relationships || [] : state.draft.relationships;
  const displayedWorldRules = showingSourceOnly ? state.server.sourceFoundation?.world_rules || [] : state.draft.world_rules;
  const displayedRelationshipCharacters = showingSourceOnly ? state.server.sourceFoundation?.characters || [] : state.draft.characters;
  const validationNotice = foundationValidationPresentation(state.server, state.validation);
  const openCharacterConfirmation = () => {
    setTab('characters');
    globalThis.requestAnimationFrame?.(() => {
      const target = globalThis.document?.getElementById('character-confirm-action');
      target?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
      target?.focus?.();
    });
  };

  return <div className="foundation-center">
    <header className="foundation-header"><div><span className="eyebrow">StoryFoundation</span><h1>设定中心</h1><p>统一管理原创与改编的目标故事设定；SourceFoundation 始终只读。</p></div>{onClose ? <button className="tool-button" type="button" onClick={requestClose}>返回创作</button> : null}</header>
    <div className="foundation-state-strip" aria-live="polite" role="status"><strong>{statusLabel(state.status)}</strong><span>target rev {state.server.baseRevision}</span>{state.server.readonlyReason ? <span>只读原因：{foundationReadonlyReasonLabel(state.server.readonlyReason)}</span> : null}</div>
    {characterConfirmationRequired ? <section className="foundation-next-action" aria-labelledby="foundation-next-action-title">
      <div><strong id="foundation-next-action-title">角色卡审核已通过，等待你的确认</strong><span>候选角色尚未发布。确认后才会写入 StoryFoundation，并继续补全关系、世界规则和后续规划。</span></div>
      <button className="tool-button accent" type="button" onClick={openCharacterConfirmation}>查看并确认角色卡</button>
    </section> : null}
    {state.error ? <div className="error-banner" role="alert"><strong>{state.error.code}</strong><span>{state.error.message}</span></div> : null}
    {state.illegalAction ? <div className="warning-note" role="status">{state.illegalAction}</div> : null}
    {state.status === 'stale' ? <div className="foundation-stale" role="alert"><strong>服务器基线已变化，草稿仍完整保留。</strong><span>先加载最新基线，再用当前草稿重新生成 preview。</span><button className="tool-button" disabled={!state.staleServer} type="button" onClick={() => dispatch({ type: 'rebase_stale', ...requestContext() })}>以最新基线重新对比</button></div> : null}
    {!showingSourceOnly && validationNotice.kind === 'actionable' ? <div aria-live="assertive" className="foundation-validation-summary" role="alert"><strong>请处理 {validationNotice.messages.length} 个字段问题</strong><ul>{validationNotice.messages.map((message) => <li key={message}>{message}</li>)}</ul></div> : null}
    {!characterConfirmationRequired && !showingSourceOnly && validationNotice.kind === 'generating' ? <div aria-live="polite" className="foundation-generation-status" role="status"><strong>设定正在生成，当前无需手动处理</strong><span>{state.server.planningReview?.brief ? '共创确认稿已作为故事前提展示；角色确认后会正式发布，世界规则由后续规划流程补齐。' : 'StoryFoundation 尚未发布，故事前提、角色和世界规则会由当前规划流程补齐；发布后页面会自动重新校验。'}</span></div> : null}
    {!showingSourceOnly && validationNotice.kind === 'readonly' ? <div aria-live="polite" className="foundation-readonly-validation" role="status"><strong>当前设定只读，暂不能处理校验项</strong><span>恢复到可编辑阶段后再处理以下内容：{validationNotice.messages.join('；')}</span></div> : null}
    <nav aria-label="设定中心区域" className="foundation-tabs" role="tablist">{tabs.map(([id, label], index) => <button aria-controls={`foundation-panel-${id}`} aria-selected={tab === id} className={tab === id ? 'active' : ''} id={`foundation-tab-${id}`} key={id} role="tab" tabIndex={tab === id ? 0 : -1} type="button" onClick={() => setTab(id)} onKeyDown={(event) => moveTab(event, index, setTab)}>{label}</button>)}</nav>
    <main aria-labelledby={`foundation-tab-${tab}`} className={`foundation-panel foundation-panel-${tab}`} id={`foundation-panel-${tab}`} role="tabpanel">
      {tab === 'overview' ? <FoundationOverview server={state.server} draft={state.draft} workspace={characterWorkspace} disabled={disabled} premiseError={state.validation.fields.premise} onPremiseChange={(premise) => edit({ premise })} onOpenCharacters={() => setTab('characters')} /> : null}
      {tab === 'characters' ? <CharacterEditor
        value={state.draft.characters} coreCast={state.server.coreCast}
        mode={state.server.mode} sourceFoundation={state.server.sourceFoundation}
        relationships={state.draft.relationships} disabled={disabled} dirty={state.status === 'dirty'}
        errors={state.validation.fields} workspace={characterWorkspace} workspaceLoading={!state.characterWorkspace && !state.characterWorkspaceError}
        agentBusy={characterSubmitting}
        agentOpenRequestId={characterAgentOpenRequestId}
        onChange={(characters) => edit({ characters })}
        onOpenRelationships={() => setTab('relationships')}
        onAnalyze={runCharacterAnalyze} onReview={runCharacterReview}
        onRetry={runCharacterRetry} onDiscard={runCharacterDiscard} onConfirm={runCharacterConfirm}
      /> : null}
		{tab === 'relationships' ? <RelationshipEditor projectId={projectId} auditSignature={state.server.baseAuditSignature} coreCast={state.server.coreCast} value={displayedRelationships} characters={displayedRelationshipCharacters} reviewed={showingSourceOnly || state.draft.relationships_reviewed} disabled={disabled || showingSourceOnly} sourceOnly={showingSourceOnly} errors={showingSourceOnly ? {} : state.validation.fields} onChange={(relationships) => edit({ relationships })} onReviewedChange={(relationships_reviewed) => edit({ relationships_reviewed })} /> : null}
      {tab === 'rules' ? <WorldRuleEditor value={displayedWorldRules} disabled={disabled || showingSourceOnly} sourceOnly={showingSourceOnly} errors={showingSourceOnly ? {} : state.validation.fields} onChange={(world_rules) => edit({ world_rules })} /> : null}
      {tab === 'preview' ? <FoundationPreview preview={state.preview} dirty={state.status === 'dirty'} disabled={['previewing', 'applying'].includes(state.status)} canApply={canApplyFoundation(state)} onPreview={runPreview} onApply={runApply} /> : null}
      {tab === 'revision' ? <FoundationRevisionStatus server={state.server} status={state.status} busy={['applying', 'auditing', 'regenerating'].includes(state.status)} onRefresh={() => load({ preserveStale: state.status === 'stale' })} onRetry={runRetry} onOpenReview={() => onOpenReview?.(state.server.mode)} /> : null}
    </main>
    {!characterConfirmationRequired ? <footer className="foundation-actions"><span>{state.status === 'dirty' ? '有未预览的设定修改' : state.status === 'preview_ready' ? '预览已持久化，可应用' : '服务端状态已同步'}</span><button className="tool-button accent" disabled={state.status !== 'dirty' || !state.validation.valid} type="button" onClick={runPreview}>预览差异与影响</button></footer> : null}
    {closeRequested ? <CloseDraftDialog trigger={closeRequested} onCancel={() => setCloseRequested(null)} onConfirm={() => { setCloseRequested(null); onClose?.(); }} /> : null}
  </div>;
}

export function FoundationLoadFailure({ error, onRetry }) {
  return (
    <div className="foundation-center empty-state" role="alert">
      <strong>StoryFoundation 加载失败</strong>
      <span>{error?.message || '无法读取当前项目设定，请重试。'}</span>
      <button className="primary-button" type="button" onClick={onRetry}>重试</button>
    </div>
  );
}

function moveTab(event, index, setTab) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
  event.preventDefault();
  let next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
  const id = tabs[next][0]; setTab(id); globalThis.requestAnimationFrame?.(() => document.getElementById(`foundation-tab-${id}`)?.focus());
}
function statusLabel(status) { return ({ loading: '加载中', clean: '已同步', dirty: '有修改', previewing: '正在预览', preview_ready: '预览就绪', applying: '正在应用', auditing: '正在审查', regenerating: '正在重新生成', awaiting_outline_approval: '等待大纲 / 提案确认', completed: '已完成', failed: '修订失败', stale: '基线已过期', readonly: '只读' })[status] || status; }
function hasUnpublishedFoundationDraft(status) { return ['dirty', 'previewing', 'preview_ready'].includes(status); }

export function characterReviewInput(state) {
  const candidate = state?.characterWorkspace?.candidate;
  return {
    foundation: candidate?.foundation || state?.draft || {},
    revision: Number(candidate?.revision || 0)
  };
}

export function foundationValidationPresentation(server, validation) {
  const messages = Array.isArray(validation?.summary) ? validation.summary : [];
  if (!messages.length) return { kind: 'none', messages };
  if (server?.editable) return { kind: 'actionable', messages };
  if (server?.readonlyReason === 'planning_stage_not_editable' && Number(server?.baseRevision || 0) === 0) {
    return { kind: 'generating', messages: [] };
  }
  return { kind: 'readonly', messages };
}

function legacyFoundationTab() {
  const location = globalThis.location;
  const requested = new URLSearchParams(location?.search || '').get('foundation_tab') || String(location?.hash || '').replace(/^#/, '');
  if (requested === 'core' || requested === 'all-characters' || requested === 'characters') return 'characters';
  return tabs.some(([id]) => id === requested) ? requested : 'overview';
}

function validFoundationTab(tab) {
  return tabs.some(([id]) => id === tab);
}

function CloseDraftDialog({ trigger, onCancel, onConfirm }) {
  const cancelRef = useRef(null);
  const dialogRef = useRef(null);
  useEffect(() => {
    cancelRef.current?.focus();
    const keydown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onCancel();
        globalThis.requestAnimationFrame?.(() => trigger?.focus());
        return;
      }
      if (event.key !== 'Tab') return;
      const focusable = Array.from(dialogRef.current?.querySelectorAll('button:not(:disabled)') || []);
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', keydown);
    return () => document.removeEventListener('keydown', keydown);
  }, [onCancel, trigger]);
  return <div className="foundation-dialog-backdrop" role="presentation"><div ref={dialogRef} aria-describedby="foundation-close-description" aria-labelledby="foundation-close-title" aria-modal="true" className="foundation-dialog" role="alertdialog">
    <h3 id="foundation-close-title">离开并保留未发布草稿？</h3>
    <p id="foundation-close-description">当前修改尚未通过 Foundation preview/apply 发布。离开后页面内草稿不会自动保存到服务器。</p>
    <div className="inline-actions"><button ref={cancelRef} className="tool-button" type="button" onClick={onCancel}>继续编辑</button><button className="tool-button danger" type="button" onClick={onConfirm}>确认离开</button></div>
  </div></div>;
}
