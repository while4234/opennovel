import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { UsageObservabilityTable } from './usage-observability.jsx';

describe('UsageObservabilityTable', () => {
  it('distinguishes unsupported cache and incomplete usage', () => {
    const html = renderToStaticMarkup(<UsageObservabilityTable report={{
      groups: [{ key: 'local/model', calls: 10, coverage: 0.8, usage_incomplete: true, cache_capable: false, failure_rate: 0.1, retry_rate: 0.2 }]
    }} />);
    expect(html).toContain('不支持 / N/A');
    expect(html).toContain('不完整 · 80%');
    expect(html).toContain('失败率');
  });
});
