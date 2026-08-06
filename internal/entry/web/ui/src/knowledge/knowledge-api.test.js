import { afterEach, describe, expect, it, vi } from 'vitest';
import { knowledgeAPI, libraryItemMeta, libraryItemName, libraryItems } from './knowledgeApi.js';
import { createScopeRequestGate } from './ProjectScopeSelector.jsx';

afterEach(() => vi.unstubAllGlobals());

function jsonResponse(body = {}) {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } });
}

describe('independent knowledge scope', () => {
  it('invalidates stale requests without touching application project state', () => {
    const gate = createScopeRequestGate();
    const first = gate.begin();
    const second = gate.begin();
    expect(gate.isCurrent(first)).toBe(false);
    expect(gate.isCurrent(second)).toBe(true);
    gate.invalidate();
    expect(gate.isCurrent(second)).toBe(false);
  });
});

describe('knowledge API contracts', () => {
  it('searches and loads novels with the existing method, path, and body', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ items: [] }));
    vi.stubGlobal('fetch', fetchMock);
    await knowledgeAPI.listNovels('旧 城');
    await knowledgeAPI.loadNovel('project/a', '长夜');
    expect(fetchMock.mock.calls[0][0]).toBe('/api/libraries/novels?q=%E6%97%A7%20%E5%9F%8E');
    expect(fetchMock.mock.calls[1][0]).toBe('/api/projects/project%2Fa/adapt/library/load');
    expect(fetchMock.mock.calls[1][1].method).toBe('POST');
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ name: '长夜' });
  });

  it('uploads and imports novel files with the backend multipart field names', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ source_file: {} }));
    vi.stubGlobal('fetch', fetchMock);
    const file = new File(['novel'], 'novel.txt', { type: 'text/plain' });
    await knowledgeAPI.uploadNovel('p', file);
    await knowledgeAPI.importNovel('p', file, 'knowledge-library');
    const upload = fetchMock.mock.calls[0];
    const imported = fetchMock.mock.calls[1];
    expect(upload[0]).toBe('/api/projects/p/adapt/source');
    expect(upload[1].method).toBe('POST');
    expect(upload[1].body).toBeInstanceOf(FormData);
    expect(upload[1].body.get('source').name).toBe('novel.txt');
    expect(imported[0]).toBe('/api/projects/p/import');
    expect(imported[1].body.get('source').name).toBe('novel.txt');
    expect(imported[1].body.get('from')).toBe('knowledge-library');
  });

  it('preserves profile library upload, load, import, and save contracts', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ items: [] }));
    vi.stubGlobal('fetch', fetchMock);
    const file = new File(['{}'], 'voice.json', { type: 'application/json' });
    await knowledgeAPI.uploadProfiles([file]);
    await knowledgeAPI.loadProfile('p/1', 'voice');
    await knowledgeAPI.importProfile('p/1', file);
    await knowledgeAPI.saveProfile('p/1', 'current voice');
    expect(fetchMock.mock.calls[0][0]).toBe('/api/libraries/simulation/upload');
    expect(fetchMock.mock.calls[0][1].body.getAll('files')[0].name).toBe('voice.json');
    expect(fetchMock.mock.calls[1][0]).toBe('/api/projects/p%2F1/simulate/library/load');
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ name: 'voice' });
    expect(fetchMock.mock.calls[2][0]).toBe('/api/projects/p%2F1/simulate/import');
    expect(fetchMock.mock.calls[2][1].body.get('profile').name).toBe('voice.json');
    expect(fetchMock.mock.calls[3][0]).toBe('/api/projects/p%2F1/simulate/library/save');
    expect(JSON.parse(fetchMock.mock.calls[3][1].body)).toEqual({ name: 'current voice' });
  });

  it('uses global or project-scoped observability GET queries', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ groups: [] }));
    vi.stubGlobal('fetch', fetchMock);
    await knowledgeAPI.usage({ projectId: 'p/1', groupBy: 'model' });
    await knowledgeAPI.recommendations({ projectId: '' });
    expect(fetchMock.mock.calls[0][0]).toBe('/api/observability/usage?project_id=p%2F1&group_by=model');
    expect(fetchMock.mock.calls[0][1]?.method).toBeUndefined();
    expect(fetchMock.mock.calls[1][0]).toBe('/api/observability/recommendations?');
  });
});

describe('knowledge library presentation', () => {
  it('normalizes backend list and metadata variants', () => {
    const entry = { Name: '雾城', ChapterCount: 12, Size: 2048, HealthState: 'healthy' };
    expect(libraryItems({ items: [entry] })).toEqual([entry]);
    expect(libraryItemName(entry)).toBe('雾城');
    expect(libraryItemMeta(entry, 'novels')).toContain('12 章');
    expect(libraryItemMeta(entry, 'novels')).toContain('2.0 KiB');
  });
});
