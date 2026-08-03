export function RevisionHistory({ items, selected, preview, onOpen, onPreview, onConfirm, onMore, hasMore, loading }) {
  return <section aria-label="修订历史">{loading ? <p role="status">正在加载历史元数据…</p> : null}<ul className="manuscript-history">
    {items.map((item) => <li key={item.revision_id}><button type="button" onClick={() => onOpen(item)}>{item.updated_at} · {item.stage}</button>{selected?.revision_id === item.revision_id ? <button type="button" onClick={() => onPreview(item)}>预览恢复影响</button> : null}
      {preview?.source_revision_id === item.revision_id ? <section className="manuscript-restore-preview" role="alert"><strong>恢复影响确认</strong><p>{preview.impact}</p><small>恢复条件已校验，请确认影响后继续。</small><button type="button" onClick={() => onConfirm(item)}>确认并新建修订</button></section> : null}
    </li>)}
  </ul>{hasMore ? <button type="button" disabled={loading} onClick={onMore}>加载更多历史</button> : null}</section>;
}
