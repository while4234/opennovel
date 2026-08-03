const STAGE_ALIASES = {
  source_importing: 'source_importing',
  importing: 'source_importing',
  source_ready: 'source_ready',
  draft_collecting: 'draft_collecting',
  draft_ready: 'draft_collecting',
  proposal_generating: 'proposal_generating',
  proposal_review_pending: 'proposal_review_pending',
  volume_review_pending: 'volume_review_pending',
  volumes_review_pending: 'volume_review_pending',
  outline_generating: 'outline_generating',
  outlines_generating: 'outline_generating',
  outline_review_pending: 'outline_review_pending',
  outlines_review_pending: 'outline_review_pending',
  ready_to_write: 'ready_to_write',
  writing: 'writing',
  paused: 'paused',
  failed: 'failed'
};

const STAGE_STEP_INDEX = {
  source_importing: 0,
  source_ready: 1,
  draft_collecting: 1,
  proposal_generating: 2,
  proposal_review_pending: 2,
  volume_review_pending: 2,
  outline_generating: 3,
  outline_review_pending: 3,
  ready_to_write: 4,
  writing: 4,
  paused: 4,
  failed: 0
};

export const CONTINUATION_STEPS = [
  { id: 'source', label: '原作' },
  { id: 'draft', label: 'Draft' },
  { id: 'proposal', label: '提案 / 分卷' },
  { id: 'outlines', label: '章节细纲' },
  { id: 'start', label: '开写' }
];

function firstValue(source, ...keys) {
  if (!source || typeof source !== 'object') {
    return undefined;
  }
  for (const key of keys) {
    if (source[key] !== undefined && source[key] !== null) {
      return source[key];
    }
  }
  return undefined;
}

function asObject(value) {
  return value && typeof value === 'object' && !Array.isArray(value) ? value : null;
}

function asArray(value) {
  return Array.isArray(value) ? value : [];
}

function normalizeStage(value) {
  const stage = String(value || '').trim().toLowerCase();
  return STAGE_ALIASES[stage] || stage || 'empty';
}

function sourceFromWorkflow(workflow) {
  return asObject(firstValue(workflow, 'source', 'Source', 'source_manifest', 'sourceManifest', 'SourceManifest'));
}

export function continuationSnapshotFrom(value, fallback = null) {
  const root = asObject(value);
  const nested = asObject(firstValue(root, 'continuation', 'Continuation'));
  const candidate = nested || root;
  const explicitWorkflow = asObject(firstValue(candidate, 'workflow', 'Workflow'));
  if (explicitWorkflow || nested) {
    return candidate;
  }
  if (root && firstValue(root, 'stage', 'Stage', 'revision', 'Revision', 'source', 'Source', 'source_file', 'sourceFile', 'SourceFile') !== undefined) {
    return root;
  }
  return asObject(fallback);
}

export function normalizeContinuationSnapshot(value, fallback = null) {
  const snapshot = continuationSnapshotFrom(value, fallback);
  if (!snapshot) {
    return {
      exists: false,
      stage: 'empty',
      resumeStage: '',
      revision: 0,
      structure: '',
      shortStory: false,
      source: null,
      sourceFile: null,
      baseChapterCount: 0,
      nextChapter: 0,
      draft: '',
      proposal: null,
      volumes: [],
      outlines: [],
      outlinePlan: null,
      plan: null,
      lastError: ''
    };
  }
  const workflow = asObject(firstValue(snapshot, 'workflow', 'Workflow')) || snapshot;
  const source = sourceFromWorkflow(workflow);
  const stage = normalizeStage(firstValue(workflow, 'stage', 'Stage', 'status', 'Status', 'state', 'State'));
  const proposal = asObject(firstValue(snapshot, 'proposal', 'Proposal') || firstValue(workflow, 'proposal', 'Proposal'));
  const plan = asObject(firstValue(snapshot, 'plan', 'Plan') || firstValue(workflow, 'plan', 'Plan'));
  const structure = String(firstValue(proposal, 'structure', 'Structure') || firstValue(workflow, 'structure', 'Structure') || firstValue(plan, 'structure', 'Structure') || '').trim().toLowerCase();
  const volumes = asArray(firstValue(snapshot, 'volumes', 'Volumes', 'volume_plan', 'volumePlan', 'VolumePlan') || firstValue(plan, 'volumes', 'Volumes') || firstValue(proposal, 'volumes', 'Volumes'));
  const outlineValue = firstValue(snapshot, 'outlines', 'Outlines', 'chapter_outlines', 'chapterOutlines', 'ChapterOutlines') || firstValue(plan, 'outlines', 'Outlines');
  const outlinePlan = asObject(outlineValue);
  const directOutlines = asArray(outlineValue);
  const outlineVolumes = asArray(firstValue(outlinePlan, 'volumes', 'Volumes'));
  const outlines = directOutlines.length > 0
    ? directOutlines
    : asArray(firstValue(outlinePlan, 'chapters', 'Chapters') || firstValue(plan, 'chapters', 'Chapters')).concat(
      outlineVolumes.flatMap((volume) => {
        const directChapters = asArray(firstValue(volume, 'chapters', 'Chapters'));
        return directChapters.length > 0
          ? directChapters
          : asArray(firstValue(volume, 'arcs', 'Arcs')).flatMap((arc) => asArray(firstValue(arc, 'chapters', 'Chapters')));
      })
    );
  const sourceFile = asObject(firstValue(workflow, 'source_file', 'sourceFile', 'SourceFile') || firstValue(source, 'file', 'File'));
  const baseChapterCount = Number(firstValue(workflow, 'base_chapter_count', 'baseChapterCount', 'BaseChapterCount') || firstValue(source, 'chapter_count', 'chapterCount', 'ChapterCount')) || 0;
  const nextChapter = Number(firstValue(workflow, 'next_chapter', 'nextChapter', 'NextChapter')) || (baseChapterCount > 0 ? baseChapterCount + 1 : 0);
  const resumeStage = normalizeStage(firstValue(workflow, 'resume_stage', 'resumeStage', 'ResumeStage'));
  return {
    exists: stage !== 'empty' || Boolean(source || sourceFile),
    stage,
    resumeStage: resumeStage === 'empty' ? '' : resumeStage,
    revision: Number(firstValue(workflow, 'revision', 'Revision')) || 0,
    structure,
    shortStory: structure === 'single' || structure === 'short' || firstValue(workflow, 'split_volumes', 'splitVolumes', 'SplitVolumes') === false,
    source,
    sourceFile,
    baseChapterCount,
    nextChapter,
    draft: String(firstValue(workflow, 'draft', 'Draft', 'draft_prompt', 'draftPrompt', 'DraftPrompt') || ''),
    proposal,
    volumes,
    outlines,
    outlinePlan,
    plan,
    lastError: String(firstValue(workflow, 'last_error', 'lastError', 'LastError', 'error', 'Error') || '')
  };
}

export function continuationReviewKind(value) {
  const { stage } = normalizeContinuationSnapshot(value);
  if (stage === 'proposal_review_pending') {
    return 'proposal';
  }
  if (stage === 'volume_review_pending') {
    return 'volumes';
  }
  if (stage === 'outline_review_pending') {
    return 'outlines';
  }
  return '';
}

export function continuationNeedsReview(value) {
  return Boolean(continuationReviewKind(value));
}

export function continuationNeedsConfirmation(value) {
  const snapshot = normalizeContinuationSnapshot(value);
  return continuationNeedsReview(snapshot) || snapshot.stage === 'ready_to_write';
}

export function continuationCanRetry(value) {
  return ['paused', 'failed'].includes(normalizeContinuationSnapshot(value).stage);
}

export function continuationCanResume(value) {
  return ['proposal_generating', 'outline_generating', 'paused', 'failed', 'writing']
    .includes(normalizeContinuationSnapshot(value).stage);
}

export function deriveContinuationSteps(value) {
  const snapshot = normalizeContinuationSnapshot(value);
  let activeIndex = STAGE_STEP_INDEX[snapshot.stage] ?? 0;
  if (snapshot.stage === 'paused' || snapshot.stage === 'failed') {
    if (snapshot.resumeStage && STAGE_STEP_INDEX[snapshot.resumeStage] !== undefined) {
      activeIndex = STAGE_STEP_INDEX[snapshot.resumeStage];
    } else if (snapshot.outlines.length > 0) {
      activeIndex = 3;
    } else if (snapshot.proposal || snapshot.volumes.length > 0) {
      activeIndex = 2;
    } else if (snapshot.source || snapshot.sourceFile || snapshot.baseChapterCount > 0) {
      activeIndex = 1;
    }
  }
  return CONTINUATION_STEPS.map((step, index) => ({
    ...step,
    status: index < activeIndex || (snapshot.stage === 'writing' && index === activeIndex)
      ? 'complete'
      : index === activeIndex
        ? 'active'
        : 'pending',
    skipped: snapshot.shortStory && step.id === 'proposal' && ['outline_generating', 'outline_review_pending', 'ready_to_write', 'writing'].includes(snapshot.stage)
  }));
}

export function continuationRequiredReviewStages(value) {
  const snapshot = normalizeContinuationSnapshot(value);
  return snapshot.shortStory
    ? ['proposal_review_pending', 'outline_review_pending']
    : ['proposal_review_pending', 'volume_review_pending', 'outline_review_pending'];
}

export function withExpectedRevision(value, payload = {}) {
  const revision = normalizeContinuationSnapshot(value).revision;
  return {
    ...payload,
    expected_revision: revision
  };
}

export function buildContinuationOutlineScopePayload(state = {}) {
  const scope = String(state.scope || 'all');
  if (scope === 'all') {
    return { body: { scope: 'all' }, error: '' };
  }
  if (scope === 'volume') {
    const volumeIndex = Number(state.volumeIndex);
    return Number.isInteger(volumeIndex) && volumeIndex > 0
      ? { body: { scope: 'volume', volume_index: volumeIndex }, error: '' }
      : { body: null, error: '请输入有效的分卷序号' };
  }
  if (scope === 'chapter') {
    const chapter = Number(state.chapter);
    return Number.isInteger(chapter) && chapter > 0
      ? { body: { scope: 'chapter', chapter }, error: '' }
      : { body: null, error: '请输入有效的章节序号' };
  }
  if (scope === 'range') {
    const fromChapter = Number(state.fromChapter);
    const toChapter = Number(state.toChapter);
    if (!Number.isInteger(fromChapter) || fromChapter <= 0 || !Number.isInteger(toChapter) || toChapter < fromChapter) {
      return { body: null, error: '请输入有效的章节范围' };
    }
    return {
      body: { scope: 'chapter', from_chapter: fromChapter, to_chapter: toChapter },
      error: ''
    };
  }
  return { body: null, error: '请选择修改范围' };
}

export function continuationUploadSuccessMessage(response, fallbackName = '') {
  const workflow = normalizeContinuationSnapshot(response);
  const sourceFile = workflow.sourceFile;
  const name = String(firstValue(sourceFile, 'name', 'Name') || fallbackName || '').trim();
  const chapterLabel = workflow.baseChapterCount > 0 ? `，共 ${workflow.baseChapterCount} 章` : '';
  return `原作已导入${name ? `：${name}` : ''}${chapterLabel}。请先确定续写 Draft。`;
}
