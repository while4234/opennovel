export const WORKSPACE_SECTIONS = Object.freeze([
  { id: 'write', label: '创作', description: '实时生成与共创' },
  { id: 'manuscript', label: '稿件', description: '章节与修订' },
  { id: 'foundation', label: '设定', description: '角色、关系与世界规则' },
  { id: 'simulation', label: '仿写', description: '画像与语料' },
  { id: 'adaptation', label: '改编', description: '提案与执行' },
  { id: 'continuation', label: '续写', description: '旧稿导入与续写' },
  { id: 'audit', label: '审计', description: '改编一致性检查' },
  { id: 'export', label: '导出', description: '成稿下载' },
  { id: 'diagnostics', label: '诊断', description: '运行与缓存' },
  { id: 'settings', label: '项目设置', description: '文风与恢复' }
]);

const ROUTE_STATE = Object.freeze({
  write: { centerView: 'writing', sideView: 'status' },
  manuscript: { centerView: 'manuscript', sideView: 'manuscript' },
  foundation: { centerView: 'foundation', sideView: 'status' },
  simulation: { centerView: 'writing', sideView: 'simulate' },
  adaptation: { centerView: 'writing', sideView: 'adapt' },
  continuation: { centerView: 'writing', sideView: 'continuation' },
  audit: { centerView: 'writing', sideView: 'audit' },
  export: { centerView: 'writing', sideView: 'export' },
  diagnostics: { centerView: 'writing', sideView: 'diag' },
  settings: { centerView: 'writing', sideView: 'settings' }
});

const VIEW_SECTION = Object.freeze({
  manuscript: 'manuscript',
  simulate: 'simulation',
  adapt: 'adaptation',
  continuation: 'continuation',
  audit: 'audit',
  export: 'export',
  diag: 'diagnostics',
  cache: 'diagnostics',
  backend: 'diagnostics',
  settings: 'settings'
});

const MOBILE_TASK_SECTIONS = new Set([
  'simulation',
  'adaptation',
  'continuation',
  'audit',
  'export',
  'diagnostics',
  'settings'
]);

export const MOBILE_WORKSPACE_MEDIA_QUERY = '(max-width: 767px), (max-height: 520px) and (max-width: 1024px)';

export function workspaceStateForSection(section) {
  return ROUTE_STATE[section] || ROUTE_STATE.write;
}

export function workspaceSectionForView(view, centerView = 'writing') {
  if (centerView === 'foundation') return 'foundation';
  if (centerView === 'manuscript' || view === 'manuscript') return 'manuscript';
  return VIEW_SECTION[view] || 'write';
}

export function isWorkspaceSection(section) {
  return Object.hasOwn(ROUTE_STATE, section);
}

export function isMobileTaskSection(section) {
  return MOBILE_TASK_SECTIONS.has(section);
}

export function isMobileWorkspaceViewport(matchMedia = globalThis.matchMedia) {
  return matchMedia?.(MOBILE_WORKSPACE_MEDIA_QUERY).matches === true;
}
