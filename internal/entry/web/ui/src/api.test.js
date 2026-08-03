import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  addGlobalProviderModel,
  analyzeAdaptationSource,
  analyzeSimulation,
  buildAdaptationProposal,
  cancelSemanticAdaptationAudit,
  clearProjectTrash,
  approveContinuationOutlines,
  approveContinuationProposal,
  approveContinuationVolumes,
  confirmAdaptationProposal,
  confirmAdaptationProposalDetails,
  cloneProject,
  compareAdaptationAuditRuns,
  createProject,
  deleteGlobalProviderModel,
  deleteProviderModel,
  discoverGlobalProviderModels,
  discoverProjectProviderModels,
  downloadSimulationSource,
  emptyTrashProjects,
  exportProjectDownload,
  generateContinuationOutlines,
  generateContinuationProposal,
  getAdaptationAudit,
  getAdaptationAuditRun,
  getSemanticAdaptationAudit,
  getSemanticAdaptationAuditReport,
  getChapter,
  getSnapshot,
  getSummarySnapshot,
  getCodexAuthStatus,
  getGlobalModels,
  getContinuation,
  getProjectEvents,
  inheritGlobalModel,
  inheritProjectModel,
  listNovelLibrary,
  listAdaptationAuditRuns,
  listProjectTrash,
  listSimulationLibrary,
  listStyles,
  listTrashProjects,
  previewProjectRollback,
  renameProject,
  restoreTrashProject,
  retryContinuation,
  retrySemanticAdaptationAudit,
  runAdaptationAudit,
  estimateSemanticAdaptationAudit,
  resumeCoCreate,
  reviseAdaptationProposal,
  reviseAdaptationVolumeReview,
  reviseChapter,
  reviseChapterOutline,
  reviseCoCreatePlanning,
  reviseCoCreate,
  reviseContinuationOutlines,
  reviseContinuationProposal,
  reviseContinuationVolumes,
  resolveCoCreateDecision,
  resolveCoCreateDecisions,
  rollbackProject,
  saveNovelToLibrary,
  searchSimulationSources,
  sendCoCreate,
  setGlobalCoCreateMaxTokens,
  setGlobalCoCreateTimeout,
  setGlobalRetrySettings,
  setGlobalThinking,
  setProjectCoCreateMaxTokens,
  setProjectCoCreateTimeout,
  setProjectRetrySettings,
  setProjectSimulationMode,
  setProjectStyle,
  startContinuation,
  startSemanticAdaptationAudit,
  applyAdaptationAudit,
  startGrokLogin,
  switchGlobalDefaultModel,
  switchGlobalModel,
  switchProjectModel,
  testGlobalProviderModel,
  testProjectProviderModel,
  trashProject,
  uploadCodexAuthFile,
  uploadContinuationSource
} from './api.js';

function mockJSONResponse(body = {}) {
  return {
    ok: true,
    text: () => Promise.resolve(JSON.stringify(body))
  };
}

function mockBlobResponse(body = 'book') {
  return {
    ok: true,
    headers: new Headers({
      'x-ainovel-export-name': 'book.txt',
      'x-ainovel-export-chapters': '59',
      'x-ainovel-export-bytes': '893100',
      'x-ainovel-export-skipped': '2,4'
    }),
    blob: () => Promise.resolve(new Blob([body], { type: 'text/plain' }))
  };
}

describe('web API helpers', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('keeps project-open and recovery snapshots on the compact route', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await getSummarySnapshot('project/1');
    await getSnapshot('project/1');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/projects/project%2F1/snapshot?detail=summary', expect.objectContaining({
      headers: {}
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project%2F1/snapshot', expect.objectContaining({
      headers: {}
    }));
  });

  it('sends project rename, clone, and trash requests to the project resource', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await renameProject('project-1', 'Renamed');
    await cloneProject('project/1', 'Renamed - 副本');
    await trashProject('project-1');
    await listTrashProjects();
    await restoreTrashProject('project-1');
    await emptyTrashProjects();

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/projects/project-1', expect.objectContaining({
      method: 'PATCH',
      body: JSON.stringify({ name: 'Renamed' })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project%2F1/clone', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ name: 'Renamed - 副本' })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/projects/project-1', expect.objectContaining({
      method: 'DELETE'
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/trash/projects', expect.objectContaining({
      headers: {}
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(5, '/api/trash/projects/project-1/restore', expect.objectContaining({
      method: 'POST'
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(6, '/api/trash/projects', expect.objectContaining({
      method: 'DELETE'
    }));
  });

  it('keeps legacy style and trash helper routes available', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await listStyles();
    await createProject('New Book', 'fantasy');
    await setProjectStyle('project-1', 'romance');
    await setProjectSimulationMode('project-1', 'reinforced');
    await listProjectTrash();
    await clearProjectTrash();

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/styles', expect.objectContaining({
      headers: {}
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ name: 'New Book', style: 'fantasy' })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/projects/project-1/style', expect.objectContaining({
      method: 'PUT',
      body: JSON.stringify({ style: 'romance' })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/projects/project-1/simulation-mode', expect.objectContaining({
      method: 'PUT',
      body: JSON.stringify({ simulation_mode: 'reinforced' })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(5, '/api/projects/trash', expect.objectContaining({
      headers: {}
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(6, '/api/projects/trash', expect.objectContaining({
      method: 'DELETE'
    }));
  });

  it('uses rollback preview and irreversible confirm routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await previewProjectRollback('project-1');
    await rollbackProject('project-1', { confirm: true, preview_hash: 'hash-1' });

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/projects/project-1/rollback/preview', expect.objectContaining({
      headers: {}
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/rollback', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ confirm: true, preview_hash: 'hash-1' })
    }));
  });

  it('encodes library search queries', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ items: [] }));

    await listSimulationLibrary('cool profile');
    await listNovelLibrary('source book');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/libraries/simulation?q=cool%20profile', expect.objectContaining({
      headers: {}
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/libraries/novels?q=source%20book', expect.objectContaining({
      headers: {}
    }));
  });

  it('sends simulation source search and download payloads', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ results: [] }));

    await searchSimulationSources('project-1', '异兽迷城');
    await downloadSimulationSource('project-1', 'result-1');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/projects/project-1/simulate/search', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ file_name: '异兽迷城' })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/simulate/search/download', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ result_id: 'result-1' })
    }));
  });

  it('requests an incremental adaptation source upgrade explicitly', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ accepted: true }));

    await analyzeAdaptationSource('project-1', 'source.txt', { force: true });

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/adapt/analyze', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ source_file: 'source.txt', force: true })
    }));
  });

  it('sends co-create source and revise payloads', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await sendCoCreate('project-1', 'Use the heroine arc', 'suggestion');
    await sendCoCreate('project-1', 'Re-scan this direction', 'custom', { forceRebrief: true });
    await reviseCoCreate('project-1', 'm3', 'Keep a slower burn');
    await resolveCoCreateDecision('project-1', 'q1', 'a', '');
    await resolveCoCreateDecisions('project-1', [{ decision_id: 'q2', option_id: 'b', custom_answer: '' }]);
    await resumeCoCreate('project-1');

    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({
      text: 'Use the heroine arc',
      source: 'suggestion'
    });
    expect(fetchMock.mock.calls[0][0]).toBe('/api/projects/project-1/cocreate/send');
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({
      text: 'Re-scan this direction',
      source: 'custom',
      force_rebrief: true
    });
    expect(fetchMock.mock.calls[1][0]).toBe('/api/projects/project-1/cocreate/send');
    expect(JSON.parse(fetchMock.mock.calls[2][1].body)).toEqual({
      message_id: 'm3',
      text: 'Keep a slower burn'
    });
    expect(fetchMock.mock.calls[2][0]).toBe('/api/projects/project-1/cocreate/revise');
    expect(JSON.parse(fetchMock.mock.calls[3][1].body)).toEqual({
      decision_id: 'q1',
      option_id: 'a',
      custom_answer: ''
    });
    expect(fetchMock.mock.calls[3][0]).toBe('/api/projects/project-1/cocreate/decision');
    expect(JSON.parse(fetchMock.mock.calls[4][1].body)).toEqual({
      decisions: [{ decision_id: 'q2', option_id: 'b', custom_answer: '' }]
    });
    expect(fetchMock.mock.calls[4][0]).toBe('/api/projects/project-1/cocreate/decision');
    expect(JSON.parse(fetchMock.mock.calls[5][1].body)).toEqual({});
    expect(fetchMock.mock.calls[5][0]).toBe('/api/projects/project-1/cocreate/resume');
  });

  it('sends completed chapter revision payloads', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await reviseChapter('project-1', {
      chapter: 3,
      mode: 'polish',
      instruction: 'tighten the ending'
    });

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/chapters/revise', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        chapter: 3,
        mode: 'polish',
        instruction: 'tighten the ending'
      })
    }));
  });

  it('sends chapter outline revision payloads', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await reviseChapterOutline('project-1', {
      chapter: 7,
      instruction: 'move the reveal into the courtroom scene'
    });

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/outline/chapters/revise', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        chapter: 7,
        instruction: 'move the reveal into the courtroom scene'
      })
    }));
  });

  it('fetches completed chapter content', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ chapter: { chapter: 3 } }));

    await getChapter('project-1', 3);

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/chapters/3', expect.objectContaining({}));
  });

  it('sends novel library replace requests explicitly', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await saveNovelToLibrary('project-1', 'Source Book', 'source.txt', { replace: true });

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/adapt/library/save', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ name: 'Source Book', source_file: 'source.txt', replace: true })
    }));
  });

  it('fetches project event history with an after cursor', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ events: [] }));

    await getProjectEvents('project 1', 7);

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project%201/events/history?after=7', expect.objectContaining({
      headers: {}
    }));
  });

  it('downloads exported novel blobs with metadata', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockBlobResponse('novel body'));

    const result = await exportProjectDownload('project-1', {
      path: 'book.txt',
      format: 'txt',
      from: 1,
      to: 59
    });

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/export/download', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        path: 'book.txt',
        format: 'txt',
        from: 1,
        to: 59
      })
    }));
    expect(result.export).toMatchObject({
      name: 'book.txt',
      chapters: 59,
      bytes: 893100,
      skipped: [2, 4]
    });
    expect(await result.blob.text()).toBe('novel body');
  });

  it('uses staged adaptation proposal, revise, details, and final confirm routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await buildAdaptationProposal('project-1', 'source.txt', 'free', 'Make it a mystery');
    await reviseAdaptationVolumeReview('project-1', { volume_index: 3, instruction: 'raise tension' });
    await confirmAdaptationProposalDetails('project-1');
    await reviseAdaptationProposal('project-1', { from_chapter: 4, to_chapter: 6, instruction: 'tighten the reveal' });
    await confirmAdaptationProposal('project-1');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/projects/project-1/adapt/proposal/volumes', expect.objectContaining({
      method: 'POST',
      body: expect.stringContaining('"async":true')
    }));
	const firstBody = JSON.parse(fetchMock.mock.calls[0][1].body);
	expect(firstBody).toMatchObject({
	  source_file: 'source.txt',
	  mode: 'free',
	  brief: 'Make it a mystery',
	  async: true
	});
	expect(firstBody.idempotency_key).toEqual(expect.any(String));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/adapt/proposal/volumes/revise', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        volume_index: 3,
        instruction: 'raise tension'
      })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/projects/project-1/adapt/proposal/details', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({})
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/projects/project-1/adapt/proposal/revise', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        from_chapter: 4,
        to_chapter: 6,
        instruction: 'tighten the reveal'
      })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(5, '/api/projects/project-1/adapt/confirm', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({})
    }));
  });

  it('uses read-only adaptation audit and confirmed repair routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));
	const options = { source_to: 284 };
    const confirmation = {
      report_digest: 'audit-digest',
      decision: 'apply',
      acknowledged_finding_ids: ['missing-mainline-1']
    };

    await getAdaptationAudit('project-1');
    await runAdaptationAudit('project-1', options);
    await applyAdaptationAudit('project-1', confirmation);

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/projects/project-1/adapt/audit', expect.objectContaining({}));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/adapt/audit', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify(options)
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/projects/project-1/adapt/audit/apply', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify(confirmation)
    }));
  });

  it('uses immutable audit history, comparison, and resumable semantic audit routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ run: { run_id: 'sem_123' } }));
    const semantic = { source_to: 12, max_calls: 20, acknowledge_unknown_price: true };

    await listAdaptationAuditRuns('project-1', { limit: 50 });
    await getAdaptationAuditRun('project-1', 'run/one');
    await compareAdaptationAuditRuns('project-1', 'base run', 'candidate/run');
    await estimateSemanticAdaptationAudit('project-1', semantic);
    await startSemanticAdaptationAudit('project-1', semantic);
    await getSemanticAdaptationAudit('project-1', 'sem/one');
    await getSemanticAdaptationAuditReport('project-1', 'sem/one');
    await cancelSemanticAdaptationAudit('project-1', 'sem/one');
    await retrySemanticAdaptationAudit('project-1', 'sem/one');

    expect(fetchMock.mock.calls.map(([path]) => path)).toEqual([
      '/api/projects/project-1/adapt/audits?limit=50',
      '/api/projects/project-1/adapt/audits/run%2Fone',
      '/api/projects/project-1/adapt/audits/compare?base_run_id=base+run&candidate_run_id=candidate%2Frun',
      '/api/projects/project-1/adapt/audits/semantic/estimate',
      '/api/projects/project-1/adapt/audits/semantic',
      '/api/projects/project-1/adapt/audits/semantic/sem%2Fone',
      '/api/projects/project-1/adapt/audits/semantic/sem%2Fone/report',
      '/api/projects/project-1/adapt/audits/semantic/sem%2Fone',
      '/api/projects/project-1/adapt/audits/semantic/sem%2Fone/retry'
    ]);
    expect(fetchMock.mock.calls[7][1].method).toBe('DELETE');
    expect(JSON.parse(fetchMock.mock.calls[4][1].body)).toEqual(semantic);
  });

  it('uses revision-checked continuation review and start routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await getContinuation('project-1');
    await reviseContinuationProposal('project-1', { instruction: '强化冲突', expected_revision: 4 });
    await approveContinuationProposal('project-1', { expected_revision: 5 });
    await reviseContinuationVolumes('project-1', { volume_index: 2, instruction: '合并支线', expected_revision: 6 });
    await approveContinuationVolumes('project-1', { expected_revision: 7 });
    await reviseContinuationOutlines('project-1', { scope: 'chapter', chapter: 43, instruction: '提前揭示', expected_revision: 8 });
    await approveContinuationOutlines('project-1', { expected_revision: 9 });
    await startContinuation('project-1', { expected_revision: 10 });

    expect(fetchMock.mock.calls.map(([path]) => path)).toEqual([
      '/api/projects/project-1/continuation',
      '/api/projects/project-1/continuation/proposal/revise',
      '/api/projects/project-1/continuation/proposal/approve',
      '/api/projects/project-1/continuation/volumes/revise',
      '/api/projects/project-1/continuation/volumes/approve',
      '/api/projects/project-1/continuation/outlines/revise',
      '/api/projects/project-1/continuation/outlines/approve',
      '/api/projects/project-1/continuation/start'
    ]);
    expect(JSON.parse(fetchMock.mock.calls[5][1].body)).toEqual({
      scope: 'chapter',
      chapter: 43,
      instruction: '提前揭示',
      expected_revision: 8
    });
  });

  it('uploads a continuation source without an automatic resume parameter', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));
    const file = new File(['chapter one'], 'source.txt', { type: 'text/plain' });

    await uploadContinuationSource('project-1', file);

    const [, options] = fetchMock.mock.calls[0];
    expect(fetchMock.mock.calls[0][0]).toBe('/api/projects/project-1/continuation/source');
    expect(options.method).toBe('POST');
    expect(options.body.get('files')).toBe(file);
    expect(options.body.has('from')).toBe(false);
    expect(options.body.has('resume_from')).toBe(false);
  });

  it('uses explicit continuation generation and retry routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await generateContinuationProposal('project-1', { expected_revision: 2 });
    await generateContinuationOutlines('project-1', { expected_revision: 6 });
    await retryContinuation('project-1', { expected_revision: 8 });

    expect(fetchMock.mock.calls.map(([path]) => path)).toEqual([
      '/api/projects/project-1/continuation/proposal/generate',
      '/api/projects/project-1/continuation/outlines/generate',
      '/api/projects/project-1/continuation/retry'
    ]);
  });

  it('uses the normal co-create planning revision route', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await reviseCoCreatePlanning('project-1', { feedback: 'tighten the opening' });

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/cocreate/planning/revise', expect.objectContaining({
      method: 'POST',
      body: expect.any(String)
    }));
    const body = JSON.parse(fetchMock.mock.calls[0][1].body);
    expect(body).toMatchObject({
      feedback: 'tighten the opening',
      async: true
    });
    expect(body.idempotency_key).toContain('/api/projects/project-1/cocreate/planning/revise:');
  });

  it('can ask the backend to open the Grok authorization page', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await startGrokLogin('project-1', 'work', 'Work', true);

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/models/grok-login/start', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        account_id: 'work',
        account_name: 'Work',
        open_browser: true
      })
    }));
  });

  it('uses global Grok login routes when no project is active', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await startGrokLogin('', 'default', 'Default', true);

    expect(fetchMock).toHaveBeenCalledWith('/api/models/grok-login/start', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        account_id: 'default',
        account_name: 'Default',
        open_browser: true
      })
    }));
  });

  it('checks Codex auth status through global and project routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await getCodexAuthStatus('', '');
    await getCodexAuthStatus('project-1', 'D:/codex/auth.json');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/models/codex-auth/status', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ auth_file: '' })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/models/codex-auth/status', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ auth_file: 'D:/codex/auth.json' })
    }));
  });

  it('uploads Codex auth through multipart without setting a JSON content type', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));
    const file = new File(['{"tokens":{}}'], 'auth.json', { type: 'application/json' });

    await uploadCodexAuthFile(file);

    const [, options] = fetchMock.mock.calls[0];
    expect(fetchMock.mock.calls[0][0]).toBe('/api/models/codex-auth/upload');
    expect(options.method).toBe('POST');
    expect(options.body).toBeInstanceOf(FormData);
    expect(options.headers).toEqual({});
  });

  it('uses global model routes for default model controls', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await getGlobalModels();
    await switchGlobalDefaultModel('openai', 'gpt-next');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/models', expect.objectContaining({
      headers: {}
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/models/default', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        provider: 'openai',
        model: 'gpt-next'
      })
    }));
  });

  it('uses the legacy global model switch route for role controls', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await switchGlobalModel('writer', 'deepseek', 'deepseek-chat');

    expect(fetchMock).toHaveBeenCalledWith('/api/models/switch', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        role: 'writer',
        provider: 'deepseek',
        model: 'deepseek-chat'
      })
    }));
  });

  it('sends the backend-owned simulation refresh action', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ accepted: true }));

    await analyzeSimulation('project/1', 'resynthesize');

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project%2F1/simulate/analyze', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ action: 'resynthesize' })
    }));
  });

  it('updates and inherits global stage routes and thinking', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await inheritGlobalModel('stage:writing');
    await setGlobalThinking('stage:writing', 'xhigh');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/models/switch', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ role: 'stage:writing', inherit: true })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/models/thinking', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ role: 'stage:writing', level: 'xhigh' })
    }));
  });

  it('switches project model routes and clears project role overrides', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await switchProjectModel('project-1', 'writer', 'deepseek', 'deepseek-chat');
    await inheritProjectModel('project-1', 'writer');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/projects/project-1/models/switch', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        role: 'writer',
        provider: 'deepseek',
        model: 'deepseek-chat'
      })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/models/switch', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        role: 'writer',
        inherit: true
      })
    }));
  });

  it('sends co-create generation setting updates to global and project model routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await setGlobalCoCreateTimeout(60);
    await setProjectCoCreateTimeout('project-1', 30);
    await setGlobalCoCreateMaxTokens(8192);
    await setProjectCoCreateMaxTokens('project-1', 12288);

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/models/cocreate-timeout', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ seconds: 60 })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/models/cocreate-timeout', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ seconds: 30 })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/models/cocreate-max-tokens', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ tokens: 8192 })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/projects/project-1/models/cocreate-max-tokens', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ tokens: 12288 })
    }));
  });

  it('adds provider models through the global route when no project is active', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await addGlobalProviderModel({
      select_after_save: false,
      role: 'default',
      provider: 'grok-oauth',
      model: 'grok-4.3-latest',
      type: 'grok',
      auth: 'grok_oauth',
      account_id: 'default'
    });

    expect(fetchMock).toHaveBeenCalledWith('/api/models/add', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        select_after_save: false,
        role: 'default',
        provider: 'grok-oauth',
        model: 'grok-4.3-latest',
        type: 'grok',
        auth: 'grok_oauth',
        account_id: 'default'
      })
    }));
  });

  it('sends retry settings to the global route', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await setGlobalRetrySettings(14, 8, 3, 5);

    expect(fetchMock).toHaveBeenCalledWith('/api/models/retry-settings', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        model_call_max_attempts: 14,
        structure_repair_max_attempts: 8,
        budget_quality_max_attempts: 3,
        adaptation_outline_audit_retry_max_attempts: 5
      })
    }));
  });

  it('sends retry settings to the project route', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await setProjectRetrySettings('project-1', 14, 8, 3, 5);

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/models/retry-settings', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        model_call_max_attempts: 14,
        structure_repair_max_attempts: 8,
        budget_quality_max_attempts: 3,
        adaptation_outline_audit_retry_max_attempts: 5
      })
    }));
  });

  it('tests and discovers provider models through global and project routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));
    const payload = {
      role: 'default',
      provider: 'codex',
      model: 'gpt-5.1-codex',
      type: 'openai',
      api: 'responses',
      use_proxy: true
    };

    await testGlobalProviderModel(payload);
    await testProjectProviderModel('project-1', payload);
    await discoverGlobalProviderModels(payload);
    await discoverProjectProviderModels('project-1', payload);

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/models/test', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify(payload)
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/models/test', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify(payload)
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/models/discover', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify(payload)
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/projects/project-1/models/discover', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify(payload)
    }));
  });

  it('deletes provider models through global and project model routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await deleteGlobalProviderModel('proxy', 'proxy-model');
    await deleteProviderModel('project-1', 'proxy', 'proxy-model');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/models', expect.objectContaining({
      method: 'DELETE',
      body: JSON.stringify({
        provider: 'proxy',
        model: 'proxy-model'
      })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/models', expect.objectContaining({
      method: 'DELETE',
      body: JSON.stringify({
        provider: 'proxy',
        model: 'proxy-model'
      })
    }));
  });
});
