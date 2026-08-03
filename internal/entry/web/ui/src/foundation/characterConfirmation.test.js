import { describe, expect, it } from 'vitest';
import {
  characterConfirmationRequiredFromSnapshot,
  characterConfirmationRequiredFromWorkspace,
  projectNextAction
} from './characterConfirmation.js';

describe('character confirmation routing', () => {
  const waitingSnapshot = {
    CharacterWorkflow: {
      analysis_status: 'candidate_ready',
      review_status: 'passed',
      confirmation_status: 'unconfirmed'
    }
  };

  it('routes reviewed, unconfirmed candidates to the central Foundation review workspace', () => {
    expect(characterConfirmationRequiredFromSnapshot(waitingSnapshot)).toBe(true);
    expect(projectNextAction(waitingSnapshot)).toMatchObject({
      code: 'confirm_character_candidate',
      centerView: 'writing',
      sideView: 'cocreate',
      tab: 'characters',
      anchor: 'foundation-review-confirm-character'
    });
  });

  it('does not route confirmed or still-reviewing candidates', () => {
    expect(characterConfirmationRequiredFromSnapshot({
      CharacterWorkflow: {
        analysis_status: 'candidate_ready',
        review_status: 'passed',
        confirmation_status: 'confirmed'
      }
    })).toBe(false);
    expect(characterConfirmationRequiredFromSnapshot({
      CharacterWorkflow: {
        analysis_status: 'candidate_ready',
        review_status: 'in_progress',
        confirmation_status: 'unconfirmed'
      }
    })).toBe(false);
  });

  it('uses the Character workspace confirm permission as the UI gate', () => {
    expect(characterConfirmationRequiredFromWorkspace({
      candidate: { revision: 4 },
      confirmationStatus: 'unconfirmed',
      allowedOperations: ['confirm']
    })).toBe(true);
    expect(characterConfirmationRequiredFromWorkspace({
      candidate: { revision: 4 },
      confirmationStatus: 'unconfirmed',
      allowedOperations: ['review']
    })).toBe(false);
  });
});
