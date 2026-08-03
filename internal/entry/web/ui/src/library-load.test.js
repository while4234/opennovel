import { describe, expect, it } from 'vitest';
import {
  adaptationEventsFromNovelLoad,
  adaptationStatusFromNovelLoad
} from './App.jsx';

describe('novel library load helpers', () => {
  it('keeps legacy library loads in running state while backfill is accepted', () => {
    const events = [{ stage: 'dossier', message: 'building dossier' }];
    const response = {
      analyzed: false,
      running: true,
      accepted: true,
      adaptation: {
        analysis_status: 'running',
        analysis_events: events
      }
    };

    expect(adaptationStatusFromNovelLoad(response)).toBe('running');
    expect(adaptationEventsFromNovelLoad(response)).toBe(events);
  });

  it('treats fully analyzed library loads as done', () => {
    expect(adaptationStatusFromNovelLoad({ analyzed: true })).toBe('done');
  });
});
