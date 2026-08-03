export const FOUNDATION_GRAPH_LAYOUT_VERSION = 2;
export const FOUNDATION_GRAPH_LARGE_THRESHOLD = 80;

export const defaultRelationshipGraphFilters = Object.freeze({
  search: '', tier: '', faction: '', relationshipType: '', relationshipStatus: ''
});

const highImportance = new Set(['protagonist', 'co_protagonist', 'major_pov', 'antagonist', 'core', 'primary', 'major', 'main']);

export function buildRelationshipGraph({
  characters = [], relationships = [], coreCast = null, positions = {}, filters = defaultRelationshipGraphFilters,
  focusNodeID = '', showIsolated = true, showAll = false
} = {}) {
  const importanceByID = coreCastImportance(coreCast);
  const uniqueCharacters = uniqueByStableID(characters);
  const characterByID = new Map(uniqueCharacters.map((character) => [String(character.id), character]));
  const warnings = [];
  const validRelationships = [];
  const seenRelationships = new Set();

  for (const relationship of relationships) {
    const id = String(relationship?.id || '');
    if (!id || seenRelationships.has(id)) continue;
    seenRelationships.add(id);
    const source = String(relationship?.source_character_id || '');
    const target = String(relationship?.target_character_id || '');
    if (!characterByID.has(source) || !characterByID.has(target)) {
      warnings.push({ code: 'missing_endpoint', relationshipID: id, source, target });
      continue;
    }
    validRelationships.push(relationship);
  }

  const normalizedFilters = normalizeFilters(filters);
  const explicitFilter = Object.values(normalizedFilters).some(Boolean);
  let visibleCharacters = uniqueCharacters.filter((character) => matchesCharacter(character, normalizedFilters, importanceByID));
  if (uniqueCharacters.length > FOUNDATION_GRAPH_LARGE_THRESHOLD && !showAll && !explicitFilter && !focusNodeID) {
    const important = visibleCharacters.filter((character) => isHighImportance(character, importanceByID));
    visibleCharacters = important.length ? important : visibleCharacters.slice(0, FOUNDATION_GRAPH_LARGE_THRESHOLD);
  }

  const relationshipFiltered = validRelationships.filter((relationship) => matchesRelationship(relationship, normalizedFilters));
  if (focusNodeID && characterByID.has(focusNodeID)) {
    const neighborhood = new Set([focusNodeID]);
    for (const relationship of relationshipFiltered) {
      if (relationship.source_character_id === focusNodeID) neighborhood.add(relationship.target_character_id);
      if (relationship.target_character_id === focusNodeID) neighborhood.add(relationship.source_character_id);
    }
    visibleCharacters = visibleCharacters.filter((character) => neighborhood.has(character.id));
  }

  let visibleIDs = new Set(visibleCharacters.map((character) => character.id));
  let visibleRelationships = relationshipFiltered.filter((relationship) => visibleIDs.has(relationship.source_character_id) && visibleIDs.has(relationship.target_character_id));
  if (!showIsolated) {
    const connected = new Set();
    for (const relationship of visibleRelationships) {
      connected.add(relationship.source_character_id);
      connected.add(relationship.target_character_id);
    }
    visibleCharacters = visibleCharacters.filter((character) => connected.has(character.id));
    visibleIDs = connected;
    visibleRelationships = visibleRelationships.filter((relationship) => visibleIDs.has(relationship.source_character_id) && visibleIDs.has(relationship.target_character_id));
  }

  const fallbackPositions = deterministicGraphPositions(visibleCharacters);
  const nodes = visibleCharacters.map((character) => {
    const importance = importanceByID.get(character.id) || String(character.tier || '未标注');
    return {
      id: character.id,
      position: validPosition(positions[character.id]) || fallbackPositions[character.id],
      data: {
        label: character.name || character.id,
        name: character.name || character.id,
        importance,
        tier: String(character.tier || '未标注'),
        faction: String(character.faction || '未标注')
      },
      ariaLabel: `${character.name || character.id}；重要级别 ${importance}；层级 ${character.tier || '未标注'}；阵营 ${character.faction || '未标注'}`
    };
  });
  const edges = visibleRelationships.map((relationship) => relationshipGraphEdge(relationship));
  return {
    nodes, edges, warnings,
    totalNodeCount: uniqueCharacters.length,
    totalEdgeCount: validRelationships.length,
    limited: uniqueCharacters.length > FOUNDATION_GRAPH_LARGE_THRESHOLD && !showAll && !explicitFilter && !focusNodeID,
    source: 'StoryFoundation.relationships'
  };
}

export function deterministicGraphPositions(characters = []) {
  const sorted = [...characters].sort((left, right) => String(left.id).localeCompare(String(right.id)));
  const columns = Math.max(1, Math.ceil(Math.sqrt(sorted.length)));
  return Object.fromEntries(sorted.map((character, index) => [String(character.id), {
    x: (index % columns) * 430,
    y: Math.floor(index / columns) * 250
  }]));
}

export function normalizeGraphDirection(value) {
  if (value === 'mutual') return 'bidirectional';
  if (['directed', 'bidirectional', 'undirected'].includes(value)) return value;
  return 'bidirectional';
}

export function foundationGraphLayoutKey(projectID, auditSignature) {
  return `foundation-graph-layout:${String(projectID || '')}:${auditSignatureNamespace(auditSignature)}`;
}

export function loadFoundationGraphLayout(storage, projectID, auditSignature, characterIDs) {
  if (!storage || !projectID || !auditSignature) return {};
  try {
    const parsed = JSON.parse(storage.getItem(foundationGraphLayoutKey(projectID, auditSignature)) || 'null');
    if (!parsed || parsed.layout_version !== FOUNDATION_GRAPH_LAYOUT_VERSION || parsed.foundation_audit_namespace !== auditSignatureNamespace(auditSignature) || typeof parsed.node_coordinates !== 'object') return {};
    const allowed = new Set(characterIDs.map(String));
    return Object.fromEntries(Object.entries(parsed.node_coordinates).filter(([id, position]) => allowed.has(id) && validPosition(position)));
  } catch {
    return {};
  }
}

export function saveFoundationGraphLayout(storage, projectID, auditSignature, positions, now = () => new Date().toISOString()) {
  if (!storage || !projectID || !auditSignature) return false;
  try {
    const nodeCoordinates = Object.fromEntries(Object.entries(positions || {}).filter(([, position]) => validPosition(position)).map(([id, position]) => [id, { x: position.x, y: position.y }]));
    storage.setItem(foundationGraphLayoutKey(projectID, auditSignature), JSON.stringify({
      layout_version: FOUNDATION_GRAPH_LAYOUT_VERSION,
      foundation_audit_namespace: auditSignatureNamespace(auditSignature),
      saved_at: now(),
      node_coordinates: nodeCoordinates
    }));
    return true;
  } catch {
    return false;
  }
}

export function auditSignatureNamespace(value) {
  const text = String(value || '');
  let first = 0x811c9dc5;
  let second = 0x9e3779b9;
  for (let index = 0; index < text.length; index += 1) {
    const code = text.charCodeAt(index);
    first = Math.imul(first ^ code, 0x01000193);
    second = Math.imul(second ^ code, 0x85ebca6b);
  }
  return `${(first >>> 0).toString(16).padStart(8, '0')}${(second >>> 0).toString(16).padStart(8, '0')}`;
}

function relationshipGraphEdge(relationship) {
  const direction = normalizeGraphDirection(relationship.direction);
  const type = String(relationship.type || 'other');
  const status = String(relationship.status || 'planned');
  return {
    id: String(relationship.id),
    source: String(relationship.source_character_id),
    target: String(relationship.target_character_id),
    label: `${relationship.label || type} · ${directionLabel(direction)} · ${statusLabel(status)}`,
    data: { direction, type, status, planned: true },
    markerStart: direction === 'bidirectional' ? 'arrow' : undefined,
    markerEnd: direction === 'directed' || direction === 'bidirectional' ? 'arrow' : undefined,
    style: { strokeDasharray: status === 'planned' ? '7 4' : status === 'broken' ? '2 5' : undefined }
  };
}

function uniqueByStableID(characters) {
  const seen = new Set();
  return characters.filter((character) => {
    const id = String(character?.id || '');
    if (!id || seen.has(id)) return false;
    seen.add(id);
    return true;
  }).map((character) => ({ ...character, id: String(character.id) })).sort((left, right) => left.id.localeCompare(right.id));
}

function coreCastImportance(coreCast) {
  return new Map((coreCast?.members || []).map((member) => [String(member?.character?.id || ''), String(member?.importance || '')]).filter(([id]) => id));
}

function normalizeFilters(filters) {
  return Object.fromEntries(Object.keys(defaultRelationshipGraphFilters).map((key) => [key, String(filters?.[key] || '').trim().toLocaleLowerCase()]));
}

function matchesCharacter(character, filters, importanceByID) {
  const searchText = [character.name, ...(character.aliases || []), character.id].join(' ').toLocaleLowerCase();
  const importance = (importanceByID.get(character.id) || '').toLocaleLowerCase();
  const tier = String(character.tier || '').toLocaleLowerCase();
  const faction = String(character.faction || '').toLocaleLowerCase();
  return (!filters.search || searchText.includes(filters.search)) && (!filters.tier || tier === filters.tier || importance === filters.tier) && (!filters.faction || faction === filters.faction);
}

function matchesRelationship(relationship, filters) {
  return (!filters.relationshipType || String(relationship.type || '').toLocaleLowerCase() === filters.relationshipType) &&
    (!filters.relationshipStatus || String(relationship.status || '').toLocaleLowerCase() === filters.relationshipStatus);
}

function isHighImportance(character, importanceByID) {
  return highImportance.has(String(importanceByID.get(character.id) || '').toLocaleLowerCase()) || highImportance.has(String(character.tier || '').toLocaleLowerCase());
}

function validPosition(position) {
  return position && Number.isFinite(position.x) && Number.isFinite(position.y) ? { x: position.x, y: position.y } : null;
}

function directionLabel(direction) {
  return ({ directed: '单向', bidirectional: '双向', undirected: '无向' })[direction];
}

function statusLabel(status) {
  return ({ planned: '计划', active: '生效', strained: '紧张', broken: '破裂', resolved: '解决' })[status] || status;
}
