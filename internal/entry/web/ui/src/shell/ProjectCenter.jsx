import {
  BookOpen,
  Check,
  Copy,
  Grid2X2,
  List,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Trash2,
  Undo2,
  X
} from 'lucide-react';
import { useMemo, useState } from 'react';

export function ProjectCenter({
  busy,
  error,
  onClone,
  onCreate,
  onEmptyTrash,
  onOpen,
  onRefresh,
  onRename,
  onRestore,
  onTrash,
  onTrashOpen,
  projectsError,
  projectsLoading,
  projects,
  trashError,
  trashLoading,
  trashProjects
}) {
  const [query, setQuery] = useState('');
  const [view, setView] = useState('grid');
  const [trashOpen, setTrashOpen] = useState(false);
  const [dialog, setDialog] = useState(null);
  const visibleProjects = useMemo(() => filterAndSortProjects(projects, query), [projects, query]);

  const openTrash = async () => {
    const next = !trashOpen;
    setTrashOpen(next);
    if (next) {
      try {
        await onTrashOpen();
      } catch {
        // The caller exposes the error in the trash panel.
      }
    }
  };

  const refreshProjects = () => {
    void onRefresh().catch(() => {});
  };

  const submitDialog = async (event) => {
    event.preventDefault();
    const name = String(dialog?.name || '').trim();
    if (!dialog || ((dialog.kind === 'create' || dialog.kind === 'rename' || dialog.kind === 'clone') && !name)) return;
    try {
      if (dialog.kind === 'create') await onCreate(name);
      if (dialog.kind === 'rename') await onRename(dialog.project, name);
      if (dialog.kind === 'clone') await onClone(dialog.project, name);
      if (dialog.kind === 'trash') await onTrash(dialog.project);
      setDialog(null);
    } catch {
      // The parent exposes the actionable API error while the dialog stays open.
    }
  };

  return (
    <section className="project-center" aria-labelledby="project-center-title">
      <header className="project-center-toolbar">
        <div>
          <span className="page-eyebrow">创作空间</span>
          <h1 id="project-center-title">项目</h1>
        </div>
        <div className="project-center-primary-actions">
          <button className="shell-button primary" onClick={() => setDialog({ kind: 'create', name: '' })} type="button">
            <Plus size={17} /> 新建项目
          </button>
          <button aria-label="刷新项目" className="shell-icon-button bordered" disabled={busy || projectsLoading} onClick={refreshProjects} title="刷新项目" type="button">
            <RefreshCw className={projectsLoading ? 'is-spinning' : ''} size={18} />
          </button>
        </div>
      </header>

      <div className="project-center-filters">
        <label className="project-search">
          <Search size={17} />
          <input aria-label="搜索项目" onChange={(event) => setQuery(event.target.value)} placeholder="搜索项目..." value={query} />
        </label>
        <span className="sort-label">最近编辑</span>
        <div className="view-switch" role="group" aria-label="项目视图">
          <button aria-label="网格视图" aria-pressed={view === 'grid'} onClick={() => setView('grid')} type="button"><Grid2X2 size={17} /></button>
          <button aria-label="列表视图" aria-pressed={view === 'list'} onClick={() => setView('list')} type="button"><List size={18} /></button>
        </div>
      </div>

      {error ? <div className="shell-error" role="alert">{error}</div> : null}
      {projectsError ? <div className="shell-error" role="alert">项目列表加载失败：{projectsError}</div> : null}
      {projectsLoading && projects.length === 0 ? <div className="project-center-loading" role="status"><RefreshCw className="is-spinning" size={20} /> 正在加载项目…</div> : projectsError && projects.length === 0 ? (
        <div className="project-center-loading">暂时无法显示项目，请使用刷新按钮重试。</div>
      ) : visibleProjects.length ? (
        <div className={`project-collection ${view}`}>
          {visibleProjects.map((project) => (
            <article className="project-card" key={project.id}>
              <button className="project-card-open" onClick={() => onOpen(project)} type="button">
                <span className="project-cover"><BookOpen size={38} /></span>
                <span className="project-card-copy">
                  <strong>{project.name || project.id}</strong>
                  <small>{projectSummary(project)}</small>
                  <time>{formatProjectDate(project)}</time>
                </span>
              </button>
              <div className="project-card-actions">
                <button aria-label={`重命名 ${project.name || project.id}`} onClick={() => setDialog({ kind: 'rename', project, name: project.name || project.id })} title="重命名" type="button"><Pencil size={16} /></button>
                <button aria-label={`克隆 ${project.name || project.id}`} onClick={() => setDialog({ kind: 'clone', project, name: `${project.name || project.id} - 副本` })} title="克隆" type="button"><Copy size={16} /></button>
                <button aria-label={`移入回收站 ${project.name || project.id}`} className="danger" onClick={() => setDialog({ kind: 'trash', project })} title="移入回收站" type="button"><Trash2 size={16} /></button>
              </div>
            </article>
          ))}
        </div>
      ) : (
        <div className="project-center-empty">
          <BookOpen size={34} />
          <strong>{query ? '没有匹配的项目' : '还没有小说项目'}</strong>
          <span>{query ? '调整搜索词后重试。' : '创建一个项目，开始整理设定和正文。'}</span>
          {!query ? <button className="shell-button" onClick={() => setDialog({ kind: 'create', name: '' })} type="button"><Plus size={17} /> 新建项目</button> : null}
        </div>
      )}

      <section className="project-trash">
        <button aria-expanded={trashOpen} className="project-trash-toggle" onClick={openTrash} type="button">
          <Trash2 size={17} /> 回收站 <span>{trashOpen ? '收起' : '查看'}</span>
        </button>
        {trashOpen ? (
          <div className="project-trash-content">
            {trashLoading ? <span className="project-trash-empty" role="status"><RefreshCw className="is-spinning" size={16} /> 正在加载回收站…</span> : null}
            {trashError ? <div className="shell-error" role="alert">回收站加载失败：{trashError}</div> : null}
            {!trashLoading && !trashError && trashProjects.length ? trashProjects.map((project) => (
              <div className="project-trash-row" key={project.id}>
                <span><strong>{project.name || project.id}</strong><small>{formatProjectDate(project)}</small></span>
                <button className="shell-button" disabled={busy} onClick={() => onRestore(project)} type="button"><Undo2 size={16} /> 恢复</button>
              </div>
            )) : !trashLoading && !trashError ? <span className="project-trash-empty">回收站为空</span> : null}
            <button className="shell-button danger" disabled={busy || trashLoading || Boolean(trashError) || trashProjects.length === 0} onClick={onEmptyTrash} type="button"><Trash2 size={16} /> 清空回收站</button>
          </div>
        ) : null}
      </section>

      {dialog ? (
        <div className="shell-dialog-backdrop" onMouseDown={() => setDialog(null)}>
          <form aria-label={dialogTitle(dialog.kind)} aria-modal="true" className="shell-dialog" onMouseDown={(event) => event.stopPropagation()} onSubmit={submitDialog} role="dialog">
            <header><strong>{dialogTitle(dialog.kind)}</strong><button aria-label="关闭" className="shell-icon-button" onClick={() => setDialog(null)} type="button"><X size={18} /></button></header>
            {dialog.kind === 'trash' ? <p>“{dialog.project.name || dialog.project.id}”将移入回收站，可稍后恢复。</p> : (
              <label><span>项目名称</span><input autoFocus disabled={busy} onChange={(event) => setDialog((current) => ({ ...current, name: event.target.value }))} value={dialog.name} /></label>
            )}
            <footer>
              <button className="shell-button" disabled={busy} onClick={() => setDialog(null)} type="button"><X size={16} /> 取消</button>
              <button className={`shell-button ${dialog.kind === 'trash' ? 'danger' : 'primary'}`} disabled={busy || (dialog.kind !== 'trash' && !String(dialog.name || '').trim())} type="submit"><Check size={16} /> {dialog.kind === 'trash' ? '移入回收站' : '确认'}</button>
            </footer>
          </form>
        </div>
      ) : null}
    </section>
  );
}

export function filterAndSortProjects(projects = [], query = '') {
  const normalizedQuery = String(query || '').trim().toLocaleLowerCase();
  return [...projects]
    .filter((project) => !normalizedQuery || String(project.name || project.id || '').toLocaleLowerCase().includes(normalizedQuery))
    .sort((left, right) => projectTimestamp(right) - projectTimestamp(left));
}

function projectTimestamp(project) {
  const value = project.last_accessed_at || project.updated_at || project.created_at || '';
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? timestamp : 0;
}

function formatProjectDate(project) {
  const timestamp = projectTimestamp(project);
  if (!timestamp) return '尚未编辑';
  return new Intl.DateTimeFormat('zh-CN', { month: 'short', day: 'numeric' }).format(timestamp);
}

function projectSummary(project) {
  const words = Number(project.word_count || project.words || 0);
  const chapters = Number(project.chapter_count || project.chapters || 0);
  return `${words.toLocaleString('zh-CN')} 字 · ${chapters.toLocaleString('zh-CN')} 章`;
}

function dialogTitle(kind) {
  return ({ create: '新建项目', rename: '重命名项目', clone: '克隆项目', trash: '移入回收站' })[kind] || '项目操作';
}
