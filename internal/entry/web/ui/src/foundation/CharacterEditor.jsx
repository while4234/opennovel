import {
  AlertTriangle, Bot, Check, ChevronDown, ChevronRight, CircleAlert, Copy, FileSearch,
  Link, Plus, RefreshCw, Search, ShieldCheck, Sparkles, Trash2, UserRound, X
} from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import {
  acceptAllCharacterCandidates, characterFieldDiff, characterFieldGroups, duplicateFoundationCharacter,
  filterAndSortCharacters, foundationOptions, isCoreCharacter, mergeCharacterCandidate, mergeCharacterField,
  newFoundationCharacter, normalizeCharacter, reviewStatusForCharacter, sourceMappingByTargetID
} from './foundationModel.js';

const tierLabels = { core: '核心', important: '重要', secondary: '次要', decorative: '装饰' };
const genderLabels = { male: '男', female: '女', nonbinary: '非二元', unspecified: '原著未明确（正文使用姓名/称谓）' };
const reviewLabels = { passed: '通过', needs_revision: '需修订', stale: '已过期', not_reviewed: '未审核' };
const sourceLabels = { keep: '保留', rename: '改名', merge: '合并', split: '拆分', exclude: '排除', target_original: '目标原创', unmapped: '未映射' };
const fieldLabels = {
  name: '姓名', aliases: '别名', role: '故事职责', gender: '性别 / 代词', tier: '层级', faction: '阵营',
  description: '人物描述', traits: '特质 / 核心标签', contrast_details: '反差细节', key_backstory: '人物小传',
  goal: '目标', motivation: '动机', conflict: '冲突', arc: '角色弧',
  voice: '语言风格', constraints: '行为 / 设定约束', knowledge_boundary: '知识边界',
  initial_state: '故事初始状态', notes: '备注', relationships: '计划关系'
};

export function CharacterEditor({
  value, coreCast, mode = 'normal', sourceFoundation, relationships = [], disabled, dirty = false,
  errors = {}, workspace, workspaceLoading = false, agentBusy = false, agentOpenRequestId = 0, onChange, onOpenRelationships,
  onAnalyze, onReview, onRetry, onDiscard, onConfirm
}) {
  const [selectedID, setSelectedID] = useState('');
  const [query, setQuery] = useState('');
  const [filtersExpanded, setFiltersExpanded] = useState(false);
  const [filters, setFilters] = useState({ tier: 'all', core: 'all', completeness: 'all', review: 'all', source: 'all', sort: 'core' });
  const [modifiedByID, setModifiedByID] = useState({});
  const [expanded, setExpanded] = useState(() => new Set(['identity', 'core']));
  const [instruction, setInstruction] = useState('');
  const [allowSupporting, setAllowSupporting] = useState(false);
  const [agentExpanded, setAgentExpanded] = useState(false);
  const [pendingDialog, setPendingDialog] = useState(null);
  const fieldRefs = useRef({});
  const addRef = useRef(null);

  const coreIDs = useMemo(() => new Set((coreCast?.members || []).map((member) => String(member?.character?.id || ''))), [coreCast]);
  const reviewCompleted = workspace?.confirmationStatus === 'confirmed' ||
    (workspace?.run?.mode === 'review' && workspace?.run?.status === 'completed');
  const mappingsByTargetID = useMemo(() => sourceMappingByTargetID(workspace?.sourceMappings), [workspace?.sourceMappings]);
  const candidateCharacters = workspace?.candidate?.foundation?.characters || [];
  const candidateByID = useMemo(() => new Map(candidateCharacters.map((character) => [character.id, character])), [candidateCharacters]);
  const sourceCharacters = useMemo(() => (Array.isArray(sourceFoundation?.characters) ? sourceFoundation.characters : [])
    .map((character, index) => normalizeCharacter({
      ...character,
      id: character?.id || `source-character-${index + 1}`,
      tier: character?.tier || 'important'
    }))
    .filter((character) => character.name || character.id), [sourceFoundation]);
  const showingSourceOnly = mode === 'adaptation' && !value.length && !candidateCharacters.length && sourceCharacters.length > 0;
  useEffect(() => {
    if (Number(agentOpenRequestId) > 0 && !showingSourceOnly) {
      setAgentExpanded(true);
    }
  }, [agentOpenRequestId, showingSourceOnly]);
  const displayCharacters = useMemo(() => {
    if (showingSourceOnly) return sourceCharacters;
    const currentIDs = new Set(value.map((character) => character.id));
    return [...value, ...candidateCharacters.filter((character) => !currentIDs.has(character.id))];
  }, [value, candidateCharacters, showingSourceOnly, sourceCharacters]);
  const visible = useMemo(() => filterAndSortCharacters(displayCharacters, {
    ...filters, query, coreIDs, completenessByID: workspace?.completenessByID,
    findings: workspace?.findings, reviewStale: workspace?.reviewStale, reviewCompleted, mappingByTargetID: mappingsByTargetID, modifiedByID
  }), [displayCharacters, filters, query, coreIDs, workspace?.completenessByID, workspace?.findings, workspace?.reviewStale, reviewCompleted, mappingsByTargetID, modifiedByID]);
  const selected = displayCharacters.find((character) => character.id === selectedID) || null;
  const selectedIsCandidateOnly = Boolean(selected && !value.some((character) => character.id === selected.id));
  const selectedIsSourceOnly = Boolean(selected && showingSourceOnly);

  useEffect(() => {
    if (selectedID && displayCharacters.some((character) => character.id === selectedID)) return;
    setSelectedID(visible[0]?.id || displayCharacters[0]?.id || '');
  }, [selectedID, displayCharacters, visible]);

  useEffect(() => {
    if (!workspace) return;
    if (Number(agentOpenRequestId) > 0 && !showingSourceOnly) {
      setAgentExpanded(true);
      return;
    }
    const runStatus = workspace.run?.status;
    if (['queued', 'running', 'failed', 'interrupted'].includes(runStatus) || !workspace.candidate) {
      setAgentExpanded(true);
    } else if (runStatus === 'completed') {
      setAgentExpanded(false);
    }
  }, [agentOpenRequestId, showingSourceOnly, workspace?.candidate?.digest, workspace?.run?.status]);

  const updateSelected = (updater) => {
    if (!selected) return;
    const next = value.map((character) => character.id === selected.id
      ? normalizeCharacter(typeof updater === 'function' ? updater(character) : { ...character, ...updater })
      : character);
    setModifiedByID((current) => ({ ...current, [selected.id]: Date.now() }));
    onChange(next);
  };
  const add = () => {
    const character = newFoundationCharacter();
    onChange([...value, character]);
    setSelectedID(character.id);
  };
  const duplicate = () => {
    if (!selected) return;
    const character = duplicateFoundationCharacter(selected);
    onChange([...value, character]);
    setSelectedID(character.id);
  };
  const requestDelete = (character, trigger) => {
    const core = isCoreCharacter(character, coreCast) || character.tier === 'core';
    if (core) {
      setPendingDialog({
        title: '删除核心角色？',
        description: `“${character.name}”属于核心角色。删除会触发 CoreCast 重新确认和高风险审核。`,
        trigger,
        confirmLabel: '确认删除',
        danger: true,
        onConfirm: () => deleteCharacter(character.id)
      });
    } else {
      deleteCharacter(character.id);
    }
  };
  const deleteCharacter = (id) => {
    const index = value.findIndex((character) => character.id === id);
    const remaining = value.filter((character) => character.id !== id);
    onChange(remaining);
    setSelectedID(remaining[Math.min(Math.max(index, 0), remaining.length - 1)]?.id || '');
  };
  const selectedCandidate = selected ? candidateByID.get(selected.id) : null;
  const selectedDiff = selectedCandidate && selected && !selectedIsCandidateOnly ? characterFieldDiff(selected, selectedCandidate) : [];
  const acceptCharacter = (candidate, trigger) => {
    const highRisk = candidate.tier === 'core' || coreIDs.has(candidate.id) || workspace?.diff?.changes?.some((change) => change.entity_id === candidate.id && change.high_risk);
    const apply = () => {
      onChange(mergeCharacterCandidate(value, candidate));
      setSelectedID(candidate.id);
    };
    if (highRisk) setPendingDialog({
      title: '接受核心角色候选？',
      description: '此变更可能改变核心身份、关系和全书约束；接受后仍需 Foundation preview 与重新确认。',
      confirmLabel: '接受候选', trigger, onConfirm: apply
    });
    else apply();
  };
  const acceptField = (field, trigger) => {
    const apply = () => onChange(mergeCharacterField(value, selectedCandidate, field));
    const highRisk = coreIDs.has(selected?.id) || selected?.tier === 'core' ||
      workspace?.diff?.changes?.some((change) => change.entity_id === selected?.id && (change.high_risk || change.core_cast_affected));
    if (highRisk) setPendingDialog({
      title: '接受核心角色字段？',
      description: `“${fieldLabels[field] || field}”会改变核心角色内容；接受后必须通过 Foundation preview 和重新确认。`,
      confirmLabel: '接受此字段', trigger, onConfirm: apply
    });
    else apply();
  };
  const acceptAllSafe = () => {
    const unsafeIDs = new Set((workspace?.diff?.changes || [])
      .filter((change) => change.high_risk || change.core_cast_affected || change.kind !== 'modified')
      .map((change) => change.entity_id));
    for (const id of coreIDs) unsafeIDs.add(id);
    const safeFoundation = {
      ...workspace?.candidate?.foundation,
      characters: candidateCharacters.filter((character) => !unsafeIDs.has(character.id))
    };
    onChange(acceptAllCharacterCandidates(value, safeFoundation));
  };
  const focusFinding = (finding) => {
    if (finding.character_id) setSelectedID(finding.character_id);
    const field = normalizeFindingField(finding.location);
    if (field === 'relationships') {
      onOpenRelationships?.();
      return;
    }
    const group = characterFieldGroups.find((item) => item.fields.includes(field))?.id || 'identity';
    setExpanded((current) => new Set([...current, group]));
    globalThis.requestAnimationFrame?.(() => fieldRefs.current[`${finding.character_id}:${field}`]?.focus());
  };

  const empty = !displayCharacters.length;
  const noMatch = !empty && !visible.length;
  return <section aria-labelledby="foundation-character-heading" className="character-workspace">
    <div className="character-workspace-top">
      <div className="foundation-section-head character-workspace-head">
        <div><h2 id="foundation-character-heading">角色卡工作台</h2><p>{showingSourceOnly ? '当前完整展示原著 SourceFoundation 角色设定（只读）；共创仅用于生成可编辑的目标改编角色卡。' : '核心与非核心角色共享同一份 Foundation 草稿；Agent 候选不会自动覆盖编辑。'}</p></div>
        <div className="inline-actions">
          <button ref={addRef} className="tool-button" disabled={disabled || showingSourceOnly} type="button" onClick={add}><Plus size={16} />新增角色</button>
          <button className="tool-button" disabled={disabled || !selected || showingSourceOnly} type="button" onClick={duplicate}><Copy size={16} />复制为新角色</button>
        </div>
      </div>
      {showingSourceOnly ? <div className="warning-note source-only-character-note" role="status"><FileSearch size={16} />原著角色设定已按正文证据展示；只读表示不可在此改写，并不表示必须共创后才能完整查看。</div> : null}
      {workspace?.reviewStale ? <div className="warning-note" role="status"><AlertTriangle size={16} />草稿已修改，旧角色审核立即标记为 stale；请在当前草稿上重新审核。</div> : null}
      {workspaceLoading ? <div className="character-workspace-skeleton" aria-live="polite" role="status">正在加载角色完整度与审核状态…</div> : null}
      <CharacterAgentPanel
        selected={selected} workspace={workspace} disabled={agentBusy || showingSourceOnly} dirty={dirty}
        instruction={instruction} allowSupporting={allowSupporting} expanded={agentExpanded && !showingSourceOnly}
        onExpandedChange={setAgentExpanded}
        onInstructionChange={setInstruction} onAllowSupportingChange={setAllowSupporting}
        onAnalyze={onAnalyze} onReview={onReview} onRetry={onRetry} onDiscard={onDiscard} onConfirm={onConfirm}
      />
      {workspace?.error ? <div className="error-banner" role="alert"><strong>{workspace.error.code}</strong><span>{workspace.error.message}</span></div> : null}
      {mode === 'adaptation' && !showingSourceOnly ? <SourceCoverage coverage={workspace?.coverage} onFilterPending={() => setFilters({ ...filters, source: 'unmapped' })} /> : null}
    </div>
    <div className="character-layout">
      <aside aria-label="角色列表" className="character-list-pane">
        <div className="character-list-toolbar">
          <div className="character-list-summary">
            <strong>角色目录</strong>
            <div><span>显示 {visible.length} / 共 {displayCharacters.length}</span><button aria-expanded={filtersExpanded} className="character-filter-toggle" type="button" onClick={() => setFiltersExpanded((current) => !current)}>{filtersExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}筛选</button></div>
          </div>
          <label className="character-search"><Search aria-hidden="true" size={16} /><input aria-label="搜索角色" placeholder="搜索姓名、别名、职责、阵营" value={query} onChange={(event) => setQuery(event.target.value)} /></label>
          {filtersExpanded ? <div className="character-filter-grid">
            <Filter label="层级" value={filters.tier} onChange={(tier) => setFilters({ ...filters, tier })} options={[['all', '全部层级'], ...foundationOptions.characterTiers.map((item) => [item, tierLabels[item]])]} />
            <Filter label="核心" value={filters.core} onChange={(core) => setFilters({ ...filters, core })} options={[['all', '核心与非核心'], ['core', '仅核心'], ['non-core', '仅非核心']]} />
            <Filter label="完整度" value={filters.completeness} onChange={(completeness) => setFilters({ ...filters, completeness })} options={[['all', '全部完整度'], ['complete', '完整'], ['incomplete', '有缺口']]} />
            <Filter label="审核" value={filters.review} onChange={(review) => setFilters({ ...filters, review })} options={[['all', '全部审核'], ['passed', '通过'], ['needs_revision', '需修订'], ['stale', '已过期']]} />
            {mode === 'adaptation' ? <Filter label="来源" value={filters.source} onChange={(source) => setFilters({ ...filters, source })} options={[['all', '全部来源'], ...foundationOptions.sourceMappingActions.map((item) => [item, sourceLabels[item]]), ['unmapped', '未映射']]} /> : null}
            <Filter label="排序" value={filters.sort} onChange={(sort) => setFilters({ ...filters, sort })} options={[['core', '核心优先'], ['tier', '按层级'], ['name', '按姓名'], ['gaps', '缺口优先'], ['recent', '最近修改']]} />
          </div> : null}
        </div>
        {empty ? <div className="empty-state"><UserRound size={24} /><strong>还没有角色</strong><span>添加第一张角色卡，或运行角色分析。</span></div> : null}
        {noMatch ? <div className="empty-state"><Search size={24} /><strong>没有匹配角色</strong><button className="tool-button" type="button" onClick={() => { setQuery(''); setFilters({ tier: 'all', core: 'all', completeness: 'all', review: 'all', source: 'all', sort: 'core' }); }}>清除筛选</button></div> : null}
        <div className="character-card-list" role="listbox" aria-label="选择角色">
          {visible.map((character) => <CharacterListCard
            key={character.id} character={character} selected={character.id === selectedID}
            core={coreIDs.has(character.id) || character.tier === 'core'} dirty={Boolean(modifiedByID[character.id])}
            completeness={workspace?.completenessByID?.[character.id]}
            review={reviewStatusForCharacter(character.id, workspace?.findings, workspace?.reviewStale, reviewCompleted)}
            mapping={mappingsByTargetID[character.id]} mode={mode} sourceOnly={showingSourceOnly}
            onSelect={() => setSelectedID(character.id)}
          />)}
        </div>
      </aside>
      <article className="character-detail-pane">
        {!selected ? <div className="empty-state">从左侧选择角色查看详情。</div> : <>
          <header className="character-detail-header">
            <div><span className="eyebrow">{selected.id}</span><h3>{selected.name || '未命名角色'}</h3><p>{selected.role || '尚未填写故事职责'}</p></div>
            <button className="tool-button danger-ghost" disabled={disabled || selectedIsCandidateOnly || selectedIsSourceOnly} type="button" onClick={(event) => requestDelete(selected, event.currentTarget)}><Trash2 size={16} />删除</button>
          </header>
          <div className="character-status-row">
            <StatusBadge icon={<UserRound size={14} />} text={tierLabels[selected.tier] || '重要'} tone={selected.tier === 'core' ? 'risk' : ''} />
            {selectedIsSourceOnly ? <>
              <StatusBadge icon={<FileSearch size={14} />} text="原著证据已提取" />
              <StatusBadge icon={<ShieldCheck size={14} />} text="只读" />
            </> : <>
              <CompletenessBadge value={workspace?.completenessByID?.[selected.id]} />
              <StatusBadge icon={<ShieldCheck size={14} />} text={`审核：${reviewLabels[reviewStatusForCharacter(selected.id, workspace?.findings, workspace?.reviewStale, reviewCompleted)]}`} />
            </>}
            {modifiedByID[selected.id] ? <StatusBadge icon={<CircleAlert size={14} />} text="未保存修改" tone="warning" /> : null}
          </div>
          {selectedIsSourceOnly ? <div className="warning-note" role="status"><FileSearch size={16} />这是原著分析得到的只读角色，不是改编目标角色。</div> : selectedIsCandidateOnly ? <div className="warning-note" role="status"><Sparkles size={16} />Character Agent 候选（尚未发布）；确认本轮角色候选后才会写入 StoryFoundation。</div> : null}
          <CharacterForm
            character={selected} disabled={disabled || selectedIsCandidateOnly || selectedIsSourceOnly} errors={selectedIsSourceOnly ? {} : errors} index={value.findIndex((item) => item.id === selected.id)}
            expanded={expanded} setExpanded={setExpanded} fieldRefs={fieldRefs}
            onChange={updateSelected}
          />
          {!selectedIsSourceOnly ? <RelationshipSummary
            character={selected}
            relationships={selectedIsCandidateOnly ? workspace?.candidate?.foundation?.relationships || [] : relationships}
            characters={displayCharacters}
            onOpen={onOpenRelationships}
          /> : null}
          {selectedIsSourceOnly ? <SourceCharacterEvidence character={selected} /> : mode === 'adaptation' ? <SourceMappingPanel mapping={mappingsByTargetID[selected.id]} sourceFoundation={sourceFoundation} /> : null}
          {!selectedIsSourceOnly ? <CharacterCompleteness value={workspace?.completenessByID?.[selected.id]} onFocus={focusFinding} characterID={selected.id} /> : null}
          {!selectedIsCandidateOnly && !selectedIsSourceOnly ? <CandidateDiff
            current={selected} candidate={selectedCandidate} diff={selectedDiff} workspace={workspace}
            disabled={disabled} onAcceptField={acceptField}
            onAcceptCharacter={acceptCharacter} onAcceptAll={acceptAllSafe}
          /> : null}
        </>}
        {!showingSourceOnly ? <FindingPanel findings={workspace?.findings} characters={displayCharacters} completeness={workspace?.completeness} stale={workspace?.reviewStale} onFocus={focusFinding} /> : null}
      </article>
    </div>
    {pendingDialog ? <ModalDialog {...pendingDialog} onClose={() => setPendingDialog(null)} /> : null}
  </section>;
}

function CharacterAgentPanel({
  selected, workspace, disabled, dirty, instruction, allowSupporting, expanded, onExpandedChange,
  onInstructionChange, onAllowSupportingChange, onAnalyze, onReview, onRetry, onDiscard, onConfirm
}) {
  const run = workspace?.run;
  const running = ['queued', 'running'].includes(run?.status);
  const failed = ['failed', 'interrupted'].includes(run?.status);
  const canAnalyze = workspace?.allowedOperations?.includes('analyze') && !disabled && !running;
  const canReview = workspace?.allowedOperations?.includes('review') && !disabled && !running;
  const canConfirm = workspace?.allowedOperations?.includes('confirm') && !disabled && !running;
  const statusText = running
    ? `${run.mode === 'review' ? '角色审核' : '角色分析'}进行中`
    : workspace?.candidate
      ? '候选已生成，等待确认'
      : '可分析并补全角色';
  return <section
    aria-labelledby="character-agent-heading"
    className={`character-agent-panel ${expanded ? 'expanded' : 'collapsed'}`}
    id="foundation-character-agent"
  >
    {canConfirm ? <div className="character-confirmation-gate" role="status">
      <div><ShieldCheck size={20} /><span><strong>角色卡审核已通过</strong><small>确认后将发布本轮角色候选，并自动继续生成完整设定。</small></span></div>
      <button id="character-confirm-action" className="tool-button accent" type="button" onClick={() => onConfirm?.()}><Check size={16} />确认角色卡并继续</button>
    </div> : null}
    <div className="character-agent-summary">
      <button aria-expanded={expanded} className="character-agent-toggle" type="button" onClick={() => onExpandedChange?.(!expanded)}>
        <Bot size={20} />
        <span><strong id="character-agent-heading">Character Agent</strong><small>{statusText}</small></span>
        {expanded ? <ChevronDown size={17} /> : <ChevronRight size={17} />}
      </button>
      <div className="inline-actions character-agent-actions">
        <button className="tool-button accent" disabled={!canAnalyze} type="button" onClick={() => onAnalyze?.({ characterIDs: [], instruction, allowSupportingCharacters: allowSupporting })}><Sparkles size={16} />分析并补全全部角色</button>
        <button className="tool-button" disabled={!canAnalyze || !selected} type="button" onClick={() => onAnalyze?.({ characterIDs: [selected.id], instruction, allowSupportingCharacters: allowSupporting })}><UserRound size={16} />分析当前角色</button>
        <button className="tool-button" disabled={!canReview} type="button" onClick={() => onReview?.()}><FileSearch size={16} />审核全部角色</button>
      </div>
    </div>
    {expanded ? <><div className="character-agent-controls">
      <p className="character-agent-explanation">分析/补全创建候选；审核只产生状态与 finding。两者都不会静默改写当前草稿；重新分析会创建新候选，本轮拒绝项不会自动接受。</p>
      <label className="character-instruction"><span>本轮补充要求（可选）</span><input maxLength={1200} disabled={disabled || running} value={instruction} onChange={(event) => onInstructionChange(event.target.value)} placeholder="例如：强化配角与主角的利益冲突" /></label>
      <label className="check-row"><input checked={allowSupporting} disabled={disabled || running} type="checkbox" onChange={(event) => onAllowSupportingChange(event.target.checked)} /><span>允许新增非核心配角</span></label>
      <dl className="character-run-meta">
        <div><dt>基线</dt><dd>rev {workspace?.baseRevision || '—'}</dd></div>
        <div><dt>模式</dt><dd>{workspace?.mode === 'adaptation' ? '改编' : '原创'}</dd></div>
        <div><dt>草稿</dt><dd>{dirty ? '含未保存修改（会作为输入）' : '与基线一致'}</dd></div>
      </dl>
      {run?.status === 'completed' && workspace?.allowedOperations?.includes('discard') ? (
        <div className="inline-actions">
          <button className="tool-button danger-ghost" type="button" onClick={onDiscard}>
            <X size={16} />
            丢弃本轮候选
          </button>
        </div>
      ) : null}
    </div>
    {run && run.status !== 'completed' ? <div className={`character-run-status ${run.status}`} aria-live="polite" role="status">
      <div><strong>{run.mode === 'review' ? '角色审核' : '角色分析'} · {runLabel(run.status)}</strong><span>阶段：{run.stage || '—'} · attempt {run.attempt || 1}</span></div>
      <progress max="3" value={run.status === 'completed' ? 3 : run.stage === 'running' ? 2 : 1}>处理中</progress>
      <p>{running ? 'Agent 正在处理；轮询只更新运行元数据，不会覆盖当前 Foundation 草稿。' : run.status === 'completed' ? '运行完成。分析结果在候选 diff 中逐项接受；审核结果在 finding 区查看。' : run.error?.message || workspace?.error?.message}</p>
      {run.finished_at ? <small>完成时间：{run.finished_at}</small> : null}
      {workspace?.staleReason ? <small>stale 原因：{workspace.staleReason}</small> : null}
      <div className="inline-actions">
        {failed ? <button className="tool-button" type="button" onClick={onRetry}><RefreshCw size={16} />安全重试</button> : null}
        {workspace?.allowedOperations?.includes('discard') && !running ? <button className="tool-button danger-ghost" type="button" onClick={onDiscard}><X size={16} />丢弃本轮候选</button> : null}
      </div>
    </div> : null}</> : null}
  </section>;
}

function CharacterListCard({ character, selected, core, dirty, completeness, review, mapping, mode, sourceOnly, onSelect }) {
  return <button
    aria-selected={selected} className={`character-list-card ${selected ? 'selected' : ''}`}
    role="option" type="button" onClick={onSelect}
  >
    <span className="character-list-title"><strong>{character.name || '未命名角色'}</strong>{core ? <span className="foundation-badge risk">core</span> : null}</span>
    <span className="character-list-role">{character.role || '未填写职责'}</span>
    <span className="character-list-badges">
      <span>{tierLabels[character.tier] || '重要'}</span>
      {sourceOnly ? <><span>原著证据</span><span>只读</span></> : <>
        <span>{completeness?.status === 'complete' ? '完整' : `缺口 ${completeness?.missing?.length ?? '—'}`}</span>
        <span>{reviewLabels[review]}</span>
        {mode === 'adaptation' ? <span>{sourceLabels[mapping?.action || 'unmapped']}</span> : <span>原创</span>}
      </>}
      {dirty ? <span>● 未保存</span> : null}
    </span>
  </button>;
}

function CharacterForm({ character, disabled, errors, index, expanded, setExpanded, fieldRefs, onChange }) {
  const errorFor = (field) => errors[`characters.${index}.${field}`];
  const refFor = (field) => (element) => { fieldRefs.current[`${character.id}:${field}`] = element; };
  const toggle = (id) => setExpanded((current) => {
    const next = new Set(current);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });
  return <div className="character-form-groups">
    {characterFieldGroups.map((group) => <section className="character-form-group" key={group.id}>
      <button aria-expanded={expanded.has(group.id)} className="character-group-toggle" type="button" onClick={() => toggle(group.id)}>
        {expanded.has(group.id) ? <ChevronDown size={17} /> : <ChevronRight size={17} />}<span>{group.label}</span>
      </button>
      {expanded.has(group.id) ? <div className="character-group-fields">
        {group.id === 'identity' ? <>
          <ReadOnlyField label="稳定 ID" value={character.id} />
          <TextField label="姓名" field="name" value={character.name} disabled={disabled} error={errorFor('name')} inputRef={refFor('name')} onChange={(name) => onChange({ ...character, name })} />
          <TagEditor label="别名" value={character.aliases} disabled={disabled} inputRef={refFor('aliases')} onChange={(aliases) => onChange({ ...character, aliases })} />
          <TextField label="故事职责" field="role" value={character.role} disabled={disabled} error={errorFor('role')} inputRef={refFor('role')} onChange={(role) => onChange({ ...character, role })} />
          <label><span>性别 / 代词</span><select ref={refFor('gender')} disabled={disabled} value={character.gender || 'unspecified'} onChange={(event) => onChange({ ...character, gender: event.target.value })}>{foundationOptions.characterGenders.map((gender) => <option key={gender} value={gender}>{genderLabels[gender]}</option>)}</select><small>原著未明确时，正文必须使用姓名或身份称谓，不得自行切换“他/她”。</small></label>
          <label><span>层级</span><select ref={refFor('tier')} disabled={disabled} value={character.tier || 'important'} onChange={(event) => onChange({ ...character, tier: event.target.value })}>{foundationOptions.characterTiers.map((tier) => <option key={tier} value={tier}>{tierLabels[tier]}</option>)}</select><small>core 变化属于高风险，会要求重新确认。</small></label>
          <TextField label="阵营" field="faction" value={character.faction} disabled={disabled} inputRef={refFor('faction')} onChange={(faction) => onChange({ ...character, faction })} />
        </> : null}
        {group.id === 'core' ? <>
          <TextArea label="人物描述" field="description" value={character.description} disabled={disabled} inputRef={refFor('description')} onChange={(description) => onChange({ ...character, description })} />
          <TagEditor label="特质 / 核心标签" value={character.traits} disabled={disabled} inputRef={refFor('traits')} onChange={(traits) => onChange({ ...character, traits })} />
          <PairEditor label="反差细节" leftLabel="表面表现" rightLabel="深层事实" value={character.contrast_details} disabled={disabled} onChange={(contrast_details) => onChange({ ...character, contrast_details })} />
          <PairEditor label="人物小传" leftLabel="关键往事" rightLabel="当下影响" value={character.key_backstory} disabled={disabled} keys={['event', 'impact']} onChange={(key_backstory) => onChange({ ...character, key_backstory })} />
        </> : null}
        {group.id === 'drive' ? ['goal', 'motivation', 'conflict', 'arc'].map((field) => <TextArea key={field} label={fieldLabels[field]} field={field} value={character[field]} disabled={disabled} inputRef={refFor(field)} onChange={(next) => onChange({ ...character, [field]: next })} />) : null}
        {group.id === 'performance' ? <>
          <TextArea label="语言风格" field="voice" value={character.voice} disabled={disabled} inputRef={refFor('voice')} onChange={(voice) => onChange({ ...character, voice })} />
          <TagEditor label="行为 / 设定约束" value={character.constraints} disabled={disabled} inputRef={refFor('constraints')} onChange={(constraints) => onChange({ ...character, constraints })} />
          <KnowledgeEditor value={character.knowledge_boundary} disabled={disabled} onChange={(knowledge_boundary) => onChange({ ...character, knowledge_boundary })} />
        </> : null}
        {group.id === 'initial' ? <>
          <InitialStateEditor value={character.initial_state} disabled={disabled} onChange={(initial_state) => onChange({ ...character, initial_state })} />
          <TextArea label="备注" field="notes" value={character.notes} disabled={disabled} inputRef={refFor('notes')} onChange={(notes) => onChange({ ...character, notes })} />
        </> : null}
      </div> : null}
    </section>)}
  </div>;
}

function TextField({ label, field, value, disabled, error, inputRef, onChange }) {
  const errorID = error ? `character-${field}-error` : undefined;
  return <label><span>{label}</span><input aria-describedby={errorID} aria-invalid={Boolean(error)} disabled={disabled} ref={inputRef} value={value || ''} onChange={(event) => onChange(event.target.value)} />{error ? <small className="field-error" id={errorID}>{error}</small> : null}</label>;
}

function TextArea({ label, field, value, disabled, inputRef, onChange }) {
  return <label><span>{label}</span><textarea disabled={disabled} ref={inputRef} rows="3" value={value || ''} onChange={(event) => onChange(event.target.value)} /></label>;
}

function ReadOnlyField({ label, value }) {
  return <label><span>{label}</span><input readOnly value={value || ''} /><small>稳定身份不可编辑；复制角色会生成全新 ID。</small></label>;
}

function TagEditor({ label, value = [], disabled, inputRef, onChange }) {
  const [draft, setDraft] = useState('');
  const add = () => {
    const tag = draft.trim();
    if (!tag) return;
    if (!value.some((item) => item.toLocaleLowerCase() === tag.toLocaleLowerCase())) onChange([...value, tag]);
    setDraft('');
  };
  return <div className="character-field-block"><span className="character-field-label">{label}</span><div className="tag-editor">
    <div className="tag-list">{value.map((tag) => <span className="tag-chip" key={tag}>{tag}<button aria-label={`移除 ${tag}`} disabled={disabled} type="button" onClick={() => onChange(value.filter((item) => item !== tag))}><X size={13} /></button></span>)}</div>
    <input aria-label={`添加${label}`} disabled={disabled} ref={inputRef} value={draft} placeholder="输入后按 Enter" onBlur={add} onChange={(event) => setDraft(event.target.value)} onKeyDown={(event) => { if (['Enter', ','].includes(event.key)) { event.preventDefault(); add(); } if (event.key === 'Backspace' && !draft && value.length) onChange(value.slice(0, -1)); }} />
  </div></div>;
}

function PairEditor({ label, leftLabel, rightLabel, value = [], disabled, keys = ['surface', 'depth'], onChange }) {
  const update = (index, key, next) => onChange(value.map((item, itemIndex) => itemIndex === index ? { ...item, [key]: next } : item));
  return <fieldset className="pair-editor"><legend>{label}</legend>{value.map((item, index) => <div className="pair-editor-row" key={`${index}-${item[keys[0]]}`}>
    <label><span>{leftLabel}</span><input disabled={disabled} value={item[keys[0]] || ''} onChange={(event) => update(index, keys[0], event.target.value)} /></label>
    <label><span>{rightLabel}</span><input disabled={disabled} value={item[keys[1]] || ''} onChange={(event) => update(index, keys[1], event.target.value)} /></label>
    <button aria-label={`删除${label} ${index + 1}`} className="icon-button" disabled={disabled} type="button" onClick={() => onChange(value.filter((_, itemIndex) => itemIndex !== index))}><Trash2 size={15} /></button>
  </div>)}<button className="tool-button" disabled={disabled} type="button" onClick={() => onChange([...value, { [keys[0]]: '', [keys[1]]: '' }])}><Plus size={15} />添加{label}</button></fieldset>;
}

function KnowledgeEditor({ value, disabled, onChange }) {
  const knowledge = value || { known: [], unknown: [], misconceptions: [], forbidden: [] };
  return <fieldset className="nested-editor"><legend>知识边界</legend>
    <TagEditor label="已知" value={knowledge.known} disabled={disabled} onChange={(known) => onChange({ ...knowledge, known })} />
    <TagEditor label="未知" value={knowledge.unknown} disabled={disabled} onChange={(unknown) => onChange({ ...knowledge, unknown })} />
    <TagEditor label="误解" value={knowledge.misconceptions} disabled={disabled} onChange={(misconceptions) => onChange({ ...knowledge, misconceptions })} />
    <TagEditor label="禁止提前知道" value={knowledge.forbidden} disabled={disabled} onChange={(forbidden) => onChange({ ...knowledge, forbidden })} />
  </fieldset>;
}

function InitialStateEditor({ value, disabled, onChange }) {
  const initial = value || { identity: '', situation: '', emotion: '', resources: [], relationships: '' };
  return <fieldset className="nested-editor"><legend>故事初始状态</legend>
    {['identity', 'situation', 'emotion', 'relationships'].map((field) => <TextField key={field} label={{ identity: '初始身份', situation: '处境', emotion: '情绪', relationships: '主要关系' }[field]} field={`initial-${field}`} value={initial[field]} disabled={disabled} onChange={(next) => onChange({ ...initial, [field]: next })} />)}
    <TagEditor label="初始资源" value={initial.resources} disabled={disabled} onChange={(resources) => onChange({ ...initial, resources })} />
  </fieldset>;
}

function RelationshipSummary({ character, relationships, characters, onOpen }) {
  const related = relationships.filter((item) => item.source_character_id === character.id || item.target_character_id === character.id);
  const names = new Map(characters.map((item) => [item.id, item.name]));
  return <section className="character-side-section"><div className="character-side-heading"><div><Link size={17} /><h4>相关计划关系</h4></div><button className="tool-button" type="button" onClick={() => onOpen?.(related[0]?.id)}>{related.length ? '查看关系' : '添加关系'}</button></div>
    {related.length ? <ul>{related.slice(0, 5).map((item) => <li key={item.id}><strong>{names.get(item.source_character_id) || item.source_character_id}</strong><span> {item.label || item.type} → </span><strong>{names.get(item.target_character_id) || item.target_character_id}</strong></li>)}</ul> : <p>还没有与该角色关联的计划关系。</p>}
  </section>;
}

function SourceCoverage({ coverage, onFilterPending }) {
  if (!coverage) return <section className="source-coverage"><strong>改编来源覆盖</strong><span>等待 Character Agent 返回来源映射统计。</span></section>;
  return <section className="source-coverage" aria-label="改编来源覆盖"><strong>改编来源覆盖</strong><dl>
    <div><dt>需决策</dt><dd>{coverage.decision_required}</dd></div><div><dt>已映射</dt><dd>{coverage.mapped}</dd></div>
    <div><dt>明确排除</dt><dd>{coverage.explicitly_excluded}</dd></div><div><dt>未决</dt><dd>{coverage.pending}</dd></div>
  </dl>{coverage.pending ? <button className="tool-button" type="button" onClick={onFilterPending}>筛选未决</button> : null}</section>;
}

function SourceMappingPanel({ mapping, sourceFoundation }) {
  if (!mapping) return <section className="character-side-section source-mapping"><h4>来源映射与证据</h4><p>当前目标角色尚未关联来源角色；这不代表来源事实不存在。</p></section>;
  const sourceByID = new Map((sourceFoundation?.characters || []).map((item) => [item.id, item]));
  return <section className="character-side-section source-mapping"><h4>来源映射与证据（只读）</h4>
    <div className="source-mapping-summary"><span className="foundation-badge">{sourceLabels[mapping.action]}</span><p>{mapping.rationale || '未提供改编决定说明'}</p></div>
    <div className="source-character-grid">{mapping.source_character_ids.map((id) => {
      const source = sourceByID.get(id);
      return <article key={id}><strong>{source?.name || id}</strong><span>{source?.role || '来源角色'}</span><p>{source?.description || '来源摘要由后端证据提供。'}</p></article>;
    })}</div>
    {mapping.evidence.length ? <ul className="evidence-list">{mapping.evidence.map((item, index) => <li key={`${item.reference}-${index}`}><strong>{item.reference || item.kind}</strong><span>{item.summary}</span></li>)}</ul> : <p>没有可展示的短证据摘要。</p>}
    <small>SourceFoundation 不可编辑，也不会包含在客户端写入 payload 中。</small>
  </section>;
}

function SourceCharacterEvidence({ character }) {
  return <section className="character-side-section source-mapping"><h4>来源映射与证据（只读）</h4>
    <div className="source-character-grid"><article><strong>{character.name}</strong><span>{character.role || '来源角色'}</span><p>{character.description || character.arc || '来源角色事实已保存在 SourceFoundation。'}</p></article></div>
    <p>{character.arc || '尚无单独的来源角色弧摘要。'}</p>
    <small>完成改编共创后，系统才会记录保留、改名、合并、拆分或排除决定，并生成目标角色卡。</small>
  </section>;
}

function CharacterCompleteness({ value, onFocus, characterID }) {
  if (!value) return <section className="character-side-section"><h4>完整度</h4><p>等待服务端完整度结果；前端不会复制后端业务规则。</p></section>;
  return <section className="character-side-section"><h4>完整度：{value.status === 'complete' ? '完整' : `缺少 ${value.missing.length} 项`}</h4>
    {value.missing.length ? <ul>{value.missing.map((item) => <li key={item.code}><button className="finding-link" type="button" onClick={() => onFocus({ character_id: characterID, location: item.field })}><AlertTriangle size={14} />{item.description}</button></li>)}</ul> : <p><Check size={15} /> 服务端判定此层级所需字段完整。</p>}
  </section>;
}

function CandidateDiff({ current, candidate, diff, workspace, disabled, onAcceptField, onAcceptCharacter, onAcceptAll }) {
  if (!workspace?.candidate) return null;
  if (!candidate) return <section className="candidate-diff high-risk"><h4>候选删除了当前角色</h4><p>删除不会静默接受。请在角色列表中人工删除，并通过高风险确认与 Foundation preview。</p></section>;
  return <section className="candidate-diff" aria-labelledby="candidate-diff-heading"><div className="character-side-heading"><div><Sparkles size={17} /><h4 id="candidate-diff-heading">Agent 候选比较</h4></div><div className="inline-actions">
    <button className="tool-button" disabled={disabled || !diff.length} type="button" onClick={(event) => onAcceptCharacter(candidate, event.currentTarget)}>接受此角色</button>
    <button className="tool-button" disabled={disabled} type="button" onClick={onAcceptAll}>接受全部安全变更</button>
  </div></div>
    {!diff.length ? <p>此角色与当前草稿相同。</p> : <div className="candidate-field-list">{diff.map((item) => <article key={item.field}>
      <header><strong>{fieldLabels[item.field] || item.field}</strong><button className="tool-button" disabled={disabled} type="button" onClick={(event) => onAcceptField(item.field, event.currentTarget)}>接受此字段</button></header>
      <div className="field-compare"><div><span>当前草稿</span><pre>{displayDiffValue(item.before)}</pre></div><div><span>Agent 候选</span><pre>{displayDiffValue(item.after)}</pre></div></div>
    </article>)}</div>}
  </section>;
}

function FindingPanel({ findings = [], characters, completeness = [], stale, onFocus }) {
  const names = new Map(characters.map((item) => [item.id, item.name]));
  const completeCharacterIDs = new Set(
    completeness
      .filter((item) => item.status === 'complete')
      .map((item) => String(item.character_id || ''))
      .filter((id) => names.has(id))
  );
  const counts = {
    passed: characters.filter((character) => !findings.some((item) =>
      item.character_id === character.id && (item.blocking || item.severity === 'blocking')
    )).length,
    blocking: findings.filter((item) => item.blocking || item.severity === 'blocking').length,
    warning: findings.filter((item) => !item.blocking && item.severity === 'warning').length,
    information: findings.filter((item) => item.severity === 'information').length
  };
  return <section className="finding-panel" aria-labelledby="character-findings-heading">
    <div className="foundation-section-head"><div><h3 id="character-findings-heading">角色审核结果</h3><p>{stale ? '这些审核结果来自旧草稿，仅作参考；修改后需要重新审核。' : '阻塞问题必须修复；优化建议不会阻止角色候选确认，可按创作取舍处理。'}</p></div>
      <div className="finding-counts"><span>通过 {counts.passed}</span><span>阻塞 {counts.blocking}</span><span>建议 {counts.warning}</span><span>信息 {counts.information}</span><span>完整 {completeCharacterIDs.size}/{characters.length}</span></div>
    </div>
    {!findings.length ? <div className="empty-state"><ShieldCheck size={22} /><span>暂无审核问题或优化建议。</span></div> : <div className="finding-list">{[...findings].sort(severitySort).map((finding) => <button className={`finding-card ${finding.severity}`} key={finding.id} type="button" onClick={() => onFocus(finding)}>
      <span className="finding-card-title"><strong>{finding.blocking ? '阻塞问题' : finding.severity === 'warning' ? '优化建议' : '信息'}</strong><span>{names.get(finding.character_id) || (finding.scope === 'global' ? '全局' : finding.character_id)}</span></span>
      <span>{finding.description}</span>{finding.evidence_summary ? <small>证据：{finding.evidence_summary}</small> : null}{finding.suggestion ? <small>建议：{finding.suggestion}</small> : null}
    </button>)}</div>}
  </section>;
}

function ModalDialog({ title, description, confirmLabel, danger, trigger, onConfirm, onClose }) {
  const cancelRef = useRef(null);
  const dialogRef = useRef(null);
  useEffect(() => {
    cancelRef.current?.focus();
    const keydown = (event) => {
      if (event.key === 'Escape') { event.preventDefault(); close(); }
      if (event.key === 'Tab') trapFocus(event, dialogRef.current);
    };
    document.addEventListener('keydown', keydown);
    return () => document.removeEventListener('keydown', keydown);
  });
  const close = () => {
    onClose();
    globalThis.requestAnimationFrame?.(() => trigger?.focus());
  };
  const confirm = () => { onConfirm(); onClose(); };
  return <div className="foundation-dialog-backdrop" role="presentation"><div aria-describedby="character-dialog-description" aria-labelledby="character-dialog-title" aria-modal="true" className="foundation-dialog" ref={dialogRef} role="alertdialog">
    <h3 id="character-dialog-title">{title}</h3><p id="character-dialog-description">{description}</p>
    <div className="inline-actions"><button ref={cancelRef} className="tool-button" type="button" onClick={close}>取消</button><button className={`tool-button ${danger ? 'danger' : 'accent'}`} type="button" onClick={confirm}>{confirmLabel}</button></div>
  </div></div>;
}

function Filter({ label, value, options, onChange }) {
  return <label><span className="sr-only">{label}</span><select aria-label={label} value={value} onChange={(event) => onChange(event.target.value)}>{options.map(([id, text]) => <option key={id} value={id}>{text}</option>)}</select></label>;
}

function StatusBadge({ icon, text, tone = '' }) { return <span className={`character-status-badge ${tone}`}>{icon}{text}</span>; }
function CompletenessBadge({ value }) { return <StatusBadge icon={value?.status === 'complete' ? <Check size={14} /> : <CircleAlert size={14} />} text={value?.status === 'complete' ? '完整' : `完整度：${value ? `缺 ${value.missing.length} 项` : '待分析'}`} tone={value?.status === 'complete' ? '' : 'warning'} />; }
function runLabel(status) { return ({ queued: '已排队', running: '运行中', completed: '已完成', failed: '失败', interrupted: '已中断', stale: '已过期', discarded: '已丢弃' })[status] || status; }
function normalizeFindingField(location) {
  const value = String(location || '').split('.').pop();
  if (String(location || '').includes('relationship')) return 'relationships';
  return Object.prototype.hasOwnProperty.call(fieldLabels, value) ? value : 'name';
}
function displayDiffValue(value) { return typeof value === 'string' ? value || '（空）' : JSON.stringify(value, null, 2) || '（空）'; }
function severitySort(left, right) { return Number(right.blocking || right.severity === 'blocking') - Number(left.blocking || left.severity === 'blocking') || left.character_id.localeCompare(right.character_id); }
function trapFocus(event, element) {
  const focusable = [...(element?.querySelectorAll('button, input, select, textarea, [tabindex]:not([tabindex="-1"])') || [])].filter((item) => !item.disabled);
  if (!focusable.length) return;
  const first = focusable[0], last = focusable[focusable.length - 1];
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
}
