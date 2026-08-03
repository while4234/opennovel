import { useEffect, useLayoutEffect, useMemo, useState } from 'react';
import { statusLabel } from './manuscript-state.js';

const TREE_WINDOW = 120;

export function ManuscriptTree({ nodes = [], selectedId, onSelect, onClose, onExpandBetween }) {
  const [expanded, setExpanded] = useState(() => new Set());
  const [windowStart, setWindowStart] = useState(0);
  const [focusedIndex, setFocusedIndex] = useState(0);
  const [pendingFocus, setPendingFocus] = useState(null);
  useEffect(() => {
    setExpanded((previous) => {
      if (previous.size) return previous;
      return new Set(nodes.flatMap((volume) => [volume.stable_id, ...(volume.children || []).map((arc) => arc.stable_id)]));
    });
  }, [nodes]);
  const visibleRows = useMemo(() => {
    const rows = [];
    for (const volume of nodes) {
      rows.push({ ...volume, level: 1, expandable: true });
      if (!expanded.has(volume.stable_id)) continue;
      for (const arc of volume.children || []) {
        rows.push({ ...arc, level: 2, expandable: true });
        if (!expanded.has(arc.stable_id)) continue;
        for (const chapter of arc.children || []) rows.push({ ...chapter, level: 3, expandable: false });
      }
    }
    return rows;
  }, [expanded, nodes]);
  useEffect(() => {
    const index = visibleRows.findIndex((row) => row.stable_id === selectedId);
    if (index >= 0) {
      setFocusedIndex(index);
      setWindowStart((previous) => index < previous || index >= previous + TREE_WINDOW ? Math.floor(index / TREE_WINDOW) * TREE_WINDOW : previous);
    }
  }, [selectedId, visibleRows]);
  useLayoutEffect(() => {
    if (pendingFocus === null) return;
    const target = document.querySelector(`[data-tree-index="${pendingFocus}"]`);
    if (!target) return;
    target.focus();
    setPendingFocus(null);
  }, [pendingFocus, windowStart]);
  function toggle(id) {
    setExpanded((previous) => {
      const next = new Set(previous);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  }
  function keyDown(event, row, index) {
    if (row.expandable && (event.key === 'ArrowRight' || event.key === 'ArrowLeft')) {
      event.preventDefault();
      if (event.key === 'ArrowRight' && !expanded.has(row.stable_id)) toggle(row.stable_id);
      if (event.key === 'ArrowLeft' && expanded.has(row.stable_id)) toggle(row.stable_id);
      return;
    }
    let next = index;
    if (event.key === 'ArrowDown') next = Math.min(index + 1, visibleRows.length - 1);
    else if (event.key === 'ArrowUp') next = Math.max(index - 1, 0);
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = visibleRows.length - 1;
    else if ((event.key === 'Enter' || event.key === ' ') && row.kind === 'chapter') return onSelect(row.stable_id);
    else return;
    event.preventDefault();
    setFocusedIndex(next);
    if (next < windowStart || next >= windowStart + TREE_WINDOW) {
      setWindowStart(Math.min(Math.floor(next / TREE_WINDOW) * TREE_WINDOW, Math.max(0, visibleRows.length - TREE_WINDOW)));
      setPendingFocus(next);
      return;
    }
    document.querySelector(`[data-tree-index="${next}"]`)?.focus();
  }
  const visible = visibleRows.slice(windowStart, windowStart + TREE_WINDOW);
  const parentByID = new Map();
  for (const volume of nodes) for (const arc of volume.children || []) {
    parentByID.set(arc.stable_id, volume.stable_id);
    for (const chapter of arc.children || []) parentByID.set(chapter.stable_id, arc.stable_id);
  }
  const visibleIDs = new Set(visible.map((row) => row.stable_id));
  for (const row of visible) {
    let parent = parentByID.get(row.stable_id);
    while (parent) { visibleIDs.add(parent); parent = parentByID.get(parent); }
  }
  function renderNodes(items, level) {
    return items.filter((row) => visibleIDs.has(row.stable_id)).map((row) => {
      const index = visibleRows.findIndex((item) => item.stable_id === row.stable_id);
      const children = row.children || [], expandable = children.length > 0;
      const isChapter = row.kind === 'chapter' || (!expandable && level === 3);
      return <div role="none" key={row.stable_id}>
        <button type="button" role="treeitem" data-tree-index={index} aria-level={level} aria-expanded={expandable ? expanded.has(row.stable_id) : undefined} aria-selected={isChapter ? row.stable_id === selectedId : undefined} tabIndex={index === focusedIndex ? 0 : -1} onFocus={() => setFocusedIndex(index)} onKeyDown={(event) => keyDown(event, { ...row, level, expandable }, index)} onClick={() => isChapter ? onSelect(row.stable_id) : toggle(row.stable_id)} style={{ paddingInlineStart: `${(level - 1) * 18 + 8}px` }}>
          <span>{expandable ? (expanded.has(row.stable_id) ? '▾ ' : '▸ ') : ''}{isChapter ? `第 ${row.display_order} 章 · ` : ''}{row.display_label}</span>
          {isChapter ? <small>{statusLabel(row.state)}{row.has_candidate ? ' · 候选稿' : ''}</small> : null}
          {row.target_display ? <small className="manuscript-target-label">{row.target_display}</small> : null}
          {row.source_display ? <small className="manuscript-source-label">{row.source_display}</small> : null}
        </button>
        {isChapter && onExpandBetween ? <button type="button" className="manuscript-expansion-between" aria-label={`在第 ${row.display_order} 章后补充剧情`} onClick={() => onExpandBetween(row.stable_id)}>＋ 补充剧情</button> : null}
        {expandable && expanded.has(row.stable_id) ? <div role="group" aria-label={`${row.display_label} 子项`}>{renderNodes(children, level + 1)}</div> : null}
      </div>;
    });
  }
  return <nav className="manuscript-tree" aria-label="稿件目录">
    {onClose ? <button className="manuscript-tree-close" data-manuscript-drawer-initial type="button" onClick={onClose}>关闭目录</button> : null}
    <div role="tree" aria-label="卷、故事弧与章节" aria-rowcount={visibleRows.length}>
      {windowStart > 0 ? <button type="button" onClick={() => setWindowStart(Math.max(0, windowStart - TREE_WINDOW))}>上一组目录</button> : null}
      {renderNodes(nodes, 1)}
      {windowStart + TREE_WINDOW < visibleRows.length ? <button type="button" onClick={() => setWindowStart(windowStart + TREE_WINDOW)}>下一组目录</button> : null}
    </div>
  </nav>;
}
