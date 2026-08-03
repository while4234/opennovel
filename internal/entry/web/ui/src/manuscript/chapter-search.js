const CHINESE_DIGITS = new Map([
  ['零', 0], ['〇', 0], ['一', 1], ['二', 2], ['两', 2], ['三', 3], ['四', 4],
  ['五', 5], ['六', 6], ['七', 7], ['八', 8], ['九', 9]
]);

const CHINESE_UNITS = new Map([['十', 10], ['百', 100], ['千', 1000], ['万', 10000]]);

export function normalizeChapterQuery(value = '') {
  return String(value).normalize('NFKC').trim().toLowerCase();
}

export function parseChineseNumber(value = '') {
  const text = String(value).trim();
  if (!text || [...text].some((character) => !CHINESE_DIGITS.has(character) && !CHINESE_UNITS.has(character))) return null;
  if (![...text].some((character) => CHINESE_UNITS.has(character))) {
    return Number([...text].map((character) => CHINESE_DIGITS.get(character)).join(''));
  }
  let total = 0;
  let section = 0;
  let number = 0;
  for (const character of text) {
    if (CHINESE_DIGITS.has(character)) {
      number = CHINESE_DIGITS.get(character);
      continue;
    }
    const unit = CHINESE_UNITS.get(character);
    if (unit === 10000) {
      section += number;
      total += (section || 1) * unit;
      section = 0;
      number = 0;
      continue;
    }
    section += (number || 1) * unit;
    number = 0;
  }
  return total + section + number;
}

export function parseChapterNumberQuery(value = '') {
  const query = normalizeChapterQuery(value).replace(/\s+/g, '');
  const match = query.match(/^第?(.+?)章?$/u);
  if (!match) return null;
  const numeric = match[1];
  if (/^\d+$/u.test(numeric)) {
    const result = Number(numeric);
    return Number.isSafeInteger(result) && result > 0 ? result : null;
  }
  const result = parseChineseNumber(numeric);
  return Number.isSafeInteger(result) && result > 0 ? result : null;
}

export function chapterOptionLabel(chapter) {
  if (!chapter) return '';
  return `第 ${Number(chapter.display_order || 0)} 章 · ${String(chapter.display_label || '').trim()}`;
}

export function filterChapters(chapters = [], value = '') {
  const query = normalizeChapterQuery(value);
  const ordered = [...chapters].sort((left, right) => Number(left.display_order || 0) - Number(right.display_order || 0));
  if (!query) return ordered;
  const chapterNumber = parseChapterNumberQuery(query);
  if (chapterNumber !== null) return ordered.filter((chapter) => Number(chapter.display_order) === chapterNumber);
  return ordered.filter((chapter) => chapterOptionLabel(chapter).normalize('NFKC').toLowerCase().includes(query));
}
