import { foundationReadonlyReasonLabel, shortSignature } from './foundationModel.js';
import { sourceDispositionLabels } from '../coreCast.js';
import { characterConfirmationRequiredFromWorkspace } from './characterConfirmation.js';

export function FoundationOverview({ server, draft, workspace, disabled, premiseError, onPremiseChange, onOpenCharacters }) {
  const runtime = server.activeRevision;
  const planning = server.planningReview;
  const confirmedBrief = server.mode === 'normal' ? String(planning?.brief || '').trim() : '';
  const premise = draft.premise || confirmedBrief;
  const showingConfirmedBrief = !draft.premise.trim() && Boolean(confirmedBrief);
  const characterConfirmationRequired = characterConfirmationRequiredFromWorkspace(workspace);
  const characterActionLabel = characterConfirmationRequired
    ? '前往角色卡确认'
    : workspace?.candidate
      ? '查看角色卡进度'
      : '前往角色卡生成';
  return <div className="foundation-overview-grid">
    <section className="foundation-card" aria-labelledby="foundation-overview-target">
      <h3 id="foundation-overview-target">目标 StoryFoundation</h3>
      <dl className="foundation-metrics">
        <Metric label="模式" value={server.mode === 'adaptation' ? '改编' : '原创'} />
        <Metric label="目标 revision" value={server.baseRevision} />
        <Metric label="audit" value={shortSignature(server.baseAuditSignature)} />
        <Metric label="角色" value={draft.characters.length} />
        <Metric label="计划关系" value={draft.relationships.length} />
        <Metric label="世界规则" value={draft.world_rules.length} />
        <Metric label="可编辑" value={server.editable ? '是' : `否：${foundationReadonlyReasonLabel(server.readonlyReason)}`} />
        <Metric label="活动修订" value={runtime ? `${runtime.stage} · ${runtime.revision_id || runtime.session_id}` : '无'} />
        <Metric label="规划审核" value={planning ? `${planning.state || planning.status || 'pending'} · rev ${planning.revision || 0}` : '无'} />
        <Metric label="核心角色" value={server.coreCastConfirmed ? '已确认' : '需要确认'} />
      </dl>
      <label className="foundation-premise-field"><span>目标故事前提{showingConfirmedBrief ? '（共创确认稿）' : ''}</span><textarea aria-invalid={Boolean(premiseError && !premise)} disabled={disabled} value={premise} onChange={(event) => onPremiseChange(event.target.value)} />{showingConfirmedBrief ? <small>该内容来自已确认的共创稿；本轮角色确认后会随 StoryFoundation 正式发布。</small> : premiseError ? <small className="field-error">{premiseError}</small> : null}</label>
      {!server.coreCastConfirmed ? <button className={`tool-button ${characterConfirmationRequired ? 'accent' : ''}`} type="button" onClick={onOpenCharacters}>{characterActionLabel}</button> : null}
    </section>

    {server.mode === 'adaptation' ? <SourceFoundation source={server.sourceFoundation} server={server} /> : null}
  </div>;
}

function SourceFoundation({ source, server }) {
  const dispositions = Array.isArray(server.coreCast?.source_dispositions) ? server.coreCast.source_dispositions : [];
  const members = Array.isArray(server.coreCast?.members) ? server.coreCast.members : [];
  const targetByID = new Map(members.map((member) => [member?.character?.id, member]));
  return <section className="foundation-card source-foundation" aria-labelledby="foundation-overview-source">
    <div className="foundation-card-title">
      <h3 id="foundation-overview-source">SourceFoundation（只读）</h3>
      <span className="foundation-badge readonly">不可写入</span>
    </div>
    <dl className="foundation-metrics">
      <Metric label="source signature" value={shortSignature(source?.source_signature || server.modeSpecific?.source_signature)} />
      <Metric label="来源章节" value={source?.source_chapter_count || 0} />
      <Metric label="来源角色" value={source?.characters?.length || 0} />
      <Metric label="来源规则" value={source?.world_rules?.length || 0} />
    </dl>
    <h4>原著前提</h4><p className="foundation-long-text">{source?.premise || '未提供'}</p>
    <h4>来源角色档案</h4>
    {(source?.characters || []).length ? <ul className="foundation-read-list">{source.characters.map((character, index) => <li key={character.id || character.name || index}>
      <strong>{character.name || character.id || `来源角色 ${index + 1}`}</strong>
      <span>{[character.role, character.description].filter(Boolean).join(' · ') || '暂无角色说明'}</span>
      {character.aliases?.length ? <small>别名：{character.aliases.join('、')}</small> : null}
    </li>)}</ul> : <p className="muted">来源分析尚未生成角色档案。</p>}
    <h4>来源主要角色处置与映射</h4>
    {dispositions.length ? <ul className="foundation-read-list">{dispositions.map((item) => <li key={item.source_character_id}>
      <strong>{sourceName(source, item.source_character_id)}</strong>
      <span>{sourceDispositionLabels[item.action] || item.action} → {item.target_character_ids?.map((id) => targetByID.get(id)?.character?.name || id).join('、') || '不映射'}</span>
      {item.rationale ? <small>{item.rationale}</small> : null}
    </li>)}</ul> : <p className="muted">暂无已确认的来源角色处置。</p>}
    <h4>原著规则</h4>
    <ul className="foundation-read-list">{(source?.world_rules || []).map((rule, index) => <li key={rule.id || index}><strong>{rule.title || rule.category || `规则 ${index + 1}`}</strong><span>{rule.rule}</span></li>)}</ul>
  </section>;
}

function sourceName(source, id) {
  return source?.characters?.find((character) => character.id === id)?.name || id;
}

function Metric({ label, value }) {
  return <div><dt>{label}</dt><dd>{value ?? '—'}</dd></div>;
}
