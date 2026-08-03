import { useState } from 'react';
import { EXPANSION_STEPS } from './expansion-state.js';

const stageIndex = (revision) => {
  const stage = revision?.stage;
  const approval = revision?.approval_stage || '';
  if (stage === 'impact_review_pending') return 0;
  if (stage === 'candidate_generating') return approval.includes('structure') ? 1 : approval.includes('outline') ? 4 : approval.includes('prose') ? 7 : 10;
  if (stage === 'candidate_audit_pending') return approval.includes('structure') ? 2 : approval.includes('outline') ? 5 : 8;
  if (stage === 'approval_pending') return approval.includes('structure') ? 3 : approval.includes('outline') ? 6 : 9;
  if (stage === 'ready_to_publish') return 10;
  if (stage === 'completed') return 11;
  return 0;
};

export function RevisionStepper({ revision, onCommand, onNavigate }) {
  const [feedback, setFeedback] = useState('');
  const active = stageIndex(revision);
  const findings = revision?.findings || [];
  const stage = revision?.stage;
  const approval = revision?.approval_stage || '';
  const navigate = () => {
    document.getElementById('expansion-revision-workbench')?.focus();
    onNavigate?.();
  };
  return <section id="expansion-revision-workbench" tabIndex="-1" aria-label="固定修订工作区">
    <h3>修订进度</h3><p>当前状态：{stage || '等待状态同步'}</p>
    <ol className="expansion-stepper">{EXPANSION_STEPS.map((step, index) => <li key={`${index}:${step}`} aria-current={index === active ? 'step' : undefined}>{step}{index === active && findings.length ? <ul role="alert">{findings.map((finding) => <li key={finding}>{finding}</li>)}</ul> : null}</li>)}</ol>
    <div className="expansion-workbench-actions">
      <button type="button" onClick={navigate}>进入当前修订</button>
      {stage === 'candidate_audit_pending' ? <><button type="button" onClick={() => onCommand('request_audit')}>发起独立审核</button><span role="status">审核结论由服务端独立 auditor 签名产生</span></> : null}
      {stage === 'approval_pending' ? <button type="button" onClick={() => onCommand('approve')}>人工确认当前阶段</button> : null}
      {stage === 'candidate_generating' && approval.includes('structure') ? <button type="button" onClick={() => onCommand('structure')}>提交结构候选</button> : null}
      {stage === 'candidate_generating' && approval.includes('outline') ? <button type="button" onClick={() => onCommand('outline')}>提交提纲候选</button> : null}
      {stage === 'candidate_generating' && approval.includes('prose') ? <button type="button" onClick={() => onCommand('prose')}>提交正文返工候选</button> : null}
      {stage === 'ready_to_publish' ? <button type="button" onClick={() => onCommand('publish')}>后处理并原子发布</button> : null}
      {(stage === 'paused' || stage === 'failed') ? <button type="button" onClick={() => onCommand('retry')}>重试当前步骤</button> : null}
      {!revision?.terminal ? <button type="button" onClick={() => onCommand('cancel')}>取消修订</button> : null}
    </div>
    {(stage === 'candidate_audit_pending' || findings.length) ? <label>审核意见<textarea value={feedback} onChange={(event) => setFeedback(event.target.value)} /></label> : null}
    {findings.length ? <button type="button" onClick={() => onCommand('feedback', feedback || '请按当前审核发现定向修复')}>提交定向反馈</button> : null}
  </section>;
}
