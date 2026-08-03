import { useEffect, useMemo, useState } from 'react';
import {
  commandManuscriptRevision,
  getManuscriptChapter,
  getManuscriptRevisionBatches,
  getManuscriptTree,
  previewManuscriptRevision
} from './api.js';

function flattenChapters(tree = []) {
  return tree.flatMap((volume) => (volume.arcs || []).flatMap((arc) => arc.chapters || []));
}

function idempotencyKey(action) {
  return `manuscript-ui:${action}:${globalThis.crypto?.randomUUID?.() || Date.now()}`;
}

export function isTerminalManuscriptRevision(value) {
  return value?.stage === 'completed' || value?.stage === 'cancelled';
}

export function resolveRefreshedManuscriptRevision(preferredRevision, activeRevision, details) {
  const active = isTerminalManuscriptRevision(preferredRevision) ? activeRevision : (preferredRevision || activeRevision);
  if (!active) return null;
  if (!details) return active;
  return { ...active, revision: details.revision, stage: details.stage, publication_status: details.publication_status, last_error_class: details.last_error_class, recovery_class: details.recovery_class, queue: details.queue, batches: details.batches };
}

export function ManuscriptRevisionWorkbench({ projectId, controlsOnly = false, selectedChapterId = '', hideLauncher = false, initialRevision = null }) {
  const [opened, setOpened] = useState(Boolean(hideLauncher || initialRevision));
  const [tree, setTree] = useState(null);
  const [stableId, setStableId] = useState('');
  const [chapter, setChapter] = useState(null);
  const [revision, setRevision] = useState(null);
  const [candidateViews, setCandidateViews] = useState([]);
  const [instruction, setInstruction] = useState('');
  const [kind, setKind] = useState('polish');
  const [status, setStatus] = useState('');
  const [error, setError] = useState('');
  const [errorClass, setErrorClass] = useState('');
  const [lastTerminalStage, setLastTerminalStage] = useState('');
  const chapters = useMemo(() => flattenChapters(tree?.tree), [tree]);

  useEffect(() => {
    setTree(null); setStableId(''); setChapter(null); setRevision(null); setCandidateViews([]); setError(''); setErrorClass(''); setLastTerminalStage('');
  }, [projectId]);

  useEffect(() => {
    if (!projectId || !hideLauncher) return;
    setOpened(true);
    if (initialRevision) setRevision(initialRevision);
    void refresh(initialRevision || revision);
  }, [projectId, hideLauncher, initialRevision?.revision_id]);

  async function refresh(preferredRevision = revision) {
    if (!projectId) return;
    const nextTree = await getManuscriptTree(projectId);
    setTree(nextTree);
    const nextStableId = selectedChapterId || stableId || flattenChapters(nextTree.tree)[0]?.id || '';
    setStableId(nextStableId);
    if (nextStableId) setChapter(await getManuscriptChapter(projectId, nextStableId));
    if (isTerminalManuscriptRevision(preferredRevision)) setLastTerminalStage(preferredRevision.stage);
    const active = resolveRefreshedManuscriptRevision(preferredRevision, nextTree.active_revision);
    setRevision(active || null);
    if (active?.revision_id) {
      const details = await getManuscriptRevisionBatches(projectId, active.revision_id);
      setRevision((current) => resolveRefreshedManuscriptRevision(current || active, active, details));
      setCandidateViews(details.candidate_views || []);
    } else {
      setCandidateViews([]);
    }
  }

  async function run(label, action) {
    setStatus(label); setError(''); setErrorClass('');
    try {
      const next = await action();
      await refresh(next?.revision || next?.preview?.runtime || next);
    } catch (cause) {
      setError(cause?.message || String(cause));
      setErrorClass(cause?.data?.error?.code || 'unknown_failure');
    } finally {
      setStatus('');
    }
  }

  async function selectChapter(nextStableId) {
    setStableId(nextStableId); setError(''); setErrorClass('');
    try { setChapter(await getManuscriptChapter(projectId, nextStableId)); } catch (cause) { setError(cause?.message || String(cause)); }
  }

  useEffect(() => {
    if (!selectedChapterId || selectedChapterId === stableId) return;
    setStableId(selectedChapterId);
    if (tree) void selectChapter(selectedChapterId);
  }, [selectedChapterId]);

  const command = (action, extra = {}) => commandManuscriptRevision(projectId, {
    action,
    revision_id: revision.revision_id,
    expected_revision: revision.revision,
    idempotency_key: idempotencyKey(action),
    ...extra
  });
  const selectedCandidate = candidateViews.find((view) => view.candidate?.chapter_id === stableId);
  const hasAdditionalUnconfirmed = (revision?.queue || []).some((item) => item.chapter_id !== revision?.baseline?.chapter_id && !item.impact_confirmed);

  return (
    <details className="manuscript-revision-workbench" open={opened} onToggle={(event) => {
      const next = event.currentTarget.open; setOpened(next);
      if (next && !tree) run('正在载入正文修订工作台', () => refresh());
    }}>
      <summary>安全正文修订与原子发布</summary>
      <div className="manuscript-revision-controls" aria-busy={Boolean(status)}>
        {!hideLauncher ? <label>章节
          <select aria-label="选择稳定章节" value={stableId} onChange={(event) => selectChapter(event.target.value)}>
            {chapters.map((item) => <option key={item.id} value={item.id}>第 {item.chapter} 章 · {item.title}</option>)}
          </select>
        </label> : null}
        {!hideLauncher ? <label>修订方式
          <select value={kind} onChange={(event) => setKind(event.target.value)}><option value="polish">润色</option><option value="rewrite">改写</option></select>
        </label> : null}
        {!hideLauncher ? <label className="manuscript-instruction">修订要求
          <textarea value={instruction} onChange={(event) => setInstruction(event.target.value)} />
        </label> : null}
        {!hideLauncher ? <button disabled={!stableId || !instruction.trim() || Boolean(revision)} onClick={() => run('正在生成预览', () => previewManuscriptRevision(projectId, { chapter_id: stableId, instruction: instruction.trim(), kind, idempotency_key: idempotencyKey('preview') }))} type="button">生成签名预览</button> : null}
        {hasAdditionalUnconfirmed ? <button onClick={() => run('正在确认关联章节', () => command('confirm_impacts'))} type="button">确认关联稳定 ID</button> : null}
        <button disabled={!revision || !['approval_pending', 'candidate_generating', 'failed'].includes(revision.stage) || hasAdditionalUnconfirmed} onClick={() => run('正在生成候选正文', () => command('generate', { expected_attempt: ((revision.queue || []).find((item) => ['pending', 'failed'].includes(item.status))?.attempt || 0) + 1 }))} type="button">生成候选</button>
        <button disabled={revision?.stage !== 'audit_pending'} onClick={() => run('正在独立审核', () => command('audit'))} type="button">独立审核</button>
        <button disabled={revision?.stage !== 'final_approval_pending'} onClick={() => run('正在批准', () => command('approve'))} type="button">批准</button>
        <button disabled={revision?.stage !== 'ready_to_publish'} onClick={() => run('正在原子发布', () => command('publish'))} type="button">原子发布</button>
        <button disabled={!revision || revision.publication_status !== 'none'} onClick={() => run('正在取消', () => command('cancel'))} type="button">取消</button>
        <button onClick={() => run('正在刷新恢复状态', () => refresh())} type="button">刷新/恢复</button>
      </div>
      <div aria-live="polite" role="status">{status || (revision ? `阶段：${revision.stage}；发布：${revision.publication_status}` : (lastTerminalStage ? `上一修订已${lastTerminalStage}，可开始新修订` : '尚无活动修订'))}</div>
      {revision?.recovery_class ? <div className="error-banner" role="alert"><strong>恢复类别：{revision.recovery_class}</strong></div> : null}
      {error ? <div className="error-banner" role="alert"><strong>恢复类别：{errorClass}</strong><div>{error}</div></div> : null}
      {!controlsOnly ? <><div className="manuscript-current-candidate">
        <article><h3>当前正式正文</h3><pre>{chapter?.chapter?.content || '请选择可读章节'}</pre></article>
        <article><h3>候选正文</h3><pre>{selectedCandidate?.content || '尚未生成候选正文'}</pre></article>
      </div>
      <section aria-label="签名审核结果">
        <h3>审核发现与恢复信息</h3>
        {selectedCandidate?.audit?.findings?.length ? <ul>{selectedCandidate.audit.findings.map((finding) => <li key={finding}>{finding}</li>)}</ul> : <p>尚无可显示的签名审核发现</p>}
      </section></> : null}
    </details>
  );
}
