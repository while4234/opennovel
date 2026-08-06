import { BookMarked, RefreshCw, UserRound } from 'lucide-react';
import { useCallback, useState } from 'react';
import { FoundationCenter } from '../foundation/FoundationCenter.jsx';
import { useNavigationGuard } from '../navigation/NavigationGuard.jsx';
import { ProjectScopeSelector, useIndependentProjectScope } from './ProjectScopeSelector.jsx';
import './knowledge.css';

const PAGE_COPY = {
  characters: {
    eyebrow: '知识资产',
    title: '角色卡',
    description: '维护角色身份、动机、关系证据和 Character Agent 审核结果。',
    icon: UserRound,
    initialTab: 'characters'
  },
  worldbook: {
    eyebrow: '知识资产',
    title: '世界书',
    description: '统一查看故事前提、计划关系、世界规则以及修订影响。',
    icon: BookMarked,
    initialTab: 'rules'
  }
};

export function KnowledgeFoundationPage({
  kind,
  onRefreshProjects,
  projects = [],
  projectsError = '',
  projectsLoading = false
}) {
  const copy = PAGE_COPY[kind] || PAGE_COPY.characters;
  const Icon = copy.icon;
  const [projectId, setProjectId] = useIndependentProjectScope(projects);
  const [dirty, setDirty] = useState(false);
  const confirmLeave = useCallback(
    () => globalThis.confirm?.('当前 Foundation 草稿尚未发布，确定离开吗？') ?? true,
    []
  );
  useNavigationGuard(dirty, confirmLeave);

  const selectProject = (nextProjectId) => {
    if (dirty && !confirmLeave()) return;
    setDirty(false);
    setProjectId(nextProjectId);
  };

  return (
    <section className={`knowledge-page knowledge-${kind}`} data-testid={`${kind}-page`}>
      <header className="knowledge-page-header">
        <div className="knowledge-page-title">
          <span className="knowledge-page-icon"><Icon size={20} /></span>
          <div><span className="page-eyebrow">{copy.eyebrow}</span><h1>{copy.title}</h1><p>{copy.description}</p></div>
        </div>
        <div className="knowledge-page-scope">
          <ProjectScopeSelector disabled={projectsLoading} onChange={selectProject} projects={projects} value={projectId} />
          <button aria-label="刷新项目列表" className="shell-icon-button bordered" disabled={projectsLoading} onClick={() => void onRefreshProjects?.().catch(() => {})} title="刷新项目列表" type="button"><RefreshCw className={projectsLoading ? 'is-spinning' : ''} size={17} /></button>
        </div>
      </header>

      {projectsError ? <div className="shell-error" role="alert">项目列表加载失败：{projectsError}</div> : null}
      {!projectId ? (
        <div className="knowledge-empty" role={projectsError ? undefined : 'status'}>
          <Icon size={30} />
          <strong>{projectsLoading ? '正在加载项目…' : '请选择一个项目'}</strong>
          <span>{projectsLoading ? '项目列表就绪后可查看知识资产。' : '知识页使用独立项目作用域，不会切换当前创作工作台。'}</span>
        </div>
      ) : (
        <div className="knowledge-foundation-surface" key={`${kind}:${projectId}`}>
          <FoundationCenter initialTab={copy.initialTab} onDirtyChange={setDirty} projectId={projectId} />
        </div>
      )}
    </section>
  );
}
