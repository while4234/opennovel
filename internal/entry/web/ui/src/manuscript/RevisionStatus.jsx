export function RevisionStatus({ node }) { return <div className="revision-status" aria-live="polite">{node?.has_candidate ? '存在独立候选稿；当前正式稿未被替换。' : '正在显示当前正式稿。'}</div>; }
