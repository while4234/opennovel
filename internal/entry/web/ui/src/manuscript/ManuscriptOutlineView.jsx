export function ManuscriptOutlineView({ artifact, busy, error, onRetry }) {
  const outline = artifact?.content;
  return <section aria-busy={busy}><h3>章节提纲</h3>{outline
    ? <><p>{outline.title}</p><p>{outline.core_event}</p><p>{outline.hook}</p><ul>{(outline.scenes || []).map((scene) => <li key={scene}>{scene}</li>)}</ul><small>提纲已校验</small></>
    : error
      ? <div role="alert"><p>章节提纲加载失败：{error}</p><button type="button" onClick={onRetry}>重新加载章节提纲</button></div>
      : <p>{busy ? '正在加载已校验的章节提纲…' : '正在恢复章节提纲加载…'}</p>}</section>;
}
