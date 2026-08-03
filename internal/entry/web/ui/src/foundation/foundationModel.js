const relationshipTypes = ['ally', 'rival', 'family', 'romantic', 'mentor', 'professional', 'other'];
const relationshipDirections = ['directed', 'bidirectional', 'undirected'];
const relationshipStatuses = ['planned', 'active', 'strained', 'broken', 'resolved'];
const ruleStrengths = ['hard', 'soft'];
const characterTiers = ['core', 'important', 'secondary', 'decorative'];
const characterGenders = ['male', 'female', 'nonbinary', 'unspecified'];
const characterReviewStatuses = ['passed', 'needs_revision', 'stale', 'not_reviewed'];
const sourceMappingActions = ['keep', 'rename', 'merge', 'split', 'exclude', 'target_original'];

export const foundationOptions = {
  relationshipTypes,
  relationshipDirections,
  relationshipStatuses,
  ruleStrengths,
  characterTiers,
  characterGenders,
  characterReviewStatuses,
  sourceMappingActions
};

const readonlyReasonLabels = {
  progress_unavailable: '无法读取当前创作进度',
  project_complete: '项目已完成，正式设定已封存',
  body_started: '正文已经开始，需通过修订流程修改正式设定',
  planning_stage_not_editable: '当前处于设定生成或审核阶段，请在中央审核区确认或提交修改意见',
  active_foundation_revision: '设定修订正在执行，请等待本轮完成',
  revision_state_unavailable: '无法读取设定修订状态',
  active_revision: '已有修订任务正在执行',
  adaptation_baseline_unavailable: '改编设定基线尚未就绪',
  adaptation_workflow_not_safely_paused: '改编流程尚未停在可安全编辑的阶段',
  adaptation_foundation_confirmation_invalid: '目标改编设定尚未完成确认',
  adaptation_foundation_source_binding_stale: '原著依据或改编意图已经变化，需要重新生成目标设定',
  foundation_confirmation_invalid: 'StoryFoundation 尚未完成确认',
  adaptation_source_inconsistent: '原著依据不完整或已变化',
  adaptation_core_cast_inconsistent: '改编核心角色契约不一致',
  adaptation_intent_unavailable: '改编意图尚未就绪',
  adaptation_workflow_unavailable: '改编工作流尚未就绪',
  adaptation_plan_unavailable: '改编方案尚未生成完成；请先在中央审核区确认当前设定'
};

export function foundationReadonlyReasonLabel(reason) {
  const code = String(reason || '').trim();
  return readonlyReasonLabels[code] || code || '当前阶段不允许手工编辑';
}

export const characterFieldGroups = [
  { id: 'identity', label: '身份', fields: ['name', 'aliases', 'role', 'gender', 'tier', 'faction'] },
  { id: 'core', label: '人物核心', fields: ['description', 'traits', 'contrast_details', 'key_backstory'] },
  { id: 'drive', label: '驱动与弧', fields: ['goal', 'motivation', 'conflict', 'arc'] },
  { id: 'performance', label: '表演约束', fields: ['voice', 'constraints', 'knowledge_boundary'] },
  { id: 'initial', label: '初始状态与备注', fields: ['initial_state', 'notes'] }
];

export function cloneFoundation(value) {
  return JSON.parse(JSON.stringify(value || {}));
}

export function normalizeFoundation(value = {}) {
  return {
    schema_version: Number(value.schema_version || 1),
    revision: Number(value.revision || 0),
    premise: String(value.premise || ''),
    characters: array(value.characters).map(normalizeCharacter),
    relationships: array(value.relationships).map(normalizeRelationship),
    relationships_reviewed: Boolean(value.relationships_reviewed),
    world_rules: array(value.world_rules).map(normalizeWorldRule),
    ...(value.updated_at ? { updated_at: String(value.updated_at) } : {})
  };
}

export function normalizeFoundationResponse(response) {
  const state = response?.foundation || {};
  return {
    project: response?.project || null,
    mode: state.mode === 'adaptation' ? 'adaptation' : 'normal',
    sourceFoundation: state.source_foundation || null,
    targetFoundation: normalizeFoundation(state.target_foundation),
    editable: Boolean(state.editable),
    readonlyReason: String(state.readonly_reason || ''),
    baseRevision: Number(state.base_revision || 0),
    baseAuditSignature: String(state.base_audit_signature || ''),
    coreCastSignature: String(state.core_cast_signature || ''),
    coreCast: state.core_cast || null,
    coreCastCompletion: state.core_cast_completion || null,
    coreCastConfirmed: Boolean(state.core_cast_confirmed),
    modeSpecific: state.mode_specific || null,
    modeSpecificError: String(state.mode_specific_error || ''),
    activeRevision: state.active_revision || null,
    planningReview: state.planning_review || null,
    allowedOperations: array(state.allowed_operations).map(String)
  };
}

export function normalizeCharacter(value = {}) {
  return {
    id: String(value.id || ''),
    name: String(value.name || ''),
    aliases: uniqueStrings(value.aliases),
    role: String(value.role || ''),
    gender: characterGenders.includes(value.gender) ? value.gender : 'unspecified',
    description: String(value.description || ''),
    arc: String(value.arc || ''),
    traits: uniqueStrings(value.traits),
    tier: String(value.tier || ''),
    faction: String(value.faction || ''),
    goal: String(value.goal || ''),
    motivation: String(value.motivation || ''),
    conflict: String(value.conflict || ''),
    voice: String(value.voice || ''),
    constraints: uniqueStrings(value.constraints),
    contrast_details: array(value.contrast_details).map((item) => ({
      surface: String(item?.surface || ''),
      depth: String(item?.depth || '')
    })).filter((item) => item.surface || item.depth),
    key_backstory: array(value.key_backstory).map((item) => ({
      event: String(item?.event || ''),
      impact: String(item?.impact || '')
    })).filter((item) => item.event || item.impact),
    initial_state: normalizeInitialState(value.initial_state),
    knowledge_boundary: normalizeKnowledgeBoundary(value.knowledge_boundary),
    notes: String(value.notes || '')
  };
}

export function normalizeCharacterWorkspace(response = {}) {
  const workspace = response.character_workspace || response;
  const completeness = array(workspace.completeness).map((item) => ({
    character_id: String(item?.character_id || ''),
    tier: characterTiers.includes(item?.tier) ? item.tier : 'important',
    status: item?.status === 'complete' ? 'complete' : 'incomplete',
    missing: array(item?.missing).map((missing) => ({
      code: String(missing?.code || ''),
      field: String(missing?.field || ''),
      severity: normalizeSeverity(missing?.severity),
      description: String(missing?.description || '')
    }))
  }));
  return {
    mode: workspace.mode === 'adaptation' ? 'adaptation' : 'original',
    baseRevision: Number(workspace.base_revision || 0),
    baseAuditSignature: String(workspace.base_audit_signature || ''),
    currentDigest: String(workspace.current_digest || ''),
    current: normalizeCharacterCandidate(workspace.current),
    candidate: workspace.candidate ? normalizeCharacterCandidate(workspace.candidate) : null,
    run: workspace.run ? normalizeCharacterRun(workspace.run) : null,
    completeness,
    completenessByID: Object.fromEntries(completeness.map((item) => [item.character_id, item])),
    coverage: normalizeSourceCoverage(workspace.source_coverage),
    sourceMappings: array(workspace.source_mappings).map(normalizeSourceMapping),
    findings: array(workspace.findings).map(normalizeFinding),
    diff: normalizeCharacterDiff(workspace.diff),
    allowedOperations: array(workspace.allowed_operations).map(String),
    confirmationStatus: String(workspace.confirmation_status || ''),
    staleReason: String(workspace.stale_reason || ''),
    readonlyReason: String(workspace.readonly_reason || ''),
    busyReason: String(workspace.busy_reason || ''),
    error: workspace.error ? {
      code: String(workspace.error.class || 'character_workspace_error'),
      message: String(workspace.error.message || '角色工作区请求失败')
    } : null
  };
}

export function characterFieldDiff(current, candidate) {
  const left = normalizeCharacter(current);
  const right = normalizeCharacter(candidate);
  return characterFieldGroups.flatMap((group) => group.fields
    .filter((field) => JSON.stringify(left[field]) !== JSON.stringify(right[field]))
    .map((field) => ({
      field,
      group: group.id,
      before: cloneFoundation(left[field]),
      after: cloneFoundation(right[field])
    })));
}

export function mergeCharacterField(characters, candidate, field) {
  return array(characters).map((character) => character.id === candidate?.id
    ? normalizeCharacter({ ...character, [field]: cloneFoundation(candidate[field]) })
    : character);
}

export function mergeCharacterCandidate(characters, candidate) {
  const normalized = normalizeCharacter(candidate);
  const found = array(characters).some((character) => character.id === normalized.id);
  return found
    ? array(characters).map((character) => character.id === normalized.id ? normalized : character)
    : [...array(characters), normalized];
}

export function acceptAllCharacterCandidates(characters, candidateFoundation) {
  const candidates = new Map(array(candidateFoundation?.characters).map((character) => {
    const normalized = normalizeCharacter(character);
    return [normalized.id, normalized];
  }));
  const merged = array(characters).map((character) => candidates.get(character.id) || character);
  for (const [id, candidate] of candidates) {
    if (!merged.some((character) => character.id === id)) merged.push(candidate);
  }
  return merged;
}

export function duplicateFoundationCharacter(character) {
  return normalizeCharacter({
    ...cloneFoundation(character),
    id: clientID('character'),
    name: `${String(character?.name || '未命名角色')}（副本）`
  });
}

export function filterAndSortCharacters(characters, options = {}) {
  const query = String(options.query || '').trim().toLocaleLowerCase();
  const tier = String(options.tier || 'all');
  const core = String(options.core || 'all');
  const completeness = String(options.completeness || 'all');
  const review = String(options.review || 'all');
  const source = String(options.source || 'all');
  const coreIDs = options.coreIDs instanceof Set ? options.coreIDs : new Set();
  const completenessByID = options.completenessByID || {};
  const findings = array(options.findings);
  const mappingByTargetID = options.mappingByTargetID || {};
  const reviewByID = (id) => reviewStatusForCharacter(id, findings, options.reviewStale, options.reviewCompleted);
  const matches = array(characters).filter((character) => {
    const normalized = normalizeCharacter(character);
    const searchable = [normalized.name, ...normalized.aliases, normalized.role, normalized.faction].join('\n').toLocaleLowerCase();
    const isCore = coreIDs.has(normalized.id) || normalized.tier === 'core';
    const complete = completenessByID[normalized.id]?.status === 'complete';
    const sourceAction = mappingByTargetID[normalized.id]?.action || 'unmapped';
    return (!query || searchable.includes(query)) &&
      (tier === 'all' || normalized.tier === tier) &&
      (core === 'all' || (core === 'core') === isCore) &&
      (completeness === 'all' || (completeness === 'complete') === complete) &&
      (review === 'all' || reviewByID(normalized.id) === review) &&
      (source === 'all' || sourceAction === source);
  });
  const sort = String(options.sort || 'core');
  const modifiedByID = options.modifiedByID || {};
  const tierRank = { core: 0, important: 1, secondary: 2, decorative: 3 };
  return matches.sort((left, right) => {
    if (sort === 'name') return left.name.localeCompare(right.name, 'zh-CN');
    if (sort === 'recent') return Number(modifiedByID[right.id] || 0) - Number(modifiedByID[left.id] || 0) || left.name.localeCompare(right.name, 'zh-CN');
    if (sort === 'gaps') {
      const gap = (completenessByID[right.id]?.missing?.length || 0) - (completenessByID[left.id]?.missing?.length || 0);
      if (gap) return gap;
    }
    if (sort === 'tier') {
      const rank = (tierRank[left.tier] ?? 9) - (tierRank[right.tier] ?? 9);
      if (rank) return rank;
    }
    const coreRank = Number(!(coreIDs.has(left.id) || left.tier === 'core')) - Number(!(coreIDs.has(right.id) || right.tier === 'core'));
    if (coreRank) return coreRank;
    return (tierRank[left.tier] ?? 9) - (tierRank[right.tier] ?? 9) || left.name.localeCompare(right.name, 'zh-CN');
  });
}

export function sourceMappingByTargetID(mappings) {
  const result = {};
  for (const mapping of array(mappings)) {
    for (const id of array(mapping.target_character_ids)) result[id] = mapping;
  }
  return result;
}

export function reviewStatusForCharacter(characterID, findings, stale = false, reviewed = false) {
  if (stale) return 'stale';
  const scoped = array(findings).filter((finding) => finding.character_id === characterID);
  if (!scoped.length) return reviewed ? 'passed' : 'not_reviewed';
  const hasBlockingFinding = scoped.some((finding) => finding.blocking || finding.severity === 'blocking');
  return hasBlockingFinding ? 'needs_revision' : reviewed ? 'passed' : 'not_reviewed';
}

export function normalizeRelationship(value = {}) {
  return {
    id: String(value.id || ''),
    source_character_id: String(value.source_character_id || ''),
    target_character_id: String(value.target_character_id || ''),
    type: relationshipTypes.includes(value.type) ? value.type : 'other',
    label: String(value.label || ''),
		direction: normalizeRelationshipDirection(value.direction),
    status: relationshipStatuses.includes(value.status) ? value.status : 'planned',
    description: String(value.description || ''),
    since: String(value.since || ''),
    tags: uniqueStrings(value.tags),
    constraints: uniqueStrings(value.constraints)
  };
}

export function normalizeWorldRule(value = {}) {
  const id = String(value.id || '');
  const legacyStrength = id.toLowerCase().startsWith('sr_')
    ? 'soft'
    : id.toLowerCase().startsWith('hr_')
      ? 'hard'
      : '';
  return {
    id,
    title: String(value.title || ''),
    category: String(value.category || 'other'),
    rule: String(value.rule || ''),
    boundary: String(value.boundary || ''),
    strength: legacyStrength || (ruleStrengths.includes(value.strength) ? value.strength : 'hard'),
    priority: Number.isFinite(Number(value.priority)) ? Number(value.priority) : 0,
    tags: uniqueStrings(value.tags)
  };
}

export function newFoundationCharacter() {
  return normalizeCharacter({ id: clientID('character') });
}

export function newFoundationRelationship() {
  return normalizeRelationship({ id: clientID('relationship') });
}

export function newFoundationWorldRule() {
  return normalizeWorldRule({ id: clientID('rule') });
}

export function candidateFingerprint(value) {
  const foundation = normalizeFoundation(value);
  delete foundation.updated_at;
  return JSON.stringify(foundation);
}

export function validateFoundationDraft(value) {
  const foundation = normalizeFoundation(value);
  const fields = {};
  const summary = [];
  if (!foundation.premise.trim()) addError(fields, summary, 'premise', '故事前提不能为空');

  const characterIDs = new Set();
  const identities = new Map();
  foundation.characters.forEach((character, index) => {
    const prefix = `characters.${index}`;
    if (!character.id.trim()) addError(fields, summary, `${prefix}.id`, `角色 ${index + 1} 缺少稳定 ID`);
    if (!character.name.trim()) addError(fields, summary, `${prefix}.name`, `角色 ${index + 1} 姓名不能为空`);
    if (character.tier !== 'decorative' && character.gender === 'unspecified') addError(fields, summary, `${prefix}.gender`, `角色 ${index + 1} 需要明确性别；若确实不设定，请将其作为装饰角色或在角色约束中固定使用姓名/称谓`);
    if (characterIDs.has(character.id)) addError(fields, summary, `${prefix}.id`, `角色 ID ${character.id} 重复`);
    characterIDs.add(character.id);
    for (const label of [character.name, ...character.aliases]) {
      const identity = label.trim().toLocaleLowerCase();
      if (!identity) continue;
      const owner = identities.get(identity);
      if (owner && owner !== character.id) addError(fields, summary, `${prefix}.aliases`, `姓名或别名 ${label} 与其他角色冲突`);
      identities.set(identity, character.id);
    }
  });
  if (!foundation.characters.length) addError(fields, summary, 'characters', '至少需要一个角色');

  const relationIDs = new Set();
  foundation.relationships.forEach((relationship, index) => {
    const prefix = `relationships.${index}`;
    if (relationIDs.has(relationship.id)) addError(fields, summary, `${prefix}.id`, `关系 ID ${relationship.id} 重复`);
    relationIDs.add(relationship.id);
    if (!characterIDs.has(relationship.source_character_id)) addError(fields, summary, `${prefix}.source_character_id`, `关系 ${index + 1} 的起点角色已悬空`);
    if (!characterIDs.has(relationship.target_character_id)) addError(fields, summary, `${prefix}.target_character_id`, `关系 ${index + 1} 的终点角色已悬空`);
    if (relationship.source_character_id && relationship.source_character_id === relationship.target_character_id) addError(fields, summary, `${prefix}.target_character_id`, `关系 ${index + 1} 不能指向同一角色`);
  });

  const ruleIDs = new Set();
  foundation.world_rules.forEach((rule, index) => {
    const prefix = `world_rules.${index}`;
    if (!rule.rule.trim()) addError(fields, summary, `${prefix}.rule`, `世界规则 ${index + 1} 的规则正文不能为空`);
    if (ruleIDs.has(rule.id)) addError(fields, summary, `${prefix}.id`, `世界规则 ID ${rule.id} 重复`);
    ruleIDs.add(rule.id);
  });
  if (!foundation.world_rules.length) addError(fields, summary, 'world_rules', '至少需要一条世界规则');
  return { valid: summary.length === 0, fields, summary };
}

export function sourceMajorCharacters(sourceFoundation) {
  return array(sourceFoundation?.characters).map((character) => ({
    id: String(character?.id || ''),
    name: String(character?.name || ''),
    aliases: uniqueStrings(character?.aliases),
    role: String(character?.role || ''),
    gender: characterGenders.includes(character?.gender) ? character.gender : 'unspecified',
    description: String(character?.description || ''),
    arc: String(character?.arc || ''),
    traits: uniqueStrings(character?.traits),
    goal: String(character?.goal || ''),
    motivation: String(character?.motivation || ''),
    conflict: String(character?.conflict || ''),
    voice: String(character?.voice || ''),
    constraints: uniqueStrings(character?.constraints),
    faction: String(character?.faction || ''),
    notes: String(character?.notes || '')
  })).filter((character) => character.id || character.name);
}

export function shortSignature(value) {
  const text = String(value || '');
  return text ? text.slice(0, 10) : '—';
}

export function isCoreCharacter(character, coreCast) {
  const coreIDs = new Set(array(coreCast?.members).map((member) => String(member?.character?.id || '')));
  return coreIDs.has(String(character?.id || ''));
}

function uniqueStrings(value) {
  const seen = new Set();
  return array(value).map((item) => String(item || '').trim()).filter((item) => {
    const key = item.toLocaleLowerCase();
    if (!item || seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function normalizeInitialState(value) {
  if (!value || typeof value !== 'object') return null;
  const result = {
    identity: String(value.identity || ''),
    situation: String(value.situation || ''),
    emotion: String(value.emotion || ''),
    resources: uniqueStrings(value.resources),
    relationships: String(value.relationships || '')
  };
  return Object.values(result).some((item) => Array.isArray(item) ? item.length : item) ? result : null;
}

function normalizeKnowledgeBoundary(value) {
  if (!value || typeof value !== 'object') return null;
  const result = {
    known: uniqueStrings(value.known),
    unknown: uniqueStrings(value.unknown),
    misconceptions: uniqueStrings(value.misconceptions),
    forbidden: uniqueStrings(value.forbidden)
  };
  return Object.values(result).some((item) => item.length) ? result : null;
}

function normalizeCharacterCandidate(value = {}) {
  return {
    revision: Number(value.revision || 0),
    digest: String(value.digest || ''),
    foundation: normalizeFoundation(value.foundation)
  };
}

function normalizeCharacterRun(value = {}) {
  return {
    run_id: String(value.run_id || ''),
    mode: value.mode === 'review' ? 'review' : 'analyze',
    status: String(value.status || ''),
    stage: String(value.stage || ''),
    requested_character_ids: array(value.requested_character_ids).map(String),
    instruction: String(value.instruction || ''),
    allow_supporting_characters: Boolean(value.allow_supporting_characters),
    input_candidate_digest: String(value.input_candidate_digest || ''),
    attempt: Number(value.attempt || 0),
    model_route: String(value.model_route || ''),
    error: value.error ? { code: String(value.error.class || ''), message: String(value.error.message || '') } : null,
    created_at: String(value.created_at || ''),
    updated_at: String(value.updated_at || ''),
    finished_at: String(value.finished_at || ''),
    duration_ms: Number(value.duration_ms || 0)
  };
}

function normalizeSeverity(value) {
  return value === 'blocking' ? 'blocking' : value === 'information' ? 'information' : 'warning';
}

function normalizeFinding(value = {}) {
  return {
    id: String(value.id || ''),
    scope: value.scope === 'character' ? 'character' : 'global',
    character_id: String(value.character_id || ''),
    location: String(value.location || ''),
    severity: normalizeSeverity(value.severity),
    issue_type: String(value.issue_type || ''),
    description: String(value.description || ''),
    evidence_summary: String(value.evidence_summary || ''),
    suggestion: String(value.suggestion || ''),
    blocking: Boolean(value.blocking)
  };
}

function normalizeSourceMapping(value = {}) {
  return {
    id: String(value.id || ''),
    action: sourceMappingActions.includes(value.action) ? value.action : 'target_original',
    source_character_ids: array(value.source_character_ids).map(String),
    target_character_ids: array(value.target_character_ids).map(String),
    rationale: String(value.rationale || ''),
    evidence: array(value.evidence).map((item) => ({
      kind: String(item?.kind || ''),
      reference: String(item?.reference || ''),
      summary: String(item?.summary || '')
    }))
  };
}

function normalizeSourceCoverage(value) {
  if (!value || typeof value !== 'object') return null;
  return {
    source_total: Number(value.source_total || 0),
    decision_required: Number(value.decision_required || 0),
    mapped: Number(value.mapped || 0),
    explicitly_excluded: Number(value.explicitly_excluded || 0),
    pending: Number(value.pending || 0),
    blocking_gaps: Number(value.blocking_gaps || 0),
    decisions: array(value.decisions).map((item) => ({
      source_character_id: String(item?.source_character_id || ''),
      canonical_name: String(item?.canonical_name || ''),
      suggested_tier: String(item?.suggested_tier || ''),
      decision_required: Boolean(item?.decision_required),
      reasons: array(item?.reasons).map(String),
      mapping_id: String(item?.mapping_id || ''),
      action: String(item?.action || ''),
      pending: Boolean(item?.pending),
      blocking: Boolean(item?.blocking)
    }))
  };
}

function normalizeCharacterDiff(value) {
  if (!value || typeof value !== 'object') return null;
  return {
    changes: array(value.changes).map((item) => ({
      entity_type: String(item?.entity_type || ''),
      entity_id: String(item?.entity_id || ''),
      kind: String(item?.kind || ''),
      changed_fields: array(item?.changed_fields).map(String),
      high_risk: Boolean(item?.high_risk),
      core_cast_affected: Boolean(item?.core_cast_affected)
    })),
    core_cast_reconfirmation: Boolean(value.core_cast_reconfirmation),
    foundation_reconfirmation: Boolean(value.foundation_reconfirmation),
    signature: String(value.signature || '')
  };
}

function addError(fields, summary, path, message) {
  if (!fields[path]) fields[path] = message;
  if (!summary.includes(message)) summary.push(message);
}

function clientID(kind) {
  const random = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  const prefix = { character: 'char', relationship: 'rel', rule: 'rule' }[kind] || kind;
  return `${prefix}-${random}`;
}

function array(value) {
	return Array.isArray(value) ? value : [];
}

function normalizeRelationshipDirection(value) {
	if (value === 'mutual' || !value) return 'bidirectional';
	return relationshipDirections.includes(value) ? value : 'bidirectional';
}
