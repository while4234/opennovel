import { readFileSync } from 'node:fs';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  normalizeDailyResumeTime,
  normalizeDailyResumeTimes,
  normalizeResumeScheduleResponse,
  projectScheduledResumeEnabled
} from './App.jsx';
import {
  getProjectResumeSchedule,
  getResumeSchedule,
  setProjectResumeSchedule,
  setResumeSchedule
} from './api.js';

const appSource = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');
const stylesSource = readFileSync(new URL('./styles.css', import.meta.url), 'utf8');

function jsonResponse(data) {
  return {
    ok: true,
    text: async () => JSON.stringify(data)
  };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('scheduled resume helpers', () => {
  it('accepts valid wall-clock times and rejects invalid values', () => {
    expect(normalizeDailyResumeTime(' 05:07 ')).toBe('05:07');
    expect(normalizeDailyResumeTime('23:59')).toBe('23:59');
    expect(normalizeDailyResumeTime('24:00')).toBe('');
    expect(normalizeDailyResumeTime('5:07')).toBe('');
  });

  it('normalizes, deduplicates, and sorts the daily schedule', () => {
    expect(normalizeDailyResumeTimes(['16:00', '15:00', '16:00', 'bad'])).toEqual(['15:00', '16:00']);
  });

  it('normalizes nested and top-level schedule responses', () => {
    expect(normalizeResumeScheduleResponse({
      schedule: { daily_times: ['16:00', '15:00'], timezone: 'Asia/Shanghai' },
      next_trigger_at: '2026-07-11T15:00:00+08:00',
      last_batch: { started: 2, skipped: 3, failed: 1 }
    })).toMatchObject({
      dailyTimes: ['15:00', '16:00'],
      timezone: 'Asia/Shanghai',
      nextTriggerAt: '2026-07-11T15:00:00+08:00',
      lastBatch: { started: 2, skipped: 3, failed: 1 }
    });
    expect(projectScheduledResumeEnabled({})).toBe(true);
    expect(projectScheduledResumeEnabled({ scheduled_resume_enabled: false })).toBe(false);
  });
});

describe('scheduled resume API', () => {
  it('reads and replaces the global schedule', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse({ daily_times: [] }));
    await getResumeSchedule();
    await setResumeSchedule(['15:00', '16:00'], 'Asia/Shanghai');
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/resume-schedule', expect.objectContaining({ headers: {} }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/resume-schedule', expect.objectContaining({
      method: 'PUT',
      body: JSON.stringify({ daily_times: ['15:00', '16:00'], timezone: 'Asia/Shanghai' })
    }));
  });

  it('reads and updates a project switch with an encoded id', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse({ scheduled_resume_enabled: true }));
    await getProjectResumeSchedule('project one');
    await setProjectResumeSchedule('project one', false);
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/projects/project%20one/resume-schedule', expect.objectContaining({ headers: {} }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project%20one/resume-schedule', expect.objectContaining({
      method: 'PUT',
      body: JSON.stringify({ enabled: false })
    }));
  });
});

describe('scheduled resume UI', () => {
  it('provides an independent schedule tab and safe-gate guidance', () => {
    expect(appSource).toContain("sideView === 'schedule'");
    expect(appSource).toMatch(/<Clock3 size=\{16\} \/>\s*定时/);
    expect(appSource).toContain('不会越过建议选择、分卷骨架、详细提案或续写提案等人工审核节点。');
    expect(appSource).toContain('允许此项目定时恢复');
  });

  it('keeps time editing responsive on narrow screens', () => {
    expect(stylesSource).toContain('.schedule-time-entry');
    expect(stylesSource).toContain('.schedule-batch-counts');
    expect(stylesSource).toMatch(/@media \(max-width: 620px\)[\s\S]*\.schedule-time-entry/);
  });
});
