import { RefreshCw, Square, WandSparkles } from 'lucide-react';
import {
  auditRunLabel,
  buildSemanticAuditRequest,
  normalizeAuditComparison,
  normalizeAuditRunList,
  semanticAuditProgress
} from './audit-runs.js';

const comparisonLabels = {
  introduced: '新增问题',
  resolved: '已解决',
  worsened: '问题加重',
  improved: '问题减轻',
  explanation_changed: '证据或说明变化',
  unchanged: '未变化'
};

export function AuditRunWorkbench({ audit, disabled, onCancel, onCompare, onEstimate, onRetry, onSelect, onStart, providers = [], setAudit }) {
  const runs = normalizeAuditRunList(audit.runs);
  const semantic = audit.semantic || {};
  const progress = semanticAuditProgress(semantic.run);
  const comparison = normalizeAuditComparison(audit.comparison);
  const updateSemantic = (changes) => setAudit((previous) => ({
    ...previous,
    semantic: { ...(previous.semantic || {}), ...changes, error: '' }
  }));
  const request = buildSemanticAuditRequest({ ...semantic, sourceTo: audit.sourceTo });
  const running = ['queued', 'running'].includes(String(semantic.run?.status || '').toLowerCase());
  const needsPriceAcknowledgement = semantic.estimate?.price_known === false;
  const callLimitTooLow = Number(semantic.estimate?.estimated_calls || 0) > Number(semantic.maxCalls || 0);
  const canStart = request.ok && !callLimitTooLow && (!needsPriceAcknowledgement || semantic.acknowledgeUnknownPrice === true);
  const selectedProvider = providers.find((provider) => provider.name === semantic.provider);
  const modelOptions = selectedProvider?.models || [];

  return (
    <>
      <section className="simulation-section audit-history-section">
        <div className="section-title"><RefreshCw size={17} /><span>审计历史与对比</span></div>
        {runs.length ? (
          <>
            <label className="field-label">
              <span>查看历史报告</span>
              <select disabled={disabled} onChange={(event) => onSelect(event.target.value)} value={audit.selectedRunId || ''}>
                <option value="">最近报告</option>
                {runs.map((run) => <option key={run.run_id} value={run.run_id}>{auditRunLabel(run)}</option>)}
              </select>
            </label>
            <label className="field-label">
              <span>比较基线</span>
              <select disabled={disabled || !audit.selectedRunId} onChange={(event) => updateSemantic({ compareBaseRunId: event.target.value })} value={semantic.compareBaseRunId || ''}>
                <option value="">选择另一份报告</option>
                {runs.filter((run) => run.run_id !== audit.selectedRunId).map((run) => <option key={run.run_id} value={run.run_id}>{auditRunLabel(run)}</option>)}
              </select>
            </label>
            <button className="tool-button full-width" disabled={disabled || !semantic.compareBaseRunId || !audit.selectedRunId} onClick={() => onCompare(semantic.compareBaseRunId, audit.selectedRunId)} type="button">比较两次审计</button>
          </>
        ) : <div className="empty-state">尚无可比较的审计历史。</div>}
        {comparison ? <AuditComparison comparison={comparison} /> : null}
      </section>

      <section className="simulation-section semantic-audit-section">
        <div className="section-title"><WandSparkles size={17} /><span>强模型全文二审</span></div>
        <p className="audit-intro">按完整改编单元分批阅读全文，模型只提交候选问题和引文；服务端验证证据后才形成只读报告。</p>
        <div className="field-grid">
          <label className="field-label"><span>Provider</span><select disabled={disabled || running} onChange={(event) => updateSemantic({ provider: event.target.value, model: '' })} value={semantic.provider || ''}><option value="">使用 auditor 默认路由</option>{providers.map((provider) => <option key={provider.name} value={provider.name}>{provider.name}</option>)}</select></label>
          <label className="field-label"><span>Model</span><select disabled={disabled || running || !semantic.provider} onChange={(event) => updateSemantic({ model: event.target.value })} value={semantic.model || ''}><option value="">选择模型</option>{modelOptions.map((model) => { const value = typeof model === 'string' ? model : model.name; return <option key={value} value={value}>{value}</option>; })}</select></label>
          <label className="field-label"><span>推理强度</span><select disabled={disabled || running} onChange={(event) => updateSemantic({ reasoningEffort: event.target.value })} value={semantic.reasoningEffort || ''}><option value="">模型默认</option><option value="low">low</option><option value="medium">medium</option><option value="high">high</option></select></label>
          <label className="field-label"><span>费用上限（USD）</span><input disabled={disabled || running} min="0" onChange={(event) => updateSemantic({ maxCostUsd: event.target.value })} placeholder="可选" step="0.1" type="number" value={semantic.maxCostUsd || ''} /></label>
          <label className="field-label"><span>调用次数上限</span><input disabled={disabled || running} min="1" onChange={(event) => updateSemantic({ maxCalls: event.target.value })} placeholder="默认 100" step="1" type="number" value={semantic.maxCalls || ''} /></label>
        </div>
        {semantic.error || (!request.ok && (semantic.maxCostUsd || audit.sourceTo)) ? <div className="error-banner compact">{semantic.error || request.error}</div> : null}
        {semantic.estimate ? <div className="audit-scope-help">约 {semantic.estimate.artifact_runes || 0} runes、{semantic.estimate.estimated_calls || 0} 次调用；{semantic.estimate.price_known === false ? '价格未知，启动前请设置调用次数上限。' : `预计费用 $${Number(semantic.estimate.estimated_cost_usd || 0).toFixed(2)}。`}</div> : null}
        {callLimitTooLow ? <div className="settings-note warning">当前调用上限为 {semantic.maxCalls || 0}，低于预计的 {semantic.estimate.estimated_calls} 次；提高上限或缩小审计范围后才能启动。</div> : null}
        {needsPriceAcknowledgement ? <label className="checkbox-row audit-confirmation-row"><input checked={semantic.acknowledgeUnknownPrice === true} disabled={disabled || running} onChange={(event) => updateSemantic({ acknowledgeUnknownPrice: event.target.checked })} type="checkbox" /><span>我已了解当前模型价格未知，本次运行以调用次数上限控制。</span></label> : null}
        {semantic.run ? <div className={`workflow-status ${semantic.run.status || 'idle'}`}><strong>{semantic.run.stage || semantic.run.status}</strong><span>{progress.completed}/{progress.total}（{progress.percent}%），已覆盖 {semantic.run.progress?.covered_runes || semantic.run.covered_target_runes || 0}/{semantic.run.progress?.total_runes || 0} runes</span></div> : null}
        <div className="project-settings-actions">
          <button className="tool-button" disabled={disabled || running || !request.ok} onClick={() => onEstimate(request.request)} type="button">估算费用</button>
          <button className="tool-button accent" disabled={disabled || running || !canStart} onClick={() => onStart(request.request)} type="button"><WandSparkles size={16} />启动二审</button>
          {running ? <button className="tool-button danger" disabled={disabled} onClick={() => onCancel(semantic.run.run_id)} type="button"><Square size={15} />取消</button> : null}
          {['failed', 'canceled', 'interrupted'].includes(semantic.run?.status) ? <button className="tool-button" disabled={disabled} onClick={() => onRetry(semantic.run.run_id)} type="button"><RefreshCw size={15} />重试未完成批次</button> : null}
        </div>
      </section>
    </>
  );
}

function AuditComparison({ comparison }) {
  const warning = comparison.comparability === 'exact'
    ? '输入和范围一致，可将差异用于模型能力比较。'
    : comparison.comparability === 'content_changed'
      ? '正文或计划已经变化，差异不能归因于模型升级。'
      : comparison.comparability === 'scope_changed'
        ? '审计范围不同，仅展示范围交集。'
        : '两份报告不可逐项比较。';
  return (
    <div className="audit-comparison">
      <div className={`settings-note ${comparison.comparability === 'exact' ? '' : 'warning'}`}>{warning}</div>
      {Object.entries(comparison.groups).map(([key, findings]) => findings.length ? (
        <div className="audit-comparison-group" key={key}>
          <strong>{comparisonLabels[key]}（{findings.length}）</strong>
          {key === 'unchanged' ? null : findings.map((item, index) => <div className="finding-row audit-finding-row" key={item.fingerprint || `${key}-${index}`}>{item.message || item.after?.message || item.before?.message || item.candidate?.message || item.base?.message || item.fingerprint}</div>)}
        </div>
      ) : null)}
    </div>
  );
}
