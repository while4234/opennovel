import {
  BrainCircuit,
  Clock3,
  Database,
  MessageSquareText,
  Server,
  SlidersHorizontal
} from 'lucide-react';
import { Link } from 'react-router-dom';
import './settings.css';

export const SETTINGS_SECTIONS = [
  { id: 'providers', label: '提供商', description: '连接与认证', icon: SlidersHorizontal },
  { id: 'models', label: '模型', description: '路由与重试', icon: BrainCircuit },
  { id: 'context', label: '上下文', description: '窗口与压缩', icon: Database },
  { id: 'prompts', label: '提示词', description: '模型族规则', icon: MessageSquareText },
  { id: 'schedule', label: '定时恢复', description: '全局恢复计划', icon: Clock3 },
  { id: 'backend', label: '后端', description: '状态与调用', icon: Server }
];

export function settingsSectionFromPath(pathname = '') {
  const section = String(pathname).match(/^\/settings\/([^/]+)/)?.[1] || 'providers';
  return SETTINGS_SECTIONS.some((item) => item.id === section) ? section : 'providers';
}

export function settingsProjectIdFromSearch(search = '') {
  return String(new URLSearchParams(search).get('project') || '').trim();
}

export function settingsSectionPath(section, projectId = '') {
  const path = `/settings/${section}`;
  const value = String(projectId || '').trim();
  if (!value || !['providers', 'models'].includes(section)) return path;
  return `${path}?${new URLSearchParams({ project: value })}`;
}

export function SettingsCenter({ children, scopeProjectId = '', section }) {
  const current = SETTINGS_SECTIONS.find((item) => item.id === section) || SETTINGS_SECTIONS[0];
  return (
    <div className="settings-center">
      <aside className="settings-center-sidebar" aria-label="配置中心导航">
        <header>
          <span>配置中心</span>
          <small>OpenNovel</small>
        </header>
        <nav>
          {SETTINGS_SECTIONS.map(({ id, label, description, icon: Icon }) => (
            <Link
              aria-current={id === current.id ? 'page' : undefined}
              className={`settings-center-link ${id === current.id ? 'active' : ''}`}
              key={id}
              to={settingsSectionPath(id, scopeProjectId)}
            >
              <Icon size={18} />
              <span><strong>{label}</strong><small>{description}</small></span>
            </Link>
          ))}
        </nav>
      </aside>
      <section className="settings-center-main">
        <header className="settings-center-heading">
          <div>
            <span className="settings-center-eyebrow">系统配置</span>
            <h1>{current.label}</h1>
            <p>{current.description}</p>
          </div>
        </header>
        <div className={`settings-center-content settings-section-${current.id}`}>
          {children}
        </div>
      </section>
    </div>
  );
}

export function SettingsScopeSelector({ selectedProjectId = '', allowGlobal = true, onSelect, projects = [] }) {
  const selectedProject = projects.find((project) => project.id === selectedProjectId);
  return (
    <div className="settings-scope-selector">
      <label>
        <span>配置作用域</span>
        <select
          aria-label="配置作用域"
          value={selectedProjectId}
          onChange={(event) => onSelect?.(event.target.value)}
        >
          {allowGlobal ? <option value="">全局配置</option> : null}
          {!allowGlobal && !selectedProjectId ? <option value="">选择项目</option> : null}
          {projects.map((project) => <option key={project.id} value={project.id}>{project.name || project.id}</option>)}
        </select>
      </label>
      <p>{selectedProjectId ? `当前项目：${selectedProject?.name || selectedProjectId}` : allowGlobal ? '当前修改全局默认，新项目会继承这些设置。' : '选择项目后可查看和测试后端状态。'}</p>
    </div>
  );
}
