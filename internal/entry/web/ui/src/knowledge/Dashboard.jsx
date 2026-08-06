import { Activity, BarChart3, CircleDollarSign, RefreshCw, Sparkles, Zap } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { UsageObservabilityTable } from '../usage-observability.jsx';
import { createScopeRequestGate, ProjectScopeSelector, useIndependentProjectScope } from './ProjectScopeSelector.jsx';
import { knowledgeAPI } from './knowledgeApi.js';
import './knowledge.css';

export function Dashboard({ api = knowledgeAPI, projects = [], projectsLoading = false }) {
  const [projectId, setProjectId] = useIndependentProjectScope(projects, { allowGlobal: true });
  const [state, setState] = useState({ status: 'idle', report: null, recommendations: [], error: '' });
  const gateRef = useRef(createScopeRequestGate());

  const load = useCallback(async (scopeProjectId = projectId) => {
    const requestVersion = gateRef.current.begin();
    setState((current) => ({ ...current, status: 'loading', error: '' }));
    try {
      const [report, advice] = await Promise.all([
        api.usage({ projectId: scopeProjectId, groupBy: 'model' }),
        api.recommendations({ projectId: scopeProjectId })
      ]);
      if (!gateRef.current.isCurrent(requestVersion)) return;
      setState({ status: 'done', report, recommendations: Array.isArray(advice?.recommendations) ? advice.recommendations : [], error: '' });
    } catch (error) {
      if (!gateRef.current.isCurrent(requestVersion)) return;
      setState((current) => ({ ...current, status: 'error', error: error.message }));
    }
  }, [api, projectId]);

  useEffect(() => {
    void load(projectId);
    return () => gateRef.current.invalidate();
  }, [load, projectId]);

  const summary = useMemo(() => usageSummary(state.report), [state.report]);
  const loading = state.status === 'loading';

  return (
    <section className="knowledge-page dashboard-page" data-testid="dashboard-page">
      <header className="knowledge-page-header">
        <div className="knowledge-page-title"><span className="knowledge-page-icon"><BarChart3 size={20} /></span><div><span className="page-eyebrow">运行观测</span><h1>仪表盘</h1><p>查看模型调用、成本、缓存和质量趋势；建议仅供人工决策。</p></div></div>
        <div className="knowledge-page-scope"><ProjectScopeSelector allowGlobal disabled={projectsLoading} label="数据范围" onChange={setProjectId} projects={projects} value={projectId} /><button aria-label="刷新仪表盘" className="shell-icon-button bordered" disabled={loading} onClick={() => void load()} title="刷新仪表盘" type="button"><RefreshCw className={loading ? 'is-spinning' : ''} size={17} /></button></div>
      </header>

      {state.error ? <div className="shell-error dashboard-error" role="alert"><span>{state.error}</span><button className="shell-button" onClick={() => void load()} type="button">重试</button></div> : null}
      {loading && !state.report ? <div className="knowledge-empty" role="status"><RefreshCw className="is-spinning" size={26} /><strong>正在汇总运行数据…</strong></div> : null}
      {!loading && !state.error && !hasUsageData(state.report) ? <div className="knowledge-empty"><Activity size={28} /><strong>暂无可观测数据</strong><span>模型产生调用记录后，这里会展示用量与质量趋势。</span></div> : null}
      {state.report ? <>
        <div className="dashboard-metrics">
          <DashboardMetric icon={Zap} label="模型调用" value={summary.calls.toLocaleString('zh-CN')} />
          <DashboardMetric icon={Activity} label="输入 Token" value={compact(summary.inputTokens)} />
          <DashboardMetric icon={CircleDollarSign} label="估算费用" value={`$${summary.cost.toFixed(4)}`} />
          <DashboardMetric icon={Sparkles} label="缓存读取" value={compact(summary.cacheReadTokens)} />
        </div>
        <div className="dashboard-content-grid">
          <section className="dashboard-panel"><header><div><span className="page-eyebrow">Usage</span><h2>模型与缓存</h2></div></header><UsageObservabilityTable report={state.report} /><Trend report={state.report} /></section>
          <section className="dashboard-panel dashboard-recommendations"><header><div><span className="page-eyebrow">Recommendations</span><h2>优化建议</h2></div></header>{state.recommendations.length ? state.recommendations.map((item, index) => <article key={item.id || `${item.model}:${index}`}><strong>{item.model || '当前模型'}</strong><p>{item.evidence || item.message || '根据当前用量生成的建议'}</p><small>{item.action || '请人工确认后再调整模型配置'}</small></article>) : <div className="dashboard-empty-inline">数据不足或当前无需优化，系统不会自动切换模型。</div>}</section>
        </div>
      </> : null}
    </section>
  );
}

function DashboardMetric({ icon: Icon, label, value }) {
  return <article><span><Icon size={18} /></span><div><small>{label}</small><strong>{value}</strong></div></article>;
}

function Trend({ report }) {
  const trend = Array.isArray(report?.trend) ? report.trend.slice(-14) : [];
  if (!trend.length) return <div className="dashboard-empty-inline">暂无趋势数据</div>;
  const maximum = Math.max(...trend.map((item) => Number(item.input_tokens || 0)), 1);
  return <section className="dashboard-trend" aria-label="最近十四天输入趋势"><h3>最近十四天</h3>{trend.map((item) => <div key={item.date}><time>{String(item.date || '').slice(5)}</time><span><i style={{ width: `${Math.max(2, Math.round(Number(item.input_tokens || 0) / maximum * 100))}%` }} /></span><small>{compact(item.input_tokens || 0)}</small></div>)}</section>;
}

export function usageSummary(report) {
  const groups = Array.isArray(report?.groups) ? report.groups : [];
  return groups.reduce((total, group) => ({
    calls: total.calls + Number(group.calls || 0),
    inputTokens: total.inputTokens + Number(group.input_tokens || 0),
    cacheReadTokens: total.cacheReadTokens + Number(group.cache_read_tokens || 0),
    cost: total.cost + Number(group.cost_usd || group.cost || 0)
  }), { calls: 0, inputTokens: 0, cacheReadTokens: 0, cost: 0 });
}

function hasUsageData(report) {
  return Boolean((Array.isArray(report?.groups) && report.groups.length) || (Array.isArray(report?.trend) && report.trend.length));
}

function compact(value) {
  return new Intl.NumberFormat('zh-CN', { notation: 'compact', maximumFractionDigits: 1 }).format(Number(value || 0));
}
