export function ManuscriptReader({ chapter, busy, error, onMore, onRetry, onPreviousChapter, previousChapterLabel, onNextChapter, nextChapterLabel }) {
  const paragraphs = chapter?.paragraphs || [];
  const totalParagraphs = Number(chapter?.total_paragraphs) || paragraphs.length;
  if (error && !chapter) return <div role="alert">{error}<button type="button" onClick={onRetry}>重试</button></div>;
  return <article className="manuscript-reader" aria-busy={busy}>
    {error ? <div role="alert">网络异常，保留上次成功正文。<button type="button" onClick={onRetry}>重试</button></div> : null}
    <div className="manuscript-window-status" aria-live="polite">
      <span>显示完整正文</span>
      <strong>{busy ? '正在自动加载' : '已加载'} {paragraphs.length} / {totalParagraphs} 段</strong>
      {chapter?.next_cursor != null && !busy ? <button type="button" onClick={onMore}>继续加载</button> : null}
    </div>
    <div className="manuscript-prose">{paragraphs.map((paragraph, index) => <p data-paragraph-index={index + 1} key={`${index}-${paragraph.slice(0, 20)}`}>{paragraph}</p>)}</div>
    <div className="manuscript-chapter-end" role="status">
      {paragraphs.length < totalParagraphs ? <p>正文仍在加载，请稍候。</p> : null}
      <div className="manuscript-chapter-navigation" aria-label="章末章节导航">
        <button type="button" className="tool-button" disabled={!onPreviousChapter || paragraphs.length < totalParagraphs} onClick={onPreviousChapter}>上一章{previousChapterLabel ? `：${previousChapterLabel}` : ''}</button>
        <button type="button" className="tool-button" disabled={!onNextChapter || paragraphs.length < totalParagraphs} onClick={onNextChapter}>下一章{nextChapterLabel ? `：${nextChapterLabel}` : ''}</button>
      </div>
    </div>
  </article>;
}
