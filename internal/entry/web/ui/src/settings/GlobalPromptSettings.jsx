import { Check, RefreshCw, RotateCcw, Search } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { getGlobalPrompts, resetGlobalPrompt, updateGlobalPrompt } from '../api.js';
import { useNavigationGuard } from '../navigation/NavigationGuard.jsx';

export const MAX_GLOBAL_PROMPT_BYTES = 64 * 1024;

export function globalPromptValidation(content) {
  const source = String(content ?? '');
  const bytes = new TextEncoder().encode(source).byteLength;
  if (!source.trim()) return { valid: false, bytes, message: '提示词 trim 后不能为空' };
  if (source.includes('\0')) return { valid: false, bytes, message: '提示词不得包含 NUL 字符' };
  if (bytes > MAX_GLOBAL_PROMPT_BYTES) return { valid: false, bytes, message: `提示词不能超过 64 KiB（当前 ${bytes} 字节）` };
  return { valid: true, bytes, message: '' };
}

export function GlobalPromptSettings({ api = defaultPromptAPI, confirmAction = defaultConfirm }) {
  const [state, setState] = useState({ status: 'loading', prompts: [], error: '', message: '' });
  const [selectedFamily, setSelectedFamily] = useState('');
  const [draft, setDraft] = useState('');
  const [saved, setSaved] = useState('');
  const [query, setQuery] = useState('');
  const [saving, setSaving] = useState(false);
  const dirty = draft !== saved;
  const validation = globalPromptValidation(draft);
  const confirmUnsaved = useCallback(
    () => confirmAction('当前提示词尚未保存，确定离开吗？'),
    [confirmAction]
  );

  useNavigationGuard(dirty, confirmUnsaved);

  const load = async (preferredFamily = selectedFamily) => {
    setState((current) => ({ ...current, status: 'loading', error: '', message: '' }));
    try {
      const response = await api.get();
      const prompts = Array.isArray(response?.prompts) ? response.prompts : [];
      const family = prompts.some((item) => item.family === preferredFamily) ? preferredFamily : prompts[0]?.family || '';
      const selected = prompts.find((item) => item.family === family);
      setState({ status: 'done', prompts, error: '', message: '' });
      setSelectedFamily(family);
      setDraft(selected?.content || '');
      setSaved(selected?.content || '');
    } catch (error) {
      setState({ status: 'error', prompts: [], error: error.message, message: '' });
    }
  };

  useEffect(() => { void load(''); }, []);

  const visiblePrompts = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return state.prompts;
    return state.prompts.filter((item) => [item.family, item.label, ...(item.aliases || [])].join(' ').toLowerCase().includes(normalized));
  }, [query, state.prompts]);
  const selected = state.prompts.find((item) => item.family === selectedFamily);

  const selectFamily = (family) => {
    if (family === selectedFamily) return;
    if (dirty && !confirmAction('当前提示词尚未保存，确定切换模型族吗？')) return;
    const next = state.prompts.find((item) => item.family === family);
    setSelectedFamily(family);
    setDraft(next?.content || '');
    setSaved(next?.content || '');
    setState((current) => ({ ...current, error: '', message: '' }));
  };

  const save = async () => {
    if (!selected || !dirty || !validation.valid || saving) return;
    setSaving(true);
    setState((current) => ({ ...current, error: '', message: '' }));
    try {
      const response = await api.update(selected.family, draft);
      const prompts = Array.isArray(response?.prompts) ? response.prompts : state.prompts;
      const next = prompts.find((item) => item.family === selected.family);
      setState({ status: 'done', prompts, error: '', message: `${selected.label || selected.family} 提示词已保存` });
      setDraft(next?.content ?? draft);
      setSaved(next?.content ?? draft);
    } catch (error) {
      setState((current) => ({ ...current, error: error.message, message: '' }));
    } finally {
      setSaving(false);
    }
  };

  const reset = async () => {
    if (!selected || saving || !confirmAction(`恢复 ${selected.label || selected.family} 的内置提示词？`)) return;
    setSaving(true);
    setState((current) => ({ ...current, error: '', message: '' }));
    try {
      const response = await api.reset(selected.family);
      const prompts = Array.isArray(response?.prompts) ? response.prompts : [];
      const next = prompts.find((item) => item.family === selected.family);
      setState({ status: 'done', prompts, error: '', message: `${selected.label || selected.family} 已恢复内置提示词` });
      setDraft(next?.content || '');
      setSaved(next?.content || '');
    } catch (error) {
      setState((current) => ({ ...current, error: error.message, message: '' }));
    } finally {
      setSaving(false);
    }
  };

  if (state.status === 'loading' && state.prompts.length === 0) return <div className="settings-empty" role="status">正在加载六族提示词…</div>;
  if (state.status === 'error' && state.prompts.length === 0) return <div className="settings-empty"><div className="settings-message error" role="alert">{state.error}</div><button className="tool-button" onClick={() => load('')} type="button"><RefreshCw size={16} />重新加载</button></div>;

  return (
    <div className="prompt-settings">
      <aside className="prompt-family-panel">
        <label className="prompt-search"><Search size={16} /><input aria-label="搜索模型族" placeholder="搜索模型族…" value={query} onChange={(event) => setQuery(event.target.value)} /></label>
        <div className="prompt-family-list">{visiblePrompts.map((prompt) => <button aria-current={prompt.family === selectedFamily ? 'true' : undefined} className={prompt.family === selectedFamily ? 'active' : ''} key={prompt.family} onClick={() => selectFamily(prompt.family)} type="button"><span><strong>{prompt.label || prompt.family}</strong><small>{prompt.aliases?.join(' · ') || prompt.family}</small>{prompt.fallback ? <small className="prompt-fallback-label">未知模型默认</small> : null}</span><span className={`settings-badge ${prompt.overridden ? 'custom' : ''}`}>{prompt.overridden ? '自定义' : '内置'}</span></button>)}</div>
      </aside>
      <section className="prompt-editor-panel">
        {selected ? <>
          <header><div className="prompt-editor-title"><span className="settings-badge">模型规则</span>{selected.overridden ? <span className="settings-badge custom">自定义</span> : <span className="settings-badge">内置</span>}{selected.fallback ? <span className="settings-badge fallback">未知模型默认</span> : null}<h2>{selected.label || selected.family}</h2><p>匹配：{selected.aliases?.join('、') || selected.family}</p></div><button className="tool-button" disabled={saving || !selected.overridden} onClick={reset} type="button"><RotateCcw size={16} />恢复内置</button></header>
          <label className="prompt-editor"><span>提示词内容</span><textarea aria-label="提示词内容" spellCheck="false" value={draft} onChange={(event) => { setDraft(event.target.value); setState((current) => ({ ...current, error: '', message: '' })); }} /></label>
          <footer><div><span className={validation.valid ? '' : 'invalid'}>{validation.bytes.toLocaleString()} / {MAX_GLOBAL_PROMPT_BYTES.toLocaleString()} 字节</span>{dirty ? <strong>未保存</strong> : <span>已保存</span>}</div><button className="tool-button accent" disabled={saving || !dirty || !validation.valid} onClick={save} type="button"><Check size={16} />{saving ? '保存中…' : '保存'}</button></footer>
          {!validation.valid ? <div className="settings-message error" role="alert">{validation.message}</div> : null}
          {state.error ? <div className="settings-message error" role="alert">{state.error}</div> : null}
          {state.message ? <div className="settings-message success" role="status">{state.message}</div> : null}
        </> : <div className="settings-empty">请选择一个模型族</div>}
      </section>
    </div>
  );
}

const defaultPromptAPI = { get: getGlobalPrompts, update: updateGlobalPrompt, reset: resetGlobalPrompt };
function defaultConfirm(message) { return globalThis.confirm?.(message) ?? true; }
