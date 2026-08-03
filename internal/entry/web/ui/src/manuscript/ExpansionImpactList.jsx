export function ExpansionImpactList({ impacts = [], mode }) {
  return <section aria-label="扩写影响"><h4>影响与证据</h4><ul>{impacts.map((impact, index) => <li key={`${impact.change}-${index}`}><strong>{impact.level === 'required' ? '必须' : '建议'}</strong> {impact.change}<ul>{(impact.evidence || []).map((evidence) => <li key={evidence}>{evidence}</li>)}</ul></li>)}</ul>{mode === 'adaptation' ? <p>目标章与原著章编号分别显示；新增剧情不冒充原著覆盖。</p> : null}</section>;
}
