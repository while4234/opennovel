import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import {
  applyAdaptationProposalSnapshot,
  applyHostEventToSimulationState,
  buildAdaptationProposalKey,
  buildAdaptationRevisionPayload,
  buildBeginCoCreatePayload,
  buildChapterRevisionPayload,
  buildCoCreatePlanningRevisionPayload,
  buildCoCreateDecisionPayload,
  buildCoCreateIntakeInitial,
  buildExportSuggestedName,
  buildOutlineRevisionPayload,
  buildVolumeReviewRevisionPayload,
  canCancelCoCreateFlow,
  adaptationRebriefDisabledReason,
  canRunAdaptationAnalysis,
  canSaveAnalyzedNovelToLibrary,
  canRunSimulationAnalysis,
  canRunSimulationPrimaryAnalysis,
  clampCoCreateDecisionPageIndex,
  coCreateDecisionOptionLetter,
  coCreateDecisionRecommendedLetter,
  CO_CREATE_DECISION_SKIP_ANSWER,
  clearAdaptationProposalSnapshot,
  formatAdaptationSourceCoverageLabel,
  formatAdaptationVolumeSourceLabel,
  getAdaptationProposalReview,
  getCompletedBookChapterRevisionView,
  getCompletedBookSelectedChapterView,
  getCoCreatePlanningReview,
  getOutlineRevisionView,
  getVisibleAdaptationProposalReview,
  getSimulationProfileStatus,
  getSnapshotOutlineRows,
  getSnapshotOutlineStructure,
  inferCoCreateIntakeFromInitial,
  isExportActionBusy,
  isCoCreateDecisionAnswerComplete,
  isCoCreateDecisionPayloadComplete,
  isCoCreateRequestBusy,
  isProjectScopedResponseCurrent,
  isSimulationProfileActionBusy,
  isProjectRunning,
  libraryEntryMeta,
  normalizeCoCreateDecisionAnswers,
  outlineRevisionSuccessMessage,
  prepareProjectOpenSnapshot,
  shouldHydratePendingPlanningReviewDetails,
  shouldHydrateOutlineRevisionDetails,
  shouldHydrateProjectOpenSnapshot,
  PROJECT_OPEN_TIMEOUT_MS,
  resolveCoCreateStructureChoice,
  resolveCoCreateTargetTotalWords,
  resolveVisibleDefaultModel,
  restoreSimulationProjectState,
  restoreProjectWorkbenchSnapshot,
  simulationAnalysisActionLabel,
  simulationAnalysisNextStep,
  simulationProfileDisplayState,
  simulationFilesFromResponse,
  simulationCheckStateLabel,
  simulationContractStatusLabel,
  simulationHealthLabel,
  simulationProfileSummaryText
} from './App.jsx';

const appSource = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');

describe('project opening', () => {
  it('allows cold project restoration to finish before reporting a timeout', () => {
    expect(PROJECT_OPEN_TIMEOUT_MS).toBe(90_000);
  });
});

describe('simulation library metadata', () => {
  it('shows whether original corpus files are archived with the profile', () => {
    expect(libraryEntryMeta({
      profile_version: 'simulation_profile.v2',
      health_state: 'portable_only',
      source_archived: true,
      archived_source_count: 17
    })).toContain('含原语料 17 篇');
    expect(libraryEntryMeta({
      profile_version: 'simulation_profile.v2',
      health_state: 'portable_only',
      source_archived: false
    })).toContain('仅 portable');
  });
});

describe('co-create begin payload helpers', () => {
  it('clamps co-create decision pagination to visible pending decisions', () => {
    expect(clampCoCreateDecisionPageIndex(2, 4)).toBe(2);
    expect(clampCoCreateDecisionPageIndex(-1, 4)).toBe(0);
    expect(clampCoCreateDecisionPageIndex(9, 4)).toBe(3);
    expect(clampCoCreateDecisionPageIndex('bad', 4)).toBe(0);
    expect(clampCoCreateDecisionPageIndex(2, 0)).toBe(0);
  });

  it('builds explicit co-create decision payloads including skip answers', () => {
    const decisions = [
      { id: 'q1', recommended_option_id: 'a', options: [{ id: 'a', label: 'accept' }] },
      { id: 'q2', recommended_option_id: 'b', options: [{ id: 'b', label: 'rewrite' }] },
      { id: 'q3', recommended_option_id: 'c', options: [{ id: 'c', label: 'merge' }] }
    ];

    const payload = buildCoCreateDecisionPayload(decisions, {
      q1: { optionId: 'a', customAnswer: '' },
      q2: { optionId: '__skip__', customAnswer: '' },
      q3: { optionId: '', customAnswer: '只保留伏笔，不改人物关系' }
    });

    expect(payload).toEqual([
      { decision_id: 'q1', option_id: 'a', custom_answer: '' },
      { decision_id: 'q2', option_id: '', custom_answer: CO_CREATE_DECISION_SKIP_ANSWER },
      { decision_id: 'q3', option_id: '', custom_answer: '只保留伏笔，不改人物关系' }
    ]);
    expect(payload.every(isCoCreateDecisionPayloadComplete)).toBe(true);
  });

  it('keeps co-create cancellation available while project work is busy', () => {
    expect(canCancelCoCreateFlow({
      activeProject: { id: 'project-1' },
      busy: true,
      coCreate: { active: true, status: 'running' }
    })).toBe(true);
    expect(canCancelCoCreateFlow({
      activeProject: { id: 'project-1' },
      coCreate: { messages: [{ role: 'user', content: 'draft' }] }
    })).toBe(true);
    expect(canCancelCoCreateFlow({
      activeProject: { id: 'project-1' },
      busy: true,
      coCreate: { active: true, status: 'idle' }
    })).toBe(false);
    expect(canCancelCoCreateFlow({
      activeProject: null,
      coCreate: { active: true }
    })).toBe(false);
  });

  it('matches async project responses only to the active project scope', () => {
    expect(isProjectScopedResponseCurrent('project-1', 'project-1')).toBe(true);
    expect(isProjectScopedResponseCurrent('project-1', 'project-2')).toBe(false);
    expect(isProjectScopedResponseCurrent('', 'project-1')).toBe(false);
    expect(isProjectScopedResponseCurrent('project-1', 'project-1', 3, 3)).toBe(true);
    expect(isProjectScopedResponseCurrent('project-1', 'project-1', 2, 3)).toBe(false);
  });

  it('does not treat a recommended co-create decision option as selected before user action', () => {
    const answers = normalizeCoCreateDecisionAnswers([
      { id: 'q1', recommended_option_id: 'a', options: [{ id: 'a', label: 'accept' }] }
    ]);

    expect(answers.q1).toEqual({ optionId: '', customAnswer: '' });
    expect(isCoCreateDecisionAnswerComplete(answers.q1)).toBe(false);
    expect(isCoCreateDecisionPayloadComplete({
      decision_id: 'q1',
      option_id: '',
      custom_answer: ''
    })).toBe(false);
  });

  it('labels co-create decisions with visible option letters and recommended letters', () => {
    expect(coCreateDecisionOptionLetter(0)).toBe('A');
    expect(coCreateDecisionOptionLetter(3)).toBe('D');
    expect(coCreateDecisionOptionLetter(26)).toBe('AA');
    expect(coCreateDecisionRecommendedLetter({
      recommended_option_id: 'b',
      options: [
        { id: 'a', label: 'first' },
        { id: 'b', label: 'second' },
        { id: 'c', label: 'third' }
      ]
    })).toBe('B');
  });

  it('builds safe export suggested filenames', () => {
    expect(buildExportSuggestedName({ path: '', format: 'txt' }, { name: '梦中的女孩改_v2' })).toBe('梦中的女孩改_v2.txt');
    expect(buildExportSuggestedName({ path: 'draft.epub', format: 'txt' }, { name: 'Book' })).toBe('draft.txt');
    expect(buildExportSuggestedName({ path: 'bad/name:*?', format: 'epub' }, { name: 'Book' })).toBe('name___.epub');
  });

  it('keeps export available while unrelated project work is running', () => {
    expect(isExportActionBusy({ status: 'idle' })).toBe(false);
    expect(isExportActionBusy({ status: 'done' })).toBe(false);
    expect(isExportActionBusy({ status: 'running' })).toBe(true);
  });

  it('uses the standard browser download path without a native file picker', () => {
    expect(appSource).not.toContain('showSaveFilePicker');
    expect(appSource).toContain('导出已完成章节');
  });

  it('sends target_total_words for normal co-create only', () => {
    expect(buildBeginCoCreatePayload({
      kind: 'normal',
      initial: '  moon city mystery  ',
      targetTotalWords: 10000
    })).toEqual({
      kind: 'normal',
      initial: 'moon city mystery',
      target_total_words: 10000
    });

    expect(buildBeginCoCreatePayload({
      kind: 'adapt',
      initial: '',
      sourceFile: 'source.txt',
      mode: 'arc',
      targetTotalWords: 30000
    })).toEqual({
      kind: 'adapt',
      initial: '',
      source_file: 'source.txt',
      mode: 'arc'
    });

    expect(buildBeginCoCreatePayload({
      kind: 'adapt',
      initial: '',
      fallbackInitial: '  保留主线但加强女主调查线  ',
      sourceFile: 'source.txt',
      mode: 'free'
    })).toEqual({
      kind: 'adapt',
      initial: '保留主线但加强女主调查线',
      source_file: 'source.txt',
      mode: 'free'
    });
  });

  it('resolves preset and custom total word choices', () => {
    expect(resolveCoCreateTargetTotalWords({})).toBe(0);
    expect(resolveCoCreateTargetTotalWords({ targetTotalWordsChoice: '30000' })).toBe(30000);
    expect(resolveCoCreateTargetTotalWords({
      targetTotalWordsChoice: 'custom',
      customTargetTotalWords: '12000'
    })).toBe(12000);
    expect(resolveCoCreateTargetTotalWords({
      targetTotalWordsChoice: 'custom',
      customTargetTotalWords: '12.5'
    })).toBe(0);
  });

  it('infers explicit total words from the initial idea', () => {
    expect(inferCoCreateIntakeFromInitial('创作一篇5000字的短篇小说')).toMatchObject({
      targetTotalWords: 5000,
      targetTotalWordsChoice: '5000',
      structureChoice: 'single'
    });
    expect(inferCoCreateIntakeFromInitial('写十万字长篇悬疑')).toMatchObject({
      targetTotalWords: 100000,
      targetTotalWordsChoice: '100000',
      structureChoice: 'auto'
    });
    expect(inferCoCreateIntakeFromInitial('写三万字都市奇幻')).toMatchObject({
      targetTotalWords: 30000,
      targetTotalWordsChoice: '30000',
      structureChoice: 'auto'
    });
    expect(inferCoCreateIntakeFromInitial('写30w字赛博故事')).toMatchObject({
      targetTotalWords: 300000,
      targetTotalWordsChoice: 'custom',
      customTargetTotalWords: '300000'
    });
    expect(inferCoCreateIntakeFromInitial('目标20万字，target_total_words=200000')).toMatchObject({
      targetTotalWords: 200000,
      customTargetTotalWords: '200000'
    });
  });

  it('requires confirmation for vague length labels and per-chapter counts', () => {
    expect(inferCoCreateIntakeFromInitial('写一部长篇悬疑小说')).toMatchObject({
      targetTotalWords: 0,
      targetTotalWordsChoice: '',
      structureChoice: 'auto'
    });
    expect(inferCoCreateIntakeFromInitial('写一个故事，每章5000字')).toMatchObject({
      targetTotalWords: 0,
      targetTotalWordsChoice: ''
    });
  });

  it('builds intake prompt with total-word and short-story structure rules', () => {
    const prompt = buildCoCreateIntakeInitial('写未来地球赛博悬疑', {
      targetTotalWords: 5000,
      structureChoice: 'single'
    });

    expect(resolveCoCreateStructureChoice({ structureChoice: 'unknown' })).toBe('single');
    expect(prompt).toContain('target_total_words=5000');
    expect(prompt).toContain('全书总字数');
    expect(prompt).toContain('3000-5000');
    expect(prompt).toContain('不要拆成多个章节');
  });
});

describe('project open snapshot preparation', () => {
  it('accepts the first click response even while the active project ref is cleared', () => {
    const prepared = prepareProjectOpenSnapshot(4, 4, {
      project: { id: 'project-2', name: '第二个项目' },
      snapshot: { RuntimeState: 'running' },
      events: [],
      latest_event_seq: 27
    });

    expect(prepared.project.id).toBe('project-2');
    expect(prepared.workbench.lastSeq).toBe(27);
    expect(prepared.workbench.snapshot.RuntimeState).toBe('running');
  });

  it('rejects a response superseded by a later project click', () => {
    expect(prepareProjectOpenSnapshot(4, 5, {
      project: { id: 'project-1' },
      snapshot: { RuntimeState: 'running' }
    })).toBeNull();
  });

  it('hydrates full outline detail only while a planning review is active', () => {
    expect(shouldHydrateProjectOpenSnapshot({
      PlanningReview: { Status: 'pending', Kind: 'chapter_outline' },
      Outline: [{ Chapter: 23, Title: '摘要标题' }]
    })).toBe(true);
    expect(shouldHydrateProjectOpenSnapshot({
      PlanningReview: { Status: 'collecting', Kind: 'volume_split' }
    })).toBe(true);
    expect(shouldHydrateProjectOpenSnapshot({
      PlanningReview: { Status: 'confirmed', Kind: 'chapter_outline' }
    })).toBe(false);
  });

  it('hydrates a completed chapter review when any generated chapter still has summary-only data', () => {
    expect(shouldHydratePendingPlanningReviewDetails({
      PlanningReview: { Status: 'pending', Kind: 'chapter_outline' },
      Outline: [
        { Chapter: 8, Title: '完整章节', CoreEvent: '完整核心事件', Hook: '完整钩子', Scenes: ['场景'] },
        { Chapter: 9, Title: '仅有摘要字段' }
      ]
    })).toBe(true);
    expect(shouldHydratePendingPlanningReviewDetails({
      PlanningReview: { Status: 'pending', Kind: 'chapter_outline' },
      Outline: [
        { Chapter: 8, Title: '完整章节', CoreEvent: '完整核心事件' },
        { Chapter: 9, Title: '完整章节', Scenes: ['场景'] }
      ]
    })).toBe(false);
    expect(shouldHydratePendingPlanningReviewDetails({
      PlanningReview: { Status: 'collecting', Kind: 'chapter_outline' },
      Outline: [{ Chapter: 9, Title: '生成中' }]
    })).toBe(false);
  });

  it('hydrates chapter details on demand when the writing workspace opens a summary-only outline', () => {
    const summarySnapshot = {
      Outline: [
        { Chapter: 39, Title: '摘要标题' },
        { Chapter: 40, Title: '另一章摘要' }
      ]
    };

    expect(shouldHydrateOutlineRevisionDetails(summarySnapshot, {
      active: true,
      chapter: '40'
    })).toBe(true);
    expect(shouldHydrateOutlineRevisionDetails(summarySnapshot, {
      active: false,
      chapter: '40'
    })).toBe(false);
    expect(shouldHydrateOutlineRevisionDetails({
      Outline: [{ Chapter: 40, Title: '完整细纲', CoreEvent: '核心事件', Scenes: ['场景一'] }]
    }, {
      active: true,
      chapter: '40'
    })).toBe(false);
  });
});

describe('workspace progress state', () => {
  it('preserves replayed event rows when project snapshot resolves after event replay', () => {
    const previous = {
      lastSeq: 2,
      eventRows: [
        { seq: 1, event: { running: false, agent: 'editor', summary: 'reviewed' } },
        { seq: 2, event: { running: true, agent: 'architect', summary: 'planning volume' } }
      ],
      streamRounds: [{ id: 'round-0', text: 'draft preview' }],
      snapshot: null
    };

    const next = restoreProjectWorkbenchSnapshot(previous, {
      RuntimeState: 'running',
      CompletedCount: 1
    });

    expect(next).toMatchObject({
      lastSeq: 2,
      eventRows: previous.eventRows,
      streamRounds: previous.streamRounds,
      snapshot: { RuntimeState: 'running', CompletedCount: 1 }
    });
  });

  it('restores event rows from project history when opening a running project', () => {
    const next = restoreProjectWorkbenchSnapshot({
      lastSeq: 0,
      eventRows: [],
      streamRounds: [{ id: 'round-0', text: '' }],
      snapshot: null
    }, {
      RuntimeState: 'running',
      CompletedCount: 1
    }, [
      {
        seq: 11,
        type: 'host_event',
        host_event_id: 'analysis-1',
        event: { id: 'analysis-1', running: true, agent: 'web', summary: 'analyzing source' }
      }
    ]);

    expect(next.lastSeq).toBe(11);
    expect(next.eventRows).toHaveLength(1);
    expect(next.eventRows[0].event.summary).toBe('analyzing source');
    expect(next.snapshot).toMatchObject({ RuntimeState: 'running' });
  });

  it('starts live events after the snapshot sequence without replaying omitted state events', () => {
    const next = restoreProjectWorkbenchSnapshot({
      lastSeq: 0,
      eventRows: [],
      streamRounds: [{ id: 'round-0', text: '' }],
      snapshot: null
    }, {
      RuntimeState: 'running',
      WorkflowProgress: { Workflow: 'adaptation', Status: 'running' }
    }, [
      {
        seq: 11,
        type: 'host_event',
        host_event_id: 'analysis-1',
        event: { running: true, summary: 'analyzing source' }
      }
    ], 719);

    expect(next.lastSeq).toBe(719);
    expect(next.eventRows).toHaveLength(1);
    expect(next.snapshot).toMatchObject({
      RuntimeState: 'running',
      WorkflowProgress: { Workflow: 'adaptation' }
    });
  });

  it('detects running snapshots for pause controls', () => {
    expect(isProjectRunning({ IsRunning: true, RuntimeState: 'paused' })).toBe(true);
    expect(isProjectRunning({ RuntimeState: 'running', Agents: [] })).toBe(true);
    expect(isProjectRunning({ RuntimeState: 'paused', Agents: [{ Name: 'writer', State: 'idle' }] })).toBe(false);
    expect(isProjectRunning({ RuntimeState: '', Agents: [{ Name: 'writer', State: 'working' }] })).toBe(true);
  });

  it('keeps simulation analysis available during unrelated adaptation work', () => {
    const activeProject = { id: 'project-1' };

    expect(canRunSimulationAnalysis({
      activeProject,
      busy: false,
      simulation: { analysisStatus: 'idle', importStatus: 'idle' }
    })).toBe(true);
    expect(canRunSimulationAnalysis({
      activeProject,
      busy: false,
      simulation: { analysisStatus: 'running', importStatus: 'idle' }
    })).toBe(false);
    expect(canRunSimulationAnalysis({
      activeProject,
      busy: true,
      simulation: { analysisStatus: 'idle', importStatus: 'idle' }
    })).toBe(true);
  });

  it('makes the first simulation analysis action explicit after corpus upload', () => {
    const activeProject = { id: 'project-1' };
    expect(canRunSimulationPrimaryAnalysis({
      activeProject,
      profile: { loaded: false },
      simulation: { files: [{ name: 'new.txt' }] }
    })).toBe(true);
    expect(simulationAnalysisActionLabel({
      analysisStatus: 'idle',
      fileCount: 1,
      profileLoaded: false
    })).toBe('开始分析并入库');
    expect(simulationAnalysisNextStep({
      analysisStatus: 'running',
      fileCount: 8,
      profileLoaded: true,
      scanAvailable: false
    })).toBe('正在分析 8 份语料并更新画像库，请等待完成。');
    expect(simulationAnalysisNextStep({
      fileCount: 56,
      profileLoaded: false
    })).toBe('已准备 56 份语料，请点击“开始分析并入库”生成画像并自动保存到画像库。');

    expect(simulationAnalysisActionLabel({
      analysisStatus: 'done',
      fileCount: 56,
      profileLoaded: true,
      scanAvailable: true
    })).toBe('重新扫描并入库');
    expect(simulationAnalysisNextStep({
      fileCount: 56,
      profileLoaded: true,
      scanAvailable: true
    })).toBe('已准备 56 份语料；点击“重新扫描并入库”只分析旧版本、新增或变化的语料，并自动更新画像库。');
    expect(simulationAnalysisActionLabel({
      analysisStatus: 'running',
      fileCount: 56,
      profileLoaded: false
    })).toBe('正在分析并入库…');
  });

  it('disables repeat simulation scans after the current corpus is fully analyzed', () => {
    const activeProject = { id: 'project-1' };
    const currentProfile = {
      loaded: true,
      healthState: 'fresh',
      sourceCount: 2,
      reportCount: 2
    };
    expect(canRunSimulationPrimaryAnalysis({
      activeProject,
      profile: currentProfile,
      simulation: { files: [{ name: 'a.txt' }, { name: 'b.txt' }] }
    })).toBe(false);
    expect(simulationAnalysisActionLabel({
      fileCount: 2,
      profileLoaded: true,
      scanAvailable: false
    })).toBe('画像已是最新');
    expect(simulationAnalysisNextStep({
      fileCount: 2,
      profileLoaded: true,
      scanAvailable: false
    })).toBe('全部 2 份语料已按当前流程完成分析，无需重复扫描。');

    expect(canRunSimulationPrimaryAnalysis({
      activeProject,
      profile: currentProfile,
      simulation: { files: [{ name: 'a.txt' }, { name: 'b.txt' }, { name: 'new.txt' }] }
    })).toBe(true);
    expect(canRunSimulationPrimaryAnalysis({
      activeProject,
      profile: { ...currentProfile, healthState: 'stale' },
      simulation: { files: [{ name: 'a.txt' }, { name: 'b.txt' }] }
    })).toBe(true);
  });

  it('shows active reanalysis instead of the stale previous profile', () => {
    expect(simulationProfileDisplayState({
      analysisStatus: 'running',
      profile: {
        loaded: true,
        healthState: 'stale',
        healthReasons: ['synthesis_pending'],
        sourceCount: 10,
        reportCount: 10
      },
      latestAnalysis: {
        stage: 'analyze',
        current: 11,
        total: 56,
        message: '分析仿写语料 11/56'
      }
    })).toEqual({
      running: true,
      tone: 'running',
      label: '正在重新分析',
      summary: '分析仿写语料 11/56'
    });
    expect(simulationProfileDisplayState({
      analysisStatus: 'done',
      profile: {
        loaded: true,
        healthState: 'fresh',
        sourceCount: 56,
        reportCount: 56
      }
    })).toEqual({
      running: false,
      tone: 'done',
      label: '新鲜可用',
      summary: '新鲜可用 · 56/56 篇报告'
    });
  });

  it('keeps adaptation analysis available while simulation preparation is running', () => {
    const activeProject = { id: 'project-1' };
    const adaptation = {
      sourceFile: { relative_path: 'source.txt' },
      analysisStatus: 'idle'
    };

    expect(isSimulationProfileActionBusy({ analysisStatus: 'running' })).toBe(true);
    expect(canRunAdaptationAnalysis({ activeProject, busy: false, adaptation })).toBe(true);
    expect(canRunAdaptationAnalysis({ activeProject, busy: true, adaptation })).toBe(true);
    expect(canRunAdaptationAnalysis({
      activeProject,
      busy: false,
      adaptation: { ...adaptation, analysisStatus: 'running' }
    })).toBe(false);
    expect(canRunAdaptationAnalysis({
      activeProject,
      busy: false,
      adaptation: { ...adaptation, analysisStatus: 'done' }
    })).toBe(true);
  });

  it('explains why adaptation co-create briefing regeneration is unavailable', () => {
    const base = {
      activeProject: { id: 'project-1' },
      busy: false,
      coCreate: { kind: 'adapt', input: '重新整理方向' },
      hasBackendSession: true,
      hasPendingDecisions: false
    };

    expect(adaptationRebriefDisabledReason(base)).toBe('');
    expect(adaptationRebriefDisabledReason({ ...base, hasPendingDecisions: true }))
      .toBe('请先处理全部共创前置决策');
    expect(adaptationRebriefDisabledReason({ ...base, coCreate: { ...base.coCreate, input: '' } }))
      .toBe('请输入希望重新整理的方向');
  });

  it('lets analyzed adaptation sources accept a new novel library name before saving', () => {
    const activeProject = { id: 'project-1' };
    const adaptation = {
      sourceFile: { relative_path: 'sources/source.txt' },
      analysisStatus: 'done',
      librarySaveName: '',
      libraryLoadedName: ''
    };

    expect(canSaveAnalyzedNovelToLibrary({ activeProject, busy: false, adaptation })).toBe(true);
    expect(canSaveAnalyzedNovelToLibrary({
      activeProject,
      busy: false,
      adaptation: { ...adaptation, analysisStatus: 'running' }
    })).toBe(false);
    expect(canSaveAnalyzedNovelToLibrary({ activeProject, busy: true, adaptation })).toBe(false);
  });

  it('treats running co-create as current-project busy without requiring global busy', () => {
    expect(isCoCreateRequestBusy({ status: 'running' })).toBe(true);
    expect(isCoCreateRequestBusy({ status: 'waiting' })).toBe(false);
    expect(isCoCreateRequestBusy({ status: 'started' })).toBe(false);
  });

  it('normalizes full outline rows with camel, Pascal, and snake fallback fields', () => {
    const rows = getSnapshotOutlineRows({
      outline: [
        {
          chapter: 13,
          title: '雨巷录音',
          coreEvent: '主角确认录音来自未来',
          hook: '门外脚步声同步响起',
          scenes: ['事务所', '雨巷'],
          writtenWordCount: 4200,
          wordBudget: { targetRunes: 4500, minRunes: 3900, maxRunes: 5100 },
          sourceCoverage: { chapters: [4, 5], from: 4, to: 5, runes: 3800 }
        },
        {
          Chapter: 14,
          Title: '旧门牌',
          CoreEvent: '门牌指向失踪者家属',
          Scenes: ['老楼'],
          WrittenWordCount: 0,
          WordBudget: { TargetWords: 4000, MinWords: 3500, MaxWords: 4500 }
        }
      ]
    });

    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({
      chapter: 13,
      title: '雨巷录音',
      coreEvent: '主角确认录音来自未来',
      hook: '门外脚步声同步响起',
      writtenWordCount: 4200
    });
    expect(rows[0].wordBudget.targetRunes).toBe(4500);
    expect(rows[0].sourceCoverage.from).toBe(4);
    expect(rows[1].wordBudget.targetWords).toBe(4000);
  });

  it('selects written, current, and unwritten chapters for outline revision previews', () => {
    const snapshot = {
      phase: 'writing',
      CurrentChapter: 3,
      outline: [
        { chapter: 1, title: 'Written', writtenWordCount: 3200 },
        { chapter: 3, title: 'Current', writtenWordCount: 0 },
        { chapter: 7, title: 'Future', writtenWordCount: 0 }
      ]
    };

    expect(getOutlineRevisionView(snapshot, { active: true, chapter: '1' })).toMatchObject({
      active: true,
      chapter: 1,
      outlineRow: { title: 'Written', writtenWordCount: 3200 }
    });
    expect(getOutlineRevisionView(snapshot, { active: true, chapter: '3' })).toMatchObject({
      active: true,
      chapter: 3,
      outlineRow: { title: 'Current' }
    });
    expect(getOutlineRevisionView(snapshot, { active: true, chapter: '7' })).toMatchObject({
      active: true,
      chapter: 7,
      outlineRow: { title: 'Future' }
    });
    expect(getOutlineRevisionView(snapshot, { active: true, chapter: '2' })).toMatchObject({
      active: true,
      chapter: 1
    });
  });

  it('builds and validates single chapter outline revision payloads', () => {
    const snapshot = {
      outline: [
        { chapter: 1, title: 'Opening' },
        { chapter: 3, title: 'Reveal' }
      ]
    };

    expect(buildOutlineRevisionPayload({
      chapter: '3',
      instruction: '  move the clue into the hearing  '
    }, snapshot)).toEqual({
      ok: true,
      body: {
        chapter: 3,
        instruction: 'move the clue into the hearing'
      }
    });
    expect(buildOutlineRevisionPayload({ chapter: '2', instruction: 'rewrite' }, snapshot)).toMatchObject({ ok: false });
    expect(buildOutlineRevisionPayload({ chapter: '1', instruction: '   ' }, snapshot)).toMatchObject({ ok: false });
    expect(buildOutlineRevisionPayload({ chapter: '1', instruction: 'rewrite' }, { outline: [] })).toMatchObject({ ok: false });
  });

  it('describes queued rewrites and draft resets after outline revision', () => {
    expect(outlineRevisionSuccessMessage({ rewrite_queued: true, draft_reset: true }, 8))
      .toBe('第 8 章细纲已修改，草稿已重置并加入重写队列');
    expect(outlineRevisionSuccessMessage({ rewrite_queued: true }, 8))
      .toBe('第 8 章细纲已修改，已加入重写队列');
    expect(outlineRevisionSuccessMessage({ draft_reset: true }, 8))
      .toBe('第 8 章细纲已修改，草稿已重置');
    expect(outlineRevisionSuccessMessage({}, 8)).toBe('第 8 章细纲已修改');
  });

  it('shows completed-book chapter revision only after complete phase with outline rows', () => {
    expect(getCompletedBookChapterRevisionView({
      phase: 'complete',
      outline: [{ chapter: 1, title: 'Opening' }]
    })).toMatchObject({
      visible: true,
      phase: 'complete'
    });

    expect(getCompletedBookChapterRevisionView({
      phase: 'writing',
      outline: [{ chapter: 1, title: 'Opening' }]
    }).visible).toBe(false);

    expect(getCompletedBookChapterRevisionView({
      phase: 'complete',
      outline: []
    }).visible).toBe(false);
  });

  it('selects completed-book revision workspace chapter content', () => {
    const snapshot = {
      phase: 'complete',
      outline: [
        { chapter: 1, title: 'Opening', content: 'Chapter one body' },
        { Chapter: 3, Title: 'Reveal', Text: 'Chapter three body' }
      ]
    };

    expect(getCompletedBookSelectedChapterView(snapshot, { chapter: '3' })).toMatchObject({
      visible: true,
      chapter: 3,
      title: 'Reveal',
      content: 'Chapter three body'
    });

    expect(getCompletedBookSelectedChapterView(snapshot, { chapter: '99' })).toMatchObject({
      chapter: 1,
      title: 'Opening',
      content: 'Chapter one body'
    });
  });

  it('hides selected chapter workspace view outside completed books', () => {
    expect(getCompletedBookSelectedChapterView({
      phase: 'writing',
      outline: [{ chapter: 1, title: 'Opening', body: 'Draft body' }]
    }, { chapter: '1' })).toMatchObject({
      visible: false,
      chapter: 0,
      content: ''
    });
  });

  it('builds completed-book single chapter revision payloads', () => {
    const snapshot = {
      phase: 'complete',
      outline: [
        { chapter: 1, title: 'Opening' },
        { chapter: 3, title: 'Reveal' }
      ]
    };

    expect(buildChapterRevisionPayload({
      chapter: '3',
      mode: 'polish',
      instruction: '  keep the plot but smooth the prose  '
    }, snapshot)).toEqual({
      ok: true,
      body: {
        chapter: 3,
        mode: 'polish',
        instruction: 'keep the plot but smooth the prose'
      }
    });

    expect(buildChapterRevisionPayload({
      chapter: '1',
      mode: 'unknown',
      instruction: 'rewrite the opening'
    }, snapshot).body.mode).toBe('rewrite');
  });

  it('validates completed-book chapter revision payloads', () => {
    const snapshot = {
      phase: 'complete',
      outline: [{ chapter: 1, title: 'Opening' }]
    };

    expect(buildChapterRevisionPayload({
      chapter: '1',
      instruction: ''
    }, snapshot)).toMatchObject({ ok: false });

    expect(buildChapterRevisionPayload({
      chapter: '2',
      instruction: 'rewrite'
    }, snapshot)).toMatchObject({ ok: false });

    expect(buildChapterRevisionPayload({
      chapter: '1',
      instruction: 'rewrite'
    }, { ...snapshot, phase: 'writing' })).toMatchObject({ ok: false });
  });

  it('falls back to adaptation proposal chapters for old snapshots without Outline', () => {
    const review = getAdaptationProposalReview({
      adaptationProposal: {
        status: 'proposal',
        granularity: 'free',
        rewrite_policy: 'full_rewrite',
        brief: '改成雨城悬疑',
        chapters: [
          {
            chapter: 1,
            title: '旧录音',
            core_event: '发现时间错位',
            source_chapters: [1, 2],
            target_runes: 3000,
            target_min_runes: 2600,
            target_max_runes: 3400
          }
        ]
      }
    });

    expect(review.loaded).toBe(true);
    expect(review.proposalReady).toBe(true);
    expect(review.chapterCount).toBe(1);
    expect(review.chapters[0].sourceCoverage.chapters).toEqual([1, 2]);
    expect(review.chapters[0].wordBudget.targetRunes).toBe(3000);
  });

  it('groups detailed adaptation proposal chapters by volume for the status tree', () => {
    const structure = getSnapshotOutlineStructure({
      AdaptationProposal: {
        status: 'proposal',
        volumes: [
          { index: 1, title: 'Opening arc', target_from: 1, target_to: 2 },
          { index: 2, title: 'Reveal arc', target_from: 3, target_to: 4 }
        ],
        chapters: [
          { chapter: 1, title: 'One' },
          { chapter: 2, title: 'Two' },
          { chapter: 3, title: 'Three' },
          { chapter: 4, title: 'Four' }
        ]
      }
    });

    expect(structure.hasVolumes).toBe(true);
    expect(structure.groups).toHaveLength(2);
    expect(structure.groups[0].title).toBe('第 1 卷：Opening arc');
    expect(structure.groups[0].chapters.map((chapter) => chapter.chapter)).toEqual([1, 2]);
    expect(structure.groups[1].chapters.map((chapter) => chapter.chapter)).toEqual([3, 4]);
  });

  it('uses layered outline volumes before falling back to a flat status outline', () => {
    const structure = getSnapshotOutlineStructure({
      LayeredOutline: [
        { Index: 1, Title: 'First volume', Theme: 'Pressure', TargetFrom: 1, ChapterCount: 2 }
      ],
      Outline: [
        { Chapter: 1, Title: 'Opening' },
        { Chapter: 2, Title: 'Choice' }
      ]
    });

    expect(structure.hasVolumes).toBe(true);
    expect(structure.volumes[0]).toMatchObject({
      index: 1,
      title: 'First volume',
      targetFrom: 1,
      targetTo: 2
    });
    expect(structure.groups[0].chapters.map((chapter) => chapter.title)).toEqual(['Opening', 'Choice']);
  });

  it('keeps unvolumed proposals as one directly visible chapter group', () => {
    const structure = getSnapshotOutlineStructure({
      AdaptationProposal: {
        status: 'proposal',
        chapters: [
          { chapter: 1, title: 'Opening' },
          { chapter: 2, title: 'Turn' }
        ]
      }
    });

    expect(structure.hasVolumes).toBe(false);
    expect(structure.groups).toHaveLength(1);
    expect(structure.groups[0].chapters.map((chapter) => chapter.chapter)).toEqual([1, 2]);
  });

  it('deduplicates proposal volumes mirrored in summary and plan', () => {
    const review = getAdaptationProposalReview({
      ProposalSummary: {
        Status: 'proposal',
        ChapterCount: 4,
        Volumes: [
          { Index: 1, Title: 'Opening summary', TargetFrom: 1, TargetTo: 2 },
          { Index: 2, Title: 'Ending summary', TargetFrom: 3, TargetTo: 4 }
        ]
      },
      AdaptationProposal: {
        status: 'proposal',
        chapters: [
          { chapter: 1, title: 'One' },
          { chapter: 2, title: 'Two' },
          { chapter: 3, title: 'Three' },
          { chapter: 4, title: 'Four' }
        ],
        volumes: [
          { index: 1, title: 'Opening plan', target_from: 1, target_to: 2, source_from: 1, source_to: 1 },
          { index: 2, title: 'Ending plan', target_from: 3, target_to: 4, source_from: 2, source_to: 2 }
        ]
      }
    });

    expect(review.volumes).toHaveLength(2);
    expect(review.volumes.map((volume) => volume.index)).toEqual([1, 2]);
    expect(review.volumes.map((volume) => volume.title)).toEqual(['Opening plan', 'Ending plan']);
    expect(review.volumes.map((volume) => [volume.targetFrom, volume.targetTo])).toEqual([[1, 2], [3, 4]]);
  });

  it('surfaces staged volume review before detailed chapter proposal', () => {
    const review = getAdaptationProposalReview({
      proposal_summary: {
        status: 'proposal',
        granularity: 'chapter',
        rewrite_policy: 'preserve_mainline',
        brief: 'slow-burn mystery'
      },
      volume_review: {
        volumes: [
          {
            index: 1,
            title: 'Opening arc',
            target_from: 1,
            target_to: 6,
            source_from: 1,
            source_to: 20,
            plot: 'Move the discovery earlier.',
            key_beats: ['discovery', 'choice']
          }
        ]
      }
    });

    expect(review.loaded).toBe(true);
    expect(review.proposalReady).toBe(true);
    expect(review.volumeReviewReady).toBe(true);
    expect(review.chapterCount).toBe(6);
    expect(review.volumeReview.volumes[0]).toMatchObject({
      index: 1,
      title: 'Opening arc',
      targetFrom: 1,
      targetTo: 6,
      plot: 'Move the discovery earlier.',
      beats: ['discovery', 'choice']
    });
  });

  it('restores staged volume review from backend snapshot fields', () => {
    const review = getAdaptationProposalReview({
      VolumeReviewSummary: {
        Status: 'volume_review',
        Granularity: 'free',
        RewritePolicy: 'full_rewrite',
        Brief: 'restore staged plan',
        TargetChapterCount: 20,
        Volumes: [
          { Index: 1, Title: 'Opening volume', TargetFrom: 1, TargetTo: 8 }
        ]
      },
      AdaptationVolumeReview: {
        status: 'volume_review',
        granularity: 'free',
        rewrite_policy: 'full_rewrite',
        brief: 'restore staged plan',
        target_chapter_count: 20,
        volumes: [
          { index: 1, title: 'Opening volume', target_from: 1, target_to: 8 }
        ]
      }
    });

    expect(review.loaded).toBe(true);
    expect(review.proposalReady).toBe(true);
    expect(review.volumeReviewReady).toBe(true);
    expect(review.granularity).toBe('free');
    expect(review.chapterCount).toBe(20);
    expect(review.volumeReview.volumes[0].title).toBe('Opening volume');
  });

  it('hides free-mode source anchors from adaptation proposal labels', () => {
    const chapterReview = getAdaptationProposalReview({
      AdaptationProposal: {
        status: 'proposal',
        granularity: 'free',
        chapters: [
          {
            chapter: 1,
            title: 'Opening',
            source_chapters: [17],
            source_range: { from: 17, to: 17 }
          }
        ]
      }
    });
    const volumeReview = getAdaptationProposalReview({
      VolumeReviewSummary: {
        Status: 'volume_review',
        Granularity: 'free',
        TargetChapterCount: 8
      },
      AdaptationVolumeReview: {
        status: 'volume_review',
        granularity: 'free',
        volumes: [
          {
            index: 1,
            title: 'Opening volume',
            target_from: 1,
            target_to: 8,
            source_from: 17,
            source_to: 17
          }
        ]
      }
    });

    expect(chapterReview.chapters[0].sourceCoverage).toMatchObject({ from: 17, to: 17, chapters: [17] });
    expect(formatAdaptationSourceCoverageLabel(chapterReview.chapters[0].sourceCoverage, chapterReview.granularity)).toBe('');
    expect(formatAdaptationSourceCoverageLabel({ isAdded: true }, chapterReview.granularity, { addedLabel: '\u65b0\u589e\u6865\u6bb5' })).toBe('\u65b0\u589e\u6865\u6bb5');
    expect(volumeReview.volumeReviewReady).toBe(true);
    expect(volumeReview.volumeReview.volumes[0]).toMatchObject({ sourceFrom: 17, sourceTo: 17 });
    expect(formatAdaptationVolumeSourceLabel(volumeReview.volumeReview.volumes[0], volumeReview.granularity)).toBe('');
  });

  it('keeps source mapping labels for chapter and arc adaptation proposal modes', () => {
    expect(formatAdaptationSourceCoverageLabel({ from: 2, to: 4 }, 'chapter')).toBe('\u539f 2-4');
    expect(formatAdaptationSourceCoverageLabel({ chapters: [2, 4] }, 'arc')).toBe('\u539f 2,4');
    expect(formatAdaptationVolumeSourceLabel({ sourceFrom: 1, sourceTo: 20 }, 'arc')).toBe('\u539f 1-20');
    expect(formatAdaptationVolumeSourceLabel({ sourceLabel: '\u539f 3-9' }, 'chapter')).toBe('\u539f 3-9');
  });

  it('deduplicates staged volume review mirrored in summary and review payload', () => {
    const review = getAdaptationProposalReview({
      VolumeReviewSummary: {
        Status: 'volume_review',
        TargetChapterCount: 10,
        Volumes: [
          { Index: 1, Title: 'Dreamer', TargetFrom: 1, TargetTo: 2 },
          { Index: 2, Title: 'Shards', TargetFrom: 3, TargetTo: 4 }
        ]
      },
      AdaptationVolumeReview: {
        status: 'volume_review',
        volumes: [
          { index: 1, title: 'Dreamer', target_from: 1, target_to: 2, plot: 'Keep the opening intact.' },
          { index: 2, title: 'Shards', target_from: 3, target_to: 4, plot: 'Escalate the middle.' }
        ]
      }
    });

    expect(review.volumeReview.volumes).toHaveLength(2);
    expect(review.volumeReview.volumes.map((volume) => volume.index)).toEqual([1, 2]);
    expect(review.volumeReview.volumes.map((volume) => volume.title)).toEqual(['Dreamer', 'Shards']);
    expect(review.volumeReview.volumes[0].plot).toBe('Keep the opening intact.');
  });

  it('uses detailed chapters instead of staged volume review once details exist', () => {
    const review = getAdaptationProposalReview({
      volume_review: {
        volumes: [{ index: 1, title: 'Opening arc', target_from: 1, target_to: 2 }]
      },
      adaptation_proposal: {
        status: 'proposal',
        chapters: [
          { chapter: 1, title: 'Opening' },
          { chapter: 2, title: 'Reveal' }
        ]
      }
    });

    expect(review.proposalReady).toBe(true);
    expect(review.volumeReviewReady).toBe(false);
    expect(review.chapters).toHaveLength(2);
  });

  it('builds staged volume-review revision payloads', () => {
    expect(buildVolumeReviewRevisionPayload({
      revisionVolume: '2',
      revisionInstruction: 'raise the midpoint stakes'
    }, {
      volumes: [{ index: 1 }, { index: 2 }]
    })).toEqual({
      ok: true,
      body: {
        volume_index: 2,
        instruction: 'raise the midpoint stakes'
      }
    });

    expect(buildVolumeReviewRevisionPayload({
      revisionVolume: '99',
      revisionInstruction: 'fallback to first visible volume'
    }, {
      volumes: [{ index: 4 }]
    }).body.volume_index).toBe(4);
  });

  it('builds normal co-create planning revision payloads', () => {
    expect(buildCoCreatePlanningRevisionPayload({
      feedback: '  make the heroine proactive  '
    })).toEqual({
      ok: true,
      body: {
        feedback: 'make the heroine proactive'
      }
    });

    expect(buildCoCreatePlanningRevisionPayload({
      instruction: 'tighten the opening'
    }).body.feedback).toBe('tighten the opening');
    expect(buildCoCreatePlanningRevisionPayload({
      instruction: 'keep volume two quieter',
      volumeIndex: '2'
    }, {
      kind: 'volume_split',
      volumes: [{ index: 1, title: 'Setup' }, { index: 2, title: 'Pressure' }]
    })).toEqual({
      ok: true,
      body: {
        feedback: 'keep volume two quieter',
        instruction: 'keep volume two quieter',
        scope: 'volume',
        target: '第 2 卷：Pressure',
        volume_index: 2
      }
    });
    expect(buildCoCreatePlanningRevisionPayload({
      instruction: 'tighten the whole outline',
      scope: 'all'
    }, {
      kind: 'chapter_outline',
      chapters: [{ chapter: 1, title: 'Opening' }, { chapter: 2, title: 'Turn' }]
    })).toEqual({
      ok: true,
      body: {
        feedback: 'tighten the whole outline',
        instruction: 'tighten the whole outline',
        scope: 'all',
        target: '全卷'
      }
    });
    expect(buildCoCreatePlanningRevisionPayload({
      instruction: 'make chapter two more active',
      scope: 'chapter',
      chapter: '2'
    }, {
      kind: 'chapter_outline',
      chapters: [{ chapter: 1, title: 'Opening' }, { chapter: 2, title: 'Turn' }]
    }).body).toMatchObject({
      scope: 'chapter',
      target: '第 2 章：Turn',
      chapter: 2,
      from_chapter: 2,
      to_chapter: 2
    });
    expect(buildCoCreatePlanningRevisionPayload({ feedback: '   ' })).toEqual({
      ok: false,
      error: '请输入审核意见'
    });
  });

  it('keeps normal co-create planning review visible while pending or regenerating', () => {
    const pending = getCoCreatePlanningReview({
      planning_review: {
        status: 'pending',
        kind: 'chapter_outline',
        brief: 'review me',
        target_total_words: 5000
      },
      outline: [{ chapter: 1, title: 'Opening' }]
    });
    expect(pending.active).toBe(true);
    expect(pending.pending).toBe(true);
    expect(pending.collecting).toBe(false);

    const collecting = getCoCreatePlanningReview({
      PlanningReview: {
        Status: 'collecting',
        Kind: 'volume_split',
        Brief: 'regenerating',
        TargetTotalWords: 8000
      },
      Outline: [{ Chapter: 1, Title: 'Opening' }]
    });
    expect(collecting.active).toBe(true);
    expect(collecting.pending).toBe(false);
    expect(collecting.collecting).toBe(true);
    expect(collecting.revising).toBe(true);
  });

  it('projects the canonical foundation checkpoint into core and support review sections', () => {
    const review = getCoCreatePlanningReview({
      PlanningReview: {
        Status: 'pending',
        Kind: 'foundation',
        FoundationStatus: 'pending',
        FoundationRevision: 7,
        FoundationAuditSignature: 'audit-7',
        CoreCastSignature: 'core-3',
        FoundationGeneration: 2,
        FoundationFeedback: 'raise the cost'
      },
      Premise: 'A courier must expose a sealed city.',
      CoreCharacterIDs: ['lead'],
      CoreCastPreserved: true,
      CharacterDetails: [
        { id: 'lead', name: 'Lin', goal: 'expose the city', arc: 'trust allies' },
        { id: 'support', name: 'Mo', role: 'witness' }
      ],
      PlannedRelationships: [{ id: 'rel-1', source_character_id: 'lead', target_character_id: 'support', status: 'planned' }],
      WorldRules: [
        { id: 'hard-1', rule: 'No resurrection', strength: 'hard' },
        { id: 'soft-1', rule: 'Rain marks transitions', strength: 'soft' },
        { id: 'hr_identity', rule: 'Identity is stable', strength: 'soft' },
        { id: 'sr_tone', rule: 'Prefer restrained narration', strength: 'hard' }
      ]
    });

    expect(review.active).toBe(true);
    expect(review.foundationRevision).toBe(7);
    expect(review.foundationAuditSignature).toBe('audit-7');
    expect(review.coreCastPreserved).toBe(true);
    expect(review.coreCharacters.map((item) => item.id)).toEqual(['lead']);
    expect(review.supportingCharacters.map((item) => item.id)).toEqual(['support']);
    expect(review.plannedRelationships).toHaveLength(1);
    expect(review.hardWorldRules.map((item) => item.id)).toEqual(['hard-1', 'hr_identity']);
    expect(review.softWorldRules.map((item) => item.id)).toEqual(['soft-1', 'sr_tone']);
    expect(review.foundationFeedback).toBe('raise the cost');
  });

  it('keeps adaptation source evidence read-only and target foundation reviewable', () => {
    const review = getCoCreatePlanningReview({
      AdaptationFoundationReview: {
        State: 'pending', FoundationRevision: 4, Generation: 2,
        Binding: {
          SourceSignature: 'source-signature', TargetFoundationAuditSignature: 'target-audit',
          CoreCastSignature: 'cast-signature', AdaptationIntentHash: 'intent-hash', WorkflowRevision: 7
        }
      },
      AdaptationSourceFoundation: {
        Premise: 'immutable source premise', WorldRules: [{ ID: 'source-rule', Rule: 'source fact' }]
      },
      AdaptationCoreCast: {
        SourceDispositions: [{ SourceCharacterID: 'source-lead', Action: 'rename', TargetCharacterIDs: ['target-lead'] }]
      },
      TargetFoundation: {
        Premise: 'target decision',
        Characters: [{ ID: 'target-lead', Name: 'Target Lead', Role: 'hero' }],
        Relationships: [],
        WorldRules: [{ ID: 'target-rule', Rule: 'target decision rule', Strength: 'hard' }]
      }
    });

    expect(review.active).toBe(true);
    expect(review.adaptation).toBe(true);
    expect(review.sourcePremise).toBe('immutable source premise');
    expect(review.premise).toBe('target decision');
    expect(review.sourceSignature).toBe('source-signature');
    expect(review.foundationAuditSignature).toBe('target-audit');
    expect(review.sourceWorldRules[0].Rule).toBe('source fact');
    expect(review.sourceDispositions).toEqual([
      { SourceCharacterID: 'source-lead', Action: 'rename', TargetCharacterIDs: ['target-lead'] }
    ]);
    expect(review.hardWorldRules[0].Rule).toBe('target decision rule');
  });

  it('restores visible adaptation proposal state from a co-create commit snapshot', () => {
    const snapshot = {
      ProposalSummary: {
        Status: 'proposal',
        Granularity: 'free',
        RewritePolicy: 'full_rewrite',
        Brief: 'Make the mystery structure richer',
        ChapterCount: 2
      },
      AdaptationProposal: {
        status: 'proposal',
        granularity: 'free',
        rewrite_policy: 'full_rewrite',
        brief: 'Make the mystery structure richer',
        chapters: [
          { chapter: 1, title: 'Opening' },
          { chapter: 2, title: 'Reveal' }
        ]
      }
    };
    const previous = {
      sourceFile: { relative_path: 'source.txt' },
      mode: 'chapter',
      brief: '',
      proposalKey: '',
      startStatus: 'idle',
      startMessage: '',
      error: 'old error'
    };

    const next = applyAdaptationProposalSnapshot(previous, snapshot);

    expect(next.mode).toBe('free');
    expect(next.brief).toBe('Make the mystery structure richer');
    expect(next.error).toBe('');
    expect(next.proposalKey).toBe(buildAdaptationProposalKey(next));
    expect(getVisibleAdaptationProposalReview(snapshot, next).proposalReady).toBe(true);
  });

  it('restores a saved proposal even when the uploaded source file cannot be reconstructed', () => {
    const snapshot = {
      ProposalSummary: {
        Status: 'proposal',
        Granularity: 'free',
        RewritePolicy: 'full_rewrite',
        Brief: 'Keep the mystery safe and long-form',
        ChapterCount: 2
      },
      AdaptationProposal: {
        status: 'proposal',
        granularity: 'free',
        rewrite_policy: 'full_rewrite',
        brief: 'Keep the mystery safe and long-form',
        chapters: [
          { chapter: 1, title: 'Opening' },
          { chapter: 2, title: 'Reveal' }
        ]
      }
    };

    const next = applyAdaptationProposalSnapshot({
      sourceFile: null,
      mode: 'chapter',
      brief: '',
      proposalKey: '',
      startStatus: 'idle',
      startMessage: '',
      error: ''
    }, snapshot);

    expect(next.sourceFile).toBeNull();
    expect(next.mode).toBe('free');
    expect(next.proposalKey).toBe(buildAdaptationProposalKey(next));
    expect(getVisibleAdaptationProposalReview(snapshot, next).proposalReady).toBe(true);
    expect(getVisibleAdaptationProposalReview(snapshot, { ...next, brief: 'changed' }).stale).toBe(true);
  });

  it('hides stale adaptation proposals after source upload or proposal input changes', () => {
    const snapshot = {
      ProposalSummary: {
        Status: 'proposal',
        Granularity: 'free',
        RewritePolicy: 'full_rewrite',
        Brief: 'Make it a mystery',
        ChapterCount: 1
      },
      AdaptationProposal: {
        status: 'proposal',
        granularity: 'free',
        rewrite_policy: 'full_rewrite',
        brief: 'Make it a mystery',
        chapters: [{ chapter: 1, title: 'Old plan' }]
      }
    };
    const currentAdaptation = {
      sourceFile: { relative_path: 'old.txt' },
      mode: 'free',
      brief: 'Make it a mystery'
    };
    currentAdaptation.proposalKey = buildAdaptationProposalKey(currentAdaptation);

    expect(getVisibleAdaptationProposalReview(snapshot, currentAdaptation).proposalReady).toBe(true);

    const uploadedSnapshot = clearAdaptationProposalSnapshot(snapshot);
    const afterUpload = {
      sourceFile: { relative_path: 'new.txt' },
      mode: 'free',
      brief: 'Make it a mystery',
      proposalKey: ''
    };
    const afterUploadReview = getVisibleAdaptationProposalReview(uploadedSnapshot, afterUpload);

    expect(getAdaptationProposalReview(uploadedSnapshot).loaded).toBe(false);
    expect(afterUploadReview.loaded).toBe(false);
    expect(afterUploadReview.proposalReady).toBe(false);

    const changedBriefReview = getVisibleAdaptationProposalReview(snapshot, {
      ...currentAdaptation,
      brief: 'Make it a romance'
    });

    expect(changedBriefReview.loaded).toBe(false);
    expect(changedBriefReview.proposalReady).toBe(false);
    expect(changedBriefReview.stale).toBe(true);
  });

  it('builds revision payloads for chapter, range, and volume targets', () => {
    const proposal = { chapterCount: 12, volumes: [{ index: 1 }, { index: 2 }] };

    expect(buildAdaptationRevisionPayload({
      revisionMode: 'chapter',
      revisionChapter: '3',
      revisionInstruction: 'strengthen the hook'
    }, proposal)).toMatchObject({
      ok: true,
      body: {
        target: '第3章',
        from_chapter: 3,
        to_chapter: 3,
        instruction: 'strengthen the hook'
      }
    });

    expect(buildAdaptationRevisionPayload({
      revisionMode: 'range',
      revisionFromChapter: '8',
      revisionToChapter: '5',
      revisionInstruction: 'smooth the arc'
    }, proposal)).toMatchObject({
      ok: true,
      body: {
        target: '第5-8章',
        from_chapter: 5,
        to_chapter: 8
      }
    });

    expect(buildAdaptationRevisionPayload({
      revisionMode: 'volume',
      revisionVolume: 'all',
      revisionInstruction: 'rebalance all volumes'
    }, proposal)).toMatchObject({
      ok: true,
      body: {
        target: '全卷',
        volume_index: -1
      }
    });

    expect(buildAdaptationRevisionPayload({
      revisionMode: 'volume',
      revisionVolume: '2',
      revisionInstruction: 'raise the midpoint stakes'
    }, proposal)).toMatchObject({
      ok: true,
      body: {
        target: '第2卷',
        volume_index: 2
      }
    });
  });

  it('reports imported simulation profiles as loaded after refresh', () => {
    const profile = getSimulationProfileStatus({
      SimulationSummary: {
        Loaded: true,
        Version: 'simulation_profile.v2',
        ProfileDigest: 'abc123def456',
        SourceCount: 2,
        ReportCount: 2,
        CoveragePercent: 100,
        HealthState: 'fresh',
        SelectedMode: 'reinforced',
        EffectiveMode: 'reinforced',
        FeatureCounts: { Stable: 4, Local: 1, Outlier: 1, Contradictory: 0 },
        Actions: {
          Rescan: { Enabled: true },
          Resynthesize: { Enabled: true },
          Reanalyze: { Enabled: false, Reason: '需要本地语料' }
        },
        Contract: {
          Revision: 3,
          Status: 'active',
          Current: true,
          FoundationRevision: 7,
          Views: [{ Role: 'writer', Phase: 'chapter', Should: 2, ByteBudget: 2000 }]
        },
        Check: {
          State: 'partial',
          Chapter: 4,
          DraftCurrent: true,
          RiskCount: 1,
          Risks: [{ Type: 'rare_ngram', DraftExcerpt: '当前草稿片段', StartRune: 8, LengthRunes: 6 }]
        },
        ModePreviews: [{
          Mode: 'reinforced',
          Status: 'active',
          Roles: [{ Role: 'writer', Phase: 'chapter', FeatureCount: 9, ByteBudget: 5600 }]
        }]
      }
    });

    expect(profile.loaded).toBe(true);
    expect(profile.sourceCount).toBe(2);
    expect(profile.reportCount).toBe(2);
    expect(profile.healthState).toBe('fresh');
    expect(profile.effectiveMode).toBe('reinforced');
    expect(profile.featureCounts.stable).toBe(4);
    expect(profile.actions.reanalyze.enabled).toBe(false);
    expect(profile.contract.foundationRevision).toBe(7);
    expect(profile.contract.views[0].byteBudget).toBe(2000);
    expect(profile.check.risks[0].draftExcerpt).toBe('当前草稿片段');
    expect(profile.modePreviews[0].roles[0].featureCount).toBe(9);
    expect(JSON.stringify(profile)).not.toContain('a.txt');
  });

  it('labels every canonical health, contract, and check state without color-only meaning', () => {
    expect(['fresh', 'stale', 'portable_only', 'legacy', 'invalid'].map(simulationHealthLabel)).toEqual([
      '新鲜可用', '已过期', '仅 portable', 'legacy 兼容', '无效'
    ]);
    expect(['active', 'degraded', 'inactive'].map(simulationContractStatusLabel)).toEqual([
      '已生效', '降级生效', '未生效'
    ]);
    expect(['pass', 'partial', 'not_run', 'stale', 'fail', 'error'].map(simulationCheckStateLabel)).toEqual([
      '检测通过', '部分能力', '未运行', '结果过期', '检测失败', '检查不可用'
    ]);
  });

  it('does not start the simulation library save flow automatically when analysis completes', () => {
    const next = applyHostEventToSimulationState({
      files: [],
      uploadMessage: '',
      analysisStatus: 'running',
      analysisEvents: [],
      importStatus: 'idle',
      importEvents: [],
      importMessage: '',
      libraryQuery: '',
      libraryStatus: 'idle',
      libraryItems: [],
      libraryMessage: '',
      libraryError: '',
      saveName: '',
      saveStatus: 'running',
      saveError: 'old error',
      error: ''
    }, {
      type: 'host_event',
      event: {
        category: 'SIMULATE',
        kind: 'done',
        level: 'success',
        summary: '仿写画像已完成'
      }
    });

    expect(next.analysisStatus).toBe('done');
    expect(next.saveStatus).toBe('idle');
    expect(next.saveError).toBe('');
  });

  it('keeps import merge progress in the load-profile workflow', () => {
    const next = applyHostEventToSimulationState({
      files: [],
      uploadMessage: '',
      analysisStatus: 'idle',
      analysisEvents: [],
      importStatus: 'running',
      importEvents: [],
      importMessage: '',
      libraryQuery: '',
      libraryStatus: 'idle',
      libraryItems: [],
      libraryMessage: '',
      libraryError: '',
      saveName: '',
      saveStatus: 'idle',
      saveError: '',
      error: ''
    }, {
      type: 'host_event',
      event: {
        category: 'SIMULATE',
        kind: 'merge',
        level: 'info',
        summary: '分批重合成仿写画像 24/49'
      }
    });

    expect(next.importStatus).toBe('running');
    expect(next.importEvents).toHaveLength(1);
    expect(next.importEvents[0].message).toContain('分批重合成');
    expect(next.analysisStatus).toBe('idle');
    expect(next.analysisEvents).toHaveLength(0);
  });

  it('restores uploaded simulation source files from refresh responses', () => {
    expect(simulationFilesFromResponse({
      files: [
        { name: 'a-source.txt', size: 12, relative_path: 'a-source.txt' },
        { Name: 'b-source.md', Size: 34, RelativePath: 'b-source.md' }
      ]
    })).toEqual([
      {
        name: 'a-source.txt',
        original_name: 'a-source.txt',
        size: 12,
        relative_path: 'a-source.txt'
      },
      {
        name: 'b-source.md',
        original_name: 'b-source.md',
        size: 34,
        relative_path: 'b-source.md'
      }
    ]);

    expect(simulationFilesFromResponse({ source_files: ['nested/c-source.txt'] })).toEqual([
      {
        name: 'c-source.txt',
        original_name: 'c-source.txt',
        size: 0,
        relative_path: 'nested/c-source.txt'
      }
    ]);
  });

  it('restores running simulation analysis state from project snapshots', () => {
    const next = restoreSimulationProjectState({
      libraryQuery: '',
      libraryStatus: 'idle',
      libraryItems: [],
      libraryMessage: '',
      libraryError: ''
    }, {
      analysis_status: 'running',
      analysis_events: [{ stage: 'analyze', message: '分析仿写语料 1/10' }],
      import_status: 'idle',
      files: [{ name: 'part_001.txt', size: 42, relative_path: 'part_001.txt' }]
    });

    expect(next.analysisStatus).toBe('running');
    expect(next.analysisEvents).toHaveLength(1);
    expect(next.files[0].name).toBe('part_001.txt');
  });

  it('shows project default model instead of the global runtime default when a project is active', () => {
    const visible = resolveVisibleDefaultModel(
      { id: 'project-1' },
      { config: { provider: 'custom-openai', model: 'deepseek-v4-pro' } },
      {
        providers: [
          { name: 'custom-openai', models: ['deepseek-v4-pro'] },
          { name: 'deepseek', models: ['deepseek-v4-pro'] }
        ],
        roles: [
          { role: 'default', provider: 'deepseek', model: 'deepseek-v4-pro', explicit: true }
        ]
      }
    );

    expect(visible.provider).toBe('deepseek');
    expect(visible.model).toBe('deepseek-v4-pro');
  });

  it('summarizes simulation profiles without timestamps or source filenames', () => {
    expect(simulationProfileSummaryText({
      loaded: true,
      sourceCount: 143,
      updatedAt: '2026-07-02T15:28:13+08:00',
      sourceFiles: ['001_第一章_新的科目.txt']
    })).toBe('143 篇语料');

    expect(simulationProfileSummaryText({ loaded: true, sourceCount: 0 })).toBe('画像已加载');
    expect(simulationProfileSummaryText({ loaded: false })).toBe('上传或导入画像后会出现在这里');
  });
});
