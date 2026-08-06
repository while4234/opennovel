import { BookOpen, RefreshCw, SearchX } from 'lucide-react';

export function ProjectRouteRecovery({
  error = '',
  kind,
  loading = false,
  onBack,
  onRetry,
  onSelect,
  projectId = '',
  projects = []
}) {
  if (loading) {
    return (
      <section className="project-route-recovery" role="status">
        <RefreshCw className="is-spinning" size={28} />
        <strong>正在加载项目…</strong>
      </section>
    );
  }

  const selecting = kind === 'knowledge';
  return (
    <section className="project-route-recovery" aria-labelledby="project-route-recovery-title">
      {selecting ? <BookOpen size={34} /> : <SearchX size={34} />}
      <h1 id="project-route-recovery-title">
        {selecting ? '选择一个项目' : '项目不存在或无法加载'}
      </h1>
      <p>
        {selecting
          ? '角色卡和世界书属于具体小说项目。选择项目后将打开对应的设定页面。'
          : `没有找到项目“${projectId}”。它可能已被移入回收站，或项目列表暂时不可用。`}
      </p>
      {error ? <div className="shell-error" role="alert">{error}</div> : null}
      {projects.length ? (
        <div className="project-route-options" aria-label="选择项目">
          {projects.map((project) => (
            <button key={project.id} onClick={() => onSelect(project)} type="button">
              <BookOpen size={18} />
              <span><strong>{project.name || project.id}</strong><small>{project.id}</small></span>
            </button>
          ))}
        </div>
      ) : null}
      <div className="project-route-actions">
        {onRetry ? <button className="shell-button" onClick={() => void onRetry().catch(() => {})} type="button"><RefreshCw size={16} /> 重新加载</button> : null}
        <button className="shell-button primary" onClick={onBack} type="button">返回项目中心</button>
      </div>
    </section>
  );
}
