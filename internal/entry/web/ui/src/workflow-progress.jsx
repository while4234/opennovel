import React from 'react';

const workflowLabels = {
  normal: '普通共创',
  adaptation: '小说改编',
  continuation: '小说续写'
};

const statusLabels = {
  idle: '待开始',
  running: '正在运行',
  waiting_confirmation: '等待确认',
  paused: '已暂停',
  failed: '需要处理',
  completed: '已完成'
};

const actionKindLabels = {
  planning_revision: '根据审核意见修改规划',
  adaptation_analysis: '原文分析',
  adaptation_proposal: '改编提案',
  adaptation_proposal_generate: '生成改编提案',
  adaptation_volume_proposal_generate: '生成改编分卷',
  adaptation_proposal_revision: '修订改编提案',
  continuation_planning: '续写规划',
  continuation_proposal_generate: '生成续写提案',
  continuation_outlines_generate: '生成续写细纲',
  simulation_analysis: '分析仿写画像',
  simulation_import: '导入仿写画像'
};

const actionStatusLabels = {
  running: '后台进行中',
  completed: '已完成',
  failed: '执行失败',
  interrupted: '服务重启后待恢复'
};

const activeAgentLabels = {
  coordinator: '流程协调',
  architect: '结构规划',
  character: '角色设计',
  writer: '正文写作',
  editor: '质量审核',
  auditor: '质量审核'
};

const workflowMessageLabels = {
  'confirm the current core cast signature': '请检查并确认当前核心角色与关系'
};

export function workflowProgressFromSnapshot(snapshot) {
  const value = snapshot?.workflow_progress || snapshot?.WorkflowProgress;
  if (!value || !Array.isArray(value.steps) || value.steps.length === 0) {
    return null;
  }
  return value;
}

export function retainWorkflowProgress(previousProgress, snapshot) {
  return workflowProgressFromSnapshot(snapshot) || previousProgress || null;
}

export function retainProjectWorkflowProgress(previousState, projectId, snapshot) {
  const normalizedProjectId = String(projectId || '').trim();
  const incomingProgress = workflowProgressFromSnapshot(snapshot);
  if (previousState?.projectId !== normalizedProjectId) {
    return {
      projectId: normalizedProjectId,
      progress: incomingProgress
    };
  }
  return {
    projectId: normalizedProjectId,
    progress: incomingProgress || previousState?.progress || null
  };
}

export function workflowOverallPercent(progress) {
  const steps = Array.isArray(progress?.steps) ? progress.steps : [];
  if (steps.length === 0) {
    return 0;
  }
  const units = steps.reduce((sum, step) => {
    if (step?.status === 'completed') {
      return sum + 1;
    }
    const current = Number(step?.current);
    const total = Number(step?.total);
    if (total > 0 && current > 0) {
      return sum + Math.min(1, current / total);
    }
    return sum;
  }, 0);
  return Math.max(0, Math.min(100, Math.round((units / steps.length) * 100)));
}

export function reflectRunningPlanningRevision(progress, action) {
  if (
    !progress ||
    action?.kind !== 'planning_revision' ||
    action?.status !== 'running'
  ) {
    return progress;
  }
  const currentStep = String(progress.current_step || '').trim();
  return {
    ...progress,
    status: 'running',
    error: '',
    recoverable: false,
    next_action: null,
    steps: progress.steps.map((step) => (
      step?.id === currentStep
        ? {
            ...step,
            status: 'running',
            message: 'AI 正在根据你的审核意见修改规划'
          }
        : step
    ))
  };
}

export function workflowRiskText(progress) {
  const error = String(progress?.error || '').trim();
  if (error) {
    return progress?.recoverable ? `${error}。请点击工作区顶部“恢复”继续。` : error;
  }
  if (progress?.recoverable) {
    return '请点击工作区顶部“恢复”从检查点继续，不会跳过已完成步骤。';
  }
  if (progress?.next_action?.requires_confirmation) {
    return '需要你确认后才会继续，不会自动改写关键产物。';
  }
  if (progress?.status === 'running') {
    return '任务正在后台进行，请留意返工或模型异常提示。';
  }
  return '暂无需要处理的风险。';
}

export function shouldShowWorkflowProgressPanel({
  adaptationProposalReviewVisible = false,
  centerView,
  continuationConfirmationVisible = false,
  planningReviewCollecting = false,
  planningReviewVisible = false,
  reviewActionRunning = false
} = {}) {
  if (centerView !== 'writing' || reviewActionRunning) {
    return true;
  }
  const userReviewVisible =
    adaptationProposalReviewVisible ||
    continuationConfirmationVisible ||
    (planningReviewVisible && !planningReviewCollecting);
  return !userReviewVisible;
}

export function WorkflowProgressPanel({ projectId = '', snapshot }) {
  const progressRef = React.useRef({ projectId: '', progress: null });
  progressRef.current = retainProjectWorkflowProgress(progressRef.current, projectId, snapshot);
  const action = snapshot?.current_action || snapshot?.CurrentAction;
  const progress = reflectRunningPlanningRevision(progressRef.current.progress, action);
  if (!progress) {
    return null;
  }

  const percent = workflowOverallPercent(progress);
  const currentStep = progress.steps.find((step) => step?.id === progress.current_step);
  const rawCurrentMessage = String(currentStep?.message || '').trim();
  const currentMessage = workflowMessageLabels[rawCurrentMessage] || rawCurrentMessage;
  const workflowLabel = workflowLabels[progress.workflow] || '创作流程';
  const statusLabel = statusLabels[progress.status] || '状态未知';
  const currentAgent = progress.status === 'running' ? String(progress.current_agent || '').trim().toLowerCase() : '';
  const currentAgentLabel = activeAgentLabels[currentAgent] || currentAgent;
  const currentProvider = progress.status === 'running' ? String(progress.current_provider || '').trim() : '';
  const currentModel = progress.status === 'running' ? String(progress.current_model || '').trim() : '';
  const currentRoute = currentProvider && currentModel
    ? `后端：${currentProvider} · 模型：${currentModel}`
    : currentModel ? `模型：${currentModel}` : '';
  return (
    <section className={`workflow-progress workflow-${progress.status || 'idle'}`} aria-labelledby="workflow-progress-title">
      <header className="workflow-progress-header">
        <div>
          <span className="workflow-progress-kicker">{workflowLabel}</span>
          <h3 id="workflow-progress-title">创作流程</h3>
        </div>
        <span className="workflow-progress-status" role="status" aria-live="polite">
          {statusLabel}{currentAgentLabel ? ` · 当前：${currentAgentLabel}` : ''}{currentRoute ? ` · ${currentRoute}` : ''}
        </span>
      </header>

      <div
        className="workflow-overall-meter"
        role="progressbar"
        aria-label={`${workflowLabel}总进度`}
        aria-valuemin="0"
        aria-valuemax="100"
        aria-valuenow={percent}
        aria-valuetext={`${percent}%，当前步骤：${currentStep?.label || '待开始'}`}
      >
        <span style={{ width: `${percent}%` }} />
      </div>

      <ol className="workflow-step-list" aria-label={`${workflowLabel}步骤`}>
        {progress.steps.map((step, index) => (
          <WorkflowStep key={step.id || `${index}`} step={step} index={index} current={step.id === progress.current_step} />
        ))}
      </ol>

      <div className="workflow-progress-detail" aria-live="polite">
        <div>
          <span>当前说明</span>
          <strong>{currentMessage || currentStep?.label || '等待开始'}</strong>
        </div>
        <div className={progress.error || progress.recoverable ? 'workflow-risk is-warning' : 'workflow-risk'}>
          <span>风险与恢复</span>
          <strong>{workflowRiskText(progress)}</strong>
        </div>
      </div>
      <WorkflowActionRecovery action={action} />
    </section>
  );
}

function WorkflowActionRecovery({ action }) {
  if (!action) {
    return null;
  }
  const kind = actionKindLabels[action.kind] || '创作后台任务';
  const status = actionStatusLabels[action.status] || '状态更新中';
  const interrupted = action.status === 'interrupted';
  const failed = action.status === 'failed';
  const recovery = interrupted
    ? (action.recoverable ? '刷新后仍会保留任务记录，可从检查点重试。' : '任务不能自动恢复，请按页面提示重试。')
    : action.status === 'running'
      ? '即使刷新页面，也会重新连接这个后台任务。'
      : '刷新后仍可查看该任务的最终状态。';

  return (
    <div className={`workflow-action-recovery ${interrupted || failed ? 'is-warning' : ''}`} role="status" aria-live="polite">
      <div>
        <span>当前后台任务</span>
        <strong>{kind} · {status}</strong>
      </div>
      <p>{action.error || recovery}</p>
    </div>
  );
}

function WorkflowStep({ step, index, current }) {
  const currentValue = Math.max(0, Number(step?.current) || 0);
  const totalValue = Math.max(0, Number(step?.total) || 0);
  const hasCount = totalValue > 0;
  const percent = hasCount ? Math.min(100, Math.round((currentValue / totalValue) * 100)) : 0;
  const label = String(step?.label || `步骤 ${index + 1}`);
  const statusLabel = statusLabels[step?.status] || '待开始';

  return (
    <li className={`workflow-step step-${step?.status || 'idle'} ${current ? 'is-current' : ''}`} aria-current={current ? 'step' : undefined}>
      <span className="workflow-step-index" aria-hidden="true">{step?.status === 'completed' ? '✓' : index + 1}</span>
      <div className="workflow-step-copy">
        <strong>{label}</strong>
        <span>{statusLabel}{hasCount ? ` · ${currentValue}/${totalValue}` : ''}</span>
        {hasCount ? (
          <div
            className="workflow-step-meter"
            role="progressbar"
            aria-label={`${label}进度`}
            aria-valuemin="0"
            aria-valuemax={totalValue}
            aria-valuenow={Math.min(currentValue, totalValue)}
            aria-valuetext={`${currentValue}/${totalValue}`}
          >
            <span style={{ width: `${percent}%` }} />
          </div>
        ) : null}
      </div>
    </li>
  );
}
