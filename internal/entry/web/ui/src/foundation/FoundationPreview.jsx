export function FoundationPreview({ preview, dirty, onPreview, onApply, disabled, canApply }) {
  if (!preview) return <section className="foundation-preview-empty" aria-labelledby="foundation-preview-heading">
    <h2 id="foundation-preview-heading">差异与影响预览</h2>
    <p>正式差异、影响范围、证据与是否可应用全部由服务端计算。前端不会用本地简化 diff 替代。</p>
    <button className="tool-button accent" disabled={disabled || !dirty} type="button" onClick={onPreview}>生成服务端预览</button>
  </section>;

  const diff = preview.diff || {};
  const impact = preview.impact || {};
  const validation = preview.validation || {};
  const adaptation = impact.adaptation;
  return <section aria-labelledby="foundation-preview-heading">
    <div className="foundation-section-head"><div><h2 id="foundation-preview-heading">差异与影响预览</h2><p>preview ID：{preview.id}</p></div><button className="tool-button" disabled={disabled} type="button" onClick={onPreview}>重新预览</button></div>
    <div aria-live="polite" className={`foundation-validation ${validation.valid ? 'valid' : 'invalid'}`} role="status">
      <strong>{validation.valid ? '服务端校验通过' : '服务端校验未通过'}</strong>
      {validation.errors?.length ? <ul>{validation.errors.map((message) => <li key={message}>{message}</li>)}</ul> : null}
      {validation.warnings?.length ? <ul>{validation.warnings.map((message) => <li key={message}>{message}</li>)}</ul> : null}
    </div>
    <div className="foundation-preview-grid">
      <section className="foundation-card"><h3>规范化差异</h3>{diff.changes?.length ? <ul className="foundation-diff-list">{diff.changes.map((change) => <li key={`${change.entity_type}-${change.entity_id}-${change.kind}`}>
        <strong>{change.entity_type}:{change.entity_id}</strong><span>{change.kind} · {change.changed_fields?.join('、') || '实体级变更'}</span>
        <small>{[change.high_risk && '高风险', change.core_cast_affected && '影响 CoreCast', change.hard_rule_affected && 'hard rule'].filter(Boolean).join(' · ')}</small>
      </li>)}</ul> : <p className="muted">服务端未检测到语义变化。</p>}</section>
      <section className="foundation-card"><h3>影响范围</h3><dl className="foundation-metrics">
        <Metric label="证据级别" value={impact.evidence_level} /><Metric label="全书扩大" value={impact.full_book ? '是' : '否'} />
        <Metric label="受影响卷" value={join(impact.affected_volume_ids)} /><Metric label="受影响弧" value={join(impact.affected_arc_ids)} /><Metric label="受影响章" value={join(impact.affected_chapter_ids)} />
        <Metric label="重确认 CoreCast" value={impact.requires_core_cast_confirmation ? '需要' : '不需要'} /><Metric label="重确认 Foundation" value={impact.requires_foundation_confirmation ? '需要' : '不需要'} />
      </dl>
      <ImpactReasons values={impact.reasons} /><AuditScopes values={impact.required_audits} /></section>
    </div>
    {adaptation ? <section className="foundation-card"><h3>改编影响与 source-fidelity</h3><dl className="foundation-metrics">
      <Metric label="证据级别" value={adaptation.evidence_level} /><Metric label="来源锚点" value={join(adaptation.source_anchor_ids)} /><Metric label="契约" value={join(adaptation.contract_ids)} />
      <Metric label="重确认 CoreCast" value={yes(adaptation.requires_core_cast_reconfirmation)} /><Metric label="重确认 AdaptationPlan" value={yes(adaptation.requires_adaptation_plan_confirmation)} />
      <Metric label="来源忠实度审查" value={yes(adaptation.source_fidelity_review)} /><Metric label="目标一致性审查" value={yes(adaptation.target_consistency_review)} />
      <Metric label="角色映射审查" value={yes(adaptation.character_mapping_review)} /><Metric label="计划契约审查" value={yes(adaptation.plan_contract_review)} /><Metric label="大纲质量审查" value={yes(adaptation.outline_quality_review)} />
      <Metric label="影响提案 / 大纲" value={`${yes(adaptation.affected_proposal)} / ${yes(adaptation.affected_outline)}`} />
    </dl>{adaptation.expansion_reasons?.length ? <p>全书扩大原因：{adaptation.expansion_reasons.join('、')}</p> : null}</section> : null}
    <div className="foundation-apply-bar"><span>{preview.can_apply ? '服务端允许应用此预览' : `不可应用：${preview.readonly_reason || '请处理服务端校验或重新确认要求'}`}</span><button className="tool-button accent" disabled={disabled || !canApply} type="button" onClick={onApply}>应用当前 preview ID</button></div>
  </section>;
}

function ImpactReasons({ values = [] }) { return values.length ? <div><h4>影响原因</h4><ul>{values.map((reason, index) => <li key={`${reason.code}-${index}`}>{reason.required ? '必需' : '建议'} · {reason.code} · {join(reason.entity_ids)}</li>)}</ul></div> : null; }
function AuditScopes({ values = [] }) { return values.length ? <div><h4>必须重跑的审查</h4><ul>{values.map((scope, index) => <li key={`${scope.scope}-${scope.scope_id}-${index}`}>{scope.required ? '必需' : '建议'} · {scope.scope}:{scope.scope_id || '—'} {scope.from_chapter ? `第 ${scope.from_chapter}-${scope.to_chapter || scope.from_chapter} 章` : ''}</li>)}</ul></div> : null; }
function Metric({ label, value }) { return <div><dt>{label}</dt><dd>{value || '—'}</dd></div>; }
function join(values) { return Array.isArray(values) && values.length ? values.join('、') : '—'; }
function yes(value) { return value ? '需要' : '不需要'; }
