import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { ChevronDown, Search } from 'lucide-react';
import { chapterOptionLabel, filterChapters } from './chapter-search.js';

export function ChapterCombobox({ chapters = [], selectedId = '', disabled = false, onSelect }) {
  const selected = chapters.find((chapter) => chapter.stable_id === selectedId) || null;
  const selectedLabel = chapterOptionLabel(selected);
  const [query, setQuery] = useState('');
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);
  const triggerRef = useRef(null);
  const searchRef = useRef(null);
  const listboxId = useId();
  const triggerId = `${listboxId}-trigger`;
  const optionId = (index) => `${listboxId}-option-${index}`;
  const matches = useMemo(() => filterChapters(chapters, query), [chapters, query]);

  useEffect(() => {
    if (!open) return;
    searchRef.current?.focus();
  }, [open]);

  useEffect(() => {
    if (!open || !matches.length) return;
    const selectedIndex = matches.findIndex((chapter) => chapter.stable_id === selectedId);
    setActiveIndex(selectedIndex >= 0 ? selectedIndex : 0);
  }, [open, query, selectedId]);

  function close({ restoreFocus = false } = {}) {
    setOpen(false);
    setQuery('');
    if (restoreFocus) queueMicrotask(() => triggerRef.current?.focus());
  }

  function openList() {
    if (disabled) return;
    setQuery('');
    setOpen(true);
  }

  function choose(chapter) {
    if (!chapter) return;
    onSelect?.(chapter.stable_id);
    close({ restoreFocus: true });
  }

  function searchKeyDown(event) {
    if (event.key === 'Escape') {
      if (open) event.preventDefault();
      close({ restoreFocus: true });
      return;
    }
    if (event.key === 'Enter') {
      if (!open || !matches.length) return;
      event.preventDefault();
      choose(matches[activeIndex] || matches[0]);
      return;
    }
    let next = activeIndex;
    if (event.key === 'ArrowDown') next = Math.min(activeIndex + 1, matches.length - 1);
    else if (event.key === 'ArrowUp') next = Math.max(activeIndex - 1, 0);
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = matches.length - 1;
    else return;
    event.preventDefault();
    setActiveIndex(Math.max(0, next));
    queueMicrotask(() => document.getElementById(optionId(Math.max(0, next)))?.scrollIntoView({ block: 'nearest' }));
  }

  return <div className="chapter-combobox">
    <label htmlFor={triggerId}>选择章节</label>
    <button
      ref={triggerRef}
      id={triggerId}
      className="chapter-combobox-trigger"
      type="button"
      role="combobox"
      aria-haspopup="listbox"
      aria-controls={listboxId}
      aria-expanded={open}
      disabled={disabled}
      onClick={() => open ? close() : openList()}
      onBlur={() => globalThis.setTimeout(() => { if (!document.activeElement?.closest?.('.chapter-combobox')) close(); }, 0)}
      onKeyDown={(event) => {
        if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
        event.preventDefault();
        openList();
      }}
    >
      <span className={selectedLabel ? '' : 'placeholder'}>{selectedLabel || '请选择章节'}</span>
      <ChevronDown size={16} aria-hidden="true" />
    </button>
    {open ? <div className="chapter-combobox-popover">
      <div className="chapter-combobox-search">
        <Search size={15} aria-hidden="true" />
        <input
          ref={searchRef}
          type="search"
          aria-label="搜索章节"
          aria-controls={listboxId}
          aria-activedescendant={matches.length ? optionId(activeIndex) : undefined}
          autoComplete="off"
          placeholder="输入章号或标题搜索"
          value={query}
          onChange={(event) => { setQuery(event.target.value); setActiveIndex(0); }}
          onKeyDown={searchKeyDown}
        />
      </div>
      <div id={listboxId} role="listbox" aria-label="章节选择结果">
        {matches.length ? matches.map((chapter, index) => <button
          id={optionId(index)}
          key={chapter.stable_id}
          type="button"
          role="option"
          aria-selected={chapter.stable_id === selectedId}
          className={index === activeIndex ? 'active' : ''}
          onMouseDown={(event) => event.preventDefault()}
          onMouseEnter={() => setActiveIndex(index)}
          onClick={() => choose(chapter)}
        >
          <span>{chapterOptionLabel(chapter)}</span>
          <small>{chapter.state || '未标记'}</small>
        </button>) : <div className="chapter-combobox-empty" role="status">未找到匹配章节</div>}
      </div>
    </div> : null}
  </div>;
}
