import { useEffect, useMemo, useRef, useState } from 'react';
import { ManuscriptRevisionWorkbench } from '../ManuscriptRevisionWorkbench.jsx';
import { ExpansionForm } from './ExpansionForm.jsx';
import { ExpansionLauncher } from './ExpansionLauncher.jsx';
import {
  cancelManuscriptActionDialogue,
  createManuscriptActionDialogue,
  executeManuscriptActionDialogue,
  loadActiveManuscriptActionDialogue,
  replyManuscriptActionDialogue,
} from './manuscript-api.js';

const actionKey = (action) => `manuscript-action:${action}:${globalThis.crypto?.randomUUID?.() || Date.now()}`;
const terminal = (status) => ['completed', 'cancelled', 'failed'].includes(status);

export function dialogueBelongsToChapter(dialogue, selectedId) {
  return !dialogue || dialogue.chapter_id === selectedId;
}

export function ManuscriptActionPanel({
  projectId,
  selectedId,
  current,
  phase,
  mode,
  structureRevision,
  structureSignature,
  launchRequest,
  activeRevision,
  onReturnChapter,
  onChanged,
  onManualEdit,
  manualEditing = false,
}) {
  const [type, setType] = useState('polish');
  const [instruction, setInstruction] = useState('');
  const [expansion, setExpansion] = useState({ location: phase === 'complete' ? 'book_end' : 'inside', sentence: '', adjustment: 'default', referenceIds: selectedId ? [selectedId] : [] });
  const [dialogue, setDialogue] = useState(null);
  const [answers, setAnswers] = useState({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const controller = useRef(null);
  const operationKeys = useRef(new Map());
  const result = useMemo(() => {
    if (!dialogue?.result) return null;
    if (typeof dialogue.result === 'string') {
      try { return JSON.parse(dialogue.result); } catch { return null; }
    }
    return dialogue.result;
  }, [dialogue]);

  useEffect(() => () => controller.current?.abort(), []);
  useEffect(() => {
    if (!projectId) return undefined;
    const activeController = new AbortController();
    operationKeys.current.clear(); setDialogue(null); setAnswers({}); setError('');
    Promise.resolve(loadActiveManuscriptActionDialogue(projectId, activeController.signal))
      .then((data) => setDialogue(data?.dialogue || null))
      .catch((cause) => { if (cause.name !== 'AbortError') setError(`稿件操作恢复失败：${cause.message}`); });
    return () => activeController.abort();
  }, [projectId]);
  useEffect(() => {
    if (!launchRequest) return;
    setType('expand');
    setExpansion((old) => ({ ...old, location: launchRequest.location || old.location, referenceIds: launchRequest.referenceIds || (selectedId ? [selectedId] : []) }));
  }, [launchRequest]);
  useEffect(() => {
    if (!dialogue || terminal(dialogue.status)) setExpansion((old) => ({ ...old, referenceIds: selectedId ? [selectedId] : [] }));
  }, [selectedId]);
  async function run(task) {
    controller.current?.abort(); controller.current = new AbortController(); setBusy(true); setError('');
    try {
      const data = await task(controller.current.signal);
      if (data?.dialogue) setDialogue(data.dialogue);
      return data;
    } catch (cause) {
      if (cause.name !== 'AbortError') setError(cause.message);
      return null;
    } finally { setBusy(false); }
  }

  function acquireOperationKey(action, fingerprint) {
    const identity = `${action}:${fingerprint}`;
    if (!operationKeys.current.has(identity)) operationKeys.current.set(identity, actionKey(action));
    return { identity, key: operationKeys.current.get(identity) };
  }

  function completeOperation(operation) {
    operationKeys.current.delete(operation.identity);
  }

  async function begin() {
    const initialInput = type === 'expand' ? expansion.sentence.trim() : instruction.trim();
    const payload = {
      chapter_id: selectedId,
      content_signature: current?.content_signature || '',
      type,
      initial_input: initialInput,
      structure_revision: structureRevision,
      structure_signature: structureSignature,
    };
    if (type === 'expand') payload.expansion = {
      location: expansion.location,
      reference_ids: expansion.referenceIds,
      adjustment: expansion.adjustment,
      expected_structure_revision: structureRevision,
      expected_structure_signature: structureSignature,
    };
    const operation = acquireOperationKey('create', JSON.stringify(payload));
    const data = await run((signal) => createManuscriptActionDialogue(projectId, { ...payload, idempotency_key: operation.key }, signal));
    if (data) completeOperation(operation);
  }

  async function reply(question) {
    const answer = answers[question.id]?.trim();
    if (!answer) return;
    const payload = {
      question_id: question.id,
      answer,
      expected_version: dialogue.version,
    };
    const operation = acquireOperationKey('reply', JSON.stringify([dialogue.id, payload]));
    const data = await run((signal) => replyManuscriptActionDialogue(projectId, dialogue.id, { ...payload, idempotency_key: operation.key }, signal));
    if (data) { completeOperation(operation); setAnswers((old) => { const next = { ...old }; delete next[question.id]; return next; }); }
  }

  async function execute() {
    const payload = { expected_version: dialogue.version };
    const operation = acquireOperationKey('execute', JSON.stringify([dialogue.id, payload]));
    const data = await run((signal) => executeManuscriptActionDialogue(projectId, dialogue.id, { ...payload, idempotency_key: operation.key }, signal));
    if (data) { completeOperation(operation); onChanged?.(); }
  }

  async function cancel() {
    const payload = { expected_version: dialogue.version };
    const operation = acquireOperationKey('cancel', JSON.stringify([dialogue.id, payload]));
    const data = await run((signal) => cancelManuscriptActionDialogue(projectId, dialogue.id, { ...payload, idempotency_key: operation.key }, signal));
    if (data) { completeOperation(operation); setAnswers({}); }
  }

  const boundElsewhere = dialogue && !terminal(dialogue.status) && !dialogueBelongsToChapter(dialogue, selectedId);
  const canBegin = selectedId && current?.content_signature && !busy && (!dialogue || terminal(dialogue.status));
  const actionInProgress = dialogue && !terminal(dialogue.status);

  return <div className="manuscript-action-panel" aria-busy={busy}>
    {boundElsewhere ? <div className="manuscript-action-origin" role="status">
      <strong>操作仍绑定在{dialogue.original_chapter_label || '原章节'}</strong>
      <p>切换章节不会迁移这次对话或正文签名。</p>
      <div><button type="button" onClick={() => onReturnChapter?.(dialogue.chapter_id)}>返回原章节</button><button type="button" disabled={busy} onClick={cancel}>取消操作</button></div>
    </div> : null}

    {!boundElsewhere && canBegin ? <section className="manuscript-action-compose" aria-label="修改、补剧情与扩写">
      <div className="manuscript-action-tabs" role="tablist" aria-label="稿件操作类型">
        {[['polish', '润色'], ['rewrite', '改写'], ['expand', '补剧情 / 扩写'], ['manual', '手动编辑']].map(([value, label]) => <button key={value} type="button" role="tab" aria-selected={type === value} onClick={() => setType(value)}>{label}</button>)}
      </div>
      {type === 'manual'
        ? <div className="manuscript-action-form manuscript-manual-launcher">
          <p>手动编辑会在中间正文区打开完整章节，右侧不再放置小型编辑框。</p>
          <button type="button" disabled={busy || manualEditing || !current?.content_signature} onClick={onManualEdit}>
            {manualEditing ? '正在编辑正文' : '在正文区开始编辑'}
          </button>
        </div>
        : type === 'expand'
        ? <ExpansionForm value={expansion} onChange={setExpansion} onSubmit={begin} busy={busy} mode={mode} maxLength={1000} submitLabel="提交意见" busyLabel="正在判断是否需要澄清…" />
        : <form className="manuscript-action-form" onSubmit={(event) => { event.preventDefault(); void begin(); }}><label>修改意见<textarea required maxLength="1000" rows="4" value={instruction} onChange={(event) => setInstruction(event.target.value)} placeholder={type === 'polish' ? '例如：保留叙事信息，压缩重复表达，让节奏更利落' : '例如：重写冲突段落，让角色的选择更符合前文动机'} /></label><button type="submit" disabled={busy || !instruction.trim()}>{busy ? '正在判断是否需要澄清…' : '提交意见'}</button></form>}
      <p className="manuscript-action-safety">当前章节是唯一目标；AI 仅在会改变人物、剧情、结构或范围时追问。</p>
    </section> : null}

    {!boundElsewhere && actionInProgress ? <section className="manuscript-action-dialogue" aria-live="polite">
      <header><strong>{dialogue.type === 'polish' ? '润色' : dialogue.type === 'rewrite' ? '改写' : '补剧情 / 扩写'} · {dialogue.original_chapter_label}</strong><span>第 {dialogue.round} / 6 轮</span></header>
      {(dialogue.messages || []).map((message, index) => <p key={`${message.role}-${message.question_id || index}`} className={`manuscript-action-message ${message.role}`}><b>{message.role === 'assistant' ? 'AI' : '你'}：</b>{message.content}</p>)}
      {dialogue.status === 'needs_input' ? (dialogue.questions || []).map((question) => <form key={question.id} onSubmit={(event) => { event.preventDefault(); void reply(question); }}>
        <label>{question.prompt}<textarea maxLength="500" rows="3" value={answers[question.id] || ''} onChange={(event) => setAnswers((old) => ({ ...old, [question.id]: event.target.value }))} /></label>
        <button type="submit" disabled={busy || !answers[question.id]?.trim()}>回答</button>
      </form>) : null}
      {dialogue.status === 'ready' ? <div className="manuscript-action-ready"><strong>意见已明确，可以生成安全预览</strong><p>{dialogue.resolved_instruction}</p><button type="button" disabled={busy} onClick={execute}>{busy ? '执行中…' : '生成签名预览'}</button></div> : null}
      <button type="button" className="secondary" disabled={busy || dialogue.status === 'executing'} onClick={cancel}>取消本次操作</button>
    </section> : null}

    {error ? <div className="error-banner" role="alert">{error}<button type="button" onClick={() => globalThis.location?.reload?.()}>刷新恢复</button></div> : null}

    {result?.kind === 'revision' || activeRevision ? <ManuscriptRevisionWorkbench projectId={projectId} selectedChapterId={dialogue?.chapter_id || selectedId} controlsOnly hideLauncher initialRevision={result?.kind === 'revision' ? result.preview?.runtime : null} /> : null}
    {result?.kind === 'expansion' ? <ExpansionLauncher projectId={projectId} phase={phase} mode={mode} structureRevision={structureRevision} structureSignature={structureSignature} selectedId={dialogue?.chapter_id || selectedId} activeRevision={null} onConfirmed={onChanged} hideLauncher initialPreview={result.preview} initialInstruction={dialogue?.resolved_instruction || ''} /> : null}
  </div>;
}
