import { useEffect, useState } from 'react';
import {
  coreCastImportanceHelp,
  coreCastImportanceLabels,
  coreCastImportanceOptions,
  coreCastOriginLabels,
  coreCastOriginOptions,
  coreCastRelationshipDirections,
  coreCastRelationshipStatuses,
  coreCastRelationshipTypes,
  newCoreCastMember,
  newCoreCastRelationship,
  normalizeCoreCast,
  relationshipDirectionLabels,
  relationshipStatusLabels,
  relationshipTypeLabels,
  setCoreCastDisposition,
  setCoreCastMemberField,
  setCoreCastMemberSourceID,
  setCoreCastRelationshipField,
  sourceDispositionActions,
  sourceDispositionLabels
} from '../coreCast.js';
import './CoreCastEditor.css';

export function CoreCastEditor({ mode = 'normal', value, completion, confirmed, sourceMajorCharacters = [], busy = false, readOnly = false, onSave, onConfirm, onUnconfirm }) {
  const [draft, setDraft] = useState(() => normalizeCoreCast(value, mode));
  const [dirty, setDirty] = useState(false);
  const [error, setError] = useState('');
  const [activeView, setActiveView] = useState('members');
  const [activeMemberIndex, setActiveMemberIndex] = useState(0);

  useEffect(() => {
    setDraft(normalizeCoreCast(value, mode));
    setDirty(false);
    setError('');
    setActiveMemberIndex((current) => clampPageIndex(current, normalizeCoreCast(value, mode).members.length));
  }, [mode, value]);

  const updateDraft = (updater) => {
    setDraft(updater);
    setDirty(true);
  };
  const changeMember = (index, path, nextValue) => {
    updateDraft((current) => setCoreCastMemberField(current, index, path, nextValue));
  };
  const changeRelationship = (index, field, nextValue) => {
    updateDraft((current) => setCoreCastRelationshipField(current, index, field, nextValue));
  };
  const save = async () => {
    try {
      setError('');
      await onSave?.(draft);
    } catch (err) {
      setError(err?.message || '核心角色保存失败，请检查填写内容。');
    }
  };

  if (readOnly) {
    return <ReadOnlyCoreCast value={draft} completion={completion} confirmed={confirmed} mode={mode} sourceMajorCharacters={sourceMajorCharacters} />;
  }

  const activeMember = draft.members[activeMemberIndex];
  const addMember = () => {
    setActiveView('members');
    setActiveMemberIndex(draft.members.length);
    updateDraft((current) => ({ ...current, members: [...current.members, newCoreCastMember()] }));
  };
  const deleteMember = (index) => {
    setActiveMemberIndex((current) => clampPageIndex(current > index ? current - 1 : current, draft.members.length - 1));
    updateDraft((current) => ({ ...current, members: current.members.filter((_, memberIndex) => memberIndex !== index) }));
  };

  return (
    <section className="core-cast-editor core-cast-workspace" aria-labelledby="core-cast-workspace-title">
      <header className="core-cast-workspace-header">
        <div>
          <span className="eyebrow">写作前确认</span>
          <h2 id="core-cast-workspace-title">核心角色工作区</h2>
          <p>这里锁定全书必须持续维护的人物和关系。底层会保存稳定代号，但你只需要按中文说明修改。</p>
        </div>
        <span className={`core-cast-status ${confirmed ? 'confirmed' : 'pending'}`}>{confirmed ? '已确认' : '待确认'}</span>
      </header>

      <ol className="core-cast-guide" aria-label="修改核心角色的方法">
        <li><strong>先改角色</strong><span>姓名、定位、目标、动机和成长变化都是必填内容。</span></li>
        <li><strong>再查关系</strong><span>用下方关系卡连接角色；确实没有核心关系时，勾选角色卡底部的说明。</span></li>
        <li><strong>保存并确认</strong><span>先保存修改，待“需要补充”清空后再确认；确认后才会进入下一步。</span></li>
      </ol>

      {dirty && confirmed ? <div className="warning-note">你正在修改已确认内容。保存后旧确认会失效，需要重新确认。</div> : null}
      {error ? <div className="error-banner compact" role="alert">{error}</div> : null}

      <nav className="core-cast-view-tabs" aria-label="核心角色工作区内容" role="tablist">
        <button aria-selected={activeView === 'members'} className={activeView === 'members' ? 'active' : ''} role="tab" type="button" onClick={() => setActiveView('members')}><span>人物角色</span><small>{draft.members.length}</small></button>
        <button aria-selected={activeView === 'relationships'} className={activeView === 'relationships' ? 'active' : ''} role="tab" type="button" onClick={() => setActiveView('relationships')}><span>核心关系</span><small>{draft.planned_relationships.length}</small></button>
        {mode === 'adapt' ? <button aria-selected={activeView === 'sources'} className={activeView === 'sources' ? 'active' : ''} role="tab" type="button" onClick={() => setActiveView('sources')}><span>源作处理</span><small>{sourceMajorCharacters.length}</small></button> : null}
      </nav>

      {activeView === 'members' ? <section className="core-cast-paged-panel" aria-label="核心角色分页">
        <PagedNavigator
          activeIndex={activeMemberIndex}
          addDisabled={busy}
          addLabel="添加角色"
          items={draft.members.map((member, index) => ({
            id: member.character.id || `member-${index}`,
            label: member.character.name || `角色 ${index + 1}`,
            meta: coreCastImportanceLabels[member.importance],
            complete: memberIsComplete(member, mode)
          }))}
          onAdd={addMember}
          onChange={setActiveMemberIndex}
        />
        {activeMember ? <CoreCastMemberCard
          busy={busy}
          index={activeMemberIndex}
          key={activeMember.character.id || `member-${activeMemberIndex}`}
          member={activeMember}
          mode={mode}
          sourceMajorCharacters={sourceMajorCharacters}
          onChange={(path, nextValue) => changeMember(activeMemberIndex, path, nextValue)}
          onSourceChange={(sourceID, selected) => updateDraft((current) => setCoreCastMemberSourceID(current, activeMemberIndex, sourceID, selected))}
          onDelete={() => deleteMember(activeMemberIndex)}
        /> : <div className="core-cast-empty"><strong>还没有核心角色</strong><span>点击“添加角色”创建主角，再填写其故事目标。</span></div>}
      </section> : null}

      {activeView === 'relationships' ? <CoreRelationshipEditor
        busy={busy}
        members={draft.members}
        relationships={draft.planned_relationships}
        onAdd={() => updateDraft((current) => ({ ...current, planned_relationships: [...current.planned_relationships, newCoreCastRelationship()] }))}
        onChange={changeRelationship}
        onDelete={(index) => updateDraft((current) => ({ ...current, planned_relationships: current.planned_relationships.filter((_, itemIndex) => itemIndex !== index) }))}
      /> : null}

      {activeView === 'sources' && mode === 'adapt' ? <SourceDispositionEditor
        busy={busy}
        draft={draft}
        sourceMajorCharacters={sourceMajorCharacters}
        onChange={(sourceID, change) => updateDraft((current) => setCoreCastDisposition(current, sourceID, change))}
      /> : null}

      <CompletionNotice completion={completion} />
      <footer className="core-cast-actions">
        <div>
          <strong>{dirty ? '有未保存修改' : completion?.complete ? '资料完整，可以确认' : '请先补齐提示内容'}</strong>
          <span>保存只更新草稿；“确认核心角色”才会允许流程继续。</span>
        </div>
        <div className="inline-actions">
          <button className="tool-button" disabled={busy || !dirty} type="button" onClick={save}>保存修改</button>
          {confirmed ? (
            <button className="tool-button" disabled={busy || dirty} type="button" onClick={onUnconfirm}>取消确认</button>
          ) : (
            <button className="tool-button accent" disabled={busy || dirty || !completion?.complete} type="button" onClick={onConfirm}>确认核心角色</button>
          )}
        </div>
      </footer>
    </section>
  );
}

function CoreCastMemberCard({ member, index, mode, sourceMajorCharacters, busy, onChange, onSourceChange, onDelete }) {
  const title = member.character.name || `角色 ${index + 1}`;
  return (
    <fieldset className="core-cast-member-card">
      <legend><span>{title}</span><small>{coreCastImportanceLabels[member.importance]}</small></legend>
      <div className="core-cast-card-section">
        <h3>人物身份</h3>
        <div className="core-cast-form-grid">
          <Field label="角色代号" help="用于关系引用；创建后尽量不要修改。">
            <input aria-label={`角色 ${index + 1} 代号`} disabled={busy} placeholder="如：mo_ziyao" value={member.character.id} onChange={(event) => onChange('character.id', event.target.value)} />
          </Field>
          <Field label="姓名" required><input disabled={busy} value={member.character.name} onChange={(event) => onChange('character.name', event.target.value)} /></Field>
          <Field label="身份 / 故事职责" required help="例如：豪门继承人、刑警、失忆的证人。"><input disabled={busy} value={member.character.role} onChange={(event) => onChange('character.role', event.target.value)} /></Field>
          <Field label="角色重要性" required help={coreCastImportanceHelp[member.importance]}>
            <select disabled={busy} value={member.importance} onChange={(event) => onChange('importance', event.target.value)}>{optionElements(coreCastImportanceOptions, coreCastImportanceLabels)}</select>
          </Field>
          {mode === 'adapt' ? <Field label="角色来源" required><select disabled={busy} value={member.origin} onChange={(event) => onChange('origin', event.target.value)}>{optionElements(coreCastOriginOptions, coreCastOriginLabels)}</select></Field> : null}
        </div>
      </div>

      {mode === 'adapt' && member.origin === 'source' ? (
        <div className="core-cast-source-picker">
          <strong>对应的源作角色 <RequiredMark /></strong>
          <div>{sourceMajorCharacters.map((source) => <label key={source.id}><input type="checkbox" checked={member.source_character_ids.includes(source.id)} disabled={busy} onChange={(event) => onSourceChange(source.id, event.target.checked)} /><span>{source.name || source.id}</span></label>)}</div>
        </div>
      ) : null}

      <div className="core-cast-card-section">
        <h3>在故事中要做什么</h3>
        <div className="core-cast-form-grid story-fields">
          <Field label="主线作用" required help="这个角色怎样推动主线，而不只是“出现”。"><textarea disabled={busy} value={member.mainline_function} onChange={(event) => onChange('mainline_function', event.target.value)} /></Field>
          <Field label="目标" required help="角色想要得到什么。"><textarea disabled={busy} value={member.character.goal} onChange={(event) => onChange('character.goal', event.target.value)} /></Field>
          <Field label="动机" required help="为什么非做不可。"><textarea disabled={busy} value={member.character.motivation} onChange={(event) => onChange('character.motivation', event.target.value)} /></Field>
          <Field label="核心冲突" required help="阻碍目标的内外矛盾。"><textarea disabled={busy} value={member.character.conflict} onChange={(event) => onChange('character.conflict', event.target.value)} /></Field>
          <Field label="成长变化" required help="从故事开始到结束，角色会发生什么变化。"><textarea disabled={busy} value={member.character.arc} onChange={(event) => onChange('character.arc', event.target.value)} /></Field>
        </div>
      </div>

      <details className="core-cast-details">
        <summary>人物表现与写作约束（建议检查）</summary>
        <div className="core-cast-form-grid">
          <Field label="性格特质" help="多个特质用逗号分隔；与语言风格至少填写一项。"><input disabled={busy} value={member.character.traits.join('，')} onChange={(event) => onChange('character.traits', splitList(event.target.value))} /></Field>
          <Field label="语言风格" help="例如：克制简短、喜欢反问。"><input disabled={busy} value={member.character.voice} onChange={(event) => onChange('character.voice', event.target.value)} /></Field>
          <Field label="不可破坏的约束" required help="例如：不会主动伤害家人。"><textarea disabled={busy} value={member.character.constraints.join('，')} onChange={(event) => onChange('character.constraints', splitList(event.target.value))} /></Field>
          {mode === 'adapt' && member.origin === 'original' ? <Field label="新增理由" required help="说明为什么改编必须加入这个原创角色。"><textarea disabled={busy} value={member.inclusion_rationale} onChange={(event) => onChange('inclusion_rationale', event.target.value)} /></Field> : null}
        </div>
      </details>

      <div className="core-cast-card-footer">
        <label className="core-cast-no-relation"><input checked={member.no_core_relationships} disabled={busy} type="checkbox" onChange={(event) => onChange('no_core_relationships', event.target.checked)} /><span>这个角色目前没有必须锁定的核心关系</span></label>
        <button className="tool-button danger-ghost" disabled={busy} type="button" onClick={onDelete}>删除角色</button>
      </div>
    </fieldset>
  );
}

function PagedNavigator({ items, activeIndex, addLabel, addDisabled = false, onAdd, onChange }) {
  const safeIndex = clampPageIndex(activeIndex, items.length);
  const hasItems = items.length > 0;
  return (
    <div className="core-cast-pagination">
      <div className="core-cast-page-position">
        <strong>{hasItems ? `第 ${safeIndex + 1} 项，共 ${items.length} 项` : '暂无内容'}</strong>
        <span>每页只显示一项，点击名称可直接跳转。</span>
      </div>
      <div className={`core-cast-page-controls${onAdd ? '' : ' no-add'}`}>
        <button aria-label="上一项" className="core-cast-page-arrow" disabled={!hasItems || safeIndex === 0} type="button" onClick={() => onChange(safeIndex - 1)}>‹</button>
        <div aria-label="分页项目" className="core-cast-page-strip" role="tablist">{items.map((item, index) => (
          <button aria-selected={index === safeIndex} className={index === safeIndex ? 'active' : ''} key={item.id} role="tab" type="button" onClick={() => onChange(index)}>
            <span className="core-cast-page-number">{index + 1}</span>
            <span className="core-cast-page-copy"><strong>{item.label}</strong><small>{item.meta}</small></span>
            <span className={`core-cast-page-health ${item.complete ? 'complete' : 'incomplete'}`}>{item.complete ? '完整' : '待补充'}</span>
          </button>
        ))}</div>
        <button aria-label="下一项" className="core-cast-page-arrow" disabled={!hasItems || safeIndex >= items.length - 1} type="button" onClick={() => onChange(safeIndex + 1)}>›</button>
        {onAdd ? <button className="tool-button core-cast-page-add" disabled={addDisabled} type="button" onClick={onAdd}>＋ {addLabel}</button> : null}
      </div>
    </div>
  );
}

function CoreRelationshipEditor({ relationships, members, busy, onAdd, onChange, onDelete }) {
  const [activeIndex, setActiveIndex] = useState(0);
  const activeRelationship = relationships[activeIndex];

  useEffect(() => {
    setActiveIndex((current) => clampPageIndex(current, relationships.length));
  }, [relationships.length]);

  const addRelationship = () => {
    setActiveIndex(relationships.length);
    onAdd();
  };
  const deleteRelationship = () => {
    onDelete(activeIndex);
    setActiveIndex((current) => clampPageIndex(current, relationships.length - 1));
  };

  return (
    <section className="core-cast-relationships" aria-labelledby="core-cast-relationships-title">
      <div className="core-cast-section-heading">
        <div><h2 id="core-cast-relationships-title">核心关系</h2><p>只记录会影响主线或人物选择的关系。普通同事、路人关系不必添加。</p></div>
      </div>
      <PagedNavigator
        activeIndex={activeIndex}
        addDisabled={busy || members.length < 2}
        addLabel="添加关系"
        items={relationships.map((relationship, index) => ({
          id: relationship.id || `relationship-${index}`,
          label: relationship.label || `关系 ${index + 1}`,
          meta: relationshipTypeLabels[relationship.type],
          complete: relationshipIsComplete(relationship)
        }))}
        onAdd={addRelationship}
        onChange={setActiveIndex}
      />
      {!relationships.length ? <div className="core-cast-empty"><strong>还没有核心关系</strong><span>如果人物确实互不关联，请在对应角色卡中勾选“没有必须锁定的核心关系”。</span></div> : null}
      {activeRelationship ? <div className="core-cast-relationship-list">
          <fieldset className="core-cast-relationship-card" key={activeRelationship.id || `relationship-${activeIndex}`}>
            <legend>{activeRelationship.label || `关系 ${activeIndex + 1}`}</legend>
            <div className="core-cast-relationship-line">
              <Field label="角色 A" required><select disabled={busy} value={activeRelationship.source_character_id} onChange={(event) => onChange(activeIndex, 'source_character_id', event.target.value)}><option value="">请选择角色</option>{memberOptions(members)}</select></Field>
              <span className="core-cast-relation-arrow">与</span>
              <Field label="关系类型" required><select disabled={busy} value={activeRelationship.type} onChange={(event) => onChange(activeIndex, 'type', event.target.value)}>{optionElements(coreCastRelationshipTypes, relationshipTypeLabels)}</select></Field>
              <span className="core-cast-relation-arrow">连接</span>
              <Field label="角色 B" required><select disabled={busy} value={activeRelationship.target_character_id} onChange={(event) => onChange(activeIndex, 'target_character_id', event.target.value)}><option value="">请选择角色</option>{memberOptions(members)}</select></Field>
            </div>
            <div className="core-cast-form-grid">
              <Field label="关系名称" help="例如：互相救赎、继承权争夺。"><input disabled={busy} value={activeRelationship.label} onChange={(event) => onChange(activeIndex, 'label', event.target.value)} /></Field>
              <Field label="关系如何影响主线" help="说明矛盾、利益或情感怎样改变人物选择。"><textarea disabled={busy} value={activeRelationship.description} onChange={(event) => onChange(activeIndex, 'description', event.target.value)} /></Field>
            </div>
            <details className="core-cast-details compact">
              <summary>更多关系设置（通常无需修改）</summary>
              <div className="core-cast-form-grid">
                <Field label="影响方向"><select disabled={busy} value={activeRelationship.direction} onChange={(event) => onChange(activeIndex, 'direction', event.target.value)}>{optionElements(coreCastRelationshipDirections, relationshipDirectionLabels)}</select></Field>
                <Field label="初始状态"><select disabled={busy} value={activeRelationship.status} onChange={(event) => onChange(activeIndex, 'status', event.target.value)}>{optionElements(coreCastRelationshipStatuses, relationshipStatusLabels)}</select></Field>
                <Field label="关系起点"><input disabled={busy} placeholder="例如：故事开始前五年" value={activeRelationship.since} onChange={(event) => onChange(activeIndex, 'since', event.target.value)} /></Field>
                <Field label="关系约束" help="多个约束用逗号分隔。"><input disabled={busy} value={activeRelationship.constraints.join('，')} onChange={(event) => onChange(activeIndex, 'constraints', splitList(event.target.value))} /></Field>
              </div>
            </details>
            <button className="tool-button danger-ghost core-cast-delete-relation" disabled={busy} type="button" onClick={deleteRelationship}>删除关系</button>
          </fieldset>
      </div> : null}
    </section>
  );
}

function SourceDispositionEditor({ draft, sourceMajorCharacters, busy, onChange }) {
  const [activeIndex, setActiveIndex] = useState(0);
  const source = sourceMajorCharacters[activeIndex];
  const disposition = source ? draft.source_dispositions.find((item) => item.source_character_id === source.id) || { action: 'keep', target_character_ids: [], rationale: '' } : null;
  const targetIDs = disposition?.target_character_ids || [];

  useEffect(() => {
    setActiveIndex((current) => clampPageIndex(current, sourceMajorCharacters.length));
  }, [sourceMajorCharacters.length]);

  return (
    <section className="core-cast-dispositions" aria-labelledby="source-disposition-title">
      <div className="core-cast-section-heading"><div><h2 id="source-disposition-title">源作角色处理</h2><p>说明每个主要原著角色在新故事中保留、合并、拆分还是不采用。</p></div></div>
      <PagedNavigator
        activeIndex={activeIndex}
        items={sourceMajorCharacters.map((item) => {
          const itemDisposition = draft.source_dispositions.find((candidate) => candidate.source_character_id === item.id);
          const complete = Boolean(itemDisposition?.rationale && (itemDisposition.action === 'exclude' || itemDisposition.target_character_ids?.length));
          return { id: item.id, label: item.name || item.id, meta: sourceDispositionLabels[itemDisposition?.action] || '待选择', complete };
        })}
        onChange={setActiveIndex}
      />
      {source && disposition ? <div className="core-cast-disposition-list"><fieldset key={source.id}><legend>{source.name || source.id}</legend>
          <Field label="处理方式" required><select aria-label={`${source.name || source.id} 处理方式`} disabled={busy} value={disposition.action} onChange={(event) => onChange(source.id, { action: event.target.value, target_character_ids: [] })}>{optionElements(sourceDispositionActions, sourceDispositionLabels)}</select></Field>
          {disposition.action === 'exclude' ? <p className="muted">这个角色不会映射到新故事角色。</p> : disposition.action === 'split' ? <div className="core-cast-target-picker"><strong>拆分到哪些新角色</strong>{draft.members.map((member) => <label key={member.character.id}><input type="checkbox" checked={targetIDs.includes(member.character.id)} disabled={busy} onChange={(event) => onChange(source.id, { target_character_ids: event.target.checked ? [...targetIDs, member.character.id] : targetIDs.filter((id) => id !== member.character.id) })} /><span>{member.character.name || member.character.id}</span></label>)}</div> : <Field label="对应的新角色" required><select disabled={busy} value={targetIDs[0] || ''} onChange={(event) => onChange(source.id, { target_character_ids: event.target.value ? [event.target.value] : [] })}><option value="">请选择角色</option>{memberOptions(draft.members)}</select></Field>}
          <Field label="处理理由" required><textarea disabled={busy} value={disposition.rationale || ''} onChange={(event) => onChange(source.id, { rationale: event.target.value })} /></Field>
        </fieldset></div> : <div className="core-cast-empty">资料包中没有可处理的源作角色。</div>}
    </section>
  );
}

function CompletionNotice({ completion }) {
  if (completion?.complete) return <div className="core-cast-complete"><strong>核心角色资料已完整</strong><span>保存后即可点击“确认核心角色”。</span></div>;
  const missing = Array.isArray(completion?.missing) ? completion.missing : [];
  if (!missing.length) return null;
  return <div className="core-cast-missing" role="alert"><strong>还需要补充 {missing.length} 项</strong><ul>{missing.map((item, index) => <li key={`${item.code}-${item.member_id || item.source_id || index}`}>{missingItemLabel(item)}</li>)}</ul></div>;
}

function ReadOnlyCoreCast({ value, completion, confirmed, mode, sourceMajorCharacters }) {
  return (
    <section className="core-cast-editor core-cast-readonly" aria-label="核心角色只读视图">
      <div className="core-cast-section-heading"><div><h2>核心角色</h2><p>{value.members.length ? '这是当前已保存内容。需要修改时，请返回共创工作区。' : mode === 'adapt' && sourceMajorCharacters.length ? '目标核心角色尚未保存，先展示已经完成分析的来源角色。' : '当前还没有已保存的核心角色。'}</p></div><span className={`core-cast-status ${confirmed ? 'confirmed' : 'pending'}`}>{confirmed ? '已确认' : '未确认'}</span></div>
      <div className="core-cast-readonly-list">{value.members.length ? value.members.map((member, index) => <article key={member.character.id || index}>
        <header><h3>{member.character.name || `角色 ${index + 1}`}</h3><span>{coreCastImportanceLabels[member.importance] || member.importance}</span></header>
        <dl className="foundation-metrics">
          <Metric label="角色代号" value={member.character.id} /><Metric label="角色来源" value={coreCastOriginLabels[member.origin] || member.origin} />
          <Metric label="身份 / 职责" value={member.character.role} /><Metric label="主线作用" value={member.mainline_function} />
          <Metric label="目标" value={member.character.goal} /><Metric label="动机" value={member.character.motivation} />
          <Metric label="核心冲突" value={member.character.conflict} /><Metric label="成长变化" value={member.character.arc} />
          <Metric label="性格 / 语言" value={[member.character.traits.join('、'), member.character.voice].filter(Boolean).join(' / ')} /><Metric label="写作约束" value={member.character.constraints.join('、')} />
          {mode === 'adapt' ? <Metric label="对应源作角色" value={member.source_character_ids.join('、')} /> : null}
        </dl>
      </article>) : <SourceCharacterCandidates mode={mode} characters={sourceMajorCharacters} />}</div>
      {value.planned_relationships.length ? <div className="core-cast-readonly-relations"><h3>核心关系</h3>{value.planned_relationships.map((relationship, index) => <p key={relationship.id || index}>{memberName(value.members, relationship.source_character_id)} — {relationshipTypeLabels[relationship.type] || relationship.type} — {memberName(value.members, relationship.target_character_id)}</p>)}</div> : null}
      <CompletionNotice completion={completion} />
    </section>
  );
}

function SourceCharacterCandidates({ mode, characters = [] }) {
  if (mode !== 'adapt' || !characters.length) return <div className="core-cast-empty">暂无核心角色</div>;
  return <section className="core-cast-source-candidates" aria-label="来源分析角色">
    <div className="core-cast-empty"><strong>目标核心角色尚未生成</strong><span>以下人物来自已经完成的原著分析。进入共创工作区后，将其映射或改写为目标故事的核心角色。</span></div>
    <div className="core-cast-readonly-list">{characters.map((character, index) => <article key={character.id || character.name || index}>
      <header><h3>{character.name || `来源角色 ${index + 1}`}</h3><span>来源分析角色</span></header>
      <dl className="foundation-metrics core-cast-source-details">
        <Metric label="来源角色代号" value={character.id} /><Metric label="身份 / 职责" value={character.role} />
        <Metric label="别名" value={(character.aliases || []).join('、')} /><Metric label="阵营" value={character.faction} />
        <Metric label="人物介绍" value={character.description} wide /><Metric label="性格特征" value={(character.traits || []).join('、')} wide />
        <Metric label="人物弧光" value={character.arc} wide /><Metric label="目标" value={character.goal} />
        <Metric label="动机" value={character.motivation} /><Metric label="核心冲突" value={character.conflict} />
        <Metric label="语言风格" value={character.voice} /><Metric label="写作约束" value={(character.constraints || []).join('、')} wide />
        <Metric label="补充备注" value={character.notes} wide />
      </dl>
    </article>)}</div>
  </section>;
}

function Field({ label, required = false, help = '', children }) {
  return <label className="core-cast-field"><span>{label}{required ? <RequiredMark /> : null}</span>{children}{help ? <small>{help}</small> : null}</label>;
}

function RequiredMark() { return <em aria-label="必填">必填</em>; }
function Metric({ label, value, wide = false }) { return <div className={wide ? 'core-cast-metric-wide' : undefined}><dt>{label}</dt><dd>{value || '—'}</dd></div>; }
function memberOptions(members) { return members.map((member) => <option key={member.character.id} value={member.character.id}>{member.character.name || member.character.id}</option>); }
function optionElements(options, labels) { return options.map((option) => <option key={option} value={option}>{labels[option] || option}</option>); }
function memberName(members, id) { return members.find((member) => member.character.id === id)?.character.name || id || '未选择角色'; }
function splitList(value) { return String(value || '').split(/[,，]/).map((item) => item.trim()).filter(Boolean); }
function clampPageIndex(index, count) { return count > 0 ? Math.max(0, Math.min(Number(index) || 0, count - 1)) : 0; }

function memberIsComplete(member, mode) {
  const character = member?.character || {};
  const requiredText = [character.id, character.name, character.role, member?.mainline_function, character.goal, character.motivation, character.conflict, character.arc];
  const hasCharacterTexture = (character.traits || []).length > 0 || Boolean(String(character.voice || '').trim());
  const hasConstraints = (character.constraints || []).length > 0;
  const hasSource = mode !== 'adapt' || member?.origin !== 'source' || (member?.source_character_ids || []).length > 0;
  const hasInclusionReason = mode !== 'adapt' || member?.origin !== 'original' || Boolean(String(member?.inclusion_rationale || '').trim());
  return requiredText.every((value) => Boolean(String(value || '').trim())) && hasCharacterTexture && hasConstraints && hasSource && hasInclusionReason;
}

function relationshipIsComplete(relationship) {
  return Boolean(
    relationship?.source_character_id &&
    relationship?.target_character_id &&
    relationship.source_character_id !== relationship.target_character_id &&
    relationship?.type &&
    relationship?.direction &&
    relationship?.status
  );
}

function missingItemLabel(item) {
  const labels = {
    member_id_required: '请填写角色代号。', duplicate_member_id: '角色代号不能重复。', member_identity_ambiguous: '角色姓名或别名与其他核心角色重复。',
    importance_invalid: '请选择有效的角色重要性。', origin_invalid: '请选择有效的角色来源。', normal_origin_invalid: '原创共创中的角色必须选择“原创角色”。',
    source_character_ids_required: '源作角色需要勾选对应的原著人物。', member_source_character_unknown: '选择的源作角色已不存在，请重新选择。',
    name_required: '请填写角色姓名。', role_required: '请填写角色身份或故事职责。', mainline_function_required: '请填写角色的主线作用。',
    goal_required: '请填写角色目标。', motivation_required: '请填写角色动机。', conflict_required: '请填写角色核心冲突。', arc_required: '请填写角色成长变化。',
    traits_or_voice_required: '性格特质与语言风格至少填写一项。', constraints_required: '请填写不可破坏的角色约束。', inclusion_rationale_required: '原创改编角色需要填写新增理由。',
    protagonist_required: '至少需要一位“主角”或“联合主角”。', relationship_id_required: '请保存新关系以生成关系代号。', relationship_id_duplicate: '存在重复的关系代号。',
    relationship_source_missing: '关系中的角色 A 不存在或尚未选择。', relationship_target_missing: '关系中的角色 B 不存在或尚未选择。', relationship_self_loop: '一条关系不能连接同一个角色。',
    relationship_invalid: '关系类型、方向或状态无效，请重新选择。', relationship_duplicate: '存在重复的核心关系。', relationship_or_declaration_required: '角色需要至少一条核心关系，或勾选“没有必须锁定的核心关系”。',
    source_signature_required: '缺少当前源作版本信息，请重新载入改编资料。'
  };
  const label = labels[item?.code];
  const character = item?.member_id ? `（角色：${item.member_id}）` : item?.source_id ? `（源作角色：${item.source_id}）` : '';
  return `${label || item?.description || '请检查这一项。'}${character}`;
}
