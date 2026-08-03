import { useState } from 'react';
import { foundationOptions, newFoundationWorldRule, normalizeWorldRule } from './foundationModel.js';

export function WorldRuleEditor({ value, disabled, sourceOnly = false, errors = {}, onChange }) {
  const [warning, setWarning] = useState('');
  const update = (index, field, raw) => {
    const current = value[index];
    if (current.strength === 'hard' && field === 'strength' && raw === 'soft') setWarning('将 hard rule 改为 soft 属于高风险变更；请等待服务端 preview 判断影响。');
    onChange(value.map((rule, itemIndex) => itemIndex === index ? normalizeWorldRule({ ...rule, [field]: field === 'tags' ? splitList(raw) : raw }) : rule));
  };
  const remove = (index) => {
    if (value[index].strength === 'hard') setWarning('删除 hard rule 属于高风险变更；服务端可能要求全书重新审查。');
    onChange(value.filter((_, itemIndex) => itemIndex !== index));
  };
  return <section aria-labelledby="foundation-rule-heading">
    <div className="foundation-section-head"><div><h2 id="foundation-rule-heading">{sourceOnly ? '原著世界规则（只读）' : '世界规则'}</h2><p>{sourceOnly ? '这些规则来自原著逐章事实分析；共创前可查看，但不会写入目标改编设定。' : '前端只提示 hard/soft 风险，不自行决定局部影响。'}</p></div><button className="tool-button" disabled={disabled} type="button" onClick={() => onChange([...value, newFoundationWorldRule()])}>添加规则</button></div>
    {warning ? <div className="warning-note" role="status">{warning}</div> : null}
    <div className="foundation-editor-list">{value.map((rule, index) => <fieldset className="foundation-editor-card" key={rule.id || index}>
      <legend>{rule.title || `规则 ${index + 1}`} {rule.strength === 'hard' ? <span className="foundation-badge risk">hard</span> : null}</legend>
      <label><span>ID（只读）</span><input readOnly value={rule.id} /></label>
      {['title', 'category', 'rule', 'boundary', 'priority', 'tags'].map((field) => <label key={field}><span>{ruleLabel(field)}</span>{['rule', 'boundary'].includes(field)
        ? <textarea aria-invalid={Boolean(errors[`world_rules.${index}.${field}`])} disabled={disabled} value={field === 'tags' ? rule.tags.join(', ') : rule[field]} onChange={(event) => update(index, field, event.target.value)} />
        : <input aria-invalid={Boolean(errors[`world_rules.${index}.${field}`])} disabled={disabled} type={field === 'priority' ? 'number' : 'text'} value={field === 'tags' ? rule.tags.join(', ') : rule[field]} onChange={(event) => update(index, field, event.target.value)} />}
        {errors[`world_rules.${index}.${field}`] ? <small className="field-error">{errors[`world_rules.${index}.${field}`]}</small> : null}</label>)}
      <label><span>强度</span><select disabled={disabled} value={rule.strength} onChange={(event) => update(index, 'strength', event.target.value)}>{foundationOptions.ruleStrengths.map((option) => <option key={option}>{option}</option>)}</select></label>
      <button className="tool-button danger-ghost" disabled={disabled} type="button" onClick={() => remove(index)}>删除规则</button>
    </fieldset>)}</div>
  </section>;
}

function splitList(value) { return String(value || '').split(/[,，]/).map((item) => item.trim()).filter(Boolean); }
function ruleLabel(field) { return ({ title: '标题', category: '类别', rule: '规则正文', boundary: '不可违反边界', priority: '优先级', tags: '标签（逗号分隔）' })[field]; }
