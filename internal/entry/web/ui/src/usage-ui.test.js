import { describe, expect, it } from 'vitest';

import { cacheHitLabel, usageConfidence, usageCoverage } from './usage-ui.js';

describe('usage presentation', () => {
  it('does not present unsupported cache as a zero hit rate', () => {
    expect(cacheHitLabel({ input: 100, cacheCapable: false })).toBe('不支持 / N/A');
  });

  it('grades confidence using the agreed coverage and call thresholds', () => {
    expect(usageConfidence(0.95, 30).level).toBe('high');
    expect(usageConfidence(0.8, 10).level).toBe('medium');
    expect(usageConfidence(0.79, 100).level).toBe('low');
  });

  it('calculates coverage from observed and missing calls', () => {
    expect(usageCoverage(1, 9)).toBe(0.9);
  });
});
