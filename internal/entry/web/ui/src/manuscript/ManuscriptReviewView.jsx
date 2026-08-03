function reviewFindingItems(findings) {
  if (!Array.isArray(findings)) return [];
  return findings.map((finding, index) => {
    if (typeof finding === 'string') return { key: `finding-${index}`, label: finding };
    const severity = String(finding?.severity || finding?.level || '').trim();
    const title = String(finding?.title || finding?.summary || finding?.message || '').trim();
    const action = String(finding?.action || finding?.suggestion || '').trim();
    return { key: `finding-${index}`, label: [severity, title, action].filter(Boolean).join('：') };
  }).filter((item) => item.label);
}

export function ManuscriptReviewView({ artifact, details = {}, busy, onOpen, onMore }) {
  const review = artifact?.content;
  return <section aria-busy={busy}><h3>审核</h3>{review ? <><p>状态：{review.status}</p><p>修订记录：{review.revisions?.length || 0}</p>
    <ul>{(review.audits || []).map((audit) => { const detail = details[audit.revision_id]?.content; const findings = reviewFindingItems(detail?.findings); return <li key={`${audit.revision_id}-${audit.signature}`}>
      <p>{audit.content_loaded ? '审核证据已加载' : '审核证据待加载'}</p>
      <button type="button" onClick={() => onOpen(audit)}>{detail ? '收起审核内容' : '加载审核报告与发现'}</button>
      {detail ? <section><p>{detail.report}</p>{findings.length ? <ul>{findings.map((finding) => <li key={finding.key}>{finding.label}</li>)}</ul> : <p>没有可展示的审核问题。</p>}</section> : null}
    </li>; })}</ul>{review.has_more ? <button type="button" disabled={busy} onClick={onMore}>加载更多审核记录</button> : null}<small>审核记录已校验</small></> : <p>正在加载已校验的审核元数据……</p>}</section>;
}
