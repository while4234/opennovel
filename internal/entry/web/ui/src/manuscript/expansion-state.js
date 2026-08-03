export const EXPANSION_STEPS = [
  '影响确认', '结构候选', '结构多层审核', '人工确认', '提纲候选', '章/弧/卷审核',
  '人工确认', '正文候选', '文学/连续性/mode audit', '人工确认', '后处理', '恢复创作'
];

export function expansionKey(scope = 'expansion') {
  return `${scope}:${globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`}`;
}

export function expansionLocationLabel(location) {
  return ({ inside: '扩写当前章', before: '在当前章前插入', after: '在当前章后插入', between: '在两章之间插入', end_arc: '在当前弧末增加', end_volume: '在当前卷末增加', book_end: '完本后续写' })[location] || location;
}

export function expansionFormLabel(form) {
  return ({ expand_current: '扩写当前章', insert_one: '插入一章', insert_multiple: '增加若干章', new_arc: '新增小弧', new_volume: '新增一卷', epilogue: '追加尾声' })[form] || form;
}
