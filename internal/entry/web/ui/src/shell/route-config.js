export const PROJECT_SECTIONS = new Set([
  'write',
  'manuscript',
  'foundation',
  'simulation',
  'adaptation',
  'continuation',
  'audit',
  'export',
  'diagnostics',
  'settings'
]);

export const GLOBAL_ROUTES = new Set([
  '/projects',
  '/characters',
  '/worldbook',
  '/libraries/novels',
  '/libraries/profiles',
  '/dashboard',
  '/settings/providers',
  '/settings/models',
  '/settings/context',
  '/settings/prompts',
  '/settings/schedule',
  '/settings/backend'
]);

export function resolveAppRoute(pathname = '/') {
  const normalized = normalizePath(pathname);
  if (normalized === '/') {
    return { kind: 'redirect', destination: '/projects' };
  }
  if (normalized === '/projects') {
    return { kind: 'projects' };
  }

  const projectMatch = normalized.match(/^\/projects\/([^/]+)(?:\/([^/]+))?$/);
  if (projectMatch) {
    const section = PROJECT_SECTIONS.has(projectMatch[2]) ? projectMatch[2] : 'write';
    return {
      kind: 'workspace',
      projectId: decodeURIComponent(projectMatch[1]),
      section
    };
  }

  if (GLOBAL_ROUTES.has(normalized)) {
    return { kind: 'compatibility', path: normalized };
  }
  return { kind: 'redirect', destination: '/projects' };
}

export function projectWorkspacePath(projectId, section = 'write') {
  const safeSection = PROJECT_SECTIONS.has(section) ? section : 'write';
  return `/projects/${encodeURIComponent(projectId)}/${safeSection}`;
}

export function projectModelSettingsPath(projectId) {
  const value = String(projectId || '').trim();
  return value ? `/settings/models?${new URLSearchParams({ project: value })}` : '/settings/models';
}

function normalizePath(pathname) {
  const value = String(pathname || '/').trim() || '/';
  return value.length > 1 ? value.replace(/\/+$/, '') : value;
}
