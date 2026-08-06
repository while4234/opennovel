import { BookOpen, Download, FileJson, RefreshCw, Search, Upload } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createScopeRequestGate, ProjectScopeSelector, useIndependentProjectScope } from './ProjectScopeSelector.jsx';
import { knowledgeAPI, libraryItemMeta, libraryItemName, libraryItems } from './knowledgeApi.js';
import './knowledge.css';

const COPY = {
  novels: {
    title: '小说仓库',
    description: '检索已分析小说，或将来源文件导入指定项目的改编工作区。',
    icon: BookOpen,
    accept: '.txt,.md,.markdown,text/plain,text/markdown',
    empty: '仓库中还没有小说'
  },
  profiles: {
    title: '仿写画像库',
    description: '管理可移植画像，并将画像加载或导入到指定项目。',
    icon: FileJson,
    accept: '.json,application/json',
    empty: '画像库中还没有条目'
  }
};

export function LibraryCenter({
  api = knowledgeAPI,
  kind,
  onRefreshProjects,
  projects = [],
  projectsError = '',
  projectsLoading = false
}) {
  const copy = COPY[kind] || COPY.novels;
  const Icon = copy.icon;
  const [projectId, setProjectId] = useIndependentProjectScope(projects);
  const [query, setQuery] = useState('');
  const [items, setItems] = useState([]);
  const [state, setState] = useState({ status: 'idle', action: '', error: '', message: '' });
  const [profileName, setProfileName] = useState('');
  const gateRef = useRef(createScopeRequestGate());

  const list = useCallback(async (search = query) => {
    const requestVersion = gateRef.current.begin();
    setState((current) => ({ ...current, status: 'loading', error: '', message: '' }));
    try {
      const response = kind === 'profiles' ? await api.listProfiles(search) : await api.listNovels(search);
      if (!gateRef.current.isCurrent(requestVersion)) return;
      setItems(libraryItems(response));
      setState({ status: 'done', action: '', error: '', message: response?.message || '' });
    } catch (error) {
      if (!gateRef.current.isCurrent(requestVersion)) return;
      setState({ status: 'error', action: '', error: error.message, message: '' });
    }
  }, [api, kind, query]);

  useEffect(() => {
    void list('');
    return () => gateRef.current.invalidate();
  }, [kind]);

  const runAction = async (action, operation, success) => {
    const requestVersion = gateRef.current.begin();
    setState({ status: 'acting', action, error: '', message: '' });
    try {
      const response = await operation();
      if (!gateRef.current.isCurrent(requestVersion)) return;
      setState({ status: 'done', action: '', error: '', message: response?.message || success });
      return response;
    } catch (error) {
      if (!gateRef.current.isCurrent(requestVersion)) return;
      setState({ status: 'error', action: '', error: error.message, message: '' });
      return null;
    }
  };

  const load = (entry) => {
    const name = libraryItemName(entry);
    if (!projectId || !name) return;
    const operation = kind === 'profiles'
      ? () => api.loadProfile(projectId, name)
      : () => api.loadNovel(projectId, name);
    void runAction(`load:${name}`, operation, `已将“${name}”加载到所选项目`);
  };

  const upload = async (event) => {
    const files = Array.from(event.target.files || []);
    event.target.value = '';
    if (!files.length || (kind === 'novels' && !projectId)) return;
    const operation = kind === 'profiles'
      ? () => api.uploadProfiles(files)
      : () => api.uploadNovel(projectId, files[0]);
    const response = await runAction('upload', operation, kind === 'profiles' ? `已上传 ${files.length} 个画像` : `已上传 ${files[0].name} 到所选项目`);
    if (response && kind === 'profiles') await list(query);
  };

  const importFile = (event) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!file || !projectId) return;
    const operation = kind === 'profiles'
      ? () => api.importProfile(projectId, file)
      : () => api.importNovel(projectId, file, 'knowledge-library');
    void runAction('import', operation, `已将 ${file.name} 导入所选项目`);
  };

  const saveProfile = async (event) => {
    event.preventDefault();
    const name = profileName.trim();
    if (!projectId || !name) return;
    const response = await runAction('save', () => api.saveProfile(projectId, name), `已保存画像“${name}”`);
    if (response) {
      setProfileName('');
      await list(query);
    }
  };

  const busy = ['loading', 'acting'].includes(state.status);
  const visibleItems = useMemo(() => items, [items]);

  return (
    <section className="knowledge-page library-center" data-testid={`${kind}-library-page`}>
      <header className="knowledge-page-header">
        <div className="knowledge-page-title"><span className="knowledge-page-icon"><Icon size={20} /></span><div><span className="page-eyebrow">创作资产</span><h1>{copy.title}</h1><p>{copy.description}</p></div></div>
        <div className="knowledge-page-scope">
          <ProjectScopeSelector disabled={projectsLoading} onChange={(value) => { gateRef.current.invalidate(); setProjectId(value); }} projects={projects} value={projectId} />
          <button aria-label="刷新项目列表" className="shell-icon-button bordered" disabled={projectsLoading} onClick={() => void onRefreshProjects?.().catch(() => {})} title="刷新项目列表" type="button"><RefreshCw className={projectsLoading ? 'is-spinning' : ''} size={17} /></button>
        </div>
      </header>

      {projectsError ? <div className="shell-error" role="alert">项目列表加载失败：{projectsError}</div> : null}
      <div className="library-toolbar">
        <form className="library-page-search" onSubmit={(event) => { event.preventDefault(); void list(query); }}>
          <Search size={17} /><input aria-label={`搜索${copy.title}`} onChange={(event) => setQuery(event.target.value)} placeholder={`搜索${copy.title}…`} value={query} />
          <button className="shell-button" disabled={busy} type="submit"><RefreshCw size={16} />刷新</button>
        </form>
        <div className="library-toolbar-actions">
          <label className={`shell-button ${kind === 'novels' && !projectId ? 'disabled' : ''}`} title={kind === 'profiles' ? '上传可移植画像到全局库' : '上传来源文件到所选项目'}><Upload size={16} />{kind === 'profiles' ? '上传画像' : '上传来源'}<input accept={copy.accept} disabled={busy || (kind === 'novels' && !projectId)} multiple={kind === 'profiles'} onChange={upload} type="file" /></label>
          <label className={`shell-button ${!projectId ? 'disabled' : ''}`} title="导入到所选项目"><Download size={16} />导入项目<input accept={copy.accept} disabled={busy || !projectId} onChange={importFile} type="file" /></label>
        </div>
      </div>

      {kind === 'profiles' ? <form className="library-profile-save" onSubmit={saveProfile}><label><span>将所选项目的当前画像保存到库</span><input aria-label="画像名称" disabled={busy || !projectId} onChange={(event) => setProfileName(event.target.value)} placeholder="画像名称" value={profileName} /></label><button className="shell-button primary" disabled={busy || !projectId || !profileName.trim()} type="submit">保存画像</button></form> : null}
      {state.error ? <div className="shell-error" role="alert">{state.error}</div> : null}
      {state.message ? <div className="knowledge-success" role="status">{state.message}</div> : null}

      <div className="library-page-grid" aria-busy={busy}>
        {state.status === 'loading' && !visibleItems.length ? <div className="knowledge-empty" role="status"><RefreshCw className="is-spinning" size={24} /><strong>正在加载{copy.title}…</strong></div> : null}
        {state.status !== 'loading' && !visibleItems.length ? <div className="knowledge-empty"><Icon size={28} /><strong>{copy.empty}</strong><span>可使用上方真实上传或导入操作添加内容。</span></div> : null}
        {visibleItems.map((entry, index) => {
          const name = libraryItemName(entry);
          return <article className="library-page-card" key={`${name}:${index}`}><span className="library-card-cover"><Icon size={28} /></span><div><strong>{name || '未命名条目'}</strong><small>{libraryItemMeta(entry, kind)}</small><time>{formatLibraryDate(entry)}</time></div><button className="shell-button" disabled={busy || !projectId || !name} onClick={() => load(entry)} type="button">{state.action === `load:${name}` ? '加载中…' : '加载到项目'}</button></article>;
        })}
      </div>
    </section>
  );
}

function formatLibraryDate(entry) {
  const value = entry?.updated_at || entry?.UpdatedAt || entry?.created_at || entry?.CreatedAt;
  const timestamp = Date.parse(value || '');
  return Number.isFinite(timestamp) ? new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'short', day: 'numeric' }).format(timestamp) : '时间未知';
}
