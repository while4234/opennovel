// @vitest-environment jsdom

import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { NavigationGuardProvider } from '../navigation/NavigationGuard.jsx';
import { Dashboard, usageSummary } from './Dashboard.jsx';
import { LibraryCenter } from './LibraryCenter.jsx';

describe('knowledge pages', () => {
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
    vi.restoreAllMocks();
  });

  async function render(element) {
    const router = createMemoryRouter([{ path: '*', element: <NavigationGuardProvider>{element}</NavigationGuardProvider> }]);
    await act(async () => root.render(<RouterProvider router={router} />));
    await act(async () => Promise.resolve());
  }

  it('loads a novel into the independently selected project', async () => {
    const api = {
      listNovels: vi.fn().mockResolvedValue({ items: [{ name: '雾城', chapter_count: 12 }] }),
      loadNovel: vi.fn().mockResolvedValue({ message: '已加载' })
    };
    await render(<LibraryCenter api={api} kind="novels" projects={[{ id: 'p-a', name: '项目 A' }, { id: 'p-b', name: '项目 B' }]} />);
    const selector = container.querySelector('select[aria-label="目标项目"]');
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set;
      setter.call(selector, 'p-b');
      selector.dispatchEvent(new Event('change', { bubbles: true }));
    });
    const loadButton = Array.from(container.querySelectorAll('button')).find((button) => button.textContent === '加载到项目');
    await act(async () => loadButton.click());
    expect(api.loadNovel).toHaveBeenCalledWith('p-b', '雾城');
    expect(container.textContent).toContain('已加载');
  });

  it('renders dashboard empty and error states explicitly', async () => {
    const emptyAPI = { usage: vi.fn().mockResolvedValue({ groups: [], trend: [] }), recommendations: vi.fn().mockResolvedValue({ recommendations: [] }) };
    await render(<Dashboard api={emptyAPI} projects={[]} />);
    expect(container.textContent).toContain('暂无可观测数据');

    const failingAPI = { usage: vi.fn().mockRejectedValue(new Error('usage unavailable')), recommendations: vi.fn().mockResolvedValue({ recommendations: [] }) };
    const router = createMemoryRouter([{ path: '*', element: <NavigationGuardProvider><Dashboard api={failingAPI} projects={[]} /></NavigationGuardProvider> }]);
    await act(async () => root.render(<RouterProvider router={router} />));
    await act(async () => Promise.resolve());
    expect(container.querySelector('[role="alert"]').textContent).toContain('usage unavailable');
  });
});

describe('usage summary', () => {
  it('aggregates calls, tokens, cache, and cost without trusting a client-side total', () => {
    expect(usageSummary({ groups: [
      { calls: 2, input_tokens: 100, cache_read_tokens: 40, cost_usd: 0.2 },
      { calls: 3, input_tokens: 50, cache_read_tokens: 10, cost_usd: 0.1 }
    ] })).toEqual({ calls: 5, inputTokens: 150, cacheReadTokens: 50, cost: 0.30000000000000004 });
  });
});
