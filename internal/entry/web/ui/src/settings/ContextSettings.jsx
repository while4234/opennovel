import { Database, RefreshCw } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { getSnapshot } from '../api.js';

function firstValue(source, ...keys) {
  for (const key of keys) {
    if (source?.[key] !== undefined && source?.[key] !== null) return source[key];
  }
  return undefined;
}

export function contextSnapshotView(snapshot = {}) {
  const agents = firstValue(snapshot, 'Agents', 'agents') || [];
  const tokens = Number(firstValue(snapshot, 'ContextTokens', 'context_tokens') || 0);
  const window = Number(firstValue(snapshot, 'ContextWindow', 'context_window') || 0);
  const percent = Number(firstValue(snapshot, 'ContextPercent', 'context_percent') || (window > 0 ? tokens / window * 100 : 0));
  return {
    modelName: String(firstValue(snapshot, 'ModelName', 'model_name') || ''),
    modelContextWindow: Number(firstValue(snapshot, 'ModelContextWindow', 'model_context_window') || 0),
    coordinator: {
      tokens,
      window,
      percent,
      scope: String(firstValue(snapshot, 'ContextScope', 'context_scope') || ''),
      strategy: String(firstValue(snapshot, 'ContextStrategy', 'context_strategy') || ''),
      active: Number(firstValue(snapshot, 'ContextActiveMessages', 'context_active_messages') || 0),
      summary: Number(firstValue(snapshot, 'ContextSummaryCount', 'context_summary_count') || 0),
      compacted: Number(firstValue(snapshot, 'ContextCompactedCount', 'context_compacted_count') || 0),
      kept: Number(firstValue(snapshot, 'ContextKeptCount', 'context_kept_count') || 0)
    },
    agents: Array.isArray(agents) ? agents.map((agent) => {
      const context = firstValue(agent, 'Context', 'context') || {};
      return {
        name: String(firstValue(agent, 'Name', 'name') || 'agent'),
        state: String(firstValue(agent, 'State', 'state') || 'idle'),
        tokens: Number(firstValue(context, 'Tokens', 'tokens') || 0),
        window: Number(firstValue(context, 'ContextWindow', 'context_window') || 0),
        percent: Number(firstValue(context, 'Percent', 'percent') || 0),
        scope: String(firstValue(context, 'Scope', 'scope') || ''),
        strategy: String(firstValue(context, 'Strategy', 'strategy') || ''),
        active: Number(firstValue(context, 'ActiveMessages', 'active_messages') || 0),
        summary: Number(firstValue(context, 'SummaryMessages', 'summary_messages') || 0),
        compacted: Number(firstValue(context, 'CompactedCount', 'compacted_count') || 0),
        kept: Number(firstValue(context, 'KeptCount', 'kept_count') || 0)
      };
    }) : []
  };
}

export function ContextSettings({ activeProject, projects = [], loadSnapshot = getSnapshot }) {
  const [projectId, setProjectId] = useState(activeProject?.id || projects[0]?.id || '');
  const [state, setState] = useState({ status: 'idle', snapshot: null, error: '' });

  useEffect(() => {
    if (!projectId && projects[0]?.id) setProjectId(projects[0].id);
  }, [projectId, projects]);

  const refresh = async () => {
    if (!projectId) return;
    setState((current) => ({ ...current, status: 'loading', error: '' }));
    try {
      const response = await loadSnapshot(projectId);
      setState({ status: 'done', snapshot: response?.snapshot || null, error: '' });
    } catch (error) {
      setState({ status: 'error', snapshot: null, error: error.message });
    }
  };

  useEffect(() => {
    void refresh();
  }, [projectId]);

  const view = useMemo(() => contextSnapshotView(state.snapshot || {}), [state.snapshot]);
  if (projects.length === 0) {
    return <div className="settings-empty"><Database size={28} /><strong>还没有可查看的项目</strong><span>创建并打开项目后，这里会显示模型上下文窗口与压缩状态。</span></div>;
  }

  return (
    <div className="context-settings">
      <div className="settings-inline-toolbar">
        <label><span>项目</span><select aria-label="上下文项目" value={projectId} onChange={(event) => setProjectId(event.target.value)}>{projects.map((project) => <option key={project.id} value={project.id}>{project.name || project.id}</option>)}</select></label>
        <button className="tool-button" disabled={state.status === 'loading'} onClick={refresh} type="button"><RefreshCw size={16} />刷新</button>
      </div>
      {state.error ? <div className="settings-message error" role="alert">{state.error}</div> : null}
      {state.status === 'loading' && !state.snapshot ? <div className="settings-empty" role="status">正在读取上下文状态…</div> : (
        <>
          <section className="settings-band">
            <header><h2>当前模型</h2><p>模型和有效上下文窗口来自项目运行时快照。</p></header>
            <div className="settings-metric-grid"><ContextMetric label="模型" value={view.modelName || '未启动'} /><ContextMetric label="模型窗口" value={formatTokens(view.modelContextWindow)} /></div>
          </section>
          <section className="settings-band">
            <header><h2>协调器上下文</h2><p>只读视图，不会修改模型或压缩策略。</p></header>
            <ContextUsage context={view.coordinator} />
          </section>
          <section className="settings-band">
            <header><h2>Agent 上下文</h2><p>各 Agent 最近一次投影到工作台的上下文状态。</p></header>
            {view.agents.length === 0 ? <div className="settings-empty compact">暂无 Agent 上下文数据</div> : <div className="context-agent-list">{view.agents.map((agent) => <article key={agent.name}><header><strong>{agent.name}</strong><span>{agent.state}</span></header><ContextUsage context={agent} /></article>)}</div>}
          </section>
        </>
      )}
    </div>
  );
}

function ContextUsage({ context }) {
  return <><div className="context-progress"><span style={{ width: `${Math.max(0, Math.min(100, context.percent || 0))}%` }} /></div><div className="settings-metric-grid"><ContextMetric label="Tokens / Window" value={`${formatTokens(context.tokens)} / ${formatTokens(context.window)}`} /><ContextMetric label="使用率" value={`${Number(context.percent || 0).toFixed(1)}%`} /><ContextMetric label="Scope" value={context.scope || '-'} /><ContextMetric label="Strategy" value={context.strategy || '-'} /><ContextMetric label="Active" value={context.active} /><ContextMetric label="Summary" value={context.summary} /><ContextMetric label="Compacted" value={context.compacted} /><ContextMetric label="Kept" value={context.kept} /></div></>;
}

function ContextMetric({ label, value }) {
  return <div className="settings-metric"><span>{label}</span><strong title={String(value)}>{String(value)}</strong></div>;
}

function formatTokens(value) {
  return new Intl.NumberFormat('zh-CN').format(Number(value || 0));
}
