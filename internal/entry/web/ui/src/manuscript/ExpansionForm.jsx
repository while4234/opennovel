export function ExpansionForm({ value, onChange, onSubmit, busy, mode, maxLength = 500, submitLabel = '让 AI 规划', busyLabel = '规划中…' }) {
  return <form className="expansion-form" onSubmit={(event) => { event.preventDefault(); onSubmit(); }}>
    <label>插入位置<select value={value.location} onChange={(event) => onChange({ ...value, location: event.target.value })}>
      <option value="inside">当前章内</option><option value="before">当前章前</option><option value="after">当前章后</option><option value="between">两章之间</option><option value="end_arc">当前弧末</option><option value="end_volume">当前卷末</option><option value="book_end">全书末尾</option>
    </select></label>
    <label>一句话描述<textarea required maxLength={maxLength} rows="2" value={value.sentence} onChange={(event) => onChange({ ...value, sentence: event.target.value })} placeholder="例如：让主角发现盟友隐瞒的证据，并被迫公开站队" /></label>
    <details><summary>高级设置</summary><label>规划偏好<select value={value.adjustment} onChange={(event) => onChange({ ...value, adjustment: event.target.value })}><option value="default">让 AI 规划</option><option value="compact">更紧凑</option><option value="full">更充分</option><option value="separate_volume">单独成卷</option></select></label></details>
    <p className="expansion-mode-copy">{mode === 'adaptation' ? '改编模式：会校验原著覆盖与受保护合同。' : '原创模式：不会读取或携带原著字段。'}</p>
    <button type="submit" disabled={busy || !value.sentence.trim()}>{busy ? busyLabel : submitLabel}</button>
  </form>;
}
