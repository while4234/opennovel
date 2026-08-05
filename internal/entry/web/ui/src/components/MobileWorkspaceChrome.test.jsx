// @vitest-environment jsdom

import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MobileToolMenu, MobileWorkspaceChrome, mobileToolLabel } from './MobileWorkspaceChrome.jsx';

describe('mobile workspace chrome', () => {
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

  async function renderChrome(overrides = {}) {
    const props = {
      actionsOpen: false,
      actions: <button type="button">保存快照</button>,
      connection: 'connected',
      currentView: 'writing',
      onCloseActions: vi.fn(),
      onOpenActions: vi.fn(),
      onOpenProjects: vi.fn(),
      onOpenTools: vi.fn(),
      onSelectManuscript: vi.fn(),
      onSelectWriting: vi.fn(),
      projectName: '很长但必须安全截断的测试小说项目名称',
      ...overrides
    };
    await act(async () => root.render(<MobileWorkspaceChrome {...props} />));
    return props;
  }

  it('routes the three primary navigation targets without hiding their labels', async () => {
    const props = await renderChrome();
    const buttons = Array.from(container.querySelectorAll('.mobile-phone-bottom-nav button'));

    expect(buttons.map((button) => button.textContent)).toEqual(['创作', '稿件', '工具']);
    expect(buttons[0].getAttribute('aria-current')).toBe('page');
    await act(async () => buttons[0].click());
    await act(async () => buttons[1].click());
    await act(async () => buttons[2].click());
    expect(props.onSelectWriting).toHaveBeenCalledOnce();
    expect(props.onSelectManuscript).toHaveBeenCalledOnce();
    expect(props.onOpenTools).toHaveBeenCalledOnce();
  });

  it('opens the project and project-action entry points', async () => {
    const props = await renderChrome();
    await act(async () => container.querySelector('[aria-label="打开项目列表"]').click());
    await act(async () => container.querySelector('[aria-label="打开项目操作"]').click());
    expect(props.onOpenProjects).toHaveBeenCalledOnce();
    expect(props.onOpenActions).toHaveBeenCalledOnce();
    expect(container.textContent).toContain('很长但必须安全截断的测试小说项目名称');
  });

  it('traps the action sheet and closes it with Escape', async () => {
    const onCloseActions = vi.fn();
    await renderChrome({ actionsOpen: true, onCloseActions });
    expect(container.querySelector('[role="dialog"][aria-label="项目操作"]')).not.toBeNull();
    await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
    expect(onCloseActions).toHaveBeenCalledOnce();
  });
});

describe('mobile tool menu', () => {
  it('groups tools, hides unavailable co-create, and reports the selected tool', () => {
    const selectTool = vi.fn();
    const close = vi.fn();
    const container = document.createElement('div');
    const root = createRoot(container);
    act(() => root.render(<MobileToolMenu coCreateVisible={false} onClose={close} onSelectTool={selectTool} />));

    expect(container.textContent).toContain('创作管理');
    expect(container.textContent).toContain('文稿流程');
    expect(container.textContent).toContain('系统管理');
    expect(container.textContent).not.toContain('与 AI 讨论并整理创作结果');
    act(() => Array.from(container.querySelectorAll('button')).find((button) => button.textContent.includes('改编审计')).click());
    expect(selectTool).toHaveBeenCalledWith('audit');
    expect(mobileToolLabel('audit')).toBe('改编审计');
    act(() => container.querySelector('[aria-label="关闭工具中心"]').click());
    expect(close).toHaveBeenCalledOnce();
    act(() => root.unmount());
  });
});
