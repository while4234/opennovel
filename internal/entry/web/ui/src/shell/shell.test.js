import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { PRIMARY_LINKS, readSidebarCollapsed, SIDEBAR_STORAGE_KEY } from './AppShell.jsx';
import { filterAndSortProjects } from './ProjectCenter.jsx';
import { projectWorkspacePath, resolveAppRoute } from './route-config.js';

const appSource = readFileSync(new URL('../App.jsx', import.meta.url), 'utf8');
const mainSource = readFileSync(new URL('../main.jsx', import.meta.url), 'utf8');
const projectCenterSource = readFileSync(new URL('./ProjectCenter.jsx', import.meta.url), 'utf8');

describe('application routes', () => {
  it('redirects the root and resolves project deep links', () => {
    expect(resolveAppRoute('/')).toEqual({ kind: 'redirect', destination: '/projects' });
    expect(resolveAppRoute('/projects/project-a/manuscript')).toEqual({
      kind: 'workspace',
      projectId: 'project-a',
      section: 'manuscript'
    });
    expect(resolveAppRoute('/projects/project-a/unknown')).toEqual({
      kind: 'workspace',
      projectId: 'project-a',
      section: 'write'
    });
    expect(projectWorkspacePath('project a', 'foundation')).toBe('/projects/project%20a/foundation');
  });

  it('keeps the stateful app mounted once in a wildcard data-router route', () => {
    expect(mainSource).toContain('createBrowserRouter');
    expect(mainSource).toMatch(/path:\s*['"]\*['"][\s\S]*?<NavigationGuardProvider>[\s\S]*?<App \/>[\s\S]*?<\/NavigationGuardProvider>/);
    expect(mainSource).toContain('<RouterProvider router={router} />');
    expect(appSource).toContain('const compatibilityWorkspace = (');
    expect(appSource).toContain('{compatibilityWorkspace}');
    expect(appSource).toContain("projectCenterVisible || routeRecoveryVisible || settingsVisible || globalFeatureVisible ? 'is-hidden' : ''");
  });

  it('keeps the existing setup gate ahead of the application shell', () => {
    expect(appSource.indexOf('if (setup.required)')).toBeLessThan(appSource.indexOf('const compatibilityWorkspace'));
    expect(appSource).toContain('<SetupWizard');
  });
});

describe('application shell preferences', () => {
  it('reads persisted collapsed state without depending on browser storage', () => {
    const storage = { getItem: (key) => key === SIDEBAR_STORAGE_KEY ? 'true' : null };
    expect(readSidebarCollapsed(storage)).toBe(true);
    expect(readSidebarCollapsed({ getItem: () => { throw new Error('blocked'); } })).toBe(false);
  });

  it('keeps prompts inside settings instead of the primary sidebar', () => {
    expect(PRIMARY_LINKS.some((link) => link.to === '/settings/prompts')).toBe(false);
    expect(appSource).toContain("settingsSection === 'prompts'");
  });
});

describe('project center behavior', () => {
  it('searches by project name and sorts by recent activity', () => {
    const projects = [
      { id: 'old', name: '旧项目', updated_at: '2026-01-01T00:00:00Z' },
      { id: 'new', name: '新项目', last_accessed_at: '2026-08-01T00:00:00Z' },
      { id: 'middle', name: '中间项目', created_at: '2026-06-01T00:00:00Z' }
    ];
    expect(filterAndSortProjects(projects).map((project) => project.id)).toEqual(['new', 'middle', 'old']);
    expect(filterAndSortProjects(projects, '中间').map((project) => project.id)).toEqual(['middle']);
  });

  it('wires every project operation through parent commands', () => {
    for (const command of ['onCreate', 'onOpen', 'onRename', 'onClone', 'onTrash', 'onRestore', 'onEmptyTrash']) {
      expect(projectCenterSource).toContain(command);
    }
    expect(appSource).toContain('await createProject(name)');
    expect(appSource).toContain('await renameProject(project.id, name)');
    expect(appSource).toContain('await cloneProject(project.id, name)');
    expect(appSource).toContain('await trashProject(project.id)');
    expect(appSource).toContain('await restoreTrashProject(project.id)');
    expect(appSource).toContain('await emptyTrashProjects()');
  });
});
