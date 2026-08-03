import { describe, expect, it } from 'vitest';
import { flattenManuscriptTree, manuscriptCacheKey, mergeParagraphChunk, statusLabel } from './manuscript-state.js';

describe('manuscript workspace state', () => {
  it('uses stable IDs and keeps candidate separate from completion state', () => {
    const chapter = { stable_id: 'chapter-stable', state: 'completed', has_candidate: true };
    expect(flattenManuscriptTree([{ children: [{ children: [chapter] }] }])).toEqual([chapter]);
    expect(statusLabel(chapter.state)).toBe('已完成');
  });
  it('binds cache keys to project, artifact, view, version and signature', () => {
    expect(manuscriptCacheKey('p', 'c', 'prose', 'candidate', 'r1', 'sha')).toBe('p|c|prose|candidate|r1|sha');
  });
  it('retains every loaded paragraph so DOM virtualization cannot lose prose', () => {
    const merged = mergeParagraphChunk({ revision_id: 'revision-active', paragraphs: Array.from({ length: 150 }, (_, i) => `old-${i}`) }, { paragraphs: Array.from({ length: 40 }, (_, i) => `new-${i}`), next_cursor: 190 });
    expect(merged.paragraphs).toHaveLength(190);
    expect(merged.paragraphs.at(-1)).toBe('new-39');
    expect(merged.revision_id).toBe('revision-active');
  });
});
