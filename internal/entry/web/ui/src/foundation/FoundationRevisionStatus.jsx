export function FoundationRevisionStatus({ server, status, onRefresh, onRetry, onOpenReview, busy }) {
  const revision = server.activeRevision;
  return <section aria-labelledby="foundation-revision-heading">
    <div className="foundation-section-head"><div><h2 id="foundation-revision-heading">修订 / 重新审查状态</h2><p>状态来自持久化 runtime；retry 不重新提交 candidate，也不会创建新 preview。</p></div><button className="tool-button" disabled={busy} type="button" onClick={onRefresh}>刷新服务端状态</button></div>
    {!revision ? <div className="empty-state">当前没有活动 Foundation 修订。</div> : <div className="foundation-card">
      <dl className="foundation-metrics"><Metric label="UI 状态" value={status} /><Metric label="服务端阶段" value={revision.stage} /><Metric label="revision ID" value={revision.revision_id} /><Metric label="session ID" value={revision.session_id} /><Metric label="preview ID" value={revision.preview_id} /><Metric label="attempt" value={revision.attempt} /><Metric label="generation" value={revision.generation} /><Metric label="恢复边界" value={revision.resume_stage} /><Metric label="更新时间" value={revision.updated_at} /></dl>
      {revision.publication ? <p>目标 Foundation 已发布 revision {revision.publication.foundation_revision}，后续恢复不会再次发布。</p> : null}
      {revision.last_error ? <div className="error-banner compact" role="alert"><strong>{revision.last_error_class || '修订失败'}</strong><span>{revision.last_error}</span></div> : null}
      {revision.stage === 'failed' ? <button className="tool-button accent" disabled={busy || !server.allowedOperations.includes('retry')} type="button" onClick={onRetry}>从持久化安全边界重试</button> : null}
      {['awaiting_outline_approval', 'completed'].includes(revision.stage) ? <button className="tool-button accent" type="button" onClick={onOpenReview}>前往现有提案 / 大纲确认</button> : null}
    </div>}
  </section>;
}
function Metric({ label, value }) { return <div><dt>{label}</dt><dd>{value || '—'}</dd></div>; }
