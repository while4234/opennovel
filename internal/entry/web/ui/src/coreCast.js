export const coreCastImportanceOptions = [
  'protagonist', 'co_protagonist', 'major_pov', 'antagonist',
  'love_interest', 'major_support', 'user_important'
];

export const coreCastOriginOptions = ['original', 'source'];
export const sourceDispositionActions = ['keep', 'rename', 'merge', 'split', 'exclude'];
export const coreCastRelationshipTypes = ['ally', 'rival', 'family', 'romantic', 'mentor', 'professional', 'other'];
export const coreCastRelationshipDirections = ['directed', 'bidirectional', 'undirected'];
export const coreCastRelationshipStatuses = ['planned', 'active', 'strained', 'broken', 'resolved'];

export const coreCastImportanceLabels = {
  protagonist: '主角',
  co_protagonist: '联合主角',
  major_pov: '重要视角角色',
  antagonist: '核心对手',
  love_interest: '核心情感角色',
  major_support: '重要配角',
  user_important: '自定义重点角色'
};

export const coreCastImportanceHelp = {
  protagonist: '推动全书主线、承担主要成长弧。',
  co_protagonist: '与主角同等重要，共同承担主线。',
  major_pov: '拥有持续且重要的独立视角。',
  antagonist: '长期阻碍主角目标的核心对手。',
  love_interest: '承担主要情感线，不能只作为标签存在。',
  major_support: '持续参与主线的重要配角。',
  user_important: '不属于以上类型，但必须被全程重点维护。'
};

export const coreCastOriginLabels = {
  original: '原创角色',
  source: '源作角色'
};

export const sourceDispositionLabels = {
  keep: '保留',
  rename: '改名保留',
  merge: '合并',
  split: '拆分',
  exclude: '不采用'
};

export const relationshipTypeLabels = {
  ally: '盟友',
  rival: '竞争 / 对手',
  family: '家人',
  romantic: '情感关系',
  mentor: '师徒',
  professional: '事业 / 合作',
  other: '其他'
};

export const relationshipDirectionLabels = {
  directed: '单向影响',
  bidirectional: '双向关系',
  undirected: '无方向关系'
};

export const relationshipStatusLabels = {
  planned: '计划建立',
  active: '故事开始时已存在',
  strained: '关系紧张',
  broken: '关系破裂',
  resolved: '矛盾已解决'
};

export function normalizeCoreCast(value, mode = 'normal') {
  const input = value && typeof value === 'object' ? value : {};
  return {
    version: 1,
    mode: mode === 'adapt' ? 'adaptation' : (input.mode || 'normal'),
    draft_revision: Number(input.draft_revision || 0),
    draft_hash: String(input.draft_hash || ''),
    source_signature: String(input.source_signature || ''),
    adaptation_intent_hash: String(input.adaptation_intent_hash || ''),
    members: Array.isArray(input.members) ? input.members.map(normalizeCoreCastMember) : [],
    planned_relationships: Array.isArray(input.planned_relationships) ? input.planned_relationships.map(normalizeCoreCastRelationship) : [],
    source_dispositions: Array.isArray(input.source_dispositions) ? input.source_dispositions.map((item) => ({ ...item, source_character_id: String(item?.source_character_id || ''), target_character_ids: arrayOfStrings(item?.target_character_ids), rationale: String(item?.rationale || '') })) : [],
    revision: Number(input.revision || 0)
  };
}

export function normalizeCoreCastMember(value = {}) {
  const character = value.character && typeof value.character === 'object' ? value.character : {};
  return {
    character: {
      id: String(character.id || ''), name: String(character.name || ''), aliases: arrayOfStrings(character.aliases),
      role: String(character.role || ''), description: String(character.description || ''), arc: String(character.arc || ''),
      traits: arrayOfStrings(character.traits), tier: String(character.tier || ''), faction: String(character.faction || ''),
      goal: String(character.goal || ''), motivation: String(character.motivation || ''), conflict: String(character.conflict || ''),
      voice: String(character.voice || ''), constraints: arrayOfStrings(character.constraints), notes: String(character.notes || '')
    },
    importance: coreCastImportanceOptions.includes(value.importance) ? value.importance : 'major_support',
    origin: coreCastOriginOptions.includes(value.origin) ? value.origin : 'original',
    mainline_function: String(value.mainline_function || ''),
    source_character_ids: arrayOfStrings(value.source_character_ids),
    inclusion_rationale: String(value.inclusion_rationale || ''),
    no_core_relationships: Boolean(value.no_core_relationships)
  };
}

export function newCoreCastMember() {
  return normalizeCoreCastMember({});
}

export function normalizeCoreCastRelationship(value = {}) {
  return {
    id: String(value.id || ''),
    source_character_id: String(value.source_character_id || ''),
    target_character_id: String(value.target_character_id || ''),
    type: coreCastRelationshipTypes.includes(value.type) ? value.type : 'other',
    label: String(value.label || ''),
    direction: coreCastRelationshipDirections.includes(value.direction) ? value.direction : 'bidirectional',
    status: coreCastRelationshipStatuses.includes(value.status) ? value.status : 'planned',
    description: String(value.description || ''),
    since: String(value.since || ''),
    tags: arrayOfStrings(value.tags),
    constraints: arrayOfStrings(value.constraints)
  };
}

export function newCoreCastRelationship() {
  return normalizeCoreCastRelationship();
}

export function setCoreCastMemberField(contract, index, path, value) {
  const next = normalizeCoreCast(contract, contract?.mode === 'adaptation' ? 'adapt' : 'normal');
  const member = next.members[index];
  if (!member) return next;
  if (path.startsWith('character.')) member.character[path.slice('character.'.length)] = value;
  else member[path] = value;
  return next;
}

export function setCoreCastMemberSourceID(contract, index, sourceID, selected) {
  const next = normalizeCoreCast(contract, contract?.mode === 'adaptation' ? 'adapt' : 'normal');
  const member = next.members[index];
  if (!member) return next;
  const ids = new Set(member.source_character_ids);
  if (selected) ids.add(String(sourceID));
  else ids.delete(String(sourceID));
  member.source_character_ids = [...ids].filter(Boolean).sort();
  return next;
}

export function setCoreCastRelationshipField(contract, index, field, value) {
  const next = normalizeCoreCast(contract, contract?.mode === 'adaptation' ? 'adapt' : 'normal');
  const relationship = next.planned_relationships[index];
  if (!relationship) return next;
  relationship[field] = ['tags', 'constraints'].includes(field) ? arrayOfStrings(value) : value;
  next.planned_relationships[index] = normalizeCoreCastRelationship(relationship);
  return next;
}

export function setCoreCastDisposition(contract, sourceID, change) {
  const next = normalizeCoreCast(contract, 'adapt');
  const id = String(sourceID || '').trim();
  let disposition = next.source_dispositions.find((item) => item.source_character_id === id);
  if (!disposition) {
    disposition = { source_character_id: id, action: 'keep', target_character_ids: [], rationale: '' };
    next.source_dispositions.push(disposition);
  }
  Object.assign(disposition, change);
  disposition.action = sourceDispositionActions.includes(disposition.action) ? disposition.action : 'keep';
  disposition.target_character_ids = arrayOfStrings(disposition.target_character_ids);
  if (disposition.action === 'exclude') disposition.target_character_ids = [];
  next.source_dispositions.sort((left, right) => left.source_character_id.localeCompare(right.source_character_id));
  return next;
}

function arrayOfStrings(value) {
  return Array.isArray(value) ? value.map((item) => String(item || '').trim()).filter(Boolean) : [];
}
