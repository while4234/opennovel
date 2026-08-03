import { describe, expect, it } from 'vitest';
import { classifyManuscriptMutation, manuscriptMutationSSEData, normalizeManuscriptMutationEvent } from './manuscript-events.js';

const chapter = 'ch_0123456789abcdef0123456789abcdef';

describe('manuscript mutation event contract', () => {
  it.each([
    ['generate', 'generation'],
    ['audit', 'audit'],
    ['confirm_impacts', 'structure_publish'],
    ['publish', 'prose_publish'],
    ['cancel', 'cancel']
  ])('classifies %s with stable identity', (action, scope) => {
    const event = classifyManuscriptMutation('/api/projects/p/manuscript/revision/command', {
      method: 'POST', body: JSON.stringify({ action, chapter_id: chapter })
    });
    expect(event).toEqual({ scope, stable_id: chapter });
  });

  it('derives stable identity from the production response and rejects broad events', () => {
    expect(classifyManuscriptMutation('/api/projects/p/manuscript/revision/command', {
      method: 'POST', body: JSON.stringify({ action: 'generate' })
    }, { revision: { baseline: { chapter_id: chapter } } })).toEqual({ scope: 'generation', stable_id: chapter });
    expect(normalizeManuscriptMutationEvent({ scope: 'all', stable_id: chapter })).toBeNull();
    expect(normalizeManuscriptMutationEvent({ scope: 'prose_publish' })).toBeNull();
  });

  it('keeps read-only POST boundaries out of the mutation stream', () => {
    const options = { method: 'POST', body: JSON.stringify({ chapter_id: chapter, stable_id: chapter }) };
    expect(classifyManuscriptMutation('/api/projects/p/manuscript/revision/preview', options)).toBeNull();
    expect(classifyManuscriptMutation('/api/projects/p/manuscript/workspace/restore/preview', options)).toBeNull();
    expect(classifyManuscriptMutation('/api/projects/p/manuscript/context/discuss', options)).toBeNull();
    expect(classifyManuscriptMutation('/api/projects/p/manuscript/workspace/restore', options))
      .toEqual({ scope: 'generation', stable_id: chapter });
  });

  it('uses the same strict schema as the browser SSE contract server', () => {
    const envelope = JSON.parse(manuscriptMutationSSEData({ scope: 'prose_publish', stable_id: chapter }));
    expect(envelope).toMatchObject({ type: 'action', project_id: 'browser-project', manuscript_mutation: { scope: 'prose_publish', stable_id: chapter } });
	expect(normalizeManuscriptMutationEvent(envelope)).toEqual({ scope: 'prose_publish', stable_id: chapter });
	expect(normalizeManuscriptMutationEvent({ scope: 'prose_publish', stable_id: chapter })).toBeNull();
    expect(() => manuscriptMutationSSEData({ scope: 'all', stable_id: chapter })).toThrow();
  });
});
