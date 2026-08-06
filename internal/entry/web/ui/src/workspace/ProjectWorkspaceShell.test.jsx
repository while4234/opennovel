// @vitest-environment jsdom

import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { ProjectModelSettingsLink, ProjectWorkspaceHeader } from './ProjectWorkspaceShell.jsx';

describe('project workspace header', () => {
  let container;
  let root;

  beforeEach(() => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  it('renders all deep links and preserves supplied project actions', async () => {
    await act(async () => root.render(
      <MemoryRouter initialEntries={['/projects/story/adaptation']}>
        <ProjectWorkspaceHeader
          actions={<button type="button">暂停</button>}
          connection="live"
          project={{ id: 'story', name: '雾城来信' }}
          section="adaptation"
        />
      </MemoryRouter>
    ));

    const links = Array.from(container.querySelectorAll('.project-section-navigation a'));
    expect(links).toHaveLength(10);
    expect(links.find((link) => link.textContent === '改编')?.getAttribute('aria-current')).toBe('page');
    expect(links.find((link) => link.textContent === '稿件')?.getAttribute('href')).toBe('/projects/story/manuscript');
    expect(container.textContent).toContain('雾城来信');
    expect(container.textContent).toContain('暂停');
  });

  it('links directly to the current project model scope', async () => {
    await act(async () => root.render(
      <MemoryRouter>
        <ProjectModelSettingsLink projectId="story one" />
      </MemoryRouter>
    ));
    const link = container.querySelector('a[aria-label="配置本项目模型"]');
    expect(link?.getAttribute('href')).toBe('/settings/models?project=story+one');
    expect(link?.textContent).toContain('本项目模型');
  });
});
