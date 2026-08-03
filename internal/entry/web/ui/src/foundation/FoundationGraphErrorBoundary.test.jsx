// @vitest-environment jsdom
import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { FoundationGraphErrorBoundary } from './FoundationGraphErrorBoundary.jsx';

describe('FoundationGraphErrorBoundary', () => {
  let root;
  afterEach(() => {
    if (root) act(() => root.unmount());
    document.body.innerHTML = '';
    vi.restoreAllMocks();
  });

  it('render failure degrades to the relationship list action without losing the outer draft', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    const onUseList = vi.fn();
    const container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    function BrokenGraph() { throw new Error('graph renderer failed'); }
    act(() => root.render(<FoundationGraphErrorBoundary fallback={<button type="button" onClick={onUseList}>使用关系列表</button>}><BrokenGraph /></FoundationGraphErrorBoundary>));
    const fallback = container.querySelector('button');
    expect(fallback?.textContent).toBe('使用关系列表');
    fallback.click();
    expect(onUseList).toHaveBeenCalledOnce();
  });
});
