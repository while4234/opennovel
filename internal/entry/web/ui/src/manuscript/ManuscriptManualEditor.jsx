import { useEffect, useRef, useState } from 'react';
import { saveManualManuscriptCandidate } from './manuscript-api.js';

const operationKey = () => `manual-candidate:${globalThis.crypto?.randomUUID?.() || Date.now()}`;

export function ManuscriptManualEditor({ projectId, selectedId, chapter, onCancel, onSaved }) {
  const [prose, setProse] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const controller = useRef(null);
  const saveKey = useRef(operationKey());
  const paragraphs = chapter?.paragraphs || [];
  const totalParagraphs = Number(chapter?.total_paragraphs) || paragraphs.length;
  const fullyLoaded = paragraphs.length >= totalParagraphs;

  useEffect(() => {
    setProse(paragraphs.join('\n\n'));
    setError('');
    saveKey.current = operationKey();
  }, [selectedId, chapter?.content_signature]);

  useEffect(() => () => controller.current?.abort(), []);

  async function save(event) {
    event.preventDefault();
    if (!fullyLoaded || !prose.trim() || busy) return;
    controller.current?.abort();
    controller.current = new AbortController();
    setBusy(true);
    setError('');
    try {
      const data = await saveManualManuscriptCandidate(projectId, selectedId, {
        content_signature: chapter?.content_signature || '',
        prose: prose.trim(),
        idempotency_key: saveKey.current,
      }, controller.current.signal);
      await onSaved?.(data?.revision || null);
    } catch (cause) {
      if (cause.name !== 'AbortError') setError(cause.message);
    } finally {
      setBusy(false);
    }
  }

  return <section className="manuscript-manual-workspace" aria-busy={busy}>
    <header className="manuscript-manual-toolbar">
      <div>
        <h3>手动编辑正文</h3>
        <p>保存后直接更新正式稿，并保留历史版本，方便随时回看或恢复。</p>
      </div>
      <div>
        <button type="button" className="tool-button" disabled={busy} onClick={onCancel}>取消</button>
        <button type="submit" form="manuscript-manual-form" disabled={busy || !fullyLoaded || !prose.trim()}>
          {busy ? '正在保存…' : '保存修改'}
        </button>
      </div>
    </header>
    {!fullyLoaded ? <div className="error-banner" role="alert">完整正文仍在加载，加载完成后才能编辑和保存。</div> : null}
    {error ? <div className="error-banner" role="alert">{error}</div> : null}
    <form id="manuscript-manual-form" onSubmit={save}>
      <label className="sr-only" htmlFor="manuscript-manual-prose">章节正文</label>
      <textarea
        id="manuscript-manual-prose"
        aria-label="章节正文"
        autoFocus
        disabled={!fullyLoaded || busy}
        value={prose}
        onChange={(event) => setProse(event.target.value)}
        spellCheck="true"
      />
    </form>
  </section>;
}
