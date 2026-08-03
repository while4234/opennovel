import { describe, expect, it } from 'vitest';
import { EXPANSION_STEPS, expansionFormLabel, expansionLocationLabel } from './expansion-state.js';

describe('expansion state', () => {
  it('keeps the fixed human-gated revision path and all recommendation forms', () => {
    expect(EXPANSION_STEPS).toHaveLength(12);
    expect(EXPANSION_STEPS.filter((step) => step === '人工确认')).toHaveLength(3);
    expect(expansionFormLabel('new_volume')).toBe('新增一卷');
    expect(expansionLocationLabel('book_end')).toBe('完本后续写');
  });
});
