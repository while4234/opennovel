import { lazy, Suspense, useState } from 'react';
import { foundationOptions, newFoundationRelationship, normalizeRelationship } from './foundationModel.js';
import { FoundationGraphErrorBoundary } from './FoundationGraphErrorBoundary.jsx';
import { relationshipDirectionLabels, relationshipStatusLabels, relationshipTypeLabels } from '../coreCast.js';

const RelationshipGraph = lazy(() => import('./RelationshipGraph.jsx').then((module) => ({ default: module.RelationshipGraph })));

export function RelationshipEditor({ projectId, auditSignature, coreCast, value, characters, reviewed, disabled, sourceOnly = false, errors = {}, onChange, onReviewedChange }) {
	const [view, setView] = useState(() => sourceOnly || globalThis.matchMedia?.('(max-width: 720px)')?.matches ? 'list' : 'graph');
	const [selectedRelationshipID, setSelectedRelationshipID] = useState('');
	const update = (index, field, raw) => onChange(value.map((relationship, itemIndex) => itemIndex === index ? normalizeRelationship({ ...relationship, [field]: ['tags', 'constraints'].includes(field) ? splitList(raw) : raw }) : relationship));
	return <section aria-labelledby="foundation-relationship-heading">
		<div className="foundation-section-head"><div><h2 id="foundation-relationship-heading">{sourceOnly ? '原著人物关系（只读）' : '计划关系'}</h2><p>{sourceOnly ? '这些关系来自原著逐章事实分析；共创前可查看，但不会写入目标改编设定。' : '这里是写作前的关系计划，不是正文 runtime relationship；本页不会调用 relationship_state API。'}</p></div>
			<div className="foundation-view-switch" role="group" aria-label={sourceOnly ? '原著人物关系视图' : '计划关系视图'}><button aria-pressed={view === 'list'} className="tool-button" type="button" onClick={() => setView('list')}>关系列表</button><button aria-pressed={view === 'graph'} className="tool-button" type="button" onClick={() => setView('graph')}>关系图谱</button>{!sourceOnly ? <button className="tool-button" disabled={disabled || characters.length < 2} type="button" onClick={() => onChange([...value, newFoundationRelationship()])}>添加计划关系</button> : null}</div>
		</div>
		{!sourceOnly ? <label className="foundation-reviewed"><input checked={reviewed} disabled={disabled} type="checkbox" onChange={(event) => onReviewedChange(event.target.checked)} /> 已显式审查关系；当前可为“暂无核心关系”</label> : null}
		{view === 'graph' ? <FoundationGraphErrorBoundary resetKey={`${projectId}:${auditSignature}`} fallback={<div className="foundation-graph-fallback" role="alert"><strong>关系图谱暂时不可用。</strong><p>草稿仍在统一 Foundation reducer 中，可继续使用关系列表、preview 和 apply。</p><button className="tool-button" type="button" onClick={() => setView('list')}>使用关系列表</button></div>}><Suspense fallback={<div className="foundation-loading" role="status">正在加载计划关系图谱…</div>}><RelationshipGraph projectId={projectId} auditSignature={auditSignature} value={value} characters={characters} coreCast={coreCast} disabled={disabled} selectedRelationshipID={selectedRelationshipID} onSelectRelationship={setSelectedRelationshipID} onChange={onChange} onUseListFallback={() => setView('list')} /></Suspense></FoundationGraphErrorBoundary> : null}
		{view === 'list' && !value.length ? <div className="empty-state">{sourceOnly ? '原著分析未发现具有稳定角色端点和明确证据的关系。' : '暂无计划关系。勾选上方状态可显式确认“暂无核心关系”。'}</div> : null}
		{view === 'list' && value.length ? <div className="foundation-editor-list">{value.map((relationship, index) => <fieldset aria-current={relationship.id === selectedRelationshipID ? 'true' : undefined} className={`foundation-editor-card${relationship.id === selectedRelationshipID ? ' selected' : ''}`} key={relationship.id || index} onFocus={() => setSelectedRelationshipID(relationship.id)}>
      <legend>{relationship.label || `关系 ${index + 1}`}</legend>
      <label><span>ID（只读）</span><input readOnly value={relationship.id} /></label>
      <SelectField label="起点角色" value={relationship.source_character_id} error={errors[`relationships.${index}.source_character_id`]} disabled={disabled} onChange={(next) => update(index, 'source_character_id', next)}><option value="">选择角色</option>{characters.map((character) => <option key={character.id} value={character.id}>{character.name || character.id}</option>)}</SelectField>
      <SelectField label="终点角色" value={relationship.target_character_id} error={errors[`relationships.${index}.target_character_id`]} disabled={disabled} onChange={(next) => update(index, 'target_character_id', next)}><option value="">选择角色</option>{characters.map((character) => <option key={character.id} value={character.id}>{character.name || character.id}</option>)}</SelectField>
      <SelectField label="方向" value={relationship.direction} disabled={disabled} onChange={(next) => update(index, 'direction', next)}>{foundationOptions.relationshipDirections.map((option) => <option key={option} value={option}>{relationshipDirectionLabels[option] || option}</option>)}</SelectField>
      <SelectField label="类型" value={relationship.type} disabled={disabled} onChange={(next) => update(index, 'type', next)}>{foundationOptions.relationshipTypes.map((option) => <option key={option} value={option}>{relationshipTypeLabels[option] || option}</option>)}</SelectField>
      <SelectField label="状态" value={relationship.status} disabled={disabled} onChange={(next) => update(index, 'status', next)}>{foundationOptions.relationshipStatuses.map((option) => <option key={option} value={option}>{relationshipStatusLabels[option] || option}</option>)}</SelectField>
      {['label', 'description', 'since', 'tags', 'constraints'].map((field) => <label key={field}><span>{relationshipLabel(field)}</span><input disabled={disabled} value={Array.isArray(relationship[field]) ? relationship[field].join(', ') : relationship[field]} onChange={(event) => update(index, field, event.target.value)} /></label>)}
      <button className="tool-button danger-ghost" disabled={disabled} type="button" onClick={() => onChange(value.filter((_, itemIndex) => itemIndex !== index))}>删除关系</button>
		</fieldset>)}</div> : null}
	</section>;
}

function SelectField({ label, value, error, disabled, onChange, children }) { return <label><span>{label}</span><select aria-invalid={Boolean(error)} disabled={disabled} value={value} onChange={(event) => onChange(event.target.value)}>{children}</select>{error ? <small className="field-error">{error}</small> : null}</label>; }
function splitList(value) { return String(value || '').split(/[,，]/).map((item) => item.trim()).filter(Boolean); }
function relationshipLabel(field) { return ({ label: '标签', description: '说明', since: '起始阶段', tags: '标签集（逗号分隔）', constraints: '约束（逗号分隔）' })[field]; }
