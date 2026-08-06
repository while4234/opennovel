import { describe, expect, it, vi } from 'vitest';
import {
  isMobileTaskSection,
  isMobileWorkspaceViewport,
  isWorkspaceSection,
  WORKSPACE_SECTIONS,
  workspaceSectionForView,
  workspaceStateForSection
} from './route-mapping.js';

describe('project workspace route mapping', () => {
  it('maps every deep-link section to the existing state owner', () => {
    expect(WORKSPACE_SECTIONS.map((section) => section.id)).toEqual([
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
    expect(workspaceStateForSection('write')).toEqual({ centerView: 'writing', sideView: 'status' });
    expect(workspaceStateForSection('manuscript')).toEqual({ centerView: 'manuscript', sideView: 'manuscript' });
    expect(workspaceStateForSection('foundation')).toEqual({ centerView: 'foundation', sideView: 'status' });
    expect(workspaceStateForSection('adaptation')).toEqual({ centerView: 'writing', sideView: 'adapt' });
    expect(workspaceStateForSection('unknown')).toEqual(workspaceStateForSection('write'));
  });

  it('maps tool changes back to canonical deep links', () => {
    expect(workspaceSectionForView('status')).toBe('write');
    expect(workspaceSectionForView('manuscript')).toBe('manuscript');
    expect(workspaceSectionForView('simulate')).toBe('simulation');
    expect(workspaceSectionForView('adapt')).toBe('adaptation');
    expect(workspaceSectionForView('continuation')).toBe('continuation');
    expect(workspaceSectionForView('audit')).toBe('audit');
    expect(workspaceSectionForView('export')).toBe('export');
    expect(workspaceSectionForView('diag')).toBe('diagnostics');
    expect(workspaceSectionForView('cache')).toBe('diagnostics');
    expect(workspaceSectionForView('backend')).toBe('diagnostics');
    expect(workspaceSectionForView('settings')).toBe('settings');
    expect(workspaceSectionForView('status', 'foundation')).toBe('foundation');
  });

  it('recognizes only supported workspace sections', () => {
    expect(isWorkspaceSection('write')).toBe(true);
    expect(isWorkspaceSection('diagnostics')).toBe(true);
    expect(isWorkspaceSection('models')).toBe(false);
    expect(isMobileTaskSection('adaptation')).toBe(true);
    expect(isMobileTaskSection('write')).toBe(false);
    expect(isMobileTaskSection('foundation')).toBe(false);
  });

  it('treats compact landscape viewports as mobile workspaces', () => {
    const matchMedia = vi.fn(() => ({ matches: true }));
    expect(isMobileWorkspaceViewport(matchMedia)).toBe(true);
    expect(matchMedia).toHaveBeenCalledWith('(max-width: 767px), (max-height: 520px) and (max-width: 1024px)');
  });
});
