import { describe, expect, it } from 'vitest';
import {
  buildContinuationOutlineScopePayload,
  continuationCanResume,
  continuationNeedsConfirmation,
  continuationRequiredReviewStages,
  continuationUploadSuccessMessage,
  deriveContinuationSteps,
  normalizeContinuationSnapshot,
  withExpectedRevision
} from './continuation.js';

describe('continuation workflow derivation', () => {
  it('derives the persisted review stage and active step', () => {
    const workflow = normalizeContinuationSnapshot({
      continuation: {
        stage: 'volume_review_pending',
        revision: 8,
        structure: 'volumes',
        base_chapter_count: 42
      }
    });

    expect(workflow).toMatchObject({
      stage: 'volume_review_pending',
      revision: 8,
      baseChapterCount: 42,
      shortStory: false
    });
    expect(deriveContinuationSteps(workflow).map((step) => step.status)).toEqual([
      'complete',
      'complete',
      'active',
      'pending',
      'pending'
    ]);
  });

  it('lets a short story skip volume review but never chapter-outline review', () => {
    const workflow = {
      stage: 'outline_review_pending',
      structure: 'single',
      revision: 3
    };

    expect(continuationRequiredReviewStages(workflow)).toEqual([
      'proposal_review_pending',
      'outline_review_pending'
    ]);
    const steps = deriveContinuationSteps(workflow);
    expect(steps.find((step) => step.id === 'proposal')).toMatchObject({ status: 'complete', skipped: true });
    expect(steps.find((step) => step.id === 'outlines')).toMatchObject({ status: 'active', skipped: false });
    expect(steps.find((step) => step.id === 'start')).toMatchObject({ status: 'pending' });
  });

  it('keeps a paused workflow on its persisted resume stage', () => {
    const steps = deriveContinuationSteps({
      workflow: {
        stage: 'paused',
        resume_stage: 'outline_generating',
        base_chapter_count: 20
      }
    });

    expect(steps.find((step) => step.id === 'outlines').status).toBe('active');
    expect(steps.find((step) => step.id === 'start').status).toBe('pending');
  });

  it.each(['proposal_generating', 'outline_generating', 'paused', 'failed', 'writing'])(
    'routes %s recovery through the workspace toolbar',
    (stage) => {
      expect(continuationCanResume({ stage })).toBe(true);
    }
  );

  it('does not treat review stages as recovery actions', () => {
    expect(continuationCanResume({ stage: 'proposal_review_pending' })).toBe(false);
  });

  it.each([
    'proposal_review_pending',
    'volume_review_pending',
    'outline_review_pending',
    'ready_to_write'
  ])('treats %s as requiring user confirmation', (stage) => {
    expect(continuationNeedsConfirmation({ stage })).toBe(true);
  });

  it.each(['proposal_generating', 'outline_generating', 'writing'])(
    'does not treat %s as requiring user confirmation',
    (stage) => {
      expect(continuationNeedsConfirmation({ stage })).toBe(false);
    }
  );

  it('adds the current revision to every mutation payload', () => {
    expect(withExpectedRevision({ stage: 'proposal_review_pending', revision: 11 }, {
      instruction: '强化第二卷的冲突'
    })).toEqual({
      instruction: '强化第二卷的冲突',
      expected_revision: 11
    });
  });

  it('builds single chapter, range, volume, and all outline revision scopes', () => {
    expect(buildContinuationOutlineScopePayload({ scope: 'chapter', chapter: '43' }).body).toEqual({
      scope: 'chapter',
      chapter: 43
    });
    expect(buildContinuationOutlineScopePayload({ scope: 'range', fromChapter: '44', toChapter: '47' }).body).toEqual({
      scope: 'chapter',
      from_chapter: 44,
      to_chapter: 47
    });
    expect(buildContinuationOutlineScopePayload({ scope: 'volume', volumeIndex: '2' }).body).toEqual({
      scope: 'volume',
      volume_index: 2
    });
    expect(buildContinuationOutlineScopePayload({ scope: 'all' }).body).toEqual({ scope: 'all' });
  });

  it('flattens volume arc chapters and derives the first continuation chapter', () => {
    const workflow = normalizeContinuationSnapshot({
      workflow: { stage: 'outline_review_pending', base_chapter_count: 80, revision: 9 },
      proposal: { structure: 'volumes' },
      outlines: {
        structure: 'volumes',
        volumes: [{
          index: 1,
          arcs: [{ chapters: [{ chapter: 81, title: '重逢' }, { chapter: 82, title: '追踪' }] }]
        }]
      }
    });

    expect(workflow.nextChapter).toBe(81);
    expect(workflow.outlines.map((chapter) => chapter.title)).toEqual(['重逢', '追踪']);
  });

  it('describes upload as imported and waiting for Draft without claiming resume', () => {
    const message = continuationUploadSuccessMessage({
      continuation: {
        stage: 'source_ready',
        base_chapter_count: 18,
        source_file: { name: '原作.txt' }
      }
    });

    expect(message).toContain('原作已导入');
    expect(message).toContain('请先确定续写 Draft');
    expect(message).not.toContain('恢复');
  });
});
