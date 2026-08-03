import { ManuscriptReader } from './ManuscriptReader.jsx';
export function RevisionCompare({ current, candidate, busy, currentBusy = busy, candidateBusy = busy, error, onMoreCurrent, onMoreCandidate, onRetry }) {
  return <div className="manuscript-compare" aria-label="当前稿与候选稿对照">
    <section><h3>当前正式稿</h3><ManuscriptReader chapter={current} busy={currentBusy} error={error} onMore={onMoreCurrent} onRetry={onRetry} /></section>
    <section className="manuscript-candidate"><h3>候选稿 <span>尚未发布</span></h3>{candidate ? <ManuscriptReader chapter={candidate} busy={candidateBusy} onMore={onMoreCandidate} /> : <p>当前章节没有候选稿。</p>}</section>
  </div>;
}
