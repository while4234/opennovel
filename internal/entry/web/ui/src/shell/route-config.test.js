import { describe, expect, it } from 'vitest';
import { projectModelSettingsPath, projectWorkspacePath, resolveAppRoute } from './route-config.js';

describe('application route contract', () => {
  it('redirects the root and unknown locations to the project center', () => {
    expect(resolveAppRoute('/')).toEqual({ kind: 'redirect', destination: '/projects' });
    expect(resolveAppRoute('/not-a-route')).toEqual({ kind: 'redirect', destination: '/projects' });
  });

  it('resolves project deep links without losing the project identifier', () => {
    expect(resolveAppRoute('/projects/project%20one/manuscript')).toEqual({
      kind: 'workspace',
      projectId: 'project one',
      section: 'manuscript'
    });
    expect(resolveAppRoute('/projects/demo/unknown')).toEqual({
      kind: 'workspace',
      projectId: 'demo',
      section: 'write'
    });
    expect(projectWorkspacePath('project one', 'foundation')).toBe('/projects/project%20one/foundation');
    expect(projectModelSettingsPath('project one')).toBe('/settings/models?project=project+one');
  });

  it('keeps every planned global route reachable through a compatibility view', () => {
    for (const path of ['/characters', '/worldbook', '/libraries/novels', '/libraries/profiles', '/dashboard', '/settings/providers', '/settings/models', '/settings/context', '/settings/prompts', '/settings/schedule', '/settings/backend']) {
      expect(resolveAppRoute(path)).toEqual({ kind: 'compatibility', path });
    }
  });
});
