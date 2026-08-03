// @vitest-environment jsdom
import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ChapterCombobox } from './ChapterCombobox.jsx';

const chapters = [
  { stable_id: 'c1', display_order: 1, display_label: '起点', state: 'completed' },
  { stable_id: 'c2', display_order: 2, display_label: '雨夜', state: 'completed' },
  { stable_id: 'c3', display_order: 3, display_label: '雨夜', state: 'writing' },
];
let root;
let container;

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  Element.prototype.scrollIntoView = vi.fn();
  container = document.createElement('div'); document.body.append(container); root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount()); container.remove(); vi.restoreAllMocks();
});

async function render(onSelect = vi.fn()) {
  await act(async () => root.render(<ChapterCombobox chapters={chapters} selectedId="c1" onSelect={onSelect} />));
  return { trigger: container.querySelector('[role="combobox"]'), onSelect };
}

function setInputValue(input, value) {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, value);
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

describe('ChapterCombobox', () => {
  it('renders a select-like trigger instead of an editable text field', async () => {
    const { trigger } = await render();
    expect(trigger.tagName).toBe('BUTTON');
    expect(trigger.textContent).toContain('第 1 章');
    expect(container.querySelector('[aria-label="搜索章节"]')).toBeNull();
  });

  it('opens all chapters and supports Arrow, Home/End, Enter, and Escape', async () => {
    const { trigger, onSelect } = await render();
    await act(async () => trigger.click());
    const input = container.querySelector('[aria-label="搜索章节"]');
    expect(trigger.getAttribute('aria-expanded')).toBe('true');
    expect(container.querySelectorAll('[role="option"]')).toHaveLength(3);
    await act(async () => input.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true })));
    expect(input.getAttribute('aria-activedescendant')).toContain('option-2');
    await act(async () => input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })));
    expect(onSelect).toHaveBeenCalledWith('c3');
    expect(document.activeElement).toBe(trigger);
    await act(async () => trigger.click());
    const reopenedInput = container.querySelector('[aria-label="搜索章节"]');
    await act(async () => reopenedInput.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
    expect(trigger.getAttribute('aria-expanded')).toBe('false');
    expect(document.activeElement).toBe(trigger);
  });

  it('filters duplicate titles in formal order and never switches on no match or out-of-range input', async () => {
    const { trigger, onSelect } = await render();
    await act(async () => trigger.click());
    const input = container.querySelector('[aria-label="搜索章节"]');
    await act(async () => setInputValue(input, '雨夜'));
    expect([...container.querySelectorAll('[role="option"]')].map((option) => option.textContent)).toEqual(expect.arrayContaining([expect.stringContaining('第 2 章'), expect.stringContaining('第 3 章')]));
    await act(async () => { setInputValue(input, '999'); });
    await act(async () => input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })));
    expect(container.textContent).toContain('未找到匹配章节');
    expect(onSelect).not.toHaveBeenCalled();
  });

  it('supports mouse selection after Chinese-number input', async () => {
    const { trigger, onSelect } = await render();
    await act(async () => trigger.click());
    const input = container.querySelector('[aria-label="搜索章节"]');
    await act(async () => setInputValue(input, '第二章'));
    const option = container.querySelector('[role="option"]');
    expect(option.textContent).toContain('第 2 章');
    await act(async () => option.click());
    expect(onSelect).toHaveBeenCalledWith('c2');
  });
});
