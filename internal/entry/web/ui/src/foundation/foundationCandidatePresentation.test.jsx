import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { getCoCreatePlanningReview } from '../App.jsx';
import FoundationReviewWorkspace from './FoundationReviewWorkspace.jsx';

describe('unpublished Character candidate presentation', () => {
  const snapshot = {
    PlanningReview: {
      Status: 'collecting',
      Kind: 'foundation',
      Brief: '一名调查员必须在保护家人的同时揭露阴谋。'
    },
    CharacterWorkflow: {
      CandidateRevision: 4,
      AnalysisStatus: 'candidate_ready',
      ReviewStatus: 'passed',
      ConfirmationStatus: 'unconfirmed',
      Candidate: {
        Premise: '一名调查员必须在保护家人的同时揭露阴谋。',
        Characters: [
          { ID: 'hero', Name: '林澈', Tier: 'core', Role: '主角调查员' },
          { ID: 'ally', Name: '顾衡', Tier: 'important', Role: '搭档' }
        ],
        Relationships: [
          { ID: 'rel-1', SourceCharacterID: 'hero', TargetCharacterID: 'ally', Label: '调查搭档' }
        ]
      }
    }
  };

  it('uses reviewed candidates instead of an empty published Foundation', () => {
    const review = getCoCreatePlanningReview(snapshot);
    expect(review.characterConfirmationRequired).toBe(true);
    expect(review.collecting).toBe(false);
    expect(review.pending).toBe(true);
    expect(review.coreCharacters.map((character) => character.Name)).toEqual(['林澈']);
    expect(review.supportingCharacters.map((character) => character.Name)).toEqual(['顾衡']);
    expect(review.plannedRelationships).toHaveLength(1);
  });

  it('labels candidate content and provides the direct confirmation action', () => {
    const review = getCoCreatePlanningReview(snapshot);
    const markup = renderToStaticMarkup(
      <FoundationReviewWorkspace review={review} selectedTab="characters" />
    );
    expect(markup).toContain('正在展示未发布的角色候选');
    expect(markup).toContain('林澈');
    expect(markup).toContain('顾衡');
    expect(markup).toContain('确认角色卡');
    expect(markup).not.toContain('本轮没有核心角色');
  });
});
