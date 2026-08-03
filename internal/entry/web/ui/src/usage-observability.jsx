import React from 'react';

function percent(value) {
  return Number.isFinite(Number(value)) ? `${Math.round(Number(value) * 100)}%` : '不完整';
}

function milliseconds(value) {
  const number = Number(value || 0);
  return number >= 1000 ? `${(number / 1000).toFixed(1)} 秒` : `${Math.round(number)} 毫秒`;
}

function cacheHit(group) {
  if (group?.cache_capable === false) return '不支持 / N/A';
  if (group?.cache_capable == null) return '能力未知';
  const input = Number(group.input_tokens || 0);
  return input > 0 ? percent(Number(group.cache_read_tokens || 0) / input) : '暂无数据';
}

function quality(value) {
  return value == null ? '不可比' : percent(value);
}

export function UsageObservabilityTable({ report }) {
  const groups = Array.isArray(report?.groups) ? report.groups : [];
  if (!groups.length) return null;
  return (
    <section className="usage-detail-section" aria-labelledby="usage-detail-title">
      <div className="section-title" id="usage-detail-title">逐模型调用质量</div>
      {report?.legacy_aggregate ? (
        <div className="warning-note" role="status">旧版累计数据时间和覆盖率未知；完整逐调用统计从本版本启用后开始。</div>
      ) : null}
      <div className="usage-detail-scroll" tabIndex="0">
        <table className="usage-detail-table">
          <thead>
            <tr>
              <th scope="col">服务商 / 模型</th>
              <th scope="col">调用</th>
              <th scope="col">数据覆盖</th>
              <th scope="col">缓存命中</th>
              <th scope="col">平均延迟</th>
              <th scope="col">失败率</th>
              <th scope="col">重试率</th>
              <th scope="col">首轮审稿通过</th>
              <th scope="col">一致性通过</th>
              <th scope="col">平均审稿分</th>
              <th scope="col">每千验收字成本</th>
            </tr>
          </thead>
          <tbody>
            {groups.map((group) => (
              <tr key={group.key}>
                <th scope="row">{group.key || '未知模型'}</th>
                <td>{group.calls || 0}</td>
                <td>{group.usage_incomplete ? `不完整 · ${percent(group.coverage)}` : percent(group.coverage)}</td>
                <td>{cacheHit(group)}</td>
                <td>{milliseconds(group.avg_latency_ms)}</td>
                <td>{percent(group.failure_rate)}</td>
                <td>{percent(group.retry_rate)}</td>
                <td>{quality(group.first_review_pass_rate)}</td>
                <td>{quality(group.consistency_pass_rate)}</td>
                <td>{group.average_review_score == null ? '不可比' : Number(group.average_review_score).toFixed(1)}</td>
                <td>{group.cost_per_accepted_1000_runes == null ? '不可比' : `$${Number(group.cost_per_accepted_1000_runes).toFixed(4)}`}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
