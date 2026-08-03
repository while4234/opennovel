import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const appSource = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');

describe('rollback UI state', () => {
  it('restores a durable co-create draft returned after rollback', () => {
    const confirmRollback = appSource.slice(
      appSource.indexOf('const confirmRollback = async () =>'),
      appSource.indexOf('const submitSteer = async', appSource.indexOf('const confirmRollback = async () =>'))
    );

    expect(confirmRollback).toContain('data.cocreate');
    expect(confirmRollback).toContain('coCreateStateFromResponse(data, previous)');
    expect(confirmRollback).toContain("data.cocreate || getCoCreatePlanningReview(nextSnapshot).active");
  });
});
