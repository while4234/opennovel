// @vitest-environment jsdom
import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ManuscriptTree } from './ManuscriptTree.jsx';

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

async function settle() {
  await act(async () => { await Promise.resolve(); await Promise.resolve(); });
}

describe('ManuscriptTree windowed keyboard navigation', () => {
  it('moves Arrow, Home, and End focus across a tree larger than 120 rows', async () => {
    const chapters = Array.from({ length: 130 }, (_, index) => ({
      kind: 'chapter', stable_id: `c${index}`, display_order: index + 1, display_label: `chapter ${index + 1}`
    }));
    const nodes = [{ kind: 'volume', stable_id: 'v1', display_label: 'volume', children: [{ kind: 'arc', stable_id: 'a1', display_label: 'arc', children: chapters }] }];
    await act(async () => root.render(<ManuscriptTree nodes={nodes} selectedId="c0" onSelect={vi.fn()} />));
    await settle();

    const boundary = container.querySelector('[data-tree-index="119"]');
    await act(async () => boundary.focus());
    await act(async () => boundary.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true })));
    await settle();
    expect(document.activeElement?.getAttribute('data-tree-index')).toBe('120');

    await act(async () => document.activeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true })));
    await settle();
    expect(document.activeElement?.getAttribute('data-tree-index')).toBe('131');

    await act(async () => document.activeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true })));
    await settle();
    expect(document.activeElement?.getAttribute('data-tree-index')).toBe('0');
    expect(container.querySelectorAll('[role="treeitem"][tabindex="0"]')).toHaveLength(1);
  });
});
