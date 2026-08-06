import {
  BarChart3,
  BookOpen,
  BrainCircuit,
  ChevronLeft,
  ChevronRight,
  FileStack,
  Globe2,
  Menu,
  MoreHorizontal,
  Settings,
  UserRound,
  X
} from 'lucide-react';
import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { projectWorkspacePath } from './route-config.js';
import './shell.css';

export const SIDEBAR_STORAGE_KEY = 'opennovel.sidebar.collapsed';

export const PRIMARY_LINKS = [
  { to: '/projects', label: '项目', icon: FileStack },
  { to: '/libraries/novels', label: '小说仓库', icon: BookOpen },
  { to: '/worldbook', label: '世界书', icon: Globe2 },
  { to: '/characters', label: '角色卡', icon: UserRound },
  { to: '/dashboard', label: '仪表盘', icon: BarChart3 }
];

export function readSidebarCollapsed(storage = globalThis.localStorage) {
  try {
    return storage?.getItem(SIDEBAR_STORAGE_KEY) === 'true';
  } catch {
    return false;
  }
}

export function AppShell({ activeProject, children, connection = 'idle', onOpenProjectActions, pathname }) {
  const [collapsed, setCollapsed] = useState(readSidebarCollapsed);
  const [mobileOpen, setMobileOpen] = useState(false);

  useEffect(() => {
    setMobileOpen(false);
  }, [pathname]);

  const toggleCollapsed = () => {
    setCollapsed((current) => {
      const next = !current;
      try {
        globalThis.localStorage?.setItem(SIDEBAR_STORAGE_KEY, String(next));
      } catch {
        // Storage can be unavailable in private browsing; the in-memory state still works.
      }
      return next;
    });
  };

  return (
    <div className={`global-shell ${collapsed ? 'sidebar-collapsed' : ''}`}>
      <header className="global-mobile-header">
        <button aria-label="打开主导航" className="shell-icon-button" onClick={() => setMobileOpen(true)} type="button">
          <Menu size={20} />
        </button>
        <span className="shell-brand-mark"><BookOpen size={18} /></span>
        <strong title={activeProject?.name || 'OpenNovel'}>{activeProject?.name || 'OpenNovel'}</strong>
        <div className="global-mobile-project-state">
          {activeProject ? (
            <span
              aria-label={`连接状态：${connection}`}
              className="global-mobile-connection"
              data-status={connection}
              role="status"
              title={connection}
            />
          ) : null}
          {activeProject && onOpenProjectActions ? (
            <button aria-label="打开项目操作" className="shell-icon-button" onClick={onOpenProjectActions} type="button">
              <MoreHorizontal size={20} />
            </button>
          ) : null}
        </div>
      </header>

      {mobileOpen ? <button aria-label="关闭主导航" className="global-sidebar-backdrop" onClick={() => setMobileOpen(false)} type="button" /> : null}
      <aside className={`global-sidebar ${mobileOpen ? 'mobile-open' : ''}`} aria-label="主导航">
        <div className="global-sidebar-brand">
          <span className="shell-brand-mark"><BookOpen size={20} /></span>
          <strong>OpenNovel</strong>
          <button aria-label="关闭主导航" className="shell-icon-button sidebar-mobile-close" onClick={() => setMobileOpen(false)} type="button">
            <X size={19} />
          </button>
        </div>

        <nav className="global-sidebar-nav">
          {PRIMARY_LINKS.map((item) => <SidebarLink key={item.to} {...item} pathname={pathname} />)}
          {activeProject?.id ? (
            <div className="current-project-entry">
              <span className="sidebar-section-label">当前项目</span>
              <SidebarLink
                icon={BrainCircuit}
                label={activeProject.name || activeProject.id}
                pathname={pathname}
                to={projectWorkspacePath(activeProject.id)}
              />
            </div>
          ) : null}
        </nav>

        <div className="global-sidebar-footer">
          <SidebarLink icon={Settings} label="设置" pathname={pathname} to="/settings/providers" />
          <button className="sidebar-link sidebar-collapse" onClick={toggleCollapsed} title={collapsed ? '展开侧栏' : '收起侧栏'} type="button">
            {collapsed ? <ChevronRight size={19} /> : <ChevronLeft size={19} />}
            <span>{collapsed ? '展开侧栏' : '收起侧栏'}</span>
          </button>
        </div>
      </aside>

      <main className="global-shell-content">{children}</main>
    </div>
  );
}

function SidebarLink({ icon: Icon, label, pathname, to }) {
  const active = to === '/projects'
    ? pathname === '/projects'
    : pathname === to || pathname.startsWith(`${to}/`);
  return (
    <Link aria-current={active ? 'page' : undefined} className={`sidebar-link ${active ? 'active' : ''}`} title={label} to={to}>
      <Icon size={19} />
      <span>{label}</span>
    </Link>
  );
}
