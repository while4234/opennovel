import { ExpansionImpactList } from './ExpansionImpactList.jsx';
import { expansionFormLabel, expansionLocationLabel } from './expansion-state.js';

export function ExpansionPreview({ preview, onAdjust, onConfirm, onCancel, busy }) {
  if (!preview) return null;
  return <section className="expansion-preview" aria-label="扩写规划预览">
    <h3>{expansionFormLabel(preview.form)}</h3><p>{preview.reason}</p><dl><dt>位置</dt><dd>{expansionLocationLabel(preview.location)}</dd><dt>章节</dt><dd>{preview.chapter_count} 章</dd><dt>每章软区间</dt><dd>{preview.chapter_min_words}–{preview.chapter_max_words} 字</dd><dt>总字数软区间</dt><dd>{preview.total_min_words}–{preview.total_max_words} 字</dd></dl>
    <details><summary>专业判断依据</summary><p>目标：{preview.assessment?.goal}</p><p>冲突：{preview.assessment?.conflict}</p><p>选择与代价：{preview.assessment?.choice} / {preview.assessment?.cost}</p><p>结果：{preview.assessment?.result}</p><p>人物阶段：{preview.assessment?.character_stage_change}</p><p>节奏：{preview.assessment?.volume_pacing_effect}</p></details>
    <ExpansionImpactList impacts={preview.impacts} mode={preview.mode} />
    {preview.display_mappings?.length ? <section aria-label="目标章与原著章映射"><h4>目标 / 原著映射</h4><ul>{preview.display_mappings.map((mapping, index) => <li key={`${mapping.target_display}-${index}`}><strong>{mapping.target_display}</strong>{mapping.source_display ? ` · ${mapping.source_display}` : ''}{mapping.addition_label ? ` · ${mapping.addition_label}` : ''}</li>)}</ul></section> : null}
    <div className="expansion-adjustments" aria-label="快捷调整"><button type="button" disabled={busy} onClick={() => onAdjust('compact')}>更紧凑</button><button type="button" disabled={busy} onClick={() => onAdjust('full')}>更充分</button><button type="button" disabled={busy} onClick={() => onAdjust('separate_volume')}>单独成卷</button><button type="button" disabled={busy} onClick={() => onAdjust('default', true)}>修改描述</button></div>
    <div className="expansion-actions"><button type="button" disabled={busy || preview.obsolete || preview.cancelled} onClick={onConfirm}>确认影响并进入固定修订</button><button type="button" disabled={busy} onClick={onCancel}>取消预览</button></div>
  </section>;
}
