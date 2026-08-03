import http from 'node:http';
import { manuscriptMutationWebEvent } from '../../src/manuscript/manuscript-events.js';

const port = 4180;
const chapterId = 'ch_0123456789abcdef0123456789abcdef';
const revisionId = 'rev_0123456789abcdef0123456789abcdef';
const historyId = 'rev_11111111111111111111111111111111';
const deepReviewId = 'rev_00000000000000000000000000000105';
const paragraphs = Array.from({ length: 240 }, (_, index) => `第${index + 1}段${'长篇正文'.repeat(130)}`);
let generation = 1;
let failNextChapter = false;
let historyTombstoned = false;
let delayNextHistory = false;
let delayNextTree = false;
let failDelayedTree = false;
let delayNextChapter = false;
let manuscriptPhase = 'writing';
let manuscriptActionDialogue = null;
let foundationScenario = 'normal';
let foundationRevision = 3;
let foundationLastRequest = null;
let delayFoundationProjectA = false;
let characterRun = null;
let characterCandidate = null;
let characterFindings = [];
let characterPolls = 0;
const streams = new Set();
let expansionMetadata = await fetch('http://127.0.0.1:4182/api/test/expansion-metadata').then((response) => response.json());
const json = (response, status, body, headers = {}) => {
	if (response.destroyed || response.writableEnded) return;
	try {
		response.writeHead(status, { 'content-type': 'application/json; charset=utf-8', ...headers });
		response.end(JSON.stringify(body));
	} catch { /* aborted race-test requests are intentionally discarded */ }
};
const readJSON = (request) => new Promise((resolve, reject) => {
	let raw = '';
	request.on('data', (chunk) => { raw += chunk; });
	request.on('end', () => { try { resolve(JSON.parse(raw || '{}')); } catch (error) { reject(error); } });
});
const targetFoundation = (projectID = 'foundation-project-a') => ({
	schema_version: 2, revision: foundationRevision, premise: projectID.endsWith('-b') ? '项目 B 的目标故事' : '项目 A 的目标故事',
	characters: [{ id: 'hero', name: '林舟', aliases: ['阿舟'], role: '主角', gender: 'male', description: '守护城市', arc: '学会信任', traits: ['坚定'], tier: 'core', faction: '守夜人', goal: '阻止灾难', motivation: '责任', conflict: '不信任盟友', voice: '简洁', constraints: ['不背叛'], notes: '' }, ...(foundationScenario === 'graph' ? [
		{ id: 'ally', name: '闻溪', aliases: [], role: '盟友', gender: 'female', description: '', arc: '', traits: [], tier: 'major', faction: '守夜人', goal: '', motivation: '', conflict: '', voice: '', constraints: [], notes: '' },
		{ id: 'rival', name: '贺沉', aliases: [], role: '对手', gender: 'male', description: '', arc: '', traits: [], tier: 'support', faction: '旧王庭', goal: '', motivation: '', conflict: '', voice: '', constraints: [], notes: '' }
	] : [])],
	relationships: foundationScenario === 'graph' ? [
		{ id: 'rel-directed', source_character_id: 'hero', target_character_id: 'ally', type: 'mentor', label: '引路', direction: 'directed', status: 'active', tags: [], constraints: [] },
		{ id: 'rel-undirected', source_character_id: 'ally', target_character_id: 'rival', type: 'rival', label: '', direction: 'undirected', status: 'planned', tags: [], constraints: [] }
	] : [], relationships_reviewed: true,
	world_rules: [{ id: 'rule-1', title: '能力代价', category: 'magic', rule: '每次施法都会失去记忆', boundary: '不可无代价复活', strength: 'hard', priority: 10, tags: ['代价'] }]
});
const sourceFoundation = { source_signature: 'source-signature-1234567890', source_chapter_count: 20, premise: '原著世界里旧王归来', characters: [{ id: 'source-hero', name: '原著林舟', role: '旧王继承人', description: '背负流亡王庭的秘密，追查城市灾变。', arc: '从独行到信任盟友', traits: ['冷静', '执着'], goal: '阻止旧王复生', motivation: '守护城市', conflict: '血脉责任与自由选择冲突', voice: '克制简短', constraints: ['不会牺牲无辜'], faction: '流亡王庭', notes: '左手保留旧伤' }], world_rules: [{ id: 'source-rule', title: '原著规则', category: 'magic', rule: '魔法源自血脉', boundary: '血脉不可伪造', strength: 'hard' }] };
const coreCast = { version: 1, mode: 'adaptation', members: [{ character: targetFoundation().characters[0], importance: 'protagonist', origin: 'source', mainline_function: '推进主线', source_character_ids: ['source-hero'], inclusion_rationale: '保留原著主角', no_core_relationships: true }], planned_relationships: [], source_dispositions: [{ source_character_id: 'source-hero', action: 'rename', target_character_ids: ['hero'], rationale: '目标世界改名' }], content_signature: 'core-signature', confirmed_signature: 'core-signature' };
const foundationState = (projectID) => {
	const active = foundationScenario === 'failed' ? { revision_id: 'revision-failed', session_id: 'revision-failed', preview_id: 'preview-browser', stage: 'failed', resume_stage: 'candidate_generating', attempt: 1, generation: 8, last_error_class: 'foundation_recovery_failed', last_error: '安全的测试失败', updated_at: '2026-07-19T00:00:00Z' }
		: foundationScenario === 'active' ? { revision_id: 'revision-active', session_id: 'revision-active', preview_id: 'preview-browser', stage: 'regenerating', attempt: 2, generation: 9, updated_at: '2026-07-19T00:00:00Z' } : null;
	const readonly = foundationScenario === 'readonly';
	const sourceOnly = foundationScenario === 'source-only';
	const adaptation = foundationScenario === 'adaptation' || sourceOnly;
	return { project: { id: projectID, name: projectID }, foundation: { mode: adaptation ? 'adaptation' : 'normal', ...(adaptation ? { source_foundation: sourceFoundation, mode_specific: { source_signature: sourceFoundation.source_signature } } : {}), target_foundation: sourceOnly ? { schema_version: 2, revision: 0, premise: '', characters: [], relationships: [], relationships_reviewed: false, world_rules: [] } : targetFoundation(projectID), editable: !readonly && !active && !sourceOnly, readonly_reason: sourceOnly ? 'adaptation_intent_unavailable' : readonly ? 'body_files_started' : active ? 'active_foundation_revision' : '', base_revision: foundationRevision, base_audit_signature: `audit-${foundationRevision}`, core_cast_signature: sourceOnly ? '' : 'core-signature', ...(sourceOnly ? {} : { core_cast: coreCast, core_cast_completion: { complete: true, missing: [], blocking_reasons: [] } }), core_cast_confirmed: !sourceOnly, active_revision: active, planning_review: { state: 'confirmed', revision: 2 }, allowed_operations: foundationScenario === 'failed' ? ['get', 'retry'] : !readonly && !active && !sourceOnly ? ['get', 'preview', 'apply'] : ['get'] } };
};
const characterWorkspaceState = (projectID) => {
	const foundation = targetFoundation(projectID);
	const adaptation = foundationScenario === 'adaptation' || foundationScenario === 'source-only';
	return {
		project: { id: projectID, name: projectID },
		character_workspace: {
			mode: adaptation ? 'adaptation' : 'original',
			base_revision: foundationRevision,
			base_audit_signature: `audit-${foundationRevision}`,
			current_digest: 'a'.repeat(64),
			current: { revision: foundationRevision, digest: 'a'.repeat(64), foundation },
			...(characterCandidate ? { candidate: { revision: 4, digest: 'b'.repeat(64), foundation: characterCandidate } } : {}),
			...(characterRun ? { run: characterRun } : {}),
			completeness: foundation.characters.map((character) => ({
				character_id: character.id, tier: character.tier || 'important',
				status: character.id === 'hero' ? 'complete' : 'incomplete',
				missing: character.id === 'hero' ? [] : [{ code: 'goal_required', field: 'goal', severity: 'blocking', description: '需要目标' }]
			})),
			...(adaptation ? {
				source_coverage: { source_total: 1, decision_required: 1, mapped: 1, explicitly_excluded: 0, pending: 0, blocking_gaps: 0, decisions: [] },
				source_mappings: [{ id: 'map-hero', action: 'rename', source_character_ids: ['source-hero'], target_character_ids: ['hero'], rationale: '目标世界改名', evidence: [{ kind: 'source_fact', reference: '第 3 章', summary: '原著角色在灾变中守城。' }] }]
			} : { source_mappings: [] }),
			findings: characterFindings,
			diff: characterCandidate ? { version: 1, changes: [{ entity_type: 'character', entity_id: 'hero', kind: 'modified', changed_fields: ['voice'], high_risk: false }], core_cast_reconfirmation: false, foundation_reconfirmation: true, signature: 'diff-character' } : undefined,
			allowed_operations: characterRun?.status === 'running' ? [] : characterRun?.status === 'failed' ? ['analyze', 'review', 'retry', 'discard'] : characterCandidate ? ['analyze', 'review', 'discard'] : ['analyze', 'review']
		}
	};
};
const tree = () => ({ phase: manuscriptPhase, mode: 'adaptation', structure_revision: expansionMetadata.structure_revision, structure_signature: expansionMetadata.structure_signature, active_revision: { revision_id: revisionId, stage: 'audit_pending' }, nodes: [{ kind: 'volume', stable_id: 'vol_0123456789abcdef0123456789abcdef', display_label: '第一卷', state: 'planned', children: [{ kind: 'arc', stable_id: 'arc_0123456789abcdef0123456789abcdef', display_label: '第一故事弧', state: 'planned', children: [{ kind: 'chapter', stable_id: chapterId, display_order: 1, display_label: '真实后端长章', state: 'review_pending', has_current: true, has_candidate: true, has_history: true, content_signature: 'outline-signature', target_display: '目标第 1 章', source_display: '原著第 7–8 章' }] }] }] });
const extraTreeChapters = Array.from({ length: 129 }, (_, index) => ({
  kind: 'chapter', stable_id: `ch_${(index + 2).toString(16).padStart(32, '0')}`, display_order: index + 2,
  display_label: `keyboard chapter ${index + 2}`, state: 'planned', has_current: false, has_candidate: false, has_history: false
}));
const treeWithLargeCatalog = () => {
  const data = tree();
  data.nodes[0].children[0].children.push(...extraTreeChapters);
  return data;
};

function manuscriptChapter(url, stableId = chapterId) {
  const view = url.searchParams.get('view') || 'current';
  if (view === 'candidate' && url.searchParams.get('version') !== revisionId) return null;
  const cursor = Number(url.searchParams.get('cursor') || 0), limit = Number(url.searchParams.get('limit') || 40);
  const prefix = view === 'candidate' ? '候选：' : generation > 1 ? '发布后：' : '当前：';
  const values = paragraphs.slice(cursor, cursor + limit).map((paragraph) => prefix + paragraph);
  return { chapter: { stable_id: stableId, display_chapter: 1, view, revision_id: view === 'candidate' ? revisionId : undefined, content_signature: `${stableId}-${view}-signature-${generation}`, paragraphs: values.map((value) => stableId === chapterId ? value : `B章节：${value}`), next_cursor: cursor + limit < paragraphs.length ? cursor + limit : null, total_paragraphs: paragraphs.length } };
}
const server = http.createServer(async (request, response) => {
	response.on('error', () => {});
  const url = new URL(request.url, `http://${request.headers.host}`), path = url.pathname;
  if (path === '/health') return json(response, 200, { ok: true });
	if (path === '/api/test/reset' && request.method === 'POST') { generation = 1; manuscriptPhase = 'writing'; manuscriptActionDialogue = null; failNextChapter = false; historyTombstoned = false; delayNextHistory = false; delayNextTree = false; failDelayedTree = false; delayNextChapter = false; foundationScenario = 'normal'; foundationRevision = 3; foundationLastRequest = null; delayFoundationProjectA = false; characterRun = null; characterCandidate = null; characterFindings = []; characterPolls = 0; return json(response, 200, { reset: true }); }
	if (path === '/api/test/foundation/scenario' && request.method === 'POST') { foundationScenario = url.searchParams.get('value') || 'normal'; return json(response, 200, { scenario: foundationScenario }); }
	if (path === '/api/test/foundation/delay-project-a' && request.method === 'POST') { delayFoundationProjectA = true; return json(response, 200, { delayed: true }); }
	if (path === '/api/test/foundation/last-request') return json(response, 200, foundationLastRequest || {});
	const characterMatch = path.match(/^\/api\/projects\/(foundation-project-[ab])\/foundation\/characters(?:\/(analyze|review|retry|discard))?$/);
	if (characterMatch) {
		const projectID = characterMatch[1], action = characterMatch[2] || 'get';
		if (action === 'get') {
			if (characterRun?.status === 'running' && ++characterPolls >= 1) characterRun = { ...characterRun, status: 'completed', stage: 'completed', finished_at: '2026-07-27T08:00:02Z', updated_at: '2026-07-27T08:00:02Z' };
			return json(response, 200, characterWorkspaceState(projectID));
		}
		const body = await readJSON(request);
		foundationLastRequest = { action: `character-${action}`, body, project_id: projectID };
		if (action === 'analyze') {
			const input = body.candidate || targetFoundation(projectID);
			characterCandidate = {
				...input,
				characters: input.characters.map((character) => character.id === 'hero' ? { ...character, voice: '克制、精确，以短句下判断', contrast_details: [{ surface: '冷静', depth: '害怕再次失去同伴' }] } : character)
			};
			characterRun = { run_id: 'character-analyze-browser', mode: 'analyze', status: 'running', stage: 'running', requested_character_ids: body.scope?.character_ids || [], instruction: body.instruction || '', allow_supporting_characters: body.allow_supporting_characters, input_candidate_digest: body.candidate_digest, attempt: 1, model_route: 'character_analysis', created_at: '2026-07-27T08:00:00Z', updated_at: '2026-07-27T08:00:01Z' };
			characterPolls = 0;
			if (foundationScenario === 'character-error') characterRun = { ...characterRun, status: 'failed', stage: 'failed', error: { class: 'character_agent_failed', message: '安全的 Character Agent 测试失败' } };
			return json(response, 202, { run_id: characterRun.run_id, ...characterWorkspaceState(projectID) });
		}
		if (action === 'review') {
			characterCandidate = body.candidate;
			characterFindings = [{ id: 'finding-voice', scope: 'character', character_id: 'hero', location: 'voice', severity: 'warning', issue_type: 'voice', description: '语言风格仍不够可执行', evidence_summary: '当前只有抽象形容词。', suggestion: '补充句式和禁用表达。', blocking: false }];
			characterRun = { run_id: 'character-review-browser', mode: 'review', status: 'running', stage: 'running', input_candidate_digest: body.candidate_digest, attempt: 1, model_route: 'character_review', created_at: '2026-07-27T08:01:00Z', updated_at: '2026-07-27T08:01:01Z' };
			characterPolls = 0;
			return json(response, 202, { run_id: characterRun.run_id, ...characterWorkspaceState(projectID) });
		}
		if (action === 'retry') {
			characterRun = { ...(characterRun || {}), status: 'running', stage: 'running', attempt: Number(characterRun?.attempt || 1) + 1 };
			characterPolls = 0;
			return json(response, 202, characterWorkspaceState(projectID));
		}
		if (action === 'discard') {
			characterCandidate = null; characterRun = { ...(characterRun || {}), status: 'discarded', stage: 'discarded' };
			return json(response, 200, characterWorkspaceState(projectID));
		}
	}
	const foundationMatch = path.match(/^\/api\/projects\/(foundation-project-[ab])\/foundation(?:\/(preview|apply|retry))?$/);
	if (foundationMatch) {
		const projectID = foundationMatch[1], action = foundationMatch[2] || 'get';
		if (action === 'get') {
			const body = foundationState(projectID);
			if (projectID.endsWith('-a') && delayFoundationProjectA) { delayFoundationProjectA = false; setTimeout(() => json(response, 200, body), 450); return; }
			return json(response, 200, body);
		}
		const body = await readJSON(request); foundationLastRequest = { action, body, project_id: projectID };
		if (action === 'preview') {
			if (foundationScenario === 'stale') { foundationScenario = 'normal'; foundationRevision += 1; return json(response, 409, { error: { code: 'foundation_stale', message: '服务器 Foundation 已变化' } }); }
			const coreChanged = !body.candidate?.characters?.some((character) => character.id === 'hero' && character.name === '林舟');
			return json(response, 200, { preview: { version: 1, id: 'preview-browser', project_mode: foundationScenario === 'adaptation' ? 'adaptation' : 'normal', base_revision: foundationRevision, candidate: body.candidate, candidate_signature: 'candidate-signature', diff: { changes: [{ entity_type: 'premise', entity_id: 'premise', kind: 'modified', changed_fields: ['premise'], high_risk: true }, ...(coreChanged ? [{ entity_type: 'character', entity_id: 'hero', kind: 'removed', core_cast_affected: true, high_risk: true }] : [])], core_cast_reconfirmation: coreChanged }, impact: { evidence_level: 'structured', full_book: coreChanged, affected_volume_ids: ['volume-1'], affected_arc_ids: ['arc-1'], affected_chapter_ids: ['chapter-1'], reasons: [{ code: coreChanged ? 'core_cast_changed' : 'structured_dependency', required: true, entity_ids: ['premise:premise'] }], required_audits: [{ scope: 'chapter', scope_id: 'chapter-1', from_chapter: 1, to_chapter: 1, required: true }], requires_core_cast_confirmation: coreChanged, requires_foundation_confirmation: true, ...(foundationScenario === 'adaptation' ? { adaptation: { evidence_level: 'structured', source_anchor_ids: ['source-1'], contract_ids: ['contract-1'], expansion_reasons: [], requires_core_cast_reconfirmation: coreChanged, requires_adaptation_plan_confirmation: true, source_fidelity_review: true, target_consistency_review: true, character_mapping_review: true, plan_contract_review: true, outline_quality_review: true, affected_proposal: true, affected_outline: true } } : {}) }, validation: { valid: true, errors: [], warnings: [] }, can_apply: !coreChanged } });
		}
		if (action === 'apply') return json(response, 200, { revision: { revision_id: 'revision-applied', session_id: 'revision-applied', preview_id: body.preview_id, stage: 'awaiting_outline_approval', attempt: 1, generation: 10, updated_at: '2026-07-19T00:00:01Z' } });
		if (action === 'retry') return json(response, 200, { revision: { revision_id: 'revision-failed', session_id: 'revision-failed', preview_id: 'preview-browser', stage: 'regenerating', attempt: 2, generation: 8, updated_at: '2026-07-19T00:00:02Z' } });
	}
	if (path === '/api/test/refresh-expansion-metadata' && request.method === 'POST') { expansionMetadata = await fetch('http://127.0.0.1:4182/api/test/expansion-metadata').then((result) => result.json()); generation += 1; return json(response, 200, expansionMetadata); }
	if (path === '/api/test/phase-complete' && request.method === 'POST') { manuscriptPhase = 'complete'; generation += 1; return json(response, 200, { phase: manuscriptPhase }); }
	if (path === '/api/test/tombstone-history' && request.method === 'POST') { historyTombstoned = true; return json(response, 200, { tombstoned: true }); }
  if (path === '/api/test/delay-next-history' && request.method === 'POST') { delayNextHistory = true; return json(response, 200, { delayed: true }); }
  if (path === '/api/test/delay-next-tree' && request.method === 'POST') { delayNextTree = true; failDelayedTree = url.searchParams.get('fail') === '1'; return json(response, 200, { delayed: true, fail: failDelayedTree }); }
  if (path === '/api/test/delay-next-chapter' && request.method === 'POST') { delayNextChapter = true; return json(response, 200, { delayed: true }); }
  if (path.endsWith('/events')) {
    response.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache', connection: 'keep-alive' });
    response.write(': connected\n\n'); streams.add(response);
    request.on('close', () => streams.delete(response)); return;
  }
  if (path === '/api/projects/browser-project/manuscript/revision/command' && request.method === 'POST') {
    generation += 1;
		const event = manuscriptMutationWebEvent({ scope: 'prose_publish', stable_id: chapterId }, { seq: generation });
		for (const stream of streams) stream.write(`event: action\ndata: ${JSON.stringify(event)}\n\n`);
    return json(response, 200, { generation });
  }
  if (path === '/api/test/emit-mutation' && request.method === 'POST') {
    const event = manuscriptMutationWebEvent({ scope: 'prose_publish', stable_id: chapterId }, { seq: generation + 1 });
    for (const stream of streams) stream.write(`event: action\ndata: ${JSON.stringify(event)}\n\n`);
    return json(response, 200, { emitted: true });
  }
  if (path === '/api/test/fail-next-chapter' && request.method === 'POST') { failNextChapter = true; return json(response, 200, { armed: true }); }
  if (path.endsWith('/manuscript/workspace/tree')) {
    if (delayNextTree) {
      const shouldFail = failDelayedTree;
      delayNextTree = false; failDelayedTree = false;
      setTimeout(() => shouldFail ? json(response, 503, { error: { code: 'temporary_failure', message: 'STALE_TREE_ERROR' } }) : json(response, 200, treeWithLargeCatalog(), { etag: `"tree-${generation}"` }), 350);
      return;
    }
    return json(response, 200, treeWithLargeCatalog(), { etag: `"tree-${generation}"` });
  }
  if (path.includes('/manuscript/workspace/chapters/')) {
    if (failNextChapter) { failNextChapter = false; return json(response, 503, { error: { code: 'temporary_failure', message: 'temporary backend failure' } }); }
    const stableId = decodeURIComponent(path.split('/chapters/')[1]?.split('/')[0] || chapterId);
    const body = manuscriptChapter(url, stableId);
    if (delayNextChapter) { delayNextChapter = false; setTimeout(() => json(response, 200, body, { etag: `"prose-${generation}"` }), 650); return; }
    return body ? json(response, 200, body, { etag: `"prose-${generation}"` }) : json(response, 409, { error: { code: 'preview_stale', message: 'candidate version changed' } });
  }
  if (path.includes('/artifacts/outline/')) return json(response, 200, { artifact: { kind: 'outline', stable_id: chapterId, signature: 'outline-signature', content: { title: '真实后端长章', core_event: '完成真实验收', hook: '发布刷新', scenes: ['真实 API'] } } });
  if (path.includes('/artifacts/volume/')) return json(response, 200, { artifact: { kind: 'volume', stable_id: 'vol_0123456789abcdef0123456789abcdef', signature: 'volume-signature', content: { title: '第一卷', theme: '可靠性', arcs: [{ id: 'arc_0123456789abcdef0123456789abcdef', title: '第一故事弧', goal: '验证真实链路' }] } } });
  if (path.includes(`/artifacts/review/${chapterId}/${historyId}`) || path.includes(`/artifacts/review/${chapterId}/${deepReviewId}`)) return json(response, 200, { artifact: { kind: 'review_detail', stable_id: chapterId, signature: 'review-detail-signature', content: { report: path.includes(deepReviewId) ? '第 101+ 条真实审核详情' : '真实延迟加载审核报告', findings: [] } } });
  if (path.includes('/artifacts/review/')) {
		const cursor = Number(url.searchParams.get('cursor') || 0);
		const revisions = Array.from({ length: Math.min(20, 126 - cursor) }, (_, index) => ({ revision_id: cursor + index === 105 ? deepReviewId : `rev_${(cursor + index + 300).toString(16).padStart(32, '0')}` }));
		const audits = cursor === 0 ? [{ revision_id: historyId, signature: 'audit-signature', content_loaded: false }] : cursor === 100 ? [{ revision_id: deepReviewId, signature: 'deep-audit-signature', content_loaded: false }] : [];
		return json(response, 200, { artifact: { kind: 'review', stable_id: chapterId, signature: `review-signature-${cursor}`, content: { status: 'audit_pending', revisions, audits, next_cursor: Math.min(126, cursor + revisions.length), has_more: cursor + revisions.length < 126 } } });
	}
  if (path.endsWith('/manuscript/workspace/history')) {
    const cursor = Number(url.searchParams.get('cursor') || 0);
    const body = cursor === 0 ? { items: [{ revision_id: historyId, updated_at: '2026-07-16', stage: 'completed' }], next_cursor: 1, has_more: true } : { items: [{ revision_id: 'rev_22222222222222222222222222222222', updated_at: '2026-07-15', stage: 'completed' }], next_cursor: 0, has_more: false };
    if (delayNextHistory) { delayNextHistory = false; setTimeout(() => json(response, 200, body), 350); return; }
    return json(response, 200, body);
  }
  if (path.includes(`/manuscript/workspace/versions/${historyId}`)) {
		if (historyTombstoned) return json(response, 410, { error: { code: 'version_gone', message: 'historical version is no longer available', action: 'reload_history' } });
    const cursor = Number(url.searchParams.get('cursor') || 0), limit = Number(url.searchParams.get('limit') || 40);
    const historyParagraphs = paragraphs.map((paragraph, index) => `历史正式正文${index + 1}：${paragraph}`);
    return json(response, 200, { chapter: { stable_id: chapterId, view: 'history', version_id: historyId, content_signature: 'history-signature', paragraphs: historyParagraphs.slice(cursor, cursor + limit), next_cursor: cursor + limit < historyParagraphs.length ? cursor + limit : null, total_paragraphs: historyParagraphs.length } });
  }
  if (path.endsWith('/manuscript/workspace/restore/preview') && request.method === 'POST') {
    let raw = ''; request.on('data', (chunk) => { raw += chunk; }); request.on('end', () => {
      const incoming = JSON.parse(raw || '{}');
      if (incoming.revision_id !== historyId || incoming.chapter_id !== chapterId || incoming.expected_content_signature !== 'history-signature') return json(response, 410, { error: { code: 'version_gone', message: 'historical version is unavailable' } });
      json(response, 200, { preview: { source_revision_id: historyId, chapter_id: chapterId, impact: '创建新的 audit_pending 修订；不覆盖当前正式稿', requires_confirmation: true, preview_signature: 'restore-preview-signature' } });
    }); return;
  }
  if (path.endsWith('/manuscript/workspace/restore') && request.method === 'POST') {
    let raw = ''; request.on('data', (chunk) => { raw += chunk; }); request.on('end', () => {
      const incoming = JSON.parse(raw || '{}');
      if (incoming.preview_signature !== 'restore-preview-signature') return json(response, 409, { error: { code: 'preview_stale', message: 'restore preview does not match' } });
      json(response, 202, { revision: { revision_id: 'rev_restored', stage: 'audit_pending', baseline: { chapter_id: chapterId } } });
    }); return;
  }
  if (path.endsWith('/manuscript/actions/dialogues/active') && request.method === 'GET') return json(response, 200, { dialogue: manuscriptActionDialogue });
  if (path.endsWith('/manuscript/actions/dialogues') && request.method === 'POST') {
    let raw = ''; request.on('data', (chunk) => { raw += chunk; }); request.on('end', () => {
      const incoming = JSON.parse(raw || '{}');
      if (!incoming.chapter_id || !incoming.content_signature || incoming.prose) return json(response, 400, { error: { code: 'invalid_request', message: 'identifier-only boundary required' } });
      manuscriptActionDialogue = incoming.type === 'expand'
        ? { id: 'mad_browser', type: incoming.type, status: 'ready', chapter_id: incoming.chapter_id, original_chapter_label: '第 1 章', version: 2, round: 1, initial_input: incoming.initial_input, resolved_instruction: incoming.initial_input, expansion: incoming.expansion, messages: [{ role: 'user', content: incoming.initial_input }, { role: 'assistant', content: '扩写要求已明确。' }], questions: [] }
        : { id: 'mad_browser', type: incoming.type, status: 'needs_input', chapter_id: incoming.chapter_id, original_chapter_label: '第 1 章', version: 2, round: 1, initial_input: incoming.initial_input, messages: [{ role: 'user', content: incoming.initial_input }, { role: 'assistant', content: '修改范围会实质影响剧情。' }, { role: 'assistant', question_id: 'r1-scope', content: '只修改冲突场景，还是整章？' }], questions: [{ id: 'r1-scope', prompt: '只修改冲突场景，还是整章？' }] };
      json(response, 201, { dialogue: manuscriptActionDialogue });
    }); return;
  }
  if (path.endsWith('/manuscript/actions/dialogues/mad_browser/reply') && request.method === 'POST') {
    let raw = ''; request.on('data', (chunk) => { raw += chunk; }); request.on('end', () => {
      const incoming = JSON.parse(raw || '{}');
      manuscriptActionDialogue = { ...manuscriptActionDialogue, status: 'ready', version: 3, questions: [], resolved_instruction: `只修改冲突场景：${incoming.answer}`, messages: [...manuscriptActionDialogue.messages, { role: 'user', question_id: incoming.question_id, content: incoming.answer }, { role: 'assistant', content: '范围已确认。' }] };
      json(response, 200, { dialogue: manuscriptActionDialogue });
    }); return;
  }
  if (path.endsWith('/manuscript/actions/dialogues/mad_browser/cancel') && request.method === 'POST') {
    manuscriptActionDialogue = { ...manuscriptActionDialogue, status: 'cancelled', version: (manuscriptActionDialogue?.version || 0) + 1 };
    return json(response, 200, { dialogue: manuscriptActionDialogue });
  }
  if (path.endsWith('/manuscript/actions/dialogues/mad_browser/execute') && request.method === 'POST') {
    let raw = ''; request.on('data', (chunk) => { raw += chunk; }); request.on('end', async () => {
      const incoming = JSON.parse(raw || '{}');
      if (manuscriptActionDialogue?.type !== 'expand') return json(response, 409, { error: { code: 'invalid_state', message: 'fixture only executes expansion dialogues' } });
      const expansion = manuscriptActionDialogue.expansion || {};
      const planned = await fetch('http://127.0.0.1:4182/api/projects/browser-project/manuscript/expansion/plan', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ location: expansion.location, reference_ids: expansion.reference_ids || [], sentence: manuscriptActionDialogue.resolved_instruction, adjustment: expansion.adjustment || 'default', expected_structure_revision: expansion.expected_structure_revision, expected_structure_signature: expansion.expected_structure_signature, idempotency_key: incoming.idempotency_key }) });
      const body = await planned.json();
      if (!planned.ok) return json(response, planned.status, body);
      manuscriptActionDialogue = { ...manuscriptActionDialogue, status: 'completed', version: manuscriptActionDialogue.version + 2, result: { kind: 'expansion', preview: body.preview, awaiting_human_confirmation: true } };
      json(response, 202, { dialogue: manuscriptActionDialogue });
    }); return;
  }
  json(response, 404, { error: { code: 'not_found', message: path } });
});

server.on('clientError', (_error, socket) => socket.destroy());

server.listen(port, '127.0.0.1');
const shutdown = () => server.close(() => process.exit(0));
process.on('SIGTERM', shutdown);
process.on('SIGINT', shutdown);
