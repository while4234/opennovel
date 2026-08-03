export function usageCoverage(missing, observedCalls) {
  const gaps = Math.max(0, Number(missing || 0));
  const observed = Math.max(0, Number(observedCalls || 0));
  const total = gaps + observed;
  return total > 0 ? observed / total : null;
}

export function usageConfidence(coverage, calls) {
  const ratio = Number(coverage);
  const count = Number(calls || 0);
  if (Number.isFinite(ratio) && ratio >= 0.95 && count >= 30) {
    return { level: 'high', label: '高置信' };
  }
  if (Number.isFinite(ratio) && ratio >= 0.8 && count >= 10) {
    return { level: 'medium', label: '中置信' };
  }
  return { level: 'low', label: '数据不足' };
}

export function cacheHitLabel({ cacheRead = 0, input = 0, cacheCapable = false } = {}) {
  const denominator = Number(input || 0);
  if (!cacheCapable && Number(cacheRead || 0) <= 0) {
    return '不支持 / N/A';
  }
  if (!denominator) {
    return '暂无数据';
  }
  return `${Math.round((Number(cacheRead || 0) / denominator) * 100)}%`;
}
