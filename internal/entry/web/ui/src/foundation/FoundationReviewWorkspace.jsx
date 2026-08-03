import { useEffect, useMemo, useState } from 'react';
import {
  BookOpen,
  Check,
  CircleDot,
  Database,
  GitBranch,
  Pencil,
  ShieldCheck,
  Sparkles,
  X
} from 'lucide-react';
import { foundationReadonlyReasonLabel } from './foundationModel.js';

const baseTabs = [
  { id: 'overview', label: '概要' },
  { id: 'characters', label: '角色' },
  { id: 'relationships', label: '关系' },
  { id: 'rules', label: '世界规则' }
];

function textValue(value, ...keys) {
  for (const key of keys) {
    const candidate = value?.[key];
    if (candidate !== undefined && candidate !== null && String(candidate).trim()) {
      return String(candidate).trim();
    }
  }
  return '';
}

function arrayValue(value, ...keys) {
  for (const key of keys) {
    if (Array.isArray(value?.[key])) return value[key];
  }
  return [];
}

function itemKey(value, fallback, ...keys) {
  return textValue(value, ...keys) || fallback;
}

function CharacterGroup({ emptyLabel, characters, title }) {
  return (
    <section className="foundation-review-section">
      <header className="foundation-review-section-head">
        <div>
          <span>{title}</span>
          <strong>{characters.length}</strong>
        </div>
      </header>
      {characters.length ? (
        <div className="foundation-review-character-grid">
          {characters.map((character, index) => (
            <article className="foundation-review-character-card" key={itemKey(character, `${title}-${index}`, 'ID', 'id')}>
              <div>
                <strong>{textValue(character, 'Name', 'name') || '未命名角色'}</strong>
                <span>{textValue(character, 'Tier', 'tier') || '未分级'}</span>
              </div>
              <p>{textValue(character, 'Role', 'role') || '尚未填写职责'}</p>
              {textValue(character, 'Description', 'description') ? (
                <small>{textValue(character, 'Description', 'description')}</small>
              ) : null}
              <dl className="foundation-review-character-details">
                <CharacterDetail label="目标" value={textValue(character, 'Goal', 'goal')} />
                <CharacterDetail label="动机" value={textValue(character, 'Motivation', 'motivation')} />
                <CharacterDetail label="核心冲突" value={textValue(character, 'Conflict', 'conflict')} />
                <CharacterDetail label="人物弧" value={textValue(character, 'Arc', 'arc')} />
              </dl>
              <details className="foundation-review-character-more">
                <summary>查看完整角色卡</summary>
                <CharacterDetail label="阵营" value={textValue(character, 'Faction', 'faction')} />
                <CharacterDetail label="语言与行为" value={textValue(character, 'Voice', 'voice')} />
                <CharacterDetail
                  label="特征"
                  value={arrayValue(character, 'Traits', 'traits').map(String).filter(Boolean).join('、')}
                />
                <CharacterDetail
                  label="约束"
                  value={arrayValue(character, 'Constraints', 'constraints').map(String).filter(Boolean).join('；')}
                />
                <CharacterDetail label="备注" value={textValue(character, 'Notes', 'notes')} />
              </details>
            </article>
          ))}
        </div>
      ) : <div className="foundation-review-empty">{emptyLabel}</div>}
    </section>
  );
}

function CharacterDetail({ label, value }) {
  if (!value) return null;
  return (
    <div>
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}

function RuleGroup({ emptyLabel, rules, title, tone }) {
  return (
    <section className={`foundation-review-rule-group ${tone}`}>
      <header>
        <strong>{title}</strong>
        <span>{rules.length}</span>
      </header>
      {rules.length ? (
        <ul>
          {rules.map((rule, index) => (
            <li key={itemKey(rule, `${tone}-${index}`, 'ID', 'id', 'Rule', 'rule')}>
              <span>{textValue(rule, 'Category', 'category') || '通用'}</span>
              <p>{textValue(rule, 'Rule', 'rule')}</p>
            </li>
          ))}
        </ul>
      ) : <div className="foundation-review-empty">{emptyLabel}</div>}
    </section>
  );
}

function FoundationReviewOverview({ review }) {
  const characterStage = review.characterConfirmationRequired;
  return (
    <div className="foundation-review-overview">
      <section className="foundation-review-document">
        <header>
          <BookOpen size={18} />
          <div>
            <span>目标故事前提</span>
            <strong>本轮完整设定候选</strong>
          </div>
        </header>
        <p>{review.premise || '正在补全故事前提…'}</p>
      </section>
      <section className="foundation-review-checks" aria-label="Foundation 审核检查">
        <article className={characterStage ? 'waiting' : review.coreCastPreserved ? 'passed' : 'blocked'}>
          <ShieldCheck size={18} />
          <div>
            <strong>{review.adaptation ? '核心角色保持' : '核心角色基线'}</strong>
            <span>{characterStage ? '角色候选待确认' : review.coreCastPreserved ? '校验通过' : '等待生成'}</span>
          </div>
        </article>
        <article className={review.foundationAuditSignature ? 'passed' : 'waiting'}>
          <Sparkles size={18} />
          <div>
            <strong>完整设定审计</strong>
            <span>{review.foundationAuditSignature ? '已生成审计签名' : '等待审计'}</span>
          </div>
        </article>
        <article className="neutral">
          <CircleDot size={18} />
          <div>
            <strong>内容规模</strong>
            <span>{review.coreCharacters.length + review.supportingCharacters.length} 个角色 · {review.plannedRelationships.length} 条关系</span>
          </div>
        </article>
      </section>
    </div>
  );
}

function FoundationReviewCharacters({ review }) {
  return (
    <div className="foundation-review-stack">
      <CharacterGroup characters={review.coreCharacters} emptyLabel={review.characterConfirmationRequired ? '角色候选加载异常，请前往角色卡工作台刷新。' : '本轮没有核心角色。'} title="核心角色" />
      <CharacterGroup characters={review.supportingCharacters} emptyLabel={review.characterConfirmationRequired ? '本轮候选没有普通配角。' : '本轮没有普通配角。'} title="普通配角" />
    </div>
  );
}

function FoundationReviewRelationships({ review }) {
  return (
    <section className="foundation-review-section">
      <header className="foundation-review-section-head">
        <div>
          <span>计划关系</span>
          <strong>{review.plannedRelationships.length}</strong>
        </div>
      </header>
      {review.plannedRelationships.length ? (
        <div className="foundation-review-relationship-list">
          {review.plannedRelationships.map((relationship, index) => (
            <article key={itemKey(relationship, `relationship-${index}`, 'ID', 'id')}>
              <div className="foundation-review-relationship-route">
                <strong>{textValue(relationship, 'SourceCharacterID', 'sourceCharacterId', 'source_character_id') || '未指定'}</strong>
                <GitBranch size={16} />
                <strong>{textValue(relationship, 'TargetCharacterID', 'targetCharacterId', 'target_character_id') || '未指定'}</strong>
              </div>
              <span>{textValue(relationship, 'Label', 'label', 'Type', 'type') || '未定义关系'}</span>
              {textValue(relationship, 'Description', 'description') ? <p>{textValue(relationship, 'Description', 'description')}</p> : null}
            </article>
          ))}
        </div>
      ) : <div className="foundation-review-empty">
        {review.characterConfirmationRequired ? '角色确认后将继续生成并审计完整关系。' : '本轮没有计划关系。'}
      </div>}
    </section>
  );
}

function FoundationReviewRules({ review }) {
  const pendingLabel = review.characterConfirmationRequired ? '角色确认后由 Architect 继续生成。' : '';
  return (
    <div className="foundation-review-rule-columns">
      <RuleGroup emptyLabel={pendingLabel || '没有不可违反的规则。'} rules={review.hardWorldRules} title="Hard · 不可违反" tone="hard" />
      <RuleGroup emptyLabel={pendingLabel || '没有软性规则。'} rules={review.softWorldRules} title="Soft · 可调整" tone="soft" />
    </div>
  );
}

function FoundationReviewSource({ review }) {
  return (
    <div className="foundation-review-stack">
      <section className="foundation-review-document source">
        <header>
          <Database size={18} />
          <div>
            <span>SourceFoundation</span>
            <strong>只读原著证据</strong>
          </div>
        </header>
        <p>{review.sourcePremise || '暂无来源故事前提。'}</p>
      </section>
      <section className="foundation-review-source-grid">
        <RuleGroup emptyLabel="没有来源规则。" rules={review.sourceWorldRules} title="原著规则" tone="source" />
        <section className="foundation-review-rule-group source">
          <header>
            <strong>角色去向与映射</strong>
            <span>{review.sourceDispositions.length}</span>
          </header>
          {review.sourceDispositions.length ? (
            <ul>
              {review.sourceDispositions.map((disposition, index) => {
                const sourceID = textValue(disposition, 'SourceCharacterID', 'sourceCharacterId', 'source_character_id') || '未指定';
                const action = textValue(disposition, 'Action', 'action') || '未处理';
                const targets = arrayValue(disposition, 'TargetCharacterIDs', 'targetCharacterIds', 'target_character_ids').map(String);
                return (
                  <li key={`${sourceID}-${action}-${index}`}>
                    <span>{action}</span>
                    <p>{sourceID}{targets.length ? ` → ${targets.join('、')}` : ''}</p>
                  </li>
                );
              })}
            </ul>
          ) : <div className="foundation-review-empty">没有来源角色映射。</div>}
        </section>
      </section>
    </div>
  );
}

function FoundationManualRevision({ busy, onSubmit, review }) {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState(() => manualDraftFromReview(review));
  const disabled = busy || review.readonly;

  useEffect(() => {
    setOpen(false);
    setDraft(manualDraftFromReview(review));
  }, [review.foundationGeneration, review.foundationRevision]);

  useEffect(() => {
    if (!open) return undefined;
    const closeOnEscape = (event) => {
      if (event.key === 'Escape' && !disabled) setOpen(false);
    };
    globalThis.document?.addEventListener('keydown', closeOnEscape);
    return () => globalThis.document?.removeEventListener('keydown', closeOnEscape);
  }, [disabled, open]);

  const updateCharacter = (index, field, value) => {
    setDraft((current) => ({
      ...current,
      characters: current.characters.map((character, characterIndex) => (
        characterIndex === index ? { ...character, [field]: value } : character
      ))
    }));
  };
  const updateRelationship = (index, field, value) => {
    setDraft((current) => ({
      ...current,
      relationships: current.relationships.map((relationship, relationshipIndex) => (
        relationshipIndex === index ? { ...relationship, [field]: value } : relationship
      ))
    }));
  };
  const updateRule = (group, index, value) => {
    setDraft((current) => ({
      ...current,
      [group]: current[group].map((rule, ruleIndex) => (
        ruleIndex === index ? { ...rule, rule: value } : rule
      ))
    }));
  };
  const feedback = manualRevisionFeedback(review, draft);
  return (
    <section className={`foundation-manual-revision ${open ? 'open' : ''}`}>
      <header>
        <div>
          <span>手工精确修改</span>
          <strong>直接指定字段最终值</strong>
          <p>你填写的是字段最终值；系统会把差异作为硬约束重新生成依赖内容，并重新执行角色与设定审核。</p>
        </div>
        <button className="tool-button" disabled={disabled} onClick={() => setOpen((current) => !current)} type="button">
          {open ? <X size={16} /> : <Pencil size={16} />}
          {open ? '退出手动编辑' : '打开手动编辑'}
        </button>
      </header>
      {open ? (
        <div className="foundation-manual-revision-body">
          <label>
            <span>故事前提</span>
            <textarea
              rows="4"
              disabled={disabled}
              value={draft.premise}
              onChange={(event) => setDraft((current) => ({ ...current, premise: event.target.value }))}
            />
          </label>
          <details open>
            <summary>角色卡 · {draft.characters.length}</summary>
            <div className="foundation-manual-entity-list">
              {draft.characters.map((character, index) => (
                <fieldset key={character.id || `${character.name}-${index}`}>
                  <legend>{character.name || character.id || `角色 ${index + 1}`}</legend>
                  {manualCharacterFields.map(([field, label]) => (
                    <label key={field}>
                      <span>{label}</span>
                      <textarea
                        rows={field === 'description' || field === 'arc' ? 3 : 2}
                        disabled={disabled}
                        value={character[field]}
                        onChange={(event) => updateCharacter(index, field, event.target.value)}
                      />
                    </label>
                  ))}
                </fieldset>
              ))}
            </div>
          </details>
          <details>
            <summary>计划关系 · {draft.relationships.length}</summary>
            <div className="foundation-manual-entity-list">
              {draft.relationships.map((relationship, index) => (
                <fieldset key={relationship.id || `relationship-${index}`}>
                  <legend>{relationship.route}</legend>
                  <label>
                    <span>关系标签</span>
                    <input
                      value={relationship.label}
                      disabled={disabled}
                      onChange={(event) => updateRelationship(index, 'label', event.target.value)}
                    />
                  </label>
                  <label>
                    <span>关系动态</span>
                    <textarea
                      rows="3"
                      disabled={disabled}
                      value={relationship.description}
                      onChange={(event) => updateRelationship(index, 'description', event.target.value)}
                    />
                  </label>
                </fieldset>
              ))}
            </div>
          </details>
          <details>
            <summary>世界规则 · {draft.hardRules.length + draft.softRules.length}</summary>
            <div className="foundation-manual-entity-list">
              {[['hardRules', '不可违反'], ['softRules', '可调整']].map(([group, label]) => (
                draft[group].map((rule, index) => (
                  <label key={`${group}-${rule.id || index}`}>
                    <span>{label} · {rule.category || `规则 ${index + 1}`}</span>
                    <textarea
                      disabled={disabled}
                      rows="3"
                      value={rule.rule}
                      onChange={(event) => updateRule(group, index, event.target.value)}
                    />
                  </label>
                ))
              ))}
            </div>
          </details>
          <div className="foundation-manual-revision-submit">
            <div>
              <strong>{feedback ? '已检测到手工修改' : '尚未修改任何字段'}</strong>
              <span>提交后不会直接绕过审核；AI 只负责补齐依赖并重新校验。</span>
            </div>
            <button
              className="tool-button accent"
              disabled={disabled || !feedback}
              onClick={() => onSubmit?.(feedback)}
              type="button"
            >
              <Check size={16} />
              保存修改并重新审核
            </button>
          </div>
        </div>
      ) : null}
    </section>
  );
}

const manualCharacterFields = [
  ['name', '姓名'],
  ['role', '故事职责'],
  ['description', '人物描述'],
  ['goal', '外部目标'],
  ['motivation', '内在动机'],
  ['conflict', '核心冲突'],
  ['arc', '人物弧'],
  ['voice', '语言与行为约束']
];

function manualDraftFromReview(review = {}) {
  return {
    premise: String(review.premise || ''),
    characters: [...(review.coreCharacters || []), ...(review.supportingCharacters || [])].map((character) => ({
      id: textValue(character, 'ID', 'id'),
      name: textValue(character, 'Name', 'name'),
      ...Object.fromEntries(manualCharacterFields.map(([field]) => [
        field,
        textValue(character, field[0].toUpperCase() + field.slice(1), field)
      ]))
    })),
    relationships: (review.plannedRelationships || []).map((relationship) => ({
      id: textValue(relationship, 'ID', 'id'),
      route: `${textValue(relationship, 'SourceCharacterID', 'source_character_id') || '未指定'} → ${textValue(relationship, 'TargetCharacterID', 'target_character_id') || '未指定'}`,
      label: textValue(relationship, 'Label', 'label'),
      description: textValue(relationship, 'Description', 'description')
    })),
    hardRules: (review.hardWorldRules || []).map(manualRule),
    softRules: (review.softWorldRules || []).map(manualRule)
  };
}

function manualRule(rule = {}) {
  return {
    id: textValue(rule, 'ID', 'id'),
    category: textValue(rule, 'Category', 'category'),
    rule: textValue(rule, 'Rule', 'rule')
  };
}

export function manualRevisionFeedback(review = {}, draft = {}) {
  const baseline = manualDraftFromReview(review);
  const changes = [];
  appendManualChange(changes, '故事前提', baseline.premise, draft.premise);
  draft.characters?.forEach((character, index) => {
    const before = baseline.characters[index] || {};
    manualCharacterFields.forEach(([field, label]) => {
      appendManualChange(changes, `角色“${character.name || character.id}”的${label}`, before[field], character[field]);
    });
  });
  draft.relationships?.forEach((relationship, index) => {
    const before = baseline.relationships[index] || {};
    appendManualChange(changes, `关系“${relationship.route}”的标签`, before.label, relationship.label);
    appendManualChange(changes, `关系“${relationship.route}”的动态说明`, before.description, relationship.description);
  });
  for (const [group, label] of [['hardRules', '硬规则'], ['softRules', '软规则']]) {
    draft[group]?.forEach((rule, index) => {
      appendManualChange(changes, `${label}“${rule.category || rule.id || index + 1}”`, baseline[group]?.[index]?.rule, rule.rule);
    });
  }
  if (!changes.length) return '';
  return [
    '【用户逐项手工修改，以下字段值是硬约束】',
    ...changes,
    '必须逐项采用上述最终值；未列出的现有设定保持不变。仅重新生成为消除矛盾所必需的依赖字段，并重新执行角色、关系、世界规则与完整 Foundation 审核。'
  ].join('\n');
}

function appendManualChange(changes, label, before, after) {
  const previous = String(before || '').trim();
  const next = String(after || '').trim();
  if (previous !== next) changes.push(`- ${label}：${JSON.stringify(next)}`);
}

function FoundationReviewActions({
  busy,
  onConfirm,
  onConfirmCharacterCandidate,
  onManualRevise,
  onOpenCharacterConfirmation,
  onOpenCharacterRevision,
  onRevise,
  onReviseCharacterCandidate,
  planningRevision,
  review,
  setPlanningRevision
}) {
  if (review.characterConfirmationRequired) {
    const characterFeedback = String(planningRevision?.characterFeedback || '');
    const allowSupportingCharacters = Boolean(planningRevision?.allowSupportingCharacters);
    const canReviseCharacters = Boolean(!busy && characterFeedback.trim() && !review.readonly);
    return (
      <div className="foundation-review-action-layout">
        <section className="foundation-review-action-copy">
          <Pencil size={20} />
          <div>
            <span>角色卡审核</span>
            <h4>确认当前候选，或提交一轮明确的角色调整意见</h4>
            <p>当前只生成了角色卡与初步关系，完整 StoryFoundation 会在角色确认后继续生成。调整会创建新的角色候选；生成完成后仍需在角色工作台重新执行独立审核。</p>
          </div>
        </section>
        {planningRevision?.error ? <div className="error-banner compact">{planningRevision.error}</div> : null}
        <div className="foundation-review-choice-grid">
          <section className="foundation-review-choice confirm foundation-review-character-gate">
            <div className="foundation-review-choice-heading">
              <span className="foundation-review-choice-icon"><ShieldCheck size={22} /></span>
              <div>
                <span>角色卡符合预期</span>
                <strong>确认已经审核通过的角色卡</strong>
              </div>
            </div>
            <p>本轮 {review.coreCharacters.length + review.supportingCharacters.length} 个角色仍是候选状态。确认后才会发布 CoreCast，并继续补全世界规则与后续规划。</p>
            <div className="foundation-review-choice-note">
              <Check size={14} />
              仅在你确认后推进流程
            </div>
            <button
              className="tool-button accent full-width"
              disabled={busy}
              id="foundation-review-confirm-character"
              onClick={onConfirmCharacterCandidate}
              type="button"
            >
              <Check size={16} />
              确认角色卡并继续生成完整设定
            </button>
          </section>
          <section className="foundation-review-choice revise">
            <div className="foundation-review-choice-heading">
              <span className="foundation-review-choice-icon"><Pencil size={20} /></span>
              <div>
                <span>AI 智能调整</span>
                <strong>描述你希望角色达到的效果</strong>
              </div>
            </div>
            <label className="foundation-review-feedback">
              <textarea
                aria-label="角色卡修改意见"
                disabled={busy || review.readonly}
                maxLength="4000"
                placeholder="例如：强化女主主动调查真相的目标，让她与搭档在利益上存在冲突；保留现有核心角色，不要新增主要人物。"
                rows="3"
                value={characterFeedback}
                onChange={(event) => setPlanningRevision((previous) => ({
                  ...previous,
                  characterFeedback: event.target.value,
                  error: ''
                }))}
              />
              <small>{characterFeedback.length} / 4000</small>
            </label>
            <label className="check-row">
              <input
                checked={allowSupportingCharacters}
                disabled={busy || review.readonly}
                type="checkbox"
                onChange={(event) => setPlanningRevision((previous) => ({
                  ...previous,
                  allowSupportingCharacters: event.target.checked,
                  error: ''
                }))}
              />
              <span>允许 AI 在确有必要时新增非核心配角</span>
            </label>
            <button
              className="tool-button full-width"
              disabled={!canReviseCharacters}
              onClick={() => onReviseCharacterCandidate({
                feedback: characterFeedback.trim(),
                allowSupportingCharacters
              })}
              type="button"
            >
              <Sparkles size={16} />
              让 AI 按意见调整角色卡
            </button>
            <button className="tool-button full-width" disabled={busy} onClick={onOpenCharacterRevision} type="button">
              <Pencil size={16} />
              打开角色工作台逐项检查
            </button>
          </section>
        </div>
        {planningRevision?.message ? <div className={`workflow-status ${planningRevision.status || 'idle'}`}><span>{planningRevision.message}</span></div> : null}
      </div>
    );
  }
  const feedback = String(planningRevision?.feedback || '');
  const canRevise = Boolean(review.pending && !busy && feedback.trim() && !review.readonly);
  const canConfirm = Boolean(review.pending && !busy && review.coreCastPreserved && !review.readonly);
  return (
    <div className="foundation-review-action-layout">
      <section className="foundation-review-action-copy">
        <Pencil size={20} />
        <div>
          <span>最后一步</span>
          <h4>确认，或提交一轮明确的修改意见</h4>
          <p>{review.adaptation
            ? '修改意见只会重新生成目标 StoryFoundation，不会写回只读的 SourceFoundation。确认后才会进入后续规划。'
            : '修改意见会让 AI 重新生成一版完整 StoryFoundation；确认后才会进入后续规划。'}</p>
        </div>
      </section>
      {planningRevision?.error ? <div className="error-banner compact">{planningRevision.error}</div> : null}
      {review.readonly ? <div className="error-banner compact">当前只读：{foundationReadonlyReasonLabel(review.readonlyReason)}</div> : null}
      <div className="foundation-review-choice-grid">
        <section className="foundation-review-choice confirm">
          <div className="foundation-review-choice-heading">
            <span className="foundation-review-choice-icon"><ShieldCheck size={22} /></span>
            <div>
              <span>设定符合预期</span>
              <strong>确认当前完整设定</strong>
            </div>
          </div>
          <p>确认后进入章节规划与审核，当前角色、关系和世界规则会成为正式写作基线。</p>
          <div className="foundation-review-choice-note">
            <Check size={14} />
            仅在你确认后推进流程
          </div>
          <button className="tool-button accent full-width" disabled={!canConfirm} onClick={onConfirm} type="button">
            <Check size={16} />
            确认完整设定并继续
          </button>
        </section>
        <section className="foundation-review-choice revise">
          <div className="foundation-review-choice-heading">
            <span className="foundation-review-choice-icon"><Pencil size={20} /></span>
            <div>
              <span>AI 智能修改</span>
              <strong>描述你想达到的效果</strong>
            </div>
          </div>
          <label className="foundation-review-feedback">
            <textarea
              aria-label="修改意见"
              disabled={busy || review.readonly}
              maxLength="4000"
              placeholder={review.adaptation
                ? '例如：保留顾衡为明确的新男主与主视角，同时加强他和袁可欣的前期取证关系；不要改变原著角色映射。'
                : '例如：加强主角的主动目标与核心矛盾，补充搭档之间的利益冲突，并让世界规则更明确地约束关键选择。'}
              rows="3"
              value={feedback}
              onChange={(event) => setPlanningRevision((previous) => ({
                ...previous,
                feedback: event.target.value,
                error: ''
              }))}
            />
            <small>{feedback.length} / 4000</small>
          </label>
          <button className="tool-button full-width" disabled={!canRevise} onClick={onRevise} type="button">
            <Pencil size={16} />
            让 AI 按意见修订
          </button>
        </section>
      </div>
      <FoundationManualRevision
        busy={busy}
        onSubmit={onManualRevise}
        review={review}
      />
      {planningRevision?.message ? <div className={`workflow-status ${planningRevision.status || 'idle'}`}><span>{planningRevision.message}</span></div> : null}
    </div>
  );
}

export default function FoundationReviewWorkspace({
  busy = false,
  onConfirm = () => {},
  onConfirmCharacterCandidate = () => {},
  onManualRevise = () => {},
  onOpenCharacterConfirmation = () => {},
  onOpenCharacterRevision = () => {},
  onRevise = () => {},
  onReviseCharacterCandidate = () => {},
  onTabChange,
  planningRevision = {},
  review = {},
  selectedTab,
  setPlanningRevision = () => {}
}) {
  const tabs = useMemo(() => [
    ...baseTabs,
    ...(review.adaptation ? [{ id: 'source', label: '原著证据' }] : []),
    { id: 'actions', label: review.characterConfirmationRequired ? '查看并调整' : '确认与修改' }
  ], [review.adaptation]);
  const [internalTab, setInternalTab] = useState('overview');
  const activeTab = tabs.some((tab) => tab.id === selectedTab) ? selectedTab : internalTab;
  const selectTab = (tab) => {
    setInternalTab(tab);
    onTabChange?.(tab);
  };

  useEffect(() => {
    setInternalTab('overview');
  }, [review.foundationGeneration, review.foundationRevision]);

  const selectAdjacentTab = (event, index) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    let nextIndex = index;
    if (event.key === 'ArrowLeft') nextIndex = (index - 1 + tabs.length) % tabs.length;
    if (event.key === 'ArrowRight') nextIndex = (index + 1) % tabs.length;
    if (event.key === 'Home') nextIndex = 0;
    if (event.key === 'End') nextIndex = tabs.length - 1;
    const nextTab = tabs[nextIndex];
    selectTab(nextTab.id);
    globalThis.requestAnimationFrame?.(() => globalThis.document?.getElementById(`foundation-review-tab-${nextTab.id}`)?.focus());
  };

  return (
    <div className="proposal-workspace foundation-review-workspace">
      <header className="proposal-workspace-header foundation-review-header">
        <div>
          <div className="eyebrow">{review.adaptation ? '小说改编共创' : '普通共创'} · 审核工作区</div>
          <h3>StoryFoundation 设定审核</h3>
          <p>{review.characterConfirmationRequired
            ? '角色候选已经生成并审核通过；请先确认角色卡，再继续完整设定审核。'
            : '逐页检查本轮目标设定；确认与修改集中在最后一个页签。'}</p>
        </div>
        <div className="foundation-review-header-actions">
          <div className="proposal-workspace-metrics">
            <span>{review.characterConfirmationRequired ? '角色卡待确认' : review.collecting ? '生成中' : '待确认'}</span>
            <span>Revision {review.foundationRevision || '—'}</span>
            <span>Generation {review.foundationGeneration || 1}</span>
          </div>
          {review.characterConfirmationRequired ? (
            <button
              className="tool-button accent foundation-review-primary-action"
              disabled={busy || review.collecting}
              onClick={onConfirmCharacterCandidate}
              type="button"
            >
              <Check size={16} />
              确认角色卡
            </button>
          ) : null}
        </div>
      </header>
      {review.characterConfirmationRequired ? (
        <div className="foundation-review-candidate-notice" role="status">
          <ShieldCheck size={18} />
          <div>
            <strong>正在展示未发布的角色候选</strong>
            <span>共 {review.coreCharacters.length + review.supportingCharacters.length} 个角色、{review.plannedRelationships.length} 条关系；它们不是空白，也尚未写入正式 Foundation。</span>
          </div>
          <div className="foundation-review-candidate-actions">
            <button className="tool-button" onClick={() => selectTab('characters')} type="button">查看全部角色</button>
            <button className="tool-button" disabled={busy} onClick={() => selectTab('actions')} type="button">填写调整意见</button>
          </div>
        </div>
      ) : null}
      <nav aria-label="Foundation 审核内容" className="foundation-review-tabs" role="tablist">
        {tabs.map((tab, index) => (
          <button
            aria-controls={`foundation-review-panel-${tab.id}`}
            aria-selected={activeTab === tab.id}
            className={activeTab === tab.id ? 'active' : ''}
            id={`foundation-review-tab-${tab.id}`}
            key={tab.id}
            onClick={() => selectTab(tab.id)}
            onKeyDown={(event) => selectAdjacentTab(event, index)}
            role="tab"
            tabIndex={activeTab === tab.id ? 0 : -1}
            type="button"
          >
            {tab.label}
          </button>
        ))}
      </nav>
      <section
        aria-labelledby={`foundation-review-tab-${activeTab}`}
        className="foundation-review-panel-content"
        id={`foundation-review-panel-${activeTab}`}
        role="tabpanel"
      >
        {activeTab === 'overview' ? <FoundationReviewOverview review={review} /> : null}
        {activeTab === 'characters' ? <FoundationReviewCharacters review={review} /> : null}
        {activeTab === 'relationships' ? <FoundationReviewRelationships review={review} /> : null}
        {activeTab === 'rules' ? <FoundationReviewRules review={review} /> : null}
        {activeTab === 'source' ? <FoundationReviewSource review={review} /> : null}
        {activeTab === 'actions' ? (
          <FoundationReviewActions
            busy={busy}
            onConfirm={onConfirm}
            onConfirmCharacterCandidate={onConfirmCharacterCandidate}
            onManualRevise={onManualRevise}
            onOpenCharacterConfirmation={onOpenCharacterConfirmation}
            onOpenCharacterRevision={onOpenCharacterRevision}
            onRevise={onRevise}
            onReviseCharacterCandidate={onReviseCharacterCandidate}
            planningRevision={planningRevision}
            review={review}
            setPlanningRevision={setPlanningRevision}
          />
        ) : null}
      </section>
    </div>
  );
}
