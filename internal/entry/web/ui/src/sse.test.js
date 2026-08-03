import { describe, expect, it } from 'vitest';

import {
  isSSEConnectionStale,
  nextSSEReconnectDelay,
  parseSSEMessage,
  shouldRefreshSSESnapshot
} from './sse.js';

describe('SSE reliability helpers', () => {
  it('parses valid events without throwing on malformed JSON', () => {
    expect(parseSSEMessage({ data: '{"seq":2}' }).event).toEqual({ seq: 2 });
    expect(parseSSEMessage({ data: '{broken' }).event).toBeNull();
  });

  it('uses bounded exponential backoff with jitter', () => {
    expect(nextSSEReconnectDelay(0, () => 0.5)).toBe(1000);
    expect(nextSSEReconnectDelay(3, () => 0.5)).toBe(8000);
    expect(nextSSEReconnectDelay(12, () => 0.5)).toBe(15000);
  });

  it('detects sequence gaps that require a snapshot refresh', () => {
    expect(shouldRefreshSSESnapshot({ seq: 11 }, 10)).toBe(false);
    expect(shouldRefreshSSESnapshot({ seq: 14 }, 10)).toBe(true);
  });

  it('detects an open connection that stopped receiving activity', () => {
    expect(isSSEConnectionStale(10_000, 54_999, 45_000)).toBe(false);
    expect(isSSEConnectionStale(10_000, 55_000, 45_000)).toBe(true);
    expect(isSSEConnectionStale(0, 55_000, 45_000)).toBe(false);
  });
});
