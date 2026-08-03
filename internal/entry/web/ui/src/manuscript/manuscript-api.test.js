import { afterEach, describe, expect, it, vi } from 'vitest';
import { invalidateManuscriptCache, invalidateManuscriptViews, loadManuscriptArtifact, loadManuscriptChunk, loadManuscriptHistory, loadManuscriptTree, loadManuscriptVersion } from './manuscript-api.js';

afterEach(() => { invalidateManuscriptCache('cache-project'); vi.unstubAllGlobals(); });

describe('manuscript production cache', () => {
  it('revalidates signed views with If-None-Match and returns cached data on 304', async () => {
    const data = { artifact: { kind: 'outline', stable_id: 'chapter-1', signature: 'signed', content: { title: 'outline' } } };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(data), { status: 200, headers: { ETag: '"signed"', 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(null, { status: 304 }));
    vi.stubGlobal('fetch', fetchMock);
    expect(await loadManuscriptArtifact('cache-project', 'outline', 'chapter-1')).toEqual(data);
    expect(await loadManuscriptArtifact('cache-project', 'outline', 'chapter-1')).toEqual(data);
    expect(fetchMock.mock.calls[1][1].headers).toEqual({ 'If-None-Match': '"signed"' });
  });

	it('evicts a tombstoned history version instead of reusing its validator', async () => {
		const valid = { chapter: { stable_id: 'c1', view: 'history', version_id: 'r1', content_signature: 'history-signed', paragraphs: ['kept'] } };
		const fetchMock = vi.fn()
			.mockResolvedValueOnce(new Response(JSON.stringify(valid), { status: 200, headers: { ETag: '"history-signed"', 'Content-Type': 'application/json' } }))
			.mockResolvedValueOnce(new Response(JSON.stringify({ error: { code: 'version_gone', message: 'gone' } }), { status: 410, headers: { 'Content-Type': 'application/json' } }))
			.mockResolvedValueOnce(new Response(JSON.stringify(valid), { status: 200, headers: { ETag: '"history-signed-2"', 'Content-Type': 'application/json' } }));
		vi.stubGlobal('fetch', fetchMock);
		await loadManuscriptVersion('cache-project', 'r1', 'c1');
		await expect(loadManuscriptVersion('cache-project', 'r1', 'c1')).rejects.toMatchObject({ status: 410 });
		await loadManuscriptVersion('cache-project', 'r1', 'c1');
		expect(fetchMock.mock.calls[1][1].headers).toEqual({ 'If-None-Match': '"history-signed"' });
		expect(fetchMock.mock.calls[2][1].headers).toEqual({});
	});

  it('drops project-scoped validators after a mutation or SSE invalidation', async () => {
    const fetchMock = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ artifact: { kind: 'review', stable_id: 'chapter-1', signature: 'fresh' } }), { status: 200, headers: { ETag: '"fresh"' } }));
    vi.stubGlobal('fetch', fetchMock);
    await loadManuscriptArtifact('cache-project', 'review', 'chapter-1');
    invalidateManuscriptCache('cache-project');
    await loadManuscriptArtifact('cache-project', 'review', 'chapter-1');
    expect(fetchMock.mock.calls[1][1].headers).toEqual({});
  });

  it('invalidates only the stable-ID and view set owned by each mutation scope', async () => {
    const fetchMock = vi.fn().mockImplementation(async (url) => {
      const stableId = String(url).includes('/c2/') ? 'c2' : 'c1';
      const view = String(url).includes('view=candidate') ? 'candidate' : 'current';
      const payload = String(url).includes('/history?')
        ? { items: [] }
        : { chapter: { stable_id: stableId, view, version_id: view === 'candidate' ? 'r1' : '', content_signature: 'signed', paragraphs: ['text'] } };
      return new Response(JSON.stringify(payload), { status: 200, headers: { ETag: '"signed"' } });
    });
    vi.stubGlobal('fetch', fetchMock);
    await loadManuscriptChunk('cache-project', 'c1', { view: 'current' });
    await loadManuscriptChunk('cache-project', 'c1', { view: 'candidate', version: 'r1' });
    await loadManuscriptChunk('cache-project', 'c2', { view: 'candidate', version: 'r1' });
    await loadManuscriptHistory('cache-project', 'c1');
    invalidateManuscriptViews('cache-project', { scope: 'generation', stable_id: 'c1' });
    await loadManuscriptChunk('cache-project', 'c1', { view: 'current' });
    await loadManuscriptChunk('cache-project', 'c1', { view: 'candidate', version: 'r1' });
    await loadManuscriptChunk('cache-project', 'c2', { view: 'candidate', version: 'r1' });
    await loadManuscriptHistory('cache-project', 'c1');
    const headers = fetchMock.mock.calls.slice(-4).map((call) => call[1].headers);
    expect(headers).toEqual([{ 'If-None-Match': '"signed"' }, {}, { 'If-None-Match': '"signed"' }, { 'If-None-Match': '"signed"' }]);
    invalidateManuscriptViews('cache-project', { scope: 'unknown', stable_id: 'c1' });
    await loadManuscriptChunk('cache-project', 'c1', { view: 'current' });
    expect(fetchMock.mock.calls.at(-1)[1].headers).toEqual({ 'If-None-Match': '"signed"' });
  });

  it('maps chapter structure events to their owning volume and invalidates active and affected tree badges', async () => {
    const tree = { nodes: [{ kind: 'volume', stable_id: 'v1', children: [{ kind: 'arc', stable_id: 'a1', children: [{ kind: 'chapter', stable_id: 'c1' }, { kind: 'chapter', stable_id: 'c2' }] }] }] };
    const responseFor = (url) => {
      if (String(url).endsWith('/tree')) return tree;
      const match = String(url).match(/artifacts\/(outline|volume|review)\/([^?]+)/);
      if (match) return { artifact: { kind: match[1], stable_id: decodeURIComponent(match[2]), signature: 'signed' } };
      return { chapter: { stable_id: String(url).includes('/c2/') ? 'c2' : 'c1', view: String(url).includes('view=candidate') ? 'candidate' : 'current', version_id: String(url).includes('view=candidate') ? 'r1' : '', content_signature: 'signed', paragraphs: ['text'] } };
    };
    const fetchMock = vi.fn().mockImplementation(async (url) => new Response(JSON.stringify(responseFor(url)), { status: 200, headers: { ETag: '"signed"' } }));
    vi.stubGlobal('fetch', fetchMock);
    await loadManuscriptTree('cache-project');
    await loadManuscriptArtifact('cache-project', 'outline', 'c1');
    await loadManuscriptArtifact('cache-project', 'volume', 'v1');
    await loadManuscriptChunk('cache-project', 'c1', { view: 'candidate', version: 'r1' });
    await loadManuscriptChunk('cache-project', 'c1', { view: 'current' });

    invalidateManuscriptViews('cache-project', { scope: 'structure_publish', stable_id: 'c1' });
    await loadManuscriptTree('cache-project');
    await loadManuscriptArtifact('cache-project', 'outline', 'c1');
    await loadManuscriptArtifact('cache-project', 'volume', 'v1');
    expect(fetchMock.mock.calls.slice(-3).map((call) => call[1].headers)).toEqual([{}, {}, {}]);

    invalidateManuscriptViews('cache-project', { scope: 'generation', stable_id: 'c1' });
    await loadManuscriptTree('cache-project');
    await loadManuscriptChunk('cache-project', 'c1', { view: 'candidate', version: 'r1' });
    await loadManuscriptChunk('cache-project', 'c1', { view: 'current' });
    expect(fetchMock.mock.calls.slice(-3).map((call) => call[1].headers)).toEqual([{}, {}, { 'If-None-Match': '"signed"' }]);

    invalidateManuscriptViews('cache-project', { scope: 'prose_publish', stable_id: 'c1' });
    await loadManuscriptTree('cache-project');
    await loadManuscriptChunk('cache-project', 'c1', { view: 'current' });
    await loadManuscriptChunk('cache-project', 'c1', { view: 'candidate', version: 'r1' });
    expect(fetchMock.mock.calls.slice(-3).map((call) => call[1].headers)).toEqual([{}, {}, { 'If-None-Match': '"signed"' }]);

    invalidateManuscriptViews('cache-project', { scope: 'cancel', stable_id: 'c1' });
    await loadManuscriptTree('cache-project');
    await loadManuscriptChunk('cache-project', 'c1', { view: 'candidate', version: 'r1' });
    await loadManuscriptChunk('cache-project', 'c1', { view: 'current' });
    expect(fetchMock.mock.calls.slice(-3).map((call) => call[1].headers)).toEqual([{}, {}, { 'If-None-Match': '"signed"' }]);
  });

  it('does not replace the last successful chapter cache with an empty 200 response', async () => {
    const valid = { chapter: { stable_id: 'c1', view: 'current', content_signature: 'signed', paragraphs: ['kept'] } };
    const empty = { chapter: { stable_id: 'c1', view: 'current', content_signature: 'empty', paragraphs: [] } };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(valid), { status: 200, headers: { ETag: '"signed"' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify(empty), { status: 200, headers: { ETag: '"empty"' } }))
      .mockResolvedValueOnce(new Response(null, { status: 304 }));
    vi.stubGlobal('fetch', fetchMock);
    expect(await loadManuscriptChunk('cache-project', 'c1')).toEqual(valid);
    await expect(loadManuscriptChunk('cache-project', 'c1')).rejects.toThrow('explicit empty contract');
    expect(await loadManuscriptChunk('cache-project', 'c1')).toEqual(valid);
    expect(fetchMock.mock.calls[2][1].headers).toEqual({ 'If-None-Match': '"signed"' });
  });

  it('does not let a late response overwrite a newer validated cache entry', async () => {
    let resolveOld;
    const old = new Promise((resolve) => { resolveOld = resolve; });
    const newer = { chapter: { stable_id: 'c1', view: 'current', content_signature: 'new', paragraphs: ['new'] } };
    const older = { chapter: { stable_id: 'c1', view: 'current', content_signature: 'old', paragraphs: ['old'] } };
    const fetchMock = vi.fn()
      .mockReturnValueOnce(old)
      .mockResolvedValueOnce(new Response(JSON.stringify(newer), { status: 200, headers: { ETag: '"new"' } }))
      .mockResolvedValueOnce(new Response(null, { status: 304 }));
    vi.stubGlobal('fetch', fetchMock);
    const first = loadManuscriptChunk('cache-project', 'c1');
    expect(await loadManuscriptChunk('cache-project', 'c1')).toEqual(newer);
    resolveOld(new Response(JSON.stringify(older), { status: 200, headers: { ETag: '"old"' } }));
    expect(await first).toEqual(older);
    expect(await loadManuscriptChunk('cache-project', 'c1')).toEqual(newer);
  });

  it('does not let a late tree response replace the chapter-to-volume invalidation map', async () => {
    let resolveOld;
    const oldResponse = new Promise((resolve) => { resolveOld = resolve; });
    const treeFor = (volumeId) => ({ nodes: [{ kind: 'volume', stable_id: volumeId, children: [{ kind: 'arc', stable_id: `a-${volumeId}`, children: [{ kind: 'chapter', stable_id: 'c1' }] }] }] });
    const fetchMock = vi.fn()
      .mockReturnValueOnce(oldResponse)
      .mockResolvedValueOnce(new Response(JSON.stringify(treeFor('v-new')), { status: 200, headers: { ETag: '"tree-new"' } }));
    vi.stubGlobal('fetch', fetchMock);
    const oldLoad = loadManuscriptTree('cache-project');
    expect(await loadManuscriptTree('cache-project')).toEqual(treeFor('v-new'));
    resolveOld(new Response(JSON.stringify(treeFor('v-old')), { status: 200, headers: { ETag: '"tree-old"' } }));
    await oldLoad;

    fetchMock.mockImplementation(async (url) => {
      const stableId = String(url).includes('v-new') ? 'v-new' : 'v-old';
      return new Response(JSON.stringify({ artifact: { kind: 'volume', stable_id: stableId, signature: 'signed' } }), { status: 200, headers: { ETag: '"signed"' } });
    });
    await loadManuscriptArtifact('cache-project', 'volume', 'v-old');
    await loadManuscriptArtifact('cache-project', 'volume', 'v-new');
    invalidateManuscriptViews('cache-project', { scope: 'structure_publish', stable_id: 'c1' });
    await loadManuscriptArtifact('cache-project', 'volume', 'v-old');
    await loadManuscriptArtifact('cache-project', 'volume', 'v-new');
    expect(fetchMock.mock.calls.slice(-2).map((call) => call[1].headers)).toEqual([{ 'If-None-Match': '"signed"' }, {}]);
  });
});
