import { describe, expect, it } from 'vitest';
import { filterChapters, parseChapterNumberQuery, parseChineseNumber } from './chapter-search.js';

const chapters = [
  { stable_id: 'c1', display_order: 1, display_label: '死亡余温与重生' },
  { stable_id: 'c2', display_order: 2, display_label: '雨夜追凶' },
  { stable_id: 'c96', display_order: 96, display_label: '终局回响' }
];

describe('chapter search', () => {
  it.each([
    ['1', 1], ['第1章', 1], ['１', 1], ['一', 1], ['第一章', 1], ['第二章', 2],
    ['第九十六章', 96], ['一百零二', 102], ['一万零二', 10002]
  ])('parses %s as chapter %d', (value, expected) => {
    expect(parseChapterNumberQuery(value)).toBe(expected);
  });

  it('parses Chinese values without accepting invalid or zero values', () => {
    expect(parseChineseNumber('两千零二十六')).toBe(2026);
    expect(parseChapterNumberQuery('第零章')).toBeNull();
    expect(parseChapterNumberQuery('第一回')).toBeNull();
  });

  it('prefers exact chapter-number matches and otherwise searches titles', () => {
    expect(filterChapters(chapters, '第九十六章').map((chapter) => chapter.stable_id)).toEqual(['c96']);
    expect(filterChapters(chapters, '雨夜').map((chapter) => chapter.stable_id)).toEqual(['c2']);
    expect(filterChapters(chapters, '999')).toEqual([]);
    expect(filterChapters(chapters, '')).toEqual(chapters);
  });
});
