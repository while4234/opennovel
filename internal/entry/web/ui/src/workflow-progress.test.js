import { readFileSync } from 'node:fs';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import {
  WorkflowProgressPanel,
  reflectRunningPlanningRevision,
  retainProjectWorkflowProgress,
  retainWorkflowProgress,
  shouldShowWorkflowProgressPanel,
  workflowOverallPercent,
  workflowProgressFromSnapshot,
  workflowRiskText
} from './workflow-progress.jsx';

const appSource = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');

function progress(overrides = {}) {
  return {
    workflow: 'normal',
    run_id: 'run-1',
    revision: 2,
    status: 'waiting_confirmation',
    current_step: 'review',
    steps: [
      { id: 'idea', label: '创意输入', status: 'completed' },
      { id: 'review', label: '设定与规划审核', status: 'waiting_confirmation', current: 2, total: 4 }
    ],
    next_action: {
      id: 'confirm',
      label: '确认规划并开始创作',
      expected_revision: 2,
      idempotency_key: 'key-1',
      requires_confirmation: true
    },
    recoverable: false,
    ...overrides
  };
}

describe('unified workflow progress', () => {
  it('renders the workflow panel through the review-aware visibility decision', () => {
    expect(appSource).toMatch(
      /\{showWorkflowProgress \? \([\s\S]*?<WorkflowProgressPanel[\s\S]*?projectId=\{activeProject\?\.id \|\| ''\}[\s\S]*?snapshot=\{snapshot\}[\s\S]*?\/>[\s\S]*?\) : null\}/
    );
    expect(appSource).toContain('adaptationProposalReviewVisible: showAdaptationProposalWorkspace');
    expect(appSource).toContain('showContinuationWorkspace && continuationNeedsConfirmation(continuationSnapshot)');
    expect(appSource).toContain('planningReviewCollecting: coCreatePlanningReview.collecting');
    expect(appSource).toContain('planningReviewVisible: showCoCreatePlanningWorkspace');
    expect(appSource).toContain('reviewActionRunning: projectBusy');
    expect(appSource).not.toMatch(/<WorkflowProgressPanel\s+key=/);
  });

  it('hides the workflow panel for user review while keeping it during AI generation', () => {
    expect(shouldShowWorkflowProgressPanel({
      centerView: 'writing',
      planningReviewCollecting: false,
      planningReviewVisible: true
    })).toBe(false);
    expect(shouldShowWorkflowProgressPanel({
      centerView: 'writing',
      planningReviewCollecting: true,
      planningReviewVisible: true
    })).toBe(true);
    expect(shouldShowWorkflowProgressPanel({
      centerView: 'writing',
      planningReviewCollecting: false,
      planningReviewVisible: true,
      reviewActionRunning: true
    })).toBe(true);
    expect(shouldShowWorkflowProgressPanel({
      adaptationProposalReviewVisible: true,
      centerView: 'writing'
    })).toBe(false);
    expect(shouldShowWorkflowProgressPanel({
      centerView: 'writing',
      continuationConfirmationVisible: true
    })).toBe(false);
    expect(shouldShowWorkflowProgressPanel({
      adaptationProposalReviewVisible: true,
      centerView: 'foundation',
      continuationConfirmationVisible: true,
      planningReviewCollecting: false,
      planningReviewVisible: true
    })).toBe(true);
  });

  it('does not hide progress merely because an inactive planning review is loaded', () => {
    expect(shouldShowWorkflowProgressPanel({
      centerView: 'writing',
      planningReviewCollecting: false,
      planningReviewVisible: false
    })).toBe(true);
  });

  it('reads the shared workflow contract from a project snapshot', () => {
    const value = progress();
    expect(workflowProgressFromSnapshot({ workflow_progress: value })).toBe(value);
    expect(workflowProgressFromSnapshot({ WorkflowProgress: value })).toBe(value);
    expect(workflowProgressFromSnapshot({ workflow_progress: { steps: [] } })).toBeNull();
  });

  it('retains the last valid workflow progress across incomplete snapshot replacements', () => {
    const previous = progress({ status: 'running' });

    expect(retainWorkflowProgress(previous, { runtime_state: 'running' })).toBe(previous);
    expect(retainWorkflowProgress(previous, null)).toBe(previous);
    expect(retainWorkflowProgress(previous, { workflow_progress: { steps: [] } })).toBe(previous);
  });

  it('replaces retained progress as soon as a newer valid workflow arrives', () => {
    const previous = progress({ status: 'running' });
    const current = progress({ status: 'completed', revision: 3 });

    expect(retainWorkflowProgress(previous, { workflow_progress: current })).toBe(current);
  });

  it('never leaks retained workflow progress into another project', () => {
    const projectA = progress({ workflow: 'normal' });
    const retainedA = retainProjectWorkflowProgress(null, 'project-a', { workflow_progress: projectA });
    const openingB = retainProjectWorkflowProgress(retainedA, 'project-b', null);
    const projectB = progress({ workflow: 'adaptation', run_id: 'run-b' });

    expect(openingB).toEqual({ projectId: 'project-b', progress: null });
    expect(retainProjectWorkflowProgress(openingB, 'project-b', { workflow_progress: projectB })).toEqual({
      projectId: 'project-b',
      progress: projectB
    });
  });

  it('combines completed and current step progress without exceeding 100%', () => {
    expect(workflowOverallPercent(progress())).toBe(75);
    expect(workflowOverallPercent(progress({
      steps: [{ id: 'writing', status: 'running', current: 14, total: 10 }]
    }))).toBe(100);
  });

  it.each([
    ['normal', '普通共创'],
    ['adaptation', '小说改编'],
    ['continuation', '小说续写']
  ])('renders %s with the same Chinese progress and accessibility contract', (workflow, label) => {
    const markup = renderToStaticMarkup(createElement(WorkflowProgressPanel, {
      snapshot: { workflow_progress: progress({ workflow }) }
    }));

    expect(markup).toContain(label);
    expect(markup).toContain('role="status"');
    expect(markup).toContain('aria-live="polite"');
    expect(markup).toContain('role="progressbar"');
    expect(markup).toContain('aria-current="step"');
    expect(markup).toContain('当前说明');
    expect(markup).not.toContain('下一操作');
    expect(markup).toContain('风险与恢复');
    expect(markup).toContain('2/4');
  });

  it('translates the core-role confirmation instruction shown to users', () => {
    const markup = renderToStaticMarkup(createElement(WorkflowProgressPanel, {
      snapshot: { workflow_progress: progress({
        steps: [
          { id: 'idea', label: '创意输入', status: 'completed' },
          { id: 'review', label: '设定与规划审核', status: 'waiting_confirmation', message: 'confirm the current core cast signature' }
        ]
      }) }
    }));
    expect(markup).toContain('请检查并确认当前核心角色与关系');
    expect(markup).not.toContain('confirm the current core cast signature');
  });

  it('shows the durable background action and refresh recovery state', () => {
    const markup = renderToStaticMarkup(createElement(WorkflowProgressPanel, {
      snapshot: {
        workflow_progress: progress({ workflow: 'adaptation', status: 'running' }),
        current_action: {
          action_id: 'action-1',
          kind: 'adaptation_proposal_generate',
          status: 'running',
          recoverable: false
        }
      }
    }));

    expect(markup).toContain('当前后台任务');
    expect(markup).toContain('生成改编提案');
    expect(markup).toContain('即使刷新页面');
  });

  it('labels a running workflow explicitly as 正在运行', () => {
    const markup = renderToStaticMarkup(createElement(WorkflowProgressPanel, {
      snapshot: {
        workflow_progress: progress({
          status: 'running',
          steps: [
            { id: 'idea', label: '创意输入', status: 'completed' },
            { id: 'review', label: '设定与规划审核', status: 'running' }
          ],
          next_action: null
        })
      }
    }));

    expect(markup).toContain('正在运行');
    expect(markup).not.toContain('等待确认');
  });

  it('shows the active backend and model beside a running status', () => {
    const markup = renderToStaticMarkup(createElement(WorkflowProgressPanel, {
      snapshot: {
        workflow_progress: progress({
          status: 'running',
          current_agent: 'writer',
          current_model: 'deepseek-v4-pro',
          current_provider: 'deepseek-yuanyu-0',
          steps: [{ id: 'review', label: '正文创作', status: 'running' }]
        })
      }
    }));

    expect(markup).toContain('正在运行 · 当前：正文写作 · 后端：deepseek-yuanyu-0 · 模型：deepseek-v4-pro');
  });

  it('prioritizes a workflow error and otherwise explains confirmation and recovery risk', () => {
    expect(workflowRiskText(progress({ error: '模型连接中断', recoverable: true }))).toBe('模型连接中断。请点击工作区顶部“恢复”继续。');
    expect(workflowRiskText(progress())).toContain('需要你确认');
    expect(workflowRiskText(progress({ next_action: null, recoverable: true }))).toContain('顶部“恢复”');
  });

  it('projects a running planning revision over a stale confirmation checkpoint', () => {
    const waiting = progress();
    const projected = reflectRunningPlanningRevision(waiting, {
      kind: 'planning_revision',
      status: 'running'
    });

    expect(projected).not.toBe(waiting);
    expect(projected.status).toBe('running');
    expect(projected.next_action).toBeNull();
    expect(projected.steps[1]).toMatchObject({
      status: 'running',
      message: 'AI 正在根据你的审核意见修改规划'
    });
    expect(waiting.status).toBe('waiting_confirmation');

    const markup = renderToStaticMarkup(createElement(WorkflowProgressPanel, {
      snapshot: {
        workflow_progress: waiting,
        current_action: {
          action_id: 'action-planning-revision',
          kind: 'planning_revision',
          status: 'running',
          recoverable: false
        }
      }
    }));
    expect(markup).toContain('正在运行');
    expect(markup).toContain('AI 正在根据你的审核意见修改规划');
    expect(markup).toContain('根据审核意见修改规划');
    expect(markup).not.toContain('等待确认');
  });

  it('does not project a completed or unrelated action over workflow progress', () => {
    const waiting = progress();

    expect(reflectRunningPlanningRevision(waiting, {
      kind: 'planning_revision',
      status: 'completed'
    })).toBe(waiting);
    expect(reflectRunningPlanningRevision(waiting, {
      kind: 'simulation_analysis',
      status: 'running'
    })).toBe(waiting);
  });

  it.each([
    ['中断恢复', 'resume_project', '恢复规划', false],
    ['阶段确认', 'commit_cocreate', '完成共创', true],
    ['开始生成', 'generate_outlines', '开始生成章节细纲', false]
  ])('leaves %s actions out of the read-only workflow display', (_, id, label, requiresConfirmation) => {
    const markup = renderToStaticMarkup(createElement(WorkflowProgressPanel, {
      snapshot: {
        workflow_progress: progress({
          next_action: {
            id,
            label,
            requires_confirmation: requiresConfirmation
          }
        })
      }
    }));

    expect(markup).not.toContain('下一操作');
    expect(markup).not.toContain(label);
    expect(markup).not.toContain('<button');
  });
});
