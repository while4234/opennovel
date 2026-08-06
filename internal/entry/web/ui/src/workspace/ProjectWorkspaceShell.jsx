import {
  Activity,
  BrainCircuit,
  BookOpen,
  Database,
  Download,
  FileText,
  Gauge,
  PenLine,
  Settings,
  TestTube2,
  Upload,
  WandSparkles
} from 'lucide-react';
import { Link } from 'react-router-dom';
import { projectModelSettingsPath, projectWorkspacePath } from '../shell/route-config.js';
import { WORKSPACE_SECTIONS } from './route-mapping.js';

const SECTION_ICONS = {
  write: PenLine,
  manuscript: BookOpen,
  foundation: Database,
  simulation: WandSparkles,
  adaptation: FileText,
  continuation: Upload,
  audit: TestTube2,
  export: Download,
  diagnostics: Activity,
  settings: Settings
};

export function ProjectWorkspaceHeader({ actions, connection, project, section }) {
  const sectionDefinition = WORKSPACE_SECTIONS.find((item) => item.id === section) || WORKSPACE_SECTIONS[0];
  return (
    <div className="project-workspace-header">
      <header className="project-workspace-topbar">
        <div className="workspace-heading">
          <div className="eyebrow">当前项目 · {sectionDefinition.label}</div>
          <h2>{project?.name || project?.id || '正在载入项目'}</h2>
        </div>
        <div className="project-workspace-status" aria-label="项目运行概况">
          <span className="project-workspace-connection" data-status={connection} />
          {actions}
        </div>
      </header>
      {project?.id ? <ProjectSectionNavigation projectId={project.id} section={section} /> : null}
    </div>
  );
}

export function ProjectSectionNavigation({ projectId, section }) {
  return (
    <nav className="project-section-navigation" aria-label="项目工作区">
      {WORKSPACE_SECTIONS.map((item) => {
        const Icon = SECTION_ICONS[item.id] || Gauge;
        const active = item.id === section;
        return (
          <Link
            aria-current={active ? 'page' : undefined}
            className={active ? 'active' : ''}
            key={item.id}
            title={item.description}
            to={projectWorkspacePath(projectId, item.id)}
          >
            <Icon aria-hidden="true" size={15} />
            <span>{item.label}</span>
          </Link>
        );
      })}
    </nav>
  );
}

export function ProjectModelSettingsLink({ className = 'tool-button', iconSize = 16, onClick, projectId }) {
  if (!projectId) return null;
  return (
    <Link aria-label="配置本项目模型" className={className} onClick={onClick} to={projectModelSettingsPath(projectId)}>
      <BrainCircuit aria-hidden="true" size={iconSize} />
      <span>本项目模型</span>
    </Link>
  );
}

export function ProjectActionRailHeader({ children, title = '项目操作' }) {
  return (
    <header className="project-action-rail-header">
      <div>
        <span>工作区</span>
        <strong>{title}</strong>
      </div>
      {children ? <div className="project-action-rail-switches">{children}</div> : null}
    </header>
  );
}
