import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { filterAndSortProjects } from './ProjectCenter.jsx';
import { readSidebarCollapsed, SIDEBAR_STORAGE_KEY } from './AppShell.jsx';
import { applyCompatibilityRoute, resolveProjectRouteRecovery } from '../App.jsx';

const appSource = readFileSync(new URL('../App.jsx', import.meta.url), 'utf8');
const mainSource = readFileSync(new URL('../main.jsx', import.meta.url), 'utf8');
const projectCenterSource = readFileSync(new URL('./ProjectCenter.jsx', import.meta.url), 'utf8');
const knowledgePageSource = readFileSync(new URL('../knowledge/KnowledgeFoundationPage.jsx', import.meta.url), 'utf8');

describe('global shell state', () => {
  it('restores the collapsed preference and tolerates unavailable storage', () => {
    expect(readSidebarCollapsed({ getItem: (key) => key === SIDEBAR_STORAGE_KEY ? 'true' : null })).toBe(true);
    expect(readSidebarCollapsed({ getItem: () => { throw new Error('blocked'); } })).toBe(false);
  });

  it('keeps the application state owner mounted above route surfaces', () => {
    expect(mainSource).toContain('createBrowserRouter');
    expect(mainSource).toContain('<RouterProvider router={router} />');
    expect(mainSource).toContain('<App />');
    expect(appSource).toContain('const compatibilityWorkspace = (');
    expect(appSource).toContain("compatibility-workspace ${projectCenterVisible || routeRecoveryVisible || settingsVisible || globalFeatureVisible ? 'is-hidden' : ''}");
    expect(appSource).toContain('knowledge-route-surface');
    expect(appSource).not.toContain('<Routes>');
  });

  it('keeps the setup gate ahead of the routed shell', () => {
    expect(appSource.indexOf('if (setup.required)')).toBeGreaterThanOrEqual(0);
    expect(appSource.indexOf('if (setup.required)')).toBeLessThan(appSource.indexOf('const compatibilityWorkspace = ('));
  });
});

describe('project center behavior', () => {
  it('searches by name and sorts projects by the latest known edit time', () => {
    const projects = [
      { id: 'old', name: '旧城', updated_at: '2026-01-01T00:00:00Z' },
      { id: 'new', name: '新城', last_accessed_at: '2026-07-01T00:00:00Z' },
      { id: 'other', name: '荒原', updated_at: '2026-08-01T00:00:00Z' }
    ];
    expect(filterAndSortProjects(projects).map((project) => project.id)).toEqual(['other', 'new', 'old']);
    expect(filterAndSortProjects(projects, '城').map((project) => project.id)).toEqual(['new', 'old']);
  });

  it('wires every project command through the existing API functions', () => {
    for (const call of [
      'await createProject(name)',
      'await renameProject(project.id, name)',
      'await cloneProject(project.id, name)',
      'await trashProject(project.id)',
      'await restoreTrashProject(project.id)',
      'await emptyTrashProjects()'
    ]) {
      expect(appSource).toContain(call);
    }
    expect(appSource).toContain('onTrashOpen={refreshTrashProjects}');
    expect(projectCenterSource).toContain('await onTrashOpen()');
  });

  it('keeps project and trash refresh failures visible without unhandled click promises', () => {
    expect(appSource).toContain("setProjectListState((previous) => ({ ...previous, loading: true, error: '' }))");
    expect(appSource).toContain("setTrashListState({ loading: true, error: '' })");
    expect(appSource).toContain('projectsError={projectListState.error}');
    expect(appSource).toContain('trashError={trashListState.error}');
    expect(projectCenterSource).toContain('void onRefresh().catch(() => {})');
    expect(projectCenterSource).toContain('回收站加载失败：{trashError}');
    expect(projectCenterSource).toContain("!trashLoading && !trashError ? <span className=\"project-trash-empty\">回收站为空</span> : null");
  });
});

describe('project-scoped route recovery', () => {
  const loaded = { loading: false, loaded: true, error: '' };

  it('keeps character and worldbook deep links independent from the active workbench project', () => {
    for (const path of ['/characters', '/worldbook']) {
      expect(resolveProjectRouteRecovery({
        activeProject: null,
        projectListState: loaded,
        projectOpen: { status: 'idle', project: null, error: '' },
        projects: [{ id: 'novel-1', name: '小说一' }],
        route: { kind: 'compatibility', path }
      })).toBeNull();
    }
    expect(knowledgePageSource).toContain('useIndependentProjectScope(projects)');
    expect(knowledgePageSource).not.toContain('openProject');
    expect(knowledgePageSource).not.toContain('resetProjectScopedState');
  });

  it('does not remap independent knowledge routes into the compatibility workbench', () => {
    const calls = [];
    const controls = {
      activeProject: { id: 'novel-1' },
      setCenterView: (value) => calls.push(['center', value]),
      setFoundationNavigation: (value) => calls.push(['foundation', value]),
      setSideView: (value) => calls.push(['side', value]),
      setToolDrawerOpen: (value) => calls.push(['drawer', value])
    };
    applyCompatibilityRoute('/characters', controls);
    applyCompatibilityRoute('/worldbook', controls);
    applyCompatibilityRoute('/libraries/novels', controls);
    applyCompatibilityRoute('/libraries/profiles', controls);
    applyCompatibilityRoute('/dashboard', controls);
    expect(calls).toEqual([]);
    expect(appSource).toContain('<KnowledgeFoundationPage kind="characters"');
    expect(appSource).toContain('<KnowledgeFoundationPage kind="worldbook"');
  });

  it('shows a recoverable state for a missing or failed project deep link', () => {
    const missing = resolveProjectRouteRecovery({
      activeProject: null,
      projectListState: loaded,
      projectOpen: { status: 'idle', project: null, error: '' },
      projects: [{ id: 'novel-1' }],
      route: { kind: 'workspace', projectId: 'missing', section: 'write' }
    });
    expect(missing).toEqual({ kind: 'missing', loading: false, error: '', projectId: 'missing', retryProjectId: '' });

    const failed = resolveProjectRouteRecovery({
      activeProject: { id: 'novel-1' },
      projectListState: loaded,
      projectOpen: { status: 'error', project: { id: 'novel-1' }, error: '加载失败' },
      projects: [{ id: 'novel-1' }],
      route: { kind: 'workspace', projectId: 'novel-1', section: 'write' }
    });
    expect(failed.error).toBe('加载失败');
    expect(failed.retryProjectId).toBe('novel-1');
    expect(appSource).toContain("onBack={() => navigate('/projects')}");
    expect(appSource).toContain('navigate(projectWorkspacePath(project.id))');
    expect(appSource).toContain('if (project) return openProject(project)');
    expect(appSource).toContain('onRetry={retryRouteRecovery}');
  });
});
