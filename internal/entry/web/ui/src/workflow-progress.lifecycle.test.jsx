// @vitest-environment jsdom

import React, { StrictMode } from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { WorkflowProgressPanel } from './workflow-progress.jsx';

function workflow(workflowName, revision) {
  return {
    workflow: workflowName,
    run_id: `${workflowName}-${revision}`,
    revision,
    status: 'paused',
    current_step: 'writing',
    steps: [{ id: 'writing', label: '正文创作', status: 'paused' }]
  };
}

describe('workflow progress lifecycle', () => {
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

  async function render(projectId, snapshot) {
    await act(async () => {
      root.render(
        <StrictMode>
          <main>
            <WorkflowProgressPanel projectId={projectId} snapshot={snapshot} />
            <div className="next-workspace" />
          </main>
        </StrictMode>
      );
    });
  }

  it('keeps exactly one panel through sparse updates and project switches', async () => {
    await render('project-a', null);
    await render('project-a', { workflow_progress: workflow('normal', 1) });
    await render('project-a', { runtime_state: 'running' });
    await render('project-a', { workflow_progress: workflow('normal', 2) });

    expect(container.querySelectorAll('.workflow-progress')).toHaveLength(1);
    expect(container.textContent).toContain('普通共创');

    await render('project-b', null);
    expect(container.querySelectorAll('.workflow-progress')).toHaveLength(0);
    expect(container.textContent).not.toContain('普通共创');

    await render('project-b', { workflow_progress: workflow('adaptation', 1) });
    await render('project-b', { workflow_progress: workflow('adaptation', 2) });

    expect(container.querySelectorAll('.workflow-progress')).toHaveLength(1);
    expect(container.textContent).toContain('小说改编');
    expect(container.textContent).not.toContain('普通共创');
  });
});
