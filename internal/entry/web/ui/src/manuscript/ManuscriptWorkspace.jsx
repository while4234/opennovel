import { useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ManuscriptTree } from './ManuscriptTree.jsx';
import { ManuscriptOutlineView } from './ManuscriptOutlineView.jsx';
import { ManuscriptReviewView } from './ManuscriptReviewView.jsx';
import { RevisionCompare } from './RevisionCompare.jsx';
import { RevisionHistory } from './RevisionHistory.jsx';
import { RevisionStatus } from './RevisionStatus.jsx';
import { ManuscriptReader } from './ManuscriptReader.jsx';
import { ManuscriptActionPanel } from './ManuscriptActionPanel.jsx';
import { ManuscriptManualEditor } from './ManuscriptManualEditor.jsx';
import { ChapterCombobox } from './ChapterCombobox.jsx';
import { invalidateManuscriptCache, invalidateManuscriptViews, loadManuscriptArtifact, loadManuscriptChunk, loadManuscriptHistory, loadManuscriptRecovery, loadManuscriptReviewDetail, loadManuscriptReviewPage, loadManuscriptTree, loadManuscriptVersion, previewManuscriptRestore, restoreManuscriptVersion, retryManuscriptRecovery } from './manuscript-api.js';
import { flattenManuscriptTree, MANUSCRIPT_TABS, mergeParagraphChunk } from './manuscript-state.js';
import { normalizeManuscriptMutationEvent } from './manuscript-events.js';
import './manuscript.css';

const newKey = () => `manuscript-workspace:${globalThis.crypto?.randomUUID?.() || Date.now()}`;
const RECOVERY_OWNER_LABELS = {
  adaptation_command: '改编修订命令',
  revision_publication: '结构修订发布',
  manuscript_publication: '正文原子发布',
  publication_authority: '发布授权维护',
  structure_migration: '结构迁移',
  unknown: '未知恢复事务'
};

export function ManuscriptWorkspace({ active = false, controlsTarget = null, projectId, onReturnToWriting }) {
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [tree, setTree] = useState([]), [selectedId, setSelectedId] = useState(''), [activeRevision, setActiveRevision] = useState(null);
  const [expansionMeta, setExpansionMeta] = useState({ phase: '', mode: 'normal', structureRevision: 1, structureSignature: '' }), [expansionLaunch, setExpansionLaunch] = useState(null);
  const [tab, setTab] = useState('prose'), [proseMode, setProseMode] = useState('current'), [current, setCurrent] = useState(null), [candidate, setCandidate] = useState(null);
  const [manualEditing, setManualEditing] = useState(false);
  const [history, setHistory] = useState({ items: [], nextCursor: 0, hasMore: false }), [historyVersion, setHistoryVersion] = useState(null);
  const [restorePreview, setRestorePreview] = useState(null), [artifacts, setArtifacts] = useState({}), [reviewDetails, setReviewDetails] = useState({});
  const [artifactLoading, setArtifactLoading] = useState({}), [artifactErrors, setArtifactErrors] = useState({});
  const [historyRecovery, setHistoryRecovery] = useState(false);
  const [busy, setBusy] = useState(false), [error, setError] = useState(''), [notice, setNotice] = useState('');
  const [autoLoading, setAutoLoading] = useState({ current: false, candidate: false });
  const [recovery, setRecovery] = useState(null);
  const requestRef = useRef({}), busyOwnerRef = useRef(''), projectEpochRef = useRef(0), selectionEpochRef = useRef(0), selectionIdRef = useRef(''), tabRef = useRef('prose'), previousProjectRef = useRef(projectId);
  const restoreKeys = useRef(new Map()), treeButtonRef = useRef(null), drawerRef = useRef(null);
  const contentScrollRef = useRef(null), scrollPositionsRef = useRef(new Map());
  const chapters = useMemo(() => flattenManuscriptTree(tree), [tree]);
  const node = chapters.find((item) => item.stable_id === selectedId);
  const selectedIndex = chapters.findIndex((item) => item.stable_id === selectedId);
  const previousChapter = selectedIndex > 0 && chapters[selectedIndex - 1]?.has_current && chapters[selectedIndex - 1]?.state === 'completed' ? chapters[selectedIndex - 1] : null;
  const nextChapter = selectedIndex >= 0 && chapters[selectedIndex + 1]?.has_current && chapters[selectedIndex + 1]?.state === 'completed' ? chapters[selectedIndex + 1] : null;
  function beginRequest(kind, stableId = selectionIdRef.current) {
    requestRef.current[kind]?.controller.abort();
    const controller = new AbortController(), sequence = (requestRef.current[kind]?.sequence || 0) + 1;
    const epoch = projectEpochRef.current, selectionEpoch = selectionEpochRef.current;
    requestRef.current[kind] = { controller, sequence, epoch, selectionEpoch, stableId };
    return { controller, sequence, epoch, selectionEpoch, stableId };
  }
  const isLatest = (kind, sequence, epoch = projectEpochRef.current, selectionEpoch, stableId) => {
    const request = requestRef.current[kind];
    if (request?.sequence !== sequence || projectEpochRef.current !== epoch) return false;
    if (kind === 'tree') return true;
    return selectionEpochRef.current === (selectionEpoch ?? request.selectionEpoch) && selectionIdRef.current === (stableId ?? request.stableId);
  };
  const selectionSnapshot = () => ({ projectId, projectEpoch: projectEpochRef.current, selectionEpoch: selectionEpochRef.current, stableId: selectionIdRef.current, tab: tabRef.current });
  const isCurrentSelection = (snapshot, includeTab = false) => snapshot.projectId === projectId
    && snapshot.projectEpoch === projectEpochRef.current
    && snapshot.selectionEpoch === selectionEpochRef.current
    && snapshot.stableId === selectionIdRef.current
    && (!includeTab || snapshot.tab === tabRef.current);
  function beginBusy(kind, request) {
    const owner = `${kind}:${request.sequence}:${request.epoch}:${request.selectionEpoch ?? ''}:${request.stableId ?? ''}`;
    busyOwnerRef.current = owner;
    setBusy(true);
    return owner;
  }
  function endBusy(owner) {
    if (busyOwnerRef.current !== owner) return;
    busyOwnerRef.current = '';
    setBusy(false);
  }
  function beginSelection(stableId) {
    selectionEpochRef.current += 1;
    selectionIdRef.current = stableId;
    Object.entries(requestRef.current).forEach(([kind, request]) => {
      if (kind !== 'tree') request.controller.abort();
    });
  }
  useEffect(() => () => {
    Object.values(requestRef.current).forEach((entry) => entry.controller.abort());
    invalidateManuscriptCache(projectId);
  }, [projectId]);
  useEffect(() => {
    if (previousProjectRef.current === projectId) return;
    previousProjectRef.current = projectId;
    projectEpochRef.current += 1;
    selectionEpochRef.current += 1;
    selectionIdRef.current = '';
    Object.values(requestRef.current).forEach((entry) => entry.controller.abort());
    requestRef.current = {};
    restoreKeys.current.clear();
    setTree([]); setSelectedId(''); setActiveRevision(null); setCurrent(null); setCandidate(null); setExpansionMeta({ phase: '', mode: 'normal', structureRevision: 1, structureSignature: '' }); setExpansionLaunch(null);
    setHistory({ items: [], nextCursor: 0, hasMore: false }); setHistoryVersion(null); setRestorePreview(null); setHistoryRecovery(false);
    busyOwnerRef.current = '';
    tabRef.current = 'prose';
    setArtifacts({}); setArtifactLoading({}); setArtifactErrors({}); setReviewDetails({}); setDrawerOpen(false); setError(''); setNotice(''); setBusy(false);
    invalidateManuscriptCache(projectId);
    setProseMode('current'); setRecovery(null); setAutoLoading({ current: false, candidate: false });
    setManualEditing(false);
    if (active && projectId) queueMicrotask(() => void loadTree());
  }, [projectId]);
  useEffect(() => {
    if (!active || !projectId) return undefined;
    const controller = new AbortController();
    if (!tree.length) void loadTree();
    loadManuscriptRecovery(projectId, controller.signal)
      .then((data) => setRecovery(data?.recovery || null))
      .catch((cause) => { if (cause.name !== 'AbortError') setNotice(`恢复状态读取失败：${cause.message}`); });
    return () => controller.abort();
  }, [active, projectId]);
  useEffect(() => {
    if (!drawerOpen) return undefined;
    const drawer = drawerRef.current;
    queueMicrotask(() => drawer?.querySelector('[data-manuscript-drawer-initial]')?.focus());
    const keepFocusInside = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setDrawerOpen(false);
        queueMicrotask(() => treeButtonRef.current?.focus());
        return;
      }
      if (event.key !== 'Tab' || !drawer) return;
      const focusable = [...drawer.querySelectorAll('button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')];
      if (!focusable.length) return;
      const first = focusable[0], last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    };
    drawer.addEventListener('keydown', keepFocusInside);
    return () => drawer.removeEventListener('keydown', keepFocusInside);
  }, [drawerOpen]);
  useEffect(() => {
    if (!projectId || !active || typeof EventSource === 'undefined') return undefined;
    const source = new EventSource(`/api/projects/${encodeURIComponent(projectId)}/events`);
    const refresh = (event) => {
      let detail = {};
      try { detail = event?.data ? JSON.parse(event.data) : {}; } catch { detail = {}; }
      const mutation = normalizeManuscriptMutationEvent(detail);
      if (!mutation) return;
      invalidateManuscriptViews(projectId, mutation);
      void refreshVisible(mutation);
    };
    source.onmessage = refresh;
    ['snapshot', 'progress', 'host_event', 'action'].forEach((name) => source.addEventListener(name, refresh));
    return () => source.close();
  }, [active, projectId, selectedId, tab]);
  useEffect(() => {
    if (!active || !selectedId) return;
    const key = `${projectId}:${selectedId}:${tab}:${proseMode}`;
    queueMicrotask(() => {
      if (contentScrollRef.current) contentScrollRef.current.scrollTop = scrollPositionsRef.current.get(key) || 0;
    });
  }, [active, projectId, proseMode, selectedId, tab]);
  useEffect(() => {
    if (!active || !selectedId || !['outline', 'volume', 'review'].includes(tab)) return;
    if (artifacts[tab] || artifactLoading[tab] || artifactErrors[tab]) return;
    queueMicrotask(() => void chooseTab(tab, true));
  }, [active, selectedId, tab, artifacts, artifactLoading, artifactErrors]);
  useEffect(() => {
    const refresh = (event) => {
      if (!String(event.detail?.path || '').includes(`/projects/${encodeURIComponent(projectId)}/`)) return;
      invalidateManuscriptViews(projectId, event.detail || {});
      if (active) void refreshVisible(event.detail || {});
    };
    window.addEventListener('ainovel:manuscript-mutated', refresh);
    return () => window.removeEventListener('ainovel:manuscript-mutated', refresh);
  }, [active, projectId, selectedId, tab]);

  async function refreshVisible() {
    const snapshot = selectionSnapshot();
    const result = await loadTree(false, snapshot);
    if (!result || !isCurrentSelection(snapshot)) return;
    if (snapshot.stableId) await selectChapter(snapshot.stableId, false, result.nodes, result.active_revision, true);
    if (!isCurrentSelection(snapshot, true)) return;
    if (snapshot.tab === 'history') await loadHistory(true);
    if (['outline', 'volume', 'review'].includes(snapshot.tab)) {
      setArtifacts((old) => { const next = { ...old }; delete next[snapshot.tab]; return next; });
      await chooseTab(snapshot.tab, true);
    }
  }
  async function loadTree(selectFirst = true, selectionGuard = null) {
    if (!projectId) return null;
    const request = beginRequest('tree');
    const { controller, sequence, epoch } = request;
    const busyOwner = beginBusy('tree', request);
    setError('');
    try {
      const data = await loadManuscriptTree(projectId, { signal: controller.signal });
      if (!isLatest('tree', sequence, epoch)) return null;
      const nodes = data.nodes || [];
      setTree(nodes); setActiveRevision(data.active_revision || null); setExpansionMeta({ phase: data.phase || '', mode: data.mode || 'normal', structureRevision: data.structure_revision || 1, structureSignature: data.structure_signature || '' });
      const first = flattenManuscriptTree(nodes)[0]?.stable_id || '';
      if (selectFirst && !selectedId && first) await selectChapter(first, false, nodes, data.active_revision);
      return data;
    } catch (cause) { if (cause.name !== 'AbortError' && isLatest('tree', sequence, epoch) && (!selectionGuard || isCurrentSelection(selectionGuard))) setError(cause.message); }
    finally { endBusy(busyOwner); }
    return null;
  }
  async function selectChapter(stableId, focus = false, knownTree = tree, knownActive = activeRevision, refreshOnly = false) {
    if (refreshOnly && stableId !== selectionIdRef.current) return;
    if (!refreshOnly) beginSelection(stableId);
    const request = beginRequest('chapter', stableId);
    const { controller, sequence, epoch, selectionEpoch } = request;
    const busyOwner = beginBusy('chapter', request);
    const retainsLastSuccessful = stableId === selectedId && current?.stable_id === stableId;
    if (!refreshOnly) {
      setManualEditing(false);
      setDrawerOpen(false);
      setSelectedId(stableId);
      if (!retainsLastSuccessful) { setCurrent(null); setCandidate(null); }
      setHistoryVersion(null); setRestorePreview(null); setHistoryRecovery(false); setHistory({ items: [], nextCursor: 0, hasMore: false }); setArtifacts({}); setArtifactLoading({}); setArtifactErrors({}); setReviewDetails({}); setError('');
      setAutoLoading({ current: false, candidate: false });
    }
    try {
      const next = await loadManuscriptChunk(projectId, stableId, { signal: controller.signal });
      if (!isLatest('chapter', sequence, epoch, selectionEpoch, stableId) || !next?.chapter?.content_signature || !(next.chapter.paragraphs || []).length) throw new Error('正文响应为空，已保留上次成功内容');
      setCurrent(next.chapter);
      void completeChapter('current', next.chapter, stableId);
      const selectedNode = flattenManuscriptTree(knownTree).find((item) => item.stable_id === stableId);
      if (selectedNode?.has_candidate && knownActive?.revision_id) {
        const draft = await loadManuscriptChunk(projectId, stableId, { view: 'candidate', version: knownActive.revision_id, signal: controller.signal });
        if (isLatest('chapter', sequence, epoch, selectionEpoch, stableId) && draft?.chapter?.content_signature && (draft.chapter.paragraphs || []).length) {
          const candidateChapter = { ...draft.chapter, revision_id: knownActive.revision_id };
          setCandidate(candidateChapter);
          void completeChapter('candidate', candidateChapter, stableId);
        }
      } else if (isLatest('chapter', sequence, epoch, selectionEpoch, stableId)) {
        setCandidate(null);
        setProseMode('current');
      }
      if (focus) queueMicrotask(() => document.querySelector('[role="treeitem"][aria-selected="true"]')?.focus());
    } catch (cause) { if (cause.name !== 'AbortError' && isLatest('chapter', sequence, epoch, selectionEpoch, stableId)) setError(cause.message); }
    finally { endBusy(busyOwner); }
  }
  async function completeChapter(view, initial, stableId = selectedId) {
    if (!initial?.next_cursor && initial?.next_cursor !== 0) return;
    const kind = `complete:${view}`, request = beginRequest(kind, stableId);
    const { controller, sequence, epoch, selectionEpoch } = request;
    setAutoLoading((previous) => ({ ...previous, [view]: true }));
    let combined = initial;
    try {
      while (combined.next_cursor != null) {
        const data = await loadManuscriptChunk(projectId, stableId, {
          view,
          version: combined.revision_id,
          signature: combined.content_signature,
          cursor: combined.next_cursor,
          limit: 100,
          signal: controller.signal
        });
        const incoming = data.chapter;
        if (!isLatest(kind, sequence, epoch, selectionEpoch, stableId)) return;
        if (incoming.stable_id !== stableId || incoming.view !== view || incoming.content_signature !== combined.content_signature) {
          throw new Error('正文版本在加载期间发生变化，请刷新后重试');
        }
        combined = mergeParagraphChunk(combined, incoming);
        (view === 'candidate' ? setCandidate : setCurrent)(combined);
      }
    } catch (cause) {
      if (cause.name !== 'AbortError' && isLatest(kind, sequence, epoch, selectionEpoch, stableId)) {
        setError(`完整正文加载中断：${cause.message}`);
      }
    } finally {
      if (isLatest(kind, sequence, epoch, selectionEpoch, stableId)) {
        setAutoLoading((previous) => ({ ...previous, [view]: false }));
      }
    }
  }
  async function more(view) {
    const old = view === 'candidate' ? candidate : current;
    if (!old || old.next_cursor == null) return;
    setError('');
    await completeChapter(view, old);
  }
  async function chooseTab(next, force = false) {
    if (next !== 'prose') setManualEditing(false);
    tabRef.current = next;
    setTab(next);
    if (['outline', 'volume', 'review'].includes(next) && (force || !artifacts[next])) {
      const kind = `artifact:${next}`, request = beginRequest(kind), { controller, sequence } = request;
      const volume = tree.find((item) => (item.children || []).some((arc) => (arc.children || []).some((chapter) => chapter.stable_id === selectedId)));
      const stableId = next === 'volume' ? volume?.stable_id : selectedId;
      if (!stableId) return;
      const busyOwner = beginBusy(kind, request);
      setArtifactLoading((old) => ({ ...old, [next]: true }));
      setArtifactErrors((old) => ({ ...old, [next]: '' }));
      try {
        const data = await loadManuscriptArtifact(projectId, next, stableId, next === 'outline' ? node?.content_signature : '', controller.signal);
        if (isLatest(kind, sequence)) setArtifacts((old) => ({ ...old, [next]: data.artifact }));
      } catch (cause) {
        if (cause.name !== 'AbortError' && isLatest(kind, sequence)) setArtifactErrors((old) => ({ ...old, [next]: cause.message }));
      } finally {
        if (isLatest(kind, sequence)) setArtifactLoading((old) => ({ ...old, [next]: false }));
        endBusy(busyOwner);
      }
      return;
    }
    if (next === 'history' && (force || !history.items.length)) await loadHistory(true);
  }
  function tabKeyDown(event, index) {
    let next = index;
    if (event.key === 'ArrowRight') next = (index + 1) % MANUSCRIPT_TABS.length;
    else if (event.key === 'ArrowLeft') next = (index - 1 + MANUSCRIPT_TABS.length) % MANUSCRIPT_TABS.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = MANUSCRIPT_TABS.length - 1;
    else return;
    event.preventDefault();
    const id = MANUSCRIPT_TABS[next][0]; void chooseTab(id); document.getElementById(`manuscript-tab-${id}`)?.focus();
  }
  async function loadHistory(reset = false) {
    const cursor = reset ? 0 : history.nextCursor;
    const request = beginRequest('history'), { controller, sequence } = request;
    const busyOwner = beginBusy('history', request);
    try { const data = await loadManuscriptHistory(projectId, selectedId, cursor, controller.signal); if (isLatest('history', sequence)) setHistory((old) => ({ items: reset ? (data.items || []) : [...old.items, ...(data.items || [])], nextCursor: data.next_cursor || 0, hasMore: Boolean(data.has_more) })); }
    catch (cause) { if (cause.name !== 'AbortError' && isLatest('history', sequence)) setError(cause.message); }
    finally { endBusy(busyOwner); }
  }
  async function openReview(audit) {
    if (reviewDetails[audit.revision_id]) return setReviewDetails((old) => { const next = { ...old }; delete next[audit.revision_id]; return next; });
    const request = beginRequest('review-detail'), { controller, sequence } = request;
    const busyOwner = beginBusy('review-detail', request);
    try { const data = await loadManuscriptReviewDetail(projectId, selectedId, audit.revision_id, audit.signature, controller.signal); if (isLatest('review-detail', sequence)) setReviewDetails((old) => ({ ...old, [audit.revision_id]: data.artifact })); }
    catch (cause) { if (cause.name !== 'AbortError' && isLatest('review-detail', sequence)) setError(cause.message); }
    finally { endBusy(busyOwner); }
  }
  async function loadMoreReview() {
    const currentReview = artifacts.review;
    const cursor = currentReview?.content?.next_cursor;
    if (cursor == null || !currentReview?.content?.has_more) return;
    const request = beginRequest('artifact:review-more'), { controller, sequence, epoch } = request;
    const busyOwner = beginBusy('artifact:review-more', request);
    try {
      const data = await loadManuscriptReviewPage(projectId, selectedId, cursor, controller.signal);
      if (!isLatest('artifact:review-more', sequence, epoch)) return;
      setArtifacts((old) => {
        const previous = old.review;
        if (!previous || previous.stable_id !== selectedId) return old;
        return { ...old, review: { ...data.artifact, content: { ...data.artifact.content, revisions: [...(previous.content?.revisions || []), ...(data.artifact.content?.revisions || [])], audits: [...(previous.content?.audits || []), ...(data.artifact.content?.audits || [])] } } };
      });
    } catch (cause) { if (cause.name !== 'AbortError' && isLatest('artifact:review-more', sequence, epoch)) setError(cause.message); }
    finally { endBusy(busyOwner); }
  }
  function recoverGoneVersion(cause, kind, sequence, epoch) {
    if (cause.status !== 410 && cause.data?.error?.code !== 'version_gone') return false;
    if (!isLatest(kind, sequence, epoch)) return true;
    requestRef.current['version-more']?.controller.abort();
    requestRef.current['restore-preview']?.controller.abort();
    requestRef.current['restore-confirm']?.controller.abort();
    setHistoryVersion(null);
    setRestorePreview(null);
    setHistoryRecovery(true);
    setError(cause.message);
    setNotice('历史版本已被清理；当前正式正文仍保留。请重新加载历史并选择可用版本。');
    return true;
  }
  async function openVersion(item) {
	requestRef.current['version-more']?.controller.abort();
    const request = beginRequest('version'), { controller, sequence, epoch } = request, busyOwner = beginBusy('version', request);
    try { const data = await loadManuscriptVersion(projectId, item.revision_id, selectedId, 0, controller.signal); if (isLatest('version', sequence, epoch)) { setHistoryVersion({ ...data.chapter, revision_id: item.revision_id }); setRestorePreview(null); setHistoryRecovery(false); } }
    catch (cause) { if (cause.name !== 'AbortError' && !recoverGoneVersion(cause, 'version', sequence, epoch) && isLatest('version', sequence, epoch)) setError(cause.message); }
    finally { endBusy(busyOwner); }
  }
	async function moreVersion() {
		const opened = historyVersion;
		if (!opened || opened.next_cursor == null) return;
		const { controller, sequence, epoch } = beginRequest('version-more');
		try {
			const data = await loadManuscriptVersion(projectId, opened.revision_id, selectedId, opened.next_cursor, controller.signal);
			const incoming = data.chapter;
			if (!isLatest('version-more', sequence, epoch)) return;
			if (incoming.version_id !== opened.revision_id || incoming.stable_id !== opened.stable_id || incoming.content_signature !== opened.content_signature) {
				throw new Error('历史版本分页签名发生变化，请重新打开该版本');
			}
			setHistoryVersion((previous) => previous?.revision_id === opened.revision_id ? mergeParagraphChunk(previous, incoming) : previous);
		} catch (cause) {
			if (cause.name !== 'AbortError' && !recoverGoneVersion(cause, 'version-more', sequence, epoch) && isLatest('version-more', sequence, epoch)) setError(cause.message);
		}
	}
  async function previewRestore(item) {
    if (!historyVersion || historyVersion.revision_id !== item.revision_id) return;
    const key = restoreKeys.current.get(item.revision_id) || newKey(); restoreKeys.current.set(item.revision_id, key);
    const request = beginRequest('restore-preview'), { controller, sequence, epoch } = request, busyOwner = beginBusy('restore-preview', request); setError('');
    try { const data = await previewManuscriptRestore(projectId, { revision_id: item.revision_id, chapter_id: selectedId, expected_content_signature: historyVersion.content_signature, idempotency_key: key }, controller.signal); if (isLatest('restore-preview', sequence, epoch)) setRestorePreview(data.preview); }
    catch (cause) { if (cause.name !== 'AbortError' && isLatest('restore-preview', sequence, epoch)) { setRestorePreview(null); setError(cause.message); setNotice(cause.data?.error?.action === 'refresh_preview' ? '版本已变化，请重新打开历史正文后再预览。' : '恢复当前被阻断；请按错误提示处理后重试。'); } }
    finally { endBusy(busyOwner); }
  }
  async function confirmRestore(item) {
    if (!restorePreview || restorePreview.source_revision_id !== item.revision_id) return;
    const request = beginRequest('restore-confirm'), { controller, sequence, epoch } = request, busyOwner = beginBusy('restore-confirm', request); setError('');
    try { await restoreManuscriptVersion(projectId, { revision_id: item.revision_id, chapter_id: selectedId, expected_content_signature: historyVersion.content_signature, idempotency_key: restoreKeys.current.get(item.revision_id), preview_signature: restorePreview.preview_signature }, controller.signal); if (!isLatest('restore-confirm', sequence, epoch)) return; setRestorePreview(null); setNotice('已从历史版本创建新的修订；当前正式稿未被覆盖，仍需独立审核与确认。'); await refreshVisible(); }
    catch (cause) { if (cause.name !== 'AbortError' && isLatest('restore-confirm', sequence, epoch)) { setRestorePreview(null); setError(cause.message); setNotice('恢复条件已变化，请重新预览后再确认。'); } }
    finally { endBusy(busyOwner); }
  }
  async function retryRecovery() {
    if (!projectId) return;
    const request = beginRequest('recovery'), { controller, sequence } = request;
    const busyOwner = beginBusy('recovery', request);
    try {
      const data = await retryManuscriptRecovery(projectId, controller.signal);
      if (!isLatest('recovery', sequence)) return;
      setRecovery(data?.recovery || null);
      setNotice(data?.recovered ? '正式稿件恢复已完成，可以重新恢复创作。' : '恢复仍未完成，系统继续保持只读保护。');
    } catch (cause) {
      if (cause.name !== 'AbortError' && isLatest('recovery', sequence)) setError(cause.message);
    } finally { endBusy(busyOwner); }
  }
  const currentTabLabel = MANUSCRIPT_TABS.find(([id]) => id === tab)?.[1] || '正文';
  const loadedParagraphs = current?.paragraphs?.length || 0;
  const totalParagraphs = current?.total_paragraphs || loadedParagraphs;
  async function finishManualEdit() {
    setManualEditing(false);
    setNotice('手动修改已保存到正式稿，并已保留修改前的历史版本。');
    await refreshVisible();
    setProseMode('current');
  }
  const controls = <div className="manuscript-controls" aria-label="专业稿件控制面板">
    <header className="manuscript-controls-header">
      <div><span className="eyebrow">专业稿件</span><h3>{node?.display_label || '选择章节'}</h3></div>
      <button type="button" className="tool-button" disabled={!projectId || busy} onClick={() => void refreshVisible()}>刷新</button>
    </header>
    {recovery?.required ? <section className="manuscript-recovery-card" role="alert">
      <strong>正式稿件处于只读保护</strong>
      <p>{(recovery.owners || ['unknown']).map((owner) => RECOVERY_OWNER_LABELS[owner] || owner).join('、')}尚未完成，系统不会绕过安全恢复。</p>
      <button type="button" className="tool-button" disabled={busy || recovery.retryable === false} onClick={() => void retryRecovery()}>重新检查并恢复</button>
    </section> : null}
    <section className="manuscript-control-card">
      <div className="manuscript-control-heading"><strong>章节选择</strong><span>{chapters.length ? `${selectedIndex + 1} / ${chapters.length}` : '暂无章节'}</span></div>
      <div className="manuscript-chapter-navigation">
        <button type="button" className="tool-button" disabled={selectedIndex <= 0 || busy} onClick={() => void selectChapter(chapters[selectedIndex - 1]?.stable_id)}>上一章</button>
        <button type="button" className="tool-button" disabled={selectedIndex < 0 || selectedIndex >= chapters.length - 1 || busy} onClick={() => void selectChapter(chapters[selectedIndex + 1]?.stable_id)}>下一章</button>
      </div>
      <ChapterCombobox chapters={chapters} selectedId={selectedId} disabled={busy} onSelect={(stableId) => void selectChapter(stableId)} />
      <button ref={treeButtonRef} className="manuscript-tree-open" type="button" tabIndex={drawerOpen ? -1 : 0} aria-haspopup="dialog" aria-expanded={drawerOpen} onClick={() => setDrawerOpen(true)}>打开完整目录</button>
      <div ref={drawerRef} className={drawerOpen ? 'manuscript-tree-drawer open' : 'manuscript-tree-drawer'} role="dialog" aria-modal={drawerOpen ? 'true' : undefined} aria-label="稿件目录抽屉">
        <ManuscriptTree nodes={tree} selectedId={selectedId} onSelect={selectChapter} onExpandBetween={(stableId) => setExpansionLaunch({ location: 'after', referenceIds: [stableId], nonce: Date.now() })} onClose={() => { setDrawerOpen(false); queueMicrotask(() => treeButtonRef.current?.focus()); }} />
      </div>
    </section>
    <section className="manuscript-control-card">
      <strong>查看内容</strong>
      <div className="manuscript-view-picker" role="tablist" aria-label="稿件视图">{MANUSCRIPT_TABS.map(([id, label], index) => <button id={`manuscript-tab-${id}`} key={id} role="tab" aria-selected={tab === id} aria-controls={`manuscript-panel-${id}`} tabIndex={tab === id ? 0 : -1} onKeyDown={(event) => tabKeyDown(event, index)} onClick={() => void chooseTab(id)}>{label}</button>)}</div>
      {tab === 'prose' ? <div className="manuscript-prose-picker" aria-label="正文版本">
        <button type="button" aria-pressed={proseMode === 'current'} onClick={() => { setManualEditing(false); setProseMode('current'); }}>正式稿</button>
        <button type="button" disabled={!candidate} aria-pressed={proseMode === 'candidate'} onClick={() => { setManualEditing(false); setProseMode('candidate'); }}>候选稿</button>
        <button type="button" disabled={!candidate} aria-pressed={proseMode === 'compare'} onClick={() => { setManualEditing(false); setProseMode('compare'); }}>正文对比</button>
      </div> : null}
    </section>
    <section className="manuscript-control-card">
      <strong>正文操作</strong>
      <ManuscriptActionPanel projectId={projectId} selectedId={selectedId} current={current} phase={expansionMeta.phase} mode={expansionMeta.mode} structureRevision={expansionMeta.structureRevision} structureSignature={expansionMeta.structureSignature} launchRequest={expansionLaunch} activeRevision={activeRevision} onReturnChapter={(stableId) => void selectChapter(stableId)} onChanged={() => void refreshVisible()} onManualEdit={() => { void chooseTab('prose'); setProseMode('current'); setManualEditing(true); queueMicrotask(() => contentScrollRef.current?.scrollTo({ top: 0 })); }} manualEditing={manualEditing} />
    </section>
  </div>;

  return <section className="manuscript-workspace-shell" aria-hidden={!active} hidden={!active}>
    {controlsTarget ? createPortal(controls, controlsTarget) : null}
    <header className="manuscript-reader-toolbar">
      <div><span className="eyebrow">{currentTabLabel}</span><h2>{node ? `第 ${node.display_order} 章 · ${node.display_label}` : '专业长篇稿件'}</h2></div>
      <div className="manuscript-reader-meta"><RevisionStatus node={node} />{tab === 'prose' ? <span>{autoLoading.current ? '正在载入完整正文 · ' : ''}已加载 {loadedParagraphs} / {totalParagraphs} 段</span> : null}</div>
      <div className="manuscript-reader-actions"><button type="button" className="tool-button" onClick={() => contentScrollRef.current?.scrollTo({ top: 0, behavior: 'smooth' })}>回到顶部</button><button type="button" className="tool-button" onClick={onReturnToWriting}>返回创作现场</button></div>
    </header>
    <main ref={contentScrollRef} className="manuscript-content-scroll" inert={drawerOpen || undefined} onScroll={(event) => scrollPositionsRef.current.set(`${projectId}:${selectedId}:${tab}:${proseMode}`, event.currentTarget.scrollTop)}>
      <div className="manuscript-reading-surface" role="tabpanel" id={`manuscript-panel-${tab}`} aria-labelledby={`manuscript-tab-${tab}`} tabIndex="0">
        {tab === 'prose' && proseMode === 'current' && manualEditing ? <ManuscriptManualEditor projectId={projectId} selectedId={selectedId} chapter={current} onCancel={() => setManualEditing(false)} onSaved={finishManualEdit} /> : null}
        {tab === 'prose' && proseMode === 'current' && !manualEditing ? <section className="manuscript-prose-section"><h3>当前正式稿</h3><ManuscriptReader chapter={current} busy={autoLoading.current} error={error} onMore={() => more('current')} onRetry={() => selectChapter(selectedId)} onPreviousChapter={previousChapter ? () => selectChapter(previousChapter.stable_id) : null} previousChapterLabel={previousChapter?.display_label || ''} onNextChapter={nextChapter ? () => selectChapter(nextChapter.stable_id) : null} nextChapterLabel={nextChapter?.display_label || ''} /></section> : null}
        {tab === 'prose' && proseMode === 'candidate' ? <section className="manuscript-candidate manuscript-prose-section"><h3>候选稿 <span>尚未发布</span></h3>{candidate ? <ManuscriptReader chapter={candidate} busy={autoLoading.candidate} error={error} onMore={() => more('candidate')} onRetry={() => selectChapter(selectedId)} /> : <p className="empty-state">当前章节没有候选稿。</p>}</section> : null}
        {tab === 'prose' && proseMode === 'compare' ? <RevisionCompare current={current} candidate={candidate} busy={busy} currentBusy={autoLoading.current} candidateBusy={autoLoading.candidate} error={error} onMoreCurrent={() => more('current')} onMoreCandidate={() => more('candidate')} onRetry={() => selectChapter(selectedId)} /> : null}
        {tab === 'outline' ? <ManuscriptOutlineView artifact={artifacts.outline} busy={artifactLoading.outline} error={artifactErrors.outline} onRetry={() => {
          setArtifactErrors((old) => ({ ...old, outline: '' }));
          void chooseTab('outline', true);
        }} /> : null}
        {tab === 'volume' ? <section className="manuscript-artifact-card" aria-busy={busy}><h3>所属分卷</h3>{artifacts.volume ? <><h4>{artifacts.volume.content?.title}</h4><p>{artifacts.volume.content?.theme}</p><ol>{(artifacts.volume.content?.arcs || []).map((arc) => <li key={arc.id}>{arc.title}：{arc.goal}</li>)}</ol><small>分卷内容已校验</small></> : <p>正在加载已校验的分卷视图…</p>}</section> : null}
        {tab === 'review' ? <ManuscriptReviewView artifact={artifacts.review} details={reviewDetails} busy={busy} onOpen={openReview} onMore={loadMoreReview} /> : null}
        {tab === 'history' ? <><RevisionHistory items={history.items} selected={historyVersion} preview={restorePreview} onOpen={openVersion} onPreview={previewRestore} onConfirm={confirmRestore} onMore={() => loadHistory(false)} hasMore={history.hasMore} loading={busy} />{historyRecovery ? <button type="button" disabled={busy} onClick={() => { setHistoryRecovery(false); void loadHistory(true); }}>重新加载历史</button> : null}{historyVersion ? <section className="manuscript-prose-section"><h3>历史版本正文</h3><ManuscriptReader chapter={historyVersion} busy={busy} error={error} onMore={moreVersion} onRetry={() => openVersion({ revision_id: historyVersion.revision_id })} /></section> : null}</> : null}
      </div>
    </main>
    {notice ? <div className="manuscript-notice" role="status" aria-live="polite">{notice}</div> : null}
  </section>;
}
