export const MANUSCRIPT_TABS = [
  ['prose', '正文'], ['outline', '章节提纲'], ['volume', '所属分卷'],
  ['review', '审核'], ['history', '修订历史']
];

export function flattenManuscriptTree(nodes = []) {
  return nodes.flatMap((volume) => (volume.children || []).flatMap((arc) => arc.children || []));
}

export function manuscriptCacheKey(projectId, stableId, kind, view, version, signature) {
  return [projectId, stableId, kind, view, version || '', signature || ''].join('|');
}

export function mergeParagraphChunk(previous, incoming, reset = false) {
  const base = reset ? [] : (previous?.paragraphs || []);
  return { ...previous, ...incoming, paragraphs: [...base, ...(incoming?.paragraphs || [])] };
}

export function statusLabel(state) {
  return ({ unplanned: '未规划', planned: '已规划', writing: '写作中', working_draft: '工作稿', completed: '已完成', review_pending: '待审核', rewrite_pending: '待返工', revision_failed: '修订失败' })[state] || state;
}
