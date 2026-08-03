import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Background, Controls, MarkerType, MiniMap, ReactFlow, applyNodeChanges, useReactFlow
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import {
  buildRelationshipGraph, defaultRelationshipGraphFilters, deterministicGraphPositions,
  loadFoundationGraphLayout, saveFoundationGraphLayout
} from './relationshipGraphModel.js';

export function RelationshipGraph({
  projectId, auditSignature, value, characters, coreCast, disabled, selectedRelationshipID,
  onSelectRelationship, onChange, onUseListFallback
}) {
  const [filters, setFilters] = useState(defaultRelationshipGraphFilters);
  const [focusNodeID, setFocusNodeID] = useState('');
  const [selectedNodeID, setSelectedNodeID] = useState('');
  const [showIsolated, setShowIsolated] = useState(true);
  const [showAll, setShowAll] = useState(false);
  const [positions, setPositions] = useState({});
  const [renderedNodes, setRenderedNodes] = useState([]);
  const [mobile, setMobile] = useState(() => globalThis.matchMedia?.('(max-width: 720px)')?.matches ?? false);
  const graph = useMemo(() => buildRelationshipGraph({
    characters, relationships: value, coreCast, positions, filters, focusNodeID, showIsolated, showAll
  }), [characters, value, coreCast, positions, filters, focusNodeID, showIsolated, showAll]);
  const reactFlowEdges = useMemo(() => graph.edges.map((edge) => ({
    ...edge,
    markerStart: edge.markerStart ? { type: MarkerType.ArrowClosed } : undefined,
    markerEnd: edge.markerEnd ? { type: MarkerType.ArrowClosed } : undefined,
    selected: edge.id === selectedRelationshipID,
    ariaLabel: edge.label
  })), [graph.edges, selectedRelationshipID]);
  const filterOptions = useMemo(() => ({
    tiers: uniqueOptions(characters.map((character) => character.tier)),
    factions: uniqueOptions(characters.map((character) => character.faction)),
    types: uniqueOptions(value.map((relationship) => relationship.type)),
    statuses: uniqueOptions(value.map((relationship) => relationship.status))
  }), [characters, value]);

  useEffect(() => {
    setFilters(defaultRelationshipGraphFilters);
    setFocusNodeID('');
    setSelectedNodeID('');
    setShowAll(false);
    setPositions(loadFoundationGraphLayout(globalThis.localStorage, projectId, auditSignature, characters.map((character) => character.id)));
  }, [projectId, auditSignature, characters]);

  useEffect(() => {
    setRenderedNodes(graph.nodes.map((node) => ({
      ...node,
      selected: node.id === selectedNodeID,
      data: { ...node.data, label: <div className="foundation-graph-node"><strong>{node.data.name}</strong><span>重要：{node.data.importance}</span><span>tier：{node.data.tier}</span><span>阵营：{node.data.faction}</span></div> }
    })));
  }, [graph.nodes, selectedNodeID]);

  useEffect(() => {
    const query = globalThis.matchMedia?.('(max-width: 720px)');
    if (!query) return undefined;
    const update = () => setMobile(query.matches);
    query.addEventListener?.('change', update);
    return () => query.removeEventListener?.('change', update);
  }, []);

  const updateFilter = (name, next) => setFilters((current) => ({ ...current, [name]: next }));
  const connect = useCallback((connection) => {
    if (disabled || !connection.source || !connection.target || connection.source === connection.target) return;
    onChange([...value, {
      id: relationshipDraftID(), source_character_id: connection.source, target_character_id: connection.target,
      type: 'other', label: '', direction: 'directed', status: 'planned', description: '', since: '', tags: [], constraints: []
    }]);
  }, [disabled, onChange, value]);
  const deleteEdges = useCallback((edges) => {
    if (disabled) return;
    const removed = new Set(edges.map((edge) => edge.id));
    onChange(value.filter((relationship) => !removed.has(relationship.id)));
    if (removed.has(selectedRelationshipID)) onSelectRelationship('');
  }, [disabled, onChange, onSelectRelationship, selectedRelationshipID, value]);
  const handleNodeChanges = useCallback((changes) => {
    setRenderedNodes((current) => applyNodeChanges(changes, current));
  }, []);
  const persistPosition = useCallback((_event, node) => {
    setPositions((current) => {
      const next = { ...current, [node.id]: { x: node.position.x, y: node.position.y } };
      saveFoundationGraphLayout(globalThis.localStorage, projectId, auditSignature, next);
      return next;
    });
  }, [projectId, auditSignature]);
  const resetLayout = () => {
    const next = deterministicGraphPositions(characters);
    setPositions(next);
    saveFoundationGraphLayout(globalThis.localStorage, projectId, auditSignature, next);
  };

  return <div className="foundation-graph-workspace">
    <div className="foundation-graph-filters" aria-label="关系图筛选">
      <label><span>角色搜索</span><input type="search" value={filters.search} onChange={(event) => updateFilter('search', event.target.value)} /></label>
      <FilterSelect label="重要级别 / tier" value={filters.tier} options={filterOptions.tiers} onChange={(next) => updateFilter('tier', next)} />
      <FilterSelect label="阵营" value={filters.faction} options={filterOptions.factions} onChange={(next) => updateFilter('faction', next)} />
      <FilterSelect label="关系类型" value={filters.relationshipType} options={filterOptions.types} onChange={(next) => updateFilter('relationshipType', next)} />
      <FilterSelect label="关系状态" value={filters.relationshipStatus} options={filterOptions.statuses} onChange={(next) => updateFilter('relationshipStatus', next)} />
      <label className="foundation-graph-check"><input checked={showIsolated} type="checkbox" onChange={(event) => setShowIsolated(event.target.checked)} />显示孤立角色</label>
      {graph.totalNodeCount > 80 ? <label className="foundation-graph-check"><input checked={showAll} type="checkbox" onChange={(event) => setShowAll(event.target.checked)} />显示全部 {graph.totalNodeCount} 个角色</label> : null}
    </div>
    <div className="foundation-graph-toolbar">
      <button className="tool-button" disabled={!selectedNodeID} type="button" onClick={() => setFocusNodeID(selectedNodeID)}>聚焦所选角色一跳邻居</button>
      <button className="tool-button" disabled={!focusNodeID} type="button" onClick={() => setFocusNodeID('')}>取消一跳聚焦</button>
      <button className="tool-button" type="button" onClick={resetLayout}>重置确定性布局</button>
      {mobile ? <button className="tool-button" type="button" onClick={onUseListFallback}>返回关系列表</button> : null}
    </div>
    {graph.limited ? <p className="warning-note" role="status">大图保护已启用：默认展示最高重要级别角色。可筛选或显式显示全部。</p> : null}
    {graph.warnings.length ? <div className="warning-note" role="alert">有 {graph.warnings.length} 条计划关系缺少稳定角色端点，已安全过滤；没有创建伪节点。</div> : null}
    <div className="foundation-graph-legend" aria-label="图例">
      <span><strong>→</strong> 单向</span><span><strong>↔</strong> 双向</span><span><strong>—</strong> 无向</span>
      <span><strong>虚线</strong> 计划中</span><span>边标签同时显示类型、方向和状态，不只依赖颜色。</span>
    </div>
    <div className="foundation-graph-canvas" aria-label="StoryFoundation 计划关系图谱" data-source={graph.source}>
      <ReactFlow
        nodes={renderedNodes}
        edges={reactFlowEdges}
        fitView
        minZoom={0.15}
        maxZoom={2}
        nodesConnectable={!disabled}
        edgesReconnectable={false}
        deleteKeyCode={disabled ? null : ['Backspace', 'Delete']}
        onNodesChange={handleNodeChanges}
        onNodeClick={(_event, node) => setSelectedNodeID(node.id)}
        onNodeDragStop={persistPosition}
        onEdgeClick={(_event, edge) => onSelectRelationship(edge.id)}
        onEdgesDelete={deleteEdges}
        onConnect={connect}
      >
        <Background gap={22} size={1} />
        <Controls showInteractive={false} />
        {!mobile ? <MiniMap pannable zoomable ariaLabel="计划关系图缩略图" /> : null}
        <FitVisibleGraph graphKey={`${graph.nodes.length}:${graph.edges.length}:${focusNodeID}`} />
      </ReactFlow>
    </div>
    <p className="foundation-graph-summary" aria-live="polite">当前显示 {graph.nodes.length}/{graph.totalNodeCount} 个角色、{graph.edges.length}/{graph.totalEdgeCount} 条有效计划关系。</p>
  </div>;
}

function FitVisibleGraph({ graphKey }) {
  const { fitView } = useReactFlow();
  useEffect(() => { fitView({ duration: 150, padding: 0.18 }); }, [fitView, graphKey]);
  return null;
}

function FilterSelect({ label, value, options, onChange }) {
  return <label><span>{label}</span><select value={value} onChange={(event) => onChange(event.target.value)}><option value="">全部</option>{options.map((option) => <option key={option} value={option}>{option}</option>)}</select></label>;
}

function uniqueOptions(values) {
  return [...new Set(values.map((value) => String(value || '').trim()).filter(Boolean))].sort((left, right) => left.localeCompare(right));
}

function relationshipDraftID() {
  return `rel-${globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`}`;
}
